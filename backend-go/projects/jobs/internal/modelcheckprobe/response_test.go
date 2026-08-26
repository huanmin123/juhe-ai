package modelcheckprobe

import (
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckprofile"
)

func TestParseResponseMatchesNodeJSONAndSSEContracts(t *testing.T) {
	for _, test := range []struct {
		name, body, model, output, usageKey string
		protocol                            modelcheckprofile.Protocol
		usage                               float64
	}{
		{
			name: "responses JSON", protocol: modelcheckprofile.ProtocolOpenAIResponses,
			body: `{"model":"gpt-5.6-sol","output_text":"OK","usage":{"total_tokens":2},"system_fingerprint":"fp-test"}`, model: "gpt-5.6-sol", output: "OK", usageKey: "total_tokens", usage: 2,
		},
		{
			name: "chat SSE", protocol: modelcheckprofile.ProtocolOpenAIChat,
			body: "event: chunk\r\ndata: {\"model\":\"glm-5.2\",\r\ndata: \"choices\":[{\"delta\":{\"content\":\"OK\"}}],\"usage\":{\"total_tokens\":3}}", model: "glm-5.2", output: "OK", usageKey: "total_tokens", usage: 3,
		},
		{
			name: "Anthropic SSE", protocol: modelcheckprofile.ProtocolAnthropic,
			body: "event: message_start\r\ndata: {\"message\":{\"model\":\"claude-opus-5\",\r\ndata: \"usage\":{\"input_tokens\":4}}}\r\n\r\nevent: content_block_delta\r\ndata: {\"delta\":{\"text\":\"OK\"}}", model: "claude-opus-5", output: "OK", usageKey: "input_tokens", usage: 4,
		},
		{
			name: "Gemini SSE", protocol: modelcheckprofile.ProtocolGeminiNative,
			body: "data: {\"modelVersion\":\"gemini-3.5-flash\",\r\ndata: \"candidates\":[{\"content\":{\"parts\":[{\"text\":\"OK\"}]}}],\"usageMetadata\":{\"totalTokenCount\":5}}", model: "gemini-3.5-flash", output: "OK", usageKey: "totalTokenCount", usage: 5,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			parsed := ParseResponse(test.protocol, []byte(test.body))
			if parsed.Model != test.model || parsed.OutputText != test.output || parsed.Usage[test.usageKey] != test.usage || !parsed.Successful(200) {
				t.Fatalf("parsed=%#v", parsed)
			}
		})
	}
}

func TestParseResponseRejectsHTTP200ErrorEnvelopes(t *testing.T) {
	for _, test := range []struct {
		protocol modelcheckprofile.Protocol
		body     string
		message  string
	}{
		{modelcheckprofile.ProtocolOpenAIResponses, `{"error":{"code":"invalid_request","message":"openai json failed"}}`, "openai json failed"},
		{modelcheckprofile.ProtocolAnthropic, `{"type":"error","error":{"type":"invalid_request_error","message":"anthropic json failed"}}`, "anthropic json failed"},
		{modelcheckprofile.ProtocolGeminiNative, `{"error":{"status":"INVALID_ARGUMENT","message":"gemini json failed"}}`, "gemini json failed"},
		{modelcheckprofile.ProtocolOpenAIResponses, "event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"error\":{\"message\":\"openai stream failed\"}}}", "openai stream failed"},
	} {
		parsed := ParseResponse(test.protocol, []byte(test.body))
		if parsed.ErrorMessage != test.message || parsed.Successful(200) {
			t.Fatalf("protocol=%s parsed=%#v", test.protocol, parsed)
		}
		if parsed.StreamFailureMessage != "" && parsed.StreamFailureMessage != test.message {
			t.Fatalf("stream failure=%q", parsed.StreamFailureMessage)
		}
	}
}
