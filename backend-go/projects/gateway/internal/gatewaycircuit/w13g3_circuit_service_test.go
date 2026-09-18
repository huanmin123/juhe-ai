package gatewaycircuit

// w13g3：PrepareAttempt 的确认路径分支（不合格确认、作用域不匹配、同证据
// 阻断、suspect 状态下的确认获取与父作用域失配）、store 错误传播、
// ReportTransportFailure 参数校验。全部内存 store 驱动。

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

var errW13g3Circuit = errors.New("w13g3 circuit 注入错误")

// w13g3ErrorGetStore 只在 Get 上注入错误。
type w13g3ErrorGetStore struct {
	Store
	getErr error
}

func (w *w13g3ErrorGetStore) Get(ctx context.Context, scope Scope, nowMs *int64) (State, error) {
	if w.getErr != nil {
		return State{}, w.getErr
	}
	return w.Store.Get(ctx, scope, nowMs)
}

func (w *w13g3ErrorGetStore) Suspect(ctx context.Context, input SuspectInput) (MutationResult, error) {
	return w.Store.Suspect(ctx, input)
}

// w13g3SuspectAccount 把协议模型作用域推入 suspect（携带证据键）。
func nilOrW13g3Evidence(evidence string) *string {
	if evidence == "" {
		return nil
	}
	return strPtr(evidence)
}

func w13g3SuspectAccount(t *testing.T, service *CircuitService, store Store, clock *int64, account gatewayruntimecache.OpenAIAccountSecret, model string, evidence string) Scope {
	t.Helper()
	ctx := context.Background()
	scope := protocolModelScope(account, LaneText, strPtr(model))
	*clock += 10_000
	if _, err := service.SuspectForegroundFailure(ctx, suspectForegroundInput{
		scope: scope, dispatchRevision: revisionOf(t, account),
		confirmationFailuresRequired: int64Ptr(2), reason: "transport:down",
		failureEvidenceKey: nilOrW13g3Evidence(evidence),
	}); err != nil {
		t.Fatalf("suspect: %v", err)
	}
	state, err := store.Get(ctx, scope, nil)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if state.Phase != PhaseSuspect {
		t.Fatalf("phase = %s, want suspect", state.Phase)
	}
	return scope
}

func TestW13g3PrepareAttemptSuspectConfirmationArms(t *testing.T) {
	now := int64(0)
	clock := &now
	store, _ := NewMemoryStore(MemoryStoreOptions{Capacity: 100, Now: func() int64 { return *clock }, Random: func() float64 { return 0.5 }})
	service, _ := newTestService(t, store, ServiceOptions{Now: func() int64 { return *clock }})
	account := testAccount()
	model := "gpt-4o"
	ctx := context.Background()

	w13g3SuspectAccount(t, service, store, clock, account, model, strings.Repeat("a", 64))

	// evidence 为空 → 阻断（694-698 前半）。
	blocked, err := service.PrepareAttempt(ctx, PrepareAttemptInput{
		Account: account, RequestLane: LaneText, Model: &model,
		ConfirmationLeaseDurationMs: 30_000,
	})
	if err != nil {
		t.Fatalf("prepare without evidence: %v", err)
	}
	if blocked.Outcome != PrepareBlocked {
		t.Fatalf("missing evidence must block, got %s", blocked.Outcome)
	}

	// 相同证据 → 阻断（695-698 后半）。
	same := strings.Repeat("a", 64)
	blocked, err = service.PrepareAttempt(ctx, PrepareAttemptInput{
		Account: account, RequestLane: LaneText, Model: &model,
		ConfirmationLeaseDurationMs: 30_000, FailureEvidenceKey: &same,
	})
	if err != nil {
		t.Fatalf("prepare with same evidence: %v", err)
	}
	if blocked.Outcome != PrepareBlocked {
		t.Fatalf("same evidence must block, got %s", blocked.Outcome)
	}

	// 新证据 → 派出观察者/确认尝试（737-746 或 703-721）。
	*clock += 70_000
	fresh := strings.Repeat("b", 64)
	confirmed, err := service.PrepareAttempt(ctx, PrepareAttemptInput{
		Account: account, RequestLane: LaneText, Model: &model,
		ConfirmationLeaseDurationMs: 30_000, FailureEvidenceKey: &fresh,
	})
	if err != nil {
		t.Fatalf("prepare with fresh evidence: %v", err)
	}
	if confirmed.Outcome != PrepareDispatchable || confirmed.Attempt == nil {
		t.Fatalf("fresh evidence must dispatch, got %s", confirmed.Outcome)
	}

	// ConfirmationEligible=false 在 suspect 上 → 阻断（690-693）。
	eligibleFalse := false
	blocked, err = service.PrepareAttempt(ctx, PrepareAttemptInput{
		Account: account, RequestLane: LaneText, Model: &model,
		ConfirmationLeaseDurationMs: 30_000, FailureEvidenceKey: strPtr(strings.Repeat("d", 64)),
		ConfirmationEligible: &eligibleFalse,
	})
	if err != nil {
		t.Fatalf("prepare ineligible: %v", err)
	}
	if blocked.Outcome != PrepareBlocked {
		t.Fatalf("ineligible confirmation must block, got %s", blocked.Outcome)
	}
}

func TestW13g3PrepareAttemptWithConfirmationInput(t *testing.T) {
	now := int64(0)
	clock := &now
	store, _ := NewMemoryStore(MemoryStoreOptions{Capacity: 100, Now: func() int64 { return *clock }, Random: func() float64 { return 0.5 }})
	service, _ := newTestService(t, store, ServiceOptions{Now: func() int64 { return *clock }})
	account := testAccount()
	model := "gpt-4o"
	ctx := context.Background()

	w13g3SuspectAccount(t, service, store, clock, account, model, "")
	*clock += 10_000
	fresh := strings.Repeat("b", 64)
	confirmed, err := service.PrepareAttempt(ctx, PrepareAttemptInput{
		Account: account, RequestLane: LaneText, Model: &model,
		ConfirmationLeaseDurationMs: 30_000, FailureEvidenceKey: &fresh,
	})
	if err != nil || confirmed.Attempt == nil {
		t.Fatalf("confirmation prepare: %v", err)
	}
	confirmation := w14eConfirmationOf(confirmed.Attempt)
	if confirmation == nil {
		t.Logf("DEBUG isObserver=%v isConf=%v", confirmed.Attempt.IsObserver, confirmed.Attempt.IsConfirmation())
		t.Fatal("attempt must carry confirmation")
	}

	// 同一确认再次进入 → 命中租约匹配分支放行（654-657）。
	*clock += 10_000
	again, err := service.PrepareAttempt(ctx, PrepareAttemptInput{
		Account: account, RequestLane: LaneText, Model: &model,
		ConfirmationLeaseDurationMs: 30_000, FailureEvidenceKey: &fresh,
		Confirmation: confirmation,
	})
	if err != nil {
		t.Fatalf("prepare with confirmation: %v", err)
	}
	if again.Outcome != PrepareDispatchable || again.Attempt == nil || !again.Attempt.IsConfirmation() {
		t.Fatalf("matching confirmation must dispatch, got %s", again.Outcome)
	}

	// ConfirmationEligible=false → 完成确认并阻断（615-626）。
	eligibleFalse := false
	blocked, err := service.PrepareAttempt(ctx, PrepareAttemptInput{
		Account: account, RequestLane: LaneText, Model: &model,
		ConfirmationLeaseDurationMs: 30_000, FailureEvidenceKey: &fresh,
		Confirmation: confirmation, ConfirmationEligible: &eligibleFalse,
	})
	if err != nil {
		t.Fatalf("prepare with ineligible confirmation: %v", err)
	}
	if blocked.Outcome != PrepareBlocked {
		t.Fatalf("ineligible confirmation input must block, got %s", blocked.Outcome)
	}

	// 作用域键不匹配 → 阻断（627-636）。
	mismatched := *confirmation
	mismatched.ScopeKey = "mismatched-scope"
	other := strings.Repeat("e", 64)
	blocked, err = service.PrepareAttempt(ctx, PrepareAttemptInput{
		Account: account, RequestLane: LaneText, Model: &model,
		ConfirmationLeaseDurationMs: 30_000, FailureEvidenceKey: &other,
		Confirmation: &mismatched,
	})
	if err != nil {
		t.Fatalf("prepare with mismatched confirmation: %v", err)
	}
	if blocked.Outcome != PrepareBlocked {
		t.Fatalf("mismatched confirmation must block, got %s", blocked.Outcome)
	}
}

func TestW13g3PrepareAttemptStoreErrors(t *testing.T) {
	store := newNonExpiringMemoryStore(t)
	wrapped := &w13g3ErrorGetStore{Store: store}
	service, _ := newTestService(t, wrapped, ServiceOptions{})
	account := testAccount()
	model := "gpt-4o"

	// 作用域 Get 错误向上传播（660-663）。
	wrapped.getErr = errW13g3Circuit
	if _, err := service.PrepareAttempt(context.Background(), PrepareAttemptInput{
		Account: account, RequestLane: LaneText, Model: &model, ConfirmationLeaseDurationMs: 30_000,
	}); !errors.Is(err, errW13g3Circuit) {
		t.Fatalf("expected store get error, got %v", err)
	}
	wrapped.getErr = nil

}

func TestW13g3PrepareAttemptReplaceRevision(t *testing.T) {
	store := newNonExpiringMemoryStore(t)
	service, _ := newTestService(t, store, ServiceOptions{})
	account := testAccount()
	rev1 := int64(1)
	account.DispatchRevision = &rev1
	model := "gpt-4o"
	ctx := context.Background()

	// 先以 rev-1 建立作用域状态。
	if _, err := service.PrepareAttempt(ctx, PrepareAttemptInput{
		Account: account, RequestLane: LaneText, Model: &model, ConfirmationLeaseDurationMs: 30_000,
	}); err != nil {
		t.Fatalf("prepare rev-1: %v", err)
	}

	// 账户升级到 rev-2 → 作用域状态迁移替换后仍放行（664-682）。
	rev2 := int64(2)
	account.DispatchRevision = &rev2
	result, err := service.PrepareAttempt(ctx, PrepareAttemptInput{
		Account: account, RequestLane: LaneText, Model: &model, ConfirmationLeaseDurationMs: 30_000,
	})
	if err != nil {
		t.Fatalf("prepare rev-2: %v", err)
	}
	if result.Outcome != PrepareDispatchable {
		t.Fatalf("revision replacement must keep dispatching, got %s", result.Outcome)
	}
}

func TestW13g3ReportTransportFailureValidation(t *testing.T) {
	store := newNonExpiringMemoryStore(t)
	service, _ := newTestService(t, store, ServiceOptions{})
	ctx := context.Background()
	result, err := service.PrepareAttempt(ctx, PrepareAttemptInput{
		Account: testAccount(), RequestLane: LaneText,
		Model: strPtr("gpt-4o"), ConfirmationLeaseDurationMs: 30_000,
	})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	// 空 reason 校验。
	if _, err := result.Attempt.ReportTransportFailure(ctx, TransportFailure{Kind: TransportFailureKindTransport, Reason: " "}); err == nil {
		t.Fatal("blank failure reason must fail")
	}
	// 带确认的尝试结算路径（hasConfirmation 分支）。
	if _, err := result.Attempt.ReportTransportFailure(ctx, TransportFailure{Kind: TransportFailureKindTimeout, Reason: "超时"}); err != nil {
		t.Fatalf("report timeout: %v", err)
	}
	scopeKey, err := ScopeKey(result.Attempt.Scope)
	if err != nil || !strings.Contains(scopeKey, "unknown") {
		t.Fatalf("scope key = %q err = %v", scopeKey, err)
	}
}
