package logging

import (
	"context"
	"log/slog"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"
)

const (
	defaultNormalQueueCapacity  = 4096
	defaultFailureQueueCapacity = defaultNormalQueueCapacity / 8
	defaultNormalQueueBytes     = 16 * 1024 * 1024
	defaultFailureQueueBytes    = 4 * 1024 * 1024
)

type RuntimeOptions struct {
	Role                 string
	NormalQueueCapacity  int
	FailureQueueCapacity int
	NormalQueueBytes     int64
	FailureQueueBytes    int64
}

type RuntimeStats struct {
	PendingNormal  int
	PendingFailure int
	NormalDropped  uint64
	FailureDropped uint64
	WriterErrors   uint64
	PendingBytes   int64
}

type Runtime struct {
	Logger     *slog.Logger
	dispatcher *asyncLogDispatcher
}

type asyncLogItem struct {
	handler slog.Handler
	record  slog.Record
	bytes   int64
}

type asyncLogDispatcher struct {
	base                slog.Handler
	normal              chan asyncLogItem
	failure             chan asyncLogItem
	stop                chan struct{}
	done                chan struct{}
	acceptMu            sync.RWMutex
	accepting           bool
	stopOnce            sync.Once
	normalDropped       atomic.Uint64
	failureDropped      atomic.Uint64
	writerErrors        atomic.Uint64
	pendingNormalBytes  atomic.Int64
	pendingFailureBytes atomic.Int64
	maxNormalBytes      int64
	maxFailureBytes     int64
	reportedNormalDrop  uint64
	reportedFailureDrop uint64
}

func newAsyncLogRuntime(base slog.Handler, options RuntimeOptions) *Runtime {
	normalCapacity := options.NormalQueueCapacity
	if normalCapacity <= 0 {
		normalCapacity = defaultNormalQueueCapacity
	}
	failureCapacity := options.FailureQueueCapacity
	if failureCapacity <= 0 {
		failureCapacity = defaultFailureQueueCapacity
	}
	normalBytes := options.NormalQueueBytes
	if normalBytes <= 0 {
		normalBytes = defaultNormalQueueBytes
	}
	failureBytes := options.FailureQueueBytes
	if failureBytes <= 0 {
		failureBytes = defaultFailureQueueBytes
	}
	dispatcher := &asyncLogDispatcher{
		base:            base,
		normal:          make(chan asyncLogItem, normalCapacity),
		failure:         make(chan asyncLogItem, failureCapacity),
		stop:            make(chan struct{}),
		done:            make(chan struct{}),
		accepting:       true,
		maxNormalBytes:  normalBytes,
		maxFailureBytes: failureBytes,
	}
	go dispatcher.run()
	return &Runtime{Logger: slog.New(&asyncSlogHandler{base: base, dispatcher: dispatcher}), dispatcher: dispatcher}
}

type asyncSlogHandler struct {
	base       slog.Handler
	dispatcher *asyncLogDispatcher
}

func (h *asyncSlogHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.base.Enabled(ctx, level)
}

func (h *asyncSlogHandler) Handle(ctx context.Context, record slog.Record) error {
	record.AddAttrs(logContextAttrs(ctx)...)
	if record.Level >= slog.LevelError {
		addFailureFallbackAttrs(&record)
	}
	item := asyncLogItem{handler: h.base, record: record.Clone(), bytes: estimateRecordBytes(record)}
	queue := h.dispatcher.normal
	pendingBytes := &h.dispatcher.pendingNormalBytes
	maxBytes := h.dispatcher.maxNormalBytes
	if record.Level >= slog.LevelError {
		queue = h.dispatcher.failure
		pendingBytes = &h.dispatcher.pendingFailureBytes
		maxBytes = h.dispatcher.maxFailureBytes
	}
	h.dispatcher.acceptMu.RLock()
	defer h.dispatcher.acceptMu.RUnlock()
	if !h.dispatcher.accepting {
		h.dispatcher.recordDrop(record.Level)
		return nil
	}
	if pendingBytes.Add(item.bytes) > maxBytes {
		pendingBytes.Add(-item.bytes)
		h.dispatcher.recordDrop(record.Level)
		return nil
	}
	select {
	case queue <- item:
	default:
		pendingBytes.Add(-item.bytes)
		h.dispatcher.recordDrop(record.Level)
	}
	return nil
}

func (h *asyncSlogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &asyncSlogHandler{base: h.base.WithAttrs(attrs), dispatcher: h.dispatcher}
}

func (h *asyncSlogHandler) WithGroup(name string) slog.Handler {
	return &asyncSlogHandler{base: h.base.WithGroup(name), dispatcher: h.dispatcher}
}

func (d *asyncLogDispatcher) recordDrop(level slog.Level) {
	if level >= slog.LevelError {
		d.failureDropped.Add(1)
		return
	}
	d.normalDropped.Add(1)
}

func (d *asyncLogDispatcher) run() {
	defer close(d.done)
	dropSummaryTicker := time.NewTicker(time.Minute)
	defer dropSummaryTicker.Stop()
	for {
		if item, ok := d.takeImmediate(); ok {
			d.handle(item)
			continue
		}
		select {
		case item := <-d.failure:
			d.handle(item)
		case item := <-d.normal:
			d.handle(item)
		case <-d.stop:
			d.drain()
			d.emitDropSummary()
			return
		case <-dropSummaryTicker.C:
			d.emitDropSummary()
		}
	}
}

func addFailureFallbackAttrs(record *slog.Record) {
	hasFailureClass := false
	hasStack := false
	record.Attrs(func(attr slog.Attr) bool {
		switch attr.Key {
		case "failureClass":
			hasFailureClass = true
		case "stack":
			hasStack = true
		}
		return true
	})
	if !hasFailureClass {
		record.AddAttrs(slog.String("failureClass", "unexpected"))
	}
	if !hasStack {
		record.AddAttrs(slog.String("stack", string(debug.Stack())))
	}
}

func (d *asyncLogDispatcher) takeImmediate() (asyncLogItem, bool) {
	select {
	case item := <-d.failure:
		return item, true
	default:
	}
	select {
	case item := <-d.failure:
		return item, true
	case item := <-d.normal:
		return item, true
	default:
		return asyncLogItem{}, false
	}
}

func (d *asyncLogDispatcher) drain() {
	for {
		item, ok := d.takeImmediate()
		if !ok {
			return
		}
		d.handle(item)
	}
}

func (d *asyncLogDispatcher) handle(item asyncLogItem) {
	defer d.releaseBytes(item)
	if err := item.handler.Handle(context.Background(), item.record); err != nil {
		d.writerErrors.Add(1)
	}
}

func (d *asyncLogDispatcher) releaseBytes(item asyncLogItem) {
	if item.record.Level >= slog.LevelError {
		d.pendingFailureBytes.Add(-item.bytes)
		return
	}
	d.pendingNormalBytes.Add(-item.bytes)
}

func estimateRecordBytes(record slog.Record) int64 {
	size := int64(len(record.Message) + 128)
	record.Attrs(func(attr slog.Attr) bool {
		size += int64(len(attr.Key) + len(attr.Value.String()) + 16)
		return true
	})
	return size
}

func (d *asyncLogDispatcher) emitDropSummary() {
	normalDropped := d.normalDropped.Load()
	failureDropped := d.failureDropped.Load()
	if normalDropped == d.reportedNormalDrop && failureDropped == d.reportedFailureDrop {
		return
	}
	record := slog.NewRecord(time.Now(), slog.LevelWarn, "异步日志队列丢弃摘要", 0)
	record.AddAttrs(
		slog.String("event", "system_log_drop"),
		slog.Uint64("normalDropped", normalDropped),
		slog.Uint64("failureDropped", failureDropped),
	)
	if err := d.base.Handle(context.Background(), record); err != nil {
		d.writerErrors.Add(1)
		return
	}
	d.reportedNormalDrop = normalDropped
	d.reportedFailureDrop = failureDropped
}

func (d *asyncLogDispatcher) shutdown(ctx context.Context) error {
	d.acceptMu.Lock()
	if d.accepting {
		d.accepting = false
		d.stopOnce.Do(func() { close(d.stop) })
	}
	d.acceptMu.Unlock()
	select {
	case <-d.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *Runtime) Shutdown(ctx context.Context) error {
	return r.dispatcher.shutdown(ctx)
}

func (r *Runtime) Stats() RuntimeStats {
	return RuntimeStats{
		PendingNormal:  len(r.dispatcher.normal),
		PendingFailure: len(r.dispatcher.failure),
		NormalDropped:  r.dispatcher.normalDropped.Load(),
		FailureDropped: r.dispatcher.failureDropped.Load(),
		WriterErrors:   r.dispatcher.writerErrors.Load(),
		PendingBytes:   r.dispatcher.pendingNormalBytes.Load() + r.dispatcher.pendingFailureBytes.Load(),
	}
}
