package openaicompat

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestComputerExecutorGating(t *testing.T) {
	tests := []struct {
		name   string
		config Config
		want   bool
	}{
		{
			name:   "default guidance mode disabled",
			config: Config{ComputerAdapter: ComputerAdapterConfig{Enabled: true, Endpoint: "http://x"}},
			want:   false,
		},
		{
			name:   "local runtime without adapter disabled",
			config: Config{HostedToolComputerMode: "local_runtime"},
			want:   false,
		},
		{
			name:   "local runtime with enabled adapter and endpoint",
			config: Config{HostedToolComputerMode: "local_runtime", ComputerAdapter: ComputerAdapterConfig{Enabled: true, Endpoint: "http://127.0.0.1:9/v1"}},
			want:   true,
		},
		{
			name:   "enabled adapter without endpoint disabled",
			config: Config{HostedToolComputerMode: "local_runtime", ComputerAdapter: ComputerAdapterConfig{Enabled: true}},
			want:   false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ComputerExecutorForRequest(tc.config, nil, nil) != nil; got != tc.want {
				t.Fatalf("executor = %v, want %v", got, tc.want)
			}
		})
	}
	// Test override wins over config gating.
	override := ComputerExecutorForRequest(Config{}, nil, fakeComputerExecutor{})
	if override == nil {
		t.Fatal("override must short-circuit gating")
	}
}

type fakeComputerExecutor struct{}

func (fakeComputerExecutor) Run(context.Context, ComputerRuntimeInput) (*ComputerRuntimeResult, error) {
	return &ComputerRuntimeResult{Message: "fake"}, nil
}

func TestComputerAdapterRunSuccess(t *testing.T) {
	transport := &mockTransport{handler: func(request *http.Request) (*http.Response, error) {
		if request.Header.Get("accept") != "application/json" || request.Header.Get("content-type") != "application/json" {
			t.Fatalf("headers = %v", request.Header)
		}
		raw, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatal(err)
		}
		body := decodeJSON(t, string(raw))
		if body["stream"] != true {
			t.Fatalf("body = %v", body)
		}
		if body["tool"] == nil || body["body"] == nil {
			t.Fatalf("body = %v", body)
		}
		return jsonResponse(200, `{"message":"clicked","call":{"call_id":"call_1","status":"done","actions":[{"type":"click"},{"ignore":1}],"metadata":{"browser":"chromium"}},"metadata":{"ms":12}}`), nil
	}}
	executor := ComputerExecutorForRequest(Config{
		HostedToolComputerMode: "local_runtime",
		ComputerAdapter:        ComputerAdapterConfig{Enabled: true, Endpoint: "http://adapter"},
	}, transport, nil)
	result, err := executor.Run(context.Background(), ComputerRuntimeInput{
		Body: map[string]any{"screenshot": "b64"}, Tool: map[string]any{"type": "computer_20250124"}, Stream: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Message != "clicked" {
		t.Fatalf("message = %s", result.Message)
	}
	if result.Call == nil || result.Call.CallID != "call_1" || result.Call.Status != "done" {
		t.Fatalf("call = %+v", result.Call)
	}
	// arrayRecordValue keeps every JSON object (Node filter(isRecord)).
	if len(result.Call.Actions) != 2 || result.Call.Actions[0]["type"] != "click" {
		t.Fatalf("actions = %+v", result.Call.Actions)
	}
	if result.Metadata["adapter"] != "http_browser" || result.Metadata["ms"] != "12" && result.Metadata["ms"] != float64(12) {
		t.Fatalf("metadata = %v", result.Metadata)
	}
}

func TestComputerAdapterCallIdFieldFallback(t *testing.T) {
	transport := &mockTransport{handler: func(request *http.Request) (*http.Response, error) {
		return jsonResponse(200, `{"call":{"callId":"camel"},"metadata":{}}`), nil
	}}
	executor := ComputerExecutorForRequest(Config{
		HostedToolComputerMode: "local_runtime",
		ComputerAdapter:        ComputerAdapterConfig{Enabled: true, Endpoint: "http://adapter"},
	}, transport, nil)
	result, err := executor.Run(context.Background(), ComputerRuntimeInput{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Call.CallID != "camel" {
		t.Fatalf("call = %+v", result.Call)
	}
	if result.Message != "Computer browser adapter completed." {
		t.Fatalf("default message = %s", result.Message)
	}
}

func TestComputerAdapterErrors(t *testing.T) {
	tests := []struct {
		name    string
		handler func(request *http.Request) (*http.Response, error)
		wantErr string
	}{
		{
			name: "http error surfaces status and snippet",
			handler: func(request *http.Request) (*http.Response, error) {
				return jsonResponse(500, strings.Repeat("boom", 40)), nil
			},
			wantErr: "Computer browser adapter HTTP 500: boomboom",
		},
		{
			name: "invalid json",
			handler: func(request *http.Request) (*http.Response, error) {
				return jsonResponse(200, "not-json"), nil
			},
			wantErr: "Computer browser adapter response is not valid JSON",
		},
		{
			name: "non-object json",
			handler: func(request *http.Request) (*http.Response, error) {
				return jsonResponse(200, "[1]"), nil
			},
			wantErr: "Computer browser adapter response must be a JSON object",
		},
		{
			name: "body over limit",
			handler: func(request *http.Request) (*http.Response, error) {
				return jsonResponse(200, strings.Repeat("x", 4096)), nil
			},
			wantErr: "Computer browser adapter response body exceeded limit",
		},
		{
			name: "transport failure with timeout",
			handler: func(request *http.Request) (*http.Response, error) {
				return nil, errors.New("dial fail")
			},
			wantErr: "dial fail",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			executor := ComputerExecutorForRequest(Config{
				HostedToolComputerMode: "local_runtime",
				ComputerAdapter:        ComputerAdapterConfig{Enabled: true, Endpoint: "http://adapter", MaxBodyBytes: 1024},
			}, &mockTransport{handler: tc.handler}, nil)
			_, err := executor.Run(context.Background(), ComputerRuntimeInput{})
			if err == nil || !strings.HasPrefix(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want prefix %q", err, tc.wantErr)
			}
		})
	}
}

func TestComputerAdapterTimeout(t *testing.T) {
	transport := &mockTransport{handler: func(request *http.Request) (*http.Response, error) {
		select {
		case <-request.Context().Done():
			return nil, request.Context().Err()
		case <-time.After(200 * time.Millisecond):
			return jsonResponse(200, `{}`), nil
		}
	}}
	executor := ComputerExecutorForRequest(Config{
		HostedToolComputerMode: "local_runtime",
		ComputerAdapter:        ComputerAdapterConfig{Enabled: true, Endpoint: "http://adapter", TimeoutMs: 20},
	}, transport, nil)
	_, err := executor.Run(context.Background(), ComputerRuntimeInput{})
	if err == nil || err.Error() != "Computer browser adapter request timed out" {
		t.Fatalf("error = %v", err)
	}
}

func TestComputerAdapterCallerCancel(t *testing.T) {
	transport := &mockTransport{handler: func(request *http.Request) (*http.Response, error) {
		// Honor the request context like a real transport.
		select {
		case <-request.Context().Done():
			return nil, request.Context().Err()
		case <-time.After(200 * time.Millisecond):
			return jsonResponse(200, `{}`), nil
		}
	}}
	executor := ComputerExecutorForRequest(Config{
		HostedToolComputerMode: "local_runtime",
		ComputerAdapter:        ComputerAdapterConfig{Enabled: true, Endpoint: "http://adapter", TimeoutMs: 30000},
	}, transport, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := executor.Run(ctx, ComputerRuntimeInput{})
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
}
