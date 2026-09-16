package modelcheckcommand

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckinput"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelchecksource"
)

type w9hFreezer struct {
	err       error
	targets   map[string]modelchecksource.FrozenTarget
	requests  []modelchecksource.Request
	lastAllow bool
}

func (f *w9hFreezer) FreezeTarget(_ context.Context, request modelchecksource.Request) (modelchecksource.FrozenTarget, error) {
	f.requests = append(f.requests, request)
	if f.err != nil {
		return modelchecksource.FrozenTarget{}, f.err
	}
	target, ok := f.targets[request.AccountID]
	if !ok {
		return modelchecksource.FrozenTarget{}, errors.New("target missing")
	}
	return target, nil
}

type w9hPolicyLoader struct {
	snapshot modelcheckinput.PolicySnapshot
	err      error
}

func (l w9hPolicyLoader) Load(context.Context, string) (modelcheckinput.PolicySnapshot, error) {
	return l.snapshot, l.err
}

func w9hConfig(freezer TargetFreezer, loader PolicyLoader) Config {
	return Config{
		Freezer:         freezer,
		PolicyLoader:    loader,
		ProbeSetVersion: "probe-set-w9h",
		Deadline:        time.Minute,
		Now:             func() time.Time { return time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC) },
	}
}

func w9hValidPolicy(t *testing.T) modelcheckinput.PolicySnapshot {
	t.Helper()
	policy, err := modelcheckinput.NewPolicySnapshot("w9h-policy", "quick", true, 70, "fallback", 10)
	if err != nil {
		t.Fatal(err)
	}
	return policy
}

func w9hFrozenTarget(id string) modelchecksource.FrozenTarget {
	return modelchecksource.FrozenTarget{
		TargetName:          "target-" + id,
		TargetOwnerSystemID: "sys_admin",
		GroupID:             "group-" + id,
		DurableAccount:      modelcheckinput.AccountSnapshot{ID: id, ProviderCode: "openai"},
	}
}

func w9hRequest() Request {
	return Request{
		SystemAccountID:      "sys_admin",
		ActorSystemAccountID: "actor",
		TargetID:             "acc-1",
		Model:                "gpt-w9h",
		Profile:              "quick",
		Trigger:              modelcheckinput.TriggerManual,
	}
}

func TestW9HBuilderNewValidation(t *testing.T) {
	freezer := &w9hFreezer{}
	loader := w9hPolicyLoader{}
	if _, err := New(Config{PolicyLoader: loader, ProbeSetVersion: "v", Deadline: time.Minute}); err == nil || !strings.Contains(err.Error(), "freezer is required") {
		t.Fatalf("freezer err=%v", err)
	}
	if _, err := New(Config{Freezer: freezer, ProbeSetVersion: "v", Deadline: time.Minute}); err == nil || !strings.Contains(err.Error(), "policy loader is required") {
		t.Fatalf("loader err=%v", err)
	}
	if _, err := New(Config{Freezer: freezer, PolicyLoader: loader, Deadline: time.Minute}); err == nil || !strings.Contains(err.Error(), "probe set snapshot is required") {
		t.Fatalf("probe set err=%v", err)
	}
	if _, err := New(Config{Freezer: freezer, PolicyLoader: loader, ProbeSetVersion: "v"}); err == nil || !strings.Contains(err.Error(), "deadline must be positive") {
		t.Fatalf("deadline err=%v", err)
	}
	builder, err := New(w9hConfig(freezer, loader))
	if err != nil || builder == nil {
		t.Fatalf("valid builder err=%v", err)
	}
}

func TestW9HBuilderValidateArms(t *testing.T) {
	freezer := &w9hFreezer{targets: map[string]modelchecksource.FrozenTarget{"acc-1": w9hFrozenTarget("acc-1")}}
	builder, err := New(w9hConfig(freezer, w9hPolicyLoader{}))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	request := w9hRequest()

	missing := request
	missing.TargetID = " "
	if _, err := builder.BuildWithPolicy(ctx, missing, modelcheckinput.PolicySnapshot{}); err == nil || !strings.Contains(err.Error(), "are required") {
		t.Fatalf("missing fields err=%v", err)
	}
	badProfile := request
	badProfile.Profile = "deep"
	if _, err := builder.BuildWithPolicy(ctx, badProfile, modelcheckinput.PolicySnapshot{}); err == nil || !strings.Contains(err.Error(), "profile is invalid") {
		t.Fatalf("profile err=%v", err)
	}
	badTrigger := request
	badTrigger.Trigger = "bogus"
	if _, err := builder.BuildWithPolicy(ctx, badTrigger, modelcheckinput.PolicySnapshot{}); err == nil || !strings.Contains(err.Error(), "trigger is invalid") {
		t.Fatalf("trigger err=%v", err)
	}
	scheduledNoID := request
	scheduledNoID.Trigger = modelcheckinput.TriggerScheduled
	if _, err := builder.BuildWithPolicy(ctx, scheduledNoID, modelcheckinput.PolicySnapshot{}); err == nil || !strings.Contains(err.Error(), "requires schedule ID") {
		t.Fatalf("schedule id err=%v", err)
	}
	manualWithID := request
	manualWithID.ScheduleID = "sched-1"
	if _, err := builder.BuildWithPolicy(ctx, manualWithID, modelcheckinput.PolicySnapshot{}); err == nil || !strings.Contains(err.Error(), "must not include schedule ID") {
		t.Fatalf("manual with schedule err=%v", err)
	}
	selfComparison := request
	selfComparison.TrustedComparisonID = "acc-1"
	if _, err := builder.BuildWithPolicy(ctx, selfComparison, modelcheckinput.PolicySnapshot{}); err == nil || !strings.Contains(err.Error(), "must differ from target") {
		t.Fatalf("self comparison err=%v", err)
	}
	// nil builder guard。
	var nilBuilder *Builder
	if _, err := nilBuilder.BuildWithPolicy(ctx, request, modelcheckinput.PolicySnapshot{}); err == nil || !strings.Contains(err.Error(), "not initialized") {
		t.Fatalf("nil builder err=%v", err)
	}
	if _, err := nilBuilder.Build(ctx, request); err == nil || !strings.Contains(err.Error(), "not initialized") {
		t.Fatalf("nil builder Build err=%v", err)
	}
}

func TestW9HBuilderBuildArms(t *testing.T) {
	freezer := &w9hFreezer{targets: map[string]modelchecksource.FrozenTarget{
		"acc-1": w9hFrozenTarget("acc-1"),
		"acc-2": w9hFrozenTarget("acc-2"),
	}}
	loader := w9hPolicyLoader{}
	builder, err := New(w9hConfig(freezer, loader))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// FreezeTarget 失败。
	freezer.err = errors.New("db down")
	if _, err := builder.BuildWithPolicy(ctx, w9hRequest(), w9hValidPolicy(t)); err == nil || !strings.Contains(err.Error(), "freeze model check target") {
		t.Fatalf("freeze err=%v", err)
	}
	freezer.err = nil
	// 无效策略快照在 freeze 前被拒。
	if _, err := builder.BuildWithPolicy(ctx, w9hRequest(), modelcheckinput.PolicySnapshot{}); err == nil || !strings.Contains(err.Error(), "verify model check policy") {
		t.Fatalf("invalid policy err=%v", err)
	}
	// 元数据不完整。
	freezer.targets["acc-1"] = modelchecksource.FrozenTarget{DurableAccount: modelcheckinput.AccountSnapshot{ID: "acc-1"}}
	if _, err := builder.BuildWithPolicy(ctx, w9hRequest(), w9hValidPolicy(t)); err == nil || !strings.Contains(err.Error(), "metadata is incomplete") {
		t.Fatalf("metadata err=%v", err)
	}
	freezer.targets["acc-1"] = w9hFrozenTarget("acc-1")
	// Build 委托 PolicyLoader 并透传错误。
	failingBuilder, builderErr := New(w9hConfig(freezer, w9hPolicyLoader{err: errors.New("policy store down")}))
	if builderErr != nil {
		t.Fatal(builderErr)
	}
	_, err = failingBuilder.Build(ctx, w9hRequest())
	if err == nil || !strings.Contains(err.Error(), "load model check policy") {
		t.Fatalf("policy load err=%v", err)
	}
	// 成功路径（带可信对照）。
	snapshot := w9hRequest()
	snapshot.TrustedComparisonID = "acc-2"
	run, err := builder.BuildWithPolicy(ctx, snapshot, w9hValidPolicy(t))
	if err != nil {
		t.Fatalf("success err=%v", err)
	}
	if !run.TrustedComparison || run.Comparison == nil || run.Comparison.ID != "acc-2" {
		t.Fatalf("comparison run=%+v", run)
	}
	if run.Target.ID != "acc-1" || run.TargetName != "target-acc-1" || run.GroupID != "group-acc-1" {
		t.Fatalf("run=%+v", run)
	}
	if run.ProbeSetVersion != "probe-set-w9h" || run.Trigger != modelcheckinput.TriggerManual {
		t.Fatalf("run=%+v", run)
	}
	if !run.DeadlineAt.After(run.StartedAt) {
		t.Fatalf("deadline=%v started=%v", run.DeadlineAt, run.StartedAt)
	}
	// quality-recovery 触发把 AllowQualityIsolated 传给 freezer。
	quality := w9hRequest()
	quality.Trigger = modelcheckinput.TriggerQualityRecovery
	quality.ScheduleID = ""
	freezer.requests = nil
	if _, err := builder.BuildWithPolicy(ctx, quality, w9hValidPolicy(t)); err != nil {
		t.Fatalf("quality err=%v", err)
	}
	if len(freezer.requests) != 1 || !freezer.requests[0].AllowQualityIsolated {
		t.Fatalf("requests=%+v", freezer.requests)
	}
}
