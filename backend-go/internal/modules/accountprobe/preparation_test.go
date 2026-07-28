package accountprobe

import (
	"errors"
	"testing"

	"juhe-ai/backend-go/internal/modules/gatewaycandidatewindow"
	"juhe-ai/backend-go/internal/store/port"
)

func TestEndpointFamilyForEveryProbeMode(t *testing.T) {
	want := map[EndpointMode]string{
		ModeChatJSON: "chat_completions", ModeChatSSE: "chat_completions",
		ModeResponsesJSON: "responses", ModeResponsesSSE: "responses",
		ModeMessagesJSON: "messages", ModeMessagesSSE: "messages",
		ModeGenerateContentJSON: "generate_content", ModeGenerateContentSSE: "stream_generate_content",
		ModeInteractionsJSON: "interactions", ModeInteractionsSSE: "interactions",
	}
	for mode, family := range want {
		got, ok := EndpointFamilyForMode(mode)
		if !ok || string(got) != family {
			t.Fatalf("EndpointFamilyForMode(%q) = %q, %v", mode, got, ok)
		}
	}
	if _, ok := EndpointFamilyForMode("future"); ok {
		t.Fatal("future mode unexpectedly accepted")
	}
}

func TestPrepareRequestAppliesDirectAndSameFamilyModel(t *testing.T) {
	direct := gatewaycandidatewindow.Candidate{SupportedModels: []string{"direct"}}
	prepared, err := PrepareRequest(direct, RequestInput{Mode: ModeResponsesJSON, Model: "direct"})
	if err != nil || prepared.Request.Model != "direct" || prepared.Resolution.MappingApplied {
		t.Fatalf("direct = %+v, %v", prepared, err)
	}

	mapped := gatewaycandidatewindow.Candidate{
		Projection:      port.GatewayAccountCandidate{ProviderCode: "gpt", ProtocolCode: "openai", ProtocolVersion: "v1"},
		SupportedModels: []string{"upstream"},
		ModelMappings: []gatewaycandidatewindow.ModelMapping{{
			ProviderCode: "gpt", SourceModel: "client", SourceEndpointFamily: "responses",
			UpstreamModel: "upstream", UpstreamEndpointFamily: "responses", Enabled: true,
		}},
	}
	prepared, err = PrepareRequest(mapped, RequestInput{Mode: ModeResponsesSSE, Model: "client"})
	if err != nil || prepared.Request.Model != "upstream" || !prepared.Resolution.MappingApplied {
		t.Fatalf("mapped = %+v, %v", prepared, err)
	}
}

func TestPrepareRequestAllowsGeminiStreamFamilySpecialCase(t *testing.T) {
	candidate := gatewaycandidatewindow.Candidate{
		Projection:      port.GatewayAccountCandidate{ProviderCode: "gemini", ProtocolCode: "gemini", ProtocolVersion: "v1beta"},
		SupportedModels: []string{"gemini-upstream"},
		ModelMappings: []gatewaycandidatewindow.ModelMapping{{
			ProviderCode: "gemini", SourceModel: "gemini-client", SourceEndpointFamily: "stream_generate_content",
			UpstreamModel: "gemini-upstream", UpstreamEndpointFamily: "generate_content", Enabled: true,
		}},
	}
	prepared, err := PrepareRequest(candidate, RequestInput{Mode: ModeGenerateContentSSE, Model: "gemini-client"})
	if err != nil || prepared.Request.PathAndQuery != "/v1beta/models/gemini-upstream:streamGenerateContent?alt=sse" {
		t.Fatalf("prepared = %+v, %v", prepared, err)
	}
}

func TestPrepareRequestRejectsMissingModelAndCrossProtocolBridge(t *testing.T) {
	missing := gatewaycandidatewindow.Candidate{SupportedModels: []string{"other"}}
	if _, err := PrepareRequest(missing, RequestInput{Mode: ModeResponsesJSON, Model: "client"}); !errors.Is(err, ErrModelUnavailable) {
		t.Fatalf("missing error = %v", err)
	}
	cross := gatewaycandidatewindow.Candidate{
		Projection:      port.GatewayAccountCandidate{ProviderCode: "hybrid", ProtocolCode: "openai", ProtocolVersion: "v1"},
		SupportedModels: []string{"upstream"},
		ModelMappings: []gatewaycandidatewindow.ModelMapping{{
			ProviderCode: "hybrid", SourceModel: "client", SourceEndpointFamily: "responses",
			UpstreamModel: "upstream", UpstreamEndpointFamily: "messages", Enabled: true,
		}},
	}
	if _, err := PrepareRequest(cross, RequestInput{Mode: ModeResponsesJSON, Model: "client"}); !errors.Is(err, ErrProtocolBridgeRequired) {
		t.Fatalf("bridge error = %v", err)
	}
}
