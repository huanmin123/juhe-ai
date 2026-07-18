package app

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/ownerlock"
)

func TestRunWorkerWithRuntimeGateDisabledCallsRunnerWithoutDependencies(t *testing.T) {
	called := false
	err := runWorkerWithRuntimeGate(t.Context(), config.Config{}, nil, func(context.Context) error {
		called = true
		return nil
	}, workerRuntimeGateDependencies{
		acquire: func(string, ownerlock.Metadata) (workerRuntimeLock, error) {
			t.Fatal("disabled gate acquired owner lock")
			return nil, nil
		},
		openStore: func(context.Context, string) (workerRuntimeSchemaStore, error) {
			t.Fatal("disabled gate opened PostgreSQL")
			return nil, nil
		},
	})
	if err != nil {
		t.Fatalf("runWorkerWithRuntimeGate() error = %v", err)
	}
	if !called {
		t.Fatal("runner was not called")
	}
}

func TestRunWorkerWithRuntimeGateRejectsNilRunner(t *testing.T) {
	err := RunWorkerWithRuntimeGate(t.Context(), config.Config{}, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "runner") {
		t.Fatalf("RunWorkerWithRuntimeGate() error = %v, want runner error", err)
	}
}

func TestRunWorkerWithRuntimeGateRejectsNonWorkerRoleBeforeDependencies(t *testing.T) {
	cfg := enabledWorkerGateConfig(t)
	cfg.OwnerLockRole = " server "
	err := runWorkerWithRuntimeGate(t.Context(), cfg, nil, func(context.Context) error {
		t.Fatal("runner called with invalid role")
		return nil
	}, workerRuntimeGateDependencies{
		acquire: func(string, ownerlock.Metadata) (workerRuntimeLock, error) {
			t.Fatal("owner lock acquired with invalid role")
			return nil, nil
		},
		openStore: func(context.Context, string) (workerRuntimeSchemaStore, error) {
			t.Fatal("PostgreSQL opened with invalid role")
			return nil, nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "role must be worker") {
		t.Fatalf("runWorkerWithRuntimeGate() error = %v, want worker role error", err)
	}
}

func TestRunWorkerWithRuntimeGateOrdersAdmissionAndReleasesAfterRunner(t *testing.T) {
	cfg := enabledWorkerGateConfig(t)
	var calls []string
	lock := &fakeWorkerRuntimeLock{release: func() error {
		calls = append(calls, "release")
		return nil
	}}
	store := &fakeWorkerRuntimeSchemaStore{
		ping: func(ctx context.Context) error {
			assertDeadlineWithin(t, ctx, 5*time.Second)
			calls = append(calls, "ping")
			return nil
		},
		requireSchema: func(ctx context.Context, expected int64) error {
			assertDeadlineWithin(t, ctx, 5*time.Second)
			if expected != 57 {
				t.Fatalf("schema version = %d, want 57", expected)
			}
			calls = append(calls, "schema")
			return nil
		},
		close: func() { calls = append(calls, "close") },
	}
	runnerErr := errors.New("runner stopped")
	err := runWorkerWithRuntimeGate(t.Context(), cfg, nil, func(context.Context) error {
		calls = append(calls, "runner")
		return runnerErr
	}, workerRuntimeGateDependencies{
		acquire: func(path string, metadata ownerlock.Metadata) (workerRuntimeLock, error) {
			if path != cfg.OwnerLockPath {
				t.Fatalf("lock path = %q, want %q", path, cfg.OwnerLockPath)
			}
			if metadata.DeploymentEpoch != cfg.OwnerLockDeploymentEpoch || metadata.RouteOwner != "worker" || metadata.Version == "" || metadata.PID != os.Getpid() {
				t.Fatalf("metadata = %+v", metadata)
			}
			calls = append(calls, "acquire")
			return lock, nil
		},
		openStore: func(ctx context.Context, rawURL string) (workerRuntimeSchemaStore, error) {
			if rawURL != cfg.PostgresURL {
				t.Fatalf("postgres URL = %q, want %q", rawURL, cfg.PostgresURL)
			}
			calls = append(calls, "open")
			return store, nil
		},
	})
	if !errors.Is(err, runnerErr) {
		t.Fatalf("runWorkerWithRuntimeGate() error = %v, want runner error", err)
	}
	want := []string{"acquire", "open", "ping", "schema", "close", "runner", "release"}
	if strings.Join(calls, ",") != strings.Join(want, ",") {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
}

func TestRunWorkerWithRuntimeGateFailsClosedAndReleases(t *testing.T) {
	for _, test := range []struct {
		name      string
		openError error
		pingError error
		schemaErr error
		wantClose bool
	}{
		{name: "open", openError: errors.New("open failed")},
		{name: "ping", pingError: errors.New("ping failed"), wantClose: true},
		{name: "schema", schemaErr: errors.New("schema failed"), wantClose: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := enabledWorkerGateConfig(t)
			released := false
			closed := false
			store := &fakeWorkerRuntimeSchemaStore{
				ping:          func(context.Context) error { return test.pingError },
				requireSchema: func(context.Context, int64) error { return test.schemaErr },
				close:         func() { closed = true },
			}
			err := runWorkerWithRuntimeGate(t.Context(), cfg, nil, func(context.Context) error {
				t.Fatal("runner called after admission failure")
				return nil
			}, workerRuntimeGateDependencies{
				acquire: func(string, ownerlock.Metadata) (workerRuntimeLock, error) {
					return &fakeWorkerRuntimeLock{release: func() error { released = true; return nil }}, nil
				},
				openStore: func(context.Context, string) (workerRuntimeSchemaStore, error) {
					if test.openError != nil {
						return nil, test.openError
					}
					return store, nil
				},
			})
			if err == nil {
				t.Fatal("runWorkerWithRuntimeGate() error = nil")
			}
			if released != true {
				t.Fatal("owner lock was not released")
			}
			if closed != test.wantClose {
				t.Fatalf("store closed = %v, want %v", closed, test.wantClose)
			}
		})
	}
}

func TestRunWorkerWithRuntimeGateHoldsRealLockForRunnerAndReleasesIt(t *testing.T) {
	cfg := enabledWorkerGateConfig(t)
	store := &fakeWorkerRuntimeSchemaStore{}
	err := runWorkerWithRuntimeGate(t.Context(), cfg, nil, func(context.Context) error {
		second, err := ownerlock.Acquire(cfg.OwnerLockPath, ownerlock.Metadata{RouteOwner: "worker"})
		if err == nil || second != nil {
			t.Fatalf("second lock during runner = (%v, %v), want contention", second, err)
		}
		return nil
	}, workerRuntimeGateDependencies{
		acquire: func(path string, metadata ownerlock.Metadata) (workerRuntimeLock, error) {
			return ownerlock.Acquire(path, metadata)
		},
		openStore: func(context.Context, string) (workerRuntimeSchemaStore, error) { return store, nil },
	})
	if err != nil {
		t.Fatalf("runWorkerWithRuntimeGate() error = %v", err)
	}
	third, err := ownerlock.Acquire(cfg.OwnerLockPath, ownerlock.Metadata{RouteOwner: "worker"})
	if err != nil {
		t.Fatalf("acquire after runner: %v", err)
	}
	if err := third.Release(); err != nil {
		t.Fatalf("release third lock: %v", err)
	}
}

func TestRunWorkerWithRuntimeGateLogsReleaseFailureWithoutReplacingRunnerResult(t *testing.T) {
	cfg := enabledWorkerGateConfig(t)
	var output bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&output, nil))
	runnerErr := errors.New("runner result")
	err := runWorkerWithRuntimeGate(t.Context(), cfg, logger, func(context.Context) error { return runnerErr }, workerRuntimeGateDependencies{
		acquire: func(string, ownerlock.Metadata) (workerRuntimeLock, error) {
			return &fakeWorkerRuntimeLock{release: func() error { return errors.New("release failed") }}, nil
		},
		openStore: func(context.Context, string) (workerRuntimeSchemaStore, error) {
			return &fakeWorkerRuntimeSchemaStore{}, nil
		},
	})
	if !errors.Is(err, runnerErr) {
		t.Fatalf("runWorkerWithRuntimeGate() error = %v, want runner error", err)
	}
	if !strings.Contains(output.String(), "ERROR") || !strings.Contains(output.String(), "release failed") {
		t.Fatalf("log output = %q, want ERROR release diagnostic", output.String())
	}
}

func TestRunWorkerWithRuntimeGateReleasesAfterContextCanceledRunner(t *testing.T) {
	cfg := enabledWorkerGateConfig(t)
	ctx, cancel := context.WithCancel(t.Context())
	released := false
	err := runWorkerWithRuntimeGate(ctx, cfg, nil, func(ctx context.Context) error {
		cancel()
		return ctx.Err()
	}, workerRuntimeGateDependencies{
		acquire: func(string, ownerlock.Metadata) (workerRuntimeLock, error) {
			return &fakeWorkerRuntimeLock{release: func() error {
				released = true
				return nil
			}}, nil
		},
		openStore: func(context.Context, string) (workerRuntimeSchemaStore, error) {
			return &fakeWorkerRuntimeSchemaStore{}, nil
		},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("runWorkerWithRuntimeGate() error = %v, want context canceled", err)
	}
	if !released {
		t.Fatal("owner lock was not released after context cancellation")
	}
}

func enabledWorkerGateConfig(t *testing.T) config.Config {
	t.Helper()
	return config.Config{
		OwnerLockEnabled:         true,
		OwnerLockPath:            filepath.Join(t.TempDir(), "runtime", "worker.lock"),
		OwnerLockDeploymentEpoch: "epoch-test",
		OwnerLockRole:            " worker ",
		PostgresURL:              "postgres://worker-gate-test",
	}
}

func assertDeadlineWithin(t *testing.T, ctx context.Context, maximum time.Duration) {
	t.Helper()
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("context has no deadline")
	}
	remaining := time.Until(deadline)
	if remaining <= 0 || remaining > maximum {
		t.Fatalf("deadline remaining = %s, want within %s", remaining, maximum)
	}
}

type fakeWorkerRuntimeLock struct{ release func() error }

func (lock *fakeWorkerRuntimeLock) Release() error {
	if lock.release == nil {
		return nil
	}
	return lock.release()
}

type fakeWorkerRuntimeSchemaStore struct {
	ping          func(context.Context) error
	requireSchema func(context.Context, int64) error
	close         func()
}

func (store *fakeWorkerRuntimeSchemaStore) Ping(ctx context.Context) error {
	if store.ping == nil {
		return nil
	}
	return store.ping(ctx)
}

func (store *fakeWorkerRuntimeSchemaStore) RequireGooseSchemaVersion(ctx context.Context, expected int64) error {
	if store.requireSchema == nil {
		return nil
	}
	return store.requireSchema(ctx, expected)
}

func (store *fakeWorkerRuntimeSchemaStore) Close() {
	if store.close != nil {
		store.close()
	}
}
