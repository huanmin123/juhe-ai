package main

// BUG-0175 (D-73) hydration regression: oauth/google_oauth accounts carry
// access_token/refresh_token credentials instead of api_key, so the
// api-key-only secret lookup silently dropped every OAuth account before it
// could enter the dispatch candidate list. These tests pin the archive
// runtimeCredentialSource contract (openai-account-selector.repository.ts:
// oauth/google_oauth resolve access_token -> refresh_token) plus the B4 第一批
// 单连接事实源 BaseURL 投影（设计 §2.1「Base URL 属于连接行」：secret 的
// BaseURL 只读 account_connections.base_url，credentials.base_url 依赖解除）。

import (
	"context"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// TestChainAccountsSelectorHydratesOAuthCredentialSource seeds an oauth
// account set next to an api_key regression account and asserts the hydrated
// dispatch secrets: the OAuth credential source, the drop of a tokenless
// oauth row, and the api_key rotation path staying intact. BaseURL 全部来自
// 各账户的连接行（health_check_connection_id 指向的 enabled 连接）。
func TestChainAccountsSelectorHydratesOAuthCredentialSource(t *testing.T) {
	fixture := newChainFixture(t)
	db := fixture.db
	now := "2026-09-04T00:00:00.000Z"
	seed := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(fixtureBind(query), args...); err != nil {
			t.Fatalf("seed row: %v: %v", query, err)
		}
	}
	// seedConnection 按设计 §2.1 写入顺序补一条 enabled 连接并回填
	// health_check_connection_id（accounts 列为目标 schema 形态：
	// vendor_code / connection_driver_code，无 provider_code/
	// provider_protocol_profile_id 旧列）。
	seedConnection := func(accountID, connectionID, baseURL string) {
		t.Helper()
		seed(`INSERT INTO account_connections (id, account_id, connection_preset_code, protocol_surface_code, base_url, enabled, created_at, updated_at)
			VALUES (?, ?, 'gpt_openai_v1', 'openai_v1', ?, 1, ?, ?)`, connectionID, accountID, baseURL, now, now)
		seed(`UPDATE accounts SET health_check_connection_id = ? WHERE id = ?`, connectionID, accountID)
	}
	insertAccount := func(id, name, accountType string, credentials map[string]any, connectionID, connectionBaseURL string) {
		t.Helper()
		encrypted, err := accounts.EncryptJSON("chain-test-secret", credentials)
		if err != nil {
			t.Fatalf("encrypt credentials for %s: %v", id, err)
		}
		seed(`INSERT INTO accounts (
				id, system_account_id, vendor_code, connection_driver_code, protocol_code, protocol_version,
				name, type, status, schedulable, credentials_encrypted, deleted_at, health_check_model
			) VALUES (?, ?, 'gpt', 'gpt', 'openai', 'v1', ?, ?, 'active', 1, ?, NULL, 'gpt-test')`,
			id, fixture.systemAccount, name, accountType, encrypted)
		seed(`INSERT INTO group_accounts (group_id, system_account_id, account_id, enabled, created_at)
			VALUES (?, ?, ?, 1, ?)`, fixture.groupID, fixture.systemAccount, id, now)
		if connectionID != "" {
			seedConnection(id, connectionID, connectionBaseURL)
		}
	}

	insertAccount("acc_oauth_access", "OAuth访问令牌", "oauth", map[string]any{
		"access_token":  "oauth-access-token",
		"refresh_token": "oauth-refresh-token",
		"account_id":    "chatgpt-account-1",
		"client_id":     "oauth-client-1",
	}, "conn_oauth_access", "https://oauth-access.example.invalid/v1")
	insertAccount("acc_oauth_refresh", "OAuth仅刷新令牌", "oauth", map[string]any{
		"refresh_token": "oauth-refresh-only",
	}, "conn_oauth_refresh", "https://oauth-refresh.example.invalid/v1")
	insertAccount("acc_google_oauth", "GoogleOAuth访问令牌", "google_oauth", map[string]any{
		"access_token": "google-access-token",
	}, "conn_google_oauth", "https://google-oauth.example.invalid/v1")
	// api_key regression: the rotation lookup must keep working unchanged.
	// credentials.base_url 保留旧格式值：单连接事实源下它不再是 BaseURL 来源，
	// 连接行 URL 胜出（契约钉住）。
	insertAccount("acc_apikey_regression", "APIKey回归", "api_key", map[string]any{
		"api_key":  "sk-regression-key",
		"base_url": "https://regression.example/v1",
	}, "conn_apikey_regression", "https://connection-wins.example.invalid/v1")
	// Neither access_token nor refresh_token: mirrors the Node undefined
	// return (the account still drops at hydration).
	insertAccount("acc_oauth_empty", "OAuth无令牌", "oauth", map[string]any{
		"account_id": "chatgpt-account-2",
	}, "", "")

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
	// BaseURL 来自 health_check_connection_id 指向的连接行。
	if oauthAccess.BaseURL != "https://oauth-access.example.invalid/v1" {
		t.Fatalf("oauth baseUrl = %q (connection row expected)", oauthAccess.BaseURL)
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
	if oauthRefresh.BaseURL != "https://oauth-refresh.example.invalid/v1" {
		t.Fatalf("refresh-only oauth baseUrl = %q (connection row expected)", oauthRefresh.BaseURL)
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
	if google.BaseURL != "https://google-oauth.example.invalid/v1" {
		t.Fatalf("google_oauth baseUrl = %q (connection row expected)", google.BaseURL)
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
	// 单连接事实源：credentials.base_url 不再是 BaseURL 来源，连接行胜出。
	if regression.BaseURL != "https://connection-wins.example.invalid/v1" {
		t.Fatalf("api_key regression baseUrl = %q (connection row must win over credentials.base_url)", regression.BaseURL)
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

// TestChainSecretBaseURLFromConnectionFacts 钉住单连接事实源的三条选择口径与
// fail closed 语义（与 jobs accounthealth resolved_health_connections 同口径；
// 设计 §2.1「每个账户至少 1 行连接」被破坏的半成品数据不虚构 URL）：
//  1. health_check_connection_id 指向的 enabled 连接优先（id 序无关）；
//  2. 无 hc 引用时 enabled 连接按 id 升序首个；
//  3. hc 指向停用连接时该连接不参与（回落 enabled id 升序首个）；
//  4. 无任何 enabled 连接 → 账户在 hydration 丢弃（nil without error）；
//  5. hc 连接的 account_endpoint_modes 实际集合成为 SupportedEndpointModes
//     （连接行事实源优先于 vendor 默认分支）。
func TestChainSecretBaseURLFromConnectionFacts(t *testing.T) {
	fixture := newChainFixture(t)
	db := fixture.db
	now := "2026-09-04T00:00:00.000Z"
	seed := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(fixtureBind(query), args...); err != nil {
			t.Fatalf("seed row: %v: %v", query, err)
		}
	}
	insertAccount := func(id string) {
		t.Helper()
		encrypted, err := accounts.EncryptJSON("chain-test-secret", map[string]any{"api_key": "sk-" + id})
		if err != nil {
			t.Fatalf("encrypt credentials for %s: %v", id, err)
		}
		seed(`INSERT INTO accounts (
				id, system_account_id, vendor_code, connection_driver_code, protocol_code, protocol_version,
				name, type, status, schedulable, credentials_encrypted, deleted_at, health_check_model
			) VALUES (?, ?, 'gpt', 'gpt', 'openai', 'v1', ?, 'api_key', 'active', 1, ?, NULL, 'gpt-test')`,
			id, fixture.systemAccount, id, encrypted)
		seed(`INSERT INTO group_accounts (group_id, system_account_id, account_id, enabled, created_at)
			VALUES (?, ?, ?, 1, ?)`, fixture.groupID, fixture.systemAccount, id, now)
	}
	seedConnection := func(accountID, connectionID, baseURL string, enabled int, modes []string) {
		t.Helper()
		seed(`INSERT INTO account_connections (id, account_id, connection_preset_code, protocol_surface_code, base_url, enabled, created_at, updated_at)
			VALUES (?, ?, 'gpt_openai_v1', 'openai_v1', ?, ?, ?, ?)`, connectionID, accountID, baseURL, enabled, now, now)
		for _, mode := range modes {
			seed(`INSERT INTO account_endpoint_modes (connection_id, endpoint_mode) VALUES (?, ?)`, connectionID, mode)
		}
	}

	// 1. hc 优先：enabled 连接 id 升序首个是 conn_prio_a，但 hc 指向
	// conn_prio_z —— BaseURL 必须取 conn_prio_z，且 mode 集取其 modes。
	insertAccount("acc_conn_prio")
	seedConnection("acc_conn_prio", "conn_prio_a", "https://prio-first.example.invalid/v1", 1, nil)
	seedConnection("acc_conn_prio", "conn_prio_z", "https://prio-healthcheck.example.invalid/v1", 1,
		[]string{"chat_json", "chat_sse"})
	seed(`UPDATE accounts SET health_check_connection_id = 'conn_prio_z' WHERE id = 'acc_conn_prio'`)

	// 2. 无 hc：enabled 连接按 id 升序首个。
	insertAccount("acc_conn_fallback")
	seedConnection("acc_conn_fallback", "conn_fb_b", "https://fb-second.example.invalid/v1", 1, nil)
	seedConnection("acc_conn_fallback", "conn_fb_a", "https://fb-first.example.invalid/v1", 1, nil)

	// 3. hc 指向停用连接：该连接不参与，回落 enabled id 升序首个。
	insertAccount("acc_conn_disabled_hc")
	seedConnection("acc_conn_disabled_hc", "conn_dh_disabled", "https://dh-disabled.example.invalid/v1", 0, nil)
	seedConnection("acc_conn_disabled_hc", "conn_dh_enabled", "https://dh-enabled.example.invalid/v1", 1, nil)
	seed(`UPDATE accounts SET health_check_connection_id = 'conn_dh_disabled' WHERE id = 'acc_conn_disabled_hc'`)

	// 4. 无任何连接：fail closed（hydration 丢弃）。
	insertAccount("acc_conn_missing")

	result, err := fixture.selector.ListOpenAIAccountsForGroupResult(context.Background(), fixture.groupID, fixture.systemAccount,
		gatewayruntimecache.OpenAIAccountsForGroupOptions{})
	if err != nil {
		t.Fatalf("list accounts: %v", err)
	}
	byID := map[string]*gatewayruntimecache.OpenAIAccountSecret{}
	for index := range result.Accounts {
		byID[result.Accounts[index].ID] = &result.Accounts[index]
	}

	if prio := byID["acc_conn_prio"]; prio == nil {
		t.Fatal("hc-priority account missing from result")
	} else {
		if prio.BaseURL != "https://prio-healthcheck.example.invalid/v1" {
			t.Fatalf("hc priority baseUrl = %q", prio.BaseURL)
		}
		// 5. hc 连接 modes 实际集合成为 SupportedEndpointModes。
		if len(prio.SupportedEndpointModes) != 2 || prio.SupportedEndpointModes[0] != "chat_json" || prio.SupportedEndpointModes[1] != "chat_sse" {
			t.Fatalf("hc connection modes = %v", prio.SupportedEndpointModes)
		}
	}
	if fallback := byID["acc_conn_fallback"]; fallback == nil {
		t.Fatal("id-asc fallback account missing from result")
	} else if fallback.BaseURL != "https://fb-first.example.invalid/v1" {
		t.Fatalf("id-asc fallback baseUrl = %q", fallback.BaseURL)
	}
	if disabledHC := byID["acc_conn_disabled_hc"]; disabledHC == nil {
		t.Fatal("disabled-hc account missing from result")
	} else if disabledHC.BaseURL != "https://dh-enabled.example.invalid/v1" {
		t.Fatalf("disabled-hc fallback baseUrl = %q", disabledHC.BaseURL)
	}
	if _, present := byID["acc_conn_missing"]; present {
		t.Fatal("connection-less account must be dropped at hydration (fail closed)")
	}
	diagnostics := result.Diagnostics
	if diagnostics == nil {
		t.Fatal("diagnostics missing")
	}
	// fixture acc_1 + 3 hydrated connection-fact accounts；无连接账户丢弃不计入。
	if diagnostics.HydrationDroppedCount != 1 {
		t.Fatalf("hydrationDroppedCount = %d", diagnostics.HydrationDroppedCount)
	}
	if diagnostics.FinalAccountCount != 4 {
		t.Fatalf("finalAccountCount = %d (fixture + 3 hydrated, dropped excluded)", diagnostics.FinalAccountCount)
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

// TestChainBaseURLOfTransitionalMediaFace pins the transitional media-face
// helper contract: chainBaseURLOf is no longer consumed by the secret
// assembly（B4 第一批单连接事实源，secret 走连接行 fail closed），仅剩媒体任务
// 面 hydrateAccount 一个消费方（连接事实源转换归媒体任务面切片）。
func TestChainBaseURLOfTransitionalMediaFace(t *testing.T) {
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
