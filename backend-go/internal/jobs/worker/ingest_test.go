package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/hibiken/asynq"

	operationlogjob "juhe-ai/backend-go/internal/jobs/operationlog"
	publicapilogjob "juhe-ai/backend-go/internal/jobs/publicapilog"
	"juhe-ai/backend-go/internal/jobs/queue"
	"juhe-ai/backend-go/internal/logging"
	"juhe-ai/backend-go/internal/store/port"
)

type ingestLogRecord struct {
	level  slog.Level
	msg    string
	attrs  map[string]any
	fields logging.LogContext
}

type ingestLogHandler struct {
	records []ingestLogRecord
}

func (h *ingestLogHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *ingestLogHandler) Handle(ctx context.Context, record slog.Record) error {
	attrs := make(map[string]any)
	record.Attrs(func(attr slog.Attr) bool {
		attrs[attr.Key] = attr.Value.Any()
		return true
	})
	h.records = append(h.records, ingestLogRecord{
		level:  record.Level,
		msg:    record.Message,
		attrs:  attrs,
		fields: logging.LogContextFrom(ctx),
	})
	return nil
}

func (h *ingestLogHandler) WithAttrs([]slog.Attr) slog.Handler { return h }

func (h *ingestLogHandler) WithGroup(string) slog.Handler { return h }

type ingestQueueCapture struct {
	taskType string
	payload  []byte
}

func (c *ingestQueueCapture) Enqueue(_ context.Context, taskType string, payload []byte, _ queue.EnqueueOptions) (queue.TaskInfo, error) {
	c.taskType = taskType
	c.payload = append([]byte(nil), payload...)
	return queue.TaskInfo{}, nil
}

func TestLoggedPublicAPILogTaskPreservesDistinctEnqueueCorrelation(t *testing.T) {
	client := &ingestQueueCapture{}
	producerCtx := logging.WithLogContext(context.Background(), logging.LogContext{
		TraceID:   "trace_public_1",
		RequestID: "request_public_1",
	})
	if _, err := publicapilogjob.EnqueueWrite(producerCtx, client, publicAPILogWorkerFixture()); err != nil {
		t.Fatalf("EnqueueWrite() error = %v", err)
	}
	store := &fakePublicAPILogStore{}
	handler := &ingestLogHandler{}
	logger := slog.New(handler)
	task := asynq.NewTask(client.taskType, client.payload)
	runtime := &ingestTaskRuntime{
		taskID:     "task_public_1",
		queue:      publicapilogjob.QueueName,
		retryCount: 2,
		maxRetry:   10,
		known:      true,
	}

	if err := handlePublicAPILogTaskLogged(context.Background(), logger, store, task, runtime); err != nil {
		t.Fatalf("handlePublicAPILogTaskLogged() error = %v", err)
	}
	if store.calls != 1 {
		t.Fatalf("store calls = %d, want 1", store.calls)
	}
	if len(handler.records) != 2 {
		t.Fatalf("log records = %d, want start and completed", len(handler.records))
	}
	start, completed := handler.records[0], handler.records[1]
	if start.msg != "ingest worker 任务开始" || completed.msg != "ingest worker 任务完成" {
		t.Fatalf("messages = %q, %q", start.msg, completed.msg)
	}
	for _, record := range []ingestLogRecord{start, completed} {
		if record.fields.TraceID != "trace_public_1" || record.fields.RequestID != "request_public_1" || record.fields.ParentID != "request_public_1" || record.fields.JobID != "task_public_1" {
			t.Fatalf("log context = %+v, want distinct trace/request plus job", record.fields)
		}
		if record.attrs["taskType"] != publicapilogjob.TaskTypeWrite || record.attrs["queue"] != publicapilogjob.QueueName {
			t.Fatalf("task attrs = %#v", record.attrs)
		}
		if record.attrs["logRecordId"] != "publog_worker_1" {
			t.Fatalf("log record id = %#v", record.attrs["logRecordId"])
		}
		if record.attrs["retryCount"] != int64(2) || record.attrs["maxRetry"] != int64(10) {
			t.Fatalf("retry attrs = %#v", record.attrs)
		}
	}
}

func TestLoggedOperationLogTaskPreservesUnexpectedStoreFailure(t *testing.T) {
	payload, err := operationlogjob.EncodeWriteTaskPayload(operationLogWorkerFixture())
	if err != nil {
		t.Fatalf("EncodeWriteTaskPayload() error = %v", err)
	}
	storeErr := errors.New("postgres insert failed")
	store := &fakePublicAPILogStore{operationErr: storeErr}
	handler := &ingestLogHandler{}
	logger := slog.New(handler)
	task := asynq.NewTask(operationlogjob.TaskTypeWrite, payload)
	runtime := &ingestTaskRuntime{taskID: "task_operation_1", queue: operationlogjob.QueueName, known: true}

	err = handleOperationLogTaskLogged(context.Background(), logger, store, task, runtime)
	if !errors.Is(err, storeErr) {
		t.Fatalf("error = %v, want store error", err)
	}
	if len(handler.records) != 2 {
		t.Fatalf("log records = %d, want start and failure", len(handler.records))
	}
	failure := handler.records[1]
	if failure.level < slog.LevelError || failure.msg != "ingest worker 任务失败" {
		t.Fatalf("failure record = %+v", failure)
	}
	if failure.fields.TraceID != "" || failure.fields.RequestID != "request_operation_legacy_1" || failure.fields.ParentID != "request_operation_legacy_1" || failure.fields.JobID != "task_operation_1" {
		t.Fatalf("failure context = %+v", failure.fields)
	}
	if failure.attrs["failureClass"] != "unexpected" {
		t.Fatalf("failure class = %#v", failure.attrs["failureClass"])
	}
	if got, ok := failure.attrs["error"].(error); !ok || !strings.Contains(got.Error(), storeErr.Error()) {
		t.Fatalf("failure error attr = %#v", failure.attrs["error"])
	}
	if failure.attrs["logRecordId"] != "oplog_worker_1" || failure.attrs["retryStatus"] != "only_attempt" || failure.attrs["willRetry"] != false {
		t.Fatalf("failure task state = %#v", failure.attrs)
	}
}

func TestLoggedPublicAPILogTaskPreservesExpectedInvalidPayloadFailure(t *testing.T) {
	store := &fakePublicAPILogStore{}
	handler := &ingestLogHandler{}
	logger := slog.New(handler)
	task := asynq.NewTask(publicapilogjob.TaskTypeWrite, []byte(`{"version":1}`))
	runtime := &ingestTaskRuntime{
		taskID:     "task_invalid_1",
		queue:      publicapilogjob.QueueName,
		retryCount: 1,
		maxRetry:   10,
		known:      true,
	}

	err := handlePublicAPILogTaskLogged(context.Background(), logger, store, task, runtime)
	if !errors.Is(err, publicapilogjob.ErrInvalidPayload) || !errors.Is(err, asynq.SkipRetry) {
		t.Fatalf("error = %v, want invalid payload plus SkipRetry", err)
	}
	if store.calls != 0 {
		t.Fatalf("store calls = %d, want 0", store.calls)
	}
	if len(handler.records) != 2 {
		t.Fatalf("log records = %d, want start and failure", len(handler.records))
	}
	failure := handler.records[1]
	if failure.level < slog.LevelError || failure.attrs["failureClass"] != "expected" || failure.attrs["retryable"] != false {
		t.Fatalf("failure record = %+v", failure)
	}
	if failure.fields.JobID != "task_invalid_1" || failure.attrs["taskType"] != publicapilogjob.TaskTypeWrite {
		t.Fatalf("failure correlation = fields:%+v attrs:%#v", failure.fields, failure.attrs)
	}
	if got, ok := failure.attrs["error"].(error); !ok || !strings.Contains(got.Error(), "payload") {
		t.Fatalf("failure error attr = %#v", failure.attrs["error"])
	}
}

func TestLoggedInvalidPayloadPreservesEnvelopeCorrelation(t *testing.T) {
	store := &fakePublicAPILogStore{}
	handler := &ingestLogHandler{}
	logger := slog.New(handler)
	payload := []byte(`{"version":1,"correlation":{"traceId":"trace-invalid-1","requestId":"request-invalid-1"},"log":{"id":"publog_invalid_1"}}`)
	task := asynq.NewTask(publicapilogjob.TaskTypeWrite, payload)
	runtime := &ingestTaskRuntime{taskID: "task_invalid_correlation_1", queue: publicapilogjob.QueueName, known: true}

	err := handlePublicAPILogTaskLogged(context.Background(), logger, store, task, runtime)
	if !errors.Is(err, publicapilogjob.ErrInvalidPayload) || !errors.Is(err, asynq.SkipRetry) {
		t.Fatalf("error = %v, want invalid payload plus SkipRetry", err)
	}
	if len(handler.records) != 2 {
		t.Fatalf("log records = %d, want start and failure", len(handler.records))
	}
	failure := handler.records[1]
	if failure.fields.TraceID != "trace-invalid-1" || failure.fields.RequestID != "request-invalid-1" || failure.fields.ParentID != "request-invalid-1" || failure.fields.JobID != "task_invalid_correlation_1" {
		t.Fatalf("failure context = %+v", failure.fields)
	}
	if failure.attrs["logRecordId"] != "publog_invalid_1" {
		t.Fatalf("log record id = %#v", failure.attrs["logRecordId"])
	}
}

func TestAsynqServerConfigUsesStructuredLoggerAndErrorHandler(t *testing.T) {
	handler := &ingestLogHandler{}
	logger := slog.New(handler)
	config := newIngestAsynqConfig(context.Background(), IngestOptions{Logger: logger})
	if config.Logger == nil || config.ErrorHandler == nil {
		t.Fatalf("asynq config adapters = logger:%T errorHandler:%T", config.Logger, config.ErrorHandler)
	}
	config.Logger.Info("processor ready")
	config.Logger.Warn("redis delayed")
	config.Logger.Error("redis unavailable")
	if len(handler.records) != 3 {
		t.Fatalf("log records = %d, want info/warn/error", len(handler.records))
	}
	if handler.records[0].level != slog.LevelInfo || handler.records[1].level != slog.LevelWarn || handler.records[2].level != slog.LevelError {
		t.Fatalf("levels = %v/%v/%v", handler.records[0].level, handler.records[1].level, handler.records[2].level)
	}
}

func TestAsynqErrorHandlerPreservesTaskRetryContextWithoutPayloadBody(t *testing.T) {
	handler := &ingestLogHandler{}
	logger := slog.New(handler)
	payload := []byte("secret-payload-body")
	task := asynq.NewTask("ingest:unknown-task", payload)
	taskErr := errors.New("handler exploded")
	runtime := &ingestTaskRuntime{
		taskID:     "task_error_handler_1",
		queue:      operationlogjob.QueueName,
		retryCount: 3,
		maxRetry:   10,
		known:      true,
	}

	logAsynqTaskError(context.Background(), logger, task, taskErr, runtime)
	if len(handler.records) != 1 {
		t.Fatalf("log records = %d, want 1", len(handler.records))
	}
	record := handler.records[0]
	if record.level != slog.LevelError || record.fields.JobID != "task_error_handler_1" {
		t.Fatalf("error handler record = %+v", record)
	}
	if record.attrs["taskType"] != "ingest:unknown-task" || record.attrs["queue"] != operationlogjob.QueueName || record.attrs["retryCount"] != int64(3) || record.attrs["payloadBytes"] != int64(len(payload)) {
		t.Fatalf("error handler attrs = %#v", record.attrs)
	}
	if got, ok := record.attrs["error"].(error); !ok || !errors.Is(got, taskErr) {
		t.Fatalf("error attr = %#v", record.attrs["error"])
	}
	for _, value := range record.attrs {
		if strings.Contains(fmt.Sprint(value), string(payload)) {
			t.Fatalf("payload body leaked in attrs: %#v", record.attrs)
		}
	}
}

func TestAsynqErrorHandlerDoesNotDuplicateKnownIngestTaskFailure(t *testing.T) {
	store := &fakePublicAPILogStore{}
	handler := &ingestLogHandler{}
	logger := slog.New(handler)
	config := newIngestAsynqConfig(context.Background(), IngestOptions{Logger: logger})
	task := asynq.NewTask(publicapilogjob.TaskTypeWrite, []byte(`{"version":1}`))
	runtime := &ingestTaskRuntime{taskID: "task_duplicate_1", queue: publicapilogjob.QueueName, known: true}
	err := handlePublicAPILogTaskLogged(context.Background(), logger, store, task, runtime)
	if err == nil {
		t.Fatal("handlePublicAPILogTaskLogged() error = nil, want invalid payload")
	}
	if len(handler.records) != 2 {
		t.Fatalf("handler records = %d, want start and failure", len(handler.records))
	}

	config.ErrorHandler.HandleError(context.Background(), task, err)
	if len(handler.records) != 2 {
		t.Fatalf("duplicate error records = %+v", handler.records)
	}
}

type fakePublicAPILogStore struct {
	err            error
	calls          int
	operationErr   error
	operationCalls int
}

func TestRunIngestRequiresStructuredLogger(t *testing.T) {
	store := &fakePublicAPILogStore{}
	err := RunIngest(context.Background(), IngestOptions{
		PublicAPILogStore: store,
		OperationLogStore: store,
	})
	if err == nil || !strings.Contains(err.Error(), "logger") {
		t.Fatalf("RunIngest() error = %v, want logger requirement", err)
	}
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
		TraceID:               "request_public_legacy_1",
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
		TraceID:              "request_operation_legacy_1",
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
