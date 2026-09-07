package main

// account_health_probe_request_outbox 消费装配（去跨进程战役第二刀）。原
// jobs internalapi loopback 派发 handler（worker_health_dispatch.go +
// internal/internalapi/healthdispatch.go，账户健康检查派发签名域）已随网关
// HTTP 桥删除；gateway 现在把每次健康检查派发写成业务库 outbox 行（gateway
// cmd chain_request_failure_health.go 的 writer，DDL 与本文件 schema 逐字
// 一致），J1 Runner 每 runCycle 开头 drain（internal/accounthealth
// outbox_drain.go）。
//
// 本文件只提供三件消费依赖：
//   - store：pending 行 claim（普通 SELECT，不改状态；jobs owner lease 是
//     唯一防双跑机制）+ consumed 幂等落位；
//   - boundary：迁移自被删 healthDispatchBoundary 的账户 J1 冻结事实读取
//     （accounts.config_revision/dispatch_revision +
//     account_health_jobs_input_versions.current_version）；
//   - settler：迁移自被删 healthDispatchSourceFenceSettler 的 Redis
//     circuitstore fence 结算（SettleDispatchedBySourceFence，state=unknown）。
//
// J1 未启用时 drain 保持未装配（消费面静默跳过，不伪装消费）。

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/accounthealth"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/circuitstore"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/speedfirstrepo"
)

// healthProbeOutboxSchema 与 gateway cmd chain_request_failure_health.go 的
// chainProbeRequestOutboxSchema 逐字一致（两侧运行时幂等建表，
// record_maintenance_jobs 先例：Go-owned 交接关系不占用生产 migration
// catalog）。
const healthProbeOutboxSchema = `CREATE TABLE IF NOT EXISTS account_health_probe_request_outbox (
  request_id TEXT PRIMARY KEY,
  account_id TEXT NOT NULL,
  reason TEXT NOT NULL,
  trace_id TEXT NOT NULL DEFAULT '',
  source_fence TEXT NOT NULL DEFAULT '',
  deadline_at TEXT NOT NULL,
  status TEXT NOT NULL CHECK (status IN ('pending', 'consumed')),
  consumed_at TEXT,
  available_at TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  CHECK ((status = 'consumed' AND consumed_at IS NOT NULL) OR (status = 'pending' AND consumed_at IS NULL))
)`

const healthProbeOutboxIndex = `CREATE INDEX IF NOT EXISTS idx_account_health_probe_request_outbox_pending
  ON account_health_probe_request_outbox(status, available_at, created_at, request_id)`

// EnsureHealthProbeOutboxSchema 幂等建表 + 建索引（消费装配前调用一次；
// gateway writer 先建表时以 IF NOT EXISTS 收敛）。
func EnsureHealthProbeOutboxSchema(ctx context.Context, business *businessDB) error {
	if business == nil || business.db == nil {
		return errors.New("account_health_probe_request_outbox 业务库句柄缺失")
	}
	if _, err := business.db.ExecContext(ctx, business.bind(healthProbeOutboxSchema)); err != nil {
		return err
	}
	_, err := business.db.ExecContext(ctx, business.bind(healthProbeOutboxIndex))
	return err
}

// healthProbeOutboxStore 实现 accounthealth.ProbeRequestOutboxStore（业务库
// 双模）。
type healthProbeOutboxStore struct {
	business *businessDB
}

// ClaimPendingProbeRequests 读取 pending 且 available_at 已到的行。普通
// SELECT 不加锁：单 owner lease 防双跑，行状态只被 CompleteProbeRequest
// 幂等推进，进程崩溃后未消费行自然回到下一周期（HasRequest 幂等防重复
// outcome）。
func (s healthProbeOutboxStore) ClaimPendingProbeRequests(ctx context.Context, limit int, now time.Time) ([]accounthealth.ProbeOutboxRow, error) {
	rows, err := s.business.db.QueryContext(ctx, s.business.bind(`SELECT request_id, account_id, reason, source_fence, deadline_at
		FROM `+s.business.table("account_health_probe_request_outbox")+`
		WHERE status = 'pending' AND available_at <= ?
		ORDER BY created_at, request_id
		LIMIT ?`), now.UTC().Format(time.RFC3339Nano), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]accounthealth.ProbeOutboxRow, 0, limit)
	for rows.Next() {
		var (
			requestID   string
			accountID   string
			reason      string
			sourceFence sql.NullString
			deadlineAt  string
		)
		if err := rows.Scan(&requestID, &accountID, &reason, &sourceFence, &deadlineAt); err != nil {
			return nil, err
		}
		row := accounthealth.ProbeOutboxRow{RequestID: requestID, AccountID: accountID, Reason: reason}
		fence, err := accounthealth.ParseProbeOutboxSourceFence(sourceFence.String)
		if err != nil {
			return nil, err
		}
		row.SourceFence = fence
		if deadlineAt != "" {
			parsed, err := time.Parse(time.RFC3339Nano, deadlineAt)
			if err != nil {
				return nil, err
			}
			row.Deadline = parsed
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// CompleteProbeRequest 把行幂等置为 consumed；0 行更新表示先前已消费。
func (s healthProbeOutboxStore) CompleteProbeRequest(ctx context.Context, requestID string, now time.Time) (bool, error) {
	result, err := s.business.db.ExecContext(ctx, s.business.bind(`UPDATE `+s.business.table("account_health_probe_request_outbox")+`
		SET status = 'consumed', consumed_at = ?, updated_at = ?
		WHERE request_id = ? AND status = 'pending'`), now.UTC().Format(time.RFC3339Nano), now.UTC().Format(time.RFC3339Nano), requestID)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected > 0, nil
}

// healthProbeBoundary 是 accounthealth.ProbeRequestBoundary 的业务库实现：
// SQL 迁移自被删 worker_health_dispatch.go healthDispatchBoundary（对齐 Node
// account-health-jobs-input.repository.ts 与 input-version repository 的
// 正整数断言）。任一事实缺失或非法即 ok=false（账户不在 J1 冻结范围）。
// 完整账户资格链（协议/状态/绑定）由消费端 runExplicitRequest 的 input_stale
// 判定兜底，此处不复制网关域读取链。
type healthProbeBoundary struct{ business *businessDB }

func (b healthProbeBoundary) CurrentProbeInput(ctx context.Context, accountID string) (configRevision, dispatchRevision, inputVersion int64, ok bool, err error) {
	var currentConfig, currentDispatch sql.NullInt64
	err = b.business.db.QueryRowContext(ctx, b.business.bind("SELECT config_revision, dispatch_revision FROM "+b.business.table("accounts")+" WHERE id = ? AND deleted_at IS NULL"), accountID).Scan(&currentConfig, &currentDispatch)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, 0, false, nil
	}
	if err != nil {
		return 0, 0, 0, false, err
	}
	if currentConfig.Int64 < 1 || currentDispatch.Int64 < 1 {
		return 0, 0, 0, false, nil
	}
	var currentVersion sql.NullInt64
	err = b.business.db.QueryRowContext(ctx, b.business.bind("SELECT current_version FROM "+b.business.table("account_health_jobs_input_versions")+" WHERE account_id = ?"), accountID).Scan(&currentVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, 0, false, nil
	}
	if err != nil {
		return 0, 0, 0, false, err
	}
	if currentVersion.Int64 < 1 {
		return 0, 0, 0, false, nil
	}
	return currentConfig.Int64, currentDispatch.Int64, currentVersion.Int64, true, nil
}

// wireHealthProbeOutboxDrain 装配 outbox 消费面（业务库 boundary + store +
// Redis fence 结算）。返回 drain；J1 未启用时返回 nil drain。装配失败不阻塞
// worker 启动：drain 缺席时 runCycle 跳过 outbox（gateway 行保持 pending，
// 等同原派发能力未装配的降级），调用方记 warn。
func (a *workerAssembly) wireHealthProbeOutboxDrain(getenv func(string) string) (*accounthealth.ProbeRequestDrain, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	config, err := accounthealth.LoadConfig(getenv)
	if err != nil {
		return nil, err
	}
	if !config.Enabled {
		// jobs 未拥有 J1（合法状态）：不消费 outbox 行。
		return nil, nil
	}
	business, err := openBusinessDB(a, "health-probe-outbox")
	if err != nil {
		return nil, err
	}
	if err := EnsureHealthProbeOutboxSchema(context.Background(), business); err != nil {
		_ = business.close()
		return nil, err
	}
	a.addCloser(business.close)
	settler, settlerCloser, settlerErr := a.wireHealthProbeFenceSettler()
	if settlerErr != nil {
		return nil, settlerErr
	}
	if settlerCloser != nil {
		a.addCloser(settlerCloser)
	}
	return &accounthealth.ProbeRequestDrain{
		Store:       healthProbeOutboxStore{business: business},
		Boundary:    healthProbeBoundary{business: business},
		SettleFence: settler,
		Limit:       config.DirectInputLimit,
	}, nil
}

// wireHealthProbeFenceSettler 迁移自被删 healthDispatchSourceFenceSettler：
// 复用账户电路运行态的 Redis 键空间（与 worker_circuit_jobs.go 同
// URL/namespace 约定）。Redis 未配置或 namespace 非法时返回 nil settler
// （fence 不结算，被删桥中该失败亦为 warn 语义），closer 为 nil。
func (a *workerAssembly) wireHealthProbeFenceSettler() (accounthealth.ProbeSourceFenceSettler, func() error, error) {
	if strings.TrimSpace(a.config.RedisStateURL) == "" || !speedfirstrepo.ValidSpeedFirstNamespace(a.config.RedisNamespace) {
		a.logger.Warn("账户健康探针 outbox 的 source fence 结算不可用（缺 JUHE_AI_REDIS_STATE_URL 或 namespace 非法）",
			"event", "account_health_probe_outbox_fence_settler_unavailable")
		return nil, nil, nil
	}
	store, err := circuitstore.NewProbeStateStore(a.config.RedisStateURL, a.config.RedisNamespace, nil)
	if err != nil {
		return nil, nil, err
	}
	settler := func(ctx context.Context, fence accounthealth.SourceFence, state string) error {
		_, err := store.SettleDispatchedBySourceFence(ctx, fence.RuntimeKey, fence.ProbeGeneration, circuitstore.ProbeSourceFence{
			StateKey:         fence.StateKey,
			AccountID:        fence.AccountID,
			SourceGeneration: fence.SourceGeneration,
			SourceFenceID:    fence.SourceFenceID,
		}, state, nil)
		return err
	}
	return settler, store.Close, nil
}
