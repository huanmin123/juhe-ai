package openaicompat

import (
	"encoding/json"
	"math/rand"
	"net/http"
	"strings"
	"time"
)

// B-4 bridge response transforms (D-149/D-157). Ports of the archived Node
// response cores:
//
//	openai-anthropic-bridge.ts        anthropicMessageToChatCompletion,
//	                                  transformAnthropicMessagesSseToChatSse
//	                                  (processAnthropicEventAsChat)
//	anthropic-openai-chat-bridge.ts   chatCompletionJsonToAnthropicMessage,
//	                                  processChatCompletionsSseEvent (chat ->
//	                                  anthropic messages SSE)
//	gemini-openai-chat-bridge.ts      chatCompletionJsonToGeminiGenerateContent,
//	                                  processChatCompletionsSseEvent (chat ->
//	                                  gemini SSE)
//	openai-anthropic-gemini-native    gemini generateContent -> chat completion
//	                                  (JSON + SSE) for chat clients on gemini
//	                                  upstreams

// BridgeTransformResponseOptions provides options for response transformation.
type BridgeTransformResponseOptions struct {
	Enabled                bool
	Model                  string
	FileResolver           FileResolver
	FileSearchExecutor     FileSearchExecutor
	SourceEndpointFamily   string
	UpstreamEndpointFamily string
}

// TransformedResponse represents a transformed bridge response.
type TransformedResponse struct {
	Status       int
	Headers      http.Header
	Body         []byte
	Stream       bool
	ResponseMode string
}

// ---------------------------------------------------------------------------
// shared id / usage helpers (openai-anthropic-bridge.ts)
// ---------------------------------------------------------------------------

func bridgeNowUnix() int64 {
	return time.Now().Unix()
}

func bridgeRandomSuffix() string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	out := make([]byte, 8)
	for i := range out {
		out[i] = alphabet[rand.Intn(len(alphabet))]
	}
	return string(out)
}

func safeBridgeIDSegment(value string) string {
	var builder strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			builder.WriteRune(r)
		default:
			builder.WriteByte('_')
		}
	}
	out := builder.String()
	if len(out) > 96 {
		out = out[:96]
	}
	if out == "" {
		return "anthropic"
	}
	return out
}

// chatCompletionIDFromAnthropicID mirrors chatCompletionIdFromAnthropicId.
func chatCompletionIDFromAnthropicID(id string) string {
	source := id
	if source == "" {
		source = int64ToText(bridgeNowUnix()) + "_" + bridgeRandomSuffix()
	}
	return "chatcmpl_" + safeBridgeIDSegment(source)
}

// normalizeAnthropicMessageID mirrors normalizeAnthropicMessageId.
func normalizeAnthropicMessageID(value string) string {
	if value == "" {
		return ""
	}
	if strings.HasPrefix(value, "msg_") {
		return value
	}
	return "msg_" + value
}

// openAIChatFinishReasonFromAnthropic mirrors openAIChatFinishReasonFromAnthropic.
func openAIChatFinishReasonFromAnthropic(reason string) string {
	switch reason {
	case "max_tokens":
		return "length"
	case "tool_use":
		return "tool_calls"
	case "stop_sequence", "end_turn", "":
		return "stop"
	default:
		return reason
	}
}

// chatFinishReasonToAnthropicStopReason mirrors chatFinishReasonToAnthropicStopReason.
func chatFinishReasonToAnthropicStopReason(finishReason string, blocks []any) string {
	switch finishReason {
	case "tool_calls":
		return "tool_use"
	case "length":
		return "max_tokens"
	case "content_filter":
		return "refusal"
	}
	for _, block := range blocks {
		if blockMap := bridgeObjectValue(block); blockMap != nil && bridgeStringValue(blockMap["type"]) == "tool_use" {
			return "tool_use"
		}
	}
	return "end_turn"
}

func anthropicInputTokens(usage map[string]any) int64 {
	if usage == nil {
		return 0
	}
	if value, ok := bridgeIntegerValue(usage["input_tokens"]); ok && value != 0 {
		return value
	}
	value, _ := bridgeIntegerValue(usage["prompt_tokens"])
	return value
}

// anthropicUsageToChatUsage mirrors anthropicUsageToChatUsage.
func anthropicUsageToChatUsage(usage map[string]any, previous map[string]any) map[string]any {
	inputTokens := anthropicInputTokens(usage)
	if inputTokens == 0 {
		if previous != nil {
			inputTokens, _ = bridgeIntegerValue(previous["prompt_tokens"])
		}
	}
	var outputTokens int64
	if usage != nil {
		if value, ok := bridgeIntegerValue(usage["output_tokens"]); ok {
			outputTokens = value
		} else if previous != nil {
			outputTokens, _ = bridgeIntegerValue(previous["completion_tokens"])
		}
	} else if previous != nil {
		outputTokens, _ = bridgeIntegerValue(previous["completion_tokens"])
	}
	cached := int64(0)
	if usage != nil {
		cached, _ = bridgeIntegerValue(usage["cache_read_input_tokens"])
	}
	if cached == 0 && previous != nil {
		if details := bridgeObjectValue(previous["prompt_tokens_details"]); details != nil {
			cached, _ = bridgeIntegerValue(details["cached_tokens"])
		}
	}
	return map[string]any{
		"prompt_tokens":     float64(inputTokens),
		"completion_tokens": float64(outputTokens),
		"total_tokens":      float64(inputTokens + outputTokens),
		"prompt_tokens_details": map[string]any{
			"cached_tokens": float64(cached),
		},
	}
}

// chatUsageToAnthropicUsage mirrors chatUsageToAnthropicUsage.
func chatUsageToAnthropicUsage(usage map[string]any) map[string]any {
	if usage == nil {
		return nil
	}
	promptTokens := int64(0)
	if value, ok := bridgeIntegerValue(usage["prompt_tokens"]); ok {
		promptTokens = value
	} else if value, ok := bridgeIntegerValue(usage["input_tokens"]); ok {
		promptTokens = value
	}
	completionTokens := int64(0)
	if value, ok := bridgeIntegerValue(usage["completion_tokens"]); ok {
		completionTokens = value
	} else if value, ok := bridgeIntegerValue(usage["output_tokens"]); ok {
		completionTokens = value
	}
	output := map[string]any{
		"input_tokens":  float64(promptTokens),
		"output_tokens": float64(completionTokens),
	}
	cached := int64(0)
	if details := bridgeObjectValue(usage["prompt_tokens_details"]); details != nil {
		cached, _ = bridgeIntegerValue(details["cached_tokens"])
	}
	if cached == 0 {
		if details := bridgeObjectValue(usage["input_tokens_details"]); details != nil {
			cached, _ = bridgeIntegerValue(details["cached_tokens"])
		}
	}
	if cached != 0 {
		output["cache_read_input_tokens"] = float64(cached)
	}
	return output
}

// ---------------------------------------------------------------------------
// Anthropic Messages -> OpenAI Chat (openai-anthropic-bridge.ts)
// ---------------------------------------------------------------------------

// AnthropicMessageToChatCompletion mirrors anthropicMessageToChatCompletion.
func AnthropicMessageToChatCompletion(message map[string]any, fallbackModel string) map[string]any {
	model := bridgeStringValue(message["model"])
	if model == "" {
		model = fallbackModel
	}
	blocks, _ := bridgeIsArray(message["content"])
	textParts := []string{}
	toolCalls := []any{}
	for _, block := range blocks {
		blockMap := bridgeObjectValue(block)
		if blockMap == nil {
			continue
		}
		switch bridgeStringValue(blockMap["type"]) {
		case "text":
			if text := bridgeStringValue(blockMap["text"]); text != "" {
				textParts = append(textParts, text)
			}
		case "tool_use":
			name := bridgeStringValue(blockMap["name"])
			input := blockMap["input"]
			if !bridgeIsPlainObject(input) {
				input = map[string]any{}
			}
			id := bridgeStringValue(blockMap["id"])
			if id == "" {
				id = "call_" + int64ToText(int64(len(toolCalls)))
			}
			toolCalls = append(toolCalls, map[string]any{
				"id":   id,
				"type": "function",
				"function": map[string]any{
					"name":      name,
					"arguments": bridgeJSONStringify(input),
				},
			})
		}
	}
	text := strings.Join(textParts, "")
	assistantMessage := map[string]any{
		"role": "assistant",
	}
	if text != "" {
		assistantMessage["content"] = text
	} else if len(toolCalls) > 0 {
		assistantMessage["content"] = nil
	} else {
		assistantMessage["content"] = ""
	}
	if len(toolCalls) > 0 {
		assistantMessage["tool_calls"] = toolCalls
	}
	stopReason := openAIChatFinishReasonFromAnthropic(bridgeStringValue(message["stop_reason"]))
	usage := map[string]any{}
	if usageValue := bridgeObjectValue(message["usage"]); usageValue != nil {
		usage = anthropicUsageToChatUsage(usageValue, nil)
	}
	return map[string]any{
		"id":      chatCompletionIDFromAnthropicID(bridgeStringValue(message["id"])),
		"object":  "chat.completion",
		"created": float64(bridgeNowUnix()),
		"model":   model,
		"choices": []any{map[string]any{
			"index":         float64(0),
			"message":       assistantMessage,
			"finish_reason": stopReason,
		}},
		"usage": usage,
	}
}

// AnthropicChatStreamState mirrors AnthropicStreamState for the chat target.
type AnthropicChatStreamState struct {
	ChatID            string
	AnthropicID       string
	Model             string
	CreatedAt         int64
	RoleSent          bool
	Completed         bool
	Failed            bool
	TerminalReceived  bool
	StopReason        string
	Usage             map[string]any
	blocks            map[int64]*anthropicStreamBlockState
	nextToolCallIndex int64
	textSeen          bool
}

type anthropicStreamBlockState struct {
	index          int64
	blockType      string
	id             string
	name           string
	text           string
	inputJSON      string
	toolCallIndex  int64
	toolCallQueued bool
}

// NewAnthropicChatStreamState mirrors createAnthropicStreamState for
// OPENAI_CHAT_COMPLETIONS_FAMILY.
func NewAnthropicChatStreamState(model string) *AnthropicChatStreamState {
	suffix := int64ToText(bridgeNowUnix()) + "_" + bridgeRandomSuffix()
	return &AnthropicChatStreamState{
		ChatID:      "chatcmpl_anthropic_" + suffix,
		AnthropicID: "msg_bridge_" + suffix,
		Model:       model,
		CreatedAt:   bridgeNowUnix(),
		blocks:      map[int64]*anthropicStreamBlockState{},
	}
}

func (s *AnthropicChatStreamState) chatSseChunk(delta map[string]any) string {
	return BridgeSseData(map[string]any{
		"id":      s.ChatID,
		"object":  "chat.completion.chunk",
		"created": float64(s.CreatedAt),
		"model":   s.Model,
		"choices": []any{map[string]any{
			"index":         float64(0),
			"delta":         delta,
			"finish_reason": nil,
		}},
		"usage": nil,
	})
}

func (s *AnthropicChatStreamState) ensureRoleChunk(out *[]string) {
	if s.RoleSent {
		return
	}
	s.RoleSent = true
	*out = append(*out, s.chatSseChunk(map[string]any{"role": "assistant"}))
}

// FailAnthropicChatStream mirrors failChatStream.
func FailAnthropicChatStream(state *AnthropicChatStreamState, message, code string) []string {
	if state.Completed || state.Failed {
		return nil
	}
	state.Failed = true
	if code == "" {
		code = "upstream_error"
	}
	return []string{
		BridgeSseData(map[string]any{
			"error": map[string]any{
				"message": message,
				"type":    "upstream_error",
				"code":    code,
			},
		}),
		"data: [DONE]\n\n",
	}
}

// CompleteAnthropicChatStream mirrors completeChatStream.
func CompleteAnthropicChatStream(state *AnthropicChatStreamState, finishReason string) []string {
	if state.Completed || state.Failed {
		return nil
	}
	state.Completed = true
	output := []string{}
	state.ensureRoleChunk(&output)
	if finishReason == "" {
		finishReason = "stop"
	}
	output = append(output, BridgeSseData(map[string]any{
		"id":      state.ChatID,
		"object":  "chat.completion.chunk",
		"created": float64(state.CreatedAt),
		"model":   state.Model,
		"choices": []any{map[string]any{
			"index":         float64(0),
			"delta":         map[string]any{},
			"finish_reason": finishReason,
		}},
		"usage": nil,
	}))
	usage := state.Usage
	if usage == nil {
		usage = map[string]any{
			"prompt_tokens":     float64(0),
			"completion_tokens": float64(0),
			"total_tokens":      float64(0),
		}
	}
	output = append(output, BridgeSseData(map[string]any{
		"id":      state.ChatID,
		"object":  "chat.completion.chunk",
		"created": float64(state.CreatedAt),
		"model":   state.Model,
		"choices": []any{},
		"usage":   usage,
	}))
	output = append(output, "data: [DONE]\n\n")
	return output
}

// ProcessAnthropicEventAsChat mirrors processAnthropicEventAsChat: one Anthropic
// Messages SSE event in, zero or more OpenAI chat.completion.chunk events out.
func ProcessAnthropicEventAsChat(state *AnthropicChatStreamState, rawEventText string) []string {
	if state.Completed || state.Failed {
		return nil
	}
	event := ParseBridgeSseEvent(rawEventText)
	if event.EventName == "error" || event.EventType == "error" {
		state.Failed = true
		payload := openAIErrorFromAnthropicPayload(event.Data)
		return []string{BridgeSseData(payload), "data: [DONE]\n\n"}
	}
	if event.DataParseError {
		return FailAnthropicChatStream(state, "上游 Anthropic Messages SSE 返回了无法解析的事件", "upstream_stream_parse_error")
	}
	data := event.Data
	if data == nil {
		return nil
	}
	output := []string{}
	switch bridgeStringValue(data["type"]) {
	case "message_start":
		message := bridgeObjectValue(data["message"])
		if message != nil {
			if id := bridgeStringValue(message["id"]); id != "" {
				state.AnthropicID = id
				state.ChatID = chatCompletionIDFromAnthropicID(id)
			}
			if model := bridgeStringValue(message["model"]); model != "" {
				state.Model = model
			}
			if usage := bridgeObjectValue(message["usage"]); usage != nil {
				state.Usage = anthropicUsageToChatUsage(usage, state.Usage)
			}
		}
		state.ensureRoleChunk(&output)
		return output
	case "content_block_start":
		index, _ := bridgeIntegerValue(data["index"])
		contentBlock := bridgeObjectValue(data["content_block"])
		block := streamBlockFromContentBlock(index, contentBlock)
		if block.blockType == "tool_use" {
			block.toolCallIndex = state.nextToolCallIndex
			state.nextToolCallIndex++
		}
		state.blocks[index] = block
		state.ensureRoleChunk(&output)
		if block.blockType == "tool_use" {
			output = append(output, state.chatSseChunk(map[string]any{
				"tool_calls": []any{map[string]any{
					"index": float64(block.toolCallIndex),
					"id":    firstNonEmpty(block.id, "call_"+int64ToText(index)),
					"type":  "function",
					"function": map[string]any{
						"name":      block.name,
						"arguments": "",
					},
				}},
			}))
			block.toolCallQueued = true
		}
		return output
	case "content_block_delta":
		index, _ := bridgeIntegerValue(data["index"])
		block := state.blocks[index]
		delta := bridgeObjectValue(data["delta"])
		state.ensureRoleChunk(&output)
		if delta == nil {
			return output
		}
		switch bridgeStringValue(delta["type"]) {
		case "text_delta":
			text := bridgeStringValue(delta["text"])
			if block != nil {
				block.text += text
				state.textSeen = true
			}
			if text != "" {
				output = append(output, state.chatSseChunk(map[string]any{"content": text}))
			}
		case "input_json_delta":
			partial := bridgeStringValue(delta["partial_json"])
			if block != nil {
				block.inputJSON += partial
			}
			if partial != "" && block != nil && block.blockType == "tool_use" {
				output = append(output, state.chatSseChunk(map[string]any{
					"tool_calls": []any{map[string]any{
						"index":    float64(block.toolCallIndex),
						"function": map[string]any{"arguments": partial},
					}},
				}))
			}
		case "thinking_delta":
			text := firstNonEmpty(bridgeStringValue(delta["thinking"]), bridgeStringValue(delta["text"]))
			if block != nil {
				block.text += text
			}
		}
		return output
	case "message_delta":
		delta := bridgeObjectValue(data["delta"])
		if delta != nil {
			if stopReason := bridgeStringValue(delta["stop_reason"]); stopReason != "" {
				state.StopReason = stopReason
			}
		}
		if usage := bridgeObjectValue(data["usage"]); usage != nil {
			state.Usage = anthropicUsageToChatUsage(usage, state.Usage)
		}
		return output
	case "message_stop":
		state.TerminalReceived = true
		output = append(output, CompleteAnthropicChatStream(state, openAIChatFinishReasonFromAnthropic(state.StopReason))...)
		return output
	default:
		return output
	}
}

func streamBlockFromContentBlock(index int64, block map[string]any) *anthropicStreamBlockState {
	state := &anthropicStreamBlockState{index: index, blockType: "text"}
	if block != nil {
		if blockType := bridgeStringValue(block["type"]); blockType != "" {
			state.blockType = blockType
		}
		state.id = bridgeStringValue(block["id"])
		state.name = bridgeStringValue(block["name"])
		state.text = bridgeStringValue(block["text"])
		if state.blockType == "tool_use" {
			if input := bridgeObjectValue(block["input"]); input != nil && len(input) > 0 {
				state.inputJSON = bridgeJSONStringify(input)
			}
		}
	}
	return state
}

func openAIErrorFromAnthropicPayload(value map[string]any) map[string]any {
	record := value
	if record == nil {
		record = map[string]any{}
	}
	errorObject := bridgeObjectValue(record["error"])
	if errorObject == nil {
		errorObject = record
	}
	message := bridgeStringValue(errorObject["message"])
	if message == "" {
		message = "上游 Anthropic Messages 请求失败"
	}
	errType := firstNonEmpty(bridgeStringValue(errorObject["type"]), bridgeStringValue(record["type"]), "upstream_error")
	return map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    errType,
			"code":    errType,
		},
	}
}

// TransformAnthropicMessagesJSONToChatJSONBody renders the buffered
// Anthropic Messages response as a chat completion JSON body.
func TransformAnthropicMessagesJSONToChatJSONBody(parsed map[string]any, fallbackModel string) []byte {
	return []byte(bridgeJSONStringify(AnthropicMessageToChatCompletion(parsed, fallbackModel)))
}

// ---------------------------------------------------------------------------
// OpenAI Chat -> Anthropic Messages responses
// (anthropic-openai-chat-bridge.ts)
// ---------------------------------------------------------------------------

// ChatCompletionJSONToAnthropicMessage mirrors chatCompletionJsonToAnthropicMessage.
func ChatCompletionJSONToAnthropicMessage(value map[string]any, fallbackModel string) map[string]any {
	choice := map[string]any{}
	if choices, ok := bridgeIsArray(value["choices"]); ok && len(choices) > 0 {
		if first := bridgeObjectValue(choices[0]); first != nil {
			choice = first
		}
	}
	message := bridgeObjectValue(choice["message"])
	blocks := chatMessageToAnthropicContentBlocks(message)
	model := bridgeStringValue(value["model"])
	if model == "" {
		model = fallbackModel
	}
	id := normalizeAnthropicMessageID(bridgeStringValue(value["id"]))
	if id == "" {
		id = "msg_" + safeBridgeIDSegment(fallbackModel) + "_" + bridgeRandomSuffix()
	}
	finishReason := bridgeStringValue(choice["finish_reason"])
	var usage any
	if usageValue := bridgeObjectValue(value["usage"]); usageValue != nil {
		usage = chatUsageToAnthropicUsage(usageValue)
	}
	return map[string]any{
		"id":            id,
		"type":          "message",
		"role":          "assistant",
		"model":         model,
		"content":       blocks,
		"stop_reason":   chatFinishReasonToAnthropicStopReason(finishReason, blocks),
		"stop_sequence": nil,
		"usage":         usage,
	}
}

func chatMessageToAnthropicContentBlocks(message map[string]any) []any {
	output := []any{}
	text := chatMessageTextContent(message)
	if text == "" && message != nil {
		text = firstNonEmpty(bridgeStringValue(message["reasoning_content"]), bridgeStringValue(message["refusal"]))
	}
	if text != "" {
		output = append(output, map[string]any{"type": "text", "text": text})
	}
	if message == nil {
		return output
	}
	toolCalls, _ := bridgeIsArray(message["tool_calls"])
	for _, item := range toolCalls {
		toolCall := bridgeObjectValue(item)
		if toolCall == nil {
			continue
		}
		fn := bridgeObjectValue(toolCall["function"])
		name := ""
		if fn != nil {
			name = bridgeStringValue(fn["name"])
		}
		if name == "" {
			continue
		}
		arguments := ""
		if fn != nil {
			arguments = bridgeStringValue(fn["arguments"])
		}
		id := bridgeStringValue(toolCall["id"])
		if id == "" {
			id = "toolu_" + int64ToText(int64(len(output)))
		}
		output = append(output, map[string]any{
			"type":  "tool_use",
			"id":    id,
			"name":  name,
			"input": bridgeParseToolArguments(arguments),
		})
	}
	return output
}

func chatMessageTextContent(message map[string]any) string {
	if message == nil {
		return ""
	}
	if text, ok := message["content"].(string); ok {
		return text
	}
	items, ok := bridgeIsArray(message["content"])
	if !ok {
		return ""
	}
	parts := []string{}
	for _, item := range items {
		part := bridgeObjectValue(item)
		if part == nil {
			continue
		}
		partType := bridgeStringValue(part["type"])
		if partType == "text" || partType == "output_text" {
			if text := bridgeStringValue(part["text"]); text != "" {
				parts = append(parts, text)
			}
		}
	}
	return strings.Join(parts, "")
}

// AnthropicFromChatStreamState mirrors AnthropicChatStreamState from
// anthropic-openai-chat-bridge.ts (chat completions -> anthropic messages SSE).
type AnthropicFromChatStreamState struct {
	ID                    string
	Model                 string
	Started               bool
	Completed             bool
	Failed                bool
	NextContentBlockIndex int64
	TextStarted           bool
	TextDone              bool
	TextBlockIndex        int64
	ToolCalls             map[int64]*anthropicChatToolCallState
	HadToolUse            bool
	StopReason            string
	Usage                 map[string]any
}

type anthropicChatToolCallState struct {
	key               int64
	blockIndex        int64
	id                string
	name              string
	arguments         string
	bufferedArguments string
	started           bool
	done              bool
}

// NewAnthropicFromChatStreamState mirrors createAnthropicChatStreamState.
func NewAnthropicFromChatStreamState(model string) *AnthropicFromChatStreamState {
	createdAt := bridgeNowUnix()
	return &AnthropicFromChatStreamState{
		ID:        "msg_chat_" + int64ToText(createdAt) + "_" + bridgeRandomSuffix(),
		Model:     model,
		ToolCalls: map[int64]*anthropicChatToolCallState{},
	}
}

func anthropicSse(name string, payload any) string {
	return BridgeSseEventText(name, payload)
}

func (s *AnthropicFromChatStreamState) ensureMessageStarted(out *[]string) {
	if s.Started {
		return
	}
	s.Started = true
	usage := s.Usage
	if usage == nil {
		usage = map[string]any{"input_tokens": float64(0), "output_tokens": float64(0)}
	}
	*out = append(*out, anthropicSse("message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id":            s.ID,
			"type":          "message",
			"role":          "assistant",
			"model":         s.Model,
			"content":       []any{},
			"stop_reason":   nil,
			"stop_sequence": nil,
			"usage":         usage,
		},
	}))
}

func (s *AnthropicFromChatStreamState) appendTextDelta(out *[]string, text string) {
	s.ensureMessageStarted(out)
	s.closeStartedToolBlocks(out, false)
	if !s.TextStarted {
		index := s.NextContentBlockIndex
		s.NextContentBlockIndex++
		s.TextBlockIndex = index
		s.TextStarted = true
		s.TextDone = false
		*out = append(*out, anthropicSse("content_block_start", map[string]any{
			"type":          "content_block_start",
			"index":         float64(index),
			"content_block": map[string]any{"type": "text", "text": ""},
		}))
	}
	*out = append(*out, anthropicSse("content_block_delta", map[string]any{
		"type":  "content_block_delta",
		"index": float64(s.TextBlockIndex),
		"delta": map[string]any{"type": "text_delta", "text": text},
	}))
}

func (s *AnthropicFromChatStreamState) appendToolCallDelta(out *[]string, toolCall map[string]any) {
	if toolCall == nil {
		return
	}
	key := int64(0)
	if value, ok := bridgeIntegerValue(toolCall["index"]); ok {
		key = value
	}
	fn := bridgeObjectValue(toolCall["function"])
	id, name := "", ""
	argumentDelta := ""
	if fn != nil {
		name = bridgeStringValue(fn["name"])
		argumentDelta = bridgeStringValue(fn["arguments"])
	}
	id = firstNonEmpty(bridgeStringValue(toolCall["id"]), id)
	current := s.ToolCalls[key]
	if current == nil {
		current = &anthropicChatToolCallState{
			key:        key,
			blockIndex: s.NextContentBlockIndex,
			id:         firstNonEmpty(id, "toolu_"+int64ToText(key)),
			name:       name,
		}
		s.NextContentBlockIndex++
		s.ToolCalls[key] = current
	}
	if id != "" {
		current.id = id
	}
	if name != "" {
		current.name = name
	}
	s.ensureMessageStarted(out)
	s.closeTextBlock(out)
	if !current.started && current.name != "" {
		current.started = true
		s.HadToolUse = true
		*out = append(*out, anthropicSse("content_block_start", map[string]any{
			"type":  "content_block_start",
			"index": float64(current.blockIndex),
			"content_block": map[string]any{
				"type":  "tool_use",
				"id":    current.id,
				"name":  current.name,
				"input": map[string]any{},
			},
		}))
		if current.bufferedArguments != "" {
			*out = append(*out, anthropicSse("content_block_delta", map[string]any{
				"type":  "content_block_delta",
				"index": float64(current.blockIndex),
				"delta": map[string]any{"type": "input_json_delta", "partial_json": current.bufferedArguments},
			}))
			current.arguments += current.bufferedArguments
			current.bufferedArguments = ""
		}
	}
	if argumentDelta != "" {
		if !current.started {
			current.bufferedArguments += argumentDelta
		} else {
			current.arguments += argumentDelta
			*out = append(*out, anthropicSse("content_block_delta", map[string]any{
				"type":  "content_block_delta",
				"index": float64(current.blockIndex),
				"delta": map[string]any{"type": "input_json_delta", "partial_json": argumentDelta},
			}))
		}
	}
}

func (s *AnthropicFromChatStreamState) closeTextBlock(out *[]string) {
	if !s.TextStarted || s.TextDone {
		return
	}
	s.TextDone = true
	*out = append(*out, anthropicSse("content_block_stop", map[string]any{
		"type":  "content_block_stop",
		"index": float64(s.TextBlockIndex),
	}))
}

func (s *AnthropicFromChatStreamState) closeStartedToolBlocks(out *[]string, includeUnstarted bool) {
	keys := make([]int64, 0, len(s.ToolCalls))
	for key := range s.ToolCalls {
		keys = append(keys, key)
	}
	for i := 0; i < len(keys); i++ {
		for j := i + 1; j < len(keys); j++ {
			if s.ToolCalls[keys[j]].blockIndex < s.ToolCalls[keys[i]].blockIndex {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
	}
	for _, key := range keys {
		toolCall := s.ToolCalls[key]
		if toolCall.done {
			continue
		}
		if !toolCall.started && includeUnstarted && (toolCall.name != "" || toolCall.bufferedArguments != "") {
			toolCall.started = true
			if toolCall.name == "" {
				toolCall.name = "unknown_tool"
			}
			s.HadToolUse = true
			*out = append(*out, anthropicSse("content_block_start", map[string]any{
				"type":  "content_block_start",
				"index": float64(toolCall.blockIndex),
				"content_block": map[string]any{
					"type":  "tool_use",
					"id":    toolCall.id,
					"name":  toolCall.name,
					"input": map[string]any{},
				},
			}))
			if toolCall.bufferedArguments != "" {
				*out = append(*out, anthropicSse("content_block_delta", map[string]any{
					"type":  "content_block_delta",
					"index": float64(toolCall.blockIndex),
					"delta": map[string]any{"type": "input_json_delta", "partial_json": toolCall.bufferedArguments},
				}))
				toolCall.bufferedArguments = ""
			}
		}
		if !toolCall.started {
			continue
		}
		toolCall.done = true
		*out = append(*out, anthropicSse("content_block_stop", map[string]any{
			"type":  "content_block_stop",
			"index": float64(toolCall.blockIndex),
		}))
	}
}

// CompleteAnthropicFromChatStream mirrors completeAnthropicStream.
func CompleteAnthropicFromChatStream(state *AnthropicFromChatStreamState) []string {
	if state.Completed || state.Failed {
		return nil
	}
	state.Completed = true
	output := []string{}
	state.ensureMessageStarted(&output)
	if !state.TextStarted && !state.HadToolUse && len(state.ToolCalls) == 0 {
		state.appendTextDelta(&output, "上游 Chat Completions 返回了空 assistant 内容。客户端可以保持当前对话并重试，或换用更稳定的上游模型；网关已将空响应转换为可读提示，避免客户端因空消息中断。")
	}
	state.closeTextBlock(&output)
	state.closeStartedToolBlocks(&output, true)
	stopReason := state.StopReason
	if stopReason == "" {
		if state.HadToolUse {
			stopReason = "tool_use"
		} else {
			stopReason = "end_turn"
		}
	}
	outputTokens := float64(0)
	if state.Usage != nil {
		if value, ok := state.Usage["output_tokens"].(float64); ok {
			outputTokens = value
		}
	}
	output = append(output, anthropicSse("message_delta", map[string]any{
		"type": "message_delta",
		"delta": map[string]any{
			"stop_reason":   stopReason,
			"stop_sequence": nil,
		},
		"usage": map[string]any{"output_tokens": outputTokens},
	}))
	output = append(output, anthropicSse("message_stop", map[string]any{"type": "message_stop"}))
	return output
}

// FailAnthropicFromChatStream mirrors failAnthropicStream.
func FailAnthropicFromChatStream(state *AnthropicFromChatStreamState, message, code string) []string {
	if state.Completed || state.Failed {
		return nil
	}
	state.Failed = true
	if code == "" {
		code = "upstream_error"
	}
	return []string{anthropicSse("error", map[string]any{
		"type": "error",
		"error": map[string]any{
			"type":    "api_error",
			"code":    code,
			"message": message,
		},
	})}
}

// ProcessChatCompletionsSseEventAsAnthropic mirrors processChatCompletionsSseEvent
// from anthropic-openai-chat-bridge.ts.
func ProcessChatCompletionsSseEventAsAnthropic(state *AnthropicFromChatStreamState, rawEventText string) []string {
	if state.Completed || state.Failed {
		return nil
	}
	event := ParseBridgeSseEvent(rawEventText)
	if event.DataText == "[DONE]" {
		return CompleteAnthropicFromChatStream(state)
	}
	if event.DataParseError {
		return FailAnthropicFromChatStream(state, "上游 Chat Completions SSE 返回了无法解析的事件", "upstream_stream_parse_error")
	}
	data := event.Data
	if data == nil {
		return nil
	}
	if event.EventName == "error" || event.EventType == "error" || event.EventType == "response.failed" {
		errorObject := bridgeObjectValue(data["error"])
		if errorObject == nil {
			if response := bridgeObjectValue(data["response"]); response != nil {
				errorObject = bridgeObjectValue(response["error"])
			}
		}
		if errorObject == nil {
			errorObject = data
		}
		message := firstNonEmpty(bridgeStringValue(errorObject["message"]), "上游 Chat Completions SSE 返回错误事件")
		code := firstNonEmpty(bridgeStringValue(errorObject["code"]), bridgeStringValue(errorObject["type"]), "upstream_error")
		return FailAnthropicFromChatStream(state, message, code)
	}
	if id := bridgeStringValue(data["id"]); id != "" {
		state.ID = normalizeAnthropicMessageID(id)
	}
	if model := bridgeStringValue(data["model"]); model != "" {
		state.Model = model
	}
	if usage := chatUsageToAnthropicUsage(bridgeObjectValue(data["usage"])); usage != nil {
		state.Usage = usage
	}
	output := []string{}
	choices, _ := bridgeIsArray(data["choices"])
	for _, item := range choices {
		choice := bridgeObjectValue(item)
		if choice == nil {
			continue
		}
		if delta := bridgeObjectValue(choice["delta"]); delta != nil {
			text := firstNonEmpty(bridgeStringValue(delta["content"]), bridgeStringValue(delta["reasoning_content"]), bridgeStringValue(delta["refusal"]))
			if text != "" {
				state.appendTextDelta(&output, text)
			}
			toolCalls, _ := bridgeIsArray(delta["tool_calls"])
			for _, toolCallValue := range toolCalls {
				state.appendToolCallDelta(&output, bridgeObjectValue(toolCallValue))
			}
		}
		if finishReason := bridgeStringValue(choice["finish_reason"]); finishReason != "" {
			state.StopReason = chatFinishReasonToAnthropicStopReason(finishReason, nil)
		}
	}
	return output
}

// ---------------------------------------------------------------------------
// OpenAI Chat -> Gemini GenerateContent responses (gemini-openai-chat-bridge.ts)
// ---------------------------------------------------------------------------

// ChatCompletionJSONToGeminiGenerateContent mirrors
// chatCompletionJsonToGeminiGenerateContent.
func ChatCompletionJSONToGeminiGenerateContent(value map[string]any, fallbackModel string) map[string]any {
	model := bridgeStringValue(value["model"])
	if model == "" {
		model = fallbackModel
	}
	choices, _ := bridgeIsArray(value["choices"])
	candidates := []any{}
	for index, item := range choices {
		choice := bridgeObjectValue(item)
		parts := []any{}
		if choice != nil {
			parts = chatMessageToGeminiParts(bridgeObjectValue(choice["message"]))
		}
		if len(parts) == 0 {
			parts = append(parts, map[string]any{"text": "上游 Chat Completions 返回了空 assistant 内容。客户端可以保持当前对话并重试，或换用更稳定的上游模型；网关已将空响应转换为 Gemini 文本提示，避免客户端因空消息中断。"})
		}
		finishReason := ""
		if choice != nil {
			finishReason = bridgeStringValue(choice["finish_reason"])
		}
		candidates = append(candidates, map[string]any{
			"content":      map[string]any{"role": "model", "parts": parts},
			"finishReason": chatFinishReasonToGeminiFinishReason(finishReason, parts),
			"index":        float64(index),
		})
	}
	if len(candidates) == 0 {
		candidates = append(candidates, map[string]any{
			"content":      map[string]any{"role": "model", "parts": []any{map[string]any{"text": "上游 Chat Completions 返回了空 assistant 内容。客户端可以保持当前对话并重试，或换用更稳定的上游模型；网关已将空响应转换为 Gemini 文本提示，避免客户端因空消息中断。"}}},
			"finishReason": "STOP",
			"index":        float64(0),
		})
	}
	output := map[string]any{
		"candidates":   candidates,
		"modelVersion": model,
	}
	if usage := chatUsageToGeminiUsage(bridgeObjectValue(value["usage"])); usage != nil {
		output["usageMetadata"] = usage
	}
	return output
}

func chatMessageToGeminiParts(message map[string]any) []any {
	output := []any{}
	if text := chatMessageTextContent(message); text != "" {
		output = append(output, map[string]any{"text": text})
	}
	if message == nil {
		return output
	}
	toolCalls, _ := bridgeIsArray(message["tool_calls"])
	for _, item := range toolCalls {
		toolCall := bridgeObjectValue(item)
		if toolCall == nil {
			continue
		}
		fn := bridgeObjectValue(toolCall["function"])
		name := ""
		arguments := ""
		if fn != nil {
			name = bridgeStringValue(fn["name"])
			arguments = bridgeStringValue(fn["arguments"])
		}
		if name == "" {
			continue
		}
		output = append(output, map[string]any{
			"functionCall": map[string]any{
				"name": name,
				"args": bridgeParseToolArguments(arguments),
			},
		})
	}
	return output
}

func chatFinishReasonToGeminiFinishReason(finishReason string, parts []any) string {
	switch finishReason {
	case "length":
		return "MAX_TOKENS"
	case "content_filter":
		return "SAFETY"
	default:
		return "STOP"
	}
}

func chatUsageToGeminiUsage(usage map[string]any) map[string]any {
	if usage == nil {
		return nil
	}
	promptTokens := int64(0)
	if value, ok := bridgeIntegerValue(usage["prompt_tokens"]); ok {
		promptTokens = value
	} else if value, ok := bridgeIntegerValue(usage["input_tokens"]); ok {
		promptTokens = value
	}
	completionTokens := int64(0)
	if value, ok := bridgeIntegerValue(usage["completion_tokens"]); ok {
		completionTokens = value
	} else if value, ok := bridgeIntegerValue(usage["output_tokens"]); ok {
		completionTokens = value
	}
	totalTokens := promptTokens + completionTokens
	if value, ok := bridgeIntegerValue(usage["total_tokens"]); ok {
		totalTokens = value
	}
	output := map[string]any{
		"promptTokenCount":     float64(promptTokens),
		"candidatesTokenCount": float64(completionTokens),
		"totalTokenCount":      float64(totalTokens),
	}
	cached := int64(0)
	if details := bridgeObjectValue(usage["prompt_tokens_details"]); details != nil {
		cached, _ = bridgeIntegerValue(details["cached_tokens"])
	}
	if cached == 0 {
		if details := bridgeObjectValue(usage["input_tokens_details"]); details != nil {
			cached, _ = bridgeIntegerValue(details["cached_tokens"])
		}
	}
	if cached != 0 {
		output["cachedContentTokenCount"] = float64(cached)
	}
	reasoning := int64(0)
	if details := bridgeObjectValue(usage["completion_tokens_details"]); details != nil {
		reasoning, _ = bridgeIntegerValue(details["reasoning_tokens"])
	}
	if reasoning == 0 {
		if details := bridgeObjectValue(usage["output_tokens_details"]); details != nil {
			reasoning, _ = bridgeIntegerValue(details["reasoning_tokens"])
		}
	}
	if reasoning != 0 {
		output["thoughtsTokenCount"] = float64(reasoning)
	}
	return output
}

// GeminiChatStreamState mirrors GeminiChatStreamState.
type GeminiChatStreamState struct {
	Model          string
	Completed      bool
	Failed         bool
	EmittedContent bool
	FinishReason   string
	Usage          map[string]any
	toolCalls      map[int64]*geminiStreamToolCallState
}

type geminiStreamToolCallState struct {
	id        string
	name      string
	arguments string
}

// NewGeminiChatStreamState mirrors createGeminiChatStreamState.
func NewGeminiChatStreamState(model string) *GeminiChatStreamState {
	return &GeminiChatStreamState{Model: model, toolCalls: map[int64]*geminiStreamToolCallState{}}
}

func geminiSse(payload any) string {
	return BridgeSseData(payload)
}

func (s *GeminiChatStreamState) emitCompletedToolCalls(out *[]string) {
	if len(s.toolCalls) == 0 {
		return
	}
	keys := make([]int64, 0, len(s.toolCalls))
	for key := range s.toolCalls {
		keys = append(keys, key)
	}
	for i := 0; i < len(keys); i++ {
		for j := i + 1; j < len(keys); j++ {
			if s.toolCalls[keys[j]].id < s.toolCalls[keys[i]].id {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
	}
	parts := []any{}
	for _, key := range keys {
		toolCall := s.toolCalls[key]
		if toolCall.name == "" {
			continue
		}
		parts = append(parts, map[string]any{
			"functionCall": map[string]any{
				"name": toolCall.name,
				"args": bridgeParseToolArguments(toolCall.arguments),
			},
		})
	}
	if len(parts) == 0 {
		return
	}
	s.toolCalls = map[int64]*geminiStreamToolCallState{}
	s.EmittedContent = true
	*out = append(*out, geminiSse(map[string]any{
		"candidates": []any{map[string]any{
			"content": map[string]any{"role": "model", "parts": parts},
		}},
		"modelVersion": s.Model,
	}))
}

// CompleteGeminiChatStream mirrors completeGeminiStream.
func CompleteGeminiChatStream(state *GeminiChatStreamState) []string {
	if state.Completed || state.Failed {
		return nil
	}
	state.Completed = true
	output := []string{}
	state.emitCompletedToolCalls(&output)
	if !state.EmittedContent {
		output = append(output, geminiSse(map[string]any{
			"candidates": []any{map[string]any{
				"content": map[string]any{"role": "model", "parts": []any{map[string]any{"text": "上游 Chat Completions 返回了空 assistant 内容。客户端可以保持当前对话并重试，或换用更稳定的上游模型；网关已将空响应转换为 Gemini 文本提示，避免客户端因空消息中断。"}}},
			}},
			"modelVersion": state.Model,
		}))
	}
	finalEvent := map[string]any{
		"candidates": []any{map[string]any{
			"finishReason": firstNonEmpty(state.FinishReason, "STOP"),
		}},
		"modelVersion": state.Model,
	}
	if state.Usage != nil {
		finalEvent["usageMetadata"] = state.Usage
	}
	output = append(output, geminiSse(finalEvent))
	return output
}

// FailGeminiChatStream mirrors failGeminiStream.
func FailGeminiChatStream(state *GeminiChatStreamState, message, code string) []string {
	if state.Completed || state.Failed {
		return nil
	}
	state.Failed = true
	if code == "" {
		code = "upstream_error"
	}
	return []string{BridgeSseEventText("error", map[string]any{
		"error": map[string]any{
			"message": message,
			"status":  "INTERNAL",
			"code":    code,
		},
	})}
}

// ProcessChatCompletionsSseEventAsGemini mirrors processChatCompletionsSseEvent
// from gemini-openai-chat-bridge.ts.
func ProcessChatCompletionsSseEventAsGemini(state *GeminiChatStreamState, rawEventText string) []string {
	if state.Completed || state.Failed {
		return nil
	}
	event := ParseBridgeSseEvent(rawEventText)
	if event.DataText == "[DONE]" {
		return CompleteGeminiChatStream(state)
	}
	if event.DataParseError {
		return FailGeminiChatStream(state, "上游 Chat Completions SSE 返回了无法解析的事件", "upstream_stream_parse_error")
	}
	data := event.Data
	if data == nil {
		return nil
	}
	if event.EventName == "error" || event.EventType == "error" || event.EventType == "response.failed" {
		errorObject := bridgeObjectValue(data["error"])
		if errorObject == nil {
			if response := bridgeObjectValue(data["response"]); response != nil {
				errorObject = bridgeObjectValue(response["error"])
			}
		}
		if errorObject == nil {
			errorObject = data
		}
		message := firstNonEmpty(bridgeStringValue(errorObject["message"]), "上游 Chat Completions SSE 返回错误事件")
		code := firstNonEmpty(bridgeStringValue(errorObject["code"]), bridgeStringValue(errorObject["type"]), "upstream_error")
		return FailGeminiChatStream(state, message, code)
	}
	if model := bridgeStringValue(data["model"]); model != "" {
		state.Model = model
	}
	if usage := chatUsageToGeminiUsage(bridgeObjectValue(data["usage"])); usage != nil {
		state.Usage = usage
	}
	output := []string{}
	choices, _ := bridgeIsArray(data["choices"])
	for _, item := range choices {
		choice := bridgeObjectValue(item)
		if choice == nil {
			continue
		}
		if delta := bridgeObjectValue(choice["delta"]); delta != nil {
			text := firstNonEmpty(bridgeStringValue(delta["content"]), bridgeStringValue(delta["reasoning_content"]), bridgeStringValue(delta["refusal"]))
			if text != "" {
				state.EmittedContent = true
				output = append(output, geminiSse(map[string]any{
					"candidates": []any{map[string]any{
						"content": map[string]any{"role": "model", "parts": []any{map[string]any{"text": text}}},
					}},
					"modelVersion": state.Model,
				}))
			}
			toolCalls, _ := bridgeIsArray(delta["tool_calls"])
			for _, toolCallValue := range toolCalls {
				toolCall := bridgeObjectValue(toolCallValue)
				if toolCall == nil {
					continue
				}
				key := int64(0)
				if value, ok := bridgeIntegerValue(toolCall["index"]); ok {
					key = value
				}
				current := state.toolCalls[key]
				if current == nil {
					id := bridgeStringValue(toolCall["id"])
					if id == "" {
						id = "call_" + int64ToText(key)
					}
					current = &geminiStreamToolCallState{id: id}
					state.toolCalls[key] = current
				}
				if id := bridgeStringValue(toolCall["id"]); id != "" {
					current.id = id
				}
				if fn := bridgeObjectValue(toolCall["function"]); fn != nil {
					if name := bridgeStringValue(fn["name"]); name != "" {
						current.name = name
					}
					current.arguments += bridgeStringValue(fn["arguments"])
				}
			}
		}
		if finishReason := bridgeStringValue(choice["finish_reason"]); finishReason != "" {
			state.FinishReason = chatFinishReasonToGeminiFinishReason(finishReason, nil)
			state.emitCompletedToolCalls(&output)
		}
	}
	return output
}

// ---------------------------------------------------------------------------
// Gemini GenerateContent -> OpenAI Chat (gemini native upstream direction)
// ---------------------------------------------------------------------------

// TransformGeminiGenerateContentToOpenAIChatResponse transforms a buffered
// Gemini GenerateContent response into a chat completion (the
// openai-anthropic-gemini-native reverse surface).
func TransformGeminiGenerateContentToOpenAIChatResponse(response map[string]any, options BridgeTransformResponseOptions) map[string]any {
	if !options.Enabled || response == nil {
		return response
	}
	candidates, _ := bridgeIsArray(response["candidates"])
	choices := []any{}
	for _, candidate := range candidates {
		candMap := bridgeObjectValue(candidate)
		if candMap == nil {
			continue
		}
		parts := extractGeminiParts(bridgeObjectValue(candMap["content"]))
		message := map[string]any{"role": "assistant", "content": strings.Join(parts, "")}
		toolCalls := geminiFunctionCallsToChatToolCalls(bridgeObjectValue(candMap["content"]))
		if len(toolCalls) > 0 {
			message["tool_calls"] = toolCalls
			if strings.Join(parts, "") == "" {
				message["content"] = nil
			}
		}
		choices = append(choices, map[string]any{
			"index":         float64(len(choices)),
			"message":       message,
			"finish_reason": finishReasonFromGeminiCandidate(candMap),
		})
	}
	result := map[string]any{}
	if len(choices) > 0 {
		result["choices"] = choices
	}
	if usage := bridgeObjectValue(response["usageMetadata"]); usage != nil {
		prompt := numberAsFloat64(usage["promptTokenCount"])
		completion := numberAsFloat64(usage["candidatesTokenCount"])
		result["usage"] = map[string]any{
			"prompt_tokens":     prompt,
			"completion_tokens": completion,
			"total_tokens":      firstNonEmptyFloat(numberAsFloat64(usage["totalTokenCount"]), prompt+completion),
		}
	}
	if id := bridgeStringValue(response["responseId"]); id != "" {
		result["id"] = id
	}
	result["object"] = "chat.completion"
	result["created"] = float64(bridgeNowUnix())
	model := bridgeStringValue(response["modelVersion"])
	if model == "" {
		model = options.Model
	}
	result["model"] = model
	return result
}

func numberAsFloat64(value any) float64 {
	switch number := value.(type) {
	case float64:
		return number
	case int:
		return float64(number)
	case int64:
		return float64(number)
	default:
		return 0
	}
}

func firstNonEmptyFloat(value, fallback float64) float64 {
	if value != 0 {
		return value
	}
	return fallback
}

func extractGeminiParts(content map[string]any) []string {
	parts := []string{}
	if content == nil {
		return parts
	}
	if text, ok := content["text"].(string); ok && text != "" {
		return []string{text}
	}
	inlineParts, _ := bridgeIsArray(content["parts"])
	for _, part := range inlineParts {
		if partMap := bridgeObjectValue(part); partMap != nil {
			if text, ok := partMap["text"].(string); ok {
				parts = append(parts, text)
			}
		}
	}
	return parts
}

func geminiFunctionCallsToChatToolCalls(content map[string]any) []any {
	if content == nil {
		return nil
	}
	parts, _ := bridgeIsArray(content["parts"])
	toolCalls := []any{}
	for _, part := range parts {
		partMap := bridgeObjectValue(part)
		if partMap == nil {
			continue
		}
		call := bridgeObjectValue(partMap["functionCall"])
		if call == nil {
			continue
		}
		name := bridgeStringValue(call["name"])
		if name == "" {
			continue
		}
		args := call["args"]
		if !bridgeIsPlainObject(args) {
			args = map[string]any{}
		}
		toolCalls = append(toolCalls, map[string]any{
			"id":   "call_" + safeBridgeIDSegment(name),
			"type": "function",
			"function": map[string]any{
				"name":      name,
				"arguments": bridgeJSONStringify(args),
			},
		})
	}
	return toolCalls
}

// ProcessGeminiSseEventAsChat mirrors the gemini-native SSE -> chat chunk
// conversion: each Gemini streamGenerateContent SSE payload becomes zero or
// more chat.completion.chunk frames.
func ProcessGeminiSseEventAsChat(state *GeminiNativeChatStreamState, rawEventText string) []string {
	if state.Completed || state.Failed {
		return nil
	}
	event := ParseBridgeSseEvent(rawEventText)
	if event.DataText == "[DONE]" {
		return CompleteGeminiNativeChatStream(state)
	}
	if event.DataParseError {
		state.Failed = true
		return []string{BridgeSseData(map[string]any{
			"error": map[string]any{
				"message": "上游 Gemini SSE 返回了无法解析的事件",
				"type":    "upstream_error",
				"code":    "upstream_stream_parse_error",
			},
		}), "data: [DONE]\n\n"}
	}
	data := event.Data
	if data == nil {
		return nil
	}
	if errorObject := bridgeObjectValue(data["error"]); errorObject != nil {
		state.Failed = true
		message := firstNonEmpty(bridgeStringValue(errorObject["message"]), "上游 Gemini SSE 返回错误事件")
		return []string{BridgeSseData(map[string]any{"error": map[string]any{
			"message": message,
			"type":    "upstream_error",
			"code":    firstNonEmpty(bridgeStringValue(errorObject["status"]), "upstream_error"),
		}}), "data: [DONE]\n\n"}
	}
	output := []string{}
	candidates, _ := bridgeIsArray(data["candidates"])
	for _, candidate := range candidates {
		candMap := bridgeObjectValue(candidate)
		if candMap == nil {
			continue
		}
		if content := bridgeObjectValue(candMap["content"]); content != nil {
			parts, _ := bridgeIsArray(content["parts"])
			for _, part := range parts {
				partMap := bridgeObjectValue(part)
				if partMap == nil {
					continue
				}
				if text, ok := partMap["text"].(string); ok && text != "" {
					state.EmittedContent = true
					state.ensureRoleChunk(&output)
					output = append(output, state.chatSseChunk(map[string]any{"content": text}))
				}
				if call := bridgeObjectValue(partMap["functionCall"]); call != nil {
					name := bridgeStringValue(call["name"])
					if name == "" {
						continue
					}
					state.EmittedContent = true
					state.ensureRoleChunk(&output)
					args := call["args"]
					if !bridgeIsPlainObject(args) {
						args = map[string]any{}
					}
					index := state.NextToolCallIndex
					state.NextToolCallIndex++
					output = append(output, state.chatSseChunk(map[string]any{
						"tool_calls": []any{map[string]any{
							"index": float64(index),
							"id":    "call_" + safeBridgeIDSegment(name) + "_" + int64ToText(index),
							"type":  "function",
							"function": map[string]any{
								"name":      name,
								"arguments": bridgeJSONStringify(args),
							},
						}},
					}))
					state.StopReason = "tool_calls"
				}
			}
		}
		if finishReason := bridgeStringValue(candMap["finishReason"]); finishReason != "" {
			state.StopReason = geminiFinishReasonToChatFinishReason(finishReason)
		}
	}
	if usage := bridgeObjectValue(data["usageMetadata"]); usage != nil {
		state.Usage = geminiUsageToChatUsage(usage)
	}
	if state.StopReason != "" && !state.TerminalReceived {
		state.TerminalReceived = true
		output = append(output, CompleteGeminiNativeChatStream(state)...)
	}
	return output
}

// GeminiNativeChatStreamState carries the gemini-native -> chat stream state.
type GeminiNativeChatStreamState struct {
	ChatID            string
	Model             string
	CreatedAt         int64
	RoleSent          bool
	Completed         bool
	Failed            bool
	TerminalReceived  bool
	EmittedContent    bool
	StopReason        string
	Usage             map[string]any
	NextToolCallIndex int64
}

// NewGeminiNativeChatStreamState builds the stream state.
func NewGeminiNativeChatStreamState(model string) *GeminiNativeChatStreamState {
	return &GeminiNativeChatStreamState{
		ChatID:    "chatcmpl_gemini_" + int64ToText(bridgeNowUnix()) + "_" + bridgeRandomSuffix(),
		Model:     model,
		CreatedAt: bridgeNowUnix(),
	}
}

func (s *GeminiNativeChatStreamState) chatSseChunk(delta map[string]any) string {
	return BridgeSseData(map[string]any{
		"id":      s.ChatID,
		"object":  "chat.completion.chunk",
		"created": float64(s.CreatedAt),
		"model":   s.Model,
		"choices": []any{map[string]any{
			"index":         float64(0),
			"delta":         delta,
			"finish_reason": nil,
		}},
		"usage": nil,
	})
}

func (s *GeminiNativeChatStreamState) ensureRoleChunk(out *[]string) {
	if s.RoleSent {
		return
	}
	s.RoleSent = true
	*out = append(*out, s.chatSseChunk(map[string]any{"role": "assistant"}))
}

// CompleteGeminiNativeChatStream finishes the gemini-native -> chat stream.
func CompleteGeminiNativeChatStream(state *GeminiNativeChatStreamState) []string {
	if state.Completed || state.Failed {
		return nil
	}
	state.Completed = true
	output := []string{}
	state.ensureRoleChunk(&output)
	finishReason := state.StopReason
	if finishReason == "" {
		finishReason = "stop"
	}
	output = append(output, BridgeSseData(map[string]any{
		"id":      state.ChatID,
		"object":  "chat.completion.chunk",
		"created": float64(state.CreatedAt),
		"model":   state.Model,
		"choices": []any{map[string]any{
			"index":         float64(0),
			"delta":         map[string]any{},
			"finish_reason": finishReason,
		}},
		"usage": nil,
	}))
	if state.Usage != nil {
		output = append(output, BridgeSseData(map[string]any{
			"id":      state.ChatID,
			"object":  "chat.completion.chunk",
			"created": float64(state.CreatedAt),
			"model":   state.Model,
			"choices": []any{},
			"usage":   state.Usage,
		}))
	}
	output = append(output, "data: [DONE]\n\n")
	return output
}

func geminiFinishReasonToChatFinishReason(finishReason string) string {
	switch finishReason {
	case "MAX_TOKENS":
		return "length"
	case "SAFETY", "BLOCKED":
		return "content_filter"
	case "STOP", "FINISH_REASON_UNSPECIFIED", "":
		return "stop"
	default:
		return "stop"
	}
}

func geminiUsageToChatUsage(usage map[string]any) map[string]any {
	prompt := numberAsFloat64(usage["promptTokenCount"])
	completion := numberAsFloat64(usage["candidatesTokenCount"])
	total := numberAsFloat64(usage["totalTokenCount"])
	if total == 0 {
		total = prompt + completion
	}
	details := map[string]any{}
	if cached := numberAsFloat64(usage["cachedContentTokenCount"]); cached != 0 {
		details["cached_tokens"] = cached
	}
	if reasoning := numberAsFloat64(usage["thoughtsTokenCount"]); reasoning != 0 {
		details["reasoning_tokens"] = reasoning
	}
	output := map[string]any{
		"prompt_tokens":     prompt,
		"completion_tokens": completion,
		"total_tokens":      total,
	}
	if len(details) > 0 {
		output["prompt_tokens_details"] = details
	}
	return output
}

func finishReasonFromGeminiCandidate(candidate map[string]any) string {
	finish := bridgeStringValue(candidate["finishReason"])
	switch finish {
	case "MAX_TOKENS":
		return "length"
	case "SAFETY", "BLOCKED":
		return "content_filter"
	default:
		return "stop"
	}
}

// ---------------------------------------------------------------------------
// Buffered Anthropic usage mapping (kept for the legacy driver surface)
// ---------------------------------------------------------------------------

func transformAnthropicUsageToOpenAI(usage map[string]any) map[string]any {
	return anthropicUsageToChatUsage(usage, nil)
}

// ---------------------------------------------------------------------------
// Legacy helpers kept for the previous revision's tests / call surface
// ---------------------------------------------------------------------------

// TransformOpenAIAnthropicBridgeUpstreamResponse renders the buffered Anthropic
// Messages response as a chat completion payload for chat clients.
func TransformOpenAIAnthropicBridgeUpstreamResponse(response map[string]any, options BridgeTransformResponseOptions) map[string]any {
	if !options.Enabled || response == nil {
		return response
	}
	return AnthropicMessageToChatCompletion(response, options.Model)
}

// TransformSSEStreamToGeminiSSE transforms one buffered chat SSE event text
// into the Gemini GenerateContent SSE stream (legacy entry, kept for callers).
func TransformSSEStreamToGeminiSSE(eventText string, options BridgeTransformResponseOptions) string {
	if !options.Enabled {
		return eventText
	}
	state := NewGeminiChatStreamState(options.Model)
	output := ProcessChatCompletionsSseEventAsGemini(state, eventText)
	return strings.Join(output, "")
}

// TransformOpenAIChatToAnthropicStream transforms one buffered chat SSE event
// into Anthropic Messages SSE events (legacy entry, kept for callers).
func TransformOpenAIChatToAnthropicStream(eventText string, options BridgeTransformResponseOptions) string {
	if !options.Enabled {
		return eventText
	}
	state := NewAnthropicFromChatStreamState(options.Model)
	output := ProcessChatCompletionsSseEventAsAnthropic(state, eventText)
	return strings.Join(output, "")
}

// BuildGatewayUpstreamResponse builds a wrapped response for bridge transformation.
func BuildGatewayUpstreamResponse(status int, headers map[string]string, body []byte) map[string]any {
	return map[string]any{
		"status":  status,
		"headers": headers,
		"body":    string(body),
	}
}

// ExtractJSONObject extracts a JSON object from a body.
func ExtractJSONObject(body string) (map[string]any, error) {
	var result map[string]any
	if err := json.Unmarshal([]byte(body), &result); err != nil {
		return nil, err
	}
	return result, nil
}

// PrepareBridgeHeaders prepares request headers for protocol bridge.
func PrepareBridgeHeaders(headers map[string]string, reqPath string) map[string]string {
	result := make(map[string]string, len(headers)+1)
	for key, value := range headers {
		result[key] = value
	}
	result["Accept"] = "application/json"
	return result
}
