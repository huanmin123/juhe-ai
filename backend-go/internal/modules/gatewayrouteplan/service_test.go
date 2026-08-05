package gatewayrouteplan

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/modules/gatewaycandidatewindow"
	"juhe-ai/backend-go/internal/modules/gatewayingress"
	"juhe-ai/backend-go/internal/modules/gatewaypreflight"
	"juhe-ai/backend-go/internal/modules/gatewayroutecoordination"
	protocolgateway "juhe-ai/backend-go/internal/protocols/gateway"
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
	if first.Plan == nil || !first.Plan.StateAdvanced || first.Plan.DispatchGeneration != 7 || loader.calls != 4 {
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

func TestBuildFromPreflightDoesNotResolveOrAdvanceTwice(t *testing.T) {
	t.Parallel()
	store := newPreflightStore("round_robin", "active")
	resolved, err := gatewaypreflight.NewService(gatewaypreflight.ServiceOptions{
		Store: store, Now: func() time.Time { return time.Unix(0, 0) },
	}).Resolve(context.Background(), "sk-preflight-once")
	if err != nil || !resolved.Decision().Allowed() {
		t.Fatalf("resolve = %#v, %v", resolved, err)
	}
	loader := &candidateLoader{}
	resolver := &preflightResolver{result: resolved}
	service, err := NewService(Options{Preflight: resolver, Coordinator: gatewayroutecoordination.NewMemoryStore(), Candidates: loader})
	if err != nil {
		t.Fatal(err)
	}

	result, err := service.BuildFromPreflight(context.Background(), PreparedInput{
		Preflight: resolved, RequestedModel: "gpt", EndpointFamily: "chat",
	})
	if err != nil || result.Plan == nil || !result.Plan.StateAdvanced || loader.calls != 1 {
		t.Fatalf("result=%#v err=%v calls=%d", result, err, loader.calls)
	}
	if resolver.calls != 0 {
		t.Fatalf("preflight resolver calls = %d, want 0", resolver.calls)
	}

	denied, err := gatewaypreflight.NewService(gatewaypreflight.ServiceOptions{}).Resolve(context.Background(), "invalid")
	if err != nil || denied.Decision().Allowed() {
		t.Fatalf("denied preflight = %#v, %v", denied, err)
	}
	result, err = service.BuildFromPreflight(context.Background(), PreparedInput{Preflight: denied})
	if err != nil || result.Plan != nil || loader.calls != 1 || resolver.calls != 0 {
		t.Fatalf("denied result=%#v err=%v calls=%d resolver=%d", result, err, loader.calls, resolver.calls)
	}
}

func TestPlanFromPreflightAdvancesRouteWithoutLoadingCandidates(t *testing.T) {
	t.Parallel()
	store := newPreflightStore("round_robin", "active")
	store.bindings = []port.GatewayPreflightBindingRecord{
		{ID: "one", APIKeyID: "key", SystemAccountID: "system", GroupID: "group-one", Priority: 1, Weight: 1, Status: "active", GroupEnabled: true},
		{ID: "two", APIKeyID: "key", SystemAccountID: "system", GroupID: "group-two", Priority: 2, Weight: 1, Status: "active", GroupEnabled: true},
	}
	resolved, err := gatewaypreflight.NewService(gatewaypreflight.ServiceOptions{
		Store: store, Now: func() time.Time { return time.Unix(0, 0) },
	}).Resolve(context.Background(), "sk-route-only")
	if err != nil || !resolved.Decision().Allowed() {
		t.Fatalf("resolve = %#v, %v", resolved, err)
	}
	loader := &candidateLoader{}
	resolver := &preflightResolver{result: resolved}
	service, err := NewService(Options{Preflight: resolver, Coordinator: gatewayroutecoordination.NewMemoryStore(), Candidates: loader})
	if err != nil {
		t.Fatal(err)
	}

	first, err := service.PlanFromPreflight(context.Background(), resolved)
	if err != nil || first.Plan == nil || !first.Plan.StateAdvanced || loader.calls != 0 || resolver.calls != 0 {
		t.Fatalf("first=%#v err=%v loader=%d resolver=%d", first, err, loader.calls, resolver.calls)
	}
	if got, want := bindingGroupIDs(first.OrderedBindings), []string{"group-one", "group-two"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("first groups = %v, want %v", got, want)
	}

	second, err := service.PlanFromPreflight(context.Background(), resolved)
	if err != nil || second.Plan == nil || loader.calls != 0 || resolver.calls != 0 {
		t.Fatalf("second=%#v err=%v loader=%d resolver=%d", second, err, loader.calls, resolver.calls)
	}
	if got, want := bindingGroupIDs(second.OrderedBindings), []string{"group-two", "group-one"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("second groups = %v, want %v", got, want)
	}
}

func TestBuildFromRouteLoadsCandidatesWithoutAdvancingRouteAgain(t *testing.T) {
	t.Parallel()
	store := newPreflightStore("round_robin", "active")
	store.bindings = []port.GatewayPreflightBindingRecord{
		{ID: "one", APIKeyID: "key", SystemAccountID: "system", GroupID: "group-one", Priority: 1, Weight: 1, Status: "active", GroupEnabled: true},
		{ID: "two", APIKeyID: "key", SystemAccountID: "system", GroupID: "group-two", Priority: 2, Weight: 1, Status: "active", GroupEnabled: true},
	}
	resolved, err := gatewaypreflight.NewService(gatewaypreflight.ServiceOptions{Store: store, Now: func() time.Time { return time.Unix(0, 0) }}).Resolve(context.Background(), "sk-route-continuation")
	if err != nil || !resolved.Decision().Allowed() {
		t.Fatalf("resolve=%#v err=%v", resolved, err)
	}
	loader := &candidateLoader{}
	service := newRoutePlanService(t, store, loader)
	route, err := service.PlanFromPreflight(context.Background(), resolved)
	if err != nil || loader.calls != 0 {
		t.Fatalf("route=%#v err=%v loader=%d", route, err, loader.calls)
	}
	result, err := service.BuildFromRoute(context.Background(), RoutePreparedInput{Route: route, RequestedModel: "gpt", EndpointFamily: "responses"})
	if err != nil || loader.calls != 2 || !reflect.DeepEqual(groupIDs(result.Groups), bindingGroupIDs(route.OrderedBindings)) {
		t.Fatalf("result=%#v err=%v loader=%d", result, err, loader.calls)
	}
	next, err := service.PlanFromPreflight(context.Background(), resolved)
	if err != nil || !reflect.DeepEqual(bindingGroupIDs(next.OrderedBindings), []string{"group-two", "group-one"}) {
		t.Fatalf("next=%#v err=%v", next, err)
	}
}

func TestFallbackCursorSelectsOnlyLaterUnenteredGroups(t *testing.T) {
	t.Parallel()
	store := newPreflightStore("normal", "active")
	store.bindings = []port.GatewayPreflightBindingRecord{
		{ID: "one", APIKeyID: "key", SystemAccountID: "system", GroupID: "group-one", Priority: 1, Weight: 1, Status: "active", GroupEnabled: true},
		{ID: "two", APIKeyID: "key", SystemAccountID: "system", GroupID: "group-two", Priority: 2, Weight: 1, Status: "active", GroupEnabled: true},
		{ID: "two-alias", APIKeyID: "key", SystemAccountID: "system", GroupID: "group-two", Priority: 3, Weight: 1, Status: "active", GroupEnabled: true},
		{ID: "three", APIKeyID: "key", SystemAccountID: "system", GroupID: "group-three", Priority: 4, Weight: 1, Status: "active", GroupEnabled: true},
	}
	resolved, err := gatewaypreflight.NewService(gatewaypreflight.ServiceOptions{Store: store, Now: func() time.Time { return time.Unix(0, 0) }}).Resolve(context.Background(), "sk-fallback")
	if err != nil || !resolved.Decision().Allowed() {
		t.Fatalf("resolve=%#v err=%v", resolved, err)
	}
	route, err := newRoutePlanService(t, store, &candidateLoader{}).PlanFromPreflight(context.Background(), resolved)
	if err != nil {
		t.Fatal(err)
	}
	current, err := InitialFallbackCursor(route, "one")
	if err != nil {
		t.Fatal(err)
	}
	target, found, err := NextFallbackTarget(route, current, []string{"group-one"})
	if err != nil || !found || target.Binding().ID() != "two" || target.Binding().GroupID() != "group-two" || ValidateFallbackTarget(route, target) != nil {
		t.Fatalf("target=%#v found=%v err=%v", target, found, err)
	}
	target, found, err = NextFallbackTarget(route, target.Cursor(), []string{"group-one", "group-two"})
	if err != nil || !found || target.Binding().ID() != "three" || target.Binding().GroupID() != "group-three" {
		t.Fatalf("target=%#v found=%v err=%v", target, found, err)
	}
	if _, found, err := NextFallbackTarget(route, target.Cursor(), []string{"group-one", "group-two", "group-three"}); err != nil || found {
		t.Fatalf("fallback wrapped after final group: found=%v err=%v", found, err)
	}
}

func TestFallbackCursorFailsClosedForUnenteredCurrentAndStaleTarget(t *testing.T) {
	t.Parallel()
	store := newPreflightStore("normal", "active")
	store.bindings = []port.GatewayPreflightBindingRecord{
		{ID: "one", APIKeyID: "key", SystemAccountID: "system", GroupID: "group-one", Priority: 1, Weight: 1, Status: "active", GroupEnabled: true},
		{ID: "two", APIKeyID: "key", SystemAccountID: "system", GroupID: "group-two", Priority: 2, Weight: 1, Status: "active", GroupEnabled: true},
	}
	resolved, err := gatewaypreflight.NewService(gatewaypreflight.ServiceOptions{Store: store, Now: func() time.Time { return time.Unix(0, 0) }}).Resolve(context.Background(), "sk-fallback-fail-closed")
	if err != nil || !resolved.Decision().Allowed() {
		t.Fatalf("resolve=%#v err=%v", resolved, err)
	}
	route, err := newRoutePlanService(t, store, &candidateLoader{}).PlanFromPreflight(context.Background(), resolved)
	if err != nil {
		t.Fatal(err)
	}
	current, err := InitialFallbackCursor(route, "one")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := NextFallbackTarget(route, current, []string{"group-two"}); err == nil {
		t.Fatal("NextFallbackTarget accepted omitted current group")
	}
	if _, _, err := NextFallbackTarget(route, current, []string{"group-one", "group-one"}); err == nil {
		t.Fatal("NextFallbackTarget accepted duplicated entered group")
	}
	if _, _, err := NextFallbackTarget(route, current, []string{"group-one", "group-two"}); err == nil {
		t.Fatal("NextFallbackTarget accepted future entered group")
	}
	if _, _, err := NextFallbackTarget(route, current, []string{"group-one", "unknown-group"}); err == nil {
		t.Fatal("NextFallbackTarget accepted unknown entered group")
	}
	if _, _, err := NextFallbackTarget(route, current, []string{"group-one", "group-two", "group-three"}); err == nil {
		t.Fatal("NextFallbackTarget accepted more entered groups than route bindings")
	}
	target, found, err := NextFallbackTarget(route, current, []string{"group-one"})
	if err != nil || !found {
		t.Fatalf("target=%#v found=%v err=%v", target, found, err)
	}
	stale := route
	stale.Plan = &gatewayroutecoordination.Plan{Scope: route.Plan.Scope, DispatchGeneration: route.Plan.DispatchGeneration + 1, Revision: route.Plan.Revision, Mode: route.Plan.Mode, Ordered: route.Plan.Ordered}
	if err := ValidateFallbackTarget(stale, target); err == nil {
		t.Fatal("ValidateFallbackTarget accepted stale dispatch generation")
	}
	staleRevision := route
	stalePlan := *route.Plan
	stalePlan.Revision = "stale-revision"
	staleRevision.Plan = &stalePlan
	if err := ValidateFallbackTarget(staleRevision, target); err == nil {
		t.Fatal("ValidateFallbackTarget accepted stale revision")
	}
	tamperedCursor := target
	tamperedCursor.cursor.position = 0
	tamperedCursor.cursor.bindingID = "one"
	tamperedCursor.cursor.groupID = "group-one"
	if err := ValidateFallbackTarget(route, tamperedCursor); err == nil {
		t.Fatal("ValidateFallbackTarget accepted tampered cursor position")
	}
	foreignStore := newPreflightStore("normal", "active")
	foreignStore.bindings = []port.GatewayPreflightBindingRecord{
		{ID: "one", APIKeyID: "key", SystemAccountID: "system", GroupID: "group-one", Priority: 1, Weight: 1, Status: "active", GroupEnabled: true},
		{ID: "two", APIKeyID: "key", SystemAccountID: "system", GroupID: "group-two", Priority: 99, Weight: 1, Status: "active", GroupEnabled: true},
	}
	foreign, err := gatewaypreflight.NewService(gatewaypreflight.ServiceOptions{Store: foreignStore, Now: func() time.Time { return time.Unix(0, 0) }}).Resolve(context.Background(), "sk-fallback-foreign")
	if err != nil || !foreign.Decision().Allowed() {
		t.Fatalf("foreign resolve=%#v err=%v", foreign, err)
	}
	spliced := target
	spliced.binding = foreign.Bindings()[1]
	if err := ValidateFallbackTarget(route, spliced); err == nil {
		t.Fatal("ValidateFallbackTarget accepted binding from another preflight")
	}
	tamperedRoute := route
	tamperedRoute.OrderedBindings = append([]gatewaypreflight.Binding(nil), route.OrderedBindings...)
	tamperedRoute.OrderedBindings[1] = foreign.Bindings()[1]
	if _, err := InitialFallbackCursor(tamperedRoute, "one"); err == nil {
		t.Fatal("InitialFallbackCursor accepted route binding from another preflight")
	}
	forgedRevision := route
	forgedRevision.Preflight = foreign
	forgedRevision.OrderedBindings = append([]gatewaypreflight.Binding(nil), route.OrderedBindings...)
	forgedRevision.OrderedBindings[1] = foreign.Bindings()[1]
	forgedPlan := *route.Plan
	forgedPlan.Revision = route.Plan.Revision
	forgedRevision.Plan = &forgedPlan
	if _, err := NewRouteFence(forgedRevision); err == nil {
		t.Fatal("NewRouteFence accepted changed binding semantics behind a forged revision")
	}
}

func TestPrepareFallbackTargetRefreshesFirstLaterUsableGroup(t *testing.T) {
	t.Parallel()
	store := newPreflightStore("normal", "active")
	store.bindings = []port.GatewayPreflightBindingRecord{
		{ID: "one", APIKeyID: "key", SystemAccountID: "system", GroupID: "group-one", Priority: 1, Weight: 1, Status: "active", GroupEnabled: true},
		{ID: "two", APIKeyID: "key", SystemAccountID: "system", GroupID: "group-two", Priority: 2, Weight: 1, Status: "active", GroupEnabled: true},
		{ID: "two-alias", APIKeyID: "key", SystemAccountID: "system", GroupID: "group-two", Priority: 3, Weight: 1, Status: "active", GroupEnabled: true},
		{ID: "three", APIKeyID: "key", SystemAccountID: "system", GroupID: "group-three", Priority: 4, Weight: 1, Status: "active", GroupEnabled: true},
	}
	resolved, err := gatewaypreflight.NewService(gatewaypreflight.ServiceOptions{Store: store, Now: func() time.Time { return time.Unix(0, 0) }}).Resolve(context.Background(), "sk-fallback-prepare")
	if err != nil || !resolved.Decision().Allowed() {
		t.Fatalf("resolve=%#v err=%v", resolved, err)
	}
	loader := &candidateLoader{results: map[string]candidateLoadResult{
		"group-two":   {found: false},
		"group-three": {found: true, window: fallbackWindow("group-three", "system")},
	}}
	service := newRoutePlanService(t, store, loader)
	route, err := service.PlanFromPreflight(context.Background(), resolved)
	if err != nil {
		t.Fatal(err)
	}
	current, err := InitialFallbackCursor(route, "one")
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := service.PrepareFallbackTarget(context.Background(), FallbackPreparedInput{
		Route: route, Current: current, EnteredGroupIDs: []string{"group-one"}, RequestedModel: "gpt", EndpointFamily: "responses",
	})
	target, window, found, validateErr := ValidateFallbackPreparedTarget(route, current, prepared)
	if err != nil || validateErr != nil || !found || target.Binding().ID() != "three" || len(window.Candidates) == 0 ||
		!reflect.DeepEqual(prepared.SkippedGroupIDs(), []string{"group-two"}) ||
		!reflect.DeepEqual(loadGroupIDs(loader.inputs), []string{"group-two", "group-three"}) {
		t.Fatalf("prepared=%#v err=%v inputs=%#v", prepared, err, loader.inputs)
	}
}

func TestPrepareFallbackTargetFailsClosedForNoTargetAndScopeMismatch(t *testing.T) {
	t.Parallel()
	store := newPreflightStore("normal", "active")
	store.bindings = []port.GatewayPreflightBindingRecord{
		{ID: "one", APIKeyID: "key", SystemAccountID: "system", GroupID: "group-one", Priority: 1, Weight: 1, Status: "active", GroupEnabled: true},
		{ID: "two", APIKeyID: "key", SystemAccountID: "system", GroupID: "group-two", Priority: 2, Weight: 1, Status: "active", GroupEnabled: true},
	}
	resolved, err := gatewaypreflight.NewService(gatewaypreflight.ServiceOptions{Store: store, Now: func() time.Time { return time.Unix(0, 0) }}).Resolve(context.Background(), "sk-fallback-prepare-fail")
	if err != nil || !resolved.Decision().Allowed() {
		t.Fatalf("resolve=%#v err=%v", resolved, err)
	}
	loader := &candidateLoader{results: map[string]candidateLoadResult{"group-two": {found: false}}}
	service := newRoutePlanService(t, store, loader)
	route, err := service.PlanFromPreflight(context.Background(), resolved)
	if err != nil {
		t.Fatal(err)
	}
	current, err := InitialFallbackCursor(route, "one")
	if err != nil {
		t.Fatal(err)
	}
	input := FallbackPreparedInput{Route: route, Current: current, EnteredGroupIDs: []string{"group-one"}, RequestedModel: "gpt", EndpointFamily: "responses"}
	prepared, err := service.PrepareFallbackTarget(context.Background(), input)
	if err != nil || prepared.Found() || !reflect.DeepEqual(prepared.SkippedGroupIDs(), []string{"group-two"}) || loader.calls != 1 {
		t.Fatalf("prepared=%#v err=%v calls=%d", prepared, err, loader.calls)
	}
	loader.results["group-two"] = candidateLoadResult{found: true, window: fallbackWindow("wrong-group", "system")}
	if _, err := service.PrepareFallbackTarget(context.Background(), input); err == nil {
		t.Fatal("PrepareFallbackTarget accepted mismatched candidate window scope")
	}
	loader.err = context.DeadlineExceeded
	if _, err := service.PrepareFallbackTarget(context.Background(), input); err == nil {
		t.Fatal("PrepareFallbackTarget accepted candidate load error")
	}
}

func TestPrepareDispatchFallbackTargetSkipsPolicyIneligibleFreshGroup(t *testing.T) {
	t.Parallel()
	store := newPreflightStore("normal", "active")
	store.bindings = []port.GatewayPreflightBindingRecord{
		{ID: "one", APIKeyID: "key", SystemAccountID: "system", GroupID: "group-one", Priority: 1, Weight: 1, Status: "active", GroupEnabled: true},
		{ID: "two", APIKeyID: "key", SystemAccountID: "system", GroupID: "group-two", Priority: 2, Weight: 1, Status: "active", GroupEnabled: true},
		{ID: "three", APIKeyID: "key", SystemAccountID: "system", GroupID: "group-three", Priority: 3, Weight: 1, Status: "active", GroupEnabled: true},
	}
	resolved, err := gatewaypreflight.NewService(gatewaypreflight.ServiceOptions{Store: store, Now: func() time.Time { return time.Unix(0, 0) }}).Resolve(context.Background(), "sk-fallback-dispatch-prepare")
	if err != nil || !resolved.Decision().Allowed() {
		t.Fatalf("resolve=%#v err=%v", resolved, err)
	}
	loader := &candidateLoader{results: map[string]candidateLoadResult{
		"group-two":   {found: true, window: fallbackWindow("group-two", "system")},
		"group-three": {found: true, window: fallbackWindow("group-three", "system")},
	}}
	service := newRoutePlanService(t, store, loader)
	route, err := service.PlanFromPreflight(context.Background(), resolved)
	if err != nil {
		t.Fatal(err)
	}
	current, err := InitialFallbackCursor(route, "one")
	if err != nil {
		t.Fatal(err)
	}
	policy := &fallbackPolicyStub{selectedByBindingID: map[string][]string{"two": {}, "three": {"account"}}}
	intent := fallbackDispatchIntent(t)
	prepared, err := service.PrepareDispatchFallbackTarget(context.Background(), FallbackDispatchPreparedInput{
		FallbackPreparedInput: FallbackPreparedInput{Route: route, Current: current, EnteredGroupIDs: []string{"group-one"}, RequestedModel: "gpt", EndpointFamily: "responses"},
		Intent:                intent, IngressFinalization: fallbackDispatchFinalization(t),
		RequestShape: protocolgateway.RequestShape{Path: "/v1/responses", Model: "gpt"}, Protocol: protocolgateway.ProtocolOpenAI, FinalLane: gatewayingress.LaneText,
		Reason: "runtime_degraded", RequestClientCompatibility: "openai", RequestLane: "text", Policy: policy,
	})
	target, window, found, validateErr := ValidateFallbackDispatchPreparedTarget(route, FallbackDispatchPreparedInput{
		FallbackPreparedInput: FallbackPreparedInput{Route: route, Current: current, EnteredGroupIDs: []string{"group-one"}, RequestedModel: "gpt", EndpointFamily: "responses"},
		Intent:                intent, IngressFinalization: fallbackDispatchFinalization(t),
		RequestShape: protocolgateway.RequestShape{Path: "/v1/responses", Model: "gpt"}, Protocol: protocolgateway.ProtocolOpenAI, FinalLane: gatewayingress.LaneText,
		Reason: "runtime_degraded", RequestClientCompatibility: "openai", RequestLane: "text", Policy: policy,
	}, prepared)
	if err != nil || validateErr != nil || !found || target.Binding().ID() != "three" || len(window.Candidates) != 1 ||
		!reflect.DeepEqual(prepared.SkippedGroupIDs(), []string{"group-two"}) || !reflect.DeepEqual(loadGroupIDs(loader.inputs), []string{"group-two", "group-three"}) {
		t.Fatalf("prepared=%#v err=%v validate=%v inputs=%#v", prepared, err, validateErr, loader.inputs)
	}
	if !reflect.DeepEqual(policy.seenBindingIDs, []string{"two", "three"}) {
		t.Fatalf("policy bindings=%#v", policy.seenBindingIDs)
	}
	wrongSnapshot := FallbackDispatchPreparedInput{
		FallbackPreparedInput: FallbackPreparedInput{Route: route, Current: current, EnteredGroupIDs: []string{"group-one"}, RequestedModel: "gpt", EndpointFamily: "responses"},
		Intent:                intent, IngressFinalization: fallbackDispatchFinalizationWithRevision(t, "different-snapshot"),
		RequestShape: protocolgateway.RequestShape{Path: "/v1/responses", Model: "gpt"}, Protocol: protocolgateway.ProtocolOpenAI, FinalLane: gatewayingress.LaneText,
		Reason: "runtime_degraded", RequestClientCompatibility: "openai", RequestLane: "text", Policy: policy,
	}
	if _, _, _, err := ValidateFallbackDispatchPreparedTarget(route, wrongSnapshot, prepared); err == nil {
		t.Fatal("prepared target accepted a different ingress snapshot revision")
	}
}

func TestPrepareDispatchFallbackTargetRejectsMissingOrForgedPolicy(t *testing.T) {
	t.Parallel()
	store := newPreflightStore("normal", "active")
	store.bindings = append(store.bindings, port.GatewayPreflightBindingRecord{ID: "two", APIKeyID: "key", SystemAccountID: "system", GroupID: "group-two", Priority: 2, Weight: 1, Status: "active", GroupEnabled: true})
	resolved, err := gatewaypreflight.NewService(gatewaypreflight.ServiceOptions{Store: store, Now: func() time.Time { return time.Unix(0, 0) }}).Resolve(context.Background(), "sk-fallback-dispatch-prepare-fail")
	if err != nil || !resolved.Decision().Allowed() {
		t.Fatalf("resolve=%#v err=%v", resolved, err)
	}
	loader := &candidateLoader{results: map[string]candidateLoadResult{"group-two": {found: true, window: fallbackWindow("group-two", "system")}}}
	service := newRoutePlanService(t, store, loader)
	route, err := service.PlanFromPreflight(context.Background(), resolved)
	if err != nil {
		t.Fatal(err)
	}
	current, err := InitialFallbackCursor(route, "one")
	if err != nil {
		t.Fatal(err)
	}
	input := FallbackDispatchPreparedInput{
		FallbackPreparedInput: FallbackPreparedInput{Route: route, Current: current, EnteredGroupIDs: []string{"group-one"}, RequestedModel: "gpt", EndpointFamily: "responses"},
		Intent:                fallbackDispatchIntent(t), IngressFinalization: fallbackDispatchFinalization(t),
		RequestShape: protocolgateway.RequestShape{Path: "/v1/responses", Model: "gpt"}, Protocol: protocolgateway.ProtocolOpenAI, FinalLane: gatewayingress.LaneText,
		Reason: "runtime_degraded", RequestClientCompatibility: "openai", RequestLane: "text",
	}
	if _, err := service.PrepareDispatchFallbackTarget(context.Background(), input); err == nil || loader.calls != 0 {
		t.Fatalf("missing policy err=%v calls=%d", err, loader.calls)
	}
	input.Policy = &fallbackPolicyStub{selectedByBindingID: map[string][]string{"two": {"foreign-account"}}}
	if _, err := service.PrepareDispatchFallbackTarget(context.Background(), input); err == nil || !strings.Contains(err.Error(), "outside the fresh window") {
		t.Fatalf("forged policy err=%v", err)
	}
	input.Reason = "unknown_node_reason"
	if _, err := service.PrepareDispatchFallbackTarget(context.Background(), input); err == nil || !strings.Contains(err.Error(), "request facts") {
		t.Fatalf("unknown reason err=%v", err)
	}
}

func TestPrepareDispatchFallbackTargetPolicyCannotMutateHydratedWindow(t *testing.T) {
	t.Parallel()
	store := newPreflightStore("normal", "active")
	store.bindings = append(store.bindings, port.GatewayPreflightBindingRecord{ID: "two", APIKeyID: "key", SystemAccountID: "system", GroupID: "group-two", Priority: 2, Weight: 1, Status: "active", GroupEnabled: true})
	resolved, err := gatewaypreflight.NewService(gatewaypreflight.ServiceOptions{Store: store, Now: func() time.Time { return time.Unix(0, 0) }}).Resolve(context.Background(), "sk-fallback-policy-isolation")
	if err != nil || !resolved.Decision().Allowed() {
		t.Fatalf("resolve=%#v err=%v", resolved, err)
	}
	window := fallbackWindow("group-two", "system")
	window.Candidates[0].Projection.Name = "original"
	window.Candidates[0].SupportedModels = []string{"gpt"}
	loader := &candidateLoader{results: map[string]candidateLoadResult{"group-two": {found: true, window: window}}}
	service := newRoutePlanService(t, store, loader)
	route, err := service.PlanFromPreflight(context.Background(), resolved)
	if err != nil {
		t.Fatal(err)
	}
	current, err := InitialFallbackCursor(route, "one")
	if err != nil {
		t.Fatal(err)
	}
	input := FallbackDispatchPreparedInput{
		FallbackPreparedInput: FallbackPreparedInput{Route: route, Current: current, EnteredGroupIDs: []string{"group-one"}, RequestedModel: "gpt", EndpointFamily: "responses"},
		Intent:                fallbackDispatchIntent(t), IngressFinalization: fallbackDispatchFinalization(t), RequestShape: protocolgateway.RequestShape{Path: "/v1/responses", Model: "gpt"}, Protocol: protocolgateway.ProtocolOpenAI,
		FinalLane: gatewayingress.LaneText, Reason: "runtime_degraded", RequestClientCompatibility: "openai", RequestLane: "text", Policy: mutatingFallbackPolicy{},
	}
	prepared, err := service.PrepareDispatchFallbackTarget(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	_, selected, found, err := ValidateFallbackDispatchPreparedTarget(route, input, prepared)
	if err != nil || !found || selected.Candidates[0].Projection.Name != "original" || selected.Candidates[0].SupportedModels[0] != "gpt" {
		t.Fatalf("policy mutated hydrated target found=%v err=%v window=%#v", found, err, selected)
	}
}

func TestBuildFromRouteFailsClosedForMismatchedRouteOnlyResult(t *testing.T) {
	t.Parallel()
	store := newPreflightStore("normal", "active")
	resolved, err := gatewaypreflight.NewService(gatewaypreflight.ServiceOptions{Store: store, Now: func() time.Time { return time.Unix(0, 0) }}).Resolve(context.Background(), "sk-route-mismatch")
	if err != nil || !resolved.Decision().Allowed() {
		t.Fatalf("resolve=%#v err=%v", resolved, err)
	}
	loader := &candidateLoader{}
	service := newRoutePlanService(t, store, loader)
	if _, err := service.BuildFromRoute(context.Background(), RoutePreparedInput{Route: RouteOnlyResult{Preflight: resolved, Plan: &gatewayroutecoordination.Plan{}}, RequestedModel: "gpt", EndpointFamily: "responses"}); err == nil || loader.calls != 0 {
		t.Fatalf("err=%v loader=%d", err, loader.calls)
	}
}

func TestBuildResolvesPreflightExactlyOnceBeforeDelegating(t *testing.T) {
	t.Parallel()
	store := newPreflightStore("normal", "active")
	resolved, err := gatewaypreflight.NewService(gatewaypreflight.ServiceOptions{
		Store: store, Now: func() time.Time { return time.Unix(0, 0) },
	}).Resolve(context.Background(), "sk-build-once")
	if err != nil || !resolved.Decision().Allowed() {
		t.Fatalf("resolve = %#v, %v", resolved, err)
	}
	resolver := &preflightResolver{result: resolved}
	loader := &candidateLoader{}
	service, err := NewService(Options{Preflight: resolver, Coordinator: gatewayroutecoordination.NewMemoryStore(), Candidates: loader})
	if err != nil {
		t.Fatal(err)
	}

	result, err := service.Build(context.Background(), Input{RawAPIKey: "sk-build-once", RequestedModel: "gpt", EndpointFamily: "chat"})
	if err != nil || result.Plan == nil || loader.calls != 1 || resolver.calls != 1 {
		t.Fatalf("result=%#v err=%v loader=%d resolver=%d", result, err, loader.calls, resolver.calls)
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
	return &preflightStore{key: port.GatewayPreflightAPIKeyRecord{ID: "key", SystemAccountID: "system", APIKeyStatus: status, SystemAccountStatus: "active", RouteStrategyID: "route", RouteStrategyStatus: "active", RouteStrategyMode: mode, RouteDispatchGeneration: 7}, bindings: []port.GatewayPreflightBindingRecord{{ID: "one", APIKeyID: "key", SystemAccountID: "system", GroupID: "group-one", Priority: 1, Weight: 1, Status: "active", GroupEnabled: true}}}
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
	inputs  []gatewaycandidatewindow.LoadInput
	calls   int
	err     error
	results map[string]candidateLoadResult
}

type fallbackPolicyStub struct {
	selectedByBindingID map[string][]string
	seenBindingIDs      []string
}

func (s *fallbackPolicyStub) SelectFallbackCandidates(_ context.Context, input FallbackCandidatePolicyInput) (FallbackCandidatePolicyResult, error) {
	bindingID := input.Target.Binding().ID()
	s.seenBindingIDs = append(s.seenBindingIDs, bindingID)
	return FallbackCandidatePolicyResult{CandidateAccountIDs: append([]string(nil), s.selectedByBindingID[bindingID]...)}, nil
}

type candidateLoadResult struct {
	window gatewaycandidatewindow.Window
	found  bool
}

func (l *candidateLoader) Load(_ context.Context, input gatewaycandidatewindow.LoadInput) (gatewaycandidatewindow.Window, bool, error) {
	l.calls++
	l.inputs = append(l.inputs, input)
	if l.err != nil {
		return gatewaycandidatewindow.Window{}, false, l.err
	}
	if result, ok := l.results[input.GroupID]; ok {
		return result.window, result.found, nil
	}
	return gatewaycandidatewindow.Window{}, false, nil
}

func fallbackWindow(groupID, systemAccountID string) gatewaycandidatewindow.Window {
	return gatewaycandidatewindow.Window{
		Access:     port.GatewayGroupAccess{GroupID: groupID, CallerSystemAccountID: systemAccountID, GroupType: "personal"},
		Candidates: []gatewaycandidatewindow.Candidate{{Projection: port.GatewayAccountCandidate{AccountID: "account"}}},
	}
}

func fallbackDispatchIntent(t *testing.T) gatewayingress.RequestIntent {
	t.Helper()
	intent, err := gatewayingress.Parse(gatewayingress.ParseInput{RawBody: []byte(`{"model":"gpt","stream":false}`)})
	if err != nil {
		t.Fatal(err)
	}
	return intent
}

func fallbackDispatchFinalization(t *testing.T) gatewayingress.FinalResult {
	return fallbackDispatchFinalizationWithRevision(t, "fallback-dispatch")
}

func fallbackDispatchFinalizationWithRevision(t *testing.T, revision string) gatewayingress.FinalResult {
	t.Helper()
	snapshot, err := gatewayingress.NewSnapshot(gatewayingress.SnapshotInput{
		Revision: revision, Model: "gpt", CandidateCapacity: 1,
		ToolCatalog: map[string]struct{}{}, ToolCatalogComplete: true, MappingLane: gatewayingress.LaneText,
	})
	if err != nil {
		t.Fatal(err)
	}
	finalization, err := gatewayingress.Finalize(fallbackDispatchIntent(t), snapshot, true)
	if err != nil {
		t.Fatal(err)
	}
	return finalization
}

type mutatingFallbackPolicy struct{}

func (mutatingFallbackPolicy) SelectFallbackCandidates(_ context.Context, input FallbackCandidatePolicyInput) (FallbackCandidatePolicyResult, error) {
	input.Window.Candidates[0].Projection.Name = "mutated"
	input.Window.Candidates[0].SupportedModels[0] = "mutated"
	return FallbackCandidatePolicyResult{CandidateAccountIDs: []string{input.Window.Candidates[0].Projection.AccountID}}, nil
}

func loadGroupIDs(inputs []gatewaycandidatewindow.LoadInput) []string {
	result := make([]string, len(inputs))
	for index, input := range inputs {
		result[index] = input.GroupID
	}
	return result
}

type preflightResolver struct {
	result gatewaypreflight.Result
	err    error
	calls  int
}

func (r *preflightResolver) Resolve(context.Context, string) (gatewaypreflight.Result, error) {
	r.calls++
	return r.result, r.err
}

func groupIDs(groups []GroupWindow) []string {
	ids := make([]string, 0, len(groups))
	for _, group := range groups {
		ids = append(ids, group.Binding.GroupID())
	}
	return ids
}

func bindingGroupIDs(bindings []gatewaypreflight.Binding) []string {
	ids := make([]string, 0, len(bindings))
	for _, binding := range bindings {
		ids = append(ids, binding.GroupID())
	}
	return ids
}
