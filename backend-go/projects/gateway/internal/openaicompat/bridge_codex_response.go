package openaicompat

import (
	"bytes"
	"encoding/json"
	"io"
	"math/rand"
	"strings"
	"time"
)

// B-4 codex-responses-chat-bridge response face (D-149/D-157). Port of the
// archived Node codex-responses-chat-bridge.ts response half:
//
//	transformCodexResponsesChatBridgeUpstreamResponse — chat SSE ->
//	Responses SSE / JSON 回转
//	createCodexBridgeToolIdentity (codex-responses-chat-bridge-tool-identity.ts)
//	— fc_/ctc_ item id + call_id 身份构造
//
// The chat-side tool-name adapters (chatName -> responsesName) come from the
// request bridge (BuildCodexResponsesToChatCompletionsBody).

// CodexBridgeToolIdentityInput mirrors CodexBridgeToolIdentityInput.
type CodexBridgeToolIdentityInput struct {
	// AdapterKind is 'function' | 'custom'.
	AdapterKind string
	IDPrefix    string
	Index       int64
	// UpstreamCallId mirrors the optional upstream call id; empty falls back
	// to the synthesized call_<prefix>_<index>_<suffix>.
	UpstreamCallId string
	Suffix         string
}

// CodexBridgeToolIdentity mirrors CodexBridgeToolIdentity.
type CodexBridgeToolIdentity struct {
	ItemID   string
	CallID   string
	ItemType string // 'function_call' | 'custom_tool_call'
}

// CreateCodexBridgeToolIdentity mirrors createCodexBridgeToolIdentity: stable
// fc_/ctc_ item ids with a call id that preserves the upstream call id when
// present.
func CreateCodexBridgeToolIdentity(input CodexBridgeToolIdentityInput) CodexBridgeToolIdentity {
	itemPrefix := "fc"
	if input.AdapterKind == "custom" {
		itemPrefix = "ctc"
	}
	callID := input.UpstreamCallId
	if callID == "" {
		callID = "call_" + input.IDPrefix + "_" + int64ToText(input.Index) + "_" + input.Suffix
	}
	return CodexBridgeToolIdentity{
		ItemID:   itemPrefix + "_" + input.IDPrefix + "_" + int64ToText(input.Index) + "_" + input.Suffix,
		CallID:   callID,
		ItemType: codexBridgeToolCallItemType(input.AdapterKind),
	}
}

func codexBridgeToolCallItemType(adapterKind string) string {
	if adapterKind == "custom" {
		return "custom_tool_call"
	}
	return "function_call"
}

// CodexBridgeCompletionPayload mirrors the
// CodexResponsesChatBridgeCompletionHandler payload (chat-bridge-state.ts):
// the bridge calls it once per completed response so the context state slice
// can persist the output items. gatewaycodex adapts it onto its own
// CodexResponsesChatBridgeCompletion record.
type CodexBridgeCompletionPayload struct {
	ResponseID  string
	CreatedAt   int64
	Model       string
	OutputItems []any
	Response    map[string]any
}

// CodexBridgeCompletionHandler mirrors the handler function shape.
type CodexBridgeCompletionHandler func(completion CodexBridgeCompletionPayload)

// CodexResponsesChatBridgeFinishReasonFailure mirrors
// CodexResponsesChatBridgeFinishReasonFailure.
type CodexResponsesChatBridgeFinishReasonFailure struct {
	Code    string
	Message string
}

// CodexResponsesChatBridgeTransformOptions mirrors
// TransformCodexResponsesChatBridgeResponseOptions' consumed fields.
type CodexResponsesChatBridgeTransformOptions struct {
	// Enabled gates the transform (explicit mapping bridge).
	Enabled bool
	// DefaultModel fills the model when neither Model nor the upstream frame
	// carries one.
	DefaultModel string
	// FinishReasonFailures maps a chat finish_reason to a synthesized
	// response.failed.
	FinishReasonFailures map[string]CodexResponsesChatBridgeFinishReasonFailure
	// IDPrefix is the response/message/reasoning id prefix (Node idPrefix,
	// default 'chat_bridge').
	IDPrefix string
	// Model overrides the fallback model (Node options.model).
	Model string
	// PreviousResponseId is echoed into response.previous_response_id.
	PreviousResponseID string
	// ToolAdaptersByChatName carries the request-side tool adapters: chat
	// tool name -> responses tool name + kind + namespace.
	ToolAdaptersByChatName map[string]CodexBridgeToolAdapter
	// EstimatedInputTokens mirrors estimateResponsesRequestInputTokens.
	EstimatedInputTokens *int64
	// OnCompleted is invoked once the response completes (state persistence).
	OnCompleted func(completion CodexBridgeCompletionPayload)
}

// CodexBridgeToolAdapter mirrors CodexResponsesChatBridgeToolAdapter.
type CodexBridgeToolAdapter struct {
	Kind          string // 'function' | 'custom'
	ChatName      string
	ResponsesName string
	Namespace     string
}

// ---------------------------------------------------------------------------
// stream state
// ---------------------------------------------------------------------------

// CodexChatToResponsesState mirrors ChatToResponsesState: the buffered
// translation state for one chat SSE upstream -> Responses client response.
type CodexChatToResponsesState struct {
	ResponseID       string
	MessageID        string
	ReasoningID      string
	PreviousResponse string
	IDPrefix         string
	CreatedAt        int64
	Model            string
	Started          bool
	NextOutputIndex  int64
	ReasoningIndex   *int64
	ReasoningStarted bool
	ReasoningDone    bool
	ReasoningText    string
	TextIndex        *int64
	TextStarted      bool
	TextDone         bool
	OutputText       string
	OutputItems      []any
	ToolCalls        map[int64]*codexChatToolCallState
	PendingToolCalls map[int64]*codexPendingChatToolCallState
	ToolAdapters     map[string]CodexBridgeToolAdapter
	Usage            map[string]any
	// EstimatedInputTokens mirrors estimatedInputTokens.
	EstimatedInputTokens *int64
	FinishReasonFailures map[string]CodexResponsesChatBridgeFinishReasonFailure
	Completed            bool
	Failed               bool
	FailureCode          string
	FailureMessage       string
	TerminalReceived     bool
	CompletionNotified   bool
	// OnCompleted is the completion handler (nil = none).
	OnCompleted CodexBridgeCompletionHandler
}

type codexChatToolCallState struct {
	id          string
	itemType    string
	callID      string
	name        string
	arguments   string
	adapter     CodexBridgeToolAdapter
	outputIndex int64
	added       bool
	done        bool
}

type codexPendingChatToolCallState struct {
	upstreamCallID string
	name           string
	arguments      string
}

// NewCodexChatToResponsesState mirrors createChatToResponsesState.
func NewCodexChatToResponsesState(options CodexResponsesChatBridgeTransformOptions) *CodexChatToResponsesState {
	idPrefix := options.IDPrefix
	if idPrefix == "" {
		idPrefix = "chat_bridge"
	}
	suffix := codexBridgeIDSuffix()
	model := options.Model
	if model == "" {
		model = options.DefaultModel
	}
	adapters := options.ToolAdaptersByChatName
	if adapters == nil {
		adapters = map[string]CodexBridgeToolAdapter{}
	}
	failures := options.FinishReasonFailures
	if failures == nil {
		failures = map[string]CodexResponsesChatBridgeFinishReasonFailure{}
	}
	return &CodexChatToResponsesState{
		ResponseID:       "resp_" + idPrefix + "_" + suffix,
		MessageID:        "msg_" + idPrefix + "_" + suffix,
		ReasoningID:      "rs_" + idPrefix + "_" + suffix,
		PreviousResponse: options.PreviousResponseID,
		IDPrefix:         idPrefix,
		CreatedAt:        time.Now().Unix(),
		Model:            model,
		ToolCalls:        map[int64]*codexChatToolCallState{},
		PendingToolCalls: map[int64]*codexPendingChatToolCallState{},
		ToolAdapters:     adapters,
		EstimatedInputTokens: options.EstimatedInputTokens,
		FinishReasonFailures: failures,
		OnCompleted:          options.OnCompleted,
	}
}

// codexBridgeIDSuffix mirrors `${Date.now().toString(36)}_${random36}`.
func codexBridgeIDSuffix() string {
	return int64ToBase36(time.Now().UnixMilli()) + "_" + codexBridgeRandom36(6)
}

func int64ToBase36(value int64) string {
	if value == 0 {
		return "0"
	}
	digits := []byte{}
	for value > 0 {
		digits = append(digits, base36Alphabet[value%36])
		value /= 36
	}
	for i, j := 0, len(digits)-1; i < j; i, j = i+1, j-1 {
		digits[i], digits[j] = digits[j], digits[i]
	}
	return string(digits)
}

const base36Alphabet = "0123456789abcdefghijklmnopqrstuvwxyz"

func codexBridgeRandom36(length int) string {
	const alphabet = "0123456789abcdefghijklmnopqrstuvwxyz"
	out := make([]byte, length)
	for i := range out {
		out[i] = alphabet[rand.Intn(len(alphabet))]
	}
	return string(out)
}

// ---------------------------------------------------------------------------
// SSE event processing
// ---------------------------------------------------------------------------

// ProcessChatSseEventToResponses mirrors processChatSseEvent: one chat SSE
// event text in, zero or more Responses SSE event strings out.
func (s *CodexChatToResponsesState) ProcessChatSseEvent(rawEventText string) []string {
	if s.Completed || s.Failed {
		return nil
	}
	event := ParseBridgeSseEvent(rawEventText)
	if event.DataText == "[DONE]" {
		s.TerminalReceived = true
		if len(s.PendingToolCalls) > 0 {
			return s.FailResponsesStream("上游 Chat SSE 返回了未在请求工具声明中解析的工具调用", "codex_bridge_unknown_tool_call")
		}
		return s.CompleteResponsesStream()
	}
	if event.DataParseError {
		return s.FailResponsesStream("上游 Chat SSE 返回了无法解析的事件", "upstream_stream_parse_error")
	}
	if event.EventName == "error" || event.EventType == "error" {
		failure := codexUpstreamChatSseErrorFailure(event.Data)
		return s.FailResponsesStream(failure.Message, failure.Code)
	}
	data := event.Data
	if data == nil {
		return nil
	}
	output := []string{}
	appendOutput := func(events []string) {
		if len(events) == 0 {
			return
		}
		output = append(output, s.EnsureResponsesStreamStarted()...)
		output = append(output, events...)
	}
	if model := bridgeStringValue(data["model"]); model != "" {
		s.Model = model
	}
	if usage := bridgeObjectValue(data["usage"]); usage != nil {
		s.Usage = codexChatUsageToResponsesUsage(usage)
	}
	choices, _ := bridgeIsArray(data["choices"])
	for _, item := range choices {
		choice := bridgeObjectValue(item)
		if choice == nil {
			continue
		}
		if delta := bridgeObjectValue(choice["delta"]); delta != nil {
			if reasoning := firstNonEmpty(bridgeStringValue(delta["reasoning_content"]), bridgeStringValue(delta["reasoning"])); reasoning != "" {
				appendOutput(s.AppendResponsesReasoningDelta(reasoning))
			}
			if text := firstNonEmpty(bridgeStringValue(delta["content"]), bridgeStringValue(delta["refusal"])); text != "" {
				appendOutput(s.AppendResponsesTextDelta(text))
			}
			toolCalls, _ := bridgeIsArray(delta["tool_calls"])
			for _, toolCallValue := range toolCalls {
				appendOutput(s.AppendResponsesToolCallDelta(toolCallValue))
			}
		}
		if finishReason, ok := choice["finish_reason"].(string); ok {
			s.TerminalReceived = true
			if len(s.PendingToolCalls) > 0 {
				output = append(output, s.FailResponsesStream("上游 Chat SSE 返回了未在请求工具声明中解析的工具调用", "codex_bridge_unknown_tool_call")...)
				continue
			}
			if failure, has := s.FinishReasonFailures[finishReason]; has {
				appendOutput(s.CompleteOpenOutputItems())
				output = append(output, s.FailResponsesStream(failure.Message, failure.Code)...)
				continue
			}
			appendOutput(s.CompleteOpenOutputItems())
			output = append(output, s.CompleteResponsesStream()...)
		}
	}
	return output
}

// EnsureResponsesStreamStarted mirrors ensureResponsesStreamStarted.
func (s *CodexChatToResponsesState) EnsureResponsesStreamStarted() []string {
	if s.Started {
		return nil
	}
	s.Started = true
	return []string{
		BridgeSseEventText("response.created", map[string]any{
			"type":     "response.created",
			"response": s.ResponseSnapshot("in_progress"),
		}),
		BridgeSseEventText("response.in_progress", map[string]any{
			"type":     "response.in_progress",
			"response": s.ResponseSnapshot("in_progress"),
		}),
	}
}

// AppendResponsesReasoningDelta mirrors appendResponsesReasoningDelta.
func (s *CodexChatToResponsesState) AppendResponsesReasoningDelta(text string) []string {
	output := []string{}
	if !s.ReasoningStarted {
		s.ReasoningStarted = true
		index := s.NextOutputIndex
		s.NextOutputIndex++
		s.ReasoningIndex = &index
		output = append(output, BridgeSseEventText("response.output_item.added", map[string]any{
			"type":         "response.output_item.added",
			"output_index": float64(index),
			"item": map[string]any{
				"id":     s.ReasoningID,
				"type":   "reasoning",
				"status": "in_progress",
				"summary": []any{},
			},
		}))
	}
	s.ReasoningText += text
	index := int64(0)
	if s.ReasoningIndex != nil {
		index = *s.ReasoningIndex
	}
	output = append(output, BridgeSseEventText("response.reasoning_summary_text.delta", map[string]any{
		"type":          "response.reasoning_summary_text.delta",
		"item_id":       s.ReasoningID,
		"output_index":  float64(index),
		"summary_index": float64(0),
		"delta":         text,
	}))
	return output
}

// AppendResponsesTextDelta mirrors appendResponsesTextDelta.
func (s *CodexChatToResponsesState) AppendResponsesTextDelta(text string) []string {
	output := []string{}
	if !s.TextStarted {
		s.TextStarted = true
		index := s.NextOutputIndex
		s.NextOutputIndex++
		s.TextIndex = &index
		output = append(output, BridgeSseEventText("response.output_item.added", map[string]any{
			"type":         "response.output_item.added",
			"output_index": float64(index),
			"item": map[string]any{
				"id":     s.MessageID,
				"type":   "message",
				"status": "in_progress",
				"role":   "assistant",
				"content": []any{},
			},
		}))
		output = append(output, BridgeSseEventText("response.content_part.added", map[string]any{
			"type":          "response.content_part.added",
			"item_id":       s.MessageID,
			"output_index":  float64(index),
			"content_index": float64(0),
			"part": map[string]any{
				"type":        "output_text",
				"text":        "",
				"annotations": []any{},
			},
		}))
	}
	s.OutputText += text
	index := int64(0)
	if s.TextIndex != nil {
		index = *s.TextIndex
	}
	output = append(output, BridgeSseEventText("response.output_text.delta", map[string]any{
		"type":          "response.output_text.delta",
		"item_id":       s.MessageID,
		"output_index":  float64(index),
		"content_index": float64(0),
		"delta":         text,
	}))
	return output
}

// AppendResponsesToolCallDelta mirrors appendResponsesToolCallDelta: pending
// chat tool calls accumulate until a declared adapter resolves, then the
// bridge identity (fc_/ctc_) is minted through CreateCodexBridgeToolIdentity.
func (s *CodexChatToResponsesState) AppendResponsesToolCallDelta(value any) []string {
	toolCall := bridgeObjectValue(value)
	if toolCall == nil {
		return nil
	}
	index := int64(0)
	if parsed, ok := bridgeIntegerValue(toolCall["index"]); ok {
		index = parsed
	}
	fn := bridgeObjectValue(toolCall["function"])
	chatName := ""
	argumentsDelta := ""
	if fn != nil {
		chatName = bridgeStringValue(fn["name"])
		argumentsDelta = bridgeStringValue(fn["arguments"])
	}
	if existing := s.ToolCalls[index]; existing != nil {
		if argumentsDelta != "" {
			existing.arguments += argumentsDelta
		}
		return s.EmitToolCallAdded(existing)
	}

	pending := s.PendingToolCalls[index]
	if pending == nil {
		pending = &codexPendingChatToolCallState{}
	}
	if pending.upstreamCallID == "" {
		pending.upstreamCallID = bridgeStringValue(toolCall["id"])
	}
	if chatName != "" {
		pending.name = codexMergeChatToolName(pending.name, chatName)
	}
	if argumentsDelta != "" {
		pending.arguments += argumentsDelta
	}
	adapter, has := s.ToolAdapters[pending.name]
	if pending.name == "" || !has {
		s.PendingToolCalls[index] = pending
		return nil
	}

	identity := CreateCodexBridgeToolIdentity(CodexBridgeToolIdentityInput{
		AdapterKind:    adapter.Kind,
		IDPrefix:       s.IDPrefix,
		Index:          index,
		UpstreamCallId: pending.upstreamCallID,
		Suffix:         codexBridgeIDSuffix(),
	})
	toolCallState := &codexChatToolCallState{
		id:          identity.ItemID,
		itemType:    identity.ItemType,
		callID:      identity.CallID,
		name:        adapter.ResponsesName,
		arguments:   pending.arguments,
		adapter:     adapter,
		outputIndex: s.NextOutputIndex,
	}
	s.NextOutputIndex++
	delete(s.PendingToolCalls, index)
	s.ToolCalls[index] = toolCallState
	return s.EmitToolCallAdded(toolCallState)
}

// EmitToolCallAdded mirrors emitToolCallAdded.
func (s *CodexChatToResponsesState) EmitToolCallAdded(toolCall *codexChatToolCallState) []string {
	if toolCall.added {
		return nil
	}
	toolCall.added = true
	return []string{BridgeSseEventText("response.output_item.added", map[string]any{
		"type":         "response.output_item.added",
		"output_index": float64(toolCall.outputIndex),
		"item":         s.toolCallInProgressItem(toolCall),
	})}
}

func codexMergeChatToolName(current, incoming string) string {
	if current == "" || current == incoming || strings.HasSuffix(current, incoming) {
		if current == "" {
			return incoming
		}
		return current
	}
	return current + incoming
}

// CompleteOpenOutputItems mirrors completeOpenOutputItems.
func (s *CodexChatToResponsesState) CompleteOpenOutputItems() []string {
	output := []string{}
	type completion struct {
		outputIndex int64
		complete    func()
	}
	completions := []completion{}
	if s.TextStarted && !s.TextDone {
		completions = append(completions, completion{outputIndex: s.textIndexOrZero(), complete: func() { s.CompleteTextOutputItem(&output) }})
	}
	if s.ReasoningStarted && !s.ReasoningDone {
		completions = append(completions, completion{outputIndex: s.reasoningIndexOrZero(), complete: func() { s.CompleteReasoningOutputItem(&output) }})
	}
	for _, toolCall := range s.ToolCalls {
		if toolCall.done {
			continue
		}
		toolCall := toolCall
		completions = append(completions, completion{outputIndex: toolCall.outputIndex, complete: func() { s.CompleteToolCallOutputItem(&output, toolCall) }})
	}
	for i := 0; i < len(completions); i++ {
		for j := i + 1; j < len(completions); j++ {
			if completions[j].outputIndex < completions[i].outputIndex {
				completions[i], completions[j] = completions[j], completions[i]
			}
		}
	}
	for _, item := range completions {
		item.complete()
	}
	return output
}

func (s *CodexChatToResponsesState) textIndexOrZero() int64 {
	if s.TextIndex != nil {
		return *s.TextIndex
	}
	return 0
}

func (s *CodexChatToResponsesState) reasoningIndexOrZero() int64 {
	if s.ReasoningIndex != nil {
		return *s.ReasoningIndex
	}
	return 0
}

// CompleteTextOutputItem mirrors completeTextOutputItem.
func (s *CodexChatToResponsesState) CompleteTextOutputItem(output *[]string) {
	s.TextDone = true
	item := map[string]any{
		"id":     s.MessageID,
		"type":   "message",
		"status": "completed",
		"role":   "assistant",
		"content": []any{map[string]any{
			"type":        "output_text",
			"text":        s.OutputText,
			"annotations": []any{},
		}},
	}
	*output = append(*output, BridgeSseEventText("response.output_text.done", map[string]any{
		"type":          "response.output_text.done",
		"item_id":       s.MessageID,
		"output_index":  float64(s.textIndexOrZero()),
		"content_index": float64(0),
		"text":          s.OutputText,
	}))
	*output = append(*output, BridgeSseEventText("response.content_part.done", map[string]any{
		"type":          "response.content_part.done",
		"item_id":       s.MessageID,
		"output_index":  float64(s.textIndexOrZero()),
		"content_index": float64(0),
		"part": map[string]any{
			"type":        "output_text",
			"text":        s.OutputText,
			"annotations": []any{},
		},
	}))
	*output = append(*output, BridgeSseEventText("response.output_item.done", map[string]any{
		"type":         "response.output_item.done",
		"output_index": float64(s.textIndexOrZero()),
		"item":         item,
	}))
	s.OutputItems = append(s.OutputItems, item)
}

// CompleteReasoningOutputItem mirrors completeReasoningOutputItem.
func (s *CodexChatToResponsesState) CompleteReasoningOutputItem(output *[]string) {
	s.ReasoningDone = true
	item := map[string]any{
		"id":     s.ReasoningID,
		"type":   "reasoning",
		"status": "completed",
		"summary": []any{map[string]any{
			"type": "summary_text",
			"text": s.ReasoningText,
		}},
		"encrypted_content": nil,
	}
	*output = append(*output, BridgeSseEventText("response.output_item.done", map[string]any{
		"type":         "response.output_item.done",
		"output_index": float64(s.reasoningIndexOrZero()),
		"item":         item,
	}))
	s.OutputItems = append(s.OutputItems, item)
}

// CompleteToolCallOutputItem mirrors completeToolCallOutputItem.
func (s *CodexChatToResponsesState) CompleteToolCallOutputItem(output *[]string, toolCall *codexChatToolCallState) {
	if !toolCall.added {
		toolCall.added = true
		*output = append(*output, BridgeSseEventText("response.output_item.added", map[string]any{
			"type":         "response.output_item.added",
			"output_index": float64(toolCall.outputIndex),
			"item":         s.toolCallInProgressItem(toolCall),
		}))
	}
	toolCall.done = true
	item := s.completedToolCallItem(toolCall)
	*output = append(*output, BridgeSseEventText("response.output_item.done", map[string]any{
		"type":         "response.output_item.done",
		"output_index": float64(toolCall.outputIndex),
		"item":         item,
	}))
	s.OutputItems = append(s.OutputItems, item)
}

func (s *CodexChatToResponsesState) toolCallInProgressItem(toolCall *codexChatToolCallState) map[string]any {
	if toolCall.itemType == "custom_tool_call" {
		item := map[string]any{
			"id":     toolCall.id,
			"type":   "custom_tool_call",
			"status": "in_progress",
			"call_id": toolCall.callID,
			"name":   toolCall.name,
			"input":  "",
		}
		if toolCall.adapter.Namespace != "" {
			item["namespace"] = toolCall.adapter.Namespace
		}
		return item
	}
	item := map[string]any{
		"id":        toolCall.id,
		"type":      "function_call",
		"status":    "in_progress",
		"call_id":   toolCall.callID,
		"name":      toolCall.name,
		"arguments": "",
	}
	if toolCall.adapter.Namespace != "" {
		item["namespace"] = toolCall.adapter.Namespace
	}
	return item
}

func (s *CodexChatToResponsesState) completedToolCallItem(toolCall *codexChatToolCallState) map[string]any {
	if toolCall.itemType == "custom_tool_call" {
		item := map[string]any{
			"id":      toolCall.id,
			"type":    "custom_tool_call",
			"status":  "completed",
			"call_id": toolCall.callID,
			"name":    toolCall.name,
			"input":   codexCustomToolInputFromChatArguments(toolCall.arguments, toolCall.adapter.ResponsesName),
		}
		if toolCall.adapter.Namespace != "" {
			item["namespace"] = toolCall.adapter.Namespace
		}
		return item
	}
	item := map[string]any{
		"id":        toolCall.id,
		"type":      "function_call",
		"status":    "completed",
		"call_id":   toolCall.callID,
		"name":      toolCall.name,
		"arguments": toolCall.arguments,
	}
	if toolCall.adapter.Namespace != "" {
		item["namespace"] = toolCall.adapter.Namespace
	}
	return item
}

// codexCustomToolInputFromChatArguments mirrors customToolInputFromChatArguments:
// unwrap {input} (or the apply_patch structured files payload), else pass
// through the raw arguments text.
func codexCustomToolInputFromChatArguments(argumentsText, toolName string) string {
	if argumentsText != "" {
		var parsed any
		if err := json.Unmarshal([]byte(argumentsText), &parsed); err == nil {
			if record, ok := parsed.(map[string]any); ok {
				if input, ok := record["input"].(string); ok && input != "" {
					return input
				}
				if toolName == "apply_patch" {
					if patch := codexApplyPatchInputFromStructuredFiles(record["files"]); patch != "" {
						return patch
					}
				}
			}
		}
	}
	return argumentsText
}

func codexApplyPatchInputFromStructuredFiles(value any) string {
	items, ok := bridgeIsArray(value)
	if !ok {
		return ""
	}
	hunks := []string{}
	for _, item := range items {
		record := bridgeObjectValue(item)
		if record == nil {
			continue
		}
		path := bridgeStringValue(record["path"])
		content, hasContent := record["content"].(string)
		if path == "" || !hasContent || strings.Contains(path, "\n") || strings.Contains(path, "\r") {
			continue
		}
		hunks = append(hunks, "*** Add File: "+path)
		lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
		for _, line := range lines {
			hunks = append(hunks, "+"+line)
		}
	}
	if len(hunks) == 0 {
		return ""
	}
	return strings.Join(append(hunks, "*** End Patch", ""), "\n")
}

// CompleteResponsesStream mirrors completeResponsesStream.
func (s *CodexChatToResponsesState) CompleteResponsesStream() []string {
	if s.Completed || s.Failed {
		return nil
	}
	output := s.EnsureResponsesStreamStarted()
	output = append(output, s.CompleteOpenOutputItems()...)
	s.Completed = true
	output = append(output, BridgeSseEventText("response.completed", map[string]any{
		"type":     "response.completed",
		"response": s.ResponseSnapshotCompleted(),
	}))
	return output
}

// FailResponsesStream mirrors failResponsesStream.
func (s *CodexChatToResponsesState) FailResponsesStream(message, code string) []string {
	if s.Completed || s.Failed {
		return nil
	}
	var output []string
	if s.Started {
		output = s.EnsureResponsesStreamStarted()
	}
	s.Failed = true
	s.FailureMessage = message
	s.FailureCode = code
	output = append(output, BridgeSseEventText("response.failed", map[string]any{
		"type":     "response.failed",
		"response": s.ResponseFailedSnapshot(message, code),
	}))
	return output
}

// ResponseSnapshot mirrors responseSnapshot (in_progress shape).
func (s *CodexChatToResponsesState) ResponseSnapshot(status string) map[string]any {
	return map[string]any{
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
}

// ResponseSnapshotCompleted mirrors responseSnapshot(state, 'completed',
// outputItems) with the usage and completed_at filled in.
func (s *CodexChatToResponsesState) ResponseSnapshotCompleted() map[string]any {
	snapshot := s.ResponseSnapshot("in_progress")
	snapshot["status"] = "completed"
	snapshot["completed_at"] = float64(time.Now().Unix())
	snapshot["output"] = s.OutputItems
	snapshot["usage"] = s.CompletedResponsesUsage()
	return snapshot
}

// ResponseFailedSnapshot mirrors responseFailedSnapshot.
func (s *CodexChatToResponsesState) ResponseFailedSnapshot(message, code string) map[string]any {
	snapshot := s.ResponseSnapshot("in_progress")
	snapshot["status"] = "failed"
	snapshot["completed_at"] = float64(time.Now().Unix())
	snapshot["error"] = map[string]any{"code": code, "message": message}
	return snapshot
}

func codexBridgeOptionalString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

// NotifyCodexResponsesChatBridgeCompletion mirrors
// notifyCodexResponsesChatBridgeCompletion.
func (s *CodexChatToResponsesState) NotifyCodexResponsesChatBridgeCompletion() {
	if s.OnCompleted == nil || !s.Completed || s.CompletionNotified {
		return
	}
	s.CompletionNotified = true
	s.OnCompleted(CodexBridgeCompletionPayload{
		ResponseID: s.ResponseID,
		CreatedAt:  s.CreatedAt,
		Model:      s.Model,
		OutputItems: s.OutputItems,
		Response:    s.ResponseSnapshotCompleted(),
	})
}

// Finish mirrors the generator tail: fail the stream when the upstream SSE
// ended without a terminal event, then flush the completion notification.
func (s *CodexChatToResponsesState) Finish() []string {
	if !s.TerminalReceived && !s.Completed && !s.Failed {
		return s.FailResponsesStream("上游 Chat SSE 在正常结束事件前中断", "upstream_stream_interrupted")
	}
	return nil
}

// CompletedResponsesUsage mirrors completedResponsesUsage: the observed chat
// usage, else the estimated shape (input from the request estimate, output
// from the emitted text/tool arguments).
func (s *CodexChatToResponsesState) CompletedResponsesUsage() map[string]any {
	if s.Usage != nil {
		return s.Usage
	}
	inputTokens := int64(0)
	if s.EstimatedInputTokens != nil {
		inputTokens = *s.EstimatedInputTokens
	}
	outputTokens := s.EstimatedBridgeOutputTokens()
	reasoningTokens := int64(bridgeEstimateTokenCountFromText(s.ReasoningText))
	return map[string]any{
		"input_tokens": float64(inputTokens),
		"input_tokens_details": map[string]any{
			"cached_tokens": float64(0),
		},
		"output_tokens": float64(outputTokens),
		"output_tokens_details": map[string]any{
			"reasoning_tokens": float64(reasoningTokens),
		},
		"total_tokens": float64(inputTokens + outputTokens),
	}
}

// EstimatedBridgeOutputTokens mirrors estimatedBridgeOutputTokens.
func (s *CodexChatToResponsesState) EstimatedBridgeOutputTokens() int64 {
	textParts := []string{s.OutputText, s.ReasoningText}
	for _, toolCall := range s.ToolCalls {
		textParts = append(textParts, toolCall.name, toolCall.arguments)
	}
	estimated := int64(bridgeEstimateTokenCountFromText(strings.Join(textParts, "\n")))
	if estimated < 0 {
		return 0
	}
	return estimated
}

// codexUpstreamChatSseErrorFailure mirrors upstreamChatSseErrorFailure.
func codexUpstreamChatSseErrorFailure(data map[string]any) CodexResponsesChatBridgeFinishReasonFailure {
	errorObject := bridgeObjectValue(data["error"])
	if errorObject == nil {
		errorObject = data
	}
	if errorObject == nil {
		return CodexResponsesChatBridgeFinishReasonFailure{Message: "上游 Chat SSE 返回错误事件", Code: "upstream_error"}
	}
	message := firstNonEmpty(bridgeStringValue(errorObject["message"]), "上游 Chat SSE 返回错误事件")
	code := firstNonEmpty(bridgeStringValue(errorObject["code"]), bridgeStringValue(errorObject["type"]), "upstream_error")
	return CodexResponsesChatBridgeFinishReasonFailure{Message: message, Code: code}
}

// codexChatUsageToResponsesUsage mirrors chatUsageToResponsesUsage.
func codexChatUsageToResponsesUsage(usage map[string]any) map[string]any {
	inputTokens, _ := bridgeIntegerValue(usage["prompt_tokens"])
	outputTokens, _ := bridgeIntegerValue(usage["completion_tokens"])
	totalTokens, hasTotal := bridgeIntegerValue(usage["total_tokens"])
	if !hasTotal {
		totalTokens = inputTokens + outputTokens
	}
	cachedTokens := int64(0)
	if details := bridgeObjectValue(usage["prompt_tokens_details"]); details != nil {
		cachedTokens, _ = bridgeIntegerValue(details["cached_tokens"])
	}
	reasoningTokens := int64(0)
	if details := bridgeObjectValue(usage["completion_tokens_details"]); details != nil {
		reasoningTokens, _ = bridgeIntegerValue(details["reasoning_tokens"])
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
		"total_tokens": float64(totalTokens),
	}
}

// ---------------------------------------------------------------------------
// buffered readers (chat SSE -> Responses SSE / JSON)
// ---------------------------------------------------------------------------

// TransformChatCompletionsSseBufferToResponsesSse consumes a buffered chat
// SSE body and renders the Responses SSE event stream.
func TransformChatCompletionsSseBufferToResponsesSse(body []byte, options CodexResponsesChatBridgeTransformOptions) []byte {
	var output bytes.Buffer
	_ = PumpChatCompletionsSseToResponsesSse(bytes.NewReader(body), &output, options)
	return output.Bytes()
}

// PumpChatCompletionsSseToResponsesSse 是 TransformChatCompletionsSseBuffer-
// ToResponsesSse 的逐事件增量变体：经 PumpBridgeSseTransform 按事件边界增量
// 消费 src，每个事件渲染后立即写入 dst（上游未 EOF 时下游已可读到已转换的
// 首事件），事件处理与收尾（Finish + completion 通知）语义与 buffer 版本
// 逐行一致。
func PumpChatCompletionsSseToResponsesSse(src io.Reader, dst io.Writer, options CodexResponsesChatBridgeTransformOptions) error {
	state := NewCodexChatToResponsesState(options)
	return PumpBridgeSseTransform(src, dst, func(eventText string) []string {
		rendered := state.ProcessChatSseEvent(eventText)
		state.NotifyCodexResponsesChatBridgeCompletion()
		return rendered
	}, func() []string {
		rendered := state.Finish()
		state.NotifyCodexResponsesChatBridgeCompletion()
		return rendered
	})
}

// TransformChatCompletionsSseBufferToResponsesJSON consumes a buffered chat
// SSE body and renders the single completed (or failed) Responses JSON
// payload.
func TransformChatCompletionsSseBufferToResponsesJSON(body []byte, options CodexResponsesChatBridgeTransformOptions) []byte {
	state := NewCodexChatToResponsesState(options)
	for _, eventText := range codexBridgeSplitCompleteSseEvents(string(body)) {
		state.ProcessChatSseEvent(eventText)
		state.NotifyCodexResponsesChatBridgeCompletion()
	}
	state.Finish()
	state.NotifyCodexResponsesChatBridgeCompletion()
	if state.Failed {
		return []byte(bridgeJSONStringify(state.ResponseFailedSnapshot(
			firstNonEmpty(state.FailureMessage, "上游 Chat SSE 返回错误"),
			firstNonEmpty(state.FailureCode, "upstream_error"),
		)))
	}
	return []byte(bridgeJSONStringify(state.ResponseSnapshotCompleted()))
}

func codexBridgeSplitCompleteSseEvents(input string) []string {
	events := []string{}
	rest := input
	for {
		index, length := IndexBridgeSseBoundary(rest)
		if index < 0 {
			break
		}
		events = append(events, rest[:index+length])
		rest = rest[index+length:]
	}
	if strings.TrimSpace(rest) != "" {
		events = append(events, rest)
	}
	return events
}

// EstimateCodexResponsesRequestInputTokens mirrors
// estimateResponsesRequestInputTokens for the parsed request body.
func EstimateCodexResponsesRequestInputTokens(body map[string]any) int64 {
	if body == nil {
		return 0
	}
	return int64(bridgeEstimateTokenCountFromText(bridgeJSONStringify(body)))
}
