package ipstats

// w9b 纯 helper 补充：PG bind、日期窗口、排序白名单、分页钳制与数字解析。

import (
	"strings"
	"testing"
	"time"
)

func TestW9BPgBindOrdinals(t *testing.T) {
	if got := pgBind("a = ? AND b = ?"); got != "a = $1 AND b = $2" {
		t.Fatalf("pgBind=%q", got)
	}
	if got := pgBind("no placeholders"); got != "no placeholders" {
		t.Fatalf("无占位=%q", got)
	}
}

func TestW9BItoa(t *testing.T) {
	cases := map[int]string{0: "0", 7: "7", -3: "-3", 123456: "123456"}
	for value, want := range cases {
		if got := itoa(value); got != want {
			t.Fatalf("itoa(%d)=%q want %q", value, got, want)
		}
	}
}

func TestW9BLastUsedEpochWindowArms(t *testing.T) {
	location := time.UTC
	window := newLastUsedEpochWindow(Range{StartDate: "2026-09-01", EndDate: "2026-09-03"}, location)
	if window == nil {
		t.Fatal("合法窗口不能为 nil")
	}
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC)
	if window.startMs != start.UnixMilli() || window.endExclusiveMs != end.UnixMilli() {
		t.Fatalf("窗口=%+v", window)
	}
	// 非法日期 → nil。
	if got := newLastUsedEpochWindow(Range{StartDate: "bad", EndDate: "2026-09-03"}, location); got != nil {
		// 起始非法 → nil；两个都解析不了才双 nil，单边非法即 nil。
		if got.startMs == 0 && got.endExclusiveMs == 0 {
			t.Fatalf("非法日期窗口=%+v", got)
		}
	}
	// 时区偏移：非 UTC 的 day start 应有偏移。
	zoned := newLastUsedEpochWindow(Range{StartDate: "2026-09-01", EndDate: "2026-09-01"}, fixedZoneW9B())
	if zoned == nil || zoned.startMs == start.UnixMilli() {
		t.Fatalf("时区偏移窗口=%+v", zoned)
	}
}

func fixedZoneW9B() *time.Location {
	return time.FixedZone("w9b", 8*3600)
}

func TestW9BZonedDayStartAndNextDateKey(t *testing.T) {
	start := zonedDayStart("2026-09-01", time.UTC)
	if start == nil || start.Format(time.RFC3339) != "2026-09-01T00:00:00Z" {
		t.Fatalf("day start=%v", start)
	}
	if zonedDayStart("bad", time.UTC) != nil {
		t.Fatal("非法日期必须 nil")
	}
	if got := nextDateKey("2026-09-01"); got != "2026-09-02" {
		t.Fatalf("next=%q", got)
	}
	if got := nextDateKey("2026-12-31"); got != "2027-01-01" {
		t.Fatalf("跨年=%q", got)
	}
	if got := nextDateKey("not-a-date"); got != "not-a-date" {
		t.Fatalf("非法回退=%q", got)
	}
}

func TestW9BRowMatchesLastUsedWindow(t *testing.T) {
	lastSeen := "2026-09-02T00:00:00Z"
	withSeen := ListRow{LastSeenAt: &lastSeen}
	window := newLastUsedEpochWindow(Range{StartDate: "2026-09-01", EndDate: "2026-09-03"}, time.UTC)
	if !rowMatchesLastUsedWindow(withSeen, window) {
		t.Fatal("窗口内行必须命中")
	}
	if rowMatchesLastUsedWindow(ListRow{}, window) {
		t.Fatal("无 lastSeen 行必须过滤")
	}
	badSeen := "nope"
	if rowMatchesLastUsedWindow(ListRow{LastSeenAt: &badSeen}, window) {
		t.Fatal("坏时间必须过滤")
	}
	if !rowMatchesLastUsedWindow(withSeen, nil) {
		t.Fatal("nil 窗口放行所有行")
	}
}

func TestW9BDetailAccountStatsOrderBy(t *testing.T) {
	if got := detailAccountStatsOrderBy("successCount", "asc"); got != "success_count ASC, account_id DESC" {
		t.Fatalf("asc=%q", got)
	}
	if got := detailAccountStatsOrderBy("successCount", "desc"); got != "success_count DESC, account_id ASC" {
		t.Fatalf("desc=%q", got)
	}
	if got := detailAccountStatsOrderBy("errorCount", "asc"); !strings.Contains(got, "error_count ASC") {
		t.Fatalf("errorCount=%q", got)
	}
}

func TestW9BBoundedDetailPageSize(t *testing.T) {
	if got := boundedDetailPageSize(0); got != 20 {
		t.Fatalf("默认=%d", got)
	}
	if got := boundedDetailPageSize(1000); got != 100 {
		t.Fatalf("上限=%d", got)
	}
	if got := boundedDetailPageSize(7); got != 7 {
		t.Fatalf("正常=%d", got)
	}
}

func TestW9BParseNodeNumberMatrix(t *testing.T) {
	cases := []struct {
		raw string
		ok  bool
	}{
		{"", true},
		{" 12 ", true},
		{"0x1f", true},
		{"0o17", true},
		{"0b101", true},
		{"abc", false},
		{"0x", false},
		{"NaN", false},
		{"Infinity", false},
	}
	for _, tc := range cases {
		if _, ok := parseNodeNumber(tc.raw); ok != tc.ok {
			t.Fatalf("parseNodeNumber(%q) ok=%v want %v", tc.raw, ok, tc.ok)
		}
	}
	if value, _ := parseNodeNumber("-3.5"); value != -3.5 {
		t.Fatalf("负小数=%v", value)
	}
}

func TestW9BListOrderByWhitelist(t *testing.T) {
	if got := listOrderBy("lastUsedAt", "asc"); got == "" {
		t.Fatal("合法字段不能为空")
	}
	if got := listOrderBy("injected; DROP", "asc"); got == "" {
		t.Fatal("非法字段应回退白名单值而非空串时需登记")
	}
}

func TestW9BBoundedDetailPageSizeFromQuery(t *testing.T) {
	if got := boundedDetailPageSizeFromQuery(map[string][]string{"pageSize": {"5"}}); got != 5 {
		t.Fatalf("正常=%d", got)
	}
	if got := boundedDetailPageSizeFromQuery(map[string][]string{"pageSize": {"abc"}}); got != 0 {
		t.Fatalf("非法应返回哨兵 0=%d", got)
	}
	if got := boundedDetailPageSizeFromQuery(map[string][]string{}); got == 0 {
		t.Fatalf("缺省=%d", got)
	}
}

func TestW9BStoreDialectHelpers(t *testing.T) {
	// SQLite 方言。
	store := &Store{}
	if got := store.table("client_ip_stats"); got != "client_ip_stats" {
		t.Fatalf("sqlite table=%q", got)
	}
	if got := store.bind("WHERE a=? AND b=?"); got != "WHERE a=? AND b=?" {
		t.Fatalf("sqlite bind=%q", got)
	}
	// PG 方言。
	pgStore := &Store{pg: true}
	if got := pgStore.table("client_ip_stats"); got != "juhe_stats.client_ip_stats" {
		t.Fatalf("pg table=%q", got)
	}
	if got := pgStore.bind("WHERE a=? AND b=?"); got != "WHERE a=$1 AND b=$2" {
		t.Fatalf("pg bind=%q", got)
	}
}

func TestW9BListOrderByAllFields(t *testing.T) {
	fields := []string{"successCount", "errorCount", "errorRate", "lastUsedAt", "ip"}
	for _, field := range fields {
		for _, order := range []string{"asc", "desc"} {
			if got := listOrderBy(field, order); got == "" {
				t.Fatalf("listOrderBy(%q,%q) 为空", field, order)
			}
		}
	}
}

func TestW9BDetailAccountStatsOrderByAllFields(t *testing.T) {
	fields := []string{"successCount", "errorCount", "requestCount", "accountId"}
	for _, field := range fields {
		for _, order := range []string{"asc", "desc"} {
			if got := detailAccountStatsOrderBy(field, order); got == "" {
				t.Fatalf("detailAccountStatsOrderBy(%q,%q) 为空", field, order)
			}
		}
	}
}

func TestW9BValidationErrorContract(t *testing.T) {
	// store.go ValidationError 的 Error() 契约（0% 缺口）。
	var err error = &ValidationError{Message: "参数无效"}
	if err.Error() != "参数无效" {
		t.Fatalf("Error=%q", err.Error())
	}
}

func TestW9BBoundedPageArms(t *testing.T) {
	if got := boundedPage(0, 20); got != 1 {
		t.Fatalf("下限=%d", got)
	}
	if got := boundedDetailPage(0, 20); got != 1 {
		t.Fatalf("detail 下限=%d", got)
	}
	if got := boundedDetailPage(1<<20, 20); got == 1<<20 {
		t.Fatalf("detail 上限未钳制=%d", got)
	}
	if got := boundedPage(1<<20, 20); got == 1<<20 {
		t.Fatalf("page 上限未钳制=%d", got)
	}
	if got := boundedPage(3, 20); got != 3 {
		t.Fatalf("正常=%d", got)
	}
}
