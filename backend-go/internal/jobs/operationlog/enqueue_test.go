package operationlog

import (
	"context"
	"testing"

	"juhe-ai/backend-go/internal/jobs/queue"
	"juhe-ai/backend-go/internal/logging"
)

type enqueueQueueClient struct {
	payload []byte
}

func (c *enqueueQueueClient) Enqueue(_ context.Context, _ string, payload []byte, _ queue.EnqueueOptions) (queue.TaskInfo, error) {
	c.payload = append([]byte(nil), payload...)
	return queue.TaskInfo{}, nil
}

func TestEnqueueWritePreservesLogContextCorrelation(t *testing.T) {
	client := &enqueueQueueClient{}
	ctx := logging.WithLogContext(context.Background(), logging.LogContext{
		TraceID:   "trace-enqueue-1",
		RequestID: "request-enqueue-1",
	})
	if _, err := EnqueueWrite(ctx, client, operationLogTaskFixture()); err != nil {
		t.Fatalf("EnqueueWrite() error = %v", err)
	}
	envelope, err := DecodeWriteTaskEnvelope(client.payload)
	if err != nil {
		t.Fatalf("DecodeWriteTaskEnvelope() error = %v", err)
	}
	if envelope.Correlation == nil || envelope.Correlation.TraceID != "trace-enqueue-1" || envelope.Correlation.RequestID != "request-enqueue-1" {
		t.Fatalf("correlation = %+v", envelope.Correlation)
	}
}
