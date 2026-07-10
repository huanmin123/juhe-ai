package managementproviders

import (
	"context"
	"testing"

	"juhe-ai/backend-go/internal/store/port"
)

func TestServiceOptionsMapsProviderDefinitions(t *testing.T) {
	store := &providerOptionStoreStub{
		options: []port.ManagementProviderOption{
			{
				ID:                            "provider_gpt",
				Code:                          "gpt",
				Name:                          "GPT",
				Enabled:                       true,
				DefaultProtocolProfileID:      "profile_gpt_openai_v1",
				ProtocolCode:                  "openai",
				ProtocolVersion:               "v1",
				BaseURL:                       "https://api.openai.com/v1",
				DefaultHealthCheckModel:       "gpt-5-user",
				SystemDefaultHealthCheckModel: "gpt-5-system",
				DefaultSupportedModels:        []string{"gpt-5.5"},
				AccountTypes:                  []string{"oauth", "api_key"},
				Capabilities:                  []string{"responses", "chat"},
				ProtocolProfiles: []port.ManagementProviderProtocolProfile{
					{
						ID:                      "profile_gpt_openai_v1",
						ProviderCode:            "gpt",
						Name:                    "GPT / OpenAI v1",
						Enabled:                 true,
						ProtocolCode:            "openai",
						ProtocolVersion:         "v1",
						BaseURL:                 "https://api.openai.com/v1",
						DefaultHealthCheckModel: "gpt-5-system",
						AccountTypes:            []string{"oauth", "api_key"},
						Capabilities:            []string{"responses", "chat"},
						EndpointFamilies: []port.ManagementProviderEndpointFamily{
							{Code: "chat_completions", Name: "Chat Completions"},
						},
					},
				},
			},
		},
	}
	service := NewService(store)

	options, err := service.Options(context.Background(), OptionListInput{SystemAccountID: " sys_admin "})
	if err != nil {
		t.Fatalf("Options() error = %v", err)
	}

	if store.input.SystemAccountID != "sys_admin" {
		t.Fatalf("store input = %+v", store.input)
	}
	if len(options) != 1 {
		t.Fatalf("options = %d, want 1", len(options))
	}
	got := options[0]
	if got.Code != "gpt" || got.DefaultProtocolProfileID != "profile_gpt_openai_v1" ||
		got.DefaultHealthCheckModel != "gpt-5-user" || got.SystemDefaultHealthCheckModel != "gpt-5-system" {
		t.Fatalf("provider = %+v", got)
	}
	if len(got.ProtocolProfiles) != 1 || got.ProtocolProfiles[0].DefaultHealthCheckModel != "gpt-5-system" ||
		got.ProtocolProfiles[0].EndpointFamilies[0].Code != "chat_completions" {
		t.Fatalf("profiles = %+v", got.ProtocolProfiles)
	}
}

func TestServiceListUsesAllProviders(t *testing.T) {
	store := &providerOptionStoreStub{
		providers: []port.ManagementProviderOption{
			{ID: "provider_disabled", Code: "disabled", Name: "Disabled", Enabled: false},
			{
				ID:                            "provider_gpt",
				Code:                          "gpt",
				Name:                          "GPT",
				Enabled:                       true,
				DefaultHealthCheckModel:       "gpt-5-user",
				SystemDefaultHealthCheckModel: "gpt-5-system",
				ProtocolProfiles: []port.ManagementProviderProtocolProfile{
					{ID: "profile_gpt_openai_v1", DefaultHealthCheckModel: "gpt-5-system"},
				},
			},
		},
	}
	service := NewService(store)

	providers, err := service.List(context.Background(), ListInput{SystemAccountID: " sys_admin "})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}

	if store.listInput.SystemAccountID != "sys_admin" {
		t.Fatalf("store list input = %+v", store.listInput)
	}
	if len(providers) != 2 || providers[0].Code != "disabled" || providers[0].Enabled {
		t.Fatalf("providers = %+v, want disabled provider preserved", providers)
	}
	if providers[1].DefaultHealthCheckModel != "gpt-5-user" ||
		providers[1].SystemDefaultHealthCheckModel != "gpt-5-system" ||
		providers[1].ProtocolProfiles[0].DefaultHealthCheckModel != "gpt-5-system" {
		t.Fatalf("provider contract = %+v", providers[1])
	}
}

type providerOptionStoreStub struct {
	listInput port.ManagementProviderListInput
	input     port.ManagementProviderOptionListInput
	providers []port.ManagementProviderOption
	options   []port.ManagementProviderOption
	err       error
}

func (s *providerOptionStoreStub) ListManagementProviders(_ context.Context, input port.ManagementProviderListInput) ([]port.ManagementProviderOption, error) {
	s.listInput = input
	return s.providers, s.err
}

func (s *providerOptionStoreStub) ListManagementProviderOptions(_ context.Context, input port.ManagementProviderOptionListInput) ([]port.ManagementProviderOption, error) {
	s.input = input
	return s.options, s.err
}
