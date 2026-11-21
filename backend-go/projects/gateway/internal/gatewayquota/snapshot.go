package gatewayquota

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"time"
)

// Snapshot pagination constants mirror quota-snapshot-cache.service.ts.
// The authorization pagination constants retired with the authorization
// quota domain (revision 31).
const (
	GatewayQuotaSnapshotCostPageSize   = 5000
	MaxGatewayQuotaSnapshotCostEntries = GatewayQuotaSnapshotCostPageSize
	// GatewayQuotaSnapshotRuntimeStateStoreName mirrors
	// gatewayQuotaSnapshotRuntimeStateStoreName.
	GatewayQuotaSnapshotRuntimeStateStoreName = "gateway_quota_snapshot"
	// GatewayQuotaSnapshotRuntimeStateKey mirrors gatewayQuotaSnapshotRuntimeStateKey.
	GatewayQuotaSnapshotRuntimeStateKey = "current"
)

// sharedSnapshotMemoTTL mirrors sharedSnapshotMemoTtlMs (1s).
const sharedSnapshotMemoTTL = time.Second

// QuotaCostSnapshotEntry mirrors GatewayQuotaCostSnapshotEntry.
type QuotaCostSnapshotEntry struct {
	SystemAccountID   string            `json:"systemAccountId"`
	ScopeType         string            `json:"scopeType"`
	ScopeID           string            `json:"scopeId"`
	HourlyWindowHours *int              `json:"hourlyWindowHours,omitempty"`
	Costs             RequestQuotaCosts `json:"costs"`
}

// GatewayQuotaSnapshot mirrors GatewayQuotaSnapshot. The complete flags are
// pointers so a missing field defaults to complete (?? true) like Node.
// The authorizationEntries arm retired with the authorization quota domain
// (revision 31); unknown JSON keys stay ignored on decode.
type GatewayQuotaSnapshot struct {
	GeneratedAt         string                   `json:"generatedAt"`
	CostEntries         []QuotaCostSnapshotEntry `json:"costEntries"`
	CostEntriesComplete *bool                    `json:"costEntriesComplete,omitempty"`
	Timezone            *string                  `json:"timezone,omitempty"`
	StatDate            *string                  `json:"statDate,omitempty"`
	StatWeek            *string                  `json:"statWeek,omitempty"`
	StatMonth           *string                  `json:"statMonth,omitempty"`
}

// SnapshotRuntimeInfo mirrors gatewayQuotaSnapshotRuntime.
type SnapshotRuntimeInfo struct {
	GeneratedAt         string
	CostEntryCount      int
	CostEntriesComplete bool
}

// SnapshotCache is the gateway quota snapshot cache: a process-local map fed
// by ReplaceGatewayQuotaSnapshot (memory cache driver) plus a 1s-memo
// read-through of the Redis runtime state document (redis cache driver).
// The authorization snapshot watermark/version semantics retired with the
// authorization quota domain (revision 31).
type SnapshotCache struct {
	modes        Modes
	runtimeState RuntimeStateStore
	now          func() time.Time
	log          LogHook

	mu           sync.Mutex
	generatedAt  string
	costComplete bool
	costSnapshot map[string]RequestQuotaCosts

	sharedMu          sync.Mutex
	shared            *GatewayQuotaSnapshot
	sharedCosts       map[string]RequestQuotaCosts
	sharedFetchedAtMs int64
	loading           chan sharedLoadResult
}

type sharedLoadResult struct {
	snapshot *GatewayQuotaSnapshot
	err      error
}

// NewSnapshotCache builds the cache. runtimeState is required only when the
// redis cache + runtime state drivers are both on.
func NewSnapshotCache(modes Modes, runtimeState RuntimeStateStore, now func() time.Time, log LogHook) (*SnapshotCache, error) {
	if now == nil {
		now = time.Now
	}
	if log == nil {
		log = noopLog
	}
	if modes.RedisCache && modes.RedisRuntimeState && runtimeState == nil {
		return nil, errors.New("gatewayquota snapshot cache requires a runtime state store in redis mode")
	}
	return &SnapshotCache{
		modes:        modes,
		runtimeState: runtimeState,
		now:          now,
		log:          log,
		costSnapshot: map[string]RequestQuotaCosts{},
		sharedCosts:  map[string]RequestQuotaCosts{},
	}, nil
}

// costSnapshotKey mirrors costSnapshotKey.
func costSnapshotKey(input QuotaCostSnapshotEntry) string {
	hourly := ""
	if input.HourlyWindowHours != nil {
		hourly = itoa(NormalizeHourlyWindowHours(*input.HourlyWindowHours))
	}
	return input.SystemAccountID + "\x00" + input.ScopeType + "\x00" + input.ScopeID + "\x00" + hourly
}

// ReplaceGatewayQuotaSnapshot mirrors replaceGatewayQuotaSnapshot.
func (c *SnapshotCache) ReplaceGatewayQuotaSnapshot(snapshot GatewayQuotaSnapshot) error {
	generatedAt, err := requiredRfc3339Instant(snapshot.GeneratedAt, "网关额度快照 generatedAt")
	if err != nil {
		return err
	}
	if c.modes.RedisCache {
		c.ClearGatewayQuotaSnapshot()
		return nil
	}
	costComplete := snapshot.CostEntriesComplete == nil || *snapshot.CostEntriesComplete
	c.mu.Lock()
	c.generatedAt = generatedAt
	c.costComplete = costComplete
	costSnapshot := make(map[string]RequestQuotaCosts, len(snapshot.CostEntries))
	for _, entry := range snapshot.CostEntries {
		costSnapshot[costSnapshotKey(entry)] = CloneRequestQuotaCosts(entry.Costs)
	}
	c.costSnapshot = costSnapshot
	log := c.log
	generatedAtValue := c.generatedAt
	c.mu.Unlock()
	if !costComplete {
		log("gateway_quota_snapshot_incomplete", map[string]any{
			"generatedAt":         generatedAtValue,
			"costEntryCount":      len(snapshot.CostEntries),
			"costEntriesComplete": costComplete,
			"maxCostEntries":      MaxGatewayQuotaSnapshotCostEntries,
		}, "网关配额快照不完整，运行时将对缺失 scope 通过 DB service 精确补判")
	}
	return nil
}

// ClearGatewayQuotaSnapshot mirrors clearGatewayQuotaSnapshot.
func (c *SnapshotCache) ClearGatewayQuotaSnapshot() {
	c.mu.Lock()
	c.generatedAt = ""
	c.costComplete = false
	c.costSnapshot = map[string]RequestQuotaCosts{}
	c.mu.Unlock()
	c.clearSharedMemo()
}

// ReadCostsSnapshot mirrors readGatewayQuotaCostsSnapshot (memory driver).
func (c *SnapshotCache) ReadCostsSnapshot(input QuotaCostSnapshotEntry) (RequestQuotaCosts, bool) {
	if c.modes.RedisCache {
		return RequestQuotaCosts{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	costs, ok := c.costSnapshot[costSnapshotKey(input)]
	if !ok {
		return RequestQuotaCosts{}, false
	}
	return CloneRequestQuotaCosts(costs), true
}

// ReadCostsSnapshotAsync mirrors readGatewayQuotaCostsSnapshotAsync.
func (c *SnapshotCache) ReadCostsSnapshotAsync(ctx context.Context, input QuotaCostSnapshotEntry) (RequestQuotaCosts, bool, error) {
	if !c.modes.RedisCache {
		costs, ok := c.ReadCostsSnapshot(input)
		return costs, ok, nil
	}
	snapshot, err := c.readSharedGatewayQuotaSnapshot(ctx)
	if err != nil || snapshot == nil {
		return RequestQuotaCosts{}, false, err
	}
	c.sharedMu.Lock()
	costs, ok := c.sharedCosts[costSnapshotKey(input)]
	c.sharedMu.Unlock()
	if !ok {
		return RequestQuotaCosts{}, false, nil
	}
	return CloneRequestQuotaCosts(costs), true, nil
}

// IsCostSnapshotComplete mirrors isGatewayQuotaCostSnapshotComplete.
func (c *SnapshotCache) IsCostSnapshotComplete() bool {
	if c.modes.RedisCache {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.generatedAt != "" && c.costComplete
}

// IsCostSnapshotCompleteAsync mirrors isGatewayQuotaCostSnapshotCompleteAsync.
func (c *SnapshotCache) IsCostSnapshotCompleteAsync(ctx context.Context) (bool, error) {
	if !c.modes.RedisCache {
		return c.IsCostSnapshotComplete(), nil
	}
	snapshot, err := c.readSharedGatewayQuotaSnapshot(ctx)
	if err != nil || snapshot == nil {
		return false, err
	}
	return snapshot.CostEntriesComplete == nil || *snapshot.CostEntriesComplete, nil
}

// IsCostSnapshotIncomplete mirrors isGatewayQuotaCostSnapshotIncomplete.
func (c *SnapshotCache) IsCostSnapshotIncomplete() bool {
	if c.modes.RedisCache {
		return true
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.generatedAt != "" && !c.costComplete
}

// IsCostSnapshotIncompleteAsync mirrors isGatewayQuotaCostSnapshotIncompleteAsync.
func (c *SnapshotCache) IsCostSnapshotIncompleteAsync(ctx context.Context) (bool, error) {
	if !c.modes.RedisCache {
		return c.IsCostSnapshotIncomplete(), nil
	}
	snapshot, err := c.readSharedGatewayQuotaSnapshot(ctx)
	if err != nil || snapshot == nil {
		return false, err
	}
	return snapshot.CostEntriesComplete != nil && !*snapshot.CostEntriesComplete, nil
}

// HasGatewayQuotaSnapshotAsync mirrors hasGatewayQuotaSnapshotAsync.
func (c *SnapshotCache) HasGatewayQuotaSnapshotAsync(ctx context.Context) (bool, error) {
	if !c.modes.RedisCache {
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.generatedAt != "", nil
	}
	snapshot, err := c.readSharedGatewayQuotaSnapshot(ctx)
	if err != nil {
		return false, err
	}
	return snapshot != nil, nil
}

// SnapshotRuntime mirrors gatewayQuotaSnapshotRuntime.
func (c *SnapshotCache) SnapshotRuntime() SnapshotRuntimeInfo {
	if c.modes.RedisCache {
		c.sharedMu.Lock()
		defer c.sharedMu.Unlock()
		info := SnapshotRuntimeInfo{
			GeneratedAt:    sharedGeneratedAt(c.shared),
			CostEntryCount: len(c.sharedCosts),
		}
		if c.shared != nil {
			info.CostEntriesComplete = c.shared.CostEntriesComplete == nil || *c.shared.CostEntriesComplete
		}
		return info
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return SnapshotRuntimeInfo{
		GeneratedAt:         c.generatedAt,
		CostEntryCount:      len(c.costSnapshot),
		CostEntriesComplete: c.costComplete,
	}
}

func sharedGeneratedAt(snapshot *GatewayQuotaSnapshot) string {
	if snapshot == nil {
		return ""
	}
	return snapshot.GeneratedAt
}

// clearSharedMemo mirrors clearSharedGatewayQuotaSnapshotMemo.
func (c *SnapshotCache) clearSharedMemo() {
	c.sharedMu.Lock()
	defer c.sharedMu.Unlock()
	c.shared = nil
	c.sharedCosts = map[string]RequestQuotaCosts{}
	c.sharedFetchedAtMs = 0
	c.loading = nil
}

// readSharedGatewayQuotaSnapshot mirrors readSharedGatewayQuotaSnapshot: 1s
// memo, in-flight load dedupe, transport errors degrade to "no snapshot"
// (with a warn log) while document validation errors propagate.
func (c *SnapshotCache) readSharedGatewayQuotaSnapshot(ctx context.Context) (*GatewayQuotaSnapshot, error) {
	if !c.modes.RedisCache || !c.modes.RedisRuntimeState {
		return nil, nil
	}
	ctx = ensureCtx(ctx)
	c.sharedMu.Lock()
	nowMs := c.now().UnixMilli()
	if c.sharedFetchedAtMs > 0 && nowMs-c.sharedFetchedAtMs < sharedSnapshotMemoTTL.Milliseconds() {
		snapshot := c.shared
		c.sharedMu.Unlock()
		return snapshot, nil
	}
	if c.loading != nil {
		ch := c.loading
		c.sharedMu.Unlock()
		result := <-ch
		return result.snapshot, result.err
	}
	ch := make(chan sharedLoadResult, 1)
	c.loading = ch
	c.sharedMu.Unlock()

	var target *GatewayQuotaSnapshot
	found, err := c.runtimeState.GetJSON(ctx, GatewayQuotaSnapshotRuntimeStateStoreName, GatewayQuotaSnapshotRuntimeStateKey, &target)
	if err != nil {
		c.log("gateway_quota_snapshot_runtime_state_read_failed", map[string]any{"error": err.Error()},
			"读取 Redis runtime state 网关配额快照失败，将回退到 DB service 精确补判")
		c.sharedMu.Lock()
		c.clearSharedMemoLocked()
		c.sharedFetchedAtMs = c.now().UnixMilli()
		c.loading = nil
		c.sharedMu.Unlock()
		ch <- sharedLoadResult{snapshot: nil, err: nil}
		return nil, nil
	}
	result, processErr := c.replaceSharedGatewayQuotaSnapshotMemo(target, found)
	c.sharedMu.Lock()
	c.loading = nil
	c.sharedMu.Unlock()
	ch <- sharedLoadResult{snapshot: result.snapshot, err: processErr}
	return result.snapshot, processErr
}

// replaceSharedGatewayQuotaSnapshotMemo mirrors replaceSharedGatewayQuotaSnapshotMemo.
func (c *SnapshotCache) replaceSharedGatewayQuotaSnapshotMemo(snapshot *GatewayQuotaSnapshot, found bool) (sharedLoadResult, error) {
	c.sharedMu.Lock()
	defer c.sharedMu.Unlock()
	c.sharedFetchedAtMs = c.now().UnixMilli()
	if !found || snapshot == nil {
		c.shared = nil
		c.sharedCosts = map[string]RequestQuotaCosts{}
		return sharedLoadResult{snapshot: nil}, nil
	}
	generatedAt, err := requiredRfc3339Instant(snapshot.GeneratedAt, "Redis runtime state 网关额度快照 generatedAt")
	if err != nil {
		c.clearSharedMemoLocked()
		return sharedLoadResult{snapshot: nil, err: err}, err
	}
	normalized := *snapshot
	normalized.GeneratedAt = generatedAt
	c.shared = &normalized
	c.sharedCosts = make(map[string]RequestQuotaCosts, len(normalized.CostEntries))
	for _, entry := range normalized.CostEntries {
		c.sharedCosts[costSnapshotKey(entry)] = CloneRequestQuotaCosts(entry.Costs)
	}
	if !(normalized.CostEntriesComplete == nil || *normalized.CostEntriesComplete) {
		c.log("gateway_quota_snapshot_runtime_state_incomplete", map[string]any{
			"generatedAt":         normalized.GeneratedAt,
			"costEntryCount":      len(normalized.CostEntries),
			"costEntriesComplete": false,
		}, "Redis runtime state 网关配额快照不完整，运行时将对缺失 scope 通过 DB service 精确补判")
	}
	return sharedLoadResult{snapshot: &normalized}, nil
}

func (c *SnapshotCache) clearSharedMemoLocked() {
	c.shared = nil
	c.sharedCosts = map[string]RequestQuotaCosts{}
	c.sharedFetchedAtMs = 0
}

func itoa(value int) string {
	return strconv.Itoa(value)
}
