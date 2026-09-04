package gatewaygemini

import (
	"strconv"
	"strings"
)

// 响应语义协议标识（对齐 Node driver 的 responseProtocol 'gemini_v1beta'）。
const ResponseProtocol = "gemini_v1beta"

// EndpointFamily 对齐 GeminiEndpointFamily（domain/gemini-endpoint-modes.ts）。
type EndpointFamily string

const (
	EndpointFamilyModels                EndpointFamily = "models"
	EndpointFamilyGenerateContent       EndpointFamily = "generate_content"
	EndpointFamilyStreamGenerateContent EndpointFamily = "stream_generate_content"
	EndpointFamilyCountTokens           EndpointFamily = "count_tokens"
	EndpointFamilyEmbedContent          EndpointFamily = "embed_content"
	EndpointFamilyInteractions          EndpointFamily = "interactions"
)

// TransportIdent 对齐 'json' | 'sse'。
type TransportIdent string

const (
	TransportJSON TransportIdent = "json"
	TransportSSE  TransportIdent = "sse"
)

// 帧类型（对齐 Node openai-v1/response-semantics.ts 的 ResponseSemanticFrameType）。
const (
	FrameTypeOutputTextDelta = "output_text_delta"
	FrameTypeOutputTextDone  = "output_text_done"
	FrameTypeCompleted       = "completed"
	FrameTypeError           = "error"
	FrameTypeUsage           = "usage"
	FrameTypeRawJSONPath     = "raw_json_path"
)

// ResponseSemanticFrame 对齐 Node 的 ResponseSemanticFrame。
type ResponseSemanticFrame struct {
	FrameType      string
	Protocol       string
	EndpointFamily string
	Transport      TransportIdent
	Text           string
	ErrorCode      string
	ErrorType      string
	ErrorMessage   string
	FinishReason   string
	Status         string
	Usage          *ParsedUsage
	RawJSON        any
	RawJSONPaths   []string
	RawText        string
	EventType      string
	ChoiceIndex    *int
	OutputIndex    *int
	StepIndex      *int
	ContentIndex   *int
	VisibleOutput  *bool
	Provenance     string
}

// ResponseEndpointFamilyFromPath 对齐 geminiResponseEndpointFamilyFromPath：
// 无法识别时回退 generate_content。
func ResponseEndpointFamilyFromPath(pathAndQuery string) EndpointFamily {
	if family := EndpointFamilyFromPath(pathAndQuery); family != "" {
		return family
	}
	return EndpointFamilyGenerateContent
}

// ExtractJSONSemanticFrames 对齐 extractGeminiJsonSemanticFrames。
func ExtractJSONSemanticFrames(value any, endpointFamily EndpointFamily) []ResponseSemanticFrame {
	root, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	frames := []ResponseSemanticFrame{}
	if rootError, ok := root["error"].(map[string]any); ok {
		frames = append(frames, errorFrame(rootError, endpointFamily, TransportJSON, []string{"error"}, "", ""))
	}
	if endpointFamily == EndpointFamilyInteractions {
		frames = append(frames, extractInteractionsJSONFrames(root, endpointFamily)...)
	} else {
		frames = append(frames, extractGenerateContentFrames(root, endpointFamily, TransportJSON, "", "")...)
	}
	if usage := ExtractUsage(root); HasAnyUsageValue(usage) {
		frames = append(frames, usageFrame(&usage, endpointFamily, TransportJSON, usageRawJSONPaths(endpointFamily), "", ""))
	}
	frames = append(frames, rawJSONFrame(root, endpointFamily, TransportJSON, "", ""))
	return attachRawJSON(frames, root, "", "")
}

// ExtractSSESemanticFrames 对齐 extractGeminiSseSemanticFrames。
func ExtractSSESemanticFrames(event StreamEvent, endpointFamily EndpointFamily) []ResponseSemanticFrame {
	data := event.Data
	eventType := orString(event.EventType, event.EventName, "message")
	rawText := orString(event.RawText, event.DataText)
	frames := []ResponseSemanticFrame{}
	if data == nil {
		return frames
	}
	if errorObject := ExtractStreamEventError(data, eventType, event.EventName); errorObject != nil {
		frames = append(frames, errorFrame(errorObject, endpointFamily, TransportSSE, []string{"error"}, eventType, rawText))
	}
	if endpointFamily == EndpointFamilyInteractions {
		frames = append(frames, extractInteractionsSSEFrames(data, endpointFamily, eventType, rawText)...)
	} else {
		frames = append(frames, extractGenerateContentFrames(data, endpointFamily, TransportSSE, eventType, rawText)...)
	}
	if usage := ExtractUsage(data); HasAnyUsageValue(usage) {
		frames = append(frames, usageFrame(&usage, endpointFamily, TransportSSE, usageRawJSONPaths(endpointFamily), eventType, rawText))
	}
	frames = append(frames, rawJSONFrame(data, endpointFamily, TransportSSE, eventType, rawText))
	return attachRawJSON(frames, data, rawText, eventType)
}

// ExtractStreamEventError 对齐 extractGeminiStreamEventError。
func ExtractStreamEventError(data map[string]any, eventType, eventName string) map[string]any {
	explicitFailure := eventType == "error" ||
		eventName == "error" ||
		data["type"] == "error" ||
		eventType == "interaction.failed" ||
		eventName == "interaction.failed"
	if !explicitFailure {
		return nil
	}
	if interaction, ok := data["interaction"].(map[string]any); ok {
		if interactionError, ok := interaction["error"].(map[string]any); ok {
			return interactionError
		}
	}
	if errorObject, ok := data["error"].(map[string]any); ok {
		return errorObject
	}
	if interaction, ok := data["interaction"].(map[string]any); ok {
		return interaction
	}
	return data
}

func extractInteractionsJSONFrames(root map[string]any, endpointFamily EndpointFamily) []ResponseSemanticFrame {
	frames := []ResponseSemanticFrame{}
	steps, _ := root["steps"].([]any)
	status := stringField(root["status"])
	for stepIndex, step := range steps {
		row, ok := step.(map[string]any)
		if !ok {
			continue
		}
		content, _ := row["content"].([]any)
		stepIndexValue := stepIndex
		for contentIndex, item := range content {
			part, ok := item.(map[string]any)
			if !ok {
				continue
			}
			text := stringField(part["text"])
			if text == "" {
				continue
			}
			rowType, _ := row["type"].(string)
			visible := rowType != "thought" && rowType != "thought_summary"
			contentIndexValue := contentIndex
			frames = append(frames, ResponseSemanticFrame{
				FrameType:      FrameTypeOutputTextDone,
				Protocol:       ResponseProtocol,
				EndpointFamily: string(endpointFamily),
				Transport:      TransportJSON,
				Text:           text,
				Status:         status,
				RawJSONPaths:   []string{"steps." + strconv.Itoa(stepIndex) + ".content." + strconv.Itoa(contentIndex) + ".text"},
				StepIndex:      &stepIndexValue,
				ContentIndex:   &contentIndexValue,
				VisibleOutput:  &visible,
			})
		}
	}
	if status != "" {
		frames = append(frames, completedFrame(endpointFamily, TransportJSON, status, "", "", nil))
	}
	return frames
}

func extractInteractionsSSEFrames(data map[string]any, endpointFamily EndpointFamily, eventType, rawText string) []ResponseSemanticFrame {
	frames := []ResponseSemanticFrame{}
	if eventType == "step.delta" {
		delta, _ := data["delta"].(map[string]any)
		if text := stringField(delta["text"]); text != "" {
			visible := false
			if deltaType, _ := delta["type"].(string); deltaType == "text" {
				visible = true
			}
			frames = append(frames, ResponseSemanticFrame{
				FrameType:      FrameTypeOutputTextDelta,
				Protocol:       ResponseProtocol,
				EndpointFamily: string(endpointFamily),
				Transport:      TransportSSE,
				Text:           text,
				RawJSONPaths:   []string{"delta.text"},
				RawText:        rawText,
				EventType:      eventType,
				VisibleOutput:  &visible,
			})
		}
	}
	if eventType == "interaction.completed" || eventType == "interaction.failed" {
		interaction, _ := data["interaction"].(map[string]any)
		status := orString(stringField(interaction["status"]), strings.TrimPrefix(eventType, "interaction."))
		if eventType == "interaction.failed" {
			var errorObject map[string]any
			if interactionError, ok := interaction["error"].(map[string]any); ok {
				errorObject = interactionError
			} else if dataError, ok := data["error"].(map[string]any); ok {
				errorObject = dataError
			}
			if errorObject != nil {
				frames = append(frames, errorFrame(errorObject, endpointFamily, TransportSSE, []string{"interaction.error"}, eventType, rawText))
			}
		}
		if status != "" {
			frames = append(frames, completedFrame(endpointFamily, TransportSSE, status, eventType, rawText, nil))
		}
	}
	return frames
}

func extractGenerateContentFrames(root map[string]any, endpointFamily EndpointFamily, transport TransportIdent, eventType, rawText string) []ResponseSemanticFrame {
	candidates, _ := root["candidates"].([]any)
	frames := []ResponseSemanticFrame{}
	for choiceIndex, candidate := range candidates {
		row, ok := candidate.(map[string]any)
		if !ok {
			continue
		}
		choiceIndexValue := choiceIndex
		content, _ := row["content"].(map[string]any)
		parts, _ := content["parts"].([]any)
		for contentIndex, part := range parts {
			item, ok := part.(map[string]any)
			if !ok {
				continue
			}
			contentIndexValue := contentIndex
			if text := stringField(item["text"]); text != "" {
				visible := item["thought"] != true
				frames = append(frames, ResponseSemanticFrame{
					FrameType:      frameTypeForTransport(transport),
					Protocol:       ResponseProtocol,
					EndpointFamily: string(endpointFamily),
					Transport:      transport,
					Text:           text,
					FinishReason:   stringField(row["finishReason"]),
					Status:         stringField(row["finishReason"]),
					RawJSONPaths:   []string{"candidates." + strconv.Itoa(choiceIndex) + ".content.parts." + strconv.Itoa(contentIndex) + ".text"},
					RawText:        rawText,
					EventType:      eventType,
					ChoiceIndex:    &choiceIndexValue,
					ContentIndex:   &contentIndexValue,
					VisibleOutput:  &visible,
				})
			}
			if hasObject(item, "functionCall", "inlineData", "fileData", "executableCode") {
				frames = append(frames, ResponseSemanticFrame{
					FrameType:      FrameTypeRawJSONPath,
					Protocol:       ResponseProtocol,
					EndpointFamily: string(endpointFamily),
					Transport:      transport,
					RawJSONPaths:   []string{"candidates." + strconv.Itoa(choiceIndex) + ".content.parts." + strconv.Itoa(contentIndex)},
					RawText:        rawText,
					EventType:      eventType,
					ChoiceIndex:    &choiceIndexValue,
					ContentIndex:   &contentIndexValue,
					VisibleOutput:  boolPtr(false),
				})
			}
		}
		if finishReason := stringField(row["finishReason"]); finishReason != "" {
			choice := choiceIndexValue
			frames = append(frames, completedFrame(endpointFamily, transport, finishReason, eventType, rawText, &choice))
		}
	}
	return frames
}

func frameTypeForTransport(transport TransportIdent) string {
	if transport == TransportSSE {
		return FrameTypeOutputTextDelta
	}
	return FrameTypeOutputTextDone
}

func hasObject(value map[string]any, keys ...string) bool {
	for _, key := range keys {
		if _, ok := value[key].(map[string]any); ok {
			return true
		}
	}
	return false
}

func rawJSONFrame(rawJSON any, endpointFamily EndpointFamily, transport TransportIdent, eventType, rawText string) ResponseSemanticFrame {
	return ResponseSemanticFrame{
		FrameType:      FrameTypeRawJSONPath,
		Protocol:       ResponseProtocol,
		EndpointFamily: string(endpointFamily),
		Transport:      transport,
		RawJSON:        rawJSON,
		RawText:        rawText,
		EventType:      eventType,
	}
}

func usageFrame(usage *ParsedUsage, endpointFamily EndpointFamily, transport TransportIdent, rawJSONPaths []string, eventType, rawText string) ResponseSemanticFrame {
	return ResponseSemanticFrame{
		FrameType:      FrameTypeUsage,
		Protocol:       ResponseProtocol,
		EndpointFamily: string(endpointFamily),
		Transport:      transport,
		Usage:          usage,
		RawJSONPaths:   rawJSONPaths,
		RawText:        rawText,
		EventType:      eventType,
	}
}

// usageRawJSONPaths 对齐 geminiUsageRawJsonPaths。
func usageRawJSONPaths(endpointFamily EndpointFamily) []string {
	if endpointFamily == EndpointFamilyInteractions {
		return []string{"metadata.total_usage"}
	}
	return []string{"usageMetadata"}
}

// attachRawJSON 对齐 attachRawJson。
func attachRawJSON(frames []ResponseSemanticFrame, rawJSON any, rawText, eventType string) []ResponseSemanticFrame {
	for i := range frames {
		if frames[i].RawJSON == nil {
			frames[i].RawJSON = rawJSON
		}
		if frames[i].RawText == "" {
			frames[i].RawText = rawText
		}
		if frames[i].EventType == "" {
			frames[i].EventType = eventType
		}
	}
	return frames
}

func errorFrame(errorObject map[string]any, endpointFamily EndpointFamily, transport TransportIdent, rawJSONPaths []string, eventType, rawText string) ResponseSemanticFrame {
	return ResponseSemanticFrame{
		FrameType:      FrameTypeError,
		Protocol:       ResponseProtocol,
		EndpointFamily: string(endpointFamily),
		Transport:      transport,
		ErrorCode:      orString(stringField(errorObject["code"]), stringField(errorObject["status"])),
		ErrorType:      stringField(errorObject["status"]),
		ErrorMessage:   stringField(errorObject["message"]),
		RawJSONPaths:   rawJSONPaths,
		RawText:        rawText,
		EventType:      eventType,
	}
}

func completedFrame(endpointFamily EndpointFamily, transport TransportIdent, finishReason, eventType, rawText string, choiceIndex *int) ResponseSemanticFrame {
	return ResponseSemanticFrame{
		FrameType:      FrameTypeCompleted,
		Protocol:       ResponseProtocol,
		EndpointFamily: string(endpointFamily),
		Transport:      transport,
		FinishReason:   finishReason,
		Status:         finishReason,
		RawText:        rawText,
		EventType:      eventType,
		ChoiceIndex:    choiceIndex,
	}
}

func boolPtr(value bool) *bool { return &value }

func orString(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
