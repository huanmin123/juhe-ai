package modelcheckowner

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckactive"
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

func TestHostMountScopedClonesOwnerHandlerAndForcesSelfScope(t *testing.T) {
	baseline := &fakeBaselineActivator{}
	owner := &HTTPHandler{Service: fakeRunService{}, Active: modelcheckactive.NewRegistry(), Baseline: baseline, Build: func(context.Context, string, RunCommand) (RunRequest, error) {
		return RunRequest{TargetID: "account", Model: "gpt-5.6", Profile: "quick"}, nil
	}}
	host := &Host{Handler: owner}
	host.ready.Store(true)
	mux := http.NewServeMux()
	self := func(context.Context, *http.Request) (string, error) { return "sys-self", nil }
	if err := host.MountScoped(mux, "/__aisys__/api/my-model-checks/", self, false); err != nil {
		t.Fatal(err)
	}
	selfHandle, selfActive, _ := owner.Active.TryStart(context.Background(), "system-account:sys-self", modelcheckactive.Summary{TargetID: "self-target"})
	if !selfActive {
		t.Fatal("seed self active run")
	}
	defer selfHandle.Finish()
	otherHandle, otherActive, _ := owner.Active.TryStart(context.Background(), "system-account:sys-other", modelcheckactive.Summary{TargetID: "other-target"})
	if !otherActive {
		t.Fatal("seed foreign active run")
	}
	defer otherHandle.Finish()
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/__aisys__/api/my-model-checks/run/active?systemAccountId=sys-other", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"targetId":"self-target"`) {
		t.Fatalf("self mount must force actor scope: status=%d body=%s", response.Code, response.Body.String())
	}
	activation := httptest.NewRecorder()
	mux.ServeHTTP(activation, httptest.NewRequest(http.MethodPost, "/__aisys__/api/my-model-checks/token-intercept-baselines/activate", strings.NewReader(`{"cohortKeyHmac":"hmac-sha256-v1:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","requestedModel":"gpt-5.6","tokenizerVersion":"o200k_base@1","probeSetVersion":"probe-v1","baselineVersion":2,"strongThresholdIntercept":128,"calibrationNote":"calibrated"}`)))
	if activation.Code != http.StatusForbidden || baseline.input.BaselineVersion != 0 {
		t.Fatalf("self mount must reject administrator-only baseline activation: status=%d body=%s input=%+v", activation.Code, activation.Body.String(), baseline.input)
	}
	if owner.AllowCrossAccount || owner.ForceActorScope {
		t.Fatal("scoped mount must not mutate host handler")
	}
}

type schedulerExecutorFunc func(context.Context, ScheduleTask) error

func (f schedulerExecutorFunc) Execute(ctx context.Context, task ScheduleTask) error {
	return f(ctx, task)
}
