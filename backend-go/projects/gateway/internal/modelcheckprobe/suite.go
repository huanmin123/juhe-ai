package modelcheckprobe

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	keymodelruntime "github.com/huanminabc/juhe-ai/backend-go-gateway/internal/business/key_model_runtime"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckprofile"
)

// Suite runs the ordered credential-free core probes. It stops after a
// transport failure so that partial evidence is explicit and never treated as
// a complete quality fact.
type Suite struct {
	Endpoint     string
	ProviderCode string
	Headers      http.Header
	Client       *http.Client
	Model        string
	Profile      string
	Protocol     modelcheckprofile.Protocol
	Stream       bool
	// EndpointMode is preferred over Stream when supplied. Supported modes are
	// checked before any request is built, preserving Business fail-closed
	// endpoint capability semantics.
	EndpointMode           string
	SupportedEndpointModes []string
	// Comparison is an independently resolved, in-process trusted target.
	// It is only used by the full profile; callers must not provide a client
	// that proxies through another service.
	Comparison  *Suite
	Tokenizer   Tokenizer
	ModelLimits ModelLimitSnapshot
	Dispatcher  DispatcherPort
	Capability  keymodelruntime.Capability
}

func RunSuite(ctx context.Context, input Suite, timeout time.Duration) ([]Evaluation, error) {
	endpointMode, stream, err := input.probeMode()
	if err != nil {
		return nil, err
	}
	results := make([]Result, 0, 4)
	requests := []Request{}
	basic, err := input.buildBasic(input.Model, "Reply with exactly: OK-MODEL-CHECK", endpointMode, stream)
	if err != nil {
		return nil, err
	}
	requests = append(requests, basic)
	structured, err := input.buildStructured(input.Model, endpointMode, stream)
	if err != nil {
		return nil, err
	}
	requests = append(requests, structured)
	tool, err := input.buildTool(input.Model, endpointMode, stream)
	if err != nil {
		return nil, err
	}
	requests = append(requests, tool)
	for round := 0; round < 3; round++ {
		stability, stabilityErr := input.buildStability(input.Model, endpointMode, stream)
		if stabilityErr != nil {
			return nil, stabilityErr
		}
		requests = append(requests, stability)
	}
	for _, request := range requests {
		result, executeErr := Execute(ctx, request, Options{Endpoint: input.Endpoint, Headers: input.Headers, Client: input.Client, Timeout: timeout, Dispatcher: input.Dispatcher, Capability: input.Capability})
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
			options := input.options(input.Endpoint, input.Headers, timeout)
			options.Client = input.Client
			return Execute(runCtx, request, options)
		}, endpointMode)
		if behaviorErr != nil {
			return nil, behaviorErr
		}
		items = append(items, behavior)
		identity, identityErr := RunIdentity(ctx, input.Protocol, input.Model, func(runCtx context.Context, request Request) (Result, error) {
			return Execute(runCtx, request, input.options(input.Endpoint, input.Headers, timeout))
		}, endpointMode)
		if identityErr != nil {
			return nil, identityErr
		}
		items = append(items, identity)
		tokenIntegrity, tokenErr := RunTokenIntegrity(ctx, input.Protocol, input.Model, input.Tokenizer, func(runCtx context.Context, request Request) (Result, error) {
			return Execute(runCtx, request, input.options(input.Endpoint, input.Headers, timeout))
		}, endpointMode)
		if tokenErr != nil {
			return nil, tokenErr
		}
		longContext, longErr := RunLongContext(ctx, input.ProviderCode, input.Model, input.Protocol, input.Tokenizer, input.ModelLimits, func(runCtx context.Context, request Request) (Result, error) {
			return Execute(runCtx, request, input.options(input.Endpoint, input.Headers, timeout))
		}, endpointMode)
		if longErr != nil {
			return nil, longErr
		}
		items = append(items, tokenIntegrity, longContext)
		if ShouldRunJuice(input.Model, input.Profile, string(input.Protocol)) {
			juiceResults := make([]Result, 0, 6)
			for _, request := range JuiceRequests(input.Model) {
				result, executeErr := Execute(ctx, request, input.options(input.Endpoint, input.Headers, timeout))
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
	endpointMode, stream, err := input.probeMode()
	if err != nil {
		return Evaluation{}, err
	}
	pairedModel := modelcheckprofile.PairedModel(input.ProfileForModel(), input.Model)
	if pairedModel == "" {
		return Evaluation{Kind: "cross_model", Status: "skipped", Evidence: map[string]any{"evidenceInsufficient": true, "excludedFromScoring": true, "reason": "no_paired_model"}}, nil
	}
	request, err := input.buildBasic(pairedModel, "Reply with exactly: CROSS-MODEL-OK", endpointMode, stream)
	if err != nil {
		return Evaluation{}, err
	}
	paired, err := Execute(ctx, request, input.options(input.Endpoint, input.Headers, timeout))
	if err != nil {
		return Evaluation{}, err
	}
	return EvaluateCrossModelPair(targetBasic, paired, input.Model, pairedModel), nil
}

func (s Suite) probeMode() (string, bool, error) {
	mode := strings.TrimSpace(s.EndpointMode)
	if mode == "" {
		mode = modelcheckprofile.EndpointModeForProtocol(s.Protocol, s.Stream)
		if mode == "" {
			return "", false, nil
		}
	}
	if !modelcheckprofile.EndpointModeMatchesProtocol(s.Protocol, mode) {
		return "", false, fmt.Errorf("J3b suite endpoint mode %q does not match protocol %q", mode, s.Protocol)
	}
	if len(s.SupportedEndpointModes) > 0 {
		found := false
		for _, candidate := range s.SupportedEndpointModes {
			if strings.TrimSpace(candidate) == mode {
				found = true
				break
			}
		}
		if !found {
			return "", false, fmt.Errorf("J3b suite endpoint mode %q is not enabled by account", mode)
		}
	}
	return mode, modelcheckprofile.EndpointModeIsStreaming(mode), nil
}

func (s Suite) buildBasic(model, prompt, mode string, stream bool) (Request, error) {
	if mode != "" {
		return BuildBasicForEndpointMode(s.Protocol, model, prompt, mode)
	}
	return BuildBasic(s.Protocol, model, prompt, stream)
}

func (s Suite) buildStructured(model, mode string, stream bool) (Request, error) {
	if mode != "" {
		return BuildStructuredForEndpointMode(s.Protocol, model, mode)
	}
	return BuildStructured(s.Protocol, model, stream)
}

func (s Suite) buildTool(model, mode string, stream bool) (Request, error) {
	if mode != "" {
		return BuildToolForEndpointMode(s.Protocol, model, mode)
	}
	return BuildTool(s.Protocol, model, stream)
}

func (s Suite) buildStability(model, mode string, stream bool) (Request, error) {
	if mode != "" {
		return BuildBasicForEndpointMode(s.Protocol, model, "Reply with exactly one uppercase word: VECTOR", mode)
	}
	return StabilityRequest(s.Protocol, model, stream)
}

func (s Suite) options(endpoint string, headers http.Header, timeout time.Duration) Options {
	return Options{Endpoint: endpoint, Headers: headers, Client: s.Client, Timeout: timeout, Dispatcher: s.Dispatcher, Capability: s.Capability}
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
	targetMode, targetStream, err := target.probeMode()
	if err != nil {
		return nil, err
	}
	comparisonMode, comparisonStream, err := comparison.probeMode()
	if err != nil {
		return nil, err
	}
	targetModel := target.Model
	comparisonModel := comparison.Model
	// Keep ownership explicit. Endpoint URLs are not identities: two resolved
	// accounts may intentionally share an endpoint while using different
	// credentials, proxy clients, or dispatch capabilities.
	execute := func(owner Suite, request Request) (Result, error) {
		return Execute(ctx, request, owner.options(owner.Endpoint, owner.Headers, timeout))
	}
	targetBasic, err := target.buildBasic(targetModel, "Reply with exactly: OK-MODEL-CHECK", targetMode, targetStream)
	if err != nil {
		return nil, err
	}
	comparisonBasic, err := comparison.buildBasic(comparisonModel, "Reply with exactly: OK-MODEL-CHECK", comparisonMode, comparisonStream)
	if err != nil {
		return nil, err
	}
	targetBasicResult, err := execute(target, targetBasic)
	if err != nil {
		return nil, err
	}
	comparisonBasicResult, err := execute(comparison, comparisonBasic)
	if err != nil {
		return nil, err
	}
	pairs := make([]DistributionPair, 0, len(distributionDefinitions))
	for _, definition := range distributionDefinitions {
		targetRequest, err := target.buildBasic(targetModel, definition.Prompt, targetMode, targetStream)
		if err != nil {
			return nil, err
		}
		comparisonRequest, err := comparison.buildBasic(comparisonModel, definition.Prompt, comparisonMode, comparisonStream)
		if err != nil {
			return nil, err
		}
		targetResult, err := execute(target, targetRequest)
		if err != nil {
			return nil, err
		}
		comparisonResult, err := execute(comparison, comparisonRequest)
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
