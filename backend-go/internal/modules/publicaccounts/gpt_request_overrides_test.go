package publicaccounts

import (
	"context"
	"errors"
	"strings"
	"testing"

	"juhe-ai/backend-go/internal/modules/managementprovidermodels"
	"juhe-ai/backend-go/internal/store/port"
)

func TestPreflightAccountRequestOverridesAcceptsGenericCapabilityTokens(t *testing.T) {
	serviceTier := strings.Repeat("a", 64)
	overrides, err := preflightAccountRequestOverrides(map[string]any{
		"service_tier_override":     serviceTier,
		"reasoning_effort_override": "Adaptive.V2_high",
	})
	if err != nil {
		t.Fatalf("preflight account request overrides: %v", err)
	}
	if overrides.serviceTier != serviceTier || overrides.reasoningEffort != "Adaptive.V2_high" {
		t.Fatalf("overrides = %+v, want generic capability tokens preserved", overrides)
	}
}

func TestPreflightAccountRequestOverridesRejectsMalformedCapabilityTokens(t *testing.T) {
	tests := []struct {
		name  string
		value any
	}{
		{name: "non string", value: true},
		{name: "leading punctuation", value: "_high"},
		{name: "leading space", value: " high"},
		{name: "trailing space", value: "high "},
		{name: "unsupported punctuation", value: "high/max"},
		{name: "non ascii", value: "高"},
		{name: "more than 64 characters", value: strings.Repeat("a", 65)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := preflightAccountRequestOverrides(map[string]any{
				"reasoning_effort_override": tt.value,
			})
			if !errors.Is(err, ErrInvalidCredentials) {
				t.Fatalf("preflight error = %v, want ErrInvalidCredentials", err)
			}
		})
	}
}

func TestValidateAccountRequestOverridesOAuthFlexRestrictionOnlyAppliesToGPT(t *testing.T) {
	credentials := map[string]any{"service_tier_override": "flex"}
	overrides, err := preflightAccountRequestOverrides(credentials)
	if err != nil {
		t.Fatalf("preflight OAuth Flex override: %v", err)
	}

	t.Run("GPT rejects", func(t *testing.T) {
		reader := providerModelReaderWithItems(requestOverrideModelItemForTest(
			"gpt",
			"gpt-flex-model",
			[]string{"flex"},
			nil,
		))
		service := newPublicAccountServiceForTest(newPublicAccountStoreFake(), reader)
		err := service.validateAccountRequestOverridesInProviderCatalog(
			context.Background(),
			"system_gpt",
			"gpt",
			"oauth",
			overrides,
			[]string{"gpt-flex-model"},
		)
		if !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("GPT OAuth validation error = %v, want ErrInvalidCredentials", err)
		}
		if !strings.Contains(err.Error(), "OpenAI OAuth 账户不支持 Flex") {
			t.Fatalf("GPT OAuth validation error = %q, want OAuth Flex message", err.Error())
		}
	})

	t.Run("OpenAI provider allows", func(t *testing.T) {
		reader := providerModelReaderWithItems(requestOverrideModelItemForTest(
			"openai",
			"openai-flex-model",
			[]string{"flex"},
			nil,
		))
		service := newPublicAccountServiceForTest(newPublicAccountStoreFake(), reader)
		if err := service.validateAccountRequestOverridesInProviderCatalog(
			context.Background(),
			"system_openai",
			"openai",
			"oauth",
			overrides,
			[]string{"openai-flex-model"},
		); err != nil {
			t.Fatalf("OpenAI provider OAuth Flex catalog validation: %v", err)
		}
	})
}

func TestValidateAccountRequestOverridesProviderWireBoundaries(t *testing.T) {
	t.Run("Gemini service tier", func(t *testing.T) {
		reader := providerModelReaderWithItems(requestOverrideModelItemForTest(
			"gemini",
			"gemini-model",
			[]string{"priority"},
			[]string{"low"},
		))
		service := newPublicAccountServiceForTest(newPublicAccountStoreFake(), reader)
		err := service.validateAccountRequestOverridesInProviderCatalog(
			context.Background(),
			"system_gemini",
			"gemini",
			AccountTypeAPIKey,
			accountRequestOverrides{serviceTier: "priority"},
			[]string{"gemini-model"},
		)
		if !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("Gemini service tier error = %v, want ErrInvalidCredentials", err)
		}
		if !strings.Contains(err.Error(), "Gemini 原生请求没有可确认的服务等级 wire 字段") {
			t.Fatalf("Gemini service tier error = %q, want wire mapping message", err.Error())
		}
		if reader.calls != 0 {
			t.Fatalf("provider model reader calls = %d, want 0", reader.calls)
		}
	})

	t.Run("unknown provider", func(t *testing.T) {
		reader := defaultProviderModelReaderStub()
		service := newPublicAccountServiceForTest(newPublicAccountStoreFake(), reader)
		err := service.validateAccountRequestOverridesInProviderCatalog(
			context.Background(),
			"system_deepseek",
			"deepseek",
			AccountTypeAPIKey,
			accountRequestOverrides{reasoningEffort: "high"},
			[]string{"deepseek-model"},
		)
		if !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("unknown provider error = %v, want ErrInvalidCredentials", err)
		}
		if !strings.Contains(err.Error(), "供应商 deepseek 没有可确认的账户请求覆盖 wire 映射") {
			t.Fatalf("unknown provider error = %q, want wire mapping message", err.Error())
		}
		if reader.calls != 0 {
			t.Fatalf("provider model reader calls = %d, want 0", reader.calls)
		}
	})
}

func TestValidateAccountRequestOverridesRequiresEverySupportedModelCapability(t *testing.T) {
	tests := []struct {
		name        string
		provider    string
		overrides   accountRequestOverrides
		catalog     []managementprovidermodels.ModelCatalogItem
		wantMessage string
	}{
		{
			name:      "service tier",
			provider:  "anthropic",
			overrides: accountRequestOverrides{serviceTier: "auto"},
			catalog: []managementprovidermodels.ModelCatalogItem{
				requestOverrideModelItemForTest("anthropic", "model-a", []string{"auto"}, nil),
				requestOverrideModelItemForTest("anthropic", "model-b", []string{"standard_only"}, nil),
			},
			wantMessage: "账户全部支持模型必须共同支持服务等级 auto",
		},
		{
			name:      "reasoning effort",
			provider:  "gemini",
			overrides: accountRequestOverrides{reasoningEffort: "high"},
			catalog: []managementprovidermodels.ModelCatalogItem{
				requestOverrideModelItemForTest("gemini", "model-a", nil, []string{"low", "high"}),
				requestOverrideModelItemForTest("gemini", "model-b", nil, []string{"low"}),
			},
			wantMessage: "账户全部支持模型必须共同支持思考级别 high",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reader := providerModelReaderWithItems(tt.catalog...)
			service := newPublicAccountServiceForTest(newPublicAccountStoreFake(), reader)
			err := service.validateAccountRequestOverridesInProviderCatalog(
				context.Background(),
				"system_intersection",
				tt.provider,
				AccountTypeAPIKey,
				tt.overrides,
				[]string{" model-a ", "model-a", "model-b"},
			)
			if !errors.Is(err, ErrInvalidSupportedModels) {
				t.Fatalf("catalog validation error = %v, want ErrInvalidSupportedModels", err)
			}
			if !strings.Contains(err.Error(), tt.wantMessage) {
				t.Fatalf("catalog validation error = %q, want message containing %q", err.Error(), tt.wantMessage)
			}
			if reader.calls != 1 {
				t.Fatalf("provider model reader calls = %d, want 1", reader.calls)
			}
		})
	}
}

func TestServiceUpdatePreservesSupportedNonGPTRequestOverrides(t *testing.T) {
	tests := []struct {
		name             string
		providerCode     string
		profileID        string
		protocolCode     string
		protocolVersion  string
		enabledModes     []string
		model            string
		serviceTiers     []string
		reasoningEfforts []string
		credentialFields map[string]any
	}{
		{
			name:             "OpenAI",
			providerCode:     "openai",
			profileID:        "profile_openai_openai_v1",
			protocolCode:     "openai",
			protocolVersion:  "v1",
			enabledModes:     []string{"chat_json", "chat_sse"},
			model:            "openai-model",
			serviceTiers:     []string{"priority"},
			reasoningEfforts: []string{"high"},
			credentialFields: map[string]any{
				"service_tier_override":     "priority",
				"reasoning_effort_override": "high",
			},
		},
		{
			name:             "Anthropic generic tier",
			providerCode:     "anthropic",
			profileID:        "profile_anthropic_messages_v1",
			protocolCode:     "anthropic",
			protocolVersion:  "v1",
			enabledModes:     []string{"messages_json", "messages_sse"},
			model:            "anthropic-model",
			serviceTiers:     []string{"auto"},
			reasoningEfforts: []string{"high"},
			credentialFields: map[string]any{
				"service_tier_override":     "auto",
				"reasoning_effort_override": "high",
			},
		},
		{
			name:             "Gemini reasoning effort",
			providerCode:     "gemini",
			profileID:        "profile_gemini_native_v1beta",
			protocolCode:     "gemini",
			protocolVersion:  "v1beta",
			enabledModes:     []string{"generate_content_json", "generate_content_sse"},
			model:            "gemini-model",
			reasoningEfforts: []string{"low", "high"},
			credentialFields: map[string]any{
				"reasoning_effort_override": "high",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newPublicAccountStoreFake()
			store.profiles[tt.providerCode+"|"+tt.profileID] = port.PublicAccountProviderProfile{
				ID:                      tt.profileID,
				ProviderCode:            tt.providerCode,
				Name:                    tt.name,
				Enabled:                 true,
				ProviderEnabled:         true,
				ProtocolCode:            tt.protocolCode,
				ProtocolVersion:         tt.protocolVersion,
				AccountTypesJSON:        `["api_key"]`,
				EnabledEndpointModes:    append([]string(nil), tt.enabledModes...),
				DefaultSupportedModels:  []string{tt.model},
				DefaultHealthCheckModel: tt.model,
			}
			reader := providerModelReaderWithItems(requestOverrideModelItemForTest(
				tt.providerCode,
				tt.model,
				tt.serviceTiers,
				tt.reasoningEfforts,
			))
			service := newPublicAccountServiceForTest(store, reader)
			input := validPublicAccountAddInput(tt.name+" 账户", tt.model)
			input.ProviderCode = tt.providerCode
			input.ProviderProtocolProfileID = tt.profileID
			created, err := service.Add(context.Background(), input)
			if err != nil {
				t.Fatalf("add public account: %v", err)
			}
			setPublicAccountCredentialFieldsForTest(
				t,
				service,
				store,
				created.Account.ID,
				tt.credentialFields,
			)

			reader.resetCalls()
			name := tt.name + " 账户改名"
			if _, err := service.Update(context.Background(), UpdateInput{
				AccountID: created.Account.ID,
				Name:      &name,
			}); err != nil {
				t.Fatalf("update public account: %v", err)
			}
			if reader.calls != 1 {
				t.Fatalf("provider model reader calls = %d, want 1", reader.calls)
			}
			if input := reader.inputs[0]; input.ProviderCode != tt.providerCode || !input.IncludeUnpriced {
				t.Fatalf("override capability query = %+v", input)
			}
			credentials, err := service.codec.DecryptJSON(store.accounts[created.Account.ID].CredentialsEncrypted)
			if err != nil {
				t.Fatalf("decrypt updated credentials: %v", err)
			}
			for key, want := range tt.credentialFields {
				if got := credentials[key]; got != want {
					t.Fatalf("credential %s = %#v, want %#v", key, got, want)
				}
			}
		})
	}
}

func requestOverrideModelItemForTest(
	providerCode string,
	model string,
	supportedServiceTiers []string,
	supportedReasoningEfforts []string,
) managementprovidermodels.ModelCatalogItem {
	return managementprovidermodels.ModelCatalogItem{
		ProviderCode:              providerCode,
		Model:                     model,
		Scope:                     "built_in",
		Status:                    "active",
		SupportedServiceTiers:     append([]string(nil), supportedServiceTiers...),
		SupportedReasoningEfforts: append([]string(nil), supportedReasoningEfforts...),
	}
}
