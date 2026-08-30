package gatewayingress

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type dispatcherStub struct {
	dispatch func(context.Context, *http.Request) (Response, error)
}

func (s dispatcherStub) Dispatch(ctx context.Context, request *http.Request) (Response, error) {
	return s.dispatch(ctx, request)
}

func TestHandlerRelaysStreamingBodiesAndCompletes(t *testing.T) {
	var gotBody string
	var gotContext context.Context
	var gotOutcome Outcome
	handler := Handler{Dispatcher: dispatcherStub{dispatch: func(ctx context.Context, request *http.Request) (Response, error) {
		gotContext = ctx
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		gotBody = string(body)
		return Response{
			StatusCode: http.StatusCreated,
			Header:     http.Header{"Content-Type": {"text/event-stream"}, "Connection": {"close"}},
			Body:       io.NopCloser(strings.NewReader("data: first\n\ndata: second\n\n")),
			Finish: func(_ context.Context, outcome Outcome) error {
				gotOutcome = outcome
				return nil
			},
		}, nil
	}}}

	request := httptest.NewRequest(http.MethodPost, "http://gateway.test/v1/responses", strings.NewReader("stream-request"))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusCreated)
	}
	if gotContext != request.Context() {
		t.Fatal("dispatcher did not receive the request context")
	}
	if gotBody != "stream-request" {
		t.Fatalf("dispatcher body = %q", gotBody)
	}
	if body := recorder.Body.String(); body != "data: first\n\ndata: second\n\n" {
		t.Fatalf("relayed body = %q", body)
	}
	if !recorder.Flushed {
		t.Fatal("streaming response was not flushed")
	}
	if got := recorder.Header().Get("Connection"); got != "" {
		t.Fatalf("hop-by-hop Connection header = %q", got)
	}
	if gotOutcome != OutcomeComplete {
		t.Fatalf("finish outcome = %q", gotOutcome)
	}
}

func TestHandlerReturnsDeterministicCancellationError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	handler := Handler{Dispatcher: dispatcherStub{dispatch: func(got context.Context, _ *http.Request) (Response, error) {
		if !errors.Is(got.Err(), context.Canceled) {
			t.Fatalf("dispatcher context error = %v, want canceled", got.Err())
		}
		return Response{}, got.Err()
	}}}
	request := httptest.NewRequest(http.MethodGet, "http://gateway.test/v1/models", nil).WithContext(ctx)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Code != 499 {
		t.Fatalf("status = %d, want 499", recorder.Code)
	}
	if got := recorder.Body.String(); got != "{\"error\":{\"code\":\"gateway_request_cancelled\",\"message\":\"gateway request was cancelled\"}}\n" {
		t.Fatalf("body = %q", got)
	}
}

func TestHandlerMarksInterruptedResponseAsAborted(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var gotOutcome Outcome
	handler := Handler{Dispatcher: dispatcherStub{dispatch: func(context.Context, *http.Request) (Response, error) {
		return Response{
			StatusCode: http.StatusOK,
			Body: io.NopCloser(errorReader{read: func([]byte) (int, error) {
				cancel()
				return 0, context.Canceled
			}}),
			Finish: func(got context.Context, outcome Outcome) error {
				if !errors.Is(got.Err(), context.Canceled) {
					t.Fatalf("finish context error = %v, want canceled", got.Err())
				}
				gotOutcome = outcome
				return nil
			},
		}, nil
	}}}
	request := httptest.NewRequest(http.MethodGet, "http://gateway.test/v1/chat/completions", nil).WithContext(ctx)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if gotOutcome != OutcomeAborted {
		t.Fatalf("finish outcome = %q, want %q", gotOutcome, OutcomeAborted)
	}
}

func TestHandlerDoesNotExposeUpstreamError(t *testing.T) {
	handler := Handler{Dispatcher: dispatcherStub{dispatch: func(context.Context, *http.Request) (Response, error) {
		return Response{}, errors.New("credential=secret upstream exploded")
	}}}
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "http://gateway.test/v1/models", nil))

	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadGateway)
	}
	if got := recorder.Body.String(); strings.Contains(got, "secret") || strings.Contains(got, "exploded") {
		t.Fatalf("upstream error leaked: %q", got)
	}
}

func TestHandlerSettlesMissingResponseBodyAsAborted(t *testing.T) {
	var calls int
	var gotOutcome Outcome
	handler := Handler{Dispatcher: dispatcherStub{dispatch: func(context.Context, *http.Request) (Response, error) {
		return Response{Finish: func(_ context.Context, outcome Outcome) error {
			calls++
			gotOutcome = outcome
			return nil
		}}, nil
	}}}
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "http://gateway.test/v1/models", nil))

	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadGateway)
	}
	if calls != 1 {
		t.Fatalf("finish calls = %d, want 1", calls)
	}
	if gotOutcome != OutcomeAborted {
		t.Fatalf("finish outcome = %q, want %q", gotOutcome, OutcomeAborted)
	}
}

func TestHandlerRejectsMissingDispatcher(t *testing.T) {
	recorder := httptest.NewRecorder()
	Handler{}.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "http://gateway.test/v1/models", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusServiceUnavailable)
	}
}

type errorReader struct {
	read func([]byte) (int, error)
}

func (r errorReader) Read(buffer []byte) (int, error) { return r.read(buffer) }
