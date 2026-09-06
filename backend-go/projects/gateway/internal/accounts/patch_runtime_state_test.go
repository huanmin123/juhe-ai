package accounts

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"
)

// 编辑路径 PATCH 三项迁移缺口（Node→Go）的配对测试：
//
//	缺口 1  连接类字段变化 → pending_test 状态联动（含 retained active Key
//	        精确化，归档 account-management-patch.repository.ts:556-637、
//	        :1492-1508 + account-api-key-rotation.ts:141-171）；
//	缺口 3  编辑路径 nextRuntimeState 归一化与状态变更分支链（归档
//	        :580-637、:1736-1845、:1938-1949 与
//	        account-runtime-mutation-helpers.ts:37-56）；
//	缺口 2  余额快照清理端口（归档 accounts.routes.ts:355-364 +
//	        account-balance-snapshot-cleanup.service.ts:220-224）。

func seedEnabledProxyProfile(t *testing.T, env *testEnv, ownerID, id string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	env.exec(t, `INSERT INTO proxy_profiles (id, system_account_id, name, type, host, port, enabled, created_at, updated_at)
		VALUES (?, ?, '出口代理', 'http', 'proxy.example.internal', 8080, 1, ?, ?)`, id, ownerID, now, now)
}

// seedPoolAccount inserts an active api_key account carrying a Key pool plus
// one active runtime-state row per key (the shape the gateway leaves behind).
func seedPoolAccount(t *testing.T, env *testEnv, ownerID, id, name string, keys []string) {
	t.Helper()
	pool := make([]any, 0, len(keys))
	for _, key := range keys {
		pool = append(pool, key)
	}
	sealed, err := EncryptJSON(testSecret, Credentials{"api_keys": pool, "base_url": "https://api.openai.com/v1"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	env.exec(t, `INSERT INTO accounts (id, system_account_id, provider_code, provider_protocol_profile_id,
		protocol_code, protocol_version, name, type, status, credentials_encrypted, credential_mask,
		health_check_model, created_at, updated_at)
		VALUES (?, ?, 'gpt', 'prof-gpt', 'openai', 'v1', ?, 'api_key', 'active', ?, 'sk-***', 'gpt-4o-mini', ?, ?)`,
		id, ownerID, name, sealed, now, now)
	for index, key := range keys {
		env.exec(t, `INSERT INTO account_api_key_runtime_states (id, system_account_id, account_id, key_fingerprint, key_index, status, updated_at)
			VALUES (?, ?, ?, ?, ?, 'active', ?)`,
			id+"-key-"+key, ownerID, id, fingerprintAccountAPIKey(testSecret, key), index, now)
	}
}

// TestPatchConnectionChangeArmsPendingTest mirrors the archive linkage
// (:591-593): a proxy switch without explicit status input pushes the active
// account into pending_test and normalizes the runtime state for the
// "configuration saved" wait.
func TestPatchConnectionChangeArmsPendingTest(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	seedEnabledProxyProfile(t, env, adminID, "proxy-1")

	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts", createPayload("conn-pending"))
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, payload)
	}
	id := dataMap(t, payload)["id"].(string)
	env.seedHealthProjection(t, id)

	code, patched := env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+id,
		`{"expectedConfigRevision":1,"proxyProfileId":"proxy-1"}`)
	if code != http.StatusOK {
		t.Fatalf("patch: %d %v", code, patched)
	}
	changed := changedFieldSet(t, patched)
	if !changed["status"] || !changed["schedulable"] || !changed["proxyProfileId"] {
		t.Fatalf("changedFields must carry status + schedulable + proxyProfileId: %v", changed)
	}
	if changed["runtimeState"] {
		t.Fatal("status changed, so the derived runtimeState entry must stay absent")
	}
	if env.count(t, `SELECT COUNT(*) FROM accounts WHERE id = ?
		AND status = 'pending_test' AND schedulable = 0
		AND last_error_code IS NULL AND last_error_message = '账户配置已保存，等待后台检查'
		AND cooldown_until IS NULL AND cooldown_retest_failure_count = 0
		AND cooldown_retest_generation IS NULL AND cooldown_retest_last_at IS NULL
		AND next_health_check_at IS NULL AND last_health_check_at IS NULL`, id) != 1 {
		t.Fatal("connection change must arm pending_test with the normalized wait state")
	}
}

// TestPatchRetainedActiveAPIKeyKeepsConnectionStable mirrors the archive
// retained-active-Key refinement (:565-579): rotating an isolated Key pool
// while one surviving Key stays runtime-active is NOT a connection change —
// the account keeps its active status; dropping every retained active Key is.
func TestPatchRetainedActiveAPIKeyKeepsConnectionStable(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	seedPoolAccount(t, env, adminID, "pool-1", "pool-1", []string{"sk-pool-key-1", "sk-pool-key-2"})

	// Keep k1 active, rotate the pool to [k1, k3]: k1 survives with an active
	// runtime state → not a connection change.
	code, patched := env.do(t, http.MethodPatch, "/__aisys__/api/accounts/pool-1",
		`{"expectedConfigRevision":1,"credentials":{"api_keys":["sk-pool-key-1","sk-pool-key-3"]}}`)
	if code != http.StatusOK {
		t.Fatalf("retain patch: %d %v", code, patched)
	}
	changed := changedFieldSet(t, patched)
	if changed["status"] {
		t.Fatalf("retained active key must keep the account active: %v", changed)
	}
	if env.count(t, `SELECT COUNT(*) FROM accounts WHERE id = 'pool-1' AND status = 'active'`) != 1 {
		t.Fatal("account must stay active while a retained key is active")
	}

	// Rotate to a single key: the pool isolation is off, so the membership
	// rotation is a connection change → pending_test.
	code, patched = env.do(t, http.MethodPatch, "/__aisys__/api/accounts/pool-1",
		`{"expectedConfigRevision":2,"credentials":{"api_keys":["sk-pool-key-4"]}}`)
	if code != http.StatusOK {
		t.Fatalf("drop patch: %d %v", code, patched)
	}
	if !changedFieldSet(t, patched)["status"] {
		t.Fatal("dropping every retained active key must flip the status linkage")
	}
	if env.count(t, `SELECT COUNT(*) FROM accounts WHERE id = 'pool-1'
		AND status = 'pending_test' AND last_error_message = '账户配置已保存，等待后台检查'`) != 1 {
		t.Fatal("unretained pool rotation must arm pending_test")
	}
}

// TestPatchStatusMutationGuards mirrors assertStatusMutationAllowed
// (:1938-1944) plus the manual isolation arm: active → temporary_unavailable
// re-arms the 3-second initial backoff (account-runtime-mutation-helpers.ts:25-34)
// and forces schedulable off.
func TestPatchStatusMutationGuards(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts", createPayload("guard"))
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, payload)
	}
	id := dataMap(t, payload)["id"].(string)

	code, rejected := env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+id,
		`{"expectedConfigRevision":1,"status":"error"}`)
	if code != http.StatusBadRequest ||
		rejected["message"] != "编辑状态只支持可调度、待检查或停用；正常账户可通过人工隔离进入临时不可调用" {
		t.Fatalf("error status must be rejected: %d %v", code, rejected)
	}

	before := time.Now().UTC()
	code, patched := env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+id,
		`{"expectedConfigRevision":1,"status":"temporary_unavailable"}`)
	if code != http.StatusOK {
		t.Fatalf("isolation patch: %d %v", code, patched)
	}
	cooldownUntil := env.queryCell(t, `SELECT cooldown_until FROM accounts WHERE id = ?`, id)
	parsed, err := time.Parse(time.RFC3339Nano, cooldownUntil)
	if err != nil {
		t.Fatalf("cooldown_until not a timestamp: %q %v", cooldownUntil, err)
	}
	delay := parsed.Sub(before)
	if delay < 2*time.Second || delay > 6*time.Second {
		t.Fatalf("manual isolation must arm the 3s initial backoff, got %v", delay)
	}
	if env.count(t, `SELECT COUNT(*) FROM accounts WHERE id = ?
		AND status = 'temporary_unavailable' AND schedulable = 0
		AND last_error_message = '手动设置为临时不可调用'
		AND cooldown_retest_observation_started_at IS NOT NULL
		AND cooldown_retest_generation LIKE 'cooldown:%'
		AND cooldown_retest_failure_count = 0
		AND cooldown_retest_last_at IS NULL`, id) != 1 {
		t.Fatal("manual isolation must restart the bounded recovery observation with a fresh generation")
	}
}

// TestPatchDisabledStatusClearsRuntimeState mirrors the disabled arm
// (:1781-1789): disabled clears the error identity and the retest window,
// and keeps the requested schedulable (Node nextSchedulable keeps the current
// value for disabled).
func TestPatchDisabledStatusClearsRuntimeState(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts", createPayload("to-disabled"))
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, payload)
	}
	id := dataMap(t, payload)["id"].(string)
	env.seedCoolingRetestState(t, id, "active")

	code, patched := env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+id,
		`{"expectedConfigRevision":1,"status":"disabled"}`)
	if code != http.StatusOK {
		t.Fatalf("patch: %d %v", code, patched)
	}
	changed := changedFieldSet(t, patched)
	if !changed["status"] || changed["runtimeState"] || changed["schedulable"] {
		t.Fatalf("disabled chain must surface status only: %v", changed)
	}
	if env.count(t, `SELECT COUNT(*) FROM accounts WHERE id = ?
		AND status = 'disabled' AND schedulable = 1
		AND last_error_code IS NULL AND last_error_message IS NULL AND last_error_trace_id IS NULL
		AND cooldown_until IS NULL AND cooldown_retest_failure_count = 0
		AND cooldown_retest_generation IS NULL AND cooldown_retest_last_at IS NULL
		AND cooldown_retest_last_status_code IS NULL`, id) != 1 {
		t.Fatal("disabled must clear the runtime-state column family")
	}
}

// TestPatchPendingTestStatusMessage mirrors the pending_test arm
// (:1774-1780): an explicit pending_test rewrite stamps the wait message and
// clears the retest window (generation included — no new observation starts).
func TestPatchPendingTestStatusMessage(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts", createPayload("to-pending"))
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, payload)
	}
	id := dataMap(t, payload)["id"].(string)
	env.seedCoolingRetestState(t, id, "active")

	code, patched := env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+id,
		`{"expectedConfigRevision":1,"status":"pending_test"}`)
	if code != http.StatusOK {
		t.Fatalf("patch: %d %v", code, patched)
	}
	if env.count(t, `SELECT COUNT(*) FROM accounts WHERE id = ?
		AND status = 'pending_test' AND schedulable = 0
		AND last_error_message = '账户配置已保存，等待后台检查'
		AND cooldown_retest_generation IS NULL AND cooldown_retest_failure_count = 0`, id) != 1 {
		t.Fatal("pending_test rewrite must normalize the wait state and clear the retest generation")
	}
}

// TestPatchExpiredPackageDisables mirrors the expiredByPackage arm
// (:583-590, :1804-1811): an expiring edit flips the account to disabled with
// the account_expired identity and forces schedulable off.
func TestPatchExpiredPackageDisables(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts", createPayload("expired"))
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, payload)
	}
	id := dataMap(t, payload)["id"].(string)

	code, patched := env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+id,
		`{"expectedConfigRevision":1,"accountExpiresAt":"2020-01-01T00:00:00.000Z"}`)
	if code != http.StatusOK {
		t.Fatalf("patch: %d %v", code, patched)
	}
	if env.count(t, `SELECT COUNT(*) FROM accounts WHERE id = ?
		AND status = 'disabled' AND schedulable = 0
		AND last_error_code = 'account_expired'
		AND last_error_message = '账户套餐已过期，已自动停用'
		AND cooldown_retest_generation IS NULL`, id) != 1 {
		t.Fatal("expired package must auto-disable with the account_expired identity")
	}

	// Enabling an already-expired account is rejected (Node :611-613).
	code, rejected := env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+id,
		`{"expectedConfigRevision":2,"status":"active"}`)
	if code != http.StatusBadRequest || rejected["message"] != "账户套餐已到期，不能启用或参与调度" {
		t.Fatalf("expired enable must be rejected: %d %v", code, rejected)
	}
}

// TestPatchRuntimeStateDerivedChangeEntry mirrors :630-636: a connection
// change that leaves status AND schedulable untouched but normalizes the
// runtime state discloses the normalization as a runtimeState change entry.
func TestPatchRuntimeStateDerivedChangeEntry(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	seedEnabledProxyProfile(t, env, adminID, "proxy-2")
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts", createPayload("derived"))
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, payload)
	}
	id := dataMap(t, payload)["id"].(string)
	env.seedCoolingRetestState(t, id, "pending_test")
	// 该账户已是 pending_test（status/schedulable 都不会再变）。
	env.exec(t, `UPDATE accounts SET schedulable = 1 WHERE id = ?`, id)

	code, patched := env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+id,
		`{"expectedConfigRevision":1,"proxyProfileId":"proxy-2"}`)
	if code != http.StatusOK {
		t.Fatalf("patch: %d %v", code, patched)
	}
	changed := changedFieldSet(t, patched)
	if !changed["proxyProfileId"] || !changed["runtimeState"] {
		t.Fatalf("derived normalization must surface runtimeState: %v", changed)
	}
	if changed["status"] || changed["schedulable"] {
		t.Fatalf("status/schedulable must stay untouched: %v", changed)
	}
	if env.count(t, `SELECT COUNT(*) FROM accounts WHERE id = ?
		AND status = 'pending_test' AND last_error_message = '账户配置已保存，等待后台检查'
		AND cooldown_retest_generation IS NULL`, id) != 1 {
		t.Fatal("connection change on a pending_test account must re-normalize the wait state")
	}
}

// TestPatchRateLimitedRearmUsesSettingsPort mirrors the cooling arm
// (:1790-1802) for rate_limited: same-value status rewrite + connection
// change with a lapsed cooldown re-arms the window from the
// defaultTemporaryUnschedulableMinutes settings port and PRESERVES the retest
// tail (clearRetest is temporary_unavailable-only).
func TestPatchRateLimitedRearmUsesSettingsPort(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	seedEnabledProxyProfile(t, env, adminID, "proxy-3")
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts", createPayload("cooling"))
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, payload)
	}
	id := dataMap(t, payload)["id"].(string)
	env.seedCoolingRetestState(t, id, "rate_limited")
	env.exec(t, `UPDATE accounts SET cooldown_until = NULL WHERE id = ?`, id)
	env.store.SetRuntimeCooldownSettings(fakeCooldownSettings{minutes: 7})

	before := time.Now().UTC()
	code, patched := env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+id,
		`{"expectedConfigRevision":1,"status":"rate_limited","proxyProfileId":"proxy-3"}`)
	if code != http.StatusOK {
		t.Fatalf("patch: %d %v", code, patched)
	}
	cooldownUntil := env.queryCell(t, `SELECT cooldown_until FROM accounts WHERE id = ?`, id)
	parsed, err := time.Parse(time.RFC3339Nano, cooldownUntil)
	if err != nil {
		t.Fatalf("cooldown_until not a timestamp: %q %v", cooldownUntil, err)
	}
	delay := parsed.Sub(before)
	if delay < 6*time.Minute || delay > 8*time.Minute {
		t.Fatalf("rate_limited re-arm must use the settings port (7m), got %v", delay)
	}
	if env.count(t, `SELECT COUNT(*) FROM accounts WHERE id = ?
		AND status = 'rate_limited' AND last_error_message = '手动设置为限流中'
		AND cooldown_retest_observation_started_at IS NOT NULL
		AND cooldown_retest_generation LIKE 'cooldown:%'
		AND cooldown_retest_generation <> 'cooldown:seed-generation'
		AND cooldown_retest_failure_count = 2
		AND cooldown_retest_last_at = '2026-09-01T00:04:00.000Z'
		AND cooldown_retest_last_status_code = 503`, id) != 1 {
		t.Fatal("rate_limited re-arm must keep the retest tail (clearRetest is temporary_unavailable only)")
	}
}

type fakeCooldownSettings struct{ minutes int }

func (f fakeCooldownSettings) DefaultTemporaryUnschedulableMinutes() int { return f.minutes }

type recordingBalanceSnapshotCleaner struct {
	mu       sync.Mutex
	requests []BalanceSnapshotCleanupRequest
}

func (c *recordingBalanceSnapshotCleaner) CleanupBalanceSnapshotAfterSave(request BalanceSnapshotCleanupRequest) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.requests = append(c.requests, request)
}

// TestPatchBalanceSnapshotCleanupPort mirrors the Node route wiring
// (accounts.routes.ts:355-364): a balance-identity-changing save invokes the
// cleanup port with the new revision and the balance_configuration_changed
// reason; unrelated saves stay silent; an unwired (nil) port keeps PATCH
// working.
func TestPatchBalanceSnapshotCleanupPort(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	cleaner := &recordingBalanceSnapshotCleaner{}
	env.store.SetBalanceSnapshotCleaner(cleaner)
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts", createPayload("balance-cleanup"))
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, payload)
	}
	id := dataMap(t, payload)["id"].(string)

	code, patched := env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+id,
		`{"expectedConfigRevision":1,"balanceQueryEnabled":true,"balanceQueryConfig":{"adapter":"builtin","preferredBuiltinAdapter":"sub2api"}}`)
	if code != http.StatusOK {
		t.Fatalf("balance patch: %d %v", code, patched)
	}
	if len(cleaner.requests) != 1 {
		t.Fatalf("balance identity change must invoke the cleanup port once, got %v", cleaner.requests)
	}
	request := cleaner.requests[0]
	if request.AccountID != id || request.ConfigRevision != 2 ||
		request.Reason != BalanceSnapshotCleanupReasonConfigurationChanged {
		t.Fatalf("cleanup request mismatch: %+v", request)
	}

	// Name-only save: balance identity untouched → no cleanup request.
	code, patched = env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+id,
		`{"expectedConfigRevision":2,"name":"balance-cleanup-2"}`)
	if code != http.StatusOK {
		t.Fatalf("name patch: %d %v", code, patched)
	}
	if len(cleaner.requests) != 1 {
		t.Fatalf("name-only save must not invoke the cleanup port: %v", cleaner.requests)
	}

	// An unwired port (a second store sharing the env DB, cleaner left nil)
	// keeps the patch path self-contained.
	silentStore, err := NewStore(env.db, false, testSecret, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := silentStore.Patch(context.Background(), id, PatchInput{
		ExpectedConfigRevision:      3,
		BalanceQueryEnabled:         boolPtr(false),
		BalanceQueryConfigPresent:   true,
		BalanceQueryConfigCanonical: stringPtr(`{"adapter":"builtin"}`),
	}, AccessScope{ViewerID: adminID}); err != nil {
		t.Fatalf("nil-port patch must succeed: %v", err)
	}
	if len(cleaner.requests) != 1 {
		t.Fatalf("unwired store must not touch the wired cleaner: %v", cleaner.requests)
	}
}
