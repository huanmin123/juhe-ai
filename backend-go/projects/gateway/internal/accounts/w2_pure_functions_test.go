package accounts

// W2 纯函数测试：endpoint 模式驱动归一与兼容断言（endpoint_modes.go）、
// 上游 Base URL 安全校验（upstream_base_url.go）、可用性时间表归一与窗口
// 判定（schedule.go）。全部无副作用、可重放。

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func TestW2NormalizeEndpointModeList(t *testing.T) {
	known := isOpenAIEndpointMode
	defaults := []string{"chat_json"}

	t.Run("缺省键回落默认", func(t *testing.T) {
		got, err := normalizeEndpointModeList(optionalValue{}, known, defaults, "上游接口能力")
		if err != nil || len(got) != 1 || got[0] != "chat_json" {
			t.Fatalf("缺省回落不一致：%v %v", got, err)
		}
	})
	t.Run("显式 null 拒绝", func(t *testing.T) {
		if _, err := normalizeEndpointModeList(opt(nil), known, defaults, "上游接口能力"); err == nil {
			t.Fatal("显式 null 应拒绝")
		}
	})
	t.Run("非数组拒绝", func(t *testing.T) {
		_, err := normalizeEndpointModeList(opt("chat_json"), known, defaults, "上游接口能力")
		if err == nil || err.Error() != "上游接口能力必须是数组" {
			t.Fatalf("非数组错误不一致：%v", err)
		}
	})
	t.Run("不支持值与渲染", func(t *testing.T) {
		for _, item := range []any{nil, float64(3), true, "bogus"} {
			_, err := normalizeEndpointModeList(opt([]any{item}), known, defaults, "上游接口能力")
			if err == nil || !strings.Contains(err.Error(), "上游接口能力包含不支持的能力：") {
				t.Fatalf("值 %v 应拒绝：%v", item, err)
			}
		}
	})
	t.Run("空数组与去重", func(t *testing.T) {
		_, err := normalizeEndpointModeList(opt([]any{}), known, defaults, "上游接口能力")
		if err == nil || err.Error() != "上游接口能力至少选择一项" {
			t.Fatalf("空数组错误不一致：%v", err)
		}
		got, err := normalizeEndpointModeList(opt([]any{"chat_json", "chat_sse", "chat_json"}), known, defaults, "上游接口能力")
		if err != nil || len(got) != 2 || got[0] != "chat_json" || got[1] != "chat_sse" {
			t.Fatalf("去重结果不一致：%v %v", got, err)
		}
	})
	t.Run("renderUnsupportedValue", func(t *testing.T) {
		cases := map[any]string{nil: "null", "bogus": "bogus", false: "false", true: "true",
			float64(1.5): "1.5", struct{}{}: "{}"}
		for input, want := range cases {
			if got := renderUnsupportedValue(input); got != want {
				t.Fatalf("渲染不一致：%v -> %q (期望 %q)", input, got, want)
			}
		}
	})
}

func TestW2EndpointModeWriteDrivers(t *testing.T) {
	t.Run("anthropic 默认与 pin", func(t *testing.T) {
		plain := defaultAnthropicEndpointModes(endpointModeDefaultContext{})
		if len(plain) != 3 || plain[0] != "messages_json" || plain[2] != "message_token_counting" {
			t.Fatalf("anthropic 默认不一致：%v", plain)
		}
		pinned := defaultAnthropicEndpointModes(endpointModeDefaultContext{providerProtocolProfileID: deepSeekAnthropicV1ProfileID})
		if len(pinned) != 2 || pinned[0] != "messages_json" {
			t.Fatalf("deepseek pin 不一致：%v", pinned)
		}
		pinnedGLM := defaultAnthropicEndpointModes(endpointModeDefaultContext{providerProtocolProfileID: glmCodingAnthropicV1ProfileID})
		if len(pinnedGLM) != 2 {
			t.Fatalf("glm pin 不一致：%v", pinnedGLM)
		}
	})
	t.Run("gemini 驱动", func(t *testing.T) {
		got, err := normalizeGeminiEndpointModesForWrite(opt([]any{"generate_content_json", "bogus"}), endpointModeDefaultContext{})
		if err == nil {
			t.Fatalf("不支持值应拒绝：%v", got)
		}
		got, err = normalizeGeminiEndpointModesForWrite(opt([]any{"generate_content_sse"}), endpointModeDefaultContext{})
		if err != nil || len(got) != 1 {
			t.Fatalf("合法值不一致：%v %v", got, err)
		}
	})
	t.Run("deepseek anthropic pin", func(t *testing.T) {
		context := endpointModeDefaultContext{providerCode: deepSeekProviderCode,
			protocolCode: anthropicProtocolCodeConstant, protocolVersion: anthropicProtocolVersionConstant}
		got, err := normalizeDeepSeekEndpointModesForWrite(opt([]any{"messages_json"}), context)
		if err != nil || len(got) != 1 || got[0] != "messages_json" {
			t.Fatalf("messages 模式应通过：%v %v", got, err)
		}
		_, err = normalizeDeepSeekEndpointModesForWrite(opt([]any{"message_token_counting"}), context)
		if err == nil || !strings.Contains(err.Error(), "DeepSeek Anthropic 账户上游接口能力只支持") {
			t.Fatalf("count_token 应拒绝：%v", err)
		}
		// openai 协议侧普通归一。
		openAIContext := endpointModeDefaultContext{providerCode: deepSeekProviderCode,
			protocolCode: openAIProtocolCode, protocolVersion: openAIProtocolVersion}
		got, err = normalizeDeepSeekEndpointModesForWrite(opt([]any{"responses_json"}), openAIContext)
		if err != nil || len(got) != 1 {
			t.Fatalf("openai 侧应通过：%v %v", got, err)
		}
	})
	t.Run("glm pin 与 chat 限制", func(t *testing.T) {
		anthropicContext := endpointModeDefaultContext{providerCode: glmProviderCode,
			providerProtocolProfileID: glmCodingAnthropicV1ProfileID}
		got, err := normalizeGLMEndpointModesForWrite(opt([]any{"messages_sse"}), anthropicContext)
		if err != nil || len(got) != 1 {
			t.Fatalf("glm anthropic 应通过：%v %v", got, err)
		}
		_, err = normalizeGLMEndpointModesForWrite(opt([]any{"message_token_counting"}), anthropicContext)
		if err == nil || !strings.Contains(err.Error(), "智谱 GLM Coding Anthropic") {
			t.Fatalf("glm anthropic 限制错误不一致：%v", err)
		}
		codingOpenAI := endpointModeDefaultContext{providerCode: glmProviderCode,
			providerProtocolProfileID: glmCodingOpenAIV1ProfileID,
			protocolCode:              openAIProtocolCode, protocolVersion: openAIProtocolVersion}
		_, err = normalizeGLMEndpointModesForWrite(opt([]any{"responses_json"}), codingOpenAI)
		if err == nil || !strings.Contains(err.Error(), "OpenAI Chat Completions") {
			t.Fatalf("glm coding openai 限制错误不一致：%v", err)
		}
		generalOpenAI := endpointModeDefaultContext{providerCode: glmProviderCode,
			providerProtocolProfileID: glmGeneralOpenAIV1ProfileID,
			protocolCode:              openAIProtocolCode, protocolVersion: openAIProtocolVersion}
		_, err = normalizeGLMEndpointModesForWrite(opt([]any{"responses_json"}), generalOpenAI)
		if err == nil || !strings.Contains(err.Error(), "对话补全") {
			t.Fatalf("glm 通用限制错误不一致：%v", err)
		}
		got, err = normalizeGLMEndpointModesForWrite(opt([]any{"chat_json"}), generalOpenAI)
		if err != nil || len(got) != 1 {
			t.Fatalf("glm chat 应通过：%v %v", got, err)
		}
	})
	t.Run("未注册驱动报错", func(t *testing.T) {
		_, err := normalizeEndpointModesForWrite(opt([]any{}), endpointModeDefaultContext{providerCode: "nosuch"})
		if err == nil || !strings.Contains(err.Error(), "供应商协议档案未注册接口能力归一化") {
			t.Fatalf("未注册驱动错误不一致：%v", err)
		}
	})
	t.Run("modeContextWith 固定账户类型", func(t *testing.T) {
		context := endpointModeDefaultContext{providerCode: "gpt", accountType: "api_key"}
		pinned := context.modeContextWith("oauth")
		if pinned.accountType != "oauth" || pinned.providerCode != "gpt" {
			t.Fatalf("modeContextWith 不一致：%+v", pinned)
		}
	})
}

func TestW2AssertEndpointModesCompatible(t *testing.T) {
	t.Run("openai oauth", func(t *testing.T) {
		if err := assertOpenAIEndpointModesCompatible([]string{"responses_json", "responses_sse"}, "oauth", ""); err != nil {
			t.Fatalf("oauth 合法组合应通过：%v", err)
		}
		err := assertOpenAIEndpointModesCompatible([]string{"chat_json"}, "oauth", "")
		if err == nil || !strings.Contains(err.Error(), "OAuth 账户上游接口能力只能选择") {
			t.Fatalf("oauth 非法模式应拒绝：%v", err)
		}
		err = assertOpenAIEndpointModesCompatible([]string{"responses_json"}, "oauth", "")
		if err == nil || !strings.Contains(err.Error(), "必须启用 Responses API (Streaming)") {
			t.Fatalf("缺 sse 应拒绝：%v", err)
		}
		err = assertOpenAIEndpointModesCompatible([]string{"chat_json"}, "api_key", "codex_responses")
		if err == nil || !strings.Contains(err.Error(), "Codex Responses") {
			t.Fatalf("codex_responses 缺 sse 应拒绝：%v", err)
		}
	})
	t.Run("anthropic", func(t *testing.T) {
		if err := assertAnthropicEndpointModesCompatible([]string{"messages_json"}, "api_key"); err != nil {
			t.Fatalf("合法组合应通过：%v", err)
		}
		err := assertAnthropicEndpointModesCompatible([]string{"messages_json"}, "vertex")
		if err == nil || !strings.Contains(err.Error(), "仅支持 API Key 或 OAuth") {
			t.Fatalf("非法类型应拒绝：%v", err)
		}
		err = assertAnthropicEndpointModesCompatible([]string{"chat_json"}, "api_key")
		if err == nil || !strings.Contains(err.Error(), "Anthropic 账户上游接口能力不支持") {
			t.Fatalf("不支持模式应拒绝：%v", err)
		}
		err = assertAnthropicEndpointModesCompatible([]string{"message_token_counting"}, "api_key")
		if err == nil || !strings.Contains(err.Error(), "必须至少启用 Messages API") {
			t.Fatalf("缺 messages 应拒绝：%v", err)
		}
	})
	t.Run("gemini", func(t *testing.T) {
		if err := assertGeminiEndpointModesCompatible([]string{"generate_content_json"}, "api_key"); err != nil {
			t.Fatalf("合法组合应通过：%v", err)
		}
		if err := assertGeminiEndpointModesCompatible([]string{"interactions_sse"}, "google_oauth"); err != nil {
			t.Fatalf("google_oauth 合法组合应通过：%v", err)
		}
		err := assertGeminiEndpointModesCompatible([]string{"generate_content_json"}, "oauth")
		if err == nil || !strings.Contains(err.Error(), "仅支持 API Key 或 Google OAuth") {
			t.Fatalf("非法类型应拒绝：%v", err)
		}
		err = assertGeminiEndpointModesCompatible([]string{"chat_json"}, "api_key")
		if err == nil || !strings.Contains(err.Error(), "Gemini 账户上游接口能力不支持") {
			t.Fatalf("不支持模式应拒绝：%v", err)
		}
		err = assertGeminiEndpointModesCompatible([]string{"count_tokens"}, "api_key")
		if err == nil || !strings.Contains(err.Error(), "必须至少启用 generateContent") {
			t.Fatalf("缺核心模式应拒绝：%v", err)
		}
	})
	t.Run("分发", func(t *testing.T) {
		if err := assertEndpointModesCompatible("hybrid", "oauth", "", protocolPredicateInput{}, nil); err != nil {
			t.Fatalf("hybrid 直通：%v", err)
		}
		if err := assertEndpointModesCompatible("gpt", "oauth", "", protocolPredicateInput{protocolCode: openAIProtocolCode, protocolVersion: openAIProtocolVersion}, []string{"chat_json"}); err == nil {
			t.Fatal("openai 分发应拒绝 oauth+chat")
		}
		if err := assertEndpointModesCompatible("anthropic", "vertex", "", protocolPredicateInput{protocolCode: anthropicProtocolCodeConstant, protocolVersion: anthropicProtocolVersionConstant}, nil); err == nil {
			t.Fatal("anthropic 分发应拒绝 vertex")
		}
		if err := assertEndpointModesCompatible("gemini", "oauth", "", protocolPredicateInput{protocolCode: geminiProtocolCodeConstant, protocolVersion: geminiProtocolVersionConstant}, nil); err == nil {
			t.Fatal("gemini 分发应拒绝 oauth")
		}
		// 未知协议族直通。
		if err := assertEndpointModesCompatible("custom", "x", "", protocolPredicateInput{protocolCode: "grpc", protocolVersion: "v9"}, nil); err != nil {
			t.Fatalf("未知协议族应直通：%v", err)
		}
	})
}

func TestW2PreferredHealthCheckEndpointMode(t *testing.T) {
	cases := map[[2]string]string{
		{"gemini", "profile_gemini_native_v1beta"}:      "generate_content_json",
		{"xai", "profile_xai_anthropic_v1"}:             "messages_json",
		{"anthropic", "profile_anthropic_anthropic_v1"}: "messages_json",
		{"gpt", "profile_gpt_openai_v1"}:                "responses_sse",
		{"deepseek", "profile_deepseek_openai_v1"}:      "chat_json",
		{"", ""}: "chat_json",
	}
	for input, want := range cases {
		if got := preferredHealthCheckEndpointMode(input[0], input[1]); got != want {
			t.Fatalf("preferred 不一致：%v -> %q (期望 %q)", input, got, want)
		}
	}
	t.Run("resolveDefaultHealthCheckEndpointMode", func(t *testing.T) {
		got, err := resolveDefaultHealthCheckEndpointMode("gpt", "profile_gpt_openai_v1",
			[]string{"chat_json", "responses_sse", "not_a_mode"})
		if err != nil || got != "responses_sse" {
			t.Fatalf("preferred 命中不一致：%q %v", got, err)
		}
		got, err = resolveDefaultHealthCheckEndpointMode("gpt", "", []string{"chat_sse", "responses_sse"})
		if err != nil || got != "responses_sse" {
			t.Fatalf("json 后缀回退不一致：%q %v", got, err)
		}
		got, err = resolveDefaultHealthCheckEndpointMode("deepseek", "", []string{"chat_sse"})
		if err != nil || got != "chat_sse" {
			t.Fatalf("唯一模式回退不一致：%q %v", got, err)
		}
		if _, err = resolveDefaultHealthCheckEndpointMode("gpt", "", []string{"weird_mode"}); err == nil {
			t.Fatal("无可用模式应报错")
		}
	})
	t.Run("isGeminiProviderCodeToken", func(t *testing.T) {
		if !isGeminiProviderCodeToken(" Gemini ") || isGeminiProviderCodeToken("gpt") {
			t.Fatal("gemini token 判定不一致")
		}
	})
}

func TestW2UpstreamBaseURLValidation(t *testing.T) {
	t.Run("原始字符串校验", func(t *testing.T) {
		cases := []struct {
			value   string
			message string
		}{
			{"   ", "上游 Base URL 不能为空"},
			{"https://a\u00a0.com", "上游 Base URL 不能包含空白字符"},
			{"https://a.com\x07", "上游 Base URL 不能包含控制字符"},
			{"https://a.com\\path", "上游 Base URL 不能包含反斜杠"},
		}
		for _, testCase := range cases {
			_, err := validateRawUpstreamURLString(testCase.value)
			if err == nil || !strings.Contains(err.Error(), testCase.message) {
				t.Fatalf("%q：期望 %q，实际 %v", testCase.value, testCase.message, err)
			}
		}
	})
	t.Run("原始绝对地址拆分", func(t *testing.T) {
		bad := []string{"a.com", "1https://a.com", "ht tp://a.com", "https:///x", "https://"}
		for _, value := range bad {
			if _, err := parseRawAbsoluteURLParts(value); err == nil {
				t.Fatalf("%q 应拒绝", value)
			}
		}
		parts, err := parseRawAbsoluteURLParts("https://host:8443/v1?x=1#frag")
		if err != nil {
			t.Fatalf("合法地址不应报错：%v", err)
		}
		if parts.protocol != "https:" || parts.authority != "host:8443" || parts.path != "/v1" ||
			parts.query != "?x=1" || parts.hash != "#frag" {
			t.Fatalf("拆分不一致：%+v", parts)
		}
	})
	t.Run("openai 路径策略", func(t *testing.T) {
		if message := validateOpenAICompatibleBaseURLPath(""); message != "" {
			t.Fatalf("空路径应通过：%q", message)
		}
		if message := validateOpenAICompatibleBaseURLPath("/v1"); message != "" {
			t.Fatalf("v1 根地址应通过：%q", message)
		}
		if message := validateOpenAICompatibleBaseURLPath("/v1/chat/completions"); message == "" {
			t.Fatal("v1 后接口路径应拒绝")
		}
		if message := validateOpenAICompatibleBaseURLPath("/chat/completions"); message == "" {
			t.Fatal("具体接口路径应拒绝")
		}
		if message := validateOpenAICompatibleBaseURLPath("/%zz"); message == "" {
			t.Fatal("非法编码应拒绝")
		}
	})
	t.Run("路径段断言", func(t *testing.T) {
		if err := assertUpstreamPathSegments("/v1/x"); err != nil {
			t.Fatalf("普通路径应通过：%v", err)
		}
		for _, path := range []string{"/a%2Fb", "/a%5Cb", "/a/../b", "/a/./b"} {
			if err := assertUpstreamPathSegments(path); err == nil {
				t.Fatalf("%q 应拒绝", path)
			}
		}
		if err := assertUpstreamPathSegments("/%zz"); err == nil {
			t.Fatal("非法编码应拒绝")
		}
	})
	t.Run("连续斜杠检测", func(t *testing.T) {
		if !matchConsecutiveSlashes("https://a.com//x") || matchConsecutiveSlashes("/single/path") {
			t.Fatal("连续斜杠判定不一致")
		}
	})
	t.Run("安全策略", func(t *testing.T) {
		// 环境变量可能放行私网：只有默认配置下才断言拒绝路径。
		if os.Getenv("JUHE_AI_ALLOW_PRIVATE_UPSTREAM_BASE_URLS") == "true" || os.Getenv("JUHE_AI_ALLOW_PRIVATE_UPSTREAM_BASE_URLS") == "1" {
			t.Skipf("已设置 JUHE_AI_ALLOW_PRIVATE_UPSTREAM_BASE_URLS；跳过私网拒绝断言")
		}
		for _, value := range []string{"http://127.0.0.1:8080/v1", "http://localhost/v1",
			"http://10.0.0.1/v1", "http://192.168.1.1/v1", "http://169.254.1.1/v1"} {
			err := assertSafeUpstreamBaseURL(value)
			if err == nil || !strings.Contains(err.Error(), "上游 Base URL 不能指向本机、内网") {
				t.Fatalf("%q 应拒绝：%v", value, err)
			}
		}
		for _, value := range []string{"https://api.openai.com/v1", "https://example.com:8443/v1"} {
			if err := assertSafeUpstreamBaseURL(value); err != nil {
				t.Fatalf("%q 应通过：%v", value, err)
			}
		}
		// 非法格式拒绝并携带策略文案。
		err := assertSafeUpstreamBaseURL("notaurl")
		if err == nil || !strings.Contains(err.Error(), "上游 Base URL") {
			t.Fatalf("非法格式应拒绝：%v", err)
		}
	})
	t.Run("私网 origin 归一", func(t *testing.T) {
		if origin, ok := normalizePrivateUpstreamOrigin("HTTP://LOCALHOST:443"); !ok || origin != "http://localhost:443" {
			t.Fatalf("origin 归一不一致：%q %v", origin, ok)
		}
		if origin, ok := normalizePrivateUpstreamOrigin("https://a.example.com"); !ok || origin != "https://a.example.com:443" {
			t.Fatalf("默认端口补全不一致：%q %v", origin, ok)
		}
		for _, bad := range []string{"", "ftp://a.com", "/only-path", "https://"} {
			if _, ok := normalizePrivateUpstreamOrigin(bad); ok {
				t.Fatalf("%q 应拒绝", bad)
			}
		}
	})
}

func TestW2ScheduleNormalization(t *testing.T) {
	// always-allow 计划对任意 UTC 时刻放行（与 m09 测试的窗口契约一致）。
	schedule, err := NormalizeSchedule(w2JSONObject(t, alwaysAllowSchedule))
	if err != nil {
		t.Fatalf("合法时间表应通过：%v", err)
	}
	status, override := ScheduleStatus(schedule, time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC))
	if !override || status != "active" {
		t.Fatalf("allow 窗口应覆写 active：%q %v", status, override)
	}
	if _, ok := NextScheduleCheckAt(schedule, time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)); !ok {
		t.Fatal("存在窗口时应给出下一次检查时间")
	}
	if raw, ok := ScheduleJSON(schedule); !ok || !strings.Contains(raw, "allow_windows") {
		t.Fatalf("计划序列化不一致：%q %v", raw, ok)
	}
	parsed, err := ParseScheduleJSON(alwaysAllowSchedule)
	if err != nil || parsed == nil {
		t.Fatalf("计划反序列化失败：%v", err)
	}

	t.Run("非法输入", func(t *testing.T) {
		for _, bad := range []any{
			"not-an-object",
			map[string]any{},
			map[string]any{"enabled": true, "mode": "weird"},
			map[string]any{"enabled": true, "mode": "allow_windows", "timezone": "NoSuch/Zone"},
			map[string]any{"enabled": true, "mode": "allow_windows", "windows": []any{map[string]any{}}},
			map[string]any{"enabled": true, "mode": "allow_windows", "windows": []any{
				map[string]any{"daysOfWeek": []any{float64(0)}, "start": "00:00", "end": "01:00"}},
			},
			map[string]any{"enabled": true, "mode": "allow_windows", "windows": []any{
				map[string]any{"daysOfWeek": []any{float64(1)}, "start": "25:00", "end": "01:00"}},
			},
		} {
			if _, err := NormalizeSchedule(bad); err == nil {
				t.Fatalf("%v 应拒绝", bad)
			}
		}
	})
	t.Run("deny 窗口覆写 disabled", func(t *testing.T) {
		denyAlways := `{"enabled":true,"timezone":"UTC","mode":"allow_windows","windows":[
			{"daysOfWeek":[1],"start":"00:00","end":"00:01"}]}`
		schedule, err := NormalizeSchedule(w2JSONObject(t, denyAlways))
		if err != nil {
			t.Fatalf("合法时间表应通过：%v", err)
		}
		// 周五 12:00 不在周一 00:00-00:01 窗口内 → 拒绝。
		friday := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
		status, override := ScheduleStatus(schedule, friday)
		if !override || status != "disabled" {
			t.Fatalf("deny 时刻应覆写 disabled：%q %v", status, override)
		}
		if _, ok := NextScheduleCheckAt(schedule, friday); !ok {
			t.Fatal("deny 时刻也应给出下一次检查时间")
		}
	})
}

// w2JSONObject 把 JSON 文本解析为对象（纯函数测试的输入工具）。
func w2JSONObject(t *testing.T, raw string) map[string]any {
	t.Helper()
	var value any
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		t.Fatalf("测试 JSON 解析失败：%v", err)
	}
	object, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("测试 JSON 不是对象：%T", value)
	}
	return object
}
