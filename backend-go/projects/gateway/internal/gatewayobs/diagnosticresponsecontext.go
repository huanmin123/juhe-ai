package gatewayobs

import (
	"encoding/json"
	"regexp"
	"strings"
)

// 诊断响应上下文，逐行为对齐
// backend/src/modules/gateway/diagnostics/diagnostic-response-context.ts。
//
// Node 依赖 GatewayNonStreamJsonBody / ParsedOpenAIStreamEvent 的结构子类型；
// 这里定义等价的视图类型（字段与 gatewayhybrid.NonStreamJSONBody /
// gatewayopenai.ParsedStreamEvent 对齐），避免包耦合。

// DiagnosticSseEvent mirrors DiagnosticSseEvent. Event 为 nil 表示 Node 的
// `event?: string` 缺省（undefined）。
type DiagnosticSseEvent struct {
	Event *string
	Data  string
	JSON  map[string]interface{}
	Done  bool
}

// DiagnosticResponseContext mirrors DiagnosticResponseContext.
type DiagnosticResponseContext struct {
	BodyText string
	JSON     interface{}
	Record   map[string]interface{}
	Events   []DiagnosticSseEvent
	Payloads []map[string]interface{}
}

// DiagnosticResponseParseOptions mirrors DiagnosticResponseParseOptions.
type DiagnosticResponseParseOptions struct {
	OnJSONParseAttempt func(text string)
}

// NonStreamJSONBodyView mirrors gatewayhybrid.NonStreamJSONBody（即 Node
// GatewayNonStreamJsonBody）中被本包消费的形状。
type NonStreamJSONBodyView struct {
	Status string      // 'valid' | 'empty' | 'not_json' | 'invalid'
	Value  interface{} // 仅 Status == 'valid' 有效
}

// ParsedOpenAIStreamEventView mirrors gatewayopenai.ParsedStreamEvent（即
// Node ParsedOpenAIStreamEvent）中被本包消费的形状。
type ParsedOpenAIStreamEventView struct {
	EventName string
	DataText  string
	Data      map[string]interface{}
}

// ParseDiagnosticResponseContext mirrors parseDiagnosticResponseContext
// （options 缺省）。
func ParseDiagnosticResponseContext(bodyText string) DiagnosticResponseContext {
	return parseDiagnosticResponseContext(bodyText, DiagnosticResponseParseOptions{})
}

// ParseDiagnosticResponseContextWithOptions mirrors parseDiagnosticResponseContext.
func ParseDiagnosticResponseContextWithOptions(bodyText string, options DiagnosticResponseParseOptions) DiagnosticResponseContext {
	return parseDiagnosticResponseContext(bodyText, options)
}

func parseDiagnosticResponseContext(bodyText string, options DiagnosticResponseParseOptions) DiagnosticResponseContext {
	normalizedBodyText := bodyText
	// JS slice(1) 按 UTF-16 码元截断；BOM 的 UTF-8 编码为 3 字节，必须整体去除。
	normalizedBodyText = strings.TrimPrefix(normalizedBodyText, "\uFEFF")
	trimmed := trimJSText(normalizedBodyText)
	if trimmed == "" {
		return emptyDiagnosticResponseContext(bodyText)
	}

	if !looksLikeServerSentEvents(trimmed) {
		if value, ok := parseDiagnosticJSON(trimmed, options); ok {
			record, _ := value.(map[string]interface{})
			context := DiagnosticResponseContext{
				BodyText: bodyText,
				JSON:     value,
				Record:   record,
				Events:   []DiagnosticSseEvent{},
			}
			if record != nil {
				context.Payloads = []map[string]interface{}{record}
			} else {
				context.Payloads = []map[string]interface{}{}
			}
			return context
		}
	}

	events := parseDiagnosticSseEvents(normalizedBodyText, options)
	payloads := make([]map[string]interface{}, 0)
	for _, event := range events {
		if event.JSON != nil {
			payloads = append(payloads, event.JSON)
		}
	}
	return DiagnosticResponseContext{
		BodyText: bodyText,
		Events:   events,
		Payloads: payloads,
	}
}

// DiagnosticResponseContextOf mirrors diagnosticResponseContext (string |
// context union).
func DiagnosticResponseContextOf(input interface{}) DiagnosticResponseContext {
	switch typed := input.(type) {
	case string:
		return ParseDiagnosticResponseContext(typed)
	case DiagnosticResponseContext:
		return typed
	default:
		return emptyDiagnosticResponseContext("")
	}
}

// DiagnosticResponseContextFromGatewayNonStream mirrors
// diagnosticResponseContextFromGatewayNonStream: parsedBody 为 nil 表示
// Node undefined。
func DiagnosticResponseContextFromGatewayNonStream(bodyText string, parsedBody *NonStreamJSONBodyView, options DiagnosticResponseParseOptions) DiagnosticResponseContext {
	if parsedBody == nil {
		return parseDiagnosticResponseContext(bodyText, options)
	}
	if parsedBody.Status != "valid" {
		return emptyDiagnosticResponseContext(bodyText)
	}
	record, _ := parsedBody.Value.(map[string]interface{})
	context := DiagnosticResponseContext{
		BodyText: bodyText,
		JSON:     parsedBody.Value,
		Record:   record,
		Events:   []DiagnosticSseEvent{},
	}
	if record != nil {
		context.Payloads = []map[string]interface{}{record}
	} else {
		context.Payloads = []map[string]interface{}{}
	}
	return context
}

// DiagnosticResponseContextFromGatewayResponse mirrors
// diagnosticResponseContextFromGatewayResponse: 复用网关已解析结果，不再重放
// JSON.parse。
func DiagnosticResponseContextFromGatewayResponse(bodyText string, parsedBody *NonStreamJSONBodyView, parsedStreamEvents []ParsedOpenAIStreamEventView, options DiagnosticResponseParseOptions) DiagnosticResponseContext {
	if parsedBody != nil {
		return DiagnosticResponseContextFromGatewayNonStream(bodyText, parsedBody, options)
	}
	if len(parsedStreamEvents) == 0 {
		return parseDiagnosticResponseContext(bodyText, options)
	}
	events := make([]DiagnosticSseEvent, 0, len(parsedStreamEvents))
	payloads := make([]map[string]interface{}, 0)
	for _, event := range parsedStreamEvents {
		var eventName *string
		if event.EventName != "" {
			name := event.EventName
			eventName = &name
		}
		mapped := DiagnosticSseEvent{
			Event: eventName,
			Data:  event.DataText,
			JSON:  event.Data,
			Done:  trimJSText(event.DataText) == "[DONE]",
		}
		events = append(events, mapped)
		if mapped.JSON != nil {
			payloads = append(payloads, mapped.JSON)
		}
	}
	return DiagnosticResponseContext{
		BodyText: bodyText,
		Events:   events,
		Payloads: payloads,
	}
}

func parseDiagnosticSseEvents(bodyText string, options DiagnosticResponseParseOptions) []DiagnosticSseEvent {
	events := make([]DiagnosticSseEvent, 0)
	event := ""
	eventSet := false
	var dataLines []string

	flush := func() {
		if len(dataLines) == 0 {
			event = ""
			eventSet = false
			return
		}
		data := strings.Join(dataLines, "\n")
		normalizedData := trimJSText(data)
		done := normalizedData == "[DONE]"
		var payload map[string]interface{}
		if !done && normalizedData != "" {
			if value, ok := parseDiagnosticJSON(data, options); ok {
				payload, _ = value.(map[string]interface{})
			}
		}
		var eventName *string
		if eventSet {
			name := event
			eventName = &name
		}
		events = append(events, DiagnosticSseEvent{
			Event: eventName,
			Data:  data,
			JSON:  payload,
			Done:  done,
		})
		event = ""
		eventSet = false
		dataLines = nil
	}

	for _, line := range splitDiagnosticSseLines(bodyText) {
		if line == "" {
			flush()
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		separatorIndex := strings.IndexByte(line, ':')
		field := line
		value := ""
		if separatorIndex >= 0 {
			field = line[:separatorIndex]
			value = line[separatorIndex+1:]
			if strings.HasPrefix(value, " ") {
				value = value[1:]
			}
		}
		switch field {
		case "event":
			event = value
			eventSet = true
		case "data":
			dataLines = append(dataLines, value)
		}
	}
	flush()
	return events
}

// parseDiagnosticJSON mirrors parseJson: onJsonParseAttempt 先行，失败返回
// ok=false（Node undefined）。JSON.parse('null') 成功 → (nil, true)。
func parseDiagnosticJSON(text string, options DiagnosticResponseParseOptions) (interface{}, bool) {
	if options.OnJSONParseAttempt != nil {
		options.OnJSONParseAttempt(text)
	}
	var value interface{}
	if err := json.Unmarshal([]byte(text), &value); err != nil {
		return nil, false
	}
	return value, true
}

var diagnosticSseHintPattern = regexp.MustCompile(`(?:\A|\r\n|\r|\n)(?::|(?:event|data|id|retry)(?::|$))`)

func looksLikeServerSentEvents(text string) bool {
	return diagnosticSseHintPattern.MatchString(text)
}

func emptyDiagnosticResponseContext(bodyText string) DiagnosticResponseContext {
	return DiagnosticResponseContext{
		BodyText: bodyText,
		Events:   []DiagnosticSseEvent{},
		Payloads: []map[string]interface{}{},
	}
}

// splitDiagnosticSseLines mirrors bodyText.split(/\r\n|\r|\n/).
func splitDiagnosticSseLines(text string) []string {
	lines := make([]string, 0)
	start := 0
	index := 0
	for index < len(text) {
		switch text[index] {
		case '\n':
			lines = append(lines, text[start:index])
			index += 1
			start = index
		case '\r':
			lines = append(lines, text[start:index])
			if index+1 < len(text) && text[index+1] == '\n' {
				index += 2
			} else {
				index += 1
			}
			start = index
		default:
			index += 1
		}
	}
	return append(lines, text[start:])
}

// trimJSText mirrors String.prototype.trim（含 \uFEFF；Go unicode.IsSpace 不含）。
func trimJSText(value string) string {
	return strings.TrimFunc(value, isJSTrimRune)
}

func isJSTrimRune(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0x00A0, 0x1680, 0x2028, 0x2029, 0x202F, 0x205F, 0x3000, 0xFEFF:
		return true
	}
	return r >= 0x2000 && r <= 0x200A
}
