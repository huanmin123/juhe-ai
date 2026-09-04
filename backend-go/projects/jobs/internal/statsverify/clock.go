// Package statsverify migrates the three J5 statistics jobs owned by the
// Node stats-worker that are not covered by the usage aggregation/window
// work package:
//
//	client-ip-stats-aggregation  (aggregateClientIpStatsBatch + client-ip range windows)
//	group-account-stats-refresh  (refreshDirtyGroupAccountStatsCache*)
//	usage-stats-consistency-check (checkUsageStatsConsistency*)
//
// Every behavioural rule mirrors the Node implementation under
// backend/src/storage/{client-ip-stats-aggregation,client-ip-stats-writer,
// client-ip-usage-range-windows,client-ip-normalization,group-account-stats-cache,
// usage-stats-helpers,usage-stats-aggregation}.repository.ts and
// backend/src/modules/background/{background-jobs,background-stats-writer}.ts.
// Deviations are listed in the package-level comment of jobs.go.
package statsverify

import "time"

// Clock is the injected time boundary. Jobs never call time.Now directly so
// golden tests can replay day/month/timezone boundaries deterministically.
type Clock interface {
	Now() time.Time
	// Sleep mirrors the 25ms pause between aggregation batches in
	// background-stats-writer.ts (statsAggregationBatchPauseMs). Tests use a
	// no-op implementation.
	Sleep(d time.Duration)
}

// SystemClock is the production clock. Instants are formatted the same way
// Node's nowIso() (Date.prototype.toISOString) formats them: UTC RFC3339 with
// exactly three millisecond digits and a trailing "Z".
type SystemClock struct{}

func (SystemClock) Now() time.Time        { return time.Now() }
func (SystemClock) Sleep(d time.Duration) { time.Sleep(d) }

// FixedClock replays a deterministic instant sequence: Now returns the last
// value until Advance is called. Sleep only advances the clock.
type FixedClock struct {
	Current time.Time
}

func NewFixedClock(t time.Time) *FixedClock {
	return &FixedClock{Current: t}
}

func (c *FixedClock) Now() time.Time {
	if c == nil {
		return time.Time{}
	}
	return c.Current
}

func (c *FixedClock) Sleep(d time.Duration) {
	if c == nil {
		return
	}
	c.Current = c.Current.Add(d)
}

// NowIso formats t the way Node nowIso() formats Date.now():
// new Date().toISOString() => "2006-01-02T15:04:05.000Z".
func NowIso(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z07:00")
}
