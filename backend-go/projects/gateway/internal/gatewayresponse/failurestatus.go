package gatewayresponse

// Responses 根节点 status 扫描器，对齐 responses-failure-status.ts。

// jsonStringCaptureMaxBytes 对齐 jsonStringCaptureMaxBytes。
const jsonStringCaptureMaxBytes = 256

// ResponsesRootStatusTracker 对齐 ResponsesRootStatusTracker：对 Responses JSON
// 文档根节点 status 字段的有界增量扫描。响应可能大到无法整体驻留内存，且供应商
// 不保证 status 位于大 output 字段之前，因此必须在每个传输分片上运行。
type ResponsesRootStatusTracker struct {
	depth          int
	rootStarted    bool
	completed      bool
	failed         bool
	rootState      string // 'key_or_end' | 'colon' | 'value' | 'after_value'
	rootKeyIsStatus bool
	inString       bool
	stringEscaped  bool
	stringContext  string // 'key' | 'status_value' | ''
	stringRaw      []byte
	stringCaptureTruncated bool
}

// NewResponsesRootStatusTracker 构造扫描器。
func NewResponsesRootStatusTracker() *ResponsesRootStatusTracker {
	return &ResponsesRootStatusTracker{rootState: "key_or_end"}
}

// Push 对齐 push。
func (t *ResponsesRootStatusTracker) Push(chunk []byte) {
	if t.completed || t.failed {
		return
	}
	for _, b := range chunk {
		t.consumeByte(b)
		if t.completed || t.failed {
			return
		}
	}
}

// HasFailedStatus 对齐 hasFailedStatus。
func (t *ResponsesRootStatusTracker) HasFailedStatus() bool { return t.failed }

func (t *ResponsesRootStatusTracker) consumeByte(b byte) {
	if t.inString {
		t.consumeStringByte(b)
		return
	}
	if !t.rootStarted {
		if isJSONWhitespace(b) {
			return
		}
		if b != 0x7b { // '{'
			t.completed = true
			return
		}
		t.rootStarted = true
		t.depth = 1
		t.rootState = "key_or_end"
		return
	}
	if b == 0x22 { // '"'
		t.startString()
		return
	}
	if isJSONWhitespace(b) {
		return
	}
	if t.depth == 1 {
		if b == 0x3a && t.rootState == "colon" { // ':'
			t.rootState = "value"
			return
		}
		if b == 0x2c { // ','
			t.rootState = "key_or_end"
			t.rootKeyIsStatus = false
			return
		}
		if t.rootState == "value" {
			t.rootState = "after_value"
			t.rootKeyIsStatus = false
		}
	}
	if b == 0x7b || b == 0x5b { // '{' '['
		t.depth++
		return
	}
	if b == 0x7d || b == 0x5d { // '}' ']'
		if t.depth > 0 {
			t.depth--
		}
		if t.depth == 0 {
			t.completed = true
		}
	}
}

func (t *ResponsesRootStatusTracker) startString() {
	t.inString = true
	t.stringEscaped = false
	t.stringRaw = t.stringRaw[:0]
	t.stringCaptureTruncated = false
	if t.depth == 1 {
		switch {
		case t.rootState == "key_or_end":
			t.stringContext = "key"
		case t.rootState == "value" && t.rootKeyIsStatus:
			t.stringContext = "status_value"
		default:
			t.stringContext = ""
		}
	} else {
		t.stringContext = ""
	}
}

func (t *ResponsesRootStatusTracker) consumeStringByte(b byte) {
	if t.stringEscaped {
		t.appendStringByte(b)
		t.stringEscaped = false
		return
	}
	if b == 0x5c { // '\\'
		t.appendStringByte(b)
		t.stringEscaped = true
		return
	}
	if b != 0x22 { // '"'
		t.appendStringByte(b)
		return
	}
	t.inString = false
	var value string
	if !t.stringCaptureTruncated {
		value = decodeJSONString(string(t.stringRaw))
	}
	switch t.stringContext {
	case "key":
		t.rootKeyIsStatus = value == "status"
		t.rootState = "colon"
	case "status_value":
		t.rootKeyIsStatus = false
		t.rootState = "after_value"
		t.failed = value == "failed"
	default:
		if t.depth == 1 && t.rootState == "value" {
			t.rootKeyIsStatus = false
			t.rootState = "after_value"
		}
	}
	t.stringContext = ""
	t.stringRaw = t.stringRaw[:0]
}

func (t *ResponsesRootStatusTracker) appendStringByte(b byte) {
	if t.stringContext == "" || t.stringCaptureTruncated {
		return
	}
	if len(t.stringRaw) >= jsonStringCaptureMaxBytes {
		t.stringCaptureTruncated = true
		return
	}
	t.stringRaw = append(t.stringRaw, b)
}

// ResponsesFailureStatusFromCapturedJSON 对齐
// responsesFailureStatusFromCapturedJson。
func ResponsesFailureStatusFromCapturedJSON(responseBodyText string) bool {
	if responseBodyText == "" {
		return false
	}
	tracker := NewResponsesRootStatusTracker()
	tracker.Push([]byte(responseBodyText))
	return tracker.HasFailedStatus()
}

// decodeJSONString 对齐 decodeJsonString：JSON.parse(`"${raw}"`)，失败时 nil。
func decodeJSONString(raw string) string {
	// Node 逐字节累积的是解码前的 JSON 源码文本（含转义序列原样字节），
	// 这里做等价的反转义：仅处理 \" \\ \/ \b \f \n \r \t \uXXXX。
	decoded, ok := unescapeJSONString(raw)
	if !ok {
		return ""
	}
	return decoded
}

func unescapeJSONString(raw string) (string, bool) {
	var out []byte
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if c != '\\' {
			out = append(out, c)
			continue
		}
		i++
		if i >= len(raw) {
			return "", false
		}
		switch raw[i] {
		case '"':
			out = append(out, '"')
		case '\\':
			out = append(out, '\\')
		case '/':
			out = append(out, '/')
		case 'b':
			out = append(out, '\b')
		case 'f':
			out = append(out, '\f')
		case 'n':
			out = append(out, '\n')
		case 'r':
			out = append(out, '\r')
		case 't':
			out = append(out, '\t')
		case 'u':
			if i+4 >= len(raw) {
				return "", false
			}
			code, ok := parseHex4(raw[i+1 : i+5])
			if !ok {
				return "", false
			}
			i += 4
			r := rune(code)
			if code >= 0xD800 && code <= 0xDBFF && i+6 < len(raw) &&
				raw[i+1] == '\\' && raw[i+2] == 'u' {
				if low, ok := parseHex4(raw[i+3 : i+7]); ok && low >= 0xDC00 && low <= 0xDFFF {
					r = ((rune(code)-0xD800)<<10 | (rune(low)-0xDC00)) + 0x10000
					i += 6
				}
			}
			out = append(out, []byte(string(r))...)
		default:
			return "", false
		}
	}
	return string(out), true
}

func parseHex4(value string) (int, bool) {
	code := 0
	for i := 0; i < 4; i++ {
		c := value[i]
		switch {
		case c >= '0' && c <= '9':
			code = code<<4 | int(c-'0')
		case c >= 'a' && c <= 'f':
			code = code<<4 | int(c-'a'+10)
		case c >= 'A' && c <= 'F':
			code = code<<4 | int(c-'A'+10)
		default:
			return 0, false
		}
	}
	return code, true
}

func isJSONWhitespace(b byte) bool {
	return b == 0x09 || b == 0x0a || b == 0x0d || b == 0x20
}
