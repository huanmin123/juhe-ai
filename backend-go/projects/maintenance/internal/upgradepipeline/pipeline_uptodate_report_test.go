// T84-4/T84-9 集成测试（pgtestkit 隔离库，真实 PG）：up-to-date 短路的账本
// 记账收敛（§7.4）与报告口径以执行期为准（§9.3，升级兼容与迁移账本设计）。
// 两条路径都触库（短路记账写账本行、报告口径依赖计划/执行两次求值），单测层
// 无法覆盖。测试步骤 revision 独占 21（= contracts.SchemaRevision；注册表真实
// 步骤最高 20）——两次 Run 共用同一库，checksum 读取侧闭环要求账本行与本测试
// 注册表一致，不得与真实注册表步骤冲突。
package upgradepipeline

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/schema"
	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

func uptodateRunOptions(dsn string) Options {
	return Options{
		PrimaryDSN:         dsn,
		Secret:             "uptodate-report-test-secret",
		StopWriteConfirmed: true,
		BackupConfirmed:    true,
		Phase:              PhaseAll,
		StepTimeout:        30 * time.Second,
		AppVersion:         "test",
	}
}

func openUptodateTestDB(t *testing.T) *pgtestkit.TestDatabase {
	t.Helper()
	return pgtestkit.NewTestDatabase(t, func(ctx context.Context, db *sql.DB) error {
		_, err := schema.EnsurePostgres(ctx, db)
		return err
	})
}

// T84-4：up-to-date 短路时补写 adopt 记账（幂等 UPDATE 刷新 impl_version），
// 业务表零写入承诺不变（短路块只 EnsureLedger + MarkAdopted）。impl_version
// 漂移期间每次短路都会收敛账本；无漂移时 pending 为空，短路零记账。
func TestRunUpToDateShortCircuitAdoptsLedger(t *testing.T) {
	handle := openUptodateTestDB(t)
	step := Step{
		Revision: 21, ID: "uptodate-adopt-probe", Title: "短路记账探针",
		Target: TargetPrimary, ImplVersion: 1,
		Satisfied: func(ctx context.Context, env Env) (bool, error) { return true, nil },
		Apply: func(ctx context.Context, env Env) (Detail, error) {
			t.Error("Satisfied 恒真的步骤不应执行 Apply")
			return Detail{}, nil
		},
		Verify: func(ctx context.Context, env Env) error { return nil },
	}
	steps := []Step{step}

	// Run1：dbRevision 0 → 完整链（短路条件不成立），步骤计划期 adopt。
	out := Run(context.Background(), uptodateRunOptions(handle.DSN), steps)
	if out.ExitCode != ExitOK || out.Report == nil || out.Report.Result != ResultUpgraded {
		t.Fatalf("Run1 exit=%d result=%v stderr=%s, want upgraded", out.ExitCode, out.Report, out.Stderr)
	}

	// 制造 impl_version 漂移：账本行 applied 但 impl 落后 → 计划期进 pending
	//（outdated-impl）、Satisfied 求值仍真 → needsApply=false → 走短路块。
	if _, err := handle.DB.Exec(`UPDATE juhe_business.schema_migrations SET impl_version = 0 WHERE revision = 21`); err != nil {
		t.Fatalf("制造 impl_version 漂移失败: %v", err)
	}

	// Run2：短路路径——断言账本 impl_version 被刷新（裁决 A 的记账语义），
	// 报告 Executed 同步列出 adopted。
	out = Run(context.Background(), uptodateRunOptions(handle.DSN), steps)
	if out.ExitCode != ExitOK || out.Report == nil || out.Report.Result != ResultUpToDate {
		t.Fatalf("Run2 exit=%d result=%v stderr=%s, want up-to-date", out.ExitCode, out.Report, out.Stderr)
	}
	var implVersion int
	if err := handle.DB.QueryRow(`SELECT impl_version FROM juhe_business.schema_migrations WHERE revision = 21`).Scan(&implVersion); err != nil {
		t.Fatalf("读账本 impl_version 失败: %v", err)
	}
	if implVersion != 1 {
		t.Fatalf("短路必须幂等刷新账本 impl_version: got %d, want 1", implVersion)
	}
	if len(out.Report.Executed) != 1 || out.Report.Executed[0].Revision != 21 || out.Report.Executed[0].Status != "adopted" {
		t.Fatalf("短路报告 Executed = %+v, want [{21 adopted}]", out.Report.Executed)
	}

	// Run3：无漂移短路——pending 为空，短路零记账（Executed 空）。
	out = Run(context.Background(), uptodateRunOptions(handle.DSN), steps)
	if out.ExitCode != ExitOK || out.Report == nil || out.Report.Result != ResultUpToDate {
		t.Fatalf("Run3 exit=%d result=%v stderr=%s, want up-to-date", out.ExitCode, out.Report, out.Stderr)
	}
	if len(out.Report.Executed) != 0 {
		t.Fatalf("无漂移短路 Executed = %+v, want 空（零多余记账）", out.Report.Executed)
	}
}

// T84-9：报告以执行期为准——计划期 pending、执行期翻转 satisfied 的步骤
// （加法相/seed 改变了求值事实）从 Plan.Pending 挪入 Plan.Adopt（reason=
// satisfied-at-execution），保持 pending/adopt 互斥与 Executed 口径一致；
// 计划期已 adopt 的步骤执行期不重复追加。
func TestRunReportFollowsExecutionFlip(t *testing.T) {
	handle := openUptodateTestDB(t)
	calls := 0
	step := Step{
		Revision: 21, ID: "flip-report-probe", Title: "执行翻转报告探针",
		Target: TargetPrimary, ImplVersion: 1,
		Satisfied: func(ctx context.Context, env Env) (bool, error) {
			calls++
			return calls >= 2, nil // 计划期（第 1 次）false，执行期（第 2 次）true
		},
		Apply: func(ctx context.Context, env Env) (Detail, error) {
			t.Error("执行期翻转 satisfied 不应执行 Apply")
			return Detail{}, nil
		},
		Verify: func(ctx context.Context, env Env) error { return nil },
	}
	steps := []Step{step}

	// Run1：翻转场景全链。断言报告三处口径一致。
	out := Run(context.Background(), uptodateRunOptions(handle.DSN), steps)
	if out.ExitCode != ExitOK || out.Report == nil || out.Report.Result != ResultUpgraded {
		t.Fatalf("Run1 exit=%d result=%v stderr=%s, want upgraded", out.ExitCode, out.Report, out.Stderr)
	}
	if len(out.Report.Plan.Pending) != 0 {
		t.Fatalf("执行期翻转后 Plan.Pending = %+v, want 空（互斥剔除）", out.Report.Plan.Pending)
	}
	if len(out.Report.Plan.Adopt) != 1 || out.Report.Plan.Adopt[0].Revision != 21 ||
		out.Report.Plan.Adopt[0].Reason != "satisfied-at-execution" {
		t.Fatalf("Plan.Adopt = %+v, want [{21 satisfied-at-execution}]", out.Report.Plan.Adopt)
	}
	if len(out.Report.Executed) != 1 || out.Report.Executed[0].Revision != 21 || out.Report.Executed[0].Status != "adopted" {
		t.Fatalf("Executed = %+v, want [{21 adopted}]", out.Report.Executed)
	}
	var status string
	if err := handle.DB.QueryRow(`SELECT status FROM juhe_business.schema_migrations WHERE revision = 21`).Scan(&status); err != nil || status != "applied" {
		t.Fatalf("翻转 adopt 账本行 = (%s, %v), want applied/nil", status, err)
	}

	// Run2：applied+impl 足够 → 计划期直接跳过（不再求值 Satisfied），短路
	// 且 pending 为空 → Adopt/Executed 均不重复出现该步骤。
	out = Run(context.Background(), uptodateRunOptions(handle.DSN), steps)
	if out.ExitCode != ExitOK || out.Report == nil || out.Report.Result != ResultUpToDate {
		t.Fatalf("Run2 exit=%d result=%v stderr=%s, want up-to-date", out.ExitCode, out.Report, out.Stderr)
	}
	if calls != 2 {
		t.Fatalf("Satisfied 调用次数 = %d, want 2（Run2 计划期应跳过已收敛步骤）", calls)
	}
	if len(out.Report.Plan.Adopt) != 0 || len(out.Report.Executed) != 0 {
		t.Fatalf("Run2 Adopt=%+v Executed=%+v, want 均空", out.Report.Plan.Adopt, out.Report.Executed)
	}
}
