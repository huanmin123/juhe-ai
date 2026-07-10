package publicapilog

import (
	"context"
	"testing"

	"juhe-ai/backend-go/internal/jobs/queue"
)

type fakeQueueClient struct {
	taskType string
	payload  []byte
	opts     queue.EnqueueOptions
	info     queue.TaskInfo
	err      error
}

func (f *fakeQueueClient) Enqueue(_ context.Context, taskType string, payload []byte, opts queue.EnqueueOptions) (queue.TaskInfo, error) {
	f.taskType = taskType
	f.payload = append([]byte(nil), payload...)
	f.opts = opts
	return f.info, f.err
}

func TestEnqueueWrite(t *testing.T) {
	client := &fakeQueueClient{
		info: queue.TaskInfo{ID: "task_1", Queue: QueueName, Type: TaskTypeWrite},
	}
	input := publicAPILogFixture()
	info, err := EnqueueWrite(context.Background(), client, input)
	if err != nil {
		t.Fatalf("EnqueueWrite() error = %v", err)
	}
	if info.ID != "task_1" {
		t.Fatalf("info = %+v", info)
	}
	if client.taskType != TaskTypeWrite || client.opts.Queue != QueueName {
		t.Fatalf("enqueue = %s %+v", client.taskType, client.opts)
	}
	if client.opts.MaxRetry == nil || *client.opts.MaxRetry != defaultMaxRetry {
		t.Fatalf("max retry = %v", client.opts.MaxRetry)
	}
	decoded, err := DecodeWriteTaskPayload(client.payload)
	if err != nil {
		t.Fatalf("DecodeWriteTaskPayload() error = %v", err)
	}
	if decoded.ID != input.ID || decoded.Path != input.Path {
		t.Fatalf("decoded = %+v", decoded)
	}
}

func TestEnqueueWriteRequiresClient(t *testing.T) {
	if _, err := EnqueueWrite(context.Background(), nil, publicAPILogFixture()); err == nil {
		t.Fatal("EnqueueWrite() error = nil, want client error")
	}
}
