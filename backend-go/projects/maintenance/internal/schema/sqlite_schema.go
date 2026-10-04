// Code generated from the Node storage schema sources listed below. The DDL
// statements are byte-for-byte ports of the corresponding Node
// database.exec template literals, executed in the Node call order. Do not
// hand-edit the DDL constants; regenerate or re-verify against the Node
// sources when they change.
//
// Node sources (juhe-ai backend/src/storage/schema):
//   - business-schema.ts             applyBusinessSchema
//   - stats-schema.ts                applyStatsSchema
//   - chat-schema.ts                 applyChatSchema
//   - codex-context-state-schema.ts  applyCodexContextStateSchema
//   - dataset-schema.ts              applyDatasetSchema
//   - usage-catalog-schema.ts        applyUsageCatalogSchema
//
// Porting rules:
//   - PRAGMA foreign_keys = ON is kept in the same position as in Node. Note
//     that it is a per-connection setting: callers that need foreign key
//     enforcement across a connection pool must configure it per connection
//     (for example by capping the pool to one connection).
//   - PRAGMA journal_mode = WAL is intentionally skipped; journal
//     configuration belongs to the caller and is meaningless for in-memory
//     databases.
//   - The stats source keeps the J3b model-integrity DDL inside a SQL block
//     comment ("J3b ownership moved to Gateway; its schemas must not be
//     created by Node"). The comment is preserved verbatim, those statements
//     are not executed and are excluded from SchemaCounts.
//   - Node-only conditional runtime migrations are NOT ported. They are
//     no-ops on a database created from the DDL below (guarded by
//     PRAGMA table_info / sqlite_master lookups) and require human review
//     before legacy-upgrade support is added to Go. BUG-0167/0168 review
//     verdict (2026-09-04, verified against business-schema.ts): every guard
//     is a legacy-upgrade path that exits before writing on a fresh database,
//     so porting stays unnecessary for the fresh dual-mode acceptance:
//       * ensureAccountHealthCheckEndpointModeSchema (business-schema.ts):
//         full accounts table rebuild only when the sqlite_master CHECK
//         constraint lacks 'images_json'; the DDL below already declares
//         images_json inside the health_check_endpoint_mode CHECK, so the
//         guard returns before any write.
//       * ensureSystemAccountRequestLimitsSchema,
//         ensureSystemAccountAiAccountLimitSchema,
//         ensureAccountTestTaskQueuedDeadlineSchema: ALTER TABLE guards that
//         return early when PRAGMA table_info shows the column (or an empty
//         table); request_limits_json / ai_account_limit / queued_deadline_at
//         all exist in the CREATE TABLE statements below.
//       * ensureAccountCircuitControlPlaneSchema: conditional ALTER TABLE ADD
//         COLUMN guards that run only when account_circuit_incidents exists
//         without the confirmation columns (the DDL below creates them);
//         its unconditional CREATE UNIQUE INDEX is ported below.

package schema

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// sqliteScript is an ordered list of statements (DDL blocks and pragmas)
// mirroring one Node apply*Schema function.
type sqliteScript []string

// sqliteBusinessScript mirrors applyBusinessSchema in business-schema.ts.
var sqliteBusinessScript = sqliteScript{
	"PRAGMA foreign_keys = ON;",
	sqliteBusinessMainDDL,
	sqliteBusinessCircuitControlPlaneIndexDDL,
	sqliteBusinessResponseInspectionPolicyIndexesDDL,
	sqliteBusinessExternalIntegrationSourceIndexesDDL,
	sqliteBusinessAuthorizationInstanceIndexesDDL,
}

// sqliteStatsScript mirrors applyStatsSchema in stats-schema.ts.
var sqliteStatsScript = sqliteScript{
	sqliteStatsDirtyIPsDDL,
	"PRAGMA foreign_keys = ON;",
	sqliteStatsMainDDL,
	sqliteStatsUsageCleanupDeductionsIndexDDL,
}

// sqliteChatScript mirrors applyChatSchema in chat-schema.ts.
var sqliteChatScript = sqliteScript{
	"PRAGMA foreign_keys = ON;",
	sqliteChatDDL,
}

// sqliteCodexContextScript mirrors applyCodexContextStateSchema in codex-context-state-schema.ts.
var sqliteCodexContextScript = sqliteScript{
	sqliteCodexContextDDL,
}

// sqliteDatasetScript mirrors applyDatasetSchema in dataset-schema.ts.
var sqliteDatasetScript = sqliteScript{
	"PRAGMA foreign_keys = ON;",
	sqliteDatasetDDL,
}

// sqliteUsageCatalogScript mirrors applyUsageCatalogSchema in usage-catalog-schema.ts.
var sqliteUsageCatalogScript = sqliteScript{
	"PRAGMA foreign_keys = ON;",
	sqliteUsageCatalogDDL,
}

// counts reports how many CREATE TABLE and CREATE INDEX statements the script
// ensures. On a fresh database this equals the number of tables and indexes
// created. SQL comments are stripped before counting so statements that the
// Node source keeps inside block comments are excluded.
func (s sqliteScript) counts() SchemaCounts {
	var counts SchemaCounts
	for _, part := range s {
		tables, indexes := countDDLStatements(part)
		counts.Tables += tables
		counts.Indexes += indexes
	}
	return counts
}

var (
	sqliteSQLBlockComment = regexp.MustCompile("(?s)/\\*.*?\\*/")
	sqliteSQLLineComment  = regexp.MustCompile("--[^\\n]*")
)

// countDDLStatements counts CREATE TABLE and CREATE INDEX statements in one
// DDL chunk, ignoring statements that only exist inside SQL comments.
func countDDLStatements(part string) (tables, indexes int) {
	stripped := sqliteSQLLineComment.ReplaceAllString(sqliteSQLBlockComment.ReplaceAllString(part, ""), "")
	tables = strings.Count(stripped, "CREATE TABLE ")
	indexes = strings.Count(stripped, "CREATE INDEX ") + strings.Count(stripped, "CREATE UNIQUE INDEX ")
	return tables, indexes
}

// ensure executes the script statements in order and returns their counts.
func (s sqliteScript) ensure(ctx context.Context, db *sql.DB) (SchemaCounts, error) {
	for i, part := range s {
		if _, err := db.ExecContext(ctx, part); err != nil {
			return SchemaCounts{}, fmt.Errorf("sqlite schema statement %d: %w", i, err)
		}
	}
	return s.counts(), nil
}

// SchemaCounts reports how many CREATE TABLE and CREATE INDEX statements were
// ensured for one schema. On a fresh database this equals the number of
// tables and indexes created.
type SchemaCounts struct {
	Tables  int
	Indexes int
}

// SQLiteResult summarizes EnsureAllSQLite: the DDL statement counts applied
// for every SQLite schema.
type SQLiteResult struct {
	Business     SchemaCounts
	Stats        SchemaCounts
	Chat         SchemaCounts
	CodexContext SchemaCounts
	Dataset      SchemaCounts
	UsageCatalog SchemaCounts
}

// EnsureSQLiteBusiness applies the business schema (system accounts, providers, accounts, groups, routing, API keys, authorizations, circuit control plane).
func EnsureSQLiteBusiness(ctx context.Context, db *sql.DB) (SchemaCounts, error) {
	counts, err := sqliteBusinessScript.ensure(ctx, db)
	if err != nil {
		return SchemaCounts{}, err
	}
	if err := ensureSQLiteBusinessCustomQuestionColumns(ctx, db); err != nil {
		return SchemaCounts{}, fmt.Errorf("ensure sqlite business custom_question_ids columns: %w", err)
	}
	if err := ensureSQLiteBusinessScheduleIntervalCheck(ctx, db); err != nil {
		return SchemaCounts{}, fmt.Errorf("ensure sqlite business schedule interval check: %w", err)
	}
	return counts, nil
}

// ensureSQLiteBusinessCustomQuestionColumns is the Go port of the Node
// conditional ALTER guard pattern documented at the top of this file
// (business-schema.ts ensure*Schema): PRAGMA table_info decides whether the
// two model-quality tables still need the custom_question_ids column. Fresh
// databases declare the column inside the CREATE TABLE statements, so the
// guard exits before writing there; legacy databases created before the
// model-check question bank receive an in-place ADD COLUMN. The migration is
// additive and never creates tables or indexes, so SchemaCounts is stable
// across fresh, legacy and repeated runs.
func ensureSQLiteBusinessCustomQuestionColumns(ctx context.Context, db *sql.DB) error {
	for _, table := range []string{"model_quality_policies", "model_quality_schedules"} {
		if err := ensureSQLiteTableColumn(ctx, db, table, "custom_question_ids", "TEXT"); err != nil {
			return fmt.Errorf("ensure %s.custom_question_ids: %w", table, err)
		}
	}
	return nil
}

// sqliteScheduleIntervalLegacyCheckText 是 model_quality_schedules 旧建表
// DDL 中 interval_minutes 列级 CHECK 的原文片段。必须带左括号前缀：
// recovery_interval_minutes 的 CHECK 同样含 "BETWEEN 10 AND 10080"，但列名
// 不同，不会被 "(interval_minutes BETWEEN 10 AND 10080)" 命中。
const sqliteScheduleIntervalLegacyCheckText = "(interval_minutes BETWEEN 10 AND 10080)"

// sqliteScheduleRebuildColumns 是重建后 model_quality_schedules 的完整列清单
// （顺序与 sqlite_schema_business.go 的建表 DDL 一致）。复制阶段按旧表实际
// 列求交集，兼容 custom_question_ids 尚未由上方守卫补齐的更老形状。
var sqliteScheduleRebuildColumns = []string{
	"id", "system_account_id", "account_id", "model", "interval_minutes",
	"profile", "penalty_threshold", "penalty_action", "recovery_interval_minutes",
	"enabled", "revision", "next_run_at", "last_run_id", "last_run_at",
	"last_run_status", "lease_owner", "lease_until", "created_at", "updated_at",
	"custom_question_ids",
}

// sqliteScheduleRebuildIndexes 在 rename 后按 business 脚本的索引定义重建
// model_quality_schedules 的两个索引（DROP TABLE 会连带删除旧表索引）。
var sqliteScheduleRebuildIndexes = []string{
	`CREATE INDEX IF NOT EXISTS idx_model_quality_schedules_due
      ON model_quality_schedules(enabled, next_run_at, id)`,
	`CREATE INDEX IF NOT EXISTS idx_model_quality_schedules_scope
      ON model_quality_schedules(system_account_id, created_at DESC, id DESC)`,
}

// sqliteScheduleRebuildDDL is the post-migration model_quality_schedules shape
// (mirrors the updated business schema DDL; the only change is the widened
// interval_minutes CHECK lower bound from 10 to 1 minute). Column and index
// parity is asserted by the schema tests so the two definitions cannot drift.
const sqliteScheduleRebuildDDL = `CREATE TABLE model_quality_schedules_migrating (
      id TEXT PRIMARY KEY,
      system_account_id TEXT NOT NULL,
      account_id TEXT NOT NULL,
      model TEXT NOT NULL,
      interval_minutes INTEGER NOT NULL DEFAULT 60 CHECK (interval_minutes BETWEEN 1 AND 10080),
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
      custom_question_ids TEXT,
      FOREIGN KEY (system_account_id) REFERENCES system_accounts(id) ON DELETE CASCADE,
      FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE,
      UNIQUE (system_account_id, account_id)
    )`

// ensureSQLiteBusinessScheduleIntervalCheck delivers the widened
// model_quality_schedules.interval_minutes CHECK (lower bound 10 -> 1 minute,
// schedule_now contract) to legacy SQLite databases. SQLite cannot DROP a
// column CHECK, so the table is rebuilt inside one transaction following the
// chatbindingmigration precedent (staging table -> copy -> drop -> rename ->
// recreate indexes), with PRAGMA foreign_keys toggled off and restored around
// the rebuild exactly like that migration. Fresh databases (new CREATE TABLE
// text) and already-migrated tables exit before writing; no other table
// references model_quality_schedules so the drop/rename stays dependency-free.
func ensureSQLiteBusinessScheduleIntervalCheck(ctx context.Context, db *sql.DB) error {
	var ddl sql.NullString
	err := db.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name='model_quality_schedules'`).Scan(&ddl)
	if errors.Is(err, sql.ErrNoRows) {
		// 表不存在：由同一次 ensure 的建表阶段负责，无需迁移。
		return nil
	}
	if err != nil {
		return err
	}
	if !ddl.Valid || !strings.Contains(ddl.String, sqliteScheduleIntervalLegacyCheckText) {
		// 已是新约束（或非预期形状）：不重建。
		return nil
	}
	existing, err := sqliteTableColumnSet(ctx, db, "model_quality_schedules")
	if err != nil {
		return err
	}
	copyColumns := make([]string, 0, len(sqliteScheduleRebuildColumns))
	for _, column := range sqliteScheduleRebuildColumns {
		if existing[column] {
			copyColumns = append(copyColumns, column)
		}
	}
	if len(copyColumns) == 0 {
		return nil
	}
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
	if _, err := db.ExecContext(ctx, "DROP TABLE IF EXISTS model_quality_schedules_migrating"); err != nil {
		return fmt.Errorf("清理上次重建残留失败: %w", err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, sqliteScheduleRebuildDDL); err != nil {
		return fmt.Errorf("创建 model_quality_schedules 重建表失败: %w", err)
	}
	columnList := strings.Join(copyColumns, ", ")
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(
		"INSERT INTO model_quality_schedules_migrating (%s)\nSELECT %s FROM model_quality_schedules",
		columnList, columnList)); err != nil {
		return fmt.Errorf("回填 model_quality_schedules 重建表数据失败: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "DROP TABLE model_quality_schedules"); err != nil {
		return fmt.Errorf("删除旧 model_quality_schedules 失败: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "ALTER TABLE model_quality_schedules_migrating RENAME TO model_quality_schedules"); err != nil {
		return fmt.Errorf("重命名 model_quality_schedules 重建表失败: %w", err)
	}
	for _, statement := range sqliteScheduleRebuildIndexes {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("重建 model_quality_schedules 索引失败: %w", err)
		}
	}
	return tx.Commit()
}

// sqliteTableColumnSet returns the set of column names the table currently
// declares. A missing table returns an empty set (callers decide whether that
// is meaningful).
func sqliteTableColumnSet(ctx context.Context, db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns := map[string]bool{}
	for rows.Next() {
		var cid, notNull, pk int
		var name, declaredType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &declaredType, &notNull, &defaultValue, &pk); err != nil {
			return nil, err
		}
		columns[name] = true
	}
	return columns, rows.Err()
}

// ensureSQLiteTableColumn adds "<column> <decl>" to table when the table
// exists without the column. A missing table is not an error: the CREATE
// TABLE phase of the same ensure run creates it with the column already
// declared. Table and column names are compile-time constants, so inline
// interpolation is safe.
func ensureSQLiteTableColumn(ctx context.Context, db *sql.DB, table, column, decl string) error {
	rows, err := db.QueryContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		return err
	}
	tableExists, columnExists := false, false
	for rows.Next() {
		var cid, notNull, pk int
		var name, declaredType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &declaredType, &notNull, &defaultValue, &pk); err != nil {
			rows.Close()
			return err
		}
		tableExists = true
		if name == column {
			columnExists = true
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if !tableExists || columnExists {
		return nil
	}
	_, err = db.ExecContext(ctx, "ALTER TABLE "+table+" ADD COLUMN "+column+" "+decl)
	return err
}

// EnsureSQLiteStats applies the stats schema (quality/usage aggregations and process samples).
func EnsureSQLiteStats(ctx context.Context, db *sql.DB) (SchemaCounts, error) {
	counts, err := sqliteStatsScript.ensure(ctx, db)
	if err != nil {
		return SchemaCounts{}, err
	}
	if err := ensureSQLiteStatsSuccessCostColumns(ctx, db); err != nil {
		return SchemaCounts{}, fmt.Errorf("ensure sqlite stats success_cost_usd columns: %w", err)
	}
	if err := ensureSQLiteStatsMediaColumns(ctx, db); err != nil {
		return SchemaCounts{}, fmt.Errorf("ensure sqlite stats media metering columns: %w", err)
	}
	return counts, nil
}

// sqliteStatsMediaColumnTables 列出承载媒体计量维度（M4a）的 stats 聚合表：
// usage_stats 六层、usage_model 五层与 usage_scope_range_windows 范围窗口。
// 概览窗口族、AI 性能摘要、授权日报族、IP 高基数表族与错误/直方图计数表
// 不承载媒体维度，不在守卫清单内。
var sqliteStatsMediaColumnTables = []string{
	"usage_stats_totals",
	"usage_stats_minute",
	"usage_stats_hourly",
	"usage_stats_daily",
	"usage_stats_weekly",
	"usage_stats_monthly",
	"usage_model_minute",
	"usage_model_hourly",
	"usage_model_daily",
	"usage_model_weekly",
	"usage_model_monthly",
	"usage_scope_range_windows",
}

// sqliteStatsMediaColumnDecls 是五个媒体计量列在既有库上的幂等补齐声明，
// 列序与基线 CREATE TABLE 一致（output_image_tokens 之后）。
var sqliteStatsMediaColumnDecls = []struct {
	column string
	decl   string
}{
	{"input_audio_tokens", "INTEGER NOT NULL DEFAULT 0"},
	{"output_audio_tokens", "INTEGER NOT NULL DEFAULT 0"},
	{"tts_input_chars", "INTEGER NOT NULL DEFAULT 0"},
	{"audio_input_seconds", "REAL NOT NULL DEFAULT 0"},
	{"output_video_seconds", "REAL NOT NULL DEFAULT 0"},
}

// ensureSQLiteStatsMediaColumns delivers the media metering columns
// (input_audio_tokens / output_audio_tokens / tts_input_chars /
// audio_input_seconds / output_video_seconds，M4a) to legacy databases
// through the same guarded PRAGMA table_info / ALTER TABLE ADD COLUMN
// pattern as the success_cost_usd guard: fresh databases declare them
// inside the CREATE TABLE statements, legacy databases receive the
// in-place ADD COLUMN. No backfill: historical aggregate rows keep 0 and
// accumulate from new usage records (统计体系按当前口径从新请求起累计).
func ensureSQLiteStatsMediaColumns(ctx context.Context, db *sql.DB) error {
	for _, table := range sqliteStatsMediaColumnTables {
		for _, target := range sqliteStatsMediaColumnDecls {
			if err := ensureSQLiteTableColumn(ctx, db, table, target.column, target.decl); err != nil {
				return fmt.Errorf("ensure %s.%s: %w", table, target.column, err)
			}
		}
	}
	return nil
}

// sqliteStatsSuccessCostGuardTables 列出需要补 success_cost_usd 列的 stats
// usage 投影表（守卫与 w9a 错误注入偏移共用，改动时测试自动跟随）。
var sqliteStatsSuccessCostGuardTables = []string{
	"usage_stats_totals",
	"usage_stats_minute",
	"usage_stats_hourly",
	"usage_stats_daily",
	"usage_stats_weekly",
	"usage_stats_monthly",
}

// ensureSQLiteStatsSuccessCostColumns delivers the stats usage projection
// success_cost_usd column (quota/billing counts only successfully delivered
// attempts) to legacy databases through the same guarded PRAGMA table_info /
// ALTER TABLE ADD COLUMN pattern as the business schema: fresh databases
// declare it inside the CREATE TABLE statements, legacy databases receive the
// in-place ADD COLUMN. The follow-up backfill copies total_cost_usd into
// success_cost_usd only for rows with error_count = 0 (failure-free rows, so
// total is exactly the success cost); mixed rows cannot be decomposed from the
// aggregate and start at 0 per the business rule that failed attempts do not
// count toward quota. The WHERE guards keep the backfill idempotent across
// repeated ensure runs and no-ops for post-cutover accumulations.
func ensureSQLiteStatsSuccessCostColumns(ctx context.Context, db *sql.DB) error {
	for _, table := range sqliteStatsSuccessCostGuardTables {
		if err := ensureSQLiteTableColumn(ctx, db, table, "success_cost_usd", "REAL NOT NULL DEFAULT 0"); err != nil {
			return fmt.Errorf("ensure %s.success_cost_usd: %w", table, err)
		}
		backfill := "UPDATE " + table + " SET success_cost_usd = total_cost_usd WHERE error_count = 0 AND success_cost_usd <> total_cost_usd"
		if _, err := db.ExecContext(ctx, backfill); err != nil {
			return fmt.Errorf("backfill %s.success_cost_usd: %w", table, err)
		}
	}
	return nil
}

// EnsureSQLiteChat applies the chat schema (conversations, messages, assets, context checkpoints).
func EnsureSQLiteChat(ctx context.Context, db *sql.DB) (SchemaCounts, error) {
	counts, err := sqliteChatScript.ensure(ctx, db)
	if err != nil {
		return SchemaCounts{}, err
	}
	if err := ensureSQLiteChatAccountBindingColumns(ctx, db); err != nil {
		return SchemaCounts{}, fmt.Errorf("ensure sqlite chat account-binding columns: %w", err)
	}
	if err := ensureSQLiteChatMediaToolColumns(ctx, db); err != nil {
		return SchemaCounts{}, fmt.Errorf("ensure sqlite chat media-tool columns: %w", err)
	}
	if err := ensureSQLiteChatAssetsMediaCheck(ctx, db); err != nil {
		return SchemaCounts{}, fmt.Errorf("ensure sqlite chat_assets media check: %w", err)
	}
	return counts, nil
}

// sqliteChatMediaToolColumns 列出 M7 问答音视频工具（问答音视频工具设计 §3，
// 2026-10-04）在既有库上的幂等补齐声明：chat_conversations 与
// chat_user_tool_preferences 各四列（video_account_id/default_video_model/
// audio_account_id/default_audio_model）。新库由 sqliteChatDDL 直接声明这些
// 列，守卫先查列存在再 ALTER。
var sqliteChatMediaToolColumns = []struct {
	table  string
	column string
	decl   string
}{
	{"chat_conversations", "video_account_id", "TEXT"},
	{"chat_conversations", "default_video_model", "TEXT"},
	{"chat_conversations", "audio_account_id", "TEXT"},
	{"chat_conversations", "default_audio_model", "TEXT"},
	{"chat_user_tool_preferences", "video_account_id", "TEXT"},
	{"chat_user_tool_preferences", "default_video_model", "TEXT"},
	{"chat_user_tool_preferences", "audio_account_id", "TEXT"},
	{"chat_user_tool_preferences", "default_audio_model", "TEXT"},
}

// ensureSQLiteChatMediaToolColumns delivers the M7 media tool binding columns
// to legacy databases through the same guarded PRAGMA table_info / ALTER TABLE
// ADD COLUMN pattern as the account-binding columns.
func ensureSQLiteChatMediaToolColumns(ctx context.Context, db *sql.DB) error {
	for _, target := range sqliteChatMediaToolColumns {
		if err := ensureSQLiteTableColumn(ctx, db, target.table, target.column, target.decl); err != nil {
			return fmt.Errorf("ensure %s.%s: %w", target.table, target.column, err)
		}
	}
	return nil
}

// sqliteChatAssetsLegacyMimeCheckText 是旧建表 DDL 中 processed_mime_type 词表
// CHECK 的精确判据片段：旧词表以 'image/webp' 收尾，新词表其后还有媒体 MIME
// 条目（'image/webp',），不会命中本片段。
const sqliteChatAssetsLegacyMimeCheckText = "'image/webp')"

// sqliteChatAssetsRebuildColumns 是重建后 chat_assets 的完整列清单（顺序与
// sqliteChatDDL 的建表 DDL 一致；M7 未新增列，仅放宽约束）。
var sqliteChatAssetsRebuildColumns = []string{
	"id", "system_account_id", "conversation_id", "source_kind", "original_filename",
	"original_mime_type", "original_width", "original_height", "original_bytes", "original_sha256",
	"processed_mime_type", "processed_width", "processed_height", "processed_bytes", "processed_sha256",
	"storage_key", "preview_mime_type", "preview_width", "preview_height", "preview_bytes",
	"preview_sha256", "preview_storage_key", "processing_status", "processing_error_code",
	"observation_status", "observation_json", "observation_revision", "observation_claim_id",
	"observation_claimed_at", "quota_bytes", "turn_id", "message_id", "committed_at",
	"cleanup_status", "cleanup_claim_id", "cleanup_attempt_count", "cleanup_claimed_at",
	"cleanup_retry_at", "cleanup_error_code", "created_at", "updated_at", "expires_at",
}

// sqliteChatAssetsRebuildIndexes 在 rename 后按 chat 脚本的索引定义重建
// chat_assets 的五个索引（DROP TABLE 会连带删除旧表索引）。
var sqliteChatAssetsRebuildIndexes = []string{
	`CREATE INDEX IF NOT EXISTS idx_chat_assets_owner_conversation
      ON chat_assets(system_account_id, conversation_id, created_at DESC, id DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_chat_assets_owner_lookup
      ON chat_assets(system_account_id, id, conversation_id)`,
	`CREATE INDEX IF NOT EXISTS idx_chat_assets_message
      ON chat_assets(conversation_id, turn_id, message_id, id)`,
	`CREATE INDEX IF NOT EXISTS idx_chat_assets_uncommitted
      ON chat_assets(system_account_id, conversation_id, expires_at, id)
      WHERE turn_id IS NULL AND message_id IS NULL
        AND processing_status IN ('pending', 'ready') AND cleanup_status = 'active'`,
	`CREATE INDEX IF NOT EXISTS idx_chat_assets_cleanup
      ON chat_assets(cleanup_status, cleanup_retry_at, expires_at, id)`,
}

// sqliteChatAssetsRebuildDDL is the post-migration chat_assets shape (mirrors
// the updated chat schema DDL; the only change is the widened media constraints:
// processed_mime_type 词表增媒体 MIME、assistant_generated 预览必备对媒体行豁免、
// ready 形状的宽高必备对媒体行豁免)。Column and index parity is asserted by the
// schema tests so the two definitions cannot drift.
const sqliteChatAssetsRebuildDDL = `CREATE TABLE chat_assets_migrating (
      id TEXT PRIMARY KEY,
      system_account_id TEXT NOT NULL,
      conversation_id TEXT NOT NULL,
      source_kind TEXT NOT NULL DEFAULT 'user_upload',
      original_filename TEXT NOT NULL,
      original_mime_type TEXT NOT NULL,
      original_width INTEGER,
      original_height INTEGER,
      original_bytes INTEGER NOT NULL,
      original_sha256 TEXT NOT NULL,
      processed_mime_type TEXT,
      processed_width INTEGER,
      processed_height INTEGER,
      processed_bytes INTEGER,
      processed_sha256 TEXT,
      storage_key TEXT,
      preview_mime_type TEXT,
      preview_width INTEGER,
      preview_height INTEGER,
      preview_bytes INTEGER,
      preview_sha256 TEXT,
      preview_storage_key TEXT,
      processing_status TEXT NOT NULL DEFAULT 'pending',
      processing_error_code TEXT,
      observation_status TEXT NOT NULL DEFAULT 'not_requested',
      observation_json TEXT,
      observation_revision INTEGER NOT NULL DEFAULT 0,
      observation_claim_id TEXT,
      observation_claimed_at TEXT,
      quota_bytes INTEGER NOT NULL,
      turn_id TEXT,
      message_id TEXT,
      committed_at TEXT,
      cleanup_status TEXT NOT NULL DEFAULT 'active',
      cleanup_claim_id TEXT,
      cleanup_attempt_count INTEGER NOT NULL DEFAULT 0,
      cleanup_claimed_at TEXT,
      cleanup_retry_at TEXT,
      cleanup_error_code TEXT,
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL,
      expires_at TEXT NOT NULL,
      UNIQUE (id, conversation_id),
      CHECK (original_width IS NULL OR original_width > 0),
      CHECK (original_height IS NULL OR original_height > 0),
      CHECK ((original_width IS NULL AND original_height IS NULL) OR (original_width IS NOT NULL AND original_height IS NOT NULL)),
      CHECK (original_bytes > 0),
      CHECK (length(original_sha256) = 64),
      CHECK (processed_width IS NULL OR processed_width > 0),
      CHECK (processed_height IS NULL OR processed_height > 0),
      CHECK ((processed_width IS NULL AND processed_height IS NULL) OR (processed_width IS NOT NULL AND processed_height IS NOT NULL)),
      CHECK (processed_bytes IS NULL OR processed_bytes > 0),
      CHECK (source_kind IN ('user_upload', 'assistant_generated')),
      CHECK (processed_mime_type IS NULL OR processed_mime_type IN ('image/jpeg', 'image/png', 'image/webp', 'audio/mpeg', 'audio/wav', 'audio/ogg', 'audio/mp4', 'video/mp4', 'video/webm')),
      CHECK (processed_sha256 IS NULL OR length(processed_sha256) = 64),
      CHECK (preview_mime_type IS NULL OR preview_mime_type = 'image/webp'),
      CHECK (preview_width IS NULL OR preview_width > 0),
      CHECK (preview_height IS NULL OR preview_height > 0),
      CHECK (preview_bytes IS NULL OR preview_bytes > 0),
      CHECK (preview_sha256 IS NULL OR length(preview_sha256) = 64),
      CHECK (
        (preview_mime_type IS NULL AND preview_width IS NULL AND preview_height IS NULL AND preview_bytes IS NULL AND preview_sha256 IS NULL AND preview_storage_key IS NULL)
        OR (preview_mime_type IS NOT NULL AND preview_width IS NOT NULL AND preview_height IS NOT NULL AND preview_bytes IS NOT NULL AND preview_sha256 IS NOT NULL AND preview_storage_key IS NOT NULL)
      ),
      CHECK (source_kind != 'assistant_generated' OR preview_storage_key IS NOT NULL
        OR processed_mime_type IN ('audio/mpeg', 'audio/wav', 'audio/ogg', 'audio/mp4', 'video/mp4', 'video/webm')),
      CHECK (processing_status IN ('pending', 'ready', 'failed')),
      CHECK (observation_status IN ('not_requested', 'pending', 'ready', 'failed')),
      CHECK (observation_revision >= 0),
      CHECK (quota_bytes > 0),
      CHECK (cleanup_status IN ('active', 'claimed', 'failed')),
      CHECK (cleanup_attempt_count >= 0),
      CHECK (
        processing_status != 'ready'
        OR (
          processed_mime_type IS NOT NULL
          AND processed_bytes IS NOT NULL
          AND processed_sha256 IS NOT NULL
          AND storage_key IS NOT NULL
          AND (
            processed_mime_type IN ('audio/mpeg', 'audio/wav', 'audio/ogg', 'audio/mp4', 'video/mp4', 'video/webm')
            OR (processed_width IS NOT NULL AND processed_height IS NOT NULL)
          )
        )
      ),
      CHECK (
        (observation_status = 'pending' AND observation_claim_id IS NOT NULL AND observation_claimed_at IS NOT NULL)
        OR (observation_status != 'pending' AND observation_claim_id IS NULL AND observation_claimed_at IS NULL)
      ),
      CHECK (
        (turn_id IS NULL AND message_id IS NULL AND committed_at IS NULL)
        OR (turn_id IS NOT NULL AND message_id IS NOT NULL AND committed_at IS NOT NULL)
      ),
      CHECK (
        (cleanup_status = 'claimed' AND cleanup_claim_id IS NOT NULL AND cleanup_claimed_at IS NOT NULL)
        OR (cleanup_status != 'claimed' AND cleanup_claim_id IS NULL AND cleanup_claimed_at IS NULL)
      )
    )`

// ensureSQLiteChatAssetsMediaCheck delivers the widened chat_assets media
// constraints (M7 问答音视频工具设计 §3) to legacy SQLite databases. SQLite
// cannot DROP a column CHECK, so the table is rebuilt inside one transaction
// following the schedule-interval precedent (staging table -> copy -> drop ->
// rename -> recreate indexes), with PRAGMA foreign_keys toggled off and
// restored around the rebuild exactly like that migration. Fresh databases
// (new CREATE TABLE text) and already-migrated tables exit before writing.
// chat_asset_references / chat_image_generations reference chat_assets by
// name; the rename restores the name so those foreign keys stay intact.
func ensureSQLiteChatAssetsMediaCheck(ctx context.Context, db *sql.DB) error {
	var ddl sql.NullString
	err := db.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name='chat_assets'`).Scan(&ddl)
	if errors.Is(err, sql.ErrNoRows) {
		// 表不存在：由同一次 ensure 的建表阶段负责，无需迁移。
		return nil
	}
	if err != nil {
		return err
	}
	if !ddl.Valid || !strings.Contains(ddl.String, sqliteChatAssetsLegacyMimeCheckText) {
		// 已是新约束（或非预期形状）：不重建。
		return nil
	}
	existing, err := sqliteTableColumnSet(ctx, db, "chat_assets")
	if err != nil {
		return err
	}
	copyColumns := make([]string, 0, len(sqliteChatAssetsRebuildColumns))
	for _, column := range sqliteChatAssetsRebuildColumns {
		if existing[column] {
			copyColumns = append(copyColumns, column)
		}
	}
	if len(copyColumns) == 0 {
		return nil
	}
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
	if _, err := db.ExecContext(ctx, "DROP TABLE IF EXISTS chat_assets_migrating"); err != nil {
		return fmt.Errorf("清理上次重建残留失败: %w", err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, sqliteChatAssetsRebuildDDL); err != nil {
		return fmt.Errorf("创建 chat_assets 重建表失败: %w", err)
	}
	columnList := strings.Join(copyColumns, ", ")
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(
		"INSERT INTO chat_assets_migrating (%s)\nSELECT %s FROM chat_assets",
		columnList, columnList)); err != nil {
		return fmt.Errorf("回填 chat_assets 重建表数据失败: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "DROP TABLE chat_assets"); err != nil {
		return fmt.Errorf("删除旧 chat_assets 失败: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "ALTER TABLE chat_assets_migrating RENAME TO chat_assets"); err != nil {
		return fmt.Errorf("重命名 chat_assets 重建表失败: %w", err)
	}
	for _, statement := range sqliteChatAssetsRebuildIndexes {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("重建 chat_assets 索引失败: %w", err)
		}
	}
	return tx.Commit()
}

// sqliteChatAccountBindingColumns 列出 chat_conversations 会话账户唯一绑定
// 列（AI 问答会话账户唯一绑定设计，2026-09-28）在既有库上的幂等补齐声明：
// bind_account_id/bind_account_name_snapshot（唯一绑定载体，早于本设计的
// 三种绑定模式时期即存在）与 archived/search_account_id/search_model_id/
// image_account_id 四个新列。新库由 sqliteChatDDL 直接声明这些列，守卫先查
// 列存在再 ALTER。bind_mode/bind_group_id/bind_group_name_snapshot 三列已由
// 一次性迁移命令 --migrate-chat-account-only-binding 删除（SQLite 侧经表重建，
// 因表级 CHECK 引用旧列时 DROP COLUMN 报错），ensure 不再重建旧列。SQLite
// 允许 ADD COLUMN 携带列级常量 CHECK，archived 存量行取 DEFAULT 0 恒满足约束。
var sqliteChatAccountBindingColumns = []struct {
	column string
	decl   string
}{
	{"bind_account_id", "TEXT"},
	{"bind_account_name_snapshot", "TEXT"},
	{"archived", "INTEGER NOT NULL DEFAULT 0 CHECK (archived IN (0, 1))"},
	{"search_account_id", "TEXT"},
	{"search_model_id", "TEXT"},
	{"image_account_id", "TEXT"},
}

// ensureSQLiteChatAccountBindingColumns delivers the conversation
// account-binding columns to legacy databases through the same guarded PRAGMA
// table_info / ALTER TABLE ADD COLUMN pattern as the business and stats
// schemas.
func ensureSQLiteChatAccountBindingColumns(ctx context.Context, db *sql.DB) error {
	for _, target := range sqliteChatAccountBindingColumns {
		if err := ensureSQLiteTableColumn(ctx, db, "chat_conversations", target.column, target.decl); err != nil {
			return fmt.Errorf("ensure chat_conversations.%s: %w", target.column, err)
		}
	}
	return nil
}

// EnsureSQLiteCodexContext applies the codex context state schema.
func EnsureSQLiteCodexContext(ctx context.Context, db *sql.DB) (SchemaCounts, error) {
	return sqliteCodexContextScript.ensure(ctx, db)
}

// EnsureSQLiteDataset applies the dataset schema (public API logs and record cleanup targets).
func EnsureSQLiteDataset(ctx context.Context, db *sql.DB) (SchemaCounts, error) {
	return sqliteDatasetScript.ensure(ctx, db)
}

// EnsureSQLiteUsageCatalog applies the usage catalog schema (usage record shards).
func EnsureSQLiteUsageCatalog(ctx context.Context, db *sql.DB) (SchemaCounts, error) {
	return sqliteUsageCatalogScript.ensure(ctx, db)
}

// EnsureAllSQLite applies all six SQLite schemas to db in dependency order
// (business first, then stats, chat, codex context state, dataset and usage
// catalog) and returns a per-schema summary. Every statement uses
// IF NOT EXISTS, so repeated calls are idempotent.
func EnsureAllSQLite(ctx context.Context, db *sql.DB) (SQLiteResult, error) {
	var result SQLiteResult
	var err error
	if result.Business, err = EnsureSQLiteBusiness(ctx, db); err != nil {
		return SQLiteResult{}, fmt.Errorf("ensure sqlite business schema: %w", err)
	}
	if result.Stats, err = EnsureSQLiteStats(ctx, db); err != nil {
		return SQLiteResult{}, fmt.Errorf("ensure sqlite stats schema: %w", err)
	}
	if result.Chat, err = EnsureSQLiteChat(ctx, db); err != nil {
		return SQLiteResult{}, fmt.Errorf("ensure sqlite chat schema: %w", err)
	}
	if result.CodexContext, err = EnsureSQLiteCodexContext(ctx, db); err != nil {
		return SQLiteResult{}, fmt.Errorf("ensure sqlite codex context schema: %w", err)
	}
	if result.Dataset, err = EnsureSQLiteDataset(ctx, db); err != nil {
		return SQLiteResult{}, fmt.Errorf("ensure sqlite dataset schema: %w", err)
	}
	if result.UsageCatalog, err = EnsureSQLiteUsageCatalog(ctx, db); err != nil {
		return SQLiteResult{}, fmt.Errorf("ensure sqlite usage catalog schema: %w", err)
	}
	return result, nil
}
