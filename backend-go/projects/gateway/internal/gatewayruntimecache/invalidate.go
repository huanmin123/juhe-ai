package gatewayruntimecache

import (
	"context"
	"time"
)

// ClearOptions mirrors clearGatewayRuntimeCacheLocal(options). ClearSettings
// nil keeps the Node default (clear), matching `options.clearSettings ?? true`.
type ClearOptions struct {
	ClearSettings     *bool
	ClearModelCatalog bool
}

// ClearGatewayRuntimeCache mirrors clearGatewayRuntimeCache: the reason drives
// the settings/model-catalog discrimination exactly like the Node reason set.
func (s *Service) ClearGatewayRuntimeCache(reason string) {
	s.ClearGatewayRuntimeCacheLocal(ClearOptions{
		ClearSettings:     boolPtr(shouldClearSettingsCacheForGatewayInvalidation(reason)),
		ClearModelCatalog: ShouldInvalidateProviderModelCatalog(reason),
	})
}

// ClearGatewayRuntimeCacheLocal mirrors clearGatewayRuntimeCacheLocal: every
// generation advances so in-flight loads cannot repopulate stale entries.
func (s *Service) ClearGatewayRuntimeCacheLocal(options ClearOptions) {
	s.mu.Lock()
	s.runtimeGeneration++
	s.apiKeyRuntimeGeneration++
	s.pendingRuntimeLoads = map[string]*runtimeLoad{}
	s.pendingGroupRefreshes = map[string]*refreshCall{}
	s.pendingAccountRefreshes = map[string]*refreshCall{}
	s.pendingInspectRefreshes = map[string]*refreshCall{}
	s.mu.Unlock()

	s.runtimeCache.clear()
	s.identityCache.clear()
	s.settingsCache.clear()
	s.groupCache.clear()
	s.accountsCache.clear()
	s.inspectCache.clear()

	clearCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	s.clearSharedSettings(clearCtx)
	s.clearSharedGroupAccess(clearCtx)
	s.clearSharedInspection(clearCtx)

	if options.ClearModelCatalog {
		s.mu.Lock()
		s.catalogGeneration++
		s.pendingCatalogLoads = map[string]*catalogLoad{}
		s.mu.Unlock()
		s.catalogCache.clear()
		s.routeIdxCache.clear()
		s.clearSharedCatalog(clearCtx)
		s.clearSharedRouteIndex(clearCtx)
	}

	clearSettings := options.ClearSettings == nil || *options.ClearSettings
	if clearSettings && s.opts.ClearSettingsCache != nil {
		s.opts.ClearSettingsCache()
	}
}

// InvalidateGatewayRuntimeCacheByAPIKeyID mirrors
// invalidateGatewayRuntimeCacheByApiKeyId: the unbounded process-local epoch
// advances on every targeted invalidation so a bounded marker can never
// re-admit an in-flight DB read.
func (s *Service) InvalidateGatewayRuntimeCacheByAPIKeyID(apiKeyID string, keyHashes []string) {
	s.mu.Lock()
	s.apiKeyRuntimeGeneration++
	s.mu.Unlock()

	cacheKeys := map[string]struct{}{}
	for _, keyHash := range keyHashes {
		if keyHash != "" {
			cacheKeys[keyHash] = struct{}{}
		}
	}
	if apiKeyID != "" {
		s.keysMu.Lock()
		for cacheKey := range s.keysByAPIKeyID[apiKeyID] {
			cacheKeys[cacheKey] = struct{}{}
		}
		s.keysMu.Unlock()
	}
	if len(cacheKeys) == 0 {
		s.mu.Lock()
		s.pendingRuntimeLoads = map[string]*runtimeLoad{}
		s.mu.Unlock()
		s.runtimeCache.clear()
		s.identityCache.clear()
		return
	}
	s.mu.Lock()
	for cacheKey := range cacheKeys {
		delete(s.pendingRuntimeLoads, cacheKey)
	}
	s.mu.Unlock()
	for cacheKey := range cacheKeys {
		s.runtimeCache.delete(cacheKey)
	}
}

func boolPtr(value bool) *bool { return &value }

// remaining shared clears (mirrors of clearSharedJsonCacheInBackground calls)

func (s *Service) clearSharedGroupAccess(ctx context.Context) {
	if s.sharedGroup == nil {
		return
	}
	if err := s.sharedGroup.Clear(ctx); err != nil {
		s.logSharedFailure("gateway_group_usage_access_shared_cache_clear_failed", err)
	}
}

func (s *Service) clearSharedInspection(ctx context.Context) {
	if s.sharedInspection == nil {
		return
	}
	if err := s.sharedInspection.Clear(ctx); err != nil {
		s.logSharedFailure("gateway_response_inspection_policy_shared_cache_clear_failed", err)
	}
}

func (s *Service) clearSharedCatalog(ctx context.Context) {
	if s.sharedCatalog == nil {
		return
	}
	if err := s.sharedCatalog.Clear(ctx); err != nil {
		s.logSharedFailure("gateway_provider_model_catalog_shared_cache_clear_failed", err)
	}
}

func (s *Service) clearSharedRouteIndex(ctx context.Context) {
	if s.sharedRouteIdx == nil {
		return
	}
	if err := s.sharedRouteIdx.Clear(ctx); err != nil {
		s.logSharedFailure("gateway_provider_model_route_index_shared_cache_clear_failed", err)
	}
}
