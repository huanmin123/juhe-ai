package gatewayanthropic

import (
	"encoding/json"
	"strings"
)

// StreamEvent 对齐 Node openai-v1/stream-events.ts 的 ParsedOpenAIStreamEvent。
// Anthropic 协议切片复用同一 SSE 事件解析语义（Node 中 Anthropic 亦复用该解析）。
type StreamEvent struct {
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

// ParseSSEEventText 对齐 parseOpenAISseEventText：解析一段完整的 SSE 事件文本。
func ParseSSEEventText(rawText string) StreamEvent {
	eventName := ""
	var dataLines []string
	dataBytes := 0
	for _, line := range splitSSELines(rawText) {
		if strings.HasPrefix(line, "event:") {
			eventName = strings.TrimSpace(line[len("event:"):])
		} else if strings.HasPrefix(line, "data:") {
			dataLine := trimStart(line[len("data:"):])
			dataLines = append(dataLines, dataLine)
			dataBytes += len(dataLine)
		}
	}
	joined := strings.TrimSpace(strings.Join(dataLines, "\n"))
	return ParseSSEEventData(joined, eventName, rawText, dataBytes)
}

// ParseSSEEventData 对齐 parseOpenAIStreamEventData。
func ParseSSEEventData(dataText, eventName, rawText string, dataBytes int) StreamEvent {
	if dataBytes == 0 {
		dataBytes = len(dataText)
	}
	if dataText == "" || dataText == "[DONE]" {
		eventType := eventName
		if dataText == "[DONE]" {
			eventType = "[DONE]"
		}
		return StreamEvent{
			RawText:   rawText,
			EventName: eventName,
			DataText:  dataText,
			DataBytes: dataBytes,
			EventType: eventType,
		}
	}
	var parsed any
	if err := json.Unmarshal([]byte(dataText), &parsed); err != nil {
		return StreamEvent{
			RawText:        rawText,
			EventName:      eventName,
			DataText:       dataText,
			DataBytes:      dataBytes,
			DataParseError: true,
			EventType:      eventName,
		}
	}
	// Node 中 JSON.parse 成功但结果不是对象时仍视为解析成功，
	// 只是后续对象字段访问全部落空。
	data, _ := parsed.(map[string]any)
	eventType := eventName
	if text, ok := data["type"].(string); ok && text != "" {
		eventType = text
	} else if text, ok := data["event_type"].(string); ok && text != "" {
		eventType = text
	}
	errorCode, errorMessage := openAIStreamEventErrorFields(data)
	return StreamEvent{
		RawText:      rawText,
		EventName:    eventName,
		DataText:     dataText,
		DataBytes:    dataBytes,
		Data:         data,
		EventType:    eventType,
		ErrorCode:    errorCode,
		ErrorMessage: errorMessage,
	}
}

// openAIStreamEventErrorFields 对齐 extractOpenAIStreamEventError 的 code/message 提取，
// 供解析层填充 StreamEvent.errorCode/errorMessage。
func openAIStreamEventErrorFields(data map[string]any) (string, string) {
	if data["type"] == "response.mcp_call.failed" {
		return "", ""
	}
	if response, ok := data["response"].(map[string]any); ok {
		if errObj, ok := response["error"].(map[string]any); ok {
			return stringField(errObj["code"]), stringField(errObj["message"])
		}
	}
	if errObj, ok := data["error"].(map[string]any); ok {
		return stringField(errObj["code"]), stringField(errObj["message"])
	}
	if data["type"] == "error" {
		code := stringField(data["code"])
		message := stringField(data["message"])
		if code != "" || message != "" {
			return code, message
		}
	}
	return "", ""
}

// EstimateTokenCountFromText 对齐 estimateTokenCountFromText：
// CJK 字符按 1 token、ASCII 按 4 字符 1 token、其余按 2 字符 1 token 估算。
func EstimateTokenCountFromText(text string) int {
	if strings.TrimSpace(text) == "" {
		return 0
	}
	asciiLikeChars := 0
	cjkChars := 0
	otherChars := 0
	for _, code := range text {
		switch {
		case isCJKCodePoint(code):
			cjkChars++
		case code <= 0x7f:
			asciiLikeChars++
		default:
			otherChars++
		}
	}
	estimate := ceilDiv(asciiLikeChars, 4) + cjkChars + ceilDiv(otherChars, 2)
	if estimate < 1 {
		return 1
	}
	return estimate
}

func ceilDiv(value, divisor int) int {
	if divisor <= 0 || value <= 0 {
		return 0
	}
	return (value + divisor - 1) / divisor
}

func isCJKCodePoint(code rune) bool {
	return (code >= 0x3400 && code <= 0x9fff) ||
		(code >= 0xf900 && code <= 0xfaff) ||
		(code >= 0x20000 && code <= 0x2ebef)
}

// stringField 严格字符串提取（对齐 Node 的 typeof === 'string' 判断，允许空串）。
func stringField(value any) string {
	text, _ := value.(string)
	return text
}

// splitSSELines 对齐 JS 的 rawText.split(/\r?\n|\r/)。
func splitSSELines(rawText string) []string {
	lines := []string{}
	current := strings.Builder{}
	runes := []rune(rawText)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		switch r {
		case '\r':
			lines = append(lines, current.String())
			current.Reset()
			if i+1 < len(runes) && runes[i+1] == '\n' {
				i++
			}
		case '\n':
			lines = append(lines, current.String())
			current.Reset()
		default:
			current.WriteRune(r)
		}
	}
	lines = append(lines, current.String())
	return lines
}

// trimStart 去除前导空白（对齐 JS trimStart）。
func trimStart(text string) string {
	return strings.TrimLeft(text, " \t\n\v\f\r ")
}

// hasPendingSSEProtocolEvent 对齐 _shared/sse-pending-event.ts 的
// hasPendingSseProtocolEvent。
func hasPendingSSEProtocolEvent(skipped bool, eventName string, dataLineCount, dataBytes int, pendingLine string) bool {
	if skipped {
		return false
	}
	if eventName != "" || dataLineCount > 0 || dataBytes > 0 {
		return true
	}
	pendingLine = strings.TrimSuffix(pendingLine, "\r")
	return strings.HasPrefix(pendingLine, "event:") || strings.HasPrefix(pendingLine, "data:")
}
