// 调度偏好独立实体（PLAN-20261012T033000000Z）P2 迁移命令薄壳：
//   - --migrate-scheduling-preferences：执行迁移；必须显式携带停写/备份确认
//     门禁（--node-stopped --go-stopped --backup-confirmed），缺一即拒绝（先
//     例照能力域命令；命令本身不负责停进程）；
//   - --verify-scheduling-preferences：只读核验；必须显式携带
//     --scheduling-preferences-check=preflight|postflight（preflight=迁移前
//     只读报告，postflight=完成态核验；不根据记录是否存在隐式切换核验语义）；
//   - 存储目标为 --dsn 单形态（PLAN-20261024T051925945Z B4 删除
//     --driver/--paths 旗标族）；开库复用能力域命令同包函数
//     openBusinessPostgres（同一 maintenance 连接契约）。
//
// 退出码契约：0 成功（含重复执行的「只报告完成」与预检报告输出）、1 运行失
// 败（阶段记录损坏、并行竞争或库操作错误）、2 用法错误（门禁/旗标缺失或矛
// 盾）、3 未就绪（执行报告失败清单非空、残留未清或 postflight 核验不通过；
// 残留未清时拒绝写完成记录，同命令重跑续跑）。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/schedprefmigration"
)

// schedulingPreferencesMigrateResult 返回 migrate 命令的 CLI 退出码；
// runMaintenance 派发它，测试进程内直调。门禁检查先于一切开库动作。
func schedulingPreferencesMigrateResult(dsn string, nodeStopped, goStopped, backupConfirmed bool) int {
	if !nodeStopped || !goStopped || !backupConfirmed {
		fmt.Fprintln(os.Stderr, "调度偏好迁移被拒绝：必须显式携带 --node-stopped --go-stopped --backup-confirmed（停写 gateway/jobs 并验证可恢复备份后才能执行；命令本身不负责停进程）")
		return 2
	}
	db, usageCode := openBusinessPostgres(dsn)
	if usageCode != 0 {
		return usageCode
	}
	defer db.Close()
	report, err := schedprefmigration.Run(context.Background(), db, schedprefmigration.Options{})
	if err != nil {
		fmt.Fprintf(os.Stderr, "调度偏好迁移失败：%v\n", err)
		return 1
	}
	if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
		fmt.Fprintf(os.Stderr, "encode scheduling preferences migration report: %v\n", err)
		return 1
	}
	if !report.Ready() {
		fmt.Fprintf(os.Stderr, "调度偏好迁移未完成：失败清单 %d 项（完成记录未写入，已提交批次保留；修复后同命令重跑续跑）。部分成功不满足上线门禁。\n", len(report.Failures))
		return 3
	}
	return 0
}

// schedulingPreferencesVerifyResult 返回 verify 命令的 CLI 退出码；
// runMaintenance 派发它，测试进程内直调。只读：全程零写入。
func schedulingPreferencesVerifyResult(dsn, check string) int {
	normalizedCheck := strings.ToLower(strings.TrimSpace(check))
	if normalizedCheck != "preflight" && normalizedCheck != "postflight" {
		fmt.Fprintln(os.Stderr, "--verify-scheduling-preferences 需要 --scheduling-preferences-check=preflight|postflight（preflight=迁移前只读报告；postflight=完成态核验；不隐式切换核验语义）")
		return 2
	}
	db, usageCode := openBusinessPostgres(dsn)
	if usageCode != 0 {
		return usageCode
	}
	defer db.Close()
	if normalizedCheck == "preflight" {
		report, err := schedprefmigration.Preflight(context.Background(), db, schedprefmigration.Options{})
		if err != nil {
			fmt.Fprintf(os.Stderr, "调度偏好预检失败：%v\n", err)
			return 1
		}
		if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
			fmt.Fprintf(os.Stderr, "encode scheduling preferences preflight report: %v\n", err)
			return 1
		}
		if report.Blocking > 0 {
			fmt.Fprintf(os.Stderr, "调度偏好预检提示：存在 %d 项阻断迁移的坏数据（见 badConfigs 清单；坏数据阻断对应迁移，不自动成本化）。\n", report.Blocking)
		}
		return 0
	}
	report, err := schedprefmigration.Verify(context.Background(), db, schedprefmigration.Options{})
	if err != nil {
		fmt.Fprintf(os.Stderr, "调度偏好核验失败：%v\n", err)
		return 1
	}
	if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
		fmt.Fprintf(os.Stderr, "encode scheduling preferences verify report: %v\n", err)
		return 1
	}
	if !report.Ready() {
		fmt.Fprintf(os.Stderr, "调度偏好核验未通过：%d 项失败（完成记录缺失/未完成、NULL 残留或引用不完整）。\n", len(report.Failures))
		return 3
	}
	return 0
}
