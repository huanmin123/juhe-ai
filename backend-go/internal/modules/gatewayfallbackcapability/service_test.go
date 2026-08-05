package gatewayfallbackcapability

import (
	"testing"

	"juhe-ai/backend-go/internal/modules/gatewaycandidatewindow"
	"juhe-ai/backend-go/internal/modules/gatewayfallbackpolicy"
	"juhe-ai/backend-go/internal/modules/gatewayingress"
	"juhe-ai/backend-go/internal/modules/gatewayrouteplan"
	protocolgateway "juhe-ai/backend-go/internal/protocols/gateway"
	"juhe-ai/backend-go/internal/store/port"
)

func TestServiceFiltersByRegisteredProtocolURLAndEndpointMode(t *testing.T) {
	service := newService(t)
	input := capabilityInput(protocolgateway.ProtocolOpenAI, protocolgateway.RequestShape{Method: "POST", Path: "/v1/chat/completions", Model: "gpt", Stream: true})
	input.Candidates = []gatewaycandidatewindow.Candidate{
		candidate("allowed", "openai", "openai", "v1", []string{"chat_sse"}),
		candidate("wrong-mode", "openai", "openai", "v1", []string{"chat_json"}),
		candidate("unknown-provider", "unknown", "openai", "v1", []string{"chat_sse"}),
		candidate("invalid-url", "openai", "openai", "v1", []string{"chat_sse"}),
	}
	input.Candidates[3].DefaultBaseURL = "file:///socket"
	result, err := service.FilterFallbackCapability(t.Context(), input)
	if err != nil || len(result.CandidateAccountIDs) != 1 || result.CandidateAccountIDs[0] != "allowed" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestServiceRejectsUnknownEndpointFactsAndUnavailableBridge(t *testing.T) {
	service := newService(t)
	input := capabilityInput(protocolgateway.ProtocolOpenAI, protocolgateway.RequestShape{Method: "POST", Path: "/v1/responses", Model: "source"})
	invalid := candidate("invalid", "openai", "openai", "v1", []string{"unknown"})
	if _, err := service.FilterFallbackCapability(t.Context(), withCandidates(input, invalid)); err == nil {
		t.Fatal("unknown endpoint mode succeeded")
	}
	bridged := candidate("bridged", "openai", "openai", "v1", []string{"responses_json"})
	bridged.SupportedModels = []string{"source"}
	bridged.ModelMappings = []gatewaycandidatewindow.ModelMapping{{Enabled: true, SourceModel: "source", SourceEndpointFamily: "responses", UpstreamModel: "upstream", UpstreamEndpointFamily: "chat_completions"}}
	result, err := service.FilterFallbackCapability(t.Context(), withCandidates(input, bridged))
	if err != nil || len(result.CandidateAccountIDs) != 0 {
		t.Fatalf("bridge result=%+v err=%v", result, err)
	}
}

func TestServiceRequiresExactProviderProfileIdentity(t *testing.T) {
	service := newService(t)
	input := capabilityInput(protocolgateway.ProtocolOpenAI, protocolgateway.RequestShape{Method: "POST", Path: "/v1/chat/completions", Model: "gpt", Stream: true})
	missing := candidate("missing", "openai", "openai", "v1", []string{"chat_sse"})
	missing.Projection.ProviderProtocolProfileID = ""
	wrong := candidate("wrong", "openai", "openai", "v1", []string{"chat_sse"})
	wrong.Projection.ProviderProtocolProfileID = "profile_gpt_openai_v1"
	wrongProtocol := candidate("wrong-protocol", "openai", "openai", "v1", []string{"chat_sse"})
	wrongProtocol.Projection.ProtocolCode = "anthropic"
	wrongVersion := candidate("wrong-version", "openai", "openai", "v1", []string{"chat_sse"})
	wrongVersion.Projection.ProtocolVersion = "v1beta"
	result, err := service.FilterFallbackCapability(t.Context(), withCandidates(input, missing, wrong, wrongProtocol, wrongVersion))
	if err != nil || len(result.CandidateAccountIDs) != 0 {
		t.Fatalf("profile identity result=%+v err=%v", result, err)
	}
}

func TestServiceRejectsMismatchedAuthorizedResourceProfile(t *testing.T) {
	service := newService(t)
	input := capabilityInput(protocolgateway.ProtocolOpenAI, protocolgateway.RequestShape{Method: "POST", Path: "/v1/chat/completions", Model: "gpt", Stream: true})
	candidate := candidate("view", "openai", "openai", "v1", []string{"chat_sse"})
	candidate.Projection.ResourceAccountID = "resource"
	candidate.Projection.ResourceProviderCode = "openai"
	candidate.Projection.ResourceProviderProtocolProfileID = "profile_gpt_openai_v1"
	candidate.Projection.ResourceProtocolCode = "openai"
	candidate.Projection.ResourceProtocolVersion = "v1"
	candidate.Projection.ResourceType = "api_key"
	result, err := service.FilterFallbackCapability(t.Context(), withCandidates(input, candidate))
	if err != nil || len(result.CandidateAccountIDs) != 0 {
		t.Fatalf("resource profile result=%+v err=%v", result, err)
	}
}

func TestServicePreservesAuthorizedResourceIdentityAndRejectsUnmaterializedCodexContext(t *testing.T) {
	service := newService(t)
	input := capabilityInput(protocolgateway.ProtocolOpenAI, protocolgateway.RequestShape{Method: "POST", Path: "/v1/responses", Model: "gpt", Stream: true})
	authorized := candidate("view", "unknown", "unknown", "v1", []string{"responses_sse"})
	authorized.Projection.ResourceAccountID = "resource"
	authorized.Projection.ResourceProviderCode = "openai"
	authorized.Projection.ResourceProviderProtocolProfileID = "profile_openai_openai_v1"
	authorized.Projection.ResourceProtocolCode = "openai"
	authorized.Projection.ResourceProtocolVersion = "v1"
	authorized.Projection.ResourceType = "api_key"
	result, err := service.FilterFallbackCapability(t.Context(), withCandidates(input, authorized))
	if err != nil || len(result.CandidateAccountIDs) != 1 {
		t.Fatalf("authorized result=%+v err=%v", result, err)
	}
	input.RequestClientCompatibility = string(protocolgateway.CompatibilityCodexResponses)
	result, err = service.FilterFallbackCapability(t.Context(), withCandidates(input, authorized))
	if err != nil || len(result.CandidateAccountIDs) != 0 {
		t.Fatalf("codex context result=%+v err=%v", result, err)
	}
}

func TestEndpointModeCoversNativeThreeProtocolStreams(t *testing.T) {
	for _, testCase := range []struct {
		protocol protocolgateway.ProtocolCode
		request  protocolgateway.RequestShape
		want     string
	}{
		{protocolgateway.ProtocolOpenAI, protocolgateway.RequestShape{Method: "POST", Path: "/v1/responses", Stream: true}, "responses_sse"},
		{protocolgateway.ProtocolAnthropic, protocolgateway.RequestShape{Method: "POST", Path: "/v1/messages"}, "messages_json"},
		{protocolgateway.ProtocolGemini, protocolgateway.RequestShape{Method: "POST", Path: "/v1beta/models/gemini:streamGenerateContent"}, "generate_content_sse"},
	} {
		got, ok := endpointMode(testCase.request, testCase.protocol)
		if !ok || got != testCase.want {
			t.Fatalf("endpointMode(%s, %+v)=%q/%t, want %q", testCase.protocol, testCase.request, got, ok, testCase.want)
		}
	}
}

func newService(t *testing.T) *Service {
	t.Helper()
	service, err := NewService()
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func capabilityInput(protocol protocolgateway.ProtocolCode, request protocolgateway.RequestShape) gatewayfallbackpolicy.CapabilityInput {
	return gatewayfallbackpolicy.CapabilityInput{
		Target: gatewayrouteplan.FallbackTarget{}, Window: gatewaycandidatewindow.Window{Access: port.GatewayGroupAccess{GroupID: "target", CallerSystemAccountID: "caller"}},
		Intent: gatewayingress.RequestIntent{}, RequestShape: request, Protocol: protocol, FinalLane: gatewayingress.LaneText,
		RequestedModel: request.Model, EndpointFamily: string(protocolgateway.EndpointFamilyFromPath(protocol, request.Path)), RequestClientCompatibility: string(protocolgateway.CompatibilityOpenAIStandard),
	}
}

func withCandidates(input gatewayfallbackpolicy.CapabilityInput, candidates ...gatewaycandidatewindow.Candidate) gatewayfallbackpolicy.CapabilityInput {
	input.Candidates = candidates
	return input
}

func candidate(id, provider, protocol, version string, modes []string) gatewaycandidatewindow.Candidate {
	profileID := ""
	for id, identity := range supportedProviderProfiles {
		if identity.provider == provider && string(identity.protocol) == protocol && identity.version == version {
			profileID = id
			break
		}
	}
	return gatewaycandidatewindow.Candidate{
		Projection:     port.GatewayAccountCandidate{AccountID: id, ProviderCode: provider, ProviderProtocolProfileID: profileID, ProtocolCode: protocol, ProtocolVersion: version, Type: "api_key"},
		DefaultBaseURL: "https://api.example.test", SupportedEndpointModes: modes, EndpointModesComplete: true, SupportedModels: []string{"gpt"},
	}
}
