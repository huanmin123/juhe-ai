package accounts

import "testing"

func TestParseTestRequestBodyAcceptsConnectivityKind(t *testing.T) {
	parsed, message := parseTestRequestBody(map[string]any{"kind": "connectivity"})
	if message != "" {
		t.Fatalf("parse message = %q", message)
	}
	if parsed.Kind != "connectivity" {
		t.Fatalf("kind = %q, want connectivity", parsed.Kind)
	}
}

func TestParseTestRequestBodyRejectsUnknownKind(t *testing.T) {
	if _, message := parseTestRequestBody(map[string]any{"kind": "deep"}); message == "" {
		t.Fatal("unknown kind must be rejected")
	}
}

func TestParseTestDraftAccountAcceptsCapabilityDomain(t *testing.T) {
	record := map[string]any{
		"providerCode":            "gpt",
		"name":                    "draft",
		"type":                    "api_key",
		"capabilityDomain":        "video",
		"credentials":             map[string]any{"api_key": "sk-test"},
		"supportedModels":         []any{"video-model"},
		"healthCheckModel":        "video-model",
		"healthCheckEndpointMode": "video_create",
		"modelMappings":           []any{},
		"groupId":                 "group-1",
	}
	input, message := parseTestDraftAccountInput(record)
	if message != "" {
		t.Fatalf("parse message = %q", message)
	}
	if input.CapabilityDomain != "video" {
		t.Fatalf("capabilityDomain = %q, want video", input.CapabilityDomain)
	}
}
