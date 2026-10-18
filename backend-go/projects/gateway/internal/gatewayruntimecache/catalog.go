package gatewayruntimecache

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/safego"
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
		defer safego.Recover("gatewayruntimecache.catalog.background_load")
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
// models 单表目录路由（供应商与上游协议面重构设计 §2.1/§3/§6.1）
// ---------------------------------------------------------------------------

// modelsDirectoryEntry 是目录集合内的单条可见性行（scope + personal 属主）。
type modelsDirectoryEntry struct {
	scope           string
	systemAccountID string
}

// modelsDirectoryActiveSet 是 models 目录可见性集合缓存载荷：全部
// scope IN ('builtin','global','personal') 且 status='active' 行按归一化
// public_model 分桶。集合构造后只读，map 并发读安全。
type modelsDirectoryActiveSet struct {
	models map[string][]modelsDirectoryEntry
}

// IsModelVisibleInDirectory 判定 owner（资源账户 system_account_id）对 model
// 的目录可见性（设计 §2.1）：builtin/global 对所有资源账户可见；personal 仅
// models.system_account_id 与 owner 精确一致（双方非空，NULL 属主行不可见）
// 可见；不在集合内（不存在、scope 外或非 active）一律不可见。模型键与装载
// 同源归一化（TrimSpace），binary 匹配由 Go map 精确相等完成。
func (set modelsDirectoryActiveSet) IsModelVisibleInDirectory(owner string, model string) bool {
	for _, entry := range set.models[normalizeProviderModelRouteKey(model)] {
		switch entry.scope {
		case "builtin", "global":
			return true
		case "personal":
			if owner != "" && entry.systemAccountID == owner {
				return true
			}
		}
	}
	return false
}

// sharedModelsDirectoryActiveSet 是共享缓存（Redis）的 JSON 往返形态。
type sharedModelsDirectoryActiveSet struct {
	Models []sharedModelsDirectoryEntry `json:"models"`
}

type sharedModelsDirectoryEntry struct {
	Model           string `json:"model"`
	Scope           string `json:"scope"`
	SystemAccountID string `json:"systemAccountId,omitempty"`
}

// modelsDirectoryActiveSetCacheKey 是目录集合在本地/共享缓存中的业务键。v1
// 只装 builtin（旧键 active-builtin），可见性补齐 global/personal 后改名，
// 使旧语义的存量共享条目自然过期、永不被新判定读到。
const modelsDirectoryActiveSetCacheKey = "active-visible"

// ResolveCachedProviderModelRouteAsync 承载 models 单表目录语义：matched =
// 请求模型对 systemAccountID 资源账户可见（设计 §2.1：builtin/global 全可见、
// personal 仅属主可见）且 status='active'，missing = 无任何可见 active 行
// （终态 404 model_not_found 数据面）。入参 includeUnpriced 是供应商收窄时
// 代的过渡载体，供应商解耦后不参与判定（分组不再携带 provider 身份，见设计
// §8）；systemAccountID 是可见性 owner，随目录判定消费。
func (s *Service) ResolveCachedProviderModelRouteAsync(ctx context.Context, model string, systemAccountID string, includeUnpriced bool) (ProviderModelRouteResolution, error) {
	_ = includeUnpriced
	modelKey := normalizeProviderModelRouteKey(model)
	if modelKey == "" {
		return ProviderModelRouteResolution{
			Outcome:  ProviderModelRouteMissing,
			ModelKey: modelKey,
		}, nil
	}
	present, err := s.ResolveCachedGatewayModelsDirectoryActiveAsync(ctx, systemAccountID, modelKey)
	if err != nil {
		return ProviderModelRouteResolution{}, err
	}
	outcome := ProviderModelRouteMissing
	if present {
		outcome = ProviderModelRouteMatched
	}
	return ProviderModelRouteResolution{
		Outcome:  outcome,
		ModelKey: modelKey,
	}, nil
}

// ResolveCachedGatewayModelsDirectoryActiveAsync 报告请求模型（public_model
// 精确 binary 匹配）在 models 单表目录中是否对 owner（资源账户
// system_account_id，空串 = 无属主上下文，仅 builtin/global 可见）可见且
// status='active'。进程内集合缓存 + 可选共享缓存，失效随目录代数推进
// （ClearModelCatalog）。
func (s *Service) ResolveCachedGatewayModelsDirectoryActiveAsync(ctx context.Context, owner string, model string) (bool, error) {
	s.syncInvalidationsBestEffort(ctx)
	modelKey := normalizeProviderModelRouteKey(model)
	if modelKey == "" {
		return false, nil
	}
	generation := s.currentCatalogGeneration()
	if entry, ok := s.modelsDirCache.get(modelsDirectoryActiveSetCacheKey); ok {
		return entry.IsModelVisibleInDirectory(owner, modelKey), nil
	}
	if s.sharedModelsDir != nil {
		shared, ok, err := s.getSharedModelsDirectoryActiveSet(ctx)
		if err != nil {
			s.logSharedFailure("gateway_models_directory_shared_cache_read_failed", err)
		}
		if ok {
			if s.isCatalogGenerationCurrent(generation) {
				s.modelsDirCache.set(modelsDirectoryActiveSetCacheKey, shared, providerModelCatalogTTL)
			}
			return shared.IsModelVisibleInDirectory(owner, modelKey), nil
		}
	}
	set, err := s.loadModelsDirectoryActiveSet(ctx)
	if err != nil {
		return false, err
	}
	// 代数复核：装载期间目录被失效则不落任何缓存（与目录装载的 generation
	// 守卫同款，避免旧集合在失效后存续）。
	if s.isCatalogGenerationCurrent(generation) {
		s.modelsDirCache.set(modelsDirectoryActiveSetCacheKey, set, providerModelCatalogTTL)
		s.setSharedModelsDirectoryActiveSet(ctx, set)
	}
	return set.IsModelVisibleInDirectory(owner, modelKey), nil
}

// loadModelsDirectoryActiveSet 装载 models 目录可见性集合；public_model 归一
// 化与路由键同源（TrimSpace），binary 匹配由 Go map 精确相等完成。读模型未
// 实装 ModelsDirectoryReader 时显式报错，不做静默降级。
func (s *Service) loadModelsDirectoryActiveSet(ctx context.Context) (modelsDirectoryActiveSet, error) {
	reader, ok := s.models.(ModelsDirectoryReader)
	if !ok {
		return modelsDirectoryActiveSet{}, errors.New("gatewayruntimecache 读模型未实现 models 目录读取（ModelsDirectoryReader）")
	}
	entries, err := reader.ListVisibleModelsDirectoryEntries(ctx)
	if err != nil {
		return modelsDirectoryActiveSet{}, err
	}
	set := modelsDirectoryActiveSet{models: make(map[string][]modelsDirectoryEntry)}
	for _, entry := range entries {
		key := normalizeProviderModelRouteKey(entry.PublicModel)
		if key == "" {
			continue
		}
		set.models[key] = append(set.models[key], modelsDirectoryEntry{scope: entry.Scope, systemAccountID: entry.SystemAccountID})
	}
	return set, nil
}

func (s *Service) getSharedModelsDirectoryActiveSet(ctx context.Context) (modelsDirectoryActiveSet, bool, error) {
	var decoded sharedModelsDirectoryActiveSet
	ok, err := s.sharedModelsDir.Get(ctx, modelsDirectoryActiveSetCacheKey, &decoded)
	if err != nil || !ok {
		return modelsDirectoryActiveSet{}, false, err
	}
	set := modelsDirectoryActiveSet{models: make(map[string][]modelsDirectoryEntry, len(decoded.Models))}
	for _, model := range decoded.Models {
		key := normalizeProviderModelRouteKey(model.Model)
		if key == "" {
			continue
		}
		set.models[key] = append(set.models[key], modelsDirectoryEntry{scope: model.Scope, systemAccountID: model.SystemAccountID})
	}
	return set, true, nil
}

func (s *Service) setSharedModelsDirectoryActiveSet(ctx context.Context, set modelsDirectoryActiveSet) {
	if s.sharedModelsDir == nil {
		return
	}
	shared := sharedModelsDirectoryActiveSet{Models: make([]sharedModelsDirectoryEntry, 0, len(set.models))}
	for model, entries := range set.models {
		for _, entry := range entries {
			shared.Models = append(shared.Models, sharedModelsDirectoryEntry{Model: model, Scope: entry.scope, SystemAccountID: entry.systemAccountID})
		}
	}
	sort.Slice(shared.Models, func(left, right int) bool {
		l, r := shared.Models[left], shared.Models[right]
		if l.Model != r.Model {
			return l.Model < r.Model
		}
		if l.Scope != r.Scope {
			return l.Scope < r.Scope
		}
		return l.SystemAccountID < r.SystemAccountID
	})
	setCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := s.sharedModelsDir.Set(setCtx, modelsDirectoryActiveSetCacheKey, shared, providerModelCatalogTTL); err != nil {
		s.logSharedFailure("gateway_models_directory_shared_cache_write_failed", err)
	}
}
