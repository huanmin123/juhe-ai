package accounts

// w13g 归还授权与余额草稿完整流补测。
//
// 不可达登记（w13g）：
// - （无新增；findReturnableDirectGrant 的查询错误臂由 DROP 探针覆盖。）

import (
	"context"
	"strings"
	"testing"
)

type constReturner struct{ status string }

func (c constReturner) Return(context.Context, string, string, string) (string, error) {
	return c.status, nil
}

func TestW13GReturnAuthorizationFlowArms(t *testing.T) {
	env, authzStore, store, ownerID, _ := w13gAdvancedAuthorizedEnv(t)
	tmMember := env.login(t, "ret-member-w13g", "ret-pass", "user")
	env.seedProviderAndDefaultGroup(t, tmMember)
	env.seedTeamMember(t, "team-w13g-tm", ownerID, tmMember)
	instanceID := w13gSeedTrafficInstance(t, env, authzStore, ownerID, tmMember, "acc-w13g-ret-src", "r")
	memberAccess := AccessScope{ViewerID: tmMember}
	store.SetAuthorizationGrantReturner(constReturner{status: "updated"})

	// 空 ID → missing。
	if err := store.ReturnAuthorizationInstance(context.Background(), "  ", memberAccess); err == nil ||
		err != errReturnAuthorizationMissing {
		t.Fatalf("空 ID：%v", err)
	}
	// runtime 授权行缺失 → missing。
	env.exec(t, `UPDATE accounts SET authorization_instance_authorization_id = 'ra-w13g-none'
		WHERE id = ?`, instanceID)
	if err := store.ReturnAuthorizationInstance(context.Background(), instanceID, memberAccess); err == nil || !strings.Contains(err.Error(), "不可归还") {
		t.Fatalf("runtime 缺失应 404 文案：%v", err)
	}
	env.exec(t, `UPDATE accounts SET authorization_instance_authorization_id = (
		SELECT id FROM resource_authorizations WHERE grantee_system_account_id = ? AND resource_id = 'acc-w13g-ret-src')
		WHERE id = ?`, tmMember, instanceID)

	// grant 缺失 → missing。
	if err := store.ReturnAuthorizationInstance(context.Background(), instanceID, memberAccess); err == nil || !strings.Contains(err.Error(), "不可归还") {
		t.Fatalf("grant 缺失应 404 文案：%v", err)
	}

	// 完整链路：直接授权 grant + updated。
	now := "2026-01-01T00:00:00Z"
	env.exec(t, `INSERT INTO resource_authorization_grants (id, resource_type, resource_id,
		resource_owner_system_account_id, grantee_type, grantee_system_account_id, scope, status, created_by, created_at, updated_at)
		VALUES ('rag-w13g-ret', 'account', 'acc-w13g-ret-src', ?, 'system_account', ?, 'use', 'active', ?, ?, ?)`,
		ownerID, tmMember, ownerID, now, now)
	if err := store.ReturnAuthorizationInstance(context.Background(), instanceID, memberAccess); err != nil {
		t.Fatalf("完整归还：%v", err)
	}

	// accounts 表缺失 → 查询错误。
	env.exec(t, `DROP TABLE accounts`)
	if err := store.ReturnAuthorizationInstance(context.Background(), instanceID, memberAccess); err == nil ||
		err == errReturnAuthorizationMissing {
		t.Fatalf("accounts 缺失应传播错误：%v", err)
	}
}

func TestW13GPrepareBalanceDraftFullFlow(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	access := AccessScope{ViewerID: adminID}

	// 停用的协议档案 / 类型不支持。
	if _, err := env.store.prepareBalanceDraft(context.Background(), map[string]any{
		"groupId": "grp-default-" + adminID, "providerCode": "gpt", "type": "bogus_type",
		"providerProtocolProfileId": "prof-gpt",
	}, access); err == nil || !strings.Contains(err.Error(), "不支持账户类型") {
		t.Fatalf("类型不支持：%v", err)
	}
	// oauth 缺 base_url → 回落 profile base_url。
	row, err := env.store.prepareBalanceDraft(context.Background(), map[string]any{
		"groupId": "grp-default-" + adminID, "providerCode": "gpt", "type": "oauth",
		"providerProtocolProfileId": "prof-gpt",
		"credentials":               map[string]any{"access_token": "at", "account_id": "acc"},
		"supportedModels":           []any{"gpt-4o-mini", " ", 1},
		"proxyProfileId":            " proxy-w13g-x ",
	}, access)
	if err != nil || row == nil {
		t.Fatalf("oauth 草稿：%v %v", row, err)
	}
	if row.credentials["base_url"] != "https://api.openai.com/v1" || len(row.supportedModels) != 1 {
		t.Fatalf("草稿回落：%+v", row)
	}
	if row.proxyProfileID == nil {
		t.Fatal("代理指针应存在")
	}
	// api_key 坏凭据传播。
	if _, err := env.store.prepareBalanceDraft(context.Background(), map[string]any{
		"groupId": "grp-default-" + adminID, "providerCode": "gpt", "type": "api_key",
		"providerProtocolProfileId": "prof-gpt",
		"credentials":               map[string]any{"api_key": " ", "base_url": "https://api.example.com"},
	}, access); err == nil || !strings.Contains(err.Error(), "API Key不能为空") {
		t.Fatalf("坏凭据：%v", err)
	}
}
