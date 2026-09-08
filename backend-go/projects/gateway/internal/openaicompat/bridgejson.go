package openaicompat

import (
	"encoding/json"
	"regexp"
	"strings"
)

// Shared JSON / SSE helpers for the B-4 protocol bridges. They mirror the
// per-module helpers of the archived Node bridges:
//
//	providers/drivers/_shared/openai-anthropic-bridge.ts
//	providers/drivers/_shared/anthropic-openai-chat-bridge.ts
//	providers/drivers/_shared/gemini-openai-chat-bridge.ts
//	providers/drivers/_shared/codex-responses-chat-bridge.ts
//
// (objectValue / stringValue / integerValue / takeCompleteSseEvents /
// parseOpenAISseEventText local copies). The parse semantics follow
// gatewayopenai.ParseSseEventText; a local copy avoids an import cycle
// (gatewayopenai -> openaicompat).

// bridgeObjectValue mirrors objectValue(value).
func bridgeObjectValue(value any) map[string]any {
	if object, ok := value.(map[string]any); ok {
		return object
	}
	return nil
}

// bridgeStringValue mirrors stringValue(value).
func bridgeStringValue(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}

// bridgeIntegerValue mirrors integerValue(value): JSON numbers decode as
// float64, so integrality is re-checked before truncation. Go-native int and
// int64 values (tests, in-process callers) are accepted as well.
func bridgeIntegerValue(value any) (int64, bool) {
	switch number := value.(type) {
	case float64:
		if number != float64(int64(number)) {
			return 0, false
		}
		return int64(number), true
	case int:
		return int64(number), true
	case int64:
		return number, true
	default:
		return 0, false
	}
}

// bridgeNumberValue mirrors a `typeof value === 'number'` check.
func bridgeNumberValue(value any) (float64, bool) {
	switch number := value.(type) {
	case float64:
		return number, true
	case int:
		return float64(number), true
	case int64:
		return float64(number), true
	default:
		return 0, false
	}
}

// bridgeBoolValue mirrors `value === true` / `value === false`.
func bridgeBoolValue(value any) (bool, bool) {
	flag, ok := value.(bool)
	return flag, ok
}

func bridgeIsPlainObject(value any) bool {
	_, ok := value.(map[string]any)
	return ok
}

func bridgeIsArray(value any) ([]any, bool) {
	items, ok := value.([]any)
	return items, ok
}

// bridgeCloneJSON deep-clones a decoded JSON value (Node jsonRecordClone).
func bridgeCloneJSON(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		cloned := make(map[string]any, len(typed))
		for key, item := range typed {
			cloned[key] = bridgeCloneJSON(item)
		}
		return cloned
	case []any:
		cloned := make([]any, len(typed))
		for index, item := range typed {
			cloned[index] = bridgeCloneJSON(item)
		}
		return cloned
	default:
		return value
	}
}

// bridgeParseToolArguments mirrors parseToolArguments: `{}` for empty input,
// the parsed object when it is one, `{value: parsed}` for other JSON values
// and `{_raw: text}` when parsing fails.
func bridgeParseToolArguments(value string) any {
	if value == "" {
		return map[string]any{}
	}
	var parsed any
	if err := json.Unmarshal([]byte(value), &parsed); err != nil {
		return map[string]any{"_raw": value}
	}
	if object, ok := parsed.(map[string]any); ok {
		return object
	}
	return map[string]any{"value": parsed}
}

// bridgeJSONStringify marshals compact JSON, mirroring JSON.stringify.
func bridgeJSONStringify(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(encoded)
}

// bridgeTakeCompleteSseEvents mirrors takeCompleteSseEvents: split complete
// SSE events (terminated by a blank line) from the trailing partial buffer.
func bridgeTakeCompleteSseEvents(input string) (events []string, rest string) {
	rest = input
	for {
		match := bridgeBlankLinePattern.FindStringIndex(rest)
		if match == nil {
			return events, rest
		}
		end := match[1]
		events = append(events, rest[:end])
		rest = rest[end:]
	}
}

var bridgeBlankLinePattern = regexp.MustCompile(`\r?\n\r?\n`)

// BridgeSseEvent mirrors ParsedSseEvent from the Node stream-events module
// (parseOpenAISseEventText) for the bridge consumers.
type BridgeSseEvent struct {
	RawText        string
	EventName      string
	EventType      string
	DataText       string
	Data           map[string]any
	DataParseError bool
}

// ParseBridgeSseEvent mirrors parseOpenAISseEventText for the bridges.
func ParseBridgeSseEvent(rawText string) BridgeSseEvent {
	eventName := ""
	var dataLines []string
	for _, line := range bridgeSseLineSplit.Split(rawText, -1) {
		if strings.HasPrefix(line, "event:") {
			eventName = strings.TrimSpace(line[len("event:"):])
		} else if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimLeft(line[len("data:"):], " \t"))
		}
	}
	dataText := strings.TrimSpace(strings.Join(dataLines, "\n"))
	event := BridgeSseEvent{RawText: rawText, EventName: eventName, DataText: dataText}
	if dataText == "" || dataText == "[DONE]" {
		return event
	}
	var parsed any
	if err := json.Unmarshal([]byte(dataText), &parsed); err != nil {
		event.DataParseError = true
		return event
	}
	if object, ok := parsed.(map[string]any); ok {
		event.Data = object
		if eventType, ok := object["type"].(string); ok {
			event.EventType = eventType
		}
	}
	return event
}

var bridgeSseLineSplit = regexp.MustCompile(`\r\n|\r|\n`)

// BridgeSseData renders `data: {json}\n\n` (chatSseData / geminiSse).
func BridgeSseData(payload any) string {
	return "data: " + bridgeJSONStringify(payload) + "\n\n"
}

// BridgeSseEventText renders `event: <name>\ndata: {json}\n\n`
// (anthropicSse / sse(eventName, payload)).
func BridgeSseEventText(name string, payload any) string {
	return "event: " + name + "\ndata: " + bridgeJSONStringify(payload) + "\n\n"
}
