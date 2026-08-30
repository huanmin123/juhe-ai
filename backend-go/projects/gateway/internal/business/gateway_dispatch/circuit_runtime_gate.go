package gatewaydispatch

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	circuitruntime "github.com/huanminabc/juhe-ai/backend-go-gateway/internal/business/circuit_runtime"
)

// RuntimeCircuitGate adapts the Gateway-owned Redis account circuit runtime
// to the narrow dispatch port. It performs no network or IPC calls.
type RuntimeCircuitGate struct {
	Store *circuitruntime.Store
}

func (g RuntimeCircuitGate) Prepare(ctx context.Context, input AccountCircuitInput) (AccountCircuitDecision, AccountCircuitAttempt, error) {
	if g.Store == nil {
		return "", nil, errors.New("gateway account circuit runtime store is required")
	}
	if strings.TrimSpace(input.AccountID) == "" {
		return "", nil, errors.New("gateway account circuit account id is required")
	}
	now := time.Now().UTC()
	state, err := g.Store.GetGatewayAccountCircuit(ctx, circuitruntime.GatewayAccountCircuitGetInput{
		AccountID: input.AccountID,
		Scope: circuitruntime.GatewayAccountCircuitScope{
			Kind:              circuitruntime.GatewayAccountCircuitScopeAccount,
			AccountRuntimeKey: input.AccountID,
		},
		Now: now,
	})
	if err != nil {
		return "", nil, err
	}
	if state.Phase == circuitruntime.GatewayAccountCircuitPhaseOpen || state.Phase == circuitruntime.GatewayAccountCircuitPhaseSuspect {
		if state.RetryAt == nil || state.RetryAt.After(now) {
			return AccountCircuitBlocked, nil, nil
		}
	}
	// A SUSPECT circuit may only be probed by a request independently qualified
	// for confirmation. Node settles an ineligible request as neutral and
	// blocks it; acquiring a confirmation lease here would let an ordinary
	// retry confirm the preceding failure.
	if circuitConfirmationIneligible(state, input) {
		return AccountCircuitBlocked, nil, nil
	}
	identity := circuitruntime.GatewayAccountCircuitTransitionIdentity{AccountID: input.AccountID, Scope: circuitruntime.GatewayAccountCircuitScope{Kind: circuitruntime.GatewayAccountCircuitScopeAccount, AccountRuntimeKey: input.AccountID}, Generation: state.Generation, DispatchRevision: input.DispatchRevision, TransitionID: input.FailureEvidenceKey, Now: now}
	attempt := &runtimeCircuitAttempt{store: g.Store, input: input, state: state, identity: identity}
	leaseID := fmt.Sprintf("dispatch-%s", input.FailureEvidenceKey)
	leaseUntil := now.Add(input.ConfirmationLeaseDuration)
	if leaseUntil.Before(now.Add(time.Second)) {
		leaseUntil = now.Add(time.Minute)
	}
	if state.Phase == circuitruntime.GatewayAccountCircuitPhaseSuspect {
		mutation, acquireErr := g.Store.AcquireGatewayAccountCircuitConfirmationLease(ctx, circuitruntime.GatewayAccountCircuitAcquireConfirmationLeaseInput{GatewayAccountCircuitTransitionIdentity: identity, LeaseID: leaseID, LeaseUntil: leaseUntil})
		if acquireErr != nil {
			return "", nil, acquireErr
		}
		if mutation.Status != circuitruntime.GatewayAccountCircuitMutationApplied && mutation.Status != circuitruntime.GatewayAccountCircuitMutationIdempotent {
			return AccountCircuitBlocked, nil, nil
		}
		attempt.leaseID, attempt.leaseKind = leaseID, circuitruntime.GatewayAccountCircuitLeaseConfirmation
	} else if state.Phase == circuitruntime.GatewayAccountCircuitPhaseHalfOpen || state.Phase == circuitruntime.GatewayAccountCircuitPhaseRecovering {
		mutation, acquireErr := g.Store.AcquireGatewayAccountCircuitCanaryLease(ctx, circuitruntime.GatewayAccountCircuitAcquireCanaryLeaseInput{GatewayAccountCircuitTransitionIdentity: identity, LeaseID: leaseID, LeaseUntil: leaseUntil})
		if acquireErr != nil {
			return "", nil, acquireErr
		}
		if mutation.Status != circuitruntime.GatewayAccountCircuitMutationApplied && mutation.Status != circuitruntime.GatewayAccountCircuitMutationIdempotent {
			return AccountCircuitBlocked, nil, nil
		}
		attempt.leaseID, attempt.leaseKind = leaseID, circuitruntime.GatewayAccountCircuitLeaseHalfOpen
	}
	return AccountCircuitDispatchable, attempt, nil
}

func circuitConfirmationIneligible(state circuitruntime.GatewayAccountCircuitState, input AccountCircuitInput) bool {
	return state.Phase == circuitruntime.GatewayAccountCircuitPhaseSuspect && !input.ConfirmationEligible
}

type runtimeCircuitAttempt struct {
	store     *circuitruntime.Store
	input     AccountCircuitInput
	state     circuitruntime.GatewayAccountCircuitState
	settled   bool
	leaseID   string
	leaseKind circuitruntime.GatewayAccountCircuitLeaseKind
	identity  circuitruntime.GatewayAccountCircuitTransitionIdentity
}

func (a *runtimeCircuitAttempt) ReportFramingComplete(ctx context.Context) error {
	if a == nil || a.settled {
		return nil
	}
	a.settled = true
	if a.leaseID == "" {
		return nil
	}
	if a.leaseKind == circuitruntime.GatewayAccountCircuitLeaseConfirmation {
		_, err := a.store.CompleteGatewayAccountCircuitConfirmation(ctx, circuitruntime.GatewayAccountCircuitCompleteConfirmationInput{GatewayAccountCircuitTransitionIdentity: a.identity, LeaseID: a.leaseID, Outcome: circuitruntime.GatewayAccountCircuitCompletionFramingComplete})
		return err
	}
	_, err := a.store.CompleteGatewayAccountCircuitCanary(ctx, circuitruntime.GatewayAccountCircuitCompleteCanaryInput{GatewayAccountCircuitTransitionIdentity: a.identity, LeaseID: a.leaseID, Outcome: circuitruntime.GatewayAccountCircuitCompletionFramingComplete})
	return err
}

func (a *runtimeCircuitAttempt) ReportTransportFailure(ctx context.Context, cause error) error {
	if a == nil || a.settled {
		return nil
	}
	a.settled = true
	return a.suspect(ctx, "transport_failure", cause, circuitruntime.GatewayAccountCircuitCompletionTransportFailure)
}

func (a *runtimeCircuitAttempt) ReportUnknown(ctx context.Context) error {
	if a == nil || a.settled {
		return nil
	}
	a.settled = true
	// An unknown lifecycle is deliberately neutral unless this attempt owns a
	// confirmation/canary lease. In particular, local admission failures,
	// cancellation and an attempt that never reached the upstream must never
	// turn an otherwise closed account circuit into SUSPECT.
	if a.leaseID == "" {
		return nil
	}
	return a.completeLease(ctx, "unknown", nil, circuitruntime.GatewayAccountCircuitCompletionUnknown)
}

func (a *runtimeCircuitAttempt) suspect(ctx context.Context, reason string, cause error, outcome circuitruntime.GatewayAccountCircuitCompletionOutcome) error {
	if cause != nil {
		reason += ": " + cause.Error()
	}
	if a.leaseID != "" {
		return a.completeLease(ctx, reason, nil, outcome)
	}
	_, err := a.store.SuspectGatewayAccountCircuit(ctx, circuitruntime.GatewayAccountCircuitSuspectInput{
		AccountID:        a.input.AccountID,
		Scope:            circuitruntime.GatewayAccountCircuitScope{Kind: circuitruntime.GatewayAccountCircuitScopeAccount, AccountRuntimeKey: a.input.AccountID},
		DispatchRevision: a.input.DispatchRevision,
		TransitionID:     a.input.FailureEvidenceKey,
		Reason:           reason,
		Now:              time.Now().UTC(),
	})
	return err
}

func (a *runtimeCircuitAttempt) completeLease(ctx context.Context, reason string, cause error, outcome circuitruntime.GatewayAccountCircuitCompletionOutcome) error {
	if cause != nil {
		reason += ": " + cause.Error()
	}
	if a.leaseKind == circuitruntime.GatewayAccountCircuitLeaseConfirmation {
		_, err := a.store.CompleteGatewayAccountCircuitConfirmation(ctx, circuitruntime.GatewayAccountCircuitCompleteConfirmationInput{GatewayAccountCircuitTransitionIdentity: a.identity, LeaseID: a.leaseID, Outcome: outcome, Reason: reason})
		return err
	}
	_, err := a.store.CompleteGatewayAccountCircuitCanary(ctx, circuitruntime.GatewayAccountCircuitCompleteCanaryInput{GatewayAccountCircuitTransitionIdentity: a.identity, LeaseID: a.leaseID, Outcome: outcome, Reason: reason})
	return err
}
