// w14d_provider_sessions_test.go pins the per-provider session exchange
// diagnostics (absent, corrupted, state mismatch, owner mismatch, consumed)
// and the upstream token validation arms for the openai/anthropic/gemini/grok
// services.
package oauthmgmt

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestW14dOpenAISessionExchangeArms(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	// Corrupted session payload.
	env.store.sessions.set("openai-oauth:sessions", "w14d-bad", make(chan int), oauthSessionTTL)
	if _, err := env.store.exchangeOpenAIAuthorizationCode(ctx, "w14d-bad", "https://cb?code=c&state=s", "owner"); err == nil ||
		err.Error() != "OAuth 会话不存在或已过期" {
		t.Fatalf("corrupted session: %v", err)
	}

	// A live session consumed twice: the first exchange succeeds, the second
	// renders the consumed-session diagnostic.
	env.exchanger.respond = staticToken(`{"access_token":"a","expires_in":3600,"token_type":"Bearer","refresh_token":"r"}`)
	env.store.sessions.set("openai-oauth:sessions", "w14d-live", openAIOAuthSession{
		State: "expected", CodeVerifier: "v", RedirectURI: "https://cb", ClientID: "cid",
	}, oauthSessionTTL)
	callback := "https://cb?code=c&state=expected"
	if _, err := env.store.exchangeOpenAIAuthorizationCode(ctx, "w14d-live", callback, "owner"); err != nil {
		t.Fatalf("first exchange: %v", err)
	}
	if _, err := env.store.exchangeOpenAIAuthorizationCode(ctx, "w14d-live", callback, "owner"); err == nil ||
		err.Error() != "OAuth 会话不存在或已过期" {
		t.Fatalf("second exchange: %v", err)
	}

	// The upstream token validator rejects missing access tokens and bad
	// expires_in values.
	env.exchanger.respond = staticToken(`{"refresh_token":"r"}`)
	if _, err := env.store.exchangeOpenAIAuthorizationCode(ctx, "w14d-fresh", callback, "owner"); err == nil {
		t.Fatal("missing session must fail")
	}
	env.store.sessions.set("openai-oauth:sessions", "w14d-fresh", openAIOAuthSession{
		State: "expected", CodeVerifier: "v", RedirectURI: "https://cb", ClientID: "cid",
	}, oauthSessionTTL)
	if _, err := env.store.exchangeOpenAIAuthorizationCode(ctx, "w14d-fresh", callback, "owner"); err == nil ||
		!strings.Contains(err.Error(), "缺少访问令牌") {
		t.Fatalf("missing access token: %v", err)
	}
	env.exchanger.respond = staticToken(`{"access_token":"a","expires_in":0,"token_type":"Bearer"}`)
	if _, err := env.store.exchangeOpenAIAuthorizationCode(ctx, "w14d-fresh", callback, "owner"); err == nil ||
		!strings.Contains(err.Error(), "expires_in") {
		t.Fatalf("bad expires_in: %v", err)
	}
}

func TestW14dAnthropicSessionExchangeArms(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	// A live session consumed twice.
	env.exchanger.respond = func(_ int, call exchangeCall) (int, string) {
		if call.URL != AnthropicOAuthTokenURL {
			return http.StatusNotFound, `{}`
		}
		return http.StatusOK, `{"access_token":"a","refresh_token":"r","expires_in":3600,"token_type":"Bearer"}`
	}
	env.store.sessions.set("anthropic-oauth:sessions", "w14d-an", anthropicOAuthSession{State: "expected"}, oauthSessionTTL)
	callback := "https://cb?code=c&state=expected"
	if _, err := env.store.exchangeAnthropicAuthorizationCode(ctx, "w14d-an", callback, "owner"); err != nil {
		t.Fatalf("first anthropic exchange: %v", err)
	}
	if _, err := env.store.exchangeAnthropicAuthorizationCode(ctx, "w14d-an", callback, "owner"); err == nil ||
		err.Error() != "Anthropic OAuth 会话不存在或已过期" {
		t.Fatalf("second anthropic exchange: %v", err)
	}

	// The anthropic token validator requires an access token.
	env.exchanger.respond = staticToken(`{"refresh_token":"r"}`)
	env.store.sessions.set("anthropic-oauth:sessions", "w14d-an2", anthropicOAuthSession{State: "expected"}, oauthSessionTTL)
	if _, err := env.store.exchangeAnthropicAuthorizationCode(ctx, "w14d-an2", callback, "owner"); err == nil ||
		!strings.Contains(err.Error(), "access_token") {
		t.Fatalf("missing anthropic access token: %v", err)
	}
}

func TestW14dGeminiAndGrokSessionConsumption(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	// gemini: consumed session on the second pass.
	env.exchanger.respond = staticToken(`{"access_token":"a","refresh_token":"r","expires_in":3600,"token_type":"Bearer"}`)
	env.store.sessions.set("gemini-oauth:sessions", "w14d-gm", geminiOAuthSession{State: "expected"}, oauthSessionTTL)
	options := geminiExchangeOptions{SessionID: "w14d-gm", CallbackURL: "https://cb?code=c&state=expected", OwnerID: "owner"}
	if _, err := env.store.exchangeGeminiAuthorizationCode(ctx, options); err != nil {
		t.Fatalf("first gemini exchange: %v", err)
	}
	if _, err := env.store.exchangeGeminiAuthorizationCode(ctx, options); err == nil ||
		err.Error() != "Gemini OAuth 会话不存在或已过期" {
		t.Fatalf("second gemini exchange: %v", err)
	}

	// grok: consumed session on the second pass.
	env.store.sessions.set("grok-oauth:sessions", "w14d-gk", grokOAuthSession{State: "expected"}, oauthSessionTTL)
	grokCallback := "https://cb?code=c&state=expected"
	if _, err := env.store.exchangeGrokAuthorizationCode(ctx, "w14d-gk", grokCallback, "owner"); err != nil {
		t.Fatalf("first grok exchange: %v", err)
	}
	if _, err := env.store.exchangeGrokAuthorizationCode(ctx, "w14d-gk", grokCallback, "owner"); err == nil ||
		err.Error() != "Grok OAuth 会话不存在或已过期" {
		t.Fatalf("second grok exchange: %v", err)
	}
}
