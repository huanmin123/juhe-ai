package accountcleanup

// w11g 覆盖补充（第二批）：deleteBusiness 完整成功链、Cleanup 孤儿与
// 候选列表错误臂、撤销链授权缺失分支。

import (
	"context"
	"testing"
)

func TestW11GDeleteBusinessFullSuccessChain(t *testing.T) {
	store, db := w11gStore(t)
	seedW7ADeletedRoot(t, db)
	cutoff := "2026-02-01T00:00:00.000Z"
	target, err := store.buildCandidate(context.Background(), accountRow{
		ID: "root", SystemAccountID: "sys", DeletedAt: nullStringV("2026-01-05T00:00:00.000Z"), UpdatedAt: nullStringV("2026-01-05T00:00:00.000Z"),
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := store.deleteBusiness(context.Background(), target, cutoff)
	if err != nil {
		t.Fatalf("full delete err=%v", err)
	}
	if result.Accounts < 2 || result.Authorizations < 1 {
		t.Fatalf("result=%+v", result)
	}
	var remaining int
	if err := db.QueryRow(`SELECT COUNT(1) FROM accounts`).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("remaining=%d err=%v", remaining, err)
	}
}

func TestW11GCleanupOrphanAndCandidateListErrors(t *testing.T) {
	ctx := context.Background()
	// 孤儿软删失败聚合。
	store, db := w11gStore(t)
	old := "2026-01-01T00:00:00.000Z"
	if _, err := db.Exec(`INSERT INTO accounts(id,system_account_id,authorization_instance_authorization_id,authorization_instance_source_account_id,deleted_at,updated_at,created_at,status,schedulable)
		VALUES ('orphan','sys','auth-w11g-c',NULL,NULL,?,?,'active',1)`, old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TABLE resource_authorizations`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Cleanup(ctx, CleanupInput{CutoffDeletedAt: "2026-02-01T00:00:00.000Z"}); err == nil {
		t.Fatal("orphan soft-delete error must surface")
	}
	// 候选列表失败。
	store, db = w11gStore(t)
	if _, err := db.Exec(`DROP TABLE accounts`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Cleanup(ctx, CleanupInput{CutoffDeletedAt: "2026-02-01T00:00:00.000Z"}); err == nil {
		t.Fatal("candidate list error must surface")
	}
}

func TestW11GRevokeInstanceAuthMissingBranch(t *testing.T) {
	// 授权行不存在（ErrNoRows 静默跳过 revokeAccountAuthorizations）后的完整软删。
	store, db := w11gStore(t)
	old := "2026-01-01T00:00:00.000Z"
	if _, err := db.Exec(`INSERT INTO accounts(id,system_account_id,authorization_instance_authorization_id,authorization_instance_source_account_id,deleted_at,updated_at,created_at,status,schedulable)
		VALUES ('orphan-ok','sys','auth-w11g-m',NULL,NULL,?,?,'active',1)`, old, old); err != nil {
		t.Fatal(err)
	}
	changed, err := store.softDeleteOrphan(context.Background(), accountRow{
		ID: "orphan-ok", SystemAccountID: "sys",
		AuthorizationInstanceAuthID: nullStringV("auth-w11g-m"), UpdatedAt: nullStringV(old),
	})
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	var status string
	var deletedAt *string
	if err := db.QueryRow(`SELECT status, deleted_at FROM accounts WHERE id='orphan-ok'`).Scan(&status, &deletedAt); err != nil {
		t.Fatal(err)
	}
	if status != "disabled" || deletedAt == nil {
		t.Fatalf("status=%s deleted=%v", status, deletedAt)
	}
}
