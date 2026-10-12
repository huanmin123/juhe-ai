// AI 账户能力域拆分（PLAN-20261011T141004000Z）maintenance 命令薄壳：
//   - --migrate-account-capability-domain：执行迁移；必须显式携带
//     --account-capability-domain-stage=m1|m2-cutover 与停写/备份确认门禁
//     （--node-stopped --go-stopped --backup-confirmed），缺确认即拒绝；
//   - --verify-account-capability-domain：只读核验；必须显式携带
//     --account-capability-domain-stage 与 --account-capability-domain-check=
//     preflight|postflight（不根据列是否存在隐式切换核验语义）；
//   - 存储目标沿用既有 --driver sqlite|postgres + --paths/--dsn 旗标风格
//     （SQLite 只消费 business 键，其余键接受并忽略，便于复用 --ensure-schema
//     的完整 --paths 参数）；解密密钥沿用 --secret → JUHE_AI_SECRET →
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

// parseCapabilityDomainBusinessPath 解析 --paths（键词汇与六库 bootstrap 相同，
// 能力域命令只读写业务库，business 为唯一必填键；其余键接受并忽略）。
func parseCapabilityDomainBusinessPath(raw string) (string, error) {
	business := ""
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
			return "", fmt.Errorf("--paths 条目必须是 key=value 形式：%q", entry)
		}
		if seen[key] {
			return "", fmt.Errorf("--paths 条目重复：%q", key)
		}
		seen[key] = true
		if key == "business" {
			business = value
		}
	}
	if business == "" {
		return "", fmt.Errorf("--paths 缺少必填 key：business（能力域迁移/核验只读写业务库）")
	}
	return business, nil
}

// openCapabilityDomainDB 按 driver 打开业务库连接（门禁检查先于开库）。
func openCapabilityDomainDB(driver, paths, dsn string) (*sql.DB, capdomain.Dialect, int) {
	driver = strings.ToLower(strings.TrimSpace(driver))
	if driver != "sqlite" && driver != "postgres" {
		fmt.Fprintf(os.Stderr, "--driver 必须为 sqlite 或 postgres: %q\n", driver)
		return nil, 0, 2
	}
	if driver == "sqlite" && strings.TrimSpace(dsn) != "" {
		fmt.Fprintln(os.Stderr, "--dsn 只适用于 --driver postgres；sqlite 模式使用 --paths")
		return nil, 0, 2
	}
	if driver == "postgres" && strings.TrimSpace(paths) != "" {
		fmt.Fprintln(os.Stderr, "--paths 只适用于 --driver sqlite；postgres 模式使用 --dsn")
		return nil, 0, 2
	}
	if driver == "sqlite" {
		businessPath, err := parseCapabilityDomainBusinessPath(paths)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			return nil, 0, 2
		}
		db, err := capabilitydomain.OpenSQLite(businessPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			return nil, 0, 2
		}
		return db, capdomain.DialectSQLite, 0
	}
	parsedDSN := strings.TrimSpace(dsn)
	if parsedDSN == "" {
		fmt.Fprintln(os.Stderr, "--driver postgres 需要 --dsn 指向显式的 PostgreSQL URL")
		return nil, 0, 2
	}
	db, err := capabilitydomain.OpenPostgres(parsedDSN)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return nil, 0, 2
	}
	return db, capdomain.DialectPostgres, 0
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
func accountCapabilityDomainMigrateResult(driver, paths, dsn, secret, stage string, nodeStopped, goStopped, backupConfirmed bool) int {
	target, ok := parseCapabilityDomainTarget(stage)
	if !ok {
		fmt.Fprintf(os.Stderr, "--migrate-account-capability-domain 需要 --account-capability-domain-stage=m1|m2-cutover（显式指定迁移目标，禁止隐式选择）\n")
		return 2
	}
	if !nodeStopped || !goStopped || !backupConfirmed {
		fmt.Fprintln(os.Stderr, "能力域迁移被拒绝：必须显式携带 --node-stopped --go-stopped --backup-confirmed（停写 gateway/jobs 并验证可恢复备份后才能执行；命令本身不负责停进程）")
		return 2
	}
	db, dialect, usageCode := openCapabilityDomainDB(driver, paths, dsn)
	if usageCode != 0 {
		return usageCode
	}
	defer db.Close()
	report, err := capabilitydomain.Run(context.Background(), db, capabilitydomain.Options{
		Dialect: dialect,
		Target:  target,
		Secret:  capabilitydomain.ResolveSecret(secret),
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
func accountCapabilityDomainVerifyResult(driver, paths, dsn, secret, stage, check string) int {
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
	db, dialect, usageCode := openCapabilityDomainDB(driver, paths, dsn)
	if usageCode != 0 {
		return usageCode
	}
	defer db.Close()
	report, err := capabilitydomain.Verify(context.Background(), db, capabilitydomain.Options{
		Dialect: dialect,
		Target:  target,
		Check:   normalizedCheck,
		Secret:  capabilitydomain.ResolveSecret(secret),
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
