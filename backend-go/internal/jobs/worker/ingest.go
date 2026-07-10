package worker

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hibiken/asynq"

	operationlogjob "juhe-ai/backend-go/internal/jobs/operationlog"
	publicapilogjob "juhe-ai/backend-go/internal/jobs/publicapilog"
	"juhe-ai/backend-go/internal/jobs/queue"
	"juhe-ai/backend-go/internal/store/port"
)

const DefaultIngestConcurrency = 1

type IngestOptions struct {
	Redis             queue.RedisOptions
	PublicAPILogStore port.PublicAPILogStore
	OperationLogStore port.OperationLogStore
	ShutdownTimeout   time.Duration
	LogLevel          string
	Concurrency       int
}

func RunIngest(ctx context.Context, opts IngestOptions) error {
	if opts.PublicAPILogStore == nil {
		return fmt.Errorf("public api log store is required")
	}
	if opts.OperationLogStore == nil {
		return fmt.Errorf("operation log store is required")
	}

	server := asynq.NewServer(asynqRedisOptions(opts.Redis), asynq.Config{
		Concurrency:     defaultInt(opts.Concurrency, DefaultIngestConcurrency),
		Queues:          map[string]int{publicapilogjob.QueueName: 1, operationlogjob.QueueName: 1},
		ShutdownTimeout: defaultDuration(opts.ShutdownTimeout, 10*time.Second),
		LogLevel:        asynqLogLevel(opts.LogLevel),
		BaseContext: func() context.Context {
			// Keep in-flight task contexts alive after process cancellation;
			// Asynq ShutdownTimeout owns the graceful drain window.
			return context.WithoutCancel(ctx)
		},
	})
	mux := asynq.NewServeMux()
	mux.HandleFunc(publicapilogjob.TaskTypeWrite, func(ctx context.Context, task *asynq.Task) error {
		return handlePublicAPILogTask(ctx, opts.PublicAPILogStore, task.Payload())
	})
	mux.HandleFunc(operationlogjob.TaskTypeWrite, func(ctx context.Context, task *asynq.Task) error {
		return handleOperationLogTask(ctx, opts.OperationLogStore, task.Payload())
	})

	if err := server.Start(mux); err != nil {
		return err
	}

	<-ctx.Done()
	server.Shutdown()
	return nil
}

func handlePublicAPILogTask(ctx context.Context, store port.PublicAPILogStore, payload []byte) error {
	if err := publicapilogjob.HandleWriteTask(ctx, store, payload); err != nil {
		if errors.Is(err, publicapilogjob.ErrInvalidPayload) {
			return fmt.Errorf("%w: %w", err, asynq.SkipRetry)
		}
		return err
	}
	return nil
}

func handleOperationLogTask(ctx context.Context, store port.OperationLogStore, payload []byte) error {
	if err := operationlogjob.HandleWriteTask(ctx, store, payload); err != nil {
		if errors.Is(err, operationlogjob.ErrInvalidPayload) {
			return fmt.Errorf("%w: %w", err, asynq.SkipRetry)
		}
		return err
	}
	return nil
}

func asynqRedisOptions(opts queue.RedisOptions) asynq.RedisClientOpt {
	return asynq.RedisClientOpt{
		Addr:         opts.Addr,
		Username:     opts.Username,
		Password:     opts.Password,
		DB:           opts.DB,
		TLSConfig:    opts.TLS,
		DialTimeout:  opts.DialTimeout,
		ReadTimeout:  opts.ReadTimeout,
		WriteTimeout: opts.WriteTimeout,
	}
}

func asynqLogLevel(value string) asynq.LogLevel {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "debug":
		return asynq.DebugLevel
	case "warn", "warning":
		return asynq.WarnLevel
	case "error":
		return asynq.ErrorLevel
	default:
		return asynq.InfoLevel
	}
}

func defaultDuration(value time.Duration, fallback time.Duration) time.Duration {
	if value > 0 {
		return value
	}
	return fallback
}

func defaultInt(value int, fallback int) int {
	if value > 0 {
		return value
	}
	return fallback
}
