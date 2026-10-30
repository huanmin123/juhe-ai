package upgradepipeline

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestValidateRegistryRejectsBrokenRegistries(t *testing.T) {
	valid := Step{Revision: 1, ID: "a", Title: "A", ImplVersion: 1,
		Satisfied: func(context.Context, Env) (bool, error) { return true, nil },
		Apply:     func(context.Context, Env) (Detail, error) { return nil, nil },
		Verify:    func(context.Context, Env) error { return nil }}
	if err := validateRegistry([]Step{valid}); err != nil {
		t.Fatalf("合法注册表不应报错: %v", err)
	}
	dup := valid
	dup.ID = "b"
	if err := validateRegistry([]Step{valid, dup}); err == nil {
		t.Fatal("重复 revision 必须被拒绝")
	}
	zeroRev := valid
	zeroRev.Revision = 0
	if err := validateRegistry([]Step{zeroRev}); err == nil {
		t.Fatal("revision 0 必须被拒绝")
	}
	noImpl := valid
	noImpl.ImplVersion = 0
	if err := validateRegistry([]Step{noImpl}); err == nil {
		t.Fatal("ImplVersion 0 必须被拒绝")
	}
}

func TestSortStepsByRevisionOrders(t *testing.T) {
	base := Step{ID: "x", Title: "X", ImplVersion: 1,
		Satisfied: func(context.Context, Env) (bool, error) { return true, nil },
		Apply:     func(context.Context, Env) (Detail, error) { return nil, nil },
		Verify:    func(context.Context, Env) error { return nil }}
	a, b, c := base, base, base
	a.Revision, a.ID = 2, "a2"
	b.Revision, b.ID = 1, "b1"
	c.Revision, c.ID = 3, "c3"
	sorted := sortStepsByRevision([]Step{a, b, c})
	if sorted[0].ID != "b1" || sorted[1].ID != "a2" || sorted[2].ID != "c3" {
		t.Fatalf("排序结果不符: %s,%s,%s", sorted[0].ID, sorted[1].ID, sorted[2].ID)
	}
}

func TestExitCodeForStepError(t *testing.T) {
	wrapped := fmt.Errorf("%w: 步骤 x 未通过: boom", errVerifyFailed)
	if code := exitCodeForStepError(wrapped); code != ExitNeedsHuman {
		t.Fatalf("Verify 失败退出码 = %d, want %d", code, ExitNeedsHuman)
	}
	if code := exitCodeForStepError(errors.New("步骤 x Apply 失败: boom")); code != ExitRuntimeFailure {
		t.Fatalf("Apply 失败退出码 = %d, want %d", code, ExitRuntimeFailure)
	}
}

func TestChecksumShapeAndStability(t *testing.T) {
	step := Step{Revision: 7, ID: "s", Title: "S", Target: TargetPrimary, ImplVersion: 1}
	first, second := step.Checksum(), step.Checksum()
	if first != second {
		t.Fatalf("checksum 不稳定")
	}
	if len(first) != len("sha256:")+64 {
		t.Fatalf("checksum 形态不符: %s", first)
	}
	other := step
	other.Destructive = true
	if other.Checksum() == first {
		t.Fatal("destructive 变化必须改变 checksum")
	}
}

func TestMinutesClampsToAtLeastOne(t *testing.T) {
	if got := minutes(30 * time.Second); got != 1 {
		t.Fatalf("minutes(30s) = %d, want 1", got)
	}
	if got := minutes(90 * time.Minute); got != 90 {
		t.Fatalf("minutes(90m) = %d, want 90", got)
	}
}

func TestTargetSchemaMapping(t *testing.T) {
	cases := map[Target]string{
		TargetPrimary:        "",
		TargetJ3A:            "juhe_jobs",
		TargetAccountBalance: "juhe_jobs",
		TargetJ3B:            "juhe_j3b",
	}
	for target, want := range cases {
		if got := target.SchemaForTarget(); got != want {
			t.Fatalf("%s.SchemaForTarget() = %q, want %q", target, got, want)
		}
	}
}
