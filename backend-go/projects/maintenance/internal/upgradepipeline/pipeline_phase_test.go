package upgradepipeline

import (
	"context"
	"testing"
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
