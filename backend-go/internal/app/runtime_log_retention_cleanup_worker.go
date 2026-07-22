package app

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/modules/runtimelogretention"
	postgresstore "juhe-ai/backend-go/internal/store/postgres"
)

const (
	defaultRuntimeLogRetentionCleanupInterval     = 10 * time.Minute
	defaultRuntimeLogRetentionCleanupInitialDelay = 13 * time.Minute
)

type RuntimeLogRetentionCleanupWorkerOptions struct {
	RetentionDays int
	BatchSize     int
	MaxBatches    int
	Interval      time.Duration
	InitialDelay  time.Duration
	RunOnce       bool
}

type runtimeLogRetentionCleanupFunc func(context.Context) (runtimelogretention.CleanupResult, error)

func RunRuntimeLogRetentionCleanupWorker(
	ctx context.Context,
	cfg config.Config,
	logger *slog.Logger,
	opts RuntimeLogRetentionCleanupWorkerOptions,
) error {
	if !cfg.RuntimeLogIndexEnabled {
		return nil
	}
	if err := validateRuntimeLogRetentionCleanupWorkerOptions(cfg, opts); err != nil {
		return err
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

	service := runtimelogretention.NewService(store)
	return runRuntimeLogRetentionCleanupLoop(ctx, logger, opts, func(ctx context.Context) (runtimelogretention.CleanupResult, error) {
		return service.Cleanup(ctx, runtimelogretention.CleanupInput{
			IndexEnabled:  cfg.RuntimeLogIndexEnabled,
			RetentionDays: opts.RetentionDays,
			BatchSize:     opts.BatchSize,
			MaxBatches:    opts.MaxBatches,
		})
	})
}

func validateRuntimeLogRetentionCleanupWorkerOptions(cfg config.Config, opts RuntimeLogRetentionCleanupWorkerOptions) error {
	if !cfg.RuntimeLogIndexEnabled {
		return nil
	}
	if cfg.PostgresURL == "" {
		return fmt.Errorf("JUHE_AI_POSTGRES_URL 不能为空")
	}
	if opts.Interval < 0 {
		return fmt.Errorf("运行日志索引保留清理间隔必须大于 0")
	}
	if opts.InitialDelay < 0 {
		return fmt.Errorf("运行日志索引保留清理初始延迟不能小于 0")
	}
	return nil
}

func runRuntimeLogRetentionCleanupLoop(
	ctx context.Context,
	logger *slog.Logger,
	opts RuntimeLogRetentionCleanupWorkerOptions,
	cleanup runtimeLogRetentionCleanupFunc,
) error {
	if logger == nil {
		logger = slog.Default()
	}
	runCleanup := func() error {
		result, err := cleanup(ctx)
		attrs := []any{
			slog.Int64("runtimeLogs", result.RuntimeLogs),
			slog.Int("runtimeLogBatches", result.RuntimeLogBatches),
			slog.Int64("runtimeLogFileCursors", result.RuntimeLogFileCursors),
			slog.Int("runtimeLogFileCursorBatches", result.RuntimeLogFileCursorBatches),
			slog.String("phase", result.Phase),
			slog.Bool("partial", result.Partial),
			slog.Int("retentionDays", result.RetentionDays),
			slog.Int("batchSize", result.BatchSize),
			slog.Int("maxBatches", result.MaxBatches),
			slog.String("cutoff", result.CutoffISO),
		}
		if err != nil {
			logger.Error("运行日志索引保留清理失败", append(attrs, slog.Any("error", err))...)
			return err
		}
		logger.Info("运行日志索引保留清理完成", attrs...)
		return nil
	}
	if opts.RunOnce {
		return runCleanup()
	}
	interval := opts.Interval
	if interval == 0 {
		interval = defaultRuntimeLogRetentionCleanupInterval
	}
	initialDelay := opts.InitialDelay
	if initialDelay == 0 {
		initialDelay = defaultRuntimeLogRetentionCleanupInitialDelay
	}
	logger.Info("Go 运行日志索引保留清理 worker 启动",
		slog.Duration("interval", interval),
		slog.Duration("initialDelay", initialDelay),
		slog.Int("retentionDaysOverride", opts.RetentionDays),
		slog.Int("batchSize", opts.BatchSize),
		slog.Int("maxBatches", opts.MaxBatches),
	)
	if err := waitRuntimeLogRetentionCleanupWorker(ctx, initialDelay); err != nil {
		return nil
	}
	for {
		runCleanup()
		if err := waitRuntimeLogRetentionCleanupWorker(ctx, interval); err != nil {
			return nil
		}
	}
}

func waitRuntimeLogRetentionCleanupWorker(ctx context.Context, duration time.Duration) error {
	if duration <= 0 {
		return nil
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
