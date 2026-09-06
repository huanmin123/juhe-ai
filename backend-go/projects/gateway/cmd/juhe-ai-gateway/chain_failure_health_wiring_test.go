package main

// 生效断言测试：失败派发链的三件登记装配——request-failure 健康检查派发桥
// （failure-dispatch.ts:404/571）、TurnAvoidanceProbeService 桥接装配
// （turn-availability-probe.service.ts + gatewaycircuit.ProbeCoordinator）、
// TurnRetryService 的 Redis 状态驱动（runtime-state-store.ts
// 'gateway-codex-turn-retry' 键空间）。逐条对照归档语义，装配断线即失败。

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	miniredis "github.com/alicebob/miniredis/v2"
	redis "github.com/redis/go-redis/v9"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycircuit"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycodex"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// ---------------------------------------------------------------------------
// 装配 1：jobs internal-api 健康检查派发桥
// ---------------------------------------------------------------------------

type healthDispatchStub struct {
	server   *httptest.Server
	requests int32
	bodies   []string
	mu       sync.Mutex
	status   int32
}

// newHealthDispatchStub 起一个 jobs internal-api 替身；status 决定响应码
// （202 = 派发接受，404 = 端点未挂载的生产现状）。
func newHealthDispatchStub(t *testing.T, status int) *healthDispatchStub {
	t.Helper()
	stub := &healthDispatchStub{status: int32(status)}
	stub.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&stub.requests, 1)
		raw, _ := io.ReadAll(r.Body)
		stub.mu.Lock()
		stub.bodies = append(stub.bodies, string(raw))
		stub.mu.Unlock()
		if r.URL.Path != chainHealthDispatchPath {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if signChainHealthDispatch("unit-secret", raw) != r.Header.Get("X-Juhe-Ai-Signature") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(int(atomic.LoadInt32(&stub.status)))
	}))
	t.Cleanup(stub.server.Close)
	return stub
}

func (s *healthDispatchStub) dispatcher() *chainRequestFailureHealthDispatcher {
	return newChainRequestFailureHealthDispatcher(s.server.URL, "unit-secret", s.server.Client())
}

func (s *healthDispatchStub) count() int { return int(atomic.LoadInt32(&s.requests)) }

func (s *healthDispatchStub) lastBody(t *testing.T) map[string]any {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.bodies) == 0 {
		t.Fatal("no dispatch body captured")
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(s.bodies[len(s.bodies)-1]), &payload); err != nil {
		t.Fatalf("decode dispatch body: %v", err)
	}
	return payload
}

// TestChainJobsHealthDispatchBridgeWire：桥的 wire 契约——路径、HMAC 签名、
// payload（version/accountId/reason）与 202→queued / 非 202→rejected。
func TestChainJobsHealthDispatchBridgeWire(t *testing.T) {
	stub := newHealthDispatchStub(t, http.StatusAccepted)
	dispatcher := stub.dispatcher()

	if !dispatcher.DispatchRequestFailureAccountHealthCheck(nil, gatewayTrafficSource, "acc_1") {
		t.Fatal("accepted dispatch must report dispatched")
	}
	payload := stub.lastBody(t)
	if payload["version"] != float64(1) || payload["accountId"] != "acc_1" || payload["reason"] != chainRequestFailureReason {
		t.Fatalf("dispatch payload = %#v", payload)
	}
	if stub.count() != 1 {
		t.Fatalf("dispatch posts = %d want 1", stub.count())
	}

	// 生产现状（jobs 端点未挂载 → 404）：派发按不可用拒绝并如实返回 false。
	rejecting := newHealthDispatchStub(t, http.StatusNotFound)
	if rejecting.dispatcher().DispatchRequestFailureAccountHealthCheck(nil, gatewayTrafficSource, "acc_1") {
		t.Fatal("rejected dispatch must report not dispatched")
	}

	// 空账户 ID → dispatch_rejected（Node dispatch_rejected 分叉）。
	empty := newHealthDispatchStub(t, http.StatusAccepted)
	if empty.dispatcher().DispatchRequestFailureAccountHealthCheck(nil, gatewayTrafficSource, "  ") {
		t.Fatal("empty account id must be rejected")
	}
	if empty.count() != 0 {
		t.Fatalf("rejected dispatch must not post, posts=%d", empty.count())
	}
}

// TestChainRequestFailureHealthDispatcherThrottle：请求级去重（Node Symbol
// 标记语义）——同一请求只派发一次，rejected 不消耗标记，非 gateway 流量不派发。
func TestChainRequestFailureHealthDispatcherThrottle(t *testing.T) {
	stub := newHealthDispatchStub(t, http.StatusAccepted)
	dispatcher := stub.dispatcher()
	req := gatewaypreauth.NewGatewayRequest(httptest.NewRequest(http.MethodPost, "http://gateway.local/v1/chat/completions", nil))

	if !dispatcher.DispatchRequestFailureAccountHealthCheck(req, gatewayTrafficSource, "acc_1") {
		t.Fatal("first dispatch must go through")
	}
	if dispatcher.DispatchRequestFailureAccountHealthCheck(req, gatewayTrafficSource, "acc_1") {
		t.Fatal("second dispatch on the same request must be throttled")
	}
	if stub.count() != 1 {
		t.Fatalf("posts = %d want 1 (per-request throttle)", stub.count())
	}

	// 非 gateway 流量：不派发、不标记。
	other := gatewaypreauth.NewGatewayRequest(httptest.NewRequest(http.MethodPost, "http://gateway.local/v1/chat/completions", nil))
	if dispatcher.DispatchRequestFailureAccountHealthCheck(other, "account_diagnostic", "acc_1") {
		t.Fatal("non-gateway traffic must not dispatch")
	}
	if stub.count() != 1 {
		t.Fatalf("non-gateway posts leaked: %d", stub.count())
	}
	// 同一请求后续 gateway 失败仍可派发（标记只由成功派发写入）。
	if !dispatcher.DispatchRequestFailureAccountHealthCheck(other, gatewayTrafficSource, "acc_1") {
		t.Fatal("gateway dispatch after a skipped non-gateway call must go through")
	}
	if stub.count() != 2 {
		t.Fatalf("posts = %d want 2", stub.count())
	}
}

// TestChainFailureDispatcherDispatchesRequestFailureHealthCheck：failure-dispatch.ts
// 两个触发点——传输失败分支（:571）与 failed-response 分支（:404，system
// quota 决策除外）都经请求级节流派发一次。
func TestChainFailureDispatcherDispatchesRequestFailureHealthCheck(t *testing.T) {
	// 传输失败分支：gateway 流量派发一次。
	stub := newHealthDispatchStub(t, http.StatusAccepted)
	sink := &failureDispatchAuditSink{}
	dispatcher := &chainFailureDispatcher{healthDispatch: stub.dispatcher()}
	input := avoidanceRecordRequestInput(t, sink, "gateway")
	if _, err := dispatcher.HandleUpstreamRequestError(context.Background(), input); err != nil {
		t.Fatalf("transport failure: %v", err)
	}
	if stub.count() != 1 {
		t.Fatalf("transport branch posts = %d want 1", stub.count())
	}
	if payload := stub.lastBody(t); payload["reason"] != chainRequestFailureReason {
		t.Fatalf("reason = %#v", payload["reason"])
	}

	// 同一请求的第二次失败（候选切换）被请求级节流吸收。
	if _, err := dispatcher.HandleUpstreamRequestError(context.Background(), input); err != nil {
		t.Fatalf("second transport failure: %v", err)
	}
	if stub.count() != 1 {
		t.Fatalf("per-request throttle failed: posts = %d", stub.count())
	}

	// failed-response 分支：无显式决策（500 普通失败）→ 派发。
	failedStub := newHealthDispatchStub(t, http.StatusAccepted)
	failedDispatcher := &chainFailureDispatcher{healthDispatch: failedStub.dispatcher()}
	opaque := gatewayFailedResponseInput(
		failureDispatchUpstreamResponse(t, http.StatusInternalServerError, "application/json", `{"error":{"message":"boom"}}`),
		&failureDispatchAuditSink{}, "gateway")
	opaque.Req = input.Req
	if _, err := failedDispatcher.HandleFailedUpstreamResponse(context.Background(), opaque); err != nil {
		t.Fatalf("opaque failure: %v", err)
	}
	if failedStub.count() != 1 {
		t.Fatalf("failed-response branch posts = %d want 1", failedStub.count())
	}

	// system quota 决策（402 + insufficient_quota）→ 不派发：探活不得与
	// 显式错误状态竞争（failure-dispatch.ts:396-404 注释）。
	systemStub := newHealthDispatchStub(t, http.StatusAccepted)
	systemDispatcher := &chainFailureDispatcher{policy: newFixedErrorPolicyService(nil), healthDispatch: systemStub.dispatcher()}
	systemInput := gatewayFailedResponseInput(
		failureDispatchUpstreamResponse(t, http.StatusPaymentRequired, "application/json",
			`{"error":{"code":"insufficient_quota","message":"insufficient quota"}}`),
		&failureDispatchAuditSink{}, "gateway")
	systemInput.Req = input.Req
	systemInput.Settings = gatewayruntimecache.GatewaySettings{DefaultTemporaryUnschedulableMinutes: 30}
	if _, err := systemDispatcher.HandleFailedUpstreamResponse(context.Background(), systemInput); err != nil {
		t.Fatalf("system quota failure: %v", err)
	}
	if systemStub.count() != 0 {
		t.Fatalf("system-quota decision must not dispatch, posts = %d", systemStub.count())
	}
}

// ---------------------------------------------------------------------------
// 装配 2：TurnAvoidanceProbeService + memory probe-state store
// ---------------------------------------------------------------------------

// TestChainTurnAvoidanceProbeServiceRunsProbe：装配后的探活服务真实走通
// Acquire → dispatch(source fence) → settle 契约——派发桥收到带 source_fence
// 的 payload，协调器进入 dispatch-pending 的 owner 态。
func TestChainTurnAvoidanceProbeServiceRunsProbe(t *testing.T) {
	stub := newHealthDispatchStub(t, http.StatusAccepted)
	dispatcher := stub.dispatcher()
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
	if stub.count() != 1 {
		t.Fatalf("probe dispatch posts = %d want 1", stub.count())
	}
	payload := stub.lastBody(t)
	fence, _ := payload["sourceFence"].(map[string]any)
	if fence == nil || fence["state_key"] != "src-key" || fence["account_id"] != "acc_1" {
		t.Fatalf("probe payload source fence missing: %#v", payload)
	}
	if payload["reason"] != chainRequestFailureReason {
		t.Fatalf("probe reason = %#v", payload["reason"])
	}

	// 派发被拒（jobs 404 的生产现状）→ probe_task_failure 结算（Node 契约：
	// 快速拒绝必须结算 fence，不能搁浅 generation）。
	rejecting := newHealthDispatchStub(t, http.StatusNotFound)
	rejectingProbe := newChainTurnAvoidanceProbeService(turnRetry, gatewaypreauth.SystemClock{}, rejecting.dispatcher())
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
// 在 deps 齐备时把三件协作器挂进失败派发器——健康检查桥、探活服务（含协调器
// 与默认派发）、turn-retry 的 Redis 状态驱动。
func TestComposeGatewayChainWiresFailureDispatchCollaborators(t *testing.T) {
	fixture := newChainFixture(t)
	stub := newHealthDispatchStub(t, http.StatusAccepted)
	redisServer := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	t.Cleanup(func() { _ = redisClient.Close() })
	turnRetryStore, err := newChainTurnRetryRedisStateStore(redisClient, "dev")
	if err != nil {
		t.Fatalf("build turn retry store: %v", err)
	}

	deps := chainSmokeDeps(t, fixture, gatewaypreauth.SystemClock{}, "")
	deps.Identity = &sessionIdentityServices{Secret: "unit-secret"}
	deps.JobsInternalURL = stub.server.URL
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
	if dispatcher.healthDispatch == nil {
		t.Fatal("request-failure health dispatch bridge missing")
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

	// 缺 URL 的装配保持显式降级：派发端口为 nil，探活服务仍装配（派发按
	// input_unavailable 拒绝）。
	degradedDeps := chainSmokeDeps(t, fixture, gatewaypreauth.SystemClock{}, "")
	degradedDeps.Identity = &sessionIdentityServices{Secret: "unit-secret"}
	degradedChain, degradedShutdown, degradedErr := composeGatewayChain(degradedDeps)
	if degradedErr != nil {
		t.Fatalf("compose degraded chain: %v", degradedErr)
	}
	defer degradedShutdown()
	degradedDispatcher := degradedChain.engine.FailureDispatcher.(*chainFailureDispatcher)
	if degradedDispatcher.healthDispatch != nil {
		t.Fatal("health dispatch must stay unwired without the jobs target")
	}
	if degradedDispatcher.avoidanceProbe == nil {
		t.Fatal("avoidance probe must stay assembled for the fence-settlement contract")
	}
	// memory 驱动：无 Redis 客户端时 Store 保持 nil。
	if degradedDispatcher.turnRetry == nil || degradedDispatcher.turnRetry.Store != nil {
		t.Fatal("turn retry must keep the memory driver without redis")
	}
}
