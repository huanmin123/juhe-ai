package gatewaycircuit

// T91-6 / B2-M5 单元钉住：CheckDispatchAdmission 是 PrepareAttempt 的只读准入
// 判定——当前配置代的 OPEN/SUSPECT 阻断且不写状态；旧配置代（dispatchRevision
// 漂移）与空状态等价 CLOSED 放行；运行态未就绪 fail-closed 阻断。

import (
	"context"
	"reflect"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

func t91AdmissionAccount(revision int64) gatewayruntimecache.OpenAIAccountSecret {
	revisionValue := revision
	return gatewayruntimecache.OpenAIAccountSecret{
		ID:               "a-t91",
		Type:             "api_key",
		Status:           "active",
		APIKey:           "key-a-t91",
		ProtocolCode:     "openai",
		ProtocolVersion:  "v1",
		DispatchRevision: &revisionValue,
		SupportedModels:  []string{"gpt-test"},
	}
}

// t91AdmissionFixture 构造独立 store/service 与同源 scope/input。
func t91AdmissionFixture(t *testing.T, revision int64, options ServiceOptions) (*MemoryStore, *CircuitService, Scope, PrepareAttemptInput) {
	t.Helper()
	account := t91AdmissionAccount(revision)
	store, err := NewMemoryStore(MemoryStoreOptions{Capacity: 64})
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	service, err := NewCircuitService(store, options)
	if err != nil {
		t.Fatalf("service: %v", err)
	}
	model := "gpt-test"
	scope, err := GatewayAccountProtocolModelScope(account, LaneText, &model)
	if err != nil {
		t.Fatalf("scope: %v", err)
	}
	return store, service, Scope{Kind: ScopeKindAccount, AccountRuntimeKey: scope.AccountRuntimeKey},
		PrepareAttemptInput{Account: account, RequestLane: LaneText, Model: &model}
}

func TestT91CheckDispatchAdmissionEmptyStateAdmits(t *testing.T) {
	store, service, accountScope, input := t91AdmissionFixture(t, 3, ServiceOptions{})
	admitted, err := service.CheckDispatchAdmission(context.Background(), input)
	if err != nil || admitted.Outcome != PrepareDispatchable {
		t.Fatalf("空状态必须放行: %+v err=%v", admitted, err)
	}
	if admitted.Attempt != nil {
		t.Fatal("准入判定不得返回 Attempt（避免重复结算/confirmation）")
	}
	// 放行也不写状态：空 store 仍无条目（Get 返回默认 CLOSED 且 revision 为空）。
	state, err := store.Get(context.Background(), accountScope, nil)
	if err != nil || state.DispatchRevision != "" {
		t.Fatalf("准入不得写状态: %+v err=%v", state, err)
	}
}

func TestT91CheckDispatchAdmissionCurrentRevisionBlocks(t *testing.T) {
	ctx := context.Background()
	store, service, accountScope, input := t91AdmissionFixture(t, 3, ServiceOptions{})
	open := ClosedState(accountScope, "3", 1, "t91-open", 0)
	open.Phase = PhaseOpen
	if _, err := store.Restore(ctx, open, nil); err != nil {
		t.Fatalf("plant open: %v", err)
	}
	before, err := store.Get(ctx, accountScope, nil)
	if err != nil {
		t.Fatalf("state before: %v", err)
	}
	blocked, err := service.CheckDispatchAdmission(ctx, input)
	if err != nil || blocked.Outcome != PrepareBlocked || blocked.State == nil || blocked.State.Phase != PhaseOpen {
		t.Fatalf("当前代 OPEN 必须阻断: %+v err=%v", blocked, err)
	}
	after, err := store.Get(ctx, accountScope, nil)
	if err != nil {
		t.Fatalf("state after: %v", err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("准入判定不得写状态:\nbefore=%+v\nafter=%+v", before, after)
	}
}

func TestT91CheckDispatchAdmissionOldRevisionAdmits(t *testing.T) {
	ctx := context.Background()
	store, service, accountScope, input := t91AdmissionFixture(t, 3, ServiceOptions{})
	old := ClosedState(accountScope, "2", 5, "t91-old", 0)
	old.Phase = PhaseOpen
	if _, err := store.Restore(ctx, old, nil); err != nil {
		t.Fatalf("plant old: %v", err)
	}
	admitted, err := service.CheckDispatchAdmission(ctx, input)
	if err != nil || admitted.Outcome != PrepareDispatchable {
		t.Fatalf("旧配置代状态必须放行: %+v err=%v", admitted, err)
	}
	// 只读判定不替换旧配置代状态（常规 PrepareAttempt 才 ReplaceDispatchRevision）。
	kept, err := store.Get(ctx, accountScope, nil)
	if err != nil {
		t.Fatalf("state kept: %v", err)
	}
	if kept.DispatchRevision != "2" || kept.Phase != PhaseOpen {
		t.Fatalf("只读判定不得替换旧配置代状态: %+v", kept)
	}
}

func TestT91CheckDispatchAdmissionRuntimeNotReadyBlocks(t *testing.T) {
	_, service, _, input := t91AdmissionFixture(t, 1, ServiceOptions{
		IsRuntimeStateReady: func(string) bool { return false },
	})
	result, err := service.CheckDispatchAdmission(context.Background(), input)
	if err != nil {
		t.Fatalf("admission: %v", err)
	}
	if result.Outcome != PrepareBlocked || result.State == nil || result.State.Phase != PhaseSuspect {
		t.Fatalf("运行态未就绪必须 fail-closed 阻断: %+v", result)
	}
}
