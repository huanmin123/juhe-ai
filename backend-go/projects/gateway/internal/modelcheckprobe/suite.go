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
	Endpoint     string
	ProviderCode string
	Headers      http.Header
	Model        string
	Profile      string
	Protocol     modelcheckprofile.Protocol
	Stream       bool
	// Comparison is an independently resolved, in-process trusted target.
	// It is only used by the full profile; callers must not provide a client
	// that proxies through another service.
	Comparison  *Suite
	Tokenizer   Tokenizer
	ModelLimits ModelLimitSnapshot
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
		tokenIntegrity, tokenErr := RunTokenIntegrity(ctx, input.Protocol, input.Model, input.Tokenizer, func(runCtx context.Context, request Request) (Result, error) {
			return Execute(runCtx, request, Options{Endpoint: input.Endpoint, Headers: input.Headers, Timeout: timeout})
		})
		if tokenErr != nil {
			return nil, tokenErr
		}
		longContext, longErr := RunLongContext(ctx, input.ProviderCode, input.Model, input.Protocol, input.Tokenizer, input.ModelLimits, func(runCtx context.Context, request Request) (Result, error) {
			return Execute(runCtx, request, Options{Endpoint: input.Endpoint, Headers: input.Headers, Timeout: timeout})
		})
		if longErr != nil {
			return nil, longErr
		}
		items = append(items, tokenIntegrity, longContext)
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
		if input.Comparison == nil {
			if results[0].Success {
				crossModel, crossErr := RunSelfCrossModel(ctx, input, results[0], timeout)
				if crossErr != nil {
					return nil, crossErr
				}
				items = append(items, crossModel)
			} else {
				items = append(items, Evaluation{Kind: "cross_model", Status: "skipped", Evidence: map[string]any{"evidenceInsufficient": true, "excludedFromScoring": true, "reason": "target_basic_probe_failed"}})
			}
			items = append(items, Evaluation{Kind: "distribution", Status: "skipped", Evidence: map[string]any{"evidenceInsufficient": true, "excludedFromScoring": true, "reason": "trusted_comparison_not_attached"}})
		} else {
			comparison, comparisonErr := RunTrustedComparison(ctx, input, *input.Comparison, timeout)
			if comparisonErr != nil {
				return nil, comparisonErr
			}
			items = append(items, comparison...)
		}
	}
	items = append(items, EvaluateUsage(results))
	return items, nil
}

// RunSelfCrossModel mirrors Node's non-trusted full check: the paired model is
// sent to the same resolved endpoint, then compared using only response
// metadata. Distribution similarity remains reserved for a trusted account.
func RunSelfCrossModel(ctx context.Context, input Suite, targetBasic Result, timeout time.Duration) (Evaluation, error) {
	pairedModel := modelcheckprofile.PairedModel(input.ProfileForModel(), input.Model)
	if pairedModel == "" {
		return Evaluation{Kind: "cross_model", Status: "skipped", Evidence: map[string]any{"evidenceInsufficient": true, "excludedFromScoring": true, "reason": "no_paired_model"}}, nil
	}
	request, err := BuildBasic(input.Protocol, pairedModel, "Reply with exactly: CROSS-MODEL-OK", input.Stream)
	if err != nil {
		return Evaluation{}, err
	}
	paired, err := Execute(ctx, request, Options{Endpoint: input.Endpoint, Headers: input.Headers, Timeout: timeout})
	if err != nil {
		return Evaluation{}, err
	}
	return EvaluateCrossModelPair(targetBasic, paired, input.Model, pairedModel), nil
}

func (s Suite) ProfileForModel() modelcheckprofile.ProtocolProfile {
	for _, candidate := range modelcheckprofile.Profiles() {
		if candidate.Protocol == s.Protocol {
			for _, model := range candidate.Models {
				if model == s.Model {
					return candidate
				}
			}
		}
	}
	return modelcheckprofile.ProtocolProfile{Protocol: s.Protocol, Models: []string{s.Model}}
}

// RunTrustedComparison directly executes the bounded distribution and model
// identity probes against the target and its independently resolved trusted
// comparison account. Only evaluated summaries are returned; provider output
// is not retained.
func RunTrustedComparison(ctx context.Context, target, comparison Suite, timeout time.Duration) ([]Evaluation, error) {
	if target.Endpoint == "" || target.Model == "" || target.Protocol == "" || comparison.Endpoint == "" || comparison.Model == "" || comparison.Protocol == "" {
		return nil, fmt.Errorf("J3b trusted comparison target is incomplete")
	}
	targetEndpoint, targetHeaders, targetProtocol, targetModel := target.Endpoint, target.Headers, target.Protocol, target.Model
	comparisonEndpoint, comparisonHeaders, comparisonProtocol, comparisonModel := comparison.Endpoint, comparison.Headers, comparison.Protocol, comparison.Model
	execute := func(endpoint string, headers http.Header, request Request) (Result, error) {
		return Execute(ctx, request, Options{Endpoint: endpoint, Headers: headers, Timeout: timeout})
	}
	targetBasic, err := BuildBasic(targetProtocol, targetModel, "Reply with exactly: OK-MODEL-CHECK", false)
	if err != nil {
		return nil, err
	}
	comparisonBasic, err := BuildBasic(comparisonProtocol, comparisonModel, "Reply with exactly: OK-MODEL-CHECK", false)
	if err != nil {
		return nil, err
	}
	targetBasicResult, err := execute(targetEndpoint, targetHeaders, targetBasic)
	if err != nil {
		return nil, err
	}
	comparisonBasicResult, err := execute(comparisonEndpoint, comparisonHeaders, comparisonBasic)
	if err != nil {
		return nil, err
	}
	pairs := make([]DistributionPair, 0, len(distributionDefinitions))
	for _, definition := range distributionDefinitions {
		targetRequest, err := BuildBasic(targetProtocol, targetModel, definition.Prompt, false)
		if err != nil {
			return nil, err
		}
		comparisonRequest, err := BuildBasic(comparisonProtocol, comparisonModel, definition.Prompt, false)
		if err != nil {
			return nil, err
		}
		targetResult, err := execute(targetEndpoint, targetHeaders, targetRequest)
		if err != nil {
			return nil, err
		}
		comparisonResult, err := execute(comparisonEndpoint, comparisonHeaders, comparisonRequest)
		if err != nil {
			return nil, err
		}
		pairs = append(pairs, DistributionPair{Definition: definition, Target: targetResult, Comparison: comparisonResult})
	}
	return []Evaluation{
		EvaluateDistribution(pairs),
		EvaluateCrossModelPair(targetBasicResult, comparisonBasicResult, targetModel, comparisonModel),
	}, nil
}
