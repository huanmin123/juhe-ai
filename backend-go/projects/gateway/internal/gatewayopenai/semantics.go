package gatewayopenai

import (
	"strconv"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
)

// ExtractJSONSemanticFrames mirrors extractOpenAIJsonSemanticFrames.
func ExtractJSONSemanticFrames(value any, endpointFamily gatewayproto.ResponseEndpointFamily) []gatewayproto.SemanticFrame {
	root, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	var frames []gatewayproto.SemanticFrame
	if rootError, ok := root["error"].(map[string]any); ok {
		frames = append(frames, errorFrame(rootError, endpointFamily, gatewayproto.TransportJSON, []string{"error"}))
	}
	response, _ := root["response"].(map[string]any)
	if response != nil {
		if responseError, ok := response["error"].(map[string]any); ok {
			frames = append(frames, errorFrame(responseError, endpointFamily, gatewayproto.TransportJSON, []string{"response.error"}))
		}
	}
	switch endpointFamily {
	case gatewayproto.EndpointFamilyChatCompletions:
		frames = append(frames, extractChatJSONFrames(root)...)
	case gatewayproto.EndpointFamilyResponses:
		frames = append(frames, extractResponsesJSONFrames(root)...)
	default:
		frames = append(frames, extractChatJSONFrames(root)...)
		frames = append(frames, extractResponsesJSONFrames(root)...)
	}
	usage := ExtractUsage(root["usage"], "")
	if gatewayproto.HasAnyUsageValue(usage) {
		frames = append(frames, gatewayproto.SemanticFrame{
			FrameType:      gatewayproto.FrameTypeUsage,
			Protocol:       ResponseProtocol,
			EndpointFamily: endpointFamily,
			Transport:      gatewayproto.TransportJSON,
			Usage:          usage,
			RawJSONPaths:   []string{"usage"},
		})
	}
	frames = append(frames, rawJSONFrame(root, endpointFamily, gatewayproto.TransportJSON))
	return attachRawJSON(frames, root)
}

// ExtractSseSemanticFrames mirrors extractOpenAISseSemanticFrames.
func ExtractSseSemanticFrames(event ParsedStreamEvent, endpointFamily gatewayproto.ResponseEndpointFamily) []gatewayproto.SemanticFrame {
	data := event.Data
	eventType := orDefault(orDefault(event.EventType, event.EventName), "message")
	rawText := event.RawText
	if rawText == "" {
		rawText = event.DataText
	}
	var frames []gatewayproto.SemanticFrame
	if data == nil {
		if eventType == "[DONE]" {
			frames = append(frames, completedFrame(endpointFamily, gatewayproto.TransportSSE, "[DONE]", eventType, rawText, 0))
		}
		return frames
	}
	if streamError := extractStreamEventError(data); streamError != nil {
		frames = append(frames, errorFrame(streamError, endpointFamily, gatewayproto.TransportSSE, errorRawPaths(data), eventType, rawText))
	}
	switch endpointFamily {
	case gatewayproto.EndpointFamilyChatCompletions:
		frames = append(frames, extractChatSseFrames(data, endpointFamily, eventType, rawText)...)
	case gatewayproto.EndpointFamilyResponses:
		frames = append(frames, extractResponsesSseFrames(data, endpointFamily, eventType, rawText)...)
	default:
		frames = append(frames, extractChatSseFrames(data, endpointFamily, eventType, rawText)...)
		frames = append(frames, extractResponsesSseFrames(data, endpointFamily, eventType, rawText)...)
	}
	var usageValue any
	usagePath := "response.usage"
	if usage, ok := data["usage"]; ok && usage != nil {
		usageValue = usage
		usagePath = "usage"
	} else if response, ok := data["response"].(map[string]any); ok {
		if responseUsage, ok := response["usage"]; ok && responseUsage != nil {
			usageValue = responseUsage
		}
	}
	if usageValue != nil {
		usage := ExtractUsage(usageValue, "")
		if gatewayproto.HasAnyUsageValue(usage) {
			frames = append(frames, gatewayproto.SemanticFrame{
				FrameType:      gatewayproto.FrameTypeUsage,
				Protocol:       ResponseProtocol,
				EndpointFamily: endpointFamily,
				Transport:      gatewayproto.TransportSSE,
				Usage:          usage,
				RawJSONPaths:   []string{usagePath},
				RawText:        rawText,
				EventType:      eventType,
			})
		}
	}
	rawJSON := rawJSONForSseInspection(data, eventType)
	frames = append(frames, rawJSONFrame(rawJSON, endpointFamily, gatewayproto.TransportSSE, eventType, rawText))
	return attachRawJSON(frames, rawJSON, rawText, eventType)
}

// rawJSONForSseInspection mirrors rawJsonForOpenAISseInspection.
func rawJSONForSseInspection(data map[string]any, eventType string) map[string]any {
	if eventType != "response.mcp_call.failed" {
		return data
	}
	rest := make(map[string]any, len(data))
	for key, value := range data {
		if key == "error" {
			continue
		}
		rest[key] = value
	}
	return rest
}

func rawJSONFrame(rawJSON map[string]any, endpointFamily gatewayproto.ResponseEndpointFamily, transport gatewayproto.ResponseTransport, fields ...string) gatewayproto.SemanticFrame {
	frame := gatewayproto.SemanticFrame{
		FrameType:      gatewayproto.FrameTypeRawJSONPath,
		Protocol:       ResponseProtocol,
		EndpointFamily: endpointFamily,
		Transport:      transport,
		RawJSON:        rawJSON,
	}
	// variadic: [eventType], [rawText, eventType] kept for call sites below.
	if len(fields) >= 1 {
		frame.EventType = fields[len(fields)-1]
	}
	if len(fields) >= 2 {
		frame.RawText = fields[0]
	}
	return frame
}

func attachRawJSON(frames []gatewayproto.SemanticFrame, rawJSON map[string]any, fields ...string) []gatewayproto.SemanticFrame {
	rawText := ""
	eventType := ""
	if len(fields) >= 1 {
		eventType = fields[len(fields)-1]
	}
	if len(fields) >= 2 {
		rawText = fields[0]
	}
	for index := range frames {
		if frames[index].RawJSON == nil {
			frames[index].RawJSON = rawJSON
		}
		if frames[index].RawText == "" {
			frames[index].RawText = rawText
		}
		if frames[index].EventType == "" {
			frames[index].EventType = eventType
		}
	}
	return frames
}

// extractChatJSONFrames mirrors extractChatJsonFrames.
func extractChatJSONFrames(root map[string]any) []gatewayproto.SemanticFrame {
	choices, _ := root["choices"].([]any)
	var frames []gatewayproto.SemanticFrame
	for choiceIndex, entry := range choices {
		row, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		message, _ := row["message"].(map[string]any)
		content := textFromOpenAITextValue(message["content"])
		reasoningContent := textFromOpenAITextValue(message["reasoning_content"])
		finishReason, _ := row["finish_reason"].(string)
		if content != "" {
			frames = append(frames, gatewayproto.SemanticFrame{
				FrameType:      gatewayproto.FrameTypeOutputTextDone,
				Protocol:       ResponseProtocol,
				EndpointFamily: gatewayproto.EndpointFamilyChatCompletions,
				Transport:      gatewayproto.TransportJSON,
				Text:           content,
				FinishReason:   finishReason,
				Status:         finishReason,
				RawJSONPaths:   []string{jsonPath("choices", choiceIndex, "message.content")},
				ChoiceIndex:    choiceIndex,
				VisibleOutput:  true,
			})
		}
		if reasoningContent != "" {
			frames = append(frames, gatewayproto.SemanticFrame{
				FrameType:      gatewayproto.FrameTypeOutputTextDone,
				Protocol:       ResponseProtocol,
				EndpointFamily: gatewayproto.EndpointFamilyChatCompletions,
				Transport:      gatewayproto.TransportJSON,
				Text:           reasoningContent,
				FinishReason:   finishReason,
				Status:         finishReason,
				RawJSONPaths:   []string{jsonPath("choices", choiceIndex, "message.reasoning_content")},
				ChoiceIndex:    choiceIndex,
				VisibleOutput:  true,
			})
		}
		if finishReason != "" {
			frames = append(frames, completedFrame(gatewayproto.EndpointFamilyChatCompletions, gatewayproto.TransportJSON, finishReason, "", "", choiceIndex))
		}
	}
	return frames
}

// extractResponsesJSONFrames mirrors extractResponsesJsonFrames.
func extractResponsesJSONFrames(root map[string]any) []gatewayproto.SemanticFrame {
	var frames []gatewayproto.SemanticFrame
	status, _ := root["status"].(string)
	if outputText, ok := root["output_text"].(string); ok && len(outputText) > 0 {
		frames = append(frames, gatewayproto.SemanticFrame{
			FrameType:      gatewayproto.FrameTypeOutputTextDone,
			Protocol:       ResponseProtocol,
			EndpointFamily: gatewayproto.EndpointFamilyResponses,
			Transport:      gatewayproto.TransportJSON,
			Text:           outputText,
			FinishReason:   status,
			Status:         status,
			RawJSONPaths:   []string{"output_text"},
			VisibleOutput:  true,
		})
	}
	output, _ := root["output"].([]any)
	for outputIndex, item := range output {
		outputItem, ok := item.(map[string]any)
		if !ok {
			continue
		}
		content, _ := outputItem["content"].([]any)
		for contentIndex, contentEntry := range content {
			contentItem, ok := contentEntry.(map[string]any)
			if !ok {
				continue
			}
			text := textFromOpenAITextValue(contentItem["text"])
			if text == "" {
				continue
			}
			frames = append(frames, gatewayproto.SemanticFrame{
				FrameType:      gatewayproto.FrameTypeOutputTextDone,
				Protocol:       ResponseProtocol,
				EndpointFamily: gatewayproto.EndpointFamilyResponses,
				Transport:      gatewayproto.TransportJSON,
				Text:           text,
				FinishReason:   status,
				Status:         status,
				RawJSONPaths:   []string{jsonPath("output", outputIndex, "content", contentIndex, "text")},
				OutputIndex:    outputIndex,
				ContentIndex:   contentIndex,
				VisibleOutput:  true,
			})
		}
	}
	if status != "" {
		frames = append(frames, completedFrame(gatewayproto.EndpointFamilyResponses, gatewayproto.TransportJSON, status, "", "", 0))
	}
	return frames
}

// extractChatSseFrames mirrors extractChatSseFrames.
func extractChatSseFrames(data map[string]any, endpointFamily gatewayproto.ResponseEndpointFamily, eventType, rawText string) []gatewayproto.SemanticFrame {
	choices, _ := data["choices"].([]any)
	var frames []gatewayproto.SemanticFrame
	for choiceIndex, entry := range choices {
		row, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		delta, _ := row["delta"].(map[string]any)
		if delta != nil {
			appendChatDelta := func(field, pathField string) {
				if text := textFromOpenAITextValue(delta[field]); text != "" {
					frames = append(frames, gatewayproto.SemanticFrame{
						FrameType:      gatewayproto.FrameTypeOutputTextDelta,
						Protocol:       ResponseProtocol,
						EndpointFamily: endpointFamily,
						Transport:      gatewayproto.TransportSSE,
						Text:           text,
						RawJSONPaths:   []string{jsonPath("choices", choiceIndex, pathField)},
						RawText:        rawText,
						EventType:      eventType,
						ChoiceIndex:    choiceIndex,
						VisibleOutput:  true,
					})
				}
			}
			appendChatDelta("content", "delta.content")
			appendChatDelta("reasoning_content", "delta.reasoning_content")
			appendChatDelta("refusal", "delta.refusal")
		}
		if finishReason, ok := row["finish_reason"].(string); ok {
			frames = append(frames, completedFrame(endpointFamily, gatewayproto.TransportSSE, finishReason, eventType, rawText, choiceIndex))
		}
	}
	return frames
}

// extractResponsesSseFrames mirrors extractResponsesSseFrames.
func extractResponsesSseFrames(data map[string]any, endpointFamily gatewayproto.ResponseEndpointFamily, eventType, rawText string) []gatewayproto.SemanticFrame {
	var frames []gatewayproto.SemanticFrame
	isCompletedFamily := eventType == "response.completed" || eventType == "response.done" || eventType == "response.incomplete"
	if eventType == "response.output_text.delta" {
		if text := textFromOpenAITextValue(data["delta"]); text != "" {
			frames = append(frames, gatewayproto.SemanticFrame{
				FrameType:      gatewayproto.FrameTypeOutputTextDelta,
				Protocol:       ResponseProtocol,
				EndpointFamily: endpointFamily,
				Transport:      gatewayproto.TransportSSE,
				Text:           text,
				RawJSONPaths:   []string{"delta"},
				RawText:        rawText,
				EventType:      eventType,
				VisibleOutput:  true,
			})
		}
	}
	if eventType == "response.output_text.done" {
		if text := textFromOpenAITextValue(data["text"]); text != "" {
			frames = append(frames, gatewayproto.SemanticFrame{
				FrameType:      gatewayproto.FrameTypeOutputTextDone,
				Protocol:       ResponseProtocol,
				EndpointFamily: endpointFamily,
				Transport:      gatewayproto.TransportSSE,
				Text:           text,
				RawJSONPaths:   []string{"text"},
				RawText:        rawText,
				EventType:      eventType,
				VisibleOutput:  true,
			})
		}
	}
	response, _ := data["response"].(map[string]any)
	if response != nil && isCompletedFamily {
		for _, frame := range extractResponsesJSONFrames(response) {
			frame.Transport = gatewayproto.TransportSSE
			frame.RawText = rawText
			frame.EventType = eventType
			frames = append(frames, frame)
		}
	}
	if isCompletedFamily || eventType == "response.failed" {
		status := ""
		if response != nil {
			status, _ = response["status"].(string)
		}
		if status == "" {
			if eventType == "response.failed" {
				status = "failed"
			} else {
				status = strings.TrimPrefix(eventType, "response.")
			}
		}
		frames = append(frames, completedFrame(endpointFamily, gatewayproto.TransportSSE, status, eventType, rawText, 0))
	}
	return frames
}

func errorFrame(streamError map[string]any, endpointFamily gatewayproto.ResponseEndpointFamily, transport gatewayproto.ResponseTransport, rawJSONPaths []string, fields ...string) gatewayproto.SemanticFrame {
	frame := gatewayproto.SemanticFrame{
		FrameType:      gatewayproto.FrameTypeError,
		Protocol:       ResponseProtocol,
		EndpointFamily: endpointFamily,
		Transport:      transport,
		RawJSONPaths:   rawJSONPaths,
	}
	if code, ok := streamError["code"].(string); ok {
		frame.ErrorCode = code
	}
	if errorType, ok := streamError["type"].(string); ok {
		frame.ErrorType = errorType
	}
	if message, ok := streamError["message"].(string); ok {
		frame.ErrorMessage = message
	}
	if len(fields) >= 1 {
		frame.EventType = fields[len(fields)-1]
	}
	if len(fields) >= 2 {
		frame.RawText = fields[0]
	}
	return frame
}

func completedFrame(endpointFamily gatewayproto.ResponseEndpointFamily, transport gatewayproto.ResponseTransport, finishReason, eventType, rawText string, choiceIndex int) gatewayproto.SemanticFrame {
	return gatewayproto.SemanticFrame{
		FrameType:      gatewayproto.FrameTypeCompleted,
		Protocol:       ResponseProtocol,
		EndpointFamily: endpointFamily,
		Transport:      transport,
		FinishReason:   finishReason,
		Status:         finishReason,
		RawText:        rawText,
		EventType:      eventType,
		ChoiceIndex:    choiceIndex,
	}
}

func errorRawPaths(data map[string]any) []string {
	var paths []string
	if _, ok := data["error"].(map[string]any); ok {
		paths = append(paths, "error")
	}
	if response, ok := data["response"].(map[string]any); ok {
		if _, ok := response["error"].(map[string]any); ok {
			paths = append(paths, "response.error")
		}
	}
	if len(paths) == 0 {
		_, hasCode := data["code"].(string)
		_, hasMessage := data["message"].(string)
		if hasCode || hasMessage {
			paths = append(paths, "error")
		}
	}
	return paths
}

// textFromOpenAITextValue mirrors textFromOpenAITextValue.
func textFromOpenAITextValue(value any) string {
	if text, ok := value.(string); ok {
		if len(text) > 0 {
			return text
		}
		return ""
	}
	array, ok := value.([]any)
	if !ok {
		return ""
	}
	var parts []string
	for _, item := range array {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if text, ok := entry["text"].(string); ok && len(text) > 0 {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "")
}

func jsonPath(parts ...any) string {
	rendered := make([]string, len(parts))
	for index, part := range parts {
		switch typed := part.(type) {
		case string:
			rendered[index] = typed
		case int:
			rendered[index] = strconv.Itoa(typed)
		}
	}
	return strings.Join(rendered, ".")
}

// JSONToolCallDetected reports whether a buffered JSON response carries
// tool-call / structured output evidence: chat-completions choices with
// message.tool_calls or Responses output items of a callable type.
func JSONToolCallDetected(value any) bool {
	root, ok := value.(map[string]any)
	if !ok {
		return false
	}
	choices, _ := root["choices"].([]any)
	for _, entry := range choices {
		row, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		if message, ok := row["message"].(map[string]any); ok {
			if hasMeaningfulDelta(message["tool_calls"]) {
				return true
			}
		}
		if hasMeaningfulDelta(row["tool_calls"]) {
			return true
		}
	}
	output, _ := root["output"].([]any)
	for _, entry := range output {
		item, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		if typeText, _ := item["type"].(string); isResponsesCallableOutputItemType(typeText) {
			return true
		}
	}
	return false
}
