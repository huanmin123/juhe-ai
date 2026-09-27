package accounts

// ListChatAccountOptions（新建会话绑定下拉账户侧）的存储级覆盖：与
// FindChatAccount 同口径的五类过滤（status 非 active、schedulable 禁用、
// deleted_at 非空、authorization_instance_authorization_id 非空、正常
// active），仅最后一类可见，排序 name ASC, id ASC（同名跨 owner 按 id 决胜）。

import (
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/chat"
)

func TestListChatAccountOptions(t *testing.T) {
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
	options, err := env.store.ListChatAccountOptions()
	if err != nil {
		t.Fatal(err)
	}
	want := []chat.ChatBindOption{
		{ID: "acc_active_1", Name: "可用账户"},
		{ID: "acc_active_2", Name: "可用账户"},
	}
	if len(options) != len(want) {
		t.Fatalf("options = %v, want %v", options, want)
	}
	for index, option := range options {
		if option != want[index] {
			t.Fatalf("options[%d] = %v, want %v（顺序须 name ASC, id ASC 且仅正常 active 行）", index, option, want[index])
		}
	}
}
