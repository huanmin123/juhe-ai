package recorddispatch

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"juhe-ai/backend-go/internal/logging"
)

type Options[T any] struct {
	Capacity int
	Workers  int
	Timeout  time.Duration
	// Clone transfers ownership of mutable values to the dispatcher. Callers
	// that submit slices, maps, pointers, or structs containing them should
	// provide a function that returns an independent snapshot.
	Clone  func(T) T
	Handle func(context.Context, T) error
}

type RejectionReason string

const (
	RejectionQueueFull RejectionReason = "queue_full"
	RejectionStopped   RejectionReason = "stopped"
)

type SubmitOutcome struct {
	Accepted        bool
	RejectionReason RejectionReason
}

type Stats struct {
	Accepted         uint64
	Completed        uint64
	Failed           uint64
	Dropped          uint64
	DroppedQueueFull uint64
	DroppedStopped   uint64
	Pending          int64
}

type Dispatcher[T any] struct {
	options          Options[T]
	queue            chan job[T]
	acceptMu         sync.RWMutex
	accepting        bool
	stopOnce         sync.Once
	done             chan struct{}
	workers          sync.WaitGroup
	accepted         atomic.Uint64
	completed        atomic.Uint64
	failed           atomic.Uint64
	dropped          atomic.Uint64
	droppedQueueFull atomic.Uint64
	droppedStopped   atomic.Uint64
	pending          atomic.Int64
}

type job[T any] struct {
	context logging.LogContext
	value   T
}

func New[T any](options Options[T]) *Dispatcher[T] {
	if options.Capacity < 1 {
		panic("recorddispatch: capacity must be at least 1")
	}
	if options.Workers < 1 {
		panic("recorddispatch: workers must be at least 1")
	}
	if options.Timeout <= 0 {
		panic("recorddispatch: timeout must be positive")
	}
	if options.Handle == nil {
		panic("recorddispatch: handler is required")
	}
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
	return d.TrySubmit(ctx, value).Accepted
}

// TrySubmit enqueues value without waiting and reports why it was rejected.
func (d *Dispatcher[T]) TrySubmit(ctx context.Context, value T) SubmitOutcome {
	d.acceptMu.RLock()
	defer d.acceptMu.RUnlock()

	if !d.accepting {
		d.dropped.Add(1)
		d.droppedStopped.Add(1)
		return SubmitOutcome{RejectionReason: RejectionStopped}
	}
	if d.options.Clone != nil {
		value = d.options.Clone(value)
	}

	d.accepted.Add(1)
	d.pending.Add(1)
	select {
	case d.queue <- job[T]{context: logging.LogContextFrom(ctx), value: value}:
		return SubmitOutcome{Accepted: true}
	default:
		d.accepted.Add(^uint64(0))
		d.pending.Add(-1)
		d.dropped.Add(1)
		d.droppedQueueFull.Add(1)
		return SubmitOutcome{RejectionReason: RejectionQueueFull}
	}
}

func (d *Dispatcher[T]) Stats() Stats {
	return Stats{
		Accepted:         d.accepted.Load(),
		Completed:        d.completed.Load(),
		Failed:           d.failed.Load(),
		Dropped:          d.dropped.Load(),
		DroppedQueueFull: d.droppedQueueFull.Load(),
		DroppedStopped:   d.droppedStopped.Load(),
		Pending:          d.pending.Load(),
	}
}

// Done closes only after every accepted job has returned from its handler.
// Dependency owners can use it as a fence before releasing resources that a
// handler may still be using after Shutdown returns a context error.
func (d *Dispatcher[T]) Done() <-chan struct{} {
	return d.done
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

// ShutdownAll starts every shutdown concurrently and applies one shared
// context budget to the group instead of multiplying the budget per member.
// Each member must honor the supplied context.
func ShutdownAll(ctx context.Context, dispatchers ...interface {
	Shutdown(context.Context) error
}) error {
	if len(dispatchers) == 0 {
		return nil
	}

	results := make(chan error, len(dispatchers))
	for _, dispatcher := range dispatchers {
		go func() {
			results <- dispatcher.Shutdown(ctx)
		}()
	}

	errorsFound := make([]error, 0, len(dispatchers))
	for range dispatchers {
		if err := <-results; err != nil {
			errorsFound = append(errorsFound, err)
		}
	}
	return errors.Join(errorsFound...)
}

func (d *Dispatcher[T]) runWorker() {
	defer d.workers.Done()
	for queued := range d.queue {
		d.runJob(queued)
	}
}

func (d *Dispatcher[T]) runJob(queued job[T]) {
	failed := false
	defer func() {
		if recover() != nil {
			failed = true
		}
		if failed {
			d.failed.Add(1)
		}
		d.completed.Add(1)
		d.pending.Add(-1)
	}()

	ctx := logging.WithLogContext(context.Background(), queued.context)
	ctx, cancel := context.WithTimeout(ctx, d.options.Timeout)
	defer cancel()
	failed = d.options.Handle(ctx, queued.value) != nil
}
