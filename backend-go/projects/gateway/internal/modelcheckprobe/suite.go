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
	Endpoint         string
	ProviderCode     string
	Headers          http.Header
	Client           *http.Client
	Model            string
	Profile          string
	Protocol         modelcheckprofile.Protocol
	UpstreamProtocol modelcheckprofile.Protocol
	Stream           bool
	// EndpointMode is preferred over Stream when supplied. Supported modes are
	// checked before any request is built, preserving Business fail-closed
	// endpoint capability semantics.
	EndpointMode           string
	UpstreamEndpointMode   string
	SupportedEndpointModes []string
	// Comparison is an independently resolved, in-process trusted target.
	// It is only used by the full profile; callers must not provide a client
	// that proxies through another service.
	Comparison  *Suite
	Tokenizer   Tokenizer
	ModelLimits ModelLimitSnapshot
	Dispatcher  DispatcherPort
	Capability  keymodelruntime.Capability
	// Adapter is set only by the resolved source-account contract. The suite
	// never infers a subscription lane from a generic OpenAI protocol.
	Adapter string
}

func RunSuite(ctx context.Context, input Suite, timeout time.Duration) ([]Evaluation, error) {
	_, stream, err := input.probeMode()
	if err != nil {
		return nil, err
	}
	if input.UpstreamProtocol == "" {
		input.UpstreamProtocol = input.Protocol
	}
	upstreamMode := strings.TrimSpace(input.UpstreamEndpointMode)
	if upstreamMode == "" {
		upstreamMode = modelcheckprofile.EndpointModeForProtocol(input.UpstreamProtocol, stream)
	}
	results := make([]Result, 0, 3)
	requests := []Request{}
	basic, err := input.buildBasic(input.Model, "Reply with exactly: OK-MODEL-CHECK", upstreamMode, stream)
	if err != nil {
		return nil, err
	}
	requests = append(requests, basic)
	structured, err := input.buildStructured(input.Model, upstreamMode, stream)
	if err != nil {
		return nil, err
	}
	requests = append(requests, structured)
	tool, err := input.buildTool(input.Model, upstreamMode, stream)
	if err != nil {
		return nil, err
	}
	requests = append(requests, tool)
	for _, request := range requests {
		result, executeErr := Execute(ctx, request, Options{Endpoint: input.Endpoint, Headers: input.Headers, Client: input.Client, Timeout: timeout, Dispatcher: input.Dispatcher, Capability: input.Capability, Adapter: input.Adapter})
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
	// A terminal core failure keeps the formed items and prevents unrelated
	// profile extensions from hiding the failed request.
	if len(results) < len(requests) || !results[len(results)-1].Success {
		items = append(items, EvaluateUsage(results))
		return items, nil
	}
	if input.Profile == "quick" {
		tokenIntegrity, tokenErr := runTokenIntegrity(ctx, input.UpstreamProtocol, input.Model, input.Tokenizer, func(runCtx context.Context, request Request) (Result, error) {
			return Execute(runCtx, request, input.options(input.Endpoint, input.Headers, timeout))
		}, 1, upstreamMode)
		if tokenErr != nil {
			return nil, tokenErr
		}
		items = append(items, tokenIntegrity, EvaluateUsage(results))
		return items, nil
	}
	if input.Profile == "full" {
		behaviorTerminal := false
		behavior, behaviorErr := RunBehavior(ctx, input.UpstreamProtocol, input.Model, func(runCtx context.Context, request Request) (Result, error) {
			options := input.options(input.Endpoint, input.Headers, timeout)
			options.Client = input.Client
			result, executeErr := Execute(runCtx, request, options)
			if executeErr == nil && !result.Success {
				behaviorTerminal = true
			}
			return result, executeErr
		}, upstreamMode)
		if behaviorErr != nil {
			return nil, behaviorErr
		}
		items = append(items, behavior)
		if behaviorTerminal {
			items = append(items, EvaluateUsage(results))
			return items, nil
		}
		longContextTerminal := false
		longContext, longErr := RunLongContext(ctx, input.ProviderCode, input.Model, input.UpstreamProtocol, input.Tokenizer, input.ModelLimits, func(runCtx context.Context, request Request) (Result, error) {
			result, executeErr := Execute(runCtx, request, input.options(input.Endpoint, input.Headers, timeout))
			if executeErr == nil && !result.Success {
				longContextTerminal = true
			}
			return result, executeErr
		}, upstreamMode)
		if longErr != nil {
			return nil, longErr
		}
		items = append(items, longContext)
		if longContextTerminal {
			items = append(items, EvaluateUsage(results))
			return items, nil
		}
		stabilityResults := make([]Result, 0, 3)
		for round := 0; round < 3; round++ {
			stability, stabilityErr := input.buildStability(input.Model, upstreamMode, stream)
			if stabilityErr != nil {
				return nil, stabilityErr
			}
			result, executeErr := Execute(ctx, stability, input.options(input.Endpoint, input.Headers, timeout))
			if executeErr != nil {
				return nil, executeErr
			}
			stabilityResults = append(stabilityResults, result)
			if !result.Success {
				break
			}
		}
		items = append(items, EvaluateStability(stabilityResults, input.Model))
		if len(stabilityResults) > 0 && !stabilityResults[len(stabilityResults)-1].Success {
			items = append(items, EvaluateUsage(results))
			return items, nil
		}
		tokenIntegrity, tokenErr := runTokenIntegrity(ctx, input.UpstreamProtocol, input.Model, input.Tokenizer, func(runCtx context.Context, request Request) (Result, error) {
			return Execute(runCtx, request, input.options(input.Endpoint, input.Headers, timeout))
		}, 3, upstreamMode)
		if tokenErr != nil {
			return nil, tokenErr
		}
		items = append(items, tokenIntegrity)
		identity, identityErr := RunIdentity(ctx, input.UpstreamProtocol, input.Model, func(runCtx context.Context, request Request) (Result, error) {
			return Execute(runCtx, request, input.options(input.Endpoint, input.Headers, timeout))
		}, upstreamMode)
		if identityErr != nil {
			return nil, identityErr
		}
		items = append(items, identity)
		if ShouldRunJuice(input.Model, input.Profile, string(input.UpstreamProtocol)) {
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
	_, stream, err := input.probeMode()
	if err != nil {
		return Evaluation{}, err
	}
	upstreamProtocol := input.UpstreamProtocol
	if upstreamProtocol == "" {
		upstreamProtocol = input.Protocol
	}
	upstreamMode := strings.TrimSpace(input.UpstreamEndpointMode)
	if upstreamMode == "" {
		upstreamMode = modelcheckprofile.EndpointModeForProtocol(upstreamProtocol, stream)
	}
	pairedModel := modelcheckprofile.PairedModel(input.ProfileForModel(), input.Model)
	if pairedModel == "" {
		return Evaluation{Kind: "cross_model", Status: "skipped", Evidence: map[string]any{"evidenceInsufficient": true, "excludedFromScoring": true, "reason": "no_paired_model"}}, nil
	}
	request, err := input.buildBasic(pairedModel, "Reply with exactly: CROSS-MODEL-OK", upstreamMode, stream)
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
	if s.Adapter == AdapterOpenAIOAuthCodex && (s.Protocol != modelcheckprofile.ProtocolOpenAIResponses || (mode != modelcheckprofile.EndpointModeResponsesJSON && mode != modelcheckprofile.EndpointModeResponsesSSE)) {
		return "", false, fmt.Errorf("J3b OpenAI OAuth Codex endpoint mode %q is unsupported", mode)
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
	protocol := s.UpstreamProtocol
	if protocol == "" {
		protocol = s.Protocol
	}
	if s.Adapter == AdapterOpenAIOAuthCodex {
		return BuildOpenAIOAuthCodexBasic(model, prompt, stream)
	}
	if mode != "" {
		return BuildBasicForEndpointMode(protocol, model, prompt, mode)
	}
	return BuildBasic(protocol, model, prompt, stream)
}

func (s Suite) buildStructured(model, mode string, stream bool) (Request, error) {
	protocol := s.UpstreamProtocol
	if protocol == "" {
		protocol = s.Protocol
	}
	if s.Adapter == AdapterOpenAIOAuthCodex {
		return BuildOpenAIOAuthCodexStructured(model, stream)
	}
	if mode != "" {
		return BuildStructuredForEndpointMode(protocol, model, mode)
	}
	return BuildStructured(protocol, model, stream)
}

func (s Suite) buildTool(model, mode string, stream bool) (Request, error) {
	protocol := s.UpstreamProtocol
	if protocol == "" {
		protocol = s.Protocol
	}
	if s.Adapter == AdapterOpenAIOAuthCodex {
		return BuildOpenAIOAuthCodexTool(model, stream)
	}
	if mode != "" {
		return BuildToolForEndpointMode(protocol, model, mode)
	}
	return BuildTool(protocol, model, stream)
}

func (s Suite) buildStability(model, mode string, stream bool) (Request, error) {
	protocol := s.UpstreamProtocol
	if protocol == "" {
		protocol = s.Protocol
	}
	if mode != "" {
		return BuildBasicForEndpointMode(protocol, model, "Reply with exactly one uppercase word: VECTOR", mode)
	}
	return StabilityRequest(protocol, model, stream)
}

func (s Suite) options(endpoint string, headers http.Header, timeout time.Duration) Options {
	return Options{Endpoint: endpoint, Headers: headers, Client: s.Client, Timeout: timeout, Dispatcher: s.Dispatcher, Capability: s.Capability, Adapter: s.Adapter}
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
	if target.Profile == "full" {
		// The trusted account must form its own full evidence families before
		// cross-account summaries are evaluated. Clear Comparison on the copy so
		// a nested trusted account cannot recurse back into this function.
		comparisonSuite := comparison
		comparisonSuite.Profile = "full"
		comparisonSuite.Comparison = nil
		if _, err := RunSuite(ctx, comparisonSuite, timeout); err != nil {
			return nil, err
		}
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
