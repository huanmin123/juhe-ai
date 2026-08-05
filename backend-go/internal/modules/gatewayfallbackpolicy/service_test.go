package gatewayfallbackpolicy

import (
	"context"
	"strings"
	"testing"

	"juhe-ai/backend-go/internal/modules/gatewaycandidatewindow"
	"juhe-ai/backend-go/internal/modules/gatewaycapacityrouting"
	"juhe-ai/backend-go/internal/modules/gatewayingress"
	"juhe-ai/backend-go/internal/modules/gatewayrouteplan"
	protocolgateway "juhe-ai/backend-go/internal/protocols/gateway"
	"juhe-ai/backend-go/internal/store/port"
)

func TestServiceSelectsNodeOrderedEligibleFallbackCandidates(t *testing.T) {
	capability := &capabilityStub{selection: AccountSelection{CandidateAccountIDs: []string{"account-two", "account-three"}}}
	quota := &quotaStub{result: AuthorizationQuotaResult{Complete: true, AllowedByAccountID: map[string]bool{"account-two": true, "account-three": false}}}
	degradation := &degradationStub{result: RuntimeDegradationResult{CandidateAccountIDs: []string{"account-two"}}}
	capacity := &capacityStub{result: gatewaycapacityrouting.Result{}}
	service := newService(t, capability, quota, degradation, capacity)

	result, err := service.SelectFallbackCandidates(t.Context(), policyInput([]gatewaycandidatewindow.Candidate{
		candidate("account-one"), candidate("account-two"), candidate("account-three"),
	}, ReasonGroupCapacityBusy, []string{"account-one"}))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(result.CandidateAccountIDs, ","); got != "account-two" {
		t.Fatalf("selected accounts=%q", got)
	}
	if got := strings.Join(candidateIDs(capability.input.Candidates), ","); got != "account-two,account-three" {
		t.Fatalf("capability input=%q", got)
	}
	if got := strings.Join(candidateIDs(quota.input.Candidates), ","); got != "account-two,account-three" {
		t.Fatalf("quota input=%q", got)
	}
	if got := strings.Join(candidateIDs(degradation.input.Candidates), ","); got != "account-two" {
		t.Fatalf("degradation input=%q", got)
	}
	capacityIDs := strings.Join(candidateIDs(capacity.window.Candidates), ",")
	if capacity.calls != 1 || capacityIDs != "account-two" {
		t.Fatalf("capacity calls/window=%d/%q", capacity.calls, capacityIDs)
	}
}

func TestServiceSkipsRuntimeDegradedTargetWhenAllCandidatesAreDegraded(t *testing.T) {
	capacity := &capacityStub{}
	service := newService(t,
		&capabilityStub{selection: AccountSelection{CandidateAccountIDs: []string{"account-one"}}},
		&quotaStub{result: AuthorizationQuotaResult{Complete: true}},
		&degradationStub{result: RuntimeDegradationResult{CandidateAccountIDs: []string{"account-one"}, BypassedAllDegraded: true}},
		capacity,
	)
	result, err := service.SelectFallbackCandidates(t.Context(), policyInput([]gatewaycandidatewindow.Candidate{candidate("account-one")}, ReasonRuntimeDegraded, nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.CandidateAccountIDs) != 0 || capacity.calls != 0 {
		t.Fatalf("result=%+v capacity calls=%d", result, capacity.calls)
	}
}

func TestServiceSkipsAllBusyCapacityTarget(t *testing.T) {
	capacity := &capacityStub{result: gatewaycapacityrouting.Result{}}
	capacity.result.Observation.AllBusy = true
	service := newService(t,
		&capabilityStub{selection: AccountSelection{CandidateAccountIDs: []string{"account-one"}}},
		&quotaStub{result: AuthorizationQuotaResult{Complete: true}},
		&degradationStub{result: RuntimeDegradationResult{CandidateAccountIDs: []string{"account-one"}}},
		capacity,
	)
	result, err := service.SelectFallbackCandidates(t.Context(), policyInput([]gatewaycandidatewindow.Candidate{candidate("account-one")}, ReasonHighConcurrencyGroupBusy, nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.CandidateAccountIDs) != 0 || capacity.calls != 1 {
		t.Fatalf("result=%+v capacity calls=%d", result, capacity.calls)
	}
}

func TestServicePreservesCapabilityOrderThenBuildsDirectBeforeMappedModelTiers(t *testing.T) {
	mapped := candidate("mapped")
	mapped.SupportedModels = []string{"upstream-gpt"}
	mapped.Projection.ProtocolCode = "openai"
	mapped.Projection.ProtocolVersion = "v1"
	mapped.ModelMappings = []gatewaycandidatewindow.ModelMapping{{
		SourceModel: "gpt-test", SourceEndpointFamily: "chat_completions",
		UpstreamModel: "upstream-gpt", UpstreamEndpointFamily: "chat_completions", Enabled: true,
	}}
	directOne := candidate("direct-one")
	directTwo := candidate("direct-two")
	capability := &capabilityStub{selection: AccountSelection{CandidateAccountIDs: []string{"direct-two", "mapped", "direct-one"}}}
	quota := &quotaStub{result: AuthorizationQuotaResult{Complete: true}}
	degradation := &degradationStub{result: RuntimeDegradationResult{CandidateAccountIDs: []string{"mapped", "direct-two", "direct-one"}}}
	service := newService(t, capability, quota, degradation, &capacityStub{})

	result, err := service.SelectFallbackCandidates(t.Context(), policyInput([]gatewaycandidatewindow.Candidate{mapped, directOne, directTwo}, ReasonRuntimeDegraded, nil))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(candidateIDs(capability.input.Candidates), ","); got != "mapped,direct-one,direct-two" {
		t.Fatalf("capability received unexpected source order=%q", got)
	}
	if got := strings.Join(candidateIDs(quota.input.Candidates), ","); got != "direct-one,direct-two,mapped" {
		t.Fatalf("model tiers=%q", got)
	}
	if got := degradation.input.ModelRankByAccountID["direct-one"]; got != 0 {
		t.Fatalf("direct rank=%d", got)
	}
	if got := degradation.input.ModelRankByAccountID["mapped"]; got != 1 {
		t.Fatalf("mapped rank=%d", got)
	}
	if got := strings.Join(result.CandidateAccountIDs, ","); got != "direct-two,direct-one,mapped" {
		t.Fatalf("tier-preserved runtime order=%q", got)
	}
}

func TestServicePreservesLocalGroupBindingTiersDespiteGlobalAccountFieldConflicts(t *testing.T) {
	preferred := candidate("preferred")
	preferred.Projection.LocalFallbackEnabled = false
	preferred.Projection.FallbackEnabled = true
	preferred.Projection.LocalSuperPriorityEnabled = false
	preferred.Projection.SuperPriorityEnabled = true
	preferred.Projection.LocalPriority = 1
	preferred.Projection.Priority = 99

	fallback := candidate("fallback")
	fallback.Projection.LocalFallbackEnabled = true
	fallback.Projection.FallbackEnabled = false
	fallback.Projection.LocalSuperPriorityEnabled = false
	fallback.Projection.SuperPriorityEnabled = true
	fallback.Projection.LocalPriority = 1
	fallback.Projection.Priority = 0

	service := newService(t,
		&capabilityStub{selection: AccountSelection{CandidateAccountIDs: []string{"preferred", "fallback"}}},
		&quotaStub{result: AuthorizationQuotaResult{Complete: true}},
		&degradationStub{result: RuntimeDegradationResult{CandidateAccountIDs: []string{"fallback", "preferred"}}},
		&capacityStub{},
	)

	result, err := service.SelectFallbackCandidates(t.Context(), policyInput([]gatewaycandidatewindow.Candidate{preferred, fallback}, ReasonRuntimeDegraded, nil))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(result.CandidateAccountIDs, ","); got != "preferred,fallback" {
		t.Fatalf("local group-binding tiers were not preserved: %q", got)
	}
}

func TestServiceRejectsIncompleteOrForgedPolicyResults(t *testing.T) {
	t.Run("incomplete quota", func(t *testing.T) {
		service := newService(t,
			&capabilityStub{selection: AccountSelection{CandidateAccountIDs: []string{"account-one"}}},
			&quotaStub{result: AuthorizationQuotaResult{}},
			&degradationStub{}, &capacityStub{},
		)
		if _, err := service.SelectFallbackCandidates(t.Context(), policyInput([]gatewaycandidatewindow.Candidate{candidate("account-one")}, ReasonRuntimeDegraded, nil)); err == nil || !strings.Contains(err.Error(), "incomplete") {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("capability injects account", func(t *testing.T) {
		service := newService(t,
			&capabilityStub{selection: AccountSelection{CandidateAccountIDs: []string{"foreign"}}},
			&quotaStub{}, &degradationStub{}, &capacityStub{},
		)
		if _, err := service.SelectFallbackCandidates(t.Context(), policyInput([]gatewaycandidatewindow.Candidate{candidate("account-one")}, ReasonRuntimeDegraded, nil)); err == nil || !strings.Contains(err.Error(), "outside its input") {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("degradation drops account", func(t *testing.T) {
		service := newService(t,
			&capabilityStub{selection: AccountSelection{CandidateAccountIDs: []string{"account-one", "account-two"}}},
			&quotaStub{result: AuthorizationQuotaResult{Complete: true}},
			&degradationStub{result: RuntimeDegradationResult{CandidateAccountIDs: []string{"account-one"}}}, &capacityStub{},
		)
		if _, err := service.SelectFallbackCandidates(t.Context(), policyInput([]gatewaycandidatewindow.Candidate{candidate("account-one"), candidate("account-two")}, ReasonRuntimeDegraded, nil)); err == nil || !strings.Contains(err.Error(), "permutation") {
			t.Fatalf("err=%v", err)
		}
	})
}

func TestServiceRejectsUnknownNodeFallbackReason(t *testing.T) {
	service := newService(t, &capabilityStub{}, &quotaStub{}, &degradationStub{}, &capacityStub{})
	if _, err := service.SelectFallbackCandidates(t.Context(), policyInput([]gatewaycandidatewindow.Candidate{candidate("account-one")}, "unknown_node_reason", nil)); err == nil {
		t.Fatal("unknown Node fallback reason accepted")
	}
}

func TestNewServiceRequiresEveryNodeEligibilityDependency(t *testing.T) {
	if _, err := NewService(Options{}); err == nil {
		t.Fatal("missing dependencies accepted")
	}
}

func newService(t *testing.T, capability CapabilityFilter, quota AuthorizationQuotaChecker, degradation RuntimeDegradationOrderer, capacity CapacityEvaluator) *Service {
	t.Helper()
	service, err := NewService(Options{Capability: capability, Quota: quota, Degradation: degradation, Capacity: capacity})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func policyInput(candidates []gatewaycandidatewindow.Candidate, reason string, excluded []string) gatewayrouteplan.FallbackCandidatePolicyInput {
	intent, err := gatewayingress.Parse(gatewayingress.ParseInput{RawBody: []byte(`{"model":"gpt-test","stream":false}`)})
	if err != nil {
		panic(err)
	}
	return gatewayrouteplan.FallbackCandidatePolicyInput{
		Window: gatewaycandidatewindow.Window{Candidates: candidates}, RequestedModel: "gpt-test",
		Intent: intent, IngressFinalization: policyFinalization(),
		RequestShape: protocolgateway.RequestShape{Path: "/v1/chat/completions", Model: "gpt-test"}, Protocol: protocolgateway.ProtocolOpenAI, FinalLane: gatewayingress.LaneText,
		EndpointFamily: "chat_completions", RequestClientCompatibility: "openai", RequestLane: string(gatewayingress.LaneText),
		Reason: reason, ExcludedAccountIDs: excluded,
	}
}

func policyFinalization() gatewayingress.FinalResult {
	intent, err := gatewayingress.Parse(gatewayingress.ParseInput{RawBody: []byte(`{"model":"gpt-test","stream":false}`)})
	if err != nil {
		panic(err)
	}
	snapshot, err := gatewayingress.NewSnapshot(gatewayingress.SnapshotInput{
		Revision: "policy-snapshot", Model: "gpt-test", CandidateCapacity: 1,
		ToolCatalog: map[string]struct{}{}, ToolCatalogComplete: true, MappingLane: gatewayingress.LaneText,
	})
	if err != nil {
		panic(err)
	}
	finalization, err := gatewayingress.Finalize(intent, snapshot, true)
	if err != nil {
		panic(err)
	}
	return finalization
}

func candidate(accountID string) gatewaycandidatewindow.Candidate {
	return gatewaycandidatewindow.Candidate{
		Projection: port.GatewayAccountCandidate{AccountID: accountID}, SupportedModels: []string{"gpt-test"},
	}
}

type capabilityStub struct {
	selection AccountSelection
	input     CapabilityInput
}

func (s *capabilityStub) FilterFallbackCapability(_ context.Context, input CapabilityInput) (AccountSelection, error) {
	s.input = input
	return s.selection, nil
}

type quotaStub struct {
	result AuthorizationQuotaResult
	input  AuthorizationQuotaInput
}

func (s *quotaStub) CheckFallbackAuthorizationQuota(_ context.Context, input AuthorizationQuotaInput) (AuthorizationQuotaResult, error) {
	s.input = input
	return s.result, nil
}

type degradationStub struct {
	result RuntimeDegradationResult
	input  RuntimeDegradationInput
}

func (s *degradationStub) OrderFallbackRuntimeDegradation(_ context.Context, input RuntimeDegradationInput) (RuntimeDegradationResult, error) {
	s.input = input
	return s.result, nil
}

type capacityStub struct {
	result gatewaycapacityrouting.Result
	calls  int
	window gatewaycandidatewindow.Window
}

func (s *capacityStub) Evaluate(_ context.Context, window gatewaycandidatewindow.Window, _ gatewayingress.Lane) (gatewaycapacityrouting.Result, error) {
	s.calls++
	s.window = window
	return s.result, nil
}
