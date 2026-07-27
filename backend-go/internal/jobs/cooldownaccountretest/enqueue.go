package cooldownaccountretest

import (
	"context"
	"errors"
	"time"

	"juhe-ai/backend-go/internal/jobs/queue"
	"juhe-ai/backend-go/internal/store/port"
)

const (
	// While the bounded queue and consumer are running, four hours covers all 100
	// tasks, four attempts, worst-case retry delays, and a restart margin. This is
	// only a lease: a Redis/consumer outage longer than the TTL can admit the same
	// fence again, so production takeover still requires durable lifecycle proof.
	DefaultUniqueTTL           = 4 * time.Hour
	DefaultMaxRetry            = 3
	DefaultTaskTimeout         = 70 * time.Second
	DefaultConsumerConcurrency = 3
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
	payload, headers, err := EncodeTask(task)
	if err != nil {
		return false, err
	}
	ttl := e.UniqueTTL
	if ttl <= 0 {
		ttl = DefaultUniqueTTL
	}
	timeout := e.TaskTimeout
	if timeout <= 0 {
		timeout = DefaultTaskTimeout
	}
	maxRetry := DefaultMaxRetry
	if e.MaxRetry != nil {
		maxRetry = max(*e.MaxRetry, 0)
	}
	// Do not use a deterministic TaskID. Asynq retains archived task IDs, which
	// would make a later due observation conflict after a bounded retry budget.
	// The bounded unique lease deduplicates concurrent scheduler pages and retries.
	_, err = e.Client.Enqueue(ctx, TaskType, payload, queue.EnqueueOptions{
		Queue: QueueName, MaxRetry: &maxRetry, Timeout: timeout, UniqueTTL: ttl, Headers: headers,
	})
	if errors.Is(err, queue.ErrTaskConflict) {
		return false, nil
	}
	return err == nil, err
}
