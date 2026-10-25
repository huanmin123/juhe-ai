package providerconnectionmigration

import "fmt"

// 旧 schema 知识归迁移器所有（任务边界）：旧库表名、必填列与主键声明以 SQL/
// 结构字面量写在本包内，不引用 internal/schema。snapshot 的列集合与 SQL 类型
// 在加载时按物理表（SELECT * 的 ordinal 与驱动报告的 declared type）读取，
// 本清单只声明规则判定必需列（子集校验，fail-closed）与主键声明。
//
// 源库方言固定为 PostgreSQL（juhe_business 等 schema 限定名）；sqlite 形态
// 随 SQLite 存储形态退役删除（PLAN-20261024T051925945Z B4）。

// SourceSchemaVersion / TargetSchemaVersion 是 manifest 记录的 schema 版本
// 口径：源版本描述本包拥有的旧 schema 迁移输入契约；目标版本描述 §2-§9 的
// 目标形态（正式执行切片落库时与 shared/contracts 对齐）。
const (
	SourceSchemaVersion = "legacy-provider-connection-model-input-1"
	TargetSchemaVersion = "vendor-connection-model-target-1"
)

type sourceTableDef struct {
	name     string
	pgSchema string
	required []string
	pk       []string
}

// configSourceTables 是 §10 覆盖的 11 张配置面 source 表（全量只读拉取并入
// snapshot/hash）。
var configSourceTables = []sourceTableDef{
	{
		name:     "providers",
		pgSchema: "juhe_business",
		required: []string{"id", "code", "name", "enabled", "created_at", "updated_at"},
		pk:       []string{"id"},
	},
	{
		name:     "provider_protocol_profiles",
		pgSchema: "juhe_business",
		required: []string{"id", "provider_code", "protocol_code", "protocol_version", "base_url", "default_health_check_model", "enabled"},
		pk:       []string{"id"},
	},
	{
		name:     "provider_protocol_profile_families",
		pgSchema: "juhe_business",
		required: []string{"profile_id", "family_code", "enabled"},
		pk:       []string{"profile_id", "family_code"},
	},
	{
		name:     "accounts",
		pgSchema: "juhe_business",
		required: []string{
			"id", "config_revision", "system_account_id", "provider_code",
			"provider_protocol_profile_id", "type", "status", "credentials_encrypted",
			"credential_fingerprint", "capability_domain", "health_check_model",
			"health_check_endpoint_mode",
			"authorization_instance_source_account_id",
			"authorization_instance_authorization_id",
			"authorization_instance_owner_system_account_id",
		},
		pk: []string{"id"},
	},
	{
		name:     "groups",
		pgSchema: "juhe_business",
		required: []string{"id", "system_account_id", "name", "enabled", "is_default", "group_type", "created_at", "updated_at"},
		pk:       []string{"id"},
	},
	{
		name:     "group_accounts",
		pgSchema: "juhe_business",
		required: []string{"group_id", "account_id", "system_account_id", "enabled"},
		pk:       []string{"group_id", "account_id"},
	},
	{
		name:     "route_strategy_groups",
		pgSchema: "juhe_business",
		required: []string{"id", "route_strategy_id", "system_account_id", "group_id", "status"},
		pk:       []string{"id"},
	},
	{
		name:     "account_supported_models",
		pgSchema: "juhe_business",
		required: []string{"account_id", "provider_code", "model"},
		pk:       []string{"account_id", "model"},
	},
	{
		name:     "account_model_mappings",
		pgSchema: "juhe_business",
		required: []string{"account_id", "provider_code", "source_model", "source_endpoint_family", "upstream_model", "upstream_endpoint_family", "enabled"},
		pk:       []string{"account_id", "source_model", "source_endpoint_family"},
	},
	{
		name:     "provider_model_catalog",
		pgSchema: "juhe_business",
		required: []string{
			"id", "provider_code", "model", "status", "mode",
			"supported_api_protocols_json", "service_tier_prices_json",
			"input_usd_per_1m", "output_usd_per_1m", "cached_input_usd_per_1m",
			"cache_write_usd_per_1m", "cache_write_1h_usd_per_1m",
			"cache_storage_usd_per_1m_per_hour", "long_context_input_cost_multiplier",
			"long_context_output_cost_multiplier", "image_input_usd_per_1m",
			"image_output_usd_per_1m", "audio_input_usd_per_1m", "audio_output_usd_per_1m",
			"output_usd_per_image",
		},
		pk: []string{"id"},
	},
	{
		name:     "custom_provider_models",
		pgSchema: "juhe_business",
		required: []string{
			"id", "provider_code", "model", "scope", "system_account_id", "status",
			"supported_api_protocols_json", "service_tier_prices_json", "currency",
			"input_usd_per_1m", "output_usd_per_1m", "cached_input_usd_per_1m",
			"cache_write_usd_per_1m", "cache_write_1h_usd_per_1m",
			"cache_storage_usd_per_1m_per_hour", "long_context_input_cost_multiplier",
			"long_context_output_cost_multiplier", "image_input_usd_per_1m",
			"image_output_usd_per_1m", "audio_input_usd_per_1m", "audio_output_usd_per_1m",
			"output_usd_per_image",
		},
		pk: []string{"id"},
	},
}

// historySourceTables 是规则 11 的历史表（usage/audit/model check）：本切片
// 只按表清点行数并全部标 blocked，不进 snapshot/hash（历史行转换属正式执行
// 切片，见 migrate.go 口径）。表缺失按 0 行处理并记录 note。
var historySourceTables = []sourceTableDef{
	{name: "usage_records", pgSchema: "juhe_usage", required: []string{"id"}, pk: []string{"id"}},
	{name: "audit_logs", pgSchema: "juhe_dataset", required: []string{"id"}, pk: []string{"id"}},
	{name: "model_check_runs", pgSchema: "juhe_j3b", required: []string{"id"}, pk: []string{"id"}},
}

// qualifiedName 返回源 schema 限定的表名。
func qualifiedName(def sourceTableDef) string {
	return fmt.Sprintf(`"%s".%s`, def.pgSchema, def.name)
}
