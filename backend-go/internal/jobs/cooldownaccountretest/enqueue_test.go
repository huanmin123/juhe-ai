package cooldownaccountretest

import (
	"context"
	"testing"

	"github.com/hibiken/asynq"
	"juhe-ai/backend-go/internal/store/port"
)

type fakeAsynqClient struct {
	task *asynq.Task
	err  error
}

func (f *fakeAsynqClient) EnqueueContext(_ context.Context, task *asynq.Task, _ ...asynq.Option) (*asynq.TaskInfo, error) {
	f.task = task
	return &asynq.TaskInfo{}, f.err
}

func TestEnqueuerTreatsTaskIDConflictAsDuplicate(t *testing.T) {
	client := &fakeAsynqClient{err: asynq.ErrTaskIDConflict}
	queued, err := (Enqueuer{Client: client}).EnqueueCooldownAccountRetest(context.Background(), port.CooldownAccountRetestTask{AccountID: "acct_1", ConfigRevision: 1})
	if err != nil || queued {
		t.Fatalf("queued=%v err=%v", queued, err)
	}
	if client.task == nil || client.task.Type() != TaskType {
		t.Fatalf("task = %#v", client.task)
	}
}

func TestEnqueuerBuildsDecodableTask(t *testing.T) {
	client := &fakeAsynqClient{}
	queued, err := (Enqueuer{Client: client}).EnqueueCooldownAccountRetest(context.Background(), port.CooldownAccountRetestTask{AccountID: "acct_1", ConfigRevision: 3})
	if err != nil || !queued {
		t.Fatalf("queued=%v err=%v", queued, err)
	}
	decoded, err := DecodeTask(client.task.Payload())
	if err != nil {
		t.Fatalf("DecodeTask() error = %v", err)
	}
	if decoded.ConfigRevision != 3 {
		t.Fatalf("decoded = %+v", decoded)
	}
}
