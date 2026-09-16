package gatewayingress

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestW9FHandlerRejectsNilRequest drives the defensive nil-request branch of
// ServeHTTP, which the happy-path tests never reach.
func TestW9FHandlerRejectsNilRequest(t *testing.T) {
	recorder := httptest.NewRecorder()
	Handler{Dispatcher: dispatcherStub{dispatch: func(context.Context, *http.Request) (Response, error) {
		t.Fatal("dispatcher must not be called for a nil request")
		return Response{}, nil
	}}}.ServeHTTP(recorder, nil)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
	if !strings.Contains(recorder.Body.String(), "gateway_ingress_invalid_request") {
		t.Fatalf("body = %q, want invalid-request error", recorder.Body.String())
	}
}

// TestW9FRelayBodyNoProgress covers the zero-read guard in relayBody: a body
// that returns (0, nil) forever must abort instead of spinning.
func TestW9FRelayBodyNoProgress(t *testing.T) {
	recorder := httptest.NewRecorder()
	var gotOutcome Outcome
	handler := Handler{Dispatcher: dispatcherStub{dispatch: func(context.Context, *http.Request) (Response, error) {
		return Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(zeroReader{}),
			Finish: func(_ context.Context, outcome Outcome) error {
				gotOutcome = outcome
				return nil
			},
		}, nil
	}}}

	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "http://gateway.test/v1/models", nil))

	if gotOutcome != OutcomeAborted {
		t.Fatalf("finish outcome = %q, want %q", gotOutcome, OutcomeAborted)
	}
}

// TestW9FCopyHeadersKeepsNormalHeaders pins the non-hop-by-hop branch of
// isHopByHopHeader through the relay path.
func TestW9FCopyHeadersKeepsNormalHeaders(t *testing.T) {
	recorder := httptest.NewRecorder()
	handler := Handler{Dispatcher: dispatcherStub{dispatch: func(context.Context, *http.Request) (Response, error) {
		return Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"X-Test": {"a", "b"}, "Upgrade": {"websocket"}},
			Body:       io.NopCloser(strings.NewReader("ok")),
		}, nil
	}}}

	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "http://gateway.test/v1/models", nil))

	if got := recorder.Header().Values("X-Test"); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("X-Test = %v, want [a b]", got)
	}
	if got := recorder.Header().Get("Upgrade"); got != "" {
		t.Fatalf("Upgrade = %q, want dropped", got)
	}
}

type zeroReader struct{}

func (zeroReader) Read([]byte) (int, error) { return 0, nil }
