package statsverify

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---- OpenStore 配置校验与 Postgres 懒打开 ----

func TestOpenStoreConfigValidation(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name    string
		config  StoreConfig
		wantErr string
	}{
		{
			name:    "sqlite 缺路径",
			config:  StoreConfig{Mode: StoreSQLite},
			wantErr: "statsverify sqlite 缺少 stats 或 business 数据库路径",
		},
		{
			name:    "postgres 缺 URL",
			config:  StoreConfig{Mode: StorePostgres},
			wantErr: "statsverify postgres 缺少连接 URL",
		},
		{
			name:    "postgres 连接池上限无效",
			config:  StoreConfig{Mode: StorePostgres, PostgresURL: "postgres://u:p@127.0.0.1:1/db", PostgresMaxOpenConns: 5, PostgresMaxIdleConns: 10},
			wantErr: "statsverify postgres max open/idle 必须满足 1 <= idle <= open，实际为 5/10",
		},
		{
			name:    "未知模式",
			config:  StoreConfig{Mode: "mysql"},
			wantErr: "statsverify store mode 必须为 sqlite 或 postgres",
		},
		{
			name:   "sqlite 正常打开",
			config: StoreConfig{Mode: StoreSQLite, SQLiteStatsPath: filepath.Join(dir, "s.db"), SQLiteBusinessPath: filepath.Join(dir, "b.db")},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, err := OpenStore(tc.config)
			if tc.wantErr != "" {
				if err == nil {
					_ = store.Close()
					t.Fatalf("应返回错误: %s", tc.wantErr)
				}
				if err.Error() != tc.wantErr {
					t.Fatalf("错误文案不符，实际: %s", err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("不应报错: %v", err)
			}
			if err := store.Close(); err != nil {
				t.Fatalf("关闭失败: %v", err)
			}
		})
	}
}

func TestOpenStorePostgresLazyOpen(t *testing.T) {
	// 契约：Postgres 模式在打开阶段只建立连接池（懒连接），
	// 不要求真实数据库可达；显式传入 pool 时直接复用。
	store, err := OpenStore(StoreConfig{
		Mode:                 StorePostgres,
		PostgresURL:          "postgres://statsverify:pw@127.0.0.1:1/juhe_stats?connect_timeout=1",
		PostgresMaxOpenConns: 10,
		PostgresMaxIdleConns: 5,
	})
	if err != nil {
		t.Fatalf("懒打开不应报错: %v", err)
	}
	if store.mode != StorePostgres || store.pool == nil {
		t.Fatalf("应为 postgres 模式且持有连接池: %#v", store)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("关闭连接池失败: %v", err)
	}
}

// ---- 方言辅助函数：SQLite / Postgres 双模式 ----

func TestStoreDialectHelpers(t *testing.T) {
	sqliteStore := &Store{mode: StoreSQLite}
	if got := sqliteStore.placeholder(1); got != "?" {
		t.Fatalf("sqlite 占位符应为 ?，实际: %s", got)
	}
	if got := sqliteStore.placeholders(3); got != "?, ?, ?" {
		t.Fatalf("sqlite 占位符串不符: %s", got)
	}
	if got := sqliteStore.placeholdersFrom(2, 2); got != "?, ?" {
		t.Fatalf("sqlite 起始占位符串不符: %s", got)
	}
	if got := sqliteStore.statsTable("usage_stats_daily"); got != "usage_stats_daily" {
		t.Fatalf("sqlite stats 表名不带前缀: %s", got)
	}
	if got := sqliteStore.businessTable("accounts"); got != "accounts" {
		t.Fatalf("sqlite business 表名不带前缀: %s", got)
	}
	if got := sqliteStore.usageTable("usage_records"); got != "usage_records" {
		t.Fatalf("sqlite usage 表名不带前缀: %s", got)
	}
	if got := sqliteStore.greatest("a", "b"); got != "MAX(a, b)" {
		t.Fatalf("sqlite 应使用 MAX: %s", got)
	}

	pgStore := &Store{mode: StorePostgres}
	if got := pgStore.placeholder(2); got != "$2" {
		t.Fatalf("postgres 占位符应为 $2: %s", got)
	}
	if got := pgStore.placeholders(2); got != "$1, $2" {
		t.Fatalf("postgres 占位符串不符: %s", got)
	}
	if got := pgStore.placeholdersFrom(3, 2); got != "$3, $4" {
		t.Fatalf("postgres 起始占位符串不符: %s", got)
	}
	if got := pgStore.statsTable("usage_stats_daily"); got != "juhe_stats.usage_stats_daily" {
		t.Fatalf("postgres stats 表应带 schema 前缀: %s", got)
	}
	if got := pgStore.businessTable("accounts"); got != "juhe_business.accounts" {
		t.Fatalf("postgres business 表应带 schema 前缀: %s", got)
	}
	if got := pgStore.usageTable("usage_records"); got != "juhe_usage.usage_records" {
		t.Fatalf("postgres usage 表应带 schema 前缀: %s", got)
	}
	if got := pgStore.greatest("a", "b"); got != "GREATEST(a, b)" {
		t.Fatalf("postgres 应使用 GREATEST: %s", got)
	}
}

func TestIsBusinessTableQuery(t *testing.T) {
	if !isBusinessTableQuery("SELECT value_json FROM system_settings WHERE key='x'") {
		t.Fatalf("system_settings 查询应路由到 business 库")
	}
	if isBusinessTableQuery("SELECT 1 FROM usage_records") {
		t.Fatalf("普通查询不应路由到 business 库")
	}
}

// ---- SQL 值转换辅助 ----

func TestSQLTimeVariants(t *testing.T) {
	expected := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if got, err := sqlTime(expected); err != nil || !got.Equal(expected) {
		t.Fatalf("time.Time 应原样 UTC 化: %v %v", got, err)
	}
	if got, err := sqlTime(sql.NullTime{Time: expected, Valid: true}); err != nil || !got.Equal(expected) {
		t.Fatalf("NullTime 有效值应转换: %v %v", got, err)
	}
	if _, err := sqlTime(sql.NullTime{}); err == nil {
		t.Fatalf("NullTime 无效值应报错")
	}
	if got, err := sqlTime("2026-01-02T03:04:05Z"); err != nil || !got.Equal(expected) {
		t.Fatalf("RFC3339 文本应可解析: %v %v", got, err)
	}
	if _, err := sqlTime(""); err == nil {
		t.Fatalf("空文本应报错")
	}
	if _, err := sqlTime("not-a-time"); err == nil {
		t.Fatalf("非 RFC3339 文本应报错")
	}
	if got, err := sqlTime([]byte("2026-01-02T03:04:05Z")); err != nil || !got.Equal(expected) {
		t.Fatalf("字节数本文本应可解析: %v %v", got, err)
	}
	if _, err := sqlTime([]byte("bad")); err == nil {
		t.Fatalf("字节数组非法文本应报错")
	}
	if _, err := sqlTime(42); err == nil {
		t.Fatalf("整型时间单元格应报错")
	}
}

func TestSQLTextVariants(t *testing.T) {
	if got, err := sqlText("a"); err != nil || got != "a" {
		t.Fatalf("字符串应原样: %v %v", got, err)
	}
	if got, err := sqlText([]byte("b")); err != nil || got != "b" {
		t.Fatalf("字节应转字符串: %v %v", got, err)
	}
	moment := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if got, err := sqlText(moment); err != nil || got != "2026-01-02T03:04:05.000Z" {
		t.Fatalf("时间应按 Node nowIso 格式化: %v %v", got, err)
	}
	if got, err := sqlText(sql.NullString{String: "c", Valid: true}); err != nil || got != "c" {
		t.Fatalf("NullString 有效值应转换: %v %v", got, err)
	}
	if got, err := sqlText(sql.NullString{}); err != nil || got != "" {
		t.Fatalf("NullString 无效值应为空串: %v %v", got, err)
	}
	if got, err := sqlText(nil); err != nil || got != "" {
		t.Fatalf("nil 应为空串: %v %v", got, err)
	}
	if _, err := sqlText(3.14); err == nil {
		t.Fatalf("浮点文本单元格应报错")
	}
}

func TestSQLIntVariants(t *testing.T) {
	cases := []struct {
		value any
		want  int64
	}{
		{int64(7), 7}, {int32(8), 8}, {int(9), 9}, {float64(10.9), 10}, {float32(11), 11},
		{"12", 12}, {[]byte("13"), 13}, {sql.NullInt64{Int64: 14, Valid: true}, 14}, {sql.NullInt64{}, 0}, {nil, 0},
	}
	for _, tc := range cases {
		got, err := sqlInt(tc.value)
		if err != nil || got != tc.want {
			t.Fatalf("sqlInt(%#v)=%d err=%v，期望 %d", tc.value, got, err, tc.want)
		}
	}
	if _, err := sqlInt("abc"); err == nil {
		t.Fatalf("非数字文本应报错")
	}
	if _, err := sqlInt(true); err == nil {
		t.Fatalf("布尔整数单元格应报错")
	}
}

func TestSQLFloatVariants(t *testing.T) {
	cases := []struct {
		value any
		want  float64
	}{
		{float64(1.5), 1.5}, {float32(2.5), 2.5}, {int64(3), 3}, {int32(4), 4},
		{"5.5", 5.5}, {nil, 0},
	}
	for _, tc := range cases {
		got, err := sqlFloat(tc.value)
		if err != nil || got != tc.want {
			t.Fatalf("sqlFloat(%#v)=%v err=%v，期望 %v", tc.value, got, err, tc.want)
		}
	}
	if _, err := sqlFloat("nan-text"); err == nil {
		t.Fatalf("非数字文本应报错")
	}
}

// ---- 时钟与日期纯函数 ----

func TestFixedClockAdvanceAndNilSafety(t *testing.T) {
	var nilClock *FixedClock
	if !nilClock.Now().IsZero() {
		t.Fatalf("nil 时钟应返回零值")
	}
	nilClock.Sleep(time.Minute) // 不应 panic
	clock := NewFixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	if got := clock.Now(); !got.Equal(clock.Current) {
		t.Fatalf("Now 应返回当前值")
	}
	clock.Sleep(time.Second)
	if got := clock.Current.Sub(time.Date(2026, 1, 2, 3, 4, 6, 0, time.UTC)); got != 0 {
		t.Fatalf("Sleep 应推进时钟，实际偏移: %v", got)
	}
}

func TestSystemClockNowIsCurrent(t *testing.T) {
	clock := SystemClock{}
	if delta := clock.Now().Sub(time.Now()).Abs(); delta > time.Minute {
		t.Fatalf("SystemClock 应接近当前时间，偏移: %v", delta)
	}
}

func TestAddCalendarDaysInBoundaries(t *testing.T) {
	loc := time.UTC
	cases := []struct {
		value string
		days  int
		want  string
	}{
		{"2026-01-31", 1, "2026-02-01"},
		{"2026-03-01", -1, "2026-02-28"},
		{"2024-02-28", 1, "2024-02-29"}, // 闰年
		{"2023-02-28", 1, "2023-03-01"}, // 平年
		{"2026-12-31", 1, "2027-01-01"}, // 跨年
	}
	for _, tc := range cases {
		if got := AddCalendarDaysIn(tc.value, tc.days, loc); got != tc.want {
			t.Fatalf("AddCalendarDaysIn(%s,%d)=%s，期望 %s", tc.value, tc.days, got, tc.want)
		}
	}
	// 非日期键原样返回。
	if got := AddCalendarDaysIn("bad-key", 3, loc); got != "bad-key" {
		t.Fatalf("非法日期键应原样返回，实际: %s", got)
	}
}

func TestParseDateKeyPartsStrictness(t *testing.T) {
	if parts, ok := parseDateKeyParts("2026-02-28"); !ok || parts.year != 2026 || parts.month != 2 || parts.day != 28 {
		t.Fatalf("合法日期应解析成功: %#v %v", parts, ok)
	}
	for _, bad := range []string{"2026-2-28", "2026/02/28", "20260228", "2026-13-01", "2026-02-30", "2026-00-10", ""} {
		if _, ok := parseDateKeyParts(bad); ok {
			t.Fatalf("非法日期键 %q 不应解析成功", bad)
		}
	}
}

func TestMinIntTable(t *testing.T) {
	cases := []struct{ left, right, want int }{
		{1, 2, 1}, {2, 1, 1}, {3, 3, 3}, {-1, 0, -1},
	}
	for _, tc := range cases {
		if got := minInt(tc.left, tc.right); got != tc.want {
			t.Fatalf("minInt(%d,%d)=%d，期望 %d", tc.left, tc.right, got, tc.want)
		}
	}
}

func TestSelectKeyColumnsAfterAndChunkStrings(t *testing.T) {
	if got := selectKeyColumnsAfter("account_id"); got != "account_id, " {
		t.Fatalf("有账号列时应返回前缀: %q", got)
	}
	if got := selectKeyColumnsAfter(""); got != "" {
		t.Fatalf("无账号列时应返回空串: %q", got)
	}
	chunks := chunkStrings([]string{"a", "b", "c", "d", "e"}, 2)
	if len(chunks) != 3 || strings.Join(chunks[0], "") != "ab" || strings.Join(chunks[2], "") != "e" {
		t.Fatalf("分块结果不符: %#v", chunks)
	}
	if chunks := chunkStrings([]string{"a"}, 0); len(chunks) != 1 || chunks[0][0] != "a" {
		t.Fatalf("size<1 应钳制为 1: %#v", chunks)
	}
}

// ---- 时区装载与脏标记 ----

func TestLoadUsageStatsLocationAndCache(t *testing.T) {
	store := openTestStore(t, "Asia/Shanghai")
	ctx := context.Background()
	now := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	location, timezone, err := store.LoadUsageStatsLocation(ctx, now)
	if err != nil || timezone != "Asia/Shanghai" || location == nil {
		t.Fatalf("装载时区失败: %v %s", err, timezone)
	}
	// 第二次读取命中进程内缓存（TTL 内），结果一致。
	_, timezoneAgain, err := store.LoadUsageStatsLocation(ctx, now.Add(time.Second))
	if err != nil || timezoneAgain != "Asia/Shanghai" {
		t.Fatalf("缓存读取失败: %v %s", err, timezoneAgain)
	}
}

func TestMarkAllGroupAccountStatsDirtyWritesMarker(t *testing.T) {
	store := openTestStore(t, "Asia/Shanghai")
	ctx := context.Background()
	if err := store.MarkAllGroupAccountStatsDirty(ctx, "wo-test", time.Date(2026, 9, 10, 1, 2, 3, 0, time.UTC)); err != nil {
		t.Fatalf("标记全量脏不应报错: %v", err)
	}
	var count int
	if err := store.business.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM group_account_stats_dirty WHERE group_id = ?`, groupAccountStatsDirtyAll).Scan(&count); err != nil {
		t.Fatalf("读取脏标记失败: %v", err)
	}
	if count != 1 {
		t.Fatalf("全量脏标记应存在 1 行，实际: %d", count)
	}
}
