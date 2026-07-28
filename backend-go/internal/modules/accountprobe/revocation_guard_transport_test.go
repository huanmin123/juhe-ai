package accountprobe

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptrace"
	"testing"

	"juhe-ai/backend-go/internal/platform/upstreamtransport"
)

func TestRevocationGuardTransportRunsFenceInsideGuardAndReleasesAtWrite(t *testing.T) {
	sequence := make([]string, 0, 4)
	guard := &revocationProtectorStub{sequence: &sequence}
	next := &revocationTransportStub{sequence: &sequence, result: upstreamtransport.Result{Attempted: true, FramingComplete: true}}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "https://example.com/v1/responses", nil)
	if err != nil {
		t.Fatal(err)
	}

	transport := RevocationGuardTransport{Next: next, Guard: guard}
	result, err := transport.ExecuteWithFence(t.Context(), request, func(context.Context) error {
		sequence = append(sequence, "reload")
		return nil
	})
	if err != nil {
		t.Fatalf("ExecuteWithFence() error = %v", err)
	}
	if !result.Attempted || !result.FramingComplete {
		t.Fatalf("result = %+v", result)
	}
	want := []string{"guard", "reload", "send", "wrote"}
	if len(sequence) != len(want) {
		t.Fatalf("sequence = %v, want %v", sequence, want)
	}
	for index := range want {
		if sequence[index] != want[index] {
			t.Fatalf("sequence = %v, want %v", sequence, want)
		}
	}
	if next.fenceWasSet {
		t.Fatal("wrapped transport received the final reload fence a second time")
	}
	transport.CloseIdleConnections()
	if !next.closed {
		t.Fatal("wrapped transport idle connections were not closed")
	}
}

func TestRevocationGuardTransportRejectsBeforeAttemptWhenReloadFails(t *testing.T) {
	reloadErr := errors.New("revoked")
	next := &revocationTransportStub{}
	request, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, "https://example.com/v1/responses", nil)
	_, err := (RevocationGuardTransport{Next: next, Guard: &revocationProtectorStub{}}).ExecuteWithFence(
		t.Context(), request, func(context.Context) error { return reloadErr },
	)
	if !errors.Is(err, reloadErr) {
		t.Fatalf("ExecuteWithFence() error = %v", err)
	}
	if next.calls != 0 {
		t.Fatalf("wrapped transport calls = %d, want 0", next.calls)
	}
}

type revocationProtectorStub struct{ sequence *[]string }

func (p *revocationProtectorStub) ProtectExternal(ctx context.Context, reload func(context.Context) error, send func(context.Context) error) error {
	if p.sequence != nil {
		*p.sequence = append(*p.sequence, "guard")
	}
	if err := reload(ctx); err != nil {
		return err
	}
	trace := &httptrace.ClientTrace{WroteRequest: func(info httptrace.WroteRequestInfo) {
		if p.sequence != nil {
			*p.sequence = append(*p.sequence, "wrote")
		}
	}}
	return send(httptrace.WithClientTrace(ctx, trace))
}

type revocationTransportStub struct {
	sequence    *[]string
	result      upstreamtransport.Result
	err         error
	calls       int
	fenceWasSet bool
	closed      bool
}

func (t *revocationTransportStub) CloseIdleConnections() { t.closed = true }

func (t *revocationTransportStub) ExecuteWithFence(ctx context.Context, _ *http.Request, fence func(context.Context) error) (upstreamtransport.Result, error) {
	t.calls++
	t.fenceWasSet = fence != nil
	if t.sequence != nil {
		*t.sequence = append(*t.sequence, "send")
	}
	if trace := httptrace.ContextClientTrace(ctx); trace != nil && trace.WroteRequest != nil {
		trace.WroteRequest(httptrace.WroteRequestInfo{})
	}
	return t.result, t.err
}
