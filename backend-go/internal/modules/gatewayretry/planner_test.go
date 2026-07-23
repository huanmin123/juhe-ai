package gatewayretry

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/modules/gatewayerrors"
	"juhe-ai/backend-go/internal/modules/gatewayrouting"
	"juhe-ai/backend-go/internal/store/port"
)

func TestPlannerUsesRouteOrderAndNeverAttemptsCandidateTwice(t *testing.T) {
	t.Parallel()

	planner := mustPlanner(t, PlanInput{
		Route: gatewayrouting.OrderResult{Bindings: []gatewayrouting.Binding{
			{ID: "binding-b", GroupID: "group-b"},
			{ID: "binding-a", GroupID: "group-a"},
		}},
		Candidates: []port.GatewayAccountCandidate{
			{AccountID: "account-a", GroupID: "group-a"},
			{AccountID: "account-b", GroupID: "group-b"},
			{AccountID: "account-a", GroupID: "group-b"},
		},
		Protocol:    gatewayerrors.ProtocolAnthropic,
		MaxAttempts: 5,
	})

	first := planner.Start(context.Background())
	assertAttempt(t, first, "binding-b", "account-b", 1)
	if first.Protocol != gatewayerrors.ProtocolAnthropic {
		t.Fatalf("first protocol = %q, want Anthropic", first.Protocol)
	}

	second := planner.Fail(context.Background(), *first.Attempt, Failure{Phase: PhaseUpstreamRequest})
	assertAttempt(t, second, "binding-b", "account-a", 2)

	terminal := planner.Fail(context.Background(), *second.Attempt, Failure{Phase: PhaseUpstreamRequest})
	if terminal.Action != ActionStopped || terminal.Reason != ReasonCandidatesExhausted {
		t.Fatalf("terminal decision = %+v, want candidate exhaustion", terminal)
	}
	if got, want := planner.AttemptedAccountIDs(), []string{"account-b", "account-a"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("attempted account IDs = %v, want %v", got, want)
	}
}

func TestPlannerStopsAtMaxAttemptsEvenWhenCandidatesRemain(t *testing.T) {
	t.Parallel()

	planner := mustPlanner(t, testPlan(2, "a", "b", "c"))
	first := planner.Start(context.Background())
	second := planner.Fail(context.Background(), *first.Attempt, Failure{Phase: PhaseUpstreamResponse, StatusCode: 503, ResponseDisposition: ResponseDispositionExplicitPolicy})
	assertAttempt(t, second, "binding", "b", 2)

	terminal := planner.Fail(context.Background(), *second.Attempt, Failure{Phase: PhaseUpstreamResponse, StatusCode: 429, ResponseDisposition: ResponseDispositionExplicitPolicy})
	if terminal.Action != ActionStopped || terminal.Reason != ReasonMaxAttempts {
		t.Fatalf("terminal decision = %+v, want max attempts", terminal)
	}
	if terminal.AttemptCount != 2 || terminal.AttemptsRemaining != 0 {
		t.Fatalf("terminal budget = count %d remaining %d", terminal.AttemptCount, terminal.AttemptsRemaining)
	}
}

func TestClassifyFailureAcceptsExplicitTwoXXProtocolSignals(t *testing.T) {
	for _, signal := range []ResponseSignal{ResponseSignalProtocolContract, ResponseSignalStreamInterrupted} {
		classification := ClassifyFailure(Failure{Phase: PhaseUpstreamResponse, ResponseSignal: signal})
		if !classification.Retryable || classification.Class == FailureClassUnknown {
			t.Fatalf("signal %q classification = %#v", signal, classification)
		}
	}
	invalid := ClassifyFailure(Failure{Phase: PhaseUpstreamResponse, ResponseSignal: ResponseSignal("future")})
	if invalid.Retryable || invalid.Class != FailureClassUnknown {
		t.Fatalf("invalid signal classification = %#v", invalid)
	}
}

func TestClassifyFailureKeepsCompleteHTTPResponseTransparentWithoutPolicy(t *testing.T) {
	transparent := ClassifyFailure(Failure{
		Phase: PhaseUpstreamResponse, StatusCode: 503,
		ResponseDisposition: ResponseDispositionCompleteTransparent,
	})
	if transparent.Retryable || transparent.Reason != "complete_response_transparent" {
		t.Fatalf("transparent classification = %#v", transparent)
	}
	explicit := ClassifyFailure(Failure{
		Phase: PhaseUpstreamResponse, StatusCode: 503,
		ResponseDisposition: ResponseDispositionExplicitPolicy,
	})
	if !explicit.Retryable || explicit.Class != FailureClassUpstreamService {
		t.Fatalf("explicit policy classification = %#v", explicit)
	}
}

func TestPlannerAdvancesOnExplicitProtocolResponseSignal(t *testing.T) {
	planner := mustPlanner(t, testPlan(2, "a", "b"))
	first := planner.Start(context.Background())
	second := planner.Fail(context.Background(), *first.Attempt, Failure{
		Phase: PhaseUpstreamResponse, ResponseSignal: ResponseSignalProtocolContract,
	})
	assertAttempt(t, second, "binding", "b", 2)
}

func TestPlannerDoesNotRotateOnCompleteResponseWithoutExplicitPolicy(t *testing.T) {
	planner := mustPlanner(t, testPlan(2, "a", "b"))
	first := planner.Start(context.Background())
	terminal := planner.Fail(context.Background(), *first.Attempt, Failure{
		Phase: PhaseUpstreamResponse, StatusCode: 503,
		ResponseDisposition: ResponseDispositionCompleteTransparent,
	})
	if terminal.Action != ActionStopped || terminal.Reason != ReasonNonRetryableFailure {
		t.Fatalf("terminal = %#v", terminal)
	}
}

func TestPlannerStopsOnClientCancelAndContextDeadline(t *testing.T) {
	t.Parallel()

	t.Run("client cancel failure", func(t *testing.T) {
		planner := mustPlanner(t, testPlan(3, "a", "b"))
		first := planner.Start(context.Background())
		terminal := planner.Fail(context.Background(), *first.Attempt, Failure{
			Phase:          PhaseUpstreamRequest,
			Err:            context.Canceled,
			ClientCanceled: true,
		})
		if terminal.Action != ActionStopped || terminal.Reason != ReasonClientCanceled {
			t.Fatalf("terminal decision = %+v", terminal)
		}
	})

	t.Run("expired context overrides retryable response", func(t *testing.T) {
		planner := mustPlanner(t, testPlan(3, "a", "b"))
		first := planner.Start(context.Background())
		ctx, cancel := context.WithCancelCause(context.Background())
		cancel(context.DeadlineExceeded)
		terminal := planner.Fail(ctx, *first.Attempt, Failure{Phase: PhaseUpstreamResponse, StatusCode: 503})
		if terminal.Action != ActionStopped || terminal.Reason != ReasonContextDeadline {
			t.Fatalf("terminal decision = %+v", terminal)
		}
	})

	t.Run("expired context before first attempt", func(t *testing.T) {
		planner := mustPlanner(t, testPlan(3, "a"))
		ctx, cancel := context.WithDeadline(context.Background(), testDeadlineInPast())
		defer cancel()
		terminal := planner.Start(ctx)
		if terminal.Action != ActionStopped || terminal.Reason != ReasonContextDeadline || terminal.Attempt != nil {
			t.Fatalf("start decision = %+v", terminal)
		}
	})
}

func TestPlannerNeverRetriesAfterFirstDownstreamByte(t *testing.T) {
	t.Parallel()

	planner := mustPlanner(t, testPlan(3, "a", "b"))
	first := planner.Start(context.Background())
	terminal := planner.Fail(context.Background(), *first.Attempt, Failure{
		Phase:               PhaseUpstreamResponse,
		StatusCode:          503,
		FirstByteForwarded:  true,
		ResponseDisposition: ResponseDispositionExplicitPolicy,
	})
	if terminal.Action != ActionStopped || terminal.Reason != ReasonDownstreamCommitted {
		t.Fatalf("terminal decision = %+v", terminal)
	}
	if terminal.Classification.Class != FailureClassUpstreamService || !terminal.Classification.Retryable {
		t.Fatalf("classification = %+v, want intrinsically retryable 503", terminal.Classification)
	}
}

func TestPlannerRetriesOnlyNodeEquivalentFailureClasses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		failure   Failure
		wantClass FailureClass
		wantRetry bool
	}{
		{name: "transport", failure: Failure{Phase: PhaseUpstreamRequest, Err: errors.New("connection reset")}, wantClass: FailureClassTransport, wantRetry: true},
		{name: "credential status", failure: Failure{Phase: PhaseUpstreamResponse, StatusCode: 401, ResponseDisposition: ResponseDispositionExplicitPolicy}, wantClass: FailureClassCredential, wantRetry: true},
		{name: "rate limit code", failure: Failure{Phase: PhaseUpstreamResponse, ErrorCode: " RATE_LIMIT_EXCEEDED ", ResponseDisposition: ResponseDispositionExplicitPolicy}, wantClass: FailureClassRateLimit, wantRetry: true},
		{name: "server response", failure: Failure{Phase: PhaseUpstreamResponse, StatusCode: 500, ResponseDisposition: ResponseDispositionExplicitPolicy}, wantClass: FailureClassUpstreamService, wantRetry: true},
		{name: "request semantic code", failure: Failure{Phase: PhaseUpstreamResponse, StatusCode: 500, ErrorCode: "model_not_found", ResponseDisposition: ResponseDispositionExplicitPolicy}, wantClass: FailureClassRequestSemantic, wantRetry: false},
		{name: "unknown client response", failure: Failure{Phase: PhaseUpstreamResponse, StatusCode: 400, ResponseDisposition: ResponseDispositionExplicitPolicy}, wantClass: FailureClassUnknown, wantRetry: false},
		{name: "invalid phase is fail closed", failure: Failure{Phase: FailurePhase("typo"), StatusCode: 503}, wantClass: FailureClassUnknown, wantRetry: false},
		{name: "client lifecycle", failure: Failure{Phase: PhaseClientLifecycle}, wantClass: FailureClassClientLifecycle, wantRetry: false},
		{name: "gateway policy", failure: Failure{Phase: PhaseGatewayPolicy}, wantClass: FailureClassGatewayPolicy, wantRetry: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			classification := ClassifyFailure(test.failure)
			if classification.Class != test.wantClass || classification.Retryable != test.wantRetry {
				t.Fatalf("ClassifyFailure() = %+v, want class %q retry %v", classification, test.wantClass, test.wantRetry)
			}
		})
	}
}

func TestPlannerSuccessIsTerminalAndHasNoRemainingAttempts(t *testing.T) {
	t.Parallel()

	planner := mustPlanner(t, testPlan(3, "a", "b"))
	first := planner.Start(context.Background())
	success := planner.Succeed(*first.Attempt)
	if success.Action != ActionSucceeded || success.Reason != ReasonSucceeded || success.AttemptsRemaining != 0 {
		t.Fatalf("success decision = %+v", success)
	}
	if repeated := planner.Start(context.Background()); repeated.Action != ActionSucceeded || repeated.Attempt != nil {
		t.Fatalf("repeated terminal decision = %+v", repeated)
	}
}

func TestPlannerRejectsInvalidConstructionAndTransition(t *testing.T) {
	t.Parallel()

	if _, err := NewPlanner(PlanInput{MaxAttempts: 0}); err == nil {
		t.Fatal("NewPlanner() error = nil, want invalid max attempts")
	}
	if _, err := NewPlanner(PlanInput{
		Route:       gatewayrouting.OrderResult{Bindings: []gatewayrouting.Binding{{ID: "binding", GroupID: "group"}}},
		Candidates:  []port.GatewayAccountCandidate{{AccountID: "", GroupID: "group"}},
		MaxAttempts: 1,
	}); err == nil {
		t.Fatal("NewPlanner() error = nil, want blank account ID")
	}

	planner := mustPlanner(t, testPlan(2, "a", "b"))
	first := planner.Start(context.Background())
	repeatedStart := planner.Start(context.Background())
	assertAttempt(t, repeatedStart, "binding", "a", 1)
	invalid := *first.Attempt
	invalid.Account.AccountID = "other"
	rejected := planner.Fail(context.Background(), invalid, Failure{Phase: PhaseUpstreamRequest})
	if rejected.Action != ActionRejected || rejected.Reason != ReasonInvalidTransition {
		t.Fatalf("rejected decision = %+v", rejected)
	}
	if success := planner.Succeed(*first.Attempt); success.Action != ActionSucceeded {
		t.Fatalf("valid attempt could not finish after rejected transition: %+v", success)
	}
}

func TestStaleResolutionDoesNotTerminateNewAttempt(t *testing.T) {
	t.Parallel()

	planner := mustPlanner(t, testPlan(3, "a", "b"))
	first := planner.Start(context.Background())
	second := planner.Fail(context.Background(), *first.Attempt, Failure{Phase: PhaseUpstreamRequest})
	assertAttempt(t, second, "binding", "b", 2)

	rejected := planner.Fail(context.Background(), *first.Attempt, Failure{Phase: PhaseUpstreamRequest})
	if rejected.Action != ActionRejected || rejected.Reason != ReasonInvalidTransition {
		t.Fatalf("stale resolution = %+v", rejected)
	}
	if success := planner.Succeed(*second.Attempt); success.Action != ActionSucceeded {
		t.Fatalf("current attempt could not finish after stale resolution: %+v", success)
	}
}

func testPlan(maxAttempts int, ids ...string) PlanInput {
	candidates := make([]port.GatewayAccountCandidate, 0, len(ids))
	for _, id := range ids {
		candidates = append(candidates, port.GatewayAccountCandidate{AccountID: id, GroupID: "group"})
	}
	return PlanInput{
		Route:       gatewayrouting.OrderResult{Bindings: []gatewayrouting.Binding{{ID: "binding", GroupID: "group"}}},
		Candidates:  candidates,
		Protocol:    gatewayerrors.ProtocolOpenAI,
		MaxAttempts: maxAttempts,
	}
}

func mustPlanner(t *testing.T, input PlanInput) *Planner {
	t.Helper()
	planner, err := NewPlanner(input)
	if err != nil {
		t.Fatalf("NewPlanner() error = %v", err)
	}
	return planner
}

func assertAttempt(t *testing.T, decision Decision, bindingID, accountID string, sequence int) {
	t.Helper()
	if decision.Action != ActionAttempt || decision.Attempt == nil {
		t.Fatalf("decision = %+v, want attempt", decision)
	}
	if decision.Attempt.Binding.ID != bindingID || decision.Attempt.Account.AccountID != accountID || decision.Attempt.Sequence != sequence {
		t.Fatalf("attempt = %+v, want binding %q account %q sequence %d", decision.Attempt, bindingID, accountID, sequence)
	}
}

func testDeadlineInPast() time.Time {
	return time.Now().Add(-time.Second)
}
