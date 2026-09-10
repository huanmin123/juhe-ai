package accounthealth

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"syscall"
	"testing"
	"time"
)

// wgSSEInput 构造指定 endpoint mode 的最小合法探针输入（api_key 形态）。
func wgSSEInput(mode, provider, profile string) Input {
	input := testInput("https://upstream.test", mode)
	input.Provider = provider
	input.ProtocolProfileID = profile
	return input
}

// TestVerifyOpenAIChatSSE 覆盖 Chat Completions SSE 验证的成功、缺 DONE、
// 缺挑战与非法 data 分支。
func TestVerifyOpenAIChatSSE(t *testing.T) {
	good := "data: {\"choices\":[{\"delta\":{\"content\":\"JUHE\"}}]}\n\n" +
		"data: {\"choices\":[{\"finish_reason\":\"stop\",\"message\":{\"content\":\"juhe\"}}]}\n\n" +
		"data: [DONE]\n\n"
	if err := verifyOpenAIChatSSE([]byte(good)); err != nil {
		t.Fatalf("完整 Chat SSE 必须通过: %v", err)
	}
	if err := verifyOpenAIChatSSE([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"juhe\"}}]}\n\n")); err == nil {
		t.Fatal("缺 [DONE] 必须失败")
	}
	noChallenge := "data: {\"choices\":[{\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
	if err := verifyOpenAIChatSSE([]byte(noChallenge)); err == nil {
		t.Fatal("缺挑战值必须失败")
	}
	if err := verifyOpenAIChatSSE([]byte("data: not-json\n\n")); err == nil {
		t.Fatal("非法 data 必须报错")
	}
	// CRLF 归一。
	if err := verifyOpenAIChatSSE([]byte(strings.ReplaceAll(good, "\n\n", "\r\n\r\n"))); err != nil {
		t.Fatalf("CRLF 流必须通过: %v", err)
	}
}

// TestVerifyAnthropicSSE 覆盖 Messages SSE 的成功、缺 message_stop、
// 非 JSON data 与事件类型内嵌分支。
func TestVerifyAnthropicSSE(t *testing.T) {
	good := "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"text\":\"juhe\"}}\n\n" +
		"data: {\"type\":\"message_stop\"}\n\n"
	if err := verifyAnthropicSSE([]byte(good)); err != nil {
		t.Fatalf("完整 Messages SSE 必须通过: %v", err)
	}
	if err := verifyAnthropicSSE([]byte("data: {\"type\":\"content_block_delta\",\"delta\":{\"text\":\"juhe\"}}\n\n")); err == nil {
		t.Fatal("缺 message_stop 必须失败")
	}
	if err := verifyAnthropicSSE([]byte("data: not-json\n\n")); err == nil {
		t.Fatal("非法 data 必须报错")
	}
	if err := verifyAnthropicSSE([]byte("event: ping\n\n")); err == nil {
		t.Fatal("无 data 块不得视为完成")
	}
}

// TestVerifyGeminiSSE 覆盖 Gemini SSE 的直接/嵌套 response、[DONE] 与
// 非法 data 分支。
func TestVerifyGeminiSSE(t *testing.T) {
	good := "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"juhe\"}]},\"finishReason\":\"STOP\"}]}\n\n"
	if err := verifyGeminiSSE([]byte(good)); err != nil {
		t.Fatalf("完整 Gemini SSE 必须通过: %v", err)
	}
	nested := "data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"JUHE\"}]},\"finishReason\":\"STOP\"}]}}\n\ndata: [DONE]\n\n"
	if err := verifyGeminiSSE([]byte(nested)); err != nil {
		t.Fatalf("嵌套 response 流必须通过: %v", err)
	}
	if err := verifyGeminiSSE([]byte("data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"juhe\"}]}}]}\n\n")); err == nil {
		t.Fatal("缺 finishReason 必须失败")
	}
	if err := verifyGeminiSSE([]byte("data: not-json\n\n")); err == nil {
		t.Fatal("非法 data 必须报错")
	}
}

// TestVerifyInteractionsSSE 覆盖 Interactions SSE 的三种完成信号分支。
func TestVerifyInteractionsSSE(t *testing.T) {
	direct := "data: {\"status\":\"completed\",\"output_text\":\"juhe\"}\n\n"
	if err := verifyInteractionsSSE([]byte(direct)); err != nil {
		t.Fatalf("status=completed 必须通过: %v", err)
	}
	typed := "data: {\"type\":\"interaction.completed\"}\n\ndata: {\"status\":\"completed\"}\n\n"
	if err := verifyInteractionsSSE([]byte(typed)); err == nil {
		t.Fatal("completed 事件缺挑战值必须失败")
	}
	nested := "data: {\"interaction\":{\"status\":\"completed\"}}\n\ndata: {\"interaction\":{\"output\":\"juhe\"}}\n\ndata: [DONE]\n\n"
	if err := verifyInteractionsSSE([]byte(nested)); err != nil {
		t.Fatalf("嵌套 interaction 完成必须通过: %v", err)
	}
	if err := verifyInteractionsSSE([]byte("data: not-json\n\n")); err == nil {
		t.Fatal("非法 data 必须报错")
	}
	if err := verifyInteractionsSSE([]byte("data: {\"status\":\"in_progress\"}\n\n")); err == nil {
		t.Fatal("未完成流必须失败")
	}
}

// TestVerifyResponsesSSEFailures 覆盖 Responses SSE 的失败事件、非法 data
// 与空流分支。
func TestVerifyResponsesSSEFailures(t *testing.T) {
	if err := verifyResponsesSSE([]byte("event: response.failed\ndata: {\"type\":\"response.failed\"}\n\n")); err == nil {
		t.Fatal("response.failed 必须报错")
	}
	if err := verifyResponsesSSE([]byte("data: not-json\n\n")); err == nil {
		t.Fatal("非法 data 必须报错")
	}
	if err := verifyResponsesSSE([]byte(": comment only\n\n")); err == nil {
		t.Fatal("无 data 流必须失败")
	}
}

// TestVerifyResponseJSONDispatch 表驱动覆盖 verifyResponse 的全部 JSON
// endpoint 分支（挑战值与完成语义）。
func TestVerifyResponseJSONDispatch(t *testing.T) {
	cases := []struct {
		name    string
		input   Input
		body    string
		wantErr bool
	}{
		{"chat_json 完成", wgSSEInput("chat_json", "openai", "profile_openai_openai_v1"),
			`{"choices":[{"finish_reason":"stop","message":{"content":"juhe"}}]}`, false},
		{"chat_json 未完成", wgSSEInput("chat_json", "openai", "profile_openai_openai_v1"),
			`{"choices":[{"delta":{"content":"juhe"}}]}`, true},
		{"responses_json 完成", wgSSEInput("responses_json", "openai", "profile_openai_openai_v1"),
			`{"status":"completed","object":"response","output":[{"content":"juhe"}]}`, false},
		{"responses_json 未完成", wgSSEInput("responses_json", "openai", "profile_openai_openai_v1"),
			`{"status":"in_progress","output":[{"content":"juhe"}]}`, true},
		{"messages_json 完成", wgSSEInput("messages_json", "anthropic", "profile_anthropic_anthropic_v1"),
			`{"type":"message","stop_reason":"end_turn","content":[{"text":"juhe"}]}`, false},
		{"messages_json 缺 stop_reason", wgSSEInput("messages_json", "anthropic", "profile_anthropic_anthropic_v1"),
			`{"type":"message","content":[{"text":"juhe"}]}`, true},
		{"generate_content_json 完成", wgSSEInput("generate_content_json", "gemini", "profile_gemini_native_v1beta"),
			`{"candidates":[{"finishReason":"STOP","content":{"parts":[{"text":"juhe"}]}}]}`, false},
		{"generate_content_json 未完成", wgSSEInput("generate_content_json", "gemini", "profile_gemini_native_v1beta"),
			`{"candidates":[{"content":{"parts":[{"text":"juhe"}]}}]}`, true},
		{"interactions_json 完成", wgSSEInput("interactions_json", "gemini", "profile_gemini_native_v1beta"),
			`{"status":"completed","output":"juhe"}`, false},
		{"images_json 有效图", wgSSEInput("images_json", "openai", ""),
			`{"data":[{"b64_json":"AAAA"}]}`, false},
		{"images_json 缺图", wgSSEInput("images_json", "openai", ""),
			`{"data":[{}]}`, true},
		{"images_json 缺 data", wgSSEInput("images_json", "openai", ""),
			`{}`, true},
		{"无 profile 默认分支含挑战", wgSSEInput("chat_json", "openai", ""),
			`{"anything":"juhe"}`, false},
		{"无 profile 默认分支缺挑战", wgSSEInput("chat_json", "openai", ""),
			`{"anything":"nope"}`, true},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			err := verifyResponse(item.input, []byte(item.body))
			if (err != nil) != item.wantErr {
				t.Fatalf("wantErr=%v, err=%v", item.wantErr, err)
			}
		})
	}
	// 非 JSON 响应。
	if err := verifyResponse(wgSSEInput("chat_json", "openai", "profile_openai_openai_v1"), []byte("<html>")); err == nil {
		t.Fatal("非 JSON 响应必须报错")
	}
	// codeAssist 分支（gemini oauth → verifyGeminiSSE）。
	codeAssist := wgSSEInput("generate_content_json", "gemini", "profile_gemini_native_v1beta")
	codeAssist.Type = "google_oauth"
	codeAssist.OAuthType = "code_assist"
	if err := verifyResponse(codeAssist, []byte("data: {\"candidates\":[{\"finishReason\":\"STOP\",\"content\":{\"parts\":[{\"text\":\"juhe\"}]}}]}\n\n")); err != nil {
		t.Fatalf("codeAssist 分支必须走 Gemini SSE 验证: %v", err)
	}
}

// TestChatCompletedAndGeminiCandidateHelpers 覆盖完成判定助手的边界。
func TestChatCompletedAndGeminiCandidateHelpers(t *testing.T) {
	if chatCompleted(map[string]any{}) {
		t.Fatal("无 choices 不得判完成")
	}
	if chatCompleted(map[string]any{"choices": "not-a-list"}) {
		t.Fatal("非法 choices 不得判完成")
	}
	if !chatCompleted(map[string]any{"choices": []any{map[string]any{"finish_reason": "stop"}}}) {
		t.Fatal("finish_reason 存在必须判完成")
	}
	if geminiCandidateComplete(map[string]any{}) {
		t.Fatal("无 candidates 不得判完成")
	}
	if !geminiCandidateComplete(map[string]any{"candidates": []any{map[string]any{"finishReason": "STOP"}}}) {
		t.Fatal("finishReason 存在必须判完成")
	}
}

// TestProbeExactKeyModelRoutesToSelectedKey 覆盖 Key-model 精准探测的路由、
// 参数缺失与非 API Key 分支。
func TestProbeExactKeyModelRoutesToSelectedKey(t *testing.T) {
	secret := "exact-key-secret"
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"content":"juhe"}}]}`))
	}))
	defer upstream.Close()
	input := testInput(upstream.URL, "chat_json")
	input.APIKeys = []APIKeyInput{
		{Index: 0, Fingerprint: "fp-a", Credential: CredentialEnvelope{Kind: "api_key", Ciphertext: testEnvelope(t, secret, `{"api_key":"sk-a"}`)}},
		{Index: 1, Fingerprint: "fp-b", Credential: CredentialEnvelope{Kind: "api_key", Ciphertext: testEnvelope(t, secret, `{"api_key":"sk-b"}`)}},
	}
	result := ProbeExactKeyModel(context.Background(), input, " fp-b ", "gpt-test", "chat_json", ProbeOptions{Secret: secret, Timeout: 2 * time.Second})
	if result.Outcome != OutcomeSuccess {
		t.Fatalf("指定 Key 探测必须成功: %#v", result)
	}
	// 指纹不存在 → 显式任务失败。
	result = ProbeExactKeyModel(context.Background(), input, "fp-zzz", "gpt-test", "chat_json", ProbeOptions{Secret: secret})
	if result.ErrorCode != "key_model_key_unavailable" {
		t.Fatalf("缺失指纹必须报 key_model_key_unavailable: %#v", result)
	}
	// 缺路由 → 任务失败。
	result = ProbeExactKeyModel(context.Background(), input, "fp-a", "  ", "chat_json", ProbeOptions{Secret: secret})
	if result.ErrorCode != "key_model_route_invalid" {
		t.Fatalf("缺模型必须报 key_model_route_invalid: %#v", result)
	}
	// 非 API Key 凭据 → 不支持。
	oauthInput := input
	oauthInput.Type = "oauth"
	result = ProbeExactKeyModel(context.Background(), oauthInput, "fp-a", "gpt-test", "responses_json", ProbeOptions{Secret: secret})
	if result.ErrorCode != "key_model_credential_type_unsupported" {
		t.Fatalf("非 API Key 必须报不支持: %#v", result)
	}
}

// TestTransportFailureClassification 表驱动覆盖传输失败分类。
func TestTransportFailureClassification(t *testing.T) {
	cases := []struct {
		name string
		err  error
		code string
	}{
		{"取消", context.Canceled, "probe_cancelled"},
		{"超时", context.DeadlineExceeded, "upstream_timeout"},
		{"连接拒绝", &url.Error{Op: "Post", URL: "https://x", Err: errWrap{syscallLike: "refused"}}, "upstream_connection_refused"},
		{"重置", &url.Error{Op: "Post", URL: "https://x", Err: errWrap{syscallLike: "reset"}}, "upstream_connection_reset"},
		{"提前关闭", &url.Error{Op: "Post", URL: "https://x", Err: io.ErrUnexpectedEOF}, "upstream_connection_closed"},
		{"DNS", &url.Error{Op: "Post", URL: "https://x", Err: &net.DNSError{Err: "no such host", Name: "x.test", IsNotFound: true}}, "upstream_dns"},
		{"通用连接", &url.Error{Op: "Post", URL: "https://x", Err: errors.New("boom")}, "upstream_connection"},
		{"本地失败", errors.New("plain"), "probe_local_failure"},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			result := transportFailure(item.err)
			if result.ErrorCode != item.code {
				t.Fatalf("code=%s 期望 %s", result.ErrorCode, item.code)
			}
		})
	}
}

// errWrap 把 syscall 错误包装成可被 errors.Is 命中的形态。
type errWrap struct{ syscallLike string }

func (e errWrap) Error() string { return e.syscallLike }
func (e errWrap) Is(target error) bool {
	if target == syscall.ECONNREFUSED {
		return e.syscallLike == "refused"
	}
	if target == syscall.ECONNRESET {
		return e.syscallLike == "reset"
	}
	return false
}

// TestResponseReadFailureClassification 覆盖响应读取失败分类。
func TestResponseReadFailureClassification(t *testing.T) {
	if result := responseReadFailure(context.Canceled); result.ErrorCode != "probe_cancelled" {
		t.Fatalf("取消分类错误: %#v", result)
	}
	if result := responseReadFailure(context.DeadlineExceeded); result.ErrorCode != "upstream_timeout" {
		t.Fatalf("超时分类错误: %#v", result)
	}
	if result := responseReadFailure(errors.New("partial")); result.ErrorCode != "upstream_read_incomplete" {
		t.Fatalf("默认分类错误: %#v", result)
	}
}

// TestProbeTransportConfigMatrix 覆盖代理配置解析：无代理、坏封套、坏 URL、
// 不支持的协议与合法代理。
func TestProbeTransportConfigMatrix(t *testing.T) {
	secret := "proxy-matrix-secret"
	input := testInput("https://upstream.test", "chat_json")
	// 无代理。
	if _, timeout, err := probeTransportConfig(input, ProbeOptions{Timeout: 3 * time.Second}); err != nil || timeout != 3*time.Second {
		t.Fatalf("无代理必须直连: %v %v", timeout, err)
	}
	// 代理封套解密失败。
	badEnvelope := input
	badEnvelope.Proxy = &CredentialEnvelope{Kind: "proxy_url", Ciphertext: "v1:bad"}
	if _, _, err := probeTransportConfig(badEnvelope, ProbeOptions{Secret: secret}); err == nil {
		t.Fatal("坏代理封套必须报错")
	}
	// 不支持的协议。
	unsupported := input
	unsupported.Proxy = &CredentialEnvelope{Kind: "proxy_url", Ciphertext: mustEnvelopeJSON(t, secret, `{"url":"gopher://127.0.0.1:1080"}`)}
	if _, _, err := probeTransportConfig(unsupported, ProbeOptions{Secret: secret}); err == nil || !strings.Contains(err.Error(), "未支持的代理协议") {
		t.Fatalf("gopher 协议必须报未支持: %v", err)
	}
	// 非法 URL。
	invalid := input
	invalid.Proxy = &CredentialEnvelope{Kind: "proxy_url", Ciphertext: mustEnvelopeJSON(t, secret, `{"url":"ht tp://bad url"}`)}
	if _, _, err := probeTransportConfig(invalid, ProbeOptions{Secret: secret}); err == nil {
		t.Fatal("非法代理 URL 必须报错")
	}
	// 合法 socks5h。
	valid := input
	valid.Proxy = &CredentialEnvelope{Kind: "proxy_url", Ciphertext: mustEnvelopeJSON(t, secret, `{"url":"socks5h://127.0.0.1:1080"}`)}
	proxyText, _, err := probeTransportConfig(valid, ProbeOptions{Secret: secret})
	if err != nil || proxyText != "socks5h://127.0.0.1:1080" {
		t.Fatalf("合法代理必须透传: %q %v", proxyText, err)
	}
	if _, err := probeHTTPClient(valid, ProbeOptions{Secret: secret}); err != nil {
		t.Fatalf("共享客户端构造: %v", err)
	}
	if _, err := probeTransport(valid, ProbeOptions{Secret: secret}); err != nil {
		t.Fatalf("传输构造: %v", err)
	}
}

// TestDecryptTokenFieldExtraction 覆盖凭据 token 解封的字段提取优先级。
func TestDecryptTokenFieldExtraction(t *testing.T) {
	secret := "token-secret"
	if _, err := decryptToken("  ", CredentialEnvelope{Ciphertext: "x"}); err == nil {
		t.Fatal("空 secret 必须报错")
	}
	if _, err := decryptToken(secret, CredentialEnvelope{}); err == nil {
		t.Fatal("空 ciphertext 必须报错")
	}
	if _, err := decryptToken(secret, CredentialEnvelope{Ciphertext: "v1:bad"}); err == nil {
		t.Fatal("坏 envelope 必须报错")
	}
	if _, err := decryptToken(secret, CredentialEnvelope{Ciphertext: mustEnvelopeJSON(t, secret, `   `)}); err == nil {
		t.Fatal("空明文必须报错")
	}
	token, err := decryptToken(secret, CredentialEnvelope{Ciphertext: mustEnvelopeJSON(t, secret, `{"access_token":"tok-1"}`)})
	if err != nil || token != "tok-1" {
		t.Fatalf("access_token 字段必须命中: %q %v", token, err)
	}
	token, err = decryptToken(secret, CredentialEnvelope{Ciphertext: mustEnvelopeJSON(t, secret, `{"token":"tok-2"}`)})
	if err != nil || token != "tok-2" {
		t.Fatalf("token 字段必须命中: %q %v", token, err)
	}
	token, err = decryptToken(secret, CredentialEnvelope{Ciphertext: mustEnvelopeJSON(t, secret, `{"url":"socks5h://x"}`)})
	if err != nil || token != "socks5h://x" {
		t.Fatalf("url 字段必须命中: %q %v", token, err)
	}
	token, err = decryptToken(secret, CredentialEnvelope{Ciphertext: mustEnvelopeJSON(t, secret, `plain-token`)})
	if err != nil || token != "plain-token" {
		t.Fatalf("纯文本明文必须原样返回: %q %v", token, err)
	}
}

// TestParseBaseURLMatrix 覆盖 base URL 校验的全部拒绝分支。
func TestParseBaseURLMatrix(t *testing.T) {
	for _, bad := range []string{"", "not a url", "/relative", "https://", "https://u:p@host", "https://host/path?q=1", "https://host#frag", "ftp://host", "http://host"} {
		if _, err := parseBaseURL(bad, false); err == nil {
			t.Errorf("parseBaseURL(%q) 必须报错", bad)
		}
	}
	if _, err := parseBaseURL("http://host", true); err != nil {
		t.Fatalf("allowInsecure 必须放行 http: %v", err)
	}
	if _, err := parseBaseURL("  https://host  ", false); err != nil {
		t.Fatalf("TrimSpace 后必须放行: %v", err)
	}
}

// TestJoinBaseURLRules 覆盖 /v1 剥离、/v1beta 对齐与 query 保留。
func TestJoinBaseURLRules(t *testing.T) {
	base, _ := url.Parse("https://api.test/v1")
	if got := joinBaseURL(base, "/v1/chat/completions").String(); got != "https://api.test/v1/chat/completions" {
		t.Fatalf("/v1 根必须保留请求版本: %s", got)
	}
	nested, _ := url.Parse("https://api.test/openai/v1")
	if got := joinBaseURL(nested, "/chat/completions").String(); got != "https://api.test/openai/chat/completions" {
		t.Fatalf("嵌套 /v1 必须剥离: %s", got)
	}
	geminiBase, _ := url.Parse("https://generativelanguage.test/v1beta")
	if got := joinBaseURL(geminiBase, "/v1beta/models/m:generateContent").String(); got != "https://generativelanguage.test/v1beta/models/m:generateContent" {
		t.Fatalf("base 以 /v1beta 结尾时必须对齐请求版本: %s", got)
	}
	withQuery, _ := url.Parse("https://api.test")
	if got := joinBaseURL(withQuery, "/v1beta/models/m:streamGenerateContent?alt=sse").String(); got != "https://api.test/v1beta/models/m:streamGenerateContent?alt=sse" {
		t.Fatalf("query 必须保留: %s", got)
	}
}

// TestGLMAndGeminiBaseOwnership 覆盖 GLM/Gemini base 判定助手。
func TestGLMAndGeminiBaseOwnership(t *testing.T) {
	if glmOpenAIBaseOwnsV1Path(nil) {
		t.Fatal("nil base 不得判定 owns")
	}
	glmRoot, _ := url.Parse("https://open.bigmodel.cn/api/paas/v4")
	if glmOpenAIBaseOwnsV1Path(glmRoot) {
		t.Fatal("GLM 官方根不含 /v1，请求侧必须保留 /v1 前缀")
	}
	compatible, _ := url.Parse("https://proxy.test/v1")
	if !glmOpenAIBaseOwnsV1Path(compatible) {
		t.Fatal("OpenAI 兼容根必须判定 owns")
	}
	empty, _ := url.Parse("https://proxy.test")
	if !glmOpenAIBaseOwnsV1Path(empty) {
		t.Fatal("空路径视同 OpenAI 服务根")
	}
	if geminiOpenAIBaseOwnsPath(nil) {
		t.Fatal("nil base 不得判定 owns")
	}
	geminiOwned, _ := url.Parse("https://g.test/V1Beta/OpenAI")
	if !geminiOpenAIBaseOwnsPath(geminiOwned) {
		t.Fatal("大小写不敏感的 /v1beta/openai 必须判定 owns")
	}
	partial, _ := url.Parse("https://g.test/v1beta")
	if geminiOpenAIBaseOwnsPath(partial) {
		t.Fatal("不完整服务路径不得判定 owns")
	}
}

// TestProbeOpenAIFailureLadder 覆盖 ProbeOpenAI 的前置失败阶梯与响应分支。
func TestProbeOpenAIFailureLadder(t *testing.T) {
	secret := "ladder-secret"
	envelope := CredentialEnvelope{Kind: "api_key", Ciphertext: testEnvelope(t, secret, `{"api_key":"sk-x"}`)}
	// 非法输入。
	if result := ProbeOpenAI(context.Background(), Input{}, envelope, ProbeOptions{Secret: secret}); result.ErrorCode != "invalid_input" {
		t.Fatalf("非法输入必须报 invalid_input: %#v", result)
	}
	valid := testInput("https://upstream.test", "chat_json")
	// 凭据不可用。
	if result := ProbeOpenAI(context.Background(), valid, CredentialEnvelope{Ciphertext: "v1:bad"}, ProbeOptions{Secret: secret}); result.ErrorCode != "credential_unavailable" {
		t.Fatalf("坏凭据必须报 credential_unavailable: %#v", result)
	}
	// base URL 非法。
	badBase := valid
	badBase.BaseURL = "not-a-url"
	if result := ProbeOpenAI(context.Background(), badBase, envelope, ProbeOptions{Secret: secret}); result.ErrorCode != "base_url_invalid" {
		t.Fatalf("坏 base URL 必须报 base_url_invalid: %#v", result)
	}
	// 代理不可用。
	proxyInput := valid
	proxyInput.Proxy = &CredentialEnvelope{Ciphertext: "v1:bad"}
	if result := ProbeOpenAI(context.Background(), proxyInput, envelope, ProbeOptions{Secret: secret}); result.ErrorCode != "proxy_unavailable" {
		t.Fatalf("坏代理必须报 proxy_unavailable: %#v", result)
	}
	// 上游 5xx → neutral。
	errorServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Error(writer, "boom", http.StatusInternalServerError)
	}))
	defer errorServer.Close()
	errorInput := testInput(errorServer.URL, "chat_json")
	if result := ProbeOpenAI(context.Background(), errorInput, envelope, ProbeOptions{Secret: secret}); result.Outcome != OutcomeNeutral || result.ErrorCode != "upstream_http_status" {
		t.Fatalf("5xx 必须 neutral: %#v", result)
	}
	// 协议不满足 → neutral。
	badProtocol := testInput(errorServer.URL, "chat_json")
	badProtocolServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write([]byte(`{"nope":true}`))
	}))
	defer badProtocolServer.Close()
	badProtocol.BaseURL = badProtocolServer.URL
	if result := ProbeOpenAI(context.Background(), badProtocol, envelope, ProbeOptions{Secret: secret}); result.Outcome != OutcomeNeutral || result.ErrorCode != "upstream_protocol_invalid" {
		t.Fatalf("协议不满足必须 neutral: %#v", result)
	}
	// 连接拒绝 → 上游失败族（dial guard 可能改写错误包装，分类落在
	// upstream_connection / upstream_connection_refused 均为上游连接失败语义）。
	refused := testInput("http://127.0.0.1:1", "chat_json")
	refused.AllowInsecureBaseURL = true
	result := ProbeOpenAI(context.Background(), refused, envelope, ProbeOptions{Secret: secret})
	if result.Outcome != OutcomeUpstreamFailed ||
		(result.ErrorCode != "upstream_connection_refused" && result.ErrorCode != "upstream_connection") {
		t.Fatalf("连接拒绝必须归类上游连接失败: %#v", result)
	}
	// 过期输入。
	expired := testInput("https://upstream.test", "chat_json")
	expired.ExpiresAt = time.Now().Add(-time.Hour)
	if result := ProbeOpenAI(context.Background(), expired, envelope, ProbeOptions{Secret: secret}); result.ErrorCode != "invalid_input" {
		t.Fatalf("过期输入必须 invalid_input: %#v", result)
	}
}
