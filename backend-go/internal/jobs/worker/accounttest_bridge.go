package worker

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/hibiken/asynq"

	accounttestjob "juhe-ai/backend-go/internal/jobs/accounttest"
	"juhe-ai/backend-go/internal/jobs/queue"
)

const DefaultAccountTestBridgeConcurrency = 100

type AccountTestBridgeOptions struct {
	Redis           queue.RedisOptions
	Dispatcher      accounttestjob.Dispatcher
	ShutdownTimeout time.Duration
	LogLevel        string
	Concurrency     int
}

func RunAccountTestBridge(ctx context.Context, opts AccountTestBridgeOptions) error {
	if opts.Dispatcher == nil {
		return fmt.Errorf("account test dispatcher is required")
	}
	server := asynq.NewServer(asynqRedisOptions(opts.Redis), asynq.Config{
		Concurrency:     defaultInt(opts.Concurrency, DefaultAccountTestBridgeConcurrency),
		Queues:          map[string]int{accounttestjob.QueueName: 1},
		ShutdownTimeout: defaultDuration(opts.ShutdownTimeout, 10*time.Second),
		LogLevel:        asynqLogLevel(opts.LogLevel),
		BaseContext: func() context.Context {
			return context.WithoutCancel(ctx)
		},
	})
	mux := asynq.NewServeMux()
	mux.HandleFunc(accounttestjob.TaskType, func(taskCtx context.Context, task *asynq.Task) error {
		return handleAccountTestDispatchTask(taskCtx, opts.Dispatcher, task.Payload())
	})
	if err := server.Start(mux); err != nil {
		return err
	}
	<-ctx.Done()
	server.Shutdown()
	return nil
}

func handleAccountTestDispatchTask(ctx context.Context, dispatcher accounttestjob.Dispatcher, payload []byte) error {
	if err := accounttestjob.HandleDispatchTask(ctx, dispatcher, payload); err != nil {
		if errors.Is(err, accounttestjob.ErrInvalidPayload) {
			return fmt.Errorf("%w: %w", err, asynq.SkipRetry)
		}
		return err
	}
	return nil
}
