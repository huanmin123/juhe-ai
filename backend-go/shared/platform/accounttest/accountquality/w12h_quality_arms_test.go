package accountquality

// w12h 补充 arms：额度恢复策略归一化与边界计算、恢复 hint 提取、冷却复测
// runner 的错误/丢弃/顺延分支、precheck 丢弃与确认失败分支、零重试队列的
// 调度与生命周期、统计存储的校验与错误传播。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// quota.go：恢复策略与 hint

func TestW12HNormalizeQuotaRecoveryPolicy(t *testing.T) {
	// 空策略合法。
	if _, err := NormalizeQuotaRecoveryPolicy(map[string]any{}); err != nil {
		t.Fatalf("空策略必须合法: %v", err)
	}

	validDuration := map[string]any{"reset_strategy": "duration", "duration_minutes": float64(90), "timezone": "Asia/Shanghai"}
	validDaily := map[string]any{"reset_strategy": "daily", "daily_reset_hour": float64(3)}
	validWeekly := map[string]any{"reset_strategy": "weekly", "weekly_reset_day": float64(1), "weekly_reset_hour": float64(5), "jitter_minutes": float64(15)}
	policy, err := NormalizeQuotaRecoveryPolicy(map[string]any{
		"api_key": validDuration, "oauth": validDaily, "google_oauth": validWeekly,
	})
	if err != nil {
		t.Fatalf("合法策略必须通过: %v", err)
	}
	if policy.APIKey == nil || policy.OAuth == nil || policy.GoogleOAuth == nil {
		t.Fatalf("三键必须齐备: %+v", policy)
	}
	if *policy.APIKey.DurationMinutes != 90 || policy.APIKey.Timezone != "Asia/Shanghai" {
		t.Fatalf("duration 策略不符: %+v", policy.APIKey)
	}
	if *policy.GoogleOAuth.WeeklyResetDay != 1 || *policy.GoogleOAuth.WeeklyResetHour != 5 {
		t.Fatalf("weekly 策略不符: %+v", policy.GoogleOAuth)
	}

	invalid := []struct {
		name   string
		policy map[string]any
		want   string
	}{
		{"策略项不是对象", map[string]any{"api_key": "x"}, "必须是对象"},
		{"未知键", map[string]any{"other": validDuration}, "不受支持"},
		{"策略项错误传播", map[string]any{"api_key": map[string]any{"reset_strategy": "bogus"}}, "reset_strategy"},
		{"duration 越界", map[string]any{"api_key": map[string]any{"reset_strategy": "duration", "duration_minutes": float64(10)}}, "duration_minutes"},
		{"duration 非整数", map[string]any{"api_key": map[string]any{"reset_strategy": "duration", "duration_minutes": 1.5}}, "duration_minutes"},
		{"daily 越界", map[string]any{"oauth": map[string]any{"reset_strategy": "daily", "daily_reset_hour": float64(24)}}, "daily_reset_hour"},
		{"weekly day 越界", map[string]any{"oauth": map[string]any{"reset_strategy": "weekly", "weekly_reset_day": float64(7), "weekly_reset_hour": float64(0)}}, "weekly_reset_day"},
		{"weekly hour 越界", map[string]any{"oauth": map[string]any{"reset_strategy": "weekly", "weekly_reset_day": float64(0), "weekly_reset_hour": float64(-1)}}, "weekly_reset_hour"},
		{"jitter 不符", map[string]any{"oauth": map[string]any{"reset_strategy": "daily", "daily_reset_hour": float64(0), "jitter_minutes": float64(5)}}, "jitter_minutes"},
		{"timezone 非字符串", map[string]any{"oauth": map[string]any{"reset_strategy": "daily", "daily_reset_hour": float64(0), "timezone": 8}}, "timezone"},
		{"timezone 未知", map[string]any{"oauth": map[string]any{"reset_strategy": "daily", "daily_reset_hour": float64(0), "timezone": "Mars/Olympus"}}, "timezone"},
		{"timezone 空白", map[string]any{"oauth": map[string]any{"reset_strategy": "daily", "daily_reset_hour": float64(0), "timezone": "  "}}, "timezone"},
	}
	for _, test := range invalid {
		if _, err := NormalizeQuotaRecoveryPolicy(test.policy); err == nil || !strings.Contains(err.Error(), test.want) {
			t.Fatalf("%s: got %v", test.name, err)
		}
	}
}

func TestW12HQuotaRecoveryScheduleForAccount(t *testing.T) {
	// nil 策略回退默认。
	oauthDefault := QuotaRecoveryScheduleForAccount(nil, "oauth")
	if oauthDefault.ResetStrategy != StrategyDaily || oauthDefault.DailyResetHour == nil || *oauthDefault.DailyResetHour != 0 {
		t.Fatalf("oauth 默认不符: %+v", oauthDefault)
	}
	apiDefault := QuotaRecoveryScheduleForAccount(nil, "api_key")
	if apiDefault.ResetStrategy != StrategyDuration || apiDefault.DurationMinutes == nil || *apiDefault.DurationMinutes != 60 {
		t.Fatalf("api_key 默认不符: %+v", apiDefault)
	}

	// 配置项逐字段合并。
	duration := 120
	tz := "Asia/Tokyo"
	policy := &QuotaRecoveryPolicy{APIKey: &QuotaRecoverySchedule{ResetStrategy: StrategyDuration, DurationMinutes: &duration, Timezone: tz}}
	merged := QuotaRecoveryScheduleForAccount(policy, "api_key")
	if merged.ResetStrategy != StrategyDuration || merged.DurationMinutes == nil || *merged.DurationMinutes != 120 || merged.Timezone != tz {
		t.Fatalf("合并不符: %+v", merged)
	}
	// daily 配置覆盖 duration 默认的 strategy，但未配置字段沿用默认。
	daily := 8
	policy = &QuotaRecoveryPolicy{APIKey: &QuotaRecoverySchedule{ResetStrategy: StrategyDaily, DailyResetHour: &daily}}
	merged = QuotaRecoveryScheduleForAccount(policy, "api_key")
	if merged.ResetStrategy != StrategyDaily || merged.DailyResetHour == nil || *merged.DailyResetHour != 8 || merged.DurationMinutes == nil || *merged.DurationMinutes != 60 {
		t.Fatalf("daily 覆盖不符: %+v", merged)
	}
	// weekly 字段合并。
	day, hour := 2, 9
	policy = &QuotaRecoveryPolicy{OAuth: &QuotaRecoverySchedule{ResetStrategy: StrategyWeekly, WeeklyResetDay: &day, WeeklyResetHour: &hour}}
	merged = QuotaRecoveryScheduleForAccount(policy, "oauth")
	if merged.WeeklyResetDay == nil || *merged.WeeklyResetDay != 2 || merged.WeeklyResetHour == nil || *merged.WeeklyResetHour != 9 {
		t.Fatalf("weekly 合并不符: %+v", merged)
	}
}

func TestW12HQuotaRecoveryCooldownUntilStrategies(t *testing.T) {
	now := time.Date(2026, 9, 17, 10, 30, 0, 0, time.UTC) // 周四

	// duration：边界 = now + minutes。
	durationPolicy := &QuotaRecoveryPolicy{APIKey: &QuotaRecoverySchedule{ResetStrategy: StrategyDuration, DurationMinutes: intPtr(30)}}
	until := QuotaRecoveryCooldownUntil(durationPolicy, "api_key", "seed-1", now)
	parsed, err := time.Parse(time.RFC3339, until)
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := parsed.Sub(now); elapsed < 15*time.Minute || elapsed > 30*time.Minute {
		t.Fatalf("duration 边界应落在 [15m,30m]: %s", elapsed)
	}

	// daily：UTC 12 点边界（含 ±抖动窗口的确定性偏移）。
	dailyPolicy := &QuotaRecoveryPolicy{OAuth: &QuotaRecoverySchedule{ResetStrategy: StrategyDaily, DailyResetHour: intPtr(12)}}
	until = QuotaRecoveryCooldownUntil(dailyPolicy, "oauth", "seed-2", now)
	parsed, _ = time.Parse(time.RFC3339, until)
	boundary := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	if drift := parsed.Sub(boundary); drift < -31*time.Minute || drift > 31*time.Minute {
		t.Fatalf("daily 边界应贴近 12:00±抖动: %v (drift %s)", parsed, drift)
	}

	// weekly：下周二 8 点边界（本周四已过周二 → 下周二），偏移只向后半小时内。
	weeklyPolicy := &QuotaRecoveryPolicy{OAuth: &QuotaRecoverySchedule{ResetStrategy: StrategyWeekly, WeeklyResetDay: intPtr(2), WeeklyResetHour: intPtr(8)}}
	until = QuotaRecoveryCooldownUntil(weeklyPolicy, "oauth", "seed-3", now)
	parsed, _ = time.Parse(time.RFC3339, until)
	weeklyBoundary := time.Date(2026, 9, 22, 8, 0, 0, 0, time.UTC)
	if drift := parsed.Sub(weeklyBoundary); drift < -61*time.Minute || drift > 61*time.Minute {
		t.Fatalf("weekly 边界应贴近下周二 8 点±1h 抖动: %v (drift %s)", parsed, drift)
	}

	// 时区感知：Asia/Shanghai 12 点 = UTC 04 点。
	tzPolicy := &QuotaRecoveryPolicy{OAuth: &QuotaRecoverySchedule{ResetStrategy: StrategyDaily, DailyResetHour: intPtr(12), Timezone: "Asia/Shanghai"}}
	until = QuotaRecoveryCooldownUntil(tzPolicy, "oauth", "seed-4", now)
	parsed, _ = time.Parse(time.RFC3339, until)
	tzBoundary := time.Date(2026, 9, 18, 4, 0, 0, 0, time.UTC)
	if drift := parsed.Sub(tzBoundary); drift < -31*time.Minute || drift > 31*time.Minute {
		t.Fatalf("时区边界应贴近次日 UTC 04:00±抖动: %v (drift %s)", parsed, drift)
	}

	// 未知时区回退 UTC（scheduleBoundary 的 LoadLocation 错误分支）；
	// 今天 12:00 UTC 尚未过 → 边界为今天 12:00±抖动。
	badPolicy := &QuotaRecoveryPolicy{OAuth: &QuotaRecoverySchedule{ResetStrategy: StrategyDaily, DailyResetHour: intPtr(12), Timezone: "Nowhere/None"}}
	until = QuotaRecoveryCooldownUntil(badPolicy, "oauth", "seed-5", now)
	parsed, _ = time.Parse(time.RFC3339, until)
	todayBoundary := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	if drift := parsed.Sub(todayBoundary); drift < -31*time.Minute || drift > 31*time.Minute {
		t.Fatalf("未知时区应回退 UTC 今日 12:00±抖动: %v (drift %s)", parsed, drift)
	}

	// 空 seed 的通用 API Key 冷却。
	until = GenericAPIKeyQuotaCooldownUntil(nil, "", now)
	if _, err := time.Parse(time.RFC3339, until); err != nil {
		t.Fatalf("通用冷却时间不可解析: %v", err)
	}
}

func intPtr(v int) *int { return &v }

func TestW12HParseRetryAfterArms(t *testing.T) {
	now := time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)
	if got := parseRetryAfter("", now); got != nil {
		t.Fatalf("空值必须为 nil: %v", got)
	}
	if got := parseRetryAfter("120", now); got == nil || !got.Equal(now.Add(2*time.Minute)) {
		t.Fatalf("秒数不符: %v", got)
	}
	if got := parseRetryAfter("2026-09-17T11:00:00Z", now); got == nil || !got.Equal(time.Date(2026, 9, 17, 11, 0, 0, 0, time.UTC)) {
		t.Fatalf("RFC3339 绝对时间不符: %v", got)
	}
	httpDate := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	if got := parseRetryAfter(httpDate.Format(time.RFC1123), now); got == nil || !got.Equal(httpDate) {
		t.Fatalf("HTTP 日期不符: %v", got)
	}
	if got := parseRetryAfter("garbage", now); got != nil {
		t.Fatalf("垃圾文本必须为 nil: %v", got)
	}
}

func TestW12HExtractQuotaRecoveryHintArms(t *testing.T) {
	now := time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)
	// 绝对 reset_at（毫秒数字）。
	hint := ExtractQuotaRecoveryHint(`{"error":{"reset_at":1789700400000}}`, nil, now)
	if hint == nil || hint.Mode != QuotaRecoveryExplicitReset || hint.Source != HintSourceResetAt {
		t.Fatalf("reset_at hint 不符: %+v", hint)
	}
	// 秒级数字字符串。
	hint = ExtractQuotaRecoveryHint(`{"resetAt":"1789700400"}`, nil, now)
	if hint == nil || hint.Source != HintSourceResetAt {
		t.Fatalf("resetAt 秒级 hint 不符: %+v", hint)
	}
	// 过去的 reset_at → 无 hint。
	hint = ExtractQuotaRecoveryHint(`{"reset_at":1000}`, nil, now)
	if hint != nil {
		t.Fatalf("过去 reset_at 必须无 hint: %+v", hint)
	}
	// delay 秒字段。
	hint = ExtractQuotaRecoveryHint(`{"retryAfterSeconds":90}`, nil, now)
	if hint == nil || hint.CooldownUntil == "" {
		t.Fatalf("delay hint 不符: %+v", hint)
	}
	// retry-after 头。
	hint = ExtractQuotaRecoveryHint(`{"error":{}}`, map[string]string{"Retry-After": "60"}, now)
	if hint == nil || hint.Source != HintSourceRetryAfter {
		t.Fatalf("retry-after hint 不符: %+v", hint)
	}
	// 供应商 reset 头。
	hint = ExtractQuotaRecoveryHint(`{}`, map[string]string{"X-RateLimit-Reset": fmt.Sprintf("%d", now.Add(time.Hour).UnixMilli())}, now)
	if hint == nil || hint.Source != HintSourceProviderHeader {
		t.Fatalf("provider header hint 不符: %+v", hint)
	}
	// 非法 JSON、无字段 → nil。
	if got := ExtractQuotaRecoveryHint("not-json", nil, now); got != nil {
		t.Fatalf("非法 JSON 必须无 hint: %+v", got)
	}
	if got := ExtractQuotaRecoveryHint(`{"unrelated":1}`, nil, now); got != nil {
		t.Fatalf("无字段必须无 hint: %+v", got)
	}
	// 嵌套对象字段搜索。
	hint = ExtractQuotaRecoveryHint(`{"error":{"details":{"reset_after_seconds":30}}}`, nil, now)
	if hint == nil {
		t.Fatal("嵌套 delay 必须命中")
	}
}

func TestW12HParseAbsoluteRecoveryTimeAndHelpers(t *testing.T) {
	// float 秒（< 1e10 → *1000）。
	seconds := parseAbsoluteRecoveryTime(float64(1789700400))
	if seconds == nil || seconds.Unix() != 1789700400 {
		t.Fatalf("float 秒不符: %v", seconds)
	}
	// float 毫秒。
	ms := parseAbsoluteRecoveryTime(float64(1789700400000))
	if ms == nil || ms.UnixMilli() != 1789700400000 {
		t.Fatalf("float 毫秒不符: %v", ms)
	}
	// 非正数 → nil。
	if parseAbsoluteRecoveryTime(float64(0)) != nil || parseAbsoluteRecoveryTime(float64(-5)) != nil {
		t.Fatal("非正数必须为 nil")
	}
	// 字符串形态。
	if parseAbsoluteRecoveryTime("") != nil || parseAbsoluteRecoveryTime("  ") != nil {
		t.Fatal("空白必须为 nil")
	}
	if parseAbsoluteRecoveryTime("abc") != nil {
		t.Fatal("非时间文本必须为 nil")
	}
	canonical, ok := canonicalizeRFC3339("2026-09-17T10:00:00Z")
	if !ok || canonical == "" {
		t.Fatalf("canonicalizeRFC3339 失败: %q %v", canonical, ok)
	}
	if _, ok := canonicalizeRFC3339("17/09/2026"); ok {
		t.Fatal("非 RFC3339 必须失败")
	}
	parsed := parseAbsoluteRecoveryTime("2026-09-17T10:00:00Z")
	if parsed == nil || parsed.UTC().Hour() != 10 {
		t.Fatalf("RFC3339 解析不符: %v", parsed)
	}
	// 布尔/其他类型 → nil。
	if parseAbsoluteRecoveryTime(true) != nil {
		t.Fatal("布尔必须为 nil")
	}
	// parsePositiveSeconds。
	if parsePositiveSeconds(nil) != nil || parsePositiveSeconds(float64(-1)) != nil || parsePositiveSeconds("") != nil || parsePositiveSeconds("abc") != nil || parsePositiveSeconds(0.4) == nil {
		t.Fatal("parsePositiveSeconds 边界不符")
	}
	if got := parsePositiveSeconds("45.2"); got == nil || *got != 46 {
		t.Fatalf("字符串秒向上取整不符: %v", got)
	}
}

func TestW12HQuotaObservationAndErrorCodes(t *testing.T) {
	if APIKeyQuotaObservationExceeded("", time.Now()) {
		t.Fatal("空 recoveryStartedAt 必须为 false")
	}
	if APIKeyQuotaObservationExceeded("not-a-time", time.Now()) {
		t.Fatal("非法时间必须为 false")
	}
	observedAt := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	if !APIKeyQuotaObservationExceeded("2026-09-01T00:00:00Z", observedAt) {
		t.Fatal("超过观察窗口必须为 true")
	}
	if APIKeyQuotaObservationExceeded("2026-10-01T00:00:00Z", observedAt) {
		t.Fatal("窗口内必须为 false")
	}
	if mode, ok := APIKeyQuotaRecoveryModeFromErrorCode(QuotaRecoveryGenericErrorCode); !ok || mode != QuotaRecoveryGeneric {
		t.Fatalf("generic 错误码不符: %v %v", mode, ok)
	}
	if mode, ok := APIKeyQuotaRecoveryModeFromErrorCode(QuotaRecoveryExplicitErrorCode); !ok || mode != QuotaRecoveryExplicitReset {
		t.Fatalf("explicit 错误码不符: %v %v", mode, ok)
	}
	if _, ok := APIKeyQuotaRecoveryModeFromErrorCode("other"); ok {
		t.Fatal("未知错误码必须失败")
	}
	if QuotaRecoveryErrorCode(QuotaRecoveryExplicitReset) != QuotaRecoveryExplicitErrorCode {
		t.Fatal("explicit 错误码映射不符")
	}
	if QuotaRecoveryErrorCode(QuotaRecoveryGeneric) != QuotaRecoveryGenericErrorCode {
		t.Fatal("generic 错误码映射不符")
	}
	// 错误标识归一化的空白折叠。
	if normalizeErrorIdentifier(" Insufficient-User  Quota") != "insufficient_user_quota" {
		t.Fatalf("归一化不符: %q", normalizeErrorIdentifier(" Insufficient-User  Quota"))
	}
	// SystemInsufficientQuotaRuleMatches 的 402 空码与文本标记臂。
	if !SystemInsufficientQuotaRuleMatches(402, "", "", "") {
		t.Fatal("402 空码必须命中")
	}
	if SystemInsufficientQuotaRuleMatches(403, "", "", "content policy violation detected") {
		t.Fatal("内容策略文本必须排除")
	}
	if !SystemInsufficientQuotaRuleMatches(402, "", "", "wallet balance exhausted") {
		t.Fatal("额度文本必须命中")
	}
	if SystemInsufficientQuotaRuleMatches(500, "quota_exceeded", "", "") {
		t.Fatal("非 402/403 必须不命中")
	}
	if SystemInsufficientQuotaRuleMatches(403, "forbidden", "", "") {
		t.Fatal("非 quota 403 标识必须排除")
	}
	if !SystemInsufficientQuotaRuleMatches(403, "Billing-Hard-Limit Reached", "", "") {
		t.Fatal("归一化后的稳定码必须命中")
	}
	// DeterministicOffsetMs 的窗口与确定性。
	offset1 := DeterministicOffsetMs(60_000, "seed")
	if offset1 == 0 || offset1 > 30_000 || offset1 < -30_000 {
		t.Fatalf("偏移超出窗口: %d", offset1)
	}
	if offset1 != DeterministicOffsetMs(60_000, "seed") {
		t.Fatal("同 seed 必须确定")
	}
	if DeterministicOffsetMs(0, "seed") != 0 {
		t.Fatal("零窗口必须为 0")
	}
	// UTF-16 代理对码元。
	if got := utf16CodeUnits("\U0001F600"); len(got) != 2 {
		t.Fatalf("代理对必须拆成两个码元: %v", got)
	}
	// JitterWindowMs 各档。
	cases := []struct {
		interval, want int64
	}{
		{0, 0}, {1000, 500}, {120_000, 30_000}, {7_200_000, 1_800_000},
		{172_800_000, 3_600_000}, {30 * 86_400_000, 8 * 3_600_000},
	}
	for _, test := range cases {
		if got := JitterWindowMs(test.interval); got != test.want {
			t.Fatalf("JitterWindowMs(%d)=%d, want %d", test.interval, got, test.want)
		}
	}
}

// ---------------------------------------------------------------------------
// cooldown runner 追加分支

type w12hFailingCandidates struct {
	err error
}

func (m *w12hFailingCandidates) ListDueForProbe(ctx context.Context, limit int) ([]CooldownProbeCandidate, error) {
	return nil, m.err
}

type w12hFailingCooldownMutation struct {
	successErr error
	failErr    error
	deferErr   error
	failures   int
	deferrals  int
	lastDefer  *KeyDeferInput
	lastFail   *KeyFailureInput
}

func (m *w12hFailingCooldownMutation) RecordKeySuccess(ctx context.Context, input KeySuccessInput) (KeyMutationResult, error) {
	if m.successErr != nil {
		return KeyMutationResult{}, m.successErr
	}
	return KeyMutationResult{Changed: true}, nil
}
func (m *w12hFailingCooldownMutation) RecordKeyFailure(ctx context.Context, input KeyFailureInput) (KeyMutationResult, error) {
	m.failures++
	captured := input
	m.lastFail = &captured
	if m.failErr != nil {
		return KeyMutationResult{}, m.failErr
	}
	return KeyMutationResult{Changed: true}, nil
}
func (m *w12hFailingCooldownMutation) DeferKeyProbe(ctx context.Context, input KeyDeferInput) (KeyMutationResult, error) {
	m.deferrals++
	captured := input
	m.lastDefer = &captured
	if m.deferErr != nil {
		return KeyMutationResult{}, m.deferErr
	}
	return KeyMutationResult{Changed: true}, nil
}

type w12hErrReader struct {
	findErr    error
	groupErr   error
	keyErr     error
	account    *AccountForTest
	candidate  *OpenAIAccountCandidate
	keyPresent bool
}

func (r *w12hErrReader) FindAccountForTest(ctx context.Context, accountID string) (*AccountForTest, error) {
	if r.findErr != nil {
		return nil, r.findErr
	}
	return r.account, nil
}
func (r *w12hErrReader) FindAccountForGroup(ctx context.Context, groupID, accountID, systemAccountID string) (*OpenAIAccountCandidate, error) {
	if r.groupErr != nil {
		return nil, r.groupErr
	}
	return r.candidate, nil
}
func (r *w12hErrReader) HasAPIKeyEntry(ctx context.Context, candidate *OpenAIAccountCandidate, fingerprint, apiKey string) (bool, error) {
	if r.keyErr != nil {
		return false, r.keyErr
	}
	return r.keyPresent, nil
}

func TestW12HCooldownRunnerErrorArms(t *testing.T) {
	logger := &fakeLogger{}
	baseReader := func() *w12hErrReader {
		return &w12hErrReader{
			account:    baseAccount("acc-1"),
			candidate:  dispatchCandidate("acc-1"),
			keyPresent: true,
		}
	}
	newRunner := func(reader AccountReader, prober Prober, mutation CooldownMutation) *CooldownRetestRunner {
		return NewCooldownRetestRunner(CooldownDeps{
			Logger: logger, Reader: reader, Prober: prober, Mutation: mutation,
			Settings: func(string, int, int) int { return 24 }, Concurrency: func() int { return 2 }, QueueWorkers: 1,
		})
	}

	t.Run("读取账户失败", func(t *testing.T) {
		reader := baseReader()
		reader.findErr = errors.New("w12h find failure")
		runner := newRunner(reader, &mockProber{}, &w12hFailingCooldownMutation{})
		runner.Enqueue(cooldownCandidate("acc-1", "fp-1", "sk-key"), 24)
		waitFor(t, func() bool { return logger.findByEvent("background_account_api_key_cooldown_retest_retry_exhausted") != nil })
	})

	t.Run("分组候选失败", func(t *testing.T) {
		reader := baseReader()
		reader.groupErr = errors.New("w12h group failure")
		runner := newRunner(reader, &mockProber{}, &w12hFailingCooldownMutation{})
		runner.Enqueue(cooldownCandidate("acc-1", "fp-1", "sk-key"), 24)
		waitFor(t, func() bool { return logger.findByEvent("background_account_api_key_cooldown_retest_retry_exhausted") != nil })
	})

	t.Run("凭据检查失败", func(t *testing.T) {
		reader := baseReader()
		reader.keyErr = errors.New("w12h key failure")
		runner := newRunner(reader, &mockProber{}, &w12hFailingCooldownMutation{})
		runner.Enqueue(cooldownCandidate("acc-1", "fp-1", "sk-key"), 24)
		waitFor(t, func() bool { return logger.findByEvent("background_account_api_key_cooldown_retest_retry_exhausted") != nil })
	})

	t.Run("系统账户缺失", func(t *testing.T) {
		reader := baseReader()
		account := baseAccount("acc-1")
		account.OwnerSystemAccountID = ""
		account.SystemAccountID = ""
		reader.account = account
		runner := newRunner(reader, &mockProber{}, &w12hFailingCooldownMutation{})
		runner.Enqueue(cooldownCandidate("acc-1", "fp-1", "sk-key"), 24)
		waitFor(t, func() bool { return !runner.queue.HasKey("acc-1:fp-1") })
	})

	t.Run("分组候选类型不符", func(t *testing.T) {
		reader := baseReader()
		reader.candidate = &OpenAIAccountCandidate{ID: "acc-1", Type: "oauth"}
		runner := newRunner(reader, &mockProber{}, &w12hFailingCooldownMutation{})
		runner.Enqueue(cooldownCandidate("acc-1", "fp-1", "sk-key"), 24)
		waitFor(t, func() bool { return !runner.queue.HasKey("acc-1:fp-1") })
	})

	t.Run("探针异常", func(t *testing.T) {
		runner := newRunner(baseReader(), &mockProber{err: errors.New("w12h probe failure")}, &w12hFailingCooldownMutation{})
		runner.Enqueue(cooldownCandidate("acc-1", "fp-1", "sk-key"), 24)
		waitFor(t, func() bool { return logger.findByEvent("background_account_api_key_cooldown_retest_retry_exhausted") != nil })
	})

	t.Run("诊断结果缺失", func(t *testing.T) {
		runner := newRunner(baseReader(), &mockProber{}, &w12hFailingCooldownMutation{})
		runner.Enqueue(cooldownCandidate("acc-1", "fp-1", "sk-key"), 24)
		waitFor(t, func() bool {
			return logger.findByEvent("background_account_api_key_cooldown_retest_missing_diagnostic_result") != nil
		})
	})

	t.Run("恢复成功写回失败", func(t *testing.T) {
		mutation := &w12hFailingCooldownMutation{successErr: errors.New("w12h success failure")}
		runner := newRunner(baseReader(), &mockProber{observation: successObservation()}, mutation)
		runner.Enqueue(cooldownCandidate("acc-1", "fp-1", "sk-key"), 24)
		waitFor(t, func() bool { return logger.findByEvent("background_account_api_key_cooldown_retest_retry_exhausted") != nil })
	})

	t.Run("额度失败写回失败", func(t *testing.T) {
		mutation := &w12hFailingCooldownMutation{failErr: errors.New("w12h fail failure")}
		status := 402
		prober := &mockProber{observation: &ProbeObservation{
			Result:   ProbeResult{Success: false, StatusCode: &status, Message: "insufficient quota"},
			Evidence: ProbeEvidence{HasRealUpstreamAttempt: true, UpstreamCompleted: true, UpstreamStatus: 402},
		}}
		runner := newRunner(baseReader(), prober, mutation)
		runner.Enqueue(cooldownCandidate("acc-1", "fp-1", "sk-key"), 24)
		waitFor(t, func() bool { return logger.findByEvent("background_account_api_key_cooldown_retest_retry_exhausted") != nil })
	})

	t.Run("顺延写回失败", func(t *testing.T) {
		mutation := &w12hFailingCooldownMutation{deferErr: errors.New("w12h defer failure")}
		prober := &mockProber{observation: &ProbeObservation{Result: ProbeResult{}, Evidence: ProbeEvidence{}}}
		runner := newRunner(baseReader(), prober, mutation)
		runner.Enqueue(cooldownCandidate("acc-1", "fp-1", "sk-key"), 24)
		waitFor(t, func() bool { return logger.findByEvent("background_account_api_key_cooldown_retest_retry_exhausted") != nil })
	})
}

func TestW12HCooldownQuotaMessageSwitches(t *testing.T) {
	logger := &fakeLogger{}
	status := 402
	prober := &mockProber{observation: &ProbeObservation{
		Result:   ProbeResult{Success: false, StatusCode: &status, Message: "insufficient quota"},
		Evidence: ProbeEvidence{HasRealUpstreamAttempt: true, UpstreamCompleted: true, UpstreamStatus: 402},
	}}
	mutation := &w12hFailingCooldownMutation{}
	reader := cooldownReader()
	// 上次为 explicit_reset、本次无 hint → 切换通用观察窗口文案。
	candidate := cooldownCandidate("acc-1", "fp-1", "sk-key")
	candidate.LastErrorCode = QuotaRecoveryExplicitErrorCode
	runner := NewCooldownRetestRunner(CooldownDeps{
		Logger: logger, Reader: reader, Prober: prober, Mutation: mutation,
		Settings: func(string, int, int) int { return 24 }, Concurrency: func() int { return 2 }, QueueWorkers: 1,
	})
	runner.Enqueue(candidate, 24)
	waitFor(t, func() bool {
		event := logger.findByEvent("background_account_api_key_quota_retest_failed")
		return event != nil && strings.Contains(event.message, "已切换通用 30 天额度观察窗口")
	})
	if mutation.lastFail == nil || mutation.lastFail.QuotaRecoveryMode != string(QuotaRecoveryGeneric) {
		t.Fatalf("应记录 generic 模式: %+v", mutation.lastFail)
	}
}

func TestW12HCooldownTransportWithQuotaWindowDefers(t *testing.T) {
	logger := &fakeLogger{}
	// 额度失败 + 传输不完整 → 不形成额度结论 → defer 且不累计确认。
	// 结果不得带 StatusCode：带状态码时结果归为 framing_complete_neutral。
	// UpstreamStatus 提供额度判定所需的 402，结果不带 StatusCode 保持
	// upstream_failure 结论。
	prober := &mockProber{observation: &ProbeObservation{
		Result:   ProbeResult{Success: false, Message: "insufficient quota"},
		Evidence: ProbeEvidence{HasRealUpstreamAttempt: true, UpstreamStatus: 402, TransportFailureKind: TransportFailureConnection},
	}}
	mutation := &w12hFailingCooldownMutation{}
	runner := NewCooldownRetestRunner(CooldownDeps{
		Logger: logger, Reader: cooldownReader(), Prober: prober, Mutation: mutation,
		Settings: func(string, int, int) int { return 24 }, Concurrency: func() int { return 2 }, QueueWorkers: 1,
	})
	candidate := cooldownCandidate("acc-1", "fp-1", "sk-key")
	candidate.LastErrorCode = QuotaRecoveryGenericErrorCode
	runner.Enqueue(candidate, 24)
	waitFor(t, func() bool {
		return logger.findByEvent("background_account_api_key_quota_retest_failed") != nil
	})
	// HasRecoveryMode 只在额度失败时为真，额度分支先于（已删除的）传输
	// 顺延分支命中：传输不完整的额度复测按通用恢复间隔记录失败。
	if mutation.lastFail == nil || mutation.lastFail.QuotaRecoveryMode != string(QuotaRecoveryGeneric) {
		t.Fatalf("应记录 generic 恢复模式: %+v", mutation.lastFail)
	}
	if mutation.lastFail.Status != AccountStatusRateLimited {
		t.Fatalf("非超时额度失败应记录 rate_limited: %+v", mutation.lastFail)
	}

	// 非额度中性结果 + 前次额度模式 → 默认间隔顺延且断开恢复窗口。
	// （HasRecoveryMode 只在额度失败时为真，额度失败已在上方分支返回。）
	logger2 := &fakeLogger{}
	mutation2 := &w12hFailingCooldownMutation{}
	prober2 := &mockProber{observation: &ProbeObservation{Result: ProbeResult{}, Evidence: ProbeEvidence{}}}
	reader2 := cooldownReader()
	runner2 := NewCooldownRetestRunner(CooldownDeps{
		Logger: logger2, Reader: reader2, Prober: prober2, Mutation: mutation2,
		Settings: func(string, int, int) int { return 24 }, Concurrency: func() int { return 2 }, QueueWorkers: 1,
	})
	runner2.Enqueue(candidate, 24)
	waitFor(t, func() bool {
		event := logger2.findByEvent("background_account_api_key_cooldown_retest_task_failed")
		return event != nil
	})
	if mutation2.lastDefer == nil {
		t.Fatal("中性结果必须顺延")
	}
	if mutation2.lastDefer.DelaySeconds != CooldownDefaultDeferSeconds {
		t.Fatalf("非额度中性结果固定默认顺延 60s: %+v", mutation2.lastDefer)
	}
	if !mutation2.lastDefer.BreakQuotaRecoveryWindow {
		t.Fatalf("前次额度模式应断开恢复窗口: %+v", mutation2.lastDefer)
	}
	if mutation2.lastDefer.QuotaRecoveryMode != "" {
		t.Fatalf("非额度结果不得带恢复模式: %+v", mutation2.lastDefer)
	}
}

func TestW12HCooldownScanErrorAndLifecycle(t *testing.T) {
	logger := &fakeLogger{}
	runner := NewCooldownRetestRunner(CooldownDeps{
		Logger: logger, Reader: cooldownReader(), Prober: &mockProber{},
		Mutation:  &w12hFailingCooldownMutation{},
		Candidates: &w12hFailingCandidates{err: errors.New("w12h list failure")},
		Settings:   func(string, int, int) int { return 24 }, Concurrency: func() int { return 2 }, QueueWorkers: 1,
	})
	if err := runner.Scan(context.Background()); err == nil || !strings.Contains(err.Error(), "w12h list failure") {
		t.Fatalf("候选读取失败必须上抛: %v", err)
	}

	// 无可用槽位 → 直接返回，不触达候选源。
	busy := NewCooldownRetestRunner(CooldownDeps{
		Logger: logger, Reader: cooldownReader(), Prober: &mockProber{}, Mutation: &w12hFailingCooldownMutation{},
		Candidates: &w12hFailingCandidates{err: errors.New("w12h should not list")},
		Settings:   func(string, int, int) int { return 24 }, Concurrency: func() int { return 1 }, QueueWorkers: 1,
	})
	busy.queue.mu.Lock()
	busy.queue.items["occupied"] = &retryQueueItem[CooldownQueueItem]{key: "occupied", nextRunAtMs: 1}
	busy.queue.items["occupied2"] = &retryQueueItem[CooldownQueueItem]{key: "occupied2", nextRunAtMs: 1, running: true}
	busy.queue.mu.Unlock()
	if err := busy.Scan(context.Background()); err != nil {
		t.Fatalf("无槽位应静默返回: %v", err)
	}

	// Snapshot 与 StopAndDrain 生命周期。
	runner.Enqueue(cooldownCandidate("acc-1", "fp-9", "sk-key"), 24)
	if runner.Snapshot().Name != CooldownQueueName {
		t.Fatalf("快照名不符: %+v", runner.Snapshot())
	}
	if drained, active := runner.StopAndDrain(2 * time.Second); !drained || active != 0 {
		t.Fatalf("停机排空不符: %v %d", drained, active)
	}
	// 停止后拒绝入队。
	if runner.Enqueue(cooldownCandidate("acc-2", "fp-1", "sk-key"), 24) {
		t.Fatal("停止后必须拒绝入队")
	}
}

// ---------------------------------------------------------------------------
// precheck 追加分支

func TestW12HPrecheckErrorAndDiscardArms(t *testing.T) {
	logger := &fakeLogger{}
	prober := &mockProber{}

	t.Run("读取账户失败", func(t *testing.T) {
		reader := &w12hErrReader{findErr: errors.New("w12h precheck find failure")}
		runner := NewPrecheckRunner(PrecheckDeps{Logger: logger, Reader: reader, Prober: prober, Mutation: &mockPrecheckMutation{}, Concurrency: 1})
		runner.Enqueue(precheckCandidate("acc-1"))
		waitFor(t, func() bool { return logger.findByEvent("background_account_quality_failure_precheck_exhausted") != nil })
	})

	t.Run("分组候选失败", func(t *testing.T) {
		reader := &w12hErrReader{
			findErr: nil, groupErr: errors.New("w12h precheck group failure"),
			account: baseAccount("acc-1"),
		}
		runner := NewPrecheckRunner(PrecheckDeps{Logger: logger, Reader: reader, Prober: prober, Mutation: &mockPrecheckMutation{}, Concurrency: 1})
		runner.Enqueue(precheckCandidate("acc-1"))
		waitFor(t, func() bool { return logger.findByEvent("background_account_quality_failure_precheck_exhausted") != nil })
	})

	t.Run("缺少调度代次", func(t *testing.T) {
		reader := &mockReader{
			accounts: map[string]*AccountForTest{"acc-1": baseAccount("acc-1")},
			group:    map[string]*OpenAIAccountCandidate{"acc-1": {ID: "acc-1", Type: "api_key", Status: AccountStatusActive}},
		}
		runner := NewPrecheckRunner(PrecheckDeps{Logger: logger, Reader: reader, Prober: prober, Mutation: &mockPrecheckMutation{}, Concurrency: 1})
		runner.Enqueue(precheckCandidate("acc-1"))
		waitFor(t, func() bool {
			return logger.findByEvent("background_account_quality_failure_precheck_discarded") != nil
		})
	})

	t.Run("Key 池探针异常", func(t *testing.T) {
		reader := &mockReader{
			accounts: map[string]*AccountForTest{"acc-1": baseAccount("acc-1")},
			group:    map[string]*OpenAIAccountCandidate{"acc-1": dispatchCandidate("acc-1")},
		}
		runner := NewPrecheckRunner(PrecheckDeps{
			Logger: logger, Reader: reader, Prober: &mockProber{err: errors.New("w12h pool failure")},
			Mutation: &mockPrecheckMutation{}, Concurrency: 1,
		})
		runner.Enqueue(precheckCandidate("acc-1"))
		waitFor(t, func() bool {
			return logger.findByEvent("background_account_quality_failure_precheck_api_key_pool_attempt_failed") != nil
		})
	})

	t.Run("标记写回失败", func(t *testing.T) {
		reader := &mockReader{
			accounts: map[string]*AccountForTest{"acc-1": baseAccount("acc-1")},
			group:    map[string]*OpenAIAccountCandidate{"acc-1": dispatchCandidate("acc-1")},
		}
		// framing_complete_neutral 形成可用性失败结论 → 触发标记写回。
		status := 402
		markingProber := &mockProber{observation: &ProbeObservation{
			Result:   ProbeResult{Success: false, StatusCode: &status},
			Evidence: ProbeEvidence{HasRealUpstreamAttempt: true, UpstreamCompleted: true, UpstreamStatus: 402},
		}}
		runner := NewPrecheckRunner(PrecheckDeps{Logger: logger, Reader: reader, Prober: markingProber, Mutation: &w12hFailingPrecheckMutation{}, Concurrency: 1})
		runner.Enqueue(precheckCandidate("acc-1"))
		waitFor(t, func() bool { return logger.findByEvent("background_account_quality_failure_precheck_exhausted") != nil })
	})

	t.Run("无效失败结论跳过写回", func(t *testing.T) {
		reader := &mockReader{
			accounts: map[string]*AccountForTest{"acc-1": baseAccount("acc-1")},
			group:    map[string]*OpenAIAccountCandidate{"acc-1": dispatchCandidate("acc-1")},
		}
		// 无上游尝试 → task_failure（非可用性失败结论）→ 跳过写回。
		runner := NewPrecheckRunner(PrecheckDeps{
			Logger: logger, Reader: reader, Prober: &mockProber{observation: &ProbeObservation{}},
			Mutation: &mockPrecheckMutation{}, Concurrency: 1,
		})
		runner.Enqueue(precheckCandidate("acc-1"))
		waitFor(t, func() bool {
			return logger.findByEvent("background_account_quality_failure_precheck_ineligible_failure_discarded") != nil
		})
	})
}

type w12hFailingPrecheckMutation struct{}

func (m *w12hFailingPrecheckMutation) MarkPrecheckTemporaryUnavailable(ctx context.Context, input PrecheckMutationInput) (PrecheckMutationResult, error) {
	return PrecheckMutationResult{}, errors.New("w12h precheck mutation failure")
}

func TestW12HIsPrecheckEligibleArms(t *testing.T) {
	now := time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)
	if isPrecheckEligible(nil, now) {
		t.Fatal("nil 账户必须不合格")
	}
	disabled := baseAccount("acc-1")
	disabled.Status = AccountStatusError
	if isPrecheckEligible(disabled, now) {
		t.Fatal("非 active 必须不合格")
	}
	unschedulable := baseAccount("acc-1")
	unschedulable.Schedulable = false
	if isPrecheckEligible(unschedulable, now) {
		t.Fatal("不可调度必须不合格")
	}
	noGroup := baseAccount("acc-1")
	noGroup.BoundGroupID = ""
	if isPrecheckEligible(noGroup, now) {
		t.Fatal("无分组必须不合格")
	}
	expired := baseAccount("acc-1")
	expired.AccountExpiresAt = "2026-09-01T00:00:00.000Z"
	if isPrecheckEligible(expired, now) {
		t.Fatal("已过期必须不合格")
	}
	valid := baseAccount("acc-1")
	valid.AccountExpiresAt = "2026-10-01T00:00:00.000Z"
	if !isPrecheckEligible(valid, now) {
		t.Fatal("未过期必须合格")
	}
	unavailable := baseAccount("acc-1")
	unavailable.HasEffectiveAvail = true
	unavailable.EffectiveAvailable = false
	if isPrecheckEligible(unavailable, now) {
		t.Fatal("有效可用性为否必须不合格")
	}
}

func TestW12HPrecheckReasonArms(t *testing.T) {
	rate := 0.256
	item := precheckCandidate("acc-1")
	item.SuccessRate = &rate
	status := 502
	result := ProbeResult{StatusCode: &status, ErrorCode: "upstream_500"}
	reason := precheckReason(item, result)
	for _, want := range []string{"近期质量频繁失败", "近窗口 10 次请求失败 8 次", "成功率 26%", "最后业务失败", "确认 HTTP 502", "upstream_500", "上游 502"} {
		if !strings.Contains(reason, want) {
			t.Fatalf("原因缺失 %q: %s", want, reason)
		}
	}
	// 结果无消息时回落 LastErrorMessage；超长按 rune 截断。
	long := strings.Repeat("长", 700)
	reason = precheckReason(FailurePrecheckCandidate{RecentRequestCount: 1, RecentErrorCount: 1, LastErrorMessage: long}, ProbeResult{})
	if len([]rune(reason)) > 1000 {
		t.Fatalf("原因必须截断到 1000 码元: %d", len([]rune(reason)))
	}
}

// ---------------------------------------------------------------------------
// queue 追加分支

func TestW12HRetryQueueLifecycleArms(t *testing.T) {
	// 默认值收敛：concurrency<1 → 1，nil clock/logger 允许。
	queue := NewRetryQueue[CooldownQueueItem]("w12h-queue", 0, nil, nil,
		func(ctx context.Context, run QueueRunContext, item CooldownQueueItem) (bool, error) { return true, nil }, nil)
	if queue.concurrency != 1 {
		t.Fatalf("默认并发应为 1: %d", queue.concurrency)
	}
	if _, ok := queue.clock.(SystemClock); !ok {
		t.Fatal("nil clock 应回落 SystemClock")
	}

	// Delete/Clear。
	q := NewRetryQueue[string]("w12h-del", 1, nil, nil,
		func(ctx context.Context, run QueueRunContext, item string) (bool, error) { return true, nil }, nil)
	q.mu.Lock()
	q.items["k1"] = &retryQueueItem[string]{key: "k1", nextRunAtMs: 1}
	q.items["k2"] = &retryQueueItem[string]{key: "k2", nextRunAtMs: 1}
	q.mu.Unlock()
	q.Delete("k1")
	if q.HasKey("k1") || !q.HasKey("k2") {
		t.Fatal("Delete 语义不符")
	}
	q.Clear()
	if q.HasKey("k2") {
		t.Fatal("Clear 应清空队列")
	}

	// 未来到期项不出队，到期后 pump 出队。
	future := time.Now().Add(time.Hour).UnixMilli()
	ran := make(chan string, 1)
	scheduled := NewRetryQueue[string]("w12h-future", 1, nil, nil,
		func(ctx context.Context, run QueueRunContext, item string) (bool, error) {
			ran <- item
			return true, nil
		}, nil)
	scheduled.mu.Lock()
	scheduled.items["future-item"] = &retryQueueItem[string]{key: "future-item", item: "x", nextRunAtMs: future}
	scheduled.mu.Unlock()
	scheduled.pump()
	if len(ran) != 0 {
		t.Fatal("未来到期项不应出队")
	}
	if snap := scheduled.Snapshot(); snap.PendingCount != 1 || snap.NextRunAt == "" {
		t.Fatalf("快照应含未来项: %+v", snap)
	}
	scheduled.mu.Lock()
	scheduled.items["future-item"].nextRunAtMs = time.Now().Add(-time.Hour).UnixMilli()
	scheduled.mu.Unlock()
	scheduled.pump()
	select {
	case item := <-ran:
		if item != "x" {
			t.Fatalf("出队项不符: %s", item)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("到期项未出队")
	}
}

// ---------------------------------------------------------------------------
// refresh / score / types / minutekey

func TestW12HRefreshRunnerOffsetAndDefaults(t *testing.T) {
	store, _, lookup := newQualityStore(t)
	ctx := context.Background()
	lookup.accounts["acc-1"] = AccountMetadata{SystemAccountID: "sys-1", ProviderCode: "openai"}
	store.seedMinute(t, "acc-1", MinuteKey(time.Now().Add(-time.Minute), time.UTC), 10, 2, 8, 0, 0, "upstream 500")
	if err := store.MarkQualityDirty(ctx, "acc-1"); err != nil {
		t.Fatal(err)
	}

	logger := &fakeLogger{}
	precheck := NewPrecheckRunner(PrecheckDeps{Logger: logger, Reader: &mockReader{}, Prober: &mockProber{}, Mutation: &mockPrecheckMutation{}, Concurrency: 1})
	runner := NewRefreshRunner(RefreshDeps{
		Store: store, Logger: logger, Caches: &mockCacheInvalidator{}, Precheck: precheck,
		IngestGate: allowIngestGate{},
		Settings:   func(string, int, int) int { return 10 }, Concurrency: func() int { return 2 },
		PrecheckBatchSize: 1,
	})
	if err := runner.Run(ctx); err != nil {
		t.Fatal(err)
	}
	// 候选数 1 >= 批大小 1 → offset 前移而非归零。
	if runner.offset != 1 {
		t.Fatalf("候选满批时 offset 应前移: %d", runner.offset)
	}

	// nil logger 允许（NopLogger 兜底）。
	bare := NewRefreshRunner(RefreshDeps{
		Store: store, Caches: &mockCacheInvalidator{}, Precheck: precheck,
		IngestGate: allowIngestGate{}, Settings: func(string, int, int) int { return 10 },
	})
	if err := bare.Run(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestW12HComputeQualityScoreArms(t *testing.T) {
	now := time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)
	if got := ComputeQualityScore(nil, nil, QualityFresh, now, now); got != UnknownQualityScore {
		t.Fatalf("缺 EWMA 应回落 %d: %d", UnknownQualityScore, got)
	}
	// 负分钳制为 0。
	negative := int64(-999_999)
	if got := ComputeQualityScore(&negative, nil, QualityFailed, now, now); got != 0 {
		t.Fatalf("负分必须钳制 0: %d", got)
	}
	// 陈旧龄期罚分封顶。
	old := now.Add(-365 * 24 * time.Hour)
	if got := AgePenaltyMs(old, now); got != AgePenaltyCapMs {
		t.Fatalf("龄期罚分应封顶: %d", got)
	}
	if got := AgePenaltyMs(now, now); got != 0 {
		t.Fatalf("零龄期罚分应为 0: %d", got)
	}
	// 成功率钳制。
	rate := SuccessRateAfterWindow(4, 8, nil)
	if rate == nil || *rate != 1 {
		t.Fatalf("超额成功率必须钳制 1: %v", rate)
	}
	rate = SuccessRateAfterWindow(4, -8, nil)
	if rate == nil || *rate != 0 {
		t.Fatalf("负成功率必须钳制 0: %v", rate)
	}
	previous := 0.9
	if got := SuccessRateAfterWindow(0, 0, &previous); got == nil || *got != previous {
		t.Fatalf("无请求沿用旧值: %v", got)
	}
	// EWMA 单侧缺失。
	recent := int64(200)
	if got := NextEwma(nil, &recent); *got != 200 {
		t.Fatalf("缺前值沿用新值: %v", got)
	}
	previousEwma := int64(100)
	if got := NextEwma(&previousEwma, nil); *got != 100 {
		t.Fatalf("缺新值沿用前值: %v", got)
	}
}

func TestW12HTransportProbeLocalFailureKinds(t *testing.T) {
	cases := []struct {
		evidence ProbeEvidence
		want     string
	}{
		{ProbeEvidence{TransportFailureKind: TransportFailureTimeout}, "timeout"},
		{ProbeEvidence{TransportFailureKind: TransportFailureRead}, "read"},
		{ProbeEvidence{TransportFailureKind: TransportFailureConnection}, "connection"},
		{ProbeEvidence{}, ""},
		{ProbeEvidence{TimedOut: true, DiagnosticTimeoutExhausted: true, HasRealUpstreamAttempt: true}, "timeout"},
		{ProbeEvidence{HasRealUpstreamAttempt: true}, "connection"},
	}
	for i, test := range cases {
		if got := transportProbeLocalFailureKind(test.evidence, false); got != test.want {
			t.Fatalf("case %d: got %q want %q", i, got, test.want)
		}
	}
}

func TestW12HResolveTimezoneArms(t *testing.T) {
	loc, err := ResolveTimezone("")
	if err != nil || loc != time.UTC {
		t.Fatalf("空时区必须回落 UTC: %v %v", loc, err)
	}
	if _, err := ResolveTimezone("Mars/Olympus"); err == nil || !strings.Contains(err.Error(), "usageStatsTimezone") {
		t.Fatalf("未知时区必须报错: %v", err)
	}
	loc, err = ResolveTimezone("Asia/Shanghai")
	if err != nil || loc == nil {
		t.Fatalf("合法时区必须解析: %v", err)
	}
	// MinuteKey 的本地时区形态。
	value := MinuteKey(time.Date(2026, 9, 17, 2, 30, 0, 0, time.UTC), loc)
	if !strings.HasPrefix(value, "2026-09-17T10:") {
		t.Fatalf("MinuteKey 本地化不符: %s", value)
	}
}

// ---------------------------------------------------------------------------
// store 校验与错误传播

func TestW12HOpenStatsStoreValidation(t *testing.T) {
	if _, err := OpenStatsStore(StatsStoreConfig{Mode: StatsSQLite}); err == nil || !strings.Contains(err.Error(), "缺少数据库路径") {
		t.Fatalf("sqlite 缺路径必须报错: %v", err)
	}
	if _, err := OpenStatsStore(StatsStoreConfig{Mode: StatsPostgres}); err == nil || !strings.Contains(err.Error(), "缺少注入") {
		t.Fatalf("postgres 缺句柄必须报错: %v", err)
	}
	if _, err := OpenStatsStore(StatsStoreConfig{Mode: "w12h-bogus"}); err == nil || !strings.Contains(err.Error(), "mode") {
		t.Fatalf("未知模式必须报错: %v", err)
	}
	// PG 模式用注入句柄：Close 不关闭注入句柄。
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "injected.db"))
	if err != nil {
		t.Fatal(err)
	}
	injected, err := OpenStatsStore(StatsStoreConfig{Mode: StatsPostgres, PostgresDB: db})
	if err != nil {
		t.Fatal(err)
	}
	// PG 冻结 DDL 引用 juhe_stats schema，sqlite 句柄上不可执行；只验证
	// OpenStatsStore 的 PG 分支与 Close 无操作。
	if err := injected.Close(); err != nil {
		t.Fatalf("PG 模式 Close 必须无操作: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	var nilStore *StatsStore
	if err := nilStore.Close(); err != nil {
		t.Fatal("nil store Close 必须无操作")
	}
}

func TestW12HRefreshValidationArms(t *testing.T) {
	store, _, _ := newQualityStore(t)
	ctx := context.Background()
	// business 缺失。
	business := store.business
	store.business = nil
	if _, err := store.RefreshFromUsage(ctx, RefreshInput{WindowMinutes: 10}); err == nil {
		t.Fatal("缺业务元数据来源必须报错")
	}
	store.business = business
	// 非法时区。
	if _, err := store.RefreshFromUsage(ctx, RefreshInput{WindowMinutes: 10, Timezone: "Mars/Olympus"}); err == nil {
		t.Fatal("非法时区必须报错")
	}
	// 窗口钳制不报错。
	if _, err := store.RefreshFromUsage(ctx, RefreshInput{WindowMinutes: -5, Timezone: "UTC"}); err != nil {
		t.Fatalf("窗口下钳应通过: %v", err)
	}
	if _, err := store.RefreshFromUsage(ctx, RefreshInput{WindowMinutes: 99999, Timezone: "UTC"}); err != nil {
		t.Fatalf("窗口上钳应通过: %v", err)
	}

	// 分钟表缺失 → 聚合失败传播。
	if _, err := store.db.Exec(`DROP TABLE account_quality_minute_stats`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RefreshFromUsage(ctx, RefreshInput{WindowMinutes: 10, Timezone: "UTC"}); err == nil {
		t.Fatal("分钟表缺失必须失败")
	}
}

func TestW12HStoreErrorPropagationArms(t *testing.T) {
	ctx := context.Background()

	// 脏账户表缺失。
	store, _, lookup := newQualityStore(t)
	lookup.accounts["acc-1"] = AccountMetadata{SystemAccountID: "sys-1", ProviderCode: "openai"}
	if _, err := store.db.Exec(`DROP TABLE account_quality_dirty_accounts`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RefreshFromUsage(ctx, RefreshInput{WindowMinutes: 10, Timezone: "UTC"}); err == nil {
		t.Fatal("脏账户表缺失必须失败")
	}
	_ = store.Close()

	// 候选行 updated_at 非毫秒 → 解析失败传播。
	store2, _, lookup2 := newQualityStore(t)
	lookup2.accounts["acc-1"] = AccountMetadata{SystemAccountID: "sys-1", ProviderCode: "openai"}
	if _, err := store2.db.Exec(`INSERT INTO account_quality_scores (
		account_id, system_account_id, provider_code, quality_score, quality_state,
		recent_request_count, recent_success_count, recent_error_count,
		window_started_at, window_ended_at, updated_at
	) VALUES ('acc-1', 'sys-1', 'openai', 100, 'fresh', 10, 2, 8, 'w', 'w', 'not-a-millis')`); err != nil {
		t.Fatal(err)
	}
	if _, err := store2.ListFailurePrecheckCandidates(ctx, 10, 0); err == nil {
		t.Fatal("updated_at 非毫秒必须失败")
	}
	_ = store2.Close()

	// 质量表缺失 → LoadQualityRow / ListFailurePrecheckCandidates 失败。
	store3, _, _ := newQualityStore(t)
	if _, err := store3.db.Exec(`DROP TABLE account_quality_scores`); err != nil {
		t.Fatal(err)
	}
	if _, err := store3.LoadQualityRow(ctx, "acc-1"); err == nil {
		t.Fatal("质量表缺失必须失败")
	}
	if _, err := store3.ListFailurePrecheckCandidates(ctx, 10, 0); err == nil {
		t.Fatal("质量表缺失必须失败")
	}
	_ = store3.Close()
}

func TestW12HStoreBehaviorArms(t *testing.T) {
	store, clock, lookup := newQualityStore(t)
	ctx := context.Background()
	// 批量脏账户 + 大于 lookup 块大小的质量行（分块读回）。
	for i := 0; i < 5; i++ {
		id := fmt.Sprintf("w12h-bulk-%d", i)
		lookup.accounts[id] = AccountMetadata{SystemAccountID: "sys-1", ProviderCode: "openai"}
		store.seedMinute(t, id, MinuteKey(clock.Now().Add(-time.Minute), time.UTC), 8, 6, 2, 500, 5, "")
		if err := store.MarkQualityDirty(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	result, err := store.RefreshFromUsage(ctx, RefreshInput{WindowMinutes: 10, Timezone: "UTC", DirtyLimit: DirtyAccountBatchLimit})
	if err != nil {
		t.Fatal(err)
	}
	if result.Refreshed != 5 {
		t.Fatalf("应刷新 5 行: %d", result.Refreshed)
	}
	row, err := store.LoadQualityRow(ctx, "w12h-bulk-0")
	if err != nil || row == nil || row.QualityState != QualityFresh {
		t.Fatalf("读取质量行不符: %+v %v", row, err)
	}
	// 读取不存在的行 → nil, nil。
	if missing, err := store.LoadQualityRow(ctx, "w12h-missing"); err != nil || missing != nil {
		t.Fatalf("缺失行应为 nil: %+v %v", missing, err)
	}
	// 刷新后脏表清空。
	if ids, err := store.loadDirtyAccountIds(ctx, 100); err != nil || len(ids) != 0 {
		t.Fatalf("刷新后脏表应清空: %v %v", ids, err)
	}
}
