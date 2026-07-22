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
		candidate("unrestricted", readyCapability(), nil),
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

	if got, want := candidateIDs(result.Candidates), []string{"direct", "mapping", "unrestricted"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("filtered candidate ids = %#v, want %#v", got, want)
	}
	if result.Capability.SkippedCount != 1 || result.Model.DirectMatchedCount != 1 || result.Model.MappingMatchedCount != 1 || result.Model.UnrestrictedAccountCount != 1 {
		t.Fatalf("unexpected filter counts: %#v", result)
	}
	if result.Model.Priority.RankByCandidateID["direct"] != ModelPriorityDirect || result.Model.Priority.RankByCandidateID["mapping"] != ModelPriorityMapping {
		t.Fatalf("unexpected priority: %#v", result.Model.Priority.RankByCandidateID)
	}
}

func TestFilterModelCandidatesAcceptsProtocolRegistryEndpointFamily(t *testing.T) {
	result := FilterModelCandidates(
		[]Candidate{candidate("unrestricted", readyCapability(), nil)},
		"gpt-5",
		gatewayprotocol.EndpointResponses,
	)
	if got, want := candidateIDs(result.Candidates), []string{"unrestricted"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("candidate ids = %#v, want %#v", got, want)
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

func TestFilterModelCandidatesOrdersDirectMappingThenUnrestricted(t *testing.T) {
	candidates := []Candidate{
		candidate("unrestricted", readyCapability(), nil),
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
	if got, want := candidateIDs(result.Candidates), []string{"direct", "mapping", "unrestricted"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("candidate ids = %#v, want %#v", got, want)
	}
	if result.SkippedCount != 1 || result.LimitedAccountCount != 3 || result.UnrestrictedAccountCount != 1 || result.DirectMatchedCount != 1 || result.MappingMatchedCount != 1 {
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

	result := FilterModelCandidates(candidates, "gpt-5", EndpointFamilyResponses)
	if len(result.Candidates) != 0 || result.Reason != ModelMismatchUnsupported {
		t.Fatalf("result = %#v, want unsupported model", result)
	}
}

func TestFilterModelCandidatesReportsMissingModelOnlyWhenAllCandidatesAreLimited(t *testing.T) {
	limited := []Candidate{candidate("strict", readyCapability(), []string{"gpt-5"})}
	result := FilterModelCandidates(limited, "  ", EndpointFamilyResponses)
	if result.Reason != ModelMismatchMissing || len(result.Candidates) != 0 {
		t.Fatalf("limited result = %#v", result)
	}

	result = FilterModelCandidates([]Candidate{candidate("open", readyCapability(), nil)}, "", EndpointFamilyResponses)
	got := candidateIDs(result.Candidates)
	if result.Reason != "" || !reflect.DeepEqual(got, []string{"open"}) {
		t.Fatalf("unrestricted result = %#v", result)
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
