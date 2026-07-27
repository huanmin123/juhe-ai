package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/ownerlock"
	"juhe-ai/backend-go/internal/version"
)

func TestRunWorkerWithRuntimeGateDisabledFailsClosedWithoutDependencies(t *testing.T) {
	err := runWorkerWithRuntimeGate(t.Context(), config.Config{}, nil, func(context.Context) error {
		t.Fatal("runner called without worker owner lock")
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
	if err == nil || !strings.Contains(err.Error(), "JUHE_AI_OWNER_LOCK_ENABLED=true") {
		t.Fatalf("runWorkerWithRuntimeGate() error = %v, want owner lock requirement", err)
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

func TestRunWorkerWithRuntimeGateRejectsIncompleteOwnershipEvidenceBeforeDependencies(t *testing.T) {
	tests := []struct {
		name string
		edit func(*config.Config)
		want string
	}{
		{name: "exclusive owner", edit: func(cfg *config.Config) { cfg.GoWorkerExclusiveOwner = false }, want: "GO_WORKER_EXCLUSIVE_OWNER"},
		{name: "legacy drained", edit: func(cfg *config.Config) { cfg.LegacyNodeWorkerDrained = false }, want: "LEGACY_NODE_WORKER_DRAINED"},
		{name: "absolute lock path", edit: func(cfg *config.Config) { cfg.OwnerLockPath = "runtime/worker.lock" }, want: "lock path must be absolute"},
		{name: "absolute manifest path", edit: func(cfg *config.Config) { cfg.OwnerManifestPath = "deploy/owner-manifest.json" }, want: "manifest path must be absolute"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := enabledWorkerGateConfig(t)
			test.edit(&cfg)
			err := runWorkerWithRuntimeGate(t.Context(), cfg, nil, func(context.Context) error {
				t.Fatal("runner called without complete ownership evidence")
				return nil
			}, workerRuntimeGateDependencies{
				readManifest: func(string) (workerOwnerManifest, error) {
					t.Fatal("manifest read before ownership preconditions passed")
					return workerOwnerManifest{}, nil
				},
				acquire: func(string, ownerlock.Metadata) (workerRuntimeLock, error) {
					t.Fatal("owner lock acquired without complete ownership evidence")
					return nil, nil
				},
				openStore: func(context.Context, string) (workerRuntimeSchemaStore, error) {
					t.Fatal("PostgreSQL opened without complete ownership evidence")
					return nil, nil
				},
			})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("runWorkerWithRuntimeGate() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestRunWorkerWithRuntimeGateRejectsManifestMismatchBeforeLock(t *testing.T) {
	tests := []struct {
		name string
		edit func(*workerOwnerManifest)
		want string
	}{
		{name: "schema", edit: func(manifest *workerOwnerManifest) { manifest.SchemaVersion = 1 }, want: "schemaVersion"},
		{name: "epoch", edit: func(manifest *workerOwnerManifest) { manifest.DeploymentEpoch = "other" }, want: "deployment epoch"},
		{name: "Node version", edit: func(manifest *workerOwnerManifest) { manifest.Release.NodeVersion = "" }, want: "Node version"},
		{name: "invalid management owner", edit: func(manifest *workerOwnerManifest) { manifest.RouteOwners.Management = "" }, want: "routeOwners.management"},
		{name: "node owner", edit: func(manifest *workerOwnerManifest) { manifest.RouteOwners.Worker = "node" }, want: "routeOwners.worker=go"},
		{name: "Go version", edit: func(manifest *workerOwnerManifest) { manifest.Release.GoVersion = "other" }, want: "Go version"},
		{name: "database schema", edit: func(manifest *workerOwnerManifest) { manifest.Release.SchemaVersion-- }, want: "schema version"},
		{name: "missing rollback owners", edit: func(manifest *workerOwnerManifest) {
			manifest.RollbackRouteOwners.Management = ""
			manifest.RollbackRouteOwners.Public = ""
			manifest.RollbackRouteOwners.Gateway = ""
			manifest.RollbackRouteOwners.Worker = ""
		}, want: "rollbackRouteOwners"},
		{name: "missing route allowlist", edit: func(manifest *workerOwnerManifest) { manifest.RouteAllowlist = nil }, want: "routeAllowlist is required"},
		{name: "invalid route allowlist", edit: func(manifest *workerOwnerManifest) {
			manifest.RouteAllowlist = []workerOwnerRoute{{Surface: "management", Method: "GET", Path: "/__aisys__/api/test", Owner: "go", RollbackOwner: "go"}}
		}, want: "must differ"},
		{name: "dot path segment", edit: func(manifest *workerOwnerManifest) {
			manifest.RouteAllowlist = []workerOwnerRoute{validWorkerOwnerRoute("management", "/__aisys__/api/accounts/..")}
		}, want: "dot segments"},
		{name: "partial path template", edit: func(manifest *workerOwnerManifest) {
			manifest.RouteAllowlist = []workerOwnerRoute{validWorkerOwnerRoute("management", "/__aisys__/api/accounts/{id}.json")}
		}, want: "complete segment"},
		{name: "unsupported path character", edit: func(manifest *workerOwnerManifest) {
			manifest.RouteAllowlist = []workerOwnerRoute{validWorkerOwnerRoute("management", "/__aisys__/api/a b")}
		}, want: "unsupported path characters"},
		{name: "gateway parameter first segment", edit: func(manifest *workerOwnerManifest) {
			manifest.RouteAllowlist = []workerOwnerRoute{validWorkerOwnerRoute("gateway", "/{surface}/api/accounts")}
		}, want: "first segment must be literal"},
		{name: "duplicate path parameter", edit: func(manifest *workerOwnerManifest) {
			manifest.RouteAllowlist = []workerOwnerRoute{validWorkerOwnerRoute("management", "/__aisys__/api/{id}/accounts/{id}")}
		}, want: "unique parameter names"},
		{name: "overlapping path templates", edit: func(manifest *workerOwnerManifest) {
			manifest.RouteAllowlist = []workerOwnerRoute{
				validWorkerOwnerRoute("management", "/__aisys__/api/accounts/{id}"),
				validWorkerOwnerRoute("management", "/__aisys__/api/accounts/current"),
			}
		}, want: "overlaps"},
		{name: "oversized route allowlist", edit: func(manifest *workerOwnerManifest) { manifest.RouteAllowlist = make([]workerOwnerRoute, 2049) }, want: "at most 2048"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := enabledWorkerGateConfig(t)
			manifest := validWorkerOwnerManifest(cfg)
			test.edit(&manifest)
			err := runWorkerWithRuntimeGate(t.Context(), cfg, nil, func(context.Context) error {
				t.Fatal("runner called with mismatched manifest")
				return nil
			}, workerRuntimeGateDependencies{
				readManifest: func(path string) (workerOwnerManifest, error) {
					if path != cfg.OwnerManifestPath {
						t.Fatalf("manifest path = %q, want %q", path, cfg.OwnerManifestPath)
					}
					return manifest, nil
				},
				acquire: func(string, ownerlock.Metadata) (workerRuntimeLock, error) {
					t.Fatal("owner lock acquired with mismatched manifest")
					return nil, nil
				},
				openStore: func(context.Context, string) (workerRuntimeSchemaStore, error) {
					t.Fatal("PostgreSQL opened with mismatched manifest")
					return nil, nil
				},
			})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("runWorkerWithRuntimeGate() error = %v, want %q", err, test.want)
			}
		})
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
			if expected != version.SchemaVersion {
				t.Fatalf("schema version = %d, want %d", expected, version.SchemaVersion)
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
		readManifest: func(path string) (workerOwnerManifest, error) {
			if path != cfg.OwnerManifestPath {
				t.Fatalf("manifest path = %q, want %q", path, cfg.OwnerManifestPath)
			}
			calls = append(calls, "manifest")
			return validWorkerOwnerManifest(cfg), nil
		},
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
	want := []string{"manifest", "acquire", "open", "ping", "schema", "close", "runner", "release"}
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
	root := t.TempDir()
	cfg := config.Config{
		OwnerLockEnabled:         true,
		OwnerLockPath:            filepath.Join(root, "runtime", "worker.lock"),
		OwnerLockDeploymentEpoch: "epoch-test",
		OwnerLockRole:            " worker ",
		OwnerManifestPath:        filepath.Join(root, "owner-manifest.json"),
		GoWorkerExclusiveOwner:   true,
		LegacyNodeWorkerDrained:  true,
		PostgresURL:              "postgres://worker-gate-test",
	}
	manifest := validWorkerOwnerManifest(cfg)
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal owner manifest: %v", err)
	}
	if err := os.WriteFile(cfg.OwnerManifestPath, data, 0o600); err != nil {
		t.Fatalf("write owner manifest: %v", err)
	}
	return cfg
}

func validWorkerOwnerManifest(cfg config.Config) workerOwnerManifest {
	manifest := workerOwnerManifest{SchemaVersion: 2, DeploymentEpoch: cfg.OwnerLockDeploymentEpoch}
	manifest.Release.GoVersion = version.Version
	manifest.Release.NodeVersion = "node-test"
	manifest.Release.SchemaVersion = version.SchemaVersion
	manifest.RouteOwners.Management = "node"
	manifest.RouteOwners.Public = "node"
	manifest.RouteOwners.Gateway = "node"
	manifest.RouteOwners.Worker = "go"
	manifest.RollbackRouteOwners = manifest.RouteOwners
	manifest.RouteAllowlist = []workerOwnerRoute{}
	return manifest
}

func validWorkerOwnerRoute(surface, path string) workerOwnerRoute {
	return workerOwnerRoute{Surface: surface, Method: "GET", Path: path, Owner: "go", RollbackOwner: "node"}
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
