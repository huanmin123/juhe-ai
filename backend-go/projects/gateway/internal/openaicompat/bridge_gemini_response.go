package openaicompat

import (
	"encoding/json"
	"strings"
	"time"
)

// timeNowUnixMilli mirrors Date.now() for the Node id suffixes.
func timeNowUnixMilli() int64 {
	return time.Now().UnixMilli()
}

// B-4 gemini-direction response cores (D-149/D-157). Ports of the archived
// Node response halves:
//
//	gemini-anthropic-messages-bridge.ts  transformGeminiGenerateContent-
//	                                     AnthropicMessagesBridgeUpstreamResponse
//	                                     (anthropic messages -> gemini client)
//	openai-anthropic-gemini-native-bridge.ts transformGeminiNativeTargetBridge
//	                                     UpstreamResponse (gemini upstream ->
//	                                     chat/responses/messages clients)
//	code-assist-runtime.ts               transformGeminiCodeAssistUpstreamResponse
//	                                     ({response:...} unwrap)

// ---------------------------------------------------------------------------
// Anthropic Messages -> Gemini GenerateContent (gemini-anthropic-messages-bridge.ts)
// ---------------------------------------------------------------------------

// AnthropicMessageJSONToGeminiGenerateContent mirrors
// anthropicMessageJsonToGeminiGenerateContent.
func AnthropicMessageJSONToGeminiGenerateContent(value map[string]any, fallbackModel string) map[string]any {
	root := value
	if root == nil {
		root = map[string]any{}
	}
	blocks, _ := bridgeIsArray(root["content"])
	parts := anthropicContentBlocksToGeminiParts(blocks)
	model := firstNonEmpty(bridgeStringValue(root["model"]), fallbackModel)
	if len(parts) == 0 {
		parts = []any{map[string]any{"text": ""}}
	}
	output := map[string]any{
		"candidates": []any{map[string]any{
			"content": map[string]any{
				"role": "model",
				"parts": parts,
			},
			"finishReason": anthropicStopReasonToGeminiFinishReason(bridgeStringValue(root["stop_reason"]), parts),
			"index":        float64(0),
		}},
		"modelVersion": model,
	}
	if usage := anthropicUsageToGeminiUsage(bridgeObjectValue(root["usage"]), nil); usage != nil {
		output["usageMetadata"] = usage
	}
	return output
}

func anthropicContentBlocksToGeminiParts(blocks []any) []any {
	output := []any{}
	for _, blockValue := range blocks {
		block := bridgeObjectValue(blockValue)
		if block == nil {
			continue
		}
		switch bridgeStringValue(block["type"]) {
		case "text":
			if text := bridgeStringValue(block["text"]); text != "" {
				output = append(output, map[string]any{"text": text})
			}
		case "tool_use":
			name := bridgeStringValue(block["name"])
			if name == "" {
				continue
			}
			input := block["input"]
			if !bridgeIsPlainObject(input) {
				input = map[string]any{}
			}
			output = append(output, map[string]any{
				"functionCall": map[string]any{
					"name": name,
					"args": input,
				},
			})
		}
	}
	return output
}

// anthropicStopReasonToGeminiFinishReason mirrors
// anthropicStopReasonToGeminiFinishReason.
func anthropicStopReasonToGeminiFinishReason(reason string, parts []any) string {
	switch reason {
	case "max_tokens":
		return "MAX_TOKENS"
	case "refusal":
		return "SAFETY"
	}
	if reason == "tool_use" {
		return "STOP"
	}
	for _, part := range parts {
		if partMap := bridgeObjectValue(part); partMap != nil && partMap["functionCall"] != nil {
			return "STOP"
		}
	}
	return "STOP"
}

// anthropicUsageToGeminiUsage mirrors anthropicUsageToGeminiUsage: message_start
// usage and message_delta usage merge through the previous snapshot.
func anthropicUsageToGeminiUsage(usage, previous map[string]any) map[string]any {
	if usage == nil && previous == nil {
		return nil
	}
	promptTokens := anthropicUsageInputTokensTotal(usage)
	if promptTokens == 0 {
		if value, ok := bridgeIntegerValue(previous["promptTokenCount"]); ok {
			promptTokens = value
		}
	}
	var completionTokens int64
	if value, ok := bridgeIntegerValue(usage["output_tokens"]); ok {
		completionTokens = value
	} else if value, ok := bridgeIntegerValue(previous["candidatesTokenCount"]); ok {
		completionTokens = value
	}
	output := map[string]any{
		"promptTokenCount":     float64(promptTokens),
		"candidatesTokenCount": float64(completionTokens),
		"totalTokenCount":      float64(promptTokens + completionTokens),
	}
	var cached *int64
	if usage != nil {
		if value, ok := bridgeIntegerValue(usage["cache_read_input_tokens"]); ok {
			cached = &value
		}
	}
	if cached == nil {
		if value, ok := bridgeIntegerValue(previous["cachedContentTokenCount"]); ok {
			cached = &value
		}
	}
	if cached != nil {
		output["cachedContentTokenCount"] = float64(*cached)
	}
	thinkingTokens := int64(0)
	hasThinking := false
	if usage != nil {
		if details := bridgeObjectValue(usage["output_tokens_details"]); details != nil {
			if value, ok := bridgeIntegerValue(details["thinking_tokens"]); ok {
				thinkingTokens, hasThinking = value, true
			}
		}
		if !hasThinking {
			if value, ok := bridgeIntegerValue(usage["thinking_tokens"]); ok {
				thinkingTokens, hasThinking = value, true
			}
		}
	}
	if hasThinking {
		output["thoughtsTokenCount"] = float64(thinkingTokens)
	}
	return output
}

func anthropicUsageInputTokensTotal(usage map[string]any) int64 {
	if usage == nil {
		return 0
	}
	total := int64(0)
	if value, ok := bridgeIntegerValue(usage["input_tokens"]); ok {
		total += value
	}
	if value, ok := bridgeIntegerValue(usage["cache_creation_input_tokens"]); ok {
		total += value
	}
	if value, ok := bridgeIntegerValue(usage["cache_read_input_tokens"]); ok {
		total += value
	}
	return total
}

// AnthropicGeminiStreamState mirrors AnthropicGeminiStreamState.
type AnthropicGeminiStreamState struct {
	Model        string
	Completed    bool
	Failed       bool
	FinishReason string
	Usage        map[string]any
	blocks       map[int64]*anthropicGeminiStreamBlockState
}

type anthropicGeminiStreamBlockState struct {
	index     int64
	blockType string
	id        string
	name      string
	inputJSON string
}

// NewAnthropicGeminiStreamState mirrors createAnthropicGeminiStreamState.
func NewAnthropicGeminiStreamState(model string) *AnthropicGeminiStreamState {
	return &AnthropicGeminiStreamState{Model: model, blocks: map[int64]*anthropicGeminiStreamBlockState{}}
}

// ProcessAnthropicSseEventAsGemini mirrors processAnthropicSseEventAsGemini:
// one Anthropic Messages SSE event in, zero or more Gemini SSE events out.
func (s *AnthropicGeminiStreamState) ProcessAnthropicSseEvent(rawEventText string) []string {
	if s.Completed || s.Failed {
		return nil
	}
	event := ParseBridgeSseEvent(rawEventText)
	if event.DataParseError {
		return s.failGeminiStream("上游 Anthropic Messages SSE 返回了无法解析的事件", "upstream_stream_parse_error")
	}
	data := event.Data
	if data == nil {
		return nil
	}
	if event.EventName == "error" || event.EventType == "error" || bridgeStringValue(data["type"]) == "error" {
		errorObject := bridgeObjectValue(data["error"])
		if errorObject == nil {
			errorObject = data
		}
		message := firstNonEmpty(bridgeStringValue(errorObject["message"]), "上游 Anthropic Messages SSE 返回错误事件")
		code := firstNonEmpty(bridgeStringValue(errorObject["code"]), bridgeStringValue(errorObject["type"]), "upstream_error")
		status := firstNonEmpty(bridgeStringValue(errorObject["type"]), "INTERNAL")
		return s.failGeminiStreamWithStatus(message, code, status)
	}
	switch bridgeStringValue(data["type"]) {
	case "message_start":
		message := bridgeObjectValue(data["message"])
		if message != nil {
			if model := bridgeStringValue(message["model"]); model != "" {
				s.Model = model
			}
			if usage := anthropicUsageToGeminiUsage(bridgeObjectValue(message["usage"]), nil); usage != nil {
				s.Usage = usage
			}
		}
		return nil
	case "content_block_start":
		index, _ := bridgeIntegerValue(data["index"])
		block := bridgeObjectValue(data["content_block"])
		state := &anthropicGeminiStreamBlockState{blockType: "unknown"}
		if block != nil {
			state.blockType = firstNonEmpty(bridgeStringValue(block["type"]), "unknown")
			state.id = bridgeStringValue(block["id"])
			state.name = bridgeStringValue(block["name"])
		}
		state.index = index
		s.blocks[index] = state
		return nil
	case "content_block_delta":
		index, _ := bridgeIntegerValue(data["index"])
		block := s.blocks[index]
		delta := bridgeObjectValue(data["delta"])
		if delta == nil {
			delta = map[string]any{}
		}
		if bridgeStringValue(delta["type"]) == "text_delta" {
			text := bridgeStringValue(delta["text"])
			if text == "" {
				return nil
			}
			return []string{geminiBridgeSse(map[string]any{
				"candidates": []any{map[string]any{
					"content": map[string]any{
						"role":  "model",
						"parts": []any{map[string]any{"text": text}},
					},
				}},
				"modelVersion": s.Model,
			})}
		}
		if bridgeStringValue(delta["type"]) == "input_json_delta" {
			if block != nil {
				block.inputJSON += bridgeStringValue(delta["partial_json"])
			}
			return nil
		}
		return nil
	case "content_block_stop":
		index, _ := bridgeIntegerValue(data["index"])
		block := s.blocks[index]
		if block == nil || block.blockType != "tool_use" || block.name == "" {
			return nil
		}
		return []string{geminiBridgeSse(map[string]any{
			"candidates": []any{map[string]any{
				"content": map[string]any{
					"role": "model",
					"parts": []any{map[string]any{
						"functionCall": map[string]any{
							"name": block.name,
							"args": bridgeParseToolArguments(block.inputJSON),
						},
					}},
				},
			}},
			"modelVersion": s.Model,
		})}
	case "message_delta":
		delta := bridgeObjectValue(data["delta"])
		if delta != nil {
			if stopReason := bridgeStringValue(delta["stop_reason"]); stopReason != "" {
				s.FinishReason = anthropicStopReasonToGeminiFinishReason(stopReason, nil)
			}
		}
		if usage := anthropicUsageToGeminiUsage(bridgeObjectValue(data["usage"]), s.Usage); usage != nil {
			s.Usage = usage
		}
		return nil
	case "message_stop":
		return s.CompleteGeminiStream()
	}
	return nil
}

// CompleteGeminiStream mirrors completeGeminiStream (the final candidate
// summary event).
func (s *AnthropicGeminiStreamState) CompleteGeminiStream() []string {
	if s.Completed || s.Failed {
		return nil
	}
	s.Completed = true
	finalEvent := map[string]any{
		"candidates": []any{map[string]any{
			"finishReason": firstNonEmpty(s.FinishReason, "STOP"),
		}},
		"modelVersion": s.Model,
	}
	if s.Usage != nil {
		finalEvent["usageMetadata"] = s.Usage
	}
	return []string{geminiBridgeSse(finalEvent)}
}

func (s *AnthropicGeminiStreamState) failGeminiStream(message, code string) []string {
	return s.failGeminiStreamWithStatus(message, code, "INTERNAL")
}

func (s *AnthropicGeminiStreamState) failGeminiStreamWithStatus(message, code, status string) []string {
	if s.Completed || s.Failed {
		return nil
	}
	s.Failed = true
	return []string{BridgeSseEventText("error", map[string]any{
		"error": map[string]any{
			"message": message,
			"status":  status,
			"code":    code,
		},
	})}
}

// ---------------------------------------------------------------------------
// Gemini upstream -> chat / responses / messages clients
// (openai-anthropic-gemini-native-bridge.ts)
// ---------------------------------------------------------------------------

// GeminiResponseSummary mirrors GeminiResponseSummary.
type GeminiResponseSummary struct {
	Text         string
	FunctionCalls []map[string]any
	FinishReason string
	Usage        map[string]any
}

// SummarizeGeminiResponse mirrors summarizeGeminiResponse.
func SummarizeGeminiResponse(data map[string]any) GeminiResponseSummary {
	summary := GeminiResponseSummary{}
	candidates, _ := bridgeIsArray(data["candidates"])
	for _, candidateValue := range candidates {
		candidate := bridgeObjectValue(candidateValue)
		if candidate == nil {
			continue
		}
		if summary.FinishReason == "" {
			summary.FinishReason = bridgeStringValue(candidate["finishReason"])
		}
		content := bridgeObjectValue(candidate["content"])
		if content == nil {
			continue
		}
		parts, _ := bridgeIsArray(content["parts"])
		for _, partValue := range parts {
			part := bridgeObjectValue(partValue)
			if part == nil {
				continue
			}
			if text, ok := part["text"].(string); ok && text != "" {
				summary.Text += text
			}
			if call := bridgeObjectValue(part["functionCall"]); call != nil {
				name := bridgeStringValue(call["name"])
				if name == "" {
					continue
				}
				args := call["args"]
				if !bridgeIsPlainObject(args) {
					args = map[string]any{}
				}
				summary.FunctionCalls = append(summary.FunctionCalls, map[string]any{
					"id":   "call_" + int64ToText(int64(len(summary.FunctionCalls))),
					"name": name,
					"args": args,
				})
			}
		}
	}
	if usage := bridgeObjectValue(data["usageMetadata"]); usage != nil {
		summary.Usage = usage
	}
	return summary
}

// GeminiNativeDownstreamProtocol mirrors downstreamProtocol(mapping).
type GeminiNativeDownstreamProtocol string

const (
	GeminiNativeProtocolChatCompletions GeminiNativeDownstreamProtocol = "chat_completions"
	GeminiNativeProtocolResponses       GeminiNativeDownstreamProtocol = "responses"
	GeminiNativeProtocolMessages        GeminiNativeDownstreamProtocol = "messages"
)

// GeminiNativeDownstreamProtocolForMapping mirrors downstreamProtocol(mapping):
// the source endpoint family selects the downstream render protocol.
func GeminiNativeDownstreamProtocolForMapping(sourceEndpointFamily string) GeminiNativeDownstreamProtocol {
	switch NormalizeEndpointFamily(sourceEndpointFamily) {
	case FamilyResponses:
		return GeminiNativeProtocolResponses
	case FamilyAnthropicMessages:
		return GeminiNativeProtocolMessages
	default:
		return GeminiNativeProtocolChatCompletions
	}
}

// RenderGeminiResponseJSON mirrors renderJsonResponse.
func RenderGeminiResponseJSON(protocol GeminiNativeDownstreamProtocol, model string, summary GeminiResponseSummary) map[string]any {
	switch protocol {
	case GeminiNativeProtocolResponses:
		return renderGeminiResponsesJSON(model, summary)
	case GeminiNativeProtocolMessages:
		return renderGeminiAnthropicJSON(model, summary)
	default:
		return renderGeminiChatJSON(model, summary)
	}
}

func renderGeminiChatJSON(model string, summary GeminiResponseSummary) map[string]any {
	message := map[string]any{"role": "assistant"}
	if len(summary.FunctionCalls) > 0 {
		message["content"] = nil
	} else {
		message["content"] = summary.Text
	}
	if len(summary.FunctionCalls) > 0 {
		toolCalls := []any{}
		for index, call := range summary.FunctionCalls {
			toolCalls = append(toolCalls, map[string]any{
				"id":     call["id"],
				"type":   "function",
				"function": map[string]any{
					"name":      call["name"],
					"arguments": bridgeJSONStringify(call["args"]),
				},
				"index": float64(index),
			})
		}
		message["tool_calls"] = toolCalls
	}
	return map[string]any{
		"id":      "chatcmpl_" + int64ToBase36(timeNowUnixMilli()),
		"object":  "chat.completion",
		"created": float64(bridgeNowUnix()),
		"model":   model,
		"choices": []any{map[string]any{
			"index":         float64(0),
			"message":       message,
			"finish_reason": geminiChatFinishReason(summary),
		}},
		"usage": geminiOpenAIUsage(summary.Usage),
	}
}

func renderGeminiResponsesJSON(model string, summary GeminiResponseSummary) map[string]any {
	output := []any{}
	if summary.Text != "" {
		output = append(output, map[string]any{
			"id":     "msg_" + int64ToBase36(timeNowUnixMilli()),
			"type":   "message",
			"status": "completed",
			"role":   "assistant",
			"content": []any{map[string]any{
				"type":        "output_text",
				"text":        summary.Text,
				"annotations": []any{},
			}},
		})
	}
	for _, call := range summary.FunctionCalls {
		output = append(output, map[string]any{
			"id":        call["id"],
			"type":      "function_call",
			"call_id":   call["id"],
			"name":      call["name"],
			"arguments": bridgeJSONStringify(call["args"]),
			"status":    "completed",
		})
	}
	return map[string]any{
		"id":         "resp_" + int64ToBase36(timeNowUnixMilli()),
		"object":     "response",
		"created_at": float64(bridgeNowUnix()),
		"status":     "completed",
		"model":      model,
		"output":     output,
		"usage":      geminiResponsesUsage(summary.Usage),
	}
}

func renderGeminiAnthropicJSON(model string, summary GeminiResponseSummary) map[string]any {
	content := []any{}
	if summary.Text != "" {
		content = append(content, map[string]any{"type": "text", "text": summary.Text})
	}
	for _, call := range summary.FunctionCalls {
		content = append(content, map[string]any{
			"type":  "tool_use",
			"id":    call["id"],
			"name":  call["name"],
			"input": call["args"],
		})
	}
	stopReason := "end_turn"
	if len(summary.FunctionCalls) > 0 {
		stopReason = "tool_use"
	} else {
		stopReason = geminiAnthropicStopReason(summary.FinishReason)
	}
	return map[string]any{
		"id":            "msg_" + int64ToBase36(timeNowUnixMilli()),
		"type":          "message",
		"role":          "assistant",
		"model":         model,
		"content":       content,
		"stop_reason":   stopReason,
		"stop_sequence": nil,
		"usage":         geminiAnthropicUsage(summary.Usage),
	}
}

func geminiChatFinishReason(summary GeminiResponseSummary) string {
	if len(summary.FunctionCalls) > 0 {
		return "tool_calls"
	}
	switch strings.ToUpper(summary.FinishReason) {
	case "MAX_TOKENS":
		return "length"
	case "SAFETY", "RECITATION":
		return "content_filter"
	}
	return "stop"
}

func geminiAnthropicStopReason(reason string) string {
	switch strings.ToUpper(reason) {
	case "MAX_TOKENS":
		return "max_tokens"
	case "SAFETY", "RECITATION":
		return "stop_sequence"
	}
	return "end_turn"
}

func geminiOpenAIUsage(usage map[string]any) map[string]any {
	prompt := numberAsFloat64(usage["promptTokenCount"])
	completion := numberAsFloat64(usage["candidatesTokenCount"])
	return map[string]any{
		"prompt_tokens":     prompt,
		"completion_tokens": completion,
		"total_tokens":      numberAsFloat64(usage["totalTokenCount"]),
	}
}

// geminiResponsesUsage mirrors responsesUsage: the responses protocol names the
// usage fields input_tokens / output_tokens / total_tokens (the chat protocol
// keeps prompt_tokens / completion_tokens).
func geminiResponsesUsage(usage map[string]any) map[string]any {
	return map[string]any{
		"input_tokens":  numberAsFloat64(usage["promptTokenCount"]),
		"output_tokens": numberAsFloat64(usage["candidatesTokenCount"]),
		"total_tokens":  numberAsFloat64(usage["totalTokenCount"]),
	}
}

func geminiAnthropicUsage(usage map[string]any) map[string]any {
	return map[string]any{
		"input_tokens":  numberAsFloat64(usage["promptTokenCount"]),
		"output_tokens": numberAsFloat64(usage["candidatesTokenCount"]),
	}
}

// GeminiNativeSseRenderState mirrors SseRenderState.
type GeminiNativeSseRenderState struct {
	Protocol    GeminiNativeDownstreamProtocol
	Model       string
	Started     bool
	TextStarted bool
	Completed   bool
	ContentIndex int64
}

// NewGeminiNativeSseRenderState mirrors createSseRenderState.
func NewGeminiNativeSseRenderState(protocol GeminiNativeDownstreamProtocol, model string) *GeminiNativeSseRenderState {
	return &GeminiNativeSseRenderState{Protocol: protocol, Model: model}
}

// RenderSseSummary mirrors renderSseSummary.
func (s *GeminiNativeSseRenderState) RenderSseSummary(summary GeminiResponseSummary) []string {
	switch s.Protocol {
	case GeminiNativeProtocolResponses:
		return s.renderResponsesSummary(summary)
	case GeminiNativeProtocolMessages:
		return s.renderAnthropicSummary(summary)
	default:
		return s.renderChatSummary(summary)
	}
}

func (s *GeminiNativeSseRenderState) renderChatSummary(summary GeminiResponseSummary) []string {
	output := []string{}
	if !s.Started {
		output = append(output, geminiBridgeChatSse(map[string]any{
			"id": "chatcmpl_" + int64ToBase36(timeNowUnixMilli()), "object": "chat.completion.chunk", "created": float64(bridgeNowUnix()), "model": s.Model,
			"choices": []any{map[string]any{"index": float64(0), "delta": map[string]any{"role": "assistant"}, "finish_reason": nil}},
		}))
		s.Started = true
	}
	if summary.Text != "" {
		output = append(output, geminiBridgeChatSse(map[string]any{
			"id": "chatcmpl_" + int64ToBase36(timeNowUnixMilli()), "object": "chat.completion.chunk", "created": float64(bridgeNowUnix()), "model": s.Model,
			"choices": []any{map[string]any{"index": float64(0), "delta": map[string]any{"content": summary.Text}, "finish_reason": nil}},
		}))
	}
	for _, call := range summary.FunctionCalls {
		output = append(output, geminiBridgeChatSse(map[string]any{
			"id": "chatcmpl_" + int64ToBase36(timeNowUnixMilli()), "object": "chat.completion.chunk", "created": float64(bridgeNowUnix()), "model": s.Model,
			"choices": []any{map[string]any{
				"index": float64(0),
				"delta": map[string]any{
					"tool_calls": []any{map[string]any{
						"index": float64(s.ContentIndex), "id": call["id"], "type": "function",
						"function": map[string]any{"name": call["name"], "arguments": bridgeJSONStringify(call["args"])},
					}},
				},
				"finish_reason": nil,
			}},
		}))
		s.ContentIndex++
	}
	if summary.FinishReason != "" {
		output = append(output, geminiBridgeChatSse(map[string]any{
			"id": "chatcmpl_" + int64ToBase36(timeNowUnixMilli()), "object": "chat.completion.chunk", "created": float64(bridgeNowUnix()), "model": s.Model,
			"choices": []any{map[string]any{"index": float64(0), "delta": map[string]any{}, "finish_reason": geminiChatFinishReason(summary)}},
		}))
		s.Completed = true
	}
	return output
}

func (s *GeminiNativeSseRenderState) renderResponsesSummary(summary GeminiResponseSummary) []string {
	output := []string{}
	if !s.Started {
		output = append(output,
			BridgeSseEventText("response.created", map[string]any{
				"type": "response.created",
				"response": map[string]any{
					"id": "resp_" + int64ToBase36(timeNowUnixMilli()), "object": "response", "status": "in_progress", "model": s.Model, "output": []any{},
				},
			}),
			BridgeSseEventText("response.output_item.added", map[string]any{
				"type": "response.output_item.added", "output_index": float64(0),
				"item": map[string]any{"id": "msg_0", "type": "message", "role": "assistant", "content": []any{}},
			}),
			BridgeSseEventText("response.content_part.added", map[string]any{
				"type": "response.content_part.added", "item_id": "msg_0", "output_index": float64(0), "content_index": float64(0),
				"part": map[string]any{"type": "output_text", "text": ""},
			}),
		)
		s.Started = true
		s.TextStarted = true
	}
	if summary.Text != "" {
		output = append(output, BridgeSseEventText("response.output_text.delta", map[string]any{
			"type": "response.output_text.delta", "item_id": "msg_0", "output_index": float64(0), "content_index": float64(0), "delta": summary.Text,
		}))
	}
	for _, call := range summary.FunctionCalls {
		s.ContentIndex++
		output = append(output, BridgeSseEventText("response.output_item.added", map[string]any{
			"type": "response.output_item.added", "output_index": float64(s.ContentIndex),
			"item": map[string]any{
				"id": call["id"], "type": "function_call", "call_id": call["id"], "name": call["name"],
				"arguments": bridgeJSONStringify(call["args"]), "status": "completed",
			},
		}))
	}
	if summary.FinishReason != "" {
		s.Completed = true
	}
	return output
}

func (s *GeminiNativeSseRenderState) renderAnthropicSummary(summary GeminiResponseSummary) []string {
	output := []string{}
	if !s.Started {
		output = append(output, BridgeSseEventText("message_start", map[string]any{
			"type": "message_start",
			"message": map[string]any{
				"id": "msg_" + int64ToBase36(timeNowUnixMilli()), "type": "message", "role": "assistant", "model": s.Model,
				"content": []any{}, "stop_reason": nil, "stop_sequence": nil,
				"usage": map[string]any{"input_tokens": float64(0), "output_tokens": float64(0)},
			},
		}))
		s.Started = true
	}
	if summary.Text != "" {
		if !s.TextStarted {
			output = append(output, BridgeSseEventText("content_block_start", map[string]any{
				"type": "content_block_start", "index": float64(0),
				"content_block": map[string]any{"type": "text", "text": ""},
			}))
			s.TextStarted = true
		}
		output = append(output, BridgeSseEventText("content_block_delta", map[string]any{
			"type": "content_block_delta", "index": float64(0),
			"delta": map[string]any{"type": "text_delta", "text": summary.Text},
		}))
	}
	for _, call := range summary.FunctionCalls {
		index := s.ContentIndex + 1
		s.ContentIndex = index
		output = append(output,
			BridgeSseEventText("content_block_start", map[string]any{
				"type": "content_block_start", "index": float64(index),
				"content_block": map[string]any{"type": "tool_use", "id": call["id"], "name": call["name"], "input": map[string]any{}},
			}),
			BridgeSseEventText("content_block_delta", map[string]any{
				"type": "content_block_delta", "index": float64(index),
				"delta": map[string]any{"type": "input_json_delta", "partial_json": bridgeJSONStringify(call["args"])},
			}),
			BridgeSseEventText("content_block_stop", map[string]any{
				"type": "content_block_stop", "index": float64(index),
			}),
		)
	}
	if summary.FinishReason != "" {
		s.Completed = true
	}
	return output
}

// RenderSseDone mirrors renderSseDone.
func (s *GeminiNativeSseRenderState) RenderSseDone() string {
	switch s.Protocol {
	case GeminiNativeProtocolResponses:
		return strings.Join([]string{
			BridgeSseEventText("response.content_part.done", map[string]any{
				"type": "response.content_part.done", "item_id": "msg_0", "output_index": float64(0), "content_index": float64(0),
				"part": map[string]any{"type": "output_text", "text": ""},
			}),
			BridgeSseEventText("response.output_item.done", map[string]any{
				"type": "response.output_item.done", "output_index": float64(0),
				"item": map[string]any{"id": "msg_0", "type": "message", "role": "assistant", "content": []any{}},
			}),
			BridgeSseEventText("response.completed", map[string]any{
				"type": "response.completed",
				"response": map[string]any{
					"id": "resp_" + int64ToBase36(timeNowUnixMilli()), "object": "response", "status": "completed", "model": s.Model,
				},
			}),
		}, "")
	case GeminiNativeProtocolMessages:
		output := []string{}
		if s.TextStarted {
			output = append(output, BridgeSseEventText("content_block_stop", map[string]any{
				"type": "content_block_stop", "index": float64(0),
			}))
		}
		output = append(output,
			BridgeSseEventText("message_delta", map[string]any{
				"type": "message_delta",
				"delta": map[string]any{"stop_reason": "end_turn", "stop_sequence": nil},
				"usage": map[string]any{"output_tokens": float64(0)},
			}),
			BridgeSseEventText("message_stop", map[string]any{"type": "message_stop"}),
		)
		return strings.Join(output, "")
	default:
		done := ""
		if !s.Completed {
			done = geminiBridgeChatSse(map[string]any{
				"id": "chatcmpl_" + int64ToBase36(timeNowUnixMilli()), "object": "chat.completion.chunk", "created": float64(bridgeNowUnix()), "model": s.Model,
				"choices": []any{map[string]any{"index": float64(0), "delta": map[string]any{}, "finish_reason": "stop"}},
			})
		}
		return done + "data: [DONE]\n\n"
	}
}

func geminiBridgeChatSse(payload any) string {
	return BridgeSseData(payload)
}

func geminiBridgeSse(payload any) string {
	return BridgeSseData(payload)
}

// TransformGeminiJSONBufferToDownstreamJSON mirrors transformGeminiJsonToDownstreamJson
// for one buffered Gemini GenerateContent body.
func TransformGeminiJSONBufferToDownstreamJSON(body []byte, protocol GeminiNativeDownstreamProtocol, model string) []byte {
	var data any
	if err := json.Unmarshal(body, &data); err != nil {
		data = map[string]any{}
	}
	root, _ := data.(map[string]any)
	if root == nil {
		root = map[string]any{}
	}
	return []byte(bridgeJSONStringify(RenderGeminiResponseJSON(protocol, model, SummarizeGeminiResponse(root))))
}

// TransformGeminiSseBufferToDownstreamSse mirrors transformGeminiSseToDownstreamSse
// for one buffered Gemini streamGenerateContent SSE body.
func TransformGeminiSseBufferToDownstreamSse(body []byte, protocol GeminiNativeDownstreamProtocol, model string) []byte {
	state := NewGeminiNativeSseRenderState(protocol, model)
	output := strings.Builder{}
	for _, eventText := range codexBridgeSplitCompleteSseEvents(string(body)) {
		event := ParseBridgeSseEvent(eventText)
		if event.DataText == "" || event.DataText == "[DONE]" {
			continue
		}
		if event.Data == nil {
			continue
		}
		for _, rendered := range state.RenderSseSummary(SummarizeGeminiResponse(event.Data)) {
			output.WriteString(rendered)
		}
	}
	output.WriteString(state.RenderSseDone())
	return []byte(output.String())
}

// ---------------------------------------------------------------------------
// Gemini Code Assist {response:...} unwrap (code-assist-runtime.ts)
// ---------------------------------------------------------------------------

// UnwrapGeminiCodeAssistPayload mirrors unwrapGeminiCodeAssistPayload: parse
// the SSE data payload and, when it is the Code Assist {response:...} wrapper,
// re-serialize the inner response; otherwise return the payload unchanged.
func UnwrapGeminiCodeAssistPayload(payload string) string {
	var parsed any
	if err := json.Unmarshal([]byte(payload), &parsed); err != nil {
		return payload
	}
	record, ok := parsed.(map[string]any)
	if !ok {
		return payload
	}
	response, has := record["response"]
	if !has {
		return payload
	}
	switch response.(type) {
	case map[string]any, []any:
		return bridgeJSONStringify(response)
	}
	return payload
}

// UnwrapGeminiCodeAssistSseBuffer mirrors unwrapGeminiCodeAssistSse: every SSE
// data payload is unwrapped in place, the event framing kept.
func UnwrapGeminiCodeAssistSseBuffer(body []byte) []byte {
	output := strings.Builder{}
	for _, eventText := range codexBridgeSplitCompleteSseEvents(string(body)) {
		payload := geminiCodeAssistSseDataPayload(eventText)
		if payload == "" || payload == "[DONE]" {
			output.WriteString(strings.TrimRight(eventText, "\n") + "\n\n")
			continue
		}
		output.WriteString("data: " + UnwrapGeminiCodeAssistPayload(payload) + "\n\n")
	}
	return []byte(output.String())
}

// CollectGeminiCodeAssistSseBuffer mirrors collectGeminiCodeAssistSse:
// non-stream consumption — unwrap every event, keep the last response with
// parts and merge the collected text into its first text part.
func CollectGeminiCodeAssistSseBuffer(body []byte) []byte {
	var last map[string]any
	var lastWithParts map[string]any
	collectedTextParts := []string{}
	for _, eventText := range codexBridgeSplitCompleteSseEvents(string(body)) {
		payload := geminiCodeAssistSseDataPayload(eventText)
		if payload == "" || payload == "[DONE]" {
			continue
		}
		unwrappedPayload := UnwrapGeminiCodeAssistPayload(payload)
		var unwrapped any
		if err := json.Unmarshal([]byte(unwrappedPayload), &unwrapped); err != nil {
			continue
		}
		record, ok := unwrapped.(map[string]any)
		if !ok {
			continue
		}
		last = record
		parts := geminiCodeAssistExtractParts(record)
		if len(parts) == 0 {
			continue
		}
		lastWithParts = record
		for _, part := range parts {
			if text, ok := part["text"].(string); ok && text != "" {
				collectedTextParts = append(collectedTextParts, text)
			}
		}
	}
	response := lastWithParts
	if response == nil {
		response = last
	}
	if response == nil {
		response = map[string]any{}
	}
	return []byte(bridgeJSONStringify(geminiCodeAssistMergeCollectedTextParts(response, collectedTextParts)))
}

// geminiCodeAssistSseDataPayload mirrors sseDataPayload: the joined data line
// text of one SSE event.
func geminiCodeAssistSseDataPayload(eventText string) string {
	var dataLines []string
	for _, line := range strings.Split(strings.ReplaceAll(eventText, "\r\n", "\n"), "\n") {
		switch {
		case line == "data":
			dataLines = append(dataLines, "")
		case strings.HasPrefix(line, "data:"):
			dataLines = append(dataLines, strings.TrimLeft(line[len("data:"):], " "))
		}
	}
	if len(dataLines) == 0 {
		return ""
	}
	return strings.TrimSpace(strings.Join(dataLines, "\n"))
}

func geminiCodeAssistExtractParts(response map[string]any) []map[string]any {
	candidates, _ := bridgeIsArray(response["candidates"])
	if len(candidates) == 0 {
		return nil
	}
	firstCandidate := bridgeObjectValue(candidates[0])
	if firstCandidate == nil {
		return nil
	}
	content := bridgeObjectValue(firstCandidate["content"])
	if content == nil {
		return nil
	}
	parts, _ := bridgeIsArray(content["parts"])
	output := []map[string]any{}
	for _, part := range parts {
		if partMap := bridgeObjectValue(part); partMap != nil {
			output = append(output, partMap)
		}
	}
	return output
}

// geminiCodeAssistMergeCollectedTextParts mirrors mergeCollectedTextParts:
// the collected text replaces the first text part (or is prepended when the
// response carries no text part).
func geminiCodeAssistMergeCollectedTextParts(response map[string]any, textParts []string) map[string]any {
	if len(textParts) == 0 {
		return response
	}
	mergedText := strings.Join(textParts, "")
	result := map[string]any{}
	for key, value := range response {
		result[key] = bridgeCloneJSON(value)
	}
	var candidates []any
	if existing, ok := bridgeIsArray(result["candidates"]); ok && len(existing) > 0 {
		candidates = existing
	} else {
		candidates = []any{map[string]any{}}
	}
	candidate, _ := candidates[0].(map[string]any)
	if candidate == nil {
		candidate = map[string]any{}
	}
	candidates[0] = candidate
	content, ok := candidate["content"].(map[string]any)
	if !ok {
		content = map[string]any{"role": "model"}
	}
	candidate["content"] = content
	existingParts, _ := bridgeIsArray(content["parts"])
	newParts := []any{}
	textUpdated := false
	for _, part := range existingParts {
		partMap := bridgeObjectValue(part)
		if partMap != nil {
			if _, hasText := partMap["text"]; hasText && !textUpdated {
				next := map[string]any{}
				for key, value := range partMap {
					next[key] = value
				}
				next["text"] = mergedText
				newParts = append(newParts, next)
				textUpdated = true
				continue
			}
		}
		newParts = append(newParts, part)
	}
	if !textUpdated {
		newParts = append([]any{map[string]any{"text": mergedText}}, newParts...)
	}
	content["parts"] = newParts
	result["candidates"] = candidates
	return result
}
