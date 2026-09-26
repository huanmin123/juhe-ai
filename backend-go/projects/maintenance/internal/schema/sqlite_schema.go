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
	return counts, nil
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
	return sqliteChatScript.ensure(ctx, db)
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
