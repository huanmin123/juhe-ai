package mockdata

// usage 域（T66 PG 造数第二波）：juhe_usage.usage_records 日分区明细 + 逻辑
// shard 四表登记。
//
// 范围（docs/functions/Mockdata造数设计.md §4）：按 --mockdata-days ×
// --mockdata-daily-requests 生成日常使用记录，外加「上游响应模型一致 / 映射后
// 不一致 / 未映射不一致」三条定向样本（trace 逐字见 usageTrace 常量）与模型
// 映射、models/images 端点覆盖样本；端点矩阵覆盖 OpenAI responses / chat
// completions / images / models、M1 同步音频（speech / transcriptions）与
// Anthropic messages / count_tokens；失败样本轮转覆盖 429/503/500/402/401。
// 明细外键一律取 business 域运行时查询到的真实 mock 资源 ID（账户、分组、
// Key、入口映射、授权实例与团队分组授权），查不到的资源字段写 NULL，不猜 ID。
//
// 边界：
//   - 只写原始事实：派生聚合族（usage_stats_*、usage_model_*、*_windows、
//     usage_rank_snapshots、account_health_hourly 等）一律不写，由 jobs 聚合 /
//     窗口任务重建（cleanup 已整表重置派生面）；
//   - PG 形态没有 sqlite 时代的 stats 库 usage_records 镜像表，不生成镜像；
//   - 记录 ID 与逻辑 shard 定位逐字对齐 jobs/internal/usagewriter/shards.go
//     （Go 三项目基线禁止 maintenance → jobs 依赖，算法在本文件对齐，由测试
//     钉住）；
//   - 记录列集与列型逐列对照 maintenance/internal/schema/pg_schema_usage.go：
//     bigint/integer 列写 Go int，double precision 列写 float64，时间列是
//     text（isoMillisLayout UTC 毫秒 ISO，分区键取其文本前缀）。

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ut 返回 juhe_usage schema 下的全限定表名（本文件所有 SQL 的唯一拼表口）。
func ut(table string) string { return qualified(SchemaUsage, table) }

// usageRecordColumns 是 juhe_usage.usage_records 的写入列序（逐列对照
// pg_schema_usage.go 的 usage_records DDL；与 jobs usagewriter
// UsageRecordColumns 同一物理列集）。列序只决定批量 INSERT 的参数顺序，
// 主键冲突目标是 (created_at, id)。
var usageRecordColumns = []string{
	"id",
	"system_account_id",
	"trace_id",
	"traffic_source",
	"client_ip",
	"api_key_id",
	"group_id",
	"route_group_id",
	"compaction_expected",
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
	"tts_input_chars",
	"audio_input_seconds",
	"usage_missing",
	"output_video_seconds",
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

// usageRecordShardSchemaVersion 与 jobs usagewriter 的常量同值（分片登记的
// schema_version 列）。
const usageRecordShardSchemaVersion = 8

// usageLogicalShardCount 与 jobs usagewriter DefaultUsageShardCount（
// JUHE_AI_USAGE_SHARD_COUNT 默认值 16）同值：逻辑 shard 路由的分片数。
const usageLogicalShardCount = 16

// usageRecordIDPattern 与 jobs usageRecordShardIDPattern 同形：从记录 ID 解析
// 桶日期与分片号（ID 内分片号是两位数字，逻辑 shard key 用三位数字，见
// usageLogicalShardLocationForRecord）。
var usageRecordIDPattern = regexp.MustCompile(`^usage_(\d{8})_s(\d+)_`)

// 上游响应模型三连样本的 trace（设计文档 §4 逐字要求；PG DDL 无派生布尔列，
// 一致 / 不一致只由 upstream_model 与 upstream_response_model 的原始值表达）。
const (
	usageTraceUpstreamMatch            = CleanupTracePrefix + "usage-coverage_upstream_response_model_match"
	usageTraceUpstreamMismatch         = CleanupTracePrefix + "usage-coverage_upstream_response_model_mismatch"
	usageTraceUpstreamUnmappedMismatch = CleanupTracePrefix + "usage-coverage_upstream_response_model_unmapped_mismatch"
	// usageMappingModel 是模型映射样本的请求模型（business 域
	// acc_standard 账户启用映射的 source_model）。
	usageMappingModel = "mockdata-global-long-context"
)

// usageResponseModelSuffix 是「上游返回了带日期后缀的真实模型」的模拟后缀：
// 它让 upstream_model <> upstream_response_model，从而命中不一致断言，同时又
// 不引入任何派生布尔列（只写原始字段）。
const usageResponseModelSuffix = "-2026-03-17"

// 覆盖样本的 ordinal 基址（与日常记录 ordinal 空间错开，保证 ID 熵不冲突）。
const (
	usageOrdinalCoverageMatch    = 20001
	usageOrdinalCoverageMismatch = 20002
	usageOrdinalCoverageUnmapped = 20003
	usageOrdinalCoverageMapping  = 20004
	usageOrdinalCoverageModels   = 20005
	usageOrdinalCoverageImage    = 20006
)

// usageEndpointVariant 是一条端点样本：method + path 是 jobs / gateway 记录的
// endpoint 形态（`<METHOD> <path>`），family 取 gatewayrouting 的端点族词汇表。
type usageEndpointVariant struct {
	method    string
	path      string
	family    string
	semantic  string
	stream    int
	imageMode bool
	// audioMode 标记 M1 同步音频端点（媒体契约 §3.4）：计量列走 TTS 字符 /
	// STT 秒（fillMetrics 的音频分支），模型取目录 audio 分类。
	audioMode bool
	// tokenless 标记不计 token 的端点（models 目录查询）。
	tokenless bool
}

// usageEndpointVariants 是端点样本矩阵：覆盖 OpenAI responses / chat
// completions / images / models、M1 同步音频（TTS speech / STT transcriptions）
// 与 Anthropic messages / count_tokens，流式与非流式混合。audio 端点的
// gatewayrouting 端点族解析为空（只认 chat/responses），family 保持空串不造词表。
var usageEndpointVariants = []usageEndpointVariant{
	{method: "POST", path: "/v1/responses", family: "responses", semantic: "openai", stream: 1},
	{method: "POST", path: "/v1/responses", family: "responses", semantic: "openai"},
	{method: "POST", path: "/v1/chat/completions", family: "chat_completions", semantic: "openai", stream: 1},
	{method: "POST", path: "/v1/chat/completions", family: "chat_completions", semantic: "openai"},
	{method: "POST", path: "/v1/images/generations", family: "chat_completions", semantic: "openai", imageMode: true},
	{method: "GET", path: "/v1/models", family: "models", semantic: "openai", tokenless: true},
	{method: "POST", path: "/v1/messages", family: "messages", semantic: "anthropic", stream: 1},
	{method: "POST", path: "/v1/messages/count_tokens", family: "count_tokens", semantic: "anthropic"},
	{method: "POST", path: "/v1/audio/speech", semantic: "openai", audioMode: true},
	{method: "POST", path: "/v1/audio/transcriptions", semantic: "openai", audioMode: true},
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

// 端点族与访问类型取值（gatewayrouting / openai-account-selector / jobs
// usagewriter 的词汇表）。statsagg 的 ShouldAggregateUsageStatsRecord 只认
// owner / account_authorized / group_authorized 三个账户访问类型，其他取值的
// 记录会被整条丢弃；team 来源让授权日报按团队维度展开。
const (
	usageAccessTypeOwner             = "owner"
	usageAccessTypeAuthorized        = "authorized"
	usageAccessTypeAccountAuthorized = "account_authorized"
	usageAccessTypeGroupAuthorized   = "group_authorized"
	usageAuthorizationSourceTypeTeam = "team"
)

// usageResources 是 usage 域从 juhe_business 运行时解析出的真实外键集合。
type usageResources struct {
	skip     string
	accounts []usageAccountRow
	groups   []usageGroupRow
	apiKeys  []usageAPIKeyRow
	mappings []usageMappingRow
	models   []string
	images   []string
	// audio 是目录里 audio lane 的模型（M1 同步音频样本的请求模型来源）。
	audio []string
	// 授权归属（statsagg 授权日报的聚合输入）。
	authInstances         []usageAuthInstanceRow
	authInstanceByAccount map[string]usageAuthInstanceRow
	groupAuths            []usageGroupAuthRow
}

type usageAccountRow struct {
	id     string
	owner  string
	vendor string
	preset string
	health string
	typ    string
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

// usageAuthInstanceRow 是一条授权实例账户的归属信息：business 域为每条活跃的
// 「账户级授权给系统账户」在被授权人命名空间建一个克隆账户，accounts 表上的
// authorization_instance_* 三列标识「该账户是某条授权的实例」。调用实例账户的
// 使用记录按它回填授权归属列，statsagg 的授权日报才能按
// 「账户归属人 != 调用者」分支聚出 authorization_user_usage_summary_daily。
type usageAuthInstanceRow struct {
	accountID       string // 实例账户 ID（usage_records.account_id）
	grantee         string // 实例账户归属 = 被授权人（调用者）
	authorizationID string // 授权运行时行 ID（resource_authorizations.id）
	sourceOwner     string // 原资源归属人（usage_records.account_owner_system_account_id）
	sourceType      string // 授权来源（resource_authorizations.effective_source_type）
	sourceTeamID    string // 授权来源团队（effective_source_team_id，manual 时为空）
}

// usageGroupAuthRow 是一条团队来源的分组授权运行时行（resource_authorizations
// 按成员扇出，每个在册成员一行）。造数用它构造「成员 Key 调用授权分组」场景，
// 回填 group_authorization_* 三列后，statsagg 的 group 分支带 team 来源命中
// authorization_team_usage_summary_daily。
type usageGroupAuthRow struct {
	authorizationID string
	groupID         string
	groupOwner      string
	sourceTeamID    string
	grantee         string // 该运行时行的被授权成员
}

// loadUsageResources 读取 juhe_business 的 mock 资源；缺 schema / seed 时返回
// skip 原因而不是报错（域跳过 = Counts 为空 = 覆盖报告按未接线归属）。
func loadUsageResources(ctx context.Context, e *env) (usageResources, error) {
	resources := usageResources{authInstanceByAccount: map[string]usageAuthInstanceRow{}}
	for _, table := range []string{"accounts", "groups", "api_keys", "account_connections", "account_entry_mappings", "models", "resource_authorizations"} {
		exists, err := tableExists(ctx, e.db, SchemaBusiness, table)
		if err != nil {
			return resources, err
		}
		if !exists {
			resources.skip = "juhe_business 缺少表 " + table + "（schema 未 ensure 或 business 域未执行）"
			return resources, nil
		}
	}

	accountRows, err := e.db.QueryContext(ctx, `SELECT id, system_account_id, COALESCE(vendor_code,''),
		COALESCE(health_check_model,''), COALESCE(type,'')
		FROM `+bt("accounts")+" WHERE id LIKE $1 ORDER BY id", CleanupIDPrefix+"%")
	if err != nil {
		return resources, fmt.Errorf("读取 mock 账户: %w", err)
	}
	for accountRows.Next() {
		var row usageAccountRow
		if err := accountRows.Scan(&row.id, &row.owner, &row.vendor, &row.health, &row.typ); err != nil {
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

	// 连接 preset 作为 usage_records.provider_protocol_profile_id 的投影
	//（provider_protocol_profiles 已退役，每账户取首个 enabled 连接）。查询
	// 串行执行：上一个流式结果集未 Close 前不在同库发起新查询。
	presetByAccount := map[string]string{}
	presetRows, err := e.db.QueryContext(ctx, `SELECT account_id, connection_preset_code FROM `+bt("account_connections")+`
		WHERE account_id LIKE $1 AND enabled = 1 ORDER BY account_id, connection_preset_code`, CleanupIDPrefix+"%")
	if err != nil {
		return resources, fmt.Errorf("读取 mock 账户连接: %w", err)
	}
	for presetRows.Next() {
		var accountID, preset string
		if err := presetRows.Scan(&accountID, &preset); err != nil {
			presetRows.Close()
			return resources, err
		}
		if _, ok := presetByAccount[accountID]; !ok {
			presetByAccount[accountID] = preset
		}
	}
	if err := presetRows.Err(); err != nil {
		presetRows.Close()
		return resources, err
	}
	presetRows.Close()
	for index, account := range resources.accounts {
		resources.accounts[index].preset = presetByAccount[account.id]
	}

	groupRows, err := e.db.QueryContext(ctx, `SELECT id, system_account_id, COALESCE(enabled,0)
		FROM `+bt("groups")+" WHERE id LIKE $1 ORDER BY id", CleanupIDPrefix+"%")
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

	keyRows, err := e.db.QueryContext(ctx, `SELECT id, system_account_id FROM `+bt("api_keys")+" WHERE id LIKE $1 ORDER BY id", CleanupIDPrefix+"%")
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

	// 账户入口映射：命中样本的 model_mapping_applied / model_mapping_source 与
	// upstream_model 都必须来自真实映射行。upstream_model 是映射 target 指向的
	// models 行（account_entry_mappings.target_model_id）。
	mappingRows, err := e.db.QueryContext(ctx, `SELECT m.account_id, m.source_model,
		COALESCE(models.upstream_model, m.target_model_id),
		m.source_endpoint_family, m.target_endpoint_family
		FROM `+bt("account_entry_mappings")+` m
		JOIN `+bt("models")+` ON models.id = m.target_model_id
		WHERE m.enabled = 1 ORDER BY m.account_id, m.source_model`)
	if err != nil {
		return resources, fmt.Errorf("读取账户入口映射: %w", err)
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

	// 授权实例账户：accounts.authorization_instance_* 标记「该账户是某条账户级
	// 授权的实例」，LEFT JOIN resource_authorizations 取授权来源（与 statsagg
	// 的授权链查找同形）。
	instanceRows, err := e.db.QueryContext(ctx, `SELECT a.id, a.system_account_id,
		COALESCE(a.authorization_instance_authorization_id,''),
		COALESCE(a.authorization_instance_owner_system_account_id,''),
		COALESCE(ra.effective_source_type,''), COALESCE(ra.effective_source_team_id,'')
		FROM `+bt("accounts")+` a
		LEFT JOIN `+bt("resource_authorizations")+` ra ON ra.id = a.authorization_instance_authorization_id
		WHERE a.id LIKE $1 AND a.authorization_instance_authorization_id IS NOT NULL
		ORDER BY a.id`, CleanupIDPrefix+"%")
	if err != nil {
		return resources, fmt.Errorf("读取授权实例账户: %w", err)
	}
	for instanceRows.Next() {
		var row usageAuthInstanceRow
		if err := instanceRows.Scan(&row.accountID, &row.grantee, &row.authorizationID,
			&row.sourceOwner, &row.sourceType, &row.sourceTeamID); err != nil {
			instanceRows.Close()
			return resources, err
		}
		if row.accountID == "" || row.authorizationID == "" || row.sourceOwner == "" || row.grantee == "" {
			// 缺任一归属字段就无法满足聚合器的分支条件，跳过该实例。
			continue
		}
		resources.authInstances = append(resources.authInstances, row)
		resources.authInstanceByAccount[row.accountID] = row
	}
	if err := instanceRows.Err(); err != nil {
		instanceRows.Close()
		return resources, err
	}
	instanceRows.Close()

	// 团队来源的分组授权运行时行：resource_authorizations 按成员扇出，每个在册
	// 成员各一行，status='active' 才代表当前可调用。
	groupAuthRows, err := e.db.QueryContext(ctx, `SELECT id, resource_id, resource_owner_system_account_id,
		COALESCE(effective_source_team_id,''), grantee_system_account_id
		FROM `+bt("resource_authorizations")+`
		WHERE resource_type = 'group' AND status = 'active' AND effective_source_type = 'team' AND id LIKE $1
		ORDER BY id`, CleanupIDPrefix+"%")
	if err != nil {
		return resources, fmt.Errorf("读取团队分组授权: %w", err)
	}
	for groupAuthRows.Next() {
		var row usageGroupAuthRow
		if err := groupAuthRows.Scan(&row.authorizationID, &row.groupID, &row.groupOwner,
			&row.sourceTeamID, &row.grantee); err != nil {
			groupAuthRows.Close()
			return resources, err
		}
		if row.authorizationID == "" || row.groupID == "" || row.groupOwner == "" ||
			row.sourceTeamID == "" || row.grantee == "" {
			continue
		}
		resources.groupAuths = append(resources.groupAuths, row)
	}
	if err := groupAuthRows.Err(); err != nil {
		groupAuthRows.Close()
		return resources, err
	}
	groupAuthRows.Close()

	modelRows, err := e.db.QueryContext(ctx, `SELECT upstream_model, supported_endpoint_modes_json FROM `+bt("models")+`
		WHERE status = 'active' AND catalog_visible ORDER BY catalog_order, upstream_model`)
	if err != nil {
		return resources, fmt.Errorf("读取模型目录: %w", err)
	}
	for modelRows.Next() {
		var (
			model     string
			modesJSON string
		)
		if err := modelRows.Scan(&model, &modesJSON); err != nil {
			modelRows.Close()
			return resources, err
		}
		var modes []string
		if err := json.Unmarshal([]byte(modesJSON), &modes); err != nil {
			modelRows.Close()
			return resources, fmt.Errorf("解析模型 %s 的 supported_endpoint_modes_json: %w", model, err)
		}
		resources.models = append(resources.models, model)
		for _, mode := range modes {
			if mode == "images_json" {
				resources.images = append(resources.images, model)
			}
			if mode == "audio_speech" || mode == "audio_transcription_json" {
				resources.audio = append(resources.audio, model)
			}
		}
	}
	if err := modelRows.Err(); err != nil {
		modelRows.Close()
		return resources, err
	}
	modelRows.Close()

	if len(resources.accounts) == 0 {
		resources.skip = "juhe_business 没有 mock 账户（请先执行 business 域造数）"
		return resources, nil
	}
	if len(resources.apiKeys) == 0 {
		resources.skip = "juhe_business 没有 mock API Key（请先执行 business 域造数）"
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

// modelAt 取第 index 个目录模型；目录为空时回落到映射样本模型名。
func (r usageResources) modelAt(index int) (string, bool) {
	if len(r.models) == 0 {
		return usageMappingModel, false
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

// audioModelAt 取第 index 个音频目录模型；目录里没有 audio 分类模型时回落
// 首个模型（保证音频样本不因目录版本缺 audio 行而失败）。
func (r usageResources) audioModelAt(index int) (string, bool) {
	if len(r.audio) == 0 {
		return r.modelAt(index)
	}
	if index < 0 {
		index = 0
	}
	return r.audio[index%len(r.audio)], true
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

// mappingFor 返回该账户在指定端点族上的启用映射（映射样本模型）。
func (r usageResources) mappingFor(accountID, family string) (usageMappingRow, bool) {
	for _, mapping := range r.mappings {
		if mapping.accountID != accountID {
			continue
		}
		if mapping.sourceModel != usageMappingModel {
			continue
		}
		if family != "" && mapping.sourceFamily != "" && mapping.sourceFamily != family {
			continue
		}
		return mapping, true
	}
	return usageMappingRow{}, false
}

// lookupCoverageMapping 选一条用于上游响应模型三连样本的映射：优先账户
// mockdata_acc_standard（business 域的映射样本账户），否则取第一条真实映射。
func (r usageResources) lookupCoverageMapping() (usageMappingRow, bool) {
	for _, mapping := range r.mappings {
		if mapping.accountID == CleanupIDPrefix+"acc_standard" && mapping.sourceModel == usageMappingModel {
			return mapping, true
		}
	}
	for _, mapping := range r.mappings {
		if mapping.accountID == CleanupIDPrefix+"acc_standard" {
			return mapping, true
		}
	}
	for _, mapping := range r.mappings {
		return mapping, true
	}
	return usageMappingRow{}, false
}

// keyForOwner 取该用户名下的一条 Key（授权场景用它把调用者绑定到被授权成员）。
func (r usageResources) keyForOwner(owner string) (usageAPIKeyRow, bool) {
	if owner == "" {
		return usageAPIKeyRow{}, false
	}
	for _, key := range r.apiKeys {
		if key.owner == owner {
			return key, true
		}
	}
	return usageAPIKeyRow{}, false
}

// accountForOwner 取该用户名下的第一条账户（团队授权场景用它挑分组归属人的
// 真实账户）；没有时回落第一个 mock 账户。
func (r usageResources) accountForOwner(owner string) usageAccountRow {
	for _, account := range r.accounts {
		if account.owner == owner {
			return account
		}
	}
	return r.accountAt(0)
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
	// caller 是记录的调用者（usage_records.system_account_id）：缺省等于账户
	// 归属人（自调语义）；团队授权分组场景里是被授权成员，与分组归属人错开，
	// statsagg 的 group 分支（group_owner != caller）才能命中。
	caller string
	// 团队授权分组归属：非空时记录回填 group_authorization_* 三列（来源恒为
	// team），来源取值以 resource_authorizations.effective_source_* 为准。
	groupAuthID     string
	groupAuthTeamID string
}

// usageRecordSpec 是一条记录的取值规格：newRecord 按它填全部列。
type usageRecordSpec struct {
	createdAt time.Time
	traceID   string
	scenario  usageScenario
	variant   usageEndpointVariant
	model     string
	// upstreamModel 省略时等于 model。
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
	audioMode             bool
	tokenless             bool
	// ordinal 只用于生成确定性的 token / 延迟数值与 ID 熵。
	ordinal int
}

// usageWriter 承载一次 usage 域写入：统一时钟、真实资源集合、分区备忘与按表
// 统计的行数（DomainResult.Counts 直接取它）。
type usageWriter struct {
	ctx        context.Context
	env        *env
	now        time.Time
	resources  usageResources
	partitions *partitionMemo
	counts     map[string]int
}

// seedUsage 是 usage 域入口（domainRegistry 的 Run 指向它）。
func seedUsage(ctx context.Context, e *env) (DomainResult, error) {
	resources, err := loadUsageResources(ctx, e)
	if err != nil {
		return DomainResult{}, err
	}
	if resources.skip != "" {
		// 不静默降级：Counts 为空即「未产出」，覆盖报告据此把 usage 域断言标成
		// NotCovered，让「usage schema 为什么没数据」可查。
		e.logger.Info("mockdata usage 域跳过", "reason", resources.skip)
		return DomainResult{Name: DomainUsage}, nil
	}
	writer := &usageWriter{
		ctx:        ctx,
		env:        e,
		now:        e.options.clock(),
		resources:  resources,
		partitions: &partitionMemo{},
		counts:     map[string]int{},
	}
	if err := writer.seed(); err != nil {
		return DomainResult{}, err
	}
	return DomainResult{Name: DomainUsage, Counts: writer.counts}, nil
}

// seed 生成全部记录并落库：分区 ensure → 单事务写 usage_records 批量 INSERT +
// shard 四表登记。
func (w *usageWriter) seed() error {
	scenarios := w.buildScenarios()
	if len(scenarios) == 0 {
		return fmt.Errorf("mockdata usage 域没有可用场景（business 资源为空）")
	}
	records := w.buildDailyRecords(scenarios)
	records = append(records, w.buildCoverageRecords(scenarios)...)
	return w.write(records)
}

// buildScenarios 为每条 mock API Key 构造「Key + 同归属人账户 + 同归属人分组」
// 场景；账户归属人没有自有账户时回落到任意账户（记录保持真实外键，缺失归属
// 组合写日志留痕）。
func (w *usageWriter) buildScenarios() []usageScenario {
	byOwner := map[string][]usageAccountRow{}
	for _, account := range w.resources.accounts {
		byOwner[account.owner] = append(byOwner[account.owner], account)
	}
	var scenarios []usageScenario
	for keyIndex, key := range w.resources.apiKeys {
		accounts := byOwner[key.owner]
		if len(accounts) == 0 {
			w.env.logger.Info("mockdata usage 场景回落：Key 归属人没有自有账户", "api_key", key.id)
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
				w.env.logger.Info("mockdata usage 场景缺分组：记录 group 列保持 NULL", "api_key", key.id)
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
	scenarios = append(scenarios, w.buildGroupAuthScenarios(scenarios)...)
	return scenarios
}

// buildGroupAuthScenarios 为每条团队来源的分组授权运行时行构造「被授权成员的
// Key 调用授权分组」场景：caller=成员、分组归属人=资源 owner，两者错开才能
// 命中 statsagg group 分支，group_authorization_source_type='team' + team id
// 让 authorization_team_usage_summary_daily 出数。账户取分组归属人的真实账户
// （记录的账户列保持 owner 语义，授权归属只由 group 列承载）。
func (w *usageWriter) buildGroupAuthScenarios(existing []usageScenario) []usageScenario {
	scenarios := make([]usageScenario, 0, len(w.resources.groupAuths))
	for _, auth := range w.resources.groupAuths {
		key, ok := w.resources.keyForOwner(auth.grantee)
		if !ok {
			// 该成员没有 mock Key：无法构造可信的调用者，跳过并留痕。
			w.env.logger.Info("mockdata usage 团队授权场景跳过：成员没有 mock Key", "grantee", auth.grantee)
			continue
		}
		duplicate := false
		for _, scenario := range existing {
			if scenario.groupID == auth.groupID && scenario.apiKeyID == key.id {
				duplicate = true
				break
			}
		}
		if duplicate {
			continue
		}
		scenarios = append(scenarios, usageScenario{
			apiKeyID:        key.id,
			apiKeyOwner:     key.owner,
			caller:          auth.grantee,
			groupID:         auth.groupID,
			groupOwner:      auth.groupOwner,
			account:         w.resources.accountForOwner(auth.groupOwner),
			clientIPBase:    fmt.Sprintf("10.20.%d.", (len(existing)+len(scenarios))%250),
			trafficSource:   usageTrafficSourceForOrdinal(len(existing) + len(scenarios)),
			groupAuthID:     auth.authorizationID,
			groupAuthTeamID: auth.sourceTeamID,
		})
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

// usageRecordEntry 是一条待落库记录：列取值 + 逻辑 shard 定位。
type usageRecordEntry struct {
	columns  map[string]any
	location usageLogicalShardLocation
}

// buildDailyRecords 生成近 Options.Days 天的日常记录：每天 DailyRequests 条，
// 时间均匀铺开且分钟偏移确定（同一 Now 下逐字节可复现）。
func (w *usageWriter) buildDailyRecords(scenarios []usageScenario) []usageRecordEntry {
	days := w.env.options.Days
	daily := w.env.options.DailyRequests
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
			entries = append(entries, w.newRecord(w.dailySpec(ordinal, createdAt, scenario)))
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
		audioMode:     variant.audioMode,
		tokenless:     variant.tokenless,
		ordinal:       ordinal,
	}
	requestedModel := scenario.account.health
	if model, ok := w.resources.modelAt(ordinal); ok {
		requestedModel = model
	}
	if variant.imageMode {
		requestedModel, _ = w.resources.imageModelAt(ordinal)
	}
	if variant.audioMode {
		requestedModel, _ = w.resources.audioModelAt(ordinal)
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
	// 思考强度：requests/effective 分别覆盖 low / medium / high 且允许不一致
	//（图像/音频/tokenless 端点无思考强度概念，不参与）。
	if !variant.imageMode && !variant.audioMode && !variant.tokenless && variant.family != "count_tokens" {
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

// buildCoverageRecords 生成上游响应模型三连定向样本与端点覆盖样本。
//
// 三连样本的语义（设计文档 §4）：
//   - match：请求映射源模型，实际发送 upstream_model，上游原样返回同一模型；
//   - mismatch：请求映射源模型，上游返回带日期后缀的真实模型（映射后不一致）；
//   - unmapped_mismatch：请求并实际发送未映射的 upstream_model，上游仍返回带
//     后缀模型（无映射不一致，model_mapping_applied=0）。
//
// 映射行来自 business 域的真实启用映射；没有映射时按目录模型兜底并写日志，
// 保证三连样本这一硬覆盖点不因上游目录版本差异整批失败。
func (w *usageWriter) buildCoverageRecords(scenarios []usageScenario) []usageRecordEntry {
	mapping, mappingOK := w.resources.lookupCoverageMapping()
	scenario, scenarioOK := usageScenarioForAccount(scenarios, mapping.accountID)
	if !scenarioOK {
		scenario = scenarios[0]
	}
	source := mapping.sourceModel
	upstream := mapping.upstreamModel
	if !mappingOK {
		w.env.logger.Info("mockdata usage 覆盖样本使用合成映射：business 域没有启用映射行")
		source = usageMappingModel
		if model, ok := w.resources.modelAt(1); ok {
			upstream = model
		} else {
			upstream = usageMappingModel
		}
	}
	if strings.TrimSpace(source) == "" {
		source = usageMappingModel
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

	match := base(usageTraceUpstreamMatch, w.now.Add(-1*time.Hour), usageOrdinalCoverageMatch)
	match.model = source
	match.upstreamModel = upstream
	match.upstreamResponseModel = upstream
	match.mappingApplied = 1
	match.mappingSource = "account_mapping"

	mismatch := base(usageTraceUpstreamMismatch, w.now.Add(-2*time.Hour), usageOrdinalCoverageMismatch)
	mismatch.model = source
	mismatch.upstreamModel = upstream
	mismatch.upstreamResponseModel = upstream + usageResponseModelSuffix
	mismatch.mappingApplied = 1
	mismatch.mappingSource = "account_mapping"

	unmapped := base(usageTraceUpstreamUnmappedMismatch, w.now.Add(-3*time.Hour), usageOrdinalCoverageUnmapped)
	// 未映射样本：请求模型本身就是要发送的上游模型，映射未命中。
	unmapped.model = upstream
	unmapped.upstreamModel = upstream
	unmapped.upstreamResponseModel = upstream + usageResponseModelSuffix

	modelMapping := base(CleanupTracePrefix+"usage-coverage_model_mapping", w.now.Add(-4*time.Hour), usageOrdinalCoverageMapping)
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
	//（含图片 token 字段）在明细里可见。
	modelsSpec := base(CleanupTracePrefix+"usage-coverage_models_endpoint", w.now.Add(-5*time.Hour), usageOrdinalCoverageModels)
	modelsSpec.variant = usageEndpointVariants[5]
	modelsSpec.tokenless = true
	modelsSpec.model, modelsSpec.upstreamModel = source, source
	modelsSpec.effectiveTier, modelsSpec.reportedTier, modelsSpec.billedTier = "default", "default", "default"
	modelsSpec.requestedEffort, modelsSpec.effectiveEffort = "", ""
	entries = append(entries, w.newRecord(modelsSpec))

	imageSpec := base(CleanupTracePrefix+"usage-coverage_image_endpoint", w.now.Add(-6*time.Hour), usageOrdinalCoverageImage)
	imageSpec.variant = usageEndpointVariants[4]
	imageSpec.imageMode = true
	imageSpec.stream = 0
	imageSpec.requestedEffort, imageSpec.effectiveEffort = "", ""
	imageModel, _ := w.resources.imageModelAt(0)
	imageSpec.model, imageSpec.upstreamModel = imageModel, imageModel
	entries = append(entries, w.newRecord(imageSpec))
	return entries
}

// usageScenarioForAccount 找该账户的既有场景（覆盖样本复用其 Key / 分组外键）。
func usageScenarioForAccount(scenarios []usageScenario, accountID string) (usageScenario, bool) {
	for _, scenario := range scenarios {
		if scenario.account.id == accountID {
			return scenario, true
		}
	}
	return usageScenario{}, false
}

// newRecord 把规格展开成全部列取值与逻辑 shard 定位。
func (w *usageWriter) newRecord(spec usageRecordSpec) usageRecordEntry {
	createdAtText := spec.createdAt.UTC().Format(isoMillisLayout)
	shard := usageLogicalShardLocationForRecord(
		usageRecordID(spec.createdAt, spec.traceID, spec.ordinal), createdAtText)

	// 调用者：显式给出时（团队授权分组场景）是被授权成员；缺省是账户归属人
	//（自调语义）。
	caller := spec.scenario.caller
	if caller == "" {
		caller = spec.scenario.account.owner
	}
	accountOwner := spec.scenario.account.owner
	accountAccessType := usageAccessTypeValue(spec.scenario.account.owner, spec.scenario.apiKeyOwner)
	if spec.scenario.groupAuthID != "" {
		// 团队授权分组场景：调用者是被授权成员，账户是分组归属人的账户，经授权
		// 分组访问——account_access_type 必须用 group_authorized 词汇（跨归属人
		// 的 "authorized" 不是合法账户访问类型，聚合器会丢弃整条记录）。
		accountAccessType = usageAccessTypeGroupAuthorized
	}
	upstreamModel := spec.upstreamModel
	if upstreamModel == "" {
		upstreamModel = spec.model
	}
	columns := map[string]any{
		"id":                           shard.RecordID,
		"system_account_id":            caller,
		"trace_id":                     spec.traceID,
		"traffic_source":               spec.scenario.trafficSource,
		"endpoint":                     spec.variant.method + " " + spec.variant.path,
		"provider_code":                nullString(spec.scenario.account.vendor),
		"provider_protocol_profile_id": nullString(spec.scenario.account.preset),
		"usage_semantic":               spec.variant.semantic,
		"model":                        nullString(spec.model),
		"upstream_model":               nullString(upstreamModel),
		"upstream_response_model":      nullString(upstreamResponseModelOf(spec, upstreamModel)),
		"pricing_model":                nullString(spec.model),
		"requested_service_tier":       spec.requestedTier,
		"effective_service_tier":       spec.effectiveTier,
		"reported_service_tier":        nullString(spec.reportedTier),
		"billed_service_tier":          spec.billedTier,
		"requested_reasoning_effort":   nullString(spec.requestedEffort),
		"effective_reasoning_effort":   nullString(spec.effectiveEffort),
		"cost_breakdown_snapshot_json": nullString(w.costBreakdown(spec)),
		"model_mapping_applied":        spec.mappingApplied,
		"model_mapping_source":         nullString(spec.mappingSource),
		"source_endpoint_family":       spec.variant.family,
		"upstream_endpoint_family":     spec.variant.family,
		"stream":                       spec.stream,
		"status_code":                  spec.statusCode,
		"success":                      boolInt(spec.success),
		// 压缩标记列（使用记录延迟健康分档设计）：mock 记录均非压缩请求，恒 0。
		"compaction_expected":             0,
		"failure_attribution":             nullString(spec.failureAttribution),
		"error_code":                      nullString(spec.errorCode),
		"error_message":                   nullString(spec.errorMessage),
		"client_ip":                       spec.scenario.clientIPBase + strconv.Itoa(1+spec.ordinal%250),
		"created_at":                      createdAtText,
		"account_owner_system_account_id": nullString(accountOwner),
		"account_access_type":             accountAccessType,
		// 媒体计量列（NOT NULL DEFAULT 0）：先落 0 默认，音频分支在 fillMetrics
		// 里覆盖 TTS 字符 / STT 秒（失败行保持 0，不虚计）。
		"tts_input_chars":      int64(0),
		"audio_input_seconds":  float64(0),
		"usage_missing":        int64(0),
		"output_video_seconds": float64(0),
	}
	// 授权实例账户归属：调用者是被授权人（实例账户 namespace 的归属者）时，账户
	// 归属改写为原资源归属人并回填授权三列。statsagg 授权日报的 account 分支
	//（account_owner != caller）据此聚出 authorization_user_usage_summary_daily。
	if instance, ok := w.resources.authInstanceByAccount[spec.scenario.account.id]; ok && caller == instance.grantee {
		columns["account_owner_system_account_id"] = nullString(instance.sourceOwner)
		columns["account_access_type"] = usageAccessTypeAccountAuthorized
		columns["account_authorization_id"] = instance.authorizationID
		columns["account_authorization_source_type"] = nullString(instance.sourceType)
		columns["account_authorization_source_team_id"] = nullString(instance.sourceTeamID)
	}
	if spec.scenario.groupID != "" {
		columns["group_id"] = spec.scenario.groupID
		columns["group_owner_system_account_id"] = nullString(spec.scenario.groupOwner)
		columns["group_access_type"] = usageAccessTypeValue(spec.scenario.groupOwner, spec.scenario.apiKeyOwner)
		// 团队授权分组：group 分支（group_owner != caller）带 team 来源展开。
		if spec.scenario.groupAuthID != "" {
			columns["group_authorization_id"] = spec.scenario.groupAuthID
			columns["group_authorization_source_type"] = usageAuthorizationSourceTypeTeam
			columns["group_authorization_source_team_id"] = nullString(spec.scenario.groupAuthTeamID)
		}
	}
	if spec.scenario.apiKeyID != "" {
		columns["api_key_id"] = spec.scenario.apiKeyID
	}
	if spec.scenario.account.id != "" {
		columns["account_id"] = spec.scenario.account.id
	}
	w.fillMetrics(spec, columns)
	return usageRecordEntry{columns: columns, location: shard}
}

// fillMetrics 填 token / 延迟 / 成本列：失败与不计 token 的端点留 NULL，
// 图像端点带图片 token，音频端点带 TTS 字符 / STT 秒计量，成功文本请求带流式
// 首 token 延迟。
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
	if spec.audioMode {
		// M1 同步音频计量行（媒体契约 §3.4）：TTS 无上游 usage 回报，网关按请求
		// input 字符数自算（tts_input_chars）；STT 按输入秒（audio_input_seconds）。
		// token 列不虚计。
		audioFirstToken := 140 + spec.ordinal%700
		columns["first_token_ms"] = audioFirstToken
		columns["duration_ms"] = audioFirstToken + 500 + spec.ordinal%1600
		if spec.variant.path == "/v1/audio/speech" {
			columns["tts_input_chars"] = int64(420 + spec.ordinal%2800)
			columns["cost_usd"] = usageCostUSD(spec.ordinal, 600, 180, spec.billedTier)
			return
		}
		columns["audio_input_seconds"] = float64(12+spec.ordinal%48) + 0.5
		columns["cost_usd"] = usageCostUSD(spec.ordinal, 800, 220, spec.billedTier)
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

// costBreakdown 生成计价快照：只有非 default 档位才有档位计价文档，default
// 档位留空表示没有档位计价来源。
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
func upstreamResponseModelOf(spec usageRecordSpec, upstreamModel string) string {
	if spec.upstreamResponseModel != "" {
		return spec.upstreamResponseModel
	}
	return upstreamModel
}

// usageAccessTypeValue 判断访问类型：归属人自己调用是 owner，他人调用是
// authorized。
func usageAccessTypeValue(resourceOwner, caller string) string {
	if resourceOwner != "" && resourceOwner == caller {
		return usageAccessTypeOwner
	}
	return usageAccessTypeAuthorized
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

// usageLogicalShardLocation 是 PG 逻辑分片的定位结果（jobs
// usagewriter.UsageRecordShardLocation 的同形投影：shard_key、bucket_date、
// 分片号与逻辑 file_path），额外携带记录 ID 供写入方复用。
type usageLogicalShardLocation struct {
	RecordID      string
	ShardKey      string
	BucketDate    string
	BucketDateKey string
	ShardID       int
	FilePath      string
}

// usageRecordID 复刻 jobs GenerateUsageRecordID 的 ID 形状：
// usage_<bucketDateKey>_s<两位分片号>_<毫秒>_<熵（alnum，截断 24）>。
func usageRecordID(createdAt time.Time, traceID string, ordinal int) string {
	bucketDateKey := createdAt.UTC().Format("20060102")
	entropy := usageShardEntropy(traceID, ordinal)
	shardID := usageStableShardID(entropy, usageLogicalShardCount)
	return fmt.Sprintf("usage_%s_s%02d_%d_%s", bucketDateKey, shardID, createdAt.UnixMilli(), entropy)
}

// usageShardEntropy 复刻 jobs SanitizeShardEntropy：只保留 [A-Za-z0-9]、截断
// 24 字符；熵里带造数标识便于人眼识别记录来源。
func usageShardEntropy(traceID string, ordinal int) string {
	raw := "mockdataUsage" + strconv.Itoa(ordinal) + traceID
	var builder strings.Builder
	for _, r := range raw {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			builder.WriteRune(r)
			if builder.Len() >= 24 {
				break
			}
		}
	}
	return builder.String()
}

// usageStableShardID 复刻 jobs StableShardID（FNV-1a 32 位模分片数）：ID 内的
// 分片号必须与 jobs 的路由算法同源。
func usageStableShardID(value string, shardCount int) int {
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

// usageLogicalShardID 复刻 jobs LogicalShardID（31 乘子多项式哈希模分片数）：
// ID 不含可解析分片号时的 PG 逻辑分片路由。
func usageLogicalShardID(value string, shardCount int) int {
	if shardCount < 1 {
		shardCount = 1
	}
	hash := uint32(0)
	for index := 0; index < len(value); index++ {
		hash = hash*31 + uint32(value[index])
	}
	return int(hash) % shardCount
}

// usageLogicalShardLocationForRecord 复刻 jobs
// UsageRecordLogicalShardLocationForPostgres：从记录 ID 解析桶日期与分片号
// （解析失败回落 createdAt 桶 + 逻辑哈希），shard_key 用三位分片号，file_path
// 是逻辑定位 `postgres:juhe_usage.usage_records:<shardKey>`。
func usageLogicalShardLocationForRecord(recordID, createdAt string) usageLogicalShardLocation {
	bucketDateKey := ""
	shardID := 0
	if match := usageRecordIDPattern.FindStringSubmatch(strings.TrimSpace(recordID)); match != nil {
		bucketDateKey = match[1]
		if parsed, err := strconv.Atoi(match[2]); err == nil && parsed >= 0 {
			shardID = parsed
		} else {
			shardID = usageLogicalShardID(recordID, usageLogicalShardCount)
		}
	} else {
		bucketDateKey = usageBucketDateKeyFromISO(createdAt)
		shardID = usageLogicalShardID(recordID, usageLogicalShardCount)
	}
	bucketDate := bucketDateKey[0:4] + "-" + bucketDateKey[4:6] + "-" + bucketDateKey[6:8]
	shardKey := bucketDateKey + ":s" + fmt.Sprintf("%03d", shardID)
	return usageLogicalShardLocation{
		RecordID:      recordID,
		ShardKey:      shardKey,
		BucketDate:    bucketDate,
		BucketDateKey: bucketDateKey,
		ShardID:       shardID,
		FilePath:      "postgres:juhe_usage.usage_records:" + shardKey,
	}
}

// usageBucketDateKeyFromISO 从 created_at 文本取 YYYYMMDD（UTC）。文本来自本域
// 的 isoMillisLayout 输出，前缀必是合法日期；防御性回退纪元值（调用方保证只
// 处理自己生成的文本）。
func usageBucketDateKeyFromISO(value string) string {
	if dateKey, ok := partitionDateKeyFromISO(value); ok {
		return dateKey
	}
	return "19700101"
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

// usage 写入列序（逐列对照 pg_schema_usage.go 的四张登记表 DDL）。
var (
	usageShardColumns = []string{
		"shard_key", "bucket_date", "shard_id", "file_path", "schema_version",
		"status", "first_seen_at", "last_write_at", "last_error_message",
		"created_at", "updated_at",
	}
	usageShardEntryColumns = []string{
		"usage_id", "shard_key", "system_account_id", "trace_id", "api_key_id",
		"account_id", "group_id", "model", "traffic_source", "success",
		"status_code", "client_ip", "first_token_ms", "duration_ms", "cost_usd",
		"created_at", "indexed_at",
	}
	usageAccountShardColumns = []string{
		"account_id", "shard_key", "first_created_at", "last_seen_at",
	}
	usageAPIKeyShardColumns = []string{
		"api_key_id", "system_account_id", "shard_key", "first_created_at", "last_seen_at",
	}
)

// write 落库：插入前 ensure 批内全部日分区，然后单事务写 usage_records 批量
// INSERT（ON CONFLICT(created_at, id) DO NOTHING，与 jobs PostgresShardStore
// 的冲突目标一致）与 shard 四表登记。
func (w *usageWriter) write(records []usageRecordEntry) error {
	if len(records) == 0 {
		return nil
	}
	// 1. 分组：shard_key 首次出现顺序即登记顺序（确定性）。
	createdAts := make([]string, 0, len(records))
	shardKeys := make([]string, 0, len(records))
	shardRows := map[string][][]any{}
	entryRows := make([][]any, 0, len(records))
	accountSpans := map[string]usageScopeSpan{}
	var accountOrder []string
	apiKeySpans := map[string]usageScopeSpan{}
	var apiKeyOrder []string
	now := w.now.UTC().Format(isoMillisLayout)
	for _, record := range records {
		columns := record.columns
		createdAt := recordString(columns, "created_at")
		createdAts = append(createdAts, createdAt)
		shard := record.location
		if _, exists := shardRows[shard.ShardKey]; !exists {
			shardKeys = append(shardKeys, shard.ShardKey)
		}
		shardRows[shard.ShardKey] = append(shardRows[shard.ShardKey], []any{
			shard.ShardKey, shard.BucketDate, shard.ShardID, shard.FilePath,
			usageRecordShardSchemaVersion, "active", now, now, nil, now, now,
		})
		entryRows = append(entryRows, []any{
			recordString(columns, "id"),
			shard.ShardKey,
			recordString(columns, "system_account_id"),
			recordString(columns, "trace_id"),
			recordValue(columns, "api_key_id"),
			recordValue(columns, "account_id"),
			recordValue(columns, "group_id"),
			recordValue(columns, "model"),
			recordString(columns, "traffic_source"),
			recordValue(columns, "success"),
			recordValue(columns, "status_code"),
			recordValue(columns, "client_ip"),
			recordValue(columns, "first_token_ms"),
			recordValue(columns, "duration_ms"),
			recordValue(columns, "cost_usd"),
			createdAt,
			now,
		})
		if accountID := recordString(columns, "account_id"); accountID != "" {
			scopeKey := accountID + "\x00" + shard.ShardKey
			if _, exists := accountSpans[scopeKey]; !exists {
				accountOrder = append(accountOrder, scopeKey)
			}
			accountSpans[scopeKey] = accountSpans[scopeKey].extend(createdAt)
		}
		apiKeyID := recordString(columns, "api_key_id")
		systemAccountID := recordString(columns, "system_account_id")
		if apiKeyID != "" && systemAccountID != "" {
			scopeKey := apiKeyID + "\x00" + systemAccountID + "\x00" + shard.ShardKey
			if _, exists := apiKeySpans[scopeKey]; !exists {
				apiKeyOrder = append(apiKeyOrder, scopeKey)
			}
			apiKeySpans[scopeKey] = apiKeySpans[scopeKey].extend(createdAt)
		}
	}

	// 2. 分区 ensure：批内 created_at 的日分区（autocommit DDL，父表锁窗口
	//    不跨事务，与 jobs 写入路径一致）。
	if err := ensureUsageRecordPartitions(w.ctx, w.env.DB(), w.partitions, createdAts); err != nil {
		return fmt.Errorf("mockdata usage 域 ensure 日分区失败: %w", err)
	}

	// 3. 单事务写明细 + 登记（FK 子表随父表登记行同事务落库）。
	err := w.env.tx(w.ctx, func(tx *sql.Tx) error {
		rows := make([][]any, 0, len(records))
		for _, record := range records {
			values, err := w.recordRow(record.columns)
			if err != nil {
				return err
			}
			rows = append(rows, values)
		}
		if err := usageBatchInsertTx(w.ctx, tx, ut("usage_records"), usageRecordColumns, rows,
			" ON CONFLICT (created_at, id) DO NOTHING"); err != nil {
			return err
		}
		// shard 登记行：upsert 保持 first_seen_at/created_at，其余随本批刷新。
		if err := usageBatchInsertTx(w.ctx, tx, ut("usage_record_shards"), usageShardColumns,
			flattenShardRows(shardKeys, shardRows),
			" ON CONFLICT (shard_key) DO UPDATE SET bucket_date = excluded.bucket_date, shard_id = excluded.shard_id, "+
				"file_path = excluded.file_path, schema_version = excluded.schema_version, status = 'active', "+
				"last_write_at = excluded.last_write_at, updated_at = excluded.updated_at"); err != nil {
			return err
		}
		if err := usageBatchInsertTx(w.ctx, tx, ut("usage_record_shard_entries"), usageShardEntryColumns, entryRows,
			" ON CONFLICT (usage_id) DO UPDATE SET shard_key = excluded.shard_key, system_account_id = excluded.system_account_id, "+
				"trace_id = excluded.trace_id, api_key_id = excluded.api_key_id, account_id = excluded.account_id, "+
				"group_id = excluded.group_id, model = excluded.model, traffic_source = excluded.traffic_source, "+
				"success = excluded.success, status_code = excluded.status_code, client_ip = excluded.client_ip, "+
				"first_token_ms = excluded.first_token_ms, duration_ms = excluded.duration_ms, cost_usd = excluded.cost_usd, "+
				"created_at = excluded.created_at, indexed_at = excluded.indexed_at"); err != nil {
			return err
		}
		accountScopeTable := quoteQualified(ut("usage_record_account_shards"))
		accountRows := make([][]any, 0, len(accountOrder))
		for _, scopeKey := range accountOrder {
			span := accountSpans[scopeKey]
			parts := strings.SplitN(scopeKey, "\x00", 2)
			accountRows = append(accountRows, []any{parts[0], parts[1], span.first, span.last})
		}
		if err := usageBatchInsertTx(w.ctx, tx, ut("usage_record_account_shards"), usageAccountShardColumns, accountRows,
			" ON CONFLICT (account_id, shard_key) DO UPDATE SET "+
				"first_created_at = LEAST("+accountScopeTable+".first_created_at, excluded.first_created_at), "+
				"last_seen_at = GREATEST("+accountScopeTable+".last_seen_at, excluded.last_seen_at)"); err != nil {
			return err
		}
		apiKeyScopeTable := quoteQualified(ut("usage_record_api_key_shards"))
		apiKeyRows := make([][]any, 0, len(apiKeyOrder))
		for _, scopeKey := range apiKeyOrder {
			span := apiKeySpans[scopeKey]
			parts := strings.SplitN(scopeKey, "\x00", 3)
			apiKeyRows = append(apiKeyRows, []any{parts[0], parts[1], parts[2], span.first, span.last})
		}
		if err := usageBatchInsertTx(w.ctx, tx, ut("usage_record_api_key_shards"), usageAPIKeyShardColumns, apiKeyRows,
			" ON CONFLICT (api_key_id, system_account_id, shard_key) DO UPDATE SET "+
				"first_created_at = LEAST("+apiKeyScopeTable+".first_created_at, excluded.first_created_at), "+
				"last_seen_at = GREATEST("+apiKeyScopeTable+".last_seen_at, excluded.last_seen_at)"); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}

	w.counts["usage_records"] = len(records)
	w.counts["usage_record_shards"] = len(shardKeys)
	w.counts["usage_record_shard_entries"] = len(entryRows)
	w.counts["usage_record_account_shards"] = len(accountOrder)
	w.counts["usage_record_api_key_shards"] = len(apiKeyOrder)
	return nil
}

// recordRow 把记录列映射投影成 usageRecordColumns 序的参数行；缺失列写 NULL
// （列存在但该记录不适用）。
func (w *usageWriter) recordRow(columns map[string]any) ([]any, error) {
	row := make([]any, len(usageRecordColumns))
	for index, column := range usageRecordColumns {
		row[index] = columns[column]
	}
	return row, nil
}

// flattenShardRows 按登记顺序展开每个 shard 的单行登记（每 shard 一行，不是
// 每记录一行）。
func flattenShardRows(shardKeys []string, shardRows map[string][][]any) [][]any {
	rows := make([][]any, 0, len(shardKeys))
	for _, key := range shardKeys {
		rows = append(rows, shardRows[key][0])
	}
	return rows
}

// usageBatchInsertTx 批量多行 INSERT：列名来自本包常量、表名全限定，行分块
// 保证单语句参数数不超过 PG 协议上限。conflict 直接追加在语句尾（含前导空格
// 的完整冲突子句）。
func usageBatchInsertTx(ctx context.Context, tx *sql.Tx, schemaTable string, columns []string, rows [][]any, conflict string) error {
	if len(rows) == 0 {
		return nil
	}
	const maxParamsPerStatement = 5000
	rowsPerStatement := maxParamsPerStatement / len(columns)
	if rowsPerStatement < 1 {
		return fmt.Errorf("批量写入 %s 的列数 %d 超过单语句参数预算", schemaTable, len(columns))
	}
	quotedColumns := make([]string, len(columns))
	for index, column := range columns {
		quotedColumns[index] = quotePGIdentifier(column)
	}
	for start := 0; start < len(rows); start += rowsPerStatement {
		end := start + rowsPerStatement
		if end > len(rows) {
			end = len(rows)
		}
		var valueTuples []string
		args := make([]any, 0, (end-start)*len(columns))
		for _, row := range rows[start:end] {
			if len(row) != len(columns) {
				return fmt.Errorf("批量写入 %s 的行参数数 %d 与列数 %d 不一致", schemaTable, len(row), len(columns))
			}
			placeholders := make([]string, len(columns))
			for index := range columns {
				placeholders[index] = "$" + strconv.Itoa(len(args)+index+1)
			}
			valueTuples = append(valueTuples, "("+strings.Join(placeholders, ", ")+")")
			args = append(args, row...)
		}
		statement := "INSERT INTO " + quoteQualified(schemaTable) +
			" (" + strings.Join(quotedColumns, ", ") + ") VALUES " + strings.Join(valueTuples, ", ") + conflict
		if _, err := tx.ExecContext(ctx, statement, args...); err != nil {
			return fmt.Errorf("写入 %s: %w", schemaTable, err)
		}
	}
	return nil
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
