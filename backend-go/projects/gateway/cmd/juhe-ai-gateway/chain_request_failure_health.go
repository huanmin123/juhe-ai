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
//     J1 probe request fact (internal-api service, fire-and-forget);
//   - rejected dispatches do not consume the mark, so consecutive failed
//     candidates would each re-read the effective background-probe switch;
//     the Go side memoizes that read-only projection per request
//     (chainProbeSwitchMemo, DD-B7): one SELECT per (request, account), read
//     errors not cached. The memo lives in a weak-keyed registry so entries
//     are reclaimed with the request object exactly like the mark (the
//     BUG-0339 shape — a process-global strong-keyed request map — stays
//     forbidden).
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
	"sync/atomic"
	"time"
	"weak"

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

// chainProbeSwitchMemo 是单请求的生效后台探测开关投影 memo（DD-B7）：rejected
// 派发不消耗请求级标记，同一请求的每个失败候选都会走到开关守卫，memo 使同
// 请求内同一账户的开关 SELECT 只执行一次。键是归一化账户 id——开关守卫的判
// 定输入只有账户 id，动态状态（授权实例跟随来源账户的生效值）在同一 SELECT
// 投影内解析，同请求同 id 投影等价。只缓存只读投影结果；读取失败不缓存
// （保守：下一候选失败重查，不把一次读取错误固化为开关结论）。并发失败分
// 支下的 memo 命中竞争可能各查一次，结果等价（与既有逐次查询行为一致）。
type chainProbeSwitchMemo struct {
	mu         sync.Mutex
	disabledBy map[string]bool // 归一化 accountID → 生效开关已关闭
}

func (m *chainProbeSwitchMemo) lookup(accountID string) (disabled, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	disabled, ok = m.disabledBy[accountID]
	return disabled, ok
}

func (m *chainProbeSwitchMemo) store(accountID string, disabled bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.disabledBy == nil {
		m.disabledBy = make(map[string]bool)
	}
	m.disabledBy[accountID] = disabled
}

// chainProbeSwitchMemoSweepThreshold 是 memo 注册表的惰性清扫阈值：条目以
// weak.Pointer 持有请求身份，请求被 GC 后条目失效；达到阈值时先清扫 dead
// 条目再插入，进程内存有界（未清扫的残留也只占弱指针 + 空 memo 的小空间）。
const chainProbeSwitchMemoSweepThreshold = 1024

// chainProbeSwitchMemoRegistry 以 weak.Pointer 为键保存请求级开关 memo
// （Node WeakMap 的 Go 形态）：条目随请求对象一起被 GC 回收，绝不强引用
// GatewayRequest——进程级强键请求 map 会把请求（含 Body.RawBody）钉在堆里
// 直至淘汰，生产实测 ~1GB 活堆增长（BUG-0339），禁止回归该形态。
type chainProbeSwitchMemoRegistry struct {
	mu      sync.Mutex
	entries map[weak.Pointer[gatewaypreauth.GatewayRequest]]*chainProbeSwitchMemo
}

// memoFor 返回请求的 memo（nil req 返回 nil——无请求身份的派发路径不
// memo 化，保持既有逐次查询）。首见请求建 memo；容量越界先清扫 dead 条目。
func (r *chainProbeSwitchMemoRegistry) memoFor(req *gatewaypreauth.GatewayRequest) *chainProbeSwitchMemo {
	if req == nil {
		return nil
	}
	key := weak.Make(req)
	r.mu.Lock()
	defer r.mu.Unlock()
	if memo, ok := r.entries[key]; ok {
		return memo
	}
	if len(r.entries) >= chainProbeSwitchMemoSweepThreshold {
		for stale := range r.entries {
			if stale.Value() == nil {
				delete(r.entries, stale)
			}
		}
	}
	if r.entries == nil {
		r.entries = make(map[weak.Pointer[gatewaypreauth.GatewayRequest]]*chainProbeSwitchMemo)
	}
	memo := &chainProbeSwitchMemo{}
	r.entries[key] = memo
	return memo
}

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
	// readBackgroundProbeSwitch 是生效开关读取的注入缝（与 now 同款的
	// hand-assembled 测试契约）：nil 回退真 SQL 读取（FromDB 后缀方法是唯一
	// SELECT 落点，默认路径无旁路）；测试以计数桩替换，验证请求级 memo 的
	// 只查一次 / 失败不缓存契约。
	readBackgroundProbeSwitch func(ctx context.Context, accountID string) (disabled bool, err error)

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
// 的一次最小 SELECT），memo 非空时按请求级 memo 命中（DD-B7：同请求内同一
// 账户只查一次 DB）。账户行不存在或读取失败都不构成「已关闭」（返回 false
// + err 语义分离），既有派发链与 jobs 消费端守卫承接；读取失败不写 memo
// （保守：下一候选失败重查，不把读取错误固化为开关结论）。
func (w *chainProbeRequestOutboxWriter) backgroundProbeDisabled(ctx context.Context, memo *chainProbeSwitchMemo, accountID string) (bool, error) {
	if memo != nil {
		if disabled, ok := memo.lookup(accountID); ok {
			return disabled, nil
		}
	}
	read := w.readBackgroundProbeSwitchFromDB
	if w.readBackgroundProbeSwitch != nil {
		read = w.readBackgroundProbeSwitch
	}
	disabled, err := read(ctx, accountID)
	if err != nil {
		return false, err
	}
	if memo != nil {
		memo.store(accountID, disabled)
	}
	return disabled, nil
}

// readBackgroundProbeSwitchFromDB 是生效开关 SELECT 的唯一落点（与 jobs 候选
// SQL / freshness 门 / outbox boundary 守卫的 CASE 投影逐字同形）。
func (w *chainProbeRequestOutboxWriter) readBackgroundProbeSwitchFromDB(ctx context.Context, accountID string) (bool, error) {
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
	return w.EnqueueProbeRequestWithMemo(ctx, nil, accountID, reason, traceID, sourceFence)
}

// EnqueueProbeRequestWithMemo 是 EnqueueProbeRequest 的请求级 memo 形态
// （DD-B7）：memo 携带本请求的生效开关投影（nil = 无请求身份的派发路径，
// 保持既有逐次查询）。memo 只影响开关 SELECT 的重复执行，不改变任何状态
// 写入——outbox INSERT 每次照常执行。
func (w *chainProbeRequestOutboxWriter) EnqueueProbeRequestWithMemo(ctx context.Context, memo *chainProbeSwitchMemo, accountID, reason, traceID string, sourceFence *chainHealthDispatchSourceFence) gatewaycodex.HealthCheckDispatchOutcome {
	normalizedID := strings.TrimSpace(accountID)
	if normalizedID == "" {
		return gatewaycodex.HealthCheckDispatchOutcome{Outcome: gatewaycodex.HealthDispatchRejected, DecisionCode: "dispatch_rejected"}
	}
	if w == nil || w.db == nil || w.deadlineMS <= 0 {
		return gatewaycodex.HealthCheckDispatchOutcome{Outcome: gatewaycodex.HealthDispatchRejected, DecisionCode: "input_unavailable"}
	}
	// 后台自动探测开关守卫（设计 §3：请求失败二次确认不投递）：关闭账户按
	// rejected 收敛且不写 outbox、不告警（真实请求失败路径）；同请求失败
	// 候选的开关读取按请求级 memo 去重（DD-B7），读取失败保持既有派发行为，
	// jobs 消费端守卫兜底。
	disabled, err := w.backgroundProbeDisabled(ctx, memo, normalizedID)
	if err != nil {
		slog.Warn("健康检查派发读取后台探测开关失败，保持既有派发行为",
			"event", "account_health_check_dispatch_flag_read_failed", "accountId", normalizedID, "error", err)
	} else if disabled {
		return gatewaycodex.HealthCheckDispatchOutcome{Outcome: gatewaycodex.HealthDispatchRejected, DecisionCode: probeSkippedDisabledDecision}
	}
	return w.insertProbeRequestRow(ctx, normalizedID, reason, traceID, sourceFence)
}

// insertProbeRequestRow 是 outbox 单行 INSERT 的落点（开关守卫之后）。
// 异步请求失败派发 worker 直接调用它，避免每条失败请求在请求协程里等
// INSERT；sourceFence 路径仍走同步 Enqueue，保证 codex turn 交接能确认
// 行已落库后才返回 queued。
func (w *chainProbeRequestOutboxWriter) insertProbeRequestRow(ctx context.Context, accountID, reason, traceID string, sourceFence *chainHealthDispatchSourceFence) gatewaycodex.HealthCheckDispatchOutcome {
	normalizedID := strings.TrimSpace(accountID)
	if normalizedID == "" {
		return gatewaycodex.HealthCheckDispatchOutcome{Outcome: gatewaycodex.HealthDispatchRejected, DecisionCode: "dispatch_rejected"}
	}
	if w == nil || w.db == nil || w.deadlineMS <= 0 {
		return gatewaycodex.HealthCheckDispatchOutcome{Outcome: gatewaycodex.HealthDispatchRejected, DecisionCode: "input_unavailable"}
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
	_, err := w.db.ExecContext(ctx, w.bind(`INSERT INTO `+w.table()+`
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
// per-request port of the Node dispatched Symbol); the switch-projection
// memo registry holds only weak references to requests (entries die with the
// request object — the BUG-0339 strong-keyed shape stays forbidden);
// otherwise this dispatcher is stateless besides the outbox writer.
const (
	probeDispatchQueueCapacity = 1024
	probeDispatchInsertTimeout = 5 * time.Second
)

type chainProbeDispatchJob struct {
	accountID string
	reason    string
	traceID   string
	barrier   chan struct{}
}

type chainRequestFailureHealthDispatcher struct {
	outbox *chainProbeRequestOutboxWriter
	memos  chainProbeSwitchMemoRegistry
	// queue 仅在有可用 outbox 时创建；请求失败路径把 INSERT 交给单 worker，
	// 请求协程只做一次开关 SELECT + 非阻塞入队。sourceFence 路径（codex
	// turn 交接）仍走同步 Enqueue，必须确认行落库后才返回 queued。
	queue  chan chainProbeDispatchJob
	stop   chan struct{}
	done   chan struct{}
	closed atomic.Bool
}

// newChainRequestFailureHealthDispatcher assembles the dispatcher over the
// shared probe-request outbox writer. The writer is process-resident: unlike
// the removed HTTP bridge there is no reachability gate, so a nil writer is
// the only inert shape (hand-assembled tests).
func newChainRequestFailureHealthDispatcher(outbox *chainProbeRequestOutboxWriter) *chainRequestFailureHealthDispatcher {
	dispatcher := &chainRequestFailureHealthDispatcher{outbox: outbox}
	if outbox != nil && outbox.db != nil {
		dispatcher.queue = make(chan chainProbeDispatchJob, probeDispatchQueueCapacity)
		dispatcher.stop = make(chan struct{})
		dispatcher.done = make(chan struct{})
		go dispatcher.run()
	}
	return dispatcher
}

func (d *chainRequestFailureHealthDispatcher) run() {
	defer close(d.done)
	for {
		select {
		case <-d.stop:
			d.drain()
			return
		case job := <-d.queue:
			d.deliver(job)
		}
	}
}

func (d *chainRequestFailureHealthDispatcher) drain() {
	for {
		select {
		case job := <-d.queue:
			d.deliver(job)
		default:
			return
		}
	}
}

func (d *chainRequestFailureHealthDispatcher) deliver(job chainProbeDispatchJob) {
	if job.barrier != nil {
		close(job.barrier)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), probeDispatchInsertTimeout)
	defer cancel()
	_ = d.outbox.insertProbeRequestRow(ctx, job.accountID, job.reason, job.traceID, nil)
}

// Flush 等待已入队的请求失败探针全部落库（测试与停机排空用）。
func (d *chainRequestFailureHealthDispatcher) Flush(ctx context.Context) error {
	if d == nil || d.queue == nil {
		return nil
	}
	if d.closed.Load() {
		return nil
	}
	barrier := make(chan struct{})
	select {
	case d.queue <- chainProbeDispatchJob{barrier: barrier}:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-barrier:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close 停止异步 INSERT worker（尽力排空后退出）。
func (d *chainRequestFailureHealthDispatcher) Close(ctx context.Context) error {
	if d == nil || d.queue == nil {
		return nil
	}
	if d.closed.CompareAndSwap(false, true) {
		close(d.stop)
	}
	select {
	case <-d.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
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
	normalizedID := strings.TrimSpace(accountID)
	if normalizedID == "" {
		return false
	}
	if d.queue == nil {
		outcome := d.dispatchWithMemo(d.memos.memoFor(req), normalizedID, chainRequestFailureReason, "", nil)
		if outcome.Outcome == gatewaycodex.HealthDispatchRejected {
			return false
		}
		req.MarkRequestFailureHealthCheckDispatched()
		return true
	}
	// 开关 SELECT 保持在请求协程（关闭账户不消耗请求标记的语义不变，且单次
	// PK 查询远轻于 INSERT）；INSERT 交给单 worker 有界队列，队列满则不标记、
	// 由后续失败候选重试。
	disabled, err := d.outbox.backgroundProbeDisabled(context.Background(), d.memos.memoFor(req), normalizedID)
	if err != nil {
		slog.Warn("健康检查派发读取后台探测开关失败，保持既有派发行为",
			"event", "account_health_check_dispatch_flag_read_failed", "accountId", normalizedID, "error", err)
	} else if disabled {
		return false
	}
	if d.closed.Load() {
		return false
	}
	select {
	case d.queue <- chainProbeDispatchJob{accountID: normalizedID, reason: chainRequestFailureReason}:
	default:
		return false
	}
	req.MarkRequestFailureHealthCheckDispatched()
	return true
}

// dispatch publishes one probe request through the outbox writer. The publish
// is fire-and-forget (Node dispatchAccountHealthCheck)：the request context
// dies with the response, the outbox insert must not. This path carries no
// request identity (TurnAvoidanceProbeService 默认派发，进程作用域) → no
// memo, the switch read stays per-call.
func (d *chainRequestFailureHealthDispatcher) dispatch(accountID, reason, traceID string, sourceFence *chainHealthDispatchSourceFence) gatewaycodex.HealthCheckDispatchOutcome {
	return d.dispatchWithMemo(nil, accountID, reason, traceID, sourceFence)
}

// dispatchWithMemo 同 dispatch，携带请求级开关 memo（nil = 逐次查询）。
func (d *chainRequestFailureHealthDispatcher) dispatchWithMemo(memo *chainProbeSwitchMemo, accountID, reason, traceID string, sourceFence *chainHealthDispatchSourceFence) gatewaycodex.HealthCheckDispatchOutcome {
	if d == nil {
		return gatewaycodex.HealthCheckDispatchOutcome{Outcome: gatewaycodex.HealthDispatchRejected, DecisionCode: "input_unavailable"}
	}
	return d.outbox.EnqueueProbeRequestWithMemo(context.Background(), memo, accountID, reason, traceID, sourceFence)
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
