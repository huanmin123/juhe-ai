package modelcheckprobe

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckprofile"
)

func TestExecuteWithRetryOnlyRetriesHTTPFailures(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			http.Error(w, "temporary", http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(`{"model":"gpt-5.6-sol","output_text":"OK-MODEL-CHECK"}`))
	}))
	defer server.Close()
	request, _ := BuildBasic(modelcheckprofile.ProtocolOpenAIResponses, "gpt-5.6-sol", "hello", BasicOptions{MaxOutputTokens: 32})
	result, err := ExecuteWithRetry(context.Background(), request, TransportOptions{Endpoint: server.URL}, RetryOptions{AttemptTimeouts: []time.Duration{time.Second, time.Second}, Delay: func(context.Context) error { return nil }})
	if err != nil || !result.Success || calls.Load() != 2 || result.RetryAttemptCount != 1 || len(result.AttemptStatusCodes) != 2 || result.AttemptStatusCodes[0] != 502 || result.AttemptStatusCodes[1] != 200 {
		t.Fatalf("result=%#v calls=%d err=%v", result, calls.Load(), err)
	}
}

func TestExecuteWithRetryDoesNotRetryHTTP200QualityFailure(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{"model":"wrong","output_text":"bad"}`))
	}))
	defer server.Close()
	request, _ := BuildBasic(modelcheckprofile.ProtocolOpenAIResponses, "gpt-5.6-sol", "hello", BasicOptions{MaxOutputTokens: 32})
	result, err := ExecuteWithRetry(context.Background(), request, TransportOptions{Endpoint: server.URL}, RetryOptions{AttemptTimeouts: []time.Duration{time.Second, time.Second}, Delay: func(context.Context) error { return nil }})
	item := EvaluateBasicProtocol(result, "gpt-5.6-sol", ProtocolEvaluationOptions{ItemKey: "target.responses_basic", ItemType: "responses_basic", SuccessMessage: "ok", FailurePrefix: "failed"})
	if err != nil || item.Status != "failed" || calls.Load() != 1 || result.RetryAttemptCount != 0 {
		t.Fatalf("result=%#v calls=%d err=%v", result, calls.Load(), err)
	}
}
