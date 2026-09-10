package main

import (
	"io"
	"net/http"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayopenai"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/openaicompat"
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
	sourceFamily := openaicompat.NormalizeEndpointFamily(mapping.SourceEndpointFamily)
	upstreamFamily := openaicompat.NormalizeEndpointFamily(mapping.UpstreamEndpointFamily)
	if sourceFamily == upstreamFamily || !openaicompat.IsCrossProtocolBridgeRequired(sourceFamily, upstreamFamily) {
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
		if pump := t.bridgeStreamPump(input, mapping, sourceFamily, upstreamFamily, model); pump != nil {
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
	case upstreamFamily == openaicompat.FamilyChatCompletions:
		transformed = t.transformToChatClient(input, sourceFamily, body, model, stream)
	case upstreamFamily == openaicompat.FamilyAnthropicMessages:
		transformed = t.transformToAnthropicClient(input, sourceFamily, body, model, stream)
	case upstreamFamily == openaicompat.FamilyGeminiGenerateContent || upstreamFamily == openaicompat.FamilyGeminiStreamGenerate:
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

// bridgeStreamPump builds the incremental per-event pump of one streaming
// bridge direction; nil = the direction has no streaming variant and keeps the
// buffered transform. The per-event state machines and finish semantics mirror
// the buffered branches in transformToChatClient / transformToAnthropicClient /
// transformToGeminiNativeClient verbatim.
func (t *chainBridgeResponseTransformer) bridgeStreamPump(
	input gatewaydispatch.UpstreamResponseTransformInput,
	mapping *gatewayproto.ResolvedModelMapping,
	sourceFamily, upstreamFamily, model string,
) func(src io.Reader, dst io.Writer) error {
	switch upstreamFamily {
	case openaicompat.FamilyChatCompletions:
		switch sourceFamily {
		case openaicompat.FamilyAnthropicMessages:
			// transformAnthropicMessagesChatBridgeUpstreamResponse（流式）。
			state := openaicompat.NewAnthropicFromChatStreamState(model)
			return func(src io.Reader, dst io.Writer) error {
				return openaicompat.PumpBridgeSseTransform(src, dst, func(event string) []string {
					return openaicompat.ProcessChatCompletionsSseEventAsAnthropic(state, event)
				}, func() []string {
					if !state.Completed && !state.Failed {
						if state.StopReason != "" {
							return openaicompat.CompleteAnthropicFromChatStream(state)
						}
						return openaicompat.FailAnthropicFromChatStream(state, "上游 Chat Completions SSE 在 message_stop 前中断", "upstream_stream_interrupted")
					}
					return nil
				})
			}
		case openaicompat.FamilyGeminiGenerateContent, openaicompat.FamilyGeminiStreamGenerate:
			// transformGeminiGenerateContentChatBridgeUpstreamResponse（流式）。
			state := openaicompat.NewGeminiChatStreamState(model)
			return func(src io.Reader, dst io.Writer) error {
				return openaicompat.PumpBridgeSseTransform(src, dst, func(event string) []string {
					return openaicompat.ProcessChatCompletionsSseEventAsGemini(state, event)
				}, func() []string {
					if !state.Completed && !state.Failed {
						return openaicompat.CompleteGeminiChatStream(state)
					}
					return nil
				})
			}
		case openaicompat.FamilyResponses:
			// transformCodexResponsesChatBridgeUpstreamResponse（chat SSE ->
			// Responses SSE 回转）。
			options := openaicompat.CodexResponsesChatBridgeTransformOptions{
				Enabled:      true,
				DefaultModel: model,
				Model:        model,
				IDPrefix:     "openai_bridge",
			}
			if parsed := input.Req.ParsedJSONObjectBody(); parsed != nil {
				options.ToolAdaptersByChatName = openaicompat.CodexResponsesChatBridgeToolAdaptersFromClientBody(parsed)
				estimated := openaicompat.EstimateCodexResponsesRequestInputTokens(parsed)
				options.EstimatedInputTokens = &estimated
				options.PreviousResponseID = chainBridgePreviousResponseIDOf(parsed)
			}
			return func(src io.Reader, dst io.Writer) error {
				return openaicompat.PumpChatCompletionsSseToResponsesSse(src, dst, options)
			}
		}
	case openaicompat.FamilyAnthropicMessages:
		switch sourceFamily {
		case openaicompat.FamilyGeminiGenerateContent, openaicompat.FamilyGeminiStreamGenerate:
			// transformGeminiGenerateContentAnthropicMessagesBridgeUpstreamResponse（流式）。
			state := openaicompat.NewAnthropicGeminiStreamState(model)
			return func(src io.Reader, dst io.Writer) error {
				return openaicompat.PumpBridgeSseTransform(src, dst, func(event string) []string {
					return state.ProcessAnthropicSseEvent(event)
				}, func() []string {
					if !state.Completed && !state.Failed {
						return state.CompleteGeminiStream()
					}
					return nil
				})
			}
		case openaicompat.FamilyChatCompletions, openaicompat.FamilyResponses:
			// transformOpenAIToAnthropicBridgeUpstreamResponse（流式）。
			previousResponseID := ""
			if parsed := input.Req.ParsedJSONObjectBody(); parsed != nil {
				previousResponseID = chainBridgePreviousResponseIDOf(parsed)
			}
			if sourceFamily == openaicompat.FamilyResponses {
				return func(src io.Reader, dst io.Writer) error {
					return openaicompat.PumpAnthropicMessagesSseToResponsesSse(src, dst, model, previousResponseID)
				}
			}
			state := openaicompat.NewAnthropicChatStreamState(model)
			return func(src io.Reader, dst io.Writer) error {
				return openaicompat.PumpBridgeSseTransform(src, dst, func(event string) []string {
					return openaicompat.ProcessAnthropicEventAsChat(state, event)
				}, func() []string {
					if !state.Completed && !state.Failed {
						return openaicompat.FailAnthropicChatStream(state, "上游 Anthropic Messages SSE 在正常结束事件前中断", "upstream_stream_interrupted")
					}
					return nil
				})
			}
		}
	case openaicompat.FamilyGeminiGenerateContent, openaicompat.FamilyGeminiStreamGenerate:
		// transformGeminiNativeTargetBridgeUpstreamResponse（流式）。
		protocol := openaicompat.GeminiNativeDownstreamProtocolForMapping(mapping.SourceEndpointFamily)
		if protocol == openaicompat.GeminiNativeProtocolChatCompletions {
			state := openaicompat.NewGeminiNativeChatStreamState(model)
			return func(src io.Reader, dst io.Writer) error {
				return openaicompat.PumpBridgeSseTransform(src, dst, func(event string) []string {
					return openaicompat.ProcessGeminiSseEventAsChat(state, event)
				}, func() []string {
					if !state.Completed && !state.Failed && !state.TerminalReceived {
						return openaicompat.CompleteGeminiNativeChatStream(state)
					}
					return nil
				})
			}
		}
		return func(src io.Reader, dst io.Writer) error {
			return openaicompat.PumpGeminiSseToDownstreamSse(src, dst, protocol, model)
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
	case openaicompat.FamilyAnthropicMessages:
		// transformAnthropicMessagesChatBridgeUpstreamResponse.
		if stream {
			state := openaicompat.NewAnthropicFromChatStreamState(model)
			out := &strings.Builder{}
			_ = openaicompat.PumpBridgeSseTransform(strings.NewReader(string(body)), out, func(event string) []string {
				return openaicompat.ProcessChatCompletionsSseEventAsAnthropic(state, event)
			}, func() []string {
				if !state.Completed && !state.Failed {
					if state.StopReason != "" {
						return openaicompat.CompleteAnthropicFromChatStream(state)
					}
					return openaicompat.FailAnthropicFromChatStream(state, "上游 Chat Completions SSE 在 message_stop 前中断", "upstream_stream_interrupted")
				}
				return nil
			})
			return []byte(out.String())
		}
		parsed, err := openaicompat.ExtractJSONObject(string(body))
		if err != nil {
			return openaicompat.TransformAnthropicJSONToChatErrorBody()
		}
		return openaicompat.TransformAnthropicMessagesJSONToChatJSONBody(parsed, model)
	case openaicompat.FamilyGeminiGenerateContent, openaicompat.FamilyGeminiStreamGenerate:
		// transformGeminiGenerateContentChatBridgeUpstreamResponse.
		if stream {
			state := openaicompat.NewGeminiChatStreamState(model)
			out := &strings.Builder{}
			_ = openaicompat.PumpBridgeSseTransform(strings.NewReader(string(body)), out, func(event string) []string {
				return openaicompat.ProcessChatCompletionsSseEventAsGemini(state, event)
			}, func() []string {
				if !state.Completed && !state.Failed {
					return openaicompat.CompleteGeminiChatStream(state)
				}
				return nil
			})
			return []byte(out.String())
		}
		parsed, err := openaicompat.ExtractJSONObject(string(body))
		if err != nil {
			return []byte(bridgeChatUpstreamInvalidJSONErrorBody(model, "上游 Chat Completions 返回了无法解析为 Gemini GenerateContent 的响应。客户端可以保持当前对话并重试，或换用更稳定的上游模型；网关已转换为 Gemini 文本提示，避免客户端因协议错误中断。"))
		}
		return []byte(openaicompat.BridgeJSONStringifyOf(openaicompat.ChatCompletionJSONToGeminiGenerateContent(parsed, model)))
	case openaicompat.FamilyResponses:
		// transformCodexResponsesChatBridgeUpstreamResponse: chat SSE -> the
		// Responses protocol the codex client speaks (SSE 或单 JSON 响应）。
		options := openaicompat.CodexResponsesChatBridgeTransformOptions{
			Enabled:      true,
			DefaultModel: model,
			Model:        model,
			IDPrefix:     "openai_bridge",
		}
		if parsed := req.ParsedJSONObjectBody(); parsed != nil {
			options.ToolAdaptersByChatName = openaicompat.CodexResponsesChatBridgeToolAdaptersFromClientBody(parsed)
			estimated := openaicompat.EstimateCodexResponsesRequestInputTokens(parsed)
			options.EstimatedInputTokens = &estimated
			options.PreviousResponseID = chainBridgePreviousResponseIDOf(parsed)
		}
		if stream {
			return openaicompat.TransformChatCompletionsSseBufferToResponsesSse(body, options)
		}
		return openaicompat.TransformChatCompletionsSseBufferToResponsesJSON(body, options)
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
	case openaicompat.FamilyGeminiGenerateContent, openaicompat.FamilyGeminiStreamGenerate:
		// transformGeminiGenerateContentAnthropicMessagesBridgeUpstreamResponse.
		if stream {
			state := openaicompat.NewAnthropicGeminiStreamState(model)
			out := &strings.Builder{}
			_ = openaicompat.PumpBridgeSseTransform(strings.NewReader(string(body)), out, func(event string) []string {
				return state.ProcessAnthropicSseEvent(event)
			}, func() []string {
				if !state.Completed && !state.Failed {
					return state.CompleteGeminiStream()
				}
				return nil
			})
			return []byte(out.String())
		}
		parsed, err := openaicompat.ExtractJSONObject(string(body))
		if err != nil {
			return []byte(openaicompat.BridgeJSONStringifyOf(map[string]any{
				"error": map[string]any{
					"status": "INTERNAL",
					"code":   "upstream_anthropic_messages_invalid_json",
					"message": "上游 Anthropic Messages 返回了无法转换为 Gemini GenerateContent 响应的 JSON",
				},
			}))
		}
		return []byte(openaicompat.BridgeJSONStringifyOf(openaicompat.AnthropicMessageJSONToGeminiGenerateContent(parsed, model)))
	case openaicompat.FamilyChatCompletions, openaicompat.FamilyResponses:
		// transformOpenAIToAnthropicBridgeUpstreamResponse: the chat target
		// keeps the already-ported core; the responses target renders through
		// the responses surface.
		previousResponseID := ""
		if parsed := input.Req.ParsedJSONObjectBody(); parsed != nil {
			previousResponseID = chainBridgePreviousResponseIDOf(parsed)
		}
		if sourceFamily == openaicompat.FamilyResponses {
			if stream {
				return openaicompat.TransformAnthropicMessagesSseBufferToResponsesSse(body, model, previousResponseID)
			}
			return openaicompat.TransformAnthropicMessagesJSONBufferToResponsesJSON(body, model, previousResponseID)
		}
		if stream {
			state := openaicompat.NewAnthropicChatStreamState(model)
			out := &strings.Builder{}
			_ = openaicompat.PumpBridgeSseTransform(strings.NewReader(string(body)), out, func(event string) []string {
				return openaicompat.ProcessAnthropicEventAsChat(state, event)
			}, func() []string {
				if !state.Completed && !state.Failed {
					return openaicompat.FailAnthropicChatStream(state, "上游 Anthropic Messages SSE 在正常结束事件前中断", "upstream_stream_interrupted")
				}
				return nil
			})
			return []byte(out.String())
		}
		parsed, err := openaicompat.ExtractJSONObject(string(body))
		if err != nil {
			return openaicompat.TransformAnthropicJSONToChatErrorBody()
		}
		return openaicompat.TransformAnthropicMessagesJSONToChatJSONBody(parsed, model)
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
	protocol := openaicompat.GeminiNativeDownstreamProtocolForMapping(mapping.SourceEndpointFamily)
	if protocol == openaicompat.GeminiNativeProtocolChatCompletions {
		// The existing chat-direction core (bridge_response.go).
		if stream {
			state := openaicompat.NewGeminiNativeChatStreamState(model)
			out := &strings.Builder{}
			_ = openaicompat.PumpBridgeSseTransform(strings.NewReader(string(body)), out, func(event string) []string {
				return openaicompat.ProcessGeminiSseEventAsChat(state, event)
			}, func() []string {
				if !state.Completed && !state.Failed && !state.TerminalReceived {
					return openaicompat.CompleteGeminiNativeChatStream(state)
				}
				return nil
			})
			return []byte(out.String())
		}
		parsed, err := openaicompat.ExtractJSONObject(string(body))
		if err != nil {
			parsed = map[string]any{}
		}
		return []byte(openaicompat.BridgeJSONStringifyOf(openaicompat.TransformGeminiGenerateContentToOpenAIChatResponse(parsed, openaicompat.BridgeTransformResponseOptions{
			Enabled: true,
			Model:   model,
		})))
	}
	if stream {
		return openaicompat.TransformGeminiSseBufferToDownstreamSse(body, protocol, model)
	}
	return openaicompat.TransformGeminiJSONBufferToDownstreamJSON(body, protocol, model)
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
		return bridgeStreamPipedResponse(response, response.Header.Clone(), openaicompat.PumpUnwrapGeminiCodeAssistSse), nil
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}
	_ = response.Body.Close()
	transformed := body
	if stream {
		transformed = openaicompat.UnwrapGeminiCodeAssistSseBuffer(body)
	} else {
		transformed = openaicompat.CollectGeminiCodeAssistSseBuffer(body)
		if !strings.Contains(strings.ToLower(response.ContentType()), "json") {
			// The non-stream collection consumes the SSE stream into one JSON
			// payload (Node sets the JSON content type).
			headers := response.Header.Clone()
			headers.Set("Content-Type", "application/json; charset=utf-8")
			headers.Del("Content-Length")
			return gatewaydispatch.NewGatewayUpstreamResponseForTransform(response.Status(), headers, io.NopCloser(strings.NewReader(string(transformed)))), nil
		}
	}
	return gatewaydispatch.NewGatewayUpstreamResponseForTransform(response.Status(), response.Header.Clone(), io.NopCloser(strings.NewReader(string(transformed)))), nil
}

// geminiCodeAssistDownstreamStreamForMapping mirrors geminiCodeAssistDownstreamStream:
// native-target bridge mappings follow the client stream flag, otherwise the
// gemini streamGenerateContent family.
func geminiCodeAssistDownstreamStreamForMapping(req *gatewaypreauth.GatewayRequest, mapping *gatewayproto.ResolvedModelMapping) bool {
	if mapping != nil {
		source := openaicompat.NormalizeEndpointFamily(mapping.SourceEndpointFamily)
		if source == openaicompat.FamilyChatCompletions || source == openaicompat.FamilyResponses || source == openaicompat.FamilyAnthropicMessages {
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
		ModelMappings:             mappings,
		ProviderCode:              account.ProviderCode,
		ProviderProtocolProfileID: account.ProviderProtocolProfileID,
		ProtocolCode:              account.ProtocolCode,
		ProtocolVersion:           account.ProtocolVersion,
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
	return openaicompat.BridgeJSONStringifyOf(map[string]any{
		"candidates": []any{map[string]any{
			"content": map[string]any{
				"role": "model",
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
