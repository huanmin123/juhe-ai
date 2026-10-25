// AI 账户能力域拆分（PLAN-20261011T141004000Z）maintenance 命令薄壳：
//   - --migrate-account-capability-domain：执行迁移（赋域 + 声明裁剪：把
//     ModesOK 行的 supported_endpoint_modes 裁剪至落域 lane 集，同语句回写
//     凭据；报告含脱敏裁剪明细）；必须显式携带
//     --account-capability-domain-stage=m1|m2-cutover 与停写/备份确认门禁
//     （--node-stopped --go-stopped --backup-confirmed），缺确认即拒绝；
//   - --verify-account-capability-domain：只读核验；必须显式携带
//     --account-capability-domain-stage 与 --account-capability-domain-check=
//     preflight|postflight（不根据列是否存在隐式切换核验语义）；
//   - 存储目标为 --dsn 单形态（PLAN-20261024T051925945Z B4 删除
//     --driver/--paths 旗标族）；解密密钥沿用 --secret → JUHE_AI_SECRET →
//     Node dev default 链。
//
// 退出码契约：0 成功（含 M1 重复执行的「只报告完成」）、1 运行失败（阶段
// 状态机拒绝、并行竞争、失败清单或核验不通过的运行期错误）、2 用法错误
// （门禁/旗标缺失或矛盾）、3 未就绪（报告 Ready=false：失败清单非空或
// postflight 核验不通过）。
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/capabilitydomain"
	capdomain "github.com/huanminabc/juhe-ai/backend-go-platform/capabilitydomain"
)

// openBusinessPostgres 打开 maintenance 业务库 PG 连接（显式 --dsn 契约，
// 门禁检查先于开库；能力域/studio 退场/调度偏好/merge 迁移共用同一连接
// 契约）。usageCode 非零时 db 为 nil。
func openBusinessPostgres(dsn string) (*sql.DB, int) {
	parsedDSN := strings.TrimSpace(dsn)
	if parsedDSN == "" {
		fmt.Fprintln(os.Stderr, "本命令需要 --dsn 指向显式的 PostgreSQL URL")
		return nil, 2
	}
	db, err := capabilitydomain.OpenPostgres(parsedDSN)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return nil, 2
	}
	return db, 0
}

// parseCapabilityDomainTarget 校验 --account-capability-domain-stage 取值。
func parseCapabilityDomainTarget(raw string) (capdomain.Target, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "m1":
		return capdomain.TargetM1, true
	case "m2-cutover":
		return capdomain.TargetM2Cutover, true
	}
	return "", false
}

// accountCapabilityDomainMigrateResult 返回 migrate 命令的 CLI 退出码；
// runMaintenance 派发它，测试进程内直调。门禁检查先于一切开库动作。
func accountCapabilityDomainMigrateResult(dsn, secret, stage string, nodeStopped, goStopped, backupConfirmed bool) int {
	target, ok := parseCapabilityDomainTarget(stage)
	if !ok {
		fmt.Fprintf(os.Stderr, "--migrate-account-capability-domain 需要 --account-capability-domain-stage=m1|m2-cutover（显式指定迁移目标，禁止隐式选择）\n")
		return 2
	}
	if !nodeStopped || !goStopped || !backupConfirmed {
		fmt.Fprintln(os.Stderr, "能力域迁移被拒绝：必须显式携带 --node-stopped --go-stopped --backup-confirmed（停写 gateway/jobs 并验证可恢复备份后才能执行；命令本身不负责停进程）")
		return 2
	}
	db, usageCode := openBusinessPostgres(dsn)
	if usageCode != 0 {
		return usageCode
	}
	defer db.Close()
	report, err := capabilitydomain.Run(context.Background(), db, capabilitydomain.Options{
		Target: target,
		Secret: capabilitydomain.ResolveSecret(secret),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "能力域迁移失败：%v\n", err)
		return 1
	}
	if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
		fmt.Fprintf(os.Stderr, "encode capability domain migration report: %v\n", err)
		return 1
	}
	if !report.Ready() {
		runningStage := capdomain.StageM1Running
		if target == capdomain.TargetM2Cutover {
			runningStage = capdomain.StageM2Running
		}
		fmt.Fprintf(os.Stderr, "能力域迁移未完成：失败清单 %d 项（保留 %s 阶段，已提交批次保留；修复后在同阶段重跑）。部分成功不满足上线门禁。\n", len(report.Failures), string(runningStage))
		return 3
	}
	return 0
}

// accountCapabilityDomainVerifyResult 返回 verify 命令的 CLI 退出码；
// runMaintenance 派发它，测试进程内直调。只读：全程零写入。
func accountCapabilityDomainVerifyResult(dsn, secret, stage, check string) int {
	target, ok := parseCapabilityDomainTarget(stage)
	if !ok {
		fmt.Fprintf(os.Stderr, "--verify-account-capability-domain 需要 --account-capability-domain-stage=m1|m2-cutover（显式指定迁移目标）\n")
		return 2
	}
	normalizedCheck := strings.ToLower(strings.TrimSpace(check))
	if !capabilitydomain.ValidCheck(normalizedCheck) {
		fmt.Fprintf(os.Stderr, "--verify-account-capability-domain 需要 --account-capability-domain-check=preflight|postflight（preflight 允许域列不存在；postflight 要求列存在；不隐式切换核验语义）\n")
		return 2
	}
	db, usageCode := openBusinessPostgres(dsn)
	if usageCode != 0 {
		return usageCode
	}
	defer db.Close()
	report, err := capabilitydomain.Verify(context.Background(), db, capabilitydomain.Options{
		Target: target,
		Check:  normalizedCheck,
		Secret: capabilitydomain.ResolveSecret(secret),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "能力域核验失败：%v\n", err)
		return 1
	}
	if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
		fmt.Fprintf(os.Stderr, "encode capability domain verify report: %v\n", err)
		return 1
	}
	if !report.Ready() {
		failures := report.Failures
		sort.Slice(failures, func(i, j int) bool { return failures[i].AccountID < failures[j].AccountID })
		fmt.Fprintf(os.Stderr, "能力域核验未通过：%d 项失败（报告只含账户 ID、域、计数与脱敏错误）。\n", len(failures))
		return 3
	}
	return 0
}
