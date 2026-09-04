package gatewayproxyhealth

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

func iptr(v int64) *int64 { return &v }

func iptrInt(v int) *int { return &v }

func settingsLimits(perMinute, perDay, perWeek, perMonth *int64) gatewayruntimecache.GatewaySettings {
	return gatewayruntimecache.GatewaySettings{
		GatewayUserRequestLimitPerMinute: perMinute,
		GatewayUserRequestLimitPerDay:    perDay,
		GatewayUserRequestLimitPerWeek:   perWeek,
		GatewayUserRequestLimitPerMonth:  perMonth,
		UsageStatsTimezone:               "UTC",
	}
}

func overridesLimits(perMinute, perDay, perWeek, perMonth *int64, expiresOn *string) *gatewayruntimecache.UserRequestLimits {
	return &gatewayruntimecache.UserRequestLimits{
		PerMinute: perMinute,
		PerDay:    perDay,
		PerWeek:   perWeek,
		PerMonth:  perMonth,
		ExpiresOn: expiresOn,
	}
}

func TestUserRequestLimitConsumeBlocksAtExactBoundary(t *testing.T) {
	clock := newFakeClock(1_770_000_000_000)
	counter := NewUserRequestLimitCounter(clock.Now, UserRequestLimitCounterOptions{})
	settings := settingsLimits(iptr(5), nil, nil, nil)

	for i := 0; i < 5; i++ {
		decision := counter.Consume(UserRequestLimitConsumeInput{SystemAccountID: "u1", Settings: settings})
		if !decision.Allowed {
			t.Fatalf("request %d must pass: %+v", i, decision)
		}
	}
	// The sixth request hits the exact boundary.
	decision := counter.Consume(UserRequestLimitConsumeInput{SystemAccountID: "u1", Settings: settings})
	if decision.Allowed || decision.Window != userRequestLimitWindowPerMinute || decision.Limit == nil || *decision.Limit != 5 {
		t.Fatalf("blocked decision = %+v", decision)
	}
	if decision.RetryAfterSeconds == nil || *decision.RetryAfterSeconds != 60 {
		t.Fatalf("retryAfterSeconds = %v", decision.RetryAfterSeconds)
	}
	// Blocked requests still count up to limit+1, never more.
	for i := 0; i < 3; i++ {
		counter.Consume(UserRequestLimitConsumeInput{SystemAccountID: "u1", Settings: settings})
	}
	if stats := counter.Stats(); stats.Entries != 1 {
		t.Fatalf("entries = %d", stats.Entries)
	}
	// The minute bucket rolls over and allows again.
	clock.Advance(61_000)
	decision = counter.Consume(UserRequestLimitConsumeInput{SystemAccountID: "u1", Settings: settings})
	if !decision.Allowed {
		t.Fatalf("new minute bucket must allow: %+v", decision)
	}
}

func TestUserRequestLimitRetryAfterCeil(t *testing.T) {
	clock := newFakeClock(1_770_000_000_050)
	counter := NewUserRequestLimitCounter(clock.Now, UserRequestLimitCounterOptions{})
	settings := settingsLimits(iptr(2), nil, nil, nil)
	counter.Consume(UserRequestLimitConsumeInput{SystemAccountID: "u1", Settings: settings})
	counter.Consume(UserRequestLimitConsumeInput{SystemAccountID: "u1", Settings: settings})
	// 50ms into the minute: retry-after rounds up to 60.
	decision := counter.Consume(UserRequestLimitConsumeInput{SystemAccountID: "u1", Settings: settings})
	if decision.RetryAfterSeconds == nil || *decision.RetryAfterSeconds != 60 {
		t.Fatalf("retryAfterSeconds = %v", decision.RetryAfterSeconds)
	}
	clock.Advance(59_000)
	// 950ms before the window ends: rounds up to 1.
	decision = counter.Consume(UserRequestLimitConsumeInput{SystemAccountID: "u1", Settings: settings})
	if decision.RetryAfterSeconds == nil || *decision.RetryAfterSeconds != 1 {
		t.Fatalf("late retryAfterSeconds = %v", decision.RetryAfterSeconds)
	}
}

func TestUserRequestLimitWindowsIndependent(t *testing.T) {
	clock := newFakeClock(1_770_000_000_000)
	counter := NewUserRequestLimitCounter(clock.Now, UserRequestLimitCounterOptions{})
	settings := settingsLimits(iptr(2), iptr(3), nil, nil)
	for i := 0; i < 2; i++ {
		if decision := counter.Consume(UserRequestLimitConsumeInput{SystemAccountID: "u1", Settings: settings}); !decision.Allowed {
			t.Fatalf("request %d blocked: %+v", i, decision)
		}
	}
	// Third request: the minute bucket rolled (allowed again) and lands
	// exactly on the day limit.
	clock.Advance(61_000)
	decision := counter.Consume(UserRequestLimitConsumeInput{SystemAccountID: "u1", Settings: settings})
	if !decision.Allowed {
		t.Fatalf("day boundary request must pass: %+v", decision)
	}
	// The next minute the day bucket blocks.
	clock.Advance(61_000)
	decision = counter.Consume(UserRequestLimitConsumeInput{SystemAccountID: "u1", Settings: settings})
	if decision.Allowed || decision.Window != userRequestLimitWindowPerDay {
		t.Fatalf("day block decision = %+v", decision)
	}
	// Non-minute blocks carry no retryAfterSeconds (Node leaves it undefined).
	if decision.RetryAfterSeconds != nil {
		t.Fatalf("perDay retryAfterSeconds = %v", decision.RetryAfterSeconds)
	}
}

func TestUserRequestLimitOverrides(t *testing.T) {
	clock := newFakeClock(1_769_991_040_000) // 2026-02-02T00:00:00Z? see asserts below
	counter := NewUserRequestLimitCounter(clock.Now, UserRequestLimitCounterOptions{})
	zeroSettings := settingsLimits(nil, nil, nil, nil)
	// Global limits are zero: without overrides everything is allowed.
	if decision := counter.Consume(UserRequestLimitConsumeInput{SystemAccountID: "u1", Settings: zeroSettings}); !decision.Allowed {
		t.Fatalf("zero settings must allow: %+v", decision)
	}
	// A per-minute override applies even with zero global settings.
	if decision := counter.Consume(UserRequestLimitConsumeInput{SystemAccountID: "u1", Settings: zeroSettings, Overrides: overridesLimits(iptr(1), nil, nil, nil, nil)}); !decision.Allowed {
		t.Fatalf("override first request: %+v", decision)
	}
	decision := counter.Consume(UserRequestLimitConsumeInput{SystemAccountID: "u1", Settings: zeroSettings, Overrides: overridesLimits(iptr(1), nil, nil, nil, nil)})
	if decision.Allowed || decision.Window != userRequestLimitWindowPerMinute || decision.Limit == nil || *decision.Limit != 1 {
		t.Fatalf("override block: %+v", decision)
	}

	// expiresOn: active while the local day bucket is <= the expiry date.
	// 2026-02-03T00:00:00Z in UTC.
	expiry := "2026-02-03"
	clock.Set(1_769_999_040_000)
	dayInput := UserRequestLimitConsumeInput{SystemAccountID: "u2", Settings: zeroSettings, Overrides: overridesLimits(iptr(1), nil, nil, nil, &expiry)}
	if active := counter.Consume(dayInput); !active.Allowed {
		t.Fatalf("expiry-day request must be allowed: %+v", active)
	}
	if blocked := counter.Consume(dayInput); blocked.Allowed {
		t.Fatalf("second expiry-day request must be blocked: %+v", blocked)
	}
	// The next day the override expires.
	clock.Set(1_770_007_680_000) // 2026-02-04T00:00:00Z
	if after := counter.Consume(dayInput); !after.Allowed {
		t.Fatalf("expired override must allow: %+v", after)
	}
}

func TestUserRequestLimitBucketShapes(t *testing.T) {
	// 2026-02-11T04:16:00.123Z (Wednesday) via civil-date construction.
	now := time.Date(2026, 2, 11, 4, 16, 0, 123_000_000, time.UTC).UnixMilli()
	clock := newFakeClock(now)
	counter := NewUserRequestLimitCounter(clock.Now, UserRequestLimitCounterOptions{})
	settings := settingsLimits(iptr(100), iptr(100), iptr(100), iptr(100))
	counter.Consume(UserRequestLimitConsumeInput{SystemAccountID: "u1", Settings: settings})

	snapshot := counter.currentBucketsLocked("UTC", now)
	if snapshot.perMinute.bucket != "29513056" { // floor(epochMs/60000) for 2026-02-11T04:16:00.123Z
		t.Fatalf("perMinute bucket = %q", snapshot.perMinute.bucket)
	}
	if snapshot.perDay.bucket != "2026-02-11" {
		t.Fatalf("perDay bucket = %q", snapshot.perDay.bucket)
	}
	// 2026-02-11 is a Wednesday; the week bucket is Monday 2026-02-09.
	if snapshot.perWeek.bucket != "2026-02-09" {
		t.Fatalf("perWeek bucket = %q", snapshot.perWeek.bucket)
	}
	if snapshot.perMonth.bucket != "2026-02" {
		t.Fatalf("perMonth bucket = %q", snapshot.perMonth.bucket)
	}

	// Timezone shift: 04:16Z is 12:16 in Asia/Shanghai, same Monday-based week.
	shanghai := counter.currentBucketsLocked("Asia/Shanghai", now)
	if shanghai.perDay.bucket != "2026-02-11" || shanghai.perWeek.bucket != "2026-02-09" {
		t.Fatalf("shanghai buckets = %q / %q", shanghai.perDay.bucket, shanghai.perWeek.bucket)
	}

	// 2026-02-11T16:00:00.500Z: UTC is still Feb 11 while Shanghai (UTC+8)
	// already rolled to Feb 12.
	lateUTC := time.Date(2026, 2, 11, 16, 0, 0, 500_000_000, time.UTC).UnixMilli()
	shanghaiLate := counter.currentBucketsLocked("Asia/Shanghai", lateUTC)
	if shanghaiLate.perDay.bucket != "2026-02-12" {
		t.Fatalf("shanghai late perDay bucket = %q", shanghaiLate.perDay.bucket)
	}
	utcLate := counter.currentBucketsLocked("UTC", lateUTC)
	if utcLate.perDay.bucket != "2026-02-11" || utcLate.perWeek.bucket != "2026-02-09" {
		t.Fatalf("utc late buckets = %q / %q", utcLate.perDay.bucket, utcLate.perWeek.bucket)
	}

	// Invalid timezone falls back to UTC (Node throws a RangeError; the Go
	// consume port cannot return an error so the fallback is documented).
	fallback := counter.currentBucketsLocked("Not/AZone", lateUTC)
	if fallback.perDay.bucket != "2026-02-11" {
		t.Fatalf("fallback bucket = %q", fallback.perDay.bucket)
	}
}

func TestUserRequestLimitDirtySnapshotAndSync(t *testing.T) {
	clock := newFakeClock(1_770_000_000_000)
	counter := NewUserRequestLimitCounter(clock.Now, UserRequestLimitCounterOptions{})
	settings := settingsLimits(iptr(10), nil, nil, nil)
	counter.Consume(UserRequestLimitConsumeInput{SystemAccountID: "u1", Settings: settings})
	counter.Consume(UserRequestLimitConsumeInput{SystemAccountID: "u2", Settings: settings})

	batch := counter.DirtySnapshot(64)
	if len(batch) != 2 {
		t.Fatalf("batch = %+v", batch)
	}
	first := batch[0]
	if first.Window != userRequestLimitWindowPerMinute || first.LocalCount != 1 || first.RedisTTLms != 120_000 {
		t.Fatalf("snapshot entry = %+v", first)
	}
	if !strings.Contains(first.EntryKey, "\x1fperMinute\x1f") {
		t.Fatalf("entry key = %q", first.EntryKey)
	}

	// Applying results clears the dirty flag and stores the remote total.
	counter.ApplySyncResults([]UserRequestLimitSyncResult{{
		EntryKey:       first.EntryKey,
		SentLocalCount: first.LocalCount,
		RemoteTotal:    42,
	}})
	if got := counter.DirtySnapshot(64); len(got) != 1 {
		t.Fatalf("dirty after sync = %+v", got)
	}
}

func TestUserRequestLimitCleanupAndCapacity(t *testing.T) {
	clock := newFakeClock(1_770_000_000_000)
	maxEntries := 8
	batch := 4
	counter := NewUserRequestLimitCounter(clock.Now, UserRequestLimitCounterOptions{
		MaxEntries:       &maxEntries,
		CleanupBatchSize: &batch,
	})
	overrides := overridesLimits(iptr(100), nil, nil, nil, nil)
	for i := 0; i < 12; i++ {
		counter.Consume(UserRequestLimitConsumeInput{SystemAccountID: "user-" + itoaForTest(int64(i)), Settings: settingsLimits(nil, nil, nil, nil), Overrides: overrides})
	}
	if counter.Size() > maxEntries {
		t.Fatalf("size %d exceeds max %d", counter.Size(), maxEntries)
	}
	if counter.Stats().CapacityEvictions == 0 {
		t.Fatal("capacity evictions must be recorded")
	}

	// Expire everything, then let the resumable cleanup walk remove the rest.
	clock.Advance(10 * 60_000)
	removed := 0
	for i := 0; i < 4; i++ {
		removed += counter.CleanupExpired(nil, iptrInt(4))
	}
	if removed == 0 || counter.Size() != 0 {
		t.Fatalf("cleanup removed=%d size=%d", removed, counter.Size())
	}
}

func TestUserRequestLimitConcurrentConsume(t *testing.T) {
	clock := newFakeClock(1_770_000_000_000)
	counter := NewUserRequestLimitCounter(clock.Now, UserRequestLimitCounterOptions{})
	settings := settingsLimits(iptr(1000), nil, nil, nil)
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			counter.Consume(UserRequestLimitConsumeInput{SystemAccountID: "u1", Settings: settings})
			counter.DirtySnapshot(4)
			counter.Stats()
		}()
	}
	wg.Wait()
	if counter.Size() != 1 {
		t.Fatalf("size = %d", counter.Size())
	}
}

func TestUserRequestLimitsServicePortMapping(t *testing.T) {
	clock := newFakeClock(1_770_000_000_000)
	counter := NewUserRequestLimitCounter(clock.Now, UserRequestLimitCounterOptions{})
	coordinator := NewUserRequestLimitCoordinator(counter, clock.Now, UserRequestLimitCoordinatorOptions{RedisEnabled: false, Namespace: "juhe-ai"})
	service := NewUserRequestLimitsService(counter, coordinator)

	settings := gatewayruntimecache.GatewaySettings{GatewayUserRequestLimitPerMinute: iptr(1), UsageStatsTimezone: "UTC"}
	decision := service.Consume(gatewaypreauth.UserRequestLimitConsumeInput{SystemAccountID: "u1", Settings: settings})
	if !decision.Allowed {
		t.Fatalf("first decision = %+v", decision)
	}
	decision = service.Consume(gatewaypreauth.UserRequestLimitConsumeInput{SystemAccountID: "u1", Settings: settings})
	if decision.Allowed || string(decision.Window) != "perMinute" || decision.Limit == nil || *decision.Limit != 1 {
		t.Fatalf("blocked port decision = %+v", decision)
	}
	// StartCoordinator on the redis-disabled coordinator is a safe no-op.
	service.StartCoordinator()
}
