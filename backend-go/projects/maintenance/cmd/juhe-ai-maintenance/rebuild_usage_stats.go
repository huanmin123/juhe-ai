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
//   - SQLite standalone：--driver sqlite --paths business=...,stats=...
//     （沿用六库 --paths 键词汇，仅要求 business/stats 两个键）；事实源是
//     stats 库内 usage_records 镜像表，业务库句柄只读；
//   - PostgreSQL performance：--driver postgres --dsn URL，事实源
//     juhe_usage.usage_records，重建目标 juhe_stats，业务库只读；
//   - 前置条件：gateway 与 jobs 已停止（停服离线窗口内执行）。
//
// 退出码契约：0 重建完成（含空源放弃历史语义）、1 运行失败、2 用法错误
// （门禁拒绝、driver/连接参数缺失或矛盾）、3 未完成（达到 --max-batches
// 上限，可再次执行续跑）。
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/statsrebuild"
	"github.com/huanminabc/juhe-ai/backend-go-platform/sqldialect"
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

// parseRebuildSQLitePaths 解析 --paths（键词汇与六库 bootstrap 相同，但重建
// 只需要 business 与 stats 两个键；其余键接受并忽略值，保持操作者可以直接
// 复用 --ensure-schema 的完整 --paths 参数）。
func parseRebuildSQLitePaths(raw string) (business, stats string, err error) {
	seen := map[string]bool{}
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		key, value, found := strings.Cut(entry, "=")
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if !found || key == "" || value == "" {
			return "", "", fmt.Errorf("--paths 条目必须是 key=value 形式：%q", entry)
		}
		if seen[key] {
			return "", "", fmt.Errorf("--paths 条目重复：%q", key)
		}
		seen[key] = true
		switch key {
		case "business":
			business = value
		case "stats":
			stats = value
		case "chat", "dataset", "usage-catalog", "codex-context-shard-root", "codex-context-shard-count":
			// 与六库 bootstrap 同词汇、重建不消费。
		default:
			return "", "", fmt.Errorf("--paths 未知 key %q（有效 key：business、chat、dataset、usage-catalog、stats、codex-context-shard-root、codex-context-shard-count）", key)
		}
	}
	var missing []string
	if business == "" {
		missing = append(missing, "business")
	}
	if stats == "" {
		missing = append(missing, "stats")
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return "", "", fmt.Errorf("--paths 缺少必填 key：%s（统计重建读写 stats 库，并以只读方式打开 business 库）", strings.Join(missing, "、"))
	}
	return business, stats, nil
}

// rebuildUsageStatsResult 返回 CLI 退出码；runMaintenance 派发它，测试进程
// 内直调。门禁检查先于一切开库动作，拒绝路径零副作用。
func rebuildUsageStatsResult(driver, paths, dsn string, batchSize, maxBatches int, confirmOffline bool) int {
	driver = strings.ToLower(strings.TrimSpace(driver))
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
	if driver != "sqlite" && driver != "postgres" {
		fmt.Fprintf(os.Stderr, "--driver 必须为 sqlite 或 postgres: %q\n", driver)
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

	var (
		statsDB     *sql.DB
		businessDB  *sql.DB
		bindPostgres bool
	)
	if driver == "sqlite" {
		businessPath, statsPath, parseErr := parseRebuildSQLitePaths(paths)
		if parseErr != nil {
			fmt.Fprintf(os.Stderr, "%v\n", parseErr)
			return 2
		}
		var openErr error
		statsDB, openErr = bootstrapOpenSQLiteForRebuild(statsPath)
		if openErr != nil {
			fmt.Fprintf(os.Stderr, "%v\n", openErr)
			return 2
		}
		defer statsDB.Close()
		// 业务库只读打开（聚合授权链查找 resource_authorizations/accounts 与
		// 默认统计时区 system_settings 读取所在；绝不写入）。
		businessDB, openErr = bootstrapOpenSQLiteForRebuild(businessPath)
		if openErr != nil {
			fmt.Fprintf(os.Stderr, "%v\n", openErr)
			return 2
		}
		defer businessDB.Close()
	} else {
		parsedDSN := strings.TrimSpace(dsn)
		if parsedDSN == "" {
			fmt.Fprintln(os.Stderr, "--driver postgres 需要 --dsn 指向显式的 PostgreSQL URL")
			return 2
		}
		db, openErr := openPostgresBootstrap(parsedDSN)
		if openErr != nil {
			fmt.Fprintf(os.Stderr, "%v\n", openErr)
			return 2
		}
		statsDB = db
		defer statsDB.Close()
		bindPostgres = true
	}

	fmt.Fprintln(os.Stderr, "统计缓存离线重建开始：清空统计结果面并重置游标，从 usage_records 重放聚合（业务库与 usage_records 源表只读）。")
	result, rebuildErr := statsrebuild.Rebuild(context.Background(), statsrebuild.Options{
		DB:         statsDB,
		Dialect:    sqldialectDialect(bindPostgres),
		BusinessDB: businessDB,
		BatchSize:  batchSize,
		MaxBatches: maxBatches,
		Logf: func(format string, args ...any) {
			fmt.Fprintf(os.Stderr, "[rebuild-usage-stats] "+format+"\n", args...)
		},
	})
	if rebuildErr != nil {
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
		Driver:               driver,
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
