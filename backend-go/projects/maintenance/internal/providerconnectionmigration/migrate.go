package providerconnectionmigration

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"sort"
	"time"
)

// dry-run 流程（任务切片）：连旧库 → 只读全量拉取 §10 覆盖的 11 张 source 表
// 并按表清点历史行 → 构建 canonical snapshot 与 source_snapshot_hash → 逐行
// 应用规则 0-13 → 产出 manifest。除调用方把 manifest JSON 写入 --manifest-out
// 外，全程零写入（不开写事务，不写任何数据库或运行时文件）。
//
// 切片边界：正式执行形态（--manifest/--manifest-sha256/--backup-id/
// --backup-receipt 消费分支）本切片不实现；CLI 层对非 dry-run 调用返回明确
// 错误（见 cmd/juhe-ai-maintenance/provider_connection_migration.go）。

// Options 是 DryRun 的注入参数。
type Options struct {
	Dialect Dialect
	Now     time.Time // 零值取当前 UTC（manifest created_at 与证据 retrieved_at）
}

// DryRun 执行只读迁移预演并返回 manifest。返回的 Manifest 由调用方序列化写
// 入 --manifest-out（UTF-8 JSON 无 BOM）。
func DryRun(ctx context.Context, db *sql.DB, options Options) (Manifest, error) {
	manifest := Manifest{FormatVersion: FormatVersion}
	if options.Dialect != DialectPostgres && options.Dialect != DialectSQLite {
		return manifest, fmt.Errorf("不支持的存在库方言 %q（仅 postgres/sqlite）", options.Dialect)
	}
	now := options.Now
	if now.IsZero() {
		now = time.Now()
	}
	source, err := loadSource(ctx, db, options.Dialect)
	if err != nil {
		return manifest, err
	}
	snapshot := source.snapshot()
	hash, err := snapshot.HashHex()
	if err != nil {
		return manifest, fmt.Errorf("计算 source snapshot hash 失败: %w", err)
	}
	output := applyRules(source, now)
	manifest.CreatedAtUTC = now.UTC().Format(time.RFC3339Nano)
	manifest.SourceSchemaVersion = SourceSchemaVersion
	manifest.TargetSchemaVersion = TargetSchemaVersion
	manifest.SourceSnapshotHash = hash
	manifest.SourceTableRowCounts = sourceTableRowCounts(source)
	manifest.Rows = output.rows
	manifest.GeneratedTargetIDs = output.generated
	manifest.BlockedCounts = output.blocked
	manifest.PublicModelConflicts = output.publicConflicts
	manifest.HealthCheckBlocks = output.healthBlocks
	manifest.Notes = append(output.notes, historyMissingNotes(source)...)
	return manifest, nil
}

func sourceTableRowCounts(source *sourceData) map[string]int {
	counts := map[string]int{}
	for name, table := range source.tables {
		counts[name] = len(table.rows)
	}
	// 历史表按表清点行数同样入 counts（不进 snapshot）。
	for name, count := range source.historyCounts {
		counts[name] = count
	}
	return counts
}

func historyMissingNotes(source *sourceData) []string {
	if len(source.historyMissing) == 0 {
		return nil
	}
	missing := append([]string{}, source.historyMissing...)
	sort.Strings(missing)
	return []string{fmt.Sprintf("规则 11：历史表 %v 在源库不存在，按 0 行清点（存量旧库应包含全部三表；缺失需人工复核）", missing)}
}

// ManifestSHA256 返回 manifest JSON 原始字节的 SHA-256（64 位小写十六进制），
// 供 CLI 汇总输出与正式执行切片的 --manifest-sha256 复核口径预演。
func ManifestSHA256(encoded []byte) string {
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}
