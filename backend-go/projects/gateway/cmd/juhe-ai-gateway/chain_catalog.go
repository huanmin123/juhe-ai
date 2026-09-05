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
// fromRow projection emits.
var chainCatalogColumns = [][2]string{
	{"id", "id"},
	{"scope", "scope"},
	{"status", "status"},
	{"provider_code", "providerCode"},
	{"model", "model"},
	{"mode", "mode"},
	{"catalog_order", "catalogOrder"},
	{"release_date", "releaseDate"},
	{"shutdown_date", "shutdownDate"},
	{"supported_api_protocols", "supportedApiProtocols"},
	{"input_modalities", "inputModalities"},
	{"output_modalities", "outputModalities"},
	{"supported_tools", "supportedTools"},
	{"input_usd_per_1m", "inputUsdPer1M"},
	{"output_usd_per_1m", "outputUsdPer1M"},
	{"cached_input_usd_per_1m", "cachedInputUsdPer1M"},
	{"cache_write_usd_per_1m", "cacheWriteUsdPer1M"},
	{"cache_write_1h_usd_per_1m", "cacheWrite1hUsdPer1M"},
	{"cache_storage_usd_per_1m_per_hour", "cacheStorageUsdPer1MPerHour"},
	{"service_tier_prices", "serviceTierPrices"},
	{"image_input_usd_per_1m", "imageInputUsdPer1M"},
	{"image_output_usd_per_1m", "imageOutputUsdPer1M"},
	{"output_usd_per_image", "outputUsdPerImage"},
	{"context_window_tokens", "contextWindowTokens"},
	{"max_input_tokens", "maxInputTokens"},
	{"max_output_tokens", "maxOutputTokens"},
	{"long_context_input_token_threshold", "longContextInputTokenThreshold"},
	{"long_context_input_token_threshold_inclusive", "longContextInputTokenThresholdInclusive"},
	{"long_context_input_cost_multiplier", "longContextInputCostMultiplier"},
	{"long_context_output_cost_multiplier", "longContextOutputCostMultiplier"},
	{"supports_prompt_caching", "supportsPromptCaching"},
	{"supported_service_tiers", "supportedServiceTiers"},
	{"supported_reasoning_efforts", "supportedReasoningEfforts"},
	{"default_reasoning_effort", "defaultReasoningEffort"},
	{"supports_service_tier", "supportsServiceTier"},
	{"catalog_visible", "catalogVisible"},
	{"source", "source"},
	{"system_account_id", "systemAccountId"},
	{"notes", "notes"},
}

// ListProviderModelCatalog mirrors listProviderModelCatalog: built-in catalog
// rows for the provider plus the account-scoped rows for the system account.
func (s *chainCatalogSource) ListProviderModelCatalog(ctx context.Context, input gatewayruntimecache.ModelCatalogListOptions) ([]gatewayruntimecache.ProviderModelCatalogItem, error) {
	availability := ""
	if !input.IncludeInactive {
		availability = " AND status = 'active' AND catalog_visible = 1 AND (shutdown_date IS NULL OR trim(shutdown_date) = '' OR shutdown_date > ?) "
	}
	columns := make([]string, 0, len(chainCatalogColumns))
	for _, pair := range chainCatalogColumns {
		columns = append(columns, pair[0])
	}
	base := fmt.Sprintf(`SELECT %s FROM %s`, strings.Join(columns, ", "), s.table("provider_model_catalog"))
	now := s.now().UTC().Format("2006-01-02")
	builtInQuery := base + " WHERE provider_code = ? " + availability + " ORDER BY catalog_order, model, id"
	builtInArgs := []any{input.ProviderCode}
	if availability != "" {
		builtInArgs = append(builtInArgs, now)
	}
	items := []gatewayruntimecache.ProviderModelCatalogItem{}
	rows, err := s.db.QueryContext(ctx, s.bind(builtInQuery), builtInArgs...)
	if err != nil {
		return nil, err
	}
	scanned, err := scanCatalogRows(rows, len(chainCatalogColumns))
	if err != nil {
		return nil, err
	}
	items = append(items, scanned...)
	accountQuery := base + " WHERE provider_code = ? AND system_account_id = ? " + availability + " ORDER BY catalog_order, model, id"
	accountArgs := []any{input.ProviderCode, input.SystemAccountID}
	if availability != "" {
		accountArgs = append(accountArgs, now)
	}
	rows, err = s.db.QueryContext(ctx, s.bind(accountQuery), accountArgs...)
	if err != nil {
		return nil, err
	}
	scanned, err = scanCatalogRows(rows, len(chainCatalogColumns))
	if err != nil {
		return nil, err
	}
	items = append(items, scanned...)
	return items, nil
}

// catalogBoolColumns are the boolean table columns the SQLite driver surfaces
// as integers (0/1) and the item JSON shape expects as bools.
var catalogBoolColumns = map[string]bool{
	"supportsPromptCaching":                   true,
	"supportsServiceTier":                     true,
	"catalogVisible":                          true,
	"longContextInputTokenThresholdInclusive": true,
}

// scanCatalogRows converts one result set into the shared catalog items: each
// row scans into a generic slice keyed by the camelCase projection and then
// decodes through the item JSON shape (the Go item tags mirror the Node row
// payload byte for byte).
func scanCatalogRows(rows *sql.Rows, columnCount int) ([]gatewayruntimecache.ProviderModelCatalogItem, error) {
	defer rows.Close()
	items := []gatewayruntimecache.ProviderModelCatalogItem{}
	for rows.Next() {
		values := make([]any, columnCount)
		pointers := make([]any, columnCount)
		for i := range values {
			pointers[i] = &values[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			return nil, err
		}
		row := map[string]any{}
		for index, pair := range chainCatalogColumns {
			value := normalizeCatalogValue(values[index])
			if catalogBoolColumns[pair[1]] {
				value = catalogBoolValue(value)
			}
			row[pair[1]] = value
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
