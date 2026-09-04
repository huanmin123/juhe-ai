package gatewayproxyhealth

import (
	"math"
	"strings"
	"sync"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// Ports runtime/user-request-limit-counter.ts: local in-memory request-limit
// buckets (minute/day/week/month) with dirty-snapshot based Redis sync.

const userRequestLimitKeySeparator = "\x1f"

var userRequestLimitWindowOrder = []userRequestLimitWindow{
	userRequestLimitWindowPerMinute,
	userRequestLimitWindowPerDay,
	userRequestLimitWindowPerWeek,
	userRequestLimitWindowPerMonth,
}

const (
	userRequestLimitWindowPerMinute userRequestLimitWindow = "perMinute"
	userRequestLimitWindowPerDay    userRequestLimitWindow = "perDay"
	userRequestLimitWindowPerWeek   userRequestLimitWindow = "perWeek"
	userRequestLimitWindowPerMonth  userRequestLimitWindow = "perMonth"
)

const (
	defaultUserRequestLimitMaxEntries    = 500_000
	defaultUserRequestLimitCleanupStride = 4096
	defaultUserRequestLimitCleanupBatch  = 64
	userRequestLimitMinuteMs             = int64(60_000)
	userRequestLimitDayMs                = int64(24 * 60 * 60_000)
)

type userRequestLimitWindow string

type userRequestLimitCounterEntry struct {
	key              string
	systemAccountID  string
	window           userRequestLimitWindow
	bucket           string
	localCount       int64
	syncedLocalCount int64
	remoteTotal      int64
	redisTTLms       int64
	expiresAtMs      int64
	dirty            bool
}

type userRequestLimitBucketDefinition struct {
	bucket         string
	windowEndsAtMs int64
	expiresAtMs    int64
	redisTTLms     int64
}

type userRequestLimitBucketSnapshot struct {
	epochMinute int64
	perMinute   userRequestLimitBucketDefinition
	perDay      userRequestLimitBucketDefinition
	perWeek     userRequestLimitBucketDefinition
	perMonth    userRequestLimitBucketDefinition
}

// UserRequestLimitConsumeInput mirrors UserRequestLimitConsumeInput.
type UserRequestLimitConsumeInput struct {
	SystemAccountID string
	Settings        gatewayruntimecache.GatewaySettings
	Overrides       *gatewayruntimecache.UserRequestLimits
	NowMs           *int64
}

// UserRequestLimitDecision mirrors UserRequestLimitDecision (counter-local
// shape; the port-facing conversion lives in the service wrapper).
type UserRequestLimitDecision struct {
	Allowed           bool
	Window            userRequestLimitWindow
	Limit             *int64
	RetryAfterSeconds *int64
}

// UserRequestLimitDirtySnapshot mirrors UserRequestLimitDirtySnapshot.
type UserRequestLimitDirtySnapshot struct {
	EntryKey        string
	SystemAccountID string
	Window          userRequestLimitWindow
	Bucket          string
	LocalCount      int64
	RedisTTLms      int64
}

// UserRequestLimitSyncResult mirrors UserRequestLimitSyncResult.
type UserRequestLimitSyncResult struct {
	EntryKey       string
	SentLocalCount int64
	RemoteTotal    int64
}

// UserRequestLimitCounterOptions mirrors UserRequestLimitCounterOptions.
type UserRequestLimitCounterOptions struct {
	MaxEntries       *int
	CleanupStride    *int
	CleanupBatchSize *int
}

// UserRequestLimitCounterStats mirrors UserRequestLimitCounterStats.
type UserRequestLimitCounterStats struct {
	Entries           int
	DirtyEntries      int
	CapacityEvictions int64
}

// UserRequestLimitCounter mirrors the UserRequestLimitCounter class.
type UserRequestLimitCounter struct {
	mu                sync.Mutex
	clock             Clock
	entries           map[string]*userRequestLimitCounterEntry
	order             []string // insertion order (JS Map semantics)
	dirtyOrder        []string // dirty key set in insertion order
	dirtyIndexOf      map[string]int
	bucketSnapshots   map[string]userRequestLimitBucketSnapshot
	snapshotOrder     []string
	maxEntries        int
	cleanupStride     int
	cleanupBatchSize  int
	consumeCount      int64
	capacityEvictions int64
	cleanupPos        int
}

// NewUserRequestLimitCounter mirrors the constructor.
func NewUserRequestLimitCounter(clock Clock, options UserRequestLimitCounterOptions) *UserRequestLimitCounter {
	return &UserRequestLimitCounter{
		clock:            clock,
		entries:          map[string]*userRequestLimitCounterEntry{},
		dirtyIndexOf:     map[string]int{},
		bucketSnapshots:  map[string]userRequestLimitBucketSnapshot{},
		maxEntries:       positiveIntegerOption(options.MaxEntries, defaultUserRequestLimitMaxEntries),
		cleanupStride:    positiveIntegerOption(options.CleanupStride, defaultUserRequestLimitCleanupStride),
		cleanupBatchSize: positiveIntegerOption(options.CleanupBatchSize, defaultUserRequestLimitCleanupBatch),
	}
}

func positiveIntegerOption(value *int, fallback int) int {
	if value == nil || *value <= 0 {
		return fallback
	}
	return *value
}

func (c *UserRequestLimitCounter) nowMsOrDefault(nowMs *int64) int64 {
	if nowMs != nil {
		return *nowMs
	}
	return ClockNowMs(c.clock)
}

// Consume mirrors consume.
func (c *UserRequestLimitCounter) Consume(input UserRequestLimitConsumeInput) UserRequestLimitDecision {
	c.mu.Lock()
	defer c.mu.Unlock()
	nowMs := c.nowMsOrDefault(input.NowMs)
	timezone := input.Settings.UsageStatsTimezone
	if strings.TrimSpace(timezone) == "" {
		timezone = "UTC"
	}
	var buckets *userRequestLimitBucketSnapshot
	activeOverrides := input.Overrides
	if activeOverrides != nil && activeOverrides.ExpiresOn != nil && *activeOverrides.ExpiresOn != "" {
		snapshot := c.currentBucketsLocked(timezone, nowMs)
		buckets = &snapshot
		if snapshot.perDay.bucket <= *activeOverrides.ExpiresOn {
			// keep the overrides active
		} else {
			activeOverrides = nil
		}
	}
	limits := effectiveUserRequestLimits(input.Settings, activeOverrides)
	if limits[userRequestLimitWindowPerMinute] == 0 && limits[userRequestLimitWindowPerDay] == 0 &&
		limits[userRequestLimitWindowPerWeek] == 0 && limits[userRequestLimitWindowPerMonth] == 0 {
		return UserRequestLimitDecision{Allowed: true}
	}

	if buckets == nil {
		snapshot := c.currentBucketsLocked(timezone, nowMs)
		buckets = &snapshot
	}
	type pendingConsume struct {
		entry *userRequestLimitCounterEntry
		limit int64
	}
	var pending []pendingConsume
	var blockedDecision *UserRequestLimitDecision
	for _, window := range userRequestLimitWindowOrder {
		limit := limits[window]
		if limit == 0 {
			continue
		}
		bucket := bucketForWindow(buckets, window)
		entry := c.entryLocked(input.SystemAccountID, window, bucket, nowMs)
		if entry == nil {
			continue
		}
		unsyncedDelta := maxInt64(0, entry.localCount-entry.syncedLocalCount)
		estimatedTotal := maxInt64(entry.localCount, entry.remoteTotal+unsyncedDelta)
		if blockedDecision == nil && estimatedTotal+1 > limit {
			blocked := &UserRequestLimitDecision{
				Allowed: false,
				Window:  window,
				Limit:   int64Ptr(limit),
			}
			if window == userRequestLimitWindowPerMinute {
				retryAfterSeconds := maxInt64(1, ceilDiv(bucket.windowEndsAtMs-nowMs, 1000))
				blocked.RetryAfterSeconds = &retryAfterSeconds
			}
			blockedDecision = blocked
		}
		pending = append(pending, pendingConsume{entry: entry, limit: limit})
	}

	for _, item := range pending {
		nextLocalCount := item.entry.localCount + 1
		if blockedDecision != nil {
			nextLocalCount = minInt64(item.entry.localCount+1, item.limit+1)
		}
		if nextLocalCount > item.entry.localCount {
			item.entry.localCount = nextLocalCount
			item.entry.dirty = true
			c.addDirtyLocked(item.entry.key)
		}
	}
	c.maybeCleanupLocked(nowMs)
	if blockedDecision != nil {
		return *blockedDecision
	}
	return UserRequestLimitDecision{Allowed: true}
}

func bucketForWindow(snapshot *userRequestLimitBucketSnapshot, window userRequestLimitWindow) userRequestLimitBucketDefinition {
	switch window {
	case userRequestLimitWindowPerMinute:
		return snapshot.perMinute
	case userRequestLimitWindowPerDay:
		return snapshot.perDay
	case userRequestLimitWindowPerWeek:
		return snapshot.perWeek
	default:
		return snapshot.perMonth
	}
}

func ceilDiv(value, divisor int64) int64 {
	return int64(math.Ceil(float64(value) / float64(divisor)))
}

// DirtySnapshot mirrors dirtySnapshot: selected keys rotate to the tail so
// continuously hot buckets cannot starve later dirty entries.
func (c *UserRequestLimitCounter) DirtySnapshot(limit int) []UserRequestLimitDirtySnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	if limit <= 0 {
		limit = 512
	}
	output := make([]UserRequestLimitDirtySnapshot, 0)
	for _, key := range append([]string(nil), c.dirtyOrder...) {
		if len(output) >= limit {
			break
		}
		entry, ok := c.entries[key]
		if !ok || !entry.dirty {
			c.removeDirtyLocked(key)
			continue
		}
		output = append(output, UserRequestLimitDirtySnapshot{
			EntryKey:        entry.key,
			SystemAccountID: entry.systemAccountID,
			Window:          entry.window,
			Bucket:          entry.bucket,
			LocalCount:      entry.localCount,
			RedisTTLms:      entry.redisTTLms,
		})
		// Rotate selected keys.
		c.removeDirtyLocked(key)
		c.addDirtyLocked(key)
	}
	return output
}

// ApplySyncResults mirrors applySyncResults.
func (c *UserRequestLimitCounter) ApplySyncResults(results []UserRequestLimitSyncResult) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, result := range results {
		entry, ok := c.entries[result.EntryKey]
		if !ok {
			continue
		}
		entry.remoteTotal = maxInt64(entry.remoteTotal, result.RemoteTotal)
		entry.syncedLocalCount = maxInt64(entry.syncedLocalCount, result.SentLocalCount)
		entry.dirty = entry.localCount > entry.syncedLocalCount
		if !entry.dirty {
			c.removeDirtyLocked(entry.key)
		}
	}
}

// CleanupExpired mirrors cleanupExpired and returns the removed count.
func (c *UserRequestLimitCounter) CleanupExpired(nowMs *int64, limit *int) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	normalizedNow := c.nowMsOrDefault(nowMs)
	normalizedLimit := 2_048
	if limit != nil && *limit > 0 {
		normalizedLimit = *limit
	}
	return c.cleanupExpiredLocked(normalizedNow, normalizedLimit)
}

func (c *UserRequestLimitCounter) cleanupExpiredLocked(nowMs int64, limit int) int {
	removed := 0
	inspected := 0
	for inspected < limit {
		if c.cleanupPos >= len(c.order) {
			c.cleanupPos = 0
			break
		}
		key := c.order[c.cleanupPos]
		entry, ok := c.entries[key]
		if !ok {
			c.removeOrderLocked(c.cleanupPos)
			continue
		}
		if entry.expiresAtMs <= nowMs {
			delete(c.entries, key)
			c.removeDirtyLocked(key)
			c.removeOrderLocked(c.cleanupPos)
			removed++
			continue
		}
		c.cleanupPos++
		inspected++
	}
	return removed
}

// Size mirrors size.
func (c *UserRequestLimitCounter) Size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

// Stats mirrors stats.
func (c *UserRequestLimitCounter) Stats() UserRequestLimitCounterStats {
	c.mu.Lock()
	defer c.mu.Unlock()
	return UserRequestLimitCounterStats{
		Entries:           len(c.entries),
		DirtyEntries:      len(c.dirtyOrder),
		CapacityEvictions: c.capacityEvictions,
	}
}

// Reset mirrors reset.
func (c *UserRequestLimitCounter) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = map[string]*userRequestLimitCounterEntry{}
	c.order = nil
	c.dirtyOrder = nil
	c.dirtyIndexOf = map[string]int{}
	c.bucketSnapshots = map[string]userRequestLimitBucketSnapshot{}
	c.snapshotOrder = nil
	c.consumeCount = 0
	c.capacityEvictions = 0
	c.cleanupPos = 0
}

func (c *UserRequestLimitCounter) addDirtyLocked(key string) {
	if _, ok := c.dirtyIndexOf[key]; ok {
		return
	}
	c.dirtyIndexOf[key] = len(c.dirtyOrder)
	c.dirtyOrder = append(c.dirtyOrder, key)
}

func (c *UserRequestLimitCounter) removeDirtyLocked(key string) {
	index, ok := c.dirtyIndexOf[key]
	if !ok {
		return
	}
	c.dirtyOrder = append(c.dirtyOrder[:index], c.dirtyOrder[index+1:]...)
	delete(c.dirtyIndexOf, key)
	for i := index; i < len(c.dirtyOrder); i++ {
		c.dirtyIndexOf[c.dirtyOrder[i]] = i
	}
}

func (c *UserRequestLimitCounter) removeOrderLocked(index int) {
	c.order = append(c.order[:index], c.order[index+1:]...)
}

// entryLocked mirrors the private entry() factory. A JS Map keeps the
// original insertion position on re-set, so an expired entry is recreated in
// place instead of being appended again.
func (c *UserRequestLimitCounter) entryLocked(systemAccountID string, window userRequestLimitWindow, bucket userRequestLimitBucketDefinition, nowMs int64) *userRequestLimitCounterEntry {
	key := systemAccountID + userRequestLimitKeySeparator + string(window) + userRequestLimitKeySeparator + bucket.bucket
	if existing, ok := c.entries[key]; ok {
		if existing.expiresAtMs > nowMs {
			return existing
		}
		existing.localCount = 0
		existing.syncedLocalCount = 0
		existing.remoteTotal = 0
		existing.redisTTLms = bucket.redisTTLms
		existing.expiresAtMs = bucket.expiresAtMs
		existing.dirty = false
		return existing
	}
	if len(c.entries) >= c.maxEntries {
		c.cleanupExpiredLocked(nowMs, c.cleanupBatchSize)
	}
	if len(c.entries) >= c.maxEntries {
		if len(c.order) > 0 {
			oldestKey := c.order[0]
			delete(c.entries, oldestKey)
			c.removeDirtyLocked(oldestKey)
			c.removeOrderLocked(0)
			c.capacityEvictions++
		}
	}
	entry := &userRequestLimitCounterEntry{
		key:              key,
		systemAccountID:  systemAccountID,
		window:           window,
		bucket:           bucket.bucket,
		localCount:       0,
		syncedLocalCount: 0,
		remoteTotal:      0,
		redisTTLms:       bucket.redisTTLms,
		expiresAtMs:      bucket.expiresAtMs,
		dirty:            false,
	}
	c.entries[key] = entry
	c.order = append(c.order, key)
	return entry
}

// currentBucketsLocked mirrors currentBuckets with the timezone formatter
// cache folded into the snapshot cache.
func (c *UserRequestLimitCounter) currentBucketsLocked(timezone string, nowMs int64) userRequestLimitBucketSnapshot {
	epochMinute := int64(math.Floor(float64(nowMs) / float64(userRequestLimitMinuteMs)))
	if cached, ok := c.bucketSnapshots[timezone]; ok && cached.epochMinute == epochMinute {
		return cached
	}

	location, err := time.LoadLocation(timezone)
	if err != nil {
		// Node throws a RangeError from Intl for unknown time zones; the Go
		// port signature cannot propagate that, so UTC is the documented
		// fallback (settings projection already defaults to 'UTC').
		location = time.UTC
	}
	local := time.UnixMilli(nowMs).In(location)
	year, month, day := local.Date()
	localDayEpoch := time.Date(year, month, day, 0, 0, 0, 0, time.UTC).UnixMilli()
	utcWeekday := int(time.UnixMilli(localDayEpoch).UTC().Weekday())
	mondayEpoch := localDayEpoch - int64((utcWeekday+6)%7)*userRequestLimitDayMs
	monday := time.UnixMilli(mondayEpoch).UTC()

	snapshot := userRequestLimitBucketSnapshot{
		epochMinute: epochMinute,
		perMinute: userRequestLimitBucketDefinition{
			bucket:         formatInt64(epochMinute),
			windowEndsAtMs: (epochMinute + 1) * userRequestLimitMinuteMs,
			expiresAtMs:    nowMs + 2*userRequestLimitMinuteMs,
			redisTTLms:     2 * userRequestLimitMinuteMs,
		},
		perDay: userRequestLimitBucketDefinition{
			bucket:         formatDateParts(year, int(month), day),
			windowEndsAtMs: nowMs + userRequestLimitDayMs,
			expiresAtMs:    nowMs + 2*userRequestLimitDayMs,
			redisTTLms:     2 * userRequestLimitDayMs,
		},
		perWeek: userRequestLimitBucketDefinition{
			bucket:         formatDateParts(monday.Year(), int(monday.Month()), monday.Day()),
			windowEndsAtMs: nowMs + 7*userRequestLimitDayMs,
			expiresAtMs:    nowMs + 9*userRequestLimitDayMs,
			redisTTLms:     9 * userRequestLimitDayMs,
		},
		perMonth: userRequestLimitBucketDefinition{
			bucket:         formatYearMonth(year, int(month)),
			windowEndsAtMs: nowMs + 31*userRequestLimitDayMs,
			expiresAtMs:    nowMs + 35*userRequestLimitDayMs,
			redisTTLms:     35 * userRequestLimitDayMs,
		},
	}
	c.bucketSnapshots[timezone] = snapshot
	c.snapshotOrder = append(c.snapshotOrder, timezone)
	if len(c.snapshotOrder) > 32 {
		oldest := c.snapshotOrder[0]
		c.snapshotOrder = c.snapshotOrder[1:]
		delete(c.bucketSnapshots, oldest)
	}
	return snapshot
}

func formatYearMonth(year, month int) string {
	return pad2(year, 4) + "-" + pad2(month, 2)
}

func formatInt64(value int64) string {
	return strings.TrimSpace(itoa(value))
}

func itoa(value int64) string {
	if value == 0 {
		return "0"
	}
	negative := value < 0
	if negative {
		value = -value
	}
	digits := make([]byte, 0, 20)
	for value > 0 {
		digits = append(digits, byte('0'+value%10))
		value /= 10
	}
	if negative {
		digits = append(digits, '-')
	}
	for i, j := 0, len(digits)-1; i < j; i, j = i+1, j-1 {
		digits[i], digits[j] = digits[j], digits[i]
	}
	return string(digits)
}

func formatDateParts(year, month, day int) string {
	return pad2(year, 4) + "-" + pad2(month, 2) + "-" + pad2(day, 2)
}

func pad2(value, width int) string {
	digits := itoa(int64(value))
	for len(digits) < width {
		digits = "0" + digits
	}
	return digits
}

// maybeCleanupLocked mirrors maybeCleanup.
func (c *UserRequestLimitCounter) maybeCleanupLocked(nowMs int64) {
	c.consumeCount++
	if c.consumeCount%int64(c.cleanupStride) != 0 {
		return
	}
	inspected := 0
	i := 0
	for i < len(c.order) && inspected < c.cleanupBatchSize {
		key := c.order[i]
		entry, ok := c.entries[key]
		if ok && entry.expiresAtMs <= nowMs {
			delete(c.entries, key)
			c.removeDirtyLocked(key)
			c.removeOrderLocked(i)
			inspected++
			continue
		}
		inspected++
		i++
	}
}

// effectiveUserRequestLimits mirrors effectiveLimits: overrides win over
// settings, zero means unlimited.
func effectiveUserRequestLimits(settings gatewayruntimecache.GatewaySettings, overrides *gatewayruntimecache.UserRequestLimits) map[userRequestLimitWindow]int64 {
	pick := func(override *int64, setting *int64) int64 {
		if override != nil {
			return *override
		}
		if setting != nil {
			return *setting
		}
		return 0
	}
	if overrides == nil {
		overrides = &gatewayruntimecache.UserRequestLimits{}
	}
	return map[userRequestLimitWindow]int64{
		userRequestLimitWindowPerMinute: pick(overrides.PerMinute, settings.GatewayUserRequestLimitPerMinute),
		userRequestLimitWindowPerDay:    pick(overrides.PerDay, settings.GatewayUserRequestLimitPerDay),
		userRequestLimitWindowPerWeek:   pick(overrides.PerWeek, settings.GatewayUserRequestLimitPerWeek),
		userRequestLimitWindowPerMonth:  pick(overrides.PerMonth, settings.GatewayUserRequestLimitPerMonth),
	}
}
