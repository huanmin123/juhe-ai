package accounts

// 账户列表 testKind 的服务端权威来源（T? 列表 testKind 与手工测试上下文同源）：
// 列表不再由前端按凭据/健康检查形态自行推导，而是按有效连接
// account_endpoint_modes（health_check_connection_id 指向的 enabled 连接优先，
// 否则 enabled 按 id ASC 首个）派生；授权实例按来源账户的连接派生。

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts/accountstest"
	capabilitydomain "github.com/huanminabc/juhe-ai/backend-go-platform/capabilitydomain"
)

func TestListItemTestKindDerivation(t *testing.T) {
	cases := []struct {
		name string
		row  listRow
		want string
	}{
		{
			name: "chat 连接生成形态 → model",
			row:  listRow{capabilityDomain: "chat", endpointModes: []string{"chat_json"}},
			want: capabilitydomain.TestKindModel,
		},
		{
			name: "chat 连接仅输入形态 → connection",
			row:  listRow{capabilityDomain: "chat", endpointModes: []string{"embeddings_json"}, healthCheckEndpointMode: "chat_sse"},
			want: capabilitydomain.TestKindConnection,
		},
		{
			name: "chat 连接形态优先于健康检查标量",
			row:  listRow{capabilityDomain: "chat", endpointModes: []string{"chat_json"}, healthCheckEndpointMode: "embeddings_json"},
			want: capabilitydomain.TestKindModel,
		},
		{
			name: "chat 无连接回落健康检查标量 → model",
			row:  listRow{capabilityDomain: "chat", healthCheckEndpointMode: "chat_sse"},
			want: capabilitydomain.TestKindModel,
		},
		{
			name: "chat 无连接空形态 → connection",
			row:  listRow{capabilityDomain: "chat"},
			want: capabilitydomain.TestKindConnection,
		},
		{
			name: "chat 无连接健康检查标量为输入形态 → connection",
			row:  listRow{capabilityDomain: "chat", healthCheckEndpointMode: "embeddings_json"},
			want: capabilitydomain.TestKindConnection,
		},
		{
			name: "媒体域恒 connection",
			row:  listRow{capabilityDomain: "image", endpointModes: []string{"chat_json"}},
			want: capabilitydomain.TestKindConnection,
		},
		{
			name: "媒体域空形态恒 connection",
			row:  listRow{capabilityDomain: "video"},
			want: capabilitydomain.TestKindConnection,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := listItemTestKind(testCase.row); got != testCase.want {
				t.Fatalf("testKind = %q, want %q", got, testCase.want)
			}
		})
	}

	// JSON 契约：testKind 恒输出（无 omitempty），恒有值。
	payload, err := json.Marshal(ListItem{TestKind: capabilitydomain.TestKindConnection})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(payload), `"testKind":"connection"`) {
		t.Fatalf("列表项 JSON 必含 testKind：%s", payload)
	}

	// SQL array_to_string 拼接形态的拆分（空串 = 无有效连接/未配形态）。
	if got := splitEndpointModes(""); got != nil {
		t.Fatalf("空串应拆为空集：%v", got)
	}
	if got := splitEndpointModes("chat_json, chat_sse"); len(got) != 2 || got[0] != "chat_json" || got[1] != "chat_sse" {
		t.Fatalf("逗号拼接拆分不符：%v", got)
	}
}

func TestListItemColumnsIncludeEffectiveConnectionModes(t *testing.T) {
	columns := strings.Join(listItemColumns("accounts"), "\n")
	for _, fragment := range []string{
		"AS endpoint_modes",
		"juhe_business.account_endpoint_modes",
		"juhe_business.account_connections",
		"c.enabled = 1",
		"COALESCE(source_accounts.id, accounts.id)",
		"COALESCE(source_accounts.health_check_connection_id, accounts.health_check_connection_id)",
		"DESC, c.id ASC",
	} {
		if !strings.Contains(columns, fragment) {
			t.Fatalf("listItemColumns 缺少有效连接形态片段 %q：\n%s", fragment, columns)
		}
	}
}

// seedListTestKindAccount 落最小账户行并覆盖域/健康检查形态（seedAccount 的
// DDL 默认 health_check_endpoint_mode='chat_json'）。
func seedListTestKindAccount(t *testing.T, env *testEnv, id, ownerID, domain, healthMode string) {
	t.Helper()
	env.seedAccount(t, id, ownerID, id, "active")
	env.exec(t, `UPDATE accounts SET capability_domain = ?, health_check_endpoint_mode = ? WHERE id = ?`,
		domain, healthMode, id)
}

func seedListTestKindConnection(t *testing.T, env *testEnv, accountID, connectionID string, enabled int, modes ...string) {
	t.Helper()
	now := "2026-01-01T00:00:00Z"
	env.exec(t, `INSERT INTO account_connections
		(id, account_id, connection_preset_code, base_url, enabled, created_at, updated_at)
		VALUES (?, ?, ?, 'https://fixture.invalid/v1', ?, ?, ?)`,
		connectionID, accountID, "preset-"+connectionID, enabled, now, now)
	for _, mode := range modes {
		env.exec(t, `INSERT INTO account_endpoint_modes (connection_id, endpoint_mode) VALUES (?, ?)`,
			connectionID, mode)
	}
}

func TestListPageTestKindUsesEffectiveConnection(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	scope := AccessScope{ViewerID: adminID, IsAdmin: true}

	// chat + 连接生成形态。
	seedListTestKindAccount(t, env, "acc-tk-chat-json", adminID, "chat", "chat_sse")
	seedListTestKindConnection(t, env, "acc-tk-chat-json", "conn-tk-chat-json", 1, "chat_json")
	env.exec(t, `UPDATE accounts SET health_check_connection_id = 'conn-tk-chat-json' WHERE id = 'acc-tk-chat-json'`)

	// chat + 连接仅输入形态。
	seedListTestKindAccount(t, env, "acc-tk-embeddings", adminID, "chat", "chat_json")
	seedListTestKindConnection(t, env, "acc-tk-embeddings", "conn-tk-embeddings", 1, "embeddings_json")
	env.exec(t, `UPDATE accounts SET health_check_connection_id = 'conn-tk-embeddings' WHERE id = 'acc-tk-embeddings'`)

	// chat + 无连接 + 健康检查标量生成形态。
	seedListTestKindAccount(t, env, "acc-tk-health", adminID, "chat", "chat_sse")

	// chat + 无连接 + 空形态。
	seedListTestKindAccount(t, env, "acc-tk-empty", adminID, "chat", "")

	// 媒体域即使连接声明生成形态也恒连接测试。
	seedListTestKindAccount(t, env, "acc-tk-image", adminID, "image", "chat_json")
	seedListTestKindConnection(t, env, "acc-tk-image", "conn-tk-image", 1, "chat_json")
	env.exec(t, `UPDATE accounts SET health_check_connection_id = 'conn-tk-image' WHERE id = 'acc-tk-image'`)

	// 多连接：健康检查连接指向输入形态连接（id 字典序在后），另一连接声明
	// 生成形态——必须按 health_check_connection_id 选择而非 id ASC。
	seedListTestKindAccount(t, env, "acc-tk-multi", adminID, "chat", "")
	seedListTestKindConnection(t, env, "acc-tk-multi", "conn-tk-multi-a", 1, "chat_json")
	seedListTestKindConnection(t, env, "acc-tk-multi", "conn-tk-multi-z", 1, "embeddings_json")
	env.exec(t, `UPDATE accounts SET health_check_connection_id = 'conn-tk-multi-z' WHERE id = 'acc-tk-multi'`)

	// 健康检查连接 disabled：回落到首个 enabled 连接（b 的输入形态），
	// disabled 的生成形态连接不得被选中。
	seedListTestKindAccount(t, env, "acc-tk-disabled", adminID, "chat", "")
	seedListTestKindConnection(t, env, "acc-tk-disabled", "conn-tk-disabled-a", 0, "chat_json")
	seedListTestKindConnection(t, env, "acc-tk-disabled", "conn-tk-disabled-b", 1, "embeddings_json")
	env.exec(t, `UPDATE accounts SET health_check_connection_id = 'conn-tk-disabled-a' WHERE id = 'acc-tk-disabled'`)

	// 授权实例：按来源账户的连接形态派生。
	seedListTestKindAccount(t, env, "acc-tk-auth-source", adminID, "chat", "")
	seedListTestKindConnection(t, env, "acc-tk-auth-source", "conn-tk-auth-source", 1, "chat_json")
	env.exec(t, `UPDATE accounts SET health_check_connection_id = 'conn-tk-auth-source' WHERE id = 'acc-tk-auth-source'`)
	env.exec(t, `INSERT INTO resource_authorizations
		(id, resource_type, resource_id, resource_owner_system_account_id, grantee_system_account_id,
		 scope, status, created_by, created_at, updated_at)
		VALUES ('auth-tk', 'account', 'acc-tk-auth-source', ?, ?, 'use', 'active', ?, ?, ?)`,
		adminID, adminID, adminID, "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z")
	seedListTestKindAccount(t, env, "acc-tk-auth-instance", adminID, "chat", "")
	env.exec(t, `UPDATE accounts SET authorization_instance_source_account_id = 'acc-tk-auth-source',
		authorization_instance_authorization_id = 'auth-tk' WHERE id = 'acc-tk-auth-instance'`)

	ids := []string{
		"acc-tk-chat-json", "acc-tk-embeddings", "acc-tk-health", "acc-tk-empty",
		"acc-tk-image", "acc-tk-multi", "acc-tk-disabled", "acc-tk-auth-instance",
	}
	page, err := env.store.ListPage(context.Background(), scope, ListOptions{Page: 1, PageSize: 50, IDs: ids})
	if err != nil {
		t.Fatalf("列表查询应成功：%v", err)
	}
	byID := map[string]ListItem{}
	for _, item := range page.Items {
		byID[item.ID] = item
	}
	want := map[string]string{
		"acc-tk-chat-json":     capabilitydomain.TestKindModel,
		"acc-tk-embeddings":    capabilitydomain.TestKindConnection,
		"acc-tk-health":        capabilitydomain.TestKindModel,
		"acc-tk-empty":         capabilitydomain.TestKindConnection,
		"acc-tk-image":         capabilitydomain.TestKindConnection,
		"acc-tk-multi":         capabilitydomain.TestKindConnection,
		"acc-tk-disabled":      capabilitydomain.TestKindConnection,
		"acc-tk-auth-instance": capabilitydomain.TestKindModel,
	}
	for id, expected := range want {
		item, ok := byID[id]
		if !ok {
			t.Fatalf("账户 %s 应在列表中", id)
		}
		if item.TestKind != expected {
			t.Fatalf("账户 %s testKind = %q, want %q", id, item.TestKind, expected)
		}
	}

	// 手工测试上下文与列表同一有效连接形态来源：凭据未声明形态时，test-options
	// 的 ManualTestKind 也必须从连接行派生为 model。
	manualContext, err := env.store.FindManualTestOptionsContext(context.Background(), "acc-tk-chat-json", &scope)
	if err != nil || manualContext == nil {
		t.Fatalf("手工测试上下文应返回：%v %v", manualContext, err)
	}
	if len(manualContext.SupportedEndpointModes) != 1 || manualContext.SupportedEndpointModes[0] != "chat_json" {
		t.Fatalf("手工测试上下文应取连接形态：%v", manualContext.SupportedEndpointModes)
	}
	if got := accountstest.ManualTestKind(manualContext); got != byID["acc-tk-chat-json"].TestKind {
		t.Fatalf("手工测试方式与列表 testKind 不同源：%q vs %q", got, byID["acc-tk-chat-json"].TestKind)
	}
}
