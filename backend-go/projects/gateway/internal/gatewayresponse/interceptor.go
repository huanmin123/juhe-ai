package gatewayresponse

import (
	"bytes"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayopenai"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
)

// OpenAI 流式检查拦截器：SSE 事件分帧 + 语义帧提取（复用 G02 的
// ParseSseEventText / ExtractSseSemanticFrames）+ 本包策略匹配，命中即在写入
// 下游前拦截。
//
// 说明：本拦截器自包含事件分帧逻辑，逐事件透传原始字节，保持字节序一致；
// gatewayopenai.ResponseInspectionBuffer 是同源的 G02 边界实现（已挂载
// 策略），其可见输出快路径见该包 buffer.go。
type OpenAIStreamInterceptor struct {
	policies           []RuntimeResponseInspectionPolicy
	context            *ResponseInspectionRuntimeContext
	clientRetry        bool
	endpointFamily     gatewayproto.ResponseEndpointFamily
	compactionExpected bool

	pending           bytes.Buffer
	downstreamWritten bool
	observations      []ResponseInspectionDecision
	seenData          bool
}

// OpenAIStreamInterceptorOptions 是 NewOpenAIStreamInterceptor 的入参。
type OpenAIStreamInterceptorOptions struct {
	ClientRetryEnabled bool
	Policies           []RuntimeResponseInspectionPolicy
	EndpointFamily     gatewayproto.ResponseEndpointFamily
	Context            *ResponseInspectionRuntimeContext
	// CompactionExpected 启用 Codex compaction 契约帧检查（G18 桥的契约面）。
	CompactionExpected bool
}

// NewOpenAIStreamInterceptor 构造拦截器。
func NewOpenAIStreamInterceptor(options OpenAIStreamInterceptorOptions) *OpenAIStreamInterceptor {
	return &OpenAIStreamInterceptor{
		policies:           options.Policies,
		context:            options.Context,
		clientRetry:        options.ClientRetryEnabled,
		endpointFamily:     options.EndpointFamily,
		compactionExpected: options.CompactionExpected,
	}
}

// PushChunk 对齐 pushChunk：逐事件提取语义帧并运行策略；未命中则原样透传。
func (i *OpenAIStreamInterceptor) PushChunk(chunk []byte) StreamInterceptorSseResult {
	i.pending.Write(chunk)
	var result StreamInterceptorSseResult
	for {
		rawEvent := i.shiftEvent()
		if rawEvent == nil {
			break
		}
		event := gatewayopenai.ParseSseEventText(string(rawEvent))
		frames := gatewayopenai.ExtractSseSemanticFrames(event, openAIEndpointFamilyOrUnknown(i.endpointFamily))
		if i.compactionExpected && i.isCodexCompactionContext() {
			if counts := CountCodexCompactionOutputItemsFromStreamEvent(event); counts != nil {
				if frame := CodexCompactionContractMismatchFrame(CodexCompactionContractMismatchInput{
					OutputItemCount:     counts.OutputItemCount,
					CompactionItemCount: counts.CompactionItemCount,
					Transport:           "sse",
					EventType:           event.EventName,
				}); frame != nil {
					frames = append(append([]gatewayproto.SemanticFrame(nil), frames...), *frame)
				}
			}
		}
		// Codex cyber_policy 失败终态保持不透明：原样透传并标记。
		passthrough := false
		for _, frame := range frames {
			if frame.FrameType == gatewayproto.FrameTypeError && frame.ErrorCode == "cyber_policy" &&
				i.context != nil && i.context.ClientProfile == "codex" &&
				frame.EndpointFamily == gatewayproto.EndpointFamilyResponses {
				passthrough = true
				break
			}
		}
		if passthrough {
			result.Chunks = append(result.Chunks, rawEvent)
			result.PassthroughUpstreamFailure = true
			continue
		}
		inspection := InspectResponseSemanticFrames(frames, i.policies, i.downstreamWritten, "sse", i.context)
		if len(inspection.Observations) > 0 {
			i.observations = append(i.observations, inspection.Observations...)
		}
		if inspection.Decision != nil {
			result.Chunks = nil
			result.Observations = append(result.Observations, *inspection.Decision)
			result.Intercepted = inspection.Decision
			result.PendingEvent = i.pending.Len() > 0
			return result
		}
		if len(frames) > 0 {
			i.seenData = true
		}
		result.Chunks = append(result.Chunks, rawEvent)
	}
	result.PendingEvent = i.pending.Len() > 0
	return result
}

// FlushPendingOnEOF 对齐 flushPendingOnEof。
func (i *OpenAIStreamInterceptor) FlushPendingOnEOF() StreamInterceptorSseResult {
	if i.pending.Len() == 0 {
		return StreamInterceptorSseResult{}
	}
	remaining := make([]byte, i.pending.Len())
	copy(remaining, i.pending.Bytes())
	i.pending.Reset()
	return StreamInterceptorSseResult{Chunks: [][]byte{remaining}}
}

// MarkDownstreamWrite 对齐 markDownstreamWrite。
func (i *OpenAIStreamInterceptor) MarkDownstreamWrite() {
	i.downstreamWritten = true
}

// TakeObservations 取走累积的 dry_run 观察。
func (i *OpenAIStreamInterceptor) TakeObservations() []ResponseInspectionDecision {
	observations := i.observations
	i.observations = nil
	return observations
}

func (i *OpenAIStreamInterceptor) isCodexCompactionContext() bool {
	// 对齐 shouldInspectCodexCompactionContract：契约帧只挂在 responses 端点
	// 族，且要求 codex 画像 + codex_responses 兼容 + 期望 compaction。
	return i.endpointFamily == gatewayproto.EndpointFamilyResponses &&
		i.context != nil &&
		i.context.ClientProfile == "codex" &&
		i.context.AccountClientCompatibility == "codex_responses"
}

// mountResponseInspectionInterceptor 对齐 Node pipeUpstreamStream 的拦截器
// 装配条件（stream.ts:215-219）：
//
//	hasPolicies || (interpretProtocolFailures && clientProfile != 'generic_anthropic' && clientRetryEnabled)
//
// 策略存在即启用；无策略时要求协议解释开启、客户端画像非 generic_anthropic
// 且预提交客户端重试开启。Codex compaction 契约帧经
// ResponseInspectionContext.CodexCompactionExpected 启用。
// 归档中 driver.sseResponseInspectionFailureEvent === 'none' 的失败事件抑制
// 分支依赖 buildFailureEvent 注入口，Go 拦截器面暂未提供（当前唯一驱动
// openai 返回 "response.failed"，分支不可达）。
func mountResponseInspectionInterceptor(options StreamPipeOptions, driver StreamDriver) StreamInterceptor {
	hasPolicies := len(options.ResponseInspectionPolicies) > 0
	context := options.ResponseInspectionContext
	enabled := hasPolicies ||
		(options.InterpretProtocolFailures &&
			(context == nil || context.ClientProfile != "generic_anthropic") &&
			options.ClientRetryEnabled)
	if !enabled {
		return nil
	}
	return NewOpenAIStreamInterceptor(OpenAIStreamInterceptorOptions{
		ClientRetryEnabled: options.ClientRetryEnabled,
		Policies:           options.ResponseInspectionPolicies,
		EndpointFamily:     driver.ResponseInspectionEndpointFamily(options.EndpointFamily),
		Context:            context,
		CompactionExpected: context != nil && context.CodexCompactionExpected,
	})
}

// shiftEvent 取出一个完整 SSE 事件（含边界空行）；未完则返回 nil。
func (i *OpenAIStreamInterceptor) shiftEvent() []byte {
	data := i.pending.Bytes()
	if len(data) == 0 {
		return nil
	}
	// 事件边界：\n\n、\r\n\r\n、\r\r。
	for index := 0; index < len(data); index++ {
		b := data[index]
		if b != '\n' && b != '\r' {
			continue
		}
		if b == '\n' {
			if index+1 < len(data) && data[index+1] == '\n' {
				event := append([]byte(nil), data[:index+2]...)
				i.consume(len(event))
				return event
			}
			continue
		}
		// '\r'
		if index+1 < len(data) && data[index+1] == '\r' {
			event := append([]byte(nil), data[:index+2]...)
			i.consume(len(event))
			return event
		}
	}
	return nil
}

func (i *OpenAIStreamInterceptor) consume(count int) {
	remaining := i.pending.Bytes()[count:]
	next := make([]byte, len(remaining))
	copy(next, remaining)
	i.pending.Reset()
	i.pending.Write(next)
}

// openAIEndpointFamilyOrUnknown 对齐 gatewayopenai 的 family 归一。
func openAIEndpointFamilyOrUnknown(family gatewayproto.ResponseEndpointFamily) gatewayproto.ResponseEndpointFamily {
	switch family {
	case gatewayproto.EndpointFamilyChatCompletions, gatewayproto.EndpointFamilyResponses:
		return family
	default:
		return gatewayproto.EndpointFamilyUnknown
	}
}

// interceptorFailureEvent 构建拦截后的失败事件（SSE 传输），对齐 G02 缓冲的
// buildFailureEvent 缺省行为：以 gatewayStreamClientRetryErrorCode 语义补发。
func interceptorFailureEvent(decision *ResponseInspectionDecision, clientRetryEnabled bool) []byte {
	errorCode := orDefault(decision.RewriteErrorCode, decision.UpstreamErrorCode)
	message := orDefault(decision.RewriteMessage, decision.UpstreamErrorMessage)
	if clientRetryEnabled && decision.RetryEnabled {
		errorCode = gatewaypreauth.GatewayStreamClientRetryErrorCode
		message = GatewayStreamClientRetryMessage
	}
	if errorCode == "" {
		errorCode = "response_inspection_matched"
	}
	return gatewaypreauth.BuildGatewayStreamFailureEvent(message, errorCode)
}
