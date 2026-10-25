package accountquality

import (
	"strings"
	"testing"
)

// w20a：store 必须把顺序 ? 改写为 $n（pgx stdlib 不改写，直发即 42601）。
// 2026-10-25 sqlite 臂退役：原 SQLite 透传断言随 StatsSQLite 臂删除。
func TestW20AStatsStoreDollarize(t *testing.T) {
	store := &StatsStore{}
	got := store.dollarize("SELECT a FROM t WHERE b = ? AND c = ? LIMIT ?")
	want := "SELECT a FROM t WHERE b = $1 AND c = $2 LIMIT $3"
	if got != want {
		t.Fatalf("PG 改写=%q want=%q", got, want)
	}
	if strings.ContainsRune(got, '?') {
		t.Fatalf("PG 改写后不得残留 ?: %q", got)
	}
}
