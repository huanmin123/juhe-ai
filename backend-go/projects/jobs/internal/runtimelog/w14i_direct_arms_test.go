package runtimelog

import (
	"context"
	"testing"
	"time"
)

// w14i_direct_arms_test.go 用直接调用与取消 ctx 覆盖防御性错误臂：
//   - EnsureSchema 的 schema 执行错误臂（query_only 连接）；
//   - ensureSQLiteFenceTokenColumn / checkSQLiteColumns / insertSQLiteRecords
//     的取消 ctx 错误臂；
//   - decrementSQLiteFacets 空行集与取消 ctx 错误臂；
//   - decrementPostgresFacets 空行集错误臂（pgFake）。

func TestW14iDecrementPostgresFacetsEmptyRows(t *testing.T) {
	server := newPGFakeServer(t)
	w14iRegisterHappyCleanup(t, server)
	store := openFakePostgresStore(t, server)
	tx, err := beginPostgresTx(context.Background(), store.pool)
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackPostgresTx(tx)
	if err := decrementPostgresFacets(context.Background(), tx, nil, time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("空行集不应报错: %v", err)
	}
}

