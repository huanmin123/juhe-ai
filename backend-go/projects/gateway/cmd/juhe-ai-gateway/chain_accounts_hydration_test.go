package main

// G20 phase-3 account selector hydration assertions: the field-by-field
// contract of the full openAIAccountSecretFromRow port (rotation transient,
// fresh quality score, proxy profile, diagnostics, authorizations).

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// seedHydratedAccount extends the fixture with a second account that carries
// a multi-key rotation pool, a proxy profile, runtime states and quality
// rows, plus an authorized third account exercising the access resolution.
func TestChainAccountsSelectorHydratesFullSecret(t *testing.T) {
	fixture := newChainFixture(t)
	db := fixture.db
	now := "2026-09-04T00:00:00.000Z"
	seed := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatalf("seed row: %v: %v", query, err)
		}
	}

	// Proxy profile (Node proxy_profiles row with an encrypted password).
	proxyPassword, err := accounts.EncryptJSON("chain-test-secret", map[string]any{"password": "proxy-pass"})
	if err != nil {
		t.Fatalf("encrypt proxy password: %v", err)
	}
	seed(`INSERT INTO proxy_profiles (id, name, type, host, port, username, password_encrypted, enabled, created_at, updated_at)
		VALUES ('proxy_1', '出口一', 'http', '10.0.0.8', 8080, 'alice', ?, 1, ?, ?)`, proxyPassword, now, now)

	// Rotation pool account: two api keys with weights + one disabled
	// runtime state + a proxy profile binding.
	credentials, err := accounts.EncryptJSON("chain-test-secret", map[string]any{
		"api_keys":         []any{"sk-pool-key-a", "sk-pool-key-b", "sk-pool-key-a"},
		"api_key_weights":  []any{2.0, 3.0},
		"base_url":         "https://pool.example.com/v1",
		"api_key_strategy": "weighted_round_robin",
	})
	if err != nil {
		t.Fatalf("encrypt credentials: %v", err)
	}
	seed(`INSERT INTO accounts (
			id, system_account_id, provider_code, provider_protocol_profile_id, protocol_code, protocol_version,
			name, type, status, schedulable, concurrency_limit, priority,
			credentials_encrypted, proxy_profile_id, cooldown_until, last_error_message,
			stream_failure_count, stream_failure_window_started_at, deleted_at, health_check_model, health_check_endpoint_mode
		) VALUES ('acc_pool', ?, 'openai', 'prof_pool', 'openai', 'v1',
			'池化账户', 'api_key', 'active', 1, 7, 3,
			?, 'proxy_1', '2020-01-01T00:00:00.000Z', '上游已限流',
			4, '2026-09-03T10:00:00.000Z', NULL, 'gpt-test', 'chat_json')`,
		fixture.systemAccount, credentials)
	seed(`INSERT INTO group_accounts (group_id, system_account_id, account_id, enabled, local_priority, created_at)
		VALUES (?, ?, 'acc_pool', 1, 2, ?)`, fixture.groupID, fixture.systemAccount, now)
	seed(`INSERT INTO account_api_key_runtime_states (account_id, key_fingerprint, key_index, status, cooldown_until, recovery_started_at, updated_at)
		VALUES ('acc_pool', ?, 1, 'temporary_unavailable', '2026-09-04T01:00:00.000Z', '2026-09-03T23:00:00.000Z', ?)`,
		chainFingerprintAPIKey("chain-test-secret", "sk-pool-key-b"), now)

	// Fresh quality row on the stats database (a far-future last_sample_at
	// keeps the 24h freshness window satisfied regardless of the wall clock).
	seedStats := func(query string, args ...any) {
		t.Helper()
		if _, err := fixture.statsDB.Exec(query, args...); err != nil {
			t.Fatalf("seed stats row: %v: %v", query, err)
		}
	}
	seedStats(`INSERT INTO account_quality_scores (account_id, quality_score, quality_state, ewma_first_token_ms, last_sample_at, updated_at)
		VALUES ('acc_pool', 12.5, 'healthy', 640.5, '2999-01-01T00:00:00.000Z', ?)`, now)

	result, err := fixture.selector.ListOpenAIAccountsForGroupResult(context.Background(), fixture.groupID, fixture.systemAccount,
		gatewayruntimecache.OpenAIAccountsForGroupOptions{})
	if err != nil {
		t.Fatalf("list accounts: %v", err)
	}
	var pool *gatewayruntimecache.OpenAIAccountSecret
	for index := range result.Accounts {
		if result.Accounts[index].ID == "acc_pool" {
			pool = &result.Accounts[index]
		}
	}
	if pool == nil {
		t.Fatalf("pool account missing from result: %+v", result.Diagnostics)
	}

	// Rotation pool projection: deduped api keys, fingerprints, runtime
	// states ride on the secret only when pool isolation is enabled.
	if len(pool.APIKeys) != 2 || pool.APIKeys[0] != "sk-pool-key-a" || pool.APIKeys[1] != "sk-pool-key-b" {
		t.Fatalf("apiKeys = %v", pool.APIKeys)
	}
	if pool.APIKey != "sk-pool-key-a" {
		t.Fatalf("apiKey = %q", pool.APIKey)
	}
	if len(pool.APIKeyRuntimeStates) != 1 {
		t.Fatalf("apiKeyRuntimeStates = %+v", pool.APIKeyRuntimeStates)
	}
	state := pool.APIKeyRuntimeStates[0]
	if state.Fingerprint != chainFingerprintAPIKey("chain-test-secret", "sk-pool-key-b") {
		t.Fatalf("runtime state fingerprint = %q", state.Fingerprint)
	}
	if !state.Disabled {
		t.Fatalf("runtime state (temporary_unavailable) must map to Disabled")
	}
	if state.CooldownUntil == nil || *state.CooldownUntil != "2026-09-04T01:00:00.000Z" {
		t.Fatalf("runtime state cooldownUntil = %v", state.CooldownUntil)
	}
	if state.RecoveryStartedAt == nil || *state.RecoveryStartedAt != "2026-09-03T23:00:00.000Z" {
		t.Fatalf("runtime state recoveryStartedAt = %v", state.RecoveryStartedAt)
	}

	// Proxy profile hydration (Node proxyUrlFromRow).
	if pool.ProxyProfileID == nil || *pool.ProxyProfileID != "proxy_1" {
		t.Fatalf("proxyProfileId = %v", pool.ProxyProfileID)
	}
	if pool.ProxyURL == nil || *pool.ProxyURL != "http://alice:proxy-pass@10.0.0.8:8080" {
		t.Fatalf("proxyUrl = %v", pool.ProxyURL)
	}
	if pool.ProxyProfileUnavailable != nil {
		t.Fatalf("proxyProfileUnavailable = %v", pool.ProxyProfileUnavailable)
	}

	// Fresh quality score from the stats database.
	if pool.QualityScore == nil || *pool.QualityScore != 12.5 {
		t.Fatalf("qualityScore = %v", pool.QualityScore)
	}
	if pool.QualityState == nil || *pool.QualityState != "healthy" {
		t.Fatalf("qualityState = %v", pool.QualityState)
	}
	if pool.QualityEwmaFirstTokenMs == nil || *pool.QualityEwmaFirstTokenMs != 640.5 {
		t.Fatalf("qualityEwmaFirstTokenMs = %v", pool.QualityEwmaFirstTokenMs)
	}

	// Diagnostics + runtime fields.
	if pool.ConcurrencyLimit != 7 || pool.Priority != 2 {
		t.Fatalf("dispatch projection = %d/%d", pool.ConcurrencyLimit, pool.Priority)
	}
	// local_super_priority_enabled / local_fallback_enabled default to 0.
	if pool.SuperPriorityEnabled || pool.FallbackEnabled {
		t.Fatalf("super/fallback = %v/%v", pool.SuperPriorityEnabled, pool.FallbackEnabled)
	}
	if pool.LastErrorMessage == nil || *pool.LastErrorMessage != "上游已限流" {
		t.Fatalf("lastErrorMessage = %v", pool.LastErrorMessage)
	}
	if pool.StreamFailureCount != 4 {
		t.Fatalf("streamFailureCount = %d", pool.StreamFailureCount)
	}
	if pool.StreamFailureWindowStartedAt == nil || *pool.StreamFailureWindowStartedAt != "2026-09-03T10:00:00.000Z" {
		t.Fatalf("streamFailureWindowStartedAt = %v", pool.StreamFailureWindowStartedAt)
	}
	if pool.CooldownUntil == nil || *pool.CooldownUntil != "2020-01-01T00:00:00.000Z" {
		t.Fatalf("cooldownUntil = %v", pool.CooldownUntil)
	}
	if pool.BaseURL != "https://pool.example.com/v1" {
		t.Fatalf("baseUrl = %q", pool.BaseURL)
	}
	if pool.ConfigRevision == nil || *pool.ConfigRevision != 1 {
		t.Fatalf("configRevision = %v (Node Number(x ?? 1))", pool.ConfigRevision)
	}
	// defaultOpenAIEndpointModes: provider 'openai' defaults to the chat
	// endpoint modes (no credentials.supported_endpoint_modes override).
	if len(pool.SupportedEndpointModes) != 2 ||
		pool.SupportedEndpointModes[0] != "chat_json" || pool.SupportedEndpointModes[1] != "chat_sse" {
		t.Fatalf("supportedEndpointModes = %v", pool.SupportedEndpointModes)
	}
	if pool.CredentialSourceAccountID != nil {
		t.Fatalf("credentialSourceAccountId must stay unset for non-authorized accounts: %v", pool.CredentialSourceAccountID)
	}
	if pool.DispatchRevision != nil {
		t.Fatalf("dispatchRevision = %v (NULL column)", pool.DispatchRevision)
	}

	diagnostics := result.Diagnostics
	if diagnostics == nil {
		t.Fatal("diagnostics missing")
	}
	if diagnostics.FinalLimit != 20 || diagnostics.ScanLimit != 200 || diagnostics.HydrationBatchCount != 1 {
		t.Fatalf("diagnostics limits = %+v", diagnostics)
	}
	if diagnostics.CandidateRowCount != 2 || diagnostics.EligibleRowCount != 2 || diagnostics.FinalAccountCount != 2 {
		t.Fatalf("diagnostics counts = %+v", diagnostics)
	}
	if diagnostics.ScanLimitReached {
		t.Fatal("scanLimitReached must be false for two rows")
	}
}

// TestChainAccountsSelectorModelRankWindow covers the model-candidate
// window: the direct-model account ranks before the mapping account and the
// no-supported-model account, and unrelated accounts drop.
func TestChainAccountsSelectorModelRankWindow(t *testing.T) {
	fixture := newChainFixture(t)
	db := fixture.db
	now := "2026-09-04T00:00:00.000Z"
	seed := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatalf("seed row: %v: %v", query, err)
		}
	}
	credentials, err := accounts.EncryptJSON("chain-test-secret", map[string]any{"api_key": "sk-upstream"})
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	// Mapped account: serves gpt-test through an enabled mapping.
	seed(`INSERT INTO accounts (
			id, system_account_id, provider_code, protocol_code, protocol_version, name, type, status, schedulable,
			credentials_encrypted, deleted_at, health_check_model
		) VALUES ('acc_mapped', ?, 'openai', 'openai', 'v1', '映射账户', 'api_key', 'active', 1, ?, NULL, 'gpt-test')`,
		fixture.systemAccount, credentials)
	seed(`INSERT INTO group_accounts (group_id, system_account_id, account_id, enabled, created_at)
		VALUES (?, ?, 'acc_mapped', 1, ?)`, fixture.groupID, fixture.systemAccount, now)
	seed(`INSERT INTO account_model_mappings (id, account_id, provider_code, source_model, source_endpoint_family, upstream_model, upstream_endpoint_family, enabled, created_at, updated_at)
		VALUES ('map_1', 'acc_mapped', 'openai', 'gpt-pro', 'chat_completions', 'gpt-test', 'chat_completions', 1, ?, ?)`, now, now)

	result, err := fixture.selector.ListOpenAIAccountsForGroupResult(context.Background(), fixture.groupID, fixture.systemAccount,
		gatewayruntimecache.OpenAIAccountsForGroupOptions{RequestedModel: "gpt-pro", RequestedEndpointFamily: "chat_completions"})
	if err != nil {
		t.Fatalf("list accounts: %v", err)
	}
	if len(result.Accounts) != 2 {
		t.Fatalf("accounts = %d (mapped + direct)", len(result.Accounts))
	}
	if result.Accounts[0].ID != "acc_mapped" || result.Accounts[1].ID != fixture.accountID {
		t.Fatalf("model rank order = %v, %v", result.Accounts[0].ID, result.Accounts[1].ID)
	}
	if result.Diagnostics == nil || result.Diagnostics.EligibleRowCount != 2 {
		t.Fatalf("diagnostics = %+v", result.Diagnostics)
	}

	// An unknown model ranks the no-supported-model account class first and
	// keeps the base-window remainder behind it (Node merge behaviour: the
	// base window re-adds the rank-3 account behind the ranked one).
	unknown, err := fixture.selector.ListOpenAIAccountsForGroupResult(context.Background(), fixture.groupID, fixture.systemAccount,
		gatewayruntimecache.OpenAIAccountsForGroupOptions{RequestedModel: "gpt-missing"})
	if err != nil {
		t.Fatalf("list unknown model accounts: %v", err)
	}
	if len(unknown.Accounts) != 2 {
		t.Fatalf("unknown model accounts = %d", len(unknown.Accounts))
	}
	if unknown.Accounts[0].ID != "acc_mapped" || unknown.Accounts[1].ID != fixture.accountID {
		t.Fatalf("unknown model order = %v, %v", unknown.Accounts[0].ID, unknown.Accounts[1].ID)
	}
}

// TestChainAccountsSelectorUnavailableCooldown covers the availability
// gates: cooldown rows leave the default window and return with
// includeUnavailable, carrying the unavailable status projection.
func TestChainAccountsSelectorUnavailableCooldown(t *testing.T) {
	fixture := newChainFixture(t)
	db := fixture.db
	if _, err := db.Exec(`UPDATE accounts SET status = 'temporary_unavailable', cooldown_until = '2999-01-01T00:00:00.000Z' WHERE id = ?`, fixture.accountID); err != nil {
		t.Fatalf("cooldown account: %v", err)
	}
	defaultWindow, err := fixture.selector.ListOpenAIAccountsForGroupResult(context.Background(), fixture.groupID, fixture.systemAccount, gatewayruntimecache.OpenAIAccountsForGroupOptions{})
	if err != nil {
		t.Fatalf("default window: %v", err)
	}
	if len(defaultWindow.Accounts) != 0 {
		t.Fatalf("default window accounts = %d", len(defaultWindow.Accounts))
	}
	includeUnavailable, err := fixture.selector.ListOpenAIAccountsForGroupResult(context.Background(), fixture.groupID, fixture.systemAccount,
		gatewayruntimecache.OpenAIAccountsForGroupOptions{IncludeUnavailable: true})
	if err != nil {
		t.Fatalf("includeUnavailable window: %v", err)
	}
	if len(includeUnavailable.Accounts) != 1 || includeUnavailable.Accounts[0].Status != "temporary_unavailable" {
		t.Fatalf("includeUnavailable accounts = %+v", includeUnavailable.Accounts)
	}
	if !includeUnavailable.Diagnostics.ScanLimitReached {
		if includeUnavailable.Diagnostics.CandidateRowCount != 1 {
			t.Fatalf("diagnostics = %+v", includeUnavailable.Diagnostics)
		}
	}
}

// TestChainAccountsSelectorGroupAuthorization covers the authorized-group
// branch: group access metadata + the authorized account projection.
func TestChainAccountsSelectorGroupAuthorization(t *testing.T) {
	fixture := newChainFixture(t)
	db := fixture.db
	now := "2026-09-04T00:00:00.000Z"
	seed := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatalf("seed row: %v: %v", query, err)
		}
	}
	grantee := "sys_grantee"
	seed(`INSERT INTO system_accounts (id, status, image_generation_enabled) VALUES (?, 'active', 1)`, grantee)
	seed(`INSERT INTO resource_authorizations (
			id, resource_type, resource_id, resource_owner_system_account_id, grantee_system_account_id,
			scope, status, effective_source_type, effective_source_team_id, expires_at, limits_json
		) VALUES ('authz_group', 'group', ?, ?, ?, 'use', 'active', 'team', 'team_1', '2999-01-01T00:00:00.000Z',
			'{"daily":{"enabled":true,"limit":100}}')`, fixture.groupID, fixture.systemAccount, grantee)

	groupAccess, err := fixture.selector.resolveGroupAccess(context.Background(), fixture.groupID, grantee)
	if err != nil {
		t.Fatalf("resolve group access: %v", err)
	}
	if groupAccess == nil {
		t.Fatal("group access missing for grantee")
	}
	if groupAccess.GroupAccessType != "authorized" || groupAccess.GroupAuthorizationID == nil || *groupAccess.GroupAuthorizationID != "authz_group" {
		t.Fatalf("group access = %+v", groupAccess)
	}
	if groupAccess.GroupAuthorizationQuotaLimited == nil || !*groupAccess.GroupAuthorizationQuotaLimited {
		t.Fatalf("quota limited = %v", groupAccess.GroupAuthorizationQuotaLimited)
	}
	if groupAccess.GroupAuthorizationSourceType == nil || *groupAccess.GroupAuthorizationSourceType != "team" {
		t.Fatalf("source type = %v", groupAccess.GroupAuthorizationSourceType)
	}
	if groupAccess.GroupAuthorizationSourceTeamID == nil || *groupAccess.GroupAuthorizationSourceTeamID != "team_1" {
		t.Fatalf("source team = %v", groupAccess.GroupAuthorizationSourceTeamID)
	}
	result, err := fixture.selector.ListOpenAIAccountsForGroupResult(context.Background(), fixture.groupID, grantee,
		gatewayruntimecache.OpenAIAccountsForGroupOptions{PreResolvedGroupAccess: groupAccess})
	if err != nil {
		t.Fatalf("list accounts for grantee: %v", err)
	}
	if len(result.Accounts) != 1 {
		t.Fatalf("grantee accounts = %d", len(result.Accounts))
	}
	account := result.Accounts[0]
	if account.AccountAccessType != "group_authorized" {
		t.Fatalf("accountAccessType = %q", account.AccountAccessType)
	}
	if account.GroupAuthorizationID == nil || *account.GroupAuthorizationID != "authz_group" {
		t.Fatalf("groupAuthorizationId = %v", account.GroupAuthorizationID)
	}
	if !strings.Contains(account.GroupOwnerSystemAccountID, "sys_owner") {
		t.Fatalf("groupOwnerSystemAccountId = %q", account.GroupOwnerSystemAccountID)
	}
	_ = now
	_ = time.Now
}
