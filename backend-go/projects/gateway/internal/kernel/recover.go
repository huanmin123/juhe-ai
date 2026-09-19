package kernel

import (
	"log/slog"
	"net/http"
	"runtime/debug"
)

// recoverMiddleware converts a panicking downstream handler into the kernel
// JSON error contract: the client receives 500 {"message":"服务器内部错误"}
// with the standard X-Trace-Id response header instead of net/http's
// connection-level empty reply, and the panic is logged with the request
// traceId and stack. When the handler already committed bytes (streaming
// responses), no envelope can be written — the panic is only logged; the
// response stream then ends truncated but frame-complete (net/http writes the
// chunked terminator on normal return), so protocol-level end markers (如
// SSE [DONE]) remain the authoritative completion signal for stream clients.
func recoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		committed := &committedWriter{ResponseWriter: w}
		defer func() {
			if recovered := recover(); recovered != nil {
				traceID := ""
				if ctx := Context(r); ctx != nil {
					traceID = ctx.TraceID
				}
				slog.Default().Error("kernel 处理器 panic 已恢复",
					"traceId", traceID, "method", r.Method, "path", r.URL.Path,
					"panic", recovered, "stack", string(debug.Stack()))
				if !committed.wrote {
					WriteError(committed, http.StatusInternalServerError, "服务器内部错误")
				}
			}
		}()
		next.ServeHTTP(committed, r)
	})
}

// committedWriter tracks whether the response started so the recovery path
// knows whether an error envelope is still possible. Flush forwards so
// streaming handlers below keep their first-byte timing.
type committedWriter struct {
	http.ResponseWriter
	wrote bool
}

func (c *committedWriter) WriteHeader(status int) {
	c.wrote = true
	c.ResponseWriter.WriteHeader(status)
}

func (c *committedWriter) Write(body []byte) (int, error) {
	c.wrote = true
	return c.ResponseWriter.Write(body)
}

func (c *committedWriter) Flush() {
	if flusher, ok := c.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}
