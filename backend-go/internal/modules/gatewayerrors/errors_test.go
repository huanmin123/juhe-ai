package gatewayerrors

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
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
