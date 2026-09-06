// Package mockupstream provides the scripted AI upstream simulation used by
// every gateway/probe slice for mock closed-loop verification. Scenarios
// mirror the plan's failure matrix: normal/stream/tool-call responses,
// upstream status errors, transport faults (slow first byte, mid-stream
// disconnect, malformed SSE), and deterministic bodies for golden replay.
//
// The wire contract mirrors the archived Node regression mocks
// (migration-backup/node/final-archive/backend/src/scripts/regression/
// chat-gateway-mock-ai-regression.ts and
// gateway-quality-priority-real-sample-mock-ai-regression.ts):
//
//   - exact method+path whitelist (POST /v1/chat/completions,
//     /v1/responses, /v1/embeddings; GET /v1/models); any other
//     method+path pair is a Node-equivalent 404;
//   - the JSON body "stream" boolean decides SSE vs JSON (the Node
//     fixtures read body.stream after parsing the request body); the
//     legacy ?stream=true query / Accept: text/event-stream signals only
//     apply when the body field is absent;
//   - Chat SSE uses the Node two-chunk shape: the first chunk carries
//     delta.role and the full delta.content together, the terminal chunk
//     carries finish_reason "stop" plus usage, followed by "data: [DONE]";
//   - /v1/responses speaks Responses JSON and the Responses SSE timeline
//     ("event:"/"data:" frames ending at response.completed, no [DONE]);
//   - the slow-first-byte delay budget is consumed exactly once per
//     request, matching the Node single-timer fixture;
//   - client disconnects cancel pending delays and stop the remaining
//     script; each request records at most one abort.
//
// Recorded requests (Requests()) keep the full request triple (method,
// path + raw query, body) plus the parsed model and body stream flag so
// golden diffs can prove the request-side contract. This package is
// test-only infrastructure: nothing in production binaries imports it.
package mockupstream

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Scenario names the upstream behavior under test.
type Scenario string

const (
	ScenarioChatOK          Scenario = "chat_ok"
	ScenarioChatStream      Scenario = "chat_stream"
	ScenarioToolCall        Scenario = "tool_call"
	ScenarioStatus400       Scenario = "status_400"
	ScenarioStatus401       Scenario = "status_401"
	ScenarioStatus403       Scenario = "status_403"
	ScenarioStatus429       Scenario = "status_429"
	ScenarioStatus500       Scenario = "status_500"
	ScenarioStatus502       Scenario = "status_502"
	ScenarioStatus504       Scenario = "status_504"
	ScenarioMalformedJSON   Scenario = "malformed_json"
	ScenarioSlowFirstByte   Scenario = "slow_first_byte"
	ScenarioMidStreamClose  Scenario = "mid_stream_close"
	ScenarioMalformedSSE    Scenario = "malformed_sse"
	ScenarioRedirectAttempt Scenario = "redirect_attempt"
	ScenarioEmptyCompletion Scenario = "empty_completion"
)

// Server is the scripted OpenAI-compatible upstream.
type Server struct {
	*httptest.Server
	now        func() time.Time
	slowDelay  time.Duration
	chunkDelay time.Duration
	seenReqs   []Request
	aborts     int
	mu         sync.Mutex
}

// Request records what the caller under test actually sent upstream.
// Model and StreamField are parsed from the JSON body (StreamField is
// "true"/"false" when the body carried an explicit stream boolean, ""
// otherwise); RawQuery keeps the full query string so the recorded
// request-target can be reconstructed.
type Request struct {
	Method      string
	Path        string
	RawQuery    string
	Body        string
	AuthHeader  string
	Model       string
	StreamField string
	Aborted     bool
}

// endpoint is one exact method+path pair accepted by the mock, mirroring
// the archived Node mocks: chat/responses/embeddings are POST-only and
// the model list is GET-only (account-api-key-pool-test-mock fixture).
type endpoint struct {
	method string
	path   string
}

var acceptedEndpoints = map[endpoint]bool{
	{http.MethodPost, "/v1/chat/completions"}: true,
	{http.MethodPost, "/v1/responses"}:        true,
	{http.MethodPost, "/v1/embeddings"}:       true,
	{http.MethodGet, "/v1/models"}:            true,
}

// New starts the mock upstream; every path is served deterministically per
// the requested scenario (sentinel "x-mock-scenario" header or ?scenario=
// query, defaulting to chat_ok).
func New() *Server {
	m := &Server{now: time.Now}
	m.Server = httptest.NewServer(http.HandlerFunc(m.serve))
	return m
}

// Close shuts the server down.
func (m *Server) Close() { m.Server.Close() }

// SetSlowFirstByteDelay configures the slow-first-byte fault delay.
func (m *Server) SetSlowFirstByteDelay(d time.Duration) { m.slowDelay = d }

// SetStreamChunkDelay configures a pause between stream events, mirroring
// the archived Node delayed-streaming fixture. A client disconnect during
// the gap cancels the pause, records one abort, and drops the remaining
// events.
func (m *Server) SetStreamChunkDelay(d time.Duration) { m.chunkDelay = d }

// Requests returns the recorded upstream requests (for golden diff of the
// request-side contract).
func (m *Server) Requests() []Request {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Request(nil), m.seenReqs...)
}

// Aborts returns how many recorded requests observed a client disconnect
// (context cancellation or write failure). At most one abort is recorded
// per request, mirroring the Node aborted-count fixtures.
func (m *Server) Aborts() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.aborts
}

func (m *Server) scenario(r *http.Request) Scenario {
	value := r.Header.Get("X-Mock-Scenario")
	if value == "" {
		value = r.URL.Query().Get("scenario")
	}
	if value == "" {
		value = string(ScenarioChatOK)
	}
	return Scenario(value)
}

// noteAbort records one abort for the request at index idx (idempotent).
func (m *Server) noteAbort(idx int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if idx < 0 || idx >= len(m.seenReqs) || m.seenReqs[idx].Aborted {
		return
	}
	m.seenReqs[idx].Aborted = true
	m.aborts++
}

// sleep waits for d; it returns false as soon as the client context is
// done, recording the abort and letting the caller stop the script. The
// slow-first-byte budget is consumed here exactly once per request.
func (m *Server) sleep(ctx context.Context, d time.Duration, idx int) bool {
	if d <= 0 {
		return true
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		m.noteAbort(idx)
		return false
	case <-timer.C:
		return true
	}
}

// writeChunk writes one SSE frame and reports whether the client is still
// listening. A done context or a write error records one abort and tells
// the caller to stop emitting further script events.
func (m *Server) writeChunk(w http.ResponseWriter, r *http.Request, idx int, payload string) bool {
	if r.Context().Err() != nil {
		m.noteAbort(idx)
		return false
	}
	if _, err := io.WriteString(w, payload); err != nil {
		m.noteAbort(idx)
		return false
	}
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	return true
}

func (m *Server) serve(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	_ = r.Body.Close()
	r.Body = io.NopCloser(bytes.NewReader(raw))

	// Parse the fields that decide protocol and routing semantics so the
	// recorded request proves the request-side contract (Node records the
	// parsed body the same way).
	var parsed struct {
		Model  string `json:"model"`
		Stream *bool  `json:"stream"`
	}
	_ = json.Unmarshal(raw, &parsed)
	streamField := ""
	if parsed.Stream != nil {
		streamField = strconv.FormatBool(*parsed.Stream)
	}

	m.mu.Lock()
	m.seenReqs = append(m.seenReqs, Request{
		Method:      r.Method,
		Path:        r.URL.Path,
		RawQuery:    r.URL.RawQuery,
		Body:        string(raw),
		AuthHeader:  r.Header.Get("Authorization"),
		Model:       parsed.Model,
		StreamField: streamField,
	})
	idx := len(m.seenReqs) - 1
	m.mu.Unlock()

	m.serve2(w, r, idx, parsed.Stream)
}

func (m *Server) serve2(w http.ResponseWriter, r *http.Request, idx int, bodyStream *bool) {
	// Exact method+path whitelist first, like the Node entry check:
	// everything else is a 404 regardless of scenario.
	if !acceptedEndpoints[endpoint{r.Method, r.URL.Path}] {
		writeJSONStatus(w, http.StatusNotFound, `{"error":{"message":"Unknown path","type":"invalid_request_error"}}`)
		return
	}

	scenario := m.scenario(r)
	switch scenario {
	case ScenarioRedirectAttempt:
		// Upstream must never be followed: respond 302 to a target that
		// would fail closed if followed.
		w.Header().Set("Location", "/definitely-not-allowed")
		w.WriteHeader(http.StatusFound)
		return
	case ScenarioSlowFirstByte:
		// Single delay budget per request (the Node fixture sets exactly
		// one timer); aborted clients stop here without any response.
		if !m.sleep(r.Context(), m.slowDelay, idx) {
			return
		}
	}

	switch scenario {
	case ScenarioStatus400:
		writeJSONStatus(w, http.StatusBadRequest, `{"error":{"message":"Invalid parameters","type":"invalid_request_error","code":"invalid_parameter"}}`)
		return
	case ScenarioStatus401:
		writeJSONStatus(w, http.StatusUnauthorized, `{"error":{"message":"Invalid API key","type":"invalid_request_error","code":"invalid_api_key"}}`)
		return
	case ScenarioStatus403:
		writeJSONStatus(w, http.StatusForbidden, `{"error":{"message":"Permission denied","type":"insufficient_quota"}}`)
		return
	case ScenarioStatus429:
		w.Header().Set("Retry-After", "30")
		writeJSONStatus(w, http.StatusTooManyRequests, `{"error":{"message":"Rate limit reached","type":"rate_limit_error"}}`)
		return
	case ScenarioStatus500:
		writeJSONStatus(w, http.StatusInternalServerError, `{"error":{"message":"Internal server error","type":"server_error"}}`)
		return
	case ScenarioStatus502:
		writeJSONStatus(w, http.StatusBadGateway, `bad gateway`)
		return
	case ScenarioStatus504:
		writeJSONStatus(w, http.StatusGatewayTimeout, `gateway timeout`)
		return
	case ScenarioMalformedJSON:
		writeJSONStatus(w, http.StatusOK, `{"id":"chatcmpl-broken", "choices": [`)
		return
	}

	switch r.URL.Path {
	case "/v1/models":
		writeJSONStatus(w, http.StatusOK, modelListBody())
		return
	case "/v1/embeddings":
		writeJSONStatus(w, http.StatusOK, embeddingsBody())
		return
	}

	stream := streamRequested(bodyStream, r)
	if scenario == ScenarioChatStream || scenario == ScenarioMidStreamClose || scenario == ScenarioMalformedSSE {
		stream = true
	}
	if r.URL.Path == "/v1/responses" {
		if stream {
			m.serveResponsesSSE(w, r, idx, scenario)
		} else {
			writeJSONStatus(w, http.StatusOK, responsesBody(scenario))
		}
		return
	}
	if !stream {
		writeJSONStatus(w, http.StatusOK, chatCompletionBody(scenario))
		return
	}
	m.serveChatSSE(w, r, idx, scenario)
}

// streamRequested resolves the effective stream mode with the Node
// precedence: an explicit body "stream" boolean wins; only when the field
// is absent do the legacy query/Accept signals apply.
func streamRequested(bodyStream *bool, r *http.Request) bool {
	if bodyStream != nil {
		return *bodyStream
	}
	return r.URL.Query().Get("stream") == "true" || strings.Contains(r.Header.Get("Accept"), "text/event-stream")
}

func writeJSONStatus(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

func chatCompletionBody(scenario Scenario) string {
	if scenario == ScenarioEmptyCompletion {
		return `{"id":"chatcmpl-mock-empty","object":"chat.completion","created":1700000000,"model":"gpt-mock","choices":[{"index":0,"message":{"role":"assistant","content":""},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":0,"total_tokens":1}}`
	}
	if scenario == ScenarioToolCall {
		return `{"id":"chatcmpl-mock-tool","object":"chat.completion","created":1700000000,"model":"gpt-mock","choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_mock_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"hangzhou\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`
	}
	return `{"id":"chatcmpl-mock-ok","object":"chat.completion","created":1700000000,"model":"gpt-mock","choices":[{"index":0,"message":{"role":"assistant","content":"MOCK-OK reply"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":8,"total_tokens":18}}`
}

// responsesBody mirrors the archived Node non-stream Responses fixture:
// object "response", a message output with output_text parts, and
// input/output/total token usage.
func responsesBody(scenario Scenario) string {
	if scenario == ScenarioToolCall {
		return `{"id":"resp-mock-tool","object":"response","status":"completed","model":"gpt-mock","output":[{"type":"function_call","id":"fc_mock_1","call_id":"call_mock_1","name":"get_weather","arguments":"{\"city\":\"hangzhou\"}","status":"completed"}],"usage":{"input_tokens":18,"output_tokens":8,"total_tokens":26}}`
	}
	if scenario == ScenarioEmptyCompletion {
		return `{"id":"resp-mock-empty","object":"response","status":"completed","model":"gpt-mock","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":""}]}],"usage":{"input_tokens":1,"output_tokens":0,"total_tokens":1}}`
	}
	return `{"id":"resp-mock-ok","object":"response","status":"completed","model":"gpt-mock","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"MOCK-OK reply"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`
}

func modelListBody() string {
	return `{"object":"list","data":[{"id":"gpt-mock","object":"model"},{"id":"gpt-mock-mini","object":"model"}]}`
}

func embeddingsBody() string {
	return `{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.1,0.2,0.3]}],"model":"text-embedding-mock","usage":{"prompt_tokens":1,"total_tokens":1}}`
}

func sseHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
}

// serveChatSSE writes the deterministic Chat Completions stream in the
// archived Node shape: one first chunk carrying delta.role together with
// the full delta.content, one terminal chunk carrying finish_reason "stop"
// plus usage, then the "data: [DONE]" sentinel. Fault scenarios:
// mid-stream close truncates before the terminal chunk; malformed SSE
// emits a broken line.
func (m *Server) serveChatSSE(w http.ResponseWriter, r *http.Request, idx int, scenario Scenario) {
	sseHeaders(w)

	first := `data: {"id":"chatcmpl-mock-stream","object":"chat.completion.chunk","created":1700000000,"model":"gpt-mock","choices":[{"index":0,"delta":{"role":"assistant","content":"MOCK-STREAM"},"finish_reason":null}]}` + "\n\n"
	terminal := `data: {"id":"chatcmpl-mock-stream","object":"chat.completion.chunk","created":1700000000,"model":"gpt-mock","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":8,"completion_tokens":2,"total_tokens":10}}` + "\n\ndata: [DONE]\n\n"

	if scenario == ScenarioMalformedSSE {
		m.writeChunk(w, r, idx, first)
		m.writeChunk(w, r, idx, "data: {broken json line\n\n")
		return
	}
	if !m.writeChunk(w, r, idx, first) {
		return
	}
	if scenario == ScenarioMidStreamClose {
		// Truncate: drop the connection before the terminal chunk, like an
		// upstream TCP reset mid-stream.
		panic(http.ErrAbortHandler)
	}
	if !m.sleep(r.Context(), m.chunkDelay, idx) {
		return // client gone mid-stream: stop, terminal chunk is never sent
	}
	m.writeChunk(w, r, idx, terminal)
}

// serveResponsesSSE writes the Responses SSE timeline using the Node
// "event:"/"data:" framing: an output_text delta then response.completed
// with usage (tool_call scenarios emit the output_item.added/done pair
// first). Responses streams end at response.completed without a [DONE]
// sentinel, exactly like the archived Node mock.
func (m *Server) serveResponsesSSE(w http.ResponseWriter, r *http.Request, idx int, scenario Scenario) {
	sseHeaders(w)

	event := func(name, data string) bool {
		return m.writeChunk(w, r, idx, "event: "+name+"\ndata: "+data+"\n\n")
	}
	delta := `{"type":"response.output_text.delta","delta":"MOCK-OK reply"}`
	completed := `{"type":"response.completed","response":{"id":"resp-mock-stream","status":"completed","usage":{"input_tokens":8,"output_tokens":4,"total_tokens":12}}}`
	functionCall := `{"type":"function_call","id":"fc_mock_1","call_id":"call_mock_1","name":"get_weather","arguments":"{\"city\":\"hangzhou\"}","status":"completed"}`

	if scenario == ScenarioMalformedSSE {
		event("response.output_text.delta", delta)
		m.writeChunk(w, r, idx, "data: {broken json line\n\n")
		return
	}
	if scenario == ScenarioToolCall {
		inProgress := `{"type":"function_call","id":"fc_mock_1","call_id":"call_mock_1","name":"get_weather","arguments":"","status":"in_progress"}`
		if !event("response.output_item.added", `{"type":"response.output_item.added","output_index":0,"item":`+inProgress+`}`) {
			return
		}
		if !event("response.output_item.done", `{"type":"response.output_item.done","output_index":0,"item":`+functionCall+`}`) {
			return
		}
		event("response.completed", `{"type":"response.completed","response":{"id":"resp-mock-tool","status":"completed","output":[`+functionCall+`],"usage":{"input_tokens":18,"output_tokens":8,"total_tokens":26}}}`)
		return
	}
	if !event("response.output_text.delta", delta) {
		return
	}
	if scenario == ScenarioMidStreamClose {
		// Truncate before response.completed, like an upstream TCP reset.
		panic(http.ErrAbortHandler)
	}
	if !m.sleep(r.Context(), m.chunkDelay, idx) {
		return // client gone mid-stream: response.completed is never sent
	}
	event("response.completed", completed)
}

// FormatRetryAfter formats Retry-After header values consistently.
func FormatRetryAfter(seconds int) string { return strconv.Itoa(seconds) }
