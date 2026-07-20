package logging

import (
	"context"
	"log/slog"
)

const asyncLogQueueCapacity = 4096

type asyncLogItem struct {
	handler slog.Handler
	record  slog.Record
}

type asyncLogDispatcher struct {
	normal  chan asyncLogItem
	failure chan asyncLogItem
}

func newAsyncLogHandler(base slog.Handler) *asyncSlogHandler {
	dispatcher := &asyncLogDispatcher{
		normal:  make(chan asyncLogItem, asyncLogQueueCapacity),
		failure: make(chan asyncLogItem, asyncLogQueueCapacity/8),
	}
	go dispatcher.run()
	return &asyncSlogHandler{base: base, dispatcher: dispatcher}
}

type asyncSlogHandler struct {
	base       slog.Handler
	dispatcher *asyncLogDispatcher
}

func (h *asyncSlogHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.base.Enabled(ctx, level)
}

func (h *asyncSlogHandler) Handle(_ context.Context, record slog.Record) error {
	item := asyncLogItem{handler: h.base, record: record.Clone()}
	queue := h.dispatcher.normal
	if record.Level >= slog.LevelError {
		queue = h.dispatcher.failure
	}
	select {
	case queue <- item:
	default:
		// 日志队列有界；失败记录使用独立预留容量，仍不阻塞业务请求。
	}
	return nil
}

func (h *asyncSlogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &asyncSlogHandler{base: h.base.WithAttrs(attrs), dispatcher: h.dispatcher}
}

func (h *asyncSlogHandler) WithGroup(name string) slog.Handler {
	return &asyncSlogHandler{base: h.base.WithGroup(name), dispatcher: h.dispatcher}
}

func (d *asyncLogDispatcher) run() {
	for {
		select {
		case item := <-d.failure:
			_ = item.handler.Handle(context.Background(), item.record)
		default:
			select {
			case item := <-d.failure:
				_ = item.handler.Handle(context.Background(), item.record)
			case item := <-d.normal:
				_ = item.handler.Handle(context.Background(), item.record)
			}
		}
	}
}
