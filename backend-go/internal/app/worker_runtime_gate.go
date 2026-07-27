package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
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
	readManifest func(string) (workerOwnerManifest, error)
	acquire      func(string, ownerlock.Metadata) (workerRuntimeLock, error)
	openStore    func(context.Context, string) (workerRuntimeSchemaStore, error)
}

type workerOwnerManifest struct {
	SchemaVersion   int    `json:"schemaVersion"`
	DeploymentEpoch string `json:"deploymentEpoch"`
	Release         struct {
		NodeVersion   string `json:"nodeVersion"`
		GoVersion     string `json:"goVersion"`
		SchemaVersion int64  `json:"schemaVersion"`
	} `json:"release"`
	RouteOwners struct {
		Management string `json:"management"`
		Public     string `json:"public"`
		Gateway    string `json:"gateway"`
		Worker     string `json:"worker"`
	} `json:"routeOwners"`
	RollbackRouteOwners struct {
		Management string `json:"management"`
		Public     string `json:"public"`
		Gateway    string `json:"gateway"`
		Worker     string `json:"worker"`
	} `json:"rollbackRouteOwners"`
	RouteAllowlist []json.RawMessage `json:"routeAllowlist"`
}

func RunWorkerWithRuntimeGate(
	ctx context.Context,
	cfg config.Config,
	logger *slog.Logger,
	runner WorkerRunner,
) error {
	return runWorkerWithRuntimeGate(ctx, cfg, logger, runner, workerRuntimeGateDependencies{
		readManifest: readWorkerOwnerManifest,
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
		return fmt.Errorf("Go mutating worker requires JUHE_AI_OWNER_LOCK_ENABLED=true")
	}
	if strings.TrimSpace(cfg.OwnerLockRole) != "worker" {
		return fmt.Errorf("Go worker owner lock role must be worker")
	}
	if !cfg.GoWorkerExclusiveOwner {
		return fmt.Errorf("Go mutating worker requires JUHE_AI_GO_WORKER_EXCLUSIVE_OWNER=true")
	}
	if !cfg.LegacyNodeWorkerDrained {
		return fmt.Errorf("Go mutating worker requires JUHE_AI_LEGACY_NODE_WORKER_DRAINED=true")
	}
	if !filepath.IsAbs(strings.TrimSpace(cfg.OwnerLockPath)) {
		return fmt.Errorf("Go worker owner lock path must be absolute")
	}
	manifestPath := strings.TrimSpace(cfg.OwnerManifestPath)
	if !filepath.IsAbs(manifestPath) {
		return fmt.Errorf("Go worker owner manifest path must be absolute")
	}
	readManifest := deps.readManifest
	if readManifest == nil {
		readManifest = readWorkerOwnerManifest
	}
	manifest, err := readManifest(manifestPath)
	if err != nil {
		return fmt.Errorf("read worker owner manifest: %w", err)
	}
	if err := validateWorkerOwnerManifest(manifest, cfg); err != nil {
		return err
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

func readWorkerOwnerManifest(path string) (workerOwnerManifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return workerOwnerManifest{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var manifest workerOwnerManifest
	if err := decoder.Decode(&manifest); err != nil {
		return workerOwnerManifest{}, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return workerOwnerManifest{}, fmt.Errorf("owner manifest contains multiple JSON values")
		}
		return workerOwnerManifest{}, err
	}
	return manifest, nil
}

func validateWorkerOwnerManifest(manifest workerOwnerManifest, cfg config.Config) error {
	if manifest.SchemaVersion != 2 {
		return fmt.Errorf("worker owner manifest schemaVersion must be 2")
	}
	if manifest.DeploymentEpoch != strings.TrimSpace(cfg.OwnerLockDeploymentEpoch) {
		return fmt.Errorf("worker owner manifest deployment epoch does not match owner lock epoch")
	}
	if manifest.RouteOwners.Worker != "go" {
		return fmt.Errorf("worker owner manifest must declare routeOwners.worker=go")
	}
	if manifest.Release.GoVersion != version.Version {
		return fmt.Errorf("worker owner manifest Go version must be %s", version.Version)
	}
	if manifest.Release.SchemaVersion != version.SchemaVersion {
		return fmt.Errorf("worker owner manifest schema version must be %d", version.SchemaVersion)
	}
	return nil
}
