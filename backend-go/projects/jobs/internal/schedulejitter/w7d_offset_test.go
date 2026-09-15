package schedulejitter

// w7d（schedulejitter 覆盖补齐）：直接驱动 Offset/Delay 委托路径与常量映射。
// 纯进程内确定性，不依赖外部资源。

import (
	"testing"
	"time"
)

func TestW7DOffsetDelegatesAndNeverZeroForPositiveWindow(t *testing.T) {
	if got := Offset(0); got != 0 {
		t.Fatalf("Offset(0) = %s, want 0", got)
	}
	if got := Offset(-time.Second); got != 0 {
		t.Fatalf("Offset(-1s) = %s, want 0", got)
	}
	interval := 10 * time.Minute
	window := Window(interval)
	for i := 0; i < 500; i++ {
		offset := Offset(interval)
		if offset < -window || offset > window {
			t.Fatalf("offset %s 超出窗口 ±%s", offset, window)
		}
		if offset == 0 {
			t.Fatal("正窗口下 offset 不得为 0（实现规范化为 +1ms）")
		}
	}
}

func TestW7DDelayCoversNonPositiveIntervalFloor(t *testing.T) {
	for i := 0; i < 100; i++ {
		if delay := Delay(0); delay < time.Millisecond {
			t.Fatalf("Delay(0) = %s, 必须 >= 1ms", delay)
		}
		if delay := Delay(-time.Hour); delay < time.Millisecond {
			t.Fatalf("Delay(-1h) = %s, 必须 >= 1ms", delay)
		}
	}
}

func TestW7DWindowConstantsMatchPlatform(t *testing.T) {
	cases := []struct {
		name     string
		window   time.Duration
		interval time.Duration
	}{
		{"sub-minute", SubMinuteWindow, 30 * time.Second},
		{"minute", MinuteWindow, 30 * time.Second},
		{"hour", HourWindow, 30 * time.Minute},
		{"day", DayWindow, time.Hour},
		{"week", WeekWindow, 8 * time.Hour},
	}
	for _, tc := range cases {
		if tc.window <= 0 {
			t.Fatalf("%s 窗口必须为正: %s", tc.name, tc.window)
		}
	}
	// Window 在各档位返回的窗口必须等于对应常量。
	if Window(time.Second) != time.Second/2 || Window(time.Second) > SubMinuteWindow {
		t.Fatalf("亚分钟窗口不正确: %s", Window(time.Second))
	}
	if Window(time.Minute) != MinuteWindow || Window(59*time.Minute) != MinuteWindow {
		t.Fatal("分钟档窗口不正确")
	}
	if Window(time.Hour) != HourWindow || Window(23*time.Hour) != HourWindow {
		t.Fatal("小时档窗口不正确")
	}
	if Window(24*time.Hour) != DayWindow || Window(6*24*time.Hour) != DayWindow {
		t.Fatal("天档窗口不正确")
	}
	if Window(7*24*time.Hour) != WeekWindow {
		t.Fatal("周档窗口不正确")
	}
}
