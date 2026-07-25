package gatewayrouteplan

import (
	"context"
	"reflect"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/modules/gatewaycandidatewindow"
	"juhe-ai/backend-go/internal/modules/gatewaypreflight"
	"juhe-ai/backend-go/internal/modules/gatewayroutecoordination"
	"juhe-ai/backend-go/internal/store/port"
)

func TestBuildOrdersGroupsWithSharedRoundRobinState(t *testing.T) {
	t.Parallel()
	store := newPreflightStore("round_robin", "active")
	store.bindings = []port.GatewayPreflightBindingRecord{
		{ID: "one", APIKeyID: "key", SystemAccountID: "system", GroupID: "group-one", Priority: 1, Weight: 1, Status: "active", GroupEnabled: true},
		{ID: "two", APIKeyID: "key", SystemAccountID: "system", GroupID: "group-two", Priority: 2, Weight: 1, Status: "active", GroupEnabled: true},
	}
	loader := &candidateLoader{}
	service := newRoutePlanService(t, store, loader)
	first, err := service.Build(context.Background(), Input{RawAPIKey: "sk-route-plan", RequestedModel: "gpt", EndpointFamily: "chat"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Build(context.Background(), Input{RawAPIKey: "sk-route-plan", RequestedModel: "gpt", EndpointFamily: "chat"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := groupIDs(first.Groups), []string{"group-one", "group-two"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("first groups = %v, want %v", got, want)
	}
	if got, want := groupIDs(second.Groups), []string{"group-two", "group-one"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("second groups = %v, want %v", got, want)
	}
	if first.Plan == nil || !first.Plan.StateAdvanced || loader.calls != 4 {
		t.Fatalf("plan=%#v calls=%d", first.Plan, loader.calls)
	}
	for _, call := range loader.inputs {
		if call.SystemAccountID != "system" || call.RequestedModel != "gpt" || call.EndpointFamily != "chat" {
			t.Fatalf("candidate input = %#v", call)
		}
	}
}

func TestBuildStopsBeforePlanningWhenPreflightDenied(t *testing.T) {
	t.Parallel()
	store := newPreflightStore("normal", "disabled")
	loader := &candidateLoader{}
	service := newRoutePlanService(t, store, loader)
	result, err := service.Build(context.Background(), Input{RawAPIKey: "sk-denied"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Plan != nil || len(result.Groups) != 0 || loader.calls != 0 || result.Preflight.Decision().Allowed() {
		t.Fatalf("denied result = %#v calls=%d", result, loader.calls)
	}
}

func TestBuildFailsClosedForUnsupportedModeAndCandidateError(t *testing.T) {
	t.Parallel()
	store := newPreflightStore("unknown", "active")
	service := newRoutePlanService(t, store, &candidateLoader{})
	if _, err := service.Build(context.Background(), Input{RawAPIKey: "sk-mode"}); err == nil {
		t.Fatal("Build() accepted unsupported mode")
	}
	store = newPreflightStore("hybrid_smart", "active")
	service = newRoutePlanService(t, store, &candidateLoader{})
	if _, err := service.Build(context.Background(), Input{RawAPIKey: "sk-hybrid"}); err == nil {
		t.Fatal("Build() accepted hybrid mode without a hybrid planner")
	}
	store = newPreflightStore("normal", "active")
	loader := &candidateLoader{err: context.DeadlineExceeded}
	service = newRoutePlanService(t, store, loader)
	if _, err := service.Build(context.Background(), Input{RawAPIKey: "sk-error"}); err == nil {
		t.Fatal("Build() accepted candidate load error")
	}
}

func newRoutePlanService(t *testing.T, store *preflightStore, loader *candidateLoader) *Service {
	t.Helper()
	preflight := gatewaypreflight.NewService(gatewaypreflight.ServiceOptions{Store: store, Now: func() time.Time { return time.Unix(0, 0) }})
	service, err := NewService(Options{Preflight: preflight, Coordinator: gatewayroutecoordination.NewMemoryStore(), Candidates: loader})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

type preflightStore struct {
	key      port.GatewayPreflightAPIKeyRecord
	bindings []port.GatewayPreflightBindingRecord
}

func newPreflightStore(mode, status string) *preflightStore {
	return &preflightStore{key: port.GatewayPreflightAPIKeyRecord{ID: "key", SystemAccountID: "system", APIKeyStatus: status, SystemAccountStatus: "active", RouteStrategyID: "route", RouteStrategyStatus: "active", RouteStrategyMode: mode}, bindings: []port.GatewayPreflightBindingRecord{{ID: "one", APIKeyID: "key", SystemAccountID: "system", GroupID: "group-one", Priority: 1, Weight: 1, Status: "active", GroupEnabled: true}}}
}
func (s *preflightStore) LoadGatewayPreflightAPIKey(context.Context, string) (port.GatewayPreflightAPIKeyRecord, bool, error) {
	return s.key, true, nil
}
func (s *preflightStore) ListGatewayPreflightBindings(context.Context, string, string, string, time.Time, int) ([]port.GatewayPreflightBindingRecord, error) {
	return append([]port.GatewayPreflightBindingRecord(nil), s.bindings...), nil
}
func (s *preflightStore) LoadGatewayPreflightSettings(context.Context) (port.GatewayPreflightSettingsRecord, error) {
	return port.GatewayPreflightSettingsRecord{}, nil
}

type candidateLoader struct {
	inputs []gatewaycandidatewindow.LoadInput
	calls  int
	err    error
}

func (l *candidateLoader) Load(_ context.Context, input gatewaycandidatewindow.LoadInput) (gatewaycandidatewindow.Window, bool, error) {
	l.calls++
	l.inputs = append(l.inputs, input)
	if l.err != nil {
		return gatewaycandidatewindow.Window{}, false, l.err
	}
	return gatewaycandidatewindow.Window{}, false, nil
}
func groupIDs(groups []GroupWindow) []string {
	ids := make([]string, 0, len(groups))
	for _, group := range groups {
		ids = append(ids, group.Binding.GroupID())
	}
	return ids
}
