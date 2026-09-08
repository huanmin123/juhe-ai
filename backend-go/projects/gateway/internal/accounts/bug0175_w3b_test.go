package accounts

import (
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

// BUG-0175 W3-B regressions: D-164 credentialsPatch null=delete, D-165 single
// api_key pool replacement, D-170 POST /accounts/test-draft, D-206 oauthUsage
// read projection.

func TestCredentialsPatchNullDeletesKeys(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)

	// Create with an optional override the edit form later clears.
	code, created := env.do(t, http.MethodPost, "/__aisys__/api/accounts",
		`{"providerCode":"gpt","providerProtocolProfileId":"prof-gpt","name":"patch-null","type":"api_key",
		"credentials":{"api_key":"sk-live-secret-1234567890","base_url":"https://api.openai.com/v1","service_tier_override":"priority"},
		"supportedModels":["gpt-4o-mini"],"status":"active"}`)
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, created)
	}
	id := dataMap(t, created)["id"].(string)
	revision := dataMap(t, created)["configRevision"].(float64)

	if got := env.queryCell(t, `SELECT credentials_encrypted FROM accounts WHERE id = ?`, id); !strings.Contains(got, "v1") {
		t.Fatalf("sealed credentials missing: %v", got)
	}

	// credentialsPatch { service_tier_override: null } deletes the optional
	// field (200, D-164), not a 不能为空 validation failure.
	code, patched := env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+id,
		`{"expectedConfigRevision":`+floatText(revision)+`,"credentialsPatch":{"service_tier_override":null}}`)
	if code != http.StatusOK {
		t.Fatalf("null patch: %d %v", code, patched)
	}
	changed := map[string]bool{}
	for _, field := range dataMap(t, patched)["changedFields"].([]any) {
		changed[field.(string)] = true
	}
	if !changed["credentials"] {
		t.Fatalf("credentials change not reported: %v", patched)
	}
	var unsealed Credentials
	if err := DecryptJSON(testSecret, env.queryCell(t, `SELECT credentials_encrypted FROM accounts WHERE id = ?`, id), &unsealed); err != nil {
		t.Fatal(err)
	}
	if _, exists := unsealed["service_tier_override"]; exists {
		t.Fatalf("service_tier_override must be deleted: %v", unsealed)
	}
	if unsealed["api_key"] != "sk-live-secret-1234567890" || unsealed["base_url"] != "https://api.openai.com/v1" {
		t.Fatalf("null patch must keep other fields: %v", unsealed)
	}
}

func TestCredentialsSingleKeyReplacesPool(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)

	// Create with a three-Key weighted pool (D-165 setup).
	code, created := env.do(t, http.MethodPost, "/__aisys__/api/accounts",
		`{"providerCode":"gpt","providerProtocolProfileId":"prof-gpt","name":"pool","type":"api_key",
		"credentials":{"api_keys":["sk-pool-1","sk-pool-2","sk-pool-3"],"api_key_strategy":"weighted_round_robin",
		"api_key_weights":[1,2,3],"base_url":"https://api.openai.com/v1"},
		"supportedModels":["gpt-4o-mini"],"status":"active"}`)
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, created)
	}
	id := dataMap(t, created)["id"].(string)
	revision := dataMap(t, created)["configRevision"].(float64)

	var pool Credentials
	if err := DecryptJSON(testSecret, env.queryCell(t, `SELECT credentials_encrypted FROM accounts WHERE id = ?`, id), &pool); err != nil {
		t.Fatal(err)
	}
	if _, hasPool := pool["api_keys"]; !hasPool {
		t.Fatalf("pool fixture missing api_keys: %v", pool)
	}

	// Submitting a lone api_key through the legacy credentials input deletes
	// the stale pool fields before normalization (mergeAccountCredentialsForUpdate).
	code, patched := env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+id,
		`{"expectedConfigRevision":`+floatText(revision)+`,"credentials":{"api_key":"sk-single-rotated-9876543210"}}`)
	if code != http.StatusOK {
		t.Fatalf("single key patch: %d %v", code, patched)
	}
	var unsealed Credentials
	if err := DecryptJSON(testSecret, env.queryCell(t, `SELECT credentials_encrypted FROM accounts WHERE id = ?`, id), &unsealed); err != nil {
		t.Fatal(err)
	}
	if unsealed["api_key"] != "sk-single-rotated-9876543210" {
		t.Fatalf("api_key not replaced: %v", unsealed)
	}
	for _, stale := range []string{"api_keys", "api_key_strategy", "api_key_weights"} {
		if _, exists := unsealed[stale]; exists {
			t.Fatalf("stale pool field %s survived: %v", stale, unsealed)
		}
	}
}

func TestAccountTestDraftRoute(t *testing.T) {
	effects := &fakeTestEffects{accept: true}
	env := newTestFamilyEnv(t, effects)
	admin := env.login(t, "admin", "admin-password", "admin")
	seedTestAccount(t, env, admin)

	body := `{"account":{"providerCode":"gpt","providerProtocolProfileId":"prof-gpt",
		"name":"未保存草稿","type":"api_key","credentials":{"api_key":"sk-draft","base_url":"https://api.openai.com/v1"},
		"supportedModels":["gpt-4o-mini"],"healthCheckModel":"gpt-4o-mini",
		"healthCheckEndpointMode":"chat_json","groupId":"grp-default-` + admin + `"}}`
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts/test-draft", body)
	if code != http.StatusAccepted {
		t.Fatalf("test-draft = %d %v", code, payload)
	}
	task := payload["data"].(map[string]any)
	taskID := task["id"].(string)
	if !strings.HasPrefix(taskID, "accttest_") {
		t.Fatalf("task id = %s", taskID)
	}
	// Task shape mirrors the archive createAccountTestTaskAsync output.
	if task["status"] != "queued" || task["accountId"] == "" || task["accountName"] != "未保存草稿" ||
		task["model"] != "gpt-4o-mini" || task["testEndpointMode"] != "chat_json" {
		t.Fatalf("task projection = %v", task)
	}
	if ids := effects.dispatchedIDs(); len(ids) != 1 || ids[0] != taskID {
		t.Fatalf("dispatched = %v want [%s]", ids, taskID)
	}
	// The draft snapshot rides the task row and carries no state target
	// (unsaved drafts are not bound to a saved account).
	sealed := env.queryCell(t, `SELECT draft_account_encrypted FROM account_test_tasks WHERE id = ?`, taskID)
	if sealed == "" {
		t.Fatal("draft snapshot not stored")
	}
	var draft TestDraftSnapshot
	if err := DecryptJSON(testSecret, sealed, &draft); err != nil {
		t.Fatal(err)
	}
	if draft.StateTargetAccountID != nil {
		t.Fatalf("unsaved draft must not carry stateTargetAccountId: %v", draft.StateTargetAccountID)
	}
	if draft.OwnerSystemAccountID != admin || draft.Credentials["api_key"] != "sk-draft" {
		t.Fatalf("draft identity = %s credentials = %v", draft.OwnerSystemAccountID, draft.Credentials)
	}
	if env.count(t, `SELECT COUNT(*) FROM account_test_tasks WHERE id = ? AND diagnostics = 'full'
		AND request_system_account_id = ?`, taskID, admin) != 1 {
		t.Fatal("task row contract violated")
	}

	// The self surface exposes the same route.
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/my-accounts/test-draft", body)
	if code != http.StatusAccepted {
		t.Fatalf("my test-draft = %d %v", code, payload)
	}

	// Missing account input renders the draft-schema rejection.
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/accounts/test-draft", `{"prompt":"hi"}`)
	if code != http.StatusBadRequest || payload["message"] != "账户草稿测试参数无效" {
		t.Fatalf("missing account = %d %v", code, payload)
	}

	// Worker unavailable → 503 with the draft-route copy. The effects port is
	// swapped on the shared env (the sqlite fixture is shared per test name).
	env.store.SetTestDispatchEffects(&fakeTestEffects{accept: false})
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/accounts/test-draft", body)
	if code != http.StatusServiceUnavailable || payload["message"] != testDraftWorkerUnavailableMessage {
		t.Fatalf("worker unavailable = %d %v", code, payload)
	}
}

func TestListOauthUsageProjection(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)

	// gpt oauth account (seeded directly: reads do not normalize credentials).
	sealed, err := EncryptJSON(testSecret, Credentials{"access_token": "oauth-token"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	env.exec(t, `INSERT INTO accounts (id, system_account_id, provider_code, provider_protocol_profile_id,
		protocol_code, protocol_version, name, type, status, credentials_encrypted, credential_mask,
		health_check_model, created_at, updated_at)
		VALUES ('acc-codex', ?, 'gpt', 'prof-gpt', 'openai', 'v1', 'Codex 账户', 'oauth', 'active', ?, 'oa***en', 'gpt-4o-mini', ?, ?)`,
		adminID, sealed, now, now)
	// An api_key account must not carry the projection (type gate).
	env.seedAccount(t, "acc-apikey", adminID, "Key 账户", "active")

	future := time.Now().Add(10 * time.Hour).UTC().Format("2006-01-02T15:04:05.000Z07:00")
	expired := time.Now().Add(-1 * time.Hour).UTC().Format("2006-01-02T15:04:05.000Z07:00")
	updated := time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00")
	env.exec(t, `INSERT INTO account_usage_snapshots (system_account_id, account_id, kind, source, snapshot_json,
		refresh_status, updated_at, created_at) VALUES (?, 'acc-codex', 'openai_codex', 'codex', ?, 'ok', ?, ?)`,
		adminID, `{"codex_5h_used_percent":42.5,"codex_5h_reset_at":"`+future+`","codex_5h_window_minutes":300,
		"codex_7d_used_percent":7.5,"codex_7d_reset_at":"`+expired+`","codex_7d_window_minutes":10080}`, updated, now)

	code, list := env.do(t, http.MethodGet, "/__aisys__/api/accounts", "")
	if code != http.StatusOK {
		t.Fatalf("list: %d %v", code, list)
	}
	items := dataMap(t, list)["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("items = %v", items)
	}
	var codex map[string]any
	var apiKeyItem map[string]any
	for _, entry := range items {
		item := entry.(map[string]any)
		if item["id"] == "acc-codex" {
			codex = item
		}
		if item["id"] == "acc-apikey" {
			apiKeyItem = item
		}
	}
	if codex == nil || apiKeyItem == nil {
		t.Fatalf("fixture items missing: %v", items)
	}
	if _, has := apiKeyItem["oauthUsage"]; has {
		t.Fatal("api_key account must not carry oauthUsage")
	}
	usage, ok := codex["oauthUsage"].(map[string]any)
	if !ok {
		t.Fatalf("oauthUsage missing on codex account: %v", codex)
	}
	if usage["kind"] != "openai_codex" || usage["source"] != "codex" || usage["refreshStatus"] != "ok" {
		t.Fatalf("oauthUsage header = %v", usage)
	}
	fiveHour := usage["fiveHour"].(map[string]any)
	if fiveHour["utilization"] != 42.5 {
		t.Fatalf("fiveHour utilization = %v", fiveHour["utilization"])
	}
	if remaining, ok := fiveHour["remainingSeconds"].(float64); !ok || remaining <= 0 {
		t.Fatalf("fiveHour remainingSeconds = %v", fiveHour["remainingSeconds"])
	}
	if fiveHour["windowMinutes"] != 300.0 {
		t.Fatalf("fiveHour windowMinutes = %v", fiveHour["windowMinutes"])
	}
	// An elapsed reset collapses utilization to 0 with remainingSeconds 0.
	sevenDay := usage["sevenDay"].(map[string]any)
	if sevenDay["utilization"] != 0.0 || sevenDay["remainingSeconds"] != 0.0 {
		t.Fatalf("sevenDay expired window = %v", sevenDay)
	}
}

func floatText(value float64) string {
	// configRevision values are small integers from the JSON envelope.
	return strconv.FormatInt(int64(value), 10)
}
