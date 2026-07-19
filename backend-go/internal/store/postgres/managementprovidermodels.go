package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"juhe-ai/backend-go/internal/store/port"
	"juhe-ai/backend-go/internal/store/postgres/postgresqueries"
)

type managementBuiltInProviderModelPriceUpdateQueries interface {
	LockManagementBuiltInProviderModelConfiguration(
		ctx context.Context,
		input postgresqueries.LockManagementBuiltInProviderModelConfigurationParams,
	) (postgresqueries.LockManagementBuiltInProviderModelConfigurationRow, error)
	UpdateManagementBuiltInProviderModelConfiguration(
		ctx context.Context,
		input postgresqueries.UpdateManagementBuiltInProviderModelConfigurationParams,
	) (postgresqueries.UpdateManagementBuiltInProviderModelConfigurationRow, error)
}

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

func (s *Store) UpdateManagementCustomProviderModel(ctx context.Context, input port.ManagementCustomProviderModelUpdateInput, validate port.ManagementCustomProviderModelUpdateValidate) (port.ManagementCustomProviderModelUpdateResult, bool, error) {
	return updateManagementCustomProviderModelInTx(ctx, s.pool.BeginTx, input, validate)
}

func (s *Store) DeleteManagementCustomProviderModel(ctx context.Context, id string) (bool, error) {
	return deleteManagementCustomProviderModel(ctx, s.queries(), id)
}

func (s *Store) GetManagementCustomProviderModelBindingSummary(ctx context.Context, input port.ManagementCustomProviderModelBindingInput) (port.ManagementCustomProviderModelBindingSummary, error) {
	return getManagementCustomProviderModelBindingSummary(ctx, s.queries(), input)
}

func (s *Store) UpdateManagementBuiltInProviderModelPrices(ctx context.Context, input port.ManagementBuiltInProviderModelPriceUpdateInput, validate port.ManagementBuiltInProviderModelUpdateValidate) (port.ManagementBuiltInProviderModelPriceUpdateResult, bool, error) {
	return updateManagementBuiltInProviderModelPricesInTx(ctx, s.pool.BeginTx, input, validate)
}

func updateManagementBuiltInProviderModelPricesInTx(ctx context.Context, beginTx func(context.Context, pgx.TxOptions) (pgx.Tx, error), input port.ManagementBuiltInProviderModelPriceUpdateInput, validate port.ManagementBuiltInProviderModelUpdateValidate) (port.ManagementBuiltInProviderModelPriceUpdateResult, bool, error) {
	tx, err := beginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return port.ManagementBuiltInProviderModelPriceUpdateResult{}, false, fmt.Errorf("begin built-in provider model update: %w", err)
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(rollbackCtx)
	}()
	result, found, err := updateManagementBuiltInProviderModelPricesTx(ctx, postgresqueries.New(tx), input, validate)
	if err != nil || !found {
		return result, found, err
	}
	if err := tx.Commit(ctx); err != nil {
		return port.ManagementBuiltInProviderModelPriceUpdateResult{}, false, fmt.Errorf("commit built-in provider model update: %w", err)
	}
	committed = true
	return result, true, nil
}

type managementCustomProviderModelUpdateQueries interface {
	LockManagementCustomProviderModel(context.Context, postgresqueries.LockManagementCustomProviderModelParams) (postgresqueries.LockManagementCustomProviderModelRow, error)
	UpdateManagementCustomProviderModel(context.Context, postgresqueries.UpdateManagementCustomProviderModelParams) (postgresqueries.UpdateManagementCustomProviderModelRow, error)
}

func updateManagementCustomProviderModelInTx(ctx context.Context, beginTx func(context.Context, pgx.TxOptions) (pgx.Tx, error), input port.ManagementCustomProviderModelUpdateInput, validate port.ManagementCustomProviderModelUpdateValidate) (port.ManagementCustomProviderModelUpdateResult, bool, error) {
	tx, err := beginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return port.ManagementCustomProviderModelUpdateResult{}, false, fmt.Errorf("begin custom provider model update: %w", err)
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(rollbackCtx)
	}()
	result, found, err := updateManagementCustomProviderModelTx(ctx, postgresqueries.New(tx), input, validate)
	if err != nil || !found {
		return result, found, err
	}
	if err := tx.Commit(ctx); err != nil {
		return port.ManagementCustomProviderModelUpdateResult{}, false, fmt.Errorf("commit custom provider model update: %w", err)
	}
	committed = true
	return result, true, nil
}

func updateManagementCustomProviderModelTx(ctx context.Context, q managementCustomProviderModelUpdateQueries, input port.ManagementCustomProviderModelUpdateInput, validate port.ManagementCustomProviderModelUpdateValidate) (port.ManagementCustomProviderModelUpdateResult, bool, error) {
	locked, err := q.LockManagementCustomProviderModel(ctx, postgresqueries.LockManagementCustomProviderModelParams{ID: input.ID, ProviderCode: input.ProviderCode})
	if errors.Is(err, pgx.ErrNoRows) {
		return port.ManagementCustomProviderModelUpdateResult{}, false, nil
	}
	if err != nil {
		return port.ManagementCustomProviderModelUpdateResult{}, false, fmt.Errorf("lock custom provider model: %w", err)
	}
	before, err := managementCustomProviderModelFromData(customProviderModelDataFromLockRow(locked))
	if err != nil {
		return port.ManagementCustomProviderModelUpdateResult{}, false, fmt.Errorf("decode locked custom provider model: %w", err)
	}
	candidateInput := customModelSaveInputFromCatalogItem(before, input.ActorSystemAccountID)
	mergeCustomProviderModelUpdate(&candidateInput, input)
	candidate := customProviderModelCatalogItemFromSaveInput(candidateInput, before)
	if validate == nil {
		return port.ManagementCustomProviderModelUpdateResult{}, false, errors.New("validate custom provider model update is required")
	}
	if err := validate(port.ManagementCustomProviderModelUpdateResult{Before: before, After: candidate}); err != nil {
		return port.ManagementCustomProviderModelUpdateResult{}, false, err
	}
	params, err := customProviderModelUpdateParams(candidate, input.ActorSystemAccountID)
	if err != nil {
		return port.ManagementCustomProviderModelUpdateResult{}, false, err
	}
	updated, err := q.UpdateManagementCustomProviderModel(ctx, params)
	if errors.Is(err, pgx.ErrNoRows) {
		return port.ManagementCustomProviderModelUpdateResult{}, false, nil
	}
	if err != nil {
		return port.ManagementCustomProviderModelUpdateResult{}, false, fmt.Errorf("update custom provider model: %w", err)
	}
	after, err := managementCustomProviderModelFromData(customProviderModelDataFromUpdateRow(updated))
	if err != nil {
		return port.ManagementCustomProviderModelUpdateResult{}, false, fmt.Errorf("decode updated custom provider model: %w", err)
	}
	if before.ID != input.ID || before.ProviderCode != input.ProviderCode || after.ID != before.ID || after.ProviderCode != before.ProviderCode {
		return port.ManagementCustomProviderModelUpdateResult{}, false, errors.New("custom provider model update identity mismatch")
	}
	return port.ManagementCustomProviderModelUpdateResult{Before: before, After: after}, true, nil
}

func customModelSaveInputFromCatalogItem(item port.ManagementProviderModelCatalogItem, actor string) port.ManagementCustomProviderModelSaveInput {
	return port.ManagementCustomProviderModelSaveInput{
		ID: item.ID, ProviderCode: item.ProviderCode, Model: item.Model, Scope: item.Scope, SystemAccountID: item.SystemAccountID,
		Status: item.Status, CatalogVisible: item.CatalogVisible, Mode: item.Mode, SupportedAPIProtocols: append([]string{}, item.SupportedAPIProtocols...), SupportedServiceTiers: append([]string{}, item.SupportedServiceTiers...), SupportedReasoningEfforts: append([]string{}, item.SupportedReasoningEfforts...), DefaultReasoningEffort: item.DefaultReasoningEffort, ReleaseDate: item.ReleaseDate, ShutdownDate: item.ShutdownDate,
		ContextWindowTokens: cloneManagementProviderModelInt(item.ContextWindowTokens), MaxInputTokens: cloneManagementProviderModelInt(item.MaxInputTokens), MaxOutputTokens: cloneManagementProviderModelInt(item.MaxOutputTokens), InputUSDPer1M: cloneManagementProviderModelFloat(item.InputUSDPer1M), OutputUSDPer1M: cloneManagementProviderModelFloat(item.OutputUSDPer1M), CachedInputUSDPer1M: cloneManagementProviderModelFloat(item.CachedInputUSDPer1M), CacheWriteUSDPer1M: cloneManagementProviderModelFloat(item.CacheWriteUSDPer1M), CacheWrite1hUSDPer1M: cloneManagementProviderModelFloat(item.CacheWrite1hUSDPer1M), ServiceTierPrices: cloneManagementProviderModelPriceMap(item.ServiceTierPrices), ImageInputUSDPer1M: cloneManagementProviderModelFloat(item.ImageInputUSDPer1M), ImageOutputUSDPer1M: cloneManagementProviderModelFloat(item.ImageOutputUSDPer1M), AudioInputUSDPer1M: cloneManagementProviderModelFloat(item.AudioInputUSDPer1M), AudioOutputUSDPer1M: cloneManagementProviderModelFloat(item.AudioOutputUSDPer1M), OutputUSDPerImage: cloneManagementProviderModelFloat(item.OutputUSDPerImage), PricingNotes: item.PricingNotes, CapabilityNotes: item.CapabilityNotes, Notes: item.Notes, ActorSystemAccountID: strings.TrimSpace(actor),
	}
}

func customProviderModelCatalogItemFromSaveInput(input port.ManagementCustomProviderModelSaveInput, metadata port.ManagementProviderModelCatalogItem) port.ManagementProviderModelCatalogItem {
	result := metadata
	result.Status = input.Status
	result.CatalogVisible = input.CatalogVisible
	result.Mode = input.Mode
	result.SupportedAPIProtocols = append([]string{}, input.SupportedAPIProtocols...)
	result.SupportedServiceTiers = append([]string{}, input.SupportedServiceTiers...)
	result.SupportedReasoningEfforts = append([]string{}, input.SupportedReasoningEfforts...)
	result.DefaultReasoningEffort = input.DefaultReasoningEffort
	result.ReleaseDate = input.ReleaseDate
	result.ShutdownDate = input.ShutdownDate
	result.ContextWindowTokens = cloneManagementProviderModelInt(input.ContextWindowTokens)
	result.MaxInputTokens = cloneManagementProviderModelInt(input.MaxInputTokens)
	result.MaxOutputTokens = cloneManagementProviderModelInt(input.MaxOutputTokens)
	result.InputUSDPer1M = cloneManagementProviderModelFloat(input.InputUSDPer1M)
	result.OutputUSDPer1M = cloneManagementProviderModelFloat(input.OutputUSDPer1M)
	result.CachedInputUSDPer1M = cloneManagementProviderModelFloat(input.CachedInputUSDPer1M)
	result.CacheWriteUSDPer1M = cloneManagementProviderModelFloat(input.CacheWriteUSDPer1M)
	result.CacheWrite1hUSDPer1M = cloneManagementProviderModelFloat(input.CacheWrite1hUSDPer1M)
	result.ServiceTierPrices = cloneManagementProviderModelPriceMap(input.ServiceTierPrices)
	result.ImageInputUSDPer1M = cloneManagementProviderModelFloat(input.ImageInputUSDPer1M)
	result.ImageOutputUSDPer1M = cloneManagementProviderModelFloat(input.ImageOutputUSDPer1M)
	result.AudioInputUSDPer1M = cloneManagementProviderModelFloat(input.AudioInputUSDPer1M)
	result.AudioOutputUSDPer1M = cloneManagementProviderModelFloat(input.AudioOutputUSDPer1M)
	result.OutputUSDPerImage = cloneManagementProviderModelFloat(input.OutputUSDPerImage)
	result.PricingNotes = input.PricingNotes
	result.CapabilityNotes = input.CapabilityNotes
	result.Notes = input.Notes
	result.UpdatedBy = input.ActorSystemAccountID
	result.SupportsPromptCaching = result.CachedInputUSDPer1M != nil
	result.SupportsServiceTier = len(result.SupportedServiceTiers) > 0
	return result
}

func mergeCustomProviderModelUpdate(candidate *port.ManagementCustomProviderModelSaveInput, input port.ManagementCustomProviderModelUpdateInput) {
	if input.Status.Present {
		candidate.Status = input.Status.Value
	}
	if input.CatalogVisible.Present {
		candidate.CatalogVisible = input.CatalogVisible.Value
	}
	if input.Mode.Present {
		candidate.Mode = input.Mode.Value
	}
	if input.SupportedAPIProtocols.Present {
		candidate.SupportedAPIProtocols = append([]string{}, input.SupportedAPIProtocols.Value...)
	}
	if input.SupportedServiceTiers.Present {
		candidate.SupportedServiceTiers = append([]string{}, input.SupportedServiceTiers.Value...)
	}
	if input.SupportedReasoningEfforts.Present {
		candidate.SupportedReasoningEfforts = append([]string{}, input.SupportedReasoningEfforts.Value...)
	}
	if input.DefaultReasoningEffort.Present {
		candidate.DefaultReasoningEffort = input.DefaultReasoningEffort.Value
	}
	if input.ReleaseDate.Present {
		candidate.ReleaseDate = input.ReleaseDate.Value
	}
	if input.ShutdownDate.Present {
		candidate.ShutdownDate = input.ShutdownDate.Value
	}
	if input.ContextWindowTokens.Present {
		candidate.ContextWindowTokens = cloneManagementProviderModelInt(input.ContextWindowTokens.Value)
	}
	if input.MaxInputTokens.Present {
		candidate.MaxInputTokens = cloneManagementProviderModelInt(input.MaxInputTokens.Value)
	}
	if input.MaxOutputTokens.Present {
		candidate.MaxOutputTokens = cloneManagementProviderModelInt(input.MaxOutputTokens.Value)
	}
	if input.InputUSDPer1M.Present {
		candidate.InputUSDPer1M = cloneManagementProviderModelFloat(input.InputUSDPer1M.Value)
	}
	if input.OutputUSDPer1M.Present {
		candidate.OutputUSDPer1M = cloneManagementProviderModelFloat(input.OutputUSDPer1M.Value)
	}
	if input.CachedInputUSDPer1M.Present {
		candidate.CachedInputUSDPer1M = cloneManagementProviderModelFloat(input.CachedInputUSDPer1M.Value)
	}
	if input.CacheWriteUSDPer1M.Present {
		candidate.CacheWriteUSDPer1M = cloneManagementProviderModelFloat(input.CacheWriteUSDPer1M.Value)
	}
	if input.CacheWrite1hUSDPer1M.Present {
		candidate.CacheWrite1hUSDPer1M = cloneManagementProviderModelFloat(input.CacheWrite1hUSDPer1M.Value)
	}
	if input.ServiceTierPrices.Present {
		candidate.ServiceTierPrices = cloneManagementProviderModelPriceMap(input.ServiceTierPrices.Value)
	}
	if input.ImageInputUSDPer1M.Present {
		candidate.ImageInputUSDPer1M = cloneManagementProviderModelFloat(input.ImageInputUSDPer1M.Value)
	}
	if input.ImageOutputUSDPer1M.Present {
		candidate.ImageOutputUSDPer1M = cloneManagementProviderModelFloat(input.ImageOutputUSDPer1M.Value)
	}
	if input.AudioInputUSDPer1M.Present {
		candidate.AudioInputUSDPer1M = cloneManagementProviderModelFloat(input.AudioInputUSDPer1M.Value)
	}
	if input.AudioOutputUSDPer1M.Present {
		candidate.AudioOutputUSDPer1M = cloneManagementProviderModelFloat(input.AudioOutputUSDPer1M.Value)
	}
	if input.OutputUSDPerImage.Present {
		candidate.OutputUSDPerImage = cloneManagementProviderModelFloat(input.OutputUSDPerImage.Value)
	}
	if input.PricingNotes.Present {
		candidate.PricingNotes = input.PricingNotes.Value
	}
	if input.CapabilityNotes.Present {
		candidate.CapabilityNotes = input.CapabilityNotes.Value
	}
	if input.Notes.Present {
		candidate.Notes = input.Notes.Value
	}
}

func customProviderModelUpdateParams(input port.ManagementProviderModelCatalogItem, actor string) (postgresqueries.UpdateManagementCustomProviderModelParams, error) {
	protocols, err := json.Marshal(input.SupportedAPIProtocols)
	if err != nil {
		return postgresqueries.UpdateManagementCustomProviderModelParams{}, fmt.Errorf("marshal custom provider model protocols: %w", err)
	}
	tiers, err := json.Marshal(input.SupportedServiceTiers)
	if err != nil {
		return postgresqueries.UpdateManagementCustomProviderModelParams{}, fmt.Errorf("marshal custom provider model service tiers: %w", err)
	}
	reasoning, err := json.Marshal(input.SupportedReasoningEfforts)
	if err != nil {
		return postgresqueries.UpdateManagementCustomProviderModelParams{}, fmt.Errorf("marshal custom provider model reasoning efforts: %w", err)
	}
	prices, err := marshalManagementProviderModelPriceMap(input.ServiceTierPrices)
	if err != nil {
		return postgresqueries.UpdateManagementCustomProviderModelParams{}, fmt.Errorf("marshal custom provider model prices: %w", err)
	}
	return postgresqueries.UpdateManagementCustomProviderModelParams{Status: input.Status, CatalogVisible: input.CatalogVisible, Mode: pgTextFromString(input.Mode), SupportedApiProtocolsJson: string(protocols), SupportedServiceTiersJson: string(tiers), SupportedReasoningEffortsJson: string(reasoning), DefaultReasoningEffort: pgTextFromString(input.DefaultReasoningEffort), ReleaseDate: pgTextFromString(input.ReleaseDate), ShutdownDate: pgTextFromString(input.ShutdownDate), ContextWindowTokens: pgInt4Ptr(input.ContextWindowTokens), MaxInputTokens: pgInt4Ptr(input.MaxInputTokens), MaxOutputTokens: pgInt4Ptr(input.MaxOutputTokens), InputUsdPer1m: pgFloat8Ptr(input.InputUSDPer1M), OutputUsdPer1m: pgFloat8Ptr(input.OutputUSDPer1M), CachedInputUsdPer1m: pgFloat8Ptr(input.CachedInputUSDPer1M), CacheWriteUsdPer1m: pgFloat8Ptr(input.CacheWriteUSDPer1M), CacheWrite1hUsdPer1m: pgFloat8Ptr(input.CacheWrite1hUSDPer1M), ServiceTierPricesJson: string(prices), ImageInputUsdPer1m: pgFloat8Ptr(input.ImageInputUSDPer1M), ImageOutputUsdPer1m: pgFloat8Ptr(input.ImageOutputUSDPer1M), AudioInputUsdPer1m: pgFloat8Ptr(input.AudioInputUSDPer1M), AudioOutputUsdPer1m: pgFloat8Ptr(input.AudioOutputUSDPer1M), OutputUsdPerImage: pgFloat8Ptr(input.OutputUSDPerImage), PricingNotes: pgTextFromString(input.PricingNotes), CapabilityNotes: pgTextFromString(input.CapabilityNotes), Notes: pgTextFromString(input.Notes), ActorSystemAccountID: pgTextFromString(actor), UpdatedAt: pgTimestamptz(time.Now().UTC()), ID: input.ID, ProviderCode: input.ProviderCode}, nil
}

func updateManagementBuiltInProviderModelPricesTx(ctx context.Context, q managementBuiltInProviderModelPriceUpdateQueries, input port.ManagementBuiltInProviderModelPriceUpdateInput, validate port.ManagementBuiltInProviderModelUpdateValidate) (port.ManagementBuiltInProviderModelPriceUpdateResult, bool, error) {
	locked, err := q.LockManagementBuiltInProviderModelConfiguration(ctx, postgresqueries.LockManagementBuiltInProviderModelConfigurationParams{ID: input.ID, ProviderCode: input.ProviderCode})
	if errors.Is(err, pgx.ErrNoRows) {
		return port.ManagementBuiltInProviderModelPriceUpdateResult{}, false, nil
	}
	if err != nil {
		return port.ManagementBuiltInProviderModelPriceUpdateResult{}, false, fmt.Errorf("lock built-in provider model configuration: %w", err)
	}
	before, err := decodeManagementProviderModelConfigurationSnapshot(managementProviderModelConfigurationRow{
		id: locked.ID, providerCode: locked.ProviderCode, status: locked.Status, catalogVisible: locked.CatalogVisible, mode: locked.Mode,
		supportedAPIProtocolsJSON: locked.SupportedApiProtocolsJson, supportedServiceTiersJSON: locked.SupportedServiceTiersJson,
		supportedReasoningEffortsJSON: locked.SupportedReasoningEffortsJson, defaultReasoningEffort: locked.DefaultReasoningEffort,
		releaseDate: locked.ReleaseDate, shutdownDate: locked.ShutdownDate, contextWindowTokens: locked.ContextWindowTokens,
		maxInputTokens: locked.MaxInputTokens, maxOutputTokens: locked.MaxOutputTokens, inputUSDPer1M: locked.InputUsdPer1m,
		outputUSDPer1M: locked.OutputUsdPer1m, cachedInputUSDPer1M: locked.CachedInputUsdPer1m, cacheWriteUSDPer1M: locked.CacheWriteUsdPer1m,
		cacheWrite1hUSDPer1M: locked.CacheWrite1hUsdPer1m, serviceTierPricesJSON: locked.ServiceTierPricesJson,
		imageInputUSDPer1M: locked.ImageInputUsdPer1m, imageOutputUSDPer1M: locked.ImageOutputUsdPer1m,
		audioInputUSDPer1M: locked.AudioInputUsdPer1m, audioOutputUSDPer1M: locked.AudioOutputUsdPer1m,
		outputUSDPerImage: locked.OutputUsdPerImage, updatedAt: locked.UpdatedAt,
	}, "before")
	if err != nil {
		return port.ManagementBuiltInProviderModelPriceUpdateResult{}, false, err
	}
	candidate := mergeManagementProviderModelConfigurationSnapshot(before, input)
	if validate == nil {
		return port.ManagementBuiltInProviderModelPriceUpdateResult{}, false, errors.New("validate built-in provider model update is required")
	}
	if err := validate(port.ManagementBuiltInProviderModelPriceUpdateResult{Before: before, After: candidate}); err != nil {
		return port.ManagementBuiltInProviderModelPriceUpdateResult{}, false, err
	}
	params, err := managementProviderModelConfigurationUpdateParams(candidate, time.Now().UTC())
	if err != nil {
		return port.ManagementBuiltInProviderModelPriceUpdateResult{}, false, err
	}
	updated, err := q.UpdateManagementBuiltInProviderModelConfiguration(ctx, params)
	if errors.Is(err, pgx.ErrNoRows) {
		return port.ManagementBuiltInProviderModelPriceUpdateResult{}, false, nil
	}
	if err != nil {
		return port.ManagementBuiltInProviderModelPriceUpdateResult{}, false, fmt.Errorf("update built-in provider model configuration: %w", err)
	}
	after, err := decodeManagementProviderModelConfigurationSnapshot(managementProviderModelConfigurationRow{
		id: updated.ID, providerCode: updated.ProviderCode, status: updated.Status, catalogVisible: updated.CatalogVisible, mode: updated.Mode,
		supportedAPIProtocolsJSON: updated.SupportedApiProtocolsJson, supportedServiceTiersJSON: updated.SupportedServiceTiersJson,
		supportedReasoningEffortsJSON: updated.SupportedReasoningEffortsJson, defaultReasoningEffort: updated.DefaultReasoningEffort,
		releaseDate: updated.ReleaseDate, shutdownDate: updated.ShutdownDate, contextWindowTokens: updated.ContextWindowTokens,
		maxInputTokens: updated.MaxInputTokens, maxOutputTokens: updated.MaxOutputTokens, inputUSDPer1M: updated.InputUsdPer1m,
		outputUSDPer1M: updated.OutputUsdPer1m, cachedInputUSDPer1M: updated.CachedInputUsdPer1m, cacheWriteUSDPer1M: updated.CacheWriteUsdPer1m,
		cacheWrite1hUSDPer1M: updated.CacheWrite1hUsdPer1m, serviceTierPricesJSON: updated.ServiceTierPricesJson,
		imageInputUSDPer1M: updated.ImageInputUsdPer1m, imageOutputUSDPer1M: updated.ImageOutputUsdPer1m,
		audioInputUSDPer1M: updated.AudioInputUsdPer1m, audioOutputUSDPer1M: updated.AudioOutputUsdPer1m,
		outputUSDPerImage: updated.OutputUsdPerImage, updatedAt: updated.UpdatedAt,
	}, "after")
	if err != nil {
		return port.ManagementBuiltInProviderModelPriceUpdateResult{}, false, err
	}
	if before.ID != input.ID || before.ProviderCode != input.ProviderCode || after.ID != before.ID || after.ProviderCode != before.ProviderCode {
		return port.ManagementBuiltInProviderModelPriceUpdateResult{}, false, errors.New("built-in provider model update identity mismatch")
	}
	return port.ManagementBuiltInProviderModelPriceUpdateResult{Before: before, After: after}, true, nil
}

func mergeManagementProviderModelConfigurationSnapshot(before port.ManagementProviderModelConfigurationSnapshot, input port.ManagementBuiltInProviderModelPriceUpdateInput) port.ManagementProviderModelConfigurationSnapshot {
	result := before
	if input.Status.Present {
		result.Status = input.Status.Value
	}
	if input.CatalogVisible.Present {
		result.CatalogVisible = input.CatalogVisible.Value
	}
	if input.Mode.Present {
		result.Mode = input.Mode.Value
	}
	if input.SupportedAPIProtocols.Present {
		result.SupportedAPIProtocols = append([]string(nil), input.SupportedAPIProtocols.Value...)
	}
	if input.SupportedServiceTiers.Present {
		result.SupportedServiceTiers = append([]string(nil), input.SupportedServiceTiers.Value...)
	}
	if input.SupportedReasoningEfforts.Present {
		result.SupportedReasoningEfforts = append([]string(nil), input.SupportedReasoningEfforts.Value...)
	}
	if input.DefaultReasoningEffort.Present {
		result.DefaultReasoningEffort = input.DefaultReasoningEffort.Value
	}
	if input.ReleaseDate.Present {
		result.ReleaseDate = input.ReleaseDate.Value
	}
	if input.ShutdownDate.Present {
		result.ShutdownDate = input.ShutdownDate.Value
	}
	if input.ContextWindowTokens.Present {
		result.ContextWindowTokens = cloneManagementProviderModelInt(input.ContextWindowTokens.Value)
	}
	if input.MaxInputTokens.Present {
		result.MaxInputTokens = cloneManagementProviderModelInt(input.MaxInputTokens.Value)
	}
	if input.MaxOutputTokens.Present {
		result.MaxOutputTokens = cloneManagementProviderModelInt(input.MaxOutputTokens.Value)
	}
	if input.InputUSDPer1M.Present {
		result.InputUSDPer1M = cloneManagementProviderModelFloat(input.InputUSDPer1M.Value)
	}
	if input.OutputUSDPer1M.Present {
		result.OutputUSDPer1M = cloneManagementProviderModelFloat(input.OutputUSDPer1M.Value)
	}
	if input.CachedInputUSDPer1M.Present {
		result.CachedInputUSDPer1M = cloneManagementProviderModelFloat(input.CachedInputUSDPer1M.Value)
	}
	if input.CacheWriteUSDPer1M.Present {
		result.CacheWriteUSDPer1M = cloneManagementProviderModelFloat(input.CacheWriteUSDPer1M.Value)
	}
	if input.CacheWrite1hUSDPer1M.Present {
		result.CacheWrite1hUSDPer1M = cloneManagementProviderModelFloat(input.CacheWrite1hUSDPer1M.Value)
	}
	if input.ServiceTierPrices.Present {
		result.ServiceTierPrices = cloneManagementProviderModelPriceMap(input.ServiceTierPrices.Value)
	}
	if input.ImageInputUSDPer1M.Present {
		result.ImageInputUSDPer1M = cloneManagementProviderModelFloat(input.ImageInputUSDPer1M.Value)
	}
	if input.ImageOutputUSDPer1M.Present {
		result.ImageOutputUSDPer1M = cloneManagementProviderModelFloat(input.ImageOutputUSDPer1M.Value)
	}
	if input.AudioInputUSDPer1M.Present {
		result.AudioInputUSDPer1M = cloneManagementProviderModelFloat(input.AudioInputUSDPer1M.Value)
	}
	if input.AudioOutputUSDPer1M.Present {
		result.AudioOutputUSDPer1M = cloneManagementProviderModelFloat(input.AudioOutputUSDPer1M.Value)
	}
	if input.OutputUSDPerImage.Present {
		result.OutputUSDPerImage = cloneManagementProviderModelFloat(input.OutputUSDPerImage.Value)
	}
	return result
}

func managementProviderModelConfigurationUpdateParams(input port.ManagementProviderModelConfigurationSnapshot, updatedAt time.Time) (postgresqueries.UpdateManagementBuiltInProviderModelConfigurationParams, error) {
	protocolsJSON, err := json.Marshal(input.SupportedAPIProtocols)
	if err != nil {
		return postgresqueries.UpdateManagementBuiltInProviderModelConfigurationParams{}, fmt.Errorf("marshal built-in provider model protocols: %w", err)
	}
	serviceTiersJSON, err := json.Marshal(input.SupportedServiceTiers)
	if err != nil {
		return postgresqueries.UpdateManagementBuiltInProviderModelConfigurationParams{}, fmt.Errorf("marshal built-in provider model service tiers: %w", err)
	}
	reasoningJSON, err := json.Marshal(input.SupportedReasoningEfforts)
	if err != nil {
		return postgresqueries.UpdateManagementBuiltInProviderModelConfigurationParams{}, fmt.Errorf("marshal built-in provider model reasoning efforts: %w", err)
	}
	pricesJSON, err := marshalManagementProviderModelPriceMap(input.ServiceTierPrices)
	if err != nil {
		return postgresqueries.UpdateManagementBuiltInProviderModelConfigurationParams{}, fmt.Errorf("marshal built-in provider model service tier prices: %w", err)
	}
	return postgresqueries.UpdateManagementBuiltInProviderModelConfigurationParams{
		Status: input.Status, CatalogVisible: input.CatalogVisible, Mode: managementProviderModelNullableText(input.Mode), SupportedApiProtocolsJson: string(protocolsJSON), SupportedServiceTiersJson: string(serviceTiersJSON), SupportedReasoningEffortsJson: string(reasoningJSON), DefaultReasoningEffort: managementProviderModelNullableText(input.DefaultReasoningEffort), ReleaseDate: managementProviderModelNullableText(input.ReleaseDate), ShutdownDate: managementProviderModelNullableText(input.ShutdownDate), ContextWindowTokens: pgInt4Ptr(input.ContextWindowTokens), MaxInputTokens: pgInt4Ptr(input.MaxInputTokens), MaxOutputTokens: pgInt4Ptr(input.MaxOutputTokens), InputUsdPer1m: pgFloat8Ptr(input.InputUSDPer1M), OutputUsdPer1m: pgFloat8Ptr(input.OutputUSDPer1M), CachedInputUsdPer1m: pgFloat8Ptr(input.CachedInputUSDPer1M), CacheWriteUsdPer1m: pgFloat8Ptr(input.CacheWriteUSDPer1M), CacheWrite1hUsdPer1m: pgFloat8Ptr(input.CacheWrite1hUSDPer1M), ServiceTierPricesJson: string(pricesJSON), ImageInputUsdPer1m: pgFloat8Ptr(input.ImageInputUSDPer1M), ImageOutputUsdPer1m: pgFloat8Ptr(input.ImageOutputUSDPer1M), AudioInputUsdPer1m: pgFloat8Ptr(input.AudioInputUSDPer1M), AudioOutputUsdPer1m: pgFloat8Ptr(input.AudioOutputUSDPer1M), OutputUsdPerImage: pgFloat8Ptr(input.OutputUSDPerImage), UpdatedAt: pgTimestamptz(updatedAt), ID: input.ID, ProviderCode: input.ProviderCode,
	}, nil
}

func managementProviderModelNullableText(value string) pgtype.Text {
	return pgtype.Text{String: value, Valid: value != ""}
}
func cloneManagementProviderModelInt(value *int) *int {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
func cloneManagementProviderModelFloat(value *float64) *float64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
func cloneManagementProviderModelPriceMap(value map[string]port.ManagementProviderModelPriceSet) map[string]port.ManagementProviderModelPriceSet {
	if value == nil {
		return nil
	}
	cloned := make(map[string]port.ManagementProviderModelPriceSet, len(value))
	for key, price := range value {
		price.InputUSDPer1M = cloneManagementProviderModelFloat(price.InputUSDPer1M)
		price.OutputUSDPer1M = cloneManagementProviderModelFloat(price.OutputUSDPer1M)
		price.CachedInputUSDPer1M = cloneManagementProviderModelFloat(price.CachedInputUSDPer1M)
		price.CacheWriteUSDPer1M = cloneManagementProviderModelFloat(price.CacheWriteUSDPer1M)
		price.CacheWrite1hUSDPer1M = cloneManagementProviderModelFloat(price.CacheWrite1hUSDPer1M)
		price.ImageInputUSDPer1M = cloneManagementProviderModelFloat(price.ImageInputUSDPer1M)
		price.ImageOutputUSDPer1M = cloneManagementProviderModelFloat(price.ImageOutputUSDPer1M)
		price.AudioInputUSDPer1M = cloneManagementProviderModelFloat(price.AudioInputUSDPer1M)
		price.AudioOutputUSDPer1M = cloneManagementProviderModelFloat(price.AudioOutputUSDPer1M)
		price.OutputUSDPerImage = cloneManagementProviderModelFloat(price.OutputUSDPerImage)
		cloned[key] = price
	}
	return cloned
}

type managementProviderModelConfigurationRow struct {
	id, providerCode, status                                    string
	catalogVisible                                              bool
	mode, defaultReasoningEffort, releaseDate, shutdownDate     pgtype.Text
	supportedAPIProtocolsJSON, supportedServiceTiersJSON        string
	supportedReasoningEffortsJSON, serviceTierPricesJSON        string
	contextWindowTokens, maxInputTokens, maxOutputTokens        pgtype.Int4
	inputUSDPer1M, outputUSDPer1M, cachedInputUSDPer1M          pgtype.Float8
	cacheWriteUSDPer1M, cacheWrite1hUSDPer1M                    pgtype.Float8
	imageInputUSDPer1M, imageOutputUSDPer1M, audioInputUSDPer1M pgtype.Float8
	audioOutputUSDPer1M, outputUSDPerImage                      pgtype.Float8
	updatedAt                                                   pgtype.Timestamptz
}

func decodeManagementProviderModelConfigurationSnapshot(row managementProviderModelConfigurationRow, label string) (port.ManagementProviderModelConfigurationSnapshot, error) {
	protocols, err := decodeProviderStringArray(row.supportedAPIProtocolsJSON, "built-in provider model "+label+" supported_api_protocols_json")
	if err != nil {
		return port.ManagementProviderModelConfigurationSnapshot{}, err
	}
	serviceTiers, err := decodeProviderStringArray(row.supportedServiceTiersJSON, "built-in provider model "+label+" supported_service_tiers_json")
	if err != nil {
		return port.ManagementProviderModelConfigurationSnapshot{}, err
	}
	reasoningEfforts, err := decodeProviderStringArray(row.supportedReasoningEffortsJSON, "built-in provider model "+label+" supported_reasoning_efforts_json")
	if err != nil {
		return port.ManagementProviderModelConfigurationSnapshot{}, err
	}
	serviceTierPrices, err := decodeProviderModelPriceMap(row.serviceTierPricesJSON, "built-in provider model "+label+" service_tier_prices_json")
	if err != nil {
		return port.ManagementProviderModelConfigurationSnapshot{}, err
	}
	return port.ManagementProviderModelConfigurationSnapshot{
		ID: row.id, ProviderCode: row.providerCode, Status: row.status, CatalogVisible: row.catalogVisible, Mode: textValue(row.mode),
		SupportedAPIProtocols: protocols, SupportedServiceTiers: serviceTiers, SupportedReasoningEfforts: reasoningEfforts,
		DefaultReasoningEffort: textValue(row.defaultReasoningEffort), ReleaseDate: textValue(row.releaseDate), ShutdownDate: textValue(row.shutdownDate),
		ContextWindowTokens: int4Ptr(row.contextWindowTokens), MaxInputTokens: int4Ptr(row.maxInputTokens), MaxOutputTokens: int4Ptr(row.maxOutputTokens),
		InputUSDPer1M: float8Ptr(row.inputUSDPer1M), OutputUSDPer1M: float8Ptr(row.outputUSDPer1M), CachedInputUSDPer1M: float8Ptr(row.cachedInputUSDPer1M),
		CacheWriteUSDPer1M: float8Ptr(row.cacheWriteUSDPer1M), CacheWrite1hUSDPer1M: float8Ptr(row.cacheWrite1hUSDPer1M), ServiceTierPrices: serviceTierPrices,
		ImageInputUSDPer1M: float8Ptr(row.imageInputUSDPer1M), ImageOutputUSDPer1M: float8Ptr(row.imageOutputUSDPer1M),
		AudioInputUSDPer1M: float8Ptr(row.audioInputUSDPer1M), AudioOutputUSDPer1M: float8Ptr(row.audioOutputUSDPer1M),
		OutputUSDPerImage: float8Ptr(row.outputUSDPerImage), UpdatedAt: timestamptzValue(row.updatedAt),
	}, nil
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
	serviceTierPricesJSON, err := marshalManagementProviderModelPriceMap(input.ServiceTierPrices)
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
		CatalogVisible:                input.CatalogVisible,
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

func marshalManagementProviderModelPriceMap(prices map[string]port.ManagementProviderModelPriceSet) ([]byte, error) {
	if prices == nil {
		prices = map[string]port.ManagementProviderModelPriceSet{}
	}
	return json.Marshal(prices)
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
		ID:                                      row.ID,
		ProviderCode:                            row.ProviderCode,
		Model:                                   row.Model,
		Scope:                                   row.Scope,
		SystemAccountID:                         textValue(row.SystemAccountID),
		Status:                                  row.Status,
		Mode:                                    textValue(row.Mode),
		CatalogOrder:                            int4Ptr(row.CatalogOrder),
		ReleaseDate:                             textValue(row.ReleaseDate),
		ShutdownDate:                            textValue(row.ShutdownDate),
		SupportedAPIProtocols:                   protocols,
		SupportedServiceTiers:                   serviceTiers,
		SupportedReasoningEfforts:               reasoningEfforts,
		DefaultReasoningEffort:                  textValue(row.DefaultReasoningEffort),
		CodexSupportedReasoningLevels:           codexReasoningLevels,
		CodexDefaultReasoningLevel:              textValue(row.CodexDefaultReasoningLevel),
		CodexMultiAgentVersion:                  textValue(row.CodexMultiAgentVersion),
		ContextWindowTokens:                     int4Ptr(row.ContextWindowTokens),
		MaxInputTokens:                          int4Ptr(row.MaxInputTokens),
		MaxOutputTokens:                         int4Ptr(row.MaxOutputTokens),
		MaxTokens:                               int4Ptr(row.MaxTokens),
		InputUSDPer1M:                           float8Ptr(row.InputUsdPer1m),
		OutputUSDPer1M:                          float8Ptr(row.OutputUsdPer1m),
		CachedInputUSDPer1M:                     float8Ptr(row.CachedInputUsdPer1m),
		CacheWriteUSDPer1M:                      float8Ptr(row.CacheWriteUsdPer1m),
		CacheWrite1hUSDPer1M:                    float8Ptr(row.CacheWrite1hUsdPer1m),
		ServiceTierPrices:                       serviceTierPrices,
		LongContextInputTokenThreshold:          int4Ptr(row.LongContextInputTokenThreshold),
		LongContextInputTokenThresholdInclusive: row.LongContextInputTokenThresholdInclusive,
		LongContextInputCostMultiplier:          float8Ptr(row.LongContextInputCostMultiplier),
		LongContextOutputCostMultiplier:         float8Ptr(row.LongContextOutputCostMultiplier),
		ImageInputUSDPer1M:                      float8Ptr(row.ImageInputUsdPer1m),
		ImageOutputUSDPer1M:                     float8Ptr(row.ImageOutputUsdPer1m),
		AudioInputUSDPer1M:                      float8Ptr(row.AudioInputUsdPer1m),
		AudioOutputUSDPer1M:                     float8Ptr(row.AudioOutputUsdPer1m),
		OutputUSDPerImage:                       float8Ptr(row.OutputUsdPerImage),
		SupportsPromptCaching:                   row.SupportsPromptCaching,
		SupportsServiceTier:                     len(serviceTiers) > 0,
		CatalogVisible:                          row.CatalogVisible,
		PricingNotes:                            textValue(row.PricingNotes),
		CapabilityNotes:                         textValue(row.CapabilityNotes),
		Notes:                                   textValue(row.Notes),
		CreatedBy:                               row.CreatedBy,
		UpdatedBy:                               textValue(row.UpdatedBy),
		Source:                                  row.Source,
		CreatedAt:                               timestamptzValue(row.CreatedAt),
		UpdatedAt:                               timestamptzValue(row.UpdatedAt),
	}, nil
}

type managementCustomProviderModelData struct {
	ID                            string
	ProviderCode                  string
	Model                         string
	Scope                         string
	SystemAccountID               pgtype.Text
	Status                        string
	CatalogVisible                bool
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
		CatalogVisible:            row.CatalogVisible,
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
		CatalogVisible:                row.CatalogVisible,
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

func customProviderModelDataFromLockRow(row postgresqueries.LockManagementCustomProviderModelRow) managementCustomProviderModelData {
	return managementCustomProviderModelData{ID: row.ID, ProviderCode: row.ProviderCode, Model: row.Model, Scope: row.Scope, SystemAccountID: row.SystemAccountID, Status: row.Status, CatalogVisible: row.CatalogVisible, Mode: row.Mode, SupportedApiProtocolsJson: row.SupportedApiProtocolsJson, SupportedServiceTiersJson: row.SupportedServiceTiersJson, SupportedReasoningEffortsJson: row.SupportedReasoningEffortsJson, DefaultReasoningEffort: row.DefaultReasoningEffort, ReleaseDate: row.ReleaseDate, ShutdownDate: row.ShutdownDate, ContextWindowTokens: row.ContextWindowTokens, MaxInputTokens: row.MaxInputTokens, MaxOutputTokens: row.MaxOutputTokens, InputUsdPer1m: row.InputUsdPer1m, OutputUsdPer1m: row.OutputUsdPer1m, CachedInputUsdPer1m: row.CachedInputUsdPer1m, CacheWriteUsdPer1m: row.CacheWriteUsdPer1m, CacheWrite1hUsdPer1m: row.CacheWrite1hUsdPer1m, ServiceTierPricesJson: row.ServiceTierPricesJson, ImageInputUsdPer1m: row.ImageInputUsdPer1m, ImageOutputUsdPer1m: row.ImageOutputUsdPer1m, AudioInputUsdPer1m: row.AudioInputUsdPer1m, AudioOutputUsdPer1m: row.AudioOutputUsdPer1m, OutputUsdPerImage: row.OutputUsdPerImage, PricingNotes: row.PricingNotes, CapabilityNotes: row.CapabilityNotes, Notes: row.Notes, CreatedBy: row.CreatedBy, UpdatedBy: row.UpdatedBy, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
}

func customProviderModelDataFromUpdateRow(row postgresqueries.UpdateManagementCustomProviderModelRow) managementCustomProviderModelData {
	return managementCustomProviderModelData{ID: row.ID, ProviderCode: row.ProviderCode, Model: row.Model, Scope: row.Scope, SystemAccountID: row.SystemAccountID, Status: row.Status, CatalogVisible: row.CatalogVisible, Mode: row.Mode, SupportedApiProtocolsJson: row.SupportedApiProtocolsJson, SupportedServiceTiersJson: row.SupportedServiceTiersJson, SupportedReasoningEffortsJson: row.SupportedReasoningEffortsJson, DefaultReasoningEffort: row.DefaultReasoningEffort, ReleaseDate: row.ReleaseDate, ShutdownDate: row.ShutdownDate, ContextWindowTokens: row.ContextWindowTokens, MaxInputTokens: row.MaxInputTokens, MaxOutputTokens: row.MaxOutputTokens, InputUsdPer1m: row.InputUsdPer1m, OutputUsdPer1m: row.OutputUsdPer1m, CachedInputUsdPer1m: row.CachedInputUsdPer1m, CacheWriteUsdPer1m: row.CacheWriteUsdPer1m, CacheWrite1hUsdPer1m: row.CacheWrite1hUsdPer1m, ServiceTierPricesJson: row.ServiceTierPricesJson, ImageInputUsdPer1m: row.ImageInputUsdPer1m, ImageOutputUsdPer1m: row.ImageOutputUsdPer1m, AudioInputUsdPer1m: row.AudioInputUsdPer1m, AudioOutputUsdPer1m: row.AudioOutputUsdPer1m, OutputUsdPerImage: row.OutputUsdPerImage, PricingNotes: row.PricingNotes, CapabilityNotes: row.CapabilityNotes, Notes: row.Notes, CreatedBy: row.CreatedBy, UpdatedBy: row.UpdatedBy, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
}

func customProviderModelDataFromScopeRow(row postgresqueries.FindManagementCustomProviderModelByScopeRow) managementCustomProviderModelData {
	return managementCustomProviderModelData{
		ID:                            row.ID,
		ProviderCode:                  row.ProviderCode,
		Model:                         row.Model,
		Scope:                         row.Scope,
		SystemAccountID:               row.SystemAccountID,
		Status:                        row.Status,
		CatalogVisible:                row.CatalogVisible,
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
		CatalogVisible:                row.CatalogVisible,
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
