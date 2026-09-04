package gatewayproxyhealth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
)

// Ports modules/rate-limit/penalty-window-rate-limit.ts plus the two gateway
// consumers runtime/authenticated-models-rate-limit.service.ts and
// runtime/public-models-rate-limit.service.ts.

// PenaltyWindowRateLimitRule mirrors PenaltyWindowRateLimitRule.
type PenaltyWindowRateLimitRule struct {
	WindowSeconds int64
	MaxRequests   int64
}

// PenaltyWindowMode mirrors penaltyMode.
type PenaltyWindowMode string

const (
	PenaltyModeExponential PenaltyWindowMode = "exponential"
	PenaltyModeFixedWindow PenaltyWindowMode = "fixed_window"
)

// PenaltyWindowStoreOptions mirrors createPenaltyWindowRateLimitStore input.
type PenaltyWindowStoreOptions struct {
	Name              string
	MaxEntries        int
	CleanupIntervalMs int64
	MaxIdleMs         int64
	MaxPenaltyMs      int64
	PenaltyMode       PenaltyWindowMode
	// RedisDriver mirrors runtimeStateDriver === 'redis': the sync memory
	// consume entry refuses to run (Node assertPenaltyWindowMemoryStoreAllowed).
	RedisDriver bool
}

const (
	defaultPenaltyCleanupIntervalMs = int64(60_000)
	defaultPenaltyMaxEntries        = 20_000
	defaultPenaltyMaxIdleMs         = int64(86_400_000)
	defaultPenaltyMaxPenaltyMs      = int64(15 * 60_000)
)

// PenaltyWindowRateLimitStore mirrors PenaltyWindowRateLimitStore.
type PenaltyWindowRateLimitStore struct {
	mu                sync.Mutex
	clock             Clock
	name              string
	maxEntries        int
	cleanupIntervalMs int64
	maxIdleMs         int64
	maxPenaltyMs      int64
	penaltyMode       PenaltyWindowMode
	redisDriver       bool
	nextCleanupAtMs   int64
	entries           map[string]*penaltyWindowEntry
	order             []string
}

type penaltyWindowEntry struct {
	windowStartedAt int64
	count           int64
	penaltyMs       int64
	blockedUntilMs  *int64
	lastSeenAtMs    int64
}

// NewPenaltyWindowRateLimitStore mirrors createPenaltyWindowRateLimitStore.
func NewPenaltyWindowRateLimitStore(clock Clock, input PenaltyWindowStoreOptions) *PenaltyWindowRateLimitStore {
	mode := input.PenaltyMode
	if mode == "" {
		mode = PenaltyModeExponential
	}
	maxEntries := input.MaxEntries
	if maxEntries <= 0 {
		maxEntries = defaultPenaltyMaxEntries
	}
	cleanupIntervalMs := input.CleanupIntervalMs
	if cleanupIntervalMs <= 0 {
		cleanupIntervalMs = defaultPenaltyCleanupIntervalMs
	}
	maxIdleMs := input.MaxIdleMs
	if maxIdleMs <= 0 {
		maxIdleMs = defaultPenaltyMaxIdleMs
	}
	maxPenaltyMs := input.MaxPenaltyMs
	if maxPenaltyMs <= 0 {
		maxPenaltyMs = defaultPenaltyMaxPenaltyMs
	}
	return &PenaltyWindowRateLimitStore{
		clock:             clock,
		name:              input.Name,
		maxEntries:        maxEntries,
		cleanupIntervalMs: cleanupIntervalMs,
		maxIdleMs:         maxIdleMs,
		maxPenaltyMs:      maxPenaltyMs,
		penaltyMode:       mode,
		redisDriver:       input.RedisDriver,
		entries:           map[string]*penaltyWindowEntry{},
	}
}

// Clear mirrors clearPenaltyWindowRateLimitStore.
func (store *PenaltyWindowRateLimitStore) Clear() {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.entries = map[string]*penaltyWindowEntry{}
	store.order = nil
	store.nextCleanupAtMs = 0
}

// PenaltyWindowDecision mirrors PenaltyWindowRateLimitDecision; Rule /
// RetryAfterSeconds / StoreName / Limit stay nil/empty where Node leaves them
// undefined.
type PenaltyWindowDecision struct {
	Allowed           bool
	RetryAfterSeconds *int64
	Rule              *PenaltyWindowRateLimitRule
	StoreName         string
	Limit             *int64
}

// PenaltyWindowScope identifies a consume group ('api_key' / 'api_key_ip').
type PenaltyWindowScope string

// PenaltyWindowGroup mirrors PenaltyWindowRateLimitGroup.
type PenaltyWindowGroup struct {
	Scope    PenaltyWindowScope
	Store    *PenaltyWindowRateLimitStore
	ScopeKey string
	Rules    []PenaltyWindowRateLimitRule
}

// PenaltyWindowGroupDecision mirrors PenaltyWindowRateLimitGroupDecision.
type PenaltyWindowGroupDecision struct {
	PenaltyWindowDecision
	Scope PenaltyWindowScope
}

// PenaltyWindowRateLimiter composes the stores with the driver and Redis
// client (Node uses the shared state Redis client without an extra deadline).
type PenaltyWindowRateLimiter struct {
	clock       Clock
	redisDriver bool
	redisClient redisStateClient
	namespace   string
}

// NewPenaltyWindowRateLimiter builds the limiter. redisClient nil with
// redisDriver set mirrors the Node missing-stateUrl failure at consume time.
func NewPenaltyWindowRateLimiter(clock Clock, redisDriver bool, redisClient redisStateClient, namespace string) *PenaltyWindowRateLimiter {
	return &PenaltyWindowRateLimiter{clock: clock, redisDriver: redisDriver, redisClient: redisClient, namespace: namespace}
}

func (store *PenaltyWindowRateLimitStore) nowMs() int64 { return ClockNowMs(store.clock) }

// ConsumeMemory mirrors consumePenaltyWindowRateLimit (memory-only path; the
// Redis driver forbids it like the Node assert).
func (store *PenaltyWindowRateLimitStore) ConsumeMemory(scopeKey string, rules []PenaltyWindowRateLimitRule, nowMs *int64) (PenaltyWindowDecision, error) {
	if store.redisDriver {
		return PenaltyWindowDecision{}, fmt.Errorf("高性能模式禁止使用本机 penalty window 限流状态：%s 必须使用 Redis async 限流入口", "consumePenaltyWindowRateLimit")
	}
	return store.consumeMemory(scopeKey, rules, nowMs), nil
}

func (store *PenaltyWindowRateLimitStore) consumeMemory(scopeKey string, rules []PenaltyWindowRateLimitRule, nowMs *int64) PenaltyWindowDecision {
	store.mu.Lock()
	defer store.mu.Unlock()
	normalizedNow := store.nowMs()
	if nowMs != nil {
		normalizedNow = *nowMs
	}
	store.cleanupLocked(normalizedNow)
	type inspectResult = penaltyWindowInspection
	var inspections []inspectResult
	for _, rule := range rules {
		if rule.MaxRequests <= 0 || rule.WindowSeconds <= 0 {
			continue
		}
		inspections = append(inspections, store.inspectBucketLocked(scopeKey, rule, normalizedNow))
	}
	for _, inspection := range inspections {
		if inspection.bucket.blockedUntilMs != nil && *inspection.bucket.blockedUntilMs > normalizedNow {
			blocked := blockedBucketDecision(inspection.rule, *inspection.bucket.blockedUntilMs-normalizedNow, store.name)
			return blocked
		}
	}
	for _, inspection := range inspections {
		inspection.commit()
	}
	return PenaltyWindowDecision{Allowed: true}
}

// penaltyWindowInspection carries one in-memory bucket inspection.
type penaltyWindowInspection struct {
	bucket *penaltyWindowEntry
	key    string
	rule   PenaltyWindowRateLimitRule
	commit func()
}

func (store *PenaltyWindowRateLimitStore) inspectBucketLocked(scopeKey string, rule PenaltyWindowRateLimitRule, nowMs int64) penaltyWindowInspection {
	windowMs := rule.WindowSeconds * 1000
	windowStartedAt := floorDiv(nowMs, windowMs) * windowMs
	key := fmt.Sprintf("%s:%d:%d", scopeKey, rule.WindowSeconds, rule.MaxRequests)
	current, ok := store.entries[key]
	var entry *penaltyWindowEntry
	if ok && current.windowStartedAt == windowStartedAt {
		entry = current
	} else {
		clone := &penaltyWindowEntry{
			windowStartedAt: windowStartedAt,
			count:           0,
			penaltyMs:       0,
			lastSeenAtMs:    nowMs,
		}
		if current != nil {
			clone.penaltyMs = current.penaltyMs
			clone.blockedUntilMs = current.blockedUntilMs
		}
		entry = clone
	}
	entry.lastSeenAtMs = nowMs

	if entry.blockedUntilMs != nil && *entry.blockedUntilMs > nowMs {
		if store.penaltyMode == PenaltyModeExponential {
			store.openPenaltyBlockLocked(entry, windowMs, nowMs)
		}
		store.setEntryLocked(key, entry)
		return penaltyWindowInspection{bucket: entry, key: key, rule: rule, commit: func() {}}
	}

	entry.blockedUntilMs = nil
	if entry.count >= rule.MaxRequests {
		if store.penaltyMode == PenaltyModeFixedWindow {
			entry.penaltyMs = 0
			blockedUntil := windowStartedAt + windowMs
			entry.blockedUntilMs = &blockedUntil
		} else {
			store.openPenaltyBlockLocked(entry, windowMs, nowMs)
		}
		store.setEntryLocked(key, entry)
		return penaltyWindowInspection{bucket: entry, key: key, rule: rule, commit: func() {}}
	}

	return penaltyWindowInspection{bucket: entry, key: key, rule: rule, commit: func() {
		entry.count++
		entry.lastSeenAtMs = nowMs
		store.setEntryLocked(key, entry)
		store.trimLocked(nowMs)
	}}
}

func (store *PenaltyWindowRateLimitStore) setEntryLocked(key string, entry *penaltyWindowEntry) {
	if _, ok := store.entries[key]; !ok {
		store.order = append(store.order, key)
	}
	store.entries[key] = entry
}

func (store *PenaltyWindowRateLimitStore) openPenaltyBlockLocked(entry *penaltyWindowEntry, windowMs, nowMs int64) {
	maxPenaltyMs := maxInt64(windowMs, store.maxPenaltyMs)
	basePenaltyMs := int64(windowMs)
	if entry.penaltyMs > 0 {
		basePenaltyMs = entry.penaltyMs * 2
	}
	entry.penaltyMs = minInt64(maxPenaltyMs, basePenaltyMs)
	blockedUntil := nowMs + entry.penaltyMs
	entry.blockedUntilMs = &blockedUntil
}

func blockedBucketDecision(rule PenaltyWindowRateLimitRule, retryAfterMs int64, storeName string) PenaltyWindowDecision {
	retryAfterSeconds := ceilDiv(retryAfterMs, 1000)
	if retryAfterSeconds < 1 {
		retryAfterSeconds = 1
	}
	limit := rule.MaxRequests
	return PenaltyWindowDecision{
		Allowed:           false,
		RetryAfterSeconds: &retryAfterSeconds,
		Rule:              &rule,
		StoreName:         storeName,
		Limit:             &limit,
	}
}

func (store *PenaltyWindowRateLimitStore) cleanupLocked(nowMs int64) {
	if store.nextCleanupAtMs > nowMs && len(store.entries) <= store.maxEntries {
		return
	}
	for _, key := range append([]string(nil), store.order...) {
		entry, ok := store.entries[key]
		if !ok {
			continue
		}
		if entry.blockedUntilMs != nil && *entry.blockedUntilMs > nowMs {
			continue
		}
		if nowMs-entry.lastSeenAtMs > store.maxIdleMs {
			store.deleteEntryLocked(key)
		}
	}
	store.nextCleanupAtMs = nowMs + store.cleanupIntervalMs
	store.trimLocked(nowMs)
}

func (store *PenaltyWindowRateLimitStore) trimLocked(nowMs int64) {
	if len(store.entries) <= store.maxEntries {
		return
	}
	targetSize := int(math.Floor(float64(store.maxEntries) * 0.9))
	for _, key := range append([]string(nil), store.order...) {
		entry, ok := store.entries[key]
		if !ok {
			continue
		}
		notBlocked := entry.blockedUntilMs == nil || *entry.blockedUntilMs <= nowMs
		if notBlocked || len(store.entries) > targetSize {
			store.deleteEntryLocked(key)
		}
		if len(store.entries) <= targetSize {
			break
		}
	}
}

func (store *PenaltyWindowRateLimitStore) deleteEntryLocked(key string) {
	delete(store.entries, key)
	for i, existing := range store.order {
		if existing == key {
			store.order = append(store.order[:i], store.order[i+1:]...)
			break
		}
	}
}

// ConsumeAsync mirrors consumePenaltyWindowRateLimitAsync: Redis driver uses
// the shared Lua script, otherwise the memory path applies.
func (l *PenaltyWindowRateLimiter) ConsumeAsync(ctx context.Context, store *PenaltyWindowRateLimitStore, scopeKey string, rules []PenaltyWindowRateLimitRule, nowMs *int64) (PenaltyWindowDecision, error) {
	if !l.redisDriver {
		return store.consumeMemory(scopeKey, rules, nowMs), nil
	}
	normalizedNow := ClockNowMs(l.clock)
	if nowMs != nil {
		normalizedNow = *nowMs
	}
	activeRules := make([]PenaltyWindowRateLimitRule, 0, len(rules))
	for _, rule := range rules {
		if rule.MaxRequests > 0 && rule.WindowSeconds > 0 {
			activeRules = append(activeRules, rule)
		}
	}
	return l.consumeRedis(ctx, store, scopeKey, activeRules, normalizedNow)
}

// ConsumeGroupsAsync mirrors consumePenaltyWindowRateLimitGroupsAsync.
func (l *PenaltyWindowRateLimiter) ConsumeGroupsAsync(ctx context.Context, groups []PenaltyWindowGroup, nowMs *int64) (PenaltyWindowGroupDecision, error) {
	normalizedNow := ClockNowMs(l.clock)
	if nowMs != nil {
		normalizedNow = *nowMs
	}
	if !l.redisDriver {
		for _, group := range groups {
			decision := group.Store.consumeMemory(group.ScopeKey, group.Rules, nowMs)
			if !decision.Allowed {
				return PenaltyWindowGroupDecision{PenaltyWindowDecision: decision, Scope: group.Scope}, nil
			}
		}
		return PenaltyWindowGroupDecision{PenaltyWindowDecision: PenaltyWindowDecision{Allowed: true}}, nil
	}
	var activeGroups []penaltyActiveGroup
	for _, group := range groups {
		rules := make([]PenaltyWindowRateLimitRule, 0, len(group.Rules))
		for _, rule := range group.Rules {
			if rule.MaxRequests > 0 && rule.WindowSeconds > 0 {
				rules = append(rules, rule)
			}
		}
		if len(rules) > 0 {
			activeGroups = append(activeGroups, penaltyActiveGroup{group: group, rules: rules})
		}
	}
	return l.consumeRedisGroups(ctx, activeGroups, normalizedNow)
}

const redisPenaltyWindowRateLimitScript = `
local now_ms = tonumber(ARGV[1])
local rule_count = tonumber(ARGV[2])
local fixed_window_mode = tonumber(ARGV[3]) == 1
local counts = {}
local penalty_values = {}
local window_started_values = {}
local ttl_values = {}
local blocked_index = 0
local blocked_retry_ms = 0

for index = 1, rule_count do
  local offset = 4 + (index - 1) * 5
  local window_ms = tonumber(ARGV[offset])
  local window_started_at = tonumber(ARGV[offset + 1])
  local max_requests = tonumber(ARGV[offset + 2])
  local max_penalty_ms = tonumber(ARGV[offset + 3])
  local ttl_ms = tonumber(ARGV[offset + 4])
  local values = redis.call('HMGET', KEYS[index], 'windowStartedAt', 'count', 'penaltyMs', 'blockedUntilMs')
  local stored_window_started_at = tonumber(values[1])
  local count = 0
  if stored_window_started_at == window_started_at then
    count = tonumber(values[2]) or 0
  end
  local penalty_ms = tonumber(values[3]) or 0
  local blocked_until_ms = tonumber(values[4]) or 0
  counts[index] = count
  penalty_values[index] = penalty_ms
  window_started_values[index] = window_started_at
  ttl_values[index] = ttl_ms

  if blocked_until_ms > now_ms or count >= max_requests then
    local next_penalty_ms = penalty_ms
    if fixed_window_mode then
      next_penalty_ms = 0
      blocked_until_ms = window_started_at + window_ms
    else
      next_penalty_ms = penalty_ms > 0 and penalty_ms * 2 or window_ms
      if next_penalty_ms > max_penalty_ms then
        next_penalty_ms = max_penalty_ms
      end
      blocked_until_ms = now_ms + next_penalty_ms
    end
    redis.call(
      'HSET',
      KEYS[index],
      'windowStartedAt', tostring(window_started_at),
      'count', tostring(count),
      'penaltyMs', tostring(next_penalty_ms),
      'blockedUntilMs', tostring(blocked_until_ms)
    )
    redis.call('PEXPIRE', KEYS[index], ttl_ms)
    if blocked_index == 0 then
      blocked_index = index
      blocked_retry_ms = blocked_until_ms - now_ms
    end
  end
end

if blocked_index > 0 then
  return {0, blocked_retry_ms, blocked_index}
end

for index = 1, rule_count do
  redis.call(
    'HSET',
    KEYS[index],
    'windowStartedAt', tostring(window_started_values[index]),
    'count', tostring(counts[index] + 1),
    'penaltyMs', tostring(penalty_values[index]),
    'blockedUntilMs', '0'
  )
  redis.call('PEXPIRE', KEYS[index], ttl_values[index])
end
return {1, 0, 0}
`

const redisPenaltyWindowRateLimitGroupsScript = `
local now_ms = tonumber(ARGV[1])
local group_count = tonumber(ARGV[2])
local argument_index = 3
local key_index = 1

for group_index = 1, group_count do
  local rule_count = tonumber(ARGV[argument_index])
  local fixed_window_mode = tonumber(ARGV[argument_index + 1]) == 1
  argument_index = argument_index + 2
  local counts = {}
  local penalty_values = {}
  local window_started_values = {}
  local ttl_values = {}
  local blocked_rule_index = 0
  local blocked_retry_ms = 0

  for rule_index = 1, rule_count do
    local window_ms = tonumber(ARGV[argument_index])
    local window_started_at = tonumber(ARGV[argument_index + 1])
    local max_requests = tonumber(ARGV[argument_index + 2])
    local max_penalty_ms = tonumber(ARGV[argument_index + 3])
    local ttl_ms = tonumber(ARGV[argument_index + 4])
    argument_index = argument_index + 5
    local redis_key = KEYS[key_index + rule_index - 1]
    local values = redis.call('HMGET', redis_key, 'windowStartedAt', 'count', 'penaltyMs', 'blockedUntilMs')
    local stored_window_started_at = tonumber(values[1])
    local count = stored_window_started_at == window_started_at and (tonumber(values[2]) or 0) or 0
    local penalty_ms = tonumber(values[3]) or 0
    local blocked_until_ms = tonumber(values[4]) or 0
    counts[rule_index] = count
    penalty_values[rule_index] = penalty_ms
    window_started_values[rule_index] = window_started_at
    ttl_values[rule_index] = ttl_ms

    if blocked_until_ms > now_ms or count >= max_requests then
      local next_penalty_ms = penalty_ms
      if fixed_window_mode then
        next_penalty_ms = 0
        blocked_until_ms = window_started_at + window_ms
      else
        next_penalty_ms = penalty_ms > 0 and penalty_ms * 2 or window_ms
        if next_penalty_ms > max_penalty_ms then next_penalty_ms = max_penalty_ms end
        blocked_until_ms = now_ms + next_penalty_ms
      end
      redis.call(
        'HSET', redis_key,
        'windowStartedAt', tostring(window_started_at),
        'count', tostring(count),
        'penaltyMs', tostring(next_penalty_ms),
        'blockedUntilMs', tostring(blocked_until_ms)
      )
      redis.call('PEXPIRE', redis_key, ttl_ms)
      if blocked_rule_index == 0 then
        blocked_rule_index = rule_index
        blocked_retry_ms = blocked_until_ms - now_ms
      end
    end
  end

  if blocked_rule_index > 0 then
    local blocked_group_index = group_index
    return {0, blocked_retry_ms, blocked_rule_index, blocked_group_index}
  end

  for rule_index = 1, rule_count do
    local redis_key = KEYS[key_index + rule_index - 1]
    redis.call(
      'HSET', redis_key,
      'windowStartedAt', tostring(window_started_values[rule_index]),
      'count', tostring(counts[rule_index] + 1),
      'penaltyMs', tostring(penalty_values[rule_index]),
      'blockedUntilMs', '0'
    )
    redis.call('PEXPIRE', redis_key, ttl_values[rule_index])
  end
  key_index = key_index + rule_count
end

return {1, 0, 0, 0}
`

func (l *PenaltyWindowRateLimiter) redisStateClient() (redisStateClient, error) {
	if !l.redisDriver || l.redisClient == nil {
		return nil, errors.New("JUHE_AI_REDIS_STATE_URL 在 Redis runtime state driver 下必须配置")
	}
	return l.redisClient, nil
}

func (l *PenaltyWindowRateLimiter) consumeRedis(
	ctx context.Context,
	store *PenaltyWindowRateLimitStore,
	scopeKey string,
	rules []PenaltyWindowRateLimitRule,
	nowMs int64,
) (PenaltyWindowDecision, error) {
	if len(rules) == 0 {
		return PenaltyWindowDecision{Allowed: true}, nil
	}
	client, err := l.redisStateClient()
	if err != nil {
		return PenaltyWindowDecision{}, err
	}
	keys := make([]string, 0, len(rules))
	args := []any{formatSyncInt64(nowMs), formatSyncInt64(int64(len(rules)))}
	if store.penaltyMode == PenaltyModeFixedWindow {
		args = append(args, "1")
	} else {
		args = append(args, "0")
	}
	for _, rule := range rules {
		keys = append(keys, redisPenaltyWindowRateLimitKey(l.namespace, store.name, scopeKey, rule))
		windowMs := rule.WindowSeconds * 1000
		maxPenaltyMs := maxInt64(windowMs, store.maxPenaltyMs)
		args = append(args,
			formatSyncInt64(windowMs),
			formatSyncInt64(floorDiv(nowMs, windowMs)*windowMs),
			formatSyncInt64(rule.MaxRequests),
			formatSyncInt64(maxPenaltyMs),
			formatSyncInt64(maxInt64(maxInt64(store.maxIdleMs, maxPenaltyMs), windowMs)))
	}
	result, err := client.Eval(ctx, redisPenaltyWindowRateLimitScript, keys, args...).Result()
	if err != nil {
		return PenaltyWindowDecision{}, err
	}
	values := numericRedisArray(result)
	if len(values) > 0 && values[0] == 1 {
		return PenaltyWindowDecision{Allowed: true}, nil
	}
	ruleIndex := int64(1)
	if len(values) > 2 && values[2] > 0 {
		ruleIndex = values[2]
	}
	rule := rules[0]
	if ruleIndex >= 1 && int(ruleIndex) <= len(rules) {
		rule = rules[ruleIndex-1]
	}
	retryAfterMs := int64(rule.WindowSeconds * 1000)
	if len(values) > 1 && values[1] > 0 {
		retryAfterMs = values[1]
	}
	retryAfterSeconds := ceilDiv(retryAfterMs, 1000)
	if retryAfterSeconds < 1 {
		retryAfterSeconds = 1
	}
	limit := rule.MaxRequests
	return PenaltyWindowDecision{
		Allowed:           false,
		RetryAfterSeconds: &retryAfterSeconds,
		Rule:              &rule,
		StoreName:         store.name,
		Limit:             &limit,
	}, nil
}

func (l *PenaltyWindowRateLimiter) consumeRedisGroups(ctx context.Context, groups []penaltyActiveGroup, nowMs int64) (PenaltyWindowGroupDecision, error) {
	if len(groups) == 0 {
		return PenaltyWindowGroupDecision{PenaltyWindowDecision: PenaltyWindowDecision{Allowed: true}}, nil
	}
	client, err := l.redisStateClient()
	if err != nil {
		return PenaltyWindowGroupDecision{}, err
	}
	var keys []string
	args := []any{formatSyncInt64(nowMs), formatSyncInt64(int64(len(groups)))}
	for _, active := range groups {
		args = append(args, formatSyncInt64(int64(len(active.rules))))
		if active.group.Store.penaltyMode == PenaltyModeFixedWindow {
			args = append(args, "1")
		} else {
			args = append(args, "0")
		}
		for _, rule := range active.rules {
			keys = append(keys, redisPenaltyWindowRateLimitKey(l.namespace, active.group.Store.name, active.group.ScopeKey, rule))
			windowMs := rule.WindowSeconds * 1000
			maxPenaltyMs := maxInt64(windowMs, active.group.Store.maxPenaltyMs)
			args = append(args,
				formatSyncInt64(windowMs),
				formatSyncInt64(floorDiv(nowMs, windowMs)*windowMs),
				formatSyncInt64(rule.MaxRequests),
				formatSyncInt64(maxPenaltyMs),
				formatSyncInt64(maxInt64(maxInt64(active.group.Store.maxIdleMs, maxPenaltyMs), windowMs)))
		}
	}
	result, err := client.Eval(ctx, redisPenaltyWindowRateLimitGroupsScript, keys, args...).Result()
	if err != nil {
		return PenaltyWindowGroupDecision{}, err
	}
	values := numericRedisArray(result)
	if len(values) > 0 && values[0] == 1 {
		return PenaltyWindowGroupDecision{PenaltyWindowDecision: PenaltyWindowDecision{Allowed: true}}, nil
	}
	groupIndex := int64(1)
	if len(values) > 3 && values[3] > 0 {
		groupIndex = values[3]
	}
	ruleIndex := int64(1)
	if len(values) > 2 && values[2] > 0 {
		ruleIndex = values[2]
	}
	group := groups[0]
	if groupIndex >= 1 && int(groupIndex) <= len(groups) {
		group = groups[groupIndex-1]
	}
	rule := group.rules[0]
	if ruleIndex >= 1 && int(ruleIndex) <= len(group.rules) {
		rule = group.rules[ruleIndex-1]
	}
	retryAfterMs := int64(rule.WindowSeconds * 1000)
	if len(values) > 1 && values[1] > 0 {
		retryAfterMs = values[1]
	}
	retryAfterSeconds := ceilDiv(retryAfterMs, 1000)
	if retryAfterSeconds < 1 {
		retryAfterSeconds = 1
	}
	limit := rule.MaxRequests
	return PenaltyWindowGroupDecision{
		PenaltyWindowDecision: PenaltyWindowDecision{
			Allowed:           false,
			RetryAfterSeconds: &retryAfterSeconds,
			Rule:              &rule,
			StoreName:         group.group.Store.name,
			Limit:             &limit,
		},
		Scope: group.group.Scope,
	}, nil
}

// penaltyActiveGroup is one group with its active rules for the Redis pass.
type penaltyActiveGroup struct {
	group PenaltyWindowGroup
	rules []PenaltyWindowRateLimitRule
}

func numericRedisArray(value any) []int64 {
	values, ok := value.([]any)
	if !ok {
		return nil
	}
	output := make([]int64, 0, len(values))
	for _, item := range values {
		output = append(output, numericRedisResult(item))
	}
	return output
}

func floorDiv(value, divisor int64) int64 {
	quotient := value / divisor
	if (value%divisor != 0) && ((value < 0) != (divisor < 0)) {
		quotient--
	}
	return quotient
}

// redisPenaltyWindowRateLimitKey mirrors redisPenaltyWindowRateLimitKey.
func redisPenaltyWindowRateLimitKey(namespace, storeName, scopeKey string, rule PenaltyWindowRateLimitRule) string {
	return strings.Join([]string{
		namespacedRedisKey(namespace, "juhe-ai:rate-limit:penalty"),
		redisKeyHashB64(storeName),
		redisKeyHashB64(scopeKey),
		formatSyncInt64(rule.WindowSeconds),
		formatSyncInt64(rule.MaxRequests),
	}, ":")
}

func redisKeyHashB64(value string) string {
	sum := sha256.Sum256([]byte(value))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
