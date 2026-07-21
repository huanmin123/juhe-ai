package publicapilog

import (
	"context"
	"fmt"
	"time"

	"juhe-ai/backend-go/internal/jobs/queue"
	"juhe-ai/backend-go/internal/logging"
	"juhe-ai/backend-go/internal/store/port"
)

const (
	defaultTaskTimeout   = 30 * time.Second
	defaultTaskRetention = 24 * time.Hour
	defaultMaxRetry      = 10
)

type EnqueueClient interface {
	Enqueue(ctx context.Context, taskType string, payload []byte, opts queue.EnqueueOptions) (queue.TaskInfo, error)
}

func EnqueueWrite(ctx context.Context, client EnqueueClient, input port.PublicAPILogInput) (queue.TaskInfo, error) {
	if client == nil {
		return queue.TaskInfo{}, fmt.Errorf("public api log queue client is required")
	}
	logContext := logging.LogContextFrom(ctx)
	payload, err := EncodeWriteTaskPayloadWithCorrelation(input, TaskCorrelation{
		TraceID:   logContext.TraceID,
		RequestID: logContext.RequestID,
	})
	if err != nil {
		return queue.TaskInfo{}, err
	}
	maxRetry := defaultMaxRetry
	return client.Enqueue(ctx, TaskTypeWrite, payload, queue.EnqueueOptions{
		Queue:     QueueName,
		MaxRetry:  &maxRetry,
		Timeout:   defaultTaskTimeout,
		Retention: defaultTaskRetention,
	})
}
