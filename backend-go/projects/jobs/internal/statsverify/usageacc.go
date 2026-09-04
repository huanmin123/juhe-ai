package statsverify

// UsageStatsRecordRow mirrors the subset of UsageStatsRecordRow
// (storage/usage-stats-types.ts lines 24-67) consumed by the client-ip
// aggregation writer. Field names follow the snake_case SQL columns so row
// scans stay mechanical.
type UsageStatsRecordRow struct {
	ID                 string
	SystemAccountID    string
	TrafficSource      string
	ClientIP           *string
	AccountID          *string
	GroupID            *string
	Success            int
	FirstTokenMs       *int
	DurationMs         *int
	InputTokens        *int
	OutputTokens       *int
	CacheReadTokens    *int
	CacheReadCostUsd   *float64
	CacheWriteTokens   *int
	CacheWrite1hTokens *int
	CacheWriteCostUsd  *float64
	ThinkingTokens     *int
	InputImageTokens   *int
	OutputImageTokens  *int
	CostUsd            *float64
	CreatedAt          string
}

// UsageStatsAccumulator mirrors UsageStatsAccumulator
// (storage/usage-stats-types.ts lines 119-142); only the fields written by
// the client-ip daily tables are carried.
type UsageStatsAccumulator struct {
	RequestCount      int
	SuccessCount      int
	ErrorCount        int
	InputTokens       int
	OutputTokens      int
	CacheReadTokens   int
	CacheReadCostUsd  float64
	CacheWriteTokens  int
	CacheWrite1hTok   int
	CacheWriteCostUsd float64
	ThinkingTokens    int
	InputImageTokens  int
	OutputImageTokens int
	TotalCostUsd      float64
	DurationMsSum     int
	DurationMsCount   int
	DurationMsMax     int
	FirstTokenMsSum   int
	FirstTokenMsCount int
	LastUsedAt        string
	LastErrorAt       string
}

// AccumulatorFromRecord mirrors usageStatsAccumulatorFromRecord
// (storage/usage-stats-aggregation.ts lines 155-183):
//   - success === 1 splits success/error counts (anything else is an error);
//   - every numeric field clamps negatives to zero;
//   - null duration/first_token contribute zero to sum and zero to count;
//   - totalCostUsd reads cost_usd;
//   - lastErrorAt is set only for failed requests.
func AccumulatorFromRecord(row UsageStatsRecordRow) UsageStatsAccumulator {
	success := row.Success == 1
	durationMs := 0
	if row.DurationMs != nil {
		durationMs = clampNonNegative(*row.DurationMs)
	}
	firstTokenMs := 0
	if row.FirstTokenMs != nil {
		firstTokenMs = clampNonNegative(*row.FirstTokenMs)
	}
	lastErrorAt := ""
	if !success {
		lastErrorAt = row.CreatedAt
	}
	return UsageStatsAccumulator{
		RequestCount:      1,
		SuccessCount:      boolToInt(success),
		ErrorCount:        boolToInt(!success),
		InputTokens:       clampNonNegative(derefInt(row.InputTokens)),
		OutputTokens:      clampNonNegative(derefInt(row.OutputTokens)),
		CacheReadTokens:   clampNonNegative(derefInt(row.CacheReadTokens)),
		CacheReadCostUsd:  clampNonNegativeFloat(derefFloat(row.CacheReadCostUsd)),
		CacheWriteTokens:  clampNonNegative(derefInt(row.CacheWriteTokens)),
		CacheWrite1hTok:   clampNonNegative(derefInt(row.CacheWrite1hTokens)),
		CacheWriteCostUsd: clampNonNegativeFloat(derefFloat(row.CacheWriteCostUsd)),
		ThinkingTokens:    clampNonNegative(derefInt(row.ThinkingTokens)),
		InputImageTokens:  clampNonNegative(derefInt(row.InputImageTokens)),
		OutputImageTokens: clampNonNegative(derefInt(row.OutputImageTokens)),
		TotalCostUsd:      clampNonNegativeFloat(derefFloat(row.CostUsd)),
		DurationMsSum:     durationMs,
		DurationMsCount:   boolToInt(row.DurationMs != nil),
		DurationMsMax:     boolToInt(row.DurationMs != nil) * durationMs,
		FirstTokenMsSum:   firstTokenMs,
		FirstTokenMsCount: boolToInt(row.FirstTokenMs != nil),
		LastUsedAt:        row.CreatedAt,
		LastErrorAt:       lastErrorAt,
	}
}

func derefInt(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}

func derefFloat(value *float64) float64 {
	if value == nil {
		return 0
	}
	return *value
}

func clampNonNegative(value int) int {
	if value < 0 {
		return 0
	}
	return value
}

func clampNonNegativeFloat(value float64) float64 {
	if value < 0 {
		return 0
	}
	return value
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
