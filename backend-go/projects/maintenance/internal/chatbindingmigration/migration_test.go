package chatbindingmigration

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/schema"
)

// legacyConversationDDL 是三种绑定模式时期的 chat_conversations 形状
// （bind_mode 带 IN CHECK 与 NOT NULL DEFAULT，来自迁移前的 chat schema）。
const legacyConversationDDL = `CREATE TABLE chat_conversations (
      id TEXT PRIMARY KEY,
      system_account_id TEXT NOT NULL,
      api_key_id TEXT,
      api_key_name_snapshot TEXT NOT NULL,
      bind_mode TEXT NOT NULL DEFAULT 'api_key',
      bind_group_id TEXT,
      bind_group_name_snapshot TEXT,
      bind_account_id TEXT,
      bind_account_name_snapshot TEXT,
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
      updated_at TEXT NOT NULL,
      CHECK (next_sequence_no >= 1),
      CHECK (user_turn_count >= 0),
      CHECK (message_revision >= 0),
      CHECK (is_pinned IN (0, 1)),
      CHECK (bind_mode IN ('api_key', 'group', 'account')),
      CHECK (context_revision >= 0)
    )`

// seedLegacyRows 灌入三种存量形态：account（保留）、group/api_key（归档）。
// NULL bind_mode 在 SQLite 被 NOT NULL 约束阻止，无法经 INSERT 构造；迁移契约
// 的 IS NULL 臂覆盖的是 PG 上历史缺列行（PG 侧经 ADD COLUMN 期缺省行）。
func seedLegacyRows(t *testing.T, db *sql.DB) {
	t.Helper()
	rows := []struct {
		id, bindMode, groupID, groupName, accountID string
	}{
		{"conv_account", "account", "", "", "account-1"},
		{"conv_group", "group", "group-a", "分组 A", ""},
		{"conv_apikey", "api_key", "", "", ""},
	}
	for _, row := range rows {
		var groupIDValue, groupNameValue, accountIDValue any
		if row.groupID != "" {
			groupIDValue = row.groupID
		}
		if row.groupName != "" {
			groupNameValue = row.groupName
		}
		if row.accountID != "" {
			accountIDValue = row.accountID
		}
		if _, err := db.Exec(`INSERT INTO chat_conversations
			(id, system_account_id, api_key_name_snapshot, bind_mode, bind_group_id, bind_group_name_snapshot, bind_account_id,
			 last_message_at, created_at, updated_at)
			VALUES (?, 'owner-1', '密钥', ?, ?, ?, ?, '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`,
			row.id, row.bindMode, groupIDValue, groupNameValue, accountIDValue); err != nil {
			t.Fatalf("seed %s: %v", row.id, err)
		}
	}
}

func openMemorySQLite(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open memory sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func tableColumns(t *testing.T, db *sql.DB, table string) []string {
	t.Helper()
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		t.Fatalf("pragma table_info(%s): %v", table, err)
	}
	defer rows.Close()
	columns := []string{}
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
		t.Fatalf("iterate table_info: %v", err)
	}
	return columns
}

func hasColumn(columns []string, name string) bool {
	for _, column := range columns {
		if column == name {
			return true
		}
	}
	return false
}

// TestRunSQLiteMigratesAndIsIdempotent 覆盖迁移契约全链：归档标记、三列备份、
// 列删除、幂等重放（第二遍零写入）。
func TestRunSQLiteMigratesAndIsIdempotent(t *testing.T) {
	db := openMemorySQLite(t)
	if _, err := db.Exec(legacyConversationDDL); err != nil {
		t.Fatalf("seed legacy DDL: %v", err)
	}
	seedLegacyRows(t, db)
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)

	report, err := Run(context.Background(), db, DialectSQLite, now)
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if !report.Ready() {
		t.Fatalf("first run not ready: %+v", report)
	}
	if report.AlreadyMigrated {
		t.Fatal("first run must not report AlreadyMigrated")
	}
	if report.ArchivedRowsUpdated != 2 || report.ArchivedRowsTotal != 2 {
		t.Fatalf("archived rows = updated %d total %d, want 2/2 (group+api_key)", report.ArchivedRowsUpdated, report.ArchivedRowsTotal)
	}
	// 列删除断言：旧三列不存在，新四列存在，保留列仍在。
	columns := tableColumns(t, db, "chat_conversations")
	for _, legacy := range legacyColumns {
		if hasColumn(columns, legacy) {
			t.Errorf("chat_conversations still has legacy column %s", legacy)
		}
	}
	for _, added := range []string{"archived", "search_account_id", "search_model_id", "image_account_id", "bind_account_id"} {
		if !hasColumn(columns, added) {
			t.Errorf("chat_conversations lacks %s after migration", added)
		}
	}
	// 数据断言：account 行保留绑定且未归档；group/api_key 行归档且保留消息事实。
	var accountArchived int
	var accountBound string
	if err := db.QueryRow(`SELECT archived, COALESCE(bind_account_id, '') FROM chat_conversations WHERE id = 'conv_account'`).Scan(&accountArchived, &accountBound); err != nil {
		t.Fatalf("read conv_account: %v", err)
	}
	if accountArchived != 0 || accountBound != "account-1" {
		t.Fatalf("conv_account archived=%d bind=%q, want 0/account-1", accountArchived, accountBound)
	}
	var groupArchived, apikeyArchived int
	if err := db.QueryRow(`SELECT archived FROM chat_conversations WHERE id = 'conv_group'`).Scan(&groupArchived); err != nil {
		t.Fatalf("read conv_group: %v", err)
	}
	if err := db.QueryRow(`SELECT archived FROM chat_conversations WHERE id = 'conv_apikey'`).Scan(&apikeyArchived); err != nil {
		t.Fatalf("read conv_apikey: %v", err)
	}
	if groupArchived != 1 || apikeyArchived != 1 {
		t.Fatalf("archived flags group=%d apikey=%d, want 1/1", groupArchived, apikeyArchived)
	}
	// 副表备份断言：全量行、列值保真。
	if report.BackedUpRows != 3 {
		t.Fatalf("backup rows = %d, want 3", report.BackedUpRows)
	}
	var backedMode, backedGroupID, backedGroupName string
	if err := db.QueryRow(`SELECT bind_mode, COALESCE(bind_group_id, ''), COALESCE(bind_group_name_snapshot, '') FROM chat_conversations_bind_legacy_backup WHERE id = 'conv_group'`).Scan(&backedMode, &backedGroupID, &backedGroupName); err != nil {
		t.Fatalf("read backup row: %v", err)
	}
	if backedMode != "group" || backedGroupID != "group-a" || backedGroupName != "分组 A" {
		t.Fatalf("backup row = %q/%q/%q, want group/group-a/分组 A", backedMode, backedGroupID, backedGroupName)
	}
	// 幂等重放：第二遍零写入、零报错、AlreadyMigrated=true。
	second, err := Run(context.Background(), db, DialectSQLite, now)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if !second.Ready() {
		t.Fatalf("second run not ready: %+v", second)
	}
	if !second.AlreadyMigrated {
		t.Fatal("second run must report AlreadyMigrated")
	}
	if second.ArchivedRowsUpdated != 0 || second.RebuiltTable {
		t.Fatalf("second run wrote: %+v", second)
	}
	if second.ArchivedRowsTotal != 2 {
		t.Fatalf("second run archived total = %d, want 2", second.ArchivedRowsTotal)
	}
	if second.RollbackSQL == "" {
		t.Fatal("rollback SQL missing from report")
	}
}

// TestRunSQLiteAlreadyNewShape 校验从未有旧列的库（新 ensure 直接建新形状）
// 迁移为无写入门的幂等 no-op。
func TestRunSQLiteAlreadyNewShape(t *testing.T) {
	db := openMemorySQLite(t)
	if _, err := schema.EnsureSQLiteChat(context.Background(), db); err != nil {
		t.Fatalf("EnsureSQLiteChat: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO chat_conversations (id, system_account_id, api_key_name_snapshot, bind_account_id, last_message_at, created_at, updated_at)
		VALUES ('conv_new', 'owner-1', '密钥', 'account-1', '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`); err != nil {
		t.Fatalf("seed row: %v", err)
	}
	report, err := Run(context.Background(), db, DialectSQLite, time.Now().UTC())
	if err != nil {
		t.Fatalf("run over new shape: %v", err)
	}
	if !report.Ready() || !report.AlreadyMigrated || report.RebuiltTable {
		t.Fatalf("new-shape report = %+v, want ready/already-migrated/no-rebuild", report)
	}
	var archived int
	if err := db.QueryRow(`SELECT archived FROM chat_conversations WHERE id = 'conv_new'`).Scan(&archived); err != nil {
		t.Fatalf("read archived: %v", err)
	}
	if archived != 0 {
		t.Fatalf("new-shape row archived = %d, want 0", archived)
	}
}

// TestSQLiteRebuildMatchesSchemaShape 钉住迁移重建表与 ensure 新建表列集一致
// （防两份 DDL 漂移；列顺序也一致，扫描结构体依赖 SELECT 列序）。
func TestSQLiteRebuildMatchesSchemaShape(t *testing.T) {
	migrated := openMemorySQLite(t)
	if _, err := migrated.Exec(legacyConversationDDL); err != nil {
		t.Fatalf("seed legacy DDL: %v", err)
	}
	seedLegacyRows(t, migrated)
	if _, err := Run(context.Background(), migrated, DialectSQLite, time.Now().UTC()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	ensured := openMemorySQLite(t)
	if _, err := schema.EnsureSQLiteChat(context.Background(), ensured); err != nil {
		t.Fatalf("EnsureSQLiteChat: %v", err)
	}
	migratedColumns := tableColumns(t, migrated, "chat_conversations")
	ensuredColumns := tableColumns(t, ensured, "chat_conversations")
	if len(migratedColumns) != len(ensuredColumns) {
		t.Fatalf("column count mismatch: migrated %v ensured %v", migratedColumns, ensuredColumns)
	}
	for index, column := range ensuredColumns {
		if migratedColumns[index] != column {
			t.Fatalf("column %d mismatch: migrated %q ensured %q", index, migratedColumns[index], column)
		}
	}
}

// TestRunSQLiteMissingTableFails 校验目标表缺失时的显式失败（不静默通过）。
func TestRunSQLiteMissingTableFails(t *testing.T) {
	db := openMemorySQLite(t)
	if _, err := Run(context.Background(), db, DialectSQLite, time.Now().UTC()); err == nil {
		t.Fatal("expected error over missing chat_conversations table")
	}
}

// TestRollbackSQLContainsRebuildRecipe 校验报告携带两种方言的回滚 SQL 骨架。
func TestRollbackSQLContainsRebuildRecipe(t *testing.T) {
	sqlite := rollbackSQL(DialectSQLite)
	for _, fragment := range []string{"ADD COLUMN bind_mode", "chat_conversations_bind_legacy_backup", "bind_group_name_snapshot"} {
		if !strings.Contains(sqlite, fragment) {
			t.Errorf("sqlite rollback SQL lacks %q", fragment)
		}
	}
	pg := rollbackSQL(DialectPostgres)
	for _, fragment := range []string{"ADD COLUMN IF NOT EXISTS bind_mode", "juhe_chat.chat_conversations_bind_legacy_backup"} {
		if !strings.Contains(pg, fragment) {
			t.Errorf("postgres rollback SQL lacks %q", fragment)
		}
	}
}
