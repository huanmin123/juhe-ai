package proxyprofiles

// P0 修复回归：Delete 的使用守卫与 DELETE 必须在同一事务内（Node 归档
// deleteProxyForManagementAsync：SQLite BEGIN IMMEDIATE / PG 事务内
// SELECT ... FOR UPDATE）。原实现是两条自动提交语句，竞态窗口内账号 patch
// 绑定该 profile 后删除成功，账号落入悬空引用。

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

func TestProxyDeleteInUseLeavesProfileUntouched(t *testing.T) {
	fixture := newProxyFixture(t)
	fixture.seedProfile(t, "p-1", "2026-09-01T00:00:00.000Z", "unknown")
	if _, err := fixture.db.Exec(`INSERT INTO accounts (id, name, proxy_profile_id) VALUES ('a-1', '账户A', 'p-1'), ('a-2', '账户B', 'p-1')`); err != nil {
		t.Fatalf("seed accounts: %v", err)
	}
	_, err := fixture.store.Delete(context.Background(), "p-1")
	var inUse *InUseError
	if err == nil || !asInUse(err, &inUse) {
		t.Fatalf("in-use not detected: %v", err)
	}
	if inUse.AccountCount != 2 || len(inUse.AccountNames) != 2 {
		t.Fatalf("in-use=%+v", inUse)
	}
	// 守卫拒绝时不得删除任何东西。
	var count int
	if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM proxy_profiles WHERE id = 'p-1'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("profile rows=%d, guard rejection must keep the profile", count)
	}
}

func TestProxyDeleteIgnoresSoftDeletedAccounts(t *testing.T) {
	fixture := newProxyFixture(t)
	fixture.seedProfile(t, "p-1", "2026-09-01T00:00:00.000Z", "unknown")
	if _, err := fixture.db.Exec(`INSERT INTO accounts (id, name, deleted_at, proxy_profile_id) VALUES ('a-gone', '已删账户', '2026-09-02T00:00:00.000Z', 'p-1')`); err != nil {
		t.Fatalf("seed deleted account: %v", err)
	}
	name, err := fixture.store.Delete(context.Background(), "p-1")
	if err != nil || name != "代理-p-1" {
		t.Fatalf("delete wrong: %q %v", name, err)
	}
	// 软删账号不参与守卫（deleted_at IS NULL 谓词与 Node 一致）。
	var count int
	if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM proxy_profiles WHERE id = 'p-1'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("profile rows=%d, delete must remove the profile", count)
	}
}

func TestProxyDeleteLoadsProfileWithRowLockOnPG(t *testing.T) {
	// PG 方言下删除临界区的 profile 读取必须带 FOR UPDATE（Node
	// client.transaction 的行锁语义）；SQLite 单 writer 无锁后缀。
	sqliteDB, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer sqliteDB.Close()
	pgStore, err := NewStore(Deps{DB: sqliteDB, PGDialect: true, Secret: "s", Now: time.Now, NewID: func(string) string { return "x" }})
	if err != nil {
		t.Fatal(err)
	}
	if got := pgStore.rowLockClause(); got != " FOR UPDATE" {
		t.Fatalf("pg lock clause=%q", got)
	}
	sqliteStore, err := NewStore(Deps{DB: sqliteDB, PGDialect: false, Secret: "s", Now: time.Now, NewID: func(string) string { return "x" }})
	if err != nil {
		t.Fatal(err)
	}
	if got := sqliteStore.rowLockClause(); got != "" {
		t.Fatalf("sqlite lock clause=%q", got)
	}
}
