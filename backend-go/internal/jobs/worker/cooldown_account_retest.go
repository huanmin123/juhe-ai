package worker

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/hibiken/asynq"

	job "juhe-ai/backend-go/internal/jobs/cooldownaccountretest"
	"juhe-ai/backend-go/internal/jobs/queue"
	module "juhe-ai/backend-go/internal/modules/cooldownaccountretest"
)

const DefaultCooldownAccountRetestConcurrency = job.DefaultConsumerConcurrency

type CooldownAccountRetestConsumerOptions struct {
	Redis           queue.RedisOptions
	Processor       module.Processor
	ShutdownTimeout time.Duration
	LogLevel        string
	Concurrency     int
}

func RunCooldownAccountRetestConsumer(ctx context.Context, opts CooldownAccountRetestConsumerOptions) error {
	if opts.Processor.Store == nil || opts.Processor.Outcomes == nil {
		return fmt.Errorf("cooldown account retest processor stores are required")
	}
	if opts.Processor.Quota == nil {
		return fmt.Errorf("cooldown account retest quota checker is required")
	}
	if opts.Processor.Probe == nil {
		return module.ErrProbeNotConfigured
	}
	server := asynq.NewServer(asynqRedisOptions(opts.Redis), asynq.Config{
		Concurrency:     defaultInt(opts.Concurrency, DefaultCooldownAccountRetestConcurrency),
		Queues:          map[string]int{job.QueueName: 1},
		ShutdownTimeout: defaultDuration(opts.ShutdownTimeout, 10*time.Second),
		LogLevel:        asynqLogLevel(opts.LogLevel),
		BaseContext: func() context.Context {
			return context.WithoutCancel(ctx)
		},
	})
	handlers := newCooldownAccountRetestHandlerTracker()
	if err := server.Start(newCooldownAccountRetestMuxWithTracker(opts.Processor, handlers)); err != nil {
		return err
	}
	<-ctx.Done()
	server.Shutdown()
	// Asynq may finish its shutdown bookkeeping before an aborted handler goroutine
	// has returned. Keep the owner alive until every outcome writer has stopped.
	handlers.CloseAndWait()
	return nil
}

func newCooldownAccountRetestMux(processor module.Processor) *asynq.ServeMux {
	return newCooldownAccountRetestMuxWithTracker(processor, nil)
}

func newCooldownAccountRetestMuxWithTracker(processor module.Processor, handlers *cooldownAccountRetestHandlerTracker) *asynq.ServeMux {
	mux := asynq.NewServeMux()
	mux.HandleFunc(job.TaskType, func(taskCtx context.Context, task *asynq.Task) error {
		if handlers != nil && !handlers.Begin() {
			return context.Canceled
		}
		if handlers != nil {
			defer handlers.End()
		}
		return handleCooldownAccountRetestTask(taskCtx, processor, task.Payload(), task.Headers())
	})
	return mux
}

type cooldownAccountRetestHandlerTracker struct {
	mu      sync.Mutex
	changed *sync.Cond
	active  int
	closing bool
}

func newCooldownAccountRetestHandlerTracker() *cooldownAccountRetestHandlerTracker {
	tracker := &cooldownAccountRetestHandlerTracker{}
	tracker.changed = sync.NewCond(&tracker.mu)
	return tracker
}

func (t *cooldownAccountRetestHandlerTracker) Begin() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closing {
		return false
	}
	t.active++
	return true
}

func (t *cooldownAccountRetestHandlerTracker) End() {
	t.mu.Lock()
	t.active--
	if t.active == 0 {
		t.changed.Broadcast()
	}
	t.mu.Unlock()
}

func (t *cooldownAccountRetestHandlerTracker) CloseAndWait() {
	t.mu.Lock()
	t.closing = true
	for t.active > 0 {
		t.changed.Wait()
	}
	t.mu.Unlock()
}

func handleCooldownAccountRetestTask(ctx context.Context, processor module.Processor, payload []byte, headers map[string]string) error {
	if err := job.HandleTask(ctx, processor, payload, headers); err != nil {
		if errors.Is(err, job.ErrInvalidPayload) || errors.Is(err, module.ErrUnsupportedProbeOutcome) {
			return fmt.Errorf("%w: %w", err, asynq.SkipRetry)
		}
		return err
	}
	return nil
}
