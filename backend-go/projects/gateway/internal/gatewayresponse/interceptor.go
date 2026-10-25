package gatewayresponse

import (
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

	// pending 是未分帧完的 SSE 字节缓冲：append 追加 + consume 原地前移压缩，
	// 容量跨 chunk/事件复用（原 bytes.Buffer 每事件一次 make+copy）。
	pending           []byte
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
	i.pending = append(i.pending, chunk...)
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
			result.PendingEvent = len(i.pending) > 0
			return result
		}
		if len(frames) > 0 {
			i.seenData = true
		}
		result.Chunks = append(result.Chunks, rawEvent)
	}
	result.PendingEvent = len(i.pending) > 0
	return result
}

// FlushPendingOnEOF 对齐 flushPendingOnEof。
func (i *OpenAIStreamInterceptor) FlushPendingOnEOF() StreamInterceptorSseResult {
	if len(i.pending) == 0 {
		return StreamInterceptorSseResult{}
	}
	remaining := append([]byte(nil), i.pending...)
	i.pending = i.pending[:0]
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

// shiftEvent 取出一个完整 SSE 事件（含边界空行）；未完则返回 nil。事件字节
// 在返回前复制——透传 chunk 会被捕获器/预提交缓冲持有到流结束，不能引用
// 本缓冲。
func (i *OpenAIStreamInterceptor) shiftEvent() []byte {
	return shiftSseEvent(&i.pending)
}

// shiftSseEvent 是 SSE 事件分帧的共享实现（OpenAIStreamInterceptor 与
// ParseError 事件隔离器同源）：事件边界 \n\n、\r\n\r\n、\r\r。
func shiftSseEvent(pending *[]byte) []byte {
	data := *pending
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
				consumeSseEvent(pending, len(event))
				return event
			}
			continue
		}
		// '\r'
		if index+1 < len(data) && data[index+1] == '\r' {
			event := append([]byte(nil), data[:index+2]...)
			consumeSseEvent(pending, len(event))
			return event
		}
	}
	return nil
}

// consumeSseEvent 原地把已取走事件之后的剩余字节前移（memmove 语义），容量
// 复用、零分配。
func consumeSseEvent(pending *[]byte, count int) {
	*pending = append((*pending)[:0], (*pending)[count:]...)
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

// ---------------------------------------------------------------------------
// ParseError 事件字节隔离（流式提交边界受限重放与异常终局处置设计 §3.2.0）
// ---------------------------------------------------------------------------

// parseErrorEventSnippetMaxBytes 是首个被隔离事件的原始字节摘要上限（WARN
// 观测面用，对齐 rolling tail 的量级）。
const parseErrorEventSnippetMaxBytes = 256

// parseErrorEventIsolatorMaxHoldBytes 是未完结事件的持有上限（对齐
// StreamPreCommitBufferMaxBytes / inspector 单行上限）：越过即把持有字节原样
// 放行回下游判定路径（超大帧保护、预提交缓冲语义照常生效），不为无界垃圾
// 字节改变既有处置。
const parseErrorEventIsolatorMaxHoldBytes = StreamPreCommitBufferMaxBytes

// parseErrorEventIsolator 在写下游前按事件粒度隔离无法 JSON 解析的事件
// （gatewayopenai.ParseSseEventText 的 DataParseError 分类，含 W22 的
// 「半截 chunk + 错误帧粘连」残片）与协议形状异常事件（解析成功但顶层
// `choices` 键存在且 JSON 值不是数组，§3.2.0 L2 v2.3 增量，生产实证空壳流
// 首帧 `"choices":null`）：与既有失败帧拦截同一时序保证——事件级分类先于
// 写入。inspector 消费原始字节（计数照常），本隔离器只过滤下游字节；首个被
// 隔离事件保留截断摘要供 WARN 观测（两类隔离共用首个摘要位）。
//
// 快路径零改写：本批事件全部可解析且形状合法时原样返回入参切片（不复制、
// 不改变下游写出粒度），只有真实出现被隔离事件时才按事件边界重写后续字节。
type parseErrorEventIsolator struct {
	pending      []byte
	dropped      int
	snippet      string
	snippetIndex int
}

// newParseErrorEventIsolatorForDriver 按 pipe 协议驱动装配隔离器：仅
// gatewayopenai inspector 驱动的直通管道启用（Anthropic / Gemini native 流的
// parse-error 有各自 summary 机制，为后续批次，设计 §1 非目标 4）。
func newParseErrorEventIsolatorForDriver(driver StreamDriver) *parseErrorEventIsolator {
	if driver == nil || driver.ClientErrorProtocol() != "openai" {
		return nil
	}
	return &parseErrorEventIsolator{}
}

// push 过滤一个下游字节分片：返回当前可安全写出的字节序列。快路径（本批全部
// 为完整且可解析的事件）原样返回入参切片——零复制、不改写下游写出粒度；出现
// 解析失败事件时按事件边界重写，坏事件字节不进入返回值；跨 chunk 未完结事件
// 的尾字节保留在 pending，完结后再判定（与拦截器挂载时的按事件边界写出粒度
// 一致，保证坏事件前缀不泄漏）。持有越过 parseErrorEventIsolatorMaxHoldBytes
// 时整段原样放行：超大帧不改变既有判定路径（pre-commit 保护照常生效）。
func (iso *parseErrorEventIsolator) push(chunk []byte) []byte {
	if len(chunk) == 0 {
		return chunk
	}
	iso.pending = append(iso.pending, chunk...)
	if len(iso.pending) > parseErrorEventIsolatorMaxHoldBytes {
		held := iso.pending
		iso.pending = nil
		return held
	}
	droppedBefore := iso.dropped
	var out []byte
	for {
		rawEvent := shiftSseEvent(&iso.pending)
		if rawEvent == nil {
			break
		}
		event := gatewayopenai.ParseSseEventText(string(rawEvent))
		if event.DataParseError || gatewayopenai.IsProtocolInvalidChunkFrame(event.Data) {
			// 解析失败（§3.2.0）与协议形状异常（§3.2.0 L2）同处置：字节不外泄。
			iso.rememberDroppedEvent(rawEvent)
			continue
		}
		out = append(out, rawEvent...)
	}
	if iso.dropped == droppedBefore && len(iso.pending) == 0 {
		return chunk
	}
	return out
}

// flushPending 在上游 EOF 时放行隔离器持有的未完结事件尾字节：未完结事件
// 无法完成分类，按既有行为透传（不新造截断语义）。
func (iso *parseErrorEventIsolator) flushPending() []byte {
	if len(iso.pending) == 0 {
		return nil
	}
	remaining := append([]byte(nil), iso.pending...)
	iso.pending = iso.pending[:0]
	return remaining
}

// firstDropped 返回首个被隔离事件的截断摘要与其事件序号（自 1 起计）。
func (iso *parseErrorEventIsolator) firstDropped() (string, int) {
	return iso.snippet, iso.snippetIndex
}

func (iso *parseErrorEventIsolator) rememberDroppedEvent(rawEvent []byte) {
	iso.dropped++
	if iso.snippetIndex != 0 {
		return
	}
	iso.snippetIndex = iso.dropped
	snippet := rawEvent
	if len(snippet) > parseErrorEventSnippetMaxBytes {
		snippet = snippet[:parseErrorEventSnippetMaxBytes]
	}
	iso.snippet = string(snippet)
}
