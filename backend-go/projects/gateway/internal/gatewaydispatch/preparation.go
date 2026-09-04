package gatewaydispatch

import (
	"context"
	"sync"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// prepareOpenAIGatewayDispatchAccounts, migrated from dispatch/preparation.ts.
// Every stage hook, log event and Chinese message mirrors the Node
// implementation; the collaborator services arrive through the ports.

// Preparation outcome union (Node outcome tags + 'ready').
const (
	PreparationOutcomeReady     = "ready"
	PreparationOutcomeFallback  = "fallback"
	PreparationOutcomeCompleted = "completed"
)

// PreparationResult mirrors DispatchPreparationResult.
type PreparationResult struct {
	// Outcome is 'ready' | 'fallback' | 'completed'.
	Outcome string
	// ready variant
	Accounts                               []AccountCandidate
	ReleaseClientIPConcurrency             func()
	NormalRouteLatencyDegradationApplied   bool
	CodexTurnAccountAvoidanceApplied       bool
	CodexTurnAvoidedAccountIDs             []string
	PrecheckHalfOpenEligible               bool
	HotQualityExplorationReservation       *HotQualityReservation
	SettleHotQualityExplorationAfterDispatch func(ctx context.Context, outcome string) error
	// fallback variant
	Reason  string
	Context any
}

// localSuppressionBypassResult mirrors localSuppressionBypassResult.
func localSuppressionBypassResult(accounts []AccountCandidate) SuppressionFilterResult {
	return SuppressionFilterResult{
		Accounts:             accounts,
		SuppressedCount:      0,
		AllSuppressed:        false,
		SuppressedAccountIDs: []string{},
		AcquiredHalfOpenLeases: []HalfOpenLease{},
	}
}

// releaseHalfOpenLease mirrors releaseHalfOpenLease.
func releaseHalfOpenLease(ctx context.Context, lease HalfOpenLease) bool {
	if lease == nil {
		return false
	}
	released, err := lease.Release()
	if released && err == nil {
		notifyOneRecoverableUnavailableRuntimeWaiter(lease.RuntimeKey())
	}
	return released
}

// completeHalfOpenLeaseSuccess mirrors completeHalfOpenLeaseSuccess.
func completeHalfOpenLeaseSuccess(ctx context.Context, lease HalfOpenLease) bool {
	if lease == nil {
		return false
	}
	completed, err := lease.CompleteSuccess()
	if completed && err == nil {
		notifyOneRecoverableUnavailableRuntimeWaiter(lease.RuntimeKey())
	}
	return completed
}

// notifyOneRecoverableUnavailableRuntimeWaiter is the waiter wake hook (Node
// runtime/recoverable-unavailable-wait.ts); the concrete wake surface is
// wired by G20, nil = no-op.
var notifyOneRecoverableUnavailableRuntimeWaiter = func(runtimeKey string) {}

// requestRouteFallback mirrors requestRouteFallback with the account-lock
// guard from preparation.ts.
func (p *CandidatePipeline) requestRouteFallback(
	ctx context.Context,
	input gatewaypreauth.DispatchPreparationInput,
	candidateAccounts []AccountCandidate,
	reason string,
) (attempted bool, contextValue any, err error) {
	if reason != "authorization_quota_exceeded" && p.engine.Locks != nil {
		ids := make([]string, 0, len(candidateAccounts))
		for _, account := range candidateAccounts {
			ids = append(ids, account.ID)
		}
		states, stateErr := p.engine.Locks.ListStatesAsync(ctx, ids)
		if stateErr != nil {
			return false, nil, stateErr
		}
		for _, state := range states {
			if accountLockBlocksCrossAccount(state) {
				return false, nil, nil
			}
		}
	}
	fallback, err := input.RouteCoordinator.RequestFallback(ctx, reason)
	if err != nil {
		return false, nil, err
	}
	return fallback.Attempted, fallback.Context, nil
}

// PrepareOpenAIGatewayDispatchAccounts mirrors
// prepareOpenAIGatewayDispatchAccounts.
func (p *CandidatePipeline) PrepareOpenAIGatewayDispatchAccounts(ctx context.Context, input gatewaypreauth.DispatchPreparationInput) (PreparationResult, error) {
	e := p.engine
	dispatchOrderingOptions := AffinityOrderingOptions{
		GroupType:        groupTypeOf(input.GroupAccess),
		SchedulingPolicy: input.GroupAccess.SchedulingPolicy,
		ModelPriority:    input.ModelPriority,
		TrafficMigrationScope: &AffinityScope{
			SystemAccountID: input.SystemAccountID,
			APIKeyID:        input.APIKeyID,
			GroupID:         input.GroupID,
		},
	}

	orderedCandidateAccounts, err := e.Affinity.OrderAsync(ctx, input.CandidateAccounts, input.SessionAffinityKey, dispatchOrderingOptions)
	if err != nil {
		return PreparationResult{}, err
	}

	bypassLocalSuppression := input.IgnoreAccountRuntimeSuppression || isAccountProbeTrafficSource(input.UsageContext.TrafficSource)
	var initialLocalSuppressionFilter SuppressionFilterResult
	if bypassLocalSuppression {
		initialLocalSuppressionFilter = localSuppressionBypassResult(orderedCandidateAccounts)
	} else {
		initialLocalSuppressionFilter, err = e.Suppression.FilterAsync(ctx, orderedCandidateAccounts, SuppressionFilterOptions{})
		if err != nil {
			return PreparationResult{}, err
		}
	}

	precheckHalfOpenEligible := false
	if initialLocalSuppressionFilter.AllSuppressed {
		fallbackAttempted, fallbackContext, err := p.requestRouteFallback(ctx, input, orderedCandidateAccounts, "local_account_suppressed")
		if err != nil {
			return PreparationResult{}, err
		}
		if fallbackAttempted {
			input.AuditCapture.AddGatewayMetadata("local_account_suppression", map[string]any{
				"suppressedCount":      initialLocalSuppressionFilter.SuppressedCount,
				"suppressedAccountIds": initialLocalSuppressionFilter.SuppressedAccountIDs,
				"allSuppressed":        true,
				"nextRetryAfterMs":     initialLocalSuppressionFilter.NextRetryAfterMs,
				"fallbackAttempted":    true,
			})
			return PreparationResult{Outcome: PreparationOutcomeFallback, Reason: "local_account_suppressed", Context: fallbackContext}, nil
		}
		precheckHalfOpenEligible = initialLocalSuppressionFilter.PrecheckSuppressedAccountIDs != nil &&
			len(initialLocalSuppressionFilter.PrecheckSuppressedAccountIDs) == len(orderedCandidateAccounts) &&
			len(initialLocalSuppressionFilter.ConfiguredPolicySuppressedAccountIDs) == 0
	}

	var localSuppressionFilter *SuppressionFilterResult
	if bypassLocalSuppression {
		bypassed := localSuppressionBypassResult(orderedCandidateAccounts)
		localSuppressionFilter = &bypassed
	} else if precheckHalfOpenEligible {
		bypassed := localSuppressionBypassResult(orderedCandidateAccounts)
		localSuppressionFilter = &bypassed
	} else {
		resolved, completed, err := e.Suppression.ResolveLocalSuppressionFilter(ctx, LocalSuppressionPreflightInput{
			Req:                      input.Req,
			UsageContext:             input.UsageContext,
			AuditCapture:             p.engine.auditCaptureOf(input.AuditCapture),
			StartedAt:                input.StartedAt,
			Accounts:                 orderedCandidateAccounts,
			SystemAccountID:          input.SystemAccountID,
			APIKeyID:                 input.APIKeyID,
			GroupID:                  input.GroupID,
			ServerRetryBudget:        input.ServerRetryBudget,
			RouteCoordinationBudget:  input.RouteCoordinationBudget,
			GatewayRequestWallBudget: input.GatewayRequestWallBudget,
			RouteCoordinator:         input.RouteCoordinator,
			Signal:                   input.Signal,
		})
		if err != nil {
			return PreparationResult{}, err
		}
		if completed {
			return PreparationResult{Outcome: PreparationOutcomeCompleted}, nil
		}
		localSuppressionFilter = resolved
	}
	if localSuppressionFilter == nil {
		return PreparationResult{Outcome: PreparationOutcomeCompleted}, nil
	}

	modelRankByAccountID := modelPriorityRankMap(input.ModelPriority)
	runtimeDegradationOrder := e.Degradation.OrderGatewayAccountsByRuntimeDegradation(localSuppressionFilter.Accounts, modelRankByAccountID)
	if runtimeDegradationOrder.Applied || runtimeDegradationOrder.BypassedAllDegraded {
		input.AuditCapture.AddGatewayMetadata("runtime_account_degradation", map[string]any{
			"applied":             runtimeDegradationOrder.Applied,
			"degradedCount":       runtimeDegradationOrder.DegradedCount,
			"degradedAccountIds":  runtimeDegradationOrder.DegradedAccountIDs,
			"bypassedAllDegraded": runtimeDegradationOrder.BypassedAllDegraded,
		})
	}
	if runtimeDegradationOrder.BypassedAllDegraded {
		fallbackAttempted, fallbackContext, err := p.requestRouteFallback(ctx, input, orderedCandidateAccounts, "runtime_degraded")
		if err != nil {
			return PreparationResult{}, err
		}
		if fallbackAttempted {
			return PreparationResult{Outcome: PreparationOutcomeFallback, Reason: "runtime_degraded", Context: fallbackContext}, nil
		}
	}

	latencyDegradationOrder, err := e.Latency.OrderAsync(ctx, runtimeDegradationOrder.Accounts, &LatencyScopeInput{
		SystemAccountID: input.SystemAccountID,
		RouteStrategyID: input.RouteStrategyID,
		GroupID:         input.GroupID,
	}, input.NormalRouteSpeedFirstConfig, input.ModelPriority)
	if err != nil {
		return PreparationResult{}, err
	}
	if latencyDegradationOrder.Applied || latencyDegradationOrder.BypassedAllDegraded {
		input.AuditCapture.AddGatewayMetadata("normal_route_latency_degradation", map[string]any{
			"applied":             latencyDegradationOrder.Applied,
			"degradedAccountIds":  latencyDegradationOrder.DegradedAccountIDs,
			"bypassedAllDegraded": latencyDegradationOrder.BypassedAllDegraded,
		})
	}

	proxyHealthOrder, err := e.ProxyHealth.OrderAsync(ctx, latencyDegradationOrder.Accounts, input.ModelPriority)
	if err != nil {
		return PreparationResult{}, err
	}
	if proxyHealthOrder.Applied || proxyHealthOrder.BypassedAllAvoided {
		input.AuditCapture.AddGatewayMetadata("upstream_bucket_health_avoidance", map[string]any{
			"applied":             proxyHealthOrder.Applied,
			"avoidedBucketKeys":   proxyHealthOrder.AvoidedBucketKeys,
			"avoidedProxyKeys":    proxyHealthOrder.AvoidedProxyKeys,
			"avoidedAccountIds":   proxyHealthOrder.AvoidedAccountIDs,
			"halfOpenBucketKeys":  proxyHealthOrder.HalfOpenBucketKeys,
			"halfOpenAccountIds":  proxyHealthOrder.HalfOpenAccountIDs,
			"bypassedAllAvoided":  proxyHealthOrder.BypassedAllAvoided,
		})
	}

	clientIpAccountAvoidance, err := e.ClientIPAvoidance.OrderAsync(ctx, proxyHealthOrder.Accounts, ClientIPAvoidanceScope{
		SystemAccountID: input.SystemAccountID,
		APIKeyID:        input.APIKeyID,
		GroupID:         input.GroupID,
		ClientIP:        input.ClientIP,
	}, input.ModelPriority)
	if err != nil {
		return PreparationResult{}, err
	}
	if clientIpAccountAvoidance.Applied || clientIpAccountAvoidance.BypassedAllAvoided {
		input.AuditCapture.AddGatewayMetadata("client_ip_account_avoidance", map[string]any{
			"applied":             clientIpAccountAvoidance.Applied,
			"avoidedAccountIds":   clientIpAccountAvoidance.AvoidedAccountIDs,
			"bypassedAllAvoided":  clientIpAccountAvoidance.BypassedAllAvoided,
		})
	}

	clientSourceAvoidance, err := e.ClientSourceAvoidance.OrderAsync(ctx, clientIpAccountAvoidance.Accounts, input.ClientStrategy, input.ModelPriority)
	if err != nil {
		return PreparationResult{}, err
	}
	if clientSourceAvoidance.Applied || clientSourceAvoidance.BypassedAllAvoided {
		input.AuditCapture.AddGatewayMetadata("client_source_account_avoidance", map[string]any{
			"applied":             clientSourceAvoidance.Applied,
			"failureCount":        clientSourceAvoidance.FailureCount,
			"avoidedAccountIds":   clientSourceAvoidance.AvoidedAccountIDs,
			"bypassedAllAvoided":  clientSourceAvoidance.BypassedAllAvoided,
		})
	}

	latencyDegradedAccountIDs := make(map[string]struct{}, len(latencyDegradationOrder.DegradedAccountIDs))
	for _, id := range latencyDegradationOrder.DegradedAccountIDs {
		latencyDegradedAccountIDs[id] = struct{}{}
	}
	readyPreparation, err := p.prepareQuotaAndCapacityReadyAccounts(ctx, quotaCapacityInput{
		input:                        input,
		accounts:                     clientSourceAvoidance.Accounts,
		dispatchOrderingOptions:      dispatchOrderingOptions,
		latencyDegradedAccountIDs:    latencyDegradedAccountIDs,
		hotQualityMode:               hotQualityModeFor(input.NormalRouteSpeedFirstConfig),
		eligibleFirstPrimaryDispatch: input.UsageContext.TrafficSource == "gateway",
	})
	if err != nil {
		return PreparationResult{}, err
	}
	if readyPreparation.Outcome != PreparationOutcomeReady {
		return readyPreparation, nil
	}
	readyAccounts := readyPreparation.Accounts

	// Session affinity claim (Node: claimOpenAIAccountForSessionAsync).
	{
		accountsBeforeSessionAffinityClaim := readyAccounts
		proposedAccountID := ""
		if len(readyAccounts) > 0 {
			proposedAccountID = readyAccounts[0].ID
		}
		claimedAccountID := ""
		if proposedAccountID != "" {
			claimedAccountID, _ = e.Affinity.ClaimAsync(ctx, input.SessionAffinityKey, proposedAccountID, AffinityScope{
				SystemAccountID: input.SystemAccountID,
				APIKeyID:        input.APIKeyID,
				GroupID:         input.GroupID,
			})
		}
		containsClaimed := false
		for _, account := range readyAccounts {
			if account.ID == claimedAccountID {
				containsClaimed = true
				break
			}
		}
		if claimedAccountID != "" && claimedAccountID != proposedAccountID && containsClaimed {
			readyAccounts, err = e.Affinity.OrderAsync(ctx, readyAccounts, input.SessionAffinityKey, dispatchOrderingOptions)
			if err != nil {
				readyPreparation.ReleaseClientIPConcurrency()
				if readyPreparation.SettleHotQualityExplorationAfterDispatch != nil {
					_ = readyPreparation.SettleHotQualityExplorationAfterDispatch(ctx, "not_dispatched")
				}
				return PreparationResult{}, err
			}
			_ = accountsBeforeSessionAffinityClaim
		}
		if input.SessionAffinityKey != "" && proposedAccountID != "" {
			winnerAvailable := false
			if claimedAccountID != "" {
				for _, account := range readyAccounts {
					if account.ID == claimedAccountID {
						winnerAvailable = true
						break
					}
				}
			}
			applied := len(readyAccounts) > 0 && readyAccounts[0].ID == claimedAccountID
			input.AuditCapture.AddGatewayMetadata("session_affinity_claim", map[string]any{
				"proposedAccountId": proposedAccountID,
				"claimedAccountId":  claimedAccountID,
				"winnerAvailable":   winnerAvailable,
				"applied":           applied,
			})
		}
	}

	readyPreparation.Accounts = readyAccounts
	readyPreparation.PrecheckHalfOpenEligible = precheckHalfOpenEligible
	readyPreparation.NormalRouteLatencyDegradationApplied = latencyDegradationOrder.Applied
	readyPreparation.CodexTurnAccountAvoidanceApplied = clientSourceAvoidance.ThresholdReached
	readyPreparation.CodexTurnAvoidedAccountIDs = clientSourceAvoidance.AvoidedAccountIDs
	return readyPreparation, nil
}

// quotaCapacityInput mirrors prepareQuotaAndCapacityReadyAccounts' input.
type quotaCapacityInput struct {
	input                        gatewaypreauth.DispatchPreparationInput
	accounts                     []AccountCandidate
	dispatchOrderingOptions      AffinityOrderingOptions
	latencyDegradedAccountIDs    map[string]struct{}
	hotQualityMode               string
	eligibleFirstPrimaryDispatch bool
}

func hotQualityModeFor(config *gatewaypreauth.NormalRouteSpeedFirstRuntimeConfig) string {
	if config != nil {
		return HotQualityModeSpeedFirst
	}
	return HotQualityModeCostFirst
}

func groupTypeOf(groupAccess gatewayruntimecache.GroupUsageAccessMetadata) string {
	if groupAccess.GroupType == nil {
		return ""
	}
	return *groupAccess.GroupType
}

func isAccountProbeTrafficSource(trafficSource string) bool {
	// usage/traffic-source.ts isAccountProbeTrafficSource.
	return trafficSource == "probe" || trafficSource == "health_check"
}

// prepareQuotaAndCapacityReadyAccounts mirrors
// prepareQuotaAndCapacityReadyAccounts.
func (p *CandidatePipeline) prepareQuotaAndCapacityReadyAccounts(ctx context.Context, input quotaCapacityInput) (PreparationResult, error) {
	e := p.engine
	req := input.input
	var authorizationQuotaDeniedAccountCount int
	accounts := []AccountCandidate{}
	var hotQualityExplorationReservation *HotQualityReservation
	var settleHotQualityExplorationAfterDispatch func(ctx context.Context, outcome string) error

	accountQuotaDecisions, err := e.Quota.CheckBatchAsync(ctx, req.GroupAccess, input.accounts)
	if err != nil {
		return PreparationResult{}, err
	}
	for _, account := range input.accounts {
		decision, ok := accountQuotaDecisions[account.ID]
		if ok && !decision.Allowed {
			authorizationQuotaDeniedAccountCount++
			continue
		}
		accounts = append(accounts, account)
	}

	// capacity.account_snapshot: high-concurrency groups refresh the
	// concurrency snapshot.
	if input.dispatchOrderingOptions.GroupType == gatewayruntimecacheGroupTypeHighConcurrency {
		accounts, err = RefreshGatewayAccountCurrentConcurrencyAsync(ctx, e.Concurrency, accounts)
		if err != nil {
			return PreparationResult{}, err
		}
	}

	if len(accounts) == 0 {
		if authorizationQuotaDeniedAccountCount > 0 {
			fallbackAttempted, fallbackContext, err := p.requestRouteFallback(ctx, req, accounts, "authorization_quota_exceeded")
			if err != nil {
				return PreparationResult{}, err
			}
			if fallbackAttempted {
				return PreparationResult{Outcome: PreparationOutcomeFallback, Reason: "authorization_quota_exceeded", Context: fallbackContext}, nil
			}
			if err := req.RouteCoordinator.CompleteFailure(ctx, gatewayrouting.GatewayRouteFinalFailure{
				StatusCode: 429,
				Message:    gatewayquotaAuthorizationQuotaExceededMessage,
				ErrorType:  "rate_limit_exceeded",
				ErrorCode:  "rate_limit_exceeded",
				ErrorPhase: "quota",
			}); err != nil {
				return PreparationResult{}, err
			}
			return PreparationResult{Outcome: PreparationOutcomeCompleted}, nil
		}
		if err := req.RouteCoordinator.CompleteFailure(ctx, gatewayrouting.GatewayRouteFinalFailure{
			StatusCode: 503,
			Message:    "没有可用的上游账户",
			ErrorType:  "service_unavailable",
			ErrorCode:  "no_available_upstream_account",
			ErrorPhase: "dispatch",
		}); err != nil {
			return PreparationResult{}, err
		}
		return PreparationResult{Outcome: PreparationOutcomeCompleted}, nil
	}

	busyOptions := HighConcurrencyBusyOptions{AffinityOrderingOptions: input.dispatchOrderingOptions, RequestLane: req.RequestLane}

	highConcurrencyBusy, err := e.Affinity.AreHighConcurrencyAccountsBusyForLaneAsync(ctx, accounts, busyOptions)
	if err != nil {
		return PreparationResult{}, err
	}
	if highConcurrencyBusy {
		refreshed, err := RefreshGatewayAccountCurrentConcurrencyAsync(ctx, e.Concurrency, accounts)
		if err != nil {
			return PreparationResult{}, err
		}
		accounts, err = e.Affinity.OrderAsync(ctx, refreshed, req.SessionAffinityKey, input.dispatchOrderingOptions)
		if err != nil {
			return PreparationResult{}, err
		}
	}

	applyHotQualityOrder := func() error {
		hotQualityOrder, err := e.HotQuality.OrderAsync(ctx, HotQualityOrderInput{
			Accounts:                     accounts,
			ModelPriority:                req.ModelPriority,
			Mode:                         input.hotQualityMode,
			SystemAccountID:              req.SystemAccountID,
			RouteStrategyID:              req.RouteStrategyID,
			GroupID:                      req.GroupID,
			RequestLane:                  req.RequestLane,
			Model:                        requestModelOrEmpty(req.Req),
			RequestID:                    req.UsageContext.TraceID,
			LatencyDegradedAccountIDs:    input.latencyDegradedAccountIDs,
			EligibleFirstPrimaryDispatch: input.eligibleFirstPrimaryDispatch,
		})
		if err != nil {
			return err
		}
		accounts = hotQualityOrder.Accounts
		if input.hotQualityMode == HotQualityModeSpeedFirst && len(input.latencyDegradedAccountIDs) > 0 {
			healthy := make([]AccountCandidate, 0, len(accounts))
			degraded := make([]AccountCandidate, 0, len(accounts))
			for _, account := range accounts {
				if _, isDegraded := input.latencyDegradedAccountIDs[account.ID]; isDegraded {
					degraded = append(degraded, account)
				} else {
					healthy = append(healthy, account)
				}
			}
			accounts = append(append([]AccountCandidate{}, healthy...), degraded...)
		}
		hotQualityExplorationReservation = hotQualityOrder.ExplorationReservation
		settleHotQualityExplorationAfterDispatch = hotQualityOrder.SettleExplorationAfterDispatch
		if hotQualityOrder.DispatchIntent == "same_tier_exploration" || len(hotQualityOrder.QualityReorderedTierKeys) > 0 {
			req.AuditCapture.AddGatewayMetadata("hot_quality_candidate_selection", map[string]any{
				"dispatchIntent":                 hotQualityOrder.DispatchIntent,
				"selectedAccountId":              hotQualityOrder.SelectedAccountID,
				"explorationStatus":              hotQualityOrder.ExplorationStatus,
				"qualityReorderedTierKeys":       hotQualityOrder.QualityReorderedTierKeys,
				"latencyDegradedOverrideApplied": hotQualityOrder.LatencyDegradedOverrideApplied,
			})
		}
		return nil
	}

	if input.dispatchOrderingOptions.GroupType == gatewayruntimecacheGroupTypeHighConcurrency {
		if err := applyHotQualityOrder(); err != nil {
			return PreparationResult{}, err
		}
	}

	highConcurrencyBusy, err = e.Affinity.AreHighConcurrencyAccountsBusyForLaneAsync(ctx, accounts, busyOptions)
	if err != nil {
		return PreparationResult{}, err
	}
	if highConcurrencyBusy {
		fallbackAttempted, fallbackContext, err := p.requestRouteFallback(ctx, req, accounts, "high_concurrency_group_busy")
		if err != nil {
			return PreparationResult{}, err
		}
		if fallbackAttempted {
			return PreparationResult{Outcome: PreparationOutcomeFallback, Reason: "high_concurrency_group_busy", Context: fallbackContext}, nil
		}
	}

	if input.dispatchOrderingOptions.GroupType != gatewayruntimecacheGroupTypeHighConcurrency {
		capacityBusy, err := AreGatewayAccountsCapacityBusyForLaneAsync(ctx, e.Concurrency, accounts, gatewayprotoLane(req.RequestLane), req.GroupAccess.SchedulingPolicy)
		if err != nil {
			return PreparationResult{}, err
		}
		if capacityBusy {
			fallbackAttempted, fallbackContext, err := p.requestRouteFallback(ctx, req, accounts, "group_capacity_busy")
			if err != nil {
				return PreparationResult{}, err
			}
			if fallbackAttempted {
				return PreparationResult{Outcome: PreparationOutcomeFallback, Reason: "group_capacity_busy", Context: fallbackContext}, nil
			}
		}
		accounts, err = OrderGatewayAccountsByLaneCapacityAvailabilityAsync(ctx, e.Concurrency, accounts, gatewayprotoLane(req.RequestLane), req.GroupAccess.SchedulingPolicy, req.ModelPriority)
		if err != nil {
			return PreparationResult{}, err
		}
		if err := applyHotQualityOrder(); err != nil {
			return PreparationResult{}, err
		}
		// Keep busy candidates in the dispatch context (Node parity comment:
		// the upstream dispatcher owns bounded capacity waiting).
	}

	var releaseClientIPConcurrency func() = noopRelease
	var releaseOnce sync.Once
	releaseClientIPConcurrencyOnce := func() {
		releaseOnce.Do(func() {
			releaseClientIPConcurrency()
		})
	}

	fail := func(err error) (PreparationResult, error) {
		releaseClientIPConcurrencyOnce()
		return PreparationResult{}, err
	}

	if input.dispatchOrderingOptions.GroupType == gatewayruntimecacheGroupTypeHighConcurrency {
		clientIpConcurrency, err := e.ClientIPConcurrency.Acquire(ctx, ClientIPConcurrencyInput{
			SystemAccountID: req.SystemAccountID,
			GroupID:         req.GroupID,
			APIKeyID:        req.APIKeyID,
			ClientIP:        req.ClientIP,
			Policy:          req.GroupAccess.SchedulingPolicy,
			Signal:          req.Signal,
		})
		if err != nil {
			return PreparationResult{}, err
		}
		if clientIpConcurrency.Acquired && clientIpConcurrency.Release != nil {
			releaseClientIPConcurrency = clientIpConcurrency.Release
		}
		if clientIpConcurrency.Enabled {
			req.AuditCapture.AddGatewayMetadata("high_concurrency_client_ip", clientIpConcurrencyAuditMetadata(clientIpConcurrency))
		}
		if !clientIpConcurrency.Acquired {
			if signalAborted(req.Signal) || resWritableEnded(ctx) {
				return PreparationResult{Outcome: PreparationOutcomeCompleted}, nil
			}
			if err := req.RouteCoordinator.CompleteFailure(ctx, gatewayrouting.GatewayRouteFinalFailure{
				StatusCode:         429,
				Message:            clientIpConcurrencyFailureMessage(clientIpConcurrency),
				ErrorType:          "rate_limit_exceeded",
				ErrorCode:          "rate_limit_exceeded",
				ErrorPhase:         "dispatch",
				FailureAttribution: "gateway_capacity",
			}); err != nil {
				return PreparationResult{}, err
			}
			return PreparationResult{Outcome: PreparationOutcomeCompleted}, nil
		}
		if signalAborted(req.Signal) || resWritableEnded(ctx) {
			releaseClientIPConcurrencyOnce()
			return PreparationResult{Outcome: PreparationOutcomeCompleted}, nil
		}
	}

	highConcurrencyBusy, err = e.Affinity.AreHighConcurrencyAccountsBusyForLaneAsync(ctx, accounts, busyOptions)
	if err != nil {
		return fail(err)
	}
	if highConcurrencyBusy {
		queueWaitStartedAtMs := NowMs()
		req.ServerRetryBudget.BeginNoAvailableWait(&queueWaitStartedAtMs)
		queueWait, waitErr := func() (QueueWaitResult, error) {
			defer req.ServerRetryBudget.PauseNoAvailableWait(&queueWaitStartedAtMs)
			serverRetryRemainingMs := req.ServerRetryBudget.RemainingMs(&queueWaitStartedAtMs)
			availableDecisionMs, err := req.GatewayRequestWallBudget.AvailableDecisionMs(gatewayrouting.GatewayRequestWallBudgetDecision{NowMs: &queueWaitStartedAtMs})
			if err != nil {
				return QueueWaitResult{}, err
			}
			maxWaitMs := serverRetryRemainingMs
			if req.RequestLane != "image" && availableDecisionMs < maxWaitMs {
				maxWaitMs = availableDecisionMs
			}
			return e.HighConcurrencyQueue.WaitForCapacity(ctx, HighConcurrencyWaitInput{
				SystemAccountID:          req.SystemAccountID,
				GroupID:                  req.GroupID,
				APIKeyID:                 req.APIKeyID,
				AccountIDs:               gatewaySessionConcurrencyIDs(accounts),
				AccountConcurrencyLimits: GatewayAccountConcurrencyLimitsByAccountID(accounts),
				Lane:                     req.RequestLane,
				Policy:                   req.GroupAccess.SchedulingPolicy,
				MaxWaitMs:                maxWaitMs,
			})
		}()
		if waitErr != nil {
			return fail(waitErr)
		}
		req.AuditCapture.AddGatewayMetadata("high_concurrency_group_queue", map[string]any{
			"ready":     queueWait.Ready,
			"reason":    queueWait.Reason,
			"waitedMs":  queueWait.WaitedMs,
			"queueSize": queueWait.QueueSize,
			"lane":      req.RequestLane,
		})
		if signalAborted(req.Signal) || resWritableEnded(ctx) {
			releaseClientIPConcurrencyOnce()
			return PreparationResult{Outcome: PreparationOutcomeCompleted}, nil
		}
		refreshed, err := RefreshGatewayAccountCurrentConcurrencyAsync(ctx, e.Concurrency, accounts)
		if err != nil {
			return fail(err)
		}
		accounts, err = e.Affinity.OrderAsync(ctx, refreshed, req.SessionAffinityKey, input.dispatchOrderingOptions)
		if err != nil {
			return fail(err)
		}
	}

	highConcurrencyBusy, err = e.Affinity.AreHighConcurrencyAccountsBusyForLaneAsync(ctx, accounts, busyOptions)
	if err != nil {
		return fail(err)
	}
	if highConcurrencyBusy {
		fallbackAttempted, fallbackContext, err := p.requestRouteFallback(ctx, req, accounts, "high_concurrency_group_busy")
		if err != nil {
			return fail(err)
		}
		if fallbackAttempted {
			releaseClientIPConcurrencyOnce()
			return PreparationResult{Outcome: PreparationOutcomeFallback, Reason: "high_concurrency_group_busy", Context: fallbackContext}, nil
		}
		releaseClientIPConcurrencyOnce()
		if err := req.RouteCoordinator.CompleteFailure(ctx, gatewayrouting.GatewayRouteFinalFailure{
			StatusCode:         429,
			Message:            "分组繁忙，请稍后重试",
			ErrorType:          "rate_limit_exceeded",
			ErrorCode:          "rate_limit_exceeded",
			ErrorPhase:         "dispatch",
			FailureAttribution: "gateway_capacity",
		}); err != nil {
			return PreparationResult{}, err
		}
		return PreparationResult{Outcome: PreparationOutcomeCompleted}, nil
	}

	return PreparationResult{
		Outcome:                                  PreparationOutcomeReady,
		Accounts:                                 accounts,
		ReleaseClientIPConcurrency:               releaseClientIPConcurrencyOnce,
		HotQualityExplorationReservation:         hotQualityExplorationReservation,
		SettleHotQualityExplorationAfterDispatch: settleHotQualityExplorationAfterDispatch,
	}, nil
}

func clientIpConcurrencyAuditMetadata(decision ClientIPConcurrencyDecision) map[string]any {
	if !decision.Enabled {
		return map[string]any{"enabled": false}
	}
	if decision.Acquired {
		return map[string]any{
			"enabled":                true,
			"acquired":               true,
			"current":                decision.Current,
			"limit":                  decision.Limit,
			"waitedMs":               decision.WaitedMs,
			"queued":                 decision.Queued,
			"queueSizeBeforeAcquire": decision.QueueSizeBeforeAcquire,
		}
	}
	return map[string]any{
		"enabled":   true,
		"acquired":  false,
		"reason":    decision.Reason,
		"current":   decision.Current,
		"limit":     decision.Limit,
		"waitedMs":  decision.WaitedMs,
		"queueSize": decision.QueueSize,
	}
}

func clientIpConcurrencyFailureMessage(decision ClientIPConcurrencyDecision) string {
	if decision.Enabled && !decision.Acquired && decision.Reason == "timeout" {
		return "当前 IP 并发排队等待超时，请稍后重试"
	}
	return "当前 IP 并发已达到分组限制，请稍后重试"
}

func noopRelease() {}

func signalAborted(signal context.Context) bool {
	return signal != nil && signal.Err() != nil
}

// resWritableEnded mirrors input.res.writableEnded: the Go response writer
// closure is injected on the engine (G20 wires the downstream writer state).
var resWritableEnded = func(ctx context.Context) bool { return false }

func requestModelOrEmpty(req *gatewaypreauth.GatewayRequest) string {
	if model, ok := gatewaypreauth.RequestModel(req); ok {
		return model
	}
	return ""
}

// gatewayruntimecacheGroupTypeHighConcurrency mirrors groupType value.
const gatewayruntimecacheGroupTypeHighConcurrency = "high_concurrency"

// gatewayquotaAuthorizationQuotaExceededMessage mirrors
// AUTHORIZATION_QUOTA_EXCEEDED_MESSAGE.
const gatewayquotaAuthorizationQuotaExceededMessage = "额度已用完，请联系管理员提升额度"
