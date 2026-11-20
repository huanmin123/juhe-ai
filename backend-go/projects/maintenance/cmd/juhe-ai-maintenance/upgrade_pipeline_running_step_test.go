// 真库集成测试：expand 对「数据已提交但账本未 MarkApplied」的启动前必需
// 破坏步骤必须 blocked。该形态来自 all/contract 在 rev25 数据事务提交后、
// MarkApplied 前崩溃：Satisfied=true，但账本残留 running+destructive；若
// expand 静默跳过并推进 schema_state，schemagate 会拒绝所有二进制。
package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/upgradepipeline"
	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

func TestUpgradeExpandBlocksInterruptedRequiredDestructiveStep(t *testing.T) {
	pgtestkit.SkipUnlessExternalAuthorized(t)
	td := pgtestkit.NewTestDatabase(t)
	const testSecret = "upgrade-running-step-secret"
	steps := func() []upgradepipeline.Step {
		return upgradeSteps(td.DSN, testSecret)
	}
	base := upgradepipeline.Options{
		PrimaryDSN:         td.DSN,
		Secret:             testSecret,
		StopWriteConfirmed: true,
		BackupConfirmed:    true,
		StepTimeout:        4 * time.Minute,
		AppVersion:         "dev-running-step-test",
	}

	// 先完成一次全量升级，构造 rev25 已满足的数据形态。
	out := upgradepipeline.Run(context.Background(), base, steps())
	if out.ExitCode != upgradepipeline.ExitOK {
		t.Fatalf("初始全量升级应 exit 0，got %d（stderr: %s）", out.ExitCode, out.Stderr)
	}

	// 模拟 rev25 数据已提交、MarkApplied 前崩溃：账本 running + destructive。
	if _, err := td.DB.Exec(`UPDATE juhe_business.schema_migrations
		SET status = 'running', phase = 'contract', destructive = TRUE
		WHERE revision = 25`); err != nil {
		t.Fatalf("模拟 rev25 中断账本失败: %v", err)
	}
	if _, err := td.DB.Exec(`UPDATE juhe_business.schema_state
		SET schema_revision = 25, min_readable_binary_revision = 25
		WHERE id = 1`); err != nil {
		t.Fatalf("回退 schema_state 失败: %v", err)
	}

	expand := base
	expand.Phase = upgradepipeline.PhaseExpand
	expand.Online = false
	out = upgradepipeline.Run(context.Background(), expand, steps())
	if out.ExitCode != upgradepipeline.ExitNeedsHuman || out.Report == nil ||
		out.Report.Result != upgradepipeline.ResultBlocked {
		t.Fatalf("expand 应 blocked/3，got code=%d result=%v（stderr: %s）",
			out.ExitCode, reportResult(out.Report), out.Stderr)
	}
	if !strings.Contains(out.Stderr, "expand 不能跳过启动前必需的数据迁移") {
		t.Fatalf("blocked 文案未指出启动前必需迁移: %s", out.Stderr)
	}
	// 阻断必须发生在写 schema_state 之前：库仍停在 25，不能出现
	// schema_state=26 但 rev25 账本仍是 running 的不可启动形态。
	var revision int64
	if err := td.DB.QueryRowContext(context.Background(),
		`SELECT schema_revision FROM juhe_business.schema_state WHERE id = 1`).Scan(&revision); err != nil {
		t.Fatalf("读取 schema_state: %v", err)
	}
	if revision != 25 {
		t.Fatalf("blocked 后 schema_revision = %d, want 25（不得推进）", revision)
	}
}
