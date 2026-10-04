// Code generated alongside sqlite_schema.go. Golden table lists are extracted
// from the Node schema sources; regenerate when the sources change.

package schema

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// goldenBusinessTables is the golden table list extracted from the Node
// business-schema.ts (72 tables) plus the Go-appended
// model_check_question_bank table (model-check question bank feature) and
// the Go-appended media_jobs table (M2 异步媒体任务，媒体设计 §8.2).
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
	"media_jobs",
	"model_check_question_bank",
	"model_quality_policies",
	"model_quality_schedules",
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

// goldenChatTables is the golden table list extracted from the Node chat-schema.ts (10 tables,
// plus the Go-appended chat_user_tool_preferences user-level tool binding table, 2026-10-02).
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
	"chat_user_tool_preferences",
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
	Business:     SchemaCounts{Tables: 74, Indexes: 217},
	Stats:        SchemaCounts{Tables: 64, Indexes: 124},
	Chat:         SchemaCounts{Tables: 11, Indexes: 26},
	CodexContext: SchemaCounts{Tables: 4, Indexes: 12},
	Dataset:      SchemaCounts{Tables: 3, Indexes: 4},
	UsageCatalog: SchemaCounts{Tables: 4, Indexes: 10},
}

// goldenTotalTables is the total number of distinct tables across all six
// schemas (the golden lists are disjoint, so a single shared database can
// verify every schema exactly).
const goldenTotalTables = 158

// goldenTotalIndexes is the total number of distinct explicitly created
// indexes across all six schemas. Duplicate CREATE INDEX statements inside one
// schema (IF NOT EXISTS no-ops on a fresh database) are not counted twice.
const goldenTotalIndexes = 390

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

// TestEnsureSQLiteBusinessAddsCustomQuestionIDsColumns covers both custom_question_ids
// delivery paths: fresh databases declare the column inside the CREATE TABLE
// statements, while legacy databases (tables created before the model-check
// question bank feature) receive it through the guarded PRAGMA table_info /
// ALTER TABLE ADD COLUMN migration.
func TestEnsureSQLiteBusinessAddsCustomQuestionIDsColumns(t *testing.T) {
	legacyPoliciesDDL := `CREATE TABLE model_quality_policies (
      system_account_id TEXT PRIMARY KEY,
      revision INTEGER NOT NULL DEFAULT 1 CHECK (revision >= 1),
      profile TEXT NOT NULL DEFAULT 'quick' CHECK (profile IN ('quick', 'full')),
      manual_enforcement_enabled INTEGER NOT NULL DEFAULT 1 CHECK (manual_enforcement_enabled IN (0, 1)),
      penalty_threshold INTEGER NOT NULL DEFAULT 70 CHECK (penalty_threshold BETWEEN 40 AND 100),
      penalty_action TEXT NOT NULL DEFAULT 'fallback' CHECK (penalty_action IN ('disable', 'fallback', 'quality_isolate')),
      recovery_interval_minutes INTEGER NOT NULL DEFAULT 10 CHECK (recovery_interval_minutes BETWEEN 10 AND 10080),
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL,
      FOREIGN KEY (system_account_id) REFERENCES system_accounts(id) ON DELETE CASCADE
    )`
	legacySchedulesDDL := `CREATE TABLE model_quality_schedules (
      id TEXT PRIMARY KEY,
      system_account_id TEXT NOT NULL,
      account_id TEXT NOT NULL,
      model TEXT NOT NULL,
      interval_minutes INTEGER NOT NULL DEFAULT 60 CHECK (interval_minutes BETWEEN 10 AND 10080),
      profile TEXT NOT NULL DEFAULT 'quick' CHECK (profile IN ('quick', 'full')),
      penalty_threshold INTEGER NOT NULL DEFAULT 70 CHECK (penalty_threshold BETWEEN 40 AND 100),
      penalty_action TEXT NOT NULL DEFAULT 'fallback' CHECK (penalty_action IN ('disable', 'fallback', 'quality_isolate')),
      recovery_interval_minutes INTEGER NOT NULL DEFAULT 10 CHECK (recovery_interval_minutes BETWEEN 10 AND 10080),
      enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
      revision INTEGER NOT NULL DEFAULT 1 CHECK (revision >= 1),
      next_run_at TEXT NOT NULL,
      last_run_id TEXT,
      last_run_at TEXT,
      last_run_status TEXT CHECK (last_run_status IS NULL OR last_run_status IN ('completed', 'failed', 'canceled')),
      lease_owner TEXT,
      lease_until TEXT,
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL,
      FOREIGN KEY (system_account_id) REFERENCES system_accounts(id) ON DELETE CASCADE,
      FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE,
      UNIQUE (system_account_id, account_id)
    )`

	t.Run("fresh database declares the column", func(t *testing.T) {
		db := openSharedMemorySQLite(t, "authsys-schema-test-custom-question-fresh")
		if _, err := EnsureSQLiteBusiness(context.Background(), db); err != nil {
			t.Fatalf("EnsureSQLiteBusiness: %v", err)
		}
		for _, table := range []string{"model_quality_policies", "model_quality_schedules"} {
			if !sqliteTableHasColumn(t, db, table, "custom_question_ids") {
				t.Errorf("fresh database table %s lacks custom_question_ids", table)
			}
		}
	})

	t.Run("legacy database receives the guarded ALTER", func(t *testing.T) {
		db := openSharedMemorySQLite(t, "authsys-schema-test-custom-question-legacy")
		for _, ddl := range []string{legacyPoliciesDDL, legacySchedulesDDL} {
			if _, err := db.Exec(ddl); err != nil {
				t.Fatalf("seed legacy DDL: %v", err)
			}
		}
		if sqliteTableHasColumn(t, db, "model_quality_policies", "custom_question_ids") {
			t.Fatal("legacy precondition violated: model_quality_policies already has custom_question_ids")
		}
		if _, err := EnsureSQLiteBusiness(context.Background(), db); err != nil {
			t.Fatalf("EnsureSQLiteBusiness over legacy tables: %v", err)
		}
		for _, table := range []string{"model_quality_policies", "model_quality_schedules"} {
			if !sqliteTableHasColumn(t, db, table, "custom_question_ids") {
				t.Errorf("legacy table %s lacks custom_question_ids after ensure", table)
			}
		}
	})
}

// TestEnsureSQLiteBusinessScheduleIntervalCheckMigration covers the widened
// model_quality_schedules.interval_minutes CHECK (lower bound 10 -> 1 minute):
// legacy databases created with the old column CHECK are rebuilt through the
// guarded sqlite_master lookup (staging table -> copy -> drop -> rename ->
// index recreation), keep their rows, accept interval_minutes=1 and still
// reject 0/10081 plus the untouched 10-minute recovery floor; fresh databases
// already declare the new CHECK; repeated ensure runs are idempotent.
func TestEnsureSQLiteBusinessScheduleIntervalCheckMigration(t *testing.T) {
	legacySchedulesIntervalDDL := `CREATE TABLE model_quality_schedules (
      id TEXT PRIMARY KEY,
      system_account_id TEXT NOT NULL,
      account_id TEXT NOT NULL,
      model TEXT NOT NULL,
      interval_minutes INTEGER NOT NULL DEFAULT 60 CHECK (interval_minutes BETWEEN 10 AND 10080),
      profile TEXT NOT NULL DEFAULT 'quick' CHECK (profile IN ('quick', 'full')),
      penalty_threshold INTEGER NOT NULL DEFAULT 70 CHECK (penalty_threshold BETWEEN 40 AND 100),
      penalty_action TEXT NOT NULL DEFAULT 'fallback' CHECK (penalty_action IN ('disable', 'fallback', 'quality_isolate')),
      recovery_interval_minutes INTEGER NOT NULL DEFAULT 10 CHECK (recovery_interval_minutes BETWEEN 10 AND 10080),
      enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
      revision INTEGER NOT NULL DEFAULT 1 CHECK (revision >= 1),
      next_run_at TEXT NOT NULL,
      last_run_id TEXT,
      last_run_at TEXT,
      last_run_status TEXT CHECK (last_run_status IS NULL OR last_run_status IN ('completed', 'failed', 'canceled')),
      lease_owner TEXT,
      lease_until TEXT,
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL,
      FOREIGN KEY (system_account_id) REFERENCES system_accounts(id) ON DELETE CASCADE,
      FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE,
      UNIQUE (system_account_id, account_id)
    )`
	insertLegacySchedule := func(t *testing.T, db *sql.DB, id, accountID string, intervalMinutes int) {
		t.Helper()
		_, err := db.Exec(`INSERT INTO model_quality_schedules
      (id, system_account_id, account_id, model, interval_minutes, profile, penalty_threshold, penalty_action,
       recovery_interval_minutes, enabled, revision, next_run_at, created_at, updated_at)
      VALUES (?, 'sys-1', ?, 'gpt-5.6-sol', ?, 'quick', 70, 'fallback', 10, 1, 1, '2026-10-01T00:00:00Z', '2026-10-01T00:00:00Z', '2026-10-01T00:00:00Z')`, id, accountID, intervalMinutes)
		if err != nil {
			t.Fatalf("insert schedule %s: %v", id, err)
		}
	}
	scheduleTableSQL := func(t *testing.T, db *sql.DB) string {
		t.Helper()
		var ddl string
		if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='model_quality_schedules'`).Scan(&ddl); err != nil {
			t.Fatalf("read schedule DDL: %v", err)
		}
		return ddl
	}

	t.Run("legacy table is rebuilt with the widened check", func(t *testing.T) {
		db := openSharedMemorySQLite(t, "authsys-schema-test-schedule-interval-legacy")
		ctx := context.Background()
		if _, err := db.Exec(legacySchedulesIntervalDDL); err != nil {
			t.Fatalf("seed legacy DDL: %v", err)
		}
		insertLegacySchedule(t, db, "sched-legacy", "acct-1", 60)

		if _, err := EnsureSQLiteBusiness(ctx, db); err != nil {
			t.Fatalf("EnsureSQLiteBusiness over legacy schedule table: %v", err)
		}

		ddl := scheduleTableSQL(t, db)
		if !strings.Contains(ddl, "CHECK (interval_minutes BETWEEN 1 AND 10080)") {
			t.Fatalf("rebuilt table must widen interval_minutes to BETWEEN 1 AND 10080:\n%s", ddl)
		}
		if strings.Contains(ddl, "(interval_minutes BETWEEN 10 AND 10080)") {
			t.Fatalf("rebuilt table must not keep the old interval_minutes check:\n%s", ddl)
		}
		if !strings.Contains(ddl, "CHECK (recovery_interval_minutes BETWEEN 10 AND 10080)") {
			t.Fatalf("rebuilt table must keep the 10-minute recovery floor:\n%s", ddl)
		}
		var legacyInterval int
		if err := db.QueryRow(`SELECT interval_minutes FROM model_quality_schedules WHERE id='sched-legacy'`).Scan(&legacyInterval); err != nil {
			t.Fatalf("legacy row must survive the rebuild: %v", err)
		}
		if legacyInterval != 60 {
			t.Fatalf("legacy row interval_minutes = %d, want 60", legacyInterval)
		}
		for _, index := range []string{"idx_model_quality_schedules_due", "idx_model_quality_schedules_scope"} {
			if !querySQLiteMasterNames(t, db, "index")[index] {
				t.Fatalf("rebuilt table must recreate index %s", index)
			}
		}

		// 下限 1 分钟可用，区间外仍被拒绝；recovery 下限保持 10。
		// ensure 脚本会执行 PRAGMA foreign_keys = ON，而本测试未播种
		// system_accounts/accounts 父行：插入断言阶段关闭 FK，保证
		// 0/10081/recovery=1 的拒绝可归因于列级 CHECK 而非外键。
		if _, err := db.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
			t.Fatalf("disable foreign_keys for check assertions: %v", err)
		}
		insertLegacySchedule(t, db, "sched-min-1", "acct-4", 1)
		for _, invalid := range []struct {
			id       string
			interval int
		}{{"sched-zero", 0}, {"sched-over", 10081}} {
			if _, err := db.Exec(`INSERT INTO model_quality_schedules
      (id, system_account_id, account_id, model, interval_minutes, profile, penalty_threshold, penalty_action,
       recovery_interval_minutes, enabled, revision, next_run_at, created_at, updated_at)
      VALUES (?, 'sys-1', 'acct-2', 'gpt-5.6-sol', ?, 'quick', 70, 'fallback', 10, 1, 1, '2026-10-01T00:00:00Z', '2026-10-01T00:00:00Z', '2026-10-01T00:00:00Z')`, invalid.id, invalid.interval); err == nil {
				t.Fatalf("interval_minutes=%d must still be rejected", invalid.interval)
			}
		}
		if _, err := db.Exec(`INSERT INTO model_quality_schedules
      (id, system_account_id, account_id, model, interval_minutes, recovery_interval_minutes, next_run_at, created_at, updated_at)
      VALUES ('sched-recovery', 'sys-1', 'acct-3', 'gpt-5.6-sol', 60, 1, '2026-10-01T00:00:00Z', '2026-10-01T00:00:00Z', '2026-10-01T00:00:00Z')`); err == nil {
			t.Fatal("recovery_interval_minutes=1 must stay rejected (10-minute floor)")
		}

		// 重复 ensure：不重建（DDL 文本不变）、不报错。
		if _, err := EnsureSQLiteBusiness(ctx, db); err != nil {
			t.Fatalf("second EnsureSQLiteBusiness: %v", err)
		}
		if after := scheduleTableSQL(t, db); after != ddl {
			t.Fatalf("second ensure must not rewrite the table DDL")
		}
		// 第二次 ensure 的脚本同样会重新打开 FK，插入前再关一次。
		if _, err := db.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
			t.Fatalf("disable foreign_keys after second ensure: %v", err)
		}
		insertLegacySchedule(t, db, "sched-after-rerun", "acct-5", 1)
	})

	t.Run("fresh database already declares the widened check", func(t *testing.T) {
		db := openSharedMemorySQLite(t, "authsys-schema-test-schedule-interval-fresh")
		if _, err := EnsureSQLiteBusiness(context.Background(), db); err != nil {
			t.Fatalf("EnsureSQLiteBusiness: %v", err)
		}
		ddl := scheduleTableSQL(t, db)
		if !strings.Contains(ddl, "CHECK (interval_minutes BETWEEN 1 AND 10080)") {
			t.Fatalf("fresh table must declare interval_minutes BETWEEN 1 AND 10080:\n%s", ddl)
		}
		if !strings.Contains(ddl, "CHECK (recovery_interval_minutes BETWEEN 10 AND 10080)") {
			t.Fatalf("fresh table must keep the 10-minute recovery floor:\n%s", ddl)
		}
	})
}

// TestEnsureSQLiteChatAddsAccountBindingColumns covers the conversation
// account-only binding column delivery (AI 问答会话账户唯一绑定设计):
// fresh databases declare the columns inside sqliteChatDDL, while legacy chat
// databases (created before the feature) receive them through the guarded
// PRAGMA table_info / ALTER TABLE ADD COLUMN migration; legacy rows take
// archived DEFAULT 0 (未归档), and ensure never recreates the dropped
// bind_mode/bind_group_id/bind_group_name_snapshot columns.
func TestEnsureSQLiteChatAddsAccountBindingColumns(t *testing.T) {
	legacyConversationDDL := `CREATE TABLE chat_conversations (
      id TEXT PRIMARY KEY,
      system_account_id TEXT NOT NULL,
      api_key_id TEXT,
      api_key_name_snapshot TEXT NOT NULL,
      title TEXT NOT NULL DEFAULT '新对话',
      title_source_message_id TEXT,
      is_pinned INTEGER NOT NULL DEFAULT 0,
      last_model TEXT,
      default_image_model TEXT NOT NULL DEFAULT 'gpt-image-2',
      next_sequence_no INTEGER NOT NULL DEFAULT 1,
      user_turn_count INTEGER NOT NULL DEFAULT 0,
      message_revision INTEGER NOT NULL DEFAULT 0,
      active_turn_id TEXT,
      active_started_at TEXT,
      context_revision INTEGER NOT NULL DEFAULT 0,
      active_checkpoint_id TEXT,
      compacted_through_sequence INTEGER NOT NULL DEFAULT 0,
      context_state TEXT NOT NULL DEFAULT 'ready',
      active_context_tokens INTEGER,
      effective_context_limit_tokens INTEGER,
      context_usage_estimated INTEGER NOT NULL DEFAULT 1,
      context_claim_id TEXT,
      context_claim_revision INTEGER,
      context_claim_through_sequence INTEGER,
      context_claimed_at TEXT,
      context_retry_at TEXT,
      context_attempt_count INTEGER NOT NULL DEFAULT 0,
      context_error_code TEXT,
      context_progress_sequence INTEGER NOT NULL DEFAULT 0,
      context_progress_earliest_expires_at TEXT,
      last_message_at TEXT NOT NULL,
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL
    )`
	accountBindingColumns := []string{"bind_account_id", "bind_account_name_snapshot", "archived", "search_account_id", "search_model_id", "image_account_id"}

	t.Run("fresh database declares the columns", func(t *testing.T) {
		db := openSharedMemorySQLite(t, "authsys-schema-test-chat-bind-fresh")
		if _, err := EnsureSQLiteChat(context.Background(), db); err != nil {
			t.Fatalf("EnsureSQLiteChat: %v", err)
		}
		for _, column := range accountBindingColumns {
			if !sqliteTableHasColumn(t, db, "chat_conversations", column) {
				t.Errorf("fresh chat_conversations lacks %s", column)
			}
		}
		if sqliteTableHasColumn(t, db, "chat_conversations", "bind_mode") {
			t.Error("fresh chat_conversations must not declare the retired bind_mode column")
		}
	})

	t.Run("legacy database receives the guarded ALTER", func(t *testing.T) {
		db := openSharedMemorySQLite(t, "authsys-schema-test-chat-bind-legacy")
		if _, err := db.Exec(legacyConversationDDL); err != nil {
			t.Fatalf("seed legacy DDL: %v", err)
		}
		if _, err := db.Exec(`INSERT INTO chat_conversations (id, system_account_id, api_key_name_snapshot, last_message_at, created_at, updated_at)
			VALUES ('conv_legacy', 'owner-1', '历史密钥', '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`); err != nil {
			t.Fatalf("seed legacy row: %v", err)
		}
		if sqliteTableHasColumn(t, db, "chat_conversations", "bind_account_id") {
			t.Fatal("legacy precondition violated: chat_conversations already has bind_account_id")
		}
		if _, err := EnsureSQLiteChat(context.Background(), db); err != nil {
			t.Fatalf("EnsureSQLiteChat over legacy table: %v", err)
		}
		for _, column := range accountBindingColumns {
			if !sqliteTableHasColumn(t, db, "chat_conversations", column) {
				t.Errorf("legacy chat_conversations lacks %s after ensure", column)
			}
		}
		var archived int
		if err := db.QueryRow(`SELECT archived FROM chat_conversations WHERE id = 'conv_legacy'`).Scan(&archived); err != nil {
			t.Fatalf("read legacy archived: %v", err)
		}
		if archived != 0 {
			t.Fatalf("legacy row archived = %d, want default 0", archived)
		}
		// ADD COLUMN 携带的列级 CHECK 对新增值同样生效。
		if _, err := db.Exec(`UPDATE chat_conversations SET archived = 2 WHERE id = 'conv_legacy'`); err == nil {
			t.Fatal("expected CHECK violation for invalid archived, got nil")
		}
	})
}

// sqliteTableHasColumn reports whether PRAGMA table_info lists the column.
func sqliteTableHasColumn(t *testing.T, db *sql.DB, table, column string) bool {
	t.Helper()
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		t.Fatalf("pragma table_info(%s): %v", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, pk int
		var name, declaredType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &declaredType, &notNull, &defaultValue, &pk); err != nil {
			t.Fatalf("scan pragma table_info(%s): %v", table, err)
		}
		if name == column {
			return true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate pragma table_info(%s): %v", table, err)
	}
	return false
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
