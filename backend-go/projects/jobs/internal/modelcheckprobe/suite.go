package modelcheckprobe

import (
	"context"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckprofile"
)

type SuiteOptions struct {
	Prefix                     string
	IncludeStream              bool
	IncludeStructured          bool
	IncludeTool                bool
	IncludeBehavior            bool
	IncludeLongContext         bool
	IncludeStability           bool
	IncludeUsageOnBasicFailure bool
	// OnItem observes every completed evaluation item in suite order. It is
	// invocation-scoped so management SSE and scheduled runs never share a
	// mutable callback.
	OnItem func(EvaluationItem)
}

// RunSuite executes the ordered basic/stream/structured/tool probes and then
// evaluates usage shape from those same responses. A terminal non-200 stops
// the same probe chain as the Node executor; the failure remains evidence.
func RunSuite(ctx context.Context, input BasicProbeInput, options SuiteOptions, retry RetryOptions) ([]EvaluationItem, error) {
	items := make([]EvaluationItem, 0, 5)
	prefix := suitePrefix(options.Prefix)
	appendItem := func(item EvaluationItem) {
		items = append(items, item)
		emitSuiteItem(options.OnItem, item)
	}
	results := make([]ProbeResult, 0, 4)
	base := TransportOptions{Endpoint: input.Endpoint, Headers: input.Headers, Client: input.Client, Timeout: input.Timeout, MaxResponseBytes: input.MaxResponseBytes}
	request, err := BuildBasic(input.Protocol, input.Model, "Reply with exactly: OK-MODEL-CHECK", BasicOptions{MaxOutputTokens: max(input.MaxOutputTokens, 16), Stream: input.Stream})
	if err != nil {
		return nil, err
	}
	result, err := ExecuteWithRetry(ctx, request, base, retry)
	if err != nil {
		return nil, err
	}
	results = append(results, result)
	appendItem(evaluateSuiteBasic(result, input.Protocol, input.Model, prefix, false))
	if isTerminalProbeResult(result) {
		if options.IncludeUsageOnBasicFailure {
			appendItem(EvaluateUsageShape(results, prefix))
		}
		return items, nil
	}
	if options.IncludeStream {
		request, err = BuildBasic(input.Protocol, input.Model, "Reply with exactly: STREAM-OK", BasicOptions{MaxOutputTokens: max(input.MaxOutputTokens, 16), Stream: true})
		if err != nil {
			return nil, err
		}
		result, err = ExecuteWithRetry(ctx, request, base, retry)
		if err != nil {
			return nil, err
		}
		results = append(results, result)
		appendItem(evaluateSuiteBasic(result, input.Protocol, input.Model, prefix, true))
		if isTerminalProbeResult(result) {
			return items, nil
		}
	}
	if options.IncludeStructured {
		request, err = BuildStructured(input.Protocol, input.Model, input.Stream)
		if err != nil {
			return nil, err
		}
		result, err = ExecuteWithRetry(ctx, request, base, retry)
		if err != nil {
			return nil, err
		}
		results = append(results, result)
		appendItem(EvaluateStructuredOutput(result, input.Model, prefix))
		if isTerminalProbeResult(result) {
			return items, nil
		}
	}
	if options.IncludeTool {
		request, err = BuildTool(input.Protocol, input.Model, input.Stream)
		if err != nil {
			return nil, err
		}
		result, err = ExecuteWithRetry(ctx, request, base, retry)
		if err != nil {
			return nil, err
		}
		results = append(results, result)
		appendItem(EvaluateToolCalling(result, input.Model, prefix))
		if isTerminalProbeResult(result) {
			return items, nil
		}
	}
	if options.IncludeStructured || options.IncludeTool {
		appendItem(EvaluateUsageShape(results, prefix))
	}
	if options.IncludeBehavior {
		behavior, terminal, behaviorErr := RunBehaviorProbeSetWithTerminal(ctx, BehaviorProbeInput{Model: input.Model, Protocol: input.Protocol, Prefix: prefix, Stream: input.Stream, RunProbe: func(ctx context.Context, request Request) (ProbeResult, error) {
			return ExecuteWithRetry(ctx, request, base, retry)
		}})
		if behaviorErr != nil {
			return nil, behaviorErr
		}
		appendItem(behavior)
		if terminal {
			return items, nil
		}
	}
	if options.IncludeLongContext {
		if input.CountTokens == nil || input.ModelLimit <= 0 {
			appendItem(EvaluationItem{ItemKey: prefix + ".long_context", ItemType: "long_context", Status: "skipped", Evidence: map[string]any{
				"message":             "长上下文 tokenizer 或模型窗口快照缺失，未形成可验证长上下文证据",
				"excludedFromScoring": true, "evidenceInsufficient": true,
			}})
		} else {
			longContext, terminal, longContextErr := RunLongContextProbeSetWithTerminal(ctx, LongContextInput{Model: input.Model, Protocol: input.Protocol, Prefix: prefix, Stream: input.Stream, ModelLimit: input.ModelLimit, CountTokens: input.CountTokens, RunProbe: func(ctx context.Context, request Request) (ProbeResult, error) {
				return ExecuteWithRetry(ctx, request, base, retry)
			}})
			if longContextErr != nil {
				return nil, longContextErr
			}
			appendItem(longContext)
			if terminal {
				return items, nil
			}
		}
	}
	if options.IncludeStability {
		stability, stabilityErr := RunStabilityProbeSet(ctx, StabilityProbeInput{Model: input.Model, Protocol: input.Protocol, Prefix: prefix, Stream: input.Stream, RunProbe: func(ctx context.Context, request Request) (ProbeResult, error) {
			return ExecuteWithRetry(ctx, request, base, retry)
		}})
		if stabilityErr != nil {
			return nil, stabilityErr
		}
		appendItem(stability)
	}
	return items, nil
}

// RunTargetEvidenceExtensions executes the evidence probes that Node runs
// once for the primary target after its full core suite. Trusted-comparison
// suites must never invoke this function: their role is comparison evidence,
// not a second target identity/token assessment.
func RunTargetEvidenceExtensions(ctx context.Context, input BasicProbeInput, prefix string, retry RetryOptions, onItem func(EvaluationItem)) ([]EvaluationItem, bool, error) {
	if input.Protocol != modelcheckprofile.ProtocolOpenAIResponses {
		return nil, false, nil
	}
	prefix = suitePrefix(prefix)
	base := TransportOptions{Endpoint: input.Endpoint, Headers: input.Headers, Client: input.Client, Timeout: input.Timeout, MaxResponseBytes: input.MaxResponseBytes}
	items := make([]EvaluationItem, 0, 2)
	appendItem := func(item EvaluationItem) {
		items = append(items, item)
		emitSuiteItem(onItem, item)
	}
	if input.CountTokens == nil {
		appendItem(EvaluationItem{ItemKey: prefix + ".token_integrity", ItemType: "token_integrity", Status: "skipped", Evidence: map[string]any{
			"message": "tokenizer 快照缺失，未形成可验证 Token 诚信证据", "excludedFromScoring": true, "evidenceInsufficient": true,
		}})
	} else {
		tokenRun, err := RunTokenIntegrity(ctx, TokenProbeInput{Model: input.Model, Protocol: input.Protocol, ProfileMode: "full", ItemPrefix: prefix, Stream: input.Stream, CountTokens: input.CountTokens, RunProbe: func(ctx context.Context, request Request) (ProbeResult, error) {
			return ExecuteWithRetry(ctx, request, base, retry)
		}})
		if err != nil {
			return nil, false, err
		}
		appendItem(tokenRun.Item)
	}
	identity, _, err := RunIdentityObservation(ctx, IdentityProbeInput{Model: input.Model, Protocol: input.Protocol, Prefix: prefix, RunProbe: func(ctx context.Context, request Request) (ProbeResult, error) {
		return ExecuteWithRetry(ctx, request, base, retry)
	}})
	if err != nil {
		return nil, false, err
	}
	appendItem(identity)
	return items, isTerminalProbeItem(identity), nil
}

func emitSuiteItem(callback func(EvaluationItem), item EvaluationItem) {
	if callback == nil {
		return
	}
	defer func() { _ = recover() }()
	callback(item)
}

func isTerminalProbeItem(item EvaluationItem) bool {
	if item.Evidence == nil {
		return false
	}
	requestFailure, _ := item.Evidence["requestFailure"].(bool)
	return requestFailure
}

func suitePrefix(value string) string {
	if value == "" {
		return "target"
	}
	return value
}

func evaluateSuiteBasic(result ProbeResult, protocol modelcheckprofile.Protocol, model, prefix string, stream bool) EvaluationItem {
	if protocol == modelcheckprofile.ProtocolOpenAIResponses {
		if stream {
			return EvaluateProtocolStream(result, model, ProtocolEvaluationOptions{ItemKey: prefix + ".responses_stream", ItemType: "responses_stream", SuccessMessage: "Responses 流式调用可用", FailurePrefix: "Responses 流式调用失败"})
		}
		return EvaluateBasicProtocol(result, model, ProtocolEvaluationOptions{ItemKey: prefix + ".responses_basic", ItemType: "responses_basic", SuccessMessage: "Responses 非流式调用可用", FailurePrefix: "Responses 非流式调用失败"})
	}
	itemType := "protocol_basic"
	itemKey := prefix + ".protocol_basic"
	if stream {
		itemType = "protocol_stream"
		itemKey = prefix + ".protocol_stream"
	}
	return func() EvaluationItem {
		if stream {
			return EvaluateProtocolStream(result, model, ProtocolEvaluationOptions{ItemKey: itemKey, ItemType: itemType, SuccessMessage: "协议流式调用可用", FailurePrefix: "协议流式调用失败"})
		}
		return EvaluateBasicProtocol(result, model, ProtocolEvaluationOptions{ItemKey: itemKey, ItemType: itemType, SuccessMessage: "协议基础调用可用", FailurePrefix: "协议基础调用失败"})
	}()
}
