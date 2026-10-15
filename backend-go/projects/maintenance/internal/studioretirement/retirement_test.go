// studioretirement 测试（SQLite 真库，进程内直调 Run；PG 侧行为走 opt-in
// live smoke 门禁 TestPostgresStudioRetirementLiveSmoke，环境变量
// JUHE_AI_STUDIO_RETIREMENT_PG_SMOKE_URL 指向一次性隔离库时才运行，口径同
// schedprefmigration）：
//   - 全量 legacy 工件：4 张真实 studio 表 + 遗留索引 + purpose='studio' 行
//     + studio_domains_json 列 → 一次 Run 全部清理，general/chat Key 与行数
//     保真，Ready；
//   - 幂等：重跑 AlreadyRetired 且零写入；
//   - 新库（ensure 形状）：Ready 且 AlreadyRetired，无任何写入；
//   - 仅 legacy 列：走保行重建（三值 CHECK 下拷贝不删行）；
//   - 重建形状与 ensure 建表 DDL 钉死一致（防两份 DDL 漂移）；
//   - 业务表缺失：显式报错，不静默通过。

package studioretirement

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/schema"
	"github.com/huanminabc/juhe-ai/backend-go-platform/capabilitydomain"
	_ "modernc.org/sqlite"
)

var fixtureClock = time.Date(2026, 10, 14, 8, 0, 0, 0, time.UTC)

const legacyStudioFixture = `
CREATE TABLE studio_assets (
	id TEXT PRIMARY KEY,
	system_account_id TEXT NOT NULL,
	kind TEXT NOT NULL,
	storage_key TEXT NOT NULL,
	created_at TEXT NOT NULL
);
CREATE TABLE studio_tasks (
	id TEXT PRIMARY KEY,
	system_account_id TEXT NOT NULL,
	status TEXT NOT NULL,
	created_at TEXT NOT NULL
);
CREATE TABLE studio_operations (
	id TEXT PRIMARY KEY,
	system_account_id TEXT NOT NULL,
	operation TEXT NOT NULL,
	created_at TEXT NOT NULL
);
CREATE TABLE studio_user_asset_usage (
	system_account_id TEXT NOT NULL,
	day TEXT NOT NULL,
	used INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (system_account_id, day)
);
CREATE INDEX idx_studio_assets_system_account ON studio_assets(system_account_id);
CREATE UNIQUE INDEX idx_api_keys_studio_purpose_unique ON api_keys(system_account_id) WHERE purpose = 'studio';
ALTER TABLE api_keys ADD COLUMN studio_domains_json TEXT;
INSERT INTO studio_assets (id, system_account_id, kind, storage_key, created_at)
	VALUES ('asset-1', 'owner-1', 'image', 'studio/asset-1', '2026-10-01T00:00:00.000Z');
INSERT INTO studio_tasks (id, system_account_id, status, created_at)
	VALUES ('task-1', 'owner-1', 'done', '2026-10-01T00:00:00.000Z');
INSERT INTO studio_operations (id, system_account_id, operation, created_at)
	VALUES ('op-1', 'owner-1', 'render', '2026-10-01T00:00:00.000Z');
INSERT INTO studio_user_asset_usage (system_account_id, day, used) VALUES ('owner-1', '2026-10-01', 3);
`

// seedKey 插入一条 API Key（studio 行的 partial unique 索引要求每个 owner 至
// 多一条；夹具连接已按 schedprefmigration 先例关闭外键，route_strategy_id
// 无需真实父行）。
func seedKey(t *testing.T, db *sql.DB, id, owner, purpose string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO api_keys
		(id, system_account_id, route_strategy_id, name, description, key_hash, key_prefix, key_suffix,
		 key_secret_encrypted, status, is_default, purpose, scheduling_preference_id, created_at, updated_at)
		VALUES (?, ?, 'strategy-x', ?, NULL, ?, 'sk-prefix', 'suf', 'enc', 'active', 0, ?, NULL, ?, ?)`,
		id, owner, id, "hash-"+id, purpose, "2026-10-01T00:00:00.000Z", "2026-10-01T00:00:00.000Z"); err != nil {
		t.Fatalf("seed api key %s (purpose=%s): %v", id, purpose, err)
	}
}

func keyExists(t *testing.T, db *sql.DB, id string) bool {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM api_keys WHERE id = ?`, id).Scan(&count); err != nil {
		t.Fatalf("query api key %s: %v", id, err)
	}
	return count > 0
}

func TestRunRefusesStudioRetirementWhenOpenAICompatibleResourcesRemain(t *testing.T) {
	db := openEnsuredBusinessDB(t, "studio-retirement-openai-resources")
	seedKey(t, db, "studio-key-with-resources", "owner-resources", "studio")
	statements := []string{
		`INSERT INTO openai_compatible_files
			(id, system_account_id, api_key_id, purpose, filename, bytes, storage_key, sha256, created_at, updated_at)
			VALUES ('file-1', 'owner-resources', 'studio-key-with-resources', 'assistants', 'f.txt', 1, 'file-1', 'hash', 'now', 'now')`,
		`INSERT INTO openai_compatible_vector_stores
			(id, system_account_id, api_key_id, created_at, updated_at)
			VALUES ('store-1', 'owner-resources', 'studio-key-with-resources', 'now', 'now')`,
		`INSERT INTO openai_compatible_vector_store_files
			(vector_store_id, file_id, system_account_id, api_key_id, created_at, updated_at)
			VALUES ('store-1', 'file-1', 'owner-resources', 'studio-key-with-resources', 'now', 'now')`,
		`INSERT INTO openai_compatible_vector_store_chunks
			(id, vector_store_id, file_id, system_account_id, api_key_id, chunk_index, content_text, content_preview, keyword_index_text, created_at)
			VALUES ('chunk-1', 'store-1', 'file-1', 'owner-resources', 'studio-key-with-resources', 0, 'body', 'preview', 'body', 'now')`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("seed linked OpenAI-compatible resource: %v: %v", statement, err)
		}
	}

	report, err := Run(context.Background(), db, capabilitydomain.DialectSQLite, fixtureClock)
	if err == nil || !strings.Contains(err.Error(), "files=1 vectorStores=1 vectorStoreFiles=1 vectorStoreChunks=1") {
		t.Fatalf("Run error = %v, report=%+v", err, report)
	}
	if report.OpenAICompatibleResourceRows != (OpenAICompatibleResourceCounts{Files: 1, VectorStores: 1, VectorStoreFiles: 1, VectorStoreChunks: 1}) {
		t.Fatalf("preflight resource counts = %+v", report.OpenAICompatibleResourceRows)
	}
	if !keyExists(t, db, "studio-key-with-resources") {
		t.Fatal("blocked retirement deleted the studio API Key")
	}
	for _, table := range []string{"openai_compatible_files", "openai_compatible_vector_stores", "openai_compatible_vector_store_files", "openai_compatible_vector_store_chunks"} {
		var rows int
		if err := db.QueryRow("SELECT count(*) FROM " + table).Scan(&rows); err != nil {
			t.Fatal(err)
		}
		if rows != 1 {
			t.Fatalf("blocked retirement changed %s rows: got %d, want 1", table, rows)
		}
	}
	if report.DeletedAPIKeyRows != 0 || len(report.DroppedTables) != 0 || report.DroppedColumn || report.DroppedIndex {
		t.Fatalf("blocked retirement performed writes: %+v", report)
	}
}

func openEnsuredBusinessDB(t *testing.T, name string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", fmt.Sprintf("file:%s?mode=memory&cache=shared", name))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if _, err := schema.EnsureSQLiteBusiness(context.Background(), db); err != nil {
		t.Fatalf("ensure business schema: %v", err)
	}
	// 夹具造数悬空引用（route_strategy_id 无父行）时关外键（schedprefmigration
	// openMigrationDB 同规）；Run 自身的重建按 PRAGMA 现值恢复，不受影响。
	if _, err := db.Exec("PRAGMA foreign_keys=OFF"); err != nil {
		t.Fatalf("toggle foreign_keys: %v", err)
	}
	return db
}

func openMemorySQLite(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name()))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func tableColumns(t *testing.T, db *sql.DB, table string) []string {
	t.Helper()
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		t.Fatalf("PRAGMA table_info(%s): %v", table, err)
	}
	defer rows.Close()
	var columns []string
	for rows.Next() {
		var cid, notNull, pk int
		var name, declaredType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &declaredType, &notNull, &defaultValue, &pk); err != nil {
			t.Fatalf("scan table_info: %v", err)
		}
		columns = append(columns, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("table_info rows: %v", err)
	}
	return columns
}

func TestRunSQLiteRetiresLegacyArtifacts(t *testing.T) {
	db := openEnsuredBusinessDB(t, "studio-retire-full")
	if _, err := db.Exec(legacyStudioFixture); err != nil {
		t.Fatalf("seed legacy studio fixture: %v", err)
	}
	seedKey(t, db, "k-studio", "owner-1", "studio")
	seedKey(t, db, "k-general", "owner-1", "general")
	seedKey(t, db, "k-chat", "owner-2", "chat")

	report, err := Run(context.Background(), db, capabilitydomain.DialectSQLite, fixtureClock)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if report.Driver != "sqlite" {
		t.Fatalf("driver = %q", report.Driver)
	}
	// preflight 计数。
	for _, table := range studioTables {
		if report.StudioTableRowCounts[table] != 1 {
			t.Fatalf("preflight count %s = %d, want 1", table, report.StudioTableRowCounts[table])
		}
	}
	if len(report.MissingStudioTables) != 0 {
		t.Fatalf("missing tables = %v", report.MissingStudioTables)
	}
	if report.StudioAPIKeyRows != 1 || report.DeletedAPIKeyRows != 1 {
		t.Fatalf("studio key rows: preflight=%d deleted=%d, want 1/1", report.StudioAPIKeyRows, report.DeletedAPIKeyRows)
	}
	if !report.LegacyIndexExisted || !report.LegacyColumnExisted {
		t.Fatalf("legacy index/column preflight = %v/%v, want true/true", report.LegacyIndexExisted, report.LegacyColumnExisted)
	}
	// 执行动作。
	if len(report.DroppedTables) != len(studioTables) {
		t.Fatalf("dropped tables = %v", report.DroppedTables)
	}
	if !report.DroppedIndex || !report.DroppedColumn || !report.RebuiltTable {
		t.Fatalf("dropped index/column, rebuilt = %v/%v/%v, want all true", report.DroppedIndex, report.DroppedColumn, report.RebuiltTable)
	}
	if !report.Ready() {
		t.Fatalf("report not ready: %+v", report)
	}
	if report.AlreadyRetired {
		t.Fatal("first run must not be alreadyRetired")
	}
	// 后检：对象消失、非 studio 行保真。
	for _, table := range studioTables {
		if exists, err := sqliteTableExists(context.Background(), db, table); err != nil || exists {
			t.Fatalf("table %s still exists (err=%v)", table, err)
		}
	}
	if exists, err := sqliteIndexExists(context.Background(), db, legacyAPIKeysIndex); err != nil || exists {
		t.Fatalf("legacy index still exists (err=%v)", err)
	}
	if exists, err := sqliteColumnExists(context.Background(), db, "api_keys", legacyAPIKeysColumn); err != nil || exists {
		t.Fatalf("legacy column still exists (err=%v)", err)
	}
	if !keyExists(t, db, "k-general") || !keyExists(t, db, "k-chat") {
		t.Fatal("general/chat keys must survive retirement")
	}
	if keyExists(t, db, "k-studio") {
		t.Fatal("studio key must be deleted")
	}
	// 幂等：重跑零写入。
	rerun, err := Run(context.Background(), db, capabilitydomain.DialectSQLite, fixtureClock)
	if err != nil {
		t.Fatalf("rerun: %v", err)
	}
	if !rerun.Ready() || !rerun.AlreadyRetired {
		t.Fatalf("rerun = %+v, want ready/alreadyRetired", rerun)
	}
	if rerun.DeletedAPIKeyRows != 0 || len(rerun.DroppedTables) != 0 || rerun.DroppedIndex || rerun.DroppedColumn || rerun.RebuiltTable {
		t.Fatalf("rerun must not write: %+v", rerun)
	}
	// 三值 legacy 词表保持可写（'studio' 是遗留词表值，不被 CHECK 拒绝）。
	seedKey(t, db, "k-studio-legacy-vocab", "owner-3", "studio")
	if !keyExists(t, db, "k-studio-legacy-vocab") {
		t.Fatal("three-value legacy CHECK must still accept 'studio' rows")
	}
}

func TestRunSQLiteFreshDatabaseReadyWithoutWrites(t *testing.T) {
	db := openEnsuredBusinessDB(t, "studio-retire-fresh")
	seedKey(t, db, "k-general", "owner-1", "general")

	report, err := Run(context.Background(), db, capabilitydomain.DialectSQLite, fixtureClock)
	if err != nil {
		t.Fatalf("run over fresh database: %v", err)
	}
	if !report.Ready() || !report.AlreadyRetired {
		t.Fatalf("fresh report = %+v, want ready/alreadyRetired", report)
	}
	if len(report.MissingStudioTables) != len(studioTables) {
		t.Fatalf("missing tables = %v, want all %d", report.MissingStudioTables, len(studioTables))
	}
	if !keyExists(t, db, "k-general") {
		t.Fatal("fresh key must survive")
	}
}

func TestRunSQLiteLegacyColumnOnlyRebuildsRowPreserving(t *testing.T) {
	db := openEnsuredBusinessDB(t, "studio-retire-column-only")
	if _, err := db.Exec(`ALTER TABLE api_keys ADD COLUMN studio_domains_json TEXT`); err != nil {
		t.Fatalf("add legacy column: %v", err)
	}
	seedKey(t, db, "k-general", "owner-1", "general")
	seedKey(t, db, "k-chat", "owner-2", "chat")

	report, err := Run(context.Background(), db, capabilitydomain.DialectSQLite, fixtureClock)
	if err != nil {
		t.Fatalf("run over legacy-column database: %v", err)
	}
	if !report.LegacyColumnExisted || !report.DroppedColumn || !report.RebuiltTable {
		t.Fatalf("report = %+v, want column dropped via rebuild", report)
	}
	if !report.Ready() {
		t.Fatalf("report not ready: %+v", report)
	}
	if !keyExists(t, db, "k-general") || !keyExists(t, db, "k-chat") {
		t.Fatal("rebuild must be row-preserving (no purpose filter)")
	}
	// 索引随重建恢复（含守卫链尾的 scheduling_preference 索引）。
	for _, index := range []string{"idx_api_keys_chat_purpose_unique", "idx_api_keys_scheduling_preference", legacyAPIKeysIndex} {
		var count int
		if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'index' AND name = ?`, index).Scan(&count); err != nil {
			t.Fatalf("query index %s: %v", index, err)
		}
		if index == legacyAPIKeysIndex {
			if count != 0 {
				t.Fatalf("legacy index must stay dropped")
			}
			continue
		}
		if count != 1 {
			t.Fatalf("index %s missing after rebuild", index)
		}
	}
}

// TestSQLiteRebuildShapeMatchesSchemaDDL 钉住重建表与 ensure 新建表的列集与
// 列序一致（防两份 DDL 漂移，chatbindingmigration 同名先例）。
func TestSQLiteRebuildShapeMatchesSchemaDDL(t *testing.T) {
	rebuilt := openEnsuredBusinessDB(t, "studio-retire-shape-rebuilt")
	if _, err := rebuilt.Exec(`ALTER TABLE api_keys ADD COLUMN studio_domains_json TEXT`); err != nil {
		t.Fatalf("add legacy column: %v", err)
	}
	if _, err := Run(context.Background(), rebuilt, capabilitydomain.DialectSQLite, fixtureClock); err != nil {
		t.Fatalf("run: %v", err)
	}
	ensured := openEnsuredBusinessDB(t, "studio-retire-shape-ensured")
	rebuiltColumns := tableColumns(t, rebuilt, "api_keys")
	ensuredColumns := tableColumns(t, ensured, "api_keys")
	if len(rebuiltColumns) != len(ensuredColumns) {
		t.Fatalf("column count mismatch: rebuilt %v ensured %v", rebuiltColumns, ensuredColumns)
	}
	for index, column := range ensuredColumns {
		if rebuiltColumns[index] != column {
			t.Fatalf("column %d mismatch: rebuilt %q ensured %q", index, rebuiltColumns[index], column)
		}
	}
	var ddl string
	if err := rebuilt.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='api_keys'`).Scan(&ddl); err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{"'general', 'chat', 'studio'", "scheduling_preference_id TEXT REFERENCES scheduling_preferences(id)"} {
		if !strings.Contains(ddl, fragment) {
			t.Fatalf("rebuilt api_keys DDL lacks %q:\n%s", fragment, ddl)
		}
	}
}

func TestRunSQLiteMissingBusinessTableFails(t *testing.T) {
	db := openMemorySQLite(t)
	if _, err := Run(context.Background(), db, capabilitydomain.DialectSQLite, fixtureClock); err == nil {
		t.Fatal("expected error over missing api_keys table")
	}
}

// TestRollbackNoteExplainsBackupRestore 校验报告明示回滚路径 = 外部备份恢复
// （DELETE/DROP 无备份表可回填，与 chatbindingmigration 的回滚 SQL 先例不同）。
func TestRollbackNoteExplainsBackupRestore(t *testing.T) {
	db := openEnsuredBusinessDB(t, "studio-retire-rollback-note")
	report, err := Run(context.Background(), db, capabilitydomain.DialectSQLite, fixtureClock)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	for _, fragment := range []string{"--backup-confirmed", "备份", "不携带回滚 SQL"} {
		if !strings.Contains(report.RollbackNote, fragment) {
			t.Fatalf("rollback note lacks %q: %s", fragment, report.RollbackNote)
		}
	}
}

// TestPostgresStudioRetirementLiveSmoke 是 opt-in 真库测试：设
// JUHE_AI_STUDIO_RETIREMENT_PG_SMOKE_URL 指向一次性隔离库时运行真实 PG 方言
// 全流程（EnsurePostgres 建表 → 造 legacy studio 工件 → Run → 重跑），
// plain `go test` 跳过（口径同 schedprefmigration live smoke）。
func TestPostgresStudioRetirementLiveSmoke(t *testing.T) {
	databaseURL := os.Getenv("JUHE_AI_STUDIO_RETIREMENT_PG_SMOKE_URL")
	if databaseURL == "" {
		t.Skip("JUHE_AI_STUDIO_RETIREMENT_PG_SMOKE_URL not set; PostgreSQL studio retirement smoke test skipped")
	}
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatalf("open pgx: %v", err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("ping postgres: %v", err)
	}
	if _, err := schema.EnsurePostgres(ctx, db); err != nil {
		t.Fatalf("ensure postgres: %v", err)
	}
	seed := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatalf("seed %q: %v", query, err)
		}
	}
	seed(`INSERT INTO "juhe_business"."system_accounts"
		(id, username, display_name, description, role, status, password_hash, created_at, updated_at)
		VALUES ($1, $2, $3, NULL, 'user', 'active', 'hash', '2026-10-14T08:00:00Z', '2026-10-14T08:00:00Z')`,
		"sys-smoke", "studio-smoke", "studio 冒烟")
	seed(`INSERT INTO "juhe_business"."route_strategies"
		(id, system_account_id, name, description, mode, status, is_default, config_json, created_at, updated_at)
		VALUES ($1, $2, $3, NULL, 'normal', 'active', 0, NULL, '2026-10-14T08:00:00Z', '2026-10-14T08:00:00Z')`,
		"st-smoke", "sys-smoke", "策略冒烟")
	seed(`INSERT INTO "juhe_business"."api_keys"
		(id, system_account_id, route_strategy_id, name, description, key_hash, key_prefix, key_suffix,
		 key_secret_encrypted, status, is_default, purpose, scheduling_preference_id, created_at, updated_at)
		VALUES ($1, $2, $3, $4, NULL, $5, 'sk-prefix', 'suf', 'enc', 'active', 0, 'general', NULL, '2026-10-14T08:00:00Z', '2026-10-14T08:00:00Z')`,
		"k-smoke", "sys-smoke", "st-smoke", "k-smoke", "hash-smoke")
	seed(`INSERT INTO "juhe_business"."api_keys"
		(id, system_account_id, route_strategy_id, name, description, key_hash, key_prefix, key_suffix,
		 key_secret_encrypted, status, is_default, purpose, scheduling_preference_id, created_at, updated_at)
		VALUES ($1, $2, $3, $4, NULL, $5, 'sk-prefix', 'suf', 'enc', 'active', 0, 'studio', NULL, '2026-10-14T08:00:00Z', '2026-10-14T08:00:00Z')`,
		"k-smoke-studio", "sys-smoke", "st-smoke", "k-smoke-studio", "hash-smoke-studio")
	seed(`CREATE TABLE "juhe_business"."studio_assets" (id text PRIMARY KEY, created_at text NOT NULL)`)
	seed(`INSERT INTO "juhe_business"."studio_assets" (id, created_at) VALUES ('asset-smoke', '2026-10-14T08:00:00Z')`)
	seed(`CREATE UNIQUE INDEX "juhe_business".idx_api_keys_studio_purpose_unique ON "juhe_business".api_keys(system_account_id) WHERE purpose = 'studio'`)
	seed(`ALTER TABLE "juhe_business".api_keys ADD COLUMN studio_domains_json text`)

	report, err := Run(ctx, db, capabilitydomain.DialectPostgres, fixtureClock)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !report.Ready() {
		t.Fatalf("PG report not ready: %+v", report)
	}
	if report.StudioAPIKeyRows != 1 || report.DeletedAPIKeyRows != 1 {
		t.Fatalf("PG studio key rows: preflight=%d deleted=%d, want 1/1", report.StudioAPIKeyRows, report.DeletedAPIKeyRows)
	}
	if len(report.DroppedTables) != 1 || !report.DroppedIndex || !report.DroppedColumn || report.RebuiltTable {
		t.Fatalf("PG actions = %+v", report)
	}
	var generalKeys int
	if err := db.QueryRow(`SELECT count(*) FROM "juhe_business".api_keys WHERE purpose = 'general'`).Scan(&generalKeys); err != nil || generalKeys != 1 {
		t.Fatalf("PG general key must survive: %v %d", err, generalKeys)
	}
	rerun, err := Run(ctx, db, capabilitydomain.DialectPostgres, fixtureClock)
	if err != nil {
		t.Fatalf("rerun: %v", err)
	}
	if !rerun.Ready() || !rerun.AlreadyRetired {
		t.Fatalf("PG rerun = %+v, want ready/alreadyRetired", rerun)
	}
}
