package modelcheckprofile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

type nodeProfileOracle struct {
	DefaultModel   string `json:"defaultModel"`
	DefaultProfile string `json:"defaultProfile"`
	Profiles       []struct {
		ProviderCode           string   `json:"providerCode"`
		ProfileIDs             []string `json:"profileIds"`
		Models                 []string `json:"models"`
		SourceEndpointFamilies []string `json:"sourceEndpointFamilies"`
	} `json:"profiles"`
}

func TestCatalogMatchesContractFixture(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	// The contract fixture was carried over from the archived Node oracle
	// (migration-backup/node/j3b-model-check, never modified) and is now the
	// Go-owned baseline: update testdata/go-model-check-profile-contract.json
	// together with the catalog when the check models change.
	fixturePath := filepath.Join(filepath.Dir(file), "testdata", "go-model-check-profile-contract.json")
	bytes, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("read model-check contract fixture %s: %v", fixturePath, err)
	}
	var oracle nodeProfileOracle
	if err := json.Unmarshal(bytes, &oracle); err != nil {
		t.Fatalf("decode model-check contract fixture: %v", err)
	}
	if oracle.DefaultModel != DefaultModel || oracle.DefaultProfile != DefaultProfile {
		t.Fatalf("defaults Go=%q/%q fixture=%q/%q", DefaultModel, DefaultProfile, oracle.DefaultModel, oracle.DefaultProfile)
	}
	profiles := Profiles()
	if len(profiles) != len(oracle.Profiles) {
		t.Fatalf("profile count Go=%d Node=%d", len(profiles), len(oracle.Profiles))
	}
	for index, expected := range oracle.Profiles {
		actual := profiles[index]
		families := SourceEndpointFamilies(actual)
		actualFamilies := make([]string, len(families))
		for familyIndex, family := range families {
			actualFamilies[familyIndex] = string(family)
		}
		if actual.ProviderCode != expected.ProviderCode || !reflect.DeepEqual(actual.ProviderProtocolProfileIDs, expected.ProfileIDs) || !reflect.DeepEqual(actual.Models, expected.Models) || !reflect.DeepEqual(actualFamilies, expected.SourceEndpointFamilies) {
			t.Fatalf("profile[%d] Go=%#v families=%v Node=%#v", index, actual, actualFamilies, expected)
		}
	}
}

func TestCatalogMatchesFrozenNodeOracle(t *testing.T) {
	profiles := Profiles()
	if len(profiles) != 9 {
		t.Fatalf("profile count=%d", len(profiles))
	}
	checks := []struct {
		provider, id, protocol string
		models                 int
		family                 EndpointFamily
	}{
		{"gpt", "profile_gpt_openai_v1", string(ProtocolOpenAIResponses), 5, EndpointResponses},
		{"openai", "profile_openai_openai_v1", string(ProtocolOpenAIResponses), 5, EndpointResponses},
		{"deepseek", "profile_deepseek_openai_v1", string(ProtocolOpenAIChat), 3, EndpointChatCompletions},
		{"deepseek", "profile_deepseek_anthropic_v1", string(ProtocolAnthropic), 3, EndpointMessages},
		{"glm", "profile_glm_general_openai_v1", string(ProtocolOpenAIChat), 2, EndpointChatCompletions},
		{"glm", "profile_glm_coding_anthropic_v1", string(ProtocolAnthropic), 2, EndpointMessages},
		{"anthropic", "profile_anthropic_anthropic_v1", string(ProtocolAnthropic), 2, EndpointMessages},
		{"gemini", "profile_gemini_native_v1beta", string(ProtocolGeminiNative), 2, EndpointGenerateContent},
		{"gemini", "profile_gemini_openai_chat_v1beta", string(ProtocolOpenAIChat), 2, EndpointChatCompletions},
	}
	for _, check := range checks {
		profile, ok := Find(check.provider, check.id)
		if !ok || string(profile.Protocol) != check.protocol || len(profile.Models) != check.models {
			t.Fatalf("lookup mismatch for %s/%s: %#v ok=%t", check.provider, check.id, profile, ok)
		}
		families := SourceEndpointFamilies(profile)
		if len(families) == 0 || families[0] != check.family {
			t.Fatalf("families for %s/%s=%v", check.provider, check.id, families)
		}
	}
	if got := PairedModel(mustFind(t, "gpt", "profile_gpt_openai_v1"), "gpt-5.6-luna"); got != "gpt-5.6-terra" {
		t.Fatalf("luna pair=%q", got)
	}
	anthropic := mustFind(t, "anthropic", "profile_anthropic_anthropic_v1")
	if anthropic.DefaultModel != "claude-opus-5" || anthropic.Models[0] != "claude-opus-5" || anthropic.Models[1] != "claude-opus-4-8" {
		t.Fatalf("Anthropic Node oracle drift: %#v", anthropic)
	}
	if got := PairedModel(anthropic, "claude-opus-5"); got != "claude-opus-4-8" {
		t.Fatalf("Anthropic pair=%q", got)
	}
	if _, ok := FindForModel("anthropic", "profile_anthropic_anthropic_v1", "claude-opus-4-7"); ok {
		t.Fatal("retired Anthropic model must not remain in the Go catalog")
	}
	if got := SupportedModels(); len(got) != 14 || got[0] != "gpt-5.6-sol" {
		t.Fatalf("supported models=%v", got)
	}
}

func TestCatalogResultsAreDefensiveCopies(t *testing.T) {
	profiles := Profiles()
	profiles[0].Models[0] = "mutated"
	profile, ok := Find("gpt", "profile_gpt_openai_v1")
	if !ok || profile.Models[0] != "gpt-5.6-sol" {
		t.Fatalf("catalog mutated through caller slice: %#v", profile)
	}
}

func mustFind(t *testing.T, provider, id string) ProtocolProfile {
	t.Helper()
	profile, ok := Find(provider, id)
	if !ok {
		t.Fatalf("missing %s/%s", provider, id)
	}
	return profile
}
