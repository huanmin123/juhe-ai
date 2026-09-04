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
// 说明：G02 的 gatewayopenai.ResponseInspectionBuffer 构造器当前未把
// options.Policies 挂到实例上（策略永不触发），且该包不在本工作包可修改范围；
// 因此本拦截器自包含事件分帧逻辑，逐事件透传原始字节，保持字节序一致。
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
	return i.context != nil &&
		i.context.ClientProfile == "codex" &&
		i.context.AccountClientCompatibility == "codex_responses"
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
