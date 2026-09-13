package main

// w1: chain_error_policy.go 纯函数、chain_accounts_secret.go 辅助投影与
// chain_usage.go 记录桥直测。

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayresponse"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayusage"
)

func TestW1SystemInsufficientQuotaRuleMatches(t *testing.T) {
	// 非 402/403：false。
	if systemInsufficientQuotaRuleMatches(500, "insufficient_quota", "", "") {
		t.Fatal("500 不得命中")
	}
	// 稳定配额码。
	if !systemInsufficientQuotaRuleMatches(402, "insufficient_quota", "", "") {
		t.Fatal("insufficient_quota 必须命中")
	}
	// 类型含 quota。
	if !systemInsufficientQuotaRuleMatches(403, "", "billing_quota", "") {
		t.Fatal("类型 quota 必须命中")
	}
	// 非 quota 403 识别符：false。
	if systemInsufficientQuotaRuleMatches(403, "content_policy_violation", "", "") {
		t.Fatal("内容策略 403 不得命中")
	}
	// 402 无码无类型：true（默认配额语义）。
	if !systemInsufficientQuotaRuleMatches(402, "", "", "") {
		t.Fatal("裸 402 必须命中")
	}
	// 403 无码无类型：false（403 默认不是配额）。
	if systemInsufficientQuotaRuleMatches(403, "", "", "") {
		t.Fatal("裸 403 不得命中")
	}
	// 可搜索文本含 quota 关键字。
	if !systemInsufficientQuotaRuleMatches(403, "", "", "insufficient quota remaining") {
		t.Fatal("quota 文本必须命中")
	}
}

func TestW1ParseAbsoluteRecoveryTime(t *testing.T) {
	// 秒级时间戳（<1e10）。
	if got := parseAbsoluteRecoveryTime(float64(1_800_000_000)); got == nil || got.Unix() != 1_800_000_000 {
		t.Fatalf("秒时间戳 = %v", got)
	}
	// 毫秒级时间戳（>1e10）。
	if got := parseAbsoluteRecoveryTime(float64(1_800_000_000_000)); got == nil || got.Unix() != 1_800_000_000 {
		t.Fatalf("毫秒时间戳 = %v", got)
	}
	// 非正数。
	if parseAbsoluteRecoveryTime(float64(0)) != nil || parseAbsoluteRecoveryTime(float64(-5)) != nil {
		t.Fatal("非正数必须 nil")
	}
	// 字符串数字。
	if got := parseAbsoluteRecoveryTime(" 1800000000 "); got == nil || got.Unix() != 1_800_000_000 {
		t.Fatalf("字符串秒 = %v", got)
	}
	// RFC3339 字符串。
	if got := parseAbsoluteRecoveryTime("2027-01-01T00:00:00Z"); got == nil {
		t.Fatalf("RFC3339 = %v", got)
	}
	// 空串 / 垃圾。
	if parseAbsoluteRecoveryTime("") != nil || parseAbsoluteRecoveryTime("garbage") != nil {
		t.Fatal("垃圾输入必须 nil")
	}
	// 其他类型。
	if parseAbsoluteRecoveryTime(nil) != nil || parseAbsoluteRecoveryTime(42) != nil {
		t.Fatal("非数字非字符串必须 nil")
	}
}

func TestW1ParseRetryAfterHeader(t *testing.T) {
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	if parseRetryAfterHeader("", now) != nil {
		t.Fatal("空值必须 nil")
	}
	// 秒数：now + 120s。
	if got := parseRetryAfterHeader("120", now); got == nil || !got.Equal(now.Add(120*time.Second)) {
		t.Fatalf("秒数 = %v", got)
	}
	// 未来绝对时间。
	if got := parseRetryAfterHeader("2026-09-13T13:00:00Z", now); got == nil || !got.Equal(time.Date(2026, 9, 13, 13, 0, 0, 0, time.UTC)) {
		t.Fatalf("绝对时间 = %v", got)
	}
	// 过去绝对时间：nil。
	if got := parseRetryAfterHeader("2020-01-01T00:00:00Z", now); got != nil {
		t.Fatalf("过去时间 = %v", got)
	}
	// RFC1123。
	future := now.Add(2 * time.Hour).Format(time.RFC1123)
	if got := parseRetryAfterHeader(future, now); got == nil {
		t.Fatalf("RFC1123 = %v", got)
	}
	// 垃圾。
	if parseRetryAfterHeader("soon-ish", now) != nil {
		t.Fatal("垃圾必须 nil")
	}
}

func TestW1AccountErrorRuleCooldownUntil(t *testing.T) {
	now := time.Date(2026, 9, 13, 10, 0, 0, 0, time.UTC)
	// duration 策略：now + 2h + 抖动。
	duration := accountErrorHandlingRule{ResetStrategy: "duration", DurationHours: 2}
	until := accountErrorRuleCooldownUntil(duration, now, "seed-1")
	parsed, err := time.Parse(time.RFC3339, until)
	if err != nil {
		t.Fatalf("解析 duration until: %v", err)
	}
	// 抖动窗口：2h 区间 → ±30min。
	if elapsed := parsed.Sub(now); elapsed < 90*time.Minute || elapsed > 150*time.Minute {
		t.Fatalf("duration 冷却跨度 = %v", elapsed)
	}
	// daily 策略：当日 22 点已过则顺延次日。
	daily := accountErrorHandlingRule{ResetStrategy: "daily", DailyResetHour: 22}
	until = accountErrorRuleCooldownUntil(daily, now, "seed-2")
	parsed, err = time.Parse(time.RFC3339, until)
	if err != nil {
		t.Fatalf("解析 daily until: %v", err)
	}
	// 次日 22 点 = 36h；当日 22 点已过（10 点 < 22 点 → 当日 22 点 = 12h）。
	// 窗口 ±30min。
	if elapsed := parsed.Sub(now); elapsed < 11*time.Hour+30*time.Minute || elapsed > 12*time.Hour+31*time.Minute {
		t.Fatalf("daily 冷却跨度 = %v", elapsed)
	}
	// 行为存疑：weekly 分支用 target.AddDate(daysAhead, 0, 0) 把目标天数
	// 加在了 AddDate 的年参数上（daysAhead=3 → +3 年，Node 语义应为
	// +3 天）。当前实际行为按「冷却落在数年后」断言；见报告
	// 「疑似生产问题」。
	weekly := accountErrorHandlingRule{ResetStrategy: "weekly", WeeklyResetHour: 8, WeeklyResetDay: 3}
	until = accountErrorRuleCooldownUntil(weekly, now, "seed-3")
	parsed, err = time.Parse(time.RFC3339, until)
	if err != nil {
		t.Fatalf("解析 weekly until: %v", err)
	}
	if elapsed := parsed.Sub(now); elapsed < 365*24*time.Hour {
		t.Fatalf("weekly 冷却按当前实现应为年跨度（AddDate 缺陷），got %v", elapsed)
	}
	if max64(5, 3) != 5 || max64(3, 5) != 5 {
		t.Fatal("max64 错误")
	}
}

func TestW1QuotaRecoveryCooldownUntil(t *testing.T) {
	now := time.Date(2026, 9, 13, 10, 0, 0, 0, time.UTC)
	// api_key 缺省：duration 60 分钟。
	until, err := quotaRecoveryCooldownUntil(map[string]any{}, "api_key", "seed", now)
	if err != nil {
		t.Fatalf("api_key 缺省: %v", err)
	}
	parsed, err := time.Parse(time.RFC3339, until)
	if err != nil {
		t.Fatalf("解析: %v", err)
	}
	// 60min 区间 → 窗口 ±30min。
	if elapsed := parsed.Sub(now); elapsed < 30*time.Minute || elapsed > 90*time.Minute+time.Minute {
		t.Fatalf("api_key 跨度 = %v", elapsed)
	}
	// oauth 缺省：每日 0 点（UTC）→ 14 小时后 + 抖动。
	until, err = quotaRecoveryCooldownUntil(map[string]any{}, "oauth", "seed", now)
	if err != nil {
		t.Fatalf("oauth 缺省: %v", err)
	}
	parsed, _ = time.Parse(time.RFC3339, until)
	// 次日 0 点 UTC = 14h；窗口 ±30min。
	if elapsed := parsed.Sub(now); elapsed < 13*time.Hour+30*time.Minute || elapsed > 14*time.Hour+31*time.Minute {
		t.Fatalf("oauth 跨度 = %v", elapsed)
	}
	// 自定义 duration_minutes。
	policy := map[string]any{"api_key": map[string]any{"reset_strategy": "duration", "duration_minutes": float64(30)}}
	until, err = quotaRecoveryCooldownUntil(policy, "api_key", "seed", now)
	if err != nil {
		t.Fatalf("自定义: %v", err)
	}
	parsed, _ = time.Parse(time.RFC3339, until)
	// 30min 区间 → 窗口 ±30s。
	if elapsed := parsed.Sub(now); elapsed < 29*time.Minute+30*time.Second || elapsed > 30*time.Minute+31*time.Second {
		t.Fatalf("自定义跨度 = %v", elapsed)
	}
	// 非法 timezone：错误。
	broken := map[string]any{"api_key": map[string]any{"reset_strategy": "daily", "timezone": "Not/AZone"}}
	if _, err := quotaRecoveryCooldownUntil(broken, "api_key", "seed", now); err == nil {
		t.Fatal("非法 timezone 必须报错")
	}
}

func TestW1PassiveScheduleJitterWindowMs(t *testing.T) {
	if got := passiveScheduleJitterWindowMs(0); got < 0 {
		t.Fatalf("0 窗口 = %d", got)
	}
	if got := passiveScheduleJitterWindowMs(60_000); got > 60_000 {
		t.Fatalf("窗口超上限 = %d", got)
	}
	// 确定性：同输入同输出。
	if passiveScheduleJitterWindowMs(60_000) != passiveScheduleJitterWindowMs(60_000) {
		t.Fatal("抖动必须确定性")
	}
}

func TestW1AccountsSecretHelpers(t *testing.T) {
	// chainAccountAPIKeyPoolProviderSupported。
	if !chainAccountAPIKeyPoolProviderSupported("OpenAI ", "openai", "v1") {
		t.Fatal("openai 必须支持")
	}
	if !chainAccountAPIKeyPoolProviderSupported("unknown", "anthropic", "v1") {
		t.Fatal("anthropic v1 必须支持")
	}
	if chainAccountAPIKeyPoolProviderSupported("unknown", "anthropic", "v2") {
		t.Fatal("anthropic v2 不得支持")
	}
	if chainAccountAPIKeyPoolProviderSupported("", "", "") {
		t.Fatal("空不得支持")
	}
	// chainNormalizeAPIKeyWeight。
	weights := []float64{50, 0, 150, 2.5}
	if chainNormalizeAPIKeyWeight(0, weights) != 50 {
		t.Fatal("正常权重必须保留")
	}
	for index := 1; index < 4; index++ {
		if chainNormalizeAPIKeyWeight(index, weights) != 1 {
			t.Fatalf("index %d 必须回 1", index)
		}
	}
	if chainNormalizeAPIKeyWeight(-1, weights) != 1 || chainNormalizeAPIKeyWeight(9, weights) != 1 {
		t.Fatal("越界必须回 1")
	}
	// chainNormalizeGroupType。
	personal, err := chainNormalizeGroupType(sql.NullString{})
	if err != nil || personal == nil || *personal != "personal" {
		t.Fatalf("空类型 = %v, %v", personal, err)
	}
	blank, err := chainNormalizeGroupType(sql.NullString{String: "  ", Valid: true})
	if err != nil || *blank != "personal" {
		t.Fatalf("空白类型 = %v, %v", blank, err)
	}
	high, err := chainNormalizeGroupType(sql.NullString{String: "high_concurrency", Valid: true})
	if err != nil || *high != "high_concurrency" {
		t.Fatalf("高并发 = %v, %v", high, err)
	}
	if _, err := chainNormalizeGroupType(sql.NullString{String: "bogus", Valid: true}); err == nil {
		t.Fatal("非法类型必须报错")
	}
	// chainParseGroupSchedulingPolicy。
	if got, err := chainParseGroupSchedulingPolicy(sql.NullString{}, nil); err != nil || got != nil {
		t.Fatalf("nil 组类型 = %v, %v", got, err)
	}
	personalType := "personal"
	if got, err := chainParseGroupSchedulingPolicy(sql.NullString{String: "{}", Valid: true}, &personalType); err != nil || got != nil {
		t.Fatalf("personal = %v, %v", got, err)
	}
	highType := "high_concurrency"
	if _, err := chainParseGroupSchedulingPolicy(sql.NullString{}, &highType); err == nil {
		t.Fatal("高并发缺策略必须报错")
	}
	policy, err := chainParseGroupSchedulingPolicy(sql.NullString{String: `{"maxQueueSize":10}`, Valid: true}, &highType)
	if err != nil || policy == nil || (*policy)["maxQueueSize"] != float64(10) {
		t.Fatalf("策略 = %+v, %v", policy, err)
	}
	if _, err := chainParseGroupSchedulingPolicy(sql.NullString{String: "bad", Valid: true}, &highType); err == nil {
		t.Fatal("非法 JSON 必须报错")
	}
	// chainResolveProxyURL。
	proxyURL := "socks5h://127.0.0.1:1080"
	resolution := chainResolveProxyURL("prof_1", map[string]chainProxyProfileResolution{"prof_1": {proxyURL: &proxyURL}})
	if resolution.proxyURL == nil || *resolution.proxyURL != "socks5h://127.0.0.1:1080" {
		t.Fatalf("命中 = %+v", resolution)
	}
	missing := chainResolveProxyURL("prof_404", nil)
	if missing.unavailable == nil || !strings.Contains(*missing.errorMessage, "代理不存在") {
		t.Fatalf("缺失 = %+v", missing)
	}
	if got := chainResolveProxyURL("", nil); got.unavailable != nil {
		t.Fatalf("空 ID = %+v", got)
	}
	// chainResourceAuthorizationQuotaLimited。
	if got := chainResourceAuthorizationQuotaLimited(nil); got == nil || *got {
		t.Fatal("nil limits 必须 false")
	}
	empty := ""
	if got := chainResourceAuthorizationQuotaLimited(&empty); got == nil || *got {
		t.Fatal("空白 limits 必须 false")
	}
	enabled := `{"hourly":{"enabled":true}}`
	if got := chainResourceAuthorizationQuotaLimited(&enabled); got == nil || !*got {
		t.Fatal("hourly 启用必须 true")
	}
	disabled := `{"hourly":{"enabled":false},"total":{"enabled":false}}`
	if got := chainResourceAuthorizationQuotaLimited(&disabled); got == nil || *got {
		t.Fatal("全禁用必须 false")
	}
	bad := "{not-json"
	if got := chainResourceAuthorizationQuotaLimited(&bad); got == nil || *got {
		t.Fatal("非法 JSON 必须 false")
	}
	monthly := `{"monthly":{"enabled":true}}`
	if got := chainResourceAuthorizationQuotaLimited(&monthly); got == nil || !*got {
		t.Fatal("monthly 启用必须 true")
	}
	// nullInt64Ptr。
	if nullInt64Ptr(sql.NullInt64{}) != nil {
		t.Fatal("invalid 必须 nil")
	}
	value := nullInt64Ptr(sql.NullInt64{Int64: 7, Valid: true})
	if value == nil || *value != 7 {
		t.Fatalf("值 = %v", value)
	}
	// chainNormalizeGatewayEndpointModesForRuntime。
	modes := []any{"chat_json", "bogus_mode"}
	if got := chainNormalizeGatewayEndpointModesForRuntime(modes, "hybrid", "api_key", "", "", "", ""); len(got) != 1 || got[0] != "chat_json" {
		t.Fatalf("hybrid 过滤 = %v", got)
	}
	if got := chainNormalizeGatewayEndpointModesForRuntime(nil, "unknown", "oauth", "", "", "openai", "v1"); len(got) == 0 {
		t.Fatal("oauth 缺省模式缺失")
	}
	if got := chainNormalizeGatewayEndpointModesForRuntime(nil, "gpt", "api_key", "", "", "openai", "v1"); len(got) == 0 {
		t.Fatal("gpt 缺省模式缺失")
	}
	if got := chainNormalizeGatewayEndpointModesForRuntime(nil, "unknown", "api_key", "codex_responses", "", "openai", "v1"); len(got) == 0 {
		t.Fatal("codex 兼容缺省缺失")
	}
}

func TestW1SpooledUsageRecorderDropAndOverflow(t *testing.T) {
	// nil spool：入队即走 drain → deliver → dropped 计数与告警。
	recorder := newSpooledUsageRecorder(usageBridgeConfig{BufferCapacity: 4}, nil)
	for index := 0; index < 3; index++ {
		if err := recorder.EnqueueUsageRecord(gatewayusage.Ctx(context.Background()), gatewayusage.UsageRecordInput{TraceID: "t", ID: "u"}); err != nil {
			t.Fatalf("enqueue %d: %v", index, err)
		}
	}
	recorder.Close()
	if recorder.dropped != 3 {
		t.Fatalf("dropped = %d，want 3", recorder.dropped)
	}
	// Close 后再入队：persistOverflow → dropped 继续累加。
	if err := recorder.EnqueueUsageRecord(gatewayusage.Ctx(context.Background()), gatewayusage.UsageRecordInput{TraceID: "t"}); err != nil {
		t.Fatalf("closed enqueue: %v", err)
	}
	if recorder.dropped != 4 {
		t.Fatalf("closed dropped = %d，want 4", recorder.dropped)
	}
	// 二次 Close 幂等。
	recorder.Close()
}

func TestW1FinalizationUsageCompletedAttempt(t *testing.T) {
	recorder := &capturingUsageRecorder{}
	usage := chainFinalizationUsage{recorder: recorder}
	firstToken := int64(120)
	completedAt := int64(1_500)
	statusCode := 200
	usage.RecordCompletedUpstreamAttempt(gatewayresponse.CompletedAttemptInput{
		UsageContext: gatewaypreauth.GatewayFailureUsageContext{
			TraceID: "trace_ok", TrafficSource: "gateway", ClientIP: "203.0.113.5",
			SystemAccountID: "sys_1", APIKeyID: "key_1", GroupID: "grp_1",
			Endpoint: "/v1/chat/completions", ProviderCode: "openai",
		},
		Account: gatewayresponse.OpenAIAccountView{Account: gatewayruntimecache.OpenAIAccountSecret{ID: "acc_done"}},
		Success: true, StatusCode: statusCode, Stream: true,
		FirstTokenMs: &firstToken, StartedAtMs: 1_000, CompletedAtMs: &completedAt,
	})
	if len(recorder.records) != 1 {
		t.Fatalf("records = %d", len(recorder.records))
	}
	record := recorder.records[0]
	if record.TraceID != "trace_ok" || !record.Success || record.AccountID != "acc_done" {
		t.Fatalf("record = %+v", record)
	}
	if record.Stream == nil || !*record.Stream {
		t.Fatal("stream 必须透传")
	}
	if record.StatusCode == nil || *record.StatusCode != 200 {
		t.Fatal("status 必须透传")
	}
	if record.FirstTokenMs == nil || *record.FirstTokenMs != 120 {
		t.Fatal("first token 必须透传")
	}
	if record.DurationMs == nil || *record.DurationMs != 500 {
		t.Fatalf("duration = %v，want 500", record.DurationMs)
	}
	// 无账户：AccountID 空。
	usage.RecordCompletedUpstreamAttempt(gatewayresponse.CompletedAttemptInput{})
	if len(recorder.records) != 2 {
		t.Fatalf("records = %d", len(recorder.records))
	}
	if recorder.records[1].AccountID != "" {
		t.Fatal("空账户 AccountID 必须为空")
	}
	// recorder 缺席：安全。
	absent := chainFinalizationUsage{}
	absent.RecordCompletedUpstreamAttempt(gatewayresponse.CompletedAttemptInput{})
	absent.RecordFailedUpstreamAttempt(gatewayresponse.FailedAttemptInput{})
}
