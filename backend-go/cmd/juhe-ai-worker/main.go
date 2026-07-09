package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"juhe-ai/backend-go/internal/app"
	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/logging"
	"juhe-ai/backend-go/internal/version"
)

func main() {
	root := &cobra.Command{
		Use:     "juhe-ai-worker",
		Version: version.Version,
		Short:   "juhe-ai Go worker process",
	}

	root.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print version",
		Run: func(cmd *cobra.Command, args []string) {
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), version.Version)
		},
	})
	root.AddCommand(&cobra.Command{
		Use:   "ingest",
		Short: "Run ingest worker",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(config.LoadOptions{LoadDotEnv: true})
			if err != nil {
				return err
			}

			logger, err := logging.New(cfg.LogLevel, cmd.ErrOrStderr())
			if err != nil {
				return err
			}

			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()

			return app.RunIngestWorker(ctx, cfg, logger)
		},
	})
	expirySweepOptions := app.AuthorizationExpirySweepWorkerOptions{
		Interval:     time.Minute,
		InitialDelay: 54 * time.Second,
	}
	expirySweepCommand := &cobra.Command{
		Use:   "authorization-expiry-sweep",
		Short: "Run authorization expiry sweep worker",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(config.LoadOptions{LoadDotEnv: true})
			if err != nil {
				return err
			}

			logger, err := logging.New(cfg.LogLevel, cmd.ErrOrStderr())
			if err != nil {
				return err
			}

			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()

			return app.RunAuthorizationExpirySweepWorker(ctx, cfg, logger, expirySweepOptions)
		},
	}
	expirySweepCommand.Flags().IntVar(&expirySweepOptions.Limit, "limit", 0, "maximum grants to expire per sweep; 0 uses the service default")
	expirySweepCommand.Flags().DurationVar(&expirySweepOptions.Interval, "interval", expirySweepOptions.Interval, "sweep interval")
	expirySweepCommand.Flags().DurationVar(&expirySweepOptions.InitialDelay, "initial-delay", expirySweepOptions.InitialDelay, "initial delay before the first sweep")
	expirySweepCommand.Flags().BoolVar(&expirySweepOptions.RunOnce, "run-once", false, "run one sweep and exit")
	root.AddCommand(expirySweepCommand)

	usageRangeWindowOptions := app.AuthorizationUsageRangeWindowRefreshWorkerOptions{
		Interval:     6 * time.Hour,
		InitialDelay: 43 * time.Minute,
	}
	usageRangeWindowCommand := &cobra.Command{
		Use:   "authorization-usage-range-windows-refresh",
		Short: "Run authorization usage range window refresh worker",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(config.LoadOptions{LoadDotEnv: true})
			if err != nil {
				return err
			}

			logger, err := logging.New(cfg.LogLevel, cmd.ErrOrStderr())
			if err != nil {
				return err
			}

			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()

			return app.RunAuthorizationUsageRangeWindowRefreshWorker(ctx, cfg, logger, usageRangeWindowOptions)
		},
	}
	usageRangeWindowCommand.Flags().DurationVar(&usageRangeWindowOptions.Interval, "interval", usageRangeWindowOptions.Interval, "refresh interval")
	usageRangeWindowCommand.Flags().DurationVar(&usageRangeWindowOptions.InitialDelay, "initial-delay", usageRangeWindowOptions.InitialDelay, "initial delay before the first refresh")
	usageRangeWindowCommand.Flags().BoolVar(&usageRangeWindowOptions.RunOnce, "run-once", false, "run one refresh and exit")
	usageRangeWindowCommand.Flags().StringVar(&usageRangeWindowOptions.Timezone, "timezone", "", "override usageStatsTimezone for local smoke; empty reads system settings")
	root.AddCommand(usageRangeWindowCommand)

	gatewayQuotaSnapshotOptions := app.GatewayQuotaSnapshotBuildWorkerOptions{
		Interval:     time.Minute,
		InitialDelay: 37 * time.Second,
	}
	gatewayQuotaSnapshotCommand := &cobra.Command{
		Use:   "gateway-quota-snapshot-build",
		Short: "Build gateway quota snapshot from PostgreSQL aggregates",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(config.LoadOptions{LoadDotEnv: true})
			if err != nil {
				return err
			}

			logger, err := logging.New(cfg.LogLevel, cmd.ErrOrStderr())
			if err != nil {
				return err
			}

			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()

			return app.RunGatewayQuotaSnapshotBuildWorker(ctx, cfg, logger, gatewayQuotaSnapshotOptions)
		},
	}
	gatewayQuotaSnapshotCommand.Flags().DurationVar(&gatewayQuotaSnapshotOptions.Interval, "interval", gatewayQuotaSnapshotOptions.Interval, "build interval")
	gatewayQuotaSnapshotCommand.Flags().DurationVar(&gatewayQuotaSnapshotOptions.InitialDelay, "initial-delay", gatewayQuotaSnapshotOptions.InitialDelay, "initial delay before the first build")
	gatewayQuotaSnapshotCommand.Flags().BoolVar(&gatewayQuotaSnapshotOptions.RunOnce, "run-once", false, "run one build and exit")
	gatewayQuotaSnapshotCommand.Flags().StringVar(&gatewayQuotaSnapshotOptions.Timezone, "timezone", "", "override usageStatsTimezone for local smoke; empty reads system settings")
	gatewayQuotaSnapshotCommand.Flags().BoolVar(&gatewayQuotaSnapshotOptions.PublishRuntimeState, "publish-runtime-state", false, "publish the built snapshot to Redis runtime state for gateway consumption")
	gatewayQuotaSnapshotCommand.Flags().DurationVar(&gatewayQuotaSnapshotOptions.SnapshotTTL, "snapshot-ttl", 0, "Redis runtime state snapshot TTL; 0 uses the service default")
	root.AddCommand(gatewayQuotaSnapshotCommand)

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}
