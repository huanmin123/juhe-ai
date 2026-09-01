package modelcheckprobe

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckprofile"
)

func TestUniqueIdentityModels(t *testing.T) {
	if got := uniqueModels("gpt-5.6-sol", "gpt-5.6-sol", "gpt-5.6-terra"); !reflect.DeepEqual(got, []string{"gpt-5.6-sol", "gpt-5.6-terra"}) {
		t.Fatalf("models=%v", got)
	}
}

func TestRunIdentityForModelsRespectsSupportedModelFence(t *testing.T) {
	seen := map[string]bool{}
	_, err := RunIdentityForModels(context.Background(), modelcheckprofile.ProtocolOpenAIResponses, "gpt-5.6-terra", []string{"gpt-5.6-terra", "gpt-5.6-luna"}, func(_ context.Context, request Request) (Result, error) {
		var payload struct {
			Model string `json:"model"`
		}
		if err := json.Unmarshal(request.Body, &payload); err != nil {
			t.Fatal(err)
		}
		seen[payload.Model] = true
		return Result{HTTPStatus: 200, Success: true, ObservedModel: payload.Model, Output: "{}"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if seen["gpt-5.6-sol"] || !seen["gpt-5.6-terra"] || !seen["gpt-5.6-luna"] {
		t.Fatalf("identity requested outside supported models: %v", seen)
	}
}

func TestPairedIdentityModelsMatchesNodeFamilies(t *testing.T) {
	tests := []struct {
		model string
		want  []string
	}{
		{model: "gpt-5.6-sol", want: []string{"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna"}},
		{model: "gpt-5.6-terra", want: []string{"gpt-5.6-terra", "gpt-5.6-sol", "gpt-5.6-luna"}},
		{model: "gpt-5.6-luna", want: []string{"gpt-5.6-luna", "gpt-5.6-sol", "gpt-5.6-terra"}},
		{model: "gpt-5.5", want: []string{"gpt-5.5", "gpt-5.4"}},
		{model: "gpt-5.4", want: []string{"gpt-5.4", "gpt-5.5"}},
		{model: "gpt-5.5-mini", want: []string{"gpt-5.5-mini"}},
		{model: "custom", want: []string{"custom"}},
	}
	for _, test := range tests {
		t.Run(test.model, func(t *testing.T) {
			if got := pairedIdentityModels(test.model); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("models=%v, want=%v", got, test.want)
			}
		})
	}
}

func TestIdentityToolSchemaRequiresExactIDs(t *testing.T) {
	valid := `{"action":"inspect","payload":{"ids":[2,7,9],"dryRun":true},"tag":"CANARY-ABC123"}`
	if !identityPassed("tool_schema", valid, "CANARY-ABC123") {
		t.Fatal("exact ids should pass")
	}
	for _, output := range []string{
		`{"action":"inspect","payload":{"ids":[2,7,8],"dryRun":true},"tag":"CANARY-ABC123"}`,
		`{"action":"inspect","payload":{"ids":[9,7,2],"dryRun":true},"tag":"CANARY-ABC123"}`,
		`{"action":"inspect","payload":{"ids":[2,7,9,11],"dryRun":true},"tag":"CANARY-ABC123"}`,
	} {
		if identityPassed("tool_schema", output, "CANARY-ABC123") {
			t.Fatalf("invalid ids should fail: %s", output)
		}
	}
}
