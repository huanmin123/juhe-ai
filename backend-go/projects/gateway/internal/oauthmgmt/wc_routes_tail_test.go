// oauthmgmt 路由守卫收尾：refresh/reauthorize 的严格 body 与 revision 校验、
// 重授权的供应商不匹配、create-from-code 的键与类型校验、sso 导入的失败
// 项收集，以及四个 provider plan 闭包的空参守卫（store 直连）。
package oauthmgmt

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// TestWCRefreshStrictBodyAndRevision：refresh-token 的严格 body/revision 分支。
func TestWCRefreshStrictBodyAndRevision(t *testing.T) {
	env := newTestEnv(t)
	env.login(t, "root", "root-pass", "super_admin")
	env.exchanger.respond = staticToken(openAITokenPayload("strict"))
	code, created := env.do(t, http.MethodPost, "/__aisys__/api/openai-oauth/create-from-refresh-token",
		`{"refreshToken":"rt","providerProtocolProfileId":"profile_gpt_openai_v1","name":"严格校验"}`)
	if code != http.StatusCreated {
		t.Fatalf("建号: %d %v", code, created)
	}
	accountID := dataMap(t, created)["id"].(string)

	// 未知键 → 400 专用文案。
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/openai-oauth/accounts/"+accountID+"/refresh-token",
		`{"expectedConfigRevision":1,"bogus":1}`)
	if code != http.StatusBadRequest || payload["message"] != "OpenAI 访问令牌刷新参数无效" {
		t.Fatalf("严格 body: %d %v", code, payload)
	}
	// revision 缺失/非法。
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/openai-oauth/accounts/"+accountID+"/refresh-token", `{}`)
	if code != http.StatusBadRequest || payload["message"] != "OpenAI 访问令牌刷新参数无效" {
		t.Fatalf("缺 revision: %d %v", code, payload)
	}
	code, _ = env.do(t, http.MethodPost, "/__aisys__/api/openai-oauth/accounts/"+accountID+"/refresh-token",
		`{"expectedConfigRevision":0}`)
	if code != http.StatusBadRequest {
		t.Fatalf("revision 0: %d", code)
	}

	// reauthorize-from-refresh-token：严格 body 与缺 revision。
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/openai-oauth/accounts/"+accountID+"/reauthorize-from-refresh-token",
		`{"refreshToken":"x","expectedConfigRevision":1,"bogus":1}`)
	if code != http.StatusBadRequest || payload["message"] != "OpenAI 刷新令牌参数无效" {
		t.Fatalf("reauth 严格 body: %d %v", code, payload)
	}
	code, _ = env.do(t, http.MethodPost, "/__aisys__/api/openai-oauth/accounts/"+accountID+"/reauthorize-from-refresh-token",
		`{"refreshToken":"x"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("reauth 缺 revision: %d", code)
	}

	// reauthorize-from-code：供应商不匹配 → 404。
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/anthropic-oauth/accounts/"+accountID+"/reauthorize-from-code",
		`{"sessionId":"s","callbackUrl":"https://cb?code=c&state=s","expectedConfigRevision":1}`)
	if code != http.StatusNotFound || payload["message"] != "Anthropic OAuth 账户不存在或无权操作" {
		t.Fatalf("reauth 供应商不符: %d %v", code, payload)
	}
}

// TestWCCreateFromCodeBodyValidation：create-from-code 的键与类型校验。
func TestWCCreateFromCodeBodyValidation(t *testing.T) {
	env := newTestEnv(t)
	env.login(t, "root", "root-pass", "super_admin")
	// 未知键。
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/openai-oauth/create-from-code",
		`{"sessionId":"s","callbackUrl":"c","bogus":1}`)
	if code != http.StatusBadRequest || payload["message"] != "OpenAI 授权码参数无效" {
		t.Fatalf("未知键: %d %v", code, payload)
	}
	// sessionId 非字符串 → parseManagedFields 拒绝。
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/openai-oauth/create-from-code",
		`{"sessionId":123,"callbackUrl":"c"}`)
	if code != http.StatusBadRequest || payload["message"] != "OpenAI 授权码参数无效" {
		t.Fatalf("sessionId 类型: %d %v", code, payload)
	}
}

// TestWCSsoUnknownKeyAndFailedItem：sso 导入的严格键与失败项收集。
func TestWCSsoUnknownKeyAndFailedItem(t *testing.T) {
	env := newTestEnv(t)
	env.login(t, "root", "root-pass", "super_admin")

	// 未知键。
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/grok-oauth/sso-to-oauth",
		`{"ssoTokens":["a"],"providerProtocolProfileId":"profile_xai_openai_v1","bogus":1}`)
	if code != http.StatusBadRequest || payload["message"] != "Grok SSO 导入参数无效" {
		t.Fatalf("未知键: %d %v", code, payload)
	}

	// 无 refresh_token 的 token + 非法 accountExpiresAt → 失败项携带错误。
	tokenBody := fmt.Sprintf(`{"access_token":%q,"id_token":%q,"token_type":"Bearer","expires_in":21600}`,
		fakeJWT(map[string]any{"email": "expire@example.com"}), fakeJWT(map[string]any{"sub": "s"}))
	env.sso.steps = []SSODeviceResponse{
		ssoStep(http.StatusOK, nil, "<html>accounts</html>"),
		ssoStep(http.StatusOK, nil, `{"device_code":"dc","user_code":"UC","verification_uri_complete":"https://auth.x.ai/device?user_code=UC","interval":1,"expires_in":600}`),
		ssoStep(http.StatusOK, nil, "<html>device</html>"),
		ssoStep(http.StatusFound, map[string]string{"location": "https://auth.x.ai/oauth2/device/consent"}, ""),
		ssoStep(http.StatusOK, nil, "<html>consent</html>"),
		ssoStep(http.StatusFound, map[string]string{"location": "https://auth.x.ai/oauth2/device/done"}, ""),
		ssoStep(http.StatusOK, nil, "<html>done</html>"),
		ssoStep(http.StatusOK, nil, tokenBody),
	}
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/grok-oauth/sso-to-oauth",
		`{"ssoTokens":["sso=expire"],"providerProtocolProfileId":"profile_xai_openai_v1","accountExpiresAt":"not-a-date"}`)
	if code != http.StatusOK {
		t.Fatalf("导入: %d %v", code, payload)
	}
	failed := dataMap(t, payload)["failed"].([]any)
	if len(failed) != 1 {
		t.Fatalf("必须收集失败项: %v", failed)
	}
	item := failed[0].(map[string]any)
	if item["index"] != float64(1) || !strings.Contains(item["error"].(string), "RFC3339") {
		t.Fatalf("失败项: %v", item)
	}
}

// TestWCProviderPlanGuardsDirect：四个 plan 闭包的空参守卫（store 直连）。
func TestWCProviderPlanGuardsDirect(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	for _, plan := range providerPlans() {
		t.Run(plan.slug, func(t *testing.T) {
			// exchangeCode：缺 sessionId/callbackUrl。
			if _, err := plan.exchangeCode(ctx, env.store, map[string]any{}, "owner", ""); err == nil {
				t.Fatalf("exchangeCode 空参必须报错")
			}
			// exchangeRefresh：缺 refreshToken。
			if _, err := plan.exchangeRefresh(ctx, env.store, map[string]any{}, ""); err == nil {
				t.Fatalf("exchangeRefresh 空参必须报错")
			}
			// refreshInput：缺 refreshToken。
			if _, err := plan.refreshInput(ctx, env.store, map[string]any{}, &rotationAccount{Credentials: map[string]any{}}, ""); err == nil {
				t.Fatalf("refreshInput 空参必须报错")
			}
			// refreshStored：缺凭据（grok/openai/anthropic/gemini 各自语义）。
			if _, err := plan.refreshStored(ctx, env.store, &rotationAccount{Credentials: map[string]any{}}, ""); err == nil {
				t.Fatalf("refreshStored 空凭据必须报错")
			}
			// safePatch 非法 patch → credentialsPatch 无效。
			if _, err := plan.exchangeRefresh(ctx, env.store, map[string]any{"refreshToken": "rt", "credentialsPatch": []any{1}}, ""); err == nil {
				t.Fatalf("非法 patch 必须报错")
			}
		})
	}
}

// TestWCCreateOAuthAccountGrokPin：grok pin 在 CreateOAuthAccount 内的校验。
func TestWCCreateOAuthAccountGrokPin(t *testing.T) {
	env := newTestEnv(t)
	_, err := env.store.CreateOAuthAccount(context.Background(), CreateAccountInput{
		ProviderCode: "xai", ProviderProtocolProfileID: ProfileGPTOpenAIV1,
		Name: "n", AccountType: "oauth", Credentials: map[string]any{"access_token": "a"},
	}, AccessScope{ViewerID: "x"})
	if err == nil {
		t.Fatalf("pin 不符必须报错")
	}
	validation, isValidation := err.(*ValidationError)
	if !isValidation {
		t.Fatalf("必须是 ValidationError: %T", err)
	}
	if !strings.Contains(validation.Message, "不支持 Grok OAuth") && !strings.Contains(validation.Message, "协议档案") {
		t.Fatalf("message: %q", validation.Message)
	}
}

// TestWCSanitizeHelpersTail：杂项零散分支（避免悬空覆盖）。
func TestWCSanitizeHelpersTail(t *testing.T) {
	if !strings.EqualFold("A", "a") {
		t.Fatalf("strings 行为")
	}
	// 归一化 oauth type。
	if normalizeGeminiOAuthType(" AI_STUDIO ") != "code_assist" {
		t.Fatalf("normalizeGeminiOAuthType 大小写敏感回退: %q", normalizeGeminiOAuthType(" AI_STUDIO "))
	}
	if normalizeGeminiOAuthType("ai_studio") != "ai_studio" {
		t.Fatalf("normalizeGeminiOAuthType 合法值")
	}
	// gemini 客户端配置。
	usesBuiltIn, redirect, scope := geminiOAuthClientConfig("ai_studio")
	if usesBuiltIn || redirect != GeminiOAuthRedirectURI || scope != GeminiOAuthScope {
		t.Fatalf("ai_studio 客户端配置: %v %q %q", usesBuiltIn, redirect, scope)
	}
	usesBuiltIn, redirect, scope = geminiOAuthClientConfig("code_assist")
	if !usesBuiltIn || redirect != GeminiCLIOAuthRedirectURI || scope != GeminiCodeAssistOAuthScope {
		t.Fatalf("code_assist 客户端配置: %v %q %q", usesBuiltIn, redirect, scope)
	}
	// 默认 base URL。
	if defaultGeminiBaseURL("ai_studio") != geminiOAuthDefaultBaseURL || defaultGeminiBaseURL("code_assist") != geminiCLIDefaultBaseURL {
		t.Fatalf("defaultGeminiBaseURL")
	}
}
