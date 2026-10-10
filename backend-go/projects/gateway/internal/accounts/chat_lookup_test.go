package accounts

// ListChatAccountOptions / FindChatAccount（会话账户绑定侧）的存储级覆盖：
// 2026-10-10 修订后列表 = 数据范围内全部未删除账户（status 非 active、
// schedulable 禁用等全部状态均可见，status 投影为生效状态原值）；仅
// deleted_at 非空与 authorization_instance_authorization_id 非空仍排除，排序
// name ASC, id ASC（同名跨 owner 按 id 决胜）；数据范围按 ChatBindScope 收
// 敛——admin/super_admin 读全量号池，普通用户仅自己名下（他人行、已删行、
// 实例戳行仍排除）。选项投影含 providerCode/status（/my-chat/accounts 形状）。

import (
	"context"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/chat"
)

func TestListChatAccountOptionsAdminSeesAllUndeleted(t *testing.T) {
	env := newTestEnv(t)
	for _, row := range []struct {
		id            string
		owner         string
		name          string
		status        string
		schedulable   int
		deletedAt     any
		authorization any
	}{
		{id: "acc_deleted", owner: "owner-a", name: "已删除账户", status: "active", schedulable: 1, deletedAt: "2026-01-02T00:00:00.000Z", authorization: nil},
		{id: "acc_authz", owner: "owner-a", name: "授权实例账户", status: "active", schedulable: 1, deletedAt: nil, authorization: "authz-1"},
		{id: "acc_inactive", owner: "owner-a", name: "停用账户", status: "disabled", schedulable: 1, deletedAt: nil, authorization: nil},
		{id: "acc_unschedulable", owner: "owner-a", name: "禁调度账户", status: "active", schedulable: 0, deletedAt: nil, authorization: nil},
		// 正常 active 两行同名分属不同 owner（唯一索引按 owner 隔离），验证
		// id ASC 决胜。
		{id: "acc_active_2", owner: "owner-b", name: "可用账户", status: "active", schedulable: 1, deletedAt: nil, authorization: nil},
		{id: "acc_active_1", owner: "owner-a", name: "可用账户", status: "active", schedulable: 1, deletedAt: nil, authorization: nil},
	} {
		if _, err := env.db.Exec(
			`INSERT INTO accounts (id, system_account_id, provider_code, provider_protocol_profile_id,
				protocol_code, protocol_version, name, type, status, schedulable, credentials_encrypted,
				deleted_at, authorization_instance_authorization_id, created_at, updated_at)
			VALUES (?, ?, 'openai', 'prof-1', 'openai', 'v1', ?, 'api_key', ?, ?, 'enc', ?, ?,
				'2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`,
			row.id, row.owner, row.name, row.status, row.schedulable, row.deletedAt, row.authorization); err != nil {
			t.Fatal(err)
		}
	}
	options, err := env.store.ListChatAccountOptions(context.Background(), chat.ChatBindScope{IsAdmin: true})
	if err != nil {
		t.Fatal(err)
	}
	want := []chat.ChatAccountOption{
		// status 投影为生效状态原值：status=disabled 原样；schedulable=0 合成
		// disabled（ownerEffectiveStatusSQL）。name 排序为字节序（停 < 可 < 禁）。
		{ID: "acc_inactive", Name: "停用账户", ProviderCode: "openai", Status: "disabled"},
		{ID: "acc_active_1", Name: "可用账户", ProviderCode: "openai", Status: "active"},
		{ID: "acc_active_2", Name: "可用账户", ProviderCode: "openai", Status: "active"},
		{ID: "acc_unschedulable", Name: "禁调度账户", ProviderCode: "openai", Status: "disabled"},
	}
	if len(options) != len(want) {
		t.Fatalf("options = %v, want %v", options, want)
	}
	for index, option := range options {
		if option != want[index] {
			t.Fatalf("options[%d] = %v, want %v（顺序须 name ASC, id ASC，已删/实例戳行排除，其余全状态可见）", index, option, want[index])
		}
	}
}

// sameChatAccountRef 逐字段比较 ChatAccountRef（含 EnabledGroupIDs 切片，
// 结构体整体不可 == 比较）。
func sameChatAccountRef(got, want *chat.ChatAccountRef) bool {
	if got.ID != want.ID || got.Name != want.Name || got.ProviderCode != want.ProviderCode || got.Enabled != want.Enabled {
		return false
	}
	if len(got.EnabledGroupIDs) != len(want.EnabledGroupIDs) {
		return false
	}
	for index, id := range want.EnabledGroupIDs {
		if got.EnabledGroupIDs[index] != id {
			return false
		}
	}
	return true
}

// TestChatAccountLookupViewerScope 覆盖普通用户臂：下拉见自己名下全部未删行
// （含停用，status 投影生效状态；他人行、已删行、实例戳行仍排除）；
// FindChatAccount 同口径返回存在性 + Enabled 投影，范围外与不存在同型返回
// (nil, nil)；admin 臂读全量号池。
func TestChatAccountLookupViewerScope(t *testing.T) {
	env := newTestEnv(t)
	viewer, other := "viewer-1", "owner-other"
	for _, row := range []struct {
		id            string
		owner         string
		name          string
		status        string
		schedulable   int
		deletedAt     any
		authorization any
	}{
		{id: "acc_own_active", owner: viewer, name: "a 自有可用", status: "active", schedulable: 1, deletedAt: nil, authorization: nil},
		{id: "acc_own_disabled", owner: viewer, name: "b 自有停用", status: "disabled", schedulable: 1, deletedAt: nil, authorization: nil},
		{id: "acc_other_active", owner: other, name: "c 他人可用", status: "active", schedulable: 1, deletedAt: nil, authorization: nil},
		{id: "acc_own_deleted", owner: viewer, name: "d 自有已删", status: "active", schedulable: 1, deletedAt: "2026-01-02T00:00:00.000Z", authorization: nil},
		{id: "acc_own_stamped", owner: viewer, name: "e 自有实例戳", status: "active", schedulable: 1, deletedAt: nil, authorization: "authz-1"},
	} {
		if _, err := env.db.Exec(
			`INSERT INTO accounts (id, system_account_id, provider_code, provider_protocol_profile_id,
				protocol_code, protocol_version, name, type, status, schedulable, credentials_encrypted,
				deleted_at, authorization_instance_authorization_id, created_at, updated_at)
			VALUES (?, ?, 'openai', 'prof-1', 'openai', 'v1', ?, 'api_key', ?, ?, 'enc', ?, ?,
				'2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`,
			row.id, row.owner, row.name, row.status, row.schedulable, row.deletedAt, row.authorization); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("非 admin 下拉见自己名下全部未删行", func(t *testing.T) {
		options, err := env.store.ListChatAccountOptions(context.Background(), chat.ChatBindScope{ViewerID: viewer})
		if err != nil {
			t.Fatal(err)
		}
		want := []chat.ChatAccountOption{
			{ID: "acc_own_active", Name: "a 自有可用", ProviderCode: "openai", Status: "active"},
			{ID: "acc_own_disabled", Name: "b 自有停用", ProviderCode: "openai", Status: "disabled"},
		}
		if len(options) != len(want) {
			t.Fatalf("viewer options = %v, want %v", options, want)
		}
		for index, option := range options {
			if option != want[index] {
				t.Fatalf("viewer options[%d] = %v, want %v", index, option, want[index])
			}
		}
	})

	t.Run("非 admin FindChatAccount 同口径", func(t *testing.T) {
		scope := chat.ChatBindScope{ViewerID: viewer}
		refs := []struct {
			name    string
			id      string
			wantRef *chat.ChatAccountRef
		}{
			{name: "自有 active 行", id: "acc_own_active", wantRef: &chat.ChatAccountRef{ID: "acc_own_active", Name: "a 自有可用", ProviderCode: "openai", Enabled: true}},
			{name: "自有停用行可见但 Enabled=false", id: "acc_own_disabled", wantRef: &chat.ChatAccountRef{ID: "acc_own_disabled", Name: "b 自有停用", ProviderCode: "openai", Enabled: false}},
			{name: "他人行不可见", id: "acc_other_active", wantRef: nil},
			{name: "已删行不可见", id: "acc_own_deleted", wantRef: nil},
			{name: "实例戳行不可见", id: "acc_own_stamped", wantRef: nil},
			{name: "不存在的账户", id: "acc_missing", wantRef: nil},
		}
		for _, item := range refs {
			ref, err := env.store.FindChatAccount(scope, item.id)
			if err != nil {
				t.Fatalf("%s: FindChatAccount = %v", item.name, err)
			}
			if item.wantRef == nil && ref != nil {
				t.Fatalf("%s: FindChatAccount = %v, want nil（范围外与不存在同型）", item.name, ref)
			}
			if item.wantRef != nil && (ref == nil || !sameChatAccountRef(ref, item.wantRef)) {
				t.Fatalf("%s: FindChatAccount = %+v, want %+v", item.name, ref, *item.wantRef)
			}
		}
	})

	t.Run("admin 下拉与 FindChatAccount 读全量号池", func(t *testing.T) {
		options, err := env.store.ListChatAccountOptions(context.Background(), chat.ChatBindScope{ViewerID: viewer, IsAdmin: true})
		if err != nil {
			t.Fatal(err)
		}
		want := []chat.ChatAccountOption{
			{ID: "acc_own_active", Name: "a 自有可用", ProviderCode: "openai", Status: "active"},
			{ID: "acc_own_disabled", Name: "b 自有停用", ProviderCode: "openai", Status: "disabled"},
			{ID: "acc_other_active", Name: "c 他人可用", ProviderCode: "openai", Status: "active"},
		}
		if len(options) != len(want) {
			t.Fatalf("admin options = %v, want %v", options, want)
		}
		for index, option := range options {
			if option != want[index] {
				t.Fatalf("admin options[%d] = %v, want %v", index, option, want[index])
			}
		}
		ref, err := env.store.FindChatAccount(chat.ChatBindScope{ViewerID: viewer, IsAdmin: true}, "acc_other_active")
		if err != nil {
			t.Fatal(err)
		}
		if ref == nil || !ref.Enabled || ref.Name != "c 他人可用" {
			t.Fatalf("admin FindChatAccount(他人行) = %v, want 启用行", ref)
		}
	})
}
