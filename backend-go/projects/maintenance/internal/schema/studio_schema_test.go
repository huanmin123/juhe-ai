package schema

// 媒体工作室 schema 测试（媒体工作室设计 §3 / §3.5）：
//  1. SQLite 形态与 PG 同构——studio 四表列集、CHECK 词表、默认值、唯一约束、
//     查询索引，以及 api_keys 的三值 purpose CHECK、studio_domains_json 列与
//     每用户 partial unique index；
//  2. 存量 SQLite 库的 api_keys purpose CHECK 整表重建（schedule-interval /
//     chat-assets media 先例）：旧行保留、'studio' 放行、非法词表仍拒绝、
//     全部索引复建、重复 ensure 幂等；
//  3. PG 语句清单的契约文本断言（DO 块无法在无真实 PG 时执行，按
//     TestPostgresQualityScheduleIntervalCheckContract 先例锁定 SQL 文本）。
import (
	"context"
	"database/sql"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func openStudioSQLite(t *testing.T, name string) *sql.DB {
	t.Helper()
	db := openSharedMemorySQLite(t, name)
	if _, err := EnsureAllSQLite(context.Background(), db); err != nil {
		t.Fatalf("EnsureAllSQLite: %v", err)
	}
	return db
}

func TestStudioTablesShapeSQLite(t *testing.T) {
	db := openStudioSQLite(t, "studio-schema-shape")

	t.Run("studio_assets 列集与默认值", func(t *testing.T) {
		required := map[string]bool{
			"id": true, "system_account_id": true, "kind": true, "mime": true,
			"storage_key": true, "bytes": true, "width": true, "height": true,
			"duration_ms": true, "sha256": true, "source_task_id": true,
			"params_json": true, "created_at": true, "updated_at": true,
		}
		for column := range required {
			if !sqliteTableHasColumn(t, db, "studio_assets", column) {
				t.Fatalf("studio_assets missing column %s", column)
			}
		}
		var columns int
		if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('studio_assets')`).Scan(&columns); err != nil {
			t.Fatal(err)
		}
		if columns != len(required) {
			t.Fatalf("studio_assets columns = %d, want %d", columns, len(required))
		}
		if _, err := db.Exec(`INSERT INTO studio_assets (id, system_account_id, kind, mime, storage_key, bytes, sha256, created_at, updated_at)
	      VALUES ('stasset_1', 'sys_admin', 'image', 'image/png', 'studio/k1', 10, '` + strings.Repeat("a", 64) + `', '2026-10-12T00:00:00.000Z', '2026-10-12T00:00:00.000Z')`); err != nil {
			t.Fatalf("insert valid asset: %v", err)
		}
		var params string
		if err := db.QueryRow(`SELECT params_json FROM studio_assets WHERE id = 'stasset_1'`).Scan(&params); err != nil {
			t.Fatal(err)
		}
		if params != "{}" {
			t.Fatalf("params_json default = %q, want {}", params)
		}
		// kind 三值词表。
		if _, err := db.Exec(`INSERT INTO studio_assets (id, system_account_id, kind, mime, storage_key, bytes, sha256, created_at, updated_at)
	      VALUES ('stasset_2', 'sys_admin', 'text', 'text/plain', 'studio/k2', 10, '` + strings.Repeat("b", 64) + `', '2026-10-12T00:00:00.000Z', '2026-10-12T00:00:00.000Z')`); err == nil {
			t.Fatal("kind=text must be rejected by CHECK")
		}
	})

	t.Run("studio_tasks 词表、默认值与用户范围幂等键", func(t *testing.T) {
		required := map[string]bool{
			"id": true, "system_account_id": true, "client_operation_id": true,
			"kind": true, "status": true, "settlement": true, "account_id": true,
			"provider_code": true, "upstream_job_id": true, "request_snapshot_json": true,
			"error_json": true, "asset_id": true, "poll_bookmark_json": true,
			"lease_owner": true, "lease_expires_at": true, "lease_attempt_count": true,
			"created_at": true, "updated_at": true,
		}
		for column := range required {
			if !sqliteTableHasColumn(t, db, "studio_tasks", column) {
				t.Fatalf("studio_tasks missing column %s", column)
			}
		}
		var columns int
		if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('studio_tasks')`).Scan(&columns); err != nil {
			t.Fatal(err)
		}
		if columns != len(required) {
			t.Fatalf("studio_tasks columns = %d, want %d", columns, len(required))
		}
		if _, err := db.Exec(`INSERT INTO studio_tasks (id, system_account_id, client_operation_id, kind, created_at, updated_at)
	      VALUES ('stask_1', 'sys_admin', 'op-1', 'video', '2026-10-12T00:00:00.000Z', '2026-10-12T00:00:00.000Z')`); err != nil {
			t.Fatalf("insert valid task: %v", err)
		}
		var status, settlement, snapshot, poll string
		var attempts int
		if err := db.QueryRow(`SELECT status, settlement, request_snapshot_json, poll_bookmark_json, lease_attempt_count
	      FROM studio_tasks WHERE id = 'stask_1'`).Scan(&status, &settlement, &snapshot, &poll, &attempts); err != nil {
			t.Fatal(err)
		}
		if status != "queued" || settlement != "pending_settled" || snapshot != "{}" || poll != "{}" || attempts != 0 {
			t.Fatalf("defaults = %q %q %q %q %d", status, settlement, snapshot, poll, attempts)
		}
		// 六值 status / 三值 settlement / 二值 kind 的 CHECK 拒绝。
		rejectTask := func(t *testing.T, id, column, value string) {
			t.Helper()
			var query string
			if column == "kind" {
				query = `INSERT INTO studio_tasks (id, system_account_id, client_operation_id, kind, created_at, updated_at)
	          VALUES ('` + id + `', 'sys_admin', 'op-` + id + `', '` + value + `', '2026-10-12T00:00:00.000Z', '2026-10-12T00:00:00.000Z')`
			} else {
				query = `INSERT INTO studio_tasks (id, system_account_id, client_operation_id, kind, ` + column + `, created_at, updated_at)
	          VALUES ('` + id + `', 'sys_admin', 'op-` + id + `', 'video', '` + value + `', '2026-10-12T00:00:00.000Z', '2026-10-12T00:00:00.000Z')`
			}
			if _, err := db.Exec(query); err == nil {
				t.Fatalf("%s=%s must be rejected by CHECK", column, value)
			}
		}
		rejectTask(t, "stask_bad_status", "status", "running")
		rejectTask(t, "stask_bad_settlement", "settlement", "deferred")
		rejectTask(t, "stask_bad_kind", "kind", "audio")
		// UNIQUE (system_account_id, client_operation_id)。
		if _, err := db.Exec(`INSERT INTO studio_tasks (id, system_account_id, client_operation_id, kind, created_at, updated_at)
	      VALUES ('stask_2', 'sys_admin', 'op-1', 'image', '2026-10-12T00:00:00.000Z', '2026-10-12T00:00:00.000Z')`); err == nil {
			t.Fatal("duplicate (system_account_id, client_operation_id) must be rejected")
		}
	})

	t.Run("studio_operations 词表与幂等键", func(t *testing.T) {
		if _, err := db.Exec(`INSERT INTO studio_operations (id, system_account_id, client_operation_id, kind, created_at, updated_at)
	      VALUES ('stoper_1', 'sys_admin', 'op-speech', 'audio_speech', '2026-10-12T00:00:00.000Z', '2026-10-12T00:00:00.000Z')`); err != nil {
			t.Fatalf("insert valid operation: %v", err)
		}
		var result string
		if err := db.QueryRow(`SELECT result_json FROM studio_operations WHERE id = 'stoper_1'`).Scan(&result); err != nil {
			t.Fatal(err)
		}
		if result != "{}" {
			t.Fatalf("result_json default = %q, want {}", result)
		}
		if _, err := db.Exec(`INSERT INTO studio_operations (id, system_account_id, client_operation_id, kind, created_at, updated_at)
	      VALUES ('stoper_2', 'sys_admin', 'op-image', 'image', '2026-10-12T00:00:00.000Z', '2026-10-12T00:00:00.000Z')`); err == nil {
			t.Fatal("kind=image must be rejected by CHECK")
		}
		if _, err := db.Exec(`INSERT INTO studio_operations (id, system_account_id, client_operation_id, kind, created_at, updated_at)
	      VALUES ('stoper_3', 'sys_admin', 'op-speech', 'audio_transcription', '2026-10-12T00:00:00.000Z', '2026-10-12T00:00:00.000Z')`); err == nil {
			t.Fatal("duplicate (system_account_id, client_operation_id) must be rejected")
		}
	})

	t.Run("studio_user_asset_usage 账本", func(t *testing.T) {
		if _, err := db.Exec(`INSERT INTO studio_user_asset_usage (system_account_id, updated_at)
	      VALUES ('sys_admin', '2026-10-12T00:00:00.000Z')`); err != nil {
			t.Fatalf("insert valid ledger: %v", err)
		}
		var bytesValue, count int64
		if err := db.QueryRow(`SELECT asset_bytes, asset_count FROM studio_user_asset_usage WHERE system_account_id = 'sys_admin'`).Scan(&bytesValue, &count); err != nil {
			t.Fatal(err)
		}
		if bytesValue != 0 || count != 0 {
			t.Fatalf("ledger defaults = %d/%d, want 0/0", bytesValue, count)
		}
	})

	t.Run("查询与结算索引存在", func(t *testing.T) {
		indexes := querySQLiteMasterNames(t, db, "index")
		for _, indexName := range []string{
			"idx_studio_assets_owner_created", "idx_studio_assets_owner_sha256",
			"idx_studio_assets_owner_source_task", "idx_studio_tasks_owner_created",
			"idx_studio_tasks_claim", "idx_api_keys_studio_purpose_unique",
		} {
			if !indexes[indexName] {
				t.Fatalf("index %s missing", indexName)
			}
		}
	})

	t.Run("source_task partial 唯一结算闸", func(t *testing.T) {
		if _, err := db.Exec(`INSERT INTO studio_assets (id, system_account_id, kind, mime, storage_key, bytes, sha256, source_task_id, created_at, updated_at)
	      VALUES ('stasset_t1', 'u1', 'video', 'video/mp4', 'studio/t1', 10, '` + strings.Repeat("c", 64) + `', 'stask_x', '2026-10-12T00:00:00.000Z', '2026-10-12T00:00:00.000Z')`); err != nil {
			t.Fatalf("insert first settled asset: %v", err)
		}
		// 同用户同 source_task_id 的第二行必须被拒（§5.4 幂等结算闸）。
		if _, err := db.Exec(`INSERT INTO studio_assets (id, system_account_id, kind, mime, storage_key, bytes, sha256, source_task_id, created_at, updated_at)
	      VALUES ('stasset_t2', 'u1', 'video', 'video/mp4', 'studio/t2', 10, '` + strings.Repeat("d", 64) + `', 'stask_x', '2026-10-12T00:00:00.000Z', '2026-10-12T00:00:00.000Z')`); err == nil {
			t.Fatal("duplicate source_task_id for same owner must be rejected")
		}
		// 跨用户同 source_task_id 不受约束（唯一键带 owner 前缀）。
		if _, err := db.Exec(`INSERT INTO studio_assets (id, system_account_id, kind, mime, storage_key, bytes, sha256, source_task_id, created_at, updated_at)
	      VALUES ('stasset_t3', 'u2', 'video', 'video/mp4', 'studio/t3', 10, '` + strings.Repeat("e", 64) + `', 'stask_x', '2026-10-12T00:00:00.000Z', '2026-10-12T00:00:00.000Z')`); err != nil {
			t.Fatalf("other owner same source_task_id must be accepted: %v", err)
		}
		// 上传行 source_task_id 为 NULL 不受约束（可多行）。
		for _, id := range []string{"stasset_t4", "stasset_t5"} {
			if _, err := db.Exec(`INSERT INTO studio_assets (id, system_account_id, kind, mime, storage_key, bytes, sha256, created_at, updated_at)
	        VALUES ('` + id + `', 'u1', 'image', 'image/png', 'studio/` + id + `', 10, '` + strings.Repeat("f", 64) + `', '2026-10-12T00:00:00.000Z', '2026-10-12T00:00:00.000Z')`); err != nil {
				t.Fatalf("NULL source_task_id rows must not conflict: %v", err)
			}
		}
	})
}

func TestStudioAPIKeyPurposeSupportSQLite(t *testing.T) {
	db := openStudioSQLite(t, "studio-schema-api-keys")

	if !sqliteTableHasColumn(t, db, "api_keys", "studio_domains_json") {
		t.Fatal("api_keys must declare studio_domains_json")
	}
	var ddl string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='api_keys'`).Scan(&ddl); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ddl, "purpose IN ('general', 'chat', 'studio')") {
		t.Fatalf("api_keys purpose CHECK must include 'studio':\n%s", ddl)
	}

	// 造一条 general Key 与一条 studio Key（同一 owner 不冲突——不同 purpose）。
	if _, err := db.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatal(err)
	}
	insertKey := func(t *testing.T, id, owner, purpose string) {
		t.Helper()
		if _, err := db.Exec(`INSERT INTO api_keys (id, system_account_id, route_strategy_id, name, key_hash, key_prefix, key_suffix, key_secret_encrypted, purpose, created_at, updated_at)
	      VALUES (?, ?, 'route_1', ?, ?, 'sk-pre', 'sk-suf', 'sealed', ?, '2026-10-12T00:00:00.000Z', '2026-10-12T00:00:00.000Z')`,
			id, owner, "Key "+id, "hash-"+id, purpose); err != nil {
			t.Fatalf("insert purpose=%s key: %v", purpose, err)
		}
	}
	insertKey(t, "key_general", "sys_admin", "general")
	insertKey(t, "key_studio", "sys_admin", "studio")
	// purpose='studio' 每用户唯一：同 owner 第二条必须被拒，其他 owner 放行。
	if _, err := db.Exec(`INSERT INTO api_keys (id, system_account_id, route_strategy_id, name, key_hash, key_prefix, key_suffix, key_secret_encrypted, purpose, created_at, updated_at)
      VALUES ('key_studio_2', 'sys_admin', 'route_1', 'dup', 'hash-dup', 'sk-pre', 'sk-suf', 'sealed', 'studio', '2026-10-12T00:00:00.000Z', '2026-10-12T00:00:00.000Z')`); err == nil {
		t.Fatal("second purpose=studio key for same owner must be rejected")
	}
	insertKey(t, "key_studio_other", "user_other", "studio")
	// 非法 purpose 仍被 CHECK 拒绝。
	if _, err := db.Exec(`INSERT INTO api_keys (id, system_account_id, route_strategy_id, name, key_hash, key_prefix, key_suffix, key_secret_encrypted, purpose, created_at, updated_at)
      VALUES ('key_bogus', 'sys_admin', 'route_1', 'bogus', 'hash-bogus', 'sk-pre', 'sk-suf', 'sealed', 'bogus', '2026-10-12T00:00:00.000Z', '2026-10-12T00:00:00.000Z')`); err == nil {
		t.Fatal("purpose=bogus must be rejected by CHECK")
	}
}

// TestEnsureSQLiteBusinessAPIKeysPurposeCheckMigration 覆盖存量库的 purpose
// CHECK 整表重建：旧行保留、'studio' 放行、非法词表仍拒绝、全部 api_keys
// 索引复建、重复 ensure 幂等不重写。
func TestEnsureSQLiteBusinessAPIKeysPurposeCheckMigration(t *testing.T) {
	legacyAPIKeysDDL := `CREATE TABLE api_keys (
      id TEXT PRIMARY KEY,
      system_account_id TEXT NOT NULL,
      route_strategy_id TEXT NOT NULL,
      name TEXT NOT NULL,
      description TEXT,
      key_hash TEXT NOT NULL UNIQUE,
      key_prefix TEXT NOT NULL,
      key_suffix TEXT NOT NULL,
      key_secret_encrypted TEXT NOT NULL,
      status TEXT NOT NULL DEFAULT 'active',
      is_default INTEGER NOT NULL DEFAULT 0,
      purpose TEXT NOT NULL DEFAULT 'general' CHECK (purpose IN ('general', 'chat')),
      expires_at TEXT,
      quota_limits_json TEXT,
      availability_schedule_json TEXT,
      availability_schedule_next_check_at TEXT,
      last_used_at TEXT,
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL,
      FOREIGN KEY (route_strategy_id) REFERENCES route_strategies(id)
    )`
	apiKeysTableSQL := func(t *testing.T, db *sql.DB) string {
		t.Helper()
		var ddl string
		if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='api_keys'`).Scan(&ddl); err != nil {
			t.Fatalf("read api_keys DDL: %v", err)
		}
		return ddl
	}

	t.Run("legacy table is rebuilt with the widened check", func(t *testing.T) {
		db := openSharedMemorySQLite(t, "studio-schema-api-keys-legacy")
		ctx := context.Background()
		if _, err := db.Exec(legacyAPIKeysDDL); err != nil {
			t.Fatalf("seed legacy DDL: %v", err)
		}
		if _, err := db.Exec(`INSERT INTO api_keys (id, system_account_id, route_strategy_id, name, key_hash, key_prefix, key_suffix, key_secret_encrypted, created_at, updated_at)
	      VALUES ('key_legacy', 'u1', 'route_1', '存量 Key', 'hash-legacy', 'sk-pre', 'sk-suf', 'sealed', '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`); err != nil {
			t.Fatalf("insert legacy row: %v", err)
		}
		if _, err := EnsureSQLiteBusiness(ctx, db); err != nil {
			t.Fatalf("EnsureSQLiteBusiness over legacy api_keys: %v", err)
		}
		ddl := apiKeysTableSQL(t, db)
		if !strings.Contains(ddl, "purpose IN ('general', 'chat', 'studio')") {
			t.Fatalf("rebuilt table must widen purpose CHECK to include 'studio':\n%s", ddl)
		}
		if !strings.Contains(ddl, "studio_domains_json") {
			t.Fatalf("rebuilt table must declare studio_domains_json:\n%s", ddl)
		}
		var name string
		if err := db.QueryRow(`SELECT name FROM api_keys WHERE id = 'key_legacy'`).Scan(&name); err != nil {
			t.Fatalf("legacy row must survive the rebuild: %v", err)
		}
		for _, index := range []string{
			"idx_api_keys_route_strategy", "idx_api_keys_default_updated",
			"idx_api_keys_system_account_default_updated", "idx_api_keys_quota_snapshot",
			"idx_api_keys_availability_schedule_next_check", "idx_api_keys_owner_name_unique",
			"idx_api_keys_route_default_unique", "idx_api_keys_chat_purpose_unique",
			"idx_api_keys_studio_purpose_unique", "idx_api_keys_name_lookup",
			"idx_api_keys_system_account_name_lookup", "idx_api_keys_scheduling_preference",
		} {
			if !querySQLiteMasterNames(t, db, "index")[index] {
				t.Fatalf("rebuilt table must recreate index %s", index)
			}
		}
		// ensure 脚本执行了 PRAGMA foreign_keys = ON：插入断言前关闭 FK，
		// 让拒绝可归因于 CHECK / 唯一索引而非外键。
		if _, err := db.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO api_keys (id, system_account_id, route_strategy_id, name, key_hash, key_prefix, key_suffix, key_secret_encrypted, purpose, created_at, updated_at)
	      VALUES ('key_studio', 'u1', 'route_1', 'studio', 'hash-studio', 'sk-pre', 'sk-suf', 'sealed', 'studio', '2026-10-12T00:00:00.000Z', '2026-10-12T00:00:00.000Z')`); err != nil {
			t.Fatalf("purpose=studio must be accepted after rebuild: %v", err)
		}
		if _, err := db.Exec(`INSERT INTO api_keys (id, system_account_id, route_strategy_id, name, key_hash, key_prefix, key_suffix, key_secret_encrypted, purpose, created_at, updated_at)
	      VALUES ('key_bogus', 'u2', 'route_1', 'bogus', 'hash-bogus', 'sk-pre', 'sk-suf', 'sealed', 'bogus', '2026-10-12T00:00:00.000Z', '2026-10-12T00:00:00.000Z')`); err == nil {
			t.Fatal("purpose=bogus must still be rejected after rebuild")
		}

		// 重复 ensure：不重写（DDL 文本不变）、不报错。
		if _, err := EnsureSQLiteBusiness(ctx, db); err != nil {
			t.Fatalf("second EnsureSQLiteBusiness: %v", err)
		}
		if after := apiKeysTableSQL(t, db); after != ddl {
			t.Fatalf("second ensure must not rewrite the table DDL")
		}
	})

	t.Run("fresh database already declares the widened check", func(t *testing.T) {
		db := openSharedMemorySQLite(t, "studio-schema-api-keys-fresh")
		if _, err := EnsureSQLiteBusiness(context.Background(), db); err != nil {
			t.Fatalf("EnsureSQLiteBusiness: %v", err)
		}
		if ddl := apiKeysTableSQL(t, db); !strings.Contains(ddl, "purpose IN ('general', 'chat', 'studio')") {
			t.Fatalf("fresh table must declare the three-value purpose CHECK:\n%s", ddl)
		}
	})
}

// TestPostgresStudioSchemaContract 锁定 PG 语句清单中的媒体工作室契约文本
// （DO 块无真实 PG 不执行，按 interval-check 契约测试先例断言 SQL 文本）。
func TestPostgresStudioSchemaContract(t *testing.T) {
	var assetsTable, tasksTable, operationsTable, usageTable string
	var domainsColumn, purposeMigration string
	for _, statement := range postgresSchemaStatements {
		switch {
		case strings.Contains(statement.SQL, "CREATE TABLE IF NOT EXISTS studio_assets"):
			assetsTable = statement.SQL
		case strings.Contains(statement.SQL, "CREATE TABLE IF NOT EXISTS studio_tasks"):
			tasksTable = statement.SQL
		case strings.Contains(statement.SQL, "CREATE TABLE IF NOT EXISTS studio_operations"):
			operationsTable = statement.SQL
		case strings.Contains(statement.SQL, "CREATE TABLE IF NOT EXISTS studio_user_asset_usage"):
			usageTable = statement.SQL
		case strings.Contains(statement.SQL, "studio_domains_json"):
			domainsColumn = statement.SQL
		case strings.Contains(statement.SQL, "api_keys_purpose_check"):
			purposeMigration = statement.SQL
		}
	}
	for name, ddl := range map[string]string{
		"studio_assets": assetsTable, "studio_tasks": tasksTable,
		"studio_operations": operationsTable, "studio_user_asset_usage": usageTable,
		"studio_domains_json ALTER": domainsColumn, "api_keys purpose migration": purposeMigration,
	} {
		if ddl == "" {
			t.Fatalf("missing PG statement for %s", name)
		}
	}

	// 列级 CHECK 词表（契约 §3.1-§3.3）。
	for _, fragment := range []string{"kind IN ('image', 'audio', 'video')"} {
		if !strings.Contains(assetsTable, fragment) {
			t.Fatalf("studio_assets missing %q", fragment)
		}
	}
	for _, fragment := range []string{
		"kind IN ('image', 'video')",
		"'queued', 'in_progress', 'completed', 'failed', 'cancelled', 'expired'",
		"'pending_settled', 'settled', 'failed'",
		"UNIQUE (system_account_id, client_operation_id)",
	} {
		if !strings.Contains(tasksTable, fragment) {
			t.Fatalf("studio_tasks missing %q", fragment)
		}
	}
	for _, fragment := range []string{
		"kind IN ('audio_speech', 'audio_transcription')",
		"UNIQUE (system_account_id, client_operation_id)",
	} {
		if !strings.Contains(operationsTable, fragment) {
			t.Fatalf("studio_operations missing %q", fragment)
		}
	}
	if !strings.Contains(usageTable, "asset_bytes bigint NOT NULL DEFAULT 0") ||
		!strings.Contains(usageTable, "asset_count bigint NOT NULL DEFAULT 0") {
		t.Fatalf("studio_user_asset_usage ledger columns drifted: %s", usageTable)
	}
	// studio_domains_json 幂等 ALTER（契约 §3.5.3）。
	if !strings.Contains(domainsColumn, "ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS studio_domains_json text") {
		t.Fatalf("studio_domains_json statement drifted: %s", domainsColumn)
	}
	// purpose CHECK 迁移 DO 块（契约 §3.5.1，沿 accounts 先例）。
	for _, fragment := range []string{
		"to_regclass('juhe_business.api_keys') IS NOT NULL",
		"pg_get_constraintdef(c.oid) NOT LIKE '%studio%'",
		"a.attname = 'purpose'",
		// DO 块字符串字面量内的单引号按 SQL 惯例双写。
		"ADD CONSTRAINT api_keys_purpose_check CHECK (purpose IN (''general'', ''chat'', ''studio''))",
	} {
		if !strings.Contains(purposeMigration, fragment) {
			t.Fatalf("purpose migration missing fragment %q", fragment)
		}
	}

	// api_keys 建表内联 CHECK 已含 'studio'（accounts 先例：新库内联约束的
	// PG 自动名与具名约束同名，DO 块第二步幂等跳过）。
	var apiKeysTable string
	for _, statement := range postgresSchemaStatements {
		if strings.Contains(statement.SQL, "CREATE TABLE IF NOT EXISTS api_keys") {
			apiKeysTable = statement.SQL
			break
		}
	}
	if !strings.Contains(apiKeysTable, "purpose IN ('general', 'chat', 'studio')") {
		t.Fatalf("api_keys CREATE TABLE must declare the three-value purpose CHECK: %s", apiKeysTable)
	}

	// 索引契约（契约 §3.1/§3.2/§3.5.2）：五枚 studio 索引 + partial unique。
	wantIndexes := map[string]string{
		"idx_api_keys_studio_purpose_unique":  "WHERE purpose = 'studio'",
		"idx_studio_assets_owner_created":     "ON studio_assets(system_account_id, created_at DESC, id DESC)",
		"idx_studio_assets_owner_sha256":      "ON studio_assets(system_account_id, sha256)",
		"idx_studio_assets_owner_source_task": "ON studio_assets(system_account_id, source_task_id) WHERE source_task_id IS NOT NULL",
		"idx_studio_tasks_owner_created":      "ON studio_tasks(system_account_id, created_at DESC, id DESC)",
		"idx_studio_tasks_claim":              "ON studio_tasks(status, settlement, lease_expires_at)",
	}
	for indexName, definition := range wantIndexes {
		found := false
		for _, statement := range postgresSchemaStatements {
			upper := strings.ToUpper(statement.SQL)
			if (strings.HasPrefix(upper, "CREATE INDEX") || strings.HasPrefix(upper, "CREATE UNIQUE INDEX")) &&
				strings.Contains(statement.SQL, indexName) {
				if !strings.Contains(statement.SQL, definition) {
					t.Fatalf("index %s definition drifted: %s", indexName, statement.SQL)
				}
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("index %s missing from PG statement list", indexName)
		}
	}
}
