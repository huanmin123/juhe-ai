package gatewaycodex

import (
	"context"
	"math"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycircuit"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// Port of client-profiles/codex-turn-availability-probe.service.ts plus the
// ClientSourceProbeFence shape of accounts/account-health-check-trigger.ts.
//
// The fence settlement stays one-shot: a generation that already carries an
// outcome can never be settled again, and a dispatched probe settles only
// through its exact registered source fence
// (gatewaycircuit.ProbeCoordinator.Settle / SettleDispatchedBySourceFence).

// AvailabilityProbeOutcome mirrors AvailabilityProbeOutcome.
type AvailabilityProbeOutcome = string

// Availability probe outcomes.
const (
	ProbeOutcomeSuccess          = "success"
	ProbeOutcomeHealthFailure    = "health_failure"
	ProbeOutcomeUnknown          = "unknown"
	ProbeOutcomeProbeTaskFailure = "probe_task_failure"
	ProbeOutcomeCanceled         = "canceled"
	ProbeOutcomeStale            = "stale"
)

// SourceProbeFence mirrors ClientSourceProbeFence.
type SourceProbeFence struct {
	StateKey         string
	AccountID        string
	SourceGeneration int64
	SourceFenceID    string
	// RuntimeKey / ProbeGeneration / ConfigRevision are attached for the
	// dispatch envelope.
	RuntimeKey      string
	ProbeGeneration int64
	ConfigRevision  int64
}

// normalizedFenceText mirrors normalizedFenceText(value, limit).
func normalizedFenceText(value string, limit int) string {
	normalized := strings.TrimSpace(value)
	if normalized == "" || len(normalized) > limit {
		return ""
	}
	return normalized
}

// normalizedFenceGeneration mirrors normalizedFenceGeneration: a positive
// finite integer clamped to >= 1.
func normalizedFenceGeneration(value int64) (int64, bool) {
	if value <= 0 || value > math.MaxInt64 {
		return 0, false
	}
	truncated := int64(math.Trunc(float64(value)))
	if truncated < 1 {
		return 0, false
	}
	return truncated, true
}

// NormalizeSourceProbeFence mirrors normalizeClientSourceProbeFence. nil
// mirrors the undefined result.
func NormalizeSourceProbeFence(fence *SourceProbeFence) *SourceProbeFence {
	if fence == nil {
		return nil
	}
	stateKey := normalizedFenceText(fence.StateKey, 512)
	accountID := normalizedFenceText(fence.AccountID, 256)
	runtimeKey := normalizedFenceText(fence.RuntimeKey, 1024)
	sourceFenceID := normalizedFenceText(fence.SourceFenceID, 64)
	sourceGeneration, sourceGenerationOK := normalizedFenceGeneration(fence.SourceGeneration)
	probeGeneration, probeGenerationOK := normalizedFenceGeneration(fence.ProbeGeneration)
	configRevision, configRevisionOK := normalizedFenceGeneration(fence.ConfigRevision)
	if stateKey == "" || accountID == "" || runtimeKey == "" || sourceFenceID == "" ||
		!sourceGenerationOK || !probeGenerationOK || !configRevisionOK {
		return nil
	}
	return &SourceProbeFence{
		StateKey:         stateKey,
		AccountID:        accountID,
		SourceGeneration: sourceGeneration,
		SourceFenceID:    sourceFenceID,
		RuntimeKey:       runtimeKey,
		ProbeGeneration:  probeGeneration,
		ConfigRevision:   configRevision,
	}
}

// AvailabilityProbeCoordinator is the seam toward
// gatewaycircuit.ProbeCoordinator; tests inject fakes.
type AvailabilityProbeCoordinator interface {
	Acquire(ctx context.Context, input gatewaycircuit.ProbeAcquireInput) (gatewaycircuit.ProbeAcquireResult, error)
	ReleaseForExecution(ctx context.Context, input gatewaycircuit.ReleaseProbeInput) (bool, error)
	Settle(ctx context.Context, input gatewaycircuit.SettleProbeInput) (bool, error)
	SettleDispatchedBySourceFence(ctx context.Context, input gatewaycircuit.SettleDispatchedProbeInput) (bool, error)
	GetState(ctx context.Context, runtimeKey string) (*gatewaycircuit.ProbeState, error)
}

// HealthCheckDispatchOutcome mirrors AccountHealthCheckDispatchOutcome.
type HealthCheckDispatchOutcome struct {
	// Outcome is 'queued' | 'rejected'.
	Outcome      string
	DecisionCode string
	TargetRole   string
}

// Health check dispatch outcomes.
const (
	HealthDispatchQueued   = "queued"
	HealthDispatchRejected = "rejected"
)

// AccountHealthCheckDispatchFunc mirrors dispatchAccountHealthCheckWithOutcome.
type AccountHealthCheckDispatchFunc func(accountID string, reason string, traceID string, sourceFence *SourceProbeFence) HealthCheckDispatchOutcome

// CodexTurnAvoidanceProbeInput mirrors
// runCodexTurnAvoidanceAvailabilityProbe's input.
type CodexTurnAvoidanceProbeInput struct {
	Account    gatewayruntimecache.OpenAIAccountSecret
	Strategy   OpenAIGatewayClientStrategyContext
	Activation CodexTurnFailureActivation
	// Dispatch mirrors the optional dispatch override; nil falls back to the
	// wired Dispatch default.
	Dispatch AccountHealthCheckDispatchFunc
}

// CodexTurnAvoidanceProbeResult mirrors the probe result.
type CodexTurnAvoidanceProbeResult struct {
	Disposition string // 'owner' | 'joined'
	Generation  int64
	// Outcome empty mirrors the Node undefined outcome.
	Outcome AvailabilityProbeOutcome
}

// TurnAvoidanceProbeService carries the probe collaborators.
type TurnAvoidanceProbeService struct {
	Coordinator AvailabilityProbeCoordinator
	TurnRetry   *TurnRetryService
	Logger      gatewaypreauth.Logger
	Clock       Clock
	// DefaultDispatch mirrors dispatchAccountHealthCheckWithOutcome.
	DefaultDispatch AccountHealthCheckDispatchFunc
	// AccountRuntimeKey mirrors gatewayAccountRuntimeKey(account).
	AccountRuntimeKey func(account gatewayruntimecache.OpenAIAccountSecret) (string, error)
}

// RunCodexTurnAvoidanceAvailabilityProbe mirrors
// runCodexTurnAvoidanceAvailabilityProbe.
func (s *TurnAvoidanceProbeService) RunCodexTurnAvoidanceAvailabilityProbe(ctx context.Context, input CodexTurnAvoidanceProbeInput) (CodexTurnAvoidanceProbeResult, error) {
	stateKey := strings.TrimSpace(input.Strategy.ClientSourceAvoidanceStateKey)
	if stateKey == "" {
		// The activation API only exposes a source generation for a legal
		// source key. Keep this defensive guard non-mutating if a future
		// caller breaks that contract.
		return CodexTurnAvoidanceProbeResult{Disposition: "joined", Generation: input.Activation.SourceGeneration, Outcome: ProbeOutcomeUnknown}, nil
	}
	accountRuntimeKey, err := s.accountRuntimeKey(input.Account)
	if err != nil {
		return CodexTurnAvoidanceProbeResult{}, err
	}
	coordination, err := s.Coordinator.Acquire(ctx, gatewaycircuit.ProbeAcquireInput{
		AccountRuntimeScope: accountRuntimeKey,
		// Use the same actual availability lease as background health checks.
		ProbeKind:      gatewaycircuit.ProbeKindAccountHealthCheck,
		ConfigRevision: accountConfigRevision(input.Account),
		ExecutionRole:  "source_dispatch",
		SourceFence: &gatewaycircuit.ProbeSourceFence{
			StateKey:         stateKey,
			AccountID:        input.Account.ID,
			SourceGeneration: input.Activation.SourceGeneration,
			SourceFenceID:    input.Activation.SourceFenceID,
		},
	})
	if err != nil {
		return CodexTurnAvoidanceProbeResult{}, err
	}
	if err := s.clearReplacedSettledSourceFences(ctx, coordination); err != nil {
		return CodexTurnAvoidanceProbeResult{}, err
	}
	if coordination.Disposition == gatewaycircuit.ProbeDispositionJoined {
		// A source that joined after a successful shared probe lazily
		// consumes the settled result; non-success outcomes retain its short
		// avoidance.
		settled, err := s.Coordinator.GetState(ctx, coordination.RuntimeKey)
		if err != nil {
			return CodexTurnAvoidanceProbeResult{}, err
		}
		if settled != nil && settled.Outcome != nil && *settled.Outcome == ProbeOutcomeSuccess {
			if _, err := s.TurnRetry.ClearCodexTurnAccountAvoidanceByFenceAsync(ctx, ClearCodexTurnAccountAvoidanceByFenceInput{
				StateKey:         stateKey,
				AccountID:        input.Account.ID,
				SourceGeneration: input.Activation.SourceGeneration,
				SourceFenceID:    input.Activation.SourceFenceID,
			}); err != nil {
				return CodexTurnAvoidanceProbeResult{}, err
			}
		}
		// A joined source still hands its fence to the worker. This is a
		// fence merge, not a follow-up probe: the worker's per-account
		// execution record attaches it to a running ordinary/source task when
		// one exists.
		handoff := s.dispatchSourceFence(input, s.sourceFenceForDispatch(input, coordination.RuntimeKey, coordination.Generation))
		result := CodexTurnAvoidanceProbeResult{Disposition: "joined", Generation: coordination.Generation}
		if handoff.Outcome == HealthDispatchRejected {
			result.Outcome = ProbeOutcomeUnknown
		}
		return result, nil
	}

	sourceFence := s.sourceFenceForDispatch(input, coordination.RuntimeKey, coordination.Generation)
	// Mark the generation as dispatch-pending before sending IPC/control
	// work. A fast control rejection can then settle this exact fence instead
	// of racing the owner token and leaving the generation stranded.
	released, err := s.Coordinator.ReleaseForExecution(ctx, gatewaycircuit.ReleaseProbeInput{
		RuntimeKey: coordination.RuntimeKey,
		Generation: coordination.Generation,
		OwnerToken: coordination.OwnerToken,
	})
	if err != nil {
		return CodexTurnAvoidanceProbeResult{}, err
	}
	if !released {
		if _, err := s.Coordinator.Settle(ctx, gatewaycircuit.SettleProbeInput{
			RuntimeKey: coordination.RuntimeKey,
			Generation: coordination.Generation,
			OwnerToken: coordination.OwnerToken,
			Outcome:    ProbeOutcomeUnknown,
		}); err != nil {
			return CodexTurnAvoidanceProbeResult{}, err
		}
		return CodexTurnAvoidanceProbeResult{Disposition: "owner", Generation: coordination.Generation, Outcome: ProbeOutcomeUnknown}, nil
	}
	dispatch := s.dispatchSourceFence(input, sourceFence)
	if dispatch.Outcome == HealthDispatchQueued {
		// The health worker, not the source observer, is now the only
		// component allowed to issue the upstream diagnostic and settle
		// account health.
		return CodexTurnAvoidanceProbeResult{Disposition: "owner", Generation: coordination.Generation}, nil
	}
	outcome := ProbeOutcomeProbeTaskFailure
	if _, err := s.Coordinator.SettleDispatchedBySourceFence(ctx, gatewaycircuit.SettleDispatchedProbeInput{
		RuntimeKey: coordination.RuntimeKey,
		Generation: coordination.Generation,
		SourceFence: gatewaycircuit.ProbeSourceFence{
			StateKey:         sourceFence.StateKey,
			AccountID:        sourceFence.AccountID,
			SourceGeneration: sourceFence.SourceGeneration,
			SourceFenceID:    sourceFence.SourceFenceID,
		},
		Outcome: outcome,
	}); err != nil {
		return CodexTurnAvoidanceProbeResult{}, err
	}
	return CodexTurnAvoidanceProbeResult{Disposition: "owner", Generation: coordination.Generation, Outcome: outcome}, nil
}

func (s *TurnAvoidanceProbeService) clearReplacedSettledSourceFences(ctx context.Context, coordination gatewaycircuit.ProbeAcquireResult) error {
	if coordination.Disposition != gatewaycircuit.ProbeDispositionOwner ||
		coordination.ReplacedFenceSettlement == nil ||
		coordination.ReplacedFenceSettlement.Outcome != ProbeOutcomeSuccess {
		return nil
	}
	for _, fence := range coordination.ReplacedFenceSettlement.SourceFences {
		if _, err := s.TurnRetry.ClearCodexTurnAccountAvoidanceByFenceAsync(ctx, ClearCodexTurnAccountAvoidanceByFenceInput{
			StateKey:         fence.StateKey,
			AccountID:        fence.AccountID,
			SourceGeneration: fence.SourceGeneration,
			SourceFenceID:    fence.SourceFenceID,
		}); err != nil {
			s.warn("gateway_codex_turn_replaced_probe_fence_clear_failed", map[string]any{
				"accountId":        fence.AccountID,
				"sourceGeneration": fence.SourceGeneration,
			}, "替换已结算探活 generation 后未能清理旧来源避让")
		}
	}
	return nil
}

func (s *TurnAvoidanceProbeService) sourceFenceForDispatch(input CodexTurnAvoidanceProbeInput, runtimeKey string, generation int64) SourceProbeFence {
	return SourceProbeFence{
		StateKey:         strings.TrimSpace(input.Strategy.ClientSourceAvoidanceStateKey),
		AccountID:        input.Account.ID,
		SourceGeneration: input.Activation.SourceGeneration,
		SourceFenceID:    input.Activation.SourceFenceID,
		RuntimeKey:       runtimeKey,
		ProbeGeneration:  generation,
		ConfigRevision:   accountConfigRevision(input.Account),
	}
}

func (s *TurnAvoidanceProbeService) dispatchSourceFence(input CodexTurnAvoidanceProbeInput, sourceFence SourceProbeFence) HealthCheckDispatchOutcome {
	dispatch := input.Dispatch
	if dispatch == nil {
		dispatch = s.DefaultDispatch
	}
	if dispatch == nil {
		return HealthCheckDispatchOutcome{Outcome: HealthDispatchRejected, DecisionCode: "input_unavailable"}
	}
	fence := sourceFence
	return dispatch(input.Account.ID, "request_failure", "", &fence)
}

func (s *TurnAvoidanceProbeService) accountRuntimeKey(account gatewayruntimecache.OpenAIAccountSecret) (string, error) {
	if s.AccountRuntimeKey != nil {
		return s.AccountRuntimeKey(account)
	}
	return gatewaycircuit.GatewayAccountRuntimeKey(gatewaycircuit.SuppressibleGatewayAccount{
		ID:                        account.ID,
		AccessType:                account.AccountAccessType,
		AccountAccessType:         account.AccountAccessType,
		BindingSystemAccountID:    derefString(account.BindingSystemAccountID),
		BoundGroupID:              derefString(account.BoundGroupID),
		AccountAuthorizationID:    derefString(account.AccountAuthorizationID),
		CredentialSourceAccountID: derefString(account.CredentialSourceAccountID),
	})
}

func accountConfigRevision(account gatewayruntimecache.OpenAIAccountSecret) int64 {
	revision := account.ConfigRevision
	if revision == nil {
		return 1
	}
	if !isFiniteInt64(*revision) {
		return 1
	}
	if *revision < 1 {
		return 1
	}
	return *revision
}

func isFiniteInt64(value int64) bool {
	// Node checks Number.isFinite on a number; int64 is always finite, the
	// nil case carries the Node undefined.
	return true
}

func (s *TurnAvoidanceProbeService) warn(event string, fields map[string]any, message string) {
	if s.Logger == nil {
		return
	}
	merged := map[string]any{"event": event}
	for key, value := range fields {
		merged[key] = value
	}
	s.Logger.Warn(event, merged, message)
}
