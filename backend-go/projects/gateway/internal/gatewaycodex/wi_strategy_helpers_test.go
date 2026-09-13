package gatewaycodex

import (
	"testing"
	"time"
)

func TestWIGatewayClientAllowsUpstreamSemanticInterpretation(t *testing.T) {
	tests := []struct {
		profile string
		want    bool
	}{
		{ClientProfileCodex, true},
		{ClientProfileClaudeCode, true},
		{ClientProfileGeminiCLI, true},
		{"openai-standard", false},
		{"", false},
	}
	for _, tc := range tests {
		if got := GatewayClientAllowsUpstreamSemanticInterpretation(OpenAIGatewayClientStrategyContext{ClientProfile: tc.profile}); got != tc.want {
			t.Fatalf("profile=%q got=%v want=%v", tc.profile, got, tc.want)
		}
	}
}

func TestWICodexTurnStateKeyAndClientSourceNilFields(t *testing.T) {
	// nil turn / 空 state key → nil（audit 元数据省略字段）。
	if codexTurnStateKeyOrNil(nil) != nil {
		t.Fatal("nil turn 必须返回 nil")
	}
	if codexTurnStateKeyOrNil(&OpenAIGatewayCodexTurnContext{}) != nil {
		t.Fatal("空 stateKey 必须返回 nil")
	}
	if got := codexTurnStateKeyOrNil(&OpenAIGatewayCodexTurnContext{StateKey: "k"}); got != "k" {
		t.Fatalf("stateKey=%v", got)
	}
	// nil source → 三个字段全 nil；非 nil 时按字段序号取值。
	for field := 0; field < 3; field++ {
		if clientSourceFieldOrNil(nil, field) != nil {
			t.Fatalf("nil source field=%d 必须返回 nil", field)
		}
	}
	source := &GatewayClientSourceIdentity{Status: "resolved", Kind: "codex", SemanticNamespace: "ns"}
	if got := clientSourceFieldOrNil(source, sourceFieldValueStatus); got != "resolved" {
		t.Fatalf("status=%v", got)
	}
	if got := clientSourceFieldOrNil(source, sourceFieldValueKind); got != "codex" {
		t.Fatalf("kind=%v", got)
	}
	if got := clientSourceFieldOrNil(source, sourceFieldValueNamespace); got != "ns" {
		t.Fatalf("namespace=%v", got)
	}
	if clientSourceFieldOrNil(source, 99) != nil {
		t.Fatal("未知字段序号必须返回 nil")
	}
}

func TestWIResolveClientSourceFallbacks(t *testing.T) {
	// Source 未装配 → missing 状态（不是 panic / 静默成功）。
	deps := &ClientStrategyDeps{}
	resolved := deps.resolveClientSource(nil, ClientStrategyIdentity{}, ClientProfileCodex, "header", "openai_v1")
	if resolved == nil || resolved.Status != SourceStatusMissing {
		t.Fatalf("resolved=%+v", resolved)
	}
	// derive：resolver 缺失 → 空 key。
	if got := deps.deriveClientSourceStateKey(resolved, ClientProfileCodex, "/responses", "openai_v1"); got != "" {
		t.Fatalf("stateKey=%q", got)
	}
}

func TestWIURLDecodeComponentAndHexDigit(t *testing.T) {
	// URLSearchParams 语义：'+' 是空格；%XX 解码；非法 % 序列原样保留。
	tests := []struct {
		in   string
		want string
	}{
		{"plain", "plain"},
		{"a+b", "a b"},
		{"%20", " "},
		{"%2F", "/"},
		{"%2f", "/"},
		{"%zz", "%zz"},
		{"%2", "%2"},
		{"100%", "100%"},
		{"k%26ey=%2B1", "k&ey=+1"},
	}
	for _, tc := range tests {
		if got := urlDecodeComponent(tc.in); got != tc.want {
			t.Fatalf("urlDecodeComponent(%q)=%q want %q", tc.in, got, tc.want)
		}
	}
	for i := 0; i < 256; i++ {
		c := byte(i)
		_, ok := hexDigit(c)
		want := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
		if ok != want {
			t.Fatalf("hexDigit(%q) ok=%v want=%v", string(c), ok, want)
		}
	}
	if got, _ := hexDigit('f'); got != 15 {
		t.Fatalf("hexDigit('f')=%d", got)
	}
	if got, _ := hexDigit('A'); got != 10 {
		t.Fatalf("hexDigit('A')=%d", got)
	}
}

func TestWIParseURLSearchParamsAndGeminiSignals(t *testing.T) {
	// 首个同名 key 生效（single-value 语义）+ '+' 空格。
	req := newTestRequest(t, "POST", "/v1beta/models/gemini:generatecontent?alt=sse&key=abc&flag=%2Fx&dup=1&dup=2", nil, nil)
	values := parseURLSearchParams("alt=sse&key=abc&flag=%2Fx&dup=1&dup=2")
	if values["alt"] != "sse" || values["key"] != "abc" || values["flag"] != "/x" || values["dup"] != "1" {
		t.Fatalf("values=%v", values)
	}
	if _, ok := geminiQueryParam(req, "missing"); ok {
		t.Fatal("缺失参数不得命中")
	}
	if got, ok := geminiQueryParam(req, "key"); !ok || got != "abc" {
		t.Fatalf("key=%q ok=%v", got, ok)
	}
	if !hasGeminiAuthSignal(req) {
		t.Fatal("key 查询参数必须算作 gemini 鉴权信号")
	}
	// 无查询串 → 无信号。
	bare := newTestRequest(t, "POST", "/v1beta/models/gemini:generatecontent", nil, nil)
	if hasGeminiAuthSignal(bare) {
		t.Fatal("无任何鉴权信号不得命中")
	}
	// 空 query 值不算有效信号。
	emptyValue := newTestRequest(t, "POST", "/models/gemini:generatecontent?key=", nil, nil)
	if hasGeminiAuthSignal(emptyValue) {
		t.Fatal("空值 key 不得命中")
	}
	// alt=sse 触发流式信号。
	if !geminiAltSSEQuery(req) {
		t.Fatal("alt=sse 必须命中")
	}
	plain := newTestRequest(t, "POST", "/models/gemini:generatecontent", nil, nil)
	if geminiAltSSEQuery(plain) {
		t.Fatal("无 alt 不得命中")
	}
}

func TestWIIsGeminiCliRequestSignature(t *testing.T) {
	// 非 POST 不命中。
	get := newTestRequest(t, "GET", "/models/gemini:generatecontent?alt=sse", nil, map[string]string{"User-Agent": "GeminiCLI/v1"})
	if isGeminiCliRequestSignature(get) {
		t.Fatal("GET 请求不得命中")
	}
	// 路径不是 models action 不命中。
	wrongPath := newTestRequest(t, "POST", "/interactions/abc", nil, map[string]string{"User-Agent": "GeminiCLI/v1", "X-Goog-Api-Key": "k"})
	if isGeminiCliRequestSignature(wrongPath) {
		t.Fatal("非 models action 路径不得命中")
	}
	// 完整签名命中。
	full := newTestRequest(t, "POST", "/models/gemini:generatecontent", nil, map[string]string{"User-Agent": "GeminiCLI/v1", "X-Goog-Api-Key": "k"})
	if !isGeminiCliRequestSignature(full) {
		t.Fatal("完整签名必须命中")
	}
	// UA 缺失不命中。
	noUA := newTestRequest(t, "POST", "/models/gemini:generatecontent", nil, map[string]string{"X-Goog-Api-Key": "k"})
	if isGeminiCliRequestSignature(noUA) {
		t.Fatal("缺 UA 不得命中")
	}
	// hasGeminiCliUserAgent 的单词边界。
	if hasGeminiCliUserAgent(newTestRequest(t, "POST", "/x", nil, map[string]string{"User-Agent": "notgeminicli/v1"})) {
		t.Fatal("非独立词不得命中")
	}
	if !hasGeminiCliUserAgent(newTestRequest(t, "POST", "/x", nil, map[string]string{"User-Agent": "proxy GeminiCLI"})) {
		t.Fatal("GeminiCLI 词尾必须命中")
	}
}

func TestWINormalizedGeminiRequestPath(t *testing.T) {
	tests := []struct {
		target string
		want   string
	}{
		{"/v1beta/models/gemini:generatecontent?alt=sse", "/models/gemini:generatecontent"},
		{"/V1BETA/models/x", "/models/x"},
		{"/v1betaX/models/x", "/v1betax/models/x"},
		{"/", "/"},
		{"/Interactions/ABC", "/interactions/abc"},
	}
	for _, tc := range tests {
		req := newTestRequest(t, "POST", tc.target, nil, nil)
		if got := normalizedGeminiRequestPath(req); got != tc.want {
			t.Fatalf("path(%q)=%q want %q", tc.target, got, tc.want)
		}
	}
	// v1beta 根自身也被剥掉（len(path)==len("/v1beta") 分支）。
	// strip 后空路径回落 "/"。
	if got := stripV1PrefixByGeminiRuleForTest(t, "/v1beta"); got != "/" {
		t.Fatalf("v1beta 根=%q", got)
	}
}

// stripV1PrefixByGeminiRuleForTest 直接走 normalizedGeminiRequestPath 的根路径分支。
func stripV1PrefixByGeminiRuleForTest(t *testing.T, rawPath string) string {
	t.Helper()
	req := newTestRequest(t, "POST", rawPath, nil, nil)
	return normalizedGeminiRequestPath(req)
}

func TestWIHasClaudeCodeUserAgent(t *testing.T) {
	// 前缀与空格分隔的 UA 命中；子串不算；空 UA 不算。
	if !hasClaudeCodeUserAgent(newTestRequest(t, "POST", "/messages", nil, map[string]string{"User-Agent": "claude-cli/1.2"})) {
		t.Fatal("前缀 UA 必须命中")
	}
	if !hasClaudeCodeUserAgent(newTestRequest(t, "POST", "/messages", nil, map[string]string{"User-Agent": "Mozilla claude-cli/1"})) {
		t.Fatal("空格分隔 UA 必须命中")
	}
	if hasClaudeCodeUserAgent(newTestRequest(t, "POST", "/messages", nil, map[string]string{"User-Agent": "xclaude-cli/1"})) {
		t.Fatal("子串不得命中")
	}
	if hasClaudeCodeUserAgent(newTestRequest(t, "POST", "/messages", nil, map[string]string{"User-Agent": "  "})) {
		t.Fatal("空 UA 不得命中")
	}
}

func TestWICompactionContractHelpers(t *testing.T) {
	// normalizeOpenAIRequestPath / stripV1Prefix 矩阵。
	pathTests := []struct {
		in   string
		want string
	}{
		{"/v1/responses?x=1", "/responses"},
		{"/v1", "/"},
		{"/v1/", "/"},
		{"/v1x/responses", "/v1x/responses"},
		{"responses", "/responses"},
		{"", "/"},
		{"/chat/completions", "/chat/completions"},
	}
	for _, tc := range pathTests {
		if got := normalizeOpenAIRequestPath(tc.in); got != tc.want {
			t.Fatalf("normalize(%q)=%q want %q", tc.in, got, tc.want)
		}
	}
	// responses POST 判定（含 /v1 前缀）。
	if !isOpenAIResponsesPostRequest(newTestRequest(t, "POST", "/v1/responses", nil, nil)) {
		t.Fatal("POST /v1/responses 必须命中")
	}
	if isOpenAIResponsesPostRequest(newTestRequest(t, "GET", "/responses", nil, nil)) {
		t.Fatal("GET 不得命中")
	}
	if isOpenAIResponsesPostRequest(newTestRequest(t, "POST", "/v1/responses/compact", nil, nil)) {
		t.Fatal("/responses/compact 不得算 /responses")
	}
	// compact POST 判定。
	if !isOpenAIResponsesCompactPostRequest(newTestRequest(t, "POST", "/responses/compact", nil, nil)) {
		t.Fatal("POST /responses/compact 必须命中")
	}
	if isOpenAIResponsesCompactPostRequest(newTestRequest(t, "GET", "/responses/compact", nil, nil)) {
		t.Fatal("GET 不得命中")
	}
	// 解析 JSON 对象体：非法与空值返回 nil。
	if parseJSONObjectValue("") != nil {
		t.Fatal("空字符串必须 nil")
	}
	if parseJSONObjectValue("not-json") != nil {
		t.Fatal("非法 JSON 必须 nil")
	}
	if parseJSONObjectValue("[1,2]") != nil {
		t.Fatal("非对象必须 nil")
	}
	parsed := parseJSONObjectValue(`{"turn_id":"t1"}`)
	if parsed == nil || parsed["turn_id"] != "t1" {
		t.Fatalf("parsed=%v", parsed)
	}
}

func TestWIRandomUUIDAndNowMs(t *testing.T) {
	// RandomUUID 输出 v4 形状且每次不同。
	first := RandomUUID()
	second := RandomUUID()
	if first == second {
		t.Fatalf("两次 UUID 不应相同: %s", first)
	}
	if len(first) != 36 || first[14] != '4' {
		t.Fatalf("UUID 形状错误: %s", first)
	}
	// NowMs：nil clock 回落系统时钟；注入 clock 用注入值。
	if NowMs(nil) <= 0 {
		t.Fatal("系统时钟毫秒必须为正")
	}
	base := time.Date(2026, 9, 10, 8, 0, 0, 0, time.UTC)
	clock := newFakeClock(base)
	if got := NowMs(clock); got != base.UnixMilli() {
		t.Fatalf("NowMs=%d", got)
	}
}
