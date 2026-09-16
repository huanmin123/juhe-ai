package statreads

// w9b 纯函数补充：空汇总、错误率、日期范围规整。

import "testing"

func TestW9BEmptyAccountUsageSummary(t *testing.T) {
	summary := emptyAccountUsageSummary()
	if summary.RequestCount != 0 || summary.InputTokens != 0 || summary.OutputTokens != 0 {
		t.Fatalf("空汇总=%+v", summary)
	}
}

func TestW9BErrorRateArms(t *testing.T) {
	if got := errorRate(0, 3); got != 0 {
		t.Fatalf("零请求=%v", got)
	}
	if got := errorRate(4, 1); got != 0.25 {
		t.Fatalf("正常=%v", got)
	}
}

func TestW9BNormalizeRangeArms(t *testing.T) {
	// 反序输入回正。
	rng := normalizeRange("2026-09-10", "2026-09-01", "2026-09-15")
	if rng.StartDate > rng.EndDate {
		t.Fatalf("未交换：%s > %s", rng.StartDate, rng.EndDate)
	}
	// 空输入回退今天。
	fallback := normalizeRange("", "", "2026-09-15")
	if fallback.StartDate == "" || fallback.EndDate == "" {
		t.Fatalf("空输入=%+v", fallback)
	}
}
