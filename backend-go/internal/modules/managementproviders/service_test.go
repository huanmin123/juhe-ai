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
				ID:                       "provider_gpt",
				Code:                     "gpt",
				Name:                     "GPT",
				Enabled:                  true,
				DefaultProtocolProfileID: "profile_gpt_openai_v1",
				ProtocolCode:             "openai",
				ProtocolVersion:          "v1",
				BaseURL:                  "https://api.openai.com/v1",
				DefaultTestModel:         "gpt-5.5",
				DefaultSupportedModels:   []string{"gpt-5.5"},
				AccountTypes:             []string{"oauth", "api_key"},
				Capabilities:             []string{"responses", "chat"},
				ProtocolProfiles: []port.ManagementProviderProtocolProfile{
					{
						ID:               "profile_gpt_openai_v1",
						ProviderCode:     "gpt",
						Name:             "GPT / OpenAI v1",
						Enabled:          true,
						ProtocolCode:     "openai",
						ProtocolVersion:  "v1",
						BaseURL:          "https://api.openai.com/v1",
						DefaultTestModel: "gpt-5.5",
						AccountTypes:     []string{"oauth", "api_key"},
						Capabilities:     []string{"responses", "chat"},
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
	if got.Code != "gpt" || got.DefaultProtocolProfileID != "profile_gpt_openai_v1" || got.DefaultTestModel != "gpt-5.5" {
		t.Fatalf("provider = %+v", got)
	}
	if len(got.ProtocolProfiles) != 1 || got.ProtocolProfiles[0].EndpointFamilies[0].Code != "chat_completions" {
		t.Fatalf("profiles = %+v", got.ProtocolProfiles)
	}
}

type providerOptionStoreStub struct {
	input   port.ManagementProviderOptionListInput
	options []port.ManagementProviderOption
	err     error
}

func (s *providerOptionStoreStub) ListManagementProviderOptions(_ context.Context, input port.ManagementProviderOptionListInput) ([]port.ManagementProviderOption, error) {
	s.input = input
	return s.options, s.err
}
