package accounthealth

import (
	"testing"
)

// w12d_drain_store_faults_test.go 覆盖 w12d 波次的 store 配置错误臂。
// 注：原借 SQLite store 载体的 outbox drain 消费臂、closed store 上抛臂与
// duplicate 幂等臂随 sqlite 臂退役（drain 逻辑的脚本化 Mock 用例一并退役，
// PG 载体回归由存储迁移批次重建）。

// TestW12dStoreConfigFaultArms 覆盖 OpenStore 配置参数错误臂。
func TestW12dStoreConfigFaultArms(t *testing.T) {
	if _, err := OpenStore(StoreConfig{Mode: StoreMode("redis")}); err == nil {
		t.Fatal("unknown mode must fail")
	}
	if _, err := OpenStore(StoreConfig{Mode: StorePostgres}); err == nil {
		t.Fatal("missing postgres url must fail")
	}
	if _, err := OpenStore(StoreConfig{Mode: StorePostgres, PostgresURL: "postgres://w12d/x", PostgresMaxOpenConns: 2, PostgresMaxIdleConns: 4}); err == nil {
		t.Fatal("idle > open must fail")
	}
}

// TestW12dClosedStoreNilSafety 覆盖 nil store Close 幂等。
func TestW12dClosedStoreNilSafety(t *testing.T) {
	var store *Store
	if err := store.Close(); err != nil {
		t.Fatalf("nil close: %v", err)
	}
}
