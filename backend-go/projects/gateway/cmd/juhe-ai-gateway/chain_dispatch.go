package main

// G20 phase-2 composition-root adapters for the gatewaydispatch candidate
// pipeline collaborators. The ordering/runtime-state ports (latency
// degradation, upstream proxy health, hot quality, client-source avoidance)
// have no Go runtime store yet — Node behaves identically when the
// corresponding runtime feature is absent (passthrough ordering, no
// avoidance), so the adapters implement exactly that semantics and log one
// line on first use. The client-IP avoidance and concurrency adapters wrap
// the existing gatewayclientip services (G13); the dispatch quota adapter
// bridges the G07 authorization quota service onto the dispatch port.

import (
	"context"
	"log/slog"
	"sync"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayclientip"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayquota"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// degradedPortWarns dedupes the once-per-port degradation logs.
var degradedPortWarns sync.Map

// slogOnceWarn logs a port degradation once per process.
func slogOnceWarn(port, effect string) {
	if _, loaded := degradedPortWarns.LoadOrStore(port, struct{}{}); loaded {
		return
	}
	slog.Warn("网关链端口显式降级", "port", port, "effect", effect)
}

// degradedLatency implements gatewaydispatch.LatencyDegradationPort with the
// Node "latency degradation runtime absent" semantics: accounts keep the
// scheduling order.
type degradedLatency struct {
	once sync.Once
}

func (d *degradedLatency) OrderAsync(_ context.Context, accounts []gatewaydispatch.AccountCandidate, _ *gatewaydispatch.LatencyScopeInput, _ *gatewaypreauth.NormalRouteSpeedFirstRuntimeConfig, _ *gatewaydispatch.ModelPriority) (gatewaydispatch.LatencyDegradationOrder, error) {
	d.once.Do(func() {
		slogOnceWarn("gatewaydispatch.LatencyDegradationPort", "时延降级排序保持不变")
	})
	return gatewaydispatch.LatencyDegradationOrder{Accounts: accounts}, nil
}

// degradedProxyHealth implements gatewaydispatch.ProxyHealthPort with the
// "upstream bucket health runtime absent" semantics.
type degradedProxyHealth struct {
	once sync.Once
}

func (d *degradedProxyHealth) OrderAsync(_ context.Context, accounts []gatewaydispatch.AccountCandidate, _ *gatewaydispatch.ModelPriority) (gatewaydispatch.ProxyHealthOrder, error) {
	d.once.Do(func() {
		slogOnceWarn("gatewaydispatch.ProxyHealthPort", "上游桶健康排序保持不变")
	})
	return gatewaydispatch.ProxyHealthOrder{Accounts: accounts}, nil
}

func (d *degradedProxyHealth) RecordFailureAsync(context.Context, gatewaydispatch.AccountCandidate, string) error {
	return nil
}

// degradedHotQuality implements gatewaydispatch.HotQualityPort with the
// "hot quality runtime absent" semantics: no re-ranking, no exploration
// reservation.
type degradedHotQuality struct {
	once sync.Once
}

func (d *degradedHotQuality) OrderAsync(_ context.Context, input gatewaydispatch.HotQualityOrderInput) (gatewaydispatch.HotQualityOrder, error) {
	d.once.Do(func() {
		slogOnceWarn("gatewaydispatch.HotQualityPort", "热度质量排序保持不变")
	})
	return gatewaydispatch.HotQualityOrder{Accounts: input.Accounts}, nil
}

// degradedClientSourceAvoidance implements
// gatewaydispatch.ClientSourceAvoidancePort (client-profiles
// client-source-avoidance.service.ts is a later slice; absent state means no
// avoidance re-rank).
type degradedClientSourceAvoidance struct {
	once sync.Once
}

func (d *degradedClientSourceAvoidance) OrderAsync(_ context.Context, accounts []gatewaydispatch.AccountCandidate, _ gatewaypreauth.ClientStrategyContext, _ *gatewaydispatch.ModelPriority) (gatewaydispatch.AvoidanceOrder, error) {
	d.once.Do(func() {
		slogOnceWarn("gatewaydispatch.ClientSourceAvoidancePort", "客户端来源规避保持直通")
	})
	return gatewaydispatch.AvoidanceOrder{Accounts: accounts}, nil
}

// chainClientIPAvoidance adapts the G13 gatewayclientip.Avoidance onto the
// dispatch ClientIPAvoidancePort (account identity projection: the avoidance
// state keys on the account id inside the client-IP scope).
type chainClientIPAvoidance struct {
	avoidance *gatewayclientip.Avoidance
}

func newChainClientIPAvoidance(avoidance *gatewayclientip.Avoidance) *chainClientIPAvoidance {
	return &chainClientIPAvoidance{avoidance: avoidance}
}

func (a *chainClientIPAvoidance) OrderAsync(ctx context.Context, accounts []gatewaydispatch.AccountCandidate, scope gatewaydispatch.ClientIPAvoidanceScope, modelPriority *gatewaydispatch.ModelPriority) (gatewaydispatch.AvoidanceOrder, error) {
	if a.avoidance == nil || len(accounts) == 0 {
		return gatewaydispatch.AvoidanceOrder{Accounts: accounts}, nil
	}
	projected := make([]gatewayruntimecache.OpenAIAccountSecret, 0, len(accounts))
	for _, account := range accounts {
		projected = append(projected, gatewayruntimecache.OpenAIAccountSecret{ID: account.ID})
	}
	result, err := a.avoidance.OrderAccountsByClientIPAccountAvoidanceAsync(ctx, projected, gatewayclientip.AvoidanceScopeInput{
		SystemAccountID: scope.SystemAccountID,
		APIKeyID:        scope.APIKeyID,
		GroupID:         scope.GroupID,
		ClientIP:        scope.ClientIP,
	}, modelPriority)
	if err != nil {
		return gatewaydispatch.AvoidanceOrder{}, err
	}
	byID := make(map[string]gatewaydispatch.AccountCandidate, len(accounts))
	for _, account := range accounts {
		byID[account.ID] = account
	}
	ordered := make([]gatewaydispatch.AccountCandidate, 0, len(result.Accounts))
	for _, account := range result.Accounts {
		if candidate, ok := byID[account.ID]; ok {
			ordered = append(ordered, candidate)
		}
	}
	return gatewaydispatch.AvoidanceOrder{
		Accounts:           ordered,
		Applied:            result.Applied,
		AvoidedAccountIDs:  result.AvoidedAccountIDs,
		BypassedAllAvoided: result.BypassedAllAvoided,
	}, nil
}

// chainDispatchQuota adapts the G07 authorization quota service onto the
// dispatch AuthorizationQuotaChecker port.
type chainDispatchQuota struct {
	quota *gatewayquota.AuthorizationQuotaService
}

func newChainDispatchQuota(quota *gatewayquota.AuthorizationQuotaService) *chainDispatchQuota {
	return &chainDispatchQuota{quota: quota}
}

func (q *chainDispatchQuota) CheckBatchAsync(ctx context.Context, groupAccess gatewayruntimecache.GroupUsageAccessMetadata, accounts []gatewaydispatch.AccountCandidate) (map[string]gatewaydispatch.QuotaDecision, error) {
	entries := make([]gatewayquota.AccountAuthorizationSummary, 0, len(accounts))
	for _, account := range accounts {
		entries = append(entries, gatewayquota.AccountAuthorizationSummary{
			ID:                               account.ID,
			AccountAuthorizationID:           deref(account.AccountAuthorizationID),
			AccountAuthorizationQuotaLimited: account.AccountAuthorizationQuotaLimited != nil && *account.AccountAuthorizationQuotaLimited,
		})
	}
	decisions, err := q.quota.CheckAuthorizationQuotaBatchAsync(ctx, gatewayquota.GroupAccessMetadata{
		GroupAuthorizationID:           deref(groupAccess.GroupAuthorizationID),
		GroupAuthorizationQuotaLimited: groupAccess.GroupAuthorizationQuotaLimited != nil && *groupAccess.GroupAuthorizationQuotaLimited,
	}, entries)
	if err != nil {
		return nil, err
	}
	out := make(map[string]gatewaydispatch.QuotaDecision, len(decisions))
	for accountID, decision := range decisions {
		out[accountID] = gatewaydispatch.QuotaDecision{Allowed: decision.Allowed}
	}
	return out, nil
}

// chainConcurrencyStore adapts the process-local account concurrency tracker
// onto the dispatch AccountConcurrencyStore port (Node shared/account-concurrency
// memory driver: state stays process-local, acquire/release per lane).
type chainConcurrencyStore struct {
	tracker *gatewayclientip.MemoryAccountConcurrency
}

func newChainConcurrencyStore(tracker *gatewayclientip.MemoryAccountConcurrency) *chainConcurrencyStore {
	return &chainConcurrencyStore{tracker: tracker}
}

func (s *chainConcurrencyStore) LoadCurrentAsync(ctx context.Context, accountIDs []string) (map[string]int, error) {
	return s.tracker.LoadAccountCurrentConcurrencyByID(ctx, accountIDs)
}

func (s *chainConcurrencyStore) LoadCurrentByLaneAsync(ctx context.Context, accountIDs []string, lane string) (map[string]int, error) {
	return s.tracker.LoadAccountCurrentConcurrencyByLane(ctx, accountIDs, lane)
}

// TryAcquireAsync acquires one concurrency slot when the account is below
// its (lane-scoped) limit; the returned release puts the slot back.
func (s *chainConcurrencyStore) TryAcquireAsync(_ context.Context, accountID string, concurrencyLimit int, options gatewaydispatch.AccountConcurrencyAcquireOptions) (gatewaydispatch.ConcurrencySlot, error) {
	current := s.tracker.CurrentAccountConcurrency(accountID, options.Lane)
	laneLimit := concurrencyLimit
	if options.LaneLimit != nil {
		laneLimit = *options.LaneLimit
	}
	slot := gatewaydispatch.ConcurrencySlot{
		Current:     current,
		Limit:       concurrencyLimit,
		Lane:        options.Lane,
		LaneCurrent: current,
		LaneLimit:   laneLimit,
	}
	if concurrencyLimit > 0 && current >= laneLimit {
		return slot, nil
	}
	s.tracker.Acquire(accountID, options.Lane)
	slot.Acquired = true
	slot.Current = current + 1
	slot.LaneCurrent = current + 1
	accountIDCopy := accountID
	laneCopy := options.Lane
	slot.Release = func() { s.tracker.Release(accountIDCopy, laneCopy) }
	return slot, nil
}

// chainRuntimeCachePort adapts the G10 runtime cache onto the dispatch
// RuntimeCachePort. LoadApiKeyTransientStatesForDispatch degrades to an
// empty state set: the api-key rotation transient store is owned by the
// keymodel runtime slice (Redis); until it mounts, rotation falls back to
// the credentials carried on the account secret (TAKEOVER POINT).
type chainRuntimeCachePort struct {
	cache *gatewayruntimecache.Service
	once  sync.Once
}

func newChainRuntimeCachePort(cache *gatewayruntimecache.Service) *chainRuntimeCachePort {
	return &chainRuntimeCachePort{cache: cache}
}

func (p *chainRuntimeCachePort) ListCachedOpenAIAccountsForGroupAsync(ctx context.Context, groupID, systemAccountID string, options gatewaydispatch.CachedAccountsOptions) ([]gatewaydispatch.AccountCandidate, error) {
	return p.cache.ListCachedOpenAIAccountsForGroupAsync(ctx, groupID, systemAccountID, gatewayruntimecache.CachedOpenAIAccountsForGroupOptions{
		RequestedModel:          options.RequestedModel,
		RequestedEndpointFamily: options.RequestedEndpointFamily,
	})
}

func (p *chainRuntimeCachePort) ResolveCachedGroupUsageAccessMetadataAsync(ctx context.Context, groupID, systemAccountID string) (gatewayruntimecache.GroupUsageAccessMetadata, bool, error) {
	meta, err := p.cache.ResolveCachedGroupUsageAccessMetadataAsync(ctx, groupID, systemAccountID)
	if err != nil {
		return gatewayruntimecache.GroupUsageAccessMetadata{}, false, err
	}
	if meta == nil {
		return gatewayruntimecache.GroupUsageAccessMetadata{}, false, nil
	}
	return *meta, true, nil
}

func (p *chainRuntimeCachePort) LoadApiKeyTransientStatesForDispatch(_ context.Context, _ string, _ []string) ([]gatewayruntimecache.AccountAPIKeyRuntimeSelectionState, error) {
	p.once.Do(func() {
		slogOnceWarn("gatewaydispatch.RuntimeCachePort.LoadApiKeyTransientStates", "API Key 轮换暂态为空")
	})
	return nil, nil
}

// localSessionAffinity (chain_ports.go) satisfies SessionAffinityPort; the
// per-request ClientIPAccountAvoidance tracker factory rides on the same G13
// avoidance service (chainRuntimeDeps.Avoidance).
var _ = gatewayrouting.GatewayAccountModelPriority{}
