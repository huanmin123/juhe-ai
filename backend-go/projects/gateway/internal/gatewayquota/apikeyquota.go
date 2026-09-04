package gatewayquota

import (
	"context"
	"encoding/base64"
	"errors"
	"time"
)

// APIKeyQuotaExceededMessage mirrors API_KEY_QUOTA_EXCEEDED_MESSAGE — the
// exact 429 copy.
const APIKeyQuotaExceededMessage = "额度已用完，请联系管理员提升额度"

// apiKeyQuotaCacheTTL mirrors API_KEY_QUOTA_CACHE_TTL_MS.
const apiKeyQuotaCacheTTL = 5 * time.Second

// apiKeyQuotaCacheMax mirrors the createAppCache max.
const apiKeyQuotaCacheMax = 10000

// APIKeyQuotaConfig wires the service.
type APIKeyQuotaConfig struct {
	Modes    Modes
	Stats    *StatsStore
	Timezone StatsTimezoneProvider
	Snapshot *SnapshotCache
	// Shared is the createSharedJsonCache('gateway:api-key-quota') backend;
	// required when Modes.RedisCache.
	Shared SharedJSONCache
	// DBService backs the server-role exact checks (db-service-ipc).
	DBService DBServiceClient
	// Syncer mirrors syncGatewayCacheInvalidationsFromRuntimeState.
	Syncer InvalidationSyncer
	Now    func() time.Time
	Log    LogHook
}

// APIKeyQuotaService ports api-key-quota.service.ts.
type APIKeyQuotaService struct {
	modes     Modes
	stats     *StatsStore
	tz        StatsTimezoneProvider
	snapshot  *SnapshotCache
	shared    SharedJSONCache
	dbService DBServiceClient
	syncer    InvalidationSyncer
	now       func() time.Time
	log       LogHook
	memory    *quotaMemoryCache
}

// NewAPIKeyQuotaService validates the wiring and builds the runtime cache.
// Stats is only required by the direct stats-database paths (worker role /
// read-only checks); the redis server role never touches it.
func NewAPIKeyQuotaService(cfg APIKeyQuotaConfig) (*APIKeyQuotaService, error) {
	if cfg.Timezone == nil {
		return nil, errors.New("gatewayquota api-key quota service requires a timezone provider")
	}
	if cfg.Snapshot == nil {
		return nil, errors.New("gatewayquota api-key quota service requires the snapshot cache")
	}
	if cfg.Modes.RedisCache && cfg.Shared == nil {
		return nil, errors.New("gatewayquota api-key quota service requires a shared cache in redis mode")
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	log := cfg.Log
	if log == nil {
		log = noopLog
	}
	return &APIKeyQuotaService{
		modes:     cfg.Modes,
		stats:     cfg.Stats,
		tz:        cfg.Timezone,
		snapshot:  cfg.Snapshot,
		shared:    cfg.Shared,
		dbService: cfg.DBService,
		syncer:    cfg.Syncer,
		now:       now,
		log:       log,
		memory:    newQuotaMemoryCache(now, apiKeyQuotaCacheTTL, apiKeyQuotaCacheMax),
	}, nil
}

// requireStats guards the direct stats-database paths against an unwired
// store (server-role redis deployments construct without one).
func (s *APIKeyQuotaService) requireStats() (*StatsStore, error) {
	if s.stats == nil {
		return nil, errors.New("api-key quota stats store is not configured")
	}
	return s.stats, nil
}

func (s *APIKeyQuotaService) nowMs() int64 { return s.now().UnixMilli() }

// sharedCacheKey mirrors sharedQuotaCacheKey (base64url of the runtime key).
func sharedCacheKey(cacheKey string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(cacheKey))
}

// apiKeyQuotaCacheKey mirrors apiKeyQuotaCacheKey(_Async): the window segment
// is the scoped cost key of "now" in the stats timezone.
func (s *APIKeyQuotaService) apiKeyQuotaCacheKey(ctx context.Context, apiKey APIKeyRow, now time.Time, hourlyWindowHours int, hasHourly bool) (string, error) {
	location, err := s.tz.StatsTimezone(ctx)
	if err != nil {
		return "", err
	}
	windowKey := CostKey(CostInput{
		SystemAccountID:   apiKey.SystemAccountID,
		ScopeType:         ScopeTypeAPIKey,
		ScopeID:           apiKey.ID,
		Now:               now,
		HourlyWindowHours: hourlyWindowHours,
		HasHourlyWindow:   hasHourly,
	}, location)
	return apiKey.SystemAccountID + "\x00" + apiKey.ID + "\x00" + windowKey + "\x00" + apiKey.QuotaLimitsJSON, nil
}

// apiKeyCostInput mirrors the repeated cost input literal.
func apiKeyCostInput(apiKey APIKeyRow, now time.Time, hourlyWindowHours int, hasHourly bool) CostInput {
	return CostInput{
		SystemAccountID:   apiKey.SystemAccountID,
		ScopeType:         ScopeTypeAPIKey,
		ScopeID:           apiKey.ID,
		Now:               now,
		HourlyWindowHours: hourlyWindowHours,
		HasHourlyWindow:   hasHourly,
	}
}

func apiKeyHourly(limits RequestQuotaLimits) (int, bool) {
	if limits.Hourly == nil {
		return 0, false
	}
	return limits.Hourly.Hours, true
}

// ReadAPIKeyQuotaCostsSnapshotAsync mirrors readGatewayApiKeyQuotaCostsSnapshotAsync:
// nil (ok=false) when no limit is enabled or the snapshot misses.
func (s *APIKeyQuotaService) ReadAPIKeyQuotaCostsSnapshotAsync(ctx context.Context, apiKey APIKeyRow) (RequestQuotaCosts, bool, error) {
	limits, err := ParseRequestQuotaLimitsJSON(apiKey.QuotaLimitsJSON)
	if err != nil {
		return RequestQuotaCosts{}, false, err
	}
	if !HasEnabledRequestQuotaLimit(limits) {
		return RequestQuotaCosts{}, false, nil
	}
	hourly, hasHourly := apiKeyHourly(limits)
	return s.snapshot.ReadCostsSnapshotAsync(ctx, QuotaCostSnapshotEntry{
		SystemAccountID:   apiKey.SystemAccountID,
		ScopeType:         ScopeTypeAPIKey,
		ScopeID:           apiKey.ID,
		HourlyWindowHours: hourlyPointer(hourly, hasHourly),
	})
}

func hourlyPointer(hours int, has bool) *int {
	if !has {
		return nil
	}
	normalized := NormalizeHourlyWindowHours(hours)
	return &normalized
}

// ReadAPIKeyQuotaCostsExact mirrors readGatewayApiKeyQuotaCostsExact (direct
// stats-database read).
func (s *APIKeyQuotaService) ReadAPIKeyQuotaCostsExact(ctx context.Context, apiKey APIKeyRow, now time.Time) (RequestQuotaCosts, error) {
	stats, err := s.requireStats()
	if err != nil {
		return RequestQuotaCosts{}, err
	}
	limits, err := ParseRequestQuotaLimitsJSON(apiKey.QuotaLimitsJSON)
	if err != nil {
		return RequestQuotaCosts{}, err
	}
	location, err := s.tz.StatsTimezone(ctx)
	if err != nil {
		return RequestQuotaCosts{}, err
	}
	hourly, hasHourly := apiKeyHourly(limits)
	return stats.LoadCosts(ctx, apiKeyCostInput(apiKey, now, hourly, hasHourly), location)
}

// ReadAPIKeyQuotaCostsExactAsync mirrors readGatewayApiKeyQuotaCostsExactAsync:
// PostgreSQL batches through loadRequestQuotaCostsBatchAsync, everything else
// takes the synchronous path.
func (s *APIKeyQuotaService) ReadAPIKeyQuotaCostsExactAsync(ctx context.Context, apiKey APIKeyRow, now time.Time) (RequestQuotaCosts, error) {
	if !s.modes.PostgresDatabase {
		return s.ReadAPIKeyQuotaCostsExact(ctx, apiKey, now)
	}
	stats, err := s.requireStats()
	if err != nil {
		return RequestQuotaCosts{}, err
	}
	limits, err := ParseRequestQuotaLimitsJSON(apiKey.QuotaLimitsJSON)
	if err != nil {
		return RequestQuotaCosts{}, err
	}
	location, err := s.tz.StatsTimezone(ctx)
	if err != nil {
		return RequestQuotaCosts{}, err
	}
	hourly, hasHourly := apiKeyHourly(limits)
	input := apiKeyCostInput(apiKey, now, hourly, hasHourly)
	costsByKey, err := stats.LoadCostsBatch(ctx, []CostInput{input}, location)
	if err != nil {
		return RequestQuotaCosts{}, err
	}
	if costs, ok := costsByKey[CostKey(input, location)]; ok {
		return costs, nil
	}
	return EmptyRequestQuotaCosts(), nil
}

// CheckAPIKeyQuota mirrors checkGatewayApiKeyQuota: cached 5s decision with
// the exact stats-database cost load behind it.
func (s *APIKeyQuotaService) CheckAPIKeyQuota(ctx context.Context, apiKey APIKeyRow, now time.Time) (Decision, error) {
	if s.modes.ServerRole {
		return Decision{}, errServerLocalSQLite("checkGatewayApiKeyQuota")
	}
	stats, err := s.requireStats()
	if err != nil {
		return Decision{}, err
	}
	limits, err := ParseRequestQuotaLimitsJSON(apiKey.QuotaLimitsJSON)
	if err != nil {
		return Decision{}, err
	}
	if !HasEnabledRequestQuotaLimit(limits) {
		return AllowedDecision(), nil
	}
	hourly, hasHourly := apiKeyHourly(limits)
	cacheKey, err := s.apiKeyQuotaCacheKey(ctx, apiKey, now, hourly, hasHourly)
	if err != nil {
		return Decision{}, err
	}
	if !s.modes.RedisCache {
		if cached, ok := s.memory.get(cacheKey); ok {
			return cached.decision(), nil
		}
	}
	location, err := s.tz.StatsTimezone(ctx)
	if err != nil {
		return Decision{}, err
	}
	quotaCosts, err := stats.LoadCosts(ctx, apiKeyCostInput(apiKey, now, hourly, hasHourly), location)
	if err != nil {
		return Decision{}, err
	}
	allowed := !IsRequestQuotaExceeded(limits, quotaCosts)
	decision := Decision{Allowed: allowed}
	if !allowed {
		decision.Message = APIKeyQuotaExceededMessage
	}
	s.setCacheEntry(apiKey.ID, cacheKey, newCachedDecision(decision, s.nowMs()), false)
	return decision, nil
}

// CheckAPIKeyQuotaReadOnly mirrors checkGatewayApiKeyQuotaReadOnly: no cache
// read/write, no role guard.
func (s *APIKeyQuotaService) CheckAPIKeyQuotaReadOnly(ctx context.Context, apiKey APIKeyRow, now time.Time) (Decision, error) {
	stats, err := s.requireStats()
	if err != nil {
		return Decision{}, err
	}
	limits, err := ParseRequestQuotaLimitsJSON(apiKey.QuotaLimitsJSON)
	if err != nil {
		return Decision{}, err
	}
	if !HasEnabledRequestQuotaLimit(limits) {
		return AllowedDecision(), nil
	}
	location, err := s.tz.StatsTimezone(ctx)
	if err != nil {
		return Decision{}, err
	}
	hourly, hasHourly := apiKeyHourly(limits)
	quotaCosts, err := stats.LoadCosts(ctx, apiKeyCostInput(apiKey, now, hourly, hasHourly), location)
	if err != nil {
		return Decision{}, err
	}
	allowed := !IsRequestQuotaExceeded(limits, quotaCosts)
	decision := Decision{Allowed: allowed}
	if !allowed {
		decision.Message = APIKeyQuotaExceededMessage
	}
	return decision, nil
}

// CheckAPIKeyQuotaExactAsync mirrors checkGatewayApiKeyQuotaExactAsync.
func (s *APIKeyQuotaService) CheckAPIKeyQuotaExactAsync(ctx context.Context, apiKey APIKeyRow, now time.Time) (Decision, error) {
	if !s.modes.PostgresDatabase {
		return s.CheckAPIKeyQuota(ctx, apiKey, now)
	}
	if s.modes.RedisRuntimeState && s.syncer != nil {
		if err := s.syncer.SyncGatewayCacheInvalidations(ctx); err != nil {
			return Decision{}, err
		}
	}
	stats, err := s.requireStats()
	if err != nil {
		return Decision{}, err
	}
	limits, err := ParseRequestQuotaLimitsJSON(apiKey.QuotaLimitsJSON)
	if err != nil {
		return Decision{}, err
	}
	if !HasEnabledRequestQuotaLimit(limits) {
		return AllowedDecision(), nil
	}
	hourly, hasHourly := apiKeyHourly(limits)
	cacheKey, err := s.apiKeyQuotaCacheKey(ctx, apiKey, now, hourly, hasHourly)
	if err != nil {
		return Decision{}, err
	}
	if !s.modes.RedisCache {
		if cached, ok := s.memory.get(cacheKey); ok {
			return cached.decision(), nil
		}
	}
	if sharedCached, ok, err := s.sharedCacheGet(ctx, cacheKey); err != nil {
		return Decision{}, err
	} else if ok {
		s.setCacheEntry(apiKey.ID, cacheKey, sharedCached, true)
		return sharedCached.decision(), nil
	}
	location, err := s.tz.StatsTimezone(ctx)
	if err != nil {
		return Decision{}, err
	}
	input := apiKeyCostInput(apiKey, now, hourly, hasHourly)
	costsByKey, err := stats.LoadCostsBatch(ctx, []CostInput{input}, location)
	if err != nil {
		return Decision{}, err
	}
	costs := EmptyRequestQuotaCosts()
	if loaded, ok := costsByKey[CostKey(input, location)]; ok {
		costs = loaded
	}
	allowed := !IsRequestQuotaExceeded(limits, costs)
	decision := Decision{Allowed: allowed}
	if !allowed {
		decision.Message = APIKeyQuotaExceededMessage
	}
	if err := s.setCacheEntryAsync(ctx, apiKey.ID, cacheKey, newCachedDecision(decision, s.nowMs())); err != nil {
		return Decision{}, err
	}
	return decision, nil
}

// CheckAPIKeyQuotaAsync mirrors checkGatewayApiKeyQuotaAsync — the full
// driver/role decision tree.
func (s *APIKeyQuotaService) CheckAPIKeyQuotaAsync(ctx context.Context, apiKey APIKeyRow) (Decision, error) {
	if s.modes.RedisRuntimeState && s.syncer != nil {
		if err := s.syncer.SyncGatewayCacheInvalidations(ctx); err != nil {
			return Decision{}, err
		}
	}
	now := s.now()
	limits, err := ParseRequestQuotaLimitsJSON(apiKey.QuotaLimitsJSON)
	if err != nil {
		return Decision{}, err
	}
	if !HasEnabledRequestQuotaLimit(limits) {
		return AllowedDecision(), nil
	}
	hourly, hasHourly := apiKeyHourly(limits)
	cacheKey, err := s.apiKeyQuotaCacheKey(ctx, apiKey, now, hourly, hasHourly)
	if err != nil {
		return Decision{}, err
	}
	if !s.modes.RedisCache {
		if cached, ok := s.memory.get(cacheKey); ok {
			return cached.decision(), nil
		}
	}
	if sharedCached, ok, err := s.sharedCacheGet(ctx, cacheKey); err != nil {
		return Decision{}, err
	} else if ok {
		s.setCacheEntry(apiKey.ID, cacheKey, sharedCached, true)
		return sharedCached.decision(), nil
	}

	if s.modes.RedisCache && s.modes.ServerRole {
		costs, costsFound, err := s.snapshot.ReadCostsSnapshotAsync(ctx, QuotaCostSnapshotEntry{
			SystemAccountID:   apiKey.SystemAccountID,
			ScopeType:         ScopeTypeAPIKey,
			ScopeID:           apiKey.ID,
			HourlyWindowHours: hourlyPointer(hourly, hasHourly),
		})
		if err != nil {
			return Decision{}, err
		}
		if costsFound {
			passive := newCachedDecision(Decision{Allowed: !IsRequestQuotaExceeded(limits, costs)}, s.nowMs())
			if !passive.Allowed {
				passive.Message = APIKeyQuotaExceededMessage
			}
			if err := s.setCacheEntryAsync(ctx, apiKey.ID, cacheKey, passive); err != nil {
				return Decision{}, err
			}
			return passive.decision(), nil
		}
		snapshotIncomplete, err := s.snapshot.IsCostSnapshotIncompleteAsync(ctx)
		if err != nil {
			return Decision{}, err
		}
		decision, dbErr := s.dbServiceCheckAPIKeyQuota(ctx, apiKey)
		if dbErr != nil {
			s.log("gateway_api_key_quota_redis_exact_check_failed", map[string]any{
				"apiKeyId":           apiKey.ID,
				"systemAccountId":    apiKey.SystemAccountID,
				"snapshotIncomplete": snapshotIncomplete,
				"error":              dbErr.Error(),
			}, "Redis 模式 API Key 配额共享快照未命中且精确补判失败，按保护策略拒绝请求")
			return DeniedDecision(APIKeyQuotaExceededMessage), nil
		}
		if err := s.setCacheEntryAsync(ctx, apiKey.ID, cacheKey, newCachedDecision(decision, s.nowMs())); err != nil {
			return Decision{}, err
		}
		return decision, nil
	}

	if s.modes.ServerRole {
		costs, costsFound := s.snapshot.ReadCostsSnapshot(QuotaCostSnapshotEntry{
			SystemAccountID:   apiKey.SystemAccountID,
			ScopeType:         ScopeTypeAPIKey,
			ScopeID:           apiKey.ID,
			HourlyWindowHours: hourlyPointer(hourly, hasHourly),
		})
		allowed := true
		if costsFound {
			allowed = !IsRequestQuotaExceeded(limits, costs)
		}
		if !costsFound && s.snapshot.IsCostSnapshotIncomplete() {
			decision, dbErr := s.dbServiceCheckAPIKeyQuota(ctx, apiKey)
			if dbErr != nil {
				s.log("gateway_api_key_quota_snapshot_fallback_failed", map[string]any{
					"apiKeyId":        apiKey.ID,
					"systemAccountId": apiKey.SystemAccountID,
					"error":           dbErr.Error(),
				}, "API Key 配额快照不完整且 DB service 精确补判失败，按保护策略拒绝请求")
				allowed = false
			} else {
				if err := s.setCacheEntryAsync(ctx, apiKey.ID, cacheKey, newCachedDecision(decision, s.nowMs())); err != nil {
					return Decision{}, err
				}
				return decision, nil
			}
		}
		passive := Decision{Allowed: allowed}
		if !allowed {
			passive.Message = APIKeyQuotaExceededMessage
		}
		if err := s.setCacheEntryAsync(ctx, apiKey.ID, cacheKey, newCachedDecision(passive, s.nowMs())); err != nil {
			return Decision{}, err
		}
		return passive, nil
	}

	var decision Decision
	if s.modes.PostgresDatabase {
		decision, err = s.CheckAPIKeyQuotaExactAsync(ctx, apiKey, now)
	} else {
		decision, err = s.CheckAPIKeyQuota(ctx, apiKey, now)
	}
	if err != nil {
		return Decision{}, err
	}
	if err := s.setCacheEntryAsync(ctx, apiKey.ID, cacheKey, newCachedDecision(decision, s.nowMs())); err != nil {
		return Decision{}, err
	}
	return decision, nil
}

func (s *APIKeyQuotaService) dbServiceCheckAPIKeyQuota(ctx context.Context, apiKey APIKeyRow) (Decision, error) {
	if s.dbService == nil {
		return Decision{}, errors.New("db service client is not configured")
	}
	return s.dbService.CheckAPIKeyQuota(ctx, apiKey)
}

// sharedCacheGet mirrors getApiKeyQuotaSharedCacheEntry (redis only).
func (s *APIKeyQuotaService) sharedCacheGet(ctx context.Context, cacheKey string) (CachedDecision, bool, error) {
	if !s.modes.RedisCache {
		return CachedDecision{}, false, nil
	}
	var entry CachedDecision
	found, err := s.shared.Get(ctx, sharedCacheKey(cacheKey), &entry)
	if err != nil || !found {
		return CachedDecision{}, false, err
	}
	return entry, true, nil
}

// setCacheEntry mirrors setApiKeyQuotaCacheEntry: redis keeps only the shared
// cache (the process-local cache is a shell), memory keeps the indexed LRU.
// Node fires the redis shared write without awaiting (void); the Go port
// performs it best-effort with a 3s deadline and surfaces failures through
// the log hook.
func (s *APIKeyQuotaService) setCacheEntry(apiKeyID, cacheKey string, entry CachedDecision, skipShared bool) {
	if s.modes.RedisCache {
		if !skipShared {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if err := s.shared.Set(ctx, sharedCacheKey(cacheKey), entry, apiKeyQuotaCacheTTL); err != nil {
				s.log("api_key_quota_shared_cache_set_failed", map[string]any{"error": err.Error()},
					"API Key 额度 Redis shared cache 写入失败")
			}
		}
		return
	}
	s.memory.set(apiKeyID, cacheKey, entry)
}

// setCacheEntryAsync mirrors setApiKeyQuotaCacheEntryAsync: await the shared
// write, then refresh the local entry without re-writing shared.
func (s *APIKeyQuotaService) setCacheEntryAsync(ctx context.Context, apiKeyID, cacheKey string, entry CachedDecision) error {
	if s.modes.RedisCache {
		if err := s.shared.Set(ctx, sharedCacheKey(cacheKey), entry, apiKeyQuotaCacheTTL); err != nil {
			return err
		}
		return nil
	}
	s.memory.set(apiKeyID, cacheKey, entry)
	return nil
}

// ClearCache mirrors clearApiKeyQuotaCache.
func (s *APIKeyQuotaService) ClearCache(ctx context.Context) {
	s.memory.clear()
	if s.modes.RedisCache {
		if err := s.shared.Clear(ctx); err != nil {
			s.log("api_key_quota_shared_cache_clear_failed", map[string]any{"error": err.Error()},
				"API Key 额度 Redis shared cache 清理失败")
		}
	}
}

// InvalidateByID mirrors invalidateApiKeyQuotaCacheById.
func (s *APIKeyQuotaService) InvalidateByID(ctx context.Context, id string) {
	if s.modes.RedisCache {
		if err := s.shared.Clear(ctx); err != nil {
			s.log("api_key_quota_shared_cache_clear_failed", map[string]any{"error": err.Error()},
				"API Key 额度 Redis shared cache 清理失败")
		}
		return
	}
	s.memory.removeByID(id)
}
