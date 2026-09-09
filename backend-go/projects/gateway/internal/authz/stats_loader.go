// stats_loader.go 移植 Node loadResourceAuthorizationStatsByResourceIds(Async)
// （storage/authorization-read-loaders.ts:71/:120，verdict-ak/ap TRULY_MISSING
// 补齐）：按资源 ID 批量聚合 resource_authorizations 的活跃授权数与团队来源数，
// 注入账户管理面摘要的 authorizationCount / authorizationTeamCount /
// authorizationUsageAvailable 投影（Node account-summary.repository.ts:1444,
// 1608-1610）。
//
// 缓存语义与 Node 同等：本地 LRU（max 10000、TTL 5min、updateAgeOnGet，键
// "resourceType:resourceId"），聚合 SQL 与 Node 逐列一致（活跃授权 COUNT(DISTINCT
// ra.id)，团队授权 COUNT(DISTINCT active team source_team_id)），缺失资源 ID
// 聚合为 {0,0} 且零值同样入缓存（Node emptyAuthorizationStats 分支）。Node 的
// Redis shared-cache 第二层服务多进程 db-service 架构，Go 单进程 gateway 按既有
// 判定（verdict-ap loadResourceAuthorizationSourcesByAuthorizationIds 行）以
// 进程内缓存取代；归档 Node 无生产调用方接线 stats 失效（仅 TTL 过期），本实现
// 保持同一失效语义并额外暴露 InvalidateResourceAuthorizationStatsCache 供写侧
// 后续接线。
package authz

import (
	"container/list"
	"context"
	"sync"
	"time"
)

// 授权统计缓存常量（Node authorizationStatsCache / authorizationStatsSharedCache）。
const (
	authorizationStatsCacheMax  = 10_000
	authorizationStatsCacheTTL  = 5 * time.Minute
	authorizationStatsChunkSize = 500
)

// ResourceAuthorizationStats 等价 Node ResourceAuthorizationStats。
type ResourceAuthorizationStats struct {
	AuthorizationCount     int
	AuthorizationTeamCount int
}

// authorizationStatsCacheEntry 是 LRU 链上的载荷。
type authorizationStatsCacheEntry struct {
	key       string
	stats     ResourceAuthorizationStats
	expiresAt time.Time
}

// authorizationStatsCache 等价 Node createAppCache({max:10000, ttlMs:5min,
// updateAgeOnGet:true})：互斥 map + LRU recency 链，get 命中刷新 TTL 与新近度。
type authorizationStatsCache struct {
	mu      sync.Mutex
	max     int
	ttl     time.Duration
	now     func() time.Time
	entries map[string]*list.Element
	order   *list.List // front = most recent
}

func newAuthorizationStatsCache(now func() time.Time) *authorizationStatsCache {
	return &authorizationStatsCache{
		max:     authorizationStatsCacheMax,
		ttl:     authorizationStatsCacheTTL,
		now:     now,
		entries: map[string]*list.Element{},
		order:   list.New(),
	}
}

func (c *authorizationStatsCache) get(cacheKey string) (ResourceAuthorizationStats, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	element, ok := c.entries[cacheKey]
	if !ok {
		return ResourceAuthorizationStats{}, false
	}
	entry := element.Value.(*authorizationStatsCacheEntry)
	now := c.now()
	if !entry.expiresAt.After(now) {
		c.order.Remove(element)
		delete(c.entries, cacheKey)
		return ResourceAuthorizationStats{}, false
	}
	// updateAgeOnGet：命中刷新过期时钟与 LRU 新近度。
	entry.expiresAt = now.Add(c.ttl)
	c.order.MoveToFront(element)
	return entry.stats, true
}

func (c *authorizationStatsCache) set(cacheKey string, stats ResourceAuthorizationStats) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if element, ok := c.entries[cacheKey]; ok {
		entry := element.Value.(*authorizationStatsCacheEntry)
		entry.stats = stats
		entry.expiresAt = now.Add(c.ttl)
		c.order.MoveToFront(element)
		return
	}
	element := c.order.PushFront(&authorizationStatsCacheEntry{
		key:       cacheKey,
		stats:     stats,
		expiresAt: now.Add(c.ttl),
	})
	c.entries[cacheKey] = element
	for c.order.Len() > c.max {
		oldest := c.order.Back()
		if oldest == nil {
			break
		}
		c.order.Remove(oldest)
		delete(c.entries, oldest.Value.(*authorizationStatsCacheEntry).key)
	}
}

// resourceAuthorizationStatsCacheKey 等价 resourceAuthorizationStatsCacheKey。
func resourceAuthorizationStatsCacheKey(resourceType, resourceID string) string {
	return resourceType + ":" + resourceID
}

// uniqueStringIDs 等价 Node uniqueIds：去重 + 丢弃空串。
func uniqueStringIDs(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

// ResourceAuthorizationStatsByResourceIds 等价
// loadResourceAuthorizationStatsByResourceIdsAsync（Go 单实现天然异步）。
// 缓存命中直接返回；缺失 ID 分块（500）聚合后写缓存；无聚合行的 ID 记
// {0,0}（Node emptyAuthorizationStats 兜底同样入缓存）。
func (s *Store) ResourceAuthorizationStatsByResourceIds(ctx context.Context, resourceType string, resourceIDs []string) (map[string]ResourceAuthorizationStats, error) {
	ctx = ensureCtx(ctx)
	ids := uniqueStringIDs(resourceIDs)
	result := map[string]ResourceAuthorizationStats{}
	if len(ids) == 0 {
		return result, nil
	}
	missing := make([]string, 0, len(ids))
	for _, id := range ids {
		if cached, ok := s.statsCache.get(resourceAuthorizationStatsCacheKey(resourceType, id)); ok {
			result[id] = cached
		} else {
			missing = append(missing, id)
		}
	}
	if len(missing) == 0 {
		return result, nil
	}
	loaded, err := s.loadResourceAuthorizationStatsFromDatabase(ctx, resourceType, missing)
	if err != nil {
		return nil, err
	}
	for _, id := range missing {
		stats, ok := loaded[id]
		if !ok {
			stats = ResourceAuthorizationStats{}
		}
		s.statsCache.set(resourceAuthorizationStatsCacheKey(resourceType, id), stats)
		result[id] = stats
	}
	return result, nil
}

// loadResourceAuthorizationStatsFromDatabase 等价
// loadResourceAuthorizationStatsFromDatabaseAsync：与 Node 聚合 SQL 逐列一致
// （resource_type/status 过滤走 ra，团队来源过滤走 JOIN 条件），分块 500。
func (s *Store) loadResourceAuthorizationStatsFromDatabase(ctx context.Context, resourceType string, resourceIDs []string) (map[string]ResourceAuthorizationStats, error) {
	loaded := map[string]ResourceAuthorizationStats{}
	for _, chunk := range chunkValues(resourceIDs, authorizationStatsChunkSize) {
		rows, err := s.db.QueryContext(ctx, s.bind(`
      SELECT
        ra.resource_id,
        COUNT(DISTINCT ra.id) AS authorization_count,
        COUNT(DISTINCT CASE WHEN ras.source_type = 'team' AND ras.status = 'active' THEN ras.source_team_id END) AS authorization_team_count
      FROM `+s.table("resource_authorizations")+` ra
      LEFT JOIN `+s.table("resource_authorization_sources")+` ras
        ON ras.authorization_id = ra.id
        AND ras.source_type = 'team'
        AND ras.status = 'active'
      WHERE ra.resource_type = ?
        AND ra.status = 'active'
        AND ra.resource_id IN (`+placeholders(len(chunk))+`)
      GROUP BY ra.resource_id
    `), append([]any{resourceType}, idsToAny(chunk)...)...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var resourceID string
			var authorizationCount, authorizationTeamCount int
			if err := rows.Scan(&resourceID, &authorizationCount, &authorizationTeamCount); err != nil {
				rows.Close()
				return nil, err
			}
			loaded[resourceID] = ResourceAuthorizationStats{
				AuthorizationCount:     authorizationCount,
				AuthorizationTeamCount: authorizationTeamCount,
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return loaded, nil
}

// InvalidateResourceAuthorizationStatsCache 等价
// invalidateResourceAuthorizationStatsCache：resourceType 为空（或未给资源 ID）
// 清空全表，二者齐备时按 resourceType + resourceIDs 定点删除。归档 Node 生产
// 路径未接线该失效（仅 TTL），Go 侧预留同款入口供授权写路径后续装配。
func (s *Store) InvalidateResourceAuthorizationStatsCache(resourceType string, resourceIDs ...string) {
	if resourceType == "" || len(resourceIDs) == 0 {
		s.statsCache.clear()
		return
	}
	for _, id := range uniqueStringIDs(resourceIDs) {
		s.statsCache.delete(resourceAuthorizationStatsCacheKey(resourceType, id))
	}
}

func (c *authorizationStatsCache) delete(cacheKey string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if element, ok := c.entries[cacheKey]; ok {
		c.order.Remove(element)
		delete(c.entries, cacheKey)
	}
}

func (c *authorizationStatsCache) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = map[string]*list.Element{}
	c.order.Init()
}

func chunkValues(values []string, size int) [][]string {
	if size <= 0 {
		size = 1
	}
	chunks := [][]string{}
	for start := 0; start < len(values); start += size {
		end := start + size
		if end > len(values) {
			end = len(values)
		}
		chunks = append(chunks, values[start:end])
	}
	return chunks
}
