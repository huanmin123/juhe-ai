// 单 IP 并发限制迁移命令薄壳（API Key 单 IP 并发限制设计 §4.2，revision 25）：
//   - migrate-key-client-ip-limit --dry-run --manifest-out <path>：只读盘点
//     api_keys / route_strategies / route_strategy_groups / groups /
//     super_group_members，把可复核 manifest 写到 --manifest-out（dry-run 唯一
//     写入）；needs_review/conflict 行只报告；
//   - migrate-key-client-ip-limit（不带 --dry-run/--check）：要求
//     --stop-write-confirmed --backup-confirmed，单业务库事务执行；存在
//     needs_review 行（畸形 JSON/未知键/Key 与策略或绑定 owner 不一致/成员
//     阈值不一致/多值冲突）即整体阻断，退出码 3，零写入；--conflict-policy=min
//     取最小值收敛多值冲突。跨 owner 的授权分组绑定属合法拓扑，不阻断。
//   - --check=preflight：迁移前只读盘点报告（不写 manifest）；
//   - --check=postflight：完成态核验（分组策略无 clientIpConcurrencyLimit、
//     所有 Key 值在 0..1000000、超级分组自身行一致），未就绪退出码 3。
//
// 退出码契约：0 成功（含 dry-run/preflight/postflight 通过）、1 运行失败、
// 2 用法错误、3 needs_review 阻断或 postflight 未就绪。
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

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/keyipmigration"
)

func runMigrateKeyClientIPLimitCommand(argv []string) int {
	fs := flag.NewFlagSet("juhe-ai-maintenance migrate-key-client-ip-limit", flag.ContinueOnError)
	dsn := fs.String("dsn", "", "postgres URL of the business database")
	dryRun := fs.Bool("dry-run", false, "read-only inventory: write the reviewable manifest to --manifest-out (the only dry-run write) and change nothing")
	manifestOut := fs.String("manifest-out", "", "manifest JSON output path (required with --dry-run; optional for apply archiving)")
	conflictPolicy := fs.String("conflict-policy", string(keyipmigration.ConflictPolicyBlock), "how to resolve a Key reaching multiple groups with different old clientIpConcurrencyLimit values: block (default: needs_review/exit 3) or min (take the minimum and record conflicts)")
	check := fs.String("check", "", "read-only verification mode: preflight (pre-migration inventory report) or postflight (migrated-state check)")
	stopWriteConfirmed := fs.Bool("stop-write-confirmed", false, "confirm gateway/jobs writers are stopped for the offline migration (required for apply)")
	backupConfirmed := fs.Bool("backup-confirmed", false, "confirm a recoverable backup was verified (required for apply)")
	if err := fs.Parse(argv); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	parsedDSN := strings.TrimSpace(*dsn)
	if parsedDSN == "" {
		fmt.Fprintln(os.Stderr, "migrate-key-client-ip-limit 需要 --dsn 指向显式的 PostgreSQL URL")
		return 2
	}
	policy, ok := keyipmigration.NormalizeConflictPolicy(*conflictPolicy)
	if !ok {
		fmt.Fprintln(os.Stderr, "--conflict-policy 只接受 block|min（block=多值冲突 needs_review 阻断；min=取最小值并记录冲突）")
		return 2
	}
	normalizedCheck := strings.ToLower(strings.TrimSpace(*check))
	switch normalizedCheck {
	case "", "preflight", "postflight":
	default:
		fmt.Fprintln(os.Stderr, "--check 只接受 preflight|postflight（preflight=迁移前盘点报告；postflight=完成态核验）")
		return 2
	}
	options := keyipmigration.Options{ConflictPolicy: policy}

	// 校验模式先于 dry-run/apply：preflight/postflight 全程只读。
	if normalizedCheck == "preflight" {
		return migrateKeyClientIPLimitPreflight(parsedDSN, options)
	}
	if normalizedCheck == "postflight" {
		return migrateKeyClientIPLimitPostflight(parsedDSN)
	}

	if *dryRun {
		outPath := strings.TrimSpace(*manifestOut)
		if outPath == "" {
			fmt.Fprintln(os.Stderr, "--dry-run 需要 --manifest-out 指向 manifest 输出文件")
			return 2
		}
		options.DryRun = true
		return migrateKeyClientIPLimitDryRun(parsedDSN, options, outPath)
	}
	if !*stopWriteConfirmed || !*backupConfirmed {
		fmt.Fprintln(os.Stderr, "正式执行要求 --stop-write-confirmed --backup-confirmed（停写并验证可恢复备份后执行；命令本身不负责停进程）")
		return 2
	}
	options.DryRun = false
	return migrateKeyClientIPLimitApply(parsedDSN, options, strings.TrimSpace(*manifestOut))
}

// writeKeyClientIPLimitManifest 渲染并写出 manifest（dry-run 唯一写入；apply
// 可选留档）。失败只报错误，不半写（WriteFile 覆盖前内容来自内存 buffer）。
func writeKeyClientIPLimitManifest(outPath string, plan keyipmigration.Plan) error {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(plan); err != nil {
		return fmt.Errorf("encode key client ip limit manifest: %w", err)
	}
	if err := os.WriteFile(outPath, buffer.Bytes(), 0o644); err != nil {
		return fmt.Errorf("写 manifest %s 失败: %w", outPath, err)
	}
	return nil
}

// logKeyClientIPLimitSummary 把盘点摘要打到 stderr（stdout 留给 JSON 报告）。
func logKeyClientIPLimitSummary(plan keyipmigration.Plan) {
	fmt.Fprintf(os.Stderr, "[migrate-key-client-ip-limit] 库 %s dryRun=%t conflictPolicy=%s keyColumn=%t keys=%d groups=%d superGroups=%d\n",
		plan.Database, plan.DryRun, plan.ConflictPolicy, plan.KeyColumnPresent,
		plan.KeysTotal, plan.GroupsTotal, plan.SuperGroupsTotal)
	fmt.Fprintf(os.Stderr, "[migrate-key-client-ip-limit] 计划 keyUpdate=%d preexisting=%d unchanged=%d needsReview=%d applied=%d conflicts=%d groupRewrites=%d superRecomputed=%d emptySuper=%d\n",
		plan.KeysUpdated, plan.KeysPreexisting, plan.KeysUnchanged, plan.Blocked, plan.Applied,
		len(plan.Conflicts), plan.GroupsRewritten, plan.SuperGroupsRecomputed, plan.EmptySuperGroups)
	for _, conflict := range plan.Conflicts {
		fmt.Fprintf(os.Stderr, "conflict: key=%s current=%d observed=%v chosen=%d kind=%s resolution=%s\n",
			conflict.KeyID, conflict.CurrentValue, conflict.ObservedValues, conflict.ChosenValue,
			conflict.Kind, conflict.Resolution)
	}
	for _, review := range plan.NeedsReview {
		fmt.Fprintf(os.Stderr, "needs_review: %s.%s reason=%s %s\n",
			review.Target, review.ID, review.Reason, review.Detail)
	}
}

// migrateKeyClientIPLimitPreflight 输出迁移前盘点报告（全程只读；needs_review
// 行只报告，预检本身仍以退出码 0 完成）。
func migrateKeyClientIPLimitPreflight(dsn string, options keyipmigration.Options) int {
	db, err := openPostgresBootstrap(dsn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 2
	}
	defer db.Close()
	plan, err := keyipmigration.BuildPlan(context.Background(), db, options)
	if err != nil {
		fmt.Fprintf(os.Stderr, "单 IP 并发限制迁移预检失败: %v\n", err)
		return 1
	}
	if err := json.NewEncoder(os.Stdout).Encode(plan); err != nil {
		fmt.Fprintf(os.Stderr, "encode key client ip limit preflight report: %v\n", err)
		return 1
	}
	logKeyClientIPLimitSummary(plan)
	if plan.Blocked > 0 {
		fmt.Fprintf(os.Stderr, "[migrate-key-client-ip-limit] 预检提示：存在 %d 行 needs_review（见 needsReview 明细；apply 阶段将被阻断，人工确认后再迁移）。\n", plan.Blocked)
	}
	return 0
}

// migrateKeyClientIPLimitPostflight 输出完成态核验报告（全程只读；未就绪
// 退出码 3）。
func migrateKeyClientIPLimitPostflight(dsn string) int {
	db, err := openPostgresBootstrap(dsn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 2
	}
	defer db.Close()
	report, err := keyipmigration.VerifyPostflight(context.Background(), db)
	if err != nil {
		fmt.Fprintf(os.Stderr, "单 IP 并发限制迁移 postflight 核验失败: %v\n", err)
		return 1
	}
	if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
		fmt.Fprintf(os.Stderr, "encode key client ip limit postflight report: %v\n", err)
		return 1
	}
	if !report.Ready() {
		fmt.Fprintf(os.Stderr, "单 IP 并发限制迁移 postflight 未就绪：列存在=%t、旧键分组 %d、非法分组策略 %d、非法 Key 值 %d、超级分组不一致 %d。\n",
			report.KeyColumnPresent, report.LegacyIPKeyGroups, report.InvalidPolicyGroups,
			report.InvalidKeyLimitValues, report.SuperGroupMismatches)
		return 3
	}
	return 0
}

// migrateKeyClientIPLimitDryRun 盘点并写出 manifest（dry-run 唯一写入）；库内
// 零写入。
func migrateKeyClientIPLimitDryRun(dsn string, options keyipmigration.Options, outPath string) int {
	db, err := openPostgresBootstrap(dsn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 2
	}
	defer db.Close()
	plan, err := keyipmigration.BuildPlan(context.Background(), db, options)
	if err != nil {
		fmt.Fprintf(os.Stderr, "单 IP 并发限制迁移盘点失败: %v\n", err)
		return 1
	}
	if err := writeKeyClientIPLimitManifest(outPath, plan); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 1
	}
	fmt.Printf("dry-run manifest 已写入 %s (conflictPolicy=%s, keyUpdate=%d, preexisting=%d, unchanged=%d, needsReview=%d)\n",
		outPath, plan.ConflictPolicy, plan.KeysUpdated, plan.KeysPreexisting, plan.KeysUnchanged, plan.Blocked)
	logKeyClientIPLimitSummary(plan)
	return 0
}

// migrateKeyClientIPLimitApply 正式执行：needs_review 行阻断（退出码 3，零写入），
// 其余在单事务内迁移；报告 JSON 走 stdout，可选 manifest 留档。
func migrateKeyClientIPLimitApply(dsn string, options keyipmigration.Options, manifestOut string) int {
	db, err := openPostgresBootstrap(dsn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 2
	}
	defer db.Close()
	plan, err := keyipmigration.Apply(context.Background(), db, options)
	if err != nil && !errors.Is(err, keyipmigration.ErrNeedsReview) {
		fmt.Fprintf(os.Stderr, "单 IP 并发限制迁移失败: %v\n", err)
		return 1
	}
	if manifestOut != "" {
		if writeErr := writeKeyClientIPLimitManifest(manifestOut, plan); writeErr != nil {
			fmt.Fprintf(os.Stderr, "%v\n", writeErr)
			return 1
		}
	}
	encodeErr := json.NewEncoder(os.Stdout).Encode(plan)
	if encodeErr != nil {
		fmt.Fprintf(os.Stderr, "encode key client ip limit migration report: %v\n", encodeErr)
		return 1
	}
	logKeyClientIPLimitSummary(plan)
	if errors.Is(err, keyipmigration.ErrNeedsReview) {
		fmt.Fprintf(os.Stderr, "单 IP 并发限制迁移被阻断：%d 行 needs_review（未写入任何数据；人工确认后重跑，或使用 --conflict-policy=min 收敛多值冲突）。\n", plan.Blocked)
		return 3
	}
	if plan.Applied == 0 {
		fmt.Fprintf(os.Stderr, "[migrate-key-client-ip-limit] 完成态幂等重跑：0 行写入。\n")
	}
	return 0
}
