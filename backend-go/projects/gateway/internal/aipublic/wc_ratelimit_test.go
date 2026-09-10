// PenaltyWindowLimiter（进程内惩罚窗口限流器）的补测：清空、指数惩罚翻倍、
// 超容量裁剪（trim）、过期清理（cleanup）与 Retry-After 换算。全部使用注入
// 时钟，无 sleep、无真实时间依赖。Redis 驱动分支由 capture_test.go 的假
// Redis 覆盖，这里补齐其决策回退分支。
package aipublic

import (
	"context"
	"testing"
	"time"
)

// newWCLimiter 用可控毫秒时钟构造限流器。
func newWCLimiter(nowMs func() int64) *PenaltyWindowLimiter {
	return &PenaltyWindowLimiter{
		maxEntries: rateLimitMaxEntries,
		maxIdleMs:  rateLimitMaxIdleMs,
		maxPenalty: rateLimitMaxPenaltyMs,
		nowMs:      nowMs,
		entries:    map[string]*penaltyEntry{},
	}
}

// TestWCPenaltyClear：清空后桶归零，惩罚块失效。
func TestWCPenaltyClear(t *testing.T) {
	clock := int64(1_000_000)
	limiter := newWCLimiter(func() int64 { return clock })
	rules := []RateLimitRule{{WindowSeconds: 60, MaxRequests: 1}}
	if decision := limiter.Consume("k", rules); !decision.Allowed {
		t.Fatalf("首次消费必须放行: %+v", decision)
	}
	clock += 1000
	if decision := limiter.Consume("k", rules); decision.Allowed {
		t.Fatalf("超限消费必须拒绝: %+v", decision)
	}
	limiter.Clear()
	if decision := limiter.Consume("k", rules); !decision.Allowed {
		t.Fatalf("清空后必须重新放行: %+v", decision)
	}
}

// TestWCPenaltyExponentialDoubling：连续触发惩罚时块时长翻倍（封顶
// max(window, maxPenalty)），窗口翻转后计数重置但惩罚基数保留。
func TestWCPenaltyExponentialDoubling(t *testing.T) {
	clock := int64(10_000_000)
	limiter := newWCLimiter(func() int64 { return clock })
	rules := []RateLimitRule{{WindowSeconds: 10, MaxRequests: 1}}

	// 第 1 次：窗口内正常。
	if decision := limiter.Consume("k", rules); !decision.Allowed {
		t.Fatalf("窗口内首次: %+v", decision)
	}
	// 第 2 次（同窗口）：count>=max → 惩罚块 = window 10s。
	clock += 1000
	blocked := limiter.Consume("k", rules)
	if blocked.Allowed || blocked.RetryAfterSeconds != 10 {
		t.Fatalf("首次惩罚块应为 10s: %+v", blocked)
	}
	// 块内重试：基数翻倍 → 20s。
	clock += 1000
	retry := limiter.Consume("k", rules)
	if retry.Allowed || retry.RetryAfterSeconds != 20 {
		t.Fatalf("块内重试惩罚应翻倍为 20s: %+v", retry)
	}
	// 仍在块内：再次翻倍 → 40s。
	clock += 1000
	retry = limiter.Consume("k", rules)
	if retry.Allowed || retry.RetryAfterSeconds != 40 {
		t.Fatalf("连续惩罚应继续翻倍为 40s: %+v", retry)
	}
	// 跨窗口（window 在 10_040_000 翻转）：blockedUntil=10_043_000 未过期，
	// 新窗口条目沿用 hasBlock 继续拒绝，惩罚基数继续翻倍（40s → 80s）。
	clock = 10_000_000 + 41_000
	after := limiter.Consume("k", rules)
	if after.Allowed || after.RetryAfterSeconds != 80 {
		t.Fatalf("惩罚块跨窗口仍拒绝且基数翻倍: %+v", after)
	}
	// 惩罚块到期：放行并重置计数。
	clock = 10_000_000 + 130_000
	if decision := limiter.Consume("k", rules); !decision.Allowed {
		t.Fatalf("块过期后放行: %+v", decision)
	}
}

// TestWCPenaltyMaxPenaltyCap：惩罚封顶取 max(windowMs, maxPenalty)。
func TestWCPenaltyMaxPenaltyCap(t *testing.T) {
	clock := int64(0)
	limiter := &PenaltyWindowLimiter{
		maxEntries: rateLimitMaxEntries, maxIdleMs: rateLimitMaxIdleMs,
		maxPenalty: 5_000, nowMs: func() int64 { return clock },
		entries: map[string]*penaltyEntry{},
	}
	rules := []RateLimitRule{{WindowSeconds: 60, MaxRequests: 1}}
	limiter.Consume("k", rules)
	clock = 1
	blocked := limiter.Consume("k", rules)
	// window 60s > maxPenalty 5s → 基数 60s。
	if blocked.RetryAfterSeconds != 60 {
		t.Fatalf("惩罚基数取窗口: %+v", blocked)
	}
	clock = 2
	retry := limiter.Consume("k", rules)
	// 翻倍 120s → 封顶 max(60s, maxPenalty 5s)=60s… 基数 60*2=120 > 60 → 封顶 60s。
	if retry.RetryAfterSeconds != 60 {
		t.Fatalf("惩罚翻倍后封顶窗口: %+v", retry)
	}
}

// TestWCPenaltyTrim：超过 maxEntries 时按 lastSeen 淘汰最旧条目，
// 惩罚块中的条目豁免。
func TestWCPenaltyTrim(t *testing.T) {
	clock := int64(1_000)
	limiter := &PenaltyWindowLimiter{
		maxEntries: 3, maxIdleMs: rateLimitMaxIdleMs, maxPenalty: rateLimitMaxPenaltyMs,
		nowMs:   func() int64 { return clock },
		entries: map[string]*penaltyEntry{},
	}
	// 预置 3 条：old（最旧）、blocked（块中豁免）、new。
	limiter.entries["old"] = &penaltyEntry{lastSeenAtMs: 100}
	limiter.entries["blocked"] = &penaltyEntry{lastSeenAtMs: 200, hasBlock: true, blockedUntilMs: 10_000}
	limiter.entries["new"] = &penaltyEntry{lastSeenAtMs: 900}
	// 第 4 条触发 trim（trim 在提交新条目后执行，容量 3）。
	limiter.entries["fresh"] = &penaltyEntry{lastSeenAtMs: 950}
	clock = 1_000
	limiter.trim(clock)
	if _, exists := limiter.entries["old"]; exists {
		t.Fatalf("最旧条目必须被裁剪: %v", limiter.entries)
	}
	if _, exists := limiter.entries["blocked"]; !exists {
		t.Fatalf("惩罚块内条目必须豁免: %v", limiter.entries)
	}
	if len(limiter.entries) > 3 {
		t.Fatalf("裁剪后必须收敛到 maxEntries: %d", len(limiter.entries))
	}
	// 未超容量时 trim 是 no-op。
	before := len(limiter.entries)
	limiter.trim(clock)
	if len(limiter.entries) != before {
		t.Fatalf("容量内 trim 不得删除: %d → %d", before, len(limiter.entries))
	}
}

// TestWCPenaltyCleanup：60s 周期性清理过期闲置条目，活跃/块中条目保留。
func TestWCPenaltyCleanup(t *testing.T) {
	clock := int64(1_000_000)
	limiter := &PenaltyWindowLimiter{
		maxEntries: rateLimitMaxEntries, maxIdleMs: 60_000, maxPenalty: rateLimitMaxPenaltyMs,
		nowMs: func() int64 { return clock },
		entries: map[string]*penaltyEntry{
			"idle":    {lastSeenAtMs: 100},
			"active":  {lastSeenAtMs: 999_500},
			"blocked": {lastSeenAtMs: 100, hasBlock: true, blockedUntilMs: 2_000_000},
		},
		nextCleanup: 0,
	}
	limiter.cleanup(clock)
	if _, exists := limiter.entries["idle"]; exists {
		t.Fatalf("超闲置期条目必须清理: %v", limiter.entries)
	}
	if _, exists := limiter.entries["active"]; !exists {
		t.Fatalf("活跃条目必须保留")
	}
	if _, exists := limiter.entries["blocked"]; !exists {
		t.Fatalf("块中条目必须保留")
	}
	if limiter.nextCleanup != clock+60_000 {
		t.Fatalf("下次清理时间: %d", limiter.nextCleanup)
	}
	// 未到期且未超容量：no-op。
	limiter.nextCleanup = clock + 60_000
	limiter.entries["extra"] = &penaltyEntry{lastSeenAtMs: 100}
	limiter.cleanup(clock + 1)
	if _, exists := limiter.entries["extra"]; !exists {
		t.Fatalf("清理周期内不得清理")
	}
}

// TestWCRetryAfterSeconds：毫秒 → 秒的向上取整与下限。
func TestWCRetryAfterSeconds(t *testing.T) {
	cases := []struct {
		inputMs int64
		want    int
	}{
		{-5, 1},
		{0, 1},
		{1, 1},
		{999, 1},
		{1000, 1},
		{1001, 2},
		{61_000, 61},
	}
	for _, testCase := range cases {
		if got := retryAfterSeconds(testCase.inputMs); got != testCase.want {
			t.Fatalf("retryAfterSeconds(%d) = %d，期望 %d", testCase.inputMs, got, testCase.want)
		}
	}
}

// TestWCConsumeEmptyRules：空规则集直接放行且不建桶。
func TestWCConsumeEmptyRules(t *testing.T) {
	limiter := newWCLimiter(func() int64 { return 0 })
	if decision := limiter.Consume("k", nil); !decision.Allowed {
		t.Fatalf("空规则必须放行: %+v", decision)
	}
	if len(limiter.entries) != 0 {
		t.Fatalf("空规则不得建桶: %v", limiter.entries)
	}
	// 非法规则（window/max <= 0）同样跳过。
	if decision := limiter.Consume("k", []RateLimitRule{{WindowSeconds: 0, MaxRequests: 5}, {WindowSeconds: 60, MaxRequests: 0}}); !decision.Allowed {
		t.Fatalf("非法规则必须跳过: %+v", decision)
	}
	if len(limiter.entries) != 0 {
		t.Fatalf("非法规则不得建桶: %v", limiter.entries)
	}
}

// TestWCPenaltyNewLimiterDefaults：NewPenaltyWindowLimiter 的 nil 时钟回退
// 与默认上限。
func TestWCPenaltyNewLimiterDefaults(t *testing.T) {
	limiter := NewPenaltyWindowLimiter(nil)
	if limiter.maxEntries != rateLimitMaxEntries || limiter.maxIdleMs != rateLimitMaxIdleMs || limiter.maxPenalty != rateLimitMaxPenaltyMs {
		t.Fatalf("默认参数错误: %+v", limiter)
	}
	if limiter.nowMs() <= 0 {
		t.Fatalf("nil 时钟必须回退 time.Now")
	}
	limiter2 := NewPenaltyWindowLimiter(func() time.Time { return time.UnixMilli(42) })
	if limiter2.nowMs() != 42 {
		t.Fatalf("注入时钟必须生效: %d", limiter2.nowMs())
	}
}

// TestWCDepsLimiterLazy：Deps.limiter 惰性构建且复用同一实例。
func TestWCDepsLimiterLazy(t *testing.T) {
	deps := &Deps{}
	first := deps.limiter()
	if first == nil || deps.rateLimiter != first {
		t.Fatalf("limiter 必须惰性构建并缓存")
	}
	if deps.limiter() != first {
		t.Fatalf("limiter 必须复用同一实例")
	}
}

// TestWCConsumeRedisDecisionFallbacks：Redis 拒绝结果缺 retry/rule 字段时
// 的回退（retry≥1、rule 取首个）。
func TestWCConsumeRedisDecisionFallbacks(t *testing.T) {
	fake := &penaltyFakeRedis{result: []any{int64(0)}}
	deps := &Deps{
		Now:              func() time.Time { return time.UnixMilli(1000) },
		RedisDriver:      true,
		RedisStateClient: fake,
		RedisNamespace:   "dev",
	}
	rules := []RateLimitRule{{WindowSeconds: 60, MaxRequests: 5}, {WindowSeconds: 3600, MaxRequests: 50}}
	decision := deps.consumeRateLimit(context.Background(), "scope", rules)
	if decision.Allowed {
		t.Fatalf("拒绝结果必须拒绝: %+v", decision)
	}
	// 共享驱动对缺失字段有自己的补齐（窗口剩余秒数 + 首个规则）；
	// aipublic 只保证 Retry-After 至少 1 秒且规则可投影。
	if decision.RetryAfterSeconds < 1 {
		t.Fatalf("Retry-After 至少 1 秒: %+v", decision)
	}
	if decision.Rule != (RateLimitRule{}) && decision.Rule != rules[0] && decision.Rule != rules[1] {
		t.Fatalf("规则必须来自请求的规则集: %+v", decision.Rule)
	}
}

// TestWCFirstRateLimitRule：空规则集的零值回退。
func TestWCFirstRateLimitRule(t *testing.T) {
	if got := firstRateLimitRule(nil); got != (RateLimitRule{}) {
		t.Fatalf("空规则回退零值: %+v", got)
	}
	rule := RateLimitRule{WindowSeconds: 5, MaxRequests: 6}
	if got := firstRateLimitRule([]RateLimitRule{rule}); got != rule {
		t.Fatalf("首个规则: %+v", got)
	}
}

// TestWCSharedRateLimitRules：aipublic 规则到共享驱动的 int64 投影。
func TestWCSharedRateLimitRules(t *testing.T) {
	converted := sharedRateLimitRules([]RateLimitRule{{WindowSeconds: 60, MaxRequests: 100}})
	if len(converted) != 1 || converted[0].WindowSeconds != 60 || converted[0].MaxRequests != 100 {
		t.Fatalf("规则投影: %+v", converted)
	}
}
