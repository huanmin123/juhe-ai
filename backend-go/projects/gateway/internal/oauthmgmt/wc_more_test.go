// oauthmgmt 长尾补测：生产 HTTP exchanger（httptest 明文端点）、各 provider
// 的会话缺失/属主不符/空刷新令牌分支、anthropic/gemini/grok 的
// reauthorize-from-refresh 流程、tier 归一矩阵与 grok 回调解析。
package oauthmgmt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// TestWCHTTPExchanger：生产 exchanger 的真实 HTTP 链路（本地 httptest）。
func TestWCHTTPExchanger(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/reject" || r.Header.Get("content-type") != "application/x-www-form-urlencoded" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"bad_request"}`))
			return
		}
		_, _ = w.Write([]byte(`{"access_token":"real","expires_in":60}`))
	}))
	defer server.Close()

	exchanger := NewHTTPTokenExchanger()
	response, err := exchanger.Do(context.Background(), formRequest(server.URL, map[string]string{"a": "b"}))
	if err != nil || response.StatusCode != 200 || !strings.Contains(response.Body, "real") {
		t.Fatalf("正常请求: %+v %v", response, err)
	}
	// 非 2xx 响应体原样透出。
	if response, err = exchanger.Do(context.Background(), formRequest(server.URL+"/reject", map[string]string{})); err != nil || response.StatusCode != 400 {
		t.Fatalf("非 2xx: %+v %v", response, err)
	}
	// 错误目标 URL → 传输错误。
	if _, err = exchanger.Do(context.Background(), TokenHTTPRequest{URL: "http://127.0.0.1:1/x", Body: "a=b"}); err == nil {
		t.Fatalf("不可达目标必须报错")
	}
}

// TestWCSmallZeroFunctions：零散 Error()/构造器分支。
func TestWCSmallZeroFunctions(t *testing.T) {
	if got := (&grokOAuthError{Message: "m", StatusCode: 400}).Error(); got != "m" {
		t.Fatalf("grokOAuthError.Error: %q", got)
	}
	called := false
	requester := SSODeviceRequesterFunc(func(context.Context, SSODeviceRequest) (SSODeviceResponse, error) {
		called = true
		return SSODeviceResponse{StatusCode: 200}, nil
	})
	if _, err := requester.Do(context.Background(), SSODeviceRequest{}); err != nil || !called {
		t.Fatalf("SSODeviceRequesterFunc.Do: %v", err)
	}
	if defaultSSODeviceRequester() == nil {
		t.Fatalf("默认传输必须可构造")
	}
	if newSessionStore(nil) == nil {
		t.Fatalf("nil 时钟回退可构造")
	}
	for provider, label := range map[string]string{
		ProviderGPT: "OpenAI", ProviderAnthropic: "Anthropic",
		ProviderGemini: "Gemini", ProviderXAI: "Grok", "other": "other",
	} {
		if got := oauthLabelForProvider(provider); got != label {
			t.Fatalf("oauthLabelForProvider(%s) = %q", provider, got)
		}
	}
}

// TestWCGeminiAccountOAuthType：凭据推断 oauth 类型矩阵。
func TestWCGeminiAccountOAuthType(t *testing.T) {
	cases := []struct {
		name        string
		credentials map[string]any
		want        string
	}{
		{"显式 ai_studio", map[string]any{"oauth_type": "ai_studio"}, "ai_studio"},
		{"显式未知回退", map[string]any{"oauth_type": "bogus"}, "code_assist"},
		{"AI Studio 域名", map[string]any{"base_url": "https://generativelanguage.googleapis.com"}, "ai_studio"},
		{"project_id 推断", map[string]any{"project_id": "p"}, "code_assist"},
		{"CLI 域名推断", map[string]any{"base_url": "https://cloudcode-pa.googleapis.com"}, "code_assist"},
		{"第三方 client 推断", map[string]any{"client_id": "custom"}, "ai_studio"},
		{"全缺省", map[string]any{}, "code_assist"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := geminiAccountOAuthType(testCase.credentials); got != testCase.want {
				t.Fatalf("得到 %q，期望 %q", got, testCase.want)
			}
		})
	}
}

// TestWCCanonicalGeminiTierID：tier 归一全矩阵。
func TestWCTestCanonicalGeminiTierID(t *testing.T) {
	cases := []struct {
		oauthType string
		raw       string
		want      string
	}{
		{"google_one", "AI-Premium", "google_ai_pro"},
		{"google_one", "google-ai-ultra", "google_ai_ultra"},
		{"google_one", "google_one_unknown", "google_one_unknown"},
		{"google_one", "free", "google_one_free"},
		{"google_one", "bogus", ""},
		{"ai_studio", "aistudio-paid", "aistudio_paid"},
		{"ai_studio", "FREE", "aistudio_free"},
		{"ai_studio", "bogus", ""},
		{"code_assist", "gcp-enterprise", "gcp_enterprise"},
		{"code_assist", "PRO", "gcp_standard"},
		{"code_assist", "bogus", ""},
		{"", "", ""},
	}
	for _, testCase := range cases {
		t.Run(testCase.oauthType+"/"+testCase.raw, func(t *testing.T) {
			if got := canonicalGeminiTierID(testCase.oauthType, testCase.raw); got != testCase.want {
				t.Fatalf("得到 %q，期望 %q", got, testCase.want)
			}
		})
	}
}

// TestWCExtractGeminiCodeAndState：gemini 回调解析全分支。
func TestWCExtractGeminiCodeAndState(t *testing.T) {
	if _, _, err := extractGeminiCodeAndState(""); err == nil || err.Error() != "Gemini 授权结果不能为空" {
		t.Fatalf("空回调: %v", err)
	}
	if _, _, err := extractGeminiCodeAndState("https://cb?error=denied&error_description=x"); err == nil {
		t.Fatalf("error 转发必须报错")
	}
	code, state, err := extractGeminiCodeAndState("https://cb?code=c1&state=s1")
	if err != nil || code != "c1" || state != "s1" {
		t.Fatalf("URL 形式: %q %q %v", code, state, err)
	}
	code, state, err = extractGeminiCodeAndState("c2#s2")
	if err != nil || code != "c2" || state != "s2" {
		t.Fatalf("code#state 形式: %q %q %v", code, state, err)
	}
	code, state, err = extractGeminiCodeAndState("code=c3&state=s3")
	if err != nil || code != "c3" || state != "s3" {
		t.Fatalf("query 形式: %q %q %v", code, state, err)
	}
	if _, _, err := extractGeminiCodeAndState("https://cb?code=c4"); err == nil || err.Error() != "Gemini 授权结果必须包含 code 和 state" {
		t.Fatalf("缺 state: %v", err)
	}
	// 会话字段一致性守卫。
	if err := assertGeminiSessionField("Project ID", "a", "b"); err == nil {
		t.Fatalf("字段不一致必须报错")
	}
	if err := assertGeminiSessionField("Project ID", " a ", "a"); err != nil {
		t.Fatalf("一致字段放行: %v", err)
	}
	// body 字段校验。
	if err := validateGeminiBodyFields(map[string]any{"oauthType": "bogus"}); err == nil {
		t.Fatalf("非法 oauthType 必须报错")
	}
	if err := validateGeminiBodyFields(map[string]any{"baseUrl": "not-url"}); err == nil {
		t.Fatalf("非法 baseUrl 必须报错")
	}
	if err := validateGeminiBodyFields(map[string]any{"oauthType": "ai_studio", "baseUrl": "https://ok.example"}); err != nil {
		t.Fatalf("合法字段: %v", err)
	}
}

// TestWCParseGrokAuthorizationInput：grok 回调解析全分支。
func TestWCParseGrokAuthorizationInput(t *testing.T) {
	authorization, err := parseGrokAuthorizationInput("")
	if err != nil || authorization.code != "" {
		t.Fatalf("空输入: %+v %v", authorization, err)
	}
	if _, err := parseGrokAuthorizationInput("https://cb?error=e&error_description=d"); err == nil {
		t.Fatalf("error 转发必须报错")
	}
	authorization, err = parseGrokAuthorizationInput("https://cb?code=c1&state=s1")
	if err != nil || authorization.code != "c1" || authorization.state != "s1" || !authorization.requiresState {
		t.Fatalf("URL 形式: %+v %v", authorization, err)
	}
	authorization, err = parseGrokAuthorizationInput("?code=c2&state=s2")
	if err != nil || authorization.code != "c2" || authorization.state != "s2" || !authorization.requiresState {
		t.Fatalf("query 形式: %+v %v", authorization, err)
	}
	authorization, err = parseGrokAuthorizationInput("?error=e")
	if err == nil {
		t.Fatalf("query error 转发必须报错")
	}
	authorization, err = parseGrokAuthorizationInput("bare")
	if err != nil || authorization.code != "bare" || authorization.requiresState {
		t.Fatalf("裸码: %+v %v", authorization, err)
	}
	// 无 code 的 URL 退化为裸码形态。
	authorization, err = parseGrokAuthorizationInput("https://cb?other=1")
	if err != nil || authorization.code == "" {
		t.Fatalf("无 code URL: %+v %v", authorization, err)
	}
}

// TestWCGeminiGrokPatchKeys：patch schema 的策略键与 modes 分支。
func TestWCGeminiGrokPatchKeys(t *testing.T) {
	patch, ok := parseGrokCredentialsPatch(map[string]any{
		"supported_endpoint_modes": []any{"chat_json"},
		"quota_recovery_policy":    map[string]any{},
	})
	if !ok || len(patch["supported_endpoint_modes"].([]any)) != 1 {
		t.Fatalf("grok patch: %v %v", patch, ok)
	}
	patch, ok = parseGeminiCredentialsPatch(map[string]any{
		"supported_endpoint_modes": []any{"chat_json"},
		"error_handling_rules":     map[string]any{},
	})
	if !ok || len(patch["supported_endpoint_modes"].([]any)) != 1 {
		t.Fatalf("gemini patch: %v %v", patch, ok)
	}
	if _, ok := parseGeminiCredentialsPatch(map[string]any{"service_tier_override": "  "}); ok {
		t.Fatalf("gemini 空 tier 必须拒绝")
	}
}

// TestWCRefreshTokenEmptyGuards：四家 refresh 的空令牌前置（直连 store）。
func TestWCRefreshTokenEmptyGuards(t *testing.T) {
	env := newTestEnv(t)
	if _, err := env.store.refreshOpenAIToken(context.Background(), "  ", ""); err == nil {
		t.Fatalf("openai 空 refresh 必须报错")
	}
	if _, err := env.store.refreshAnthropicToken(context.Background(), "  ", ""); err == nil {
		t.Fatalf("anthropic 空 refresh 必须报错")
	}
	if _, err := env.store.refreshGeminiToken(context.Background(), "  ", geminiAuthURLOptions{}); err == nil {
		t.Fatalf("gemini 空 refresh 必须报错")
	}
	if _, err := env.store.refreshGrokToken(context.Background(), "  ", ""); err == nil {
		t.Fatalf("grok 空 refresh 必须报错")
	}
	// gemini ai_studio 缺 client 凭据。
	if _, err := env.store.refreshGeminiToken(context.Background(), "rt", geminiAuthURLOptions{OAuthType: "ai_studio"}); err == nil {
		t.Fatalf("ai_studio 缺凭据必须报错")
	}
}

// TestWCSessionGuardsViaStore：会话缺失/属主不符/单次消费（store 直连）。
func TestWCSessionGuardsViaStore(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.exchanger.respond = staticToken(openAITokenPayload("guard"))

	// 未知会话。
	if _, err := env.store.exchangeOpenAIAuthorizationCode(context.Background(), "no-session", "https://cb?code=c&state=s", adminID); err == nil {
		t.Fatalf("未知 openai 会话必须报错")
	}
	if _, err := env.store.exchangeAnthropicAuthorizationCode(context.Background(), "no-session", "https://cb?code=c&state=s", adminID); err == nil {
		t.Fatalf("未知 anthropic 会话必须报错")
	}
	if _, err := env.store.exchangeGrokAuthorizationCode(context.Background(), "no-session", "https://cb?code=c&state=s", adminID); err == nil {
		t.Fatalf("未知 grok 会话必须报错")
	}
	// grok 属主不符。
	_, authPayload := env.do(t, http.MethodPost, "/__aisys__/api/grok-oauth/auth-url", `{}`)
	sessionID := dataMap(t, authPayload)["sessionId"].(string)
	if _, err := env.store.exchangeGrokAuthorizationCode(context.Background(), sessionID, "bare-code", "other-owner"); err == nil {
		t.Fatalf("grok 属主不符必须报错")
	}
	// openai 属主不符。
	_, authPayload = env.do(t, http.MethodPost, "/__aisys__/api/openai-oauth/auth-url", `{}`)
	openaiSession := dataMap(t, authPayload)["sessionId"].(string)
	openaiState := authStateFromURL(t, dataMap(t, authPayload)["authUrl"].(string))
	if _, err := env.store.exchangeOpenAIAuthorizationCode(context.Background(), openaiSession,
		"https://cb?code=c&state="+url.QueryEscape(openaiState), "other-owner"); err == nil {
		t.Fatalf("openai 属主不符必须报错")
	}
	// openai 空回调。
	if _, err := env.store.exchangeOpenAIAuthorizationCode(context.Background(), openaiSession, "", adminID); err == nil {
		t.Fatalf("空回调必须报错")
	}
}

// TestWCAuthropicRefreshAndReauth：anthropic 刷新/重授权流程（HTTP）。
func TestWCAnthropicRefreshAndReauth(t *testing.T) {
	env := newTestEnv(t)
	env.login(t, "root", "root-pass", "super_admin")
	anthropicPayload := `{"access_token":"ant-1","refresh_token":"ant-r1","expires_in":3600,` +
		`"account":{"email_address":"a@b.c","uuid":"u1"},"organization":{"uuid":"o1"}}`
	// 每次调用返回不同 access_token：若 create 与 refresh 落在同一毫秒，
	// expires_at 相同且 token 相同会触发"凭据未变化"的 no-op 分支。
	callIndex := 0
	env.exchanger.respond = func(_ int, call exchangeCall) (int, string) {
		if call.URL != AnthropicOAuthTokenURL {
			return http.StatusNotFound, `{}`
		}
		callIndex++
		body := map[string]any{}
		_ = json.Unmarshal([]byte(call.Body), &body)
		if body["grant_type"] == "refresh_token" {
			return http.StatusOK, fmt.Sprintf(`{"access_token":"ant-call-%d","refresh_token":"ant-r2","expires_in":3600}`, callIndex)
		}
		return http.StatusOK, anthropicPayload
	}
	code, created := env.do(t, http.MethodPost, "/__aisys__/api/anthropic-oauth/create-from-refresh-token",
		`{"refreshToken":"ant-refresh","providerProtocolProfileId":"profile_anthropic_anthropic_v1"}`)
	if code != http.StatusCreated {
		t.Fatalf("创建: %d %v", code, created)
	}
	accountID := dataMap(t, created)["id"].(string)
	// 刷新。
	code, refreshed := env.do(t, http.MethodPost, "/__aisys__/api/anthropic-oauth/accounts/"+accountID+"/refresh-token", `{"expectedConfigRevision":1}`)
	if code != http.StatusOK || dataMap(t, refreshed)["configRevision"] != float64(2) {
		t.Fatalf("刷新: %d %v", code, refreshed)
	}
	// reauthorize-from-refresh-token。
	env.exchanger.respond = staticToken(`{"access_token":"ant-3","refresh_token":"ant-r3","expires_in":3600}`)
	code, reauthorized := env.do(t, http.MethodPost, "/__aisys__/api/anthropic-oauth/accounts/"+accountID+"/reauthorize-from-refresh-token",
		`{"refreshToken":"ant-r3","expectedConfigRevision":2}`)
	if code != http.StatusOK || dataMap(t, reauthorized)["configRevision"] != float64(3) {
		t.Fatalf("重授权: %d %v", code, reauthorized)
	}
	if credentials := env.accountCredentials(t, accountID); credentials["access_token"] != "ant-3" {
		t.Fatalf("重授权凭据: %v", credentials)
	}
	// 缺 refreshToken → 当前实现映射 502 回退（行为存疑同 openai）。
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/anthropic-oauth/accounts/"+accountID+"/reauthorize-from-refresh-token",
		`{"expectedConfigRevision":3}`)
	if code != http.StatusBadGateway || payload["message"] != "Anthropic 刷新令牌重新授权失败" {
		t.Fatalf("缺 refreshToken（当前 502）: %d %v", code, payload)
	}
}

// TestWCGrokReauthorizeAndCreateRefresh：grok 重授权 + stored client 回退。
func TestWCGrokReauthorizeAndCreateRefresh(t *testing.T) {
	env := newTestEnv(t)
	env.login(t, "root", "root-pass", "super_admin")
	env.exchanger.respond = staticToken(grokTokenPayload("grok-reauth"))
	code, created := env.do(t, http.MethodPost, "/__aisys__/api/grok-oauth/create-from-refresh-token",
		`{"refreshToken":"grok-r0","providerProtocolProfileId":"profile_xai_openai_v1"}`)
	if code != http.StatusCreated {
		t.Fatalf("创建: %d %v", code, created)
	}
	accountID := dataMap(t, created)["id"].(string)

	// reauthorize-from-refresh-token：上游不带 refresh_token → 保留请求值。
	env.exchanger.respond = staticToken(`{"access_token":"grok-ra","token_type":"Bearer","expires_in":21600}`)
	code, reauthorized := env.do(t, http.MethodPost, "/__aisys__/api/grok-oauth/accounts/"+accountID+"/reauthorize-from-refresh-token",
		`{"refreshToken":"grok-r1","expectedConfigRevision":1}`)
	if code != http.StatusOK || dataMap(t, reauthorized)["configRevision"] != float64(2) {
		t.Fatalf("重授权: %d %v", code, reauthorized)
	}
	credentials := env.accountCredentials(t, accountID)
	if credentials["access_token"] != "grok-ra" || credentials["refresh_token"] != "grok-r1" {
		t.Fatalf("重授权凭据: %v", credentials)
	}
	// openai expires_in 缺失 → 502 回退。
	env.exchanger.respond = staticToken(`{"access_token":"x","token_type":"Bearer"}`)
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/openai-oauth/create-from-refresh-token",
		`{"refreshToken":"no-exp","providerProtocolProfileId":"profile_gpt_openai_v1"}`)
	if code != http.StatusBadGateway || payload["message"] != "OpenAI 刷新令牌授权失败" {
		t.Fatalf("缺 expires_in: %d %v", code, payload)
	}
}

// TestWCGeminiReauthorizeRefresh：gemini 重授权流程（HTTP）。
func TestWCGeminiReauthorizeRefresh(t *testing.T) {
	env := newTestEnv(t)
	env.login(t, "root", "root-pass", "super_admin")
	callIndex := 0
	env.exchanger.respond = func(_ int, call exchangeCall) (int, string) {
		if call.URL != GeminiOAuthTokenURL {
			return http.StatusNotFound, `{}`
		}
		callIndex++
		form := mustForm(t, call.Body)
		if form.Get("grant_type") != "refresh_token" {
			t.Fatalf("gemini 重授权请求: %v", form)
		}
		return http.StatusOK, fmt.Sprintf(`{"access_token":"gem-ra-%d","refresh_token":"gem-ra2","expires_in":3600,"scope":"s2"}`, callIndex)
	}
	code, created := env.do(t, http.MethodPost, "/__aisys__/api/gemini-oauth/create-from-refresh-token",
		`{"refreshToken":"gem-r0","projectId":"proj-1","providerProtocolProfileId":"profile_gemini_native_v1beta"}`)
	if code != http.StatusCreated {
		t.Fatalf("创建: %d %v", code, created)
	}
	accountID := dataMap(t, created)["id"].(string)
	code, reauthorized := env.do(t, http.MethodPost, "/__aisys__/api/gemini-oauth/accounts/"+accountID+"/reauthorize-from-refresh-token",
		`{"refreshToken":"gem-r1","expectedConfigRevision":1}`)
	if code != http.StatusOK || dataMap(t, reauthorized)["configRevision"] != float64(2) {
		t.Fatalf("重授权: %d %v", code, reauthorized)
	}
	credentials := env.accountCredentials(t, accountID)
	if credentials["access_token"] != "gem-ra-2" {
		t.Fatalf("重授权凭据: %v", credentials)
	}
	if credentials["scope"] != "s2" {
		t.Fatalf("新 scope 优先: %v", credentials["scope"])
	}
}

// TestWCSSOInputValidation：sso-to-oauth 的入参校验分支。
func TestWCSSOInputValidation(t *testing.T) {
	env := newTestEnv(t)
	env.login(t, "root", "root-pass", "super_admin")
	// name 参与指纹计算，用不同 name 避免失败指纹去重拦截后续子用例。
	cases := []struct {
		name string
		body string
	}{
		{"非数组 ssoTokens", `{"ssoTokens":"x","providerProtocolProfileId":"profile_xai_openai_v1","name":"n1"}`},
		{"非字符串项", `{"ssoTokens":[1],"providerProtocolProfileId":"profile_xai_openai_v1","name":"n2"}`},
		{"超长 token", `{"ssoTokens":["` + strings.Repeat("x", 16385) + `"],"providerProtocolProfileId":"profile_xai_openai_v1","name":"n3"}`},
		{"超长单 token", `{"ssoToken":"` + strings.Repeat("x", 16385) + `","providerProtocolProfileId":"profile_xai_openai_v1","name":"n4"}`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			code, payload := env.do(t, http.MethodPost, "/__aisys__/api/grok-oauth/sso-to-oauth", testCase.body)
			if code != http.StatusBadRequest || payload["message"] != "Grok SSO 导入参数无效" {
				t.Fatalf("%s: %d %v", testCase.name, code, payload)
			}
		})
	}
}

// TestWCCreateLogWithGroup：绑定分组的创建记录（logging 分支）。
func TestWCCreateLogWithGroup(t *testing.T) {
	env := newTestEnv(t)
	env.login(t, "root", "root-pass", "super_admin")
	env.exchanger.respond = staticToken(openAITokenPayload("group-log"))
	// 找到 root 的 gpt 默认分组（login 已 seed）。
	var groupID string
	if err := env.db.QueryRow(`SELECT id FROM groups WHERE provider_code = 'gpt' LIMIT 1`).Scan(&groupID); err != nil {
		t.Fatal(err)
	}
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/openai-oauth/create-from-refresh-token",
		fmt.Sprintf(`{"refreshToken":"rt","providerProtocolProfileId":"profile_gpt_openai_v1","groupId":%q,"name":"带组账户"}`, groupID))
	if code != http.StatusCreated {
		t.Fatalf("带组创建: %d %v", code, payload)
	}
	entries := env.sink.entries
	if len(entries) == 0 {
		t.Fatalf("必须记录操作日志")
	}
	last := entries[len(entries)-1]
	found := false
	for _, change := range last.Changes {
		if change.Field == "groupId" && change.After == groupID {
			found = true
		}
	}
	if !found {
		t.Fatalf("日志必须包含绑定分组: %+v", last.Changes)
	}
}

// TestWCOpenAIBlockedDirect：blocked 判定的边界（非 openai 供应商不走）。
func TestWCUpstreamErrorKind(t *testing.T) {
	err := errors.New("plain")
	if _, ok := upstreamStatus(err); ok {
		t.Fatalf("普通错误无 upstream 状态")
	}
	if got := upstreamError("X", 502, "detail"); got.StatusCode != 502 {
		t.Fatalf("upstreamError 默认状态: %+v", got)
	}
}
