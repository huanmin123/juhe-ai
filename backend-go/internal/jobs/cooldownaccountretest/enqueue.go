package cooldownaccountretest

import (
	"context"
	"errors"
	"time"

	"juhe-ai/backend-go/internal/jobs/queue"
	"juhe-ai/backend-go/internal/store/port"
)

const (
	DefaultUniqueTTL = 2 * time.Minute
	DefaultMaxRetry  = 3
)

type EnqueueClient interface {
	Enqueue(context.Context, string, []byte, queue.EnqueueOptions) (queue.TaskInfo, error)
}

type Enqueuer struct {
	Client      EnqueueClient
	UniqueTTL   time.Duration
	TaskTimeout time.Duration
	MaxRetry    *int
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
		ttl = DefaultUniqueTTL
	}
	timeout := e.TaskTimeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	maxRetry := DefaultMaxRetry
	if e.MaxRetry != nil {
		maxRetry = max(*e.MaxRetry, 0)
	}
	// Do not use a deterministic TaskID. Asynq retains archived task IDs, which
	// would make a later due observation conflict after a bounded retry budget.
	// The short unique window deduplicates concurrent scheduler pages instead.
	_, err = e.Client.Enqueue(ctx, TaskType, payload, queue.EnqueueOptions{Queue: QueueName, MaxRetry: &maxRetry, Timeout: timeout, UniqueTTL: ttl})
	if errors.Is(err, queue.ErrTaskConflict) {
		return false, nil
	}
	return err == nil, err
}
