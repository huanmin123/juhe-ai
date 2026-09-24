package statreads

import "testing"

// TestDepsBindPlaceholderRewrite 锁定 PG 模式 ?→$n 绑定（2026-09-25 生产
// usage-records/usage-overview 500 的修复）：SQLite/关闭 PGDialect 时原样返回，
// PG 模式按出现顺序重写为 $1..$n；非占位符问号（如字面量）不受影响场景由调用方
// SQL 保证（本包无 jsonb ? 运算符用法，见 2026-09-25 扫描记录）。
func TestDepsBindPlaceholderRewrite(t *testing.T) {
	d := &Deps{PGDialect: true}
	got := d.bind("SELECT a FROM t WHERE b = ? AND c = ? LIMIT ?")
	want := "SELECT a FROM t WHERE b = $1 AND c = $2 LIMIT $3"
	if got != want {
		t.Fatalf("bind(pg) = %q want %q", got, want)
	}
	if got := d.bind("SELECT 1"); got != "SELECT 1" {
		t.Fatalf("bind(pg) no-op query = %q", got)
	}
	sqlite := &Deps{PGDialect: false}
	if got := sqlite.bind("SELECT a FROM t WHERE b = ?"); got != "SELECT a FROM t WHERE b = ?" {
		t.Fatalf("bind(sqlite) must keep ? got %q", got)
	}
}
