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
//   - every step is idempotent: a second run finds no legacy columns and
//     reports alreadyMigrated without writing.
//
// The CLI entry (cmd/juhe-ai-maintenance --migrate-chat-account-only-binding)
// takes --dsn URL and prints the rollback SQL next to the JSON report.
// SQLite 目标随 SQLite 存储形态退役删除
// （PLAN-20261024T051925945Z B4，2026-10-24 用户裁决）。
package chatbindingmigration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// SchemaName is the PostgreSQL schema owning chat_conversations.
const SchemaName = "juhe_chat"

// BackupTable is the side table holding the dropped legacy column data.
const BackupTable = "chat_conversations_bind_legacy_backup"

// legacyColumns are the dropped bind-mode trio, in rollback ADD COLUMN order.
var legacyColumns = []string{"bind_mode", "bind_group_id", "bind_group_name_snapshot"}

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
// step is independently idempotent and a rerun converges.
func Run(ctx context.Context, db *sql.DB, now time.Time) (Report, error) {
	report := Report{
		Driver:      "postgres",
		BackupTable: BackupTable,
		MigratedAt:  isoMillis(now),
	}
	if err := runPostgres(ctx, db, &report, now); err != nil {
		return report, err
	}
	remaining, err := detectLegacyColumns(ctx, db)
	if err != nil {
		return report, err
	}
	report.RemainingLegacyColumns = remaining
	report.AlreadyMigrated = len(remaining) == 0 && !report.RebuiltTable && report.DroppedColumns == nil
	if err := countArchived(ctx, db, &report); err != nil {
		return report, err
	}
	report.RollbackSQL = rollbackSQL()
	return report, nil
}

// ensureColumns adds the account-only binding columns when missing. The
// guarded ADD COLUMN list mirrors the schema ensure (bind_account_* covers
// databases predating the bind-mode feature entirely).
func ensureColumns(ctx context.Context, db *sql.DB) error {
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
	if err := ensureColumns(ctx, db); err != nil {
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
		if err := backupLegacyColumns(ctx, db, now); err != nil {
			return err
		}
		if err := countBackupRows(ctx, db, report); err != nil {
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

// backupLegacyColumns creates/refreshes the backup side table with the
// legacy column data of every conversation row (INSERT ... ON CONFLICT keeps
// a rerun idempotent while refreshing values).
func backupLegacyColumns(ctx context.Context, db *sql.DB, now time.Time) error {
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

func countBackupRows(ctx context.Context, db *sql.DB, report *Report) error {
	exists, err := pgTableExists(ctx, db, BackupTable)
	if err != nil || !exists {
		return err
	}
	query := "SELECT COUNT(*) FROM " + SchemaName + "." + BackupTable
	var count int64
	if err := db.QueryRowContext(ctx, query).Scan(&count); err != nil {
		return err
	}
	report.BackedUpRows = count
	return nil
}

func countArchived(ctx context.Context, db *sql.DB, report *Report) error {
	query := "SELECT COUNT(*) FROM " + SchemaName + ".chat_conversations WHERE archived = 1"
	var count int64
	if err := db.QueryRowContext(ctx, query).Scan(&count); err != nil {
		return err
	}
	report.ArchivedRowsTotal = count
	return nil
}

func detectLegacyColumns(ctx context.Context, db *sql.DB) ([]string, error) {
	remaining := []string{}
	for _, column := range legacyColumns {
		exists, err := pgColumnExists(ctx, db, column)
		if err != nil {
			return nil, err
		}
		if exists {
			remaining = append(remaining, column)
		}
	}
	return remaining, nil
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

// rollbackSQL returns the rollback recipe (design §10 回滚窗口)：代码回滚后
// 从备份副表重建三列并回填存量值。
func rollbackSQL() string {
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
