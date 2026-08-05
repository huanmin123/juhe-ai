package gatewaymodelcapability

import (
	"reflect"
	"testing"

	gatewayprotocol "juhe-ai/backend-go/internal/protocols/gateway"
)

func TestFilterCandidatesAppliesCapabilityBeforeModelPriority(t *testing.T) {
	candidates := []Candidate{
		candidate("blocked", Capability{Registered: true, ContextAllowed: true, UpstreamRouteAvailable: true, SupportedEndpointModes: []string{"chat_json"}, ClientCompatibilities: []string{"openai_standard"}}, []string{"gpt-5"}),
		candidate("mapping", readyCapability(), []string{"gpt-4o"}),
		candidate("direct", readyCapability(), []string{"gpt-5"}),
		candidate("invalid", readyCapability(), nil),
	}
	candidates[1].ModelMappings = []ModelMapping{{
		SourceModel: "gpt-5", SourceEndpointFamily: EndpointFamilyResponses,
		UpstreamModel: "gpt-4o", UpstreamEndpointFamily: EndpointFamilyChatCompletions, Enabled: true,
	}}
	candidates[1].ProtocolCode = "openai"
	candidates[1].ProtocolVersion = "v1"

	result := FilterCandidates(FilterInput{
		Candidates:     candidates,
		Capability:     CapabilityRequest{EndpointMode: "responses_json", ClientCompatibility: "codex_responses"},
		RequestedModel: " gpt-5 ", SourceEndpointFamily: EndpointFamilyResponses,
	})

	if got, want := candidateIDs(result.Candidates), []string{"direct", "mapping"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("filtered candidate ids = %#v, want %#v", got, want)
	}
	if result.Capability.SkippedCount != 1 || result.Model.DirectMatchedCount != 1 || result.Model.MappingMatchedCount != 1 || result.Model.InvalidModelConstraintCount != 1 || result.Model.SkippedCount != 1 {
		t.Fatalf("unexpected filter counts: %#v", result)
	}
	if result.Model.Priority.RankByCandidateID["direct"] != ModelPriorityDirect || result.Model.Priority.RankByCandidateID["mapping"] != ModelPriorityMapping {
		t.Fatalf("unexpected priority: %#v", result.Model.Priority.RankByCandidateID)
	}
}

func TestFilterModelCandidatesRejectsEmptySupportedModels(t *testing.T) {
	result := FilterModelCandidates(
		[]Candidate{candidate("invalid", readyCapability(), nil)},
		"gpt-5",
		gatewayprotocol.EndpointResponses,
	)
	if len(result.Candidates) != 0 || result.Reason != ModelMismatchUnsupported || result.InvalidModelConstraintCount != 1 || result.SkippedCount != 1 || result.Priority.RankByCandidateID["invalid"] != ModelPriorityUnsupported {
		t.Fatalf("result = %#v", result)
	}
}

func TestFilterCandidatesReportsCapabilityMismatchBeforeModelMismatch(t *testing.T) {
	result := FilterCandidates(FilterInput{
		Candidates: []Candidate{candidate("only", Capability{Registered: true, ContextAllowed: true, UpstreamRouteAvailable: true, SupportedEndpointModes: []string{"chat_json"}}, []string{"gpt-4o"})},
		Capability: CapabilityRequest{
			EndpointMode: "responses_json",
			EmptyReason:  CapabilityMismatchNativeAnthropicOpenAICompatible,
		},
		RequestedModel: "gpt-5", SourceEndpointFamily: EndpointFamilyResponses,
	})

	if len(result.Candidates) != 0 || result.Capability.Reason != CapabilityMismatchNativeAnthropicOpenAICompatible || result.Model.Reason != "" {
		t.Fatalf("result = %#v, want capability mismatch only", result)
	}
}

func TestFilterModelCandidatesOrdersDirectThenMappingAndSkipsInvalidConstraint(t *testing.T) {
	candidates := []Candidate{
		candidate("invalid", readyCapability(), nil),
		candidate("unsupported", readyCapability(), []string{"gpt-4o-mini"}),
		candidate("mapping", readyCapability(), []string{"gpt-4o"}),
		candidate("direct", readyCapability(), []string{"gpt-5"}),
	}
	candidates[2].ProviderCode = "hybrid"
	candidates[2].ModelMappings = []ModelMapping{{
		SourceModel: "gpt-5", SourceEndpointFamily: EndpointFamilyResponses,
		UpstreamModel: "gpt-4o", UpstreamEndpointFamily: EndpointFamilyMessages, Enabled: true,
	}}

	result := FilterModelCandidates(candidates, "gpt-5", EndpointFamilyResponses)
	if got, want := candidateIDs(result.Candidates), []string{"direct", "mapping"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("candidate ids = %#v, want %#v", got, want)
	}
	if result.SkippedCount != 2 || result.LimitedAccountCount != 3 || result.InvalidModelConstraintCount != 1 || result.DirectMatchedCount != 1 || result.MappingMatchedCount != 1 {
		t.Fatalf("result = %#v", result)
	}
	if result.Priority.RankByCandidateID["unsupported"] != ModelPriorityUnsupported {
		t.Fatalf("unsupported priority = %d", result.Priority.RankByCandidateID["unsupported"])
	}
}

func TestFilterModelCandidatesDoesNotTreatUnsupportedMappingAsEligible(t *testing.T) {
	candidates := []Candidate{
		candidate("identity", readyCapability(), []string{"gpt-4o"}),
		candidate("cross-protocol", readyCapability(), []string{"gpt-4o"}),
		candidate("mapping-without-upstream", readyCapability(), []string{"gpt-4o-mini"}),
	}
	candidates[0].ModelMappings = []ModelMapping{{
		SourceModel: "gpt-5", SourceEndpointFamily: EndpointFamilyResponses,
		UpstreamModel: "gpt-5", UpstreamEndpointFamily: EndpointFamilyResponses, Enabled: true,
	}}
	candidates[1].ModelMappings = []ModelMapping{{
		SourceModel: "gpt-5", SourceEndpointFamily: EndpointFamilyResponses,
		UpstreamModel: "gpt-4o", UpstreamEndpointFamily: EndpointFamilyMessages, Enabled: true,
	}}
	candidates[2].ModelMappings = []ModelMapping{{
		SourceModel: "gpt-5", SourceEndpointFamily: EndpointFamilyResponses,
		UpstreamModel: "gpt-4o", UpstreamEndpointFamily: EndpointFamilyChatCompletions, Enabled: true,
	}}
	candidates[2].SupportedModels = []string{"gpt-5", "gpt-4o-mini"}
	candidates[2].ProviderCode = "hybrid"

	result := FilterModelCandidates(candidates, "gpt-5", EndpointFamilyResponses)
	if len(result.Candidates) != 0 || result.Reason != ModelMismatchUnsupported {
		t.Fatalf("result = %#v, want unsupported model", result)
	}
}

func TestResolveEffectiveModelDoesNotFallBackToDirectAfterUnsupportedMapping(t *testing.T) {
	item := candidate("mapped-but-upstream-unsupported", readyCapability(), []string{"gpt-client"})
	item.ProviderCode = "hybrid"
	item.ModelMappings = []ModelMapping{{
		SourceModel: "gpt-client", SourceEndpointFamily: EndpointFamilyResponses,
		UpstreamModel: "gpt-upstream", UpstreamEndpointFamily: EndpointFamilyChatCompletions, Enabled: true,
	}}
	if _, ok := ResolveEffectiveModel(item, "gpt-client", EndpointFamilyResponses); ok {
		t.Fatal("ResolveEffectiveModel() fell back to source model after unsupported mapping")
	}
	result := FilterModelCandidates([]Candidate{item}, "gpt-client", EndpointFamilyResponses)
	if len(result.Candidates) != 0 || result.Priority.RankByCandidateID[item.ID] != ModelPriorityUnsupported {
		t.Fatalf("model result = %#v", result)
	}
}

func TestFilterModelCandidatesReportsMissingModelForEveryInvalidConstraint(t *testing.T) {
	limited := []Candidate{candidate("strict", readyCapability(), []string{"gpt-5"})}
	result := FilterModelCandidates(limited, "  ", EndpointFamilyResponses)
	if result.Reason != ModelMismatchMissing || len(result.Candidates) != 0 {
		t.Fatalf("limited result = %#v", result)
	}

	result = FilterModelCandidates([]Candidate{candidate("invalid", readyCapability(), nil)}, "", EndpointFamilyResponses)
	if result.Reason != ModelMismatchMissing || len(result.Candidates) != 0 || result.InvalidModelConstraintCount != 1 {
		t.Fatalf("invalid result = %#v", result)
	}
}

func TestResolveEffectiveModelAllowsDirectWithoutFamilyButRequiresExactCase(t *testing.T) {
	item := candidate("direct", readyCapability(), []string{"GPT-5"})
	resolution, ok := ResolveEffectiveModel(item, " GPT-5 ", "")
	if !ok || resolution.MappingApplied || resolution.UpstreamModel != "GPT-5" || resolution.UpstreamEndpointFamily != "" {
		t.Fatalf("resolution = %+v, ok=%v", resolution, ok)
	}
	if _, ok := ResolveEffectiveModel(item, "gpt-5", EndpointFamilyResponses); ok {
		t.Fatal("case-insensitive model unexpectedly matched")
	}
}

func TestResolveEffectiveModelReturnsMappedFamilyAndSource(t *testing.T) {
	item := candidate("mapped", readyCapability(), []string{"gpt-upstream"})
	item.ProviderCode = "hybrid"
	item.ModelMappings = []ModelMapping{{
		SourceModel: "gpt-client", SourceEndpointFamily: EndpointFamilyResponses,
		UpstreamModel: "gpt-upstream", UpstreamEndpointFamily: EndpointFamilyMessages,
		RuntimeSource: "runtime", Enabled: true,
	}}
	resolution, ok := ResolveEffectiveModel(item, "gpt-client", EndpointFamilyResponses)
	if !ok || !resolution.MappingApplied || resolution.UpstreamModel != "gpt-upstream" || resolution.UpstreamEndpointFamily != EndpointFamilyMessages || resolution.MappingSource != "runtime" {
		t.Fatalf("resolution = %+v, ok=%v", resolution, ok)
	}
}

func TestResolveEffectiveModelPrefersMappingOverSameNameDirectModel(t *testing.T) {
	item := candidate("mapped-first", readyCapability(), []string{"gpt-client", "gpt-upstream"})
	item.ModelMappings = []ModelMapping{{
		SourceModel: "gpt-client", SourceEndpointFamily: EndpointFamilyResponses,
		UpstreamModel: "gpt-upstream", UpstreamEndpointFamily: EndpointFamilyResponses, Enabled: true,
	}}
	resolution, ok := ResolveEffectiveModel(item, "gpt-client", EndpointFamilyResponses)
	if !ok || !resolution.MappingApplied || resolution.UpstreamModel != "gpt-upstream" {
		t.Fatalf("resolution = %+v, ok=%v", resolution, ok)
	}
	result := FilterModelCandidates([]Candidate{item}, "gpt-client", EndpointFamilyResponses)
	if result.MappingMatchedCount != 1 || result.DirectMatchedCount != 0 || result.Priority.RankByCandidateID["mapped-first"] != ModelPriorityMapping {
		t.Fatalf("model result = %#v", result)
	}
}

func candidate(id string, capability Capability, models []string) Candidate {
	return Candidate{ID: id, Capability: capability, SupportedModels: models}
}

func readyCapability() Capability {
	return Capability{
		Registered: true, ContextAllowed: true, UpstreamRouteAvailable: true,
		SupportedEndpointModes: []string{"chat_json", "responses_json"},
		ClientCompatibilities:  []string{"openai_standard", "codex_responses"},
	}
}

func candidateIDs(candidates []Candidate) []string {
	output := make([]string, 0, len(candidates))
	for _, item := range candidates {
		output = append(output, item.ID)
	}
	return output
}
