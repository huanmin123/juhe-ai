package gatewaycanary

import "testing"

func TestEvaluateAdmitsOnlyCompleteGatewayCanaryEvidence(t *testing.T) {
	t.Parallel()
	input := completeInput()
	got := Evaluate(input)
	if got.Decision() != DecisionEligibleForOperatorCanary || got.ReasonCode() != ReasonReady {
		t.Fatalf("Evaluate(complete) = (%q, %q), want (%q, %q)", got.Decision(), got.ReasonCode(), DecisionEligibleForOperatorCanary, ReasonReady)
	}
	if input != completeInput() {
		t.Fatal("Evaluate mutated its value input")
	}
}

func TestEvaluateFailsClosedForEveryRequiredEvidence(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*Input)
		want   ReasonCode
	}{
		{"models only", func(input *Input) { input.ModelsOnlySurface = true }, ReasonModelsOnlySurface},
		{"models endpoint", func(input *Input) {
			input.Route.RouteID, input.Route.RollbackRouteID = "GET /v1/models", "GET /v1/models"
		}, ReasonModelsOnlySurface},
		{"missing epoch", func(input *Input) { input.Version.ObservedEpoch = "" }, ReasonEpochMissing},
		{"invalid epoch", func(input *Input) { input.Version.ObservedEpoch = "node production" }, ReasonEpochInvalid},
		{"stale epoch", func(input *Input) { input.Version.ObservedEpoch = "node-production-2026-07-19" }, ReasonEpochMismatch},
		{"missing schema", func(input *Input) { input.Version.ObservedSchemaVersion = 0 }, ReasonSchemaVersionMissing},
		{"wrong schema", func(input *Input) { input.Version.ObservedSchemaVersion++ }, ReasonSchemaVersionMismatch},
		{"missing route", func(input *Input) { input.Route.RouteID = "" }, ReasonRouteIdentifierMissing},
		{"lowercase method", func(input *Input) { input.Route.RouteID = "post /v1/chat/completions" }, ReasonRouteIdentifierInvalid},
		{"route wildcard", func(input *Input) { input.Route.RouteID = "POST /v1/*" }, ReasonRouteIdentifierInvalid},
		{"route query", func(input *Input) { input.Route.RouteID = "POST /v1/chat/completions?stream=true" }, ReasonRouteIdentifierInvalid},
		{"route fragment", func(input *Input) { input.Route.RouteID = "POST /v1/chat/completions#x" }, ReasonRouteIdentifierInvalid},
		{"route percent", func(input *Input) { input.Route.RouteID = "POST /v1/chat%2Fcompletions" }, ReasonRouteIdentifierInvalid},
		{"route backslash", func(input *Input) { input.Route.RouteID = "POST /v1\\chat" }, ReasonRouteIdentifierInvalid},
		{"management route", func(input *Input) { input.Route.RouteID = "POST /__aisys__/api/settings" }, ReasonRouteIdentifierInvalid},
		{"public route", func(input *Input) { input.Route.RouteID = "POST /__aipublic__/v1/accounts" }, ReasonRouteIdentifierInvalid},
		{"invalid rollback route", func(input *Input) { input.Route.RollbackRouteID = "POST /v1/chat?stream=true" }, ReasonRouteIdentifierInvalid},
		{"different rollback route", func(input *Input) { input.Route.RollbackRouteID = "POST /v1/other" }, ReasonRollbackRouteMismatch},
		{"node remains current owner", func(input *Input) { input.Route.CurrentOwner = OwnerNode }, ReasonGoRouteOwnerMissing},
		{"go rollback owner", func(input *Input) { input.Route.RollbackOwner = OwnerGo }, ReasonNodeRollbackOwnerMissing},
		{"route binding absent", func(input *Input) { input.Route.RouteBindingVerified = false }, ReasonRouteBindingUnverified},
		{"rollback route absent", func(input *Input) { input.Route.RollbackRouteVerified = false }, ReasonRollbackRouteUnverified},
		{"no non-models gateway traffic", func(input *Input) { input.Gateway.NonModelsGatewayTrafficObserved = false }, ReasonGatewayTrafficEvidenceMissing},
		{"listener absent", func(input *Input) { input.Gateway.GoListenerObserved = false }, ReasonGoListenerEvidenceMissing},
		{"body admission absent", func(input *Input) { input.Gateway.BodyAdmissionObserved = false }, ReasonBodyAdmissionEvidenceMissing},
		{"preflight absent", func(input *Input) { input.Gateway.PreflightObserved = false }, ReasonPreflightEvidenceMissing},
		{"route plan absent", func(input *Input) { input.Gateway.RoutePlanObserved = false }, ReasonRoutePlanEvidenceMissing},
		{"attempt terminal absent", func(input *Input) { input.Gateway.AttemptTerminalObserved = false }, ReasonAttemptTerminalEvidenceMissing},
		{"single writer absent", func(input *Input) { input.Handover.SingleGatewayWriterVerified = false }, ReasonSingleWriterEvidenceMissing},
		{"node drain absent", func(input *Input) { input.Handover.NodeDrainVerified = false }, ReasonNodeDrainEvidenceMissing},
		{"proxy dispatch absent", func(input *Input) { input.Handover.ProxyDispatchVerified = false }, ReasonProxyDispatchEvidenceMissing},
		{"upstream smoke absent", func(input *Input) { input.Handover.UpstreamSmokeVerified = false }, ReasonUpstreamSmokeEvidenceMissing},
		{"rollback evidence absent", func(input *Input) { input.Handover.RollbackEvidenceVerified = false }, ReasonRollbackEvidenceMissing},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := completeInput()
			test.mutate(&input)
			got := Evaluate(input)
			if got.Decision() != DecisionNotEligible || got.ReasonCode() != test.want {
				t.Fatalf("Evaluate() = (%q, %q), want (%q, %q)", got.Decision(), got.ReasonCode(), DecisionNotEligible, test.want)
			}
		})
	}
}

func TestEvaluateUsesDeterministicReasonPriority(t *testing.T) {
	t.Parallel()
	input := Input{}
	got := Evaluate(input)
	if got.Decision() != DecisionNotEligible || got.ReasonCode() != ReasonEpochMissing {
		t.Fatalf("Evaluate(zero) = (%q, %q), want (%q, %q)", got.Decision(), got.ReasonCode(), DecisionNotEligible, ReasonEpochMissing)
	}

	input = completeInput()
	input.ModelsOnlySurface = true
	input.Version.ObservedEpoch = ""
	if got := Evaluate(input); got.ReasonCode() != ReasonModelsOnlySurface {
		t.Fatalf("models-only priority = %q, want %q", got.ReasonCode(), ReasonModelsOnlySurface)
	}
}

func TestEvaluateDoesNotExposePartialEnablement(t *testing.T) {
	t.Parallel()
	input := completeInput()
	input.Gateway.AttemptTerminalObserved = false
	result := Evaluate(input)
	if result.Decision() != DecisionNotEligible {
		t.Fatalf("partial gateway evidence decision = %q, want %q", result.Decision(), DecisionNotEligible)
	}
	if result.ReasonCode() != ReasonAttemptTerminalEvidenceMissing {
		t.Fatalf("partial gateway evidence reason = %q, want %q", result.ReasonCode(), ReasonAttemptTerminalEvidenceMissing)
	}
}

func completeInput() Input {
	return Input{
		Version: VersionEvidence{
			ExpectedEpoch:         "node-production-2026-07-18",
			ObservedEpoch:         "node-production-2026-07-18",
			ExpectedSchemaVersion: 82,
			ObservedSchemaVersion: 82,
		},
		Route: RouteOwnerRollbackEvidence{
			RouteID:               "POST /v1/chat/completions",
			RollbackRouteID:       "POST /v1/chat/completions",
			CurrentOwner:          OwnerGo,
			RollbackOwner:         OwnerNode,
			RouteBindingVerified:  true,
			RollbackRouteVerified: true,
		},
		Gateway: GatewayPathEvidence{
			NonModelsGatewayTrafficObserved: true,
			GoListenerObserved:              true,
			BodyAdmissionObserved:           true,
			PreflightObserved:               true,
			RoutePlanObserved:               true,
			AttemptTerminalObserved:         true,
		},
		Handover: HandoverEvidence{
			SingleGatewayWriterVerified: true,
			NodeDrainVerified:           true,
			ProxyDispatchVerified:       true,
			UpstreamSmokeVerified:       true,
			RollbackEvidenceVerified:    true,
		},
	}
}
