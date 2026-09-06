package mockupstream

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// post sends a POST request the way a real OpenAI-compatible caller does:
// the scenario sentinel header selects the script and body carries the
// protocol fields. Method correctness is part of the mock contract, so the
// success baseline must not use method-agnostic helpers (BUG-0160).
func post(t *testing.T, server *httptest.Server, path, scenario, body string) (int, http.Header, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, server.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer test-key")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Mock-Scenario", scenario)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, string(raw)
}

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

func lastRequest(t *testing.T, m *Server) Request {
	t.Helper()
	requests := m.Requests()
	if len(requests) == 0 {
		t.Fatal("no requests recorded")
	}
	return requests[len(requests)-1]
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
		code, _, body := post(t, m.Server, "/v1/chat/completions", string(tc.scenario), `{"model":"gpt-mock"}`)
		if code != tc.wantCode {
			t.Fatalf("scenario %s: status = %d, want %d", tc.scenario, code, tc.wantCode)
		}
		if !strings.Contains(body, tc.wantSub) {
			t.Fatalf("scenario %s: body missing %q", tc.scenario, tc.wantSub)
		}
	}
}

// TestStreamGolden asserts the full Node-equivalent stream contract: the
// Node two-chunk shape (role+content together in the first chunk,
// finish_reason+usage in the terminal chunk), exact event count, the
// concatenated body, the stream response headers, and the [DONE] sentinel.
func TestStreamGolden(t *testing.T) {
	m := New()
	defer m.Close()

	// body stream:true only (no query, no Accept header): the JSON body
	// flag must be enough to enter SSE, like the Node mock.
	code, header, body := post(t, m.Server, "/v1/chat/completions", string(ScenarioChatStream), `{"model":"gpt-mock","stream":true}`)
	if code != 200 {
		t.Fatalf("stream status = %d", code)
	}
	if !strings.Contains(header.Get("Content-Type"), "text/event-stream") {
		t.Fatal("stream content type missing")
	}
	if got := header.Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("Cache-Control = %q, want the Node baseline %q", got, "no-cache")
	}

	events := strings.Count(body, "data: {")
	if events != 2 {
		t.Fatalf("stream event count = %d, want the Node shape of 2 data events", events)
	}
	first := `data: {"id":"chatcmpl-mock-stream","object":"chat.completion.chunk","created":1700000000,"model":"gpt-mock","choices":[{"index":0,"delta":{"role":"assistant","content":"MOCK-STREAM"},"finish_reason":null}]}`
	terminal := `"delta":{},"finish_reason":"stop"`
	for _, want := range []string{first, terminal, `"usage":{"prompt_tokens":8,"completion_tokens":2,"total_tokens":10}`, "data: [DONE]"} {
		if !strings.Contains(body, want) {
			t.Fatalf("stream body missing %q", want)
		}
	}
	if !strings.HasPrefix(body, first+"\n\n") {
		t.Fatal("first chunk must carry role and content together, before any other data event")
	}
}

// TestBodyStreamFlagPriority locks the Node precedence: an explicit body
// stream boolean wins; the query/Accept signals only apply when the body
// field is absent.
func TestBodyStreamFlagPriority(t *testing.T) {
	m := New()
	defer m.Close()

	cases := []struct {
		name      string
		path      string
		accept    string
		body      string
		wantSSE   bool
	}{
		{"body true only", "/v1/chat/completions", "", `{"stream":true}`, true},
		{"body false beats query", "/v1/chat/completions?stream=true", "", `{"stream":false}`, false},
		{"body false beats accept", "/v1/chat/completions", "text/event-stream", `{"stream":false}`, false},
		{"missing body falls back to query", "/v1/chat/completions?stream=true", "", `{}`, true},
		{"missing body falls back to accept", "/v1/chat/completions", "text/event-stream", `{}`, true},
		{"no signal is json", "/v1/chat/completions", "", `{}`, false},
	}
	for _, tc := range cases {
		req, _ := http.NewRequest(http.MethodPost, m.Server.URL+tc.path, strings.NewReader(tc.body))
		req.Header.Set("X-Mock-Scenario", string(ScenarioChatOK))
		if tc.accept != "" {
			req.Header.Set("Accept", tc.accept)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		isSSE := strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream")
		if isSSE != tc.wantSSE {
			t.Fatalf("%s: SSE = %v, want %v (content-type %q)", tc.name, isSSE, tc.wantSSE, resp.Header.Get("Content-Type"))
		}
	}
}

// TestMethodAndPathWhitelist proves the mock enforces the exact Node
// method+path contract: wrong methods and wrong path spellings must 404 so
// method/path drift in the code under test cannot pass as success.
func TestMethodAndPathWhitelist(t *testing.T) {
	m := New()
	defer m.Close()

	bad := []struct {
		method, path string
	}{
		{http.MethodGet, "/v1/chat/completions"},
		{http.MethodGet, "/v1/responses"},
		{http.MethodGet, "/v1/embeddings"},
		{http.MethodPost, "/v1/models"},
		{http.MethodPut, "/v1/chat/completions"},
		{http.MethodPost, "/evil/chat/completions"},
		{http.MethodPost, "/v1/chat/completions/extra"},
		{http.MethodPost, "/chat/completions"},
		{http.MethodPost, "/v2/chat/completions"},
		{http.MethodPost, "/proxy/v1/models"},
		{http.MethodPost, "/v1/models/list"},
	}
	for _, tc := range bad {
		req, _ := http.NewRequest(tc.method, m.Server.URL+tc.path, strings.NewReader(`{}`))
		req.Header.Set("X-Mock-Scenario", string(ScenarioChatOK))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("%s %s: status = %d, want 404", tc.method, tc.path, resp.StatusCode)
		}
	}

	// Sanity: the whitelisted pairs still succeed.
	if code, _, _ := post(t, m.Server, "/v1/chat/completions", string(ScenarioChatOK), `{}`); code != 200 {
		t.Fatalf("chat completions POST: %d", code)
	}
	if code, _, _ := post(t, m.Server, "/v1/responses", string(ScenarioChatOK), `{}`); code != 200 {
		t.Fatalf("responses POST: %d", code)
	}
	if code, _, _ := get(t, m.Server, "/v1/models", string(ScenarioChatOK)); code != 200 {
		t.Fatalf("models GET: %d", code)
	}
}

// TestResponsesEndpoint covers the Responses protocol face required by the
// migration plan §5.1: non-stream JSON, the SSE event timeline with
// event/data framing ending at response.completed (no [DONE]), and the
// tool_call output shape.
func TestResponsesEndpoint(t *testing.T) {
	m := New()
	defer m.Close()

	code, header, body := post(t, m.Server, "/v1/responses", string(ScenarioChatOK), `{"model":"gpt-mock","stream":false}`)
	if code != 200 {
		t.Fatalf("responses non-stream status = %d", code)
	}
	if !strings.Contains(header.Get("Content-Type"), "application/json") {
		t.Fatalf("responses non-stream content-type = %q", header.Get("Content-Type"))
	}
	for _, want := range []string{`"object":"response"`, `"status":"completed"`, `"type":"output_text"`, `"text":"MOCK-OK reply"`, `"input_tokens":1`} {
		if !strings.Contains(body, want) {
			t.Fatalf("responses json body missing %q", want)
		}
	}

	code, header, body = post(t, m.Server, "/v1/responses", string(ScenarioChatStream), `{"model":"gpt-mock","stream":true}`)
	if code != 200 {
		t.Fatalf("responses stream status = %d", code)
	}
	if !strings.Contains(header.Get("Content-Type"), "text/event-stream") {
		t.Fatal("responses stream content type missing")
	}
	for _, want := range []string{
		"event: response.output_text.delta\ndata: ",
		`"type":"response.output_text.delta","delta":"MOCK-OK reply"`,
		"event: response.completed\ndata: ",
		`"type":"response.completed"`,
		`"total_tokens":12`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("responses sse body missing %q", want)
		}
	}
	if strings.Contains(body, "[DONE]") {
		t.Fatal("responses sse must end at response.completed without a [DONE] sentinel, like Node")
	}

	code, _, body = post(t, m.Server, "/v1/responses", string(ScenarioToolCall), `{"model":"gpt-mock","stream":true}`)
	if code != 200 {
		t.Fatalf("responses tool stream status = %d", code)
	}
	for _, want := range []string{"event: response.output_item.added", "event: response.output_item.done", `"type":"function_call"`, `"name":"get_weather"`, "event: response.completed"} {
		if !strings.Contains(body, want) {
			t.Fatalf("responses tool stream missing %q", want)
		}
	}
}

func TestFaultScenarios(t *testing.T) {
	m := New()
	defer m.Close()
	m.SetSlowFirstByteDelay(50 * time.Millisecond)

	code, _, body := post(t, m.Server, "/v1/chat/completions", string(ScenarioMalformedJSON), `{}`)
	if code != 200 || !strings.Contains(body, "chatcmpl-broken") {
		t.Fatalf("malformed json: %d %s", code, body)
	}

	code, _, body = post(t, m.Server, "/v1/chat/completions", string(ScenarioMalformedSSE), `{"stream":true}`)
	if code != 200 || !strings.Contains(body, "{broken json line") {
		t.Fatalf("malformed sse: %d %s", code, body)
	}

	// Mid-stream close: the handler aborts the connection; the client must
	// observe a truncated stream (error or partial bytes, no [DONE]).
	req, _ := http.NewRequest(http.MethodPost, m.Server.URL+"/v1/chat/completions?scenario="+string(ScenarioMidStreamClose), strings.NewReader(`{}`))
	resp, err := http.DefaultClient.Do(req)
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

	// Responses mid-stream close truncates before response.completed.
	req, _ = http.NewRequest(http.MethodPost, m.Server.URL+"/v1/responses?scenario="+string(ScenarioMidStreamClose), strings.NewReader(`{}`))
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("responses mid-stream close transport: %v", err)
	}
	raw, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if strings.Contains(string(raw), `"type":"response.completed"`) {
		t.Fatal("responses mid-stream close must truncate before response.completed")
	}

	// Upstreams that redirect must be surfaced to the caller under test
	// unfollowed: use a no-redirect client (the gateway under test must
	// treat redirects as upstream failures, per the no-follow contract).
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	req, _ = http.NewRequest(http.MethodPost, m.Server.URL+"/v1/chat/completions", strings.NewReader(`{}`))
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

// TestSlowFirstByteSingleDelay proves the delay budget is consumed exactly
// once per request (the Node fixture sets a single timer): the old
// double-sleep bug made streaming requests wait ~2x slowDelay, which must
// fail here. First-byte time is asserted, not just the status code.
func TestSlowFirstByteSingleDelay(t *testing.T) {
	m := New()
	defer m.Close()
	const delay = 200 * time.Millisecond
	m.SetSlowFirstByteDelay(delay)

	measure := func(name, body string) {
		t.Helper()
		start := time.Now()
		req, _ := http.NewRequest(http.MethodPost, m.Server.URL+"/v1/chat/completions?scenario="+string(ScenarioSlowFirstByte), strings.NewReader(body))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if _, err := io.ReadFull(resp.Body, make([]byte, 1)); err != nil {
			t.Fatalf("%s: first byte: %v", name, err)
		}
		elapsed := time.Since(start)
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if elapsed < delay-30*time.Millisecond {
			t.Fatalf("%s: first byte after %v, want >= ~%v (delay budget not consumed)", name, elapsed, delay)
		}
		if elapsed >= 2*delay-40*time.Millisecond {
			t.Fatalf("%s: first byte after %v, want a single %v budget (delay applied twice?)", name, elapsed, delay)
		}
	}

	measure("non-stream", `{}`)
	measure("stream", `{"stream":true}`)

	// Zero delay must not sleep at all.
	m.SetSlowFirstByteDelay(0)
	start := time.Now()
	code, _, _ := post(t, m.Server, "/v1/chat/completions", string(ScenarioSlowFirstByte), `{}`)
	if code != 200 {
		t.Fatalf("zero delay status = %d", code)
	}
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Fatalf("zero delay took %v, want immediate", elapsed)
	}
}

// TestClientCancelAborts covers the migration plan §5.2 client-cancellation
// scenarios: cancelling must cancel pending delays / stop the remaining
// script, record exactly one abort per request, and produce no late
// terminal chunk.
func TestClientCancelAborts(t *testing.T) {
	m := New()
	defer m.Close()

	// Slow-first-byte cancellation: the in-flight request runs on its own
	// goroutine so cancel() can fire while it is pending. The handler must
	// abandon the pending delay, record one abort, and never respond.
	m.SetSlowFirstByteDelay(3 * time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, m.Server.URL+"/v1/chat/completions?scenario="+string(ScenarioSlowFirstByte), strings.NewReader(`{}`))
	start := time.Now()
	doDone := make(chan error, 1)
	go func() {
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
		doDone <- err
	}()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case err := <-doDone:
		if err == nil {
			t.Fatal("canceled slow-first-byte request unexpectedly completed with a response")
		}
	case <-time.After(time.Second):
		t.Fatal("cancel did not unblock the in-flight request")
	}
	deadline := time.Now().Add(2 * time.Second)
	for !lastRequest(t, m).Aborted && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !lastRequest(t, m).Aborted {
		t.Fatal("slow-first-byte cancellation must mark the request aborted")
	}
	if elapsed := time.Since(start); elapsed >= 2*time.Second {
		t.Fatalf("cancel took %v, delay must be abandoned promptly", elapsed)
	}

	// Mid-stream cancellation: a chunk-gap delay (the Node delayed-streaming
	// fixture) gives the handler an observable window; cancelling during the
	// gap must stop the script, record the abort exactly once, and never
	// deliver the late terminal chunk.
	m.SetSlowFirstByteDelay(0)
	m.SetStreamChunkDelay(4 * time.Second)
	before := m.Aborts()
	ctx2, cancel2 := context.WithCancel(context.Background())
	req2, _ := http.NewRequestWithContext(ctx2, http.MethodPost, m.Server.URL+"/v1/chat/completions?scenario="+string(ScenarioChatStream), strings.NewReader(`{}`))
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(resp2.Body)
	if _, err := reader.ReadString('\n'); err != nil {
		t.Fatalf("first chunk read: %v", err)
	}
	cancel2()
	deadline = time.Now().Add(2 * time.Second)
	for m.Aborts() == before && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if m.Aborts() == before {
		t.Fatal("mid-stream cancel was not recorded as an abort")
	}
	resp2.Body.Close()
	if got := m.Aborts() - before; got != 1 {
		t.Fatalf("abort count delta = %d, want exactly 1 per request", got)
	}
}

func TestModelAndEmbeddingsPaths(t *testing.T) {
	m := New()
	defer m.Close()
	code, _, body := get(t, m.Server, "/v1/models", string(ScenarioChatOK))
	if code != 200 || !strings.Contains(body, "gpt-mock") {
		t.Fatalf("models: %d %s", code, body)
	}
	code, _, body = post(t, m.Server, "/v1/embeddings", string(ScenarioChatOK), `{"model":"text-embedding-mock","input":"hi"}`)
	if code != 200 || !strings.Contains(body, "embedding") {
		t.Fatalf("embeddings: %d %s", code, body)
	}
	unknown, _, _ := post(t, m.Server, "/v1/unknown", string(ScenarioChatOK), `{}`)
	if unknown != 404 {
		t.Fatalf("unknown path must 404, got %d", unknown)
	}
}

// TestRequestRecording proves the recorded request keeps the full
// request-side contract: method, path, raw query (including the scenario
// selector), body, auth header, parsed model and body stream flag.
func TestRequestRecording(t *testing.T) {
	m := New()
	defer m.Close()
	post(t, m.Server, "/v1/chat/completions?stream=true&fixture=%20encoded", string(ScenarioChatOK), `{"model":"gpt-mock","stream":true,"messages":[{"role":"user","content":"hi"}]}`)

	req := lastRequest(t, m)
	if req.Method != http.MethodPost {
		t.Fatalf("recorded method = %q", req.Method)
	}
	if req.Path != "/v1/chat/completions" {
		t.Fatalf("recorded path = %q", req.Path)
	}
	if req.RawQuery != "stream=true&fixture=%20encoded" {
		t.Fatalf("recorded raw query = %q", req.RawQuery)
	}
	if !strings.Contains(req.Body, `"messages"`) {
		t.Fatalf("recorded body missing messages: %q", req.Body)
	}
	if req.AuthHeader != "Bearer test-key" {
		t.Fatal("authorization header not recorded")
	}
	if req.Model != "gpt-mock" {
		t.Fatalf("recorded model = %q, want gpt-mock", req.Model)
	}
	if req.StreamField != "true" {
		t.Fatalf("recorded stream field = %q, want \"true\"", req.StreamField)
	}
	if req.Aborted {
		t.Fatal("completed request must not be marked aborted")
	}

	// A request without an explicit body stream flag records an empty
	// StreamField; an explicit false records "false".
	post(t, m.Server, "/v1/chat/completions", string(ScenarioChatOK), `{"model":"gpt-mock"}`)
	if got := lastRequest(t, m).StreamField; got != "" {
		t.Fatalf("absent stream field recorded as %q", got)
	}
	post(t, m.Server, "/v1/chat/completions", string(ScenarioChatOK), `{"model":"gpt-mock","stream":false}`)
	if got := lastRequest(t, m).StreamField; got != "false" {
		t.Fatalf("explicit false stream field recorded as %q", got)
	}
}
