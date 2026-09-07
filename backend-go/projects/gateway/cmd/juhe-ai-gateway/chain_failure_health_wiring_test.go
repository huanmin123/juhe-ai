package main

// 生效断言测试：失败派发链的三件登记装配——request-failure 健康检查派发
// outbox 通道（failure-dispatch.ts:404/571；去跨进程战役第二刀：原 loopback
// HMAC 桥已删，派发落 account_health_probe_request_outbox 行，jobs J1 Runner
// drain）、TurnAvoidanceProbeService 桥接装配
// （turn-availability-probe.service.ts + gatewaycircuit.ProbeCoordinator）、
// TurnRetryService 的 Redis 状态驱动（runtime-state-store.ts
// 'gateway-codex-turn-retry' 键空间）。逐条对照归档语义，装配断线即失败。

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	redis "github.com/redis/go-redis/v9"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycircuit"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycodex"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	_ "modernc.org/sqlite"
)

// ---------------------------------------------------------------------------
// 装配 1：健康检查派发 DB outbox（account_health_probe_request_outbox）
// ---------------------------------------------------------------------------

type healthOutboxFixture struct {
	db     *sql.DB
	writer *chainProbeRequestOutboxWriter
}

// newHealthOutboxFixture 在临时 SQLite 文件上构造 writer（与生产 SQLite 模式
// 同一驱动、同一 DDL ensure 路径）。
func newHealthOutboxFixture(t *testing.T, deadlineMS int64) *healthOutboxFixture {
	t.Helper()
	dsn := "file:" + filepath.ToSlash(filepath.Join(t.TempDir(), "probe-outbox.sqlite3")) + "?_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open outbox sqlite: %v", err)
	}
	// 单连接：测试串行执行，固定句柄消除连接池对 SQLite 文件的重复打开。
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	writer := newChainProbeRequestOutboxWriter(db, false, deadlineMS)
	return &healthOutboxFixture{db: db, writer: writer}
}

func (f *healthOutboxFixture) dispatcher() *chainRequestFailureHealthDispatcher {
	return newChainRequestFailureHealthDispatcher(f.writer)
}

func (f *healthOutboxFixture) dispatch(t *testing.T, req *gatewaypreauth.GatewayRequest, trafficSource, accountID string) bool {
	t.Helper()
	return f.dispatcher().DispatchRequestFailureAccountHealthCheck(req, trafficSource, accountID)
}

// rows 返回全部 outbox 行（自然序），行键为列名。
func (f *healthOutboxFixture) rows(t *testing.T) []map[string]any {
	t.Helper()
	result, err := f.db.Query(`SELECT request_id, account_id, reason, trace_id, source_fence, deadline_at, status, consumed_at, available_at, created_at
		FROM account_health_probe_request_outbox ORDER BY created_at, request_id`)
	if err != nil {
		t.Fatalf("query outbox rows: %v", err)
	}
	defer result.Close()
	out := []map[string]any{}
	columns, err := result.Columns()
	if err != nil {
		t.Fatal(err)
	}
	for result.Next() {
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err := result.Scan(pointers...); err != nil {
			t.Fatal(err)
		}
		row := map[string]any{}
		for i, name := range columns {
			row[name] = values[i]
		}
		out = append(out, row)
	}
	if err := result.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func (f *healthOutboxFixture) count(t *testing.T) int {
	t.Helper()
	return len(f.rows(t))
}

func (f *healthOutboxFixture) lastRow(t *testing.T) map[string]any {
	t.Helper()
	rows := f.rows(t)
	if len(rows) == 0 {
		t.Fatal("no outbox row captured")
	}
	return rows[len(rows)-1]
}

// TestChainProbeRequestOutboxWire：outbox 通道的行契约——幂等键、账户/reason
// 投影、deadline 窗口（同 env 同默认同范围）、pending 可消费状态、inert
// writer（缺 DB 或非法 deadline）按 input_unavailable 拒绝。
func TestChainProbeRequestOutboxWire(t *testing.T) {
	fixture := newHealthOutboxFixture(t, 65_000)

	outcome := fixture.writer.EnqueueProbeRequest(context.Background(), "acc_1", chainRequestFailureReason, "", nil)
	if outcome.Outcome != gatewaycodex.HealthDispatchQueued || outcome.DecisionCode != "queued" || outcome.TargetRole != "go-jobs" {
		t.Fatalf("dispatch outcome = %#v", outcome)
	}
	row := fixture.lastRow(t)
	if row["account_id"] != "acc_1" || row["reason"] != chainRequestFailureReason {
		t.Fatalf("outbox row = %#v", row)
	}
	if row["request_id"] == "" || !strings.HasPrefix(row["request_id"].(string), "j1-") {
		t.Fatalf("request id must be the j1- idempotency key: %#v", row["request_id"])
	}
	if row["status"] != "pending" || row["consumed_at"] != nil {
		t.Fatalf("row must be immediately consumable (pending): %#v", row)
	}
	deadline, err := time.Parse(time.RFC3339Nano, row["deadline_at"].(string))
	if err != nil {
		t.Fatalf("parse deadline_at: %v", err)
	}
	created, err := time.Parse(time.RFC3339Nano, row["created_at"].(string))
	if err != nil {
		t.Fatalf("parse created_at: %v", err)
	}
	if elapsed := deadline.Sub(created); elapsed != 65_000*time.Millisecond {
		t.Fatalf("deadline window = %s want 65s (the shared env default)", elapsed)
	}

	// trace_id 投影（dispatchWithOutcome 的 wire 契约延续）。
	if _, err := fixture.db.Exec(`INSERT INTO account_health_probe_request_outbox
		(request_id, account_id, reason, trace_id, source_fence, deadline_at, status, consumed_at, available_at, created_at, updated_at)
		VALUES ('j1-seed', 'acc_1', 'x', '', '', '2026-01-01T00:00:00.000000000Z', 'pending', NULL, '2026-01-01T00:00:00.000000000Z', '2026-01-01T00:00:00.000000000Z', '2026-01-01T00:00:00.000000000Z')`); err != nil {
		t.Fatalf("seed second row: %v", err)
	}
	withTrace := fixture.dispatcher().dispatchWithOutcome("acc_1", "probe_timeout", "trace-7", nil)
	if withTrace.Outcome != gatewaycodex.HealthDispatchQueued {
		t.Fatalf("trace dispatch outcome = %#v", withTrace)
	}
	traceRow := fixture.lastRow(t)
	if traceRow["trace_id"] != "trace-7" {
		t.Fatalf("trace id projection = %#v", traceRow["trace_id"])
	}

	// inert writer：缺 DB（nil 句柄）与非法 deadline 都保持 input_unavailable，
	// 不写行（原桥缺装配/超时的显式降级契约）。
	var nilDBWriter *chainProbeRequestOutboxWriter
	if got := nilDBWriter.EnqueueProbeRequest(context.Background(), "acc_1", "request_failure", "", nil); got.Outcome != gatewaycodex.HealthDispatchRejected || got.DecisionCode != "input_unavailable" {
		t.Fatalf("nil-db writer outcome = %#v", got)
	}
	if got := newChainProbeRequestOutboxWriter(fixture.db, false, 0).EnqueueProbeRequest(context.Background(), "acc_1", "request_failure", "", nil); got.Outcome != gatewaycodex.HealthDispatchRejected || got.DecisionCode != "input_unavailable" {
		t.Fatalf("zero-deadline writer outcome = %#v", got)
	}

	// 空账户 ID → dispatch_rejected（Node dispatch_rejected 分叉）。ensure 后
	// 派发不写行。
	empty := newHealthOutboxFixture(t, 65_000)
	if err := empty.writer.ensureSchema(context.Background()); err != nil {
		t.Fatalf("ensure empty fixture schema: %v", err)
	}
	if got := empty.writer.EnqueueProbeRequest(context.Background(), "  ", chainRequestFailureReason, "", nil); got.Outcome != gatewaycodex.HealthDispatchRejected || got.DecisionCode != "dispatch_rejected" {
		t.Fatalf("empty account id outcome = %#v", got)
	}
	if empty.count(t) != 0 {
		t.Fatalf("rejected dispatch must not write rows, rows=%d", empty.count(t))
	}
}

// TestChainProbeRequestOutboxEnsureSchemaIdempotent：双侧幂等建表契约——同一
// 句柄重复 ensure（gateway 先到、jobs 后到的同 DDL 幂等收敛）必须无错。
func TestChainProbeRequestOutboxEnsureSchemaIdempotent(t *testing.T) {
	fixture := newHealthOutboxFixture(t, 65_000)
	ctx := context.Background()
	if err := fixture.writer.ensureSchema(ctx); err != nil {
		t.Fatalf("first ensure: %v", err)
	}
	if err := fixture.writer.ensureSchema(ctx); err != nil {
		t.Fatalf("second ensure: %v", err)
	}
	if _, err := fixture.db.Exec(chainProbeRequestOutboxSchema); err != nil {
		t.Fatalf("re-run create: %v", err)
	}
	if _, err := fixture.db.Exec(chainProbeRequestOutboxIndex); err != nil {
		t.Fatalf("re-run index: %v", err)
	}
}

// TestChainRequestFailureHealthDispatcherThrottle：请求级去重（Node Symbol
// 标记语义）——同一请求只派发一次，rejected 不消耗标记，非 gateway 流量不派发。
func TestChainRequestFailureHealthDispatcherThrottle(t *testing.T) {
	fixture := newHealthOutboxFixture(t, 65_000)
	dispatcher := fixture.dispatcher()
	req := gatewaypreauth.NewGatewayRequest(httptest.NewRequest(http.MethodPost, "http://gateway.local/v1/chat/completions", nil))

	if !dispatcher.DispatchRequestFailureAccountHealthCheck(req, gatewayTrafficSource, "acc_1") {
		t.Fatal("first dispatch must go through")
	}
	if dispatcher.DispatchRequestFailureAccountHealthCheck(req, gatewayTrafficSource, "acc_1") {
		t.Fatal("second dispatch on the same request must be throttled")
	}
	if rows := fixture.count(t); rows != 1 {
		t.Fatalf("rows = %d want 1 (per-request throttle)", rows)
	}

	// 非 gateway 流量：不派发、不标记。
	other := gatewaypreauth.NewGatewayRequest(httptest.NewRequest(http.MethodPost, "http://gateway.local/v1/chat/completions", nil))
	if dispatcher.DispatchRequestFailureAccountHealthCheck(other, "account_diagnostic", "acc_1") {
		t.Fatal("non-gateway traffic must not dispatch")
	}
	if rows := fixture.count(t); rows != 1 {
		t.Fatalf("non-gateway rows leaked: %d", rows)
	}
	// 同一请求后续 gateway 失败仍可派发（标记只由成功派发写入）。
	if !dispatcher.DispatchRequestFailureAccountHealthCheck(other, gatewayTrafficSource, "acc_1") {
		t.Fatal("gateway dispatch after a skipped non-gateway call must go through")
	}
	if rows := fixture.count(t); rows != 2 {
		t.Fatalf("rows = %d want 2", rows)
	}
}

// TestChainFailureDispatcherDispatchesRequestFailureHealthCheck：failure-dispatch.ts
// 两个触发点——传输失败分支（:571）与 failed-response 分支（:404，system
// quota 决策除外）都经请求级节流派发一次。
func TestChainFailureDispatcherDispatchesRequestFailureHealthCheck(t *testing.T) {
	// 传输失败分支：gateway 流量派发一次。
	fixture := newHealthOutboxFixture(t, 65_000)
	sink := &failureDispatchAuditSink{}
	dispatcher := &chainFailureDispatcher{healthDispatch: fixture.dispatcher()}
	input := avoidanceRecordRequestInput(t, sink, "gateway")
	if _, err := dispatcher.HandleUpstreamRequestError(context.Background(), input); err != nil {
		t.Fatalf("transport failure: %v", err)
	}
	if rows := fixture.count(t); rows != 1 {
		t.Fatalf("transport branch rows = %d want 1", rows)
	}
	if row := fixture.lastRow(t); row["reason"] != chainRequestFailureReason {
		t.Fatalf("reason = %#v", row["reason"])
	}

	// 同一请求的第二次失败（候选切换）被请求级节流吸收。
	if _, err := dispatcher.HandleUpstreamRequestError(context.Background(), input); err != nil {
		t.Fatalf("second transport failure: %v", err)
	}
	if rows := fixture.count(t); rows != 1 {
		t.Fatalf("per-request throttle failed: rows = %d", rows)
	}

	// failed-response 分支：无显式决策（500 普通失败）→ 派发。
	failedFixture := newHealthOutboxFixture(t, 65_000)
	failedDispatcher := &chainFailureDispatcher{healthDispatch: failedFixture.dispatcher()}
	opaque := gatewayFailedResponseInput(
		failureDispatchUpstreamResponse(t, http.StatusInternalServerError, "application/json", `{"error":{"message":"boom"}}`),
		&failureDispatchAuditSink{}, "gateway")
	opaque.Req = input.Req
	if _, err := failedDispatcher.HandleFailedUpstreamResponse(context.Background(), opaque); err != nil {
		t.Fatalf("opaque failure: %v", err)
	}
	if rows := failedFixture.count(t); rows != 1 {
		t.Fatalf("failed-response branch rows = %d want 1", rows)
	}

	// system quota 决策（402 + insufficient_quota）→ 不派发：探活不得与
	// 显式错误状态竞争（failure-dispatch.ts:396-404 注释）。
	systemFixture := newHealthOutboxFixture(t, 65_000)
	if err := systemFixture.writer.ensureSchema(context.Background()); err != nil {
		t.Fatalf("ensure system fixture schema: %v", err)
	}
	systemDispatcher := &chainFailureDispatcher{policy: newFixedErrorPolicyService(nil), healthDispatch: systemFixture.dispatcher()}
	systemInput := gatewayFailedResponseInput(
		failureDispatchUpstreamResponse(t, http.StatusPaymentRequired, "application/json",
			`{"error":{"code":"insufficient_quota","message":"insufficient quota"}}`),
		&failureDispatchAuditSink{}, "gateway")
	systemInput.Req = input.Req
	systemInput.Settings = gatewayruntimecache.GatewaySettings{DefaultTemporaryUnschedulableMinutes: 30}
	if _, err := systemDispatcher.HandleFailedUpstreamResponse(context.Background(), systemInput); err != nil {
		t.Fatalf("system quota failure: %v", err)
	}
	if rows := systemFixture.count(t); rows != 0 {
		t.Fatalf("system-quota decision must not dispatch, rows = %d", rows)
	}
}

// ---------------------------------------------------------------------------
// 装配 2：TurnAvoidanceProbeService + memory probe-state store
// ---------------------------------------------------------------------------

// TestChainTurnAvoidanceProbeServiceRunsProbe：装配后的探活服务真实走通
// Acquire → dispatch(source fence) → settle 契约——outbox 行携带 source_fence
// JSON 窄投影，协调器进入 dispatch-pending 的 owner 态。
func TestChainTurnAvoidanceProbeServiceRunsProbe(t *testing.T) {
	fixture := newHealthOutboxFixture(t, 65_000)
	dispatcher := fixture.dispatcher()
	turnRetry := &gatewaycodex.TurnRetryService{Secret: "unit-secret"}
	probe := newChainTurnAvoidanceProbeService(turnRetry, gatewaypreauth.SystemClock{}, dispatcher)
	if probe.Coordinator == nil || probe.TurnRetry == nil || probe.DefaultDispatch == nil {
		t.Fatal("probe collaborators must be wired")
	}
	strategy := gatewaycodex.OpenAIGatewayClientStrategyContext{
		AllowClientSourceAccountAvoidance: true,
		ClientSourceAvoidanceStateKey:     "src-key",
	}
	result, err := probe.RunCodexTurnAvoidanceAvailabilityProbe(context.Background(), gatewaycodex.CodexTurnAvoidanceProbeInput{
		Account:  gatewayruntimecache.OpenAIAccountSecret{ID: "acc_1"},
		Strategy: strategy,
		Activation: gatewaycodex.CodexTurnFailureActivation{
			AccountID:     "acc_1",
			SourceFenceID: gatewaycodex.RandomUUID(),
		},
	})
	if err != nil {
		t.Fatalf("run probe: %v", err)
	}
	if result.Disposition != "owner" {
		t.Fatalf("disposition = %s want owner", result.Disposition)
	}
	if rows := fixture.count(t); rows != 1 {
		t.Fatalf("probe dispatch rows = %d want 1", rows)
	}
	row := fixture.lastRow(t)
	if row["reason"] != chainRequestFailureReason {
		t.Fatalf("probe reason = %#v", row["reason"])
	}
	fenceText, _ := row["source_fence"].(string)
	var fence map[string]any
	if err := json.Unmarshal([]byte(fenceText), &fence); err != nil {
		t.Fatalf("decode source fence %q: %v", fenceText, err)
	}
	if fence["state_key"] != "src-key" || fence["account_id"] != "acc_1" {
		t.Fatalf("probe row source fence missing: %#v", fence)
	}

	// 派发被拒（inert writer 的生产降级形态）→ probe_task_failure 结算
	// （Node 契约：快速拒绝必须结算 fence，不能搁浅 generation）。
	rejecting := newChainRequestFailureHealthDispatcher(newChainProbeRequestOutboxWriter(nil, false, 65_000))
	rejectingProbe := newChainTurnAvoidanceProbeService(turnRetry, gatewaypreauth.SystemClock{}, rejecting)
	rejected, err := rejectingProbe.RunCodexTurnAvoidanceAvailabilityProbe(context.Background(), gatewaycodex.CodexTurnAvoidanceProbeInput{
		Account:  gatewayruntimecache.OpenAIAccountSecret{ID: "acc_1"},
		Strategy: strategy,
		Activation: gatewaycodex.CodexTurnFailureActivation{
			AccountID:     "acc_1",
			SourceFenceID: gatewaycodex.RandomUUID(),
		},
	})
	if err != nil {
		t.Fatalf("run rejected probe: %v", err)
	}
	if rejected.Disposition != "owner" || rejected.Outcome != gatewaycodex.ProbeOutcomeProbeTaskFailure {
		t.Fatalf("rejected probe = %+v want owner/probe_task_failure", rejected)
	}
}

// TestChainMemoryProbeStateStoreSemantics：memory probe-state store 的协调
// 语义（Node MemoryRuntimeProbeStateStore）——代际单调、缺席写入、run 提交
// 的 sourceFences 并集、已结算代际的原子替换。
func TestChainMemoryProbeStateStoreSemantics(t *testing.T) {
	now := int64(1000)
	store := newChainMemoryProbeStateStore(func() int64 { return now })
	ctx := context.Background()

	first, err := store.NextGeneration(ctx, "rt-key", 1000)
	if err != nil || first != 1 {
		t.Fatalf("first generation = %d, %v", first, err)
	}
	second, err := store.NextGeneration(ctx, "rt-key", 1000)
	if err != nil || second != 2 {
		t.Fatalf("second generation = %d, %v", second, err)
	}

	state := gatewaycircuitProbeState("rt-key", 1)
	ok, err := store.SetIfAbsent(ctx, state, 60_000)
	if err != nil || !ok {
		t.Fatalf("setIfAbsent = %v, %v", ok, err)
	}
	ok, err = store.SetIfAbsent(ctx, state, 60_000)
	if err != nil || ok {
		t.Fatalf("duplicate setIfAbsent = %v, %v", ok, err)
	}

	// run 提交：runId 匹配才提交，sourceFences 并集去重（上限 64）。
	if _, err := store.AcquireGenerationRun(ctx, "rt-key", 1, "run-1", 2000, 60_000); err != nil {
		t.Fatalf("acquire run: %v", err)
	}
	committedState := gatewaycircuitProbeState("rt-key", 1)
	committedState.SourceFences = []string{"fence-b"}
	committed, err := store.CommitGenerationRun(ctx, committedState, "run-1", 60_000)
	if err != nil || !committed {
		t.Fatalf("commit run = %v, %v", committed, err)
	}
	merged, err := store.Get(ctx, "rt-key")
	if err != nil || merged == nil {
		t.Fatalf("get committed: %v, %v", merged, err)
	}
	if merged.ProbeRunID != nil {
		t.Fatalf("committed run id must clear: %+v", merged.ProbeRunID)
	}
	if len(merged.SourceFences) != 2 || merged.SourceFences[0] != "fence-a" || merged.SourceFences[1] != "fence-b" {
		t.Fatalf("source fences = %v want union", merged.SourceFences)
	}

	// 已结算代际替换：outcome 存在且无 run 时返回精确前照并写入新代际。
	settled := *merged
	outcome := gatewaycircuit.ProbeOutcomeSuccess
	settled.Outcome = &outcome
	if err := store.setForTest(ctx, settled); err != nil {
		t.Fatalf("seed settled state: %v", err)
	}
	replacement := gatewaycircuitProbeState("rt-key", 2)
	previous, err := store.ReplaceSettledGeneration(ctx, replacement, 1, 60_000)
	if err != nil || previous == nil || previous.Generation != 1 {
		t.Fatalf("replace settled = %+v, %v", previous, err)
	}
	current, err := store.Get(ctx, "rt-key")
	if err != nil || current == nil || current.Generation != 2 {
		t.Fatalf("current after replace = %+v, %v", current, err)
	}
}

func gatewaycircuitProbeState(runtimeKey string, generation int64) gatewaycircuit.ProbeState {
	return gatewaycircuit.ProbeState{
		RuntimeKey:          runtimeKey,
		Generation:          generation,
		NextProbeAtMs:       1000,
		AccountRuntimeScope: "acc_1",
		ProbeKind:           gatewaycircuit.ProbeKindAccountHealthCheck,
		ConfigRevision:      1,
		SourceFences:        []string{"fence-a"},
	}
}

// setForTest 直接写入一个状态（绕过 SetIfAbsent 的缺席约束）。
func (s *chainMemoryProbeStateStore) setForTest(_ context.Context, state gatewaycircuit.ProbeState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries[state.RuntimeKey] = &chainMemoryProbeStateEntry{value: state, expiresAtMs: s.nowMs() + 60_000}
	return nil
}

// ---------------------------------------------------------------------------
// 装配 3：TurnRetryStateStore Redis 驱动
// ---------------------------------------------------------------------------

// TestChainTurnRetryRedisStateStoreRoundTrip：miniredis 上的键空间与
// getJson / compareSetJson / incr 契约（Node RedisRuntimeStateStore）。
func TestChainTurnRetryRedisStateStoreRoundTrip(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	store, err := newChainTurnRetryRedisStateStore(client, "dev")
	if err != nil || store == nil {
		t.Fatalf("build store: %v, %v", store, err)
	}
	if store.prefix != "juhe-ai:dev:state:gateway-codex-turn-retry:" {
		t.Fatalf("prefix = %s want the Node redisNamespacedKey layout", store.prefix)
	}
	ctx := context.Background()
	stateKey := "state:src-key:a_digest"

	// 缺席读取。
	raw, err := store.GetJSON(ctx, stateKey)
	if err != nil || raw != nil {
		t.Fatalf("absent get = %s, %v", raw, err)
	}
	// expected=nil 的 CAS：键必须不存在才写。
	next := map[string]any{"failureCount": 2}
	ok, err := store.CompareSetJSON(ctx, stateKey, nil, next, 60_000)
	if err != nil || !ok {
		t.Fatalf("cas create = %v, %v", ok, err)
	}
	stored, err := server.Get(store.prefix + stateKey)
	if err != nil || !strings.Contains(stored, "failureCount") {
		t.Fatalf("stored value = %s, %v", stored, err)
	}
	if ttl := server.TTL(store.prefix + stateKey); ttl <= 0 {
		t.Fatalf("ttl = %v want the PX window", ttl)
	}
	// expected 不匹配 → false。
	ok, err = store.CompareSetJSON(ctx, stateKey, json.RawMessage(`{"failureCount":9}`), next, 60_000)
	if err != nil || ok {
		t.Fatalf("cas mismatched = %v, %v", ok, err)
	}
	// expected 精确匹配 → true。
	current, err := store.GetJSON(ctx, stateKey)
	if err != nil {
		t.Fatalf("get current: %v", err)
	}
	ok, err = store.CompareSetJSON(ctx, stateKey, current, map[string]any{"failureCount": 3}, 60_000)
	if err != nil || !ok {
		t.Fatalf("cas matched = %v, %v", ok, err)
	}

	// incr：单调计数沿用同一 TTL 窗口。
	value, err := store.Incr(ctx, "generation:src-key:a_digest", 60_000)
	if err != nil || value != 1 {
		t.Fatalf("first incr = %d, %v", value, err)
	}
	value, err = store.Incr(ctx, "generation:src-key:a_digest", 60_000)
	if err != nil || value != 2 {
		t.Fatalf("second incr = %d, %v", value, err)
	}

	// 损坏值：读取删除并按缺席返回（Node catch）。
	if err := server.Set(store.prefix+stateKey, "{not-json"); err != nil {
		t.Fatalf("seed corrupt value: %v", err)
	}
	raw, err = store.GetJSON(ctx, stateKey)
	if err != nil || raw != nil {
		t.Fatalf("corrupt get = %s, %v", raw, err)
	}
	if _, err := server.Get(store.prefix + stateKey); err == nil {
		t.Fatal("corrupt value must be deleted")
	}

	// nil client → memory 驱动（nil 适配器）。
	if memoryStore, buildErr := newChainTurnRetryRedisStateStore(nil, "dev"); memoryStore != nil || buildErr != nil {
		t.Fatalf("nil client must keep the memory driver: %v, %v", memoryStore, buildErr)
	}
}

// ---------------------------------------------------------------------------
// 组合根接线：装配断线即失败
// ---------------------------------------------------------------------------

// TestComposeGatewayChainWiresFailureDispatchCollaborators：composeGatewayChain
// 在 deps 齐备时把三件协作器挂进失败派发器——健康检查 outbox writer、探活
// 服务（含协调器与默认派发）、turn-retry 的 Redis 状态驱动。
func TestComposeGatewayChainWiresFailureDispatchCollaborators(t *testing.T) {
	fixture := newChainFixture(t)
	outbox := newHealthOutboxFixture(t, 65_000)
	redisServer := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	t.Cleanup(func() { _ = redisClient.Close() })
	turnRetryStore, err := newChainTurnRetryRedisStateStore(redisClient, "dev")
	if err != nil {
		t.Fatalf("build turn retry store: %v", err)
	}

	deps := chainSmokeDeps(t, fixture, gatewaypreauth.SystemClock{}, "")
	deps.Identity = &sessionIdentityServices{Secret: "unit-secret"}
	deps.HealthProbeOutbox = outbox.writer
	deps.TurnRetryStateStore = turnRetryStore
	chain, shutdown, assembleErr := composeGatewayChain(deps)
	if assembleErr != nil {
		t.Fatalf("compose gateway chain: %v", assembleErr)
	}
	defer shutdown()

	dispatcher, ok := chain.engine.FailureDispatcher.(*chainFailureDispatcher)
	if !ok {
		t.Fatalf("failure dispatcher type = %T", chain.engine.FailureDispatcher)
	}
	if dispatcher.healthDispatch == nil || dispatcher.healthDispatch.outbox != outbox.writer {
		t.Fatal("request-failure health outbox writer not mounted")
	}
	if dispatcher.avoidanceProbe == nil {
		t.Fatal("turn avoidance probe service missing")
	}
	if dispatcher.avoidanceProbe.Coordinator == nil || dispatcher.avoidanceProbe.DefaultDispatch == nil {
		t.Fatal("avoidance probe collaborators missing")
	}
	if dispatcher.turnRetry == nil {
		t.Fatal("turn retry service missing")
	}
	if dispatcher.turnRetry.Store != turnRetryStore {
		t.Fatal("turn retry redis state store not mounted")
	}

	// 缺 writer 的装配保持显式降级：派发端口常驻但 writer 为 nil（派发按
	// input_unavailable 拒绝），探活服务仍装配。
	degradedDeps := chainSmokeDeps(t, fixture, gatewaypreauth.SystemClock{}, "")
	degradedDeps.Identity = &sessionIdentityServices{Secret: "unit-secret"}
	degradedChain, degradedShutdown, degradedErr := composeGatewayChain(degradedDeps)
	if degradedErr != nil {
		t.Fatalf("compose degraded chain: %v", degradedErr)
	}
	defer degradedShutdown()
	degradedDispatcher := degradedChain.engine.FailureDispatcher.(*chainFailureDispatcher)
	if degradedDispatcher.healthDispatch == nil || degradedDispatcher.healthDispatch.outbox != nil {
		t.Fatal("health dispatch must stay inert without the outbox writer")
	}
	if degradedDispatcher.avoidanceProbe == nil {
		t.Fatal("avoidance probe must stay assembled for the fence-settlement contract")
	}
	// memory 驱动：无 Redis 客户端时 Store 保持 nil。
	if degradedDispatcher.turnRetry == nil || degradedDispatcher.turnRetry.Store != nil {
		t.Fatal("turn retry must keep the memory driver without redis")
	}
}
