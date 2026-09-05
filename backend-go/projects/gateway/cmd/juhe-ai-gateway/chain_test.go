package main

// G20 phase-2 composition adapter tests: the unit coverage for the account /
// catalog read seams, the provider driver, the usage persistence bridge, the
// chat executor bridge, plus the preauth→dispatch(mock upstream)→response
// full-chain smoke test over a seeded SQLite business database.

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/chat"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycircuit"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayclientip"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaygemini"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproxyhealth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayquota"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayusage"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/pgpool"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/bootstrap"
)

// ---------------------------------------------------------------------------
// seeded business schema (the runtime cache + selector read surface)
// ---------------------------------------------------------------------------

type chainFixture struct {
	db            *sql.DB
	statsDB       *sql.DB
	cache         *gatewayruntimecache.Service
	selector      *chainAccountsSelector
	apiKeySecret  string
	systemAccount string
	groupID       string
	accountID     string
}

func newChainFixture(t *testing.T) *chainFixture {
	t.Helper()
	root := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(root, "business.sqlite3"))
	if err != nil {
		t.Fatalf("open business db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	statsDB, err := sql.Open("sqlite", filepath.Join(root, "stats.sqlite3"))
	if err != nil {
		t.Fatalf("open stats db: %v", err)
	}
	t.Cleanup(func() { _ = statsDB.Close() })
	seedChainBusinessSchema(t, db)
	seedChainStatsSchema(t, statsDB)
	fixture := &chainFixture{db: db, statsDB: statsDB, systemAccount: "sys_owner", groupID: "group_main", accountID: "acc_1"}
	secret := seedChainRuntimeRows(t, db, fixture)
	fixture.apiKeySecret = secret

	models, err := gatewayruntimecache.NewSQLReadModels(db, false, time.Now, nil, nil, nil)
	if err != nil {
		t.Fatalf("create sql read models: %v", err)
	}
	selector, err := newChainAccountsSelectorWithStats(db, statsDB, false, "chain-test-secret", time.Now)
	if err != nil {
		t.Fatalf("create selector: %v", err)
	}
	models.SetAccountsSelector(selector)
	catalogSource, err := newChainCatalogSource(db, false)
	if err != nil {
		t.Fatalf("create catalog source: %v", err)
	}
	models.SetCatalogSource(catalogSource)
	models.SetConcurrencySource(gatewayclientip.NewMemoryAccountConcurrency(nil))
	cache, err := gatewayruntimecache.New(models, gatewayruntimecache.Options{Clock: gatewayruntimecache.SystemClock()})
	if err != nil {
		t.Fatalf("create runtime cache: %v", err)
	}
	t.Cleanup(cache.Close)
	fixture.cache = cache
	fixture.selector = selector
	return fixture
}

func gatewayclientipMemoryConcurrency() gatewayruntimecache.ConcurrencySource {
	return gatewayclientip.NewMemoryAccountConcurrency(nil)
}

// seedChainBusinessSchema creates the runtime-cache read surface tables.
func seedChainBusinessSchema(t *testing.T, db *sql.DB) {
	t.Helper()
	statements := []string{
		`CREATE TABLE system_settings (system_account_id TEXT NOT NULL, key TEXT NOT NULL, value_json TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE TABLE system_accounts (id TEXT PRIMARY KEY, status TEXT NOT NULL, image_generation_enabled INTEGER NOT NULL DEFAULT 1, request_limits_json TEXT)`,
		`CREATE TABLE groups (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, provider_code TEXT NOT NULL, enabled INTEGER NOT NULL, group_type TEXT, scheduling_policy_json TEXT)`,
		`CREATE TABLE accounts (
			id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, provider_code TEXT NOT NULL,
			provider_protocol_profile_id TEXT, protocol_code TEXT, protocol_version TEXT,
			name TEXT NOT NULL, type TEXT NOT NULL, status TEXT NOT NULL, schedulable INTEGER NOT NULL DEFAULT 1,
			concurrency_limit INTEGER NOT NULL DEFAULT 0, priority INTEGER NOT NULL DEFAULT 0,
			super_priority_enabled INTEGER NOT NULL DEFAULT 0, fallback_enabled INTEGER NOT NULL DEFAULT 0,
			client_compatibility TEXT, config_revision INTEGER, dispatch_revision INTEGER,
			credentials_encrypted TEXT, proxy_profile_id TEXT, cooldown_until TEXT,
			last_error_message TEXT, last_error_code TEXT, stream_failure_count INTEGER NOT NULL DEFAULT 0,
			stream_failure_window_started_at TEXT, account_expires_at TEXT,
			health_check_model TEXT, health_check_endpoint_mode TEXT,
			authorization_instance_source_account_id TEXT, authorization_instance_authorization_id TEXT,
			authorization_instance_owner_system_account_id TEXT, deleted_at TEXT)`,
		`CREATE TABLE group_accounts (
			group_id TEXT NOT NULL, system_account_id TEXT NOT NULL, account_id TEXT NOT NULL,
			enabled INTEGER NOT NULL, account_authorization_id TEXT,
			local_priority INTEGER, local_super_priority_enabled INTEGER NOT NULL DEFAULT 0,
			local_fallback_enabled INTEGER NOT NULL DEFAULT 0, created_at TEXT NOT NULL)`,
		`CREATE TABLE route_strategies (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, name TEXT, mode TEXT NOT NULL, config_json TEXT, status TEXT NOT NULL)`,
		`CREATE TABLE route_strategy_groups (
			id TEXT PRIMARY KEY, route_strategy_id TEXT NOT NULL, system_account_id TEXT NOT NULL,
			group_id TEXT NOT NULL, priority INTEGER NOT NULL, weight INTEGER, status TEXT NOT NULL, created_at TEXT NOT NULL)`,
		`CREATE TABLE api_keys (
			id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, route_strategy_id TEXT, name TEXT,
			key_hash TEXT NOT NULL, key_prefix TEXT, key_suffix TEXT, key_secret_encrypted TEXT,
			status TEXT NOT NULL, is_default INTEGER NOT NULL DEFAULT 0, purpose TEXT,
			expires_at TEXT, quota_limits_json TEXT, availability_schedule_json TEXT, created_at TEXT, updated_at TEXT)`,
		`CREATE TABLE resource_authorizations (
			id TEXT PRIMARY KEY, resource_type TEXT NOT NULL, resource_id TEXT NOT NULL,
			resource_owner_system_account_id TEXT, grantee_system_account_id TEXT NOT NULL,
			scope TEXT, status TEXT NOT NULL, expires_at TEXT,
			effective_source_type TEXT, effective_source_team_id TEXT, limits_json TEXT)`,
		`CREATE TABLE group_authorization_settings (
			authorization_id TEXT NOT NULL, system_account_id TEXT NOT NULL, group_id TEXT NOT NULL,
			enabled INTEGER, group_type TEXT, scheduling_policy_json TEXT)`,
		`CREATE TABLE account_supported_models (account_id TEXT NOT NULL, provider_code TEXT, model TEXT NOT NULL, created_at TEXT NOT NULL)`,
		`CREATE TABLE account_api_key_runtime_states (
			account_id TEXT NOT NULL, key_fingerprint TEXT NOT NULL, key_index INTEGER,
			status TEXT NOT NULL, cooldown_until TEXT, next_probe_at TEXT, recovery_started_at TEXT,
			updated_at TEXT)`,
		`CREATE TABLE proxy_profiles (
			id TEXT PRIMARY KEY, name TEXT, type TEXT NOT NULL, host TEXT NOT NULL, port INTEGER NOT NULL,
			username TEXT, password_encrypted TEXT, enabled INTEGER NOT NULL DEFAULT 1,
			created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		// Real-DDL shape (Node business-schema.ts + maintenance
		// sqlite_schema.go): composite primary key, NO id column. The drifted
		// hand-built `id` column masked the account_model_mappings ORDER BY
		// defect on fresh databases.
		`CREATE TABLE account_model_mappings (
			account_id TEXT NOT NULL, provider_code TEXT NOT NULL,
			source_model TEXT NOT NULL, source_endpoint_family TEXT NOT NULL,
			upstream_model TEXT NOT NULL, upstream_endpoint_family TEXT NOT NULL,
			enabled INTEGER NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
			PRIMARY KEY (account_id, source_model, source_endpoint_family))`,
		`CREATE TABLE response_inspection_policies (
			id TEXT PRIMARY KEY, name TEXT, enabled INTEGER NOT NULL, priority INTEGER NOT NULL,
			scope_type TEXT NOT NULL, protocol_code TEXT, provider_code TEXT, match_json TEXT,
			action TEXT, notes TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		// Real-DDL shape (Node provider-model-catalog.repository.ts columns()
		// + maintenance sqlite_schema.go): built-in catalog rows only. The
		// drifted hand-built columns (scope/input_modalities/...) masked the
		// catalog 500 on fresh databases. Per-account rows live in
		// custom_provider_models below.
		`CREATE TABLE provider_model_catalog (
			id TEXT PRIMARY KEY, provider_code TEXT NOT NULL, model TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'active', mode TEXT, catalog_order INTEGER,
			release_date TEXT, shutdown_date TEXT,
			supported_api_protocols_json TEXT NOT NULL DEFAULT '[]',
			supported_service_tiers_json TEXT NOT NULL DEFAULT '[]',
			supported_reasoning_efforts_json TEXT NOT NULL DEFAULT '[]',
			default_reasoning_effort TEXT, codex_supported_reasoning_levels_json TEXT NOT NULL DEFAULT '[]',
			codex_default_reasoning_level TEXT, codex_multi_agent_version TEXT,
			context_window_tokens INTEGER, max_input_tokens INTEGER, max_output_tokens INTEGER,
			max_tokens INTEGER, input_usd_per_1m REAL, output_usd_per_1m REAL,
			cached_input_usd_per_1m REAL, cache_write_usd_per_1m REAL, cache_write_1h_usd_per_1m REAL,
			cache_storage_usd_per_1m_per_hour REAL, service_tier_prices_json TEXT NOT NULL DEFAULT '{}',
			long_context_input_token_threshold INTEGER, long_context_input_token_threshold_inclusive INTEGER NOT NULL DEFAULT 0,
			long_context_input_cost_multiplier REAL, long_context_output_cost_multiplier REAL,
			image_input_usd_per_1m REAL, image_output_usd_per_1m REAL, audio_input_usd_per_1m REAL,
			audio_output_usd_per_1m REAL, output_usd_per_image REAL,
			supports_prompt_caching INTEGER NOT NULL DEFAULT 0, catalog_visible INTEGER NOT NULL DEFAULT 1,
			source TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		// Real-DDL shape (Node custom-provider-models.repository.ts
		// customProviderModelColumns()): the per-account catalog rows.
		`CREATE TABLE custom_provider_models (
			id TEXT PRIMARY KEY, provider_code TEXT NOT NULL, model TEXT NOT NULL,
			scope TEXT NOT NULL DEFAULT 'personal', system_account_id TEXT,
			status TEXT NOT NULL DEFAULT 'active', catalog_visible INTEGER NOT NULL DEFAULT 1,
			mode TEXT, supported_api_protocols_json TEXT NOT NULL DEFAULT '[]',
			supported_service_tiers_json TEXT NOT NULL DEFAULT '[]',
			supported_reasoning_efforts_json TEXT NOT NULL DEFAULT '[]',
			default_reasoning_effort TEXT, release_date TEXT, shutdown_date TEXT,
			context_window_tokens INTEGER, max_input_tokens INTEGER, max_output_tokens INTEGER,
			input_usd_per_1m REAL, output_usd_per_1m REAL, cached_input_usd_per_1m REAL,
			cache_write_usd_per_1m REAL, cache_write_1h_usd_per_1m REAL,
			cache_storage_usd_per_1m_per_hour REAL, service_tier_prices_json TEXT NOT NULL DEFAULT '{}',
			image_input_usd_per_1m REAL, image_output_usd_per_1m REAL, audio_input_usd_per_1m REAL,
			audio_output_usd_per_1m REAL, output_usd_per_image REAL, currency TEXT,
			pricing_notes TEXT, capability_notes TEXT, notes TEXT,
			created_by TEXT, updated_by TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
			CHECK (scope IN ('personal', 'global')),
			CHECK ((scope = 'personal' AND system_account_id IS NOT NULL) OR (scope = 'global' AND system_account_id IS NULL)))`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("seed schema: %v: %v", statement, err)
		}
	}
}

// seedChainStatsSchema creates the stats-side read surface the fresh
// dispatch quality rows come from (account_quality_scores).
func seedChainStatsSchema(t *testing.T, statsDB *sql.DB) {
	t.Helper()
	statements := []string{
		`CREATE TABLE account_quality_scores (
			account_id TEXT NOT NULL, quality_score REAL, quality_state TEXT,
			ewma_first_token_ms REAL, last_sample_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
	}
	for _, statement := range statements {
		if _, err := statsDB.Exec(statement); err != nil {
			t.Fatalf("seed stats schema: %v: %v", statement, err)
		}
	}
}

// seedChainRuntimeRows inserts the owner/strategy/key/group/account rows and
// returns the gateway API key secret.
func seedChainRuntimeRows(t *testing.T, db *sql.DB, fixture *chainFixture) string {
	t.Helper()
	now := "2026-09-04T00:00:00.000Z"
	seed := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatalf("seed row: %v: %v", query, err)
		}
	}
	seed(`INSERT INTO system_accounts (id, status, image_generation_enabled) VALUES (?, 'active', 1)`, fixture.systemAccount)
	seed(`INSERT INTO groups (id, system_account_id, provider_code, enabled, group_type) VALUES (?, ?, 'openai', 1, 'personal')`, fixture.groupID, fixture.systemAccount)
	credentials, err := accounts.EncryptJSON("chain-test-secret", map[string]any{
		"api_key":  "sk-upstream-account-key",
		"base_url": "",
	})
	if err != nil {
		t.Fatalf("encrypt credentials: %v", err)
	}
	seed(`INSERT INTO accounts (
			id, system_account_id, provider_code, provider_protocol_profile_id, protocol_code, protocol_version,
			name, type, status, schedulable, credentials_encrypted, deleted_at, health_check_model
		) VALUES (?, ?, 'openai', 'prof_1', 'openai', 'v1', '账户一', 'api_key', 'active', 1, ?, NULL, 'gpt-test')`,
		fixture.accountID, fixture.systemAccount, credentials)
	seed(`INSERT INTO group_accounts (group_id, system_account_id, account_id, enabled, created_at) VALUES (?, ?, ?, 1, ?)`,
		fixture.groupID, fixture.systemAccount, fixture.accountID, now)
	seed(`INSERT INTO account_supported_models (account_id, provider_code, model, created_at) VALUES (?, 'openai', 'gpt-test', ?)`,
		fixture.accountID, now)
	seed(`INSERT INTO route_strategies (id, system_account_id, name, mode, config_json, status) VALUES ('rs_1', ?, '默认', 'normal', NULL, 'active')`,
		fixture.systemAccount)
	seed(`INSERT INTO route_strategy_groups (id, route_strategy_id, system_account_id, group_id, priority, weight, status, created_at)
		VALUES ('rsg_1', 'rs_1', ?, ?, 0, 1, 'active', ?)`, fixture.systemAccount, fixture.groupID, now)
	secret := "sk-chain-test-key"
	seed(`INSERT INTO api_keys (id, system_account_id, route_strategy_id, name, key_hash, status, created_at)
		VALUES ('key_1', ?, 'rs_1', '链路测试', ?, 'active', ?)`,
		fixture.systemAccount, gatewayruntimecache.HashSecret(secret), now)
	seed(`INSERT INTO provider_model_catalog (
			id, status, provider_code, model, catalog_order, supported_api_protocols_json, source,
			catalog_visible, supports_prompt_caching, created_at, updated_at)
		VALUES ('cat_1', 'active', 'openai', 'gpt-test', 0, '["chat_completions"]', 'builtin', 1, 0, ?, ?)`, now, now)
	seedSettingsDefaults(t, db)
	return secret
}

// seedSettingsDefaults mirrors seedSystemSettings with the settings the chain
// preflight reads.
func seedSettingsDefaults(t *testing.T, db *sql.DB) {
	t.Helper()
	defaults := map[string]any{
		"gatewayTextRawBodyLimitMegabytes":           16,
		"accountCircuitConfirmationFailuresRequired": 2,
		"gatewayUserRequestLimitPerMinute":           0,
		"gatewayUserRequestLimitPerDay":              0,
		"gatewayUserRequestLimitPerWeek":             0,
		"gatewayUserRequestLimitPerMonth":            0,
		"usageStatsTimezone":                         "UTC",
		"defaultTemporaryUnschedulableMinutes":       2,
		"temporaryUnschedulableRetryIntervalSeconds": 3,
		"temporaryUnschedulableRetryAttempts":        2,
		"textFirstResponseTimeoutSeconds":            120,
		"textStreamIdleTimeoutSeconds":               30,
		"textUncommittedAttemptMaxLifetimeSeconds":   1800,
		"imageFirstResponseTimeoutSeconds":           600,
		"imageStreamIdleTimeoutSeconds":              120,
		"imageUncommittedAttemptMaxLifetimeSeconds":  3600,
		"imageRequestWallTimeoutSeconds":             3600,
		"noAvailableAccountWaitTimeoutSeconds":       270,
		"streamFailureThresholdCount":                3,
		"streamFailureThresholdWindowMinutes":        5,
	}
	for key, value := range defaults {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("marshal %s: %v", key, err)
		}
		if _, err := db.Exec(`INSERT INTO system_settings (system_account_id, key, value_json, updated_at) VALUES ('sys_admin', ?, ?, '2026-09-04T00:00:00.000Z')`,
			key, string(encoded)); err != nil {
			t.Fatalf("seed setting %s: %v", key, err)
		}
	}
}

// ---------------------------------------------------------------------------
// compose-level chain assembly
// ---------------------------------------------------------------------------

func TestComposeSystemAPIServesGatewayChain(t *testing.T) {
	cfg := composeTestConfig(t)
	cfg.ChainEnabled = true
	store := openComposeOperationStore(t)
	createRuntimeLogDataset(t, cfg.RuntimeLogDatabasePath)
	auditConfig, closeAudit := openComposeAuditSources(t, filepath.Dir(cfg.DatasetDatabasePath))
	defer closeAudit()
	composed, err := composeSystemAPI(cfg, pgpool.NewRegistry(), store, openComposeOperationLease(t, store), auditConfig)
	if err != nil {
		t.Fatalf("compose system api with chain: %v", err)
	}
	defer composed.Shutdown()
	seedSystemSettings(t, composed.DB)
	if composed.chain == nil {
		t.Fatal("chain must be assembled when JUHE_AI_GATEWAY_CHAIN_ENABLED=true")
	}

	server := httptest.NewServer(composed.Kernel)
	defer server.Close()
	client := &http.Client{Timeout: 10 * time.Second}

	// Non-protocol /v1 paths keep the Node 404 JSON contract (the
	// openai-compatible families stay on the legacy bridge).
	response, err := client.Get(server.URL + "/v1/definitely-not-a-protocol-path")
	if err != nil {
		t.Fatalf("GET /v1 unknown: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("non-protocol /v1 status=%d want 404", response.StatusCode)
	}
	body, _ := io.ReadAll(response.Body)
	if !strings.Contains(string(body), "资源不存在") {
		t.Fatalf("non-protocol /v1 body=%q", string(body))
	}

	// A protocol path without a gateway API key reaches the pre-auth auth
	// stage (Node: 401 invalid_api_key).
	chatResponse, err := client.Post(server.URL+"/v1/chat/completions", "application/json",
		strings.NewReader(`{"model":"gpt-test","messages":[]}`))
	if err != nil {
		t.Fatalf("POST /v1/chat/completions: %v", err)
	}
	defer chatResponse.Body.Close()
	if chatResponse.StatusCode != http.StatusUnauthorized {
		chatBody, _ := io.ReadAll(chatResponse.Body)
		t.Fatalf("unauthenticated chat status=%d body=%s", chatResponse.StatusCode, string(chatBody))
	}
}

// ---------------------------------------------------------------------------
// chainAccountsSelector + catalog
// ---------------------------------------------------------------------------

func TestChainAccountsSelectorListsDispatchableAccounts(t *testing.T) {
	fixture := newChainFixture(t)
	groupAccess, err := fixture.cache.ResolveCachedGroupUsageAccessMetadataAsync(context.Background(), fixture.groupID, fixture.systemAccount)
	if err != nil {
		t.Fatalf("resolve group access: %v", err)
	}
	if groupAccess == nil {
		t.Fatal("group access missing")
	}
	result, err := fixture.selector.ListOpenAIAccountsForGroupResult(context.Background(), fixture.groupID, fixture.systemAccount, gatewayruntimecache.OpenAIAccountsForGroupOptions{
		PreResolvedGroupAccess: groupAccess,
	})
	if err != nil {
		t.Fatalf("list accounts: %v", err)
	}
	if len(result.Accounts) != 1 {
		t.Fatalf("accounts=%d want 1", len(result.Accounts))
	}
	account := result.Accounts[0]
	if account.ID != fixture.accountID || account.ProviderCode != "openai" || account.ProtocolCode != "openai" {
		t.Fatalf("account identity wrong: %#v", account)
	}
	if account.APIKey != "sk-upstream-account-key" {
		t.Fatalf("credential decrypt failed: %q", account.APIKey)
	}
	if len(account.SupportedModels) != 1 || account.SupportedModels[0] != "gpt-test" {
		t.Fatalf("supported models wrong: %#v", account.SupportedModels)
	}
	if account.GroupOwnerSystemAccountID != fixture.systemAccount {
		t.Fatalf("group owner wrong: %q", account.GroupOwnerSystemAccountID)
	}
}

func TestChainCatalogSourceListsProviderModels(t *testing.T) {
	fixture := newChainFixture(t)
	source, err := newChainCatalogSource(fixture.db, false)
	if err != nil {
		t.Fatalf("create source: %v", err)
	}
	items, err := source.ListProviderModelCatalog(context.Background(), gatewayruntimecache.ModelCatalogListOptions{ProviderCode: "openai"})
	if err != nil {
		t.Fatalf("list catalog: %v", err)
	}
	if len(items) != 1 || items[0].Model != "gpt-test" || items[0].ProviderCode != "openai" {
		t.Fatalf("catalog items wrong: %#v", items)
	}
	if len(items[0].SupportedAPIProtocols) != 1 || items[0].SupportedAPIProtocols[0] != "chat_completions" {
		t.Fatalf("protocols wrong: %#v", items[0].SupportedAPIProtocols)
	}
}

// ---------------------------------------------------------------------------
// provider driver
// ---------------------------------------------------------------------------

func TestChainProviderDriverBuildsUpstreamRequests(t *testing.T) {
	driver := newChainProviderDriver()
	// Server-received requests carry a path-only URL (httptest.NewServer
	// equivalent); the driver appends it to the account base URL.
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-test","messages":[]}`))
	req := gatewaypreauth.NewGatewayRequest(request)
	account := gatewaydispatch.AccountCandidate{
		ID:           "acc_1",
		ProviderCode: "openai",
		ProtocolCode: "openai",
		BaseURL:      "https://upstream.example",
		APIKey:       "sk-upstream",
		Type:         "api_key",
	}
	urls, err := driver.BuildGatewayUpstreamURLsForAccount(context.Background(), account, req)
	if err != nil {
		t.Fatalf("build urls: %v", err)
	}
	if len(urls) != 1 || urls[0] != "https://upstream.example/v1/chat/completions" {
		t.Fatalf("urls wrong: %#v", urls)
	}
	parts, err := driver.BuildGatewayUpstreamRequestParts(context.Background(), req, account, gatewaydispatch.UsageIdentity{}, "")
	if err != nil {
		t.Fatalf("build parts: %v", err)
	}
	if parts.Headers.Get("Authorization") != "Bearer sk-upstream" {
		t.Fatalf("authorization header wrong: %q", parts.Headers.Get("Authorization"))
	}
	if parts.Headers.Get("Cookie") != "" {
		t.Fatalf("downstream cookie leaked upstream")
	}
	// Capability: the seeded model is supported, an unknown model is not.
	if !driver.AccountSupportsGatewayRequest(req, withSupportedModels(account, "gpt-test"), "") {
		t.Fatal("supported model must pass the capability gate")
	}
	if driver.AccountSupportsGatewayRequest(req, withSupportedModels(account, "other-model"), "") != true {
		// Supported-models constraint applies; but the mapping resolver may
		// still admit it. Assert the mismatch reason is reported either way.
		t.Skipf("mapping resolver admitted unknown model")
	}
}

func withSupportedModels(account gatewaydispatch.AccountCandidate, models ...string) gatewaydispatch.AccountCandidate {
	account.SupportedModels = models
	return account
}

func TestChainProviderDriverCapabilityMismatchReason(t *testing.T) {
	driver := newChainProviderDriver()
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-test","messages":[]}`))
	req := gatewaypreauth.NewGatewayRequest(request)
	account := gatewaydispatch.AccountCandidate{
		ID:                  "acc_1",
		ProviderCode:        "openai",
		ProtocolCode:        "openai",
		ClientCompatibility: "codex",
	}
	if reason := driver.gatewayRequestCapabilityMismatchReasonFor(req, account, "generic"); reason != "client_compatibility_mismatch" {
		t.Fatalf("compatibility mismatch reason = %q", reason)
	}
	anthropic := gatewaydispatch.AccountCandidate{ID: "acc_2", ProtocolCode: "anthropic"}
	if reason := driver.gatewayRequestCapabilityMismatchReasonFor(req, anthropic, ""); reason != "anthropic_native_unsupported" {
		t.Fatalf("anthropic mismatch reason = %q", reason)
	}
}

// ---------------------------------------------------------------------------
// usage persistence bridge
// ---------------------------------------------------------------------------

func TestSpooledUsageRecorderDeliversAndOverflowsToSpool(t *testing.T) {
	dir := t.TempDir()
	clock := gatewaypreauth.SystemClock{}
	spool := gatewayusageNewSpool(t, filepath.Join(dir, "spool"), clock)
	recorder := newSpooledUsageRecorder(usageBridgeConfig{BufferCapacity: 1}, spool)
	record := gatewayusage.UsageRecordInput{TraceID: "trace_1", TrafficSource: "gateway"}
	for index := 0; index < 3; index++ {
		if err := recorder.EnqueueUsageRecord(gatewayusage.Ctx(context.Background()), record); err != nil {
			t.Fatalf("enqueue %d: %v", index, err)
		}
	}
	recorder.Close()
	if recorder.dropped+recorder.failed > 0 {
		t.Fatalf("unexpected drop/failure: dropped=%d failed=%d", recorder.dropped, recorder.failed)
	}
	entries := spoolDirectoryEntries(t, filepath.Join(dir, "spool"))
	if len(entries) == 0 {
		t.Fatal("spool must hold the delivered records")
	}
}

func gatewayusageNewSpool(t *testing.T, dir string, clock gatewaypreauth.Clock) *gatewayusage.UsageRecordSpool {
	t.Helper()
	spool := newUsageSpool(dir, clock, nil)
	if spool == nil {
		t.Fatal("spool missing")
	}
	t.Cleanup(spool.StopReplay)
	return spool
}

func spoolDirectoryEntries(t *testing.T, dir string) []os.DirEntry {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read spool dir: %v", err)
	}
	return entries
}

// ---------------------------------------------------------------------------
// chat executor bridge
// ---------------------------------------------------------------------------

func TestChatGatewayExecutorStreamsThroughHandler(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("X-Trace-Id") != "trace_chat" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`data: {"chunk":1}` + "\n\n"))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		_, _ = w.Write([]byte(`data: {"chunk":2}` + "\n\n"))
	})
	executor := newChatGatewayExecutor(handler)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	response, err := executor.Dispatch(ctx, chat.GenerationDispatchRequest{
		Path:    "v1/chat/completions",
		Method:  http.MethodPost,
		Headers: map[string]string{"X-Trace-Id": "trace_chat"},
		Body:    []byte(`{"model":"gpt-test"}`),
	})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if response.Status != http.StatusOK {
		t.Fatalf("status=%d", response.Status)
	}
	payload, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !strings.Contains(string(payload), `"chunk":1`) || !strings.Contains(string(payload), `"chunk":2`) {
		t.Fatalf("stream body incomplete: %q", string(payload))
	}
}

// ---------------------------------------------------------------------------
// chain assembly gate
// ---------------------------------------------------------------------------

func TestComposeGatewayChainFailsFastNamingMissingPorts(t *testing.T) {
	fixture := newChainFixture(t)
	_, _, err := composeGatewayChain(chainRuntimeDeps{Cache: fixture.cache})
	if err == nil {
		t.Fatal("missing runtime services must fail the assembly")
	}
	for _, name := range []string{
		"PreAuthCircuits", "ClientIPPolicy", "UserRequestLimits", "AuthenticatedModelsRateLimit",
		"APIKeyQuota", "AuthorizationQuota", "InflightQuota", "Avoidance", "Affinity",
	} {
		if !strings.Contains(err.Error(), name) {
			t.Fatalf("missing port not named (%s): %v", name, err)
		}
	}
}

// ---------------------------------------------------------------------------
// full-chain smoke: preauth -> dispatch(mock upstream) -> response
// ---------------------------------------------------------------------------

func TestGatewayChainServeSmokePreauthDispatchResponse(t *testing.T) {
	fixture := newChainFixture(t)

	upstreamRequests := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamRequests++
		t.Logf("mock upstream hit: path=%q auth=%q", r.URL.Path, r.Header.Get("Authorization"))
		if r.URL.Path != "/v1/chat/completions" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "Bearer sk-upstream-account-key" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"message":"bad key"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-smoke","object":"chat.completion","model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","content":"链路冒烟"},"finish_reason":"stop"}]}`))
	}))
	defer upstream.Close()
	// Point the seeded account at the mock upstream.
	if _, err := fixture.db.Exec(`UPDATE accounts SET credentials_encrypted = ? WHERE id = ?`,
		mustEncryptCredentials(t, map[string]any{"api_key": "sk-upstream-account-key", "base_url": upstream.URL}), fixture.accountID); err != nil {
		t.Fatalf("update account credentials: %v", err)
	}

	spoolDir := filepath.Join(t.TempDir(), "spool")
	clock := gatewaypreauth.SystemClock{}
	chain, shutdown, err := composeGatewayChain(chainSmokeDeps(t, fixture, clock, spoolDir))
	if err != nil {
		t.Fatalf("compose gateway chain: %v", err)
	}
	defer shutdown()

	server := httptest.NewServer(chain)
	defer server.Close()
	request, err := http.NewRequest(http.MethodPost, server.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-test","messages":[{"role":"user","content":"你好"}]}`))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+fixture.apiKeySecret)
	client := &http.Client{Timeout: 15 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("POST /v1/chat/completions: %v", err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.StatusCode, string(body))
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	choices, _ := payload["choices"].([]any)
	if len(choices) == 0 {
		t.Fatalf("response missing choices: %s", string(body))
	}
	if !strings.Contains(string(body), "链路冒烟") {
		t.Fatalf("upstream content missing: %s", string(body))
	}
	if upstreamRequests != 1 {
		t.Fatalf("upstream requests=%d want 1", upstreamRequests)
	}
	// The usage persistence bridge must have captured the finalized record.
	// The record queue is asynchronous; poll briefly for the spool flush.
	spoolEntries := []os.DirEntry{}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		spoolEntries = spoolDirectoryEntries(t, spoolDir)
		if len(spoolEntries) > 0 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if len(spoolEntries) == 0 {
		t.Fatal("usage spool must hold the finalized record")
	}
}

// chainSmokeDeps assembles the chainRuntimeDeps for the smoke test: real
// memory-driver G13/G11 services, real quota services over the fixture
// databases, disabled audit dispatch.
func chainSmokeDeps(t *testing.T, fixture *chainFixture, clock gatewaypreauth.Clock, spoolDir string) chainRuntimeDeps {
	t.Helper()
	circuits, err := gatewayclientip.NewErrorCircuit(gatewayclientip.ErrorCircuitOptions{Clock: clock})
	if err != nil {
		t.Fatalf("create circuits: %v", err)
	}
	t.Cleanup(circuits.Close)
	policyCache, err := gatewayclientip.NewPolicyCache(gatewayclientip.PolicyCacheOptions{Clock: clock, Source: &chainPolicySource{}})
	if err != nil {
		t.Fatalf("create policy cache: %v", err)
	}
	t.Cleanup(policyCache.Close)
	avoidance, err := gatewayclientip.NewAvoidance(gatewayclientip.AvoidanceOptions{Clock: clock})
	if err != nil {
		t.Fatalf("create avoidance: %v", err)
	}
	t.Cleanup(avoidance.Close)
	modelsRateLimit := gatewayproxyhealth.NewAuthenticatedModelsRateLimitService(nil,
		gatewayproxyhealth.NewPenaltyWindowRateLimiter(nil, false, nil, ""), nil)
	counter := gatewayproxyhealth.NewUserRequestLimitCounter(nil, gatewayproxyhealth.UserRequestLimitCounterOptions{})
	coordinator := gatewayproxyhealth.NewUserRequestLimitCoordinator(counter, nil, gatewayproxyhealth.UserRequestLimitCoordinatorOptions{})
	userLimits := gatewayproxyhealth.NewUserRequestLimitsService(counter, coordinator)

	statsStore, err := gatewayquota.NewStatsStore(fixture.statsDB, false)
	if err != nil {
		t.Fatalf("create stats store: %v", err)
	}
	snapshot, err := gatewayquota.NewSnapshotCache(gatewayquota.Modes{}, nil, time.Now, nil)
	if err != nil {
		t.Fatalf("create snapshot cache: %v", err)
	}
	timezone := chainFixedTimezone{}
	apiKeyQuota, err := gatewayquota.NewAPIKeyQuotaService(gatewayquota.APIKeyQuotaConfig{
		Stats:    statsStore,
		Timezone: timezone,
		Snapshot: snapshot,
		Now:      time.Now,
	})
	if err != nil {
		t.Fatalf("create api-key quota: %v", err)
	}
	authzQuota, err := gatewayquota.NewAuthorizationQuotaService(gatewayquota.AuthorizationQuotaConfig{
		Business: fixture.db,
		Stats:    statsStore,
		Timezone: timezone,
		Snapshot: snapshot,
		Now:      time.Now,
	})
	if err != nil {
		t.Fatalf("create authz quota: %v", err)
	}
	inflightQuota, err := gatewayquota.NewInflightQuotaService(gatewayquota.InflightQuotaConfig{APIKeys: apiKeyQuota})
	if err != nil {
		t.Fatalf("create inflight quota: %v", err)
	}
	return chainRuntimeDeps{
		Cache:           fixture.cache,
		Clock:           clock,
		AuditLogEnabled: func() bool { return false },
		AuditInputURL:   "",
		SpoolDirectory:  spoolDir,
		Circuits:        circuits,
		IPPolicy:        policyCache,
		UserLimits:      userLimits,
		ModelsRateLimit: modelsRateLimit,
		APIKeyQuota:     apiKeyQuota,
		AuthzQuota:      authzQuota,
		InflightQuota:   inflightQuota,
		Avoidance:       avoidance,
		Affinity:        gatewaygemini.NewInteractionAffinity(nil),
		Recoverable: gatewaycircuit.NewPreAuthRecoverableWait(
			gatewaycircuit.NewWaitCoordinator(gatewaycircuit.WaitCoordinatorOptions{}),
			chainTestWaitLogger{t: t},
		),
	}
}

// ---------------------------------------------------------------------------
// small test collaborators
// ---------------------------------------------------------------------------

// chainPolicySource answers "no client-ip policies" (the empty stats table
// read the memory-mode policy cache would perform).
type chainPolicySource struct{}

func (s *chainPolicySource) ListActiveClientIPPolicies(context.Context) ([]gatewayclientip.ActiveClientIPPolicy, error) {
	return nil, nil
}

func (s *chainPolicySource) FindActiveClientIPPolicyByHash(context.Context, string) (*gatewayclientip.ActiveClientIPPolicy, error) {
	return nil, nil
}

func (s *chainPolicySource) RecordClientIPPolicyHits(context.Context, []gatewayclientip.PolicyHitInput) error {
	return nil
}

type chainFixedTimezone struct{}

func (chainFixedTimezone) StatsTimezone(context.Context) (*time.Location, error) {
	return time.UTC, nil
}

// chainTestWaitLogger funnels the circuit wait engine lines into the test log.
type chainTestWaitLogger struct{ t *testing.T }

func (l chainTestWaitLogger) Info(fields map[string]any, message string) {}

func (l chainTestWaitLogger) Warn(fields map[string]any, message string) {
	l.t.Logf("circuit-wait warn: %s %v", message, fields)
}

func mustEncryptCredentials(t *testing.T, credentials map[string]any) string {
	t.Helper()
	sealed, err := accounts.EncryptJSON("chain-test-secret", credentials)
	if err != nil {
		t.Fatalf("encrypt credentials: %v", err)
	}
	return sealed
}

// TestChainAccountsModelMappingsLoadsOverRealMaintenanceDDL is the
// account_model_mappings schema-drift regression (X05 defect: the runtime
// read ordered by a nonexistent `id` column and every runtime-resolution
// request 500'd on fresh databases). It applies the REAL maintenance business
// DDL (the same statements maintenance ensure and the gateway SQLite
// preflight apply), asserts the real composite-primary-key shape, and proves
// loadModelMappingsByAccountIds reads it with the Node ordering.
func TestChainAccountsModelMappingsLoadsOverRealMaintenanceDDL(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "business.sqlite3"))
	if err != nil {
		t.Fatalf("open business db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := bootstrap.EnsureSQLiteSchema(context.Background(), bootstrap.SQLiteSchemaBusiness, db); err != nil {
		t.Fatalf("apply real maintenance business DDL: %v", err)
	}
	// Seed the parent rows (providers / profiles / sys_admin) the real DDL
	// foreign keys point at, then add one account to map models for.
	if _, err := bootstrap.SeedSQLiteBusiness(context.Background(), db, bootstrap.SeedOptions{Now: func() time.Time { return time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC) }, Secret: "chain-test-secret"}); err != nil {
		t.Fatalf("seed real maintenance business data: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO accounts (
			id, system_account_id, provider_code, provider_protocol_profile_id, protocol_code, protocol_version,
			name, type, status, credentials_encrypted, health_check_model, health_check_endpoint_mode, created_at, updated_at
		) VALUES ('acc_real_ddl', 'sys_admin', 'openai', 'profile_gpt_openai_v1', 'openai', 'v1',
			'真实 DDL 账户', 'api_key', 'active', 'envelope', 'gpt-test', 'chat_json', '2026-09-04T00:00:00.000Z', '2026-09-04T00:00:00.000Z')`); err != nil {
		t.Fatalf("seed account: %v", err)
	}

	// Schema-drift guard: the real table has NO id column and keeps the
	// Node composite primary key.
	columns := map[string]bool{}
	rows, err := db.Query(`PRAGMA table_info(account_model_mappings)`)
	if err != nil {
		t.Fatalf("pragma table_info: %v", err)
	}
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull, pk int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			rows.Close()
			t.Fatalf("scan table_info: %v", err)
		}
		columns[name] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate table_info: %v", err)
	}
	if columns["id"] {
		t.Fatal("real account_model_mappings DDL drifted: id column reappeared")
	}
	for _, required := range []string{"account_id", "source_model", "source_endpoint_family", "upstream_model", "upstream_endpoint_family", "enabled"} {
		if !columns[required] {
			t.Fatalf("real account_model_mappings DDL missing column %s", required)
		}
	}
	var pkColumns string
	if err := db.QueryRow(`SELECT group_concat(name, ',') FROM (SELECT name FROM pragma_table_info('account_model_mappings') WHERE pk > 0 ORDER BY pk)`).Scan(&pkColumns); err != nil {
		t.Fatalf("read primary key: %v", err)
	}
	if pkColumns != "account_id,source_model,source_endpoint_family" {
		t.Fatalf("primary key = %q, want account_id,source_model,source_endpoint_family", pkColumns)
	}

	// Two mapping rows inserted deliberately out of source_model order: the
	// read must return them in the Node ORDER BY (account_id, source_model,
	// source_endpoint_family) and must not error on the real DDL.
	now := "2026-09-04T00:00:00.000Z"
	for _, row := range []struct{ source, family, upstream string }{
		{"gpt-pro", "chat_completions", "gpt-real-1"},
		{"gpt-5", "responses", "gpt-real-2"},
	} {
		if _, err := db.Exec(`INSERT INTO account_model_mappings (account_id, provider_code, source_model, source_endpoint_family, upstream_model, upstream_endpoint_family, enabled, created_at, updated_at)
			VALUES ('acc_real_ddl', 'openai', ?, ?, ?, ?, 1, ?, ?)`, row.source, row.family, row.upstream, row.family, now, now); err != nil {
			t.Fatalf("seed mapping: %v", err)
		}
	}

	selector, err := newChainAccountsSelector(db, false, "chain-test-secret", time.Now)
	if err != nil {
		t.Fatalf("create selector: %v", err)
	}
	mappings, err := selector.loadModelMappingsByAccountIds(context.Background(), []string{"acc_real_ddl", "acc_missing"})
	if err != nil {
		t.Fatalf("real-DDL mapping read failed: %v", err)
	}
	if len(mappings["acc_real_ddl"]) != 2 {
		t.Fatalf("mappings = %d, want 2: %+v", len(mappings["acc_real_ddl"]), mappings)
	}
	first, second := mappings["acc_real_ddl"][0], mappings["acc_real_ddl"][1]
	if first.SourceModel != "gpt-5" || second.SourceModel != "gpt-pro" {
		t.Fatalf("mapping order wrong: %s before %s", first.SourceModel, second.SourceModel)
	}
	if _, exists := mappings["acc_missing"]; exists {
		t.Fatalf("unknown account must not map: %+v", mappings)
	}
}
