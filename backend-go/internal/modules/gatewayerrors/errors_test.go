package gatewayerrors

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"juhe-ai/backend-go/internal/modules/gatewaycredentials"
	gatewayprotocol "juhe-ai/backend-go/internal/protocols/gateway"
)

func TestClassifyAPIKeyStateUsesOnePublicUnauthorizedError(t *testing.T) {
	states := []APIKeyState{APIKeyStateInvalid, APIKeyStateDisabled, APIKeyStateExpired, APIKeyState("unexpected_database_value")}
	for _, state := range states {
		t.Run(string(state), func(t *testing.T) {
			public, ok := ClassifyAPIKeyState(state)
			if !ok {
				t.Fatal("ClassifyAPIKeyState() ok = false, want true")
			}
			if public.StatusCode() != 401 || public.Message() != "API Key 无效或不可用" || public.Class() != ErrorClassAuthentication || public.Code() != "invalid_api_key" {
				t.Fatalf("public error = %+v", public)
			}
			payload, err := json.Marshal(public.Render(ProtocolOpenAI).Payload)
			if err != nil {
				t.Fatalf("marshal payload: %v", err)
			}
			for _, internal := range []string{"disabled", "expired"} {
				if strings.Contains(string(payload), internal) {
					t.Fatalf("payload leaked internal state %q: %s", internal, payload)
				}
			}
		})
	}
}

func TestClassifyRecognizesWrappedAPIKeyFailuresWithoutLeakingCause(t *testing.T) {
	internal := errors.New("postgres account_status=disabled password=secret")
	classified, ok := Classify(fmt.Errorf("gateway runtime lookup: %w", NewAPIKeyStateError(APIKeyStateDisabled, internal)))
	if !ok {
		t.Fatal("Classify() ok = false, want true")
	}
	if classified.Message() != "API Key 无效或不可用" || classified.Code() != "invalid_api_key" {
		t.Fatalf("classified = %+v", classified)
	}
	payload, err := json.Marshal(classified.Render(ProtocolOpenAI).Payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	if string(payload) != `{"error":{"message":"API Key 无效或不可用","type":"invalid_request_error","code":"invalid_api_key"}}` {
		t.Fatalf("payload = %s", payload)
	}
}

func TestClassifyCredentialMissingAndActiveKey(t *testing.T) {
	missing, ok := Classify(ErrCredentialMissing)
	if !ok || missing.StatusCode() != 401 || missing.Message() != "缺少访问令牌" || missing.Code() != "missing_api_key" {
		t.Fatalf("missing credential = %+v, ok=%v", missing, ok)
	}
	if _, ok := ClassifyAPIKeyState(APIKeyStateActive); ok {
		t.Fatal("active API Key was classified as a failure")
	}
	if _, ok := Classify(errors.New("postgres connection failed")); ok {
		t.Fatal("unrelated error was classified as a client authentication failure")
	}
}

func TestRenderUsesProtocolSpecificDTOs(t *testing.T) {
	public, ok := ClassifyAPIKeyState(APIKeyStateExpired)
	if !ok {
		t.Fatal("ClassifyAPIKeyState() ok = false")
	}

	openAI, ok := public.Render(ProtocolOpenAI).Payload.(OpenAIErrorPayload)
	if !ok || openAI.Error.Type != "invalid_request_error" || openAI.Error.Code != "invalid_api_key" {
		t.Fatalf("OpenAI payload = %#v", public.Render(ProtocolOpenAI).Payload)
	}
	anthropic, ok := public.Render(ProtocolAnthropic).Payload.(AnthropicErrorPayload)
	if !ok || anthropic.Type != "error" || anthropic.Error.Type != "authentication_error" || anthropic.Error.Code != "invalid_api_key" {
		t.Fatalf("Anthropic payload = %#v", public.Render(ProtocolAnthropic).Payload)
	}
	gemini, ok := public.Render(ProtocolGemini).Payload.(GeminiErrorPayload)
	if !ok || gemini.Error.Status != "UNAUTHENTICATED" || gemini.Error.Code != "invalid_api_key" {
		t.Fatalf("Gemini payload = %#v", public.Render(ProtocolGemini).Payload)
	}
}

func TestClassifyCredentialExtractionErrors(t *testing.T) {
	tests := []struct {
		name     string
		input    gatewaycredentials.Input
		wantCode string
		wantText string
	}{
		{
			name:     "missing",
			input:    gatewaycredentials.Input{},
			wantCode: "missing_api_key",
			wantText: "缺少访问令牌",
		},
		{
			name:     "malformed",
			input:    gatewaycredentials.Input{Authorization: []string{"Basic secret"}},
			wantCode: "invalid_api_key",
			wantText: "API Key 无效或不可用",
		},
		{
			name:     "ambiguous",
			input:    gatewaycredentials.Input{XAPIKey: []string{"first", "second"}},
			wantCode: "invalid_api_key",
			wantText: "API Key 无效或不可用",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, extractionErr := gatewaycredentials.Extract(test.input)
			if extractionErr == nil {
				t.Fatal("Extract() error = nil")
			}
			public, ok := Classify(fmt.Errorf("extract gateway credential: %w", extractionErr))
			if !ok {
				t.Fatalf("Classify() ok = false for %v", extractionErr)
			}
			if public.Code() != test.wantCode || public.Message() != test.wantText {
				t.Fatalf("Classify() = code %q message %q, want code %q message %q", public.Code(), public.Message(), test.wantCode, test.wantText)
			}
		})
	}
}

func TestRenderAcceptsProtocolRegistryClientErrorProtocol(t *testing.T) {
	public, ok := ClassifyAPIKeyState(APIKeyStateInvalid)
	if !ok {
		t.Fatal("ClassifyAPIKeyState() ok = false")
	}
	rendered := public.Render(gatewayprotocol.ClientErrorAnthropic)
	if _, ok := rendered.Payload.(AnthropicErrorPayload); !ok {
		t.Fatalf("Render() payload = %T, want AnthropicErrorPayload", rendered.Payload)
	}
}
