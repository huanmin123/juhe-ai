package worker

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/hibiken/asynq"

	job "juhe-ai/backend-go/internal/jobs/cooldownaccountretest"
	"juhe-ai/backend-go/internal/jobs/queue"
	module "juhe-ai/backend-go/internal/modules/cooldownaccountretest"
)

const DefaultCooldownAccountRetestConcurrency = 3

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
	if err := server.Start(newCooldownAccountRetestMux(opts.Processor)); err != nil {
		return err
	}
	<-ctx.Done()
	server.Shutdown()
	return nil
}

func newCooldownAccountRetestMux(processor module.Processor) *asynq.ServeMux {
	mux := asynq.NewServeMux()
	mux.HandleFunc(job.TaskType, func(taskCtx context.Context, task *asynq.Task) error {
		return handleCooldownAccountRetestTask(taskCtx, processor, task.Payload())
	})
	return mux
}

func handleCooldownAccountRetestTask(ctx context.Context, processor module.Processor, payload []byte) error {
	if err := job.HandleTask(ctx, processor, payload); err != nil {
		if errors.Is(err, job.ErrInvalidPayload) {
			return fmt.Errorf("%w: %w", err, asynq.SkipRetry)
		}
		return err
	}
	return nil
}
