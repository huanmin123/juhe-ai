package modelcheckhttp

import (
	"context"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckcommand"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckinput"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelchecksource"
)

func TestNewBuildRequestFuncUsesGoCommandBuilder(t *testing.T) {
	builder, err := modelcheckcommand.New(modelcheckcommand.Config{
		Freezer:         httpFakeFreezer{},
		PolicyLoader:    httpFakePolicyLoader{},
		ProbeSetVersion: "probe-v1",
		Deadline:        time.Minute,
		Now:             func() time.Time { return time.Date(2026, 8, 27, 16, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	request, err := NewBuildRequestFunc(builder)(context.Background(), Scope{SystemAccountID: "system-1"}, Command{TargetType: "account", TargetID: "target-1", Model: "gpt-5.6-sol", Profile: "quick"})
	if err != nil {
		t.Fatal(err)
	}
	if request.SystemAccountID != "system-1" || request.ActorSystemAccountID != "system-1" || request.Target.ID != "target-1" || request.Trigger != modelcheckinput.TriggerManual || request.Policy.Revision != "1" {
		t.Fatalf("request=%#v", request)
	}
}

func TestNewBuildRequestFuncRejectsNilBuilder(t *testing.T) {
	_, err := NewBuildRequestFunc(nil)(context.Background(), Scope{SystemAccountID: "system-1"}, Command{})
	if err == nil || err.Error() != "model check command builder is not initialized" {
		t.Fatalf("err=%v", err)
	}
}

type httpFakeFreezer struct{}

func (httpFakeFreezer) FreezeTarget(_ context.Context, request modelchecksource.Request) (modelchecksource.FrozenTarget, error) {
	return modelchecksource.FrozenTarget{DurableAccount: modelcheckinput.AccountSnapshot{ID: request.AccountID, ConfigRevision: "1", ProviderCode: "openai", ProtocolProfileID: "profile_openai_openai_v1", ProtocolProfileRevision: "profile-1", EndpointFingerprint: "endpoint-1", MappedUpstreamModel: request.Model, CredentialEnvelopeRef: "credential-1", ProxyConfigurationVersion: "direct"}, TargetName: "Target", TargetOwnerSystemID: request.SystemAccountID, GroupID: "group-1"}, nil
}

type httpFakePolicyLoader struct{}

func (httpFakePolicyLoader) Load(context.Context, string) (modelcheckinput.PolicySnapshot, error) {
	return modelcheckinput.NewPolicySnapshot("1", "quick", true, 70, "fallback", 10)
}
