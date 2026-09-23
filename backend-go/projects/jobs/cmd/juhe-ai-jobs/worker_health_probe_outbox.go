package main

// account_health_probe_request_outbox 消费装配（去跨进程战役第二刀）。原
// jobs internalapi loopback 派发 handler（worker_health_dispatch.go +
// internal/internalapi/healthdispatch.go，账户健康检查派发签名域）已随网关
// HTTP 桥删除；gateway 现在把每次健康检查派发写成业务库 outbox 行（gateway
// cmd chain_request_failure_health.go 的 writer，DDL 与本文件 schema 逐字
// 一致），J1 Runner 每 runCycle 开头 drain（internal/accounthealth
// outbox_drain.go）。
//
// 本文件提供四件消费/清理依赖：
//   - store：pending 行 claim（普通 SELECT，不加锁；jobs owner lease 是
//     唯一防双跑机制）+ 处理成功即删行（幂等 DELETE，record_maintenance_jobs
//     先例；幂等键在 juhe_jobs.account_health_outcomes）。确定性损坏行
//     （source_fence/deadline_at 文本无法解析）在 claim 内按已处理收敛出队，
//     不让毒丸行卡死整个 J1 drain；
//   - boundary：迁移自被删 healthDispatchBoundary 的账户 J1 冻结事实读取
//     （accounts.config_revision/dispatch_revision +
//     account_health_jobs_input_versions.current_version）；
//   - settler：迁移自被删 healthDispatchSourceFenceSettler 的 Redis
//     circuitstore fence 结算（SettleDispatchedBySourceFence，state=unknown）；
//   - pruner：常驻保留清理（独立于 J1 Runner，J1 关闭也要跑）：按节拍删除
//     created_at 早于保留期的行——无论 status。过期 pending 行删除等价旧
//     HTTP 时代的 rejected 弃置（outcome 永不写入，HasRequest 幂等保证不会
//     产生部分 outcome），有界且无信息丢失；gateway 写侧不删行，消费前堆积
//     与历史遗留 consumed 行都由保留期收敛。
//
// J1 未启用时 drain 保持未装配（消费面静默跳过，不伪装消费）；prune 仍装配，
// J1 关闭部署的 pending 堆积由保留期删除兜底。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/accounthealth"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/circuitstore"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/schedulejitter"
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
	// PostgreSQL 业务表位于 juhe_business；DDL 必须与读写路径使用同一
	// schema，不能依赖连接 search_path（否则会在 public 建表而查询不到）。
	tableName := business.table("account_health_probe_request_outbox")
	schema := strings.Replace(healthProbeOutboxSchema,
		"CREATE TABLE IF NOT EXISTS account_health_probe_request_outbox",
		"CREATE TABLE IF NOT EXISTS "+tableName, 1)
	index := strings.Replace(healthProbeOutboxIndex,
		"ON account_health_probe_request_outbox(",
		"ON "+tableName+"(", 1)
	if _, err := business.db.ExecContext(ctx, business.bind(schema)); err != nil {
		return err
	}
	_, err := business.db.ExecContext(ctx, business.bind(index))
	return err
}

// healthProbeOutboxStore 实现 accounthealth.ProbeRequestOutboxStore（业务库
// 双模）。
type healthProbeOutboxStore struct {
	business *businessDB
	// logger 用于确定性损坏行的隔离出队告警；nil（测试手工构造 store）时静默。
	logger *slog.Logger
}

// ClaimPendingProbeRequests 读取 pending 且 available_at 已到的行。普通
// SELECT 不加锁：单 owner lease 防双跑，行状态只被 CompleteProbeRequest
// 幂等推进，进程崩溃后未消费行自然回到下一周期（HasRequest 幂等防重复
// outcome）。确定性损坏行（source_fence 非 JSON、deadline_at 非 RFC3339）
// 重试永远不会成功，保持 pending 会让每次 claim 整体失败、owner lease 反复
// 释放（2026-09-23 实测 mockdata 占位行毒丸）：这里按已处理收敛——记 warn
// 后幂等出队，不阻塞其余行；SQL/扫描级错误仍整体上抛（下一周期重试）。
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
	// 损坏行在行游标关闭后统一出队，不与同一张表的读游标交叠写入。
	var corruptRequestIDs []string
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
		fence, fenceErr := accounthealth.ParseProbeOutboxSourceFence(sourceFence.String)
		if fenceErr != nil {
			corruptRequestIDs = append(corruptRequestIDs, requestID)
			s.warnCorruptRow(requestID, accountID, "source_fence", fenceErr)
			continue
		}
		row.SourceFence = fence
		if deadlineAt != "" {
			parsed, parseErr := time.Parse(time.RFC3339Nano, deadlineAt)
			if parseErr != nil {
				corruptRequestIDs = append(corruptRequestIDs, requestID)
				s.warnCorruptRow(requestID, accountID, "deadline_at", parseErr)
				continue
			}
			row.Deadline = parsed
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, corruptID := range corruptRequestIDs {
		if _, err := s.CompleteProbeRequest(ctx, corruptID, now); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// warnCorruptRow 输出确定性损坏行的隔离出队告警（logger 缺席时静默）。
func (s healthProbeOutboxStore) warnCorruptRow(requestID, accountID, column string, cause error) {
	if s.logger == nil {
		return
	}
	s.logger.Warn("probe_request outbox 行字段损坏，按已处理收敛出队",
		"event", "account_health_probe_outbox_row_corrupt",
		"requestId", requestID, "accountId", accountID, "column", column, "error", cause.Error())
}

// CompleteProbeRequest 以单条幂等 DELETE 表达「处理成功即出队」（保持既有
// `WHERE request_id = ? AND status = 'pending'` 幂等语义，沿用
// record_maintenance_jobs 的成功后删行先例，jobs internal
// recordmaintenance/queue.go Store.Delete）：outbox 不是审计面，真正的幂等键
// 在 juhe_jobs.account_health_outcomes（store.go HasRequest），HasRequest 幂等
// 已保证删行不会产生部分 outcome，置 consumed 后永久保留只会无界累积。行已被
// 并发/先前处理删除时 affected=0 返回 false（不视为错误）。now 参数保持
// ProbeRequestOutboxStore 接口签名稳定，删行语义下不再使用。
func (s healthProbeOutboxStore) CompleteProbeRequest(ctx context.Context, requestID string, now time.Time) (bool, error) {
	result, err := s.business.db.ExecContext(ctx, s.business.bind(`DELETE FROM `+s.business.table("account_health_probe_request_outbox")+`
		WHERE request_id = ? AND status = 'pending'`), requestID)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected > 0, nil
}

// CountPendingProbeRequests 实现 accounthealth.ProbeRequestBacklogCounter
// （D 任务③堆积告警的廉价只读面）：单条聚合查询返回 pending 行总数与最旧行
// created_at（无 pending 行时 oldest 为零值）。created_at 解析失败（写侧异常
// 文本）不掩盖计数，oldest 退化为零值（告警省略年龄字段）。
func (s healthProbeOutboxStore) CountPendingProbeRequests(ctx context.Context) (int64, time.Time, error) {
	var (
		count  int64
		oldest sql.NullString
	)
	err := s.business.db.QueryRowContext(ctx, s.business.bind(`SELECT COUNT(*), MIN(created_at) FROM `+s.business.table("account_health_probe_request_outbox")+`
		WHERE status = 'pending'`)).Scan(&count, &oldest)
	if err != nil {
		return 0, time.Time{}, err
	}
	var oldestAt time.Time
	if oldest.Valid && oldest.String != "" {
		if parsed, parseErr := time.Parse(time.RFC3339Nano, oldest.String); parseErr == nil {
			oldestAt = parsed
		}
	}
	return count, oldestAt, nil
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

// healthProbeOutboxFace 聚合 outbox 消费与清理装配产物：装配成功时 drain 与
// pruner 均非 nil（J1 恒装配，drain 不再有 J1 门控缺席路径）。
type healthProbeOutboxFace struct {
	drain  *accounthealth.ProbeRequestDrain
	pruner *healthProbeOutboxPruner
}

// wireHealthProbeOutboxFace 装配 outbox 消费面 + 常驻 prune（业务库 boundary +
// store + Redis fence 结算 + 保留清理）。装配失败不阻塞 worker 启动：调用方
// warn 后 drain 缺席时 runCycle 跳过 outbox（gateway 行保持 pending，等同原
// 派发能力未装配的降级），prune 同样缺席。业务库打不开则整个面本来就不存在。
// J1 强制常开（2026-09-19 决策）：drain 恒装配，无 J1 门控缺席路径。
func (a *workerAssembly) wireHealthProbeOutboxFace(getenv func(string) string) (*healthProbeOutboxFace, error) {
	if getenv == nil {
		getenv = os.Getenv
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
	face := &healthProbeOutboxFace{
		pruner: &healthProbeOutboxPruner{
			business: business,
			retention: time.Duration(parseProbeOutboxRetentionDays(getenv, func(message string) {
				a.logger.Warn(message, "event", "account_health_probe_outbox_retention_invalid")
			})) * 24 * time.Hour,
			interval: defaultProbeOutboxPruneInterval,
			logger:   a.logger,
		},
	}
	// jobs 拥有 J1（恒开）：装配完整消费面。
	settler, settlerCloser, settlerErr := a.wireHealthProbeFenceSettler()
	if settlerErr != nil {
		return nil, settlerErr
	}
	if settlerCloser != nil {
		a.addCloser(settlerCloser)
	}
	// D 任务②③：drain 上限/并发与堆积告警阈值经 env 配置（非法回退默认
	// 并 warn，风格对齐保留天数 env）。
	warnInvalidEnv := func(message string) {
		a.logger.Warn(message, "event", "account_health_probe_outbox_env_invalid")
	}
	face.drain = &accounthealth.ProbeRequestDrain{
		Store:                healthProbeOutboxStore{business: business, logger: a.logger},
		Boundary:             healthProbeBoundary{business: business},
		SettleFence:          settler,
		Limit:                parseProbeOutboxBoundedInt(getenv, probeOutboxDrainLimitEnvVar, defaultProbeOutboxDrainLimit, minProbeOutboxDrainLimit, maxProbeOutboxDrainLimit, warnInvalidEnv),
		Concurrency:          parseProbeOutboxBoundedInt(getenv, probeOutboxDrainConcurrencyEnvVar, defaultProbeOutboxDrainConcurrency, minProbeOutboxDrainConcurrency, maxProbeOutboxDrainConcurrency, warnInvalidEnv),
		BacklogWarnThreshold: parseProbeOutboxBoundedInt(getenv, probeOutboxBacklogWarnEnvVar, defaultProbeOutboxBacklogWarnThreshold, minProbeOutboxBacklogWarnThreshold, maxProbeOutboxBacklogWarnThreshold, warnInvalidEnv),
	}
	return face, nil
}

// probeOutboxRetentionEnvVar 是 outbox 保留天数 env（默认 7 天，1..365，
// 非法值取默认并 warn）。
const probeOutboxRetentionEnvVar = "JUHE_AI_ACCOUNT_HEALTH_PROBE_OUTBOX_RETENTION_DAYS"

// D 任务②③的 outbox 消费面 env：
//   - DRAIN_LIMIT：单周期 claim 上限（默认 256，16..4096）；
//   - DRAIN_CONCURRENCY：行消费有界并发（默认 2，1..8，保守起步）；
//   - BACKLOG_WARN：drain 后 pending 堆积告警阈值（默认 1000，1..1000000）。
const (
	probeOutboxDrainLimitEnvVar       = "JUHE_AI_ACCOUNT_HEALTH_PROBE_OUTBOX_DRAIN_LIMIT"
	probeOutboxDrainConcurrencyEnvVar = "JUHE_AI_ACCOUNT_HEALTH_PROBE_OUTBOX_DRAIN_CONCURRENCY"
	probeOutboxBacklogWarnEnvVar      = "JUHE_AI_ACCOUNT_HEALTH_PROBE_OUTBOX_BACKLOG_WARN"
)

const (
	defaultProbeOutboxRetentionDays = 7
	minProbeOutboxRetentionDays     = 1
	maxProbeOutboxRetentionDays     = 365
	// defaultProbeOutboxPruneInterval 对齐仓库既有 maintenance 循环节拍量级
	// （jobregistry 1h 级任务）；实际唤醒经 schedulejitter 抖动。
	defaultProbeOutboxPruneInterval = time.Hour

	// drain 上限默认 256（D 任务②由 64 上调）：派发风暴时原 64 上限使溢出
	// 行在消费前过期；上限与 accounthealth 包内 defaultProbeOutboxDrainLimit
	// 兜底同值。
	defaultProbeOutboxDrainLimit = 256
	minProbeOutboxDrainLimit     = 16
	maxProbeOutboxDrainLimit     = 4096
	// 并发默认 2 保守起步（快探针串行曾是周期扫描的拖累；行间无顺序依赖，
	// 上限 8 与 ListProjectionWorkerConcurrency 档位一致）。
	defaultProbeOutboxDrainConcurrency     = 2
	minProbeOutboxDrainConcurrency         = 1
	maxProbeOutboxDrainConcurrency         = 8
	defaultProbeOutboxBacklogWarnThreshold = 1000
	minProbeOutboxBacklogWarnThreshold     = 1
	maxProbeOutboxBacklogWarnThreshold     = 1_000_000
)

// parseProbeOutboxBoundedInt 是 outbox 家族整数 env 的通用解析：空/未设置取
// 默认；非整数或越界回退默认并回调 warn（沿用 parseProbeOutboxRetentionDays
// 的不 fail-closed 风格——消费面调优参数配置错误不应让 drain/prune 整体缺席）。
func parseProbeOutboxBoundedInt(getenv func(string) string, name string, fallback, minimum, maximum int, warn func(message string)) int {
	raw := ""
	if getenv != nil {
		raw = strings.TrimSpace(getenv(name))
	}
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < minimum || value > maximum {
		if warn != nil {
			warn(fmt.Sprintf("%s 必须是 %d..%d 的整数，回退默认 %d", name, minimum, maximum, fallback))
		}
		return fallback
	}
	return value
}

// parseProbeOutboxRetentionDays 解析保留天数 env：空/未设置取默认；超出
// 1..365 或非整数取默认并回调 warn（不 fail closed——保留期配置错误不应把
// 写侧变成无界堆积，回退默认即恢复有界）。
func parseProbeOutboxRetentionDays(getenv func(string) string, warn func(message string)) int {
	raw := ""
	if getenv != nil {
		raw = strings.TrimSpace(getenv(probeOutboxRetentionEnvVar))
	}
	if raw == "" {
		return defaultProbeOutboxRetentionDays
	}
	days, err := strconv.Atoi(raw)
	if err != nil || days < minProbeOutboxRetentionDays || days > maxProbeOutboxRetentionDays {
		if warn != nil {
			warn(fmt.Sprintf("%s 必须是 %d..%d 的整数，回退默认 %d 天",
				probeOutboxRetentionEnvVar, minProbeOutboxRetentionDays, maxProbeOutboxRetentionDays, defaultProbeOutboxRetentionDays))
		}
		return defaultProbeOutboxRetentionDays
	}
	return days
}

// healthProbeOutboxPruner 是 outbox 保留清理的常驻小组件：独立于 J1 Runner
// 与 scheduler（supervisor.Component 承载），J1 关闭部署也保持有界。DELETE
// 幂等（无行即无事），即使多副本重叠执行也不产生副作用。
type healthProbeOutboxPruner struct {
	business  *businessDB
	retention time.Duration
	interval  time.Duration
	logger    *slog.Logger
}

// pruneOnce 删除 created_at 早于保留期的行（无论 status），返回删除行数。
// 时间比较沿用消费侧同款文本序（gateway writer 固定 9 位小数 UTC 书写，
// 字典序与时间序一致；business.bind 覆盖 PG $n 方言，TEXT 列两侧一致）。
func (p healthProbeOutboxPruner) pruneOnce(ctx context.Context, now time.Time) (int64, error) {
	cutoff := now.UTC().Add(-p.retention).Format(time.RFC3339Nano)
	result, err := p.business.db.ExecContext(ctx, p.business.bind(`DELETE FROM `+p.business.table("account_health_probe_request_outbox")+`
		WHERE created_at < ?`), cutoff)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// Run 常驻 prune 循环：启动先跑一轮（J1 关闭部署的既有堆积在启动即开始
// 收敛），随后按 passive jitter 节拍重复（tablemonitor Runner 同款模式）。
// 单轮失败记 warn 不终止循环，下一节拍重试。
func (p healthProbeOutboxPruner) Run(ctx context.Context) error {
	p.pruneCycle(ctx)
	timer := time.NewTimer(schedulejitter.Delay(p.interval))
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			p.pruneCycle(ctx)
			timer.Reset(schedulejitter.Delay(p.interval))
		}
	}
}

func (p healthProbeOutboxPruner) pruneCycle(ctx context.Context) {
	deleted, err := p.pruneOnce(ctx, time.Now())
	if err != nil {
		p.logger.Warn("账户健康探针 outbox 保留清理失败；下一节拍重试",
			"event", "account_health_probe_outbox_prune_failed", "error", err.Error())
		return
	}
	if deleted > 0 {
		p.logger.Info("账户健康探针 outbox 保留清理完成",
			"event", "account_health_probe_outbox_pruned", "deleted", deleted,
			"retentionDays", int(p.retention/(24*time.Hour)))
	}
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
