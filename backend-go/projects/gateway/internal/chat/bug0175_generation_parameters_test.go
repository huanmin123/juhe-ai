package chat

import (
	"testing"
)

// BUG-0175 D-185 regressions: the generation-parameter capability chain.
// GET models fills ChatModelOption.GenerationParameters (flatten), the route
// constraint intersects the per-account capabilities, and the 422 boundary
// rejects only parameters outside the (constrained) capability list.

func f64Ptr(value float64) *float64 { return &value }

func TestBuildChatModelOptionsFillsGenerationParameters(t *testing.T) {
	maxTokens := int64(20000)
	catalog := []ProviderModelCatalogItem{
		{
			Model: "gpt-4o", ProviderCode: "gpt",
			SupportedAPIProtocols: []string{"chat_completions", "responses"},
			MaxOutputTokens:       &maxTokens,
		},
		{
			Model: "gpt-5", ProviderCode: "gpt",
			SupportedAPIProtocols: []string{"chat_completions", "responses"},
		},
		{
			Model: "glm-4x", ProviderCode: "glm",
			SupportedAPIProtocols: []string{"chat_completions"},
		},
	}
	options := buildChatModelOptions([]string{"gpt-4o", "gpt-5", "glm-4x"}, catalog)
	byModel := map[string]*ChatModelOption{}
	for _, option := range options {
		byModel[option.ID] = option
	}

	// gpt-4o: chat ∩ responses = {temperature, topP, maxOutputTokens}; the
	// maxOutputTokens range rides the catalog cap (20k, default min(4096, 20k)).
	gpt4o := byModel["gpt-4o"]
	if gpt4o == nil {
		t.Fatal("gpt-4o option missing")
	}
	parameters := map[string]ChatGenerationParameterCapability{}
	for _, capability := range gpt4o.GenerationParameters {
		parameters[capability.Parameter] = capability
	}
	if len(parameters) != 3 {
		t.Fatalf("gpt-4o parameters = %v", gpt4o.GenerationParameters)
	}
	temperature, ok := parameters["temperature"]
	if !ok || temperature.Min != 0 || temperature.Max != 2 || temperature.DefaultValue != 1 {
		t.Fatalf("temperature capability = %v", temperature)
	}
	topP, ok := parameters["topP"]
	if !ok || topP.Min != 0 || topP.Max != 1 || topP.DefaultValue != 1 {
		t.Fatalf("topP capability = %v", topP)
	}
	maxOutput, ok := parameters["maxOutputTokens"]
	if !ok || maxOutput.Min != 1 || maxOutput.Max != 20000 || maxOutput.DefaultValue != 4096 {
		t.Fatalf("maxOutputTokens capability = %v", maxOutput)
	}

	// gpt-5: chat {frequencyPenalty,presencePenalty,maxOutputTokens,seed} ∩
	// responses {maxOutputTokens} = {maxOutputTokens}.
	gpt5 := byModel["gpt-5"]
	if gpt5 == nil || len(gpt5.GenerationParameters) != 1 || gpt5.GenerationParameters[0].Parameter != "maxOutputTokens" {
		t.Fatalf("gpt-5 parameters = %v", gpt5Generation(gpt5))
	}

	// glm: temperature max 1, topP min 0.01 (the glm overrides survive).
	glm := byModel["glm-4x"]
	glmParameters := map[string]ChatGenerationParameterCapability{}
	for _, capability := range glm.GenerationParameters {
		glmParameters[capability.Parameter] = capability
	}
	if glmParameters["temperature"].Max != 1 || glmParameters["topP"].Min != 0.01 {
		t.Fatalf("glm parameters = %v", glm.GenerationParameters)
	}
}

func gpt5Generation(option *ChatModelOption) []ChatGenerationParameterCapability {
	if option == nil {
		return nil
	}
	return option.GenerationParameters
}

func TestResolveChatModelRequestOptionsAcceptsSupportedParameters(t *testing.T) {
	rt := newChatRoutesForTest(&Deps{})
	option := &ChatModelOption{
		ID: "gpt-4o",
		GenerationParameters: []ChatGenerationParameterCapability{
			{Parameter: "temperature", Min: 0, Max: 2, DefaultValue: 1},
			{Parameter: "topP", Min: 0, Max: 1, DefaultValue: 1},
			{Parameter: "maxOutputTokens", Min: 1, Max: 128000, DefaultValue: 4096},
		},
	}
	body := &streamMessageBody{GenerationParameters: &ChatGenerationParameters{Temperature: f64Ptr(0.7), MaxOutputTokens: f64Ptr(4096)}}
	_, _, params, _, err := resolveChatModelRequestOptions(rt, option, body)
	if err != nil {
		t.Fatalf("supported parameters rejected: %v", err)
	}
	if params.Temperature == nil || *params.Temperature != 0.7 {
		t.Fatalf("parameters = %v", params)
	}

	// An unsupported parameter still renders the 422 capability error.
	body = &streamMessageBody{GenerationParameters: &ChatGenerationParameters{FrequencyPenalty: f64Ptr(0.5)}}
	_, _, _, _, err = resolveChatModelRequestOptions(rt, option, body)
	capabilityErr, ok := err.(*ModelCapabilityError)
	if !ok || capabilityErr.Message != "当前模型或路由不支持所选生成参数，请重新选择" {
		t.Fatalf("unsupported parameter err = %v", err)
	}

	// Out-of-range values keep the range copy.
	body = &streamMessageBody{GenerationParameters: &ChatGenerationParameters{Temperature: f64Ptr(5)}}
	_, _, _, _, err = resolveChatModelRequestOptions(rt, option, body)
	if _, ok := err.(*ModelCapabilityError); !ok {
		t.Fatalf("range err = %v", err)
	}
}

func TestConstrainChatModelOptionForAccountsIntersectsParameters(t *testing.T) {
	enabled := true
	option := &ChatModelOption{
		ID:                    "gpt-4o",
		SupportedAPIProtocols: []string{"chat_completions", "responses"},
		GenerationParameters: []ChatGenerationParameterCapability{
			{Parameter: "temperature", Min: 0, Max: 2, DefaultValue: 1},
			{Parameter: "topP", Min: 0, Max: 1, DefaultValue: 1},
			{Parameter: "maxOutputTokens", Min: 1, Max: 20000, DefaultValue: 4096},
		},
	}

	// A bridge mapping (chat_completions → upstream gpt-5 chat) keeps only the
	// bridge-preserved parameters the upstream model supports: maxOutputTokens.
	bridged := []ChatTransportAccount{{
		ID: "account-1", Type: "api_key", ProviderCode: "gpt",
		SupportedEndpointModes: []string{"chat_sse", "responses_sse"},
		ModelMappings: []ChatTransportModelMapping{{
			Enabled: &enabled, SourceModel: "gpt-4o", SourceEndpointFamily: "chat_completions",
			UpstreamModel: "gpt-5", UpstreamEndpointFamily: "chat_completions",
		}},
	}}
	constrained := constrainChatModelOptionForAccounts(option, "gpt-4o", bridged, []ChatTransportProtocol{ProtocolChatCompletions})
	if len(constrained.SupportedAPIProtocols) != 1 || constrained.SupportedAPIProtocols[0] != "chat_completions" {
		t.Fatalf("supported protocols = %v", constrained.SupportedAPIProtocols)
	}
	if len(constrained.GenerationParameters) != 1 || constrained.GenerationParameters[0].Parameter != "maxOutputTokens" {
		t.Fatalf("bridged parameters = %v", constrained.GenerationParameters)
	}

	// A direct mapping (no upstream change) keeps the full capability list.
	direct := []ChatTransportAccount{{
		ID: "account-2", Type: "api_key", ProviderCode: "gpt",
		SupportedEndpointModes: []string{"chat_sse"},
		ModelMappings: []ChatTransportModelMapping{{
			Enabled: &enabled, SourceModel: "gpt-4o", SourceEndpointFamily: "chat_completions",
			UpstreamModel: "gpt-4o", UpstreamEndpointFamily: "chat_completions",
		}},
	}}
	constrained = constrainChatModelOptionForAccounts(option, "gpt-4o", direct, []ChatTransportProtocol{ProtocolChatCompletions})
	if len(constrained.GenerationParameters) != 3 {
		t.Fatalf("direct parameters = %v", constrained.GenerationParameters)
	}

	// A gpt oauth account carries no sampling capabilities: the constrained
	// list collapses and the 422 boundary rejects every parameter.
	oauthAccounts := []ChatTransportAccount{{
		ID: "account-3", Type: "oauth", ProviderCode: "gpt",
		SupportedEndpointModes: []string{"chat_sse"},
		ModelMappings: []ChatTransportModelMapping{{
			Enabled: &enabled, SourceModel: "gpt-4o", SourceEndpointFamily: "chat_completions",
		}},
	}}
	constrained = constrainChatModelOptionForAccounts(option, "gpt-4o", oauthAccounts, []ChatTransportProtocol{ProtocolChatCompletions})
	if len(constrained.GenerationParameters) != 0 {
		t.Fatalf("oauth parameters = %v", constrained.GenerationParameters)
	}
	rt := newChatRoutesForTest(&Deps{})
	body := &streamMessageBody{GenerationParameters: &ChatGenerationParameters{Temperature: f64Ptr(0.7)}}
	if _, _, _, _, err := resolveChatModelRequestOptions(rt, constrained, body); err == nil {
		t.Fatal("oauth route must reject generation parameters")
	}
}
