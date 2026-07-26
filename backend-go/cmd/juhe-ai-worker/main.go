package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
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
	os.Exit(executeCommand(newRootCommand(defaultWorkerCommandDependencies()), os.Stderr))
}

type workerCommandDependencies struct {
	loadConfig         func() (config.Config, error)
	newLogger          func(string, io.Writer) (*slog.Logger, error)
	newLoggerRuntime   func(string, io.Writer) (*logging.Runtime, error)
	runWithRuntimeGate func(context.Context, config.Config, *slog.Logger, app.WorkerRunner) error
}

type workerRunnerFactory func(config.Config, *slog.Logger) app.WorkerRunner

func defaultWorkerCommandDependencies() workerCommandDependencies {
	return workerCommandDependencies{
		loadConfig: func() (config.Config, error) {
			return config.Load(config.LoadOptions{LoadDotEnv: true})
		},
		newLogger: logging.New,
		newLoggerRuntime: func(level string, output io.Writer) (*logging.Runtime, error) {
			return logging.NewRuntime(level, output, logging.RuntimeOptions{Role: "go-worker"})
		},
		runWithRuntimeGate: app.RunWorkerWithRuntimeGate,
	}
}

func newRootCommand(deps workerCommandDependencies) *cobra.Command {
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
		RunE: newWorkerCommandRunE(deps, func(cfg config.Config, logger *slog.Logger) app.WorkerRunner {
			return func(ctx context.Context) error { return app.RunIngestWorker(ctx, cfg, logger) }
		}),
	})
	root.AddCommand(&cobra.Command{
		Use:   "account-test",
		Short: "Run account test bridge worker",
		RunE: newWorkerCommandRunE(deps, func(cfg config.Config, logger *slog.Logger) app.WorkerRunner {
			return func(ctx context.Context) error { return app.RunAccountTestWorker(ctx, cfg, logger) }
		}),
	})
	expirySweepOptions := app.AuthorizationExpirySweepWorkerOptions{
		Interval:     time.Minute,
		InitialDelay: 54 * time.Second,
	}
	expirySweepCommand := &cobra.Command{
		Use:   "authorization-expiry-sweep",
		Short: "Run authorization expiry sweep worker",
		RunE: newWorkerCommandRunE(deps, func(cfg config.Config, logger *slog.Logger) app.WorkerRunner {
			return func(ctx context.Context) error {
				return app.RunAuthorizationExpirySweepWorker(ctx, cfg, logger, expirySweepOptions)
			}
		}),
	}
	expirySweepCommand.Flags().IntVar(&expirySweepOptions.Limit, "limit", 0, "maximum grants to expire per sweep; 0 uses the service default")
	expirySweepCommand.Flags().DurationVar(&expirySweepOptions.Interval, "interval", expirySweepOptions.Interval, "sweep interval")
	expirySweepCommand.Flags().DurationVar(&expirySweepOptions.InitialDelay, "initial-delay", expirySweepOptions.InitialDelay, "initial delay before the first sweep")
	expirySweepCommand.Flags().BoolVar(&expirySweepOptions.RunOnce, "run-once", false, "run one sweep and exit")
	root.AddCommand(expirySweepCommand)

	operationLogRetentionCleanupOptions := app.OperationLogRetentionCleanupWorkerOptions{
		Interval:     10 * time.Minute,
		InitialDelay: 13 * time.Minute,
	}
	operationLogRetentionCleanupCommand := &cobra.Command{
		Use:   "operation-log-retention-cleanup",
		Short: "Run operation log retention cleanup worker",
		RunE: newWorkerCommandRunE(deps, func(cfg config.Config, logger *slog.Logger) app.WorkerRunner {
			return func(ctx context.Context) error {
				return app.RunOperationLogRetentionCleanupWorker(ctx, cfg, logger, operationLogRetentionCleanupOptions)
			}
		}),
	}
	operationLogRetentionCleanupCommand.Flags().IntVar(&operationLogRetentionCleanupOptions.RetentionDays, "retention-days", 0, "override operation log retention days; 0 reads system settings")
	operationLogRetentionCleanupCommand.Flags().IntVar(&operationLogRetentionCleanupOptions.BatchSize, "batch-size", 0, "operation logs to delete per batch; 0 uses the service default")
	operationLogRetentionCleanupCommand.Flags().IntVar(&operationLogRetentionCleanupOptions.MaxBatches, "max-batches", 0, "maximum cleanup batches per run; 0 uses the service default")
	operationLogRetentionCleanupCommand.Flags().DurationVar(&operationLogRetentionCleanupOptions.Interval, "interval", operationLogRetentionCleanupOptions.Interval, "cleanup interval")
	operationLogRetentionCleanupCommand.Flags().DurationVar(&operationLogRetentionCleanupOptions.InitialDelay, "initial-delay", operationLogRetentionCleanupOptions.InitialDelay, "initial delay before the first cleanup")
	operationLogRetentionCleanupCommand.Flags().BoolVar(&operationLogRetentionCleanupOptions.RunOnce, "run-once", false, "run one cleanup and exit")
	root.AddCommand(operationLogRetentionCleanupCommand)

	usageRangeWindowOptions := app.AuthorizationUsageRangeWindowRefreshWorkerOptions{
		Interval:     6 * time.Hour,
		InitialDelay: 43 * time.Minute,
	}
	usageRangeWindowCommand := &cobra.Command{
		Use:   "authorization-usage-range-windows-refresh",
		Short: "Run authorization usage range window refresh worker",
		RunE: newWorkerCommandRunE(deps, func(cfg config.Config, logger *slog.Logger) app.WorkerRunner {
			return func(ctx context.Context) error {
				return app.RunAuthorizationUsageRangeWindowRefreshWorker(ctx, cfg, logger, usageRangeWindowOptions)
			}
		}),
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
		RunE: newWorkerCommandRunE(deps, func(cfg config.Config, logger *slog.Logger) app.WorkerRunner {
			return func(ctx context.Context) error {
				return app.RunGatewayQuotaSnapshotBuildWorker(ctx, cfg, logger, gatewayQuotaSnapshotOptions)
			}
		}),
	}
	gatewayQuotaSnapshotCommand.Flags().DurationVar(&gatewayQuotaSnapshotOptions.Interval, "interval", gatewayQuotaSnapshotOptions.Interval, "build interval")
	gatewayQuotaSnapshotCommand.Flags().DurationVar(&gatewayQuotaSnapshotOptions.InitialDelay, "initial-delay", gatewayQuotaSnapshotOptions.InitialDelay, "initial delay before the first build")
	gatewayQuotaSnapshotCommand.Flags().BoolVar(&gatewayQuotaSnapshotOptions.RunOnce, "run-once", false, "run one build and exit")
	gatewayQuotaSnapshotCommand.Flags().StringVar(&gatewayQuotaSnapshotOptions.Timezone, "timezone", "", "override usageStatsTimezone for local smoke; empty reads system settings")
	gatewayQuotaSnapshotCommand.Flags().BoolVar(&gatewayQuotaSnapshotOptions.PublishRuntimeState, "publish-runtime-state", false, "publish the built snapshot to Redis runtime state for gateway consumption")
	gatewayQuotaSnapshotCommand.Flags().DurationVar(&gatewayQuotaSnapshotOptions.SnapshotTTL, "snapshot-ttl", 0, "Redis runtime state snapshot TTL; 0 uses the service default")
	root.AddCommand(gatewayQuotaSnapshotCommand)

	modelQualityHealthSyncOptions := app.ModelQualityHealthSyncWorkerOptions{
		Enabled:      true,
		Interval:     time.Minute,
		InitialDelay: 58 * time.Second,
	}
	modelQualityHealthSyncCommand := &cobra.Command{
		Use:   "model-quality-health-sync",
		Short: "Run the Go model-quality health-sync retry worker",
		RunE: newWorkerCommandRunE(deps, func(cfg config.Config, logger *slog.Logger) app.WorkerRunner {
			return func(ctx context.Context) error {
				return app.RunModelQualityHealthSyncWorker(ctx, cfg, logger, modelQualityHealthSyncOptions)
			}
		}),
	}
	modelQualityHealthSyncCommand.Flags().StringVar(&modelQualityHealthSyncOptions.OwnerID, "owner-id", "", "stable owner ID; empty generates one per process")
	modelQualityHealthSyncCommand.Flags().DurationVar(&modelQualityHealthSyncOptions.Interval, "interval", modelQualityHealthSyncOptions.Interval, "retry interval")
	modelQualityHealthSyncCommand.Flags().DurationVar(&modelQualityHealthSyncOptions.InitialDelay, "initial-delay", modelQualityHealthSyncOptions.InitialDelay, "initial delay before the first retry")
	modelQualityHealthSyncCommand.Flags().IntVar(&modelQualityHealthSyncOptions.BatchSize, "batch-size", 0, "runs to claim per batch; 0 uses the service default")
	modelQualityHealthSyncCommand.Flags().IntVar(&modelQualityHealthSyncOptions.Workers, "workers", 0, "parallel completion workers; 0 uses the service default")
	modelQualityHealthSyncCommand.Flags().DurationVar(&modelQualityHealthSyncOptions.Lease, "lease", 0, "claim lease duration; 0 uses the service default")
	modelQualityHealthSyncCommand.Flags().DurationVar(&modelQualityHealthSyncOptions.AttemptTimeout, "attempt-timeout", 0, "claim/complete timeout; 0 uses the service default")
	modelQualityHealthSyncCommand.Flags().BoolVar(&modelQualityHealthSyncOptions.GoExclusiveOwner, "go-exclusive-owner", false, "assert that Go is the only health-sync writer")
	modelQualityHealthSyncCommand.Flags().BoolVar(&modelQualityHealthSyncOptions.LegacyWorkerDrained, "legacy-worker-drained", false, "assert that the legacy Node health-sync worker is drained")
	modelQualityHealthSyncCommand.Flags().BoolVar(&modelQualityHealthSyncOptions.NodeRetentionSafe, "node-retention-safe", false, "assert that Node retention preserves failed and claimed health-sync runs")
	modelQualityHealthSyncCommand.Flags().BoolVar(&modelQualityHealthSyncOptions.RunOnce, "run-once", false, "run one batch and exit")
	root.AddCommand(modelQualityHealthSyncCommand)

	cooldownAccountRetestOptions := app.CooldownAccountRetestWorkerOptions{
		InitialDelay: 60 * time.Second,
	}
	cooldownAccountRetestCommand := &cobra.Command{
		Use:   "cooldown-account-retest",
		Short: "Run account-level cooldown retest scheduler and worker",
		RunE: newWorkerCommandRunE(deps, func(cfg config.Config, logger *slog.Logger) app.WorkerRunner {
			return func(ctx context.Context) error {
				return app.RunCooldownAccountRetestWorker(ctx, cfg, logger, cooldownAccountRetestOptions)
			}
		}),
	}
	cooldownAccountRetestCommand.Flags().DurationVar(
		&cooldownAccountRetestOptions.InitialDelay,
		"initial-delay",
		cooldownAccountRetestOptions.InitialDelay,
		"initial delay before the first account-level cooldown retest scan",
	)
	root.AddCommand(cooldownAccountRetestCommand)

	return root
}

func newWorkerCommandRunE(deps workerCommandDependencies, factory workerRunnerFactory) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, _ []string) error {
		cfg, err := deps.loadConfig()
		if err != nil {
			return err
		}
		var logger *slog.Logger
		var loggerRuntime *logging.Runtime
		if deps.newLoggerRuntime != nil {
			loggerRuntime, err = deps.newLoggerRuntime(cfg.LogLevel, cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			logger = loggerRuntime.Logger
		} else {
			logger, err = deps.newLogger(cfg.LogLevel, cmd.ErrOrStderr())
			if err != nil {
				return err
			}
		}
		ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		runErr := deps.runWithRuntimeGate(ctx, cfg, logger, factory(cfg, logger))
		if loggerRuntime == nil {
			return runErr
		}
		shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer cancelShutdown()
		return errors.Join(runErr, loggerRuntime.Shutdown(shutdownCtx))
	}
}

func executeCommand(root *cobra.Command, stderr io.Writer) int {
	root.SetErr(stderr)
	root.SilenceErrors = true
	root.SilenceUsage = true
	if err := root.Execute(); err != nil {
		logging.WriteFatal(stderr, err)
		return 1
	}
	return 0
}
