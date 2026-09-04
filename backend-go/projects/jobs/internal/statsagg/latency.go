package statsagg

import "math"

// LatencyMetricType mirrors usage-stats-latency-writer.ts LatencyMetricType。
type LatencyMetricType string

const (
	LatencyMetricDurationMs   LatencyMetricType = "duration_ms"
	LatencyMetricFirstTokenMs LatencyMetricType = "first_token_ms"
)

// LatencyBucketUpperBoundsMs mirrors latencyBucketUpperBoundsMs。
var LatencyBucketUpperBoundsMs = []int{100, 250, 500, 1000, 2000, 5000, 10000, 30000, 60000, -1}

// AggregatedLatencyEntry mirrors AggregatedLatencyEntry。
type AggregatedLatencyEntry struct {
	Bucket             TimeBucketDefinition
	SystemAccountID    string
	ScopeType          string
	ScopeID            string
	MetricType         LatencyMetricType
	TimeValue          string
	BucketUpperBoundMs int
	SampleCount        float64
}

// LatencyBucketUpperBound mirrors latencyBucketUpperBound。
func LatencyBucketUpperBound(value float64) int {
	for _, upperBound := range LatencyBucketUpperBoundsMs {
		if upperBound == -1 || value <= float64(upperBound) {
			return upperBound
		}
	}
	return -1
}

// latencySamples mirrors latencySamples：duration_ms 与 first_token_ms 各产生
// 一个样本（有限且非负才记录）。
func latencySamples(row UsageStatsRecordRow) []struct {
	MetricType         LatencyMetricType
	BucketUpperBoundMs int
} {
	var samples []struct {
		MetricType         LatencyMetricType
		BucketUpperBoundMs int
	}
	if durationMs, ok := finiteNonNegativeNumber(row.DurationMs); ok {
		samples = append(samples, struct {
			MetricType         LatencyMetricType
			BucketUpperBoundMs int
		}{LatencyMetricDurationMs, LatencyBucketUpperBound(durationMs)})
	}
	if firstTokenMs, ok := finiteNonNegativeNumber(row.FirstTokenMs); ok {
		samples = append(samples, struct {
			MetricType         LatencyMetricType
			BucketUpperBoundMs int
		}{LatencyMetricFirstTokenMs, LatencyBucketUpperBound(firstTokenMs)})
	}
	return samples
}

func finiteNonNegativeNumber(value *float64) (float64, bool) {
	if value == nil {
		return 0, false
	}
	number := *value
	if !isNaN(number) && number >= 0 {
		return number, true
	}
	return 0, false
}

func isNaN(value float64) bool {
	return value != value || math.IsInf(value, 0)
}

type latencyEntryKey struct {
	tableName  string
	timeValue  string
	systemID   string
	scopeType  string
	scopeID    string
	metricType LatencyMetricType
	upperBound int
}

// AddAggregatedLatencyEntries mirrors addAggregatedLatencyEntries。
func AddAggregatedLatencyEntries(target map[latencyEntryKey]*AggregatedLatencyEntry, entry UsageStatsEntry, row UsageStatsRecordRow, timeKeys UsageStatsTimeKeys) {
	for _, sample := range latencySamples(row) {
		for _, bucket := range usageLatencyTimeBuckets {
			timeValue := timeKeys.timeValue(bucket.ValueKey)
			key := latencyEntryKey{
				tableName:  bucket.TableName,
				timeValue:  timeValue,
				systemID:   entry.SystemAccountID,
				scopeType:  entry.ScopeType,
				scopeID:    entry.ScopeID,
				metricType: sample.MetricType,
				upperBound: sample.BucketUpperBoundMs,
			}
			if existing, ok := target[key]; ok {
				existing.SampleCount += 1
				continue
			}
			target[key] = &AggregatedLatencyEntry{
				Bucket:             bucket,
				SystemAccountID:    entry.SystemAccountID,
				ScopeType:          entry.ScopeType,
				ScopeID:            entry.ScopeID,
				MetricType:         sample.MetricType,
				TimeValue:          timeValue,
				BucketUpperBoundMs: sample.BucketUpperBoundMs,
				SampleCount:        1,
			}
		}
	}
}
