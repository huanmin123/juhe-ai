package authsys

import (
	"database/sql"
	"net/http"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// openTestDB reopens the shared in-memory fixture database so the semantics
// tests can observe raw column values (must_change_password) that the summary
// projection normalizes away.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:authsys-"+strings.ReplaceAll(t.Name(), "/", "-")+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	return db
}

func mustChangeColumn(t *testing.T, db *sql.DB, accountID string) int {
	t.Helper()
	var value int
	if err := db.QueryRow(`SELECT must_change_password FROM system_accounts WHERE id = ?`, accountID).Scan(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

// D-103 regression: an identical management password patch must be a no-op —
// no hash rotation (no new version) and no session revocation (Node
// repository.ts:752-760 verifyPassword gate).
func TestPatchIdenticalPasswordIsNoOp(t *testing.T) {
	deps, _, server := newTestEnv(t)
	seedAccount(t, deps, "root", "root-password", "super_admin")
	seedAccount(t, deps, "plain", "plain-password", "user")
	adminCookie := login(t, server, "root", "root-password")
	plainCookie := login(t, server, "plain", "plain-password")
	plainID := accountIDByUsername(t, deps, "plain")

	_, listPayload := getJSON(t, server, "/__aisys__/api/system-accounts?page=1&pageSize=20", adminCookie)
	item := findItemByUsername(t, listPayload, "plain")
	beforeVersion := item["editVersion"].(string)

	same, samePayload := patchJSON(t, server, "/__aisys__/api/system-accounts/"+plainID,
		`{"expectedUpdatedAt":"`+beforeVersion+`","password":"plain-password"}`, adminCookie)
	if same.StatusCode != http.StatusOK {
		t.Fatalf("identical password patch = %d %v", same.StatusCode, samePayload)
	}

	// No session revocation: the plain user's session still authenticates.
	me, meBody := getJSON(t, server, "/__aisys__/api/auth/me", plainCookie)
	if me.StatusCode != http.StatusOK {
		t.Fatalf("identical password patch must not revoke sessions: %d %v", me.StatusCode, meBody)
	}

	// No hash rotation: no new version was produced.
	_, listPayload = getJSON(t, server, "/__aisys__/api/system-accounts?page=1&pageSize=20", adminCookie)
	item = findItemByUsername(t, listPayload, "plain")
	if item["editVersion"].(string) != beforeVersion {
		t.Fatalf("identical password patch must not rotate the version: %s -> %s", beforeVersion, item["editVersion"])
	}

	// A genuinely new password still rotates and revokes (regression guard).
	rotated, _ := patchJSON(t, server, "/__aisys__/api/system-accounts/"+plainID,
		`{"expectedUpdatedAt":"`+item["editVersion"].(string)+`","password":"rotated-pass"}`, adminCookie)
	if rotated.StatusCode != http.StatusOK {
		t.Fatalf("new password patch = %d", rotated.StatusCode)
	}
	revoked, _ := getJSON(t, server, "/__aisys__/api/auth/me", plainCookie)
	if revoked.StatusCode != http.StatusUnauthorized {
		t.Fatalf("new password patch must revoke sessions: %d", revoked.StatusCode)
	}
}

// D-103 regression: promoting a must-change user to admin re-evaluates the
// must_change_password column with the NEXT role (Node repository.ts:713-725),
// clearing the stale flag instead of trapping the promoted admin in the
// forced-password-change loop.
func TestPatchRolePromotionClearsMustChangePassword(t *testing.T) {
	deps, _, server := newTestEnv(t)
	db := openTestDB(t)
	seedAccount(t, deps, "root", "root-password", "super_admin")
	// store.Create defaults mustChangePassword=true for the user role.
	forced, err := deps.Accounts.Create(nil, CreateInput{
		Username: "newadmin", DisplayName: "NewAdmin_Name", Password: "newadmin-pass", Role: "user",
	})
	if err != nil {
		t.Fatal(err)
	}
	if column := mustChangeColumn(t, db, forced.ID); column != 1 {
		t.Fatalf("fixture must seed must_change_password=1, got %d", column)
	}
	adminCookie := login(t, server, "root", "root-password")

	_, listPayload := getJSON(t, server, "/__aisys__/api/system-accounts?page=1&pageSize=20", adminCookie)
	item := findItemByUsername(t, listPayload, "newadmin")
	promotion, promotionPayload := patchJSON(t, server, "/__aisys__/api/system-accounts/"+forced.ID,
		`{"expectedUpdatedAt":"`+item["editVersion"].(string)+`","role":"admin"}`, adminCookie)
	if promotion.StatusCode != http.StatusOK {
		t.Fatalf("role promotion patch = %d %v", promotion.StatusCode, promotionPayload)
	}
	if column := mustChangeColumn(t, db, forced.ID); column != 0 {
		t.Fatalf("role promotion must clear the must_change_password column, got %d", column)
	}

	// The promoted admin can actually log in and is not asked to change the
	// password (the Node forced-change loop symptom).
	promotedCookie := login(t, server, "newadmin", "newadmin-pass")
	me, meBody := getJSON(t, server, "/__aisys__/api/auth/me", promotedCookie)
	if me.StatusCode != http.StatusOK {
		t.Fatalf("promoted admin login = %d %v", me.StatusCode, meBody)
	}
	if meData, ok := meBody["data"].(map[string]any); !ok || meData["mustChangePassword"] != false {
		t.Fatalf("promoted admin must not be forced to change password: %v", meBody)
	}
}

// D-103 regression: the system-account list keyword filters by exact match OR
// prefix match (Node buildSystemAccountListKeywordFilter), not contains.
func TestListPageKeywordIsExactOrPrefix(t *testing.T) {
	deps, _, server := newTestEnv(t)
	seedAccount(t, deps, "root", "root-password", "super_admin")
	seedAccount(t, deps, "alice", "alice-password", "user")
	item, err := deps.Accounts.Create(nil, CreateInput{
		Username: "alice_backup", DisplayName: "Alice_Two", Password: "backup-pass", Role: "user",
		MustChangePassword: boolPtr(false),
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = item
	adminCookie := login(t, server, "root", "root-password")

	countNames := func(keyword string) int {
		t.Helper()
		_, payload := getJSON(t, server, "/__aisys__/api/system-accounts?page=1&pageSize=50&keyword="+keyword, adminCookie)
		data, _ := payload["data"].(map[string]any)
		items, _ := data["items"].([]any)
		return len(items)
	}

	// Prefix hits: alice matches alice and alice_backup.
	if got := countNames("alice"); got != 2 {
		t.Fatalf("prefix keyword 'alice' = %d items, want 2", got)
	}
	// A contained substring does NOT hit: Node has no contains filter.
	if got := countNames("lice"); got != 0 {
		t.Fatalf("contains keyword 'lice' = %d items, want 0", got)
	}
	// Display-name prefix hits case-insensitively.
	if got := countNames("ALICE_T"); got != 1 {
		t.Fatalf("display prefix keyword 'ALICE_T' = %d items, want 1", got)
	}
	// Exact username hit.
	if got := countNames("alice_backup"); got != 1 {
		t.Fatalf("exact keyword 'alice_backup' = %d items, want 1", got)
	}
}

// D-103 regression: a display-name rename runs the uniqueness precheck inside
// the mutation and reports the concrete '用户名称已存在' 409.
func TestPatchDisplayNameUniquenessPrecheck(t *testing.T) {
	deps, _, server := newTestEnv(t)
	seedAccount(t, deps, "root", "root-password", "super_admin")
	seedAccount(t, deps, "plain", "plain-password", "user")
	adminCookie := login(t, server, "root", "root-password")
	plainID := accountIDByUsername(t, deps, "plain")

	_, listPayload := getJSON(t, server, "/__aisys__/api/system-accounts?page=1&pageSize=20", adminCookie)
	item := findItemByUsername(t, listPayload, "plain")

	// Case-insensitive collision with ROOT_Name.
	conflict, conflictPayload := patchJSON(t, server, "/__aisys__/api/system-accounts/"+plainID,
		`{"expectedUpdatedAt":"`+item["editVersion"].(string)+`","displayName":"root_name"}`, adminCookie)
	if conflict.StatusCode != http.StatusConflict || conflictPayload["message"] != "用户名称已存在" {
		t.Fatalf("display name collision = %d %v, want 409 用户名称已存在", conflict.StatusCode, conflictPayload)
	}

	// Renaming to the account's own current name is a no-op, not a conflict.
	same, samePayload := patchJSON(t, server, "/__aisys__/api/system-accounts/"+plainID,
		`{"expectedUpdatedAt":"`+item["editVersion"].(string)+`","displayName":"PLAIN_Name"}`, adminCookie)
	if same.StatusCode != http.StatusOK {
		t.Fatalf("same-name patch = %d %v", same.StatusCode, samePayload)
	}
}

// D-103 regression: unknown patch fields are rejected instead of silently
// ignored (Node assertKnownInputKeys, repository.ts:642/:1518-1523).
func TestPatchRejectsUnknownFields(t *testing.T) {
	deps, _, server := newTestEnv(t)
	seedAccount(t, deps, "root", "root-password", "super_admin")
	seedAccount(t, deps, "plain", "plain-password", "user")
	adminCookie := login(t, server, "root", "root-password")
	plainID := accountIDByUsername(t, deps, "plain")

	unknown, unknownPayload := patchJSON(t, server, "/__aisys__/api/system-accounts/"+plainID,
		`{"expectedUpdatedAt":"2026-01-01T00:00:00Z","status":"active","hackerField":true}`, adminCookie)
	if unknown.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown field patch = %d %v", unknown.StatusCode, unknownPayload)
	}
	if msg, _ := unknownPayload["message"].(string); !strings.Contains(msg, "系统账户更新参数包含未知字段") || !strings.Contains(msg, "hackerField") {
		t.Fatalf("unknown field message must name the field: %v", unknownPayload)
	}
}

// D-238 regression: whitespace rejections are route-level 400s with the
// verbatim Node messages, and the displayName check still runs after the
// optimistic-concurrency comparison like Node (repository.ts:661 CAS precede
// :678 normalizeRequiredText).
func TestWhitespaceRejectionsAre400(t *testing.T) {
	deps, _, server := newTestEnv(t)
	seedAccount(t, deps, "root", "root-password", "super_admin")
	seedAccount(t, deps, "plain", "plain-password", "user")
	adminCookie := login(t, server, "root", "root-password")
	plainID := accountIDByUsername(t, deps, "plain")

	// create: username/displayName/password whitespace -> 400.
	for _, body := range []struct {
		name string
		json string
		want string
	}{
		{"username", `{"username":"sp ace","displayName":"Name_X","password":"create-pass"}`, "用户账户不能包含空格"},
		{"displayName", `{"username":"spaceuser","displayName":"Name X","password":"create-pass"}`, "用户名称不能包含空格"},
		{"password", `{"username":"spaceuser","displayName":"Name_X","password":"cr eate"}`, "登录密码不能包含空格"},
	} {
		response, payload := postJSON(t, server, "/__aisys__/api/system-accounts", body.json, adminCookie)
		if response.StatusCode != http.StatusBadRequest || payload["message"] != body.want {
			t.Fatalf("create %s whitespace = %d %v, want 400 %s", body.name, response.StatusCode, payload, body.want)
		}
	}

	_, listPayload := getJSON(t, server, "/__aisys__/api/system-accounts?page=1&pageSize=20", adminCookie)
	item := findItemByUsername(t, listPayload, "plain")

	// patch displayName whitespace with a fresh version -> 400.
	patch, patchPayload := patchJSON(t, server, "/__aisys__/api/system-accounts/"+plainID,
		`{"expectedUpdatedAt":"`+item["editVersion"].(string)+`","displayName":"Renamed Name"}`, adminCookie)
	if patch.StatusCode != http.StatusBadRequest || patchPayload["message"] != "用户名称不能包含空格" {
		t.Fatalf("patch displayName whitespace = %d %v, want 400", patch.StatusCode, patchPayload)
	}

	// patch password whitespace -> 400 before the store runs.
	passwordPatch, passwordPatchPayload := patchJSON(t, server, "/__aisys__/api/system-accounts/"+plainID,
		`{"expectedUpdatedAt":"`+item["editVersion"].(string)+`","password":"ro tated"}`, adminCookie)
	if passwordPatch.StatusCode != http.StatusBadRequest || passwordPatchPayload["message"] != "登录密码不能包含空格" {
		t.Fatalf("patch password whitespace = %d %v, want 400", passwordPatch.StatusCode, passwordPatchPayload)
	}

	// A stale version wins with 409 even when the displayName carries
	// whitespace (Node order: CAS precedes the displayName projection).
	stale, stalePayload := patchJSON(t, server, "/__aisys__/api/system-accounts/"+plainID,
		`{"expectedUpdatedAt":"2001-01-01T00:00:00Z","displayName":"Renamed Name"}`, adminCookie)
	if stale.StatusCode != http.StatusConflict || stalePayload["message"] != "系统账户已被其他操作修改，请刷新后重试" {
		t.Fatalf("stale patch with whitespace = %d %v, want 409 conflict first", stale.StatusCode, stalePayload)
	}
}
