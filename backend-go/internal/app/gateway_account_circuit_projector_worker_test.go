package app

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/config"
)

func TestGatewayAccountCircuitProjectorWorkerIsDisabledByDefault(t *testing.T) {
	if err := RunGatewayAccountCircuitProjectorWorker(t.Context(), config.Config{}, nil, GatewayAccountCircuitProjectorWorkerOptions{}); err != nil {
		t.Fatalf("disabled worker error = %v", err)
	}
}

func TestGatewayAccountCircuitProjectorWorkerRequiresExclusiveOwnership(t *testing.T) {
	err := RunGatewayAccountCircuitProjectorWorker(t.Context(), config.Config{}, nil, GatewayAccountCircuitProjectorWorkerOptions{Enabled: true})
	if err == nil || !strings.Contains(err.Error(), "exclusive") {
		t.Fatalf("worker error = %v, want exclusive ownership error", err)
	}
}

func TestGatewayAccountCircuitProjectorWorkerRequiresRuntimeWriterDrain(t *testing.T) {
	opts := GatewayAccountCircuitProjectorWorkerOptions{Enabled: true, GoExclusiveProjectionOwner: true}
	err := RunGatewayAccountCircuitProjectorWorker(t.Context(), config.Config{}, nil, opts)
	if err == nil || !strings.Contains(err.Error(), "runtime-state") {
		t.Fatalf("worker error = %v, want runtime-state ownership error", err)
	}
	opts.GoExclusiveRuntimeStateOwner = true
	err = RunGatewayAccountCircuitProjectorWorker(t.Context(), config.Config{}, nil, opts)
	if err == nil || !strings.Contains(err.Error(), "drained") {
		t.Fatalf("worker error = %v, want legacy writer drain error", err)
	}
}

func TestGatewayAccountCircuitProjectorWorkerValidatesDependenciesBeforeOpeningThem(t *testing.T) {
	opts := GatewayAccountCircuitProjectorWorkerOptions{Enabled: true, GoExclusiveProjectionOwner: true, GoExclusiveRuntimeStateOwner: true, LegacyRuntimeWritersDrained: true}
	err := RunGatewayAccountCircuitProjectorWorker(t.Context(), config.Config{}, nil, opts)
	if err == nil || !strings.Contains(err.Error(), "POSTGRES") {
		t.Fatalf("worker error = %v, want PostgreSQL configuration error", err)
	}
	err = RunGatewayAccountCircuitProjectorWorker(t.Context(), config.Config{PostgresURL: "postgres://unused"}, nil, opts)
	if err == nil || !strings.Contains(err.Error(), "REDIS") {
		t.Fatalf("worker error = %v, want Redis configuration error", err)
	}
}

func TestGatewayAccountCircuitProjectorWorkerRejectsInvalidRuntimeOptionsBeforeOpeningDependencies(t *testing.T) {
	cfg := config.Config{PostgresURL: "://must-not-open", RedisStateURL: "://must-not-open"}
	base := GatewayAccountCircuitProjectorWorkerOptions{Enabled: true, GoExclusiveProjectionOwner: true, GoExclusiveRuntimeStateOwner: true, LegacyRuntimeWritersDrained: true}
	for _, test := range []struct {
		name string
		edit func(*GatewayAccountCircuitProjectorWorkerOptions)
		want string
	}{
		{name: "owner", edit: func(value *GatewayAccountCircuitProjectorWorkerOptions) { value.OwnerID = strings.Repeat("x", 129) }, want: "owner"},
		{name: "batch", edit: func(value *GatewayAccountCircuitProjectorWorkerOptions) { value.BatchSize = 501 }, want: "limit"},
		{name: "lease", edit: func(value *GatewayAccountCircuitProjectorWorkerOptions) { value.Lease = time.Hour + 1 }, want: "lease"},
		{name: "retry", edit: func(value *GatewayAccountCircuitProjectorWorkerOptions) { value.RetryDelay = 24*time.Hour + 1 }, want: "retry"},
		{name: "retention", edit: func(value *GatewayAccountCircuitProjectorWorkerOptions) { value.ClosedRetention = 24*time.Hour + 1 }, want: "retention"},
		{name: "capacity", edit: func(value *GatewayAccountCircuitProjectorWorkerOptions) { value.RuntimeCapacity = 1000001 }, want: "capacity"},
		{name: "rebuild page", edit: func(value *GatewayAccountCircuitProjectorWorkerOptions) { value.RebuildPageSize = 501 }, want: "page size"},
		{name: "rebuild pages", edit: func(value *GatewayAccountCircuitProjectorWorkerOptions) { value.RebuildMaxPages = 10001 }, want: "max pages"},
	} {
		t.Run(test.name, func(t *testing.T) {
			opts := base
			test.edit(&opts)
			err := RunGatewayAccountCircuitProjectorWorker(t.Context(), cfg, nil, opts)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("worker error = %v, want %q validation error", err, test.want)
			}
		})
	}
}

func TestGatewayAccountCircuitProjectorLoopRunsBatchesUntilCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	calls := 0
	err := runGatewayAccountCircuitProjectorLoop(ctx, slog.Default(), 0, time.Millisecond, false, func() error {
		calls++
		if calls == 2 {
			cancel()
		}
		return nil
	})
	if err != nil {
		t.Fatalf("loop error = %v", err)
	}
	if calls != 2 {
		t.Fatalf("batch calls = %d, want 2", calls)
	}
}

func TestGatewayAccountCircuitProjectorLoopRunOnceReturnsBatchError(t *testing.T) {
	want := context.DeadlineExceeded
	err := runGatewayAccountCircuitProjectorLoop(t.Context(), slog.Default(), 0, time.Second, true, func() error { return want })
	if err != want {
		t.Fatalf("run-once error = %v, want %v", err, want)
	}
}
