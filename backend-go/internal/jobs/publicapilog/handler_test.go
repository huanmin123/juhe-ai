package publicapilog

import (
	"context"
	"errors"
	"testing"

	"juhe-ai/backend-go/internal/store/port"
)

type fakePublicAPILogStore struct {
	input port.PublicAPILogInput
	err   error
	calls int
}

func (f *fakePublicAPILogStore) InsertPublicAPILog(_ context.Context, input port.PublicAPILogInput) error {
	f.calls++
	f.input = input
	return f.err
}

func TestHandleWriteTask(t *testing.T) {
	input := publicAPILogFixture()
	payload, err := EncodeWriteTaskPayload(input)
	if err != nil {
		t.Fatalf("EncodeWriteTaskPayload() error = %v", err)
	}
	store := &fakePublicAPILogStore{}
	if err := HandleWriteTask(context.Background(), store, payload); err != nil {
		t.Fatalf("HandleWriteTask() error = %v", err)
	}
	if err := HandleWriteTask(context.Background(), store, payload); err != nil {
		t.Fatalf("HandleWriteTask() second call error = %v", err)
	}
	if store.calls != 2 {
		t.Fatalf("store calls = %d, want 2", store.calls)
	}
	if store.input.ID != input.ID || store.input.TraceID != input.TraceID {
		t.Fatalf("stored input = %+v", store.input)
	}
}

func TestHandleWriteTaskRejectsInvalidPayloadBeforeStore(t *testing.T) {
	store := &fakePublicAPILogStore{}
	if err := HandleWriteTask(context.Background(), store, []byte(`{"version":1}`)); err == nil {
		t.Fatal("HandleWriteTask() error = nil, want payload error")
	}
	if store.calls != 0 {
		t.Fatalf("store calls = %d, want 0", store.calls)
	}
}

func TestHandleWriteTaskPropagatesStoreError(t *testing.T) {
	payload, err := EncodeWriteTaskPayload(publicAPILogFixture())
	if err != nil {
		t.Fatalf("EncodeWriteTaskPayload() error = %v", err)
	}
	store := &fakePublicAPILogStore{err: errors.New("store down")}
	if err := HandleWriteTask(context.Background(), store, payload); err == nil {
		t.Fatal("HandleWriteTask() error = nil, want store error")
	}
}
