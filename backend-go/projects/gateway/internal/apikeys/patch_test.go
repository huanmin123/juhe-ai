package apikeys

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
)

// patchRow reads the persisted mutation columns back for contract checks.
func patchRow(t *testing.T, env *testEnv, keyID string) (name, description, status, routeStrategyID, expiresAt, quotaJSON, scheduleJSON, updatedAt string) {
	t.Helper()
	var rowName, rowDescription, rowExpiresAt, rowQuotaJSON, rowScheduleJSON, rowUpdatedAt []byte
	err := env.db.QueryRow(`SELECT name, COALESCE(description, ''), status, route_strategy_id,
			COALESCE(expires_at, ''), COALESCE(quota_limits_json, ''), COALESCE(availability_schedule_json, ''), updated_at
		FROM api_keys WHERE id = ?`, keyID).
		Scan(&rowName, &rowDescription, &status, &routeStrategyID, &rowExpiresAt, &rowQuotaJSON, &rowScheduleJSON, &rowUpdatedAt)
	if err != nil {
		t.Fatal(err)
	}
	return string(rowName), string(rowDescription), status, routeStrategyID, string(rowExpiresAt), string(rowQuotaJSON), string(rowScheduleJSON), string(rowUpdatedAt)
}

func changedFieldsOf(t *testing.T, payload map[string]any) []string {
	t.Helper()
	data := dataMap(t, payload)
	raw, ok := data["changedFields"].([]any)
	if !ok {
		t.Fatalf("changedFields missing: %v", payload)
	}
	fields := []string{}
	for _, item := range raw {
		fields = append(fields, item.(string))
	}
	return fields
}

func rowPatchOf(t *testing.T, payload map[string]any) map[string]any {
	t.Helper()
	rowPatch, ok := dataMap(t, payload)["rowPatch"].(map[string]any)
	if !ok {
		t.Fatalf("rowPatch missing: %v", payload)
	}
	return rowPatch
}

func hasField(fields []string, want string) bool {
	for _, field := range fields {
		if field == want {
			return true
		}
	}
	return false
}

func fieldChange(t *testing.T, entry authsys.OperationLogEntry, field string) authsys.OperationLogChange {
	t.Helper()
	for _, change := range entry.Changes {
		if change.Field == field {
			return change
		}
	}
	t.Fatalf("change %q missing in %+v", field, entry.Changes)
	return authsys.OperationLogChange{}
}

func updateEntries(sink *recordingSink) []authsys.OperationLogEntry {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	entries := []authsys.OperationLogEntry{}
	for _, entry := range sink.entries {
		if entry.Action == "update" {
			entries = append(entries, entry)
		}
	}
	return entries
}

func seedOtherStrategy(t *testing.T, env *testEnv, ownerID, strategyID, name string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	env.exec(t, `INSERT INTO route_strategies (id, system_account_id, name, mode, status, is_default, created_at, updated_at)
		VALUES (?, ?, ?, 'normal', 'active', 0, ?, ?)`, strategyID, ownerID, name, now, now)
}

// seedGuardKey inserts a raw api_keys row (default/chat guard fixtures) with
// a known RFC3339 revision.
func seedGuardKey(t *testing.T, env *testEnv, id, ownerID, name, purpose string, isDefault int) string {
	t.Helper()
	revision := "2020-01-01T00:00:00.000Z"
	sealed, err := EncryptJSON(testSecret, secretPayload{Key: "sk-" + id})
	if err != nil {
		t.Fatal(err)
	}
	env.exec(t, `INSERT INTO api_keys (id, system_account_id, route_strategy_id, name, key_hash, key_prefix, key_suffix,
		key_secret_encrypted, status, is_default, purpose, created_at, updated_at)
		VALUES (?, ?, 'rs-default', ?, ?, ?, 'suffix', ?, 'active', ?, ?, '2019-01-01T00:00:00.000Z', ?)`,
		id, ownerID, name, "hash-"+id, "sk-"+id[:6], sealed, isDefault, purpose, revision)
	return revision
}

func TestAPIKeyPatchUpdateLifecycleAndChangedFields(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedDefaultRouteStrategy(t, adminID, "rs-default")
	seedOtherStrategy(t, env, adminID, "rs-other", "备用策略")

	code, created := env.do(t, http.MethodPost, "/__aisys__/api/api-keys", `{"name":"alpha"}`)
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, created)
	}
	keyID := dataMap(t, created)["id"].(string)
	revision := dataMap(t, created)["revision"].(string)

	// Full-field patch: every mutable scalar moves at once.
	body := `{"expectedRevision":"` + revision + `","name":"alpha-2","description":"备注","status":"disabled","routeStrategyId":"rs-other","expiresAt":"2030-06-01T00:00:00+08:00"}`
	code, patched := env.do(t, http.MethodPatch, "/__aisys__/api/api-keys/"+keyID, body)
	if code != http.StatusOK {
		t.Fatalf("patch: %d %v", code, patched)
	}
	if _, hasMessage := patched["message"]; hasMessage {
		t.Fatalf("patch response must not carry a message: %v", patched)
	}
	fields := changedFieldsOf(t, patched)
	if len(fields) != 5 || !hasField(fields, "name") || !hasField(fields, "description") ||
		!hasField(fields, "status") || !hasField(fields, "routeStrategyId") || !hasField(fields, "expiresAt") {
		t.Fatalf("changedFields: %v", fields)
	}
	nextRevisionText := dataMap(t, patched)["revision"].(string)
	if nextRevisionText == revision || len(nextRevisionText) != 27 || !strings.HasSuffix(nextRevisionText, "Z") {
		t.Fatalf("revision: %v", nextRevisionText)
	}
	rowPatch := rowPatchOf(t, patched)
	if rowPatch["name"] != "alpha-2" || rowPatch["status"] != "disabled" ||
		rowPatch["routeStrategyId"] != "rs-other" || rowPatch["routeStrategyName"] != "备用策略" ||
		rowPatch["routeStrategyMode"] != "normal" || rowPatch["routeStrategyStatus"] != "active" ||
		rowPatch["expiresAt"] != "2030-05-31T16:00:00.000Z" || rowPatch["description"] != "备注" ||
		rowPatch["revision"] != nextRevisionText {
		t.Fatalf("rowPatch: %v", rowPatch)
	}

	// Persisted row matches the patch contract, including the canonicalized
	// expiry and the bumped revision.
	name, description, status, routeStrategyID, expiresAt, _, _, updatedAt := patchRow(t, env, keyID)
	if name != "alpha-2" || description != "备注" || status != "disabled" ||
		routeStrategyID != "rs-other" || expiresAt != "2030-05-31T16:00:00.000Z" || updatedAt != nextRevisionText {
		t.Fatalf("patched row: %v %v %v %v %v %v", name, description, status, routeStrategyID, expiresAt, updatedAt)
	}

	// Operation log: recorded once with the NEW name in summary/resource and
	// the diffSafeFields changes in label order.
	updates := updateEntries(env.sink)
	if len(updates) != 1 {
		t.Fatalf("update logs: %d", len(updates))
	}
	entry := updates[0]
	if entry.OperationKey != "api_keys.update" || entry.ResourceID != keyID ||
		entry.OperationScopeSystemAccountID != adminID || entry.ResourceName != "alpha-2" ||
		entry.Summary != "更新 API Key：alpha-2" || entry.Mode != "admin" {
		t.Fatalf("update log head: %+v", entry)
	}
	if len(entry.Viewers) != 1 || entry.Viewers[0].SystemAccountID != adminID || entry.Viewers[0].Reason != "resource_owner" {
		t.Fatalf("viewers: %+v", entry.Viewers)
	}
	if len(entry.Changes) != 5 {
		t.Fatalf("changes: %+v", entry.Changes)
	}
	wantChanges := []authsys.OperationLogChange{
		{Field: "name", Label: "名称", Before: "alpha", After: "alpha-2"},
		{Field: "description", Label: "说明", Before: "", After: "备注"},
		{Field: "status", Label: "状态", Before: "active", After: "disabled"},
		{Field: "routeStrategyId", Label: "策略路由", Before: "rs-default", After: "rs-other"},
		{Field: "expiresAt", Label: "过期时间", Before: "", After: "2030-05-31T16:00:00.000Z"},
	}
	for index, want := range wantChanges {
		if entry.Changes[index] != want {
			t.Fatalf("change %d: got %+v want %+v", index, entry.Changes[index], want)
		}
	}
	if !env.inval.has("api_key_updated") {
		t.Fatalf("invalidation reasons: %v", env.inval.reasons)
	}

	// Clearing expiresAt with null flips the column back to NULL.
	revision = nextRevisionText
	code, cleared := env.do(t, http.MethodPatch, "/__aisys__/api/api-keys/"+keyID,
		`{"expectedRevision":"`+revision+`","expiresAt":null}`)
	if code != http.StatusOK {
		t.Fatalf("clear expiry: %d %v", code, cleared)
	}
	if fields := changedFieldsOf(t, cleared); len(fields) != 1 || fields[0] != "expiresAt" {
		t.Fatalf("clear expiry changedFields: %v", fields)
	}
	if rowPatch := rowPatchOf(t, cleared); rowPatch["expiresAt"] != nil {
		t.Fatalf("cleared rowPatch.expiresAt must be null: %v", rowPatch)
	}
	if _, _, _, _, expiresAt, _, _, _ := patchRow(t, env, keyID); expiresAt != "" {
		t.Fatalf("expires_at must be NULL: %v", expiresAt)
	}

	// No-op patch: empty changedFields, unchanged revision, no log, no cache
	// invalidation.
	revision = dataMap(t, cleared)["revision"].(string)
	reasonsBefore := len(env.inval.reasons)
	logsBefore := len(updateEntries(env.sink))
	code, noop := env.do(t, http.MethodPatch, "/__aisys__/api/api-keys/"+keyID,
		`{"expectedRevision":"`+revision+`","description":"备注"}`)
	if code != http.StatusOK {
		t.Fatalf("noop patch: %d %v", code, noop)
	}
	if fields := changedFieldsOf(t, noop); len(fields) != 0 {
		t.Fatalf("noop changedFields: %v", fields)
	}
	if dataMap(t, noop)["revision"] != revision {
		t.Fatalf("noop must keep the revision: %v", noop)
	}
	if rowPatch := rowPatchOf(t, noop); len(rowPatch) != 1 || rowPatch["revision"] != revision {
		t.Fatalf("noop rowPatch: %v", rowPatch)
	}
	if len(updateEntries(env.sink)) != logsBefore || len(env.inval.reasons) != reasonsBefore {
		t.Fatal("no-op patch must not log or invalidate")
	}
}

func TestAPIKeyPatchRevisionConflict(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedDefaultRouteStrategy(t, adminID, "rs-default")

	code, created := env.do(t, http.MethodPost, "/__aisys__/api/api-keys", `{"name":"conflict-key"}`)
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, created)
	}
	keyID := dataMap(t, created)["id"].(string)
	revision := dataMap(t, created)["revision"].(string)

	// Stale revision → 409 with the current revision and no mutation.
	code, conflict := env.do(t, http.MethodPatch, "/__aisys__/api/api-keys/"+keyID,
		`{"expectedRevision":"2019-01-01T00:00:00.000Z","status":"disabled"}`)
	if code != http.StatusConflict || conflict["message"] != "API Key 已被其他操作修改，请刷新后重试" {
		t.Fatalf("conflict: %d %v", code, conflict)
	}
	if conflict["currentRevision"] != revision {
		t.Fatalf("currentRevision: %v", conflict["currentRevision"])
	}
	if _, _, status, _, _, _, _, _ := patchRow(t, env, keyID); status != "active" {
		t.Fatalf("conflict must not mutate: %v", status)
	}

	// After a successful patch the previous revision is stale again.
	code, patched := env.do(t, http.MethodPatch, "/__aisys__/api/api-keys/"+keyID,
		`{"expectedRevision":"`+revision+`","status":"disabled"}`)
	if code != http.StatusOK {
		t.Fatalf("patch: %d %v", code, patched)
	}
	nextRevisionText := dataMap(t, patched)["revision"].(string)
	code, stale := env.do(t, http.MethodPatch, "/__aisys__/api/api-keys/"+keyID,
		`{"expectedRevision":"`+revision+`","status":"active"}`)
	if code != http.StatusConflict || stale["message"] != "API Key 已被其他操作修改，请刷新后重试" || stale["currentRevision"] != nextRevisionText {
		t.Fatalf("stale after success: %d %v", code, stale)
	}
}

func TestAPIKeyPatchNameGuardsAndDuplicate(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedDefaultRouteStrategy(t, adminID, "rs-default")
	seedOtherStrategy(t, env, adminID, "rs-other", "备用策略")

	// Default key: rename forbidden, strategy change forbidden.
	defaultRevision := seedGuardKey(t, env, "key-default", adminID, "默认 API Key", "general", 1)
	code, renamed := env.do(t, http.MethodPatch, "/__aisys__/api/api-keys/key-default",
		`{"expectedRevision":"`+defaultRevision+`","name":"new-name"}`)
	if code != http.StatusBadRequest || renamed["message"] != "默认 API Key 不允许修改名称" {
		t.Fatalf("default rename: %d %v", code, renamed)
	}
	code, restaged := env.do(t, http.MethodPatch, "/__aisys__/api/api-keys/key-default",
		`{"expectedRevision":"`+defaultRevision+`","routeStrategyId":"rs-other"}`)
	if code != http.StatusBadRequest || restaged["message"] != "默认 API Key 不允许更换策略路由" {
		t.Fatalf("default strategy: %d %v", code, restaged)
	}
	// Same-value rename skips the guard entirely (no-op success).
	code, noop := env.do(t, http.MethodPatch, "/__aisys__/api/api-keys/key-default",
		`{"expectedRevision":"`+defaultRevision+`","name":"默认 API Key"}`)
	if code != http.StatusOK || len(changedFieldsOf(t, noop)) != 0 {
		t.Fatalf("default same-name noop: %d %v", code, noop)
	}

	// Chat key: rename forbidden, strategy change allowed.
	chatRevision := seedGuardKey(t, env, "key-chat", adminID, "AI 对话 API Key", "chat", 0)
	code, chatRenamed := env.do(t, http.MethodPatch, "/__aisys__/api/api-keys/key-chat",
		`{"expectedRevision":"`+chatRevision+`","name":"new-chat-name"}`)
	if code != http.StatusBadRequest || chatRenamed["message"] != "AI 对话 API Key 不允许修改名称" {
		t.Fatalf("chat rename: %d %v", code, chatRenamed)
	}
	code, chatRestaged := env.do(t, http.MethodPatch, "/__aisys__/api/api-keys/key-chat",
		`{"expectedRevision":"`+chatRevision+`","routeStrategyId":"rs-other"}`)
	if code != http.StatusOK {
		t.Fatalf("chat strategy: %d %v", code, chatRestaged)
	}
	if fields := changedFieldsOf(t, chatRestaged); len(fields) != 1 || fields[0] != "routeStrategyId" {
		t.Fatalf("chat strategy changedFields: %v", fields)
	}

	// Rename onto an existing owner-scoped name → 409 with the exact message.
	code, first := env.do(t, http.MethodPost, "/__aisys__/api/api-keys", `{"name":"dup-a"}`)
	if code != http.StatusCreated {
		t.Fatalf("dup-a create: %d %v", code, first)
	}
	code, second := env.do(t, http.MethodPost, "/__aisys__/api/api-keys", `{"name":"dup-b"}`)
	if code != http.StatusCreated {
		t.Fatalf("dup-b create: %d %v", code, second)
	}
	dupBID := dataMap(t, second)["id"].(string)
	dupBRevision := dataMap(t, second)["revision"].(string)
	code, duplicate := env.do(t, http.MethodPatch, "/__aisys__/api/api-keys/"+dupBID,
		`{"expectedRevision":"`+dupBRevision+`","name":"dup-a"}`)
	if code != http.StatusConflict || duplicate["message"] != "API Key 名称已存在：dup-a" {
		t.Fatalf("duplicate rename: %d %v", code, duplicate)
	}
}

func TestAPIKeyPatchBodyAndReferenceValidation(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedDefaultRouteStrategy(t, adminID, "rs-default")
	env.exec(t, `INSERT INTO route_strategies (id, system_account_id, name, mode, status, is_default, created_at, updated_at)
		VALUES ('rs-disabled', ?, '停用策略', 'normal', 'disabled', 0, '2020-01-01T00:00:00.000Z', '2020-01-01T00:00:00.000Z')`, adminID)

	code, created := env.do(t, http.MethodPost, "/__aisys__/api/api-keys", `{"name":"validate-key"}`)
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, created)
	}
	keyID := dataMap(t, created)["id"].(string)
	revision := dataMap(t, created)["revision"].(string)

	// English zod default messages contain no CJK, so both the Node server
	// (systemErrorMessageLocalizationMiddleware) and the Go kernel localize
	// them to the 400 status default before the client sees them.
	cases := []struct {
		name    string
		path    string
		body    string
		status  int
		message string
	}{
		{"empty body", "/__aisys__/api/api-keys/" + keyID, `{}`, 400, "请求参数无效"},
		{"blank revision", "/__aisys__/api/api-keys/" + keyID, `{"expectedRevision":""}`, 400, "缺少 API Key revision"},
		{"whitespace revision", "/__aisys__/api/api-keys/" + keyID, `{"expectedRevision":"   "}`, 400, "缺少 API Key revision"},
		{"revision only", "/__aisys__/api/api-keys/" + keyID, `{"expectedRevision":"` + revision + `"}`, 400, "请提供要修改的 API Key 内容"},
		{"unknown key", "/__aisys__/api/api-keys/" + keyID, `{"expectedRevision":"` + revision + `","bogus":1}`, 400, "请求参数无效"},
		{"null name", "/__aisys__/api/api-keys/" + keyID, `{"expectedRevision":"` + revision + `","name":null}`, 400, "请求参数无效"},
		{"number name", "/__aisys__/api/api-keys/" + keyID, `{"expectedRevision":"` + revision + `","name":5}`, 400, "请求参数无效"},
		{"empty name", "/__aisys__/api/api-keys/" + keyID, `{"expectedRevision":"` + revision + `","name":""}`, 400, "请填写 API Key 名称"},
		{"blank name", "/__aisys__/api/api-keys/" + keyID, `{"expectedRevision":"` + revision + `","name":"  "}`, 400, "请填写 API Key 名称"},
		{"bad status", "/__aisys__/api/api-keys/" + keyID, `{"expectedRevision":"` + revision + `","status":"enabled"}`, 400, "请求参数无效"},
		{"null status", "/__aisys__/api/api-keys/" + keyID, `{"expectedRevision":"` + revision + `","status":null}`, 400, "请求参数无效"},
		{"long description", "/__aisys__/api/api-keys/" + keyID, `{"expectedRevision":"` + revision + `","description":"` + strings.Repeat("a", 201) + `"}`, 400, "请求参数无效"},
		{"blank strategy", "/__aisys__/api/api-keys/" + keyID, `{"expectedRevision":"` + revision + `","routeStrategyId":"  "}`, 400, "请选择策略路由"},
		{"null strategy", "/__aisys__/api/api-keys/" + keyID, `{"expectedRevision":"` + revision + `","routeStrategyId":null}`, 400, "请求参数无效"},
		{"quota scalar", "/__aisys__/api/api-keys/" + keyID, `{"expectedRevision":"` + revision + `","quotaLimits":"x"}`, 400, "请求参数无效"},
		{"schedule scalar", "/__aisys__/api/api-keys/" + keyID, `{"expectedRevision":"` + revision + `","availabilitySchedule":5}`, 400, "请求参数无效"},
		{"missing strategy", "/__aisys__/api/api-keys/" + keyID, `{"expectedRevision":"` + revision + `","routeStrategyId":"rs-missing"}`, 400, "API Key 绑定的策略路由不存在或不属于当前用户"},
		{"disabled strategy", "/__aisys__/api/api-keys/" + keyID, `{"expectedRevision":"` + revision + `","routeStrategyId":"rs-disabled"}`, 400, "API Key 只能绑定启用状态的策略路由"},
		{"bad expiry", "/__aisys__/api/api-keys/" + keyID, `{"expectedRevision":"` + revision + `","expiresAt":"yesterday"}`, 400, "API Key 过期时间必须是有效时间字符串"},
		{"unknown key 404", "/__aisys__/api/api-keys/key-nope", `{"expectedRevision":"` + revision + `","status":"disabled"}`, 404, "API Key 不存在"},
		{"blank scope", "/__aisys__/api/api-keys/" + keyID + "?systemAccountId=", `{"expectedRevision":"` + revision + `","status":"disabled"}`, 400, "系统账号 ID 不能为空"},
	}
	for _, testCase := range cases {
		code, payload := env.do(t, http.MethodPatch, testCase.path, testCase.body)
		if code != testCase.status || payload["message"] != testCase.message {
			t.Fatalf("%s: %d %v (want %d %s)", testCase.name, code, payload, testCase.status, testCase.message)
		}
	}

	// expiresAt round trip: set then clear via null.
	code, set := env.do(t, http.MethodPatch, "/__aisys__/api/api-keys/"+keyID,
		`{"expectedRevision":"`+revision+`","expiresAt":"2030-06-01T00:00:00+08:00"}`)
	if code != http.StatusOK || len(changedFieldsOf(t, set)) != 1 {
		t.Fatalf("set expiry: %d %v", code, set)
	}
	revision = dataMap(t, set)["revision"].(string)
	code, cleared := env.do(t, http.MethodPatch, "/__aisys__/api/api-keys/"+keyID,
		`{"expectedRevision":"`+revision+`","expiresAt":null}`)
	if code != http.StatusOK {
		t.Fatalf("clear expiry: %d %v", code, cleared)
	}
	if rowPatch := rowPatchOf(t, cleared); rowPatch["expiresAt"] != nil {
		t.Fatalf("cleared expiry rowPatch: %v", rowPatch)
	}
}

func TestAPIKeyPatchQuotaAndSchedule(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedDefaultRouteStrategy(t, adminID, "rs-default")

	code, created := env.do(t, http.MethodPost, "/__aisys__/api/api-keys",
		`{"name":"quota-patch","quotaLimits":{"hourly":{"enabled":true,"hours":3,"limit":12.5},"daily":{"enabled":true,"limit":40}}}`)
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, created)
	}
	keyID := dataMap(t, created)["id"].(string)
	revision := dataMap(t, created)["revision"].(string)
	if env.count(t, `SELECT COUNT(*) FROM request_quota_hourly_window_scope_bindings WHERE source_type='api_key' AND source_id = ? AND window_hours = 3`, keyID) != 1 {
		t.Fatal("initial hourly binding missing")
	}

	// Dropping the hourly quota rewrites the JSON and removes the binding.
	code, quotaPatched := env.do(t, http.MethodPatch, "/__aisys__/api/api-keys/"+keyID,
		`{"expectedRevision":"`+revision+`","quotaLimits":{"daily":{"enabled":true,"limit":40}}}`)
	if code != http.StatusOK {
		t.Fatalf("quota patch: %d %v", code, quotaPatched)
	}
	if fields := changedFieldsOf(t, quotaPatched); len(fields) != 1 || fields[0] != "quotaLimits" {
		t.Fatalf("quota changedFields: %v", fields)
	}
	if env.count(t, `SELECT COUNT(*) FROM request_quota_hourly_window_scope_bindings WHERE source_id = ?`, keyID) != 0 {
		t.Fatal("hourly binding must be removed")
	}
	if _, _, _, _, _, quotaJSON, _, _ := patchRow(t, env, keyID); quotaJSON != `{"daily":{"enabled":true,"limit":40}}` {
		t.Fatalf("quota json: %v", quotaJSON)
	}
	updates := updateEntries(env.sink)
	if len(updates) != 1 {
		t.Fatalf("update logs: %d", len(updates))
	}
	quotaChange := fieldChange(t, updates[0], "quotaLimits")
	if quotaChange.Label != "额度限制" || !strings.Contains(quotaChange.After, `"daily"`) || strings.Contains(quotaChange.After, `"hourly"`) {
		t.Fatalf("quota change: %+v", quotaChange)
	}
	if !env.inval.has("api_key_quota_updated") {
		t.Fatalf("quota invalidation missing: %v", env.inval.reasons)
	}
	revision = dataMap(t, quotaPatched)["revision"].(string)

	// null clears the limits entirely (rowPatch renders the empty object).
	code, quotaCleared := env.do(t, http.MethodPatch, "/__aisys__/api/api-keys/"+keyID,
		`{"expectedRevision":"`+revision+`","quotaLimits":null}`)
	if code != http.StatusOK {
		t.Fatalf("quota clear: %d %v", code, quotaCleared)
	}
	if rowPatch := rowPatchOf(t, quotaCleared); len(rowPatch["quotaLimits"].(map[string]any)) != 0 {
		t.Fatalf("cleared quotaLimits rowPatch: %v", rowPatch)
	}
	if _, _, _, _, _, quotaJSON, _, _ := patchRow(t, env, keyID); quotaJSON != "" {
		t.Fatalf("quota_limits_json must be NULL: %v", quotaJSON)
	}
	revision = dataMap(t, quotaCleared)["revision"].(string)

	// Always-on schedule: schedule change only, status override keeps active.
	alwaysOn := `{"expectedRevision":"` + revision + `","availabilitySchedule":{"enabled":true,"timezone":"UTC","mode":"allow_windows",
		"windows":[{"daysOfWeek":[1,2,3,4,5,6,7],"start":"00:00","end":"23:59"}]}}`
	code, scheduled := env.do(t, http.MethodPatch, "/__aisys__/api/api-keys/"+keyID, alwaysOn)
	if code != http.StatusOK {
		t.Fatalf("schedule patch: %d %v", code, scheduled)
	}
	fields := changedFieldsOf(t, scheduled)
	if len(fields) != 1 || fields[0] != "availabilitySchedule" {
		t.Fatalf("schedule changedFields: %v", fields)
	}
	if _, _, status, _, _, _, scheduleJSON, _ := patchRow(t, env, keyID); status != "active" || scheduleJSON == "" {
		t.Fatalf("schedule row: %v %v", status, scheduleJSON)
	}
	if nextCheck := env.queryCell(t, `SELECT availability_schedule_next_check_at FROM api_keys WHERE id = ?`, keyID); nextCheck == "" {
		t.Fatal("next check missing")
	}
	scheduleChange := fieldChange(t, updateEntries(env.sink)[len(updateEntries(env.sink))-1], "availabilitySchedule")
	if scheduleChange.Label != "时间计划" || !strings.Contains(scheduleChange.After, "allow_windows") {
		t.Fatalf("schedule change: %+v", scheduleChange)
	}
	revision = dataMap(t, scheduled)["revision"].(string)

	// A schedule that denies `now` (past date range) forces status → disabled;
	// the schedule entry precedes the status entry.
	expired := `{"expectedRevision":"` + revision + `","availabilitySchedule":{"enabled":true,"timezone":"UTC","mode":"allow_windows",
		"windows":[{"daysOfWeek":[1,2,3,4,5,6,7],"start":"00:00","end":"23:59"}],
		"dateRange":{"startDate":"2000-01-01","endDate":"2000-01-02"}}}`
	code, disabled := env.do(t, http.MethodPatch, "/__aisys__/api/api-keys/"+keyID, expired)
	if code != http.StatusOK {
		t.Fatalf("expired schedule patch: %d %v", code, disabled)
	}
	fields = changedFieldsOf(t, disabled)
	if len(fields) != 2 || fields[0] != "availabilitySchedule" || fields[1] != "status" {
		t.Fatalf("expired schedule changedFields: %v", fields)
	}
	if _, _, status, _, _, _, _, _ := patchRow(t, env, keyID); status != "disabled" {
		t.Fatalf("schedule must disable the key: %v", status)
	}
	revision = dataMap(t, disabled)["revision"].(string)

	// Invalid schedule bodies surface the store normalization messages.
	code, sameBounds := env.do(t, http.MethodPatch, "/__aisys__/api/api-keys/"+keyID,
		`{"expectedRevision":"`+revision+`","availabilitySchedule":{"enabled":true,"timezone":"UTC","mode":"allow_windows","windows":[{"daysOfWeek":[1],"start":"01:00","end":"01:00"}]}}`)
	if code != http.StatusBadRequest || sameBounds["message"] != "API Key 时间计划开始时间和停止时间不能相同" {
		t.Fatalf("same bounds: %d %v", code, sameBounds)
	}
}

func TestAPIKeyPatchSelfSurfaceAndPermissions(t *testing.T) {
	env := newTestEnv(t)
	aliceID := env.login(t, "alice", "alice-pass", "user")
	env.seedDefaultRouteStrategy(t, aliceID, "rs-alice")

	code, created := env.do(t, http.MethodPost, "/__aisys__/api/my-api-keys", `{"name":"alice-patch"}`)
	if code != http.StatusCreated {
		t.Fatalf("alice create: %d %v", code, created)
	}
	aliceKeyID := dataMap(t, created)["id"].(string)
	revision := dataMap(t, created)["revision"].(string)

	// Bob cannot patch alice's key through the self surface.
	env.login(t, "bob", "bob-pass", "user")
	code, forbidden := env.do(t, http.MethodPatch, "/__aisys__/api/my-api-keys/"+aliceKeyID,
		`{"expectedRevision":"`+revision+`","name":"bob-was-here"}`)
	if code != http.StatusNotFound || forbidden["message"] != "API Key 不存在" {
		t.Fatalf("bob patch alice key: %d %v", code, forbidden)
	}

	// Alice patches her own key on the self surface; the log mode is self.
	env.login(t, "alice", "alice-pass", "user")
	logsBefore := len(updateEntries(env.sink))
	code, patched := env.do(t, http.MethodPatch, "/__aisys__/api/my-api-keys/"+aliceKeyID,
		`{"expectedRevision":"`+revision+`","name":"alice-patch-2"}`)
	if code != http.StatusOK {
		t.Fatalf("alice patch: %d %v", code, patched)
	}
	if fields := changedFieldsOf(t, patched); len(fields) != 1 || fields[0] != "name" {
		t.Fatalf("alice changedFields: %v", fields)
	}
	updates := updateEntries(env.sink)
	if len(updates) != logsBefore+1 {
		t.Fatalf("self update logs: %d (before %d)", len(updates), logsBefore)
	}
	entry := updates[len(updates)-1]
	if entry.Mode != "self" || entry.Summary != "更新 API Key：alice-patch-2" || entry.OperationScopeSystemAccountID != aliceID {
		t.Fatalf("self update log: %+v", entry)
	}

	// The user role cannot reach the admin-surface PATCH.
	code, denied := env.do(t, http.MethodPatch, "/__aisys__/api/api-keys/"+aliceKeyID,
		`{"expectedRevision":"`+revision+`","name":"nope"}`)
	if code != http.StatusForbidden || denied["message"] != "需要管理员权限" {
		t.Fatalf("admin patch as user: %d %v", code, denied)
	}
}
