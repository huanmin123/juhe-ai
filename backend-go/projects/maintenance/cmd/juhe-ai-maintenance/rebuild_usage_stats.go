// The juhe-ai-maintenance rebuild-usage-stats command (BUG-0182): the
// offline statistics-cache rebuild entry that replaces the archived Node
// script backend/dist/scripts/maintenance/rebuild-usage-stats.js.
//
// 语义与边界：
//   - 离线门禁对齐 Node 归档脚本（BUG-0182 :52-53）：必须显式
//     --confirm-offline 或设置 JUHE_AI_CONFIRM_USAGE_STATS_REBUILD=1，
//     否则拒绝执行且零副作用（门禁先于一切开库动作）；
//   - 重建编排、聚合口径、窗口阶段全部来自 jobs 模块受控导出面
//     backend-go-jobs/statsrebuild（内部复用 internal/statsagg，避免
//     第二套口径实现；受控例外登记见 docs/architecture/Go三项目架构基线.md
//     §2 与 scripts/regression/go-project-boundary-regression.mjs 白名单）；
//   - PostgreSQL 唯一形态：--dsn URL，事实源 juhe_usage.usage_records，
//     重建目标 juhe_stats，业务库只读（原 --driver sqlite --paths 臂随
//     sqlite 存储形态退役删除）；
//   - 前置条件：gateway 与 jobs 已停止（停服离线窗口内执行）。除显式确认
//     门禁外，重建入口还会做离线校验（statsrebuild.CheckJobsOffline）：在
//     background_job_leases 发现未过期租约或 background_task_runs 在活动
//     回看窗口（5 分钟）内有心跳痕迹时拒绝，先于任何清空动作，零副作用。
//
// 退出码契约：0 重建完成（含空源放弃历史语义）、1 运行失败、2 用法错误
// （门禁拒绝、driver/连接参数缺失或矛盾）、3 未完成（达到 --max-batches
// 上限，可再次执行续跑）、4 离线校验未通过（检测到 jobs 活动痕迹；零副作用，
// 停止 jobs 并等待活动滑出回看窗口后重试）。
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/statsrebuild"
)

// confirmUsageStatsRebuildEnv 与 Node 归档脚本同名的离线确认环境变量。
const confirmUsageStatsRebuildEnv = "JUHE_AI_CONFIRM_USAGE_STATS_REBUILD"

// rebuildUsageStatsReport 是命令的 JSON 报告面。
type rebuildUsageStatsReport struct {
	Driver               string              `json:"driver"`
	ConfirmOfflineSource string              `json:"confirmOfflineSource"`
	Completed            bool                `json:"completed"`
	Rebuild              statsrebuild.Result `json:"rebuild"`
}

// rebuildUsageStatsResult 返回 CLI 退出码；runMaintenance 派发它，测试进程
// 内直调。门禁检查先于一切开库动作，拒绝路径零副作用。
func rebuildUsageStatsResult(dsn string, batchSize, maxBatches int, confirmOffline bool) int {
	confirmSource := ""
	if confirmOffline {
		confirmSource = "flag"
	} else if os.Getenv(confirmUsageStatsRebuildEnv) == "1" {
		confirmSource = "env"
	}
	if confirmSource == "" {
		fmt.Fprintf(os.Stderr, "统计缓存离线重建被拒绝：必须显式 --confirm-offline 或设置 %s=1。该命令会清空统计缓存面并从 usage_records 重放聚合，只能在 gateway/jobs 停止的离线窗口执行。\n", confirmUsageStatsRebuildEnv)
		return 2
	}
	parsedDSN := strings.TrimSpace(dsn)
	if parsedDSN == "" {
		fmt.Fprintln(os.Stderr, "统计缓存重建需要 --dsn 指向显式的 PostgreSQL URL")
		return 2
	}
	statsDB, openErr := openPostgresBootstrap(parsedDSN)
	if openErr != nil {
		fmt.Fprintf(os.Stderr, "%v\n", openErr)
		return 2
	}
	defer statsDB.Close()

	fmt.Fprintln(os.Stderr, "统计缓存离线重建开始：清空统计结果面并重置游标，从 usage_records 重放聚合（业务库与 usage_records 源表只读）。")
	result, rebuildErr := statsrebuild.Rebuild(context.Background(), statsrebuild.Options{
		DB:         statsDB,
		BatchSize:  batchSize,
		MaxBatches: maxBatches,
		Logf: func(format string, args ...any) {
			fmt.Fprintf(os.Stderr, "[rebuild-usage-stats] "+format+"\n", args...)
		},
	})
	if rebuildErr != nil {
		if errors.Is(rebuildErr, statsrebuild.ErrJobsNotOffline) {
			fmt.Fprintf(os.Stderr, "统计缓存离线重建被拒绝（离线校验未通过，零副作用）：%v\n", rebuildErr)
			return 4
		}
		fmt.Fprintf(os.Stderr, "统计缓存离线重建失败：%v\n", rebuildErr)
		return 1
	}
	if !result.Drained {
		fmt.Fprintf(os.Stderr, "统计缓存重建未完成：已达 --max-batches 上限（%d 批，处理 %d 行）。统计缓存处于部分重建状态；请调大 --max-batches（或 --batch-size）后再次执行本命令完成重建。\n", result.Batches, result.ProcessedRows)
	}
	if result.EmptySource {
		fmt.Fprintln(os.Stderr, "usage_records 为空（历史用量已清理或尚未产生）：放弃历史统计重建，后续从新请求重新累计。统计缓存面已重置为空。")
	}
	report := rebuildUsageStatsReport{
		Driver:               "postgres",
		ConfirmOfflineSource: confirmSource,
		Completed:            result.Drained,
		Rebuild:              result,
	}
	if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
		fmt.Fprintf(os.Stderr, "encode rebuild-usage-stats report: %v\n", err)
		return 1
	}
	if !result.Drained {
		return 3
	}
	return 0
}
