package mockupstream

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func get(t *testing.T, server *httptest.Server, path, scenario string) (int, http.Header, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, server.URL+path, nil)
	req.Header.Set("Authorization", "Bearer test-key")
	req.Header.Set("X-Mock-Scenario", scenario)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, string(raw)
}

func TestChatCompletionScenarios(t *testing.T) {
	m := New()
	defer m.Close()

	cases := []struct {
		scenario Scenario
		wantCode int
		wantSub  string
	}{
		{ScenarioChatOK, 200, `"content":"MOCK-OK reply"`},
		{ScenarioChatOK, 200, `"total_tokens":18`},
		{ScenarioToolCall, 200, `"finish_reason":"tool_calls"`},
		{ScenarioEmptyCompletion, 200, `"content":""`},
		{ScenarioStatus400, 400, "invalid_request_error"},
		{ScenarioStatus401, 401, "Invalid API key"},
		{ScenarioStatus403, 403, "insufficient_quota"},
		{ScenarioStatus429, 429, "rate_limit_error"},
		{ScenarioStatus500, 500, "server_error"},
		{ScenarioStatus502, 502, "bad gateway"},
		{ScenarioStatus504, 504, "gateway timeout"},
	}
	for _, tc := range cases {
		code, _, body := get(t, m.Server, "/v1/chat/completions", string(tc.scenario))
		if code != tc.wantCode {
			t.Fatalf("scenario %s: status = %d, want %d", tc.scenario, code, tc.wantCode)
		}
		if !strings.Contains(body, tc.wantSub) {
			t.Fatalf("scenario %s: body missing %q", tc.scenario, tc.wantSub)
		}
	}
}

func TestStreamScenarioSSE(t *testing.T) {
	m := New()
	defer m.Close()
	code, header, body := get(t, m.Server, "/v1/chat/completions?stream=true", string(ScenarioChatStream))
	if code != 200 {
		t.Fatalf("stream status = %d", code)
	}
	if !strings.Contains(header.Get("Content-Type"), "text/event-stream") {
		t.Fatal("stream content type missing")
	}
	for _, want := range []string{`"delta":{"role":"assistant"}`, `"delta":{"content":"MOCK"}`, `"finish_reason":"stop"`, "data: [DONE]"} {
		if !strings.Contains(body, want) {
			t.Fatalf("stream body missing %q", want)
		}
	}
}

func TestFaultScenarios(t *testing.T) {
	m := New()
	defer m.Close()
	m.SetSlowFirstByteDelay(50 * time.Millisecond)

	code, _, body := get(t, m.Server, "/v1/chat/completions", string(ScenarioMalformedJSON))
	if code != 200 || !strings.Contains(body, "chatcmpl-broken") {
		t.Fatalf("malformed json: %d %s", code, body)
	}

	code, _, body = get(t, m.Server, "/v1/chat/completions?stream=true", string(ScenarioMalformedSSE))
	if code != 200 || !strings.Contains(body, "{broken json line") {
		t.Fatalf("malformed sse: %d %s", code, body)
	}

	// Mid-stream close: the handler aborts the connection; the client must
	// observe a truncated stream (EOF, no [DONE]).
	resp, err := http.Get(m.Server.URL + "/v1/chat/completions?stream=true&scenario=" + string(ScenarioMidStreamClose))
	if err != nil {
		t.Fatalf("mid-stream close transport: %v", err)
	}
	raw, readErr := io.ReadAll(resp.Body)
	resp.Body.Close()
	if strings.Contains(string(raw), "[DONE]") {
		t.Fatal("mid-stream close must truncate before [DONE]")
	}
	if readErr == nil && len(raw) == 0 {
		t.Fatal("mid-stream close must deliver partial bytes or error")
	}

	// Upstreams that redirect must be surfaced to the caller under test
	// unfollowed: use a no-redirect client (the gateway under test must
	// treat redirects as upstream failures, per the no-follow contract).
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	req, _ := http.NewRequest(http.MethodGet, m.Server.URL+"/v1/chat/completions", nil)
	req.Header.Set("X-Mock-Scenario", string(ScenarioRedirectAttempt))
	dupResp, doErr := noRedirect.Do(req)
	if doErr != nil {
		t.Fatal(doErr)
	}
	dupResp.Body.Close()
	if dupResp.StatusCode != http.StatusFound {
		t.Fatalf("redirect attempt must surface 302 to the caller under test, got %d", dupResp.StatusCode)
	}
}

func TestModelAndEmbeddingsPaths(t *testing.T) {
	m := New()
	defer m.Close()
	code, _, body := get(t, m.Server, "/v1/models", string(ScenarioChatOK))
	if code != 200 || !strings.Contains(body, "gpt-mock") {
		t.Fatalf("models: %d %s", code, body)
	}
	code, _, body = get(t, m.Server, "/v1/embeddings", string(ScenarioChatOK))
	if code != 200 || !strings.Contains(body, "embedding") {
		t.Fatalf("embeddings: %d %s", code, body)
	}
	unknown, _, _ := get(t, m.Server, "/v1/unknown", string(ScenarioChatOK))
	if unknown != 404 {
		t.Fatalf("unknown path must 404, got %d", unknown)
	}
}

func TestRequestRecording(t *testing.T) {
	m := New()
	defer m.Close()
	get(t, m.Server, "/v1/chat/completions", string(ScenarioChatOK))
	requests := m.Requests()
	if len(requests) == 0 {
		t.Fatal("no requests recorded")
	}
	if requests[len(requests)-1].AuthHeader != "Bearer test-key" {
		t.Fatal("authorization header not recorded")
	}
}
