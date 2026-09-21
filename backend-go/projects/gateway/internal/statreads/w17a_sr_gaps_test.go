package statreads

// W17a 覆盖率补缺（statreads ≥95% 硬门）。补纯函数守卫臂与时区解析错误
// 臂：nullText 扫描 []byte、toFloat 的 int/float32 分支、startOfZonedDateKeyIso
// 的极值时区守卫（DST 安全二分的 guard 循环与不可解析返回）、
// truncateHealthReason 截断臂、normalizeRange 起点越界钳制臂，以及
// Timezone source 失败时 usageOverviewSummary / isCurrentUsageStatsDay 的
// 错误透传臂。全部测试串行（无 t.Parallel）。

import (
	"bytes"
	"context"
	"errors"
	"net/http/httptest"
	"testing"
	"time"
)

func TestW17aSrNullTextAndToFloatArms(t *testing.T) {
	// nullText.Scan：[]byte 分支。
	var text nullText
	if err := text.Scan([]byte("字节值")); err != nil || !text.Valid || text.String != "字节值" {
		t.Fatalf("text = %#v, err = %v", text, err)
	}
	// toFloat：int 与 float32 分支。
	if got, ok := toFloat(3); !ok || got != 3 {
		t.Fatalf("int = %v %v", got, ok)
	}
	if got, ok := toFloat(float32(2.5)); !ok || got != 2.5 {
		t.Fatalf("float32 = %v %v", got, ok)
	}
}

func TestW17aSrStartOfZonedDateKeyIsoGuardLoops(t *testing.T) {
	const key = "2026-09-21"
	// 东向超界时区（+49h 固定偏移）：触发起点下探守卫循环。
	east := time.FixedZone("w17a-east", 49*3600)
	if got := startOfZonedDateKeyIso(key, east); got == "" {
		t.Fatal("东向极值时区应能解析出当日起点")
	}
	// 西向超界时区（-73h 固定偏移）：触发起点上探守卫循环。
	west := time.FixedZone("w17a-west", -73*3600)
	if got := startOfZonedDateKeyIso(key, west); got == "" {
		t.Fatal("西向极值时区应能解析出当日起点")
	}
	// 守卫耗尽仍无法定位 → 返回空串。
	uneven := time.FixedZone("w17a-uneven", -440*3600)
	if got := startOfZonedDateKeyIso(key, uneven); got != "" {
		t.Fatalf("守卫耗尽应返回空串, got %q", got)
	}
	// 非法日期键 → 空串（基线臂）。
	if got := startOfZonedDateKeyIso("not-a-date", time.UTC); got != "" {
		t.Fatalf("非法日期键应返回空串, got %q", got)
	}
}

func TestW17aSrTruncateHealthReasonAndNormalizeRange(t *testing.T) {
	// 截断臂：超过 200 字符的原文被裁剪。
	long := bytes.Repeat([]byte("x"), healthSnapshotJobsReasonLimit+50)
	if got := truncateHealthReason(long); len(got) != healthSnapshotJobsReasonLimit {
		t.Fatalf("截断长度 = %d", len(got))
	}
	// normalizeRange：起点早于支持窗口 → 钳制到最早支持日。
	rng := normalizeRange("2020-01-01", "2099-01-01", "2026-09-21")
	if rng.StartDate != "2026-08-22" || rng.EndDate != "2026-09-21" {
		t.Fatalf("range = %#v", rng)
	}
	earliest, err := parseDateKeyForW17a("2026-08-22")
	if err != nil {
		t.Fatal(err)
	}
	if rng.StartDate != earliest {
		t.Fatalf("range = %#v", rng)
	}
}

// parseDateKeyForW17a 复用被测包的日期键解析，用于推导钳制后的期望起点。
func parseDateKeyForW17a(value string) (string, error) {
	if _, ok := dateKeyTime(value); !ok {
		return "", errors.New("测试内的期望日期键无效")
	}
	return value, nil
}

func TestW17aSrTimezoneErrorPropagationArms(t *testing.T) {
	deps := &Deps{
		Now: func() time.Time { return time.Date(2026, 9, 21, 10, 0, 0, 0, time.UTC) },
		Timezone: func(context.Context) (string, error) {
			return "", errors.New("时区源故障")
		},
	}
	request := httptest.NewRequest("GET", "/__aisys__/api/stats/usage-window", nil)
	rng := Range{StartDate: "2026-09-21", EndDate: "2026-09-21"}
	// usageOverviewSummary：isCurrentUsageStatsDay 错误透传臂。
	if _, err := deps.usageOverviewSummary(request, AccessScope{}, rng); err == nil {
		t.Fatal("时区源故障应透传错误")
	}
	// isCurrentUsageStatsDay：timezoneLocation 错误臂。
	if _, err := deps.isCurrentUsageStatsDay(request, rng); err == nil {
		t.Fatal("时区源故障应透传错误")
	}
}
