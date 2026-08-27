package modelcheckprobe

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckprofile"
)

// Suite runs the ordered credential-free core probes. It stops after a
// transport failure so that partial evidence is explicit and never treated as
// a complete quality fact.
type Suite struct {
	Endpoint string
	Headers  http.Header
	Model    string
	Profile  string
	Protocol modelcheckprofile.Protocol
	Stream   bool
}

func RunSuite(ctx context.Context, input Suite, timeout time.Duration) ([]Evaluation, error) {
	results := make([]Result, 0, 4)
	requests := []Request{}
	basic, err := BuildBasic(input.Protocol, input.Model, "Reply with exactly: OK-MODEL-CHECK", input.Stream)
	if err != nil {
		return nil, err
	}
	requests = append(requests, basic)
	structured, err := BuildStructured(input.Protocol, input.Model, input.Stream)
	if err != nil {
		return nil, err
	}
	requests = append(requests, structured)
	tool, err := BuildTool(input.Protocol, input.Model, input.Stream)
	if err != nil {
		return nil, err
	}
	requests = append(requests, tool)
	for round := 0; round < 3; round++ {
		stability, stabilityErr := StabilityRequest(input.Protocol, input.Model, input.Stream)
		if stabilityErr != nil {
			return nil, stabilityErr
		}
		requests = append(requests, stability)
	}
	for _, request := range requests {
		result, executeErr := Execute(ctx, request, Options{Endpoint: input.Endpoint, Headers: input.Headers, Timeout: timeout})
		if executeErr != nil {
			return nil, executeErr
		}
		results = append(results, result)
		if !result.Success {
			break
		}
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("J3b probe suite produced no results")
	}
	items := make([]Evaluation, 0, len(results)+1)
	items = append(items, EvaluateBasic(results[0], input.Model))
	if len(results) > 1 {
		items = append(items, EvaluateStructured(results[1], input.Model))
	}
	if len(results) > 2 {
		items = append(items, EvaluateTool(results[2], input.Model))
	}
	if len(results) > 3 {
		stabilityResults := results[3:]
		items = append(items, EvaluateStability(stabilityResults, input.Model))
	}
	if input.Profile == "full" {
		behavior, behaviorErr := RunBehavior(ctx, input.Protocol, input.Model, func(runCtx context.Context, request Request) (Result, error) {
			return Execute(runCtx, request, Options{Endpoint: input.Endpoint, Headers: input.Headers, Timeout: timeout})
		})
		if behaviorErr != nil {
			return nil, behaviorErr
		}
		items = append(items, behavior)
		identity, identityErr := RunIdentity(ctx, input.Protocol, input.Model, func(runCtx context.Context, request Request) (Result, error) {
			return Execute(runCtx, request, Options{Endpoint: input.Endpoint, Headers: input.Headers, Timeout: timeout})
		})
		if identityErr != nil {
			return nil, identityErr
		}
		items = append(items, identity)
		items = append(items,
			Evaluation{Kind: "token_integrity", Status: "skipped", Evidence: map[string]any{"evidenceInsufficient": true, "excludedFromScoring": true, "reason": "tokenizer_snapshot_not_attached"}},
			Evaluation{Kind: "long_context", Status: "skipped", Evidence: map[string]any{"evidenceInsufficient": true, "excludedFromScoring": true, "reason": "model_limit_snapshot_not_attached"}},
		)
		if ShouldRunJuice(input.Model, input.Profile, string(input.Protocol)) {
			juiceResults := make([]Result, 0, 6)
			for _, request := range JuiceRequests(input.Model) {
				result, executeErr := Execute(ctx, request, Options{Endpoint: input.Endpoint, Headers: input.Headers, Timeout: timeout})
				if executeErr != nil {
					return nil, executeErr
				}
				juiceResults = append(juiceResults, result)
			}
			items = append(items, EvaluateJuice(input.Model, juiceResults, "0"))
		} else {
			items = append(items, Evaluation{Kind: "juice", Status: "skipped", Evidence: map[string]any{"evidenceInsufficient": true, "excludedFromScoring": true, "reason": "juice_scope_not_applicable"}})
		}
		items = append(items,
			Evaluation{Kind: "distribution", Status: "skipped", Evidence: map[string]any{"evidenceInsufficient": true, "excludedFromScoring": true, "reason": "trusted_comparison_not_attached"}},
			Evaluation{Kind: "cross_model", Status: "skipped", Evidence: map[string]any{"evidenceInsufficient": true, "excludedFromScoring": true, "reason": "trusted_comparison_not_attached"}},
		)
	}
	items = append(items, EvaluateUsage(results))
	return items, nil
}
