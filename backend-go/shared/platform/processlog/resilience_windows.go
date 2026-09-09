//go:build windows

package processlog

// keepAliveOnBrokenOutputPipe: Windows has no SIGPIPE — writes to a closed
// pipe return ERROR_BROKEN_PIPE / ERROR_NO_DATA, which the slog handler
// already drops. Nothing to install; the keep-alive contract holds as is.
func keepAliveOnBrokenOutputPipe() {}
