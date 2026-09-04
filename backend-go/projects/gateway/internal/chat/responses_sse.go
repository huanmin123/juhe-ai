package chat

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"
)

// OpenAI Responses SSE collection ported from chat-responses-sse.ts. Event
// mapping, budgets, oversize image-event spooling (in-memory up to 64 MiB
// instead of a temp file) and the base64 redaction scanner keep Node behavior.

// ChatResponsesEvent mirrors the ChatResponsesEvent union; Type selects the
// union member and the matching payload field carries its data.
type ChatResponsesEvent struct {
	Type     string // text_delta|reasoning_delta|reasoning_completed|tool_started|tool_updated|tool_completed|image_started|image_updated|image_completed|image_failed|completed|failed
	Delta    string
	Item     map[string]any
	Response map[string]any
	Error    map[string]any
}

// ChatResponsesCollectionResult mirrors ChatResponsesCollectionResult.
type ChatResponsesCollectionResult struct {
	Content           string
	InputTokens       *int64
	OutputTokens      *int64
	ToolCalls         []ChatToolCall
	ContinuationItems []any
}

const (
	responsesMaxEventBytes     = 64 * 1024
	responsesMaxImageEventByte = 64 * 1024 * 1024
	responsesMaxAuxiliaryBytes = 192 * 1024
	responsesSpoolMetaMaxBytes = 512 * 1024
)

// ImageResultSink receives the base64 payload chunks of one completed image.
type ImageResultSink func(callID string, revisedPrompt string, base64Chunks []string) error

// CollectChatResponsesSse mirrors collectChatResponsesSse. onImageResult is
// optional; when nil image payloads are dropped.
func CollectChatResponsesSse(stream io.Reader, maxBytes, maxEvents int, onEvent func(ChatResponsesEvent) error, onImageResult ImageResultSink) (ChatResponsesCollectionResult, error) {
	result := ChatResponsesCollectionResult{ToolCalls: []ChatToolCall{}, ContinuationItems: []any{}}
	if maxEvents <= 0 {
		maxEvents = defaultMaxSSEEvents
	}
	raw, err := io.ReadAll(stream)
	if err != nil {
		return result, err
	}
	if !utf8.Valid(raw) {
		return result, errors.New("上游返回了无效的 SSE JSON")
	}
	var (
		content           strings.Builder
		completed         bool
		auxiliaryBytes    int
		eventCount        int
		argumentDeltas    = map[string]string{}
		completedItems    = map[int64]map[string]any{}
		completedImageIDs = map[string]bool{}
	)
	argumentDeltaFor := func(keys ...string) string {
		for _, key := range keys {
			if key == "" {
				continue
			}
			if value, ok := argumentDeltas[key]; ok {
				return value
			}
		}
		return ""
	}
	consumeEvent := func(parsed ChatResponsesEvent) error {
		switch parsed.Type {
		case "text_delta":
			content.WriteString(parsed.Delta)
			if content.Len() > maxBytes {
				return errors.New("模型回答超过 192 KiB 上限")
			}
		case "reasoning_delta":
			auxiliaryBytes += len(parsed.Delta)
		case "tool_started", "tool_updated", "tool_completed":
			itemJSON, err := json.Marshal(parsed.Item)
			if err != nil {
				return errors.New("模型结构化过程超过 192 KiB 上限")
			}
			auxiliaryBytes += len(itemJSON)
			if parsed.Type == "tool_updated" {
				itemID := stringValueItem(parsed.Item, "item_id", "call_id", "callId", "id")
				delta := stringValueItem(parsed.Item, "delta")
				if itemID != "" && delta != "" {
					current := argumentDeltas[itemID] + delta
					if len(current) > 64*1024 {
						return errors.New("Responses 单个工具参数超过 64 KiB 上限")
					}
					argumentDeltas[itemID] = current
				}
			}
		}
		if auxiliaryBytes > responsesMaxAuxiliaryBytes {
			return errors.New("模型结构化过程超过 192 KiB 上限")
		}
		if parsed.Type == "completed" {
			completed = true
			usage := objectItem(parsed.Response["usage"])
			if value := nonNegativeInteger(usage["input_tokens"]); value != nil {
				result.InputTokens = value
			}
			if value := nonNegativeInteger(usage["output_tokens"]); value != nil {
				result.OutputTokens = value
			}
			responseOutput := []map[string]any{}
			if output, ok := parsed.Response["output"].([]any); ok {
				for _, item := range output {
					responseOutput = append(responseOutput, objectItem(item))
				}
			}
			source := responseOutput
			if len(source) == 0 {
				indices := make([]int64, 0, len(completedItems))
				for index := range completedItems {
					indices = append(indices, index)
				}
				sortInt64s(indices)
				for _, index := range indices {
					source = append(source, completedItems[index])
				}
			}
			continuation := []any{}
			for _, item := range source {
				itemType, _ := item["type"].(string)
				if itemType == "reasoning" || itemType == "function_call" {
					continuation = append(continuation, normalizeResponsesContinuationItem(item))
				}
			}
			if payload, err := json.Marshal(continuation); err != nil || len(payload) > responsesMaxAuxiliaryBytes {
				return errors.New("Responses 工具往返项目超过 192 KiB 上限")
			}
			result.ContinuationItems = continuation
			toolCalls := []ChatToolCall{}
			for index, item := range source {
				call, err := normalizeFunctionCall(item, argumentDeltaFor, int64(index))
				if err != nil {
					return err
				}
				if call != nil {
					toolCalls = append(toolCalls, *call)
				}
			}
			result.ToolCalls = toolCalls
		}
		if onEvent != nil {
			return onEvent(parsed)
		}
		return nil
	}
	consumeBlock := func(block string) error {
		eventCount++
		if eventCount > maxEvents {
			return errors.New("上游 Responses 事件数量超过 " + itoa(maxEvents) + " 上限")
		}
		parsed := parseResponsesBlock(block)
		if parsed.completedOutputItem != nil {
			completedItems[parsed.completedOutputItem.index] = parsed.completedOutputItem.item
		}
		event := parsed.event
		isImageEvent := event != nil && (event.Type == "image_started" || event.Type == "image_updated" || event.Type == "image_completed" || event.Type == "image_failed")
		if len(block) > responsesMaxEventBytes && !isImageEvent {
			return errors.New("上游 Responses 单个事件超过 64 KiB 上限")
		}
		if event != nil && event.Type == "image_completed" {
			callID := responsesImageCallID(event.Item)
			if callID != "" && parsed.imageResultData != "" && !completedImageIDs[callID] {
				completedImageIDs[callID] = true
				if onImageResult != nil {
					chunks := extractImageResultChunks(parsed.imageResultData)
					if err := onImageResult(callID, stringValueItem(event.Item, "revisedPrompt"), chunks); err != nil {
						return err
					}
				}
			}
		}
		if event != nil {
			return consumeEvent(*event)
		}
		return nil
	}
	consumeSpooledBlock := func(block string) error {
		eventNameTrimmed := extractEventName(block)
		if eventNameTrimmed == "response.completed" {
			dataJSON := sseDataJSON(block)
			if len(dataJSON) > responsesSpoolMetaMaxBytes {
				return errors.New("上游 Responses 终态元数据超过 512 KiB 上限")
			}
			sanitized, values, err := stripImageResultStrings(dataJSON, "result", "b64_json")
			if err != nil {
				return err
			}
			var payload map[string]any
			if err := json.Unmarshal([]byte(sanitized), &payload); err != nil {
				return errors.New("上游 Responses 终态 JSON 无法解析")
			}
			response := objectItem(payload["response"])
			images := completedResponseImages(response)
			for index, image := range images {
				if completedImageIDs[image.callID] {
					continue
				}
				completedImageIDs[image.callID] = true
				if onImageResult != nil {
					base64 := ""
					if index < len(values) {
						base64 = values[index]
					}
					if err := onImageResult(image.callID, image.revisedPrompt, []string{base64}); err != nil {
						return err
					}
				}
				item := map[string]any{"callId": image.callID, "status": "completed"}
				if image.revisedPrompt != "" {
					item["revisedPrompt"] = image.revisedPrompt
				}
				if err := consumeEvent(ChatResponsesEvent{Type: "image_completed", Item: item}); err != nil {
					return err
				}
			}
			eventCount++
			if eventCount > maxEvents {
				return errors.New("上游 Responses 事件数量超过 " + itoa(maxEvents) + " 上限")
			}
			return consumeEvent(ChatResponsesEvent{Type: "completed", Response: response})
		}
		parsed := parseResponsesBlock(block)
		if parsed.event == nil {
			return nil
		}
		if parsed.event.Type == "image_completed" {
			eventCount++
			if eventCount > maxEvents {
				return errors.New("上游 Responses 事件数量超过 " + itoa(maxEvents) + " 上限")
			}
			return consumeBlock(block)
		}
		if len(block) > responsesMaxEventBytes {
			return errors.New("上游 Responses 单个事件超过 64 KiB 上限")
		}
		eventCount++
		if eventCount > maxEvents {
			return errors.New("上游 Responses 事件数量超过 " + itoa(maxEvents) + " 上限")
		}
		return consumeBlock(block)
	}
	buffer := string(raw)
	pendingImage := ""
	for {
		if pendingImage != "" {
			combined := pendingImage + buffer
			boundary := findEventBoundary(combined)
			if boundary == nil {
				if len(combined) > responsesMaxImageEventByte {
					return result, errors.New("图像 SSE 事件超过 64 MiB 上限")
				}
				pendingImage = combined
				buffer = ""
				break
			}
			if len(combined) > responsesMaxImageEventByte {
				return result, errors.New("图像 SSE 事件超过 64 MiB 上限")
			}
			block := combined[:boundary.index]
			buffer = combined[boundary.index+boundary.length:]
			pendingImage = ""
			if err := consumeSpooledBlock(block); err != nil {
				return result, err
			}
			continue
		}
		boundary := findEventBoundary(buffer)
		if boundary == nil {
			break
		}
		block := buffer[:boundary.index]
		buffer = buffer[boundary.index+boundary.length:]
		if err := consumeBlock(block); err != nil {
			return result, err
		}
	}
	if pendingImage != "" {
		return result, errors.New("图像 SSE 事件被截断")
	}
	if len(buffer) > responsesMaxEventBytes {
		if !isPendingImageBlock(buffer) {
			return result, errors.New("上游 Responses 单个事件超过 64 KiB 上限")
		}
		return result, errors.New("图像 SSE 事件被截断")
	}
	if strings.TrimSpace(buffer) != "" {
		if err := consumeBlock(buffer); err != nil {
			return result, err
		}
	}
	if !completed {
		return result, errors.New("上游 Responses 流缺少 response.completed")
	}
	result.Content = content.String()
	return result, nil
}

type parsedResponseBlock struct {
	event               *ChatResponsesEvent
	imageResultData     string
	completedOutputItem *completedOutputItem
}

type completedOutputItem struct {
	index int64
	item  map[string]any
}

var eventNamePattern = regexp.MustCompile(`(?m)^event:\s*(.+)$`)

func extractEventName(block string) string {
	match := eventNamePattern.FindStringSubmatch(block)
	if match == nil {
		return ""
	}
	return strings.TrimSpace(match[1])
}

func sseDataJSON(block string) string {
	dataLines := []string{}
	for _, line := range splitSSELines(block) {
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimLeft(line[5:], " "))
		}
	}
	return strings.Join(dataLines, "\n")
}

func parseResponsesBlock(block string) parsedResponseBlock {
	eventNameTrimmed := extractEventName(block)
	data := sseDataJSON(block)
	if data == "" || data == "[DONE]" {
		return parsedResponseBlock{}
	}
	if isExplicitImageBlock(eventNameTrimmed, eventNameTrimmed) {
		return parseImageBlock(eventNameTrimmed, data)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(data), &payload); err != nil {
		return parsedResponseBlock{}
	}
	blockType, _ := payload["type"].(string)
	if blockType == "" {
		blockType = eventNameTrimmed
	}
	if isExplicitImageBlock(eventNameTrimmed, blockType) {
		name := eventNameTrimmed
		if name == "" {
			name = blockType
		}
		return parseImageBlock(name, data)
	}
	switch blockType {
	case "response.output_text.delta":
		delta, _ := payload["delta"].(string)
		return parsedResponseBlock{event: &ChatResponsesEvent{Type: "text_delta", Delta: delta}}
	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		delta, _ := payload["delta"].(string)
		return parsedResponseBlock{event: &ChatResponsesEvent{Type: "reasoning_delta", Delta: delta}}
	case "response.output_item.added":
		item := objectItem(payload["item"])
		itemType, _ := item["type"].(string)
		if itemType == "image_generation_call" {
			if callID := responsesImageCallID(item); callID != "" {
				merged := cloneJSONMap(item)
				merged["callId"] = callID
				return parsedResponseBlock{event: &ChatResponsesEvent{Type: "image_started", Item: merged}}
			}
			return parsedResponseBlock{event: &ChatResponsesEvent{Type: "image_started", Item: item}}
		}
		if containsString([]string{"function_call", "computer_call", "web_search_call", "file_search_call"}, itemType) {
			return parsedResponseBlock{event: &ChatResponsesEvent{Type: "tool_started", Item: item}}
		}
		return parsedResponseBlock{}
	case "response.function_call_arguments.delta":
		return parsedResponseBlock{event: &ChatResponsesEvent{Type: "tool_updated", Item: payload}}
	case "response.output_item.done":
		item := objectItem(payload["item"])
		itemType, _ := item["type"].(string)
		var completedItem *completedOutputItem
		if index := nonNegativeInteger(payload["output_index"]); index != nil {
			completedItem = &completedOutputItem{index: *index, item: item}
		}
		if containsString([]string{"function_call", "computer_call", "web_search_call", "file_search_call"}, itemType) {
			return parsedResponseBlock{event: &ChatResponsesEvent{Type: "tool_completed", Item: item}, completedOutputItem: completedItem}
		}
		if itemType == "reasoning" {
			return parsedResponseBlock{event: &ChatResponsesEvent{Type: "reasoning_completed", Item: item}, completedOutputItem: completedItem}
		}
		return parsedResponseBlock{}
	case "response.completed":
		return parsedResponseBlock{event: &ChatResponsesEvent{Type: "completed", Response: objectItem(payload["response"])}}
	case "response.failed":
		if response, ok := payload["response"].(map[string]any); ok {
			return parsedResponseBlock{event: &ChatResponsesEvent{Type: "failed", Error: response}}
		}
		if failure, ok := payload["error"].(map[string]any); ok {
			return parsedResponseBlock{event: &ChatResponsesEvent{Type: "failed", Error: failure}}
		}
		return parsedResponseBlock{event: &ChatResponsesEvent{Type: "failed", Error: map[string]any{}}}
	}
	return parsedResponseBlock{}
}

func parseImageBlock(eventName, data string) parsedResponseBlock {
	callID := extractJSONStringField(data, "call_id")
	if callID == "" {
		callID = extractJSONStringField(data, "item_id")
	}
	if callID == "" {
		callID = extractJSONStringField(data, "id")
	}
	status := extractJSONStringField(data, "status")
	revisedPrompt := extractJSONStringField(data, "revised_prompt")
	item := map[string]any{}
	if callID != "" {
		item["callId"] = callID
	}
	if status != "" {
		item["status"] = status
	}
	if revisedPrompt != "" {
		item["revisedPrompt"] = revisedPrompt
	}
	hasResult := hasJSONStringField(data, "result") || hasJSONStringField(data, "b64_json")
	blockType := eventName
	if blockType == "" {
		blockType = extractJSONStringField(data, "type")
	}
	if strings.Contains(blockType, "failed") {
		return parsedResponseBlock{event: &ChatResponsesEvent{Type: "image_failed", Item: item}}
	}
	if strings.Contains(blockType, "partial_image") || strings.Contains(blockType, "in_progress") {
		return parsedResponseBlock{event: &ChatResponsesEvent{Type: "image_updated", Item: item}}
	}
	if strings.Contains(blockType, "completed") || strings.Contains(blockType, "done") || strings.Contains(blockType, "output_item") {
		if hasResult {
			return parsedResponseBlock{event: &ChatResponsesEvent{Type: "image_completed", Item: item}, imageResultData: data}
		}
		if strings.Contains(blockType, "added") {
			return parsedResponseBlock{event: &ChatResponsesEvent{Type: "image_started", Item: item}}
		}
		return parsedResponseBlock{event: &ChatResponsesEvent{Type: "image_failed", Item: item}}
	}
	return parsedResponseBlock{event: &ChatResponsesEvent{Type: "image_started", Item: item}}
}

var jsonStringFieldPatterns = map[string]*regexp.Regexp{}

func fieldPattern(field, suffix string) *regexp.Regexp {
	pattern := jsonStringFieldPatterns[field+suffix]
	if pattern == nil {
		pattern = regexp.MustCompile(`"` + regexp.QuoteMeta(field) + `"\s*:\s*` + suffix)
		jsonStringFieldPatterns[field+suffix] = pattern
	}
	return pattern
}

func extractJSONStringField(data, field string) string {
	match := fieldPattern(field, `"([^"\\]{1,512})"`).FindStringSubmatch(data)
	if match == nil {
		return ""
	}
	return match[1]
}

func hasJSONStringField(data, field string) bool {
	return fieldPattern(field, `"`).MatchString(data)
}

var pendingImageBlockPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?:^|\r?\n)event:\s*[^\r\n]*image_generation_call`),
	regexp.MustCompile(`"type"\s*:\s*"image_generation_call"`),
}

func isPendingImageBlock(block string) bool {
	for _, pattern := range pendingImageBlockPatterns {
		if pattern.MatchString(block) {
			return true
		}
	}
	return false
}

func isExplicitImageBlock(eventName, blockType string) bool {
	return strings.Contains(eventName, "image_generation_call") ||
		strings.Contains(blockType, "image_generation_call") ||
		blockType == "image_generation" ||
		strings.HasPrefix(blockType, "image_generation.")
}

func responsesImageCallID(item map[string]any) string {
	for _, key := range []string{"callId", "call_id", "id"} {
		if value, ok := item[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

type spooledResponseImage struct {
	callID        string
	revisedPrompt string
}

func completedResponseImages(response map[string]any) []spooledResponseImage {
	output, _ := response["output"].([]any)
	images := []spooledResponseImage{}
	for _, value := range output {
		item := objectItem(value)
		itemType, _ := item["type"].(string)
		if itemType != "image_generation_call" {
			continue
		}
		_, hasResult := item["result"]
		_, hasB64 := item["b64_json"]
		if !hasResult && !hasB64 {
			continue
		}
		callID := responsesImageCallID(item)
		if callID == "" {
			continue
		}
		revisedPrompt := stringValueItem(item, "revised_prompt", "revisedPrompt")
		images = append(images, spooledResponseImage{callID: callID, revisedPrompt: revisedPrompt})
	}
	return images
}

func normalizeFunctionCall(item map[string]any, argumentDeltaFor func(keys ...string) string, sourceOrder int64) (*ChatToolCall, error) {
	itemType, _ := item["type"].(string)
	if itemType != "function_call" {
		return nil, nil
	}
	callID := stringValueItem(item, "call_id", "callId")
	toolName := stringValueItem(item, "name")
	itemID := stringValueItem(item, "id")
	argumentsJSON := stringValueItem(item, "arguments")
	if argumentsJSON == "" {
		argumentsJSON = argumentDeltaFor(itemID, callID)
	}
	if callID == "" || toolName == "" || argumentsJSON == "" {
		return nil, errors.New("Responses function_call 缺少 call_id、name 或 arguments")
	}
	return &ChatToolCall{CallID: callID, ToolName: toolName, ArgumentsJSON: argumentsJSON, SourceOrder: sourceOrder}, nil
}

var functionCallIDSanitizer = regexp.MustCompile(`[^A-Za-z0-9_-]`)

func normalizeResponsesContinuationItem(item map[string]any) map[string]any {
	itemType, _ := item["type"].(string)
	if itemType != "function_call" {
		return item
	}
	callID := stringValueItem(item, "call_id", "callId")
	if callID == "" {
		return item
	}
	fallbackID := "fc_" + functionCallIDSanitizer.ReplaceAllString(strings.TrimPrefix(strings.TrimPrefix(callID, "call"), "_"), "_")
	if len(fallbackID) > 63 {
		fallbackID = fallbackID[:63]
	}
	merged := cloneJSONMap(item)
	if stringValueItem(merged, "id") == "" {
		merged["id"] = fallbackID
	}
	if stringValueItem(merged, "status") == "" {
		merged["status"] = "completed"
	}
	return merged
}

func objectItem(value any) map[string]any {
	if item, ok := value.(map[string]any); ok {
		return item
	}
	return map[string]any{}
}

func stringValueItem(item map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := item[key].(string); ok && strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func sortInt64s(values []int64) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}

// stripImageResultStrings splits one spooled JSON document into the payload
// with image base64 values removed plus the extracted values in order. It
// mirrors the readSpooledCompletedResponse / extractSpooledImageResultChunks
// streaming state machine (Node chat-responses-sse.ts) including the
// no-backslash contract inside result/b64_json strings.
func stripImageResultStrings(data string, resultFields ...string) (string, []string, error) {
	var values []string
	var out bytes.Buffer
	var current bytes.Buffer
	inString := false
	escaped := false
	stringToken := ""
	stringTokenEscaped := false
	stringIsValue := false
	stringIsImageResult := false
	collectingResult := false
	var lastStringToken *string
	awaitingValue := false
	var valueKey *string

	for _, character := range data {
		if inString {
			if stringIsImageResult {
				if escaped || character == '\\' {
					return "", nil, errors.New("生成图片 Base64 不允许 JSON 转义")
				}
				if character == '"' {
					if collectingResult && current.Len() == 0 {
						return "", nil, errors.New("生成图片 Base64 不能为空")
					}
					if collectingResult {
						values = append(values, current.String())
					}
					current.Reset()
					out.WriteByte('"')
					inString = false
					stringIsImageResult = false
					collectingResult = false
					lastStringToken = nil
					continue
				}
				if collectingResult {
					current.WriteRune(character)
				}
				continue
			}
			out.WriteRune(character)
			if escaped {
				escaped = false
				stringTokenEscaped = true
				continue
			}
			if character == '\\' {
				escaped = true
				stringTokenEscaped = true
				continue
			}
			if character == '"' {
				inString = false
				if stringIsValue || stringTokenEscaped {
					lastStringToken = nil
				} else {
					copied := stringToken
					lastStringToken = &copied
				}
				continue
			}
			if !stringTokenEscaped && len(stringToken) <= 64 {
				stringToken += string(character)
			}
			continue
		}
		out.WriteRune(character)
		if character == '"' {
			inString = true
			escaped = false
			stringToken = ""
			stringTokenEscaped = false
			stringIsValue = awaitingValue
			stringIsImageResult = awaitingValue && valueKey != nil && isImageResultFieldName(*valueKey, resultFields...)
			if stringIsImageResult {
				collectingResult = true
				current.Reset()
				out.Truncate(out.Len() - 1)
			}
			awaitingValue = false
			valueKey = nil
			lastStringToken = nil
			continue
		}
		if character == ' ' || character == '\t' || character == '\n' || character == '\r' {
			continue
		}
		if character == ':' {
			if lastStringToken != nil {
				copied := *lastStringToken
				valueKey = &copied
				awaitingValue = true
			} else {
				valueKey = nil
				awaitingValue = false
			}
			lastStringToken = nil
			continue
		}
		if awaitingValue {
			awaitingValue = false
			valueKey = nil
		}
		lastStringToken = nil
	}
	if inString {
		return "", nil, errors.New("上游 Responses 终态 JSON 被截断")
	}
	return out.String(), values, nil
}

func isImageResultFieldName(value string, resultFields ...string) bool {
	if len(resultFields) == 0 {
		resultFields = []string{"result", "b64_json"}
	}
	return containsString(resultFields, value)
}

// extractImageResultChunks mirrors imageResultChunks: the ordered base64
// payloads of the result/b64_json fields in one data document.
func extractImageResultChunks(data string) []string {
	_, values, err := stripImageResultStrings(data, "result", "b64_json")
	if err != nil {
		return nil
	}
	return values
}
