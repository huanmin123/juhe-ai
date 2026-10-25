package main

// B3（SQLite 与内存模式彻底退役）共享 PG 测试夹具：余额探测族组合根运行态
// 的隔离 PG 夹具。pgtestkit 提供每测试独立库，sqldialect 包装保留测试内
// `?` 占位符写法（与生产 pgpool 打开路径同款改写语义）。连接串不落日志。

import (
	"context"
	"database/sql"
	"testing"

	"github.com/jackc/pgx/v5/stdlib"
	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
	"github.com/huanminabc/juhe-ai/backend-go-platform/sqldialect"
)

// wgOpenPGDialectDB 打开一个隔离 PG 测试库并套 sqldialect 改写（`?`→`$n`，
// 与生产 pgpool openDriver 同语义）；prepare 在独立库上注入 fresh DDL。
// 返回被改写的 DB 与裸 DSN（后者供需要自管连接的 store 构造注入 env/URL）。
func wgOpenPGDialectDB(t *testing.T, prepare func(ctx context.Context, db *sql.DB) error) (*sql.DB, string) {
	t.Helper()
	if prepare == nil {
		prepare = func(context.Context, *sql.DB) error { return nil }
	}
	testDB := pgtestkit.NewTestDatabase(t, prepare)
	db, err := sqldialect.OpenDB(stdlib.GetDefaultDriver(), testDB.DSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, testDB.DSN
}
