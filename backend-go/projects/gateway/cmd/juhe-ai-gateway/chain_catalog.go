package main

// G20 phase-2 provider model catalog source (gatewayruntimecache.CatalogSource),
// carried over unchanged from the phase-2 chain_accounts.go split.
//
// 2026-09-20 修复：补回 Node listProviderModelCatalogAsync 的
// modelCatalogSourceProviderCodes 源扩展语义（model-catalog.service.ts
// buildProviderModelCatalogAsync）。openai 兼容供应商的目录是
// 「openai 协议子供应商 + 自己」的聚合，hybrid 是 openai/anthropic/gemini
// 三协议子供应商的聚合；此前 Go 迁移只按单码查询，导致 AI 对话与 /v1/models
// 在 openai/hybrid 分组下稳定返回空目录（管理面 internal/providers 已移植
// 同一扩展，两侧语义自此重新对齐）。

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
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

// ListProviderModelCatalog mirrors listProviderModelCatalogAsync over the Node
// source semantics: source-provider expansion (modelCatalogSourceProvider
// CodesAsync), built-in rows from provider_model_catalog for the expanded
// built-in codes (the openai-compatible target drops itself: it has no
// built-in catalog), custom rows from custom_provider_models for every source
// code (listCustomProviderModelsForCatalog), the model-key scope-priority
// merge, the isSupportedCatalogModel / active / priced filters and the
// release-date ordering. The historical account-scoped provider_model_catalog
// query 500'd on fresh databases — that table has no scope/system_account_id
// columns.
func (s *chainCatalogSource) ListProviderModelCatalog(ctx context.Context, input gatewayruntimecache.ModelCatalogListOptions) ([]gatewayruntimecache.ProviderModelCatalogItem, error) {
	sourceCodes, err := s.sourceProviderCodes(ctx, input.ProviderCode)
	if err != nil {
		return nil, err
	}
	if len(sourceCodes) == 0 {
		return []gatewayruntimecache.ProviderModelCatalogItem{}, nil
	}
	now := s.now().UTC().Format("2006-01-02")
	items := []gatewayruntimecache.ProviderModelCatalogItem{}

	builtInCodes := chainCatalogBuiltInSourceProviderCodes(input.ProviderCode, sourceCodes)
	if len(builtInCodes) > 0 {
		builtInQuery, builtInArgs := s.builtinCatalogQuery(input, now, builtInCodes)
		rows, err := s.db.QueryContext(ctx, s.bind(builtInQuery), builtInArgs...)
		if err != nil {
			return nil, err
		}
		scanned, err := scanCatalogRows(rows, chainBuiltinCatalogColumns, decorateBuiltinCatalogRow)
		if err != nil {
			return nil, err
		}
		items = append(items, scanned...)
	}

	customQuery, customArgs := s.customCatalogQuery(input, now, sourceCodes)
	rows, err := s.db.QueryContext(ctx, s.bind(customQuery), customArgs...)
	if err != nil {
		return nil, err
	}
	scanned, err := scanCatalogRows(rows, chainCustomCatalogColumns, decorateCustomCatalogRow)
	if err != nil {
		return nil, err
	}
	items = append(items, scanned...)

	preserveProviderIdentity := chainNormalizeProviderToken(input.ProviderCode) == chainCatalogHybridProviderCode
	merged := chainMergeCatalogItems(items, preserveProviderIdentity)
	out := make([]gatewayruntimecache.ProviderModelCatalogItem, 0, len(merged))
	for _, item := range merged {
		if !chainIsSupportedCatalogModel(item) {
			continue
		}
		if !input.IncludeInactive && item.Status != "active" {
			continue
		}
		if !input.IncludeUnpriced && !chainHasDirectCatalogPrice(item) {
			continue
		}
		out = append(out, item)
	}
	sort.SliceStable(out, func(left, right int) bool {
		return chainCompareCatalogItems(out[left], out[right]) < 0
	})
	return out, nil
}

// chainCatalogHybridProviderCode / chainCatalogOpenAICompatibleProviderCode
// mirror the providerProtocol.ts tokens; the protocol pairs mirror the
// modelCatalogSourceProviderCodesAsync expansion set.
const (
	chainCatalogHybridProviderCode           = "hybrid"
	chainCatalogOpenAICompatibleProviderCode = "openai"
	chainCatalogMaxProviderDefinitions       = 50
)

var chainCatalogProtocolPairs = [][2]string{
	{"openai", "v1"},
	{"anthropic", "v1"},
	{"gemini", "v1beta"},
}

// sourceProviderCodes mirrors modelCatalogSourceProviderCodesAsync: hybrid
// expands to every enabled openai/anthropic/gemini protocol provider, the
// openai-compatible provider expands to its openai-protocol children plus
// itself, anything else is its own (normalized) source.
func (s *chainCatalogSource) sourceProviderCodes(ctx context.Context, providerCode string) ([]string, error) {
	normalized := chainNormalizeProviderToken(providerCode)
	if normalized == "" {
		return []string{}, nil
	}
	if normalized == chainCatalogHybridProviderCode {
		codes := []string{}
		for _, pair := range chainCatalogProtocolPairs {
			list, err := s.protocolProviderCodes(ctx, pair[0], pair[1])
			if err != nil {
				return nil, err
			}
			for _, code := range list {
				token := chainNormalizeProviderToken(code)
				if token == "" || token == chainCatalogHybridProviderCode {
					continue
				}
				codes = append(codes, token)
			}
		}
		return chainDedupeCatalogCodes(codes), nil
	}
	if normalized != chainCatalogOpenAICompatibleProviderCode {
		return []string{normalized}, nil
	}
	list, err := s.protocolProviderCodes(ctx, chainCatalogProtocolPairs[0][0], chainCatalogProtocolPairs[0][1])
	if err != nil {
		return nil, err
	}
	codes := []string{}
	for _, code := range list {
		token := chainNormalizeProviderToken(code)
		if token == "" || token == normalized {
			continue
		}
		codes = append(codes, token)
	}
	return chainDedupeCatalogCodes(append(codes, normalized)), nil
}

// protocolProviderCodes mirrors listProtocolProviderCodesAsync: distinct
// provider codes carrying an enabled profile of the protocol, provider row
// enabled, ordered by code, bounded by the provider-definition ceiling.
func (s *chainCatalogSource) protocolProviderCodes(ctx context.Context, protocolCode, protocolVersion string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT p.code
		FROM `+s.table("provider_protocol_profiles")+` ppp
		INNER JOIN `+s.table("providers")+` p
			ON p.code = ppp.provider_code
		WHERE p.enabled = 1
			AND ppp.enabled = 1
			AND ppp.protocol_code = ?
			AND ppp.protocol_version = ?
		ORDER BY p.code ASC
		LIMIT ?`), protocolCode, protocolVersion, chainCatalogMaxProviderDefinitions)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	codes := []string{}
	for rows.Next() {
		var code string
		if err := rows.Scan(&code); err != nil {
			return nil, err
		}
		codes = append(codes, code)
	}
	return codes, rows.Err()
}

// chainCatalogBuiltInSourceProviderCodes mirrors the same-named helper: only
// the openai-compatible target drops itself from its built-in sources (it has
// no built-in catalog of its own).
func chainCatalogBuiltInSourceProviderCodes(providerCode string, sourceProviderCodes []string) []string {
	if chainNormalizeProviderToken(providerCode) != chainCatalogOpenAICompatibleProviderCode {
		return sourceProviderCodes
	}
	codes := []string{}
	for _, code := range sourceProviderCodes {
		if chainNormalizeProviderToken(code) == chainCatalogOpenAICompatibleProviderCode {
			continue
		}
		codes = append(codes, code)
	}
	return codes
}

// builtinCatalogQuery mirrors listBuiltInProviderModels: availability filter
// plus the provider_code window, ordered like the Node read.
func (s *chainCatalogSource) builtinCatalogQuery(input gatewayruntimecache.ModelCatalogListOptions, now string, codes []string) (string, []any) {
	availability := ""
	if !input.IncludeInactive {
		availability = " AND status = 'active' AND CAST(catalog_visible AS integer) = 1 AND (shutdown_date IS NULL OR trim(shutdown_date) = '' OR shutdown_date > ?) "
	}
	base := fmt.Sprintf(`SELECT %s FROM %s`, catalogColumnList(chainBuiltinCatalogColumns), s.table("provider_model_catalog"))
	query := base + " WHERE provider_code IN (" + chainCatalogPlaceholders(len(codes)) + ") " + availability + " ORDER BY provider_code, catalog_order, model, id"
	args := chainCatalogCodeArgs(codes)
	if availability != "" {
		args = append(args, now)
	}
	return query, args
}

// customCatalogQuery mirrors listCustomProviderModelsForCatalog: the
// global/personal scope window over custom_provider_models across the source
// codes. The model ordering is dual-dialect like the Node read
// (custom-provider-models.repository.ts): SQLite orders by
// `model COLLATE NOCASE`, PostgreSQL has no such collation and orders by
// `lower(model)`.
func (s *chainCatalogSource) customCatalogQuery(input gatewayruntimecache.ModelCatalogListOptions, now string, codes []string) (string, []any) {
	clauses := []string{"provider_code IN (" + chainCatalogPlaceholders(len(codes)) + ")"}
	args := chainCatalogCodeArgs(codes)
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
	query := fmt.Sprintf(`SELECT %s FROM %s WHERE %s ORDER BY provider_code ASC, scope ASC, %s ASC, id ASC`,
		catalogColumnList(chainCustomCatalogColumns), s.table("custom_provider_models"), strings.Join(clauses, " AND "), s.modelOrderExpression())
	return query, args
}

func chainCatalogPlaceholders(count int) string {
	if count <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?,", count), ",")
}

func chainCatalogCodeArgs(codes []string) []any {
	args := make([]any, 0, len(codes))
	for _, code := range codes {
		args = append(args, code)
	}
	return args
}

func chainDedupeCatalogCodes(values []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, value := range values {
		if seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

// chainMergeCatalogItems mirrors mergeModelCatalogItems: dedupe by model (or
// provider+model for the hybrid identity), higher scope priority wins and
// later rows win ties.
func chainMergeCatalogItems(items []gatewayruntimecache.ProviderModelCatalogItem, preserveProviderIdentity bool) []gatewayruntimecache.ProviderModelCatalogItem {
	type key struct {
		provider string
		model    string
	}
	merged := map[key]gatewayruntimecache.ProviderModelCatalogItem{}
	order := []key{}
	for _, item := range items {
		model := strings.TrimSpace(item.Model)
		if model == "" {
			continue
		}
		itemKey := key{model: model}
		if preserveProviderIdentity {
			itemKey.provider = chainNormalizeProviderToken(item.ProviderCode)
		}
		existing, ok := merged[itemKey]
		if !ok || chainCatalogScopePriority(item.Scope) >= chainCatalogScopePriority(existing.Scope) {
			if !ok {
				order = append(order, itemKey)
			}
			merged[itemKey] = item
		}
	}
	output := make([]gatewayruntimecache.ProviderModelCatalogItem, 0, len(order))
	for _, itemKey := range order {
		output = append(output, merged[itemKey])
	}
	return output
}

func chainCatalogScopePriority(scope string) int {
	switch scope {
	case "personal":
		return 3
	case "global":
		return 2
	default:
		return 1
	}
}

// chainIsSupportedCatalogModel mirrors isSupportedCatalogModel.
func chainIsSupportedCatalogModel(item gatewayruntimecache.ProviderModelCatalogItem) bool {
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
		if chainCatalogModelMatchesToken(model, token) {
			return false
		}
	}
	return true
}

// chainCatalogModelMatchesToken mirrors /(?:^|[-_.])(token)(?:$|[-_.])/.
func chainCatalogModelMatchesToken(model, token string) bool {
	position := 0
	for position <= len(model) {
		found := strings.Index(model[position:], token)
		if found < 0 {
			return false
		}
		start := position + found
		end := start + len(token)
		if !chainCatalogTokenBoundary(model, start) {
			position = start + 1
			continue
		}
		if !chainCatalogTokenBoundaryEnd(model, end) {
			position = start + 1
			continue
		}
		return true
	}
	return false
}

func chainCatalogTokenBoundary(model string, index int) bool {
	if index == 0 {
		return true
	}
	switch model[index-1] {
	case '-', '_', '.':
		return true
	}
	return false
}

func chainCatalogTokenBoundaryEnd(model string, index int) bool {
	if index == len(model) {
		return true
	}
	switch model[index] {
	case '-', '_', '.':
		return true
	}
	return false
}

// chainHasDirectCatalogPrice mirrors hasDirectPrice.
func chainHasDirectCatalogPrice(item gatewayruntimecache.ProviderModelCatalogItem) bool {
	if item.InputUsdPer1M != nil || item.OutputUsdPer1M != nil || item.CachedInputUsdPer1M != nil ||
		item.CacheWriteUsdPer1M != nil || item.CacheWrite1hUsdPer1M != nil ||
		item.CacheStorageUsdPer1MPerHour != nil || item.ImageInputUsdPer1M != nil ||
		item.ImageOutputUsdPer1M != nil || item.AudioInputUsdPer1M != nil ||
		item.AudioOutputUsdPer1M != nil || item.OutputUsdPerImage != nil {
		return true
	}
	return chainCatalogTierPriceCount(item.ServiceTierPrices) > 0
}

func chainCatalogTierPriceCount(raw json.RawMessage) int {
	trimmed := strings.TrimSpace(string(raw))
	if len(trimmed) < 2 || trimmed == "null" {
		return 0
	}
	var prices map[string]json.RawMessage
	if err := json.Unmarshal(raw, &prices); err != nil {
		return 0
	}
	return len(prices)
}

// chainCompareCatalogItems mirrors compareProviderModelCatalogItems: newer
// release dates sort first, then catalog_order, model and id.
func chainCompareCatalogItems(left, right gatewayruntimecache.ProviderModelCatalogItem) int {
	leftRelease := chainCatalogSortableReleaseDate(left.ReleaseDate)
	rightRelease := chainCatalogSortableReleaseDate(right.ReleaseDate)
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
	if order := chainCompareCatalogModels(left.Model, right.Model); order != 0 {
		return order
	}
	return strings.Compare(chainCatalogItemID(left), chainCatalogItemID(right))
}

// chainCompareCatalogModels approximates the Node model.localeCompare
// ordering (case-insensitive first, raw compare breaking ties).
func chainCompareCatalogModels(left, right string) int {
	lowerOrder := strings.Compare(strings.ToLower(left), strings.ToLower(right))
	if lowerOrder != 0 {
		return lowerOrder
	}
	return strings.Compare(left, right)
}

func chainCatalogSortableReleaseDate(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

func chainCatalogItemID(item gatewayruntimecache.ProviderModelCatalogItem) string {
	if item.ID == nil {
		return ""
	}
	return *item.ID
}

// modelOrderExpression mirrors the Node dual-dialect custom-provider model
// ordering (custom-provider-models.repository.ts listCustomProviderModelsForCatalog
// vs its postgres arm): `model COLLATE NOCASE` on SQLite, `lower(model)` on
// PostgreSQL.
func (s *chainCatalogSource) modelOrderExpression() string {
	if s.postgres {
		return "lower(model)"
	}
	return "model COLLATE NOCASE"
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
