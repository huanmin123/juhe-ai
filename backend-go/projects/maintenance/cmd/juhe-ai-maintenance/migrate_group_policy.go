// 分组调度策略迁移命令薄壳（普通分组可选调度策略设计 §5，T195 阶段③）：
//   - migrate-group-policy --dry-run --manifest-out <path>：只读盘点
//     juhe_business.groups 与 group_authorization_settings，把可复核 manifest
//     写到 --manifest-out（dry-run 唯一写入）；needs_review 行只报告不阻断；
//   - migrate-group-policy（不带 --dry-run）：要求 --node-stopped --go-stopped
//     --backup-confirmed，单业务库事务执行；存在 needs_review 行（畸形 JSON/
//     未知键/未知 group_type）即整体阻断，退出码 3，不静默补默认（契约 §5.2）；
//   - --check=preflight：迁移前只读盘点报告（语义同 dry-run 但不写 manifest）；
//     --check=postflight：完成态核验（两表无 personal 之外的 group_type、策略
//     列只含两键或 NULL），未就绪退出码 3；不根据数据状态隐式切换核验语义。
//
// 退出码契约：0 成功（含 dry-run 与 preflight 报告输出）、1 运行失败、
// 2 用法/门禁错误、3 needs_review 阻断或 postflight 未就绪。
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/groupspolicymigration"
)

func runMigrateGroupPolicyCommand(argv []string) int {
	fs := flag.NewFlagSet("juhe-ai-maintenance migrate-group-policy", flag.ContinueOnError)
	dsn := fs.String("dsn", "", "postgres URL of the business database")
	dryRun := fs.Bool("dry-run", false, "read-only inventory: write the reviewable manifest to --manifest-out (the only dry-run write) and change nothing")
	manifestOut := fs.String("manifest-out", "", "manifest JSON output path (required with --dry-run)")
	globalMax := fs.Int("global-max", groupspolicymigration.DefaultGlobalConcurrencyMax, "legacy JUHE_AI_CONCURRENCY_GLOBAL_MAX value the old binary ran with; thresholds in [1, min(1000000, --global-max)) are kept, others map to -1")
	nodeStopped := fs.Bool("node-stopped", false, "confirm Node writers are stopped for the offline migration")
	goStopped := fs.Bool("go-stopped", false, "confirm Go owners are stopped for the offline migration")
	backupConfirmed := fs.Bool("backup-confirmed", false, "confirm a recoverable backup was verified")
	check := fs.String("check", "", "read-only verification mode: preflight (pre-migration inventory report) or postflight (migrated-state check)")
	if err := fs.Parse(argv); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	parsedDSN := strings.TrimSpace(*dsn)
	if parsedDSN == "" {
		fmt.Fprintln(os.Stderr, "migrate-group-policy 需要 --dsn 指向显式的 PostgreSQL URL")
		return 2
	}
	normalizedCheck := strings.ToLower(strings.TrimSpace(*check))
	switch normalizedCheck {
	case "", "preflight", "postflight":
	default:
		fmt.Fprintln(os.Stderr, "--check 只接受 preflight|postflight（preflight=迁移前盘点报告；postflight=完成态核验）")
		return 2
	}
	if *globalMax < 1 || *globalMax > groupspolicymigration.MaxGlobalConcurrencyMax {
		fmt.Fprintf(os.Stderr, "--global-max 必须在 1-%d 范围内（收到 %d）\n", groupspolicymigration.MaxGlobalConcurrencyMax, *globalMax)
		return 2
	}
	options := groupspolicymigration.Options{GlobalMax: *globalMax}

	// 校验模式先于门禁：preflight/postflight 全程只读，不要求停写/备份确认。
	if normalizedCheck == "preflight" {
		return migrateGroupPolicyPreflight(parsedDSN, options)
	}
	if normalizedCheck == "postflight" {
		return migrateGroupPolicyPostflight(parsedDSN)
	}

	if *dryRun {
		outPath := strings.TrimSpace(*manifestOut)
		if outPath == "" {
			fmt.Fprintln(os.Stderr, "--dry-run 需要 --manifest-out 指向 manifest 输出文件")
			return 2
		}
		return migrateGroupPolicyDryRun(parsedDSN, options, outPath)
	}
	if !*nodeStopped || !*goStopped || !*backupConfirmed {
		fmt.Fprintln(os.Stderr, "正式执行要求 --node-stopped --go-stopped --backup-confirmed（停写并验证可恢复备份后才能执行；命令本身不负责停进程）")
		return 2
	}
	return migrateGroupPolicyApply(parsedDSN, options)
}

// writeGroupPolicyManifest 渲染并写出 manifest（dry-run 唯一写入；apply 留档
// 可选）。失败只报错误，不半写（WriteFile 覆盖前内容来自内存 buffer）。
func writeGroupPolicyManifest(outPath string, plan groupspolicymigration.Plan) error {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(plan); err != nil {
		return fmt.Errorf("encode group policy manifest: %w", err)
	}
	if err := os.WriteFile(outPath, buffer.Bytes(), 0o644); err != nil {
		return fmt.Errorf("写 manifest %s 失败: %w", outPath, err)
	}
	return nil
}

// logGroupPolicySummary 把盘点摘要打到 stderr（stdout 留给 JSON 报告）。
func logGroupPolicySummary(plan groupspolicymigration.Plan) {
	fmt.Fprintf(os.Stderr, "[migrate-group-policy] 库 %s globalMax=%d groups=%d authSettings=%d highConcurrency(groups=%d auth=%d) unknownType=%d superGroups=%d queueOverflow=%d imageLane=%d\n",
		plan.Database, plan.GlobalMax, plan.GroupsTotal, plan.AuthSettingsTotal,
		plan.HighConcurrencyGroups, plan.HighConcurrencyAuthSettings,
		plan.UnknownGroupTypeRows, plan.SuperGroups, plan.QueueOverflowRows, plan.ImageLaneRows)
	fmt.Fprintf(os.Stderr, "[migrate-group-policy] 计划 update=%d unchanged=%d needsReview=%d applied=%d（defaultSoftConcurrency 目标分布 %v；单 IP 并发目标分布 %v）\n",
		plan.Mapped, plan.Unchanged, plan.Blocked, plan.Applied,
		plan.DefaultSoftDistribution, plan.ClientIPLimitDistribution)
	for _, review := range plan.NeedsReview {
		fmt.Fprintf(os.Stderr, "needs_review: %s.%s type=%s reason=%s\n", review.Table, review.ID, review.CurrentType, review.Reason)
	}
}

// migrateGroupPolicyPreflight 输出迁移前盘点报告（全程只读；needs_review 行只
// 报告，预检本身仍以退出码 0 完成）。
func migrateGroupPolicyPreflight(dsn string, options groupspolicymigration.Options) int {
	db, err := openPostgresBootstrap(dsn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 2
	}
	defer db.Close()
	plan, err := groupspolicymigration.BuildPlan(context.Background(), db, options)
	if err != nil {
		fmt.Fprintf(os.Stderr, "分组调度策略预检失败: %v\n", err)
		return 1
	}
	if err := json.NewEncoder(os.Stdout).Encode(plan); err != nil {
		fmt.Fprintf(os.Stderr, "encode group policy preflight report: %v\n", err)
		return 1
	}
	logGroupPolicySummary(plan)
	if plan.Blocked > 0 {
		fmt.Fprintf(os.Stderr, "[migrate-group-policy] 预检提示：存在 %d 行 needs_review（见 needsReview 明细；apply 阶段将被阻断，人工确认后再迁移）。\n", plan.Blocked)
	}
	return 0
}

// migrateGroupPolicyPostflight 输出完成态核验报告（全程只读；未就绪退出码 3）。
func migrateGroupPolicyPostflight(dsn string) int {
	db, err := openPostgresBootstrap(dsn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 2
	}
	defer db.Close()
	report, err := groupspolicymigration.VerifyPostflight(context.Background(), db)
	if err != nil {
		fmt.Fprintf(os.Stderr, "分组调度策略 postflight 核验失败: %v\n", err)
		return 1
	}
	if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
		fmt.Fprintf(os.Stderr, "encode group policy postflight report: %v\n", err)
		return 1
	}
	if !report.Ready() {
		fmt.Fprintf(os.Stderr, "分组调度策略 postflight 未就绪：groups 非 personal 行 %d、授权设置非 personal 行 %d、策略列违规（groups %d / 授权设置 %d）。\n",
			report.NonPersonalGroups, report.NonPersonalAuthSettings, report.InvalidPolicyGroups, report.InvalidPolicyAuthSettings)
		return 3
	}
	return 0
}

// migrateGroupPolicyDryRun 盘点并写出 manifest（dry-run 唯一写入）；库内零写入。
func migrateGroupPolicyDryRun(dsn string, options groupspolicymigration.Options, outPath string) int {
	db, err := openPostgresBootstrap(dsn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 2
	}
	defer db.Close()
	plan, err := groupspolicymigration.BuildPlan(context.Background(), db, options)
	if err != nil {
		fmt.Fprintf(os.Stderr, "分组调度策略盘点失败: %v\n", err)
		return 1
	}
	if err := writeGroupPolicyManifest(outPath, plan); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 1
	}
	fmt.Printf("dry-run manifest 已写入 %s (globalMax=%d, update=%d, unchanged=%d, needsReview=%d)\n",
		outPath, plan.GlobalMax, plan.Mapped, plan.Unchanged, plan.Blocked)
	logGroupPolicySummary(plan)
	return 0
}

// migrateGroupPolicyApply 正式执行：needs_review 行阻断（退出码 3，零写入），
// 其余在单事务内迁移；报告 JSON 走 stdout。
func migrateGroupPolicyApply(dsn string, options groupspolicymigration.Options) int {
	db, err := openPostgresBootstrap(dsn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 2
	}
	defer db.Close()
	plan, err := groupspolicymigration.Apply(context.Background(), db, options)
	if err != nil && !errors.Is(err, groupspolicymigration.ErrNeedsReview) {
		fmt.Fprintf(os.Stderr, "分组调度策略迁移失败: %v\n", err)
		return 1
	}
	encodeErr := json.NewEncoder(os.Stdout).Encode(plan)
	if encodeErr != nil {
		fmt.Fprintf(os.Stderr, "encode group policy migration report: %v\n", encodeErr)
		return 1
	}
	logGroupPolicySummary(plan)
	if errors.Is(err, groupspolicymigration.ErrNeedsReview) {
		fmt.Fprintf(os.Stderr, "分组调度策略迁移被阻断：%d 行 needs_review（未写入任何数据；人工确认后重跑）。\n", plan.Blocked)
		return 3
	}
	if plan.Applied == 0 {
		fmt.Fprintf(os.Stderr, "[migrate-group-policy] 完成态幂等重跑：0 行写入。\n")
	}
	return 0
}
