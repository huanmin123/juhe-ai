package uibootstrap

import "testing"

// ISSUE-005 同类回归：`?` 必须改写为 $n，否则占位符直达 pgx
// 触发 42601（2026-09-25 生产 my-ui-bootstrap 500，failureReason 定位）。
func TestBindRewritesPlaceholdersForPG(t *testing.T) {
	d := &Deps{}
	got := d.bind("WHERE a = ? AND b = ? ORDER BY c")
	if got != "WHERE a = $1 AND b = $2 ORDER BY c" {
		t.Fatalf("bind = %q", got)
	}
}
