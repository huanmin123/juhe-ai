package modelcheckprobe

import (
	"context"
	"errors"
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
	// Prefix scopes durable item keys (for example target.* or
	// trusted_comparison.*). Empty preserves the package's legacy unscoped keys.
	Prefix       string
	Endpoint     string
	ProviderCode string
	// ProviderProtocolProfileID is the immutable Business profile identity.
	// Trusted comparisons must use the exact same provider profile as target.
	ProviderProtocolProfileID string
	Headers                   http.Header
	Client                    *http.Client
	Model                     string
	Profile                   string
	Protocol                  modelcheckprofile.Protocol
	UpstreamProtocol          modelcheckprofile.Protocol
	Stream                    bool
	// EndpointMode is preferred over Stream when supplied. Supported modes are
	// checked before any request is built, preserving Business fail-closed
	// endpoint capability semantics.
	EndpointMode           string
	UpstreamEndpointMode   string
	SupportedEndpointModes []string
	// SupportedModels restricts family-wide diagnostic requests to models the
	// resolved physical account explicitly permits. Empty means unrestricted.
	SupportedModels []string
	// Comparison is an independently resolved, in-process trusted target.
	// Quick and full profiles both support trusted comparison; callers must not
	// provide a client that proxies through another service.
	Comparison  *Suite
	Tokenizer   Tokenizer
	ModelLimits ModelLimitSnapshot
	Dispatcher  DispatcherPort
	Capability  keymodelruntime.Capability
	// Adapter is set only by the resolved source-account contract. The suite
	// never infers a subscription lane from a generic OpenAI protocol.
	Adapter string
	Retry   RetryOptions
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
	results := make([]Result, 0, 4)
	basic, err := input.buildBasic(input.Model, "Reply with exactly: OK-MODEL-CHECK", upstreamMode, stream)
	if err != nil {
		return nil, err
	}
	basicResult, executeErr := input.execute(ctx, basic, timeout)
	if executeErr != nil {
		return nil, executeErr
	}
	results = append(results, basicResult)
	items := make([]Evaluation, 0, len(results)+2)
	items = append(items, scopeEvaluation(input.Prefix, EvaluateBasic(basicResult, input.Model)))
	// A non-200 result after the retry boundary is terminal for the containing
	// core family. Do not spend the remaining core probes on an unavailable
	// upstream; the partial evaluations below retain exact failure evidence.
	if isTerminalProbeFailure(basicResult) {
		items = append(items, scopeEvaluation(input.Prefix, EvaluateUsage(results)))
		return items, nil
	}
	if stream {
		streamRequest, streamErr := input.buildBasic(input.Model, "Reply with exactly: STREAM-OK", upstreamMode, true)
		if streamErr != nil {
			return nil, streamErr
		}
		streamResult, streamExecuteErr := input.execute(ctx, streamRequest, timeout)
		if streamExecuteErr != nil {
			return nil, streamExecuteErr
		}
		results = append(results, streamResult)
		items = append(items, scopeEvaluation(input.Prefix, EvaluateProtocolStream(streamResult, input.Model, input.UpstreamProtocol)))
		if isTerminalProbeFailure(streamResult) {
			items = append(items, scopeEvaluation(input.Prefix, EvaluateUsage(results)))
			return items, nil
		}
	}
	structured, err := input.buildStructured(input.Model, upstreamMode, stream)
	if err != nil {
		return nil, err
	}
	structuredResult, structuredExecuteErr := input.execute(ctx, structured, timeout)
	if structuredExecuteErr != nil {
		return nil, structuredExecuteErr
	}
	results = append(results, structuredResult)
	items = append(items, scopeEvaluation(input.Prefix, EvaluateStructured(structuredResult, input.Model)))
	if isTerminalProbeFailure(structuredResult) {
		items = append(items, scopeEvaluation(input.Prefix, EvaluateUsage(results)))
		return items, nil
	}
	tool, err := input.buildTool(input.Model, upstreamMode, stream)
	if err != nil {
		return nil, err
	}
	toolResult, toolExecuteErr := input.execute(ctx, tool, timeout)
	if toolExecuteErr != nil {
		return nil, toolExecuteErr
	}
	results = append(results, toolResult)
	items = append(items, scopeEvaluation(input.Prefix, EvaluateTool(toolResult, input.Model)))
	if isTerminalProbeFailure(toolResult) {
		items = append(items, scopeEvaluation(input.Prefix, EvaluateUsage(results)))
		return items, nil
	}
	coreSuccess := false
	for _, result := range results {
		if result.Success {
			coreSuccess = true
			break
		}
	}
	if !coreSuccess {
		items = append(items, scopeEvaluation(input.Prefix, EvaluateUsage(results)))
		return items, nil
	}
	if input.Profile == "quick" {
		if input.supportsTokenIdentityProbes() {
			tokenIntegrity, tokenErr := runTokenIntegrity(ctx, input.UpstreamProtocol, input.Model, input.Tokenizer, func(runCtx context.Context, request Request) (Result, error) {
				return input.execute(runCtx, request, timeout)
			}, 1, upstreamMode)
			if tokenErr != nil {
				return nil, tokenErr
			}
			items = append(items, scopeEvaluation(input.Prefix, tokenIntegrity))
		} else {
			items = append(items, scopeEvaluation(input.Prefix, protocolScopedSkip("token_integrity")))
		}
		if results[0].Success {
			crossModel, crossErr := RunSelfCrossModel(ctx, input, results[0], timeout)
			if crossErr != nil {
				return nil, crossErr
			}
			items = append(items, scopeEvaluation(input.Prefix, crossModel))
		}
		if input.Comparison != nil {
			// Node forms the trusted aggregate from the complete quick target
			// suite, including usage-shape evidence. Keep that evidence in the
			// aggregate input without changing the public item order below.
			targetComparisonItems := append(append([]Evaluation(nil), items...), scopeEvaluation(input.Prefix, EvaluateUsage(results)))
			comparison, comparisonErr := runQuickTrustedComparison(ctx, input, targetComparisonItems, timeout)
			if comparisonErr != nil {
				return nil, comparisonErr
			}
			items = append(items, comparison...)
		}
		items = append(items, scopeEvaluation(input.Prefix, EvaluateUsage(results)))
		return items, nil
	}
	if input.Profile == "full" {
		behaviorRun, behaviorTerminal := input.familyRunner(timeout)
		behavior, behaviorErr := RunBehavior(ctx, input.UpstreamProtocol, input.Model, behaviorRun, upstreamMode)
		if behaviorErr != nil {
			return nil, behaviorErr
		}
		if behaviorTerminal() {
			behavior = terminalFamilyEvaluation(behavior)
		}
		items = append(items, scopeEvaluation(input.Prefix, behavior))
		if behaviorTerminal() {
			return append(items, scopeEvaluation(input.Prefix, EvaluateUsage(results))), nil
		}
		longRun, longTerminal := input.familyRunner(timeout)
		longContext, longErr := RunLongContext(ctx, input.ProviderCode, input.Model, input.UpstreamProtocol, input.Tokenizer, input.ModelLimits, longRun, upstreamMode)
		if longErr != nil {
			return nil, longErr
		}
		if longTerminal() {
			longContext = terminalFamilyEvaluation(longContext)
		}
		items = append(items, scopeEvaluation(input.Prefix, longContext))
		if longTerminal() {
			return append(items, scopeEvaluation(input.Prefix, EvaluateUsage(results))), nil
		}
		stabilityResults := make([]Result, 0, 3)
		for round := 0; round < 3; round++ {
			stability, stabilityErr := input.buildStability(input.Model, upstreamMode, stream)
			if stabilityErr != nil {
				return nil, stabilityErr
			}
			result, executeErr := input.execute(ctx, stability, timeout)
			if executeErr != nil {
				return nil, executeErr
			}
			stabilityResults = append(stabilityResults, result)
			if isTerminalProbeFailure(result) {
				break
			}
		}
		items = append(items, scopeEvaluation(input.Prefix, EvaluateStability(stabilityResults, input.Model)))
		if len(stabilityResults) > 0 && isTerminalProbeFailure(stabilityResults[len(stabilityResults)-1]) {
			return append(items, scopeEvaluation(input.Prefix, EvaluateUsage(results))), nil
		}
		if input.supportsTokenIdentityProbes() {
			tokenIntegrity, tokenErr := runTokenIntegrity(ctx, input.UpstreamProtocol, input.Model, input.Tokenizer, func(runCtx context.Context, request Request) (Result, error) {
				return input.execute(runCtx, request, timeout)
			}, 3, upstreamMode)
			if tokenErr != nil {
				return nil, tokenErr
			}
			items = append(items, scopeEvaluation(input.Prefix, tokenIntegrity))
		} else {
			items = append(items, scopeEvaluation(input.Prefix, protocolScopedSkip("token_integrity")))
		}
		if input.supportsTokenIdentityProbes() {
			identityRun, identityTerminal := input.familyRunner(timeout)
			identity, identityErr := RunIdentityForModels(ctx, input.UpstreamProtocol, input.Model, input.identityModels(), identityRun, upstreamMode)
			if identityErr != nil {
				return nil, identityErr
			}
			if identityTerminal() {
				identity = terminalFamilyEvaluation(identity)
			}
			items = append(items, scopeEvaluation(input.Prefix, identity))
			if identityTerminal() {
				return append(items, scopeEvaluation(input.Prefix, EvaluateUsage(results))), nil
			}
		} else {
			items = append(items, scopeEvaluation(input.Prefix, protocolScopedSkip("identity_observation")))
		}
		if ShouldRunJuice(input.Model, input.Profile, string(input.UpstreamProtocol)) {
			juiceResults := make([]Result, 0, 6)
			juiceRequests, coverage, juiceErr := JuiceRequestsForStream(input.Model, stream)
			if juiceErr != nil {
				return nil, juiceErr
			}
			for _, request := range juiceRequests {
				result, executeErr := input.execute(ctx, request, timeout)
				if executeErr != nil {
					return nil, executeErr
				}
				juiceResults = append(juiceResults, result)
				if isTerminalProbeFailure(result) {
					break
				}
			}
			items = append(items, scopeEvaluation(input.Prefix, EvaluateJuice(input.Model, juiceResults, coverage)))
		} else {
			items = append(items, scopeEvaluation(input.Prefix, Evaluation{Kind: "juice", Status: "skipped", Evidence: map[string]any{"evidenceInsufficient": true, "excludedFromScoring": true, "notApplicable": true, "reason": "juice_scope_not_applicable"}}))
		}
		if input.Comparison == nil {
			if results[0].Success {
				crossModel, crossErr := RunSelfCrossModel(ctx, input, results[0], timeout)
				if crossErr != nil {
					return nil, crossErr
				}
				items = append(items, scopeEvaluation(input.Prefix, crossModel))
			} else {
				items = append(items, scopeEvaluation(input.Prefix, Evaluation{Kind: "cross_model", Status: "skipped", Evidence: map[string]any{"evidenceInsufficient": true, "excludedFromScoring": true, "reason": "target_basic_probe_failed"}}))
			}
			items = append(items, scopeEvaluation(input.Prefix, Evaluation{Kind: "distribution", Status: "skipped", Evidence: map[string]any{"evidenceInsufficient": true, "excludedFromScoring": true, "reason": "trusted_comparison_not_attached"}}))
		} else {
			comparison, comparisonErr := RunTrustedComparison(ctx, input, *input.Comparison, timeout)
			if comparisonErr != nil {
				return nil, comparisonErr
			}
			for _, item := range comparison {
				items = append(items, scopeEvaluation(input.Prefix, item))
			}
		}
	}
	items = append(items, scopeEvaluation(input.Prefix, EvaluateUsage(results)))
	return items, nil
}

// runQuickTrustedComparison mirrors Node's quick trusted path: the trusted
// account forms its own quick core suite, then the two independently resolved
// suites are reduced to one bounded comparison item. Quick does not run the
// full distribution family, so the aggregate is deliberately based on the
// quick quality items only.
func runQuickTrustedComparison(ctx context.Context, target Suite, targetItems []Evaluation, timeout time.Duration) ([]Evaluation, error) {
	if target.Comparison == nil {
		return nil, errors.New("J3b quick trusted comparison is missing comparison suite")
	}
	comparison := *target.Comparison
	comparison.Profile = "quick"
	comparison.Comparison = nil
	comparison.Prefix = "trusted_comparison"
	comparison.Tokenizer = target.Tokenizer
	comparison.ModelLimits = target.ModelLimits
	comparisonItems, err := RunSuite(ctx, comparison, timeout)
	if err != nil {
		return nil, err
	}
	aggregate := buildQuickTrustedComparison(targetItems, comparisonItems)
	return append(comparisonItems, aggregate), nil
}

func buildQuickTrustedComparison(targetItems, comparisonItems []Evaluation) Evaluation {
	targetBasic := findSuiteEvaluation(targetItems, "protocol_basic")
	comparisonBasic := findSuiteEvaluation(comparisonItems, "protocol_basic")
	targetQualityScore, targetQualityMax := quickQualityScore(targetItems)
	comparisonQualityScore, comparisonQualityMax := quickQualityScore(comparisonItems)
	targetBasicMismatch := evaluationBool(targetBasic, "modelMismatch")
	comparisonBasicMismatch := evaluationBool(comparisonBasic, "modelMismatch")
	targetOK := evaluationSuccess(targetBasic) && !targetBasicMismatch && targetQualityMax > 0 && targetQualityScore == targetQualityMax
	comparisonOK := evaluationSuccess(comparisonBasic) && !comparisonBasicMismatch && comparisonQualityMax > 0 && comparisonQualityScore == comparisonQualityMax
	requestFailure := !evaluationSuccess(targetBasic) || !evaluationSuccess(comparisonBasic) || quickQualitySkipped(targetItems) || quickQualitySkipped(comparisonItems)
	evidence := map[string]any{
		"targetQualityScore": targetQualityScore, "targetQualityMax": targetQualityMax,
		"comparisonQualityScore": comparisonQualityScore, "comparisonQualityMax": comparisonQualityMax,
		"targetBasicModelMismatch": targetBasicMismatch, "comparisonBasicModelMismatch": comparisonBasicMismatch,
		"targetBasicSuccess": evaluationSuccess(targetBasic), "comparisonBasicSuccess": evaluationSuccess(comparisonBasic),
	}
	if requestFailure {
		evidence["message"] = "可信对比核心探针请求失败，未形成可比模型证据"
		evidence["requestFailure"] = true
		evidence["excludedFromScoring"] = true
		return Evaluation{Kind: "trusted_comparison.comparison", Status: "skipped", Evidence: evidence}
	}
	comparable := targetOK && comparisonOK
	status, score := "failed", 0
	if targetBasicMismatch || comparisonBasicMismatch {
		status = "failed"
	} else if comparable {
		status, score = "passed", 10
	} else if comparisonOK {
		status, score = "warning", 4
	}
	if comparisonBasicMismatch {
		evidence["message"] = "可信对比账户基础探针返回模型不匹配，不能作为可信对比基准"
	} else if targetBasicMismatch {
		evidence["message"] = "目标账户基础探针返回模型不匹配，可信对比未形成完整可比结果"
	} else if comparable {
		evidence["message"] = "目标链路和可信对比链路均完成核心探针"
	} else {
		evidence["message"] = "可信对比未形成完整可比结果"
	}
	return Evaluation{Kind: "trusted_comparison.comparison", Status: status, Score: score, MaxScore: 10, Evidence: evidence}
}

func findSuiteEvaluation(items []Evaluation, kind string) *Evaluation {
	for index := range items {
		if unscopedKind(items[index].Kind) == kind {
			return &items[index]
		}
	}
	return nil
}

func evaluationSuccess(item *Evaluation) bool {
	return item != nil && evidenceBool(item.Evidence, "success")
}

func evaluationBool(item *Evaluation, key string) bool {
	return item != nil && evidenceBool(item.Evidence, key)
}

func quickQualityScore(items []Evaluation) (score, maxScore int) {
	for _, item := range items {
		kind := unscopedKind(item.Kind)
		if item.MaxScore <= 0 || (item.Status == "skipped" && kind == "cross_model") {
			continue
		}
		score += item.Score
		maxScore += item.MaxScore
	}
	return score, maxScore
}

func quickQualitySkipped(items []Evaluation) bool {
	for _, item := range items {
		if item.MaxScore > 0 && item.Status == "skipped" && unscopedKind(item.Kind) != "cross_model" {
			return true
		}
	}
	return false
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
	pairedModel := input.pairedModel()
	if pairedModel == "" {
		return Evaluation{Kind: "cross_model", Status: "skipped", Evidence: map[string]any{"evidenceInsufficient": true, "excludedFromScoring": true, "reason": "no_paired_model"}}, nil
	}
	request, err := input.buildBasic(pairedModel, "Reply with exactly: CROSS-MODEL-OK", upstreamMode, stream)
	if err != nil {
		return Evaluation{}, err
	}
	paired, err := input.execute(ctx, request, timeout)
	if err != nil {
		return Evaluation{}, err
	}
	return EvaluateCrossModelPair(targetBasic, paired, input.Model, pairedModel), nil
}

func (s Suite) identityModels() []string {
	return allowedFamilyModels(s.Model, s.SupportedModels)
}

func (s Suite) supportsTokenIdentityProbes() bool {
	protocol := s.UpstreamProtocol
	if protocol == "" {
		protocol = s.Protocol
	}
	return protocol == modelcheckprofile.ProtocolOpenAIResponses
}

func protocolScopedSkip(kind string) Evaluation {
	return Evaluation{Kind: kind, Status: "skipped", Evidence: map[string]any{
		"evidenceInsufficient": true,
		"excludedFromScoring":  true,
		"notApplicable":        true,
		"reason":               "protocol_scope_not_applicable",
	}}
}

func (s Suite) execute(ctx context.Context, request Request, timeout time.Duration) (Result, error) {
	options := s.options(s.Endpoint, s.Headers, timeout)
	options.Client = s.Client
	return ExecuteWithRetry(ctx, request, options, s.Retry)
}

// familyRunner prevents a family helper that has no terminal-aware callback
// of its own from issuing requests after the retry boundary. The helper may
// still finish its in-memory aggregation with the terminal result, but the
// upstream is never contacted again.
func (s Suite) familyRunner(timeout time.Duration) (func(context.Context, Request) (Result, error), func() bool) {
	var terminal *Result
	run := func(ctx context.Context, request Request) (Result, error) {
		if terminal != nil {
			return *terminal, nil
		}
		result, err := s.execute(ctx, request, timeout)
		if err == nil && isTerminalProbeFailure(result) {
			copy := result
			terminal = &copy
		}
		return result, err
	}
	return run, func() bool { return terminal != nil }
}

func terminalFamilyEvaluation(item Evaluation) Evaluation {
	evidence := make(map[string]any, len(item.Evidence)+3)
	for key, value := range item.Evidence {
		evidence[key] = value
	}
	evidence["requestFailure"] = true
	evidence["evidenceInsufficient"] = true
	evidence["excludedFromScoring"] = true
	evidence["terminalFailure"] = true
	if item.Status == "passed" {
		// A partial family must never look complete merely because the probes
		// that preceded the terminal failure happened to pass.
		item.Status = "warning"
	}
	item.Evidence = evidence
	return item
}

func (s Suite) pairedModel() string {
	preferred := modelcheckprofile.PairedModel(s.ProfileForModel(), s.Model)
	if len(s.SupportedModels) == 0 {
		return preferred
	}
	for _, candidate := range allowedFamilyModels(s.Model, s.SupportedModels) {
		if candidate != s.Model {
			return candidate
		}
	}
	return ""
}

func allowedFamilyModels(model string, supported []string) []string {
	candidates := pairedIdentityModels(model)
	if len(supported) == 0 {
		return candidates
	}
	allowed := make(map[string]struct{}, len(supported))
	for _, candidate := range supported {
		if candidate = strings.TrimSpace(candidate); candidate != "" {
			allowed[candidate] = struct{}{}
		}
	}
	result := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if _, ok := allowed[candidate]; ok {
			result = append(result, candidate)
		}
	}
	if len(result) == 0 {
		return []string{model}
	}
	return result
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

func scopeEvaluation(prefix string, item Evaluation) Evaluation {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" || strings.Contains(item.Kind, ".") {
		return item
	}
	item.Kind = prefix + "." + item.Kind
	return item
}

// RunTrustedComparison directly executes the bounded distribution and model
// identity probes against the target and its independently resolved trusted
// comparison account. Only evaluated summaries are returned; provider output
// is not retained.
func RunTrustedComparison(ctx context.Context, target, comparison Suite, timeout time.Duration) ([]Evaluation, error) {
	if target.Endpoint == "" || target.Model == "" || target.Protocol == "" || comparison.Endpoint == "" || comparison.Model == "" || comparison.Protocol == "" {
		return nil, fmt.Errorf("J3b trusted comparison target is incomplete")
	}
	targetProvider := modelcheckprofile.NormalizeToken(target.ProviderCode)
	comparisonProvider := modelcheckprofile.NormalizeToken(comparison.ProviderCode)
	if targetProvider == "" || comparisonProvider == "" || targetProvider != comparisonProvider {
		return nil, fmt.Errorf("J3b trusted comparison provider is incompatible")
	}
	targetProtocol := modelcheckprofile.NormalizeToken(string(target.Protocol))
	comparisonProtocol := modelcheckprofile.NormalizeToken(string(comparison.Protocol))
	if targetProtocol == "" || comparisonProtocol == "" || targetProtocol != comparisonProtocol {
		return nil, fmt.Errorf("J3b trusted comparison protocol is incompatible")
	}
	targetProfile := modelcheckprofile.NormalizeToken(target.ProviderProtocolProfileID)
	comparisonProfile := modelcheckprofile.NormalizeToken(comparison.ProviderProtocolProfileID)
	if targetProfile == "" || comparisonProfile == "" || targetProfile != comparisonProfile {
		return nil, fmt.Errorf("J3b trusted comparison provider protocol profile is incompatible")
	}
	targetMode, targetStream, err := target.probeMode()
	if err != nil {
		return nil, err
	}
	comparisonMode, comparisonStream, err := comparison.probeMode()
	if err != nil {
		return nil, err
	}
	comparisonItems := []Evaluation(nil)
	if target.Profile == "full" {
		// The trusted account must form its own full evidence families before
		// cross-account summaries are evaluated. Clear Comparison on the copy so
		// a nested trusted account cannot recurse back into this function.
		comparisonSuite := comparison
		comparisonSuite.Profile = "full"
		comparisonSuite.Comparison = nil
		comparisonSuite.Prefix = "trusted_comparison"
		// Long-context and token-integrity evidence must be generated with the
		// same versioned snapshots as the target. The comparison resolver may
		// omit these fields; they are owner-wide dependencies, not account
		// credentials, so copy the target snapshots explicitly.
		comparisonSuite.Tokenizer = target.Tokenizer
		comparisonSuite.ModelLimits = target.ModelLimits
		var err error
		comparisonItems, err = RunSuite(ctx, comparisonSuite, timeout)
		if err != nil {
			return nil, err
		}
		formed, incomplete, negative := comparisonEvidenceState(comparisonItems)
		if !formed {
			if comparisonCoreModelUnavailable(comparisonItems) {
				return appendComparisonUnavailable(comparisonItems, comparison.Model), nil
			}
			return nil, fmt.Errorf("J3b trusted comparison full suite core evidence is incomplete")
		}
		if incomplete || negative {
			comparisonEvidence := Evaluation{Kind: "comparison_evidence", Status: "warning", Evidence: map[string]any{"evidenceInsufficient": incomplete, "negativeEvidence": negative, "excludedFromScoring": true}}
			comparisonItems = append(comparisonItems, comparisonEvidence)
		}
	}
	targetModel := target.Model
	comparisonModel := comparison.Model
	// Keep ownership explicit. Endpoint URLs are not identities: two resolved
	// accounts may intentionally share an endpoint while using different
	// credentials, proxy clients, or dispatch capabilities.
	execute := func(owner Suite, request Request) (Result, error) {
		return owner.execute(ctx, request, timeout)
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
	if IsModelUnavailable(comparisonBasicResult, comparisonModel) {
		return appendComparisonUnavailable(comparisonItems, comparisonModel), nil
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
		if IsModelUnavailable(comparisonResult, comparisonModel) {
			return appendComparisonUnavailable(comparisonItems, comparisonModel), nil
		}
		pairs = append(pairs, DistributionPair{Definition: definition, Target: targetResult, Comparison: comparisonResult})
	}
	distribution := EvaluateDistribution(pairs)
	crossModel := EvaluateCrossModelPair(targetBasicResult, comparisonBasicResult, targetModel, comparisonModel)
	if evidence := findEvaluation(unscopedEvaluations(comparisonItems), "comparison_evidence"); evidence != nil {
		crossModel.Evidence = mergeEvidence(crossModel.Evidence, evidence.Evidence)
		crossModel.Status = "warning"
	}
	distribution.Kind = "distribution_similarity"
	crossModel.Kind = "comparison"
	comparisonItems = append(comparisonItems, distribution, crossModel)
	if strings.TrimSpace(target.Prefix) == "" {
		return comparisonItems, nil
	}
	return comparisonItems, nil
}

func comparisonCoreModelUnavailable(items []Evaluation) bool {
	found := false
	for _, item := range items {
		switch unscopedKind(item.Kind) {
		case "protocol_basic", "structured_output", "tool_calling":
			found = true
			if !evidenceBool(item.Evidence, "modelUnavailable") {
				return false
			}
		}
	}
	return found
}

func appendComparisonUnavailable(items []Evaluation, model string) []Evaluation {
	evidence := map[string]any{"modelUnavailable": true, "reason": "comparison_model_unavailable", "model": model, "excludedFromScoring": true, "evidenceInsufficient": true}
	return append(items,
		Evaluation{Kind: "comparison_evidence", Status: "skipped", Evidence: mergeEvidence(evidence, map[string]any{"comparisonSkipped": true})},
		Evaluation{Kind: "distribution_similarity", Status: "skipped", Evidence: mergeEvidence(evidence, map[string]any{"requestFailure": true})},
		Evaluation{Kind: "comparison", Status: "skipped", Evidence: evidence},
	)
}

func comparisonEvidenceState(items []Evaluation) (formed, incomplete, negative bool) {
	required := map[string]bool{"protocol_basic": false, "structured_output": false, "tool_calling": false}
	for _, item := range items {
		kind := unscopedKind(item.Kind)
		if _, ok := required[kind]; ok {
			required[kind] = true
			if !evidenceBool(item.Evidence, "success") {
				// A failed core request cannot form a trustworthy comparison. This
				// is deliberately separate from a valid 200 response that fails a
				// semantic constraint; the latter remains durable negative evidence.
				return false, false, false
			}
			if item.Status == "failed" {
				negative = true
			}
			continue
		}
		if strings.HasPrefix(item.Kind, "trusted_comparison.") {
			// A trusted family that stopped at its retry boundary is incomplete
			// even when earlier requests gave it a warning/partial status. Do not
			// let that partial account form a comparable aggregate.
			if kind == "juice" && evidenceBool(item.Evidence, "notApplicable") {
				continue
			}
			if item.Status == "failed" {
				negative = true
			}
			if item.Status == "skipped" || evidenceBool(item.Evidence, "evidenceInsufficient") || evidenceBool(item.Evidence, "requestFailure") || evidenceBool(item.Evidence, "terminalFailure") {
				incomplete = true
			}
		}
	}
	for _, present := range required {
		if !present {
			return false, false, false
		}
	}
	return true, incomplete, negative
}

// comparisonEvidenceFormed is retained as a small fail-closed predicate for
// package tests and callers that only need the core admission decision.
func comparisonEvidenceFormed(items []Evaluation) bool {
	formed, _, _ := comparisonEvidenceState(items)
	return formed
}

func mergeEvidence(left, right map[string]any) map[string]any {
	merged := make(map[string]any, len(left)+len(right))
	for key, value := range left {
		merged[key] = value
	}
	for key, value := range right {
		merged[key] = value
	}
	return merged
}
