package gatewayrequestfinalization

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/gatewayaudit"
	"juhe-ai/backend-go/internal/modules/gatewayhttpcompletion"
	"juhe-ai/backend-go/internal/modules/gatewayresponse"
	"juhe-ai/backend-go/internal/modules/gatewayusage"
)

func TestFinalizeResponseFinishedBuildsDetachedFactsAndReleasesOnce(t *testing.T) {
	started := time.Now().Add(-time.Second)
	observer := gatewayhttpcompletion.New(context.Background())
	capture, err := gatewayaudit.NewCapture(16*1024, gatewayaudit.MetadataDTO{TraceID: "trace-1", Method: "POST", Path: "/v1/responses"})
	if err != nil {
		t.Fatal(err)
	}
	var releases atomic.Int32
	finalizer, err := New(Input{
		Observer: observer,
		Request:  gatewayusage.RequestFacts{TraceID: "trace-1", TrafficSource: gatewayusage.TrafficSourceGateway, SystemAccountID: "sys-1", Endpoint: "/v1/responses", StartedAt: started},
		Response: gatewayresponse.Handoff{
			Usage: gatewayusage.TerminalFacts{Outcome: gatewayusage.OutcomeSucceeded, StatusCode: intPtr(200), Usage: gatewayusage.UsageFacts{OutputTokens: int64Ptr(3)}},
			Audit: gatewayaudit.TerminalInput{Success: true, RequestedOutcome: gatewayaudit.OutcomeSuccess},
		},
		Capture: capture,
		Release: func() { releases.Add(1) },
	})
	if err != nil {
		t.Fatal(err)
	}
	observer.Complete()
	first, err := finalizer.Finalize()
	if err != nil {
		t.Fatal(err)
	}
	second, err := finalizer.Finalize()
	if err != nil {
		t.Fatal(err)
	}
	if first.Usage.Outcome != gatewayusage.OutcomeSucceeded || first.Usage.CompletedAt.IsZero() || first.Audit.DTO.Terminal.Outcome != gatewayaudit.OutcomeSuccess {
		t.Fatalf("handoff = %#v", first)
	}
	if first.Usage.CompletedAt.Before(started) || second.Usage.TraceID != first.Usage.TraceID || releases.Load() != 1 {
		t.Fatalf("timestamps/second/release = %v/%v/%d", first.Usage.CompletedAt, second.Usage.TraceID, releases.Load())
	}
}

func TestFinalizeClientCancellationOverridesResponseSuccess(t *testing.T) {
	observer := gatewayhttpcompletion.New(context.Background())
	capture, err := gatewayaudit.NewCapture(16 * 1024)
	if err != nil {
		t.Fatal(err)
	}
	finalizer, err := New(Input{
		Observer: observer,
		Request:  gatewayusage.RequestFacts{TraceID: "trace-2", TrafficSource: gatewayusage.TrafficSourceGateway, SystemAccountID: "sys-1", Endpoint: "/v1/chat/completions", StartedAt: time.Now().Add(-time.Second)},
		Response: gatewayresponse.Handoff{
			Usage: gatewayusage.TerminalFacts{Outcome: gatewayusage.OutcomeSucceeded, StatusCode: intPtr(200)},
			Audit: gatewayaudit.TerminalInput{Success: true, RequestedOutcome: gatewayaudit.OutcomeSuccess},
		},
		Capture: capture,
	})
	if err != nil {
		t.Fatal(err)
	}
	observer.CompleteClientCanceled()
	handoff, err := finalizer.Finalize()
	if err != nil {
		t.Fatal(err)
	}
	if handoff.Usage.Outcome != gatewayusage.OutcomeFailed || handoff.Usage.FailureAttribution != gatewayusage.FailureAttributionDownstreamClosed || handoff.Audit.DTO.Terminal.Outcome != gatewayaudit.OutcomeDownstreamClosed || handoff.Audit.DTO.Terminal.Success {
		t.Fatalf("cancellation handoff = %#v", handoff)
	}
}

func TestFinalizeRequiresObservedTerminalAndQueueErrorsStayVisible(t *testing.T) {
	observer := gatewayhttpcompletion.New(context.Background())
	capture, err := gatewayaudit.NewCapture(16 * 1024)
	if err != nil {
		t.Fatal(err)
	}
	finalizer, err := New(Input{
		Observer: observer,
		Request:  gatewayusage.RequestFacts{TraceID: "trace-3", TrafficSource: gatewayusage.TrafficSourceGateway, SystemAccountID: "sys-1", Endpoint: "/v1/responses", StartedAt: time.Now().Add(-time.Second)},
		Response: gatewayresponse.Handoff{Usage: gatewayusage.TerminalFacts{Outcome: gatewayusage.OutcomeSucceeded}, Audit: gatewayaudit.TerminalInput{Success: true}},
		Capture:  capture,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := finalizer.Finalize(); !errors.Is(err, ErrTerminalRequired) {
		t.Fatalf("Finalize() error = %v", err)
	}
	if err := finalizer.Enqueue(context.Background(), nil); !errors.Is(err, ErrSideEffectQueueNeeded) {
		t.Fatalf("Enqueue(nil) error = %v", err)
	}
	observer.Complete()
	queue := &queueStub{err: errors.New("queue unavailable")}
	if err := finalizer.Enqueue(context.Background(), queue); !errors.Is(err, queue.err) {
		t.Fatalf("Enqueue() error = %v", err)
	}
	if queue.calls != 1 {
		t.Fatalf("queue calls = %d", queue.calls)
	}
}

func TestCompleteResponseRequiresCallerOwnedCompletionEvidence(t *testing.T) {
	observer := gatewayhttpcompletion.New(context.Background())
	capture, err := gatewayaudit.NewCapture(16 * 1024)
	if err != nil {
		t.Fatal(err)
	}
	finalizer, err := New(Input{
		Observer: observer,
		Request:  gatewayusage.RequestFacts{TraceID: "trace-5", TrafficSource: gatewayusage.TrafficSourceGateway, SystemAccountID: "sys-1", Endpoint: "/v1/responses", StartedAt: time.Now().Add(-time.Second)},
		Response: gatewayresponse.Handoff{
			Usage: gatewayusage.TerminalFacts{Outcome: gatewayusage.OutcomeSucceeded, StatusCode: intPtr(200)},
			Audit: gatewayaudit.TerminalInput{Success: true, RequestedOutcome: gatewayaudit.OutcomeSuccess},
		},
		Capture: capture,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := finalizer.CompleteResponse(); !errors.Is(err, ErrResponseCompleterNeeded) {
		t.Fatalf("CompleteResponse() error = %v", err)
	}
}

func TestCompleteResponseUsesCallerOwnedCompleterBeforeFinalization(t *testing.T) {
	observer := gatewayhttpcompletion.New(context.Background())
	capture, err := gatewayaudit.NewCapture(16 * 1024)
	if err != nil {
		t.Fatal(err)
	}
	completer := completerStub{complete: func() error { observer.Complete(); return nil }}
	finalizer, err := New(Input{
		Observer:  observer,
		Completer: &completer,
		Request:   gatewayusage.RequestFacts{TraceID: "trace-6", TrafficSource: gatewayusage.TrafficSourceGateway, SystemAccountID: "sys-1", Endpoint: "/v1/responses", StartedAt: time.Now().Add(-time.Second)},
		Response: gatewayresponse.Handoff{
			Usage: gatewayusage.TerminalFacts{Outcome: gatewayusage.OutcomeSucceeded, StatusCode: intPtr(200)},
			Audit: gatewayaudit.TerminalInput{Success: true, RequestedOutcome: gatewayaudit.OutcomeSuccess},
		},
		Capture: capture,
	})
	if err != nil {
		t.Fatal(err)
	}
	handoff, err := finalizer.CompleteResponse()
	if err != nil || completer.calls != 1 || handoff.Usage.Outcome != gatewayusage.OutcomeSucceeded {
		t.Fatalf("handoff/error/calls = %#v/%v/%d", handoff, err, completer.calls)
	}
}

func TestTerminalReleaseRunsEvenWhenFinalizationFactsAreInvalid(t *testing.T) {
	observer := gatewayhttpcompletion.New(context.Background())
	capture, err := gatewayaudit.NewCapture(16 * 1024)
	if err != nil {
		t.Fatal(err)
	}
	var releases atomic.Int32
	finalizer, err := New(Input{
		Observer: observer,
		Request:  gatewayusage.RequestFacts{TraceID: "trace-4", TrafficSource: gatewayusage.TrafficSourceGateway, Endpoint: "/v1/responses"},
		Capture:  capture,
		Release:  func() { releases.Add(1) },
	})
	if err != nil {
		t.Fatal(err)
	}
	observer.Complete()
	if _, err := finalizer.Finalize(); err == nil {
		t.Fatal("Finalize() error = nil, want invalid request facts")
	}
	if releases.Load() != 1 {
		t.Fatalf("release count = %d", releases.Load())
	}
}

type queueStub struct {
	err   error
	calls int
}

type completerStub struct {
	complete func() error
	calls    int
}

func (c *completerStub) CompleteResponse() error {
	c.calls++
	return c.complete()
}

func (q *queueStub) Enqueue(_ context.Context, _ Handoff) error {
	q.calls++
	return q.err
}

func intPtr(value int) *int { return &value }

func int64Ptr(value int64) *int64 { return &value }
