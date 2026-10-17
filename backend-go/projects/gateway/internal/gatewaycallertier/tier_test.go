package gatewaycallertier

import (
	"context"
	"errors"
	"testing"
	"time"
)

// tierTestNow 固定时钟（UTC，便于日/分钟窗口确定性）。
func tierTestNow(y int, mo time.Month, d, h, mi, s int) time.Time {
	return time.Date(y, mo, d, h, mi, s, 0, time.UTC)
}

// TestEvaluateDefaultAndConservativePaths 新 Key/空状态/损坏态全部保守 T2。
func TestEvaluateDefaultAndConservativePaths(t *testing.T) {
	now := tierTestNow(2026, 10, 17, 8, 0, 0)
	fresh := Evaluate(State{}, now)
	if fresh.Tier != DefaultTier {
		t.Fatalf("空状态必须保守 T2: %d", fresh.Tier)
	}
	if fresh.MinuteEpoch != minuteEpochOf(now) || fresh.DayUTC != dayUTCOf(now) {
		t.Fatalf("空状态求值必须完成窗口初始化: %+v", fresh)
	}
	corrupted := State{Tier: 9}
	if got := Evaluate(corrupted, now); got.Tier != DefaultTier {
		t.Fatalf("非法层级必须归一 T2: %d", got.Tier)
	}
	if blob := []byte("{broken"); unmarshalState(blob, now).Tier != DefaultTier {
		t.Fatal("损坏序列化必须保守 T2")
	}
}

// TestEvaluateDegradationHysteresis 降级迟滞：15 连续劣化分钟 + 当日比率/
// 样本双门 → T3；持续 60 分钟 → T4；比率或样本不足不降。
func TestEvaluateDegradationHysteresis(t *testing.T) {
	base := tierTestNow(2026, 10, 17, 8, 0, 0)
	buildState := func(tier int, consecutive int, dayReqs, daySignals int64) State {
		s := State{Tier: tier, DayUTC: dayUTCOf(base), DayRequests: dayReqs, DaySignals: daySignals}
		for i := 0; i < consecutive; i++ {
			s.DegradedMinutes |= 1 << uint(i)
		}
		s.MinuteEpoch = minuteEpochOf(base) + 1
		return s
	}
	// 15 分钟连续 + 比率达标 → T2 到 T3。
	s := buildState(TierNormal, watchContinuationMinutes, 400, 30)
	if got := Evaluate(s, base); got.Tier != TierWatch {
		t.Fatalf("15 连续劣化分钟应降 T3: %d", got.Tier)
	}
	// 比率不足（信号率 < 5%）不降。
	s = buildState(TierNormal, watchContinuationMinutes, 400, 3)
	if got := Evaluate(s, base); got.Tier != TierNormal {
		t.Fatalf("比率不足不降级: %d", got.Tier)
	}
	// 样本不足不降。
	s = buildState(TierNormal, watchContinuationMinutes, 10, 10)
	if got := Evaluate(s, base); got.Tier != TierNormal {
		t.Fatalf("样本不足不降级: %d", got.Tier)
	}
	// 60 分钟连续 → T3 到 T4。
	s = buildState(TierWatch, restrictedContinuationMinutes, 400, 30)
	if got := Evaluate(s, base); got.Tier != TierRestricted {
		t.Fatalf("60 连续劣化分钟应降 T4: %d", got.Tier)
	}
	// T1 持续劣化同样到 T3（优质不豁免）。
	s = buildState(TierPremium, watchContinuationMinutes, 400, 30)
	if got := Evaluate(s, base); got.Tier != TierWatch {
		t.Fatalf("T1 持续劣化应降 T3: %d", got.Tier)
	}
	// 14 分钟连续不降（阈值边界）。
	s = buildState(TierNormal, watchContinuationMinutes-1, 400, 30)
	if got := Evaluate(s, base); got.Tier != TierNormal {
		t.Fatalf("未满 15 连续分钟不降级: %d", got.Tier)
	}
}

// TestEvaluateRecovery 迟滞回升：T3 静默 60 分钟回 T2；T4 需连续干净天；
// T2 升 T1 需 7 天干净 + 窗口量达标；今天有信号立即取消干净资格。
func TestEvaluateRecovery(t *testing.T) {
	base := tierTestNow(2026, 10, 17, 8, 0, 0)
	// T3 静默 60 分钟 → T2。
	s := State{Tier: TierWatch, MinuteEpoch: minuteEpochOf(base), DayUTC: dayUTCOf(base), DayRequests: 100}
	if got := Evaluate(s, base); got.Tier != TierNormal {
		t.Fatalf("T3 静默应回 T2: %d", got.Tier)
	}
	// T3 仍有近期信号不回升。
	s.DegradedMinutes = 1 << (watchRecoveryMinutes - 1)
	s.MinuteEpoch = minuteEpochOf(base) + 1
	if got := Evaluate(s, base); got.Tier != TierWatch {
		t.Fatalf("静默不足不回升: %d", got.Tier)
	}
	// T4 无干净天不回升。
	s = State{Tier: TierRestricted, DayUTC: dayUTCOf(base), DayRequests: 100, CleanStreakDays: 0}
	if got := Evaluate(s, base); got.Tier != TierRestricted {
		t.Fatalf("T4 无干净天不回升: %d", got.Tier)
	}
	// T4 连续 1 干净天 → T3。
	s.CleanStreakDays = cleanDaysToWatch
	if got := Evaluate(s, base); got.Tier != TierWatch {
		t.Fatalf("T4 干净 1 天应回 T3: %d", got.Tier)
	}
	// T4 今天已有信号：干净资格立即失效不回升。
	s.DaySignals = 1
	if got := Evaluate(s, base); got.Tier != TierRestricted {
		t.Fatalf("今日有信号不得回升: %d", got.Tier)
	}
	// T2 六天干净不升 T1；七天 + 量达标升 T1。
	s = State{Tier: TierNormal, DayUTC: dayUTCOf(base), DayRequests: 30, CleanStreakDays: cleanDaysToPremium - 1, CleanWindowReqs: 500}
	if got := Evaluate(s, base); got.Tier != TierNormal {
		t.Fatalf("干净不足 7 天不升 T1: %d", got.Tier)
	}
	s.CleanStreakDays = cleanDaysToPremium
	if got := Evaluate(s, base); got.Tier != TierPremium {
		t.Fatalf("7 天干净 + 量达标应升 T1: %d", got.Tier)
	}
	// 窗口量不足不升。
	s.CleanWindowReqs = 10
	if got := Evaluate(s, base); got.Tier != TierNormal {
		t.Fatalf("晋升窗口量不足不升 T1: %d", got.Tier)
	}
}

// TestApplyRecordWindows 分钟/日窗口滚动与位图语义：新信号进入 bit0；日切换
// 结转干净天。
func TestApplyRecordWindows(t *testing.T) {
	now := tierTestNow(2026, 10, 17, 8, 0, 0)
	s := State{}
	s = Evaluate(s, now)
	// 三条信号（跨三个完整分钟）。
	s, err := applyRecord(s, SignalInvalidJSON, now)
	if err != nil {
		t.Fatal(err)
	}
	later := now.Add(2 * time.Minute)
	s = Evaluate(s, later)
	s, err = applyRecord(s, SignalModelFilterFailed, later)
	if err != nil {
		t.Fatal(err)
	}
	s = Evaluate(s, later)
	// T 信号经 2 分钟滚动到 bit2，T+2 信号在 bit0，中间完整分钟 bit1 无信号。
	if s.DegradedMinutes&(1|4) != 1|4 {
		t.Fatalf("信号位应落在 bit0/bit2: %b", s.DegradedMinutes)
	}
	if s.DegradedMinutes&2 != 0 {
		t.Fatalf("无信号的完整分钟不得置位: %b", s.DegradedMinutes)
	}
	if s.DaySignals != 2 || s.DayRequests != 2 {
		t.Fatalf("当日计数不符: req=%d sig=%d", s.DayRequests, s.DaySignals)
	}
	// 静默 61 分钟后位图清零 → T3 回落条件满足。
	s.Tier = TierWatch
	s = Evaluate(s, later.Add(61*time.Minute))
	if s.Tier != TierNormal {
		t.Fatalf("静默超窗应回落 T2: %d", gotTier(s))
	}
}

func gotTier(s State) int { return s.Tier }

// TestDayRollCleanStreak 日切换结转：昨天零信号且样本达标 → 连续干净天累加，
// 否则清零。
func TestDayRollCleanStreak(t *testing.T) {
	day1 := tierTestNow(2026, 10, 16, 23, 59, 0)
	day2 := tierTestNow(2026, 10, 17, 8, 0, 0)
	s := Evaluate(State{}, day1)
	// 第 1 天：有请求无信号且达标。
	s, err := applyRecord(s, "", day1)
	if err != nil {
		t.Fatal(err)
	}
	s.DayRequests = 30
	// 跨到第 2 天：结转后 streak=1。
	s = Evaluate(s, day2)
	if s.CleanStreakDays != 1 {
		t.Fatalf("零信号达标日应结转干净天: %d", s.CleanStreakDays)
	}
	if s.DayRequests != 0 || s.DaySignals != 0 {
		t.Fatalf("日切换必须重置当日计数: %+v", s)
	}
	// 第 2 天有信号 → 第 3 天结转清零。
	s, _ = applyRecord(s, SignalInvalidJSON, day2)
	day3 := tierTestNow(2026, 10, 18, 8, 0, 0)
	s = Evaluate(s, day3)
	if s.CleanStreakDays != 0 {
		t.Fatalf("有信号日必须清零干净天: %d", s.CleanStreakDays)
	}
}

// TestRecorderConservativeAndMetrics 门面行为：空 Key 拒绝、未知信号按错误
// 计量、读取失败/无状态一律 T2、指标回调触发。
func TestRecorderConservativeAndMetrics(t *testing.T) {
	store := NewMemoryStore(nil)
	var recordTotal [][2]string
	var transitions [][2]int
	metrics := Metrics{
		RecordTotal:     func(name, kind string) { recordTotal = append(recordTotal, [2]string{name, kind}) },
		TierGauge:       func(string, int) {},
		TransitionTotal: func(from, to int) { transitions = append(transitions, [2]int{from, to}) },
	}
	recorder, err := NewRecorder(store, metrics, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewRecorder(nil, metrics, nil); !errors.Is(err, ErrStoreRequired) {
		t.Fatalf("缺存储必须报 ErrStoreRequired: %v", err)
	}
	ctx := context.Background()
	recorder.RecordRequest(ctx, "")
	recorder.RecordSignal(ctx, "key-a", SignalKind("bogus"))
	if len(recordTotal) != 2 || recordTotal[0][0] != "error" || recordTotal[1][1] != "unknown_signal_kind" {
		t.Fatalf("空 Key/未知信号必须按错误计量: %+v", recordTotal)
	}
	if got := recorder.TierOf(ctx, "missing-key"); got != DefaultTier {
		t.Fatalf("无状态必须 T2: %d", got)
	}
	recorder.RecordSignal(ctx, "key-a", SignalModelFilterFailed)
	recorder.RecordRequest(ctx, "key-a")
	if got := recorder.TierOf(ctx, "key-a"); got != DefaultTier {
		t.Fatalf("单次信号远不构成降级，必须 T2: %d", got)
	}
	if len(transitions) != 0 {
		t.Fatalf("小样本不得发生层级转换: %+v", transitions)
	}
}

// TestMemoryStoreTTLAndSnapshot 过期即保守无状态。
func TestMemoryStoreTTLAndSnapshot(t *testing.T) {
	current := tierTestNow(2026, 10, 17, 8, 0, 0)
	store := NewMemoryStore(func() time.Time { return current })
	ctx := context.Background()
	if err := store.Mutate(ctx, "k", func([]byte) ([]byte, error) { return marshalState(State{Tier: TierWatch}) }); err != nil {
		t.Fatal(err)
	}
	if got := unmarshalState(mustRaw(t, store, ctx, "k"), current); got.Tier != TierWatch {
		t.Fatalf("未过期应可读: %d", got.Tier)
	}
	current = current.Add(StateTTL + time.Minute)
	if raw, err := store.GetRaw(ctx, "k"); err != nil || raw != nil {
		t.Fatalf("过期必须视为无状态: %v %v", raw, err)
	}
	if len(store.Snapshot()) != 0 {
		t.Fatal("过期条目不得进入快照")
	}
}

func mustRaw(t *testing.T, store *MemoryStore, ctx context.Context, key string) []byte {
	t.Helper()
	raw, err := store.GetRaw(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
