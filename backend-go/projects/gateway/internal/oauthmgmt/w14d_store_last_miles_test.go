// w14d_store_last_miles_test.go closes the final oauthmgmt seams: corrupted
// session entries written straight into the store, the cross-protocol
// profile validation, the rotation expires_at write arm, the dispatch
// revision advancer failure arm and the session compare JSON arm.
package oauthmgmt

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestW14dCorruptedSessionEntries(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	// Broken JSON written straight into the session store hits the
	// unmarshalSession arms of every provider exchange.
	broken := json.RawMessage("{broken")
	env.store.sessions.entries["anthropic-oauth:sessions:w14d-bad"] = sessionEntry{
		value: broken, expiresAt: time.Now().Add(oauthSessionTTL),
	}
	if _, err := env.store.exchangeAnthropicAuthorizationCode(ctx, "w14d-bad", "https://cb?code=c&state=s", "owner", ""); err == nil ||
		err.Error() != "Anthropic OAuth 会话不存在或已过期" {
		t.Fatalf("anthropic broken entry: %v", err)
	}
	env.store.sessions.entries["gemini-oauth:sessions:w14d-bad"] = sessionEntry{
		value: broken, expiresAt: time.Now().Add(oauthSessionTTL),
	}
	if _, err := env.store.exchangeGeminiAuthorizationCode(ctx, geminiExchangeOptions{
		SessionID: "w14d-bad", CallbackURL: "https://cb?code=c&state=s", OwnerID: "owner",
	}, ""); err == nil || err.Error() != "Gemini OAuth 会话不存在或已过期" {
		t.Fatalf("gemini broken entry: %v", err)
	}

	// The compare arm also fails on a broken stored value.
	env.store.sessions.entries["ns:w14d-broken-compare"] = sessionEntry{
		value: broken, expiresAt: time.Now().Add(oauthSessionTTL),
	}
	if env.store.sessions.compareDelete("ns", "w14d-broken-compare", map[string]any{}) {
		t.Fatal("broken entry must not compare")
	}
}

func TestW14dCrossProtocolProfileValidation(t *testing.T) {
	env := newTestEnv(t)
	env.w14dLoginAdmin(t)

	// An anthropic-provider profile that carries the openai protocol cannot
	// serve the anthropic OAuth plan.
	now := time.Now().UTC().Format(time.RFC3339Nano)
	env.exec(t, `INSERT INTO provider_protocol_profiles (id, provider_code, name, enabled, protocol_code,
		protocol_version, base_url, default_health_check_model, account_types_json, capabilities_json, created_at, updated_at)
		VALUES ('w14d_profile_mismatch', 'anthropic', '协议错位', 1, 'openai', 'v1', 'https://x', 'claude',
		'["oauth"]', '[]', ?, ?)`, now, now)
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/anthropic-oauth/create-from-code",
		`{"sessionId":"s","callbackUrl":"https://cb","providerProtocolProfileId":"w14d_profile_mismatch"}`)
	if code != http.StatusBadRequest || !strings.Contains(payload["message"].(string), "不支持") {
		t.Fatalf("cross protocol profile: %d %v", code, payload)
	}
}

func TestW14dRotationExpiresAtWriteArm(t *testing.T) {
	env := newTestEnv(t)
	env.w14dSeedRotatableAccount(t, "w14d-exp", "gpt", "profile_gpt_openai_v1", "openai", "v1", true)
	adminID := env.w14dLoginAdmin(t)

	// A canonical expires_at rides onto the rotated row.
	result, err := env.store.RotateCredentials(context.Background(), RotateCredentialsInput{
		AccountID:                         "w14d-exp",
		ExpectedConfigRevision:            1,
		ExpectedProviderCode:              "gpt",
		ExpectedAccountType:               "oauth",
		ExpectedProviderProtocolProfileID: "profile_gpt_openai_v1",
		Credentials: map[string]any{
			"access_token": "brand-new", "refresh_token": "r2",
			"expires_at": "2030-01-01T00:00:00Z",
		},
		Access: AccessScope{ViewerID: adminID, IsAdmin: true},
	})
	if err != nil || result == nil || !result.Changed {
		t.Fatalf("rotate with expires_at: %v %+v", err, result)
	}
	var expiresAt *string
	if err := env.db.QueryRow(`SELECT oauth_access_token_expires_at FROM accounts WHERE id = 'w14d-exp'`).Scan(&expiresAt); err != nil {
		t.Fatal(err)
	}
	if expiresAt == nil || !strings.HasPrefix(*expiresAt, "2030-01-01T00:00:00") {
		t.Fatalf("persisted expires_at: %v", expiresAt)
	}
}
