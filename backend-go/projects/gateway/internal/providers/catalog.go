// catalog.go owns the provider model catalog read family ported from
// model-catalog.service.ts + provider-model-catalog.repository.ts +
// custom-provider-models.repository.ts: the merged built-in/custom catalog
// (GET /{code}/models), the merged model selection options
// (GET /models/options) and the model capabilities lookup
// (GET /{code}/models/{modelId}/capabilities). Fields sourced from Node's
// static in-code pricing tables (inputModalities, outputModalities,
// supportedTools, generationParameterCapabilities, catalogDisplay, the
// cachedImageInput/sourcePricing fallbacks) are shape-preserved as empty
// values and are not fabricated.
package providers

import (
	"context"
	"database/sql"
	"encoding/json"
	"sort"
	"strings"
)

// ModelPriceSet mirrors ProviderModelPriceSet.
type ModelPriceSet struct {
	InputUsdPer1M               *float64 `json:"inputUsdPer1M,omitempty"`
	OutputUsdPer1M              *float64 `json:"outputUsdPer1M,omitempty"`
	CachedInputUsdPer1M         *float64 `json:"cachedInputUsdPer1M,omitempty"`
	CacheWriteUsdPer1M          *float64 `json:"cacheWriteUsdPer1M,omitempty"`
	CacheWrite1hUsdPer1M        *float64 `json:"cacheWrite1hUsdPer1M,omitempty"`
	CacheStorageUsdPer1MPerHour *float64 `json:"cacheStorageUsdPer1MPerHour,omitempty"`
	ImageInputUsdPer1M          *float64 `json:"imageInputUsdPer1M,omitempty"`
	ImageOutputUsdPer1M         *float64 `json:"imageOutputUsdPer1M,omitempty"`
	AudioInputUsdPer1M          *float64 `json:"audioInputUsdPer1M,omitempty"`
	AudioOutputUsdPer1M         *float64 `json:"audioOutputUsdPer1M,omitempty"`
	OutputUsdPerImage           *float64 `json:"outputUsdPerImage,omitempty"`
}

// ModelCatalogItem mirrors the ProviderModelCatalogItem projection served by
// GET /{code}/models. Pointers carrying omitempty correspond to the Node
// optional keys; defaultReasoningEffort is nullable in Node (always present)
// while serviceTierPrices / the always-array keys are non-optional objects.
type ModelCatalogItem struct {
	ID                                      string                   `json:"id"`
	ProviderCode                            string                   `json:"providerCode"`
	Model                                   string                   `json:"model"`
	Scope                                   string                   `json:"scope"`
	Status                                  string                   `json:"status"`
	CatalogVisible                          *bool                    `json:"catalogVisible,omitempty"`
	SystemAccountID                         *string                  `json:"systemAccountId,omitempty"`
	Mode                                    *string                  `json:"mode,omitempty"`
	CatalogOrder                            *int64                   `json:"catalogOrder,omitempty"`
	ReleaseDate                             *string                  `json:"releaseDate,omitempty"`
	ShutdownDate                            *string                  `json:"shutdownDate,omitempty"`
	SupportedAPIProtocols                   []string                 `json:"supportedApiProtocols"`
	InputModalities                         []string                 `json:"inputModalities"`
	OutputModalities                        []string                 `json:"outputModalities"`
	SupportedTools                          []string                 `json:"supportedTools"`
	GenerationParameterCapabilities         map[string]any           `json:"generationParameterCapabilities"`
	SupportedServiceTiers                   []string                 `json:"supportedServiceTiers"`
	SupportedReasoningEfforts               []string                 `json:"supportedReasoningEfforts"`
	DefaultReasoningEffort                  *string                  `json:"defaultReasoningEffort"`
	CodexSupportedReasoningLevels           []string                 `json:"codexSupportedReasoningLevels"`
	CodexDefaultReasoningLevel              *string                  `json:"codexDefaultReasoningLevel,omitempty"`
	CodexMultiAgentVersion                  *string                  `json:"codexMultiAgentVersion,omitempty"`
	ContextWindowTokens                     *int64                   `json:"contextWindowTokens,omitempty"`
	MaxInputTokens                          *int64                   `json:"maxInputTokens,omitempty"`
	MaxOutputTokens                         *int64                   `json:"maxOutputTokens,omitempty"`
	MaxTokens                               *int64                   `json:"maxTokens,omitempty"`
	InputUsdPer1M                           *float64                 `json:"inputUsdPer1M,omitempty"`
	OutputUsdPer1M                          *float64                 `json:"outputUsdPer1M,omitempty"`
	CachedInputUsdPer1M                     *float64                 `json:"cachedInputUsdPer1M,omitempty"`
	CacheWriteUsdPer1M                      *float64                 `json:"cacheWriteUsdPer1M,omitempty"`
	CacheWrite1hUsdPer1M                    *float64                 `json:"cacheWrite1hUsdPer1M,omitempty"`
	CacheStorageUsdPer1MPerHour             *float64                 `json:"cacheStorageUsdPer1MPerHour,omitempty"`
	ServiceTierPrices                       map[string]ModelPriceSet `json:"serviceTierPrices"`
	LongContextInputTokenThreshold          *int64                   `json:"longContextInputTokenThreshold,omitempty"`
	LongContextInputTokenThresholdInclusive *bool                    `json:"longContextInputTokenThresholdInclusive,omitempty"`
	LongContextInputCostMultiplier          *float64                 `json:"longContextInputCostMultiplier,omitempty"`
	LongContextOutputCostMultiplier         *float64                 `json:"longContextOutputCostMultiplier,omitempty"`
	ImageInputUsdPer1M                      *float64                 `json:"imageInputUsdPer1M,omitempty"`
	ImageOutputUsdPer1M                     *float64                 `json:"imageOutputUsdPer1M,omitempty"`
	AudioInputUsdPer1M                      *float64                 `json:"audioInputUsdPer1M,omitempty"`
	AudioOutputUsdPer1M                     *float64                 `json:"audioOutputUsdPer1M,omitempty"`
	OutputUsdPerImage                       *float64                 `json:"outputUsdPerImage,omitempty"`
	SupportsPromptCaching                   bool                     `json:"supportsPromptCaching"`
	SupportsServiceTier                     bool                     `json:"supportsServiceTier"`
	PricingNotes                            *string                  `json:"pricingNotes,omitempty"`
	CapabilityNotes                         *string                  `json:"capabilityNotes,omitempty"`
	Notes                                   *string                  `json:"notes,omitempty"`
	CreatedAt                               string                   `json:"createdAt"`
	UpdatedAt                               string                   `json:"updatedAt"`
	Source                                  string                   `json:"source"`
}

// ModelSelectionOption mirrors ProviderModelSelectionOption.
type ModelSelectionOption struct {
	ID                        string   `json:"id"`
	Name                      string   `json:"name"`
	SupportedAPIProtocols     []string `json:"supportedApiProtocols"`
	SupportedServiceTiers     []string `json:"supportedServiceTiers"`
	SupportedReasoningEfforts []string `json:"supportedReasoningEfforts"`
	DefaultReasoningEffort    *string  `json:"defaultReasoningEffort,omitempty"`
}

// ModelCapabilities mirrors the findProviderModelCapabilitiesAsync payload.
type ModelCapabilities struct {
	ID                        string   `json:"id"`
	Name                      string   `json:"name"`
	SupportedAPIProtocols     []string `json:"supportedApiProtocols"`
	SupportedServiceTiers     []string `json:"supportedServiceTiers"`
	SupportedReasoningEfforts []string `json:"supportedReasoningEfforts"`
	DefaultReasoningEffort    *string  `json:"defaultReasoningEffort,omitempty"`
}

// ModelOptionQuery mirrors normalizeProviderModelOptionQuery output plus the
// resolved access-scope system account.
type ModelOptionQuery struct {
	ProviderCode    string
	Protocol        string
	Keyword         string
	Limit           int
	SelectedIDs     []string
	SystemAccountID string
}

const catalogScopeBuiltIn = "built_in"
const catalogScopeGlobal = "global"
const catalogScopePersonal = "personal"

// ListProviderModelsForRequest ports listProviderModelsForRequestAsync (the
// GET /{code}/models body): hybrid codes flatten the catalogs of every
// enabled non-hybrid provider; everything else expands through
// modelCatalogSourceProviderCodesAsync.
func (s *Store) ListProviderModelsForRequest(ctx context.Context, providerCode, systemAccountID string, includeInactive, includeUnpriced bool) ([]ModelCatalogItem, error) {
	if isHybridProviderCode(providerCode) {
		options, err := s.ListProviderOptions(ctx)
		if err != nil {
			return nil, err
		}
		merged := []ModelCatalogItem{}
		for _, option := range options {
			if !option.Enabled || isHybridProviderCode(option.Code) {
				continue
			}
			items, err := s.listProviderModelCatalog(ctx, option.Code, systemAccountID, includeInactive, includeUnpriced)
			if err != nil {
				return nil, err
			}
			merged = append(merged, items...)
		}
		return merged, nil
	}
	return s.listProviderModelCatalog(ctx, providerCode, systemAccountID, includeInactive, includeUnpriced)
}

// listProviderModelCatalog ports buildProviderModelCatalogAsync: source
// expansion, built-in + custom merge (scope priority), the supported-model
// filter, the active/priced filters and the release-date ordering.
func (s *Store) listProviderModelCatalog(ctx context.Context, providerCode, systemAccountID string, includeInactive, includeUnpriced bool) ([]ModelCatalogItem, error) {
	sourceCodes, err := s.ModelCatalogSourceProviderCodes(ctx, providerCode)
	if err != nil {
		return nil, err
	}
	if len(sourceCodes) == 0 {
		return []ModelCatalogItem{}, nil
	}
	builtInCodes := modelCatalogBuiltInSourceProviderCodes(providerCode, sourceCodes)
	builtIn, err := s.listBuiltInCatalogModels(ctx, builtInCodes, includeInactive)
	if err != nil {
		return nil, err
	}
	custom, err := s.listCustomCatalogModels(ctx, sourceCodes, systemAccountID, includeInactive)
	if err != nil {
		return nil, err
	}
	items := mergeModelCatalogItems(append(builtIn, custom...), isHybridProviderCode(providerCode))
	filtered := []ModelCatalogItem{}
	for _, item := range items {
		if !isSupportedCatalogModel(item) {
			continue
		}
		if !includeInactive && item.Status != "active" {
			continue
		}
		if !includeUnpriced && !hasDirectPrice(item) {
			continue
		}
		filtered = append(filtered, item)
	}
	sort.SliceStable(filtered, func(left, right int) bool {
		return compareProviderModelCatalogItems(filtered[left], filtered[right]) < 0
	})
	return filtered, nil
}

// ModelCatalogSourceProviderCodes ports modelCatalogSourceProviderCodesAsync:
// hybrid expands to every enabled openai/anthropic/gemini protocol provider,
// the openai-compatible provider expands to its openai-protocol children plus
// itself, anything else is its own (normalized) source.
func (s *Store) ModelCatalogSourceProviderCodes(ctx context.Context, providerCode string) ([]string, error) {
	normalized := normalizeProviderToken(providerCode)
	if normalized == "" {
		return []string{}, nil
	}
	if normalized == hybridProviderCode {
		codes := []string{}
		for _, pair := range [][2]string{
			{openaiProtocolCode, openaiProtocolVersion},
			{anthropicProtocolCode, anthropicProtocolVersion},
			{geminiProtocolCode, geminiProtocolVersion},
		} {
			list, err := s.ProtocolProviderCodes(ctx, pair[0], pair[1])
			if err != nil {
				return nil, err
			}
			for _, code := range list {
				token := normalizeProviderToken(code)
				if token == "" || token == hybridProviderCode {
					continue
				}
				codes = append(codes, token)
			}
		}
		return dedupeStrings(codes), nil
	}
	if normalized != openaiProviderCode {
		return []string{normalized}, nil
	}
	list, err := s.ProtocolProviderCodes(ctx, openaiProtocolCode, openaiProtocolVersion)
	if err != nil {
		return nil, err
	}
	codes := []string{}
	for _, code := range list {
		token := normalizeProviderToken(code)
		if token == "" || token == normalized {
			continue
		}
		codes = append(codes, token)
	}
	return dedupeStrings(append(codes, normalized)), nil
}

// modelCatalogBuiltInSourceProviderCodes ports the same-named helper: only
// the openai-compatible target drops itself from its built-in sources.
func modelCatalogBuiltInSourceProviderCodes(providerCode string, sourceProviderCodes []string) []string {
	if normalizeProviderToken(providerCode) != openaiProviderCode {
		return sourceProviderCodes
	}
	codes := []string{}
	for _, code := range sourceProviderCodes {
		if normalizeProviderToken(code) == openaiProviderCode {
			continue
		}
		codes = append(codes, code)
	}
	return codes
}

// builtInCatalogColumns mirrors the provider-model-catalog.repository
// columns() projection.
const builtInCatalogColumns = `id, provider_code, model, status, mode, catalog_order, release_date, shutdown_date,
	supported_api_protocols_json, supported_service_tiers_json, supported_reasoning_efforts_json,
	default_reasoning_effort, codex_supported_reasoning_levels_json, codex_default_reasoning_level,
	codex_multi_agent_version, context_window_tokens, max_input_tokens, max_output_tokens, max_tokens,
	input_usd_per_1m, output_usd_per_1m, cached_input_usd_per_1m, cache_write_usd_per_1m,
	cache_write_1h_usd_per_1m, cache_storage_usd_per_1m_per_hour, service_tier_prices_json,
	long_context_input_token_threshold, long_context_input_token_threshold_inclusive,
	long_context_input_cost_multiplier, long_context_output_cost_multiplier,
	image_input_usd_per_1m, image_output_usd_per_1m, audio_input_usd_per_1m, audio_output_usd_per_1m,
	output_usd_per_image, supports_prompt_caching, catalog_visible, source, created_at, updated_at`

// listBuiltInCatalogModels mirrors listBuiltInProviderModels(Async): the
// availability filter (active + catalog_visible + not shutdown) is applied
// in SQL unless includeInactive lifts it (provider-model-catalog.repository
// .ts:244-282).
func (s *Store) listBuiltInCatalogModels(ctx context.Context, providerCodes []string, includeInactive bool) ([]ModelCatalogItem, error) {
	codes := normalizeProviderCodeList(providerCodes)
	if len(codes) == 0 {
		return []ModelCatalogItem{}, nil
	}
	availabilityFilter := ""
	if !includeInactive {
		availabilityFilter = `
		AND status = 'active'
		AND catalog_visible = 1
		AND (shutdown_date IS NULL OR trim(shutdown_date) = '' OR shutdown_date > ` + s.todayText() + `)`
	}
	args := append([]any{}, stringSliceToAny(codes)...)
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT `+builtInCatalogColumns+`
		FROM `+s.table("provider_model_catalog")+`
		WHERE provider_code IN (`+placeholders(len(codes))+`)`+availabilityFilter+`
		ORDER BY provider_code, catalog_order, model, id`), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []ModelCatalogItem{}
	for rows.Next() {
		item, scanErr := scanBuiltInCatalogItem(rows.Scan)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func scanBuiltInCatalogItem(scan func(...any) error) (ModelCatalogItem, error) {
	var (
		item                             ModelCatalogItem
		mode, releaseDate, shutdownDate  sql.NullString
		catalogOrder                     sql.NullInt64
		protocols, tiers, efforts        sql.NullString
		codexLevels                      sql.NullString
		codexDefaultLevel                sql.NullString
		codexMultiAgent                  sql.NullString
		contextWindow, maxInput, maxOut  sql.NullInt64
		maxTokens                        sql.NullInt64
		inputUsd, outputUsd              sql.NullFloat64
		cachedInput, cacheWrite          sql.NullFloat64
		cacheWrite1h, cacheStorage       sql.NullFloat64
		tierPrices                       sql.NullString
		longThreshold                    sql.NullInt64
		longInclusive                    sql.NullInt64
		longInputMultiplier, longOutMult sql.NullFloat64
		imageIn, imageOut, audioIn       sql.NullFloat64
		audioOut, outputPerImage         sql.NullFloat64
		promptCaching, catalogVisible    sql.NullInt64
	)
	var defaultEffort sql.NullString
	if err := scan(&item.ID, &item.ProviderCode, &item.Model, &item.Status, &mode, &catalogOrder,
		&releaseDate, &shutdownDate, &protocols, &tiers, &efforts, &defaultEffort,
		&codexLevels, &codexDefaultLevel, &codexMultiAgent,
		&contextWindow, &maxInput, &maxOut, &maxTokens,
		&inputUsd, &outputUsd, &cachedInput, &cacheWrite, &cacheWrite1h, &cacheStorage,
		&tierPrices, &longThreshold, &longInclusive, &longInputMultiplier, &longOutMult,
		&imageIn, &imageOut, &audioIn, &audioOut, &outputPerImage,
		&promptCaching, &catalogVisible, &item.Source, &item.CreatedAt, &item.UpdatedAt); err != nil {
		return ModelCatalogItem{}, err
	}
	item.Scope = catalogScopeBuiltIn
	item.Mode = textPtr(mode)
	item.ReleaseDate = textPtr(releaseDate)
	item.ShutdownDate = textPtr(shutdownDate)
	item.CatalogOrder = nullInt64Ptr(catalogOrder)
	item.SupportedAPIProtocols = parseJSONArray(protocols)
	item.SupportedServiceTiers = parseJSONArray(tiers)
	item.SupportedReasoningEfforts = parseJSONArray(efforts)
	item.DefaultReasoningEffort = textPtr(defaultEffort)
	item.CodexSupportedReasoningLevels = parseJSONArray(codexLevels)
	item.CodexDefaultReasoningLevel = textPtr(codexDefaultLevel)
	item.CodexMultiAgentVersion = textPtr(codexMultiAgent)
	item.ContextWindowTokens = nullInt64Ptr(contextWindow)
	item.MaxInputTokens = nullInt64Ptr(maxInput)
	item.MaxOutputTokens = nullInt64Ptr(maxOut)
	item.MaxTokens = nullInt64Ptr(maxTokens)
	item.InputUsdPer1M = nullFloat64Ptr(inputUsd)
	item.OutputUsdPer1M = nullFloat64Ptr(outputUsd)
	item.CachedInputUsdPer1M = nullFloat64Ptr(cachedInput)
	item.CacheWriteUsdPer1M = nullFloat64Ptr(cacheWrite)
	item.CacheWrite1hUsdPer1M = nullFloat64Ptr(cacheWrite1h)
	item.CacheStorageUsdPer1MPerHour = nullFloat64Ptr(cacheStorage)
	item.ServiceTierPrices = normalizeServiceTierPrices(tierPrices)
	item.LongContextInputTokenThreshold = nullInt64Ptr(longThreshold)
	if longInclusive.Valid {
		inclusive := longInclusive.Int64 == 1
		item.LongContextInputTokenThresholdInclusive = &inclusive
	}
	item.LongContextInputCostMultiplier = nullFloat64Ptr(longInputMultiplier)
	item.LongContextOutputCostMultiplier = nullFloat64Ptr(longOutMult)
	item.ImageInputUsdPer1M = nullFloat64Ptr(imageIn)
	item.ImageOutputUsdPer1M = nullFloat64Ptr(imageOut)
	item.AudioInputUsdPer1M = nullFloat64Ptr(audioIn)
	item.AudioOutputUsdPer1M = nullFloat64Ptr(audioOut)
	item.OutputUsdPerImage = nullFloat64Ptr(outputPerImage)
	item.SupportsPromptCaching = promptCaching.Int64 == 1 && promptCaching.Valid
	visible := catalogVisible.Int64 == 1 && catalogVisible.Valid
	item.CatalogVisible = &visible
	item.InputModalities = []string{}
	item.OutputModalities = []string{}
	item.SupportedTools = []string{}
	item.GenerationParameterCapabilities = map[string]any{}
	item.SupportsServiceTier = len(item.SupportedServiceTiers) > 0
	return item, nil
}

// listCustomCatalogModels mirrors listCustomProviderModelsForCatalogAsync
// fanned across the source codes (single IN query, same clauses).
func (s *Store) listCustomCatalogModels(ctx context.Context, providerCodes []string, systemAccountID string, includeInactive bool) ([]ModelCatalogItem, error) {
	return s.listCustomModelRows(ctx, providerCodes, systemAccountID, includeInactive, "")
}

// listCustomModelRows shares the custom_provider_models read shape; a
// non-empty modelFilter pins the rows to one model (the capabilities path).
// includeInactive=false carries the active + shutdown clauses, matching both
// the catalog default and the always-on test-catalog predicates.
func (s *Store) listCustomModelRows(ctx context.Context, providerCodes []string, systemAccountID string, includeInactive bool, modelFilter string) ([]ModelCatalogItem, error) {
	codes := normalizeProviderCodeList(providerCodes)
	if len(codes) == 0 {
		return []ModelCatalogItem{}, nil
	}
	clauses := []string{"provider_code IN (" + placeholders(len(codes)) + ")"}
	args := append([]any{}, stringSliceToAny(codes)...)
	if !includeInactive {
		clauses = append(clauses, "status = 'active'",
			"(shutdown_date IS NULL OR trim(shutdown_date) = '' OR shutdown_date > "+s.todayText()+")")
	}
	if trimmedAccount := strings.TrimSpace(systemAccountID); trimmedAccount != "" {
		clauses = append(clauses, "((scope = 'global' AND system_account_id IS NULL) OR (scope = 'personal' AND system_account_id = ?))")
		args = append(args, trimmedAccount)
	} else {
		clauses = append(clauses, "scope = 'global' AND system_account_id IS NULL")
	}
	if modelFilter != "" {
		clauses = append(clauses, "model = ?")
		args = append(args, modelFilter)
	}
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT id, provider_code, model, scope, system_account_id, status,
			mode, release_date, supported_api_protocols_json, supported_service_tiers_json,
			supported_reasoning_efforts_json, default_reasoning_effort,
			context_window_tokens, max_input_tokens, max_output_tokens,
			input_usd_per_1m, output_usd_per_1m, cached_input_usd_per_1m, cache_write_usd_per_1m,
			cache_write_1h_usd_per_1m, cache_storage_usd_per_1m_per_hour, service_tier_prices_json,
			image_input_usd_per_1m, image_output_usd_per_1m, audio_input_usd_per_1m, audio_output_usd_per_1m,
			output_usd_per_image, pricing_notes, capability_notes, notes, created_at, updated_at
		FROM `+s.table("custom_provider_models")+`
		WHERE `+strings.Join(clauses, " AND ")+`
		ORDER BY provider_code ASC, scope ASC, lower(model) ASC, id ASC`), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []ModelCatalogItem{}
	for rows.Next() {
		item, scanErr := scanCustomCatalogItem(rows.Scan)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func scanCustomCatalogItem(scan func(...any) error) (ModelCatalogItem, error) {
	var (
		item                            ModelCatalogItem
		systemAccountID                 sql.NullString
		mode, releaseDate, shutdownDate sql.NullString
		protocols, tiers, efforts       sql.NullString
		defaultEffort                   sql.NullString
		contextWindow, maxInput         sql.NullInt64
		maxOut                          sql.NullInt64
		inputUsd, outputUsd             sql.NullFloat64
		cachedInput, cacheWrite         sql.NullFloat64
		cacheWrite1h, cacheStorage      sql.NullFloat64
		tierPrices                      sql.NullString
		imageIn, imageOut, audioIn      sql.NullFloat64
		audioOut, outputPerImage        sql.NullFloat64
		pricingNotes, capabilityNotes   sql.NullString
		notes                           sql.NullString
	)
	if err := scan(&item.ID, &item.ProviderCode, &item.Model, &item.Scope, &systemAccountID, &item.Status,
		&mode, &releaseDate, &protocols, &tiers, &efforts, &defaultEffort,
		&contextWindow, &maxInput, &maxOut,
		&inputUsd, &outputUsd, &cachedInput, &cacheWrite, &cacheWrite1h, &cacheStorage,
		&tierPrices,
		&imageIn, &imageOut, &audioIn, &audioOut, &outputPerImage,
		&pricingNotes, &capabilityNotes, &notes, &item.CreatedAt, &item.UpdatedAt); err != nil {
		return ModelCatalogItem{}, err
	}
	item.SystemAccountID = nullPtrString(systemAccountID)
	item.Mode = textPtr(mode)
	item.ReleaseDate = textPtr(releaseDate)
	item.ShutdownDate = textPtr(shutdownDate)
	item.SupportedAPIProtocols = parseJSONArray(protocols)
	item.SupportedServiceTiers = parseJSONArray(tiers)
	item.SupportedReasoningEfforts = parseJSONArray(efforts)
	if defaultEffort.Valid && strings.TrimSpace(defaultEffort.String) != "" {
		trimmed := strings.TrimSpace(defaultEffort.String)
		item.DefaultReasoningEffort = &trimmed
	}
	item.ContextWindowTokens = nullInt64Ptr(contextWindow)
	item.MaxInputTokens = nullInt64Ptr(maxInput)
	item.MaxOutputTokens = nullInt64Ptr(maxOut)
	item.InputUsdPer1M = nullFloat64Ptr(inputUsd)
	item.OutputUsdPer1M = nullFloat64Ptr(outputUsd)
	item.CachedInputUsdPer1M = nullFloat64Ptr(cachedInput)
	item.CacheWriteUsdPer1M = nullFloat64Ptr(cacheWrite)
	item.CacheWrite1hUsdPer1M = nullFloat64Ptr(cacheWrite1h)
	item.CacheStorageUsdPer1MPerHour = nullFloat64Ptr(cacheStorage)
	item.ServiceTierPrices = normalizeServiceTierPrices(tierPrices)
	item.ImageInputUsdPer1M = nullFloat64Ptr(imageIn)
	item.ImageOutputUsdPer1M = nullFloat64Ptr(imageOut)
	item.AudioInputUsdPer1M = nullFloat64Ptr(audioIn)
	item.AudioOutputUsdPer1M = nullFloat64Ptr(audioOut)
	item.OutputUsdPerImage = nullFloat64Ptr(outputPerImage)
	item.PricingNotes = textPtr(pricingNotes)
	item.CapabilityNotes = textPtr(capabilityNotes)
	item.Notes = textPtr(notes)
	// toCustomCatalogItem: empty static capability shapes, prompt caching is
	// derived from the cached-input price, source from the scope.
	item.InputModalities = []string{}
	item.OutputModalities = []string{}
	item.SupportedTools = []string{}
	item.GenerationParameterCapabilities = map[string]any{}
	item.CodexSupportedReasoningLevels = []string{}
	item.SupportsPromptCaching = item.CachedInputUsdPer1M != nil
	item.SupportsServiceTier = len(item.SupportedServiceTiers) > 0
	if item.Scope == catalogScopeGlobal {
		item.Source = "custom-global"
	} else {
		item.Source = "custom-personal"
	}
	return item, nil
}

// mergeModelCatalogItems ports mergeModelCatalogItems: dedupe by model (or
// provider+model for the hybrid identity), higher scope priority wins and
// later rows win ties.
func mergeModelCatalogItems(items []ModelCatalogItem, preserveProviderIdentity bool) []ModelCatalogItem {
	type key struct {
		provider string
		model    string
	}
	merged := map[key]ModelCatalogItem{}
	order := []key{}
	for _, item := range items {
		model := strings.TrimSpace(item.Model)
		if model == "" {
			continue
		}
		itemKey := key{model: model}
		if preserveProviderIdentity {
			itemKey.provider = normalizeProviderToken(item.ProviderCode)
		}
		existing, ok := merged[itemKey]
		if !ok || catalogScopePriority(item.Scope) >= catalogScopePriority(existing.Scope) {
			if !ok {
				order = append(order, itemKey)
			}
			merged[itemKey] = item
		}
	}
	output := make([]ModelCatalogItem, 0, len(order))
	for _, itemKey := range order {
		output = append(output, merged[itemKey])
	}
	return output
}

func catalogScopePriority(scope string) int {
	if scope == catalogScopePersonal {
		return 3
	}
	if scope == catalogScopeGlobal {
		return 2
	}
	return 1
}

// isSupportedCatalogModel ports isSupportedCatalogModel.
func isSupportedCatalogModel(item ModelCatalogItem) bool {
	mode := ""
	if item.Mode != nil {
		mode = strings.ToLower(strings.TrimSpace(*item.Mode))
	}
	if mode == "audio" || mode == "audio_speech" || mode == "audio_transcription" {
		return false
	}
	for _, protocol := range item.SupportedAPIProtocols {
		if protocol == "realtime" {
			return false
		}
	}
	if len(item.SupportedAPIProtocols) == 1 && item.SupportedAPIProtocols[0] == "audio" {
		return false
	}
	model := strings.ToLower(strings.TrimSpace(item.Model))
	for _, token := range []string{"audio", "realtime", "transcribe", "tts", "whisper"} {
		if matchesModelToken(model, token) {
			return false
		}
	}
	return true
}

// matchesModelToken mirrors /(?:^|[-_.])(token)(?:$|[-_.])/.
func matchesModelToken(model, token string) bool {
	position := 0
	for position <= len(model) {
		found := strings.Index(model[position:], token)
		if found < 0 {
			return false
		}
		start := position + found
		end := start + len(token)
		if !modelTokenBoundary(model, start) {
			position = start + 1
			continue
		}
		if !modelTokenBoundaryEnd(model, end) {
			position = start + 1
			continue
		}
		return true
	}
	return false
}

func modelTokenBoundary(model string, index int) bool {
	if index == 0 {
		return true
	}
	switch model[index-1] {
	case '-', '_', '.':
		return true
	}
	return false
}

func modelTokenBoundaryEnd(model string, index int) bool {
	if index == len(model) {
		return true
	}
	switch model[index] {
	case '-', '_', '.':
		return true
	}
	return false
}

// hasDirectPrice ports hasDirectPrice.
func hasDirectPrice(item ModelCatalogItem) bool {
	if item.InputUsdPer1M != nil || item.OutputUsdPer1M != nil || item.CachedInputUsdPer1M != nil ||
		item.CacheWriteUsdPer1M != nil || item.CacheWrite1hUsdPer1M != nil ||
		item.CacheStorageUsdPer1MPerHour != nil || item.ImageInputUsdPer1M != nil ||
		item.ImageOutputUsdPer1M != nil || item.AudioInputUsdPer1M != nil ||
		item.AudioOutputUsdPer1M != nil || item.OutputUsdPerImage != nil {
		return true
	}
	return len(item.ServiceTierPrices) > 0
}

// compareProviderModelCatalogItems ports compareProviderModelCatalogItems:
// newer release dates sort first (right.localeCompare(left) in Node).
func compareProviderModelCatalogItems(left, right ModelCatalogItem) int {
	leftRelease := sortableCatalogReleaseDate(left.ReleaseDate)
	rightRelease := sortableCatalogReleaseDate(right.ReleaseDate)
	if leftRelease != "" && rightRelease != "" && leftRelease != rightRelease {
		if leftRelease > rightRelease {
			return -1
		}
		return 1
	}
	if leftRelease != "" && rightRelease == "" {
		return -1
	}
	if leftRelease == "" && rightRelease != "" {
		return 1
	}
	if left.CatalogOrder != nil && right.CatalogOrder != nil && *left.CatalogOrder != *right.CatalogOrder {
		if *left.CatalogOrder < *right.CatalogOrder {
			return -1
		}
		return 1
	}
	if order := strings.Compare(left.Model, right.Model); order != 0 {
		return order
	}
	return strings.Compare(left.ID, right.ID)
}

func sortableCatalogReleaseDate(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

// todayText renders the SQL "today" literal for the shutdown-date predicate
// (SQLite date('now') vs PostgreSQL CURRENT_DATE::text).
func (s *Store) todayText() string {
	if s.pg {
		return "CURRENT_DATE::text"
	}
	return "date('now')"
}

// ListProviderModelSelectionOptions ports listProviderModelSelectionOptionsAsync
// (the GET /models/options body).
func (s *Store) ListProviderModelSelectionOptions(ctx context.Context, query ModelOptionQuery) ([]ModelSelectionOption, error) {
	sourceCodes, builtInCodes, err := s.modelOptionSourceCodes(ctx, query.ProviderCode, query.Protocol)
	if err != nil {
		return nil, err
	}
	if len(sourceCodes) == 0 {
		return []ModelSelectionOption{}, nil
	}
	builtIn, err := s.listBuiltInModelOptions(ctx, builtInCodes, query)
	if err != nil {
		return nil, err
	}
	custom, err := s.listCustomModelOptions(ctx, sourceCodes, query)
	if err != nil {
		return nil, err
	}
	return mergeProviderModelOptionRows(append(builtIn, custom...), query), nil
}

// modelOptionSourceCodes mirrors providerModelSourceCodesAsync.
func (s *Store) modelOptionSourceCodes(ctx context.Context, providerCode, protocol string) (sourceCodes, builtInCodes []string, err error) {
	if providerCode != "" {
		sourceCodes, err = s.ModelCatalogSourceProviderCodes(ctx, providerCode)
		if err != nil {
			return nil, nil, err
		}
		return sourceCodes, modelCatalogBuiltInSourceProviderCodes(providerCode, sourceCodes), nil
	}
	switch protocol {
	case "openai":
		sourceCodes, err = s.ProtocolProviderCodes(ctx, openaiProtocolCode, openaiProtocolVersion)
	case "anthropic":
		sourceCodes, err = s.ProtocolProviderCodes(ctx, anthropicProtocolCode, anthropicProtocolVersion)
	case "gemini":
		sourceCodes, err = s.ProtocolProviderCodes(ctx, geminiProtocolCode, geminiProtocolVersion)
	default:
		sourceCodes, err = s.EnabledNonHybridProviderCodes(ctx)
	}
	if err != nil {
		return nil, nil, err
	}
	codes := []string{}
	for _, code := range sourceCodes {
		trimmed := strings.TrimSpace(code)
		if trimmed == "" {
			continue
		}
		codes = append(codes, trimmed)
	}
	return codes, codes, nil
}

// modelOptionRow mirrors ProviderModelOptionRow.
type modelOptionRow struct {
	ProviderCode              string
	Model                     string
	Scope                     string
	ReleaseDate               *string
	SupportedAPIProtocols     []string
	SupportedServiceTiers     []string
	SupportedReasoningEfforts []string
	DefaultReasoningEffort    *string
}

func (s *Store) listBuiltInModelOptions(ctx context.Context, providerCodes []string, query ModelOptionQuery) ([]modelOptionRow, error) {
	codes := normalizeProviderCodeList(providerCodes)
	if len(codes) == 0 {
		return []modelOptionRow{}, nil
	}
	clauses := []string{
		"provider_code IN (" + placeholders(len(codes)) + ")",
		"status = 'active'",
		"catalog_visible = 1",
		"(shutdown_date IS NULL OR trim(shutdown_date) = '' OR shutdown_date > " + s.todayText() + ")",
	}
	args := append([]any{}, stringSliceToAny(codes)...)
	args = appendOptionTextFilter(&clauses, args, query)
	args = appendSelectedOrderArgs(args, query)
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT provider_code, model, mode, release_date,
			supported_api_protocols_json, supported_service_tiers_json, supported_reasoning_efforts_json,
			default_reasoning_effort
		FROM `+s.table("provider_model_catalog")+`
		WHERE `+strings.Join(clauses, " AND ")+`
		ORDER BY `+selectedOrderClause(query)+`CASE WHEN release_date IS NULL OR trim(release_date) = '' THEN 1 ELSE 0 END ASC,
			release_date DESC, lower(model) ASC, provider_code ASC, id ASC`), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []modelOptionRow{}
	for rows.Next() {
		var row modelOptionRow
		var mode, releaseDate sql.NullString
		var protocols, tiers, efforts sql.NullString
		var defaultEffort sql.NullString
		if err := rows.Scan(&row.ProviderCode, &row.Model, &mode, &releaseDate, &protocols, &tiers, &efforts, &defaultEffort); err != nil {
			return nil, err
		}
		row.Scope = catalogScopeBuiltIn
		row.ReleaseDate = textPtr(releaseDate)
		row.SupportedAPIProtocols = parseJSONArray(protocols)
		row.SupportedServiceTiers = parseJSONArray(tiers)
		row.SupportedReasoningEfforts = parseJSONArray(efforts)
		row.DefaultReasoningEffort = textPtr(defaultEffort)
		items = append(items, row)
	}
	return items, rows.Err()
}

func (s *Store) listCustomModelOptions(ctx context.Context, providerCodes []string, query ModelOptionQuery) ([]modelOptionRow, error) {
	codes := normalizeProviderCodeList(providerCodes)
	if len(codes) == 0 {
		return []modelOptionRow{}, nil
	}
	clauses := []string{
		"provider_code IN (" + placeholders(len(codes)) + ")",
		"status = 'active'",
		"(shutdown_date IS NULL OR trim(shutdown_date) = '' OR shutdown_date > " + s.todayText() + ")",
	}
	args := append([]any{}, stringSliceToAny(codes)...)
	if trimmedAccount := strings.TrimSpace(query.SystemAccountID); trimmedAccount != "" {
		clauses = append(clauses, "((scope = 'global' AND system_account_id IS NULL) OR (scope = 'personal' AND system_account_id = ?))")
		args = append(args, trimmedAccount)
	} else {
		clauses = append(clauses, "scope = 'global' AND system_account_id IS NULL")
	}
	args = appendOptionTextFilter(&clauses, args, query)
	args = appendSelectedOrderArgs(args, query)
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT provider_code, model, scope, mode, release_date,
			supported_api_protocols_json, supported_service_tiers_json, supported_reasoning_efforts_json,
			default_reasoning_effort
		FROM `+s.table("custom_provider_models")+`
		WHERE `+strings.Join(clauses, " AND ")+`
		ORDER BY `+selectedOrderClause(query)+`CASE WHEN release_date IS NULL OR trim(release_date) = '' THEN 1 ELSE 0 END ASC,
			release_date DESC, lower(model) ASC, provider_code ASC, scope ASC, id ASC`), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []modelOptionRow{}
	for rows.Next() {
		var row modelOptionRow
		var mode, releaseDate sql.NullString
		var protocols, tiers, efforts sql.NullString
		var defaultEffort sql.NullString
		if err := rows.Scan(&row.ProviderCode, &row.Model, &row.Scope, &mode, &releaseDate, &protocols, &tiers, &efforts, &defaultEffort); err != nil {
			return nil, err
		}
		row.ReleaseDate = textPtr(releaseDate)
		row.SupportedAPIProtocols = parseJSONArray(protocols)
		row.SupportedServiceTiers = parseJSONArray(tiers)
		row.SupportedReasoningEfforts = parseJSONArray(efforts)
		row.DefaultReasoningEffort = textPtr(defaultEffort)
		items = append(items, row)
	}
	return items, rows.Err()
}

// appendOptionTextFilter mirrors the WHERE side of the options queries: the
// (selectedIds OR keyword LIKE) clause is pushed only when a keyword is
// present (provider-model-catalog.repository.ts:132-134,
// custom-provider-models.repository.ts:141-143). Without a keyword the
// selectedIds participate only in the ORDER BY preface and the final
// selected-aware visibility merge.
func appendOptionTextFilter(clauses *[]string, args []any, query ModelOptionQuery) []any {
	if query.Keyword == "" {
		return args
	}
	parts := []string{}
	if len(query.SelectedIDs) > 0 {
		parts = append(parts, "model IN ("+placeholders(len(query.SelectedIDs))+")")
		for _, id := range query.SelectedIDs {
			args = append(args, id)
		}
	}
	parts = append(parts, "lower(model) LIKE ?")
	args = append(args, "%"+strings.ToLower(query.Keyword)+"%")
	*clauses = append(*clauses, "("+strings.Join(parts, " OR ")+")")
	return args
}

// selectedOrderClause mirrors the ORDER BY preface that pins selected models
// first (the SQL-side guarantee that they survive any limit).
func selectedOrderClause(query ModelOptionQuery) string {
	if len(query.SelectedIDs) == 0 {
		return ""
	}
	return "CASE WHEN model IN (" + placeholders(len(query.SelectedIDs)) + ") THEN 0 ELSE 1 END, "
}

// appendSelectedOrderArgs appends the selectedIds parameters consumed by the
// ORDER BY preface after every WHERE parameter.
func appendSelectedOrderArgs(args []any, query ModelOptionQuery) []any {
	if len(query.SelectedIDs) == 0 {
		return args
	}
	for _, id := range query.SelectedIDs {
		args = append(args, id)
	}
	return args
}

// mergeProviderModelOptionRows ports mergeProviderModelOptionRows: keyword /
// selected filter, model dedupe with scope priority, release-date ordering
// and the selected-aware limit.
func mergeProviderModelOptionRows(rows []modelOptionRow, query ModelOptionQuery) []ModelSelectionOption {
	selectedIDs := map[string]bool{}
	for _, id := range query.SelectedIDs {
		selectedIDs[id] = true
	}
	keyword := ""
	if query.Keyword != "" {
		keyword = strings.ToLower(query.Keyword)
	}
	byModel := map[string]modelOptionRow{}
	order := []string{}
	for _, row := range rows {
		providerCode := strings.TrimSpace(row.ProviderCode)
		model := strings.TrimSpace(row.Model)
		if providerCode == "" || model == "" {
			continue
		}
		if keyword != "" && !strings.Contains(strings.ToLower(model), keyword) && !selectedIDs[model] {
			continue
		}
		row.ProviderCode = providerCode
		row.Model = model
		existing, ok := byModel[model]
		if !ok {
			order = append(order, model)
			byModel[model] = row
			continue
		}
		if optionScopePriority(row.Scope) > optionScopePriority(existing.Scope) {
			byModel[model] = row
		}
	}
	sort.SliceStable(order, func(left, right int) bool {
		leftRow, rightRow := byModel[order[left]], byModel[order[right]]
		leftDate, rightDate := normalizedModelReleaseDate(leftRow.ReleaseDate), normalizedModelReleaseDate(rightRow.ReleaseDate)
		if leftDate != "" && rightDate != "" && leftDate != rightDate {
			return leftDate > rightDate
		}
		if leftDate != "" && rightDate == "" {
			return true
		}
		if leftDate == "" && rightDate != "" {
			return false
		}
		if cmp := strings.Compare(leftRow.Model, rightRow.Model); cmp != 0 {
			return cmp < 0
		}
		return strings.Compare(leftRow.ProviderCode, rightRow.ProviderCode) < 0
	})
	visible := map[string]bool{}
	for id := range selectedIDs {
		visible[id] = true
	}
	admitted := 0
	for _, model := range order {
		if visible[model] || admitted >= query.Limit {
			continue
		}
		visible[model] = true
		admitted++
	}
	options := []ModelSelectionOption{}
	for _, model := range order {
		if !visible[model] {
			continue
		}
		row := byModel[model]
		options = append(options, ModelSelectionOption{
			ID:                        row.Model,
			Name:                      row.Model,
			SupportedAPIProtocols:     copyStringSlice(row.SupportedAPIProtocols),
			SupportedServiceTiers:     copyStringSlice(row.SupportedServiceTiers),
			SupportedReasoningEfforts: copyStringSlice(row.SupportedReasoningEfforts),
			DefaultReasoningEffort:    row.DefaultReasoningEffort,
		})
	}
	return options
}

func optionScopePriority(scope string) int {
	if scope == catalogScopePersonal {
		return 3
	}
	if scope == catalogScopeGlobal {
		return 2
	}
	return 1
}

// normalizedModelReleaseDate mirrors normalizedModelReleaseDate: only valid
// YYYY-MM-DD strings participate in the ordering.
func normalizedModelReleaseDate(value *string) string {
	if value == nil {
		return ""
	}
	normalized := (*value)[:minInt(len(*value), 10)]
	normalized = strings.TrimSpace(normalized)
	if len(normalized) != 10 || normalized[4] != '-' || normalized[7] != '-' {
		return ""
	}
	for index, char := range normalized {
		if index == 4 || index == 7 {
			continue
		}
		if char < '0' || char > '9' {
			return ""
		}
	}
	month := (int(normalized[5]-'0') * 10) + int(normalized[6]-'0')
	day := (int(normalized[8]-'0') * 10) + int(normalized[9]-'0')
	if month < 1 || month > 12 || day < 1 || day > 31 {
		return ""
	}
	return normalized
}

// FindProviderModelCapabilities ports findProviderModelTestCatalogItemAsync +
// findProviderModelCapabilitiesAsync: active, visible, unshutdown rows only,
// merged with scope priority and ordered by the test-catalog comparator.
func (s *Store) FindProviderModelCapabilities(ctx context.Context, providerCode, systemAccountID, model string) (*ModelCapabilities, error) {
	trimmedModel := strings.TrimSpace(model)
	if trimmedModel == "" {
		return nil, nil
	}
	sourceCodes, err := s.ModelCatalogSourceProviderCodes(ctx, providerCode)
	if err != nil {
		return nil, err
	}
	if len(sourceCodes) == 0 {
		return nil, nil
	}
	builtInCodes := modelCatalogBuiltInSourceProviderCodes(providerCode, sourceCodes)
	builtIn, err := s.findBuiltInTestCatalogItems(ctx, builtInCodes, trimmedModel)
	if err != nil {
		return nil, err
	}
	customItems, err := s.listCustomModelRows(ctx, sourceCodes, systemAccountID, false, trimmedModel)
	if err != nil {
		return nil, err
	}
	custom := make([]testCatalogItem, 0, len(customItems))
	for _, item := range customItems {
		custom = append(custom, testCatalogItemFromCatalogItem(item))
	}
	preserveProviderIdentity := normalizeProviderToken(providerCode) == hybridProviderCode
	winner := mergeTestCatalogItems(append(builtIn, custom...), preserveProviderIdentity)
	if winner == nil {
		return nil, nil
	}
	capability := &ModelCapabilities{
		ID:                        winner.Model,
		Name:                      winner.Model,
		SupportedAPIProtocols:     copyStringSlice(winner.SupportedAPIProtocols),
		SupportedServiceTiers:     copyStringSlice(winner.SupportedServiceTiers),
		SupportedReasoningEfforts: copyStringSlice(winner.SupportedReasoningEfforts),
		DefaultReasoningEffort:    winner.DefaultReasoningEffort,
	}
	return capability, nil
}

// testCatalogItem mirrors ProviderModelTestCatalogRecord /
// CustomProviderModelTestCatalogRecord.
type testCatalogItem struct {
	ID                        string
	ProviderCode              string
	Model                     string
	Scope                     string
	CatalogOrder              *int64
	ReleaseDate               *string
	SupportedAPIProtocols     []string
	SupportedServiceTiers     []string
	SupportedReasoningEfforts []string
	DefaultReasoningEffort    *string
}

func testCatalogItemFromCatalogItem(item ModelCatalogItem) testCatalogItem {
	return testCatalogItem{
		ID:                        item.ID,
		ProviderCode:              item.ProviderCode,
		Model:                     item.Model,
		Scope:                     item.Scope,
		ReleaseDate:               item.ReleaseDate,
		SupportedAPIProtocols:     item.SupportedAPIProtocols,
		SupportedServiceTiers:     item.SupportedServiceTiers,
		SupportedReasoningEfforts: item.SupportedReasoningEfforts,
		DefaultReasoningEffort:    item.DefaultReasoningEffort,
	}
}

// findBuiltInTestCatalogItems mirrors findBuiltInProviderModelTestCatalogAsync
// with the default 'test' projection.
func (s *Store) findBuiltInTestCatalogItems(ctx context.Context, providerCodes []string, model string) ([]testCatalogItem, error) {
	codes := normalizeProviderCodeList(providerCodes)
	if len(codes) == 0 {
		return []testCatalogItem{}, nil
	}
	args := append([]any{}, stringSliceToAny(codes)...)
	args = append(args, model)
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT id, provider_code, model, catalog_order, release_date,
			supported_api_protocols_json, supported_service_tiers_json, supported_reasoning_efforts_json,
			default_reasoning_effort
		FROM `+s.table("provider_model_catalog")+`
		WHERE provider_code IN (`+placeholders(len(codes))+`)
			AND model = ?
			AND status = 'active'
			AND catalog_visible = 1
			AND (shutdown_date IS NULL OR trim(shutdown_date) = '' OR shutdown_date > `+s.todayText()+`)
		ORDER BY provider_code ASC, catalog_order ASC, model ASC, id ASC`), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []testCatalogItem{}
	for rows.Next() {
		var item testCatalogItem
		var catalogOrder sql.NullInt64
		var releaseDate sql.NullString
		var protocols, tiers, efforts sql.NullString
		var defaultEffort sql.NullString
		if err := rows.Scan(&item.ID, &item.ProviderCode, &item.Model, &catalogOrder, &releaseDate,
			&protocols, &tiers, &efforts, &defaultEffort); err != nil {
			return nil, err
		}
		item.Scope = catalogScopeBuiltIn
		item.CatalogOrder = nullInt64Ptr(catalogOrder)
		item.ReleaseDate = textPtr(releaseDate)
		item.SupportedAPIProtocols = parseJSONArray(protocols)
		item.SupportedServiceTiers = parseJSONArray(tiers)
		item.SupportedReasoningEfforts = parseJSONArray(efforts)
		item.DefaultReasoningEffort = textPtr(defaultEffort)
		items = append(items, item)
	}
	return items, rows.Err()
}

// mergeTestCatalogItems ports mergeProviderModelTestCatalogItems +
// compareProviderModelTestCatalogItems, returning the first row.
func mergeTestCatalogItems(items []testCatalogItem, preserveProviderIdentity bool) *testCatalogItem {
	type key struct {
		provider string
		model    string
	}
	merged := map[key]testCatalogItem{}
	order := []key{}
	for _, item := range items {
		model := strings.TrimSpace(item.Model)
		if model == "" {
			continue
		}
		itemKey := key{model: model}
		if preserveProviderIdentity {
			itemKey.provider = normalizeProviderToken(item.ProviderCode)
		}
		existing, ok := merged[itemKey]
		if !ok || testCatalogPriority(item.Scope) >= testCatalogPriority(existing.Scope) {
			if !ok {
				order = append(order, itemKey)
			}
			merged[itemKey] = item
		}
	}
	if len(order) == 0 {
		return nil
	}
	sorted := make([]testCatalogItem, 0, len(order))
	for _, itemKey := range order {
		sorted = append(sorted, merged[itemKey])
	}
	sort.SliceStable(sorted, func(left, right int) bool {
		return compareProviderModelTestCatalogItems(sorted[left], sorted[right]) < 0
	})
	return &sorted[0]
}

func testCatalogPriority(scope string) int {
	if scope == catalogScopePersonal {
		return 3
	}
	if scope == catalogScopeGlobal {
		return 2
	}
	return 1
}

// compareProviderModelTestCatalogItems ports the test-catalog comparator:
// newer release dates sort first.
func compareProviderModelTestCatalogItems(left, right testCatalogItem) int {
	sameProvider := normalizeProviderToken(left.ProviderCode) == normalizeProviderToken(right.ProviderCode)
	leftDate := textValue(left.ReleaseDate)
	rightDate := textValue(right.ReleaseDate)
	if leftDate != "" && rightDate != "" && leftDate != rightDate {
		if leftDate > rightDate {
			return -1
		}
		return 1
	}
	if leftDate != "" && rightDate == "" {
		return -1
	}
	if leftDate == "" && rightDate != "" {
		return 1
	}
	if sameProvider && left.CatalogOrder != nil && right.CatalogOrder != nil && *left.CatalogOrder != *right.CatalogOrder {
		if *left.CatalogOrder < *right.CatalogOrder {
			return -1
		}
		return 1
	}
	if cmp := strings.Compare(left.Model, right.Model); cmp != 0 {
		return cmp
	}
	return strings.Compare(left.ID, right.ID)
}

func textValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// normalizeServiceTierPrices ports normalizeServiceTierPrices (read-side
// normalization of service_tier_prices_json).
func normalizeServiceTierPrices(value sql.NullString) map[string]ModelPriceSet {
	result := map[string]ModelPriceSet{}
	if !value.Valid || strings.TrimSpace(value.String) == "" {
		return result
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(value.String), &raw); err != nil {
		return result
	}
	for rawTier, rawPrices := range raw {
		tier := strings.TrimSpace(rawTier)
		if tier == "" || tier == "default" || tier == "standard" || len(tier) > 64 {
			continue
		}
		var prices map[string]*float64
		if err := json.Unmarshal(rawPrices, &prices); err != nil {
			continue
		}
		normalized := ModelPriceSet{}
		assignPrice(prices, "inputUsdPer1M", &normalized.InputUsdPer1M)
		assignPrice(prices, "outputUsdPer1M", &normalized.OutputUsdPer1M)
		assignPrice(prices, "cachedInputUsdPer1M", &normalized.CachedInputUsdPer1M)
		assignPrice(prices, "cacheWriteUsdPer1M", &normalized.CacheWriteUsdPer1M)
		assignPrice(prices, "cacheWrite1hUsdPer1M", &normalized.CacheWrite1hUsdPer1M)
		assignPrice(prices, "cacheStorageUsdPer1MPerHour", &normalized.CacheStorageUsdPer1MPerHour)
		assignPrice(prices, "imageInputUsdPer1M", &normalized.ImageInputUsdPer1M)
		assignPrice(prices, "imageOutputUsdPer1M", &normalized.ImageOutputUsdPer1M)
		assignPrice(prices, "audioInputUsdPer1M", &normalized.AudioInputUsdPer1M)
		assignPrice(prices, "audioOutputUsdPer1M", &normalized.AudioOutputUsdPer1M)
		assignPrice(prices, "outputUsdPerImage", &normalized.OutputUsdPerImage)
		if normalized.InputUsdPer1M != nil || normalized.OutputUsdPer1M != nil || normalized.CachedInputUsdPer1M != nil ||
			normalized.CacheWriteUsdPer1M != nil || normalized.CacheWrite1hUsdPer1M != nil ||
			normalized.CacheStorageUsdPer1MPerHour != nil || normalized.ImageInputUsdPer1M != nil ||
			normalized.ImageOutputUsdPer1M != nil || normalized.AudioInputUsdPer1M != nil ||
			normalized.AudioOutputUsdPer1M != nil || normalized.OutputUsdPerImage != nil {
			result[tier] = normalized
		}
	}
	return result
}

func assignPrice(prices map[string]*float64, key string, target **float64) {
	if price, ok := prices[key]; ok && price != nil && *price >= 0 {
		copied := *price
		*target = &copied
	}
}

// normalizeProviderToken mirrors domain/provider-protocol.ts
// normalizeProviderToken.
func normalizeProviderToken(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	return normalized
}

func isHybridProviderCode(value string) bool {
	return normalizeProviderToken(value) == hybridProviderCode
}

func dedupeStrings(values []string) []string {
	seen := map[string]bool{}
	output := []string{}
	for _, value := range values {
		if seen[value] {
			continue
		}
		seen[value] = true
		output = append(output, value)
	}
	return output
}

func copyStringSlice(values []string) []string {
	output := []string{}
	output = append(output, values...)
	return output
}

func stringSliceToAny(values []string) []any {
	output := make([]any, 0, len(values))
	for _, value := range values {
		output = append(output, value)
	}
	return output
}

func nullInt64Ptr(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	copied := value.Int64
	return &copied
}

func nullFloat64Ptr(value sql.NullFloat64) *float64 {
	if !value.Valid {
		return nil
	}
	copied := value.Float64
	return &copied
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
