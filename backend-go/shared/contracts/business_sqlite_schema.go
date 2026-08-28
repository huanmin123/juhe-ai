package contracts

// SQLiteTableSpec describes the minimum Business SQLite shape required by a
// Gateway owner. Extra tables/columns remain forward compatible; missing
// required objects fail the schemaReady gate.
type SQLiteTableSpec struct {
	Columns     []string               `json:"columns"`
	Indexes     []string               `json:"indexes,omitempty"`
	ForeignKeys []SQLiteForeignKeySpec `json:"foreignKeys,omitempty"`
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
const BusinessSQLiteSchemaVersion = "business-sqlite-gateway-v3"

var BusinessSQLiteSchema = map[string]SQLiteTableSpec{
	"system_accounts":              {Columns: []string{"id", "username", "status", "password_hash", "last_login_at", "updated_at"}},
	"system_sessions":              {Columns: []string{"id", "system_account_id", "token_hash", "expires_at", "created_at", "last_seen_at"}, Indexes: []string{"idx_system_sessions_expires_at"}, ForeignKeys: []SQLiteForeignKeySpec{{Columns: []string{"system_account_id"}, RefTable: "system_accounts", RefColumns: []string{"id"}, OnDelete: "CASCADE"}}},
	"providers":                    {Columns: []string{"id", "code", "name", "enabled"}},
	"provider_protocol_profiles":   {Columns: []string{"id", "provider_code", "protocol_code", "protocol_version", "base_url", "enabled"}},
	"provider_model_catalog":       {Columns: []string{"id", "provider_code", "model", "status", "context_window_tokens", "max_input_tokens", "max_output_tokens"}, Indexes: []string{"idx_provider_model_catalog_lookup"}},
	"groups":                       {Columns: []string{"id", "system_account_id", "name", "provider_code", "enabled"}},
	"group_accounts":               {Columns: []string{"group_id", "account_id", "system_account_id", "enabled"}, Indexes: []string{"idx_group_accounts_dispatch_priority"}},
	"accounts":                     {Columns: []string{"id", "config_revision", "system_account_id", "provider_code", "provider_protocol_profile_id", "protocol_code", "protocol_version", "name", "status", "credentials_encrypted", "health_check_model", "availability_schedule_json", "schedulable", "fallback_enabled", "super_priority_enabled", "last_error_code", "last_error_message", "authorization_instance_authorization_id", "deleted_at", "updated_at"}},
	"account_supported_models":     {Columns: []string{"account_id", "provider_code", "model"}, Indexes: []string{"idx_account_supported_models_provider_model"}},
	"account_model_mappings":       {Columns: []string{"account_id", "provider_code", "source_model", "source_endpoint_family", "upstream_model", "upstream_endpoint_family", "enabled"}, Indexes: []string{"idx_account_model_mappings_source"}},
	"model_quality_policies":       {Columns: []string{"system_account_id", "revision", "profile", "manual_enforcement_enabled", "penalty_threshold", "penalty_action", "recovery_interval_minutes"}},
	"model_quality_schedules":      {Columns: []string{"id", "system_account_id", "account_id", "model", "interval_minutes", "profile", "penalty_threshold", "penalty_action", "recovery_interval_minutes", "enabled", "revision", "next_run_at", "last_run_id", "last_run_at", "last_run_status", "lease_owner", "lease_until", "updated_at"}, Indexes: []string{"idx_model_quality_schedules_due"}},
	"account_quality_enforcements": {Columns: []string{"account_id", "system_account_id", "enforcement_id", "generation", "state", "action", "trigger_run_id", "config_source", "config_source_id", "policy_revision", "profile", "penalty_threshold", "recovery_interval_minutes", "recovery_model", "account_config_revision", "before_status", "after_status", "recovery_due_at", "recovery_lease_owner", "recovery_lease_until", "last_recovery_run_id", "cleared_at", "updated_at"}, Indexes: []string{"idx_account_quality_enforcements_recovery"}},
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
