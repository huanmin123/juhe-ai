package worker

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hibiken/asynq"

	job "juhe-ai/backend-go/internal/jobs/cooldownaccountretest"
	module "juhe-ai/backend-go/internal/modules/cooldownaccountretest"
	"juhe-ai/backend-go/internal/store/port"
)

func TestCooldownAccountRetestMuxRegistersOnlyAccountLevelTask(t *testing.T) {
	mux := newCooldownAccountRetestMux(module.Processor{})
	if handler, pattern := mux.Handler(asynq.NewTask(job.TaskType, nil)); handler == nil || pattern != job.TaskType {
		t.Fatalf("account-level handler=%v pattern=%q", handler, pattern)
	}
	if _, pattern := mux.Handler(asynq.NewTask("account-api-key-cooldown-retest:probe", nil)); pattern != "" {
		t.Fatalf("key-level sibling pattern=%q, want unregistered", pattern)
	}
}

func TestHandleCooldownAccountRetestTaskSkipsInvalidPayload(t *testing.T) {
	err := handleCooldownAccountRetestTask(context.Background(), module.Processor{}, []byte(`{"version":1}`))
	if !errors.Is(err, job.ErrInvalidPayload) || !errors.Is(err, asynq.SkipRetry) {
		t.Fatalf("error = %v, want invalid payload and SkipRetry", err)
	}
}

func TestHandleCooldownAccountRetestTaskDoesNotHideMissingProbe(t *testing.T) {
	payload, err := job.EncodeTask(port.CooldownAccountRetestTask{AccountID: "acct_1", ConfigRevision: 1})
	if err != nil {
		t.Fatalf("EncodeTask() error = %v", err)
	}
	processor := module.Processor{
		Store:    cooldownRetestStoreStub{},
		Outcomes: cooldownRetestOutcomeStoreStub{},
	}
	err = handleCooldownAccountRetestTask(context.Background(), processor, payload)
	if !errors.Is(err, module.ErrProbeNotConfigured) || errors.Is(err, asynq.SkipRetry) {
		t.Fatalf("error = %v, want visible missing probe error", err)
	}
}

func TestRunCooldownAccountRetestConsumerFailsBeforeRedisWhenProcessorIncomplete(t *testing.T) {
	err := RunCooldownAccountRetestConsumer(context.Background(), CooldownAccountRetestConsumerOptions{})
	if err == nil {
		t.Fatal("RunCooldownAccountRetestConsumer() error = nil")
	}
}

func TestCooldownAccountRetestTrackedMuxWaitsForOutcomeHandlerReturn(t *testing.T) {
	taskPayload, err := job.EncodeTask(port.CooldownAccountRetestTask{AccountID: "acct_1", ConfigRevision: 1})
	if err != nil {
		t.Fatalf("EncodeTask() error = %v", err)
	}
	outcomes := &cooldownRetestBlockingOutcomeStoreStub{started: make(chan struct{}), release: make(chan struct{})}
	processor := module.Processor{
		Store: cooldownRetestDueStoreStub{}, Outcomes: outcomes, Probe: cooldownRetestSuccessProbeStub{},
	}
	handlers := newCooldownAccountRetestHandlerTracker()
	mux := newCooldownAccountRetestMuxWithTracker(processor, handlers)
	task := asynq.NewTask(job.TaskType, taskPayload)
	handler, pattern := mux.Handler(task)
	if handler == nil || pattern != job.TaskType {
		t.Fatalf("handler=%v pattern=%q", handler, pattern)
	}
	handlerDone := make(chan error, 1)
	go func() { handlerDone <- handler.ProcessTask(context.Background(), task) }()
	<-outcomes.started
	waitDone := make(chan struct{})
	go func() {
		handlers.CloseAndWait()
		close(waitDone)
	}()
	select {
	case <-waitDone:
		t.Fatal("handler tracker returned before outcome writer")
	case <-time.After(20 * time.Millisecond):
	}
	close(outcomes.release)
	if err := <-handlerDone; err != nil {
		t.Fatalf("handler error = %v", err)
	}
	select {
	case <-waitDone:
	case <-time.After(time.Second):
		t.Fatal("handler tracker did not return after outcome writer")
	}
}

func TestCooldownAccountRetestTrackedMuxRejectsHandlerAfterClose(t *testing.T) {
	taskPayload, err := job.EncodeTask(port.CooldownAccountRetestTask{AccountID: "acct_1", ConfigRevision: 1})
	if err != nil {
		t.Fatalf("EncodeTask() error = %v", err)
	}
	outcomes := &cooldownRetestBlockingOutcomeStoreStub{started: make(chan struct{}), release: make(chan struct{})}
	processor := module.Processor{
		Store: cooldownRetestDueStoreStub{}, Outcomes: outcomes, Probe: cooldownRetestSuccessProbeStub{},
	}
	handlers := newCooldownAccountRetestHandlerTracker()
	mux := newCooldownAccountRetestMuxWithTracker(processor, handlers)
	handlers.CloseAndWait()
	task := asynq.NewTask(job.TaskType, taskPayload)
	handler, _ := mux.Handler(task)
	if err := handler.ProcessTask(context.Background(), task); !errors.Is(err, context.Canceled) {
		t.Fatalf("handler error = %v, want cancellation", err)
	}
	select {
	case <-outcomes.started:
		t.Fatal("closed tracker admitted a late outcome writer")
	default:
	}
}

type cooldownRetestStoreStub struct{}

func (cooldownRetestStoreStub) ListDueCooldownAccountRetests(context.Context, port.CooldownAccountRetestListInput) (port.CooldownAccountRetestPage, error) {
	return port.CooldownAccountRetestPage{}, nil
}

type cooldownRetestDueStoreStub struct{}

func (cooldownRetestDueStoreStub) ListDueCooldownAccountRetests(context.Context, port.CooldownAccountRetestListInput) (port.CooldownAccountRetestPage, error) {
	return port.CooldownAccountRetestPage{}, nil
}

func (cooldownRetestDueStoreStub) FindDueCooldownAccountRetest(context.Context, string, time.Time) (port.CooldownAccountRetestCandidate, bool, error) {
	return port.CooldownAccountRetestCandidate{ID: "acct_1", ConfigRevision: 1}, true, nil
}

type cooldownRetestSuccessProbeStub struct{}

func (cooldownRetestSuccessProbeStub) Probe(context.Context, port.CooldownAccountRetestCandidate) (port.CooldownAccountRetestProbeResult, error) {
	return port.CooldownAccountRetestProbeResult{Outcome: "complete_success"}, nil
}

type cooldownRetestBlockingOutcomeStoreStub struct {
	started chan struct{}
	release chan struct{}
}

func (s *cooldownRetestBlockingOutcomeStoreStub) RecordCooldownAccountRetestSuccess(context.Context, port.CooldownAccountRetestTask) error {
	close(s.started)
	<-s.release
	return nil
}

func (*cooldownRetestBlockingOutcomeStoreStub) DeferCooldownAccountRetest(context.Context, port.CooldownAccountRetestTask, time.Duration) error {
	return nil
}

func (*cooldownRetestBlockingOutcomeStoreStub) RecordCooldownAccountRetestFailure(context.Context, port.CooldownAccountRetestTask, port.CooldownAccountRetestProbeResult) error {
	return nil
}

func (cooldownRetestStoreStub) FindDueCooldownAccountRetest(context.Context, string, time.Time) (port.CooldownAccountRetestCandidate, bool, error) {
	return port.CooldownAccountRetestCandidate{}, false, nil
}

type cooldownRetestOutcomeStoreStub struct{}

func (cooldownRetestOutcomeStoreStub) RecordCooldownAccountRetestSuccess(context.Context, port.CooldownAccountRetestTask) error {
	return nil
}

func (cooldownRetestOutcomeStoreStub) DeferCooldownAccountRetest(context.Context, port.CooldownAccountRetestTask, time.Duration) error {
	return nil
}

func (cooldownRetestOutcomeStoreStub) RecordCooldownAccountRetestFailure(context.Context, port.CooldownAccountRetestTask, port.CooldownAccountRetestProbeResult) error {
	return nil
}
