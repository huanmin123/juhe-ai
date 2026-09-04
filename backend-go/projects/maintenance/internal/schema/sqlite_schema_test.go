// Code generated alongside sqlite_schema.go. Golden table lists are extracted
// from the Node schema sources; regenerate when the sources change.

package schema

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	_ "modernc.org/sqlite"
)

// goldenBusinessTables is the golden table list extracted from the Node business-schema.ts (80 tables).
var goldenBusinessTables = []string{
	"account_api_key_pool_probe_cursors",
	"account_api_key_runtime_states",
	"account_balance_projection_cursors",
	"account_circuit_incidents",
	"account_circuit_outbox",
	"account_health_jobs_input_outbox",
	"account_health_jobs_input_versions",
	"account_health_projection_cursors",
	"account_health_projection_receipts",
	"account_list_availability_dirty",
	"account_list_availability_projection_dependency_health",
	"account_list_availability_projection_index",
	"account_list_availability_projection_search_terms",
	"account_list_availability_projection_tags",
	"account_list_availability_projection_viewer_health",
	"account_list_availability_projections",
	"account_list_availability_runtime_overlays",
	"account_lock_states",
	"account_model_mappings",
	"account_name_search_documents",
	"account_name_search_terms",
	"account_quality_enforcements",
	"account_schedule_status_events",
	"account_supported_models",
	"account_tag_bindings",
	"account_tags",
	"account_test_session_tasks",
	"account_test_sessions",
	"account_test_tasks",
	"accounts",
	"announcement_reads",
	"announcements",
	"api_key_schedule_status_events",
	"api_keys",
	"custom_provider_models",
	"external_integration_source_tokens",
	"external_integration_sources",
	"global_settings",
	"group_account_stats_dirty",
	"group_accounts",
	"group_authorization_settings",
	"groups",
	"model_quality_policies",
	"model_quality_schedules",
	"oauth_access_tokens",
	"oauth_authorization_code_oidc_contexts",
	"oauth_authorization_codes",
	"oauth_authorization_transactions",
	"oauth_clients",
	"oauth_device_authorizations",
	"oauth_grants",
	"oauth_signing_keys",
	"openai_compatible_files",
	"openai_compatible_vector_store_chunks",
	"openai_compatible_vector_store_files",
	"openai_compatible_vector_stores",
	"protocol_endpoint_families",
	"protocols",
	"provider_default_health_check_models",
	"provider_model_catalog",
	"provider_protocol_profile_families",
	"provider_protocol_profiles",
	"provider_system_default_health_check_models",
	"providers",
	"proxy_latency_projection_cursors",
	"proxy_latency_projection_receipts",
	"proxy_profiles",
	"request_quota_hourly_window_configs",
	"request_quota_hourly_window_scope_bindings",
	"resource_authorization_grants",
	"resource_authorization_sources",
	"resource_authorizations",
	"response_inspection_policies",
	"route_strategies",
	"route_strategy_groups",
	"system_accounts",
	"system_sessions",
	"system_settings",
	"system_team_members",
	"system_teams",
}

// goldenStatsTables is the golden table list extracted from the Node stats-schema.ts (62 tables).
var goldenStatsTables = []string{
	"account_health_hourly",
	"account_quality_dirty_accounts",
	"account_quality_minute_stats",
	"account_quality_scores",
	"account_usage_snapshots",
	"ai_performance_summary_dirty_system_accounts",
	"ai_performance_summary_windows",
	"authorization_team_usage_range_windows",
	"authorization_team_usage_summary_daily",
	"authorization_user_usage_range_windows",
	"authorization_user_usage_summary_daily",
	"background_job_leases",
	"background_task_runs",
	"client_ip_account_range_window_dirty_ips",
	"client_ip_account_stats_daily",
	"client_ip_account_usage_range_windows",
	"client_ip_policies",
	"client_ip_policy_hits",
	"client_ip_range_window_dirty_ips",
	"client_ip_registry",
	"client_ip_stats_daily",
	"client_ip_usage_range_windows",
	"group_account_stats",
	"process_event_loop_hourly",
	"process_event_loop_samples",
	"process_event_loop_trend_windows",
	"stats_job_state",
	"system_metrics_hourly",
	"system_metrics_samples",
	"system_metrics_trend_windows",
	"usage_error_daily",
	"usage_error_hourly",
	"usage_error_minute",
	"usage_error_monthly",
	"usage_error_rank_windows",
	"usage_error_weekly",
	"usage_latency_daily",
	"usage_latency_hourly",
	"usage_latency_minute",
	"usage_latency_monthly",
	"usage_latency_weekly",
	"usage_model_daily",
	"usage_model_hourly",
	"usage_model_minute",
	"usage_model_monthly",
	"usage_model_rank_windows",
	"usage_model_weekly",
	"usage_overview_dirty_scopes",
	"usage_overview_summary_windows",
	"usage_overview_trend_windows",
	"usage_quota_hourly_window_dirty_scopes",
	"usage_quota_hourly_windows",
	"usage_range_window_requests",
	"usage_rank_snapshots",
	"usage_record_cleanup_deductions",
	"usage_scope_range_windows",
	"usage_stats_daily",
	"usage_stats_hourly",
	"usage_stats_minute",
	"usage_stats_monthly",
	"usage_stats_totals",
	"usage_stats_weekly",
}

// goldenChatTables is the golden table list extracted from the Node chat-schema.ts (10 tables).
var goldenChatTables = []string{
	"chat_asset_references",
	"chat_assets",
	"chat_context_checkpoints",
	"chat_context_entries",
	"chat_conversations",
	"chat_image_generations",
	"chat_message_idempotency",
	"chat_messages",
	"chat_user_asset_usage",
	"chat_user_storage_windows",
}

// goldenCodexContextTables is the golden table list extracted from the Node codex-context-state-schema.ts (4 tables).
var goldenCodexContextTables = []string{
	"codex_context_compacts",
	"codex_context_responses",
	"codex_context_sessions",
	"codex_context_storage_cleanup_queue",
}

// goldenDatasetTables is the golden table list extracted from the Node dataset-schema.ts (3 tables).
var goldenDatasetTables = []string{
	"account_record_cleanup_targets",
	"api_key_record_cleanup_targets",
	"public_api_logs",
}

// goldenUsageCatalogTables is the golden table list extracted from the Node usage-catalog-schema.ts (4 tables).
var goldenUsageCatalogTables = []string{
	"usage_record_account_shards",
	"usage_record_api_key_shards",
	"usage_record_shard_entries",
	"usage_record_shards",
}

// goldenSchemaCounts records how many CREATE TABLE / CREATE INDEX statements
// each schema's Node source executes. A schema may repeat a statement across
// exec blocks (an IF NOT EXISTS no-op on a fresh database), so these are
// statement counts; goldenTotalTables/goldenTotalIndexes below count distinct
// objects.
var goldenSchemaCounts = SQLiteResult{
	Business:     SchemaCounts{Tables: 80, Indexes: 218},
	Stats:        SchemaCounts{Tables: 64, Indexes: 124},
	Chat:         SchemaCounts{Tables: 10, Indexes: 26},
	CodexContext: SchemaCounts{Tables: 4, Indexes: 12},
	Dataset:      SchemaCounts{Tables: 3, Indexes: 4},
	UsageCatalog: SchemaCounts{Tables: 4, Indexes: 10},
}

// goldenTotalTables is the total number of distinct tables across all six
// schemas (the golden lists are disjoint, so a single shared database can
// verify every schema exactly).
const goldenTotalTables = 163

// goldenTotalIndexes is the total number of distinct explicitly created
// indexes across all six schemas. Duplicate CREATE INDEX statements inside one
// schema (IF NOT EXISTS no-ops on a fresh database) are not counted twice.
const goldenTotalIndexes = 391

// openSharedMemorySQLite opens one shared-cache in-memory SQLite database.
func openSharedMemorySQLite(t *testing.T, name string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", fmt.Sprintf("file:%s?mode=memory&cache=shared", name))
	if err != nil {
		t.Fatalf("open sqlite %s: %v", name, err)
	}
	// A single connection keeps per-connection PRAGMAs and the shared memory
	// database deterministic for the whole test.
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// querySQLiteMasterNames returns the set of user-created objects of the given
// type. Entries with sql IS NULL are internal (for example sqlite_autoindex_*
// rows backing UNIQUE constraints) and are excluded.
func querySQLiteMasterNames(t *testing.T, db *sql.DB, objectType string) map[string]bool {
	t.Helper()
	rows, err := db.Query("SELECT name FROM sqlite_master WHERE type = ? AND sql IS NOT NULL", objectType)
	if err != nil {
		t.Fatalf("query sqlite_master: %v", err)
	}
	defer rows.Close()
	names := make(map[string]bool)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan sqlite_master row: %v", err)
		}
		if names[name] {
			t.Fatalf("sqlite_master returned duplicate %s name %q", objectType, name)
		}
		names[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate sqlite_master rows: %v", err)
	}
	return names
}

// assertExactNameSet fails when got differs from want in either direction.
func assertExactNameSet(t *testing.T, objectType string, got map[string]bool, want []string) {
	t.Helper()
	wantSet := make(map[string]bool, len(want))
	for _, name := range want {
		wantSet[name] = true
	}
	for name := range got {
		if !wantSet[name] {
			t.Errorf("unexpected %s %q", objectType, name)
		}
	}
	for name := range wantSet {
		if !got[name] {
			t.Errorf("missing %s %q", objectType, name)
		}
	}
	if len(got) != len(wantSet) {
		t.Errorf("%s count: got %d, want %d", objectType, len(got), len(wantSet))
	}
}

// TestEnsureAllSQLiteCreatesAllGoldenObjects applies every schema to one
// shared in-memory database and verifies the golden table lists, index set
// and per-schema statement counts. The six golden table lists are disjoint by
// construction, so exact union equality proves each schema's own table set.
func TestEnsureAllSQLiteCreatesAllGoldenObjects(t *testing.T) {
	db := openSharedMemorySQLite(t, "authsys-schema-test")
	ctx := context.Background()

	got, err := EnsureAllSQLite(ctx, db)
	if err != nil {
		t.Fatalf("EnsureAllSQLite: %v", err)
	}

	want := goldenSchemaCounts
	if got != want {
		t.Fatalf("EnsureAllSQLite counts mismatch:\n got %+v\nwant %+v", got, want)
	}

	// Guard the disjointness assumption that makes single-database
	// verification exact.
	owners := make(map[string]string)
	for _, tc := range []struct {
		schema string
		tables []string
	}{
		{"business", goldenBusinessTables},
		{"stats", goldenStatsTables},
		{"chat", goldenChatTables},
		{"codex-context", goldenCodexContextTables},
		{"dataset", goldenDatasetTables},
		{"usage-catalog", goldenUsageCatalogTables},
	} {
		for _, table := range tc.tables {
			if owner, dup := owners[table]; dup {
				t.Fatalf("golden table %q declared by both %s and %s", table, owner, tc.schema)
			}
			owners[table] = tc.schema
		}
	}

	gotTables := querySQLiteMasterNames(t, db, "table")
	if len(gotTables) != goldenTotalTables {
		t.Fatalf("table count in sqlite_master: got %d, want %d", len(gotTables), goldenTotalTables)
	}
	assertExactNameSet(t, "table", gotTables, append(
		append(append(append(append(append([]string{}, goldenBusinessTables...), goldenStatsTables...), goldenChatTables...), goldenCodexContextTables...), goldenDatasetTables...),
		goldenUsageCatalogTables...))

	gotIndexes := querySQLiteMasterNames(t, db, "index")
	if len(gotIndexes) != goldenTotalIndexes {
		t.Fatalf("index count in sqlite_master: got %d, want %d", len(gotIndexes), goldenTotalIndexes)
	}
}

// TestEnsureAllSQLiteIsIdempotent verifies that repeating EnsureAllSQLite
// neither fails nor changes the database shape.
func TestEnsureAllSQLiteIsIdempotent(t *testing.T) {
	db := openSharedMemorySQLite(t, "authsys-schema-test-idempotent")
	ctx := context.Background()

	first, err := EnsureAllSQLite(ctx, db)
	if err != nil {
		t.Fatalf("first EnsureAllSQLite: %v", err)
	}
	tablesAfterFirst := querySQLiteMasterNames(t, db, "table")
	indexesAfterFirst := querySQLiteMasterNames(t, db, "index")

	second, err := EnsureAllSQLite(ctx, db)
	if err != nil {
		t.Fatalf("second EnsureAllSQLite: %v", err)
	}
	if second != first {
		t.Fatalf("EnsureAllSQLite result changed on rerun: first=%+v second=%+v", first, second)
	}
	tablesAfterSecond := querySQLiteMasterNames(t, db, "table")
	indexesAfterSecond := querySQLiteMasterNames(t, db, "index")
	if len(tablesAfterSecond) != len(tablesAfterFirst) {
		t.Fatalf("table count changed on rerun: %d -> %d", len(tablesAfterFirst), len(tablesAfterSecond))
	}
	if len(indexesAfterSecond) != len(indexesAfterFirst) {
		t.Fatalf("index count changed on rerun: %d -> %d", len(indexesAfterFirst), len(indexesAfterSecond))
	}
}

// TestEnsureFunctionsCreateExactTableSets applies each Ensure* function to its
// own fresh in-memory database and asserts the exact per-schema table set.
func TestEnsureFunctionsCreateExactTableSets(t *testing.T) {
	cases := []struct {
		name   string
		ensure func(context.Context, *sql.DB) (SchemaCounts, error)
		counts SchemaCounts
		tables []string
	}{
		{"business", EnsureSQLiteBusiness, goldenSchemaCounts.Business, goldenBusinessTables},
		{"stats", EnsureSQLiteStats, goldenSchemaCounts.Stats, goldenStatsTables},
		{"chat", EnsureSQLiteChat, goldenSchemaCounts.Chat, goldenChatTables},
		{"codex-context", EnsureSQLiteCodexContext, goldenSchemaCounts.CodexContext, goldenCodexContextTables},
		{"dataset", EnsureSQLiteDataset, goldenSchemaCounts.Dataset, goldenDatasetTables},
		{"usage-catalog", EnsureSQLiteUsageCatalog, goldenSchemaCounts.UsageCatalog, goldenUsageCatalogTables},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := openSharedMemorySQLite(t, "authsys-schema-test-"+tc.name)
			counts, err := tc.ensure(context.Background(), db)
			if err != nil {
				t.Fatalf("ensure: %v", err)
			}
			if counts != tc.counts {
				t.Fatalf("counts: got %+v, want %+v", counts, tc.counts)
			}
			assertExactNameSet(t, "table", querySQLiteMasterNames(t, db, "table"), tc.tables)
		})
	}
}
