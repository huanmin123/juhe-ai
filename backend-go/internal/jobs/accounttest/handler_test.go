package accounttest

import (
	"context"
	"errors"
	"testing"

	"juhe-ai/backend-go/internal/store/port"
)

func TestHandleTaskCompletesClaimedTask(t *testing.T) {
	store := &workerStoreStub{task: port.ManagementAccountTestTask{ID: "accttest_1"}, claimed: true}
	runner := workerRunnerStub{result: map[string]any{"ok": true}}
	payload, _ := Encode(EnqueuePayload{TaskID: "accttest_1"})
	if err := HandleTask(context.Background(), store, runner, payload); err != nil {
		t.Fatalf("HandleTask() error = %v", err)
	}
	if store.finish.Status != "completed" || store.finish.Result["ok"] != true {
		t.Fatalf("finish = %#v", store.finish)
	}
}

func TestHandleTaskMarksFailure(t *testing.T) {
	store := &workerStoreStub{task: port.ManagementAccountTestTask{ID: "accttest_1"}, claimed: true}
	payload, _ := Encode(EnqueuePayload{TaskID: "accttest_1"})
	err := HandleTask(context.Background(), store, workerRunnerStub{err: errors.New("upstream failed")}, payload)
	if err == nil || store.finish.Status != "failed" || store.finish.Message != "upstream failed" {
		t.Fatalf("err=%v finish=%#v", err, store.finish)
	}
}

func TestHandleTaskSkipsAlreadyClaimedTask(t *testing.T) {
	store := &workerStoreStub{}
	payload, _ := Encode(EnqueuePayload{TaskID: "accttest_1"})
	if err := HandleTask(context.Background(), store, workerRunnerStub{}, payload); err != nil {
		t.Fatalf("HandleTask() error = %v", err)
	}
	if store.finish.TaskID != "" {
		t.Fatalf("finish = %#v", store.finish)
	}
}

type workerStoreStub struct {
	task    port.ManagementAccountTestTask
	claimed bool
	finish  port.AccountTestWorkerFinishInput
}

func (s *workerStoreStub) ClaimAccountTestTask(context.Context, string) (port.ManagementAccountTestTask, bool, error) {
	return s.task, s.claimed, nil
}
func (s *workerStoreStub) FinishAccountTestTask(_ context.Context, input port.AccountTestWorkerFinishInput) error {
	s.finish = input
	return nil
}

type workerRunnerStub struct {
	result map[string]any
	err    error
}

func (s workerRunnerStub) RunAccountTest(context.Context, port.ManagementAccountTestTask) (map[string]any, error) {
	return s.result, s.err
}
