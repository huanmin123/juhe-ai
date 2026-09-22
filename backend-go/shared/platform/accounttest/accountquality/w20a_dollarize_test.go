package accountquality

import (
	"strings"
	"testing"
)

// w20a：PG 模式必须把顺序 ? 改写为 $n（pgx stdlib 不改写，直发即 42601）；
// SQLite 模式原样透传。
func TestW20AStatsStoreDollarize(t *testing.T) {
	pg := &StatsStore{mode: StatsPostgres}
	got := pg.dollarize("SELECT a FROM t WHERE b = ? AND c = ? LIMIT ?")
	want := "SELECT a FROM t WHERE b = $1 AND c = $2 LIMIT $3"
	if got != want {
		t.Fatalf("PG 改写=%q want=%q", got, want)
	}
	if strings.ContainsRune(got, '?') {
		t.Fatalf("PG 改写后不得残留 ?: %q", got)
	}
	sql := &StatsStore{mode: StatsSQLite}
	if passthrough := "SELECT a FROM t WHERE b = ? LIMIT ?"; sql.dollarize(passthrough) != passthrough {
		t.Fatalf("SQLite 必须透传: %q", sql.dollarize(passthrough))
	}
}
