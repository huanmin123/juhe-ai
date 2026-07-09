package app

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/modules/managementoperationlogs"
	postgresstore "juhe-ai/backend-go/internal/store/postgres"
)

const (
	defaultOperationLogRetentionCleanupInterval     = 10 * time.Minute
	defaultOperationLogRetentionCleanupInitialDelay = 13 * time.Minute
)

type OperationLogRetentionCleanupWorkerOptions struct {
	RetentionDays int
	BatchSize     int
	MaxBatches    int
	Interval      time.Duration
	InitialDelay  time.Duration
	RunOnce       bool
}

func RunOperationLogRetentionCleanupWorker(ctx context.Context, cfg config.Config, logger *slog.Logger, opts OperationLogRetentionCleanupWorkerOptions) error {
	if cfg.PostgresURL == "" {
		return fmt.Errorf("JUHE_AI_POSTGRES_URL 不能为空")
	}
	interval := opts.Interval
	if interval == 0 {
		interval = defaultOperationLogRetentionCleanupInterval
	}
	if interval <= 0 {
		return fmt.Errorf("操作日志保留清理间隔必须大于 0")
	}
	initialDelay := opts.InitialDelay
	if initialDelay < 0 {
		return fmt.Errorf("操作日志保留清理初始延迟不能小于 0")
	}
	if logger == nil {
		logger = slog.Default()
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

	service := managementoperationlogs.NewRetentionCleanupService(store)
	cleanup := func() error {
		result, err := service.Cleanup(ctx, managementoperationlogs.RetentionCleanupInput{
			RetentionDays: opts.RetentionDays,
			BatchSize:     opts.BatchSize,
			MaxBatches:    opts.MaxBatches,
		})
		if err != nil {
			return err
		}
		logger.Info("操作日志保留清理完成",
			slog.Int64("deleted", result.Deleted),
			slog.Int("batches", result.Batches),
			slog.Int("retentionDays", result.RetentionDays),
			slog.Int("batchSize", result.BatchSize),
			slog.Int("maxBatches", result.MaxBatches),
			slog.String("cutoffCreatedAt", result.CutoffCreatedAt.Format(time.RFC3339Nano)),
		)
		return nil
	}

	if opts.RunOnce {
		return cleanup()
	}
	logger.Info("Go 操作日志保留清理 worker 启动",
		slog.Duration("interval", interval),
		slog.Duration("initialDelay", initialDelay),
		slog.Int("retentionDaysOverride", opts.RetentionDays),
		slog.Int("batchSize", opts.BatchSize),
		slog.Int("maxBatches", opts.MaxBatches),
	)
	if err := waitAuthorizationExpirySweepWorker(ctx, initialDelay); err != nil {
		return nil
	}
	for {
		if err := cleanup(); err != nil {
			logger.Error("操作日志保留清理失败", slog.Any("error", err))
		}
		if err := waitAuthorizationExpirySweepWorker(ctx, interval); err != nil {
			return nil
		}
	}
}
