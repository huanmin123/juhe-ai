package taskruns

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---- OpenStore 配置校验与双模式 ----

func TestOpenStoreValidationAndModes(t *testing.T) {
	if _, err := OpenStore(StoreConfig{Mode: ModeSQLite}); err == nil {
		t.Fatalf("sqlite 缺路径应报错")
	}
	if _, err := OpenStore(StoreConfig{Mode: StoreMode("mysql")}); err == nil {
		t.Fatalf("非法模式应报错")
	}
	if _, err := OpenStore(StoreConfig{Mode: ModePostgres}); err == nil {
		t.Fatalf("postgres 缺 URL 应报错")
	}
	// SQLite 正常打开并建表。
	store, err := OpenStore(StoreConfig{Mode: ModeSQLite, DatabasePath: filepath.Join(t.TempDir(), "taskruns.sqlite3")})
	if err != nil {
		t.Fatalf("sqlite 打开失败: %v", err)
	}
	if store.Mode() != ModeSQLite {
		t.Fatalf("模式应为 sqlite: %s", store.Mode())
	}
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatalf("建表失败: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("关闭失败: %v", err)
	}
	// PostgreSQL 懒打开（不要求真实数据库可达）后关闭归还连接池。
	pgStore, err := OpenStore(StoreConfig{Mode: ModePostgres, PostgresURL: "postgres://taskruns:pw@127.0.0.1:1/juhe_stats?connect_timeout=1"})
	if err != nil {
		t.Fatalf("postgres 懒打开不应报错: %v", err)
	}
	if pgStore.Mode() != ModePostgres {
		t.Fatalf("模式应为 postgres: %s", pgStore.Mode())
	}
	if err := pgStore.Close(); err != nil {
		t.Fatalf("关闭失败: %v", err)
	}
	// NewStore 复用已有连接。
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	reused := NewStore(db, ModeSQLite)
	if reused.Mode() != ModeSQLite {
		t.Fatalf("NewStore 模式不符: %s", reused.Mode())
	}
	if err := reused.EnsureSchema(context.Background()); err != nil {
		t.Fatalf("NewStore 建表失败: %v", err)
	}
	var nilStore *Store
	if err := nilStore.Close(); err != nil {
		t.Fatalf("nil store 关闭应安全: %v", err)
	}
}

func TestStoreDialectHelpers(t *testing.T) {
	sqliteStore := NewStore(nil, ModeSQLite)
	pgStore := NewStore(nil, ModePostgres)
	if got := sqliteStore.runsTable(); got != "background_task_runs" {
		t.Fatalf("sqlite runs 表名不符: %s", got)
	}
	if got := pgStore.runsTable(); got != "juhe_stats.background_task_runs" {
		t.Fatalf("postgres runs 表名不符: %s", got)
	}
	if got := sqliteStore.leasesTable(); got != "background_job_leases" {
		t.Fatalf("sqlite leases 表名不符: %s", got)
	}
	if got := pgStore.leasesTable(); got != "juhe_stats.background_job_leases" {
		t.Fatalf("postgres leases 表名不符: %s", got)
	}
}

// ---- 纯函数 ----

func TestPostgresBoolVariants(t *testing.T) {
	cases := []struct {
		value any
		want  bool
	}{
		{true, true}, {false, false},
		{"t", true}, {"true", true}, {"1", true}, {"f", false}, {"other", false},
		{int64(1), true}, {int64(0), false},
		{nil, false}, {3.5, false},
	}
	for _, tc := range cases {
		if got := postgresBool(tc.value); got != tc.want {
			t.Fatalf("postgresBool(%#v)=%v，期望 %v", tc.value, got, tc.want)
		}
	}
}

func TestParseJSONObjectVariants(t *testing.T) {
	if got := parseJSONObject(" "); len(got) != 0 {
		t.Fatalf("空文本应返回空对象: %#v", got)
	}
	if got := parseJSONObject("{broken"); len(got) != 0 {
		t.Fatalf("非法 JSON 应返回空对象: %#v", got)
	}
	if got := parseJSONObject(`[1,2]`); len(got) != 0 {
		t.Fatalf("非对象应返回空对象: %#v", got)
	}
	got := parseJSONObject(`{"k":"v"}`)
	if got["k"] != "v" {
		t.Fatalf("合法对象应解析: %#v", got)
	}
}

func TestClockHelpers(t *testing.T) {
	fixed := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	if got := NowIso(NewFakeClock(fixed)); got != "2026-09-10T12:00:00.000Z" {
		t.Fatalf("NowIso 应输出毫秒 UTC 文本: %s", got)
	}
	// OptionalInstant：nil/空串放行，非法拒绝。
	if got, err := OptionalInstant(nil, "startAt"); err != nil || got != nil {
		t.Fatalf("nil 应放行: %v %v", got, err)
	}
	empty := ""
	if got, err := OptionalInstant(&empty, "startAt"); err != nil || got != nil {
		t.Fatalf("空串应放行: %v %v", got, err)
	}
	valid := "2026-09-10T12:00:00.000Z"
	got, err := OptionalInstant(&valid, "startAt")
	if err != nil || got == nil || !got.Equal(fixed) {
		t.Fatalf("合法时间应解析: %v %v", got, err)
	}
	invalid := "not-a-time"
	if _, err := OptionalInstant(&invalid, "startAt"); err == nil || !strings.Contains(err.Error(), "startAt") {
		t.Fatalf("非法时间应报错并带字段名: %v", err)
	}
}

func TestDerefRunNilSafety(t *testing.T) {
	zero := TaskRun{}
	if derefRun(nil).JobType != zero.JobType || derefRun(nil).RunID != zero.RunID {
		t.Fatalf("nil 应解引用为零值")
	}
	run := &TaskRun{RunID: "run-1"}
	if got := derefRun(run); got.RunID != "run-1" {
		t.Fatalf("非 nil 应返回拷贝: %#v", got)
	}
}
