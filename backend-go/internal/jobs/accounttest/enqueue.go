package accounttest

import (
	"context"
	"fmt"
	"time"

	"juhe-ai/backend-go/internal/jobs/queue"
)

const (
	defaultTaskTimeout   = 10 * time.Minute
	defaultTaskRetention = 24 * time.Hour
	defaultMaxRetry      = 5
)

type EnqueueClient interface {
	Enqueue(ctx context.Context, taskType string, payload []byte, opts queue.EnqueueOptions) (queue.TaskInfo, error)
}

func Enqueue(ctx context.Context, client EnqueueClient, payload EnqueuePayload) (queue.TaskInfo, error) {
	if client == nil {
		return queue.TaskInfo{}, fmt.Errorf("account test queue client is required")
	}
	data, err := Encode(payload)
	if err != nil {
		return queue.TaskInfo{}, err
	}
	maxRetry := defaultMaxRetry
	return client.Enqueue(ctx, TaskType, data, queue.EnqueueOptions{
		Queue:     QueueName,
		MaxRetry:  &maxRetry,
		Timeout:   defaultTaskTimeout,
		Retention: defaultTaskRetention,
	})
}
