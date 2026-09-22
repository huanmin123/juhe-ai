package mockdata

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/schema"
)

// seedUsage 是用量域：usage-catalog 库的分片登记 + usage-shards 分片文件里的
// usage_records 明细 + stats 库的聚合输入镜像。
//
// 范围：gateway / manual_account_test / account_health_check / cooldown_retest /
// runtime_recovery_probe 来源，OpenAI（responses / chat completions / images /
// models）与 Anthropic（messages / count_tokens）端点，成功与失败样本，图片
// token、缓存读取、模型映射命中、上游响应模型一致 / 映射后不一致 / 未映射不一致
// 三连样本、流式与非流式、服务档位与思考强度样本，时间跨度由 Options.Days 决定；
// 另按 business 域的真实账户逐小时生成 account_health_check 记录（含缺口与失败
// 小时），供 jobs 聚合出 account_health_hourly。
//
// 边界（见 docs/functions/Mockdata造数设计.md 与域骨架注释）：
//   - 只写原始事实与采样：派生表（usage_stats_*、usage_model_*、account_health_hourly、
//     *_windows、usage_rank_snapshots、group_account_stats 等）一律不写，由 jobs 的
//     聚合 / 窗口任务重建；
//   - 分片文件用 schema.EnsureSQLiteUsageShard 建表（jobs UsageShardBaseSchemaSQL 的
//     逐字副本），catalog 四表与 stats 镜像表按 jobs 的 CatalogSchemaSQL /
//     statsverify usage_records 形状按需建表后写入；
//   - 记录外键一律取 business 域运行时查询到的真实 mock 资源 ID，查不到的资源
//     字段写 NULL 并计数，不猜 ID。
func seedUsage(ctx context.Context, e *env) (result DomainResult, err error) {
	resources, err := loadUsageResources(ctx, e)
	if err != nil {
		return DomainResult{}, err
	}
	if resources.skip != "" {
		// 不静默降级：Counts 为空即「未接线」，覆盖报告据此把用量域断言标成
		// not-covered，而不是报一个看不懂的失败。
		e.logger.Info("mockdata usage 域跳过", "reason", resources.skip)
		return DomainResult{Name: DomainUsage}, nil
	}
	// 分片句柄由本域自己持有并关闭：分片文件是运行时按记录创建的新文件，
	// 不在 env 启动时枚举出的存储清单里（见 env.usageShardStores 的注释）。
	files := &usageShardFiles{root: e.options.Paths.UsageShardRoot, dbs: map[string]*sql.DB{}}
	defer func() {
		if closeErr := files.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("关闭 usage 分片句柄: %w", closeErr)
		}
	}()
	writer := &usageWriter{
		ctx:       ctx,
		e:         e,
		now:       e.options.clock(),
		resources: resources,
		files:     files,
		counts:    map[string]int{},
	}
	if err := writer.seed(); err != nil {
		return DomainResult{}, err
	}
	return DomainResult{Name: DomainUsage, Counts: writer.counts}, nil
}

// usageRecordColumns 是 usage_records 的列序（jobs usagewriter UsageRecordColumns
// 的逐字副本）。
//
// 为什么在本文件复制而不是 import：Go 三项目基线禁止 maintenance -> jobs 依赖，
// 而分片文件必须与 jobs 写出 / 读取的文件同形——列序决定行参数顺序，错一处就会
// 把流式标记写进 token 列。副本由 mockdata_domain_usage_test.go 的列名不变量
// 测试钉住，任何一侧改动都会立刻失败。
var usageRecordColumns = []string{
	"id",
	"system_account_id",
	"trace_id",
	"traffic_source",
	"client_ip",
	"api_key_id",
	"group_id",
	"account_id",
	"endpoint",
	"provider_code",
	"provider_protocol_profile_id",
	"usage_semantic",
	"model",
	"upstream_model",
	"upstream_response_model",
	"pricing_model",
	"requested_service_tier",
	"effective_service_tier",
	"reported_service_tier",
	"billed_service_tier",
	"requested_reasoning_effort",
	"effective_reasoning_effort",
	"cost_breakdown_snapshot_json",
	"model_mapping_applied",
	"model_mapping_source",
	"source_endpoint_family",
	"upstream_endpoint_family",
	"stream",
	"status_code",
	"success",
	"failure_attribution",
	"first_token_ms",
	"duration_ms",
	"input_tokens",
	"output_tokens",
	"cache_read_tokens",
	"cache_read_cost_usd",
	"cache_write_tokens",
	"cache_write_1h_tokens",
	"cache_write_cost_usd",
	"thinking_tokens",
	"input_image_tokens",
	"output_image_tokens",
	"input_audio_tokens",
	"output_audio_tokens",
	"output_image_count",
	"cost_usd",
	"error_code",
	"error_message",
	"request_snapshot_json",
	"response_snapshot_json",
	"account_owner_system_account_id",
	"group_owner_system_account_id",
	"account_access_type",
	"group_access_type",
	"account_authorization_id",
	"account_authorization_source_type",
	"account_authorization_source_team_id",
	"group_authorization_id",
	"group_authorization_source_type",
	"group_authorization_source_team_id",
	"created_at",
}

// statsUsageRecordColumns 是 stats 库 usage_records 镜像的列（jobs
// statsMirrorAggregateColumns 的逐字副本，也是 statsverify sqliteStatsSchema 的
// usage_records 形状）：SQLite standalone 模式下 statsagg 聚合器的唯一输入源。
var statsUsageRecordColumns = []string{
	"id",
	"system_account_id",
	"trace_id",
	"traffic_source",
	"client_ip",
	"api_key_id",
	"group_id",
	"account_id",
	"endpoint",
	"provider_code",
	"provider_protocol_profile_id",
	"model",
	"status_code",
	"success",
	"failure_attribution",
	"first_token_ms",
	"duration_ms",
	"input_tokens",
	"output_tokens",
	"cache_read_tokens",
	"cache_read_cost_usd",
	"cache_write_tokens",
	"cache_write_1h_tokens",
	"cache_write_cost_usd",
	"thinking_tokens",
	"input_image_tokens",
	"output_image_tokens",
	"cost_usd",
	"error_code",
	"error_message",
	"account_owner_system_account_id",
	"group_owner_system_account_id",
	"account_access_type",
	"group_access_type",
	"account_authorization_id",
	"account_authorization_source_type",
	"account_authorization_source_team_id",
	"group_authorization_id",
	"group_authorization_source_type",
	"group_authorization_source_team_id",
	"created_at",
}

// usageCatalogDDL 是 usage-catalog 库的四张分片登记表（jobs CatalogSchemaSQL 的
// 逐字副本）。jobs 侧由 writer 的 EnsureCatalogSchema 建表，maintenance 直接落库
// 时同样必须按需建表，否则在只跑过 --mockdata 的数据根上无处登记分片。
const usageCatalogDDL = `
    CREATE TABLE IF NOT EXISTS usage_record_shards (
          shard_key TEXT PRIMARY KEY,
          bucket_date TEXT NOT NULL,
          shard_id INTEGER NOT NULL,
          file_path TEXT NOT NULL,
          schema_version INTEGER NOT NULL DEFAULT 1,
          status TEXT NOT NULL DEFAULT 'active',
          first_seen_at TEXT NOT NULL,
          last_write_at TEXT,
          last_error_message TEXT,
          created_at TEXT NOT NULL,
          updated_at TEXT NOT NULL
        );
    CREATE TABLE IF NOT EXISTS usage_record_shard_entries (
          usage_id TEXT PRIMARY KEY,
          shard_key TEXT NOT NULL,
          system_account_id TEXT NOT NULL,
          trace_id TEXT NOT NULL,
          api_key_id TEXT,
          account_id TEXT,
          group_id TEXT,
          model TEXT,
          traffic_source TEXT NOT NULL,
          success INTEGER NOT NULL DEFAULT 0,
          status_code INTEGER,
          client_ip TEXT,
          first_token_ms INTEGER,
          duration_ms INTEGER,
          cost_usd REAL,
          created_at TEXT NOT NULL,
          indexed_at TEXT NOT NULL
        );
    CREATE TABLE IF NOT EXISTS usage_record_account_shards (
          account_id TEXT NOT NULL,
          shard_key TEXT NOT NULL,
          first_created_at TEXT NOT NULL,
          last_seen_at TEXT NOT NULL,
          PRIMARY KEY (account_id, shard_key)
        );
    CREATE TABLE IF NOT EXISTS usage_record_api_key_shards (
          api_key_id TEXT NOT NULL,
          system_account_id TEXT NOT NULL,
          shard_key TEXT NOT NULL,
          first_created_at TEXT NOT NULL,
          last_seen_at TEXT NOT NULL,
          PRIMARY KEY (api_key_id, system_account_id, shard_key)
        );
`

// statsUsageRecordDDL 是 stats 库 usage_records 镜像的建表语句（jobs statsverify
// sqliteStatsSchema 的 usage_records 形状）。jobs 会在启动时 ensure，maintenance
// 只跑造数时表还不存在，因此这里用同一形状按需建表；列名与
// statsUsageRecordColumns 必须一致。
const statsUsageRecordDDL = `
CREATE TABLE IF NOT EXISTS usage_records (
	id TEXT PRIMARY KEY,
	system_account_id TEXT NOT NULL,
	trace_id TEXT NOT NULL,
	traffic_source TEXT NOT NULL,
	client_ip TEXT,
	api_key_id TEXT,
	group_id TEXT,
	account_id TEXT,
	model TEXT,
	status_code INTEGER,
	success INTEGER NOT NULL,
	first_token_ms INTEGER,
	duration_ms INTEGER,
	input_tokens INTEGER,
	output_tokens INTEGER,
	cache_read_tokens INTEGER,
	cache_read_cost_usd REAL,
	cache_write_tokens INTEGER,
	cache_write_1h_tokens INTEGER,
	cache_write_cost_usd REAL,
	thinking_tokens INTEGER,
	input_image_tokens INTEGER,
	output_image_tokens INTEGER,
	endpoint TEXT,
	provider_code TEXT,
	provider_protocol_profile_id TEXT,
	failure_attribution TEXT,
	error_code TEXT,
	error_message TEXT,
	account_owner_system_account_id TEXT,
	group_owner_system_account_id TEXT,
	account_access_type TEXT,
	group_access_type TEXT,
	account_authorization_id TEXT,
	account_authorization_source_type TEXT,
	account_authorization_source_team_id TEXT,
	group_authorization_id TEXT,
	group_authorization_source_type TEXT,
	group_authorization_source_team_id TEXT,
	cost_usd REAL,
	created_at TEXT NOT NULL
);
`

// usageRecordShardSchemaVersion 与 jobs usagewriter 的常量同值。
const usageRecordShardSchemaVersion = 8

// 上游响应模型三连样本的 trace（设计文档「验证点」逐字要求）。
const (
	usageTraceUpstreamMatch             = CleanupTracePrefix + "usage-coverage_upstream_response_model_match"
	usageTraceUpstreamMismatch          = CleanupTracePrefix + "usage-coverage_upstream_response_model_mismatch"
	usageTraceUpstreamUnmappedMismatch  = CleanupTracePrefix + "usage-coverage_upstream_response_model_unmapped_mismatch"
	usageMachineVariantModelMappingHint = "mockdata-global-long-context"
)

// usageResponseModelSuffix 是「上游返回了带日期后缀的真实模型」的模拟后缀：
// 它让 upstream_model <> upstream_response_model，从而命中不一致断言，同时又
// 不引入任何派生布尔列（设计文档明确要求只写原始字段）。
const usageResponseModelSuffix = "-2026-03-17"

// usageEndpointVariant 是一条端点样本：method + path 是 jobs / gateway 记录的
// endpoint 形态（`<METHOD> <path>`），family 取 gatewayrouting 的端点族词汇表。
type usageEndpointVariant struct {
	method    string
	path      string
	family    string
	semantic  string
	stream    int
	imageMode bool
	// tokenless 标记不计 token 的端点（models 目录查询）。
	tokenless bool
}

// usageEndpointVariants 是端点样本矩阵：覆盖 OpenAI responses / chat completions /
// images / models 与 Anthropic messages / count_tokens，流式与非流式混合。
var usageEndpointVariants = []usageEndpointVariant{
	{method: "POST", path: "/v1/responses", family: "responses", semantic: "openai", stream: 1},
	{method: "POST", path: "/v1/responses", family: "responses", semantic: "openai"},
	{method: "POST", path: "/v1/chat/completions", family: "chat_completions", semantic: "openai", stream: 1},
	{method: "POST", path: "/v1/chat/completions", family: "chat_completions", semantic: "openai"},
	{method: "POST", path: "/v1/images/generations", family: "chat_completions", semantic: "openai", imageMode: true},
	{method: "GET", path: "/v1/models", family: "models", semantic: "openai", tokenless: true},
	{method: "POST", path: "/v1/messages", family: "messages", semantic: "anthropic", stream: 1},
	{method: "POST", path: "/v1/messages/count_tokens", family: "count_tokens", semantic: "anthropic"},
}

// usageFailureSample 是一条失败样本：状态码与错误码覆盖设计文档要求分布。
type usageFailureSample struct {
	statusCode  int
	errorCode   string
	errorText   string
	attribution string
}

var usageFailureSamples = []usageFailureSample{
	{statusCode: 429, errorCode: "rate_limit_exceeded", errorText: "Mockdata 模拟上游限流", attribution: "upstream"},
	{statusCode: 503, errorCode: "service_unavailable", errorText: "Mockdata 模拟上游维护", attribution: "upstream"},
	{statusCode: 500, errorCode: "internal_server_error", errorText: "Mockdata 模拟上游内部错误", attribution: "upstream"},
	{statusCode: 402, errorCode: "insufficient_quota", errorText: "Mockdata 模拟上游额度不足", attribution: "upstream"},
	{statusCode: 401, errorCode: "invalid_api_key", errorText: "Mockdata 模拟上游认证失败", attribution: "upstream"},
}

// 端点族与访问类型取值（gatewayrouting / openai-account-selector 的词汇表）。
const (
	usageAccessTypeOwner      = "owner"
	usageAccessTypeAuthorized = "authorized"
)

// usageResources 是 usage 域从 business 库运行时解析出的真实外键集合。
type usageResources struct {
	skip     string
	accounts []usageAccountRow
	groups   []usageGroupRow
	apiKeys  []usageAPIKeyRow
	mappings []usageMappingRow
	models   []string
	images   []string
}

type usageAccountRow struct {
	id        string
	owner     string
	provider  string
	profile   string
	health    string
	typ       string
	groupHint string
}

type usageGroupRow struct {
	id     string
	owner  string
	active bool
}

type usageAPIKeyRow struct {
	id    string
	owner string
}

type usageMappingRow struct {
	accountID      string
	sourceModel    string
	upstreamModel  string
	sourceFamily   string
	upstreamFamily string
}

// loadUsageResources 读取 business 库的 mock 资源；缺 schema / seed 时返回 skip
// 原因而不是报错（mockdata 必须能在空数据根上跑完）。
func loadUsageResources(ctx context.Context, e *env) (usageResources, error) {
	resources := usageResources{}
	for _, table := range []string{"accounts", "groups", "api_keys"} {
		exists, err := e.existsTable(ctx, StoreBusiness, table)
		if err != nil {
			return resources, err
		}
		if !exists {
			resources.skip = "business 库缺少表 " + table + "（请先执行 --seed 与 business 域造数）"
			return resources, nil
		}
	}
	db, err := e.openExisting(StoreBusiness)
	if err != nil {
		return resources, err
	}
	if db == nil {
		resources.skip = "business 库文件不存在"
		return resources, nil
	}

	accountRows, err := db.QueryContext(ctx, `SELECT id, system_account_id, COALESCE(provider_code,''),
		COALESCE(provider_protocol_profile_id,''), COALESCE(health_check_model,''), COALESCE(type,'')
		FROM accounts WHERE id LIKE ? ORDER BY id`, CleanupIDPrefix+"%")
	if err != nil {
		return resources, fmt.Errorf("读取 mock 账户: %w", err)
	}
	for accountRows.Next() {
		var row usageAccountRow
		if err := accountRows.Scan(&row.id, &row.owner, &row.provider, &row.profile, &row.health, &row.typ); err != nil {
			accountRows.Close()
			return resources, err
		}
		resources.accounts = append(resources.accounts, row)
	}
	if err := accountRows.Err(); err != nil {
		accountRows.Close()
		return resources, err
	}
	accountRows.Close()

	groupRows, err := db.QueryContext(ctx, `SELECT id, system_account_id, COALESCE(enabled,0)
		FROM groups WHERE id LIKE ? ORDER BY id`, CleanupIDPrefix+"%")
	if err != nil {
		return resources, fmt.Errorf("读取 mock 分组: %w", err)
	}
	for groupRows.Next() {
		var (
			row     usageGroupRow
			enabled int
		)
		if err := groupRows.Scan(&row.id, &row.owner, &enabled); err != nil {
			groupRows.Close()
			return resources, err
		}
		row.active = enabled != 0
		resources.groups = append(resources.groups, row)
	}
	if err := groupRows.Err(); err != nil {
		groupRows.Close()
		return resources, err
	}
	groupRows.Close()

	keyRows, err := db.QueryContext(ctx, `SELECT id, system_account_id FROM api_keys WHERE id LIKE ? ORDER BY id`, CleanupIDPrefix+"%")
	if err != nil {
		return resources, fmt.Errorf("读取 mock API Key: %w", err)
	}
	for keyRows.Next() {
		var row usageAPIKeyRow
		if err := keyRows.Scan(&row.id, &row.owner); err != nil {
			keyRows.Close()
			return resources, err
		}
		resources.apiKeys = append(resources.apiKeys, row)
	}
	if err := keyRows.Err(); err != nil {
		keyRows.Close()
		return resources, err
	}
	keyRows.Close()

	// 账户模型映射：命中样本的 model_mapping_applied / model_mapping_source 与
	// upstream_model 都必须来自真实映射行，而不是硬编码猜测。
	mappingRows, err := db.QueryContext(ctx, `SELECT account_id, source_model, upstream_model,
		COALESCE(source_endpoint_family,''), COALESCE(upstream_endpoint_family,'')
		FROM account_model_mappings WHERE enabled = 1 ORDER BY account_id, source_model`)
	if err != nil {
		return resources, fmt.Errorf("读取账户模型映射: %w", err)
	}
	for mappingRows.Next() {
		var row usageMappingRow
		if err := mappingRows.Scan(&row.accountID, &row.sourceModel, &row.upstreamModel, &row.sourceFamily, &row.upstreamFamily); err != nil {
			mappingRows.Close()
			return resources, err
		}
		resources.mappings = append(resources.mappings, row)
	}
	if err := mappingRows.Err(); err != nil {
		mappingRows.Close()
		return resources, err
	}
	mappingRows.Close()

	modelRows, err := db.QueryContext(ctx, `SELECT model, COALESCE(mode,'') FROM provider_model_catalog
		WHERE status = 'active' ORDER BY catalog_order, model`)
	if err != nil {
		return resources, fmt.Errorf("读取模型目录: %w", err)
	}
	for modelRows.Next() {
		var (
			model string
			mode  string
		)
		if err := modelRows.Scan(&model, &mode); err != nil {
			modelRows.Close()
			return resources, err
		}
		resources.models = append(resources.models, model)
		if mode == "image" {
			resources.images = append(resources.images, model)
		}
	}
	if err := modelRows.Err(); err != nil {
		modelRows.Close()
		return resources, err
	}
	modelRows.Close()

	if len(resources.accounts) == 0 {
		resources.skip = "business 库没有 mock 账户（请先执行 business 域造数）"
		return resources, nil
	}
	if len(resources.apiKeys) == 0 {
		resources.skip = "business 库没有 mock API Key（请先执行 business 域造数）"
		return resources, nil
	}
	return resources, nil
}

// accountAt 取第 index 个账户（越界回落，保证场景构造不越界）。
func (r usageResources) accountAt(index int) usageAccountRow {
	if index < 0 {
		index = 0
	}
	if index >= len(r.accounts) {
		index = len(r.accounts) - 1
	}
	return r.accounts[index]
}

// modelAt 取第 index 个目录模型；目录为空时回落到设计文档的兜底模型名。
func (r usageResources) modelAt(index int) (string, bool) {
	if len(r.models) == 0 {
		return usageMachineVariantModelMappingHint, false
	}
	if index < 0 {
		index = 0
	}
	return r.models[index%len(r.models)], true
}

// imageModelAt 取第 index 个图像模型；目录里没有图像模型时回落首个模型。
func (r usageResources) imageModelAt(index int) (string, bool) {
	if len(r.images) == 0 {
		return r.modelAt(index)
	}
	if index < 0 {
		index = 0
	}
	return r.images[index%len(r.images)], true
}

// groupsForOwner 返回该账户归属人名下的分组（优先 enabled）。
func (r usageResources) groupsForOwner(owner string) []usageGroupRow {
	var matched, all []usageGroupRow
	for _, group := range r.groups {
		if group.owner != owner {
			continue
		}
		all = append(all, group)
		if group.active {
			matched = append(matched, group)
		}
	}
	if len(matched) > 0 {
		return matched
	}
	return all
}

// mappingFor 返回该账户在指定端点族上的启用映射。
func (r usageResources) mappingFor(accountID, family string) (usageMappingRow, bool) {
	for _, mapping := range r.mappings {
		if mapping.accountID != accountID {
			continue
		}
		if mapping.sourceModel != usageMachineVariantModelMappingHint {
			continue
		}
		if family != "" && mapping.sourceFamily != "" && mapping.sourceFamily != family {
			continue
		}
		return mapping, true
	}
	return usageMappingRow{}, false
}

// mappingAny 返回该账户的任一条启用映射（三连样本用；不限定端点族）。
func (r usageResources) mappingAny(accountID string) (usageMappingRow, bool) {
	for _, mapping := range r.mappings {
		if mapping.accountID == accountID {
			return mapping, true
		}
	}
	return usageMappingRow{}, false
}

// usageScenario 是一条造数场景：某个 API Key 在某个分组下调用某个账户。
type usageScenario struct {
	apiKeyID      string
	apiKeyOwner   string
	groupID       string
	groupOwner    string
	account       usageAccountRow
	clientIPBase  string
	trafficSource string
}

// usageRecordSpec 是一条记录的取值规格：buildRecord 按它填 61 列。
type usageRecordSpec struct {
	createdAt     time.Time
	traceID       string
	scenario      usageScenario
	variant       usageEndpointVariant
	model         string
	upstreamModel string
	// upstreamResponseModel 省略时等于 upstreamModel（一致样本）。
	upstreamResponseModel string
	mappingApplied        int
	mappingSource         string
	success               bool
	statusCode            int
	errorCode             string
	errorMessage          string
	failureAttribution    string
	stream                int
	requestedTier         string
	effectiveTier         string
	reportedTier          string
	billedTier            string
	requestedEffort       string
	effectiveEffort       string
	imageMode             bool
	tokenless             bool
	// ordinal 只用于生成确定性的 token / 延迟数值。
	ordinal int
}

// usageWriter 承载一次用量域写入：统一时钟、真实资源集合、分片句柄与按
// 逻辑对象统计的行数（DomainResult.Counts 直接取它）。
type usageWriter struct {
	ctx       context.Context
	e         *env
	now       time.Time
	resources usageResources
	files     *usageShardFiles
	counts    map[string]int
}

// stamp 返回造数基准时间的毫秒 ISO 字符串（与 Node nowIso 一致）。
func (w *usageWriter) stamp() string {
	return w.now.UTC().Format(isoMillisLayout)
}

// seed 生成全部记录并写分片 + catalog + stats 镜像。
func (w *usageWriter) seed() error {
	scenarios := w.buildScenarios()
	if len(scenarios) == 0 {
		return fmt.Errorf("mockdata usage 域没有可用场景（business 资源为空）")
	}
	daily := w.buildDailyRecords(scenarios)
	health := w.buildHealthRecords(scenarios)
	coverage := w.buildCoverageRecords(scenarios)
	w.counts["usageHealthRecords"] = len(health)
	w.counts["usageCoverageRecords"] = len(coverage)
	entries := make([]usageRecordEntry, 0, len(daily)+len(health)+len(coverage))
	entries = append(entries, daily...)
	entries = append(entries, health...)
	entries = append(entries, coverage...)
	return w.write(entries)
}

// buildScenarios 为每条 mock API Key 构造「Key + 同归属人账户 + 同归属人分组」
// 场景；账户归属人没有可用账户时回落到任意账户并计数（不猜 ID）。
func (w *usageWriter) buildScenarios() []usageScenario {
	byOwner := map[string][]usageAccountRow{}
	for _, account := range w.resources.accounts {
		byOwner[account.owner] = append(byOwner[account.owner], account)
	}
	var scenarios []usageScenario
	for keyIndex, key := range w.resources.apiKeys {
		accounts := byOwner[key.owner]
		if len(accounts) == 0 {
			// 该 Key 归属人没有自有账户：仍按真实账户写入，但显式记账，
			// 让「哪条记录的外键不是同归属资源」在报告里可查。
			w.counts["usageRecordsWithoutOwnerAccount"]++
			accounts = w.resources.accounts
		}
		limit := len(accounts)
		if limit > 3 {
			limit = 3
		}
		for index := 0; index < limit; index++ {
			account := accounts[(keyIndex+index)%len(accounts)]
			groupID, groupOwner := "", ""
			if groups := w.resources.groupsForOwner(account.owner); len(groups) > 0 {
				group := groups[(keyIndex+index)%len(groups)]
				groupID, groupOwner = group.id, group.owner
			} else if groups := w.resources.groupsForOwner(key.owner); len(groups) > 0 {
				group := groups[keyIndex%len(groups)]
				groupID, groupOwner = group.id, group.owner
			}
			if groupID == "" {
				// 分组是必填外键之一，缺失即该字段为 NULL 并计数。
				w.counts["usageRecordsWithoutGroup"]++
			}
			clientIPBase := fmt.Sprintf("10.10.%d.", keyIndex%250)
			if keyIndex%3 == 2 {
				clientIPBase = fmt.Sprintf("10.20.%d.", keyIndex%250)
			}
			scenarios = append(scenarios, usageScenario{
				apiKeyID: key.id, apiKeyOwner: key.owner,
				groupID: groupID, groupOwner: groupOwner,
				account:       account,
				clientIPBase:  clientIPBase,
				trafficSource: usageTrafficSourceForOrdinal(len(scenarios)),
			})
		}
	}
	return scenarios
}

// usageTrafficSourceForOrdinal 让场景矩阵覆盖全部五种流量来源：gateway 为主，
// 其余四种按固定节拍出现，保证「每种来源都至少一条」。
func usageTrafficSourceForOrdinal(index int) string {
	switch index % 7 {
	case 3:
		return "manual_account_test"
	case 4:
		return "cooldown_retest"
	case 5:
		return "runtime_recovery_probe"
	case 6:
		return "account_health_check"
	default:
		return "gateway"
	}
}

// usageRecordEntry 是一条待落库记录及其目标分片。
type usageRecordEntry struct {
	columns  map[string]any
	location usageShardLocation
}

// buildDailyRecords 生成近 Options.Days 天的日常记录：每天 DailyRequests 条，
// 时间均匀铺开且分钟偏移确定（同一 Now 下逐字节可复现）。
func (w *usageWriter) buildDailyRecords(scenarios []usageScenario) []usageRecordEntry {
	days := w.e.options.Days
	daily := w.e.options.DailyRequests
	endAt := w.now.Add(-10 * time.Minute)
	startAt := time.Date(endAt.Year(), endAt.Month(), endAt.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -(days - 1))
	entries := make([]usageRecordEntry, 0, days*daily)
	for dayIndex := 0; dayIndex < days; dayIndex++ {
		dayStart := startAt.AddDate(0, 0, dayIndex)
		latest := dayStart.Add(24*time.Hour - time.Minute)
		if dayIndex == days-1 {
			latest = endAt
		}
		if latest.Before(dayStart) {
			latest = dayStart
		}
		minutesRange := int(latest.Sub(dayStart) / time.Minute)
		if minutesRange < 1 {
			minutesRange = 1
		}
		for requestIndex := 0; requestIndex < daily; requestIndex++ {
			ordinal := dayIndex*daily + requestIndex
			scenario := scenarios[ordinal%len(scenarios)]
			minuteOfDay := (requestIndex * minutesRange) / daily
			if minuteOfDay > minutesRange {
				minuteOfDay = minutesRange
			}
			createdAt := dayStart.Add(time.Duration(minuteOfDay)*time.Minute +
				time.Duration(mockPseudoRandom(ordinal, 3)*45)*time.Second)
			if createdAt.After(latest) {
				createdAt = latest
			}
			spec := w.dailySpec(ordinal, createdAt, scenario)
			entries = append(entries, w.newRecord(spec))
		}
	}
	return entries
}

// dailySpec 按样本矩阵把一个序号映射成一条记录规格。
func (w *usageWriter) dailySpec(ordinal int, createdAt time.Time, scenario usageScenario) usageRecordSpec {
	variant := usageEndpointVariants[ordinal%len(usageEndpointVariants)]
	spec := usageRecordSpec{
		createdAt:     createdAt,
		traceID:       fmt.Sprintf("%susage-%05d", CleanupTracePrefix, ordinal),
		scenario:      scenario,
		variant:       variant,
		success:       true,
		statusCode:    200,
		stream:        variant.stream,
		requestedTier: "default",
		effectiveTier: "default",
		reportedTier:  "default",
		billedTier:    "default",
		imageMode:     variant.imageMode,
		tokenless:     variant.tokenless,
		ordinal:       ordinal,
	}
	requestedModel := scenario.account.health
	if model, ok := w.resources.modelAt(ordinal); ok {
		requestedModel = model
	} else {
		w.counts["usageRecordsWithoutModel"]++
	}
	if variant.imageMode {
		requestedModel, _ = w.resources.imageModelAt(ordinal)
	}
	spec.model = requestedModel
	spec.upstreamModel = requestedModel

	// 模型映射命中：命中样本的 model_mapping_applied / model_mapping_source 与
	// upstream_model 都取真实启用映射行。
	if mapping, ok := w.resources.mappingFor(scenario.account.id, variant.family); ok && ordinal%4 == 0 {
		spec.model = mapping.sourceModel
		spec.upstreamModel = mapping.upstreamModel
		spec.mappingApplied = 1
		spec.mappingSource = "account_mapping"
	}

	// 计费档位：priority / flex / default 各三分之一，非 default 档位附计价快照。
	switch ordinal % 3 {
	case 0:
		spec.effectiveTier, spec.reportedTier, spec.billedTier = "priority", "priority", "priority"
	case 1:
		spec.effectiveTier, spec.reportedTier, spec.billedTier = "flex", "flex", "flex"
	}
	// 思考强度：requests/effective 分别覆盖 low / medium / high 且允许不一致。
	if !variant.imageMode && !variant.tokenless && variant.family != "count_tokens" {
		efforts := []string{"low", "medium", "high"}
		spec.requestedEffort = efforts[ordinal%3]
		spec.effectiveEffort = efforts[(ordinal+1)%3]
	}

	// 失败样本：每 7 条命中一条，错误码按矩阵轮转，覆盖 429/503/500/402/401。
	if ordinal%7 == 6 && !variant.tokenless {
		failure := usageFailureSamples[(ordinal/7)%len(usageFailureSamples)]
		spec.success = false
		spec.statusCode = failure.statusCode
		spec.errorCode = failure.errorCode
		spec.errorMessage = failure.errorText
		spec.failureAttribution = failure.attribution
	}
	return spec
}

// buildHealthRecords 为每个 mock 账户生成 account_health_check 来源的逐小时记录。
//
// 为什么每个账户都要覆盖：account_health_hourly 由 jobs 从用量记录聚合，设计
// 文档要求每个账户有稳定的可用 / 不可用 / 无记录间隔（覆盖率 ≥90% 且存在空
// 小时）。缺口与失败小时按账户序号固定错位，逐账户样本互不重叠。
func (w *usageWriter) buildHealthRecords(scenarios []usageScenario) []usageRecordEntry {
	rangeHours := w.e.options.Days * 24
	if rangeHours > 31*24 {
		rangeHours = 31 * 24
	}
	if rangeHours < 1 {
		rangeHours = 1
	}
	endAt := w.now.Add(-10 * time.Minute)
	entries := make([]usageRecordEntry, 0, len(w.resources.accounts)*rangeHours)
	byAccount := map[string]usageScenario{}
	for _, scenario := range scenarios {
		if _, exists := byAccount[scenario.account.id]; !exists {
			scenario.trafficSource = "account_health_check"
			byAccount[scenario.account.id] = scenario
		}
	}
	for accountIndex, account := range w.resources.accounts {
		scenario, ok := byAccount[account.id]
		if !ok {
			// 该账户没有出现在任何 Key 场景里（business 域的 Key/账户归属组合
			// 不保证全覆盖）：现场拼一个健康检查场景，保证「每个 mock 账户都有
			// 逐小时体检记录」不依赖归属巧合，并计数留痕。
			scenario = w.healthScenarioForAccount(account, accountIndex)
			w.counts["usageHealthScenariosSynthesized"]++
		}
		gapOffset := 0
		if rangeHours > 2 {
			gapOffset = 1 + (accountIndex % (rangeHours - 1))
		}
		failureOffset := 0
		if rangeHours > 2 {
			failureOffset = 1 + ((gapOffset + 7) % (rangeHours - 1))
		}
		for hourOffset := 0; hourOffset < rangeHours; hourOffset++ {
			failure := false
			if rangeHours == 1 {
				failure = accountIndex%5 == 0
			} else {
				failure = hourOffset == failureOffset || (hourOffset+accountIndex*11)%47 == 0
			}
			if hourOffset > 0 && !failure && (hourOffset == gapOffset || (hourOffset+accountIndex*5)%29 == 0) {
				continue
			}
			model := account.health
			if model == "" {
				model, _ = w.resources.modelAt(accountIndex)
			}
			spec := usageRecordSpec{
				createdAt:     endAt.Add(-time.Duration(hourOffset) * time.Hour),
				traceID:       fmt.Sprintf("%susage-health-%03d-%04d", CleanupTracePrefix, accountIndex+1, hourOffset),
				scenario:      scenario,
				variant:       usageEndpointVariants[0],
				model:         model,
				upstreamModel: model,
				success:       !failure,
				statusCode:    200,
				stream:        0,
				requestedTier: "default",
				effectiveTier: "default",
				reportedTier:  "default",
				billedTier:    "default",
				ordinal:       10_000 + accountIndex*rangeHours + hourOffset,
			}
			if failure {
				spec.statusCode = 503
				spec.errorCode = "service_unavailable"
				spec.errorMessage = "Mockdata 模拟健康检查失败"
				spec.failureAttribution = "upstream"
			}
			entries = append(entries, w.newRecord(spec))
		}
	}
	return entries
}

// healthScenarioForAccount 为没有 Key 场景的账户拼一个健康检查场景：Key 按账户
// 序号轮转取真实 mock Key，分组优先取账户归属人名下的分组（外键仍然全部来自
// business 域的真实资源，缺失字段保持 NULL 并沿用 newRecord 的 NULL 投影）。
func (w *usageWriter) healthScenarioForAccount(account usageAccountRow, accountIndex int) usageScenario {
	scenario := usageScenario{
		account:       account,
		trafficSource: "account_health_check",
		clientIPBase:  fmt.Sprintf("10.10.%d.", accountIndex%250),
	}
	if len(w.resources.apiKeys) > 0 {
		key := w.resources.apiKeys[accountIndex%len(w.resources.apiKeys)]
		scenario.apiKeyID = key.id
		scenario.apiKeyOwner = key.owner
	}
	if groups := w.resources.groupsForOwner(account.owner); len(groups) > 0 {
		group := groups[accountIndex%len(groups)]
		scenario.groupID = group.id
		scenario.groupOwner = group.owner
	}
	return scenario
}

// buildCoverageRecords 生成上游响应模型三连定向样本与端点覆盖样本。
//
// 三连样本的语义（设计文档 §4「验证点」）：
//   - match：请求映射源模型，实际发送 upstream_model，上游原样返回同一模型；
//   - mismatch：请求映射源模型，上游返回带日期后缀的真实模型（映射后不一致）；
//   - unmapped_mismatch：请求并实际发送未映射的 upstream_model，上游仍返回带
//     后缀模型（无映射不一致，model_mapping_applied=0）。
//
// 映射行来自 business 库的 account_model_mappings（真实启用映射）；business 库
// 没有映射时按目录模型兜底并计数，保证三连样本这一硬覆盖点不因上游库版本差异
// 整批失败。
func (w *usageWriter) buildCoverageRecords(scenarios []usageScenario) []usageRecordEntry {
	mapping, mappingOK := w.resources.lookupCoverageMapping()
	scenario, scenarioOK := usageScenarioForAccount(scenarios, mapping.accountID)
	if !scenarioOK {
		scenario = scenarios[0]
	}
	source := mapping.sourceModel
	upstream := mapping.upstreamModel
	if !mappingOK {
		w.counts["usageSyntheticMappingSamples"]++
		source = usageMachineVariantModelMappingHint
		if model, ok := w.resources.modelAt(1); ok {
			upstream = model
		} else {
			upstream = usageMachineVariantModelMappingHint
		}
	}
	if strings.TrimSpace(source) == "" {
		source = usageMachineVariantModelMappingHint
	}
	if strings.TrimSpace(upstream) == "" {
		upstream = source
	}
	variant := usageEndpointVariants[2] // POST /v1/chat/completions
	base := func(traceID string, createdAt time.Time, ordinal int) usageRecordSpec {
		return usageRecordSpec{
			createdAt:     createdAt,
			traceID:       traceID,
			scenario:      scenario,
			variant:       variant,
			stream:        0,
			success:       true,
			statusCode:    200,
			requestedTier: "default",
			effectiveTier: "priority",
			reportedTier:  "priority",
			billedTier:    "priority",
			ordinal:       ordinal,
		}
	}

	match := base(usageTraceUpstreamMatch, w.now.Add(-1*time.Hour), 20_001)
	match.model = source
	match.upstreamModel = upstream
	match.upstreamResponseModel = upstream
	match.mappingApplied = 1
	match.mappingSource = "account_mapping"

	mismatch := base(usageTraceUpstreamMismatch, w.now.Add(-2*time.Hour), 20_002)
	mismatch.model = source
	mismatch.upstreamModel = upstream
	mismatch.upstreamResponseModel = upstream + usageResponseModelSuffix
	mismatch.mappingApplied = 1
	mismatch.mappingSource = "account_mapping"

	unmapped := base(usageTraceUpstreamUnmappedMismatch, w.now.Add(-3*time.Hour), 20_003)
	// 未映射样本：请求模型本身就是要发送的上游模型，映射未命中。
	unmapped.model = upstream
	unmapped.upstreamModel = upstream
	unmapped.upstreamResponseModel = upstream + usageResponseModelSuffix

	modelMapping := base(CleanupTracePrefix+"usage-coverage_model_mapping", w.now.Add(-4*time.Hour), 20_004)
	modelMapping.model = source
	modelMapping.upstreamModel = upstream
	modelMapping.upstreamResponseModel = upstream
	modelMapping.mappingApplied = 1
	modelMapping.mappingSource = "account_mapping"

	entries := []usageRecordEntry{
		w.newRecord(match),
		w.newRecord(mismatch),
		w.newRecord(unmapped),
		w.newRecord(modelMapping),
	}

	// 端点覆盖样本：models 目录查询与 images 图像生成各一条，保证端点值域
	// （含图片 token 字段）在明细里可见。
	modelsSpec := base(CleanupTracePrefix+"usage-coverage_models_endpoint", w.now.Add(-5*time.Hour), 20_005)
	modelsSpec.variant = usageEndpointVariants[5]
	modelsSpec.tokenless = true
	modelsSpec.model, modelsSpec.upstreamModel = source, source
	modelsSpec.effectiveTier, modelsSpec.reportedTier, modelsSpec.billedTier = "default", "default", "default"
	modelsSpec.requestedEffort, modelsSpec.effectiveEffort = "", ""
	entries = append(entries, w.newRecord(modelsSpec))

	imageSpec := base(CleanupTracePrefix+"usage-coverage_image_endpoint", w.now.Add(-6*time.Hour), 20_006)
	imageSpec.variant = usageEndpointVariants[4]
	imageSpec.imageMode = true
	imageSpec.stream = 0
	imageSpec.requestedEffort, imageSpec.effectiveEffort = "", ""
	imageModel, _ := w.resources.imageModelAt(0)
	imageSpec.model, imageSpec.upstreamModel = imageModel, imageModel
	entries = append(entries, w.newRecord(imageSpec))

	w.counts["usageUpstreamResponseModelSamples"] = 3
	return entries
}

// lookupCoverageMapping 选一条用于上游响应模型三连样本的映射：优先账户
// mockdata_acc_standard（business 域的映射样本账户），否则取第一条真实映射。
func (w *usageResources) lookupCoverageMapping() (usageMappingRow, bool) {
	for _, mapping := range w.mappings {
		if mapping.accountID != CleanupIDPrefix+"acc_standard" {
			continue
		}
		if mapping.sourceModel == usageMachineVariantModelMappingHint {
			return mapping, true
		}
	}
	for _, mapping := range w.mappings {
		if mapping.accountID == CleanupIDPrefix+"acc_standard" {
			return mapping, true
		}
	}
	for _, mapping := range w.mappings {
		return mapping, true
	}
	return usageMappingRow{}, false
}

// usageScenarioForAccount 找该账户的既有场景（三连样本复用其 Key / 分组外键）。
func usageScenarioForAccount(scenarios []usageScenario, accountID string) (usageScenario, bool) {
	for _, scenario := range scenarios {
		if scenario.account.id == accountID {
			return scenario, true
		}
	}
	return usageScenario{}, false
}

// newRecord 把规格展开成 61 列取值与目标分片。
func (w *usageWriter) newRecord(spec usageRecordSpec) usageRecordEntry {
	shardCount := w.e.options.Paths.UsageShardCount
	entropy := usageEntropy(spec.traceID, spec.ordinal)
	bucketDateKey := spec.createdAt.UTC().Format("20060102")
	shardID := stableUsageShardID(entropy, shardCount)
	id := fmt.Sprintf("usage_%s_s%02d_%d_%s", bucketDateKey, shardID, spec.createdAt.UnixMilli(), entropy)
	location := usageShardLocationForBucket(bucketDateKey, shardID, w.e.options.Paths.UsageShardRoot)

	columns := map[string]any{
		"id":                              id,
		"system_account_id":               spec.scenario.account.owner,
		"trace_id":                        spec.traceID,
		"traffic_source":                  spec.scenario.trafficSource,
		"endpoint":                        spec.variant.method + " " + spec.variant.path,
		"provider_code":                   spec.scenario.account.provider,
		"provider_protocol_profile_id":    nullString(spec.scenario.account.profile),
		"usage_semantic":                  spec.variant.semantic,
		"model":                           nullString(spec.model),
		"upstream_model":                  nullString(spec.upstreamModel),
		"upstream_response_model":         nullString(upstreamResponseModelOf(spec)),
		"pricing_model":                   nullString(spec.model),
		"requested_service_tier":          spec.requestedTier,
		"effective_service_tier":          spec.effectiveTier,
		"reported_service_tier":           nullString(spec.reportedTier),
		"billed_service_tier":             spec.billedTier,
		"requested_reasoning_effort":      nullString(spec.requestedEffort),
		"effective_reasoning_effort":      nullString(spec.effectiveEffort),
		"cost_breakdown_snapshot_json":    nullString(w.costBreakdown(spec)),
		"model_mapping_applied":           spec.mappingApplied,
		"model_mapping_source":            nullString(spec.mappingSource),
		"source_endpoint_family":          spec.variant.family,
		"upstream_endpoint_family":        spec.variant.family,
		"stream":                          spec.stream,
		"status_code":                     spec.statusCode,
		"success":                         boolInt(spec.success),
		"failure_attribution":             nullString(spec.failureAttribution),
		"error_code":                      nullString(spec.errorCode),
		"error_message":                   nullString(spec.errorMessage),
		"client_ip":                       spec.scenario.clientIPBase + strconv.Itoa(1+spec.ordinal%250),
		"created_at":                      spec.createdAt.UTC().Format(isoMillisLayout),
		"account_owner_system_account_id": nullString(spec.scenario.account.owner),
		"account_access_type":             usageAccessType(spec.scenario.account.owner, spec.scenario.apiKeyOwner),
	}
	if spec.scenario.groupID != "" {
		columns["group_id"] = spec.scenario.groupID
		columns["group_owner_system_account_id"] = nullString(spec.scenario.groupOwner)
		columns["group_access_type"] = usageAccessType(spec.scenario.groupOwner, spec.scenario.apiKeyOwner)
	}
	if spec.scenario.apiKeyID != "" {
		columns["api_key_id"] = spec.scenario.apiKeyID
	}
	columns["account_id"] = spec.scenario.account.id
	w.fillMetrics(spec, columns)
	return usageRecordEntry{columns: columns, location: location}
}

// fillMetrics 填 token / 延迟 / 成本列：失败与不计 token 的端点留 NULL，
// 图像端点带图片 token，成功文本请求带流式首 token 延迟。
func (w *usageWriter) fillMetrics(spec usageRecordSpec, columns map[string]any) {
	if !spec.success {
		columns["first_token_ms"] = nil
		columns["duration_ms"] = 300 + spec.ordinal%2000
		columns["cost_usd"] = 0.0
		return
	}
	if spec.tokenless {
		columns["duration_ms"] = 30 + spec.ordinal%80
		return
	}
	firstToken := 120 + spec.ordinal%900
	columns["first_token_ms"] = firstToken
	columns["duration_ms"] = firstToken + 400 + spec.ordinal%3000
	if spec.imageMode {
		columns["input_tokens"] = 120
		columns["output_tokens"] = 0
		columns["input_image_tokens"] = 1024
		columns["output_image_tokens"] = 512
		columns["output_image_count"] = 2
		columns["cost_usd"] = usageCostUSD(spec.ordinal, 120, 0, spec.billedTier)
		return
	}
	if spec.variant.family == "count_tokens" {
		columns["input_tokens"] = 320 + spec.ordinal%200
		columns["cost_usd"] = 0.0
		return
	}
	inputTokens := 800 + spec.ordinal%4000
	outputTokens := 200 + spec.ordinal%1500
	columns["input_tokens"] = inputTokens
	columns["output_tokens"] = outputTokens
	if spec.ordinal%5 == 0 {
		columns["cache_read_tokens"] = 256 + spec.ordinal%512
		columns["cache_read_cost_usd"] = 0.000042
	}
	if spec.ordinal%9 == 0 {
		columns["cache_write_tokens"] = 128
		columns["cache_write_1h_tokens"] = 64
		columns["cache_write_cost_usd"] = 0.000021
	}
	if spec.effectiveEffort == "high" {
		columns["thinking_tokens"] = 128 + spec.ordinal%256
	}
	columns["cost_usd"] = usageCostUSD(spec.ordinal, inputTokens, outputTokens, spec.billedTier)
}

// costBreakdown 生成计价快照：只有非 default 档位才有档位计价文档（与 Node
// tierCostBreakdown 的语义一致），default 档位留空表示没有档位计价来源。
func (w *usageWriter) costBreakdown(spec usageRecordSpec) string {
	if spec.billedTier == "" || spec.billedTier == "default" {
		return ""
	}
	document := map[string]any{
		"serviceTier":  spec.billedTier,
		"source":       "mockdata",
		"recordedAt":   spec.createdAt.UTC().Format(isoMillisLayout),
		"inputTokens":  spec.ordinal%4000 + 800,
		"outputTokens": spec.ordinal % 1500,
		"unitPriceUsd": usageTierUnitPrice(spec.billedTier),
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		return ""
	}
	return string(encoded)
}

// usageTierUnitPrice 是档位单价倍率（priority 更贵、flex 更便宜），只用于让
// 快照文档可读，不参与任何派生计算。
func usageTierUnitPrice(tier string) float64 {
	switch tier {
	case "priority":
		return 0.0000022
	case "flex":
		return 0.0000011
	default:
		return 0.0000016
	}
}

// usageCostUSD 生成与档位相关的成本数值。
func usageCostUSD(ordinal, inputTokens, outputTokens int, tier string) float64 {
	price := usageTierUnitPrice(tier)
	cost := float64(inputTokens)*price + float64(outputTokens)*price*2
	return float64(int(cost*1_000_000+0.5)) / 1_000_000
}

// upstreamResponseModelOf 返回上游返回模型：未显式给出时与 upstream_model 一致
// （一致样本）；显式给出时原样写入（不一致样本）。
func upstreamResponseModelOf(spec usageRecordSpec) string {
	if spec.upstreamResponseModel != "" {
		return spec.upstreamResponseModel
	}
	return spec.upstreamModel
}

// usageAccessType 判断访问类型：归属人自己调用是 owner，他人调用是 authorized。
func usageAccessType(resourceOwner, caller string) string {
	if resourceOwner != "" && resourceOwner == caller {
		return usageAccessTypeOwner
	}
	return usageAccessTypeAuthorized
}

// usageEntropy 生成 24 字符以内的 ASCII 熵（jobs 的 shard id 需要 [A-Za-z0-9]），
// 同时带造数标识便于人眼识别分片内记录来源。
func usageEntropy(traceID string, ordinal int) string {
	var builder strings.Builder
	builder.WriteString("mockdataUsage")
	builder.WriteString(strconv.Itoa(ordinal))
	for _, r := range traceID {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			continue
		}
		builder.WriteRune('x')
	}
	value := builder.String()
	if len(value) > 24 {
		value = value[:24]
	}
	return value
}

// mockPseudoRandom 是确定性伪随机（0..1）：造数在同一 Now 下必须逐字节可复现，
// 不能用 math/rand。
func mockPseudoRandom(ordinal, salt int) float64 {
	hash := uint32(2166136261)
	for _, value := range []int{ordinal, salt} {
		for shift := 0; shift < 32; shift += 8 {
			hash ^= uint32(byte(value >> shift))
			hash *= 16777619
		}
	}
	return float64(hash%10000) / 10000
}

// usageShardLocation 是分片文件的定位结果（jobs UsageRecordShardLocation 的
// 同形副本：shard_key、bucket_date、分片号与绝对文件路径）。
type usageShardLocation struct {
	ShardKey      string
	BucketDate    string
	BucketDateKey string
	ShardID       int
	FilePath      string
}

// usageShardLocationForBucket 复刻 jobs usageRecordShardLocation 的布局：
// <root>/YYYY/MM/DD/usage-YYYYMMDD-sNN.sqlite3，shard_key = <YYYYMMDD>:s<NN>。
func usageShardLocationForBucket(bucketDateKey string, shardIDInput int, shardRoot string) usageShardLocation {
	shardID := shardIDInput
	if shardID < 0 {
		shardID = 0
	}
	if len(bucketDateKey) != 8 {
		bucketDateKey = "19700101"
	}
	return usageShardLocation{
		ShardKey:      bucketDateKey + ":s" + fmt.Sprintf("%02d", shardID),
		BucketDate:    bucketDateKey[0:4] + "-" + bucketDateKey[4:6] + "-" + bucketDateKey[6:8],
		BucketDateKey: bucketDateKey,
		ShardID:       shardID,
		FilePath: filepath.Join(shardRoot,
			bucketDateKey[0:4], bucketDateKey[4:6], bucketDateKey[6:8],
			fmt.Sprintf("usage-%s-s%02d.sqlite3", bucketDateKey, shardID)),
	}
}

// stableUsageShardID 复刻 jobs stableShardId（FNV-1a 32 位模分片数）：造数必须
// 与 jobs 的路由算法同源，否则 catalog 里的 shard_key 与 jobs 的读取分片不一致。
func stableUsageShardID(value string, shardCount int) int {
	if shardCount < 1 {
		shardCount = 1
	}
	hash := uint32(2166136261)
	for index := 0; index < len(value); index++ {
		hash ^= uint32(value[index])
		hash *= 16777619
	}
	return int(hash) % shardCount
}

// usageShardFiles 管理本次造数打开的分片文件句柄。
type usageShardFiles struct {
	root string
	dbs  map[string]*sql.DB
}

// db 打开（缺失时创建）分片文件并应用分片基础 schema。
func (f *usageShardFiles) db(ctx context.Context, path string) (*sql.DB, error) {
	if db, ok := f.dbs[path]; ok {
		return db, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("创建分片目录 %s: %w", filepath.Dir(path), err)
	}
	db, err := openSQLiteDatabase(sqliteDSN(path))
	if err != nil {
		return nil, fmt.Errorf("打开分片 %s: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("PRAGMA journal_mode = WAL;"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("配置分片 %s 的 journal_mode: %w", path, err)
	}
	if _, err := schema.EnsureSQLiteUsageShard(ctx, db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("建分片 schema %s: %w", path, err)
	}
	f.dbs[path] = db
	return db, nil
}

// Close 关闭全部分片句柄。
func (f *usageShardFiles) Close() error {
	var failures []error
	paths := make([]string, 0, len(f.dbs))
	for path := range f.dbs {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		if err := f.dbs[path].Close(); err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", path, err))
		}
	}
	f.dbs = map[string]*sql.DB{}
	if len(failures) > 0 {
		return errors.Join(failures...)
	}
	return nil
}

// usageShardPlan 是一个分片文件待写入的记录集合。
type usageShardPlan struct {
	location usageShardLocation
	records  []map[string]any
}

// write 落库：分片文件 → catalog 登记 → stats 聚合镜像。
func (w *usageWriter) write(entries []usageRecordEntry) error {
	plans := map[string]*usageShardPlan{}
	var keys []string
	for _, entry := range entries {
		key := entry.location.ShardKey
		plan, exists := plans[key]
		if !exists {
			plan = &usageShardPlan{location: entry.location}
			plans[key] = plan
			keys = append(keys, key)
		}
		plan.records = append(plan.records, entry.columns)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if err := w.writeShard(plans[key]); err != nil {
			return err
		}
	}
	w.counts["usageShardFiles"] = len(keys)
	if err := w.writeCatalog(plans, keys); err != nil {
		return err
	}
	return w.writeStatsMirror(entries)
}

// writeShard 写入一个分片文件的全部记录。
func (w *usageWriter) writeShard(plan *usageShardPlan) error {
	db, err := w.files.db(w.ctx, plan.location.FilePath)
	if err != nil {
		return err
	}
	query := "INSERT INTO usage_records (" + strings.Join(usageRecordColumns, ", ") + ") VALUES (" +
		sqlitePlaceholders(len(usageRecordColumns)) + ") ON CONFLICT(id) DO NOTHING"
	tx, err := db.BeginTx(w.ctx, nil)
	if err != nil {
		return fmt.Errorf("分片 %s begin: %w", plan.location.ShardKey, err)
	}
	statement, err := tx.PrepareContext(w.ctx, query)
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("分片 %s prepare: %w", plan.location.ShardKey, err)
	}
	defer statement.Close()
	inserted := 0
	for _, record := range plan.records {
		result, err := statement.ExecContext(w.ctx, recordValues(record, usageRecordColumns)...)
		if err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("分片 %s 写入记录: %w", plan.location.ShardKey, err)
		}
		if affected, err := result.RowsAffected(); err == nil && affected > 0 {
			inserted++
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("分片 %s commit: %w", plan.location.ShardKey, err)
	}
	w.counts["usageRecords"] += inserted
	return nil
}

// writeCatalog 登记分片、逐记录条目与账户 / Key 作用域目录。
func (w *usageWriter) writeCatalog(plans map[string]*usageShardPlan, keys []string) error {
	now := w.stamp()
	return w.e.tx(w.ctx, StoreUsageCatalog, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(w.ctx, usageCatalogDDL); err != nil {
			return fmt.Errorf("建 usage-catalog schema: %w", err)
		}
		shardStatement, err := tx.PrepareContext(w.ctx, `
			INSERT INTO usage_record_shards (
				shard_key, bucket_date, shard_id, file_path, schema_version, status,
				first_seen_at, last_write_at, last_error_message, created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, 'active', ?, ?, NULL, ?, ?)
			ON CONFLICT(shard_key) DO UPDATE SET
				bucket_date = excluded.bucket_date,
				shard_id = excluded.shard_id,
				file_path = excluded.file_path,
				schema_version = excluded.schema_version,
				status = 'active',
				updated_at = excluded.updated_at`)
		if err != nil {
			return fmt.Errorf("prepare usage_record_shards: %w", err)
		}
		defer shardStatement.Close()
		entryStatement, err := tx.PrepareContext(w.ctx, `
			INSERT INTO usage_record_shard_entries (
				usage_id, shard_key, system_account_id, trace_id, api_key_id, account_id, group_id, model,
				traffic_source, success, status_code, client_ip, first_token_ms, duration_ms, cost_usd,
				created_at, indexed_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(usage_id) DO UPDATE SET
				shard_key = excluded.shard_key,
				system_account_id = excluded.system_account_id,
				trace_id = excluded.trace_id,
				api_key_id = excluded.api_key_id,
				account_id = excluded.account_id,
				group_id = excluded.group_id,
				model = excluded.model,
				traffic_source = excluded.traffic_source,
				success = excluded.success,
				status_code = excluded.status_code,
				client_ip = excluded.client_ip,
				first_token_ms = excluded.first_token_ms,
				duration_ms = excluded.duration_ms,
				cost_usd = excluded.cost_usd,
				created_at = excluded.created_at,
				indexed_at = excluded.indexed_at`)
		if err != nil {
			return fmt.Errorf("prepare usage_record_shard_entries: %w", err)
		}
		defer entryStatement.Close()
		accountStatement, err := tx.PrepareContext(w.ctx, `
			INSERT INTO usage_record_account_shards (account_id, shard_key, first_created_at, last_seen_at)
			VALUES (?, ?, ?, ?)
			ON CONFLICT(account_id, shard_key) DO UPDATE SET
				first_created_at = MIN(usage_record_account_shards.first_created_at, excluded.first_created_at),
				last_seen_at = MAX(usage_record_account_shards.last_seen_at, excluded.last_seen_at)`)
		if err != nil {
			return fmt.Errorf("prepare usage_record_account_shards: %w", err)
		}
		defer accountStatement.Close()
		apiKeyStatement, err := tx.PrepareContext(w.ctx, `
			INSERT INTO usage_record_api_key_shards (api_key_id, system_account_id, shard_key, first_created_at, last_seen_at)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(api_key_id, system_account_id, shard_key) DO UPDATE SET
				first_created_at = MIN(usage_record_api_key_shards.first_created_at, excluded.first_created_at),
				last_seen_at = MAX(usage_record_api_key_shards.last_seen_at, excluded.last_seen_at)`)
		if err != nil {
			return fmt.Errorf("prepare usage_record_api_key_shards: %w", err)
		}
		defer apiKeyStatement.Close()

		for _, key := range keys {
			plan := plans[key]
			if _, err := shardStatement.ExecContext(w.ctx,
				plan.location.ShardKey, plan.location.BucketDate, plan.location.ShardID,
				plan.location.FilePath, usageRecordShardSchemaVersion, now, now, now, now); err != nil {
				return fmt.Errorf("登记分片 %s: %w", plan.location.ShardKey, err)
			}
			w.counts["usageRecordShards"]++
			accounts := map[string]usageScopeSpan{}
			var accountOrder []string
			apiKeys := map[string]usageScopeSpan{}
			var apiKeyOrder []string
			for _, record := range plan.records {
				createdAt := recordString(record, "created_at")
				if _, err := entryStatement.ExecContext(w.ctx,
					recordString(record, "id"),
					plan.location.ShardKey,
					recordString(record, "system_account_id"),
					recordString(record, "trace_id"),
					recordValue(record, "api_key_id"),
					recordValue(record, "account_id"),
					recordValue(record, "group_id"),
					recordValue(record, "model"),
					recordString(record, "traffic_source"),
					recordValue(record, "success"),
					recordValue(record, "status_code"),
					recordValue(record, "client_ip"),
					recordValue(record, "first_token_ms"),
					recordValue(record, "duration_ms"),
					recordValue(record, "cost_usd"),
					createdAt,
					now,
				); err != nil {
					return fmt.Errorf("登记记录 %s: %w", recordString(record, "id"), err)
				}
				w.counts["usageRecordShardEntries"]++
				if accountID := recordString(record, "account_id"); accountID != "" {
					if _, exists := accounts[accountID]; !exists {
						accountOrder = append(accountOrder, accountID)
					}
					accounts[accountID] = accounts[accountID].extend(createdAt)
				}
				apiKeyID := recordString(record, "api_key_id")
				systemAccountID := recordString(record, "system_account_id")
				if apiKeyID != "" && systemAccountID != "" {
					scopeKey := apiKeyID + "\x00" + systemAccountID
					if _, exists := apiKeys[scopeKey]; !exists {
						apiKeyOrder = append(apiKeyOrder, scopeKey)
					}
					apiKeys[scopeKey] = apiKeys[scopeKey].extend(createdAt)
				}
			}
			for _, accountID := range accountOrder {
				span := accounts[accountID]
				if _, err := accountStatement.ExecContext(w.ctx, accountID, plan.location.ShardKey, span.first, span.last); err != nil {
					return fmt.Errorf("登记账户分片 %s: %w", accountID, err)
				}
				w.counts["usageRecordAccountShards"]++
			}
			for _, scopeKey := range apiKeyOrder {
				parts := strings.SplitN(scopeKey, "\x00", 2)
				span := apiKeys[scopeKey]
				if _, err := apiKeyStatement.ExecContext(w.ctx, parts[0], parts[1], plan.location.ShardKey, span.first, span.last); err != nil {
					return fmt.Errorf("登记 Key 分片 %s: %w", parts[0], err)
				}
				w.counts["usageRecordApiKeyShards"]++
			}
		}
		return nil
	})
}

// usageScopeSpan 是作用域目录行的最小 / 最大时间跨度。
type usageScopeSpan struct {
	first string
	last  string
}

// extend 合并一条记录的时间跨度。
func (s usageScopeSpan) extend(createdAt string) usageScopeSpan {
	if s.first == "" || createdAt < s.first {
		s.first = createdAt
	}
	if createdAt > s.last {
		s.last = createdAt
	}
	return s
}

// writeStatsMirror 把记录镜像进 stats 库 usage_records（SQLite standalone 模式下
// statsagg 聚合器的唯一输入源；与分片写分属不同库文件，无法同事务）。
func (w *usageWriter) writeStatsMirror(entries []usageRecordEntry) error {
	if len(entries) == 0 {
		return nil
	}
	db, err := w.e.open(StoreStats)
	if err != nil {
		return err
	}
	if _, err := db.ExecContext(w.ctx, statsUsageRecordDDL); err != nil {
		return fmt.Errorf("建 stats usage_records 镜像: %w", err)
	}
	query := "INSERT INTO usage_records (" + strings.Join(statsUsageRecordColumns, ", ") + ") VALUES (" +
		sqlitePlaceholders(len(statsUsageRecordColumns)) + ") ON CONFLICT(id) DO NOTHING"
	tx, err := db.BeginTx(w.ctx, nil)
	if err != nil {
		return fmt.Errorf("stats 镜像 begin: %w", err)
	}
	statement, err := tx.PrepareContext(w.ctx, query)
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("stats 镜像 prepare: %w", err)
	}
	defer statement.Close()
	inserted := 0
	for _, entry := range entries {
		result, err := statement.ExecContext(w.ctx, recordValues(entry.columns, statsUsageRecordColumns)...)
		if err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("stats 镜像写入 %v: %w", entry.columns["id"], err)
		}
		if affected, err := result.RowsAffected(); err == nil && affected > 0 {
			inserted++
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("stats 镜像 commit: %w", err)
	}
	w.counts["statsUsageRecords"] += inserted
	return nil
}

// recordValues 按列序取出行参数；缺失列写 NULL（列存在但该记录不适用）。
func recordValues(record map[string]any, columns []string) []any {
	values := make([]any, len(columns))
	for index, column := range columns {
		values[index] = record[column]
	}
	return values
}

// recordValue 取列值，缺失为 NULL。
func recordValue(record map[string]any, column string) any {
	return record[column]
}

// recordString 取字符串列值，非字符串或缺失返回空串。
func recordString(record map[string]any, column string) string {
	value, ok := record[column].(string)
	if !ok {
		return ""
	}
	return value
}

// sqlitePlaceholders 生成 "?, ?, ?"（jobs 同形辅助）。
func sqlitePlaceholders(count int) string {
	return strings.TrimSuffix(strings.Repeat("?, ", count), ", ")
}
