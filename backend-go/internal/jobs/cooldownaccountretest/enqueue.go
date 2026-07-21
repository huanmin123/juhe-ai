package cooldownaccountretest

import (
	"context"
	"errors"
	"time"

	"github.com/hibiken/asynq"
	"juhe-ai/backend-go/internal/store/port"
)

type AsynqClient interface {
	EnqueueContext(context.Context, *asynq.Task, ...asynq.Option) (*asynq.TaskInfo, error)
}

type Enqueuer struct {
	Client      AsynqClient
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
	_, err = e.Client.EnqueueContext(ctx, asynq.NewTask(TaskType, payload), asynq.Queue(QueueName), asynq.TaskID(UniqueKey(task)), asynq.Unique(ttl), asynq.Timeout(timeout), asynq.MaxRetry(0))
	if errors.Is(err, asynq.ErrTaskIDConflict) {
		return false, nil
	}
	return err == nil, err
}
