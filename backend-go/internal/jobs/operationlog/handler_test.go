package operationlog

import (
	"context"
	"errors"
	"testing"

	"juhe-ai/backend-go/internal/store/port"
)

type operationLogStoreStub struct {
	calls int
	input port.OperationLogInput
	err   error
}

func (s *operationLogStoreStub) InsertOperationLog(_ context.Context, input port.OperationLogInput) error {
	s.calls++
	s.input = input
	return s.err
}

func TestHandleWriteTaskRejectsInvalidPayload(t *testing.T) {
	store := &operationLogStoreStub{}
	err := HandleWriteTask(context.Background(), store, []byte(`{"version":1}`))
	if !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("HandleWriteTask() error = %v, want ErrInvalidPayload", err)
	}
	if store.calls != 0 {
		t.Fatalf("store calls = %d, want 0", store.calls)
	}
}

func TestHandleWriteTaskWritesPayload(t *testing.T) {
	payload, err := EncodeWriteTaskPayload(operationLogTaskFixture())
	if err != nil {
		t.Fatalf("EncodeWriteTaskPayload() error = %v", err)
	}
	store := &operationLogStoreStub{}

	if err := HandleWriteTask(context.Background(), store, payload); err != nil {
		t.Fatalf("HandleWriteTask() error = %v", err)
	}
	if store.calls != 1 || store.input.ID != "oplog_1" {
		t.Fatalf("store = %+v", store)
	}
}

func TestHandleWriteTaskReturnsStoreError(t *testing.T) {
	payload, err := EncodeWriteTaskPayload(operationLogTaskFixture())
	if err != nil {
		t.Fatalf("EncodeWriteTaskPayload() error = %v", err)
	}
	storeErr := errors.New("store down")
	store := &operationLogStoreStub{err: storeErr}

	err = HandleWriteTask(context.Background(), store, payload)
	if !errors.Is(err, storeErr) {
		t.Fatalf("HandleWriteTask() error = %v, want store error", err)
	}
}
