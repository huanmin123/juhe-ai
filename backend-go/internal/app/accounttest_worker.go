package app

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/jobs/queue"
	"juhe-ai/backend-go/internal/jobs/worker"
	"juhe-ai/backend-go/internal/platform/accounttestdispatch"
)

func RunAccountTestWorker(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	if strings.TrimSpace(cfg.RedisQueueURL) == "" {
		return fmt.Errorf("JUHE_AI_REDIS_QUEUE_URL 不能为空")
	}
	if strings.TrimSpace(cfg.NodeInternalBaseURL) == "" {
		return fmt.Errorf("JUHE_AI_NODE_INTERNAL_BASE_URL 不能为空")
	}
	secret := strings.TrimSpace(cfg.Secret)
	if secret == "" {
		return fmt.Errorf("JUHE_AI_SECRET 不能为空")
	}
	dispatcher, err := accounttestdispatch.NewClientWithTimeout(
		strings.TrimSpace(cfg.NodeInternalBaseURL),
		secret,
		cfg.NodeInternalRequestTimeout,
	)
	if err != nil {
		return fmt.Errorf("初始化账户测试 Node bridge 失败: %w", err)
	}
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
	logger.Info("Go 账户测试 bridge worker 启动", slog.Int("concurrency", worker.DefaultAccountTestBridgeConcurrency))
	return worker.RunAccountTestBridge(ctx, worker.AccountTestBridgeOptions{
		Redis: redisOpts, Dispatcher: dispatcher, ShutdownTimeout: cfg.ShutdownTimeout,
		LogLevel: cfg.LogLevel, Concurrency: worker.DefaultAccountTestBridgeConcurrency,
	})
}
