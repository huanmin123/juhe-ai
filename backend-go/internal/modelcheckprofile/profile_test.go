package modelcheckprofile

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"juhe-ai/backend-go/internal/modules/gatewaymodelcapability"
)

func TestProfileCatalogMatchesSharedNodeContract(t *testing.T) {
	var contract struct {
		DefaultModel   string `json:"defaultModel"`
		DefaultProfile string `json:"defaultProfile"`
		Profiles       []struct {
			ProviderCode           string   `json:"providerCode"`
			ProfileIDs             []string `json:"profileIds"`
			Models                 []string `json:"models"`
			SourceEndpointFamilies []string `json:"sourceEndpointFamilies"`
		} `json:"profiles"`
	}
	raw, err := os.ReadFile("testdata/node-model-check-profile-contract.json")
	if err != nil {
		t.Fatalf("read shared Node contract: %v", err)
	}
	if err := json.Unmarshal(raw, &contract); err != nil {
		t.Fatalf("decode shared Node contract: %v", err)
	}
	if contract.DefaultModel != DefaultModel || contract.DefaultProfile != DefaultProfile {
		t.Fatalf("contract defaults = %q/%q, Go = %q/%q", contract.DefaultModel, contract.DefaultProfile, DefaultModel, DefaultProfile)
	}
	if len(contract.Profiles) != len(profiles) {
		t.Fatalf("contract profile count = %d, Go = %d", len(contract.Profiles), len(profiles))
	}
	for index, item := range profiles {
		gotFamilies := make([]string, 0, len(item.sourceEndpointIDs))
		for _, family := range item.sourceEndpointIDs {
			gotFamilies = append(gotFamilies, string(family))
		}
		want := contract.Profiles[index]
		if item.providerCode != want.ProviderCode ||
			!reflect.DeepEqual(item.profileIDs, want.ProfileIDs) ||
			!reflect.DeepEqual(item.models, want.Models) ||
			!reflect.DeepEqual(gotFamilies, want.SourceEndpointFamilies) {
			t.Fatalf("profile %d = %#v/%#v/%#v/%#v, contract = %#v", index, item.providerCode, item.profileIDs, item.models, gotFamilies, want)
		}
	}
}

func TestSupportedModelsAndDefaultsMatchNode(t *testing.T) {
	want := []string{
		"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna", "gpt-5.5", "gpt-5.4",
		"deepseek-v4-flash", "deepseek-v4-pro", "glm-5.2", "glm-5.1",
		"claude-opus-4-8", "claude-opus-4-7", "gemini-3.5-flash", "gemini-3.1-pro-preview",
	}
	if got := SupportedModels(); !reflect.DeepEqual(got, want) {
		t.Fatalf("supported models = %#v, want %#v", got, want)
	}
	if DefaultModel != want[0] || DefaultProfile != "quick" {
		t.Fatalf("defaults = %q/%q", DefaultModel, DefaultProfile)
	}
	got := SupportedModels()
	got[0] = "mutated"
	if SupportedModels()[0] != DefaultModel {
		t.Fatal("SupportedModels leaked caller mutation")
	}
}

func TestConfiguredModelsMatchesNodeProfileAndEmptyListSemantics(t *testing.T) {
	tests := []struct {
		name    string
		account Account
		want    []string
	}{
		{
			name:    "empty supported models inherit matched profile",
			account: Account{ProviderCode: " GPT ", ProviderProtocolProfileID: " PROFILE_GPT_OPENAI_V1 "},
			want:    []string{"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna", "gpt-5.5", "gpt-5.4"},
		},
		{
			name:    "non-empty list allows exact direct model only",
			account: Account{ProviderCode: "gpt", ProviderProtocolProfileID: "profile_gpt_openai_v1", SupportedModels: []string{" gpt-5.5 "}},
			want:    []string{"gpt-5.5"},
		},
		{
			name:    "provider and profile pair must both match",
			account: Account{ProviderCode: "openai", ProviderProtocolProfileID: "profile_gpt_openai_v1"},
			want:    []string{},
		},
		{
			name:    "unsupported provider profile is empty",
			account: Account{ProviderCode: "hybrid", ProviderProtocolProfileID: "profile_hybrid_openai_chat_v1"},
			want:    []string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ConfiguredModels(tt.account); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("configured models = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestConfiguredModelsUsesGatewayRuntimeMappingRules(t *testing.T) {
	base := Account{
		ProviderCode: "gpt", ProviderProtocolProfileID: "profile_gpt_openai_v1",
		ProtocolCode: "openai", ProtocolVersion: "v1",
		SupportedModels: []string{"upstream-model"},
		ModelMappings: []gatewaymodelcapability.ModelMapping{{
			SourceModel: "gpt-5.6-sol", SourceEndpointFamily: gatewaymodelcapability.EndpointFamilyResponses,
			UpstreamModel: "upstream-model", UpstreamEndpointFamily: gatewaymodelcapability.EndpointFamilyChatCompletions,
			Enabled: true,
		}},
	}
	if got := ConfiguredModels(base); !reflect.DeepEqual(got, []string{"gpt-5.6-sol"}) {
		t.Fatalf("openai responses mapping models = %#v", got)
	}

	missingUpstream := base
	missingUpstream.SupportedModels = []string{"different-upstream"}
	if got := ConfiguredModels(missingUpstream); len(got) != 0 {
		t.Fatalf("mapping without configured upstream was accepted: %#v", got)
	}

	disabled := base
	disabled.ModelMappings = append([]gatewaymodelcapability.ModelMapping(nil), base.ModelMappings...)
	disabled.ModelMappings[0].Enabled = false
	if got := ConfiguredModels(disabled); len(got) != 0 {
		t.Fatalf("disabled mapping was accepted: %#v", got)
	}

	wrongFamily := base
	wrongFamily.ModelMappings = append([]gatewaymodelcapability.ModelMapping(nil), base.ModelMappings...)
	wrongFamily.ModelMappings[0].SourceEndpointFamily = gatewaymodelcapability.EndpointFamilyChatCompletions
	if got := ConfiguredModels(wrongFamily); len(got) != 0 {
		t.Fatalf("wrong model-check source family was accepted: %#v", got)
	}

	unsupportedConversion := base
	unsupportedConversion.ProtocolCode = "anthropic"
	unsupportedConversion.ProtocolVersion = "v1"
	if got := ConfiguredModels(unsupportedConversion); len(got) != 0 {
		t.Fatalf("unsupported runtime conversion was accepted: %#v", got)
	}

	sameFamilyAlias := base
	sameFamilyAlias.ModelMappings = []gatewaymodelcapability.ModelMapping{{
		SourceModel: "gpt-5.6-sol", SourceEndpointFamily: gatewaymodelcapability.EndpointFamilyResponses,
		UpstreamModel: "upstream-model", UpstreamEndpointFamily: gatewaymodelcapability.EndpointFamilyResponses,
		Enabled: true,
	}}
	if got := ConfiguredModels(sameFamilyAlias); !reflect.DeepEqual(got, []string{"gpt-5.6-sol"}) {
		t.Fatalf("same-family alias models = %#v", got)
	}

	invalidFirst := base
	invalidFirst.ModelMappings = []gatewaymodelcapability.ModelMapping{
		{
			SourceModel: "gpt-5.6-sol", SourceEndpointFamily: gatewaymodelcapability.EndpointFamilyResponses,
			UpstreamModel: "gpt-5.6-sol", UpstreamEndpointFamily: gatewaymodelcapability.EndpointFamilyResponses,
			Enabled: true,
		},
		base.ModelMappings[0],
	}
	if got := ConfiguredModels(invalidFirst); len(got) != 0 {
		t.Fatalf("invalid first matching mapping was skipped instead of rejecting: %#v", got)
	}
}

func TestConfiguredModelsChecksBothGeminiNativeSourceFamilies(t *testing.T) {
	account := Account{
		ProviderCode: "gemini", ProviderProtocolProfileID: "profile_gemini_native_v1beta",
		ProtocolCode: "gemini", ProtocolVersion: "v1beta",
		SupportedModels: []string{"gemini-upstream"},
		ModelMappings: []gatewaymodelcapability.ModelMapping{{
			SourceModel: "gemini-3.5-flash", SourceEndpointFamily: gatewaymodelcapability.EndpointFamilyStreamGenerateContent,
			UpstreamModel: "gemini-upstream", UpstreamEndpointFamily: gatewaymodelcapability.EndpointFamilyGenerateContent,
			Enabled: true,
		}},
	}
	if got := ConfiguredModels(account); !reflect.DeepEqual(got, []string{"gemini-3.5-flash"}) {
		t.Fatalf("gemini stream mapping models = %#v", got)
	}
}
