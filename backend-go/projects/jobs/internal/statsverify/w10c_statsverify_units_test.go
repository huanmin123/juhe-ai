package statsverify

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

// w10c_statsverify_units_test.go 覆盖 statsverify 剩余可直达臂：client-ip
// 写入/归一/扫描纯函数、时钟、lag、游标参数转换，以及少量 DB 驱动的行为
// 与错误臂（LoadUsageStatsLocation 非法时区、忽略游标非法时间戳、空 store）。

func TestW10CTrimSpacesAndSpaceByte(t *testing.T) {
	// 六种空白字节全部归类。
	for _, b := range []byte{' ', '\t', '\n', '\v', '\f', '\r'} {
		if !isSpaceByte(b) {
			t.Fatalf("字节 %q 应判空白", b)
		}
	}
	if isSpaceByte('x') {
		t.Fatalf("'x' 不应判空白")
	}
	// trimSpaces：全空白、前后空白、无空白、混合换行。
	if got := trimSpaces("\t \n  abc \v\f\r "); got != "abc" {
		t.Fatalf("trimSpaces = %q", got)
	}
	if got := trimSpaces("   "); got != "" {
		t.Fatalf("trimSpaces 全空白 = %q", got)
	}
	if got := trimSpaces("abc"); got != "abc" {
		t.Fatalf("trimSpaces 无空白 = %q", got)
	}
}

func TestW10CClientIPIdentityStable(t *testing.T) {
	// 同一 key 生成稳定 hash/bucket，bucket 落在 [0,4096)。
	first := clientIPIdentity("1.2.3.4", "1.2.3.4", 4)
	second := clientIPIdentity("1.2.3.4", "1.2.3.4", 4)
	if first == nil || second == nil || first.IPHash != second.IPHash || first.BucketNo != second.BucketNo {
		t.Fatalf("clientIPIdentity 应稳定: %+v vs %+v", first, second)
	}
	if first.BucketNo < 0 || first.BucketNo >= ClientIPRegistryBucketCount {
		t.Fatalf("bucket 越界: %d", first.BucketNo)
	}
	if len(first.IPHash) != 64 {
		t.Fatalf("ipHash 长度 = %d", len(first.IPHash))
	}
	// 不同 key 产生不同 hash。
	other := clientIPIdentity("1.2.3.4", "5.6.7.8", 4)
	if first.IPHash == other.IPHash {
		t.Fatalf("不同 key 不应同 hash")
	}
}

func TestW10CSQLNumberVariants(t *testing.T) {
	// sqlFloat 全类型臂。
	for _, testCase := range []struct {
		value any
		want  float64
	}{
		{float64(1.5), 1.5},
		{float32(2.5), 2.5},
		{int64(3), 3},
		{int32(4), 4},
		{"5.25", 5.25},
		{[]byte("6.5"), 6.5},
		{sql.NullFloat64{Valid: false}, 0},
		{sql.NullFloat64{Valid: true, Float64: 7}, 7},
		{nil, 0},
	} {
		got, err := sqlFloat(testCase.value)
		if err != nil || got != testCase.want {
			t.Fatalf("sqlFloat(%v) = %v, %v want %v", testCase.value, got, err, testCase.want)
		}
	}
	if _, err := sqlFloat("not-a-number"); err == nil {
		t.Fatalf("sqlFloat 非法字符串应报错")
	}
	if _, err := sqlFloat(struct{}{}); err == nil {
		t.Fatalf("sqlFloat 未知类型应报错")
	}

	// sqlInt 补臂。
	for _, testCase := range []struct {
		value any
		want  int64
	}{
		{int64(9), 9},
		{int32(10), 10},
		{int(11), 11},
		{float64(12), 12},
		{float32(13), 13},
		{"14", 14},
		{[]byte("15"), 15},
		{sql.NullInt64{Valid: false}, 0},
		{sql.NullInt64{Valid: true, Int64: 16}, 16},
		{nil, 0},
	} {
		got, err := sqlInt(testCase.value)
		if err != nil || got != testCase.want {
			t.Fatalf("sqlInt(%v) = %v, %v want %v", testCase.value, got, err, testCase.want)
		}
	}
	if _, err := sqlInt("not-an-int"); err == nil {
		t.Fatalf("sqlInt 非法字符串应报错")
	}
	if _, err := sqlInt(struct{}{}); err == nil {
		t.Fatalf("sqlInt 未知类型应报错")
	}
	// sqlStringPtr：nil → nil，字符串 → 指针。
	if got, err := sqlStringPtr(nil); err != nil || got != nil {
		t.Fatalf("sqlStringPtr nil = %v, %v", got, err)
	}
	if got, err := sqlStringPtr("x"); err != nil || got == nil || *got != "x" {
		t.Fatalf("sqlStringPtr string = %v, %v", got, err)
	}
}

func TestW10CClockBoundaries(t *testing.T) {
	base := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)

	// SystemClock 真实 Sleep（短）。
	SystemClock{}.Sleep(1 * time.Millisecond)

	// FixedClock nil 接收器安全臂。
	var nilClock *FixedClock
	if !nilClock.Now().IsZero() {
		t.Fatalf("nil FixedClock.Now 应返回零值")
	}
	nilClock.Sleep(5 * time.Second)

	// FixedClock Advance：Sleep 推进内部时钟。
	clock := NewFixedClock(base)
	if !clock.Now().Equal(base) {
		t.Fatalf("初始时间错误")
	}
	clock.Sleep(2 * time.Second)
	if !clock.Now().Equal(base.Add(2 * time.Second)) {
		t.Fatalf("Sleep 未推进时钟")
	}
	// NowIso 固定毫秒 + Z。
	if got := NowIso(base); got != "2026-09-06T12:00:00.000Z" {
		t.Fatalf("NowIso = %s", got)
	}
}

func TestW10CCursorLagSeconds(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	// 非法时间戳 → 0。
	if got := cursorLagSeconds("garbage", now); got != 0 {
		t.Fatalf("非法时间戳 lag = %d want 0", got)
	}
	// 未来时间戳 → 负 lag 钳制 0。
	if got := cursorLagSeconds("2026-09-06T12:01:00.000Z", now); got != 0 {
		t.Fatalf("未来 lag = %d want 0", got)
	}
	// 有效历史时间戳 → 秒数。
	if got := cursorLagSeconds("2026-09-06T11:59:30.000Z", now); got != 30 {
		t.Fatalf("lag = %d want 30", got)
	}
}

func TestW10CArgConverters(t *testing.T) {
	if intPointerArg(nil) != nil || intPointerArg(intPtr(7)) != 7 {
		t.Fatalf("intPointerArg 断言失败")
	}
	if textPointerArg(nil) != nil || textPointerArg(strPtr("x")) != "x" {
		t.Fatalf("textPointerArg 断言失败")
	}
	if nilIfEmpty("") != nil || nilIfEmpty("x") == nil || *nilIfEmpty("x") != "x" {
		t.Fatalf("nilIfEmpty 断言失败")
	}
}

func TestW10CAggregateBatchNilStoreErrors(t *testing.T) {
	if _, err := (*Store)(nil).AggregateClientIPStatsBatch(context.Background(), 1, time.Now()); err == nil {
		t.Fatalf("nil store 应报错")
	}
	if _, err := (&Store{}).AggregateClientIPStatsBatch(context.Background(), 1, time.Now()); err == nil {
		t.Fatalf("空 store 应报错")
	}
	if err := (*Store)(nil).MarkAllGroupAccountStatsDirty(context.Background(), "reason", time.Now()); err == nil {
		t.Fatalf("nil store MarkAll 应报错")
	}
	if _, err := (*Store)(nil).RefreshDirtyGroupAccountStats(context.Background(), GroupAccountStatsRefreshOptions{}); err == nil {
		t.Fatalf("nil store Refresh 应报错")
	}
}

func TestW10CLoadUsageStatsLocationInvalidTimezone(t *testing.T) {
	store := openTestStore(t, "UTC")
	ctx := context.Background()
	// 覆盖时区为非法值 → LoadUsageStatsLocation 错误臂。
	mustExec(t, ctx, store.business,
		`UPDATE system_settings SET value_json = ? WHERE key = 'usageStatsTimezone'`, mustJSONString("Not/AZone"))
	if _, _, err := store.LoadUsageStatsLocation(ctx, time.Now()); err == nil {
		t.Fatalf("非法时区应报错")
	}
}

func TestW10CIgnoredCursorInvalidTimestampError(t *testing.T) {
	store := openTestStore(t, "UTC")
	ctx := context.Background()
	// 一条被排除流量来源 + 非法 created_at 的记录（字符串 '0000-...' 小于
	// safeCreatedBefore，能通过过滤；ParseRFC3339 失败触发忽略游标错误臂）。
	insertUsageRecord(t, ctx, store, UsageStatsRecordRow{
		ID: "w10c-ignored", SystemAccountID: "alice", TrafficSource: "runtime_recovery_probe",
		CreatedAt: "0000-00-00T00:00:00.000Z",
	})
	now := fixedUTC(t, "2026-09-06T12:00:00Z").Now()
	if _, err := store.AggregateClientIPStatsBatch(ctx, 10, now); err == nil {
		t.Fatalf("非法忽略游标时间戳应报错")
	}
}
