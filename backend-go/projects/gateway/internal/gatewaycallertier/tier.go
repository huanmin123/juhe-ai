// Package gatewaycallertier 实现调用方（API Key）四级优先级的影子状态机
//（调用方分级调度设计 M1，docs/functions/调用方分级调度设计.md v2）。
//
// M1 边界：只做状态存储 + 信号采集 + 自动升降级（影子运行），无任何资源
// 效果——排队/调度行为与现状完全一致。信号只收高置信本地事实（非法 JSON、
// 本地模型过滤失败），硬信号清单为空、不存在任何「立即 T4」路径；一切
// 读取失败/状态丢失/样本不足都保守回退 T2 默认档。
package gatewaycallertier

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// 层级常量（设计 §2）。DefaultTier 是一切保守回退的终点：新 Key、状态
// 丢失、读取失败、样本不足都按 T2 参与调度。
const (
	TierPremium    = 1
	TierNormal     = 2
	TierWatch      = 3
	TierRestricted = 4

	DefaultTier = TierNormal
	MinTier     = TierPremium
	MaxTier     = TierRestricted
)

// SignalKind 是高置信本地信号的种类。v1 设计中的 429 撞击/4xx 率/取消
// 风暴/队列重入等全部降为二期候选（归因未闭合），当前仅两种。
type SignalKind string

const (
	// SignalInvalidJSON 非法 JSON 请求体解析失败（gatewaybody 本地判定，
	// FailureAttribution=GatewayPolicy，责任方=调用方）。
	SignalInvalidJSON SignalKind = "invalid_json"
	// SignalModelFilterFailed 本地模型过滤失败（preauth model_not_allowed，
	// 请求模型在 Key 资格集内不存在，责任方=调用方）。
	SignalModelFilterFailed SignalKind = "model_filter_failed"
)

// 内部阈值常量（设计 §3/§4；「功能不设开关」纪律，无配置项）。首版取值
// 偏保守（宁可少降级），M1 影子观测数据回填校准后再定稿。
const (
	// minWindowRequests 是评分最小样本：当日请求总数不足此值不评分、层级
	// 保持不变（防小样本误降）。
	minWindowRequests = 20
	// degradedMinuteRate 是「劣化分钟」之外的日级比率门：当日信号率低于
	// 此值即使有信号也不构成降级条件。
	degradedMinuteRate = 0.05
	// watchContinuationMinutes：连续劣化分钟数达到此值触发 T2（或 T1）→T3。
	watchContinuationMinutes = 15
	// restrictedContinuationMinutes：连续劣化分钟数达到此值触发 T3→T4。
	restrictedContinuationMinutes = 60
	// watchRecoveryMinutes：T3 连续无信号分钟数达到此值自动回落 T2。
	watchRecoveryMinutes = 60
	// cleanDaysToWatch：T4 回 T3 所需连续零信号天。
	cleanDaysToWatch = 1
	// cleanDaysToPremium：T2 升 T1 所需连续零信号天。
	cleanDaysToPremium = 7
	// premiumMinDailyRequests 是 T1 的量达标下限（防止低频冷启动 Key 冒充
	// 优质）：晋升窗口内日均请求数下限。
	premiumMinDailyRequests = minWindowRequests
	// degradedMinuteRingBits 是分钟级劣化位图宽度（最近 N 个完整分钟）。
	degradedMinuteRingBits = 64
	// StateTTL 是状态滑动过期（设计 §6.3）：有写即续期；无流量自然过期回
	// T2（不活跃即默认档）。
	StateTTL = 48 * time.Hour
)

// ErrStoreRequired 等构造校验错误。
var ErrStoreRequired = errors.New("gatewaycallertier: 缺少状态存储")

// State 是单 Key 的层级状态（JSON 序列化存储，含滚动窗口计数）。
type State struct {
	Tier       int   `json:"tier"`
	Generation int64 `json:"generation"`
	UpdatedAtMs int64 `json:"updatedAtMs"`
	TierSinceMs int64 `json:"tierSinceMs"`

	// 分钟级劣化位图：bit i = 距 now 第 i+1 个完整分钟（bit0=最近一个完整
	// 分钟）是否发生 ≥1 次信号。仅用于连续性检测，与持久层级无关。
	DegradedMinutes uint64 `json:"degradedMinutes"`
	MinuteEpoch     int64  `json:"minuteEpoch"` // 位图已滚动到的 UTC 分钟号（time.Now().Unix()/60）

	// UTC 日计数（设计 §3 分母 + 连续零信号天）。
	DayUTC           string `json:"dayUTC"`
	DayRequests      int64  `json:"dayRequests"`
	DaySignals       int64  `json:"daySignals"`
	CleanStreakDays  int    `json:"cleanStreakDays"`
	CleanWindowReqs  int64  `json:"cleanWindowRequests"`

	PremiumQualifiedAtMs int64 `json:"premiumQualifiedAtMs"`
}

// Clone 返回深拷贝（mutator 内修改不影响存储原值）。
func (s State) Clone() State { return s }

func (s State) valid() bool {
	return s.Tier >= MinTier && s.Tier <= MaxTier
}

// normalize 把缺失/非法字段收敛为保守合法状态（存量损坏或版本演化时）。
func normalize(s State, now time.Time) State {
	if !s.valid() {
		s.Tier = DefaultTier
		s.TierSinceMs = now.UnixMilli()
	}
	if s.Generation < 0 {
		s.Generation = 0
	}
	return s
}

// minuteEpochOf 把时间折算为 UTC 分钟号。
func minuteEpochOf(now time.Time) int64 { return now.Unix() / 60 }

// dayUTCOf 返回 now 的 UTC 日期串。
func dayUTCOf(now time.Time) string { return now.UTC().Format("2006-01-02") }

// rollMinutes 滚动分钟位图到 currentMinute：把 (current-stored) 个完整分钟
// 移入窗口；窗口宽 64 位，溢出即全部遗忘。
func rollMinutes(s *State, currentMinute int64) {
	diff := currentMinute - s.MinuteEpoch
	if diff <= 0 {
		return
	}
	if diff >= degradedMinuteRingBits {
		s.DegradedMinutes = 0
	} else {
		s.DegradedMinutes <<= uint(diff)
	}
	s.MinuteEpoch = currentMinute
}

// rollDay 在 UTC 日切换时结转前一天：昨天零信号且样本达标则累加连续干净
// 天与晋升窗口请求量，否则清零（保守方向）。
func rollDay(s *State, now time.Time) {
	today := dayUTCOf(now)
	if s.DayUTC == today {
		return
	}
	if s.DayUTC != "" {
		if s.DaySignals == 0 && s.DayRequests >= minWindowRequests {
			s.CleanStreakDays++
			s.CleanWindowReqs += s.DayRequests
		} else {
			s.CleanStreakDays = 0
			s.CleanWindowReqs = 0
		}
	}
	s.DayUTC = today
	s.DayRequests = 0
	s.DaySignals = 0
}

// consecutiveDegraded 返回位图低位的连续劣化分钟数（最近 N 个完整分钟全部
// 有信号）。
func consecutiveDegraded(bmp uint64) int {
	count := 0
	for bmp&1 == 1 && count < degradedMinuteRingBits {
		count++
		bmp >>= 1
	}
	return count
}

// dayRateOK 报告当日是否满足降级的比率与样本双门。
func dayRateOK(s State) bool {
	return s.DayRequests >= minWindowRequests &&
		float64(s.DaySignals)/float64(s.DayRequests) >= degradedMinuteRate
}

// cleanStreakEffective 返回考虑当日的有效连续干净天：今天已有信号则按 0
// 计（干净天资格立即失效，不等日切换）。
func cleanStreakEffective(s State) int {
	if s.DaySignals > 0 {
		return 0
	}
	return s.CleanStreakDays
}

// Evaluate 是纯函数式的影子升降级：先滚动窗口，再按迟滞规则计算下一层级。
// 只返回结果，不改状态；幂等性由「下一层级完全由当前状态推导」保证——重复
// 求值不产生额外变化。资源效果为零（M1 影子边界）。
func Evaluate(s State, now time.Time) State {
	s = normalize(s, now)
	rollMinutes(&s, minuteEpochOf(now))
	rollDay(&s, now)

	next := s.Tier
	consecutive := consecutiveDegraded(s.DegradedMinutes)
	switch {
	// 降级：连续劣化分钟 + 当日比率/样本双门。任何层级持续劣化都至少到 T3
	//（T1 也不豁免）；T3 持续满 60 分钟到 T4。
	case consecutive >= restrictedContinuationMinutes && s.Tier == TierWatch && dayRateOK(s):
		next = TierRestricted
	case consecutive >= watchContinuationMinutes && s.Tier < TierWatch && dayRateOK(s):
		next = TierWatch
	// 回升（迟滞：升慢）。
	case s.Tier == TierWatch && consecutive == 0 && bitsWithoutRecent(s, watchRecoveryMinutes):
		next = TierNormal
	case s.Tier == TierRestricted && cleanStreakEffective(s) >= cleanDaysToWatch:
		next = TierWatch
	case s.Tier == TierNormal &&
		cleanStreakEffective(s) >= cleanDaysToPremium &&
		s.CleanWindowReqs >= int64(cleanDaysToPremium)*premiumMinDailyRequests:
		next = TierPremium
	}
	if next != s.Tier {
		s.Tier = next
		s.TierSinceMs = now.UnixMilli()
		if next == TierPremium {
			s.PremiumQualifiedAtMs = s.TierSinceMs
		}
	}
	return s
}

// bitsWithoutRecent 报告最近 windowMinutes 个完整分钟是否全部无信号。
func bitsWithoutRecent(s State, windowMinutes int) bool {
	if windowMinutes >= degradedMinuteRingBits {
		return s.DegradedMinutes == 0
	}
	mask := uint64(1)<<uint(windowMinutes) - 1
	return s.DegradedMinutes&mask == 0
}

// ErrUnknownSignal 表示信号种类不在白名单（调用方编程错误，非运行态错误）。
var ErrUnknownSignal = errors.New("gatewaycallertier: 未知信号种类")

// applyRecord 把一次请求/信号计入窗口并完成影子求值。kind 为空串表示纯
// 请求分母计数（RecordRequest），非空必须是白名单信号。
func applyRecord(s State, kind SignalKind, now time.Time) (State, error) {
	if kind != "" {
		switch kind {
		case SignalInvalidJSON, SignalModelFilterFailed:
		default:
			return State{}, fmt.Errorf("%w: %q", ErrUnknownSignal, string(kind))
		}
	}
	rollMinutes(&s, minuteEpochOf(now))
	rollDay(&s, now)
	s.DayRequests++
	if kind != "" {
		s.DaySignals++
		s.DegradedMinutes |= 1
	}
	return Evaluate(s, now), nil
}

// marshalState/unmarshalState 集中序列化边界（损坏一律保守回退默认态，
// 不向上传播数据错误）。
func marshalState(s State) ([]byte, error) {
	return json.Marshal(s)
}

func unmarshalState(blob []byte, now time.Time) State {
	var s State
	if err := json.Unmarshal(blob, &s); err != nil {
		return State{Tier: DefaultTier, TierSinceMs: now.UnixMilli()}
	}
	return normalize(s, now)
}
