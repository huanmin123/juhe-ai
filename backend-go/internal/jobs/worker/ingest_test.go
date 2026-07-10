package worker

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hibiken/asynq"

	operationlogjob "juhe-ai/backend-go/internal/jobs/operationlog"
	publicapilogjob "juhe-ai/backend-go/internal/jobs/publicapilog"
	"juhe-ai/backend-go/internal/store/port"
)

type fakePublicAPILogStore struct {
	err            error
	calls          int
	operationErr   error
	operationCalls int
}

func (f *fakePublicAPILogStore) InsertPublicAPILog(_ context.Context, _ port.PublicAPILogInput) error {
	f.calls++
	return f.err
}

func (f *fakePublicAPILogStore) InsertOperationLog(_ context.Context, _ port.OperationLogInput) error {
	f.operationCalls++
	return f.operationErr
}

func TestHandlePublicAPILogTaskSkipsRetryForInvalidPayload(t *testing.T) {
	store := &fakePublicAPILogStore{}
	err := handlePublicAPILogTask(context.Background(), store, []byte(`{"version":1}`))
	if !errors.Is(err, asynq.SkipRetry) {
		t.Fatalf("error = %v, want SkipRetry", err)
	}
	if !errors.Is(err, publicapilogjob.ErrInvalidPayload) {
		t.Fatalf("error = %v, want invalid payload", err)
	}
	if store.calls != 0 {
		t.Fatalf("store calls = %d, want 0", store.calls)
	}
}

func TestHandlePublicAPILogTaskReturnsStoreErrorForRetry(t *testing.T) {
	payload, err := publicapilogjob.EncodeWriteTaskPayload(publicAPILogWorkerFixture())
	if err != nil {
		t.Fatalf("EncodeWriteTaskPayload() error = %v", err)
	}
	storeErr := errors.New("store down")
	store := &fakePublicAPILogStore{err: storeErr}

	err = handlePublicAPILogTask(context.Background(), store, payload)
	if !errors.Is(err, storeErr) {
		t.Fatalf("error = %v, want store error", err)
	}
	if errors.Is(err, asynq.SkipRetry) {
		t.Fatalf("store error should remain retryable: %v", err)
	}
	if store.calls != 1 {
		t.Fatalf("store calls = %d, want 1", store.calls)
	}
}

func TestHandlePublicAPILogTaskWritesValidPayload(t *testing.T) {
	payload, err := publicapilogjob.EncodeWriteTaskPayload(publicAPILogWorkerFixture())
	if err != nil {
		t.Fatalf("EncodeWriteTaskPayload() error = %v", err)
	}
	store := &fakePublicAPILogStore{}

	if err := handlePublicAPILogTask(context.Background(), store, payload); err != nil {
		t.Fatalf("handlePublicAPILogTask() error = %v", err)
	}
	if store.calls != 1 {
		t.Fatalf("store calls = %d, want 1", store.calls)
	}
}

func TestHandleOperationLogTaskSkipsRetryForInvalidPayload(t *testing.T) {
	store := &fakePublicAPILogStore{}
	err := handleOperationLogTask(context.Background(), store, []byte(`{"version":1}`))
	if !errors.Is(err, asynq.SkipRetry) {
		t.Fatalf("error = %v, want SkipRetry", err)
	}
	if !errors.Is(err, operationlogjob.ErrInvalidPayload) {
		t.Fatalf("error = %v, want invalid payload", err)
	}
	if store.operationCalls != 0 {
		t.Fatalf("store calls = %d, want 0", store.operationCalls)
	}
}

func TestHandleOperationLogTaskReturnsStoreErrorForRetry(t *testing.T) {
	payload, err := operationlogjob.EncodeWriteTaskPayload(operationLogWorkerFixture())
	if err != nil {
		t.Fatalf("EncodeWriteTaskPayload() error = %v", err)
	}
	storeErr := errors.New("store down")
	store := &fakePublicAPILogStore{operationErr: storeErr}

	err = handleOperationLogTask(context.Background(), store, payload)
	if !errors.Is(err, storeErr) {
		t.Fatalf("error = %v, want store error", err)
	}
	if errors.Is(err, asynq.SkipRetry) {
		t.Fatalf("store error should remain retryable: %v", err)
	}
	if store.operationCalls != 1 {
		t.Fatalf("store calls = %d, want 1", store.operationCalls)
	}
}

func TestHandleOperationLogTaskWritesValidPayload(t *testing.T) {
	payload, err := operationlogjob.EncodeWriteTaskPayload(operationLogWorkerFixture())
	if err != nil {
		t.Fatalf("EncodeWriteTaskPayload() error = %v", err)
	}
	store := &fakePublicAPILogStore{}

	if err := handleOperationLogTask(context.Background(), store, payload); err != nil {
		t.Fatalf("handleOperationLogTask() error = %v", err)
	}
	if store.operationCalls != 1 {
		t.Fatalf("store calls = %d, want 1", store.operationCalls)
	}
}

func publicAPILogWorkerFixture() port.PublicAPILogInput {
	statusCode := 200
	startedAt := time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC)
	return port.PublicAPILogInput{
		ID:                    "publog_worker_1",
		Method:                "GET",
		Path:                  "/__aipublic__/group/list",
		StatusCode:            &statusCode,
		Success:               true,
		RequestCaptureStatus:  port.PublicAPILogCaptureComplete,
		ResponseCaptureStatus: port.PublicAPILogCaptureComplete,
		RequestData:           map[string]any{"query": map[string]any{}},
		ResponseData:          map[string]any{"body": map[string]any{"items": []any{}}},
		StartedAt:             startedAt,
		EndedAt:               startedAt.Add(time.Millisecond),
		CreatedAt:             startedAt.Add(time.Millisecond),
	}
}

func operationLogWorkerFixture() port.OperationLogInput {
	createdAt := time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC)
	statusCode := 200
	return port.OperationLogInput{
		ID:                   "oplog_worker_1",
		ActorSystemAccountID: "sys_user",
		ActorRole:            "user",
		Module:               "accounts",
		Action:               "update_tags",
		OperationKey:         "accounts.update_tags",
		ResourceType:         "account",
		ResourceID:           "acct_main",
		ResourceName:         "主账号",
		Summary:              "更新账户标签：主账号",
		Method:               "PATCH",
		Path:                 "/__aisys__/api/my-accounts/acct_main/tags",
		StatusCode:           &statusCode,
		CreatedAt:            createdAt,
	}
}
