package statsverify

import (
)

// Golden derivations follow the Node chain
// aggregateClientIpStatsBatchAsync -> writeClientIpStatsAggregatesFromUsageRowsAsync:
//   - dimension key ipHash:statDate with statDate = dateKey(created_at, tz);
//   - eligible rows: created_at <= now-15s, traffic_source NOT IN
//     ('runtime_recovery_probe','cooldown_retest');
//   - daily UPSERT accumulates every metric and keeps MAX(duration_ms_max);
//   - registry keeps MIN(first_seen_at) / MAX(last_seen_at);
//   - the job-state cursor lands on the last processed (created_at, id).

