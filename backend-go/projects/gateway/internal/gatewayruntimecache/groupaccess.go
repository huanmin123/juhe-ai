package gatewayruntimecache

import (
	"context"
	"time"
)

// ---------------------------------------------------------------------------
// group usage access cache (Node resolveCachedGroupUsageAccessMetadata /
// resolveCachedGroupUsageAccessMetadataAsync)
// ---------------------------------------------------------------------------

// ResolveCachedGroupUsageAccessMetadata mirrors the sync local-mode read:
// negative (false) entries are cached with the invalid runtime TTL.
func (s *Service) ResolveCachedGroupUsageAccessMetadata(ctx context.Context, groupID, systemAccountID string) (*GroupUsageAccessMetadata, error) {
	cacheKey := gatewayCacheKey(groupID, systemAccountID)
	if cached, ok := s.groupCache.get(cacheKey); ok {
		return s.groupUsageAccessFromCacheEntry(cached), nil
	}
	value, err := s.models.ResolveGroupUsageAccessMetadata(ctx, groupID, systemAccountID)
	if err != nil {
		return nil, err
	}
	entry, entryErr := newGroupUsageAccessCacheEntry(value, s.nowMs())
	if entryErr != nil {
		return nil, entryErr
	}
	s.groupCache.set(cacheKey, entry, groupUsageAccessRetainTTL)
	return cloneGroupUsageAccessPtr(value), nil
}

// ResolveCachedGroupUsageAccessMetadataAsync mirrors the async read: stale
// entries serve as last-good while a background refresh runs (the Node
// shouldAllowStaleGatewayRuntimeFallback() gate is constantly true).
func (s *Service) ResolveCachedGroupUsageAccessMetadataAsync(ctx context.Context, groupID, systemAccountID string) (*GroupUsageAccessMetadata, error) {
	s.syncInvalidationsBestEffort(ctx)
	if s.sharedGroup == nil {
		return s.ResolveCachedGroupUsageAccessMetadata(ctx, groupID, systemAccountID)
	}
	cacheKey := gatewayCacheKey(groupID, systemAccountID)
	if cached, ok := s.groupCache.get(cacheKey); ok {
		return s.staleAwareGroupAccess(groupID, systemAccountID, cacheKey, cached), nil
	}
	shared, ok, err := s.getSharedGroupAccess(ctx, cacheKey)
	if err != nil {
		s.logSharedFailure("gateway_group_usage_access_shared_cache_read_failed", err)
	}
	if ok {
		s.groupCache.set(cacheKey, cloneGroupUsageAccessCacheEntry(shared), groupUsageAccessRetainTTL)
		return s.staleAwareGroupAccess(groupID, systemAccountID, cacheKey, shared), nil
	}
	return s.loadGroupUsageAccessMetadataAndPopulateCache(ctx, groupID, systemAccountID, cacheKey)
}

// staleAwareGroupAccess mirrors the stale-fallback branch: fresh values return
// directly; stale values trigger the deduped background refresh and return as
// last-good.
func (s *Service) staleAwareGroupAccess(groupID, systemAccountID, cacheKey string, entry groupUsageAccessCacheEntry) *GroupUsageAccessMetadata {
	if !isEntryFresh(entry.revalidateAtMs, s.nowMs()) {
		s.refreshGroupUsageAccessMetadataInBackground(groupID, systemAccountID, cacheKey)
	}
	return s.groupUsageAccessFromCacheEntry(entry)
}

func (s *Service) getSharedGroupAccess(ctx context.Context, cacheKey string) (groupUsageAccessCacheEntry, bool, error) {
	var decoded sharedGroupUsageAccessEntry
	ok, err := s.sharedGroup.Get(ctx, cacheKey, &decoded)
	if err != nil || !ok {
		return groupUsageAccessCacheEntry{}, false, err
	}
	return decoded.toEntry(), true, nil
}

// sharedGroupUsageAccessEntry is the shared-cache JSON shape; value null is
// the Node `false` negative entry.
type sharedGroupUsageAccessEntry struct {
	Value          *GroupUsageAccessMetadata `json:"value"`
	RevalidateAtMs int64                     `json:"revalidateAtMs"`
}

func (e sharedGroupUsageAccessEntry) toEntry() groupUsageAccessCacheEntry {
	if e.Value == nil {
		return groupUsageAccessCacheEntry{value: nil, revalidateAtMs: e.RevalidateAtMs}
	}
	clone := CloneGroupUsageAccessMetadata(*e.Value)
	return groupUsageAccessCacheEntry{value: &clone, revalidateAtMs: e.RevalidateAtMs}
}

func (e groupUsageAccessCacheEntry) toShared() sharedGroupUsageAccessEntry {
	if e.value == nil {
		return sharedGroupUsageAccessEntry{Value: nil, RevalidateAtMs: e.revalidateAtMs}
	}
	clone := CloneGroupUsageAccessMetadata(*e.value)
	return sharedGroupUsageAccessEntry{Value: &clone, RevalidateAtMs: e.revalidateAtMs}
}

// newGroupUsageAccessCacheEntry mirrors groupUsageAccessCacheEntry: positive
// entries take the expiry-bounded TTL, negatives the invalid runtime window.
func newGroupUsageAccessCacheEntry(value *GroupUsageAccessMetadata, now int64) (groupUsageAccessCacheEntry, error) {
	if value == nil {
		return groupUsageAccessCacheEntry{value: nil, revalidateAtMs: now + invalidGatewayRuntimeTTL.Milliseconds()}, nil
	}
	ttl, err := groupUsageAccessCacheTTL(*value, now)
	if err != nil {
		return groupUsageAccessCacheEntry{}, err
	}
	clone := CloneGroupUsageAccessMetadata(*value)
	return groupUsageAccessCacheEntry{value: &clone, revalidateAtMs: now + ttl.Milliseconds()}, nil
}

func cloneGroupUsageAccessCacheEntry(entry groupUsageAccessCacheEntry) groupUsageAccessCacheEntry {
	return groupUsageAccessCacheEntry{
		value:          cloneGroupUsageAccessPtr(entry.value),
		revalidateAtMs: entry.revalidateAtMs,
	}
}

func cloneGroupUsageAccessPtr(value *GroupUsageAccessMetadata) *GroupUsageAccessMetadata {
	if value == nil {
		return nil
	}
	clone := CloneGroupUsageAccessMetadata(*value)
	return &clone
}

// groupUsageAccessFromCacheEntry mirrors groupUsageAccessFromCacheEntry:
// negative entries and expired authorizations both read as undefined.
func (s *Service) groupUsageAccessFromCacheEntry(entry groupUsageAccessCacheEntry) *GroupUsageAccessMetadata {
	if entry.value == nil {
		return nil
	}
	usable, err := isGroupUsageAccessRuntimeUsableAt(entry.value, s.nowMs())
	if err != nil || !usable {
		return nil
	}
	return cloneGroupUsageAccessPtr(entry.value)
}

// loadGroupUsageAccessMetadataAndPopulateCache mirrors the Node loader:
// generation-guarded populate so an invalidation during the read cannot
// repopulate a stale entry.
func (s *Service) loadGroupUsageAccessMetadataAndPopulateCache(ctx context.Context, groupID, systemAccountID, cacheKey string) (*GroupUsageAccessMetadata, error) {
	generation := s.currentRuntimeGeneration()
	value, err := s.models.ResolveGroupUsageAccessMetadata(ctx, groupID, systemAccountID)
	if err != nil {
		return nil, err
	}
	if s.isRuntimeGenerationCurrent(generation) {
		entry, entryErr := newGroupUsageAccessCacheEntry(value, s.nowMs())
		if entryErr != nil {
			return nil, entryErr
		}
		s.setGroupUsageAccessCacheEntry(cacheKey, entry)
	}
	return cloneGroupUsageAccessPtr(value), nil
}

// setGroupUsageAccessCacheEntry mirrors setGroupUsageAccessCacheEntryAsync.
func (s *Service) setGroupUsageAccessCacheEntry(cacheKey string, entry groupUsageAccessCacheEntry) {
	cached := cloneGroupUsageAccessCacheEntry(entry)
	s.groupCache.set(cacheKey, cached, groupUsageAccessRetainTTL)
	if s.sharedGroup == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := s.sharedGroup.Set(ctx, cacheKey, cached.toShared(), groupUsageAccessRetainTTL); err != nil {
		s.logSharedFailure("gateway_group_usage_access_shared_cache_write_failed", err)
	}
}

// refreshGroupUsageAccessMetadataInBackground mirrors
// refreshGroupUsageAccessMetadataInBackground: dedupe by cacheKey while a
// refresh is in flight; failures keep the bounded snapshot.
func (s *Service) refreshGroupUsageAccessMetadataInBackground(groupID, systemAccountID, cacheKey string) {
	s.mu.Lock()
	if _, pending := s.pendingGroupRefreshes[cacheKey]; pending {
		s.mu.Unlock()
		return
	}
	// s.mu 已持有：直接读世代字段，禁止重入 currentRuntimeGeneration。
	generation := s.runtimeGeneration
	call := newRefreshCall()
	s.pendingGroupRefreshes[cacheKey] = call
	s.mu.Unlock()

	go func() {
		defer func() {
			s.mu.Lock()
			delete(s.pendingGroupRefreshes, cacheKey)
			s.mu.Unlock()
			call.finish()
		}()
		ctx, cancel := context.WithTimeout(context.Background(), GatewayRuntimeLoadTimeout)
		defer cancel()
		value, err := s.models.ResolveGroupUsageAccessMetadata(ctx, groupID, systemAccountID)
		if err != nil {
			s.warnEvent("gateway_group_access_stale_refresh_failed", map[string]any{
				"groupId": groupID, "systemAccountId": systemAccountID, "err": err.Error(),
			}, "网关分组访问元数据后台刷新失败，保留当前有界缓存快照")
			return
		}
		if !s.isRuntimeGenerationCurrent(generation) {
			return
		}
		entry, entryErr := newGroupUsageAccessCacheEntry(value, s.nowMs())
		if entryErr != nil {
			s.warnEvent("gateway_group_access_stale_refresh_failed", map[string]any{
				"groupId": groupID, "systemAccountId": systemAccountID, "err": entryErr.Error(),
			}, "网关分组访问元数据后台刷新失败，保留当前有界缓存快照")
			return
		}
		s.setGroupUsageAccessCacheEntry(cacheKey, entry)
	}()
}

func (s *Service) warnEvent(event string, fields map[string]any, message string) {
	if s.logger != nil {
		s.logger.Warn(event, fields, message)
	}
}
