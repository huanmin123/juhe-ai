package accounts

// w13g 余额草稿完整流补测。（原"归还授权完整流"臂随授权大简化退役删除：
// accounts 自面的 ReturnAuthorizationInstance 读链与
// SetAuthorizationGrantReturner 端口已卸载，归还契约由 authz 域
// my-authorization-sources/{sourceId}/return 路由承载，w13g2 路由测试钉值。）

import (
	"context"
	"strings"
	"testing"
)

func TestW13GPrepareBalanceDraftFullFlow(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	access := AccessScope{ViewerID: adminID}

	// 类型不支持（profile 行删除后由凭据归一化的类型词表拒绝，#B1 剩余①）。
	if _, err := env.store.prepareBalanceDraft(context.Background(), map[string]any{
		"groupId": "grp-default-" + adminID, "providerCode": "gpt", "type": "bogus_type",
		"providerProtocolProfileId": "prof-gpt",
	}, access); err == nil || !strings.Contains(err.Error(), "bogus_type") {
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
