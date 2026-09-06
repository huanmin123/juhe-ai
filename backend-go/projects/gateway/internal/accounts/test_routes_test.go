package accounts

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// Contract tests for the manual account test diagnostic family (port of the
// Node account-test dispatch/session/status routes over the shared tables).

// testFamilySchema mirrors the production tables the diagnostic family owns
// or reads (the gateway never creates them; the fixtures stand in for the
// maintenance schema).
var testFamilySchema = []string{
	`CREATE TABLE IF NOT EXISTS account_test_sessions (
		id TEXT PRIMARY KEY,
		request_system_account_id TEXT NOT NULL,
		request_role TEXT NOT NULL,
		request_system_account_filter_id TEXT,
		status TEXT NOT NULL,
		cancel_reason TEXT,
		last_heartbeat_at TEXT NOT NULL,
		cancel_requested_at TEXT,
		finished_at TEXT,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS account_test_tasks (
		id TEXT PRIMARY KEY,
		session_id TEXT,
		account_id TEXT NOT NULL,
		account_name TEXT NOT NULL,
		provider_code TEXT NOT NULL,
		provider_protocol_profile_id TEXT NOT NULL DEFAULT '',
		protocol_code TEXT NOT NULL DEFAULT '',
		protocol_version TEXT NOT NULL DEFAULT '',
		account_type TEXT NOT NULL,
		request_system_account_id TEXT NOT NULL,
		request_role TEXT NOT NULL,
		request_system_account_filter_id TEXT,
		diagnostics TEXT NOT NULL,
		model TEXT,
		test_endpoint_mode TEXT,
		draft_account_encrypted TEXT,
		status TEXT NOT NULL,
		status_message TEXT,
		result_json TEXT,
		error_message TEXT,
		cancel_requested INTEGER NOT NULL DEFAULT 0,
		queued_at TEXT NOT NULL,
		queued_deadline_at TEXT,
		started_at TEXT,
		finished_at TEXT,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS account_test_session_tasks (
		session_id TEXT NOT NULL,
		task_id TEXT NOT NULL,
		created_at TEXT NOT NULL,
		PRIMARY KEY (session_id, task_id)
	)`,
	`CREATE TABLE IF NOT EXISTS provider_model_catalog (
		id TEXT PRIMARY KEY,
		provider_code TEXT NOT NULL,
		model TEXT NOT NULL,
		status TEXT NOT NULL DEFAULT 'active',
		mode TEXT,
		catalog_order INTEGER,
		release_date TEXT,
		shutdown_date TEXT,
		supported_api_protocols_json TEXT,
		default_reasoning_effort TEXT,
		catalog_visible INTEGER NOT NULL DEFAULT 1,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS custom_provider_models (
		id TEXT PRIMARY KEY,
		provider_code TEXT NOT NULL,
		model TEXT NOT NULL,
		scope TEXT NOT NULL,
		system_account_id TEXT,
		status TEXT NOT NULL DEFAULT 'active',
		mode TEXT,
		release_date TEXT,
		shutdown_date TEXT,
		supported_api_protocols_json TEXT,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`,
}

const testCatalogDDL = `INSERT INTO provider_model_catalog
	(id, provider_code, model, status, mode, release_date, supported_api_protocols_json, catalog_visible, created_at, updated_at)
	VALUES ('cat-1', 'gpt', 'gpt-4o-mini', 'active', NULL, '2026-01-01',
	'["chat_completions","responses"]', 1, '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`

type fakeTestEffects struct {
	mu         sync.Mutex
	dispatched [][]string
	canceled   []string
	accept     bool
}

func (f *fakeTestEffects) DispatchAccountTestTasks(_ context.Context, taskIDs []string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dispatched = append(f.dispatched, append([]string{}, taskIDs...))
	return f.accept
}

func (f *fakeTestEffects) DispatchAccountTestCancel(taskID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.canceled = append(f.canceled, taskID)
}

func (f *fakeTestEffects) dispatchedIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []string{}
	for _, batch := range f.dispatched {
		out = append(out, batch...)
	}
	return out
}

func newTestFamilyEnv(t *testing.T, effects *fakeTestEffects) *testEnv {
	t.Helper()
	env := newTestEnv(t)
	for _, statement := range testFamilySchema {
		env.exec(t, statement)
	}
	if effects != nil {
		env.store.SetTestDispatchEffects(effects)
	}
	return env
}

// seedTestCatalog seeds the gpt provider profile fixture plus the model
// catalog row the options/capabilities reads consume.
func (e *testEnv) seedTestCatalog(t *testing.T) {
	t.Helper()
	e.exec(t, strings.Replace(testProviderDDL, "INSERT INTO providers", "INSERT OR IGNORE INTO providers", 1))
	e.exec(t, strings.Replace(testProfileDDL, "INSERT INTO provider_protocol_profiles", "INSERT OR IGNORE INTO provider_protocol_profiles", 1))
	e.exec(t, strings.Replace(testCatalogDDL, "INSERT INTO provider_model_catalog", "INSERT OR IGNORE INTO provider_model_catalog", 1))
}

func seedTestAccount(t *testing.T, env *testEnv, ownerID string) string {
	t.Helper()
	env.seedProviderAndDefaultGroup(t, ownerID)
	env.seedAccount(t, "acc-test-1", ownerID, "测试账户", "active")
	env.seedTestCatalog(t)
	return "acc-test-1"
}

func TestTestSessionLifecycle(t *testing.T) {
	env := newTestFamilyEnv(t, &fakeTestEffects{accept: true})
	admin := env.login(t, "admin", "admin-password", "admin")

	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts/test-sessions", "")
	if code != http.StatusCreated {
		t.Fatalf("create session status = %d payload = %v", code, payload)
	}
	data := payload["data"].(map[string]any)
	sessionID := data["id"].(string)
	if !strings.HasPrefix(sessionID, "acctsess_") {
		t.Fatalf("session id prefix = %s", sessionID)
	}
	if data["status"] != "running" {
		t.Fatalf("session status = %v", data["status"])
	}
	if data["lastHeartbeatAt"] == "" {
		t.Fatalf("missing lastHeartbeatAt: %v", data)
	}

	// Heartbeat refreshes the stamp and keeps the status.
	time.Sleep(5 * time.Millisecond)
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/accounts/test-sessions/"+sessionID+"/heartbeat", "")
	if code != http.StatusOK {
		t.Fatalf("heartbeat status = %d payload = %v", code, payload)
	}

	// Complete with no tasks settles the session.
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/accounts/test-sessions/"+sessionID+"/complete", "")
	if code != http.StatusOK {
		t.Fatalf("complete status = %d payload = %v", code, payload)
	}
	if payload["data"].(map[string]any)["status"] != "completed" {
		t.Fatalf("completed status = %v", payload["data"])
	}

	// Status reads.
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/accounts/test-sessions/"+sessionID, "")
	if code != http.StatusOK || payload["data"].(map[string]any)["status"] != "completed" {
		t.Fatalf("get session = %d %v", code, payload)
	}
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/accounts/test-sessions/"+sessionID+"/tasks", "")
	if code != http.StatusOK {
		t.Fatalf("get session tasks = %d %v", code, payload)
	}
	if tasks, ok := payload["data"].([]any); !ok || len(tasks) != 0 {
		t.Fatalf("expected empty task list, got %v", payload["data"])
	}

	// Unknown ids render the Node 404 copy.
	code, _ = env.do(t, http.MethodGet, "/__aisys__/api/accounts/test-sessions/acctsess_missing", "")
	if code != http.StatusNotFound {
		t.Fatalf("missing session status = %d", code)
	}
	code, _ = env.do(t, http.MethodPost, "/__aisys__/api/accounts/test-sessions/acctsess_missing/heartbeat", "")
	if code != http.StatusNotFound {
		t.Fatalf("missing heartbeat status = %d", code)
	}

	// The self surface exposes the same lifecycle.
	env.login(t, "user", "user-password", "user")
	code, _ = env.do(t, http.MethodPost, "/__aisys__/api/my-accounts/test-sessions", "")
	if code != http.StatusCreated {
		t.Fatalf("my session status = %d", code)
	}

	_ = admin
}

func TestCreateTestTaskDispatchesAndStatusReads(t *testing.T) {
	effects := &fakeTestEffects{accept: true}
	env := newTestFamilyEnv(t, effects)
	admin := env.login(t, "admin", "admin-password", "admin")
	seedTestAccount(t, env, admin)

	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts/acc-test-1/test",
		`{"model":"gpt-4o-mini"}`)
	if code != http.StatusAccepted {
		t.Fatalf("POST test status = %d payload = %v", code, payload)
	}
	task := payload["data"].(map[string]any)
	taskID := task["id"].(string)
	if !strings.HasPrefix(taskID, "accttest_") {
		t.Fatalf("task id = %s", taskID)
	}
	if task["status"] != "queued" || task["message"] != "等待后台测试" {
		t.Fatalf("task = %v", task)
	}
	if task["accountId"] != "acc-test-1" || task["model"] != "gpt-4o-mini" || task["testEndpointMode"] != "chat_json" {
		t.Fatalf("task projection = %v", task)
	}
	if ids := effects.dispatchedIDs(); len(ids) != 1 || ids[0] != taskID {
		t.Fatalf("dispatched = %v want [%s]", ids, taskID)
	}
	if env.count(t, `SELECT COUNT(*) FROM account_test_tasks WHERE id = ? AND status = 'queued'
		AND request_system_account_id = ? AND diagnostics = 'full'`, taskID, admin) != 1 {
		t.Fatalf("task row missing")
	}

	// Status list + detail.
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/accounts/test-tasks?ids="+taskID, "")
	if code != http.StatusOK {
		t.Fatalf("list tasks = %d %v", code, payload)
	}
	list := payload["data"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["id"] != taskID {
		t.Fatalf("task list = %v", payload["data"])
	}
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/accounts/test-tasks/"+taskID, "")
	if code != http.StatusOK || payload["data"].(map[string]any)["status"] != "queued" {
		t.Fatalf("get task = %d %v", code, payload)
	}

	// Cancel: queued tasks settle immediately and signal the worker.
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/accounts/test-tasks/"+taskID+"/cancel", "")
	if code != http.StatusOK {
		t.Fatalf("cancel task = %d %v", code, payload)
	}
	if payload["data"].(map[string]any)["status"] != "canceled" {
		t.Fatalf("canceled task = %v", payload["data"])
	}
	if len(effects.canceled) != 1 || effects.canceled[0] != taskID {
		t.Fatalf("cancel dispatch = %v", effects.canceled)
	}
	if env.queryCell(t, `SELECT status_message FROM account_test_tasks WHERE id = ?`, taskID) != "已停止测试" {
		t.Fatalf("cancel message missing")
	}

	// Unknown task → Node 404 copy.
	code, _ = env.do(t, http.MethodGet, "/__aisys__/api/accounts/test-tasks/accttest_missing", "")
	if code != http.StatusNotFound {
		t.Fatalf("missing task status = %d", code)
	}
}

func TestCreateTestTaskValidationGates(t *testing.T) {
	effects := &fakeTestEffects{accept: true}
	env := newTestFamilyEnv(t, effects)
	admin := env.login(t, "admin", "admin-password", "admin")
	seedTestAccount(t, env, admin)

	// Invalid body (unknown key is strict-rejected).
	code, _ := env.do(t, http.MethodPost, "/__aisys__/api/accounts/acc-test-1/test", `{"prompt":"x","unknown":1}`)
	if code != http.StatusBadRequest {
		t.Fatalf("invalid body status = %d", code)
	}
	// Missing model.
	code, _ = env.do(t, http.MethodPost, "/__aisys__/api/accounts/acc-test-1/test", `{}`)
	if code != http.StatusBadRequest {
		t.Fatalf("missing model status = %d", code)
	}
	// Model outside the provider catalog.
	code, _ = env.do(t, http.MethodPost, "/__aisys__/api/accounts/acc-test-1/test", `{"model":"not-a-model"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("unknown model status = %d", code)
	}
	// Unsupported endpoint mode for the model.
	code, _ = env.do(t, http.MethodPost, "/__aisys__/api/accounts/acc-test-1/test",
		`{"model":"gpt-4o-mini","testEndpointMode":"messages_json"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("bad mode status = %d", code)
	}
	// Unknown account.
	code, _ = env.do(t, http.MethodPost, "/__aisys__/api/accounts/acc-missing/test", `{"model":"gpt-4o-mini"}`)
	if code != http.StatusNotFound {
		t.Fatalf("unknown account status = %d", code)
	}
	if len(effects.dispatchedIDs()) != 0 {
		t.Fatalf("unexpected dispatches: %v", effects.dispatchedIDs())
	}
}

func TestCreateTestTaskWorkerUnavailable(t *testing.T) {
	effects := &fakeTestEffects{accept: false}
	env := newTestFamilyEnv(t, effects)
	admin := env.login(t, "admin", "admin-password", "admin")
	seedTestAccount(t, env, admin)

	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts/acc-test-1/test",
		`{"model":"gpt-4o-mini"}`)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d payload = %v", code, payload)
	}
	if payload["message"] != testWorkerUnavailableMessage {
		t.Fatalf("message = %v", payload["message"])
	}
	if status := env.queryCell(t, `SELECT status FROM account_test_tasks`); status != "failed" {
		t.Fatalf("task status = %s", status)
	}
	if message := env.queryCell(t, `SELECT error_message FROM account_test_tasks`); message != testWorkerUnavailableMessage {
		t.Fatalf("task message = %s", message)
	}
}

func TestTestSessionCancelCancelsLinkedTask(t *testing.T) {
	effects := &fakeTestEffects{accept: true}
	env := newTestFamilyEnv(t, effects)
	admin := env.login(t, "admin", "admin-password", "admin")
	seedTestAccount(t, env, admin)

	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts/test-sessions", "")
	sessionID := payload["data"].(map[string]any)["id"].(string)
	if code != http.StatusCreated {
		t.Fatalf("session status = %d", code)
	}
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/accounts/acc-test-1/test",
		`{"model":"gpt-4o-mini","testSessionId":"`+sessionID+`"}`)
	if code != http.StatusAccepted {
		t.Fatalf("task create = %d %v", code, payload)
	}
	taskID := payload["data"].(map[string]any)["id"].(string)

	// A second task in the same session is rejected (one task per session).
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/accounts/acc-test-1/test",
		`{"model":"gpt-4o-mini","testSessionId":"`+sessionID+`"}`)
	if code != http.StatusBadRequest || payload["message"] != "账户测试会话只能包含一个账户任务" {
		t.Fatalf("second task = %d %v", code, payload)
	}

	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/accounts/test-sessions/"+sessionID+"/cancel", "")
	if code != http.StatusOK {
		t.Fatalf("cancel session = %d %v", code, payload)
	}
	session := payload["data"].(map[string]any)
	if session["status"] != "canceled" || session["message"] != "已停止测试" {
		t.Fatalf("canceled session = %v", session)
	}
	if len(effects.canceled) != 1 || effects.canceled[0] != taskID {
		t.Fatalf("cancel dispatch = %v want [%s]", effects.canceled, taskID)
	}
	if status := env.queryCell(t, `SELECT status FROM account_test_tasks WHERE id = ?`, taskID); status != "canceled" {
		t.Fatalf("linked task status = %s", status)
	}
	// The session status read renders the canceled shape.
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/accounts/test-sessions/"+sessionID, "")
	if code != http.StatusOK || payload["data"].(map[string]any)["status"] != "canceled" {
		t.Fatalf("session read = %d %v", code, payload)
	}
}

func TestTestOptionsEndpoints(t *testing.T) {
	env := newTestFamilyEnv(t, &fakeTestEffects{accept: true})
	admin := env.login(t, "admin", "admin-password", "admin")
	seedTestAccount(t, env, admin)

	code, payload := env.do(t, http.MethodGet, "/__aisys__/api/accounts/acc-test-1/test-options", "")
	if code != http.StatusOK {
		t.Fatalf("test-options = %d %v", code, payload)
	}
	options := payload["data"].([]any)
	if len(options) != 1 {
		t.Fatalf("options = %v", payload["data"])
	}
	option := options[0].(map[string]any)
	if option["id"] != "gpt-4o-mini" || option["name"] != "gpt-4o-mini" {
		t.Fatalf("option = %v", option)
	}
	modes := fmt.Sprint(option["testEndpointModes"])
	if !strings.Contains(modes, "chat_json") || !strings.Contains(modes, "chat_sse") || !strings.Contains(modes, "responses_sse") {
		t.Fatalf("modes = %s", modes)
	}
	if strings.Contains(modes, "messages_json") {
		t.Fatalf("unexpected anthropic mode: %s", modes)
	}

	// Keyword miss keeps only the selected-aware rows: the account health
	// check model rides selectedIds, so it stays visible even when the
	// keyword does not match (Node selected-aware merge).
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/accounts/acc-test-1/test-options?keyword=nomatch", "")
	if code != http.StatusOK {
		t.Fatalf("keyword filter = %d %v", code, payload)
	}
	kept := payload["data"].([]any)
	if len(kept) != 1 || kept[0].(map[string]any)["id"] != "gpt-4o-mini" {
		t.Fatalf("keyword filter kept = %v", payload["data"])
	}

	// Invalid limit.
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/accounts/acc-test-1/test-options?limit=99", "")
	if code != http.StatusBadRequest || payload["message"] != "limit 必须是 1 到 50 的整数" {
		t.Fatalf("limit = %d %v", code, payload)
	}

	// Capabilities per model.
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/accounts/acc-test-1/test-options/models/gpt-4o-mini", "")
	if code != http.StatusOK {
		t.Fatalf("capabilities = %d %v", code, payload)
	}
	capabilities := payload["data"].(map[string]any)
	if capabilities["id"] != "gpt-4o-mini" {
		t.Fatalf("capabilities = %v", capabilities)
	}
	if !strings.Contains(fmt.Sprint(capabilities["testEndpointModes"]), "chat_json") {
		t.Fatalf("capabilities modes = %v", capabilities["testEndpointModes"])
	}

	// Unknown model renders the Node copy.
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/accounts/acc-test-1/test-options/models/nope", "")
	if code != http.StatusBadRequest || payload["message"] != "模型不在当前账户供应商可用目录中：nope" {
		t.Fatalf("unknown model = %d %v", code, payload)
	}

	// Unknown account renders the Node 404 copy.
	code, _ = env.do(t, http.MethodGet, "/__aisys__/api/accounts/acc-missing/test-options", "")
	if code != http.StatusNotFound {
		t.Fatalf("unknown account options = %d", code)
	}
}

func TestTestTaskScopeIsolation(t *testing.T) {
	effects := &fakeTestEffects{accept: true}
	env := newTestFamilyEnv(t, effects)
	admin := env.login(t, "admin", "admin-password", "admin")
	seedTestAccount(t, env, admin)

	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts/acc-test-1/test",
		`{"model":"gpt-4o-mini"}`)
	if code != http.StatusAccepted {
		t.Fatalf("task create = %d %v", code, payload)
	}
	taskID := payload["data"].(map[string]any)["id"].(string)

	// A different user cannot read the admin's task through the self surface
	// (the /accounts admin surface rejects non-admins outright).
	env.do(t, http.MethodGet, "/__aisys__/api/auth/logout", "")
	env.login(t, "other2", "other2-password", "user")
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/accounts/acc-test-1/test",
		`{"model":"gpt-4o-mini"}`)
	if code != http.StatusForbidden {
		t.Fatalf("admin surface as user = %d %v", code, payload)
	}
	code, _ = env.do(t, http.MethodGet, "/__aisys__/api/my-accounts/test-tasks/"+taskID, "")
	if code != http.StatusNotFound {
		t.Fatalf("cross-user read = %d", code)
	}
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/my-accounts/test-tasks?ids="+taskID, "")
	if code != http.StatusOK {
		t.Fatalf("cross-user list = %d", code)
	}
	if tasks, ok := payload["data"].([]any); !ok || len(tasks) != 0 {
		t.Fatalf("cross-user list content = %v", payload["data"])
	}
}

func TestCreateTestTaskDraftSnapshot(t *testing.T) {
	effects := &fakeTestEffects{accept: true}
	env := newTestFamilyEnv(t, effects)
	admin := env.login(t, "admin", "admin-password", "admin")
	seedTestAccount(t, env, admin)

	body := `{"model":"gpt-4o-mini","account":{"providerCode":"gpt","providerProtocolProfileId":"prof-gpt",
		"name":"草稿测试","type":"api_key","credentials":{"api_key":"sk-draft","base_url":"https://api.openai.com/v1"},
		"supportedModels":["gpt-4o-mini"],"healthCheckModel":"gpt-4o-mini",
		"healthCheckEndpointMode":"chat_json","groupId":"grp-default-` + admin + `"}}`
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts/acc-test-1/test", body)
	if code != http.StatusAccepted {
		t.Fatalf("draft test = %d %v", code, payload)
	}
	taskID := payload["data"].(map[string]any)["id"].(string)
	sealed := env.queryCell(t, `SELECT draft_account_encrypted FROM account_test_tasks WHERE id = ?`, taskID)
	if sealed == "" {
		t.Fatalf("draft snapshot not stored")
	}
	var draft TestDraftSnapshot
	if err := DecryptJSON(testSecret, sealed, &draft); err != nil {
		t.Fatalf("draft decrypt = %v", err)
	}
	if draft.StateTargetAccountID == nil || *draft.StateTargetAccountID != "acc-test-1" {
		t.Fatalf("state target = %v", draft.StateTargetAccountID)
	}
	if draft.OwnerSystemAccountID != admin || draft.GroupID != "grp-default-"+admin {
		t.Fatalf("draft owner/group = %s/%s", draft.OwnerSystemAccountID, draft.GroupID)
	}
	if draft.Credentials["api_key"] != "sk-draft" {
		t.Fatalf("draft credentials = %v", draft.Credentials)
	}
	// Draft selection rides the draft health-check model.
	if payload["data"].(map[string]any)["testEndpointMode"] != "chat_json" {
		t.Fatalf("draft mode = %v", payload["data"])
	}

	// Provider mismatch renders the Node copy.
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/accounts/acc-test-1/test",
		`{"account":{"providerCode":"other","providerProtocolProfileId":"prof-gpt","name":"x","type":"api_key",
		"healthCheckModel":"gpt-4o-mini","healthCheckEndpointMode":"chat_json","groupId":"grp-default-`+admin+`"}}`)
	if code != http.StatusBadRequest || payload["message"] != "账户测试草稿与当前账户不一致" {
		t.Fatalf("provider mismatch = %d %v", code, payload)
	}
}

func TestTestRoutesRequireAuth(t *testing.T) {
	env := newTestFamilyEnv(t, &fakeTestEffects{accept: true})
	for _, attempt := range []struct{ method, path string }{
		{http.MethodPost, "/__aisys__/api/accounts/test-sessions"},
		{http.MethodGet, "/__aisys__/api/accounts/test-tasks"},
		{http.MethodGet, "/__aisys__/api/accounts/some-id/test-options"},
	} {
		code, _ := env.do(t, attempt.method, attempt.path, "")
		if code == http.StatusOK || code == http.StatusCreated {
			t.Fatalf("%s %s reachable without auth: %d", attempt.method, attempt.path, code)
		}
	}
}
