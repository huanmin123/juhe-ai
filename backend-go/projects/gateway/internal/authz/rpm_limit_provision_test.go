package authz

// 授权实例每分钟请求上限继承（AI账户每分钟请求上限设计 §3）最小回归：
// 插入臂实例 rpm_limit 取来源值（COALESCE 投影 + INSERT 同步）。

import (
	"context"
	"testing"
)

func TestRPMProvisionInstanceInheritsSourceRPMLimit(t *testing.T) {
	f := newFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "grantee", "active")
	f.seedProvisionSource(t, "acc-rpm-src", "owner", "RPM生产账户", "gpt")
	if _, err := f.db.Exec(fixtureBind(`UPDATE accounts SET rpm_limit = 25 WHERE id = 'acc-rpm-src'`)); err != nil {
		t.Fatal(err)
	}
	f.seedGranteeGroup(t, "grp-rpm-target", "grantee", "gpt", 1, 0)

	if _, err := f.store.Create(context.Background(), CreateInput{
		ResourceType: "account", ResourceID: "acc-rpm-src",
		GranteeType: "system_account", GranteeID: "grantee",
		TargetGroupID: strPtr("grp-rpm-target"),
	}, "owner"); err != nil {
		t.Fatal(err)
	}

	row := f.provisionInstanceByAuthorization(t, "acc-rpm-src", "grantee")
	if row.id == "" {
		t.Fatal("实例行应创建")
	}
	var rpmLimit int
	if err := f.db.QueryRow(fixtureBind(`SELECT rpm_limit FROM accounts WHERE id = ?`), row.id).Scan(&rpmLimit); err != nil {
		t.Fatal(err)
	}
	if rpmLimit != 25 {
		t.Fatalf("实例 rpm_limit 应继承来源值 25，got %d", rpmLimit)
	}
}
