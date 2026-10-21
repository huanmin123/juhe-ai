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
//
// 2026-09-28 修复：补回 Node toBuiltInCatalogItem 的静态能力兜底
// （decorateBuiltinStaticDerivedCapabilities）。数据库目录表从 Node 时代起就
// 没有 supported_tools / input_modalities / output_modalities 列，Node 在读取
// 链用静态定价表兜底，Go 迁移此前只移植到了管理面
// （internal/providers ApplyBuiltInStaticDerivedFields），chat 面目录的
// supportedTools / inputModalities / outputModalities 恒空，导致 AI 问答
// supportsWebSearch 恒 false、web_search 等内置工具对 GPT 模型永不注入。
// 两侧现在共同调用 providers.ResolveBuiltInStaticDerivedCapabilities。
//
// 2026-09-28 修复（BUG-0229）：custom（global/personal）目录行按 scope 优先级
// 整行替换内置行后，custom_provider_models 没有能力三列，替换后目录里该模型
// 的 supportedTools / inputModalities / outputModalities 恒空（web_search 永不
// 注入、带图输入被 400、协议偏好退化）。现按文档契约（AI问答设计 8.6
// 「custom 目录行能力继承」）在合并后对 custom 来源行按同一合并键回填空能力
// 键（仅填空，providers.InheritBuiltinCatalogCapabilities 与管理面同源）；
// 全新自定义模型（内置无对应行）保持空，不用静态定价表的别名/前缀匹配回填。
//
// 2026-09-28 目录「协议 × 工具」矩阵（工具体系设计 6.4，整体改造第一阶段）：
// 静态兜底与 custom 继承改为携带二维 supportedToolsByProtocol（键=该行
// supportedApiProtocols 枚举，值=该协议下可用工具）；一维 supportedTools 作为
// 二维并集的过渡投影保留（阶段 2 随 chat 面切换删除），chat 面现有
// supportsWebSearch / function_calling 判定继续读一维并集，行为不变。

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/pricing"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/providers"
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

// chainCatalogColumns 是 models 单表（供应商与上游协议面重构设计 §3）的目录
// 读投影：pair[0] 为 SQL 列表达式、pair[1] 为 Node camelCase 行键。旧
// provider_model_catalog（内置）与 custom_provider_models（自定义）合并为唯一
// 物理表后双源共用本列集；`mode` 与 `supportedApiProtocols` 两键不再有物理列，
// 由 decorate 层从 supported_endpoint_modes_json 反推（seed 正向投影
// modelSeedProtocolEndpoints 的读侧逆映射，见 catalogDeriveLegacyModeAndProtocols）。
var chainCatalogColumns = [][2]string{
	{"id", "id"},
	{"COALESCE(vendor_code, '') AS provider_code", "providerCode"},
	{"public_model", "model"},
	{"scope", "scope"},
	{"system_account_id", "systemAccountId"},
	{"status", "status"},
	{"catalog_order", "catalogOrder"},
	{"release_date", "releaseDate"},
	{"shutdown_date", "shutdownDate"},
	{"supported_endpoint_modes_json", "supportedEndpointModes"},
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
	{"pricing_notes", "pricingNotes"},
	{"capability_notes", "capabilityNotes"},
	{"notes", "notes"},
	{"created_at", "createdAt"},
	{"updated_at", "updatedAt"},
}

// ListProviderModelCatalog mirrors listProviderModelCatalogAsync over the
// models 单表（供应商与上游协议面重构设计 §3）：vendor_code 维度下 builtin 行
// （scope='builtin'）承内置目录、global/personal 行承自定义目录（owner 可见性
// 按 system_account_id）。旧「openai 兼容目标聚合协议子供应商目录」的源扩展
// 随 providers/provider_protocol_profiles 两表退役拆除——vendor 值域与旧
// providerCode 同名且每个 vendor 即自身目录维度，源码恒为归一化后的单值；
// 双源（builtin ∪ custom）合并、scope 优先级、isSupportedCatalogModel/active/
// priced 过滤与 release-date 排序语义保持。
func (s *chainCatalogSource) ListProviderModelCatalog(ctx context.Context, input gatewayruntimecache.ModelCatalogListOptions) ([]gatewayruntimecache.ProviderModelCatalogItem, error) {
	sourceCodes := sourceVendorCodes(input.ProviderCode)
	if len(sourceCodes) == 0 {
		return []gatewayruntimecache.ProviderModelCatalogItem{}, nil
	}
	now := s.now().UTC().Format("2006-01-02")
	items := []gatewayruntimecache.ProviderModelCatalogItem{}
	builtinRows := []gatewayruntimecache.ProviderModelCatalogItem{}

	builtInCodes := chainCatalogBuiltInSourceProviderCodes(input.ProviderCode, sourceCodes)
	if len(builtInCodes) > 0 {
		builtInQuery, builtInArgs := s.builtinCatalogQuery(input, now, builtInCodes)
		rows, err := s.db.QueryContext(ctx, s.bind(builtInQuery), builtInArgs...)
		if err != nil {
			return nil, err
		}
		scanned, err := scanCatalogRows(rows, chainCatalogColumns, decorateBuiltinCatalogRow)
		if err != nil {
			return nil, err
		}
		builtinRows = append(builtinRows, scanned...)
		items = append(items, scanned...)
	}

	customQuery, customArgs := s.customCatalogQuery(input, now, sourceCodes)
	rows, err := s.db.QueryContext(ctx, s.bind(customQuery), customArgs...)
	if err != nil {
		return nil, err
	}
	scanned, err := scanCatalogRows(rows, chainCatalogColumns, decorateCustomCatalogRow)
	if err != nil {
		return nil, err
	}
	items = append(items, scanned...)

	preserveProviderIdentity := chainNormalizeProviderToken(input.ProviderCode) == chainCatalogHybridProviderCode
	merged := chainMergeCatalogItems(items, preserveProviderIdentity)
	chainInheritCustomCatalogCapabilities(merged, builtinRows, preserveProviderIdentity)
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

// accountModelCatalogColumns 在统一目录列集上追加 upstream_model：账户作用域
// 目录必须同时给出 model_id（ID）、vendor（ProviderCode）、public/upstream 名，
// 计价与模型映射才能按真实 models 行解析，不靠名字猜 vendor。
var accountModelCatalogColumns = func() [][2]string {
	columns := make([][2]string, 0, len(chainCatalogColumns)+1)
	columns = append(columns, chainCatalogColumns...)
	columns = append(columns, [2]string{"upstream_model", "upstreamModel"})
	return columns
}()

// ListAccountSupportedModelCatalog 实现 gatewayruntimecache.AccountModelCatalogSource：
// 只返回 account_supported_models 显式绑定的 active models 行，保留完整目录
// 元数据（context window、reasoning、价格、modes/protocols 派生）。generic_http
// 账户的 vendor_code 为空不再导致空目录；不按 vendor 聚合、不伪造 custom。
func (s *chainCatalogSource) ListAccountSupportedModelCatalog(ctx context.Context, accountID, systemAccountID string) ([]gatewayruntimecache.ProviderModelCatalogItem, error) {
	if strings.TrimSpace(accountID) == "" {
		return []gatewayruntimecache.ProviderModelCatalogItem{}, nil
	}
	query := fmt.Sprintf(`SELECT %s FROM (
		SELECT models.* FROM %s models
		INNER JOIN %s supported ON supported.model_id = models.id
		WHERE supported.account_id = ?
		  AND models.status = 'active'
		  AND (models.scope IN ('builtin', 'global')
		       OR (models.scope = 'personal' AND models.system_account_id = ?))
	) models
	ORDER BY catalog_order ASC, lower(public_model) ASC, id ASC`,
		catalogColumnList(accountModelCatalogColumns), s.table("models"), s.table("account_supported_models"))
	rows, err := s.db.QueryContext(ctx, s.bind(query), accountID, strings.TrimSpace(systemAccountID))
	if err != nil {
		return nil, err
	}
	scanned, err := scanCatalogRows(rows, accountModelCatalogColumns, decorateAccountModelCatalogRow)
	if err != nil {
		return nil, err
	}
	out := make([]gatewayruntimecache.ProviderModelCatalogItem, 0, len(scanned))
	for _, item := range scanned {
		if !chainIsSupportedCatalogModel(item) {
			continue
		}
		out = append(out, item)
	}
	sort.SliceStable(out, func(left, right int) bool {
		return chainCompareCatalogItems(out[left], out[right]) < 0
	})
	return out, nil
}

// decorateAccountModelCatalogRow 按行 scope 复用 builtin/custom 装饰器：
// builtin 行走静态能力兜底，global/personal 行走 custom 来源标记；两条路径
// 都从 supported_endpoint_modes_json 反推 mode/supportedApiProtocols。
func decorateAccountModelCatalogRow(row map[string]any) {
	if scope, _ := row["scope"].(string); scope == "builtin" {
		decorateBuiltinCatalogRow(row)
		return
	}
	decorateCustomCatalogRow(row)
}

// chainCatalogHybridProviderCode / chainCatalogOpenAICompatibleProviderCode
// mirror the providerProtocol.ts tokens；协议子供应商源扩展（原
// chainCatalogProtocolPairs + provider-definition ceiling）已随
// providers/provider_protocol_profiles 退役拆除，常量仅供 openai 自身内置
// 目录剔除与 hybrid 身份保留判定使用。
const (
	chainCatalogHybridProviderCode           = "hybrid"
	chainCatalogOpenAICompatibleProviderCode = "openai"
)

// sourceVendorCodes 归一化目录维度入参（供应商解耦设计 §3：vendor_code 即
// 目录归属维度；providers/provider_protocol_profiles 协议子供应商聚合随两表
// 退役拆除，无 hybrid/openai 源扩展，空入参返回空源）。
func sourceVendorCodes(providerCode string) []string {
	normalized := chainNormalizeProviderToken(providerCode)
	if normalized == "" {
		return []string{}
	}
	return []string{normalized}
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
// plus the vendor_code window over models.scope='builtin', ordered like the
// Node read（catalog_visible 在 PostgreSQL 为 boolean、SQLite 为 integer，
// 谓词按方言生成）。
func (s *chainCatalogSource) builtinCatalogQuery(input gatewayruntimecache.ModelCatalogListOptions, now string, codes []string) (string, []any) {
	availability := ""
	if !input.IncludeInactive {
		visible := "CAST(catalog_visible AS integer) = 1"
		if s.postgres {
			visible = "catalog_visible = TRUE"
		}
		availability = " AND status = 'active' AND " + visible + " AND (shutdown_date IS NULL OR trim(shutdown_date) = '' OR shutdown_date > ?) "
	}
	base := fmt.Sprintf(`SELECT %s FROM %s`, catalogColumnList(chainCatalogColumns), s.table("models"))
	query := base + " WHERE scope = 'builtin' AND vendor_code IN (" + chainCatalogPlaceholders(len(codes)) + ") " + availability + " ORDER BY provider_code, catalog_order, public_model, id"
	args := chainCatalogCodeArgs(codes)
	if availability != "" {
		args = append(args, now)
	}
	return query, args
}

// customCatalogQuery mirrors listCustomProviderModelsForCatalog: the
// global/personal scope window over models across the source vendor codes
// （owner 可见性按 system_account_id）。The model ordering is dual-dialect like
// the Node read: SQLite orders by `public_model COLLATE NOCASE`, PostgreSQL
// has no such collation and orders by `lower(public_model)`.
func (s *chainCatalogSource) customCatalogQuery(input gatewayruntimecache.ModelCatalogListOptions, now string, codes []string) (string, []any) {
	clauses := []string{"vendor_code IN (" + chainCatalogPlaceholders(len(codes)) + ")"}
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
		catalogColumnList(chainCatalogColumns), s.table("models"), strings.Join(clauses, " AND "), s.modelOrderExpression())
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

// catalogEndpointModeLegacyProjection 是 models.supported_endpoint_modes_json
// 元素到旧目录行 (mode, supportedApiProtocols) 投影的读侧逆映射（与
// maintenance modelSeedProtocolEndpointModes 的正向投影互为镜像；改动必须两处
// 同步）。generate_content_sse 正向来自 generate_content 与
// stream_generate_content 两个旧协议声明，反推取两 token 并集。
var catalogEndpointModeLegacyProjection = map[string][][2]string{
	"chat_json":                {{"chat", "chat_completions"}},
	"chat_sse":                 {{"chat", "chat_completions"}},
	"messages_json":            {{"chat", "messages"}},
	"messages_sse":             {{"chat", "messages"}},
	"responses_json":           {{"responses", "responses"}},
	"responses_sse":            {{"responses", "responses"}},
	"generate_content_json":    {{"chat", "generate_content"}},
	"generate_content_sse":     {{"chat", "generate_content"}, {"chat", "stream_generate_content"}},
	"interactions_json":        {{"chat", "interactions"}},
	"interactions_sse":         {{"chat", "interactions"}},
	"images_json":              {{"image_generation", "images"}},
	"video_create":             {{"video", "video"}},
	"video_get":                {{"video", "video"}},
	"video_content":            {{"video", "video"}},
	"video_cancel":             {{"video", "video"}},
	"audio_speech":             {{"audio", "audio_speech"}},
	"audio_transcription_json": {{"audio", "audio_transcription"}},
	"realtime_session":         {{"audio", "realtime"}},
	"message_token_counting":   {{"embedding", "message_token_counting"}},
	"count_tokens":             {{"embedding", "count_tokens"}},
	"embed_content":            {{"embedding", "embed_content"}},
}

// catalogDeriveLegacyModeAndProtocols 从 supportedEndpointModes 行键反推旧目
// 录行的 mode 与 supportedApiProtocols 投影：mode 取词表最高优先命中（video >
// audio > image_generation > embedding > responses > chat，与旧 seed 行的
// mode 值域一致），协议 token 去重升序；空 mode 集（draft/disabled 行）不注入
// mode 键（旧行为 NULL→absent），协议投影保持空数组。
func catalogDeriveLegacyModeAndProtocols(row map[string]any) {
	modeRank := map[string]int{"chat": 0, "responses": 1, "embedding": 2, "image_generation": 3, "audio": 4, "video": 5}
	seenProtocols := map[string]bool{}
	protocols := []string{}
	bestMode := ""
	bestRank := -1
	for _, mode := range catalogStringList(row["supportedEndpointModes"]) {
		for _, pair := range catalogEndpointModeLegacyProjection[mode] {
			if rank := modeRank[pair[0]]; rank > bestRank {
				bestRank = rank
				bestMode = pair[0]
			}
			if !seenProtocols[pair[1]] {
				seenProtocols[pair[1]] = true
				protocols = append(protocols, pair[1])
			}
		}
	}
	sort.Strings(protocols)
	row["supportedApiProtocols"] = protocols
	if bestRank >= 0 {
		row["mode"] = bestMode
	} else {
		delete(row, "mode")
	}
}

// catalogStringList 读取行内的字符串数组键（JSON 二次解码后的 []any）。
func catalogStringList(value any) []string {
	list, _ := value.([]any)
	out := make([]string, 0, len(list))
	for _, item := range list {
		if text, ok := item.(string); ok {
			out = append(out, text)
		}
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

// chainInheritCustomCatalogCapabilities 应用 custom 目录行能力继承（BUG-0229，
// 契约见 AI问答设计 8.6、工具体系设计 6.4）：合并胜出的 custom（global/personal）
// 行按 chainMergeCatalogItems 的同一合并键（hybrid 下 provider+model，其余裸 model）
// 查找内置扫描行（已过 decorateBuiltinStaticDerivedCapabilities 静态兜底），
// 仅填空 supportedToolsByProtocol / inputModalities / outputModalities（二维矩阵
// 继承）；一维 supportedTools 投影在二维继承后按并集回填（custom 行入链时一维
// 恒空）；全新自定义模型（内置无对应行）保持空，不退回静态定价表别名/前缀匹配。
// 回填发生在缓存写入前，缓存命中路径同样携带继承值；解析本体与管理面同源
// （providers.InheritBuiltinCatalogCapabilities）。
func chainInheritCustomCatalogCapabilities(merged, builtinRows []gatewayruntimecache.ProviderModelCatalogItem, preserveProviderIdentity bool) {
	if len(merged) == 0 || len(builtinRows) == 0 {
		return
	}
	type key struct {
		provider string
		model    string
	}
	builtinByKey := make(map[key]gatewayruntimecache.ProviderModelCatalogItem, len(builtinRows))
	for _, item := range builtinRows {
		model := strings.TrimSpace(item.Model)
		if model == "" {
			continue
		}
		itemKey := key{model: model}
		if preserveProviderIdentity {
			itemKey.provider = chainNormalizeProviderToken(item.ProviderCode)
		}
		// 同优先级内置行后行胜出，与 chainMergeCatalogItems 的决胜语义一致。
		builtinByKey[itemKey] = item
	}
	for index := range merged {
		item := &merged[index]
		if item.Scope != "global" && item.Scope != "personal" {
			continue
		}
		model := strings.TrimSpace(item.Model)
		if model == "" {
			continue
		}
		itemKey := key{model: model}
		if preserveProviderIdentity {
			itemKey.provider = chainNormalizeProviderToken(item.ProviderCode)
		}
		builtin, ok := builtinByKey[itemKey]
		if !ok {
			continue
		}
		resolved := providers.InheritBuiltinCatalogCapabilities(
			providers.CustomCatalogCapabilityKeys{
				SupportedToolsByProtocol: item.SupportedToolsByProtocol,
				InputModalities:          item.InputModalities,
				OutputModalities:         item.OutputModalities,
			},
			providers.CustomCatalogCapabilityKeys{
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

// chainIsSupportedCatalogModel mirrors isSupportedCatalogModel. M1 裁决（音频
// 设计 §2）：mode=audio（含别名）且协议为 audio_speech / audio_transcription
// 的模型收录（名字 token 排除不再生效）；旧词表的单 audio 协议继续排除；
// mode 标注 audio 但协议集里没有音频协议的行维持排除；mode 未标注 audio
// 的模型继续按名字 token 排除。M2 裁决（媒体设计 §2）同构放开 video：
// mode=video 且协议含 video 的模型收录。M5b 裁决（Realtime 设计 §6）同构
// 放开 realtime：mode=audio 且协议含 realtime 的目录行收录（显式标注行
// 不受名字 token 排除，沿 M1 裁决）；mode 未标注的行 realtime 协议维持
// 排除（词表放行只对显式标注行生效）。其余维持排除。与
// internal/providers/catalog.go 的 isSupportedCatalogModel 同源，改动必须两处同步。
func chainIsSupportedCatalogModel(item gatewayruntimecache.ProviderModelCatalogItem) bool {
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
	// 媒体单价维度（TTS 字符 / 音频秒 / 视频秒 / 视频按次）不在
	// provider_model_catalog 列集内，唯一权威源是 go:embed 静态快照层
	//（catalogdata/*.json，计价落法"目录表无列、计费读静态层"）——DB 行无
	// token 价但静态层命中媒体价的目录行（volcengine seed-audio-1.0 /
	// seedance 系）不得被 priced 过滤误杀：秒计量落值后运行时计费真实读取
	// 该价（2026-10-17 维度修正批，真实复现：TTS usage 行 audioInputSeconds
	// 落值而 costUsd 恒 NULL——priced 过滤把无 token 价媒体行剔除，
	// chainCatalogPricing 的静态层补齐永远不执行）。
	if media := pricing.FindProviderModelPricing(item.ProviderCode, item.Model); media != nil {
		if media.TtsInputUsdPer1MChars != nil || media.AudioInputUsdPerSecond != nil ||
			media.VideoOutputUsdPerSecond != nil || media.VideoOutputUsdPerCall != nil {
			return true
		}
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
// ordering on the models 单表 public_model 列: `public_model COLLATE NOCASE`
// on SQLite, `lower(public_model)` on PostgreSQL.
func (s *chainCatalogSource) modelOrderExpression() string {
	if s.postgres {
		return "lower(public_model)"
	}
	return "public_model COLLATE NOCASE"
}

func catalogColumnList(columns [][2]string) string {
	names := make([]string, 0, len(columns))
	for _, pair := range columns {
		names = append(names, pair[0])
	}
	return strings.Join(names, ", ")
}

// decorateBuiltinCatalogRow applies the toBuiltInCatalogItem derivations the
// SQL read cannot express: scope='built_in', the legacy mode/protocols reverse
// projection, supportsServiceTier=len(supportedServiceTiers)>0 and the static
// capability fallback (decorateBuiltinStaticDerivedCapabilities).
func decorateBuiltinCatalogRow(row map[string]any) {
	row["scope"] = "built_in"
	catalogDeriveLegacyModeAndProtocols(row)
	row["supportsServiceTier"] = catalogStringListLength(row["supportedServiceTiers"]) > 0
	decorateBuiltinStaticDerivedCapabilities(row)
}

// decorateBuiltinStaticDerivedCapabilities 补上 toBuiltInCatalogItem 的静态能力
// 兜底（移植自 Node toBuiltInCatalogItem 的静态能力兜底，与管理面
// providers.ApplyBuiltInStaticDerivedFields 同源：两侧共同调用
// providers.ResolveBuiltInStaticDerivedCapabilities，见 internal/providers/catalog.go）。
// 背景：数据库目录表从 Node 时代起就没有 supported_tools / input_modalities /
// output_modalities 列，Node 在读取链用静态定价表兜底，Go 迁移只移植到了管理面，
// chat 面（AI 问答模型选项 / toolCapabilities / 模型能力详情 / 协议偏好）此前恒空，
// 导致 supportsWebSearch 恒 false、web_search 等内置工具永不注入。
// 放置点在目录装载（gatewayruntimecache 缓存写入前），缓存命中路径因此同样
// 携带兜底值。工具能力以「协议 × 工具」矩阵（supportedToolsByProtocol，6.4）
// 解析，一维 supportedTools 投影为矩阵并集（过渡字段，阶段 2 随 chat 面切换
// 删除）。custom（custom_provider_models）行不做静态表兜底（行装饰层无法
// 判定内置对应行）；其能力按 BUG-0229 契约在合并后经
// chainInheritCustomCatalogCapabilities 从被覆盖内置行继承（仅填空）。
// 行自身对这三个能力键没有声明（列清单不含它们），空值一律按静态快照填充。
func decorateBuiltinStaticDerivedCapabilities(row map[string]any) {
	resolved := providers.ResolveBuiltInStaticDerivedCapabilities(
		catalogRowString(row["providerCode"]),
		catalogRowString(row["model"]),
		catalogRowInt64Ptr(row["maxOutputTokens"]),
		catalogRowString(row["source"]))
	row["inputModalities"] = resolved.InputModalities
	row["outputModalities"] = resolved.OutputModalities
	// 「协议 × 工具」矩阵（6.4）：二维矩阵为权威数据；一维 supportedTools 为
	// 矩阵并集的过渡投影（阶段 2 随 chat 面切换删除）。两键都在缓存写入前的
	// 行装饰层注入，缓存命中路径同样携带。
	row["supportedToolsByProtocol"] = resolved.SupportedToolsByProtocol
	// GenerationParameterCapabilities 序列化为与 管理面 ModelCatalogItem 相同的
	// JSON 形状（含 step 字段），经 RawMessage 进入共享 item。
	encoded, err := json.Marshal(resolved.GenerationParameterCapabilities)
	if err != nil {
		// map[string]any 的值全是 string/float64，序列化不会失败；兜底为空对象
		// 保持键存在（与管理面恒非 nil 的语义一致）。
		encoded = []byte("{}")
	}
	row["generationParameterCapabilities"] = json.RawMessage(encoded)
	if resolved.CachedImageInputUsdPer1M != nil {
		row["cachedImageInputUsdPer1M"] = *resolved.CachedImageInputUsdPer1M
	}
	if resolved.SourcePricingCurrency != "" {
		row["sourcePricingCurrency"] = resolved.SourcePricingCurrency
	}
	if resolved.SourceExchangeRateToUsd != nil {
		row["sourceExchangeRateToUsd"] = *resolved.SourceExchangeRateToUsd
	}
	if resolved.SourceExchangeRateDate != "" {
		row["sourceExchangeRateDate"] = resolved.SourceExchangeRateDate
	}
	if resolved.SourcePricingNote != "" {
		row["sourcePricingNote"] = resolved.SourcePricingNote
	}
}

// catalogRowString reads a string row value (the scan normalization already
// renders SQL text into string).
func catalogRowString(value any) string {
	text, _ := value.(string)
	return text
}

// catalogRowInt64Ptr reads a nullable integer row value; SQLite decodes
// INTEGER as float64/int64 and PostgreSQL as int64.
func catalogRowInt64Ptr(value any) *int64 {
	switch typed := value.(type) {
	case float64:
		converted := int64(typed)
		return &converted
	case int64:
		converted := typed
		return &converted
	case int:
		converted := int64(typed)
		return &converted
	default:
		return nil
	}
}

// decorateCustomCatalogRow applies the toCustomCatalogItem derivations:
// source custom-global/custom-personal（models.source 列对 custom 行为 NULL，
// 沿旧 custom 表投影覆盖写）、legacy mode/protocols 反推与
// supportsServiceTier（supportsPromptCaching 直接读 models 同名列）。
// 能力三键不在此装饰：能力继承依赖合并键与内置扫描行，在合并后由
// chainInheritCustomCatalogCapabilities 统一回填（BUG-0229 契约，仅填空；工具
// 键为二维矩阵）。
func decorateCustomCatalogRow(row map[string]any) {
	catalogDeriveLegacyModeAndProtocols(row)
	if scope, _ := row["scope"].(string); scope == "global" {
		row["source"] = "custom-global"
	} else {
		row["source"] = "custom-personal"
	}
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
