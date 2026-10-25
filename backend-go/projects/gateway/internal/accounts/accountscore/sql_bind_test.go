package accountscore

import "testing"

// 2026-09-25 生产 my-accounts keyword 搜索 500 修复的方言契约：
// PG 分支必须同时完成 ?→$N 与 instr(→strpos( 两类改写；SQLite 分支原样返回。
func TestSQLBindRewritesInstrToStrposOnPostgres(t *testing.T) {
	query := "SELECT * FROM t WHERE instr(documents.normalized_name, ?) > 0 AND name LIKE ?"

	got := SQLBind(query)
	want := "SELECT * FROM t WHERE strpos(documents.normalized_name, $1) > 0 AND name LIKE $2"
	if got != want {
		t.Fatalf("PG 改写不符:\n got=%s\nwant=%s", got, want)
	}

}

func TestSQLBindWithoutInstrKeepsPlaceholderOnlyRewrite(t *testing.T) {
	got := SQLBind("SELECT 1 WHERE a = ? AND b = ?")
	if got != "SELECT 1 WHERE a = $1 AND b = $2" {
		t.Fatalf("PG 占位符改写不符: %s", got)
	}
}
