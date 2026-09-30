// gemini_session_secret_consistency_test.go pins the gemini 授权码交换的
// clientSecret 会话一致性检查：提交与会话保存值不一致的 clientSecret 时，
// 交换照常拒绝（gemini.go assertGeminiSessionField 矩阵的 Client Secret 臂）。
package oauthmgmt

import (
	"context"
	"testing"
)

// TestGeminiExchangeCodeClientSecretMismatch 覆盖 reauthorize-from-code 链路：
// 授权会话以 env 回退建立后，提交错误 clientSecret 触发会话一致性拒绝。
func TestGeminiExchangeCodeClientSecretMismatch(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	t.Setenv("GEMINI_OAUTH_CLIENT_ID", "env-cid")
	t.Setenv("GEMINI_OAUTH_CLIENT_SECRET", "env-sec")
	ownerID := "gemini-secret-consistency-owner"

	authPayload, err := geminiPlan().authURL(ctx, env.store, map[string]any{
		"oauthType": "ai_studio", "clientSecret": "env-sec",
	}, ownerID)
	if err != nil {
		t.Fatal(err)
	}
	sessionID, _ := authPayload["sessionId"].(string)
	state, _ := authPayload["state"].(string)
	if sessionID == "" || state == "" {
		t.Fatalf("auth-url 应返回会话：%v", authPayload)
	}

	// 会话一致性检查在 token 请求之前拒绝，默认 exchanger 不会被触达。
	callback := "https://cb?code=gem-code&state=" + state
	_, err = geminiPlan().exchangeCode(ctx, env.store, map[string]any{
		"sessionId": sessionID, "callbackUrl": callback,
		"oauthType": "ai_studio", "clientSecret": "wrong-secret",
	}, ownerID, "")
	if err == nil || err.Error() != "Gemini OAuth Client Secret 与授权会话不一致" {
		t.Fatalf("错误 secret 应触发会话一致性拒绝：%v", err)
	}
}
