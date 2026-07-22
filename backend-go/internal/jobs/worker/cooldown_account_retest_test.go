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

type cooldownRetestStoreStub struct{}

func (cooldownRetestStoreStub) ListDueCooldownAccountRetests(context.Context, port.CooldownAccountRetestListInput) (port.CooldownAccountRetestPage, error) {
	return port.CooldownAccountRetestPage{}, nil
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
