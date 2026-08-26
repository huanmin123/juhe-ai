package modelcheckprobe

import (
	"encoding/json"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckprofile"
)

func TestBuildBasicMatchesFrozenNodeProtocolContracts(t *testing.T) {
	for _, test := range []struct {
		name       string
		protocol   modelcheckprofile.Protocol
		model      string
		stream     bool
		path       string
		assertBody func(*testing.T, map[string]any)
	}{
		{
			name: "OpenAI Responses", protocol: modelcheckprofile.ProtocolOpenAIResponses, model: "gpt-5.6-sol", path: "/v1/responses",
			assertBody: func(t *testing.T, body map[string]any) {
				t.Helper()
				if body["model"] != "gpt-5.6-sol" || body["max_output_tokens"] != float64(16) || body["store"] != false || body["temperature"] != float64(0) {
					t.Fatalf("responses body=%#v", body)
				}
			},
		},
		{
			name: "OpenAI Chat", protocol: modelcheckprofile.ProtocolOpenAIChat, model: "glm-5.2", path: "/v1/chat/completions",
			assertBody: func(t *testing.T, body map[string]any) {
				t.Helper()
				if body["model"] != "glm-5.2" || body["max_tokens"] != float64(64) || body["temperature"] != float64(0) {
					t.Fatalf("chat body=%#v", body)
				}
			},
		},
		{
			name: "Anthropic Messages", protocol: modelcheckprofile.ProtocolAnthropic, model: "claude-opus-5", path: "/v1/messages",
			assertBody: func(t *testing.T, body map[string]any) {
				t.Helper()
				if body["model"] != "claude-opus-5" || body["max_tokens"] != float64(16) {
					t.Fatalf("Anthropic body=%#v", body)
				}
				if _, sentTemperature := body["temperature"]; sentTemperature {
					t.Fatalf("Anthropic body must not contain temperature: %#v", body)
				}
			},
		},
		{
			name: "Gemini Native", protocol: modelcheckprofile.ProtocolGeminiNative, model: "gemini-3.5-flash", stream: true, path: "/v1beta/models/gemini-3.5-flash:streamGenerateContent?alt=sse",
			assertBody: func(t *testing.T, body map[string]any) {
				t.Helper()
				config := body["generationConfig"].(map[string]any)
				if config["maxOutputTokens"] != float64(128) || config["temperature"] != float64(0) {
					t.Fatalf("Gemini config=%#v", config)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			request, err := BuildBasic(test.protocol, test.model, "only OK", BasicOptions{MaxOutputTokens: 16, Stream: test.stream})
			if err != nil || request.Path != test.path || request.ExpectedModel != test.model {
				t.Fatalf("request=%#v err=%v", request, err)
			}
			body := map[string]any{}
			if err := json.Unmarshal(request.Body, &body); err != nil {
				t.Fatal(err)
			}
			test.assertBody(t, body)
		})
	}
}

func TestBuildStructuredAndToolRequestsPreserveProtocolShapes(t *testing.T) {
	structured, err := BuildStructured(modelcheckprofile.ProtocolOpenAIResponses, "gpt-5.6-sol", false)
	if err != nil {
		t.Fatal(err)
	}
	var structuredBody map[string]any
	if err := json.Unmarshal(structured.Body, &structuredBody); err != nil {
		t.Fatal(err)
	}
	if _, ok := structuredBody["text"]; !ok {
		t.Fatalf("structured body=%#v", structuredBody)
	}
	tool, err := BuildTool(modelcheckprofile.ProtocolOpenAIChat, "gpt-5.6-sol", false)
	if err != nil {
		t.Fatal(err)
	}
	var toolBody map[string]any
	if err := json.Unmarshal(tool.Body, &toolBody); err != nil {
		t.Fatal(err)
	}
	if _, ok := toolBody["tools"]; !ok {
		t.Fatalf("tool body=%#v", toolBody)
	}
}

func TestBuildBasicRejectsMalformedAndUnknownInput(t *testing.T) {
	for _, test := range []struct {
		protocol modelcheckprofile.Protocol
		model    string
		prompt   string
		tokens   int
	}{
		{modelcheckprofile.ProtocolOpenAIResponses, "", "prompt", 1},
		{modelcheckprofile.ProtocolOpenAIResponses, "model", "", 1},
		{modelcheckprofile.ProtocolOpenAIResponses, "model", "prompt", 0},
		{modelcheckprofile.Protocol("unknown"), "model", "prompt", 1},
	} {
		if _, err := BuildBasic(test.protocol, test.model, test.prompt, BasicOptions{MaxOutputTokens: test.tokens}); err == nil {
			t.Fatalf("expected reject: %#v", test)
		}
	}
}
