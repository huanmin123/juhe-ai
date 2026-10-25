// Business schema 形状契约（PG-only）。历史名 business_sqlite_schema.go 于
// 2026-10-25 随 SQLite 存储形态彻底退役（PLAN-20261024T051925945Z D8）收口：
// 文件与类型去 sqlite 化重命名，map 内容按 PG 实际形状复核（对照 maintenance
// pg_schema_business_tables.go：契约列为最小依赖集，CREATE TABLE + 幂等 ALTER
// 后全部存在；PG 侧多出的列为前向兼容面，不进契约），CheckBusinessPostgresSchema
// （gateway modelcheckowner）继续以本 map 为唯一事实源，校验语义不变。
// （原文件尾 SQLite* 别名编译桥已随 CheckBusinessSQLiteSchema 退役一并删除，
// B8。）
package contracts

// BusinessTableSpec describes the minimum Business (juhe_business, PostgreSQL)
// shape required by a Gateway owner. Extra tables/columns remain forward
// compatible; missing required objects fail the schemaReady gate.
type BusinessTableSpec struct {
	Columns           []string                  `json:"columns"`
	PrimaryKey        []string                  `json:"primaryKey,omitempty"`
	UniqueConstraints [][]string                `json:"uniqueConstraints,omitempty"`
	Indexes           []string                  `json:"indexes,omitempty"`
	IndexDefinitions  []BusinessIndexDefinition `json:"indexDefinitions,omitempty"`
	ForeignKeys       []BusinessForeignKeySpec  `json:"foreignKeys,omitempty"`
}

// BusinessIndexDefinition describes structural requirements for an index. The
// legacy Indexes field remains name-only for contracts that do not need
// stronger evidence.
type BusinessIndexDefinition struct {
	Name      string   `json:"name"`
	Columns   []string `json:"columns"`
	Unique    bool     `json:"unique"`
	Predicate string   `json:"predicate,omitempty"`
}

// BusinessForeignKeySpec describes one required foreign-key relation. The
// contract is structural only; maintenance verifies it without issuing DDL
// and the runtime owner remains responsible for enabling enforcement.
type BusinessForeignKeySpec struct {
	Columns    []string `json:"columns"`
	RefTable   string   `json:"refTable"`
	RefColumns []string `json:"refColumns"`
	OnDelete   string   `json:"onDelete,omitempty"`
	OnUpdate   string   `json:"onUpdate,omitempty"`
}

// BusinessSQLiteSchemaVersion changes whenever Gateway starts depending on a
// new table, column, index, or foreign key. This is a contract identifier,
// not a migration instruction; maintenance never creates missing objects at
// runtime.
//
// v16 (business-sqlite-gateway-v16) tracks the maintenance vendor/model DDL
// target shape: retired provider-facing tables (providers,
// provider_protocol_profiles, provider_model_catalog, account_model_mappings)
// are removed together with the provider columns on groups/accounts;
// accounts gains vendor_code/connection_driver_code/oauth_type and the
// nullable health_check_connection_id with its composite foreign key;
// account_supported_models moves to the (account_id, model_id) composite
// primary key; groups carries uniqueness entirely through the unique indexes
// idx_groups_owner_name_unique and idx_groups_owner_default_unique (no
// table-level UNIQUE constraint: the PG target DDL has none and
// CheckBusinessPostgresSchema only reads pg_constraint, so a declared
// constraint would never verify); new tables vendors, models,
// account_connections, account_endpoint_modes and account_entry_mappings are
// declared (models.idx_models_identity_unique stays name-only because it is
// an expression index, which IndexDefinitions cannot describe).
// v17 (business-sqlite-gateway-v17) adds the super-group target shape:
// groups.group_kind ('standard' | 'super', default 'standard') and the
// super_group_members relation table with its member-side reference index.
// Gateway runtime reads both, so an old database fails the schemaReady gate
// instead of silently degrading.
// v18 (business-postgres-gateway-v18) 是 2026-10-25 SQLite 退役收口版本：
// 契约事实源切换为 PG-only 业务形状（表/列/键/索引最小集与 v17 逐项一致，
// 经 maintenance DDL 复核），版本随事实源口径切换递增。
const BusinessSchemaVersion = "business-postgres-gateway-v18"

var BusinessSchema = map[string]BusinessTableSpec{
	"system_accounts":              {Columns: []string{"id", "username", "display_name", "status", "role", "must_change_password", "password_hash", "expedited_account_limit", "last_login_at", "updated_at"}, UniqueConstraints: [][]string{{"username"}}},
	"system_sessions":              {Columns: []string{"id", "system_account_id", "token_hash", "expires_at", "created_at", "last_seen_at"}, UniqueConstraints: [][]string{{"token_hash"}}, Indexes: []string{"idx_system_sessions_expires_at"}, ForeignKeys: []BusinessForeignKeySpec{{Columns: []string{"system_account_id"}, RefTable: "system_accounts", RefColumns: []string{"id"}, OnDelete: "CASCADE"}}},
	"system_settings":              {Columns: []string{"system_account_id", "key", "value_json", "updated_at"}, PrimaryKey: []string{"system_account_id", "key"}, ForeignKeys: []BusinessForeignKeySpec{{Columns: []string{"system_account_id"}, RefTable: "system_accounts", RefColumns: []string{"id"}, OnDelete: "CASCADE"}}},
	"vendors":                      {Columns: []string{"id", "code", "name", "description", "enabled", "default_supported_models_json", "created_at", "updated_at"}, UniqueConstraints: [][]string{{"code"}}},
	"models":                       {Columns: []string{"id", "vendor_code", "public_model", "upstream_model", "scope", "system_account_id", "status", "catalog_visible", "catalog_order", "release_date", "shutdown_date", "supported_endpoint_modes_json", "supported_service_tiers_json", "supported_reasoning_efforts_json", "default_reasoning_effort", "codex_supported_reasoning_levels_json", "codex_default_reasoning_level", "codex_multi_agent_version", "context_window_tokens", "max_input_tokens", "max_output_tokens", "max_tokens", "supports_prompt_caching", "source", "pricing_notes", "capability_notes", "notes", "created_by", "updated_by", "created_at", "updated_at", "input_usd_per_1m", "output_usd_per_1m", "cached_input_usd_per_1m", "cache_write_usd_per_1m", "cache_write_1h_usd_per_1m", "cache_storage_usd_per_1m_per_hour", "service_tier_prices_json", "long_context_input_token_threshold", "long_context_input_token_threshold_inclusive", "long_context_input_cost_multiplier", "long_context_output_cost_multiplier", "image_input_usd_per_1m", "image_output_usd_per_1m", "audio_input_usd_per_1m", "audio_output_usd_per_1m", "output_usd_per_image", "currency"}, Indexes: []string{"idx_models_identity_unique"}, ForeignKeys: []BusinessForeignKeySpec{{Columns: []string{"vendor_code"}, RefTable: "vendors", RefColumns: []string{"code"}}, {Columns: []string{"system_account_id"}, RefTable: "system_accounts", RefColumns: []string{"id"}, OnDelete: "CASCADE"}}},
	"groups":                       {Columns: []string{"id", "system_account_id", "name", "description", "enabled", "is_default", "group_type", "group_kind", "scheduling_policy_json", "created_at", "updated_at"}, Indexes: []string{"idx_groups_owner_default_unique", "idx_groups_owner_name_unique"}, IndexDefinitions: []BusinessIndexDefinition{{Name: "idx_groups_owner_default_unique", Columns: []string{"system_account_id"}, Unique: true, Predicate: "is_default = 1"}, {Name: "idx_groups_owner_name_unique", Columns: []string{"system_account_id", "name"}, Unique: true}}},
	"super_group_members":          {Columns: []string{"id", "super_group_id", "member_group_id", "priority", "enabled", "created_at", "updated_at"}, UniqueConstraints: [][]string{{"super_group_id", "member_group_id"}, {"super_group_id", "priority"}}, Indexes: []string{"idx_super_group_members_member_group"}, ForeignKeys: []BusinessForeignKeySpec{{Columns: []string{"super_group_id"}, RefTable: "groups", RefColumns: []string{"id"}, OnDelete: "RESTRICT"}, {Columns: []string{"member_group_id"}, RefTable: "groups", RefColumns: []string{"id"}, OnDelete: "RESTRICT"}}},
	"group_accounts":               {Columns: []string{"group_id", "account_id", "system_account_id", "account_authorization_id", "enabled"}, Indexes: []string{"idx_group_accounts_dispatch_priority"}},
	"account_connections":          {Columns: []string{"id", "account_id", "connection_preset_code", "protocol_surface_code", "base_url", "enabled", "created_at", "updated_at"}, UniqueConstraints: [][]string{{"account_id", "connection_preset_code"}, {"account_id", "id"}}, ForeignKeys: []BusinessForeignKeySpec{{Columns: []string{"account_id"}, RefTable: "accounts", RefColumns: []string{"id"}, OnDelete: "CASCADE"}}},
	"account_endpoint_modes":       {Columns: []string{"connection_id", "endpoint_mode"}, PrimaryKey: []string{"connection_id", "endpoint_mode"}, ForeignKeys: []BusinessForeignKeySpec{{Columns: []string{"connection_id"}, RefTable: "account_connections", RefColumns: []string{"id"}, OnDelete: "CASCADE"}}},
	"accounts":                     {Columns: []string{"id", "config_revision", "dispatch_revision", "circuit_projection_revision", "system_account_id", "vendor_code", "connection_driver_code", "oauth_type", "protocol_code", "protocol_version", "name", "type", "status", "credentials_encrypted", "credential_fingerprint", "credential_mask", "oauth_access_token_expires_at", "oauth_refresh_token_present", "proxy_profile_id", "concurrency_limit", "priority", "super_priority_enabled", "fallback_enabled", "schedulable", "availability_schedule_json", "availability_schedule_next_check_at", "notes", "account_expires_at", "last_used_at", "cooldown_until", "last_error_code", "last_error_message", "last_error_trace_id", "cooldown_retest_failure_count", "cooldown_retest_observation_started_at", "cooldown_retest_generation", "cooldown_retest_last_at", "cooldown_retest_last_status_code", "temporary_unavailable_continuous_probe_enabled", "expedited_recovery_enabled", "health_check_connection_id", "health_check_model", "health_check_endpoint_mode", "last_health_check_at", "next_health_check_at", "last_health_success_at", "health_check_failure_count", "health_check_failure_started_at", "last_health_check_status_code", "last_health_check_error_code", "last_health_check_error_message", "last_health_check_trace_id", "stream_failure_count", "stream_failure_window_started_at", "balance_query_enabled", "balance_query_config_json", "balance_query_next_refresh_at", "authorization_instance_source_account_id", "authorization_instance_authorization_id", "authorization_instance_owner_system_account_id", "deleted_at", "deleted_by", "created_at", "updated_at"}, ForeignKeys: []BusinessForeignKeySpec{{Columns: []string{"vendor_code"}, RefTable: "vendors", RefColumns: []string{"code"}}, {Columns: []string{"id", "health_check_connection_id"}, RefTable: "account_connections", RefColumns: []string{"account_id", "id"}}}},
	"proxy_profiles":               {Columns: []string{"id", "enabled", "type", "host", "port", "username", "password_encrypted"}},
	"resource_authorizations":      {Columns: []string{"id", "resource_type", "resource_id", "resource_owner_system_account_id", "grantee_system_account_id", "scope", "status", "expires_at"}},
	"account_supported_models":     {Columns: []string{"account_id", "model_id", "created_at"}, PrimaryKey: []string{"account_id", "model_id"}, ForeignKeys: []BusinessForeignKeySpec{{Columns: []string{"account_id"}, RefTable: "accounts", RefColumns: []string{"id"}, OnDelete: "CASCADE"}, {Columns: []string{"model_id"}, RefTable: "models", RefColumns: []string{"id"}, OnDelete: "RESTRICT"}}},
	"account_entry_mappings":       {Columns: []string{"id", "account_id", "source_model", "source_endpoint_family", "target_connection_id", "target_model_id", "target_endpoint_family", "enabled", "created_at", "updated_at"}, UniqueConstraints: [][]string{{"account_id", "source_model", "source_endpoint_family"}}, ForeignKeys: []BusinessForeignKeySpec{{Columns: []string{"account_id"}, RefTable: "accounts", RefColumns: []string{"id"}, OnDelete: "CASCADE"}, {Columns: []string{"account_id", "target_connection_id"}, RefTable: "account_connections", RefColumns: []string{"account_id", "id"}}, {Columns: []string{"account_id", "target_model_id"}, RefTable: "account_supported_models", RefColumns: []string{"account_id", "model_id"}}}},
	"model_quality_policies":       {Columns: []string{"system_account_id", "revision", "profile", "manual_enforcement_enabled", "timeout_penalty_enabled", "penalty_threshold", "penalty_action", "recovery_interval_minutes", "created_at", "updated_at", "custom_question_ids"}},
	"model_quality_schedules":      {Columns: []string{"id", "system_account_id", "account_id", "model", "interval_minutes", "profile", "penalty_threshold", "penalty_action", "recovery_interval_minutes", "timeout_penalty_enabled", "enabled", "revision", "next_run_at", "last_run_id", "last_run_at", "last_run_status", "lease_owner", "lease_until", "created_at", "updated_at", "custom_question_ids"}, UniqueConstraints: [][]string{{"system_account_id", "account_id"}}, Indexes: []string{"idx_model_quality_schedules_due"}},
	"account_quality_enforcements": {Columns: []string{"account_id", "system_account_id", "enforcement_id", "generation", "state", "action", "trigger_run_id", "config_source", "config_source_id", "policy_revision", "profile", "penalty_threshold", "recovery_interval_minutes", "recovery_model", "account_config_revision", "before_status", "after_status", "fallback_was_enabled", "super_priority_was_enabled", "started_at", "recovery_due_at", "recovery_lease_owner", "recovery_lease_until", "last_recovery_run_id", "cleared_at", "created_at", "updated_at"}, PrimaryKey: []string{"account_id"}, Indexes: []string{"idx_account_quality_enforcements_recovery"}},
	"account_circuit_incidents":    {Columns: []string{"circuit_scope_key", "account_id", "account_runtime_key", "scope_kind", "key_fingerprint", "protocol_code", "request_lane", "model_family", "client_model", "capability_hash", "credential_source_account_id", "client_endpoint_family", "final_upstream_model", "upstream_endpoint_mode", "incident_id", "parent_incident_id", "child_incident_ids_json", "caused_by_terminal_outcome_id", "state", "failure_scope", "generation", "dispatch_revision", "ledger_revision", "projected_ledger_revision", "transition_id", "cooldown_observation_generation", "open_until_ms", "next_transition_at_ms", "lease_id", "lease_purpose", "lease_owner_run_id", "lease_until_ms", "attempt_started_at_ms", "attempt_hard_deadline_ms", "upstream_attempt_observed", "backoff_level", "consecutive_failures", "confirmation_failures_required", "confirmation_failure_evidence_keys_json", "recovering_successes", "last_failure_class", "retained_until_ms", "created_at_ms", "updated_at_ms"}, PrimaryKey: []string{"circuit_scope_key"}, Indexes: []string{"idx_account_circuit_incidents_key_model_capability"}, IndexDefinitions: []BusinessIndexDefinition{{Name: "idx_account_circuit_incidents_key_model_capability", Columns: []string{"scope_kind", "capability_hash"}, Unique: true, Predicate: "scope_kind = 'key_model' AND capability_hash IS NOT NULL"}}, ForeignKeys: []BusinessForeignKeySpec{{Columns: []string{"account_id"}, RefTable: "accounts", RefColumns: []string{"id"}, OnDelete: "CASCADE"}}},
	"account_circuit_outbox":       {Columns: []string{"event_id", "projection_key", "dedupe_key", "event_type", "account_id", "account_runtime_key", "circuit_scope_key", "incident_id", "transition_id", "dispatch_revision", "generation", "ledger_revision", "status", "available_at_ms", "claim_token", "claimed_by", "claim_until_ms", "attempt_count", "last_error_class", "acknowledged_at_ms", "created_at_ms", "updated_at_ms"}, ForeignKeys: []BusinessForeignKeySpec{{Columns: []string{"account_id"}, RefTable: "accounts", RefColumns: []string{"id"}, OnDelete: "CASCADE"}}},
	"announcements": {
		Columns: []string{"id", "title", "content", "level", "status", "created_by", "updated_by", "published_at", "created_at", "updated_at"},
		Indexes: []string{"idx_announcements_public", "idx_announcements_admin", "idx_announcements_admin_page"},
		ForeignKeys: []BusinessForeignKeySpec{
			{Columns: []string{"created_by"}, RefTable: "system_accounts", RefColumns: []string{"id"}},
			{Columns: []string{"updated_by"}, RefTable: "system_accounts", RefColumns: []string{"id"}},
		},
	},
	"announcement_reads": {
		Columns: []string{"announcement_id", "system_account_id", "read_at"},
		Indexes: []string{"idx_announcement_reads_account"},
		ForeignKeys: []BusinessForeignKeySpec{
			{Columns: []string{"announcement_id"}, RefTable: "announcements", RefColumns: []string{"id"}, OnDelete: "CASCADE"},
			{Columns: []string{"system_account_id"}, RefTable: "system_accounts", RefColumns: []string{"id"}, OnDelete: "CASCADE"},
		},
	},
}
