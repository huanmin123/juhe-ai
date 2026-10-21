// merge 路由策略 → 超级分组迁移命令薄壳（超级分组设计 §6）：
//   - --migrate-merge-super-groups --dry-run --manifest-out <path>：
//     只读扫描存量业务库，写出可复核 manifest（dry-run 唯一写入）；
//   - --migrate-merge-super-groups（不带 --dry-run）：要求
//     --node-stopped --go-stopped --backup-confirmed，在单个业务库事务内执行
//     mapped 策略迁移并写出执行后 manifest；blocked 计数 > 0 时退出码 1。
//
// 存储形态与其它 maintenance 命令一致：--driver postgres --dsn 或
// --driver sqlite --paths（仅必填 business=，其余键接受并忽略）。
//
// 退出码契约：0 成功且 blocked=0（dry-run 允许 blocked 非零，退出 0）；1 执行
// 失败或 apply 后仍有 blocked；2 用法错误。
package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/mergesupermigration"
)

func mergeSuperGroupsMigrationResult(dryRun bool, driver, paths, dsn, manifestOut, batchID string, nodeStopped, goStopped, backupConfirmed bool) int {
	driver = strings.ToLower(strings.TrimSpace(driver))
	if driver != "postgres" && driver != "sqlite" {
		fmt.Fprintln(os.Stderr, "--driver 必须为 sqlite 或 postgres")
		return 2
	}
	if driver == "sqlite" && strings.TrimSpace(dsn) != "" {
		fmt.Fprintln(os.Stderr, "--dsn 只适用于 --driver postgres；sqlite 模式使用 --paths")
		return 2
	}
	if driver == "postgres" && strings.TrimSpace(paths) != "" {
		fmt.Fprintln(os.Stderr, "--paths 只适用于 --driver sqlite；postgres 模式使用 --dsn")
		return 2
	}
	parsedDSN := strings.TrimSpace(dsn)
	if driver == "postgres" && parsedDSN == "" {
		fmt.Fprintln(os.Stderr, "--driver postgres 需要 --dsn 指向显式的 PostgreSQL URL")
		return 2
	}
	outPath := strings.TrimSpace(manifestOut)
	if outPath == "" {
		fmt.Fprintln(os.Stderr, "--migrate-merge-super-groups 需要 --manifest-out 指向 manifest 输出文件")
		return 2
	}
	if strings.TrimSpace(batchID) == "" {
		fmt.Fprintln(os.Stderr, "--migrate-merge-super-groups 需要 --migration-batch-id（manifest 复用键）")
		return 2
	}
	if !dryRun && (!nodeStopped || !goStopped || !backupConfirmed) {
		fmt.Fprintln(os.Stderr, "正式执行要求 --node-stopped --go-stopped --backup-confirmed")
		return 2
	}
	var db *sql.DB
	var err error
	dialect := mergesupermigration.DialectPostgres
	if driver == "sqlite" {
		businessPath, pathErr := parseCapabilityDomainBusinessPath(paths)
		if pathErr != nil {
			fmt.Fprintf(os.Stderr, "%v\n", pathErr)
			return 2
		}
		db, err = sql.Open("sqlite", "file:"+businessPath+"?mode=rwc&_pragma=busy_timeout(5000)")
		if db != nil {
			// 与其它 maintenance SQLite 入口一致：单连接串行化，避免迁移事务
			// 与连接池内另一条连接争写锁导致 database is locked。
			db.SetMaxOpenConns(1)
		}
		dialect = mergesupermigration.DialectSQLite
	} else {
		db, err = openPostgresBootstrap(parsedDSN)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 2
	}
	defer db.Close()
	options := mergesupermigration.Options{Dialect: dialect, BatchID: strings.TrimSpace(batchID)}
	var plan mergesupermigration.Plan
	if dryRun {
		plan, err = mergesupermigration.BuildPlan(context.Background(), db, options)
	} else {
		plan, err = mergesupermigration.Apply(context.Background(), db, options)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "merge→super 迁移失败: %v\n", err)
		return 1
	}
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(plan); err != nil {
		fmt.Fprintf(os.Stderr, "encode merge→super manifest: %v\n", err)
		return 1
	}
	if err := os.WriteFile(outPath, buffer.Bytes(), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "写 manifest %s 失败: %v\n", outPath, err)
		return 1
	}
	mode := "apply"
	if dryRun {
		mode = "dry-run"
	}
	fmt.Printf("%s manifest 已写入 %s (batch=%s, mapped=%d, blocked=%d)\n", mode, outPath, plan.BatchID, plan.Mapped, plan.Blocked)
	for _, strategy := range plan.Strategies {
		if strategy.Disposition == mergesupermigration.DispositionBlocked {
			fmt.Fprintf(os.Stderr, "blocked: strategy=%s owner=%s reason=%s\n", strategy.StrategyID, strategy.OwnerID, strategy.BlockedReason)
		}
	}
	if !dryRun && plan.Blocked > 0 {
		return 1
	}
	return 0
}
