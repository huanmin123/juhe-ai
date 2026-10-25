// studio 退场显式维护命令薄壳（studio 退场 schema 契约 2026-10-14）：
//   - --apply-studio-retirement：清理存量 studio 工件（4 张真实 studio 表、
//     idx_api_keys_studio_purpose_unique、purpose='studio' 的 api_keys 行、
//     api_keys.studio_domains_json 列）；必须显式携带停写/备份确认门禁
//     （--node-stopped --go-stopped --backup-confirmed），缺一即拒绝（先例照
//     调度偏好/能力域命令；命令本身不负责停进程）。普通 --ensure-schema 保持
//     幂等加法，破坏性清理只存在于本命令；
//   - 存储目标为 --dsn 单形态（PLAN-20261024T051925945Z B4 删除
//     --driver/--paths 旗标族，business 单库契约）。
//
// 退出码契约：0 成功（含重复执行的「只报告已退场」）、1 运行失败、2 用法错
// 误（门禁/旗标缺失或矛盾）、3 未就绪（后检仍有 studio 残留；同命令重跑继
// 续收敛）。回滚 = 用 --backup-confirmed 验证过的外部备份整体恢复业务库，
// 报告 JSON 的 rollbackNote 明示（DELETE/DROP 无备份表可回填，不携带回滚
// SQL）；回滚指引同时落在 stderr，stdout 保持单条 Report JSON。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/studioretirement"
)

// studioRetirementResult 返回 --apply-studio-retirement 的 CLI 退出码；
// runMaintenance 派发它，测试进程内直调。门禁检查先于一切开库动作。
func studioRetirementResult(dsn string, nodeStopped, goStopped, backupConfirmed bool) int {
	if !nodeStopped || !goStopped || !backupConfirmed {
		fmt.Fprintln(os.Stderr, "studio 退场清理被拒绝：必须显式携带 --node-stopped --go-stopped --backup-confirmed（停写 gateway/jobs 并验证可恢复备份后才能执行；命令本身不负责停进程）")
		return 2
	}
	db, usageCode := openBusinessPostgres(dsn)
	if usageCode != 0 {
		return usageCode
	}
	defer db.Close()
	report, err := studioretirement.Run(context.Background(), db, time.Now().UTC())
	if err != nil {
		fmt.Fprintf(os.Stderr, "studio 退场清理失败：%v\n", err)
		return 1
	}
	if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
		fmt.Fprintf(os.Stderr, "encode studio retirement report: %v\n", err)
		return 1
	}
	fmt.Fprintln(os.Stderr, report.RollbackNote)
	if !report.Ready() {
		fmt.Fprintf(os.Stderr, "studio 退场清理未完成：后检仍有残留（见报告 remaining* 字段）；同命令重跑继续收敛。\n")
		return 3
	}
	return 0
}
