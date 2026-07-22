package app

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/modules/managementpublicapilogs"
	postgresstore "juhe-ai/backend-go/internal/store/postgres"
)

type PublicAPILogRetentionCleanupWorkerOptions struct {
	RetentionDays int
	BatchSize     int
	MaxBatches    int
}

type publicAPILogRetentionCleanupFunc func(context.Context) (managementpublicapilogs.RetentionCleanupResult, error)

func RunPublicAPILogRetentionCleanupWorker(ctx context.Context, cfg config.Config, logger *slog.Logger, opts PublicAPILogRetentionCleanupWorkerOptions) error {
	if cfg.PostgresURL == "" {
		return fmt.Errorf("JUHE_AI_POSTGRES_URL 不能为空")
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

	service := managementpublicapilogs.NewRetentionCleanupService(store)
	return runPublicAPILogRetentionCleanup(ctx, logger, func(ctx context.Context) (managementpublicapilogs.RetentionCleanupResult, error) {
		return service.Cleanup(ctx, managementpublicapilogs.RetentionCleanupInput{
			RetentionDays: opts.RetentionDays,
			BatchSize:     opts.BatchSize,
			MaxBatches:    opts.MaxBatches,
		})
	})
}

func runPublicAPILogRetentionCleanup(ctx context.Context, logger *slog.Logger, cleanup publicAPILogRetentionCleanupFunc) error {
	if logger == nil {
		logger = slog.Default()
	}
	result, err := cleanup(ctx)
	attrs := []any{
		slog.Int64("deleted", result.Deleted),
		slog.Int("batches", result.Batches),
		slog.String("phase", result.Phase),
		slog.Bool("partial", result.Partial),
		slog.Int("retentionDays", result.RetentionDays),
		slog.Int("batchSize", result.BatchSize),
		slog.Int("maxBatches", result.MaxBatches),
		slog.String("cutoffCreatedAt", result.CutoffCreatedAt.Format(time.RFC3339Nano)),
	}
	if err != nil {
		logger.Error("公开接口日志保留清理失败", append(attrs, slog.Any("error", err))...)
		return err
	}
	logger.Info("公开接口日志保留清理完成", attrs...)
	return nil
}
