package main

// Request-failure account health-check dispatch assembly: the port of the
// archived response/request-failure-health-check.ts + the
// internal-api/account-health-check-dispatch.service.ts publish path.
//
// Node contract (request-failure-health-check.ts):
//   - trafficSource !== 'gateway' → no dispatch;
//   - one dispatched health check per request (the dispatched Symbol on the
//     express request throttles the candidate-failover loop and keeps the
//     failed-response and transport-failure branches from double firing);
//     the Go side keeps the mark per-request on GatewayRequest
//     (failureHealthCheckMark), so it is reclaimed with the request object
//     exactly like the Node WeakMap entry (BUG-0339: a process-global map
//     keyed by request pointers pinned Body.RawBody and grew the live heap);
//   - dispatchAccountHealthCheck(accountId, 'request_failure') publishes the
//     J1 probe request fact (internal-api service, fire-and-forget).
//
// Go two-process topology (去跨进程战役第二刀)：the gateway process cannot
// import the jobs internal-api package (module boundary), and Go processes
// never call each other over HTTP, so the publish rides the same durable
// channel pattern as record_maintenance_jobs：the gateway writes one
// account_health_probe_request_outbox row per dispatch (DB outbox, in-process
// INSERT, no network hop) and the jobs J1 Runner drains pending rows at the
// head of every runCycle. The loopback HMAC dispatch bridge (its own
// signature-domain constant and route) was removed with this slice; the wire
// payload semantics survive unchanged as row columns
// (version collapsed to the schema, accountId/reason/traceId/sourceFence).
//
// Row contract (the J1 projection the old bridge carried verbatim):
//   - request_id is the idempotency key (j1-<uuid>, jobs HasRequest dedupes);
//   - source_fence is the snake_case narrow projection (jobs
//     HealthCheckSourceFence / the J1 request-file source_fence field names),
//     empty string meaning no fence → mutate_account on the jobs side;
//   - deadline_at = now + JUHE_AI_BACKGROUND_ACCOUNT_HEALTH_CHECK_PROBE_DEADLINE_MS
//     (the same env the old jobs-side publisher read; same default 65000 and
//     [1000, 600000] range), written by the gateway so consumption needs no
//     second read of the env.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycodex"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

// chainProbeRequestOutboxTableName 是 gateway 写入、jobs J1 Runner 消费的
// probe_request outbox 交接表（record_maintenance_jobs 先例：Go-owned 交接
// 关系不占用生产 migration catalog，两侧各自运行时幂等建表）。
const chainProbeRequestOutboxTableName = "account_health_probe_request_outbox"

// chainProbeRequestOutboxSchema 与 jobs cmd 侧 worker_health_probe_outbox.go
// 的 healthProbeOutboxSchema 逐字一致（CREATE IF NOT EXISTS 幂等）。
const chainProbeRequestOutboxSchema = `CREATE TABLE IF NOT EXISTS account_health_probe_request_outbox (
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

const chainProbeRequestOutboxIndex = `CREATE INDEX IF NOT EXISTS idx_account_health_probe_request_outbox_pending
  ON account_health_probe_request_outbox(status, available_at, created_at, request_id)`

// chainProbeRequestOutboxTimeFormat 是 outbox 行时间列的固定书写格式（UTC、
// 固定 9 位小数）：字典序与时间序一致，jobs 消费侧按 status/available_at 做
// 文本比较与稳定排序；time.RFC3339Nano 可直接解析。
const chainProbeRequestOutboxTimeFormat = "2006-01-02T15:04:05.000000000Z07:00"

// probeSkippedDisabledDecision 是后台自动探测开关关闭（设计 §3：请求失败
// 二次确认属后台探测，关闭账户不投递）时的拒绝 decision code；与 jobs 消费
// 端的 probe_skipped_disabled 终态码同形。读取失败不改变既有派发行为——
// jobs 消费端的开关守卫是最终兜底。
const probeSkippedDisabledDecision = "probe_skipped_disabled"

// chainBackgroundProbeEnabledSQL 读账户生效的后台自动探测开关（授权实例取
// 来源账户值）：与 jobs 候选 SQL / freshness 门 / outbox boundary 守卫的
// CASE 投影逐字同形，四处共享同一生效语义。
const chainBackgroundProbeEnabledSQL = `SELECT CASE WHEN a.authorization_instance_authorization_id IS NULL THEN a.background_probe_enabled ELSE COALESCE(source.background_probe_enabled, 1) END
FROM juhe_business.accounts a
LEFT JOIN juhe_business.accounts source ON source.id = a.authorization_instance_source_account_id AND source.deleted_at IS NULL
WHERE a.id = ? AND a.deleted_at IS NULL`

// chainRequestFailureReason mirrors AccountHealthCheckTriggerReason
// 'request_failure' — the only reason this port dispatches.
const chainRequestFailureReason = "request_failure"

// chainProbeRequestOutboxWriter persists one health-check probe request row
// per dispatch (the in-process replacement of the removed loopback HMAC
// bridge). A nil writer or a nil DB keeps the dispatcher inert (dispatch
// reports input_unavailable without touching the database), which is the
// degraded contract for hand-assembled tests.
type chainProbeRequestOutboxWriter struct {
	db         *sql.DB
	deadlineMS int64
	now        func() time.Time

	mu        sync.Mutex
	ensured   bool
	ensureErr error
}

// newChainProbeRequestOutboxWriter builds the durable probe-request writer;
// deadlineMS <= 0 keeps the writer inert (input_unavailable), matching the
// old bridge's missing-assembly contract.
func newChainProbeRequestOutboxWriter(db *sql.DB, probeDeadlineMS int64) *chainProbeRequestOutboxWriter {
	return &chainProbeRequestOutboxWriter{db: db, deadlineMS: probeDeadlineMS, now: time.Now}
}

func (w *chainProbeRequestOutboxWriter) table() string {
	return "juhe_business." + chainProbeRequestOutboxTableName
}

// backgroundProbeDisabled 读账户生效的后台自动探测开关（设计 §3 投递端守卫
// 的一次最小 SELECT）。账户行不存在或读取失败都不构成「已关闭」（返回 false
// + err 语义分离），既有派发链与 jobs 消费端守卫承接。
func (w *chainProbeRequestOutboxWriter) backgroundProbeDisabled(ctx context.Context, accountID string) (bool, error) {
	var enabled sql.NullInt64
	err := w.db.QueryRowContext(ctx, w.bind(chainBackgroundProbeEnabledSQL), accountID).Scan(&enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return !(enabled.Valid && enabled.Int64 == 1), nil
}

func (w *chainProbeRequestOutboxWriter) bind(query string) string {
	var out strings.Builder
	index := 1
	for i := 0; i < len(query); i++ {
		if query[i] == '?' {
			out.WriteString("$" + fmt.Sprint(index))
			index++
			continue
		}
		out.WriteByte(query[i])
	}
	return out.String()
}

// ensureSchema 幂等建表 + 建索引（全新 Go-owned 交接表，无旧表补列；
// record_maintenance_jobs DurableDispatch.ensureSchema 同款 once 缓存）。
// PostgreSQL 业务表位于 juhe_business；DDL 与读写路径必须使用同一 schema，
// 不能依赖连接 search_path（否则会在 public 建表而 INSERT/查询不到，W32
// 同源缺陷；jobs 侧 EnsureHealthProbeOutboxSchema 同款限定先例）。
func (w *chainProbeRequestOutboxWriter) ensureSchema(ctx context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.ensured {
		return w.ensureErr
	}
	tableName := w.table()
	schema := strings.Replace(chainProbeRequestOutboxSchema,
		"CREATE TABLE IF NOT EXISTS "+chainProbeRequestOutboxTableName,
		"CREATE TABLE IF NOT EXISTS "+tableName, 1)
	index := strings.Replace(chainProbeRequestOutboxIndex,
		"ON "+chainProbeRequestOutboxTableName+"(",
		"ON "+tableName+"(", 1)
	if _, err := w.db.ExecContext(ctx, w.bind(schema)); err != nil {
		w.ensureErr = err
		return w.ensureErr
	}
	if _, err := w.db.ExecContext(ctx, w.bind(index)); err != nil {
		w.ensureErr = err
		return w.ensureErr
	}
	w.ensured = true
	return nil
}

// chainHealthDispatchSourceFence mirrors the J1 request-file source_fence
// narrow projection (snake_case); it marshals straight into the outbox row's
// source_fence JSON column.
type chainHealthDispatchSourceFence struct {
	StateKey         string `json:"state_key"`
	AccountID        string `json:"account_id"`
	SourceGeneration int64  `json:"source_generation"`
	SourceFenceID    string `json:"source_fence_id"`
	RuntimeKey       string `json:"runtime_key"`
	ProbeGeneration  int64  `json:"probe_generation"`
	ConfigRevision   int64  `json:"config_revision"`
}

// EnqueueProbeRequest mirrors the old bridge dispatch(): normalize + validate
// the account, project the fence and insert the row in-process. The insert is
// the entire fire-and-forget publish (no network, no HTTP timeout): the jobs
// drain absorbs delivery, and an insert failure degrades to the same
// rejected + warn contract the non-202 bridge responses produced.
func (w *chainProbeRequestOutboxWriter) EnqueueProbeRequest(ctx context.Context, accountID, reason, traceID string, sourceFence *chainHealthDispatchSourceFence) gatewaycodex.HealthCheckDispatchOutcome {
	normalizedID := strings.TrimSpace(accountID)
	if normalizedID == "" {
		return gatewaycodex.HealthCheckDispatchOutcome{Outcome: gatewaycodex.HealthDispatchRejected, DecisionCode: "dispatch_rejected"}
	}
	if w == nil || w.db == nil || w.deadlineMS <= 0 {
		return gatewaycodex.HealthCheckDispatchOutcome{Outcome: gatewaycodex.HealthDispatchRejected, DecisionCode: "input_unavailable"}
	}
	// 后台自动探测开关守卫（设计 §3：请求失败二次确认不投递）：关闭账户按
	// rejected 收敛且不写 outbox、不告警（真实请求失败路径，逐次打点只会
	// 形成噪音）；读取失败保持既有派发行为，jobs 消费端守卫兜底。
	disabled, err := w.backgroundProbeDisabled(ctx, normalizedID)
	if err != nil {
		slog.Warn("健康检查派发读取后台探测开关失败，保持既有派发行为",
			"event", "account_health_check_dispatch_flag_read_failed", "accountId", normalizedID, "error", err)
	} else if disabled {
		return gatewaycodex.HealthCheckDispatchOutcome{Outcome: gatewaycodex.HealthDispatchRejected, DecisionCode: probeSkippedDisabledDecision}
	}
	fenceJSON := ""
	if sourceFence != nil {
		encoded, err := json.Marshal(sourceFence)
		if err != nil {
			slog.Warn("健康检查派发 source fence 编码失败", "event", "account_health_check_dispatch_encode_failed", "error", err)
			return gatewaycodex.HealthCheckDispatchOutcome{Outcome: gatewaycodex.HealthDispatchRejected, DecisionCode: "dispatch_rejected"}
		}
		fenceJSON = string(encoded)
	}
	requestID := "j1-" + gatewaycodex.RandomUUID()
	now := w.now().UTC()
	deadline := now.Add(time.Duration(w.deadlineMS) * time.Millisecond)
	if err := w.ensureSchema(ctx); err != nil {
		slog.Warn("健康检查派发 outbox 建表失败",
			"event", "account_health_check_dispatch_unavailable", "accountId", normalizedID, "error", err)
		return gatewaycodex.HealthCheckDispatchOutcome{Outcome: gatewaycodex.HealthDispatchRejected, DecisionCode: "input_unavailable"}
	}
	_, err = w.db.ExecContext(ctx, w.bind(`INSERT INTO `+w.table()+`
		(request_id, account_id, reason, trace_id, source_fence, deadline_at, status, consumed_at, available_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, 'pending', NULL, ?, ?, ?)`),
		requestID, normalizedID, strings.TrimSpace(reason), strings.TrimSpace(traceID), fenceJSON,
		deadline.Format(chainProbeRequestOutboxTimeFormat), now.Format(chainProbeRequestOutboxTimeFormat),
		now.Format(chainProbeRequestOutboxTimeFormat), now.Format(chainProbeRequestOutboxTimeFormat))
	if err != nil {
		slog.Warn("健康检查派发写入 outbox 失败",
			"event", "account_health_check_dispatch_unavailable", "accountId", normalizedID, "error", err)
		return gatewaycodex.HealthCheckDispatchOutcome{Outcome: gatewaycodex.HealthDispatchRejected, DecisionCode: "dispatch_rejected"}
	}
	return gatewaycodex.HealthCheckDispatchOutcome{Outcome: gatewaycodex.HealthDispatchQueued, DecisionCode: "queued", TargetRole: "go-jobs"}
}

// chainRequestFailureHealthDispatcher is the port of
// dispatchRequestFailureAccountHealthCheck: the gateway-traffic gate, the
// per-request throttle and the outbox publish in the Node order. The
// per-request throttle lives on GatewayRequest.failureHealthCheckMark (the
// per-request port of the Node dispatched Symbol); this dispatcher itself is
// stateless besides the outbox writer.
type chainRequestFailureHealthDispatcher struct {
	outbox *chainProbeRequestOutboxWriter
}

// newChainRequestFailureHealthDispatcher assembles the dispatcher over the
// shared probe-request outbox writer. The writer is process-resident: unlike
// the removed HTTP bridge there is no reachability gate, so a nil writer is
// the only inert shape (hand-assembled tests).
func newChainRequestFailureHealthDispatcher(outbox *chainProbeRequestOutboxWriter) *chainRequestFailureHealthDispatcher {
	return &chainRequestFailureHealthDispatcher{
		outbox: outbox,
	}
}

// DispatchRequestFailureAccountHealthCheck mirrors
// dispatchRequestFailureAccountHealthCheck(req, trafficSource, accountId):
// non-gateway traffic and an already-dispatched request both return false
// without touching the writer; a queued dispatch marks the request (the mark
// dies with the request object — see GatewayRequest.failureHealthCheckMark).
func (d *chainRequestFailureHealthDispatcher) DispatchRequestFailureAccountHealthCheck(req *gatewaypreauth.GatewayRequest, trafficSource, accountID string) bool {
	if d == nil {
		return false
	}
	if trafficSource != gatewayTrafficSource {
		return false
	}
	if req.RequestFailureHealthCheckDispatched() {
		return false
	}
	outcome := d.dispatch(accountID, chainRequestFailureReason, "", nil)
	if outcome.Outcome == gatewaycodex.HealthDispatchRejected {
		return false
	}
	req.MarkRequestFailureHealthCheckDispatched()
	return true
}

// dispatch publishes one probe request through the outbox writer. The publish
// is fire-and-forget (Node dispatchAccountHealthCheck)：the request context
// dies with the response, the outbox insert must not.
func (d *chainRequestFailureHealthDispatcher) dispatch(accountID, reason, traceID string, sourceFence *chainHealthDispatchSourceFence) gatewaycodex.HealthCheckDispatchOutcome {
	if d == nil {
		return gatewaycodex.HealthCheckDispatchOutcome{Outcome: gatewaycodex.HealthDispatchRejected, DecisionCode: "input_unavailable"}
	}
	return d.outbox.EnqueueProbeRequest(context.Background(), accountID, reason, traceID, sourceFence)
}

// dispatchWithOutcome adapts the dispatcher onto the
// gatewaycodex.AccountHealthCheckDispatchFunc seam (the
// TurnAvoidanceProbeService DefaultDispatch): the probe envelope carries the
// source fence so the jobs worker settles the exact registered fence.
func (d *chainRequestFailureHealthDispatcher) dispatchWithOutcome(accountID, reason, traceID string, sourceFence *gatewaycodex.SourceProbeFence) gatewaycodex.HealthCheckDispatchOutcome {
	if d == nil {
		return gatewaycodex.HealthCheckDispatchOutcome{Outcome: gatewaycodex.HealthDispatchRejected, DecisionCode: "input_unavailable"}
	}
	var fence *chainHealthDispatchSourceFence
	if sourceFence != nil {
		fence = &chainHealthDispatchSourceFence{
			StateKey:         sourceFence.StateKey,
			AccountID:        sourceFence.AccountID,
			SourceGeneration: sourceFence.SourceGeneration,
			SourceFenceID:    sourceFence.SourceFenceID,
			RuntimeKey:       sourceFence.RuntimeKey,
			ProbeGeneration:  sourceFence.ProbeGeneration,
			ConfigRevision:   sourceFence.ConfigRevision,
		}
	}
	return d.dispatch(accountID, reason, traceID, fence)
}
