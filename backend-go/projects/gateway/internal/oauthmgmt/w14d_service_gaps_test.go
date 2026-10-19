// w14d_service_gaps_test.go closes the remaining provider-service and store
// seams: corrupted/absent OAuth sessions, state mismatches, profile
// validation errors, the rotation no-op receipt, the expires_at validation
// and the session-store compare arm.
package oauthmgmt

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestW14dSessionCorruptionArms(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	// Corrupted session payloads surface as absent sessions.
	env.store.sessions.set("anthropic-oauth:sessions", "w14d-broken", make(chan int), oauthSessionTTL)
	if _, err := env.store.exchangeAnthropicAuthorizationCode(ctx, "w14d-broken", "https://cb?code=c&state=s", "owner", ""); err == nil ||
		err.Error() != "Anthropic OAuth 会话不存在或已过期" {
		t.Fatalf("anthropic broken session: %v", err)
	}
	env.store.sessions.set("gemini-oauth:sessions", "w14d-broken", make(chan int), oauthSessionTTL)
	if _, err := env.store.exchangeGeminiAuthorizationCode(ctx, geminiExchangeOptions{SessionID: "w14d-broken", CallbackURL: "https://cb?code=c&state=s", OwnerID: "owner"}, ""); err == nil ||
		err.Error() != "Gemini OAuth 会话不存在或已过期" {
		t.Fatalf("gemini broken session: %v", err)
	}
	env.store.sessions.set("openai-oauth:sessions", "w14d-broken", make(chan int), oauthSessionTTL)
	if _, err := env.store.exchangeOpenAIAuthorizationCode(ctx, "w14d-broken", "https://cb?code=c&state=s", "owner", ""); err == nil ||
		err.Error() != "OAuth 会话不存在或已过期" {
		t.Fatalf("openai broken session: %v", err)
	}

	// A state mismatch is rejected before the upstream call.
	env.store.sessions.set("openai-oauth:sessions", "w14d-state", openAIOAuthSession{
		State: "expected", CodeVerifier: "v", RedirectURI: "https://cb", ClientID: "cid",
		OwnerSystemAccountID: "owner",
	}, oauthSessionTTL)
	if _, err := env.store.exchangeOpenAIAuthorizationCode(ctx, "w14d-state", "https://cb?code=c&state=wrong", "owner", ""); err == nil ||
		err.Error() != "OAuth state 无效" {
		t.Fatalf("openai state mismatch: %v", err)
	}
	// An owner mismatch is rejected as well.
	if _, err := env.store.exchangeOpenAIAuthorizationCode(ctx, "w14d-state", "https://cb?code=c&state=expected", "other", ""); err == nil ||
		err.Error() != "OAuth session owner 归属无效" {
		t.Fatalf("openai owner mismatch: %v", err)
	}
	// Gemini state mismatch.
	env.store.sessions.set("gemini-oauth:sessions", "w14d-gstate", geminiOAuthSession{State: "expected"}, oauthSessionTTL)
	if _, err := env.store.exchangeGeminiAuthorizationCode(ctx, geminiExchangeOptions{SessionID: "w14d-gstate", CallbackURL: "https://cb?code=c&state=wrong", OwnerID: "owner"}, ""); err == nil ||
		err.Error() != "Gemini OAuth state 无效" {
		t.Fatalf("gemini state mismatch: %v", err)
	}
}

func TestW14dProfileValidationErrorArms(t *testing.T) {
	env := newTestEnv(t)
	env.w14dLoginAdmin(t)

	// 目标 schema 无档案表（T30/B4 ⑧）：停用门落在 vendors 行；未知 profile
	// 身份仍按既有文案拒绝。
	env.exec(t, `UPDATE vendors SET enabled = 0 WHERE code = 'gpt'`)
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/openai-oauth/create-from-code",
		`{"sessionId":"s","callbackUrl":"https://cb","providerProtocolProfileId":"profile_gpt_openai_v1"}`)
	if code != http.StatusBadRequest || !strings.Contains(payload["message"].(string), "已停用") {
		t.Fatalf("disabled vendor: %d %v", code, payload)
	}
	env.exec(t, `UPDATE vendors SET enabled = 1 WHERE code = 'gpt'`)

	// An unknown profile id renders the invalid-profile message.
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/anthropic-oauth/create-from-code",
		`{"sessionId":"s","callbackUrl":"https://cb","providerProtocolProfileId":"profile-nope"}`)
	if code != http.StatusBadRequest || !strings.Contains(payload["message"].(string), "供应商协议档案无效") {
		t.Fatalf("unknown profile: %d %v", code, payload)
	}
}

func TestW14dRotationNoopAndExpiresValidation(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.w14dSeedRotatableAccount(t, "w14d-noop", "gpt", "profile_gpt_openai_v1", "openai", "v1", true)

	// An invalid expires_at is rejected before the transaction.
	_, err := env.store.RotateCredentials(context.Background(), RotateCredentialsInput{
		AccountID:              "w14d-noop",
		ExpectedConfigRevision: 1,
		ExpectedProviderCode:   "gpt",
		ExpectedAccountType:    "oauth",
		Credentials:            map[string]any{"access_token": "a", "expires_at": "yesterday"},
		Access:                 AccessScope{ViewerID: adminID, IsAdmin: true},
	})
	if err == nil || err.Error() != "OAuth 凭据 expires_at 必须是带 Z 或数值 offset 的 RFC3339 时间" {
		t.Fatalf("expires validation: %v", err)
	}

	// Identical credentials produce the unchanged no-op receipt.
	current, err := env.store.findRotationAccount(context.Background(), "w14d-noop", AccessScope{ViewerID: adminID, IsAdmin: true})
	if err != nil || current == nil {
		t.Fatalf("find rotation account: %v %v", current, err)
	}
	result, err := env.store.RotateCredentials(context.Background(), RotateCredentialsInput{
		AccountID:              "w14d-noop",
		ExpectedConfigRevision: current.ConfigRevision,
		ExpectedProviderCode:   "gpt",
		ExpectedAccountType:    "oauth",
		Credentials:            current.Credentials,
		Access:                 AccessScope{ViewerID: adminID, IsAdmin: true},
	})
	if err != nil {
		t.Fatalf("no-op rotation: %v", err)
	}
	if result.Changed {
		t.Fatalf("identical credentials must be a no-op: %+v", result)
	}
	if result.ConfigRevision != current.ConfigRevision {
		t.Fatalf("no-op keeps the revision: %+v", result)
	}
}

func TestW14dSessionCompareJSONArm(t *testing.T) {
	store := newSessionStore(nil)
	store.set("ns", "live", map[string]any{"a": 1}, oauthSessionTTL)
	// A non-serializable expectation fails the comparison without deleting.
	if ok := store.compareDelete("ns", "live", make(chan int)); ok {
		t.Fatal("unserializable expectation must not delete")
	}
	// The entry survives the failed comparison.
	var raw json.RawMessage
	if err := json.Unmarshal(store.get("ns", "live"), &raw); err != nil {
		t.Fatalf("entry must survive: %v", err)
	}
}

func TestW14dCryptoSizeArms(t *testing.T) {
	sealed, err := encryptJSON(testSecret, map[string]any{"a": 1})
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(sealed, ":")
	var out map[string]any
	// iv / tag length mismatches fail before the GCM open.
	if err := decryptJSON(testSecret, "v1:QUJD:"+parts[2]+":"+parts[3], &out); err == nil {
		t.Fatal("short iv must fail")
	}
	if err := decryptJSON(testSecret, "v1:"+parts[1]+":QUJD:"+parts[3], &out); err == nil {
		t.Fatal("short tag must fail")
	}
	// A gcm.Open failure renders the unsupported-format error.
	if err := decryptJSON(testSecret, "v1:"+parts[1]+":"+parts[2]+":"+"AAAA", &out); err == nil {
		t.Fatal("tampered ciphertext must fail")
	}
	// encryptJSON marshal failure.
	if _, err := encryptJSON(testSecret, map[string]any{"ch": make(chan int)}); err == nil {
		t.Fatal("unmarshalable value must fail")
	}
}
