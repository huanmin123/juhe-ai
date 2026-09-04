package gatewayanthropic

import (
	"encoding/json"
	"strconv"
	"strings"
)

// 响应语义协议标识（对齐 Node driver 的 responseProtocol 'anthropic_v1'）。
const ResponseProtocol = "anthropic_v1"

// EndpointFamily 对齐 AnthropicResponseEndpointFamily。
type EndpointFamily string

const (
	EndpointFamilyMessages          EndpointFamily = "messages"
	EndpointFamilyModels            EndpointFamily = "models"
	EndpointFamilyMessageTokenCount EndpointFamily = "message_token_counting"
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
// 可选字段使用指针以区分「未设置」与「零值」。
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

// ResponseEndpointFamilyFromPath 对齐 anthropicResponseEndpointFamilyFromPath。
func ResponseEndpointFamilyFromPath(pathAndQuery string) EndpointFamily {
	path := normalizedAnthropicPath(pathAndQuery)
	if path == "/messages/count_tokens" {
		return EndpointFamilyMessageTokenCount
	}
	if path == "/models" {
		return EndpointFamilyModels
	}
	return EndpointFamilyMessages
}

// ExtractJSONSemanticFrames 对齐 extractAnthropicJsonSemanticFrames。
func ExtractJSONSemanticFrames(value any, endpointFamily EndpointFamily) []ResponseSemanticFrame {
	root, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	frames := []ResponseSemanticFrame{}
	rootError, hasRootError := root["error"].(map[string]any)
	if hasRootError || root["type"] == "error" {
		errorObject := root
		paths := []string{}
		if hasRootError {
			errorObject = rootError
			paths = []string{"error"}
		}
		frames = append(frames, errorFrame(errorObject, endpointFamily, TransportJSON, paths, "", ""))
	}
	if endpointFamily == EndpointFamilyMessages {
		frames = append(frames, extractMessageJSONFrames(root, endpointFamily)...)
	}
	if usage := ExtractUsage(root["usage"]); HasAnyUsageValue(usage) {
		frames = append(frames, ResponseSemanticFrame{
			FrameType:      FrameTypeUsage,
			Protocol:       ResponseProtocol,
			EndpointFamily: string(endpointFamily),
			Transport:      TransportJSON,
			Usage:          &usage,
			RawJSONPaths:   []string{"usage"},
		})
	}
	frames = append(frames, rawJSONFrame(root, endpointFamily, TransportJSON, "", ""))
	return attachRawJSON(frames, root, "", "")
}

// ExtractSSESemanticFrames 对齐 extractAnthropicSseSemanticFrames。
func ExtractSSESemanticFrames(event StreamEvent, endpointFamily EndpointFamily) []ResponseSemanticFrame {
	data := event.Data
	eventType := event.EventType
	if eventType == "" {
		eventType = event.EventName
	}
	if eventType == "" {
		eventType = "message"
	}
	rawText := event.RawText
	if rawText == "" {
		rawText = event.DataText
	}
	frames := []ResponseSemanticFrame{}
	if data == nil {
		return frames
	}

	if errorObject := ExtractStreamEventError(data, eventType, event.EventName); errorObject != nil {
		frames = append(frames, errorFrame(errorObject, endpointFamily, TransportSSE, errorRawPaths(data), eventType, rawText))
	}

	if endpointFamily == EndpointFamilyMessages {
		frames = append(frames, extractMessageSSEFrames(data, endpointFamily, eventType, rawText)...)
	}

	if usage := extractEventUsage(data); HasAnyUsageValue(usage) {
		frames = append(frames, ResponseSemanticFrame{
			FrameType:      FrameTypeUsage,
			Protocol:       ResponseProtocol,
			EndpointFamily: string(endpointFamily),
			Transport:      TransportSSE,
			Usage:          &usage,
			RawJSONPaths:   usageRawPaths(data),
			RawText:        rawText,
			EventType:      eventType,
		})
	}
	frames = append(frames, rawJSONFrame(data, endpointFamily, TransportSSE, eventType, rawText))
	return attachRawJSON(frames, data, rawText, eventType)
}

// ExtractStreamEventError 对齐 extractAnthropicStreamEventError。
func ExtractStreamEventError(data map[string]any, eventType, eventName string) map[string]any {
	if eventType != "error" && eventName != "error" && data["type"] != "error" {
		return nil
	}
	if errorObject, ok := data["error"].(map[string]any); ok {
		return errorObject
	}
	return data
}

func extractMessageJSONFrames(root map[string]any, endpointFamily EndpointFamily) []ResponseSemanticFrame {
	frames := []ResponseSemanticFrame{}
	content, _ := root["content"].([]any)
	stopReason := stringValue(root["stop_reason"])
	for contentIndex, entry := range content {
		item, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		index := contentIndex
		if itemType, _ := item["type"].(string); itemType == "text" {
			if text, _ := item["text"].(string); text != "" {
				frames = append(frames, ResponseSemanticFrame{
					FrameType:      FrameTypeOutputTextDone,
					Protocol:       ResponseProtocol,
					EndpointFamily: string(endpointFamily),
					Transport:      TransportJSON,
					Text:           text,
					FinishReason:   stopReason,
					Status:         stopReason,
					RawJSONPaths:   []string{"content." + strconv.Itoa(contentIndex) + ".text"},
					ContentIndex:   &index,
					VisibleOutput:  boolPtr(true),
				})
			}
		}
		if itemType, _ := item["type"].(string); itemType == "thinking" {
			if thinking, _ := item["thinking"].(string); thinking != "" {
				frames = append(frames, ResponseSemanticFrame{
					FrameType:      FrameTypeOutputTextDone,
					Protocol:       ResponseProtocol,
					EndpointFamily: string(endpointFamily),
					Transport:      TransportJSON,
					Text:           thinking,
					FinishReason:   stopReason,
					Status:         stopReason,
					RawJSONPaths:   []string{"content." + strconv.Itoa(contentIndex) + ".thinking"},
					ContentIndex:   &index,
					VisibleOutput:  boolPtr(false),
				})
			}
		}
		if itemType, _ := item["type"].(string); itemType == "tool_use" {
			frames = append(frames, ResponseSemanticFrame{
				FrameType:      FrameTypeRawJSONPath,
				Protocol:       ResponseProtocol,
				EndpointFamily: string(endpointFamily),
				Transport:      TransportJSON,
				RawJSONPaths:   []string{"content." + strconv.Itoa(contentIndex)},
				ContentIndex:   &index,
				VisibleOutput:  boolPtr(false),
			})
		}
	}
	if stopReason != "" {
		frames = append(frames, completedFrame(endpointFamily, TransportJSON, stopReason, "", "", nil))
	}
	return frames
}

func extractMessageSSEFrames(data map[string]any, endpointFamily EndpointFamily, eventType, rawText string) []ResponseSemanticFrame {
	frames := []ResponseSemanticFrame{}
	if eventType == "content_block_start" {
		if block, ok := data["content_block"].(map[string]any); ok && block["type"] == "tool_use" {
			frames = append(frames, ResponseSemanticFrame{
				FrameType:      FrameTypeRawJSONPath,
				Protocol:       ResponseProtocol,
				EndpointFamily: string(endpointFamily),
				Transport:      TransportSSE,
				RawJSONPaths:   []string{"content_block"},
				RawText:        rawText,
				EventType:      eventType,
				ContentIndex:   numberPtr(data["index"]),
				VisibleOutput:  boolPtr(false),
			})
		}
	}
	if eventType == "content_block_delta" {
		delta, _ := data["delta"].(map[string]any)
		text := anthropicDeltaText(delta)
		if text != "" {
			path := "delta.text"
			if deltaType, _ := delta["type"].(string); deltaType == "input_json_delta" {
				path = "delta.partial_json"
			} else if deltaType == "thinking_delta" {
				path = "delta.thinking"
			}
			visible := true
			if deltaType, _ := delta["type"].(string); deltaType == "thinking_delta" {
				visible = false
			}
			frames = append(frames, ResponseSemanticFrame{
				FrameType:      FrameTypeOutputTextDelta,
				Protocol:       ResponseProtocol,
				EndpointFamily: string(endpointFamily),
				Transport:      TransportSSE,
				Text:           text,
				RawJSONPaths:   []string{path},
				RawText:        rawText,
				EventType:      eventType,
				ContentIndex:   numberPtr(data["index"]),
				VisibleOutput:  &visible,
			})
		}
	}
	if eventType == "message_delta" {
		delta, _ := data["delta"].(map[string]any)
		if stopReason := stringValue(delta["stop_reason"]); stopReason != "" {
			frames = append(frames, completedFrame(endpointFamily, TransportSSE, stopReason, eventType, rawText, nil))
		}
	}
	if eventType == "message_stop" {
		frames = append(frames, completedFrame(endpointFamily, TransportSSE, "message_stop", eventType, rawText, nil))
	}
	return frames
}

func anthropicDeltaText(delta map[string]any) string {
	if delta == nil {
		return ""
	}
	switch deltaType, _ := delta["type"].(string); deltaType {
	case "text_delta":
		if text, _ := delta["text"].(string); text != "" {
			return text
		}
	case "input_json_delta":
		if partial, _ := delta["partial_json"].(string); partial != "" {
			return partial
		}
	case "thinking_delta":
		if thinking, _ := delta["thinking"].(string); thinking != "" {
			return thinking
		}
	}
	return ""
}

// extractEventUsage 对齐 response-semantics.ts 的 extractAnthropicEventUsage：
// 优先 data.usage，其次 data.message.usage。
func extractEventUsage(data map[string]any) ParsedUsage {
	if usage, ok := data["usage"].(map[string]any); ok {
		return ExtractUsage(usage)
	}
	message, _ := data["message"].(map[string]any)
	if messageUsage, ok := message["usage"].(map[string]any); ok {
		return ExtractUsage(messageUsage)
	}
	return EmptyUsage()
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

// attachRawJSON 对齐 attachRawJson：为未设置的 frame 回填根对象、rawText 与 eventType。
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
		ErrorCode:      orString(stringValue(errorObject["code"]), stringValue(errorObject["type"])),
		ErrorType:      stringValue(errorObject["type"]),
		ErrorMessage:   stringValue(errorObject["message"]),
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

func errorRawPaths(data map[string]any) []string {
	if _, ok := data["error"].(map[string]any); ok {
		return []string{"error"}
	}
	return []string{}
}

func usageRawPaths(data map[string]any) []string {
	if _, ok := data["usage"].(map[string]any); ok {
		return []string{"usage"}
	}
	if message, ok := data["message"].(map[string]any); ok {
		if _, ok := message["usage"].(map[string]any); ok {
			return []string{"message.usage"}
		}
	}
	return []string{}
}

// normalizedAnthropicPath 对齐 normalizedAnthropicPath：去 query、补前导斜杠、
// 去掉开头的 /v1 段。
func normalizedAnthropicPath(pathAndQuery string) string {
	path := splitPathAndQuery(pathAndQuery).Path
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	path = stripV1Prefix(path)
	if path == "" {
		return "/"
	}
	return path
}

// stripV1Prefix 对齐 JS replace(/^\/v1(?=\/|$)/, ”)：仅剥离 /v1 后紧跟
// 斜杠或结尾的前缀。
func stripV1Prefix(path string) string {
	if !strings.HasPrefix(path, "/v1") {
		return path
	}
	rest := path[len("/v1"):]
	if rest == "" || strings.HasPrefix(rest, "/") {
		return rest
	}
	return path
}

func splitPathAndQuery(pathAndQuery string) (result struct{ Path, Query string }) {
	if index := strings.Index(pathAndQuery, "?"); index >= 0 {
		result.Path = pathAndQuery[:index]
		result.Query = pathAndQuery[index:]
		return result
	}
	result.Path = pathAndQuery
	return result
}

// stringValue 对齐 response-semantics.ts 的 stringValue：非空字符串才有效。
func stringValue(value any) string {
	text, ok := value.(string)
	if !ok || text == "" {
		return ""
	}
	return text
}

// stringFieldValue 宽松字符串：字符串去空白、数字/布尔转字符串（对齐
// error-payload.ts 的 stringErrorField 语义，供错误负载复用）。
func stringFieldValue(value any) string {
	switch typed := value.(type) {
	case string:
		trimmed := strings.TrimSpace(typed)
		return trimmed
	case float64:
		if typed == float64(int64(typed)) {
			return strconv.FormatInt(int64(typed), 10)
		}
		return strconv.FormatFloat(typed, 'g', -1, 64)
	case bool:
		return strconv.FormatBool(typed)
	default:
		return ""
	}
}

func numberPtr(value any) *int {
	switch typed := value.(type) {
	case float64:
		number := int(typed)
		return &number
	case string:
		if parsed, err := strconv.Atoi(typed); err == nil {
			return &parsed
		}
	}
	return nil
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

// decodeJSONIntoMap 供测试与调用方把 JSON 文本解码为语义提取输入。
func decodeJSONIntoMap(text string) (map[string]any, error) {
	var value map[string]any
	if err := json.Unmarshal([]byte(text), &value); err != nil {
		return nil, err
	}
	return value, nil
}
