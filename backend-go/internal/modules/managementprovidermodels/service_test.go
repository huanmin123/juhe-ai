package managementprovidermodels

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"slices"
	"strings"
	"testing"

	"juhe-ai/backend-go/internal/store/port"
)

func TestServiceModelOptionsUsesProtocolScopeAndDedupe(t *testing.T) {
	store := &providerModelStoreStub{
		protocolCodes: map[string][]string{
			"openai:v1": {"gpt"},
		},
		catalog: []port.ManagementProviderModelCatalogItem{
			{
				ProviderCode: "gpt", Model: "gpt-5.5", Scope: "built_in", Status: "active",
				SupportedAPIProtocols:     []string{"chat_completions"},
				SupportedServiceTiers:     []string{"priority", "priority"},
				SupportedReasoningEfforts: []string{"low"},
				DefaultReasoningEffort:    "ultra",
			},
			{
				ProviderCode: "gpt", Model: "gpt-5.5", Scope: "global", Status: "active",
				SupportedAPIProtocols:     []string{"responses"},
				SupportedServiceTiers:     []string{"flex", "priority"},
				SupportedReasoningEfforts: []string{"high"},
				DefaultReasoningEffort:    " max ",
			},
			{
				ProviderCode: "gpt", Model: "gpt-5.5", Scope: "personal", Status: "active",
				SupportedReasoningEfforts: []string{"max", "high"},
				DefaultReasoningEffort:    "low",
			},
		},
	}
	service := NewService(store)

	options, err := service.ModelOptions(context.Background(), ModelOptionListInput{
		SystemAccountID: " sys_user ",
		Protocol:        "openai",
	})
	if err != nil {
		t.Fatalf("ModelOptions() error = %v", err)
	}

	if len(options) != 1 {
		t.Fatalf("options = %+v, want 1 deduped item", options)
	}
	if options[0].ProviderCode != "gpt" || options[0].Model != "gpt-5.5" {
		t.Fatalf("option = %+v", options[0])
	}
	if len(options[0].SupportedAPIProtocols) != 2 || options[0].SupportedAPIProtocols[0] != "chat_completions" || options[0].SupportedAPIProtocols[1] != "responses" {
		t.Fatalf("protocols = %+v", options[0].SupportedAPIProtocols)
	}
	if !slices.Equal(options[0].SupportedServiceTiers, []string{"priority", "flex"}) {
		t.Fatalf("service tiers = %+v", options[0].SupportedServiceTiers)
	}
	if !slices.Equal(options[0].SupportedReasoningEfforts, []string{"low", "high", "max"}) {
		t.Fatalf("reasoning efforts = %+v", options[0].SupportedReasoningEfforts)
	}
	if options[0].DefaultReasoningEffort != "max" {
		t.Fatalf("default reasoning effort = %q, want first valid candidate max", options[0].DefaultReasoningEffort)
	}
	if store.catalogInput.SystemAccountID != "sys_user" || store.catalogInput.IncludeInactive {
		t.Fatalf("catalog input = %+v", store.catalogInput)
	}
}

func TestServiceModelOptionsExcludesHybridProvider(t *testing.T) {
	store := &providerModelStoreStub{
		protocolCodes: map[string][]string{
			"openai:v1": {"hybrid"},
		},
		catalog: []port.ManagementProviderModelCatalogItem{
			{ProviderCode: "gpt", Model: "gpt-5.5", Scope: "built_in", Status: "active", SupportedAPIProtocols: []string{"chat_completions"}},
			{ProviderCode: "hybrid", Model: "hybrid-direct", Scope: "personal", Status: "active", SupportedAPIProtocols: []string{"messages"}},
		},
	}
	service := NewService(store)

	options, err := service.ModelOptions(context.Background(), ModelOptionListInput{Protocol: "openai"})
	if err != nil {
		t.Fatalf("ModelOptions() error = %v", err)
	}

	if len(options) != 0 {
		t.Fatalf("options = %+v, want no hybrid catalog items", options)
	}
	if len(store.catalogInput.BuiltInProviderCodes) != 0 {
		t.Fatalf("built-in provider codes = %+v, want no hybrid expansion", store.catalogInput.BuiltInProviderCodes)
	}
	if len(store.catalogInput.CustomProviderCodes) != 0 {
		t.Fatalf("custom provider codes = %+v, want no hybrid source", store.catalogInput.CustomProviderCodes)
	}
}

func TestServiceModelsMergesScopesAndFiltersUnpriced(t *testing.T) {
	basePrice := 1.5
	store := &providerModelStoreStub{
		providers: map[string]port.ManagementProviderModelProvider{
			"gpt": {Code: "gpt", Enabled: true},
		},
		catalog: []port.ManagementProviderModelCatalogItem{
			{ProviderCode: "gpt", Model: "gpt-5.5", Scope: "built_in", Status: "active", Source: "seed", InputUSDPer1M: &basePrice},
			{ProviderCode: "gpt", Model: "gpt-5.5", Scope: "global", Status: "active", Source: "custom-global", InputUSDPer1M: &basePrice},
			{ProviderCode: "gpt", Model: "gpt-alias", Scope: "personal", SystemAccountID: "sys_user", Status: "active", Source: "custom-personal", InputUSDPer1M: &basePrice},
			{ProviderCode: "gpt", Model: "draft-model", Scope: "personal", Status: "draft", Source: "custom-personal"},
			{ProviderCode: "gpt", Model: "unpriced-model", Scope: "global", Status: "active", Source: "custom-global"},
		},
	}
	service := NewService(store)

	models, err := service.Models(context.Background(), ModelListInput{
		ProviderCode:    "gpt",
		SystemAccountID: "sys_user",
		IncludeInactive: true,
		IncludeUnpriced: false,
	})
	if err != nil {
		t.Fatalf("Models() error = %v", err)
	}

	if len(models) != 2 {
		t.Fatalf("models = %+v, want priced base and alias", models)
	}
	alias := findModelCatalogItem(models, "gpt-alias")
	if alias == nil || alias.Scope != "personal" || alias.Source != "custom-personal" || alias.InputUSDPer1M == nil {
		t.Fatalf("alias model = %+v", alias)
	}
	if !store.catalogInput.IncludeInactive || store.catalogInput.SystemAccountID != "sys_user" {
		t.Fatalf("catalog input = %+v", store.catalogInput)
	}
}

func TestCatalogItemFromPortMapsRequestAndCodexCapabilities(t *testing.T) {
	priorityInput := 10.0
	priorityOutput := 60.0
	flexInput := 2.5
	flexOutput := 15.0
	longContextThreshold := 272000
	longContextInputMultiplier := 2.0
	longContextOutputMultiplier := 1.5
	builtIn := catalogItemFromPort(port.ManagementProviderModelCatalogItem{
		ProviderCode:                  "gpt",
		Model:                         "gpt-5.6-sol",
		Scope:                         "built_in",
		Status:                        "active",
		SupportedServiceTiers:         []string{"priority", "priority"},
		SupportedReasoningEfforts:     []string{"low", "high", "high", "max"},
		DefaultReasoningEffort:        " high ",
		CodexSupportedReasoningLevels: []string{"low", "high", "ultra", "ultra"},
		CodexDefaultReasoningLevel:    " low ",
		CodexMultiAgentVersion:        " v2 ",
		SupportsServiceTier:           false,
		ServiceTierPrices: map[string]port.ManagementProviderModelPriceSet{
			"priority": {InputUSDPer1M: &priorityInput, OutputUSDPer1M: &priorityOutput},
			"flex":     {InputUSDPer1M: &flexInput, OutputUSDPer1M: &flexOutput},
		},
		LongContextInputTokenThreshold:  &longContextThreshold,
		LongContextInputCostMultiplier:  &longContextInputMultiplier,
		LongContextOutputCostMultiplier: &longContextOutputMultiplier,
	})
	if !slices.Equal(builtIn.SupportedServiceTiers, []string{"priority"}) ||
		!slices.Equal(builtIn.SupportedReasoningEfforts, []string{"low", "high", "max"}) ||
		builtIn.DefaultReasoningEffort != "high" ||
		!builtIn.SupportsServiceTier {
		t.Fatalf("built-in request capabilities = %+v", builtIn)
	}
	if !slices.Equal(builtIn.CodexSupportedReasoningLevels, []string{"low", "high", "ultra"}) ||
		builtIn.CodexDefaultReasoningLevel != "low" ||
		builtIn.CodexMultiAgentVersion != "v2" {
		t.Fatalf("built-in Codex capabilities = %+v", builtIn)
	}
	if builtIn.ServiceTierPrices["priority"].InputUSDPer1M == nil || *builtIn.ServiceTierPrices["priority"].InputUSDPer1M != 10 ||
		builtIn.ServiceTierPrices["priority"].OutputUSDPer1M == nil || *builtIn.ServiceTierPrices["priority"].OutputUSDPer1M != 60 ||
		builtIn.ServiceTierPrices["flex"].InputUSDPer1M == nil || *builtIn.ServiceTierPrices["flex"].InputUSDPer1M != 2.5 ||
		builtIn.ServiceTierPrices["flex"].OutputUSDPer1M == nil || *builtIn.ServiceTierPrices["flex"].OutputUSDPer1M != 15 {
		t.Fatalf("built-in tier pricing metadata = %+v", builtIn)
	}
	if builtIn.LongContextInputTokenThreshold == nil || *builtIn.LongContextInputTokenThreshold != 272000 ||
		builtIn.LongContextInputCostMultiplier == nil || *builtIn.LongContextInputCostMultiplier != 2 ||
		builtIn.LongContextOutputCostMultiplier == nil || *builtIn.LongContextOutputCostMultiplier != 1.5 {
		t.Fatalf("built-in long-context metadata = %+v", builtIn)
	}

	custom := catalogItemFromPort(port.ManagementProviderModelCatalogItem{
		ProviderCode:                  "gpt",
		Model:                         "custom-model",
		Scope:                         "personal",
		Status:                        "active",
		CodexSupportedReasoningLevels: []string{"ultra"},
		CodexDefaultReasoningLevel:    "ultra",
		CodexMultiAgentVersion:        "v2",
		SupportsServiceTier:           true,
	})
	if custom.SupportsServiceTier || len(custom.CodexSupportedReasoningLevels) != 0 ||
		custom.CodexDefaultReasoningLevel != "" || custom.CodexMultiAgentVersion != "" {
		t.Fatalf("custom capabilities = %+v, want derived false and empty Codex fields", custom)
	}
}

func TestModelCatalogWireDefaultReasoningEffortIsExplicitlyNullable(t *testing.T) {
	tests := []struct {
		name  string
		value any
		want  any
	}{
		{name: "catalog null", value: ModelCatalogItem{ProviderCode: "gpt", Model: "without-default"}, want: nil},
		{name: "catalog string", value: ModelCatalogItem{ProviderCode: "gpt", Model: "with-default", DefaultReasoningEffort: "high"}, want: "high"},
		{name: "option null", value: ModelOption{ProviderCode: "gpt", Model: "without-default"}, want: nil},
		{name: "option string", value: ModelOption{ProviderCode: "gpt", Model: "with-default", DefaultReasoningEffort: "high"}, want: "high"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload, err := json.Marshal(tt.value)
			if err != nil {
				t.Fatalf("json.Marshal() error = %v", err)
			}
			var wire map[string]any
			if err := json.Unmarshal(payload, &wire); err != nil {
				t.Fatalf("json.Unmarshal() error = %v", err)
			}
			actual, exists := wire["defaultReasoningEffort"]
			if !exists || actual != tt.want {
				t.Fatalf("defaultReasoningEffort = %#v, exists = %v, want %#v; payload=%s", actual, exists, tt.want, payload)
			}
		})
	}
}

func TestServiceModelsOpenAICompatibleExcludesHybridSource(t *testing.T) {
	price := 1.5
	store := &providerModelStoreStub{
		providers: map[string]port.ManagementProviderModelProvider{
			"openai": {Code: "openai", Enabled: true},
		},
		protocolCodes: map[string][]string{
			"openai:v1": {"gpt", "openai", "hybrid"},
		},
		catalog: []port.ManagementProviderModelCatalogItem{
			{ProviderCode: "gpt", Model: "gpt-model", Scope: "built_in", Status: "active", Source: "seed", InputUSDPer1M: &price},
			{ProviderCode: "openai", Model: "openai-custom", Scope: "personal", Status: "active", Source: "custom-personal", InputUSDPer1M: &price},
			{ProviderCode: "hybrid", Model: "hybrid-direct", Scope: "personal", Status: "active", Source: "custom-personal", InputUSDPer1M: &price},
		},
	}
	service := NewService(store)

	models, err := service.Models(context.Background(), ModelListInput{
		ProviderCode:    "openai",
		SystemAccountID: "sys_user",
	})
	if err != nil {
		t.Fatalf("Models() error = %v", err)
	}

	if findModelCatalogItem(models, "gpt-model") == nil || findModelCatalogItem(models, "openai-custom") == nil {
		t.Fatalf("models = %+v, want gpt built-in and openai custom entries", models)
	}
	if findModelCatalogItem(models, "hybrid-direct") != nil {
		t.Fatalf("models = %+v, want no hybrid source entries", models)
	}
}

func findModelCatalogItem(items []ModelCatalogItem, model string) *ModelCatalogItem {
	for index := range items {
		if items[index].Model == model {
			return &items[index]
		}
	}
	return nil
}

func TestServiceModelsHybridUsesDistinctProtocolProviderUnion(t *testing.T) {
	price := 1.0
	store := &providerModelStoreStub{
		providers: map[string]port.ManagementProviderModelProvider{
			"hybrid": {Code: "hybrid", Enabled: true},
		},
		enabledCodes: []string{"legacy-without-protocol"},
		protocolCodes: map[string][]string{
			"openai:v1":     {" gpt ", "openai", "shared", "", "hybrid"},
			"anthropic:v1":  {" anthropic ", "shared", "hybrid"},
			"gemini:v1beta": {"gemini", " openai ", "hybrid", " "},
		},
		catalog: []port.ManagementProviderModelCatalogItem{
			{ProviderCode: "gpt", Model: "gpt-model", Scope: "built_in", Status: "active", Source: "seed", InputUSDPer1M: &price},
			{ProviderCode: "openai", Model: "openai-custom", Scope: "personal", Status: "active", Source: "custom-personal", InputUSDPer1M: &price},
			{ProviderCode: "shared", Model: "shared-model", Scope: "built_in", Status: "active", Source: "seed", InputUSDPer1M: &price},
			{ProviderCode: "anthropic", Model: "anthropic-model", Scope: "built_in", Status: "active", Source: "seed", InputUSDPer1M: &price},
			{ProviderCode: "gemini", Model: "gemini-model", Scope: "built_in", Status: "active", Source: "seed", InputUSDPer1M: &price},
			{ProviderCode: "legacy-without-protocol", Model: "legacy-model", Scope: "built_in", Status: "active", Source: "seed", InputUSDPer1M: &price},
			{ProviderCode: "hybrid", Model: "hybrid-direct", Scope: "personal", Status: "active", Source: "custom-personal", InputUSDPer1M: &price},
		},
	}
	service := NewService(store)

	models, err := service.Models(context.Background(), ModelListInput{ProviderCode: "hybrid"})
	if err != nil {
		t.Fatalf("Models() error = %v", err)
	}
	wantProviderCodes := []string{"gpt", "openai", "shared", "anthropic", "gemini"}
	if !slices.Equal(store.catalogInput.BuiltInProviderCodes, wantProviderCodes) || !slices.Equal(store.catalogInput.CustomProviderCodes, wantProviderCodes) {
		t.Fatalf("catalog input = %+v, want protocol provider union %+v", store.catalogInput, wantProviderCodes)
	}
	if findModelCatalogItem(models, "hybrid-direct") != nil {
		t.Fatalf("hybrid models = %+v, want no hybrid source entries", models)
	}
	if findModelCatalogItem(models, "legacy-model") != nil {
		t.Fatalf("hybrid models = %+v, want no enabled-only legacy provider entries", models)
	}
}

func TestServiceModelsReturnsProviderNotFound(t *testing.T) {
	service := NewService(&providerModelStoreStub{})
	_, err := service.Models(context.Background(), ModelListInput{ProviderCode: "missing"})
	if err != ErrProviderNotFound {
		t.Fatalf("Models() error = %v, want ErrProviderNotFound", err)
	}
}

func TestServiceSetDefaultHealthCheckModelPersistsPersonalPreferenceForOrdinaryUser(t *testing.T) {
	store := &providerModelStoreStub{
		providers: map[string]port.ManagementProviderModelProvider{
			"gpt": {Code: "gpt", Enabled: true},
		},
		catalog: []port.ManagementProviderModelCatalogItem{
			{ProviderCode: "gpt", Model: "gpt-5.5", Scope: "built_in", Status: "active", Mode: "text", SupportedAPIProtocols: []string{"responses"}},
		},
	}
	service := NewService(store)

	result, err := service.SetDefaultHealthCheckModel(context.Background(), DefaultHealthCheckModelInput{
		ProviderCode:         " gpt ",
		ActorSystemAccountID: " sys_user ",
		ActorRole:            "user",
		Model:                " gpt-5.5 ",
	})
	if err != nil {
		t.Fatalf("SetDefaultHealthCheckModel() error = %v", err)
	}

	if result.ProviderCode != "gpt" || result.DefaultHealthCheckModel != "gpt-5.5" {
		t.Fatalf("result = %+v", result)
	}
	if store.catalogInput.SystemAccountID != "sys_user" || !store.catalogInput.IncludeInactive {
		t.Fatalf("catalog input = %+v", store.catalogInput)
	}
	if store.setDefaultInput.ProviderCode != "gpt" ||
		store.setDefaultInput.SystemAccountID != "sys_user" ||
		store.setDefaultInput.Model != "gpt-5.5" {
		t.Fatalf("set default input = %+v", store.setDefaultInput)
	}
}

func TestServiceSetDefaultHealthCheckModelPersistsSystemDefaultForAdmin(t *testing.T) {
	store := &providerModelStoreStub{
		providers: map[string]port.ManagementProviderModelProvider{
			"gpt": {Code: "gpt", Enabled: true},
		},
		catalog: []port.ManagementProviderModelCatalogItem{
			{ProviderCode: "gpt", Model: "gpt-5.6-sol", Scope: "built_in", Status: "active", Mode: "text", SupportedAPIProtocols: []string{"responses"}},
			{ProviderCode: "gpt", Model: "admin-personal", Scope: "personal", SystemAccountID: "sys_admin", Status: "active", Mode: "text", SupportedAPIProtocols: []string{"responses"}},
		},
	}

	result, err := NewService(store).SetDefaultHealthCheckModel(context.Background(), DefaultHealthCheckModelInput{
		ProviderCode:         "gpt",
		ActorSystemAccountID: "sys_admin",
		ActorRole:            "admin",
		Model:                "gpt-5.6-sol",
	})
	if err != nil {
		t.Fatalf("SetDefaultHealthCheckModel() error = %v", err)
	}
	if result.DefaultHealthCheckModel != "gpt-5.6-sol" {
		t.Fatalf("result = %+v", result)
	}
	if store.catalogInput.SystemAccountID != "" {
		t.Fatalf("admin catalog scope = %q, want global scope", store.catalogInput.SystemAccountID)
	}
	if store.setDefaultInput.ProviderCode != "" {
		t.Fatalf("admin must not write personal preference: %+v", store.setDefaultInput)
	}
	if store.setSystemDefaultInput.ProviderCode != "gpt" || store.setSystemDefaultInput.Model != "gpt-5.6-sol" {
		t.Fatalf("system default input = %+v", store.setSystemDefaultInput)
	}
}

func TestServiceSetDefaultHealthCheckModelRejectsPersonalModelForAdminSystemDefault(t *testing.T) {
	store := &providerModelStoreStub{
		providers: map[string]port.ManagementProviderModelProvider{
			"gpt": {Code: "gpt", Enabled: true},
		},
		catalog: []port.ManagementProviderModelCatalogItem{
			{ProviderCode: "gpt", Model: "admin-personal", Scope: "personal", SystemAccountID: "sys_admin", Status: "active", Mode: "text", SupportedAPIProtocols: []string{"responses"}},
		},
	}

	_, err := NewService(store).SetDefaultHealthCheckModel(context.Background(), DefaultHealthCheckModelInput{
		ProviderCode:         "gpt",
		ActorSystemAccountID: "sys_admin",
		ActorRole:            "admin",
		Model:                "admin-personal",
	})
	message, ok := DefaultHealthCheckModelValidationMessage(err)
	if !ok || message != "系统默认检查模型不能选择个人模型" {
		t.Fatalf("validation message = %q, %v; err = %v", message, ok, err)
	}
	if store.setSystemDefaultInput.ProviderCode != "" {
		t.Fatalf("system default write should not run: %+v", store.setSystemDefaultInput)
	}
}

func TestServiceSetDefaultHealthCheckModelRejectsInvalidSelections(t *testing.T) {
	tests := []struct {
		name    string
		input   DefaultHealthCheckModelInput
		catalog []port.ManagementProviderModelCatalogItem
		wantMsg string
	}{
		{
			name:    "missing system account",
			input:   DefaultHealthCheckModelInput{ProviderCode: "gpt", Model: "gpt-5.5"},
			wantMsg: "请选择要设置默认检查模型的系统账户",
		},
		{
			name:    "missing model",
			input:   DefaultHealthCheckModelInput{ProviderCode: "gpt", ActorSystemAccountID: "sys_user", ActorRole: "user"},
			wantMsg: "默认检查模型参数无效",
		},
		{
			name:    "not visible",
			input:   DefaultHealthCheckModelInput{ProviderCode: "gpt", ActorSystemAccountID: "sys_user", ActorRole: "user", Model: "missing"},
			catalog: []port.ManagementProviderModelCatalogItem{{ProviderCode: "gpt", Model: "gpt-5.5", Scope: "built_in", Status: "active"}},
			wantMsg: "模型不在当前用户可见目录中：missing",
		},
		{
			name:    "inactive",
			input:   DefaultHealthCheckModelInput{ProviderCode: "gpt", ActorSystemAccountID: "sys_user", ActorRole: "user", Model: "draft-model"},
			catalog: []port.ManagementProviderModelCatalogItem{{ProviderCode: "gpt", Model: "draft-model", Scope: "global", Status: "draft"}},
			wantMsg: "只能把启用模型设置为默认检查模型",
		},
		{
			name:    "image model",
			input:   DefaultHealthCheckModelInput{ProviderCode: "gpt", ActorSystemAccountID: "sys_user", ActorRole: "user", Model: "image-model"},
			catalog: []port.ManagementProviderModelCatalogItem{{ProviderCode: "gpt", Model: "image-model", Scope: "built_in", Status: "active", Mode: "image", SupportedAPIProtocols: []string{"images"}}},
			wantMsg: "默认检查模型只能选择文本生成模型",
		},
		{
			name:    "unsupported protocol",
			input:   DefaultHealthCheckModelInput{ProviderCode: "gpt", ActorSystemAccountID: "sys_user", ActorRole: "user", Model: "embed-model"},
			catalog: []port.ManagementProviderModelCatalogItem{{ProviderCode: "gpt", Model: "embed-model", Scope: "built_in", Status: "active", SupportedAPIProtocols: []string{"embed_content"}}},
			wantMsg: "默认检查模型只能选择文本生成模型",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &providerModelStoreStub{
				providers: map[string]port.ManagementProviderModelProvider{
					"gpt": {Code: "gpt", Enabled: true},
				},
				catalog: tt.catalog,
			}
			_, err := NewService(store).SetDefaultHealthCheckModel(context.Background(), tt.input)
			if err == nil {
				t.Fatal("SetDefaultHealthCheckModel() error = nil, want validation error")
			}
			got, ok := DefaultHealthCheckModelValidationMessage(err)
			if !ok || got != tt.wantMsg {
				t.Fatalf("validation message = %q, %v; want %q", got, ok, tt.wantMsg)
			}
			if store.setDefaultInput.ProviderCode != "" {
				t.Fatalf("set default should not be called, got %+v", store.setDefaultInput)
			}
		})
	}
}

func TestServiceSetDefaultHealthCheckModelReturnsProviderNotFound(t *testing.T) {
	_, err := NewService(&providerModelStoreStub{}).SetDefaultHealthCheckModel(context.Background(), DefaultHealthCheckModelInput{
		ProviderCode:         "missing",
		ActorSystemAccountID: "sys_user",
		ActorRole:            "user",
		Model:                "gpt-5.5",
	})
	if err != ErrProviderNotFound {
		t.Fatalf("SetDefaultHealthCheckModel() error = %v, want ErrProviderNotFound", err)
	}
}

func TestServiceCreateCustomModelPersistsPersonalModelAndInvalidates(t *testing.T) {
	price := 1.25
	store := &providerModelStoreStub{
		providers: map[string]port.ManagementProviderModelProvider{"gpt": {Code: "gpt", Enabled: true}},
	}
	invalidator := &customProviderModelInvalidatorStub{}
	service := NewServiceWithOptions(ServiceOptions{
		Store:       store,
		Invalidator: invalidator,
		NewID:       func(prefix string) string { return prefix + "_fixed" },
	})

	result, err := service.CreateCustomModel(context.Background(), CustomModelCreateInput{
		ProviderCode:          " gpt ",
		ActorSystemAccountID:  "sys_admin",
		ActorRole:             "admin",
		TargetSystemAccountID: " sys_user ",
		Fields: CustomModelMutation{
			Model:                     OptionalString{Set: true, Value: " custom-chat "},
			SupportedAPIProtocols:     OptionalStringList{Set: true, Value: []string{"responses", "responses"}},
			SupportedServiceTiers:     OptionalStringList{Set: true, Value: []string{"priority", "flex"}},
			SupportedReasoningEfforts: OptionalStringList{Set: true, Value: []string{"low", "high", "high"}},
			DefaultReasoningEffort:    OptionalString{Set: true, Value: "high"},
			InputUSDPer1M:             OptionalFloat{Set: true, Value: &price},
			PricingNotes:              OptionalString{Set: true, Value: " 计费说明 "},
		},
	})
	if err != nil {
		t.Fatalf("CreateCustomModel() error = %v", err)
	}
	if result.ID != "custom_model_fixed" || result.Scope != "personal" || result.SystemAccountID != "sys_user" || result.PricingNotes != "计费说明" {
		t.Fatalf("result = %+v", result)
	}
	if store.saveInput.ProviderCode != "gpt" ||
		store.saveInput.Model != "custom-chat" ||
		store.saveInput.Scope != "personal" ||
		store.saveInput.SystemAccountID != "sys_user" ||
		store.saveInput.Status != "active" ||
		len(store.saveInput.SupportedAPIProtocols) != 1 ||
		store.saveInput.SupportedAPIProtocols[0] != "responses" ||
		len(store.saveInput.SupportedServiceTiers) != 2 ||
		store.saveInput.SupportedServiceTiers[0] != "priority" ||
		store.saveInput.SupportedServiceTiers[1] != "flex" ||
		len(store.saveInput.SupportedReasoningEfforts) != 2 ||
		store.saveInput.SupportedReasoningEfforts[0] != "low" ||
		store.saveInput.SupportedReasoningEfforts[1] != "high" ||
		store.saveInput.DefaultReasoningEffort != "high" {
		t.Fatalf("save input = %+v", store.saveInput)
	}
	if invalidator.reason != CustomProviderModelSavedReason {
		t.Fatalf("invalidation reason = %q", invalidator.reason)
	}
}

func TestServiceCreateCustomModelLogsCommittedCacheInvalidationFailureWithoutFailingWrite(t *testing.T) {
	var logs bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })
	price := 1.25
	store := &providerModelStoreStub{
		providers: map[string]port.ManagementProviderModelProvider{"gpt": {Code: "gpt", Enabled: true}},
	}
	invalidator := &customProviderModelInvalidatorStub{err: errors.New("invalidation failed")}
	service := NewServiceWithOptions(ServiceOptions{
		Store:       store,
		Invalidator: invalidator,
		NewID:       func(prefix string) string { return prefix + "_fixed" },
	})

	result, err := service.CreateCustomModel(context.Background(), CustomModelCreateInput{
		ProviderCode:          "gpt",
		ActorSystemAccountID:  "sys_admin",
		ActorRole:             "admin",
		TargetSystemAccountID: "sys_user",
		TraceID:               "trace_model_cache_sync",
		Fields: CustomModelMutation{
			Model:         OptionalString{Set: true, Value: "custom-chat"},
			InputUSDPer1M: OptionalFloat{Set: true, Value: &price},
		},
	})
	if err != nil || result.ID != "custom_model_fixed" {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
	if store.saveInput.Model != "custom-chat" {
		t.Fatalf("save=%+v", store.saveInput)
	}
	if invalidator.calls != 1 || invalidator.reason != CustomProviderModelSavedReason {
		t.Fatalf("invalidation calls=%d reason=%q", invalidator.calls, invalidator.reason)
	}
	if !strings.Contains(logs.String(), `"event":"model_cache_sync_failed_after_commit"`) || !strings.Contains(logs.String(), `"trace_id":"trace_model_cache_sync"`) {
		t.Fatalf("cache sync warning log = %s", logs.String())
	}
}

func TestServiceCreateCustomModelValidatesGPTRequestCapabilities(t *testing.T) {
	price := 1.25
	tests := []struct {
		name          string
		providerCode  string
		mode          OptionalString
		serviceTiers  []string
		efforts       []string
		defaultEffort OptionalString
		wantMessage   string
	}{
		{
			name:         "reject unknown service tier",
			providerCode: "gpt",
			serviceTiers: []string{"fast"},
			wantMessage:  "自定义模型参数无效",
		},
		{
			name:         "reject service tier raw length before dedupe",
			providerCode: "gpt",
			serviceTiers: []string{"priority", "priority", "priority"},
			wantMessage:  "自定义模型参数无效",
		},
		{
			name:         "reject whitespace padded service tier",
			providerCode: "gpt",
			serviceTiers: []string{" priority "},
			wantMessage:  "自定义模型参数无效",
		},
		{
			name:         "reject codex ultra as wire effort",
			providerCode: "gpt",
			efforts:      []string{"ultra"},
			wantMessage:  "自定义模型参数无效",
		},
		{
			name:         "reject reasoning effort raw length before dedupe",
			providerCode: "gpt",
			efforts:      []string{"low", "low", "low", "low", "low", "low", "low", "low"},
			wantMessage:  "自定义模型参数无效",
		},
		{
			name:         "reject whitespace padded reasoning effort",
			providerCode: "gpt",
			efforts:      []string{" high "},
			wantMessage:  "自定义模型参数无效",
		},
		{
			name:          "reject default outside supported efforts",
			providerCode:  "gpt",
			efforts:       []string{"low"},
			defaultEffort: OptionalString{Set: true, Value: "high"},
			wantMessage:   "默认思考级别必须属于支持的思考级别",
		},
		{
			name:         "reject invalid generic capability token",
			providerCode: "anthropic",
			serviceTiers: []string{" priority "},
			wantMessage:  "自定义模型参数无效",
		},
		{
			name:         "reject image capabilities",
			providerCode: "gpt",
			mode:         OptionalString{Set: true, Value: "image"},
			efforts:      []string{"high"},
			wantMessage:  "只有文本自定义模型支持服务等级和思考能力配置",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &providerModelStoreStub{
				providers: map[string]port.ManagementProviderModelProvider{
					tt.providerCode: {Code: tt.providerCode, Enabled: true},
				},
			}
			_, err := NewService(store).CreateCustomModel(context.Background(), CustomModelCreateInput{
				ProviderCode:          tt.providerCode,
				ActorSystemAccountID:  "sys_admin",
				ActorRole:             "admin",
				TargetSystemAccountID: "sys_user",
				Fields: CustomModelMutation{
					Model:                     OptionalString{Set: true, Value: "custom-chat"},
					Mode:                      tt.mode,
					SupportedServiceTiers:     OptionalStringList{Set: true, Value: tt.serviceTiers},
					SupportedReasoningEfforts: OptionalStringList{Set: true, Value: tt.efforts},
					DefaultReasoningEffort:    tt.defaultEffort,
					InputUSDPer1M:             OptionalFloat{Set: true, Value: &price},
				},
			})
			message, ok := CustomModelValidationMessage(err)
			if !ok || message != tt.wantMessage {
				t.Fatalf("error = %v, message = %q, want %q", err, message, tt.wantMessage)
			}
		})
	}
}

func TestServiceCreateCustomModelAllowsExplicitCapabilityClearsOutsideGPTText(t *testing.T) {
	price := 1.25
	for _, input := range []struct {
		name         string
		providerCode string
		mode         string
	}{
		{name: "non gpt", providerCode: "anthropic"},
		{name: "gpt image", providerCode: "gpt", mode: "image"},
		{name: "gpt audio", providerCode: "gpt", mode: "audio"},
	} {
		t.Run(input.name, func(t *testing.T) {
			store := &providerModelStoreStub{
				providers: map[string]port.ManagementProviderModelProvider{
					input.providerCode: {Code: input.providerCode, Enabled: true},
				},
			}
			result, err := NewService(store).CreateCustomModel(context.Background(), CustomModelCreateInput{
				ProviderCode:          input.providerCode,
				ActorSystemAccountID:  "sys_admin",
				ActorRole:             "admin",
				TargetSystemAccountID: "sys_user",
				Fields: CustomModelMutation{
					Model:                     OptionalString{Set: true, Value: "custom-model"},
					Status:                    OptionalString{Set: true, Value: "draft"},
					Mode:                      OptionalString{Set: input.mode != "", Value: input.mode},
					SupportedServiceTiers:     OptionalStringList{Set: true, Value: []string{}},
					SupportedReasoningEfforts: OptionalStringList{Set: true, Value: []string{}},
					InputUSDPer1M:             OptionalFloat{Set: true, Value: &price},
				},
			})
			if err != nil {
				t.Fatalf("CreateCustomModel() error = %v", err)
			}
			if result.SupportsServiceTier ||
				len(store.saveInput.SupportedServiceTiers) != 0 ||
				len(store.saveInput.SupportedReasoningEfforts) != 0 ||
				store.saveInput.DefaultReasoningEffort != "" {
				t.Fatalf("result=%+v save=%+v", result, store.saveInput)
			}
		})
	}
}

func TestServiceUpdateCustomModelClonesAndValidatesRequestCapabilities(t *testing.T) {
	price := 1.25
	existing := port.ManagementProviderModelCatalogItem{
		ID:                        "custom_model_1",
		ProviderCode:              "gpt",
		Model:                     "custom-chat",
		Scope:                     "personal",
		SystemAccountID:           "sys_user",
		Status:                    "active",
		Mode:                      "text",
		SupportedServiceTiers:     []string{"priority"},
		SupportedReasoningEfforts: []string{"low", "high"},
		DefaultReasoningEffort:    "high",
		InputUSDPer1M:             &price,
	}
	store := &providerModelStoreStub{
		customByID: map[string]port.ManagementProviderModelCatalogItem{"custom_model_1": existing},
	}
	service := NewService(store)

	_, err := service.UpdateCustomModel(context.Background(), CustomModelUpdateInput{
		ProviderCode:         "gpt",
		ID:                   "custom_model_1",
		ActorSystemAccountID: "sys_user",
		ActorRole:            "user",
		Fields:               CustomModelMutation{Notes: OptionalString{Set: true, Value: "updated"}},
	})
	if err != nil {
		t.Fatalf("UpdateCustomModel() clone error = %v", err)
	}
	if !slices.Equal(store.saveInput.SupportedServiceTiers, []string{"priority"}) ||
		!slices.Equal(store.saveInput.SupportedReasoningEfforts, []string{"low", "high"}) ||
		store.saveInput.DefaultReasoningEffort != "high" {
		t.Fatalf("cloned save input = %+v", store.saveInput)
	}

	_, err = service.UpdateCustomModel(context.Background(), CustomModelUpdateInput{
		ProviderCode:         "gpt",
		ID:                   "custom_model_1",
		ActorSystemAccountID: "sys_user",
		ActorRole:            "user",
		Fields: CustomModelMutation{
			SupportedReasoningEfforts: OptionalStringList{Set: true, Value: []string{"low"}},
		},
	})
	if message, ok := CustomModelValidationMessage(err); !ok || message != "默认思考级别必须属于支持的思考级别" {
		t.Fatalf("reducing supported reasoning efforts message = %q, ok = %v, err = %v", message, ok, err)
	}

	_, err = service.UpdateCustomModel(context.Background(), CustomModelUpdateInput{
		ProviderCode:         "gpt",
		ID:                   "custom_model_1",
		ActorSystemAccountID: "sys_user",
		ActorRole:            "user",
		Fields: CustomModelMutation{
			DefaultReasoningEffort: OptionalString{Set: true, Value: ""},
		},
	})
	if err != nil || store.saveInput.DefaultReasoningEffort != "" {
		t.Fatalf("explicit default reasoning clear err = %v, save = %+v", err, store.saveInput)
	}

	_, err = service.UpdateCustomModel(context.Background(), CustomModelUpdateInput{
		ProviderCode:         "gpt",
		ID:                   "custom_model_1",
		ActorSystemAccountID: "sys_user",
		ActorRole:            "user",
		Fields: CustomModelMutation{
			Status:                    OptionalString{Set: true, Value: "draft"},
			Mode:                      OptionalString{Set: true, Value: "image"},
			SupportedServiceTiers:     OptionalStringList{Set: true, Value: []string{}},
			SupportedReasoningEfforts: OptionalStringList{Set: true, Value: []string{}},
			DefaultReasoningEffort:    OptionalString{Set: true, Value: ""},
		},
	})
	if err != nil {
		t.Fatalf("UpdateCustomModel() clear error = %v", err)
	}
	if store.saveInput.Mode != "image" ||
		len(store.saveInput.SupportedServiceTiers) != 0 ||
		len(store.saveInput.SupportedReasoningEfforts) != 0 ||
		store.saveInput.DefaultReasoningEffort != "" {
		t.Fatalf("cleared save input = %+v", store.saveInput)
	}
}

func TestServiceCreateCustomModelRejectsGlobalForOrdinaryUser(t *testing.T) {
	price := 1.25
	store := &providerModelStoreStub{
		providers: map[string]port.ManagementProviderModelProvider{"gpt": {Code: "gpt", Enabled: true}},
	}
	_, err := NewService(store).CreateCustomModel(context.Background(), CustomModelCreateInput{
		ProviderCode:         "gpt",
		ActorSystemAccountID: "sys_user",
		ActorRole:            "user",
		Fields: CustomModelMutation{
			Scope:         OptionalString{Set: true, Value: "global"},
			Model:         OptionalString{Set: true, Value: "custom-chat"},
			InputUSDPer1M: OptionalFloat{Set: true, Value: &price},
		},
	})
	message, ok := CustomModelForbiddenMessage(err)
	if !ok || message != "只有管理员可以创建全局模型" {
		t.Fatalf("forbidden message = %q, %v; err = %v", message, ok, err)
	}
	if store.saveInput.Model != "" {
		t.Fatalf("save should not be called, got %+v", store.saveInput)
	}
}

func TestServiceCreateCustomModelPrioritizesGlobalForbiddenBeforeCapabilityValidation(t *testing.T) {
	price := 1.25
	store := &providerModelStoreStub{
		providers: map[string]port.ManagementProviderModelProvider{"anthropic": {Code: "anthropic", Enabled: true}},
	}
	_, err := NewService(store).CreateCustomModel(context.Background(), CustomModelCreateInput{
		ProviderCode:         "anthropic",
		ActorSystemAccountID: "sys_user",
		ActorRole:            "user",
		Fields: CustomModelMutation{
			Scope:                 OptionalString{Set: true, Value: "global"},
			Model:                 OptionalString{Set: true, Value: "custom-chat"},
			SupportedServiceTiers: OptionalStringList{Set: true, Value: []string{"priority"}},
			InputUSDPer1M:         OptionalFloat{Set: true, Value: &price},
		},
	})
	message, ok := CustomModelForbiddenMessage(err)
	if !ok || message != "只有管理员可以创建全局模型" {
		t.Fatalf("forbidden message = %q, %v; err = %v", message, ok, err)
	}
	if store.saveInput.Model != "" {
		t.Fatalf("save should not be called, got %+v", store.saveInput)
	}
}

func TestServiceAllowsOrdinaryUserToMaintainOwnPersonalModelPrices(t *testing.T) {
	price := 1.25
	store := &providerModelStoreStub{
		providers: map[string]port.ManagementProviderModelProvider{"gpt": {Code: "gpt", Enabled: true}},
		customByID: map[string]port.ManagementProviderModelCatalogItem{
			"custom_model_1": {ID: "custom_model_1", ProviderCode: "gpt", Model: "custom-chat", Scope: "personal", SystemAccountID: "sys_user", Status: "draft"},
		},
	}
	service := NewService(store)

	created, err := service.CreateCustomModel(context.Background(), CustomModelCreateInput{
		ProviderCode: "gpt", ActorSystemAccountID: "sys_user", ActorRole: "user",
		Fields: CustomModelMutation{Model: OptionalString{Set: true, Value: "custom-chat"}, Status: OptionalString{Set: true, Value: "draft"}, InputUSDPer1M: OptionalFloat{Set: true, Value: &price}},
	})
	if err != nil || created.InputUSDPer1M == nil || *created.InputUSDPer1M != price {
		t.Fatalf("created = %+v, err = %v", created, err)
	}

	updated, err := service.UpdateCustomModel(context.Background(), CustomModelUpdateInput{
		ProviderCode: "gpt", ID: "custom_model_1", ActorSystemAccountID: "sys_user", ActorRole: "user",
		Fields: CustomModelMutation{InputUSDPer1M: OptionalFloat{Set: true, Value: &price}},
	})
	if err != nil || updated.InputUSDPer1M == nil || *updated.InputUSDPer1M != price {
		t.Fatalf("updated = %+v, err = %v", updated, err)
	}
}

func TestServiceCreateCustomModelRequiresOwnPriceWhenActive(t *testing.T) {
	price := 2.0
	store := &providerModelStoreStub{
		providers: map[string]port.ManagementProviderModelProvider{"gpt": {Code: "gpt", Enabled: true}},
	}
	service := NewServiceWithOptions(ServiceOptions{Store: store, NewID: func(prefix string) string { return prefix + "_price" }})

	_, err := service.CreateCustomModel(context.Background(), CustomModelCreateInput{
		ProviderCode:          "gpt",
		ActorSystemAccountID:  "sys_admin",
		ActorRole:             "admin",
		TargetSystemAccountID: "sys_user",
		Fields: CustomModelMutation{
			Model:  OptionalString{Set: true, Value: "custom-unpriced"},
			Status: OptionalString{Set: true, Value: "active"},
		},
	})
	message, ok := CustomModelValidationMessage(err)
	if !ok || message != "启用的自定义模型必须配置价格" {
		t.Fatalf("missing pricing message = %q, %v; err = %v", message, ok, err)
	}

	result, err := service.CreateCustomModel(context.Background(), CustomModelCreateInput{
		ProviderCode:          "gpt",
		ActorSystemAccountID:  "sys_admin",
		ActorRole:             "admin",
		TargetSystemAccountID: "sys_user",
		Fields: CustomModelMutation{
			Model:                 OptionalString{Set: true, Value: "custom-tier-priced"},
			Status:                OptionalString{Set: true, Value: "active"},
			ServiceTierPrices:     OptionalProviderModelPriceMap{Set: true, Value: map[string]port.ManagementProviderModelPriceSet{"priority": {InputUSDPer1M: &price}}},
			SupportedServiceTiers: OptionalStringList{Set: true, Value: []string{"priority"}},
		},
	})
	if err != nil {
		t.Fatalf("CreateCustomModel() with tier price error = %v", err)
	}
	if result.ServiceTierPrices["priority"].InputUSDPer1M == nil || *result.ServiceTierPrices["priority"].InputUSDPer1M != price {
		t.Fatalf("tier pricing result=%+v save=%+v", result, store.saveInput)
	}
}

func TestServiceCreateCustomModelCopiesVisibleConfigurationTemplateForOrdinaryUser(t *testing.T) {
	inputPrice := 5.0
	outputPrice := 30.0
	contextWindow := 1_050_000
	maxInput := 922_000
	maxOutput := 128_000
	store := &providerModelStoreStub{
		providers: map[string]port.ManagementProviderModelProvider{"gpt": {Code: "gpt", Enabled: true}},
		catalog: []port.ManagementProviderModelCatalogItem{{
			ID: "provider_model_gpt_5_6_sol", ProviderCode: "gpt", Model: "gpt-5.6-sol", Scope: "built_in", Status: "active", Mode: "chat",
			ReleaseDate: "2026-06-26", ShutdownDate: "2027-06-26",
			SupportedAPIProtocols: []string{"responses", "chat_completions"}, SupportedServiceTiers: []string{"priority", "flex"},
			SupportedReasoningEfforts: []string{"none", "low", "medium", "high", "xhigh", "max"},
			DefaultReasoningEffort:    "high",
			ContextWindowTokens:       &contextWindow, MaxInputTokens: &maxInput, MaxOutputTokens: &maxOutput,
			InputUSDPer1M: &inputPrice, OutputUSDPer1M: &outputPrice,
			PricingNotes: "trusted pricing", CapabilityNotes: "trusted capability", Notes: "trusted internal",
		}},
	}
	service := NewServiceWithOptions(ServiceOptions{Store: store, NewID: func(prefix string) string { return prefix + "_copied" }})

	result, err := service.CreateCustomModel(context.Background(), CustomModelCreateInput{
		ProviderCode: "gpt", ActorSystemAccountID: "sys_user", ActorRole: "user",
		Fields: CustomModelMutation{
			ConfigurationTemplateID: OptionalString{Set: true, Value: "provider_model_gpt_5_6_sol"},
			Model:                   OptionalString{Set: true, Value: "my-gpt-model"}, Status: OptionalString{Set: true, Value: "active"},
		},
	})
	if err != nil {
		t.Fatalf("CreateCustomModel() error = %v", err)
	}
	if result.Status != "active" || result.Model != "my-gpt-model" || result.InputUSDPer1M == nil || *result.InputUSDPer1M != inputPrice {
		t.Fatalf("result = %+v", result)
	}
	if store.saveInput.ContextWindowTokens == nil || *store.saveInput.ContextWindowTokens != contextWindow ||
		store.saveInput.MaxInputTokens == nil || *store.saveInput.MaxInputTokens != maxInput ||
		store.saveInput.MaxOutputTokens == nil || *store.saveInput.MaxOutputTokens != maxOutput ||
		!slices.Equal(store.saveInput.SupportedServiceTiers, []string{"priority", "flex"}) ||
		!slices.Equal(store.saveInput.SupportedReasoningEfforts, []string{"none", "low", "medium", "high", "xhigh", "max"}) ||
		store.saveInput.DefaultReasoningEffort != "high" ||
		store.saveInput.ReleaseDate != "2026-06-26" || store.saveInput.ShutdownDate != "2027-06-26" ||
		store.saveInput.PricingNotes != "trusted pricing" || store.saveInput.CapabilityNotes != "trusted capability" || store.saveInput.Notes != "trusted internal" {
		t.Fatalf("save input did not copy template configuration: %+v", store.saveInput)
	}
	if store.catalogInput.SystemAccountID != "sys_user" || !store.catalogInput.IncludeInactive {
		t.Fatalf("template catalog input = %+v", store.catalogInput)
	}
}

func TestServiceCreateCustomModelRejectsUnavailableConfigurationTemplate(t *testing.T) {
	price := 1.0
	store := &providerModelStoreStub{
		providers: map[string]port.ManagementProviderModelProvider{"gpt": {Code: "gpt", Enabled: true}},
		catalog: []port.ManagementProviderModelCatalogItem{{
			ID: "disabled_template", ProviderCode: "gpt", Model: "disabled-model", Scope: "built_in", Status: "disabled", InputUSDPer1M: &price,
		}},
	}
	_, err := NewService(store).CreateCustomModel(context.Background(), CustomModelCreateInput{
		ProviderCode: "gpt", ActorSystemAccountID: "sys_user", ActorRole: "user",
		Fields: CustomModelMutation{
			ConfigurationTemplateID: OptionalString{Set: true, Value: "disabled_template"},
			Model:                   OptionalString{Set: true, Value: "invalid-copy"}, Status: OptionalString{Set: true, Value: "active"},
		},
	})
	message, ok := CustomModelValidationMessage(err)
	if !ok || message != "配置模板不可用" {
		t.Fatalf("message = %q, ok = %v, err = %v", message, ok, err)
	}
}

func TestServiceCreateCustomModelResolvesTemplateFromMergedVisibleCatalog(t *testing.T) {
	price := 1.0
	store := &providerModelStoreStub{
		providers: map[string]port.ManagementProviderModelProvider{"gpt": {Code: "gpt", Enabled: true}},
		catalog: []port.ManagementProviderModelCatalogItem{
			{ID: "built_in_shared", ProviderCode: "gpt", Model: "shared-model", Scope: "built_in", Status: "active", InputUSDPer1M: &price},
			{ID: "personal_shared", ProviderCode: "gpt", Model: "shared-model", Scope: "personal", SystemAccountID: "sys_user", Status: "active", InputUSDPer1M: &price},
		},
	}
	service := NewServiceWithOptions(ServiceOptions{Store: store, NewID: func(prefix string) string { return prefix + "_merged" }})

	_, err := service.CreateCustomModel(context.Background(), CustomModelCreateInput{
		ProviderCode: "gpt", ActorSystemAccountID: "sys_user", ActorRole: "user",
		Fields: CustomModelMutation{ConfigurationTemplateID: OptionalString{Set: true, Value: "built_in_shared"}, Model: OptionalString{Set: true, Value: "copy-built-in"}},
	})
	if message, ok := CustomModelValidationMessage(err); !ok || message != "配置模板不可用" {
		t.Fatalf("covered built-in template message = %q, ok = %v, err = %v", message, ok, err)
	}
	result, err := service.CreateCustomModel(context.Background(), CustomModelCreateInput{
		ProviderCode: "gpt", ActorSystemAccountID: "sys_user", ActorRole: "user",
		Fields: CustomModelMutation{ConfigurationTemplateID: OptionalString{Set: true, Value: "personal_shared"}, Model: OptionalString{Set: true, Value: "copy-personal"}},
	})
	if err != nil || result.Model != "copy-personal" {
		t.Fatalf("visible personal template result = %+v, err = %v", result, err)
	}
}

func TestServiceUpdateBuiltInModelPricesPreservesPatchPresence(t *testing.T) {
	inputPrice := 1.0
	outputPrice := 2.0
	cachedInputPrice := 0.5
	store := &providerModelStoreStub{catalog: []port.ManagementProviderModelCatalogItem{{
		ID: "provider_model_gpt_real", ProviderCode: "gpt", Model: "gpt-real", Scope: "built_in", Status: "active",
		InputUSDPer1M: &inputPrice, OutputUSDPer1M: &outputPrice, CachedInputUSDPer1M: &cachedInputPrice,
	}}}
	nextInputPrice := 11.0
	result, err := NewService(store).UpdateCustomModel(context.Background(), CustomModelUpdateInput{
		ProviderCode: "gpt", ID: "provider_model_gpt_real", ActorSystemAccountID: "sys_admin", ActorRole: "admin",
		Fields: CustomModelMutation{InputUSDPer1M: OptionalFloat{Set: true, Value: &nextInputPrice}},
	})
	if err != nil {
		t.Fatalf("UpdateCustomModel() error = %v", err)
	}
	if len(store.builtInUpdateInputs) != 1 || !store.builtInUpdateInputs[0].InputUSDPer1M.Present || store.builtInUpdateInputs[0].OutputUSDPer1M.Present {
		t.Fatalf("built-in store input = %+v", store.builtInUpdateInputs)
	}
	if result.ID != "provider_model_gpt_real" || result.InputUSDPer1M == nil || *result.InputUSDPer1M != nextInputPrice || result.OutputUSDPer1M == nil || *result.OutputUSDPer1M != outputPrice {
		t.Fatalf("result = %+v", result)
	}
}

func TestServiceUpdateBuiltInModelAllowsCompleteCatalogConfiguration(t *testing.T) {
	inputPrice := 1.0
	contextTokens := 1_050_000
	maxInputTokens := 922_000
	maxOutputTokens := 128_000
	store := &providerModelStoreStub{catalog: []port.ManagementProviderModelCatalogItem{{
		ID: "provider_model_gpt_real", ProviderCode: "gpt", Model: "gpt-real", Scope: "built_in", Status: "active",
		Mode: "text", SupportedAPIProtocols: []string{"responses"}, InputUSDPer1M: &inputPrice,
	}}}
	result, err := NewService(store).UpdateCustomModel(context.Background(), CustomModelUpdateInput{
		ProviderCode: "gpt", ID: "provider_model_gpt_real", ActorSystemAccountID: "sys_admin", ActorRole: "admin",
		Fields: CustomModelMutation{
			Status: OptionalString{Set: true, Value: "disabled"}, Mode: OptionalString{Set: true, Value: "text"},
			SupportedAPIProtocols:     OptionalStringList{Set: true, Value: []string{"responses", "chat_completions"}},
			SupportedServiceTiers:     OptionalStringList{Set: true, Value: []string{"priority"}},
			SupportedReasoningEfforts: OptionalStringList{Set: true, Value: []string{"low", "high"}},
			DefaultReasoningEffort:    OptionalString{Set: true, Value: "high"},
			ReleaseDate:               OptionalString{Set: true, Value: "2026-07-16"}, ShutdownDate: OptionalString{Set: true, Value: ""},
			ContextWindowTokens: OptionalInt{Set: true, Value: &contextTokens}, MaxInputTokens: OptionalInt{Set: true, Value: &maxInputTokens}, MaxOutputTokens: OptionalInt{Set: true, Value: &maxOutputTokens},
		},
	})
	if err != nil {
		t.Fatalf("UpdateCustomModel() error = %v", err)
	}
	if result.Status != "disabled" || !slices.Equal(result.SupportedAPIProtocols, []string{"responses", "chat_completions"}) ||
		!slices.Equal(result.SupportedServiceTiers, []string{"priority"}) || result.DefaultReasoningEffort != "high" ||
		result.ContextWindowTokens == nil || *result.ContextWindowTokens != contextTokens || result.MaxInputTokens == nil || *result.MaxInputTokens != maxInputTokens {
		t.Fatalf("result = %+v", result)
	}
}

func TestServiceCreateCustomModelRejectsOrphanTierPrices(t *testing.T) {
	price := 1.0
	store := &providerModelStoreStub{providers: map[string]port.ManagementProviderModelProvider{
		"anthropic": {Code: "anthropic", Enabled: true},
	}}
	service := NewService(store)

	_, err := service.CreateCustomModel(context.Background(), CustomModelCreateInput{
		ProviderCode: "anthropic", ActorSystemAccountID: "sys_admin", ActorRole: "admin", TargetSystemAccountID: "sys_user",
		Fields: CustomModelMutation{Model: OptionalString{Set: true, Value: "image-orphan"}, Mode: OptionalString{Set: true, Value: "image"},
			ServiceTierPrices: OptionalProviderModelPriceMap{Set: true, Value: map[string]port.ManagementProviderModelPriceSet{"fast": {InputUSDPer1M: &price}}}},
	})
	message, ok := CustomModelValidationMessage(err)
	if !ok || message != "只有文本自定义模型支持服务档位价格" {
		t.Fatalf("non-text tier price message = %q, %v; err = %v", message, ok, err)
	}

	_, err = service.CreateCustomModel(context.Background(), CustomModelCreateInput{
		ProviderCode: "anthropic", ActorSystemAccountID: "sys_admin", ActorRole: "admin", TargetSystemAccountID: "sys_user",
		Fields: CustomModelMutation{Model: OptionalString{Set: true, Value: "text-orphan"}, SupportedServiceTiers: OptionalStringList{Set: true, Value: []string{"fast"}},
			ServiceTierPrices: OptionalProviderModelPriceMap{Set: true, Value: map[string]port.ManagementProviderModelPriceSet{"priority": {InputUSDPer1M: &price}}}},
	})
	message, ok = CustomModelValidationMessage(err)
	if !ok || message != "服务档位价格必须属于模型支持的服务等级" {
		t.Fatalf("orphan tier price message = %q, %v; err = %v", message, ok, err)
	}
}

func TestServiceCreateCustomModelRejectsInt4Overflow(t *testing.T) {
	price := 1.25
	tooLarge := math.MaxInt32 + 1
	store := &providerModelStoreStub{
		providers: map[string]port.ManagementProviderModelProvider{"gpt": {Code: "gpt", Enabled: true}},
	}
	_, err := NewService(store).CreateCustomModel(context.Background(), CustomModelCreateInput{
		ProviderCode:          "gpt",
		ActorSystemAccountID:  "sys_admin",
		ActorRole:             "admin",
		TargetSystemAccountID: "sys_user",
		Fields: CustomModelMutation{
			Model:           OptionalString{Set: true, Value: "custom-chat"},
			MaxOutputTokens: OptionalInt{Set: true, Value: &tooLarge},
			InputUSDPer1M:   OptionalFloat{Set: true, Value: &price},
		},
	})
	message, ok := CustomModelValidationMessage(err)
	if !ok || message != "自定义模型参数无效" {
		t.Fatalf("overflow message = %q, %v; err = %v", message, ok, err)
	}
	if store.saveInput.Model != "" {
		t.Fatalf("save should not be called, got %+v", store.saveInput)
	}
}

func TestServiceUpdateCustomModelPrioritizesLookupAndPermissionBeforeBodyValidation(t *testing.T) {
	store := &providerModelStoreStub{customByID: map[string]port.ManagementProviderModelCatalogItem{
		"custom_model_1": {
			ID:              "custom_model_1",
			ProviderCode:    "gpt",
			Model:           "custom-chat",
			Scope:           "personal",
			SystemAccountID: "sys_owner",
			Status:          "active",
		},
	}}
	service := NewService(store)

	_, err := service.UpdateCustomModel(context.Background(), CustomModelUpdateInput{
		ProviderCode:         "gpt",
		ID:                   "missing",
		ActorSystemAccountID: "sys_owner",
		ActorRole:            "user",
		Fields:               CustomModelMutation{},
	})
	if !errors.Is(err, ErrCustomProviderModelNotFound) {
		t.Fatalf("missing err = %v, want ErrCustomProviderModelNotFound", err)
	}

	_, err = service.UpdateCustomModel(context.Background(), CustomModelUpdateInput{
		ProviderCode:         "gpt",
		ID:                   "custom_model_1",
		ActorSystemAccountID: "sys_other",
		ActorRole:            "user",
		Fields:               CustomModelMutation{},
	})
	message, ok := CustomModelForbiddenMessage(err)
	if !ok || message != "无权修改该自定义模型" {
		t.Fatalf("forbidden message = %q, %v; err = %v", message, ok, err)
	}
}

func TestServiceUpdateCustomModelRejectsInt4Overflow(t *testing.T) {
	price := 1.25
	tooLarge := math.MaxInt32 + 1
	store := &providerModelStoreStub{customByID: map[string]port.ManagementProviderModelCatalogItem{
		"custom_model_1": {
			ID:              "custom_model_1",
			ProviderCode:    "gpt",
			Model:           "custom-chat",
			Scope:           "personal",
			SystemAccountID: "sys_user",
			Status:          "active",
			InputUSDPer1M:   &price,
		},
	}}
	_, err := NewService(store).UpdateCustomModel(context.Background(), CustomModelUpdateInput{
		ProviderCode:         "gpt",
		ID:                   "custom_model_1",
		ActorSystemAccountID: "sys_user",
		ActorRole:            "user",
		Fields: CustomModelMutation{
			ContextWindowTokens: OptionalInt{Set: true, Value: &tooLarge},
		},
	})
	message, ok := CustomModelValidationMessage(err)
	if !ok || message != "自定义模型参数无效" {
		t.Fatalf("overflow message = %q, %v; err = %v", message, ok, err)
	}
	if store.saveInput.Model != "" {
		t.Fatalf("save should not be called, got %+v", store.saveInput)
	}
}

func TestServiceUpdateCustomModelRejectsModelChangeAndClearsDefaultWhenDisabled(t *testing.T) {
	price := 1.25
	existing := port.ManagementProviderModelCatalogItem{
		ID:              "custom_model_1",
		ProviderCode:    "gpt",
		Model:           "custom-chat",
		Scope:           "personal",
		SystemAccountID: "sys_user",
		Status:          "active",
		InputUSDPer1M:   &price,
	}
	store := &providerModelStoreStub{customByID: map[string]port.ManagementProviderModelCatalogItem{"custom_model_1": existing}}
	service := NewService(store)

	_, err := service.UpdateCustomModel(context.Background(), CustomModelUpdateInput{
		ProviderCode:         "gpt",
		ID:                   "custom_model_1",
		ActorSystemAccountID: "sys_user",
		ActorRole:            "user",
		Fields:               CustomModelMutation{Model: OptionalString{Set: true, Value: "other-model"}},
	})
	message, ok := CustomModelValidationMessage(err)
	if !ok || message != "模型 ID 创建后不能修改" {
		t.Fatalf("model change message = %q, %v; err = %v", message, ok, err)
	}

	invalidator := &customProviderModelInvalidatorStub{}
	service = NewServiceWithOptions(ServiceOptions{Store: store, Invalidator: invalidator})
	result, err := service.UpdateCustomModel(context.Background(), CustomModelUpdateInput{
		ProviderCode:         "gpt",
		ID:                   "custom_model_1",
		ActorSystemAccountID: "sys_user",
		ActorRole:            "user",
		Fields:               CustomModelMutation{Status: OptionalString{Set: true, Value: "disabled"}},
	})
	if err != nil {
		t.Fatalf("UpdateCustomModel() error = %v", err)
	}
	if result.Status != "disabled" || store.clearInput.Model != "custom-chat" || store.clearInput.SystemAccountID != "sys_user" {
		t.Fatalf("result=%+v clear=%+v", result, store.clearInput)
	}
	if invalidator.reason != CustomProviderModelSavedReason {
		t.Fatalf("invalidation reason = %q", invalidator.reason)
	}

	clearErr := errors.New("clear default failed")
	store.clearErr = clearErr
	invalidator.reason = ""
	_, err = service.UpdateCustomModel(context.Background(), CustomModelUpdateInput{
		ProviderCode:         "gpt",
		ID:                   "custom_model_1",
		ActorSystemAccountID: "sys_user",
		ActorRole:            "user",
		Fields:               CustomModelMutation{Status: OptionalString{Set: true, Value: "disabled"}},
	})
	if !errors.Is(err, clearErr) {
		t.Fatalf("UpdateCustomModel() clear error = %v, want %v", err, clearErr)
	}
	if invalidator.reason != CustomProviderModelSavedReason {
		t.Fatalf("invalidation should happen before clear error, reason = %q", invalidator.reason)
	}
}

func TestServiceUpdateGlobalCustomModelClearsPersonalAndSystemDefaultsWhenDisabled(t *testing.T) {
	price := 1.25
	store := &providerModelStoreStub{customByID: map[string]port.ManagementProviderModelCatalogItem{
		"custom_model_global": {
			ID:            "custom_model_global",
			ProviderCode:  "gpt",
			Model:         "global-chat",
			Scope:         "global",
			Status:        "active",
			InputUSDPer1M: &price,
		},
	}}

	result, err := NewService(store).UpdateCustomModel(context.Background(), CustomModelUpdateInput{
		ProviderCode:         "gpt",
		ID:                   "custom_model_global",
		ActorSystemAccountID: "sys_admin",
		ActorRole:            "admin",
		Fields:               CustomModelMutation{Status: OptionalString{Set: true, Value: "disabled"}},
	})
	if err != nil {
		t.Fatalf("UpdateCustomModel() error = %v", err)
	}
	if result.Status != "disabled" {
		t.Fatalf("result = %+v", result)
	}
	if store.clearInput.ProviderCode != "gpt" || store.clearInput.SystemAccountID != "" || store.clearInput.Model != "global-chat" {
		t.Fatalf("personal default clear input = %+v", store.clearInput)
	}
	if store.clearSystemInput.ProviderCode != "gpt" || store.clearSystemInput.Model != "global-chat" {
		t.Fatalf("system default clear input = %+v", store.clearSystemInput)
	}
}

func TestServiceUpdateCustomModelLogsCommittedCacheInvalidationFailureWithoutFailingWrite(t *testing.T) {
	price := 1.25
	existing := port.ManagementProviderModelCatalogItem{
		ID:              "custom_model_1",
		ProviderCode:    "gpt",
		Model:           "custom-chat",
		Scope:           "personal",
		SystemAccountID: "sys_user",
		Status:          "active",
		InputUSDPer1M:   &price,
	}
	store := &providerModelStoreStub{
		customByID: map[string]port.ManagementProviderModelCatalogItem{"custom_model_1": existing},
	}
	invalidator := &customProviderModelInvalidatorStub{err: errors.New("invalidation failed")}
	service := NewServiceWithOptions(ServiceOptions{Store: store, Invalidator: invalidator})

	result, err := service.UpdateCustomModel(context.Background(), CustomModelUpdateInput{
		ProviderCode:         "gpt",
		ID:                   "custom_model_1",
		ActorSystemAccountID: "sys_user",
		ActorRole:            "user",
		Fields:               CustomModelMutation{Notes: OptionalString{Set: true, Value: "updated"}},
	})
	if err != nil || result.Notes != "updated" {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
	if store.saveInput.Notes != "updated" {
		t.Fatalf("save=%+v", store.saveInput)
	}
	if invalidator.calls != 1 || invalidator.reason != CustomProviderModelSavedReason {
		t.Fatalf("invalidation calls=%d reason=%q", invalidator.calls, invalidator.reason)
	}
}

func TestServiceDeleteCustomModelChecksBindingsAndInvalidates(t *testing.T) {
	existing := port.ManagementProviderModelCatalogItem{
		ID:              "custom_model_1",
		ProviderCode:    "gpt",
		Model:           "custom-chat",
		Scope:           "personal",
		SystemAccountID: "sys_user",
		Status:          "disabled",
	}
	store := &providerModelStoreStub{
		customByID:     map[string]port.ManagementProviderModelCatalogItem{"custom_model_1": existing},
		bindingSummary: port.ManagementCustomProviderModelBindingSummary{SupportedModelAccountCount: 1, TotalAccountCount: 1},
	}
	_, err := NewService(store).DeleteCustomModel(context.Background(), CustomModelDeleteInput{
		ProviderCode:         "gpt",
		ID:                   "custom_model_1",
		ActorSystemAccountID: "sys_user",
		ActorRole:            "user",
	})
	message, ok := CustomModelBoundMessage(err)
	if !ok || !strings.Contains(message, "1 个账户支持模型") {
		t.Fatalf("bound message = %q, %v; err = %v", message, ok, err)
	}
	if store.deleteID != "" {
		t.Fatalf("delete should not be called, got %q", store.deleteID)
	}

	invalidator := &customProviderModelInvalidatorStub{}
	store.bindingSummary = port.ManagementCustomProviderModelBindingSummary{}
	store.deleteResult = true
	result, err := NewServiceWithOptions(ServiceOptions{Store: store, Invalidator: invalidator}).DeleteCustomModel(context.Background(), CustomModelDeleteInput{
		ProviderCode:         "gpt",
		ID:                   "custom_model_1",
		ActorSystemAccountID: "sys_user",
		ActorRole:            "user",
	})
	if err != nil {
		t.Fatalf("DeleteCustomModel() error = %v", err)
	}
	if !result.Deleted || store.deleteID != "custom_model_1" || store.clearInput.Model != "custom-chat" {
		t.Fatalf("result=%+v delete=%q clear=%+v", result, store.deleteID, store.clearInput)
	}
	if invalidator.reason != CustomProviderModelDeletedReason {
		t.Fatalf("invalidation reason = %q", invalidator.reason)
	}

	clearErr := errors.New("clear default failed")
	store.clearErr = clearErr
	invalidator.reason = ""
	_, err = NewServiceWithOptions(ServiceOptions{Store: store, Invalidator: invalidator}).DeleteCustomModel(context.Background(), CustomModelDeleteInput{
		ProviderCode:         "gpt",
		ID:                   "custom_model_1",
		ActorSystemAccountID: "sys_user",
		ActorRole:            "user",
	})
	if !errors.Is(err, clearErr) {
		t.Fatalf("DeleteCustomModel() clear error = %v, want %v", err, clearErr)
	}
	if invalidator.reason != CustomProviderModelDeletedReason {
		t.Fatalf("invalidation should happen before clear error, reason = %q", invalidator.reason)
	}
}

func TestServiceDeleteCustomModelLogsCommittedCacheInvalidationFailureWithoutFailingWrite(t *testing.T) {
	existing := port.ManagementProviderModelCatalogItem{
		ID:              "custom_model_1",
		ProviderCode:    "gpt",
		Model:           "custom-chat",
		Scope:           "personal",
		SystemAccountID: "sys_user",
		Status:          "disabled",
	}
	store := &providerModelStoreStub{
		customByID:   map[string]port.ManagementProviderModelCatalogItem{"custom_model_1": existing},
		deleteResult: true,
	}
	invalidator := &customProviderModelInvalidatorStub{err: errors.New("invalidation failed")}
	service := NewServiceWithOptions(ServiceOptions{Store: store, Invalidator: invalidator})

	result, err := service.DeleteCustomModel(context.Background(), CustomModelDeleteInput{
		ProviderCode:         "gpt",
		ID:                   "custom_model_1",
		ActorSystemAccountID: "sys_user",
		ActorRole:            "user",
	})
	if err != nil || !result.Deleted {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
	if store.deleteID != "custom_model_1" {
		t.Fatalf("delete=%q", store.deleteID)
	}
	if invalidator.calls != 1 || invalidator.reason != CustomProviderModelDeletedReason {
		t.Fatalf("invalidation calls=%d reason=%q", invalidator.calls, invalidator.reason)
	}
}

type providerModelStoreStub struct {
	providers              map[string]port.ManagementProviderModelProvider
	enabledCodes           []string
	protocolCodes          map[string][]string
	catalog                []port.ManagementProviderModelCatalogItem
	catalogInput           port.ManagementProviderModelCatalogListInput
	setDefaultInput        port.ManagementProviderDefaultHealthCheckModelInput
	setDefaultResult       port.ManagementProviderDefaultHealthCheckModelPreference
	setDefaultErr          error
	setSystemDefaultInput  port.ManagementProviderSystemDefaultHealthCheckModelInput
	setSystemDefaultResult port.ManagementProviderDefaultHealthCheckModelPreference
	setSystemDefaultErr    error
	customByID             map[string]port.ManagementProviderModelCatalogItem
	customByScope          map[string]port.ManagementProviderModelCatalogItem
	saveInput              port.ManagementCustomProviderModelSaveInput
	saveResult             port.ManagementProviderModelCatalogItem
	deleteID               string
	deleteResult           bool
	bindingInput           port.ManagementCustomProviderModelBindingInput
	bindingSummary         port.ManagementCustomProviderModelBindingSummary
	clearInput             port.ManagementProviderDefaultHealthCheckModelClearInput
	clearErr               error
	clearSystemInput       port.ManagementProviderSystemDefaultHealthCheckModelClearInput
	clearSystemErr         error
	builtInUpdateInputs    []port.ManagementBuiltInProviderModelPriceUpdateInput
}

func (s *providerModelStoreStub) UpdateManagementBuiltInProviderModelPrices(_ context.Context, input port.ManagementBuiltInProviderModelPriceUpdateInput) (port.ManagementBuiltInProviderModelPriceUpdateResult, bool, error) {
	s.builtInUpdateInputs = append(s.builtInUpdateInputs, input)
	for index := range s.catalog {
		if s.catalog[index].ID == input.ID && s.catalog[index].ProviderCode == input.ProviderCode {
			applyBuiltInProviderModelOptionalString(&s.catalog[index].Status, input.Status)
			applyBuiltInProviderModelOptionalString(&s.catalog[index].Mode, input.Mode)
			applyBuiltInProviderModelOptionalStringList(&s.catalog[index].SupportedAPIProtocols, input.SupportedAPIProtocols)
			applyBuiltInProviderModelOptionalStringList(&s.catalog[index].SupportedServiceTiers, input.SupportedServiceTiers)
			applyBuiltInProviderModelOptionalStringList(&s.catalog[index].SupportedReasoningEfforts, input.SupportedReasoningEfforts)
			applyBuiltInProviderModelOptionalString(&s.catalog[index].DefaultReasoningEffort, input.DefaultReasoningEffort)
			applyBuiltInProviderModelOptionalString(&s.catalog[index].ReleaseDate, input.ReleaseDate)
			applyBuiltInProviderModelOptionalString(&s.catalog[index].ShutdownDate, input.ShutdownDate)
			applyBuiltInProviderModelOptionalInt(&s.catalog[index].ContextWindowTokens, input.ContextWindowTokens)
			applyBuiltInProviderModelOptionalInt(&s.catalog[index].MaxInputTokens, input.MaxInputTokens)
			applyBuiltInProviderModelOptionalInt(&s.catalog[index].MaxOutputTokens, input.MaxOutputTokens)
			applyBuiltInProviderModelOptionalFloat(&s.catalog[index].InputUSDPer1M, input.InputUSDPer1M)
			applyBuiltInProviderModelOptionalFloat(&s.catalog[index].OutputUSDPer1M, input.OutputUSDPer1M)
			applyBuiltInProviderModelOptionalFloat(&s.catalog[index].CachedInputUSDPer1M, input.CachedInputUSDPer1M)
			item := s.catalog[index]
			return port.ManagementBuiltInProviderModelPriceUpdateResult{
				ID: item.ID, ProviderCode: item.ProviderCode, InputUSDPer1M: item.InputUSDPer1M,
				Status: item.Status, Mode: item.Mode, SupportedAPIProtocols: append([]string(nil), item.SupportedAPIProtocols...),
				SupportedServiceTiers: append([]string(nil), item.SupportedServiceTiers...), SupportedReasoningEfforts: append([]string(nil), item.SupportedReasoningEfforts...),
				DefaultReasoningEffort: item.DefaultReasoningEffort, ReleaseDate: item.ReleaseDate, ShutdownDate: item.ShutdownDate,
				ContextWindowTokens: cloneIntPtr(item.ContextWindowTokens), MaxInputTokens: cloneIntPtr(item.MaxInputTokens), MaxOutputTokens: cloneIntPtr(item.MaxOutputTokens),
				OutputUSDPer1M: item.OutputUSDPer1M, CachedInputUSDPer1M: item.CachedInputUSDPer1M,
				ServiceTierPrices: cloneProviderModelPriceMap(item.ServiceTierPrices),
			}, true, nil
		}
	}
	return port.ManagementBuiltInProviderModelPriceUpdateResult{}, false, nil
}

func applyBuiltInProviderModelOptionalString(target *string, input port.ManagementProviderModelOptionalString) {
	if input.Present {
		*target = input.Value
	}
}

func applyBuiltInProviderModelOptionalStringList(target *[]string, input port.ManagementProviderModelOptionalStringList) {
	if input.Present {
		*target = append([]string(nil), input.Value...)
	}
}

func applyBuiltInProviderModelOptionalInt(target **int, input port.ManagementProviderModelOptionalInt) {
	if input.Present {
		*target = cloneIntPtr(input.Value)
	}
}

func applyBuiltInProviderModelOptionalFloat(target **float64, input port.ManagementProviderModelOptionalFloat) {
	if input.Present {
		*target = cloneFloatPtr(input.Value)
	}
}

func (s *providerModelStoreStub) FindManagementProviderModelProvider(_ context.Context, code string) (port.ManagementProviderModelProvider, bool, error) {
	provider, ok := s.providers[code]
	return provider, ok, nil
}

func (s *providerModelStoreStub) ListManagementEnabledModelProviderCodes(context.Context) ([]string, error) {
	return append([]string(nil), s.enabledCodes...), nil
}

func (s *providerModelStoreStub) ListManagementProviderCodesByProtocol(_ context.Context, protocolCode string, protocolVersion string) ([]string, error) {
	return append([]string(nil), s.protocolCodes[protocolCode+":"+protocolVersion]...), nil
}

func (s *providerModelStoreStub) ListManagementProviderModelCatalog(_ context.Context, input port.ManagementProviderModelCatalogListInput) ([]port.ManagementProviderModelCatalogItem, error) {
	s.catalogInput = input
	allowed := map[string]struct{}{}
	for _, code := range append(append([]string{}, input.BuiltInProviderCodes...), input.CustomProviderCodes...) {
		allowed[code] = struct{}{}
	}
	if len(allowed) == 0 {
		return nil, nil
	}
	items := make([]port.ManagementProviderModelCatalogItem, 0, len(s.catalog))
	for _, item := range s.catalog {
		if _, ok := allowed[item.ProviderCode]; ok {
			items = append(items, item)
		}
	}
	return items, nil
}

func (s *providerModelStoreStub) SetManagementProviderDefaultHealthCheckModel(_ context.Context, input port.ManagementProviderDefaultHealthCheckModelInput) (port.ManagementProviderDefaultHealthCheckModelPreference, error) {
	s.setDefaultInput = input
	if s.setDefaultErr != nil {
		return port.ManagementProviderDefaultHealthCheckModelPreference{}, s.setDefaultErr
	}
	if s.setDefaultResult.ProviderCode != "" || s.setDefaultResult.Model != "" {
		return s.setDefaultResult, nil
	}
	return port.ManagementProviderDefaultHealthCheckModelPreference{
		ProviderCode: input.ProviderCode,
		Model:        input.Model,
	}, nil
}

func (s *providerModelStoreStub) ClearManagementProviderDefaultHealthCheckModelIfModel(_ context.Context, input port.ManagementProviderDefaultHealthCheckModelClearInput) (bool, error) {
	s.clearInput = input
	if s.clearErr != nil {
		return false, s.clearErr
	}
	return true, nil
}

func (s *providerModelStoreStub) SetManagementProviderSystemDefaultHealthCheckModel(_ context.Context, input port.ManagementProviderSystemDefaultHealthCheckModelInput) (port.ManagementProviderDefaultHealthCheckModelPreference, error) {
	s.setSystemDefaultInput = input
	if s.setSystemDefaultErr != nil {
		return port.ManagementProviderDefaultHealthCheckModelPreference{}, s.setSystemDefaultErr
	}
	if s.setSystemDefaultResult.ProviderCode != "" || s.setSystemDefaultResult.Model != "" {
		return s.setSystemDefaultResult, nil
	}
	return port.ManagementProviderDefaultHealthCheckModelPreference{
		ProviderCode: input.ProviderCode,
		Model:        input.Model,
	}, nil
}

func (s *providerModelStoreStub) ClearManagementProviderSystemDefaultHealthCheckModelIfModel(_ context.Context, input port.ManagementProviderSystemDefaultHealthCheckModelClearInput) (bool, error) {
	s.clearSystemInput = input
	if s.clearSystemErr != nil {
		return false, s.clearSystemErr
	}
	return true, nil
}

func (s *providerModelStoreStub) FindManagementCustomProviderModel(_ context.Context, id string) (port.ManagementProviderModelCatalogItem, bool, error) {
	item, ok := s.customByID[id]
	return item, ok, nil
}

func (s *providerModelStoreStub) FindManagementCustomProviderModelByScope(_ context.Context, input port.ManagementCustomProviderModelScopeInput) (port.ManagementProviderModelCatalogItem, bool, error) {
	item, ok := s.customByScope[customProviderModelScopeKey(input.ProviderCode, input.Scope, input.SystemAccountID, input.Model)]
	return item, ok, nil
}

func (s *providerModelStoreStub) SaveManagementCustomProviderModel(_ context.Context, input port.ManagementCustomProviderModelSaveInput) (port.ManagementProviderModelCatalogItem, error) {
	s.saveInput = input
	if s.saveResult.ID != "" {
		return s.saveResult, nil
	}
	return port.ManagementProviderModelCatalogItem{
		ID:                        input.ID,
		ProviderCode:              input.ProviderCode,
		Model:                     input.Model,
		Scope:                     input.Scope,
		SystemAccountID:           input.SystemAccountID,
		Status:                    input.Status,
		Mode:                      input.Mode,
		SupportedAPIProtocols:     append([]string(nil), input.SupportedAPIProtocols...),
		SupportedServiceTiers:     append([]string(nil), input.SupportedServiceTiers...),
		SupportedReasoningEfforts: append([]string(nil), input.SupportedReasoningEfforts...),
		DefaultReasoningEffort:    input.DefaultReasoningEffort,
		ReleaseDate:               input.ReleaseDate,
		ShutdownDate:              input.ShutdownDate,
		ContextWindowTokens:       input.ContextWindowTokens,
		MaxInputTokens:            input.MaxInputTokens,
		MaxOutputTokens:           input.MaxOutputTokens,
		InputUSDPer1M:             input.InputUSDPer1M,
		OutputUSDPer1M:            input.OutputUSDPer1M,
		CachedInputUSDPer1M:       input.CachedInputUSDPer1M,
		CacheWriteUSDPer1M:        input.CacheWriteUSDPer1M,
		CacheWrite1hUSDPer1M:      input.CacheWrite1hUSDPer1M,
		ServiceTierPrices:         cloneProviderModelPriceMap(input.ServiceTierPrices),
		ImageInputUSDPer1M:        input.ImageInputUSDPer1M,
		ImageOutputUSDPer1M:       input.ImageOutputUSDPer1M,
		AudioInputUSDPer1M:        input.AudioInputUSDPer1M,
		AudioOutputUSDPer1M:       input.AudioOutputUSDPer1M,
		OutputUSDPerImage:         input.OutputUSDPerImage,
		SupportsPromptCaching:     input.CachedInputUSDPer1M != nil,
		CatalogVisible:            true,
		PricingNotes:              input.PricingNotes,
		CapabilityNotes:           input.CapabilityNotes,
		Notes:                     input.Notes,
		Source:                    "custom-" + input.Scope,
	}, nil
}

func (s *providerModelStoreStub) DeleteManagementCustomProviderModel(_ context.Context, id string) (bool, error) {
	s.deleteID = id
	return s.deleteResult, nil
}

func (s *providerModelStoreStub) GetManagementCustomProviderModelBindingSummary(_ context.Context, input port.ManagementCustomProviderModelBindingInput) (port.ManagementCustomProviderModelBindingSummary, error) {
	s.bindingInput = input
	return s.bindingSummary, nil
}

func customProviderModelScopeKey(providerCode string, scope string, systemAccountID string, model string) string {
	return providerCode + "\n" + scope + "\n" + systemAccountID + "\n" + model
}

type customProviderModelInvalidatorStub struct {
	reason string
	err    error
	calls  int
}

func (s *customProviderModelInvalidatorStub) InvalidateCustomProviderModelChanged(_ context.Context, reason string) error {
	s.calls++
	s.reason = reason
	return s.err
}
