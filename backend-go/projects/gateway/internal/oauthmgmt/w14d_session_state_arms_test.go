// w14d_session_state_arms_test.go pins the state-mismatch and owner-mismatch
// diagnostics of the anthropic and grok session exchanges.
package oauthmgmt

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

type jsonRawBytes = json.RawMessage

func TestW14dAnthropicAndGrokSessionStateArms(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	// anthropic state mismatch and owner mismatch.
	env.store.sessions.entries["anthropic-oauth:sessions:w14d-state"] = sessionEntry{
		value:     mustJSON(t, anthropicOAuthSession{State: "expected", OwnerSystemAccountID: "owner"}),
		expiresAt: time.Now().Add(oauthSessionTTL),
	}
	if _, err := env.store.exchangeAnthropicAuthorizationCode(ctx, "w14d-state", "https://cb?code=c&state=wrong", "owner"); err == nil ||
		err.Error() != "Anthropic OAuth state 无效" {
		t.Fatalf("anthropic state mismatch: %v", err)
	}
	env.store.sessions.entries["anthropic-oauth:sessions:w14d-state"] = sessionEntry{
		value:     mustJSON(t, anthropicOAuthSession{State: "expected", OwnerSystemAccountID: "owner"}),
		expiresAt: time.Now().Add(oauthSessionTTL),
	}
	if _, err := env.store.exchangeAnthropicAuthorizationCode(ctx, "w14d-state", "https://cb?code=c&state=expected", "other"); err == nil ||
		err.Error() != "Anthropic OAuth session owner 归属无效" {
		t.Fatalf("anthropic owner mismatch: %v", err)
	}

	// grok state mismatch and owner mismatch.
	env.store.sessions.entries["grok-oauth:sessions:w14d-state"] = sessionEntry{
		value:     mustJSON(t, grokOAuthSession{State: "expected", OwnerSystemAccountID: "owner"}),
		expiresAt: time.Now().Add(oauthSessionTTL),
	}
	if _, err := env.store.exchangeGrokAuthorizationCode(ctx, "w14d-state", "https://cb?code=c&state=wrong", "owner"); err == nil ||
		err.Error() != "Grok OAuth state 无效" {
		t.Fatalf("grok state mismatch: %v", err)
	}
	env.store.sessions.entries["grok-oauth:sessions:w14d-state"] = sessionEntry{
		value:     mustJSON(t, grokOAuthSession{State: "expected", OwnerSystemAccountID: "owner"}),
		expiresAt: time.Now().Add(oauthSessionTTL),
	}
	if _, err := env.store.exchangeGrokAuthorizationCode(ctx, "w14d-state", "https://cb?code=c&state=expected", "other"); err == nil ||
		err.Error() != "Grok OAuth session owner 归属无效" {
		t.Fatalf("grok owner mismatch: %v", err)
	}

	// openai corrupted entry rides the unmarshal arm.
	env.store.sessions.entries["openai-oauth:sessions:w14d-bad"] = sessionEntry{
		value:     json.RawMessage("{broken"),
		expiresAt: time.Now().Add(oauthSessionTTL),
	}
	if _, err := env.store.exchangeOpenAIAuthorizationCode(ctx, "w14d-bad", "https://cb?code=c&state=s", "owner"); err == nil ||
		err.Error() != "OAuth 会话不存在或已过期" {
		t.Fatalf("openai broken entry: %v", err)
	}
}

func mustJSON(t *testing.T, value any) jsonRawBytes {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return jsonRawBytes(raw)
}
