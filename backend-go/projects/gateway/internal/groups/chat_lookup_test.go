package groups

// ListChatGroupOptions（新建会话绑定下拉分组侧）的存储级覆盖：仅 enabled=1
// 行可见，排序 name ASC, id ASC 稳定（同名分组按 id 决胜）。

import (
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/chat"
)

func TestListChatGroupOptions(t *testing.T) {
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
	options, err := store.ListChatGroupOptions()
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
