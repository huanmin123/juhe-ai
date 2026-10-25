// merge 路由策略 → 超级分组迁移命令薄壳（超级分组设计 §6）：
//   - --migrate-merge-super-groups --dry-run --manifest-out <path>：
//     只读扫描存量业务库，写出可复核 manifest（dry-run 唯一写入）；
//   - --migrate-merge-super-groups（不带 --dry-run）：要求
//     --node-stopped --go-stopped --backup-confirmed，在单个业务库事务内执行
//     mapped 策略迁移并写出执行后 manifest；blocked 计数 > 0 时退出码 1。
//
// 存储目标为 --dsn 单形态（PLAN-20261024T051925945Z B4 删除 --driver/--paths
// 旗标族）。
//
// 退出码契约：0 成功且 blocked=0（dry-run 允许 blocked 非零，退出 0）；1 执行
// 失败或 apply 后仍有 blocked；2 用法错误。
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/mergesupermigration"
)

func mergeSuperGroupsMigrationResult(dryRun bool, dsn, manifestOut, batchID string, nodeStopped, goStopped, backupConfirmed bool) int {
	parsedDSN := strings.TrimSpace(dsn)
	if parsedDSN == "" {
		fmt.Fprintln(os.Stderr, "--migrate-merge-super-groups 需要 --dsn 指向显式的 PostgreSQL URL")
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
	db, err := openPostgresBootstrap(parsedDSN)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 2
	}
	defer db.Close()
	options := mergesupermigration.Options{BatchID: strings.TrimSpace(batchID)}
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
