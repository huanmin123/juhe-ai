package gatewaydispatch

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	circuitruntime "github.com/huanminabc/juhe-ai/backend-go-gateway/internal/business/circuit_runtime"

	"github.com/alicebob/miniredis/v2"
	redis "github.com/redis/go-redis/v9"
)

// wkFixedRevisionReader 以固定分页供给运行态索引 backfill。
type wkFixedRevisionReader struct {
	page circuitruntime.GatewayAccountCircuitDispatchRevisionPage
	err  error
}

func (r wkFixedRevisionReader) ListGatewayAccountCircuitDispatchRevisions(context.Context, circuitruntime.GatewayAccountCircuitDispatchRevisionPageInput) (circuitruntime.GatewayAccountCircuitDispatchRevisionPage, error) {
	return r.page, r.err
}

// wkRuntimeGate 组装一个索引已 ready 的真实运行态存储，供 RuntimeCircuitGate 驱动。
func wkRuntimeGate(t *testing.T) (RuntimeCircuitGate, *miniredis.Miniredis, *redis.Client) {
	t.Helper()
	server := miniredis.RunT(t)
	store, err := circuitruntime.New(circuitruntime.Config{URL: "redis://" + server.Addr(), Namespace: "wk-gate"}, circuitruntime.OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	result, err := store.BackfillRuntimeIndex(context.Background(), circuitruntime.GatewayAccountCircuitRuntimeIndexBackfillInput{OwnerID: "wk-gate-owner"},
		wkFixedRevisionReader{page: circuitruntime.GatewayAccountCircuitDispatchRevisionPage{Items: []circuitruntime.GatewayAccountCircuitDispatchRevisionSnapshot{{AccountID: "a1", DispatchRevision: 1}, {AccountID: "a2", DispatchRevision: 1}}}})
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if result.RevisionCount != 2 {
		t.Fatalf("backfill revision count=%d", result.RevisionCount)
	}
	if err := store.CheckReady(context.Background()); err != nil {
		t.Fatalf("index not ready: %v", err)
	}
	raw := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = raw.Close() })
	return RuntimeCircuitGate{Store: store}, server, raw
}

func wkAccountCircuitScopeKey(t *testing.T, accountID string) string {
	t.Helper()
	key, err := circuitruntime.GatewayAccountCircuitScopeKey(circuitruntime.GatewayAccountCircuitScope{Kind: circuitruntime.GatewayAccountCircuitScopeAccount, AccountRuntimeKey: accountID})
	if err != nil {
		t.Fatal(err)
	}
	return key
}

// wkPatchRetryAt 把运行态的 retryAt 改写到过去，模拟退避到期。
func wkPatchRetryAt(t *testing.T, raw *redis.Client, accountID string, retryAtMS int64) {
	t.Helper()
	ctx := context.Background()
	statesKey := "juhe-ai:wk-gate:account-circuit:gateway-account-circuit:states"
	rawState, err := raw.HGet(ctx, statesKey, wkAccountCircuitScopeKey(t, accountID)).Result()
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	var envelope map[string]any
	if err := json.Unmarshal([]byte(rawState), &envelope); err != nil {
		t.Fatal(err)
	}
	state, ok := envelope["state"].(map[string]any)
	if !ok {
		t.Fatal("运行态信封缺少 state 对象")
	}
	if retryAtMS <= 0 {
		delete(state, "retryAtMs")
	} else {
		state["retryAtMs"] = retryAtMS
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if err := raw.HSet(ctx, statesKey, wkAccountCircuitScopeKey(t, accountID), string(encoded)).Err(); err != nil {
		t.Fatal(err)
	}
}

// wkPatchPhase 直接改写运行态相位（仅测试注入用）。
func wkPatchPhase(t *testing.T, raw *redis.Client, accountID, phase string) {
	t.Helper()
	ctx := context.Background()
	statesKey := "juhe-ai:wk-gate:account-circuit:gateway-account-circuit:states"
	rawState, err := raw.HGet(ctx, statesKey, wkAccountCircuitScopeKey(t, accountID)).Result()
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	var envelope map[string]any
	if err := json.Unmarshal([]byte(rawState), &envelope); err != nil {
		t.Fatal(err)
	}
	state, ok := envelope["state"].(map[string]any)
	if !ok {
		t.Fatal("运行态信封缺少 state 对象")
	}
	state["phase"] = phase
	// 相位补写同时清掉 half-open 起源，避免组合出非法状态。
	delete(state, "halfOpenOrigin")
	encoded, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if err := raw.HSet(ctx, statesKey, wkAccountCircuitScopeKey(t, accountID), string(encoded)).Err(); err != nil {
		t.Fatal(err)
	}
}

func wkAccountCircuitInput() *AccountCircuitInput {
	return &AccountCircuitInput{AccountID: "a1", RequestLane: "text", Model: "m", DispatchRevision: 1, ConfirmationLeaseDuration: time.Minute, ConfirmationEligible: true, FailureEvidenceKey: "ev-1"}
}

func TestRuntimeGatePrepareValidation(t *testing.T) {
	ctx := context.Background()
	if _, _, err := (RuntimeCircuitGate{}).Prepare(ctx, AccountCircuitInput{AccountID: "a1"}); err == nil {
		t.Fatal("nil store 必须失败")
	}
	gate, _, _ := wkRuntimeGate(t)
	if _, _, err := gate.Prepare(ctx, AccountCircuitInput{AccountID: "   "}); err == nil {
		t.Fatal("空 account id 必须失败")
	}
	server := miniredis.RunT(t)
	blocked, err := circuitruntime.New(circuitruntime.Config{URL: "redis://" + server.Addr(), Namespace: "wk-gate-blocked"}, circuitruntime.OwnerGate{Confirmed: true, SchemaReady: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = blocked.Close() })
	if _, _, err := (RuntimeCircuitGate{Store: blocked}).Prepare(ctx, AccountCircuitInput{AccountID: "a1"}); err == nil {
		t.Fatal("gate 未就绪时 Prepare 必须失败")
	}
}

func TestRuntimeGateClosedCircuitLifecycle(t *testing.T) {
	gate, _, raw := wkRuntimeGate(t)
	ctx := context.Background()
	decision, attempt, err := gate.Prepare(ctx, *wkAccountCircuitInput())
	if err != nil || decision != AccountCircuitDispatchable {
		t.Fatalf("closed 电路应直接放行: %s %v", decision, err)
	}
	if attempt == nil {
		t.Fatal("dispatchable 必须返回 attempt")
	}
	closed := attempt.(*runtimeCircuitAttempt)
	if closed.leaseID != "" {
		t.Fatalf("closed 电路不应带租约: %q", closed.leaseID)
	}
	// 无租约时 framing/unknown 都是中性 no-op。
	if err := attempt.ReportFramingComplete(ctx); err != nil {
		t.Fatalf("closed framing: %v", err)
	}
	_, unknownAttempt, err := gate.Prepare(ctx, *wkAccountCircuitInput())
	if err != nil {
		t.Fatal(err)
	}
	if err := unknownAttempt.ReportUnknown(ctx); err != nil {
		t.Fatalf("closed unknown 必须中性: %v", err)
	}
	// 传输失败必须把电路打成 SUSPECT（用全新 attempt，前面的已结算）。
	_, failureAttempt, err := gate.Prepare(ctx, *wkAccountCircuitInput())
	if err != nil {
		t.Fatal(err)
	}
	if err := failureAttempt.ReportTransportFailure(ctx, errors.New("conn reset")); err != nil {
		t.Fatalf("transport failure: %v", err)
	}
	state, err := gate.Store.GetGatewayAccountCircuit(ctx, circuitruntime.GatewayAccountCircuitGetInput{
		AccountID: "a1",
		Scope:     circuitruntime.GatewayAccountCircuitScope{Kind: circuitruntime.GatewayAccountCircuitScopeAccount, AccountRuntimeKey: "a1"},
		Now:       time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if state.Phase != circuitruntime.GatewayAccountCircuitPhaseSuspect {
		t.Fatalf("传输失败后电路未进入 SUSPECT: %+v", state)
	}
	// RetryAt 在未来时必须被拦截（不论是否具备确认资格）。
	blockedDecision, blockedAttempt, err := gate.Prepare(ctx, *wkAccountCircuitInput())
	if err != nil || blockedDecision != AccountCircuitBlocked || blockedAttempt != nil {
		t.Fatalf("退避期内必须拦截: %s %v", blockedDecision, err)
	}
	// 把 retryAt 改写到过去后，不具备确认资格的请求必须被拦截。
	wkPatchRetryAt(t, raw, "a1", 1000)
	ineligible := wkAccountCircuitInput()
	ineligible.ConfirmationEligible = false
	ineligible.FailureEvidenceKey = "ev-2"
	blockedDecision, _, err = gate.Prepare(ctx, *ineligible)
	if err != nil || blockedDecision != AccountCircuitBlocked {
		t.Fatalf("无确认资格必须拦截: %s %v", blockedDecision, err)
	}
	// 具备确认资格的请求获得确认租约。
	eligible := wkAccountCircuitInput()
	eligible.FailureEvidenceKey = "ev-3"
	confirmDecision, confirmAttempt, err := gate.Prepare(ctx, *eligible)
	if err != nil || confirmDecision != AccountCircuitDispatchable {
		t.Fatalf("确认探测被拒: %s %v", confirmDecision, err)
	}
	confirmed := confirmAttempt.(*runtimeCircuitAttempt)
	if confirmed.leaseID == "" || confirmed.leaseKind != circuitruntime.GatewayAccountCircuitLeaseConfirmation {
		t.Fatalf("未取得确认租约: %q %q", confirmed.leaseID, confirmed.leaseKind)
	}
	// framing 完成通过确认租约结算。
	if err := confirmAttempt.ReportFramingComplete(ctx); err != nil {
		t.Fatalf("确认结算 framing: %v", err)
	}
	// 结算后重复上报必须被本地围栏吞掉。
	if err := confirmAttempt.ReportTransportFailure(ctx, nil); err != nil {
		t.Fatalf("settled attempt 上报必须为 no-op: %v", err)
	}
	// 在第二个账户上复现：确认租约下上报传输失败，走 completeLease(confirmation)
	// 结算路径（cause 为 nil 覆盖 reason 拼接的另一分支）。
	a2 := wkAccountCircuitInput()
	a2.AccountID = "a2"
	a2.FailureEvidenceKey = "ev-a2-open"
	_, a2First, err := gate.Prepare(ctx, *a2)
	if err != nil {
		t.Fatal(err)
	}
	if err := a2First.ReportTransportFailure(ctx, errors.New("conn reset")); err != nil {
		t.Fatal(err)
	}
	wkPatchRetryAt(t, raw, "a2", 1000)
	confirmA2 := wkAccountCircuitInput()
	confirmA2.AccountID = "a2"
	confirmA2.FailureEvidenceKey = "ev-a2-confirm"
	_, a2Confirm, err := gate.Prepare(ctx, *confirmA2)
	if err != nil {
		t.Fatalf("a2 确认探测: %v", err)
	}
	if a2Confirm == nil {
		t.Fatal("a2 确认探测被拦截")
	}
	if err := a2Confirm.ReportTransportFailure(ctx, nil); err != nil {
		t.Fatalf("确认租约下的传输失败结算: %v", err)
	}
}

func TestRuntimeGateCanaryLeaseLifecycle(t *testing.T) {
	gate, _, raw := wkRuntimeGate(t)
	ctx := context.Background()
	// 先制造 SUSPECT，再把相位补写到 RECOVERING 以驱动 canary 租约路径。
	_, firstAttempt, err := gate.Prepare(ctx, *wkAccountCircuitInput())
	if err != nil {
		t.Fatal(err)
	}
	if err := firstAttempt.ReportTransportFailure(ctx, errors.New("boom")); err != nil {
		t.Fatal(err)
	}
	wkPatchRetryAt(t, raw, "a1", 1000)
	wkPatchPhase(t, raw, "a1", string(circuitruntime.GatewayAccountCircuitPhaseRecovering))
	canaryInput := wkAccountCircuitInput()
	canaryInput.FailureEvidenceKey = "ev-canary"
	decision, attempt, err := gate.Prepare(ctx, *canaryInput)
	if err != nil || decision != AccountCircuitDispatchable {
		t.Fatalf("canary 探测被拒: %s %v", decision, err)
	}
	canary := attempt.(*runtimeCircuitAttempt)
	if canary.leaseKind != circuitruntime.GatewayAccountCircuitLeaseHalfOpen {
		t.Fatalf("应取得 canary 租约: %q", canary.leaseKind)
	}
	// canary 租约持有期间再次 Prepare 必须被拦截（租约互斥）。
	again := wkAccountCircuitInput()
	again.FailureEvidenceKey = "ev-canary-2"
	againDecision, againAttempt, err := gate.Prepare(ctx, *again)
	if err != nil || againDecision != AccountCircuitBlocked || againAttempt != nil {
		t.Fatalf("租约互斥期内必须拦截: %s %v", againDecision, err)
	}
	// framing 完成必须通过 canary 租约结算。
	if err := attempt.ReportFramingComplete(ctx); err != nil {
		t.Fatalf("canary framing: %v", err)
	}
	// unknown 生命周期必须通过租约结算为中性结果，而不是误判失败。
	// （租约互斥验证之后，改在 a2 的首个 canary 周期上驱动 unknown 结算。）
	a2Open := wkAccountCircuitInput()
	a2Open.AccountID = "a2"
	a2Open.FailureEvidenceKey = "ev-a2-open"
	_, a2SuspectAttempt, err := gate.Prepare(ctx, *a2Open)
	if err != nil {
		t.Fatal(err)
	}
	if err := a2SuspectAttempt.ReportTransportFailure(ctx, errors.New("boom")); err != nil {
		t.Fatal(err)
	}
	wkPatchRetryAt(t, raw, "a2", 1000)
	wkPatchPhase(t, raw, "a2", string(circuitruntime.GatewayAccountCircuitPhaseRecovering))
	a2Canary := wkAccountCircuitInput()
	a2Canary.AccountID = "a2"
	a2Canary.FailureEvidenceKey = "ev-a2-canary"
	_, a2CanaryAttempt, err := gate.Prepare(ctx, *a2Canary)
	if err != nil {
		t.Fatalf("a2 canary 探测: %v", err)
	}
	if a2CanaryAttempt == nil {
		t.Fatal("a2 canary 探测被拦截")
	}
	if err := a2CanaryAttempt.ReportUnknown(ctx); err != nil {
		t.Fatalf("canary unknown: %v", err)
	}
	if !a2CanaryAttempt.(*runtimeCircuitAttempt).settled {
		t.Fatal("canary unknown 未结算本地 attempt")
	}
}
