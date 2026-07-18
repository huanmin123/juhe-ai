package app

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/ownerlock"
	postgresstore "juhe-ai/backend-go/internal/store/postgres"
	"juhe-ai/backend-go/internal/version"
)

const workerRuntimeGateTimeout = 5 * time.Second

type WorkerRunner func(context.Context) error

type workerRuntimeLock interface {
	Release() error
}

type workerRuntimeSchemaStore interface {
	Ping(context.Context) error
	RequireGooseSchemaVersion(context.Context, int64) error
	Close()
}

type workerRuntimeGateDependencies struct {
	acquire   func(string, ownerlock.Metadata) (workerRuntimeLock, error)
	openStore func(context.Context, string) (workerRuntimeSchemaStore, error)
}

func RunWorkerWithRuntimeGate(
	ctx context.Context,
	cfg config.Config,
	logger *slog.Logger,
	runner WorkerRunner,
) error {
	return runWorkerWithRuntimeGate(ctx, cfg, logger, runner, workerRuntimeGateDependencies{
		acquire: func(path string, metadata ownerlock.Metadata) (workerRuntimeLock, error) {
			return ownerlock.Acquire(path, metadata)
		},
		openStore: func(ctx context.Context, rawURL string) (workerRuntimeSchemaStore, error) {
			return postgresstore.Open(ctx, rawURL)
		},
	})
}

func runWorkerWithRuntimeGate(
	ctx context.Context,
	cfg config.Config,
	logger *slog.Logger,
	runner WorkerRunner,
	deps workerRuntimeGateDependencies,
) error {
	if runner == nil {
		return fmt.Errorf("worker runner is required")
	}
	if !cfg.OwnerLockEnabled {
		return runner(ctx)
	}
	if strings.TrimSpace(cfg.OwnerLockRole) != "worker" {
		return fmt.Errorf("Go worker owner lock role must be worker")
	}
	if logger == nil {
		logger = slog.Default()
	}

	runtimeLock, err := deps.acquire(cfg.OwnerLockPath, ownerlock.Metadata{
		DeploymentEpoch: cfg.OwnerLockDeploymentEpoch,
		RouteOwner:      "worker",
		Version:         version.Version,
		PID:             os.Getpid(),
	})
	if err != nil {
		return err
	}
	defer func() {
		if err := runtimeLock.Release(); err != nil {
			logger.Error("释放 Go worker owner lock 失败", slog.String("error", err.Error()))
		}
	}()

	store, err := deps.openStore(ctx, cfg.PostgresURL)
	if err != nil {
		return fmt.Errorf("open worker schema gate PostgreSQL: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, workerRuntimeGateTimeout)
	err = store.Ping(pingCtx)
	cancel()
	if err != nil {
		store.Close()
		return fmt.Errorf("ping worker schema gate PostgreSQL: %w", err)
	}
	schemaCtx, cancel := context.WithTimeout(ctx, workerRuntimeGateTimeout)
	err = store.RequireGooseSchemaVersion(schemaCtx, version.SchemaVersion)
	cancel()
	store.Close()
	if err != nil {
		return fmt.Errorf("require goose schema version %d: %w", version.SchemaVersion, err)
	}

	return runner(ctx)
}
