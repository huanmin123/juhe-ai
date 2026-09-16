package accountcleanup

// w11g 覆盖补充：查询链错误臂（drop 注入）、撤销链阶段错误、addChanged
// 防御臂与 Cleanup 的失败聚合分支。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

var w11gStoreSeq int64

// w11gStore 复用 w7a 的 schema，但每次分配独立内存库（同一测试内可多次创建）。
func w11gStore(t *testing.T) (*Store, *sql.DB) {
	t.Helper()
	name := fmt.Sprintf("w11g-cleanup-%d-%s", atomic.AddInt64(&w11gStoreSeq, 1), strings.ReplaceAll(t.Name(), "/", "-"))
	db, err := sql.Open("sqlite", "file:"+name+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	for _, ddl := range []string{
		`CREATE TABLE accounts (
			id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL,
			authorization_instance_authorization_id TEXT,
			authorization_instance_source_account_id TEXT,
			deleted_at TEXT, deleted_by TEXT, updated_at TEXT NOT NULL, created_at TEXT NOT NULL,
			status TEXT NOT NULL, schedulable INTEGER NOT NULL, cooldown_until TEXT
		)`,
		`CREATE TABLE resource_authorizations (
			id TEXT PRIMARY KEY, resource_type TEXT NOT NULL, resource_id TEXT NOT NULL,
			resource_owner_system_account_id TEXT,
			grantee_system_account_id TEXT, status TEXT NOT NULL,
			effective_source_type TEXT, effective_source_team_id TEXT,
			revoked_by TEXT, revoked_at TEXT, revoked_reason TEXT,
			last_source_changed_at TEXT, updated_at TEXT
		)`,
		`CREATE TABLE resource_authorization_sources (
			authorization_id TEXT NOT NULL, source_type TEXT NOT NULL, source_team_id TEXT,
			status TEXT NOT NULL, ended_at TEXT, ended_reason TEXT,
			revoked_by TEXT, revoked_at TEXT, updated_at TEXT
		)`,
		`CREATE TABLE resource_authorization_grants (
			id TEXT PRIMARY KEY, resource_type TEXT NOT NULL, resource_id TEXT NOT NULL,
			resource_owner_system_account_id TEXT, grantee_type TEXT,
			grantee_system_account_id TEXT, status TEXT NOT NULL,
			revoked_by TEXT, revoked_at TEXT, updated_at TEXT
		)`,
		`CREATE TABLE group_accounts (account_id TEXT, account_authorization_id TEXT)`,
		`CREATE TABLE request_quota_hourly_window_scope_bindings (
			source_type TEXT, source_id TEXT, scope_type TEXT, scope_id TEXT
		)`,
		`CREATE TABLE account_supported_models (account_id TEXT)`,
		`CREATE TABLE account_model_mappings (account_id TEXT)`,
		`CREATE TABLE account_tag_bindings (account_id TEXT)`,
		`CREATE TABLE account_name_search_terms (account_id TEXT)`,
		`CREATE TABLE account_name_search_documents (account_id TEXT)`,
		`CREATE TABLE account_api_key_runtime_states (account_id TEXT)`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	store, err := New(db, SQLite, "", OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	if err != nil {
		t.Fatal(err)
	}
	store.now = func() time.Time { return time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC) }
	return store, db
}

func TestW11GQueryChainErrorArms(t *testing.T) {
	drop := func(t *testing.T, store *Store, db *sql.DB, statement string) {
		t.Helper()
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	// queryAuthorizations：账户侧与显式侧查询失败。
	store, db := w11gStore(t)
	drop(t, store, db, `DROP TABLE resource_authorizations`)
	if _, err := store.queryAuthorizations(ctx, []string{"a"}, nil); err == nil {
		t.Fatal("account authorization query error must propagate")
	}
	if _, err := store.queryAuthorizations(ctx, nil, []string{"auth-x"}); err == nil {
		t.Fatal("explicit authorization query error must propagate")
	}
	// activeAuthorizationInstances。
	store, db = w11gStore(t)
	drop(t, store, db, `DROP TABLE accounts`)
	if _, err := store.activeAuthorizationInstances(ctx, []string{"a"}, true); err == nil {
		t.Fatal("active instance query error must propagate")
	}
	// queryTeamSources。
	store, db = w11gStore(t)
	drop(t, store, db, `DROP TABLE resource_authorization_sources`)
	if _, err := store.queryTeamSources(ctx, []string{"a"}); err == nil {
		t.Fatal("team source query error must propagate")
	}
	// queryGrantIDs（account 侧与 instance 侧）。
	store, db = w11gStore(t)
	drop(t, store, db, `DROP TABLE resource_authorization_grants`)
	if _, err := store.queryGrantIDs(ctx, []string{"a"}, nil, false); err == nil {
		t.Fatal("grant id query error must propagate")
	}
	if _, err := store.queryGrantIDs(ctx, nil, []string{"auth-x"}, true); err == nil {
		t.Fatal("instance grant id query error must propagate")
	}
}

func TestW11GRevokeChainStageErrors(t *testing.T) {
	ctx := context.Background()
	// 软删孤儿走 revokeInstance：sources 表损坏。
	store, db := w11gStore(t)
	old := "2026-01-01T00:00:00.000Z"
	if _, err := db.Exec(`INSERT INTO accounts(id,system_account_id,authorization_instance_authorization_id,authorization_instance_source_account_id,deleted_at,updated_at,created_at,status,schedulable)
		VALUES ('orphan','sys','auth-w11g',NULL,NULL,?,?,'active',1)`, old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TABLE resource_authorization_sources`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.softDeleteOrphan(ctx, accountRow{ID: "orphan", SystemAccountID: "sys", AuthorizationInstanceAuthID: nullStringV("auth-w11g"), UpdatedAt: nullStringV(old)}); err == nil {
		t.Fatal("revoke sources error must propagate")
	}
	// 授权主表损坏。
	store, db = w11gStore(t)
	if _, err := db.Exec(`INSERT INTO accounts(id,system_account_id,authorization_instance_authorization_id,authorization_instance_source_account_id,deleted_at,updated_at,created_at,status,schedulable)
		VALUES ('orphan2','sys','auth-w11g-2',NULL,NULL,?,?,'active',1)`, old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TABLE resource_authorizations`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.softDeleteOrphan(ctx, accountRow{ID: "orphan2", SystemAccountID: "sys", AuthorizationInstanceAuthID: nullStringV("auth-w11g-2"), UpdatedAt: nullStringV(old)}); err == nil {
		t.Fatal("revoke authorizations error must propagate")
	}
	// revokeAccountAuthorizations 的 grants/quota 链（auth 指向 account 资源）。
	store, db = w11gStore(t)
	if _, err := db.Exec(`INSERT INTO accounts(id,system_account_id,authorization_instance_authorization_id,authorization_instance_source_account_id,deleted_at,updated_at,created_at,status,schedulable)
		VALUES ('orphan3','sys','auth-w11g-3',NULL,NULL,?,?,'active',1)`, old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO resource_authorizations(id,resource_type,resource_id,grantee_system_account_id,status) VALUES ('auth-w11g-3','account','orphan3','g','active')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TABLE request_quota_hourly_window_scope_bindings`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.softDeleteOrphan(ctx, accountRow{ID: "orphan3", SystemAccountID: "sys", AuthorizationInstanceAuthID: nullStringV("auth-w11g-3"), UpdatedAt: nullStringV(old)}); err == nil {
		t.Fatal("revoke grants quota error must propagate")
	}
}

func TestW11GDeleteBusinessRelationErrors(t *testing.T) {
	ctx := context.Background()
	store, db := w11gStore(t)
	seedW7ADeletedRoot(t, db)
	// 目标构建后，关系表损坏导致 deleteBusiness 阶段失败。
	target, err := store.buildCandidate(ctx, accountRow{
		ID: "root", SystemAccountID: "sys", DeletedAt: nullStringV("2026-01-05T00:00:00.000Z"), UpdatedAt: nullStringV("2026-01-05T00:00:00.000Z"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TABLE account_supported_models`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.deleteBusiness(ctx, target, "2026-02-01T00:00:00.000Z"); err == nil {
		t.Fatal("relation delete error must propagate")
	}
	// 授权删除链。
	store, db = w11gStore(t)
	seedW7ADeletedRoot(t, db)
	target, err = store.buildCandidate(ctx, accountRow{
		ID: "root", SystemAccountID: "sys", DeletedAt: nullStringV("2026-01-05T00:00:00.000Z"), UpdatedAt: nullStringV("2026-01-05T00:00:00.000Z"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TABLE resource_authorization_sources`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.deleteBusiness(ctx, target, "2026-02-01T00:00:00.000Z"); err == nil {
		t.Fatal("authorization delete error must propagate")
	}
}

func TestW11GAddChangedAndCleanupFailureArms(t *testing.T) {
	// addChanged：错误与 RowsAffected 失败直传。
	if _, err := addChanged(3, nil, errors.New("w11g exec")); err == nil {
		t.Fatal("exec error must propagate")
	}
	if _, err := addChanged(3, w7aResult{err: errors.New("w11g rows")}, nil); err == nil {
		t.Fatal("rows-affected error must propagate")
	}
	if total, err := addChanged(3, w7aResult{affected: 2}, nil); err != nil || total != 5 {
		t.Fatalf("addChanged total=%d err=%v", total, err)
	}
	// Cleanup 的 build 失败聚合：fence 失败记录到 Failures。
	store, db := w11gStore(t)
	seedW7ADeletedRoot(t, db)
	if _, err := db.Exec(`DROP TABLE resource_authorization_sources`); err != nil {
		t.Fatal(err)
	}
	result, err := store.Cleanup(context.Background(), CleanupInput{CutoffDeletedAt: "2026-02-01T00:00:00.000Z"})
	if err != nil {
		t.Fatalf("cleanup err=%v", err)
	}
	if result.Attempted < 1 || len(result.Failures) == 0 {
		t.Fatalf("result=%+v", result)
	}
}
