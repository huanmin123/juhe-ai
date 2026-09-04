package groups

import (
	"net/http"
	"testing"
	"time"
)

// TestGroupOptionsAndEditBasicLocksIn mirrors the M05 deferral surface:
// GET /options (purpose select/account projections, purpose validation,
// filters and preferDefault ordering) and GET /:id/edit-basic (owner fields
// plus scheduling policy), both on the admin and my- surfaces.
func TestGroupOptionsAndEditBasicLocksIn(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	now := time.Now().UTC().Format(time.RFC3339Nano)
	env.exec(t, `INSERT INTO groups (id, system_account_id, name, provider_code, description, enabled, is_default, group_type, created_at, updated_at)
		VALUES ('grp-opt-1', ?, 'Alpha 选项组', 'openai', NULL, 1, 1, 'personal', ?, ?)`, adminID, now, now)
	env.exec(t, `INSERT INTO groups (id, system_account_id, name, provider_code, description, enabled, is_default, group_type, scheduling_policy_json, created_at, updated_at)
		VALUES ('grp-opt-2', ?, 'Beta 选项组', 'gemini', '第二个', 0, 0, 'high_concurrency', '{"concurrencyLimit":9}', ?, ?)`, adminID, now, now)

	// purpose=select renders {id,name} pairs only.
	code, payload := env.do(t, http.MethodGet, "/__aisys__/api/groups/options?purpose=select", "")
	if code != http.StatusOK {
		t.Fatalf("select options: %d %v", code, payload)
	}
	items := payload["data"].([]any)
	if len(items) != 2 {
		t.Fatalf("expected 2 select options, got %v", payload)
	}
	// Without preferDefault the Node order clause is updated_at DESC, id DESC.
	first := items[0].(map[string]any)
	if first["id"] != "grp-opt-2" || first["name"] != "Beta 选项组" {
		t.Fatalf("default ordering or projection mismatch: %v", first)
	}
	if len(first) != 2 {
		t.Fatalf("select projection must be {id,name} only: %v", first)
	}

	// purpose=account renders the full owner summary.
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/groups/options?purpose=account&providerCode=gemini", "")
	if code != http.StatusOK {
		t.Fatalf("account options: %d %v", code, payload)
	}
	items = payload["data"].([]any)
	if len(items) != 1 {
		t.Fatalf("providerCode filter mismatch: %v", payload)
	}
	summary := items[0].(map[string]any)
	if summary["id"] != "grp-opt-2" || summary["enabled"] != false || summary["isDefault"] != false {
		t.Fatalf("summary fields mismatch: %v", summary)
	}
	if summary["ownerSystemAccountId"] != adminID || summary["ownerSystemAccountName"] != "root_name" {
		t.Fatalf("owner hydration mismatch: %v", summary)
	}
	if summary["groupType"] != "high_concurrency" || summary["accessType"] != "owner" {
		t.Fatalf("type/access mismatch: %v", summary)
	}
	policy := summary["schedulingPolicy"].(map[string]any)
	if policy["concurrencyLimit"].(float64) != 9 {
		t.Fatalf("scheduling policy mismatch: %v", policy)
	}
	permissions := summary["permissions"].(map[string]any)
	if permissions["canUse"] != true || permissions["canEdit"] != true || permissions["canDelete"] != true || permissions["canReturnAuthorization"] != false {
		t.Fatalf("owner permissions mismatch: %v", permissions)
	}
	// Admin surface: includeSystemAccountFields(access) is true, so the
	// systemAccountId/systemAccountName pair is projected too.
	if summary["systemAccountId"] != adminID || summary["systemAccountName"] != "root_name" {
		t.Fatalf("admin owner fields mismatch: %v", summary)
	}

	// An absent purpose normalizes to select (Node groupOptionPurpose); an
	// unknown value → 400 with the Node message.
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/groups/options", "")
	if code != http.StatusOK {
		t.Fatalf("absent purpose must behave as select: %d %v", code, payload)
	}
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/groups/options?purpose=bogus", "")
	if code != http.StatusBadRequest || payload["message"] != "分组选项 purpose 仅支持 select 或 account" {
		t.Fatalf("purpose guard: %d %v", code, payload)
	}

	// ids filter (comma list with dedupe) plus preferDefault ordering.
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/groups/options?purpose=select&ids=grp-opt-2,grp-opt-1,grp-opt-2&preferDefault=1", "")
	if code != http.StatusOK {
		t.Fatalf("ids options: %d %v", code, payload)
	}
	items = payload["data"].([]any)
	if len(items) != 2 {
		t.Fatalf("ids filter mismatch: %v", payload)
	}
	if items[0].(map[string]any)["id"] != "grp-opt-1" {
		t.Fatalf("preferDefault ordering mismatch: %v", items)
	}

	// limit=1 keeps a single row (Node pageClause LIMIT ? OFFSET ?).
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/groups/options?purpose=select&limit=1", "")
	if code != http.StatusOK {
		t.Fatalf("limit options: %d %v", code, payload)
	}
	items = payload["data"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["id"] != "grp-opt-2" {
		t.Fatalf("limit clamp mismatch: %v", payload)
	}

	// edit-basic returns the owner edit projection.
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/groups/grp-opt-2/edit-basic", "")
	if code != http.StatusOK {
		t.Fatalf("edit-basic: %d %v", code, payload)
	}
	edit := payload["data"].(map[string]any)
	if edit["name"] != "Beta 选项组" || edit["providerCode"] != "gemini" || edit["enabled"] != false || edit["groupType"] != "high_concurrency" {
		t.Fatalf("edit projection mismatch: %v", edit)
	}
	if edit["description"] != "第二个" {
		t.Fatalf("edit description mismatch: %v", edit)
	}
	if _, present := edit["schedulingPolicy"]; !present {
		t.Fatalf("edit scheduling policy missing: %v", edit)
	}

	// Another owner's group is invisible on edit-basic for the self surface.
	env.login(t, "alice", "alice-pass", "user")
	code, _ = env.do(t, http.MethodGet, "/__aisys__/api/my-groups/grp-opt-2/edit-basic", "")
	if code != http.StatusNotFound {
		t.Fatalf("cross-owner edit-basic must 404: %d", code)
	}
	code, _ = env.do(t, http.MethodGet, "/__aisys__/api/my-groups/options?purpose=account", "")
	if code != http.StatusOK {
		t.Fatalf("self options must render: %d", code)
	}
	if items := payload["data"]; items == nil {
		t.Fatalf("self options payload mismatch")
	}
	// Switch back to the admin cookie for the remaining assertions (login
	// only: the accounts already exist).
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/auth/login", `{"username":"root","password":"root-pass"}`)
	if code != http.StatusOK {
		t.Fatalf("root re-login failed: %d %v", code, payload)
	}
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/groups/missing-group/edit-basic", "")
	if code != http.StatusNotFound || payload["message"] != "分组不存在" {
		t.Fatalf("missing edit-basic: %d %v", code, payload)
	}
}
