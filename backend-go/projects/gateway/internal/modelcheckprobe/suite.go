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
	// RequestModel is the public model selected before a configured upstream
	// mapping. It is intentionally distinct from Model, which is the exact
	// upstream model placed in a probe payload.
	RequestModel        string
	ModelMappingApplied bool
	Profile             string
	Protocol            modelcheckprofile.Protocol
	UpstreamProtocol    modelcheckprofile.Protocol
	Stream              bool
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
	// QuizRequested marks that the owner resolved a custom quiz configuration.
	// It stays true even when every configured id was filtered out, so the
	// family still emits its "quiz_questions_unavailable" skipped item.
	QuizRequested bool
	// QuizQuestions carries the approved bank questions resolved by the owner
	// layer. Only the top-level suite (empty Prefix) may run the quiz family;
	// trusted-comparison nested suites never see quiz requests.
	QuizQuestions []QuizQuestion
	// probeCapture collects this round's per-probe executed outcomes (behavior
	// probes and the identity anchor) for the sampling family's paired
	// McNemar test. It never leaves the in-memory run.
	probeCapture *probeCapture
	// anchorProbe carries this suite's executed identity anchor. The trusted
	// comparison suite reuses the target's anchor question (same parameters) so
	// the sampling family's paired McNemar test compares both accounts on the
	// same question instead of mixing in per-question difficulty noise.
	anchorProbe *IdentityAnchorProbe
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
	basic, err := input.tunedBasic(input.Model, "Reply with exactly: OK-MODEL-CHECK", upstreamMode, stream, 16, 0)
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
		streamRequest, streamErr := input.tunedBasic(input.Model, "Reply with exactly: STREAM-OK", upstreamMode, true, 16, 0)
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
	// §16 身份与篡改探针组：quick 与 full 都在五核心通过后执行（核心失败
	// 截断语义不变；空 Profile 不执行）。终局请求失败按家族约定截断整轮。
	var identityAnchor IdentityAnchorProbe
	if input.Profile == "quick" || input.Profile == "full" {
		if input.probeCapture == nil {
			input.probeCapture = newProbeCapture()
		}
		identityItems, anchor, identityTerminal, identityErr := RunIdentityFamily(ctx, input, timeout)
		if identityErr != nil {
			return nil, identityErr
		}
		for _, item := range identityItems {
			items = append(items, scopeEvaluation(input.Prefix, item))
		}
		if identityTerminal {
			return append(items, scopeEvaluation(input.Prefix, EvaluateUsage(results))), nil
		}
		identityAnchor = anchor
		// 记录本轮锚点题：可信对照套件复用它（同题同参数），供 §17 配对检验。
		input.anchorProbe = &anchor
	}
	if input.Profile == "quick" {
		if reason := input.tokenIdentitySkipReason(); reason == "" {
			tokenIntegrity, tokenErr := runTokenIntegrity(ctx, input.UpstreamProtocol, input.Model, input.Tokenizer, func(runCtx context.Context, request Request) (Result, error) {
				return input.execute(runCtx, request, timeout)
			}, 1, upstreamMode)
			if tokenErr != nil {
				return nil, tokenErr
			}
			items = append(items, scopeEvaluation(input.Prefix, tokenIntegrity))
		} else {
			items = append(items, scopeEvaluation(input.Prefix, catalogScopeSkip("token_integrity", reason)))
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
		if input.Prefix == "" && input.QuizRequested {
			quizItems, quizErr := RunQuizFamily(ctx, input, timeout)
			if quizErr != nil {
				return nil, quizErr
			}
			items = append(items, quizItems...)
		}
		return items, nil
	}
	if input.Profile == "full" {
		behaviorRun, behaviorTerminal := input.familyRunner(timeout)
		if capture := input.probeCapture; capture != nil {
			// §17 paired_divergence 需要本轮行为题的逐题对错格局；包装家族
			// runner 记录每次行为题请求的 executed 结果（不改判分）。
			innerRun := behaviorRun
			behaviorRun = func(runCtx context.Context, request Request) (Result, error) {
				result, runErr := innerRun(runCtx, request)
				if runErr == nil {
					capture.recordBehaviorSample(input.Prefix, request, result)
				}
				return result, runErr
			}
		}
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
			stability, stabilityErr := input.tunedBasic(input.Model, "Reply with exactly one uppercase word: VECTOR", upstreamMode, stream, 16, 0)
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
		stabilityEvaluation := EvaluateStability(stabilityResults, input.Model)
		stabilityTerminal := len(stabilityResults) > 0 && isTerminalProbeFailure(stabilityResults[len(stabilityResults)-1])
		if stabilityTerminal {
			stabilityEvaluation = terminalFamilyEvaluation(stabilityEvaluation)
		}
		items = append(items, scopeEvaluation(input.Prefix, stabilityEvaluation))
		if stabilityTerminal {
			return append(items, scopeEvaluation(input.Prefix, EvaluateUsage(results))), nil
		}
		if reason := input.tokenIdentitySkipReason(); reason == "" {
			tokenIntegrity, tokenErr := runTokenIntegrity(ctx, input.UpstreamProtocol, input.Model, input.Tokenizer, func(runCtx context.Context, request Request) (Result, error) {
				return input.execute(runCtx, request, timeout)
			}, 3, upstreamMode)
			if tokenErr != nil {
				return nil, tokenErr
			}
			items = append(items, scopeEvaluation(input.Prefix, tokenIntegrity))
		} else {
			items = append(items, scopeEvaluation(input.Prefix, catalogScopeSkip("token_integrity", reason)))
		}
		if input.Comparison == nil {
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
		// §17 采样统计族：full 在既有家族之后追加。嵌套可信对照套件自身
		// 不执行本族（预算契约：有对照翻倍而非四倍），由下方构造性 scope
		// 跳过；对照模型不可用时不再消耗采样请求，直接落跳过证据。
		if strings.TrimSpace(input.Prefix) == "trusted_comparison" {
			items = append(items, scopeEvaluation(input.Prefix, catalogScopeSkip("sampling_statistics", "trusted_comparison_not_attached")))
		} else if comparisonSamplingBlocked(items) {
			items = append(items, scopeEvaluation(input.Prefix, SamplingComparisonUnavailableSkip(input.Comparison.Model)))
		} else {
			sampling, samplingTerminal, samplingErr := RunSamplingStatistics(ctx, input, timeout, identityAnchor)
			if samplingErr != nil {
				return nil, samplingErr
			}
			items = append(items, scopeEvaluation(input.Prefix, sampling))
			if samplingTerminal {
				return append(items, scopeEvaluation(input.Prefix, EvaluateUsage(results))), nil
			}
		}
	}
	items = append(items, scopeEvaluation(input.Prefix, EvaluateUsage(results)))
	if input.Prefix == "" && input.QuizRequested {
		quizItems, quizErr := RunQuizFamily(ctx, input, timeout)
		if quizErr != nil {
			return nil, quizErr
		}
		items = append(items, quizItems...)
	}
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
		if item.MaxScore <= 0 || item.Status == "skipped" {
			continue
		}
		score += item.Score
		maxScore += item.MaxScore
	}
	return score, maxScore
}

func quickQualitySkipped(items []Evaluation) bool {
	for _, item := range items {
		if item.MaxScore > 0 && item.Status == "skipped" {
			return true
		}
	}
	return false
}

// tokenIdentitySkipReason decides whether the token-integrity family may run.
// Its differential baseline is the versioned o200k GPT tokenizer snapshot, so
// it is only meaningful for catalog models: a non-Responses protocol and a
// catalog-external (account-supported) model both skip the family with
// distinct reasons, and the skip evidence stays excludedFromScoring so the
// brand tokenizer baseline can never enter a foreign model's score.
func (s Suite) tokenIdentitySkipReason() string {
	protocol := s.UpstreamProtocol
	if protocol == "" {
		protocol = s.Protocol
	}
	if protocol != modelcheckprofile.ProtocolOpenAIResponses {
		return "protocol_scope_not_applicable"
	}
	if _, ok := modelcheckprofile.FindForModel(s.ProviderCode, s.ProviderProtocolProfileID, s.Model); !ok {
		return "model_catalog_scope_not_applicable"
	}
	return ""
}

func (s Suite) supportsTokenIdentityProbes() bool {
	return s.tokenIdentitySkipReason() == ""
}

func protocolScopedSkip(kind string) Evaluation {
	return catalogScopeSkip(kind, "protocol_scope_not_applicable")
}

// catalogScopeSkip records a family that cannot produce comparable evidence
// for this target. The item stays out of the score denominator, matching the
// long_context skip precedent.
func catalogScopeSkip(kind, reason string) Evaluation {
	return Evaluation{Kind: kind, Status: "skipped", Evidence: map[string]any{
		"evidenceInsufficient": true,
		"excludedFromScoring":  true,
		"notApplicable":        true,
		"reason":               reason,
	}}
}

func (s Suite) execute(ctx context.Context, request Request, timeout time.Duration) (Result, error) {
	// Only the suite's primary mapped model may accept the public request model
	// as a valid upstream echo. Auxiliary cross-model probes use their paired
	// model directly and must retain strict matching for that paired request.
	if request.ExpectedModel == s.Model && strings.TrimSpace(s.RequestModel) != "" {
		request.RequestModel = s.RequestModel
		request.ModelMappingApplied = s.ModelMappingApplied
	}
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

// tunedBasic builds the basic request shape with the Node per-probe output
// budget and optional sampling temperature.
func (s Suite) tunedBasic(model, prompt, mode string, stream bool, maxOutputTokens int, temperature float64) (Request, error) {
	protocol := s.UpstreamProtocol
	if protocol == "" {
		protocol = s.Protocol
	}
	if s.Adapter == AdapterOpenAIOAuthCodex {
		// Codex normalization strips tuning fields anyway; keep the bounded
		// basic shape so the adapter contract stays the single source.
		return BuildOpenAIOAuthCodexBasic(model, prompt, stream)
	}
	return buildBasicWithTunings(protocol, model, prompt, mode, stream, maxOutputTokens, temperature)
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
		// The sampling family's paired McNemar test needs the comparison
		// account's own per-probe executed outcomes, so the capture buffer is
		// shared with the target suite for the duration of this run.
		comparisonSuite.probeCapture = target.probeCapture
		// The comparison suite reuses the target's anchor question (same
		// parameters): the paired anchor must be the same question on both
		// sides, or per-question difficulty noise enters the McNemar
		// discordant pairs.
		comparisonSuite.anchorProbe = target.anchorProbe
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
	targetBasic, err := target.tunedBasic(targetModel, "Reply with exactly: OK-MODEL-CHECK", targetMode, targetStream, 16, 0)
	if err != nil {
		return nil, err
	}
	// The paired request is the independent cross-model contract. The
	// comparison suite above already ran its own ordinary basic probe; this
	// request must use the CROSS-MODEL-OK output contract so the comparison item
	// does not silently score as a generic basic response.
	comparisonBasic, err := comparison.tunedBasic(comparisonModel, "Reply with exactly: CROSS-MODEL-OK", comparisonMode, comparisonStream, 16, 0)
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
		// Node distribution probes pin temperature 0.2 and a 96-token budget so
		// both accounts sample the same mildly-greedy distribution.
		targetRequest, err := target.tunedBasic(targetModel, definition.Prompt, targetMode, targetStream, 96, 0.2)
		if err != nil {
			return nil, err
		}
		comparisonRequest, err := comparison.tunedBasic(comparisonModel, definition.Prompt, comparisonMode, comparisonStream, 96, 0.2)
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

// comparisonSamplingBlocked reports whether the trusted comparison account was
// rejected as model-unavailable by the earlier comparison run; the sampling
// family must not spend interleaved requests on an unavailable comparison.
func comparisonSamplingBlocked(items []Evaluation) bool {
	item := findEvaluation(unscopedEvaluations(items), "comparison")
	return item != nil && evidenceBool(item.Evidence, "modelUnavailable")
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
			//
			// Exception: the nested comparison suite structurally cannot attach
			// its own comparison, so its distribution and sampling families are
			// scope-neutral skips (trusted_comparison_not_attached). Treating
			// those as incomplete would permanently cap the comparison aggregate
			// at warning and make high_confidence unreachable for every trusted
			// full run after the self cross-model retirement.
			if kind == "distribution" || kind == "sampling_statistics" {
				reason, _ := item.Evidence["reason"].(string)
				if reason == "trusted_comparison_not_attached" && evidenceBool(item.Evidence, "excludedFromScoring") {
					continue
				}
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
