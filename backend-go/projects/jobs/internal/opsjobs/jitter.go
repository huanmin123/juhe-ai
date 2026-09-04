package opsjobs

import "math"

// 被动调度抖动策略逐字段对齐 Node shared/passive-schedule-jitter.ts。
// 被动轮询与周期扫描必须使用对称抖动，避免同相位集群收敛；
// 租约续期、请求超时、心跳等精确 deadline 语义不使用本策略。
const (
	jitterSubMinuteWindowMS = 30_000
	jitterMinuteWindowMS    = 30_000
	jitterHourWindowMS      = 30 * 60_000
	jitterDayWindowMS       = 60 * 60_000
	jitterWeekWindowMS      = 8 * 60 * 60_000
)

// PassiveScheduleJitterWindowMS 返回单个被动间隔的对称抖动窗口。
func PassiveScheduleJitterWindowMS(intervalMS int64) int64 {
	interval := normalizedIntervalMS(intervalMS)
	var windowMS int64
	switch {
	case interval < 60_000:
		// 短间隔不允许变负或背靠背执行。
		windowMS = min64(jitterSubMinuteWindowMS, interval/2)
	case interval < 60*60_000:
		windowMS = jitterMinuteWindowMS
	case interval < 24*60*60_000:
		windowMS = jitterHourWindowMS
	case interval < 7*24*60*60_000:
		windowMS = jitterDayWindowMS
	default:
		windowMS = jitterWeekWindowMS
	}
	return min64(windowMS, max64(0, interval/2))
}

// RandomUnit 是被动抖动的随机源抽象，返回 [0,1] 均匀样本；nil = 确定性 0。
type RandomUnit func() float64

// PassiveScheduleOffsetMS 产生全新的有界对称偏移；0 会被改成 1ms，
// 保证被动任务永远不会精确落在配置时间戳上。
func PassiveScheduleOffsetMS(intervalMS int64, random RandomUnit) int64 {
	return passiveScheduleOffsetWithinWindowMS(PassiveScheduleJitterWindowMS(intervalMS), random)
}

// PassiveScheduleDelayMS 在保持严格正延迟的前提下叠加新偏移。
func PassiveScheduleDelayMS(intervalMS int64, random RandomUnit) int64 {
	return max64(1, normalizedIntervalMS(intervalMS)+PassiveScheduleOffsetMS(intervalMS, random))
}

func passiveScheduleOffsetWithinWindowMS(windowMS int64, random RandomUnit) int64 {
	if windowMS <= 0 {
		return 0
	}
	var unit float64
	if random != nil {
		unit = random()
		if math.IsNaN(unit) || math.IsInf(unit, 0) {
			unit = 0
		}
		unit = math.Min(1, math.Max(0, unit))
	}
	offset := min64(windowMS, int64(math.Floor(unit*float64(windowMS*2+1)))-windowMS)
	if offset == 0 {
		return 1
	}
	return offset
}

func normalizedIntervalMS(intervalMS int64) int64 {
	if intervalMS < 1 || math.IsNaN(float64(intervalMS)) || math.IsInf(float64(intervalMS), 0) {
		return 1
	}
	return intervalMS
}

func min64(left, right int64) int64 {
	if left < right {
		return left
	}
	return right
}

func max64(left, right int64) int64 {
	if left > right {
		return left
	}
	return right
}
