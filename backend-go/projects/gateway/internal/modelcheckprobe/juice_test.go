package modelcheckprobe

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckprofile"
)

func TestJuiceRequestsMatchGPT56Contract(t *testing.T) {
	requests, coverage, err := JuiceRequestsForStream("gpt-5.6-sol", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 6 || coverage == "" || coverage == "0" {
		t.Fatalf("requests=%d coverage=%q", len(requests), coverage)
	}
	var nonce string
	for index, request := range requests {
		if request.Path != "/v1/responses" || request.EndpointMode != modelcheckprofile.EndpointModeResponsesSSE {
			t.Fatalf("request %d routing=%+v", index, request)
		}
		var payload map[string]any
		if err := json.Unmarshal(request.Body, &payload); err != nil {
			t.Fatalf("request %d body: %v", index, err)
		}
		if payload["stream"] != true || payload["max_output_tokens"] != float64(16) || payload["temperature"] != float64(0) {
			t.Fatalf("request %d bounded fields=%+v", index, payload)
		}
		if payload["instructions"] == "" {
			t.Fatalf("request %d missing instructions", index)
		}
		reasoning, ok := payload["reasoning"].(map[string]any)
		if !ok || reasoning["effort"] != "high" {
			t.Fatalf("request %d reasoning=%+v", index, payload["reasoning"])
		}
		include, ok := payload["include"].([]any)
		if !ok || len(include) != 1 || include[0] != "reasoning.encrypted_content" {
			t.Fatalf("request %d include=%+v", index, payload["include"])
		}
		input, ok := payload["input"].([]any)
		if !ok || len(input) != 1 {
			t.Fatalf("request %d input=%+v", index, payload["input"])
		}
		message, ok := input[0].(map[string]any)
		if !ok {
			t.Fatalf("request %d input message=%+v", index, input[0])
		}
		content, ok := message["content"].([]any)
		if !ok || len(content) != 1 {
			t.Fatalf("request %d content=%+v", index, message["content"])
		}
		part := content[0].(map[string]any)
		prompt, _ := part["text"].(string)
		if index == 1 {
			start := strings.Index(prompt, "Trace ")
			if start < 0 {
				t.Fatalf("nonce missing from request 2: %q", prompt)
			}
			nonce = strings.TrimSuffix(strings.Fields(prompt[start+len("Trace "):])[0], ".")
		}
		if index == 2 && (nonce == "" || !strings.Contains(prompt, `"trace":"`+nonce+`"`)) {
			t.Fatalf("nonce not carried into request 3: nonce=%q prompt=%q", nonce, prompt)
		}
		if index == 5 && !strings.Contains(payload["instructions"].(string), "Juice="+coverage) {
			t.Fatalf("coverage instruction=%q coverage=%q", payload["instructions"], coverage)
		}
	}
}

func TestEvaluateStreamUsesIndependentOutputContract(t *testing.T) {
	item := EvaluateStream(Result{Success: true, HTTPStatus: 200, ObservedModel: "gpt-5.6-sol", Output: "STREAM-OK"}, "gpt-5.6-sol")
	if item.Kind != "responses_stream" || item.Status != "passed" || item.Evidence["outputMatches"] != true {
		t.Fatalf("stream=%#v", item)
	}
}

func TestJuiceScopeAndStrongAnomaly(t *testing.T) {
	if !ShouldRunJuice("gpt-5.6-sol", "full", "openai_responses") || ShouldRunJuice("gpt-5.6-sol", "quick", "openai_responses") {
		t.Fatal("juice scope mismatch")
	}
	results := []Result{{Success: true, Output: "32"}, {Success: true, Output: "32"}, {Success: true, Output: "32"}, {Success: true, Output: "32"}, {Success: true, Output: "48"}, {Success: true, Output: "77777"}}
	item := EvaluateJuice("gpt-5.6-sol", results, "77777")
	if item.Status != "failed" || item.Evidence["scorePenalty"] != JuiceStrongPenalty || item.Evidence["strongAnomaly"] != true {
		t.Fatalf("juice=%#v", item)
	}
}

func TestEvaluateJuiceMarksOutOfScopeAsNotApplicable(t *testing.T) {
	item := EvaluateJuice("claude-opus-5", nil, "12345")
	if item.Status != "skipped" || item.Evidence["notApplicable"] != true || item.Evidence["excludedFromScoring"] != true {
		t.Fatalf("juice=%#v", item)
	}
}

func TestEvaluateJuiceTerminalHTTPFailureIsExcluded(t *testing.T) {
	item := EvaluateJuice("gpt-5.6-sol", []Result{
		{Success: true, HTTPStatus: 200, Output: "40"},
		{Success: false, HTTPStatus: 503, RetryAttemptCount: 2, RetryMaxAttempts: 3, AttemptStatusCodes: []int{503, 503, 503}},
	}, "77777")
	if item.Status != "skipped" || item.Evidence["terminalFailure"] != true || item.Evidence["excludedFromScoring"] != true {
		t.Fatalf("juice=%#v", item)
	}
}

func TestEvaluateJuiceRejectsFixedCoverageSentinel(t *testing.T) {
	item := EvaluateJuice("gpt-5.6-sol", []Result{{Success: true, HTTPStatus: 200, Output: "0"}}, "0")
	if item.Status != "skipped" || item.Evidence["evidenceInsufficient"] != true || item.Evidence["reason"] != "juice_coverage_value_invalid" {
		t.Fatalf("juice=%#v", item)
	}
}
