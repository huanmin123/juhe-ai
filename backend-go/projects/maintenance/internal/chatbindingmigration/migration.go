// Package chatbindingmigration contains the one-shot maintenance migration
// that converges chat_conversations to the account-only binding model
// (docs/functions/AI问答会话账户唯一绑定设计.md §4/§8/§10).
//
// Migration contract (fixed by the design):
//   - rows with bind_mode IS NULL OR bind_mode <> 'account' (存量 api_key/
//     group 会话与缺省行) are stamped archived=1 (只读归档，不删除会话与消息);
//   - the three legacy columns bind_mode / bind_group_id /
//     bind_group_name_snapshot are backed up into
//     chat_conversations_bind_legacy_backup and then dropped (不留停用列);
//   - the new columns archived / search_account_id / search_model_id /
//     image_account_id are ensured on every run (ADD COLUMN, idempotent);
//   - PostgreSQL drops the columns with ALTER TABLE ... DROP COLUMN IF EXISTS
//     (the table CHECK on bind_mode is dropped automatically with it);
//     SQLite cannot DROP COLUMN while a table CHECK references the column
//     (verified against modernc.org/sqlite 3.53.4: "error in table after drop
//     column: no such column: bind_mode"), so SQLite rebuilds the table
//     (create new shape → copy with archived CASE → drop old → rename →
//     recreate the five conversation indexes) inside one transaction with
//     PRAGMA foreign_keys=OFF (restored afterwards);
//   - every step is idempotent: a second run finds no legacy columns and
//     reports alreadyMigrated without writing.
//
// The CLI entry (cmd/juhe-ai-maintenance --migrate-chat-account-only-binding)
// accepts --driver sqlite --chat-sqlite-path FILE or --driver postgres --dsn
// URL and prints the rollback SQL next to the JSON report.
package chatbindingmigration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

// SchemaName is the PostgreSQL schema owning chat_conversations.
const SchemaName = "juhe_chat"

// BackupTable is the side table holding the dropped legacy column data.
const BackupTable = "chat_conversations_bind_legacy_backup"

// legacyColumns are the dropped bind-mode trio, in rollback ADD COLUMN order.
var legacyColumns = []string{"bind_mode", "bind_group_id", "bind_group_name_snapshot"}

// Dialect selects schema qualification, column detection and the drop
// strategy (DROP COLUMN vs table rebuild).
type Dialect int

const (
	DialectPostgres Dialect = iota
	DialectSQLite
)

// String returns the report driver label.
func (d Dialect) String() string {
	if d == DialectSQLite {
		return "sqlite"
	}
	return "postgres"
}

// OpenPostgres validates the explicit maintenance-scoped PostgreSQL URL
// before opening a one-shot connection (no application URL fallback).
func OpenPostgres(rawURL string) (*sql.DB, error) {
	rawURL = strings.TrimSpace(rawURL)
	if !strings.HasPrefix(rawURL, "postgres://") && !strings.HasPrefix(rawURL, "postgresql://") {
		return nil, errors.New("chat account-only binding migration 必须提供 postgres:// 或 postgresql:// URL")
	}
	db, err := sql.Open("pgx", rawURL)
	if err != nil {
		return nil, fmt.Errorf("打开 chat account-only binding migration PostgreSQL 连接失败: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	return db, nil
}

// OpenSQLite opens the chat SQLite file with the maintenance connection
// contract (single connection so PRAGMA foreign_keys toggling below is
// connection-stable, busy timeout, WAL untouched).
func OpenSQLite(rawPath string) (*sql.DB, error) {
	path := strings.TrimSpace(rawPath)
	if path == "" {
		return nil, errors.New("chat account-only binding migration 需要 --chat-sqlite-path 指向 chat SQLite 文件")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("创建 sqlite 目录 %s 失败: %w", filepath.Dir(path), err)
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=rwc&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("打开 chat SQLite 文件 %s 失败: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("初始化 chat SQLite PRAGMA 失败: %w", err)
	}
	return db, nil
}

// Report is the JSON result printed by the command.
type Report struct {
	Driver                 string   `json:"driver"`
	AlreadyMigrated        bool     `json:"alreadyMigrated"`
	ArchivedRowsUpdated    int64    `json:"archivedRowsUpdated"`
	ArchivedRowsTotal      int64    `json:"archivedRowsTotal"`
	BackupTable            string   `json:"backupTable"`
	BackedUpRows           int64    `json:"backedUpRows"`
	DroppedColumns         []string `json:"droppedColumns,omitempty"`
	RebuiltTable           bool     `json:"rebuiltTable"`
	MigratedAt             string   `json:"migratedAt"`
	RemainingLegacyColumns []string `json:"remainingLegacyColumns,omitempty"`
	RollbackSQL            string   `json:"rollbackSql"`
}

// Ready reports the post-condition: none of the legacy columns may remain.
func (r Report) Ready() bool {
	return len(r.RemainingLegacyColumns) == 0
}

// Run performs the idempotent migration. now stamps backup rows and the
// report. Statements run one by one (ensure-schema model, design §10): each
// step is independently idempotent, and the SQLite rebuild — the only
// multi-statement sequence — runs inside one transaction so a mid-way failure
// rolls back to the pre-rebuild state and a rerun converges.
func Run(ctx context.Context, db *sql.DB, dialect Dialect, now time.Time) (Report, error) {
	report := Report{
		Driver:      dialect.String(),
		BackupTable: BackupTable,
		MigratedAt:  isoMillis(now),
	}
	if dialect == DialectSQLite {
		if err := runSQLite(ctx, db, &report, now); err != nil {
			return report, err
		}
	} else {
		if err := runPostgres(ctx, db, &report, now); err != nil {
			return report, err
		}
	}
	remaining, err := detectLegacyColumns(ctx, db, dialect)
	if err != nil {
		return report, err
	}
	report.RemainingLegacyColumns = remaining
	report.AlreadyMigrated = len(remaining) == 0 && !report.RebuiltTable && report.DroppedColumns == nil
	if err := countArchived(ctx, db, dialect, &report); err != nil {
		return report, err
	}
	report.RollbackSQL = rollbackSQL(dialect)
	return report, nil
}

// ensureColumns adds the account-only binding columns when missing. The
// guarded ADD COLUMN list mirrors the schema ensure (bind_account_* covers
// databases predating the bind-mode feature entirely).
func ensureColumns(ctx context.Context, db *sql.DB, dialect Dialect) error {
	if dialect == DialectSQLite {
		columns := []struct{ column, decl string }{
			{"bind_account_id", "TEXT"},
			{"bind_account_name_snapshot", "TEXT"},
			{"archived", "INTEGER NOT NULL DEFAULT 0 CHECK (archived IN (0, 1))"},
			{"search_account_id", "TEXT"},
			{"search_model_id", "TEXT"},
			{"image_account_id", "TEXT"},
		}
		for _, target := range columns {
			exists, err := sqliteColumnExists(ctx, db, "chat_conversations", target.column)
			if err != nil {
				return err
			}
			if exists {
				continue
			}
			if _, err := db.ExecContext(ctx, "ALTER TABLE chat_conversations ADD COLUMN "+target.column+" "+target.decl); err != nil {
				return fmt.Errorf("ensure chat_conversations.%s: %w", target.column, err)
			}
		}
		return nil
	}
	statements := []string{
		`ALTER TABLE juhe_chat.chat_conversations ADD COLUMN IF NOT EXISTS bind_account_id text`,
		`ALTER TABLE juhe_chat.chat_conversations ADD COLUMN IF NOT EXISTS bind_account_name_snapshot text`,
		`ALTER TABLE juhe_chat.chat_conversations ADD COLUMN IF NOT EXISTS archived integer NOT NULL DEFAULT 0 CHECK (archived IN (0, 1))`,
		`ALTER TABLE juhe_chat.chat_conversations ADD COLUMN IF NOT EXISTS search_account_id text`,
		`ALTER TABLE juhe_chat.chat_conversations ADD COLUMN IF NOT EXISTS search_model_id text`,
		`ALTER TABLE juhe_chat.chat_conversations ADD COLUMN IF NOT EXISTS image_account_id text`,
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("ensure account-binding columns: %w", err)
		}
	}
	return nil
}

// runPostgres migrates a PostgreSQL chat schema.
func runPostgres(ctx context.Context, db *sql.DB, report *Report, now time.Time) error {
	if err := ensureColumns(ctx, db, DialectPostgres); err != nil {
		return err
	}
	hasBindMode, err := pgColumnExists(ctx, db, "bind_mode")
	if err != nil {
		return err
	}
	if hasBindMode {
		result, err := db.ExecContext(ctx, `UPDATE juhe_chat.chat_conversations
			SET archived = 1 WHERE bind_mode IS NULL OR bind_mode <> 'account'`)
		if err != nil {
			return fmt.Errorf("归档旧模式会话失败: %w", err)
		}
		if affected, err := result.RowsAffected(); err == nil {
			report.ArchivedRowsUpdated = affected
		}
	}
	if hasBindMode {
		if err := backupLegacyColumns(ctx, db, DialectPostgres, now); err != nil {
			return err
		}
		if err := countBackupRows(ctx, db, DialectPostgres, report); err != nil {
			return err
		}
		if _, err := db.ExecContext(ctx, `ALTER TABLE juhe_chat.chat_conversations
			DROP COLUMN IF EXISTS bind_mode,
			DROP COLUMN IF EXISTS bind_group_id,
			DROP COLUMN IF EXISTS bind_group_name_snapshot`); err != nil {
			return fmt.Errorf("删除旧绑定列失败: %w", err)
		}
		report.DroppedColumns = append([]string{}, legacyColumns...)
	}
	return nil
}

// runSQLite migrates a SQLite chat database.
func runSQLite(ctx context.Context, db *sql.DB, report *Report, now time.Time) error {
	tableExists, err := sqliteTableExists(ctx, db, "chat_conversations")
	if err != nil {
		return err
	}
	if !tableExists {
		return errors.New("chat_conversations 表不存在：请先对该库执行 --ensure-schema 再迁移")
	}
	if err := ensureColumns(ctx, db, DialectSQLite); err != nil {
		return err
	}
	hasBindMode, err := sqliteColumnExists(ctx, db, "chat_conversations", "bind_mode")
	if err != nil {
		return err
	}
	if !hasBindMode {
		// 已迁移（或从未有旧列）：新列已在上方 ensure，直接收敛报告。
		if err := countBackupRows(ctx, db, DialectSQLite, report); err != nil {
			return err
		}
		return nil
	}
	// 旧列存在：备份 → 归档计数 → 表重建（DROP COLUMN 被表级 CHECK 阻断）。
	if err := backupLegacyColumns(ctx, db, DialectSQLite, now); err != nil {
		return err
	}
	result, err := db.ExecContext(ctx, `UPDATE chat_conversations
		SET archived = 1 WHERE bind_mode IS NULL OR bind_mode <> 'account'`)
	if err != nil {
		return fmt.Errorf("归档旧模式会话失败: %w", err)
	}
	if affected, err := result.RowsAffected(); err == nil {
		report.ArchivedRowsUpdated = affected
	}
	if err := countBackupRows(ctx, db, DialectSQLite, report); err != nil {
		return err
	}
	if err := rebuildSQLiteConversations(ctx, db); err != nil {
		return err
	}
	report.RebuiltTable = true
	report.DroppedColumns = append([]string{}, legacyColumns...)
	return nil
}

// retainedConversationColumns is the post-migration chat_conversations column
// list in storage order (the rebuild INSERT ... SELECT column list).
var retainedConversationColumns = []string{
	"id", "system_account_id", "api_key_id", "api_key_name_snapshot",
	"bind_account_id", "bind_account_name_snapshot",
	"title", "title_source_message_id",
	"is_pinned", "last_model", "default_image_model", "next_sequence_no",
	"user_turn_count", "message_revision",
	"active_turn_id", "active_started_at", "context_revision", "active_checkpoint_id",
	"compacted_through_sequence", "context_state", "active_context_tokens",
	"effective_context_limit_tokens", "context_usage_estimated",
	"context_claim_id", "context_claim_revision", "context_claim_through_sequence",
	"context_claimed_at", "context_retry_at", "context_attempt_count",
	"context_error_code", "context_progress_sequence",
	"context_progress_earliest_expires_at", "last_message_at", "created_at", "updated_at",
}

// sqliteConversationIndexes recreates the five chat_conversations indexes
// after the rebuild (index DDL mirrors the chat schema script).
var sqliteConversationIndexes = []string{
	`CREATE INDEX IF NOT EXISTS idx_chat_conversations_owner_recent
      ON chat_conversations(system_account_id, last_message_at DESC, id DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_chat_conversations_owner_pinned_recent
      ON chat_conversations(system_account_id, is_pinned DESC, last_message_at DESC, id DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_chat_conversations_owner_api_key
      ON chat_conversations(system_account_id, api_key_id)`,
	`CREATE INDEX IF NOT EXISTS idx_chat_conversations_active_started
      ON chat_conversations(active_started_at, id)`,
	`CREATE INDEX IF NOT EXISTS idx_chat_conversations_context_queue
      ON chat_conversations(context_state, context_retry_at, context_claimed_at, updated_at, id)`,
}

// rebuildSQLiteConversations performs the SQLite table rebuild inside one
// transaction with PRAGMA foreign_keys=OFF (restored afterwards). The staging
// table is dropped first so a previous mid-way failure reruns cleanly.
func rebuildSQLiteConversations(ctx context.Context, db *sql.DB) error {
	foreignKeys := 0
	if err := db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		return fmt.Errorf("读取 PRAGMA foreign_keys 失败: %w", err)
	}
	if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys=OFF"); err != nil {
		return fmt.Errorf("关闭 PRAGMA foreign_keys 失败: %w", err)
	}
	defer func() {
		if foreignKeys != 0 {
			_, _ = db.ExecContext(context.WithoutCancel(ctx), "PRAGMA foreign_keys=ON")
		}
	}()
	if _, err := db.ExecContext(ctx, "DROP TABLE IF EXISTS chat_conversations_migrating"); err != nil {
		return fmt.Errorf("清理上次重建残留失败: %w", err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, sqliteRebuildConversationDDL); err != nil {
		return fmt.Errorf("创建重建目标表失败: %w", err)
	}
	copyColumns := append(append([]string{}, retainedConversationColumns...),
		"archived", "search_account_id", "search_model_id", "image_account_id",
		// 媒体账户四列（chat schema 24d40743c 起）：legacy 表无这些列，重建按
		// 可空缺省回填 NULL，与 ensure-schema 的 chat 形状对齐。
		"video_account_id", "default_video_model", "audio_account_id", "default_audio_model")
	selectColumns := append(append([]string{}, retainedConversationColumns...),
		"CASE WHEN bind_mode IS NULL OR bind_mode <> 'account' THEN 1 ELSE 0 END AS archived",
		"NULL AS search_account_id", "NULL AS search_model_id", "NULL AS image_account_id",
		"NULL AS video_account_id", "NULL AS default_video_model",
		"NULL AS audio_account_id", "NULL AS default_audio_model")
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(
		"INSERT INTO chat_conversations_migrating (%s)\nSELECT %s FROM chat_conversations",
		strings.Join(copyColumns, ", "), strings.Join(selectColumns, ", "))); err != nil {
		return fmt.Errorf("回填重建表数据失败: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "DROP TABLE chat_conversations"); err != nil {
		return fmt.Errorf("删除旧 chat_conversations 失败: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "ALTER TABLE chat_conversations_migrating RENAME TO chat_conversations"); err != nil {
		return fmt.Errorf("重命名重建表失败: %w", err)
	}
	for _, statement := range sqliteConversationIndexes {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("重建 chat_conversations 索引失败: %w", err)
		}
	}
	return tx.Commit()
}

// sqliteRebuildConversationDDL is the post-migration chat_conversations shape
// (mirrors the updated chat schema DDL; the schema test asserts column parity
// with schema.EnsureSQLiteChat so the two definitions cannot drift).
const sqliteRebuildConversationDDL = `CREATE TABLE chat_conversations_migrating (
      id TEXT PRIMARY KEY,
      system_account_id TEXT NOT NULL,
      api_key_id TEXT,
      api_key_name_snapshot TEXT NOT NULL,
      bind_account_id TEXT,
      bind_account_name_snapshot TEXT,
      archived INTEGER NOT NULL DEFAULT 0,
      search_account_id TEXT,
      search_model_id TEXT,
      image_account_id TEXT,
      video_account_id TEXT,
      default_video_model TEXT,
      audio_account_id TEXT,
      default_audio_model TEXT,
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
      CHECK (archived IN (0, 1)),
      CHECK (context_revision >= 0),
      CHECK (compacted_through_sequence >= 0 AND compacted_through_sequence < next_sequence_no),
      CHECK (context_state IN ('ready', 'compact_pending', 'compacting', 'compact_failed')),
      CHECK (active_context_tokens IS NULL OR active_context_tokens >= 0),
      CHECK (effective_context_limit_tokens IS NULL OR effective_context_limit_tokens > 0),
      CHECK (context_usage_estimated IN (0, 1)),
      CHECK (context_attempt_count >= 0),
      CHECK (context_progress_sequence >= 0),
      CHECK (
        (active_checkpoint_id IS NULL AND compacted_through_sequence = 0)
        OR active_checkpoint_id IS NOT NULL
      ),
      CHECK (
        (
          context_state = 'compacting'
          AND context_claim_id IS NOT NULL
          AND context_claim_revision = context_revision
          AND context_claim_through_sequence IS NOT NULL
          AND context_claim_through_sequence > compacted_through_sequence
          AND context_claim_through_sequence <= next_sequence_no - 3
          AND context_claimed_at IS NOT NULL
          AND context_progress_sequence >= compacted_through_sequence
          AND context_progress_sequence <= context_claim_through_sequence
        )
        OR (
          context_state != 'compacting'
          AND context_claim_id IS NULL
          AND context_claim_revision IS NULL
          AND context_claim_through_sequence IS NULL
          AND context_claimed_at IS NULL
          AND context_progress_sequence = 0
          AND context_progress_earliest_expires_at IS NULL
        )
      )
    )`

// backupLegacyColumns creates/refreshes the backup side table with the
// legacy column data of every conversation row (INSERT ... ON CONFLICT keeps
// a rerun idempotent while refreshing values).
func backupLegacyColumns(ctx context.Context, db *sql.DB, dialect Dialect, now time.Time) error {
	if dialect == DialectSQLite {
		if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS `+BackupTable+` (
			id TEXT PRIMARY KEY,
			bind_mode TEXT,
			bind_group_id TEXT,
			bind_group_name_snapshot TEXT,
			backed_up_at TEXT NOT NULL
		)`); err != nil {
			return fmt.Errorf("创建备份副表失败: %w", err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO `+BackupTable+` (id, bind_mode, bind_group_id, bind_group_name_snapshot, backed_up_at)
			SELECT id, bind_mode, bind_group_id, bind_group_name_snapshot, ? FROM chat_conversations WHERE true
			ON CONFLICT(id) DO UPDATE SET
				bind_mode = excluded.bind_mode,
				bind_group_id = excluded.bind_group_id,
				bind_group_name_snapshot = excluded.bind_group_name_snapshot,
				backed_up_at = excluded.backed_up_at`, isoMillis(now)); err != nil {
			return fmt.Errorf("备份旧绑定列数据失败: %w", err)
		}
		return nil
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS `+SchemaName+`.`+BackupTable+` (
		id text PRIMARY KEY,
		bind_mode text,
		bind_group_id text,
		bind_group_name_snapshot text,
		backed_up_at text NOT NULL
	)`); err != nil {
		return fmt.Errorf("创建备份副表失败: %w", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO `+SchemaName+`.`+BackupTable+` (id, bind_mode, bind_group_id, bind_group_name_snapshot, backed_up_at)
		SELECT id, bind_mode, bind_group_id, bind_group_name_snapshot, $1 FROM `+SchemaName+`.chat_conversations
		ON CONFLICT (id) DO UPDATE SET
			bind_mode = EXCLUDED.bind_mode,
			bind_group_id = EXCLUDED.bind_group_id,
			bind_group_name_snapshot = EXCLUDED.bind_group_name_snapshot,
			backed_up_at = EXCLUDED.backed_up_at`, isoMillis(now)); err != nil {
		return fmt.Errorf("备份旧绑定列数据失败: %w", err)
	}
	return nil
}

func countBackupRows(ctx context.Context, db *sql.DB, dialect Dialect, report *Report) error {
	exists := false
	var err error
	if dialect == DialectSQLite {
		exists, err = sqliteTableExists(ctx, db, BackupTable)
	} else {
		exists, err = pgTableExists(ctx, db, BackupTable)
	}
	if err != nil || !exists {
		return err
	}
	query := "SELECT COUNT(*) FROM chat_conversations_bind_legacy_backup"
	if dialect == DialectPostgres {
		query = "SELECT COUNT(*) FROM " + SchemaName + "." + BackupTable
	}
	var count int64
	if err := db.QueryRowContext(ctx, query).Scan(&count); err != nil {
		return err
	}
	report.BackedUpRows = count
	return nil
}

func countArchived(ctx context.Context, db *sql.DB, dialect Dialect, report *Report) error {
	query := "SELECT COUNT(*) FROM chat_conversations WHERE archived = 1"
	if dialect == DialectPostgres {
		query = "SELECT COUNT(*) FROM " + SchemaName + ".chat_conversations WHERE archived = 1"
	}
	var count int64
	if err := db.QueryRowContext(ctx, query).Scan(&count); err != nil {
		return err
	}
	report.ArchivedRowsTotal = count
	return nil
}

func detectLegacyColumns(ctx context.Context, db *sql.DB, dialect Dialect) ([]string, error) {
	remaining := []string{}
	for _, column := range legacyColumns {
		exists := false
		var err error
		if dialect == DialectSQLite {
			exists, err = sqliteColumnExists(ctx, db, "chat_conversations", column)
		} else {
			exists, err = pgColumnExists(ctx, db, column)
		}
		if err != nil {
			return nil, err
		}
		if exists {
			remaining = append(remaining, column)
		}
	}
	return remaining, nil
}

func sqliteColumnExists(ctx context.Context, db *sql.DB, table, column string) (bool, error) {
	rows, err := db.QueryContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, pk int
		var name, declaredType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &declaredType, &notNull, &defaultValue, &pk); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

func sqliteTableExists(ctx context.Context, db *sql.DB, table string) (bool, error) {
	var name string
	err := db.QueryRowContext(ctx, "SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?", table).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func pgColumnExists(ctx context.Context, db *sql.DB, column string) (bool, error) {
	var count int
	err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.columns
		WHERE table_schema = $1 AND table_name = 'chat_conversations' AND column_name = $2`,
		SchemaName, column).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

func pgTableExists(ctx context.Context, db *sql.DB, table string) (bool, error) {
	var count int
	err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.tables
		WHERE table_schema = $1 AND table_name = $2`, SchemaName, table).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// rollbackSQL returns the dialect-specific rollback recipe (design §10 回滚
// 窗口)：代码回滚后从备份副表重建三列并回填存量值。
func rollbackSQL(dialect Dialect) string {
	if dialect == DialectSQLite {
		return `-- SQLite 回滚（代码回滚到旧版本二进制后执行）：
ALTER TABLE chat_conversations ADD COLUMN bind_mode TEXT NOT NULL DEFAULT 'api_key';
ALTER TABLE chat_conversations ADD COLUMN bind_group_id TEXT;
ALTER TABLE chat_conversations ADD COLUMN bind_group_name_snapshot TEXT;
UPDATE chat_conversations SET
  bind_mode = COALESCE((SELECT b.bind_mode FROM chat_conversations_bind_legacy_backup b WHERE b.id = chat_conversations.id), 'api_key'),
  bind_group_id = (SELECT b.bind_group_id FROM chat_conversations_bind_legacy_backup b WHERE b.id = chat_conversations.id),
  bind_group_name_snapshot = (SELECT b.bind_group_name_snapshot FROM chat_conversations_bind_legacy_backup b WHERE b.id = chat_conversations.id);`
	}
	return `-- PostgreSQL 回滚（代码回滚到旧版本二进制后执行）：
ALTER TABLE juhe_chat.chat_conversations ADD COLUMN IF NOT EXISTS bind_mode text NOT NULL DEFAULT 'api_key';
ALTER TABLE juhe_chat.chat_conversations ADD COLUMN IF NOT EXISTS bind_group_id text;
ALTER TABLE juhe_chat.chat_conversations ADD COLUMN IF NOT EXISTS bind_group_name_snapshot text;
UPDATE juhe_chat.chat_conversations c SET
  bind_mode = COALESCE(b.bind_mode, 'api_key'),
  bind_group_id = b.bind_group_id,
  bind_group_name_snapshot = b.bind_group_name_snapshot
FROM juhe_chat.chat_conversations_bind_legacy_backup b WHERE b.id = c.id;`
}

// isoMillis stamps backup rows and the report (UTC ISO milliseconds).
func isoMillis(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z")
}
