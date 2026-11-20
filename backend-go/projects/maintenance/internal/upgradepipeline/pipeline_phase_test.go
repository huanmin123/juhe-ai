package upgradepipeline

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// 分相过滤：expand 只含非破坏步骤，contract 只含破坏步骤，all 全量。
func TestFilterStepsForPhase(t *testing.T) {
	steps := []Step{
		{Revision: 1, ID: "additive-a"},
		{Revision: 2, ID: "destructive-b", Destructive: true},
		{Revision: 3, ID: "additive-c"},
	}
	cases := []struct {
		phase UpgradePhase
		want  []string
	}{
		{PhaseAll, []string{"additive-a", "destructive-b", "additive-c"}},
		{PhaseExpand, []string{"additive-a", "additive-c"}},
		{PhaseContract, []string{"destructive-b"}},
	}
	for _, tc := range cases {
		got := filterStepsForPhase(steps, tc.phase)
		if len(got) != len(tc.want) {
			t.Fatalf("%s: len = %d, want %d", tc.phase, len(got), len(tc.want))
		}
		for i := range got {
			if got[i].ID != tc.want[i] {
				t.Fatalf("%s: [%d] = %s, want %s", tc.phase, i, got[i].ID, tc.want[i])
			}
		}
	}
}

// 未知 phase 必须在 Run 入口拒绝（不触库、不产生报告）。
func TestRunRejectsUnknownPhase(t *testing.T) {
	out := Run(context.Background(), Options{Phase: UpgradePhase("switch")}, nil)
	if out.ExitCode != ExitUsage {
		t.Fatalf("exit = %d, want %d", out.ExitCode, ExitUsage)
	}
	if out.Report != nil {
		t.Fatal("未知 phase 不应产生报告")
	}
}

// pendingDestructiveSteps 是 expand 显式呈现/阻断的数据源：已干净 applied、
// 无账本行且 Satisfied 或非破坏步骤不算待执行；账本残留 running/outdated
// 行即使 Satisfied 也必须算待执行（否则 expand 会推进 schema_state 但留下
// destructive 中断标记，被 schemagate 拒绝）。启动前必需项由调用方 blocked。
func TestPendingDestructiveSteps(t *testing.T) {
	steps := []Step{
		{Revision: 1, ID: "cleanup", Destructive: true, Satisfied: func(context.Context, Env) (bool, error) { return false, nil }},
		{Revision: 2, ID: "applied-cleanup", Destructive: true},
		{Revision: 3, ID: "already-satisfied", Destructive: true, RequiredBeforeStartup: true,
			Satisfied: func(context.Context, Env) (bool, error) { return true, nil }},
		{Revision: 4, ID: "startup-migration", Destructive: true, RequiredBeforeStartup: true,
			Satisfied: func(context.Context, Env) (bool, error) { return false, nil }},
		{Revision: 5, ID: "additive"},
		{Revision: 6, ID: "interrupted-startup-migration", Destructive: true, RequiredBeforeStartup: true,
			Satisfied: func(context.Context, Env) (bool, error) { return true, nil }},
		{Revision: 7, ID: "predicate-error-nonrequired", Destructive: true,
			Satisfied: func(context.Context, Env) (bool, error) {
				return false, context.DeadlineExceeded
			}},
	}
	rows := map[int64]MigrationRow{
		2: {Revision: 2, Status: "applied", ImplVersion: 1},
		6: {Revision: 6, Status: "running", ImplVersion: 1},
	}
	got, err := pendingDestructiveSteps(context.Background(), Env{}, steps, rows)
	if err != nil {
		t.Fatalf("pendingDestructiveSteps: %v", err)
	}
	if len(got) != 4 || got[0].ID != "cleanup" || got[1].ID != "startup-migration" ||
		got[2].ID != "interrupted-startup-migration" || got[3].ID != "predicate-error-nonrequired" {
		t.Fatalf("pending = %+v, want cleanup/startup-migration/interrupted-startup-migration/predicate-error-nonrequired", got)
	}
}

// TestIsUndefinedTableCoversMissingSchema 钉住 fresh 空库语义：限定
// juhe_business schema 的查询在 schema 尚未创建时返回 3F000，必须与
// 42P01（表缺失）一样按「未初始化」处理，否则 upgrade 在 ensure 前 exit 1。
func TestIsUndefinedTableCoversMissingSchema(t *testing.T) {
	for _, code := range []string{"42P01", "3F000"} {
		if !isUndefinedTable(&pgconn.PgError{Code: code}) {
			t.Fatalf("PG %s must be treated as uninitialized", code)
		}
		if !isUndefinedTableError(&pgconn.PgError{Code: code}) {
			t.Fatalf("KCV must treat PG %s as missing carrier", code)
		}
	}
	if isUndefinedTable(errors.New("other")) || isUndefinedTable(&pgconn.PgError{Code: "08006"}) {
		t.Fatal("non-missing-schema errors must stay fatal")
	}
}
