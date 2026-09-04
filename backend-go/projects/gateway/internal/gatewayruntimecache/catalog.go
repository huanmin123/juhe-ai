package gatewayruntimecache

import (
	"context"
	"time"
)

// ---------------------------------------------------------------------------
// provider model catalog cache (Node listCachedProviderModelCatalogAsync)
// ---------------------------------------------------------------------------

// catalogLoad mirrors PendingGatewayRuntimeLoad for the catalog slice: a
// singleflight entry carrying its own generation.
type catalogLoad struct {
	generation int64
	done       chan struct{}
	items      []ProviderModelCatalogItem
	err        error
}

// ListCachedProviderModelCatalogAsync mirrors listCachedProviderModelCatalogAsync:
// local (memory mode) or shared (redis mode) cache, deduped loads, and a
// generation guard so a clear during the load leaves no stale entry behind.
func (s *Service) ListCachedProviderModelCatalogAsync(ctx context.Context, input ModelCatalogListOptions) ([]ProviderModelCatalogItem, error) {
	s.syncInvalidationsBestEffort(ctx)
	generation := s.currentCatalogGeneration()
	cacheKey := providerModelCatalogCacheKey(input)
	if s.sharedCatalog == nil {
		if cached, ok := s.catalogCache.get(cacheKey); ok {
			return CloneProviderModelCatalogItems(cached), nil
		}
	} else {
		shared, ok, err := s.getSharedCatalog(ctx, cacheKey)
		if err != nil {
			s.logSharedFailure("gateway_provider_model_catalog_shared_cache_read_failed", err)
		}
		if ok && shared != nil {
			if s.isCatalogGenerationCurrent(generation) {
				s.catalogCache.set(cacheKey, CloneProviderModelCatalogItems(shared), providerModelCatalogTTL)
			}
			return CloneProviderModelCatalogItems(shared), nil
		}
	}

	s.mu.Lock()
	if pending, ok := s.pendingCatalogLoads[cacheKey]; ok {
		s.mu.Unlock()
		if err := awaitCatalogLoad(ctx, pending); err != nil {
			return nil, err
		}
		return CloneProviderModelCatalogItems(pending.items), nil
	}
	load := &catalogLoad{generation: generation, done: make(chan struct{})}
	s.pendingCatalogLoads[cacheKey] = load
	s.mu.Unlock()

	go func() {
		defer func() {
			s.mu.Lock()
			if s.pendingCatalogLoads[cacheKey] == load {
				delete(s.pendingCatalogLoads, cacheKey)
			}
			s.mu.Unlock()
			close(load.done)
		}()
		loadCtx, cancel := context.WithTimeout(context.Background(), GatewayRuntimeLoadTimeout)
		defer cancel()
		items, err := s.models.ListProviderModelCatalog(loadCtx, input)
		if err != nil {
			load.err = err
			return
		}
		if s.isCatalogGenerationCurrent(load.generation) {
			s.setCatalogCacheEntry(cacheKey, items)
		}
		load.items = items
	}()

	if err := awaitCatalogLoad(ctx, load); err != nil {
		return nil, err
	}
	return CloneProviderModelCatalogItems(load.items), nil
}

func awaitCatalogLoad(ctx context.Context, load *catalogLoad) error {
	select {
	case <-load.done:
		return load.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Service) getSharedCatalog(ctx context.Context, cacheKey string) ([]ProviderModelCatalogItem, bool, error) {
	var decoded []ProviderModelCatalogItem
	ok, err := s.sharedCatalog.Get(ctx, cacheKey, &decoded)
	if err != nil || !ok {
		return nil, false, err
	}
	return decoded, true, nil
}

// setCatalogCacheEntry mirrors setProviderModelCatalogCacheEntryAsync.
func (s *Service) setCatalogCacheEntry(cacheKey string, items []ProviderModelCatalogItem) {
	cached := CloneProviderModelCatalogItems(items)
	s.catalogCache.set(cacheKey, cached, providerModelCatalogTTL)
	if s.sharedCatalog == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := s.sharedCatalog.Set(ctx, cacheKey, CloneProviderModelCatalogItems(cached), providerModelCatalogTTL); err != nil {
		s.logSharedFailure("gateway_provider_model_catalog_shared_cache_write_failed", err)
	}
}

// ---------------------------------------------------------------------------
// provider model route index (Node resolveCachedProviderModelRouteAsync)
// ---------------------------------------------------------------------------

// ResolveCachedProviderModelRouteAsync mirrors
// resolveCachedProviderModelRouteAsync: normalized model key against the
// provider-code index built from the cached catalogs.
func (s *Service) ResolveCachedProviderModelRouteAsync(ctx context.Context, model string, providerCodes []string, systemAccountID string, includeUnpriced bool) (ProviderModelRouteResolution, error) {
	s.syncInvalidationsBestEffort(ctx)
	generation := s.currentCatalogGeneration()
	modelKey := normalizeProviderModelRouteKey(model)
	codes := normalizedProviderRouteCodes(providerCodes)
	if modelKey == "" || len(codes) == 0 {
		return ProviderModelRouteResolution{
			Outcome:              ProviderModelRouteMissing,
			ModelKey:             modelKey,
			MatchedProviderCodes: []string{},
		}, nil
	}
	cacheKey := providerModelRouteIndexCacheKey(codes, systemAccountID, includeUnpriced)

	var cached *providerModelRouteIndexCacheEntry
	if s.sharedRouteIdx == nil {
		if entry, ok := s.routeIdxCache.get(cacheKey); ok {
			cloned := cloneProviderModelRouteIndexCacheEntry(entry)
			cached = &cloned
		}
	}
	if cached == nil && s.sharedRouteIdx != nil {
		shared, ok, err := s.getSharedRouteIndex(ctx, cacheKey)
		if err != nil {
			s.logSharedFailure("gateway_provider_model_route_index_shared_cache_read_failed", err)
		}
		if ok {
			cloned := cloneProviderModelRouteIndexCacheEntry(shared)
			cached = &cloned
			if s.isCatalogGenerationCurrent(generation) {
				s.routeIdxCache.set(cacheKey, cloneProviderModelRouteIndexCacheEntry(shared), providerModelCatalogTTL)
			}
		}
	}
	if cached == nil {
		built, err := s.buildProviderModelRouteIndex(ctx, codes, systemAccountID, includeUnpriced)
		if err != nil {
			return ProviderModelRouteResolution{}, err
		}
		entry := providerModelRouteIndexCacheEntry{index: built}
		if s.isCatalogGenerationCurrent(generation) {
			s.setRouteIndexSharedCacheEntry(ctx, cacheKey, entry)
			s.routeIdxCache.set(cacheKey, cloneProviderModelRouteIndexCacheEntry(entry), providerModelCatalogTTL)
		}
		cached = &entry
	}

	matched := cached.index[modelKey]
	if matched == nil {
		matched = []string{}
	}
	if len(matched) == 1 {
		return ProviderModelRouteResolution{
			Outcome:              ProviderModelRouteMatched,
			ModelKey:             modelKey,
			ProviderCode:         matched[0],
			MatchedProviderCodes: matched,
		}, nil
	}
	outcome := ProviderModelRouteMissing
	if len(matched) > 1 {
		outcome = ProviderModelRouteAmbiguous
	}
	return ProviderModelRouteResolution{
		Outcome:              outcome,
		ModelKey:             modelKey,
		MatchedProviderCodes: matched,
	}, nil
}

// buildProviderModelRouteIndex mirrors buildProviderModelRouteIndex: model key
// -> sorted unique provider codes.
func (s *Service) buildProviderModelRouteIndex(ctx context.Context, providerCodes []string, systemAccountID string, includeUnpriced bool) (map[string][]string, error) {
	set := map[string]map[string]bool{}
	for _, providerCode := range providerCodes {
		catalog, err := s.ListCachedProviderModelCatalogAsync(ctx, ModelCatalogListOptions{
			ProviderCode:    providerCode,
			SystemAccountID: systemAccountID,
			IncludeUnpriced: includeUnpriced,
		})
		if err != nil {
			return nil, err
		}
		for _, item := range catalog {
			modelKey := normalizeProviderModelRouteKey(item.Model)
			if modelKey == "" {
				continue
			}
			if set[modelKey] == nil {
				set[modelKey] = map[string]bool{}
			}
			set[modelKey][providerCode] = true
		}
	}
	index := make(map[string][]string, len(set))
	for modelKey, codes := range set {
		list := make([]string, 0, len(codes))
		for code := range codes {
			list = append(list, code)
		}
		sortStrings(list)
		index[modelKey] = list
	}
	return index, nil
}

func cloneProviderModelRouteIndexCacheEntry(entry providerModelRouteIndexCacheEntry) providerModelRouteIndexCacheEntry {
	index := make(map[string][]string, len(entry.index))
	for modelKey, codes := range entry.index {
		index[modelKey] = append([]string(nil), codes...)
	}
	return providerModelRouteIndexCacheEntry{index: index}
}

// sharedProviderModelRouteIndexEntry mirrors
// ProviderModelRouteIndexSharedCacheEntry (entries array round-trip).
type sharedProviderModelRouteIndexEntry struct {
	Entries [][2]any `json:"entries"`
}

func (e sharedProviderModelRouteIndexEntry) toEntry() providerModelRouteIndexCacheEntry {
	index := map[string][]string{}
	for _, pair := range e.Entries {
		if len(pair) != 2 {
			continue
		}
		modelKey, okModel := pair[0].(string)
		codesAny, okCodes := pair[1].([]any)
		if !okModel || !okCodes {
			continue
		}
		codes := make([]string, 0, len(codesAny))
		for _, codeAny := range codesAny {
			if code, ok := codeAny.(string); ok {
				codes = append(codes, code)
			}
		}
		index[modelKey] = codes
	}
	return providerModelRouteIndexCacheEntry{index: index}
}

func providerModelRouteIndexToShared(entry providerModelRouteIndexCacheEntry) sharedProviderModelRouteIndexEntry {
	shared := sharedProviderModelRouteIndexEntry{Entries: [][2]any{}}
	for modelKey, codes := range entry.index {
		shared.Entries = append(shared.Entries, [2]any{modelKey, codes})
	}
	return shared
}

func (s *Service) getSharedRouteIndex(ctx context.Context, cacheKey string) (providerModelRouteIndexCacheEntry, bool, error) {
	var decoded sharedProviderModelRouteIndexEntry
	ok, err := s.sharedRouteIdx.Get(ctx, cacheKey, &decoded)
	if err != nil || !ok {
		return providerModelRouteIndexCacheEntry{}, false, err
	}
	return decoded.toEntry(), true, nil
}

// setRouteIndexSharedCacheEntry mirrors setProviderModelRouteIndexSharedCacheEntry.
func (s *Service) setRouteIndexSharedCacheEntry(ctx context.Context, cacheKey string, entry providerModelRouteIndexCacheEntry) {
	if s.sharedRouteIdx == nil {
		return
	}
	if err := s.sharedRouteIdx.Set(ctx, cacheKey, providerModelRouteIndexToShared(entry), providerModelCatalogTTL); err != nil {
		s.logSharedFailure("gateway_provider_model_route_index_shared_cache_write_failed", err)
	}
}
