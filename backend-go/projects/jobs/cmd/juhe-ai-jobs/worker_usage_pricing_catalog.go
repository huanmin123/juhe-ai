package main

// worker_usage_pricing_catalog.go：usagewriter 定价冻结的目录适配器（P1 修复，
// usagewriter.WithCatalog 首个生产注入）。
//
// 数据源判定（审计遗留缺口 worker_assembly「jobs 暂无 C03 catalog 适配器」）：
//   - usage-catalog 库（juhe_usage / UsageCatalogSQLitePath）只承载使用记录
//     分片目录（usage_record_shards/usage_records），没有任何定价列，不是
//     定价数据源；
//   - 正确数据源是业务库 models 单表（供应商与上游协议面重构设计 §3：
//     provider_model_catalog 与 custom_provider_models 已合并为唯一物理表），
//     内置行 scope='builtin'、运营自定义行 global/personal，均按 vendor_code
//     归属供应商；即 Node listProviderModelCatalog 的同源读取（gateway 侧实现
//     见 cmd/juhe-ai-gateway/chain_catalog.go，billing 引擎见
//     gateway/internal/pricing —— 跨 module 不可 import，本文件按其语义做
//     忠实移植，覆盖冻结路径消费面：BuildCostBreakdown 于已解析目录行）。
//
// 合并与解析语义（Node model-catalog.service.ts + findCatalogItem）：同 model
// 按 scope 优先级合并 personal(3) > global(2) > built_in(1)，模型匹配为
// trim 相等；目录读取失败时 usagewriter 侧保留确定性 fallback 快照（契约
// 降级路径，不得删）。
//
// 缓存：Node 用 24h TTL + 写路径进程内失效；jobs 只读镜像收不到失效事件，
// 改用短 TTL（60s）替代失效通道，定价新鲜度上限 1 分钟。

import (
	"context"
	"database/sql"
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/usagewriter"
)

// usagePricingCatalogCacheTTL 是目录只读缓存的有效期（见文件头注释）。
const usagePricingCatalogCacheTTL = 60 * time.Second

// usagePricingCatalogErrorTTL 是目录读取失败状态的负缓存窗口（数据库故障时
// 限制逐记录重查压力；窗口过后自动重试）。
const usagePricingCatalogErrorTTL = 10 * time.Second

// usagePricingCatalogCacheMaxEntries 是目录缓存条目上限：键空间
// （provider+systemAccountID）理论上有界，但脏数据或异常放大不可信，长驻
// 进程不得无界增长。超限整体清空——jobs 消费为单线程串行，最坏代价只是
// 一轮全量回源，不引入 LRU 复杂度。
const usagePricingCatalogCacheMaxEntries = 1024

// usagePricingCatalog 实现 usagewriter.CatalogPricing（组合根侧适配器）。
type usagePricingCatalog struct {
	db  *sql.DB
	now func() time.Time

	mu    sync.Mutex
	cache map[string]usagePricingCatalogEntry
}

type usagePricingCatalogEntry struct {
	rows      map[string]usageCatalogPricingRow
	expiresAt time.Time
	err       error
}

func newUsagePricingCatalog(db *sql.DB) *usagePricingCatalog {
	return &usagePricingCatalog{db: db, now: time.Now, cache: map[string]usagePricingCatalogEntry{}}
}

// usageCatalogModelVariants 镜像 gatewayrouting.VolcengineTTSModelAliasVariantsOf
// （跨 module 不可 import，语义与取值以 gateway 侧为权威、以契约为准）：
// volcengine TTS 占位名 doubao-tts ↔ 现役名 seed-audio-1.0 等价组（EqualFold
// 语义）；组外模型返回仅含自身的单元素列表（行为零变化）。计价解析是
// 「派发匹配等价」的第六消费点：占位名 doubao-tts 请求的用量按 seed-audio-1.0
// 目录行计费——2026-10-17 实弹取证：无此归一时 usage 冻结按目录 miss 落 0
// 成本，与接入文档「计费按归一后的 seed-audio-1.0 秒价」不符。
func usageCatalogModelVariants(model string) []string {
	trimmed := strings.TrimSpace(model)
	switch strings.ToLower(trimmed) {
	case "doubao-tts", "seed-audio-1.0":
		return []string{"doubao-tts", "seed-audio-1.0"}
	}
	return []string{model}
}

// firstUsageCatalogRow 按「精确优先 + TTS 等价变体回退」在合并目录中找行；
// 未命中返回 nil。合并视图以 public_model 为键，上游回包常用 upstream_model，
// 因此未命中 public 键时再按 upstream_model 等值匹配（trim 相等，大小写敏感，
// 与旧目录的 model 匹配语义一致）。
func firstUsageCatalogRow(rows map[string]usageCatalogPricingRow, model string) *usageCatalogPricingRow {
	for _, variant := range usageCatalogModelVariants(model) {
		trimmed := strings.TrimSpace(variant)
		if row, ok := rows[trimmed]; ok {
			return &row
		}
		for _, row := range rows {
			if strings.TrimSpace(row.upstreamModel) == trimmed {
				return &row
			}
		}
	}
	return nil
}

// ResolvePricingModel mirrors resolveUsageRecordPricingModel：按
// （provider, systemAccount）目录解析实际（upstream ?? requested）模型的
// 目录规范 model id（TTS 等价组变体回退见 usageCatalogModelVariants）；
// 未解析返回空串。
func (c *usagePricingCatalog) ResolvePricingModel(ctx context.Context, providerCode string, systemAccountID string, upstreamModel string, requestedModel string) string {
	rows, err := c.lookup(ctx, providerCode, systemAccountID)
	if err != nil || len(rows) == 0 {
		return ""
	}
	if upstreamModel != "" {
		if row := firstUsageCatalogRow(rows, upstreamModel); row != nil {
			return row.model
		}
	}
	if requestedModel != "" {
		if row := firstUsageCatalogRow(rows, requestedModel); row != nil {
			return row.model
		}
	}
	return ""
}

// BuildBreakdown mirrors buildCatalogCostBreakdownFromPricing：已解析目录行 +
// 冻结输入跑计费引擎；模型不在目录或计费规则不可满足（未知 provider、tier
// 不支持、用量无定价）时返回 nil（usagewriter 回落确定性 fallback）。
// 模型查找与 ResolvePricingModel 同用 TTS 等价变体回退。
func (c *usagePricingCatalog) BuildBreakdown(ctx context.Context, providerCode string, systemAccountID string, model string, serviceTier string, input usagewriter.UsageRecordInput) *usagewriter.CostBreakdown {
	rows, err := c.lookup(ctx, providerCode, systemAccountID)
	if err != nil || len(rows) == 0 {
		return nil
	}
	row := firstUsageCatalogRow(rows, model)
	if row == nil {
		return nil
	}
	return buildUsageCatalogCostBreakdown(*row, providerCode, serviceTier, input)
}

// lookup 返回（provider, systemAccount）目录的合并视图（trim 后 model 为键），
// 带正/负缓存。失败返回 error（调用方降级 fallback）。
func (c *usagePricingCatalog) lookup(ctx context.Context, providerCode string, systemAccountID string) (map[string]usageCatalogPricingRow, error) {
	cacheKey := strings.ToLower(strings.TrimSpace(providerCode)) + "\n" + strings.TrimSpace(systemAccountID)
	now := c.now()
	c.mu.Lock()
	if entry, ok := c.cache[cacheKey]; ok && now.Before(entry.expiresAt) {
		c.mu.Unlock()
		return entry.rows, entry.err
	}
	c.mu.Unlock()

	rows, err := c.load(ctx, providerCode, systemAccountID)
	entry := usagePricingCatalogEntry{rows: rows, err: err}
	if err != nil {
		entry.expiresAt = now.Add(usagePricingCatalogErrorTTL)
	} else {
		entry.expiresAt = now.Add(usagePricingCatalogCacheTTL)
	}
	c.mu.Lock()
	// 超限整体清空（见 usagePricingCatalogCacheMaxEntries 注释）；已存在键
	// 原地覆盖不触发清空。
	if _, ok := c.cache[cacheKey]; !ok && len(c.cache) >= usagePricingCatalogCacheMaxEntries {
		clear(c.cache)
	}
	c.cache[cacheKey] = entry
	c.mu.Unlock()
	return entry.rows, entry.err
}

// load 读取内置 + 自定义两份目录并按 scope 优先级合并
// （Node listProviderModelCatalog + catalogPriority：personal > global >
// built_in；同优先级后来者覆盖）。
func (c *usagePricingCatalog) load(ctx context.Context, providerCode string, systemAccountID string) (map[string]usageCatalogPricingRow, error) {
	today := c.now().UTC().Format("2006-01-02")
	merged := map[string]usageCatalogPricingRow{}
	builtin, err := c.loadBuiltin(ctx, providerCode, today)
	if err != nil {
		return nil, err
	}
	for _, row := range builtin {
		mergeUsageCatalogRow(merged, row, "built_in")
	}
	custom, err := c.loadCustom(ctx, providerCode, systemAccountID, today)
	if err != nil {
		return nil, err
	}
	for _, row := range custom {
		mergeUsageCatalogRow(merged, row, row.scope)
	}
	return merged, nil
}

// mergeUsageCatalogRow mirrors Node mergeModelCatalogItems + catalogPriority：
// personal(3) > global(2) > built_in(1)，同优先级后来者覆盖。
func mergeUsageCatalogRow(merged map[string]usageCatalogPricingRow, row usageCatalogPricingRow, scope string) {
	key := strings.TrimSpace(row.model)
	if key == "" {
		return
	}
	row.scope = scope
	if existing, ok := merged[key]; ok && usageCatalogScopePriority(existing.scope) > usageCatalogScopePriority(scope) {
		return
	}
	merged[key] = row
}

func (c *usagePricingCatalog) table(name string) string {
	return "juhe_business." + name
}

func (c *usagePricingCatalog) bind(query string) string {
	var out strings.Builder
	index := 1
	for i := 0; i < len(query); i++ {
		if query[i] == '?' {
			out.WriteString("$" + strconv.Itoa(index))
			index++
			continue
		}
		out.WriteByte(query[i])
	}
	return out.String()
}

// usageCatalogPricingColumns 是 models 单表的定价读取列（供应商与上游协议面
// 重构设计 §3：内置目录与自定义模型同表，列集以 pg_schema_business_tables.go
// 的 models 定义为准）。模型身份为 public_model，upstream_model 用于上游回包
// 名称回退匹配；mode 由 supported_endpoint_modes_json 反推（本文件只消费
// image_generation 分支）。
var usageCatalogPricingColumns = strings.Join([]string{
	"public_model", "upstream_model", "supported_endpoint_modes_json",
	"supported_service_tiers_json", "service_tier_prices_json",
	"input_usd_per_1m", "output_usd_per_1m", "cached_input_usd_per_1m",
	"cache_write_usd_per_1m", "cache_write_1h_usd_per_1m", "cache_storage_usd_per_1m_per_hour",
	"image_input_usd_per_1m", "image_output_usd_per_1m",
	"audio_input_usd_per_1m", "audio_output_usd_per_1m", "output_usd_per_image",
	"long_context_input_token_threshold", "long_context_input_token_threshold_inclusive",
	"long_context_input_cost_multiplier", "long_context_output_cost_multiplier",
}, ", ")

func (c *usagePricingCatalog) loadBuiltin(ctx context.Context, providerCode string, today string) ([]usageCatalogPricingRow, error) {
	// 镜像 chain_catalog.go builtinCatalogQuery / Node listBuiltInProviderModels：
	// scope='builtin' + active + 目录可见 + 未下线；顺序 catalog_order,
	// public_model, id。catalog_visible 在 PostgreSQL 为 boolean，谓词恒按
	// PG 方言生成。
	visible := "catalog_visible = TRUE"
	query := c.bind(`SELECT ` + usageCatalogPricingColumns + `, '' AS scope FROM ` + c.table("models") + `
		WHERE scope = 'builtin' AND vendor_code = ? AND status = 'active'
		  AND ` + visible + `
		  AND (shutdown_date IS NULL OR trim(shutdown_date) = '' OR shutdown_date > ?)
		ORDER BY catalog_order, public_model, id`)
	return c.scanRows(ctx, query, providerCode, today)
}

func (c *usagePricingCatalog) loadCustom(ctx context.Context, providerCode string, systemAccountID string, today string) ([]usageCatalogPricingRow, error) {
	// 镜像 chain_catalog.go customCatalogQuery / Node listCustomProviderModelsForCatalog：
	// global 行（system_account_id IS NULL）+ personal 行（本 system account）。
	query := c.bind(`SELECT ` + usageCatalogPricingColumns + `, scope FROM ` + c.table("models") + `
		WHERE scope IN ('global', 'personal') AND vendor_code = ? AND status = 'active'
		  AND (shutdown_date IS NULL OR trim(shutdown_date) = '' OR shutdown_date > ?)
		  AND ((scope = 'global' AND system_account_id IS NULL) OR (scope = 'personal' AND system_account_id = ?))
		ORDER BY id`)
	return c.scanRows(ctx, query, providerCode, today, strings.TrimSpace(systemAccountID))
}

// scanRows 扫描目录行（内置与自定义同列集，均携带长上下文 4 列）。
func (c *usagePricingCatalog) scanRows(ctx context.Context, query string, args ...any) ([]usageCatalogPricingRow, error) {
	rows, err := c.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []usageCatalogPricingRow{}
	for rows.Next() {
		var (
			row                usageCatalogPricingRow
			endpointModesJSON  sql.NullString
			supportedTiersJSON sql.NullString
			tierPricesJSON     sql.NullString
			scope              sql.NullString
			longContext        usageCatalogLongContextScan
		)
		dest := []any{
			&row.model, &row.upstreamModel, &endpointModesJSON,
			&supportedTiersJSON, &tierPricesJSON,
			&row.inputUsdPer1M, &row.outputUsdPer1M, &row.cachedInputUsdPer1M,
			&row.cacheWriteUsdPer1M, &row.cacheWrite1hUsdPer1M, &row.cacheStorageUsdPer1MPerHour,
			&row.imageInputUsdPer1M, &row.imageOutputUsdPer1M,
			&row.audioInputUsdPer1M, &row.audioOutputUsdPer1M, &row.outputUsdPerImage,
			&longContext.threshold, &longContext.thresholdInclusive,
			&longContext.inputMultiplier, &longContext.outputMultiplier,
			&scope,
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		row.mode = usageCatalogModeFromEndpointModes(endpointModesJSON.String)
		row.supportedServiceTiers = decodeUsageCatalogStringList(supportedTiersJSON)
		row.serviceTierPrices = decodeUsageCatalogTierPrices(tierPricesJSON)
		if longContext.threshold.Valid {
			value := longContext.threshold.Int64
			row.longContextInputTokenThreshold = &value
		}
		row.longContextInputTokenThresholdInclusive = longContext.thresholdInclusive.Valid && longContext.thresholdInclusive.Bool
		if longContext.inputMultiplier.Valid {
			value := longContext.inputMultiplier.Float64
			row.longContextInputCostMultiplier = &value
		}
		if longContext.outputMultiplier.Valid {
			value := longContext.outputMultiplier.Float64
			row.longContextOutputCostMultiplier = &value
		}
		row.scope = scope.String
		out = append(out, row)
	}
	return out, rows.Err()
}

// usageCatalogModeFromEndpointModes 从 models.supported_endpoint_modes_json 反推
// 旧目录行 mode（与 gateway chain_catalog.go catalogDeriveLegacyModeAndProtocols
// 同优先级：video > audio > image_generation > embedding > responses > chat）。
// 本文件只消费 image_generation 分支（按张/图片 token 计价），其余 mode 仅供
// 行结构完整。
func usageCatalogModeFromEndpointModes(raw string) string {
	var modes []string
	if strings.TrimSpace(raw) == "" || json.Unmarshal([]byte(raw), &modes) != nil {
		return ""
	}
	has := func(values ...string) bool {
		for _, mode := range modes {
			for _, value := range values {
				if mode == value {
					return true
				}
			}
		}
		return false
	}
	switch {
	case has("video_create", "video_get", "video_content", "video_cancel"):
		return "video"
	case has("audio_speech", "audio_transcription_json", "realtime_session"):
		return "audio"
	case has("images_json"):
		return "image_generation"
	case has("message_token_counting", "count_tokens", "embed_content"):
		return "embedding"
	case has("responses_json", "responses_sse"):
		return "responses"
	default:
		return "chat"
	}
}

// usageCatalogLongContextScan 聚合 models 单表长上下文 4 列的可空扫描目标
// （内置与自定义行同列集）。
type usageCatalogLongContextScan struct {
	threshold          sql.NullInt64
	thresholdInclusive sql.NullBool
	inputMultiplier    sql.NullFloat64
	outputMultiplier   sql.NullFloat64
}

func decodeUsageCatalogStringList(raw sql.NullString) []string {
	if !raw.Valid || strings.TrimSpace(raw.String) == "" {
		return nil
	}
	var decoded []string
	if json.Unmarshal([]byte(raw.String), &decoded) != nil {
		return nil
	}
	return decoded
}

func decodeUsageCatalogTierPrices(raw sql.NullString) map[string]usageCatalogPriceSet {
	if !raw.Valid || strings.TrimSpace(raw.String) == "" {
		return nil
	}
	var decoded map[string]usageCatalogPriceSet
	if json.Unmarshal([]byte(raw.String), &decoded) != nil || len(decoded) == 0 {
		return nil
	}
	return decoded
}

// ---- 目录行形状与计费引擎（gateway/internal/pricing 忠实移植，仅覆盖
// 冻结路径消费面 BuildCostBreakdown；display/candidate 机器不在端口面内）----

// usageCatalogPriceSet mirrors pricing.PriceSet（per-1M 美元单价，nil=未定价）。
type usageCatalogPriceSet struct {
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

// usageCatalogPricingRow mirrors pricing.Pricing 的目录行消费面。
type usageCatalogPricingRow struct {
	model                                   string
	upstreamModel                           string
	mode                                    string
	scope                                   string
	supportedServiceTiers                   []string
	serviceTierPrices                       map[string]usageCatalogPriceSet
	inputUsdPer1M                           *float64
	outputUsdPer1M                          *float64
	cachedInputUsdPer1M                     *float64
	cacheWriteUsdPer1M                      *float64
	cacheWrite1hUsdPer1M                    *float64
	cacheStorageUsdPer1MPerHour             *float64
	imageInputUsdPer1M                      *float64
	imageOutputUsdPer1M                     *float64
	audioInputUsdPer1M                      *float64
	audioOutputUsdPer1M                     *float64
	outputUsdPerImage                       *float64
	longContextInputTokenThreshold          *int64
	longContextInputTokenThresholdInclusive bool
	longContextInputCostMultiplier          *float64
	longContextOutputCostMultiplier         *float64
}

func (r usageCatalogPricingRow) priceSet() usageCatalogPriceSet {
	return usageCatalogPriceSet{
		InputUsdPer1M:               r.inputUsdPer1M,
		OutputUsdPer1M:              r.outputUsdPer1M,
		CachedInputUsdPer1M:         r.cachedInputUsdPer1M,
		CacheWriteUsdPer1M:          r.cacheWriteUsdPer1M,
		CacheWrite1hUsdPer1M:        r.cacheWrite1hUsdPer1M,
		CacheStorageUsdPer1MPerHour: r.cacheStorageUsdPer1MPerHour,
		ImageInputUsdPer1M:          r.imageInputUsdPer1M,
		ImageOutputUsdPer1M:         r.imageOutputUsdPer1M,
		AudioInputUsdPer1M:          r.audioInputUsdPer1M,
		AudioOutputUsdPer1M:         r.audioOutputUsdPer1M,
		OutputUsdPerImage:           r.outputUsdPerImage,
	}
}

// usageCatalogScopePriority mirrors Node catalogPriority.
func usageCatalogScopePriority(scope string) int {
	switch scope {
	case "personal":
		return 3
	case "global":
		return 2
	}
	return 1
}

// usageCatalogBillingPolicy mirrors pricing.billingPolicies（六家 provider 的
// buildCostBreakdown 差异：长上下文乘数、tier 拒绝、缓存读口径与 cache 写
// 标签；其余 token 标签与默认表一致）。
type usageCatalogBillingPolicy struct {
	id                       string
	applyLongContext         bool
	rejectUnsupportedTier    bool
	cacheReadIncludedInInput bool
	cacheReadFallbackToInput bool
	cacheWriteLabel          string
}

func usageCatalogBillingPolicyFor(providerCode string) *usageCatalogBillingPolicy {
	switch strings.ToLower(strings.TrimSpace(providerCode)) {
	case "openai", "gpt":
		return &usageCatalogBillingPolicy{id: "openai", applyLongContext: true, cacheReadIncludedInInput: true, cacheReadFallbackToInput: true, cacheWriteLabel: "缓存写入 Token"}
	case "anthropic":
		return &usageCatalogBillingPolicy{id: "anthropic", rejectUnsupportedTier: true, cacheWriteLabel: "5m 缓存写入 Token"}
	case "gemini":
		return &usageCatalogBillingPolicy{id: "gemini", applyLongContext: true, cacheReadIncludedInInput: true, cacheWriteLabel: "缓存写入 Token"}
	case "xai":
		return &usageCatalogBillingPolicy{id: "xai", applyLongContext: true, cacheReadIncludedInInput: true, cacheWriteLabel: "缓存写入 Token"}
	case "deepseek":
		return &usageCatalogBillingPolicy{id: "deepseek", rejectUnsupportedTier: true, cacheReadIncludedInInput: true, cacheWriteLabel: "缓存写入 Token"}
	case "glm":
		return &usageCatalogBillingPolicy{id: "glm", rejectUnsupportedTier: true, cacheReadIncludedInInput: true, cacheWriteLabel: "缓存写入 Token"}
	}
	return nil
}

// usageCatalogRates mirrors pricing.resolvedRates.
type usageCatalogRates struct {
	set    usageCatalogPriceSet
	source string
	multi  *float64
}

func usageCatalogStandardRates(row usageCatalogPricingRow) usageCatalogRates {
	return usageCatalogRates{set: directUsageCatalogRates(row.priceSet()), source: usagewriter.TierSourceDefault}
}

// usageCatalogTierRates mirrors pricing.serviceTierRates：tier 表持有 token
// 费率（无标准费率回退），image/audio/每图费率回退标准档位；tier 不在
// supported 列表或无费率表时 source=unknown 且费率集为空（后续按无费率
// 收敛为 nil breakdown）。
func usageCatalogTierRates(row usageCatalogPricingRow, serviceTier string) usageCatalogRates {
	tier := normalizedUsageCatalogTier(serviceTier)
	if tier == "" {
		return usageCatalogStandardRates(row)
	}
	supported := false
	for _, candidate := range row.supportedServiceTiers {
		if candidate == tier {
			supported = true
			break
		}
	}
	if !supported {
		return usageCatalogRates{source: usagewriter.TierSourceUnknown}
	}
	tierRates, ok := row.serviceTierPrices[tier]
	if !ok {
		return usageCatalogRates{source: usagewriter.TierSourceUnknown}
	}
	resolved := directUsageCatalogRates(tierRates)
	if resolved.ImageInputUsdPer1M == nil {
		resolved.ImageInputUsdPer1M = finiteUsageCatalogRate(row.imageInputUsdPer1M)
	}
	if resolved.ImageOutputUsdPer1M == nil {
		resolved.ImageOutputUsdPer1M = finiteUsageCatalogRate(row.imageOutputUsdPer1M)
	}
	if resolved.AudioInputUsdPer1M == nil {
		resolved.AudioInputUsdPer1M = finiteUsageCatalogRate(row.audioInputUsdPer1M)
	}
	if resolved.AudioOutputUsdPer1M == nil {
		resolved.AudioOutputUsdPer1M = finiteUsageCatalogRate(row.audioOutputUsdPer1M)
	}
	if resolved.OutputUsdPerImage == nil {
		resolved.OutputUsdPerImage = finiteUsageCatalogRate(row.outputUsdPerImage)
	}
	source, multi := usageCatalogTierMetadata(row.priceSet(), tierRates)
	return usageCatalogRates{set: resolved, source: source, multi: multi}
}

// usageCatalogApplyLongContext mirrors pricing.applyLongContextRates：仅改写
// 五个 token 费率，tier source 与乘数原样透传。
func usageCatalogApplyLongContext(row usageCatalogPricingRow, inputTokens *int, rates usageCatalogRates) usageCatalogRates {
	if row.longContextInputTokenThreshold == nil {
		return rates
	}
	tokens := nonNegativeUsageCatalogFloat(inputTokens)
	applies := tokens >= float64(*row.longContextInputTokenThreshold)
	if !row.longContextInputTokenThresholdInclusive {
		applies = tokens > float64(*row.longContextInputTokenThreshold)
	}
	if !applies {
		return rates
	}
	inputMultiplier := validUsageCatalogMultiplier(row.longContextInputCostMultiplier)
	outputMultiplier := validUsageCatalogMultiplier(row.longContextOutputCostMultiplier)
	rates.set.InputUsdPer1M = multiplyUsageCatalogRate(rates.set.InputUsdPer1M, inputMultiplier)
	rates.set.CachedInputUsdPer1M = multiplyUsageCatalogRate(rates.set.CachedInputUsdPer1M, inputMultiplier)
	rates.set.CacheWriteUsdPer1M = multiplyUsageCatalogRate(rates.set.CacheWriteUsdPer1M, inputMultiplier)
	rates.set.CacheWrite1hUsdPer1M = multiplyUsageCatalogRate(rates.set.CacheWrite1hUsdPer1M, inputMultiplier)
	rates.set.OutputUsdPer1M = multiplyUsageCatalogRate(rates.set.OutputUsdPer1M, outputMultiplier)
	return rates
}

// buildUsageCatalogCostBreakdown mirrors pricing.BuildCostBreakdown 于目录行：
// 计费策略按 provider 选择（目录行归属 provider），输出加 USD 币种与策略
// 戳；nil=Node undefined（未知 provider、tier 不支持或用量无定价）。
func buildUsageCatalogCostBreakdown(row usageCatalogPricingRow, providerCode string, serviceTier string, input usagewriter.UsageRecordInput) *usagewriter.CostBreakdown {
	policy := usageCatalogBillingPolicyFor(providerCode)
	if policy == nil {
		return nil
	}
	breakdown := policy.buildUsageCatalogBreakdown(row, serviceTier, input)
	if breakdown == nil {
		return nil
	}
	breakdown.Currency = "USD"
	breakdown.BillingPolicy = policy.id
	return breakdown
}

func (p *usageCatalogBillingPolicy) buildUsageCatalogBreakdown(row usageCatalogPricingRow, serviceTier string, input usagewriter.UsageRecordInput) *usagewriter.CostBreakdown {
	if p.rejectUnsupportedTier && usageCatalogHasUnsupportedTier(row, serviceTier) {
		return nil
	}
	rates := usageCatalogTierRates(row, serviceTier)
	if p.applyLongContext {
		rates = usageCatalogApplyLongContext(row, input.InputTokens, rates)
	}
	outputImageTokens := input.OutputImageTokens
	if row.mode == "image_generation" &&
		rates.set.ImageOutputUsdPer1M != nil && outputImageTokens == nil {
		// OpenAI image policy：文本输出流按图片输出 token 计费（调用方未拆分时）。
		outputImageTokens = input.OutputTokens
	}
	if row.mode == "image_generation" &&
		rates.set.OutputUsdPerImage != nil &&
		rates.set.OutputUsdPer1M == nil && rates.set.ImageOutputUsdPer1M == nil {
		// 按张计价策略（seedream/image-01/cogview/grok-imagine-image 族）：上游
		// usage.output_tokens 是图像内部 token 核算，行上无独立文本输出 token
		// 价——置零避免 hasUnpricedUsage 拒绝整单计费（与 gateway pricing 引擎
		// 同一策略镜像；2026-10-17 实弹取证回填）。
		input.OutputTokens = nil
	}
	return buildUsageCatalogTokenBreakdown(row, input, rates, outputImageTokens, p)
}

// buildUsageCatalogTokenBreakdown mirrors pricing.buildTokenCostBreakdown。
func buildUsageCatalogTokenBreakdown(row usageCatalogPricingRow, input usagewriter.UsageRecordInput, rates usageCatalogRates, outputImageTokens *int, policy *usageCatalogBillingPolicy) *usagewriter.CostBreakdown {
	if !usageCatalogHasAnyRate(rates.set) {
		return nil
	}
	cacheReadTokens := nonNegativeUsageCatalogFloat(input.CacheReadTokens)
	cacheWriteTokens := nonNegativeUsageCatalogFloat(input.CacheWriteTokens)
	cacheWrite1hTotal := cacheWriteTokens
	if cacheWrite1hTotal == 0 {
		cacheWrite1hTotal = nonNegativeUsageCatalogFloat(input.CacheWrite1hTokens)
	}
	cacheWrite1hTokens := math.Min(nonNegativeUsageCatalogFloat(input.CacheWrite1hTokens), cacheWrite1hTotal)
	cacheWriteStandardTokens := math.Max(cacheWriteTokens-cacheWrite1hTokens, 0)
	inputImageTokens := 0.0
	if rates.set.ImageInputUsdPer1M != nil {
		inputImageTokens = nonNegativeUsageCatalogFloat(input.InputImageTokens)
	}
	outputImageTokenCount := 0.0
	if rates.set.ImageOutputUsdPer1M != nil {
		outputImageTokenCount = nonNegativeUsageCatalogFloat(outputImageTokens)
	}
	inputAudioTokens := 0.0
	if rates.set.AudioInputUsdPer1M != nil {
		inputAudioTokens = nonNegativeUsageCatalogFloat(input.InputAudioTokens)
	}
	outputAudioTokens := 0.0
	if rates.set.AudioOutputUsdPer1M != nil {
		outputAudioTokens = nonNegativeUsageCatalogFloat(input.OutputAudioTokens)
	}
	outputImageCount := 0.0
	if rates.set.OutputUsdPerImage != nil {
		outputImageCount = nonNegativeUsageCatalogFloat(input.OutputImageCount)
	}
	if nonNegativeUsageCatalogFloat(input.OutputImageCount) > 0 && rates.set.OutputUsdPerImage == nil {
		return nil
	}
	cacheReadRate := rates.set.CachedInputUsdPer1M
	if cacheReadRate == nil && policy.cacheReadFallbackToInput {
		cacheReadRate = rates.set.InputUsdPer1M
	}
	includedCacheRead := 0.0
	if policy.cacheReadIncludedInInput {
		includedCacheRead = cacheReadTokens
	}
	uncachedInputTokens := math.Max(
		nonNegativeUsageCatalogFloat(input.InputTokens)-
			includedCacheRead-
			inputImageTokens-
			inputAudioTokens,
		0)
	outputTokens := math.Max(nonNegativeUsageCatalogFloat(input.OutputTokens)-outputImageTokenCount-outputAudioTokens, 0)

	if usageCatalogHasUnpricedUsage(usageCatalogUnpricedUsageInput{
		uncachedInputTokens: uncachedInputTokens,
		inputRate:           rates.set.InputUsdPer1M,
		outputTokens:        outputTokens,
		outputRate:          rates.set.OutputUsdPer1M,
		cacheReadTokens:     cacheReadTokens,
		cacheReadRate:       cacheReadRate,
		cacheWriteStandard:  cacheWriteStandardTokens,
		cacheWriteRate:      rates.set.CacheWriteUsdPer1M,
		cacheWrite1hTokens:  cacheWrite1hTokens,
		cacheWrite1hRate:    orUsageCatalogRate(rates.set.CacheWrite1hUsdPer1M, rates.set.CacheWriteUsdPer1M),
	}) {
		return nil
	}

	var lines []usagewriter.CostLineItem
	lines = addUsageCatalogTokenLine(lines, "input", "输入 Token", uncachedInputTokens, rates.set.InputUsdPer1M)
	lines = addUsageCatalogTokenLine(lines, "output", "输出 Token", outputTokens, rates.set.OutputUsdPer1M)
	lines = addUsageCatalogTokenLine(lines, "cache_read", "缓存读 Token", cacheReadTokens, cacheReadRate)
	lines = addUsageCatalogTokenLine(lines, "cache_write", policy.cacheWriteLabel, cacheWriteStandardTokens, rates.set.CacheWriteUsdPer1M)
	lines = addUsageCatalogTokenLine(lines, "cache_write_1h", "1h 缓存写入 Token", cacheWrite1hTokens, orUsageCatalogRate(rates.set.CacheWrite1hUsdPer1M, rates.set.CacheWriteUsdPer1M))
	lines = addUsageCatalogTokenLine(lines, "image_input", "图片输入 Token", inputImageTokens, rates.set.ImageInputUsdPer1M)
	lines = addUsageCatalogTokenLine(lines, "image_output", "图片输出 Token", outputImageTokenCount, rates.set.ImageOutputUsdPer1M)
	lines = addUsageCatalogTokenLine(lines, "audio_input", "音频输入 Token", inputAudioTokens, rates.set.AudioInputUsdPer1M)
	lines = addUsageCatalogTokenLine(lines, "audio_output", "音频输出 Token", outputAudioTokens, rates.set.AudioOutputUsdPer1M)
	lines = addUsageCatalogUnitLine(lines, "image_output_unit", "输出图片", outputImageCount, "image", rates.set.OutputUsdPerImage)
	return usageCatalogBreakdownFromLines(lines, input, rates)
}

// usageCatalogBreakdownFromLines mirrors pricing.legacyBreakdownFromLines：
// 平铺字段从行项读回，账户费用为 costUsd 覆盖或行项合计。
func usageCatalogBreakdownFromLines(lines []usagewriter.CostLineItem, input usagewriter.UsageRecordInput, rates usageCatalogRates) *usagewriter.CostBreakdown {
	line := func(kind string) *usagewriter.CostLineItem {
		for index := range lines {
			if lines[index].Kind == kind {
				return &lines[index]
			}
		}
		return nil
	}
	cost := func(kind string) *float64 {
		if item := line(kind); item != nil {
			value := item.CostUsd
			return &value
		}
		return nil
	}
	rate := func(kind string) *float64 {
		if item := line(kind); item != nil {
			value := item.UnitPriceUsd
			return &value
		}
		return nil
	}
	calculated := 0.0
	for index := range lines {
		calculated += lines[index].CostUsd
	}
	calculated = roundUsageCatalogCost(calculated)

	return &usagewriter.CostBreakdown{
		LineItems:                lines,
		InputCostUsd:             cost("input"),
		OutputCostUsd:            cost("output"),
		InputUsdPer1M:            orUsageCatalogRate(rate("input"), rates.set.InputUsdPer1M),
		OutputUsdPer1M:           orUsageCatalogRate(rate("output"), rates.set.OutputUsdPer1M),
		CacheReadCostUsd:         cost("cache_read"),
		CacheReadUsdPer1M:        orUsageCatalogRate(rate("cache_read"), rates.set.CachedInputUsdPer1M),
		CacheWriteCostUsd:        cost("cache_write"),
		CacheWriteUsdPer1M:       orUsageCatalogRate(rate("cache_write"), rates.set.CacheWriteUsdPer1M),
		CacheWrite1hCostUsd:      cost("cache_write_1h"),
		CacheWrite1hUsdPer1M:     orUsageCatalogRate(orUsageCatalogRate(rate("cache_write_1h"), rates.set.CacheWrite1hUsdPer1M), rates.set.CacheWriteUsdPer1M),
		ThinkingTokens:           usageCatalogFloatPtr(input.ThinkingTokens),
		InputImageCostUsd:        cost("image_input"),
		OutputImageCostUsd:       cost("image_output"),
		InputImageUsdPer1M:       orUsageCatalogRate(rate("image_input"), rates.set.ImageInputUsdPer1M),
		OutputImageUsdPer1M:      orUsageCatalogRate(rate("image_output"), rates.set.ImageOutputUsdPer1M),
		InputAudioCostUsd:        cost("audio_input"),
		OutputAudioCostUsd:       cost("audio_output"),
		InputAudioUsdPer1M:       orUsageCatalogRate(rate("audio_input"), rates.set.AudioInputUsdPer1M),
		OutputAudioUsdPer1M:      orUsageCatalogRate(rate("audio_output"), rates.set.AudioOutputUsdPer1M),
		OutputImageUnitCostUsd:   cost("image_output_unit"),
		OutputUsdPerImage:        orUsageCatalogRate(rate("image_output_unit"), rates.set.OutputUsdPerImage),
		AccountChargeUsd:         orUsageCatalogRate(finiteUsageCatalogRate(input.CostUsd), &calculated),
		Multiplier:               1,
		ServiceTierPricingSource: rates.source,
		ServiceTierMultiplier:    rates.multi,
	}
}

// usageCatalogTierMetadata mirrors pricing.tierPricingMetadata.
func usageCatalogTierMetadata(standard, tier usageCatalogPriceSet) (string, *float64) {
	pairs := [][2]*float64{
		{standard.InputUsdPer1M, tier.InputUsdPer1M},
		{standard.OutputUsdPer1M, tier.OutputUsdPer1M},
		{standard.CachedInputUsdPer1M, tier.CachedInputUsdPer1M},
		{standard.CacheWriteUsdPer1M, tier.CacheWriteUsdPer1M},
		{standard.CacheWrite1hUsdPer1M, tier.CacheWrite1hUsdPer1M},
		{standard.AudioInputUsdPer1M, tier.AudioInputUsdPer1M},
		{standard.AudioOutputUsdPer1M, tier.AudioOutputUsdPer1M},
	}
	specific := 0
	missing := 0
	for _, pair := range pairs {
		switch {
		case pair[1] != nil:
			specific++
		case pair[0] != nil:
			missing++
		}
	}
	switch {
	case specific > 0 && missing == 0:
		return usagewriter.TierSourceTierSpecific, nil
	case specific > 0:
		return usagewriter.TierSourceMixed, nil
	default:
		return usagewriter.TierSourceUnknown, nil
	}
}

type usageCatalogUnpricedUsageInput struct {
	uncachedInputTokens float64
	inputRate           *float64
	outputTokens        float64
	outputRate          *float64
	cacheReadTokens     float64
	cacheReadRate       *float64
	cacheWriteStandard  float64
	cacheWriteRate      *float64
	cacheWrite1hTokens  float64
	cacheWrite1hRate    *float64
}

// usageCatalogHasUnpricedUsage mirrors pricing.hasUnpricedUsage：任一正数量
// 缺费率即收敛为 nil breakdown。
func usageCatalogHasUnpricedUsage(input usageCatalogUnpricedUsageInput) bool {
	return (input.uncachedInputTokens > 0 && input.inputRate == nil) ||
		(input.outputTokens > 0 && input.outputRate == nil) ||
		(input.cacheReadTokens > 0 && input.cacheReadRate == nil) ||
		(input.cacheWriteStandard > 0 && input.cacheWriteRate == nil) ||
		(input.cacheWrite1hTokens > 0 && input.cacheWrite1hRate == nil)
}

func addUsageCatalogTokenLine(lines []usagewriter.CostLineItem, kind string, label string, quantity float64, unitPriceUsd *float64) []usagewriter.CostLineItem {
	if quantity <= 0 || unitPriceUsd == nil {
		return lines
	}
	cost := roundUsageCatalogCost(quantity / 1_000_000 * *unitPriceUsd)
	return append(lines, usagewriter.CostLineItem{
		Key:          kind,
		Kind:         kind,
		Label:        label,
		Quantity:     quantity,
		Unit:         "token",
		UnitSize:     1_000_000,
		UnitPriceUsd: *unitPriceUsd,
		CostUsd:      cost,
	})
}

func addUsageCatalogUnitLine(lines []usagewriter.CostLineItem, kind string, label string, quantity float64, unit string, unitPriceUsd *float64) []usagewriter.CostLineItem {
	if quantity <= 0 || unitPriceUsd == nil {
		return lines
	}
	cost := roundUsageCatalogCost(quantity * *unitPriceUsd)
	return append(lines, usagewriter.CostLineItem{
		Key:          kind,
		Kind:         kind,
		Label:        label,
		Quantity:     quantity,
		Unit:         unit,
		UnitSize:     1,
		UnitPriceUsd: *unitPriceUsd,
		CostUsd:      cost,
	})
}

// ---- 标量工具（镜像 pricing 同名 helper）----

func directUsageCatalogRates(set usageCatalogPriceSet) usageCatalogPriceSet {
	return usageCatalogPriceSet{
		InputUsdPer1M:               finiteUsageCatalogRate(set.InputUsdPer1M),
		OutputUsdPer1M:              finiteUsageCatalogRate(set.OutputUsdPer1M),
		CachedInputUsdPer1M:         finiteUsageCatalogRate(set.CachedInputUsdPer1M),
		CacheWriteUsdPer1M:          finiteUsageCatalogRate(set.CacheWriteUsdPer1M),
		CacheWrite1hUsdPer1M:        finiteUsageCatalogRate(set.CacheWrite1hUsdPer1M),
		CacheStorageUsdPer1MPerHour: finiteUsageCatalogRate(set.CacheStorageUsdPer1MPerHour),
		ImageInputUsdPer1M:          finiteUsageCatalogRate(set.ImageInputUsdPer1M),
		ImageOutputUsdPer1M:         finiteUsageCatalogRate(set.ImageOutputUsdPer1M),
		AudioInputUsdPer1M:          finiteUsageCatalogRate(set.AudioInputUsdPer1M),
		AudioOutputUsdPer1M:         finiteUsageCatalogRate(set.AudioOutputUsdPer1M),
		OutputUsdPerImage:           finiteUsageCatalogRate(set.OutputUsdPerImage),
	}
}

func usageCatalogHasUnsupportedTier(row usageCatalogPricingRow, serviceTier string) bool {
	tier := normalizedUsageCatalogTier(serviceTier)
	if tier == "" {
		return false
	}
	for _, supported := range row.supportedServiceTiers {
		if supported == tier {
			return false
		}
	}
	return true
}

func usageCatalogHasAnyRate(set usageCatalogPriceSet) bool {
	return set.InputUsdPer1M != nil || set.OutputUsdPer1M != nil ||
		set.CachedInputUsdPer1M != nil || set.CacheWriteUsdPer1M != nil ||
		set.CacheWrite1hUsdPer1M != nil || set.CacheStorageUsdPer1MPerHour != nil ||
		set.ImageInputUsdPer1M != nil || set.ImageOutputUsdPer1M != nil ||
		set.AudioInputUsdPer1M != nil || set.AudioOutputUsdPer1M != nil ||
		set.OutputUsdPerImage != nil
}

func normalizedUsageCatalogTier(value string) string {
	tier := strings.TrimSpace(value)
	if tier == "" || tier == "default" || tier == "standard" {
		return ""
	}
	return tier
}

func orUsageCatalogRate(primary, fallback *float64) *float64 {
	if primary != nil {
		return primary
	}
	return fallback
}

func finiteUsageCatalogRate(value *float64) *float64 {
	if value == nil || math.IsNaN(*value) || math.IsInf(*value, 0) {
		return nil
	}
	return value
}

func nonNegativeUsageCatalogFloat(value *int) float64 {
	if value == nil {
		return 0
	}
	return math.Max(float64(*value), 0)
}

func usageCatalogFloatPtr(value *int) *float64 {
	if value == nil {
		return nil
	}
	out := float64(*value)
	return &out
}

func validUsageCatalogMultiplier(value *float64) float64 {
	if value == nil || *value < 0 || math.IsNaN(*value) || math.IsInf(*value, 0) {
		return 1
	}
	return *value
}

func multiplyUsageCatalogRate(value *float64, multiplier float64) *float64 {
	if value == nil {
		return nil
	}
	out := *value * multiplier
	return &out
}

// roundUsageCatalogCost mirrors roundCost / Number(value.toFixed(10))。
func roundUsageCatalogCost(value float64) float64 {
	out, err := strconv.ParseFloat(strconv.FormatFloat(value, 'f', 10, 64), 64)
	if err != nil {
		return value
	}
	return out
}
