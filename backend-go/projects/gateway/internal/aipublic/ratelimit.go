// Penalty-window rate limiter for the /__aipublic__ family, ported from
// backend/src/modules/rate-limit/penalty-window-rate-limit.ts (memory mode,
// exponential penalty). Each rule gets its own bucket keyed
// "<scopeKey>:<windowSeconds>:<maxRequests>"; hitting the cap opens a penalty
// block whose duration doubles per consecutive block up to maxPenaltyMs.
// Node's Redis runtime-state driver shares these buckets across instances;
// the Go slice keeps the process-local memory model until the shared
// runtime-state driver lands for this store (tracked as a leftover).
package aipublic

import (
	"sync"
	"time"
)

const (
	rateLimitMaxEntries   = 20_000
	rateLimitMaxIdleMs    = 86_400_000
	rateLimitMaxPenaltyMs = 15 * 60_000
)

// PenaltyWindowLimiter is the memory store mirror of
// createPenaltyWindowRateLimitStore({name: 'external_source_public_api'}).
type PenaltyWindowLimiter struct {
	maxEntries  int
	maxIdleMs   int64
	maxPenalty  int64
	nowMs       func() int64
	mutex       sync.Mutex
	entries     map[string]*penaltyEntry
	nextCleanup int64
}

type penaltyEntry struct {
	windowStartedAt int64
	count           int
	penaltyMs       int64
	blockedUntilMs  int64
	hasBlock        bool
	lastSeenAtMs    int64
}

// LimitDecision mirrors PenaltyWindowRateLimitDecision.
type LimitDecision struct {
	Allowed           bool
	RetryAfterSeconds int
	Rule              RateLimitRule
}

// NewPenaltyWindowLimiter builds the limiter; now may be nil (time.Now).
func NewPenaltyWindowLimiter(now func() time.Time) *PenaltyWindowLimiter {
	clock := time.Now
	if now != nil {
		clock = now
	}
	return &PenaltyWindowLimiter{
		maxEntries: rateLimitMaxEntries,
		maxIdleMs:  rateLimitMaxIdleMs,
		maxPenalty: rateLimitMaxPenaltyMs,
		nowMs:      func() int64 { return clock().UnixMilli() },
		entries:    map[string]*penaltyEntry{},
	}
}

func (d *Deps) limiter() *PenaltyWindowLimiter {
	if d.rateLimiter == nil {
		d.rateLimiter = NewPenaltyWindowLimiter(d.Now)
	}
	return d.rateLimiter
}

// Clear resets every bucket (mirror of clearPenaltyWindowRateLimitStore).
func (l *PenaltyWindowLimiter) Clear() {
	l.mutex.Lock()
	defer l.mutex.Unlock()
	l.entries = map[string]*penaltyEntry{}
	l.nextCleanup = 0
}

// Consume mirrors consumePenaltyWindowRateLimit: inspect every active rule,
// block when any bucket disallows, otherwise commit one request to each.
func (l *PenaltyWindowLimiter) Consume(scopeKey string, rules []RateLimitRule) LimitDecision {
	if len(rules) == 0 {
		return LimitDecision{Allowed: true}
	}
	l.mutex.Lock()
	defer l.mutex.Unlock()
	nowMs := l.nowMs()
	l.cleanup(nowMs)

	type bucket struct {
		key     string
		entry   *penaltyEntry
		rule    RateLimitRule
		allowed bool
		retry   int
	}
	buckets := make([]bucket, 0, len(rules))
	for _, rule := range rules {
		if rule.MaxRequests <= 0 || rule.WindowSeconds <= 0 {
			continue
		}
		windowMs := int64(rule.WindowSeconds) * 1000
		windowStartedAt := nowMs / windowMs * windowMs
		key := scopeKey + ":" + itoa(rule.WindowSeconds) + ":" + itoa(rule.MaxRequests)
		current := l.entries[key]
		var entry *penaltyEntry
		if current != nil && current.windowStartedAt == windowStartedAt {
			entry = current
		} else {
			copied := penaltyEntry{}
			if current != nil {
				copied = *current
			}
			copied.windowStartedAt = windowStartedAt
			copied.count = 0
			entry = &copied
		}
		entry.lastSeenAtMs = nowMs

		allowed := true
		retrySeconds := 0
		if entry.hasBlock && entry.blockedUntilMs > nowMs {
			l.openPenaltyBlock(entry, windowMs, nowMs)
			retrySeconds = retryAfterSeconds(entry.blockedUntilMs - nowMs)
			allowed = false
		} else {
			entry.hasBlock = false
			if entry.count >= rule.MaxRequests {
				l.openPenaltyBlock(entry, windowMs, nowMs)
				retrySeconds = retryAfterSeconds(entry.blockedUntilMs - nowMs)
				allowed = false
			}
		}
		l.entries[key] = entry
		buckets = append(buckets, bucket{key: key, entry: entry, rule: rule, allowed: allowed, retry: retrySeconds})
	}

	for _, item := range buckets {
		if !item.allowed {
			return LimitDecision{Allowed: false, RetryAfterSeconds: item.retry, Rule: item.rule}
		}
	}
	for _, item := range buckets {
		item.entry.count++
		item.entry.lastSeenAtMs = nowMs
		l.entries[item.key] = item.entry
		l.trim(nowMs)
	}
	return LimitDecision{Allowed: true}
}

// openPenaltyBlock mirrors openPenaltyBlock (exponential doubling capped at
// max(windowMs, maxPenaltyMs)).
func (l *PenaltyWindowLimiter) openPenaltyBlock(entry *penaltyEntry, windowMs, nowMs int64) {
	maxPenaltyMs := windowMs
	if l.maxPenalty > maxPenaltyMs {
		maxPenaltyMs = l.maxPenalty
	}
	base := windowMs
	if entry.penaltyMs > 0 {
		base = entry.penaltyMs * 2
	}
	if base > maxPenaltyMs {
		base = maxPenaltyMs
	}
	entry.penaltyMs = base
	entry.blockedUntilMs = nowMs + base
	entry.hasBlock = true
}

func retryAfterSeconds(retryAfterMs int64) int {
	if retryAfterMs < 0 {
		retryAfterMs = 0
	}
	seconds := (retryAfterMs + 999) / 1000
	if seconds < 1 {
		return 1
	}
	return int(seconds)
}

// cleanup mirrors cleanupPenaltyWindowRateLimitStore.
func (l *PenaltyWindowLimiter) cleanup(nowMs int64) {
	if l.nextCleanup > nowMs && len(l.entries) <= l.maxEntries {
		return
	}
	l.nextCleanup = nowMs + 60_000
	for key, entry := range l.entries {
		if entry.hasBlock && entry.blockedUntilMs > nowMs {
			continue
		}
		if nowMs-entry.lastSeenAtMs > l.maxIdleMs {
			delete(l.entries, key)
		}
	}
}

// trim mirrors trimPenaltyWindowRateLimitStore: drop the oldest last-seen
// entries beyond maxEntries.
func (l *PenaltyWindowLimiter) trim(nowMs int64) {
	if len(l.entries) <= l.maxEntries {
		return
	}
	type keyed struct {
		key        string
		lastSeenAt int64
	}
	items := make([]keyed, 0, len(l.entries))
	for key, entry := range l.entries {
		items = append(items, keyed{key: key, lastSeenAt: entry.lastSeenAtMs})
	}
	for i := 1; i < len(items); i++ {
		for j := i; j > 0 && items[j].lastSeenAt < items[j-1].lastSeenAt; j-- {
			items[j], items[j-1] = items[j-1], items[j]
		}
	}
	for index := 0; index < len(items) && len(l.entries) > l.maxEntries; index++ {
		entry := l.entries[items[index].key]
		if entry.hasBlock && entry.blockedUntilMs > nowMs {
			continue
		}
		delete(l.entries, items[index].key)
	}
}
