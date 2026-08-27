package modelcheckcommand

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckinput"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelchecksource"
)

func TestBuilderFreezesTargetAndTrustedComparisonIntoRuntimeRequest(t *testing.T) {
	freezer := &fakeFreezer{targets: map[string]modelchecksource.FrozenTarget{
		"target":     frozen("target", "Target"),
		"comparison": frozen("comparison", "Comparison"),
	}}
	now := time.Date(2026, 8, 27, 15, 0, 0, 0, time.UTC)
	builder, err := New(Config{Freezer: freezer, PolicyLoader: staticPolicyLoader{policy: testPolicySnapshot()}, ProbeSetVersion: "probe-v1", Deadline: time.Minute, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	request, err := builder.Build(context.Background(), Request{SystemAccountID: "system-1", ActorSystemAccountID: "actor-1", TargetID: "target", Model: "gpt-5.6-sol", Profile: "full", TrustedComparisonID: "comparison", Trigger: modelcheckinput.TriggerManual, TraceID: "trace-1"})
	if err != nil {
		t.Fatal(err)
	}
	if request.Target.ID != "target" || request.Comparison == nil || request.Comparison.ID != "comparison" || !request.TrustedComparison || request.TargetName != "Target" || request.GroupID != "group-1" || request.DeadlineAt != now.Add(time.Minute) {
		t.Fatalf("runtime request=%#v", request)
	}
	if len(freezer.requests) != 2 || freezer.requests[0].AllowQualityIsolated || freezer.requests[1].AccountID != "comparison" {
		t.Fatalf("freeze requests=%#v", freezer.requests)
	}
}

func TestBuilderRestrictsQualityRecoveryAndDoesNotFreezeInvalidRequest(t *testing.T) {
	freezer := &fakeFreezer{targets: map[string]modelchecksource.FrozenTarget{"target": frozen("target", "Target")}}
	builder, err := New(Config{Freezer: freezer, PolicyLoader: staticPolicyLoader{policy: testPolicySnapshot()}, ProbeSetVersion: "probe-v1", Deadline: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := builder.Build(context.Background(), Request{SystemAccountID: "system-1", ActorSystemAccountID: "actor-1", TargetID: "target", Model: "gpt-5.6-sol", Profile: "quick", Trigger: modelcheckinput.TriggerManual, TrustedComparisonID: "target"}); err == nil || len(freezer.requests) != 0 {
		t.Fatalf("err=%v requests=%#v", err, freezer.requests)
	}
	if _, err := builder.Build(context.Background(), Request{SystemAccountID: "system-1", ActorSystemAccountID: "actor-1", TargetID: "target", Model: "gpt-5.6-sol", Profile: "quick", Trigger: modelcheckinput.TriggerQualityRecovery}); err != nil {
		t.Fatal(err)
	}
	if !freezer.requests[0].AllowQualityIsolated {
		t.Fatalf("quality recovery request=%#v", freezer.requests[0])
	}
}

type fakeFreezer struct {
	targets  map[string]modelchecksource.FrozenTarget
	requests []modelchecksource.Request
}

func (f *fakeFreezer) FreezeTarget(_ context.Context, request modelchecksource.Request) (modelchecksource.FrozenTarget, error) {
	f.requests = append(f.requests, request)
	target, ok := f.targets[request.AccountID]
	if !ok {
		return modelchecksource.FrozenTarget{}, errors.New("missing target")
	}
	return target, nil
}

func frozen(id, name string) modelchecksource.FrozenTarget {
	return modelchecksource.FrozenTarget{
		DurableAccount:      modelcheckinput.AccountSnapshot{ID: id, ConfigRevision: "rev-1", ProviderCode: "openai", ProtocolProfileID: "profile_openai_openai_v1", ProtocolProfileRevision: "profile-rev-1", EndpointFingerprint: "endpoint-fingerprint", MappedUpstreamModel: "gpt-5.6-sol", CredentialEnvelopeRef: "credential-ref", ProxyConfigurationVersion: "direct"},
		TargetName:          name,
		TargetOwnerSystemID: "system-1",
		GroupID:             "group-1",
	}
}

func testPolicySnapshot() modelcheckinput.PolicySnapshot {
	policy, err := modelcheckinput.NewPolicySnapshot("policy-1", "quick", true, 70, "fallback", 10)
	if err != nil {
		panic(err)
	}
	return policy
}

type staticPolicyLoader struct {
	policy modelcheckinput.PolicySnapshot
	err    error
}

func (loader staticPolicyLoader) Load(context.Context, string) (modelcheckinput.PolicySnapshot, error) {
	return loader.policy, loader.err
}
