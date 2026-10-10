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
	// 分组模型并集缓存无条件随运行时失效推进（网关模型列表账户并集设计 §6.3，
	// 不使用目录 reason 白名单）。专用发布锁内单临界区完成：旧版本键捕获 → 代
	// 推进 → pending 清空 → 本地清空，与本地发布互斥（目录缓存“检查与写入分两
	// 次加锁”的窗口不得复现）。旧代共享桶异步尽力回收：桶名含旧 token/generation，
	// 当前代桶名不同、不会被误删；失败记 WARN——旧代键已因版本键不可达，删除
	// 只负责空间回收。
	s.unionPublicationMu.Lock()
	previousUnionVersionKey := s.unionVersionKeyLocked()
	s.unionGeneration++
	s.pendingUnionLoads = map[string]*unionLoad{}
	s.unionCache.clear()
	s.unionPublicationMu.Unlock()
	if s.opts.Shared != nil {
		go s.cleanupUnionSharedBucket(previousUnionVersionKey)
	}

	s.mu.Lock()
	s.runtimeGeneration++
	s.apiKeyRuntimeGeneration++
	s.pendingRuntimeLoads = map[string]*runtimeLoad{}
	s.pendingGroupRefreshes = map[string]*refreshCall{}
	s.pendingInspectRefreshes = map[string]*refreshCall{}
	s.pendingAccountRefreshes = map[string]*refreshCall{}
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
