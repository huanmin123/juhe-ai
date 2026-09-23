// w14d_final_arms_test.go closes the last counting arms: the anthropic
// requiresState and owner diagnostics, the anthropic request token
// validation, the grok state/owner guards and the crypto gcm-open arm.
package oauthmgmt

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestW14dAnthropicSessionRequiresStateArms(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	// The owner mismatch diagnostic.
	env.store.sessions.entries["anthropic-oauth:sessions:w14d-owner"] = sessionEntry{
		value:     mustJSON(t, anthropicOAuthSession{State: "expected", OwnerSystemAccountID: "owner"}),
		expiresAt: time.Now().Add(oauthSessionTTL),
	}
	if _, err := env.store.exchangeAnthropicAuthorizationCode(ctx, "w14d-owner", "https://cb?code=c&state=expected", "other", ""); err == nil ||
		err.Error() != "Anthropic OAuth session owner 归属无效" {
		t.Fatalf("owner mismatch: %v", err)
	}
	// The upstream validator requires an access token and tolerates a body
	// without error details (raw body fallback).
	env.exchanger.respond = staticToken(`totally not json`)
	env.store.sessions.entries["anthropic-oauth:sessions:w14d-raw"] = sessionEntry{
		value:     mustJSON(t, anthropicOAuthSession{State: "expected"}),
		expiresAt: time.Now().Add(oauthSessionTTL),
	}
	if _, err := env.store.exchangeAnthropicAuthorizationCode(ctx, "w14d-raw", "https://cb?code=c&state=expected", "owner", ""); err == nil ||
		!strings.Contains(err.Error(), "access_token") {
		t.Fatalf("raw body detail: %v", err)
	}
}

func TestW14dGrokSessionGuardsArms(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	// The missing-state guard.
	env.store.sessions.entries["grok-oauth:sessions:w14d-nostate"] = sessionEntry{
		value:     mustJSON(t, grokOAuthSession{State: "expected"}),
		expiresAt: time.Now().Add(oauthSessionTTL),
	}
	if _, err := env.store.exchangeGrokAuthorizationCode(ctx, "w14d-nostate", "https://cb?code=c", "owner", ""); err == nil ||
		err.Error() != "Grok OAuth 回调缺少 state" {
		t.Fatalf("missing grok state: %v", err)
	}
	// The owner mismatch guard.
	env.store.sessions.entries["grok-oauth:sessions:w14d-owner"] = sessionEntry{
		value:     mustJSON(t, grokOAuthSession{State: "expected", OwnerSystemAccountID: "owner"}),
		expiresAt: time.Now().Add(oauthSessionTTL),
	}
	if _, err := env.store.exchangeGrokAuthorizationCode(ctx, "w14d-owner", "https://cb?code=c&state=expected", "other", ""); err == nil ||
		err.Error() != "Grok OAuth session owner 归属无效" {
		t.Fatalf("grok owner mismatch: %v", err)
	}
	// The upstream validator requires an access token.
	env.exchanger.respond = staticToken(`{"refresh_token":"r"}`)
	env.store.sessions.entries["grok-oauth:sessions:w14d-tok"] = sessionEntry{
		value:     mustJSON(t, grokOAuthSession{State: "expected"}),
		expiresAt: time.Now().Add(oauthSessionTTL),
	}
	if _, err := env.store.exchangeGrokAuthorizationCode(ctx, "w14d-tok", "https://cb?code=c&state=expected", "owner", ""); err == nil ||
		!strings.Contains(err.Error(), "access_token") {
		t.Fatalf("missing grok access token: %v", err)
	}
}

func TestW14dCryptoGCMOpenArm(t *testing.T) {
	sealed, err := encryptJSON(testSecret, map[string]any{"a": 1})
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(sealed, ":")
	var out map[string]any
	// A valid-size but wrong ciphertext/tag pair fails the GCM open.
	if err := decryptJSON(testSecret, "v1:"+parts[1]+":"+parts[2]+":"+parts[3][1:]+parts[3][:1], &out); err == nil {
		t.Fatal("tampered payload must fail the GCM open")
	}
	// The encrypt arm rejects values JSON cannot represent.
	if _, err := encryptJSON(testSecret, map[string]any{"fn": func() {}}); err == nil {
		t.Fatal("unmarshalable value must fail")
	}
}
