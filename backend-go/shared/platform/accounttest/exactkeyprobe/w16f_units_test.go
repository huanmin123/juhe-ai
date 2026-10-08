// 本文件由 jobs internal/accounthealth/w16f_units_test.go 拆分而来（成对关系见 probe.go 包注释）：
// 承载随探针执行器闭包下沉的被移函数测试；同源文件的留守测试仍在 jobs 包内，
// 两侧不重复、不丢失。
package exactkeyprobe

import (
	"context"
	"net"
	"net/url"
	"strings"
	"testing"
	"time"
)

// w16fTimeoutNetError 是实现 net.Error 的固定超时错误。
type w16fTimeoutNetError struct{}

func (w16fTimeoutNetError) Error() string   { return "w16f 网络超时" }
func (w16fTimeoutNetError) Timeout() bool   { return true }
func (w16fTimeoutNetError) Temporary() bool { return true }

// TestW16fValidateInputOAuthArms 覆盖 validateInput 的 OAuth 到期与
// Code Assist 缺 project_id 臂。
func TestW16fValidateInputOAuthArms(t *testing.T) {
	// OAuth access token 已到期。
	expired := testInput("https://api.example.com", "responses_json")
	expired.Type = "oauth"
	expired.OAuthExpiresAt = ptrTime(time.Now().UTC().Add(-time.Minute))
	if err := validateInput(expired, ProbeOptions{}); err == nil || !strings.Contains(err.Error(), "OAuth access token 已到期") {
		t.Fatalf("OAuth 到期必须报错: %v", err)
	}
	// google_oauth + code_assist 缺 project_id。
	assist := testInput("https://example.com", "generate_content_json")
	assist.ProtocolProfileID = "profile_gemini_native_v1beta"
	assist.Provider = "gemini"
	assist.ProtocolVersion = "v1beta"
	assist.Type = "google_oauth"
	assist.OAuthType = "code_assist"
	assist.OAuthExpiresAt = ptrTime(time.Now().UTC().Add(time.Hour))
	if err := validateInput(assist, ProbeOptions{}); err == nil || !strings.Contains(err.Error(), "Code Assist") {
		t.Fatalf("code_assist 缺 project_id 必须报错: %v", err)
	}
}

// TestW16fBuildProbeRequestArms 覆盖 buildProbeRequest 的未知 endpoint mode
// 与 codeAssist 请求包装臂。
func TestW16fBuildProbeRequestArms(t *testing.T) {
	base, err := url.Parse("https://api.example.com")
	if err != nil {
		t.Fatal(err)
	}
	// 未知 endpoint mode → 请求构造失败臂。
	unknown := testInput("https://api.example.com", "chat_json")
	unknown.EndpointMode = "w16f-unknown"
	if _, err := buildProbeRequest(context.Background(), base, unknown, "token"); err == nil || !strings.Contains(err.Error(), "未冻结的 endpoint mode") {
		t.Fatalf("未知 mode 必须报错: %v", err)
	}
	// codeAssist 包装：gemini 协议 + google_oauth code_assist。
	assist := testInput("https://cloudcode-pa.googleapis.com", "generate_content_json")
	assist.ProtocolProfileID = "profile_gemini_native_v1beta"
	assist.Provider = "gemini"
	assist.Type = "google_oauth"
	assist.OAuthType = "code_assist"
	assist.OAuthProjectID = "w16f-project"
	request, err := buildProbeRequest(context.Background(), base, assist, "token")
	if err != nil {
		t.Fatalf("codeAssist 请求必须构造成功: %v", err)
	}
	if !strings.Contains(request.URL.Path, "v1internal") {
		t.Fatalf("codeAssist 必须改写路径: %s", request.URL.Path)
	}
}

// TestW16fProbeTransportBadProxy 覆盖 probeTransport 的代理凭据解封失败臂。
func TestW16fProbeTransportBadProxy(t *testing.T) {
	input := testInput("https://api.example.com", "chat_json")
	input.Proxy = &CredentialEnvelope{Kind: "proxy_url", Ciphertext: "w16f-not-an-envelope"}
	if _, err := probeTransport(input, ProbeOptions{Secret: "w16f-secret"}); err == nil || !strings.Contains(err.Error(), "代理凭据不可用") {
		t.Fatalf("坏代理 envelope 必须报错: %v", err)
	}
}

// TestW16fVerifyResponseArms 覆盖 verifyResponse 的 profile images、default
// 与 images data 非法/url 臂。
func TestW16fVerifyResponseArms(t *testing.T) {
	// profile 非空 + images_json。
	if err := verifyResponse(Input{ProtocolProfileID: "profile_openai_openai_v1", EndpointMode: "images_json"}, []byte(`{"data":[{"b64_json":"dzE2Zg=="}]}`)); err != nil {
		t.Fatalf("profile images 必须通过: %v", err)
	}
	// profile 非空 + 未知 mode + 含挑战 → default 成功臂。
	if err := verifyResponse(Input{ProtocolProfileID: "profile_openai_openai_v1", EndpointMode: "w16f-custom"}, []byte(`{"note":"JUHE"}`)); err != nil {
		t.Fatalf("default 含挑战必须通过: %v", err)
	}
	// default 无挑战 → 失败臂。
	if err := verifyResponse(Input{ProtocolProfileID: "profile_openai_openai_v1", EndpointMode: "w16f-custom"}, []byte(`{"note":"none"}`)); err == nil {
		t.Fatal("default 无挑战必须报错")
	}
	// images data 元素非对象 → continue 后报缺少有效图片。
	if err := verifyImagesJSON(map[string]any{"data": []any{1, "w16f"}}); err == nil || !strings.Contains(err.Error(), "缺少有效图片") {
		t.Fatalf("非法 data 元素必须报错: %v", err)
	}
	// images url 字段 → 成功臂。
	if err := verifyImagesJSON(map[string]any{"data": []any{map[string]any{"url": "https://img.example/w16f"}}}); err != nil {
		t.Fatalf("url 字段必须通过: %v", err)
	}
}

// TestW16fResponsesSSEArms 覆盖 verifyResponsesSSE 的缺挑战臂与
// consumeResponsesSSEEvent 的 [DONE] / output_text.done 臂。
func TestW16fResponsesSSEArms(t *testing.T) {
	// completed 事件但输出不含挑战。
	body := []byte("event: response.completed\ndata: {\"type\":\"response.completed\"}\n\n")
	if err := verifyResponsesSSE(body); err == nil || !strings.Contains(err.Error(), "未包含挑战值") {
		t.Fatalf("缺挑战必须报错: %v", err)
	}
	// [DONE] 数据行短路臂。
	var output strings.Builder
	completed := false
	if err := consumeResponsesSSEEvent("data: [DONE]", &output, &completed); err != nil || completed {
		t.Fatalf("[DONE] 必须短路: %v completed=%t", err, completed)
	}
	// output_text.done 聚合臂。
	if err := consumeResponsesSSEEvent("data: {\"type\":\"response.output_text.done\",\"text\":\"juhe\"}", &output, &completed); err != nil {
		t.Fatalf("output_text.done 必须聚合: %v", err)
	}
	if !strings.Contains(output.String(), "juhe") {
		t.Fatalf("输出必须包含聚合文本: %q", output.String())
	}
}

// TestW16fFailureClassifiers 覆盖 transportFailure / responseReadFailure 的
// 网络超时臂。
func TestW16fFailureClassifiers(t *testing.T) {
	var networkErr net.Error = w16fTimeoutNetError{}
	timeout := transportFailure(networkErr)
	if timeout.Outcome != OutcomeUpstreamFailed || timeout.ErrorCode != "upstream_timeout" {
		t.Fatalf("transport 超时分类错误: %+v", timeout)
	}
	readTimeout := responseReadFailure(networkErr)
	if readTimeout.ErrorCode != "upstream_timeout" {
		t.Fatalf("read 超时分类错误: %+v", readTimeout)
	}
}

// w16fDirectFixture 构造通过基础校验的 DirectInput。
func w16fDirectFixture(t *testing.T) (DirectInput, time.Time) {
	t.Helper()
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	credentials, err := EncryptV1Envelope("w16f-secret", []byte(`{"api_key":"sk-w16f"}`))
	if err != nil {
		t.Fatal(err)
	}
	input := DirectInput{
		Account: DirectAccount{
			ID: "w16f-acc", ConfigRevision: 1, DispatchRevision: 1,
			Provider: "gpt", ProtocolProfileID: "profile_gpt_openai_v1", ProtocolCode: "openai", ProtocolVersion: "v1",
			Type: "api_key", Status: "active", Schedulable: true,
			EndpointMode: "chat_json", HealthModel: "w16f-model",
			CredentialsEncrypted: credentials,
		},
		Binding:      DirectBinding{GroupID: "w16f-group", Enabled: true},
		InputVersion: 1,
		IssuedAt:     now.Add(-time.Minute),
		ExpiresAt:    now.Add(time.Hour),
		TLSPolicy:    "j1-direct-upstream-v1",
	}
	return input, now
}

func w16fSource(t *testing.T, provider, profile, code string) *DirectSource {
	t.Helper()
	credentials, err := EncryptV1Envelope("w16f-secret", []byte(`{"api_key":"sk-w16f-src"}`))
	if err != nil {
		t.Fatal(err)
	}
	return &DirectSource{
		ID: "w16f-src", ConfigRevision: 1, Provider: provider,
		ProtocolProfileID: profile, ProtocolCode: code, ProtocolVersion: "v1",
		Type: "api_key", Status: "active", Schedulable: true,
		CredentialsEncrypted: credentials,
	}
}

// TestW16fToInputSourceMetadataArms 覆盖 ToInput 的来源协议元数据不一致、
// hybrid 探活目标失败与 hybrid 缺 base_url 臂。
func TestW16fToInputSourceMetadataArms(t *testing.T) {
	direct, now := w16fDirectFixture(t)
	direct.Authorization = &DirectAuthorization{ID: "w16f-auth", Status: "active", QuotaEligible: true}
	direct.Binding.AuthorizationBindingID = "w16f-auth"
	// 来源 protocol_code 与 profile 不一致。
	direct.Source = w16fSource(t, "gpt", "profile_gpt_openai_v1", "anthropic")
	if _, err := direct.ToInput("w16f-secret", now); err == nil || !strings.Contains(err.Error(), "protocol_code") {
		t.Fatalf("来源 protocol_code 不一致必须报错: %v", err)
	}
	// hybrid 来源缺模型映射 → 探活目标解析失败。
	hybridDirect := direct
	hybridDirect.Source = w16fSource(t, "hybrid", "profile_hybrid_openai_chat_v1", "openai")
	if _, err := hybridDirect.ToInput("w16f-secret", now); err == nil {
		t.Fatal("hybrid 缺模型映射必须报错")
	}
	// hybrid 带映射但凭据缺 base_url → base URL 失败臂。
	mappedDirect := direct
	mappedDirect.Account.MappedUpstreamModel = "w16f-upstream-model"
	mappedDirect.Account.MappedUpstreamEndpointFamily = "chat_completions"
	mappedDirect.Source = w16fSource(t, "hybrid", "profile_hybrid_openai_chat_v1", "openai")
	if _, err := mappedDirect.ToInput("w16f-secret", now); err == nil || !strings.Contains(err.Error(), "base_url") {
		t.Fatalf("hybrid 缺 base_url 必须报错: %v", err)
	}
}

// TestW16fToInputChatGPTUserIDFallback 覆盖 OAuth chatgpt_user_id 回退臂。
func TestW16fToInputChatGPTUserIDFallback(t *testing.T) {
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	credentials, err := EncryptV1Envelope("w16f-secret", []byte(`{"access_token":"at-w16f","expires_at":"2027-01-01T00:00:00Z","chatgpt_user_id":"w16f-user"}`))
	if err != nil {
		t.Fatal(err)
	}
	direct := DirectInput{
		Account: DirectAccount{
			ID: "w16f-oauth", ConfigRevision: 1, DispatchRevision: 1,
			Provider: "gpt", ProtocolProfileID: "profile_gpt_openai_v1", ProtocolCode: "openai", ProtocolVersion: "v1",
			Type: "oauth", Status: "active", Schedulable: true,
			EndpointMode: "responses_json", HealthModel: "w16f-model",
			CredentialsEncrypted: credentials,
		},
		Binding:      DirectBinding{GroupID: "w16f-group", Enabled: true},
		InputVersion: 1,
		IssuedAt:     now.Add(-time.Minute),
		ExpiresAt:    now.Add(time.Hour),
		TLSPolicy:    "j1-direct-upstream-v1",
	}
	input, err := direct.ToInput("w16f-secret", now)
	if err != nil {
		t.Fatalf("oauth ToInput 必须成功: %v", err)
	}
	if input.OAuthAccountID != "w16f-user" {
		t.Fatalf("chatgpt_user_id 必须回退为账户 ID: %q", input.OAuthAccountID)
	}
}

// TestW16fDirectProfileAndBaseURLArms 覆盖 isSupportedDirectProfile 的 xai
// provider 不匹配臂与 directBaseURL 的 gemini native 非 code-assist 分支。
func TestW16fDirectProfileAndBaseURLArms(t *testing.T) {
	if isSupportedDirectProfile("profile_xai_openai_v1", "gpt", "api_key", "chat_json") {
		t.Fatal("xai profile 配 gpt provider 必须不支持")
	}
	base, err := directBaseURL(nil, DirectAccount{ProtocolProfileID: "profile_gemini_native_v1beta", Type: "api_key"}, "gemini")
	if err != nil || base != "https://generativelanguage.googleapis.com" {
		t.Fatalf("gemini native 非 code-assist 必须用公共端点: %q %v", base, err)
	}
}

// TestW16fDirectProxyEnvelopeEmptySecret 覆盖 directProxyEnvelope 的空
// secret 加密失败臂。
func TestW16fDirectProxyEnvelopeEmptySecret(t *testing.T) {
	proxy := DirectProxy{ID: "w16f-proxy", Enabled: true, Type: "http", Host: "127.0.0.1", Port: 8080}
	if _, err := directProxyEnvelope("  ", proxy); err == nil || !strings.Contains(err.Error(), "secret 不能为空") {
		t.Fatalf("空 secret 必须报错: %v", err)
	}
}

// ptrTime 是 jobs accounthealth scheduler.go 同名测试助手的成对副本（原实现
// 留守 jobs，跨包不可见，按需复制）。
func ptrTime(value time.Time) *time.Time { return &value }
