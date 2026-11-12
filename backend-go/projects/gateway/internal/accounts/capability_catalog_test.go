package accounts

import (
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-platform/scenario"
)

func TestBuildCapabilityCatalogCoversRegisteredScenarios(t *testing.T) {
	catalog := BuildCapabilityCatalog()
	if len(catalog.Scenarios) != len(scenario.All()) {
		t.Fatalf("catalog scenarios = %d, want %d", len(catalog.Scenarios), len(scenario.All()))
	}
	seen := map[string]bool{}
	for _, entry := range catalog.Scenarios {
		if seen[entry.ScenarioType] {
			t.Fatalf("duplicate scenario %q", entry.ScenarioType)
		}
		seen[entry.ScenarioType] = true
		if entry.Label == "" || entry.DispatchClass == "" {
			t.Fatalf("scenario %q missing label/dispatch class: %+v", entry.ScenarioType, entry)
		}
		if len(entry.Providers) == 0 {
			t.Fatalf("scenario %q has no providers", entry.ScenarioType)
		}
		for _, provider := range entry.Providers {
			if provider.ProviderCode == "" || len(provider.Profiles) == 0 {
				t.Fatalf("scenario %q provider %+v has no profiles", entry.ScenarioType, provider)
			}
			for _, profile := range provider.Profiles {
				if profile.ProfileID == "" || profile.ConnectionPreset == "" {
					t.Fatalf("scenario %q provider %q profile identity incomplete: %+v", entry.ScenarioType, provider.ProviderCode, profile)
				}
				if len(profile.AccountTypes) == 0 || len(profile.AllowedEndpointModes) == 0 {
					t.Fatalf("scenario %q provider %q profile %q empty account types/modes", entry.ScenarioType, provider.ProviderCode, profile.ProfileID)
				}
			}
		}
	}
	if catalog.Scenarios[0].ScenarioType != string(scenario.TypeChat) {
		t.Fatalf("first scenario = %q, want chat", catalog.Scenarios[0].ScenarioType)
	}
}

func TestScenarioRegistryRejectsUnknown(t *testing.T) {
	if scenario.Valid("unknown") {
		t.Fatal("unknown scenario must be rejected")
	}
	if scenario.Valid(string(scenario.TypeEmbedding)) {
		t.Fatal("embedding is not registered until B7")
	}
	if !scenario.IsNonChat(string(scenario.TypeAudio)) {
		t.Fatal("audio must be non-chat")
	}
	if scenario.IsNonChat(string(scenario.TypeChat)) {
		t.Fatal("chat must not be non-chat")
	}
}
