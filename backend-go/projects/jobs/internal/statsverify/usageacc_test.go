package statsverify

import "testing"

// TestAccumulatorFromRecord mirrors usageStatsAccumulatorFromRecord
// (usage-stats-aggregation.ts lines 155-183): success flag split, negative
// clamping, null durations contributing zero sum AND zero count, cost_usd as
// total, and lastErrorAt only for failures.
func TestAccumulatorFromRecord(t *testing.T) {
	t.Run("success row", func(t *testing.T) {
		acc := AccumulatorFromRecord(UsageStatsRecordRow{
			Success:            1,
			DurationMs:         intPtr(120),
			FirstTokenMs:       intPtr(30),
			InputTokens:        intPtr(10),
			OutputTokens:       intPtr(20),
			CacheReadTokens:    intPtr(5),
			CacheReadCostUsd:   f64Ptr(0.5),
			CacheWriteTokens:   intPtr(6),
			CacheWrite1hTokens: intPtr(7),
			CacheWriteCostUsd:  f64Ptr(0.75),
			ThinkingTokens:     intPtr(8),
			InputImageTokens:   intPtr(9),
			OutputImageTokens:  intPtr(10),
			CostUsd:            f64Ptr(1.25),
			CreatedAt:          "2026-03-01T00:00:00.000Z",
		})
		if acc.RequestCount != 1 || acc.SuccessCount != 1 || acc.ErrorCount != 0 {
			t.Fatalf("request split wrong: %+v", acc)
		}
		if acc.DurationMsSum != 120 || acc.DurationMsCount != 1 || acc.DurationMsMax != 120 {
			t.Fatalf("duration wrong: %+v", acc)
		}
		if acc.FirstTokenMsSum != 30 || acc.FirstTokenMsCount != 1 {
			t.Fatalf("first token wrong: %+v", acc)
		}
		if acc.TotalCostUsd != 1.25 {
			t.Fatalf("total cost=%v", acc.TotalCostUsd)
		}
		if acc.LastUsedAt != "2026-03-01T00:00:00.000Z" {
			t.Fatalf("lastUsedAt=%q", acc.LastUsedAt)
		}
		if acc.LastErrorAt != "" {
			t.Fatalf("success row must not carry lastErrorAt, got %q", acc.LastErrorAt)
		}
	})

	t.Run("failure row", func(t *testing.T) {
		acc := AccumulatorFromRecord(UsageStatsRecordRow{
			Success:   0,
			CreatedAt: "2026-03-01T01:00:00.000Z",
		})
		if acc.SuccessCount != 0 || acc.ErrorCount != 1 {
			t.Fatalf("failure split wrong: %+v", acc)
		}
		if acc.LastErrorAt != "2026-03-01T01:00:00.000Z" {
			t.Fatalf("lastErrorAt=%q", acc.LastErrorAt)
		}
	})

	t.Run("null and negative fields", func(t *testing.T) {
		acc := AccumulatorFromRecord(UsageStatsRecordRow{
			Success:          1,
			DurationMs:       nil,
			InputTokens:      intPtr(-5),
			CacheReadCostUsd: f64Ptr(-0.25),
			CostUsd:          f64Ptr(-1),
			CreatedAt:        "2026-03-01T02:00:00.000Z",
		})
		if acc.DurationMsSum != 0 || acc.DurationMsCount != 0 || acc.DurationMsMax != 0 {
			t.Fatalf("null duration must contribute zero sum and count: %+v", acc)
		}
		if acc.FirstTokenMsCount != 0 {
			t.Fatalf("null first token must contribute zero count")
		}
		if acc.InputTokens != 0 || acc.CacheReadCostUsd != 0 || acc.TotalCostUsd != 0 {
			t.Fatalf("negative values must clamp to zero: %+v", acc)
		}
	})
}
