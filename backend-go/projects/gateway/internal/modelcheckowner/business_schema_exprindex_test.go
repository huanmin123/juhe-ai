package modelcheckowner

// 回归测试：表达式唯一索引（真实维护 DDL 的 lower(username) 大小写
// 不敏感唯一约束）不得把 schemaReady 检查打成硬错误。历史缺陷：唯一
// 约束验证遍历表上全部唯一索引时，读到表达式列的 NULL 名直接返回
// "index contains an expression"，导致满足契约（UNIQUE(username) 表约束
// 的 sqlite_autoindex 仍在）的合法库被拒。

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func newExpressionIndexRegressionDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	statements := []string{
		// 与真实维护 DDL 对齐：表内 UNIQUE(username) 产生 sqlite_autoindex，
		// 外加两个表达式唯一索引。
		`CREATE TABLE system_accounts (
			id TEXT PRIMARY KEY,
			username TEXT NOT NULL,
			display_name TEXT NOT NULL,
			status TEXT NOT NULL,
			role TEXT NOT NULL,
			must_change_password INTEGER NOT NULL,
			password_hash TEXT NOT NULL,
			last_login_at TEXT,
			updated_at TEXT NOT NULL,
			UNIQUE(username)
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_system_accounts_username_unique_lower ON system_accounts(lower(username))`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_system_accounts_display_name_unique_lower ON system_accounts(lower(display_name))`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("seed %q: %v", statement, err)
		}
	}
	return db
}

// TestSQLiteSchemaHasUniqueConstraintToleratesExpressionIndexes：表达式唯一
// 索引只是"非候选"，不阻断普通列唯一约束的验证。
func TestSQLiteSchemaHasUniqueConstraintToleratesExpressionIndexes(t *testing.T) {
	db := newExpressionIndexRegressionDB(t)
	ok, observed, err := sqliteSchemaHasUniqueConstraint(context.Background(), db, "system_accounts", []string{"username"})
	if err != nil {
		t.Fatalf("表达式唯一索引不得触发硬错误: %v", err)
	}
	if !ok {
		t.Fatalf("UNIQUE(username) 应被 sqlite_autoindex 满足, observed=%v", observed)
	}
}

// TestSQLiteSchemaHasUniqueConstraintStillRejectsMissing：修复不得弱化契约
// ——没有普通列唯一约束时仍须报告缺失。
func TestSQLiteSchemaHasUniqueConstraintStillRejectsMissing(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TABLE t (id TEXT PRIMARY KEY, name TEXT NOT NULL)`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// 只有表达式唯一索引，没有普通列唯一约束 → 不满足。
	if _, err := db.Exec(`CREATE UNIQUE INDEX idx_t_name_lower ON t(lower(name))`); err != nil {
		t.Fatalf("seed index: %v", err)
	}
	ok, observed, err := sqliteSchemaHasUniqueConstraint(context.Background(), db, "t", []string{"name"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatalf("表达式索引不能替代普通列唯一约束, observed=%v", observed)
	}
}
