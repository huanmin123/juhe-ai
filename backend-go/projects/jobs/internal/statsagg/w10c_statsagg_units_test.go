package statsagg

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"strings"
	"testing"
	"time"
)

// w10c_statsagg_units_test.go 覆盖统计聚合域的剩余单元臂：
// 窗口计划纯函数、PG 聚合 map 组装纯函数、稳定水印序列化、RFC3339 工具、
// 记录校验矩阵、排行快照 stage、job state 生命周期与窗口编排错误臂。
// DB 相关断言全部使用 SQLite 测试库（newTestEnv，同包既有 fixture）。

// ---- 窗口计划纯函数 ----

func TestW10CWindowPlanHelpers(t *testing.T) {
	// startOfWeekMonday：周日（weekday=0）→ offset 6 回退到周一。
	sunday := time.Date(2026, 4, 19, 12, 0, 0, 0, time.UTC)
	if got := startOfWeekMonday(sunday).Format("2006-01-02"); got != "2026-04-13" {
		t.Fatalf("startOfWeekMonday(sunday) = %s want 2026-04-13", got)
	}
	monday := time.Date(2026, 4, 13, 12, 0, 0, 0, time.UTC)
	if got := startOfWeekMonday(monday).Format("2006-01-02"); got != "2026-04-13" {
		t.Fatalf("startOfWeekMonday(monday) = %s want unchanged", got)
	}
	wednesday := time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)
	if got := startOfWeekMonday(wednesday).Format("2006-01-02"); got != "2026-04-13" {
		t.Fatalf("startOfWeekMonday(wednesday) = %s want 2026-04-13", got)
	}

	// CompareText 三臂。
	if CompareText("a", "b") != -1 || CompareText("b", "a") != 1 || CompareText("a", "a") != 0 {
		t.Fatalf("CompareText 三臂断言失败")
	}

	// NextCalendarDateKey：普通日期 +1、月末进位、非法输入原样返回。
	if got := NextCalendarDateKey("2026-04-18"); got != "2026-04-19" {
		t.Fatalf("NextCalendarDateKey = %s want 2026-04-19", got)
	}
	if got := NextCalendarDateKey("2026-04-30"); got != "2026-05-01" {
		t.Fatalf("NextCalendarDateKey 月末进位 = %s want 2026-05-01", got)
	}
	if got := NextCalendarDateKey("not-a-date"); got != "not-a-date" {
		t.Fatalf("NextCalendarDateKey 非法输入应原样返回, got %s", got)
	}

	// TrendBucketKey 长尾臂：>=24 小时桶且 statHour 不足 10 位、<=1 小时桶、
	// 6 小时桶归一、Atoi 失败（小时段非数字）原样返回。
	if got := TrendBucketKey("short", 24); got != "short" {
		t.Fatalf("TrendBucketKey 短键 24h = %s", got)
	}
	if got := TrendBucketKey("2026-04-18T10", 1); got != "2026-04-18T10" {
		t.Fatalf("TrendBucketKey 1h = %s", got)
	}
	if got := TrendBucketKey("2026-04-18T13", 6); got != "2026-04-18T12" {
		t.Fatalf("TrendBucketKey 6h 归一 = %s want 2026-04-18T12", got)
	}
	if got := TrendBucketKey("2026-04-18TXX", 6); got != "2026-04-18TXX" {
		t.Fatalf("TrendBucketKey 非数字小时应原样返回, got %s", got)
	}
	if got := TrendBucketKey("2026-04-18T1", 6); got != "2026-04-18T1" {
		t.Fatalf("TrendBucketKey len<13 应原样返回, got %s", got)
	}
}

// ---- AuthorizationLookup.setAccountAuthorizationResourceID ----

func TestW10CAuthorizationLookupResourceID(t *testing.T) {
	lookup := &AuthorizationLookup{}
	// nil map 惰性初始化臂。
	lookup.setAccountAuthorizationResourceID("auth-1", "res-1")
	if lookup.AccountAuthorizationResourceIDs["auth-1"] != "res-1" {
		t.Fatalf("setAccountAuthorizationResourceID 未写入")
	}
	lookup.setAccountAuthorizationResourceID("auth-2", "res-2")
	if len(lookup.AccountAuthorizationResourceIDs) != 2 {
		t.Fatalf("第二次写入丢失: %v", lookup.AccountAuthorizationResourceIDs)
	}
}

// ---- applyPostgresEstimatedCacheReadCost 全臂 ----

func TestW10CPostgresEstimatedCacheReadCost(t *testing.T) {
	aggregator := &Aggregator{}

	// 已有 cost → 不回填。
	withCost := UsageStatsRecordRow{CacheReadCostUsd: f64Ptr(0.5), CacheReadTokens: f64Ptr(10)}
	aggregator.applyPostgresEstimatedCacheReadCost(&withCost)
	if *withCost.CacheReadCostUsd != 0.5 {
		t.Fatalf("已有 cost 被覆盖")
	}

	// nil estimator → 不回填。
	noEstimator := UsageStatsRecordRow{CacheReadTokens: f64Ptr(10)}
	aggregator.applyPostgresEstimatedCacheReadCost(&noEstimator)
	if noEstimator.CacheReadCostUsd != nil {
		t.Fatalf("nil estimator 不应回填")
	}

	estimating := &Aggregator{CacheReadCostEstimator: func(providerCode, model string, tokens float64) (float64, bool) {
		if providerCode == "" || model == "" {
			t.Fatalf("estimator 入参 providerCode/model 不应为空")
		}
		return 0.25, true
	}}
	// lenientEstimator 供 nil provider/model 臂（deref 空串）使用。
	lenientEstimator := &Aggregator{CacheReadCostEstimator: func(providerCode, model string, tokens float64) (float64, bool) {
		return 0.25, providerCode == "" && model == ""
	}}

	// 估算失败（ok=false）→ 不回填。
	notOk := &Aggregator{CacheReadCostEstimator: func(string, string, float64) (float64, bool) { return 0, false }}
	row := UsageStatsRecordRow{ProviderCode: strPtr("openai"), Model: strPtr("gpt-5"), CacheReadTokens: f64Ptr(10)}
	notOk.applyPostgresEstimatedCacheReadCost(&row)
	if row.CacheReadCostUsd != nil {
		t.Fatalf("ok=false 不应回填")
	}

	// 估算值 <= 0 → 不回填。
	zero := &Aggregator{CacheReadCostEstimator: func(string, string, float64) (float64, bool) { return 0, true }}
	row = UsageStatsRecordRow{ProviderCode: strPtr("openai"), Model: strPtr("gpt-5")}
	zero.applyPostgresEstimatedCacheReadCost(&row)
	if row.CacheReadCostUsd != nil {
		t.Fatalf("cost<=0 不应回填")
	}

	// 成功回填（providerCode/model 均非 nil）。
	row = UsageStatsRecordRow{ProviderCode: strPtr("openai"), Model: strPtr("gpt-5"), CacheReadTokens: f64Ptr(20)}
	estimating.applyPostgresEstimatedCacheReadCost(&row)
	if row.CacheReadCostUsd == nil || *row.CacheReadCostUsd != 0.25 {
		t.Fatalf("成功臂未回填: %v", row.CacheReadCostUsd)
	}

	// providerCode/model 为 nil → deref 空串臂。
	row = UsageStatsRecordRow{CacheReadTokens: f64Ptr(1)}
	lenientEstimator.applyPostgresEstimatedCacheReadCost(&row)
	if row.CacheReadCostUsd == nil || *row.CacheReadCostUsd != 0.25 {
		t.Fatalf("nil provider/model 臂未回填")
	}
}

// ---- addPostgresAggregatedAccountQualityEntry 全臂 ----

func w10cQualityRow(mutate func(*UsageStatsRecordRow)) UsageStatsRecordRow {
	row := UsageStatsRecordRow{
		ID: "rec-q", SystemAccountID: "alice", TraceID: "tr", TrafficSource: "gateway",
		AccountID: strPtr("acc-1"), APIKeyID: strPtr("key-1"),
		AccountOwnerSystemAccountID: strPtr("alice"), AccountAccessType: strPtr("owner"),
		Success: 1, CreatedAt: "2026-04-18T10:15:00.000Z",
	}
	if mutate != nil {
		mutate(&row)
	}
	return row
}

func TestW10CPostgresAccountQualityEntry(t *testing.T) {
	timeKeys := UsageStatsTimeKeys{StatMinute: "2026-04-18T10:15", StatHour: "2026-04-18T10", StatDate: "2026-04-18"}

	// 过滤臂：探针类流量来源不计质量。
	for _, source := range []string{"runtime_recovery_probe", "cooldown_retest", "hybrid_scoring", "hybrid_quality_scoring"} {
		target := map[string]*aggregatedAccountQualityEntry{}
		if err := addPostgresAggregatedAccountQualityEntry(target, w10cQualityRow(func(r *UsageStatsRecordRow) { r.TrafficSource = source }), timeKeys); err != nil {
			t.Fatal(err)
		}
		if len(target) != 0 {
			t.Fatalf("traffic source %s 不应记质量", source)
		}
	}

	// account / api_key 缺失臂。
	target := map[string]*aggregatedAccountQualityEntry{}
	if err := addPostgresAggregatedAccountQualityEntry(target, w10cQualityRow(func(r *UsageStatsRecordRow) { r.AccountID = nil }), timeKeys); err != nil {
		t.Fatal(err)
	}
	if err := addPostgresAggregatedAccountQualityEntry(target, w10cQualityRow(func(r *UsageStatsRecordRow) { r.APIKeyID = nil }), timeKeys); err != nil {
		t.Fatal(err)
	}
	if len(target) != 0 {
		t.Fatalf("缺 account/api_key 不应记质量")
	}

	// 失败行且 failure attribution 非上游 → 不记。
	target = map[string]*aggregatedAccountQualityEntry{}
	if err := addPostgresAggregatedAccountQualityEntry(target, w10cQualityRow(func(r *UsageStatsRecordRow) {
		r.Success = 0
		r.FailureAttribution = strPtr("client_error")
	}), timeKeys); err != nil {
		t.Fatal(err)
	}
	if len(target) != 0 {
		t.Fatalf("非上游失败不应记质量")
	}

	// 成功新条目：first token 有效样本计数 1。
	target = map[string]*aggregatedAccountQualityEntry{}
	if err := addPostgresAggregatedAccountQualityEntry(target, w10cQualityRow(func(r *UsageStatsRecordRow) { r.FirstTokenMs = f64Ptr(250) }), timeKeys); err != nil {
		t.Fatal(err)
	}
	entry := target["acc-1\x002026-04-18T10:15"]
	if entry == nil || entry.RequestCount != 1 || entry.SuccessCount != 1 || entry.FirstTokenMsCount != 1 || entry.FirstTokenMsSum != 250 {
		t.Fatalf("成功新条目错误: %+v", entry)
	}
	if entry.LastSuccessAt == "" || entry.LastErrorAt != "" {
		t.Fatalf("成功条目 last 字段错误: %+v", entry)
	}

	// first token NaN / Inf / 负值 → 不计样本。
	for _, invalid := range []float64{math.NaN(), math.Inf(1), math.Inf(-1), -5} {
		target = map[string]*aggregatedAccountQualityEntry{}
		if err := addPostgresAggregatedAccountQualityEntry(target, w10cQualityRow(func(r *UsageStatsRecordRow) { r.FirstTokenMs = f64Ptr(invalid) }), timeKeys); err != nil {
			t.Fatal(err)
		}
		if got := target["acc-1\x002026-04-18T10:15"]; got == nil || got.FirstTokenMsCount != 0 || got.FirstTokenMsSum != 0 {
			t.Fatalf("first token %v 不应计样本: %+v", invalid, got)
		}
	}

	// 失败新条目：error count + last error message。
	target = map[string]*aggregatedAccountQualityEntry{}
	if err := addPostgresAggregatedAccountQualityEntry(target, w10cQualityRow(func(r *UsageStatsRecordRow) {
		r.Success = 0
		r.FailureAttribution = strPtr("account_upstream")
		r.ErrorMessage = strPtr("upstream 500")
	}), timeKeys); err != nil {
		t.Fatal(err)
	}
	entry = target["acc-1\x002026-04-18T10:15"]
	if entry == nil || entry.ErrorCount != 1 || entry.LastErrorMessage != "upstream 500" || entry.LastErrorAt == "" {
		t.Fatalf("失败新条目错误: %+v", entry)
	}

	// 账户归属：access type 非 account_authorized → 使用 owner system account id。
	target = map[string]*aggregatedAccountQualityEntry{}
	if err := addPostgresAggregatedAccountQualityEntry(target, w10cQualityRow(func(r *UsageStatsRecordRow) {
		r.AccountOwnerSystemAccountID = strPtr("owner-org")
		r.AccountAccessType = strPtr("group_authorized")
	}), timeKeys); err != nil {
		t.Fatal(err)
	}
	if got := target["acc-1\x002026-04-18T10:15"]; got == nil || got.SystemAccountID != "owner-org" {
		t.Fatalf("归属 system account 错误: %+v", got)
	}

	// postgresAccountQualityStatsSystemAccountID 错误臂：缺 access type / 缺 owner。
	if _, err := postgresAccountQualityStatsSystemAccountID(UsageStatsRecordRow{ID: "rec-9"}); err == nil || !strings.Contains(err.Error(), "account_access_type") {
		t.Fatalf("缺 access type 应报错, got %v", err)
	}
	if _, err := postgresAccountQualityStatsSystemAccountID(UsageStatsRecordRow{ID: "rec-9", AccountAccessType: strPtr("owner")}); err == nil || !strings.Contains(err.Error(), "account_owner_system_account_id") {
		t.Fatalf("缺 owner 应报错, got %v", err)
	}
	if got, err := postgresAccountQualityStatsSystemAccountID(UsageStatsRecordRow{SystemAccountID: "alice", AccountAccessType: strPtr("account_authorized")}); err != nil || got != "alice" {
		t.Fatalf("account_authorized 应取 caller system account, got %s err=%v", got, err)
	}

	// 合并臂：同分钟两行累加 + last 字段取最新；同时间戳失败行替换错误信息
	//（CompareUsageStatsTimestamp >= 0 分支）。
	target = map[string]*aggregatedAccountQualityEntry{}
	if err := addPostgresAggregatedAccountQualityEntry(target, w10cQualityRow(func(r *UsageStatsRecordRow) {
		r.Success = 0
		r.FailureAttribution = strPtr("account_upstream")
		r.ErrorMessage = strPtr("first error")
	}), timeKeys); err != nil {
		t.Fatal(err)
	}
	if err := addPostgresAggregatedAccountQualityEntry(target, w10cQualityRow(func(r *UsageStatsRecordRow) {
		r.ID = "rec-q2"
		r.Success = 0
		r.FailureAttribution = strPtr("account_upstream")
		r.ErrorMessage = strPtr("second error")
	}), timeKeys); err != nil {
		t.Fatal(err)
	}
	entry = target["acc-1\x002026-04-18T10:15"]
	if entry == nil || entry.RequestCount != 2 || entry.ErrorCount != 2 || entry.LastErrorMessage != "second error" {
		t.Fatalf("同分钟合并错误: %+v", entry)
	}

	// MaxOptionalISO 错误臂：既有 LastSuccessAt 为非法时间时不更新该字段。
	target = map[string]*aggregatedAccountQualityEntry{}
	if err := addPostgresAggregatedAccountQualityEntry(target, w10cQualityRow(func(r *UsageStatsRecordRow) {
		r.CreatedAt = "2026-04-18T10:14:00.000Z"
	}), timeKeys); err != nil {
		t.Fatal(err)
	}
	target["acc-1\x002026-04-18T10:15"].LastSuccessAt = "garbage"
	if err := addPostgresAggregatedAccountQualityEntry(target, w10cQualityRow(nil), timeKeys); err != nil {
		t.Fatal(err)
	}
	if target["acc-1\x002026-04-18T10:15"].LastSuccessAt != "garbage" {
		t.Fatalf("非法既有 LastSuccessAt 应保持不变")
	}
}

// ---- addPostgresAggregatedAccountHealthEntry 全臂 ----

func w10cHealthRow(mutate func(*UsageStatsRecordRow)) UsageStatsRecordRow {
	row := UsageStatsRecordRow{
		ID: "rec-h", SystemAccountID: "alice", TraceID: "tr", TrafficSource: "account_health_check",
		AccountID: strPtr("acc-1"), APIKeyID: strPtr("key-1"), Success: 1, CreatedAt: "2026-04-18T10:30:00.000Z",
		ProviderCode: strPtr("openai"),
		// 聚合主循环先经 ShouldAggregateUsageStatsRecord 过滤：health 记录
		// 带 account 维度时同样需要完整 owner 元数据。
		AccountOwnerSystemAccountID: strPtr("alice"), AccountAccessType: strPtr("owner"),
	}
	if mutate != nil {
		mutate(&row)
	}
	return row
}

func TestW10CPostgresAccountHealthEntry(t *testing.T) {
	statHour := "2026-04-18T10"
	key := "acc-1\x00" + statHour

	// 非 health check 流量来源 / account 缺失臂。
	target := map[string]*aggregatedAccountHealthEntry{}
	addPostgresAggregatedAccountHealthEntry(target, w10cHealthRow(func(r *UsageStatsRecordRow) { r.TrafficSource = "gateway" }), statHour)
	addPostgresAggregatedAccountHealthEntry(target, w10cHealthRow(func(r *UsageStatsRecordRow) { r.AccountID = nil }), statHour)
	if len(target) != 0 {
		t.Fatalf("非 health check 不应记健康")
	}

	// 新条目：success 状态 + provider code。
	target = map[string]*aggregatedAccountHealthEntry{}
	addPostgresAggregatedAccountHealthEntry(target, w10cHealthRow(nil), statHour)
	if got := target[key]; got == nil || got.Status != "success" || got.ProviderCode != "openai" || got.LastRecordID != "rec-h" {
		t.Fatalf("健康新条目错误: %+v", got)
	}

	// 失败状态 + provider code 缺省 unknown。
	addPostgresAggregatedAccountHealthEntry(target, w10cHealthRow(func(r *UsageStatsRecordRow) {
		r.ID = "rec-h2"
		r.Success = 0
		r.ProviderCode = nil
		r.CreatedAt = "2026-04-18T10:45:00.000Z"
	}), statHour)
	if got := target[key]; got == nil || got.Status != "failure" || got.ProviderCode != "unknown" || got.LastObservedAt != "2026-04-18T10:45:00.000Z" {
		t.Fatalf("健康失败条目错误: %+v", got)
	}

	// 已有条目更旧 → 保留（comparison > 0 分支）。
	stale := map[string]*aggregatedAccountHealthEntry{key: {AccountID: "acc-1", StatHour: statHour, LastObservedAt: "2026-04-18T11:00:00.000Z", LastRecordID: "rec-early"}}
	addPostgresAggregatedAccountHealthEntry(stale, w10cHealthRow(nil), statHour)
	if stale[key].LastObservedAt != "2026-04-18T11:00:00.000Z" {
		t.Fatalf("更旧的行不应覆盖: %+v", stale[key])
	}

	// 同时间戳且既有 last_record_id 更大 → 保留。
	sameTs := map[string]*aggregatedAccountHealthEntry{key: {AccountID: "acc-1", StatHour: statHour, LastObservedAt: "2026-04-18T10:30:00.000Z", LastRecordID: "rec-zzz"}}
	addPostgresAggregatedAccountHealthEntry(sameTs, w10cHealthRow(nil), statHour)
	if sameTs[key].LastRecordID != "rec-zzz" {
		t.Fatalf("同时间戳旧 record id 不应被覆盖")
	}

	// 同时间戳但新 record id 更大 → 覆盖（comparison == 0 && id < 分支）。
	sameTs2 := map[string]*aggregatedAccountHealthEntry{key: {AccountID: "acc-1", StatHour: statHour, LastObservedAt: "2026-04-18T10:30:00.000Z", LastRecordID: "rec-aaa"}}
	addPostgresAggregatedAccountHealthEntry(sameTs2, w10cHealthRow(nil), statHour)
	if sameTs2[key].LastRecordID != "rec-h" {
		t.Fatalf("同时间戳新 record id 应覆盖")
	}
}

// ---- addPostgresAggregatedUsageErrorEntries 全臂 ----

func TestW10CPostgresUsageErrorEntries(t *testing.T) {
	timeKeys := UsageStatsTimeKeys{StatHour: "2026-04-18T10", StatDate: "2026-04-18"}

	// error code 优先取 ErrorCode。
	target := map[statsErrorKey]*aggregatedUsageErrorEntry{}
	addPostgresAggregatedUsageErrorEntries(target, UsageStatsRecordRow{
		ID: "rec-e", SystemAccountID: "bob", Success: 0, ProviderCode: strPtr("openai"),
		ErrorCode: strPtr("rate_limited"), StatusCode: f64Ptr(429), ErrorMessage: strPtr("too many"),
		CreatedAt: "2026-04-18T10:20:00.000Z",
	}, timeKeys)
	key := statsErrorKey{"usage_error_hourly", "2026-04-18T10", "bob", "openai", "openai", "rate_limited", 429}
	if got := target[key]; got == nil || got.RequestCount != 1 || got.ErrorMessage == nil || *got.ErrorMessage != "too many" {
		t.Fatalf("error code 臂错误: %+v", got)
	}

	// 无 ErrorCode → String(status_code)。
	target = map[statsErrorKey]*aggregatedUsageErrorEntry{}
	addPostgresAggregatedUsageErrorEntries(target, UsageStatsRecordRow{
		ID: "rec-e", SystemAccountID: "bob", Success: 0, ProviderCode: strPtr("openai"), StatusCode: f64Ptr(502),
	}, timeKeys)
	key = statsErrorKey{"usage_error_hourly", "2026-04-18T10", "bob", "openai", "openai", "502", 502}
	if got := target[key]; got == nil {
		t.Fatalf("status code 臂缺失: %+v", target)
	}

	// 双缺省 → unknown。
	target = map[statsErrorKey]*aggregatedUsageErrorEntry{}
	addPostgresAggregatedUsageErrorEntries(target, UsageStatsRecordRow{
		ID: "rec-e", SystemAccountID: "bob", Success: 0, ProviderCode: nil,
	}, timeKeys)
	key = statsErrorKey{"usage_error_hourly", "2026-04-18T10", "bob", "unknown", "unknown", "unknown", 0}
	if got := target[key]; got == nil {
		t.Fatalf("unknown 臂缺失: %+v", target)
	}

	// 既有条目累加且 ErrorMessage 为 nil 时保留旧值。
	addPostgresAggregatedUsageErrorEntries(target, UsageStatsRecordRow{
		ID: "rec-e2", SystemAccountID: "bob", Success: 0, ProviderCode: nil,
	}, timeKeys)
	if got := target[key]; got == nil || got.RequestCount != 2 || got.ErrorCount != 2 || got.ErrorMessage != nil {
		t.Fatalf("累加臂错误: %+v", got)
	}

	// global system account 维度同步生成。
	globalKey := statsErrorKey{"usage_error_hourly", "2026-04-18T10", GlobalStatsSystemAccountID, "unknown", "unknown", "unknown", 0}
	if got := target[globalKey]; got == nil || got.RequestCount != 2 {
		t.Fatalf("global 维度缺失: %+v", target)
	}
}

// ---- 稳定水印序列化 ----

func TestW10CStableWatermarkSerialization(t *testing.T) {
	// stableWatermarkValue 全类型臂。
	cases := []struct {
		value any
		want  string
	}{
		{nil, "null"},
		{true, "true"},
		{false, "false"},
		{int64(42), "42"},
		{int(7), "7"},
		{float64(3), "3"},
		{float64(-3.5), "-3.5"},
		{float64(1e16), "1e+16"},
		{"plain", `"plain"`},
		{`a"b\c` + "\n\r\t" + "\x01", `"a\"b\\c\n\r\t\u0001"`},
		{struct{ X int }{X: 1}, "null"},
	}
	for _, testCase := range cases {
		if got := stableWatermarkValue(testCase.value); got != testCase.want {
			t.Fatalf("stableWatermarkValue(%v) = %s want %s", testCase.value, got, testCase.want)
		}
	}

	// normalizeRawValue：[]byte → string、time.Time → RFC3339 毫秒。
	if got := normalizeRawValue([]byte("text")); got != "text" {
		t.Fatalf("normalizeRawValue []byte = %v", got)
	}
	stamp := time.Date(2026, 4, 18, 10, 0, 0, 0, time.UTC)
	if got := normalizeRawValue(stamp); got != "2026-04-18T10:00:00.000Z" {
		t.Fatalf("normalizeRawValue time = %v", got)
	}
	if got := normalizeRawValue(float64(1.5)); got != float64(1.5) {
		t.Fatalf("normalizeRawValue 透传臂 = %v", got)
	}

	// abs 双臂。
	if abs(-2.5) != 2.5 || abs(2.5) != 2.5 {
		t.Fatalf("abs 断言失败")
	}

	// requireSystemMetricsTrendSourceVersion。
	digest := strings.Repeat("a", 64)
	if err := requireSystemMetricsTrendSourceVersion("v2:" + digest); err != nil {
		t.Fatalf("合法 sourceVersion 报错: %v", err)
	}
	for _, bad := range []string{"v2:", "v1:" + digest, "v2:" + strings.Repeat("g", 64), "v2:" + digest + "extra"} {
		if err := requireSystemMetricsTrendSourceVersion(bad); err == nil {
			t.Fatalf("非法 sourceVersion %q 应报错", bad)
		}
	}
}

func TestW10CProcessEventLoopRoleValidation(t *testing.T) {
	// 固定角色与 worker:replica 白名单。
	for _, valid := range []string{"server", "ingest-worker", "stats-worker", "ops-worker", "db-service", "usage-worker:3", "log-worker:8", "gateway:edge-1", "control-replica:main"} {
		if !isValidProcessEventLoopRole(valid) {
			t.Fatalf("角色 %q 应合法", valid)
		}
	}
	// 非法：空、超长、纯前缀、随机串、副本超界。
	longRole := strings.Repeat("x", 97)
	for _, invalid := range []string{"", longRole, "gateway:", "random-role", "ingest-worker:9", "ingest-worker:99"} {
		if isValidProcessEventLoopRole(invalid) {
			t.Fatalf("角色 %q 应非法", invalid)
		}
	}
	// 96 字符以内随机前缀臂。
	if isValidProcessEventLoopRole(strings.Repeat("x", 96)) {
		t.Fatalf("96 字符非前缀角色应非法")
	}
}

// ---- RFC3339 工具长尾臂 ----

func TestW10CRFC3339Helpers(t *testing.T) {
	// sign 负 offset 分支：-08:00 规范化为 UTC。
	normalized, ok := CanonicalizeRFC3339Instant("2026-04-18T02:00:00-08:00")
	if !ok || normalized != "2026-04-18T10:00:00.000Z" {
		t.Fatalf("负 offset 规范化失败: %s ok=%v", normalized, ok)
	}
	// RequiredRFC3339Instant 错误臂。
	if _, err := RequiredRFC3339Instant("nope", "测试时间"); err == nil {
		t.Fatalf("RequiredRFC3339Instant 非法输入应报错")
	}
	// CompareUsageStatsTimestamp 错误臂。
	if _, err := CompareUsageStatsTimestamp("2026-04-18T10:00:00.000Z", "nope"); err == nil {
		t.Fatalf("CompareUsageStatsTimestamp 非法输入应报错")
	}
	// MaxOptionalISO 全臂。
	if got, err := MaxOptionalISO("", ""); err != nil || got != "" {
		t.Fatalf("MaxOptionalISO 双缺省 = %s err=%v", got, err)
	}
	if got, err := MaxOptionalISO("", "2026-04-18T10:00:00.000Z"); err != nil || got != "2026-04-18T10:00:00.000Z" {
		t.Fatalf("MaxOptionalISO 右值臂 = %s err=%v", got, err)
	}
	if got, err := MaxOptionalISO("2026-04-18T10:00:00.000Z", ""); err != nil || got != "2026-04-18T10:00:00.000Z" {
		t.Fatalf("MaxOptionalISO 左值臂 = %s err=%v", got, err)
	}
	if got, err := MaxOptionalISO("2026-04-18T09:00:00.000Z", "2026-04-18T10:00:00.000Z"); err != nil || got != "2026-04-18T10:00:00.000Z" {
		t.Fatalf("MaxOptionalISO 取大臂 = %s err=%v", got, err)
	}
	if _, err := MaxOptionalISO("garbage", ""); err == nil {
		t.Fatalf("MaxOptionalISO 非法左值应报错")
	}
	// statsLagSecondsFromCursor：未来时间戳 → 0（负 lag 钳制）。
	future := time.Date(2026, 4, 18, 12, 0, 0, 0, time.UTC)
	if got := statsLagSecondsFromCursor("2026-04-18T12:01:00.000Z", future); got != 0 {
		t.Fatalf("负 lag 应钳制为 0, got %v", got)
	}
	if got := statsLagSecondsFromCursor("2026-04-18T11:59:30.000Z", future); got != 30 {
		t.Fatalf("正 lag = %v want 30", got)
	}
	// statsLagSecondsFromCursor 非法输入 panic 臂（游标时间非 RFC3339 时触发；
	// 生产聚合游标写入前已规范化，直接单测覆盖 panic 语义）。
	defer func() {
		if recover() == nil {
			t.Fatalf("非法 cursor 应 panic")
		}
	}()
	statsLagSecondsFromCursor("garbage", future)
}

// ---- ShouldAggregateUsageStatsRecord 校验矩阵 ----

func TestW10CShouldAggregateRecordValidation(t *testing.T) {
	valid := UsageStatsRecordRow{
		SystemAccountID: "alice", Success: 1,
		AccountID: strPtr("acc-1"), AccountOwnerSystemAccountID: strPtr("alice"), AccountAccessType: strPtr("owner"),
		GroupID: strPtr("grp-1"), GroupOwnerSystemAccountID: strPtr("alice"), GroupAccessType: strPtr("owner"),
	}
	if !ShouldAggregateUsageStatsRecord(valid) {
		t.Fatalf("最小合法行应通过")
	}

	cases := []struct {
		name   string
		mutate func(*UsageStatsRecordRow)
	}{
		{"account id 非规范空白", func(r *UsageStatsRecordRow) { r.AccountID = strPtr(" acc-1") }},
		{"group id 空白", func(r *UsageStatsRecordRow) { r.GroupID = strPtr("grp-1 ") }},
		{"owner system account 空白", func(r *UsageStatsRecordRow) { r.AccountOwnerSystemAccountID = strPtr("alice ") }},
		{"account access type 非法", func(r *UsageStatsRecordRow) { r.AccountAccessType = strPtr("other") }},
		{"group access type 非法", func(r *UsageStatsRecordRow) { r.GroupAccessType = strPtr("other") }},
		{"account authz source type 非法", func(r *UsageStatsRecordRow) {
			r.AccountAccessType = strPtr("account_authorized")
			r.AccountAuthorizationID = strPtr("auth-1")
			r.AccountAuthorizationSourceType = strPtr("other")
		}},
		{"team source 缺 team id", func(r *UsageStatsRecordRow) {
			r.AccountAccessType = strPtr("account_authorized")
			r.AccountAuthorizationID = strPtr("auth-1")
			r.AccountAuthorizationSourceType = strPtr("team")
		}},
		{"team source 带 team id 但类型 manual", func(r *UsageStatsRecordRow) {
			r.AccountAccessType = strPtr("account_authorized")
			r.AccountAuthorizationID = strPtr("auth-1")
			r.AccountAuthorizationSourceType = strPtr("manual")
			r.AccountAuthorizationSourceTeamID = strPtr("team-1")
		}},
		{"有 account 缺 owner", func(r *UsageStatsRecordRow) { r.AccountOwnerSystemAccountID = nil }},
		{"有 account 缺 access type", func(r *UsageStatsRecordRow) { r.AccountAccessType = nil }},
		{"无 account 带 owner 字段", func(r *UsageStatsRecordRow) {
			r.AccountID = nil
			r.AccountAccessType = nil
		}},
		{"无 group 带 group owner", func(r *UsageStatsRecordRow) {
			r.GroupID = nil
			r.GroupAccessType = nil
		}},
		{"有 group 缺 group owner", func(r *UsageStatsRecordRow) { r.GroupOwnerSystemAccountID = nil }},
		{"account_authorized 缺 authz id", func(r *UsageStatsRecordRow) { r.AccountAccessType = strPtr("account_authorized") }},
		{"group_authorized 缺 authz id", func(r *UsageStatsRecordRow) {
			r.AccountAccessType = strPtr("group_authorized")
			r.AccountAuthorizationID = strPtr("auth-1")
		}},
		{"非 authorized 带 group authz id", func(r *UsageStatsRecordRow) {
			r.GroupAuthorizationID = strPtr("gauth-1")
		}},
		{"非 authorized 带 group source type", func(r *UsageStatsRecordRow) {
			r.GroupAuthorizationSourceType = strPtr("manual")
		}},
	}
	for _, testCase := range cases {
		row := valid
		testCase.mutate(&row)
		if ShouldAggregateUsageStatsRecord(row) {
			t.Fatalf("%s 应拒绝", testCase.name)
		}
	}

	// 合法全量行（account_authorized + 授权 + group_authorized）。
	full := UsageStatsRecordRow{
		SystemAccountID: "alice", Success: 1,
		AccountID: strPtr("acc-1"), AccountOwnerSystemAccountID: strPtr("alice"), AccountAccessType: strPtr("account_authorized"),
		AccountAuthorizationID: strPtr("auth-1"), AccountAuthorizationSourceType: strPtr("manual"),
		GroupID:                   strPtr("grp-1"),
		GroupOwnerSystemAccountID: strPtr("bob"), GroupAccessType: strPtr("authorized"), GroupAuthorizationID: strPtr("gauth-1"),
		GroupAuthorizationSourceType: strPtr("manual"),
	}
	if !ShouldAggregateUsageStatsRecord(full) {
		t.Fatalf("全量合法行应通过")
	}
	// team 授权合法形态。
	full.AccountAuthorizationSourceType = strPtr("team")
	full.AccountAuthorizationSourceTeamID = strPtr("team-1")
	if !ShouldAggregateUsageStatsRecord(full) {
		t.Fatalf("team 授权合法行应通过")
	}
}

// ---- Aggregator 选项错误臂 ----

type w10cErrorClock struct{}

func (w10cErrorClock) StatsTimezone(context.Context) (*time.Location, error) {
	return nil, errors.New("clock 失败")
}

func TestW10CAggregatorOptionErrors(t *testing.T) {
	env := newTestEnv(t)
	env.seedGoldenPair()
	aggregator := env.aggregator()

	// 非法 SafeCreatedBefore。
	if _, err := aggregator.AggregateUsageStatsBatch(context.Background(), AggregateOptions{SafeCreatedBefore: "nope"}); err == nil {
		t.Fatalf("非法 safeCreatedBefore 应报错")
	}

	// Clock 失败透传。
	failing := &Aggregator{DB: env.db, Dialect: env.dialect, Clock: w10cErrorClock{}, Now: func() time.Time { return env.now }}
	if _, err := failing.AggregateUsageStatsBatch(context.Background(), AggregateOptions{}); err == nil || err.Error() != "clock 失败" {
		t.Fatalf("clock 失败应透传, got %v", err)
	}

	// StaticTimezoneSource nil location 错误臂。
	if _, err := (StaticTimezoneSource{}).StatsTimezone(context.Background()); err == nil {
		t.Fatalf("nil location 应报错")
	}

	// statsJobState：库里非法 cursor_created_at → 错误臂。
	env.exec(`INSERT INTO stats_job_state (scope_type, scope_id, job_name, cursor_created_at, cursor_id, updated_at)
		VALUES ('global', '', 'usage_stats_aggregation', 'garbage', 'rec-1', '2026-04-18T12:00:00.000Z')`)
	if _, err := aggregator.AggregateUsageStatsBatch(context.Background(), AggregateOptions{}); err == nil {
		t.Fatalf("非法 cursor 应报错")
	}
}

// ---- 排行快照 stage（SQLite）----

func TestW10CRankSnapshotStagesSQLite(t *testing.T) {
	env := newTestEnv(t)
	// caller_account 7 日请求排行源数据（today=2026-04-18 → 窗口 >= 2026-04-12）。
	env.exec(`INSERT INTO usage_stats_daily (system_account_id, scope_type, scope_id, stat_date, request_count, total_cost_usd, last_used_at, updated_at)
		VALUES
		('alice', 'caller_account', 'acc-1', '2026-04-15', 3, 0.5, '2026-04-15T10:00:00.000Z', '2026-04-15T10:00:00.000Z'),
		('alice', 'caller_account', 'acc-2', '2026-04-16', 5, 0.9, '2026-04-16T10:00:00.000Z', '2026-04-16T10:00:00.000Z'),
		('alice', 'caller_account', 'acc-old', '2026-04-01', 99, 9, '2026-04-01T10:00:00.000Z', '2026-04-01T10:00:00.000Z')`)

	result, err := env.refresher().RunStages(context.Background(), []WindowStageName{StageCallerAccountLast7dRequestRank}, RefreshOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Stages) != 1 || result.Stages[0].Name != string(StageCallerAccountLast7dRequestRank) {
		t.Fatalf("stage 结果错误: %+v", result.Stages)
	}
	// 窗口外行不参与，rank 按 request_count DESC。
	if got := env.queryString(`SELECT scope_id FROM usage_rank_snapshots WHERE scope_type='caller_account' ORDER BY rank ASC`); len(got) != 2 || got[0] != "acc-2" || got[1] != "acc-1" {
		t.Fatalf("caller_account 排行错误: %v", got)
	}

	// 授权月成本排行：account_authorization 与 group_authorization 两个 scope。
	env.exec(`INSERT INTO usage_stats_monthly (system_account_id, scope_type, scope_id, stat_month, request_count, total_cost_usd, last_used_at, updated_at)
		VALUES
		('alice', 'account_authorization', 'auth-1', '2026-04', 2, 3.5, '2026-04-10T10:00:00.000Z', '2026-04-10T10:00:00.000Z'),
		('alice', 'group_authorization', 'gauth-1', '2026-04', 1, 2.5, '2026-04-11T10:00:00.000Z', '2026-04-11T10:00:00.000Z')`)
	if _, err := env.refresher().RunStages(context.Background(), []WindowStageName{StageAccountAuthorizationCurrentMonthRank, StageGroupAuthorizationCurrentMonthRank}, RefreshOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := env.queryString(`SELECT scope_id FROM usage_rank_snapshots WHERE scope_type='account_authorization'`); len(got) != 1 || got[0] != "auth-1" {
		t.Fatalf("account_authorization 排行错误: %v", got)
	}
	if got := env.queryString(`SELECT scope_id FROM usage_rank_snapshots WHERE scope_type='group_authorization'`); len(got) != 1 || got[0] != "gauth-1" {
		t.Fatalf("group_authorization 排行错误: %v", got)
	}

	// 非法 scope_type 错误臂（直接调用）。
	tx, err := env.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := refreshAuthorizationCurrentMonthCostRankSnapshot(context.Background(), tx, env.dialect, "bad_scope", "2026-04-18T12:00:00.000Z", "2026-04-18T12:00:00.000Z", env.zone, env.now); err == nil || !strings.Contains(err.Error(), "bad_scope") {
		t.Fatalf("非法授权排行 scope 应报错, got %v", err)
	}
}

// ---- 账户健康 UPSERT（SQLite）----

func TestW10CAccountHealthUpsertSQLite(t *testing.T) {
	env := newTestEnv(t)
	// 预置既有行：一条更新（10:00 < 10:30 → 覆盖）、一条拒绝（11:00 > 10:30 → 保留）。
	env.exec(`INSERT INTO account_health_hourly (account_id, system_account_id, provider_code, stat_hour, status, last_observed_at, last_record_id, updated_at)
		VALUES
		('acc-older', 'alice', 'openai', '2026-04-18T10', 'failure', '2026-04-18T10:00:00.000Z', 'rec-old', '2026-04-18T10:00:00.000Z'),
		('acc-newer', 'alice', 'openai', '2026-04-18T10', 'failure', '2026-04-18T11:00:00.000Z', 'rec-new', '2026-04-18T11:00:00.000Z')`)
	env.seedUsageRecord(w10cHealthRow(func(r *UsageStatsRecordRow) {
		r.ID = "rec-h1"
		r.AccountID = strPtr("acc-older")
	}))
	env.seedUsageRecord(w10cHealthRow(func(r *UsageStatsRecordRow) {
		r.ID = "rec-h2"
		r.AccountID = strPtr("acc-newer")
	}))
	if _, err := env.aggregator().AggregateUsageStatsBatch(context.Background(), AggregateOptions{BatchSize: 10}); err != nil {
		t.Fatal(err)
	}
	older := env.queryString(`SELECT status || '|' || last_record_id FROM account_health_hourly WHERE account_id='acc-older'`)
	if len(older) != 1 || older[0] != "success|rec-h1" {
		t.Fatalf("较旧既有行应被覆盖: %v", older)
	}
	newer := env.queryString(`SELECT status || '|' || last_record_id FROM account_health_hourly WHERE account_id='acc-newer'`)
	if len(newer) != 1 || newer[0] != "failure|rec-new" {
		t.Fatalf("较新既有行应保留: %v", newer)
	}
	// 质量统计与脏账户同步落库（traffic source=gateway + account + api_key）。
	if got := env.queryRowFloats(`SELECT COUNT(*) FROM account_quality_minute_stats WHERE account_id='acc-older'`); got[0] != 1 {
		t.Fatalf("质量统计未落库: %v", got)
	}
	if got := env.queryRowFloats(`SELECT COUNT(*) FROM account_quality_dirty_accounts`); got[0] != 2 {
		t.Fatalf("质量脏账户未标记: %v", got)
	}
}

// ---- rank refresh job state 生命周期 ----

func w10cSeedJobState(t *testing.T, env *testEnv, jobName, cursorCreatedAt, cursorID string) {
	t.Helper()
	env.exec(`INSERT INTO stats_job_state (scope_type, scope_id, job_name, cursor_created_at, cursor_id, updated_at)
		VALUES (?, '', ?, ?, ?, '2026-04-18T12:00:00.000Z')`, RankSnapshotJobStateScopeType, jobName, cursorCreatedAt, cursorID)
}

func TestW10CRankJobStateLifecycle(t *testing.T) {
	env := newTestEnv(t)
	refresher := env.refresher()
	ctx := context.Background()

	// 无状态行 → nil。
	state, err := refresher.rankRefreshJobState(ctx, "w10c-job", false)
	if err != nil || state != nil {
		t.Fatalf("无状态行应返回 nil, got %+v err=%v", state, err)
	}

	// 合法水位行。
	w10cSeedJobState(t, env, "w10c-job", "2026-04-18T10:00:00.000Z", "2026-04-18")
	state, err = refresher.rankRefreshJobState(ctx, "w10c-job", false)
	if err != nil || state == nil || state.cursorCreatedAt == nil || *state.cursorCreatedAt != "2026-04-18T10:00:00.000Z" || state.cursorID == nil || *state.cursorID != "2026-04-18" {
		t.Fatalf("合法水位解析错误: %+v err=%v", state, err)
	}

	// 非法水位（无 legacy 分支）→ 错误。
	w10cSeedJobState(t, env, "w10c-bad", "garbage", "")
	if _, err := refresher.rankRefreshJobState(ctx, "w10c-bad", false); err == nil {
		t.Fatalf("非法水位应报错")
	}

	// legacy 空水位。
	w10cSeedJobState(t, env, "w10c-legacy-empty", LegacyEmptySourceWatermark, "")
	state, err = refresher.rankRefreshJobState(ctx, "w10c-legacy-empty", false)
	if err != nil || state == nil || !state.legacyEmptySourceWatermark {
		t.Fatalf("legacy 空水位解析错误: %+v err=%v", state, err)
	}

	// legacy "wm|ver"：allowLegacy=true 拆分；非法水位报错。
	w10cSeedJobState(t, env, "w10c-legacy", "2026-04-17T10:00:00.000Z|v2:"+strings.Repeat("a", 64), "")
	state, err = refresher.rankRefreshJobState(ctx, "w10c-legacy", true)
	if err != nil || state == nil || state.legacySourceVersion == "" || state.cursorCreatedAt == nil || *state.cursorCreatedAt != "2026-04-17T10:00:00.000Z" {
		t.Fatalf("legacy 拆分错误: %+v err=%v", state, err)
	}
	w10cSeedJobState(t, env, "w10c-legacy-bad", "garbage|v2:"+strings.Repeat("a", 64), "")
	if _, err := refresher.rankRefreshJobState(ctx, "w10c-legacy-bad", true); err == nil {
		t.Fatalf("legacy 非法水位应报错")
	}
	// allowLegacy=false 时 legacy 复合值无法规范化 → 错误。
	if _, err := refresher.rankRefreshJobState(ctx, "w10c-legacy", false); err == nil {
		t.Fatalf("非 legacy 模式应拒绝复合值")
	}

	// previousSourceVersion：状态全缺省 → 空。
	emptyState, err := refresher.previousSourceVersion(ctx, "w10c-job", nil)
	if err != nil || emptyState != "" {
		t.Fatalf("无状态 previousSourceVersion = %s err=%v", emptyState, err)
	}
	// 主状态缺失但 sourceVersion 行存在 → 错误。
	env.exec(`INSERT INTO stats_job_state (scope_type, scope_id, job_name, cursor_created_at, cursor_id, updated_at)
		VALUES (?, '', 'w10c-main-missing', '2026-04-18T10:00:00.000Z', ?, '2026-04-18T12:00:00.000Z')`,
		RankSnapshotSourceVersionScopeType, "v2:"+strings.Repeat("b", 64))
	if _, err := refresher.previousSourceVersion(ctx, "w10c-main-missing", nil); err == nil || !strings.Contains(err.Error(), "主刷新状态") {
		t.Fatalf("主状态缺失应报错, got %v", err)
	}
	// 主状态 cursorCreatedAt 缺失 → 错误。
	if _, err := refresher.previousSourceVersion(ctx, "w10c-job", &rankRefreshJobState{}); err == nil || !strings.Contains(err.Error(), "主刷新状态缺少 sourceWatermark") {
		t.Fatalf("主状态缺水位应报错, got %v", err)
	}
	// legacy version：无 sourceVersion 行 → 透传 legacy 值。
	legacyState := &rankRefreshJobState{cursorCreatedAt: strPtr("2026-04-17T10:00:00.000Z"), legacySourceVersion: "legacy-ver"}
	if got, err := refresher.previousSourceVersion(ctx, "w10c-no-version-row", legacyState); err != nil || got != "legacy-ver" {
		t.Fatalf("legacy 无 version 行应透传, got %s err=%v", got, err)
	}
	// legacy version 与 version 行一致 → 透传。
	env.exec(`INSERT INTO stats_job_state (scope_type, scope_id, job_name, cursor_created_at, cursor_id, updated_at)
		VALUES (?, '', 'w10c-legacy-match', '2026-04-17T10:00:00.000Z', 'legacy-ver', '2026-04-18T12:00:00.000Z')`, RankSnapshotSourceVersionScopeType)
	if got, err := refresher.previousSourceVersion(ctx, "w10c-legacy-match", legacyState); err != nil || got != "legacy-ver" {
		t.Fatalf("legacy 一致应透传, got %s err=%v", got, err)
	}
	// legacy version 不一致 → 错误。
	env.exec(`INSERT INTO stats_job_state (scope_type, scope_id, job_name, cursor_created_at, cursor_id, updated_at)
		VALUES (?, '', 'w10c-legacy-mismatch', '2026-04-17T10:00:00.000Z', 'other-ver', '2026-04-18T12:00:00.000Z')`, RankSnapshotSourceVersionScopeType)
	if _, err := refresher.previousSourceVersion(ctx, "w10c-legacy-mismatch", legacyState); err == nil || !strings.Contains(err.Error(), "不一致") {
		t.Fatalf("legacy 不一致应报错, got %v", err)
	}
	// 非 legacy：sourceVersion 行缺失 → 错误。
	plainState := &rankRefreshJobState{cursorCreatedAt: strPtr("2026-04-18T10:00:00.000Z")}
	if _, err := refresher.previousSourceVersion(ctx, "w10c-no-version-row-2", plainState); err == nil || !strings.Contains(err.Error(), "缺失") {
		t.Fatalf("version 行缺失应报错, got %v", err)
	}
	// 非 legacy：水位不一致 → 错误。
	env.exec(`INSERT INTO stats_job_state (scope_type, scope_id, job_name, cursor_created_at, cursor_id, updated_at)
		VALUES (?, '', 'w10c-ver', '2026-04-18T09:00:00.000Z', ?, '2026-04-18T12:00:00.000Z')`,
		RankSnapshotSourceVersionScopeType, "v2:"+strings.Repeat("c", 64))
	if _, err := refresher.previousSourceVersion(ctx, "w10c-ver", plainState); err == nil || !strings.Contains(err.Error(), "水位不一致") {
		t.Fatalf("水位不一致应报错, got %v", err)
	}
	// 非 legacy：一致 → 返回 version。
	env.exec(`INSERT INTO stats_job_state (scope_type, scope_id, job_name, cursor_created_at, cursor_id, updated_at)
		VALUES (?, '', 'w10c-ver-ok', '2026-04-18T10:00:00.000Z', ?, '2026-04-18T12:00:00.000Z')`,
		RankSnapshotSourceVersionScopeType, "v2:"+strings.Repeat("d", 64))
	if got, err := refresher.previousSourceVersion(ctx, "w10c-ver-ok", plainState); err != nil || got != "v2:"+strings.Repeat("d", 64) {
		t.Fatalf("一致应返回 version, got %s err=%v", got, err)
	}
	// version 行缺 cursor_created_at / cursor_id → 错误臂。
	env.exec(`INSERT INTO stats_job_state (scope_type, scope_id, job_name, cursor_created_at, cursor_id, updated_at)
		VALUES (?, '', 'w10c-ver-empty-wm', NULL, 'x', '2026-04-18T12:00:00.000Z')`, RankSnapshotSourceVersionScopeType)
	if _, err := refresher.previousSourceVersion(ctx, "w10c-ver-empty-wm", nil); err == nil || !strings.Contains(err.Error(), "缺少 sourceWatermark") {
		t.Fatalf("version 行缺水位应报错, got %v", err)
	}
	env.exec(`INSERT INTO stats_job_state (scope_type, scope_id, job_name, cursor_created_at, cursor_id, updated_at)
		VALUES (?, '', 'w10c-ver-empty-ver', '2026-04-18T10:00:00.000Z', NULL, '2026-04-18T12:00:00.000Z')`, RankSnapshotSourceVersionScopeType)
	if _, err := refresher.previousSourceVersion(ctx, "w10c-ver-empty-ver", nil); err == nil || !strings.Contains(err.Error(), "缺少 sourceVersion") {
		t.Fatalf("version 行缺 version 应报错, got %v", err)
	}

	// rankSnapshotSourceUnchanged 全臂。
	sourceState := &rankSnapshotSourceState{sourceWatermark: "2026-04-18T10:00:00.000Z", sourceVersion: "v2:" + strings.Repeat("d", 64)}
	if rankSnapshotSourceUnchanged(nil, sourceState, "2026-04-18", "") {
		t.Fatalf("previousState 缺失应视为变化")
	}
	if rankSnapshotSourceUnchanged(plainState, nil, "2026-04-18", "") {
		t.Fatalf("sourceState 缺失应视为变化")
	}
	if rankSnapshotSourceUnchanged(&rankRefreshJobState{legacyEmptySourceWatermark: true, cursorCreatedAt: strPtr(sourceState.sourceWatermark), cursorID: strPtr("2026-04-18")}, sourceState, "2026-04-18", "") {
		t.Fatalf("legacy 空水位应视为变化")
	}
	if rankSnapshotSourceUnchanged(&rankRefreshJobState{legacySourceVersion: "x", cursorCreatedAt: strPtr(sourceState.sourceWatermark), cursorID: strPtr("2026-04-18")}, sourceState, "2026-04-18", "") {
		t.Fatalf("legacy version 应视为变化（迁移强制刷新）")
	}
	if rankSnapshotSourceUnchanged(&rankRefreshJobState{cursorCreatedAt: strPtr("other"), cursorID: strPtr("2026-04-18")}, sourceState, "2026-04-18", sourceState.sourceVersion) {
		t.Fatalf("水位不一致应视为变化")
	}
	if rankSnapshotSourceUnchanged(&rankRefreshJobState{cursorCreatedAt: strPtr(sourceState.sourceWatermark)}, sourceState, "2026-04-18", sourceState.sourceVersion) {
		t.Fatalf("refreshDate 不一致应视为变化")
	}
	if rankSnapshotSourceUnchanged(&rankRefreshJobState{cursorCreatedAt: strPtr(sourceState.sourceWatermark), cursorID: strPtr("2026-04-18")}, sourceState, "2026-04-18", "stale-version") {
		t.Fatalf("sourceVersion 不一致应视为变化")
	}
	if !rankSnapshotSourceUnchanged(&rankRefreshJobState{cursorCreatedAt: strPtr(sourceState.sourceWatermark), cursorID: strPtr("2026-04-18")}, sourceState, "2026-04-18", sourceState.sourceVersion) {
		t.Fatalf("全部一致应视为未变化")
	}

	// sourceWatermarkOrEmpty nil 接收器臂。
	var nilState *rankSnapshotSourceState
	if nilState.sourceWatermarkOrEmpty() != "" {
		t.Fatalf("nil 接收器应返回空")
	}

	// updateRankRefreshJobState 错误臂。
	if err := refresher.updateRankRefreshJobState(ctx, "w10c-job", rankRefreshStateInput{SourceWatermark: "garbage", LastSuccessAt: "2026-04-18T12:00:00.000Z"}); err == nil {
		t.Fatalf("非法水位应报错")
	}
	if err := refresher.updateRankRefreshJobState(ctx, "w10c-job", rankRefreshStateInput{
		SourceWatermark: "2026-04-18T10:00:00.000Z", SourceVersion: "bad-version", LastSuccessAt: "2026-04-18T12:00:00.000Z",
	}); err == nil {
		t.Fatalf("非法 sourceVersion 应报错")
	}
	if err := refresher.updateRankRefreshJobState(ctx, "w10c-job", rankRefreshStateInput{SourceWatermark: "2026-04-18T10:00:00.000Z", LastSuccessAt: "garbage"}); err == nil {
		t.Fatalf("非法 lastSuccessAt 应报错")
	}
}

func TestW10CUpdateRankRefreshJobStateRoundtrip(t *testing.T) {
	env := newTestEnv(t)
	refresher := env.refresher()
	ctx := context.Background()
	version := "v2:" + strings.Repeat("e", 64)

	// 带 sourceVersion → 主行 + version 行。
	if err := refresher.updateRankRefreshJobState(ctx, "w10c-rt", rankRefreshStateInput{
		SourceWatermark: "2026-04-18T10:00:00.000Z", SourceVersion: version, RefreshDate: "2026-04-18", LastSuccessAt: "2026-04-18T12:00:00.000Z",
	}); err != nil {
		t.Fatal(err)
	}
	if got := env.queryString(`SELECT cursor_id FROM stats_job_state WHERE scope_type='global' AND job_name='w10c-rt'`); len(got) != 1 || got[0] != "2026-04-18" {
		t.Fatalf("主状态行错误: %v", got)
	}
	if got := env.queryString(`SELECT cursor_id FROM stats_job_state WHERE scope_type=? AND job_name='w10c-rt'`, RankSnapshotSourceVersionScopeType); len(got) != 1 || got[0] != version {
		t.Fatalf("sourceVersion 行错误: %v", got)
	}

	// 不带 sourceVersion → version 行删除。
	if err := refresher.updateRankRefreshJobState(ctx, "w10c-rt", rankRefreshStateInput{
		SourceWatermark: "2026-04-18T11:00:00.000Z", RefreshDate: "2026-04-18", LastSuccessAt: "2026-04-18T12:00:00.000Z",
	}); err != nil {
		t.Fatal(err)
	}
	if got := env.queryRowFloats(`SELECT COUNT(*) FROM stats_job_state WHERE scope_type=? AND job_name='w10c-rt'`, RankSnapshotSourceVersionScopeType); got[0] != 0 {
		t.Fatalf("sourceVersion 行应删除: %v", got)
	}
}

// ---- 窗口编排错误臂 ----

func TestW10CRunStagesOrchestration(t *testing.T) {
	env := newTestEnv(t)
	refresher := env.refresher()
	ctx := context.Background()

	// selectStages 错误臂：空列表、未知阶段。
	if _, err := refresher.RunStages(ctx, []WindowStageName{}, RefreshOptions{}); err == nil {
		t.Fatalf("空阶段列表应报错")
	}
	if _, err := refresher.RunStages(ctx, []WindowStageName{"nope"}, RefreshOptions{}); err == nil {
		t.Fatalf("未知阶段应报错")
	}
	// runStage 未知阶段 default 臂（直接调用）。
	if err := refresher.runStage(ctx, "nope", refreshStageContext{timezone: env.zone}); err == nil {
		t.Fatalf("runStage 未知阶段应报错")
	}
	// DefaultJobName：10 阶段主名 vs 单阶段复合名。
	allStages := []WindowStageName{
		StageAccountLast7dRequestRank, StageCallerAccountLast7dRequestRank, StageApiKeyCurrentMonthCostRank,
		StageAccountAuthorizationCurrentMonthRank, StageGroupAuthorizationCurrentMonthRank, StageUsageOverviewWindows,
		StageAiPerformanceSummaryWindows, StageSystemMetricsTrendWindows, StageUsageScopeRangeWindows, StageAuthorizationUsageRangeWindows,
	}
	if got := DefaultJobName(allStages); got != "usage_rank_snapshots_refresh" {
		t.Fatalf("10 阶段默认名错误: %s", got)
	}
	if got := DefaultJobName([]WindowStageName{StageUsageOverviewWindows, StageUsageScopeRangeWindows}); got != "usage_rank_snapshots_refresh:usage_overview_windows+usage_scope_range_windows" {
		t.Fatalf("单阶段默认名错误: %s", got)
	}
	// selectStages nil → 全部阶段。
	selected, err := refresher.selectStages(nil)
	if err != nil || len(selected) != 10 {
		t.Fatalf("nil 阶段应选全部: %d err=%v", len(selected), err)
	}

	// SkipIfUnchanged：首轮执行后次轮跳过。
	if _, err := refresher.RunStages(ctx, []WindowStageName{StageApiKeyCurrentMonthCostRank}, RefreshOptions{SkipIfUnchanged: true}); err != nil {
		t.Fatal(err)
	}
	second, err := refresher.RunStages(ctx, []WindowStageName{StageApiKeyCurrentMonthCostRank}, RefreshOptions{SkipIfUnchanged: true})
	if err != nil {
		t.Fatal(err)
	}
	if !second.Skipped || second.SkipReason != "source_watermark_unchanged" {
		t.Fatalf("次轮应跳过: %+v", second)
	}

	// Now=nil 覆盖 time.Now 兜底臂（无注入 Now 的 refresher）。
	bare := &WindowRefresher{DB: env.db, Dialect: env.dialect, Clock: StaticTimezoneSource{env.zone}}
	if _, err := bare.RunStages(ctx, []WindowStageName{StageApiKeyCurrentMonthCostRank}, RefreshOptions{}); err != nil {
		t.Fatal(err)
	}

	// refreshSystemMetricsTrendWindowsStage：非法 todayKey 早退臂（直接调用）。
	tx, err := env.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := refresher.refreshSystemMetricsTrendWindowsStage(ctx, tx, refreshStageContext{todayKey: "not-a-date"}); err != nil {
		t.Fatalf("非法 todayKey 应早退, got %v", err)
	}
}

// ---- latency 工具长尾臂 ----

func TestW10CLatencyHelpers(t *testing.T) {
	// LatencyBucketUpperBound：上界命中、最大桶（-1）。
	if got := LatencyBucketUpperBound(100); got != 100 {
		t.Fatalf("边界值应命中 100, got %d", got)
	}
	if got := LatencyBucketUpperBound(101); got != 250 {
		t.Fatalf("越界取下一桶, got %d", got)
	}
	if got := LatencyBucketUpperBound(60001); got != -1 {
		t.Fatalf("超界取 -1, got %d", got)
	}
	if got := LatencyBucketUpperBound(999999); got != -1 {
		t.Fatalf("超大取 -1, got %d", got)
	}

	// NaN/Inf duration 不产生样本（finiteNonNegativeNumber 过滤臂）。
	row := UsageStatsRecordRow{DurationMs: f64Ptr(math.NaN())}
	entry := UsageStatsEntry{SystemAccountID: "alice", ScopeType: "system_account", ScopeID: "alice"}
	target := map[latencyEntryKey]*AggregatedLatencyEntry{}
	timeKeys := UsageStatsTimeKeys{StatHour: "2026-04-18T10"}
	AddAggregatedLatencyEntries(target, entry, row, timeKeys)
	if len(target) != 0 {
		t.Fatalf("NaN duration 不应产生样本")
	}
	// Inf first token 同样过滤；有限 duration 正常入桶（5 个 latency 时间桶）。
	row.FirstTokenMs = f64Ptr(math.Inf(1))
	row.DurationMs = f64Ptr(1500)
	AddAggregatedLatencyEntries(target, entry, row, timeKeys)
	if len(target) != 5 {
		t.Fatalf("仅 duration 样本应入 5 个桶: %d", len(target))
	}
	for key, value := range target {
		if key.upperBound != 2000 || key.metricType != LatencyMetricDurationMs {
			t.Fatalf("1500ms 应入 2000 duration 桶, got %d %s", key.upperBound, key.metricType)
		}
		_ = value
	}
	// 既有 key 累加臂。
	AddAggregatedLatencyEntries(target, entry, row, timeKeys)
	for _, existing := range target {
		if existing.SampleCount != 2 {
			t.Fatalf("累加臂错误: %+v", existing)
		}
	}
}

// ---- 时区加载长尾臂 ----

func TestW10CLoadStatsTimezoneAndTimeKeys(t *testing.T) {
	if _, err := LoadStatsTimezone(""); err == nil {
		t.Fatalf("空时区应报错")
	}
	if _, err := LoadStatsTimezone("Not/AZone"); err == nil {
		t.Fatalf("未知时区应报错")
	}
	// UsageStatsTimeKeysFor 非法时间错误臂。
	if _, err := UsageStatsTimeKeysFor("garbage", time.UTC); err == nil {
		t.Fatalf("非法时间应报错")
	}
	keys, err := UsageStatsTimeKeysFor("2026-04-18T10:15:00.000Z", time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if keys.StatMinute != "2026-04-18T10:15" || keys.StatHour != "2026-04-18T10" || keys.StatDate != "2026-04-18" || keys.StatWeek != "2026-04-13" || keys.StatMonth != "2026-04" {
		t.Fatalf("时间键错误: %+v", keys)
	}
	// timeValue 未知 valueKey 臂。
	if keys.timeValue("unknown") != "" {
		t.Fatalf("未知 valueKey 应返回空")
	}
	// deref 双臂（authorization.go）。
	if deref(nil) != "" || deref(strPtr("x")) != "x" {
		t.Fatalf("deref 断言失败")
	}
}

// ---- Dialect PG 臂（纯字符串函数，无需真实 PG）----

func TestW10CDialectPostgresArms(t *testing.T) {
	pg := Dialect{Postgres: true}
	// bind：把每个 ? 依序替换为 $n。
	if got := pg.bind("SELECT * FROM t WHERE a = ? AND b = ? AND c = ?"); got != "SELECT * FROM t WHERE a = $1 AND b = $2 AND c = $3" {
		t.Fatalf("bind PG 替换错误: %s", got)
	}
	// 无占位符查询走逐字符拷贝臂。
	if got := pg.bind("SELECT 1"); got != "SELECT 1" {
		t.Fatalf("bind 无占位符错误: %s", got)
	}
	// 各表引用前缀。
	if got := pg.StatsTable("usage_stats_daily"); got != "juhe_stats.usage_stats_daily" {
		t.Fatalf("StatsTable = %s", got)
	}
	if got := pg.BusinessTable("accounts"); got != "juhe_business.accounts" {
		t.Fatalf("BusinessTable = %s", got)
	}
	if got := pg.UsageRecordsTable(); got != "juhe_usage.usage_records" {
		t.Fatalf("UsageRecordsTable = %s", got)
	}
	if got := pg.qualifiedTarget("stats_job_state"); got != "juhe_stats.stats_job_state" {
		t.Fatalf("qualifiedTarget = %s", got)
	}
	if got := pg.leastExpr("a", "b"); got != "LEAST(a, b)" {
		t.Fatalf("leastExpr = %s", got)
	}
	if got := pg.greatestExpr("a", "b"); got != "GREATEST(a, b)" {
		t.Fatalf("greatestExpr = %s", got)
	}
	// SQLite 臂（非 PG）逐一对照。
	sqlite := Dialect{Postgres: false}
	if got := sqlite.StatsTable("t"); got != "t" || got != sqlite.BusinessTable("t") || sqlite.UsageRecordsTable() != "usage_records" || sqlite.qualifiedTarget("t") != "t" {
		t.Fatalf("SQLite 表引用错误")
	}
	if got := sqlite.leastExpr("a", "b"); got != "MIN(a, b)" {
		t.Fatalf("SQLite leastExpr = %s", got)
	}
	if got := sqlite.greatestExpr("a", "b"); got != "MAX(a, b)" {
		t.Fatalf("SQLite greatestExpr = %s", got)
	}
}

// w10cPGProbe 编译期引用 database/sql（SQLite env 复用同包 fixture）。
var _ = sql.NullString{}
