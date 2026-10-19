// catalog.go owns the provider model catalog read family over the models
// 单表（供应商与上游协议面重构设计 §3；原 provider_model_catalog 与
// custom_provider_models 合并为唯一物理表）：scope='builtin' 行承内置目录
// （管理面"内置目录"= vendors 维度下的 builtin 行，只读），global/personal 行
// 承自定义目录（owner 可见性按 system_account_id）。旧 `mode` 与
// `supported_api_protocols_json` 无物理列，目录 DTO 的 mode/supportedApiProtocols
// 键由 supported_endpoint_modes_json 经 DeriveLegacyModeAndProtocols 逆映射
// 派生（对前端保留键名，见 endpoint_modes.go）。Fields sourced from Node's
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
// ProviderCode/Model/Mode/SupportedAPIProtocols 四键沿用旧目录行形状：
// ProviderCode=COALESCE(vendor_code,”)（9 值域；vendor_code NULL 的手工模型
// 展示口径 "custom"，对齐设计 §2.1「UI 显示自定义协议连接」）、Model=
// public_model、Mode 与 SupportedAPIProtocols 由 supported_endpoint_modes_json
// 派生。
type ModelCatalogItem struct {
	ID                    string   `json:"id"`
	ProviderCode          string   `json:"providerCode"`
	Model                 string   `json:"model"`
	Scope                 string   `json:"scope"`
	Status                string   `json:"status"`
	CatalogVisible        *bool    `json:"catalogVisible,omitempty"`
	SystemAccountID       *string  `json:"systemAccountId,omitempty"`
	Mode                  *string  `json:"mode,omitempty"`
	CatalogOrder          *int64   `json:"catalogOrder,omitempty"`
	ReleaseDate           *string  `json:"releaseDate,omitempty"`
	ShutdownDate          *string  `json:"shutdownDate,omitempty"`
	SupportedAPIProtocols []string `json:"supportedApiProtocols"`
	InputModalities       []string `json:"inputModalities"`
	OutputModalities      []string `json:"outputModalities"`
	// SupportedToolsByProtocol 是「协议 × 工具」矩阵（AI问答工具体系与主子模型
	// 设计 6.4）：键为该行 supportedApiProtocols 的现有枚举值，值为该协议下
	// 可用的工具集。一维 SupportedTools 已随阶段 2 全链退场。
	SupportedToolsByProtocol                map[string][]string      `json:"supportedToolsByProtocol"`
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
	CachedImageInputUsdPer1M                *float64                 `json:"cachedImageInputUsdPer1M,omitempty"`
	SourcePricingCurrency                   string                   `json:"sourcePricingCurrency,omitempty"`
	SourceExchangeRateToUsd                 *float64                 `json:"sourceExchangeRateToUsd,omitempty"`
	SourceExchangeRateDate                  string                   `json:"sourceExchangeRateDate,omitempty"`
	SourcePricingNote                       string                   `json:"sourcePricingNote,omitempty"`
	// 媒体单价四维（静态定价表透传；目录表无列，仅供目录展示与前端渲染）。
	VideoOutputUsdPerSecond *float64 `json:"videoOutputUsdPerSecond,omitempty"`
	VideoOutputUsdPerCall   *float64 `json:"videoOutputUsdPerCall,omitempty"`
	TtsInputUsdPer1MChars   *float64 `json:"ttsInputUsdPer1MChars,omitempty"`
	AudioInputUsdPerSecond  *float64 `json:"audioInputUsdPerSecond,omitempty"`
	// CatalogDisplay mirrors withCatalogDisplay: present on every merged
	// catalog row (possibly []), absent on the save/mutation responses
	// (Node toCustomCatalogItem carries no catalogDisplay).
	CatalogDisplay *[]catalogDisplaySection `json:"catalogDisplay,omitempty"`
}

// ModelSelectionOption mirrors ProviderModelSelectionOption. Mode carries the
// winning catalog row's derived directory mode (逆映射自
// supported_endpoint_modes_json；空 mode 集渲染为空) so callers can apply a
// client-side second gate.
type ModelSelectionOption struct {
	ID                        string   `json:"id"`
	Name                      string   `json:"name"`
	Mode                      string   `json:"mode,omitempty"`
	SupportedAPIProtocols     []string `json:"supportedApiProtocols"`
	SupportedServiceTiers     []string `json:"supportedServiceTiers"`
	SupportedReasoningEfforts []string `json:"supportedReasoningEfforts"`
	DefaultReasoningEffort    *string  `json:"defaultReasoningEffort,omitempty"`
}

// ProviderCatalogModeValues mirrors the six-value directory mode word list of
// the /models/options `modes` query（旧 provider_model_catalog.mode 值域）。
// models 单表无 mode 列：过滤时经 catalogModeEndpointModeExpansion 展开成
// 25 值 endpoint mode 集做 json 交集。
var ProviderCatalogModeValues = []string{"chat", "responses", "embedding", "audio", "video", "image_generation"}

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
	// Modes is the validated catalog-mode whitelist; empty means no filtering
	// (backward compatible for existing callers).
	Modes []string
}

const catalogScopeBuiltIn = "built_in"
const catalogScopeGlobal = "global"
const catalogScopePersonal = "personal"

// ListProviderModelsForRequest serves GET /{code}/models over the models
// 单表：vendor 即目录维度（设计 §3），旧「openai 兼容目标聚合协议子供应商目
// 录」的源扩展随 providers/provider_protocol_profiles 退役拆除——源码恒为归
// 一化后的单值；hybrid 不在 vendors 值域，查询自然返回空。双臂（builtin ∪
// global/personal）合并、scope 优先级、isSupportedCatalogModel/active/priced
// 过滤与 release-date 排序语义保持。
func (s *Store) ListProviderModelsForRequest(ctx context.Context, providerCode, systemAccountID string, includeInactive, includeUnpriced bool) ([]ModelCatalogItem, error) {
	sourceCodes := modelCatalogSourceProviderCodes(providerCode)
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
	// B4 ④/⑪：hybrid 身份保留门拆除——providerCode 归一化后单值（设计 §3，
	// hybrid 不在 vendors 值域；models.vendor_code 外键到 vendors.code，行查
	// 询在 hybrid 入参下恒空）；合并/继承不再携带 provider 身份参数。
	items := mergeModelCatalogItems(append(builtIn, custom...))
	inheritCustomCatalogCapabilities(items, builtIn)
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
	// withCatalogDisplay rides on every merged catalog row (Node maps the
	// sorted catalog through withCatalogDisplay).
	for index := range filtered {
		display := buildProviderCatalogDisplay(&filtered[index])
		filtered[index].CatalogDisplay = &display
	}
	return filtered, nil
}

// 供应商 token（vendor 值域沿用旧 providerCode 同名值；hybrid/openai 仅用于
// 内置源剔除与 hybrid 身份保留判定）。
const (
	hybridProviderCode = "hybrid"
	openaiProviderCode = "openai"
)

// modelCatalogSourceProviderCodes 归一化目录维度入参（设计 §3：vendor_code 即
// 目录归属维度，每个 vendor 即自身目录维度，无协议子供应商源扩展）。
func modelCatalogSourceProviderCodes(providerCode string) []string {
	normalized := normalizeProviderToken(providerCode)
	if normalized == "" {
		return []string{}
	}
	return []string{normalized}
}

// modelCatalogBuiltInSourceProviderCodes mirrors the same-named helper: only
// the openai-compatible target drops itself from its built-in sources (it has
// no built-in catalog of its own).
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

// builtInCatalogColumns mirrors the models 单表 builtin 读投影：旧目录行形状
// （provider_code/model 别名）+ supported_endpoint_modes_json（mode 与
// supported_api_protocols_json 的物理来源）。
const builtInCatalogColumns = `id, COALESCE(vendor_code, '') AS provider_code, public_model AS model, status,
	catalog_order, release_date, shutdown_date, supported_endpoint_modes_json,
	supported_service_tiers_json, supported_reasoning_efforts_json,
	default_reasoning_effort, codex_supported_reasoning_levels_json, codex_default_reasoning_level,
	codex_multi_agent_version, context_window_tokens, max_input_tokens, max_output_tokens, max_tokens,
	input_usd_per_1m, output_usd_per_1m, cached_input_usd_per_1m, cache_write_usd_per_1m,
	cache_write_1h_usd_per_1m, cache_storage_usd_per_1m_per_hour, service_tier_prices_json,
	long_context_input_token_threshold, long_context_input_token_threshold_inclusive,
	long_context_input_cost_multiplier, long_context_output_cost_multiplier,
	image_input_usd_per_1m, image_output_usd_per_1m, audio_input_usd_per_1m, audio_output_usd_per_1m,
	output_usd_per_image, supports_prompt_caching, catalog_visible, source, created_at, updated_at`

// listBuiltInCatalogModels reads the builtin arm：scope='builtin' 行按
// vendor_code 窗口，availability 过滤（active + catalog_visible + 未 shutdown）
// 在 SQL 施加，includeInactive 抬起（与 chain_catalog builtin 臂同谓词）。
func (s *Store) listBuiltInCatalogModels(ctx context.Context, providerCodes []string, includeInactive bool) ([]ModelCatalogItem, error) {
	codes := normalizeProviderCodeList(providerCodes)
	if len(codes) == 0 {
		return []ModelCatalogItem{}, nil
	}
	availabilityFilter := ""
	if !includeInactive {
		availabilityFilter = `
			AND status = 'active'
			AND ` + s.visibleTruePredicate("catalog_visible") + `
			AND (shutdown_date IS NULL OR trim(shutdown_date) = '' OR shutdown_date > ` + s.todayText() + `)`
	}
	args := append([]any{}, stringSliceToAny(codes)...)
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT `+builtInCatalogColumns+`
		FROM `+s.table("models")+`
		WHERE scope = 'builtin' AND vendor_code IN (`+placeholders(len(codes))+`)`+availabilityFilter+`
		ORDER BY provider_code, catalog_order, public_model, id`), args...)
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
		item                            ModelCatalogItem
		endpointModes                   sql.NullString
		releaseDate, shutdownDate       sql.NullString
		catalogOrder                    sql.NullInt64
		tiers, efforts                  sql.NullString
		codexLevels                     sql.NullString
		codexDefaultLevel               sql.NullString
		codexMultiAgent                 sql.NullString
		contextWindow, maxInput, maxOut sql.NullInt64
		maxTokens                       sql.NullInt64
		inputUsd, outputUsd             sql.NullFloat64
		cachedInput, cacheWrite         sql.NullFloat64
		cacheWrite1h, cacheStorage      sql.NullFloat64
		tierPrices                      sql.NullString
		longThreshold                   sql.NullInt64
		// models.catalog_visible 在 PostgreSQL 为 boolean、SQLite 为 integer，
		// 扫描目标用 NullBool 同时兼容两种驱动。
		longInclusive                    sql.NullBool
		longInputMultiplier, longOutMult sql.NullFloat64
		imageIn, imageOut, audioIn       sql.NullFloat64
		audioOut, outputPerImage         sql.NullFloat64
		promptCaching, catalogVisible    sql.NullBool
	)
	var defaultEffort sql.NullString
	if err := scan(&item.ID, &item.ProviderCode, &item.Model, &item.Status, &catalogOrder,
		&releaseDate, &shutdownDate, &endpointModes, &tiers, &efforts, &defaultEffort,
		&codexLevels, &codexDefaultLevel, &codexMultiAgent,
		&contextWindow, &maxInput, &maxOut, &maxTokens,
		&inputUsd, &outputUsd, &cachedInput, &cacheWrite, &cacheWrite1h, &cacheStorage,
		&tierPrices, &longThreshold, &longInclusive, &longInputMultiplier, &longOutMult,
		&imageIn, &imageOut, &audioIn, &audioOut, &outputPerImage,
		&promptCaching, &catalogVisible, &item.Source, &item.CreatedAt, &item.UpdatedAt); err != nil {
		return ModelCatalogItem{}, err
	}
	item.Scope = catalogScopeBuiltIn
	// 旧 mode / supportedApiProtocols 无物理列：从 supported_endpoint_modes_json
	// 逆映射派生（对前端保留键名；空 mode 集不注入 mode 键）。
	item.Mode, item.SupportedAPIProtocols = deriveLegacyCatalogProjection(endpointModes)
	item.ReleaseDate = textPtr(releaseDate)
	item.ShutdownDate = textPtr(shutdownDate)
	item.CatalogOrder = nullInt64Ptr(catalogOrder)
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
		inclusive := longInclusive.Bool
		item.LongContextInputTokenThresholdInclusive = &inclusive
	}
	item.LongContextInputCostMultiplier = nullFloat64Ptr(longInputMultiplier)
	item.LongContextOutputCostMultiplier = nullFloat64Ptr(longOutMult)
	item.ImageInputUsdPer1M = nullFloat64Ptr(imageIn)
	item.ImageOutputUsdPer1M = nullFloat64Ptr(imageOut)
	item.AudioInputUsdPer1M = nullFloat64Ptr(audioIn)
	item.AudioOutputUsdPer1M = nullFloat64Ptr(audioOut)
	item.OutputUsdPerImage = nullFloat64Ptr(outputPerImage)
	item.SupportsPromptCaching = promptCaching.Bool && promptCaching.Valid
	visible := catalogVisible.Bool && catalogVisible.Valid
	item.CatalogVisible = &visible
	ApplyBuiltInStaticDerivedFields(&item)
	item.SupportsServiceTier = len(item.SupportedServiceTiers) > 0
	return item, nil
}

// deriveLegacyCatalogProjection 把 supported_endpoint_modes_json 列值逆映射成
// 旧目录行 (mode, supportedApiProtocols) 投影；空/坏 JSON 渲染空协议数组且不
// 注入 mode（旧行为 NULL→absent）。
func deriveLegacyCatalogProjection(value sql.NullString) (*string, []string) {
	modes := parseJSONArray(value)
	mode, protocols := DeriveLegacyModeAndProtocols(modes)
	copiedMode := mode
	if mode == "" {
		return nil, protocols
	}
	return &copiedMode, protocols
}

// ApplyBuiltInStaticDerivedFields ports the toBuiltInCatalogItem capability
// derivations: modality/tool fallbacks from the static pricing table (the
// internal/pricing seam), the generated generation-parameter capabilities
// clamped by maxOutputTokens and the static-only source-pricing passthroughs
// (keepStaticPricingSource: a manual-override row keeps its own pricing
// provenance). 解析本体在 ResolveBuiltInStaticDerivedCapabilities（derived.go），
// 与 chat 面目录读取链共用同一实现。工具能力以「协议 × 工具」矩阵解析
// （SupportedToolsByProtocol）；一维 supportedTools 已随阶段 2 全链退场（
// chat 面切换删除）。SourceExchangeRateToUsd 有意收窄为
// “解析出非 nil 才赋值”：静态表未解析出汇率时不回写，防止未来 DB 行自带
// 该值被静态 nil 覆盖，与共享解析器的语义一致。
func ApplyBuiltInStaticDerivedFields(item *ModelCatalogItem) {
	resolved := ResolveBuiltInStaticDerivedCapabilities(item.ProviderCode, item.Model, item.MaxOutputTokens, item.Source)
	item.InputModalities = resolved.InputModalities
	item.OutputModalities = resolved.OutputModalities
	item.SupportedToolsByProtocol = resolved.SupportedToolsByProtocol
	item.SourcePricingCurrency = resolved.SourcePricingCurrency
	item.SourceExchangeRateDate = resolved.SourceExchangeRateDate
	item.SourcePricingNote = resolved.SourcePricingNote
	if resolved.CachedImageInputUsdPer1M != nil {
		item.CachedImageInputUsdPer1M = resolved.CachedImageInputUsdPer1M
	}
	if resolved.SourceExchangeRateToUsd != nil {
		item.SourceExchangeRateToUsd = resolved.SourceExchangeRateToUsd
	}
	item.VideoOutputUsdPerSecond = resolved.VideoOutputUsdPerSecond
	item.VideoOutputUsdPerCall = resolved.VideoOutputUsdPerCall
	item.TtsInputUsdPer1MChars = resolved.TtsInputUsdPer1MChars
	item.AudioInputUsdPerSecond = resolved.AudioInputUsdPerSecond
	item.GenerationParameterCapabilities = resolved.GenerationParameterCapabilities
}

// listCustomCatalogModels reads the custom arm（models 单表 global/personal
// scope 窗口）。
func (s *Store) listCustomCatalogModels(ctx context.Context, providerCodes []string, systemAccountID string, includeInactive bool) ([]ModelCatalogItem, error) {
	return s.listCustomModelRows(ctx, providerCodes, systemAccountID, includeInactive, "")
}

// listCustomModelRows shares the models 单表 custom 读形状；a non-empty
// modelFilter pins the rows to one public_model (the capabilities path).
// includeInactive=false carries the active + shutdown clauses, matching both
// the catalog default and the always-on test-catalog predicates.
func (s *Store) listCustomModelRows(ctx context.Context, providerCodes []string, systemAccountID string, includeInactive bool, modelFilter string) ([]ModelCatalogItem, error) {
	codes := normalizeProviderCodeList(providerCodes)
	if len(codes) == 0 {
		return []ModelCatalogItem{}, nil
	}
	clauses := []string{"vendor_code IN (" + placeholders(len(codes)) + ")"}
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
		clauses = append(clauses, "public_model = ?")
		args = append(args, modelFilter)
	}
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT id, COALESCE(vendor_code, '') AS provider_code, public_model AS model, scope, system_account_id, status,
			supported_endpoint_modes_json, release_date, shutdown_date, supported_service_tiers_json,
			supported_reasoning_efforts_json, default_reasoning_effort,
			context_window_tokens, max_input_tokens, max_output_tokens,
			input_usd_per_1m, output_usd_per_1m, cached_input_usd_per_1m, cache_write_usd_per_1m,
			cache_write_1h_usd_per_1m, cache_storage_usd_per_1m_per_hour, service_tier_prices_json,
			image_input_usd_per_1m, image_output_usd_per_1m, audio_input_usd_per_1m, audio_output_usd_per_1m,
			output_usd_per_image, supports_prompt_caching, pricing_notes, capability_notes, notes, created_at, updated_at
		FROM `+s.table("models")+`
		WHERE `+strings.Join(clauses, " AND ")+`
		ORDER BY provider_code ASC, scope ASC, `+s.modelOrderExpression()+` ASC, id ASC`), args...)
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
		item                          ModelCatalogItem
		systemAccountID               sql.NullString
		endpointModes                 sql.NullString
		releaseDate, shutdownDate     sql.NullString
		tiers, efforts                sql.NullString
		defaultEffort                 sql.NullString
		contextWindow, maxInput       sql.NullInt64
		maxOut                        sql.NullInt64
		inputUsd, outputUsd           sql.NullFloat64
		cachedInput, cacheWrite       sql.NullFloat64
		cacheWrite1h, cacheStorage    sql.NullFloat64
		tierPrices                    sql.NullString
		imageIn, imageOut, audioIn    sql.NullFloat64
		audioOut, outputPerImage      sql.NullFloat64
		pricingNotes, capabilityNotes sql.NullString
		notes                         sql.NullString
		promptCaching                 sql.NullBool
	)
	if err := scan(&item.ID, &item.ProviderCode, &item.Model, &item.Scope, &systemAccountID, &item.Status,
		&endpointModes, &releaseDate, &shutdownDate, &tiers, &efforts, &defaultEffort,
		&contextWindow, &maxInput, &maxOut,
		&inputUsd, &outputUsd, &cachedInput, &cacheWrite, &cacheWrite1h, &cacheStorage,
		&tierPrices,
		&imageIn, &imageOut, &audioIn, &audioOut, &outputPerImage,
		&promptCaching, &pricingNotes, &capabilityNotes, &notes, &item.CreatedAt, &item.UpdatedAt); err != nil {
		return ModelCatalogItem{}, err
	}
	item.SystemAccountID = nullPtrString(systemAccountID)
	// 旧 mode / supportedApiProtocols 无物理列：从 supported_endpoint_modes_json
	// 逆映射派生（对前端保留键名）。
	item.Mode, item.SupportedAPIProtocols = deriveLegacyCatalogProjection(endpointModes)
	item.ReleaseDate = textPtr(releaseDate)
	item.ShutdownDate = textPtr(shutdownDate)
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
	// toCustomCatalogItem: empty static capability shapes; prompt caching 直接
	// 读 models.supports_prompt_caching（写入侧按 cached-input 价维护，与 chat
	// 面 decorateCustomCatalogRow 同口径）；generation-parameter capabilities
	// generate from provider+model。
	item.InputModalities = []string{}
	item.OutputModalities = []string{}
	item.SupportedToolsByProtocol = map[string][]string{}
	item.GenerationParameterCapabilities = generationParameterCapabilitiesToAny(
		generationParameterCapabilitiesForModel(item.ProviderCode, item.Model, item.MaxOutputTokens))
	item.CodexSupportedReasoningLevels = []string{}
	item.SupportsPromptCaching = promptCaching.Bool && promptCaching.Valid
	item.SupportsServiceTier = len(item.SupportedServiceTiers) > 0
	// models.source 列对 custom 行为 NULL：沿旧 custom 表投影覆盖写来源标记。
	if item.Scope == catalogScopeGlobal {
		item.Source = "custom-global"
	} else {
		item.Source = "custom-personal"
	}
	return item, nil
}

// mergeModelCatalogItems ports mergeModelCatalogItems: dedupe by model, higher
// scope priority wins and later rows win ties. hybrid 身份随目标 schema 退役
// （B4 ⑪：providers 值域已无 hybrid，合并键恒为裸 model）。
func mergeModelCatalogItems(items []ModelCatalogItem) []ModelCatalogItem {
	merged := map[string]ModelCatalogItem{}
	order := []string{}
	for _, item := range items {
		model := strings.TrimSpace(item.Model)
		if model == "" {
			continue
		}
		existing, ok := merged[model]
		if !ok || catalogScopePriority(item.Scope) >= catalogScopePriority(existing.Scope) {
			if !ok {
				order = append(order, model)
			}
			merged[model] = item
		}
	}
	output := make([]ModelCatalogItem, 0, len(order))
	for _, model := range order {
		output = append(output, merged[model])
	}
	return output
}

// inheritCustomCatalogCapabilities 应用 custom 目录行能力继承（BUG-0229，
// 契约见 AI问答设计 8.6、工具体系设计 6.4）：合并胜出的 custom（global/personal）
// 行按 mergeModelCatalogItems 的同一合并键（裸 model，hybrid 身份已退役）
// 查找内置扫描行（scanBuiltInCatalogItem 已过 ApplyBuiltInStaticDerivedFields
// 静态兜底），仅填空 supportedToolsByProtocol / inputModalities /
// outputModalities（二维矩阵继承）；一维 supportedTools 投影在二维继承后按
// 并集回填（custom 行入链时一维恒空）；全新自定义模型（内置无对应行）保持空。
// 解析本体 InheritBuiltinCatalogCapabilities 与 chat 面
// （chain_catalog.go chainInheritCustomCatalogCapabilities）同源，两面不漂移。
func inheritCustomCatalogCapabilities(items, builtinRows []ModelCatalogItem) {
	if len(items) == 0 || len(builtinRows) == 0 {
		return
	}
	builtinByKey := make(map[string]ModelCatalogItem, len(builtinRows))
	for _, item := range builtinRows {
		model := strings.TrimSpace(item.Model)
		if model == "" {
			continue
		}
		// 同优先级内置行后行胜出，与 mergeModelCatalogItems 的决胜语义一致。
		builtinByKey[model] = item
	}
	for index := range items {
		item := &items[index]
		if item.Scope != catalogScopeGlobal && item.Scope != catalogScopePersonal {
			continue
		}
		model := strings.TrimSpace(item.Model)
		if model == "" {
			continue
		}
		builtin, ok := builtinByKey[model]
		if !ok {
			continue
		}
		resolved := InheritBuiltinCatalogCapabilities(
			CustomCatalogCapabilityKeys{
				SupportedToolsByProtocol: item.SupportedToolsByProtocol,
				InputModalities:          item.InputModalities,
				OutputModalities:         item.OutputModalities,
			},
			CustomCatalogCapabilityKeys{
				SupportedToolsByProtocol: builtin.SupportedToolsByProtocol,
				InputModalities:          builtin.InputModalities,
				OutputModalities:         builtin.OutputModalities,
			},
		)
		item.SupportedToolsByProtocol = resolved.SupportedToolsByProtocol
		item.InputModalities = resolved.InputModalities
		item.OutputModalities = resolved.OutputModalities
	}
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

// isSupportedCatalogModel ports isSupportedCatalogModel. M1 裁决（音频设计
// §2）：mode=audio（含 audio_speech / audio_transcription 别名）且协议为
// audio_speech / audio_transcription 的模型收录（名字 token 排除不再生效）；
// 旧词表的单 audio 协议继续排除；mode 标注 audio 但协议集里没有音频协议的行
// 维持排除（防止 audio 分类行进对话链）；mode 未标注 audio 的模型继续按名字
// token 排除。M2 裁决（媒体设计 §2）同构放开 video：mode=video 且协议含
// video 的模型收录。M5b 裁决（Realtime 设计 §6）同构放开 realtime：
// mode=audio 且协议含 realtime 的目录行收录（显式标注行不受名字 token
// 排除，沿 M1 裁决）；mode 未标注的行 realtime 协议维持排除（词表放行只
// 对显式标注行生效）。其余维持排除。mode/protocols 输入为逆映射派生值。
// 与 chain_catalog.go 的 chainIsSupportedCatalogModel 同源，改动必须两处同步。
func isSupportedCatalogModel(item ModelCatalogItem) bool {
	mode := ""
	if item.Mode != nil {
		mode = strings.ToLower(strings.TrimSpace(*item.Mode))
	}
	modeIsAudio := mode == "audio" || mode == "audio_speech" || mode == "audio_transcription"
	modeIsVideo := mode == "video"
	if len(item.SupportedAPIProtocols) == 1 && item.SupportedAPIProtocols[0] == "audio" {
		return false
	}
	if modeIsAudio {
		for _, protocol := range item.SupportedAPIProtocols {
			if protocol == "audio_speech" || protocol == "audio_transcription" || protocol == "realtime" {
				return true
			}
		}
		return false
	}
	if modeIsVideo {
		for _, protocol := range item.SupportedAPIProtocols {
			if protocol == "video" {
				return true
			}
		}
		return false
	}
	for _, protocol := range item.SupportedAPIProtocols {
		if protocol == "realtime" {
			return false
		}
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

// hasDirectPrice ports hasDirectPrice. 2026-10-05 验收批补媒体四维：静态
// 层派生的视频秒价/按次价、TTS 字符价、ASR 秒价已由
// ApplyBuiltInStaticDerivedFields 填充到 item（目录表无列），不纳入判断会
// 把 cogvideox/doubao-tts/seedance/paraformer 等纯媒体行误判为无价、在
// priced-only 目录请求里整行消失（用户验收报「缺行」的直接根因）。
func hasDirectPrice(item ModelCatalogItem) bool {
	if item.InputUsdPer1M != nil || item.OutputUsdPer1M != nil || item.CachedInputUsdPer1M != nil ||
		item.CacheWriteUsdPer1M != nil || item.CacheWrite1hUsdPer1M != nil ||
		item.CacheStorageUsdPer1MPerHour != nil || item.ImageInputUsdPer1M != nil ||
		item.ImageOutputUsdPer1M != nil || item.AudioInputUsdPer1M != nil ||
		item.AudioOutputUsdPer1M != nil || item.OutputUsdPerImage != nil {
		return true
	}
	if item.VideoOutputUsdPerSecond != nil || item.VideoOutputUsdPerCall != nil ||
		item.TtsInputUsdPer1MChars != nil || item.AudioInputUsdPerSecond != nil {
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

// visibleTruePredicate renders the catalog visibility predicate for the
// active dialect: SQLite stores 1/0, PostgreSQL uses boolean.
func (s *Store) visibleTruePredicate(column string) string {
	if s.pg {
		return column + " = TRUE"
	}
	return column + " = 1"
}

// modelOrderExpression mirrors the archived dual-dialect custom model ordering
// on the models 单表 public_model 列: `public_model COLLATE NOCASE` on SQLite,
// `lower(public_model)` on PostgreSQL.
func (s *Store) modelOrderExpression() string {
	if s.pg {
		return "lower(public_model)"
	}
	return "public_model COLLATE NOCASE"
}

// ListProviderModelSelectionOptions serves GET /models/options: the merged
// builtin/custom selection options over the models 单表（双臂同表、谓词分臂）.
// 第二返回值 total 是去重与 keyword/selected 过滤后、limit 截断前的候选总数
// （当前 providerCode/modes/scope 筛选下的可见候选全集），供响应顶层
// hasMore = total > len(items) 元数据（T25 截断提示根治）。
func (s *Store) ListProviderModelSelectionOptions(ctx context.Context, query ModelOptionQuery) ([]ModelSelectionOption, int, error) {
	sourceCodes, builtInCodes, err := s.modelOptionSourceCodes(ctx, query.ProviderCode, query.Protocol)
	if err != nil {
		return nil, 0, err
	}
	if len(sourceCodes) == 0 {
		return []ModelSelectionOption{}, 0, nil
	}
	builtIn, err := s.listBuiltInModelOptions(ctx, builtInCodes, query)
	if err != nil {
		return nil, 0, err
	}
	custom, err := s.listCustomModelOptions(ctx, sourceCodes, query)
	if err != nil {
		return nil, 0, err
	}
	options, total := mergeProviderModelOptionRows(append(builtIn, custom...), query)
	return options, total, nil
}

// ListProviderModelOptionProviderCodes aggregates the distinct vendor codes
// that carry at least one in-catalog model option under the query filter
// (v8-D media-domain default-provider convergence). 单表后双臂合流为一条
// models 查询：builtin 臂的 catalog_visible 谓词收窄为 builtin 行、custom 臂
// 的 scope 窗口保持，谓词集与 ListProviderModelSelectionOptions 完全同构
// （hybrid 不在 vendors 值域，经 modelOptionSourceCodes 自然排除），只取
// DISTINCT provider_code，不受 options endpoint LIMIT 50 截断。
func (s *Store) ListProviderModelOptionProviderCodes(ctx context.Context, query ModelOptionQuery) ([]string, error) {
	// 单表后 DISTINCT 聚合只看 vendor 窗口（builtin ∪ global/personal 同表），
	// openai 自剔除臂不再影响去重结果，builtInCodes 弃用。
	sourceCodes, _, err := s.modelOptionSourceCodes(ctx, query.ProviderCode, query.Protocol)
	if err != nil {
		return nil, err
	}
	if len(sourceCodes) == 0 {
		return []string{}, nil
	}
	clauses := []string{
		"vendor_code IN (" + placeholders(len(sourceCodes)) + ")",
		"status = 'active'",
		"(shutdown_date IS NULL OR trim(shutdown_date) = '' OR shutdown_date > " + s.todayText() + ")",
		// builtin 臂的目录可见性谓词（custom 行写路径恒 true，不受影响）。
		"(" + s.visibleTruePredicate("catalog_visible") + " OR scope != 'builtin')",
	}
	args := append([]any{}, stringSliceToAny(sourceCodes)...)
	// 双臂合流查询：builtin 行不受 owner 归属窗口过滤（旧 builtin 臂无窗口），
	// global/personal 行保持 owner 可见性。
	if trimmedAccount := strings.TrimSpace(query.SystemAccountID); trimmedAccount != "" {
		clauses = append(clauses, "(scope = 'builtin' OR (scope = 'global' AND system_account_id IS NULL) OR (scope = 'personal' AND system_account_id = ?))")
		args = append(args, trimmedAccount)
	} else {
		clauses = append(clauses, "(scope = 'builtin' OR (scope = 'global' AND system_account_id IS NULL))")
	}
	args = s.appendCatalogModeFilter(&clauses, args, query.Modes)
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT DISTINCT COALESCE(vendor_code, '') AS provider_code
		FROM `+s.table("models")+`
		WHERE `+strings.Join(clauses, " AND ")+`
		ORDER BY provider_code ASC`), args...)
	if err != nil {
		return nil, err
	}
	codes := map[string]bool{}
	if err := collectDistinctProviderCodes(rows, codes); err != nil {
		return nil, err
	}
	output := make([]string, 0, len(codes))
	for code := range codes {
		output = append(output, code)
	}
	sort.Strings(output)
	return output, nil
}

func collectDistinctProviderCodes(rows *sql.Rows, codes map[string]bool) error {
	defer rows.Close()
	for rows.Next() {
		var code string
		if err := rows.Scan(&code); err != nil {
			return err
		}
		if trimmed := strings.TrimSpace(code); trimmed != "" {
			codes[trimmed] = true
		}
	}
	return rows.Err()
}

// modelOptionSourceCodes resolves the option source windows. 旧协议子供应商
// 展开（protocol → 携该协议 profile 的 provider 集合）随
// providers/provider_protocol_profiles 退役拆除：providerCode 入参取归一化
// 单值；protocol 入参不再收窄源集（vendors 不携带协议维度，行级协议信息由
// supported_endpoint_modes_json 派生投影承载），与默认臂同为全部启用 vendor。
func (s *Store) modelOptionSourceCodes(ctx context.Context, providerCode, protocol string) (sourceCodes, builtInCodes []string, err error) {
	if providerCode != "" {
		sourceCodes = modelCatalogSourceProviderCodes(providerCode)
		return sourceCodes, modelCatalogBuiltInSourceProviderCodes(providerCode, sourceCodes), nil
	}
	sourceCodes, err = s.EnabledNonHybridProviderCodes(ctx)
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
	Mode                      string
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
		"scope = 'builtin'",
		"vendor_code IN (" + placeholders(len(codes)) + ")",
		"status = 'active'",
		s.visibleTruePredicate("catalog_visible"),
		"(shutdown_date IS NULL OR trim(shutdown_date) = '' OR shutdown_date > " + s.todayText() + ")",
	}
	args := append([]any{}, stringSliceToAny(codes)...)
	args = s.appendOptionTextFilter(&clauses, args, query)
	args = s.appendCatalogModeFilter(&clauses, args, query.Modes)
	args = appendSelectedOrderArgs(args, query)
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT COALESCE(vendor_code, '') AS provider_code, public_model AS model, supported_endpoint_modes_json, release_date,
			supported_service_tiers_json, supported_reasoning_efforts_json,
			default_reasoning_effort
		FROM `+s.table("models")+`
		WHERE `+strings.Join(clauses, " AND ")+`
		ORDER BY `+selectedOrderClause(query)+`CASE WHEN release_date IS NULL OR trim(release_date) = '' THEN 1 ELSE 0 END ASC,
			release_date DESC, `+s.modelOrderExpression()+` ASC, provider_code ASC, id ASC`), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []modelOptionRow{}
	for rows.Next() {
		row, scanErr := scanModelOptionRow(rows.Scan, catalogScopeBuiltIn, false)
		if scanErr != nil {
			return nil, scanErr
		}
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
		"vendor_code IN (" + placeholders(len(codes)) + ")",
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
	args = s.appendOptionTextFilter(&clauses, args, query)
	args = s.appendCatalogModeFilter(&clauses, args, query.Modes)
	args = appendSelectedOrderArgs(args, query)
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT COALESCE(vendor_code, '') AS provider_code, public_model AS model, scope, supported_endpoint_modes_json, release_date,
			supported_service_tiers_json, supported_reasoning_efforts_json,
			default_reasoning_effort
		FROM `+s.table("models")+`
		WHERE `+strings.Join(clauses, " AND ")+`
		ORDER BY `+selectedOrderClause(query)+`CASE WHEN release_date IS NULL OR trim(release_date) = '' THEN 1 ELSE 0 END ASC,
			release_date DESC, `+s.modelOrderExpression()+` ASC, provider_code ASC, scope ASC, id ASC`), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []modelOptionRow{}
	for rows.Next() {
		row, scanErr := scanModelOptionRow(rows.Scan, "", true)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, row)
	}
	return items, rows.Err()
}

// scanModelOptionRow 扫描 options 行：scope 由调用方注入（builtin 臂）或按行
// 读取（custom 臂）；mode 从 supported_endpoint_modes_json 逆映射派生。
func scanModelOptionRow(scan func(...any) error, scope string, scanScope bool) (modelOptionRow, error) {
	var row modelOptionRow
	var endpointModes, releaseDate sql.NullString
	var tiers, efforts sql.NullString
	var defaultEffort sql.NullString
	row.Scope = scope
	columns := []any{&row.ProviderCode, &row.Model, &endpointModes, &releaseDate, &tiers, &efforts, &defaultEffort}
	if scanScope {
		columns = []any{&row.ProviderCode, &row.Model, &row.Scope, &endpointModes, &releaseDate, &tiers, &efforts, &defaultEffort}
	}
	if err := scan(columns...); err != nil {
		return modelOptionRow{}, err
	}
	modes := parseJSONArray(endpointModes)
	mode, protocols := DeriveLegacyModeAndProtocols(modes)
	row.Mode = mode
	row.ReleaseDate = textPtr(releaseDate)
	row.SupportedAPIProtocols = protocols
	row.SupportedServiceTiers = parseJSONArray(tiers)
	row.SupportedReasoningEfforts = parseJSONArray(efforts)
	row.DefaultReasoningEffort = textPtr(defaultEffort)
	return row, nil
}

// catalogModeEndpointModeExpansion 把六值目录 mode 白名单展开成 25 值 endpoint
// mode 集合（supported_endpoint_modes_json 的元素域）。单表后 builtin 行与
// custom 行共用同一 mode 词表，旧 customVocabularyModes 的 text/image 翻译层
// 随双表退役；展开表与逆映射 catalogEndpointModeLegacyProjection 互为镜像。
func catalogModeEndpointModeExpansion(modes []string) []string {
	expansion := catalogModeFilterExpansion()
	seen := map[string]bool{}
	output := []string{}
	for _, mode := range modes {
		for _, endpointMode := range expansion[mode] {
			if !seen[endpointMode] {
				seen[endpointMode] = true
				output = append(output, endpointMode)
			}
		}
	}
	sort.Strings(output)
	return output
}

func catalogModeFilterExpansion() map[string][]string {
	return map[string][]string{
		"chat":             {"chat_json", "chat_sse", "messages_json", "messages_sse", "generate_content_json", "generate_content_sse", "interactions_json", "interactions_sse"},
		"responses":        {"responses_json", "responses_sse"},
		"embedding":        {"message_token_counting", "count_tokens", "embed_content"},
		"audio":            {"audio_speech", "audio_transcription_json", "realtime_session"},
		"video":            {"video_create", "video_get", "video_content", "video_cancel"},
		"image_generation": {"images_json"},
	}
}

// appendCatalogModeFilter adds the endpoint-modes json 交集 clause when the
// query carries validated modes. 空模式集（draft/disabled 行）不与任何展开集
// 相交，行为对齐旧 `mode IN (...)` 对 NULL/空 mode 的排除；without modes the
// clause is skipped and the behavior stays backward compatible.
func (s *Store) appendCatalogModeFilter(clauses *[]string, args []any, modes []string) []any {
	if len(modes) == 0 {
		return args
	}
	endpointModes := catalogModeEndpointModeExpansion(modes)
	if len(endpointModes) == 0 {
		*clauses = append(*clauses, "1 = 0")
		return args
	}
	*clauses = append(*clauses, s.endpointModesAnyPredicate("supported_endpoint_modes_json", endpointModes))
	return append(args, stringSliceToAny(endpointModes)...)
}

// endpointModesAnyPredicate 渲染 supported_endpoint_modes_json 与给定 mode 集
// 交集非空的方言谓词（SQLite json_each / PG jsonb_array_elements_text）。
func (s *Store) endpointModesAnyPredicate(column string, modes []string) string {
	list := placeholders(len(modes))
	if s.pg {
		return `EXISTS (
			SELECT 1
			FROM jsonb_array_elements_text(` + column + `::jsonb) AS endpoint_mode
			WHERE endpoint_mode IN (` + list + `)
		)`
	}
	return `EXISTS (
			SELECT 1
			FROM json_each(` + column + `) AS endpoint_mode
			WHERE endpoint_mode.value IN (` + list + `)
		)`
}

// appendOptionTextFilter mirrors the WHERE side of the options queries: the
// (selectedIds OR keyword LIKE) clause is pushed only when a keyword is
// present. Without a keyword the selectedIds participate only in the ORDER BY
// preface and the final selected-aware visibility merge.
func (s *Store) appendOptionTextFilter(clauses *[]string, args []any, query ModelOptionQuery) []any {
	if query.Keyword == "" {
		return args
	}
	parts := []string{}
	if len(query.SelectedIDs) > 0 {
		parts = append(parts, "public_model IN ("+placeholders(len(query.SelectedIDs))+")")
		for _, id := range query.SelectedIDs {
			args = append(args, id)
		}
	}
	parts = append(parts, s.modelOrderExpression()+" LIKE ?")
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
	return "CASE WHEN public_model IN (" + placeholders(len(query.SelectedIDs)) + ") THEN 0 ELSE 1 END, "
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
// and the selected-aware limit. 第二返回值是 limit 截断前的候选总数（len(order)，
// T25 total 元数据）：selectedIds 全部可见，因此返回条数可能超过 limit，
// hasMore 必须按 total 与实际返回条数比较而不是与 limit 比较。
func mergeProviderModelOptionRows(rows []modelOptionRow, query ModelOptionQuery) ([]ModelSelectionOption, int) {
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
			Mode:                      row.Mode,
			SupportedAPIProtocols:     copyStringSlice(row.SupportedAPIProtocols),
			SupportedServiceTiers:     copyStringSlice(row.SupportedServiceTiers),
			SupportedReasoningEfforts: copyStringSlice(row.SupportedReasoningEfforts),
			DefaultReasoningEffort:    row.DefaultReasoningEffort,
		})
	}
	return options, len(order)
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
	sourceCodes := modelCatalogSourceProviderCodes(providerCode)
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
	// B4 ⑪：hybrid 身份门随目标 schema 退役，合并键恒为裸 model。
	winner := mergeTestCatalogItems(append(builtIn, custom...))
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
// with the default 'test' projection over the models 单表 builtin 臂.
func (s *Store) findBuiltInTestCatalogItems(ctx context.Context, providerCodes []string, model string) ([]testCatalogItem, error) {
	codes := normalizeProviderCodeList(providerCodes)
	if len(codes) == 0 {
		return []testCatalogItem{}, nil
	}
	args := append([]any{}, stringSliceToAny(codes)...)
	args = append(args, model)
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT id, COALESCE(vendor_code, '') AS provider_code, public_model AS model, catalog_order, release_date,
			supported_endpoint_modes_json, supported_service_tiers_json, supported_reasoning_efforts_json,
			default_reasoning_effort
		FROM `+s.table("models")+`
		WHERE scope = 'builtin'
			AND vendor_code IN (`+placeholders(len(codes))+`)
			AND public_model = ?
			AND status = 'active'
			AND `+s.visibleTruePredicate("catalog_visible")+`
			AND (shutdown_date IS NULL OR trim(shutdown_date) = '' OR shutdown_date > `+s.todayText()+`)
		ORDER BY provider_code ASC, catalog_order ASC, public_model ASC, id ASC`), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []testCatalogItem{}
	for rows.Next() {
		var item testCatalogItem
		var catalogOrder sql.NullInt64
		var releaseDate sql.NullString
		var endpointModes, tiers, efforts sql.NullString
		var defaultEffort sql.NullString
		if err := rows.Scan(&item.ID, &item.ProviderCode, &item.Model, &catalogOrder, &releaseDate,
			&endpointModes, &tiers, &efforts, &defaultEffort); err != nil {
			return nil, err
		}
		item.Scope = catalogScopeBuiltIn
		item.CatalogOrder = nullInt64Ptr(catalogOrder)
		item.ReleaseDate = textPtr(releaseDate)
		_, item.SupportedAPIProtocols = deriveLegacyCatalogProjection(endpointModes)
		item.SupportedServiceTiers = parseJSONArray(tiers)
		item.SupportedReasoningEfforts = parseJSONArray(efforts)
		item.DefaultReasoningEffort = textPtr(defaultEffort)
		items = append(items, item)
	}
	return items, rows.Err()
}

// mergeTestCatalogItems ports mergeProviderModelTestCatalogItems +
// compareProviderModelTestCatalogItems, returning the first row. hybrid 身份已
// 退役（B4 ⑪）：合并键恒为裸 model。
func mergeTestCatalogItems(items []testCatalogItem) *testCatalogItem {
	merged := map[string]testCatalogItem{}
	order := []string{}
	for _, item := range items {
		model := strings.TrimSpace(item.Model)
		if model == "" {
			continue
		}
		existing, ok := merged[model]
		if !ok || testCatalogPriority(item.Scope) >= testCatalogPriority(existing.Scope) {
			if !ok {
				order = append(order, model)
			}
			merged[model] = item
		}
	}
	if len(order) == 0 {
		return nil
	}
	sorted := make([]testCatalogItem, 0, len(order))
	for _, model := range order {
		sorted = append(sorted, merged[model])
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
