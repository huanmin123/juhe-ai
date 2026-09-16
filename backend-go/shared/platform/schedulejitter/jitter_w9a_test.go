package schedulejitter

import (
	"testing"
	"time"
)

// TestW9AWindowCoversAllBranches 覆盖窗口选择的全部档位与上限收缩。
func TestW9AWindowCoversAllBranches(t *testing.T) {
	if got := Window(0); got != 0 {
		t.Fatalf("Window(0) = %v", got)
	}
	if got := Window(-time.Hour); got != 0 {
		t.Fatalf("Window(负值) = %v", got)
	}
	if got := Window(59 * time.Second); got != 29500*time.Millisecond {
		t.Fatalf("sub-minute 档 = %v", got)
	}
	if got := Window(90 * time.Second); got != MinuteWindow {
		t.Fatalf("minute 档 = %v", got)
	}
	if got := Window(2 * time.Hour); got != HourWindow {
		t.Fatalf("hour 档 = %v", got)
	}
	if got := Window(3 * 24 * time.Hour); got != DayWindow {
		t.Fatalf("day 档 = %v", got)
	}
	if got := Window(30 * 24 * time.Hour); got != WeekWindow {
		t.Fatalf("week 档 = %v", got)
	}
	// 59min 落在 minute 档（防御性收缩分支在各档位恒不可达：window 恒 ≤
	// interval/2，见报告不可达登记）。
	if got := Window(59 * time.Minute); got != MinuteWindow {
		t.Fatalf("59min 档 = %v", got)
	}
	if got := Window(2 * time.Hour); got > 2*time.Hour/2 {
		t.Fatalf("窗口不得超过间隔一半: %v", got)
	}
}

// TestW9AOffsetAndDelay 覆盖 Offset/Delay 的边界分支。
func TestW9AOffsetAndDelay(t *testing.T) {
	if got := Offset(0); got != 0 {
		t.Fatalf("Offset(0) = %v", got)
	}
	if got := Offset(-time.Minute); got != 0 {
		t.Fatalf("Offset(负) = %v", got)
	}
	for i := 0; i < 200; i++ {
		offset := Offset(time.Minute)
		if offset > MinuteWindow || offset < -MinuteWindow {
			t.Fatalf("偏移超出窗口: %v", offset)
		}
	}
	sawSubMillisecond := false
	for i := 0; i < 200 && !sawSubMillisecond; i++ {
		delay := Delay(time.Millisecond)
		if delay < time.Millisecond {
			sawSubMillisecond = true
		}
		if delay < time.Millisecond {
			t.Fatalf("Delay 永远不低于 1ms")
		}
	}
	if !sawSubMillisecond {
		t.Log("200 次内未观察到 <1ms 的原始延迟（概率行为，不作为失败）")
	}
	if got := Delay(0); got < time.Millisecond {
		t.Fatalf("Delay(0) 必须不低于 1ms: %v", got)
	}
}
