package processlog

import (
	"log/slog"
	"os"
	"runtime/debug"
)

// Fatal event names mirror the Node process diagnostics contract
// (shared/logger.ts installProcessLogHandlers).
const (
	EventProcessUncaughtException = "process_uncaught_exception"
	EventProcessAttributedEPIPE   = "process_unattributed_epipe"
)

// osExit is the process exit seam so tests can assert the fatal path.
var osExit = os.Exit

// CatchPanic mirrors the Node uncaughtException branch of
// installProcessLogHandlers: an unrecovered panic on the calling goroutine is
// logged as a fatal diagnostic and the process exits 1. Defer it at the very
// top of main. Panics on other goroutines are out of reach for a recover
// barrier — the runtime default applies (the supervisor wraps component
// goroutines itself).
func CatchPanic(logger *slog.Logger) {
	recovered := recover()
	if recovered == nil {
		return
	}
	if logger != nil {
		logger.Error("未捕获异常",
			"event", EventProcessUncaughtException,
			"err", recovered,
			"stack", string(debug.Stack()),
		)
	}
	osExit(1)
}

// KeepAliveOnBrokenOutputPipe mirrors the Node EPIPE branch of
// installProcessLogHandlers (logger.ts:958-976): writing the log stream into
// a broken pipe must not kill the process — the failed write is dropped and
// the service keeps running. Call it once at startup, next to the slog logger
// construction; the slog JSON handler drops write errors on its own, the
// platform shim only stops the runtime from turning the broken-pipe signal
// into process death.
func KeepAliveOnBrokenOutputPipe() {
	keepAliveOnBrokenOutputPipe()
}
