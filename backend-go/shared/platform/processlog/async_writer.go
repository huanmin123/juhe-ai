// async_writer.go —— 运行日志文件路的异步有界写侧（2026-11-09，T192）。
//
// 背景：FileSink.Write 是同步 append + 进程内互斥。本地磁盘为 page cache
// 写入时成本很低，但只要日志目录被换成 NFS/云 NAS 或底层盘卡死，一次 write
// 就可能无限阻塞，进而把持有同一个 slog handler 的请求协程全部拖住。
//
// 这里把「文件路」包成单 worker 有界队列：请求协程只做一次定长 copy 和
// 非阻塞入队；队列满时丢弃该条日志（日志是非业务数据，允许取舍），绝不
// 阻塞请求。stdout 路仍由 TeeWriter 同步写，保证进程前台日志的即时性。
package processlog

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
	"time"
)

const (
	defaultAsyncQueueCapacity = 4096
	defaultAsyncCloseTimeout  = 2 * time.Second
)

var ErrAsyncWriterClosed = errors.New("processlog: async writer closed")

// AsyncWriter 把写入转交给单 worker 异步落盘；实现 io.Writer 与 io.Closer。
type AsyncWriter struct {
	writer io.Writer
	events chan asyncWriteEvent
	stop   chan struct{}
	done   chan struct{}
	closed atomic.Bool
	// dropped 统计队列满/关闭后丢弃的记录数（观测用）。
	dropped atomic.Int64
}

type asyncWriteEvent struct {
	data    []byte
	barrier chan struct{}
}

// NewAsyncWriter 启动异步写 worker。QueueCapacity <= 0 时取默认 4096。
func NewAsyncWriter(writer io.Writer, queueCapacity int) *AsyncWriter {
	if queueCapacity <= 0 {
		queueCapacity = defaultAsyncQueueCapacity
	}
	async := &AsyncWriter{
		writer: writer,
		events: make(chan asyncWriteEvent, queueCapacity),
		stop:   make(chan struct{}),
		done:   make(chan struct{}),
	}
	go async.run()
	return async
}

func (w *AsyncWriter) Write(p []byte) (int, error) {
	// 与 Close 的 closed 检查存在极窄窗口：检查通过后 Close 关闭 stop、
	// worker 退出，事件可能留在队列无人消费。日志允许丢，且 Flush 的 ctx
	// 超时兜底，不会让调用方无限等待。
	if w.closed.Load() {
		return 0, ErrAsyncWriterClosed
	}
	data := make([]byte, len(p))
	copy(data, p)
	select {
	case w.events <- asyncWriteEvent{data: data}:
	default:
		w.dropped.Add(1)
	}
	// 对 slog 恒报成功：丢弃是写侧策略，不能让日志错误反向影响请求。
	return len(p), nil
}

func (w *AsyncWriter) run() {
	defer close(w.done)
	for {
		select {
		case <-w.stop:
			w.drain()
			return
		case event := <-w.events:
			if event.barrier != nil {
				close(event.barrier)
				continue
			}
			_, _ = w.writer.Write(event.data)
		}
	}
}

func (w *AsyncWriter) drain() {
	for {
		select {
		case event := <-w.events:
			if event.barrier != nil {
				close(event.barrier)
				continue
			}
			_, _ = w.writer.Write(event.data)
		default:
			return
		}
	}
}

// Flush 等待此前入队的记录全部写出；ctx 必须有超时。已关闭时直接返回。
func (w *AsyncWriter) Flush(ctx context.Context) error {
	if w.closed.Load() {
		return nil
	}
	barrier := make(chan struct{})
	select {
	case w.events <- asyncWriteEvent{barrier: barrier}:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-barrier:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close 停止 worker（尽力排空队列）并关闭下游 writer（若实现 io.Closer）。
func (w *AsyncWriter) Close() error {
	stopCtx, cancel := context.WithTimeout(context.Background(), defaultAsyncCloseTimeout)
	defer cancel()
	return w.CloseContext(stopCtx)
}

// CloseContext 是 Close 的有界版本。
func (w *AsyncWriter) CloseContext(ctx context.Context) error {
	if w.closed.CompareAndSwap(false, true) {
		close(w.stop)
	}
	var err error
	select {
	case <-w.done:
	case <-ctx.Done():
		err = ctx.Err()
	}
	if closer, ok := w.writer.(io.Closer); ok {
		if closeErr := closer.Close(); err == nil {
			err = closeErr
		}
	}
	return err
}

// DroppedCount 返回因队列满或写侧关闭丢弃的记录数。
func (w *AsyncWriter) DroppedCount() int64 { return w.dropped.Load() }

// CurrentPath/CurrentSize/Dir 透传 FileSink 的诊断面（测试与启动日志用）。
func (w *AsyncWriter) CurrentPath() string {
	if source, ok := w.writer.(interface{ CurrentPath() string }); ok {
		return source.CurrentPath()
	}
	return ""
}

func (w *AsyncWriter) CurrentSize() int64 {
	if source, ok := w.writer.(interface{ CurrentSize() int64 }); ok {
		return source.CurrentSize()
	}
	return 0
}

func (w *AsyncWriter) Dir() string {
	if source, ok := w.writer.(interface{ Dir() string }); ok {
		return source.Dir()
	}
	return ""
}

// 保证 AsyncWriter 满足 io.WriteCloser。
var _ io.WriteCloser = (*AsyncWriter)(nil)
