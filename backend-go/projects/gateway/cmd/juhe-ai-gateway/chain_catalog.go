package main

// G20 phase-2 provider model catalog source (gatewayruntimecache.CatalogSource),
// carried over unchanged from the phase-2 chain_accounts.go split.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// ---------------------------------------------------------------------------
// provider model catalog source (gatewayruntimecache.CatalogSource)
// ---------------------------------------------------------------------------

// chainCatalogSource implements gatewayruntimecache.CatalogSource over the
// provider_model_catalog table (Node provider-model-catalog.repository.ts
// listBuiltInProviderModels + listProviderAccountModels). The row mapping
// round-trips the Node camelCase projection through the shared item JSON
// shape so the cache and downstream consumers see identical payloads.
type chainCatalogSource struct {
	db       *sql.DB
	postgres bool
}

func newChainCatalogSource(db *sql.DB, postgres bool) (*chainCatalogSource, error) {
	if db == nil {
		return nil, fmt.Errorf("网关链模型目录源需要业务数据库")
	}
	return &chainCatalogSource{db: db, postgres: postgres}, nil
}

// chainCatalogColumns pairs the table column with the item JSON key the Node
// fromRow projection emits. Column sets mirror the CURRENT Node sources byte
// for byte: provider_model_catalog carries the built-in catalog
// (provider-model-catalog.repository.ts columns()) while per-account rows live
// in custom_provider_models (custom-provider-models.repository.ts
// customProviderModelColumns()) — the drifted columns here (scope /
// *_json-less capability names on provider_model_catalog) 500'd every
// catalog read on fresh databases.
var chainBuiltinCatalogColumns = [][2]string{
	{"id", "id"},
	{"provider_code", "providerCode"},
	{"model", "model"},
	{"status", "status"},
	{"mode", "mode"},
	{"catalog_order", "catalogOrder"},
	{"release_date", "releaseDate"},
	{"shutdown_date", "shutdownDate"},
	{"supported_api_protocols_json", "supportedApiProtocols"},
	{"supported_service_tiers_json", "supportedServiceTiers"},
	{"supported_reasoning_efforts_json", "supportedReasoningEfforts"},
	{"default_reasoning_effort", "defaultReasoningEffort"},
	{"codex_supported_reasoning_levels_json", "codexSupportedReasoningLevels"},
	{"codex_default_reasoning_level", "codexDefaultReasoningLevel"},
	{"codex_multi_agent_version", "codexMultiAgentVersion"},
	{"context_window_tokens", "contextWindowTokens"},
	{"max_input_tokens", "maxInputTokens"},
	{"max_output_tokens", "maxOutputTokens"},
	{"max_tokens", "maxTokens"},
	{"input_usd_per_1m", "inputUsdPer1M"},
	{"output_usd_per_1m", "outputUsdPer1M"},
	{"cached_input_usd_per_1m", "cachedInputUsdPer1M"},
	{"cache_write_usd_per_1m", "cacheWriteUsdPer1M"},
	{"cache_write_1h_usd_per_1m", "cacheWrite1hUsdPer1M"},
	{"cache_storage_usd_per_1m_per_hour", "cacheStorageUsdPer1MPerHour"},
	{"service_tier_prices_json", "serviceTierPrices"},
	{"long_context_input_token_threshold", "longContextInputTokenThreshold"},
	{"long_context_input_token_threshold_inclusive", "longContextInputTokenThresholdInclusive"},
	{"long_context_input_cost_multiplier", "longContextInputCostMultiplier"},
	{"long_context_output_cost_multiplier", "longContextOutputCostMultiplier"},
	{"image_input_usd_per_1m", "imageInputUsdPer1M"},
	{"image_output_usd_per_1m", "imageOutputUsdPer1M"},
	{"audio_input_usd_per_1m", "audioInputUsdPer1M"},
	{"audio_output_usd_per_1m", "audioOutputUsdPer1M"},
	{"output_usd_per_image", "outputUsdPerImage"},
	{"supports_prompt_caching", "supportsPromptCaching"},
	{"catalog_visible", "catalogVisible"},
	{"source", "source"},
	{"created_at", "createdAt"},
	{"updated_at", "updatedAt"},
}

// chainCustomCatalogColumns mirrors customProviderModelColumns().
var chainCustomCatalogColumns = [][2]string{
	{"id", "id"},
	{"provider_code", "providerCode"},
	{"model", "model"},
	{"scope", "scope"},
	{"system_account_id", "systemAccountId"},
	{"status", "status"},
	{"catalog_visible", "catalogVisible"},
	{"mode", "mode"},
	{"supported_api_protocols_json", "supportedApiProtocols"},
	{"supported_service_tiers_json", "supportedServiceTiers"},
	{"supported_reasoning_efforts_json", "supportedReasoningEfforts"},
	{"default_reasoning_effort", "defaultReasoningEffort"},
	{"release_date", "releaseDate"},
	{"shutdown_date", "shutdownDate"},
	{"context_window_tokens", "contextWindowTokens"},
	{"max_input_tokens", "maxInputTokens"},
	{"max_output_tokens", "maxOutputTokens"},
	{"input_usd_per_1m", "inputUsdPer1M"},
	{"output_usd_per_1m", "outputUsdPer1M"},
	{"cached_input_usd_per_1m", "cachedInputUsdPer1M"},
	{"cache_write_usd_per_1m", "cacheWriteUsdPer1M"},
	{"cache_write_1h_usd_per_1m", "cacheWrite1hUsdPer1M"},
	{"cache_storage_usd_per_1m_per_hour", "cacheStorageUsdPer1MPerHour"},
	{"service_tier_prices_json", "serviceTierPrices"},
	{"image_input_usd_per_1m", "imageInputUsdPer1M"},
	{"image_output_usd_per_1m", "imageOutputUsdPer1M"},
	{"audio_input_usd_per_1m", "audioInputUsdPer1M"},
	{"audio_output_usd_per_1m", "audioOutputUsdPer1M"},
	{"output_usd_per_image", "outputUsdPerImage"},
	{"pricing_notes", "pricingNotes"},
	{"capability_notes", "capabilityNotes"},
	{"notes", "notes"},
	{"created_at", "createdAt"},
	{"updated_at", "updatedAt"},
}

// ListProviderModelCatalog mirrors listProviderModelCatalog over the current
// Node sources: built-in rows from provider_model_catalog (columns() of
// provider-model-catalog.repository.ts) plus the system-account scoped custom
// rows from custom_provider_models (listCustomProviderModelsForCatalog). The
// historical account-scoped provider_model_catalog query 500'd on fresh
// databases — that table has no scope/system_account_id columns.
func (s *chainCatalogSource) ListProviderModelCatalog(ctx context.Context, input gatewayruntimecache.ModelCatalogListOptions) ([]gatewayruntimecache.ProviderModelCatalogItem, error) {
	now := s.now().UTC().Format("2006-01-02")
	items := []gatewayruntimecache.ProviderModelCatalogItem{}

	builtInQuery, builtInArgs := s.builtinCatalogQuery(input, now)
	rows, err := s.db.QueryContext(ctx, s.bind(builtInQuery), builtInArgs...)
	if err != nil {
		return nil, err
	}
	scanned, err := scanCatalogRows(rows, chainBuiltinCatalogColumns, decorateBuiltinCatalogRow)
	if err != nil {
		return nil, err
	}
	items = append(items, scanned...)

	customQuery, customArgs := s.customCatalogQuery(input, now)
	rows, err = s.db.QueryContext(ctx, s.bind(customQuery), customArgs...)
	if err != nil {
		return nil, err
	}
	scanned, err = scanCatalogRows(rows, chainCustomCatalogColumns, decorateCustomCatalogRow)
	if err != nil {
		return nil, err
	}
	items = append(items, scanned...)
	return items, nil
}

// builtinCatalogQuery mirrors listBuiltInProviderModels: availability filter
// plus the provider_code window, ordered like the Node read.
func (s *chainCatalogSource) builtinCatalogQuery(input gatewayruntimecache.ModelCatalogListOptions, now string) (string, []any) {
	availability := ""
	if !input.IncludeInactive {
		availability = " AND status = 'active' AND catalog_visible = 1 AND (shutdown_date IS NULL OR trim(shutdown_date) = '' OR shutdown_date > ?) "
	}
	base := fmt.Sprintf(`SELECT %s FROM %s`, catalogColumnList(chainBuiltinCatalogColumns), s.table("provider_model_catalog"))
	query := base + " WHERE provider_code = ? " + availability + " ORDER BY catalog_order, model, id"
	args := []any{input.ProviderCode}
	if availability != "" {
		args = append(args, now)
	}
	return query, args
}

// customCatalogQuery mirrors listCustomProviderModelsForCatalog: the
// global/personal scope window over custom_provider_models.
func (s *chainCatalogSource) customCatalogQuery(input gatewayruntimecache.ModelCatalogListOptions, now string) (string, []any) {
	clauses := []string{"provider_code = ?"}
	args := []any{input.ProviderCode}
	if !input.IncludeInactive {
		clauses = append(clauses, "status = 'active'", "(shutdown_date IS NULL OR trim(shutdown_date) = '' OR shutdown_date > ?)")
		args = append(args, now)
	}
	if systemAccountID := strings.TrimSpace(input.SystemAccountID); systemAccountID != "" {
		clauses = append(clauses, "((scope = 'global' AND system_account_id IS NULL) OR (scope = 'personal' AND system_account_id = ?))")
		args = append(args, systemAccountID)
	} else {
		clauses = append(clauses, "scope = 'global' AND system_account_id IS NULL")
	}
	query := fmt.Sprintf(`SELECT %s FROM %s WHERE %s ORDER BY scope ASC, model COLLATE NOCASE ASC, id ASC`,
		catalogColumnList(chainCustomCatalogColumns), s.table("custom_provider_models"), strings.Join(clauses, " AND "))
	return query, args
}

func catalogColumnList(columns [][2]string) string {
	names := make([]string, 0, len(columns))
	for _, pair := range columns {
		names = append(names, pair[0])
	}
	return strings.Join(names, ", ")
}

// decorateBuiltinCatalogRow applies the toBuiltInCatalogItem derivations the
// SQL read cannot express: scope='built_in' and
// supportsServiceTier=len(supportedServiceTiers)>0.
func decorateBuiltinCatalogRow(row map[string]any) {
	row["scope"] = "built_in"
	row["supportsServiceTier"] = catalogStringListLength(row["supportedServiceTiers"]) > 0
}

// decorateCustomCatalogRow applies the toCustomCatalogItem derivations:
// source custom-global/custom-personal, supportsPromptCaching from a present
// cachedInputUsdPer1M and supportsServiceTier from the tier list.
func decorateCustomCatalogRow(row map[string]any) {
	if scope, _ := row["scope"].(string); scope == "global" {
		row["source"] = "custom-global"
	} else {
		row["source"] = "custom-personal"
	}
	_, hasCachedInput := row["cachedInputUsdPer1M"]
	row["supportsPromptCaching"] = hasCachedInput
	row["supportsServiceTier"] = catalogStringListLength(row["supportedServiceTiers"]) > 0
}

func catalogStringListLength(value any) int {
	if list, ok := value.([]any); ok {
		return len(list)
	}
	return 0
}

// scanCatalogRows converts one result set into the shared catalog items: each
// row scans into a generic slice keyed by the camelCase projection, applies
// the Node item derivations, and then decodes through the item JSON shape
// (the Go item tags mirror the Node row payload byte for byte).
func scanCatalogRows(rows *sql.Rows, columns [][2]string, decorate func(map[string]any)) ([]gatewayruntimecache.ProviderModelCatalogItem, error) {
	defer rows.Close()
	items := []gatewayruntimecache.ProviderModelCatalogItem{}
	for rows.Next() {
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			return nil, err
		}
		row := map[string]any{}
		for index, pair := range columns {
			value := normalizeCatalogValue(values[index])
			if catalogBoolColumns[pair[1]] {
				value = catalogBoolValue(value)
			}
			row[pair[1]] = value
		}
		if decorate != nil {
			decorate(row)
		}
		encoded, err := json.Marshal(row)
		if err != nil {
			return nil, err
		}
		var item gatewayruntimecache.ProviderModelCatalogItem
		if err := json.Unmarshal(encoded, &item); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// catalogBoolColumns are the boolean table columns the SQLite driver surfaces
// as integers (0/1) and the item JSON shape expects as bools.
var catalogBoolColumns = map[string]bool{
	"supportsPromptCaching":                   true,
	"supportsServiceTier":                     true,
	"catalogVisible":                          true,
	"longContextInputTokenThresholdInclusive": true,
}

// catalogBoolValue renders 0/1 integers (SQLite ints decode as float64) into
// JSON bools; NULL stays absent.
func catalogBoolValue(value any) any {
	switch typed := value.(type) {
	case float64:
		return typed != 0
	case int64:
		return typed != 0
	case int:
		return typed != 0
	case nil:
		return false
	default:
		return value
	}
}

// normalizeCatalogValue renders SQL values into the JSON shapes the item
// decode expects (JSON text columns decode through a second pass; numerics
// arrive as float64 from SQLite; booleans arrive as int).
func normalizeCatalogValue(value any) any {
	switch typed := value.(type) {
	case []byte:
		text := string(typed)
		var decoded any
		if len(text) > 0 && (text[0] == '[' || text[0] == '{') {
			if err := json.Unmarshal([]byte(text), &decoded); err == nil {
				return decoded
			}
		}
		return text
	case string:
		var decoded any
		if len(typed) > 0 && (typed[0] == '[' || typed[0] == '{') {
			if err := json.Unmarshal([]byte(typed), &decoded); err == nil {
				return decoded
			}
		}
		return typed
	default:
		return typed
	}
}

func (s *chainCatalogSource) table(name string) string {
	if s.postgres {
		return "juhe_business." + name
	}
	return name
}

func (s *chainCatalogSource) bind(query string) string {
	if !s.postgres {
		return query
	}
	var out strings.Builder
	index := 1
	for i := 0; i < len(query); i++ {
		if query[i] == '?' {
			out.WriteString("$" + fmt.Sprint(index))
			index++
		} else {
			out.WriteByte(query[i])
		}
	}
	return out.String()
}

func (s *chainCatalogSource) now() time.Time { return time.Now() }
