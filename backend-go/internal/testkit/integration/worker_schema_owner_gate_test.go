//go:build integration

package integration

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pressly/goose/v3"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"juhe-ai/backend-go/internal/app"
	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/ownerlock"
	"juhe-ai/backend-go/internal/version"
)

const workerGatePostgresURLEnv = "JUHE_AI_INTEGRATION_POSTGRES_URL"

type workerGatePostgresTarget struct {
	URL     string
	Cleanup func()
}

func resolveWorkerGatePostgresTarget(
	externalURL string,
	startContainer func() (workerGatePostgresTarget, error),
) (workerGatePostgresTarget, error) {
	if externalURL = strings.TrimSpace(externalURL); externalURL != "" {
		return workerGatePostgresTarget{URL: externalURL, Cleanup: func() {}}, nil
	}
	return startContainer()
}

func TestWorkerGatePostgresTargetUsesExternalURLWithoutContainer(t *testing.T) {
	containerStarted := false
	target, err := resolveWorkerGatePostgresTarget("  postgres://fresh-worker-gate  ", func() (workerGatePostgresTarget, error) {
		containerStarted = true
		return workerGatePostgresTarget{}, errors.New("container must not start")
	})
	if err != nil {
		t.Fatalf("resolve external PostgreSQL target: %v", err)
	}
	defer target.Cleanup()
	if target.URL != "postgres://fresh-worker-gate" {
		t.Fatalf("target URL = %q", target.URL)
	}
	if containerStarted {
		t.Fatal("container starter was called for external PostgreSQL URL")
	}
}

func TestWorkerSchemaOwnerGatePostgresSmoke(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	target, err := resolveWorkerGatePostgresTarget(os.Getenv(workerGatePostgresURLEnv), func() (workerGatePostgresTarget, error) {
		testcontainers.SkipIfProviderIsNotHealthy(t)
		container, err := tcpostgres.Run(ctx, postgresImage,
			tcpostgres.WithDatabase("juhe_ai"),
			tcpostgres.WithUsername("juhe_ai"),
			tcpostgres.WithPassword("juhe_ai_password"),
			tcpostgres.BasicWaitStrategies(),
		)
		if err != nil {
			return workerGatePostgresTarget{}, fmt.Errorf("start postgres container: %w", err)
		}
		postgresURL, err := container.ConnectionString(ctx, "sslmode=disable")
		if err != nil {
			_ = container.Terminate(ctx)
			return workerGatePostgresTarget{}, fmt.Errorf("postgres connection string: %w", err)
		}
		return workerGatePostgresTarget{
			URL:     postgresURL,
			Cleanup: func() { terminateContainer(t, ctx, container) },
		}, nil
	})
	if err != nil {
		t.Fatalf("resolve worker gate PostgreSQL target: %v", err)
	}
	defer target.Cleanup()
	postgresURL := target.URL
	db := openSQLDB(t, postgresURL)
	defer closeSQLDB(t, db)
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatalf("set goose dialect: %v", err)
	}
	migrationDir := filepath.Join(repoRoot(t), "db", "migrations")
	if err := goose.UpTo(db, migrationDir, version.SchemaVersion-1); err != nil {
		t.Fatalf("goose up to %d: %v", version.SchemaVersion-1, err)
	}

	cfg := config.Config{
		OwnerLockEnabled:         true,
		OwnerLockPath:            filepath.Join(t.TempDir(), "runtime", "worker.lock"),
		OwnerLockDeploymentEpoch: "integration-epoch",
		OwnerLockRole:            " worker ",
		PostgresURL:              postgresURL,
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	runnerCalled := false
	err = app.RunWorkerWithRuntimeGate(ctx, cfg, logger, func(context.Context) error {
		runnerCalled = true
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "expected 59") {
		t.Fatalf("schema %d gate error = %v, want expected 59 rejection", version.SchemaVersion-1, err)
	}
	if runnerCalled {
		t.Fatalf("runner called with schema %d", version.SchemaVersion-1)
	}
	releasedLock, err := ownerlock.Acquire(cfg.OwnerLockPath, ownerlock.Metadata{RouteOwner: "worker"})
	if err != nil {
		t.Fatalf("owner lock was not released after schema rejection: %v", err)
	}
	if err := releasedLock.Release(); err != nil {
		t.Fatalf("release owner lock after schema rejection check: %v", err)
	}

	if err := goose.UpTo(db, migrationDir, version.SchemaVersion); err != nil {
		t.Fatalf("goose up to %d: %v", version.SchemaVersion, err)
	}
	runnerCalled = false
	err = app.RunWorkerWithRuntimeGate(ctx, cfg, logger, func(context.Context) error {
		runnerCalled = true
		second, acquireErr := ownerlock.Acquire(cfg.OwnerLockPath, ownerlock.Metadata{RouteOwner: "worker"})
		if acquireErr == nil || second != nil {
			if second != nil {
				_ = second.Release()
			}
			return errors.New("worker owner lock was not held during runner")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("schema %d worker gate: %v", version.SchemaVersion, err)
	}
	if !runnerCalled {
		t.Fatalf("runner was not called with schema %d", version.SchemaVersion)
	}
	lock, err := ownerlock.Acquire(cfg.OwnerLockPath, ownerlock.Metadata{DeploymentEpoch: "after", RouteOwner: "worker", PID: os.Getpid()})
	if err != nil {
		t.Fatalf("owner lock after runner: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatalf("release owner lock after runner: %v", err)
	}
}
