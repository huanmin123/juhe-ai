package exactkeyprobe

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type w1ResponsesSSETransport func(*http.Request) (*http.Response, error)

func (transport w1ResponsesSSETransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

type w1ResponsesSSEBody struct {
	reader *strings.Reader
	tail   error
	closed bool
}

func (body *w1ResponsesSSEBody) Read(buffer []byte) (int, error) {
	n, err := body.reader.Read(buffer)
	if err == io.EOF && body.tail != nil {
		return n, body.tail
	}
	return n, err
}

func (body *w1ResponsesSSEBody) Close() error {
	body.closed = true
	return nil
}

// 实际 F4 入口回放 A/B/C 三种 Responses 请求。上游失败即使夹在有效
// challenge/completed 前后，或同时发生读尾错误，也不能被成功证据掩盖。
func TestW1ResponsesSSEFailureEvidenceThroughProbe(t *testing.T) {
	const delta = "data: {\"type\":\"response.output_text.delta\",\"delta\":\"juhe\"}\n\n"
	const completed = "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"error\":null}}\n\n"
	const errorEvent = "event: error\ndata: {\"type\":\"error\",\"message\":\"quota exceeded 原文\"}\n\n"
	const failedEvent = "data: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":{\"message\":\"model failed 原文\"}}}\n\n"
	tailErr := errors.New("read tail sentinel")
	cases := []struct {
		name    string
		body    string
		tail    error
		limit   int64
		outcome string
		code    string
		message string
	}{
		{name: "completed", body: delta + completed, outcome: OutcomeSuccess},
		{name: "completed_without_status_legacy", body: delta + "event: response.completed\ndata: {}\n\n", outcome: OutcomeSuccess},
		{name: "completed_read_tail", body: delta + completed, tail: tailErr, outcome: OutcomeSuccess},
		{name: "error_before_completed", body: delta + errorEvent + completed, outcome: OutcomeNeutral, code: "upstream_protocol_invalid", message: "quota exceeded 原文"},
		{name: "error_after_completed", body: delta + completed + errorEvent, outcome: OutcomeNeutral, code: "upstream_protocol_invalid", message: "quota exceeded 原文"},
		{name: "error_after_completed_read_tail", body: delta + completed + errorEvent, tail: tailErr, outcome: OutcomeNeutral, code: "upstream_protocol_invalid", message: "quota exceeded 原文"},
		{name: "error_after_malformed_data_read_tail", body: "data: not-json\n\n" + delta + completed + errorEvent, tail: tailErr, outcome: OutcomeNeutral, code: "upstream_protocol_invalid", message: "quota exceeded 原文"},
		{name: "malformed_data_cannot_be_hidden_by_completion", body: "data: not-json\n\n" + delta + completed, outcome: OutcomeNeutral, code: "upstream_protocol_invalid", message: "上游响应未满足探活语义"},
		{name: "null_completed_frame", body: delta + "event: response.completed\ndata: null\n\n", outcome: OutcomeNeutral, code: "upstream_protocol_invalid", message: "上游响应未满足探活语义"},
		{name: "null_completed_frame_read_tail", body: delta + "event: response.completed\ndata: null\n\n", tail: tailErr, outcome: OutcomeUpstreamFailed, code: "upstream_read_incomplete", message: "上游响应读取未完成"},
		{name: "failed_before_completed", body: delta + failedEvent + completed, outcome: OutcomeNeutral, code: "upstream_protocol_invalid", message: "model failed 原文"},
		{name: "failed_after_completed", body: delta + completed + failedEvent, outcome: OutcomeNeutral, code: "upstream_protocol_invalid", message: "model failed 原文"},
		{name: "failed_completed_status", body: delta + "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"failed\"}}\n\n", outcome: OutcomeNeutral, code: "upstream_protocol_invalid", message: "SSE 上游未完成"},
		{name: "incomplete_completed_status", body: delta + "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"incomplete\"}}\n\n", outcome: OutcomeNeutral, code: "upstream_protocol_invalid", message: "SSE 上游未完成"},
		{name: "in_progress_completed_status", body: delta + "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"in_progress\"}}\n\n", outcome: OutcomeNeutral, code: "upstream_protocol_invalid", message: "SSE 上游未完成"},
		{name: "nested_error_despite_completed_status", body: delta + "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"error\":{\"message\":\"  nested error 原文  \"}}}\n\n", outcome: OutcomeNeutral, code: "upstream_protocol_invalid", message: "  nested error 原文  "},
		{name: "top_error_despite_completed", body: delta + completed + "data: {\"error\":{\"message\":\"top error 原文\"}}\n\n", outcome: OutcomeNeutral, code: "upstream_protocol_invalid", message: "top error 原文"},
		{name: "string_error", body: delta + completed + "data: {\"error\":\"string error 原文\"}\n\n", outcome: OutcomeNeutral, code: "upstream_protocol_invalid", message: "string error 原文"},
		{name: "failed_header_cannot_be_overridden_by_type", body: delta + completed + "event: response.failed\ndata: {\"type\":\"response.completed\"}\n\n", outcome: OutcomeNeutral, code: "upstream_protocol_invalid", message: "SSE 上游未完成"},
		{name: "failed_event_without_data", body: delta + completed + "event: response.failed\n\n", outcome: OutcomeNeutral, code: "upstream_protocol_invalid", message: "SSE 上游未完成"},
		{name: "failed_event_with_done", body: delta + completed + "event: error\ndata: [DONE]\n\n", outcome: OutcomeNeutral, code: "upstream_protocol_invalid", message: "SSE 上游未完成"},
		{name: "incomplete_event", body: delta + "data: {\"type\":\"response.incomplete\"}\n\n" + completed, outcome: OutcomeNeutral, code: "upstream_protocol_invalid", message: "SSE 上游未完成"},
		{name: "nested_failure_without_failure_event", body: delta + completed + "data: {\"type\":\"response.updated\",\"response\":{\"status\":\"failed\"}}\n\n", outcome: OutcomeNeutral, code: "upstream_protocol_invalid", message: "SSE 上游未完成"},
		{name: "top_failure_status", body: delta + "data: {\"type\":\"response.completed\",\"status\":\"failed\"}\n\n", outcome: OutcomeNeutral, code: "upstream_protocol_invalid", message: "SSE 上游未完成"},
		{name: "unknown_error_object", body: delta + completed + "data: {\"error\":{\"code\":\"server_error\"}}\n\n", outcome: OutcomeNeutral, code: "upstream_protocol_invalid", message: "SSE 上游未完成"},
		{name: "delta_without_completion", body: delta, outcome: OutcomeNeutral, code: "upstream_protocol_invalid", message: "上游响应未满足探活语义"},
		{name: "delta_read_tail", body: delta, tail: tailErr, outcome: OutcomeUpstreamFailed, code: "upstream_read_incomplete", message: "上游响应读取未完成"},
		{name: "completion_without_challenge", body: completed, outcome: OutcomeNeutral, code: "upstream_protocol_invalid", message: "上游响应未满足探活语义"},
		{name: "completion_body_limit", body: delta + completed + strings.Repeat(" ", 20), limit: int64(len(delta + completed)), outcome: OutcomeUpstreamFailed, code: "upstream_read_incomplete", message: "上游响应读取未完成"},
	}
	for _, shape := range []string{"A", "B", "C"} {
		for _, test := range cases {
			t.Run(shape+"/"+test.name, func(t *testing.T) {
				input := testInput("https://f4-probe.invalid", "responses_sse")
				input.ProtocolProfileID = "profile_openai_openai_v1"
				input.ProtocolCode = "openai"
				credentialJSON, credentialKind := `{"api_key":"w1-mock-key"}`, "api_key"
				if shape == "A" {
					input.Type = "oauth"
					input.ProtocolProfileID = "profile_gpt_openai_v1"
					expires := time.Now().Add(time.Hour)
					input.OAuthExpiresAt = &expires
					credentialJSON, credentialKind = `{"access_token":"w1-mock-token"}`, "oauth_access"
				} else if shape == "B" {
					input.ClientCompatibility = "codex_responses"
				}
				body := &w1ResponsesSSEBody{reader: strings.NewReader(test.body), tail: test.tail}
				calls := 0
				client := &http.Client{Transport: w1ResponsesSSETransport(func(request *http.Request) (*http.Response, error) {
					calls++
					if request.Method != http.MethodPost || !strings.HasSuffix(request.URL.Path, "/responses") {
						t.Fatalf("unexpected actual request: %s %s", request.Method, request.URL.Path)
					}
					return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: body, Request: request}, nil
				})}
				const secret = "w1-f4-sse-secret"
				credential := CredentialEnvelope{Kind: credentialKind, Ciphertext: testEnvelope(t, secret, credentialJSON)}
				result := ProbeOpenAI(context.Background(), input, credential, ProbeOptions{Client: client, Secret: secret, MaxResponseBytes: test.limit})
				if calls != 1 || !body.closed {
					t.Fatalf("actual HTTP dependency: calls=%d closed=%t", calls, body.closed)
				}
				if result.Outcome != test.outcome || result.ErrorCode != test.code || result.ErrorMessage != test.message || result.StatusCode != http.StatusOK && test.outcome != OutcomeUpstreamFailed {
					t.Fatalf("result=%#v, want outcome=%s code=%s message=%q", result, test.outcome, test.code, test.message)
				}
			})
		}
	}
}
