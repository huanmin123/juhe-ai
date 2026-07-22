package recorddispatch

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"juhe-ai/backend-go/internal/logging"
)

type Options[T any] struct {
	Capacity int
	Workers  int
	Timeout  time.Duration
	Handle   func(context.Context, T) error
}

type Stats struct {
	Accepted  uint64
	Completed uint64
	Failed    uint64
	Dropped   uint64
	Pending   int64
}

type Dispatcher[T any] struct {
	options   Options[T]
	queue     chan job[T]
	acceptMu  sync.RWMutex
	accepting bool
	stopOnce  sync.Once
	done      chan struct{}
	workers   sync.WaitGroup
	accepted  atomic.Uint64
	completed atomic.Uint64
	failed    atomic.Uint64
	dropped   atomic.Uint64
	pending   atomic.Int64
}

type job[T any] struct {
	context logging.LogContext
	value   T
}

func New[T any](options Options[T]) *Dispatcher[T] {
	dispatcher := &Dispatcher[T]{
		options:   options,
		queue:     make(chan job[T], options.Capacity),
		accepting: true,
		done:      make(chan struct{}),
	}
	dispatcher.workers.Add(options.Workers)
	for range options.Workers {
		go dispatcher.runWorker()
	}
	go func() {
		dispatcher.workers.Wait()
		close(dispatcher.done)
	}()
	return dispatcher
}

func (d *Dispatcher[T]) Submit(ctx context.Context, value T) bool {
	d.acceptMu.RLock()
	defer d.acceptMu.RUnlock()

	if !d.accepting {
		d.dropped.Add(1)
		return false
	}

	d.accepted.Add(1)
	d.pending.Add(1)
	select {
	case d.queue <- job[T]{context: logging.LogContextFrom(ctx), value: value}:
		return true
	default:
		d.accepted.Add(^uint64(0))
		d.pending.Add(-1)
		d.dropped.Add(1)
		return false
	}
}

func (d *Dispatcher[T]) Stats() Stats {
	return Stats{
		Accepted:  d.accepted.Load(),
		Completed: d.completed.Load(),
		Failed:    d.failed.Load(),
		Dropped:   d.dropped.Load(),
		Pending:   d.pending.Load(),
	}
}

func (d *Dispatcher[T]) Shutdown(ctx context.Context) error {
	d.stopOnce.Do(func() {
		d.acceptMu.Lock()
		d.accepting = false
		close(d.queue)
		d.acceptMu.Unlock()
	})

	select {
	case <-d.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (d *Dispatcher[T]) runWorker() {
	defer d.workers.Done()
	for queued := range d.queue {
		ctx := logging.WithLogContext(context.Background(), queued.context)
		ctx, cancel := context.WithTimeout(ctx, d.options.Timeout)
		err := d.options.Handle(ctx, queued.value)
		cancel()

		if err != nil {
			d.failed.Add(1)
		}
		d.completed.Add(1)
		d.pending.Add(-1)
	}
}
