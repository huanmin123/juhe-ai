package accounts

import (
	"context"
	"net/http"
	"testing"
)

// BUG-0162 对齐测试：M08 accounts 运行态副作用与字段契约的修复回归。
// 每个 用例对应一个已确认偏差的 Node 语义锚点，对照
// docs/bug/问题-0162-M08账户迁移遗漏端点与运行态副作用.md 的已核实子项。

// newBug0162Env builds the shared SQLite env with the fake runtime-effects
// port wired, so health-check dispatches are observable.
func newBug0162Env(t *testing.T) (*testEnv, *fakeRuntimeEffects) {
	t.Helper()
	env := newTestEnv(t)
	effects := &fakeRuntimeEffects{}
	env.store.SetRuntimeResetEffects(effects)
	return env, effects
}

// seedEngagedLock plants an ENGAGED lock row with an active retry lease.
func (e *testEnv) seedEngagedLock(t *testing.T, accountID string, timeoutSeconds int) {
	t.Helper()
	e.exec(t, `INSERT INTO account_lock_states (account_id, enabled, lock_state,
		lock_death_timeout_seconds, lock_retry_interval_seconds, incident_id, incident_started_at,
		deadline_at, original_status, provenance, next_retry_at_ms, lease_id, lease_until_ms,
		generation, updated_at)
		VALUES (?, 1, 'ENGAGED', ?, 5, 'lock-1', '2026-09-01T00:00:00.000Z', '2026-09-01T00:05:00.000Z',
		'active', 'gateway_failure', 12345, 'lease-abc', 99999, 3, '2026-09-01T00:00:00.000Z')`,
		accountID, timeoutSeconds)
}

// TestBug0162LockEngagedDeadlineRecomputed mirrors Node
// setAccountLockAsync: changing the death timeout on an ENGAGED incident
// recomputes deadline_at from the original incident start and releases the
// retry lease.
func TestBug0162LockEngagedDeadlineRecomputed(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts", createPayload("alpha"))
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, payload)
	}
	id := dataMap(t, payload)["id"].(string)
	env.seedEngagedLock(t, id, 300)

	updated, err := env.store.SetLock(context.Background(), SetLockInput{
		AccountID:              id,
		Enabled:                true,
		LockDeathTimeoutSeconds: func() *int { v := 600; return &v }(),
	}, AccessScope{ViewerID: adminID, IsAdmin: true})
	if err != nil {
		t.Fatal(err)
	}
	if updated == nil || updated.LockState != "ENGAGED" {
		t.Fatalf("state: %v", updated)
	}
	// deadline = incident start + new timeout (2026-09-01T00:00 + 600s).
	if deadline := env.queryCell(t, `SELECT deadline_at FROM account_lock_states WHERE account_id = ?`, id); deadline != "2026-09-01T00:10:00.000Z" {
		t.Fatalf("deadline not recomputed: %q", deadline)
	}
	// A death-config change releases the retry lease.
	if env.count(t, `SELECT COUNT(*) FROM account_lock_states WHERE account_id = ?
		AND next_retry_at_ms IS NULL AND lease_id IS NULL AND lease_until_ms IS NULL`, id) != 1 {
		t.Fatal("lease must be released on timeout change")
	}
}

// TestBug0162LockRetainsLeaseWhenConfigUnchanged mirrors Node: re-submitting
// the same lock config keeps next_retry_at_ms/lease_id/lease_until_ms.
func TestBug0162LockRetainsLeaseWhenConfigUnchanged(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts", createPayload("alpha"))
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, payload)
	}
	id := dataMap(t, payload)["id"].(string)
	env.seedEngagedLock(t, id, 300)

	if _, err := env.store.SetLock(context.Background(), SetLockInput{
		AccountID: id,
		Enabled:   true,
	}, AccessScope{ViewerID: adminID, IsAdmin: true}); err != nil {
		t.Fatal(err)
	}
	if lease := env.queryCell(t, `SELECT lease_id FROM account_lock_states WHERE account_id = ?`, id); lease != "lease-abc" {
		t.Fatalf("lease lost on unchanged config: %q", lease)
	}
	if retry := env.queryCell(t, `SELECT CAST(next_retry_at_ms AS TEXT) FROM account_lock_states WHERE account_id = ?`, id); retry != "12345" {
		t.Fatalf("retry lease lost on unchanged config: %q", retry)
	}
	// deadline stays pinned to the original value when the timeout is unchanged.
	if deadline := env.queryCell(t, `SELECT deadline_at FROM account_lock_states WHERE account_id = ?`, id); deadline != "2026-09-01T00:05:00.000Z" {
		t.Fatalf("deadline drifted: %q", deadline)
	}
}

// TestBug0162DeleteRevokesAuthorizationChain mirrors Node
// revokeAccountAuthorizationsForDeletedResource: the resource-owner account's
// grants, sources, authorizations and quota scope bindings flip to the revoked
// terminal state inside the delete transaction.
func TestBug0162DeleteRevokesAuthorizationChain(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts", createPayload("alpha"))
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, payload)
	}
	id := dataMap(t, payload)["id"].(string)
	now := "2026-09-01T00:00:00.000Z"
	e := func(q string, args ...any) {
		t.Helper()
		env.exec(t, q, args...)
	}
	e(`INSERT INTO resource_authorizations (id, resource_type, resource_id, resource_owner_system_account_id,
		grantee_system_account_id, status, created_by, created_at, updated_at)
		VALUES ('ra-1', 'account', ?, ?, 'sys-grantee', 'active', 'sys-grantee', ?, ?)`, id, adminID, now, now)
	e(`INSERT INTO resource_authorization_grants (id, resource_type, resource_id, resource_owner_system_account_id,
		grantee_type, grantee_system_account_id, status, created_by, created_at, updated_at)
		VALUES ('rg-1', 'account', ?, ?, 'system_account', 'sys-grantee', 'active', 'sys-grantee', ?, ?)`, id, adminID, now, now)
	e(`INSERT INTO resource_authorization_sources (id, authorization_id, source_type, status, created_by, created_at, updated_at)
		VALUES ('rs-1', 'ra-1', 'manual', 'active', 'sys-grantee', ?, ?)`, now, now)
	e(`INSERT INTO request_quota_hourly_window_scope_bindings (system_account_id, scope_type, scope_id,
		source_type, source_id, window_hours, created_at, updated_at)
		VALUES ('sys-grantee', 'account_authorization', 'ra-1', 'resource_authorization_grant', 'rg-1', 1, ?, ?)`, now, now)

	code, _ = env.do(t, http.MethodDelete, "/__aisys__/api/accounts/"+id, "")
	if code != http.StatusNoContent {
		t.Fatalf("delete: %d", code)
	}
	if env.count(t, `SELECT COUNT(*) FROM resource_authorization_grants WHERE id = 'rg-1' AND status = 'revoked'`) != 1 {
		t.Fatal("grant must be revoked")
	}
	if env.count(t, `SELECT COUNT(*) FROM resource_authorization_sources WHERE id = 'rs-1' AND status = 'revoked'
		AND ended_reason = 'account_deleted'`) != 1 {
		t.Fatal("source must be revoked with reason account_deleted")
	}
	if env.count(t, `SELECT COUNT(*) FROM resource_authorizations WHERE id = 'ra-1' AND status = 'revoked'
		AND revoked_reason = 'account_deleted' AND effective_source_type IS NULL`) != 1 {
		t.Fatal("authorization must be revoked and lose its effective source")
	}
	if env.count(t, `SELECT COUNT(*) FROM request_quota_hourly_window_scope_bindings WHERE source_id = 'rg-1'`) != 0 {
		t.Fatal("quota scope bindings must be dropped")
	}
}

// TestBug0162DeleteEnqueuesHealthTombstone mirrors Node logicallyDeleteAccounts:
// health-capable deleted accounts get a fenced tombstone outbox row; other
// providers/types do not.
func TestBug0162DeleteEnqueuesHealthTombstone(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts", createPayload("alpha"))
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, payload)
	}
	id := dataMap(t, payload)["id"].(string)
	env.exec(t, `UPDATE accounts SET config_revision = 3, dispatch_revision = 4 WHERE id = ?`, id)
	// A non-health-capable provider/type pairing (custom provider) rides along.
	env.exec(t, `INSERT INTO accounts (id, system_account_id, provider_code, provider_protocol_profile_id,
		protocol_code, protocol_version, name, type, status, credentials_encrypted, credential_mask,
		health_check_model, created_at, updated_at)
		VALUES ('acc-custom', ?, 'custom-x', 'prof-gpt', 'openai', 'v1', 'custom', 'api_key', 'active',
		'cipher', 'sk-***', 'm', '2026-09-01T00:00:00.000Z', '2026-09-01T00:00:00.000Z')`, adminID)

	code, _ = env.do(t, http.MethodDelete, "/__aisys__/api/accounts/"+id, "")
	if code != http.StatusNoContent {
		t.Fatalf("delete: %d", code)
	}
	if env.count(t, `SELECT COUNT(*) FROM account_health_jobs_input_outbox WHERE account_id = ?
		AND event_kind = 'tombstone' AND reason = 'account_deleted'
		AND config_revision = 3 AND dispatch_revision = 4 AND status = 'pending'`, id) != 1 {
		t.Fatal("health-capable account must receive one tombstone outbox row")
	}
	if env.count(t, `SELECT COUNT(*) FROM account_health_jobs_input_versions WHERE account_id = ?
		AND current_version = 1`, id) != 1 {
		t.Fatal("health input epoch must be reserved")
	}
	if env.count(t, `SELECT COUNT(*) FROM account_health_jobs_input_outbox WHERE account_id = 'acc-custom'`) != 0 {
		t.Fatal("non-health-capable provider must not receive a tombstone")
	}
}

// TestBug0162CreateModelMappingEndpointFamilyEnum mirrors accountModelMappingSchema:
// unknown endpoint families fail the create parse with 400.
func TestBug0162CreateModelMappingEndpointFamilyEnum(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)

	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts", `{"providerCode":"gpt","providerProtocolProfileId":"prof-gpt","name":"bad-source",
		"type":"api_key","credentials":{"api_key":"sk-live-secret-1234567890","base_url":"https://api.openai.com/v1"},
		"supportedModels":["gpt-4o-mini"],"status":"active",
		"modelMappings":[{"sourceModel":"m1","sourceEndpointFamily":"bogus","upstreamModel":"u1","upstreamEndpointFamily":"chat_completions"}]}`)
	if code != http.StatusBadRequest {
		t.Fatalf("bogus source family: %d %v", code, payload)
	}
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/accounts", `{"providerCode":"gpt","providerProtocolProfileId":"prof-gpt","name":"bad-upstream",
		"type":"api_key","credentials":{"api_key":"sk-live-secret-1234567890","base_url":"https://api.openai.com/v1"},
		"supportedModels":["gpt-4o-mini"],"status":"active",
		"modelMappings":[{"sourceModel":"m1","sourceEndpointFamily":"chat_completions","upstreamModel":"u1","upstreamEndpointFamily":"responses_sse"}]}`)
	if code != http.StatusBadRequest {
		t.Fatalf("bogus upstream family: %d %v", code, payload)
	}
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/accounts", `{"providerCode":"gpt","providerProtocolProfileId":"prof-gpt","name":"good-mapping",
		"type":"api_key","credentials":{"api_key":"sk-live-secret-1234567890","base_url":"https://api.openai.com/v1"},
		"supportedModels":["gpt-4o-mini"],"status":"active",
		"modelMappings":[{"sourceModel":"m1","sourceEndpointFamily":"stream_generate_content","upstreamModel":"u1","upstreamEndpointFamily":"generate_content"}]}`)
	if code != http.StatusCreated {
		t.Fatalf("legal families: %d %v", code, payload)
	}
	if env.count(t, `SELECT COUNT(*) FROM account_model_mappings WHERE source_endpoint_family = 'stream_generate_content'
		AND upstream_endpoint_family = 'generate_content'`) != 1 {
		t.Fatal("legal mapping must persist")
	}
}

// TestBug0162CreateTemporaryProbeFlagPersisted mirrors normalizeOptionalBooleanInput:
// an explicit false persists 0; absence persists the default 1.
func TestBug0162CreateTemporaryProbeFlagPersisted(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)

	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts", `{"providerCode":"gpt","providerProtocolProfileId":"prof-gpt","name":"probe-off",
		"type":"api_key","credentials":{"api_key":"sk-live-secret-1234567890","base_url":"https://api.openai.com/v1"},
		"supportedModels":["gpt-4o-mini"],"status":"active","temporaryUnavailableContinuousProbeEnabled":false}`)
	if code != http.StatusCreated {
		t.Fatalf("create probe-off: %d %v", code, payload)
	}
	offID := dataMap(t, payload)["id"].(string)
	if env.count(t, `SELECT COUNT(*) FROM accounts WHERE id = ? AND temporary_unavailable_continuous_probe_enabled = 0`, offID) != 1 {
		t.Fatal("explicit false must persist 0")
	}

	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/accounts", `{"providerCode":"gpt","providerProtocolProfileId":"prof-gpt","name":"probe-default",
		"type":"api_key","credentials":{"api_key":"sk-live-secret-1234567890","base_url":"https://api.openai.com/v1"},
		"supportedModels":["gpt-4o-mini"],"status":"active"}`)
	if code != http.StatusCreated {
		t.Fatalf("create probe-default: %d %v", code, payload)
	}
	defID := dataMap(t, payload)["id"].(string)
	if env.count(t, `SELECT COUNT(*) FROM accounts WHERE id = ? AND temporary_unavailable_continuous_probe_enabled = 1`, defID) != 1 {
		t.Fatal("absent flag must default to enabled")
	}

	code, _ = env.do(t, http.MethodPost, "/__aisys__/api/accounts", `{"providerCode":"gpt","providerProtocolProfileId":"prof-gpt","name":"probe-bad",
		"type":"api_key","credentials":{"api_key":"sk-live-secret-1234567890","base_url":"https://api.openai.com/v1"},
		"supportedModels":["gpt-4o-mini"],"status":"active","temporaryUnavailableContinuousProbeEnabled":"true"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("non-boolean flag: %d", code)
	}
}

// TestBug0162CreateBalanceCapabilityGuards mirrors the Node create route:
// capability validation (API Key only, at least one key, config required) and
// config normalization run before persistence.
func TestBug0162CreateBalanceCapabilityGuards(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)

	oauth := `{"providerCode":"gpt","providerProtocolProfileId":"prof-gpt","name":"oauth-balance",
		"type":"oauth","credentials":{"refresh_token":"rt-secret","access_token":"at","base_url":"https://api.openai.com/v1"},
		"supportedModels":["gpt-4o-mini"],"status":"active","balanceQueryEnabled":true}`
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts", oauth)
	if code != http.StatusBadRequest || payload["message"] != "上游余额查询仅支持 API Key 账户" {
		t.Fatalf("oauth balance: %d %v", code, payload)
	}

	noConfig := `{"providerCode":"gpt","providerProtocolProfileId":"prof-gpt","name":"key-noconfig",
		"type":"api_key","credentials":{"api_key":"sk-live-secret-1234567890","base_url":"https://api.openai.com/v1"},
		"supportedModels":["gpt-4o-mini"],"status":"active","balanceQueryEnabled":true}`
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/accounts", noConfig)
	if code != http.StatusBadRequest || payload["message"] != "开启上游余额查询时必须选择查询类型" {
		t.Fatalf("missing config: %d %v", code, payload)
	}

	badConfig := `{"providerCode":"gpt","providerProtocolProfileId":"prof-gpt","name":"key-badconfig",
		"type":"api_key","credentials":{"api_key":"sk-live-secret-1234567890","base_url":"https://api.openai.com/v1"},
		"supportedModels":["gpt-4o-mini"],"status":"active","balanceQueryConfig":{"adapter":"bogus"}}`
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/accounts", badConfig)
	if code != http.StatusBadRequest || payload["message"] != "余额查询类型无效" {
		t.Fatalf("bad config: %d %v", code, payload)
	}

	goodConfig := `{"providerCode":"gpt","providerProtocolProfileId":"prof-gpt","name":"key-goodconfig",
		"type":"api_key","credentials":{"api_key":"sk-live-secret-1234567890","base_url":"https://api.openai.com/v1"},
		"supportedModels":["gpt-4o-mini"],"status":"active","balanceQueryEnabled":true,
		"balanceQueryConfig":{"adapter":"builtin","preferredBuiltinAdapter":"sub2api"}}`
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/accounts", goodConfig)
	if code != http.StatusCreated {
		t.Fatalf("good config: %d %v", code, payload)
	}
	id := dataMap(t, payload)["id"].(string)
	if env.count(t, `SELECT COUNT(*) FROM accounts WHERE id = ? AND balance_query_enabled = 1
		AND balance_query_config_json LIKE '%sub2api%' AND balance_query_config_json LIKE '%"intervalMinutes":5%'`, id) != 1 {
		t.Fatal("normalized config with default interval must persist")
	}
}

// TestBug0162CreateInitialHealthCheckDispatch mirrors dispatchInitialAccountHealthCheck:
// pending_test always probes; an active single-Key API Key account without
// balance query probes once; multi-Key or balance-enabled actives do not.
func TestBug0162CreateInitialHealthCheckDispatch(t *testing.T) {
	env, effects := newBug0162Env(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)

	// pending_test (no status field).
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts", `{"providerCode":"gpt","providerProtocolProfileId":"prof-gpt","name":"pending",
		"type":"api_key","credentials":{"api_key":"sk-live-secret-1234567890","base_url":"https://api.openai.com/v1"},
		"supportedModels":["gpt-4o-mini"]}`)
	if code != http.StatusCreated {
		t.Fatalf("pending create: %d %v", code, payload)
	}
	pendingID := dataMap(t, payload)["id"].(string)

	// active single-Key without balance query.
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/accounts", createPayload("active-single"))
	if code != http.StatusCreated {
		t.Fatalf("active create: %d %v", code, payload)
	}
	activeID := dataMap(t, payload)["id"].(string)

	// active single-Key WITH balance query: no probe.
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/accounts", `{"providerCode":"gpt","providerProtocolProfileId":"prof-gpt","name":"active-balance",
		"type":"api_key","credentials":{"api_key":"sk-live-secret-1234567890","base_url":"https://api.openai.com/v1"},
		"supportedModels":["gpt-4o-mini"],"status":"active","balanceQueryEnabled":true,
		"balanceQueryConfig":{"adapter":"builtin","preferredBuiltinAdapter":"sub2api"}}`)
	if code != http.StatusCreated {
		t.Fatalf("balance create: %d %v", code, payload)
	}

	// active multi-Key: no probe.
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/accounts", `{"providerCode":"gpt","providerProtocolProfileId":"prof-gpt","name":"active-multi",
		"type":"api_key","credentials":{"api_keys":["sk-live-secret-1234567890","sk-second-key-0987654321"],"base_url":"https://api.openai.com/v1"},
		"supportedModels":["gpt-4o-mini"],"status":"active"}`)
	if code != http.StatusCreated {
		t.Fatalf("multi create: %d %v", code, payload)
	}

	calls := effects.healthCheckDispatches
	if len(calls) != 2 {
		t.Fatalf("expected exactly two activation dispatches, got %v", calls)
	}
	if calls[0][0] != pendingID || calls[0][1] != "activation" {
		t.Fatalf("pending dispatch: %v", calls[0])
	}
	if calls[1][0] != activeID || calls[1][1] != "activation" {
		t.Fatalf("active dispatch: %v", calls[1])
	}
}

// seedHealthProjection plants a stale health projection so the reset contract
// is observable.
func (e *testEnv) seedHealthProjection(t *testing.T, accountID string) {
	t.Helper()
	e.exec(t, `UPDATE accounts SET next_health_check_at = '2026-09-09T00:00:00.000Z',
		last_health_check_at = '2026-09-01T00:00:00.000Z', last_health_success_at = '2026-09-01T00:00:00.000Z',
		health_check_failure_count = 3, health_check_failure_started_at = '2026-09-01T00:00:00.000Z',
		last_health_check_status_code = 500, last_health_check_error_code = 'upstream',
		last_health_check_error_message = 'boom', last_health_check_trace_id = 'trace-1'
		WHERE id = ?`, accountID)
}

// TestBug0162PatchExtendedFieldsAndHealthReset covers the extended PATCH field
// set (modelMappings / proxyProfileId / groupId / balance config / probe flag)
// plus the health-state reset and the post-commit configuration dispatch.
func TestBug0162PatchExtendedFieldsAndHealthReset(t *testing.T) {
	env, effects := newBug0162Env(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	// Alternate bindable group + enabled proxy profile.
	now := "2026-09-01T00:00:00.000Z"
	env.exec(t, `INSERT INTO groups (id, system_account_id, name, provider_code, enabled, is_default, group_type, created_at, updated_at)
		VALUES ('grp-alt', ?, '备用分组', 'gpt', 1, 0, 'personal', ?, ?)`, adminID, now, now)
	env.exec(t, `INSERT INTO proxy_profiles (id, system_account_id, name, type, host, port, enabled, created_at, updated_at)
		VALUES ('proxy-1', ?, '出口代理', 'http', 'proxy.example.internal', 8080, 1, ?, ?)`, adminID, now, now)

	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts", createPayload("alpha"))
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, payload)
	}
	id := dataMap(t, payload)["id"].(string)
	env.seedHealthProjection(t, id)

	code, patched := env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+id, `{
		"expectedConfigRevision": 1,
		"modelMappings": [{"sourceModel":"m1","sourceEndpointFamily":"chat_completions","upstreamModel":"u1","upstreamEndpointFamily":"chat_completions"}],
		"proxyProfileId": "proxy-1",
		"groupId": "grp-alt",
		"temporaryUnavailableContinuousProbeEnabled": false,
		"balanceQueryConfig": {"adapter":"builtin","preferredBuiltinAdapter":"sub2api"}
	}`)
	if code != http.StatusOK {
		t.Fatalf("patch: %d %v", code, patched)
	}
	changed := map[string]bool{}
	for _, item := range dataMap(t, patched)["changedFields"].([]any) {
		changed[item.(string)] = true
	}
	for _, field := range []string{"modelMappings", "proxyProfileId", "groupId", "temporaryUnavailableContinuousProbeEnabled", "balanceQueryConfig"} {
		if !changed[field] {
			t.Fatalf("changedFields missing %s: %v", field, changed)
		}
	}
	if env.count(t, `SELECT COUNT(*) FROM accounts WHERE id = ? AND next_health_check_at IS NULL
		AND last_health_check_at IS NULL AND last_health_success_at IS NULL
		AND health_check_failure_count = 0 AND health_check_failure_started_at IS NULL
		AND last_health_check_status_code IS NULL AND last_health_check_error_code IS NULL
		AND last_health_check_error_message IS NULL AND last_health_check_trace_id IS NULL`, id) != 1 {
		t.Fatal("connection change must reset the health projection")
	}
	if env.count(t, `SELECT COUNT(*) FROM accounts WHERE id = ? AND temporary_unavailable_continuous_probe_enabled = 0
		AND balance_query_config_json LIKE '%sub2api%' AND balance_query_enabled = 0`, id) != 1 {
		t.Fatal("probe flag and balance config must persist")
	}
	if env.count(t, `SELECT COUNT(*) FROM group_accounts WHERE account_id = ? AND group_id = 'grp-alt' AND enabled = 1`, id) != 1 {
		t.Fatal("group binding must switch to grp-alt")
	}
	if env.count(t, `SELECT COUNT(*) FROM account_model_mappings WHERE account_id = ? AND source_model = 'm1'`, id) != 1 {
		t.Fatal("model mapping must persist")
	}
	// The post-commit configuration probe fires once for the health-relevant
	// change set.
	found := false
	for _, call := range effects.healthCheckDispatches {
		if call[0] == id && call[1] == "configuration" {
			found = true
		}
	}
	if !found {
		t.Fatal("configuration probe dispatch missing")
	}

	// The invalidation contract sees the group change as gateway-runtime
	// relevant (Node gatewayRuntimeAffected: groupChanged || ...).
	if env.count(t, `SELECT COUNT(*) FROM accounts WHERE id = ?`, id) != 1 {
		t.Fatal("row must survive")
	}

	// Group binding guards: a foreign group is rejected with Node's copy.
	code, payload = env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+id, `{"expectedConfigRevision": 2, "groupId": "grp-foreign"}`)
	if code != http.StatusBadRequest || payload["message"] != "账户分组无效" {
		t.Fatalf("foreign group: %d %v", code, payload)
	}
	// An unknown proxy fails with the Node copy.
	code, payload = env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+id, `{"expectedConfigRevision": 2, "proxyProfileId": "proxy-missing"}`)
	if code != http.StatusBadRequest || payload["message"] != "代理不存在或已停用，请选择一个已启用的代理" {
		t.Fatalf("missing proxy: %d %v", code, payload)
	}
}

// TestBug0162PatchCredentialsResetHealthProjection mirrors the connection
// change arm: rekeying credentials wipes the persisted health projection and
// schedules a fresh probe.
func TestBug0162PatchCredentialsResetHealthProjection(t *testing.T) {
	env, effects := newBug0162Env(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts", createPayload("alpha"))
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, payload)
	}
	id := dataMap(t, payload)["id"].(string)
	env.seedHealthProjection(t, id)

	code, patched := env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+id, `{
		"expectedConfigRevision": 1,
		"credentials": {"api_key": "sk-rotated-secret-0099887766", "base_url": "https://api.openai.com/v1"}
	}`)
	if code != http.StatusOK {
		t.Fatalf("patch credentials: %d %v", code, patched)
	}
	if env.count(t, `SELECT COUNT(*) FROM accounts WHERE id = ? AND next_health_check_at IS NULL
		AND last_health_check_at IS NULL AND health_check_failure_count = 0
		AND last_health_check_error_code IS NULL AND last_health_check_trace_id IS NULL`, id) != 1 {
		t.Fatal("credential rotation must reset the health projection")
	}
	found := false
	for _, call := range effects.healthCheckDispatches {
		if call[0] == id && call[1] == "configuration" {
			found = true
		}
	}
	if !found {
		t.Fatal("configuration probe dispatch missing")
	}
}

// TestBug0162PatchBalanceCapabilityRejected keeps the PATCH-side capability
// boundary: an OAuth account can never enable the balance query.
func TestBug0162PatchBalanceCapabilityRejected(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	oauthPayload := `{"providerCode":"gpt","providerProtocolProfileId":"prof-gpt","name":"oauth-acct",
		"type":"oauth","credentials":{"refresh_token":"rt-secret","access_token":"at","account_id":"acct-1","base_url":"https://api.openai.com/v1"},
		"supportedModels":["gpt-4o-mini"],"status":"active"}`
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts", oauthPayload)
	if code != http.StatusCreated {
		t.Fatalf("oauth create: %d %v", code, payload)
	}
	id := dataMap(t, payload)["id"].(string)

	code, payload = env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+id,
		`{"expectedConfigRevision": 1, "balanceQueryEnabled": true, "balanceQueryConfig": {"adapter":"builtin","preferredBuiltinAdapter":"sub2api"}}`)
	if code != http.StatusBadRequest || payload["message"] != "上游余额查询仅支持 API Key 账户" {
		t.Fatalf("oauth balance patch: %d %v", code, payload)
	}
}
