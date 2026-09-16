package accountcleanup

// w11g 覆盖补充（第三批）：softDeleteOrphan 主表更新与关系清理的触发器
// 错误臂、撤销链 grants 阶段、buildCandidate 查询失败与关库 BeginTx。

import (
	"context"
	"testing"
)

const w11gOldTime = "2026-01-01T00:00:00.000Z"

func TestW11GSoftDeleteTriggerArms(t *testing.T) {
	ctx := context.Background()
	// 主表 UPDATE 失败。
	store, db := w11gStore(t)
	if _, err := db.Exec(`INSERT INTO accounts(id,system_account_id,authorization_instance_authorization_id,authorization_instance_source_account_id,deleted_at,updated_at,created_at,status,schedulable)
		VALUES ('orphan-t1','sys','auth-t1',NULL,NULL,?,?,'active',1)`, w11gOldTime, w11gOldTime); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TRIGGER w11g_fail_upd BEFORE UPDATE ON accounts
		BEGIN SELECT RAISE(ABORT, 'w11g upd'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.softDeleteOrphan(ctx, accountRow{
		ID: "orphan-t1", SystemAccountID: "sys",
		AuthorizationInstanceAuthID: nullStringV("auth-t1"), UpdatedAt: nullStringV(w11gOldTime),
	}); err == nil {
		t.Fatal("main update error must propagate")
	}
	if _, err := db.Exec(`DROP TRIGGER w11g_fail_upd`); err != nil {
		t.Fatal(err)
	}
	// 关系清理失败（主表更新成功后清 account_tag_bindings；行级触发器需先有行）。
	if _, err := db.Exec(`INSERT INTO account_tag_bindings (account_id) VALUES ('orphan-t1')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TRIGGER w11g_fail_del BEFORE DELETE ON account_tag_bindings
		BEGIN SELECT RAISE(ABORT, 'w11g del'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.softDeleteOrphan(ctx, accountRow{
		ID: "orphan-t1", SystemAccountID: "sys",
		AuthorizationInstanceAuthID: nullStringV("auth-t1"), UpdatedAt: nullStringV(w11gOldTime),
	}); err == nil {
		t.Fatal("relation cleanup error must propagate")
	}
	if _, err := db.Exec(`DROP TRIGGER w11g_fail_del`); err != nil {
		t.Fatal(err)
	}
	// BeginTx 失败：关库。
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.softDeleteOrphan(ctx, accountRow{
		ID: "x", SystemAccountID: "sys",
		AuthorizationInstanceAuthID: nullStringV("a"), UpdatedAt: nullStringV(w11gOldTime),
	}); err == nil {
		t.Fatal("begin error must propagate")
	}
}

func TestW11GRevokeGrantStageError(t *testing.T) {
	ctx := context.Background()
	store, db := w11gStore(t)
	if _, err := db.Exec(`INSERT INTO accounts(id,system_account_id,authorization_instance_authorization_id,authorization_instance_source_account_id,deleted_at,updated_at,created_at,status,schedulable)
		VALUES ('orphan-g','sys','auth-g',NULL,NULL,?,?,'active',1)`, w11gOldTime, w11gOldTime); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO resource_authorizations(id,resource_type,resource_id,grantee_system_account_id,status) VALUES ('auth-g','account','orphan-g','g','active')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TABLE resource_authorization_grants`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.softDeleteOrphan(ctx, accountRow{
		ID: "orphan-g", SystemAccountID: "sys",
		AuthorizationInstanceAuthID: nullStringV("auth-g"), UpdatedAt: nullStringV(w11gOldTime),
	}); err == nil {
		t.Fatal("grant revoke stage error must propagate")
	}
}

func TestW11GBuildCandidateQueryErrors(t *testing.T) {
	ctx := context.Background()
	// 关系账户查询失败。
	store, db := w11gStore(t)
	if _, err := db.Exec(`INSERT INTO accounts(id,system_account_id,authorization_instance_authorization_id,authorization_instance_source_account_id,deleted_at,updated_at,created_at,status,schedulable)
		VALUES ('root-q','sys',NULL,NULL,?,?,?,'disabled',0)`, w11gOldTime, w11gOldTime, w11gOldTime); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TABLE accounts`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.buildCandidate(ctx, accountRow{
		ID: "root-q", SystemAccountID: "sys",
		DeletedAt: nullStringV(w11gOldTime), UpdatedAt: nullStringV(w11gOldTime),
	}); err == nil {
		t.Fatal("related query error must propagate")
	}
	// 授权查询失败。
	store, db = w11gStore(t)
	if _, err := db.Exec(`DROP TABLE resource_authorizations`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.buildCandidate(ctx, accountRow{
		ID: "root-q2", SystemAccountID: "sys",
		DeletedAt: nullStringV(w11gOldTime), UpdatedAt: nullStringV(w11gOldTime),
	}); err == nil {
		t.Fatal("authorization query error must propagate")
	}
}
