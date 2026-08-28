package modelcheckowner

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestOpenHostFailsClosedBeforeOpeningStoreWhenDependenciesMissing(t *testing.T) {
	_, err := OpenHost(context.Background(), Config{Enabled: true, StoreMode: "sqlite", DatabasePath: "unused.db", BusinessHandoffConfirmed: true, NodeWriterStopped: true, SchemaReady: true, HealthBoundaryReady: true, RuntimeReady: true, InstanceID: "gateway-1"}, HostDependencies{})
	if err == nil {
		t.Fatal("missing in-process owner dependencies must fail closed")
	}
}

func TestOpenHostRejectsConfirmedHandoffWhileNodeWriterIsActive(t *testing.T) {
	_, err := OpenHost(context.Background(), Config{
		Enabled: true, StoreMode: "sqlite", DatabasePath: "unused.db", BusinessHandoffConfirmed: true,
		SchemaReady: true, HealthBoundaryReady: true, RuntimeReady: true, InstanceID: "gateway-1",
	}, HostDependencies{})
	if err == nil || !strings.Contains(err.Error(), "Node writer") {
		t.Fatalf("confirmed handoff with active Node writer must fail closed, err=%v", err)
	}
}

func TestHostRunFailsClosedWithoutScheduler(t *testing.T) {
	host := &Host{}
	host.ready.Store(true)
	if err := host.Run(context.Background()); err == nil {
		t.Fatal("host without scheduler must fail closed")
	}
}

func TestOpenHostRequiresCompleteSchedulerDependencies(t *testing.T) {
	freezer := func(context.Context, RunRequest) (Target, error) {
		return Target{Endpoint: "https://example.invalid", Prompt: "OK"}, nil
	}
	_, err := OpenHost(context.Background(), Config{
		Enabled: true, StoreMode: "sqlite", DatabasePath: "unused.db",
		BusinessHandoffConfirmed: true, NodeWriterStopped: true, SchemaReady: true, HealthBoundaryReady: true,
		RuntimeReady: true, InstanceID: "gateway-1",
	}, HostDependencies{
		Resolve:   freezer,
		Authorize: func(context.Context, *http.Request) (string, error) { return "sys", nil },
		Build:     func(context.Context, string, RunCommand) (RunRequest, error) { return RunRequest{}, nil },
	})
	if err == nil {
		t.Fatal("host without durable scheduler must fail closed during construction")
	}
}

func TestHostComponentUsesSchedulerLifecycle(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	host := &Host{Scheduler: &Scheduler{
		Source:   &memorySchedulerSource{},
		Executor: schedulerExecutorFunc(func(context.Context, ScheduleTask) error { return nil }),
		Interval: time.Hour,
	}}
	host.ready.Store(true)
	component := host.Component()
	if component.Name == "" || component.Run == nil || component.Close == nil {
		t.Fatal("host component must expose name, run, and close")
	}
	cancel()
	if err := component.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("component run error = %v, want context canceled", err)
	}
}

func TestHostMountRejectsUnreadyAndPreservesStablePaths(t *testing.T) {
	var unready Host
	if err := unready.Mount(http.NewServeMux(), "/model-checks/"); err == nil {
		t.Fatal("unready host must not mount routes")
	}
	host := &Host{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/run" {
			t.Fatalf("stripped route path=%q", r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	})}
	host.ready.Store(true)
	mux := http.NewServeMux()
	if err := host.Mount(mux, "/model-checks/"); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/model-checks/run", nil))
	if response.Code != http.StatusNoContent {
		t.Fatalf("mounted route status=%d", response.Code)
	}
}

type schedulerExecutorFunc func(context.Context, ScheduleTask) error

func (f schedulerExecutorFunc) Execute(ctx context.Context, task ScheduleTask) error {
	return f(ctx, task)
}
