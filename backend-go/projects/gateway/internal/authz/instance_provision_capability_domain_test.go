// AI 账户能力域拆分 M1（PLAN-20261011T141004000Z D3）：授权实例域继承——
// 插入臂携带源账户 capability_domain，软删除恢复臂把实例域同步回源当前域。
// 实例域恒等于源域（随源继承，不独立赋值）。
package authz

import (
	"context"
	"testing"
)

func TestProvisionInstanceInheritsSourceCapabilityDomainOnInsert(t *testing.T) {
	f := newFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "grantee", "active")
	f.seedProvisionSource(t, "acc-src-video", "owner", "视频账户", "gpt")
	if _, err := f.db.Exec(`UPDATE accounts SET capability_domain = 'video' WHERE id = 'acc-src-video'`); err != nil {
		t.Fatal(err)
	}
	f.seedGranteeGroup(t, "grp-target", "grantee", "gpt", 1, 0)

	if _, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "account", ResourceID: "acc-src-video",
		GranteeType: "system_account", GranteeID: "grantee",
		TargetGroupID: strPtr("grp-target"),
	}, "owner"); err != nil {
		t.Fatal(err)
	}

	row := f.provisionInstanceByAuthorization(t, "acc-src-video", "grantee")
	if row.capabilityDomain != "video" {
		t.Fatalf("插入臂实例域应继承源域 video，got %q", row.capabilityDomain)
	}

	// 缺省源（列 DEFAULT 'chat'）：实例域继承 'chat'，无独立赋值臂。
	f.seedProvisionSource(t, "acc-src-chat", "owner", "对话账户", "gpt")
	if _, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "account", ResourceID: "acc-src-chat",
		GranteeType: "system_account", GranteeID: "grantee",
		TargetGroupID: strPtr("grp-target"),
	}, "owner"); err != nil {
		t.Fatal(err)
	}
	chatRow := f.provisionInstanceByAuthorization(t, "acc-src-chat", "grantee")
	if chatRow.capabilityDomain != "chat" {
		t.Fatalf("缺省源实例域应为 chat，got %q", chatRow.capabilityDomain)
	}
}

func TestProvisionRestoresInstanceCapabilityDomainFromSource(t *testing.T) {
	f := newFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "grantee", "active")
	f.seedProvisionSource(t, "acc-src-audio", "owner", "音频账户", "gpt")
	if _, err := f.db.Exec(`UPDATE accounts SET capability_domain = 'audio' WHERE id = 'acc-src-audio'`); err != nil {
		t.Fatal(err)
	}
	f.seedGranteeGroup(t, "grp-target", "grantee", "gpt", 1, 0)

	result, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "account", ResourceID: "acc-src-audio",
		GranteeType: "system_account", GranteeID: "grantee",
		TargetGroupID: strPtr("grp-target"),
	}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	first := f.provisionInstanceByAuthorization(t, "acc-src-audio", "grantee")

	// 实例软删除 + 陈旧域（模拟删除后源域语义变化的存量形态）：恢复臂必须
	// 把实例域同步回源当前域，而不是保留陈旧值。
	if _, err := f.db.Exec(`UPDATE accounts SET deleted_at = '2026-02-01T00:00:00Z', deleted_by = 'owner',
		capability_domain = 'chat', status = 'disabled' WHERE id = ?`, first.id); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`UPDATE resource_authorization_grants SET status = 'revoked',
		revoked_by = 'owner', revoked_at = '2026-02-01T00:00:00Z' WHERE id = ?`, result.Item.ID); err != nil {
		t.Fatal(err)
	}

	if _, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "account", ResourceID: "acc-src-audio",
		GranteeType: "system_account", GranteeID: "grantee",
		TargetGroupID: strPtr("grp-target"),
	}, "owner"); err != nil {
		t.Fatal(err)
	}

	restored := f.provisionInstanceByAuthorization(t, "acc-src-audio", "grantee")
	if restored.id != first.id {
		t.Fatalf("恢复臂应复用软删除行: %q vs %q", restored.id, first.id)
	}
	if restored.capabilityDomain != "audio" {
		t.Fatalf("恢复臂实例域应同步源域 audio，got %q", restored.capabilityDomain)
	}
}
