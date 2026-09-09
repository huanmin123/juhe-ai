package openaicompat

import (
	"strings"
	"time"
)

// B-4 openai-anthropic-bridge response face, responses target (D-149/D-157).
// Port of the archived Node openai-anthropic-bridge.ts responses half:
//
//	transformAnthropicMessagesJsonToResponsesJson /
//	transformAnthropicMessagesSseToResponsesSse /
//	processAnthropicEventAsResponses / responseSnapshot / failResponsesStream /
//	completeResponsesStream / anthropicUsageToResponsesUsage
//
// The requestPlan-gated branches (image generation streaming, structured
// output synthetic tool, local-runtime tool loop) belong to the executor
// slices that Go keeps out of the bridge request plan, so their response-side
// branches are not ported (registered deviation).

// AnthropicResponsesStreamState mirrors AnthropicStreamState for the
// OPENAI_RESPONSES_FAMILY target.
type AnthropicResponsesStreamState struct {
	ResponseID       string
	ResponseMessageID string
	MessageID        string
	Model            string
	CreatedAt        int64
	PreviousResponse string
	Started          bool
	NextOutputIndex  int64
	Blocks           map[int64]*anthropicResponsesStreamBlock
	OutputItems      []any
	Completed        bool
	Failed           bool
	TerminalReceived bool
	CompletionNotified bool
	StopReason       string
	Usage            map[string]any
	// OnCompleted mirrors notifyResponsesCompletion's handler; nil drops it.
	OnCompleted func(completion CodexBridgeCompletionPayload)
}

type anthropicResponsesStreamBlock struct {
	index     int64
	blockType string
	id        string
	name      string
	text      string
	inputJSON string
	outputIndex *int64
	done      bool
}

// NewAnthropicResponsesStreamState mirrors createAnthropicStreamState for the
// responses target.
func NewAnthropicResponsesStreamState(model, previousResponseID string) *AnthropicResponsesStreamState {
	suffix := codexBridgeIDSuffix()
	return &AnthropicResponsesStreamState{
		ResponseID:        "resp_anthropic_" + suffix,
		ResponseMessageID: "msg_anthropic_" + suffix,
		MessageID:         "msg_bridge_" + suffix,
		Model:             model,
		CreatedAt:         time.Now().Unix(),
		PreviousResponse:  previousResponseID,
		Blocks:            map[int64]*anthropicResponsesStreamBlock{},
	}
}

// ProcessAnthropicEventAsResponses mirrors processAnthropicEventAsResponses.
func (s *AnthropicResponsesStreamState) ProcessAnthropicEvent(rawEventText string) []string {
	if s.Completed || s.Failed {
		return nil
	}
	event := ParseBridgeSseEvent(rawEventText)
	if event.EventName == "error" || (event.Data != nil && bridgeStringValue(event.Data["type"]) == "error") {
		payload := openAIErrorFromAnthropicPayload(event.Data)
		errorObject := map[string]any{"message": "上游 Anthropic Messages 流式响应失败", "type": "upstream_error", "code": "upstream_error"}
		if record := bridgeObjectValue(payload["error"]); record != nil {
			errorObject = record
		}
		return s.FailResponsesStream(errorObject)
	}
	if event.DataParseError {
		return s.FailResponsesStream(map[string]any{
			"message": "上游 Anthropic Messages SSE 返回了无法解析的事件",
			"type":    "upstream_error",
			"code":    "upstream_stream_parse_error",
		})
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
				s.MessageID = id
				s.ResponseID = anthropicResponseIDFromAnthropicID(id)
				s.ResponseMessageID = "msg_" + safeBridgeIDSegment(anthropicResponseIDFromAnthropicID(id))
			}
			if model := bridgeStringValue(message["model"]); model != "" {
				s.Model = model
			}
			s.Usage = anthropicUsageToResponsesUsage(bridgeObjectValue(message["usage"]), s.Usage)
		}
		output = append(output, s.EnsureResponsesStreamStarted()...)
		return output
	case "content_block_start":
		index, _ := bridgeIntegerValue(data["index"])
		contentBlock := bridgeObjectValue(data["content_block"])
		block := anthropicResponsesStreamBlockFromContentBlock(index, contentBlock)
		s.Blocks[index] = block
		output = append(output, s.EnsureResponsesStreamStarted()...)
		if !s.shouldEmitResponsesStreamBlock(block) {
			return output
		}
		blockIndex := s.NextOutputIndex
		s.NextOutputIndex++
		block.outputIndex = &blockIndex
		switch block.blockType {
		case "text":
			output = append(output,
				BridgeSseEventText("response.output_item.added", map[string]any{
					"type": "response.output_item.added", "output_index": float64(blockIndex),
					"item": map[string]any{
						"id": s.ResponseMessageID, "type": "message", "status": "in_progress", "role": "assistant", "content": []any{},
					},
				}),
				BridgeSseEventText("response.content_part.added", map[string]any{
					"type": "response.content_part.added", "item_id": s.ResponseMessageID, "output_index": float64(blockIndex), "content_index": float64(0),
					"part": map[string]any{"type": "output_text", "text": "", "annotations": []any{}},
				}),
			)
		case "thinking":
			output = append(output, BridgeSseEventText("response.output_item.added", map[string]any{
				"type": "response.output_item.added", "output_index": float64(blockIndex),
				"item": map[string]any{
					"id": anthropicResponsesReasoningItemID(s.ResponseID, block.index), "type": "reasoning", "status": "in_progress", "summary": []any{},
				},
			}))
		case "tool_use":
			output = append(output, BridgeSseEventText("response.output_item.added", map[string]any{
				"type": "response.output_item.added", "output_index": float64(blockIndex),
				"item": anthropicResponsesFunctionCallItem(
					firstNonEmpty(block.id, int64ToText(block.index)),
					"in_progress",
					firstNonEmpty(block.id, "call_"+int64ToText(block.index)),
					block.name,
					"",
				),
			}))
			if block.inputJSON != "" {
				output = append(output, BridgeSseEventText("response.function_call_arguments.delta", map[string]any{
					"type": "response.function_call_arguments.delta", "response_id": s.ResponseID,
					"item_id": anthropicResponsesFunctionCallItemID(block), "output_index": float64(blockIndex), "delta": block.inputJSON,
				}))
			}
		}
		return output
	case "content_block_delta":
		index, _ := bridgeIntegerValue(data["index"])
		block := s.Blocks[index]
		delta := bridgeObjectValue(data["delta"])
		if delta == nil {
			delta = map[string]any{}
		}
		output = append(output, s.EnsureResponsesStreamStarted()...)
		switch bridgeStringValue(delta["type"]) {
		case "text_delta":
			text := bridgeStringValue(delta["text"])
			if block != nil {
				block.text += text
			}
			if block != nil && text != "" {
				output = append(output, BridgeSseEventText("response.output_text.delta", map[string]any{
					"type": "response.output_text.delta", "item_id": s.ResponseMessageID,
					"output_index": float64(derefInt64(block.outputIndex)), "content_index": float64(0), "delta": text,
				}))
			}
		case "input_json_delta":
			partial := bridgeStringValue(delta["partial_json"])
			if block != nil {
				block.inputJSON += partial
			}
			if block != nil && partial != "" && block.blockType == "tool_use" {
				output = append(output, BridgeSseEventText("response.function_call_arguments.delta", map[string]any{
					"type": "response.function_call_arguments.delta", "response_id": s.ResponseID,
					"item_id": anthropicResponsesFunctionCallItemID(block), "output_index": float64(derefInt64(block.outputIndex)), "delta": partial,
				}))
			}
		case "thinking_delta":
			text := firstNonEmpty(bridgeStringValue(delta["thinking"]), bridgeStringValue(delta["text"]))
			if block != nil {
				block.text += text
			}
		}
		return output
	case "content_block_stop":
		index, _ := bridgeIntegerValue(data["index"])
		if block := s.Blocks[index]; block != nil {
			output = append(output, s.CompleteResponsesBlock(block)...)
		}
		return output
	case "message_delta":
		delta := bridgeObjectValue(data["delta"])
		if delta != nil {
			if stopReason := bridgeStringValue(delta["stop_reason"]); stopReason != "" {
				s.StopReason = stopReason
			}
		}
		if usage := bridgeObjectValue(data["usage"]); usage != nil {
			s.Usage = anthropicUsageToResponsesUsage(usage, s.Usage)
		}
		return output
	case "message_stop":
		s.TerminalReceived = true
		output = append(output, s.CompleteResponsesStream()...)
		return output
	}
	return output
}

func derefInt64(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

func anthropicResponsesStreamBlockFromContentBlock(index int64, block map[string]any) *anthropicResponsesStreamBlock {
	state := &anthropicResponsesStreamBlock{blockType: "text"}
	if block != nil {
		state.blockType = firstNonEmpty(bridgeStringValue(block["type"]), "text")
		state.id = bridgeStringValue(block["id"])
		state.name = bridgeStringValue(block["name"])
		state.text = bridgeStringValue(block["text"])
		if state.blockType == "tool_use" {
			if input := bridgeObjectValue(block["input"]); input != nil && len(input) > 0 {
				state.inputJSON = bridgeJSONStringify(input)
			}
		}
	}
	state.index = index
	return state
}

// shouldEmitResponsesStreamBlock mirrors shouldEmitResponsesStreamBlock: the
// plain plan emits text and tool_use blocks; thinking items render through
// the reasoning summary surface.
func (s *AnthropicResponsesStreamState) shouldEmitResponsesStreamBlock(block *anthropicResponsesStreamBlock) bool {
	switch block.blockType {
	case "text", "thinking", "tool_use":
		return true
	}
	return false
}

func anthropicResponsesReasoningItemID(responseID string, index int64) string {
	return "rs_" + safeBridgeIDSegment(responseID+"_"+int64ToText(index))
}

func anthropicResponsesFunctionCallItemID(block *anthropicResponsesStreamBlock) string {
	return "fc_" + safeBridgeIDSegment(firstNonEmpty(block.id, int64ToText(block.index)))
}

func anthropicResponsesFunctionCallItem(idSegment, status, callID, name, argumentsText string) map[string]any {
	return map[string]any{
		"id":        "fc_" + safeBridgeIDSegment(idSegment),
		"type":      "function_call",
		"status":    status,
		"call_id":   callID,
		"name":      name,
		"arguments": argumentsText,
	}
}

// anthropicResponseIDFromAnthropicID mirrors responseIdFromAnthropicId.
func anthropicResponseIDFromAnthropicID(id string) string {
	source := id
	if source == "" {
		source = int64ToBase36(timeNowUnixMilli()) + "_" + codexBridgeRandom36(6)
	}
	return "resp_" + safeBridgeIDSegment(source)
}

// anthropicUsageToResponsesUsage mirrors anthropicUsageToResponsesUsage.
func anthropicUsageToResponsesUsage(usage, previous map[string]any) map[string]any {
	inputTokens := anthropicUsageInputTokensTotal(usage)
	if inputTokens == 0 {
		if value, ok := bridgeIntegerValue(previous["input_tokens"]); ok {
			inputTokens = value
		}
	}
	var outputTokens int64
	if value, ok := bridgeIntegerValue(usage["output_tokens"]); ok {
		outputTokens = value
	} else if value, ok := bridgeIntegerValue(previous["output_tokens"]); ok {
		outputTokens = value
	}
	reasoningTokens := int64(0)
	if details := bridgeObjectValue(usage["output_tokens_details"]); details != nil {
		reasoningTokens, _ = bridgeIntegerValue(details["thinking_tokens"])
	}
	if reasoningTokens == 0 {
		if _, ok := bridgeIntegerValue(usage["thinking_tokens"]); ok {
			reasoningTokens, _ = bridgeIntegerValue(usage["thinking_tokens"])
		}
	}
	if reasoningTokens == 0 {
		if details := bridgeObjectValue(previous["output_tokens_details"]); details != nil {
			reasoningTokens, _ = bridgeIntegerValue(details["reasoning_tokens"])
		}
	}
	cachedTokens := int64(0)
	if value, ok := bridgeIntegerValue(usage["cache_read_input_tokens"]); ok {
		cachedTokens = value
	} else if details := bridgeObjectValue(previous["input_tokens_details"]); details != nil {
		cachedTokens, _ = bridgeIntegerValue(details["cached_tokens"])
	}
	return map[string]any{
		"input_tokens": float64(inputTokens),
		"input_tokens_details": map[string]any{
			"cached_tokens": float64(cachedTokens),
		},
		"output_tokens": float64(outputTokens),
		"output_tokens_details": map[string]any{
			"reasoning_tokens": float64(reasoningTokens),
		},
		"total_tokens": float64(inputTokens + outputTokens),
	}
}

// EnsureResponsesStreamStarted mirrors ensureResponsesStreamStarted.
func (s *AnthropicResponsesStreamState) EnsureResponsesStreamStarted() []string {
	if s.Started {
		return nil
	}
	s.Started = true
	return []string{
		BridgeSseEventText("response.created", map[string]any{
			"type": "response.created", "response": s.ResponseSnapshot("in_progress"),
		}),
		BridgeSseEventText("response.in_progress", map[string]any{
			"type": "response.in_progress", "response": s.ResponseSnapshot("in_progress"),
		}),
	}
}

// CompleteResponsesBlock mirrors completeResponsesBlock.
func (s *AnthropicResponsesStreamState) CompleteResponsesBlock(block *anthropicResponsesStreamBlock) []string {
	if block.done {
		return nil
	}
	block.done = true
	if block.blockType == "thinking" {
		if strings.TrimSpace(block.text) == "" {
			return nil
		}
		item := anthropicResponsesReasoningItem(s.ResponseID, block.index, block.text)
		s.OutputItems = append(s.OutputItems, item)
		return []string{BridgeSseEventText("response.output_item.done", map[string]any{
			"type": "response.output_item.done", "output_index": float64(derefInt64(block.outputIndex)), "item": item,
		})}
	}
	if block.blockType == "tool_use" {
		item := anthropicResponsesFunctionCallItem(
			firstNonEmpty(block.id, int64ToText(block.index)),
			"completed",
			firstNonEmpty(block.id, "call_"+int64ToText(block.index)),
			block.name,
			firstNonEmpty(block.inputJSON, "{}"),
		)
		s.OutputItems = append(s.OutputItems, item)
		return []string{
			BridgeSseEventText("response.function_call_arguments.done", map[string]any{
				"type": "response.function_call_arguments.done", "response_id": s.ResponseID,
				"output_index": float64(derefInt64(block.outputIndex)), "item": item,
			}),
			BridgeSseEventText("response.output_item.done", map[string]any{
				"type": "response.output_item.done", "output_index": float64(derefInt64(block.outputIndex)), "item": item,
			}),
		}
	}
	item := map[string]any{
		"id":     s.ResponseMessageID,
		"type":   "message",
		"status": "completed",
		"role":   "assistant",
		"content": []any{map[string]any{
			"type":        "output_text",
			"text":        block.text,
			"annotations": []any{},
		}},
	}
	output := []string{
		BridgeSseEventText("response.output_text.done", map[string]any{
			"type": "response.output_text.done", "item_id": s.ResponseMessageID,
			"output_index": float64(derefInt64(block.outputIndex)), "content_index": float64(0), "text": block.text,
		}),
		BridgeSseEventText("response.content_part.done", map[string]any{
			"type": "response.content_part.done", "item_id": s.ResponseMessageID,
			"output_index": float64(derefInt64(block.outputIndex)), "content_index": float64(0),
			"part": map[string]any{"type": "output_text", "text": block.text, "annotations": []any{}},
		}),
		BridgeSseEventText("response.output_item.done", map[string]any{
			"type": "response.output_item.done", "output_index": float64(derefInt64(block.outputIndex)), "item": item,
		}),
	}
	s.OutputItems = append(s.OutputItems, item)
	return output
}

func anthropicResponsesReasoningItem(responseID string, index int64, text string) map[string]any {
	summary := []any{}
	if text != "" {
		summary = append(summary, map[string]any{"type": "summary_text", "text": text})
	}
	return map[string]any{
		"id":     anthropicResponsesReasoningItemID(responseID, index),
		"type":   "reasoning",
		"status": "completed",
		"summary": summary,
	}
}

// CompleteResponsesStream mirrors completeResponsesStream.
func (s *AnthropicResponsesStreamState) CompleteResponsesStream() []string {
	if s.Completed || s.Failed {
		return nil
	}
	output := s.EnsureResponsesStreamStarted()
	keys := make([]int64, 0, len(s.Blocks))
	for key := range s.Blocks {
		keys = append(keys, key)
	}
	for i := 0; i < len(keys); i++ {
		for j := i + 1; j < len(keys); j++ {
			if keys[j] < keys[i] {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
	}
	for _, key := range keys {
		output = append(output, s.CompleteResponsesBlock(s.Blocks[key])...)
	}
	s.Completed = true
	output = append(output, BridgeSseEventText("response.completed", map[string]any{
		"type": "response.completed", "response": s.ResponseSnapshot("completed"),
	}))
	return output
}

// FailResponsesStream mirrors failResponsesStream.
func (s *AnthropicResponsesStreamState) FailResponsesStream(errorObject map[string]any) []string {
	if s.Completed || s.Failed {
		return nil
	}
	output := s.EnsureResponsesStreamStarted()
	s.Failed = true
	failed := s.ResponseSnapshot("in_progress")
	failed["status"] = "failed"
	failed["completed_at"] = float64(time.Now().Unix())
	failed["error"] = errorObject
	output = append(output, BridgeSseEventText("response.failed", map[string]any{
		"type": "response.failed", "response": failed,
	}))
	return output
}

// ResponseSnapshot mirrors responseSnapshot.
func (s *AnthropicResponsesStreamState) ResponseSnapshot(status string) map[string]any {
	snapshot := map[string]any{
		"id":                   s.ResponseID,
		"object":               "response",
		"created_at":           float64(s.CreatedAt),
		"status":               status,
		"completed_at":         nil,
		"error":                nil,
		"incomplete_details":   nil,
		"instructions":         nil,
		"max_output_tokens":    nil,
		"model":                s.Model,
		"output":               []any{},
		"parallel_tool_calls":  false,
		"previous_response_id": codexBridgeOptionalString(s.PreviousResponse),
		"reasoning":            map[string]any{"effort": nil, "summary": nil},
		"store":                false,
		"temperature":          nil,
		"text":                 map[string]any{"format": map[string]any{"type": "text"}},
		"tool_choice":          "auto",
		"tools":                []any{},
		"top_p":                nil,
		"truncation":           "disabled",
		"usage":                nil,
		"user":                 nil,
		"metadata":             map[string]any{},
	}
	if status == "completed" {
		snapshot["completed_at"] = float64(time.Now().Unix())
		snapshot["output"] = s.OutputItems
		usage := s.Usage
		if usage == nil {
			usage = anthropicResponsesEstimatedUsage(s.OutputItems)
		}
		snapshot["usage"] = usage
	}
	return snapshot
}

// anthropicResponsesEstimatedUsage mirrors estimatedResponsesUsageFromStreamState.
func anthropicResponsesEstimatedUsage(outputItems []any) map[string]any {
	textParts := []string{}
	for _, item := range outputItems {
		itemMap := bridgeObjectValue(item)
		if itemMap == nil {
			continue
		}
		if bridgeStringValue(itemMap["type"]) == "function_call" {
			textParts = append(textParts, bridgeStringValue(itemMap["arguments"]))
			continue
		}
		content, _ := bridgeIsArray(itemMap["content"])
		for _, part := range content {
			if partMap := bridgeObjectValue(part); partMap != nil {
				textParts = append(textParts, bridgeStringValue(partMap["text"]))
			}
		}
	}
	outputTokens := int64(bridgeEstimateTokenCountFromText(strings.Join(textParts, "\n")))
	return map[string]any{
		"input_tokens": float64(0),
		"input_tokens_details": map[string]any{
			"cached_tokens": float64(0),
		},
		"output_tokens": float64(outputTokens),
		"output_tokens_details": map[string]any{
			"reasoning_tokens": float64(0),
		},
		"total_tokens": float64(outputTokens),
	}
}

// NotifyResponsesCompletion mirrors notifyResponsesCompletion.
func (s *AnthropicResponsesStreamState) NotifyResponsesCompletion() {
	if s.OnCompleted == nil || !s.Completed || s.CompletionNotified {
		return
	}
	s.CompletionNotified = true
	s.OnCompleted(CodexBridgeCompletionPayload{
		ResponseID: s.ResponseID,
		CreatedAt:  s.CreatedAt,
		Model:      s.Model,
		OutputItems: s.OutputItems,
		Response:    s.ResponseSnapshot("completed"),
	})
}

// FinishAnthropicResponsesStream mirrors the generator tail: fail the stream
// when the upstream ended without message_stop, then flush the completion.
func (s *AnthropicResponsesStreamState) FinishAnthropicResponsesStream() []string {
	if !s.TerminalReceived && !s.Completed && !s.Failed {
		return s.FailResponsesStream(map[string]any{
			"message": "上游 Anthropic Messages SSE 在正常结束事件前中断",
			"type":    "upstream_error",
			"code":    "upstream_stream_interrupted",
		})
	}
	return nil
}

// ---------------------------------------------------------------------------
// buffered variants
// ---------------------------------------------------------------------------

// TransformAnthropicMessagesJSONBufferToResponsesJSON mirrors
// transformAnthropicMessagesJsonToResponsesJson (plain plan, no requestPlan).
func TransformAnthropicMessagesJSONBufferToResponsesJSON(body []byte, model, previousResponseID string) []byte {
	value, err := ExtractJSONObject(string(body))
	if err != nil {
		value = map[string]any{}
	}
	if value == nil {
		value = map[string]any{}
	}
	state := NewAnthropicResponsesStreamState(model, previousResponseID)
	// The JSON path renders through the same output-item surface as the
	// stream path (anthropicContentBlocksToResponsesOutputItems).
	blocks, _ := bridgeIsArray(value["content"])
	for _, blockValue := range blocks {
		block := bridgeObjectValue(blockValue)
		if block == nil {
			continue
		}
		index := int64(len(state.Blocks))
		switch bridgeStringValue(block["type"]) {
		case "text":
			state.Blocks[index] = &anthropicResponsesStreamBlock{
				index: index, blockType: "text", text: bridgeStringValue(block["text"]),
			}
		case "thinking":
			if text := firstNonEmpty(bridgeStringValue(block["thinking"]), bridgeStringValue(block["text"])); text != "" {
				state.Blocks[index] = &anthropicResponsesStreamBlock{
					index: index, blockType: "thinking", text: text,
				}
			}
		case "tool_use":
			input := block["input"]
			inputJSON := ""
			if bridgeIsPlainObject(input) && len(input.(map[string]any)) > 0 {
				inputJSON = bridgeJSONStringify(input)
			}
			state.Blocks[index] = &anthropicResponsesStreamBlock{
				index: index, blockType: "tool_use",
				id: bridgeStringValue(block["id"]), name: bridgeStringValue(block["name"]), inputJSON: inputJSON,
			}
		}
	}
	if model := bridgeStringValue(value["model"]); model != "" {
		state.Model = model
	}
	if id := bridgeStringValue(value["id"]); id != "" {
		state.MessageID = id
		state.ResponseID = anthropicResponseIDFromAnthropicID(id)
		state.ResponseMessageID = "msg_" + safeBridgeIDSegment(state.ResponseID)
	}
	state.Usage = anthropicUsageToResponsesUsage(bridgeObjectValue(value["usage"]), nil)
	keys := make([]int64, 0, len(state.Blocks))
	for key := range state.Blocks {
		keys = append(keys, key)
	}
	for i := 0; i < len(keys); i++ {
		for j := i + 1; j < len(keys); j++ {
			if keys[j] < keys[i] {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
	}
	for _, key := range keys {
		block := state.Blocks[key]
		block.outputIndex = new(int64)
		*block.outputIndex = state.NextOutputIndex
		state.NextOutputIndex++
		state.CompleteResponsesBlock(block)
	}
	state.Completed = true
	state.NotifyResponsesCompletion()
	return []byte(bridgeJSONStringify(state.ResponseSnapshot("completed")))
}

// TransformAnthropicMessagesSseBufferToResponsesSse mirrors
// transformAnthropicMessagesSseToResponsesSse (plain plan).
func TransformAnthropicMessagesSseBufferToResponsesSse(body []byte, model, previousResponseID string) []byte {
	state := NewAnthropicResponsesStreamState(model, previousResponseID)
	output := strings.Builder{}
	for _, eventText := range codexBridgeSplitCompleteSseEvents(string(body)) {
		for _, rendered := range state.ProcessAnthropicEvent(eventText) {
			output.WriteString(rendered)
		}
		state.NotifyResponsesCompletion()
	}
	for _, rendered := range state.FinishAnthropicResponsesStream() {
		output.WriteString(rendered)
	}
	state.NotifyResponsesCompletion()
	return []byte(output.String())
}
