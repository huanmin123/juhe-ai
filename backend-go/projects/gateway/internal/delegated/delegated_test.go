package delegated

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/apikeys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/groups"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/oidc"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/routestrategies"
)

// ---------------------------------------------------------------------------
// Fixed clock + test environment (one in-memory SQLite database wiring every
// Deps collaborator; no real network, httptest only).
// ---------------------------------------------------------------------------

var clockStart = time.Date(2026, 1, 10, 8, 30, 0, 0, time.UTC)

// contextDeadlineError simulates the runtime-state read timeout (Node
// runRedisOperationWithDeadline).
var contextDeadlineError = errors.New("redis read deadline exceeded")

type fakeClock struct{ current time.Time }

func newFakeClock() *fakeClock { return &fakeClock{current: clockStart} }

func (c *fakeClock) Now() time.Time          { return c.current }
func (c *fakeClock) Advance(d time.Duration) { c.current = c.current.Add(d) }

func randomHex(t *testing.T) string {
	t.Helper()
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(buf)
}

type env struct {
	t          *testing.T
	db         *sql.DB
	server     *httptest.Server
	clock      *fakeClock
	tokens     *oidc.Store
	groups     *groups.Store
	strategies *routestrategies.Store
	apiKeys    *apikeys.Store
	accts      *accounts.Store
	deps       *Deps
}

// delegatedSchema mirrors the maintenance SQLite business schema subset the
// delegated collaborators read and write (groups M05, route-strategies M06,
// api-keys M07, accounts M08-M10, oidc provider store).
var delegatedSchema = []string{
	`CREATE TABLE IF NOT EXISTS system_accounts (id TEXT PRIMARY KEY, username TEXT NOT NULL UNIQUE, display_name TEXT NOT NULL, description TEXT, role TEXT NOT NULL DEFAULT 'user', status TEXT NOT NULL DEFAULT 'active', password_hash TEXT NOT NULL, must_change_password INTEGER NOT NULL DEFAULT 0, image_generation_enabled INTEGER NOT NULL DEFAULT 0, ai_account_limit INTEGER, request_limits_json TEXT, last_login_at TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS providers (id TEXT PRIMARY KEY, code TEXT NOT NULL UNIQUE, name TEXT NOT NULL, description TEXT, parent_code TEXT, enabled INTEGER NOT NULL DEFAULT 1, default_supported_models_json TEXT NOT NULL DEFAULT '[]', created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS provider_protocol_profiles (id TEXT PRIMARY KEY, provider_code TEXT NOT NULL, name TEXT NOT NULL, description TEXT, enabled INTEGER NOT NULL DEFAULT 1, protocol_code TEXT NOT NULL, protocol_version TEXT NOT NULL, base_url TEXT NOT NULL, default_health_check_model TEXT NOT NULL, account_types_json TEXT NOT NULL, capabilities_json TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS proxy_profiles (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, name TEXT NOT NULL, description TEXT, type TEXT NOT NULL, host TEXT NOT NULL, port INTEGER NOT NULL, username TEXT, password_encrypted TEXT, enabled INTEGER NOT NULL DEFAULT 1, test_status TEXT NOT NULL DEFAULT 'unknown', latency_ms INTEGER, outbound_ip TEXT, outbound_region TEXT, last_test_message TEXT, last_tested_at TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS groups (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, name TEXT NOT NULL, provider_code TEXT NOT NULL, description TEXT, enabled INTEGER NOT NULL DEFAULT 1, is_default INTEGER NOT NULL DEFAULT 0, group_type TEXT NOT NULL DEFAULT 'personal', scheduling_policy_json TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_groups_owner_provider_name_unique ON groups(system_account_id, provider_code, name)`,
	`CREATE TABLE IF NOT EXISTS group_accounts (
		system_account_id TEXT NOT NULL, group_id TEXT NOT NULL, account_id TEXT NOT NULL,
		account_authorization_id TEXT, local_priority INTEGER NOT NULL DEFAULT 0,
		local_super_priority_enabled INTEGER NOT NULL DEFAULT 0, local_fallback_enabled INTEGER NOT NULL DEFAULT 0,
		enabled INTEGER NOT NULL DEFAULT 1, created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
		PRIMARY KEY (group_id, account_id)
	)`,
	`CREATE TABLE IF NOT EXISTS group_authorization_settings (authorization_id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, group_id TEXT NOT NULL, enabled INTEGER NOT NULL DEFAULT 1, group_type TEXT NOT NULL DEFAULT 'personal', scheduling_policy_json TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS group_account_stats_dirty (group_id TEXT PRIMARY KEY, reason TEXT, updated_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS route_strategies (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, name TEXT NOT NULL, description TEXT, mode TEXT NOT NULL DEFAULT 'normal', status TEXT NOT NULL DEFAULT 'active', is_default INTEGER NOT NULL DEFAULT 0, config_json TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS route_strategy_groups (id TEXT PRIMARY KEY, route_strategy_id TEXT NOT NULL, system_account_id TEXT NOT NULL, group_id TEXT NOT NULL, priority INTEGER NOT NULL DEFAULT 1, weight INTEGER NOT NULL DEFAULT 1, status TEXT NOT NULL DEFAULT 'active', created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS api_keys (
		id TEXT PRIMARY KEY,
		system_account_id TEXT NOT NULL,
		route_strategy_id TEXT NOT NULL,
		name TEXT NOT NULL,
		description TEXT,
		key_hash TEXT NOT NULL UNIQUE,
		key_prefix TEXT NOT NULL,
		key_suffix TEXT NOT NULL,
		key_secret_encrypted TEXT NOT NULL,
		status TEXT NOT NULL DEFAULT 'active',
		is_default INTEGER NOT NULL DEFAULT 0,
		purpose TEXT NOT NULL DEFAULT 'general',
		expires_at TEXT,
		quota_limits_json TEXT,
		availability_schedule_json TEXT,
		availability_schedule_next_check_at TEXT,
		last_used_at TEXT,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_api_keys_owner_name_unique ON api_keys(system_account_id, name)`,
	`CREATE TABLE IF NOT EXISTS request_quota_hourly_window_scope_bindings (
		system_account_id TEXT NOT NULL,
		scope_type TEXT NOT NULL,
		scope_id TEXT NOT NULL,
		source_type TEXT NOT NULL,
		source_id TEXT NOT NULL,
		window_hours INTEGER NOT NULL,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		PRIMARY KEY (system_account_id, scope_type, scope_id)
	)`,
	`CREATE TABLE IF NOT EXISTS api_key_record_cleanup_targets (
		api_key_id TEXT PRIMARY KEY,
		system_account_id TEXT NOT NULL,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		attempt_count INTEGER NOT NULL DEFAULT 0,
		last_attempt_at TEXT,
		last_blocked_reason TEXT,
		last_error_message TEXT
	)`,
	`CREATE TABLE IF NOT EXISTS accounts (
		id TEXT PRIMARY KEY,
		config_revision INTEGER NOT NULL DEFAULT 1,
		dispatch_revision INTEGER NOT NULL DEFAULT 1,
		circuit_projection_revision INTEGER NOT NULL DEFAULT 0,
		system_account_id TEXT NOT NULL,
		provider_code TEXT NOT NULL,
		provider_protocol_profile_id TEXT NOT NULL,
		protocol_code TEXT NOT NULL,
		protocol_version TEXT NOT NULL,
		name TEXT NOT NULL,
		type TEXT NOT NULL,
		status TEXT NOT NULL DEFAULT 'pending_test',
		credentials_encrypted TEXT NOT NULL,
		credential_fingerprint TEXT,
		credential_mask TEXT NOT NULL DEFAULT '',
		oauth_access_token_expires_at TEXT,
		oauth_refresh_token_present INTEGER NOT NULL DEFAULT 0,
		proxy_profile_id TEXT,
		concurrency_limit INTEGER NOT NULL DEFAULT 5000,
		priority INTEGER NOT NULL DEFAULT 0,
		super_priority_enabled INTEGER NOT NULL DEFAULT 0,
		fallback_enabled INTEGER NOT NULL DEFAULT 0,
		client_compatibility TEXT NOT NULL DEFAULT 'openai_standard',
		schedulable INTEGER NOT NULL DEFAULT 1,
		availability_schedule_json TEXT,
		availability_schedule_next_check_at TEXT,
		notes TEXT,
		account_expires_at TEXT,
		last_used_at TEXT,
		cooldown_until TEXT,
		last_error_code TEXT,
		last_error_message TEXT,
		last_error_trace_id TEXT,
		health_check_model TEXT NOT NULL DEFAULT '',
		health_check_endpoint_mode TEXT NOT NULL DEFAULT 'chat_json',
		health_check_failure_count INTEGER NOT NULL DEFAULT 0,
		health_check_failure_started_at TEXT,
		cooldown_retest_failure_count INTEGER NOT NULL DEFAULT 0,
		cooldown_retest_observation_started_at TEXT,
		cooldown_retest_last_at TEXT,
		cooldown_retest_last_status_code INTEGER,
		last_health_check_at TEXT,
		last_health_success_at TEXT,
		last_health_check_status_code INTEGER,
		last_health_check_error_code TEXT,
		last_health_check_error_message TEXT,
		last_health_check_trace_id TEXT,
		temporary_unavailable_continuous_probe_enabled INTEGER NOT NULL DEFAULT 1,
		next_health_check_at TEXT,
		balance_query_enabled INTEGER NOT NULL DEFAULT 0,
		balance_query_next_refresh_at TEXT,
		balance_query_config_json TEXT NOT NULL DEFAULT '{}',
		authorization_instance_source_account_id TEXT,
		authorization_instance_authorization_id TEXT,
		deleted_at TEXT,
		deleted_by TEXT,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_accounts_owner_name_unique ON accounts(system_account_id, name) WHERE deleted_at IS NULL`,
	// accounts.ListPage joins resource_authorizations unconditionally
	// (list.go listJoins, the M10 authorized-instance overlay); the DDL
	// mirrors the accounts package test fixture.
	`CREATE TABLE IF NOT EXISTS resource_authorizations (id TEXT PRIMARY KEY, resource_type TEXT NOT NULL, resource_id TEXT NOT NULL, resource_owner_system_account_id TEXT NOT NULL, grantee_system_account_id TEXT NOT NULL, scope TEXT NOT NULL DEFAULT 'use', status TEXT NOT NULL DEFAULT 'active', effective_source_type TEXT, effective_source_team_id TEXT, activated_at TEXT, last_source_changed_at TEXT, remark TEXT, expires_at TEXT, limits_json TEXT, created_by TEXT NOT NULL, created_at TEXT NOT NULL, revoked_by TEXT, revoked_at TEXT, revoked_reason TEXT, updated_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS account_supported_models (
		account_id TEXT NOT NULL, provider_code TEXT NOT NULL, model TEXT NOT NULL, created_at TEXT NOT NULL,
		PRIMARY KEY (account_id, model)
	)`,
	`CREATE TABLE IF NOT EXISTS account_model_mappings (
		account_id TEXT NOT NULL, provider_code TEXT NOT NULL, source_model TEXT NOT NULL,
		source_endpoint_family TEXT NOT NULL, upstream_model TEXT NOT NULL, upstream_endpoint_family TEXT NOT NULL,
		enabled INTEGER NOT NULL DEFAULT 1, created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
		PRIMARY KEY (account_id, source_model, source_endpoint_family)
	)`,
	`CREATE TABLE IF NOT EXISTS account_tags (
		id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, name TEXT NOT NULL,
		created_at TEXT NOT NULL, updated_at TEXT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS account_tag_bindings (
		account_id TEXT NOT NULL, tag_id TEXT NOT NULL, system_account_id TEXT NOT NULL, created_at TEXT NOT NULL,
		PRIMARY KEY (account_id, tag_id)
	)`,
	`CREATE TABLE IF NOT EXISTS account_name_search_terms (
		account_id TEXT NOT NULL, system_account_id TEXT NOT NULL, term TEXT NOT NULL, created_at TEXT NOT NULL,
		PRIMARY KEY (account_id, term)
	)`,
	`CREATE TABLE IF NOT EXISTS account_name_search_documents (
		account_id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, normalized_name TEXT NOT NULL, updated_at TEXT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS account_lock_states (
		account_id TEXT PRIMARY KEY,
		enabled INTEGER NOT NULL DEFAULT 0 CHECK (enabled IN (0, 1)),
		lock_state TEXT NOT NULL DEFAULT 'UNLOCKED' CHECK (lock_state IN ('UNLOCKED', 'LOCKED_IDLE', 'ENGAGED', 'DEAD_CONFIRMED')),
		lock_death_timeout_seconds INTEGER NOT NULL DEFAULT 300 CHECK (lock_death_timeout_seconds BETWEEN 30 AND 3600),
		lock_retry_interval_seconds INTEGER NOT NULL DEFAULT 5 CHECK (lock_retry_interval_seconds BETWEEN 5 AND 30),
		incident_id TEXT,
		generation INTEGER NOT NULL DEFAULT 0 CHECK (generation >= 0),
		incident_started_at TEXT,
		deadline_at TEXT,
		original_status TEXT,
		provenance TEXT,
		next_retry_at_ms INTEGER,
		lease_id TEXT,
		lease_until_ms INTEGER,
		updated_at TEXT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS oauth_clients (
		id TEXT PRIMARY KEY,
		client_id TEXT NOT NULL UNIQUE,
		display_name TEXT NOT NULL,
		client_type TEXT NOT NULL,
		client_secret_hash TEXT,
		client_secret_ciphertext TEXT,
		redirect_uris_json TEXT NOT NULL,
		allowed_scopes_json TEXT NOT NULL,
		status TEXT NOT NULL DEFAULT 'active',
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS oauth_grants (
		id TEXT PRIMARY KEY,
		client_id TEXT NOT NULL,
		system_account_id TEXT NOT NULL,
		scopes_json TEXT NOT NULL,
		expires_at TEXT NOT NULL,
		revoked_at TEXT,
		created_at TEXT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS oauth_access_tokens (
		id TEXT PRIMARY KEY,
		token_hash TEXT NOT NULL UNIQUE,
		client_id TEXT NOT NULL,
		grant_id TEXT NOT NULL,
		issued_at TEXT NOT NULL,
		expires_at TEXT NOT NULL,
		revoked_at TEXT,
		replaced_at TEXT,
		successor_token_id TEXT,
		created_at TEXT NOT NULL
	)`,
}

func execSchema(t *testing.T, db *sql.DB, statements []string) {
	t.Helper()
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
}

func newEnv(t *testing.T) *env {
	t.Helper()
	clock := newFakeClock()
	db, err := sql.Open("sqlite", "file:delegated-"+strings.ReplaceAll(t.Name(), "/", "-")+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	// One connection: shared-cache in-memory databases lose tables across
	// pooled connections.
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	execSchema(t, db, delegatedSchema)
	return assembleEnv(t, db, clock)
}

func assembleEnv(t *testing.T, db *sql.DB, clock *fakeClock) *env {
	t.Helper()
	tokenStore, err := oidc.NewStore(db, false, clock.Now, "delegated-test-secret")
	if err != nil {
		t.Fatal(err)
	}
	groupStore, err := groups.NewStore(db, false, clock.Now, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	strategyStore, err := routestrategies.NewStore(db, false, clock.Now, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	apiKeyStore, err := apikeys.NewStore(db, false, "delegated-test-secret", clock.Now, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	accountStore, err := accounts.NewStore(db, false, "delegated-test-secret", clock.Now, nil)
	if err != nil {
		t.Fatal(err)
	}
	k := kernel.New(kernel.Options{CompressionDisabled: true})
	deps := &Deps{
		Tokens:     tokenStore,
		Limiter:    oidc.NewProtocolRateLimiter(clock.Now),
		Groups:     groupStore,
		Strategies: strategyStore,
		ApiKeys:    apiKeyStore,
		AiAccounts: accountStore,
		DB:         db,
		Now:        clock.Now,
	}
	deps.Mount(k)
	server := httptest.NewServer(k.Handler())
	t.Cleanup(server.Close)
	return &env{
		t: t, db: db, server: server, clock: clock,
		tokens: tokenStore, groups: groupStore, strategies: strategyStore,
		apiKeys: apiKeyStore, accts: accountStore, deps: deps,
	}
}

// tokenContext resolves a seeded bearer token into its access context.
func (e *env) tokenContext(token string) *oidc.AccessTokenContext {
	e.t.Helper()
	context, err := e.tokens.FindAccessTokenContext(context.Background(), token)
	if err != nil {
		e.t.Fatal(err)
	}
	return context
}

// ---------------------------------------------------------------------------
// Seed helpers.
// ---------------------------------------------------------------------------

func (e *env) exec(query string, args ...any) {
	e.t.Helper()
	if _, err := e.db.Exec(query, args...); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) seedSystemAccount(id, username string) {
	e.t.Helper()
	now := isoMillis(e.clock.Now())
	e.exec(`INSERT INTO system_accounts (id, username, display_name, role, status, password_hash, created_at, updated_at)
		VALUES (?, ?, ?, 'user', 'active', 'test-hash', ?, ?)`, id, username, username+"-display", now, now)
}

func (e *env) seedProviders() {
	e.t.Helper()
	now := isoMillis(e.clock.Now())
	e.exec(`INSERT INTO providers (id, code, name, enabled, created_at, updated_at)
		VALUES ('prov_openai', 'openai', 'OpenAI', 1, ?, ?)`, now, now)
	e.exec(`INSERT INTO providers (id, code, name, enabled, created_at, updated_at)
		VALUES ('prov_disabled', 'disabled-provider', 'Disabled', 0, ?, ?)`, now, now)
}

// seedDelegatedToken inserts an active oauth client + grant + access token
// row family and returns the bearer token value.
func (e *env) seedDelegatedToken(systemAccountID string, scopes ...string) string {
	e.t.Helper()
	suffix := randomHex(e.t)
	clientID := "dclient_" + suffix
	grantID := "dgrant_" + suffix
	tokenID := "dtok_" + suffix
	accessToken := "dat_" + suffix
	now := isoMillis(e.clock.Now())
	expires := isoMillis(e.clock.Now().Add(time.Hour))
	scopesJSON, _ := json.Marshal(scopes)
	e.exec(`INSERT INTO oauth_clients (id, client_id, display_name, client_type, redirect_uris_json, allowed_scopes_json, status, created_at, updated_at)
		VALUES (?, ?, 'Delegated Test Client', 'public', '[]', ?, 'active', ?, ?)`,
		"dclientrow_"+suffix, clientID, string(scopesJSON), now, now)
	e.exec(`INSERT INTO oauth_grants (id, client_id, system_account_id, scopes_json, expires_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`, grantID, clientID, systemAccountID, string(scopesJSON), expires, now)
	e.exec(`INSERT INTO oauth_access_tokens (id, token_hash, client_id, grant_id, issued_at, expires_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		tokenID, apikeys.HashSecret(accessToken), clientID, grantID, now, expires, now)
	return accessToken
}

func (e *env) seedGroup(id, ownerID, name, providerCode, groupType string, enabled bool) {
	e.t.Helper()
	now := isoMillis(e.clock.Now())
	e.exec(`INSERT INTO groups (id, system_account_id, name, provider_code, description, enabled, is_default, group_type, created_at, updated_at)
		VALUES (?, ?, ?, ?, NULL, ?, 0, ?, ?, ?)`, id, ownerID, name, providerCode, boolToInt(enabled), groupType, now, now)
}

func (e *env) seedStrategy(id, ownerID, name, mode, status string) {
	e.t.Helper()
	now := isoMillis(e.clock.Now())
	e.exec(`INSERT INTO route_strategies (id, system_account_id, name, description, mode, status, is_default, config_json, created_at, updated_at)
		VALUES (?, ?, ?, NULL, ?, ?, 0, NULL, ?, ?)`, id, ownerID, name, mode, status, now, now)
}

func (e *env) seedStrategyBinding(id, strategyID, ownerID, groupID string, priority, weight int, status string) {
	e.t.Helper()
	now := isoMillis(e.clock.Now())
	e.exec(`INSERT INTO route_strategy_groups (id, route_strategy_id, system_account_id, group_id, priority, weight, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, strategyID, ownerID, groupID, priority, weight, status, now, now)
}

func (e *env) seedApiKey(id, ownerID, name, routeStrategyID, status string) {
	e.t.Helper()
	now := isoMillis(e.clock.Now())
	e.exec(`INSERT INTO api_keys (id, system_account_id, route_strategy_id, name, description, key_hash, key_prefix, key_suffix, key_secret_encrypted, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, NULL, ?, 'sk-abcd', '1234', 'enc', ?, ?, ?)`,
		id, ownerID, routeStrategyID, name, apikeys.HashSecret(id+name), status, now, now)
}

func (e *env) seedAiAccount(id, ownerID, name, status string, inheritedFrom string) {
	e.t.Helper()
	now := isoMillis(e.clock.Now())
	e.exec(`INSERT INTO accounts (id, system_account_id, provider_code, provider_protocol_profile_id, protocol_code, protocol_version, name, type, status, credentials_encrypted, authorization_instance_source_account_id, created_at, updated_at)
		VALUES (?, ?, 'openai', 'ppp_1', 'openai', 'v1', ?, 'api_key', ?, 'enc', ?, ?, ?)`,
		id, ownerID, name, status, nullString(inheritedFrom), now, now)
}

func nullString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

// ---------------------------------------------------------------------------
// HTTP helpers.
// ---------------------------------------------------------------------------

type response struct {
	status int
	header http.Header
	raw    []byte
	body   map[string]any
}

func (e *env) do(method, path, body, bearer string) response {
	e.t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request, err := http.NewRequest(method, e.server.URL+path, reader)
	if err != nil {
		e.t.Fatal(err)
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		request.Header.Set("Authorization", "Bearer "+bearer)
	}
	httpClient := &http.Client{}
	resp, err := httpClient.Do(request)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		e.t.Fatal(err)
	}
	payload := map[string]any{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &payload); err != nil {
			e.t.Fatalf("decode %s %s response %q: %v", method, path, raw, err)
		}
	}
	return response{status: resp.StatusCode, header: resp.Header, raw: raw, body: payload}
}

func (r response) data(t *testing.T) map[string]any {
	t.Helper()
	data, ok := r.body["data"].(map[string]any)
	if !ok {
		t.Fatalf("missing data object in %s", r.raw)
	}
	return data
}

func (r response) dataArray(t *testing.T, key string) []any {
	t.Helper()
	data, ok := r.body["data"].(map[string]any)
	if !ok {
		t.Fatalf("missing data object in %s", r.raw)
	}
	items, ok := data[key].([]any)
	if !ok {
		t.Fatalf("missing %q array in %s", key, r.raw)
	}
	return items
}

// requireMessage asserts the exact {"message": ...} error envelope plus the
// shared Content-Type contract.
func (r response) requireMessage(t *testing.T, wantStatus int, wantMessage string) {
	t.Helper()
	if r.status != wantStatus {
		t.Fatalf("status = %d, want %d (body %s)", r.status, wantStatus, r.raw)
	}
	if want := fmt.Sprintf(`{"message":%q}`, wantMessage); string(r.raw) != want {
		t.Fatalf("body = %s, want %s", r.raw, want)
	}
	if got := r.header.Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Fatalf("Content-Type = %q", got)
	}
}

// requireOAuthError asserts the byte-exact OAuth error envelope (RFC 6750)
// used by 401/403/429 delegated responses.
func (r response) requireOAuthError(t *testing.T, wantStatus int, wantBody, wantWWWAuthenticate string) {
	t.Helper()
	if r.status != wantStatus {
		t.Fatalf("status = %d, want %d (body %s)", r.status, wantStatus, r.raw)
	}
	if string(r.raw) != wantBody {
		t.Fatalf("body = %s, want %s", r.raw, wantBody)
	}
	if got := r.header.Get("WWW-Authenticate"); got != wantWWWAuthenticate {
		t.Fatalf("WWW-Authenticate = %q, want %q", got, wantWWWAuthenticate)
	}
	if got := r.header.Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Fatalf("Content-Type = %q", got)
	}
}

// ---------------------------------------------------------------------------
// Delegated access gate (requireDelegatedAccess + requireScope + limiter).
// ---------------------------------------------------------------------------

const (
	invalidTokenBody      = `{"error":"invalid_token","error_description":"访问令牌无效或已过期"}`
	insufficientScopeBody = `{"error":"insufficient_scope","error_description":"访问令牌缺少所需权限"}`
	rateLimitedBody       = `{"error":"rate_limited","error_description":"OAuth 请求过于频繁，请稍后重试"}`
	notFoundBody          = `{"message":"资源不存在"}`
	serverErrorBody       = `{"message":"服务器内部错误"}`
	groupMissingBody      = `{"message":"分组不存在"}`
	strategyMissingBody   = `{"message":"策略路由不存在"}`
	accountMissingBody    = `{"message":"AI 账户不存在"}`
	profileMissingBody    = `{"message":"用户不存在"}`
)

func TestRequireDelegatedAccessRejectsMissingBearer(t *testing.T) {
	env := newEnv(t)
	for name, authorization := range map[string]string{
		"no_header":        "",
		"basic_scheme":     "Basic Zm9vOmJhcg==",
		"bare_scheme":      "Bearer",
		"two_tokens":       "Bearer abc def",
		"unknown_token":    "dat_does_not_exist",
		"case_insensitive": "bearer dat_does_not_exist",
	} {
		t.Run(name, func(t *testing.T) {
			request, err := http.NewRequest(http.MethodGet, env.server.URL+Prefix+"/request-limits", nil)
			if err != nil {
				t.Fatal(err)
			}
			if authorization != "" {
				request.Header.Set("Authorization", authorization)
			}
			resp, err := http.DefaultClient.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			raw, _ := io.ReadAll(resp.Body)
			r := response{status: resp.StatusCode, header: resp.Header, raw: raw}
			r.requireOAuthError(t, http.StatusUnauthorized, invalidTokenBody, `Bearer error="invalid_token"`)
		})
	}
}

func TestRequireScopeRejectsMissingScope(t *testing.T) {
	env := newEnv(t)
	env.seedSystemAccount("acc-1", "alice")
	token := env.seedDelegatedToken("acc-1", "juhe:groups.read")
	r := env.do(http.MethodPost, Prefix+"/groups", `{"name":"g","providerCode":"openai"}`, token)
	r.requireOAuthError(t, http.StatusForbidden, insufficientScopeBody,
		`Bearer error="insufficient_scope", scope="juhe:groups.write"`)
	// The scope check must be per-route: the granted scope still works.
	ok := env.do(http.MethodGet, Prefix+"/groups", "", token)
	if ok.status != http.StatusOK {
		t.Fatalf("groups.read request status = %d (body %s)", ok.status, ok.raw)
	}
}

func TestRequireDelegatedAccessMapsStoreErrorTo500(t *testing.T) {
	// A store whose database lacks the oauth tables surfaces the lookup
	// failure as the Node 500 copy instead of the 401 envelope.
	clock := newFakeClock()
	db, err := sql.Open("sqlite", "file:delegated-nooauth-"+strings.ReplaceAll(t.Name(), "/", "-")+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	env := assembleEnv(t, db, clock)
	r := env.do(http.MethodGet, Prefix+"/groups", "", "dat_any_token")
	r.requireMessage(t, http.StatusInternalServerError, "服务器内部错误")
}

func TestUnknownDelegatedRouteFallsBackToAPI404(t *testing.T) {
	env := newEnv(t)
	env.seedSystemAccount("acc-1", "alice")
	token := env.seedDelegatedToken("acc-1", "juhe:profile.read")
	r := env.do(http.MethodGet, Prefix+"/nope", "", token)
	r.requireMessage(t, http.StatusNotFound, "资源不存在")
}

func TestDelegatedMethodMismatchFallsBackToAPI404(t *testing.T) {
	env := newEnv(t)
	env.seedSystemAccount("acc-1", "alice")
	token := env.seedDelegatedToken("acc-1", "juhe:request_limits.read", "juhe:profile.read")
	// request-limits is GET-only; profile is GET+PATCH.
	for _, mismatch := range []struct{ method, path string }{
		{http.MethodPatch, Prefix + "/request-limits"},
		{http.MethodDelete, Prefix + "/profile"},
		{http.MethodPut, Prefix + "/groups"},
	} {
		r := env.do(mismatch.method, mismatch.path, "", token)
		r.requireMessage(t, http.StatusNotFound, "资源不存在")
	}
}

func TestDelegatedRateLimitWindow(t *testing.T) {
	env := newEnv(t)
	env.seedSystemAccount("acc-1", "alice")
	token := env.seedDelegatedToken("acc-1", "juhe:request_limits.read")
	// The delegated limiter class allows 300 requests per 60s fixed window.
	for i := 0; i < 300; i++ {
		r := env.do(http.MethodGet, Prefix+"/request-limits", "", token)
		if r.status != http.StatusOK {
			t.Fatalf("request %d status = %d (body %s), want 200", i+1, r.status, r.raw)
		}
	}
	r := env.do(http.MethodGet, Prefix+"/request-limits", "", token)
	if r.status != http.StatusTooManyRequests {
		t.Fatalf("request 301 status = %d (body %s), want 429", r.status, r.raw)
	}
	r.requireOAuthError(t, http.StatusTooManyRequests, rateLimitedBody, "")
	if retryAfter := r.header.Get("Retry-After"); retryAfter != "60" {
		t.Fatalf("Retry-After = %q, want 60", retryAfter)
	}
	// Advancing the injected clock past the window end restores access.
	env.clock.Advance(61 * time.Second)
	r = env.do(http.MethodGet, Prefix+"/request-limits", "", token)
	if r.status != http.StatusOK {
		t.Fatalf("post-window request status = %d (body %s), want 200", r.status, r.raw)
	}
}

// ---------------------------------------------------------------------------
// Profile (GET/PATCH /profile): the delegated-local store reads and updates
// system_accounts like Node findSystemAccountByIdAsync /
// updateSystemAccountAsync; the PATCH schema mirrors zod
// (displayName trim→min(1)), whose English issue copy renders as the localized
// 400 请求参数无效 through the kernel envelope, while interior whitespace hits
// the store normalizeRequiredText 409 and padded input trims to success.
// ---------------------------------------------------------------------------

func TestGetProfileReturnsUsernameAndDisplayName(t *testing.T) {
	env := newEnv(t)
	env.seedSystemAccount("acc-1", "alice")
	token := env.seedDelegatedToken("acc-1", "juhe:profile.read")
	r := env.do(http.MethodGet, Prefix+"/profile", "", token)
	if r.status != http.StatusOK {
		t.Fatalf("status = %d (body %s)", r.status, r.raw)
	}
	data := r.data(t)
	if data["username"] != "alice" || data["displayName"] != "alice-display" {
		t.Fatalf("profile dto = %v", data)
	}
	if len(data) != 2 {
		t.Fatalf("profile dto keys = %v", data)
	}
}

// Note: the 404 用户不存在 branches are defensive only — both Node
// findAccessTokenContext and the Go oidc store resolve tokens through an
// INNER JOIN on an active system account, so a missing caller profile is
// unconstructible behind a valid bearer token (401 renders first).

func TestPatchProfileValidation(t *testing.T) {
	env := newEnv(t)
	env.seedSystemAccount("acc-1", "alice")
	token := env.seedDelegatedToken("acc-1", "juhe:profile.write")

	cases := []struct {
		name       string
		body       string
		wantStatus int
		wantBody   string
	}{
		// zod issue copy contains no CJK, so the kernel envelope localizes it
		// to the 400 status default — identical to the Node pipeline.
		{"empty_object", `{}`, 400, `{"message":"请求参数无效"}`},
		{"missing_displayName", `{"other":"x"}`, 400, `{"message":"请求参数无效"}`},
		{"non_string_displayName", `{"displayName":42}`, 400, `{"message":"请求参数无效"}`},
		{"extra_field_rejected", `{"displayName":"张三","extra":1}`, 400, `{"message":"请求参数无效"}`},
		{"blank_displayName_400", `{"displayName":"   "}`, 400, `{"message":"请求参数无效"}`},
		{"malformed_json", `not-json`, 400, `{"message":"请求体无效"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := env.do(http.MethodPatch, Prefix+"/profile", tc.body, token)
			if r.status != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body %s)", r.status, tc.wantStatus, r.raw)
			}
			if string(r.raw) != tc.wantBody {
				t.Fatalf("body = %s, want %s", r.raw, tc.wantBody)
			}
		})
	}
}

func TestPatchProfileDisplayNameTrimAndWhitespace(t *testing.T) {
	// Interior whitespace → 409 用户名称不能包含空格 (normalizeRequiredText);
	// padded input passes the zod trim transform and succeeds (200) with the
	// trimmed value persisted.
	env := newEnv(t)
	env.seedSystemAccount("acc-1", "alice")
	token := env.seedDelegatedToken("acc-1", "juhe:profile.write")

	r := env.do(http.MethodPatch, Prefix+"/profile", `{"displayName":"张 三"}`, token)
	r.requireMessage(t, http.StatusConflict, "用户名称不能包含空格")

	r = env.do(http.MethodPatch, Prefix+"/profile", `{"displayName":"  张三  "}`, token)
	if r.status != http.StatusOK {
		t.Fatalf("padded status = %d (body %s)", r.status, r.raw)
	}
	data := r.data(t)
	if data["displayName"] != "张三" || data["username"] != "alice" {
		t.Fatalf("trimmed dto = %v", data)
	}
	var stored string
	if err := env.db.QueryRow(`SELECT display_name FROM system_accounts WHERE id = 'acc-1'`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != "张三" {
		t.Fatalf("stored display_name = %q, want 张三", stored)
	}
}

func TestPatchProfileDisplayNameConflictAndMissing(t *testing.T) {
	env := newEnv(t)
	env.seedSystemAccount("acc-1", "alice")
	env.seedSystemAccount("acc-2", "bob")
	token := env.seedDelegatedToken("acc-2", "juhe:profile.write")

	// Case-insensitive display-name uniqueness against a different account.
	r := env.do(http.MethodPatch, Prefix+"/profile", `{"displayName":"ALICE-DISPLAY"}`, token)
	r.requireMessage(t, http.StatusConflict, "用户名称已存在")
}

// ---------------------------------------------------------------------------
// Request limits (GET /request-limits): the Node requestLimitSnapshot over
// settings + per-user overrides + runtime-state usage totals.
// ---------------------------------------------------------------------------

type fakeSettings struct {
	values map[string]string
}

func (f *fakeSettings) SettingValue(key string) (string, error) {
	return f.values[key], nil
}

type fakeUsage struct {
	values map[string]string
	err    error
}

func (f *fakeUsage) RequestLimitTotal(_ context.Context, key string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return f.values[key], nil
}

// clockStart ms is 2026-01-10T08:30:00Z; 2026-01-10 is a Saturday, so the
// perWeek bucket key is the Monday 2026-01-05.
func requestLimitWindows(t *testing.T, r response) map[string]map[string]any {
	t.Helper()
	data := r.data(t)
	rawWindows, ok := data["windows"].(map[string]any)
	if !ok {
		t.Fatalf("windows missing in %s", r.raw)
	}
	windows := map[string]map[string]any{}
	for name, raw := range rawWindows {
		window, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("window %q malformed: %v", name, raw)
		}
		windows[name] = window
	}
	if len(windows) != 4 {
		t.Fatalf("windows = %v", rawWindows)
	}
	return windows
}

func TestGetRequestLimitsAllUnlimitedNotTracked(t *testing.T) {
	// No settings rows → the compatible defaults (all 0) make every window
	// unlimited; nothing is usage-tracked so no runtime state is read.
	env := newEnv(t)
	env.seedSystemAccount("acc-1", "alice")
	token := env.seedDelegatedToken("acc-1", "juhe:request_limits.read")
	r := env.do(http.MethodGet, Prefix+"/request-limits", "", token)
	if r.status != http.StatusOK {
		t.Fatalf("status = %d (body %s)", r.status, r.raw)
	}
	data := r.data(t)
	if data["usageStatus"] != "not_tracked" {
		t.Fatalf("usageStatus = %v", data["usageStatus"])
	}
	if data["timezone"] != "UTC" || data["overrideActive"] != false {
		t.Fatalf("timezone/overrideActive = %v/%v", data["timezone"], data["overrideActive"])
	}
	if _, has := data["overrideExpiresOn"]; has {
		t.Fatalf("overrideExpiresOn must be absent: %s", r.raw)
	}
	if _, has := data["asOf"]; !has {
		t.Fatalf("asOf missing: %s", r.raw)
	}
	windows := requestLimitWindows(t, r)
	for name, window := range windows {
		if !numEqual(window["limit"], 0) || window["limitMode"] != "unlimited" ||
			window["usageTracked"] != false || window["used"] != nil || window["remaining"] != nil ||
			window["source"] != "global" {
			t.Fatalf("window %q = %v", name, window)
		}
	}
	// Bucket-anchored resetsAt (fixed clock): the minute window rolls at
	// 08:31:00Z.
	if windows["perMinute"]["resetsAt"] != "2026-01-10T08:31:00.000Z" {
		t.Fatalf("perMinute resetsAt = %v", windows["perMinute"]["resetsAt"])
	}
	if windows["perWeek"]["resetsAt"] != "2026-01-17T08:30:00.000Z" {
		t.Fatalf("perWeek resetsAt = %v", windows["perWeek"]["resetsAt"])
	}
}

func TestGetRequestLimitsGlobalLimitsWithRedisTotals(t *testing.T) {
	env := newEnv(t)
	env.seedSystemAccount("acc-1", "alice")
	token := env.seedDelegatedToken("acc-1", "juhe:request_limits.read")
	env.deps.Settings = &fakeSettings{values: map[string]string{
		"gatewayUserRequestLimitPerMinute": `10`,
		"gatewayUserRequestLimitPerDay":    `100`,
		"gatewayUserRequestLimitPerWeek":   `0`,
		"gatewayUserRequestLimitPerMonth":  `1000`,
		"usageStatsTimezone":               `"UTC"`,
	}}
	env.deps.RedisNamespace = "juhe:dev"
	env.deps.Usage = &fakeUsage{values: map[string]string{
		// Keys mirror the Node redisKey format for the fixed clock.
		"juhe:dev:gateway:user-request-limit:perMinute:29467230:acc-1": "3",
		"juhe:dev:gateway:user-request-limit:perDay:2026-01-10:acc-1":  "0",
		"juhe:dev:gateway:user-request-limit:perMonth:2026-01:acc-1":   "-4", // invalid → nonNegativeInteger 0
	}}

	r := env.do(http.MethodGet, Prefix+"/request-limits", "", token)
	if r.status != http.StatusOK {
		t.Fatalf("status = %d (body %s)", r.status, r.raw)
	}
	data := r.data(t)
	if data["usageStatus"] != "estimated" {
		t.Fatalf("usageStatus = %v (body %s)", data["usageStatus"], r.raw)
	}
	windows := requestLimitWindows(t, r)
	perMinute := windows["perMinute"]
	if !numEqual(perMinute["limit"], 10) || perMinute["limitMode"] != "limited" ||
		perMinute["usageTracked"] != true || !numEqual(perMinute["used"], 3) || !numEqual(perMinute["remaining"], 7) {
		t.Fatalf("perMinute = %v", perMinute)
	}
	perDay := windows["perDay"]
	if !numEqual(perDay["used"], 0) || !numEqual(perDay["remaining"], 100) {
		t.Fatalf("perDay = %v", perDay)
	}
	perMonth := windows["perMonth"]
	if !numEqual(perMonth["used"], 0) || !numEqual(perMonth["remaining"], 1000) {
		t.Fatalf("perMonth invalid-total fallback = %v", perMonth)
	}
	perWeek := windows["perWeek"]
	if perWeek["limitMode"] != "unlimited" || perWeek["used"] != nil {
		t.Fatalf("perWeek = %v", perWeek)
	}
}

func TestGetRequestLimitsUserOverrideAndExpiry(t *testing.T) {
	env := newEnv(t)
	env.seedSystemAccount("acc-1", "alice")
	env.exec(`UPDATE system_accounts SET request_limits_json = ? WHERE id = 'acc-1'`,
		`{"perDay":5,"expiresOn":"2026-01-10"}`)
	token := env.seedDelegatedToken("acc-1", "juhe:request_limits.read")
	env.deps.Settings = &fakeSettings{values: map[string]string{
		"gatewayUserRequestLimitPerDay":  `100`,
		"gatewayUserRequestLimitPerWeek": `0`,
		"usageStatsTimezone":             `"UTC"`,
	}}

	r := env.do(http.MethodGet, Prefix+"/request-limits", "", token)
	data := r.data(t)
	// The override expires today (local date == expiresOn) → active, source
	// user, and overrideExpiresOn is echoed even for inactive windows.
	if data["overrideActive"] != true || data["overrideExpiresOn"] != "2026-01-10" {
		t.Fatalf("override fields = %v/%v", data["overrideActive"], data["overrideExpiresOn"])
	}
	windows := requestLimitWindows(t, r)
	if !numEqual(windows["perDay"]["limit"], 5) || windows["perDay"]["source"] != "user" {
		t.Fatalf("perDay = %v", windows["perDay"])
	}
	if !numEqual(windows["perMinute"]["limit"], 0) || windows["perMinute"]["source"] != "global" {
		t.Fatalf("perMinute = %v", windows["perMinute"])
	}

	// An expired override falls back to the global limits.
	env.exec(`UPDATE system_accounts SET request_limits_json = ? WHERE id = 'acc-1'`,
		`{"perDay":5,"expiresOn":"2026-01-01"}`)
	r = env.do(http.MethodGet, Prefix+"/request-limits", "", token)
	data = r.data(t)
	if data["overrideActive"] != false || data["overrideExpiresOn"] != "2026-01-01" {
		t.Fatalf("expired override fields = %v/%v", data["overrideActive"], data["overrideExpiresOn"])
	}
	windows = requestLimitWindows(t, r)
	if !numEqual(windows["perDay"]["limit"], 100) || windows["perDay"]["source"] != "global" {
		t.Fatalf("expired perDay = %v", windows["perDay"])
	}
}

func TestGetRequestLimitsUsageUnavailableDegrades(t *testing.T) {
	// Runtime state unreachable → Node degrade: usageStatus unavailable and
	// null used/remaining even for limited windows.
	env := newEnv(t)
	env.seedSystemAccount("acc-1", "alice")
	token := env.seedDelegatedToken("acc-1", "juhe:request_limits.read")
	env.deps.Settings = &fakeSettings{values: map[string]string{
		"gatewayUserRequestLimitPerMinute": `10`,
		"gatewayUserRequestLimitPerWeek":   `0`,
		"usageStatsTimezone":               `"UTC"`,
	}}
	env.deps.RedisNamespace = "juhe:dev"
	env.deps.Usage = &fakeUsage{err: contextDeadlineError}

	r := env.do(http.MethodGet, Prefix+"/request-limits", "", token)
	if r.status != http.StatusOK {
		t.Fatalf("status = %d (body %s)", r.status, r.raw)
	}
	data := r.data(t)
	if data["usageStatus"] != "unavailable" {
		t.Fatalf("usageStatus = %v", data["usageStatus"])
	}
	windows := requestLimitWindows(t, r)
	if !numEqual(windows["perMinute"]["limit"], 10) || windows["perMinute"]["used"] != nil || windows["perMinute"]["remaining"] != nil {
		t.Fatalf("perMinute = %v", windows["perMinute"])
	}
}
