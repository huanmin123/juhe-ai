// 退役监控表清退命令薄壳（--drop-retired-monitoring-tables，2026-10-14）：
//   - 幂等（探测 + DROP TABLE IF EXISTS）删除 12 张退役监控表 = 统计库 9 张
//     （system_metrics_* / process_event_loop_* 6 张 + go_runtime_metrics_*
//     3 张）+ 表监控 3 张（database_storage_snapshots、
//     table_storage_snapshots、table_monitor_owner_leases）；
//   - 必须显式携带停写/备份确认门禁（--node-stopped --go-stopped
//     --backup-confirmed），缺一即拒绝（先例照 studio 退场/调度偏好/能力域
//     命令；命令本身不负责停进程）；
//   - 存储目标为 --dsn 单形态（PLAN-20261024T051925945Z B4 删除
//     --driver/--paths 旗标族）：postgres 单 --dsn 覆盖 juhe_stats schema
//     全部 12 张（gometrics 与 tablemonitor 的 PG 建表都落在同一库；历史
//     独立 metrics 库以该库为 --dsn 重跑本命令即可，缺席表是良性 no-op）。
//
// 退出码契约：0 成功（含重跑的「只报告已清退」）、1 运行失败（任一表失败
// 或目标库不可达；报告始终列出已成功与已缺席清单）、2 用法错误（门禁/旗标
// 缺失或矛盾）。回滚 = 用 --backup-confirmed 验证过的外部备份整体恢复对应
// 数据库，报告 JSON 的 rollbackNote 明示（DROP 无备份表可回填，不携带回滚
// SQL）；回滚指引同时落在 stderr，stdout 保持单条 Report JSON。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/monitoringretirement"
)

// dropRetiredMonitoringTablesResult 返回 --drop-retired-monitoring-tables
// 的 CLI 退出码；runMaintenance 派发它，测试进程内直调。门禁检查先于一切
// 开库动作；用法错误退出码 2，运行失败（任一表失败或目标库不可达）退出码 1。
func dropRetiredMonitoringTablesResult(dsn string, nodeStopped, goStopped, backupConfirmed bool) int {
	if !nodeStopped || !goStopped || !backupConfirmed {
		fmt.Fprintln(os.Stderr, "退役监控表清退被拒绝：必须显式携带 --node-stopped --go-stopped --backup-confirmed（停写 gateway/jobs 并验证可恢复备份后才能执行；命令本身不负责停进程）")
		return 2
	}
	parsedDSN := strings.TrimSpace(dsn)
	if parsedDSN == "" {
		fmt.Fprintln(os.Stderr, "--drop-retired-monitoring-tables 需要 --dsn 指向显式的 PostgreSQL URL")
		return 2
	}
	if !strings.HasPrefix(parsedDSN, "postgres://") && !strings.HasPrefix(parsedDSN, "postgresql://") {
		fmt.Fprintln(os.Stderr, "--dsn 必须是 postgres:// 或 postgresql:// URL")
		return 2
	}
	db, openErr := openPostgresBootstrap(parsedDSN)
	if openErr != nil {
		fmt.Fprintf(os.Stderr, "%v\n", openErr)
		return 2
	}
	defer db.Close()
	report, runErr := monitoringretirement.RunPostgres(context.Background(), db, time.Now().UTC())
	return dropRetiredMonitoringTablesOutcome(report, runErr)
}

// dropRetiredMonitoringTablesOutcome 渲染报告并映射退出码：运行失败 1（报告
// 已列出成功与缺席清单），成功 0。
func dropRetiredMonitoringTablesOutcome(report monitoringretirement.Report, runErr error) int {
	if runErr != nil {
		fmt.Fprintf(os.Stderr, "退役监控表清退失败：%v\n", runErr)
		return 1
	}
	if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
		fmt.Fprintf(os.Stderr, "encode drop-retired-monitoring-tables report: %v\n", err)
		return 1
	}
	fmt.Fprintln(os.Stderr, report.RollbackNote)
	if !report.Ready() {
		fmt.Fprintf(os.Stderr, "退役监控表清退未完成：%d 张表失败（%s）；修复后重跑继续收敛，报告已列出成功与缺席清单。\n",
			len(report.FailedTables), strings.Join(report.FailedTables, "、"))
		return 1
	}
	if report.AlreadyRetired {
		fmt.Fprintln(os.Stderr, "退役监控表已全部缺席（重复执行只报告，不写入）。")
	}
	return 0
}
