package modelcheckprobe

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckprofile"
)

type deterministicTokenizer struct{}

func (deterministicTokenizer) Version() string { return "test-tokenizer-v1" }
func (deterministicTokenizer) Count(value string) (int, error) {
	// The fixture treats the fixed prefix as one token and each " x" as one;
	// this mirrors the exact-padding contract without shipping a tokenizer.
	count := 1
	for index := 0; index+1 < len(value); index++ {
		if value[index] == ' ' && value[index+1] == 'x' {
			count++
		}
	}
	return count, nil
}

type deterministicLimits struct{}

func (deterministicLimits) Version() string { return "limits-v1" }
func (deterministicLimits) MaxInputTokens(string, string, modelcheckprofile.Protocol) (int, error) {
	return 12000, nil
}

func TestRunTokenIntegrityRequiresUsageAndUsesVersionedSnapshot(t *testing.T) {
	item, err := RunTokenIntegrity(context.Background(), modelcheckprofile.ProtocolOpenAIResponses, "gpt-5.6-sol", deterministicTokenizer{}, func(_ context.Context, request Request) (Result, error) {
		var body struct {
			Input string `json:"input"`
		}
		if err := json.Unmarshal(request.Body, &body); err != nil {
			t.Fatal(err)
		}
		count := 0
		for index := 0; index+1 < len(body.Input); index++ {
			if body.Input[index] == ' ' && body.Input[index+1] == 'x' {
				count++
			}
		}
		return Result{Success: true, HTTPStatus: 200, ExpectedModel: request.ExpectedModel, ObservedModel: request.ExpectedModel, Output: "OK", Usage: map[string]any{"input_tokens": float64(count + 1)}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if item.Kind != "token_integrity" || item.Status != "passed" || item.Score != 10 || item.Evidence["tokenizerVersion"] != "test-tokenizer-v1" {
		t.Fatalf("item=%+v", item)
	}
}

func TestRunTokenIntegrityWithoutSnapshotIsExcluded(t *testing.T) {
	item, err := RunTokenIntegrity(context.Background(), modelcheckprofile.ProtocolOpenAIResponses, "gpt-5.6-sol", nil, nil)
	if err != nil || item.Status != "skipped" || item.Evidence["excludedFromScoring"] != true {
		t.Fatalf("item=%+v err=%v", item, err)
	}
}

func TestRunTokenIntegrityTerminalFailureStopsPaddingAndRounds(t *testing.T) {
	requests := 0
	item, err := RunTokenIntegrity(context.Background(), modelcheckprofile.ProtocolOpenAIResponses, "gpt-5.6-sol", deterministicTokenizer{}, func(_ context.Context, _ Request) (Result, error) {
		requests++
		return Result{Success: false, HTTPStatus: 503, ErrorMessage: "upstream unavailable"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if requests != 1 || item.Status != "skipped" || item.Evidence["partial"] != true || item.Evidence["terminalFailure"] != true || item.Evidence["requestCount"] != 1 {
		t.Fatalf("requests=%d item=%+v", requests, item)
	}
}

func TestRunTokenIntegrityWarningDoesNotDiluteScore(t *testing.T) {
	item, err := RunTokenIntegrity(context.Background(), modelcheckprofile.ProtocolOpenAIResponses, "gpt-5.6-sol", deterministicTokenizer{}, func(_ context.Context, request Request) (Result, error) {
		var body struct {
			Input string `json:"input"`
		}
		if err := json.Unmarshal(request.Body, &body); err != nil {
			t.Fatal(err)
		}
		local := strings.Count(body.Input, " x") + 1
		reported := ((local + 63) / 64) * 64
		return Result{Success: true, HTTPStatus: 200, ExpectedModel: request.ExpectedModel, ObservedModel: request.ExpectedModel, Output: "OK", Usage: map[string]any{"input_tokens": float64(reported)}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if item.Status != "warning" || item.Score != 0 || item.MaxScore != 0 {
		t.Fatalf("item=%+v", item)
	}
}

func TestRunLongContextRequiresVersionedLimitAndPreservesMarker(t *testing.T) {
	item, err := RunLongContext(context.Background(), "openai", "gpt-5.6-sol", modelcheckprofile.ProtocolOpenAIResponses, deterministicTokenizer{}, deterministicLimits{}, func(_ context.Context, request Request) (Result, error) {
		var body struct {
			Input string `json:"input"`
		}
		if err := json.Unmarshal(request.Body, &body); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(body.Input, "NEEDLE-") {
			t.Fatalf("long context marker missing from prompt=%q", body.Input)
		}
		return Result{Success: true, HTTPStatus: 200, ExpectedModel: request.ExpectedModel, ObservedModel: request.ExpectedModel, Output: "NEEDLE", Usage: map[string]any{}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if item.Kind != "long_context" || item.Evidence["tokenizerVersion"] != "test-tokenizer-v1" || item.Evidence["limitVersion"] != "limits-v1" {
		t.Fatalf("item=%+v", item)
	}
}
