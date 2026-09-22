// Tests for the usage shard DDL mirror. The DDL is a byte-for-byte copy of the
// jobs-side authority (projects/jobs/internal/usagewriter/store.go), so the
// invariant is checked against the jobs source text instead of a hand-kept
// snapshot: a one-sided edit on either project must fail here.
//
// The maintenance module cannot import jobs (Go 三项目架构基线)，so the
// comparison reads the source file through a path relative to this package
// directory (backend-go/projects/maintenance/internal/schema ->
// backend-go/projects/jobs/internal/usagewriter/store.go).

package schema

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

const usageWriterStoreSource = "../../../../projects/jobs/internal/usagewriter/store.go"

// usageWriterDDLLiteral extracts the backtick literal assigned to name in the
// jobs usagewriter source. The DDL contains no backtick, so the first backtick
// after the assignment terminates the literal.
func usageWriterDDLLiteral(source, name string) (string, bool) {
	marker := "const " + name + " = `"
	start := strings.Index(source, marker)
	if start < 0 {
		return "", false
	}
	start += len(marker)
	end := strings.IndexByte(source[start:], '`')
	if end < 0 {
		return "", false
	}
	return source[start : start+end], true
}

// ddlDifference points at the first divergent byte offset so the failure text
// names the actually edited column or index instead of the whole script.
func ddlDifference(want, got string) string {
	limit := len(want)
	if len(got) < limit {
		limit = len(got)
	}
	for i := 0; i < limit; i++ {
		if want[i] != got[i] {
			return "first difference at byte offset " + strconvItoa(i)
		}
	}
	if len(want) != len(got) {
		return "one side is a prefix of the other (length " + strconvItoa(len(got)) + " vs " + strconvItoa(len(want)) + ")"
	}
	return "equal"
}

func strconvItoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := ""
	for value > 0 {
		digits = string(rune('0'+value%10)) + digits
		value /= 10
	}
	return digits
}

// readUsageWriterSource reads the jobs authority as text; the maintenance
// module cannot import jobs, so the mirror is enforced on source text.
func readUsageWriterSource(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func TestSQLiteUsageShardDDLMirrorsJobsSource(t *testing.T) {
	if _, err := readUsageWriterSource(filepath.Join("..", "..", "..", "..", "missing-store.go")); err == nil {
		t.Fatal("missing source should fail to read")
	}
	raw, err := readUsageWriterSource(usageWriterStoreSource)
	if err != nil {
		t.Skipf("jobs usagewriter source not readable from this checkout (%v); mirror invariant cannot be enforced here", err)
	}
	want, ok := usageWriterDDLLiteral(raw, "UsageShardBaseSchemaSQL")
	if !ok {
		t.Fatalf("jobs usagewriter source no longer declares the UsageShardBaseSchemaSQL backtick literal; re-sync %s", usageWriterStoreSource)
	}
	if sqliteUsageShardBaseDDL != want {
		t.Fatalf("usage shard DDL diverged from %s (%s): maintenance bytes=%d jobs bytes=%d",
			usageWriterStoreSource, ddlDifference(want, sqliteUsageShardBaseDDL), len(sqliteUsageShardBaseDDL), len(want))
	}
}

// TestEnsureSQLiteUsageShardCreatesJobCompatibleSchema runs the copied DDL
// against a real SQLite file and pins the resulting object set, so a syntax
// drift that still compares equal to a broken jobs literal is caught by
// execution rather than by text comparison alone.
func TestEnsureSQLiteUsageShardCreatesJobCompatibleSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shard.sqlite3")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=rwc&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	counts, err := EnsureSQLiteUsageShard(context.Background(), db)
	if err != nil {
		t.Fatalf("ensure usage shard schema: %v", err)
	}
	if counts.Tables != 1 || counts.Indexes != 10 {
		t.Fatalf("counts = %+v, want 1 table and 10 indexes", counts)
	}
	var columns int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('usage_records')`).Scan(&columns); err != nil {
		t.Fatal(err)
	}
	if columns != 62 {
		t.Fatalf("usage_records columns = %d, want 62", columns)
	}
	// 幂等：重复 ensure 不改计数也不报错（legacy index drop 在新库上是 no-op）。
	if _, err := EnsureSQLiteUsageShard(context.Background(), db); err != nil {
		t.Fatalf("second ensure: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO usage_records (id, system_account_id, trace_id, traffic_source, created_at) VALUES ('r1','s1','t1','gateway','2026-01-01T00:00:00.000Z')`); err != nil {
		t.Fatalf("insert into copied schema: %v", err)
	}
}
