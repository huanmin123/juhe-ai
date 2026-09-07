package accounthealth

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

// account_health_probe_request_outbox 的消费面（去跨进程战役第二刀）：gateway
// 进程把原 loopback HMAC 派发桥（/v1/account-health-check/dispatch 派发面，
// 已删除）的每次探针派发写成业务库 outbox 行；J1 Runner 在每个 runCycle 开头
// drain pending 行，逐行复刻被删 HTTP handler 的语义：
//   - boundary 读（账户 config/dispatch revision + J1 input epoch）；
//   - 不在 J1 冻结范围 → 不发布探针，仍结算 source fence = unknown；
//   - 在范围 → 组装显式 ProbeRequest 走 runExplicitRequest 全链（HasRequest
//     幂等、行内 deadline、mutate_account=(source_fence==nil)、
//     request_failure_health 防抖豁免、AppendOutcome）。
//
// claim / 出队策略：jobs 是唯一消费者（store 的 owner lease 是唯一防双
// 跑机制），claim 是普通 SELECT（不加锁），处理成功后按
// `DELETE ... WHERE request_id = ? AND status = 'pending'` 幂等出队（业务库
// 实现沿用 record_maintenance_jobs 的成功后删行先例，outbox 不是审计面，
// 幂等键在本包 account_health_outcomes.HasRequest）。进程在处理中途崩溃时
// 行保持 pending，下一周期重跑：HasRequest 幂等防重复 outcome，fence 结算
// 幂等。逐行失败不阻塞其余行（记录 firstErr，行保持 pending 下周期重试，
// 长期滞留由业务库侧保留期清理兜底）。

// ProbeOutboxRow 是 outbox probe_request 行的窄投影。
type ProbeOutboxRow struct {
	RequestID   string
	AccountID   string
	Reason      string
	SourceFence *SourceFence // nil = 无 fence（mutate_account）
	Deadline    time.Time
}

// ProbeRequestOutboxStore 是 outbox 表的读侧窄口（业务库实现在组合根，
// gateway cmd 侧 writer 的 DDL 与之逐字一致）。
type ProbeRequestOutboxStore interface {
	// ClaimPendingProbeRequests 返回 pending 且 available_at 已到的行（最多
	// limit 行，按 created_at、request_id 稳定排序）。不改行状态。
	ClaimPendingProbeRequests(ctx context.Context, limit int, now time.Time) ([]ProbeOutboxRow, error)
	// CompleteProbeRequest 把行幂等出队（处理成功即删行；业务库实现沿用
	// record_maintenance_jobs 先例）；返回是否发生了本次删除（行已被并发/
	// 先前处理删除时返回 false 且不视为错误）。
	CompleteProbeRequest(ctx context.Context, requestID string, now time.Time) (bool, error)
}

// ProbeRequestBoundary 读账户 J1 冻结事实（被删 jobs internalapi HTTP 派发
// boundary 的窄投影；ok=false 表示账户不在 J1 冻结范围或 input epoch 缺失）。
type ProbeRequestBoundary interface {
	CurrentProbeInput(ctx context.Context, accountID string) (configRevision, dispatchRevision, inputVersion int64, ok bool, err error)
}

// ProbeSourceFenceSettler 结算 source fence（Redis 电路运行态；
// state 取 "unknown"）。
type ProbeSourceFenceSettler func(ctx context.Context, fence SourceFence, state string) error

// ProbeRequestDrain 聚合 outbox 消费依赖；nil Store/Boundary 表示通道未装配
// （J1 未启用或 worker 业务库缺失），drain 静默跳过。
type ProbeRequestDrain struct {
	Store       ProbeRequestOutboxStore
	Boundary    ProbeRequestBoundary
	SettleFence ProbeSourceFenceSettler
	// Limit 是单周期 claim 上限（<=0 时取 defaultProbeOutboxDrainLimit）。
	Limit int
}

const defaultProbeOutboxDrainLimit = 64

// SetProbeRequestDrain 注入 outbox 消费面（组合根在 J1 runner 与 worker 业务
// 库都就绪后调用；不注入则 runCycle 不消费 outbox——J1 未启用时的合法形态）。
func (r *Runner) SetProbeRequestDrain(drain *ProbeRequestDrain) {
	r.probeDrain = drain
}

// drainProbeRequestOutbox 消费 pending probe_request 行；返回首个错误（其余
// 行仍处理完）。
func (r *Runner) drainProbeRequestOutbox(ctx context.Context, lease OwnerLease) error {
	if r.probeDrain == nil || r.probeDrain.Store == nil || r.probeDrain.Boundary == nil {
		return nil
	}
	now := r.cfg.Now().UTC()
	limit := r.probeDrain.Limit
	if limit <= 0 {
		limit = defaultProbeOutboxDrainLimit
	}
	rows, err := r.probeDrain.Store.ClaimPendingProbeRequests(ctx, limit, now)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}
	var firstErr error
	for _, row := range rows {
		if err := r.consumeProbeOutboxRow(ctx, lease, row, now); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			r.logger.Warn("消费账户健康探针 outbox 行失败；行保持 pending 等待下周期",
				"event", "account_health_probe_outbox_row_failed",
				"requestId", row.RequestID, "accountId", row.AccountID, "error", err.Error())
			continue
		}
		if _, err := r.probeDrain.Store.CompleteProbeRequest(ctx, row.RequestID, r.cfg.Now().UTC()); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// consumeProbeOutboxRow 对单行复刻被删 HTTP 派发 handler 的发布语义
// （internalapi.DispatchAccountHealthCheckWithOutcome）：boundary 读 → 范围外
// 结算 fence=unknown → 范围内组装显式请求走 runExplicitRequest 全链。
// 收敛语义：确定性行损坏（字段缺失、fence 与当前 config revision 不一致、
// deadline 缺失）记 warn 后按已处理收敛（返回 nil，调用方删行出队）——
// 这些失败重试永远不会成功，保持 pending 只会形成毒丸；瞬态错误（业务库 /
// Redis / 探针执行错误）返回 err，行保持 pending 下周期重试。
func (r *Runner) consumeProbeOutboxRow(ctx context.Context, lease OwnerLease, row ProbeOutboxRow, now time.Time) error {
	if strings.TrimSpace(row.RequestID) == "" || strings.TrimSpace(row.AccountID) == "" || strings.TrimSpace(row.Reason) == "" {
		r.logger.Warn("probe_request outbox 行字段缺失，按已处理收敛",
			"event", "account_health_probe_outbox_row_invalid",
			"requestId", row.RequestID, "accountId", row.AccountID)
		return nil
	}
	configRevision, dispatchRevision, inputVersion, ok, err := r.probeDrain.Boundary.CurrentProbeInput(ctx, row.AccountID)
	if err != nil {
		return err
	}
	if !ok || inputVersion < 1 {
		// 冻结 J1 范围之外的账户：跳过探针发布，仍结算 source fence = unknown。
		if row.SourceFence != nil && r.probeDrain.SettleFence != nil {
			return r.probeDrain.SettleFence(ctx, *row.SourceFence, "unknown")
		}
		return nil
	}
	if row.SourceFence != nil && row.SourceFence.ConfigRevision != configRevision {
		// 被删桥的 buildProbeRequestPayload 同款断言：fence 与当前账户
		// revision 不一致时派发失败（原 HTTP 500；gateway 协调器已按
		// rejected 结算本地 fence）。不发布探针、不写 outcome，确定性失败
		// 按已处理收敛。
		r.logger.Warn("probe_request source fence 与账户 config revision 不一致，放弃派发",
			"event", "account_health_probe_outbox_row_stale_fence",
			"requestId", row.RequestID, "accountId", row.AccountID,
			"fenceConfigRevision", row.SourceFence.ConfigRevision, "configRevision", configRevision)
		return nil
	}
	if row.Deadline.IsZero() {
		r.logger.Warn("probe_request outbox 行缺少 deadline，按已处理收敛",
			"event", "account_health_probe_outbox_row_invalid",
			"requestId", row.RequestID, "accountId", row.AccountID)
		return nil
	}
	var input Input
	if r.directInputReader != nil {
		inputs, err := r.directInputReader.LoadAccount(ctx, row.AccountID)
		if err != nil {
			return err
		}
		// 与 runCycle 的 request 文件消费一致：候选缺失/不唯一时保持零值，
		// 由 runExplicitRequest 的 input_stale 判定落 stale 终态。
		if len(inputs) == 1 {
			input = inputs[0]
		}
	}
	request := ProbeRequest{
		RequestID:        row.RequestID,
		AccountID:        row.AccountID,
		Reason:           row.Reason,
		InputVersion:     inputVersion,
		ConfigRevision:   configRevision,
		DispatchRevision: dispatchRevision,
		Deadline:         row.Deadline,
		MutateAccount:    row.SourceFence == nil,
		SourceFence:      row.SourceFence,
	}
	return r.runExplicitRequest(ctx, lease, input, request, now)
}

// ParseProbeOutboxSourceFence 解析行内 source_fence JSON（gateway writer 投影
// 的 J1 request-file source_fence 字段名）；空文本返回 nil（无 fence）。
func ParseProbeOutboxSourceFence(raw string) (*SourceFence, error) {
	normalized := strings.TrimSpace(raw)
	if normalized == "" {
		return nil, nil
	}
	var fence SourceFence
	if err := json.Unmarshal([]byte(normalized), &fence); err != nil {
		return nil, err
	}
	return &fence, nil
}
