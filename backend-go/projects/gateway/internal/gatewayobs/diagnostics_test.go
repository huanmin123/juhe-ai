package gatewayobs

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// sanitizer（对齐官方回归 gateway-diagnostic-sanitizer-regression.ts）
// ---------------------------------------------------------------------------

func TestSanitizeDiagnosticPayloadRegressionCase(t *testing.T) {
	diagnostic := SanitizeDiagnosticPayload(map[string]interface{}{
		"upstreamUrl": "https://url-user:url-password@example.com/v1/chat/completions?client_secret=url-secret&safe=ok",
		"message":     "fallback id_token=fallback-id-token client_secret=fallback-client-secret",
		"error": map[string]interface{}{
			"message": "上游失败 id_token=diagnostic-id-token client_secret=diagnostic-client-secret Authorization: Bearer sk-diagnostic-secret-token",
			"type":    "invalid_request_error",
			"details": map[string]interface{}{
				"id_token":      "diagnostic-id-token",
				"client_secret": "diagnostic-client-secret",
				"nested": map[string]interface{}{
					"apiKey": "diagnostic-api-key",
				},
			},
		},
	})
	serialized := serializeJSON(t, diagnostic)
	for _, marker := range []string{
		"diagnostic-id-token",
		"diagnostic-client-secret",
		"diagnostic-api-key",
		"sk-diagnostic-secret-token",
		"fallback-client-secret",
		"fallback-id-token",
		"url-user",
		"url-password",
		"url-secret",
	} {
		if strings.Contains(serialized, marker) {
			t.Fatalf("上游诊断错误响应正文不应泄露敏感字段或 token: %q", marker)
		}
	}
	if !strings.Contains(serialized, `example.com/v1/chat/completions?client_secret=[redacted]`+`\u0026safe=ok`) &&
		!strings.Contains(serialized, "example.com/v1/chat/completions?client_secret=[redacted]&safe=ok") {
		t.Fatalf("诊断 URL 应保留主机、路径和安全查询参数: %s", serialized)
	}
}

func TestSanitizeDiagnosticPayloadStringRedaction(t *testing.T) {
	sanitized := SanitizeDiagnosticPayload("proxy failed at https://diagnostic-user:diagnostic-password@example.com/v1?safe=ok")
	text, ok := sanitized.(string)
	if !ok {
		t.Fatalf("字符串输入必须返回字符串: %T", sanitized)
	}
	for _, marker := range []string{"diagnostic-user", "diagnostic-password"} {
		if strings.Contains(text, marker) {
			t.Fatalf("诊断普通字符串中的 URL 用户信息不应保留敏感原文: %q", marker)
		}
	}
	if !strings.Contains(text, "example.com/v1?safe=ok") {
		t.Fatalf("诊断 URL 脱敏后应保留主机、路径和安全查询参数: %q", text)
	}
}

func TestSanitizeSensitiveStringTable(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"bearer", "x Bearer sk-abc123456789", "x Bearer [redacted]"},
		// Node 六段流水线全量结果：pass2 红掉 token 后，pass6 再把
		// 'authorization: ' 后的值（'Bearer'）红掉。
		{"bearer-with-authorization-key", "Authorization: Bearer sk-abc123456789", "Authorization: [redacted] [redacted]"},
		{"bearer-short-untouched", "Bearer abc", "Bearer abc"},
		{"bearer-case-insensitive", "x BEARER\tsuper-secret-token", "x Bearer [redacted]"},
		{"sk-token", "key sk-abcdefgh123 value", "key sk-[redacted] value"},
		{"sk-short-untouched", "sk-abc", "sk-abc"},
		{"sk-case-sensitive", "SK-abcdefgh123", "SK-abcdefgh123"},
		{"juis-token", "id juis_abcdefgh123 end", "id juis_[redacted] end"},
		{"quoted-double", "{\"api_key\": \"secret-value\"}", "{\"api_key\": \"[redacted]\"}"},
		{"quoted-single", "'password':'hunter2'", "'password':'[redacted]'"},
		// 值内的 \" 是转义对，不终止匹配。
		{"quoted-escaped-quote", `{"token": "va\"lue"}`, `{"token": "[redacted]"}`},
		// 值内的 \\ 消费为转义对，随后的 " 即为收尾引号（与 JS 贪婪回溯一致）。
		{"quoted-double-backslash", `{"token": "va\\"lue"}`, `{"token": "[redacted]"lue"}`},
		{"quoted-nonsensitive", "{\"model\": \"gpt-x\"}", "{\"model\": \"gpt-x\"}"},
		{"bare-equals", "fallback id_token=fallback-id-token", "fallback id_token=[redacted]"},
		{"bare-colon", "client_secret: abc", "client_secret: [redacted]"},
		{"bare-word-boundary", "tokenizer=abc", "tokenizer=abc"},
		{"bare-keys-untouched", "keys=1", "keys=1"},
		{"bare-key", "my key=abc", "my key=[redacted]"},
		{"bare-session-id", "sessionid:zzz", "sessionid:[redacted]"},
		{"credentials-plural", "credentials=zz", "credentials=[redacted]"},
		{"url-userinfo", "https://user:pass@example.com/x", "https://[redacted]@example.com/x"},
		{"url-last-at", "https://a@b@c.com/x", "https://[redacted]@b@c.com/x"},
		{"url-no-userinfo-untouched", "https://example.com/x", "https://example.com/x"},
		{"url-keeps-query", "https://u:p@example.com/v1?client_secret=s&safe=ok", "https://[redacted]@example.com/v1?client_secret=[redacted]&safe=ok"},
		{"chinese-context", "上游失败 id_token=diagnostic-id-token", "上游失败 id_token=[redacted]"},
		{"refreshtoken", "refresh_token=rt-value", "refresh_token=[redacted]"},
		{"proxy-authorization", "proxy_authorization: zz", "proxy_authorization: [redacted]"},
		{"code-verifier", "code_verifier:zz", "code_verifier:[redacted]"},
		{"set-cookie", "set_cookie=a=b", "set_cookie=[redacted]"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := sanitizeSensitiveString(testCase.input)
			if got != testCase.want {
				t.Fatalf("sanitizeSensitiveString(%q) = %q, want %q", testCase.input, got, testCase.want)
			}
		})
	}
}

func TestSanitizeDiagnosticPayloadFieldNameNormalization(t *testing.T) {
	cases := []struct {
		name     string
		field    string
		redacted bool
	}{
		{"apikey", "apiKey", true},
		{"dashed", "API-Key", true},
		{"spaced", "  access token ", true},
		{"idtoken", "id_token", true},
		{"authorization", "Authorization", true},
		{"setcookie", "Set-Cookie", true},
		{"credentials", "credentials", true},
		{"auth-token-not-in-set", "auth_token", false},
		{"model", "model", false},
		{"upstreamurl", "upstreamUrl", false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			payload := map[string]interface{}{testCase.field: "secret"}
			output := SanitizeDiagnosticPayload(payload).(map[string]interface{})
			if testCase.redacted && output[testCase.field] != "[redacted]" {
				t.Fatalf("field %q 必须脱敏: %v", testCase.field, output)
			}
			if !testCase.redacted && output[testCase.field] != "secret" {
				t.Fatalf("field %q 不得误伤: %v", testCase.field, output)
			}
		})
	}
}

func TestSanitizeDiagnosticPayloadStructureTruncation(t *testing.T) {
	// 深度截断：第 9 层返回 [truncated]。
	deep := interface{}("leaf")
	for depth := 0; depth < 9; depth += 1 {
		deep = map[string]interface{}{"child": deep}
	}
	output := SanitizeDiagnosticPayload(deep).(map[string]interface{})
	node := interface{}(output)
	for depth := 0; depth < 8; depth += 1 {
		node = node.(map[string]interface{})["child"]
	}
	if node != "[truncated]" {
		t.Fatalf("深度 >= 8 必须截断: %v", node)
	}

	// 数组截断。
	array := make([]interface{}, 103)
	for index := range array {
		array[index] = index
	}
	sanitizedArray := SanitizeDiagnosticPayload(array).([]interface{})
	if len(sanitizedArray) != 101 {
		t.Fatalf("数组必须保留前 100 项 + 截断标记: %d", len(sanitizedArray))
	}
	if sanitizedArray[100] != "[truncated:3]" {
		t.Fatalf("截断标记 = %v", sanitizedArray[100])
	}

	// 敏感字段名优先于 null/结构（与 Node 顺序一致）。
	if got := SanitizeDiagnosticPayload(map[string]interface{}{"password": nil}); got.(map[string]interface{})["password"] != "[redacted]" {
		t.Fatalf("敏感字段即使是 null 也必须脱敏: %v", got)
	}

	// 标量原样保留。
	if got := SanitizeDiagnosticPayload(float64(42)); got != float64(42) {
		t.Fatalf("数字必须原样: %v", got)
	}
	if got := SanitizeDiagnosticPayload(true); got != true {
		t.Fatalf("布尔必须原样: %v", got)
	}
}

// ---------------------------------------------------------------------------
// 诊断响应上下文
// ---------------------------------------------------------------------------

func TestParseDiagnosticResponseContextJSONBody(t *testing.T) {
	attempts := 0
	context := ParseDiagnosticResponseContextWithOptions(`{"model":"gpt-5.6-sol"}`, DiagnosticResponseParseOptions{
		OnJSONParseAttempt: func(text string) { attempts += 1 },
	})
	if context.JSON == nil || context.Record == nil {
		t.Fatalf("context = %+v", context)
	}
	if context.Record["model"] != "gpt-5.6-sol" {
		t.Fatalf("record = %v", context.Record)
	}
	if len(context.Events) != 0 || len(context.Payloads) != 1 || context.Payloads[0]["model"] != "gpt-5.6-sol" {
		t.Fatalf("events/payloads = %+v / %+v", context.Events, context.Payloads)
	}
	if attempts != 1 {
		t.Fatalf("JSON 正文必须恰好尝试一次解析: %d", attempts)
	}
	if context.BodyText != `{"model":"gpt-5.6-sol"}` {
		t.Fatalf("bodyText 必须保留原文: %q", context.BodyText)
	}
}

func TestParseDiagnosticResponseContextJSONPrimitive(t *testing.T) {
	context := ParseDiagnosticResponseContext("123")
	if context.JSON != float64(123) || context.Record != nil || len(context.Payloads) != 0 {
		t.Fatalf("原始值 JSON 必须保留 json 但不产生 payloads: %+v", context)
	}
	// JSON.parse('null') 在 JS 中不是 undefined。
	context = ParseDiagnosticResponseContext("null")
	if context.JSON != nil || context.Record != nil || len(context.Payloads) != 0 || len(context.Events) != 0 {
		t.Fatalf("null 正文必须与 undefined 区分: %+v", context)
	}
	// 非法 JSON 落入 SSE 路径（looksLikeSSE=false 时 json 解析失败 → SSE 解析）。
	context = ParseDiagnosticResponseContext("{invalid")
	if context.JSON != nil || len(context.Events) != 0 {
		t.Fatalf("非法正文必须产生空上下文: %+v", context)
	}
}

func TestParseDiagnosticResponseContextEmptyAndBOM(t *testing.T) {
	context := ParseDiagnosticResponseContext("   \n  ")
	if len(context.Events) != 0 || len(context.Payloads) != 0 || context.JSON != nil || context.Record != nil {
		t.Fatalf("空白正文必须为空上下文: %+v", context)
	}
	bomContext := ParseDiagnosticResponseContext("\uFEFF{\"a\":1}")
	if bomContext.BodyText != "\uFEFF{\"a\":1}" {
		t.Fatalf("bodyText 必须保留 BOM 原文: %q", bomContext.BodyText)
	}
	if bomContext.Record == nil || bomContext.Record["a"] != float64(1) {
		t.Fatalf("BOM 后的 JSON 必须可解析: %+v", bomContext)
	}
}

func TestParseDiagnosticResponseContextSSEBody(t *testing.T) {
	bodyText := strings.Join([]string{
		": keep-alive",
		"event: response.output_text.delta",
		`data: {"type":"response.output_text.delta","delta":"OK"}`,
		"",
		"event: response.completed",
		`data: {"type":"response.completed","response":{"status":"completed","output":[]}}`,
		"",
		"data: [DONE]",
		"",
		"",
	}, "\n")
	attempts := 0
	context := ParseDiagnosticResponseContextWithOptions(bodyText, DiagnosticResponseParseOptions{
		OnJSONParseAttempt: func(text string) { attempts += 1 },
	})
	if len(context.Events) != 3 {
		t.Fatalf("events = %+v", context.Events)
	}
	first := context.Events[0]
	if first.Event == nil || *first.Event != "response.output_text.delta" {
		t.Fatalf("event = %v", first.Event)
	}
	if first.JSON == nil || first.JSON["delta"] != "OK" || first.Done {
		t.Fatalf("first = %+v", first)
	}
	if context.Events[1].Event == nil || *context.Events[1].Event != "response.completed" {
		t.Fatalf("second = %+v", context.Events[1])
	}
	done := context.Events[2]
	if !done.Done || done.JSON != nil || done.Event != nil {
		t.Fatalf("[DONE] 必须无 json 无 event: %+v", done)
	}
	// 只有非 [DONE] 非空 data 才尝试 JSON 解析（3 条 data，[DONE] 不算）。
	if attempts != 2 {
		t.Fatalf("JSON 解析次数 = %d, want 2", attempts)
	}
	if len(context.Payloads) != 2 {
		t.Fatalf("payloads = %d, want 2", len(context.Payloads))
	}
	if context.BodyText != bodyText {
		t.Fatalf("bodyText 必须保留原文")
	}
}

func TestParseDiagnosticResponseContextSSELineHandling(t *testing.T) {
	// CRLF、多 data 行合并、value 首个空格剥离、无 event 字段的事件。
	bodyText := "data: line1\r\ndata:line2\r\n\r\n"
	context := ParseDiagnosticResponseContext(bodyText)
	if len(context.Events) != 1 {
		t.Fatalf("events = %+v", context.Events)
	}
	if context.Events[0].Data != "line1\nline2" {
		t.Fatalf("多 data 行必须以 \\n 合并: %q", context.Events[0].Data)
	}
	if context.Events[0].Event != nil {
		t.Fatalf("缺省 event 必须为 nil: %v", context.Events[0].Event)
	}
	// event 字段在无 data 的 flush 中被重置。
	context = ParseDiagnosticResponseContext("event: a\n\ndata: {\"x\":1}\n\n")
	if len(context.Events) != 1 || context.Events[0].Event != nil {
		t.Fatalf("孤立 event 必须被随后的空行重置: %+v", context.Events)
	}
	// CR-only 分行；连续 data 行合并，空行才结束事件。
	context = ParseDiagnosticResponseContext("data: {\"a\":1}\r\rdata: {\"b\":2}\r\r")
	if len(context.Events) != 2 {
		t.Fatalf("events = %+v", context.Events)
	}
	// 非对象 JSON data 不产生 json。
	context = ParseDiagnosticResponseContext("data: 123\n\n")
	if len(context.Events) != 1 || context.Events[0].JSON != nil || len(context.Payloads) != 0 {
		t.Fatalf("非对象 data 不得产生 payload: %+v", context)
	}
}

func TestDiagnosticResponseContextFromGatewayNonStream(t *testing.T) {
	parsedValue := map[string]interface{}{
		"model":       "gpt-5.6-sol",
		"output_text": "OK",
		"usage":       map[string]interface{}{"total_tokens": float64(2)},
	}
	parsedBody := &NonStreamJSONBodyView{Status: "valid", Value: parsedValue}
	attempts := 0
	context := DiagnosticResponseContextFromGatewayNonStream("{\"model\":\"gpt-5.6-sol\"}", parsedBody, DiagnosticResponseParseOptions{
		OnJSONParseAttempt: func(text string) { attempts += 1 },
	})
	// interface 比较对 map 会 panic；按底层引用核对「同一 parsed value」。
	if context.JSON == nil || reflect.ValueOf(context.JSON).Pointer() != reflect.ValueOf(interface{}(parsedValue)).Pointer() {
		t.Fatalf("诊断上下文必须直接复用网关 parsed value")
	}
	if attempts != 0 {
		t.Fatalf("已有网关 parsed body 时诊断上下文不得再次 JSON.parse: %d", attempts)
	}
	if len(context.Payloads) != 1 || len(context.Events) != 0 {
		t.Fatalf("payloads/events = %d / %d", len(context.Payloads), len(context.Events))
	}

	invalidAttempts := 0
	invalidBody := &NonStreamJSONBodyView{Status: "invalid"}
	invalidContext := DiagnosticResponseContextFromGatewayNonStream("{invalid", invalidBody, DiagnosticResponseParseOptions{
		OnJSONParseAttempt: func(text string) { invalidAttempts += 1 },
	})
	if len(invalidContext.Payloads) != 0 || invalidContext.JSON != nil {
		t.Fatalf("invalid 正文必须为空上下文: %+v", invalidContext)
	}
	if invalidAttempts != 0 {
		t.Fatalf("网关已确认 invalid 的非流式正文不得再次尝试解析: %d", invalidAttempts)
	}

	// nil parsedBody 回退到文本解析。
	fallback := DiagnosticResponseContextFromGatewayNonStream("{\"a\":1}", nil, DiagnosticResponseParseOptions{})
	if fallback.Record == nil || fallback.Record["a"] != float64(1) {
		t.Fatalf("nil parsedBody 必须回退文本解析: %+v", fallback)
	}
}

func TestDiagnosticResponseContextFromGatewayResponse(t *testing.T) {
	streamEvents := []ParsedOpenAIStreamEventView{
		{EventName: "response.output_text.delta", DataText: `{"type":"response.output_text.delta"}`, Data: map[string]interface{}{"type": "response.output_text.delta"}},
		{EventName: "", DataText: `{"type":"response.completed"}`, Data: map[string]interface{}{"type": "response.completed"}},
		{EventName: "response.done", DataText: "  [DONE]  ", Data: nil},
	}
	attempts := 0
	context := DiagnosticResponseContextFromGatewayResponse("ignored", nil, streamEvents, DiagnosticResponseParseOptions{
		OnJSONParseAttempt: func(text string) { attempts += 1 },
	})
	if attempts != 0 {
		t.Fatalf("已有网关 SSE event 时诊断上下文不得重放 JSON.parse: %d", attempts)
	}
	if len(context.Events) != 3 {
		t.Fatalf("events = %d", len(context.Events))
	}
	if context.Events[0].Event == nil || *context.Events[0].Event != "response.output_text.delta" {
		t.Fatalf("event = %v", context.Events[0].Event)
	}
	// Node: event.eventName || undefined — 空串映射为 undefined。
	if context.Events[1].Event != nil {
		t.Fatalf("空 eventName 必须为 nil: %v", context.Events[1].Event)
	}
	if context.Events[1].JSON == nil || context.Events[1].JSON["type"] != "response.completed" {
		t.Fatalf("json 必须复用网关解析对象: %+v", context.Events[1].JSON)
	}
	if !context.Events[2].Done {
		t.Fatalf("trim 后 [DONE] 必须标记 done: %+v", context.Events[2])
	}
	if len(context.Payloads) != 2 {
		t.Fatalf("payloads = %d, want 2", len(context.Payloads))
	}

	// 无事件时回退文本解析。
	fallback := DiagnosticResponseContextFromGatewayResponse("data: {\"b\":2}\n\n", nil, nil, DiagnosticResponseParseOptions{})
	if len(fallback.Events) != 1 || fallback.Events[0].JSON["b"] != float64(2) {
		t.Fatalf("回退解析失败: %+v", fallback)
	}

	// parsedBody 优先。
	nonStream := DiagnosticResponseContextFromGatewayResponse("ignored", &NonStreamJSONBodyView{Status: "valid", Value: map[string]interface{}{"c": 3}}, streamEvents, DiagnosticResponseParseOptions{})
	if len(nonStream.Events) != 0 || nonStream.Record == nil {
		t.Fatalf("parsedBody 必须优先于流事件: %+v", nonStream)
	}
}

func TestLooksLikeServerSentEvents(t *testing.T) {
	cases := []struct {
		text string
		want bool
	}{
		{"data: x", true},
		{"event: x", true},
		{"id: 1", true},
		{"retry: 5", true},
		{"just json {\"a\":1}", false},
		{"line1\ndata: x", true},
		{"line1\r\ndata: x", true},
		{"line1\rdata: x", true},
		{": comment", true},
		{"nodata: x", false},
		{"datax", false},
		{"prefix data: x", false},
	}
	for _, testCase := range cases {
		if got := looksLikeServerSentEvents(testCase.text); got != testCase.want {
			t.Fatalf("looksLikeSSE(%q) = %v, want %v", testCase.text, got, testCase.want)
		}
	}
}

func TestDiagnosticResponseContextOf(t *testing.T) {
	if context := DiagnosticResponseContextOf("data: {\"a\":1}\n\n"); len(context.Events) != 1 {
		t.Fatalf("string 输入必须解析: %+v", context)
	}
	nested := ParseDiagnosticResponseContext("{\"a\":1}")
	if context := DiagnosticResponseContextOf(nested); context.Record["a"] != float64(1) {
		t.Fatalf("context 输入必须原样返回: %+v", context)
	}
}

func serializeJSON(t *testing.T, value interface{}) string {
	t.Helper()
	serialized, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(serialized)
}
