package modelcheckprobe

import (
	"context"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckprofile"
)

func TestPairedIdentityModelsMatchesNodeFamilies(t *testing.T) {
	if got := PairedIdentityModels("gpt-5.6-sol"); len(got) != 3 || got[0] != "gpt-5.6-sol" {
		t.Fatalf("gpt56 family=%v", got)
	}
	if got := PairedIdentityModels("gpt-5.6-terra"); len(got) != 3 || got[0] != "gpt-5.6-terra" {
		t.Fatalf("gpt56 family should probe target first=%v", got)
	}
	if got := PairedIdentityModels("custom"); len(got) != 1 || got[0] != "custom" {
		t.Fatalf("custom family=%v", got)
	}
}

func TestRunIdentityObservationProducesFeatureEvidence(t *testing.T) {
	calls := 0
	item, observations, err := RunIdentityObservation(context.Background(), IdentityProbeInput{Model: "gpt-5.6-sol", Protocol: modelcheckprofile.ProtocolOpenAIResponses, RunProbe: func(context.Context, Request) (ProbeResult, error) {
		calls++
		return ProbeResult{HTTPStatusCode: 200, Success: true, Response: ParsedResponse{Model: "gpt-5.6-sol", OutputText: "{\"result\":42,\"tag\":\"CANARY-ABCDEF\"}", Usage: map[string]any{"output_tokens": float64(4)}}}, nil
	}})
	if err != nil {
		t.Fatalf("RunIdentityObservation: %v", err)
	}
	if calls == 0 || len(observations) != calls || item.ItemType != "identity_observation" {
		t.Fatalf("unexpected identity output calls=%d observations=%d item=%+v", calls, len(observations), item)
	}
	if _, ok := item.Evidence["featureVersion"]; !ok {
		t.Fatalf("missing feature version evidence=%v", item.Evidence)
	}
}
