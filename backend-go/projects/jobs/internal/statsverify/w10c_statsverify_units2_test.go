package statsverify

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"
)

// w10c_statsverify_units2_test.go 补 statsverify 第二轮可直达臂：OpenStore
// 配置校验错误臂、optionalTimestampText 纯函数、扫描器时间/整数纯函数。

func TestW10COpenStoreConfigValidation(t *testing.T) {
	// SQLite 缺路径。
	if _, err := OpenStore(StoreConfig{Mode: StoreSQLite}); err == nil || !strings.Contains(err.Error(), "缺少 stats 或 business 数据库路径") {
		t.Fatalf("SQLite 缺路径应报错, got %v", err)
	}
	// 非法 mode。
	if _, err := OpenStore(StoreConfig{Mode: "nope"}); err == nil {
		t.Fatalf("非法 mode 应报错")
	}
	// Postgres 缺 URL。
	if _, err := OpenStore(StoreConfig{Mode: StorePostgres}); err == nil {
		t.Fatalf("Postgres 缺 URL 应报错")
	}
	// Postgres max idle > open 校验。
	if _, err := OpenStore(StoreConfig{Mode: StorePostgres, PostgresURL: "postgres://x", PostgresMaxOpenConns: 1, PostgresMaxIdleConns: 5}); err == nil {
		t.Fatalf("max idle > open 应报错")
	}
	if _, err := OpenStore(StoreConfig{Mode: StorePostgres, PostgresURL: "postgres://x", PostgresMaxOpenConns: 0, PostgresMaxIdleConns: 0}); err == nil {
		t.Fatalf("max open 非法应报错")
	}
}

func TestW10COptionalTimestampText(t *testing.T) {
	// nil → 空串。
	if got, err := optionalTimestampText(nil, "测试"); err != nil || got != "" {
		t.Fatalf("nil = %q, %v", got, err)
	}
	// 空字符串 → 空串。
	if got, err := optionalTimestampText("", "测试"); err != nil || got != "" {
		t.Fatalf("空串 = %q, %v", got, err)
	}
	// 合法时间戳 → 原样。
	if got, err := optionalTimestampText("2026-09-06T12:00:00.000Z", "测试"); err != nil || got != "2026-09-06T12:00:00.000Z" {
		t.Fatalf("合法时间戳 = %q, %v", got, err)
	}
	// 非法时间戳 → 错误。
	if _, err := optionalTimestampText("garbage", "测试"); err == nil {
		t.Fatalf("非法时间戳应报错")
	}
	// sql.NullString 无效 → 空串；有效 → 值。
	if got, err := optionalTimestampText(sql.NullString{Valid: false}, "测试"); err != nil || got != "" {
		t.Fatalf("NullString 无效 = %q, %v", got, err)
	}
	if got, err := optionalTimestampText(sql.NullString{Valid: true, String: "2026-09-06T12:00:00.000Z"}, "测试"); err != nil || got != "2026-09-06T12:00:00.000Z" {
		t.Fatalf("NullString 有效 = %q, %v", got, err)
	}
	// 无法 sqlText 的类型 → 错误。
	if _, err := optionalTimestampText(struct{}{}, "测试"); err == nil {
		t.Fatalf("未知类型应报错")
	}
}

func TestW10CSQLTimeAndIntPtr(t *testing.T) {
	// sqlFloatPtr：nil → nil，值 → 指针。
	if got, err := sqlFloatPtr(nil); err != nil || got != nil {
		t.Fatalf("sqlFloatPtr nil = %v, %v", got, err)
	}
	if got, err := sqlFloatPtr(2.5); err != nil || got == nil || *got != 2.5 {
		t.Fatalf("sqlFloatPtr 值 = %v, %v", got, err)
	}
	// sqlIntPtr：nil → nil。
	if got, err := sqlIntPtr(nil); err != nil || got != nil {
		t.Fatalf("sqlIntPtr nil = %v, %v", got, err)
	}
	if got, err := sqlIntPtr(int64(3)); err != nil || got == nil || *got != 3 {
		t.Fatalf("sqlIntPtr 值 = %v, %v", got, err)
	}
	// sqlTextPtr：nil → nil。
	if got, err := sqlTextPtr(nil); err != nil || got != nil {
		t.Fatalf("sqlTextPtr nil = %v, %v", got, err)
	}
	// sqlText 全类型。
	if got, err := sqlText([]byte("bytes")); err != nil || got != "bytes" {
		t.Fatalf("sqlText []byte = %v, %v", got, err)
	}
	if got, err := sqlText("str"); err != nil || got != "str" {
		t.Fatalf("sqlText string = %v, %v", got, err)
	}
	if _, err := sqlText(struct{}{}); err == nil {
		t.Fatalf("sqlText 未知类型应报错")
	}
}

func TestW10CRememberAndForgetDirtyIPHashes(t *testing.T) {
	store := openTestStore(t, "UTC")
	hashes := []string{"aaa", "bbb"}
	// 初始无内存脏 hash。
	if store.hasInMemoryDirtyIPHashes() {
		t.Fatalf("初始不应有内存脏 hash")
	}
	store.rememberDirtyIPHashes(hashes)
	if !store.hasInMemoryDirtyIPHashes() {
		t.Fatalf("记入后应存在")
	}
	store.forgetDirtyIPHashes(hashes)
	if store.hasInMemoryDirtyIPHashes() {
		t.Fatalf("遗忘后应为空")
	}
}

func TestW10CConsistencyLatencyAndTimestamp(t *testing.T) {
	// groupAccountStatsAllCursor：nil reason → ""；带前缀 → 截取。
	if got := groupAccountStatsAllCursor(nil); got != "" {
		t.Fatalf("nil reason = %q", got)
	}
	if got := groupAccountStatsAllCursor(strPtr("plain")); got != "" {
		t.Fatalf("无前缀 reason = %q", got)
	}
	prefix := groupAccountStatsAllCursorPrefix + "cursor-1"
	if got := groupAccountStatsAllCursor(strPtr(prefix)); got != "cursor-1" {
		t.Fatalf("带前缀 reason = %q", got)
	}
	// uniqueStrings：去空白、去空、去重。
	if got := uniqueStrings([]string{" a ", "", "a", "b", "b"}); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("uniqueStrings = %v", got)
	}
	// NowIso 毫秒固定。
	if got := NowIso(time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)); got != "2026-09-06T12:00:00.000Z" {
		t.Fatalf("NowIso = %s", got)
	}
}

var _ = context.Background
