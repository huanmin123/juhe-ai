package gatewayaccounteffects

import (
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

func testEpoch(runtimeKey string, sequence int64, observedAt string, success bool) AccountSideEffectEpoch {
	return AccountSideEffectEpoch{RuntimeKey: runtimeKey, Sequence: sequence, ObservedAt: observedAt, Success: success}
}

func TestEpochRegistryObserveSequences(t *testing.T) {
	registry, err := NewAccountSideEffectEpochRegistry(0)
	if err != nil {
		t.Fatal(err)
	}
	decision, err := registry.Observe("acc-1", EpochObservation{ObservedAt: "2026-01-01T00:00:00.100Z", Success: false, Retain: true})
	if err != nil || !decision.Accepted || decision.Epoch.Sequence != 1 {
		t.Fatalf("first observe = %+v, err %v", decision, err)
	}
	if decision.Epoch.ObservedAt != "2026-01-01T00:00:00.100Z" {
		t.Fatalf("canonical observedAt = %q", decision.Epoch.ObservedAt)
	}
	// Offset input is canonicalized like Node toISOString.
	decision, err = registry.Observe("acc-1", EpochObservation{ObservedAt: "2026-01-01T08:00:00.200+08:00", Success: true, Retain: true})
	if err != nil || !decision.Accepted || decision.Epoch.Sequence != 2 {
		t.Fatalf("second observe = %+v, err %v", decision, err)
	}
	if decision.Epoch.ObservedAt != "2026-01-01T00:00:00.200Z" {
		t.Fatalf("offset canonicalization = %q", decision.Epoch.ObservedAt)
	}
	if !registry.IsCurrent(testEpoch("acc-1", 2, "2026-01-01T00:00:00.200Z", true)) {
		t.Fatal("second epoch should be current")
	}
	if registry.IsCurrent(testEpoch("acc-1", 1, "2026-01-01T00:00:00.100Z", false)) {
		t.Fatal("first epoch should not be current")
	}
}

func TestEpochRegistryStaleRules(t *testing.T) {
	revision1 := int64(1)
	revision3 := int64(3)
	tests := []struct {
		name           string
		setup          func(*AccountSideEffectEpochRegistry) error
		observation    EpochObservation
		wantAccepted   bool
		wantSequence   int64
		wantRevision   *int64
	}{
		{
			name: "更早时间戳被拒绝",
			setup: func(r *AccountSideEffectEpochRegistry) error {
				_, err := r.Observe("k", EpochObservation{ObservedAt: "2026-01-01T00:00:01Z", Success: false})
				return err
			},
			observation:  EpochObservation{ObservedAt: "2026-01-01T00:00:00Z", Success: true},
			wantAccepted: false,
			wantSequence: 1,
		},
		{
			name: "同时间戳失败覆盖成功被拒绝",
			setup: func(r *AccountSideEffectEpochRegistry) error {
				_, err := r.Observe("k", EpochObservation{ObservedAt: "2026-01-01T00:00:01Z", Success: true})
				return err
			},
			observation:  EpochObservation{ObservedAt: "2026-01-01T00:00:01Z", Success: false},
			wantAccepted: false,
			wantSequence: 1,
		},
		{
			name: "更低 dispatchRevision 被拒绝",
			setup: func(r *AccountSideEffectEpochRegistry) error {
				revision := revision3
				_, err := r.Observe("k", EpochObservation{ObservedAt: "2026-01-01T00:00:00Z", Success: false, DispatchRevision: &revision})
				return err
			},
			observation:  EpochObservation{ObservedAt: "2026-01-01T00:00:05Z", Success: false, DispatchRevision: &revision1},
			wantAccepted: false,
			wantSequence: 1,
			wantRevision: &revision1,
		},
		{
			name: "更新 dispatchRevision 接受并回写",
			setup: func(r *AccountSideEffectEpochRegistry) error {
				revision := revision1
				_, err := r.Observe("k", EpochObservation{ObservedAt: "2026-01-01T00:00:00Z", Success: false, DispatchRevision: &revision})
				return err
			},
			observation:  EpochObservation{ObservedAt: "2026-01-01T00:00:00Z", Success: false, DispatchRevision: &revision3},
			wantAccepted: true,
			wantSequence: 2,
			wantRevision: &revision3,
		},
		{
			name: "更新 revision 允许更早时间戳",
			setup: func(r *AccountSideEffectEpochRegistry) error {
				revision := revision1
				_, err := r.Observe("k", EpochObservation{ObservedAt: "2026-01-01T00:00:09Z", Success: false, DispatchRevision: &revision})
				return err
			},
			observation:  EpochObservation{ObservedAt: "2026-01-01T00:00:01Z", Success: false, DispatchRevision: &revision3},
			wantAccepted: true,
			wantSequence: 2,
			wantRevision: &revision3,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			registry, err := NewAccountSideEffectEpochRegistry(0)
			if err != nil {
				t.Fatal(err)
			}
			if err := tt.setup(registry); err != nil {
				t.Fatal(err)
			}
			decision, err := registry.Observe("k", tt.observation)
			if err != nil {
				t.Fatal(err)
			}
			if decision.Accepted != tt.wantAccepted {
				t.Fatalf("accepted = %v, want %v", decision.Accepted, tt.wantAccepted)
			}
			if decision.Epoch.Sequence != tt.wantSequence {
				t.Fatalf("sequence = %d, want %d", decision.Epoch.Sequence, tt.wantSequence)
			}
			if tt.wantRevision != nil {
				if decision.Epoch.DispatchRevision == nil || *decision.Epoch.DispatchRevision != *tt.wantRevision {
					t.Fatalf("revision = %v, want %v", decision.Epoch.DispatchRevision, *tt.wantRevision)
				}
			}
		})
	}
}

func TestEpochRegistryValidationErrors(t *testing.T) {
	registry, err := NewAccountSideEffectEpochRegistry(0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Observe("  ", EpochObservation{ObservedAt: "2026-01-01T00:00:00Z"}); err == nil || err.Error() != "account side effect runtimeKey is required" {
		t.Fatalf("runtimeKey err = %v", err)
	}
	if _, err := registry.Observe("k", EpochObservation{ObservedAt: "2026-01-01T00:00:00"}); err == nil || err.Error() != "account side effect observedAt必须是带 Z 或数值 offset 的 RFC3339 时间" {
		t.Fatalf("observedAt err = %v", err)
	}
	if _, err := NewAccountSideEffectEpochRegistry(0); err != nil {
		t.Fatalf("default capacity err = %v", err)
	}
	if _, err := NewAccountSideEffectEpochRegistry(-1); err == nil || err.Error() != "account side effect epoch capacity must be a positive integer" {
		t.Fatalf("capacity err = %v", err)
	}
}

func TestEpochRegistryTrimsNonRetainedFirst(t *testing.T) {
	registry, err := NewAccountSideEffectEpochRegistry(3)
	if err != nil {
		t.Fatal(err)
	}
	observe := func(key string, retain bool) {
		t.Helper()
		if _, err := registry.Observe(key, EpochObservation{ObservedAt: "2026-01-01T00:00:00Z", Success: true, Retain: retain}); err != nil {
			t.Fatal(err)
		}
	}
	observe("retained-a", true)
	observe("retained-b", true)
	observe("ephemeral-1", false)
	observe("ephemeral-2", false)
	// capacity 3: ephemeral-1 was evicted for ephemeral-2; retained entries stay.
	if registry.Size() != 3 {
		t.Fatalf("size = %d, want 3", registry.Size())
	}
	if !registry.IsCurrent(testEpoch("retained-a", 1, "2026-01-01T00:00:00.000Z", true)) {
		t.Fatal("retained-a should survive")
	}
	if !registry.IsCurrent(testEpoch("retained-b", 1, "2026-01-01T00:00:00.000Z", true)) {
		t.Fatal("retained-b should survive")
	}
	// Release both retained epochs; the retained-count must drop and trimming
	// resumes (leak recovery).
	registry.Release(testEpoch("retained-a", 1, "2026-01-01T00:00:00.000Z", true))
	observe("ephemeral-3", false)
	observe("ephemeral-4", false)
	if !registry.IsCurrent(testEpoch("retained-b", 1, "2026-01-01T00:00:00.000Z", true)) {
		t.Fatal("retained-b should still survive while retained")
	}
	registry.Release(testEpoch("retained-b", 1, "2026-01-01T00:00:00.000Z", true))
	observe("ephemeral-5", false)
	observe("ephemeral-6", false)
	// All retained epochs released; retained entries are now evictable.
	if registry.IsCurrent(testEpoch("retained-b", 1, "2026-01-01T00:00:00.000Z", true)) {
		t.Fatal("retained-b should be evicted after release under pressure")
	}
}

func TestSideEffectQueueOrderingAndFailureHeap(t *testing.T) {
	queue := NewAccountSideEffectQueue()
	mk := func(key string, success bool, nextAttemptAtMs, enqueuedAtMs int64) *QueuedAccountSideEffect {
		return &QueuedAccountSideEffect{
			Operation:       newTestOperation(key, success),
			Epoch:           AccountSideEffectEpoch{RuntimeKey: key, Sequence: enqueuedAtMs},
			EnqueuedAtMs:    enqueuedAtMs,
			NextAttemptAtMs: nextAttemptAtMs,
		}
	}
	queue.Push(mk("a", false, 200, 100))
	queue.Push(mk("b", false, 100, 300))
	queue.Push(mk("c", true, 100, 200))
	if queue.Len() != 3 || !queue.HasFailures() {
		t.Fatalf("queue state len=%d failures=%v", queue.Len(), queue.HasFailures())
	}
	if peek := queue.Peek(); peek.Epoch.RuntimeKey != "c" {
		t.Fatalf("peek = %s, want c (next=100, enqueued=200)", peek.Epoch.RuntimeKey)
	}
	if queue.FindIndexByRuntimeKey("b") < 0 || !queue.HasRuntimeKey("b") {
		t.Fatal("runtime key index should find b")
	}
	oldest := queue.RemoveOldestFailure()
	if oldest == nil || oldest.Epoch.RuntimeKey != "a" {
		t.Fatalf("oldest failure = %#v, want a (earliest enqueuedAtMs)", oldest)
	}
	if queue.HasRuntimeKey("a") {
		t.Fatal("a should be removed")
	}
	if removed := queue.Pop(); removed.Epoch.RuntimeKey != "c" {
		t.Fatalf("pop = %s, want c", removed.Epoch.RuntimeKey)
	}
	if removed := queue.Pop(); removed.Epoch.RuntimeKey != "b" {
		t.Fatalf("pop = %s, want b", removed.Epoch.RuntimeKey)
	}
	if queue.Len() != 0 || queue.HasFailures() {
		t.Fatal("queue should be empty")
	}
}

func TestSideEffectQueueReplaceAtCoalesce(t *testing.T) {
	queue := NewAccountSideEffectQueue()
	original := &QueuedAccountSideEffect{
		Operation:       newTestOperation("acc-1", false),
		Epoch:           AccountSideEffectEpoch{RuntimeKey: "acc-1", Sequence: 1},
		EnqueuedAtMs:    10,
		NextAttemptAtMs: 500,
	}
	queue.Push(original)
	replacement := &QueuedAccountSideEffect{
		Operation:       newTestOperation("acc-1", false),
		Epoch:           AccountSideEffectEpoch{RuntimeKey: "acc-1", Sequence: 2},
		EnqueuedAtMs:    20,
		NextAttemptAtMs: 20,
	}
	if replaced := queue.ReplaceAt(0, replacement); replaced != original {
		t.Fatalf("replaceAt returned %#v, want original", replaced)
	}
	if queue.Len() != 1 || queue.Peek() != replacement {
		t.Fatal("replacement should be queued")
	}
	if queue.Peek().NextAttemptAtMs != 20 {
		t.Fatal("heap should rebalance to the earlier next attempt")
	}
	if queue.ReplaceAt(9, replacement) != nil {
		t.Fatal("out of range replaceAt should return nil")
	}
}

func TestSideEffectQueueRemoveRuntimeKeyAndWhere(t *testing.T) {
	queue := NewAccountSideEffectQueue()
	for index, key := range []string{"a", "a", "b"} {
		queue.Push(&QueuedAccountSideEffect{
			Operation:       newTestOperation(key, false),
			Epoch:           AccountSideEffectEpoch{RuntimeKey: key, Sequence: int64(index)},
			EnqueuedAtMs:    int64(index),
			NextAttemptAtMs: int64(index),
		})
	}
	removed := queue.RemoveRuntimeKey("a")
	if len(removed) != 2 {
		t.Fatalf("removed = %d, want 2", len(removed))
	}
	if queue.HasRuntimeKey("a") || !queue.HasRuntimeKey("b") {
		t.Fatal("runtime key index inconsistent after removal")
	}
	if queue.Len() != 1 {
		t.Fatal("only b should remain")
	}
	if count := queue.RemoveWhere(func(item *QueuedAccountSideEffect) bool { return item.Epoch.RuntimeKey == "b" }); count != 1 {
		t.Fatalf("removeWhere = %d, want 1", count)
	}
	if queue.Len() != 0 {
		t.Fatal("queue should be empty")
	}
	// Bulk removal rebuilds the heap deterministically.
	for index := 0; index < 50; index++ {
		key := string(rune('a' + index%5))
		queue.Push(&QueuedAccountSideEffect{
			Operation:       newTestOperation(key, index%2 == 0),
			Epoch:           AccountSideEffectEpoch{RuntimeKey: key, Sequence: int64(index)},
			EnqueuedAtMs:    int64(index),
			NextAttemptAtMs: int64(49 - index),
		})
	}
	queue.RemoveWhere(func(item *QueuedAccountSideEffect) bool { return item.Epoch.Sequence%3 == 0 })
	previous := int64(-1)
	for queue.Len() > 0 {
		item := queue.Pop()
		current := item.NextAttemptAtMs*1_000_000 + item.EnqueuedAtMs
		if previous >= 0 && current < previous {
			t.Fatalf("heap order violated at %d", item.Epoch.Sequence)
		}
		previous = current
	}
}

func TestSideEffectPolicyPredicates(t *testing.T) {
	operation := newTestOperation("acc-1", false)
	item := &QueuedAccountSideEffect{Operation: operation, Epoch: AccountSideEffectEpoch{RuntimeKey: "acc-1"}}
	if !ShouldCancelQueuedAccountErrorHandlingSideEffectAfterSuccess(item, "acc-1") {
		t.Fatal("cancel predicate should match same runtime key")
	}
	if ShouldCancelQueuedAccountErrorHandlingSideEffectAfterSuccess(item, "acc-2") {
		t.Fatal("cancel predicate should not match other key")
	}
	if ShouldCoalesceQueuedAccountErrorHandlingSideEffect(item, newTestOperation("acc-1", true)) {
		t.Fatal("success must not coalesce")
	}
	if !ShouldCoalesceQueuedAccountErrorHandlingSideEffect(item, newTestOperation("acc-1", false)) {
		t.Fatal("failure should coalesce same key")
	}
	healthy := newTestOperation("acc-1", true)
	if !ShouldSkipHealthySuccessfulAccountSideEffect(healthy) {
		t.Fatal("healthy success should skip")
	}
	cooling := newTestOperation("acc-1", true)
	cooling.Account.CooldownUntil = stringPtr("2026-01-01T00:00:00Z")
	if ShouldSkipHealthySuccessfulAccountSideEffect(cooling) {
		t.Fatal("cooldown success must not skip")
	}
	failing := newTestOperation("acc-1", false)
	if ShouldSkipHealthySuccessfulAccountSideEffect(failing) {
		t.Fatal("failure must not skip")
	}
	streamFailing := newTestOperation("acc-1", true)
	streamFailing.Account.StreamFailureCount = 1
	if ShouldSkipHealthySuccessfulAccountSideEffect(streamFailing) {
		t.Fatal("stream failure count must block skip")
	}
}

func newTestOperation(accountID string, success bool) AccountSideEffectOperation {
	return AccountSideEffectOperation{
		Type:    AccountSideEffectOperationType,
		Account: gatewayAccountForTest(accountID),
		Input:   AccountErrorHandlingInput{Success: success, ObservedAt: "2026-01-01T00:00:00.000Z"},
	}
}

func gatewayAccountForTest(accountID string) gatewayruntimecache.OpenAIAccountSecret {
	return gatewayruntimecache.OpenAIAccountSecret{ID: accountID, Status: "active"}
}

func stringPtr(value string) *string { return &value }

var _ = strings.TrimSpace
