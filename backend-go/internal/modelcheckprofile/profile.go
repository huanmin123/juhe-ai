package modelcheckprofile

import (
	"strings"

	"juhe-ai/backend-go/internal/modules/gatewaymodelcapability"
)

const (
	DefaultModel   = "gpt-5.6-sol"
	DefaultProfile = "quick"
)

type Account struct {
	ProviderCode              string
	ProviderProtocolProfileID string
	ProtocolCode              string
	ProtocolVersion           string
	SupportedModels           []string
	ModelMappings             []ModelMapping
}

type ModelMapping = gatewaymodelcapability.ModelMapping
type EndpointFamily = gatewaymodelcapability.EndpointFamily

type profile struct {
	providerCode      string
	profileIDs        []string
	models            []string
	sourceEndpointIDs []gatewaymodelcapability.EndpointFamily
}

var profiles = [...]profile{
	{
		providerCode: "gpt", profileIDs: []string{"profile_gpt_openai_v1"},
		models:            []string{"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna", "gpt-5.5", "gpt-5.4"},
		sourceEndpointIDs: []gatewaymodelcapability.EndpointFamily{gatewaymodelcapability.EndpointFamilyResponses},
	},
	{
		providerCode: "openai", profileIDs: []string{"profile_openai_openai_v1"},
		models:            []string{"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna", "gpt-5.5", "gpt-5.4"},
		sourceEndpointIDs: []gatewaymodelcapability.EndpointFamily{gatewaymodelcapability.EndpointFamilyResponses},
	},
	{
		providerCode: "deepseek", profileIDs: []string{"profile_deepseek_openai_v1"},
		models:            []string{"deepseek-v4-flash", "deepseek-v4-pro"},
		sourceEndpointIDs: []gatewaymodelcapability.EndpointFamily{gatewaymodelcapability.EndpointFamilyChatCompletions},
	},
	{
		providerCode: "deepseek", profileIDs: []string{"profile_deepseek_anthropic_v1"},
		models:            []string{"deepseek-v4-flash", "deepseek-v4-pro"},
		sourceEndpointIDs: []gatewaymodelcapability.EndpointFamily{gatewaymodelcapability.EndpointFamilyMessages},
	},
	{
		providerCode: "glm", profileIDs: []string{"profile_glm_general_openai_v1", "profile_glm_coding_openai_v1"},
		models:            []string{"glm-5.2", "glm-5.1"},
		sourceEndpointIDs: []gatewaymodelcapability.EndpointFamily{gatewaymodelcapability.EndpointFamilyChatCompletions},
	},
	{
		providerCode: "glm", profileIDs: []string{"profile_glm_coding_anthropic_v1"},
		models:            []string{"glm-5.2", "glm-5.1"},
		sourceEndpointIDs: []gatewaymodelcapability.EndpointFamily{gatewaymodelcapability.EndpointFamilyMessages},
	},
	{
		providerCode: "anthropic", profileIDs: []string{"profile_anthropic_anthropic_v1"},
		models:            []string{"claude-opus-4-8", "claude-opus-4-7"},
		sourceEndpointIDs: []gatewaymodelcapability.EndpointFamily{gatewaymodelcapability.EndpointFamilyMessages},
	},
	{
		providerCode: "gemini", profileIDs: []string{"profile_gemini_native_v1beta"},
		models: []string{"gemini-3.5-flash", "gemini-3.1-pro-preview"},
		sourceEndpointIDs: []gatewaymodelcapability.EndpointFamily{
			gatewaymodelcapability.EndpointFamilyGenerateContent,
			gatewaymodelcapability.EndpointFamilyStreamGenerateContent,
		},
	},
	{
		providerCode: "gemini", profileIDs: []string{"profile_gemini_openai_chat_v1beta"},
		models:            []string{"gemini-3.5-flash", "gemini-3.1-pro-preview"},
		sourceEndpointIDs: []gatewaymodelcapability.EndpointFamily{gatewaymodelcapability.EndpointFamilyChatCompletions},
	},
}

// SupportedModels returns the same stable, de-duplicated order as Node's
// modelCheckProtocolProfiles.flatMap(profile.models).
func SupportedModels() []string {
	seen := make(map[string]struct{})
	models := make([]string, 0, 13)
	for _, item := range profiles {
		for _, model := range item.models {
			if _, exists := seen[model]; exists {
				continue
			}
			seen[model] = struct{}{}
			models = append(models, model)
		}
	}
	return models
}

// ConfiguredModels reproduces Node configuredModelCheckModelsForAccount.
// An empty configured model list falls back to every model in the matched
// model-check profile. A non-empty list requires either an exact direct model
// or a runtime-supported account mapping whose upstream model is configured.
func ConfiguredModels(account Account) []string {
	matched := findProfile(account.ProviderCode, account.ProviderProtocolProfileID)
	if matched == nil {
		return []string{}
	}

	supported := make([]string, 0, len(account.SupportedModels))
	for _, value := range account.SupportedModels {
		if model := strings.TrimSpace(value); model != "" {
			supported = append(supported, model)
		}
	}
	if len(supported) == 0 {
		return append([]string(nil), matched.models...)
	}

	candidate := gatewaymodelcapability.Candidate{
		ID:                        "model-check-account",
		ProviderCode:              account.ProviderCode,
		ProviderProtocolProfileID: account.ProviderProtocolProfileID,
		ProtocolCode:              account.ProtocolCode,
		ProtocolVersion:           account.ProtocolVersion,
		SupportedModels:           supported,
		ModelMappings:             append([]gatewaymodelcapability.ModelMapping(nil), account.ModelMappings...),
	}
	models := make([]string, 0, len(matched.models))
	for _, model := range matched.models {
		for _, family := range matched.sourceEndpointIDs {
			result := gatewaymodelcapability.FilterModelCandidates(
				[]gatewaymodelcapability.Candidate{candidate},
				model,
				family,
			)
			if len(result.Candidates) != 0 {
				models = append(models, model)
				break
			}
		}
	}
	return models
}

func findProfile(providerCode, profileID string) *profile {
	provider := normalizeToken(providerCode)
	profileToken := normalizeToken(profileID)
	for index := range profiles {
		item := &profiles[index]
		if normalizeToken(item.providerCode) != provider {
			continue
		}
		for _, candidate := range item.profileIDs {
			if normalizeToken(candidate) == profileToken {
				return item
			}
		}
	}
	return nil
}

func normalizeToken(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}
