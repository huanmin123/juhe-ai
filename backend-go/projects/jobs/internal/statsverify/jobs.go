package statsverify

import (
	"context"
	"log/slog"
	"time"
)

// Job orchestration mirroring the scheduler entry points
// runClientIpStatsAggregation / runGroupAccountStatsRefresh /
// runUsageStatsConsistencyCheck (background-jobs.ts) and their stats-writer
// handlers aggregateClientIpStats / refreshGroupAccountStats /
// handleStatsWriteOperation (background-stats-writer.ts).
//
// Known, deliberate deviations from the Node runtime (each inherited from
// the Go jobs architecture, not from behavioural drift):
//   - ingest-worker drain safety (ensureUsageRecordsSafeForStatsAggregation)
//     is a scheduler-side IPC concern; Go hosts gate the call themselves.
//   - PostgreSQL fencing leases are pinned by the jobs supervisor, not
//     inside this package.
//   - Node's SQLite driver scans usage-record shard files; the Go SQLite
//     layout keeps one local usage_records table and therefore one global
//     cursor (same semantics as Node's PostgreSQL path).
//   - the consistency job only detects and reports: Node has no repair
//     write-back either.

const (
	// clientIpStatsAggregationBatchSizeCap mirrors
	// clientIpStatsAggregationBatchSizeCap (background-jobs.ts line 93).
	ClientIPStatsAggregationBatchSizeCap = 1000
	// clientIpStatsAggregationMaxBatchesCap mirrors
	// clientIpStatsAggregationMaxBatchesCap (background-jobs.ts line 94).
	ClientIPStatsAggregationMaxBatchesCap = 10
	// ClientIPStatsAggregationMaxRunMs mirrors
	// clientIpStatsAggregationMaxRunMs (background-jobs.ts line 95).
	ClientIPStatsAggregationMaxRunMs = 5000
	// statsAggregationBatchPauseMs mirrors
	// statsAggregationBatchPauseMs (background-stats-writer.ts line 78).
	StatsAggregationBatchPauseMs = 25 * time.Millisecond

	// Settings bounds mirrored from the scheduler calls
	// settingsNumber('statsAggregationBatchSize', 100, 10000) etc.
	StatsAggregationBatchSizeMin   = 100
	StatsAggregationBatchSizeMax   = 10000
	StatsAggregationBatchSizeDflt  = 2000
	StatsAggregationMaxBatchesMin  = 1
	StatsAggregationMaxBatchesMax  = 100
	StatsAggregationMaxBatchesDflt = 5
	// GroupAccountStatsRefreshLimit mirrors refreshGroupAccountStats(limit)
	// from runGroupAccountStatsRefresh -> refreshGroupAccountStats.
	GroupAccountStatsRefreshLimit = 1000
	// UsageStatsConsistencyCheckLimit mirrors
	// runUsageStatsConsistencyCheck's requestStatsWriter limit: 20.
	UsageStatsConsistencyCheckLimit = 20
)

// SettingsNumber is the settings boundary the host plugs in (mirrors
// settingsNumber(key, min, max): integer values inside the bounds). The
// default path applies the DEFAULT_SYSTEM_SETTINGS value from
// schema-defaults.ts.
type SettingsNumber func(key string, min, max int) (int, error)

// RunClientIPStatsAggregationOptions carries the host-provided settings and
// clock. Interval scheduling and lease ownership stay with the jobs
// supervisor.
type RunClientIPStatsAggregationOptions struct {
	Clock Clock
	// StatsAggregationBatchSize resolves system setting
	// 'statsAggregationBatchSize' (bounded 100..10000).
	StatsAggregationBatchSize int
	// StatsAggregationMaxBatchesPerRun resolves system setting
	// 'statsAggregationMaxBatchesPerRun' (bounded 1..100).
	StatsAggregationMaxBatchesPerRun int
}

// ClientIPStatsAggregationResult mirrors the stats-writer result
// { processed } plus the batch loop bookkeeping Node keeps locally.
type ClientIPStatsAggregationResult struct {
	Processed int
	Batches   int
}

// RunClientIPStatsAggregation mirrors aggregateClientIpStats
// (background-stats-writer.ts lines 393-408): the caller-provided batch size
// is capped at 1000 and the batch count at 10 (scheduler-side caps); the
// loop stops when a batch returns fewer rows than requested or the 5s time
// budget is exhausted; the range-window refresh always runs at the end.
func (s *Store) RunClientIPStatsAggregation(ctx context.Context, options RunClientIPStatsAggregationOptions) (ClientIPStatsAggregationResult, error) {
	clock := options.Clock
	if clock == nil {
		clock = SystemClock{}
	}
	batchSize := boundPositiveInt(options.StatsAggregationBatchSize, 1, 10000)
	if batchSize > ClientIPStatsAggregationBatchSizeCap {
		batchSize = ClientIPStatsAggregationBatchSizeCap
	}
	maxBatches := boundPositiveInt(options.StatsAggregationMaxBatchesPerRun, 1, 100)
	if maxBatches > ClientIPStatsAggregationMaxBatchesCap {
		maxBatches = ClientIPStatsAggregationMaxBatchesCap
	}

	result := ClientIPStatsAggregationResult{}
	startedAt := clock.Now()
	for index := 0; index < maxBatches; index++ {
		processed, err := s.AggregateClientIPStatsBatch(ctx, batchSize, clock.Now())
		if err != nil {
			return result, err
		}
		result.Processed += processed
		result.Batches++
		if processed < batchSize {
			break
		}
		if clock.Now().Sub(startedAt) >= ClientIPStatsAggregationMaxRunMs*time.Millisecond {
			break
		}
		clock.Sleep(StatsAggregationBatchPauseMs)
	}
	if err := s.RefreshClientIPUsageRangeWindows(ctx, ClientIPRangeWindowRefreshOptions{Now: clock.Now()}); err != nil {
		return result, err
	}
	return result, nil
}

// RunGroupAccountStatsRefresh mirrors runGroupAccountStatsRefresh ->
// refreshGroupAccountStats: PostgreSQL refreshes inside one leased
// transaction; SQLite consumes dirty rows across the business/stats
// databases. startupDirtyMark mirrors the one-shot
// mark_all_group_account_stats_dirty the scheduler performs on its first
// PostgreSQL round.
func (s *Store) RunGroupAccountStatsRefresh(ctx context.Context, now time.Time) (int, error) {
	return s.RefreshDirtyGroupAccountStats(ctx, GroupAccountStatsRefreshOptions{Limit: GroupAccountStatsRefreshLimit, Now: now})
}

// MarkGroupAccountStatsStartupDirty mirrors the scheduler's one-time
// requestBackgroundWorkerDbService({type:'mark_all_group_account_stats_dirty',
// reason:'stats_worker_startup_refresh'}).
func (s *Store) MarkGroupAccountStatsStartupDirty(ctx context.Context, now time.Time) error {
	return s.MarkAllGroupAccountStatsDirty(ctx, "stats_worker_startup_refresh", now)
}

// RunUsageStatsConsistencyCheck mirrors runUsageStatsConsistencyCheck:
// sample 20 daily buckets, compare against their hourly sums, and surface
// the issues (the scheduler logs them at warn level). No rows are written.
func (s *Store) RunUsageStatsConsistencyCheck(ctx context.Context, now time.Time, logger *slog.Logger) ([]UsageStatsConsistencyIssue, error) {
	issues, err := s.CheckUsageStatsConsistency(ctx, UsageStatsConsistencyOptions{
		SampleLimit: UsageStatsConsistencyCheckLimit,
		Now:         now,
	})
	if err != nil {
		return nil, err
	}
	if len(issues) > 0 && logger != nil {
		logger.Warn("usage_stats_consistency_mismatch",
			"issueCount", len(issues),
			"issues", issues[:minInt(len(issues), 20)],
		)
	}
	return issues, nil
}

// boundPositiveInt mirrors boundedPositiveInteger
// (background-stats-writer.ts lines 479-482): truncation and clamping with a
// minimum fallback; Node falls back to `min` for non-finite input, which
// cannot occur for an int.
func boundPositiveInt(value, min, max int) int {
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}
