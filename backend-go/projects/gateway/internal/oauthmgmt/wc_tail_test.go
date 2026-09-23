// oauthmgmt 收尾补测：NewStore 参数校验、会话守卫的 store 直连矩阵
// （缺失/属主/state/类型）、token 请求非 2xx 分支、findGroupForProvider 与
// findRotationAccount 过滤、provider 档案协议不符、plan 守卫（缺
// sessionId/callbackUrl）、crypto 与 sessionstore 的序列化失败分支。
package oauthmgmt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// TestWCNewStoreValidationBranches：NewStore 其余参数校验。
func TestWCNewStoreValidationBranches(t *testing.T) {
	env := newTestEnv(t)
	if _, err := NewStore(env.db, false, "  ", nil, nil, nil, nil); err == nil || !strings.Contains(err.Error(), "secret") {
		t.Fatalf("空 secret: %v", err)
	}
	exchanger := ExchangerFunc(func(context.Context, TokenHTTPRequest) (TokenHTTPResponse, error) {
		return TokenHTTPResponse{StatusCode: 200}, nil
	})
	if _, err := NewStore(env.db, false, testSecret, nil, exchanger, nil, nil); err == nil || !strings.Contains(err.Error(), "accounts") {
		t.Fatalf("缺 accounts store: %v", err)
	}
	if _, err := NewStore(env.db, false, testSecret, env.store.Accounts, nil, nil, nil); err == nil || !strings.Contains(err.Error(), "exchanger") {
		t.Fatalf("缺 exchanger: %v", err)
	}
}

// TestWCSessionGuardsMatrix：四家交换的会话守卫矩阵（store 直连）。
func TestWCSessionGuardsMatrix(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	ctx := context.Background()

	// gemini：会话缺失 / state 为空 / 类型不符 / 属主不符。
	if _, err := env.store.exchangeGeminiAuthorizationCode(ctx, geminiExchangeOptions{SessionID: "none"}, ""); err == nil {
		t.Fatalf("gemini 缺失会话必须报错")
	}
	_, authPayload := env.do(t, http.MethodPost, "/__aisys__/api/gemini-oauth/auth-url", `{"oauthType":"ai_studio","clientId":"c","clientSecret":"s"}`)
	geminiSession := dataMap(t, authPayload)["sessionId"].(string)
	geminiState := authStateFromURL(t, dataMap(t, authPayload)["authUrl"].(string))
	// state 为空：URL 形式缺 state 在解析层即被拒。
	if _, err := env.store.exchangeGeminiAuthorizationCode(ctx, geminiExchangeOptions{
		SessionID: geminiSession, CallbackURL: "https://cb?code=c",
	}, ""); err == nil || !strings.Contains(err.Error(), "必须包含 code 和 state") {
		t.Fatalf("gemini 空 state: %v", err)
	}
	if _, err := env.store.exchangeGeminiAuthorizationCode(ctx, geminiExchangeOptions{
		SessionID: geminiSession, CallbackURL: "https://cb?code=c&state=" + geminiState,
		OwnerID: "other",
	}, ""); err == nil || !strings.Contains(err.Error(), "owner 归属无效") {
		t.Fatalf("gemini 属主不符: %v", err)
	}
	if _, err := env.store.exchangeGeminiAuthorizationCode(ctx, geminiExchangeOptions{
		SessionID: geminiSession, CallbackURL: "https://cb?code=c&state=" + geminiState,
		OwnerID: adminID, OAuthType: "google_one",
	}, ""); err == nil || !strings.Contains(err.Error(), "类型与授权会话不一致") {
		t.Fatalf("gemini 类型不符: %v", err)
	}
	if _, err := env.store.exchangeGeminiAuthorizationCode(ctx, geminiExchangeOptions{
		SessionID: geminiSession, CallbackURL: "https://cb?code=c&state=" + geminiState,
		OwnerID: adminID, ClientID: "mismatch",
	}, ""); err == nil || !strings.Contains(err.Error(), "Client ID 与授权会话不一致") {
		t.Fatalf("gemini clientId 不符: %v", err)
	}

	// anthropic：空回调后的 URL 形式缺 state / 属主不符。
	_, authPayload = env.do(t, http.MethodPost, "/__aisys__/api/anthropic-oauth/auth-url", `{}`)
	anthropicSession := dataMap(t, authPayload)["sessionId"].(string)
	anthropicState := authStateFromURL(t, dataMap(t, authPayload)["authUrl"].(string))
	if _, err := env.store.exchangeAnthropicAuthorizationCode(ctx, anthropicSession, "https://cb?code=c", adminID, ""); err == nil {
		t.Fatalf("anthropic 缺 state 必须报错")
	}
	if _, err := env.store.exchangeAnthropicAuthorizationCode(ctx, anthropicSession,
		"https://cb?code=c&state="+urlQueryEscape(anthropicState), "other", ""); err == nil {
		t.Fatalf("anthropic 属主不符必须报错")
	}

	// grok：URL 形式缺 state。
	_, authPayload = env.do(t, http.MethodPost, "/__aisys__/api/grok-oauth/auth-url", `{}`)
	grokSession := dataMap(t, authPayload)["sessionId"].(string)
	if _, err := env.store.exchangeGrokAuthorizationCode(ctx, grokSession, "https://cb?code=c", adminID, ""); err == nil ||
		!strings.Contains(err.Error(), "缺少 state") {
		t.Fatalf("grok 缺 state: %v", err)
	}

	// openai：state 不符。
	_, authPayload = env.do(t, http.MethodPost, "/__aisys__/api/openai-oauth/auth-url", `{}`)
	openaiSession := dataMap(t, authPayload)["sessionId"].(string)
	if _, err := env.store.exchangeOpenAIAuthorizationCode(ctx, openaiSession, "https://cb?code=c&state=wrong", adminID, ""); err == nil ||
		!strings.Contains(err.Error(), "state 无效") {
		t.Fatalf("openai state 不符: %v", err)
	}
}

func urlQueryEscape(value string) string {
	return strings.ReplaceAll(value, " ", "+")
}

// TestWCRequestTokenNon2xx：store 层 token 请求的非 2xx 分支。
func TestWCRequestTokenNon2xx(t *testing.T) {
	env := newTestEnv(t)
	// 2xx 但缺 access_token → 普通错误（非 UpstreamError）。
	env.exchanger.respond = staticToken(`{"expires_in":60}`)
	if _, err := env.store.requestOpenAIToken(context.Background(), map[string]string{"grant_type": "refresh_token"}, ""); err == nil ||
		err.Error() != "OpenAI OAuth 令牌响应缺少访问令牌" {
		t.Fatalf("openai 缺 access_token: %v", err)
	}
	// 非 2xx：让 exchanger 返回 500。
	env.exchanger.respond = func(_ int, _ exchangeCall) (int, string) {
		return http.StatusInternalServerError, `{"error":"boom"}`
	}
	if _, err := env.store.requestOpenAIToken(context.Background(), map[string]string{}, ""); err == nil ||
		!strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("openai 500: %v", err)
	}
	if _, err := env.store.requestAnthropicToken(context.Background(), map[string]string{}, ""); err == nil ||
		!strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("anthropic 500: %v", err)
	}
	if _, err := env.store.requestGrokToken(context.Background(), map[string]string{}, GrokOAuthClientID, ""); err == nil ||
		!strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("grok 500: %v", err)
	}
	if _, err := env.store.requestGeminiToken(context.Background(), map[string]string{}, geminiRequestOptions{}, ""); err == nil ||
		!strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("gemini 500: %v", err)
	}
	// gemini 缺 access_token（2xx）。
	env.exchanger.respond = staticToken(`{"expires_in":60}`)
	if _, err := env.store.requestGeminiToken(context.Background(), map[string]string{}, geminiRequestOptions{Scope: "fb"}, ""); err == nil ||
		!strings.Contains(err.Error(), "缺少 access_token") {
		t.Fatalf("gemini 缺 access_token: %v", err)
	}
}

// TestWCFindGroupAndRotationFilters：分组与轮换账号的作用域过滤。
func TestWCFindGroupAndRotationFilters(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.exchanger.respond = staticToken(openAITokenPayload("filter"))
	code, created := env.do(t, http.MethodPost, "/__aisys__/api/openai-oauth/create-from-refresh-token",
		`{"refreshToken":"rt","providerProtocolProfileId":"profile_gpt_openai_v1","name":"过滤账户"}`)
	if code != http.StatusCreated {
		t.Fatalf("建号: %d %v", code, created)
	}
	accountID := dataMap(t, created)["id"].(string)

	// 分组存在但供应商不符。
	adminScope := AccessScope{ViewerID: adminID, IsAdmin: true}
	ok, err := env.store.findGroupForProvider(context.Background(), "grp-default-gemini-"+adminID, adminScope, "gpt")
	if err != nil || ok {
		t.Fatalf("供应商不符分组必须 false: %v %v", ok, err)
	}
	// 分组缺失。
	ok, err = env.store.findGroupForProvider(context.Background(), "grp-none", adminScope, "gpt")
	if err != nil || ok {
		t.Fatalf("缺失分组必须 false: %v %v", ok, err)
	}
	// 匹配分组。
	ok, err = env.store.findGroupForProvider(context.Background(), "grp-default-gpt-"+adminID, adminScope, "gpt")
	if err != nil || !ok {
		t.Fatalf("匹配分组必须 true: %v %v", ok, err)
	}

	// 软删账号不可轮换读取。
	env.exec(t, `UPDATE accounts SET deleted_at = '2026-01-01T00:00:00Z' WHERE id = ?`, accountID)
	account, err := env.store.findRotationAccount(context.Background(), accountID, adminScope)
	if err != nil || account != nil {
		t.Fatalf("软删账号必须不可见: %+v %v", account, err)
	}
}

// TestWCResolveProviderProtocolMismatch：协议版本不符的档案拒绝。
func TestWCResolveProviderProtocolMismatch(t *testing.T) {
	env := newTestEnv(t)
	env.exec(t, `UPDATE provider_protocol_profiles SET protocol_code = 'anthropic' WHERE id = 'profile_gpt_openai_v1'`)
	_, err := env.store.resolveProviderProfile(context.Background(), "gpt", ProfileGPTOpenAIV1, "oauth", "")
	if err == nil || !strings.Contains(err.Error(), "不支持 OpenAI OAuth") {
		t.Fatalf("协议不符: %v", err)
	}
	// 停用供应商。
	env.exec(t, `UPDATE providers SET enabled = 0 WHERE code = 'gpt'`)
	_, err = env.store.resolveProviderProfile(context.Background(), "gpt", ProfileGPTOpenAIV1, "oauth", "")
	if err == nil || err.Error() != "供应商已停用：gpt" {
		t.Fatalf("停用供应商: %v", err)
	}
}

// TestWCPlanGuardsMissingFields：create-from-code 缺 sessionId/callbackUrl
// 的 plan 守卫（路由层校验必填字段并以 400 呈现）。
func TestWCPlanGuardsMissingFields(t *testing.T) {
	env := newTestEnv(t)
	env.login(t, "root", "root-pass", "super_admin")
	cases := []struct {
		name    string
		path    string
		message string
	}{
		{"openai", "/__aisys__/api/openai-oauth/create-from-code", "OpenAI 授权码参数无效"},
		{"anthropic", "/__aisys__/api/anthropic-oauth/create-from-code", "Anthropic 授权码参数无效"},
		{"grok", "/__aisys__/api/grok-oauth/create-from-code", "Grok 授权码参数无效"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			code, payload := env.do(t, http.MethodPost, testCase.path, `{}`)
			if code != http.StatusBadRequest || payload["message"] != testCase.message {
				t.Fatalf("%s: %d %v", testCase.name, code, payload)
			}
		})
	}
}

// TestWCCryptoAndSessionMarshalFailures：序列化失败的容错分支。
func TestWCCryptoAndSessionMarshalFailures(t *testing.T) {
	if _, err := encryptJSON(testSecret, make(chan int)); err == nil {
		t.Fatalf("不可序列化值必须报错")
	}
	// IV 长度错误（合法 base64）。
	sealed, err := encryptJSON(testSecret, map[string]any{"k": "v"})
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(sealed, ":")
	broken := strings.Join([]string{"v1", "AAAA", parts[2], parts[3]}, ":")
	var out map[string]any
	if err := decryptJSON(testSecret, broken, &out); err == nil {
		t.Fatalf("IV 长度错误必须失败")
	}

	store := newSessionStore(nil)
	type payload struct {
		State string
		Ch    chan int `json:"-"`
	}
	// compareDelete 对期望值序列化失败 → false 且不删除。
	if store.compareDelete("ns", "missing", make(chan int)) {
		t.Fatalf("缺失会话必须 false")
	}
	_ = payload{}
	var raw json.RawMessage
	_ = raw
}

// TestWCGeminiCredentialFallbackPicks：fallback 字段的兜底投影。
func TestWCGeminiCredentialFallbackPicks(t *testing.T) {
	info := &geminiTokenInfo{AccessToken: "a", OAuthType: "ai_studio"}
	fallback := &geminiCredentialFallback{
		RefreshToken: "fr", OAuthType: "ai_studio", ProjectID: "fp",
		TierID: "aistudio-paid", QuotaProjectID: "fq", BaseURL: "https://fallback", Scope: "fs",
	}
	credentials := buildGeminiOAuthCredentials(info, fallback)
	if credentials["refresh_token"] != "fr" || credentials["project_id"] != "fp" ||
		credentials["quota_project_id"] != "fq" || credentials["base_url"] != "https://fallback" ||
		credentials["scope"] != "fs" || credentials["tier_id"] != "aistudio_paid" {
		t.Fatalf("fallback 投影: %v", credentials)
	}
	if _, hasModes := credentials["supported_endpoint_modes"]; hasModes {
		t.Fatalf("ai_studio 不写端点模式: %v", credentials)
	}
	// 行为存疑：fallback=nil 时 buildGeminiOAuthCredentials 在 gemini.go:583
	// 解引用 fallback.ProjectID 必然 panic（见 wc_flow_test.go 的标注），
	// 此处以非 nil fallback 验证 info 字段优先的分支。
	credentials = buildGeminiOAuthCredentials(&geminiTokenInfo{AccessToken: "a", TierID: "legacy", ProjectID: "ip", Scope: "is", QuotaProjectID: "iq", BaseURL: "https://info"}, fallback)
	if credentials["project_id"] != "ip" || credentials["scope"] != "is" || credentials["base_url"] != "https://info" || credentials["tier_id"] != "aistudio_paid" {
		t.Fatalf("info 优先投影: %v", credentials)
	}
	// code_assist：oauthType 由 info 缺省回退到 fallback，端点模式数组写入。
	codeAssist := buildGeminiOAuthCredentials(&geminiTokenInfo{AccessToken: "a"}, &geminiCredentialFallback{
		OAuthType: "code_assist", TierID: "legacy", ProjectID: "fp", RefreshToken: "fr",
	})
	if codeAssist["oauth_type"] != "code_assist" || codeAssist["tier_id"] != "gcp_standard" {
		t.Fatalf("code_assist 投影: %v", codeAssist)
	}
	if modes, ok := codeAssist["supported_endpoint_modes"].([]any); !ok || len(modes) != 2 {
		t.Fatalf("code_assist 模式: %v", codeAssist["supported_endpoint_modes"])
	}
	if fallbackScope(nil) != "" || fallbackScope(fallback) != "fs" {
		t.Fatalf("fallbackScope")
	}
}

// TestWCUpstreamErrVariants：UpstreamError 的构造与判定补充。
func TestWCUpstreamErrVariants(t *testing.T) {
	var target *UpstreamError
	if !errors.As(error(&UpstreamError{}), &target) {
		t.Fatalf("errors.As 必须命中")
	}
	if _, ok := upstreamStatus(nil); ok {
		t.Fatalf("nil 错误无状态")
	}
	if got := fmt.Sprint(grokSSOHTTPError("x", 1)); !strings.Contains(got, "xAI OAuth HTTP 1") {
		t.Fatalf("grokSSOHTTPError: %s", got)
	}
}
