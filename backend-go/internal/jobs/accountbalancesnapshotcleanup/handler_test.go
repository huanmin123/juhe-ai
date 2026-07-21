package accountbalancesnapshotcleanup

import (
	"context"
	"errors"
	"testing"

	"juhe-ai/backend-go/internal/store/port"
)

type cleanupStoreStub struct {
	calls []port.AccountBalanceSnapshotCleanupInput
	err   error
}

func (s *cleanupStoreStub) DeleteAccountBalanceSnapshot(
	_ context.Context,
	input port.AccountBalanceSnapshotCleanupInput,
) error {
	s.calls = append(s.calls, input)
	return s.err
}

func TestHandleTaskDeletesBoundedSnapshot(t *testing.T) {
	input := cleanupTaskFixture()
	payload, err := Encode(input)
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	store := &cleanupStoreStub{}

	if err := HandleTask(context.Background(), store, payload); err != nil {
		t.Fatalf("HandleTask() error = %v", err)
	}
	if len(store.calls) != 1 {
		t.Fatalf("store calls = %d, want 1", len(store.calls))
	}
	got := store.calls[0]
	if got.AccountID != input.AccountID || got.SystemAccountID != input.SystemAccountID ||
		!got.UpdatedBefore.Equal(input.UpdatedBefore) || got.Reason != input.Reason {
		t.Fatalf("store input = %+v, want task %+v", got, input)
	}
}

func TestHandleTaskIsSafeToRepeat(t *testing.T) {
	payload, err := Encode(cleanupTaskFixture())
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	store := &cleanupStoreStub{}

	for range 2 {
		if err := HandleTask(context.Background(), store, payload); err != nil {
			t.Fatalf("HandleTask() error = %v", err)
		}
	}
	if len(store.calls) != 2 {
		t.Fatalf("store calls = %d, want 2 idempotent attempts", len(store.calls))
	}
}

func TestHandleTaskRejectsInvalidPayloadBeforeStore(t *testing.T) {
	store := &cleanupStoreStub{}
	err := HandleTask(context.Background(), store, []byte(`{"version":1}`))
	if !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("HandleTask() error = %v, want ErrInvalidPayload", err)
	}
	if len(store.calls) != 0 {
		t.Fatalf("store calls = %d, want 0", len(store.calls))
	}
}

func TestHandleTaskReturnsStoreErrorForAsynqRetry(t *testing.T) {
	payload, err := Encode(cleanupTaskFixture())
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	storeErr := errors.New("database unavailable")
	store := &cleanupStoreStub{err: storeErr}

	err = HandleTask(context.Background(), store, payload)
	if !errors.Is(err, storeErr) {
		t.Fatalf("HandleTask() error = %v, want store error", err)
	}
}
