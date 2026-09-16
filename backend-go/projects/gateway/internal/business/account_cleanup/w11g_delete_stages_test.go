package accountcleanup

// w11g 覆盖补充（第五批）：deleteBusiness 各删除阶段错误臂（drop / 条件
// 触发器注入）。

import (
	"context"
	"testing"
)

func w11gBuildRootTarget(t *testing.T, store *Store) (candidate, error) {
	t.Helper()
	return store.buildCandidate(context.Background(), accountRow{
		ID: "root", SystemAccountID: "sys",
		DeletedAt: nullStringV("2026-01-05T00:00:00.000Z"), UpdatedAt: nullStringV("2026-01-05T00:00:00.000Z"),
	})
}

func TestW11GDeleteBusinessStageErrors(t *testing.T) {
	ctx := context.Background()
	cutoff := "2026-02-01T00:00:00.000Z"
	// 956：account 维度 group_accounts 删除失败。
	store, db := w11gStore(t)
	seedW7ADeletedRoot(t, db)
	target, err := w11gBuildRootTarget(t, store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TABLE group_accounts`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.deleteBusiness(ctx, target, cutoff); err == nil {
		t.Fatal("group binding delete error must propagate")
	}
	// 972：auth 维度 group_accounts 删除失败（条件触发器只拦授权绑定）。
	store, db = w11gStore(t)
	seedW7ADeletedRoot(t, db)
	target, err = w11gBuildRootTarget(t, store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TRIGGER w11g_fail_auth_bind BEFORE DELETE ON group_accounts
		WHEN OLD.account_authorization_id IS NOT NULL
		BEGIN SELECT RAISE(ABORT, 'w11g auth bind'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.deleteBusiness(ctx, target, cutoff); err == nil {
		t.Fatal("auth binding delete error must propagate")
	}
	if _, err := db.Exec(`DROP TRIGGER w11g_fail_auth_bind`); err != nil {
		t.Fatal(err)
	}
	// 978：授权维度 quota 绑定删除失败。
	store, db = w11gStore(t)
	seedW7ADeletedRoot(t, db)
	target, err = w11gBuildRootTarget(t, store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TABLE request_quota_hourly_window_scope_bindings`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.deleteBusiness(ctx, target, cutoff); err == nil {
		t.Fatal("auth quota delete error must propagate")
	}
}

func TestW11GDeleteBusinessGrantStages(t *testing.T) {
	ctx := context.Background()
	cutoff := "2026-02-01T00:00:00.000Z"
	// 984/988：grant 维度删除失败（先 seed 授权与 grant 行）。
	store, db := w11gStore(t)
	seedW7ADeletedRoot(t, db)
	if _, err := db.Exec(`INSERT INTO resource_authorization_grants(id,resource_type,resource_id,status) VALUES ('grant-r','account','root','active')`); err != nil {
		t.Fatal(err)
	}
	target, err := w11gBuildRootTarget(t, store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TABLE request_quota_hourly_window_scope_bindings`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.deleteBusiness(ctx, target, cutoff); err == nil {
		t.Fatal("grant quota delete error must propagate")
	}
	// grants 主表删除失败（恢复 quota 表后用触发器）。
	store, db = w11gStore(t)
	seedW7ADeletedRoot(t, db)
	if _, err := db.Exec(`INSERT INTO resource_authorization_grants(id,resource_type,resource_id,status) VALUES ('grant-r2','account','root','active')`); err != nil {
		t.Fatal(err)
	}
	target, err = w11gBuildRootTarget(t, store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TRIGGER w11g_fail_grant_del BEFORE DELETE ON resource_authorization_grants
		BEGIN SELECT RAISE(ABORT, 'w11g grant del'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.deleteBusiness(ctx, target, cutoff); err == nil {
		t.Fatal("grant delete error must propagate")
	}
}

func TestW11GDeleteBusinessAccountStages(t *testing.T) {
	ctx := context.Background()
	cutoff := "2026-02-01T00:00:00.000Z"
	// 1010：related 账户删除失败（含 child 的 root）。
	store, db := w11gStore(t)
	seedW7ADeletedRoot(t, db)
	target, err := w11gBuildRootTarget(t, store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TRIGGER w11g_fail_acct_del BEFORE DELETE ON accounts
		BEGIN SELECT RAISE(ABORT, 'w11g acct del'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.deleteBusiness(ctx, target, cutoff); err == nil {
		t.Fatal("related account delete error must propagate")
	}
	if _, err := db.Exec(`DROP TRIGGER w11g_fail_acct_del`); err != nil {
		t.Fatal(err)
	}
	// 1018：root 主行删除失败（无 child 场景 + 条件触发器跳过？直接拦所有行）。
	store, db = w11gStore(t)
	seedW7ADeletedRoot(t, db)
	target, err = w11gBuildRootTarget(t, store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TRIGGER w11g_fail_root_del BEFORE DELETE ON accounts
		WHEN OLD.id = 'root'
		BEGIN SELECT RAISE(ABORT, 'w11g root del'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.deleteBusiness(ctx, target, cutoff); err == nil {
		t.Fatal("root account delete error must propagate")
	}
}
