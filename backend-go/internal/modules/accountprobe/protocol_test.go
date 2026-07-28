package accountprobe

import (
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func TestBuildRequestCoversEveryEndpointMode(t *testing.T) {
	tests := []struct {
		mode       EndpointMode
		path       string
		stream     bool
		bodyChecks map[string]any
	}{
		{ModeChatJSON, "/v1/chat/completions", false, map[string]any{"max_tokens": json.Number("1")}},
		{ModeChatSSE, "/v1/chat/completions", true, map[string]any{"max_tokens": json.Number("1")}},
		{ModeResponsesJSON, "/v1/responses", false, map[string]any{"instructions": "You are ChatGPT, a helpful assistant."}},
		{ModeResponsesSSE, "/v1/responses", true, map[string]any{"instructions": "You are ChatGPT, a helpful assistant."}},
		{ModeMessagesJSON, "/v1/messages", false, map[string]any{"max_tokens": json.Number("32000")}},
		{ModeMessagesSSE, "/v1/messages", true, map[string]any{"max_tokens": json.Number("32000")}},
		{ModeGenerateContentJSON, "/v1beta/models/vendor%2Fmodel:generateContent", false, nil},
		{ModeGenerateContentSSE, "/v1beta/models/vendor%2Fmodel:streamGenerateContent?alt=sse", false, nil},
		{ModeInteractionsJSON, "/v1beta/interactions", false, nil},
		{ModeInteractionsSSE, "/v1beta/interactions", true, nil},
	}
	for _, test := range tests {
		t.Run(string(test.mode), func(t *testing.T) {
			spec, err := BuildRequest(RequestInput{
				Mode: test.mode, Model: "MODELS/vendor/model", Prompt: "ping",
				SessionID: "session-1", Today: "2026-07-28", WorkingDirectory: `F:\work`,
			})
			if err != nil {
				t.Fatalf("BuildRequest() error = %v", err)
			}
			if spec.Method != http.MethodPost || spec.PathAndQuery != test.path || spec.Mode != test.mode || spec.Model != "MODELS/vendor/model" {
				t.Fatalf("spec = %+v", spec)
			}
			if spec.Header.Get("Content-Type") != "application/json" {
				t.Fatalf("content type = %q", spec.Header.Get("Content-Type"))
			}
			body := decodeTestObject(t, spec.Body)
			if body["model"] != "MODELS/vendor/model" && !strings.HasPrefix(string(test.mode), "generate_content") {
				t.Fatalf("model = %#v", body["model"])
			}
			if got, ok := body["stream"].(bool); ok && got != test.stream {
				t.Fatalf("stream = %v, want %v", got, test.stream)
			}
			for key, want := range test.bodyChecks {
				if !reflect.DeepEqual(body[key], want) {
					t.Fatalf("body[%q] = %#v, want %#v", key, body[key], want)
				}
			}
			if test.mode == ModeInteractionsSSE && spec.Header.Get("Accept") != "text/event-stream" {
				t.Fatalf("accept = %q", spec.Header.Get("Accept"))
			}
		})
	}
}

func TestBuildRequestMatchesOAuthAndCodexResponsesContract(t *testing.T) {
	spec, err := BuildRequest(RequestInput{
		Mode: ModeResponsesSSE, Model: "gpt-5.6-sol", Prompt: "ping", OAuth: true,
		ClientCompatibility: " CODEX_RESPONSES ",
	})
	if err != nil {
		t.Fatalf("BuildRequest() error = %v", err)
	}
	body := decodeTestObject(t, spec.Body)
	for key, want := range map[string]any{
		"stream": true, "store": false, "max_output_tokens": json.Number("1"),
		"parallel_tool_calls": false,
	} {
		if !reflect.DeepEqual(body[key], want) {
			t.Fatalf("body[%q] = %#v, want %#v", key, body[key], want)
		}
	}
	if !reflect.DeepEqual(body["include"], []any{"reasoning.encrypted_content"}) {
		t.Fatalf("include = %#v", body["include"])
	}
	if got := objectValue(body["reasoning"])["context"]; got != "all_turns" {
		t.Fatalf("reasoning context = %#v", got)
	}
	if spec.Header.Get("originator") != "Codex Desktop" || spec.Header.Get("x-openai-internal-codex-responses-lite") != "true" {
		t.Fatalf("codex headers = %#v", spec.Header)
	}
	metadata := decodeTestObject(t, []byte(spec.Header.Get("x-codex-turn-metadata")))
	clientMetadata := objectValue(body["client_metadata"])
	sessionID := text(metadata["session_id"])
	if sessionID == "" || spec.Header.Get("session-id") != sessionID || spec.Header.Get("thread-id") != sessionID || body["prompt_cache_key"] != sessionID {
		t.Fatalf("inconsistent session metadata: header=%#v body=%#v metadata=%#v", spec.Header, body, metadata)
	}
	if clientMetadata["session_id"] != sessionID || clientMetadata["thread_id"] != sessionID || metadata["turn_started_at_unix_ms"] == nil {
		t.Fatalf("client metadata = %#v, metadata = %#v", clientMetadata, metadata)
	}
}

func TestBuildRequestMatchesClaudeCodeProfile(t *testing.T) {
	spec, err := BuildRequest(RequestInput{
		Mode: ModeMessagesSSE, Model: "claude", Prompt: "ping", SessionID: "session-1",
		Today: "2026-07-28", WorkingDirectory: `F:\sub2api-lite`,
	})
	if err != nil {
		t.Fatalf("BuildRequest() error = %v", err)
	}
	body := decodeTestObject(t, spec.Body)
	if spec.Header.Get("x-juhe-client-profile") != "claude_code" || spec.Header.Get("x-claude-code-session-id") != "session-1" {
		t.Fatalf("headers = %#v", spec.Header)
	}
	messages := arrayValue(body["messages"])
	content := arrayValue(objectValue(messages[0])["content"])
	if len(content) != 2 || !strings.Contains(text(objectValue(content[0])["text"]), "<system-reminder>") || text(objectValue(content[1])["text"]) != "ping" {
		t.Fatalf("messages = %#v", messages)
	}
	system := arrayValue(body["system"])
	if len(system) != 3 || !strings.Contains(text(objectValue(system[0])["text"]), "cc_version=2.1.201.eb7") || text(objectValue(system[2])["text"]) != `CWD: F:\sub2api-lite`+"\nDate: 2026-07-28" {
		t.Fatalf("system = %#v", system)
	}
	metadata := objectValue(body["metadata"])
	userID := decodeTestObject(t, []byte(text(metadata["user_id"])))
	if userID["session_id"] != "session-1" {
		t.Fatalf("metadata user id = %#v", userID)
	}
}

func TestBuildRequestRejectsInvalidInputs(t *testing.T) {
	valid := RequestInput{Mode: ModeChatJSON, Model: "model"}
	tests := []struct {
		name  string
		input RequestInput
	}{
		{"missing model", RequestInput{Mode: ModeChatJSON}},
		{"unknown mode", RequestInput{Mode: "unknown", Model: "model"}},
		{"model control character", RequestInput{Mode: ModeChatJSON, Model: "bad\nmodel"}},
		{"messages missing session", RequestInput{Mode: ModeMessagesJSON, Model: "model", Today: "2026-07-28", WorkingDirectory: `F:\work`}},
		{"messages invalid date", RequestInput{Mode: ModeMessagesJSON, Model: "model", SessionID: "s", Today: "2026-02-30", WorkingDirectory: `F:\work`}},
		{"messages missing cwd", RequestInput{Mode: ModeMessagesJSON, Model: "model", SessionID: "s", Today: "2026-07-28"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := BuildRequest(test.input)
			if !errors.Is(err, ErrInvalidProtocolInput) {
				t.Fatalf("error = %v", err)
			}
		})
	}
	if _, err := BuildRequest(valid); err != nil {
		t.Fatalf("valid request error = %v", err)
	}
}

func TestInspectEvidenceCoversEveryEndpointMode(t *testing.T) {
	tests := []struct {
		mode EndpointMode
		body string
	}{
		{ModeChatJSON, `{"choices":[{"finish_reason":"stop"}]}`},
		{ModeChatSSE, "data: {\"choices\":[{\"delta\":{\"content\":\"OK\"}}]}\n\ndata: [DONE]\n"},
		{ModeResponsesJSON, `{"status":"completed","object":"response"}`},
		{ModeResponsesSSE, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}"},
		{ModeMessagesJSON, `{"type":"message","stop_reason":"end_turn"}`},
		{ModeMessagesSSE, "event: message_stop\ndata: {\"type\":\"message_stop\"}"},
		{ModeGenerateContentJSON, `{"candidates":[{"finishReason":"STOP"}]}`},
		{ModeGenerateContentSSE, "data: {\"candidates\":[{\"finishReason\":\"STOP\"}]}"},
		{ModeInteractionsJSON, `{"status":"completed","object":"interaction"}`},
		{ModeInteractionsSSE, "event: update\ndata: {\"interaction\":{\"status\":\"completed\"}}"},
	}
	for _, test := range tests {
		t.Run(string(test.mode), func(t *testing.T) {
			evidence, err := InspectEvidence(test.mode, []byte(test.body), false)
			if err != nil || !evidence.Complete || evidence.Failed {
				t.Fatalf("InspectEvidence() = %+v, %v", evidence, err)
			}
		})
	}
}

func TestInspectEvidenceRejectsFalseCompletion(t *testing.T) {
	tests := []struct {
		name          string
		mode          EndpointMode
		body          string
		wantFailed    bool
		wantMalformed int
	}{
		{"done alone", ModeChatSSE, "data: [DONE]\n", false, 0},
		{"done unsupported", ModeResponsesSSE, "data: [DONE]\n", false, 0},
		{"response output wrong type", ModeResponsesJSON, `{"status":"completed","output":{}}`, false, 0},
		{"interaction steps wrong type", ModeInteractionsJSON, `{"status":"completed","steps":{}}`, false, 0},
		{"message without stop", ModeMessagesJSON, `{"type":"message"}`, false, 0},
		{"gemini empty candidates", ModeGenerateContentJSON, `{"candidates":[]}`, false, 0},
		{"explicit json error", ModeChatJSON, `{"error":"denied","choices":[{"finish_reason":"stop"}]}`, true, 0},
		{"stream error overrides completion", ModeMessagesSSE, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\nevent: error\ndata: {\"type\":\"error\"}", true, 0},
		{"malformed event ignored", ModeChatSSE, "data: nope\n\ndata: [DONE]", false, 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			evidence, err := InspectEvidence(test.mode, []byte(test.body), false)
			if err != nil || evidence.Complete || evidence.Failed != test.wantFailed || evidence.MalformedEvents != test.wantMalformed {
				t.Fatalf("InspectEvidence() = %+v, %v", evidence, err)
			}
		})
	}
}

func TestInspectEvidenceHandlesMultilineSSEAndTruncation(t *testing.T) {
	body := "event: response.completed\n" +
		"data: {\"type\":\"response.completed\",\n" +
		"data: \"response\":{\"status\":\"completed\",\"object\":\"response\"}}"
	evidence, err := InspectEvidence(ModeResponsesSSE, []byte(body), false)
	if err != nil || !evidence.Complete {
		t.Fatalf("multiline evidence = %+v, %v", evidence, err)
	}
	if _, err := InspectEvidence(ModeResponsesJSON, []byte(`{"status":"completed","object":"response"}`), true); !errors.Is(err, ErrInvalidProtocolInput) {
		t.Fatalf("truncated error = %v", err)
	}
	if _, err := InspectEvidence(ModeChatJSON, []byte(`{} {}`), false); !errors.Is(err, ErrInvalidProtocolInput) {
		t.Fatalf("trailing JSON error = %v", err)
	}
}

func TestInspectEvidenceAcceptsGeminiCodeAssistNestedSSE(t *testing.T) {
	body := []byte("data: {\"response\":{\"candidates\":[{\"finishReason\":\"STOP\"}]}}\n\n")
	evidence, err := InspectEvidence(ModeGenerateContentSSE, body, false)
	if err != nil || !evidence.Complete || evidence.Failed {
		t.Fatalf("evidence = %+v error = %v", evidence, err)
	}
}

func decodeTestObject(t *testing.T, body []byte) map[string]any {
	t.Helper()
	object, err := decodeObject(body)
	if err != nil {
		t.Fatalf("decode object: %v", err)
	}
	return object
}
