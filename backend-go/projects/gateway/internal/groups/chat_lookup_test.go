package groups

// ListChatGroupOptions / FindChatGroup（新建会话绑定分组侧）的存储级覆盖：
// 数据范围按 ChatBindScope 收敛——admin/super_admin 读全量号池；普通用户读
// 自有 + 被授权行（resource_authorizations 授权臂谓词与 options.go:145-157
// 同源，per-grantee settings 覆盖 enabled）。排序 name ASC, id ASC 稳定。

import (
	"context"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/chat"
)

func TestListChatGroupOptionsAdminSeesAllEnabled(t *testing.T) {
	env := newTestEnv(t)
	store, err := NewStore(env.db, false, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	// 插入启用/停用混合行：zeta 分组停用（enabled=0）必须被过滤；omega
	// 同名两行分属不同 owner（唯一索引按 owner 隔离），验证 id ASC 决胜。
	for _, row := range []struct {
		id      string
		owner   string
		name    string
		enabled int
	}{
		{id: "grp_omega_b", owner: "owner-b", name: "omega 分组", enabled: 1},
		{id: "grp_2", owner: "owner-a", name: "beta 分组", enabled: 1},
		{id: "grp_disabled", owner: "owner-a", name: "zeta 分组", enabled: 0},
		{id: "grp_1", owner: "owner-a", name: "alpha 分组", enabled: 1},
		{id: "grp_omega_a", owner: "owner-a", name: "omega 分组", enabled: 1},
	} {
		if _, err := env.db.Exec(
			`INSERT INTO groups (id, system_account_id, name, provider_code, enabled, created_at, updated_at)
			VALUES (?, ?, ?, 'openai', ?, '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`,
			row.id, row.owner, row.name, row.enabled); err != nil {
			t.Fatal(err)
		}
	}
	options, err := store.ListChatGroupOptions(context.Background(), chat.ChatBindScope{IsAdmin: true})
	if err != nil {
		t.Fatal(err)
	}
	want := []chat.ChatBindOption{
		{ID: "grp_1", Name: "alpha 分组"},
		{ID: "grp_2", Name: "beta 分组"},
		{ID: "grp_omega_a", Name: "omega 分组"},
		{ID: "grp_omega_b", Name: "omega 分组"},
	}
	if len(options) != len(want) {
		t.Fatalf("options = %v, want %v", options, want)
	}
	for index, option := range options {
		if option != want[index] {
			t.Fatalf("options[%d] = %v, want %v（顺序须 name ASC, id ASC 且仅启用行）", index, option, want[index])
		}
	}
}

// TestChatGroupLookupViewerScope 覆盖普通用户臂：自有启用行可见、他人行不
// 可见、被授权 active 行可见、被授权被 per-grantee 停用的行 Enabled=false
// （下拉过滤后不可见）、授权 status=revoked 行不可见；FindChatGroup 同口径。
func TestChatGroupLookupViewerScope(t *testing.T) {
	env := newTestEnv(t)
	store, err := NewStore(env.db, false, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	viewer, other := "viewer-1", "owner-other"
	now := time.Now().UTC().Format(time.RFC3339Nano)
	// 分组行：名称按字母序排布，便于断言下拉顺序。
	for _, row := range []struct {
		id      string
		owner   string
		name    string
		enabled int
	}{
		{id: "grp_own_enabled", owner: viewer, name: "alpha 自有启用", enabled: 1},
		{id: "grp_other_enabled", owner: other, name: "bravo 他人启用", enabled: 1},
		{id: "grp_authz_active", owner: other, name: "charlie 授权启用", enabled: 1},
		{id: "grp_authz_disabled", owner: other, name: "delta 授权停用", enabled: 1},
		{id: "grp_authz_revoked", owner: other, name: "echo 授权撤销", enabled: 1},
		{id: "grp_own_disabled", owner: viewer, name: "zulu 自有停用", enabled: 0},
		{id: "grp_other_disabled", owner: other, name: "yankee 他人停用", enabled: 0},
	} {
		if _, err := env.db.Exec(
			`INSERT INTO groups (id, system_account_id, name, provider_code, enabled, created_at, updated_at)
			VALUES (?, ?, ?, 'openai', ?, '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`,
			row.id, row.owner, row.name, row.enabled); err != nil {
			t.Fatal(err)
		}
	}
	// 授权行：active（可见）、active + per-grantee settings 停用（可见但
	// Enabled=false）、revoked（不可见）。
	for _, row := range []struct {
		id     string
		group  string
		status string
	}{
		{id: "authz-active", group: "grp_authz_active", status: "active"},
		{id: "authz-disabled", group: "grp_authz_disabled", status: "active"},
		{id: "authz-revoked", group: "grp_authz_revoked", status: "revoked"},
	} {
		if _, err := env.db.Exec(
			`INSERT INTO resource_authorizations (id, resource_type, resource_id, resource_owner_system_account_id,
				grantee_system_account_id, scope, status, created_by, created_at, updated_at)
			VALUES (?, 'group', ?, ?, ?, 'use', ?, ?, ?, ?)`,
			row.id, row.group, other, viewer, row.status, other, now, now); err != nil {
			t.Fatal(err)
		}
	}
	// per-grantee settings：authz-disabled 对 viewer 停用（enabled=0）。
	if _, err := env.db.Exec(
		`INSERT INTO group_authorization_settings (authorization_id, system_account_id, group_id, enabled, group_type, created_at, updated_at)
		VALUES ('authz-disabled', ?, 'grp_authz_disabled', 0, 'personal', ?, ?)`,
		viewer, now, now); err != nil {
		t.Fatal(err)
	}

	t.Run("非 admin 下拉只见自有启用行与被授权 active 行", func(t *testing.T) {
		options, err := store.ListChatGroupOptions(context.Background(), chat.ChatBindScope{ViewerID: viewer})
		if err != nil {
			t.Fatal(err)
		}
		want := []chat.ChatBindOption{
			{ID: "grp_own_enabled", Name: "alpha 自有启用"},
			{ID: "grp_authz_active", Name: "charlie 授权启用"},
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

	t.Run("非 admin FindChatGroup 同口径", func(t *testing.T) {
		scope := chat.ChatBindScope{ViewerID: viewer}
		refs := []struct {
			name    string
			groupID string
			wantRef *chat.ChatGroupRef
		}{
			{name: "自有启用行", groupID: "grp_own_enabled", wantRef: &chat.ChatGroupRef{ID: "grp_own_enabled", Name: "alpha 自有启用", Enabled: true}},
			{name: "自有停用行可见但 Enabled=false", groupID: "grp_own_disabled", wantRef: &chat.ChatGroupRef{ID: "grp_own_disabled", Name: "zulu 自有停用", Enabled: false}},
			{name: "他人行不可见", groupID: "grp_other_enabled", wantRef: nil},
			{name: "他人停用行不可见", groupID: "grp_other_disabled", wantRef: nil},
			{name: "被授权 active 行可见", groupID: "grp_authz_active", wantRef: &chat.ChatGroupRef{ID: "grp_authz_active", Name: "charlie 授权启用", Enabled: true}},
			{name: "被授权 per-grantee 停用行 Enabled=false", groupID: "grp_authz_disabled", wantRef: &chat.ChatGroupRef{ID: "grp_authz_disabled", Name: "delta 授权停用", Enabled: false}},
			{name: "revoked 授权行不可见", groupID: "grp_authz_revoked", wantRef: nil},
			{name: "不存在的分组", groupID: "grp_missing", wantRef: nil},
		}
		for _, item := range refs {
			ref, err := store.FindChatGroup(scope, item.groupID)
			if err != nil {
				t.Fatalf("%s: FindChatGroup = %v", item.name, err)
			}
			if item.wantRef == nil && ref != nil {
				t.Fatalf("%s: FindChatGroup = %v, want nil（范围外与不存在同型）", item.name, ref)
			}
			if item.wantRef != nil && (ref == nil || *ref != *item.wantRef) {
				t.Fatalf("%s: FindChatGroup = %v, want %v", item.name, ref, *item.wantRef)
			}
		}
	})

	t.Run("admin 下拉与 FindChatGroup 读全量启用号池", func(t *testing.T) {
		options, err := store.ListChatGroupOptions(context.Background(), chat.ChatBindScope{ViewerID: viewer, IsAdmin: true})
		if err != nil {
			t.Fatal(err)
		}
		want := []chat.ChatBindOption{
			{ID: "grp_own_enabled", Name: "alpha 自有启用"},
			{ID: "grp_other_enabled", Name: "bravo 他人启用"},
			{ID: "grp_authz_active", Name: "charlie 授权启用"},
			{ID: "grp_authz_disabled", Name: "delta 授权停用"},
			{ID: "grp_authz_revoked", Name: "echo 授权撤销"},
		}
		if len(options) != len(want) {
			t.Fatalf("admin options = %v, want %v", options, want)
		}
		for index, option := range options {
			if option != want[index] {
				t.Fatalf("admin options[%d] = %v, want %v", index, option, want[index])
			}
		}
		// admin 臂读 g.enabled 原值，不看授权设置。
		ref, err := store.FindChatGroup(chat.ChatBindScope{ViewerID: viewer, IsAdmin: true}, "grp_other_disabled")
		if err != nil {
			t.Fatal(err)
		}
		if ref == nil || ref.Enabled {
			t.Fatalf("admin FindChatGroup(他人停用行) = %v, want Enabled=false", ref)
		}
	})
}
