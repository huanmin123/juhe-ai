package mockdata

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// 固定时钟让 ISO 时间断言可复现。
var autofillTestNow = time.Date(2026, 9, 22, 10, 30, 0, 0, time.UTC)

func TestAutofillFillsStructureValidPlaceholderRows(t *testing.T) {
	e := testEnv(t)
	createTestTable(t, e, StoreBusiness, `CREATE TABLE sample_records (
		id TEXT PRIMARY KEY,
		account_id TEXT NOT NULL,
		created_at TEXT NOT NULL,
		status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled', 'revoked')),
		attempts INTEGER NOT NULL,
		ratio REAL,
		payload_json TEXT NOT NULL,
		checksum TEXT NOT NULL,
		client_ip TEXT NOT NULL,
		email TEXT NOT NULL,
		optional_note TEXT,
		notes_json TEXT
	)`)
	inserted, skipped, err := autofillTables(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	if inserted[StoreBusiness+".sample_records"] < 1 {
		t.Fatalf("sample_records not filled: %v (skipped %v)", inserted, skipped)
	}
	if _, ok := skipped[StoreBusiness+".sample_records"]; ok {
		t.Fatalf("sample_records should not be skipped: %v", skipped[StoreBusiness+".sample_records"])
	}
	count, err := e.queryCount(context.Background(), StoreBusiness, "sample_records")
	if err != nil {
		t.Fatal(err)
	}
	if count < 1 || count > 3 {
		t.Fatalf("placeholder rows = %d, want 1..3", count)
	}
	var (
		id, accountID, createdAt, status, payload string
		attempts                                  int
		checksum, clientIP, email                 string
		note, nullableJSON                        *string
	)
	row := e.opened[StoreBusiness].QueryRow(`SELECT id, account_id, created_at, status, attempts, payload_json, checksum, client_ip, email, optional_note, notes_json FROM sample_records`)
	if err := row.Scan(&id, &accountID, &createdAt, &status, &attempts, &payload, &checksum, &clientIP, &email, &note, &nullableJSON); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(id, CleanupIDPrefix) {
		t.Fatalf("id = %q, want a cleanup-marked id", id)
	}
	if !strings.HasPrefix(accountID, CleanupIDPrefix) {
		t.Fatalf("account_id = %q", accountID)
	}
	if _, err := time.Parse(isoMillisLayout, createdAt); err != nil {
		t.Fatalf("created_at = %q is not the ISO millisecond layout: %v", createdAt, err)
	}
	// CHECK IN 的第一个允许值是 'active'：能解析时必须取它而不是自己编值。
	if status != "active" {
		t.Fatalf("status = %q, want the first CHECK value", status)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
	if payload != "{}" {
		t.Fatalf("payload_json = %q, want {}", payload)
	}
	if len(checksum) != 64 {
		t.Fatalf("checksum = %q, want a 64-char digest", checksum)
	}
	if clientIP != "10.10.0.1" {
		t.Fatalf("client_ip = %q", clientIP)
	}
	if !strings.Contains(email, "@example.invalid") {
		t.Fatalf("email = %q", email)
	}
	// 可空无默认值的列留空，避免撞 CHECK / UNIQUE（含 *_json 列）。
	if note != nil {
		t.Fatalf("optional_note = %v, want NULL", *note)
	}
	if nullableJSON != nil {
		t.Fatalf("notes_json = %v, want NULL", *nullableJSON)
	}
}

func TestAutofillHonoursAutoIncrementPrimaryKey(t *testing.T) {
	e := testEnv(t)
	createTestTable(t, e, StoreBusiness, `CREATE TABLE counters (
		seq INTEGER PRIMARY KEY,
		label TEXT NOT NULL
	)`)
	if _, _, err := autofillTables(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	var seq int
	if err := e.opened[StoreBusiness].QueryRow(`SELECT seq FROM counters LIMIT 1`).Scan(&seq); err != nil {
		t.Fatal(err)
	}
	if seq < 1 {
		t.Fatalf("seq = %d, want SQLite-assigned rowid", seq)
	}
}

func TestAutofillRecordsSkipReasons(t *testing.T) {
	e := testEnv(t)
	ctx := context.Background()
	// 派生聚合族、owner 租约族、域业务表都要跳过。
	for _, ddl := range []string{
		`CREATE TABLE usage_stats_daily (id TEXT PRIMARY KEY, total INTEGER NOT NULL)`,
		`CREATE TABLE account_health_hourly (account_id TEXT NOT NULL, stat_hour TEXT NOT NULL)`,
		`CREATE TABLE stats_job_state (job_name TEXT PRIMARY KEY)`,
		`CREATE TABLE audit_log_owner_leases (lease_key TEXT PRIMARY KEY)`,
		`CREATE TABLE account_health_key_cursors (cursor_key TEXT PRIMARY KEY)`,
		`CREATE TABLE groups (id TEXT PRIMARY KEY, name TEXT NOT NULL)`,
		`CREATE TABLE usage_range_window_requests (id TEXT PRIMARY KEY)`,
	} {
		createTestTable(t, e, StoreBusiness, ddl)
	}
	inserted, skipped, err := autofillTables(ctx, e)
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{
		"usage_stats_daily", "account_health_hourly", "stats_job_state",
		"audit_log_owner_leases", "account_health_key_cursors", "groups",
		"usage_range_window_requests",
	} {
		if _, ok := inserted[StoreBusiness+"."+table]; ok {
			t.Fatalf("%s must not be autofilled", table)
		}
		if skipped[StoreBusiness+"."+table] == "" {
			t.Fatalf("%s must record a skip reason (%v)", table, skipped)
		}
	}
}

func TestAutofillSkipReasonCategories(t *testing.T) {
	cases := []struct {
		store   string
		table   string
		wantSub string
	}{
		{StoreBusiness, "usage_stats_minute", "派生聚合族前缀 usage_stats_"},
		{StoreBusiness, "usage_model_daily", "派生聚合族前缀 usage_model_"},
		{StoreBusiness, "usage_error_rank_windows", "派生聚合族前缀 usage_error_"},
		{StoreBusiness, "usage_latency_weekly", "派生聚合族前缀 usage_latency_"},
		{StoreBusiness, "account_quality_scores", "派生聚合族前缀 account_quality_"},
		{StoreBusiness, "client_ip_stats_daily", "派生聚合族前缀 client_ip_"},
		{StoreBusiness, "system_metrics_hourly", "派生聚合族前缀 system_metrics_"},
		{StoreBusiness, "process_event_loop_trend_windows", "派生聚合族前缀 process_event_loop_"},
		{StoreBusiness, "go_runtime_metrics_samples", "派生聚合族前缀 go_runtime_metrics_"},
		{StoreBusiness, "some_thing_windows", "运行时状态族后缀 _windows"},
		{StoreBusiness, "anything_projection_cursors", "运行时状态族后缀 _projection_cursors"},
		// 域业务表的精确登记优先于专库兜底，因此这里用未登记的表名验证兜底规则。
		{StoreModelCheck, "model_check_runs", "域业务表"},
		{StoreModelCheck, "unregistered_table", "J3b"},
		{StoreTaskRuns, "anything", "J3b"},
		{StoreAccountHealth, "anything", "J3b"},
		{StoreRuntimeLog, "runtime_logs", "域业务表"},
		{StoreRuntimeLog, "unregistered_log_table", "可观测域"},
		{StoreAuditLog, "audit_logs", "域业务表"},
		{StoreOperationLog, "operation_logs", "域业务表"},
		{StoreTableMonitor, "table_storage_snapshots", "域业务表"},
		{StoreTableMonitor, "unregistered_snapshot_table", "可观测域"},
		{StoreChat, "chat_conversations", "域业务表"},
		{StoreUsageCatalog, "usage_record_shards", "域业务表"},
	}
	for _, testCase := range cases {
		reason, skip := autofillSkipReason(testCase.store, testCase.table)
		if !skip {
			t.Fatalf("%s.%s should be skipped", testCase.store, testCase.table)
		}
		if !strings.Contains(reason, testCase.wantSub) {
			t.Fatalf("%s.%s reason = %q, want substring %q", testCase.store, testCase.table, reason, testCase.wantSub)
		}
	}
	if _, skip := autofillSkipReason(StoreBusiness, "global_settings"); skip {
		t.Fatal("an uncovered business table must stay in the autofill set")
	}
}

func TestAutofillDegradesPerTableOnInsertFailure(t *testing.T) {
	e := testEnv(t)
	// NOT NULL 且带 CHECK 约束、且没有任何可解析允许值的列：占位值必然违约，
	// autofill 必须记录原因并继续处理后面的表，而不是让整个造数失败。
	createTestTable(t, e, StoreBusiness, `CREATE TABLE brittle (id TEXT PRIMARY KEY, code TEXT NOT NULL CHECK (length(code) > 40))`)
	createTestTable(t, e, StoreBusiness, `CREATE TABLE healthy (id TEXT PRIMARY KEY, label TEXT NOT NULL)`)
	inserted, skipped, err := autofillTables(context.Background(), e)
	if err != nil {
		t.Fatalf("autofill must degrade instead of failing: %v", err)
	}
	if skipped[StoreBusiness+".brittle"] == "" {
		t.Fatalf("brittle must carry a failure reason (%v)", skipped)
	}
	if inserted[StoreBusiness+".healthy"] < 1 {
		t.Fatalf("healthy table must still be filled: %v", inserted)
	}
}

func TestAutofillValueHeuristics(t *testing.T) {
	now := autofillTestNow
	cases := []struct {
		name     string
		column   tableColumn
		checks   []string
		contains string
	}{
		{name: "check wins", column: tableColumn{Name: "status", Type: "TEXT"}, checks: []string{"pending", "approved"}, contains: "pending"},
		{name: "foreign key", column: tableColumn{Name: "account_id", Type: "TEXT"}, contains: CleanupIDPrefix + "autofill_"},
		{name: "timestamp", column: tableColumn{Name: "created_at", Type: "TEXT"}, contains: "2026-09-22T10:30:00.000Z"},
		{name: "json", column: tableColumn{Name: "metadata_json", Type: "TEXT"}, contains: "{}"},
		{name: "digest", column: tableColumn{Name: "sha256", Type: "TEXT"}, contains: "a"},
		{name: "ip", column: tableColumn{Name: "client_ip", Type: "TEXT"}, contains: "10.10.0.1"},
		{name: "email", column: tableColumn{Name: "email", Type: "TEXT"}, contains: "@example.invalid"},
		{name: "url", column: tableColumn{Name: "callback_url", Type: "TEXT"}, contains: "https://example.invalid/"},
		{name: "integer", column: tableColumn{Name: "attempts", Type: "INTEGER"}, contains: "1"},
		{name: "real", column: tableColumn{Name: "ratio", Type: "REAL"}, contains: "1"},
		{name: "blob", column: tableColumn{Name: "payload", Type: "BLOB"}, contains: "<nil>"},
		{name: "generic", column: tableColumn{Name: "note", Type: "TEXT"}, contains: CleanupIDPrefix + "autofill_"},
	}
	for _, testCase := range cases {
		got := autofillValue("t", testCase.column, testCase.checks, 0, now)
		if !strings.Contains(toString(got), testCase.contains) {
			t.Fatalf("%s: value = %v, want substring %q", testCase.name, got, testCase.contains)
		}
	}
	// 时间列随行号回退，保证同一表内多行时间不重复。
	first := autofillValue("t", tableColumn{Name: "created_at", Type: "TEXT"}, nil, 0, now)
	second := autofillValue("t", tableColumn{Name: "created_at", Type: "TEXT"}, nil, 1, now)
	if first == second {
		t.Fatalf("timestamps should differ per row: %v", first)
	}
}

func TestAutofillRowCountIsStableAndBounded(t *testing.T) {
	for _, table := range []string{"a", "b", "chat_messages", "usage_records", "model_check_runs"} {
		first := autofillRowCount(table)
		if first < 1 || first > 3 {
			t.Fatalf("row count for %s = %d, want 1..3", table, first)
		}
		if second := autofillRowCount(table); second != first {
			t.Fatalf("row count for %s is not stable: %d -> %d", table, first, second)
		}
	}
}

func TestQueryCheckInValuesParsesAndTolerates(t *testing.T) {
	e := testEnv(t)
	ctx := context.Background()
	createTestTable(t, e, StoreBusiness, `CREATE TABLE with_checks (
		id TEXT PRIMARY KEY,
		status TEXT NOT NULL CHECK (status IN ('pending', 'approved', 'rejected')),
		kind TEXT NOT NULL CHECK (kind IN ('a''b', 'c')),
		other TEXT
	)`)
	createTestTable(t, e, StoreBusiness, `CREATE TABLE without_checks (id TEXT PRIMARY KEY, note TEXT)`)
	db := e.opened[StoreBusiness]

	checks, err := queryCheckInValues(ctx, db, "with_checks")
	if err != nil {
		t.Fatal(err)
	}
	if len(checks["status"]) != 3 || checks["status"][0] != "pending" {
		t.Fatalf("status checks = %v", checks["status"])
	}
	// 单引号转义（''）要还原成一个引号。
	if len(checks["kind"]) != 2 || checks["kind"][0] != "a'b" {
		t.Fatalf("kind checks = %v", checks["kind"])
	}
	if checks, err := queryCheckInValues(ctx, db, "without_checks"); err != nil || len(checks) != 0 {
		t.Fatalf("no checks = %v/%v", checks, err)
	}
	// 表不存在：sqlite_master 无行，按「没有约束」处理而不是错误。
	if checks, err := queryCheckInValues(ctx, db, "ghost"); err != nil || checks != nil {
		t.Fatalf("missing table = %v/%v", checks, err)
	}
}

func TestHasIntegerPrimaryKey(t *testing.T) {
	cases := []struct {
		name    string
		columns []tableColumn
		want    bool
	}{
		{name: "integer pk", columns: []tableColumn{{Name: "id", Type: "INTEGER", PrimaryKey: 1}}, want: true},
		{name: "text pk", columns: []tableColumn{{Name: "id", Type: "TEXT", PrimaryKey: 1}}, want: false},
		{name: "composite pk", columns: []tableColumn{{Name: "a", Type: "INTEGER", PrimaryKey: 1}, {Name: "b", Type: "INTEGER", PrimaryKey: 2}}, want: false},
		{name: "no pk", columns: []tableColumn{{Name: "a", Type: "TEXT"}}, want: false},
	}
	for _, testCase := range cases {
		if got := hasIntegerPrimaryKey(testCase.columns); got != testCase.want {
			t.Fatalf("%s: got %v, want %v", testCase.name, got, testCase.want)
		}
	}
}

// toString 把占位值渲染成可做子串断言的形式（nil 渲染成 "<nil>"）。
func toString(value any) string {
	return fmt.Sprintf("%v", value)
}

// TestAutofillHandlesEmptyDataRoot 覆盖「数据根下什么都还没有」的首次运行：
// 必须整轮成功、只记 skipped。
func TestAutofillHandlesEmptyDataRoot(t *testing.T) {
	e := testEnv(t)
	if err := os.MkdirAll(e.options.Paths.DataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	inserted, skipped, err := autofillTables(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	if len(inserted) != 0 || len(skipped) != 0 {
		t.Fatalf("empty root should do nothing: inserted=%v skipped=%v", inserted, skipped)
	}
}
