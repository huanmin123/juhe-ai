package modelcheckprobe

import (
	"context"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckprofile"
)

type SuiteOptions struct {
	IncludeStream              bool
	IncludeStructured          bool
	IncludeTool                bool
	IncludeBehavior            bool
	IncludeLongContext         bool
	IncludeStability           bool
	IncludeUsageOnBasicFailure bool
}

// RunSuite executes the ordered basic/stream/structured/tool probes and then
// evaluates usage shape from those same responses. A terminal non-200 stops
// the same probe chain as the Node executor; the failure remains evidence.
func RunSuite(ctx context.Context, input BasicProbeInput, options SuiteOptions, retry RetryOptions) ([]EvaluationItem, error) {
	items := make([]EvaluationItem, 0, 5)
	results := make([]ProbeResult, 0, 4)
	base := TransportOptions{Endpoint: input.Endpoint, Headers: input.Headers, Timeout: input.Timeout, MaxResponseBytes: input.MaxResponseBytes}
	request, err := BuildBasic(input.Protocol, input.Model, "Reply with exactly: OK-MODEL-CHECK", BasicOptions{MaxOutputTokens: max(input.MaxOutputTokens, 16), Stream: input.Stream})
	if err != nil {
		return nil, err
	}
	result, err := ExecuteWithRetry(ctx, request, base, retry)
	if err != nil {
		return nil, err
	}
	results = append(results, result)
	items = append(items, evaluateSuiteBasic(result, input.Protocol, input.Model, "target", false))
	if isTerminalProbeResult(result) {
		if options.IncludeUsageOnBasicFailure {
			items = append(items, EvaluateUsageShape(results, "target"))
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
		items = append(items, evaluateSuiteBasic(result, input.Protocol, input.Model, "target", true))
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
		items = append(items, EvaluateStructuredOutput(result, input.Model, "target"))
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
		items = append(items, EvaluateToolCalling(result, input.Model, "target"))
		if isTerminalProbeResult(result) {
			return items, nil
		}
	}
	if options.IncludeStructured || options.IncludeTool {
		items = append(items, EvaluateUsageShape(results, "target"))
	}
	if options.IncludeBehavior {
		behavior, terminal, behaviorErr := RunBehaviorProbeSetWithTerminal(ctx, BehaviorProbeInput{Model: input.Model, Protocol: input.Protocol, Stream: input.Stream, RunProbe: func(ctx context.Context, request Request) (ProbeResult, error) {
			return ExecuteWithRetry(ctx, request, base, retry)
		}})
		if behaviorErr != nil {
			return nil, behaviorErr
		}
		items = append(items, behavior)
		if terminal {
			return items, nil
		}
	}
	if options.IncludeLongContext {
		if input.CountTokens == nil || input.ModelLimit <= 0 {
			items = append(items, EvaluationItem{ItemKey: "target.long_context", ItemType: "long_context", Status: "skipped", Evidence: map[string]any{
				"message":             "长上下文 tokenizer 或模型窗口快照缺失，未形成可验证长上下文证据",
				"excludedFromScoring": true, "evidenceInsufficient": true,
			}})
		} else {
			longContext, terminal, longContextErr := RunLongContextProbeSetWithTerminal(ctx, LongContextInput{Model: input.Model, Protocol: input.Protocol, Stream: input.Stream, ModelLimit: input.ModelLimit, CountTokens: input.CountTokens, RunProbe: func(ctx context.Context, request Request) (ProbeResult, error) {
				return ExecuteWithRetry(ctx, request, base, retry)
			}})
			if longContextErr != nil {
				return nil, longContextErr
			}
			items = append(items, longContext)
			if terminal {
				return items, nil
			}
		}
	}
	if options.IncludeStability {
		stability, stabilityErr := RunStabilityProbeSet(ctx, StabilityProbeInput{Model: input.Model, Protocol: input.Protocol, Stream: input.Stream, RunProbe: func(ctx context.Context, request Request) (ProbeResult, error) {
			return ExecuteWithRetry(ctx, request, base, retry)
		}})
		if stabilityErr != nil {
			return nil, stabilityErr
		}
		items = append(items, stability)
	}
	return items, nil
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
