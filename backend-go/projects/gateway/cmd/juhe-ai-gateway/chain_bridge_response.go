package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayopenai"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/openaicompat/openaicompatbridge"
	"github.com/huanminabc/juhe-ai/backend-go-platform/safego"
)

// B-4 cross-protocol bridge response face (audit batch ah group A). Port of
// the Node transformUpstreamResponse chains:
//
//	gpt / openai-compatible / deepseek / glm drivers:
//	  transformAnthropicMessagesChatBridgeUpstreamResponse
//	  -> transformGeminiGenerateContentChatBridgeUpstreamResponse
//	  -> transformCodexResponsesChatBridgeUpstreamResponse
//	anthropic driver:
//	  transformGeminiGenerateContentAnthropicMessagesBridgeUpstreamResponse
//	  -> transformOpenAIToAnthropicBridgeUpstreamResponse
//	gemini driver:
//	  transformGeminiCodeAssistUpstreamResponse
//	  -> transformGeminiNativeTargetBridgeUpstreamResponse
//
// The dispatch engine owns the attempt loop and calls the transformer once per
// OK upstream response (gatewaydispatch.Engine.ResponseTransformer); error
// responses stay untouched exactly like the Node `!response.ok` guards.

// chainBridgeResponseTransformer implements
// gatewaydispatch.UpstreamResponseTransformer over the openaicompat bridge
// response cores.
type chainBridgeResponseTransformer struct{}

// newChainBridgeResponseTransformer builds the composition-root transformer.
func newChainBridgeResponseTransformer() *chainBridgeResponseTransformer {
	return &chainBridgeResponseTransformer{}
}

// TransformUpstreamResponseForAccount implements
// gatewaydispatch.UpstreamResponseTransformer. The resolved account mapping
// picks the bridge pair; responses without a cross-protocol mapping pass
// through untouched.
func (t *chainBridgeResponseTransformer) TransformUpstreamResponseForAccount(
	input gatewaydispatch.UpstreamResponseTransformInput,
) (*gatewaydispatch.GatewayUpstreamResponse, error) {
	req := input.Req
	response := input.Response
	if req == nil || response == nil || !response.OK() || response.Body == nil {
		return response, nil
	}
	// DD-C6：每个 attempt 进入响应转换面即换入本次的桥接 usage 快照槽——上一
	// attempt（可能桥接）的槽就此失效，本次非桥接（含透传 / Code Assist unwrap）
	// 时不给 finalization 留任何残留供给（重试换号场景的隔离）。
	bridgeSlot := &chainBridgeUsageSlot{}
	chainBridgeUsageCarrierOf(req.HTTP).replaceSlot(bridgeSlot)
	mapping := chainBridgeResponseMappingOf(req, input.Account)
	if mapping == nil {
		// Native Code Assist requests carry no cross-protocol model mapping,
		// but the request side still wraps them into the Code Assist endpoint
		// family (chain_driver.go), so the {response:...} upstream envelope
		// must be unwrapped before the nil-mapping passthrough (Node
		// transformGeminiCodeAssistUpstreamResponse sits first in the gemini
		// driver chain, ahead of the native target bridge).
		return t.transformGeminiCodeAssistIfApplicable(input, nil, response)
	}
	sourceFamily := openaicompatbridge.NormalizeEndpointFamily(mapping.SourceEndpointFamily)
	upstreamFamily := openaicompatbridge.NormalizeEndpointFamily(mapping.UpstreamEndpointFamily)
	// BUG-0178：responses -> chat_completions 回转只服务 chat-only 账户；持有
	// 原生 Responses 端点模式的账户（codex OAuth、responses 模式 api_key）对
	// /responses 按原生直通，与 URL 构建器的改写守卫（chain_driver.go
	// accountHasNativeResponsesModes）成对生效，避免把原生 Responses 响应误
	// 当 chat 上游回转。
	if sourceFamily == upstreamFamily || !openaicompatbridge.IsCrossProtocolBridgeRequired(sourceFamily, upstreamFamily) ||
		(sourceFamily == openaicompatbridge.FamilyResponses && upstreamFamily == openaicompatbridge.FamilyChatCompletions &&
			accountHasNativeResponsesModes(input.Account)) {
		// Gemini Code Assist accounts unwrap {response:...} even without a
		// cross-protocol mapping (Node transformGeminiCodeAssistUpstreamResponse
		// sits ahead of the native target bridge in the gemini driver chain).
		return t.transformGeminiCodeAssistIfApplicable(input, mapping, response)
	}
	stream := gatewaypreauth.RequestStream(req)

	model := strings.TrimSpace(mapping.UpstreamModel)
	if model == "" {
		if requested, ok := gatewaypreauth.RequestModel(req); ok {
			model = requested
		}
	}
	headers := response.Header.Clone()

	// 流式桥转换走增量管道（Node 逐事件生成器语义）：上游流按事件边界被增量
	// 消费并即时转换，首个转换事件在上游 EOF 之前即可送达下游（不丢首 token
	// 延迟、取消与背压语义，也不占用完整响应大小的内存）；上游断开 / 请求取消
	// 以管道错误传播。非流式保持 buffer 语义（无流式问题，ReadAll 合法）；
	// 无增量变体的组合退回 buffer 路径（pump 覆盖面与既有流式分支一致）。
	if stream {
		if pump := t.bridgeStreamPump(input, mapping, sourceFamily, upstreamFamily, model, bridgeSlot); pump != nil {
			headers.Set("Content-Type", "text/event-stream; charset=utf-8")
			headers.Del("Content-Length")
			return bridgeStreamPipedResponse(response, headers, pump), nil
		}
	}

	body, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}
	_ = response.Body.Close()

	transformed := []byte(nil)
	switch {
	case upstreamFamily == openaicompatbridge.FamilyChatCompletions:
		transformed = t.transformToChatClient(input, sourceFamily, body, model, stream)
	case upstreamFamily == openaicompatbridge.FamilyAnthropicMessages:
		transformed = t.transformToAnthropicClient(input, sourceFamily, body, model, stream)
	case upstreamFamily == openaicompatbridge.FamilyGeminiGenerateContent || upstreamFamily == openaicompatbridge.FamilyGeminiStreamGenerate:
		transformed = t.transformToGeminiNativeClient(input, mapping, body, model, stream)
	}
	if transformed == nil {
		return response, nil
	}
	if stream {
		headers.Set("Content-Type", "text/event-stream; charset=utf-8")
	} else {
		headers.Set("Content-Type", "application/json; charset=utf-8")
	}
	headers.Del("Content-Length")
	return gatewaydispatch.NewGatewayUpstreamResponseForTransform(response.Status(), headers, io.NopCloser(strings.NewReader(string(transformed)))), nil
}

// bridgeStreamPipedResponse 把上游响应体替换为 io.Pipe：后台 goroutine 按事件
// 边界增量消费上游流并逐事件转换写入管道（Node for-await 生成器语义），下游
// 拿到的响应体在上游 EOF 之前即可读到首个转换事件。EOF 正常关写；读取错误
// （含上游断开、请求取消触发的传输错误）经 CloseWithError 传播为下游读取
// 错误（Node abort 分支的截断语义）；下游提前关闭时管道写失败，goroutine
// 退出并关闭上游体（并发槽释放 + 请求取消）。
func bridgeStreamPipedResponse(
	response *gatewaydispatch.GatewayUpstreamResponse,
	headers http.Header,
	pump func(src io.Reader, dst io.Writer) error,
) *gatewaydispatch.GatewayUpstreamResponse {
	reader, writer := io.Pipe()
	go func() {
		defer safego.Handle("juheai.chain_bridge_response.stream_pump", func(recovered any) {
			// panic 兜底：以错误关闭管道写端，避免下游读取永久阻塞。
			_ = writer.CloseWithError(fmt.Errorf("桥接流式泵异常终止: %v", recovered))
		})
		// goroutine 独占上游体的消费责任：EOF 或任一错误都关闭上游体
		// （slotReleasingBody 的 release/cancel 幂等）。
		defer response.Body.Close()
		if err := pump(response.Body, writer); err != nil {
			_ = writer.CloseWithError(err)
			return
		}
		_ = writer.Close()
	}()
	return gatewaydispatch.NewGatewayUpstreamResponseForTransform(response.Status(), headers, reader)
}

// chainBridgePumpWithUsageSnapshot 包装桥泵（DD-C6 / DD-RC-C1）：pump 正常
// 返回（上游 EOF、finish 臂已终值化桥状态）后把桥内计算 usage 快照写入本次
// attempt 槽，经 HandleUpstreamResponseInput.BridgeUsage 供给 finalization。
// usage 或 slot 缺席（未钉住方向 / 无载体）返回裸泵，行为与既有完全一致。
// 流中断（err != nil）不写：pipe 错误路径（chain finalize 的 pipeErr 臂）不
// 消费快照。快照的消费端完成条件见 finalize.go（DD2-B1/B2）：仅完成流
// （pipeResult.Completed）消费快照，截断/失败终态回落既有估算兜底——中途
// usage 的半程快照不得把截断流计费成 0 输出。
// DD-RC-C1：usage() 的 ok=false 表示上游确实没发 usage（桥状态 Usage 为
// nil map）——此时不写快照，supplier 对本次 attempt 返回 ok=false，finalize
// 保持既有估算兜底（estimatedOutputTokens），不得把 nil 桥状态转成零值快照
// 否决估算（那会把「上游没发 usage」计费成 0）。ok=true（含上游发了全 0
// usage 的合法形态）照常写入，计 0 是上游事实。
func chainBridgePumpWithUsageSnapshot(
	pump func(src io.Reader, dst io.Writer) error,
	usage func() (gatewayproto.ParsedUsage, bool),
	slot *chainBridgeUsageSlot,
) func(src io.Reader, dst io.Writer) error {
	if usage == nil || slot == nil {
		return pump
	}
	return func(src io.Reader, dst io.Writer) error {
		err := pump(src, dst)
		if err == nil {
			if snapshot, ok := usage(); ok {
				slot.storeUsage(snapshot)
			}
		}
		return err
	}
}

// bridgeStreamPump builds the incremental per-event pump of one streaming
// bridge direction; nil = the direction has no streaming variant and keeps the
// buffered transform. The per-event state machines and finish semantics mirror
// the buffered branches in transformToChatClient / transformToAnthropicClient /
// transformToGeminiNativeClient verbatim.
//
// usageSlot（DD-C6，变参保持既有调用形态）：传入本次 attempt 的桥接 usage 快
// 照槽时，钉住方向（anthropic messages 上游 → chat 客户端、chat completions
// 上游 → gemini 客户端）的 pump 在正常返回后把桥状态终值 usage 写入槽位，经
// finalization 的 BridgeUsage 供给结算；其余方向无桥内 usage 供给（T9b
// chat→anthropic 方向的 inspector 双口径照常生效，行为不变）。DD-RC-C1：桥
// 状态 Usage 为 nil（上游未发 usage）时不写快照，finalization 回落既有估算
// 兜底；非 nil（含全 0）才写入。
func (t *chainBridgeResponseTransformer) bridgeStreamPump(
	input gatewaydispatch.UpstreamResponseTransformInput,
	mapping *gatewayproto.ResolvedModelMapping,
	sourceFamily, upstreamFamily, model string,
	usageSlot ...*chainBridgeUsageSlot,
) func(src io.Reader, dst io.Writer) error {
	var slot *chainBridgeUsageSlot
	if len(usageSlot) > 0 {
		slot = usageSlot[0]
	}
	switch upstreamFamily {
	case openaicompatbridge.FamilyChatCompletions:
		switch sourceFamily {
		case openaicompatbridge.FamilyAnthropicMessages:
			// transformAnthropicMessagesChatBridgeUpstreamResponse（流式）。
			state := openaicompatbridge.NewAnthropicFromChatStreamState(model)
			return func(src io.Reader, dst io.Writer) error {
				return openaicompatbridge.PumpBridgeSseTransform(src, dst, func(event string) []string {
					return openaicompatbridge.ProcessChatCompletionsSseEventAsAnthropic(state, event)
				}, func() []string {
					if !state.Completed && !state.Failed {
						if state.StopReason != "" {
							return openaicompatbridge.CompleteAnthropicFromChatStream(state)
						}
						return openaicompatbridge.FailAnthropicFromChatStream(state, "上游 Chat Completions SSE 在 message_stop 前中断", "upstream_stream_interrupted")
					}
					return nil
				})
			}
		case openaicompatbridge.FamilyGeminiGenerateContent, openaicompatbridge.FamilyGeminiStreamGenerate:
			// transformGeminiGenerateContentChatBridgeUpstreamResponse（流式）。
			// DD-C6：桥状态终值 usage（usageMetadata 形）供结算链，inspector 按
			// 上游账户 openai 协议解析桥接后字节取不到 usage。
			// DD-RC-C1：state.Usage 只在桥内确实取到上游 usage 对象时被赋值
			// （ProcessChatCompletionsSseEventAsGemini 的 usage != nil 守卫），
			// nil = 上游未发 usage → 不写快照回落估算兜底；非 nil（含全 0）=
			// 上游事实，照常快照计 0。
			state := openaicompatbridge.NewGeminiChatStreamState(model)
			pump := func(src io.Reader, dst io.Writer) error {
				return openaicompatbridge.PumpBridgeSseTransform(src, dst, func(event string) []string {
					return openaicompatbridge.ProcessChatCompletionsSseEventAsGemini(state, event)
				}, func() []string {
					if !state.Completed && !state.Failed {
						return openaicompatbridge.CompleteGeminiChatStream(state)
					}
					return nil
				})
			}
			return chainBridgePumpWithUsageSnapshot(pump, func() (gatewayproto.ParsedUsage, bool) {
				if state.Usage == nil {
					return gatewayproto.EmptyUsage(), false
				}
				return chainBridgeGeminiUsageMapToParsedUsage(state.Usage), true
			}, slot)
		case openaicompatbridge.FamilyResponses:
			// transformCodexResponsesChatBridgeUpstreamResponse（chat SSE ->
			// Responses SSE 回转）。
			options := openaicompatbridge.CodexResponsesChatBridgeTransformOptions{
				Enabled:      true,
				DefaultModel: model,
				Model:        model,
				IDPrefix:     "openai_bridge",
			}
			if parsed := input.Req.ParsedJSONObjectBody(); parsed != nil {
				options.ToolAdaptersByChatName = openaicompatbridge.CodexResponsesChatBridgeToolAdaptersFromClientBody(parsed)
				estimated := openaicompatbridge.EstimateCodexResponsesRequestInputTokens(parsed)
				options.EstimatedInputTokens = &estimated
				options.PreviousResponseID = chainBridgePreviousResponseIDOf(parsed)
			}
			return func(src io.Reader, dst io.Writer) error {
				return openaicompatbridge.PumpChatCompletionsSseToResponsesSse(src, dst, options)
			}
		}
	case openaicompatbridge.FamilyAnthropicMessages:
		switch sourceFamily {
		case openaicompatbridge.FamilyGeminiGenerateContent, openaicompatbridge.FamilyGeminiStreamGenerate:
			// transformGeminiGenerateContentAnthropicMessagesBridgeUpstreamResponse（流式）。
			state := openaicompatbridge.NewAnthropicGeminiStreamState(model)
			return func(src io.Reader, dst io.Writer) error {
				return openaicompatbridge.PumpBridgeSseTransform(src, dst, func(event string) []string {
					return state.ProcessAnthropicSseEvent(event)
				}, func() []string {
					if !state.Completed && !state.Failed {
						return state.CompleteGeminiStream()
					}
					return nil
				})
			}
		case openaicompatbridge.FamilyChatCompletions, openaicompatbridge.FamilyResponses:
			// transformOpenAIToAnthropicBridgeUpstreamResponse（流式）。
			previousResponseID := ""
			if parsed := input.Req.ParsedJSONObjectBody(); parsed != nil {
				previousResponseID = chainBridgePreviousResponseIDOf(parsed)
			}
			if sourceFamily == openaicompatbridge.FamilyResponses {
				return func(src io.Reader, dst io.Writer) error {
					return openaicompatbridge.PumpAnthropicMessagesSseToResponsesSse(src, dst, model, previousResponseID)
				}
			}
			state := openaicompatbridge.NewAnthropicChatStreamState(model)
			// DD-C6：桥状态终值 usage（chat 形，anthropicUsageToChatUsage 已把
			// 上游 input_tokens/output_tokens/cache_* 重渲染）供结算链——inspector
			// 按 anthropic 协议解析桥接后的 chat 字节取不到 usage。
			// DD-RC-C1：state.Usage 只在上游 message_start/message_delta 确实
			// 携带 usage 对象时被赋值（ProcessAnthropicEventAsChat 的 usage !=
			// nil 守卫），nil = 上游未发 usage → 不写快照回落估算兜底；非 nil
			// （含 anthropicUsageToChatUsage 渲染出的全 0 键形）= 上游事实，
			// 照常快照计 0。
			pump := func(src io.Reader, dst io.Writer) error {
				return openaicompatbridge.PumpBridgeSseTransform(src, dst, func(event string) []string {
					return openaicompatbridge.ProcessAnthropicEventAsChat(state, event)
				}, func() []string {
					if !state.Completed && !state.Failed {
						return openaicompatbridge.FailAnthropicChatStream(state, "上游 Anthropic Messages SSE 在正常结束事件前中断", "upstream_stream_interrupted")
					}
					return nil
				})
			}
			return chainBridgePumpWithUsageSnapshot(pump, func() (gatewayproto.ParsedUsage, bool) {
				if state.Usage == nil {
					return gatewayproto.EmptyUsage(), false
				}
				return chainBridgeChatUsageMapToParsedUsage(state.Usage), true
			}, slot)
		}
	case openaicompatbridge.FamilyGeminiGenerateContent, openaicompatbridge.FamilyGeminiStreamGenerate:
		// transformGeminiNativeTargetBridgeUpstreamResponse（流式）。
		protocol := openaicompatbridge.GeminiNativeDownstreamProtocolForMapping(mapping.SourceEndpointFamily)
		if protocol == openaicompatbridge.GeminiNativeProtocolChatCompletions {
			state := openaicompatbridge.NewGeminiNativeChatStreamState(model)
			return func(src io.Reader, dst io.Writer) error {
				return openaicompatbridge.PumpBridgeSseTransform(src, dst, func(event string) []string {
					return openaicompatbridge.ProcessGeminiSseEventAsChat(state, event)
				}, func() []string {
					if !state.Completed && !state.Failed && !state.TerminalReceived {
						return openaicompatbridge.CompleteGeminiNativeChatStream(state)
					}
					return nil
				})
			}
		}
		return func(src io.Reader, dst io.Writer) error {
			return openaicompatbridge.PumpGeminiSseToDownstreamSse(src, dst, protocol, model)
		}
	}
	return nil
}

// transformToChatClient covers the chat upstream -> chat-protocol clients:
// anthropic/gemini source bridging plus the codex responses -> chat SSE 回转
// (Node gpt/openai-compatible/deepseek/glm transformUpstreamResponse chain).
func (t *chainBridgeResponseTransformer) transformToChatClient(
	input gatewaydispatch.UpstreamResponseTransformInput,
	sourceFamily string,
	body []byte,
	model string,
	stream bool,
) []byte {
	req := input.Req
	switch sourceFamily {
	case openaicompatbridge.FamilyAnthropicMessages:
		// transformAnthropicMessagesChatBridgeUpstreamResponse.
		if stream {
			state := openaicompatbridge.NewAnthropicFromChatStreamState(model)
			out := &strings.Builder{}
			_ = openaicompatbridge.PumpBridgeSseTransform(strings.NewReader(string(body)), out, func(event string) []string {
				return openaicompatbridge.ProcessChatCompletionsSseEventAsAnthropic(state, event)
			}, func() []string {
				if !state.Completed && !state.Failed {
					if state.StopReason != "" {
						return openaicompatbridge.CompleteAnthropicFromChatStream(state)
					}
					return openaicompatbridge.FailAnthropicFromChatStream(state, "上游 Chat Completions SSE 在 message_stop 前中断", "upstream_stream_interrupted")
				}
				return nil
			})
			return []byte(out.String())
		}
		parsed, err := openaicompatbridge.ExtractJSONObject(string(body))
		if err != nil {
			return openaicompatbridge.TransformAnthropicJSONToChatErrorBody()
		}
		// 缺陷修复（w14a）：chat 上游 → anthropic 客户端的缓冲臂此前误用
		// TransformAnthropicMessagesJSONToChatJSONBody（anthropic→chat 反向
		// 渲染）；Node transformAnthropicMessagesChatBridgeUpstreamResponse 的
		// 非流式分支是 chatCompletionsJson → AnthropicMessagesJson。
		return []byte(openaicompatbridge.BridgeJSONStringifyOf(openaicompatbridge.ChatCompletionJSONToAnthropicMessage(parsed, model)))
	case openaicompatbridge.FamilyGeminiGenerateContent, openaicompatbridge.FamilyGeminiStreamGenerate:
		// transformGeminiGenerateContentChatBridgeUpstreamResponse.
		if stream {
			state := openaicompatbridge.NewGeminiChatStreamState(model)
			out := &strings.Builder{}
			_ = openaicompatbridge.PumpBridgeSseTransform(strings.NewReader(string(body)), out, func(event string) []string {
				return openaicompatbridge.ProcessChatCompletionsSseEventAsGemini(state, event)
			}, func() []string {
				if !state.Completed && !state.Failed {
					return openaicompatbridge.CompleteGeminiChatStream(state)
				}
				return nil
			})
			return []byte(out.String())
		}
		parsed, err := openaicompatbridge.ExtractJSONObject(string(body))
		if err != nil {
			return []byte(bridgeChatUpstreamInvalidJSONErrorBody(model, "上游 Chat Completions 返回了无法解析为 Gemini GenerateContent 的响应。客户端可以保持当前对话并重试，或换用更稳定的上游模型；网关已转换为 Gemini 文本提示，避免客户端因协议错误中断。"))
		}
		return []byte(openaicompatbridge.BridgeJSONStringifyOf(openaicompatbridge.ChatCompletionJSONToGeminiGenerateContent(parsed, model)))
	case openaicompatbridge.FamilyResponses:
		// transformCodexResponsesChatBridgeUpstreamResponse: chat SSE -> the
		// Responses protocol the codex client speaks (SSE 或单 JSON 响应）。
		options := openaicompatbridge.CodexResponsesChatBridgeTransformOptions{
			Enabled:      true,
			DefaultModel: model,
			Model:        model,
			IDPrefix:     "openai_bridge",
		}
		if parsed := req.ParsedJSONObjectBody(); parsed != nil {
			options.ToolAdaptersByChatName = openaicompatbridge.CodexResponsesChatBridgeToolAdaptersFromClientBody(parsed)
			estimated := openaicompatbridge.EstimateCodexResponsesRequestInputTokens(parsed)
			options.EstimatedInputTokens = &estimated
			options.PreviousResponseID = chainBridgePreviousResponseIDOf(parsed)
		}
		if stream {
			return openaicompatbridge.TransformChatCompletionsSseBufferToResponsesSse(body, options)
		}
		return openaicompatbridge.TransformChatCompletionsSseBufferToResponsesJSON(body, options)
	}
	return nil
}

// transformToAnthropicClient covers the anthropic messages upstream:
// gemini -> messages 回转 plus the openai (chat/responses) -> messages 回转.
func (t *chainBridgeResponseTransformer) transformToAnthropicClient(
	input gatewaydispatch.UpstreamResponseTransformInput,
	sourceFamily string,
	body []byte,
	model string,
	stream bool,
) []byte {
	switch sourceFamily {
	case openaicompatbridge.FamilyGeminiGenerateContent, openaicompatbridge.FamilyGeminiStreamGenerate:
		// transformGeminiGenerateContentAnthropicMessagesBridgeUpstreamResponse.
		if stream {
			state := openaicompatbridge.NewAnthropicGeminiStreamState(model)
			out := &strings.Builder{}
			_ = openaicompatbridge.PumpBridgeSseTransform(strings.NewReader(string(body)), out, func(event string) []string {
				return state.ProcessAnthropicSseEvent(event)
			}, func() []string {
				if !state.Completed && !state.Failed {
					return state.CompleteGeminiStream()
				}
				return nil
			})
			return []byte(out.String())
		}
		parsed, err := openaicompatbridge.ExtractJSONObject(string(body))
		if err != nil {
			return []byte(openaicompatbridge.BridgeJSONStringifyOf(map[string]any{
				"error": map[string]any{
					"status":  "INTERNAL",
					"code":    "upstream_anthropic_messages_invalid_json",
					"message": "上游 Anthropic Messages 返回了无法转换为 Gemini GenerateContent 响应的 JSON",
				},
			}))
		}
		return []byte(openaicompatbridge.BridgeJSONStringifyOf(openaicompatbridge.AnthropicMessageJSONToGeminiGenerateContent(parsed, model)))
	case openaicompatbridge.FamilyChatCompletions, openaicompatbridge.FamilyResponses:
		// transformOpenAIToAnthropicBridgeUpstreamResponse: the chat target
		// keeps the already-ported core; the responses target renders through
		// the responses surface.
		previousResponseID := ""
		if parsed := input.Req.ParsedJSONObjectBody(); parsed != nil {
			previousResponseID = chainBridgePreviousResponseIDOf(parsed)
		}
		if sourceFamily == openaicompatbridge.FamilyResponses {
			if stream {
				return openaicompatbridge.TransformAnthropicMessagesSseBufferToResponsesSse(body, model, previousResponseID)
			}
			return openaicompatbridge.TransformAnthropicMessagesJSONBufferToResponsesJSON(body, model, previousResponseID)
		}
		if stream {
			state := openaicompatbridge.NewAnthropicChatStreamState(model)
			out := &strings.Builder{}
			_ = openaicompatbridge.PumpBridgeSseTransform(strings.NewReader(string(body)), out, func(event string) []string {
				return openaicompatbridge.ProcessAnthropicEventAsChat(state, event)
			}, func() []string {
				if !state.Completed && !state.Failed {
					return openaicompatbridge.FailAnthropicChatStream(state, "上游 Anthropic Messages SSE 在正常结束事件前中断", "upstream_stream_interrupted")
				}
				return nil
			})
			return []byte(out.String())
		}
		parsed, err := openaicompatbridge.ExtractJSONObject(string(body))
		if err != nil {
			return openaicompatbridge.TransformAnthropicJSONToChatErrorBody()
		}
		return openaicompatbridge.TransformAnthropicMessagesJSONToChatJSONBody(parsed, model)
	}
	return nil
}

// transformToGeminiNativeClient covers the gemini native upstream ->
// chat/responses/messages clients (Node transformGeminiNativeTargetBridge-
// UpstreamResponse; the chat direction keeps the existing core).
func (t *chainBridgeResponseTransformer) transformToGeminiNativeClient(
	input gatewaydispatch.UpstreamResponseTransformInput,
	mapping *gatewayproto.ResolvedModelMapping,
	body []byte,
	model string,
	stream bool,
) []byte {
	protocol := openaicompatbridge.GeminiNativeDownstreamProtocolForMapping(mapping.SourceEndpointFamily)
	if protocol == openaicompatbridge.GeminiNativeProtocolChatCompletions {
		// The existing chat-direction core (bridge_response.go).
		if stream {
			state := openaicompatbridge.NewGeminiNativeChatStreamState(model)
			out := &strings.Builder{}
			_ = openaicompatbridge.PumpBridgeSseTransform(strings.NewReader(string(body)), out, func(event string) []string {
				return openaicompatbridge.ProcessGeminiSseEventAsChat(state, event)
			}, func() []string {
				if !state.Completed && !state.Failed && !state.TerminalReceived {
					return openaicompatbridge.CompleteGeminiNativeChatStream(state)
				}
				return nil
			})
			return []byte(out.String())
		}
		parsed, err := openaicompatbridge.ExtractJSONObject(string(body))
		if err != nil {
			parsed = map[string]any{}
		}
		return []byte(openaicompatbridge.BridgeJSONStringifyOf(openaicompatbridge.TransformGeminiGenerateContentToOpenAIChatResponse(parsed, openaicompatbridge.BridgeTransformResponseOptions{
			Enabled: true,
			Model:   model,
		})))
	}
	if stream {
		return openaicompatbridge.TransformGeminiSseBufferToDownstreamSse(body, protocol, model)
	}
	return openaicompatbridge.TransformGeminiJSONBufferToDownstreamJSON(body, protocol, model)
}

// transformGeminiCodeAssistIfApplicable unwraps the Code Assist {response:...}
// wrapper for google_oauth code_assist/google_one accounts (Node
// transformGeminiCodeAssistUpstreamResponse: 流式逐事件 unwrap、非流式收集
// 合并 text)。
func (t *chainBridgeResponseTransformer) transformGeminiCodeAssistIfApplicable(
	input gatewaydispatch.UpstreamResponseTransformInput,
	mapping *gatewayproto.ResolvedModelMapping,
	response *gatewaydispatch.GatewayUpstreamResponse,
) (*gatewaydispatch.GatewayUpstreamResponse, error) {
	if !geminiAccountUsesCodeAssistRuntime(input.Account) || !isGeminiCodeAssistGenerationRequest(input.Req) {
		return response, nil
	}
	stream := geminiCodeAssistDownstreamStreamForMapping(input.Req, mapping)
	// 流式 unwrap 走增量管道（Node 流式分支逐事件 unwrap）：事件边界到达即
	// 解包转发，上游未 EOF 时下游已可读到首个解包事件；非流式收集合并 text
	// 保持 buffer 语义。
	if stream {
		return bridgeStreamPipedResponse(response, response.Header.Clone(), openaicompatbridge.PumpUnwrapGeminiCodeAssistSse), nil
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}
	_ = response.Body.Close()
	// stream 已在上方管道分支 return（明显不可达守卫，w14a 删除）；此处恒为
	// 非流式收集合并。
	transformed := openaicompatbridge.CollectGeminiCodeAssistSseBuffer(body)
	if !strings.Contains(strings.ToLower(response.ContentType()), "json") {
		// The non-stream collection consumes the SSE stream into one JSON
		// payload (Node sets the JSON content type).
		headers := response.Header.Clone()
		headers.Set("Content-Type", "application/json; charset=utf-8")
		headers.Del("Content-Length")
		return gatewaydispatch.NewGatewayUpstreamResponseForTransform(response.Status(), headers, io.NopCloser(strings.NewReader(string(transformed)))), nil
	}
	return gatewaydispatch.NewGatewayUpstreamResponseForTransform(response.Status(), response.Header.Clone(), io.NopCloser(strings.NewReader(string(transformed)))), nil
}

// geminiCodeAssistDownstreamStreamForMapping mirrors geminiCodeAssistDownstreamStream:
// native-target bridge mappings follow the client stream flag, otherwise the
// gemini streamGenerateContent family.
func geminiCodeAssistDownstreamStreamForMapping(req *gatewaypreauth.GatewayRequest, mapping *gatewayproto.ResolvedModelMapping) bool {
	if mapping != nil {
		source := openaicompatbridge.NormalizeEndpointFamily(mapping.SourceEndpointFamily)
		if source == openaicompatbridge.FamilyChatCompletions || source == openaicompatbridge.FamilyResponses || source == openaicompatbridge.FamilyAnthropicMessages {
			return gatewaypreauth.RequestStream(req)
		}
	}
	return isGeminiStreamGenerateContentRequestPath(req)
}

func isGeminiStreamGenerateContentRequestPath(req *gatewaypreauth.GatewayRequest) bool {
	if req == nil {
		return false
	}
	return strings.Contains(gatewaypreauth.RequestPathWithoutQuery(req), ":streamGenerateContent")
}

// chainBridgeResponseMappingOf resolves the account mapping for the request
// through the same resolver the request-side bridge used.
func chainBridgeResponseMappingOf(req *gatewaypreauth.GatewayRequest, account gatewaydispatch.AccountCandidate) *gatewayproto.ResolvedModelMapping {
	requestedModel := ""
	if model, ok := gatewaypreauth.RequestModel(req); ok {
		requestedModel = model
	}
	if requestedModel == "" {
		return nil
	}
	mappings := make([]gatewayopenai.AccountModelMapping, 0, len(account.ModelMappings))
	for _, mapping := range account.ModelMappings {
		enabled := mapping.Enabled
		runtimeSource := ""
		if mapping.RuntimeSource != nil {
			runtimeSource = *mapping.RuntimeSource
		}
		runtimeRouteRuleID := ""
		if mapping.RuntimeRouteRuleID != nil {
			runtimeRouteRuleID = *mapping.RuntimeRouteRuleID
		}
		mappings = append(mappings, gatewayopenai.AccountModelMapping{
			SourceModel:            mapping.SourceModel,
			SourceEndpointFamily:   mapping.SourceEndpointFamily,
			UpstreamModel:          mapping.UpstreamModel,
			UpstreamEndpointFamily: mapping.UpstreamEndpointFamily,
			Enabled:                &enabled,
			RuntimeSource:          runtimeSource,
			RuntimeRouteRuleID:     runtimeRouteRuleID,
		})
	}
	runtimeAccount := &gatewayopenai.RuntimeAccount{
		ModelMappings: mappings,
	}
	resolved := gatewayopenai.ResolveAccountModelMapping(runtimeAccount, requestedModel, requestMappingSourceFamilyOf(req))
	if resolved == nil {
		return nil
	}
	return &gatewayproto.ResolvedModelMapping{
		SourceModel:            resolved.SourceModel,
		SourceEndpointFamily:   resolved.SourceEndpointFamily,
		UpstreamModel:          resolved.UpstreamModel,
		UpstreamEndpointFamily: resolved.UpstreamEndpointFamily,
		RuntimeSource:          resolved.RuntimeSource,
		RuntimeRouteRuleID:     resolved.RuntimeRouteRuleID,
	}
}

// chainBridgePreviousResponseIDOf reads previous_response_id from the parsed
// client body (the codex bridge completion handler context).
func chainBridgePreviousResponseIDOf(body map[string]any) string {
	if body == nil {
		return ""
	}
	text, _ := body["previous_response_id"].(string)
	return text
}

// bridgeChatUpstreamInvalidJSONErrorBody renders the gemini-protocol guidance
// payload for an unparseable chat upstream response (Node
// geminiChatBridgeResponseGuidanceJson).
func bridgeChatUpstreamInvalidJSONErrorBody(model, text string) string {
	return openaicompatbridge.BridgeJSONStringifyOf(map[string]any{
		"candidates": []any{map[string]any{
			"content": map[string]any{
				"role":  "model",
				"parts": []any{map[string]any{"text": text}},
			},
			"finishReason": "STOP",
			"index":        float64(0),
		}},
		"modelVersion": model,
	})
}

// The codex bridge completion persistence (Node onCompleted) rides the state
// port; the composition-root keeps the handler nil until the G18 storage is
// attached to the dispatch engine (chatbridgestate completion handler). The
// transform options therefore never carry OnCompleted here — the Node parity
// note is registered in the audit report.

// ---------------------------------------------------------------------------
// 桥接流 usage 结算传递（DD-C6）
// ---------------------------------------------------------------------------

// chainBridgeUsageSlot 是单次 attempt 的桥接 usage 快照槽：桥泵 goroutine 在
// 关闭响应管道前写入（store 先于 writer.Close 的程序序 + atomic 语义，下游
// 读到 EOF 时快照必然可见），finalization 在管道消费结束后经 supplier 读取。
// 槽指针随 attempt 更换，旧 attempt 迟到的写入落在旧槽上，不污染新 attempt。
type chainBridgeUsageSlot struct {
	value atomic.Pointer[gatewayproto.ParsedUsage]
}

func (s *chainBridgeUsageSlot) storeUsage(usage gatewayproto.ParsedUsage) {
	if s == nil {
		return
	}
	value := usage
	s.value.Store(&value)
}

func (s *chainBridgeUsageSlot) loadUsage() (gatewayproto.ParsedUsage, bool) {
	if s == nil {
		return gatewayproto.ParsedUsage{}, false
	}
	if value := s.value.Load(); value != nil {
		return *value, true
	}
	return gatewayproto.ParsedUsage{}, false
}

// chainBridgeUsageCarrier 是请求级载体（安装进 req.HTTP context，对象不可变
// 而 carrier 指针可变）：桥转换面每个 attempt 换入当前槽，finalization input
// 构造点取 supplier。非桥接 attempt 换入 nil 槽，不给 finalization 留残留供给。
type chainBridgeUsageCarrier struct {
	mu   sync.Mutex
	slot *chainBridgeUsageSlot
}

type chainBridgeUsageCarrierKey struct{}

// chainBridgeUsageCarrierInstall 在请求 context 上安装请求级载体（chain_v1
// 请求入口调用一次）。
func chainBridgeUsageCarrierInstall(r *http.Request) *http.Request {
	if r == nil {
		return r
	}
	return r.WithContext(context.WithValue(r.Context(), chainBridgeUsageCarrierKey{}, &chainBridgeUsageCarrier{}))
}

// chainBridgeUsageCarrierOf 读取请求载体；未安装（非 /v1 链入口的构造路径）
// 返回 nil，supplier 为 nil 时结算链保持既有 inspector/估算兜底路径。
func chainBridgeUsageCarrierOf(r *http.Request) *chainBridgeUsageCarrier {
	if r == nil {
		return nil
	}
	carrier, _ := r.Context().Value(chainBridgeUsageCarrierKey{}).(*chainBridgeUsageCarrier)
	return carrier
}

func (c *chainBridgeUsageCarrier) replaceSlot(slot *chainBridgeUsageSlot) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.slot = slot
}

// supplier 是 finalization 消费面（HandleUpstreamResponseInput.BridgeUsage）：
// 返回当前 attempt 槽上的桥接快照。
func (c *chainBridgeUsageCarrier) supplier() func() (gatewayproto.ParsedUsage, bool) {
	if c == nil {
		return nil
	}
	return func() (gatewayproto.ParsedUsage, bool) {
		c.mu.Lock()
		slot := c.slot
		c.mu.Unlock()
		return slot.loadUsage()
	}
}

// chainBridgeChatUsageMapToParsedUsage 把桥内 chat 形 usage（anthropicUsageToChatUsage
// 的输出键形，anthropic messages 上游 → chat 客户端方向的桥状态终值）解析为
// 结算 usage。解析复用 gatewayopenai.ExtractUsage（chat 口径与直连 chat 流的
// inspector 同源：prompt_tokens / completion_tokens / prompt_tokens_details）。
// nil usage = 上游未发 usage（UsageMissing 语义）：调用方（DD-RC-C1 的快照
// 闭包）先以 map 是否为 nil 判定，nil 时不调本函数、不写快照。
func chainBridgeChatUsageMapToParsedUsage(usage map[string]any) gatewayproto.ParsedUsage {
	if usage == nil {
		return gatewayproto.EmptyUsage()
	}
	return gatewayopenai.ExtractUsage(usage, "")
}

// chainBridgeGeminiUsageMapToParsedUsage 把桥内 gemini usageMetadata 形 usage
// （chatUsageToGeminiUsage 的输出键形，chat completions 上游 → gemini 客户端
// 方向的桥状态终值）解析为结算 usage。字段与口径同 gatewaygemini.ExtractUsage
// （InputTokens=promptTokenCount；OutputTokens=candidatesTokenCount+thoughtsTokenCount
// 的 defined 求和；CacheRead=cachedContentTokenCount；Thinking=thoughtsTokenCount）。
func chainBridgeGeminiUsageMapToParsedUsage(usage map[string]any) gatewayproto.ParsedUsage {
	if usage == nil {
		return gatewayproto.EmptyUsage()
	}
	intOf := func(key string) *int {
		if number, ok := usage[key].(float64); ok {
			converted := int(number)
			return &converted
		}
		return nil
	}
	candidateTokens := intOf("candidatesTokenCount")
	thinkingTokens := intOf("thoughtsTokenCount")
	var outputTokens *int
	switch {
	case candidateTokens != nil && thinkingTokens != nil:
		combined := *candidateTokens + *thinkingTokens
		outputTokens = &combined
	case candidateTokens != nil:
		outputTokens = candidateTokens
	case thinkingTokens != nil:
		outputTokens = thinkingTokens
	}
	return gatewayproto.ParsedUsage{
		InputTokens:     intOf("promptTokenCount"),
		OutputTokens:    outputTokens,
		CacheReadTokens: intOf("cachedContentTokenCount"),
		ThinkingTokens:  thinkingTokens,
	}
}
