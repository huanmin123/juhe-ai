package accountcleanup

// w11g 覆盖补充（第四批）：带来源账户的孤儿软删（source 参数分支）与
// grants 撤销阶段错误、Cleanup 孤儿软删聚合臂。

import (
	"context"
	"testing"
)

func TestW11GSoftDeleteOrphanWithSourceBranch(t *testing.T) {
	ctx := context.Background()
	store, db := w11gStore(t)
	if _, err := db.Exec(`INSERT INTO accounts(id,system_account_id,authorization_instance_authorization_id,authorization_instance_source_account_id,deleted_at,updated_at,created_at,status,schedulable)
		VALUES ('orphan-s','sys','auth-s','src-root',NULL,?,?,'active',1)`, w11gOldTime, w11gOldTime); err != nil {
		t.Fatal(err)
	}
	changed, err := store.softDeleteOrphan(ctx, accountRow{
		ID: "orphan-s", SystemAccountID: "sys",
		AuthorizationInstanceAuthID:   nullStringV("auth-s"),
		AuthorizationInstanceSourceID: nullStringV("src-root"),
		UpdatedAt:                     nullStringV(w11gOldTime),
	})
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
}

func TestW11GRevokeGrantsUpdateStageError(t *testing.T) {
	ctx := context.Background()
	store, db := w11gStore(t)
	if _, err := db.Exec(`INSERT INTO accounts(id,system_account_id,authorization_instance_authorization_id,authorization_instance_source_account_id,deleted_at,updated_at,created_at,status,schedulable)
		VALUES ('orphan-gr','sys','auth-gr',NULL,NULL,?,?,'active',1)`, w11gOldTime, w11gOldTime); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO resource_authorizations(id,resource_type,resource_id,grantee_system_account_id,status) VALUES ('auth-gr','account','orphan-gr','g','active')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO resource_authorization_grants(id,resource_type,resource_id,status) VALUES ('grant-w11g','account','orphan-gr','active')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TRIGGER w11g_fail_grant BEFORE UPDATE ON resource_authorization_grants
		BEGIN SELECT RAISE(ABORT, 'w11g grant'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.softDeleteOrphan(ctx, accountRow{
		ID: "orphan-gr", SystemAccountID: "sys",
		AuthorizationInstanceAuthID: nullStringV("auth-gr"), UpdatedAt: nullStringV(w11gOldTime),
	}); err == nil {
		t.Fatal("grant update stage error must propagate")
	}
}

func TestW11GCleanupOrphanSoftDeleteErrorArm(t *testing.T) {
	store, db := w11gStore(t)
	if _, err := db.Exec(`INSERT INTO accounts(id,system_account_id,authorization_instance_authorization_id,authorization_instance_source_account_id,deleted_at,updated_at,created_at,status,schedulable)
		VALUES ('orphan-c2','sys','auth-c2',NULL,NULL,?,?,'active',1)`, w11gOldTime, w11gOldTime); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TRIGGER w11g_fail_upd2 BEFORE UPDATE ON accounts
		BEGIN SELECT RAISE(ABORT, 'w11g upd2'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Cleanup(context.Background(), CleanupInput{CutoffDeletedAt: "2026-02-01T00:00:00.000Z"}); err == nil {
		t.Fatal("orphan soft-delete failure must surface from Cleanup")
	}
}
