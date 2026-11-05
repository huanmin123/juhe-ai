package processlog

import (
	"bytes"
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
