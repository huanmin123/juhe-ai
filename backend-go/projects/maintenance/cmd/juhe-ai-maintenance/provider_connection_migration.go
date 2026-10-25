// 供应商与上游协议面重构迁移命令薄壳（《供应商与上游协议面重构设计》§10，
// docs/functions/供应商与上游协议面重构设计.md :421-465）——dry-run 切片：
//   - --migrate-provider-connection-model --dry-run --manifest-out <path>：
//     只读旧 schema 存量业务库（--dsn），把可复核 manifest
//     写到 --manifest-out（dry-run 唯一写入）。判定实现与 manifest 契约在
//     internal/providerconnectionmigration。
//   - 非 dry-run（正式执行形态，--manifest/--manifest-sha256/--backup-id/
//     --backup-receipt 消费分支）本切片不交付：调用返回退出码 2 与固定错误
//     「正式执行尚未交付：历史 usage/audit 列改造完成后提供」。这是切片边界，
//     不是功能开关。
//
// 退出码契约：0 dry-run manifest 已写出（blocked 计数允许非零——dry-run 就是
// 用来列出 conflict 的）、1 运行失败、2 用法错误。
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/providerconnectionmigration"
)

// providerConnectionMigrationResult 返回 CLI 退出码；runMaintenance 派发它，
// 测试进程内直调。
func providerConnectionMigrationResult(dryRun bool, dsn, manifestOut string) int {
	if !dryRun {
		fmt.Fprintln(os.Stderr, "正式执行尚未交付：历史 usage/audit 列改造完成后提供")
		return 2
	}
	parsedDSN := strings.TrimSpace(dsn)
	if parsedDSN == "" {
		fmt.Fprintln(os.Stderr, "--migrate-provider-connection-model --dry-run 需要 --dsn 指向显式的 PostgreSQL URL")
		return 2
	}
	outPath := strings.TrimSpace(manifestOut)
	if outPath == "" {
		fmt.Fprintln(os.Stderr, "--migrate-provider-connection-model --dry-run 需要 --manifest-out 指向 manifest 输出文件（dry-run 唯一写入）")
		return 2
	}
	db, err := openPostgresBootstrap(parsedDSN)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 2
	}
	defer db.Close()
	manifest, err := providerconnectionmigration.DryRun(context.Background(), db, providerconnectionmigration.Options{
		})
	if err != nil {
		fmt.Fprintf(os.Stderr, "provider connection model migration dry-run 失败: %v\n", err)
		return 1
	}
	// manifest JSON：UTF-8 无 BOM；SetEscapeHTML(false) 保持非 ASCII 原文。
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(manifest); err != nil {
		fmt.Fprintf(os.Stderr, "encode provider connection model migration manifest: %v\n", err)
		return 1
	}
	if err := os.WriteFile(outPath, buffer.Bytes(), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "写 manifest %s 失败: %v\n", outPath, err)
		return 1
	}
	counts := manifest.BlockedCounts
	fmt.Printf("dry-run manifest 已写入 %s (format_version=%d, source_snapshot_hash=%s, manifest_sha256=%s)\n",
		outPath, manifest.FormatVersion, manifest.SourceSnapshotHash,
		providerconnectionmigration.ManifestSHA256(buffer.Bytes()))
	fmt.Fprintf(os.Stderr, "blocked 计数：accounts=%d connections=%d models=%d supported_models=%d mappings=%d groups=%d health_checks=%d history_rows=%d public_model_conflicts=%d（dry-run 允许非零；正式执行前必须全部为 0）\n",
		counts.AccountsBlocked, counts.ConnectionsBlocked, counts.ModelsBlocked,
		counts.SupportedModelsBlocked, counts.MappingsBlocked, counts.GroupsBlocked,
		counts.HealthChecksBlocked, counts.HistoryRowsUnmatched, counts.PublicModelConflicts)
	return 0
}
