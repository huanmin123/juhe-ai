package runtimelog

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// MigrateLegacySQLite copies the F1 rows out of the old shared dataset file
// while the Go owner lease is held. Node no longer writes these tables, so the
// copy is idempotent and leaves the old file available for rollback comparison.
func MigrateLegacySQLite(ctx context.Context, config Config, store Store) error {
	if config.Mode != ModeSQLite {
		return fmt.Errorf("旧运行日志 SQLite 数据仅能迁移到 sqlite Store")
	}
	if strings.TrimSpace(config.DatasetPath) == "" {
		return fmt.Errorf("旧运行日志 SQLite 迁移缺少 JUHE_AI_DATASET_DATABASE_PATH")
	}
	if sameSQLitePath(config.DatasetPath, config.RuntimeLogDatabasePath) {
		return fmt.Errorf("旧运行日志 SQLite 数据源不得与 JUHE_AI_RUNTIME_LOG_DATABASE_PATH 共用文件")
	}
	if _, err := os.Stat(config.DatasetPath); err != nil {
		return fmt.Errorf("无法访问旧运行日志 SQLite 数据源: %w", err)
	}
	sqlite, ok := store.(*sqliteStore)
	if !ok {
		return fmt.Errorf("旧运行日志 SQLite 迁移需要 sqlite Store，实际为 %T", store)
	}
	lease, err := ownerLeaseFromContext(ctx)
	if err != nil {
		return err
	}
	sqlite.writeMu.Lock()
	defer sqlite.writeMu.Unlock()
	if _, err := sqlite.db.ExecContext(ctx, `ATTACH DATABASE ? AS legacy_runtime_log`, config.DatasetPath); err != nil {
		return fmt.Errorf("附加旧运行日志数据库失败: %w", err)
	}
	attached := true
	defer func() {
		if attached {
			_, _ = sqlite.db.ExecContext(context.Background(), `DETACH DATABASE legacy_runtime_log`)
		}
	}()
	tx, err := sqlite.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := verifySQLiteOwnerLease(ctx, tx, lease); err != nil {
		return err
	}
	if err := verifyLegacySQLiteIntegrity(ctx, tx); err != nil {
		return err
	}
	if err := verifyLegacyTables(ctx, tx); err != nil {
		return err
	}
	copyStatements := []string{
		`INSERT OR IGNORE INTO runtime_logs (id, log_file, log_offset, line_number, time, level, trace_id, event, message, error_message, raw_json, created_at) SELECT id, log_file, log_offset, line_number, time, level, trace_id, event, message, error_message, raw_json, created_at FROM legacy_runtime_log.runtime_logs`,
		`INSERT OR IGNORE INTO runtime_log_file_cursors (log_file, file_identity, cursor_offset, line_number, file_size, truncation_generation, file_mtime_ms, last_read_at, last_error_message, created_at, updated_at) SELECT log_file, file_identity, cursor_offset, line_number, file_size, truncation_generation, file_mtime_ms, last_read_at, last_error_message, created_at, updated_at FROM legacy_runtime_log.runtime_log_file_cursors`,
		`INSERT OR IGNORE INTO runtime_log_facet_summary (bucket_key, total_count, earliest_time, latest_time, updated_at) SELECT bucket_key, total_count, earliest_time, latest_time, updated_at FROM legacy_runtime_log.runtime_log_facet_summary`,
		`INSERT OR IGNORE INTO runtime_log_level_facets (bucket_key, level, count, updated_at) SELECT bucket_key, level, count, updated_at FROM legacy_runtime_log.runtime_log_level_facets`,
		`INSERT OR IGNORE INTO runtime_log_event_facets (bucket_key, event, count, latest_time, updated_at) SELECT bucket_key, event, count, latest_time, updated_at FROM legacy_runtime_log.runtime_log_event_facets`,
	}
	for _, statement := range copyStatements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("复制旧运行日志 SQLite 数据失败: %w", err)
		}
	}
	if err := verifyLegacyMigration(ctx, tx); err != nil {
		return err
	}
	if err := verifySQLiteIntegrity(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if _, err := sqlite.db.ExecContext(ctx, `DETACH DATABASE legacy_runtime_log`); err != nil {
		return fmt.Errorf("分离旧运行日志数据库失败: %w", err)
	}
	attached = false
	return nil
}

func verifyLegacyMigration(ctx context.Context, tx *sql.Tx) error {
	checks := []struct {
		name      string
		table     string
		join      string
		targetKey string
		matches   string
	}{
		{name: "运行日志记录", table: "runtime_logs", join: "target.id = source.id", targetKey: "target.id", matches: "target.log_file IS source.log_file AND target.log_offset IS source.log_offset AND target.line_number IS source.line_number AND target.time IS source.time AND target.level IS source.level AND target.trace_id IS source.trace_id AND target.event IS source.event AND target.message IS source.message AND target.error_message IS source.error_message AND target.raw_json IS source.raw_json AND target.created_at IS source.created_at"},
		{name: "文件游标", table: "runtime_log_file_cursors", join: "target.log_file = source.log_file", targetKey: "target.log_file", matches: "target.file_identity IS source.file_identity AND target.cursor_offset IS source.cursor_offset AND target.line_number IS source.line_number AND target.file_size IS source.file_size AND target.truncation_generation IS source.truncation_generation AND target.file_mtime_ms IS source.file_mtime_ms AND target.last_read_at IS source.last_read_at AND target.last_error_message IS source.last_error_message AND target.created_at IS source.created_at AND target.updated_at IS source.updated_at"},
		{name: "聚合摘要", table: "runtime_log_facet_summary", join: "target.bucket_key = source.bucket_key", targetKey: "target.bucket_key", matches: "target.total_count IS source.total_count AND target.earliest_time IS source.earliest_time AND target.latest_time IS source.latest_time AND target.updated_at IS source.updated_at"},
		{name: "级别聚合", table: "runtime_log_level_facets", join: "target.bucket_key = source.bucket_key AND target.level = source.level", targetKey: "target.level", matches: "target.count IS source.count AND target.updated_at IS source.updated_at"},
		{name: "事件聚合", table: "runtime_log_event_facets", join: "target.bucket_key = source.bucket_key AND target.event = source.event", targetKey: "target.event", matches: "target.count IS source.count AND target.latest_time IS source.latest_time AND target.updated_at IS source.updated_at"},
	}
	for _, check := range checks {
		var sourceCount int64
		var targetCount int64
		countQuery := fmt.Sprintf("SELECT (SELECT COUNT(*) FROM legacy_runtime_log.%s), (SELECT COUNT(*) FROM %s)", check.table, check.table)
		if err := tx.QueryRowContext(ctx, countQuery).Scan(&sourceCount, &targetCount); err != nil {
			return fmt.Errorf("校验旧运行日志 %s 数量失败: %w", check.name, err)
		}
		if targetCount < sourceCount {
			return fmt.Errorf("旧运行日志 %s 迁移后数量不足: source=%d target=%d", check.name, sourceCount, targetCount)
		}
		missingQuery := fmt.Sprintf("SELECT COUNT(*) FROM legacy_runtime_log.%s source LEFT JOIN %s target ON %s WHERE %s IS NULL", check.table, check.table, check.join, check.targetKey)
		var missing int64
		if err := tx.QueryRowContext(ctx, missingQuery).Scan(&missing); err != nil {
			return fmt.Errorf("校验旧运行日志 %s 主键失败: %w", check.name, err)
		}
		if missing != 0 {
			return fmt.Errorf("旧运行日志 %s 存在 %d 条未迁移记录", check.name, missing)
		}
		mismatchQuery := fmt.Sprintf("SELECT COUNT(*) FROM legacy_runtime_log.%s source JOIN %s target ON %s WHERE NOT (%s)", check.table, check.table, check.join, check.matches)
		var mismatched int64
		if err := tx.QueryRowContext(ctx, mismatchQuery).Scan(&mismatched); err != nil {
			return fmt.Errorf("校验旧运行日志 %s 值失败: %w", check.name, err)
		}
		if mismatched != 0 {
			return fmt.Errorf("旧运行日志 %s 存在 %d 条字段值不一致记录", check.name, mismatched)
		}
	}
	return nil
}

func verifyLegacyTables(ctx context.Context, tx *sql.Tx) error {
	for _, table := range []string{"runtime_logs", "runtime_log_file_cursors", "runtime_log_facet_summary", "runtime_log_level_facets", "runtime_log_event_facets"} {
		var found string
		if err := tx.QueryRowContext(ctx, "SELECT name FROM legacy_runtime_log.sqlite_master WHERE type = 'table' AND name = ?", table).Scan(&found); err != nil {
			return fmt.Errorf("旧运行日志 SQLite 数据源缺少表 %s: %w", table, err)
		}
	}
	return nil
}

func verifyLegacySQLiteIntegrity(ctx context.Context, tx *sql.Tx) error {
	var result string
	if err := tx.QueryRowContext(ctx, "PRAGMA legacy_runtime_log.integrity_check").Scan(&result); err != nil {
		return fmt.Errorf("校验旧运行日志 SQLite 完整性失败: %w", err)
	}
	if result != "ok" {
		return fmt.Errorf("旧运行日志 SQLite 完整性校验失败: %s", result)
	}
	return nil
}

func verifySQLiteIntegrity(ctx context.Context, tx *sql.Tx) error {
	var result string
	if err := tx.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&result); err != nil {
		return fmt.Errorf("校验运行日志 SQLite 完整性失败: %w", err)
	}
	if result != "ok" {
		return fmt.Errorf("运行日志 SQLite 完整性校验失败: %s", result)
	}
	return nil
}

func sameSQLitePath(left, right string) bool {
	if strings.TrimSpace(left) == "" || strings.TrimSpace(right) == "" {
		return false
	}
	leftAbs, leftErr := canonicalSQLitePath(left)
	rightAbs, rightErr := canonicalSQLitePath(right)
	if leftErr != nil || rightErr != nil {
		return false
	}
	if strings.EqualFold(filepath.Clean(leftAbs), filepath.Clean(rightAbs)) {
		return true
	}
	leftInfo, leftErr := os.Stat(left)
	rightInfo, rightErr := os.Stat(right)
	return leftErr == nil && rightErr == nil && os.SameFile(leftInfo, rightInfo)
}

func sqlitePathWithin(root, candidate string) bool {
	rootPath, rootErr := canonicalSQLitePath(root)
	candidatePath, candidateErr := canonicalSQLitePath(candidate)
	if rootErr != nil || candidateErr != nil {
		return false
	}
	relative, err := filepath.Rel(rootPath, candidatePath)
	if err != nil {
		return false
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}

func canonicalSQLitePath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	abs = filepath.Clean(abs)
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return filepath.Clean(resolved), nil
	}
	parent := filepath.Dir(abs)
	suffix := []string{filepath.Base(abs)}
	for parent != filepath.Dir(parent) {
		if resolved, err := filepath.EvalSymlinks(parent); err == nil {
			return filepath.Join(append([]string{resolved}, suffix...)...), nil
		}
		suffix = append([]string{filepath.Base(parent)}, suffix...)
		parent = filepath.Dir(parent)
	}
	return abs, nil
}
