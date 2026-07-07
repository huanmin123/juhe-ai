package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"juhe-ai/backend-go/internal/store/port"
	"juhe-ai/backend-go/internal/store/postgres/postgresqueries"
)

func (s *Store) FindManagementProviderModelProvider(ctx context.Context, code string) (port.ManagementProviderModelProvider, bool, error) {
	return findManagementProviderModelProvider(ctx, s.queries(), code)
}

func (s *Store) ListManagementEnabledModelProviderCodes(ctx context.Context) ([]string, error) {
	return listManagementEnabledModelProviderCodes(ctx, s.queries())
}

func (s *Store) ListManagementProviderCodesByProtocol(ctx context.Context, protocolCode string, protocolVersion string) ([]string, error) {
	return listManagementProviderCodesByProtocol(ctx, s.queries(), protocolCode, protocolVersion)
}

func (s *Store) ListManagementProviderModelCatalog(ctx context.Context, input port.ManagementProviderModelCatalogListInput) ([]port.ManagementProviderModelCatalogItem, error) {
	return listManagementProviderModelCatalog(ctx, s.queries(), input)
}

func (s *Store) SetManagementProviderDefaultTestModel(ctx context.Context, input port.ManagementProviderDefaultTestModelInput) (port.ManagementProviderDefaultTestModelPreference, error) {
	return setManagementProviderDefaultTestModel(ctx, s.queries(), input)
}

func findManagementProviderModelProvider(
	ctx context.Context,
	q *postgresqueries.Queries,
	code string,
) (port.ManagementProviderModelProvider, bool, error) {
	row, err := q.FindManagementProviderModelProvider(ctx, code)
	if err != nil {
		if err == pgx.ErrNoRows {
			return port.ManagementProviderModelProvider{}, false, nil
		}
		return port.ManagementProviderModelProvider{}, false, fmt.Errorf("find management provider model provider: %w", err)
	}
	return port.ManagementProviderModelProvider{
		Code:       row.Code,
		Enabled:    row.Enabled,
		ParentCode: textValue(row.ParentCode),
	}, true, nil
}

func listManagementEnabledModelProviderCodes(ctx context.Context, q *postgresqueries.Queries) ([]string, error) {
	codes, err := q.ListManagementEnabledModelProviderCodes(ctx)
	if err != nil {
		return nil, fmt.Errorf("list management enabled model provider codes: %w", err)
	}
	return codes, nil
}

func listManagementProviderCodesByProtocol(
	ctx context.Context,
	q *postgresqueries.Queries,
	protocolCode string,
	protocolVersion string,
) ([]string, error) {
	codes, err := q.ListManagementProviderCodesByProtocol(ctx, postgresqueries.ListManagementProviderCodesByProtocolParams{
		ProtocolCode:    protocolCode,
		ProtocolVersion: protocolVersion,
	})
	if err != nil {
		return nil, fmt.Errorf("list management provider codes by protocol: %w", err)
	}
	return codes, nil
}

func listManagementProviderModelCatalog(
	ctx context.Context,
	q *postgresqueries.Queries,
	input port.ManagementProviderModelCatalogListInput,
) ([]port.ManagementProviderModelCatalogItem, error) {
	rows, err := q.ListManagementProviderModelCatalog(ctx, postgresqueries.ListManagementProviderModelCatalogParams{
		BuiltInProviderCodes: input.BuiltInProviderCodes,
		CustomProviderCodes:  input.CustomProviderCodes,
		SystemAccountID:      input.SystemAccountID,
		IncludeInactive:      input.IncludeInactive,
	})
	if err != nil {
		return nil, fmt.Errorf("list management provider model catalog: %w", err)
	}
	items := make([]port.ManagementProviderModelCatalogItem, 0, len(rows))
	for _, row := range rows {
		item, err := managementProviderModelCatalogItemFromRow(row)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

func setManagementProviderDefaultTestModel(
	ctx context.Context,
	q *postgresqueries.Queries,
	input port.ManagementProviderDefaultTestModelInput,
) (port.ManagementProviderDefaultTestModelPreference, error) {
	row, err := q.UpsertManagementProviderDefaultTestModelPreference(ctx, postgresqueries.UpsertManagementProviderDefaultTestModelPreferenceParams{
		SystemAccountID: input.SystemAccountID,
		ProviderCode:    input.ProviderCode,
		Model:           input.Model,
	})
	if err != nil {
		return port.ManagementProviderDefaultTestModelPreference{}, fmt.Errorf("set management provider default test model: %w", err)
	}
	return port.ManagementProviderDefaultTestModelPreference{
		ProviderCode: row.ProviderCode,
		Model:        row.Model,
	}, nil
}

func managementProviderModelCatalogItemFromRow(row postgresqueries.ListManagementProviderModelCatalogRow) (port.ManagementProviderModelCatalogItem, error) {
	protocols, err := decodeProviderStringArray(row.SupportedApiProtocolsJson, "provider model supported_api_protocols_json")
	if err != nil {
		return port.ManagementProviderModelCatalogItem{}, err
	}
	return port.ManagementProviderModelCatalogItem{
		ID:                    row.ID,
		ProviderCode:          row.ProviderCode,
		Model:                 row.Model,
		Scope:                 row.Scope,
		SystemAccountID:       textValue(row.SystemAccountID),
		Status:                row.Status,
		Mode:                  textValue(row.Mode),
		CatalogOrder:          int4Ptr(row.CatalogOrder),
		ReleaseDate:           textValue(row.ReleaseDate),
		ShutdownDate:          textValue(row.ShutdownDate),
		SupportedAPIProtocols: protocols,
		PricingModel:          textValue(row.PricingModel),
		ContextWindowTokens:   int4Ptr(row.ContextWindowTokens),
		MaxInputTokens:        int4Ptr(row.MaxInputTokens),
		MaxOutputTokens:       int4Ptr(row.MaxOutputTokens),
		MaxTokens:             int4Ptr(row.MaxTokens),
		InputUSDPer1M:         float8Ptr(row.InputUsdPer1m),
		OutputUSDPer1M:        float8Ptr(row.OutputUsdPer1m),
		CachedInputUSDPer1M:   float8Ptr(row.CachedInputUsdPer1m),
		CacheWriteUSDPer1M:    float8Ptr(row.CacheWriteUsdPer1m),
		CacheWrite1hUSDPer1M:  float8Ptr(row.CacheWrite1hUsdPer1m),
		ImageInputUSDPer1M:    float8Ptr(row.ImageInputUsdPer1m),
		ImageOutputUSDPer1M:   float8Ptr(row.ImageOutputUsdPer1m),
		AudioInputUSDPer1M:    float8Ptr(row.AudioInputUsdPer1m),
		AudioOutputUSDPer1M:   float8Ptr(row.AudioOutputUsdPer1m),
		OutputUSDPerImage:     float8Ptr(row.OutputUsdPerImage),
		SupportsPromptCaching: row.SupportsPromptCaching,
		SupportsServiceTier:   row.SupportsServiceTier,
		CatalogVisible:        row.CatalogVisible,
		Source:                row.Source,
		CreatedAt:             timestamptzValue(row.CreatedAt),
		UpdatedAt:             timestamptzValue(row.UpdatedAt),
	}, nil
}

func int4Ptr(value pgtype.Int4) *int {
	if !value.Valid {
		return nil
	}
	output := int(value.Int32)
	return &output
}

func float8Ptr(value pgtype.Float8) *float64 {
	if !value.Valid {
		return nil
	}
	return &value.Float64
}

var _ port.ManagementProviderModelCatalogReader = (*Store)(nil)
var _ port.ManagementProviderDefaultTestModelWriter = (*Store)(nil)
