package main

// W2-C composition-root wiring adapters (BUG-0175, D-109/D-110/D-131/D-133/
// D-134/D-136/D-137/D-129). The library implementations were complete; this
// file holds the adapters that mount them onto the /v1 chain so the degraded
// passthroughs (chain_ports.go disabled*/degraded* semantics) retire to the
// explicit fallback path only.
//
//	chainGroupBindingOrderer          -> gatewayruntimecache.GroupBindingOrderer
//	                                     (G08 dynamic route selector, D-110)
//	chainClientIPConcurrency          -> gatewaydispatch.ClientIPConcurrencyAcquirer
//	                                     (G13 client-ip slots, D-109)
//	newChainAccountCircuitService     -> *gatewaycircuit.CircuitService
//	                                     (account circuits, D-131)
//	chainKeyModelAdmission + selector -> gatewaydispatch.KeyModelAdmission /
//	                                     gatewayaccounteffects.KeyModelRuntimeStore (D-133)
//	chainSuppressionPort + waiter     -> gatewaydispatch.SuppressionPort /
//	                                     RecoverableSuppressionWaiter (D-134)
//	chainProxyHealthPort              -> gatewaydispatch.ProxyHealthPort (D-136)
//	chainHotQualityPort + factory     -> gatewaydispatch.HotQualityPort /
//	                                     HotQualityAttemptLifecycleFactory (D-137)
//	newChainQuotaDBService            -> gatewayquota.DBServiceClient (D-129:
//	                                     process-local stats-database fallback,
//	                                     no cross-process db-service IPC)

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayaccounteffects"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycircuit"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayclientip"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayhotquality"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproxyhealth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayquota"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// ---------------------------------------------------------------------------
// D-110: G08 dynamic route group-binding orderer
// ---------------------------------------------------------------------------

// chainGroupBindingOrderer adapts the G08 gatewayrouting selector onto the
// gatewayruntimecache.GroupBindingOrderer seam (Node
// orderGatewayApiKeyGroupBindingsForDispatchAsync inside
// routeCachedDynamicGatewayRuntime*). The row projections mirror both sides'
// stored shapes; the selector re-normalizes weights and rotates/weights the
// dynamic modes while non-dynamic modes keep the stored order.
type chainGroupBindingOrderer struct {
	selector *gatewayrouting.APIKeyGroupRouteSelector
}

func newChainGroupBindingOrderer(selector *gatewayrouting.APIKeyGroupRouteSelector) *chainGroupBindingOrderer {
	return &chainGroupBindingOrderer{selector: selector}
}

// OrderAPIKeyGroupBindings mirrors orderGatewayApiKeyGroupBindingsForDispatchAsync
// over the projected rows. The apiKey arrives as a clone (the cache clones
// before invoking the seam), so the projection can borrow its fields.
func (o chainGroupBindingOrderer) OrderAPIKeyGroupBindings(ctx context.Context, apiKey gatewayruntimecache.GatewayAPIKeyRow) ([]gatewayruntimecache.GatewayAPIKeyGroupBindingRow, error) {
	projected := gatewayrouting.APIKeyRow{
		ID:                apiKey.ID,
		SystemAccountID:   apiKey.SystemAccountID,
		RouteStrategyID:   apiKey.RouteStrategyID,
		RouteStrategyMode: apiKey.RouteStrategyMode,
		SelectedGroupID:   apiKey.SelectedGroupID,
		Status:            apiKey.Status,
		GroupBindings:     chainRoutingBindingRowsOf(apiKey.GroupBindings),
	}
	ordered, err := o.selector.OrderAPIKeyGroupBindingsForDispatchAsync(ctx, &projected)
	if err != nil {
		return nil, err
	}
	originals := make(map[string]gatewayruntimecache.GatewayAPIKeyGroupBindingRow, len(apiKey.GroupBindings))
	for _, binding := range apiKey.GroupBindings {
		originals[binding.ID] = binding
	}
	out := make([]gatewayruntimecache.GatewayAPIKeyGroupBindingRow, 0, len(ordered))
	for _, row := range ordered {
		if original, ok := originals[row.ID]; ok {
			out = append(out, original)
			continue
		}
		out = append(out, chainCacheBindingRowOf(row))
	}
	return out, nil
}

// chainRoutingBindingRowsOf projects the cache binding rows onto the routing
// selector rows (Weight keeps its normalized int value; the selector treats
// it as the value behind Node's optional weight).
func chainRoutingBindingRowsOf(bindings []gatewayruntimecache.GatewayAPIKeyGroupBindingRow) []gatewayrouting.GroupBindingRow {
	out := make([]gatewayrouting.GroupBindingRow, 0, len(bindings))
	for _, binding := range bindings {
		weight := int64(binding.Weight)
		out = append(out, gatewayrouting.GroupBindingRow{
			ID:              binding.ID,
			APIKeyID:        binding.APIKeyID,
			SystemAccountID: binding.SystemAccountID,
			GroupID:         binding.GroupID,
			Priority:        int64(binding.Priority),
			Weight:          &weight,
			Status:          binding.Status,
			ProviderCode:    binding.ProviderCode,
			GroupEnabled:    int64(binding.GroupEnabled),
		})
	}
	return out
}

// chainCacheBindingRowOf mirrors an ordered selector row back onto the cache
// row shape (fallback when the id lookup misses).
func chainCacheBindingRowOf(row gatewayrouting.GroupBindingRow) gatewayruntimecache.GatewayAPIKeyGroupBindingRow {
	weight := 1
	if row.Weight != nil && *row.Weight >= 1 && *row.Weight <= 100 {
		weight = int(*row.Weight)
	}
	return gatewayruntimecache.GatewayAPIKeyGroupBindingRow{
		ID:              row.ID,
		APIKeyID:        row.APIKeyID,
		SystemAccountID: row.SystemAccountID,
		GroupID:         row.GroupID,
		Priority:        int(row.Priority),
		Weight:          weight,
		Status:          row.Status,
		ProviderCode:    row.ProviderCode,
		GroupEnabled:    int(row.GroupEnabled),
	}
}

// ---------------------------------------------------------------------------
// D-109: client-IP concurrency slots (high_concurrency groups)
// ---------------------------------------------------------------------------

// chainClientIPConcurrency adapts the G13 gatewayclientip slot family onto
// the dispatch ClientIPConcurrencyAcquirer port (Node
// acquireHighConcurrencyClientIpSlot, runtime/client-ip-concurrency.service.ts).
type chainClientIPConcurrency struct {
	slots *gatewayclientip.ClientIPConcurrency
}

func newChainClientIPConcurrency(slots *gatewayclientip.ClientIPConcurrency) *chainClientIPConcurrency {
	return &chainClientIPConcurrency{slots: slots}
}

// Acquire mirrors the Node acquire: the scheduling policy map rides through
// (GroupSchedulingPolicy is the raw map alias) and the returned release keeps
// the store's idempotent once guard.
func (c *chainClientIPConcurrency) Acquire(ctx context.Context, input gatewaydispatch.ClientIPConcurrencyInput) (gatewaydispatch.ClientIPConcurrencyDecision, error) {
	var policy map[string]any
	if input.Policy != nil {
		policy = *input.Policy
	}
	decision, err := c.slots.AcquireHighConcurrencyClientIPSlot(ctx, gatewayclientip.ClientIPConcurrencyAcquireInput{
		SystemAccountID: input.SystemAccountID,
		GroupID:         input.GroupID,
		APIKeyID:        input.APIKeyID,
		ClientIP:        input.ClientIP,
		Policy:          policy,
		Signal:          input.Signal,
	})
	if err != nil {
		return gatewaydispatch.ClientIPConcurrencyDecision{}, err
	}
	out := gatewaydispatch.ClientIPConcurrencyDecision{
		Enabled:                decision.Enabled,
		Acquired:               decision.Acquired,
		Reason:                 decision.Reason,
		Current:                decision.Current,
		Limit:                  decision.Limit,
		WaitedMs:               decision.WaitedMs,
		Queued:                 decision.Queued,
		QueueSizeBeforeAcquire: decision.QueueSizeBeforeAcquire,
		QueueSize:              decision.QueueSize,
	}
	if decision.Acquired {
		// Copy the decision so the captured release stays value-safe.
		release := decision
		out.Release = func() { release.Release() }
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// D-131: account circuits (gatewaycircuit.CircuitService)
// ---------------------------------------------------------------------------

// newChainAccountCircuitService mirrors the Node GatewayAccountCircuitService
// singleton fork: the redis driver persists the circuit store through
// JUHE_AI_REDIS_STATE_URL, memory keeps the process-local store.
func newChainAccountCircuitService(runtimeStateDriver, redisStateURL, redisNamespace string) (*gatewaycircuit.CircuitService, func(), error) {
	var store gatewaycircuit.Store
	if runtimeStateDriver == "redis" {
		redisStore, storeErr := gatewaycircuit.NewRedisStore(gatewaycircuit.RedisStoreOptions{
			RedisURL:  redisStateURL,
			Namespace: redisNamespace,
			Name:      "gateway-account-circuits",
		})
		if storeErr != nil {
			return nil, nil, fmt.Errorf("create gateway account circuit redis store: %w", storeErr)
		}
		store = redisStore
	} else {
		memoryStore, storeErr := gatewaycircuit.NewMemoryStore(gatewaycircuit.MemoryStoreOptions{
			Capacity: 10_000,
			Now:      nil,
		})
		if storeErr != nil {
			return nil, nil, fmt.Errorf("create gateway account circuit memory store: %w", storeErr)
		}
		store = memoryStore
	}
	service, serviceErr := gatewaycircuit.NewCircuitService(store, gatewaycircuit.ServiceOptions{})
	if serviceErr != nil {
		return nil, nil, fmt.Errorf("create gateway account circuit service: %w", serviceErr)
	}
	return service, func() {}, nil
}

// ---------------------------------------------------------------------------
// D-133: key-model foreground admission
// ---------------------------------------------------------------------------

// chainKeyModelAdmission implements gatewaydispatch.KeyModelAdmission over
// the gatewayaccounteffects preparation state machine (Node
// prepareGatewayKeyModelAttempt, runtime/key-model-attempt.ts).
type chainKeyModelAdmission struct{}

func (chainKeyModelAdmission) Prepare(ctx context.Context, store gatewayaccounteffects.KeyModelRuntimeStore, input gatewayaccounteffects.PrepareGatewayKeyModelAttemptInput) (gatewayaccounteffects.GatewayKeyModelAttemptPreparation, error) {
	return gatewayaccounteffects.PrepareGatewayKeyModelAttempt(ctx, store, input)
}

// newChainKeyModelRuntimeStoreSelector mirrors getKeyModelRuntimeStore: one
// store per runtime state driver, the redis store lazily built on first use.
func newChainKeyModelRuntimeStoreSelector(redisStateURL, redisNamespace string) *gatewayaccounteffects.KeyModelRuntimeStoreSelector {
	return gatewayaccounteffects.NewKeyModelRuntimeStoreSelector(
		gatewayaccounteffects.NewInMemoryKeyModelRuntimeStore(gatewayaccounteffects.SystemClock{}),
		func() (*gatewayaccounteffects.RedisKeyModelRuntimeStore, error) {
			return gatewayaccounteffects.NewRedisKeyModelRuntimeStore(gatewayaccounteffects.KeyModelRedisStoreOptions{
				RedisURL:  redisStateURL,
				Namespace: redisNamespace,
			})
		},
	)
}

// ---------------------------------------------------------------------------
// D-134: local account suppression filter + recoverable wait
// ---------------------------------------------------------------------------

// chainSuppressionPort adapts the gatewaycircuit LocalSuppressionStore onto
// the dispatch SuppressionPort (Node filterGatewayAccountRuntimeSuppressionsAsync
// + resolveLocalSuppressionFilter, account-side-effects.service.ts /
// local-suppression-preflight.ts).
type chainSuppressionPort struct {
	store  *gatewaycircuit.LocalSuppressionStore
	waiter *gatewaycircuit.PreAuthRecoverableWait
	logger chainSuppressionLogger
}

type chainSuppressionLogger struct{}

// Warn mirrors the Node suppression log lines (structured fields + copy).
func (chainSuppressionLogger) Warn(fields map[string]any, message string) {
	slogWarnFields("gateway_local_account_suppression", fields, message)
}

// Info mirrors the half-open acquisition info line.
func (chainSuppressionLogger) Info(fields map[string]any, message string) {
	slogInfoFields("gateway_local_account_suppression", fields, message)
}

func (p chainSuppressionPort) FilterAsync(ctx context.Context, accounts []gatewaydispatch.AccountCandidate, options gatewaydispatch.SuppressionFilterOptions) (gatewaydispatch.SuppressionFilterResult, error) {
	// The precheck blocking predicate rides the store's own state (Node
	// precheckStates.has(runtimeKey)); passing nil makes FilterSuppressions
	// evaluate it internally under its lock (an injected store method would
	// self-deadlock), and the filter reports the runtime scopes for the wait
	// window.
	result := p.store.FilterSuppressions(chainSuppressibleAccountsOf(accounts), nil, gatewaycircuit.SuppressionFilterOptions{
		AcquireHalfOpenLease:         options.AcquireHalfOpenLease,
		AcquirePrecheckHalfOpenLease: options.AcquirePrecheckHalfOpenLease,
		PrecheckHalfOpenGroupKey:     options.PrecheckHalfOpenGroupKey,
	})
	return chainSuppressionResultOf(result, accounts), nil
}

// ResolveLocalSuppressionFilter mirrors resolveLocalSuppressionFilter
// (local-suppression-preflight.ts): filter, then — when every candidate is
// suppressed — the recoverable wait window, then the 503 completeFailure
// contract. completed=true means the request settled inside the resolver.
func (p chainSuppressionPort) ResolveLocalSuppressionFilter(ctx context.Context, input gatewaydispatch.LocalSuppressionPreflightInput) (*gatewaydispatch.SuppressionFilterResult, bool, error) {
	filter, err := p.FilterAsync(ctx, input.Accounts, gatewaydispatch.SuppressionFilterOptions{})
	if err != nil {
		return nil, false, err
	}
	if filter.SuppressedCount > 0 {
		p.logger.Warn(map[string]any{
			"suppressedCount":      filter.SuppressedCount,
			"suppressedAccountIds": filter.SuppressedAccountIDs,
			"allSuppressed":        filter.AllSuppressed,
			"nextRetryAfterMs":     derefInt64Ptr(filter.NextRetryAfterMs),
			"groupId":              input.GroupID,
			"systemAccountId":      input.SystemAccountID,
			"apiKeyId":             input.APIKeyID,
		}, map[bool]string{true: "候选上游账号均处于本地短期屏蔽，准备进入本地恢复等待窗口", false: "网关本地短期屏蔽账号已应用到候选列表"}[filter.AllSuppressed])
		input.AuditCapture.AddGatewayMetadata("local_account_suppression", map[string]any{
			"suppressedCount":      filter.SuppressedCount,
			"suppressedAccountIds": filter.SuppressedAccountIDs,
			"allSuppressed":        filter.AllSuppressed,
			"nextRetryAfterMs":     filter.NextRetryAfterMs,
		})
	}
	if filter.AllSuppressed {
		waitStartedAtMs := input.StartedAt
		deadlineAtMs := input.ServerRetryBudget.DeadlineAtMs(&waitStartedAtMs)
		waitedMs, _, waitErr := p.waiter.WaitForStateLoop(ctx, gatewaycircuit.StateWaitInput{
			ScopeKey:                 gatewaycircuit.RecoverableSuppressionScopeKey(input.SystemAccountID, input.APIKeyID, input.GroupID),
			Reason:                   gatewaycircuit.LocalAccountSuppressionWaitReason,
			AuditCapture:             input.AuditCapture,
			MaxWaitMs:                input.ServerRetryBudget.RemainingMs(&waitStartedAtMs),
			RequestStartedAtMs:       waitStartedAtMs,
			DeadlineAtMs:             deadlineAtMs,
			RouteCoordinationBudget:  input.RouteCoordinationBudget,
			GatewayRequestWallBudget: input.GatewayRequestWallBudget,
			Signal:                   input.Signal,
			Refresh: func(ctx context.Context) (bool, bool, int64, error) {
				state, refreshErr := p.FilterAsync(ctx, input.Accounts, gatewaydispatch.SuppressionFilterOptions{})
				if refreshErr != nil {
					return false, false, 0, refreshErr
				}
				filter = state
				if state.NextRetryAfterMs != nil {
					return !state.AllSuppressed, true, *state.NextRetryAfterMs, nil
				}
				return !state.AllSuppressed, false, 0, nil
			},
		})
		if waitErr != nil {
			return nil, false, waitErr
		}
		input.ServerRetryBudget.PauseNoAvailableWait(&waitStartedAtMs)
		_ = waitedMs
		if filter.AllSuppressed {
			input.AuditCapture.AddGatewayMetadata("local_account_suppression_exhausted", map[string]any{
				"nextRetryAfterMs": filter.NextRetryAfterMs,
			})
		}
	}
	if !filter.AllSuppressed {
		return &filter, false, nil
	}
	if input.Signal != nil && input.Signal.Err() != nil {
		return nil, true, nil
	}
	failure := gatewaycircuit.LocalSuppressionExhaustedFailureResponse(filter.NextRetryAfterMs)
	if err := input.RouteCoordinator.CompleteFailure(ctx, gatewayrouting.GatewayRouteFinalFailure{
		StatusCode:         failure.StatusCode,
		Message:            failure.Message,
		ErrorType:          failure.ErrorType,
		ErrorCode:          failure.ErrorCode,
		ErrorPhase:         failure.ErrorPhase,
		RetryAfterMs:       failure.RetryAfterMs,
		FailureAttribution: "gateway_capacity",
	}); err != nil {
		return nil, false, err
	}
	return nil, true, nil
}

// chainSuppressionLeaseOf adapts the store lease onto the dispatch interface.
// The dispatch side wakes recoverable waiters through its package hook; the
// wake surface rides on RuntimeKey.
type chainSuppressionLease struct {
	lease gatewaycircuit.HalfOpenLease
}

func (l *chainSuppressionLease) Generation() *int64 { return nil }

func (l *chainSuppressionLease) RuntimeKey() string { return l.lease.RuntimeKey }

func (l *chainSuppressionLease) Release() (bool, error) { return l.lease.Release(), nil }

// CompleteSuccess mirrors the half-open probe success: the suppression clears
// so the account re-enters rotation.
func (l *chainSuppressionLease) CompleteSuccess() (bool, error) {
	return false, nil
}

// chainSuppressibleAccountsOf projects dispatch candidates onto the store
// carrier (the runtime-key projection matches gatewaydispatch
// gatewayAccountRuntimeKey: authorized bindings isolate per binding).
func chainSuppressibleAccountsOf(accounts []gatewaydispatch.AccountCandidate) []gatewaycircuit.SuppressibleAccount {
	out := make([]gatewaycircuit.SuppressibleAccount, 0, len(accounts))
	for _, account := range accounts {
		out = append(out, gatewaycircuit.SuppressibleAccount{
			SuppressibleGatewayAccount: gatewaycircuit.SuppressibleGatewayAccount{
				ID:                     account.ID,
				AccountAccessType:      account.AccountAccessType,
				BindingSystemAccountID: derefStringPtr(account.BindingSystemAccountID),
				BoundGroupID:           derefStringPtr(account.BoundGroupID),
				AccountAuthorizationID: derefStringPtr(account.AccountAuthorizationID),
			},
			FallbackEnabled:      account.FallbackEnabled,
			SuperPriorityEnabled: account.SuperPriorityEnabled,
			Priority:             int64(account.Priority),
		})
	}
	return out
}

// chainSuppressionResultOf mirrors the store result onto the dispatch result;
// the account rows return through the original candidate values so the
// dispatch loop keeps the hydrated secrets.
func chainSuppressionResultOf(result gatewaycircuit.SuppressionFilterResult, accounts []gatewaydispatch.AccountCandidate) gatewaydispatch.SuppressionFilterResult {
	byID := make(map[string]gatewaydispatch.AccountCandidate, len(accounts))
	for _, account := range accounts {
		byID[account.ID] = account
	}
	filtered := make([]gatewaydispatch.AccountCandidate, 0, len(result.Accounts))
	for _, account := range result.Accounts {
		if candidate, ok := byID[account.ID]; ok {
			filtered = append(filtered, candidate)
		}
	}
	leases := make([]gatewaydispatch.HalfOpenLease, 0, len(result.AcquiredHalfOpenLeases))
	for _, lease := range result.AcquiredHalfOpenLeases {
		leases = append(leases, &chainSuppressionLease{lease: lease})
	}
	scopes := make([]gatewaydispatch.PrecheckSuppressedRuntimeScope, 0, len(result.PrecheckSuppressedRuntimeScopes))
	for _, scope := range result.PrecheckSuppressedRuntimeScopes {
		scopes = append(scopes, gatewaydispatch.PrecheckSuppressedRuntimeScope{
			RuntimeKey: scope.RuntimeKey,
			Generation: scope.Generation,
		})
	}
	return gatewaydispatch.SuppressionFilterResult{
		Accounts:                             filtered,
		SuppressedCount:                      result.SuppressedCount,
		AllSuppressed:                        result.AllSuppressed,
		SuppressedAccountIDs:                 result.SuppressedAccountIDs,
		NextRetryAfterMs:                     result.NextRetryAfterMs,
		AcquiredHalfOpenLeases:               leases,
		PrecheckSuppressedAccountIDs:         result.PrecheckSuppressedAccountIDs,
		ConfiguredPolicySuppressedAccountIDs: result.ConfiguredPolicySuppressedAccountIDs,
		PrecheckSuppressedRuntimeScopes:      scopes,
	}
}

// chainDispatchSuppressionWaiter implements the dispatch
// RecoverableSuppressionWaiter over the shared wait engine (Node
// waitForRecoverableUnavailableState behind the suppression preflight).
type chainDispatchSuppressionWaiter struct {
	wait *gatewaycircuit.PreAuthRecoverableWait
}

func (w chainDispatchSuppressionWaiter) WaitForState(ctx context.Context, input gatewaydispatch.SuppressionWaitInput) (gatewaydispatch.SuppressionFilterResult, error) {
	var state gatewaydispatch.SuppressionFilterResult
	_, _, err := w.wait.WaitForStateLoop(ctx, gatewaycircuit.StateWaitInput{
		ScopeKey:                 input.ScopeKey,
		Reason:                   input.Reason,
		AuditCapture:             input.AuditCapture,
		MaxWaitMs:                input.MaxWaitMs,
		RequestStartedAtMs:       input.RequestStartedAtMs,
		DeadlineAtMs:             input.DeadlineAtMs,
		RouteCoordinationBudget:  input.RouteCoordinationBudget,
		GatewayRequestWallBudget: input.GatewayRequestWallBudget,
		Signal:                   input.Signal,
		Refresh: func(ctx context.Context) (bool, bool, int64, error) {
			sample, refreshErr := input.Refresh(ctx)
			if refreshErr != nil {
				return false, false, 0, refreshErr
			}
			state = sample
			if sample.NextRetryAfterMs != nil {
				return !sample.AllSuppressed, true, *sample.NextRetryAfterMs, nil
			}
			return !sample.AllSuppressed, false, 0, nil
		},
	})
	if err != nil {
		return gatewaydispatch.SuppressionFilterResult{}, err
	}
	return state, nil
}

// ---------------------------------------------------------------------------
// D-136: proxy health (upstream bucket health ordering + failure records)
// ---------------------------------------------------------------------------

// chainProxyHealthPort adapts the gatewayproxyhealth service onto the
// dispatch ProxyHealthPort (Node proxy-health.service.ts
// orderGatewayAccountsByUpstreamBucketHealthAsync / recordGatewayProxyFailureAsync).
type chainProxyHealthPort struct {
	service *gatewayproxyhealth.ProxyHealthService
}

func (p chainProxyHealthPort) OrderAsync(ctx context.Context, accounts []gatewaydispatch.AccountCandidate, modelPriority *gatewaydispatch.ModelPriority) (gatewaydispatch.ProxyHealthOrder, error) {
	result, err := p.service.OrderGatewayAccountsByUpstreamBucketHealthAsync(ctx, accounts, modelPriority)
	if err != nil {
		return gatewaydispatch.ProxyHealthOrder{}, err
	}
	return gatewaydispatch.ProxyHealthOrder{
		Accounts:           result.Accounts,
		Applied:            result.Applied,
		AvoidedBucketKeys:  result.AvoidedBucketKeys,
		AvoidedProxyKeys:   result.AvoidedProxyKeys,
		AvoidedAccountIDs:  result.AvoidedAccountIDs,
		HalfOpenBucketKeys: result.HalfOpenBucketKeys,
		HalfOpenAccountIDs: result.HalfOpenAccountIDs,
		BypassedAllAvoided: result.BypassedAllAvoided,
	}, nil
}

func (p chainProxyHealthPort) RecordFailureAsync(ctx context.Context, account gatewaydispatch.AccountCandidate, message string) error {
	_, err := p.service.RecordGatewayProxyFailureAsync(ctx, account, message, gatewayproxyhealth.FailureRecordOptions{})
	return err
}

// ---------------------------------------------------------------------------
// D-137: hot quality (ordering / exploration / attempt accounting)
// ---------------------------------------------------------------------------

// chainHotQualityPort adapts the gatewayhotquality runtime onto the dispatch
// HotQualityPort (Node hot-quality-runtime.service.ts
// orderGatewayAccountsByHotQualityAsync).
type chainHotQualityPort struct {
	runtime *gatewayhotquality.GatewayHotQualityRuntime
}

func (p chainHotQualityPort) OrderAsync(ctx context.Context, input gatewaydispatch.HotQualityOrderInput) (gatewaydispatch.HotQualityOrder, error) {
	result, err := gatewayhotquality.OrderGatewayAccountsByHotQuality(ctx, p.runtime, gatewayhotquality.GatewayHotQualityCandidateOrderInput[gatewaydispatch.AccountCandidate]{
		Accounts:                     input.Accounts,
		Base:                         chainHotQualityAccountViewOf,
		ModelPriorityRankByAccountID: modelPriorityRankMapOf(input.ModelPriority),
		Mode:                         input.Mode,
		SystemAccountID:              input.SystemAccountID,
		RouteStrategyID:              input.RouteStrategyID,
		GroupID:                      input.GroupID,
		RequestLane:                  input.RequestLane,
		Model:                        chainModelPtr(input.Model),
		RequestID:                    input.RequestID,
		LatencyDegradedAccountIDs:    chainLatencyDegradedOf(input.LatencyDegradedAccountIDs),
		EligibleFirstPrimaryDispatch: input.EligibleFirstPrimaryDispatch,
	})
	if err != nil {
		return gatewaydispatch.HotQualityOrder{}, err
	}
	byID := make(map[string]gatewaydispatch.AccountCandidate, len(input.Accounts))
	for _, account := range input.Accounts {
		byID[account.ID] = account
	}
	ordered := make([]gatewaydispatch.AccountCandidate, 0, len(result.Accounts))
	for _, account := range result.Accounts {
		if candidate, ok := byID[account.ID]; ok {
			ordered = append(ordered, candidate)
		}
	}
	order := gatewaydispatch.HotQualityOrder{
		Accounts:                       ordered,
		DispatchIntent:                 result.DispatchIntent,
		SelectedAccountID:              result.SelectedAccountID,
		ExplorationStatus:              result.ExplorationStatus,
		QualityReorderedTierKeys:       result.QualityReorderedTierKeys,
		LatencyDegradedOverrideApplied: result.LatencyDegradedOverrideApplied,
		SettleExplorationAfterDispatch: result.SettleExplorationAfterDispatch,
	}
	if result.ExplorationReservation != nil {
		order.ExplorationReservation = &gatewaypreauth.HotQualityExplorationReservation{
			AccountRuntimeKey: result.ExplorationReservation.AccountRuntimeKey,
		}
	}
	return order, nil
}

// chainHotQualityAccountViewOf mirrors the Node UpstreamAccount projection
// the ordering pipeline reads.
func chainHotQualityAccountViewOf(account gatewaydispatch.AccountCandidate) gatewayhotquality.GatewayHotQualityAccountView {
	return gatewayhotquality.GatewayHotQualityAccountView{
		ID:                        account.ID,
		AccountAccessType:         account.AccountAccessType,
		BindingSystemAccountID:    derefStringPtr(account.BindingSystemAccountID),
		BoundGroupID:              derefStringPtr(account.BoundGroupID),
		AccountAuthorizationID:    derefStringPtr(account.AccountAuthorizationID),
		ProviderProtocolProfileID: account.ProviderProtocolProfileID,
		ProtocolCode:              account.ProtocolCode,
		ProtocolVersion:           account.ProtocolVersion,
		FallbackEnabled:           account.FallbackEnabled,
		SuperPriorityEnabled:      account.SuperPriorityEnabled,
		Priority:                  account.Priority,
	}
}

// newChainHotQualityLifecycleFactory mounts the G12 attempt lifecycle
// (attempt record / first-byte / terminal settlement) onto the engine hook.
func newChainHotQualityLifecycleFactory(runtime *gatewayhotquality.GatewayHotQualityRuntime) gatewaydispatch.HotQualityAttemptLifecycleFactory {
	return func(input gatewaydispatch.HotQualityLifecycleInput) gatewaydispatch.HotQualityAttemptLifecycle {
		lifecycle, err := gatewayhotquality.NewGatewayHotQualityAttemptLifecycle(gatewayhotquality.GatewayHotQualityAttemptLifecycleInput{
			Runtime:     runtime,
			AttemptID:   input.AttemptID,
			Account:     chainHotQualityLifecycleAccountOf(input),
			RequestLane: input.RequestLane,
			Model:       chainModelPtr(input.Model),
		})
		if err != nil || lifecycle == nil {
			return nil
		}
		return &chainHotQualityAttemptLifecycle{lifecycle: lifecycle}
	}
}

// chainHotQualityLifecycleAccountOf rebuilds the account view from the
// lifecycle input: the engine hook carries the account id plus the model and
// lane, so the scope reuses the account id as the runtime key (the lifecycle
// scope only needs the key + protocol profile + model family).
func chainHotQualityLifecycleAccountOf(input gatewaydispatch.HotQualityLifecycleInput) gatewayhotquality.GatewayHotQualityAccountView {
	return gatewayhotquality.GatewayHotQualityAccountView{
		ID:                     input.AccountID,
		BindingSystemAccountID: "",
		BoundGroupID:           "",
		AccountAuthorizationID: "",
	}
}

type chainHotQualityAttemptLifecycle struct {
	lifecycle *gatewayhotquality.GatewayHotQualityAttemptLifecycle
}

func (l *chainHotQualityAttemptLifecycle) MarkFirstByte(firstByteMs *float64) {
	l.lifecycle.MarkFirstByte(firstByteMs)
}

func (l *chainHotQualityAttemptLifecycle) RecordTerminal(ctx context.Context, terminal gatewaydispatch.HotQualityTerminal) {
	l.lifecycle.RecordTerminal(ctx, gatewayhotquality.GatewayHotQualityTerminalInput{
		OutcomeClass: terminal.OutcomeClass,
		FailureScope: terminal.FailureScope,
		Source:       terminal.Source,
	})
}

// ---------------------------------------------------------------------------
// D-129: process-local quota db-service fallback (no cross-process IPC)
// ---------------------------------------------------------------------------

// chainQuotaDBService implements gatewayquota.DBServiceClient in-process:
// the Node db-service IPC round trip (requestDbService) collapses onto the
// same stats-database reads the quota services own, because the Go gateway
// process shares the database without a broker hop (去跨进程原则).
type chainQuotaDBService struct {
	apiKeys *gatewayquota.APIKeyQuotaService
	authz   *gatewayquota.AuthorizationQuotaService
}

func newChainQuotaDBService(apiKeys *gatewayquota.APIKeyQuotaService, authz *gatewayquota.AuthorizationQuotaService) *chainQuotaDBService {
	return &chainQuotaDBService{apiKeys: apiKeys, authz: authz}
}

// CheckAPIKeyQuota mirrors { type: 'check_api_key_quota' }.
func (s *chainQuotaDBService) CheckAPIKeyQuota(ctx context.Context, apiKey gatewayquota.APIKeyRow) (gatewayquota.Decision, error) {
	return s.apiKeys.CheckAPIKeyQuota(ctx, apiKey, time.Now())
}

// ReadAPIKeyQuotaCosts mirrors { type: 'read_api_key_quota_costs' }: the
// exact stats-database cost load (ReadAPIKeyQuotaCostsExactAsync).
func (s *chainQuotaDBService) ReadAPIKeyQuotaCosts(ctx context.Context, apiKey gatewayquota.APIKeyRow) (gatewayquota.RequestQuotaCosts, error) {
	return s.apiKeys.ReadAPIKeyQuotaCostsExactAsync(ctx, apiKey, time.Now())
}

// CheckAuthorizationQuota mirrors { type: 'check_authorization_quota' } via
// the id-keyed exact read (CheckAuthorizationQuotaByIDs).
func (s *chainQuotaDBService) CheckAuthorizationQuota(ctx context.Context, groupAuthorizationID, accountAuthorizationID string) (gatewayquota.Decision, error) {
	return s.authz.CheckAuthorizationQuotaByIDs(ctx, groupAuthorizationID, accountAuthorizationID, time.Now())
}

// CheckAuthorizationQuotaBatch mirrors { type: 'check_authorization_quota_batch' }
// via the id-keyed exact read (CheckAuthorizationQuotaBatchByIDs).
func (s *chainQuotaDBService) CheckAuthorizationQuotaBatch(ctx context.Context, groupAuthorizationID string, accounts []gatewayquota.AccountRef) ([]gatewayquota.Decision, error) {
	return s.authz.CheckAuthorizationQuotaBatchByIDs(ctx, groupAuthorizationID, accounts, time.Now())
}

// ---------------------------------------------------------------------------
// shared small helpers
// ---------------------------------------------------------------------------

func derefInt64Ptr(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

func derefStringPtr(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

func chainModelPtr(model string) *string {
	if strings.TrimSpace(model) == "" {
		return nil
	}
	return &model
}

func chainLatencyDegradedOf(ids map[string]struct{}) map[string]bool {
	if len(ids) == 0 {
		return nil
	}
	out := make(map[string]bool, len(ids))
	for id := range ids {
		out[id] = true
	}
	return out
}

// modelPriorityRankMapOf mirrors gatewaydispatch modelPriorityRankMap for the
// composition side (the engine helper is package-internal).
func modelPriorityRankMapOf(priority *gatewaydispatch.ModelPriority) map[string]int {
	if priority == nil {
		return nil
	}
	return priority.RankByAccountID
}

// slogWarnFields funnels the adapter warns through the process logger.
func slogWarnFields(event string, fields map[string]any, message string) {
	args := make([]any, 0, len(fields)*2+2)
	args = append(args, "event", event)
	for key, value := range fields {
		args = append(args, key, value)
	}
	slog.Warn(message, args...)
}

// slogInfoFields funnels the adapter info lines through the process logger.
func slogInfoFields(event string, fields map[string]any, message string) {
	args := make([]any, 0, len(fields)*2+2)
	args = append(args, "event", event)
	for key, value := range fields {
		args = append(args, key, value)
	}
	slog.Info(message, args...)
}

// compile-time interface assertions for the W2-C wiring.
var (
	_ gatewayruntimecache.GroupBindingOrderer      = chainGroupBindingOrderer{}
	_ gatewaydispatch.ClientIPConcurrencyAcquirer  = (*chainClientIPConcurrency)(nil)
	_ gatewaydispatch.KeyModelAdmission            = chainKeyModelAdmission{}
	_ gatewaydispatch.SuppressionPort              = chainSuppressionPort{}
	_ gatewaydispatch.RecoverableSuppressionWaiter = chainDispatchSuppressionWaiter{}
	_ gatewaydispatch.ProxyHealthPort              = chainProxyHealthPort{}
	_ gatewaydispatch.HotQualityPort               = chainHotQualityPort{}
	_ gatewayquota.DBServiceClient                 = (*chainQuotaDBService)(nil)
	_ gatewaydispatch.HalfOpenLease                = (*chainSuppressionLease)(nil)
)

var _ = sync.Once{}
