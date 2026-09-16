package schedulejitter

// w12h 补充 arms：Window 的亚分钟档与 Offset 的 offset==0 收口分支。
// Window 原有的两处恒假上限钳制已按 w12h 授权删除（亚分钟档 window =
// interval/2 < 30s 恒小于 SubMinuteWindow；各档 window 均不超过 interval/2）。

import (
	"testing"
	"time"
)

func TestW12HSubMinuteWindowUsesHalfInterval(t *testing.T) {
	if got := Window(2 * time.Nanosecond); got != time.Nanosecond {
		t.Fatalf("Window(2ns)=%s, want 1ns", got)
	}
	if got := Window(59 * time.Second); got != 29*time.Second+500*time.Millisecond {
		t.Fatalf("Window(59s)=%s, want 29.5s", got)
	}
	if got := Window(0); got != 0 {
		t.Fatalf("Window(0)=%s, want 0", got)
	}
}

func TestW12HOffsetZeroCollapsesToMillisecond(t *testing.T) {
	// window=1ns 时 rand.Int63n(3) 命中 window 本身的概率为 1/3，少量循环
	// 内必然触发 offset==0 → 1ms 的收口分支。
	sawMillisecond := false
	for i := 0; i < 500 && !sawMillisecond; i++ {
		if got := Offset(2 * time.Nanosecond); got == time.Millisecond {
			sawMillisecond = true
		} else if got != -time.Nanosecond && got != time.Nanosecond {
			t.Fatalf("Offset(2ns)=%s 超出 ±1ns 窗口", got)
		}
	}
	if !sawMillisecond {
		t.Fatal("500 次 1ns 窗口采样未命中 offset==0 收口分支")
	}
	if got := Offset(0); got != 0 {
		t.Fatalf("Offset(0)=%s, want 0", got)
	}
}
