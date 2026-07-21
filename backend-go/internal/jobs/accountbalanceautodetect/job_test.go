package accountbalanceautodetect

import (
	"context"
	"errors"
	"testing"

	accountbalanceservice "juhe-ai/backend-go/internal/modules/accountbalanceautodetect"
)

type runnerStub struct {
	result accountbalanceservice.Result
	err    error
	inputs []accountbalanceservice.Input
}

func (s *runnerStub) Run(_ context.Context, input accountbalanceservice.Input) (accountbalanceservice.Result, error) {
	s.inputs = append(s.inputs, input)
	return s.result, s.err
}

func TestPayloadRoundTripContainsOnlyAccountAndRevision(t *testing.T) {
	encoded, err := Encode(Task{AccountID: "account-1", ConfigRevision: 7})
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	if string(encoded) != `{"version":1,"accountId":"account-1","configRevision":7}` {
		t.Fatalf("payload = %s", encoded)
	}
	decoded, err := Decode(encoded)
	if err != nil || decoded != (Task{AccountID: "account-1", ConfigRevision: 7}) {
		t.Fatalf("Decode() = %#v, %v", decoded, err)
	}
}

func TestDecodeRejectsInvalidTask(t *testing.T) {
	for _, payload := range []string{
		`{"version":1,"accountId":"","configRevision":1}`,
		`{"version":1,"accountId":"account-1","configRevision":0}`,
		`{"version":2,"accountId":"account-1","configRevision":1}`,
	} {
		if _, err := Decode([]byte(payload)); !errors.Is(err, ErrInvalidPayload) {
			t.Fatalf("Decode(%s) error = %v, want ErrInvalidPayload", payload, err)
		}
	}
}

func TestHandleTaskTreatsUnsupportedAndStaleAsCompleted(t *testing.T) {
	for _, result := range []accountbalanceservice.Result{accountbalanceservice.ResultUnsupported, accountbalanceservice.ResultStale} {
		runner := &runnerStub{result: result}
		payload, err := Encode(Task{AccountID: "account-1", ConfigRevision: 7})
		if err != nil {
			t.Fatal(err)
		}
		if err := HandleTask(context.Background(), runner, payload); err != nil {
			t.Fatalf("HandleTask(%q) error = %v", result, err)
		}
		if len(runner.inputs) != 1 || runner.inputs[0] != (accountbalanceservice.Input{AccountID: "account-1", ConfigRevision: 7}) {
			t.Fatalf("runner inputs = %#v", runner.inputs)
		}
	}
}

func TestHandleTaskReturnsServiceErrorForAsynqRetry(t *testing.T) {
	runErr := errors.New("database unavailable")
	runner := &runnerStub{err: runErr}
	payload, err := Encode(Task{AccountID: "account-1", ConfigRevision: 7})
	if err != nil {
		t.Fatal(err)
	}
	if err := HandleTask(context.Background(), runner, payload); !errors.Is(err, runErr) {
		t.Fatalf("HandleTask() error = %v, want wrapped run error", err)
	}
}
