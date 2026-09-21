package bootstrap

// cov 波次：补齐 EnsureSQLiteJ3bModelCheck 包装层覆盖（此前整函数 0%）。
// 成功路径走真实 SQLite 文件（OpenSQLiteFile 打开，验证幂等两次 ensure），
// 失败路径用已关闭句柄注入，验证错误包装语义。
//
// 不可达清单沿用 w12g_bootstrap_errors_test.go 登记：
// bootstrap.go:163-165 OpenSQLiteFile 的 sql.Open 错误臂在测试进程内不可达。

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func TestCovEnsureSQLiteJ3bModelCheckEnsuresIdempotentAndPropagates(t *testing.T) {
	db, err := OpenSQLiteFile(filepath.Join(t.TempDir(), "j3b.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := EnsureSQLiteJ3bModelCheck(ctx, db); err != nil {
		t.Fatalf("首次 ensure 必须成功: %v", err)
	}
	// 幂等：契约已完整时第二次调用不执行写事务且必须成功。
	if err := EnsureSQLiteJ3bModelCheck(ctx, db); err != nil {
		t.Fatalf("重复 ensure 必须幂等成功: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	// 已关闭句柄：底层 PRAGMA 失败必须包装上抛。
	err = EnsureSQLiteJ3bModelCheck(ctx, db)
	if err == nil || !strings.Contains(err.Error(), "ensure J3b model-check sqlite schema") {
		t.Fatalf("已关闭句柄必须上抛且携带包装语义: %v", err)
	}
}
