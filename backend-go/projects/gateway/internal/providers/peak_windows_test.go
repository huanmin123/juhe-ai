package providers

// peak_windows_test.go 固化供应商高峰时段目录定义：DeepSeek 口径（工作日
// 09:00-12:00、14:00-18:00，Asia/Shanghai，2026-11-10 用户定稿）与无定义
// 供应商的 nil 省略语义。

import "testing"

func TestPeakWindowsForProviderDeepSeek(t *testing.T) {
	peak := peakWindowsForProvider("deepseek")
	if peak == nil {
		t.Fatalf("deepseek 高峰定义缺失")
	}
	if peak.Timezone != "Asia/Shanghai" {
		t.Fatalf("timezone = %q, want Asia/Shanghai", peak.Timezone)
	}
	if len(peak.Windows) != 2 {
		t.Fatalf("windows = %+v, want 2 段工作日高峰", peak.Windows)
	}
	want := []ProviderPeakWindow{
		{DaysOfWeek: []int{1, 2, 3, 4, 5}, Start: "09:00", End: "12:00"},
		{DaysOfWeek: []int{1, 2, 3, 4, 5}, Start: "14:00", End: "18:00"},
	}
	for index, window := range peak.Windows {
		if window.Start != want[index].Start || window.End != want[index].End {
			t.Fatalf("windows[%d] = %s-%s, want %s-%s", index, window.Start, window.End, want[index].Start, want[index].End)
		}
		for dayOffset, day := range window.DaysOfWeek {
			if day != want[index].DaysOfWeek[dayOffset] {
				t.Fatalf("windows[%d].daysOfWeek = %v", index, window.DaysOfWeek)
			}
		}
	}
}

func TestPeakWindowsForProviderWithoutDefinition(t *testing.T) {
	for _, code := range []string{"gpt", "anthropic", "", "unknown-provider"} {
		if peak := peakWindowsForProvider(code); peak != nil {
			t.Fatalf("供应商 %q 不应有高峰定义, got %+v", code, peak)
		}
	}
}
