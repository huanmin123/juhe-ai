package uibootstrap

import "testing"

// ISSUE-005 同类回归：PG 方言下 `?` 必须改写为 $n，否则占位符直达 pgx
// 触发 42601（2026-09-25 生产 my-ui-bootstrap 500，failureReason 定位）。
func TestBindRewritesPlaceholdersForPG(t *testing.T) {
	d := &Deps{PGDialect: true}
	got := d.bind("WHERE a = ? AND b = ? ORDER BY c")
	if got != "WHERE a = $1 AND b = $2 ORDER BY c" {
		t.Fatalf("bind(pg) = %q", got)
	}
	sqlite := &Deps{}
	if unchanged := "WHERE a = ? ORDER BY c"; sqlite.bind(unchanged) != unchanged {
		t.Fatalf("sqlite dialect must keep placeholders verbatim")
	}
}
