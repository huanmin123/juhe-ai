package contracts

// SQLiteTableSpec describes the minimum Business SQLite shape required by a
// Gateway owner. Extra tables/columns remain forward compatible; missing
// required objects fail the schemaReady gate.
type SQLiteTableSpec struct {
	Columns           []string                `json:"columns"`
	PrimaryKey        []string                `json:"primaryKey,omitempty"`
	UniqueConstraints [][]string              `json:"uniqueConstraints,omitempty"`
	Indexes           []string                `json:"indexes,omitempty"`
	IndexDefinitions  []SQLiteIndexDefinition `json:"indexDefinitions,omitempty"`
	ForeignKeys       []SQLiteForeignKeySpec  `json:"foreignKeys,omitempty"`
}

// SQLiteIndexDefinition describes structural requirements for an index. The
// legacy Indexes field remains name-only for contracts that do not need
// stronger evidence.
type SQLiteIndexDefinition struct {
	Name      string   `json:"name"`
	Columns   []string `json:"columns"`
	Unique    bool     `json:"unique"`
	Predicate string   `json:"predicate,omitempty"`
}

// SQLiteForeignKeySpec describes one required SQLite foreign-key relation.
// The contract is structural only; maintenance verifies it without issuing
// DDL and the runtime owner remains responsible for enabling enforcement.
type SQLiteForeignKeySpec struct {
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
const BusinessSQLiteSchemaVersion = "business-sqlite-gateway-v11"

var BusinessSQLiteSchema = map[string]SQLiteTableSpec{
	"system_accounts":              {Columns: []string{"id", "username", "display_name", "status", "role", "must_change_password", "password_hash", "last_login_at", "updated_at"}, UniqueConstraints: [][]string{{"username"}}},
	"system_sessions":              {Columns: []string{"id", "system_account_id", "token_hash", "expires_at", "created_at", "last_seen_at"}, Indexes: []string{"idx_system_sessions_expires_at"}, ForeignKeys: []SQLiteForeignKeySpec{{Columns: []string{"system_account_id"}, RefTable: "system_accounts", RefColumns: []string{"id"}, OnDelete: "CASCADE"}}},
	"system_settings":              {Columns: []string{"system_account_id", "key", "value_json", "updated_at"}, PrimaryKey: []string{"system_account_id", "key"}, ForeignKeys: []SQLiteForeignKeySpec{{Columns: []string{"system_account_id"}, RefTable: "system_accounts", RefColumns: []string{"id"}, OnDelete: "CASCADE"}}},
	"providers":                    {Columns: []string{"id", "code", "name", "enabled"}},
	"provider_protocol_profiles":   {Columns: []string{"id", "provider_code", "protocol_code", "protocol_version", "base_url", "enabled"}},
	"provider_model_catalog":       {Columns: []string{"id", "provider_code", "model", "status", "catalog_visible", "context_window_tokens", "max_input_tokens", "max_output_tokens"}, Indexes: []string{"idx_provider_model_catalog_lookup"}},
	"groups":                       {Columns: []string{"id", "system_account_id", "name", "provider_code", "enabled"}},
	"group_accounts":               {Columns: []string{"group_id", "account_id", "system_account_id", "account_authorization_id", "enabled"}, Indexes: []string{"idx_group_accounts_dispatch_priority"}},
	"accounts":                     {Columns: []string{"id", "config_revision", "dispatch_revision", "circuit_projection_revision", "system_account_id", "provider_code", "provider_protocol_profile_id", "protocol_code", "protocol_version", "name", "type", "status", "credentials_encrypted", "proxy_profile_id", "health_check_model", "health_check_endpoint_mode", "availability_schedule_json", "schedulable", "fallback_enabled", "super_priority_enabled", "last_error_code", "last_error_message", "account_expires_at", "cooldown_until", "authorization_instance_authorization_id", "authorization_instance_source_account_id", "deleted_at", "updated_at"}},
	"proxy_profiles":               {Columns: []string{"id", "enabled", "type", "host", "port", "username", "password_encrypted"}},
	"resource_authorizations":      {Columns: []string{"id", "resource_type", "resource_id", "resource_owner_system_account_id", "grantee_system_account_id", "scope", "status", "expires_at"}},
	"account_supported_models":     {Columns: []string{"account_id", "provider_code", "model"}, Indexes: []string{"idx_account_supported_models_provider_model"}},
	"account_model_mappings":       {Columns: []string{"account_id", "provider_code", "source_model", "source_endpoint_family", "upstream_model", "upstream_endpoint_family", "enabled"}, Indexes: []string{"idx_account_model_mappings_source"}},
	"model_quality_policies":       {Columns: []string{"system_account_id", "revision", "profile", "manual_enforcement_enabled", "penalty_threshold", "penalty_action", "recovery_interval_minutes", "created_at", "updated_at"}},
	"model_quality_schedules":      {Columns: []string{"id", "system_account_id", "account_id", "model", "interval_minutes", "profile", "penalty_threshold", "penalty_action", "recovery_interval_minutes", "enabled", "revision", "next_run_at", "last_run_id", "last_run_at", "last_run_status", "lease_owner", "lease_until", "created_at", "updated_at"}, UniqueConstraints: [][]string{{"system_account_id", "account_id"}}, Indexes: []string{"idx_model_quality_schedules_due"}},
	"account_quality_enforcements": {Columns: []string{"account_id", "system_account_id", "enforcement_id", "generation", "state", "action", "trigger_run_id", "config_source", "config_source_id", "policy_revision", "profile", "penalty_threshold", "recovery_interval_minutes", "recovery_model", "account_config_revision", "before_status", "after_status", "fallback_was_enabled", "super_priority_was_enabled", "started_at", "recovery_due_at", "recovery_lease_owner", "recovery_lease_until", "last_recovery_run_id", "cleared_at", "created_at", "updated_at"}, PrimaryKey: []string{"account_id"}, Indexes: []string{"idx_account_quality_enforcements_recovery"}},
	"account_circuit_incidents":    {Columns: []string{"circuit_scope_key", "account_id", "account_runtime_key", "scope_kind", "key_fingerprint", "protocol_code", "request_lane", "model_family", "client_model", "capability_hash", "credential_source_account_id", "client_endpoint_family", "final_upstream_model", "upstream_endpoint_mode", "incident_id", "parent_incident_id", "child_incident_ids_json", "caused_by_terminal_outcome_id", "state", "failure_scope", "generation", "dispatch_revision", "ledger_revision", "projected_ledger_revision", "transition_id", "cooldown_observation_generation", "open_until_ms", "next_transition_at_ms", "lease_id", "lease_purpose", "lease_owner_run_id", "lease_until_ms", "attempt_started_at_ms", "attempt_hard_deadline_ms", "upstream_attempt_observed", "backoff_level", "consecutive_failures", "confirmation_failures_required", "confirmation_failure_evidence_keys_json", "recovering_successes", "last_failure_class", "retained_until_ms", "created_at_ms", "updated_at_ms"}, PrimaryKey: []string{"circuit_scope_key"}, Indexes: []string{"idx_account_circuit_incidents_key_model_capability"}, IndexDefinitions: []SQLiteIndexDefinition{{Name: "idx_account_circuit_incidents_key_model_capability", Columns: []string{"scope_kind", "capability_hash"}, Unique: true, Predicate: "scope_kind = 'key_model' AND capability_hash IS NOT NULL"}}, ForeignKeys: []SQLiteForeignKeySpec{{Columns: []string{"account_id"}, RefTable: "accounts", RefColumns: []string{"id"}, OnDelete: "CASCADE"}}},
	"account_circuit_outbox":       {Columns: []string{"event_id", "projection_key", "dedupe_key", "event_type", "account_id", "account_runtime_key", "circuit_scope_key", "incident_id", "transition_id", "dispatch_revision", "generation", "ledger_revision", "status", "available_at_ms", "claim_token", "claimed_by", "claim_until_ms", "attempt_count", "last_error_class", "acknowledged_at_ms", "created_at_ms", "updated_at_ms"}, ForeignKeys: []SQLiteForeignKeySpec{{Columns: []string{"account_id"}, RefTable: "accounts", RefColumns: []string{"id"}, OnDelete: "CASCADE"}}},
	"announcements": {
		Columns: []string{"id", "title", "content", "level", "status", "created_by", "updated_by", "published_at", "created_at", "updated_at"},
		Indexes: []string{"idx_announcements_public", "idx_announcements_admin", "idx_announcements_admin_page"},
		ForeignKeys: []SQLiteForeignKeySpec{
			{Columns: []string{"created_by"}, RefTable: "system_accounts", RefColumns: []string{"id"}},
			{Columns: []string{"updated_by"}, RefTable: "system_accounts", RefColumns: []string{"id"}},
		},
	},
	"announcement_reads": {
		Columns: []string{"announcement_id", "system_account_id", "read_at"},
		Indexes: []string{"idx_announcement_reads_account"},
		ForeignKeys: []SQLiteForeignKeySpec{
			{Columns: []string{"announcement_id"}, RefTable: "announcements", RefColumns: []string{"id"}, OnDelete: "CASCADE"},
			{Columns: []string{"system_account_id"}, RefTable: "system_accounts", RefColumns: []string{"id"}, OnDelete: "CASCADE"},
		},
	},
}
