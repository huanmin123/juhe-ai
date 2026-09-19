package gatewayaccounteffects

import (
	"io"
	"log/slog"
	"testing"
	"time"
)

// 本文件承载原 we_effects_facade_test.go 中与已删除 Node side-effect
// 半区无关的通用活测试（logger 适配、scheduler、纯函数），随
// PLAN-20260919T000723744Z 任务 B 的死簇删除迁入。
func TestWeLoggerAdapters(t *testing.T) {
	nop := NopLogger{}
	nop.Info(map[string]any{"event": "x"}, "info")
	nop.Warn(nil, "warn")
	nop.Error(map[string]any{"event": "y"}, "error")

	if _, ok := SlogLogger(nil).(NopLogger); !ok {
		t.Fatal("SlogLogger(nil) 应回落 NopLogger")
	}
	adapter := SlogLogger(slog.New(slog.NewTextHandler(io.Discard, nil)))
	adapter.Info(map[string]any{"event": "e", "accountId": "a-1"}, "消息")
	adapter.Warn(nil, "空字段")
	adapter.Error(map[string]any{"event": "z"}, "错误")
}

// ---------------------------------------------------------------------------
// clock.go：RealScheduler / sortInts / passiveScheduleNotBeforeDelayMs
// ---------------------------------------------------------------------------

func TestWeRealSchedulerFiresAndCancels(t *testing.T) {
	fired := make(chan struct{}, 1)
	scheduler := RealScheduler{}
	handle := scheduler.After(20, func() { fired <- struct{}{} })
	// 契约：Cancel 后定时器不再触发（20ms 足够长，取消必然先于触发）。
	handle.Cancel()
	select {
	case <-fired:
		t.Fatal("取消后的定时器不应触发")
	default:
	}
	handle2 := scheduler.After(1, func() { fired <- struct{}{} })
	select {
	case <-fired:
	case <-time.After(5 * time.Second):
		handle2.Cancel()
		t.Fatal("到期定时器未触发")
	}
	// 负延迟夹到 0：立刻触发。
	handle3 := scheduler.After(-5, func() { fired <- struct{}{} })
	select {
	case <-fired:
	case <-time.After(5 * time.Second):
		handle3.Cancel()
		t.Fatal("负延迟应夹到 0 立即触发")
	}
}

func TestWeSortInts(t *testing.T) {
	values := []int{3, 1, 2}
	sortInts(values)
	if values[0] != 1 || values[1] != 2 || values[2] != 3 {
		t.Fatalf("sortInts = %v", values)
	}
	single := []int{7}
	sortInts(single)
	if single[0] != 7 {
		t.Fatalf("单元素被改动: %v", single)
	}
	empty := []int{}
	sortInts(empty)
	if len(empty) != 0 {
		t.Fatalf("空切片被改动: %v", empty)
	}
}

func TestWePassiveScheduleNotBeforeDelayMs(t *testing.T) {
	// interval=1 时 jitter 窗口为 0，offset 为 0，必须回落 interval+1。
	if got := passiveScheduleNotBeforeDelayMs(1, func() float64 { return 0.5 }); got != 2 {
		t.Fatalf("notBefore(1) = %d, want 2", got)
	}
	// 正常窗口：sampled=0.25 产生负 offset，notBefore 取绝对值保证不早于外部硬期限。
	got := passiveScheduleNotBeforeDelayMs(60_000, func() float64 { return 0.25 })
	if got != 60_000+15_000 {
		t.Fatalf("notBefore(60000) = %d, want %d", got, 75_000)
	}
}
