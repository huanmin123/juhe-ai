package main

// BUG-0175 (D-73) hydration regression: oauth/google_oauth accounts carry
// access_token/refresh_token credentials instead of api_key, so the
// api-key-only secret lookup silently dropped every OAuth account before it
// could enter the dispatch candidate list. These tests pin the archive
// runtimeCredentialSource contract (openai-account-selector.repository.ts:
// oauth/google_oauth resolve access_token -> refresh_token) plus the
// protocol-default baseUrl projection (defaultBaseUrlForProtocol).

import (
	"context"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// TestChainAccountsSelectorHydratesOAuthCredentialSource seeds an oauth
// account set next to an api_key regression account and asserts the hydrated
// dispatch secrets: the OAuth credential source, the drop of a tokenless
// oauth row, and the api_key rotation path staying intact.
func TestChainAccountsSelectorHydratesOAuthCredentialSource(t *testing.T) {
	fixture := newChainFixture(t)
	db := fixture.db
	now := "2026-09-04T00:00:00.000Z"
	seed := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatalf("seed row: %v: %v", query, err)
		}
	}
	insertAccount := func(id, name, accountType string, credentials map[string]any) {
		t.Helper()
		encrypted, err := accounts.EncryptJSON("chain-test-secret", credentials)
		if err != nil {
			t.Fatalf("encrypt credentials for %s: %v", id, err)
		}
		seed(`INSERT INTO accounts (
				id, system_account_id, provider_code, provider_protocol_profile_id, protocol_code, protocol_version,
				name, type, status, schedulable, credentials_encrypted, deleted_at, health_check_model
			) VALUES (?, ?, 'openai', 'prof_1', 'openai', 'v1', ?, ?, 'active', 1, ?, NULL, 'gpt-test')`,
			id, fixture.systemAccount, name, accountType, encrypted)
		seed(`INSERT INTO group_accounts (group_id, system_account_id, account_id, enabled, created_at)
			VALUES (?, ?, ?, 1, ?)`, fixture.groupID, fixture.systemAccount, id, now)
	}

	insertAccount("acc_oauth_access", "OAuth访问令牌", "oauth", map[string]any{
		"access_token":  "oauth-access-token",
		"refresh_token": "oauth-refresh-token",
		"account_id":    "chatgpt-account-1",
		"client_id":     "oauth-client-1",
	})
	insertAccount("acc_oauth_refresh", "OAuth仅刷新令牌", "oauth", map[string]any{
		"refresh_token": "oauth-refresh-only",
	})
	insertAccount("acc_google_oauth", "GoogleOAuth访问令牌", "google_oauth", map[string]any{
		"access_token": "google-access-token",
	})
	// api_key regression: the rotation lookup must keep working unchanged.
	insertAccount("acc_apikey_regression", "APIKey回归", "api_key", map[string]any{
		"api_key":  "sk-regression-key",
		"base_url": "https://regression.example/v1",
	})
	// Neither access_token nor refresh_token: mirrors the Node undefined
	// return (the account still drops at hydration).
	insertAccount("acc_oauth_empty", "OAuth无令牌", "oauth", map[string]any{
		"account_id": "chatgpt-account-2",
	})

	result, err := fixture.selector.ListOpenAIAccountsForGroupResult(context.Background(), fixture.groupID, fixture.systemAccount,
		gatewayruntimecache.OpenAIAccountsForGroupOptions{})
	if err != nil {
		t.Fatalf("list accounts: %v", err)
	}

	byID := map[string]*gatewayruntimecache.OpenAIAccountSecret{}
	for index := range result.Accounts {
		byID[result.Accounts[index].ID] = &result.Accounts[index]
	}

	oauthAccess := byID["acc_oauth_access"]
	if oauthAccess == nil {
		t.Fatalf("oauth account with access_token missing from result: %+v", result.Diagnostics)
	}
	if oauthAccess.Type != "oauth" {
		t.Fatalf("oauth access type = %q", oauthAccess.Type)
	}
	if oauthAccess.APIKey != "oauth-access-token" {
		t.Fatalf("oauth access apiKey = %q (must resolve access_token)", oauthAccess.APIKey)
	}
	if oauthAccess.APIKeys != nil {
		t.Fatalf("oauth apiKeys = %v (Node keeps undefined for non-api_key types)", oauthAccess.APIKeys)
	}
	if oauthAccess.RefreshToken == nil || *oauthAccess.RefreshToken != "oauth-refresh-token" {
		t.Fatalf("oauth refreshToken = %v", oauthAccess.RefreshToken)
	}
	if oauthAccess.ClientID == nil || *oauthAccess.ClientID != "oauth-client-1" {
		t.Fatalf("oauth clientId = %v", oauthAccess.ClientID)
	}
	// Missing credentials.base_url falls back to the openai protocol default.
	if oauthAccess.BaseURL != "https://api.openai.com/v1" {
		t.Fatalf("oauth baseUrl = %q (protocol default expected)", oauthAccess.BaseURL)
	}

	oauthRefresh := byID["acc_oauth_refresh"]
	if oauthRefresh == nil {
		t.Fatalf("refresh-token-only oauth account missing from result: %+v", result.Diagnostics)
	}
	if oauthRefresh.APIKey != "oauth-refresh-only" {
		t.Fatalf("refresh-only oauth apiKey = %q (must fall back to refresh_token)", oauthRefresh.APIKey)
	}
	if oauthRefresh.RefreshToken == nil || *oauthRefresh.RefreshToken != "oauth-refresh-only" {
		t.Fatalf("refresh-only oauth refreshToken = %v", oauthRefresh.RefreshToken)
	}

	google := byID["acc_google_oauth"]
	if google == nil {
		t.Fatalf("google_oauth account missing from result: %+v", result.Diagnostics)
	}
	if google.Type != "google_oauth" {
		t.Fatalf("google_oauth type = %q", google.Type)
	}
	if google.APIKey != "google-access-token" {
		t.Fatalf("google_oauth apiKey = %q (must resolve access_token)", google.APIKey)
	}

	regression := byID["acc_apikey_regression"]
	if regression == nil {
		t.Fatalf("api_key regression account missing from result: %+v", result.Diagnostics)
	}
	if regression.APIKey != "sk-regression-key" {
		t.Fatalf("api_key regression apiKey = %q", regression.APIKey)
	}
	if len(regression.APIKeys) != 1 || regression.APIKeys[0] != "sk-regression-key" {
		t.Fatalf("api_key regression apiKeys = %v", regression.APIKeys)
	}
	if regression.BaseURL != "https://regression.example/v1" {
		t.Fatalf("api_key regression baseUrl = %q (explicit base_url must win)", regression.BaseURL)
	}

	if _, present := byID["acc_oauth_empty"]; present {
		t.Fatal("tokenless oauth account must be dropped at hydration")
	}
	diagnostics := result.Diagnostics
	if diagnostics == nil {
		t.Fatal("diagnostics missing")
	}
	// 5 seeded + fixture acc_1; only the tokenless oauth row drops.
	if diagnostics.HydrationDroppedCount != 1 {
		t.Fatalf("hydrationDroppedCount = %d", diagnostics.HydrationDroppedCount)
	}
	// 4 seeded hydrated + fixture acc_1 = 5; only the tokenless oauth row drops.
	if diagnostics.FinalAccountCount != 5 {
		t.Fatalf("finalAccountCount = %d (fixture + 4 hydrated seeded)", diagnostics.FinalAccountCount)
	}
}

// TestChainRuntimeCredentialSourceFallbackOrder pins the helper contract:
// access_token wins when non-empty, refresh_token is the fallback, and the
// api_key type keeps the rotation-pool first key.
func TestChainRuntimeCredentialSourceFallbackOrder(t *testing.T) {
	cases := []struct {
		name        string
		accountType string
		credentials map[string]any
		apiKey      string
		expected    string
	}{
		{"oauth prefers access_token", "oauth", map[string]any{"access_token": "a", "refresh_token": "r"}, "", "a"},
		{"oauth falls back to refresh_token", "oauth", map[string]any{"refresh_token": "r"}, "", "r"},
		{"oauth empty access_token falls back", "oauth", map[string]any{"access_token": "", "refresh_token": "r"}, "", "r"},
		{"oauth without tokens", "oauth", map[string]any{}, "", ""},
		{"google_oauth prefers access_token", "google_oauth", map[string]any{"access_token": "g"}, "", "g"},
		{"google_oauth falls back to refresh_token", "google_oauth", map[string]any{"refresh_token": "gr"}, "", "gr"},
		{"api_key keeps rotation first key", "api_key", map[string]any{}, "sk-first", "sk-first"},
		{"api_key without key", "api_key", map[string]any{}, "", ""},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := chainRuntimeCredentialSource(testCase.accountType, testCase.credentials, testCase.apiKey)
			if got != testCase.expected {
				t.Fatalf("chainRuntimeCredentialSource(%q) = %q, want %q", testCase.accountType, got, testCase.expected)
			}
		})
	}
}

// TestChainBaseURLOfProtocolDefaults pins the archive defaultBaseUrlForProtocol
// projection: an explicit credentials.base_url always wins, otherwise the
// protocol profile decides the default.
func TestChainBaseURLOfProtocolDefaults(t *testing.T) {
	cases := []struct {
		name            string
		credentials     map[string]any
		protocolCode    string
		protocolVersion string
		expected        string
	}{
		{"explicit base_url wins over protocol default", map[string]any{"base_url": "https://custom.example/v1"}, "anthropic", "v1", "https://custom.example/v1"},
		{"empty base_url falls back", map[string]any{"base_url": ""}, "openai", "v1", "https://api.openai.com/v1"},
		{"missing base_url openai default", map[string]any{}, "openai", "v1", "https://api.openai.com/v1"},
		{"anthropic profile default", map[string]any{}, "anthropic", "v1", "https://api.anthropic.com/v1"},
		{"gemini profile default", map[string]any{}, "gemini", "v1beta", "https://generativelanguage.googleapis.com"},
		{"unknown protocol keeps openai default", map[string]any{}, "unknown", "v9", "https://api.openai.com/v1"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := chainBaseURLOf(testCase.credentials, testCase.protocolCode, testCase.protocolVersion)
			if got != testCase.expected {
				t.Fatalf("chainBaseURLOf(%q, %q) = %q, want %q", testCase.protocolCode, testCase.protocolVersion, got, testCase.expected)
			}
		})
	}
}
