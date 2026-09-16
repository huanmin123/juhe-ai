package modelcheckprofile

import (
	"reflect"
	"testing"
)

// TestW9AFrozenCatalogShape freezes the structural contract of the catalog:
// every entry is fully populated, DefaultModel is a member of Models and
// profile IDs referenced by ProviderProtocolProfileIDs are unique across the
// whole catalog.
func TestW9AFrozenCatalogShape(t *testing.T) {
	profiles := Profiles()
	if len(profiles) != 9 {
		t.Fatalf("catalog size = %d, want 9", len(profiles))
	}
	seenProfileIDs := map[string]bool{}
	for _, p := range profiles {
		if p.ID == "" || p.ProtocolLabel == "" || p.ProviderCode == "" || p.DefaultModel == "" {
			t.Fatalf("catalog entry has empty identity fields: %+v", p)
		}
		if len(p.Models) == 0 {
			t.Fatalf("catalog entry %s has no models", p.ID)
		}
		if len(p.ProviderProtocolProfileIDs) == 0 {
			t.Fatalf("catalog entry %s has no provider profile ids", p.ID)
		}
		found := false
		for _, m := range p.Models {
			if m == p.DefaultModel {
				found = true
			}
		}
		if !found {
			t.Fatalf("DefaultModel %q not in Models for %s", p.DefaultModel, p.ID)
		}
		for _, id := range p.ProviderProtocolProfileIDs {
			if seenProfileIDs[id] {
				t.Fatalf("provider profile id %s duplicated", id)
			}
			seenProfileIDs[id] = true
		}
		switch p.Protocol {
		case ProtocolOpenAIResponses, ProtocolOpenAIChat, ProtocolAnthropic, ProtocolGeminiNative:
		default:
			t.Fatalf("unknown protocol %q in catalog", p.Protocol)
		}
	}
	if DefaultModel != "gpt-5.6-sol" || DefaultProfile != "quick" || DistributionSampleCount != 5 {
		t.Fatalf("frozen constants drifted: %q %q %d", DefaultModel, DefaultProfile, DistributionSampleCount)
	}
	if ProbeSetVersion == "" || QuickProbeSetVersion == "" {
		t.Fatal("probe set versions must not be empty")
	}
}

// TestW9AProfilesReturnsDeepCopies verifies callers cannot mutate the shared
// catalog through the returned slice.
func TestW9AProfilesReturnsDeepCopies(t *testing.T) {
	first := Profiles()
	first[0].Models[0] = "mutated"
	first[0].ProviderProtocolProfileIDs[0] = "mutated-id"
	second := Profiles()
	if second[0].Models[0] == "mutated" {
		t.Fatal("Profiles() leaked mutable Models slice")
	}
	if second[0].ProviderProtocolProfileIDs[0] == "mutated-id" {
		t.Fatal("Profiles() leaked mutable ProviderProtocolProfileIDs slice")
	}
	if !reflect.DeepEqual(Profiles(), Profiles()) {
		t.Fatal("Profiles() output not deterministic")
	}
}

func TestW9ANormalizeToken(t *testing.T) {
	cases := []struct{ in, want string }{
		{"  GPT ", "gpt"},
		{"OpenAI_Responses", "openai_responses"},
		{"", ""},
		{"\tMixed CASE\n", "mixed case"},
	}
	for _, c := range cases {
		if got := NormalizeToken(c.in); got != c.want {
			t.Fatalf("NormalizeToken(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestW9AFind(t *testing.T) {
	p, ok := Find("GPT", "PROFILE_GPT_OPENAI_V1")
	if !ok || p.ProviderCode != "gpt" || p.Protocol != ProtocolOpenAIResponses {
		t.Fatalf("Find case-insensitive hit failed: %+v ok=%v", p, ok)
	}
	if _, ok := Find("OpenAI", "profile_openai_openai_v1"); !ok {
		t.Fatal("Find should match openai provider profile")
	}
	if p, ok := Find("nope", "profile_gpt_openai_v1"); ok || p.ID != "" {
		t.Fatalf("unknown provider must miss, got %+v ok=%v", p, ok)
	}
	if p, ok := Find("gpt", "unknown-profile"); ok || p.ID != "" {
		t.Fatalf("unknown profile id must miss, got %+v ok=%v", p, ok)
	}
}

func TestW9AFindForModel(t *testing.T) {
	if p, ok := FindForModel("gpt", "profile_gpt_openai_v1", "gpt-5.6-terra"); !ok || p.DefaultModel != "gpt-5.6-sol" {
		t.Fatalf("FindForModel known model failed: %+v ok=%v", p, ok)
	}
	if _, ok := FindForModel("gpt", "profile_gpt_openai_v1", "not-a-model"); ok {
		t.Fatal("model outside profile must miss")
	}
	if _, ok := FindForModel("gpt", "unknown", "gpt-5.6-sol"); ok {
		t.Fatal("unknown profile must miss")
	}
}

func TestW9ASupportedModels(t *testing.T) {
	models := SupportedModels()
	if len(models) == 0 {
		t.Fatal("SupportedModels empty")
	}
	seen := map[string]bool{}
	for _, m := range models {
		if m == "" {
			t.Fatal("empty model in SupportedModels")
		}
		if seen[m] {
			t.Fatalf("model %s duplicated despite dedup", m)
		}
		seen[m] = true
	}
	if !seen[DefaultModel] {
		t.Fatalf("default model %s missing from SupportedModels", DefaultModel)
	}
}

func TestW9APairedModel(t *testing.T) {
	gptProfile, ok := Find("gpt", "profile_gpt_openai_v1")
	if !ok {
		t.Fatal("gpt profile missing")
	}
	if got := PairedModel(gptProfile, "gpt-5.6-sol"); got != "gpt-5.6-terra" {
		t.Fatalf("paired model = %q, want gpt-5.6-terra", got)
	}
	// Paired entry exists globally but is not part of this profile: fall
	// through to the first different candidate.
	narrow := ProtocolProfile{Models: []string{"gpt-5.6-sol", "gpt-5.4"}}
	if got := PairedModel(narrow, "gpt-5.6-sol"); got != "gpt-5.4" {
		t.Fatalf("pair outside profile = %q, want gpt-5.4", got)
	}
	// No global pair: first different candidate.
	if got := PairedModel(narrow, "unknown-model"); got != "gpt-5.6-sol" {
		t.Fatalf("unpaired model = %q, want gpt-5.6-sol", got)
	}
	// Profile contains only the model itself: empty result.
	single := ProtocolProfile{Models: []string{"only-model"}}
	if got := PairedModel(single, "only-model"); got != "" {
		t.Fatalf("single-model profile pair = %q, want empty", got)
	}
}

func TestW9ASourceEndpointFamilies(t *testing.T) {
	cases := []struct {
		protocol Protocol
		want     []EndpointFamily
	}{
		{ProtocolOpenAIResponses, []EndpointFamily{EndpointResponses}},
		{ProtocolOpenAIChat, []EndpointFamily{EndpointChatCompletions}},
		{ProtocolAnthropic, []EndpointFamily{EndpointMessages}},
		{ProtocolGeminiNative, []EndpointFamily{EndpointGenerateContent, EndpointStreamGenerate}},
		{Protocol("bogus"), nil},
	}
	for _, c := range cases {
		got := SourceEndpointFamilies(ProtocolProfile{Protocol: c.protocol})
		if !reflect.DeepEqual(got, c.want) {
			t.Fatalf("SourceEndpointFamilies(%s) = %v, want %v", c.protocol, got, c.want)
		}
	}
}

func TestW9AEndpointModeForProtocol(t *testing.T) {
	cases := []struct {
		protocol Protocol
		stream   bool
		want     string
	}{
		{ProtocolOpenAIResponses, false, EndpointModeResponsesJSON},
		{ProtocolOpenAIResponses, true, EndpointModeResponsesSSE},
		{ProtocolOpenAIChat, false, EndpointModeChatJSON},
		{ProtocolOpenAIChat, true, EndpointModeChatSSE},
		{ProtocolAnthropic, false, EndpointModeMessagesJSON},
		{ProtocolAnthropic, true, EndpointModeMessagesSSE},
		{ProtocolGeminiNative, false, EndpointModeGenerateContentJSON},
		{ProtocolGeminiNative, true, EndpointModeGenerateContentSSE},
		{Protocol("bogus"), false, ""},
		{Protocol("bogus"), true, ""},
	}
	for _, c := range cases {
		got := EndpointModeForProtocol(c.protocol, c.stream)
		if got != c.want {
			t.Fatalf("EndpointModeForProtocol(%s, %v) = %q, want %q", c.protocol, c.stream, got, c.want)
		}
	}
}

func TestW9AProtocolForEndpointMode(t *testing.T) {
	cases := []struct {
		mode   string
		want   Protocol
		wantOK bool
	}{
		{EndpointModeResponsesJSON, ProtocolOpenAIResponses, true},
		{EndpointModeResponsesSSE, ProtocolOpenAIResponses, true},
		{EndpointModeChatJSON, ProtocolOpenAIChat, true},
		{EndpointModeChatSSE, ProtocolOpenAIChat, true},
		{EndpointModeMessagesJSON, ProtocolAnthropic, true},
		{EndpointModeMessagesSSE, ProtocolAnthropic, true},
		{EndpointModeGenerateContentJSON, ProtocolGeminiNative, true},
		{EndpointModeGenerateContentSSE, ProtocolGeminiNative, true},
		{" images_json ", "", false},
		{"", "", false},
		{"bogus", "", false},
	}
	for _, c := range cases {
		got, ok := ProtocolForEndpointMode(c.mode)
		if ok != c.wantOK || got != c.want {
			t.Fatalf("ProtocolForEndpointMode(%q) = (%q, %v), want (%q, %v)", c.mode, got, ok, c.want, c.wantOK)
		}
	}
}

func TestW9AEndpointModeIsStreaming(t *testing.T) {
	streaming := []string{EndpointModeResponsesSSE, EndpointModeChatSSE, EndpointModeMessagesSSE, EndpointModeGenerateContentSSE}
	for _, mode := range streaming {
		if !EndpointModeIsStreaming(mode) {
			t.Fatalf("mode %q should be streaming", mode)
		}
	}
	nonStreaming := []string{EndpointModeResponsesJSON, EndpointModeChatJSON, EndpointModeMessagesJSON, EndpointModeGenerateContentJSON, "bogus", ""}
	for _, mode := range nonStreaming {
		if EndpointModeIsStreaming(mode) {
			t.Fatalf("mode %q should not be streaming", mode)
		}
	}
}

func TestW9AEndpointModeMatchesProtocol(t *testing.T) {
	if !EndpointModeMatchesProtocol(ProtocolOpenAIResponses, EndpointModeResponsesSSE) {
		t.Fatal("matching mode should resolve to its protocol")
	}
	if EndpointModeMatchesProtocol(ProtocolAnthropic, EndpointModeResponsesSSE) {
		t.Fatal("mismatched mode should not match protocol")
	}
	if EndpointModeMatchesProtocol(ProtocolOpenAIChat, "bogus-mode") {
		t.Fatal("unknown mode should never match")
	}
}
