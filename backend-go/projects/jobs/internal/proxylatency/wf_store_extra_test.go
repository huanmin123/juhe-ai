package proxylatency

import (
	"context"
	"database/sql/driver"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-contracts"
)

// 本文件补齐 Store 的剩余分支：OpenStore 装配、EnsureSchema SQLite 路径、
// schema 契约检查的各负向变体，以及 SQLite 租约的丢失语义。

func TestWFOpenStoreBranches(t *testing.T) {
	if _, err := OpenStore(StoreConfig{Mode: StoreSQLite, DatabasePath: ""}); err == nil {
		t.Fatal("缺 sqlite 路径必须拒绝")
	}
	if _, err := OpenStore(StoreConfig{Mode: StoreMode("bogus")}); err == nil {
		t.Fatal("非法 mode 必须拒绝")
	}
	if _, err := OpenStore(StoreConfig{Mode: StorePostgres}); err == nil {
		t.Fatal("缺 PG URL 必须拒绝")
	}
	if _, err := OpenStore(StoreConfig{Mode: StorePostgres, PostgresURL: "postgres://x", PostgresMaxOpenConns: 1, PostgresMaxIdleConns: 2}); err == nil {
		t.Fatal("idle > open 必须拒绝")
	}
	// 默认连接池上限不满足共享校验（1 <= idle <= min(open,10)）→ fail closed。
	if _, err := OpenStore(StoreConfig{Mode: StorePostgres, PostgresURL: "postgres://jobs:x@127.0.0.1:5432/j"}); err == nil {
		t.Fatal("默认超限池必须拒绝")
	}
	// 合法 PG 配置：连接惰性建立，不拨号即可打开与关闭。
	store, err := OpenStore(StoreConfig{Mode: StorePostgres, PostgresURL: "postgres://jobs:x@127.0.0.1:5432/j?sslmode=disable", PostgresMaxOpenConns: 4, PostgresMaxIdleConns: 2})
	if err != nil {
		t.Fatalf("PG 打开失败: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("PG Close 失败: %v", err)
	}
	var nilStore *Store
	if err := nilStore.Close(); err != nil {
		t.Fatalf("nil Close 失败: %v", err)
	}
}

func TestWFEnsureSchemaSQLite(t *testing.T) {
	ctx := context.Background()
	// 全新文件：EnsureSchema 装配完整 SQLite schema 后可再次执行（IF NOT EXISTS）。
	store, err := OpenStore(StoreConfig{Mode: StoreSQLite, DatabasePath: filepath.Join(t.TempDir(), "wf-ensure.sqlite3")})
	if err != nil {
		t.Fatalf("OpenStore 失败: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatalf("EnsureSchema 失败: %v", err)
	}
	if err := store.CheckSchema(context.Background()); err != nil {
		t.Fatalf("CheckSchema 失败: %v", err)
	}
	// 缺对象时 fail closed。
	if _, err := store.db.Exec(`DROP TABLE proxy_latency_execution_claims`); err != nil {
		t.Fatalf("准备缺表失败: %v", err)
	}
	err = store.CheckSchema(context.Background())
	if err == nil || !strings.Contains(err.Error(), "缺少对象") {
		t.Fatalf("缺对象 err=%v", err)
	}
	if _, _, err := store.AcquireOwnerLease(ctx, "wf-x", time.Minute); err == nil {
		t.Fatal("缺对象后租约操作必须 fail closed")
	}
}

func TestWFStoreSQLiteLeaseLostPaths(t *testing.T) {
	store, err := OpenStore(StoreConfig{Mode: StoreSQLite, DatabasePath: filepath.Join(t.TempDir(), "wf-lease.sqlite3")})
	if err != nil {
		t.Fatalf("OpenStore 失败: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	if _, _, err := store.AcquireOwnerLease(ctx, "", time.Minute); err == nil {
		t.Fatal("空 owner 必须拒绝")
	}
	owner, ok, err := store.AcquireOwnerLease(ctx, "wf-lease", time.Minute)
	if err != nil || !ok {
		t.Fatalf("owner lease ok=%v err=%v", ok, err)
	}
	if err := store.VerifyOwnerLease(ctx, owner); err != nil {
		t.Fatalf("VerifyOwnerLease 失败: %v", err)
	}
	proxy, ok, err := store.AcquireProxyLease(ctx, owner, "p-lease", time.Minute)
	if err != nil || !ok {
		t.Fatalf("proxy lease ok=%v err=%v", ok, err)
	}
	// 双重释放：第二次必然命中"已丢失"分支。
	if err := store.ReleaseProxyLease(ctx, proxy); err != nil {
		t.Fatalf("ReleaseProxyLease 失败: %v", err)
	}
	if err := store.ReleaseProxyLease(ctx, proxy); !errors.Is(err, ErrProxyLeaseLost) {
		t.Fatalf("proxy 双重释放 err=%v", err)
	}
	if err := store.ReleaseOwnerLease(ctx, owner); err != nil {
		t.Fatalf("ReleaseOwnerLease 失败: %v", err)
	}
	if err := store.ReleaseOwnerLease(ctx, owner); !errors.Is(err, ErrOwnerLeaseLost) {
		t.Fatalf("owner 双重释放 err=%v", err)
	}
	if err := store.ReleaseOwnerLease(ctx, OwnerLease{OwnerID: "x", FenceToken: 0}); err == nil {
		t.Fatal("非法 fence 必须拒绝")
	}
	if err := store.RenewOwnerLease(ctx, owner, time.Minute); !errors.Is(err, ErrOwnerLeaseLost) {
		t.Fatalf("释放后续租必须报丢失 err=%v", err)
	}
	if err := store.ReleaseProxyLease(ctx, ProxyLease{ProxyID: "", OwnerID: "x", FenceToken: 1}); err == nil {
		t.Fatal("非法 proxy 释放参数必须拒绝")
	}
}

func TestWFStorePGSchemaNegativeVariants(t *testing.T) {
	buildStore := func(t *testing.T) (*wfRecorder, *Store) {
		rec := newWFRecorder()
		db := wfOpenRecorderDB(t, rec)
		return rec, &Store{db: db, mode: StorePostgres, postgresSchemaReady: false}
	}
	ctx := context.Background()

	// 列可空性不兼容（NOT NULL 列实际可空）。
	{
		rec, store := buildStore(t)
		var rows [][]driver.Value
		for _, table := range contracts.J3AProxyLatencyTables {
			for column, spec := range contracts.J3AProxyLatencyColumns[table] {
				nullable := "NO"
				if spec.Nullable {
					nullable = "YES"
				}
				if table == "proxy_latency_owner_leases" && column == "lease_key" {
					nullable = "YES"
				}
				rows = append(rows, []driver.Value{table, column, spec.DataType, spec.UdtName, nullable})
			}
		}
		rec.script("FROM pg_namespace WHERE nspname='juhe_jobs'", []string{"owner", "current"}, [][]driver.Value{{"role", "role"}})
		tableRows := make([][]driver.Value, 0, len(contracts.J3AProxyLatencyTables))
		for _, table := range contracts.J3AProxyLatencyTables {
			tableRows = append(tableRows, []driver.Value{table})
		}
		rec.script("FROM information_schema.tables WHERE table_schema='juhe_jobs'", []string{"t"}, tableRows)
		rec.script("FROM information_schema.columns WHERE table_schema='juhe_jobs'", []string{"t", "c", "d", "u", "n"}, rows)
		if err := store.CheckSchema(ctx); err == nil || !strings.Contains(err.Error(), "列定义不兼容") {
			t.Fatalf("可空性不兼容 err=%v", err)
		}
	}
	// 约束缺失（空结果先注册，先于完整夹具被消费）。
	{
		rec, store := buildStore(t)
		rec.script("FROM pg_constraint AS c", []string{"relname", "def"}, nil)
		wfScriptSchema(t, rec)
		if err := store.CheckSchema(ctx); err == nil || !strings.Contains(err.Error(), "constraint 不兼容") {
			t.Fatalf("约束缺失 err=%v", err)
		}
	}
	// 列查询失败。
	{
		rec, store := buildStore(t)
		wfScriptSchema(t, rec)
		rec.failQuery("FROM information_schema.columns", errors.New("boom"))
		if err := store.CheckSchema(ctx); err == nil || !strings.Contains(err.Error(), "boom") {
			t.Fatalf("列查询失败 err=%v", err)
		}
	}
	// 索引查询失败。
	{
		rec, store := buildStore(t)
		wfScriptSchema(t, rec)
		rec.failQuery("FROM pg_indexes", errors.New("boom"))
		if err := store.CheckSchema(ctx); err == nil {
			t.Fatal("索引查询失败必须传播")
		}
	}
}
