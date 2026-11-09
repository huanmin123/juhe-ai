package aipublic

// SEC-002 接线回归：update/delete 面通过 resolveOwnedTarget 按 owner 直达
// （可省略 targetUsername），超管守卫必须在该入口同样生效。此测试走真实
// 存储与 HTTP 面（JUHE_AI_TEST_EXTERNAL=1 时运行），钉住 target.go 里
// requirePublicTarget / ensureTargetSystemAccount 之外的第二个守卫接线点。
// 用分组资源而非账户资源，避免依赖模型计价种子。

import (
	"fmt"
	"net/http"
	"testing"
)

func TestPublicTargetRejectsSuperAdminOnUpdate(t *testing.T) {
	env := newAIPublicEnv(t)
	scopes := []string{
		"juhe_ai_public:group_add:write", "juhe_ai_public:group_update:write",
	}
	token := env.seedSource("sec2_ext", "sec2_tok", "juis_token_sec2superadmin0001", "active", "active", scopes, "[]", "", "")

	status, payload, _ := env.doAuth(http.MethodPost, "/__aipublic__/group/add",
		`{"targetUsername":"sec2_promoted","name":"sec2组"}`, token)
	if status != http.StatusCreated {
		t.Fatalf("group add: %d %v", status, payload)
	}
	groupID := payload["data"].(map[string]any)["group"].(map[string]any)["id"].(string)

	// 提升为 super_admin：模拟生产内置超管形态（role=super_admin）。
	if _, err := env.db.Exec(`UPDATE system_accounts SET role = 'super_admin' WHERE username = $1`, "sec2_promoted"); err != nil {
		t.Fatalf("promote target: %v", err)
	}

	update := fmt.Sprintf(`{"groupId":%q,"targetUsername":"sec2_promoted","name":"sec2改名"}`, groupID)
	status, payload, _ = env.doAuth(http.MethodPost, "/__aipublic__/group/update", update, token)
	// update 面把任何 target 解析失败统一渲染为 404（不泄露超管资源是否
	// 存在）；delete 面沿用各接口既有幂等 not_found 契约。断言资源未被改动。
	if status != http.StatusNotFound {
		t.Fatalf("update super admin target = %d %v, want 404 not found", status, payload)
	}
	var name string
	if err := env.db.QueryRow(`SELECT name FROM groups WHERE id = $1`, groupID).Scan(&name); err != nil {
		t.Fatalf("read group: %v", err)
	}
	if name != "sec2组" {
		t.Fatalf("super admin target group must stay unchanged, got %q", name)
	}
}
