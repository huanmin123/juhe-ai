package gatewayfallbackruntimedegradation

import (
	"sync"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/modules/gatewaycandidatewindow"
	"juhe-ai/backend-go/internal/modules/gatewayfallbackpolicy"
	"juhe-ai/backend-go/internal/store/port"
)

func TestServiceOrdersOnlyActiveLocalDegradations(t *testing.T) {
	now := time.Date(2026, 8, 4, 10, 0, 0, 0, time.UTC)
	service := newService(t, RuntimeStateDriverMemory, &now)
	window := testWindow()
	degraded := testCandidate("degraded")
	normal := testCandidate("normal")

	if got, err := service.ObserveGatewayFailure(FailureInput{Window: window, Candidate: degraded, Reason: "upstream_5xx", SuppressionStateKnown: true, SuppressionAdvancesFailureCount: true}); err != nil || got.Degraded {
		t.Fatalf("first observation = %+v, %v", got, err)
	}
	now = now.Add(time.Minute)
	if got, err := service.ObserveGatewayFailure(FailureInput{Window: window, Candidate: degraded, Reason: "upstream_5xx", SuppressionStateKnown: true, SuppressionAdvancesFailureCount: true}); err != nil || !got.Degraded {
		t.Fatalf("second observation = %+v, %v", got, err)
	}

	result, err := service.OrderFallbackRuntimeDegradation(t.Context(), gatewayfallbackpolicy.RuntimeDegradationInput{
		Window: window, Candidates: []gatewaycandidatewindow.Candidate{degraded, normal},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := join(result.CandidateAccountIDs); got != "normal,degraded" || result.BypassedAllDegraded {
		t.Fatalf("order = %+v", result)
	}
}

func TestServicePreservesAllDegradedOrderAndReportsBypass(t *testing.T) {
	now := time.Date(2026, 8, 4, 10, 0, 0, 0, time.UTC)
	service := newService(t, RuntimeStateDriverMemory, &now)
	window := testWindow()
	first, second := testCandidate("first"), testCandidate("second")
	for _, candidate := range []gatewaycandidatewindow.Candidate{first, second} {
		if _, err := service.ActivateFromProbe(FailureInput{Window: window, Candidate: candidate, Reason: "probe"}, now.Add(-2*time.Minute), 2); err != nil {
			t.Fatal(err)
		}
	}

	result, err := service.OrderFallbackRuntimeDegradation(t.Context(), gatewayfallbackpolicy.RuntimeDegradationInput{
		Window: window, Candidates: []gatewaycandidatewindow.Candidate{first, second},
	})
	if err != nil || join(result.CandidateAccountIDs) != "first,second" || !result.BypassedAllDegraded {
		t.Fatalf("result = %+v, %v", result, err)
	}
}

func TestServiceScopesAuthorizedRuntimeStateToBinding(t *testing.T) {
	now := time.Date(2026, 8, 4, 10, 0, 0, 0, time.UTC)
	service := newService(t, RuntimeStateDriverMemory, &now)
	window := testWindow()
	authorized := testCandidate("view")
	authorized.Projection.AuthorizationID = "authorization"
	authorized.Projection.AuthorizationSourceAccountID = "source"
	authorized.Projection.AuthorizationOwnerSystemAccountID = "owner"
	authorized.Projection.AccountAuthorizationID = "authorization"
	if _, err := service.ActivateFromProbe(FailureInput{Window: window, Candidate: authorized, Reason: "probe"}, now.Add(-2*time.Minute), 2); err != nil {
		t.Fatal(err)
	}

	otherBinding := authorized
	otherWindow := window
	otherWindow.Access.GroupID = "other-group"
	result, err := service.OrderFallbackRuntimeDegradation(t.Context(), gatewayfallbackpolicy.RuntimeDegradationInput{
		Window: otherWindow, Candidates: []gatewaycandidatewindow.Candidate{otherBinding},
	})
	if err != nil || result.BypassedAllDegraded || join(result.CandidateAccountIDs) != "view" {
		t.Fatalf("other binding result = %+v, %v", result, err)
	}
}

func TestServiceRedisDisablesAndClearsLocalState(t *testing.T) {
	now := time.Date(2026, 8, 4, 10, 0, 0, 0, time.UTC)
	service := newService(t, RuntimeStateDriverRedis, &now)
	window, candidate := testWindow(), testCandidate("candidate")
	if got, err := service.ObserveGatewayFailure(FailureInput{Window: window, Candidate: candidate, Reason: "failure"}); err != nil || got.Degraded || got.FailureCount != 0 {
		t.Fatalf("redis observation = %+v, %v", got, err)
	}
	result, err := service.OrderFallbackRuntimeDegradation(t.Context(), gatewayfallbackpolicy.RuntimeDegradationInput{Window: window, Candidates: []gatewaycandidatewindow.Candidate{candidate}})
	if err != nil || result.BypassedAllDegraded || join(result.CandidateAccountIDs) != "candidate" {
		t.Fatalf("redis result = %+v, %v", result, err)
	}
}

func TestServiceRequiresSuppressionAdvanceFactsAndDoesNotCountSameRoundTwice(t *testing.T) {
	now := time.Date(2026, 8, 4, 10, 0, 0, 0, time.UTC)
	service := newService(t, RuntimeStateDriverMemory, &now)
	input := FailureInput{Window: testWindow(), Candidate: testCandidate("candidate"), Reason: "upstream_5xx"}
	if _, err := service.ObserveGatewayFailure(input); err == nil {
		t.Fatal("missing suppression facts succeeded")
	}
	input.SuppressionStateKnown = true
	input.SuppressionAdvancesFailureCount = true
	if got, err := service.ObserveGatewayFailure(input); err != nil || got.FailureCount != 1 {
		t.Fatalf("first observation = %+v, %v", got, err)
	}
	now = now.Add(time.Minute)
	input.SuppressionAdvancesFailureCount = false
	if got, err := service.ObserveGatewayFailure(input); err != nil || got.FailureCount != 1 || got.Degraded {
		t.Fatalf("same suppression round = %+v, %v", got, err)
	}
}

func TestServiceDoesNotCountConcurrentFailuresFromOneSuppressionRoundTwice(t *testing.T) {
	now := time.Date(2026, 8, 4, 10, 0, 0, 0, time.UTC)
	service := newService(t, RuntimeStateDriverMemory, &now)
	input := FailureInput{Window: testWindow(), Candidate: testCandidate("candidate"), Reason: "upstream_5xx", SuppressionStateKnown: true, SuppressionAdvancesFailureCount: true}
	if got, err := service.ObserveGatewayFailure(input); err != nil || got.FailureCount != 1 {
		t.Fatalf("first observation = %+v, %v", got, err)
	}
	now = now.Add(time.Minute)
	input.SuppressionAdvancesFailureCount = false
	var group sync.WaitGroup
	errs := make(chan error, 16)
	for range 16 {
		group.Add(1)
		go func() {
			defer group.Done()
			_, err := service.ObserveGatewayFailure(input)
			errs <- err
		}()
	}
	group.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	result, err := service.OrderFallbackRuntimeDegradation(t.Context(), gatewayfallbackpolicy.RuntimeDegradationInput{Window: testWindow(), Candidates: []gatewaycandidatewindow.Candidate{input.Candidate}})
	if err != nil || result.BypassedAllDegraded || join(result.CandidateAccountIDs) != "candidate" {
		t.Fatalf("same-round concurrent result = %+v, %v", result, err)
	}
}

func TestServiceClearsOnlySuccessfulCandidateRuntimeDegradation(t *testing.T) {
	now := time.Date(2026, 8, 4, 10, 0, 0, 0, time.UTC)
	service := newService(t, RuntimeStateDriverMemory, &now)
	window := testWindow()
	succeeded, retained := testCandidate("succeeded"), testCandidate("retained")
	for _, candidate := range []gatewaycandidatewindow.Candidate{succeeded, retained} {
		if _, err := service.ActivateFromProbe(FailureInput{Window: window, Candidate: candidate, Reason: "probe"}, now.Add(-2*time.Minute), 2); err != nil {
			t.Fatal(err)
		}
	}
	cleared, err := service.ClearGatewaySuccess(SuccessInput{Window: window, Candidate: succeeded})
	if err != nil || !cleared {
		t.Fatalf("clear = %t, %v", cleared, err)
	}
	result, err := service.OrderFallbackRuntimeDegradation(t.Context(), gatewayfallbackpolicy.RuntimeDegradationInput{
		Window: window, Candidates: []gatewaycandidatewindow.Candidate{succeeded, retained},
	})
	if err != nil || join(result.CandidateAccountIDs) != "succeeded,retained" || result.BypassedAllDegraded {
		t.Fatalf("result = %+v, %v", result, err)
	}
}

func TestServiceRejectsInvalidDriverAndAuthorizedFacts(t *testing.T) {
	if _, err := NewService(Options{RuntimeStateDriver: "sqlite"}); err == nil {
		t.Fatal("invalid driver succeeded")
	}
	now := time.Date(2026, 8, 4, 10, 0, 0, 0, time.UTC)
	service := newService(t, RuntimeStateDriverMemory, &now)
	candidate := testCandidate("view")
	candidate.Projection.AuthorizationID = "authorization"
	if _, err := service.OrderFallbackRuntimeDegradation(t.Context(), gatewayfallbackpolicy.RuntimeDegradationInput{Window: testWindow(), Candidates: []gatewaycandidatewindow.Candidate{candidate}}); err == nil {
		t.Fatal("incomplete authorization facts succeeded")
	}
	candidate = testCandidate("view")
	candidate.Projection.AccountAuthorizationID = "authorization"
	if _, err := service.OrderFallbackRuntimeDegradation(t.Context(), gatewayfallbackpolicy.RuntimeDegradationInput{Window: testWindow(), Candidates: []gatewaycandidatewindow.Candidate{candidate}}); err == nil {
		t.Fatal("isolated account authorization id succeeded")
	}
}

func newService(t *testing.T, driver string, now *time.Time) *Service {
	t.Helper()
	service, err := NewService(Options{RuntimeStateDriver: driver, Now: func() time.Time { return *now }})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func testWindow() gatewaycandidatewindow.Window {
	return gatewaycandidatewindow.Window{Access: port.GatewayGroupAccess{GroupID: "group", CallerSystemAccountID: "caller"}}
}

func testCandidate(id string) gatewaycandidatewindow.Candidate {
	return gatewaycandidatewindow.Candidate{Projection: port.GatewayAccountCandidate{AccountID: id}}
}

func join(values []string) string {
	result := ""
	for index, value := range values {
		if index > 0 {
			result += ","
		}
		result += value
	}
	return result
}
