package gatewayquota

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"time"
)

// Snapshot pagination constants mirror quota-snapshot-cache.service.ts.
const (
	GatewayQuotaSnapshotCostPageSize            = 5000
	GatewayQuotaSnapshotAuthorizationPageSize   = 5000
	MaxGatewayQuotaSnapshotCostEntries          = GatewayQuotaSnapshotCostPageSize
	MaxGatewayQuotaSnapshotAuthorizationEntries = GatewayQuotaSnapshotAuthorizationPageSize
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

// AuthorizationQuotaSnapshotEntry mirrors GatewayAuthorizationQuotaSnapshotEntry.
type AuthorizationQuotaSnapshotEntry struct {
	ScopeType       string   `json:"scopeType"` // account_authorization | group_authorization
	AuthorizationID string   `json:"authorizationId"`
	Decision        Decision `json:"decision"`
}

// GatewayQuotaSnapshot mirrors GatewayQuotaSnapshot. The complete flags are
// pointers so a missing field defaults to complete (?? true) like Node.
type GatewayQuotaSnapshot struct {
	GeneratedAt                  string                            `json:"generatedAt"`
	CostEntries                  []QuotaCostSnapshotEntry          `json:"costEntries"`
	AuthorizationEntries         []AuthorizationQuotaSnapshotEntry `json:"authorizationEntries"`
	CostEntriesComplete          *bool                             `json:"costEntriesComplete,omitempty"`
	AuthorizationEntriesComplete *bool                             `json:"authorizationEntriesComplete,omitempty"`
	Timezone                     *string                           `json:"timezone,omitempty"`
	StatDate                     *string                           `json:"statDate,omitempty"`
	StatWeek                     *string                           `json:"statWeek,omitempty"`
	StatMonth                    *string                           `json:"statMonth,omitempty"`
}

// SnapshotRuntimeInfo mirrors gatewayQuotaSnapshotRuntime().
type SnapshotRuntimeInfo struct {
	GeneratedAt                  string
	CostEntryCount               int
	AuthorizationEntryCount      int
	CostEntriesComplete          bool
	AuthorizationEntriesComplete bool
}

// SnapshotCache is the gateway quota snapshot cache: a process-local pair of
// maps fed by ReplaceGatewayQuotaSnapshot (memory cache driver) plus a 1s-memo
// read-through of the Redis runtime state document (redis cache driver), with
// the authorization watermark/version semantics of the Node module.
type SnapshotCache struct {
	modes        Modes
	runtimeState RuntimeStateStore
	now          func() time.Time
	log          LogHook

	mu                   sync.Mutex
	generatedAt          string
	costComplete         bool
	authzComplete        bool
	authzInvalidated     bool
	authzInvalidatedAtMs int64
	authzVersion         int64
	costSnapshot         map[string]RequestQuotaCosts
	authzSnapshot        map[string]Decision

	sharedMu          sync.Mutex
	shared            *GatewayQuotaSnapshot
	sharedCosts       map[string]RequestQuotaCosts
	sharedAuthz       map[string]Decision
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
		modes:         modes,
		runtimeState:  runtimeState,
		now:           now,
		log:           log,
		costSnapshot:  map[string]RequestQuotaCosts{},
		authzSnapshot: map[string]Decision{},
		sharedCosts:   map[string]RequestQuotaCosts{},
		sharedAuthz:   map[string]Decision{},
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

// authorizationSnapshotKey mirrors authorizationSnapshotKey.
func authorizationSnapshotKey(scopeType, authorizationID string) string {
	return scopeType + "\x00" + authorizationID
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
	authzComplete := snapshot.AuthorizationEntriesComplete == nil || *snapshot.AuthorizationEntriesComplete
	c.mu.Lock()
	c.generatedAt = generatedAt
	c.costComplete = costComplete
	c.authzComplete = authzComplete
	c.authzInvalidated = false
	c.authzVersion++
	costSnapshot := make(map[string]RequestQuotaCosts, len(snapshot.CostEntries))
	for _, entry := range snapshot.CostEntries {
		costSnapshot[costSnapshotKey(entry)] = CloneRequestQuotaCosts(entry.Costs)
	}
	authzSnapshot := make(map[string]Decision, len(snapshot.AuthorizationEntries))
	for _, entry := range snapshot.AuthorizationEntries {
		authzSnapshot[authorizationSnapshotKey(entry.ScopeType, entry.AuthorizationID)] = Decision{
			Allowed: entry.Decision.Allowed,
			Message: entry.Decision.Message,
		}
	}
	c.costSnapshot = costSnapshot
	c.authzSnapshot = authzSnapshot
	log := c.log
	generatedAtValue := c.generatedAt
	c.mu.Unlock()
	if !costComplete || !authzComplete {
		log("gateway_quota_snapshot_incomplete", map[string]any{
			"generatedAt":                  generatedAtValue,
			"costEntryCount":               len(snapshot.CostEntries),
			"authorizationEntryCount":      len(snapshot.AuthorizationEntries),
			"costEntriesComplete":          costComplete,
			"authorizationEntriesComplete": authzComplete,
			"maxCostEntries":               MaxGatewayQuotaSnapshotCostEntries,
			"maxAuthorizationEntries":      MaxGatewayQuotaSnapshotAuthorizationEntries,
		}, "网关配额快照不完整，运行时将对缺失 scope 通过 DB service 精确补判")
	}
	return nil
}

// ClearGatewayQuotaSnapshot mirrors clearGatewayQuotaSnapshot.
func (c *SnapshotCache) ClearGatewayQuotaSnapshot() {
	c.mu.Lock()
	c.generatedAt = ""
	c.costComplete = false
	c.authzComplete = false
	c.authzInvalidated = false
	c.authzInvalidatedAtMs = 0
	c.authzVersion++
	c.costSnapshot = map[string]RequestQuotaCosts{}
	c.authzSnapshot = map[string]Decision{}
	c.mu.Unlock()
	c.clearSharedMemo()
}

// InvalidateAuthorizationQuotaSnapshot mirrors
// invalidateGatewayAuthorizationQuotaSnapshot: nil publishedAt means "now";
// the watermark only moves forward (monotonic max) and the version bumps so
// runtime cache keys rotate (inval 总线对齐：授权快照失效即版本推进).
func (c *SnapshotCache) InvalidateAuthorizationQuotaSnapshot(publishedAt *string) error {
	var publishedAtMs int64
	if publishedAt == nil {
		publishedAtMs = c.now().UnixMilli()
	} else {
		normalized, err := requiredRfc3339Instant(*publishedAt, "网关额度快照授权失效 publishedAt")
		if err != nil {
			return err
		}
		ms, ok := rfc3339InstantMilliseconds(normalized)
		if !ok {
			return errors.New("网关额度快照授权失效 publishedAt 规范化后无效")
		}
		publishedAtMs = ms
	}
	c.mu.Lock()
	c.authzInvalidated = true
	if publishedAtMs > c.authzInvalidatedAtMs {
		c.authzInvalidatedAtMs = publishedAtMs
	}
	c.authzComplete = false
	c.authzVersion++
	c.authzSnapshot = map[string]Decision{}
	c.mu.Unlock()
	c.clearSharedMemo()
	return nil
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

// ReadAuthorizationSnapshot mirrors readGatewayAuthorizationQuotaSnapshot.
func (c *SnapshotCache) ReadAuthorizationSnapshot(scopeType, authorizationID string) (Decision, bool) {
	if c.modes.RedisCache || authorizationID == "" {
		return Decision{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	decision, ok := c.authzSnapshot[authorizationSnapshotKey(scopeType, authorizationID)]
	if !ok {
		return Decision{}, false
	}
	return decision, true
}

// ReadAuthorizationSnapshotAsync mirrors readGatewayAuthorizationQuotaSnapshotAsync.
func (c *SnapshotCache) ReadAuthorizationSnapshotAsync(ctx context.Context, scopeType, authorizationID string) (Decision, bool, error) {
	if !c.modes.RedisCache {
		decision, ok := c.ReadAuthorizationSnapshot(scopeType, authorizationID)
		return decision, ok, nil
	}
	if authorizationID == "" {
		return Decision{}, false, nil
	}
	snapshot, err := c.readSharedGatewayQuotaSnapshot(ctx)
	if err != nil || snapshot == nil {
		return Decision{}, false, err
	}
	usable, err := c.sharedSnapshotAuthorizationUsable(snapshot)
	if err != nil {
		return Decision{}, false, err
	}
	if !usable {
		return Decision{}, false, nil
	}
	c.sharedMu.Lock()
	decision, ok := c.sharedAuthz[authorizationSnapshotKey(scopeType, authorizationID)]
	c.sharedMu.Unlock()
	if !ok {
		return Decision{}, false, nil
	}
	return decision, true, nil
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

// IsAuthorizationSnapshotComplete mirrors isGatewayAuthorizationSnapshotComplete.
func (c *SnapshotCache) IsAuthorizationSnapshotComplete() bool {
	if c.modes.RedisCache {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.generatedAt != "" && c.authzComplete && !c.authzInvalidated
}

// IsAuthorizationSnapshotCompleteAsync mirrors isGatewayAuthorizationSnapshotCompleteAsync.
func (c *SnapshotCache) IsAuthorizationSnapshotCompleteAsync(ctx context.Context) (bool, error) {
	if !c.modes.RedisCache {
		return c.IsAuthorizationSnapshotComplete(), nil
	}
	snapshot, err := c.readSharedGatewayQuotaSnapshot(ctx)
	if err != nil || snapshot == nil {
		return false, err
	}
	complete, err := c.sharedSnapshotAuthorizationComplete(snapshot)
	if err != nil {
		return false, err
	}
	return complete, nil
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

// IsAuthorizationSnapshotIncomplete mirrors isGatewayAuthorizationSnapshotIncomplete.
func (c *SnapshotCache) IsAuthorizationSnapshotIncomplete() bool {
	if c.modes.RedisCache {
		return true
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.authzInvalidated || (c.generatedAt != "" && !c.authzComplete)
}

// IsAuthorizationSnapshotIncompleteAsync mirrors isGatewayAuthorizationSnapshotIncompleteAsync.
func (c *SnapshotCache) IsAuthorizationSnapshotIncompleteAsync(ctx context.Context) (bool, error) {
	if !c.modes.RedisCache {
		return c.IsAuthorizationSnapshotIncomplete(), nil
	}
	snapshot, err := c.readSharedGatewayQuotaSnapshot(ctx)
	if err != nil {
		return false, err
	}
	if snapshot == nil {
		c.mu.Lock()
		invalidated := c.authzInvalidated
		c.mu.Unlock()
		return invalidated, nil
	}
	complete, err := c.sharedSnapshotAuthorizationComplete(snapshot)
	if err != nil {
		return false, err
	}
	return !complete, nil
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

// AuthorizationQuotaSnapshotVersion mirrors gatewayAuthorizationQuotaSnapshotVersion.
func (c *SnapshotCache) AuthorizationQuotaSnapshotVersion() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.authzVersion
}

// SnapshotRuntime mirrors gatewayQuotaSnapshotRuntime.
func (c *SnapshotCache) SnapshotRuntime() SnapshotRuntimeInfo {
	if c.modes.RedisCache {
		c.sharedMu.Lock()
		defer c.sharedMu.Unlock()
		info := SnapshotRuntimeInfo{
			GeneratedAt:             sharedGeneratedAt(c.shared),
			CostEntryCount:          len(c.sharedCosts),
			AuthorizationEntryCount: len(c.sharedAuthz),
			CostEntriesComplete:     false,
		}
		if c.shared != nil {
			info.CostEntriesComplete = c.shared.CostEntriesComplete == nil || *c.shared.CostEntriesComplete
			// sharedMu already held: use the locked usability check.
			usable, err := c.sharedSnapshotAuthorizationUsableLocked(c.shared)
			info.AuthorizationEntriesComplete = err == nil && usable &&
				(c.shared.AuthorizationEntriesComplete == nil || *c.shared.AuthorizationEntriesComplete)
		}
		return info
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return SnapshotRuntimeInfo{
		GeneratedAt:                  c.generatedAt,
		CostEntryCount:               len(c.costSnapshot),
		AuthorizationEntryCount:      len(c.authzSnapshot),
		CostEntriesComplete:          c.costComplete,
		AuthorizationEntriesComplete: c.authzComplete,
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
	c.sharedAuthz = map[string]Decision{}
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
		c.sharedAuthz = map[string]Decision{}
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
	c.sharedAuthz = make(map[string]Decision, len(normalized.AuthorizationEntries))
	for _, entry := range normalized.AuthorizationEntries {
		c.sharedAuthz[authorizationSnapshotKey(entry.ScopeType, entry.AuthorizationID)] = Decision{
			Allowed: entry.Decision.Allowed,
			Message: entry.Decision.Message,
		}
	}
	if c.authzInvalidated {
		usable, usableErr := c.sharedSnapshotAuthorizationUsableLocked(&normalized)
		if usableErr != nil {
			c.clearSharedMemoLocked()
			return sharedLoadResult{snapshot: nil, err: usableErr}, usableErr
		}
		if usable {
			c.authzInvalidated = false
			c.authzInvalidatedAtMs = 0
			c.authzVersion++
		}
	}
	if !(normalized.CostEntriesComplete == nil || *normalized.CostEntriesComplete) {
		c.log("gateway_quota_snapshot_runtime_state_incomplete", map[string]any{
			"generatedAt":             normalized.GeneratedAt,
			"costEntryCount":          len(normalized.CostEntries),
			"authorizationEntryCount": len(normalized.AuthorizationEntries),
			"costEntriesComplete":     false,
		}, "Redis runtime state 网关配额快照不完整，运行时将对缺失 scope 通过 DB service 精确补判")
	}
	return sharedLoadResult{snapshot: &normalized}, nil
}

func (c *SnapshotCache) clearSharedMemoLocked() {
	c.shared = nil
	c.sharedCosts = map[string]RequestQuotaCosts{}
	c.sharedAuthz = map[string]Decision{}
	c.sharedFetchedAtMs = 0
}

// sharedSnapshotAuthorizationComplete mirrors sharedSnapshotAuthorizationComplete.
func (c *SnapshotCache) sharedSnapshotAuthorizationComplete(snapshot *GatewayQuotaSnapshot) (bool, error) {
	usable, err := c.sharedSnapshotAuthorizationUsable(snapshot)
	if err != nil {
		return false, err
	}
	return (snapshot.AuthorizationEntriesComplete == nil || *snapshot.AuthorizationEntriesComplete) && usable, nil
}

// sharedSnapshotAuthorizationUsable mirrors sharedSnapshotAuthorizationUsable.
// Requires sharedMu (reads the invalidation watermark).
func (c *SnapshotCache) sharedSnapshotAuthorizationUsable(snapshot *GatewayQuotaSnapshot) (bool, error) {
	c.sharedMu.Lock()
	defer c.sharedMu.Unlock()
	return c.sharedSnapshotAuthorizationUsableLocked(snapshot)
}

func (c *SnapshotCache) sharedSnapshotAuthorizationUsableLocked(snapshot *GatewayQuotaSnapshot) (bool, error) {
	if !c.authzInvalidated {
		return true, nil
	}
	generatedAtMs, ok := rfc3339InstantMilliseconds(snapshot.GeneratedAt)
	if !ok {
		return false, errors.New("Redis runtime state 网关额度快照 generatedAt 必须是带 Z 或数值 offset 的 RFC3339 时间")
	}
	return generatedAtMs > c.authzInvalidatedAtMs, nil
}

func itoa(value int) string {
	return strconv.Itoa(value)
}
