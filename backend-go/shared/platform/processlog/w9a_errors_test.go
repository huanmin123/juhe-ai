package processlog

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

// TestW9ACatchPanicWithoutPanicReturnsNormally 无 panic 时 CatchPanic 直接返回。
func TestW9ACatchPanicWithoutPanicReturnsNormally(t *testing.T) {
	defer CatchPanic(slog.Default())
	// 到达这里即说明 recover()==nil 分支正常返回。
}

// TestW9ACatchPanicLogsAndExits 覆盖 panic → fatal 日志 → os.Exit(1)。
func TestW9ACatchPanicLogsAndExits(t *testing.T) {
	originalExit := osExit
	exitCode := 0
	osExit = func(code int) { exitCode = code }
	defer func() { osExit = originalExit }()

	var buffer bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buffer, nil))

	func() {
		defer CatchPanic(logger)
		panic("boom")
	}()

	if exitCode != 1 {
		t.Fatalf("fatal 退出码 = %d, want 1", exitCode)
	}
	logged := buffer.String()
	if !strings.Contains(logged, EventProcessUncaughtException) || !strings.Contains(logged, "boom") {
		t.Fatalf("fatal 日志缺失契约字段: %s", logged)
	}
}

// TestW9ACatchPanicNilLoggerStillExits 覆盖 nil logger 分支。
func TestW9ACatchPanicNilLoggerStillExits(t *testing.T) {
	originalExit := osExit
	exitCode := 0
	osExit = func(code int) { exitCode = code }
	defer func() { osExit = originalExit }()

	func() {
		defer CatchPanic(nil)
		panic("silent")
	}()

	if exitCode != 1 {
		t.Fatalf("nil logger 仍必须退出 1: %d", exitCode)
	}
}

// TestW9AKeepAliveOnBrokenOutputPipeIsSafe Windows 下的 keep-alive 是 no-op，
// 调用必须安全返回。
func TestW9AKeepAliveOnBrokenOutputPipeIsSafe(t *testing.T) {
	KeepAliveOnBrokenOutputPipe()
	keepAliveOnBrokenOutputPipe()
}

// TestW9ALoadStageDetailGateErrors 覆盖配置解析错误分支。
func TestW9ALoadStageDetailGateErrors(t *testing.T) {
	if _, err := LoadStageDetailGate(func(string) string { return "abc" }, true); err == nil || !strings.Contains(err.Error(), "必须配置为数字") {
		t.Fatalf("非数字配置必须报错: %v", err)
	}
	if _, err := LoadStageDetailGate(func(string) string { return "1001" }, true); err == nil || !strings.Contains(err.Error(), "0-1000") {
		t.Fatalf("超上限配置必须报错: %v", err)
	}
	if _, err := LoadStageDetailGate(func(string) string { return "-1" }, true); err == nil || !strings.Contains(err.Error(), "0-1000") {
		t.Fatalf("负配置必须报错: %v", err)
	}
	gate, err := LoadStageDetailGate(nil, true)
	if err != nil || gate.SamplePermille != DefaultTimingDetailSamplePermillePerformance {
		t.Fatalf("默认 performance 采样 = %+v err %v", gate, err)
	}
	gate, err = LoadStageDetailGate(nil, false)
	if err != nil || gate.SamplePermille != DefaultTimingDetailSamplePermilleFull {
		t.Fatalf("默认 standalone 采样 = %+v err %v", gate, err)
	}
}

// TestW9ATimingDetailSampledBranches 覆盖采样判定的各分支。
func TestW9ATimingDetailSampledBranches(t *testing.T) {
	// 非 performance 模式全采样。
	if !(StageDetailGate{}).TimingDetailSampled("trace") {
		t.Fatal("非 performance 模式必须全采样")
	}
	// performance + 0‰ 全不采样。
	zero := StageDetailGate{PerformanceMode: true, SamplePermille: 0}
	if zero.TimingDetailSampled("trace") {
		t.Fatal("0‰ 必须全不采样")
	}
	// performance + 1000‰ 全采样。
	full := StageDetailGate{PerformanceMode: true, SamplePermille: 1000}
	if !full.TimingDetailSampled("trace") {
		t.Fatal("1000‰ 必须全采样")
	}
	// 中间档位：确定性（同 traceID 同结果）且两个方向都出现。
	mid := StageDetailGate{PerformanceMode: true, SamplePermille: 500}
	sampled, unsampled := false, false
	for i := 0; i < 200 && !(sampled && unsampled); i++ {
		traceID := "trace-" + strings.Repeat("x", i%37)
		if mid.TimingDetailSampled(traceID) {
			sampled = true
		} else {
			unsampled = true
		}
	}
	if !sampled || !unsampled {
		t.Fatalf("500‰ 采样必须双向出现: sampled=%v unsampled=%v", sampled, unsampled)
	}
	if mid.TimingDetailSampled("trace") != mid.TimingDetailSampled("trace") {
		t.Fatal("同 traceID 采样必须稳定")
	}
}

// TestW9AShouldWriteStageDetailBranches 覆盖 stage 写入判定的组合分支。
func TestW9AShouldWriteStageDetailBranches(t *testing.T) {
	full := StageDetailGate{}
	// 失败类 outcome 恒写入。
	for _, outcome := range []string{"unexpected_failure", "expected_failure", "aborted"} {
		if !full.ShouldWriteStageDetail(outcome, "t", 0, 0) {
			t.Fatalf("outcome %s 必须恒写入", outcome)
		}
	}
	// standalone 全写入。
	if !full.ShouldWriteStageDetail("success", "t", 0, 0) {
		t.Fatal("standalone 模式必须全写入")
	}
	// performance + 压力超限 → 不写。
	perf := StageDetailGate{PerformanceMode: true, SamplePermille: 1000}
	if perf.ShouldWriteStageDetail("success", "t", 1000, 500) {
		t.Fatal("压力超限必须跳过")
	}
	// performance + 压力未超限 → 采样判定。
	if !perf.ShouldWriteStageDetail("success", "t", 0, 500) {
		t.Fatal("1000‰ + 无压力必须写入")
	}
	_ = context.Background()
}

// TestW9ALoadStageDetailGateFractionalAndSampledSurrogate 覆盖小数 permille
// 拒绝与 fnv1aUTF16 的代理对（非 BMP 字符）分支。
func TestW9ALoadStageDetailGateFractionalAndSampledSurrogate(t *testing.T) {
	if _, err := LoadStageDetailGate(func(string) string { return "50.5" }, true); err == nil || !strings.Contains(err.Error(), "必须配置为数字") {
		t.Fatalf("小数 permille 必须拒绝: %v", err)
	}
	mid := StageDetailGate{PerformanceMode: true, SamplePermille: 500}
	// 含 emoji（需要 UTF-16 代理对展开）的 traceID 必须稳定且两个方向都可达。
	emojiTraces := []string{"trace-😀", "trace-🚀-𝕏", "😀😀😀"}
	for _, traceID := range emojiTraces {
		first := mid.TimingDetailSampled(traceID)
		if first != mid.TimingDetailSampled(traceID) {
			t.Fatalf("emoji traceID 采样不稳定: %s", traceID)
		}
	}
	sampled, unsampled := false, false
	for i := 0; i < 4000 && !(sampled && unsampled); i++ {
		traceID := "trace-😀-" + strings.Repeat("y", i%61)
		if mid.TimingDetailSampled(traceID) {
			sampled = true
		} else {
			unsampled = true
		}
	}
	if !sampled || !unsampled {
		t.Fatalf("emoji traceID 采样必须双向出现: sampled=%v unsampled=%v", sampled, unsampled)
	}
}
