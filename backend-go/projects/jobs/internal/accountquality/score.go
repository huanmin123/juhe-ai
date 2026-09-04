package accountquality

import (
	"fmt"
	"time"
)

// ComputeQualityScore 是 Node computeQualityScore 的逐字段移植：
//
//	latency（EWMA 毫秒；缺失取 1_000_000）+ 状态罚分 + 陈旧龄期罚分，下限 0。
func ComputeQualityScore(ewmaFirstTokenMs *int64, successRate *float64, state QualityState, updatedAt time.Time, now time.Time) int64 {
	latency := int64(UnknownQualityScore)
	if ewmaFirstTokenMs != nil {
		latency = *ewmaFirstTokenMs
	}
	var statePenalty int64
	switch state {
	case QualityFailed:
		statePenalty = FailurePenaltyMs
	case QualityStale:
		statePenalty = StalePenaltyMs
	case QualityUnknown:
		statePenalty = UnknownStatePenaltyMs
	}
	agePenalty := AgePenaltyMs(updatedAt, now)
	score := latency + statePenalty + agePenalty
	if score < 0 {
		score = 0
	}
	return score
}

// AgePenaltyMs 是 Node agePenaltyMs 的移植：min(10_000, floor(ageMinutes)*100)。
func AgePenaltyMs(updatedAt, now time.Time) int64 {
	ageMs := now.Sub(updatedAt).Milliseconds()
	var ageMinutes int64
	if ageMs > 0 {
		ageMinutes = ageMs / 60_000
	}
	penalty := ageMinutes * 100
	if penalty > AgePenaltyCapMs {
		penalty = AgePenaltyCapMs
	}
	return penalty
}

// NextEwma 是 EWMA 0.6/0.4 合并（Node：Math.round(prev*0.6 + recent*0.4)）。
// prev/recent 任一缺失时保留另一侧。
func NextEwma(previousEwma, recentAvg *int64) *int64 {
	if recentAvg == nil {
		return previousEwma
	}
	if previousEwma == nil {
		return recentAvg
	}
	merged := int64(float64(*previousEwma)*0.6 + float64(*recentAvg)*0.4)
	return &merged
}

// SuccessRateAfterWindow 与 Node 一致：窗口内有请求则 clamp(0,1)，否则沿用旧值。
func SuccessRateAfterWindow(recentRequestCount, recentSuccessCount int64, previousRate *float64) *float64 {
	if recentRequestCount > 0 {
		rate := float64(recentSuccessCount) / float64(recentRequestCount)
		if rate < 0 {
			rate = 0
		}
		if rate > 1 {
			rate = 1
		}
		return &rate
	}
	return previousRate
}

// IntegerOrNull 与 Node integerOrNull 一致（仅接受非负有限数值的 round），
// 由调用方直接传数值，故此处在 SQL 扫描侧完成（见 store.go）。
func parseInstantOrError(value, field string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("%s 必须是带 Z 或数值 offset 的 RFC3339 时间：%s", field, value)
	}
	return t, nil
}
