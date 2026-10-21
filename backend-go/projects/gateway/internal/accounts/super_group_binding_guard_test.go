// 超级分组设计 §5：超级分组不得直接持有 group_accounts 行——账户创建与
// PATCH 换组两条写入路径都必须以 400 group_kind_conflict 拒绝。
package accounts

import (
	"fmt"
	"net/http"
	"testing"
	"time"
)

func TestAccountWritesRejectSuperGroupBinding(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	env.exec(t, `INSERT INTO groups (id, system_account_id, name, enabled, is_default, group_type, group_kind, created_at, updated_at)
		VALUES ('grp-super-guard', ?, '超级守卫组', 1, 0, 'personal', 'super', ?, ?)`, adminID, now, now)

	// 创建路径：显式 groupId 指向超级分组。
	createBody := createPayload("super-guard")
	createBody = createBody[:len(createBody)-1] + `,"groupId":"grp-super-guard"}`
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts", createBody)
	if code != http.StatusBadRequest || payload["code"] != "group_kind_conflict" {
		t.Fatalf("create into super group = %d %v, want 400 group_kind_conflict", code, payload)
	}
	if got := env.count(t, `SELECT COUNT(*) FROM group_accounts WHERE group_id = 'grp-super-guard'`); got != 0 {
		t.Fatalf("super group must not hold group_accounts rows, got %d", got)
	}

	// 换组路径：先把账户建在默认分组，再 PATCH 到超级分组。
	code, created := env.do(t, http.MethodPost, "/__aisys__/api/accounts", createPayload("super-guard-2"))
	if code != http.StatusCreated {
		t.Fatalf("create normal account: %d %v", code, created)
	}
	accountID := dataMap(t, created)["id"].(string)
	revision := env.queryCell(t, `SELECT config_revision FROM accounts WHERE id = ?`, accountID)
	code, payload = env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+accountID,
		fmt.Sprintf(`{"expectedConfigRevision":%s,"groupId":"grp-super-guard"}`, revision))
	if code != http.StatusBadRequest || payload["code"] != "group_kind_conflict" {
		t.Fatalf("patch into super group = %d %v, want 400 group_kind_conflict", code, payload)
	}
	if got := env.count(t, `SELECT COUNT(*) FROM group_accounts WHERE account_id = ? AND group_id = 'grp-super-guard'`, accountID); got != 0 {
		t.Fatalf("patch must not bind super group, got %d rows", got)
	}
	if got := env.queryCell(t, `SELECT group_id FROM group_accounts WHERE account_id = ?`, accountID); got != "grp-default-"+adminID {
		t.Fatalf("account group changed to %q, want default group", got)
	}
}
