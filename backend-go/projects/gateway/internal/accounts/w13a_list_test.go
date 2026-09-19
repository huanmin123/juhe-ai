package accounts

// w13a list.go 未覆盖臂补齐：列表/选项查询的过滤器矩阵、代理投影（拥有者
// 启用/停用/缺失、授权实例来源代理）、标签/锁态/用量快照水合、分页边界。

import (
	"context"
	"net/http"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authz"
)

func TestW13AListFilterMatrixAndHydration(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	now := "2026-09-17T00:00:00.000Z"

	// 代理三态：启用 / 停用。
	env.exec(t, `INSERT INTO proxy_profiles (id, system_account_id, name, type, host, port, enabled, test_status, created_at, updated_at)
		VALUES ('proxy-w13a-on', ?, 'w13a-on', 'socks5', 'h', 1080, 1, 'unknown', ?, ?)`, adminID, now, now)
	env.exec(t, `INSERT INTO proxy_profiles (id, system_account_id, name, type, host, port, enabled, test_status, created_at, updated_at)
		VALUES ('proxy-w13a-off', ?, 'w13a-off', 'http', 'h', 8080, 0, 'unknown', ?, ?)`, adminID, now, now)

	env.seedAccount(t, "acc-w13a-l1", adminID, "w13a-l1", "active")
	env.exec(t, `UPDATE accounts SET proxy_profile_id = 'proxy-w13a-on' WHERE id = 'acc-w13a-l1'`)
	env.seedAccount(t, "acc-w13a-l2", adminID, "w13a-l2", "active")
	env.exec(t, `UPDATE accounts SET proxy_profile_id = 'proxy-w13a-off' WHERE id = 'acc-w13a-l2'`)
	env.seedAccount(t, "acc-w13a-l3", adminID, "w13a-l3", "active")
	env.exec(t, `UPDATE accounts SET proxy_profile_id = 'proxy-w13a-gone' WHERE id = 'acc-w13a-l3'`)
	env.seedAccount(t, "acc-w13a-l4", adminID, "w13a-l4", "active")
	env.seedAccount(t, "acc-w13a-l5", adminID, "w13a-l5", "rate_limited")
	env.exec(t, `UPDATE accounts SET cooldown_until = '2030-01-01T00:00:00Z' WHERE id = 'acc-w13a-l5'`)

	// 标签与绑定（水合）。
	env.exec(t, `INSERT INTO account_tags (id, system_account_id, name, created_at, updated_at)
		VALUES ('acctag-w13a-l', ?, 'w13a-ltag', ?, ?)`, adminID, now, now)
	env.exec(t, `INSERT INTO account_tag_bindings (account_id, tag_id, system_account_id, created_at)
		VALUES ('acc-w13a-l1', 'acctag-w13a-l', ?, ?)`, adminID, now)
	// 锁态（水合）。
	env.exec(t, `INSERT INTO account_lock_states (account_id, enabled, lock_state, updated_at)
		VALUES ('acc-w13a-l4', 1, 'LOCKED_IDLE', ?)`, now)
	// OAuth 用量快照（水合）。
	env.exec(t, `INSERT INTO account_usage_snapshots (system_account_id, account_id, kind, snapshot_json, updated_at, created_at)
		VALUES (?, 'acc-w13a-l1', 'relay_balance', '{"balance":1}', ?, ?)`, adminID, now, now)

	// 全量列表：代理投影 + 水合。
	code, payload := env.do(t, http.MethodGet, "/__aisys__/api/accounts", "")
	if code != http.StatusOK {
		t.Fatalf("列表应 200：%d %v", code, payload)
	}
	items := listItems(t, payload)
	if items["acc-w13a-l1"]["proxyProfileId"] != "proxy-w13a-on" || items["acc-w13a-l1"]["proxyProfileName"] != "w13a-on" {
		t.Fatalf("启用代理投影不一致：%v", items["acc-w13a-l1"])
	}
	if items["acc-w13a-l2"]["proxyProfileUnavailable"] != true {
		t.Fatalf("停用代理应标记不可用：%v", items["acc-w13a-l2"])
	}
	if items["acc-w13a-l3"]["proxyProfileUnavailable"] != true || items["acc-w13a-l3"]["proxyProfileErrorMessage"] == nil {
		t.Fatalf("缺失代理应不可用并带管理员文案：%v", items["acc-w13a-l3"])
	}
	if tags, ok := items["acc-w13a-l1"]["tags"].([]any); !ok || len(tags) != 1 {
		t.Fatalf("标签应水合：%v", items["acc-w13a-l1"]["tags"])
	}
	if _, ok := items["acc-w13a-l1"]["usage"]; !ok {
		t.Fatal("用量汇总应存在")
	}

	// 过滤矩阵：status、schedulable=disabled（含冷却）、keyword、type、分组。
	if code, payload := env.do(t, http.MethodGet, "/__aisys__/api/accounts?status=rate_limited", ""); code != http.StatusOK {
		t.Fatalf("status 过滤应 200：%d %v", code, payload)
	}
	if code, payload := env.do(t, http.MethodGet, "/__aisys__/api/accounts?schedulable=cooling", ""); code != http.StatusOK {
		t.Fatalf("cooling 过滤应 200：%d %v", code, payload)
	}
	if code, payload := env.do(t, http.MethodGet, "/__aisys__/api/accounts?keyword=w13a-l1", ""); code != http.StatusOK {
		t.Fatalf("keyword 过滤应 200：%d %v", code, payload)
	}
	if code, payload := env.do(t, http.MethodGet, "/__aisys__/api/accounts?tagIds=acctag-w13a-l", ""); code != http.StatusOK {
		t.Fatalf("tagIds 过滤应 200：%d %v", code, payload)
	}
	if code, payload := env.do(t, http.MethodGet, "/__aisys__/api/accounts?groupId=grp-default-"+adminID, ""); code != http.StatusOK {
		t.Fatalf("groupId 过滤应 200：%d %v", code, payload)
	}
	if code, payload := env.do(t, http.MethodGet, "/__aisys__/api/accounts?type=api_key&providerCode=gpt&providerProtocolProfileId=prof-gpt", ""); code != http.StatusOK {
		t.Fatalf("组合过滤应 200：%d %v", code, payload)
	}
	// 分页边界：pageSize=1 触发 hasMore。
	if code, payload := env.do(t, http.MethodGet, "/__aisys__/api/accounts?page=1&pageSize=1", ""); code != http.StatusOK {
		t.Fatalf("分页应 200：%d %v", code, payload)
	}

	// 选项查询：过滤器矩阵 + limit。
	for _, query := range []string{
		"?keyword=w13a", "?ids=acc-w13a-l1", "?groupId=" + "grp-default-" + adminID,
		"?tagIds=acctag-w13a-l", "?type=api_key", "?providerCode=gpt",
		"?providerProtocolProfileId=prof-gpt", "?status=active", "?schedulable=enabled",
		"?schedulable=disabled", "?limit=3",
	} {
		if code, payload := env.do(t, http.MethodGet, "/__aisys__/api/accounts/options"+query, ""); code != http.StatusOK {
			t.Fatalf("options %s 应 200：%d %v", query, code, payload)
		}
	}
}

func TestW13AListAuthorizedProxyProjection(t *testing.T) {
	env, authzStore := newAuthorizedTestEnv(t)
	ownerID := env.login(t, "w13a-lo", "owner-pass", "user")
	memberID := env.login(t, "w13a-me", "member-pass", "user")
	env.seedAccount(t, "acc-w13a-lsrc", ownerID, "w13a-lsrc", "active")
	now := "2026-09-17T00:00:00.000Z"
	// 来源账户绑定启用代理。
	env.exec(t, `INSERT INTO proxy_profiles (id, system_account_id, name, type, host, port, enabled, test_status, created_at, updated_at)
		VALUES ('proxy-w13a-lsrc', ?, 'w13a-lsrc-proxy', 'socks5', 'h', 1080, 1, 'unknown', ?, ?)`, ownerID, now, now)
	env.exec(t, `UPDATE accounts SET proxy_profile_id = 'proxy-w13a-lsrc' WHERE id = 'acc-w13a-lsrc'`)
	env.seedTeamMember(t, "team-w13a-list", ownerID, memberID)
	if _, err := authzStore.Create(context.Background(), authz.CreateInput{
		ResourceType: "account", ResourceID: "acc-w13a-lsrc",
		GranteeType: "team", GranteeID: "team-w13a-list",
	}, ownerID); err != nil {
		t.Fatal(err)
	}
	runtimeID := env.queryCell(t, `SELECT id FROM resource_authorizations
		WHERE grantee_system_account_id = ? AND resource_id = 'acc-w13a-lsrc'`, memberID)
	env.seedAuthorizationInstance(t, "acc-w13a-linst", memberID, runtimeID, "acc-w13a-lsrc")

	// 成员面读取实例：来源代理已解析且启用 → 投影名称/类型/启用。
	env.login(t, "w13a-me", "member-pass", "user")
	code, payload := env.do(t, http.MethodGet, "/__aisys__/api/my-accounts", "")
	if code != http.StatusOK {
		t.Fatalf("成员列表应 200：%d %v", code, payload)
	}
	items := listItems(t, payload)
	instance, ok := items["acc-w13a-linst"]
	if !ok {
		t.Fatalf("实例应可见：%v", items)
	}
	if instance["proxyProfileName"] != "w13a-lsrc-proxy" || instance["proxyProfileEnabled"] != true {
		t.Fatalf("来源代理投影不一致：%v", instance)
	}

	// 来源代理停用 → 实例侧不可用（无管理员文案）。
	env.exec(t, `UPDATE proxy_profiles SET enabled = 0 WHERE id = 'proxy-w13a-lsrc'`)
	if code, payload := env.do(t, http.MethodGet, "/__aisys__/api/my-accounts", ""); code != http.StatusOK {
		t.Fatalf("成员列表二应 200：%d %v", code, payload)
	} else {
		items = listItems(t, payload)
		if items["acc-w13a-linst"]["proxyProfileUnavailable"] != true {
			t.Fatalf("停用来源代理应不可用：%v", items["acc-w13a-linst"])
		}
		if items["acc-w13a-linst"]["proxyProfileErrorMessage"] != nil {
			t.Fatalf("非管理员不应带修复文案：%v", items["acc-w13a-linst"])
		}
	}
}
