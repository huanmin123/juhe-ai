// Package mockupstream provides the scripted AI upstream simulation used by
// every gateway/probe slice for mock closed-loop verification. Scenarios
// mirror the plan's failure matrix: normal/stream/tool-call responses,
// upstream status errors, transport faults (slow first byte, mid-stream
// disconnect, malformed SSE), and deterministic bodies for golden replay.
// This package is test-only infrastructure: nothing in production binaries
// imports it.
package mockupstream

import (
	"fmt"
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
	now       func() time.Time
	slowDelay time.Duration
	seenReqs  []Request
	mu        sync.Mutex
}

// Request records what the caller under test actually sent upstream.
type Request struct {
	Method      string
	Path        string
	Body        string
	AuthHeader  string
	Model       string
	StreamField string
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

// Requests returns the recorded upstream requests (for golden diff of the
// request-side contract).
func (m *Server) Requests() []Request {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Request(nil), m.seenReqs...)
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

func (m *Server) serve(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	m.seenReqs = append(m.seenReqs, Request{
		Method:     r.Method,
		Path:       r.URL.Path,
		AuthHeader: r.Header.Get("Authorization"),
	})
	m.mu.Unlock()
	m.serve2(w, r)
}

func (m *Server) serve2(w http.ResponseWriter, r *http.Request) {
	scenario := m.scenario(r)
	switch scenario {
	case ScenarioSlowFirstByte:
		time.Sleep(m.slowDelay)
	case ScenarioRedirectAttempt:
		// Upstream must never be followed: respond 302 to a target that
		// would fail closed if followed.
		w.Header().Set("Location", "/definitely-not-allowed")
		w.WriteHeader(http.StatusFound)
		return
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

	if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
		switch {
		case strings.HasSuffix(r.URL.Path, "/models"):
			writeJSONStatus(w, http.StatusOK, modelListBody())
			return
		case strings.HasSuffix(r.URL.Path, "/embeddings"):
			writeJSONStatus(w, http.StatusOK, embeddingsBody())
			return
		}
		writeJSONStatus(w, http.StatusNotFound, `{"error":{"message":"Unknown path","type":"invalid_request_error"}}`)
		return
	}

	stream := r.URL.Query().Get("stream") == "true" || strings.Contains(r.Header.Get("Accept"), "text/event-stream")
	if scenario == ScenarioChatStream || scenario == ScenarioMidStreamClose || scenario == ScenarioMalformedSSE {
		stream = true
	}
	if !stream {
		writeJSONStatus(w, http.StatusOK, chatCompletionBody(scenario))
		return
	}
	serveSSE(w, scenario, m.slowDelay)
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

func modelListBody() string {
	return `{"object":"list","data":[{"id":"gpt-mock","object":"model"},{"id":"gpt-mock-mini","object":"model"}]}`
}

func embeddingsBody() string {
	return `{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.1,0.2,0.3]}],"model":"text-embedding-mock","usage":{"prompt_tokens":1,"total_tokens":1}}`
}

// serveSSE writes the deterministic five-chunk stream. Fault scenarios:
// mid-stream close truncates before the terminal chunk; malformed SSE emits
// a broken line.
func serveSSE(w http.ResponseWriter, scenario Scenario, slowDelay time.Duration) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)

	chunk := func(payload string) {
		fmt.Fprint(w, payload)
		if flusher != nil {
			flusher.Flush()
		}
	}
	first := func() string {
		return `data: {"id":"chatcmpl-mock-stream","object":"chat.completion.chunk","created":1700000000,"model":"gpt-mock","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}` + "\n\n"
	}
	delta := func(text string) string {
		return `data: {"id":"chatcmpl-mock-stream","object":"chat.completion.chunk","created":1700000000,"model":"gpt-mock","choices":[{"index":0,"delta":{"content":"` + text + `"},"finish_reason":null}]}` + "\n\n"
	}
	terminal := func() string {
		return `data: {"id":"chatcmpl-mock-stream","object":"chat.completion.chunk","created":1700000000,"model":"gpt-mock","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\ndata: [DONE]\n\n"
	}

	if scenario == ScenarioMalformedSSE {
		chunk(first())
		chunk("data: {broken json line\n\n")
		return
	}
	if scenario == ScenarioSlowFirstByte && slowDelay > 0 {
		time.Sleep(slowDelay)
	}
	chunk(first())
	chunk(delta("MOCK"))
	chunk(delta("-"))
	if scenario == ScenarioMidStreamClose {
		// Truncate: hold the connection then drop before terminal chunk.
		if flusher != nil {
			flusher.Flush()
		}
		panic(http.ErrAbortHandler)
	}
	chunk(delta("STREAM"))
	chunk(terminal())
}

// FormatRetryAfter formats Retry-After header values consistently.
func FormatRetryAfter(seconds int) string { return strconv.Itoa(seconds) }
