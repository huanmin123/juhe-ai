package cooldownaccountretest

import (
	"context"
	"testing"

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

func TestEnqueuerTreatsTaskIDConflictAsDuplicate(t *testing.T) {
	client := &fakeEnqueueClient{err: queue.ErrTaskConflict}
	queued, err := (Enqueuer{Client: client}).EnqueueCooldownAccountRetest(context.Background(), port.CooldownAccountRetestTask{AccountID: "acct_1", ConfigRevision: 1})
	if err != nil || queued {
		t.Fatalf("queued=%v err=%v", queued, err)
	}
	if client.taskType != TaskType || client.options.TaskID == "" || client.options.UniqueTTL <= 0 {
		t.Fatalf("task = %q options = %+v", client.taskType, client.options)
	}
}

func TestEnqueuerBuildsDecodableTask(t *testing.T) {
	client := &fakeEnqueueClient{}
	queued, err := (Enqueuer{Client: client}).EnqueueCooldownAccountRetest(context.Background(), port.CooldownAccountRetestTask{AccountID: "acct_1", ConfigRevision: 3})
	if err != nil || !queued {
		t.Fatalf("queued=%v err=%v", queued, err)
	}
	decoded, err := DecodeTask(client.payload)
	if err != nil {
		t.Fatalf("DecodeTask() error = %v", err)
	}
	if decoded.ConfigRevision != 3 {
		t.Fatalf("decoded = %+v", decoded)
	}
}
