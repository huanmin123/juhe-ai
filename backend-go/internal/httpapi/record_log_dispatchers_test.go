package httpapi

import (
	"context"
	"testing"
	"time"

	operationlogjob "juhe-ai/backend-go/internal/jobs/operationlog"
	publicapilogjob "juhe-ai/backend-go/internal/jobs/publicapilog"
	"juhe-ai/backend-go/internal/jobs/queue"
	"juhe-ai/backend-go/internal/logging"
	"juhe-ai/backend-go/internal/store/port"
)

func TestManagementOperationLogDispatcherMovesSettingsAndQueueOffCaller(t *testing.T) {
	settingsStarted := make(chan struct{}, 1)
	releaseSettings := make(chan struct{})
	reader := operationLogSettingsReaderFunc(func(context.Context) (int, error) {
		settingsStarted <- struct{}{}
		<-releaseSettings
		return 50, nil
	})
	client := &capturingOperationLogQueue{calls: make(chan operationLogQueueCall, 1), done: make(chan struct{})}
	dispatcher := NewManagementOperationLogDispatcher(client, reader, nil)
	t.Cleanup(func() { shutdownRecordDispatcher(t, dispatcher) })

	ctx := logging.WithLogContext(context.Background(), logging.LogContext{
		TraceID:   "trace-op-1",
		RequestID: "request-op-1",
	})
	startedAt := time.Now()
	if !dispatcher.Submit(ctx, operationLogDispatcherInput("oplog_1")) {
		t.Fatal("Submit() = false, want true")
	}
	if elapsed := time.Since(startedAt); elapsed > 100*time.Millisecond {
		t.Fatalf("Submit() blocked for %s", elapsed)
	}

	select {
	case <-settingsStarted:
	case <-time.After(time.Second):
		t.Fatal("settings reader was not called")
	}
	close(releaseSettings)

	select {
	case call := <-client.calls:
		envelope, err := operationlogjob.DecodeWriteTaskEnvelope(call.payload)
		if err != nil {
			t.Fatalf("DecodeWriteTaskEnvelope() error = %v", err)
		}
		if envelope.Correlation == nil || envelope.Correlation.TraceID != "trace-op-1" || envelope.Correlation.RequestID != "request-op-1" {
			t.Fatalf("correlation = %+v, want trace-op-1/request-op-1", envelope.Correlation)
		}
	case <-time.After(time.Second):
		t.Fatal("queue client did not receive a task")
	}
}

func TestManagementOperationLogDispatcherDropsWhenSaturated(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	reader := operationLogSettingsReaderFunc(func(context.Context) (int, error) {
		started <- struct{}{}
		<-release
		return 50, nil
	})
	dispatcher := newManagementOperationLogDispatcher(1, reader, &capturingOperationLogQueue{calls: make(chan operationLogQueueCall, 2), done: make(chan struct{})}, nil)
	t.Cleanup(func() {
		close(release)
		shutdownRecordDispatcher(t, dispatcher)
	})

	if !dispatcher.Submit(context.Background(), operationLogDispatcherInput("oplog_1")) {
		t.Fatal("first Submit() = false, want true")
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("settings reader was not called")
	}
	if !dispatcher.Submit(context.Background(), operationLogDispatcherInput("oplog_2")) {
		t.Fatal("second Submit() = false, want true")
	}
	startedAt := time.Now()
	if dispatcher.Submit(context.Background(), operationLogDispatcherInput("oplog_3")) {
		t.Fatal("saturated Submit() = true, want false")
	}
	if elapsed := time.Since(startedAt); elapsed > 100*time.Millisecond {
		t.Fatalf("saturated Submit() blocked for %s", elapsed)
	}
}

func TestManagementOperationLogDispatcherDoesNotWaitForBlockedQueue(t *testing.T) {
	client := &blockingOperationLogQueue{started: make(chan struct{}), release: make(chan struct{})}
	dispatcher := NewManagementOperationLogDispatcher(client, nil, nil)
	t.Cleanup(func() {
		close(client.release)
		shutdownRecordDispatcher(t, dispatcher)
	})

	startedAt := time.Now()
	if !dispatcher.Submit(context.Background(), operationLogDispatcherInput("oplog_blocked_queue")) {
		t.Fatal("Submit() = false, want true")
	}
	if elapsed := time.Since(startedAt); elapsed > 100*time.Millisecond {
		t.Fatalf("Submit() blocked for %s", elapsed)
	}
	select {
	case <-client.started:
	case <-time.After(time.Second):
		t.Fatal("queue client was not called")
	}
}

func TestPublicAPILogDispatcherPreservesLogContextCorrelation(t *testing.T) {
	client := &capturingOperationLogQueue{calls: make(chan operationLogQueueCall, 1), done: make(chan struct{})}
	dispatcher := NewPublicAPILogDispatcher(client, nil)
	t.Cleanup(func() { shutdownRecordDispatcher(t, dispatcher) })
	ctx := logging.WithLogContext(context.Background(), logging.LogContext{
		TraceID:   "trace-public-1",
		RequestID: "request-public-1",
	})
	now := time.Now().UTC()
	if !dispatcher.Submit(ctx, port.PublicAPILogInput{
		ID:        "publog_1",
		Method:    "GET",
		Path:      "/__aipublic__/group/list",
		StartedAt: now,
		EndedAt:   now,
	}) {
		t.Fatal("Submit() = false, want true")
	}
	select {
	case call := <-client.calls:
		envelope, err := publicapilogjob.DecodeWriteTaskEnvelope(call.payload)
		if err != nil {
			t.Fatalf("DecodeWriteTaskEnvelope() error = %v", err)
		}
		if envelope.Correlation == nil || envelope.Correlation.TraceID != "trace-public-1" || envelope.Correlation.RequestID != "request-public-1" {
			t.Fatalf("correlation = %+v, want trace-public-1/request-public-1", envelope.Correlation)
		}
	case <-time.After(time.Second):
		t.Fatal("queue client did not receive a task")
	}
}

func TestRecordLogClonesDetachMutableValues(t *testing.T) {
	statusCode := 200
	duration := int64(12)
	operationInput := port.OperationLogInput{
		Changes:    []port.OperationLogChange{{Before: map[string]any{"items": []any{"before"}}}},
		Metadata:   map[string]any{"labels": []string{"one"}},
		Targets:    []port.OperationLogTargetInput{{TargetID: "target-1"}},
		Viewers:    []port.OperationLogViewerInput{{SystemAccountID: "viewer-1"}},
		StatusCode: &statusCode,
	}
	operationClone := cloneOperationLogInput(operationInput)
	operationInput.Changes[0].Before.(map[string]any)["items"].([]any)[0] = "after"
	operationInput.Metadata["labels"].([]string)[0] = "two"
	operationInput.Targets[0].TargetID = "target-2"
	operationInput.Viewers[0].SystemAccountID = "viewer-2"
	statusCode = 500
	if got := operationClone.Changes[0].Before.(map[string]any)["items"].([]any)[0]; got != "before" {
		t.Fatalf("operation change clone = %v", got)
	}
	if got := operationClone.Metadata["labels"].([]string)[0]; got != "one" {
		t.Fatalf("metadata clone = %v", got)
	}
	if operationClone.Targets[0].TargetID != "target-1" || operationClone.Viewers[0].SystemAccountID != "viewer-1" || *operationClone.StatusCode != 200 {
		t.Fatalf("operation clone retained mutable aliases: %+v", operationClone)
	}

	publicInput := port.PublicAPILogInput{
		RequestData:  map[string]any{"items": []any{"request"}},
		ResponseData: map[string]any{"items": []any{"response"}},
		StatusCode:   &statusCode,
		DurationMs:   &duration,
	}
	publicClone := clonePublicAPILogInput(publicInput)
	publicInput.RequestData["items"].([]any)[0] = "changed"
	publicInput.ResponseData["items"].([]any)[0] = "changed"
	statusCode = 201
	duration = 99
	if publicClone.RequestData["items"].([]any)[0] != "request" || publicClone.ResponseData["items"].([]any)[0] != "response" || *publicClone.StatusCode != 500 || *publicClone.DurationMs != 12 {
		t.Fatalf("public clone retained mutable aliases: %+v", publicClone)
	}
}

type operationLogSettingsReaderFunc func(context.Context) (int, error)

func (f operationLogSettingsReaderFunc) OperationLogMaxChangesPerRecord(ctx context.Context) (int, error) {
	return f(ctx)
}

func operationLogDispatcherInput(id string) port.OperationLogInput {
	return port.OperationLogInput{
		ID:                   id,
		ActorSystemAccountID: "sys_user",
		ActorRole:            "admin",
		Module:               "accounts",
		Action:               "update",
		OperationKey:         "accounts.update",
		ResourceType:         "account",
		Summary:              "更新账户",
		CreatedAt:            time.Now().UTC(),
	}
}

type operationLogQueueCall struct {
	payload []byte
}

type capturingOperationLogQueue struct {
	calls chan operationLogQueueCall
	done  chan struct{}
}

type blockingOperationLogQueue struct {
	started chan struct{}
	release chan struct{}
}

type blockingPublicAPILogQueue struct {
	started chan struct{}
	release chan struct{}
}

func (q *blockingPublicAPILogQueue) Enqueue(_ context.Context, _ string, _ []byte, _ queue.EnqueueOptions) (queue.TaskInfo, error) {
	close(q.started)
	<-q.release
	return queue.TaskInfo{}, nil
}

func (q *blockingOperationLogQueue) Enqueue(_ context.Context, _ string, _ []byte, _ queue.EnqueueOptions) (queue.TaskInfo, error) {
	close(q.started)
	<-q.release
	return queue.TaskInfo{}, nil
}

func (q *capturingOperationLogQueue) Enqueue(_ context.Context, _ string, payload []byte, _ queue.EnqueueOptions) (queue.TaskInfo, error) {
	copyPayload := append([]byte(nil), payload...)
	select {
	case q.calls <- operationLogQueueCall{payload: copyPayload}:
	case <-q.done:
	}
	return queue.TaskInfo{}, nil
}

func shutdownRecordDispatcher(t *testing.T, dispatcher interface {
	Shutdown(context.Context) error
}) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := dispatcher.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
}
