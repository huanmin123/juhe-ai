package gatewayclientip

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// Constants mirror client-ip-policy-cache.service.ts verbatim.
const (
	clientIPPolicyCacheTTL            = 30 * time.Second
	clientIPPolicyCacheMaxEntries     = 5_000
	clientIPPolicyHitFlushDelay       = 1000 * time.Millisecond
	clientIPPolicyHitMaxPendingEntries = 5_000
	clientIPPolicyHitFlushBatchSize   = 1_000
)

// Cache driver / runtime mode values mirror runtimeConfig.
const (
	CacheDriverRedis = "redis"
	RuntimeModePerformance = "performance"
	ProcessRoleServer = "server"
	ProcessRoleWorker = "worker"
	WorkerRoleStatsWorker = "stats-worker"
)

// InspectClientIPPolicyOptions mirrors InspectClientIpPolicyOptions.
type InspectClientIPPolicyOptions struct {
	CacheOnly           bool
	EnsureSnapshotLoaded bool
}

// ReplaceClientIPPolicyCacheLocalOptions mirrors the replace options.
type ReplaceClientIPPolicyCacheLocalOptions struct {
	SkipSharedCache bool
}

// ReloadClientIPPolicyCacheLocalOptions mirrors the reload options.
type ReloadClientIPPolicyCacheLocalOptions struct {
	BypassSharedCache bool
}

// ClientIPPolicyDecision mirrors ClientIpPolicyDecision with the full
// blacklist/allowlist payloads (the frozen gatewaypreauth
// ClientIPPolicyDecision carries the pre-auth consumed subset).
type ClientIPPolicyDecision struct {
	Blocked         bool
	Allowlisted     bool
	NormalizedIP    *NormalizedClientIP
	BlacklistPolicy *ActiveClientIPPolicy
	AllowlistPolicy *ActiveClientIPPolicy
}

// PolicyDecisionFromCacheEntryInput mirrors the cache entry carrier.
type policyCacheEntry struct {
	policy *ActiveClientIPPolicy
}

// PolicyCacheRuntime mirrors getClientIpPolicyCacheRuntime().
type PolicyCacheRuntime struct {
	SnapshotLoadedAt     string
	SnapshotPolicyCount  int
	PendingPolicyHitCount int
	DroppedPolicyHitCount int
	MaxPendingPolicyHits  int
	FlushBatchSize       int
}

// FlushScheduler mirrors the setTimeout scheduling of the hit buffer flush;
// tests replace it with a deterministic scheduler.
type FlushScheduler interface {
	// AfterFunc schedules fn after delay and returns a cancel handle.
	AfterFunc(delay time.Duration, fn func()) func()
}

type timerFlushScheduler struct{}

// AfterFunc implements FlushScheduler with time.AfterFunc.
func (timerFlushScheduler) AfterFunc(delay time.Duration, fn func()) func() {
	timer := time.AfterFunc(delay, fn)
	return func() { timer.Stop() }
}

// PolicyCacheOptions configures the policy cache service.
type PolicyCacheOptions struct {
	Clock  Clock
	Logger Logger
	// CacheDriver mirrors runtimeConfig.cacheDriver ('memory' | 'redis').
	CacheDriver string
	// RuntimeMode mirrors runtimeConfig.runtimeMode
	// ('standalone' | 'performance').
	RuntimeMode string
	// ProcessRole / WorkerRole mirror runtimeConfig.processRole /
	// runtimeConfig.workerRole for shouldUseStatsWriterBridge.
	ProcessRole string
	WorkerRole  string
	// Source is the direct stats-database policy read/hit source.
	Source PolicySource
	// StatsWriter mirrors requestStatsWriter; optional.
	StatsWriter StatsWriterBridge
	// Shared mirrors the Redis shared cache factory for
	// cacheDriver === 'redis'; in memory mode the family keeps the Node
	// MemorySharedJsonCache LRU behavior in-process.
	Shared gatewayruntimecache.SharedCacheFactory
	// Scheduler schedules the hit-buffer flush; defaults to time.AfterFunc.
	Scheduler FlushScheduler
}

// PolicyCache is the G13a client-ip policy cache + hit buffer. It satisfies
// the G05 gatewaypreauth.ClientIPPolicy port.
type PolicyCache struct {
	opts     PolicyCacheOptions
	clock    Clock
	logger   Logger
	sched    FlushScheduler
	source   PolicySource
	stats    StatsWriterBridge
	useStats bool

	// policyCache mirrors the per-IP app cache
	// ('gateway:client-ip-policy-by-ip', max 5000, ttl 30s with per-entry
	// expiry-bounded TTL).
	policyCache *entryTTLCache[policyCacheEntry]
	// activePolicySnapshot mirrors the memory-driver snapshot map.
	snapshotMu    sync.Mutex
	snapshot      map[string]ActiveClientIPPolicy
	snapshotLoadedAt string

	// sharedSnapshot / sharedByIP mirror the shared JSON caches
	// ('gateway:client-ip-policy-snapshot' max 1,
	// 'gateway:client-ip-policy-by-ip' max 5000). In Redis mode they are
	// backed by the Shared factory; in memory mode by in-process LRUs.
	sharedSnapshot gatewayruntimecache.SharedCache
	sharedByIP     gatewayruntimecache.SharedCache

	// pendingHits mirrors pendingPolicyHits with Node Map insertion order.
	pendingMu    sync.Mutex
	pendingHits  map[string]PolicyHitInput
	pendingOrder []string
	droppedHits  int
	flushPending bool
	flushCancel  func()
	closed       bool
}

// NewPolicyCache builds the service. Errors mirror the construction-time
// contract violations (missing source, Redis driver without shared factory).
func NewPolicyCache(opts PolicyCacheOptions) (*PolicyCache, error) {
	if opts.Source == nil {
		return nil, errors.New("gatewayclientip PolicyCache 需要 PolicySource")
	}
	clock := opts.Clock
	if clock == nil {
		clock = systemClock()
	}
	sched := opts.Scheduler
	if sched == nil {
		sched = timerFlushScheduler{}
	}
	cache := &PolicyCache{
		opts:        opts,
		clock:       clock,
		logger:      opts.Logger,
		sched:       sched,
		source:      opts.Source,
		stats:       opts.StatsWriter,
		useStats:    shouldUseStatsWriterBridge(opts.ProcessRole, opts.WorkerRole),
		policyCache: newEntryTTLCache[policyCacheEntry](clock, clientIPPolicyCacheMaxEntries),
		snapshot:    map[string]ActiveClientIPPolicy{},
		pendingHits: map[string]PolicyHitInput{},
	}
	if opts.CacheDriver == CacheDriverRedis {
		if opts.Shared == nil {
			return nil, errors.New("cacheDriver redis 需要 Shared shared cache factory")
		}
		cache.sharedSnapshot = opts.Shared.Cache(policySnapshotCacheName)
		cache.sharedByIP = opts.Shared.Cache(policyByIPCacheName)
	} else {
		cache.sharedSnapshot = newMemorySharedCache(clock, 1, clientIPPolicyCacheTTL)
		cache.sharedByIP = newMemorySharedCache(clock, clientIPPolicyCacheMaxEntries, clientIPPolicyCacheTTL)
	}
	return cache, nil
}

// shouldUseStatsWriterBridge mirrors shouldUseStatsWriterBridge.
func shouldUseStatsWriterBridge(processRole, workerRole string) bool {
	return processRole == ProcessRoleServer ||
		(processRole == ProcessRoleWorker && workerRole != WorkerRoleStatsWorker)
}

// Close cancels a scheduled flush; the Node timer relies on unref.
func (c *PolicyCache) Close() {
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()
	c.closed = true
	if c.flushCancel != nil {
		c.flushCancel()
		c.flushCancel = nil
	}
}

// ---------------------------------------------------------------------------
// inspect
// ---------------------------------------------------------------------------

// InspectPolicy mirrors inspectClientIpPolicy(clientIp, options) with the
// full Node contract and the full decision payload.
func (c *PolicyCache) InspectPolicy(ctx context.Context, clientIP string, options InspectClientIPPolicyOptions) (ClientIPPolicyDecision, error) {
	normalizedIP := NormalizeClientIPForStats(clientIP)
	if normalizedIP == nil {
		return ClientIPPolicyDecision{}, nil
	}
	if c.opts.CacheDriver == CacheDriverRedis {
		var entry *sharedByIPEntry
		if options.CacheOnly {
			cached, err := c.getClientIPPolicyByIPSharedCacheEntry(ctx, normalizedIP.IPHash)
			if err != nil {
				return ClientIPPolicyDecision{}, err
			}
			entry = cached
		} else {
			loaded, err := c.loadClientIPPolicyByHashFromSharedCacheOrDatabase(ctx, normalizedIP.IPHash)
			if err != nil {
				return ClientIPPolicyDecision{}, err
			}
			entry = loaded
		}
		var policy *ActiveClientIPPolicy
		if entry != nil {
			policy = entry.Policy
		}
		return c.policyDecisionFromCacheEntry(normalizedIP, policy), nil
	}
	if cached, ok := c.policyCache.get(normalizedIP.IPHash); ok {
		return c.policyDecisionFromCacheEntry(normalizedIP, cached.policy), nil
	}
	c.snapshotMu.Lock()
	loadedAtEmpty := c.snapshotLoadedAt == ""
	c.snapshotMu.Unlock()
	if loadedAtEmpty && options.EnsureSnapshotLoaded {
		if err := c.ReloadClientIPPolicyCacheLocal(ctx, ReloadClientIPPolicyCacheLocalOptions{}); err != nil {
			return ClientIPPolicyDecision{}, err
		}
	}
	c.snapshotMu.Lock()
	snapshotPolicyValue, hasSnapshotPolicy := c.snapshot[normalizedIP.IPHash]
	snapshotLoadedAt := c.snapshotLoadedAt
	c.snapshotMu.Unlock()
	var snapshotPolicy *ActiveClientIPPolicy
	if hasSnapshotPolicy {
		snapshotPolicy = &snapshotPolicyValue
	}
	if snapshotPolicy == nil && options.CacheOnly && snapshotLoadedAt == "" {
		return ClientIPPolicyDecision{NormalizedIP: normalizedIP}, nil
	}
	ttl, err := c.clientIPPolicyTTL(snapshotPolicy)
	if err != nil {
		return ClientIPPolicyDecision{}, err
	}
	c.policyCache.set(normalizedIP.IPHash, policyCacheEntry{policy: snapshotPolicy}, ttl)
	return c.policyDecisionFromCacheEntry(normalizedIP, snapshotPolicy), nil
}

// InspectClientIPPolicy is the G05 gatewaypreauth.ClientIPPolicy port
// method: inspectClientIpPolicy(clientIp, { cacheOnly }).
func (c *PolicyCache) InspectClientIPPolicy(ctx context.Context, clientIP string, cacheOnly bool) (gatewaypreauth.ClientIPPolicyDecision, error) {
	decision, err := c.InspectPolicy(ctx, clientIP, InspectClientIPPolicyOptions{CacheOnly: cacheOnly})
	if err != nil {
		return gatewaypreauth.ClientIPPolicyDecision{}, err
	}
	return decisionForPreAuth(decision), nil
}

// policyDecisionFromCacheEntry mirrors policyDecisionFromCacheEntry.
func (c *PolicyCache) policyDecisionFromCacheEntry(normalizedIP *NormalizedClientIP, policy *ActiveClientIPPolicy) ClientIPPolicyDecision {
	if policy != nil {
		active, err := isActiveClientIPPolicyAt(*policy, c.clock.Now().UnixMilli())
		if err != nil || !active {
			policy = nil
		}
	}
	var blacklistPolicy *ActiveClientIPPolicy
	var allowlistPolicy *ActiveClientIPPolicy
	if policy != nil {
		switch policy.PolicyType {
		case PolicyTypeBlacklist:
			blacklistPolicy = policy
		case PolicyTypeAllowlist:
			allowlistPolicy = policy
		}
	}
	return ClientIPPolicyDecision{
		Blocked:         blacklistPolicy != nil,
		Allowlisted:     allowlistPolicy != nil,
		NormalizedIP:    normalizedIP,
		BlacklistPolicy: blacklistPolicy,
		AllowlistPolicy: allowlistPolicy,
	}
}

// clientIPPolicyTTL mirrors clientIpPolicyTtlMs.
func (c *PolicyCache) clientIPPolicyTTL(policy *ActiveClientIPPolicy) (time.Duration, error) {
	if policy == nil || policy.ExpiresAt == nil {
		return clientIPPolicyCacheTTL, nil
	}
	expiresAtMs, err := requiredRFC3339Millis(*policy.ExpiresAt, "Client-IP 策略 expiresAt")
	if err != nil {
		return 0, err
	}
	delta := time.Duration(expiresAtMs-c.clock.Now().UnixMilli()) * time.Millisecond
	if delta < time.Millisecond {
		return time.Millisecond, nil
	}
	if delta < clientIPPolicyCacheTTL {
		return delta, nil
	}
	return clientIPPolicyCacheTTL, nil
}

// ---------------------------------------------------------------------------
// local snapshot maintenance
// ---------------------------------------------------------------------------

// PrimeClientIPPolicyCacheLocal mirrors primeClientIpPolicyCacheLocal.
func (c *PolicyCache) PrimeClientIPPolicyCacheLocal(policies []ActiveClientIPPolicy) error {
	return c.ReplaceClientIPPolicyCacheLocal(policies, ReplaceClientIPPolicyCacheLocalOptions{})
}

// ReplaceClientIPPolicyCacheLocal mirrors replaceClientIpPolicyCacheLocal.
func (c *PolicyCache) ReplaceClientIPPolicyCacheLocal(policies []ActiveClientIPPolicy, options ReplaceClientIPPolicyCacheLocalOptions) error {
	c.policyCache.clear()
	c.snapshotMu.Lock()
	c.snapshot = map[string]ActiveClientIPPolicy{}
	if c.opts.CacheDriver == CacheDriverRedis {
		c.snapshotLoadedAt = ""
		c.snapshotMu.Unlock()
		if !options.SkipSharedCache {
			return errors.New("高性能模式禁止同步写入 Client-IP 策略 Redis shared cache，必须使用异步刷新入口")
		}
		return nil
	}
	for _, policy := range policies {
		cloned, err := cloneActiveClientIPPolicy(policy)
		if err != nil {
			c.snapshotMu.Unlock()
			return err
		}
		if _, exists := c.snapshot[cloned.IPHash]; !exists {
			c.snapshot[cloned.IPHash] = cloned
		}
	}
	c.snapshotLoadedAt = isoNow(c.clock)
	c.snapshotMu.Unlock()
	return nil
}

// ReloadClientIPPolicyCacheLocal mirrors reloadClientIpPolicyCacheLocal.
func (c *PolicyCache) ReloadClientIPPolicyCacheLocal(ctx context.Context, options ReloadClientIPPolicyCacheLocalOptions) error {
	if c.opts.CacheDriver == CacheDriverRedis {
		c.snapshotMu.Lock()
		c.snapshot = map[string]ActiveClientIPPolicy{}
		c.snapshotLoadedAt = ""
		c.snapshotMu.Unlock()
		if options.BypassSharedCache {
			if err := c.sharedSnapshot.Clear(ctx); err != nil {
				return err
			}
			return c.sharedByIP.Clear(ctx)
		}
		return c.sharedByIP.Clear(ctx)
	}
	var sharedSnapshot *sharedSnapshotEntry
	if !options.BypassSharedCache {
		entry, err := c.getActivePolicySnapshotSharedCacheEntry(ctx)
		if err != nil {
			return err
		}
		sharedSnapshot = entry
	}
	if sharedSnapshot != nil {
		if err := c.ReplaceClientIPPolicyCacheLocal(sharedSnapshot.Policies, ReplaceClientIPPolicyCacheLocalOptions{SkipSharedCache: true}); err != nil {
			return err
		}
		c.snapshotMu.Lock()
		c.snapshotLoadedAt = sharedSnapshot.LoadedAt
		c.snapshotMu.Unlock()
		return nil
	}
	snapshot, err := c.loadClientIPPolicySnapshotFromDatabase(ctx)
	if err != nil {
		return err
	}
	if err := c.ReplaceClientIPPolicyCacheLocal(snapshot.Policies, ReplaceClientIPPolicyCacheLocalOptions{SkipSharedCache: true}); err != nil {
		return err
	}
	c.snapshotMu.Lock()
	c.snapshotLoadedAt = snapshot.LoadedAt
	c.snapshotMu.Unlock()
	return nil
}

// ReplaceClientIPPolicySharedSnapshotAsync mirrors
// replaceClientIpPolicySharedSnapshotAsync.
func (c *PolicyCache) ReplaceClientIPPolicySharedSnapshotAsync(ctx context.Context, policies []ActiveClientIPPolicy) error {
	c.policyCache.clear()
	c.snapshotMu.Lock()
	c.snapshot = map[string]ActiveClientIPPolicy{}
	c.snapshotMu.Unlock()
	if c.opts.CacheDriver != CacheDriverRedis {
		return c.ReplaceClientIPPolicyCacheLocal(policies, ReplaceClientIPPolicyCacheLocalOptions{})
	}
	c.snapshotMu.Lock()
	c.snapshotLoadedAt = ""
	c.snapshotMu.Unlock()
	if err := c.sharedSnapshot.Clear(ctx); err != nil {
		return err
	}
	if err := c.sharedByIP.Clear(ctx); err != nil {
		return err
	}
	loadedAt := isoNow(c.clock)
	for _, policy := range policies {
		if err := c.setClientIPPolicyByIPSharedCacheEntry(ctx, policy.IPHash, &policy, loadedAt); err != nil {
			return err
		}
	}
	return nil
}

// ClearClientIPPolicyCacheLocal mirrors clearClientIpPolicyCacheLocal.
func (c *PolicyCache) ClearClientIPPolicyCacheLocal(ctx context.Context) error {
	c.policyCache.clear()
	c.snapshotMu.Lock()
	c.snapshot = map[string]ActiveClientIPPolicy{}
	c.snapshotLoadedAt = ""
	c.snapshotMu.Unlock()
	if err := c.sharedSnapshot.Clear(ctx); err != nil {
		return err
	}
	return c.sharedByIP.Clear(ctx)
}

// ---------------------------------------------------------------------------
// hit recording + buffer
// ---------------------------------------------------------------------------

// RecordClientIPPolicyHit mirrors recordClientIpPolicyHitAsync as the G05
// fire-and-forget port: the pre-auth path forwards a BlacklistPolicy and the
// implementation owns error handling
// ('gateway_client_ip_blacklist_hit_record_failed' warn, then the caller
// continues sending the blacklist response).
func (c *PolicyCache) RecordClientIPPolicyHit(policy gatewaypreauth.BlacklistPolicy) {
	converted := ActiveClientIPPolicy{
		ID:             policy.ID,
		IPHash:         policy.IPHash,
		PolicyType:     PolicyTypeBlacklist,
		AggregateIPKey: policy.AggregateIPKey,
		ClientIP:       policy.ClientIP,
	}
	if policy.Reason != "" {
		reason := policy.Reason
		converted.Reason = &reason
	}
	if err := c.RecordClientIPPolicyHitAsync(context.Background(), converted); err != nil {
		if c.logger != nil {
			fields := map[string]any{"err": err.Error()}
			c.logger.Warn("gateway_client_ip_blacklist_hit_record_failed", fields, "")
		}
	}
}

// RecordClientIPPolicyHitAsync mirrors recordClientIpPolicyHitAsync.
func (c *PolicyCache) RecordClientIPPolicyHitAsync(ctx context.Context, policy ActiveClientIPPolicy) error {
	if policy.PolicyType != PolicyTypeBlacklist {
		return nil
	}
	hit := PolicyHitInput{
		IPHash:   policy.IPHash,
		PolicyID: policy.ID,
		HitCount: 1,
		HitAt:    isoNow(c.clock),
	}
	if c.opts.CacheDriver == CacheDriverRedis || c.opts.RuntimeMode == RuntimeModePerformance {
		return c.writeClientIPPolicyHits(ctx, []PolicyHitInput{hit})
	}
	key := policy.IPHash + ":" + policy.ID
	c.pendingMu.Lock()
	current, exists := c.pendingHits[key]
	if !exists && len(c.pendingHits) >= clientIPPolicyHitMaxPendingEntries {
		c.droppedHits += 1
		dropped := c.droppedHits
		pendingCount := len(c.pendingHits)
		c.pendingMu.Unlock()
		if dropped <= 10 || dropped%1000 == 0 {
			if c.logger != nil {
				c.logger.Warn("client_ip_policy_hit_buffer_dropped", map[string]any{
					"ipHash":                policy.IPHash,
					"policyId":              policy.ID,
					"pendingHitCount":       pendingCount,
					"maxPendingEntries":     clientIPPolicyHitMaxPendingEntries,
					"droppedPolicyHitCount": dropped,
				}, "IP 封禁命中缓冲达到保护上限，已丢弃新的 distinct 命中")
			}
		}
		return nil
	}
	next := hit
	next.HitCount = current.HitCount + 1
	if !exists {
		// Node 的 Map 插入序即冲刷顺序；Go 用 pendingOrder 复刻。
		c.pendingOrder = append(c.pendingOrder, key)
	}
	c.pendingHits[key] = next
	c.pendingMu.Unlock()
	c.scheduleClientIPPolicyHitFlush(clientIPPolicyHitFlushDelay)
	return nil
}

// scheduleClientIPPolicyHitFlush mirrors scheduleClientIpPolicyHitFlush.
func (c *PolicyCache) scheduleClientIPPolicyHitFlush(delay time.Duration) {
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()
	if c.flushPending || c.closed {
		return
	}
	c.flushPending = true
	c.flushCancel = c.sched.AfterFunc(delay, func() {
		c.pendingMu.Lock()
		c.flushPending = false
		c.flushCancel = nil
		c.pendingMu.Unlock()
		c.flushClientIPPolicyHits()
	})
}

// flushClientIPPolicyHits mirrors flushClientIpPolicyHits.
func (c *PolicyCache) flushClientIPPolicyHits() {
	c.pendingMu.Lock()
	if len(c.pendingHits) == 0 {
		c.pendingMu.Unlock()
		return
	}
	batch := make([]PolicyHitInput, 0, minInt(clientIPPolicyHitFlushBatchSize, len(c.pendingHits)))
	taken := 0
	for taken < len(c.pendingOrder) && len(batch) < clientIPPolicyHitFlushBatchSize {
		key := c.pendingOrder[taken]
		batch = append(batch, c.pendingHits[key])
		delete(c.pendingHits, key)
		taken++
	}
	c.pendingOrder = c.pendingOrder[taken:]
	c.pendingMu.Unlock()

	err := c.writeClientIPPolicyHits(context.Background(), batch)
	if err != nil {
		if c.logger != nil {
			hitCount := int64(0)
			for _, hit := range batch {
				hitCount += hit.HitCount
			}
			c.logger.Warn("client_ip_policy_hits_flush_failed", map[string]any{
				"err":      err.Error(),
				"hitCount": hitCount,
			}, "IP 封禁命中记录写入失败")
		}
	}
	c.pendingMu.Lock()
	remaining := len(c.pendingHits)
	c.pendingMu.Unlock()
	if remaining > 0 {
		c.scheduleClientIPPolicyHitFlush(0)
	}
}

// writeClientIPPolicyHits mirrors writeClientIpPolicyHits.
func (c *PolicyCache) writeClientIPPolicyHits(ctx context.Context, hits []PolicyHitInput) error {
	if c.useStats {
		return c.requestStatsWriter(ctx, StatsWriterOpRecordClientIPPolicyHits, StatsWriterPayload{Hits: hits})
	}
	return c.source.RecordClientIPPolicyHits(ctx, hits)
}

// requestStatsWriter mirrors requestStatsWriter with the fixed 1000ms budget.
func (c *PolicyCache) requestStatsWriter(ctx context.Context, operation string, payload StatsWriterPayload) error {
	if c.stats == nil {
		return errors.New("stats-writer 不可用，无法执行统计写操作：" + operation)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	runCtx, cancel := context.WithTimeout(ctx, requestStatsWriterTimeout)
	defer cancel()
	_, err := c.stats.RequestStatsWriter(runCtx, operation, payload)
	return err
}

// GetClientIPPolicyCacheRuntime mirrors getClientIpPolicyCacheRuntime.
func (c *PolicyCache) GetClientIPPolicyCacheRuntime() PolicyCacheRuntime {
	c.snapshotMu.Lock()
	loadedAt := c.snapshotLoadedAt
	snapshotCount := len(c.snapshot)
	c.snapshotMu.Unlock()
	c.pendingMu.Lock()
	pending := len(c.pendingHits)
	dropped := c.droppedHits
	c.pendingMu.Unlock()
	if c.opts.CacheDriver == CacheDriverRedis {
		snapshotCount = 0
	}
	return PolicyCacheRuntime{
		SnapshotLoadedAt:      loadedAt,
		SnapshotPolicyCount:   snapshotCount,
		PendingPolicyHitCount: pending,
		DroppedPolicyHitCount: dropped,
		MaxPendingPolicyHits:  clientIPPolicyHitMaxPendingEntries,
		FlushBatchSize:        clientIPPolicyHitFlushBatchSize,
	}
}

// ---------------------------------------------------------------------------
// shared cache entries
// ---------------------------------------------------------------------------

// getActivePolicySnapshotSharedCacheEntry mirrors
// getActivePolicySnapshotSharedCacheEntry.
func (c *PolicyCache) getActivePolicySnapshotSharedCacheEntry(ctx context.Context) (*sharedSnapshotEntry, error) {
	var cached sharedSnapshotEntry
	found, err := c.sharedSnapshot.Get(ctx, activePolicySnapshotSharedCacheKey, &cached)
	if err != nil || !found {
		return nil, err
	}
	policies := make([]ActiveClientIPPolicy, 0, len(cached.Policies))
	for _, policy := range cached.Policies {
		if !isActiveClientIPPolicyShape(policy) {
			continue
		}
		cloned, err := cloneActiveClientIPPolicy(policy)
		if err != nil {
			return nil, err
		}
		policies = append(policies, cloned)
	}
	loadedAt := cached.LoadedAt
	if loadedAt == "" {
		// Node: typeof cached.loadedAt === 'string' ? … : new Date().toISOString()
		loadedAt = isoNow(c.clock)
	}
	return &sharedSnapshotEntry{LoadedAt: loadedAt, Policies: policies}, nil
}

// setActivePolicySnapshotSharedCacheEntry mirrors
// setActivePolicySnapshotSharedCacheEntry.
func (c *PolicyCache) setActivePolicySnapshotSharedCacheEntry(ctx context.Context, entry sharedSnapshotEntry) error {
	policies := make([]ActiveClientIPPolicy, 0, len(entry.Policies))
	for _, policy := range entry.Policies {
		cloned, err := cloneActiveClientIPPolicy(policy)
		if err != nil {
			return err
		}
		policies = append(policies, cloned)
	}
	return c.sharedSnapshot.Set(ctx, activePolicySnapshotSharedCacheKey, sharedSnapshotEntry{
		LoadedAt: entry.LoadedAt,
		Policies: policies,
	}, clientIPPolicyCacheTTL)
}

// activePolicySnapshotSharedCacheKey mirrors activePolicySnapshotSharedCacheKey.
const activePolicySnapshotSharedCacheKey = "active"

// loadClientIPPolicySnapshotFromDatabase mirrors
// loadClientIpPolicySnapshotFromDatabase.
func (c *PolicyCache) loadClientIPPolicySnapshotFromDatabase(ctx context.Context) (*sharedSnapshotEntry, error) {
	var policies []ActiveClientIPPolicy
	if c.useStats {
		payload, err := c.statsListActivePolicies(ctx)
		if err != nil {
			return nil, err
		}
		policies = payload
	} else {
		loaded, err := c.source.ListActiveClientIPPolicies(ctx)
		if err != nil {
			return nil, err
		}
		policies = loaded
	}
	snapshot := sharedSnapshotEntry{LoadedAt: isoNow(c.clock), Policies: policies}
	if err := c.setActivePolicySnapshotSharedCacheEntry(ctx, snapshot); err != nil {
		return nil, err
	}
	return &snapshot, nil
}

func (c *PolicyCache) statsListActivePolicies(ctx context.Context) ([]ActiveClientIPPolicy, error) {
	if c.stats == nil {
		return nil, errors.New("stats-writer 不可用，无法执行统计写操作：" + StatsWriterOpListActiveClientIPPolicies)
	}
	runCtx, cancel := context.WithTimeout(ctx, requestStatsWriterTimeout)
	defer cancel()
	payload, err := c.stats.RequestStatsWriter(runCtx, StatsWriterOpListActiveClientIPPolicies, StatsWriterPayload{})
	if err != nil {
		return nil, err
	}
	return payload.Policies, nil
}

// getClientIPPolicyByIPSharedCacheEntry mirrors
// getClientIpPolicyByIpSharedCacheEntry.
func (c *PolicyCache) getClientIPPolicyByIPSharedCacheEntry(ctx context.Context, ipHash string) (*sharedByIPEntry, error) {
	var cached sharedByIPEntry
	found, err := c.sharedByIP.Get(ctx, ipHash, &cached)
	if err != nil || !found {
		return nil, err
	}
	var policy *ActiveClientIPPolicy
	if cached.Policy != nil && isActiveClientIPPolicyShape(*cached.Policy) {
		cloned, cloneErr := cloneActiveClientIPPolicy(*cached.Policy)
		if cloneErr != nil {
			return nil, cloneErr
		}
		policy = &cloned
	}
	loadedAt := cached.LoadedAt
	if loadedAt == "" {
		loadedAt = isoNow(c.clock)
	}
	return &sharedByIPEntry{LoadedAt: loadedAt, Policy: policy}, nil
}

// setClientIPPolicyByIPSharedCacheEntry mirrors
// setClientIpPolicyByIpSharedCacheEntry.
func (c *PolicyCache) setClientIPPolicyByIPSharedCacheEntry(ctx context.Context, ipHash string, policy *ActiveClientIPPolicy, loadedAt string) error {
	if loadedAt == "" {
		loadedAt = isoNow(c.clock)
	}
	entry := sharedByIPEntry{LoadedAt: loadedAt}
	var sharedPolicy *ActiveClientIPPolicy
	if policy != nil {
		cloned, err := cloneActiveClientIPPolicy(*policy)
		if err != nil {
			return err
		}
		sharedPolicy = &cloned
	}
	entry.Policy = sharedPolicy
	ttl, err := c.clientIPPolicyTTL(policy)
	if err != nil {
		return err
	}
	return c.sharedByIP.Set(ctx, ipHash, entry, ttl)
}

// loadClientIpPolicyByHashFromSharedCacheOrDatabase mirrors
// loadClientIpPolicyByHashFromSharedCacheOrDatabase.
func (c *PolicyCache) loadClientIPPolicyByHashFromSharedCacheOrDatabase(ctx context.Context, ipHash string) (*sharedByIPEntry, error) {
	sharedEntry, err := c.getClientIPPolicyByIPSharedCacheEntry(ctx, ipHash)
	if err != nil {
		return nil, err
	}
	if sharedEntry != nil {
		return sharedEntry, nil
	}
	var policy *ActiveClientIPPolicy
	if c.useStats {
		if c.stats == nil {
			return nil, errors.New("stats-writer 不可用，无法执行统计写操作：" + StatsWriterOpFindActiveClientIPPolicyByHash)
		}
		runCtx, cancel := context.WithTimeout(ctx, requestStatsWriterTimeout)
		payload, statsErr := c.stats.RequestStatsWriter(runCtx, StatsWriterOpFindActiveClientIPPolicyByHash, StatsWriterPayload{IPHash: ipHash})
		cancel()
		if statsErr != nil {
			return nil, statsErr
		}
		policy = payload.Policy
	} else {
		loaded, findErr := c.source.FindActiveClientIPPolicyByHash(ctx, ipHash)
		if findErr != nil {
			return nil, findErr
		}
		policy = loaded
	}
	loadedAt := isoNow(c.clock)
	if err := c.setClientIPPolicyByIPSharedCacheEntry(ctx, ipHash, policy, loadedAt); err != nil {
		return nil, err
	}
	return &sharedByIPEntry{LoadedAt: loadedAt, Policy: policy}, nil
}

// isActiveClientIPPolicyShape mirrors isActiveClientIpPolicy (structural
// validation for JSON-decoded shared cache values).
func isActiveClientIPPolicyShape(policy ActiveClientIPPolicy) bool {
	if policy.PolicyType != PolicyTypeBlacklist && policy.PolicyType != PolicyTypeAllowlist {
		return false
	}
	return true
}

// cloneActiveClientIPPolicy mirrors cloneActiveClientIpPolicy: expiresAt is
// canonicalized through requiredRfc3339Instant (throws when malformed).
func cloneActiveClientIPPolicy(policy ActiveClientIPPolicy) (ActiveClientIPPolicy, error) {
	cloned := ActiveClientIPPolicy{
		ID:             policy.ID,
		IPHash:         policy.IPHash,
		PolicyType:     policy.PolicyType,
		AggregateIPKey: policy.AggregateIPKey,
		ClientIP:       policy.ClientIP,
		Reason:         policy.Reason,
	}
	if policy.ExpiresAt != nil {
		parsed, ok := parseRFC3339InstantTime(*policy.ExpiresAt)
		if !ok {
			return ActiveClientIPPolicy{}, errors.New("Client-IP 策略 expiresAt 必须是带 Z 或数值 offset 的 RFC3339 时间")
		}
		canonical := canonicalRFC3339(parsed)
		cloned.ExpiresAt = &canonical
	}
	return cloned, nil
}

// decisionForPreAuth projects the full decision onto the frozen G05
// gatewaypreauth.ClientIPPolicyDecision shape.
func decisionForPreAuth(decision ClientIPPolicyDecision) gatewaypreauth.ClientIPPolicyDecision {
	out := gatewaypreauth.ClientIPPolicyDecision{
		Blocked:     decision.Blocked,
		Allowlisted: decision.Allowlisted,
	}
	if decision.NormalizedIP != nil {
		out.NormalizedIP = &gatewaypreauth.NormalizedClientIP{
			ClientIP:       decision.NormalizedIP.ClientIP,
			AggregateIPKey: decision.NormalizedIP.AggregateIPKey,
		}
	}
	if decision.BlacklistPolicy != nil {
		reason := ""
		if decision.BlacklistPolicy.Reason != nil {
			reason = *decision.BlacklistPolicy.Reason
		}
		out.BlacklistPolicy = &gatewaypreauth.BlacklistPolicy{
			ID:             decision.BlacklistPolicy.ID,
			IPHash:         decision.BlacklistPolicy.IPHash,
			Reason:         reason,
			ClientIP:       decision.BlacklistPolicy.ClientIP,
			AggregateIPKey: decision.BlacklistPolicy.AggregateIPKey,
		}
	}
	return out
}

// Compile-time G05 port assertion for G20 assembly: *PolicyCache implements
// gatewaypreauth.ClientIPPolicy.
var _ gatewaypreauth.ClientIPPolicy = (*PolicyCache)(nil)
