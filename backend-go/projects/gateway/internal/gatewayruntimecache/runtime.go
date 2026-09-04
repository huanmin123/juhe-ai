package gatewayruntimecache

import (
	"context"
	"errors"
)

// ---------------------------------------------------------------------------
// gateway runtime cache (Node readCachedGatewayRuntimeAsync)
// ---------------------------------------------------------------------------

// runtimeLoad mirrors PendingGatewayRuntimeLoad: one in-flight runtime load
// per hashed cache key, carrying the generation it started under.
type runtimeLoad struct {
	generation int64
	done       chan struct{}
	runtime    GatewayRuntime
	err        error
}

// ReadCachedGatewayRuntimeAsync mirrors readCachedGatewayRuntimeAsync: forced
// cross-instance invalidation sync, fresh-entry fast path, bounded stale
// fallback with background refresh, singleflight load with the generation
// retry loop.
func (s *Service) ReadCachedGatewayRuntimeAsync(ctx context.Context, apiKey string) (GatewayRuntime, error) {
	s.syncInvalidationsForRuntime(ctx)
	cacheKey := HashSecret(apiKey)
	now := s.nowMs()
	if cached, ok := s.runtimeCache.get(cacheKey); ok {
		// Touch the identity cache exactly like the Node read (keeps the
		// apiKeyId index hot for targeted invalidations).
		s.identityCache.get(cacheKey)
		if isEntryFresh(cached.revalidateAtMs, now) {
			return s.dispatchGatewayRuntimeForSend(ctx, cached.runtime)
		}
		// shouldAllowStaleGatewayRuntimeFallback() is constantly true in the
		// Node service: serve the bounded last-good snapshot while the
		// background refresh runs.
		s.refreshGatewayRuntimeInBackground(apiKey, cacheKey)
		return s.dispatchGatewayRuntimeForSend(ctx, cached.runtime)
	}
	runtime, err := s.loadGatewayRuntimeOnce(ctx, apiKey, cacheKey)
	if err != nil {
		return GatewayRuntime{}, err
	}
	return s.dispatchGatewayRuntimeForSend(ctx, runtime)
}

// dispatchGatewayRuntimeForSend mirrors the shared dispatch tail of every
// Node read path: sanitize, then dynamic re-route / concurrency overlay.
func (s *Service) dispatchGatewayRuntimeForSend(ctx context.Context, runtime GatewayRuntime) (GatewayRuntime, error) {
	sanitized, err := s.sanitizedGatewayRuntimeForDispatch(runtime)
	if err != nil {
		return GatewayRuntime{}, err
	}
	if sanitized.APIKey != nil && IsDynamicRouteStrategyMode(sanitized.APIKey.RouteStrategyMode) {
		return s.routeCachedDynamicGatewayRuntimeForDispatch(ctx, sanitized)
	}
	if sanitized.APIKey != nil {
		return s.cloneGatewayRuntimeForDispatchAsync(ctx, sanitized)
	}
	return cloneStaticGatewayRuntime(sanitized), nil
}

// loadGatewayRuntimeOnce mirrors loadGatewayRuntimeOnce: up to
// gatewayRuntimeLoadAttemptLimit attempts; a load whose generation was
// invalidated mid-flight is retried, a settled one is returned.
func (s *Service) loadGatewayRuntimeOnce(ctx context.Context, apiKey, cacheKey string) (GatewayRuntime, error) {
	pending := s.ensureRuntimeLoad(ctx, apiKey, cacheKey)
	return s.awaitGatewayRuntimeLoad(ctx, apiKey, cacheKey, pending)
}

// ensureRuntimeLoad registers (or reuses) the pending singleflight load. The
// registration is synchronous — exactly like the Node async function, whose
// body runs up to the first await before returning — so a background refresh
// that just started is observable to AwaitBackgroundWork.
func (s *Service) ensureRuntimeLoad(ctx context.Context, apiKey, cacheKey string) *runtimeLoad {
	s.mu.Lock()
	if pending, ok := s.pendingRuntimeLoads[cacheKey]; ok {
		s.mu.Unlock()
		return pending
	}
	generation := s.apiKeyRuntimeGeneration
	pending := &runtimeLoad{generation: generation, done: make(chan struct{})}
	s.pendingRuntimeLoads[cacheKey] = pending
	s.mu.Unlock()
	go s.runGatewayRuntimeLoad(ctx, apiKey, cacheKey, generation, pending)
	return pending
}

// awaitGatewayRuntimeLoad mirrors the Node retry loop around one pending load.
func (s *Service) awaitGatewayRuntimeLoad(ctx context.Context, apiKey, cacheKey string, pending *runtimeLoad) (GatewayRuntime, error) {
	for attempt := 0; attempt < gatewayRuntimeLoadAttemptLimit; attempt++ {
		select {
		case <-pending.done:
			if s.isAPIKeyRuntimeGenerationCurrent(pending.generation) {
				return pending.runtime, pending.err
			}
		case <-ctx.Done():
			return GatewayRuntime{}, ctx.Err()
		}
		pending = s.ensureRuntimeLoad(ctx, apiKey, cacheKey)
	}
	return GatewayRuntime{}, errors.New("网关 API Key 运行时在连续失效后仍发生变化，请重试")
}

// runGatewayRuntimeLoad mirrors loadGatewayRuntimeAndPopulateCaches and the
// pending cleanup.
func (s *Service) runGatewayRuntimeLoad(ctx context.Context, apiKey, cacheKey string, generation int64, pending *runtimeLoad) {
	loadCtx, cancel := context.WithTimeout(ctx, GatewayRuntimeLoadTimeout)
	defer cancel()
	runtime, err := s.models.ReadGatewayRuntime(loadCtx, apiKey)
	if err == nil && s.isAPIKeyRuntimeGenerationCurrent(generation) {
		err = s.populateGatewayRuntimeCaches(cacheKey, runtime)
	}
	s.mu.Lock()
	if s.pendingRuntimeLoads[cacheKey] == pending {
		delete(s.pendingRuntimeLoads, cacheKey)
	}
	s.mu.Unlock()
	pending.runtime = runtime
	pending.err = err
	close(pending.done)
}

// populateGatewayRuntimeCaches mirrors populateGatewayRuntimeCaches: the
// runtime entry, the settings entry and (when group access resolved) the
// group access + accounts entries under the natural cache keys.
func (s *Service) populateGatewayRuntimeCaches(cacheKey string, runtime GatewayRuntime) error {
	now := s.nowMs()
	if runtime.APIKey == nil {
		s.setGatewayRuntimeCacheEntry(cacheKey, gatewayRuntimeCacheEntry{
			runtime:        cloneStaticGatewayRuntime(runtime),
			revalidateAtMs: now + invalidGatewayRuntimeTTL.Milliseconds(),
		})
		s.setSettingsCacheEntry(runtime.Settings)
		return nil
	}
	ttl, err := gatewayRuntimeCacheTTL(runtime, now)
	if err != nil {
		return err
	}
	s.setGatewayRuntimeCacheEntry(cacheKey, gatewayRuntimeCacheEntry{
		runtime:        cloneStaticGatewayRuntime(runtime),
		revalidateAtMs: now + ttl.Milliseconds(),
	})
	s.setSettingsCacheEntry(runtime.Settings)
	if runtime.GroupAccess != nil {
		groupKey := gatewayCacheKey(runtime.APIKey.SelectedGroupID, runtime.APIKey.SystemAccountID)
		entry, entryErr := newGroupUsageAccessCacheEntry(runtime.GroupAccess, now)
		if entryErr != nil {
			return entryErr
		}
		s.setGroupUsageAccessCacheEntry(groupKey, entry)
	}
	if runtime.GroupAccess != nil {
		groupKey := gatewayCacheKey(runtime.APIKey.SelectedGroupID, runtime.APIKey.SystemAccountID)
		entry, entryErr := newOpenAIAccountsCacheEntry(cloneStaticOpenAIAccounts(runtime.Accounts), now)
		if entryErr != nil {
			return entryErr
		}
		s.accountsCache.set(groupKey, entry, openAIAccountsRetainTTL)
	}
	return nil
}

// setGatewayRuntimeCacheEntry mirrors setGatewayRuntimeCacheEntry: the cache
// write plus the apiKeyId -> cacheKeys index maintenance through the identity
// cache.
func (s *Service) setGatewayRuntimeCacheEntry(cacheKey string, entry gatewayRuntimeCacheEntry) {
	s.runtimeCache.set(cacheKey, entry, gatewayRuntimeRetainTTL)
	apiKeyID := ""
	if entry.runtime.APIKey != nil {
		apiKeyID = entry.runtime.APIKey.ID
	}
	if apiKeyID == "" {
		return
	}
	if previous, ok := s.identityCache.get(cacheKey); ok && previous.apiKeyID != apiKeyID {
		s.removeGatewayRuntimeCacheIndex(previous.apiKeyID, cacheKey)
	}
	s.identityCache.set(cacheKey, gatewayRuntimeAPIKeyIdentity{apiKeyID: apiKeyID}, gatewayRuntimeRetainTTL)
	s.keysMu.Lock()
	if s.keysByAPIKeyID[apiKeyID] == nil {
		s.keysByAPIKeyID[apiKeyID] = map[string]struct{}{}
	}
	s.keysByAPIKeyID[apiKeyID][cacheKey] = struct{}{}
	s.keysMu.Unlock()
}

func (s *Service) removeGatewayRuntimeCacheIndex(apiKeyID, cacheKey string) {
	if apiKeyID == "" {
		return
	}
	s.keysMu.Lock()
	defer s.keysMu.Unlock()
	cacheKeys := s.keysByAPIKeyID[apiKeyID]
	if cacheKeys == nil {
		return
	}
	delete(cacheKeys, cacheKey)
	if len(cacheKeys) == 0 {
		delete(s.keysByAPIKeyID, apiKeyID)
	}
}

func (s *Service) clearGatewayRuntimeCacheIndex() {
	s.keysMu.Lock()
	defer s.keysMu.Unlock()
	s.keysByAPIKeyID = map[string]map[string]struct{}{}
}

// refreshGatewayRuntimeInBackground mirrors refreshGatewayRuntimeInBackground:
// an atomic dedupe check plus synchronous pending registration (the Node async
// body registers before its first await), with the IO and the generation retry
// loop running detached on their own budget.
func (s *Service) refreshGatewayRuntimeInBackground(apiKey, cacheKey string) {
	s.mu.Lock()
	if _, pending := s.pendingRuntimeLoads[cacheKey]; pending {
		s.mu.Unlock()
		return
	}
	generation := s.apiKeyRuntimeGeneration
	pending := &runtimeLoad{generation: generation, done: make(chan struct{})}
	s.pendingRuntimeLoads[cacheKey] = pending
	s.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), GatewayRuntimeLoadTimeout)
		defer cancel()
		s.runGatewayRuntimeLoad(ctx, apiKey, cacheKey, generation, pending)
		if _, err := s.awaitGatewayRuntimeLoad(ctx, apiKey, cacheKey, pending); err != nil {
			s.warnEvent("gateway_runtime_stale_refresh_failed", map[string]any{
				"err": err.Error(),
			}, "网关运行配置后台刷新失败，保留当前有界内存快照")
		}
	}()
}

// ---------------------------------------------------------------------------
// dispatch clones (Node sanitizedGatewayRuntimeForDispatch and friends)
// ---------------------------------------------------------------------------

// sanitizedGatewayRuntimeForDispatch mirrors sanitizedGatewayRuntimeForDispatch:
// unusable keys / expired group access collapse to a settings-only snapshot
// and unusable accounts drop out.
func (s *Service) sanitizedGatewayRuntimeForDispatch(runtime GatewayRuntime) (GatewayRuntime, error) {
	settings := CloneGatewaySettings(runtime.Settings)
	empty := GatewayRuntime{Settings: settings, Accounts: []OpenAIAccountSecret{}}
	now := s.nowMs()
	if runtime.APIKey == nil {
		return empty, nil
	}
	usable, err := isGatewayAPIKeyRuntimeUsableAt(runtime.APIKey, now)
	if err != nil {
		return GatewayRuntime{}, err
	}
	if !usable {
		return empty, nil
	}
	if runtime.GroupAccess != nil {
		groupUsable, err := isGroupUsageAccessRuntimeUsableAt(runtime.GroupAccess, now)
		if err != nil {
			return GatewayRuntime{}, err
		}
		if !groupUsable {
			return empty, nil
		}
	}
	accounts := []OpenAIAccountSecret{}
	for i := range runtime.Accounts {
		account := &runtime.Accounts[i]
		accountUsable, err := isOpenAIAccountRuntimeUsableAt(account, now)
		if err != nil {
			return GatewayRuntime{}, err
		}
		if !accountUsable {
			continue
		}
		accounts = append(accounts, CloneStaticOpenAIAccountSecret(*account))
	}
	return GatewayRuntime{
		APIKey:                     cloneGatewayAPIKeyRowPtr(runtime.APIKey),
		Settings:                   settings,
		GroupAccess:                cloneGroupUsageAccessPtr(runtime.GroupAccess),
		AccountDispatchDiagnostics: cloneDiagnostics(runtime.AccountDispatchDiagnostics),
		Accounts:                   accounts,
		ResponseInspectionPolicies: clonePoliciesPtr(runtime.ResponseInspectionPolicies),
	}, nil
}

// cloneStaticGatewayRuntime mirrors cloneStaticGatewayRuntime.
func cloneStaticGatewayRuntime(runtime GatewayRuntime) GatewayRuntime {
	return GatewayRuntime{
		APIKey:                     cloneGatewayAPIKeyRowPtr(runtime.APIKey),
		AccountDispatchDiagnostics: cloneDiagnostics(runtime.AccountDispatchDiagnostics),
		Settings:                   CloneGatewaySettings(runtime.Settings),
		GroupAccess:                cloneGroupUsageAccessPtr(runtime.GroupAccess),
		Accounts:                   cloneStaticOpenAIAccounts(runtime.Accounts),
		ResponseInspectionPolicies: clonePoliciesPtr(runtime.ResponseInspectionPolicies),
	}
}

// cloneGatewayRuntimeForDispatchAsync mirrors cloneGatewayRuntimeForDispatchAsync:
// the static clone with the accounts replaced by the concurrency overlay.
func (s *Service) cloneGatewayRuntimeForDispatchAsync(ctx context.Context, runtime GatewayRuntime) (GatewayRuntime, error) {
	accounts, err := s.cloneOpenAIAccountsWithCurrentConcurrency(ctx, runtime.Accounts)
	if err != nil {
		return GatewayRuntime{}, err
	}
	out := cloneStaticGatewayRuntime(runtime)
	out.Accounts = accounts
	return out, nil
}

// routeCachedDynamicGatewayRuntimeForDispatch mirrors
// routeCachedDynamicGatewayRuntimeForDispatch: fresh re-selection with the
// last-good fallback inside the hard-expired window.
func (s *Service) routeCachedDynamicGatewayRuntimeForDispatch(ctx context.Context, runtime GatewayRuntime) (GatewayRuntime, error) {
	if runtime.APIKey == nil {
		return cloneStaticGatewayRuntime(runtime), nil
	}
	selected, err := s.routeCachedDynamicGatewayRuntimeWithFreshSelection(ctx, runtime)
	if err != nil {
		if runtime.APIKey.SelectedGroupID == "" || runtime.GroupAccess == nil || len(runtime.Accounts) == 0 {
			return GatewayRuntime{}, err
		}
		s.warnEvent("gateway_dynamic_route_last_good_fallback", map[string]any{
			"err": err.Error(),
		}, "动态路由重新选组失败，保留硬过期窗口内上次已验证的分组和账户快照")
		return s.cloneGatewayRuntimeForDispatchAsync(ctx, runtime)
	}
	return selected, nil
}

// routeCachedDynamicGatewayRuntimeWithFreshSelection mirrors
// routeCachedDynamicGatewayRuntimeWithFreshSelection: ordered bindings, first
// group with access and (uniquely-candidate) dispatchable accounts wins.
func (s *Service) routeCachedDynamicGatewayRuntimeWithFreshSelection(ctx context.Context, runtime GatewayRuntime) (GatewayRuntime, error) {
	if runtime.APIKey == nil {
		return cloneStaticGatewayRuntime(runtime), nil
	}
	apiKey := CloneGatewayAPIKeyRow(*runtime.APIKey)
	systemAccountID := apiKey.SystemAccountID
	orderedBindings := apiKey.GroupBindings
	if s.opts.Orderer != nil {
		ordered, err := s.opts.Orderer.OrderAPIKeyGroupBindings(ctx, apiKey)
		if err != nil {
			return GatewayRuntime{}, err
		}
		orderedBindings = ordered
	}
	uniqueCandidateGroupIDs := []string{}
	seen := map[string]bool{}
	for _, binding := range orderedBindings {
		if binding.GroupID == "" || seen[binding.GroupID] {
			continue
		}
		seen[binding.GroupID] = true
		uniqueCandidateGroupIDs = append(uniqueCandidateGroupIDs, binding.GroupID)
	}
	if len(orderedBindings) > 0 {
		apiKey.GroupBindings = append([]GatewayAPIKeyGroupBindingRow(nil), orderedBindings...)
	}

	for _, groupID := range uniqueCandidateGroupIDs {
		groupAccess, err := s.ResolveCachedGroupUsageAccessMetadataAsync(ctx, groupID, systemAccountID)
		if err != nil {
			return GatewayRuntime{}, err
		}
		if groupAccess == nil {
			continue
		}
		accounts, err := s.ListCachedOpenAIAccountsForGroupAsync(ctx, groupID, systemAccountID, CachedOpenAIAccountsForGroupOptions{})
		if err != nil {
			return GatewayRuntime{}, err
		}
		if !hasDispatchableCachedGatewayAccount(accounts) && len(uniqueCandidateGroupIDs) > 1 {
			continue
		}
		selected := apiKey
		selected.SelectedGroupID = groupID
		return GatewayRuntime{
			APIKey:                     &selected,
			Settings:                   CloneGatewaySettings(runtime.Settings),
			GroupAccess:                cloneGroupUsageAccessPtr(groupAccess),
			Accounts:                   accounts,
			ResponseInspectionPolicies: []ResponseInspectionPolicySummary{},
		}, nil
	}

	return GatewayRuntime{
		APIKey:                     &apiKey,
		Settings:                   CloneGatewaySettings(runtime.Settings),
		Accounts:                   []OpenAIAccountSecret{},
		ResponseInspectionPolicies: []ResponseInspectionPolicySummary{},
	}, nil
}

// hasDispatchableCachedGatewayAccount mirrors hasDispatchableCachedGatewayAccount.
func hasDispatchableCachedGatewayAccount(accounts []OpenAIAccountSecret) bool {
	for i := range accounts {
		account := &accounts[i]
		if account.Status == AccountStatusActive && (account.ProxyProfileUnavailable == nil || !*account.ProxyProfileUnavailable) {
			return true
		}
	}
	return false
}

func cloneGatewayAPIKeyRowPtr(row *GatewayAPIKeyRow) *GatewayAPIKeyRow {
	if row == nil {
		return nil
	}
	cloned := CloneGatewayAPIKeyRow(*row)
	return &cloned
}

func cloneDiagnostics(diagnostics *OpenAIAccountsForGroupDiagnostics) *OpenAIAccountsForGroupDiagnostics {
	if diagnostics == nil {
		return nil
	}
	cloned := *diagnostics
	return &cloned
}

func clonePoliciesPtr(policies []ResponseInspectionPolicySummary) []ResponseInspectionPolicySummary {
	if policies == nil {
		return nil
	}
	return CloneResponseInspectionPolicies(policies)
}
