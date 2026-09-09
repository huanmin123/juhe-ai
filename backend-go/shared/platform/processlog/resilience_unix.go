//go:build unix

package processlog

import (
	"os"
	"os/signal"
	"syscall"
)

// keepAliveOnBrokenOutputPipe implements the Unix half of the Node EPIPE
// keep-alive. os.File.Write raises a fatal SIGPIPE when fd 1/2 fails with
// EPIPE (os/signal "SIGPIPE" contract); registering a handler for SIGPIPE
// converts that death into an ordinary EPIPE error returned to the slog
// handler, which drops the write and keeps serving — exactly the Node
// “进程输出管道已关闭，保留服务并等待日志目的地降级” behaviour. The channel is
// never drained: buffered delivery simply drops further signals.
func keepAliveOnBrokenOutputPipe() {
	channel := make(chan os.Signal, 1)
	signal.Notify(channel, syscall.SIGPIPE)
}
