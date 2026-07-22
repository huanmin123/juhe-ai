package gatewayclientcatalog

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

func TestServicePublicUsesEnabledRealProviders(t *testing.T) {
	reader := &catalogReaderStub{
		providers: []port.GatewayClientCatalogProvider{
			{Code: " gpt ", Enabled: true},
			{Code: "glm", Enabled: false},
			{Code: "HYBRID", Enabled: true},
			{Code: "gemini", Enabled: true},
			{Code: "gpt", Enabled: true},
		},
		models: []port.GatewayClientCatalogModel{pricedModel("gpt", "gpt-5.6")},
	}

	got, err := NewService(reader).Public(context.Background())
	if err != nil {
		t.Fatalf("public catalog: %v", err)
	}
	if len(got) != 1 || got[0].Model != "gpt-5.6" {
		t.Fatalf("models = %+v", got)
	}
	wantInput := port.GatewayClientCatalogModelListInput{LogicalProviderCodes: []string{"gemini", "gpt"}}
	if !reflect.DeepEqual(reader.modelInputs, []port.GatewayClientCatalogModelListInput{wantInput}) {
		t.Fatalf("model inputs = %#v, want %#v", reader.modelInputs, []port.GatewayClientCatalogModelListInput{wantInput})
	}
}

func TestServiceAPIKeyUsesOnlyActiveBindings(t *testing.T) {
	reader := &catalogReaderStub{models: []port.GatewayClientCatalogModel{pricedModel("gpt", "gpt-5.6")}}
	bindings := []Binding{
		{ProviderCode: " gpt ", Status: "active"},
		{ProviderCode: "gemini", Status: "disabled"},
		{ProviderCode: "GPT", Status: "ACTIVE"},
		{ProviderCode: "anthropic", Status: "active"},
		{ProviderCode: "hybrid", Status: "active"},
		{ProviderCode: "", Status: "active"},
	}

	_, err := NewService(reader).APIKey(context.Background(), APIKeyInput{
		SystemAccountID: " sys_owner ",
		Bindings:        bindings,
	})
	if err != nil {
		t.Fatalf("api key catalog: %v", err)
	}
	wantInput := port.GatewayClientCatalogModelListInput{
		LogicalProviderCodes: []string{"anthropic", "gpt", "hybrid"},
		SystemAccountID:      "sys_owner",
	}
	if !reflect.DeepEqual(reader.modelInputs, []port.GatewayClientCatalogModelListInput{wantInput}) {
		t.Fatalf("model inputs = %#v, want %#v", reader.modelInputs, []port.GatewayClientCatalogModelListInput{wantInput})
	}
	if reader.providerCalls != 0 {
		t.Fatalf("api key catalog unexpectedly listed providers %d times", reader.providerCalls)
	}
}

func TestServiceKeepsModelsResolvedFromLogicalProviderBindings(t *testing.T) {
	reader := &catalogReaderStub{models: []port.GatewayClientCatalogModel{
		func() port.GatewayClientCatalogModel {
			item := pricedModel("gpt", "gpt-from-hybrid")
			item.RequestedProviderCode = "hybrid"
			return item
		}(),
		func() port.GatewayClientCatalogModel {
			item := pricedModel("anthropic", "claude-from-hybrid")
			item.RequestedProviderCode = "hybrid"
			return item
		}(),
		func() port.GatewayClientCatalogModel {
			item := pricedModel("gpt", "gpt-from-openai")
			item.RequestedProviderCode = "openai"
			return item
		}(),
	}}

	hybrid, err := NewService(reader).APIKey(context.Background(), APIKeyInput{
		SystemAccountID: "sys_owner",
		Bindings:        []Binding{{ProviderCode: "hybrid", Status: "active"}},
	})
	if err != nil {
		t.Fatalf("hybrid api key catalog: %v", err)
	}
	if got := modelIDs(hybrid); !reflect.DeepEqual(got, []string{"claude-from-hybrid", "gpt-from-hybrid"}) {
		t.Fatalf("hybrid models = %#v", got)
	}

	reader.modelInputs = nil
	openAI, err := NewService(reader).APIKey(context.Background(), APIKeyInput{
		SystemAccountID: "sys_owner",
		Bindings:        []Binding{{ProviderCode: "openai", Status: "active"}},
	})
	if err != nil {
		t.Fatalf("openai api key catalog: %v", err)
	}
	if got := modelIDs(openAI); !reflect.DeepEqual(got, []string{"gpt-from-openai"}) {
		t.Fatalf("openai logical models = %#v", got)
	}
}

func TestServiceAPIKeyWithNoActiveBindingsReturnsEmptyWithoutReadingModels(t *testing.T) {
	reader := &catalogReaderStub{modelsErr: errors.New("must not be called")}
	got, err := NewService(reader).APIKey(context.Background(), APIKeyInput{
		SystemAccountID: "sys_owner",
		Bindings: []Binding{
			{ProviderCode: "gpt", Status: "disabled"},
		},
	})
	if err != nil {
		t.Fatalf("api key catalog: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("models = %+v, want empty", got)
	}
	if len(reader.modelInputs) != 0 {
		t.Fatalf("model reader called with %#v", reader.modelInputs)
	}
}

func TestServiceDoesNotTrustReaderScope(t *testing.T) {
	reader := &catalogReaderStub{
		providers: []port.GatewayClientCatalogProvider{{Code: "gpt", Enabled: true}},
		models: []port.GatewayClientCatalogModel{
			pricedModel("gpt", "public-gpt"),
			func() port.GatewayClientCatalogModel {
				item := model("gpt", "personal-owner", "personal", "2026-01-01")
				item.SystemAccountID = "sys_owner"
				return item
			}(),
			func() port.GatewayClientCatalogModel {
				item := model("gpt", "personal-other", "personal", "2026-01-01")
				item.SystemAccountID = "sys_other"
				return item
			}(),
			pricedModel("anthropic", "unbound-provider"),
		},
	}

	public, err := NewService(reader).Public(context.Background())
	if err != nil {
		t.Fatalf("public catalog: %v", err)
	}
	if got := modelIDs(public); !reflect.DeepEqual(got, []string{"public-gpt"}) {
		t.Fatalf("public models = %#v", got)
	}

	apiKey, err := NewService(reader).APIKey(context.Background(), APIKeyInput{
		SystemAccountID: "sys_owner",
		Bindings:        []Binding{{ProviderCode: "gpt", Status: "active"}},
	})
	if err != nil {
		t.Fatalf("api key catalog: %v", err)
	}
	if got := modelIDs(apiKey); !reflect.DeepEqual(got, []string{"personal-owner", "public-gpt"}) {
		t.Fatalf("api key models = %#v", got)
	}
}

func TestSelectClientModelsFiltersAndPrefersPersonalGlobalBuiltIn(t *testing.T) {
	zero := 0.0
	items := []port.GatewayClientCatalogModel{
		model("gpt", "current-model", "built_in", "2026-07-22"),
		model("gpt", "old-model", "built_in", "2020-01-01"),
		model("gpt", "missing-date", "built_in", ""),
		func() port.GatewayClientCatalogModel {
			item := model("gpt", "hidden", "built_in", "2026-01-01")
			item.CatalogVisible = false
			return item
		}(),
		func() port.GatewayClientCatalogModel {
			item := model("gpt", "disabled", "built_in", "2026-01-01")
			item.Status = "disabled"
			return item
		}(),
		func() port.GatewayClientCatalogModel {
			item := model("gpt", "unpriced", "built_in", "2026-01-01")
			item.InputUSDPer1M = nil
			return item
		}(),
		func() port.GatewayClientCatalogModel {
			item := model("gpt", "free", "built_in", "2026-01-02")
			item.InputUSDPer1M = &zero
			return item
		}(),
		func() port.GatewayClientCatalogModel {
			item := model("gpt", "tier-priced", "built_in", "2026-01-03")
			item.InputUSDPer1M = nil
			item.ServiceTierPrices = map[string]port.GatewayClientCatalogPriceSet{"priority": {OutputUSDPer1M: float64Pointer(2)}}
			return item
		}(),
		func() port.GatewayClientCatalogModel {
			item := model("gpt", "tier-declared", "built_in", "2026-01-04")
			item.InputUSDPer1M = nil
			item.ServiceTierPrices = map[string]port.GatewayClientCatalogPriceSet{"priority": {}}
			return item
		}(),
		model("gpt", "shared-model", "built_in", "2026-07-01"),
		model("glm", "shared-model", "global", "2025-01-01"),
		model("gemini", "shared-model", "personal", "2024-01-01"),
		model("gpt", "CaseModel", "built_in", "2026-01-01"),
		model("gpt", "casemodel", "built_in", "2026-01-01"),
	}

	got := SelectClientModels(items)
	byID := make(map[string]port.GatewayClientCatalogModel, len(got))
	for _, item := range got {
		byID[item.Model] = item
	}
	for _, id := range []string{"current-model", "old-model", "missing-date", "free", "tier-priced", "tier-declared", "CaseModel", "casemodel"} {
		if _, ok := byID[id]; !ok {
			t.Errorf("missing %q from %+v", id, got)
		}
	}
	for _, id := range []string{"hidden", "disabled", "unpriced"} {
		if _, ok := byID[id]; ok {
			t.Errorf("unexpected %q in %+v", id, got)
		}
	}
	shared := byID["shared-model"]
	if shared.Scope != "personal" || shared.ProviderCode != "gemini" {
		t.Fatalf("shared model = %+v, want personal gemini", shared)
	}
	count := 0
	for _, item := range got {
		if item.Model == "shared-model" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("shared model count = %d", count)
	}
}

func TestServiceReturnsReaderErrors(t *testing.T) {
	providerErr := errors.New("provider read failed")
	if _, err := NewService(&catalogReaderStub{providersErr: providerErr}).Public(context.Background()); !errors.Is(err, providerErr) {
		t.Fatalf("public error = %v, want %v", err, providerErr)
	}

	modelErr := errors.New("model read failed")
	reader := &catalogReaderStub{
		providers: []port.GatewayClientCatalogProvider{{Code: "gpt", Enabled: true}},
		modelsErr: modelErr,
	}
	if _, err := NewService(reader).Public(context.Background()); !errors.Is(err, modelErr) {
		t.Fatalf("model error = %v, want %v", err, modelErr)
	}
}

func TestResolveModelsResponseProtocol(t *testing.T) {
	tests := []struct {
		name  string
		input ModelsProtocolInput
		want  ModelsResponseProtocol
		ok    bool
	}{
		{name: "plain root models defaults openai", input: ModelsProtocolInput{PathAndQuery: "/models"}, want: ModelsProtocolOpenAI, ok: true},
		{name: "v1 models defaults openai", input: ModelsProtocolInput{PathAndQuery: "/v1/models"}, want: ModelsProtocolOpenAI, ok: true},
		{name: "v1beta models is gemini", input: ModelsProtocolInput{PathAndQuery: "/v1beta/models"}, want: ModelsProtocolGemini, ok: true},
		{name: "explicit gemini profile", input: ModelsProtocolInput{PathAndQuery: "/models", ExplicitProfile: "generic-gemini"}, want: ModelsProtocolGemini, ok: true},
		{name: "explicit anthropic profile", input: ModelsProtocolInput{PathAndQuery: "/models", ExplicitProfile: "claude-code"}, want: ModelsProtocolAnthropic, ok: true},
		{name: "anthropic profile applies to v1 models", input: ModelsProtocolInput{PathAndQuery: "/v1/models", ExplicitProfile: "generic_anthropic"}, want: ModelsProtocolAnthropic, ok: true},
		{name: "gemini query key", input: ModelsProtocolInput{PathAndQuery: "/models?key=test-key"}, want: ModelsProtocolGemini, ok: true},
		{name: "empty gemini query key", input: ModelsProtocolInput{PathAndQuery: "/models?key="}, want: ModelsProtocolOpenAI, ok: true},
		{name: "gemini api key signal", input: ModelsProtocolInput{PathAndQuery: "/models", HasGeminiAPIKey: true}, want: ModelsProtocolGemini, ok: true},
		{name: "gemini cli bare user agent", input: ModelsProtocolInput{PathAndQuery: "/models", UserAgent: "GeminiCLI"}, want: ModelsProtocolGemini, ok: true},
		{name: "gemini cli proxy user agent", input: ModelsProtocolInput{PathAndQuery: "/models", UserAgent: "sdk proxy_client=geminicli"}, want: ModelsProtocolGemini, ok: true},
		{name: "gemini cli proxy prefix is not enough", input: ModelsProtocolInput{PathAndQuery: "/models", UserAgent: "sdk proxy_client=geminiclievil"}, want: ModelsProtocolOpenAI, ok: true},
		{name: "anthropic version signal", input: ModelsProtocolInput{PathAndQuery: "/models", HasAnthropicVersion: true}, want: ModelsProtocolAnthropic, ok: true},
		{name: "codex client version", input: ModelsProtocolInput{PathAndQuery: "/models", HasCodexClientVersion: true}, want: ModelsProtocolCodex, ok: true},
		{name: "codex client version query", input: ModelsProtocolInput{PathAndQuery: "/models?client_version=0.144.4"}, want: ModelsProtocolCodex, ok: true},
		{name: "codex originator", input: ModelsProtocolInput{PathAndQuery: "/v1/models", Originator: "codex_cli_rs"}, want: ModelsProtocolCodex, ok: true},
		{name: "post models is not discovery", input: ModelsProtocolInput{Method: "POST", PathAndQuery: "/models"}, ok: false},
		{name: "unrelated path", input: ModelsProtocolInput{PathAndQuery: "/not-models", ExplicitProfile: "gemini"}, ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.input.Method == "" {
				tt.input.Method = "GET"
			}
			got, ok := ResolveModelsResponseProtocol(tt.input)
			if got != tt.want || ok != tt.ok {
				t.Fatalf("ResolveModelsResponseProtocol() = (%q, %v), want (%q, %v)", got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestBuildModelsResponseUsesNativeShape(t *testing.T) {
	createdAt := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	items := []port.GatewayClientCatalogModel{
		{
			ProviderCode:          "gemini",
			Model:                 "gemini-3",
			Scope:                 "built_in",
			ReleaseDate:           "2026-01-02",
			CreatedAt:             createdAt,
			ContextWindowTokens:   intPointer(1_000_000),
			MaxOutputTokens:       intPointer(8192),
			CapabilityNotes:       "native model",
			SupportedAPIProtocols: []string{"generate_content", "count_tokens"},
		},
		{ProviderCode: "gpt", Model: "custom-model", Scope: "personal", CreatedAt: createdAt},
	}

	openAIPayload, err := BuildModelsResponse(ModelsProtocolOpenAI, items)
	if err != nil {
		t.Fatalf("build openai: %v", err)
	}
	openAI, ok := openAIPayload.(*OpenAIModelsResponse)
	if !ok || openAI.Object != "list" || openAI.Data[0].OwnedBy != "openai" || openAI.Data[1].OwnedBy != "juhe-ai" {
		t.Fatalf("openai response = %#v", openAIPayload)
	}
	assertJSONEqual(t, openAIPayload, `{"object":"list","data":[{"id":"gemini-3","object":"model","created":1767312000,"owned_by":"openai"},{"id":"custom-model","object":"model","created":1735787045,"owned_by":"juhe-ai"}]}`)

	anthropicPayload, err := BuildModelsResponse(ModelsProtocolAnthropic, items)
	if err != nil {
		t.Fatalf("build anthropic: %v", err)
	}
	anthropic, ok := anthropicPayload.(*AnthropicModelsResponse)
	if !ok || stringPointerValue(anthropic.FirstID) != "gemini-3" || stringPointerValue(anthropic.LastID) != "custom-model" || anthropic.Data[0].CreatedAt != "2026-01-02T00:00:00Z" {
		t.Fatalf("anthropic response = %#v", anthropicPayload)
	}
	emptyAnthropicPayload, err := BuildModelsResponse(ModelsProtocolAnthropic, nil)
	emptyAnthropic, ok := emptyAnthropicPayload.(*AnthropicModelsResponse)
	if err != nil || !ok || emptyAnthropic.FirstID != nil || emptyAnthropic.LastID != nil {
		t.Fatalf("empty anthropic response = %#v, err = %v", emptyAnthropicPayload, err)
	}
	assertJSONEqual(t, emptyAnthropicPayload, `{"data":[],"has_more":false,"first_id":null,"last_id":null}`)

	geminiPayload, err := BuildModelsResponse(ModelsProtocolGemini, items)
	if err != nil {
		t.Fatalf("build gemini: %v", err)
	}
	gemini, ok := geminiPayload.(*GeminiModelsResponse)
	if !ok || gemini.Models[0].Name != "models/gemini-3" || gemini.Models[0].InputTokenLimit != 1_000_000 {
		t.Fatalf("gemini response = %#v", geminiPayload)
	}
	wantMethods := []string{"generateContent", "countTokens"}
	if !reflect.DeepEqual(gemini.Models[0].SupportedGenerationMethods, wantMethods) {
		t.Fatalf("methods = %#v, want %#v", gemini.Models[0].SupportedGenerationMethods, wantMethods)
	}
}

func TestBuildCodexModelsResponse(t *testing.T) {
	items := []port.GatewayClientCatalogModel{{
		ProviderCode:                  "gpt",
		Model:                         "gpt-5.6-sol",
		Scope:                         "built_in",
		ContextWindowTokens:           intPointer(400_000),
		CapabilityNotes:               "coding model",
		SupportedServiceTiers:         []string{"priority", "flex"},
		CodexSupportedReasoningLevels: []string{"low", "xhigh"},
		CodexDefaultReasoningLevel:    "xhigh",
		CodexMultiAgentVersion:        "v2",
	}}
	payload, err := BuildModelsResponse(ModelsProtocolCodex, items)
	if err != nil {
		t.Fatalf("build codex: %v", err)
	}
	response, ok := payload.(*CodexModelsResponse)
	if !ok || len(response.Models) != 1 {
		t.Fatalf("codex payload = %#v", payload)
	}
	model := response.Models[0]
	if model.Slug != "gpt-5.6-sol" || model.ContextWindow != 400_000 || !model.UseResponsesLite || stringPointerValue(model.DefaultReasoningLevel) != "xhigh" || stringPointerValue(model.MultiAgentVersion) != "v2" {
		t.Fatalf("codex model = %+v", model)
	}
	if !reflect.DeepEqual(model.AdditionalSpeedTiers, []string{"fast"}) || len(model.ServiceTiers) != 2 {
		t.Fatalf("codex tiers = %+v / %+v", model.AdditionalSpeedTiers, model.ServiceTiers)
	}
	assertJSONHasOnlyTopLevelKey(t, payload, "models")
}

type catalogReaderStub struct {
	providers     []port.GatewayClientCatalogProvider
	providersErr  error
	models        []port.GatewayClientCatalogModel
	modelsErr     error
	providerCalls int
	modelInputs   []port.GatewayClientCatalogModelListInput
}

func (s *catalogReaderStub) ListGatewayClientCatalogProviders(context.Context) ([]port.GatewayClientCatalogProvider, error) {
	s.providerCalls++
	return append([]port.GatewayClientCatalogProvider(nil), s.providers...), s.providersErr
}

func (s *catalogReaderStub) ListGatewayClientCatalogModels(_ context.Context, input port.GatewayClientCatalogModelListInput) ([]port.GatewayClientCatalogModel, error) {
	input.LogicalProviderCodes = append([]string(nil), input.LogicalProviderCodes...)
	s.modelInputs = append(s.modelInputs, input)
	return append([]port.GatewayClientCatalogModel(nil), s.models...), s.modelsErr
}

func pricedModel(providerCode, modelID string) port.GatewayClientCatalogModel {
	return model(providerCode, modelID, "built_in", "2026-01-01")
}

func model(providerCode, modelID, scope, releaseDate string) port.GatewayClientCatalogModel {
	return port.GatewayClientCatalogModel{
		ProviderCode:   providerCode,
		Model:          modelID,
		Scope:          scope,
		Status:         "active",
		CatalogVisible: true,
		ReleaseDate:    releaseDate,
		InputUSDPer1M:  float64Pointer(1),
	}
}

func float64Pointer(value float64) *float64 { return &value }
func intPointer(value int) *int             { return &value }

func modelIDs(items []port.GatewayClientCatalogModel) []string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.Model)
	}
	return ids
}

func stringPointerValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func assertJSONEqual(t *testing.T, got any, want string) {
	t.Helper()
	gotJSON, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	var gotValue any
	var wantValue any
	if err := json.Unmarshal(gotJSON, &gotValue); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if err := json.Unmarshal([]byte(want), &wantValue); err != nil {
		t.Fatalf("decode expected response: %v", err)
	}
	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Fatalf("response json = %s, want %s", gotJSON, want)
	}
}

func assertJSONHasOnlyTopLevelKey(t *testing.T, got any, key string) {
	t.Helper()
	payload, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(payload, &object); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(object) != 1 || object[key] == nil {
		t.Fatalf("response keys = %#v, want only %q; payload=%s", object, key, payload)
	}
}
