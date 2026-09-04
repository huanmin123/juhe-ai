package gatewayruntimecache

import (
	"context"
	"time"
)

// ---------------------------------------------------------------------------
// active response inspection policies cache (Node
// listCachedActiveResponseInspectionPoliciesAsync /
// listCachedActiveResponseInspectionPoliciesForAccountsAsync)
// ---------------------------------------------------------------------------

// ListCachedActiveResponseInspectionPoliciesAsync mirrors the async read with
// the retain TTL (10 min) and the 60 s revalidate window; stale entries serve
// as last-good while the background refresh runs.
func (s *Service) ListCachedActiveResponseInspectionPoliciesAsync(ctx context.Context, protocolCode string, providerCode string) ([]ResponseInspectionPolicySummary, error) {
	s.syncInvalidationsBestEffort(ctx)
	cacheKey := responseInspectionPolicyCacheKey(protocolCode, providerCode)
	if s.sharedInspection == nil {
		if cached, ok := s.inspectCache.get(cacheKey); ok {
			if !isEntryFresh(cached.revalidateAtMs, s.nowMs()) {
				s.refreshActiveResponseInspectionPoliciesInBackground(protocolCode, providerCode, cacheKey)
			}
			return CloneResponseInspectionPolicies(cached.policies), nil
		}
		return s.loadActiveResponseInspectionPoliciesAndPopulateCache(ctx, protocolCode, providerCode, cacheKey)
	}

	shared, ok, err := s.getSharedInspection(ctx, cacheKey)
	if err != nil {
		s.logSharedFailure("gateway_response_inspection_policy_shared_cache_read_failed", err)
	}
	if ok {
		s.inspectCache.set(cacheKey, cloneResponseInspectionPolicyCacheEntry(shared), responseInspectionPolicyRetainTTL)
		if !isEntryFresh(shared.revalidateAtMs, s.nowMs()) {
			s.refreshActiveResponseInspectionPoliciesInBackground(protocolCode, providerCode, cacheKey)
		}
		return CloneResponseInspectionPolicies(shared.policies), nil
	}
	return s.loadActiveResponseInspectionPoliciesAndPopulateCache(ctx, protocolCode, providerCode, cacheKey)
}

// ListCachedActiveResponseInspectionPoliciesForAccountsAsync mirrors the
// account-scoped fan-out: unique scopes in first-seen order, policies merged
// by id preserving first insertion.
func (s *Service) ListCachedActiveResponseInspectionPoliciesForAccountsAsync(ctx context.Context, accounts []OpenAIAccountSecret) ([]ResponseInspectionPolicySummary, error) {
	seen := map[string]bool{}
	scopes := []([2]string){}
	for i := range accounts {
		protocolCode := trimSpace(accounts[i].ProtocolCode)
		if protocolCode == "" {
			continue
		}
		providerCode := ""
		if accounts[i].ProviderCode != "" {
			providerCode = trimSpace(accounts[i].ProviderCode)
		}
		key := protocolCode + ":" + providerCode
		if seen[key] {
			continue
		}
		seen[key] = true
		scopes = append(scopes, [2]string{protocolCode, providerCode})
	}
	ids := []string{}
	byID := map[string]ResponseInspectionPolicySummary{}
	for _, scope := range scopes {
		policies, err := s.ListCachedActiveResponseInspectionPoliciesAsync(ctx, scope[0], scope[1])
		if err != nil {
			return nil, err
		}
		for _, policy := range policies {
			if _, exists := byID[policy.ID]; !exists {
				ids = append(ids, policy.ID)
			}
			byID[policy.ID] = policy
		}
	}
	out := make([]ResponseInspectionPolicySummary, 0, len(ids))
	for _, id := range ids {
		out = append(out, CloneResponseInspectionPolicy(byID[id]))
	}
	return out, nil
}

// responseInspectionPolicyCacheEntryNow mirrors responseInspectionPolicyCacheEntry.
func responseInspectionPolicyCacheEntryNow(policies []ResponseInspectionPolicySummary, now int64, revalidateAtMs int64) responseInspectionPolicyCacheEntry {
	return responseInspectionPolicyCacheEntry{
		policies:       CloneResponseInspectionPolicies(policies),
		revalidateAtMs: revalidateAtMs,
	}
}

func cloneResponseInspectionPolicyCacheEntry(entry responseInspectionPolicyCacheEntry) responseInspectionPolicyCacheEntry {
	return responseInspectionPolicyCacheEntry{
		policies:       CloneResponseInspectionPolicies(entry.policies),
		revalidateAtMs: entry.revalidateAtMs,
	}
}

// sharedResponseInspectionPolicyEntry is the shared-cache JSON shape.
type sharedResponseInspectionPolicyEntry struct {
	Policies       []ResponseInspectionPolicySummary `json:"policies"`
	RevalidateAtMs int64                             `json:"revalidateAtMs"`
}

func (e sharedResponseInspectionPolicyEntry) toEntry() responseInspectionPolicyCacheEntry {
	return responseInspectionPolicyCacheEntry{
		policies:       CloneResponseInspectionPolicies(e.Policies),
		revalidateAtMs: e.RevalidateAtMs,
	}
}

func (e responseInspectionPolicyCacheEntry) toShared() sharedResponseInspectionPolicyEntry {
	return sharedResponseInspectionPolicyEntry{
		Policies:       CloneResponseInspectionPolicies(e.policies),
		RevalidateAtMs: e.revalidateAtMs,
	}
}

func (s *Service) getSharedInspection(ctx context.Context, cacheKey string) (responseInspectionPolicyCacheEntry, bool, error) {
	var decoded sharedResponseInspectionPolicyEntry
	ok, err := s.sharedInspection.Get(ctx, cacheKey, &decoded)
	if err != nil || !ok {
		return responseInspectionPolicyCacheEntry{}, false, err
	}
	return decoded.toEntry(), true, nil
}

// loadActiveResponseInspectionPoliciesAndPopulateCache mirrors the Node loader
// with the runtime generation guard.
func (s *Service) loadActiveResponseInspectionPoliciesAndPopulateCache(ctx context.Context, protocolCode, providerCode, cacheKey string) ([]ResponseInspectionPolicySummary, error) {
	generation := s.currentRuntimeGeneration()
	value, err := s.models.ListActiveResponseInspectionPolicies(ctx, protocolCode, providerCode)
	if err != nil {
		return nil, err
	}
	policies := CloneResponseInspectionPolicies(value)
	if s.isRuntimeGenerationCurrent(generation) {
		s.setInspectionCacheEntry(cacheKey, responseInspectionPolicyCacheEntryNow(policies, s.nowMs(), s.nowMs()+gatewayRuntimeTTL.Milliseconds()))
	}
	return CloneResponseInspectionPolicies(policies), nil
}

// setInspectionCacheEntry mirrors setResponseInspectionPolicyCacheEntryAsync.
func (s *Service) setInspectionCacheEntry(cacheKey string, entry responseInspectionPolicyCacheEntry) {
	cached := cloneResponseInspectionPolicyCacheEntry(entry)
	s.inspectCache.set(cacheKey, cached, responseInspectionPolicyRetainTTL)
	if s.sharedInspection == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := s.sharedInspection.Set(ctx, cacheKey, cached.toShared(), responseInspectionPolicyRetainTTL); err != nil {
		s.logSharedFailure("gateway_response_inspection_policy_shared_cache_write_failed", err)
	}
}

// refreshActiveResponseInspectionPoliciesInBackground mirrors the Node
// background refresh with per-key dedupe.
func (s *Service) refreshActiveResponseInspectionPoliciesInBackground(protocolCode, providerCode, cacheKey string) {
	s.mu.Lock()
	if _, pending := s.pendingInspectRefreshes[cacheKey]; pending {
		s.mu.Unlock()
		return
	}
	generation := s.currentRuntimeGeneration()
	call := newRefreshCall()
	s.pendingInspectRefreshes[cacheKey] = call
	s.mu.Unlock()

	go func() {
		defer func() {
			s.mu.Lock()
			delete(s.pendingInspectRefreshes, cacheKey)
			s.mu.Unlock()
			call.finish()
		}()
		ctx, cancel := context.WithTimeout(context.Background(), GatewayRuntimeLoadTimeout)
		defer cancel()
		value, err := s.models.ListActiveResponseInspectionPolicies(ctx, protocolCode, providerCode)
		if err != nil {
			s.warnEvent("gateway_response_inspection_policy_stale_refresh_failed", map[string]any{
				"protocolCode": protocolCode, "providerCode": providerCode, "err": err.Error(),
			}, "网关响应检查策略后台刷新失败，保留当前有界缓存快照")
			return
		}
		if !s.isRuntimeGenerationCurrent(generation) {
			return
		}
		entry := responseInspectionPolicyCacheEntryNow(CloneResponseInspectionPolicies(value), s.nowMs(), s.nowMs()+gatewayRuntimeTTL.Milliseconds())
		s.setInspectionCacheEntry(cacheKey, entry)
	}()
}
