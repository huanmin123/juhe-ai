package gatewayopenai

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
)

// ParsedStreamEvent mirrors ParsedOpenAIStreamEvent.
type ParsedStreamEvent struct {
	RawText        string
	EventName      string
	DataText       string
	DataBytes      int
	Data           map[string]any
	DataParseError bool
	EventType      string
	ErrorCode      string
	ErrorMessage   string
}

// StreamEventClassification mirrors OpenAIStreamEventClassification.
type StreamEventClassification struct {
	EventType             string
	Terminal              bool
	Failed                bool
	VisibleOutput         bool
	ImageOutput           bool
	EstimatedOutputTokens int
	Usage                 gatewayproto.ParsedUsage
	UsageFound            bool
	ErrorCode             string
	ErrorMessage          string
}

// splitSseLines mirrors the /\r?\n|\r/ split.
var sseLineSplitPattern = regexp.MustCompile(`\r\n|\r|\n`)

func splitSseLines(text string) []string {
	return sseLineSplitPattern.Split(text, -1)
}

// ParseSseEventText mirrors parseOpenAISseEventText.
func ParseSseEventText(rawText string) ParsedStreamEvent {
	eventName := ""
	var dataLines []string
	dataBytes := 0
	for _, line := range splitSseLines(rawText) {
		if strings.HasPrefix(line, "event:") {
			eventName = strings.TrimSpace(line[len("event:"):])
		} else if strings.HasPrefix(line, "data:") {
			dataLine := trimStart(line[len("data:"):])
			dataLines = append(dataLines, dataLine)
			dataBytes += len(dataLine)
		}
	}
	joined := strings.TrimSpace(strings.Join(dataLines, "\n"))
	return ParseStreamEventData(joined, eventName, rawText, dataBytes)
}

func trimStart(value string) string {
	return strings.TrimLeft(value, " \t\n\v\f\r ")
}

// ParseStreamEventData mirrors parseOpenAIStreamEventData.
func ParseStreamEventData(dataText, eventName, rawText string, dataBytes int) ParsedStreamEvent {
	if dataText == "" || dataText == "[DONE]" {
		eventType := eventName
		if dataText == "[DONE]" {
			eventType = "[DONE]"
		}
		return ParsedStreamEvent{
			RawText:   rawText,
			EventName: eventName,
			DataText:  dataText,
			DataBytes: dataBytes,
			EventType: eventType,
		}
	}

	var data map[string]any
	if err := json.Unmarshal([]byte(dataText), &data); err != nil || data == nil {
		return ParsedStreamEvent{
			RawText:        rawText,
			EventName:      eventName,
			DataText:       dataText,
			DataBytes:      dataBytes,
			DataParseError: true,
			EventType:      eventName,
		}
	}
	eventType := eventName
	if text, ok := data["type"].(string); ok {
		eventType = text
	} else if text, ok := data["event_type"].(string); ok {
		eventType = text
	}
	errorObject := extractStreamEventError(data)
	event := ParsedStreamEvent{
		RawText:   rawText,
		EventName: eventName,
		DataText:  dataText,
		DataBytes: dataBytes,
		Data:      data,
		EventType: eventType,
	}
	if errorObject != nil {
		if code, ok := errorObject["code"].(string); ok {
			event.ErrorCode = code
		}
		if message, ok := errorObject["message"].(string); ok {
			event.ErrorMessage = message
		}
	}
	return event
}

// extractStreamEventError mirrors extractOpenAIStreamEventError.
func extractStreamEventError(data map[string]any) map[string]any {
	if typeText, _ := data["type"].(string); typeText == "response.mcp_call.failed" {
		return nil
	}
	response, _ := data["response"].(map[string]any)
	if response != nil {
		if responseError, ok := response["error"].(map[string]any); ok {
			return responseError
		}
	}
	if errorObject, ok := data["error"].(map[string]any); ok {
		return errorObject
	}
	if typeText, _ := data["type"].(string); typeText == "error" {
		_, hasCode := data["code"].(string)
		_, hasMessage := data["message"].(string)
		if hasCode || hasMessage {
			return data
		}
	}
	return nil
}

// IsStreamFailureEvent mirrors isOpenAIStreamFailureEvent.
func IsStreamFailureEvent(event ParsedStreamEvent) bool {
	if event.EventType == "response.failed" || event.EventName == "response.failed" {
		return true
	}
	if event.EventType == "error" || event.EventName == "error" {
		return true
	}
	return event.Data != nil && extractStreamEventError(event.Data) != nil
}

// IsStreamVisibleOutputEvent mirrors isOpenAIStreamVisibleOutputEvent.
func IsStreamVisibleOutputEvent(event ParsedStreamEvent) bool {
	return event.Data != nil && streamEventHasVisibleOutput(event.Data, event.EventType)
}

// streamEventHasVisibleOutput mirrors openAIStreamEventHasVisibleOutput.
func streamEventHasVisibleOutput(data map[string]any, eventType string) bool {
	if strings.HasSuffix(eventType, ".delta") && hasMeaningfulDelta(data["delta"]) {
		return true
	}
	if eventType == "response.output_item.added" || eventType == "response.output_item.done" {
		item, _ := data["item"].(map[string]any)
		return responsesOutputItemRepresentsClientOutput(item) || EstimateTokensFromOutputValue(data["item"]) > 0
	}
	if eventType == "response.completed" || eventType == "response.done" || eventType == "response.incomplete" {
		response, _ := data["response"].(map[string]any)
		output, _ := response["output"].([]any)
		return responsesOutputArrayRepresentsClientOutput(output) || EstimateTokensFromOutputValue(output) > 0
	}
	if IsImageStreamEventType(eventType) {
		return true
	}
	choices, _ := data["choices"].([]any)
	for _, entry := range choices {
		row, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		if text, ok := row["text"].(string); ok && text != "" {
			return true
		}
		delta, ok := row["delta"].(map[string]any)
		if ok && hasMeaningfulChoiceDelta(delta) {
			return true
		}
	}
	return false
}

// streamEventHasImageOutput mirrors openAIStreamEventHasImageOutput.
func streamEventHasImageOutput(data map[string]any, eventType string) bool {
	if IsImageStreamEventType(eventType) {
		return true
	}
	if item, ok := data["item"].(map[string]any); ok {
		if typeText, _ := item["type"].(string); typeText == "image_generation_call" {
			return true
		}
	}
	response, _ := data["response"].(map[string]any)
	if response != nil {
		output, _ := response["output"].([]any)
		for _, entry := range output {
			item, ok := entry.(map[string]any)
			if ok {
				if typeText, _ := item["type"].(string); typeText == "image_generation_call" {
					return true
				}
			}
		}
	}
	return false
}

// IsImageStreamEventType mirrors isOpenAIImageStreamEventType.
func IsImageStreamEventType(eventType string) bool {
	return strings.HasPrefix(eventType, "response.image_generation_call.") ||
		eventType == "image_generation.partial_image" ||
		eventType == "image_generation.completed" ||
		eventType == "image_generation.failed"
}

// ToolCallDetected reports whether a parsed stream event carries tool-call /
// structured output evidence. This is the structured detection contract of
// the slice: chat-completions delta.tool_calls / text, and Responses callable
// output items (function_call etc.). The inspector forwards every parsed
// event through its observer callback so callers can run this check inline.
func ToolCallDetected(event ParsedStreamEvent) bool {
	if event.Data == nil {
		return false
	}
	data := event.Data
	if strings.HasSuffix(event.EventType, ".delta") {
		if delta, ok := data["delta"].(map[string]any); ok {
			if hasMeaningfulDelta(delta["tool_calls"]) {
				return true
			}
		}
	}
	if item, ok := data["item"].(map[string]any); ok {
		if typeText, _ := item["type"].(string); isResponsesCallableOutputItemType(typeText) {
			return true
		}
	}
	choices, _ := data["choices"].([]any)
	for _, entry := range choices {
		row, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		if delta, ok := row["delta"].(map[string]any); ok {
			if hasMeaningfulDelta(delta["tool_calls"]) {
				return true
			}
		}
		if message, ok := row["message"].(map[string]any); ok {
			if hasMeaningfulDelta(message["tool_calls"]) {
				return true
			}
		}
	}
	return false
}

// hasMeaningfulChoiceDelta mirrors hasMeaningfulChoiceDelta.
func hasMeaningfulChoiceDelta(delta map[string]any) bool {
	return hasMeaningfulDelta(delta["content"]) ||
		hasMeaningfulDelta(delta["refusal"]) ||
		hasMeaningfulDelta(delta["reasoning_content"]) ||
		hasMeaningfulDelta(delta["audio"]) ||
		hasMeaningfulDelta(delta["tool_calls"])
}

// hasMeaningfulDelta mirrors hasMeaningfulDelta.
func hasMeaningfulDelta(value any) bool {
	if text, ok := value.(string); ok {
		return text != ""
	}
	if array, ok := value.([]any); ok {
		for _, item := range array {
			if hasMeaningfulDelta(item) {
				return true
			}
		}
		return false
	}
	object, ok := value.(map[string]any)
	if !ok {
		return false
	}
	for key, child := range object {
		if key == "index" || key == "type" || key == "id" {
			continue
		}
		if hasMeaningfulDelta(child) {
			return true
		}
	}
	return false
}

func responsesOutputArrayRepresentsClientOutput(value any) bool {
	array, ok := value.([]any)
	if !ok {
		return false
	}
	for _, item := range array {
		object, ok := item.(map[string]any)
		if ok && responsesOutputItemRepresentsClientOutput(object) {
			return true
		}
	}
	return false
}

func responsesOutputItemRepresentsClientOutput(item map[string]any) bool {
	if item == nil {
		return false
	}
	typeText, ok := item["type"].(string)
	if !ok {
		return false
	}
	return isResponsesCallableOutputItemType(typeText)
}

// isResponsesCallableOutputItemType mirrors isResponsesCallableOutputItemType.
func isResponsesCallableOutputItemType(typeText string) bool {
	switch typeText {
	case "function_call",
		"custom_tool_call",
		"computer_call",
		"web_search_call",
		"file_search_call",
		"mcp_call",
		"code_interpreter_call",
		"image_generation_call":
		return true
	}
	return false
}

// estimateStreamEventOutputTokens mirrors estimateOpenAIStreamEventOutputTokens.
func estimateStreamEventOutputTokens(data map[string]any, eventType string, priorEstimatedOutputTokens int) int {
	tokens := 0
	if strings.HasSuffix(eventType, ".delta") {
		tokens += EstimateTokensFromOutputValue(data["delta"])
	}
	choices, _ := data["choices"].([]any)
	for _, entry := range choices {
		row, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		tokens += EstimateTokensFromOutputValue(row["text"])
		tokens += EstimateTokensFromOutputValue(row["delta"])
	}
	if tokens == 0 && priorEstimatedOutputTokens == 0 {
		if eventType == "response.output_item.done" {
			tokens += EstimateTokensFromOutputValue(data["item"])
		} else if eventType == "response.completed" || eventType == "response.done" || eventType == "response.incomplete" {
			response, _ := data["response"].(map[string]any)
			tokens += EstimateTokensFromOutputValue(response["output"])
		}
	}
	return tokens
}

// extractEventUsage mirrors extractEventUsage: response.usage ?? usage.
func extractEventUsage(data map[string]any) (any, bool) {
	response, _ := data["response"].(map[string]any)
	if response != nil {
		if usage, ok := response["usage"]; ok && usage != nil {
			return usage, true
		}
	}
	usage, ok := data["usage"]
	return usage, ok && usage != nil
}

// ClassifyStreamEvent mirrors classifyOpenAIStreamEvent.
func ClassifyStreamEvent(event ParsedStreamEvent, priorEstimatedOutputTokens int) StreamEventClassification {
	data := event.Data
	estimatedOutputTokens := 0
	imageOutput := false
	visibleOutput := false
	usage := gatewayproto.EmptyUsage()
	if data != nil {
		estimatedOutputTokens = estimateStreamEventOutputTokens(data, event.EventType, priorEstimatedOutputTokens)
		imageOutput = streamEventHasImageOutput(data, event.EventType)
		visibleOutput = estimatedOutputTokens > 0 || streamEventHasVisibleOutput(data, event.EventType)
		if usageValue, ok := extractEventUsage(data); ok {
			usage = ExtractUsage(usageValue, "")
		}
	}
	failed := event.EventType == "response.failed" ||
		event.EventName == "response.failed" ||
		event.EventType == "image_generation.failed" ||
		event.EventName == "image_generation.failed" ||
		event.EventType == "error" ||
		event.EventName == "error"
	terminal := failed ||
		event.EventType == "[DONE]" ||
		event.EventType == "response.completed" ||
		event.EventType == "response.done" ||
		event.EventType == "response.incomplete" ||
		event.EventType == "image_generation.completed"
	classification := StreamEventClassification{
		EventType:             event.EventType,
		Terminal:              terminal,
		Failed:                failed,
		VisibleOutput:         visibleOutput,
		ImageOutput:           imageOutput,
		EstimatedOutputTokens: estimatedOutputTokens,
		Usage:                 usage,
		UsageFound:            gatewayproto.HasAnyUsageValue(usage),
	}
	if failed {
		classification.ErrorCode = event.ErrorCode
		classification.ErrorMessage = event.ErrorMessage
	}
	return classification
}
