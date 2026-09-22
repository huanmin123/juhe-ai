package mockdata

import (
	"context"
	"strings"
	"testing"
)

// seedCleanupFixture 建一张带清理标识列的最小业务表并写入 mock / 真实两类行。
func seedCleanupFixture(t *testing.T, e *env) {
	t.Helper()
	ctx := context.Background()
	createTestTable(t, e, StoreBusiness, `CREATE TABLE system_accounts (
		id TEXT PRIMARY KEY,
		username TEXT NOT NULL,
		display_name TEXT
	)`)
	createTestTable(t, e, StoreBusiness, `CREATE TABLE groups (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		system_account_id TEXT NOT NULL
	)`)
	rows := []struct {
		table  string
		values map[string]any
	}{
		{"system_accounts", map[string]any{"id": CleanupIDPrefix + "admin", "username": CleanupIDPrefix + "admin", "display_name": CleanupNamePrefix + "管理员用户"}},
		{"system_accounts", map[string]any{"id": "real-admin", "username": "admin", "display_name": "管理员"}},
		{"groups", map[string]any{"id": CleanupIDPrefix + "main", "name": CleanupNamePrefix + "主力分组", "system_account_id": CleanupIDPrefix + "admin"}},
		{"groups", map[string]any{"id": "real-group", "name": "默认分组", "system_account_id": "real-admin"}},
	}
	for _, row := range rows {
		if err := e.insertMap(ctx, StoreBusiness, row.table, row.values); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCleanupAllRemovesOnlyMockdataRows(t *testing.T) {
	e := testEnv(t)
	seedCleanupFixture(t, e)
	deleted, skipped, err := cleanupAll(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	if deleted[StoreBusiness+".system_accounts"] != 1 {
		t.Fatalf("system_accounts deleted = %d, want 1 (%v)", deleted[StoreBusiness+".system_accounts"], deleted)
	}
	if deleted[StoreBusiness+".groups"] != 1 {
		t.Fatalf("groups deleted = %d, want 1", deleted[StoreBusiness+".groups"])
	}
	for _, table := range []string{"system_accounts", "groups"} {
		count, err := e.queryCount(context.Background(), StoreBusiness, table)
		if err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("%s rows left = %d, want the single real row", table, count)
		}
	}
	// 未 bootstrap 的存储与不存在的表进 skipped，不视为错误。
	if len(skipped) == 0 {
		t.Fatal("skipped list should record missing stores/tables")
	}
	joined := strings.Join(skipped, "\n")
	if !strings.Contains(joined, StoreStats) {
		t.Fatalf("skipped should mention the missing stats store:\n%s", joined)
	}
	// 幂等：第二次运行只删 0 行且不报错。
	second, _, err := cleanupAll(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	for table, rows := range second {
		if rows != 0 {
			t.Fatalf("second cleanup deleted %d rows from %s", rows, table)
		}
	}
}

func TestCleanupSweepMatchesAllThreeMarkers(t *testing.T) {
	e := testEnv(t)
	ctx := context.Background()
	createTestTable(t, e, StoreChat, `CREATE TABLE chat_messages (
		id TEXT PRIMARY KEY,
		trace_id TEXT,
		body TEXT
	)`)
	// 表里没有 chat_messages 的显式清理规则之外的行：这里验证通用扫描层。
	rows := []map[string]any{
		{"id": CleanupIDPrefix + "msg_a", "trace_id": "t-real", "body": "x"},
		{"id": "real-msg", "trace_id": CleanupTracePrefix + "msg_b", "body": "y"},
		{"id": "real-msg-2", "trace_id": "t-real", "body": CleanupNamePrefix + "示例"},
		{"id": "real-msg-3", "trace_id": "t-real", "body": "keep me"},
	}
	for _, row := range rows {
		if err := e.insertMap(ctx, StoreChat, "chat_messages", row); err != nil {
			t.Fatal(err)
		}
	}
	deleted, err := sweepCleanupMarkers(ctx, e)
	if err != nil {
		t.Fatal(err)
	}
	// body 不是标识列（只有 id / *_id / *_key / *_name 命名参与扫描），因此
	// 名称标识写在 body 上的第三行不被通用扫描命中。
	if deleted[StoreChat+".chat_messages"] != 2 {
		t.Fatalf("sweep deleted = %d, want 2 (%v)", deleted[StoreChat+".chat_messages"], deleted)
	}
	count, err := e.queryCount(ctx, StoreChat, "chat_messages")
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("rows left = %d, want 2", count)
	}
}

func TestCleanupSweepSkipsTablesWithoutIdentifierColumns(t *testing.T) {
	e := testEnv(t)
	createTestTable(t, e, StoreStats, `CREATE TABLE samples (amount INTEGER NOT NULL, ratio REAL)`)
	if err := e.insertMap(context.Background(), StoreStats, "samples", map[string]any{"amount": 5, "ratio": 1.5}); err != nil {
		t.Fatal(err)
	}
	deleted, err := sweepCleanupMarkers(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := deleted[StoreStats+".samples"]; ok {
		t.Fatalf("a table without identifier columns must be skipped: %v", deleted)
	}
	if count, err := e.queryCount(context.Background(), StoreStats, "samples"); err != nil || count != 1 {
		t.Fatalf("rows = %d/%v", count, err)
	}
}

func TestCleanupRuleHelpers(t *testing.T) {
	// 分片前缀规则展开到全部已存在分片存储。
	root := t.TempDir()
	paths, err := ResolvePaths(root, "", envMap(nil))
	if err != nil {
		t.Fatal(err)
	}
	e := newEnv(Options{Paths: paths, Days: 1, DailyRequests: 1}, nil)
	defer func() { _ = e.Close() }()
	createTestTable(t, e, StoreCodexContextShardPrefix+"[0]", `CREATE TABLE codex_context_sessions (id TEXT PRIMARY KEY)`)
	x := CleanupIDPrefix + "session"
	if err := e.insertMap(context.Background(), StoreCodexContextShardPrefix+"[0]", "codex_context_sessions", map[string]any{"id": x}); err != nil {
		t.Fatal(err)
	}
	// codex 分片是「按数量派生」的清单：即使文件尚未创建也会出现在目标里，
	// 这样清理才能把「文件存在但没建表」记成 skipped 而不是无声跳过。
	targets := resolveCleanupTargets(e, cleanupRule{ShardPrefix: StoreCodexContextShardPrefix, Table: "t"})
	if len(targets) != paths.CodexContextShardCount {
		t.Fatalf("shard targets = %d, want %d", len(targets), paths.CodexContextShardCount)
	}
	if targets[0].Name != StoreCodexContextShardPrefix+"[0]" {
		t.Fatalf("first shard target = %q", targets[0].Name)
	}
	if targets := resolveCleanupTargets(e, cleanupRule{Store: "not-a-store"}); targets != nil {
		t.Fatalf("unknown store targets = %v", targets)
	}
	rows, err := deleteIfTableExists(context.Background(), e, e.byName[StoreCodexContextShardPrefix+"[0]"], "codex_context_sessions", "DELETE FROM codex_context_sessions WHERE id LIKE ?", []any{CleanupIDPrefix + "%"})
	if err != nil || rows != 1 {
		t.Fatalf("deleteIfTableExists = %d/%v", rows, err)
	}
	// 表不存在返回 -1（记 skipped）。
	if rows, err := deleteIfTableExists(context.Background(), e, e.byName[StoreCodexContextShardPrefix+"[0]"], "ghost", "DELETE FROM ghost", nil); err != nil || rows != -1 {
		t.Fatalf("deleteIfTableExists on a missing table = %d/%v", rows, err)
	}
	// 存储文件不存在同样返回 -1。
	if rows, err := deleteIfTableExists(context.Background(), e, e.byName[StoreStats], "samples", "DELETE FROM samples", nil); err != nil || rows != -1 {
		t.Fatalf("deleteIfTableExists on a missing store = %d/%v", rows, err)
	}
	// 清理语句本身有错时冒泡。
	if _, err := deleteIfTableExists(context.Background(), e, e.byName[StoreCodexContextShardPrefix+"[0]"], "codex_context_sessions", "DELETE FROM codex_context_sessions WHERE no_such_column = ?", []any{"x"}); err == nil {
		t.Fatal("a broken cleanup statement should fail")
	}

	// storeTableCount 区分 0 行与缺表。
	if count, err := storeTableCount(context.Background(), e, StoreCodexContextShardPrefix+"[0]", "codex_context_sessions"); err != nil || count != 0 {
		t.Fatalf("storeTableCount = %d/%v", count, err)
	}
	if count, err := storeTableCount(context.Background(), e, StoreCodexContextShardPrefix+"[0]", "ghost"); err != nil || count != -1 {
		t.Fatalf("storeTableCount(missing table) = %d/%v", count, err)
	}
	if count, err := storeTableCount(context.Background(), e, StoreStats, "ghost"); err != nil || count != -1 {
		t.Fatalf("storeTableCount(missing store) = %d/%v", count, err)
	}
}

func TestSweepColumnHeuristics(t *testing.T) {
	cases := []struct {
		column tableColumn
		want   bool
	}{
		{tableColumn{Name: "id", Type: "TEXT"}, true},
		{tableColumn{Name: "system_account_id", Type: "TEXT"}, true},
		{tableColumn{Name: "lease_key", Type: "TEXT"}, true},
		{tableColumn{Name: "display_name", Type: "TEXT"}, true},
		{tableColumn{Name: "run_id", Type: "TEXT"}, true},
		{tableColumn{Name: "body", Type: "TEXT"}, false},
		{tableColumn{Name: "amount", Type: "INTEGER"}, false},
		{tableColumn{Name: "id", Type: "INTEGER"}, false},
		{tableColumn{Name: "id", Type: ""}, true},
	}
	for _, testCase := range cases {
		if got := sweepColumn(testCase.column); got != testCase.want {
			t.Fatalf("sweepColumn(%+v) = %v, want %v", testCase.column, got, testCase.want)
		}
	}
	if query := sweepDeleteQuery("t", []tableColumn{{Name: "body", Type: "TEXT"}}); query != "" {
		t.Fatalf("query = %q, want empty", query)
	}
	query := sweepDeleteQuery("t", []tableColumn{{Name: "id", Type: "TEXT"}})
	for _, marker := range cleanupMarkers {
		if !strings.Contains(query, marker+"%") {
			t.Fatalf("query %q must contain marker %q", query, marker)
		}
	}
}
