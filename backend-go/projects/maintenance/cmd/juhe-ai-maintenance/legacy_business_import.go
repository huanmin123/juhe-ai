// 业务库重建回迁命令（2026-10-22 上线批）：
//
//	juhe-ai-maintenance --import-legacy-business --legacy-dsn <旧库> \
//	    --dsn <目标库> --secret <生产 JUHE_AI_SECRET> [--dry-run]
//
// 目标库必须已经执行过 `--ensure-schema --seed`（结构由 HEAD schema 提供）。
// 命令只回迁业务配置（账户/分组/路由/API Key/授权/标签/设置/团队/外部来源/
// 配额窗口），历史用量、审计、统计、模型检测运行态按上线决策整体丢弃。
//
// dry-run 完整执行同一事务后回滚，只输出报告；正式执行单事务提交，任何一行
// 无法翻译即整体失败，不会留下半成品业务库。
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/legacybusinessimport"
)

// legacyBusinessImportResult returns the CLI exit code. runMaintenance dispatches
// it, and tests call it directly.
func legacyBusinessImportResult(dryRun bool, legacyDSN, targetDSN, secret string) int {
	if strings.TrimSpace(legacyDSN) == "" {
		fmt.Fprintln(os.Stderr, "--import-legacy-business 需要 --legacy-dsn 指向旧 schema 业务库")
		return 2
	}
	if strings.TrimSpace(targetDSN) == "" {
		fmt.Fprintln(os.Stderr, "--import-legacy-business 需要 --dsn 指向目标业务库")
		return 2
	}
	if strings.TrimSpace(secret) == "" {
		fmt.Fprintln(os.Stderr, "--import-legacy-business 需要 --secret（或 JUHE_AI_SECRET）解密旧账户凭据")
		return 2
	}
	source, err := sql.Open("pgx", legacyDSN)
	if err != nil {
		fmt.Fprintf(os.Stderr, "打开旧库连接失败: %v\n", err)
		return 1
	}
	defer source.Close()
	source.SetMaxOpenConns(2)
	target, err := sql.Open("pgx", targetDSN)
	if err != nil {
		fmt.Fprintf(os.Stderr, "打开目标库连接失败: %v\n", err)
		return 1
	}
	defer target.Close()
	target.SetMaxOpenConns(1)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	report, err := legacybusinessimport.Import(ctx, source, target, legacybusinessimport.Options{
		Secret: secret,
		DryRun: dryRun,
	})
	encoded, marshalErr := json.MarshalIndent(report, "", "  ")
	if marshalErr == nil {
		fmt.Println(string(encoded))
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "回迁失败: %v\n", err)
		return 1
	}
	return 0
}
