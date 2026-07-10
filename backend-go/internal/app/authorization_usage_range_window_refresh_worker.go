package app

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/modules/managementauthorizations"
	postgresstore "juhe-ai/backend-go/internal/store/postgres"
)

const (
	defaultAuthorizationUsageRangeWindowRefreshInterval     = 6 * time.Hour
	defaultAuthorizationUsageRangeWindowRefreshInitialDelay = 43 * time.Minute
)

type AuthorizationUsageRangeWindowRefreshWorkerOptions struct {
	Interval     time.Duration
	InitialDelay time.Duration
	RunOnce      bool
	Timezone     string
}

func RunAuthorizationUsageRangeWindowRefreshWorker(ctx context.Context, cfg config.Config, logger *slog.Logger, opts AuthorizationUsageRangeWindowRefreshWorkerOptions) error {
	if cfg.PostgresURL == "" {
		return fmt.Errorf("JUHE_AI_POSTGRES_URL 不能为空")
	}
	interval := opts.Interval
	if interval == 0 {
		interval = defaultAuthorizationUsageRangeWindowRefreshInterval
	}
	if interval <= 0 {
		return fmt.Errorf("授权用量窗口刷新间隔必须大于 0")
	}
	initialDelay := opts.InitialDelay
	if initialDelay < 0 {
		return fmt.Errorf("授权用量窗口刷新初始延迟不能小于 0")
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

	service := managementauthorizations.NewServiceWithOptions(managementauthorizations.ServiceOptions{
		UsageStatsTimezoneStore: store,
		UsageRangeWindowStore:   store,
	})
	refresh := func() error {
		result, err := service.RefreshUsageRangeWindows(ctx, managementauthorizations.UsageRangeWindowRefreshInput{
			Timezone: opts.Timezone,
		})
		if err != nil {
			return err
		}
		logger.Info("授权用量范围窗口刷新完成",
			slog.Int("ranges", result.RangeCount),
			slog.Int64("teamRows", result.TeamRows),
			slog.Int64("userRows", result.UserRows),
			slog.String("today", result.Today),
			slog.String("timezone", result.Timezone),
		)
		return nil
	}

	if opts.RunOnce {
		return refresh()
	}
	logger.Info("Go 授权用量范围窗口刷新 worker 启动",
		slog.Duration("interval", interval),
		slog.Duration("initialDelay", initialDelay),
		slog.String("timezoneOverride", opts.Timezone),
	)
	if err := waitAuthorizationExpirySweepWorker(ctx, initialDelay); err != nil {
		return nil
	}
	for {
		if err := refresh(); err != nil {
			logger.Error("授权用量范围窗口刷新失败", slog.Any("error", err))
		}
		if err := waitAuthorizationExpirySweepWorker(ctx, interval); err != nil {
			return nil
		}
	}
}
