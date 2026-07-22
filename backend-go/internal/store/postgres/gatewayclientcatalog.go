package postgres

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"juhe-ai/backend-go/internal/store/port"
	"juhe-ai/backend-go/internal/store/postgres/postgresqueries"
)

const (
	maxGatewayClientCatalogProviderScope = 50
	maxGatewayClientCatalogModels        = 20_000
)

type gatewayClientCatalogQueries interface {
	ListGatewayClientCatalogProviders(context.Context) ([]postgresqueries.ListGatewayClientCatalogProvidersRow, error)
	ListGatewayClientCatalogModels(context.Context, postgresqueries.ListGatewayClientCatalogModelsParams) ([]postgresqueries.ListGatewayClientCatalogModelsRow, error)
}

func (s *Store) ListGatewayClientCatalogProviders(ctx context.Context) ([]port.GatewayClientCatalogProvider, error) {
	return listGatewayClientCatalogProviders(ctx, s.queries())
}

func (s *Store) ListGatewayClientCatalogModels(ctx context.Context, input port.GatewayClientCatalogModelListInput) ([]port.GatewayClientCatalogModel, error) {
	return listGatewayClientCatalogModels(ctx, s.queries(), input)
}

func listGatewayClientCatalogProviders(ctx context.Context, q gatewayClientCatalogQueries) ([]port.GatewayClientCatalogProvider, error) {
	rows, err := q.ListGatewayClientCatalogProviders(ctx)
	if err != nil {
		return nil, fmt.Errorf("list gateway client catalog providers: %w", err)
	}
	items := make([]port.GatewayClientCatalogProvider, 0, len(rows))
	for _, row := range rows {
		items = append(items, port.GatewayClientCatalogProvider{Code: row.Code, Enabled: row.Enabled})
	}
	return items, nil
}

func listGatewayClientCatalogModels(
	ctx context.Context,
	q gatewayClientCatalogQueries,
	input port.GatewayClientCatalogModelListInput,
) ([]port.GatewayClientCatalogModel, error) {
	providerCodes := normalizeGatewayClientCatalogProviderScope(input.LogicalProviderCodes)
	if len(providerCodes) == 0 {
		return []port.GatewayClientCatalogModel{}, nil
	}
	if len(providerCodes) > maxGatewayClientCatalogProviderScope {
		return nil, fmt.Errorf("gateway client catalog provider scope supports at most %d values", maxGatewayClientCatalogProviderScope)
	}
	rows, err := q.ListGatewayClientCatalogModels(ctx, postgresqueries.ListGatewayClientCatalogModelsParams{
		LogicalProviderCodes: providerCodes,
		SystemAccountID:      strings.TrimSpace(input.SystemAccountID),
	})
	if err != nil {
		return nil, fmt.Errorf("list gateway client catalog models: %w", err)
	}
	if len(rows) > maxGatewayClientCatalogModels {
		return nil, fmt.Errorf("gateway client catalog exceeds %d model candidates", maxGatewayClientCatalogModels)
	}

	items := make([]port.GatewayClientCatalogModel, 0, len(rows))
	for _, row := range rows {
		item, err := gatewayClientCatalogModelFromRow(row)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

func normalizeGatewayClientCatalogProviderScope(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value != "" {
			seen[value] = struct{}{}
		}
	}
	result := make([]string, 0, len(seen))
	for value := range seen {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func gatewayClientCatalogModelFromRow(row postgresqueries.ListGatewayClientCatalogModelsRow) (port.GatewayClientCatalogModel, error) {
	protocols, err := decodeProviderStringArray(row.SupportedApiProtocolsJson, "gateway client catalog supported_api_protocols_json")
	if err != nil {
		return port.GatewayClientCatalogModel{}, err
	}
	serviceTiers, err := decodeProviderStringArray(row.SupportedServiceTiersJson, "gateway client catalog supported_service_tiers_json")
	if err != nil {
		return port.GatewayClientCatalogModel{}, err
	}
	reasoningLevels, err := decodeProviderStringArray(row.CodexSupportedReasoningLevelsJson, "gateway client catalog codex_supported_reasoning_levels_json")
	if err != nil {
		return port.GatewayClientCatalogModel{}, err
	}
	managementPrices, err := decodeProviderModelPriceMap(row.ServiceTierPricesJson, "gateway client catalog service_tier_prices_json")
	if err != nil {
		return port.GatewayClientCatalogModel{}, err
	}
	prices := make(map[string]port.GatewayClientCatalogPriceSet, len(managementPrices))
	for tier, price := range managementPrices {
		prices[tier] = port.GatewayClientCatalogPriceSet{
			InputUSDPer1M: price.InputUSDPer1M, OutputUSDPer1M: price.OutputUSDPer1M,
			CachedInputUSDPer1M: price.CachedInputUSDPer1M, CacheWriteUSDPer1M: price.CacheWriteUSDPer1M,
			CacheWrite1hUSDPer1M: price.CacheWrite1hUSDPer1M, ImageInputUSDPer1M: price.ImageInputUSDPer1M,
			ImageOutputUSDPer1M: price.ImageOutputUSDPer1M, AudioInputUSDPer1M: price.AudioInputUSDPer1M,
			AudioOutputUSDPer1M: price.AudioOutputUSDPer1M, OutputUSDPerImage: price.OutputUSDPerImage,
		}
	}

	return port.GatewayClientCatalogModel{
		RequestedProviderCode:         row.RequestedProviderCode,
		ProviderCode:                  row.ProviderCode,
		Model:                         row.Model,
		Scope:                         row.Scope,
		SystemAccountID:               textValue(row.SystemAccountID),
		Status:                        row.Status,
		CatalogVisible:                row.CatalogVisible,
		ReleaseDate:                   textValue(row.ReleaseDate),
		CreatedAt:                     timestamptzValue(row.CreatedAt),
		SupportedAPIProtocols:         protocols,
		SupportedServiceTiers:         serviceTiers,
		CodexSupportedReasoningLevels: reasoningLevels,
		CodexDefaultReasoningLevel:    textValue(row.CodexDefaultReasoningLevel),
		CodexMultiAgentVersion:        textValue(row.CodexMultiAgentVersion),
		ContextWindowTokens:           int4Ptr(row.ContextWindowTokens),
		MaxInputTokens:                int4Ptr(row.MaxInputTokens),
		MaxOutputTokens:               int4Ptr(row.MaxOutputTokens),
		PricingNotes:                  textValue(row.PricingNotes),
		CapabilityNotes:               textValue(row.CapabilityNotes),
		Notes:                         textValue(row.Notes),
		InputUSDPer1M:                 float8Ptr(row.InputUsdPer1m),
		OutputUSDPer1M:                float8Ptr(row.OutputUsdPer1m),
		CachedInputUSDPer1M:           float8Ptr(row.CachedInputUsdPer1m),
		CacheWriteUSDPer1M:            float8Ptr(row.CacheWriteUsdPer1m),
		CacheWrite1hUSDPer1M:          float8Ptr(row.CacheWrite1hUsdPer1m),
		ImageInputUSDPer1M:            float8Ptr(row.ImageInputUsdPer1m),
		ImageOutputUSDPer1M:           float8Ptr(row.ImageOutputUsdPer1m),
		AudioInputUSDPer1M:            float8Ptr(row.AudioInputUsdPer1m),
		AudioOutputUSDPer1M:           float8Ptr(row.AudioOutputUsdPer1m),
		OutputUSDPerImage:             float8Ptr(row.OutputUsdPerImage),
		ServiceTierPrices:             prices,
	}, nil
}

var _ port.GatewayClientCatalogReader = (*Store)(nil)
