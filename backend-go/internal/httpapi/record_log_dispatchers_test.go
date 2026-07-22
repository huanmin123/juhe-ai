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
