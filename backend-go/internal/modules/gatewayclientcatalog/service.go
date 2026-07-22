package gatewayclientcatalog

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

type Service struct {
	reader port.GatewayClientCatalogReader
}

type APIKeyInput struct {
	SystemAccountID string
	Bindings        []Binding
}

type Binding struct {
	ProviderCode string
	Status       string
}

func NewService(reader port.GatewayClientCatalogReader) *Service {
	return &Service{reader: reader}
}

func (s *Service) Public(ctx context.Context) ([]port.GatewayClientCatalogModel, error) {
	if s == nil || s.reader == nil {
		return nil, fmt.Errorf("gateway client catalog reader is required")
	}
	providers, err := s.reader.ListGatewayClientCatalogProviders(ctx)
	if err != nil {
		return nil, fmt.Errorf("list gateway client catalog providers: %w", err)
	}
	providerCodes := publicProviderCodes(providers)
	if len(providerCodes) == 0 {
		return []port.GatewayClientCatalogModel{}, nil
	}
	return s.list(ctx, port.GatewayClientCatalogModelListInput{LogicalProviderCodes: providerCodes})
}

func (s *Service) APIKey(ctx context.Context, input APIKeyInput) ([]port.GatewayClientCatalogModel, error) {
	if s == nil || s.reader == nil {
		return nil, fmt.Errorf("gateway client catalog reader is required")
	}
	providerCodes := APIKeyBindingProviderCodes(input.Bindings)
	if len(providerCodes) == 0 {
		return []port.GatewayClientCatalogModel{}, nil
	}
	return s.list(ctx, port.GatewayClientCatalogModelListInput{
		LogicalProviderCodes: providerCodes,
		SystemAccountID:      strings.TrimSpace(input.SystemAccountID),
	})
}

func (s *Service) list(ctx context.Context, input port.GatewayClientCatalogModelListInput) ([]port.GatewayClientCatalogModel, error) {
	models, err := s.reader.ListGatewayClientCatalogModels(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("list gateway client catalog models: %w", err)
	}
	return SelectClientModels(filterModelsToRequestedScope(models, input)), nil
}

func filterModelsToRequestedScope(items []port.GatewayClientCatalogModel, input port.GatewayClientCatalogModelListInput) []port.GatewayClientCatalogModel {
	providerCodes := make(map[string]struct{}, len(input.LogicalProviderCodes))
	for _, providerCode := range input.LogicalProviderCodes {
		if code := normalizeProviderCode(providerCode); code != "" {
			providerCodes[code] = struct{}{}
		}
	}
	owner := strings.TrimSpace(input.SystemAccountID)
	filtered := make([]port.GatewayClientCatalogModel, 0, len(items))
	for _, item := range items {
		requestedProviderCode := normalizeProviderCode(item.RequestedProviderCode)
		if requestedProviderCode == "" {
			requestedProviderCode = normalizeProviderCode(item.ProviderCode)
		}
		if _, allowed := providerCodes[requestedProviderCode]; !allowed {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(item.Scope), "personal") &&
			(owner == "" || strings.TrimSpace(item.SystemAccountID) != owner) {
			continue
		}
		filtered = append(filtered, item)
	}
	return filtered
}

func publicProviderCodes(providers []port.GatewayClientCatalogProvider) []string {
	codes := make(map[string]struct{}, len(providers))
	for _, provider := range providers {
		code := normalizeProviderCode(provider.Code)
		if provider.Enabled && code != "" && code != "hybrid" {
			codes[code] = struct{}{}
		}
	}
	return sortedKeys(codes)
}

func APIKeyBindingProviderCodes(bindings []Binding) []string {
	codes := make(map[string]struct{}, len(bindings))
	for _, binding := range bindings {
		if binding.Status != "active" {
			continue
		}
		if code := normalizeProviderCode(binding.ProviderCode); code != "" {
			codes[code] = struct{}{}
		}
	}
	return sortedKeys(codes)
}

func SelectClientModels(items []port.GatewayClientCatalogModel) []port.GatewayClientCatalogModel {
	candidates := make([]port.GatewayClientCatalogModel, 0, len(items))
	for _, item := range items {
		item.Model = strings.TrimSpace(item.Model)
		item.ProviderCode = normalizeProviderCode(item.ProviderCode)
		if item.Model == "" || item.ProviderCode == "" ||
			item.Status != "active" ||
			!item.CatalogVisible || !hasClientVisiblePrice(item) {
			continue
		}
		candidates = append(candidates, cloneModel(item))
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		left, right := candidates[i], candidates[j]
		if rank := catalogScopeRank(left.Scope) - catalogScopeRank(right.Scope); rank != 0 {
			return rank > 0
		}
		return compareCatalogItems(left, right) < 0
	})

	selected := make([]port.GatewayClientCatalogModel, 0, len(candidates))
	seen := make(map[string]struct{}, len(candidates))
	for _, item := range candidates {
		if _, exists := seen[item.Model]; exists {
			continue
		}
		seen[item.Model] = struct{}{}
		selected = append(selected, item)
	}
	sort.SliceStable(selected, func(i, j int) bool {
		return compareCatalogItems(selected[i], selected[j]) < 0
	})
	return selected
}

func compareCatalogItems(left, right port.GatewayClientCatalogModel) int {
	leftDate, rightDate := sortableReleaseDate(left.ReleaseDate), sortableReleaseDate(right.ReleaseDate)
	if leftDate != rightDate {
		if leftDate > rightDate {
			return -1
		}
		return 1
	}
	if result := strings.Compare(left.ProviderCode, right.ProviderCode); result != 0 {
		return result
	}
	return strings.Compare(left.Model, right.Model)
}

func sortableReleaseDate(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if parsed, ok := parseCatalogTime(value); ok {
		return parsed.UTC().Format("2006-01-02")
	}
	return ""
}

func catalogScopeRank(scope string) int {
	switch strings.ToLower(strings.TrimSpace(scope)) {
	case "personal":
		return 3
	case "global":
		return 2
	default:
		return 1
	}
}

func hasClientVisiblePrice(item port.GatewayClientCatalogModel) bool {
	if item.InputUSDPer1M != nil || item.OutputUSDPer1M != nil || item.CachedInputUSDPer1M != nil ||
		item.CacheWriteUSDPer1M != nil || item.CacheWrite1hUSDPer1M != nil ||
		item.ImageInputUSDPer1M != nil || item.ImageOutputUSDPer1M != nil ||
		item.AudioInputUSDPer1M != nil || item.AudioOutputUSDPer1M != nil || item.OutputUSDPerImage != nil {
		return true
	}
	return len(item.ServiceTierPrices) > 0
}

func normalizeProviderCode(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func sortedKeys(values map[string]struct{}) []string {
	keys := make([]string, 0, len(values))
	for value := range values {
		keys = append(keys, value)
	}
	sort.Strings(keys)
	return keys
}

func cloneModel(item port.GatewayClientCatalogModel) port.GatewayClientCatalogModel {
	item.SupportedAPIProtocols = append([]string(nil), item.SupportedAPIProtocols...)
	item.SupportedServiceTiers = append([]string(nil), item.SupportedServiceTiers...)
	item.CodexSupportedReasoningLevels = append([]string(nil), item.CodexSupportedReasoningLevels...)
	if item.ServiceTierPrices != nil {
		prices := make(map[string]port.GatewayClientCatalogPriceSet, len(item.ServiceTierPrices))
		for key, value := range item.ServiceTierPrices {
			prices[key] = value
		}
		item.ServiceTierPrices = prices
	}
	return item
}

func parseCatalogTime(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	for _, layout := range []string{"2006-01-02", time.RFC3339Nano, time.RFC3339} {
		parsed, err := time.Parse(layout, value)
		if err == nil {
			return parsed, true
		}
	}
	return time.Time{}, false
}
