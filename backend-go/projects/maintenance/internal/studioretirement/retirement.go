// Package studioretirement contains the explicit maintenance command that
// removes the legacy media-studio artifacts from an existing business database
// (studio 退场 schema 契约 2026-10-14):
//
//   - the four real studio tables (studio_assets / studio_tasks /
//     studio_operations / studio_user_asset_usage) — studio_shots /
//     studio_element_refs / studio_elements / studio_projects only ever
//     appeared in the old retirement DROP list and were never CREATEd, so they
//     are not cleanup targets;
//   - the idx_api_keys_studio_purpose_unique index on api_keys;
//   - api_keys rows with purpose='studio' (DELETE, not filtered copies);
//   - the api_keys.studio_domains_json column (PostgreSQL DROP COLUMN IF
//     EXISTS).
//
// Normal --ensure-schema runs stay purely additive: the destructive cleanup
// that briefly lived in the ensure statements was deliberately moved here so
// a gateway preflight can never delete studio data as a side effect. Rollback
// for everything this command deletes is restoring the business database from
// the external backup verified by --backup-confirmed — the report says so
// explicitly instead of carrying rollback SQL (unlike chatbindingmigration,
// whose dropped columns live on in a backup table).
//
// Every step is idempotent: a second run finds nothing left and reports
// alreadyRetired without writing; a run interrupted mid-way converges on
// rerun because each step is guarded. SQLite 目标随 SQLite 存储形态退役删除
// （PLAN-20261024T051925945Z B4，2026-10-24 用户裁决）。
package studioretirement

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// SchemaName is the PostgreSQL schema owning the business tables.
const SchemaName = "juhe_business"

// studioTables 是历史上真实存在过、需要清理的 4 张 studio 表。
var studioTables = []string{"studio_assets", "studio_tasks", "studio_operations", "studio_user_asset_usage"}

// legacyAPIKeysIndex / legacyAPIKeysColumn 是挂在 api_keys 上的两个 studio
// 时代遗留工件（表自身索引随 DROP TABLE 连带消失，无需逐个清理）。
const (
	legacyAPIKeysIndex  = "idx_api_keys_studio_purpose_unique"
	legacyAPIKeysColumn = "studio_domains_json"
)

// rollbackNote 是报告与 stderr 共用的回滚指引：本命令的 DELETE / DROP 无
// 备份表可回填，唯一回滚路径是外部备份整体恢复。
const rollbackNote = "回滚 = 用 --backup-confirmed 验证过的外部备份整体恢复业务库。" +
	"本命令的 DELETE（purpose='studio' 行）、DROP TABLE / DROP INDEX / DROP COLUMN 不可原地撤销，" +
	"报告因此不携带回滚 SQL（chatbindingmigration 的 RollbackSQL 先例依赖备份表回填，此处删除即销毁）。"

const isoMillisLayout = "2006-01-02T15:04:05.000Z"

// Report 是 --apply-studio-retirement 的 stdout JSON。preflight 计数在写入
// 前采集（受影响面），postflight 计数在全部步骤后采集（Ready 依据）。
type Report struct {
	Driver string `json:"driver"`
	// Preflight：各真实 studio 表行数（表不存在的记入 MissingStudioTables）。
	StudioTableRowCounts         map[string]int64               `json:"studioTableRowCounts"`
	MissingStudioTables          []string                       `json:"missingStudioTables,omitempty"`
	StudioAPIKeyRows             int64                          `json:"studioApiKeyRows"`
	OpenAICompatibleResourceRows OpenAICompatibleResourceCounts `json:"openaiCompatibleResourceRows"`
	LegacyIndexExisted           bool                           `json:"legacyIndexExisted"`
	LegacyColumnExisted          bool                           `json:"legacyColumnExisted"`
	// 已执行动作（本次运行真正发生的删除）。
	DeletedAPIKeyRows int64    `json:"deletedApiKeyRows"`
	DroppedTables     []string `json:"droppedTables,omitempty"`
	DroppedIndex      bool     `json:"droppedLegacyIndex"`
	DroppedColumn     bool     `json:"droppedLegacyColumn"`
	// RebuiltTable 是历史 sqlite 整表重建路径的报告字段；PG 直接 DROP
	// COLUMN 恒为 false（字段保留，PG 报告 JSON 形状不变）。
	RebuiltTable   bool   `json:"rebuiltTable"`
	AlreadyRetired bool   `json:"alreadyRetired"`
	FinishedAt     string `json:"finishedAt"`
	// Postflight：全零/全空即 Ready。
	RemainingStudioTables     []string `json:"remainingStudioTables,omitempty"`
	RemainingStudioAPIKeyRows int64    `json:"remainingStudioApiKeyRows"`
	RemainingLegacyIndex      bool     `json:"remainingLegacyIndex"`
	RemainingLegacyColumn     bool     `json:"remainingLegacyColumn"`
	RollbackNote              string   `json:"rollbackNote"`
}

// OpenAICompatibleResourceCounts are rows linked to studio API Keys that
// would otherwise be removed by ON DELETE CASCADE.
type OpenAICompatibleResourceCounts struct {
	Files             int64 `json:"files"`
	VectorStores      int64 `json:"vectorStores"`
	VectorStoreFiles  int64 `json:"vectorStoreFiles"`
	VectorStoreChunks int64 `json:"vectorStoreChunks"`
}

func (c OpenAICompatibleResourceCounts) any() bool {
	return c.Files > 0 || c.VectorStores > 0 || c.VectorStoreFiles > 0 || c.VectorStoreChunks > 0
}

// Ready 是后检通过条件：4 张表、studio Key 行、遗留索引与遗留列全部消失。
func (r Report) Ready() bool {
	return len(r.RemainingStudioTables) == 0 &&
		r.RemainingStudioAPIKeyRows == 0 &&
		!r.RemainingLegacyIndex &&
		!r.RemainingLegacyColumn
}

// Run performs the idempotent retirement. Statements run one by one (the
// ensure-schema model): each step is independently idempotent and guarded, so
// a mid-way failure leaves partial state that a rerun converges.
func Run(ctx context.Context, db *sql.DB, now time.Time) (Report, error) {
	report := Report{
		Driver:               "postgres",
		StudioTableRowCounts: map[string]int64{},
		FinishedAt:           now.UTC().Format(isoMillisLayout),
		RollbackNote:         rollbackNote,
	}
	if err := runPostgres(ctx, db, &report); err != nil {
		return report, err
	}
	if err := postflight(ctx, db, &report); err != nil {
		return report, err
	}
	report.AlreadyRetired = report.DeletedAPIKeyRows == 0 &&
		len(report.DroppedTables) == 0 && !report.DroppedIndex &&
		!report.DroppedColumn && !report.RebuiltTable
	return report, nil
}

// postflight re-runs the existence checks and fails the report (Ready=false,
// CLI exit 3) when anything survives; it never writes.
func postflight(ctx context.Context, db *sql.DB, report *Report) error {
	for _, table := range studioTables {
		exists, err := pgRelationExists(ctx, db, SchemaName+"."+table)
		if err != nil {
			return err
		}
		if exists {
			report.RemainingStudioTables = append(report.RemainingStudioTables, table)
		}
	}
	rows, err := studioAPIKeyCount(ctx, db)
	if err != nil {
		return err
	}
	report.RemainingStudioAPIKeyRows = rows
	indexExists, err := pgRelationExists(ctx, db, SchemaName+"."+legacyAPIKeysIndex)
	if err != nil {
		return err
	}
	report.RemainingLegacyIndex = indexExists
	columnExists, err := pgColumnExists(ctx, db)
	if err != nil {
		return err
	}
	report.RemainingLegacyColumn = columnExists
	return nil
}

// runPostgres migrates a PostgreSQL business schema.表名均为编译期常量，内联
// 拼接与 ensure/schema 包同口径。
func runPostgres(ctx context.Context, db *sql.DB, report *Report) error {
	apiKeysExists, err := pgRelationExists(ctx, db, SchemaName+".api_keys")
	if err != nil {
		return err
	}
	if !apiKeysExists {
		return errors.New("juhe_business.api_keys 不存在：请先 --ensure-schema 初始化业务库")
	}
	// 1) preflight 计数。
	for _, table := range studioTables {
		exists, err := pgRelationExists(ctx, db, SchemaName+"."+table)
		if err != nil {
			return err
		}
		if !exists {
			report.MissingStudioTables = append(report.MissingStudioTables, table)
			continue
		}
		var rows int64
		if err := db.QueryRowContext(ctx, "SELECT count(*) FROM "+SchemaName+"."+table).Scan(&rows); err != nil {
			return fmt.Errorf("统计 %s.%s 行数失败: %w", SchemaName, table, err)
		}
		report.StudioTableRowCounts[table] = rows
	}
	if report.StudioAPIKeyRows, err = studioAPIKeyCount(ctx, db); err != nil {
		return err
	}
	if report.OpenAICompatibleResourceRows, err = openAICompatibleResourceCounts(ctx, db); err != nil {
		return err
	}
	if report.OpenAICompatibleResourceRows.any() {
		return blockedByOpenAICompatibleResources(report.OpenAICompatibleResourceRows)
	}
	if report.LegacyIndexExisted, err = pgRelationExists(ctx, db, SchemaName+"."+legacyAPIKeysIndex); err != nil {
		return err
	}
	if report.LegacyColumnExisted, err = pgColumnExists(ctx, db); err != nil {
		return err
	}
	// 2) DELETE purpose='studio' 行（子表外键引用时会失败并保留原错误，不
	// 静默跳过）。
	if report.StudioAPIKeyRows > 0 {
		result, err := db.ExecContext(ctx, "DELETE FROM "+SchemaName+".api_keys WHERE purpose = 'studio'")
		if err != nil {
			return fmt.Errorf("删除 purpose='studio' api_keys 行失败: %w", err)
		}
		if affected, err := result.RowsAffected(); err == nil {
			report.DeletedAPIKeyRows = affected
		}
	}
	// 3) DROP 4 张真实 studio 表与遗留索引（幻影表名不在清理范围）。
	for _, table := range studioTables {
		if _, missing := contains(report.MissingStudioTables, table); missing {
			continue
		}
		if _, err := db.ExecContext(ctx, "DROP TABLE IF EXISTS "+SchemaName+"."+table); err != nil {
			return fmt.Errorf("DROP TABLE %s.%s 失败: %w", SchemaName, table, err)
		}
		report.DroppedTables = append(report.DroppedTables, table)
	}
	if report.LegacyIndexExisted {
		if _, err := db.ExecContext(ctx, "DROP INDEX IF EXISTS "+SchemaName+"."+legacyAPIKeysIndex); err != nil {
			return fmt.Errorf("DROP INDEX %s.%s 失败: %w", SchemaName, legacyAPIKeysIndex, err)
		}
		report.DroppedIndex = true
	}
	// 4) 删 studio_domains_json 列（PG 直接 DROP COLUMN）。
	if report.LegacyColumnExisted {
		if _, err := db.ExecContext(ctx, "ALTER TABLE "+SchemaName+".api_keys DROP COLUMN IF EXISTS "+legacyAPIKeysColumn); err != nil {
			return fmt.Errorf("DROP COLUMN api_keys.%s 失败: %w", legacyAPIKeysColumn, err)
		}
		report.DroppedColumn = true
	}
	return nil
}

// studioAPIKeyCount 统计 purpose='studio' 的 api_keys 行。
func studioAPIKeyCount(ctx context.Context, db *sql.DB) (int64, error) {
	query := "SELECT count(*) FROM " + SchemaName + ".api_keys WHERE purpose = 'studio'"
	var rows int64
	if err := db.QueryRowContext(ctx, query).Scan(&rows); err != nil {
		return 0, fmt.Errorf("统计 purpose='studio' api_keys 行数失败: %w", err)
	}
	return rows, nil
}

func openAICompatibleResourceCounts(ctx context.Context, db *sql.DB) (OpenAICompatibleResourceCounts, error) {
	prefix := SchemaName + "."
	tables := []string{
		"openai_compatible_files",
		"openai_compatible_vector_stores",
		"openai_compatible_vector_store_files",
		"openai_compatible_vector_store_chunks",
	}
	queries := []string{
		"SELECT count(*) FROM " + prefix + "openai_compatible_files WHERE api_key_id IN (SELECT id FROM " + prefix + "api_keys WHERE purpose = 'studio')",
		"SELECT count(*) FROM " + prefix + "openai_compatible_vector_stores WHERE api_key_id IN (SELECT id FROM " + prefix + "api_keys WHERE purpose = 'studio')",
		"SELECT count(*) FROM " + prefix + "openai_compatible_vector_store_files WHERE api_key_id IN (SELECT id FROM " + prefix + "api_keys WHERE purpose = 'studio')",
		"SELECT count(*) FROM " + prefix + "openai_compatible_vector_store_chunks chunks INNER JOIN " + prefix + "openai_compatible_vector_store_files files ON files.vector_store_id = chunks.vector_store_id AND files.file_id = chunks.file_id WHERE files.api_key_id IN (SELECT id FROM " + prefix + "api_keys WHERE purpose = 'studio')",
	}
	counts := OpenAICompatibleResourceCounts{}
	values := []*int64{&counts.Files, &counts.VectorStores, &counts.VectorStoreFiles, &counts.VectorStoreChunks}
	for i, table := range tables {
		exists, err := pgRelationExists(ctx, db, prefix+table)
		if err != nil {
			return OpenAICompatibleResourceCounts{}, err
		}
		if !exists {
			continue
		}
		if err := db.QueryRowContext(ctx, queries[i]).Scan(values[i]); err != nil {
			return OpenAICompatibleResourceCounts{}, fmt.Errorf("统计 studio API Key 关联的 %s 失败: %w", table, err)
		}
	}
	return counts, nil
}

func blockedByOpenAICompatibleResources(counts OpenAICompatibleResourceCounts) error {
	return fmt.Errorf("studio 退场已在写入前拒绝：关联 OpenAI-compatible 资源仍存在（files=%d vectorStores=%d vectorStoreFiles=%d vectorStoreChunks=%d）；请先迁移或清理这些资源后重试", counts.Files, counts.VectorStores, counts.VectorStoreFiles, counts.VectorStoreChunks)
}

// pgRelationExists reports whether a schema-qualified relation (table or
// index) exists; to_regclass returns NULL for missing relations.
func pgRelationExists(ctx context.Context, db *sql.DB, qualified string) (bool, error) {
	var found sql.NullString
	if err := db.QueryRowContext(ctx, "SELECT to_regclass($1)", qualified).Scan(&found); err != nil {
		return false, fmt.Errorf("探测关系 %s 失败: %w", qualified, err)
	}
	return found.Valid && strings.TrimSpace(found.String) != "", nil
}

// pgColumnExists reports whether api_keys still carries the legacy column.
func pgColumnExists(ctx context.Context, db *sql.DB) (bool, error) {
	var count int
	err := db.QueryRowContext(ctx,
		"SELECT count(*) FROM information_schema.columns WHERE table_schema = $1 AND table_name = 'api_keys' AND column_name = $2",
		SchemaName, legacyAPIKeysColumn).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("探测 api_keys.%s 列失败: %w", legacyAPIKeysColumn, err)
	}
	return count > 0, nil
}

// contains 是小清单（4 个表名）的线性查找辅助。
func contains(list []string, target string) (int, bool) {
	for i, item := range list {
		if item == target {
			return i, true
		}
	}
	return -1, false
}
