package gatewaycandidatewindow

import (
	"testing"

	"juhe-ai/backend-go/internal/store/port"
)

func TestResolveEffectiveModelScopesMappingsToEffectiveProvider(t *testing.T) {
	candidate := Candidate{
		Projection: port.GatewayAccountCandidate{
			ProviderCode: "binding-provider", ResourceAccountID: "resource",
			ResourceProviderCode: "resource-provider", ResourceProtocolCode: "openai", ResourceProtocolVersion: "v1",
		},
		SupportedModels: []string{"upstream"},
		ModelMappings: []ModelMapping{
			{ProviderCode: "binding-provider", SourceModel: "client", SourceEndpointFamily: "responses", UpstreamModel: "wrong", UpstreamEndpointFamily: "responses", Enabled: true},
			{ProviderCode: "resource-provider", SourceModel: "client", SourceEndpointFamily: "responses", UpstreamModel: "upstream", UpstreamEndpointFamily: "responses", Enabled: true},
		},
	}
	resolution, ok := ResolveEffectiveModel(candidate, "client", "responses")
	if !ok || !resolution.MappingApplied || resolution.UpstreamModel != "upstream" {
		t.Fatalf("resolution = %+v, ok=%v", resolution, ok)
	}
}

func TestResolveEffectiveModelRejectsCaseMismatchAndUnsupportedUpstream(t *testing.T) {
	candidate := Candidate{
		Projection:      port.GatewayAccountCandidate{ProviderCode: "gpt", ProtocolCode: "openai", ProtocolVersion: "v1"},
		SupportedModels: []string{"GPT-Upstream"},
		ModelMappings: []ModelMapping{{
			ProviderCode: "gpt", SourceModel: "client", SourceEndpointFamily: "responses",
			UpstreamModel: "gpt-upstream", UpstreamEndpointFamily: "responses", Enabled: true,
		}},
	}
	if _, ok := ResolveEffectiveModel(candidate, "CLIENT", "responses"); ok {
		t.Fatal("case-insensitive source mapping unexpectedly matched")
	}
	if _, ok := ResolveEffectiveModel(candidate, "client", "responses"); ok {
		t.Fatal("case-insensitive upstream supported-model unexpectedly matched")
	}
}
