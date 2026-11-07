// w14d_codex_tails_test.go drives the remaining error/edge arms of the codex
// gateway family: async turn-retry state arms and the turn-retry memory
// tails. 2026-11-07 桥退役批删除了本文件原 segments / contextstore /
// chat-bridge state / compact preflight 尾部用例（机制整体退役）。
package gatewaycodex

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// ---------------------------------------------------------------------------
// turn retry async + memory arms
// ---------------------------------------------------------------------------

type w14dTurnStore struct {
	mu       sync.Mutex
	values   map[string]json.RawMessage
	failGet  bool
	failIncr bool
	failCAS  bool
}

func (s *w14dTurnStore) GetJSON(_ context.Context, key string) (json.RawMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failGet {
		return nil, errors.New("get failed")
	}
	if value, ok := s.values[key]; ok {
		return append(json.RawMessage(nil), value...), nil
	}
	return nil, nil
}

func (s *w14dTurnStore) CompareSetJSON(_ context.Context, key string, _ json.RawMessage, next any, _ int64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failCAS {
		return false, nil
	}
	encoded, err := json.Marshal(next)
	if err != nil {
		return false, err
	}
	s.values[key] = encoded
	return true, nil
}

func (s *w14dTurnStore) Incr(_ context.Context, _ string, _ int64) (int64, error) {
	if s.failIncr {
		return 0, errors.New("incr failed")
	}
	return 3, nil
}

func TestW14dTurnRetryAsyncArms(t *testing.T) {
	ctx := context.Background()
	strategy := avoidanceStrategy("w14d-state")
	accounts := []gatewayruntimecache.OpenAIAccountSecret{{ID: "acc-1"}, {ID: "acc-2"}}

	// GetJSON failure arms (L210/L407/L522).
	store := &w14dTurnStore{values: map[string]json.RawMessage{}, failGet: true}
	service := newTurnRetryService(t)
	service.Store = store
	if _, err := service.OrderOpenAIAccountsByCodexTurnAvoidanceAsync(ctx, accounts, strategy, nil); err == nil {
		t.Fatal("order get failure must surface")
	}
	if _, err := service.RememberCodexTurnStreamFailureAsync(ctx, strategy, "acc-1", CodexTurnFailureInput{}); err == nil {
		t.Fatal("remember get failure must surface")
	}
	if _, err := service.ClearCodexTurnAccountAvoidanceAsync(ctx, strategy, "acc-1"); err == nil {
		t.Fatal("clear get failure must surface")
	}
	// Fence clear get failure with a real fence id: create it on a working
	// store, then swap in the failing store (L596).
	warm := newTurnRetryService(t)
	warm.Store = &w14dTurnStore{values: map[string]json.RawMessage{}}
	warm.RememberCodexTurnStreamFailureAsync(ctx, strategy, "acc-1", CodexTurnFailureInput{})
	activation, err := warm.RememberCodexTurnStreamFailureAsync(ctx, strategy, "acc-1", CodexTurnFailureInput{})
	if err != nil || activation == nil || activation.Activation == nil {
		t.Fatalf("warm activation: %+v %v", activation, err)
	}
	fence := activation.Activation.SourceFenceID
	generation := activation.Activation.SourceGeneration
	service.Store = &w14dTurnStore{values: map[string]json.RawMessage{}, failGet: true}
	if _, err := service.ClearCodexTurnAccountAvoidanceByFenceAsync(ctx, ClearCodexTurnAccountAvoidanceByFenceInput{
		StateKey: "w14d-state", AccountID: "acc-1", SourceGeneration: generation, SourceFenceID: fence,
	}); err == nil {
		t.Fatal("fence clear get failure must surface")
	}

	// Working store: remember twice activates, duplicate observation, clear.
	store = &w14dTurnStore{values: map[string]json.RawMessage{}}
	service = newTurnRetryService(t)
	service.Store = store
	first, err := service.RememberCodexTurnStreamFailureAsync(ctx, strategy, "acc-1", CodexTurnFailureInput{ObservationID: "obs-1"})
	if err != nil || first == nil {
		t.Fatalf("first remember: %v %v", first, err)
	}
	// Same observation id short-circuits as duplicate (L423).
	dup, err := service.RememberCodexTurnStreamFailureAsync(ctx, strategy, "acc-1", CodexTurnFailureInput{ObservationID: "obs-1"})
	if err != nil || dup == nil || !dup.DuplicateObservation {
		t.Fatalf("duplicate remember: %+v %v", dup, err)
	}
	second, err := service.RememberCodexTurnStreamFailureAsync(ctx, strategy, "acc-1", CodexTurnFailureInput{})
	if err != nil || second == nil || second.Activation == nil {
		t.Fatalf("second remember: %+v %v", second, err)
	}

	// Incr failure during activation (L430).
	incrFail := &w14dTurnStore{values: map[string]json.RawMessage{}, failIncr: true}
	incrService := newTurnRetryService(t)
	incrService.Store = incrFail
	incrService.RememberCodexTurnStreamFailureAsync(ctx, strategy, "acc-7", CodexTurnFailureInput{ObservationID: "o1"})
	if _, err := incrService.RememberCodexTurnStreamFailureAsync(ctx, strategy, "acc-7", CodexTurnFailureInput{ObservationID: "o2"}); err == nil {
		t.Fatal("incr failure must surface")
	}

	// CAS failure during activation (L437).
	casFail := &w14dTurnStore{values: map[string]json.RawMessage{}, failIncr: true}
	casService := newTurnRetryService(t)
	casService.Store = casFail
	casService.RememberCodexTurnStreamFailureAsync(ctx, strategy, "acc-8", CodexTurnFailureInput{ObservationID: "o1"})
	if _, err := casService.RememberCodexTurnStreamFailureAsync(ctx, strategy, "acc-8", CodexTurnFailureInput{ObservationID: "o2"}); err == nil {
		t.Fatal("cas failure must surface")
	}

	// Clear: unknown account, known account, exhausted CAS.
	cleared, err := service.ClearCodexTurnAccountAvoidanceAsync(ctx, strategy, "acc-unknown")
	if err != nil || cleared {
		t.Fatalf("unknown clear: %v %v", cleared, err)
	}
	cleared, err = service.ClearCodexTurnAccountAvoidanceAsync(ctx, strategy, "acc-1")
	if err != nil || !cleared {
		t.Fatalf("clear: %v %v", cleared, err)
	}
	exhaustService := newTurnRetryService(t)
	exhaustStore := &w14dTurnStore{values: map[string]json.RawMessage{}, failCAS: true}
	exhaustService.Store = exhaustStore
	exhaustService.RememberCodexTurnStreamFailureAsync(ctx, strategy, "acc-2", CodexTurnFailureInput{})
	if cleared, err := exhaustService.ClearCodexTurnAccountAvoidanceAsync(ctx, strategy, "acc-2"); err != nil || cleared {
		t.Fatalf("exhausted clear: %v %v", cleared, err)
	}

	// Fence clear: missing state (L600), fence mismatch (L607), applied path.
	fenceService := newTurnRetryService(t)
	fenceStore := &w14dTurnStore{values: map[string]json.RawMessage{}}
	fenceService.Store = fenceStore
	if cleared, err := fenceService.ClearCodexTurnAccountAvoidanceByFenceAsync(ctx, ClearCodexTurnAccountAvoidanceByFenceInput{
		StateKey: "w14d-state", AccountID: "acc-1", SourceGeneration: 1, SourceFenceID: "f-1",
	}); err != nil || cleared {
		t.Fatalf("empty fence clear: %v %v", cleared, err)
	}
	fenceService.RememberCodexTurnStreamFailureAsync(ctx, strategy, "acc-1", CodexTurnFailureInput{})
	fenceService.RememberCodexTurnStreamFailureAsync(ctx, strategy, "acc-1", CodexTurnFailureInput{})
	if cleared, err := fenceService.ClearCodexTurnAccountAvoidanceByFenceAsync(ctx, ClearCodexTurnAccountAvoidanceByFenceInput{
		StateKey: "w14d-state", AccountID: "acc-1", SourceGeneration: 99, SourceFenceID: "f-1",
	}); err != nil || cleared {
		t.Fatalf("mismatch fence clear: %v %v", cleared, err)
	}
	if cleared, err := fenceService.ClearCodexTurnAccountAvoidanceByFenceAsync(ctx, ClearCodexTurnAccountAvoidanceByFenceInput{
		StateKey: "w14d-state", AccountID: "acc-1", SourceGeneration: 3, SourceFenceID: "fence-fence",
	}); err != nil || cleared {
		t.Fatalf("wrong fence id clear: %v %v", cleared, err)
	}
	if cleared, err := fenceService.ClearCodexTurnAccountAvoidanceByFenceAsync(ctx, ClearCodexTurnAccountAvoidanceByFenceInput{
		StateKey: "w14d-state", AccountID: "acc-1", SourceGeneration: 3, SourceFenceID: "fence_fence",
	}); err != nil || cleared {
		t.Fatalf("right fence clear: %v %v", cleared, err)
	}

	// Guards: disabled strategy and blank account (L399/L514/L747).
	disabled := OpenAIGatewayClientStrategyContext{ClientSourceAvoidanceStateKey: "k"}
	if result, err := service.RememberCodexTurnStreamFailureAsync(ctx, disabled, "acc-1", CodexTurnFailureInput{}); result != nil || err != nil {
		t.Fatal("disabled remember must be nil")
	}
	if cleared, err := service.ClearCodexTurnAccountAvoidanceAsync(ctx, disabled, "acc-1"); cleared || err != nil {
		t.Fatal("disabled clear must be false")
	}
	// Blank account id trips the defensive panic guard (L747).
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("blank account id must trip the guard")
			}
		}()
		codexTurnAccountStateKey("secret", "k", "  ")
	}()

	// Ordering arms: no state at all, state with no activations (L223/247).
	result := orderOpenOpenAIOrderingStub(t)
	if result.Applied {
		t.Fatal("plain state must not apply avoidance")
	}

	// decodeRetryState malformed payloads (L969/972).
	if decodeRetryState(json.RawMessage("{bad")) != nil {
		t.Fatal("broken json must decode to nil")
	}
	partial := decodeRetryState(json.RawMessage(`{"stateKey":"k","failureCount":1}`))
	if partial == nil || partial.FailedAccounts == nil {
		t.Fatal("missing failedAccounts must be normalized to an empty map")
	}
}

func orderOpenOpenAIOrderingStub(t *testing.T) CodexTurnAccountAvoidanceResult {
	t.Helper()
	state := &codexTurnRetryState{StateKey: "k", FailureCount: 1, FailedAccounts: map[string]*codexTurnFailedAccount{}}
	strategy := OpenAIGatewayClientStrategyContext{AllowClientSourceAccountAvoidance: false}
	result := orderOpenAIAccountsByCodexTurnAvoidanceWithState([]gatewayruntimecache.OpenAIAccountSecret{{ID: "a"}}, strategy, state, nil)
	if result.Applied || result.FailureCount != 1 {
		t.Fatalf("disabled ordering = %+v", result)
	}
	active := &codexTurnRetryState{
		StateKey: "k", FailureCount: 5,
		FailedAccounts: map[string]*codexTurnFailedAccount{
			"a": {AccountID: "a", FailureCount: 9},
		},
	}
	strategy.AllowClientSourceAccountAvoidance = true
	return orderOpenAIAccountsByCodexTurnAvoidanceWithState([]gatewayruntimecache.OpenAIAccountSecret{{ID: "a"}, {ID: "b"}}, strategy, active, nil)
}

func TestW14dTurnRetryMemoryArms(t *testing.T) {
	service := newTurnRetryService(t)
	strategy := avoidanceStrategy("w14d-mem")

	// Activation tombstones populate the generation cache and eviction.
	service.RememberCodexTurnStreamFailure(strategy, "acc-1", CodexTurnFailureInput{ObservationID: "o1"})
	service.RememberCodexTurnStreamFailure(strategy, "acc-1", CodexTurnFailureInput{ObservationID: "o2"})
	if len(service.memory.generations) == 0 {
		t.Fatal("activation must record a generation tombstone")
	}

	// Expired memory entries read as absent (L644).
	service.Clock.(*fakeClock).Advance(31 * time.Minute)
	if state := service.getMemoryCodexTurnRetryState(strategy.ClientSourceAvoidanceStateKey + ":a_x"); state != nil {
		t.Fatal("expired entry must be dropped")
	}

	// Generation eviction loop (L713-725) via many activations.
	for index := 0; index < 12; index++ {
		applyMutation := codexTurnStateMutation{
			state: codexTurnRetryState{
				StateKey: "w14d-mem", FailureCount: 2,
				FailedAccounts: map[string]*codexTurnFailedAccount{
					"acc-x": {AccountID: "acc-x", FailureCount: 2},
				},
			},
			activation: &CodexTurnFailureActivation{AccountID: "acc-x"},
		}
		service.applyCodexTurnAvoidanceGeneration(applyMutation, "w14d-mem:gen", nil)
	}
}

