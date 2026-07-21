package accounttest

import (
	"context"
	"errors"
	"testing"
)

func TestHandleDispatchTaskSendsDecodedTaskID(t *testing.T) {
	dispatcher := &dispatchStub{}
	payload, _ := Encode(EnqueuePayload{TaskID: "accttest_1"})
	if err := HandleDispatchTask(context.Background(), dispatcher, payload); err != nil {
		t.Fatalf("HandleDispatchTask() error = %v", err)
	}
	if dispatcher.taskID != "accttest_1" {
		t.Fatalf("task ID = %q", dispatcher.taskID)
	}
}

func TestHandleDispatchTaskPreservesBridgeFailure(t *testing.T) {
	bridgeErr := errors.New("node unavailable")
	dispatcher := &dispatchStub{err: bridgeErr}
	payload, _ := Encode(EnqueuePayload{TaskID: "accttest_2"})
	if err := HandleDispatchTask(context.Background(), dispatcher, payload); !errors.Is(err, bridgeErr) {
		t.Fatalf("error = %v, want bridge error", err)
	}
}

type dispatchStub struct {
	taskID string
	err    error
}

func (s *dispatchStub) Dispatch(_ context.Context, taskID string) error {
	s.taskID = taskID
	return s.err
}
