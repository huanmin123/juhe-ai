package cooldownaccountretest

import (
	"context"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/jobs/queue"
	"juhe-ai/backend-go/internal/store/port"
)

type fakeEnqueueClient struct {
	taskType string
	payload  []byte
	options  queue.EnqueueOptions
	err      error
}

func (f *fakeEnqueueClient) Enqueue(_ context.Context, taskType string, payload []byte, options queue.EnqueueOptions) (queue.TaskInfo, error) {
	f.taskType, f.payload, f.options = taskType, payload, options
	return queue.TaskInfo{}, f.err
}

func TestEnqueuerTreatsUniqueTaskConflictAsDuplicate(t *testing.T) {
	client := &fakeEnqueueClient{err: queue.ErrTaskConflict}
	queued, err := (Enqueuer{Client: client}).EnqueueCooldownAccountRetest(context.Background(), validCooldownRetestTask())
	if err != nil || queued {
		t.Fatalf("queued=%v err=%v", queued, err)
	}
	if client.taskType != TaskType || client.options.TaskID != "" || client.options.UniqueTTL != DefaultUniqueTTL {
		t.Fatalf("task = %q options = %+v", client.taskType, client.options)
	}
}

func TestEnqueuerUsesBoundedRetriesWithoutStableArchiveTaskID(t *testing.T) {
	client := &fakeEnqueueClient{}
	queued, err := (Enqueuer{Client: client}).EnqueueCooldownAccountRetest(context.Background(), validCooldownRetestTask())
	if err != nil || !queued {
		t.Fatalf("queued=%v err=%v", queued, err)
	}
	if client.options.MaxRetry == nil || *client.options.MaxRetry != DefaultMaxRetry {
		t.Fatalf("max retry = %v, want %d", client.options.MaxRetry, DefaultMaxRetry)
	}
	if client.options.Timeout != DefaultTaskTimeout {
		t.Fatalf("task timeout = %s, want %s", client.options.Timeout, DefaultTaskTimeout)
	}
	if client.options.TaskID != "" {
		t.Fatalf("stable TaskID = %q; archived tasks must not block rescheduling", client.options.TaskID)
	}
	executionWaves := (port.CooldownAccountRetestMaxPageSize + DefaultConsumerConcurrency - 1) / DefaultConsumerConcurrency
	minimumRetryLifecycle := time.Duration(executionWaves*(DefaultMaxRetry+1))*DefaultTaskTimeout + 40*time.Minute
	if client.options.UniqueTTL < minimumRetryLifecycle {
		t.Fatalf("unique TTL = %s, must cover retry lifecycle of at least %s", client.options.UniqueTTL, minimumRetryLifecycle)
	}
}

func TestEnqueuerBuildsDecodableTask(t *testing.T) {
	client := &fakeEnqueueClient{}
	task := validCooldownRetestTask()
	task.ConfigRevision = 3
	queued, err := (Enqueuer{Client: client}).EnqueueCooldownAccountRetest(context.Background(), task)
	if err != nil || !queued {
		t.Fatalf("queued=%v err=%v", queued, err)
	}
	decoded, err := DecodeTask(client.payload, client.options.Headers)
	if err != nil {
		t.Fatalf("DecodeTask() error = %v", err)
	}
	if decoded.ConfigRevision != 3 {
		t.Fatalf("decoded = %+v", decoded)
	}
}

func TestEnqueuerUsesSameAsynqUniquePayloadForDifferentStrategies(t *testing.T) {
	firstClient := &fakeEnqueueClient{}
	secondClient := &fakeEnqueueClient{}
	first := validCooldownRetestTask()
	second := first
	second.MaxPauseMinutes++
	second.MaxRecoveryHours++
	if queued, err := (Enqueuer{Client: firstClient}).EnqueueCooldownAccountRetest(context.Background(), first); err != nil || !queued {
		t.Fatalf("first queued=%v err=%v", queued, err)
	}
	if queued, err := (Enqueuer{Client: secondClient}).EnqueueCooldownAccountRetest(context.Background(), second); err != nil || !queued {
		t.Fatalf("second queued=%v err=%v", queued, err)
	}
	if string(firstClient.payload) != string(secondClient.payload) {
		t.Fatalf("Asynq Unique hashes full payload; same fence must use identical bytes:\n%s\n%s", firstClient.payload, secondClient.payload)
	}
	if firstClient.options.Headers[maxPauseMinutesHeader] == secondClient.options.Headers[maxPauseMinutesHeader] ||
		firstClient.options.Headers[maxRecoveryHoursHeader] == secondClient.options.Headers[maxRecoveryHoursHeader] {
		t.Fatalf("strategy headers were not preserved: first=%v second=%v", firstClient.options.Headers, secondClient.options.Headers)
	}
}
