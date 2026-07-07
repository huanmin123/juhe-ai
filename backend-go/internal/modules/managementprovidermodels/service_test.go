package managementprovidermodels

import (
	"context"
	"testing"

	"juhe-ai/backend-go/internal/store/port"
)

func TestServiceModelOptionsUsesProtocolScopeAndDedupe(t *testing.T) {
	store := &providerModelStoreStub{
		protocolCodes: map[string][]string{
			"openai:v1": {"gpt"},
		},
		catalog: []port.ManagementProviderModelCatalogItem{
			{ProviderCode: "gpt", Model: "gpt-5.5", Scope: "built_in", Status: "active", SupportedAPIProtocols: []string{"chat_completions"}},
			{ProviderCode: "gpt", Model: "gpt-5.5", Scope: "global", Status: "active", SupportedAPIProtocols: []string{"responses"}},
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
			{ProviderCode: "gpt", Model: "gpt-alias", Scope: "personal", SystemAccountID: "sys_user", Status: "active", Source: "custom-personal", PricingModel: "gpt-5.5"},
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
	if alias == nil || alias.Scope != "personal" || alias.Source != "custom-personal" || alias.PricingModel != "gpt-5.5" {
		t.Fatalf("alias model = %+v", alias)
	}
	if !store.catalogInput.IncludeInactive || store.catalogInput.SystemAccountID != "sys_user" {
		t.Fatalf("catalog input = %+v", store.catalogInput)
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

func TestServiceModelsHybridKeepsProviderModelsDistinct(t *testing.T) {
	price := 1.0
	store := &providerModelStoreStub{
		providers: map[string]port.ManagementProviderModelProvider{
			"hybrid": {Code: "hybrid", Enabled: true},
		},
		enabledCodes: []string{"gpt", "deepseek"},
		catalog: []port.ManagementProviderModelCatalogItem{
			{ProviderCode: "gpt", Model: "shared-model", Scope: "built_in", Status: "active", Source: "seed", InputUSDPer1M: &price},
			{ProviderCode: "deepseek", Model: "shared-model", Scope: "built_in", Status: "active", Source: "seed", InputUSDPer1M: &price},
			{ProviderCode: "hybrid", Model: "hybrid-direct", Scope: "personal", Status: "active", Source: "custom-personal", InputUSDPer1M: &price},
		},
	}
	service := NewService(store)

	models, err := service.Models(context.Background(), ModelListInput{ProviderCode: "hybrid"})
	if err != nil {
		t.Fatalf("Models() error = %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("hybrid models = %+v, want both provider entries", models)
	}
	if findModelCatalogItem(models, "hybrid-direct") != nil {
		t.Fatalf("hybrid models = %+v, want no hybrid source entries", models)
	}
}

func TestServiceModelsReturnsProviderNotFound(t *testing.T) {
	service := NewService(&providerModelStoreStub{})
	_, err := service.Models(context.Background(), ModelListInput{ProviderCode: "missing"})
	if err != ErrProviderNotFound {
		t.Fatalf("Models() error = %v, want ErrProviderNotFound", err)
	}
}

type providerModelStoreStub struct {
	providers     map[string]port.ManagementProviderModelProvider
	enabledCodes  []string
	protocolCodes map[string][]string
	catalog       []port.ManagementProviderModelCatalogItem
	catalogInput  port.ManagementProviderModelCatalogListInput
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
