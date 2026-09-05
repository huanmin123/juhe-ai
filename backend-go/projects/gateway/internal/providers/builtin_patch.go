// builtin_patch.go ports the built-in model configuration write family of
// backend/src/storage/provider-model-catalog.repository.ts:
// findBuiltInProviderModelPatchStateAsync (full-row read via the shared
// catalog scan) and patchBuiltInProviderModelConfigurationAsync (the
// optimistic-concurrency UPDATE with the manual-override source transition
// and the in-transaction default-reference cleanup).
package providers

import (
	"context"
	"strings"
	"time"
)

// builtinPatchField is one submitted configuration field (JSON key order
// preserved).
type builtinPatchField struct {
	Name  string
	Value any
}

// findBuiltInModelPatchState ports findBuiltInProviderModelPatchStateAsync:
// the full provider_model_catalog row (Node projects the submitted columns;
// the full row is a behavior-identical superset). Returns nil when the id
// does not resolve.
func (s *Store) findBuiltInModelPatchState(ctx context.Context, id string) (*ModelCatalogItem, error) {
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT `+builtInCatalogColumns+`
		FROM `+s.table("provider_model_catalog")+`
		WHERE id = ?
		LIMIT 1`), id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		item, scanErr := scanBuiltInCatalogItem(rows.Scan)
		if scanErr != nil {
			return nil, scanErr
		}
		return &item, rows.Err()
	}
	return nil, rows.Err()
}

// configurationChanges ports providerModelConfigurationChanges: only fields
// whose submitted value differs from the current record survive (JSON
// equality). patch carries the submitted fields in JSON key order.
func configurationChanges(current *ModelCatalogItem, patch []builtinPatchField) []builtinPatchField {
	changes := []builtinPatchField{}
	for _, field := range patch {
		if patchValuesEqual(field.Value, builtInCurrentValue(current, field.Name)) {
			continue
		}
		changes = append(changes, field)
	}
	return changes
}

// builtInCurrentValue resolves the current record value for a patch field
// (nil for absent keys, mirroring the undefined comparison).
func builtInCurrentValue(current *ModelCatalogItem, field string) any {
	switch field {
	case "status":
		return current.Status
	case "catalogVisible":
		if current.CatalogVisible == nil {
			return nil
		}
		return *current.CatalogVisible
	case "mode":
		return nullableTextPtr(current.Mode)
	case "supportedApiProtocols":
		return anySlice(current.SupportedAPIProtocols)
	case "supportedServiceTiers":
		return anySlice(current.SupportedServiceTiers)
	case "supportedReasoningEfforts":
		return anySlice(current.SupportedReasoningEfforts)
	case "defaultReasoningEffort":
		return nullableTextPtr(current.DefaultReasoningEffort)
	case "releaseDate":
		return nullableTextPtr(current.ReleaseDate)
	case "shutdownDate":
		return nullableTextPtr(current.ShutdownDate)
	case "contextWindowTokens":
		return nullableInt64Ptr(current.ContextWindowTokens)
	case "maxInputTokens":
		return nullableInt64Ptr(current.MaxInputTokens)
	case "maxOutputTokens":
		return nullableInt64Ptr(current.MaxOutputTokens)
	case "inputUsdPer1M":
		return nullableFloat64Ptr(current.InputUsdPer1M)
	case "outputUsdPer1M":
		return nullableFloat64Ptr(current.OutputUsdPer1M)
	case "cachedInputUsdPer1M":
		return nullableFloat64Ptr(current.CachedInputUsdPer1M)
	case "cacheWriteUsdPer1M":
		return nullableFloat64Ptr(current.CacheWriteUsdPer1M)
	case "cacheWrite1hUsdPer1M":
		return nullableFloat64Ptr(current.CacheWrite1hUsdPer1M)
	case "cacheStorageUsdPer1MPerHour":
		return nullableFloat64Ptr(current.CacheStorageUsdPer1MPerHour)
	case "serviceTierPrices":
		return serviceTierPricesToAny(current.ServiceTierPrices)
	case "imageInputUsdPer1M":
		return nullableFloat64Ptr(current.ImageInputUsdPer1M)
	case "imageOutputUsdPer1M":
		return nullableFloat64Ptr(current.ImageOutputUsdPer1M)
	case "audioInputUsdPer1M":
		return nullableFloat64Ptr(current.AudioInputUsdPer1M)
	case "audioOutputUsdPer1M":
		return nullableFloat64Ptr(current.AudioOutputUsdPer1M)
	case "outputUsdPerImage":
		return nullableFloat64Ptr(current.OutputUsdPerImage)
	}
	return nil
}

func anySlice(values []string) []any {
	output := make([]any, 0, len(values))
	for _, value := range values {
		output = append(output, value)
	}
	return output
}

func serviceTierPricesToAny(prices map[string]ModelPriceSet) map[string]any {
	output := map[string]any{}
	for tier, set := range prices {
		output[tier] = set
	}
	return output
}

// patchBuiltInModelConfiguration ports patchBuiltInProviderModelConfigurationAsync.
// patch must already be the diffed configuration changes; returns nil when
// the updated_at guard detects a concurrent write.
func (s *Store) patchBuiltInModelConfiguration(ctx context.Context, current *ModelCatalogItem,
	patch []builtinPatchField, expectedUpdatedAt string, cleanup *defaultReferenceCleanupInput) (*customProviderModelMutationRecord, error) {
	assignments, params := builtinPatchAssignments(s, patch)
	if len(assignments) == 0 {
		return builtinMutationRecord(current), nil
	}
	marksManualOverride := false
	for _, field := range patch {
		if field.Name != "status" && field.Name != "catalogVisible" {
			marksManualOverride = true
			break
		}
	}
	sourceValue := "manual-visibility-override"
	sourceAssignment := ", source = CASE WHEN source = 'manual-override' THEN source ELSE ? END"
	if marksManualOverride {
		sourceValue = "manual-override"
		sourceAssignment = ", source = ?"
	}
	updatedAt := nextCustomModelUpdatedAt(expectedUpdatedAt, s.nowUTC())
	writeParams := append([]any{}, params...)
	writeParams = append(writeParams, sourceValue, updatedAt, current.ID, expectedUpdatedAt)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("provider_model_catalog")+`
		SET `+strings.Join(assignments, ", ")+sourceAssignment+`, updated_at = ?
		WHERE id = ? AND updated_at = ?`), writeParams...)
	if err != nil {
		return nil, err
	}
	changes, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if changes == 0 {
		return nil, nil
	}
	if cleanup != nil {
		if _, err := clearUnavailableProviderModelDefaultReferences(ctx, tx, s, cleanup); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return builtinMutationRecordOf(current, patch, updatedAt), nil
}

// builtinPatchAssignments ports configurationPatchAssignments.
func builtinPatchAssignments(s *Store, patch []builtinPatchField) ([]string, []any) {
	assignments := []string{}
	params := []any{}
	add := func(column string, value any) {
		assignments = append(assignments, column+" = ?")
		params = append(params, value)
	}
	for _, field := range patch {
		switch field.Name {
		case "status":
			add("status", nullableText(field.Value))
		case "catalogVisible":
			visible, ok := field.Value.(bool)
			if !ok {
				continue
			}
			add("catalog_visible", s.boolValue(visible))
		case "mode":
			add("mode", nullableText(field.Value))
		case "supportedApiProtocols":
			add("supported_api_protocols_json", mustJSON(stringListJSON(field.Value)))
		case "supportedServiceTiers":
			add("supported_service_tiers_json", mustJSON(stringListJSON(field.Value)))
		case "supportedReasoningEfforts":
			add("supported_reasoning_efforts_json", mustJSON(stringListJSON(field.Value)))
		case "defaultReasoningEffort":
			add("default_reasoning_effort", nullableText(field.Value))
		case "releaseDate":
			add("release_date", nullableText(field.Value))
		case "shutdownDate":
			add("shutdown_date", nullableText(field.Value))
		case "contextWindowTokens":
			add("context_window_tokens", nullableIntegerJSON(field.Value))
		case "maxInputTokens":
			add("max_input_tokens", nullableIntegerJSON(field.Value))
		case "maxOutputTokens":
			add("max_output_tokens", nullableIntegerJSON(field.Value))
		case "inputUsdPer1M":
			add("input_usd_per_1m", nullablePriceJSON(field.Value))
		case "outputUsdPer1M":
			add("output_usd_per_1m", nullablePriceJSON(field.Value))
		case "cachedInputUsdPer1M":
			add("cached_input_usd_per_1m", nullablePriceJSON(field.Value))
		case "cacheWriteUsdPer1M":
			add("cache_write_usd_per_1m", nullablePriceJSON(field.Value))
		case "cacheWrite1hUsdPer1M":
			add("cache_write_1h_usd_per_1m", nullablePriceJSON(field.Value))
		case "cacheStorageUsdPer1MPerHour":
			add("cache_storage_usd_per_1m_per_hour", nullablePriceJSON(field.Value))
		case "serviceTierPrices":
			add("service_tier_prices_json", mustJSON(normalizeServiceTierPricesValue(serviceTierPricesFromAny(field.Value))))
		case "imageInputUsdPer1M":
			add("image_input_usd_per_1m", nullablePriceJSON(field.Value))
		case "imageOutputUsdPer1M":
			add("image_output_usd_per_1m", nullablePriceJSON(field.Value))
		case "audioInputUsdPer1M":
			add("audio_input_usd_per_1m", nullablePriceJSON(field.Value))
		case "audioOutputUsdPer1M":
			add("audio_output_usd_per_1m", nullablePriceJSON(field.Value))
		case "outputUsdPerImage":
			add("output_usd_per_image", nullablePriceJSON(field.Value))
		}
	}
	return assignments, params
}

// nullableText mirrors the repository helper: trimmed non-empty string or nil.
func nullableText(value any) any {
	text, ok := value.(string)
	if !ok {
		return nil
	}
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return nil
	}
	return trimmed
}

// stringListJSON mirrors stringListJSON: keep string entries only.
func stringListJSON(value any) []string {
	items, ok := value.([]any)
	if !ok {
		return []string{}
	}
	output := []string{}
	for _, item := range items {
		if text, ok := item.(string); ok {
			output = append(output, text)
		}
	}
	return output
}

// nullableIntegerJSON mirrors nullableInteger.
func nullableIntegerJSON(value any) any {
	number, ok := value.(float64)
	if !ok || number != float64(int64(number)) || number < 0 {
		return nil
	}
	return int64(number)
}

// nullablePriceJSON mirrors nullablePrice.
func nullablePriceJSON(value any) any {
	number, ok := value.(float64)
	if !ok || number < 0 {
		return nil
	}
	return number
}

// serviceTierPricesFromAny converts the decoded JSON record into the typed
// map for normalization; invalid entries drop.
func serviceTierPricesFromAny(value any) map[string]ModelPriceSet {
	output := map[string]ModelPriceSet{}
	record, ok := value.(map[string]any)
	if !ok {
		return output
	}
	for tier, rawPrices := range record {
		prices, ok := rawPrices.(map[string]any)
		if !ok {
			continue
		}
		set := ModelPriceSet{}
		assignJSONPrice(prices, "inputUsdPer1M", &set.InputUsdPer1M)
		assignJSONPrice(prices, "outputUsdPer1M", &set.OutputUsdPer1M)
		assignJSONPrice(prices, "cachedInputUsdPer1M", &set.CachedInputUsdPer1M)
		assignJSONPrice(prices, "cacheWriteUsdPer1M", &set.CacheWriteUsdPer1M)
		assignJSONPrice(prices, "cacheWrite1hUsdPer1M", &set.CacheWrite1hUsdPer1M)
		assignJSONPrice(prices, "cacheStorageUsdPer1MPerHour", &set.CacheStorageUsdPer1MPerHour)
		assignJSONPrice(prices, "imageInputUsdPer1M", &set.ImageInputUsdPer1M)
		assignJSONPrice(prices, "imageOutputUsdPer1M", &set.ImageOutputUsdPer1M)
		assignJSONPrice(prices, "audioInputUsdPer1M", &set.AudioInputUsdPer1M)
		assignJSONPrice(prices, "audioOutputUsdPer1M", &set.AudioOutputUsdPer1M)
		assignJSONPrice(prices, "outputUsdPerImage", &set.OutputUsdPerImage)
		output[tier] = set
	}
	return output
}

func assignJSONPrice(prices map[string]any, key string, target **float64) {
	number, ok := prices[key].(float64)
	if !ok {
		return
	}
	copied := number
	*target = &copied
}

func builtinMutationRecord(current *ModelCatalogItem) *customProviderModelMutationRecord {
	record := customModelMutationRecordOf(&customProviderModelRecord{
		ID:             current.ID,
		ProviderCode:   current.ProviderCode,
		Model:          current.Model,
		Status:         current.Status,
		CatalogVisible: current.CatalogVisible != nil && *current.CatalogVisible,
		ShutdownDate:   current.ShutdownDate,
		UpdatedAt:      current.UpdatedAt,
	})
	return &record
}

// builtinMutationRecordOf merges the applied patch fields onto the current
// row (Node { ...input.current, ...patch, updatedAt }).
func builtinMutationRecordOf(current *ModelCatalogItem, patch []builtinPatchField, updatedAt string) *customProviderModelMutationRecord {
	status := current.Status
	catalogVisible := current.CatalogVisible != nil && *current.CatalogVisible
	shutdownDate := current.ShutdownDate
	for _, field := range patch {
		switch field.Name {
		case "status":
			if text, ok := field.Value.(string); ok {
				status = text
			}
		case "catalogVisible":
			if visible, ok := field.Value.(bool); ok {
				catalogVisible = visible
			}
		case "shutdownDate":
			shutdownDate = normalizedDateString(field.Value)
		}
	}
	record := customProviderModelMutationRecord{
		ID:             current.ID,
		ProviderCode:   current.ProviderCode,
		Model:          current.Model,
		Status:         status,
		CatalogVisible: catalogVisible,
		ShutdownDate:   shutdownDate,
		UpdatedAt:      updatedAt,
	}
	return &record
}

func normalizedDateString(value any) *string {
	text, ok := value.(string)
	if !ok {
		return nil
	}
	trimmed := strings.TrimSpace(text)
	if trimmed == "" || !customModelDatePattern.MatchString(trimmed) {
		return nil
	}
	return &trimmed
}

// nextProviderModelUpdatedAt mirrors nextProviderModelUpdatedAt with the
// expected stamp as the concurrency base.
func nextProviderModelUpdatedAt(expected string, now time.Time) string {
	return nextCustomModelUpdatedAt(expected, now)
}
