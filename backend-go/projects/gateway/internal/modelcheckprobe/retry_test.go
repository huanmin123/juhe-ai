package modelcheckprobe

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckprofile"
)

func TestExecuteWithRetryRetriesNonOKThenStopsAtOK(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"gpt-5.6-terra","output_text":"OK-MODEL-CHECK","usage":{"total_tokens":2}}`))
	}))
	defer server.Close()
	request, err := BuildBasic(modelcheckprofile.ProtocolOpenAIResponses, "gpt-5.6-terra", "Reply with exactly: OK-MODEL-CHECK", false)
	if err != nil {
		t.Fatal(err)
	}
	result, err := ExecuteWithRetry(context.Background(), request, Options{Endpoint: server.URL}, RetryOptions{
		AttemptTimeouts: []time.Duration{time.Second, time.Second, time.Second},
		Delay:           func(context.Context) error { return nil },
	})
	if err != nil || !result.Success || calls.Load() != 3 {
		t.Fatalf("result=%+v calls=%d err=%v", result, calls.Load(), err)
	}
	if result.RetryAttemptCount != 2 || result.RetryMaxAttempts != 3 || len(result.AttemptStatusCodes) != 3 {
		t.Fatalf("retry evidence=%+v", result)
	}
}

func TestRetryOptionsForProfileUsesFiveAttemptBudget(t *testing.T) {
	for _, profile := range []string{"quick", "full", "unknown"} {
		options := RetryOptionsForProfile(profile)
		if len(options.AttemptTimeouts) != 5 {
			t.Fatalf("profile=%q attempts=%d want=5", profile, len(options.AttemptTimeouts))
		}
	}
}

func TestExecuteWithRetryDoesNotTrustAuthenticationStatusAsTerminal(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) < 5 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"gpt-5.6-terra","output_text":"OK-MODEL-CHECK"}`))
	}))
	defer server.Close()
	request, err := BuildBasic(modelcheckprofile.ProtocolOpenAIResponses, "gpt-5.6-terra", "Reply with exactly: OK-MODEL-CHECK", false)
	if err != nil {
		t.Fatal(err)
	}
	result, err := ExecuteWithRetry(context.Background(), request, Options{Endpoint: server.URL}, RetryOptions{
		AttemptTimeouts: []time.Duration{time.Second, time.Second, time.Second, time.Second, time.Second},
		Delay:           func(context.Context) error { return nil },
	})
	if err != nil || !result.Success || calls.Load() != 5 || result.RetryAttemptCount != 4 || result.RetryMaxAttempts != 5 {
		t.Fatalf("result=%+v calls=%d err=%v", result, calls.Load(), err)
	}
	if len(result.AttemptDetails) != 5 || len(result.RetryWaitDurations) != 4 {
		t.Fatalf("attempt evidence result=%+v", result)
	}
}

func TestEvaluateBasicNonOKIsExcluded(t *testing.T) {
	item := EvaluateBasic(Result{HTTPStatus: http.StatusTooManyRequests, ErrorMessage: "J3b upstream returned HTTP 429", RetryAttemptCount: 2, RetryMaxAttempts: 3, AttemptStatusCodes: []int{429, 429, 429}}, "gpt-5.6-terra")
	if item.Status != "skipped" || item.MaxScore != 0 || item.Evidence["requestFailure"] != true || item.Evidence["excludedFromScoring"] != true {
		t.Fatalf("item=%+v", item)
	}
}

func TestModelMatchesRequiresExactOrDateSuffixedIdentity(t *testing.T) {
	for _, test := range []struct {
		name, actual string
		want         bool
	}{
		{name: "exact", actual: "gpt-5.6-sol", want: true},
		{name: "date suffix", actual: "gpt-5.6-sol-2026-08-31", want: true},
		{name: "date suffix punctuation", actual: "gpt-5.6-sol-2026-08-31.preview", want: true},
		{name: "missing", actual: "", want: false},
		{name: "arbitrary suffix", actual: "gpt-5.6-sol-preview", want: false},
		{name: "invalid date", actual: "gpt-5.6-sol-2026-8-31", want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := modelMatches(test.actual, "gpt-5.6-sol"); got != test.want {
				t.Fatalf("modelMatches(%q)=%v, want %v", test.actual, got, test.want)
			}
		})
	}
}
