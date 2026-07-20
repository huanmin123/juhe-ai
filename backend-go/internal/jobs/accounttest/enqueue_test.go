package accounttest

import (
	"context"
	"testing"

	"juhe-ai/backend-go/internal/jobs/queue"
)

func TestEnqueueUsesFixedContractAndBoundedOptions(t *testing.T) {
	client := &enqueueClientStub{info: queue.TaskInfo{ID: "queue_1"}}
	info, err := Enqueue(context.Background(), client, EnqueuePayload{TaskID: "accttest_1"})
	if err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	if info.ID != "queue_1" || client.taskType != TaskType || client.options.Queue != QueueName {
		t.Fatalf("info=%+v taskType=%q options=%+v", info, client.taskType, client.options)
	}
	if client.options.MaxRetry == nil || *client.options.MaxRetry < 0 || client.options.Timeout <= 0 || client.options.Retention <= 0 {
		t.Fatalf("options are not bounded: %+v", client.options)
	}
	decoded, err := Decode(client.payload)
	if err != nil || decoded.Version != 1 || decoded.TaskID != "accttest_1" {
		t.Fatalf("decoded=%+v err=%v", decoded, err)
	}
}

type enqueueClientStub struct {
	taskType string
	payload  []byte
	options  queue.EnqueueOptions
	info     queue.TaskInfo
	err      error
}

func (s *enqueueClientStub) Enqueue(_ context.Context, taskType string, payload []byte, options queue.EnqueueOptions) (queue.TaskInfo, error) {
	s.taskType = taskType
	s.payload = append([]byte(nil), payload...)
	s.options = options
	return s.info, s.err
}
