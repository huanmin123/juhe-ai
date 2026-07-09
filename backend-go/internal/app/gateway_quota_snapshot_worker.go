package app

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/modules/gatewayquotasnapshot"
	postgresstore "juhe-ai/backend-go/internal/store/postgres"
)

const (
	defaultGatewayQuotaSnapshotBuildInterval     = time.Minute
	defaultGatewayQuotaSnapshotBuildInitialDelay = 37 * time.Second
)

type GatewayQuotaSnapshotBuildWorkerOptions struct {
	Interval     time.Duration
	InitialDelay time.Duration
	RunOnce      bool
	Timezone     string
}

func RunGatewayQuotaSnapshotBuildWorker(ctx context.Context, cfg config.Config, logger *slog.Logger, opts GatewayQuotaSnapshotBuildWorkerOptions) error {
	if cfg.PostgresURL == "" {
		return fmt.Errorf("JUHE_AI_POSTGRES_URL 不能为空")
	}
	interval := opts.Interval
	if interval == 0 {
		interval = defaultGatewayQuotaSnapshotBuildInterval
	}
	if interval <= 0 {
		return fmt.Errorf("网关配额快照构建间隔必须大于 0")
	}
	initialDelay := opts.InitialDelay
	if initialDelay < 0 {
		return fmt.Errorf("网关配额快照构建初始延迟不能小于 0")
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

	service := gatewayquotasnapshot.NewServiceWithOptions(gatewayquotasnapshot.ServiceOptions{
		Store:         store,
		TimezoneStore: store,
	})
	build := func() error {
		snapshot, err := service.Build(ctx, gatewayquotasnapshot.BuildInput{
			Timezone: opts.Timezone,
		})
		if err != nil {
			return err
		}
		logger.Info("网关配额快照构建完成",
			slog.Int("costEntries", len(snapshot.CostEntries)),
			slog.Int("authorizationEntries", len(snapshot.AuthorizationEntries)),
			slog.Bool("costEntriesComplete", snapshot.CostEntriesComplete),
			slog.Bool("authorizationEntriesComplete", snapshot.AuthorizationEntriesComplete),
			slog.String("generatedAt", snapshot.GeneratedAt),
			slog.String("timezone", snapshot.Timezone),
			slog.String("statDate", snapshot.StatDate),
			slog.String("statWeek", snapshot.StatWeek),
			slog.String("statMonth", snapshot.StatMonth),
		)
		return nil
	}

	if opts.RunOnce {
		return build()
	}
	logger.Info("Go 网关配额快照构建 worker 启动",
		slog.Duration("interval", interval),
		slog.Duration("initialDelay", initialDelay),
		slog.String("timezoneOverride", opts.Timezone),
	)
	if err := waitAuthorizationExpirySweepWorker(ctx, initialDelay); err != nil {
		return nil
	}
	for {
		if err := build(); err != nil {
			logger.Error("网关配额快照构建失败", slog.Any("error", err))
		}
		if err := waitAuthorizationExpirySweepWorker(ctx, interval); err != nil {
			return nil
		}
	}
}
