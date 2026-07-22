package gateway

import "testing"

func TestRegistryListsSupportedProtocols(t *testing.T) {
	t.Parallel()

	want := []Definition{
		{ID: "openai-v1", Code: ProtocolOpenAI, Version: "v1", ResponseProtocol: ResponseProtocolOpenAIV1, ClientErrorProtocol: ClientErrorOpenAI, DefaultClientProfile: ClientProfileGenericOpenAI},
		{ID: "anthropic-v1", Code: ProtocolAnthropic, Version: "v1", ResponseProtocol: ResponseProtocolAnthropicV1, ClientErrorProtocol: ClientErrorAnthropic, DefaultClientProfile: ClientProfileGenericAnthropic},
		{ID: "gemini-v1beta", Code: ProtocolGemini, Version: "v1beta", ResponseProtocol: ResponseProtocolGeminiV1Beta, ClientErrorProtocol: ClientErrorGemini, DefaultClientProfile: ClientProfileGenericGemini},
	}

	got := ListDefinitions()
	if len(got) != len(want) {
		t.Fatalf("ListDefinitions() length = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("ListDefinitions()[%d] = %#v, want %#v", i, got[i], want[i])
		}
	}

	got[0].ID = "mutated"
	if next := ListDefinitions()[0].ID; next != "openai-v1" {
		t.Fatalf("ListDefinitions() exposed registry storage: %q", next)
	}
}

func TestDefinitionForProfileNormalizesTokens(t *testing.T) {
	t.Parallel()

	definition, ok := DefinitionForProfile(Profile{Code: " OpenAI ", Version: " V1 "})
	if !ok || definition.ID != "openai-v1" {
		t.Fatalf("DefinitionForProfile() = %#v, %v", definition, ok)
	}
	if _, ok := DefinitionForProfile(Profile{Code: "openai", Version: "v1beta"}); ok {
		t.Fatal("DefinitionForProfile() accepted mismatched version")
	}
}

func TestResolveDefinitionUsesPathThenProfile(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		request RequestShape
		profile *Profile
		wantID  string
		wantOK  bool
	}{
		{name: "openai path wins over profile", request: RequestShape{Method: "POST", Path: "/v1/responses"}, profile: &Profile{Code: "anthropic", Version: "v1"}, wantID: "openai-v1", wantOK: true},
		{name: "ambiguous models keeps node priority", request: RequestShape{Method: "GET", Path: "/v1/models"}, profile: &Profile{Code: "gemini", Version: "v1beta"}, wantID: "openai-v1", wantOK: true},
		{name: "anthropic native path", request: RequestShape{Method: "POST", Path: "/v1/messages"}, wantID: "anthropic-v1", wantOK: true},
		{name: "gemini native path", request: RequestShape{Method: "POST", Path: "/v1beta/models/gemini-2.5-pro:generateContent"}, wantID: "gemini-v1beta", wantOK: true},
		{name: "profile fallback", request: RequestShape{Method: "POST", Path: "/custom"}, profile: &Profile{Code: "gemini", Version: "v1beta"}, wantID: "gemini-v1beta", wantOK: true},
		{name: "unknown", request: RequestShape{Method: "POST", Path: "/custom"}, profile: &Profile{Code: "unknown", Version: "v1"}, wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := ResolveDefinition(tt.request, tt.profile)
			if ok != tt.wantOK || got.ID != tt.wantID {
				t.Fatalf("ResolveDefinition() = %#v, %v, want id=%q ok=%v", got, ok, tt.wantID, tt.wantOK)
			}
		})
	}
}
