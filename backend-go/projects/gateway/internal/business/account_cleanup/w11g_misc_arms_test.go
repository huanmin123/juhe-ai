package accountcleanup

// w11g 覆盖补充（第七批）：取消上下文的孤儿循环中断、Postgres 锁定子句
// 求值、团队授权账户回退与 grant 配额阶段的构建顺序。

import (
	"context"
	"testing"
)

func TestW11GCleanupCancelledDuringOrphanLoop(t *testing.T) {
	store, db := w11gStore(t)
	if _, err := db.Exec(`INSERT INTO accounts(id,system_account_id,authorization_instance_authorization_id,authorization_instance_source_account_id,deleted_at,updated_at,created_at,status,schedulable)
		VALUES ('orphan-cc','sys','auth-cc',NULL,NULL,?,?,'active',1)`, w11gOldTime, w11gOldTime); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.Cleanup(ctx, CleanupInput{CutoffDeletedAt: "2026-02-01T00:00:00.000Z"}); err == nil {
		t.Fatal("cancelled context must abort orphan loop")
	}
}

func TestW11GSoftDeletePostgresLockClauseEvaluation(t *testing.T) {
	store, db := w11gStore(t)
	_ = store
	// Postgres 方言：FOR UPDATE 追加后 bind 占位符在 SQLite 上失败，
	// 但锁定子句分支已被求值。
	pgStore, err := New(db, Postgres, "", OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pgStore.softDeleteOrphan(context.Background(), accountRow{
		ID: "x", SystemAccountID: "sys",
		AuthorizationInstanceAuthID: nullStringV("a"), UpdatedAt: nullStringV(w11gOldTime),
	}); err == nil {
		t.Fatal("postgres dialect on sqlite must fail the locked select")
	}
}

func TestW11GBuildCandidateTeamFallbackArm(t *testing.T) {
	store, db := w11gStore(t)
	old := "2026-01-05T00:00:00.000Z"
	if _, err := db.Exec(`INSERT INTO accounts(id,system_account_id,authorization_instance_authorization_id,authorization_instance_source_account_id,deleted_at,updated_at,created_at,status,schedulable)
		VALUES ('root-tf','sys',NULL,NULL,?,?,?,'disabled',0)`, old, old, old); err != nil {
		t.Fatal(err)
	}
	// 团队授权 sources 指向 root 的真实授权：team 作用域进入目标。
	if _, err := db.Exec(`INSERT INTO resource_authorizations(id,resource_type,resource_id,grantee_system_account_id,status) VALUES ('auth-tf','account','root-tf','g','revoked')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO resource_authorization_sources(authorization_id,source_type,source_team_id,status) VALUES ('auth-tf','manual','team-w11g','active')`); err != nil {
		t.Fatal(err)
	}
	target, err := store.buildCandidate(context.Background(), accountRow{
		ID: "root-tf", SystemAccountID: "sys",
		DeletedAt: nullStringV(old), UpdatedAt: nullStringV(old),
	})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, id := range target.TeamScopeIDs {
		if id == "root-tf:team-w11g" {
			found = true
		}
	}
	if !found {
		t.Fatalf("team scope ids=%v", target.TeamScopeIDs)
	}
}

func TestW11GDeleteBusinessGrantQuotaAfterBuild(t *testing.T) {
	ctx := context.Background()
	store, db := w11gStore(t)
	seedW7ADeletedRoot(t, db)
	// grant 先落库再构建目标（确保 grantIDs 进入目标集合）。
	if _, err := db.Exec(`INSERT INTO resource_authorization_grants(id,resource_type,resource_id,status) VALUES ('grant-q2','account','root','active')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO request_quota_hourly_window_scope_bindings(source_type,source_id,scope_type,scope_id) VALUES ('resource_authorization_grant','grant-q2','other_scope','auth-other')`); err != nil {
		t.Fatal(err)
	}
	target, err := w11gBuildRootTarget(t, store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TRIGGER w11g_fail_gq BEFORE DELETE ON request_quota_hourly_window_scope_bindings
		WHEN OLD.source_type = 'resource_authorization_grant'
		BEGIN SELECT RAISE(ABORT, 'w11g gq'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.deleteBusiness(ctx, target, "2026-02-01T00:00:00.000Z"); err == nil {
		t.Fatal("grant quota stage error must propagate")
	}
}

func TestW11GRevokeAccountAuthorizationUpdateError(t *testing.T) {
	ctx := context.Background()
	store, db := w11gStore(t)
	if _, err := db.Exec(`INSERT INTO accounts(id,system_account_id,authorization_instance_authorization_id,authorization_instance_source_account_id,deleted_at,updated_at,created_at,status,schedulable)
		VALUES ('orphan-au','sys','auth-au',NULL,NULL,?,?,'active',1)`, w11gOldTime, w11gOldTime); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO resource_authorizations(id,resource_type,resource_id,grantee_system_account_id,status) VALUES ('auth-au','account','orphan-au','g','active')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TRIGGER w11g_fail_au BEFORE UPDATE ON resource_authorizations
		BEGIN SELECT RAISE(ABORT, 'w11g au'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.softDeleteOrphan(ctx, accountRow{
		ID: "orphan-au", SystemAccountID: "sys",
		AuthorizationInstanceAuthID: nullStringV("auth-au"), UpdatedAt: nullStringV(w11gOldTime),
	}); err == nil {
		t.Fatal("account authorization update error must propagate")
	}
}

func TestW11GQueryAuthorizationsSortComparator(t *testing.T) {
	store, db := w11gStore(t)
	old := "2026-01-05T00:00:00.000Z"
	if _, err := db.Exec(`INSERT INTO accounts(id,system_account_id,authorization_instance_authorization_id,authorization_instance_source_account_id,deleted_at,updated_at,created_at,status,schedulable)
		VALUES ('root-sc','sys',NULL,NULL,?,?,?,'disabled',0)`, old, old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO resource_authorizations(id,resource_type,resource_id,grantee_system_account_id,status)
		VALUES ('auth-b','account','root-sc','g','revoked'),('auth-a','account','root-sc','g','revoked')`); err != nil {
		t.Fatal(err)
	}
	rows, err := store.queryAuthorizations(context.Background(), []string{"root-sc"}, nil)
	if err != nil || len(rows) != 2 || rows[0].ID != "auth-a" {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
}

func TestW11GBuildInstanceCandidateActiveInstances(t *testing.T) {
	store, db := w11gStore(t)
	old := "2026-01-05T00:00:00.000Z"
	if _, err := db.Exec(`INSERT INTO accounts(id,system_account_id,authorization_instance_authorization_id,authorization_instance_source_account_id,deleted_at,updated_at,created_at,status,schedulable)
		VALUES ('root-ia','sys',NULL,NULL,?,?,?,'disabled',0),('child-ia','sys','auth-ia','root-ia',?,?,?,'disabled',0)`, old, old, old, old, old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO resource_authorizations(id,resource_type,resource_id,grantee_system_account_id,status) VALUES ('auth-ia','account','root-ia','g','revoked')`); err != nil {
		t.Fatal(err)
	}
	target, err := store.buildCandidate(context.Background(), accountRow{
		ID: "child-ia", SystemAccountID: "sys",
		AuthorizationInstanceAuthID:   nullStringV("auth-ia"),
		AuthorizationInstanceSourceID: nullStringV("root-ia"),
		DeletedAt:                     nullStringV(old), UpdatedAt: nullStringV(old),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(target.AuthorizationIDs) != 1 || target.AuthorizationIDs[0] != "auth-ia" {
		t.Fatalf("target=%+v", target.AuthorizationIDs)
	}
}
