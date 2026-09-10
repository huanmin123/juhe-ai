package gatewayanthropic

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// Claude Code 兼容触发契约：POST /messages 前提 + 显式能力 / 网关画像头 /
// 原生签名信号 >= 2 三条路径。
func TestWAShouldApplyClaudeCodeMessagesCompatibility(t *testing.T) {
	newMessages := func() *http.Request {
		return httptest.NewRequest("POST", "/v1/messages", nil)
	}
	t.Run("非 messages 请求不触发", func(t *testing.T) {
		request := httptest.NewRequest("GET", "/v1/models", nil)
		if ShouldApplyClaudeCodeMessagesCompatibility(request, ClientCompatibilityOptions{RequestClientCompatibility: "claude_code"}) {
			t.Fatal("非 messages 请求不应触发兼容")
		}
	})
	t.Run("显式 claude_code 能力", func(t *testing.T) {
		if !ShouldApplyClaudeCodeMessagesCompatibility(newMessages(), ClientCompatibilityOptions{RequestClientCompatibility: "claude_code"}) {
			t.Fatal("显式能力应触发")
		}
	})
	t.Run("网关客户端画像头归一化", func(t *testing.T) {
		request := newMessages()
		request.Header.Set(GatewayClientProfileHeader, "Claude Code")
		if !ShouldApplyClaudeCodeMessagesCompatibility(request, ClientCompatibilityOptions{}) {
			t.Fatal("画像头 Claude Code 应归一化为 claude_code 并触发")
		}
	})
	t.Run("签名信号 >= 2", func(t *testing.T) {
		request := newMessages()
		request.Header.Set("User-Agent", "claude-cli/2.1.201 (external, sdk-cli)")
		request.Header.Set(AnthropicBetaHeader, "claude-code-20250219")
		if !ShouldApplyClaudeCodeMessagesCompatibility(request, ClientCompatibilityOptions{}) {
			t.Fatal("UA + beta 双信号应触发")
		}
	})
	t.Run("单一信号不足", func(t *testing.T) {
		request := newMessages()
		request.Header.Set("User-Agent", "claude-cli/2.1.201 (external, sdk-cli)")
		if ShouldApplyClaudeCodeMessagesCompatibility(request, ClientCompatibilityOptions{}) {
			t.Fatal("单信号不应触发")
		}
	})
	t.Run("目标路径是 messages 也可触发", func(t *testing.T) {
		request := httptest.NewRequest("POST", "/gateway/relay", nil)
		if ShouldApplyClaudeCodeMessagesCompatibility(request, ClientCompatibilityOptions{TargetPathAndQuery: "/v1/messages"}) {
			t.Fatal("目标 messages 但无能力/画像/签名不应触发")
		}
		// 当前实现：签名信号路径要求原始请求本身是 messages POST；仅目标路径
		// 是 messages 时只能靠显式能力或画像头触发。
		request.Header.Set(AnthropicBetaHeader, "claude-code-20250219")
		request.Header.Set(ClaudeCodeSessionIDHeader, "s-1")
		if ShouldApplyClaudeCodeMessagesCompatibility(request, ClientCompatibilityOptions{TargetPathAndQuery: "/v1/messages"}) {
			t.Fatal("目标 messages + 信号不应绕过原始请求判定")
		}
		request.Header.Set(GatewayClientProfileHeader, "claude-code")
		if !ShouldApplyClaudeCodeMessagesCompatibility(request, ClientCompatibilityOptions{TargetPathAndQuery: "/v1/messages"}) {
			t.Fatal("目标 messages + 画像头应触发")
		}
	})
	t.Run("beta=true query 计入信号", func(t *testing.T) {
		request := httptest.NewRequest("POST", "/v1/messages?beta=true", nil)
		request.Header.Set("User-Agent", "claude-cli/2.1.201 (external, sdk-cli)")
		if !ShouldApplyClaudeCodeMessagesCompatibility(request, ClientCompatibilityOptions{}) {
			t.Fatal("UA + beta query 双信号应触发")
		}
	})
}

func TestWAApplyClientCompatibilityHeaders(t *testing.T) {
	t.Run("覆写 UA 并合并 beta", func(t *testing.T) {
		request := httptest.NewRequest("POST", "/v1/messages", nil)
		request.Header.Set("User-Agent", "some-client/1.0")
		request.Header.Set(AnthropicBetaHeader, "Interleaved-Thinking-2025-05-14, output-128k")
		session := ApplyClientCompatibilityHeaders(request, ClientCompatibilityOptions{RequestClientCompatibility: "claude_code"})
		if request.Header.Get("User-Agent") != AnthropicClaudeCodeUserAgent {
			t.Fatalf("User-Agent = %q", request.Header.Get("User-Agent"))
		}
		merged := request.Header.Get(AnthropicBetaHeader)
		// 大小写不敏感合并去重：既有 token 保留原样大小写。
		for _, want := range []string{"interleaved-thinking-2025-05-14", "output-128k", "claude-code-20250219", "effort-2025-11-24"} {
			if !containsTokenFold(merged, want) {
				t.Fatalf("beta 头缺少 %q: %q", want, merged)
			}
		}
		if session == "" || request.Header.Get(ClaudeCodeSessionIDHeader) != session {
			t.Fatalf("会话 ID 未回填: %q", session)
		}
	})
	t.Run("已是 claude-cli UA 不覆写", func(t *testing.T) {
		request := httptest.NewRequest("POST", "/v1/messages", nil)
		request.Header.Set("User-Agent", "claude-cli/9.9.9 (custom)")
		ApplyClientCompatibilityHeaders(request, ClientCompatibilityOptions{RequestClientCompatibility: "claude_code"})
		if request.Header.Get("User-Agent") != "claude-cli/9.9.9 (custom)" {
			t.Fatalf("既有 claude-cli UA 不应覆写: %q", request.Header.Get("User-Agent"))
		}
	})
	t.Run("既有会话 ID 继承", func(t *testing.T) {
		request := httptest.NewRequest("POST", "/v1/messages", nil)
		request.Header.Set(ClaudeCodeSessionIDHeader, "session-42")
		session := ApplyClientCompatibilityHeaders(request, ClientCompatibilityOptions{RequestClientCompatibility: "claude_code"})
		if session != "session-42" {
			t.Fatalf("既有会话 ID 应继承: %q", session)
		}
	})
	t.Run("不触发时不改写", func(t *testing.T) {
		request := httptest.NewRequest("GET", "/v1/models", nil)
		request.Header.Set("User-Agent", "some-client/1.0")
		if session := ApplyClientCompatibilityHeaders(request, ClientCompatibilityOptions{RequestClientCompatibility: "claude_code"}); session != "" {
			t.Fatalf("不触发应返回空: %q", session)
		}
		if request.Header.Get(AnthropicBetaHeader) != "" {
			t.Fatalf("不触发不应改 beta 头: %q", request.Header.Get(AnthropicBetaHeader))
		}
	})
}

func containsToken(header, token string) bool {
	return containsTokenFold(header, token)
}

// containsTokenFold 大小写不敏感的逗号 token 匹配。
func containsTokenFold(header, token string) bool {
	for _, item := range splitString(header, ',') {
		if equalFoldASCII(trimSpaces(item), token) {
			return true
		}
	}
	return false
}

func equalFoldASCII(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for index := 0; index < len(a); index++ {
		left, right := a[index], b[index]
		if 'A' <= left && left <= 'Z' {
			left += 'a' - 'A'
		}
		if 'A' <= right && right <= 'Z' {
			right += 'a' - 'A'
		}
		if left != right {
			return false
		}
	}
	return true
}

func splitString(value string, sep byte) []string {
	var parts []string
	start := 0
	for index := 0; index < len(value); index++ {
		if value[index] == sep {
			parts = append(parts, value[start:index])
			start = index + 1
		}
	}
	return append(parts, value[start:])
}

func trimSpaces(value string) string {
	start, end := 0, len(value)
	for start < end && (value[start] == ' ' || value[start] == '\t') {
		start++
	}
	for end > start && (value[end-1] == ' ' || value[end-1] == '\t') {
		end--
	}
	return value[start:end]
}

// 会话 ID 继承链：session > x-client-request-id > x-request-id > 生成（可缓存）。
func TestWAClaudeCodeSessionIDForRequest(t *testing.T) {
	request := httptest.NewRequest("POST", "/v1/messages", nil)
	request.Header.Set(ClaudeCodeSessionIDHeader, " s-1 ")
	if got := ClaudeCodeSessionIDForRequest(request, nil); got != "s-1" {
		t.Fatalf("session 头优先且去空白 = %q", got)
	}
	request = httptest.NewRequest("POST", "/v1/messages", nil)
	request.Header.Set(ClientRequestIDHeader, "client-1")
	if got := ClaudeCodeSessionIDForRequest(request, nil); got != "client-1" {
		t.Fatalf("x-client-request-id 回退 = %q", got)
	}
	request = httptest.NewRequest("POST", "/v1/messages", nil)
	request.Header.Set(RequestIDHeader, "req-1")
	if got := ClaudeCodeSessionIDForRequest(request, nil); got != "req-1" {
		t.Fatalf("x-request-id 回退 = %q", got)
	}
	// holder 复用：两次请求同一 holder 返回同一生成值。
	request = httptest.NewRequest("POST", "/v1/messages", nil)
	holder := ""
	first := ClaudeCodeSessionIDForRequest(request, &holder)
	second := ClaudeCodeSessionIDForRequest(httptest.NewRequest("POST", "/v1/messages", nil), &holder)
	if first == "" || first != second {
		t.Fatalf("holder 应复用生成值: %q vs %q", first, second)
	}
	// holder 已有值直接使用。
	existing := "preset"
	if got := ClaudeCodeSessionIDForRequest(httptest.NewRequest("POST", "/v1/messages", nil), &existing); got != "preset" {
		t.Fatalf("holder 既有值应直接使用 = %q", got)
	}
}

func TestWAPathAndQueryForRequest(t *testing.T) {
	request := httptest.NewRequest("POST", "/v1/messages", nil)
	t.Run("触发时补 beta=true", func(t *testing.T) {
		got := PathAndQueryForRequest(request, "/v1/messages", &ClientCompatibilityOptions{RequestClientCompatibility: "claude_code"})
		if got != "/v1/messages?beta=true" {
			t.Fatalf("补参 = %q", got)
		}
	})
	t.Run("不触发时原样", func(t *testing.T) {
		got := PathAndQueryForRequest(request, "/v1/messages", &ClientCompatibilityOptions{})
		if got != "/v1/messages" {
			t.Fatalf("不触发 = %q", got)
		}
	})
	t.Run("空路径回退请求自身", func(t *testing.T) {
		got := PathAndQueryForRequest(request, "", &ClientCompatibilityOptions{RequestClientCompatibility: "claude_code"})
		if got != "/v1/messages?beta=true" {
			t.Fatalf("空路径 = %q", got)
		}
	})
	t.Run("nil options", func(t *testing.T) {
		if got := PathAndQueryForRequest(request, "/v1/messages", nil); got != "/v1/messages" {
			t.Fatalf("nil options = %q", got)
		}
	})
}

func TestWAMergeAnthropicBetaHeader(t *testing.T) {
	got := mergeAnthropicBetaHeader("A, B ,, ", []string{"b", "c", "d"})
	if !containsToken(got, "A") || !containsToken(got, "B") || !containsToken(got, "c") || !containsToken(got, "d") {
		t.Fatalf("合并结果 = %q", got)
	}
	// 大小写不敏感去重：保留先出现的原样值。
	dedup := mergeAnthropicBetaHeader("X-Pilot", []string{"x-pilot"})
	if dedup != "X-Pilot" {
		t.Fatalf("大小写去重应保留首个 = %q", dedup)
	}
	if mergeAnthropicBetaHeader("", nil) != "" {
		t.Fatal("空输入应为空")
	}
	if items := splitAnthropicBetaHeader(""); items != nil {
		t.Fatalf("空串应返回 nil: %v", items)
	}
}

func TestWAIsClaudeCodeUserAgent(t *testing.T) {
	cases := []struct {
		value string
		want  bool
	}{
		{"claude-cli/2.1.201 (external, sdk-cli)", true},
		{"Mozilla/5.0 claude-cli/1.0", true}, // 包含 " claude-cli/"
		{"Claude-CLI/2.0", true},             // 大小写不敏感前缀
		{"", false},
		{"some-client/1.0", false},
		{"xclaude-cli/1.0", false},
	}
	for _, tc := range cases {
		if got := isClaudeCodeUserAgent(tc.value); got != tc.want {
			t.Fatalf("isClaudeCodeUserAgent(%q) = %v，期望 %v", tc.value, got, tc.want)
		}
	}
}

func TestWAParseGatewayClientProfileHeader(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"   ", ""},
		{"Claude Code", "claude_code"},
		{"openai-compact\tprofile", "openai_compact_profile"},
		{"  gemini--web  ", "gemini_web"}, // 连续分隔符合并为单个下划线
	}
	for _, tc := range cases {
		if got := parseGatewayClientProfileHeader(tc.in); got != tc.want {
			t.Fatalf("parseGatewayClientProfileHeader(%q) = %q，期望 %q", tc.in, got, tc.want)
		}
	}
}

func TestWAHasClaudeCodeBetaAndSession(t *testing.T) {
	request := httptest.NewRequest("POST", "/v1/messages", nil)
	request.Header.Set(AnthropicBetaHeader, "OAuth-2024-01, claude-code-20250219")
	if !hasClaudeCodeBetaHeader(request) {
		t.Fatal("含 claude-code- 前缀 beta 应命中")
	}
	request.Header.Set(AnthropicBetaHeader, "oauth-2024-01")
	if hasClaudeCodeBetaHeader(request) {
		t.Fatal("无 claude-code- 前缀不应命中")
	}
	request.Header.Set(AnthropicBetaHeader, "")
	if hasClaudeCodeBetaHeader(request) {
		t.Fatal("空 beta 头不应命中")
	}

	agent := httptest.NewRequest("POST", "/v1/messages", nil)
	agent.Header.Set(ClaudeCodeAgentIDHeader, "agent-1")
	if !hasClaudeCodeSessionHeader(agent) {
		t.Fatal("agent ID 头应命中")
	}

	betaQuery := httptest.NewRequest("POST", "/v1/messages?beta=true", nil)
	if !hasAnthropicBetaQuery(RequestPathAndQuery(betaQuery)) {
		t.Fatal("beta=true query 应命中")
	}
	off := httptest.NewRequest("POST", "/v1/messages?beta=false", nil)
	if hasAnthropicBetaQuery(RequestPathAndQuery(off)) {
		t.Fatal("beta=false 不应命中")
	}
	broken := httptest.NewRequest("POST", "/v1/messages?%zz=1", nil)
	if hasAnthropicBetaQuery(RequestPathAndQuery(broken)) {
		t.Fatal("非法 query 不应命中")
	}
	noQuery := httptest.NewRequest("POST", "/v1/messages", nil)
	if hasAnthropicBetaQuery(RequestPathAndQuery(noQuery)) {
		t.Fatal("无 query 不应命中")
	}
}

func TestWAFirstHeaderValue(t *testing.T) {
	request := httptest.NewRequest("POST", "/v1/messages", nil)
	request.Header.Set("A", " ")
	request.Header.Set("B", "b")
	if got := firstHeaderValue(request, "A", "B"); got != "b" {
		t.Fatalf("空值跳过 = %q", got)
	}
	if got := firstHeaderValue(request, "C"); got != "" {
		t.Fatalf("缺失头 = %q", got)
	}
}

// Driver 门面：所有方法都应与包级函数语义一致（G05/G16 编排层基于该接口注册）。
func TestWADriverIdentity(t *testing.T) {
	driver := AnthropicV1Driver
	if driver.ID() != DriverID || driver.ID() != "anthropic-v1" {
		t.Fatalf("ID = %q", driver.ID())
	}
	if driver.ProtocolCode() != "anthropic" || driver.ProtocolVersion() != "v1" {
		t.Fatalf("协议标识 = %q/%q", driver.ProtocolCode(), driver.ProtocolVersion())
	}
	if driver.ResponseProtocol() != ResponseProtocol || driver.ResponseProtocol() != "anthropic_v1" {
		t.Fatalf("ResponseProtocol = %q", driver.ResponseProtocol())
	}
	if driver.ClientErrorProtocol() != "anthropic" {
		t.Fatalf("ClientErrorProtocol = %q", driver.ClientErrorProtocol())
	}
	if driver.DefaultClientProfile() != "generic_anthropic" {
		t.Fatalf("DefaultClientProfile = %q", driver.DefaultClientProfile())
	}
	if !driver.SupportsProfile("anthropic", "v1") || driver.SupportsProfile("openai", "v1") {
		t.Fatal("SupportsProfile 语义错误")
	}
	if driver.SSEResponseInspectionFailureEvent() != "none" {
		t.Fatalf("Anthropic 流内无显式失败事件名，应为 none: %q", driver.SSEResponseInspectionFailureEvent())
	}
	if driver.DrainForKeepAliveAfterTerminal() {
		t.Fatal("Anthropic 终止后不需要 Keep-Alive 排空")
	}
}

func TestWADriverRequestClassification(t *testing.T) {
	driver := AnthropicV1Driver
	messages := httptest.NewRequest("POST", "/v1/messages?beta=true", nil)
	if !driver.IsNativeRequest(messages) || driver.IsModelsRequest(messages) {
		t.Fatal("messages 请求分类错误")
	}
	models := httptest.NewRequest("GET", "/v1/models", nil)
	if !driver.IsModelsRequest(models) {
		t.Fatal("models 请求分类错误")
	}
	if !driver.IsNativeRequest(models) {
		t.Fatal("GET /models 属于原生面")
	}
	if driver.EndpointModeForRequestShape("/v1/messages", true) != EndpointModeMessagesSSE {
		t.Fatal("SSE 模式错误")
	}
	if driver.ResponseEndpointFamilyForRequest(messages) != EndpointFamilyMessages {
		t.Fatal("端点族错误")
	}
	if got := driver.ResponseInspectionEndpointFamily("message_token_counting"); got != EndpointFamilyMessageTokenCount {
		t.Fatalf("已知族应原样返回: %q", got)
	}
	if got := driver.ResponseInspectionEndpointFamily("whatever"); got != EndpointFamilyMessages {
		t.Fatalf("未知族应回退 messages: %q", got)
	}
}

func TestWADriverExtractionAndUsage(t *testing.T) {
	driver := AnthropicV1Driver
	request := httptest.NewRequest("POST", "/v1/messages", nil)
	root := waParseJSONObject(t, `{"content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":2}}`)
	frames := driver.ExtractJSONSemanticFrames(root, request)
	if len(frames) == 0 {
		t.Fatal("driver JSON 帧提取不应为空")
	}
	event := ParseSSEEventData(`{"type":"message_stop"}`, "", "", 0)
	if frames := driver.ExtractSSESemanticFrames(event, "messages"); len(frames) == 0 {
		t.Fatal("driver SSE 帧提取不应为空")
	}
	if inspector := driver.CreateStreamInspector(); inspector == nil {
		t.Fatal("CreateStreamInspector 不应为 nil")
	}
	usageBody := []byte(`{"usage":{"input_tokens":6,"output_tokens":1}}`)
	waAssertToken(t, driver.ParseUsageFromJSONBuffer(usageBody).InputTokens, 6, "buffer input")
	waAssertToken(t, driver.ParseUsageFromJSONValue(root).OutputTokens, 2, "value output")
	waAssertToken(t, driver.ParseUsageFromJSONTextFragment(string(usageBody)).InputTokens, 6, "fragment input")
	payload := driver.ParseErrorPayload(`{"error":{"message":"m"}}`, nil)
	if payload.Message != "m" {
		t.Fatalf("driver 错误负载 = %+v", payload)
	}
	payload = driver.ParseErrorPayloadFromJSONValue(waParseJSONObject(t, `{"error":{"code":"c"}}`))
	if payload.Code != "c" {
		t.Fatalf("driver JSONValue 错误负载 = %+v", payload)
	}
	result := driver.ApplyStreamUsageFallback(RequestFacts{}, EmptyUsage(), StreamUsageFallbackInput{OutputReceived: true})
	if !result.Estimated {
		t.Fatal("driver 兜底应估算")
	}
}
