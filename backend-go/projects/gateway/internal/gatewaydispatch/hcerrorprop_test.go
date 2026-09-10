package gatewaydispatch

import (
	"context"
	"errors"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// 高并发分组准备路径的错误传播与 Key 选择瞬态状态错误。

type errorLaneConcurrencyStore struct{}

func (errorLaneConcurrencyStore) LoadCurrentAsync(context.Context, []string) (map[string]int, error) {
	return nil, errPortBoom
}

func (errorLaneConcurrencyStore) LoadCurrentByLaneAsync(context.Context, []string, string) (map[string]int, error) {
	return nil, errPortBoom
}

func (errorLaneConcurrencyStore) TryAcquireAsync(context.Context, string, int, AccountConcurrencyAcquireOptions) (ConcurrencySlot, error) {
	return ConcurrencySlot{Acquired: true}, nil
}

// errorBusyAffinity 的高并发繁忙检查报错。
type errorBusyAffinity struct {
	fakeAffinity
}

func (errorBusyAffinity) AreHighConcurrencyAccountsBusyForLaneAsync(context.Context, []AccountCandidate, HighConcurrencyBusyOptions) (bool, error) {
	return false, errPortBoom
}

func TestPrepareDispatchAccountsHighConcurrencyErrorsPropagate(t *testing.T) {
	cases := []struct {
		name  string
		setup func(engine *Engine)
	}{
		{"snapshot_refresh", func(engine *Engine) { engine.Concurrency = errorLaneConcurrencyStore{} }},
		{"busy_check", func(engine *Engine) { engine.Affinity = &errorBusyAffinity{} }},
		{"hot_quality", func(engine *Engine) { engine.HotQuality = errorHotQualityPort{} }},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			pipeline, engine, _, _ := newPipeline(t)
			testCase.setup(engine)
			policy := gatewayruntimecache.GroupSchedulingPolicy{}
			input := dispatchPreparationInput(t, testAccounts("a-1"))
			input.GroupAccess = highConcurrencyGroupAccess(&policy)
			_, err := pipeline.PrepareDispatchAccounts(context.Background(), input)
			if !errors.Is(err, errPortBoom) {
				t.Fatalf("expected 端口爆炸, got %v", err)
			}
		})
	}
}

// errorTransientCache 的瞬态 Key 状态加载报错。
type errorTransientCache struct {
	fakeCache
}

func (errorTransientCache) LoadApiKeyTransientStatesForDispatch(ctx context.Context, accountID string, fingerprints []string) ([]gatewayruntimecache.AccountAPIKeyRuntimeSelectionState, error) {
	return nil, errPortBoom
}

func TestSelectAccountApiKeyTransientStateError(t *testing.T) {
	engine, _, _ := newTestEngine(t)
	engine.Cache = &errorTransientCache{}
	// 双 Key 触发池隔离并读取瞬态状态。
	account := multiKeyTestAccount("a-1", "key-a", "key-b")
	_, ok, err := engine.SelectAccountApiKeyForDispatch(context.Background(), account, SelectApiKeyOptions{})
	if !errors.Is(err, errPortBoom) || ok {
		t.Fatalf("expected 端口爆炸, got ok=%v err=%v", ok, err)
	}
}
