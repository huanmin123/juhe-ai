package modelcheckprobe

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckprofile"
)

func TestExecuteNormalizesProtocolBasePathsAndPreservesOnlyEvidence(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Errorf("path=%s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("missing auth header")
		}
		if r.Header.Get("Accept") != "application/json" {
			t.Errorf("accept=%s", r.Header.Get("Accept"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp","model":"gpt-5.6-sol","output_text":"OK-MODEL-CHECK","usage":{"input_tokens":2}}`))
	}))
	defer server.Close()
	request, err := BuildBasic(modelcheckprofile.ProtocolOpenAIResponses, "gpt-5.6-sol", "hello", BasicOptions{MaxOutputTokens: 32})
	if err != nil {
		t.Fatal(err)
	}
	result, err := Execute(context.Background(), request, TransportOptions{Endpoint: server.URL + "/v1", Headers: http.Header{"Authorization": []string{"Bearer secret"}}})
	if err != nil || !result.Success {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if result.UpstreamModel != "gpt-5.6-sol" || result.Response.OutputText != "OK-MODEL-CHECK" || result.TraceID == "" || result.UpstreamStatusCode == nil || *result.UpstreamStatusCode != 200 {
		t.Fatalf("result=%#v", result)
	}
	if strings.Contains(string(result.Response.JSON["id"].(string)), "secret") {
		t.Fatal("credential leaked into response evidence")
	}
}

func TestExecuteSSEAndGeminiVersionPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1beta/models/gemini-3.5-flash:streamGenerateContent" {
			t.Errorf("path=%s", r.URL.Path)
		}
		if r.URL.RawQuery != "alt=sse" {
			t.Errorf("query=%s", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"modelVersion\":\"gemini-3.5-flash\",\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"STREAM-OK\"}]}}]}\n\n"))
	}))
	defer server.Close()
	request, err := BuildBasic(modelcheckprofile.ProtocolGeminiNative, "gemini-3.5-flash", "hello", BasicOptions{MaxOutputTokens: 32, Stream: true})
	if err != nil {
		t.Fatal(err)
	}
	result, err := Execute(context.Background(), request, TransportOptions{Endpoint: server.URL + "/v1beta"})
	if err != nil || !result.Success || result.Response.OutputText != "STREAM-OK" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestExecuteRejectsUnsafeEndpointAndBoundsResponse(t *testing.T) {
	if _, err := Execute(context.Background(), Request{Path: "/v1/responses", Protocol: modelcheckprofile.ProtocolOpenAIResponses}, TransportOptions{Endpoint: "https://user:pass@example.com/v1"}); err == nil {
		t.Fatal("userinfo endpoint accepted")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(strings.Repeat("x", 128))) }))
	defer server.Close()
	request, _ := BuildBasic(modelcheckprofile.ProtocolOpenAIResponses, "gpt-5.6-sol", "hello", BasicOptions{MaxOutputTokens: 32})
	result, err := Execute(context.Background(), request, TransportOptions{Endpoint: server.URL, MaxResponseBytes: 32})
	if err != nil || !result.ResponseTruncated || result.Success {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestExecuteHonorsCancellationAndTimeoutWithoutRawError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(100 * time.Millisecond):
		}
	}))
	defer server.Close()
	request, _ := BuildBasic(modelcheckprofile.ProtocolOpenAIResponses, "gpt-5.6-sol", "hello", BasicOptions{MaxOutputTokens: 32})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := Execute(ctx, request, TransportOptions{Endpoint: server.URL})
	if err != nil || !strings.Contains(result.Response.ErrorMessage, "已取消") {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if errors.Is(ctx.Err(), context.Canceled) == false {
		t.Fatal("test context was not canceled")
	}
	short, err := Execute(context.Background(), request, TransportOptions{Endpoint: server.URL, Timeout: time.Millisecond})
	if err != nil || !strings.Contains(short.Response.ErrorMessage, "超时") {
		t.Fatalf("timeout result=%#v err=%v", short, err)
	}
}
