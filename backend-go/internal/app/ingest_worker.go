package app

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/jobs/queue"
	"juhe-ai/backend-go/internal/jobs/worker"
	postgresstore "juhe-ai/backend-go/internal/store/postgres"
)

func RunIngestWorker(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	if cfg.PostgresURL == "" {
		return fmt.Errorf("JUHE_AI_POSTGRES_URL 不能为空")
	}
	if cfg.RedisQueueURL == "" {
		return fmt.Errorf("JUHE_AI_REDIS_QUEUE_URL 不能为空")
	}

	store, err := postgresstore.Open(ctx, cfg.PostgresURL)
	if err != nil {
		return err
	}
	defer store.Close()
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	if err := store.Ping(pingCtx); err != nil {
		cancel()
		return err
	}
	cancel()

	redisOpts, err := queue.ParseRedisURL(cfg.RedisQueueURL)
	if err != nil {
		return fmt.Errorf("JUHE_AI_REDIS_QUEUE_URL 无效: %w", err)
	}
	queueClient := queue.NewClient(redisOpts)
	if err := queueClient.Ping(); err != nil {
		_ = queueClient.Close()
		return err
	}
	if err := queueClient.Close(); err != nil {
		return err
	}

	logger.Info("Go 后端 ingest worker 启动", slog.String("queue", "public-api-logs,operation-logs"))
	return worker.RunIngest(ctx, worker.IngestOptions{
		Redis:             redisOpts,
		PublicAPILogStore: store,
		OperationLogStore: store,
		ShutdownTimeout:   cfg.ShutdownTimeout,
		LogLevel:          cfg.LogLevel,
	})
}
