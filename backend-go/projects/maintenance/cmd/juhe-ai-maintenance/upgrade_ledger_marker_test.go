// T175 回归：applied 破坏性步骤行不得残留 destructive/phase 中断标记。
// schemagate 以账本 max(revision) 行的 destructive/phase 判定升级是否中断：
// 旧管线 contract 完成后残留 `destructive=true, phase='all'`，所有二进制被
// 永久 fail-fast（2026-11-07 dev 演练实录，升级兼容与迁移账本设计 §7.2）。
// 修复面：MarkApplied 同语句清标记 + ClearCompletedDestructiveMarkers 收尾
// 兜底（历史中断库重跑 contract 时 pending 为空、不经 MarkApplied）。
package main

import (
	"context"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/upgradepipeline"
	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

func TestUpgradeLedgerAppliedDestructiveMarkerCleared(t *testing.T) {
	pgtestkit.SkipUnlessExternalAuthorized(t)
	td := pgtestkit.NewTestDatabase(t)
	ctx := context.Background()
	if err := upgradepipeline.EnsureLedger(ctx, td.DB); err != nil {
		t.Fatalf("EnsureLedger: %v", err)
	}
	step := upgradepipeline.Step{
		Revision: 99, ID: "t175-destructive", Title: "T175 回归步骤",
		Target: upgradepipeline.TargetPrimary, Destructive: true, ImplVersion: 1,
	}
	now := time.Now()

	readRow := func() (destructive bool, phase, status string) {
		t.Helper()
		if err := td.DB.QueryRowContext(ctx,
			`SELECT destructive, phase, status FROM juhe_business.schema_migrations WHERE revision = 99`,
		).Scan(&destructive, &phase, &status); err != nil {
			t.Fatalf("读账本行: %v", err)
		}
		return destructive, phase, status
	}

	// running 行保留 destructive 标记：中断在破坏性步骤执行中时门禁必须拦截。
	if err := upgradepipeline.MarkRunning(ctx, td.DB, step, upgradepipeline.PhaseContract, false, "test", now); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}
	if destructive, phase, status := readRow(); !destructive || phase != "contract" || status != "running" {
		t.Fatalf("running 行标记错误: destructive=%v phase=%q status=%q", destructive, phase, status)
	}

	// applied 即完成：同语句清 destructive/phase，门禁放行。
	if err := upgradepipeline.MarkApplied(ctx, td.DB, step, 5, "test", now); err != nil {
		t.Fatalf("MarkApplied: %v", err)
	}
	if destructive, phase, status := readRow(); destructive || phase != "" || status != "applied" {
		t.Fatalf("applied 行残留中断标记: destructive=%v phase=%q status=%q", destructive, phase, status)
	}

	// 历史残留兜底：模拟旧管线产出的 applied+destructive 行，Clear 统一清除。
	if _, err := td.DB.ExecContext(ctx,
		`UPDATE juhe_business.schema_migrations SET destructive = true, phase = 'all' WHERE revision = 99`); err != nil {
		t.Fatalf("恢复残留态: %v", err)
	}
	cleared, err := upgradepipeline.ClearCompletedDestructiveMarkers(ctx, td.DB)
	if err != nil {
		t.Fatalf("ClearCompletedDestructiveMarkers: %v", err)
	}
	if cleared != 1 {
		t.Fatalf("cleared = %d, want 1", cleared)
	}
	if destructive, phase, _ := readRow(); destructive || phase != "" {
		t.Fatalf("兜底清扫后仍残留: destructive=%v phase=%q", destructive, phase)
	}

	// running 行不被兜底误清（中断判定依据必须保留）。
	running := upgradepipeline.Step{
		Revision: 100, ID: "t175-running", Title: "T175 running",
		Target: upgradepipeline.TargetPrimary, Destructive: true, ImplVersion: 1,
	}
	if err := upgradepipeline.MarkRunning(ctx, td.DB, running, upgradepipeline.PhaseContract, false, "test", now); err != nil {
		t.Fatalf("MarkRunning(running): %v", err)
	}
	if _, err := upgradepipeline.ClearCompletedDestructiveMarkers(ctx, td.DB); err != nil {
		t.Fatalf("Clear(running 场景): %v", err)
	}
	var destructive bool
	var phase string
	if err := td.DB.QueryRowContext(ctx,
		`SELECT destructive, phase FROM juhe_business.schema_migrations WHERE revision = 100`,
	).Scan(&destructive, &phase); err != nil {
		t.Fatalf("读 running 行: %v", err)
	}
	if !destructive || phase != "contract" {
		t.Fatalf("running 行被误清: destructive=%v phase=%q", destructive, phase)
	}

	// 幂等：重复 Clear 对已清行是 no-op。
	idempotent, err := upgradepipeline.ClearCompletedDestructiveMarkers(ctx, td.DB)
	if err != nil {
		t.Fatalf("Clear 幂等: %v", err)
	}
	if idempotent != 0 {
		t.Fatalf("幂等重跑 cleared = %d, want 0", idempotent)
	}
}
