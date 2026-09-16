package accountcleanup

// w11g 覆盖补充（第六批）：revokeInstance 授权主表更新错误、deleteBusiness
// 授权删除阶段与 grant 维度 quota 删除阶段错误。

import (
	"context"
	"testing"
)

func TestW11GRevokeAuthorizationUpdateError(t *testing.T) {
	ctx := context.Background()
	store, db := w11gStore(t)
	if _, err := db.Exec(`INSERT INTO accounts(id,system_account_id,authorization_instance_authorization_id,authorization_instance_source_account_id,deleted_at,updated_at,created_at,status,schedulable)
		VALUES ('orphan-ra','sys','auth-ra',NULL,NULL,?,?,'active',1)`, w11gOldTime, w11gOldTime); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO resource_authorizations(id,resource_type,resource_id,grantee_system_account_id,status) VALUES ('auth-ra','group','grp-w11g','g','active')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TRIGGER w11g_fail_ra_upd BEFORE UPDATE ON resource_authorizations
		BEGIN SELECT RAISE(ABORT, 'w11g ra upd'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.softDeleteOrphan(ctx, accountRow{
		ID: "orphan-ra", SystemAccountID: "sys",
		AuthorizationInstanceAuthID: nullStringV("auth-ra"), UpdatedAt: nullStringV(w11gOldTime),
	}); err == nil {
		t.Fatal("authorization update error must propagate")
	}
}

func TestW11GDeleteBusinessAuthorizationStageError(t *testing.T) {
	ctx := context.Background()
	store, db := w11gStore(t)
	seedW7ADeletedRoot(t, db)
	target, err := w11gBuildRootTarget(t, store)
	if err != nil {
		t.Fatal(err)
	}
	// 授权主表在账户删除阶段之后才被删除：直调时此刻 drop 即命中尾部阶段。
	if _, err := db.Exec(`DROP TABLE resource_authorizations`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.deleteBusiness(ctx, target, "2026-02-01T00:00:00.000Z"); err == nil {
		t.Fatal("authorization tail delete error must propagate")
	}
}

func TestW11GDeleteBusinessGrantQuotaStageError(t *testing.T) {
	ctx := context.Background()
	store, db := w11gStore(t)
	seedW7ADeletedRoot(t, db)
	if _, err := db.Exec(`INSERT INTO resource_authorization_grants(id,resource_type,resource_id,status) VALUES ('grant-q','account','root','active')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO request_quota_hourly_window_scope_bindings(source_type,source_id,scope_type,scope_id) VALUES ('resource_authorization_grant','grant-q','account_authorization','auth-child')`); err != nil {
		t.Fatal(err)
	}
	target, err := w11gBuildRootTarget(t, store)
	if err != nil {
		t.Fatal(err)
	}
	// 只拦 grant 维度的 quota 删除。
	if _, err := db.Exec(`CREATE TRIGGER w11g_fail_grant_quota BEFORE DELETE ON request_quota_hourly_window_scope_bindings
		WHEN OLD.source_type = 'resource_authorization_grant'
		BEGIN SELECT RAISE(ABORT, 'w11g grant quota'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.deleteBusiness(ctx, target, "2026-02-01T00:00:00.000Z"); err == nil {
		t.Fatal("grant quota delete error must propagate")
	}
}
