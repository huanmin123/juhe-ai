package oauthrefresh

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Gemini OAuth type / tier / client resolution
// ---------------------------------------------------------------------------

func TestW9HGeminiAccountOAuthTypeInference(t *testing.T) {
	cases := []struct {
		name        string
		credentials map[string]any
		want        string
	}{
		{"explicit oauth_type wins", map[string]any{"oauth_type": "google_one"}, "google_one"},
		{"explicit unknown normalizes", map[string]any{"oauth_type": "bogus"}, "code_assist"},
		{"generativelanguage base url", map[string]any{"base_url": "https://generativelanguage.googleapis.com/x"}, "ai_studio"},
		{"project id", map[string]any{"project_id": "proj"}, "code_assist"},
		{"cloudcode base url", map[string]any{"base_url": "https://cloudcode-pa.googleapis.com"}, "code_assist"},
		{"foreign client id", map[string]any{"client_id": "my-client.apps.googleusercontent.com"}, "ai_studio"},
		{"builtin client id", map[string]any{"client_id": GeminiCLIOAuthClientID}, "code_assist"},
		{"empty credentials", map[string]any{}, "code_assist"},
		{"blank oauth type falls through", map[string]any{"oauth_type": "  "}, "code_assist"},
	}
	for _, tc := range cases {
		if got := GeminiAccountOAuthType(tc.credentials); got != tc.want {
			t.Fatalf("%s: got %q want %q", tc.name, got, tc.want)
		}
	}
}

func TestW9HCanonicalGeminiTierIDTable(t *testing.T) {
	cases := []struct {
		oauthType string
		raw       string
		want      string
	}{
		{"google_one", "AI-Premium", "google_ai_pro"},
		{"google_one", "Google-AI-Pro", "google_ai_pro"},
		{"google_one", "google_one_unlimited", "google_ai_ultra"},
		{"google_one", "Google-AI-Ultra", "google_ai_ultra"},
		{"google_one", "google_one_unknown", "google_one_unknown"},
		{"google_one", "Free", "google_one_free"},
		{"google_one", "google_one_basic", "google_one_free"},
		{"google_one", "google-one-standard", "google_one_free"},
		{"google_one", "google_one_free", "google_one_free"},
		{"google_one", "mystery", ""},
		{"ai_studio", "aistudio-paid", "aistudio_paid"},
		{"ai_studio", "PAID", "aistudio_paid"},
		{"ai_studio", "aistudio_free", "aistudio_free"},
		{"ai_studio", "FREE", "aistudio_free"},
		{"ai_studio", "other", ""},
		{"code_assist", "Enterprise", "gcp_enterprise"},
		{"code_assist", "ultra-tier", "gcp_enterprise"},
		{"code_assist", "gcp_enterprise", "gcp_enterprise"},
		{"code_assist", "legacy", "gcp_standard"},
		{"code_assist", "STANDARD", "gcp_standard"},
		{"code_assist", "pro", "gcp_standard"},
		{"code_assist", "standard_tier", "gcp_standard"},
		{"code_assist", "pro_tier", "gcp_standard"},
		{"code_assist", "unknown", ""},
		{"anything", "", ""},
	}
	for _, tc := range cases {
		if got := CanonicalGeminiTierID(tc.oauthType, tc.raw); got != tc.want {
			t.Fatalf("CanonicalGeminiTierID(%q,%q)=%q want %q", tc.oauthType, tc.raw, got, tc.want)
		}
	}
}

func TestW9HNormalizeGeminiOAuthTypeAndBaseURL(t *testing.T) {
	for _, value := range []string{"code_assist", "google_one", "ai_studio"} {
		if got := NormalizeGeminiOAuthType(value); got != value {
			t.Fatalf("NormalizeGeminiOAuthType(%q)=%q", value, got)
		}
	}
	for _, value := range []string{"", "  ", "Gemini", "x"} {
		if got := NormalizeGeminiOAuthType(value); got != "code_assist" {
			t.Fatalf("NormalizeGeminiOAuthType(%q)=%q want code_assist", value, got)
		}
	}
	if got := defaultGeminiBaseURL("ai_studio"); got != geminiOAuthDefaultBaseURL {
		t.Fatalf("ai_studio base url=%q", got)
	}
	if got := defaultGeminiBaseURL("code_assist"); got != geminiCLIDefaultBaseURL {
		t.Fatalf("code_assist base url=%q", got)
	}
	if got := defaultGeminiBaseURL("google_one"); got != geminiCLIDefaultBaseURL {
		t.Fatalf("google_one base url=%q", got)
	}
}

func TestW9HResolveGeminiOAuthClient(t *testing.T) {
	// code_assist/google_one: builtin pair, env secret override.
	t.Setenv("GEMINI_CLI_OAUTH_CLIENT_SECRET", "env-cli-secret")
	id, secret, err := resolveGeminiOAuthClient("code_assist", "", "")
	if err != nil || id != GeminiCLIOAuthClientID || secret != "env-cli-secret" {
		t.Fatalf("builtin client: id=%q err=%v", id, err)
	}
	t.Setenv("GEMINI_CLI_OAUTH_CLIENT_SECRET", "")
	id, secret, err = resolveGeminiOAuthClient("google_one", "", "")
	if err != nil || id != GeminiCLIOAuthClientID || secret != GeminiCLIOAuthClientSecret {
		t.Fatalf("builtin default secret: id=%q err=%v", id, err)
	}
	// ai_studio: explicit credentials win.
	id, secret, err = resolveGeminiOAuthClient("ai_studio", "cid", "csecret")
	if err != nil || id != "cid" || secret != "csecret" {
		t.Fatalf("ai_studio explicit: id=%q err=%v", id, err)
	}
	// ai_studio: env fallback.
	t.Setenv("GEMINI_OAUTH_CLIENT_ID", "env-cid")
	t.Setenv("GEMINI_OAUTH_CLIENT_SECRET", "env-csecret")
	id, secret, err = resolveGeminiOAuthClient("ai_studio", "", "")
	if err != nil || id != "env-cid" || secret != "env-csecret" {
		t.Fatalf("ai_studio env: id=%q err=%v", id, err)
	}
	// ai_studio: missing secret fails.
	t.Setenv("GEMINI_OAUTH_CLIENT_SECRET", "")
	if _, _, err := resolveGeminiOAuthClient("ai_studio", "cid", ""); err == nil {
		t.Fatal("ai_studio missing secret must fail")
	}
}

// ---------------------------------------------------------------------------
// Drive quota prober
// ---------------------------------------------------------------------------

type w9hFakeExchanger struct {
	response TokenHTTPResponse
	err      error
	request  TokenHTTPRequest
	calls    int
}

func (f *w9hFakeExchanger) Do(_ context.Context, request TokenHTTPRequest) (TokenHTTPResponse, error) {
	f.calls++
	f.request = request
	if f.err != nil {
		return TokenHTTPResponse{}, f.err
	}
	return f.response, nil
}

func TestW9HGoogleOneDriveQuotaProberArms(t *testing.T) {
	// Nil exchanger fails without a network call.
	if _, err := newGoogleOneDriveQuotaProber(nil, "").ProbeDriveQuota(context.Background(), "tok"); err == nil {
		t.Fatal("nil exchanger must fail")
	}
	// Transport error propagates.
	boom := errors.New("dial fail")
	prober := newGoogleOneDriveQuotaProber(&w9hFakeExchanger{err: boom}, " http://proxy ")
	probe, ok := prober.(googleOneDriveQuotaProber)
	if !ok {
		t.Fatalf("prober type %T", prober)
	}
	if probe.proxyURL != "http://proxy" {
		t.Fatalf("proxyURL normalized to %q", probe.proxyURL)
	}
	if _, err := probe.ProbeDriveQuota(context.Background(), "tok"); !errors.Is(err, boom) {
		t.Fatalf("transport error=%v", err)
	}
	// Non-2xx reads as upstream error with the raw body detail.
	failing := &w9hFakeExchanger{response: TokenHTTPResponse{StatusCode: 403, Body: `{"error":"denied"}`}}
	probe.exchanger = failing
	_, err := probe.ProbeDriveQuota(context.Background(), "tok")
	upstream, isUpstream := AsUpstreamError(err)
	if !isUpstream || !strings.Contains(upstream.Message, "denied") {
		t.Fatalf("non-2xx err=%v", err)
	}
	// Success: GET method, quota URL, bearer header, string/number coercion.
	success := &w9hFakeExchanger{response: TokenHTTPResponse{StatusCode: 200, Body: `{"storageQuota":{"limit":"107374182400","usage":2048}}`}}
	probe.exchanger = success
	quota, err := probe.ProbeDriveQuota(context.Background(), "tok")
	if err != nil {
		t.Fatalf("success err=%v", err)
	}
	if quota.Limit != 107374182400 || quota.Usage != 2048 {
		t.Fatalf("quota=%+v", quota)
	}
	if success.request.Method != http.MethodGet {
		t.Fatalf("method=%q", success.request.Method)
	}
	if success.request.URL != "https://www.googleapis.com/drive/v3/about?fields=storageQuota" {
		t.Fatalf("url=%q", success.request.URL)
	}
	if success.request.Headers["authorization"] != "Bearer tok" {
		t.Fatalf("auth header=%q", success.request.Headers["authorization"])
	}
	if success.request.Headers["user-agent"] != geminiCLIUserAgent {
		t.Fatalf("user-agent=%q", success.request.Headers["user-agent"])
	}
	// Missing quota fields read as zero.
	empty := &w9hFakeExchanger{response: TokenHTTPResponse{StatusCode: 200, Body: `{}`}}
	probe.exchanger = empty
	quota, err = probe.ProbeDriveQuota(context.Background(), "tok")
	if err != nil || quota.Limit != 0 || quota.Usage != 0 {
		t.Fatalf("empty quota=%+v err=%v", quota, err)
	}
	// The function adapter delegates.
	called := false
	var adapter GeminiDriveQuotaProber = geminiDriveQuotaProbeFunc(func(context.Context, string) (*GeminiDriveQuota, error) {
		called = true
		return &GeminiDriveQuota{Limit: 1}, nil
	})
	out, err := adapter.ProbeDriveQuota(context.Background(), "tok")
	if err != nil || !called || out.Limit != 1 {
		t.Fatalf("adapter out=%+v called=%v err=%v", out, called, err)
	}
}

// ---------------------------------------------------------------------------
// Gemini token request variants
// ---------------------------------------------------------------------------

func TestW9HRequestGeminiTokenErrorDetailVariants(t *testing.T) {
	cases := []struct {
		name     string
		status   int
		body     string
		contains []string
	}{
		{"error and description", 400, `{"error":"invalid_grant","error_description":"Token has been expired or revoked."}`, []string{"HTTP 400", "invalid_grant", "Token has been expired or revoked"}},
		{"error only", 400, `{"error":"invalid_client"}`, []string{"HTTP 400", "invalid_client"}},
		{"description only", 400, `{"error_description":"bad request"}`, []string{"HTTP 400", "bad request"}},
		{"plain body", 500, "upstream exploded", []string{"HTTP 500", "upstream exploded"}},
		{"same code and description", 400, `{"error":"x","error_description":"x"}`, []string{"HTTP 400", "x"}},
	}
	for _, tc := range cases {
		ex := &w9hFakeExchanger{response: TokenHTTPResponse{StatusCode: tc.status, Body: tc.body}}
		_, err := requestGeminiToken(context.Background(), ex, map[string]string{"grant_type": "refresh_token"}, geminiRequestOptions{}, defaultNow())
		upstream, isUpstream := AsUpstreamError(err)
		if !isUpstream {
			t.Fatalf("%s: err=%v not upstream", tc.name, err)
		}
		for _, want := range tc.contains {
			if !strings.Contains(upstream.Message, want) {
				t.Fatalf("%s: message %q missing %q", tc.name, upstream.Message, want)
			}
		}
	}
	// Missing access token on a 2xx body.
	ex := &w9hFakeExchanger{response: TokenHTTPResponse{StatusCode: 200, Body: `{"expires_in":3600}`}}
	if _, err := requestGeminiToken(context.Background(), ex, nil, geminiRequestOptions{}, defaultNow()); err == nil || !strings.Contains(err.Error(), "缺少 access_token") {
		t.Fatalf("missing access token err=%v", err)
	}
	// Clock-skew clamp: expires_in below the safety margin floors at 30s.
	ex = &w9hFakeExchanger{response: TokenHTTPResponse{StatusCode: 200, Body: `{"access_token":"at","expires_in":100}`}}
	info, err := requestGeminiToken(context.Background(), ex, nil, geminiRequestOptions{Scope: "fallback-scope"}, defaultNow())
	if err != nil {
		t.Fatalf("clamp err=%v", err)
	}
	expiresMs, ok := rfc3339Millis(info.ExpiresAt)
	if !ok {
		t.Fatalf("expires_at %q unparsable", info.ExpiresAt)
	}
	if delta := expiresMs - defaultNow().UnixMilli(); delta < 25_000 || delta > 31_000 {
		t.Fatalf("clamped expires delta=%dms", delta)
	}
	if info.ExpiresIn != 100 {
		t.Fatalf("ExpiresIn=%d", info.ExpiresIn)
	}
	// Payload scope wins; options scope is only the fallback.
	ex = &w9hFakeExchanger{response: TokenHTTPResponse{StatusCode: 200, Body: `{"access_token":"at","scope":"payload-scope"}`}}
	info, err = requestGeminiToken(context.Background(), ex, nil, geminiRequestOptions{Scope: "fallback-scope"}, defaultNow())
	if err != nil || info.Scope != "payload-scope" {
		t.Fatalf("payload scope=%q err=%v", info.Scope, err)
	}
	// Missing expires_in keeps zero values.
	ex = &w9hFakeExchanger{response: TokenHTTPResponse{StatusCode: 200, Body: `{"access_token":"at"}`}}
	info, err = requestGeminiToken(context.Background(), ex, nil, geminiRequestOptions{}, defaultNow())
	if err != nil || info.ExpiresIn != 0 || info.ExpiresAt != "" {
		t.Fatalf("no expires_in info=%+v err=%v", info, err)
	}
}

func TestW9HRefreshGeminiTokenClientValidation(t *testing.T) {
	if _, err := RefreshGeminiToken(context.Background(), &w9hFakeExchanger{}, "  ", GeminiCredentialFallback{}, defaultNow()); err == nil || !strings.Contains(err.Error(), "Gemini Refresh Token 不能为空") {
		t.Fatalf("empty refresh token err=%v", err)
	}
	t.Setenv("GEMINI_OAUTH_CLIENT_ID", "")
	t.Setenv("GEMINI_OAUTH_CLIENT_SECRET", "")
	_, err := RefreshGeminiToken(context.Background(), &w9hFakeExchanger{}, "rt", GeminiCredentialFallback{OAuthType: "ai_studio"}, defaultNow())
	if err == nil || !strings.Contains(err.Error(), "Client ID 和 Client Secret") {
		t.Fatalf("ai_studio missing client err=%v", err)
	}
	// code_assist path resolves the builtin client and canonicalizes tiers.
	ex := &w9hFakeExchanger{response: TokenHTTPResponse{StatusCode: 200, Body: `{"access_token":"at","expires_in":3600}`}}
	info, err := RefreshGeminiToken(context.Background(), ex, "rt", GeminiCredentialFallback{OAuthType: "code_assist", TierID: "PRO"}, defaultNow())
	if err != nil {
		t.Fatalf("code_assist refresh err=%v", err)
	}
	if info.TierID != "gcp_standard" || info.OAuthType != "code_assist" {
		t.Fatalf("info=%+v", info)
	}
	if got := ex.request.Headers["content-type"]; got != "application/x-www-form-urlencoded" {
		t.Fatalf("content-type=%q", got)
	}
	body := ex.request.Body
	if !strings.Contains(body, "client_id=") || !strings.Contains(body, "client_secret=") {
		t.Fatalf("form body=%q", body)
	}
}

// ---------------------------------------------------------------------------
// Anthropic token request variants
// ---------------------------------------------------------------------------

func TestW9HRequestAnthropicTokenArms(t *testing.T) {
	refresh := func(body string, status int) (*AnthropicTokenInfo, error) {
		ex := &w9hFakeExchanger{response: TokenHTTPResponse{StatusCode: status, Body: body}}
		return requestAnthropicToken(context.Background(), ex, map[string]string{}, defaultNow(), "")
	}
	if _, err := refresh(`{"error_description":"expired"}`, 400); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("description detail err=%v", err)
	}
	if _, err := refresh(`{"error":"invalid_grant"}`, 401); err == nil || !strings.Contains(err.Error(), "invalid_grant") {
		t.Fatalf("error detail err=%v", err)
	}
	if _, err := refresh("raw failure", 500); err == nil || !strings.Contains(err.Error(), "raw failure") {
		t.Fatalf("raw body detail err=%v", err)
	}
	if _, err := refresh(`{"expires_in":3600}`, 200); err == nil || !strings.Contains(err.Error(), "缺少 access_token") {
		t.Fatalf("missing access token err=%v", err)
	}
	// Full success payload with account/organization extraction.
	info, err := refresh(`{"access_token":"at","refresh_token":"rt","expires_in":3600,"scope":"s","token_type":"bearer",
		"account":{"email_address":"a@b.c","uuid":"acc-1"},"organization":{"uuid":"org-1"}}`, 200)
	if err != nil {
		t.Fatalf("success err=%v", err)
	}
	if info.Email != "a@b.c" || info.AccountID != "acc-1" || info.OrganizationID != "org-1" || info.Scope != "s" || info.TokenType != "bearer" {
		t.Fatalf("info=%+v", info)
	}
	if info.ExpiresIn != 3600 || info.ExpiresAt == "" {
		t.Fatalf("expiry fields=%+v", info)
	}
	if info.ClientID != AnthropicOAuthClientID {
		t.Fatalf("default client id=%q", info.ClientID)
	}
	// Missing expires_in keeps zero expiry but still succeeds.
	info, err = refresh(`{"access_token":"at"}`, 200)
	if err != nil || info.ExpiresIn != 0 || info.ExpiresAt != "" {
		t.Fatalf("no expiry info=%+v err=%v", info, err)
	}
	// JSON body + axios user-agent on the wire.
	ex := &w9hFakeExchanger{response: TokenHTTPResponse{StatusCode: 200, Body: `{"access_token":"at"}`}}
	if _, err := RefreshAnthropicToken(context.Background(), ex, "rt", "", defaultNow(), ""); err != nil {
		t.Fatalf("refresh err=%v", err)
	}
	if got := ex.request.Headers["user-agent"]; got != "axios/1.13.6" {
		t.Fatalf("user-agent=%q", got)
	}
	if !strings.Contains(ex.request.Body, `"refresh_token":"rt"`) {
		t.Fatalf("json body=%q", ex.request.Body)
	}
	if err := func() error {
		_, err := RefreshAnthropicToken(context.Background(), &w9hFakeExchanger{}, "", "", defaultNow(), "")
		return err
	}(); err == nil || !strings.Contains(err.Error(), "Anthropic Refresh Token 不能为空") {
		t.Fatalf("empty refresh err=%v", err)
	}
}

func TestW9HBuildAnthropicOAuthCredentialsVariants(t *testing.T) {
	full := BuildAnthropicOAuthCredentials(&AnthropicTokenInfo{
		AccessToken: "at", RefreshToken: "rt", ExpiresAt: isoMillis(defaultNow()), Email: "e", AccountID: "a",
		OrganizationID: "o", Scope: "s", TokenType: "bearer", ClientID: "cid",
	}, "fallback")
	for _, key := range []string{"access_token", "refresh_token", "expires_at", "email", "account_id", "organization_id", "scope", "token_type", "client_id", "base_url"} {
		if _, ok := full[key]; !ok {
			t.Fatalf("full credentials missing %q: %+v", key, full)
		}
	}
	if full["base_url"] != anthropicOAuthBaseURL {
		t.Fatalf("base_url=%v", full["base_url"])
	}
	// Missing refresh token falls back; optional fields drop.
	minimal := BuildAnthropicOAuthCredentials(&AnthropicTokenInfo{AccessToken: "at", ClientID: "cid"}, "fallback-rt")
	if minimal["refresh_token"] != "fallback-rt" {
		t.Fatalf("fallback refresh=%v", minimal["refresh_token"])
	}
	if _, ok := minimal["expires_at"]; ok {
		t.Fatalf("minimal must skip expires_at: %+v", minimal)
	}
	// Empty info refresh token and empty fallback drops the key entirely.
	dropped := BuildAnthropicOAuthCredentials(&AnthropicTokenInfo{AccessToken: "at"}, "")
	if _, ok := dropped["refresh_token"]; ok {
		t.Fatalf("refresh_token must drop: %+v", dropped)
	}
}

// ---------------------------------------------------------------------------
// Grok token variants
// ---------------------------------------------------------------------------

func TestW9HRequestGrokTokenEntitlementAndDefaults(t *testing.T) {
	refresh := func(body string, status int) (*GrokTokenInfo, error) {
		ex := &w9hFakeExchanger{response: TokenHTTPResponse{StatusCode: status, Body: body}}
		return requestGrokToken(context.Background(), ex, map[string]string{}, "cid", defaultNow(), "")
	}
	// 403 with explicit denial keeps 403.
	_, err := refresh(`{"error":"access_denied"}`, 403)
	upstream, isUpstream := AsUpstreamError(err)
	if !isUpstream || upstream.StatusCode != 403 {
		t.Fatalf("entitlement 403 err=%v", err)
	}
	// 403 with body-text denial keeps 403.
	_, err = refresh("no active grok subscription", 403)
	if upstream, isUpstream = AsUpstreamError(err); !isUpstream || upstream.StatusCode != 403 {
		t.Fatalf("body denial 403 err=%v", err)
	}
	// 403 without denial degrades to 502.
	_, err = refresh(`{"error":"other"}`, 403)
	if upstream, isUpstream = AsUpstreamError(err); !isUpstream || upstream.StatusCode != 502 {
		t.Fatalf("plain 403 err=%v", err)
	}
	// Other statuses use the shared envelope.
	_, err = refresh(`{"error_description":"nope"}`, 429)
	if upstream, isUpstream = AsUpstreamError(err); !isUpstream || !strings.Contains(upstream.Message, "429") {
		t.Fatalf("429 err=%v", err)
	}
	// Missing access token.
	if _, err := refresh(`{"expires_in":10}`, 200); err == nil {
		t.Fatal("missing access token must fail")
	}
	// Defaults: 6h TTL, Bearer type, JWT claim merge.
	jwt := "h." + encodeBase64URL([]byte(`{"email":"g@x.y","sub":"sub-1","team_id":"team","subscription_tier":"pro","entitlement_status":"active"}`)) + ".s"
	info, err := refresh(fmt.Sprintf(`{"access_token":"%s","refresh_token":"rt2"}`, jwt), 200)
	if err != nil {
		t.Fatalf("success err=%v", err)
	}
	if info.ExpiresIn != grokDefaultTokenTTL || info.TokenType != "Bearer" {
		t.Fatalf("defaults info=%+v", info)
	}
	if info.Email != "g@x.y" || info.Subject != "sub-1" || info.TeamID != "team" || info.SubscriptionTier != "pro" || info.EntitlementStatus != "active" {
		t.Fatalf("claims info=%+v", info)
	}
	if info.ExpiresAt == "" || info.ClientID != "cid" {
		t.Fatalf("info=%+v", info)
	}
	// Missing expires_in/token_type fall back through toGrokTokenInfo defaults.
	info, err = refresh(`{"access_token":"plain-at"}`, 200)
	if err != nil || info.ExpiresIn != grokDefaultTokenTTL || info.TokenType != "Bearer" {
		t.Fatalf("plain info=%+v err=%v", info, err)
	}
}

func TestW9HToGrokTokenInfoDefaults(t *testing.T) {
	info := toGrokTokenInfo(grokRawToken{AccessToken: "at", ExpiresIn: -5}, "", defaultNow())
	if info.ExpiresIn != grokDefaultTokenTTL || info.ClientID != GrokOAuthClientID || info.TokenType != "Bearer" {
		t.Fatalf("info=%+v", info)
	}
	claims := mergeJWTClaims(encodeBase64URL([]byte(`{"sub":"s1"}`)), info.AccessToken)
	_ = claims
}

func TestW9HRefreshGrokTokenArms(t *testing.T) {
	if _, err := RefreshGrokToken(context.Background(), &w9hFakeExchanger{}, "", "", defaultNow(), ""); err == nil || !strings.Contains(err.Error(), "Grok Refresh Token 不能为空") {
		t.Fatalf("empty refresh err=%v", err)
	}
	// Missing rotated refresh token keeps the input one; empty client id falls
	// back to the builtin.
	ex := &w9hFakeExchanger{response: TokenHTTPResponse{StatusCode: 200, Body: `{"access_token":"at"}`}}
	info, err := RefreshGrokToken(context.Background(), ex, "rt-in", "", defaultNow(), "")
	if err != nil {
		t.Fatalf("refresh err=%v", err)
	}
	if info.RefreshToken != "rt-in" || info.ClientID != GrokOAuthClientID {
		t.Fatalf("info=%+v", info)
	}
	if !strings.Contains(ex.request.Body, "client_id="+GrokOAuthClientID) {
		t.Fatalf("body=%q", ex.request.Body)
	}
	if got := ex.request.Headers["user-agent"]; got != "sub2api-grok-oauth/1.0" {
		t.Fatalf("user-agent=%q", got)
	}
}

func TestW9HBuildGrokOAuthCredentialsVariants(t *testing.T) {
	full := BuildGrokOAuthCredentials(&GrokTokenInfo{
		AccessToken: "at", ExpiresAt: isoMillis(defaultNow()), TokenType: "Bearer", ClientID: "cid",
		RefreshToken: "rt", IDToken: "idt", Scope: "s", Email: "e", Subject: "sub",
		TeamID: "team", SubscriptionTier: "pro", EntitlementStatus: "active",
	}, "unused")
	for _, key := range []string{"access_token", "expires_at", "token_type", "client_id", "base_url", "refresh_token", "id_token", "scope", "email", "sub", "team_id", "subscription_tier", "entitlement_status"} {
		if _, ok := full[key]; !ok {
			t.Fatalf("missing %q in %+v", key, full)
		}
	}
	if full["base_url"] != GrokOAuthBaseURL {
		t.Fatalf("base_url=%v", full["base_url"])
	}
	// Fallback refresh token applies when the response omitted one.
	fallback := BuildGrokOAuthCredentials(&GrokTokenInfo{AccessToken: "at", TokenType: "Bearer", ClientID: "cid"}, "fb-rt")
	if fallback["refresh_token"] != "fb-rt" {
		t.Fatalf("fallback refresh=%v", fallback["refresh_token"])
	}
}

// ---------------------------------------------------------------------------
// OpenAI token request arms
// ---------------------------------------------------------------------------

func TestW9HRequestOpenAITokenDetailFallbacks(t *testing.T) {
	request := func(body string, status int) (*OpenAITokenInfo, error) {
		ex := &w9hFakeExchanger{response: TokenHTTPResponse{StatusCode: status, Body: body}}
		return requestOpenAIToken(context.Background(), ex, map[string]string{"client_id": "cid"}, defaultNow(), "")
	}
	if _, err := request("raw down", 503); err == nil || !strings.Contains(err.Error(), "raw down") {
		t.Fatalf("raw body detail err=%v", err)
	}
	if _, err := request(`{"error":"server_error"}`, 500); err == nil || !strings.Contains(err.Error(), "server_error") {
		t.Fatalf("error detail err=%v", err)
	}
	if _, err := request(`{"expires_in":3600}`, 200); err == nil || !strings.Contains(err.Error(), "缺少访问令牌") {
		t.Fatalf("missing access token err=%v", err)
	}
	if _, err := request(`{"access_token":"at"}`, 200); err == nil || !strings.Contains(err.Error(), "expires_in") {
		t.Fatalf("bad expires_in err=%v", err)
	}
	// JWT enrichment from id_token and access_token claims.
	// Full compact JWS shape: header.payload.signature.
	makeJWT := func(claims string) string {
		return "h." + encodeBase64URL([]byte(claims)) + ".s"
	}
	body := fmt.Sprintf(`{"access_token":"%s","id_token":"%s","refresh_token":"rt","expires_in":3600}`,
		makeJWT(`{"email":"from-access@x.y","https://api.openai.com/auth":{"chatgpt_account_id":"acct-access"}}`),
		makeJWT(`{"email":"from-id@x.y","https://api.openai.com/auth":{"chatgpt_account_id":"acct-id","chatgpt_user_id":"u-id","chatgpt_plan_type":"pro"}}`))
	info, err := request(body, 200)
	if err != nil {
		t.Fatalf("enrich err=%v", err)
	}
	if info.Email != "from-id@x.y" {
		t.Fatalf("id token email wins: %q", info.Email)
	}
	if info.AccountID != "acct-id" || info.ChatGPTUserID != "u-id" || info.PlanType != "pro" {
		t.Fatalf("info=%+v", info)
	}
	if info.ExpiresAt == "" || info.ClientID != "cid" {
		t.Fatalf("info=%+v", info)
	}
	// Transport error propagates unwrapped.
	boom := errors.New("socket closed")
	ex := &w9hFakeExchanger{err: boom}
	if _, err := requestOpenAIToken(context.Background(), ex, nil, defaultNow(), ""); !errors.Is(err, boom) {
		t.Fatalf("transport err=%v", err)
	}
}

func TestW9HRefreshOpenAITokenValidation(t *testing.T) {
	if _, err := RefreshOpenAIToken(context.Background(), &w9hFakeExchanger{}, " ", "", defaultNow(), ""); err == nil || !strings.Contains(err.Error(), "刷新令牌不能为空") {
		t.Fatalf("empty refresh err=%v", err)
	}
	ex := &w9hFakeExchanger{response: TokenHTTPResponse{StatusCode: 200, Body: `{"access_token":"at","expires_in":60}`}}
	info, err := RefreshOpenAIToken(context.Background(), ex, "rt", "", defaultNow(), "")
	if err != nil {
		t.Fatalf("refresh err=%v", err)
	}
	if info.ClientID != OpenAIOAuthClientID {
		t.Fatalf("default client=%q", info.ClientID)
	}
	if !strings.Contains(ex.request.Body, "scope="+strings.ReplaceAll(OpenAIOAuthRefreshScopes, " ", "+")) {
		t.Fatalf("scope form=%q", ex.request.Body)
	}
	// BuildOpenAIOAuthCredentials round trip incl. fallback refresh token.
	// The token response omitted refresh_token, so the key drops with an empty
	// fallback.
	credentials := BuildOpenAIOAuthCredentials(info, "")
	for _, key := range []string{"access_token", "expires_at", "client_id", "base_url"} {
		if _, ok := credentials[key]; !ok {
			t.Fatalf("missing %q in %v", key, credentials)
		}
	}
	if credentials["base_url"] != openAIOAuthBaseURL {
		t.Fatalf("base_url=%v", credentials["base_url"])
	}
	minimal := BuildOpenAIOAuthCredentials(&OpenAITokenInfo{AccessToken: "at", ExpiresAt: isoMillis(defaultNow()), ClientID: "cid"}, "fb-rt")
	if minimal["refresh_token"] != "fb-rt" {
		t.Fatalf("fallback refresh=%v", minimal["refresh_token"])
	}
	if _, ok := minimal["email"]; ok {
		t.Fatalf("optional email must drop: %v", minimal)
	}
	if minimal["refresh_token"] != "fb-rt" {
		t.Fatalf("fallback refresh=%v", minimal["refresh_token"])
	}
}

// ---------------------------------------------------------------------------
// HTTP token exchanger (loopback httptest only)
// ---------------------------------------------------------------------------

func TestW9HHTTPTokenExchangerDoArms(t *testing.T) {
	var seenMethod, seenHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenMethod = r.Method
		seenHeader = r.Header.Get("x-proof")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer server.Close()

	exchanger := NewHTTPTokenExchanger()
	// Default POST + header propagation + status passthrough.
	response, err := exchanger.Do(context.Background(), TokenHTTPRequest{
		URL:     server.URL,
		Headers: map[string]string{"x-proof": "1"},
		Body:    "a=1",
	})
	if err != nil || response.StatusCode != 200 || response.Body != `{"ok":true}` {
		t.Fatalf("post response=%+v err=%v", response, err)
	}
	if seenMethod != http.MethodPost || seenHeader != "1" {
		t.Fatalf("method=%q header=%q", seenMethod, seenHeader)
	}
	// Explicit method.
	response, err = exchanger.Do(context.Background(), TokenHTTPRequest{URL: server.URL, Method: http.MethodGet})
	if err != nil || response.StatusCode != 200 || seenMethod != http.MethodGet {
		t.Fatalf("get response=%+v err=%v method=%q", response, err, seenMethod)
	}
	// Nil context falls back to background.
	response, err = exchanger.Do(nil, TokenHTTPRequest{URL: server.URL})
	if err != nil || response.StatusCode != 200 {
		t.Fatalf("nil ctx response=%+v err=%v", response, err)
	}
	// Invalid URL fails the request build.
	if _, err := exchanger.Do(context.Background(), TokenHTTPRequest{URL: "://bad"}); err == nil {
		t.Fatal("invalid URL must fail")
	}
	// Timeout<=0 uses the default bound (observable only as no-error).
	if _, err := exchanger.Do(context.Background(), TokenHTTPRequest{URL: server.URL, Timeout: -1}); err != nil {
		t.Fatalf("default timeout err=%v", err)
	}
}

func TestW9HHTTPTokenExchangerOversizedBodyRejected(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Stream a chunked body well past the cap: the client cuts the read at
		// cap+1 and rejects the payload.
		chunk := strings.Repeat("x", 4096)
		for i := 0; i < tokenResponseMaxBytes/4096+2; i++ {
			_, _ = io.WriteString(w, chunk)
		}
	}))
	defer server.Close()
	exchanger := NewHTTPTokenExchanger()
	_, err := exchanger.Do(context.Background(), TokenHTTPRequest{URL: server.URL})
	if err == nil || !strings.Contains(err.Error(), "OAuth 令牌响应体过大") {
		t.Fatalf("oversized err=%v", err)
	}
}

func TestW9HHTTPTokenExchangerBodyReadError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "1024")
		_, _ = io.WriteString(w, "short")
		panic(http.ErrAbortHandler)
	}))
	defer server.Close()
	exchanger := NewHTTPTokenExchanger()
	_, err := exchanger.Do(context.Background(), TokenHTTPRequest{URL: server.URL})
	if err == nil {
		t.Fatal("aborted body must fail")
	}
}

func TestW9HHTTPTokenExchangerNilClientFallback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "{}")
	}))
	defer server.Close()
	// nil Client falls back to a fresh http.Client for direct dials.
	exchanger := &HTTPTokenExchanger{}
	response, err := exchanger.Do(context.Background(), TokenHTTPRequest{URL: server.URL})
	if err != nil || response.StatusCode != 200 {
		t.Fatalf("nil client response=%+v err=%v", response, err)
	}
}

// ---------------------------------------------------------------------------
// Protocol payload helpers
// ---------------------------------------------------------------------------

func TestW9HParseTokenPayloadArms(t *testing.T) {
	if got := parseTokenPayload("   "); len(got) != 0 {
		t.Fatalf("blank payload=%v", got)
	}
	raw := parseTokenPayload("not json")
	if text, ok := raw["raw"].(string); !ok || text != "not json" {
		t.Fatalf("raw payload=%v", raw)
	}
	if got := parseTokenPayload("null"); got["raw"] != "null" {
		t.Fatalf("null payload=%v", got)
	}
	if got := parseTokenPayload(`{"a":1}`)["a"].(float64); got != 1 {
		t.Fatalf("json payload=%v", got)
	}
}

func TestW9HDecodeAndMergeJWTClaims(t *testing.T) {
	if got := decodeJWTClaims(""); len(got) != 0 {
		t.Fatalf("empty claims=%v", got)
	}
	if got := decodeJWTClaims("only-one-part"); len(got) != 0 {
		t.Fatalf("one part=%v", got)
	}
	if got := decodeJWTClaims("a.!!!bad-base64!!!"); len(got) != 0 {
		t.Fatalf("bad base64=%v", got)
	}
	invalidJSON := encodeBase64URL([]byte("no json"))
	if got := decodeJWTClaims("h." + invalidJSON + ".s"); len(got) != 0 {
		t.Fatalf("bad json=%v", got)
	}
	claims := decodeJWTClaims("h." + encodeBase64URL([]byte(`{"sub":"s1","n":5}`)) + ".sig")
	if claims["sub"] != "s1" || claims["n"].(float64) != 5 {
		t.Fatalf("claims=%v", claims)
	}
	first := "h." + encodeBase64URL([]byte(`{"email":"first@x.y","plan":"pro"}`)) + ".s"
	second := "h." + encodeBase64URL([]byte(`{"email":"second@x.y","tier":9}`)) + ".s"
	merged := mergeJWTClaims(first, second)
	if merged["email"] != "first@x.y" {
		t.Fatalf("earlier wins: %v", merged)
	}
	// Non-string existing values (normalizeText=="") are overwritten by later tokens.
	if merged["tier"].(float64) != 9 {
		t.Fatalf("non-string overwritten: %v", merged)
	}
	if got := mergeJWTClaims(); len(got) != 0 {
		t.Fatalf("no tokens=%v", got)
	}
}

func TestW9HProtocolMiscHelpers(t *testing.T) {
	if got := normalizeText("  x  "); got != "x" {
		t.Fatalf("normalizeText=%q", got)
	}
	if got := normalizeText(42); got != "" {
		t.Fatalf("normalizeText(42)=%q", got)
	}
	if _, ok := finitePositiveInt(float64(3.9)); !ok {
		t.Fatal("finite positive must parse")
	}
	if _, ok := finitePositiveInt(0.0); ok {
		t.Fatal("zero is not positive")
	}
	if _, ok := finitePositiveInt("5"); ok {
		t.Fatal("string is not a number")
	}
	if got := itoa(0); got != "0" {
		t.Fatalf("itoa(0)=%q", got)
	}
	if got := itoa(12345); got != "12345" {
		t.Fatalf("itoa(12345)=%q", got)
	}
	body := encodeForm(map[string]string{"b": "y", "a": "x&z"})
	if !strings.Contains(body, "a=x%26z") || !strings.HasPrefix(body, "a=") {
		t.Fatalf("form=%q", body)
	}
	request := formRequest("https://token", map[string]string{"k": "v"})
	if request.Headers["accept"] != "application/json" || request.Headers["content-length"] != itoa(len(request.Body)) {
		t.Fatalf("form request=%+v", request)
	}
	jsonReq := jsonRequest("https://token", map[string]string{"k": "v"})
	if jsonReq.Headers["content-type"] != "application/json" || jsonReq.Body != `{"k":"v"}` {
		t.Fatalf("json request=%+v", jsonReq)
	}
	// exchange with nil context still delegates.
	called := false
	_, _ = exchange(nil, ExchangerFunc(func(context.Context, TokenHTTPRequest) (TokenHTTPResponse, error) {
		called = true
		return TokenHTTPResponse{}, nil
	}), TokenHTTPRequest{})
	if !called {
		t.Fatal("exchange must call the exchanger")
	}
	if message := (&UpstreamError{Message: "m"}).Error(); message != "m" {
		t.Fatalf("UpstreamError.Error=%q", message)
	}
	if _, ok := AsUpstreamError(errors.New("plain")); ok {
		t.Fatal("plain error is not upstream")
	}
	// finiteNonNegativeInt64 coercion table.
	cases := []struct {
		value any
		want  int64
	}{
		{float64(12.9), 12}, {float64(-1), 0}, {"42", 42}, {" -7 ", 0}, {"abc", 0}, {true, 1}, {false, 0}, {nil, 0}, {map[string]any{}, 0},
	}
	for _, tc := range cases {
		if got := finiteNonNegativeInt64(tc.value); got != tc.want {
			t.Fatalf("finiteNonNegativeInt64(%v)=%d want %d", tc.value, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Refresh predicate helpers
// ---------------------------------------------------------------------------

func TestW9HShouldPreRefreshAccessToken(t *testing.T) {
	now := defaultNow()
	fresh := map[string]any{"access_token": "at", "expires_at": isoMillis(now.Add(time.Hour))}
	if shouldPreRefreshAccessToken(fresh, now, DefaultRefreshLeadSeconds*1000) {
		t.Fatal("fresh token is not due")
	}
	withinLead := map[string]any{"access_token": "at", "expires_at": isoMillis(now.Add(60 * time.Second))}
	if !shouldPreRefreshAccessToken(withinLead, now, DefaultRefreshLeadSeconds*1000) {
		t.Fatal("inside the lead window is due")
	}
	if !shouldPreRefreshAccessToken(map[string]any{"expires_at": isoMillis(now.Add(time.Hour))}, now, 0) {
		t.Fatal("missing access token is due")
	}
	if !shouldPreRefreshAccessToken(map[string]any{"access_token": "at", "expires_at": "bogus"}, now, 0) {
		t.Fatal("unparsable expiry is due")
	}
	if !shouldPreRefreshAccessToken(map[string]any{"access_token": "at"}, now, 0) {
		t.Fatal("missing expiry is due")
	}
}

func TestW9HAccessTokenExpiredOrMissing(t *testing.T) {
	now := defaultNow()
	if !accessTokenExpiredOrMissing(map[string]any{}, now) {
		t.Fatal("missing access token reads expired")
	}
	if accessTokenExpiredOrMissing(map[string]any{"access_token": "at", "expires_at": isoMillis(now.Add(time.Minute))}, now) {
		t.Fatal("future expiry reads valid")
	}
	if !accessTokenExpiredOrMissing(map[string]any{"access_token": "at", "expires_at": isoMillis(now.Add(-time.Minute))}, now) {
		t.Fatal("past expiry reads expired")
	}
	if !accessTokenExpiredOrMissing(map[string]any{"access_token": "at", "expires_at": 123}, now) {
		t.Fatal("non-string expiry reads expired")
	}
	if !accessTokenExpiredOrMissing(map[string]any{"access_token": "at", "expires_at": "junk"}, now) {
		t.Fatal("unparsable expiry reads expired")
	}
}

func TestW9HCredentialExpiryHelpers(t *testing.T) {
	// parseCredentialExpiresAt
	if value, err := parseCredentialExpiresAt(map[string]any{"expires_at": " "}); err != nil || value != nil {
		t.Fatalf("blank expiry value=%v err=%v", value, err)
	}
	if _, err := parseCredentialExpiresAt(map[string]any{"expires_at": "junk"}); err == nil {
		t.Fatal("junk expiry must fail")
	}
	valid, err := parseCredentialExpiresAt(map[string]any{"expires_at": isoMillis(defaultNow())})
	if err != nil || valid == nil {
		t.Fatalf("valid expiry=%v err=%v", valid, err)
	}
	// credentialExpiresAt
	if got := credentialExpiresAt(map[string]any{"expires_at": 42}); got != "" {
		t.Fatalf("non-string expiry=%q", got)
	}
	if got := credentialExpiresAt(map[string]any{"expires_at": " 2026-01-01T00:00:00Z "}); got != "2026-01-01T00:00:00Z" {
		t.Fatalf("trimmed expiry=%q", got)
	}
	if got := credentialExpiresAt(map[string]any{}); got != "" {
		t.Fatalf("absent expiry=%q", got)
	}
	// credentialExpiresAtLater
	later := map[string]any{"expires_at": isoMillis(defaultNow().Add(time.Hour))}
	earlier := map[string]any{"expires_at": isoMillis(defaultNow())}
	if !credentialExpiresAtLater(later, earlier) {
		t.Fatal("later expiry must report true")
	}
	if credentialExpiresAtLater(earlier, later) {
		t.Fatal("earlier expiry must report false")
	}
	if !credentialExpiresAtLater(later, map[string]any{"access_token": "at"}) {
		t.Fatal("absent current expiry defers to next")
	}
	if credentialExpiresAtLater(later, map[string]any{"expires_at": "junk"}) {
		t.Fatal("unparsable current expiry must report false")
	}
	if credentialExpiresAtLater(map[string]any{"expires_at": 1}, earlier) {
		t.Fatal("unparsable next must report false")
	}
	// credentialsChanged
	base := map[string]any{"access_token": "a", "refresh_token": "r", "expires_at": isoMillis(defaultNow())}
	if credentialsChanged(base, base) {
		t.Fatal("identical credentials unchanged")
	}
	expiryOnly := map[string]any{"access_token": "a", "refresh_token": "r", "expires_at": isoMillis(defaultNow().Add(time.Second))}
	if !credentialsChanged(base, expiryOnly) {
		t.Fatal("expiry delta counts as changed")
	}
	// mergeCredentials overrides token keys.
	merged := mergeCredentials(base, map[string]any{"access_token": "z"})
	if merged["access_token"] != "z" || merged["refresh_token"] != "r" {
		t.Fatalf("merged=%v", merged)
	}
}

func TestW9HSanitizedErrorMessageAndStoppedMessage(t *testing.T) {
	if got := sanitizedErrorMessage(&UpstreamError{Message: "up verbatim"}); got != "up verbatim" {
		t.Fatalf("upstream message=%q", got)
	}
	if got := sanitizedErrorMessage(&LocalConfigurationError{Message: "local verbatim"}); got != "local verbatim" {
		t.Fatalf("local message=%q", got)
	}
	if got := sanitizedErrorMessage(errors.New("secret internal")); got != "系统内部错误，请稍后重试" {
		t.Fatalf("internal message=%q", got)
	}
	long := strings.Repeat("长", 300)
	got := sanitizedErrorMessage(&LocalConfigurationError{Message: long})
	if len([]rune(got)) != 240 || !strings.HasSuffix(got, "...") {
		t.Fatalf("truncated length=%d suffix=%q", len([]rune(got)), got[len(got)-3:])
	}
	message := openAIOAuthTokenRefreshLocalConfigurationStoppedMessage(3, strings.Repeat("x", 2000))
	if len([]rune(message)) != 1000 {
		t.Fatalf("stopped message length=%d", len([]rune(message)))
	}
	short := openAIOAuthTokenRefreshLocalConfigurationStoppedMessage(2, "short")
	if !strings.Contains(short, "连续 2 次") || !strings.Contains(short, "最后本地错误：short") {
		t.Fatalf("stopped message=%q", short)
	}
	if truncateRunes("hello", 3) != "hel" || truncateRunes("hi", 5) != "hi" {
		t.Fatal("truncateRunes bounds")
	}
}

func TestW9HAccountPredicateHelpers(t *testing.T) {
	stopped := &RotationAccount{ProviderCode: ProviderGPT, Type: AccountTypeOAuth, Status: "error", LastErrorCode: OpenAIOAuthTokenRefreshLocalConfigurationInvalidCode}
	if isExistingOpenAIOAuthAccountForRefresh(stopped) {
		t.Fatal("terminal stopped account must be excluded")
	}
	if isExistingOpenAIOAuthAccountForRefresh(nil) {
		t.Fatal("nil account excluded")
	}
	if isExistingOpenAIOAuthAccountForRefresh(&RotationAccount{ProviderCode: ProviderAnthropic, Type: AccountTypeOAuth}) {
		t.Fatal("wrong provider excluded")
	}
	if isExistingOpenAIOAuthAccountForRefresh(&RotationAccount{ProviderCode: ProviderGPT, Type: "api_key"}) {
		t.Fatal("wrong type excluded")
	}
	if !isExistingOpenAIOAuthAccountForRefresh(&RotationAccount{ProviderCode: ProviderGPT, Type: AccountTypeOAuth, Status: "active"}) {
		t.Fatal("active gpt oauth account refreshable")
	}
	if isManagedOpenAIOAuthRefreshErrorCode("other") || !isManagedOpenAIOAuthRefreshErrorCode(OpenAIOAuthTokenRefreshFailedErrorCode) {
		t.Fatal("managed code membership")
	}
	if IsLocalConfigurationError(errors.New("plain")) {
		t.Fatal("plain error is not local configuration")
	}
	if (&LockBusyError{Message: "busy"}).Error() != "busy" {
		t.Fatal("LockBusyError message")
	}
	if (&CredentialsUnavailableError{}).Error() != "OAuth 凭据读取失败" {
		t.Fatal("CredentialsUnavailableError message")
	}
	if (&RevisionConflictError{Message: "conflict"}).Error() != "conflict" {
		t.Fatal("RevisionConflictError message")
	}
}

func TestW9HNormalizedAccountIDSetAndCandidateHelpers(t *testing.T) {
	if normalizedRefreshAccountIDSet(nil) != nil {
		t.Fatal("nil slice reads nil")
	}
	if normalizedRefreshAccountIDSet([]string{"", "   "}) != nil {
		t.Fatal("blank-only slice reads nil")
	}
	set := normalizedRefreshAccountIDSet([]string{" a ", "b", "a"})
	if len(set) != 2 || !set["a"] || !set["b"] {
		t.Fatalf("set=%v", set)
	}
	candidate := RefreshCandidate{AccountID: "id1", AccountName: "name1", AccountStatus: "active", ConfigRevision: 0}
	if candidateID(candidate) != "id1" || candidateName(candidate) != "name1" || candidateStatus(candidate) != "active" {
		t.Fatal("decrypt-failure candidate accessors")
	}
	if candidateConfigRevision(candidate) != 1 {
		t.Fatal("non-positive revision normalizes to 1")
	}
	withAccount := RefreshCandidate{Account: &RotationAccount{ID: "id2", Name: "name2", Status: "disabled", ConfigRevision: 0}}
	if candidateID(withAccount) != "id2" || candidateName(withAccount) != "name2" || candidateStatus(withAccount) != "disabled" {
		t.Fatal("account candidate accessors")
	}
	if candidateConfigRevision(withAccount) != 1 {
		t.Fatal("account revision normalizes to 1")
	}
	withAccount.Account.ConfigRevision = 7
	if candidateConfigRevision(withAccount) != 7 {
		t.Fatal("account revision passthrough")
	}
	if !(RefreshCandidate{Account: nil}).IsDecryptFailure() || (RefreshCandidate{Account: &RotationAccount{}}).IsDecryptFailure() {
		t.Fatal("IsDecryptFailure classification")
	}
}

func TestW9HOptionIntAndSettingsReader(t *testing.T) {
	if _, err := optionInt(5, "批量", 1, 3); err == nil || !strings.Contains(err.Error(), "必须在 1 到 3 之间") {
		t.Fatalf("out of range err=%v", err)
	}
	if value, err := optionInt(2, "批量", 1, 3); err != nil || value != 2 {
		t.Fatalf("in range value=%d err=%v", value, err)
	}
	reader := MapSettingsReader{"k": 7}
	value, ok, err := reader.SettingInt(context.Background(), "k")
	if err != nil || !ok || value != 7 {
		t.Fatalf("reader value=%d ok=%v err=%v", value, ok, err)
	}
	if _, ok, _ := reader.SettingInt(context.Background(), "missing"); ok {
		t.Fatal("missing key not ok")
	}
}

// ---------------------------------------------------------------------------
// Clock + time-format arms
// ---------------------------------------------------------------------------

func TestW9HClockAndTimeFormatArms(t *testing.T) {
	// ClockFunc adapter.
	stamp := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if got := (ClockFunc(func() time.Time { return stamp })).Now(); !got.Equal(stamp) {
		t.Fatalf("ClockFunc=%v", got)
	}
	if SystemClock().Now().IsZero() {
		t.Fatal("system clock must tick")
	}
	if got := isoMillis(stamp); got != "2026-01-02T03:04:05.000Z" {
		t.Fatalf("isoMillis=%q", got)
	}
	if got, ok := rfc3339Millis("2026-01-02T03:04:05.000Z"); !ok || got != stamp.UnixMilli() {
		t.Fatalf("rfc3339Millis=%d ok=%v", got, ok)
	}
	if _, ok := rfc3339Millis("junk"); ok {
		t.Fatal("junk must not parse")
	}
	if got, ok := canonicalRFC3339(" 2026-01-02T05:04:05+02:00 "); !ok || got != "2026-01-02T03:04:05.000Z" {
		t.Fatalf("canonicalRFC3339=%q ok=%v", got, ok)
	}
	if _, ok := canonicalRFC3339("junk"); ok {
		t.Fatal("junk canonical must fail")
	}
}

// ---------------------------------------------------------------------------
// Failure context deep arms
// ---------------------------------------------------------------------------

type w9hCodedError struct{ message string }

func (e *w9hCodedError) Error() string      { return e.message }
func (e *w9hCodedError) ErrorCode() string  { return "W9H_CODE" }
func (e *w9hCodedError) StackTrace() string { return "frame1\nframe2" }

type w9hNilUnwrapError struct{}

func (e *w9hNilUnwrapError) Error() string { return "nil unwrap" }
func (e *w9hNilUnwrapError) Unwrap() error { return nil }

func TestW9HFailureContextStructuredErrorAttributes(t *testing.T) {
	context := CaptureUnexpectedFailureContext(&w9hCodedError{message: "coded"}, FailureCaptureOptions{})
	if context.Error == nil || context.Error.Code != "W9H_CODE" || context.Error.Stack != "frame1\nframe2" {
		t.Fatalf("context=%+v", context.Error)
	}
	if !strings.Contains(context.Error.Name, "w9hCodedError") {
		t.Fatalf("name=%q", context.Error.Name)
	}
	// Anonymous error type name falls back through the fmt type name.
	anonymous := errors.New("plain")
	if got := errorTypeName(anonymous); got == "" {
		t.Fatal("type name empty")
	}
	// Unwrap returning nil contributes no cause.
	captured := captureFailureError(&w9hNilUnwrapError{}, newFailureCaptureState(), 0)
	if captured.Cause != nil {
		t.Fatalf("nil unwrap cause=%+v", captured.Cause)
	}
	// Joined errors continue the cause chain with the first member.
	joined := errors.Join(errors.New("first"), errors.New("second"))
	captured = captureFailureError(joined, newFailureCaptureState(), 0)
	if captured.Cause == nil || captured.Cause.Message != "first" {
		t.Fatalf("joined cause=%+v", captured.Cause)
	}
	// Cause chain deeper than the cap sets the truncation marker.
	deep := errors.New("level0")
	for i := 0; i < 8; i++ {
		deep = fmt.Errorf("level%d: %w", i+1, deep)
	}
	state := newFailureCaptureState()
	captured = captureFailureError(deep, state, 0)
	if !state.truncated {
		t.Fatal("deep chain must mark truncation")
	}
	count := 0
	for node := captured; node != nil; node = node.Cause {
		count++
	}
	if count != failureMaxCauseDepth+1 {
		t.Fatalf("cause chain depth=%d", count)
	}
	// nil error captures nothing.
	if captureFailureError(nil, newFailureCaptureState(), 0) != nil {
		t.Fatal("nil error must capture nothing")
	}
}

func TestW9HFailureContextValueKinds(t *testing.T) {
	cycle := map[string]any{}
	cycle["self"] = cycle
	shared := []any{1, 2}
	funcValue := func() {}
	type w9hStruct struct{ A int }
	inputs := map[string]any{
		"nil-value":   nil,
		"bool":        true,
		"struct":      w9hStruct{A: 1},
		"func":        funcValue,
		"pointer":     &w9hStruct{A: 2},
		"nil-pointer": (*w9hStruct)(nil),
		"array":       [2]int{1, 2},
		"slice":       shared,
		"shared-again": map[string]any{
			"ref": shared,
		},
		"cycle": cycle,
		"deep":  w9hDeepMap(0),
		"big-slice": func() []any {
			out := make([]any, failureMaxCollectionEntries+10)
			for i := range out {
				out[i] = i
			}
			return out
		}(),
	}
	context := CaptureUnexpectedFailureContext(errors.New("kinds"), FailureCaptureOptions{DecisionInputs: inputs})
	if context.TruncationReason != failureTruncationReason {
		t.Fatalf("truncation=%q", context.TruncationReason)
	}
	if context.DecisionInputs["func"] != "[function]" {
		t.Fatalf("func=%v", context.DecisionInputs["func"])
	}
	if context.DecisionInputs["struct"] != "{1}" {
		t.Fatalf("struct=%v", context.DecisionInputs["struct"])
	}
	if context.DecisionInputs["pointer"] != "{2}" {
		t.Fatalf("pointer=%v", context.DecisionInputs["pointer"])
	}
	if context.DecisionInputs["nil-pointer"] != nil {
		t.Fatalf("nil pointer=%v", context.DecisionInputs["nil-pointer"])
	}
	// Sorted key order visits "shared-again" before "slice": the first visit
	// keeps the value, the second sees the seen marker.
	if got, ok := context.DecisionInputs["shared-again"].(map[string]any); !ok || got["ref"] == nil {
		t.Fatalf("shared reference=%v", context.DecisionInputs["shared-again"])
	}
	if got := context.DecisionInputs["slice"]; got != "[truncated: circular reference]" {
		t.Fatalf("slice=%v", got)
	}
	if got, ok := context.DecisionInputs["cycle"].(map[string]any); !ok || got["self"] != "[truncated: circular reference]" {
		t.Fatalf("cycle=%v", context.DecisionInputs["cycle"])
	}
	if context.DecisionInputs["big-slice"] == nil {
		t.Fatal("big slice dropped")
	}
	if _, ok := context.DecisionInputs["deep"]; !ok {
		t.Fatal("deep map dropped")
	}
	// LogValue of the unexpected context renders a group.
	value := context.LogValue()
	if value.Kind() != slog.KindGroup {
		t.Fatalf("unexpected log kind=%v", value.Kind())
	}
	// Expected context with nil decision inputs.
	expected, err := CaptureExpectedFailureContext("reason", nil)
	if err != nil || expected.DecisionInputs != nil {
		t.Fatalf("expected=%+v err=%v", expected, err)
	}
	if (expected.LogValue()).Kind() != slog.KindGroup {
		t.Fatal("expected log value must render a group")
	}
	// CapturedError.LogValue arms: nil receiver, minimal, full.
	var nilError *CapturedError
	if (nilError.LogValue()).Kind() != slog.KindAny {
		t.Fatal("nil captured error renders any")
	}
	minimal := (&CapturedError{Name: "n", Message: "m"}).LogValue()
	if minimal.Kind() != slog.KindGroup {
		t.Fatal("minimal captured error renders a group")
	}
	full := (&CapturedError{Name: "n", Message: "m", Stack: "s", Code: "c", Cause: &CapturedError{Name: "inner"}}).LogValue()
	if full.Kind() != slog.KindGroup {
		t.Fatal("full captured error renders a group")
	}
}

func w9hDeepMap(depth int) map[string]any {
	node := map[string]any{"leaf": depth}
	for i := 0; i < 12; i++ {
		node = map[string]any{"child": node}
	}
	_ = depth
	return node
}

func TestW9HFailureContextBudgetExhaustion(t *testing.T) {
	// A single huge string exhausts the budget; the next value sees zero.
	state := newFailureCaptureState()
	big := strings.Repeat("y", failureMaxEventBytes+1024)
	output := sanitizeFailureValue(big, "big", state, 0)
	if !state.truncated {
		t.Fatal("oversized string must mark truncation")
	}
	if state.remainingBytes != failureMaxEventBytes-failureMaxStringLength {
		t.Fatalf("remaining=%d", state.remainingBytes)
	}
	if text, ok := output.(string); !ok || len(text) != failureMaxStringLength {
		t.Fatalf("output length=%d", len(text))
	}
	// Small values still pass while budget remains.
	next := sanitizeFailureValue("tiny", "next", state, 0)
	if next != "tiny" {
		t.Fatalf("next=%v", next)
	}
	// A drained budget truncates every further value at entry.
	state.remainingBytes = 0
	if got := sanitizeFailureValue("tiny", "next", state, 0); got != "[truncated: event byte budget]" {
		t.Fatalf("drained=%v", got)
	}
	// consume() clamps at zero.
	state.consume(500)
	if state.remainingBytes != 0 {
		t.Fatalf("remaining=%d", state.remainingBytes)
	}
	// utf16Length counts astral runes as two units.
	if got := utf16Length("a\U0001F600b"); got != 4 {
		t.Fatalf("utf16Length=%d", got)
	}
}

// ---------------------------------------------------------------------------
// Redis failure state store with a scripted Scripter
// ---------------------------------------------------------------------------

type w9hScripter struct {
	evalFunc func(ctx context.Context, script string, keys []string, args ...any) (any, error)
	getFunc  func(ctx context.Context, key string) (string, error)
}

func (s *w9hScripter) Eval(ctx context.Context, script string, keys []string, args ...any) (any, error) {
	return s.evalFunc(ctx, script, keys, args...)
}

func (s *w9hScripter) Get(ctx context.Context, key string) (string, error) {
	return s.getFunc(ctx, key)
}

func TestW9HRedisFailureStateRecordShapes(t *testing.T) {
	// Eval error propagates.
	failing := &w9hScripter{evalFunc: func(context.Context, string, []string, ...any) (any, error) {
		return nil, errors.New("redis down")
	}}
	if _, err := NewRedisFailureStateStore(failing).Record(context.Background(), "acc", 100, FailureKindLocalConfiguration, 3); err == nil {
		t.Fatal("eval error must propagate")
	}
	// Non-slice reply reads as a shape error.
	shaped := &w9hScripter{evalFunc: func(context.Context, string, []string, ...any) (any, error) {
		return "bogus", nil
	}}
	if _, err := NewRedisFailureStateStore(shaped).Record(context.Background(), "acc", 100, FailureKindUntrustedUpstream, 3); !errors.Is(err, errRedisShape) {
		t.Fatalf("shape err=%v", err)
	}
	// Mixed scalar types exercise every numericRedis/stringRedis branch.
	mixed := &w9hScripter{evalFunc: func(_ context.Context, script string, keys []string, args ...any) (any, error) {
		if keys[0] != failureStateKey("acc") {
			t.Fatalf("key=%q", keys[0])
		}
		if len(args) != 5 {
			t.Fatalf("args=%v", args)
		}
		if args[3] != "3" || args[2] != "1" {
			t.Fatalf("revision/kind args=%v", args)
		}
		return []any{int64(4), float64(500), "2", int64(3), int64(1), []byte("mutation-bytes"), "payload-json"}, nil
	}}
	state, err := NewRedisFailureStateStore(mixed).Record(context.Background(), "acc", 100, FailureKindLocalConfiguration, 3)
	if err != nil {
		t.Fatalf("record err=%v", err)
	}
	if state.Count != 4 || state.BackoffUntil != 500 || state.LocalConfigurationCount != 2 || state.ConfigRevision != 3 || !state.Applied || state.MutationID != "mutation-bytes" || state.Snapshot != "payload-json" {
		t.Fatalf("state=%+v", state)
	}
	// Short replies fall back per index and negative clamps apply.
	short := &w9hScripter{evalFunc: func(context.Context, string, []string, ...any) (any, error) {
		return []any{}, nil
	}}
	state, err = NewRedisFailureStateStore(short).Record(context.Background(), "acc", -5, FailureKindUntrustedUpstream, 0)
	if err != nil {
		t.Fatalf("short record err=%v", err)
	}
	if state.Count != 1 || state.BackoffUntil != 0 || state.LocalConfigurationCount != 0 || state.ConfigRevision != 1 || state.Applied || state.MutationID == "" {
		t.Fatalf("fallback state=%+v", state)
	}
	// Unparsable numeric string falls back too.
	unparsable := &w9hScripter{evalFunc: func(context.Context, string, []string, ...any) (any, error) {
		return []any{"NaN", "x", true, nil}, nil
	}}
	state, _ = NewRedisFailureStateStore(unparsable).Record(context.Background(), "acc", 100, FailureKindUntrustedUpstream, 9)
	if state.Count != 1 || state.BackoffUntil != 100 || state.ConfigRevision != 9 {
		t.Fatalf("unparsable state=%+v", state)
	}
	// CleanupBackoff is a no-op on Redis.
	NewRedisFailureStateStore(mixed).CleanupBackoff(1)
}

func TestW9HRedisFailureStateReadArms(t *testing.T) {
	store := NewRedisFailureStateStore(&w9hScripter{
		getFunc: func(context.Context, string) (string, error) { return "", nil },
		evalFunc: func(context.Context, string, []string, ...any) (any, error) {
			t.Fatal("empty read must not eval")
			return nil, nil
		},
	})
	if state, _ := store.Read(context.Background(), "acc", 0, 1); state != nil {
		t.Fatalf("empty read state=%v", state)
	}
	// Get error reads as absent.
	store = NewRedisFailureStateStore(&w9hScripter{
		getFunc: func(context.Context, string) (string, error) { return "", errors.New("down") },
		evalFunc: func(context.Context, string, []string, ...any) (any, error) {
			return int64(1), nil
		},
	})
	if state, _ := store.Read(context.Background(), "acc", 0, 1); state != nil {
		t.Fatalf("error read state=%v", state)
	}
	evalCalls := 0
	newStore := func(payload string) *RedisFailureStateStore {
		evalCalls = 0
		return NewRedisFailureStateStore(&w9hScripter{
			getFunc: func(context.Context, string) (string, error) { return payload, nil },
			evalFunc: func(context.Context, string, []string, ...any) (any, error) {
				evalCalls++
				return int64(1), nil
			},
		})
	}
	// Malformed JSON clears the record.
	if state, _ := newStore("not json").Read(context.Background(), "acc", 0, 1); state != nil || evalCalls != 1 {
		t.Fatalf("malformed state=%v evals=%d", state, evalCalls)
	}
	// Missing/zero revision clears the record.
	if state, _ := newStore(`{"count":2}`).Read(context.Background(), "acc", 0, 1); state != nil || evalCalls != 1 {
		t.Fatalf("missing revision state=%v evals=%d", state, evalCalls)
	}
	// Newer stored revision shadows.
	if state, _ := newStore(`{"count":2,"configRevision":9}`).Read(context.Background(), "acc", 0, 1); state != nil || evalCalls != 0 {
		t.Fatalf("newer revision state=%v evals=%d", state, evalCalls)
	}
	// Older stored revision clears.
	if state, _ := newStore(`{"count":2,"configRevision":1}`).Read(context.Background(), "acc", 0, 5); state != nil || evalCalls != 1 {
		t.Fatalf("older revision state=%v evals=%d", state, evalCalls)
	}
	// Equal revision with a live backoff reads applied state.
	state, _ := newStore(`{"count":2,"localConfigurationCount":1,"backoffUntil":5000,"configRevision":3,"mutationId":"m"}`).Read(context.Background(), "acc", 1000, 3)
	if state == nil || state.Count != 2 || state.BackoffUntil != 5000 || !state.Applied || state.MutationID != "m" || state.Snapshot == "" {
		t.Fatalf("live state=%+v", state)
	}
	// Expired backoff reads zero.
	state, _ = newStore(`{"count":2,"backoffUntil":500,"configRevision":3}`).Read(context.Background(), "acc", 1000, 3)
	if state == nil || state.BackoffUntil != 0 {
		t.Fatalf("expired state=%+v", state)
	}
	// Missing count fields default to zero.
	state, _ = newStore(`{"configRevision":3}`).Read(context.Background(), "acc", 0, 3)
	if state == nil || state.Count != 0 || state.LocalConfigurationCount != 0 {
		t.Fatalf("sparse state=%+v", state)
	}
}

func TestW9HRedisFailureStateClearArms(t *testing.T) {
	evalCalls := 0
	store := NewRedisFailureStateStore(&w9hScripter{
		getFunc: func(context.Context, string) (string, error) { return "", nil },
		evalFunc: func(_ context.Context, script string, _ []string, args ...any) (any, error) {
			evalCalls++
			if script != redisCompareDeleteRefreshFailureScript {
				t.Fatalf("script mismatch")
			}
			if args[1] != "7" {
				t.Fatalf("revision arg=%v", args)
			}
			return int64(1), nil
		},
	})
	if err := store.Clear(context.Background(), "acc", RefreshFailureState{Snapshot: "snap", ConfigRevision: 7}); err != nil || evalCalls != 1 {
		t.Fatalf("clear err=%v evals=%d", err, evalCalls)
	}
	// Empty snapshot skips the script.
	if err := store.Clear(context.Background(), "acc", RefreshFailureState{}); err != nil || evalCalls != 1 {
		t.Fatalf("empty snapshot err=%v evals=%d", err, evalCalls)
	}
	// Eval error propagates.
	failing := NewRedisFailureStateStore(&w9hScripter{
		getFunc: func(context.Context, string) (string, error) { return "", nil },
		evalFunc: func(context.Context, string, []string, ...any) (any, error) {
			return nil, errors.New("down")
		},
	})
	if err := failing.Clear(context.Background(), "acc", RefreshFailureState{Snapshot: "snap"}); err == nil {
		t.Fatal("clear eval error must propagate")
	}
}

func TestW9HFailureStatePureHelpers(t *testing.T) {
	if kindBool(true) != "1" || kindBool(false) != "0" {
		t.Fatal("kindBool")
	}
	if localCountFallback(FailureKindLocalConfiguration) != 1 || localCountFallback(FailureKindUntrustedUpstream) != 0 {
		t.Fatal("localCountFallback")
	}
	if normalizedRevisionOr(0) != 1 || normalizedRevisionOr(-2) != 1 || normalizedRevisionOr(9) != 9 {
		t.Fatal("normalizedRevisionOr")
	}
	if clampInt64(5, 1, 3) != 3 || clampInt64(0, 1, 3) != 1 || clampInt64(2, 1, 3) != 2 {
		t.Fatal("clampInt64")
	}
	if formatInt64(42) != "42" {
		t.Fatal("formatInt64")
	}
	// Deterministic key derivation.
	if failureStateKey("a") != failureStateKey("a") || failureStateKey("a") == failureStateKey("b") {
		t.Fatal("failureStateKey derivation")
	}
	if !strings.HasPrefix(failureStateKey("a"), "juhe-ai:state:openai-oauth-refresh-failure:") {
		t.Fatalf("key prefix=%q", failureStateKey("a"))
	}
	// Mutation ids are random hex.
	first, second := newMutationID(), newMutationID()
	if len(first) != 24 || first == second {
		t.Fatalf("mutation ids %q %q", first, second)
	}
}

// ---------------------------------------------------------------------------
// Failure log attrs arms
// ---------------------------------------------------------------------------

func TestW9HFailureLogAttrsArms(t *testing.T) {
	if got := tokenExchangeFailureLogAttrs(nil, "openai", "stage", nil); got != nil {
		t.Fatalf("nil error attrs=%v", got)
	}
	args := tokenExchangeFailureLogAttrs(&LocalConfigurationError{Message: "local"}, "openai", "oauth_oauth_refresh", map[string]any{"accountId": "a"})
	if len(args) < 2 || args[0] != "failureClass" || args[1] != "expected" {
		t.Fatalf("expected args=%v", args)
	}
	// Truncation flag rides the flat list when inputs overflow the budget.
	huge := map[string]any{"blob": strings.Repeat("z", failureMaxEventBytes+10)}
	truncated := tokenExchangeFailureLogAttrs(&LocalConfigurationError{Message: "local"}, "openai", "stage", huge)
	found := false
	for i := 0; i+1 < len(truncated); i++ {
		if truncated[i] == "truncationReason" {
			found = true
		}
	}
	if !found {
		t.Fatalf("truncation attr missing in %d args", len(truncated))
	}
	unexpected := tokenExchangeFailureLogAttrs(errors.New("boom"), "openai", "stage", map[string]any{"accountId": "a"})
	if unexpected[0] != "failureClass" || unexpected[1] != "unexpected" {
		t.Fatalf("unexpected args=%v", unexpected)
	}
	// expectedFailureLogAttrs direct: with and without truncation.
	plain := expectedFailureLogAttrs(ExpectedFailureContext{FailureClass: "expected", ReasonCode: "r"})
	if plain[0] != "failureClass" {
		t.Fatalf("plain=%v", plain)
	}
	withTruncation := expectedFailureLogAttrs(ExpectedFailureContext{FailureClass: "expected", ReasonCode: "r", TruncationReason: failureTruncationReason})
	if len(withTruncation) != len(plain)+2 {
		t.Fatalf("truncation adds one pair: %v", withTruncation)
	}
	if failureReasonCode(errors.New("x")) != "" {
		t.Fatal("plain errors have no reason code")
	}
	// maskSecret arm: empty input.
	if maskSecret("") != "" {
		t.Fatal("empty mask")
	}
}

// ---------------------------------------------------------------------------
// Misc store helpers without a database
// ---------------------------------------------------------------------------

func TestW9HStoreDialectHelpers(t *testing.T) {
	sqlite := &Store{}
	if sqlite.table("accounts") != "accounts" {
		t.Fatalf("sqlite table=%q", sqlite.table("accounts"))
	}
	if sqlite.bind("SELECT * FROM t WHERE a = ? AND b = ?") != "SELECT * FROM t WHERE a = ? AND b = ?" {
		t.Fatal("sqlite bind passthrough")
	}
	pg := &Store{pg: true}
	if pg.table("accounts") != "juhe_business.accounts" {
		t.Fatalf("pg table=%q", pg.table("accounts"))
	}
	if got := pg.bind("SELECT * FROM t WHERE a = ? AND b = ? AND c = ?"); got != "SELECT * FROM t WHERE a = $1 AND b = $2 AND c = $3" {
		t.Fatalf("pg bind=%q", got)
	}
	if got := pg.bind("no placeholders"); got != "no placeholders" {
		t.Fatalf("pg bind plain=%q", got)
	}
	if outerEntityName("accounts") != "accounts" || outerEntityName("juhe_business.accounts") != "accounts" || outerEntityName("api_keys") != "api_keys" {
		t.Fatal("outerEntityName")
	}
	if sqlPlaceholders(3) != "?, ?, ?" || sqlPlaceholders(1) != "?" {
		t.Fatal("sqlPlaceholders")
	}
	if ensureCtx(nil) == nil || ensureCtx(context.Background()) == nil {
		t.Fatal("ensureCtx")
	}
	if credentialsEqual(map[string]any{"a": 1}, map[string]any{"a": 1}) != true {
		t.Fatal("credentialsEqual same")
	}
	if credentialsEqual(map[string]any{"a": 1}, map[string]any{"a": 2}) {
		t.Fatal("credentialsEqual differ")
	}
	if stringCredential(map[string]any{"k": " v "}, "k") != "v" || stringCredential(map[string]any{}, "k") != "" {
		t.Fatal("stringCredential")
	}
}

func TestW9HNewRefreshJobOptionWiring(t *testing.T) {
	failures := NewMemoryFailureStateStore()
	logger := slog.Default()
	settings := MapSettingsReader{"oauthAccessTokenRefreshLeadSeconds": 600}
	job := NewRefreshJob(nil, &w9hFakeExchanger{},
		WithFailureStateStore(failures),
		WithSettings(settings),
		WithLogger(logger),
		WithClock(ClockFunc(defaultNow)),
	)
	if job.failures != failures || job.settings == nil || job.logger != logger {
		t.Fatalf("job wiring failures=%T settings=%T logger=%T", job.failures, job.settings, job.logger)
	}
	// The injected settings reader drives the lead window: a token expiring in
	// 400s is inside the 600s lead and therefore listed as due.
	store, db, _ := newTestStore(t)
	job.store = store
	seedOpenAIOAuthAccount(t, db, "w9h-opt-acc", map[string]any{
		"refresh_token": "rt", "access_token": "at", "expires_at": isoMillis(defaultNow().Add(400 * time.Second)),
	}, defaultNow())
	result, err := job.RunOnce(context.Background(), RefreshOptions{})
	if err != nil {
		t.Fatalf("run err=%v", err)
	}
	if result.Scanned != 1 || result.Due != 1 {
		t.Fatalf("result=%+v", result)
	}
}

func TestW9HNewKeepaliveJobOptionWiring(t *testing.T) {
	logger := slog.Default()
	job := NewKeepaliveJob(nil, &w9hFakeExchanger{}, WithKeepaliveLogger(logger), WithKeepaliveClock(ClockFunc(defaultNow)))
	if job.logger != logger {
		t.Fatal("keepalive logger wiring")
	}
	if job.now().Compare(defaultNow()) != 0 {
		t.Fatalf("keepalive clock=%v", job.now())
	}
	plans := KeepalivePlans()
	if len(plans) != 3 || plans[0].Provider != ProviderAnthropic || plans[1].Provider != ProviderGemini || plans[2].Provider != ProviderXAI {
		t.Fatalf("plans=%+v", plans)
	}
	if plans[2].RequiredProfileID != ProfileXAIOpenAIV1 || plans[2].Lead != GrokKeepaliveLead {
		t.Fatalf("grok plan=%+v", plans[2])
	}
}

func TestW9HKeepaliveProviderMessages(t *testing.T) {
	if providerSourceMissingMessage(ProviderAnthropic) != "Anthropic OAuth 凭据源账户不存在或类型不匹配" {
		t.Fatal("anthropic source message")
	}
	if providerSourceMissingMessage(ProviderGemini) != "Gemini OAuth 凭据源账户不存在或类型不匹配" {
		t.Fatal("gemini source message")
	}
	if providerSourceMissingMessage(ProviderXAI) != "Grok OAuth 凭据源账户不存在或类型不匹配" || providerSourceMissingMessage("other") != "Grok OAuth 凭据源账户不存在或类型不匹配" {
		t.Fatal("grok/default source message")
	}
	if providerMissingRefreshTokenMessage(ProviderAnthropic) != "Anthropic OAuth 账户缺少 Refresh Token" {
		t.Fatal("anthropic token message")
	}
	if providerMissingRefreshTokenMessage(ProviderGemini) != "Gemini OAuth 账户缺少 Refresh Token" {
		t.Fatal("gemini token message")
	}
	if providerMissingRefreshTokenMessage(ProviderXAI) != "Grok OAuth 账户缺少 Refresh Token" || providerMissingRefreshTokenMessage("other") != "Grok OAuth 账户缺少 Refresh Token" {
		t.Fatal("grok/default token message")
	}
	if !isRefreshableKeepaliveAccount(KeepalivePlan{Provider: "p", AccountType: "t"}, &RotationAccount{ProviderCode: "p", Type: "t"}) {
		t.Fatal("matching plan refreshable")
	}
	if isRefreshableKeepaliveAccount(KeepalivePlan{Provider: "p", AccountType: "t"}, &RotationAccount{ProviderCode: "x", Type: "t"}) {
		t.Fatal("provider mismatch not refreshable")
	}
	if isRefreshableKeepaliveAccount(KeepalivePlan{Provider: "p", AccountType: "t", RequiredProfileID: "prof"}, &RotationAccount{ProviderCode: "p", Type: "t", ProviderProtocolProfileID: "other"}) {
		t.Fatal("profile mismatch not refreshable")
	}
	if !shouldKeepaliveRefresh(map[string]any{"access_token": "at", "expires_at": "junk"}, defaultNow(), time.Minute) {
		t.Fatal("unparsable expiry is due")
	}
	if !shouldKeepaliveRefresh(map[string]any{"access_token": "at"}, defaultNow(), time.Minute) {
		t.Fatal("missing expiry is due")
	}
}

func TestW9HGeminiFallbackFromCredentials(t *testing.T) {
	credentials := map[string]any{
		"refresh_token": "rt", "oauth_type": "google_one", "client_id": "cid", "client_secret": "sec",
		"project_id": "proj", "tier_id": "ai-premium", "quota_project_id": "quota", "base_url": "https://x", "scope": "s",
	}
	fallback := geminiFallbackFromCredentials(credentials)
	if fallback != (GeminiCredentialFallback{
		RefreshToken: "rt", OAuthType: "google_one", ClientID: "cid", ClientSecret: "sec",
		ProjectID: "proj", TierID: "ai-premium", QuotaProjectID: "quota", BaseURL: "https://x", Scope: "s",
	}) {
		t.Fatalf("fallback=%+v", fallback)
	}
}

// ---------------------------------------------------------------------------
// schedule normalization helpers (direct)
// ---------------------------------------------------------------------------

func TestW9HScheduleHelperArms(t *testing.T) {
	if got := defaultScheduleTimezone(); got == "" || got == "Local" {
		t.Fatalf("default timezone=%q", got)
	}
	if _, err := normalizeScheduleTimezone(nil); err != nil {
		t.Fatalf("nil timezone err=%v", err)
	}
	if _, err := normalizeScheduleTimezone(42); err == nil {
		t.Fatal("non-string timezone must fail")
	}
	if _, err := normalizeScheduleTimezone("Not/AZone"); err == nil {
		t.Fatal("invalid timezone must fail")
	}
	if _, err := normalizeScheduleTime("9:99", "开始时间"); err == nil {
		t.Fatal("bad time must fail")
	}
	if got, err := normalizeScheduleTime(" 08:30 ", "开始时间"); err != nil || got != "08:30" {
		t.Fatalf("normalized time=%q err=%v", got, err)
	}
	if _, err := normalizeDateKey(42, "开始日期"); err == nil {
		t.Fatal("non-string date must fail")
	}
	if _, err := normalizeDateKey("2026-13-40", "开始日期"); err == nil {
		t.Fatal("invalid date must fail")
	}
	if got, err := normalizeDateKey("2026-09-01", "开始日期"); err != nil || got != "2026-09-01" {
		t.Fatalf("date=%q err=%v", got, err)
	}
	if _, err := normalizeScheduleDateRange(map[string]any{"startDate": "2026-09-02", "endDate": "2026-09-01"}); err == nil {
		t.Fatal("inverted range must fail")
	}
	if got, err := normalizeScheduleDateRange(nil); err != nil || got != nil {
		t.Fatalf("nil range=%v err=%v", got, err)
	}
	if _, err := normalizeScheduleDateRange(map[string]any{"startDate": "", "endDate": ""}); err == nil {
		t.Fatal("empty-string date keys must fail")
	}
	if got, err := normalizeScheduleDateRange(map[string]any{"startDate": nil, "endDate": nil}); err != nil || got != nil {
		t.Fatalf("nil range=%v err=%v", got, err)
	}
	if _, err := normalizeScheduleDateRange(map[string]any{"bogus": 1}); err == nil {
		t.Fatal("unknown range keys must fail")
	}
	// minuteOfDay parsing incl. the no-match zero.
	if minuteOfDay("09:05") != 9*60+5 || minuteOfDay("23:59") != 23*60+59 || minuteOfDay("bad") != 0 {
		t.Fatal("minuteOfDay")
	}
	schedule := &AvailabilitySchedule{DateRange: &ScheduleDateRange{StartDate: "2026-09-01", EndDate: "2026-09-10"}}
	if dateInScheduleRange("2026-08-31", schedule) || dateInScheduleRange("2026-09-11", schedule) || !dateInScheduleRange("2026-09-05", schedule) {
		t.Fatal("dateInScheduleRange bounds")
	}
	if !dateInScheduleRange("2020-01-01", &AvailabilitySchedule{}) {
		t.Fatal("nil range always allows")
	}
	// windowEndDateKey: same-day end vs cross-midnight end.
	if windowEndDateKey("2026-09-01", "08:00", "09:00") != "2026-09-01" {
		t.Fatal("same-day end key")
	}
	if windowEndDateKey("2026-09-01", "22:00", "02:00") != "2026-09-02" {
		t.Fatal("cross-midnight end key")
	}
	// zonedLocalMinuteToUTC round trip in UTC and a DST zone.
	millis, ok := zonedLocalMinuteToUTC("2026-09-01", 8*60+30, "UTC")
	if !ok {
		t.Fatal("UTC conversion failed")
	}
	parts := scheduleZonedParts(time.UnixMilli(millis), "UTC")
	if parts.dateKey != "2026-09-01" || parts.minuteOfDay != 8*60+30 {
		t.Fatalf("parts=%+v", parts)
	}
	if _, ok := zonedLocalMinuteToUTC("2026-09-01", 510, "Not/AZone"); ok {
		t.Fatal("invalid timezone must fail")
	}
	if _, ok := zonedLocalMinuteToUTC("2026-09-01", 510, "America/New_York"); !ok {
		t.Fatal("DST zone conversion failed")
	}
	// normalizeScheduleWindow shape errors.
	if _, err := normalizeScheduleWindow("not-a-map", false); err == nil {
		t.Fatal("non-object window must fail")
	}
	if _, err := normalizeScheduleWindow(map[string]any{"start": "08:00", "end": "09:00", "extra": 1}, false); err == nil {
		t.Fatal("unknown window keys must fail")
	}
	// normalizeDaysOfWeek arms.
	if _, err := normalizeDaysOfWeek("x"); err == nil {
		t.Fatal("non-list days must fail")
	}
	if _, err := normalizeDaysOfWeek([]any{float64(0)}); err == nil {
		t.Fatal("day 0 must fail")
	}
	if _, err := normalizeDaysOfWeek([]any{}); err == nil {
		t.Fatal("empty days must fail")
	}
	days, err := normalizeDaysOfWeek([]any{float64(3), float64(1), float64(3)})
	if err != nil || len(days) != 2 || days[0] != 1 || days[1] != 3 {
		t.Fatalf("days=%v err=%v", days, err)
	}
	// normalizeScheduleExceptions arms.
	if got, err := normalizeScheduleExceptions(nil); err != nil || got != nil {
		t.Fatalf("nil exceptions=%v err=%v", got, err)
	}
	if _, err := normalizeScheduleExceptions("x"); err == nil {
		t.Fatal("non-list exceptions must fail")
	}
}

func TestW9HJsonPayloadRawFallback(t *testing.T) {
	// The raw-body fallback keeps unparsable upstream bodies diagnosable.
	payload := parseTokenPayload("\n\t {\"partial\": true} \n")
	if payload["partial"].(bool) != true {
		t.Fatalf("payload=%v", payload)
	}
	encoded, err := json.Marshal(map[string]any{"a": 1})
	if err != nil || !strings.Contains(string(encoded), `"a":1`) {
		t.Fatalf("marshal=%q err=%v", encoded, err)
	}
}
