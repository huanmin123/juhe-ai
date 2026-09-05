package accountprobe

import (
	"encoding/json"
	"strings"
)

// 本文件移植 Node account-test-response-diagnostics.ts 与
// account-test-success-evidence.ts 的响应分类：
//   - diagnosticResponseContext 的 SSE / JSON 双解析；
//   - parseAccountTestUpstreamErrorCode / parseAccountTestUpstreamMessage /
//     parseAccountTestStreamFailureMessage；
//   - extractAccountTestResponseOutputText / RawVisibleOutputText；
//   - hasAccountTestProtocolSuccessEvidence。

// sseEvent 等价 DiagnosticSseEvent。
type sseEvent struct {
	event string
	data  string
	json  map[string]any
	done  bool
}

// responseContext 等价 DiagnosticResponseContext。
type responseContext struct {
	bodyText string
	record   map[string]any
	events   []sseEvent
	payloads []map[string]any
}

func parseResponseContext(bodyText string) responseContext {
	normalized := bodyText
	if strings.HasPrefix(normalized, "\uFEFF") {
		normalized = normalized[3:]
	}
	trimmed := strings.TrimSpace(normalized)
	if trimmed == "" {
		return responseContext{bodyText: bodyText}
	}
	if !looksLikeSSE(trimmed) {
		var jsonValue any
		if err := json.Unmarshal([]byte(trimmed), &jsonValue); err == nil {
			context := responseContext{bodyText: bodyText}
			if record, ok := jsonValue.(map[string]any); ok {
				context.record = record
				context.payloads = []map[string]any{record}
			}
			return context
		}
	}
	context := responseContext{bodyText: bodyText, events: parseSSEEvents(normalized)}
	for _, event := range context.events {
		if event.json != nil {
			context.payloads = append(context.payloads, event.json)
		}
	}
	return context
}

func looksLikeSSE(text string) bool {
	for _, line := range splitLines(text) {
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, ":") {
			return true
		}
		for _, field := range []string{"event", "data", "id", "retry"} {
			if line == field || strings.HasPrefix(line, field+":") {
				return true
			}
		}
	}
	return false
}

func splitLines(text string) []string {
	// Node split(/\r\n|\r|\n/)。
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		line = strings.TrimSuffix(line, "\r")
		lines[i] = line
	}
	return lines
}

func parseSSEEvents(bodyText string) []sseEvent {
	var events []sseEvent
	var eventName string
	var dataLines []string
	flush := func() {
		if len(dataLines) == 0 {
			eventName = ""
			return
		}
		data := strings.Join(dataLines, "\n")
		normalizedData := strings.TrimSpace(data)
		done := normalizedData == "[DONE]"
		event := sseEvent{event: eventName, data: data, done: done}
		if !done && normalizedData != "" {
			var jsonValue any
			if err := json.Unmarshal([]byte(data), &jsonValue); err == nil {
				if record, ok := jsonValue.(map[string]any); ok {
					event.json = record
				}
			}
		}
		events = append(events, event)
		eventName = ""
		dataLines = nil
	}
	for _, rawLine := range splitLines(bodyText) {
		if rawLine == "" {
			flush()
			continue
		}
		if strings.HasPrefix(rawLine, ":") {
			continue
		}
		field := rawLine
		value := ""
		if index := strings.Index(rawLine, ":"); index >= 0 {
			field = rawLine[:index]
			value = rawLine[index+1:]
			if strings.HasPrefix(value, " ") {
				value = value[1:]
			}
		}
		switch field {
		case "event":
			eventName = value
		case "data":
			dataLines = append(dataLines, value)
		}
	}
	flush()
	return events
}

// ---- 值工具（等价 objectValue/stringValue/rawStringValue）----

func objectValue(value any) map[string]any {
	if record, ok := value.(map[string]any); ok {
		return record
	}
	return nil
}

func arrayValue(value any) []any {
	if list, ok := value.([]any); ok {
		return list
	}
	return nil
}

func stringValue(value any) string {
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text)
	}
	return ""
}

func rawStringValue(value any) (string, bool) {
	if text, ok := value.(string); ok {
		return text, true
	}
	return "", false
}

func joinedText(parts []string) string {
	var kept []string
	for _, part := range parts {
		if part != "" {
			kept = append(kept, part)
		}
	}
	text := strings.TrimSpace(strings.Join(kept, ""))
	return text
}

func joinedRawText(parts []string) (string, bool) {
	text := strings.Join(parts, "")
	return text, text != ""
}

// ---- 上游错误码 / 消息 ----

func upstreamErrorCodeFromPayload(payload map[string]any) string {
	response := objectValue(payload["response"])
	errorValue := objectValue(payload["error"])
	if errorValue == nil {
		if response != nil {
			errorValue = objectValue(response["error"])
		}
	}
	if errorValue == nil {
		errorValue = payload
	}
	if code := stringValue(errorValue["code"]); code != "" {
		return code
	}
	return stringValue(errorValue["type"])
}

// parseUpstreamErrorCode 等价 parseAccountTestUpstreamErrorCode。
func parseUpstreamErrorCode(input string) string {
	context := parseResponseContext(input)
	for _, payload := range context.payloads {
		if code := upstreamErrorCodeFromPayload(payload); code != "" {
			return code
		}
	}
	return ""
}

func protocolMessage(payload map[string]any, protocol DiagnosticProtocol) string {
	if payload == nil {
		return ""
	}
	if protocol == ProtocolAnthropic || protocol == ProtocolGemini {
		errorValue := objectValue(payload["error"])
		if errorValue != nil {
			if message := stringValue(errorValue["message"]); message != "" {
				return message
			}
			if status := stringValue(errorValue["status"]); status != "" {
				return status
			}
		}
		return stringValue(payload["message"])
	}
	var errorValue map[string]any
	if candidate := objectValue(payload["error"]); candidate != nil {
		errorValue = candidate
	} else if response := objectValue(payload["response"]); response != nil {
		errorValue = objectValue(response["error"])
	}
	if message := openAIErrorMessage(errorValue); message != "" {
		return message
	}
	return stringValue(payload["message"])
}

func openAIErrorMessage(value any) string {
	if text, ok := value.(string); ok {
		trimmed := strings.TrimSpace(text)
		return trimmed
	}
	record := objectValue(value)
	if record == nil {
		return ""
	}
	if message := stringValue(record["message"]); message != "" {
		return message
	}
	if code := stringValue(record["code"]); code != "" {
		return code
	}
	return stringValue(record["type"])
}

// parseUpstreamMessage 等价 parseAccountTestUpstreamMessage（rawFallback 由调用方决定）。
func parseUpstreamMessage(input string, protocol DiagnosticProtocol, rawFallback bool) string {
	context := parseResponseContext(input)
	for _, payload := range context.payloads {
		if message := protocolMessage(payload, protocol); message != "" {
			return message
		}
	}
	if failure := parseStreamFailureMessage(context, protocol); failure != "" {
		return failure
	}
	if rawFallback && context.bodyText != "" {
		return truncateRunes(context.bodyText, 240)
	}
	return ""
}

func truncateRunes(text string, limit int) string {
	runes := []rune(text)
	if len(runes) > limit {
		return string(runes[:limit])
	}
	return text
}

// parseStreamFailureMessage 等价 parseAccountTestStreamFailureMessage。
func parseStreamFailureMessage(context responseContext, protocol DiagnosticProtocol) string {
	if protocol == ProtocolAnthropic {
		for _, event := range context.events {
			if event.event == "error" {
				if message := protocolMessage(event.json, protocol); message != "" {
					return message
				}
				return "Anthropic 流式响应失败"
			}
		}
		return ""
	}
	if protocol == ProtocolGemini {
		for _, payload := range context.payloads {
			if message := protocolMessage(payload, protocol); message != "" {
				return message
			}
		}
		return ""
	}
	for _, event := range context.events {
		eventType := stringValue(event.json["type"])
		if eventType == "" {
			eventType = event.event
		}
		if eventType != "response.failed" && eventType != "response.incomplete" && eventType != "error" {
			continue
		}
		if event.json == nil {
			continue
		}
		if message := openAIErrorMessage(event.json["error"]); message != "" {
			return message
		}
		if response := objectValue(event.json["response"]); response != nil {
			if message := openAIErrorMessage(response["error"]); message != "" {
				return message
			}
		}
		if message := openAIErrorMessage(event.json); message != "" {
			return message
		}
		return eventType
	}
	return ""
}

// ---- 输出文本抽取 ----

func extractOutputText(context responseContext, protocol DiagnosticProtocol) string {
	switch protocol {
	case ProtocolAnthropic:
		return extractAnthropicOutputText(context)
	case ProtocolGemini:
		var parts []string
		for _, payload := range context.payloads {
			parts = append(parts, geminiCandidateTexts(payload)...)
		}
		return joinedText(parts)
	default:
		return extractOpenAIOutputText(context)
	}
}

func extractRawVisibleOutputText(context responseContext, protocol DiagnosticProtocol) (string, bool) {
	switch protocol {
	case ProtocolAnthropic:
		return extractRawAnthropicVisibleOutputText(context)
	case ProtocolGemini:
		var parts []string
		for _, payload := range context.payloads {
			visible, _ := geminiVisibleTexts(payload)
			parts = append(parts, visible...)
		}
		return joinedRawText(parts)
	default:
		return extractRawOpenAIVisibleOutputText(context)
	}
}

func extractOpenAIOutputText(context responseContext) string {
	if direct := extractOpenAIResponsePayloadText(context.record); direct != "" {
		return direct
	}
	if direct := extractOpenAIChatPayloadText(context.record, "message"); direct != "" {
		return direct
	}
	var chunks []string
	for _, event := range context.events {
		payload := event.json
		if payload == nil {
			continue
		}
		eventType := stringValue(payload["type"])
		if eventType == "" {
			eventType = event.event
		}
		if eventType == "response.output_text.delta" || eventType == "response.refusal.delta" {
			if delta := stringValue(payload["delta"]); delta != "" {
				chunks = append(chunks, delta)
			}
		}
		if eventType == "response.output_text.done" {
			if text := stringValue(payload["text"]); text != "" {
				return text
			}
		}
		if eventType == "response.completed" || eventType == "response.done" {
			if text := extractOpenAIResponsePayloadText(objectValue(payload["response"])); text != "" {
				return text
			}
		}
		if chatText := extractOpenAIChatPayloadText(payload, "delta"); chatText != "" {
			chunks = append(chunks, chatText)
		}
	}
	return joinedText(chunks)
}

func extractOpenAIResponsePayloadText(payload map[string]any) string {
	if payload == nil {
		return ""
	}
	if direct := stringValue(payload["output_text"]); direct != "" {
		return direct
	}
	output := arrayValue(payload["output"])
	var parts []string
	for _, item := range output {
		entry := objectValue(item)
		content := arrayValue(entry["content"])
		for _, element := range content {
			if text := stringValue(objectValue(element)["text"]); text != "" {
				parts = append(parts, text)
			}
		}
	}
	return joinedText(parts)
}

func extractOpenAIChatPayloadText(payload map[string]any, field string) string {
	choices := arrayValue(payload["choices"])
	var parts []string
	for _, choice := range choices {
		container := objectValue(objectValue(choice)[field])
		if container == nil {
			continue
		}
		if text := stringValue(container["content"]); text != "" {
			parts = append(parts, text)
			continue
		}
		if text := stringValue(container["reasoning_content"]); text != "" {
			parts = append(parts, text)
			continue
		}
		if text := stringValue(container["refusal"]); text != "" {
			parts = append(parts, text)
		}
	}
	return joinedText(parts)
}

func extractAnthropicOutputText(context responseContext) string {
	content := arrayValue(context.record["content"])
	var direct []string
	for _, item := range content {
		if text := stringValue(objectValue(item)["text"]); text != "" {
			direct = append(direct, text)
		}
	}
	if text := joinedText(direct); text != "" {
		return text
	}
	var parts []string
	for _, payload := range context.payloads {
		if text := stringValue(objectValue(payload["delta"])["text"]); text != "" {
			parts = append(parts, text)
		}
	}
	return joinedText(parts)
}

func geminiCandidateTexts(payload map[string]any) []string {
	candidates := arrayValue(payload["candidates"])
	var parts []string
	for _, candidate := range candidates {
		content := objectValue(objectValue(candidate)["content"])
		partsList := arrayValue(content["parts"])
		for _, part := range partsList {
			if text := stringValue(objectValue(part)["text"]); text != "" {
				parts = append(parts, text)
			}
		}
	}
	return parts
}

func openAIVisibleContentText(value any) (string, bool) {
	entry := objectValue(value)
	if entry == nil {
		return "", false
	}
	entryType, _ := rawStringValue(entry["type"])
	if entryType != "output_text" && entryType != "text" && entryType != "refusal" {
		return "", false
	}
	return rawStringValue(entry["text"])
}

func extractRawOpenAIVisibleOutputText(context responseContext) (string, bool) {
	if text, ok := extractRawOpenAIResponseVisibleText(context.record); ok {
		return text, true
	}
	if text, ok := extractRawOpenAIChatVisibleText(context.record, "message"); ok {
		return text, true
	}
	var chunks []string
	for _, event := range context.events {
		payload := event.json
		if payload == nil {
			continue
		}
		eventType := stringValue(payload["type"])
		if eventType == "" {
			eventType = event.event
		}
		if eventType == "response.output_text.delta" || eventType == "response.refusal.delta" {
			if delta, ok := rawStringValue(payload["delta"]); ok {
				chunks = append(chunks, delta)
			}
		}
		if eventType == "response.output_text.done" {
			if text, ok := rawStringValue(payload["text"]); ok && text != "" {
				return text, true
			}
		}
		if eventType == "response.completed" || eventType == "response.done" {
			if text, ok := extractRawOpenAIResponseVisibleText(objectValue(payload["response"])); ok {
				return text, true
			}
		}
		if text, ok := extractRawOpenAIChatVisibleText(payload, "delta"); ok {
			chunks = append(chunks, text)
		}
	}
	return joinedRawText(chunks)
}

func extractRawOpenAIResponseVisibleText(payload map[string]any) (string, bool) {
	if payload == nil {
		return "", false
	}
	if direct, ok := rawStringValue(payload["output_text"]); ok && direct != "" {
		return direct, true
	}
	output := arrayValue(payload["output"])
	var parts []string
	for _, item := range output {
		content := arrayValue(objectValue(item)["content"])
		for _, element := range content {
			if text, ok := openAIVisibleContentText(element); ok {
				parts = append(parts, text)
			}
		}
	}
	return joinedRawText(parts)
}

func extractRawOpenAIChatVisibleText(payload map[string]any, field string) (string, bool) {
	choices := arrayValue(payload["choices"])
	var parts []string
	for _, choice := range choices {
		container := objectValue(objectValue(choice)[field])
		if container != nil {
			if content, ok := rawStringValue(container["content"]); ok {
				parts = append(parts, content)
				continue
			}
			if contentBlocks, ok := joinedRawText(contentBlocksTexts(arrayValue(container["content"]))); ok {
				parts = append(parts, contentBlocks)
				continue
			}
			if refusal, ok := rawStringValue(container["refusal"]); ok {
				parts = append(parts, refusal)
			}
		}
	}
	return joinedRawText(parts)
}

func contentBlocksTexts(content []any) []string {
	var parts []string
	for _, element := range content {
		if text, ok := openAIVisibleContentText(element); ok {
			parts = append(parts, text)
		}
	}
	return parts
}

func extractRawAnthropicVisibleOutputText(context responseContext) (string, bool) {
	content := arrayValue(context.record["content"])
	var direct []string
	for _, item := range content {
		entry := objectValue(item)
		if entry == nil {
			continue
		}
		entryType, hasType := rawStringValue(entry["type"])
		if hasType && entryType != "text" {
			continue
		}
		if text, ok := rawStringValue(entry["text"]); ok {
			direct = append(direct, text)
		}
	}
	if text, ok := joinedRawText(direct); ok {
		return text, true
	}
	var parts []string
	for _, payload := range context.payloads {
		delta := objectValue(payload["delta"])
		if delta == nil {
			continue
		}
		deltaType, hasType := rawStringValue(delta["type"])
		if hasType && deltaType != "text_delta" {
			continue
		}
		if text, ok := rawStringValue(delta["text"]); ok {
			parts = append(parts, text)
		}
	}
	return joinedRawText(parts)
}

func geminiVisibleTexts(payload map[string]any) ([]string, bool) {
	candidates := arrayValue(payload["candidates"])
	steps := arrayValue(payload["steps"])
	var visible []string
	for _, candidate := range candidates {
		visible = append(visible, geminiVisibleContentTexts(objectValue(candidate))...)
	}
	for _, step := range steps {
		record := objectValue(step)
		stepType, _ := rawStringValue(record["type"])
		if stepType == "thought" || stepType == "thought_summary" {
			continue
		}
		visible = append(visible, geminiVisibleContentTexts(record)...)
	}
	delta := objectValue(payload["delta"])
	eventType, _ := rawStringValue(payload["event_type"])
	if eventType == "step.delta" {
		deltaType, _ := rawStringValue(delta["type"])
		if deltaType == "text" {
			if text, ok := rawStringValue(delta["text"]); ok {
				visible = append(visible, text)
				return visible, true
			}
		}
	}
	return visible, false
}

func geminiVisibleContentTexts(record map[string]any) []string {
	if record == nil {
		return nil
	}
	var partsList []any
	if content := arrayValue(record["content"]); content != nil {
		partsList = content
	} else if content := objectValue(record["content"]); content != nil {
		partsList = arrayValue(content["parts"])
	}
	var visible []string
	for _, part := range partsList {
		entry := objectValue(part)
		if entry != nil && entry["thought"] == true {
			continue
		}
		if text, ok := rawStringValue(entry["text"]); ok {
			visible = append(visible, text)
		}
	}
	return visible
}

// ---- 协议成功证据（account-test-success-evidence.ts）----

func hasCompletedChatPayload(payload map[string]any) bool {
	choices := arrayValue(payload["choices"])
	for _, choice := range choices {
		if stringValue(objectValue(choice)["finish_reason"]) != "" {
			return true
		}
	}
	return false
}

func hasChatContentPayload(payload map[string]any) bool {
	choices := arrayValue(payload["choices"])
	for _, choice := range choices {
		item := objectValue(choice)
		if stringValue(objectValue(item["delta"])["content"]) != "" ||
			stringValue(objectValue(item["message"])["content"]) != "" {
			return true
		}
	}
	return false
}

func hasCompletedResponsesPayload(payload map[string]any) bool {
	if payload == nil {
		return false
	}
	return payload["status"] == "completed" &&
		(payload["object"] == "response" || arrayValue(payload["output"]) != nil)
}

func hasCompletedMessagesPayload(payload map[string]any) bool {
	if payload == nil {
		return false
	}
	return payload["type"] == "message" && stringValue(payload["stop_reason"]) != ""
}

func hasCompletedGeminiPayload(payload map[string]any) bool {
	candidates := arrayValue(payload["candidates"])
	for _, candidate := range candidates {
		if stringValue(objectValue(candidate)["finishReason"]) != "" {
			return true
		}
	}
	return false
}

func hasCompletedInteractionsPayload(payload map[string]any) bool {
	if payload == nil {
		return false
	}
	return payload["status"] == "completed" &&
		(payload["object"] == "interaction" || arrayValue(payload["steps"]) != nil)
}

func hasStreamingSuccessEvidence(mode EndpointMode, context responseContext) bool {
	hasChatContent := false
	for _, event := range context.events {
		if event.done {
			return mode == ModeChatSSE && hasChatContent
		}
		payload := event.json
		if payload == nil {
			continue
		}
		eventType := stringValue(payload["type"])
		if eventType == "" {
			eventType = event.event
		}
		if mode == ModeChatSSE && hasCompletedChatPayload(payload) {
			return true
		}
		if mode == ModeChatSSE && hasChatContentPayload(payload) {
			hasChatContent = true
		}
		if mode == ModeResponsesSSE {
			response := objectValue(payload["response"])
			if eventType == "response.completed" || hasCompletedResponsesPayload(response) || hasCompletedResponsesPayload(payload) {
				return true
			}
		}
		if mode == ModeMessagesSSE {
			message := objectValue(payload["message"])
			if eventType == "message_stop" || hasCompletedMessagesPayload(message) || hasCompletedMessagesPayload(payload) {
				return true
			}
		}
		if mode == ModeGenerateContentSSE && hasCompletedGeminiPayload(payload) {
			return true
		}
		if mode == ModeInteractionsSSE {
			interaction := objectValue(payload["interaction"])
			if eventType == "interaction.completed" || (interaction != nil && interaction["status"] == "completed") {
				return true
			}
		}
	}
	return false
}

// hasProtocolSuccessEvidence 等价 hasAccountTestProtocolSuccessEvidence。
func hasProtocolSuccessEvidence(mode EndpointMode, context responseContext) bool {
	if mode.streaming() {
		return hasStreamingSuccessEvidence(mode, context)
	}
	payload := context.record
	if payload == nil {
		return false
	}
	switch mode {
	case ModeChatJSON:
		return hasCompletedChatPayload(payload)
	case ModeResponsesJSON:
		return hasCompletedResponsesPayload(payload)
	case ModeMessagesJSON:
		return hasCompletedMessagesPayload(payload)
	case ModeGenerateContentJSON:
		return hasCompletedGeminiPayload(payload)
	case ModeInteractionsJSON:
		return hasCompletedInteractionsPayload(payload)
	default:
		return false
	}
}
