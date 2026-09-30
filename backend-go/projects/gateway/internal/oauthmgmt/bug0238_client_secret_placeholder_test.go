// bug0238_client_secret_placeholder_test.go pins the BUG-0238 契约延伸 on the
// gemini 授权/重新授权流：重新授权上下文回显的统一密文占位被前端原样提交时，
// 视为"未提交 clientSecret"，各链路走既有空值回退（存储现值 / 授权会话值 /
// GEMINI_OAUTH_CLIENT_SECRET 环境变量），存储凭据不被占位覆盖、不被清空。
package oauthmgmt

import (
	"context"
	"net/http"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts"
)

// TestBug0238GeminiReauthRefreshPlaceholderKeepsStoredSecret 覆盖
// reauthorize-from-refresh-token 链路：提交占位 clientSecret → pick 回退账户
// 存储现值，上游 token 请求与落库合并后的 client_secret 均保持原值。
func TestBug0238GeminiReauthRefreshPlaceholderKeepsStoredSecret(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	current := &rotationAccount{Credentials: map[string]any{
		"refresh_token": "stored", "client_id": "cid", "client_secret": "sec",
		"oauth_type": "ai_studio", "base_url": "https://g.example", "scope": "s",
	}}
	var upstreamSecret string
	env.exchanger.respond = func(_ int, call exchangeCall) (int, string) {
		upstreamSecret = mustForm(t, call.Body).Get("client_secret")
		return http.StatusOK, `{"access_token":"a","refresh_token":"r","expires_in":3600,"token_type":"Bearer"}`
	}
	credentials, err := geminiPlan().refreshInput(ctx, env.store, map[string]any{
		"refreshToken": "rt", "clientSecret": accounts.CredentialCipherPlaceholder,
	}, current, "")
	if err != nil {
		t.Fatal(err)
	}
	if upstreamSecret != "sec" {
		t.Fatalf("上游请求应使用存储现值 client_secret：%q", upstreamSecret)
	}
	if credentials["client_secret"] != "sec" {
		t.Fatalf("token 凭据 client_secret 应保持现值：%v", credentials["client_secret"])
	}
	merged := mergeRotationCredentials(current.Credentials, credentials, true)
	if merged["client_secret"] != "sec" {
		t.Fatalf("落库合并后 client_secret 应保持现值：%v", merged["client_secret"])
	}
}

// TestBug0238GeminiAuthURLAndCodePlaceholderFallBack 覆盖 auth-url 与
// reauthorize-from-code 链路：占位 clientSecret 在 auth-url 走
// GEMINI_OAUTH_CLIENT_SECRET 环境变量回退（建户链路无账户存储上下文），授权码
// 交换的占位提交跳过会话一致性检查、沿用授权会话保存的真实 secret；非占位的
// 错误 secret 仍照常触发会话一致性拒绝。
func TestBug0238GeminiAuthURLAndCodePlaceholderFallBack(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	t.Setenv("GEMINI_OAUTH_CLIENT_ID", "env-cid")
	t.Setenv("GEMINI_OAUTH_CLIENT_SECRET", "env-sec")
	ownerID := "b0238-owner"

	// auth-url 提交占位 → 视为未提交 → ai_studio 走 env 回退建会话。
	authPayload, err := geminiPlan().authURL(ctx, env.store, map[string]any{
		"oauthType": "ai_studio", "clientSecret": accounts.CredentialCipherPlaceholder,
	}, ownerID)
	if err != nil {
		t.Fatal(err)
	}
	sessionID, _ := authPayload["sessionId"].(string)
	state, _ := authPayload["state"].(string)
	if sessionID == "" || state == "" {
		t.Fatalf("auth-url 应返回会话：%v", authPayload)
	}
	var session geminiOAuthSession
	if err := unmarshalSession(env.store.sessions.get(geminiSessionNamespace, sessionID), &session); err != nil {
		t.Fatal(err)
	}
	if session.ClientSecret != "env-sec" || session.ClientID != "env-cid" {
		t.Fatalf("会话应保存 env 回退凭据：%+v", session)
	}

	env.exchanger.respond = func(_ int, call exchangeCall) (int, string) {
		if got := mustForm(t, call.Body).Get("client_secret"); got != "env-sec" {
			t.Fatalf("交换请求应使用会话保存的真实 client_secret：%q", got)
		}
		return http.StatusOK, `{"access_token":"a","refresh_token":"r","expires_in":3600,"token_type":"Bearer"}`
	}
	callback := "https://cb?code=gem-code&state=" + state

	// 授权码交换提交占位 → 跳过会话一致性检查，沿用会话真实 secret。
	outcome, err := geminiPlan().exchangeCode(ctx, env.store, map[string]any{
		"sessionId": sessionID, "callbackUrl": callback, "oauthType": "ai_studio",
		"clientSecret": accounts.CredentialCipherPlaceholder,
	}, ownerID, "")
	if err != nil {
		t.Fatalf("占位 clientSecret 不应触发会话不一致：%v", err)
	}
	if outcome.Credentials["client_secret"] != "env-sec" {
		t.Fatalf("凭据 client_secret 应为会话真实值：%v", outcome.Credentials["client_secret"])
	}

	// 对照：非占位的错误 secret 仍触发既有会话一致性拒绝（占位识别不是移除校验）。
	authPayload, err = geminiPlan().authURL(ctx, env.store, map[string]any{
		"oauthType": "ai_studio", "clientSecret": "env-sec",
	}, ownerID)
	if err != nil {
		t.Fatal(err)
	}
	wrongStateCallback := "https://cb?code=gem-code&state=" + authPayload["state"].(string)
	_, err = geminiPlan().exchangeCode(ctx, env.store, map[string]any{
		"sessionId": authPayload["sessionId"].(string), "callbackUrl": wrongStateCallback,
		"oauthType": "ai_studio", "clientSecret": "wrong-secret",
	}, ownerID, "")
	if err == nil || err.Error() != "Gemini OAuth Client Secret 与授权会话不一致" {
		t.Fatalf("错误 secret 应触发会话一致性拒绝：%v", err)
	}
}

// TestBug0238GeminiCreateRefreshPlaceholderUsesEnv 覆盖
// create-from-refresh-token 建户链路：占位 clientSecret → env 回退（无账户存储
// 上下文），占位不进入上游请求。
func TestBug0238GeminiCreateRefreshPlaceholderUsesEnv(t *testing.T) {
	env := newTestEnv(t)
	t.Setenv("GEMINI_OAUTH_CLIENT_ID", "env-cid")
	t.Setenv("GEMINI_OAUTH_CLIENT_SECRET", "env-sec")
	var upstreamSecret string
	env.exchanger.respond = func(_ int, call exchangeCall) (int, string) {
		upstreamSecret = mustForm(t, call.Body).Get("client_secret")
		return http.StatusOK, `{"access_token":"a","refresh_token":"r","expires_in":3600,"token_type":"Bearer"}`
	}
	outcome, err := geminiPlan().exchangeRefresh(context.Background(), env.store, map[string]any{
		"refreshToken": "rt", "oauthType": "ai_studio",
		"clientSecret": accounts.CredentialCipherPlaceholder,
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if upstreamSecret != "env-sec" {
		t.Fatalf("建户刷新请求应使用 env 回退 client_secret：%q", upstreamSecret)
	}
	if outcome.Credentials["client_secret"] != "env-sec" {
		t.Fatalf("建户凭据 client_secret 应为 env 值：%v", outcome.Credentials["client_secret"])
	}
}
