package worker

import (
	"context"
	"errors"
	"testing"

	"github.com/hibiken/asynq"

	accounttestjob "juhe-ai/backend-go/internal/jobs/accounttest"
)

func TestHandleAccountTestDispatchTaskSkipsInvalidPayload(t *testing.T) {
	err := handleAccountTestDispatchTask(context.Background(), &accountTestDispatcherStub{}, []byte(`{"version":1}`))
	if !errors.Is(err, accounttestjob.ErrInvalidPayload) || !errors.Is(err, asynq.SkipRetry) {
		t.Fatalf("error = %v, want invalid payload and SkipRetry", err)
	}
}

func TestHandleAccountTestDispatchTaskKeepsBridgeErrorRetryable(t *testing.T) {
	bridgeErr := errors.New("node unavailable")
	payload, _ := accounttestjob.Encode(accounttestjob.EnqueuePayload{TaskID: "accttest_1"})
	err := handleAccountTestDispatchTask(context.Background(), &accountTestDispatcherStub{err: bridgeErr}, payload)
	if !errors.Is(err, bridgeErr) || errors.Is(err, asynq.SkipRetry) {
		t.Fatalf("error = %v, want retryable bridge error", err)
	}
}

type accountTestDispatcherStub struct{ err error }

func (s *accountTestDispatcherStub) Dispatch(context.Context, string) error { return s.err }
