package modelcheckdurable

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckinput"
)

// ---- 方言辅助：SQLite / Postgres 双模式字符串变换 ----

func TestStoreDialectHelpers(t *testing.T) {
	sqliteStore := &Store{mode: SQLite}
	pgStore := &Store{mode: Postgres}
	if got := sqliteStore.bind("SELECT ?"); got != "SELECT ?" {
		t.Fatalf("sqlite 不应改写占位符: %s", got)
	}
	if got := pgStore.bind("SELECT ?,?"); got != "SELECT $1,$2" {
		t.Fatalf("postgres 占位符应转换: %s", got)
	}
	if got := sqliteStore.table("model_check_inputs"); got != "model_check_inputs" {
		t.Fatalf("sqlite 表名不带前缀: %s", got)
	}
	if got := pgStore.table("model_check_inputs"); got != "juhe_jobs.model_check_inputs" {
		t.Fatalf("postgres 表名应带 schema: %s", got)
	}
	if got := sqliteStore.lock("SELECT 1"); got != "SELECT 1" {
		t.Fatalf("sqlite 不应追加锁子句: %s", got)
	}
	if got := pgStore.lock("SELECT 1"); got != "SELECT 1 FOR UPDATE" {
		t.Fatalf("postgres 应追加 FOR UPDATE: %s", got)
	}
	if got := sqliteStore.committedLiteral(); got != "1" {
		t.Fatalf("sqlite committed 字面量应为 1: %s", got)
	}
	if got := pgStore.committedLiteral(); got != "TRUE" {
		t.Fatalf("postgres committed 字面量应为 TRUE: %s", got)
	}
	moment := time.Date(2026, 9, 10, 1, 2, 3, 0, time.UTC)
	if got := sqliteStore.timeValue(moment); got != "2026-09-10T01:02:03Z" {
		t.Fatalf("sqlite 时间应存为 RFC3339 文本: %v", got)
	}
	if _, ok := pgStore.timeValue(moment).(time.Time); !ok {
		t.Fatalf("postgres 时间应保持 time.Time: %T", pgStore.timeValue(moment))
	}
}

func TestReadTimeVariants(t *testing.T) {
	expected := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	store := &Store{mode: SQLite}
	if got, err := store.readTime(expected); err != nil || !got.Equal(expected) {
		t.Fatalf("time.Time 应原样 UTC 化: %v %v", got, err)
	}
	if got, err := store.readTime("2026-01-02T03:04:05Z"); err != nil || !got.Equal(expected) {
		t.Fatalf("RFC3339 文本应可解析: %v %v", got, err)
	}
	if got, err := store.readTime([]byte("2026-01-02T03:04:05Z")); err != nil || !got.Equal(expected) {
		t.Fatalf("字节数组应递归解析: %v %v", got, err)
	}
	if _, err := store.readTime("not-a-time"); err == nil {
		t.Fatalf("非法文本应报错")
	}
	if _, err := store.readTime(42); err == nil {
		t.Fatalf("未知类型应报错")
	}
}

// ---- 构造与 schema ----

func TestNewAndOpenValidation(t *testing.T) {
	if _, err := New(nil, SQLite); err == nil {
		t.Fatalf("nil 数据库应报错")
	}
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := New(db, Mode("oracle")); err == nil {
		t.Fatalf("非法模式应报错")
	}
	store, err := New(db, SQLite)
	if err != nil || store == nil {
		t.Fatalf("合法构造不应报错: %v", err)
	}
	if _, err := OpenSQLite(" "); err == nil {
		t.Fatalf("空路径应报错")
	}
	if _, err := OpenPostgres("", 10); err == nil {
		t.Fatalf("空 DSN 应报错")
	}
	pgStore, err := OpenPostgres("postgres://durable:pw@127.0.0.1:1/juhe_jobs?connect_timeout=1", 0)
	if err != nil {
		t.Fatalf("懒打开不应报错: %v", err)
	}
	if err := pgStore.Close(); err != nil {
		t.Fatalf("关闭失败: %v", err)
	}
	var nilStore *Store
	if err := nilStore.Close(); err != nil {
		t.Fatalf("nil store 关闭应安全: %v", err)
	}
	if err := nilStore.EnsureSchema(context.Background()); err == nil {
		t.Fatalf("nil store schema 校验应报错")
	}
}

func TestEnsureSchemaSQLiteIdempotent(t *testing.T) {
	store, err := OpenSQLite(sqliteSchemaPath(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatalf("首次建表不应报错: %v", err)
	}
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatalf("重复建表应幂等: %v", err)
	}
}

func sqliteSchemaPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "durable.sqlite3")
}

// ---- 游标与查询校验分支 ----

func TestListCommittedOutcomesValidation(t *testing.T) {
	store, err := OpenSQLite(sqliteSchemaPath(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ListCommittedOutcomes(ctx, OutcomeCursor{}, 0); err == nil {
		t.Fatalf("limit=0 应报错")
	}
	if _, err := store.ListCommittedOutcomes(ctx, OutcomeCursor{}, 10001); err == nil {
		t.Fatalf("limit>10000 应报错")
	}
	incomplete := []OutcomeCursor{
		{StoredAt: time.Now()},
		{OutcomeID: "outcome-1"},
	}
	for _, cursor := range incomplete {
		if _, err := store.ListCommittedOutcomes(ctx, cursor, 10); err == nil || !strings.Contains(err.Error(), "incomplete") {
			t.Fatalf("不完整游标应报错: %#v", cursor)
		}
	}
	// 空表返回空结果。
	result, err := store.ListCommittedOutcomes(ctx, OutcomeCursor{}, 10)
	if err != nil || len(result) != 0 {
		t.Fatalf("空表应返回空列表: %d %v", len(result), err)
	}
	if _, _, err := store.FindCommittedOutcome(ctx, " "); err == nil {
		t.Fatalf("空 outcome ID 应报错")
	}
	found, ok, err := store.FindCommittedOutcome(ctx, "missing")
	if err != nil || ok || found.Outcome.OutcomeID != "" {
		t.Fatalf("未找到应返回 false: %v %v", ok, err)
	}
}

func TestIssueRejectsInvalidDraft(t *testing.T) {
	store, err := OpenSQLite(sqliteSchemaPath(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	// 缺字段草稿在输入签发层即失败。
	draft := modelcheckinput.Draft{}
	if _, err := store.Issue(context.Background(), draft); err == nil {
		t.Fatalf("非法草稿应报错")
	}
	if _, err := store.LoadInput(context.Background(), " ", time.Now()); err == nil {
		t.Fatalf("空 input ID 应报错")
	}
	if _, err := store.LoadInput(context.Background(), "missing", time.Now()); err == nil {
		t.Fatalf("缺失输入应报错")
	}
}

func TestScanStoredOutcomeRejectsTamperedInput(t *testing.T) {
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "j3b-wo.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 10, 8, 0, 0, 0, time.UTC)
	issued, err := store.Issue(ctx, validDraft(now))
	if err != nil {
		t.Fatal(err)
	}
	claim, err := store.Claim(ctx, issued.Input.InputID, "owner-wo", "claim-wo", "outcome-wo", now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte(`{"status":"passed"}`)
	if err := store.CommitOutcome(ctx, Outcome{OutcomeID: "outcome-wo", InputID: issued.Input.InputID, InputDigest: issued.Input.InputDigest, Payload: payload}, claim, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	// 游标分页分支：完整游标（StoredAt+OutcomeID）应可执行且不报错。
	stored, ok, err := store.FindCommittedOutcome(ctx, "outcome-wo")
	if err != nil || !ok {
		t.Fatalf("应能找回已提交结果: %v %v", ok, err)
	}
	page, err := store.ListCommittedOutcomes(ctx, OutcomeCursor{StoredAt: stored.Outcome.StoredAt, OutcomeID: stored.Outcome.OutcomeID}, 10)
	if err != nil || len(page) != 0 {
		t.Fatalf("游标之后应无更多结果: %d %v", len(page), err)
	}
	// 篡改输入载荷：完整性校验必须报 ErrInputTampered。
	if _, err := store.db.Exec(`UPDATE model_check_inputs SET payload = ? WHERE input_id = ?`, []byte(`{"tampered":true}`), issued.Input.InputID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.FindCommittedOutcome(ctx, "outcome-wo"); !errors.Is(err, ErrInputTampered) {
		t.Fatalf("输入被篡改应报 ErrInputTampered: %v", err)
	}
	if _, err := store.ListCommittedOutcomes(ctx, OutcomeCursor{}, 10); !errors.Is(err, ErrInputTampered) {
		t.Fatalf("列表读取同样必须 fail closed: %v", err)
	}
}
