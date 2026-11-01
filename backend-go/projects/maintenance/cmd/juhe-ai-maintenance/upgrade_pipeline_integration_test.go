// 真库集成测试（P1 完成判定证据，升级兼容与迁移账本设计 §17/§18）：在
// pgtestkit 隔离空库上走完整 upgrade 路径——dry-run 预演 → 确认门禁 → 全量
// 升级 → 重跑短路。外部依赖授权门由 pgtestkit 承担（未授权 SKIP）。
package main

import (
	"context"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-contracts"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/upgradepipeline"
	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

func TestUpgradeFreshDatabasePipeline(t *testing.T) {
	pgtestkit.SkipUnlessExternalAuthorized(t)
	td := pgtestkit.NewTestDatabase(t)
	// 防跨用例污染：upgradeSteps 的 upgradeTargets 会把缺失的 J3a/J3b/balance
	// 连接串写回进程环境（生产语义：缺省回落 primary）。临时库在本用例结束时
	// 被删除，泄漏的 DSN 会让同包后续用例（如 TestW1J2BootstrapMutualExclusion
	// 的互斥负臂）拿到已删除库、在互斥门之前就以运行时失败 exit 1 收场。
	// t.Setenv 显式占位并在用例结束自动还原。
	t.Setenv(envJ3AURL, td.DSN)
	t.Setenv(envJ3BURL, td.DSN)
	t.Setenv(envBalanceURL, td.DSN)
	const testSecret = "upgrade-integration-secret"

	newOpts := func(dryRun, stopWrite, backup bool) upgradepipeline.Options {
		return upgradepipeline.Options{
			PrimaryDSN:         td.DSN,
			Secret:             testSecret,
			DryRun:             dryRun,
			StopWriteConfirmed: stopWrite,
			BackupConfirmed:    backup,
			StepTimeout:        4 * time.Minute,
			AppVersion:         "dev-integration-test",
		}
	}
	steps := func() []upgradepipeline.Step {
		return upgradeSteps(td.DSN, testSecret)
	}

	// 1) 落后库 dry-run：blocked/3，pending 应为 7 步 = 注册表 12 步中排除
	// fresh-adopt 的 5 步（rev8 upstream-base-url 空库无行、rev10 studio 无遗留、
	// rev11 退役监控表不存在、rev12 无系统账户即满足、rev13 空库无
	// active+schedulable=0 行——各自 Satisfied 在空库按目标态判据即为真，见
	// upgrade_steps.go 各步注释）。revision 7 chat-account-only-binding 已随
	// AI 问答退役删除，revision 序列 6 后直接 8。
	out := upgradepipeline.Run(context.Background(), newOpts(true, false, false), steps())
	if out.ExitCode != upgradepipeline.ExitNeedsHuman || out.Report == nil || out.Report.Result != upgradepipeline.ResultBlocked {
		t.Fatalf("dry-run 应 blocked/3，got code=%d result=%v", out.ExitCode, reportResult(out.Report))
	}
	if len(out.Report.Plan.Pending) != 7 {
		t.Fatalf("fresh 库 pending 步骤数 = %d, want 7（11 步 - 4 步 fresh-adopt）", len(out.Report.Plan.Pending))
	}

	// 2) 缺确认：exit 2。
	out = upgradepipeline.Run(context.Background(), newOpts(false, false, false), steps())
	if out.ExitCode != upgradepipeline.ExitUsage {
		t.Fatalf("缺确认应 exit 2，got %d（stderr: %s）", out.ExitCode, out.Stderr)
	}

	// 3) 完整升级：exit 0 / upgraded / postflight OK。
	out = upgradepipeline.Run(context.Background(), newOpts(false, true, true), steps())
	if out.ExitCode != upgradepipeline.ExitOK {
		t.Fatalf("fresh 全量升级应 exit 0，got %d（stderr: %s）", out.ExitCode, out.Stderr)
	}
	if out.Report == nil || out.Report.Result != upgradepipeline.ResultUpgraded || !out.Report.Postflight.OK {
		t.Fatalf("升级报告不符: result=%v postflight=%v",
			reportResult(out.Report), reportPostflightOK(out.Report))
	}

	// 4) 账本：12 行全 applied（rev7 chat-account-only-binding 已随退役删除，
	// 注册表 12 步）；schema_state revision = SchemaRevision。
	db := td.DB
	var applied int
	if err := db.QueryRowContext(context.Background(),
		`SELECT count(*) FROM juhe_business.schema_migrations WHERE status = 'applied'`).Scan(&applied); err != nil {
		t.Fatalf("读账本失败: %v", err)
	}
	if applied != 12 {
		t.Fatalf("applied 行数 = %d, want 12", applied)
	}
	var stateRevision int64
	if err := db.QueryRowContext(context.Background(),
		`SELECT schema_revision FROM juhe_business.schema_state WHERE id = 1`).Scan(&stateRevision); err != nil {
		t.Fatalf("读 schema_state 失败: %v", err)
	}
	if stateRevision != contracts.SchemaRevision {
		t.Fatalf("schema_state.revision = %d, want %d", stateRevision, contracts.SchemaRevision)
	}

	// 5) 重跑短路：up-to-date/0，账本零新增。
	out = upgradepipeline.Run(context.Background(), newOpts(false, false, false), steps())
	if out.ExitCode != upgradepipeline.ExitOK || out.Report.Result != upgradepipeline.ResultUpToDate {
		t.Fatalf("重跑应 up-to-date/0，got code=%d result=%v（stderr: %s）",
			out.ExitCode, reportResult(out.Report), out.Stderr)
	}
	if len(out.Report.Plan.Pending) != 0 {
		t.Fatalf("up-to-date 后 pending 应为空，got %d", len(out.Report.Plan.Pending))
	}
}

func reportResult(report *upgradepipeline.Report) string {
	if report == nil {
		return "<nil report>"
	}
	return report.Result
}

func reportPostflightOK(report *upgradepipeline.Report) bool {
	if report == nil || report.Postflight == nil {
		return false
	}
	return report.Postflight.OK
}
