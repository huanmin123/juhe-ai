package cooldownaccountretest

import (
	"context"
	"errors"
	"time"

	"juhe-ai/backend-go/internal/jobs/queue"
	"juhe-ai/backend-go/internal/store/port"
)

type EnqueueClient interface {
	Enqueue(context.Context, string, []byte, queue.EnqueueOptions) (queue.TaskInfo, error)
}

type Enqueuer struct {
	Client      EnqueueClient
	UniqueTTL   time.Duration
	TaskTimeout time.Duration
}

func (e Enqueuer) EnqueueCooldownAccountRetest(ctx context.Context, task port.CooldownAccountRetestTask) (bool, error) {
	if e.Client == nil {
		return false, errors.New("asynq client is required")
	}
	payload, err := EncodeTask(task)
	if err != nil {
		return false, err
	}
	ttl := e.UniqueTTL
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	timeout := e.TaskTimeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	noRetry := 0
	_, err = e.Client.Enqueue(ctx, TaskType, payload, queue.EnqueueOptions{Queue: QueueName, MaxRetry: &noRetry, Timeout: timeout, TaskID: UniqueKey(task), UniqueTTL: ttl})
	if errors.Is(err, queue.ErrTaskConflict) {
		return false, nil
	}
	return err == nil, err
}
