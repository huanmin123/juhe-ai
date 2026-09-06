package accounts

// M11 contract tests: the advanced / oauth-reauthorization-context /
// api-key-runtime / balance / force-activate / traffic-migration /
// return-authorization / authorized-dispatch / group-binding route families
// against the Node archive contract (status + payload copy + row effects).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

// m11ExtraDDL carries the columns/tables the M11 families read that the base
// fixture lacks: the instance owner stamp column (Node
// accounts.authorization_instance_owner_system_account_id) and the
// juhe_stats.account_usage_snapshots relay_balance rows (SQLite keeps one
// file).
var m11ExtraDDL = []string{
	`ALTER TABLE accounts ADD COLUMN authorization_instance_owner_system_account_id TEXT`,
	`CREATE TABLE IF NOT EXISTS account_usage_snapshots (
		system_account_id TEXT NOT NULL,
		account_id TEXT NOT NULL,
		kind TEXT NOT NULL,
		source TEXT NOT NULL DEFAULT 'upstream_api',
		snapshot_json TEXT NOT NULL,
		refresh_status TEXT NOT NULL DEFAULT 'pending',
		last_attempt_at TEXT,
		last_success_at TEXT,
		next_refresh_after TEXT,
		last_error_message TEXT,
		updated_at TEXT NOT NULL,
		created_at TEXT NOT NULL,
		PRIMARY KEY (system_account_id, account_id, kind)
	)`,
}

// fakeBalanceRefresher records the manual refresh invocations and replays the
// canned outcome.
type fakeBalanceRefresher struct {
	mu        sync.Mutex
	refreshes []BalanceRefreshCandidate
	outcome   BalanceManualRefreshOutcome
	err       error
	draft     map[string]any
	draftErr  error
}

func (f *fakeBalanceRefresher) RefreshManual(_ context.Context, candidate BalanceRefreshCandidate) (BalanceManualRefreshOutcome, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refreshes = append(f.refreshes, candidate)
	return f.outcome, f.err
}

func (f *fakeBalanceRefresher) TestDraft(_ context.Context, input BalanceDraftProbeInput) (map[string]any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.draftErr != nil {
		return nil, f.draftErr
	}
	if f.draft != nil {
		return f.draft, nil
	}
	return map[string]any{
		"status":       "fresh",
		"remainingUsd": "12.5",
		"probeKey":     input.Config["adapter"],
	}, nil
}

type fakeModelCatalogRefresher struct {
	result map[string]any
	err    error
	calls  int
}

func (f *fakeModelCatalogRefresher) RefreshDraftModelCatalog(_ context.Context, _ ModelCatalogDiscoveryInput) (map[string]any, error) {
	f.calls++
	return f.result, f.err
}

// recordingReturner captures the authz Return handover.
type recordingReturner struct {
	mu       sync.Mutex
	grants   []string
	grantees []string
	versions []string
	status   string
}

func (r *recordingReturner) Return(_ context.Context, grantID, expectedUpdatedAt, granteeUserID string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.grants = append(r.grants, grantID)
	r.versions = append(r.versions, expectedUpdatedAt)
	r.grantees = append(r.grantees, granteeUserID)
	if r.status == "" {
		return "updated", nil
	}
	return r.status, nil
}

// newM11TestEnv mounts a fresh kernel over the base fixture with the M11
// extra schema; the returned store exposes the narrow ports for fakes.
func newM11TestEnv(t *testing.T) (*testEnv, *Store) {
	t.Helper()
	base := newTestEnv(t)
	for _, statement := range m11ExtraDDL {
		// Multiple envs over one test share the named in-memory database; a
		// column added by a previous env is fine.
		if _, err := base.db.Exec(statement); err != nil && !strings.Contains(err.Error(), "duplicate column name") {
			t.Fatal(err)
		}
	}
	store, err := NewStore(base.db, false, testSecret, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	k := kernel.New(kernel.Options{CompressionDisabled: true})
	base.deps.MountAuth(k, "lax", false)
	(&Deps{Store: store, Auth: base.deps, Sink: base.sink}).Mount(k)
	server := httptest.NewServer(k.Handler())
	t.Cleanup(server.Close)
	base.server = server
	return base, store
}

// seedM11Account inserts an account row with explicit columns.
func (e *testEnv) seedM11Account(t *testing.T, id, ownerID, name, accountType, status string, credentials Credentials) {
	t.Helper()
	sealed, err := EncryptJSON(testSecret, credentials)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	e.exec(t, `INSERT INTO accounts (id, system_account_id, provider_code, provider_protocol_profile_id,
		protocol_code, protocol_version, name, type, status, credentials_encrypted, credential_mask,
		health_check_model, schedulable, created_at, updated_at)
		VALUES (?, ?, 'gpt', 'prof-gpt', 'openai', 'v1', ?, ?, ?, ?, 'sk-***', 'gpt-4o-mini', 1, ?, ?)`,
		id, ownerID, name, accountType, status, sealed, now, now)
}

// seedBalanceAccount adds the balance query columns to an account row.
func (e *testEnv) seedBalanceAccount(t *testing.T, id, configJSON string, enabled bool, nextRefreshAt any) {
	t.Helper()
	enabledInt := 0
	if enabled {
		enabledInt = 1
	}
	if nextRefreshAt == nil {
		e.exec(t, `UPDATE accounts SET balance_query_enabled = ?, balance_query_config_json = ?,
			balance_query_next_refresh_at = NULL WHERE id = ?`, enabledInt, configJSON, id)
		return
	}
	e.exec(t, `UPDATE accounts SET balance_query_enabled = ?, balance_query_config_json = ?,
		balance_query_next_refresh_at = ? WHERE id = ?`, enabledInt, configJSON, nextRefreshAt, id)
}

func (e *testEnv) seedBalanceSnapshot(t *testing.T, ownerID, accountID, snapshotJSON, nextRefreshAfter, updatedAt string) {
	t.Helper()
	e.exec(t, `INSERT INTO account_usage_snapshots (system_account_id, account_id, kind, source,
		snapshot_json, refresh_status, next_refresh_after, updated_at, created_at)
		VALUES (?, ?, 'relay_balance', 'upstream_api', ?, 'fresh', ?, ?, ?)
		ON CONFLICT(system_account_id, account_id, kind) DO UPDATE SET
		snapshot_json = excluded.snapshot_json, next_refresh_after = excluded.next_refresh_after,
		updated_at = excluded.updated_at`, ownerID, accountID, snapshotJSON, nextRefreshAfter, updatedAt, updatedAt)
}

func TestM11AdvancedDetailContract(t *testing.T) {
	env, _ := newM11TestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	env.seedM11Account(t, "acc-adv", adminID, "高级账户", "api_key", "active",
		Credentials{"api_key": "sk-adv-secret", "error_handling_rules": []any{}, "quota_recovery_policy": map[string]any{
			"api_key": map[string]any{"reset_strategy": "duration", "duration_minutes": float64(90), "jitter_minutes": float64(15), "timezone": "UTC"},
		}})
	env.exec(t, `UPDATE accounts SET balance_query_enabled = 1, balance_query_config_json = '{"adapter":"builtin","intervalMinutes":5}' WHERE id = 'acc-adv'`)

	code, payload := env.do(t, http.MethodGet, "/__aisys__/api/accounts/acc-adv/advanced", "")
	if code != http.StatusOK {
		t.Fatalf("advanced: %d %v", code, payload)
	}
	data := dataMap(t, payload)
	if data["id"] != "acc-adv" || data["configRevision"] != float64(1) || data["accessType"] != "owner" {
		t.Fatalf("advanced identity: %v", data)
	}
	if data["balanceQueryEnabled"] != true {
		t.Fatalf("balanceQueryEnabled: %v", data)
	}
	config, ok := data["balanceQueryConfig"].(map[string]any)
	if !ok || config["adapter"] != "builtin" || config["intervalMinutes"] != float64(5) {
		t.Fatalf("balanceQueryConfig: %v", config)
	}
	policy, ok := data["effectiveQuotaRecoveryPolicy"].(map[string]any)
	if !ok {
		t.Fatalf("effectiveQuotaRecoveryPolicy missing: %v", data)
	}
	apiKeySchedule := policy["api_key"].(map[string]any)
	if apiKeySchedule["duration_minutes"] != float64(90) || apiKeySchedule["jitter_minutes"] != float64(15) {
		t.Fatalf("configured api_key schedule: %v", apiKeySchedule)
	}
	oauthSchedule := policy["oauth"].(map[string]any)
	if oauthSchedule["reset_strategy"] != "daily" || oauthSchedule["daily_reset_hour"] != float64(0) {
		t.Fatalf("oauth fallback schedule: %v", oauthSchedule)
	}
	rules, ok := data["effectiveErrorHandlingRules"].([]any)
	if !ok || len(rules) != 1 {
		t.Fatalf("effectiveErrorHandlingRules: %v", data["effectiveErrorHandlingRules"])
	}
	first := rules[0].(map[string]any)
	if first["id"] != "system.upstream_insufficient_quota" || first["source"] != "system" || first["inherited"] != true || first["editable"] != false {
		t.Fatalf("system rule projection: %v", first)
	}
	if _, leaked := data["credentials"]; leaked {
		// Owner rows project the advanced-editable keys only; the raw
		// api_key secret must never appear.
		if credentials := data["credentials"].(map[string]any); len(credentials) == 0 {
			t.Fatal("credentials projection must not be empty when policy keys exist")
		} else if _, has := credentials["api_key"]; has {
			t.Fatalf("api_key leaked through advanced credentials: %v", credentials)
		}
	}
	// Lock defaults ride the projection.
	if data["lockEnabled"] != false || data["lockState"] != "UNLOCKED" || data["lockDeathTimeoutSeconds"] != float64(300) {
		t.Fatalf("lock projection: %v", data)
	}

	// Missing account → 404.
	code, missing := env.do(t, http.MethodGet, "/__aisys__/api/accounts/acc-missing/advanced", "")
	if code != http.StatusNotFound || missing["message"] != "账户不存在" {
		t.Fatalf("missing advanced: %d %v", code, missing)
	}

	// my-accounts mirror serves the same contract.
	code, mine := env.do(t, http.MethodGet, "/__aisys__/api/my-accounts/acc-adv/advanced", "")
	if code != http.StatusOK || dataMap(t, mine)["id"] != "acc-adv" {
		t.Fatalf("my-accounts advanced: %d %v", code, mine)
	}
}

func TestM11OAuthReauthorizationContext(t *testing.T) {
	env, _ := newM11TestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	// The gemini provider row the interaction context filters on.
	now := time.Now().UTC().Format(time.RFC3339Nano)
	env.exec(t, `INSERT OR IGNORE INTO providers (id, code, name, enabled, created_at, updated_at)
		VALUES ('prov-gemini', 'gemini', 'Gemini', 1, ?, ?)`, now, now)
	sealed, err := EncryptJSON(testSecret, Credentials{
		"oauth_type": "ai_studio", "client_id": "cid-1", "client_secret": "sec-1",
		"quota_project_id": "qp-1", "project_id": "pj-1", "tier_id": "tier-1",
		"base_url": "https://generativelanguage.googleapis.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	env.exec(t, `INSERT INTO accounts (id, system_account_id, provider_code, provider_protocol_profile_id,
		protocol_code, protocol_version, name, type, status, credentials_encrypted, credential_mask,
		health_check_model, created_at, updated_at)
		VALUES ('acc-gem', ?, 'gemini', 'prof-gem', 'gemini', 'v1', 'Gemini 账户', 'google_oauth', 'active',
		?, '***', '', ?, ?)`, adminID, sealed, now, now)

	code, payload := env.do(t, http.MethodGet, "/__aisys__/api/accounts/acc-gem/oauth-reauthorization-context", "")
	if code != http.StatusOK {
		t.Fatalf("oauth context: %d %v", code, payload)
	}
	data := dataMap(t, payload)
	if data["oauthType"] != "ai_studio" || data["clientId"] != "cid-1" || data["clientSecret"] != "sec-1" ||
		data["quotaProjectId"] != "qp-1" || data["projectId"] != "pj-1" || data["tierId"] != "tier-1" {
		t.Fatalf("oauth context projection: %v", data)
	}
	if data["baseUrl"] != "https://generativelanguage.googleapis.com" {
		t.Fatalf("baseUrl: %v", data)
	}

	// Non-google_oauth rows stay invisible (404).
	code, wrong := env.do(t, http.MethodGet, "/__aisys__/api/my-accounts/acc-adv/oauth-reauthorization-context", "")
	if wrong == nil || code != http.StatusNotFound {
		t.Fatalf("non-gemini context: %d %v", code, wrong)
	}
}

func TestM11APIKeyRuntimeRead(t *testing.T) {
	env, _ := newM11TestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	env.seedM11Account(t, "acc-pool", adminID, "池账户", "api_key", "active", Credentials{"api_keys": []any{"sk-a", "sk-b"}})

	code, payload := env.do(t, http.MethodGet, "/__aisys__/api/accounts/acc-pool/api-key-runtime", "")
	if code != http.StatusOK {
		t.Fatalf("api-key-runtime: %d %v", code, payload)
	}
	data := dataMap(t, payload)
	if data["accountId"] != "acc-pool" || data["configRevision"] != float64(1) {
		t.Fatalf("runtime identity: %v", data)
	}
	items, ok := data["items"].([]any)
	if !ok || len(items) != 0 {
		t.Fatalf("runtime items default: %v", data["items"])
	}
	// Missing account → 404.
	code, missing := env.do(t, http.MethodGet, "/__aisys__/api/accounts/acc-missing/api-key-runtime", "")
	if code != http.StatusNotFound || missing["message"] != "账户不存在" {
		t.Fatalf("missing runtime: %d %v", code, missing)
	}
}

func TestM11BalanceDetailsProjection(t *testing.T) {
	env, _ := newM11TestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	env.seedM11Account(t, "acc-bal", adminID, "余额账户", "api_key", "active",
		Credentials{"api_key": "sk-balance-key-123456"})
	env.seedBalanceAccount(t, "acc-bal", `{"adapter":"builtin","intervalMinutes":5}`, true, "2026-09-04T00:05:00.000Z")
	// One stored per-Key entry keyed by the HMAC fingerprint; the config
	// revision matches so the snapshot counts as current.
	env.exec(t, "UPDATE accounts SET config_revision = 3 WHERE id = 'acc-bal'")
	store, err := NewStore(env.db, false, testSecret, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := store.balanceAPIKeyFingerprint("sk-balance-key-123456")
	env.seedBalanceSnapshot(t, adminID, "acc-bal",
		`{"status":"fresh","configRevision":3,"scope":"key","aggregation":"sum","queriedKeyCount":1,
		  "keyBalances":[{"keyFingerprint":"`+fingerprint+`","maskedKey":"sk-b…3456","status":"fresh","remainingUsd":"8.25","rawUnit":"usd"}]}`,
		"2026-09-04T00:05:00.000Z", "2026-09-04T00:04:30.000Z")

	code, payload := env.do(t, http.MethodGet, "/__aisys__/api/accounts/acc-bal/balance/details", "")
	if code != http.StatusOK {
		t.Fatalf("balance details: %d %v", code, payload)
	}
	data := dataMap(t, payload)
	if data["accountId"] != "acc-bal" || data["configRevision"] != float64(3) {
		t.Fatalf("details identity: %v", data)
	}
	if data["keyCount"] != float64(1) || data["queriedKeyCount"] != float64(1) ||
		data["scope"] != "key" || data["aggregation"] != "sum" {
		t.Fatalf("details summary: %v", data)
	}
	if data["updatedAt"] != "2026-09-04T00:04:30.000Z" {
		t.Fatalf("details updatedAt: %v", data)
	}
	keyBalances := data["keyBalances"].([]any)
	if len(keyBalances) != 1 {
		t.Fatalf("keyBalances: %v", keyBalances)
	}
	entry := keyBalances[0].(map[string]any)
	if entry["keyFingerprint"] != fingerprint || entry["status"] != "fresh" || entry["remainingUsd"] != "8.25" {
		t.Fatalf("key balance entry: %v", entry)
	}

	// A stale config revision maps every Key back to pending and drops the
	// stored timestamp.
	env.seedBalanceSnapshot(t, adminID, "acc-bal",
		`{"status":"fresh","configRevision":1,"keyBalances":[]}`,
		"2026-09-04T00:05:00.000Z", "2026-09-04T00:04:30.000Z")
	code, stale := env.do(t, http.MethodGet, "/__aisys__/api/my-accounts/acc-bal/balance/details", "")
	if code != http.StatusOK {
		t.Fatalf("stale details: %d %v", code, stale)
	}
	staleData := dataMap(t, stale)
	if staleData["updatedAt"] != nil {
		t.Fatalf("stale snapshot must not expose updatedAt: %v", staleData)
	}
	entries := staleData["keyBalances"].([]any)
	if len(entries) != 1 || entries[0].(map[string]any)["status"] != "pending" {
		t.Fatalf("stale keys map to pending: %v", entries)
	}

	// Disabled query → 404 with the Node copy.
	env.exec(t, "UPDATE accounts SET balance_query_enabled = 0 WHERE id = 'acc-bal'")
	code, disabled := env.do(t, http.MethodGet, "/__aisys__/api/accounts/acc-bal/balance/details", "")
	if code != http.StatusNotFound || disabled["message"] != "账户未开启余额查询" {
		t.Fatalf("disabled details: %d %v", code, disabled)
	}
}

func TestM11BalanceRefreshContract(t *testing.T) {
	env, store := newM11TestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	env.seedM11Account(t, "acc-refresh", adminID, "刷新账户", "api_key", "active", Credentials{"api_key": "sk-refresh"})
	env.seedBalanceAccount(t, "acc-refresh", `{"adapter":"builtin","intervalMinutes":5}`, true, nil)

	// A non-candidate account (query disabled) → 400 with the Node copy.
	env.seedM11Account(t, "acc-off", adminID, "未开启账户", "api_key", "active", Credentials{"api_key": "sk-off"})
	code, off := env.do(t, http.MethodPost, "/__aisys__/api/accounts/acc-off/balance/refresh", "")
	if code != http.StatusBadRequest || off["message"] != "账户未开启余额查询或当前账户类型不支持" {
		t.Fatalf("disabled refresh: %d %v", code, off)
	}

	// Unwired port → the Node unexpected-failure shape (500).
	code, failed := env.do(t, http.MethodPost, "/__aisys__/api/accounts/acc-refresh/balance/refresh", "")
	if code != http.StatusInternalServerError || failed["message"] != "服务器内部错误" {
		t.Fatalf("nil refresher: %d %v", code, failed)
	}

	refresher := &fakeBalanceRefresher{}
	store.SetManualBalanceRefresher(refresher)

	// lease_busy → 409.
	refresher.outcome = BalanceManualRefreshOutcome{Persisted: false, Outcome: "lease_busy"}
	code, busy := env.do(t, http.MethodPost, "/__aisys__/api/accounts/acc-refresh/balance/refresh", "")
	if code != http.StatusConflict || busy["message"] != "余额查询正在进行，请稍后刷新" {
		t.Fatalf("lease busy: %d %v", code, busy)
	}
	// stale → 409.
	refresher.outcome = BalanceManualRefreshOutcome{Persisted: false, Outcome: "stale"}
	code, staleRefresh := env.do(t, http.MethodPost, "/__aisys__/api/my-accounts/acc-refresh/balance/refresh", "")
	if code != http.StatusConflict || staleRefresh["message"] != "账户余额配置已变化，请刷新列表后重试" {
		t.Fatalf("stale refresh: %d %v", code, staleRefresh)
	}
	// committed → 200 + the candidate carries the localized row.
	refresher.outcome = BalanceManualRefreshOutcome{Persisted: true,
		Snapshot: map[string]any{"status": "fresh", "remainingUsd": "42"}}
	code, refreshed := env.do(t, http.MethodPost, "/__aisys__/api/accounts/acc-refresh/balance/refresh", "")
	if code != http.StatusOK {
		t.Fatalf("committed refresh: %d %v", code, refreshed)
	}
	snapshot := dataMap(t, refreshed)
	if snapshot["status"] != "fresh" || snapshot["remainingUsd"] != "42" {
		t.Fatalf("refresh snapshot: %v", snapshot)
	}
	last := refresher.refreshes[len(refresher.refreshes)-1]
	if last.ID != "acc-refresh" || last.SystemAccountID != adminID || last.ConfigRevision != 1 {
		t.Fatalf("refresh candidate localization: %+v", last)
	}
	// Missing account → 404.
	code, missing := env.do(t, http.MethodPost, "/__aisys__/api/accounts/acc-missing/balance/refresh", "")
	if code != http.StatusNotFound || missing["message"] != "账户不存在" {
		t.Fatalf("missing refresh: %d %v", code, missing)
	}
}

func TestM11BalanceTestDraftContract(t *testing.T) {
	env, store := newM11TestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)

	accountBody := `{"account":{"providerCode":"gpt","providerProtocolProfileId":"prof-gpt","name":"草稿",` +
		`"type":"api_key","groupId":"grp-default-` + adminID + `","credentials":{"api_key":"sk-draft","base_url":"https://api.openai.com/v1"},` +
		`"healthCheckModel":"gpt-4o-mini","healthCheckEndpointMode":"chat_json"},` +
		`"balanceQueryConfig":{"adapter":"builtin","preferredBuiltinAdapter":"sub2api"}}`

	// Strict body: unknown key → 400.
	code, bad := env.do(t, http.MethodPost, "/__aisys__/api/accounts/balance/test-draft",
		`{"account":{},"balanceQueryConfig":{"adapter":"builtin"},"bogus":1}`)
	if code != http.StatusBadRequest || bad["message"] != "余额查询测试参数无效" {
		t.Fatalf("test-draft strict body: %d %v", code, bad)
	}
	// Unknown group → 400 账户分组无效.
	code, wrongGroup := env.do(t, http.MethodPost, "/__aisys__/api/accounts/balance/test-draft",
		strings.Replace(accountBody, "grp-default-"+adminID, "grp-missing", 1))
	if code != http.StatusBadRequest || wrongGroup["message"] != "账户分组无效" {
		t.Fatalf("test-draft group guard: %d %v", code, wrongGroup)
	}
	// Unwired port resolves to the failed-snapshot shape (200).
	code, degraded := env.do(t, http.MethodPost, "/__aisys__/api/accounts/balance/test-draft", accountBody)
	if code != http.StatusOK {
		t.Fatalf("test-draft degraded: %d %v", code, degraded)
	}
	if dataMap(t, degraded)["status"] != "failed" {
		t.Fatalf("degraded draft snapshot: %v", degraded)
	}
	// Wired port → the probe snapshot rides through.
	refresher := &fakeBalanceRefresher{}
	store.SetManualBalanceRefresher(refresher)
	code, ok := env.do(t, http.MethodPost, "/__aisys__/api/my-accounts/balance/test-draft", accountBody)
	if code != http.StatusOK {
		t.Fatalf("test-draft: %d %v", code, ok)
	}
	data := dataMap(t, ok)
	if data["status"] != "fresh" || data["remainingUsd"] != "12.5" || data["probeKey"] != "builtin" {
		t.Fatalf("draft snapshot: %v", data)
	}
}

func TestM11ModelCatalogRefreshContract(t *testing.T) {
	env, store := newM11TestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	body := `{"account":{"providerCode":"gpt","providerProtocolProfileId":"prof-gpt","name":"目录",` +
		`"type":"api_key","groupId":"grp-default-` + adminID + `","credentials":{"api_key":"sk-catalog","base_url":"https://api.openai.com/v1"},` +
		`"healthCheckModel":"gpt-4o-mini","healthCheckEndpointMode":"chat_json"}}`

	// Unwired port → 400 with the Node fallback copy.
	code, degraded := env.do(t, http.MethodPost, "/__aisys__/api/accounts/model-catalog/refresh", body)
	if code != http.StatusBadRequest || degraded["message"] != "获取上游模型目录失败" {
		t.Fatalf("catalog degraded: %d %v", code, degraded)
	}
	refresher := &fakeModelCatalogRefresher{result: map[string]any{"models": []any{"gpt-4o-mini"}}}
	store.SetModelCatalogRefresher(refresher)
	code, ok := env.do(t, http.MethodPost, "/__aisys__/api/my-accounts/model-catalog/refresh", body)
	if code != http.StatusOK {
		t.Fatalf("catalog refresh: %d %v", code, ok)
	}
	if dataMap(t, ok)["models"] == nil || refresher.calls != 1 {
		t.Fatalf("catalog result: %v calls %d", ok, refresher.calls)
	}
	// Execution failure → 400 with the error message.
	refresher.err = fmt.Errorf("上游拒绝访问")
	code, failure := env.do(t, http.MethodPost, "/__aisys__/api/accounts/model-catalog/refresh", body)
	if code != http.StatusBadRequest || failure["message"] != "上游拒绝访问" {
		t.Fatalf("catalog failure: %d %v", code, failure)
	}
}

func TestM11ForceActivateContract(t *testing.T) {
	env, store := newM11TestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	env.seedM11Account(t, "acc-pending", adminID, "待检账户", "api_key", "pending_test", Credentials{"api_key": "sk-pending"})
	env.exec(t, `UPDATE accounts SET last_error_code = 'rate_limited', cooldown_until = '2026-09-04T01:00:00.000Z',
		cooldown_retest_failure_count = 2, stream_failure_count = 3 WHERE id = 'acc-pending'`)

	// Missing acknowledgement → 400.
	code, ack := env.do(t, http.MethodPost, "/__aisys__/api/accounts/acc-pending/force-activate", `{}`)
	if code != http.StatusBadRequest || ack["message"] != "请先确认账户当前可用并接受人工恢复风险" {
		t.Fatalf("acknowledgement guard: %d %v", code, ack)
	}
	// Non-pending → 409.
	env.seedM11Account(t, "acc-active", adminID, "在用账户", "api_key", "active", Credentials{"api_key": "sk-active"})
	code, conflict := env.do(t, http.MethodPost, "/__aisys__/api/accounts/acc-active/force-activate",
		`{"acknowledgedAccountAvailable":true}`)
	if code != http.StatusConflict || conflict["message"] != "只有待检查账户可以人工恢复可调度" {
		t.Fatalf("non-pending guard: %d %v", code, conflict)
	}
	// Missing → 404.
	code, missing := env.do(t, http.MethodPost, "/__aisys__/api/accounts/acc-missing/force-activate",
		`{"acknowledgedAccountAvailable":true}`)
	if code != http.StatusNotFound || missing["message"] != "账户不存在" {
		t.Fatalf("missing force-activate: %d %v", code, missing)
	}
	// Happy path: the pending CAS restore + the failure columns clear.
	code, activated := env.do(t, http.MethodPost, "/__aisys__/api/accounts/acc-pending/force-activate",
		`{"acknowledgedAccountAvailable":true}`)
	if code != http.StatusOK {
		t.Fatalf("force-activate: %d %v", code, activated)
	}
	account := dataMap(t, activated)
	if account["id"] != "acc-pending" || account["status"] != "active" || account["schedulable"] != true {
		t.Fatalf("activated summary: %v", account)
	}
	if env.count(t, `SELECT COUNT(*) FROM accounts WHERE id = 'acc-pending' AND status = 'active'
		AND schedulable = 1 AND cooldown_until IS NULL AND last_error_code IS NULL
		AND cooldown_retest_failure_count = 0 AND stream_failure_count = 0`) != 1 {
		t.Fatal("force-activate row contract violated")
	}
	if env.count(t, `SELECT COUNT(*) FROM account_circuit_outbox WHERE account_id = 'acc-pending'
		AND event_type = 'dispatch_revision_changed'`) != 1 {
		t.Fatal("force-activate dispatch revision advance missing")
	}
	seen := false
	for _, action := range env.sink.actions() {
		if action == "accounts.force_activate" {
			seen = true
		}
	}
	if !seen {
		t.Fatalf("force-activate operation log missing: %v", env.sink.actions())
	}
	// A row that flipped underneath re-reads changed=false (the route turns
	// that into 409 状态已变化; over HTTP the mutationGuard dedup owns the
	// second identical call first, so the store contract is pinned here).
	rerun, err := store.ForceActivatePending(context.Background(), "acc-pending", AccessScope{ViewerID: adminID, IsAdmin: true})
	if err != nil {
		t.Fatal(err)
	}
	if rerun == nil || rerun.Changed {
		t.Fatalf("re-run must stay unchanged: %+v", rerun)
	}
}

func TestM11TrafficMigrationContract(t *testing.T) {
	env, _ := newM11TestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	groupID := "grp-default-" + adminID
	bind := func(accountID string) {
		t.Helper()
		now := time.Now().UTC().Format(time.RFC3339Nano)
		env.exec(t, `INSERT OR REPLACE INTO group_accounts (system_account_id, group_id, account_id, enabled, created_at, updated_at)
			VALUES (?, ?, ?, 1, ?, ?)`, adminID, groupID, accountID, now, now)
	}
	env.seedM11Account(t, "acc-src", adminID, "源账户", "api_key", "active", Credentials{"api_key": "sk-src"})
	env.seedM11Account(t, "acc-dst", adminID, "目标账户", "api_key", "active", Credentials{"api_key": "sk-dst"})
	bind("acc-src")
	bind("acc-dst")

	// Body contract.
	code, bad := env.do(t, http.MethodPost, "/__aisys__/api/accounts/acc-src/traffic-migration",
		`{"targetAccountId":"acc-dst","sourceStatus":"bogus"}`)
	if code != http.StatusBadRequest || bad["message"] != "迁移流量参数无效" {
		t.Fatalf("traffic body: %d %v", code, bad)
	}
	// Self migration → 400.
	code, same := env.do(t, http.MethodPost, "/__aisys__/api/accounts/acc-src/traffic-migration",
		`{"targetAccountId":"acc-src"}`)
	if code != http.StatusBadRequest || same["message"] != "目标账户不能和当前账户相同" {
		t.Fatalf("same account: %d %v", code, same)
	}
	// Cross-group target → 400.
	env.exec(t, `INSERT INTO groups (id, system_account_id, name, provider_code, enabled, is_default, group_type, created_at, updated_at)
		VALUES ('grp-other', ?, '其它分组', 'gpt', 1, 0, 'personal', '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`, adminID)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	env.exec(t, `INSERT OR REPLACE INTO group_accounts (system_account_id, group_id, account_id, enabled, created_at, updated_at)
		VALUES (?, 'grp-other', 'acc-dst', 1, ?, ?)`, adminID, now, now)
	code, crossGroup := env.do(t, http.MethodPost, "/__aisys__/api/accounts/acc-src/traffic-migration",
		`{"targetAccountId":"acc-dst"}`)
	if code != http.StatusBadRequest || crossGroup["message"] != "目标账户必须和当前账户在同一个分组内" {
		t.Fatalf("cross group: %d %v", code, crossGroup)
	}
	// Restore the shared group binding (drop the cross-group row first: the
	// primary key is (group_id, account_id)).
	env.exec(t, `DELETE FROM group_accounts WHERE account_id = 'acc-dst'`)
	now2 := time.Now().UTC().Format(time.RFC3339Nano)
	env.exec(t, `INSERT INTO group_accounts (system_account_id, group_id, account_id, enabled, created_at, updated_at)
		VALUES (?, ?, 'acc-dst', 1, ?, ?)`, adminID, groupID, now2, now2)

	// Missing target → 404.
	code, missing := env.do(t, http.MethodPost, "/__aisys__/api/accounts/acc-src/traffic-migration",
		`{"targetAccountId":"acc-missing"}`)
	if code != http.StatusNotFound || missing["message"] != "账户不存在或无权迁移" {
		t.Fatalf("missing migration: %d %v", code, missing)
	}
	// Happy path: temporary_unavailable source + the runtime session count 0
	// (nil-port degraded fallback: with the chain disabled the composition
	// root keeps the port unwired and the route mirrors the Node IPC-miss
	// `?? { migratedSessionCount: 0 }`; the wired contract is pinned in
	// TestM11TrafficMigrationRuntimeMigratorWiring).
	code, migrated := env.do(t, http.MethodPost, "/__aisys__/api/accounts/acc-src/traffic-migration",
		`{"targetAccountId":"acc-dst"}`)
	if code != http.StatusOK {
		t.Fatalf("migration: %d %v", code, migrated)
	}
	data := dataMap(t, migrated)
	source := data["sourceAccount"].(map[string]any)
	target := data["targetAccount"].(map[string]any)
	if source["id"] != "acc-src" || target["id"] != "acc-dst" || data["sourceStatus"] != "temporary_unavailable" {
		t.Fatalf("migration payload: %v", data)
	}
	if data["migratedSessionCount"] != float64(0) {
		t.Fatalf("migratedSessionCount: %v", data["migratedSessionCount"])
	}
	if cooldown, _ := data["sourceCooldownUntil"].(string); cooldown == "" {
		t.Fatalf("sourceCooldownUntil missing: %v", data)
	}
	if env.count(t, `SELECT COUNT(*) FROM accounts WHERE id = 'acc-src' AND status = 'temporary_unavailable'
		AND cooldown_retest_generation IS NOT NULL AND last_error_message = '手动迁移流量'`) != 1 {
		t.Fatal("migration source row contract violated")
	}
	if env.count(t, `SELECT COUNT(*) FROM accounts WHERE id = 'acc-dst' AND status = 'active'`) != 1 {
		t.Fatal("migration target must stay untouched")
	}
	// disabled sourceStatus branch.
	env.exec(t, `UPDATE accounts SET status = 'active' WHERE id = 'acc-src'`)
	code, disabledMigration := env.do(t, http.MethodPost, "/__aisys__/api/my-accounts/acc-src/traffic-migration",
		`{"targetAccountId":"acc-dst","sourceStatus":"disabled"}`)
	if code != http.StatusOK || dataMap(t, disabledMigration)["sourceStatus"] != "disabled" {
		t.Fatalf("disabled migration: %d %v", code, disabledMigration)
	}
	if env.count(t, `SELECT COUNT(*) FROM accounts WHERE id = 'acc-src' AND status = 'disabled' AND schedulable = 0`) != 1 {
		t.Fatal("disabled source row contract violated")
	}
	seen := false
	for _, action := range env.sink.actions() {
		if action == "accounts.traffic_migration" {
			seen = true
		}
	}
	if !seen {
		t.Fatal("traffic migration operation log missing")
	}
}

func TestM11TrafficMigrationRuntimeMigratorWiring(t *testing.T) {
	env, store := newM11TestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	groupID := "grp-default-" + adminID
	bind := func(accountID string) {
		t.Helper()
		now := time.Now().UTC().Format(time.RFC3339Nano)
		env.exec(t, `INSERT OR REPLACE INTO group_accounts (system_account_id, group_id, account_id, enabled, created_at, updated_at)
			VALUES (?, ?, ?, 1, ?, ?)`, adminID, groupID, accountID, now, now)
	}
	env.seedM11Account(t, "acc-src", adminID, "源账户", "api_key", "active", Credentials{"api_key": "sk-src"})
	env.seedM11Account(t, "acc-dst", adminID, "目标账户", "api_key", "active", Credentials{"api_key": "sk-dst"})
	bind("acc-src")
	bind("acc-dst")

	migrator := &fakeTrafficRuntimeMigrator{count: 3}
	store.SetTrafficRuntimeMigrator(migrator)

	// Wired port: the migrated session count flows through and the runtime
	// input mirrors the route assembly (owner branch → no affinity scope, the
	// preference scope rides the source group).
	code, migrated := env.do(t, http.MethodPost, "/__aisys__/api/accounts/acc-src/traffic-migration",
		`{"targetAccountId":"acc-dst"}`)
	if code != http.StatusOK {
		t.Fatalf("migration: %d %v", code, migrated)
	}
	if dataMap(t, migrated)["migratedSessionCount"] != float64(3) {
		t.Fatalf("migratedSessionCount must surface the wired port result: %v", dataMap(t, migrated))
	}
	if len(migrator.inputs()) != 1 {
		t.Fatalf("migrator calls = %d", len(migrator.inputs()))
	}
	input := migrator.inputs()[0]
	if input.SourceAccountID != "acc-src" || input.TargetAccountID != "acc-dst" || input.PreferMigratedSessions {
		t.Fatalf("runtime input: %+v", input)
	}
	if input.AffinityScope != nil {
		t.Fatalf("owner branch must not carry an affinity scope: %+v", input.AffinityScope)
	}
	if input.PreferenceScope == nil || input.PreferenceScope.SystemAccountID != adminID || input.PreferenceScope.GroupID != groupID {
		t.Fatalf("preference scope: %+v", input.PreferenceScope)
	}

	// Failure path: the explicit error outlet (Node catch renders the
	// message); the route never silently degrades to zero.
	migrator.fail(errors.New("Redis 会话亲和迁移失败"))
	env.exec(t, `UPDATE accounts SET status = 'active' WHERE id = 'acc-src'`)
	code, failed := env.do(t, http.MethodPost, "/__aisys__/api/accounts/acc-src/traffic-migration",
		`{"targetAccountId":"acc-dst"}`)
	if code != http.StatusBadRequest || failed["message"] != "Redis 会话亲和迁移失败" {
		t.Fatalf("migrator failure must surface explicitly: %d %v", code, failed)
	}
	// The committed DB effect stays durable (Node: the repository write is
	// committed inside runLoggedOperationAsync before the runtime handover).
	if env.count(t, `SELECT COUNT(*) FROM accounts WHERE id = 'acc-src' AND status = 'temporary_unavailable'`) != 1 {
		t.Fatal("source row must stay migrated after the runtime failure")
	}
	// The operation log lands before the handover (Node ordering), so the
	// failed runtime migration keeps its audit trail.
	seen := false
	for _, action := range env.sink.actions() {
		if action == "accounts.traffic_migration" {
			seen = true
		}
	}
	if !seen {
		t.Fatal("traffic migration operation log missing on the failure path")
	}

	// unchanged branch: preferMigratedSessions=true and no preference write.
	migrator.reset(5)
	env.exec(t, `UPDATE accounts SET status = 'active' WHERE id = 'acc-src'`)
	code, unchanged := env.do(t, http.MethodPost, "/__aisys__/api/accounts/acc-src/traffic-migration",
		`{"targetAccountId":"acc-dst","sourceStatus":"unchanged"}`)
	if code != http.StatusOK || dataMap(t, unchanged)["migratedSessionCount"] != float64(5) {
		t.Fatalf("unchanged migration: %d %v", code, unchanged)
	}
	unchangedInput := migrator.inputs()[len(migrator.inputs())-1]
	if !unchangedInput.PreferMigratedSessions || unchangedInput.PreferenceScope != nil {
		t.Fatalf("unchanged runtime input: %+v", unchangedInput)
	}
}

// fakeTrafficRuntimeMigrator records the runtime handover inputs and replays
// the canned outcome.
type fakeTrafficRuntimeMigrator struct {
	mu    sync.Mutex
	calls []TrafficRuntimeMigrationInput
	count int
	err   error
}

func (f *fakeTrafficRuntimeMigrator) MigrateOpenAIAccountTrafficRuntime(_ context.Context, input TrafficRuntimeMigrationInput) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, input)
	if f.err != nil {
		return 0, f.err
	}
	return f.count, nil
}

func (f *fakeTrafficRuntimeMigrator) inputs() []TrafficRuntimeMigrationInput {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]TrafficRuntimeMigrationInput(nil), f.calls...)
}

func (f *fakeTrafficRuntimeMigrator) fail(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

func (f *fakeTrafficRuntimeMigrator) reset(count int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.count = count
	f.err = nil
}

func TestM11ReturnAuthorizationContract(t *testing.T) {
	env, store := newM11TestEnv(t)
	granteeID := env.login(t, "grantee1", "grantee-pass", "user")
	ownerID := env.login(t, "owner9", "owner-pass", "user")
	env.seedM11Account(t, "acc-ret", granteeID, "授权实例", "api_key", "active", Credentials{"api_key": "sk-ret"})
	now := time.Now().UTC().Format(time.RFC3339Nano)
	// The stamped instance + runtime authorization + direct grant.
	env.exec(t, `UPDATE accounts SET authorization_instance_authorization_id = 'ra-ret',
		authorization_instance_source_account_id = 'acc-ownsrc' WHERE id = 'acc-ret'`)
	env.exec(t, `INSERT INTO resource_authorizations (id, resource_type, resource_id, resource_owner_system_account_id,
		grantee_system_account_id, status, effective_source_type, created_by, created_at, updated_at)
		VALUES ('ra-ret', 'account', 'acc-ownsrc', ?, ?, 'active', 'manual', ?, ?, ?)`, ownerID, granteeID, ownerID, now, now)
	env.exec(t, `INSERT INTO resource_authorization_grants (id, resource_type, resource_id, resource_owner_system_account_id,
		grantee_type, grantee_system_account_id, status, created_by, created_at, updated_at)
		VALUES ('grant-ret', 'account', 'acc-ownsrc', ?, 'system_account', ?, 'active', ?, ?, ?)`,
		ownerID, granteeID, ownerID, now, now)
	returner := &recordingReturner{}
	store.SetAuthorizationGrantReturner(returner)
	env.login(t, "grantee1", "grantee-pass", "user")

	// Happy path: 204 + the authz Return handover carries grant/version/grantee.
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/my-accounts/acc-ret/return-authorization", "")
	if code != http.StatusNoContent || payload != nil && len(payload) > 0 {
		t.Fatalf("return: %d %v", code, payload)
	}
	if len(returner.grants) != 1 || returner.grants[0] != "grant-ret" ||
		returner.versions[0] == "" || returner.grantees[0] != granteeID {
		t.Fatalf("returner handover: %+v", returner)
	}
	seen := false
	for _, entry := range env.sink.entries {
		if entry.Action == "return" && entry.Module == "authorizations" {
			seen = true
		}
	}
	if !seen {
		t.Fatal("return operation log missing")
	}
	// A not_found terminal status renders 404 with the Node copy (a fresh
	// instance avoids the mutationGuard dedup on the first fingerprint).
	env.exec(t, `INSERT INTO accounts (id, system_account_id, provider_code, provider_protocol_profile_id,
		protocol_code, protocol_version, name, type, status, credentials_encrypted, credential_mask,
		health_check_model, authorization_instance_authorization_id, created_at, updated_at)
		VALUES ('acc-ret-b', ?, 'gpt', 'prof-gpt', 'openai', 'v1', '授权实例B', 'api_key', 'active',
		'sealed', '***', 'gpt-4o-mini', 'ra-ret-b', ?, ?)`, granteeID, now, now)
	env.exec(t, `INSERT INTO resource_authorizations (id, resource_type, resource_id, resource_owner_system_account_id,
		grantee_system_account_id, status, effective_source_type, created_by, created_at, updated_at)
		VALUES ('ra-ret-b', 'account', 'acc-ownsrc-b', ?, ?, 'active', 'manual', ?, ?, ?)`, ownerID, granteeID, ownerID, now, now)
	env.exec(t, `INSERT INTO resource_authorization_grants (id, resource_type, resource_id, resource_owner_system_account_id,
		grantee_type, grantee_system_account_id, status, created_by, created_at, updated_at)
		VALUES ('grant-ret-b', 'account', 'acc-ownsrc-b', ?, 'system_account', ?, 'active', ?, ?, ?)`,
		ownerID, granteeID, ownerID, now, now)
	returner.status = "not_found"
	code, notFound := env.do(t, http.MethodPost, "/__aisys__/api/my-accounts/acc-ret-b/return-authorization", "")
	if code != http.StatusNotFound || notFound["message"] != "授权账户不存在或不可归还" {
		t.Fatalf("return not_found: %d %v", code, notFound)
	}
	// A plain account without the instance stamp → 404.
	env.seedM11Account(t, "acc-plain", granteeID, "普通账户", "api_key", "active", Credentials{"api_key": "sk-plain"})
	code, plain := env.do(t, http.MethodPost, "/__aisys__/api/my-accounts/acc-plain/return-authorization", "")
	if code != http.StatusNotFound || plain["message"] != "授权账户不存在或不可归还" {
		t.Fatalf("plain return: %d %v", code, plain)
	}
	// Unwired port degrades to the same 404 (the localization cannot complete).
	env2, _ := newM11TestEnv(t)
	env2.login(t, "grantee2", "grantee-pass", "user")
	env2.seedM11Account(t, "acc-ret2", env2.queryCell(t, `SELECT id FROM system_accounts WHERE username = 'grantee2'`),
		"实例2", "api_key", "active", Credentials{"api_key": "sk-ret2"})
	env2.exec(t, `UPDATE accounts SET authorization_instance_authorization_id = 'ra-2' WHERE id = 'acc-ret2'`)
	code, unwired := env2.do(t, http.MethodPost, "/__aisys__/api/my-accounts/acc-ret2/return-authorization", "")
	if code != http.StatusNotFound || unwired["message"] != "授权账户不存在或不可归还" {
		t.Fatalf("unwired return: %d %v", code, unwired)
	}
}

func TestM11AuthorizedDispatchContract(t *testing.T) {
	env, _ := newM11TestEnv(t)
	granteeID := env.login(t, "grantee3", "grantee-pass", "user")
	ownerID := env.login(t, "owner7", "owner-pass", "user")
	env.login(t, "root", "root-pass", "super_admin")
	env.seedM11Account(t, "acc-ownsrc", ownerID, "授权源", "api_key", "active", Credentials{"api_key": "sk-ownsrc"})
	env.seedAuthorizationInstance(t, "acc-inst", granteeID, "ra-dispatch", "acc-ownsrc")
	now := time.Now().UTC().Format(time.RFC3339Nano)
	env.exec(t, `INSERT INTO resource_authorizations (id, resource_type, resource_id, resource_owner_system_account_id,
		grantee_system_account_id, status, effective_source_type, created_by, created_at, updated_at)
		VALUES ('ra-dispatch', 'account', 'acc-ownsrc', ?, ?, 'active', 'manual', ?, ?, ?)`, ownerID, granteeID, ownerID, now, now)
	env.exec(t, `INSERT INTO group_accounts (system_account_id, group_id, account_id, account_authorization_id,
		local_priority, local_super_priority_enabled, local_fallback_enabled, enabled, created_at, updated_at)
		VALUES (?, 'grp-dispatch', 'acc-inst', 'ra-dispatch', 5, 0, 0, 1, ?, ?)`, granteeID, now, now)

	// Body contract: revision + at least one change. Admin-surface probes
	// carry the grantee scope filter (Node scopedSystemAccountId).
	code, bad := env.do(t, http.MethodPatch, "/__aisys__/api/accounts/acc-inst/authorized-dispatch?systemAccountId="+granteeID,
		`{"expectedConfigRevision":1}`)
	if code != http.StatusBadRequest || bad["message"] != "请至少提交一项授权账户调度变更" {
		t.Fatalf("dispatch superRefine: %d %v", code, bad)
	}
	code, wrongRevision := env.do(t, http.MethodPatch, "/__aisys__/api/accounts/acc-inst/authorized-dispatch?systemAccountId="+granteeID,
		`{"expectedConfigRevision":9,"priority":10}`)
	if code != http.StatusConflict || wrongRevision["message"] != RevisionConflictMessage {
		t.Fatalf("dispatch revision conflict: %d %v", code, wrongRevision)
	}
	// The grantee session drives the self surface; re-login before the my-*
	// call (the admin probes above switched the cookie jar).
	env.login(t, "grantee3", "grantee-pass", "user")
	// Happy path: priority + super priority on the binding row.
	code, patched := env.do(t, http.MethodPatch, "/__aisys__/api/my-accounts/acc-inst/authorized-dispatch",
		`{"expectedConfigRevision":1,"priority":10,"superPriorityEnabled":true}`)
	if code != http.StatusOK {
		t.Fatalf("dispatch: %d %v", code, patched)
	}
	data := dataMap(t, patched)
	if data["id"] != "acc-inst" || data["configRevision"] != float64(2) {
		t.Fatalf("dispatch identity: %v", data)
	}
	changed := data["changedFields"].([]any)
	if len(changed) != 2 {
		t.Fatalf("dispatch changedFields: %v", changed)
	}
	patch := data["patch"].(map[string]any)
	if patch["priority"] != float64(10) || patch["superPriorityEnabled"] != true {
		t.Fatalf("dispatch patch: %v", patch)
	}
	if env.count(t, `SELECT COUNT(*) FROM group_accounts WHERE account_id = 'acc-inst'
		AND local_priority = 10 AND local_super_priority_enabled = 1`) != 1 {
		t.Fatal("dispatch binding row contract violated")
	}
	// Binding-only changes leave the account row untouched (Node only writes
	// the accounts columns for the failure-state/status branch); the response
	// still reports revision + 1 (Node integerValue(row.config_revision) + 1).
	if env.count(t, `SELECT COUNT(*) FROM accounts WHERE id = 'acc-inst' AND config_revision = 1`) != 1 {
		t.Fatal("binding-only dispatch must not bump the account row")
	}
	// Both dispatch flags → 400.
	code, both := env.do(t, http.MethodPatch, "/__aisys__/api/my-accounts/acc-inst/authorized-dispatch",
		`{"expectedConfigRevision":1,"superPriorityEnabled":true,"fallbackEnabled":true}`)
	if code != http.StatusBadRequest || both["message"] != "超级优先和降级备用不能同时开启" {
		t.Fatalf("dispatch flags conflict: %d %v", code, both)
	}
	// clearFailureState clears the failure columns and re-enables scheduling.
	env.exec(t, `UPDATE accounts SET status = 'temporary_unavailable', schedulable = 0,
		cooldown_until = '2026-09-04T02:00:00.000Z', last_error_code = 'rate_limited' WHERE id = 'acc-inst'`)
	env.login(t, "root", "root-pass", "super_admin")
	code, restored := env.do(t, http.MethodPatch, "/__aisys__/api/accounts/acc-inst/authorized-dispatch?systemAccountId="+granteeID,
		`{"expectedConfigRevision":1,"clearFailureState":true}`)
	if code != http.StatusOK {
		t.Fatalf("dispatch restore: %d %v", code, restored)
	}
	restoredData := dataMap(t, restored)
	if restoredData["configRevision"] != float64(2) { // 1 (binding-only did not bump) + 1
		t.Fatalf("restore revision: %v", restoredData)
	}
	if env.count(t, `SELECT COUNT(*) FROM accounts WHERE id = 'acc-inst' AND status = 'active'
		AND schedulable = 1 AND cooldown_until IS NULL AND last_error_code IS NULL
		AND config_revision = 2`) != 1 {
		t.Fatal("dispatch restore row contract violated")
	}
	// Unbound instance → 404 (the admin session from the restore probe).
	env.seedAuthorizationInstance(t, "acc-unbound", granteeID, "ra-none", "acc-ownsrc")
	code, unbound := env.do(t, http.MethodPatch, "/__aisys__/api/accounts/acc-unbound/authorized-dispatch?systemAccountId="+granteeID,
		`{"expectedConfigRevision":1,"priority":1}`)
	if code != http.StatusNotFound || unbound["message"] != "授权账户不存在或尚未绑定分组" {
		t.Fatalf("unbound dispatch: %d %v", code, unbound)
	}
}

func TestM11GroupBindingContract(t *testing.T) {
	env, _ := newM11TestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	code, created := env.do(t, http.MethodPost, "/__aisys__/api/accounts", createPayload("绑定账户"))
	if code != http.StatusCreated {
		t.Fatalf("seed create: %d %v", code, created)
	}
	accountID := dataMap(t, created)["id"].(string)
	env.exec(t, `INSERT INTO groups (id, system_account_id, name, provider_code, enabled, is_default, group_type, created_at, updated_at)
		VALUES ('grp-target', ?, '目标分组', 'gpt', 1, 0, 'personal', '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`, adminID)

	// Body contract: unknown key / missing group / bad revision → 400.
	code, badBody := env.do(t, http.MethodPost, "/__aisys__/api/accounts/"+accountID+"/group",
		`{"groupId":"grp-target","expectedConfigRevision":1,"bogus":1}`)
	if code != http.StatusBadRequest || badBody["message"] != "绑定分组参数无效" {
		t.Fatalf("group body: %d %v", code, badBody)
	}
	code, emptyGroup := env.do(t, http.MethodPost, "/__aisys__/api/accounts/"+accountID+"/group",
		`{"groupId":"","expectedConfigRevision":1}`)
	if code != http.StatusBadRequest || emptyGroup["message"] != "绑定分组参数无效" {
		t.Fatalf("group empty: %d %v", code, emptyGroup)
	}
	// Missing account → 400 (the Node group route has no 404 branch).
	code, missing := env.do(t, http.MethodPost, "/__aisys__/api/accounts/acc-missing/group",
		`{"groupId":"grp-target","expectedConfigRevision":1}`)
	if code != http.StatusBadRequest || missing["message"] != "账户不存在、授权已失效或分组不可用" {
		t.Fatalf("group missing: %d %v", code, missing)
	}
	// Happy path: the binding switch + the bind_group operation log.
	code, bound := env.do(t, http.MethodPost, "/__aisys__/api/accounts/"+accountID+"/group",
		`{"groupId":"grp-target","expectedConfigRevision":1}`)
	if code != http.StatusOK {
		t.Fatalf("group bind: %d %v", code, bound)
	}
	data := dataMap(t, bound)
	if data["id"] != accountID || data["configRevision"] != float64(2) {
		t.Fatalf("group bind identity: %v", data)
	}
	changed := data["changedFields"].([]any)
	if len(changed) != 1 || changed[0] != "groupId" {
		t.Fatalf("group changedFields: %v", changed)
	}
	if env.count(t, `SELECT COUNT(*) FROM group_accounts WHERE account_id = ? AND group_id = 'grp-target' AND enabled = 1`, accountID) != 1 {
		t.Fatal("group binding row missing")
	}
	bindSeen := false
	for _, action := range env.sink.actions() {
		if action == "accounts.bind_group" {
			bindSeen = true
		}
	}
	if !bindSeen {
		t.Fatalf("bind_group operation log missing: %v", env.sink.actions())
	}
	// Revision conflict → 409 with the repository message.
	code, conflict := env.do(t, http.MethodPost, "/__aisys__/api/my-accounts/"+accountID+"/group",
		`{"groupId":"grp-target","expectedConfigRevision":1}`)
	if code != http.StatusConflict {
		t.Fatalf("group conflict: %d %v", code, conflict)
	}
	if message, _ := conflict["message"].(string); !strings.Contains(message, "并发") && !strings.Contains(message, "刷新") {
		t.Fatalf("group conflict copy: %v", conflict)
	}
}

// TestM11EffectiveErrorHandlingRulesOrder pins the system/account merge
// order and the replace/delete overrides.
func TestM11EffectiveErrorHandlingRulesOrder(t *testing.T) {
	accountRules := []any{
		map[string]any{"enabled": true, "name": "低优先", "priority": float64(9), "action": "rate_limited", "reset_strategy": "duration", "duration_hours": float64(1), "status_codes": []any{float64(429)}},
		map[string]any{"enabled": true, "name": "高优先", "priority": float64(2), "action": "error_disabled", "reset_strategy": "manual", "keywords": []any{"额度"}},
	}
	overrides := []any{
		map[string]any{"system_rule_id": "system.upstream_insufficient_quota", "action": "replace", "rule_index": float64(1)},
	}
	effective, err := effectiveAccountErrorHandlingRules(accountRules, overrides)
	if err != nil {
		t.Fatal(err)
	}
	if len(effective) != 2 {
		t.Fatalf("replace removes the system rule: %d", len(effective))
	}
	if effective[0].ID != "system.upstream_insufficient_quota" || effective[0].Source != "account" {
		t.Fatalf("replaced account rule id: %+v", effective[0])
	}
	if effective[1].ID != "account.1" {
		t.Fatalf("stable original index: %+v", effective[1])
	}
	effective, err = effectiveAccountErrorHandlingRules(accountRules, []any{
		map[string]any{"system_rule_id": "system.upstream_insufficient_quota", "action": "delete"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(effective) != 2 || effective[0].ID == "system.upstream_insufficient_quota" {
		t.Fatalf("delete override: %+v", effective)
	}
	// Default order: system first, account rules by priority.
	effective, err = effectiveAccountErrorHandlingRules(accountRules, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(effective) != 3 || effective[0].Source != "system" || effective[1].ID != "account.2" || effective[2].ID != "account.1" {
		t.Fatalf("default merge order: %+v", effective)
	}
	raw, err := json.Marshal(effective[0])
	if err != nil {
		t.Fatal(err)
	}
	var flattened map[string]any
	if err := json.Unmarshal(raw, &flattened); err != nil {
		t.Fatal(err)
	}
	if flattened["name"] != "上游额度不足" || flattened["id"] != "system.upstream_insufficient_quota" || flattened["priority"] != float64(1) {
		t.Fatalf("flattened system rule: %v", flattened)
	}
}
