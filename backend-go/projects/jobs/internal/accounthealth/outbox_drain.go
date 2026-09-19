package accounthealth

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
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
	// Limit 是单周期 claim 上限（<=0 时取 defaultProbeOutboxDrainLimit；
	// 组合根经 env JUHE_AI_ACCOUNT_HEALTH_PROBE_OUTBOX_DRAIN_LIMIT 配置，
	// 默认 256、边界 16..4096）。
	Limit int
	// Concurrency 是行消费的有界并发 worker 数（<=1 保持既有串行语义；
	// 上限 clamp 到 maxProbeOutboxDrainConcurrency。组合根经 env
	// JUHE_AI_ACCOUNT_HEALTH_PROBE_OUTBOX_DRAIN_CONCURRENCY 配置，默认 2、
	// 边界 1..8）。行间无顺序依赖：幂等键是逐行 RequestID（HasRequest），
	// 账户级状态收敛由 outcome 投影面的 epoch/fence 校验兜底。
	Concurrency int
	// BacklogWarnThreshold 是 drain 后 pending 堆积告警阈值（<=0 关闭告警；
	// 组合根经 env JUHE_AI_ACCOUNT_HEALTH_PROBE_OUTBOX_BACKLOG_WARN 配置，
	// 默认 1000）。
	BacklogWarnThreshold int
}

// defaultProbeOutboxDrainLimit 是未配置（<=0）时的单周期 claim 上限兜底
// （组合根默认同值：D 任务②把原 64 上调至 256，缓解派发风暴下溢出行在
// 消费前过期的堆积）。
const defaultProbeOutboxDrainLimit = 256

// maxProbeOutboxDrainConcurrency 是行消费并发的硬上限（组合根 env 边界
// 1..8；包内 clamp 防御直接构造 ProbeRequestDrain 的调用方）。
const maxProbeOutboxDrainConcurrency = 8

// ProbeRequestBacklogCounter 是 outbox store 的可选只读能力：统计 pending
// 行数与最旧行创建时间（堆积告警用）。可选接口——既有测试 fake 与极简
// store 不必实现计数也能接入 drain。
type ProbeRequestBacklogCounter interface {
	// CountPendingProbeRequests 返回 pending 行总数与最旧行 created_at
	//（无 pending 行时 oldest 为零值）。
	CountPendingProbeRequests(ctx context.Context) (count int64, oldest time.Time, err error)
}

// probeOutboxBacklogWarnSuppressInterval 是堆积告警的频控窗口：同一窗口内
// 不重复告警，防日志风暴（D 任务③）。
const probeOutboxBacklogWarnSuppressInterval = 10 * time.Minute

// SetProbeRequestDrain 注入 outbox 消费面（组合根在 J1 runner 与 worker 业务
// 库都就绪后调用；不注入则 runCycle 不消费 outbox——J1 未启用时的合法形态）。
func (r *Runner) SetProbeRequestDrain(drain *ProbeRequestDrain) {
	r.probeDrain = drain
}

// drainProbeRequestOutbox 消费 pending probe_request 行；返回首个错误（其余
// 行仍处理完）。行消费经有界并发 worker 池（Concurrency<=1 时保持逐行串行
// 的既有语义）；行级 deadline 校验与失败隔离不变：单行失败保持 pending 并
// 记入 firstErr，不阻塞其余行。
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
	// 堆积可见性（D 任务③）：每个 claim 成功的周期（含 0 行——pending 行
	// 可能因 available_at 未到期而不被 claim，但堆积真实存在）在 drain 完成
	// 后廉价 COUNT pending 行，达到阈值时结构化告警（带频控）。
	defer r.warnProbeOutboxBacklog(ctx)
	if len(rows) == 0 {
		return nil
	}
	return r.consumeProbeOutboxRows(ctx, lease, rows, now)
}

// consumeProbeOutboxRows 以有界并发消费已 claim 的行。行间无顺序依赖：
// 每行是独立 ProbeRequest（幂等键 RequestID；HasRequest 防重复 outcome），
// 同账户并发探针的状态收敛由 outcome 投影面的 epoch/fence 校验兜底（既有
// 机制，与调度批量路径的并发语义一致）。ctx 取消时停止派发新行并等待在途
// worker 返回。
func (r *Runner) consumeProbeOutboxRows(ctx context.Context, lease OwnerLease, rows []ProbeOutboxRow, now time.Time) error {
	concurrency := r.probeDrain.Concurrency
	if concurrency > maxProbeOutboxDrainConcurrency {
		concurrency = maxProbeOutboxDrainConcurrency
	}
	if concurrency <= 1 || len(rows) == 1 {
		var firstErr error
		for _, row := range rows {
			if err := r.consumeOneProbeOutboxRow(ctx, lease, row, now); err != nil && firstErr == nil {
				firstErr = err
			}
		}
		return firstErr
	}
	var (
		wg       sync.WaitGroup
		errMu    sync.Mutex
		firstErr error
	)
	recordError := func(err error) {
		if err == nil {
			return
		}
		errMu.Lock()
		if firstErr == nil {
			firstErr = err
		}
		errMu.Unlock()
	}
	rowsCh := make(chan ProbeOutboxRow)
	for range concurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for row := range rowsCh {
				recordError(r.consumeOneProbeOutboxRow(ctx, lease, row, now))
			}
		}()
	}
	for _, row := range rows {
		select {
		case <-ctx.Done():
			close(rowsCh)
			wg.Wait()
			return context.Cause(ctx)
		case rowsCh <- row:
		}
	}
	close(rowsCh)
	wg.Wait()
	errMu.Lock()
	defer errMu.Unlock()
	return firstErr
}

// consumeOneProbeOutboxRow 消费单行：处理成功后幂等出队；处理失败记 warn 并
// 保持 pending（下周期重试），出队失败只记入返回错误（沿用既有语义）。
func (r *Runner) consumeOneProbeOutboxRow(ctx context.Context, lease OwnerLease, row ProbeOutboxRow, now time.Time) error {
	if err := r.consumeProbeOutboxRow(ctx, lease, row, now); err != nil {
		r.logger.Warn("消费账户健康探针 outbox 行失败；行保持 pending 等待下周期",
			"event", "account_health_probe_outbox_row_failed",
			"requestId", row.RequestID, "accountId", row.AccountID, "error", err.Error())
		return err
	}
	if _, err := r.probeDrain.Store.CompleteProbeRequest(ctx, row.RequestID, r.cfg.Now().UTC()); err != nil {
		return err
	}
	return nil
}

// warnProbeOutboxBacklog 在 drain 完成后统计 pending 堆积；达到阈值时输出
// 一条结构化 warn（pending 数、最旧行年龄），并做 10 分钟频控防日志风暴。
// store 未实现 ProbeRequestBacklogCounter 或阈值 <=0 时保持沉默（能力缺席
// 不构成告警条件）。
func (r *Runner) warnProbeOutboxBacklog(ctx context.Context) {
	if r.probeDrain == nil || r.probeDrain.BacklogWarnThreshold <= 0 {
		return
	}
	counter, ok := r.probeDrain.Store.(ProbeRequestBacklogCounter)
	if !ok {
		return
	}
	count, oldest, err := counter.CountPendingProbeRequests(ctx)
	if err != nil {
		r.logger.Warn("统计账户健康探针 outbox pending 堆积失败",
			"event", "account_health_probe_outbox_backlog_count_failed", "error", err.Error())
		return
	}
	if count < int64(r.probeDrain.BacklogWarnThreshold) {
		return
	}
	now := r.cfg.Now()
	r.mu.Lock()
	suppressed := now.Sub(r.backlogWarnedAt) < probeOutboxBacklogWarnSuppressInterval
	if !suppressed {
		r.backlogWarnedAt = now
	}
	r.mu.Unlock()
	if suppressed {
		return
	}
	attrs := []any{
		"event", "account_health_probe_outbox_backlog",
		"pending", count,
		"threshold", r.probeDrain.BacklogWarnThreshold,
	}
	if age := now.Sub(oldest).Seconds(); !oldest.IsZero() && age > 0 {
		attrs = append(attrs, "oldestAgeSeconds", int64(age))
	}
	r.logger.Warn("账户健康探针 outbox pending 堆积超过阈值；消费吞吐不足或派发风暴，请检查 J1 drain 上限/并发配置", attrs...)
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
