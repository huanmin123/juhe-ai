package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

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

func (s *Store) SetManagementProviderDefaultHealthCheckModel(ctx context.Context, input port.ManagementProviderDefaultHealthCheckModelInput) (port.ManagementProviderDefaultHealthCheckModelPreference, error) {
	return setManagementProviderDefaultHealthCheckModel(ctx, s.queries(), input)
}

func (s *Store) ClearManagementProviderDefaultHealthCheckModelIfModel(ctx context.Context, input port.ManagementProviderDefaultHealthCheckModelClearInput) (bool, error) {
	return clearManagementProviderDefaultHealthCheckModelIfModel(ctx, s.queries(), input)
}

func (s *Store) SetManagementProviderSystemDefaultHealthCheckModel(ctx context.Context, input port.ManagementProviderSystemDefaultHealthCheckModelInput) (port.ManagementProviderDefaultHealthCheckModelPreference, error) {
	return setManagementProviderSystemDefaultHealthCheckModel(ctx, s.queries(), input)
}

func (s *Store) ClearManagementProviderSystemDefaultHealthCheckModelIfModel(ctx context.Context, input port.ManagementProviderSystemDefaultHealthCheckModelClearInput) (bool, error) {
	return clearManagementProviderSystemDefaultHealthCheckModelIfModel(ctx, s.queries(), input)
}

func (s *Store) FindManagementCustomProviderModel(ctx context.Context, id string) (port.ManagementProviderModelCatalogItem, bool, error) {
	return findManagementCustomProviderModel(ctx, s.queries(), id)
}

func (s *Store) FindManagementCustomProviderModelByScope(ctx context.Context, input port.ManagementCustomProviderModelScopeInput) (port.ManagementProviderModelCatalogItem, bool, error) {
	return findManagementCustomProviderModelByScope(ctx, s.queries(), input)
}

func (s *Store) SaveManagementCustomProviderModel(ctx context.Context, input port.ManagementCustomProviderModelSaveInput) (port.ManagementProviderModelCatalogItem, error) {
	return saveManagementCustomProviderModel(ctx, s.queries(), input)
}

func (s *Store) DeleteManagementCustomProviderModel(ctx context.Context, id string) (bool, error) {
	return deleteManagementCustomProviderModel(ctx, s.queries(), id)
}

func (s *Store) GetManagementCustomProviderModelBindingSummary(ctx context.Context, input port.ManagementCustomProviderModelBindingInput) (port.ManagementCustomProviderModelBindingSummary, error) {
	return getManagementCustomProviderModelBindingSummary(ctx, s.queries(), input)
}

func (s *Store) UpdateManagementBuiltInProviderModelPrices(ctx context.Context, input port.ManagementBuiltInProviderModelPriceUpdateInput) (port.ManagementBuiltInProviderModelPriceUpdateResult, bool, error) {
	pricesJSON := []byte("{}")
	if input.ServiceTierPrices.Present {
		var err error
		pricesJSON, err = json.Marshal(input.ServiceTierPrices.Value)
		if err != nil {
			return port.ManagementBuiltInProviderModelPriceUpdateResult{}, false, fmt.Errorf("marshal built-in provider model service tier prices: %w", err)
		}
	}
	row, err := s.queries().UpdateManagementBuiltInProviderModelPrices(ctx, postgresqueries.UpdateManagementBuiltInProviderModelPricesParams{
		InputUsdPer1mPresent: input.InputUSDPer1M.Present, InputUsdPer1m: pgFloat8Ptr(input.InputUSDPer1M.Value),
		OutputUsdPer1mPresent: input.OutputUSDPer1M.Present, OutputUsdPer1m: pgFloat8Ptr(input.OutputUSDPer1M.Value),
		CachedInputUsdPer1mPresent: input.CachedInputUSDPer1M.Present, CachedInputUsdPer1m: pgFloat8Ptr(input.CachedInputUSDPer1M.Value),
		CacheWriteUsdPer1mPresent: input.CacheWriteUSDPer1M.Present, CacheWriteUsdPer1m: pgFloat8Ptr(input.CacheWriteUSDPer1M.Value),
		CacheWrite1hUsdPer1mPresent: input.CacheWrite1hUSDPer1M.Present, CacheWrite1hUsdPer1m: pgFloat8Ptr(input.CacheWrite1hUSDPer1M.Value),
		ServiceTierPricesPresent: input.ServiceTierPrices.Present, ServiceTierPricesJson: string(pricesJSON),
		ImageInputUsdPer1mPresent: input.ImageInputUSDPer1M.Present, ImageInputUsdPer1m: pgFloat8Ptr(input.ImageInputUSDPer1M.Value),
		ImageOutputUsdPer1mPresent: input.ImageOutputUSDPer1M.Present, ImageOutputUsdPer1m: pgFloat8Ptr(input.ImageOutputUSDPer1M.Value),
		AudioInputUsdPer1mPresent: input.AudioInputUSDPer1M.Present, AudioInputUsdPer1m: pgFloat8Ptr(input.AudioInputUSDPer1M.Value),
		AudioOutputUsdPer1mPresent: input.AudioOutputUSDPer1M.Present, AudioOutputUsdPer1m: pgFloat8Ptr(input.AudioOutputUSDPer1M.Value),
		OutputUsdPerImagePresent: input.OutputUSDPerImage.Present, OutputUsdPerImage: pgFloat8Ptr(input.OutputUSDPerImage.Value),
		ID: input.ID, ProviderCode: input.ProviderCode,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return port.ManagementBuiltInProviderModelPriceUpdateResult{}, false, nil
	}
	if err != nil {
		return port.ManagementBuiltInProviderModelPriceUpdateResult{}, false, fmt.Errorf("update built-in provider model prices: %w", err)
	}
	serviceTierPrices, err := decodeProviderModelPriceMap(row.ServiceTierPricesJson, "built-in provider model service_tier_prices_json")
	if err != nil {
		return port.ManagementBuiltInProviderModelPriceUpdateResult{}, false, err
	}
	return port.ManagementBuiltInProviderModelPriceUpdateResult{
		ID: row.ID, ProviderCode: row.ProviderCode,
		InputUSDPer1M: float8Ptr(row.InputUsdPer1m), OutputUSDPer1M: float8Ptr(row.OutputUsdPer1m),
		CachedInputUSDPer1M: float8Ptr(row.CachedInputUsdPer1m), CacheWriteUSDPer1M: float8Ptr(row.CacheWriteUsdPer1m),
		CacheWrite1hUSDPer1M: float8Ptr(row.CacheWrite1hUsdPer1m), ServiceTierPrices: serviceTierPrices,
		ImageInputUSDPer1M: float8Ptr(row.ImageInputUsdPer1m), ImageOutputUSDPer1M: float8Ptr(row.ImageOutputUsdPer1m),
		AudioInputUSDPer1M: float8Ptr(row.AudioInputUsdPer1m), AudioOutputUSDPer1M: float8Ptr(row.AudioOutputUsdPer1m),
		OutputUSDPerImage: float8Ptr(row.OutputUsdPerImage), UpdatedAt: timestamptzValue(row.UpdatedAt),
	}, true, nil
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

func setManagementProviderDefaultHealthCheckModel(
	ctx context.Context,
	q *postgresqueries.Queries,
	input port.ManagementProviderDefaultHealthCheckModelInput,
) (port.ManagementProviderDefaultHealthCheckModelPreference, error) {
	row, err := q.UpsertManagementProviderDefaultHealthCheckModelPreference(ctx, postgresqueries.UpsertManagementProviderDefaultHealthCheckModelPreferenceParams{
		SystemAccountID: input.SystemAccountID,
		ProviderCode:    input.ProviderCode,
		Model:           input.Model,
	})
	if err != nil {
		return port.ManagementProviderDefaultHealthCheckModelPreference{}, fmt.Errorf("set management provider default health check model: %w", err)
	}
	return port.ManagementProviderDefaultHealthCheckModelPreference{
		ProviderCode: row.ProviderCode,
		Model:        row.Model,
	}, nil
}

func clearManagementProviderDefaultHealthCheckModelIfModel(
	ctx context.Context,
	q *postgresqueries.Queries,
	input port.ManagementProviderDefaultHealthCheckModelClearInput,
) (bool, error) {
	rows, err := q.ClearManagementProviderDefaultHealthCheckModelIfModel(ctx, postgresqueries.ClearManagementProviderDefaultHealthCheckModelIfModelParams{
		ProviderCode:    input.ProviderCode,
		Model:           input.Model,
		SystemAccountID: input.SystemAccountID,
	})
	if err != nil {
		return false, fmt.Errorf("clear management provider default health check model if model: %w", err)
	}
	return rows > 0, nil
}

func setManagementProviderSystemDefaultHealthCheckModel(
	ctx context.Context,
	q *postgresqueries.Queries,
	input port.ManagementProviderSystemDefaultHealthCheckModelInput,
) (port.ManagementProviderDefaultHealthCheckModelPreference, error) {
	row, err := q.UpsertManagementProviderSystemDefaultHealthCheckModel(ctx, postgresqueries.UpsertManagementProviderSystemDefaultHealthCheckModelParams{
		ProviderCode: input.ProviderCode,
		Model:        input.Model,
	})
	if err != nil {
		return port.ManagementProviderDefaultHealthCheckModelPreference{}, fmt.Errorf("set management provider system default health check model: %w", err)
	}
	return port.ManagementProviderDefaultHealthCheckModelPreference{
		ProviderCode: row.ProviderCode,
		Model:        row.Model,
	}, nil
}

func clearManagementProviderSystemDefaultHealthCheckModelIfModel(
	ctx context.Context,
	q *postgresqueries.Queries,
	input port.ManagementProviderSystemDefaultHealthCheckModelClearInput,
) (bool, error) {
	rows, err := q.ClearManagementProviderSystemDefaultHealthCheckModelIfModel(ctx, postgresqueries.ClearManagementProviderSystemDefaultHealthCheckModelIfModelParams{
		ProviderCode: input.ProviderCode,
		Model:        input.Model,
	})
	if err != nil {
		return false, fmt.Errorf("clear management provider system default health check model if model: %w", err)
	}
	return rows > 0, nil
}

func findManagementCustomProviderModel(
	ctx context.Context,
	q *postgresqueries.Queries,
	id string,
) (port.ManagementProviderModelCatalogItem, bool, error) {
	row, err := q.FindManagementCustomProviderModel(ctx, id)
	if err != nil {
		if err == pgx.ErrNoRows {
			return port.ManagementProviderModelCatalogItem{}, false, nil
		}
		return port.ManagementProviderModelCatalogItem{}, false, fmt.Errorf("find management custom provider model: %w", err)
	}
	item, err := managementCustomProviderModelFromData(customProviderModelDataFromFindRow(row))
	if err != nil {
		return port.ManagementProviderModelCatalogItem{}, false, err
	}
	return item, true, nil
}

func findManagementCustomProviderModelByScope(
	ctx context.Context,
	q *postgresqueries.Queries,
	input port.ManagementCustomProviderModelScopeInput,
) (port.ManagementProviderModelCatalogItem, bool, error) {
	row, err := q.FindManagementCustomProviderModelByScope(ctx, postgresqueries.FindManagementCustomProviderModelByScopeParams{
		ProviderCode:    input.ProviderCode,
		Model:           input.Model,
		Scope:           input.Scope,
		SystemAccountID: pgTextFromString(input.SystemAccountID),
	})
	if err != nil {
		if err == pgx.ErrNoRows {
			return port.ManagementProviderModelCatalogItem{}, false, nil
		}
		return port.ManagementProviderModelCatalogItem{}, false, fmt.Errorf("find management custom provider model by scope: %w", err)
	}
	item, err := managementCustomProviderModelFromData(customProviderModelDataFromScopeRow(row))
	if err != nil {
		return port.ManagementProviderModelCatalogItem{}, false, err
	}
	return item, true, nil
}

func saveManagementCustomProviderModel(
	ctx context.Context,
	q *postgresqueries.Queries,
	input port.ManagementCustomProviderModelSaveInput,
) (port.ManagementProviderModelCatalogItem, error) {
	protocolsJSON, err := json.Marshal(input.SupportedAPIProtocols)
	if err != nil {
		return port.ManagementProviderModelCatalogItem{}, fmt.Errorf("marshal management custom provider model protocols: %w", err)
	}
	serviceTiersJSON, err := json.Marshal(input.SupportedServiceTiers)
	if err != nil {
		return port.ManagementProviderModelCatalogItem{}, fmt.Errorf("marshal management custom provider model service tiers: %w", err)
	}
	reasoningEffortsJSON, err := json.Marshal(input.SupportedReasoningEfforts)
	if err != nil {
		return port.ManagementProviderModelCatalogItem{}, fmt.Errorf("marshal management custom provider model reasoning efforts: %w", err)
	}
	serviceTierPricesJSON, err := json.Marshal(input.ServiceTierPrices)
	if err != nil {
		return port.ManagementProviderModelCatalogItem{}, fmt.Errorf("marshal management custom provider model service tier prices: %w", err)
	}
	row, err := q.UpsertManagementCustomProviderModel(ctx, postgresqueries.UpsertManagementCustomProviderModelParams{
		ID:                            input.ID,
		ProviderCode:                  input.ProviderCode,
		Model:                         input.Model,
		Scope:                         input.Scope,
		SystemAccountID:               pgTextFromString(input.SystemAccountID),
		Status:                        input.Status,
		Mode:                          pgTextFromString(input.Mode),
		SupportedApiProtocolsJson:     string(protocolsJSON),
		SupportedServiceTiersJson:     string(serviceTiersJSON),
		SupportedReasoningEffortsJson: string(reasoningEffortsJSON),
		DefaultReasoningEffort:        pgTextFromString(input.DefaultReasoningEffort),
		ReleaseDate:                   pgTextFromString(input.ReleaseDate),
		ShutdownDate:                  pgTextFromString(input.ShutdownDate),
		ContextWindowTokens:           pgInt4Ptr(input.ContextWindowTokens),
		MaxInputTokens:                pgInt4Ptr(input.MaxInputTokens),
		MaxOutputTokens:               pgInt4Ptr(input.MaxOutputTokens),
		InputUsdPer1m:                 pgFloat8Ptr(input.InputUSDPer1M),
		OutputUsdPer1m:                pgFloat8Ptr(input.OutputUSDPer1M),
		CachedInputUsdPer1m:           pgFloat8Ptr(input.CachedInputUSDPer1M),
		CacheWriteUsdPer1m:            pgFloat8Ptr(input.CacheWriteUSDPer1M),
		CacheWrite1hUsdPer1m:          pgFloat8Ptr(input.CacheWrite1hUSDPer1M),
		ServiceTierPricesJson:         string(serviceTierPricesJSON),
		ImageInputUsdPer1m:            pgFloat8Ptr(input.ImageInputUSDPer1M),
		ImageOutputUsdPer1m:           pgFloat8Ptr(input.ImageOutputUSDPer1M),
		AudioInputUsdPer1m:            pgFloat8Ptr(input.AudioInputUSDPer1M),
		AudioOutputUsdPer1m:           pgFloat8Ptr(input.AudioOutputUSDPer1M),
		OutputUsdPerImage:             pgFloat8Ptr(input.OutputUSDPerImage),
		PricingNotes:                  pgTextFromString(input.PricingNotes),
		CapabilityNotes:               pgTextFromString(input.CapabilityNotes),
		Notes:                         pgTextFromString(input.Notes),
		ActorSystemAccountID:          input.ActorSystemAccountID,
	})
	if err != nil {
		return port.ManagementProviderModelCatalogItem{}, fmt.Errorf("save management custom provider model: %w", err)
	}
	return managementCustomProviderModelFromData(customProviderModelDataFromUpsertRow(row))
}

func deleteManagementCustomProviderModel(ctx context.Context, q *postgresqueries.Queries, id string) (bool, error) {
	rows, err := q.DeleteManagementCustomProviderModel(ctx, id)
	if err != nil {
		return false, fmt.Errorf("delete management custom provider model: %w", err)
	}
	return rows > 0, nil
}

func getManagementCustomProviderModelBindingSummary(
	ctx context.Context,
	q *postgresqueries.Queries,
	input port.ManagementCustomProviderModelBindingInput,
) (port.ManagementCustomProviderModelBindingSummary, error) {
	row, err := q.GetManagementCustomProviderModelBindingSummary(ctx, postgresqueries.GetManagementCustomProviderModelBindingSummaryParams{
		Scope:           input.Scope,
		SystemAccountID: input.SystemAccountID,
		ProviderCode:    input.ProviderCode,
		Model:           input.Model,
	})
	if err != nil {
		return port.ManagementCustomProviderModelBindingSummary{}, fmt.Errorf("get management custom provider model binding summary: %w", err)
	}
	return port.ManagementCustomProviderModelBindingSummary{
		SupportedModelAccountCount:  int(row.SupportedModelAccountCount),
		MappingSourceAccountCount:   int(row.MappingSourceAccountCount),
		MappingUpstreamAccountCount: int(row.MappingUpstreamAccountCount),
		TotalAccountCount:           int(row.TotalAccountCount),
	}, nil
}

func managementProviderModelCatalogItemFromRow(row postgresqueries.ListManagementProviderModelCatalogRow) (port.ManagementProviderModelCatalogItem, error) {
	protocols, err := decodeProviderStringArray(row.SupportedApiProtocolsJson, "provider model supported_api_protocols_json")
	if err != nil {
		return port.ManagementProviderModelCatalogItem{}, err
	}
	serviceTiers, err := decodeProviderStringArray(row.SupportedServiceTiersJson, "provider model supported_service_tiers_json")
	if err != nil {
		return port.ManagementProviderModelCatalogItem{}, err
	}
	reasoningEfforts, err := decodeProviderStringArray(row.SupportedReasoningEffortsJson, "provider model supported_reasoning_efforts_json")
	if err != nil {
		return port.ManagementProviderModelCatalogItem{}, err
	}
	codexReasoningLevels, err := decodeProviderStringArray(row.CodexSupportedReasoningLevelsJson, "provider model codex_supported_reasoning_levels_json")
	if err != nil {
		return port.ManagementProviderModelCatalogItem{}, err
	}
	serviceTierPrices, err := decodeProviderModelPriceMap(row.ServiceTierPricesJson, "provider model service_tier_prices_json")
	if err != nil {
		return port.ManagementProviderModelCatalogItem{}, err
	}
	return port.ManagementProviderModelCatalogItem{
		ID:                              row.ID,
		ProviderCode:                    row.ProviderCode,
		Model:                           row.Model,
		Scope:                           row.Scope,
		SystemAccountID:                 textValue(row.SystemAccountID),
		Status:                          row.Status,
		Mode:                            textValue(row.Mode),
		CatalogOrder:                    int4Ptr(row.CatalogOrder),
		ReleaseDate:                     textValue(row.ReleaseDate),
		ShutdownDate:                    textValue(row.ShutdownDate),
		SupportedAPIProtocols:           protocols,
		SupportedServiceTiers:           serviceTiers,
		SupportedReasoningEfforts:       reasoningEfforts,
		DefaultReasoningEffort:          textValue(row.DefaultReasoningEffort),
		CodexSupportedReasoningLevels:   codexReasoningLevels,
		CodexDefaultReasoningLevel:      textValue(row.CodexDefaultReasoningLevel),
		CodexMultiAgentVersion:          textValue(row.CodexMultiAgentVersion),
		ContextWindowTokens:             int4Ptr(row.ContextWindowTokens),
		MaxInputTokens:                  int4Ptr(row.MaxInputTokens),
		MaxOutputTokens:                 int4Ptr(row.MaxOutputTokens),
		MaxTokens:                       int4Ptr(row.MaxTokens),
		InputUSDPer1M:                   float8Ptr(row.InputUsdPer1m),
		OutputUSDPer1M:                  float8Ptr(row.OutputUsdPer1m),
		CachedInputUSDPer1M:             float8Ptr(row.CachedInputUsdPer1m),
		CacheWriteUSDPer1M:              float8Ptr(row.CacheWriteUsdPer1m),
		CacheWrite1hUSDPer1M:            float8Ptr(row.CacheWrite1hUsdPer1m),
		ServiceTierPrices:               serviceTierPrices,
		LongContextInputTokenThreshold:  int4Ptr(row.LongContextInputTokenThreshold),
		LongContextInputCostMultiplier:  float8Ptr(row.LongContextInputCostMultiplier),
		LongContextOutputCostMultiplier: float8Ptr(row.LongContextOutputCostMultiplier),
		ImageInputUSDPer1M:              float8Ptr(row.ImageInputUsdPer1m),
		ImageOutputUSDPer1M:             float8Ptr(row.ImageOutputUsdPer1m),
		AudioInputUSDPer1M:              float8Ptr(row.AudioInputUsdPer1m),
		AudioOutputUSDPer1M:             float8Ptr(row.AudioOutputUsdPer1m),
		OutputUSDPerImage:               float8Ptr(row.OutputUsdPerImage),
		SupportsPromptCaching:           row.SupportsPromptCaching,
		SupportsServiceTier:             len(serviceTiers) > 0,
		CatalogVisible:                  row.CatalogVisible,
		PricingNotes:                    textValue(row.PricingNotes),
		CapabilityNotes:                 textValue(row.CapabilityNotes),
		Notes:                           textValue(row.Notes),
		CreatedBy:                       row.CreatedBy,
		UpdatedBy:                       textValue(row.UpdatedBy),
		Source:                          row.Source,
		CreatedAt:                       timestamptzValue(row.CreatedAt),
		UpdatedAt:                       timestamptzValue(row.UpdatedAt),
	}, nil
}

type managementCustomProviderModelData struct {
	ID                            string
	ProviderCode                  string
	Model                         string
	Scope                         string
	SystemAccountID               pgtype.Text
	Status                        string
	Mode                          pgtype.Text
	SupportedApiProtocolsJson     string
	SupportedServiceTiersJson     string
	SupportedReasoningEffortsJson string
	DefaultReasoningEffort        pgtype.Text
	ReleaseDate                   pgtype.Text
	ShutdownDate                  pgtype.Text
	ContextWindowTokens           pgtype.Int4
	MaxInputTokens                pgtype.Int4
	MaxOutputTokens               pgtype.Int4
	InputUsdPer1m                 pgtype.Float8
	OutputUsdPer1m                pgtype.Float8
	CachedInputUsdPer1m           pgtype.Float8
	CacheWriteUsdPer1m            pgtype.Float8
	CacheWrite1hUsdPer1m          pgtype.Float8
	ServiceTierPricesJson         string
	ImageInputUsdPer1m            pgtype.Float8
	ImageOutputUsdPer1m           pgtype.Float8
	AudioInputUsdPer1m            pgtype.Float8
	AudioOutputUsdPer1m           pgtype.Float8
	OutputUsdPerImage             pgtype.Float8
	PricingNotes                  pgtype.Text
	CapabilityNotes               pgtype.Text
	Notes                         pgtype.Text
	CreatedBy                     string
	UpdatedBy                     pgtype.Text
	CreatedAt                     pgtype.Timestamptz
	UpdatedAt                     pgtype.Timestamptz
}

func managementCustomProviderModelFromData(row managementCustomProviderModelData) (port.ManagementProviderModelCatalogItem, error) {
	protocols, err := decodeProviderStringArray(row.SupportedApiProtocolsJson, "custom provider model supported_api_protocols_json")
	if err != nil {
		return port.ManagementProviderModelCatalogItem{}, err
	}
	serviceTiers, err := decodeProviderStringArray(row.SupportedServiceTiersJson, "custom provider model supported_service_tiers_json")
	if err != nil {
		return port.ManagementProviderModelCatalogItem{}, err
	}
	reasoningEfforts, err := decodeProviderStringArray(row.SupportedReasoningEffortsJson, "custom provider model supported_reasoning_efforts_json")
	if err != nil {
		return port.ManagementProviderModelCatalogItem{}, err
	}
	serviceTierPrices, err := decodeProviderModelPriceMap(row.ServiceTierPricesJson, "custom provider model service_tier_prices_json")
	if err != nil {
		return port.ManagementProviderModelCatalogItem{}, err
	}
	return port.ManagementProviderModelCatalogItem{
		ID:                        row.ID,
		ProviderCode:              row.ProviderCode,
		Model:                     row.Model,
		Scope:                     row.Scope,
		SystemAccountID:           textValue(row.SystemAccountID),
		Status:                    row.Status,
		Mode:                      textValue(row.Mode),
		ReleaseDate:               textValue(row.ReleaseDate),
		ShutdownDate:              textValue(row.ShutdownDate),
		SupportedAPIProtocols:     protocols,
		SupportedServiceTiers:     serviceTiers,
		SupportedReasoningEfforts: reasoningEfforts,
		DefaultReasoningEffort:    textValue(row.DefaultReasoningEffort),
		ContextWindowTokens:       int4Ptr(row.ContextWindowTokens),
		MaxInputTokens:            int4Ptr(row.MaxInputTokens),
		MaxOutputTokens:           int4Ptr(row.MaxOutputTokens),
		InputUSDPer1M:             float8Ptr(row.InputUsdPer1m),
		OutputUSDPer1M:            float8Ptr(row.OutputUsdPer1m),
		CachedInputUSDPer1M:       float8Ptr(row.CachedInputUsdPer1m),
		CacheWriteUSDPer1M:        float8Ptr(row.CacheWriteUsdPer1m),
		CacheWrite1hUSDPer1M:      float8Ptr(row.CacheWrite1hUsdPer1m),
		ServiceTierPrices:         serviceTierPrices,
		ImageInputUSDPer1M:        float8Ptr(row.ImageInputUsdPer1m),
		ImageOutputUSDPer1M:       float8Ptr(row.ImageOutputUsdPer1m),
		AudioInputUSDPer1M:        float8Ptr(row.AudioInputUsdPer1m),
		AudioOutputUSDPer1M:       float8Ptr(row.AudioOutputUsdPer1m),
		OutputUSDPerImage:         float8Ptr(row.OutputUsdPerImage),
		SupportsPromptCaching:     row.CachedInputUsdPer1m.Valid,
		SupportsServiceTier:       len(serviceTiers) > 0,
		CatalogVisible:            true,
		PricingNotes:              textValue(row.PricingNotes),
		CapabilityNotes:           textValue(row.CapabilityNotes),
		Notes:                     textValue(row.Notes),
		CreatedBy:                 row.CreatedBy,
		UpdatedBy:                 textValue(row.UpdatedBy),
		Source:                    customProviderModelSource(row.Scope),
		CreatedAt:                 timestamptzValue(row.CreatedAt),
		UpdatedAt:                 timestamptzValue(row.UpdatedAt),
	}, nil
}

func customProviderModelDataFromFindRow(row postgresqueries.FindManagementCustomProviderModelRow) managementCustomProviderModelData {
	return managementCustomProviderModelData{
		ID:                            row.ID,
		ProviderCode:                  row.ProviderCode,
		Model:                         row.Model,
		Scope:                         row.Scope,
		SystemAccountID:               row.SystemAccountID,
		Status:                        row.Status,
		Mode:                          row.Mode,
		SupportedApiProtocolsJson:     row.SupportedApiProtocolsJson,
		SupportedServiceTiersJson:     row.SupportedServiceTiersJson,
		SupportedReasoningEffortsJson: row.SupportedReasoningEffortsJson,
		DefaultReasoningEffort:        row.DefaultReasoningEffort,
		ReleaseDate:                   row.ReleaseDate,
		ShutdownDate:                  row.ShutdownDate,
		ContextWindowTokens:           row.ContextWindowTokens,
		MaxInputTokens:                row.MaxInputTokens,
		MaxOutputTokens:               row.MaxOutputTokens,
		InputUsdPer1m:                 row.InputUsdPer1m,
		OutputUsdPer1m:                row.OutputUsdPer1m,
		CachedInputUsdPer1m:           row.CachedInputUsdPer1m,
		CacheWriteUsdPer1m:            row.CacheWriteUsdPer1m,
		CacheWrite1hUsdPer1m:          row.CacheWrite1hUsdPer1m,
		ServiceTierPricesJson:         row.ServiceTierPricesJson,
		ImageInputUsdPer1m:            row.ImageInputUsdPer1m,
		ImageOutputUsdPer1m:           row.ImageOutputUsdPer1m,
		AudioInputUsdPer1m:            row.AudioInputUsdPer1m,
		AudioOutputUsdPer1m:           row.AudioOutputUsdPer1m,
		OutputUsdPerImage:             row.OutputUsdPerImage,
		PricingNotes:                  row.PricingNotes,
		CapabilityNotes:               row.CapabilityNotes,
		Notes:                         row.Notes,
		CreatedBy:                     row.CreatedBy,
		UpdatedBy:                     row.UpdatedBy,
		CreatedAt:                     row.CreatedAt,
		UpdatedAt:                     row.UpdatedAt,
	}
}

func customProviderModelDataFromScopeRow(row postgresqueries.FindManagementCustomProviderModelByScopeRow) managementCustomProviderModelData {
	return managementCustomProviderModelData{
		ID:                            row.ID,
		ProviderCode:                  row.ProviderCode,
		Model:                         row.Model,
		Scope:                         row.Scope,
		SystemAccountID:               row.SystemAccountID,
		Status:                        row.Status,
		Mode:                          row.Mode,
		SupportedApiProtocolsJson:     row.SupportedApiProtocolsJson,
		SupportedServiceTiersJson:     row.SupportedServiceTiersJson,
		SupportedReasoningEffortsJson: row.SupportedReasoningEffortsJson,
		DefaultReasoningEffort:        row.DefaultReasoningEffort,
		ReleaseDate:                   row.ReleaseDate,
		ShutdownDate:                  row.ShutdownDate,
		ContextWindowTokens:           row.ContextWindowTokens,
		MaxInputTokens:                row.MaxInputTokens,
		MaxOutputTokens:               row.MaxOutputTokens,
		InputUsdPer1m:                 row.InputUsdPer1m,
		OutputUsdPer1m:                row.OutputUsdPer1m,
		CachedInputUsdPer1m:           row.CachedInputUsdPer1m,
		CacheWriteUsdPer1m:            row.CacheWriteUsdPer1m,
		CacheWrite1hUsdPer1m:          row.CacheWrite1hUsdPer1m,
		ServiceTierPricesJson:         row.ServiceTierPricesJson,
		ImageInputUsdPer1m:            row.ImageInputUsdPer1m,
		ImageOutputUsdPer1m:           row.ImageOutputUsdPer1m,
		AudioInputUsdPer1m:            row.AudioInputUsdPer1m,
		AudioOutputUsdPer1m:           row.AudioOutputUsdPer1m,
		OutputUsdPerImage:             row.OutputUsdPerImage,
		PricingNotes:                  row.PricingNotes,
		CapabilityNotes:               row.CapabilityNotes,
		Notes:                         row.Notes,
		CreatedBy:                     row.CreatedBy,
		UpdatedBy:                     row.UpdatedBy,
		CreatedAt:                     row.CreatedAt,
		UpdatedAt:                     row.UpdatedAt,
	}
}

func customProviderModelDataFromUpsertRow(row postgresqueries.UpsertManagementCustomProviderModelRow) managementCustomProviderModelData {
	return managementCustomProviderModelData{
		ID:                            row.ID,
		ProviderCode:                  row.ProviderCode,
		Model:                         row.Model,
		Scope:                         row.Scope,
		SystemAccountID:               row.SystemAccountID,
		Status:                        row.Status,
		Mode:                          row.Mode,
		SupportedApiProtocolsJson:     row.SupportedApiProtocolsJson,
		SupportedServiceTiersJson:     row.SupportedServiceTiersJson,
		SupportedReasoningEffortsJson: row.SupportedReasoningEffortsJson,
		DefaultReasoningEffort:        row.DefaultReasoningEffort,
		ReleaseDate:                   row.ReleaseDate,
		ShutdownDate:                  row.ShutdownDate,
		ContextWindowTokens:           row.ContextWindowTokens,
		MaxInputTokens:                row.MaxInputTokens,
		MaxOutputTokens:               row.MaxOutputTokens,
		InputUsdPer1m:                 row.InputUsdPer1m,
		OutputUsdPer1m:                row.OutputUsdPer1m,
		CachedInputUsdPer1m:           row.CachedInputUsdPer1m,
		CacheWriteUsdPer1m:            row.CacheWriteUsdPer1m,
		CacheWrite1hUsdPer1m:          row.CacheWrite1hUsdPer1m,
		ServiceTierPricesJson:         row.ServiceTierPricesJson,
		ImageInputUsdPer1m:            row.ImageInputUsdPer1m,
		ImageOutputUsdPer1m:           row.ImageOutputUsdPer1m,
		AudioInputUsdPer1m:            row.AudioInputUsdPer1m,
		AudioOutputUsdPer1m:           row.AudioOutputUsdPer1m,
		OutputUsdPerImage:             row.OutputUsdPerImage,
		PricingNotes:                  row.PricingNotes,
		CapabilityNotes:               row.CapabilityNotes,
		Notes:                         row.Notes,
		CreatedBy:                     row.CreatedBy,
		UpdatedBy:                     row.UpdatedBy,
		CreatedAt:                     row.CreatedAt,
		UpdatedAt:                     row.UpdatedAt,
	}
}

func decodeProviderModelPriceMap(raw string, label string) (map[string]port.ManagementProviderModelPriceSet, error) {
	value := map[string]port.ManagementProviderModelPriceSet{}
	if strings.TrimSpace(raw) == "" {
		return value, nil
	}
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return nil, fmt.Errorf("decode %s: %w", label, err)
	}
	return value, nil
}

func customProviderModelSource(scope string) string {
	if scope == "global" {
		return "custom-global"
	}
	return "custom-personal"
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

func pgFloat8Ptr(value *float64) pgtype.Float8 {
	if value == nil {
		return pgtype.Float8{}
	}
	return pgtype.Float8{Float64: *value, Valid: true}
}

var _ port.ManagementProviderModelCatalogReader = (*Store)(nil)
var _ port.ManagementProviderDefaultHealthCheckModelWriter = (*Store)(nil)
var _ port.ManagementCustomProviderModelWriter = (*Store)(nil)
