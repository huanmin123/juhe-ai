package circuitstore

// listavailability_sources.go 纯函数面测试：Node 数值容错、quota 窗口解析与
// 超限判定、usage 窗口键、余额快照配置匹配、公共电路摘要 reducer 与
// api key 池隔离开关。SQL 读路径见 sources_loader_reads_test.go。

import (
	"testing"
	"time"
)

func sourcesNow() time.Time { return time.Date(2026, 9, 4, 10, 30, 0, 0, time.UTC) }

// TestNumberValueCoercion 对齐 Node numberValue 的类型容错矩阵。
func TestNumberValueCoercion(t *testing.T) {
	cases := []struct {
		name  string
		value any
		want  float64
	}{
		{"nil默认0", nil, 0},
		{"int64", int64(7), 7},
		{"int", 5, 5},
		{"float64", 2.5, 2.5},
		{"float32", float32(1.5), 1.5},
		{"bytes数字", []byte("42"), 42},
		{"字符串数字带空白", " 3.25 ", 3.25},
		{"非法字符串回默认", "abc", 0},
		{"空字符串回默认", "", 0},
		{"布尔类型不识别回默认", true, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := numberValue(tc.value); got != tc.want {
				t.Fatalf("numberValue(%v) = %v, 期望 %v", tc.value, got, tc.want)
			}
		})
	}
	if got := numberValue("abc", 9); got != 9 {
		t.Fatalf("非法值应回退显式默认: %v", got)
	}
	if got := numberValue(nil, 4); got != 4 {
		t.Fatalf("nil 应回退显式默认: %v", got)
	}
}

// TestBooleanValueAndCoalesce 对齐 booleanValue（===1）与 coalesceAny。
func TestBooleanValueAndCoalesce(t *testing.T) {
	if booleanValue(1) != true || booleanValue(int64(1)) != true || booleanValue("1") != true {
		t.Fatal("1 语义为 true")
	}
	if booleanValue(0) || booleanValue(2) || booleanValue(nil) {
		t.Fatal("非 1 必须为 false")
	}
	if got := coalesceAny(nil, nil, "x", "y"); got != "x" {
		t.Fatalf("coalesceAny 应取第一个非 nil: %v", got)
	}
	if got := coalesceAny(nil, nil); got != nil {
		t.Fatalf("全 nil 应返回 nil: %v", got)
	}
	if timeParamText(true, sourcesNow()) != any(sourcesNow()) {
		t.Fatal("PG 方言应传 time.Time")
	}
	if _, ok := timeParamText(false, sourcesNow()).(string); !ok {
		t.Fatal("SQLite 方言应传 RFC3339 文本")
	}
	if got := sha256HexShort("abc", 8); len(got) != 8 {
		t.Fatalf("sha256HexShort 长度不符: %s", got)
	}
}

// TestParseQuotaLimitsAndExceeded 覆盖 quota 窗口解析与超限判定。
func TestParseQuotaLimitsAndExceeded(t *testing.T) {
	empty := parseQuotaLimits("")
	if empty.hasEnabled() {
		t.Fatal("空 JSON 不得有启用窗口")
	}
	if empty.exceeded(quotaCosts{Total: 1e9}) {
		t.Fatal("无启用窗口不得判超限")
	}

	raw := `{"hourly":{"enabled":true,"limit":10,"hours":2},"daily":{"enabled":true,"limit":100},
		"weekly":{"enabled":false,"limit":700},"monthly":{"enabled":true,"limit":3000},"total":{"enabled":true,"limit":99999}}`
	limits := parseQuotaLimits(raw)
	if limits.Hourly == nil || !limits.Hourly.Enabled || limits.Hourly.Limit != 10 || limits.Hourly.Hours != 2 {
		t.Fatalf("hourly 窗口解析不符: %+v", limits.Hourly)
	}
	if limits.Daily == nil || limits.Daily.Limit != 100 {
		t.Fatalf("daily 窗口解析不符: %+v", limits.Daily)
	}
	if limits.Weekly == nil || limits.Weekly.Enabled {
		t.Fatalf("weekly 未启用不得算 hasEnabled: %+v", limits.Weekly)
	}
	if !limits.hasEnabled() {
		t.Fatal("存在启用窗口时 hasEnabled 必须为 true")
	}

	// 各窗口独立超限。
	if !limits.exceeded(quotaCosts{Hourly: 10}) {
		t.Fatal("hourly 达上限应超限")
	}
	if !limits.exceeded(quotaCosts{Daily: 100}) {
		t.Fatal("daily 达上限应超限")
	}
	if !limits.exceeded(quotaCosts{Monthly: 3001}) {
		t.Fatal("monthly 超上限应超限")
	}
	if !limits.exceeded(quotaCosts{Total: 100000}) {
		t.Fatal("total 超上限应超限")
	}
	if limits.exceeded(quotaCosts{Hourly: 9.99, Daily: 99, Monthly: 2999, Total: 99998}) {
		t.Fatal("全部低于上限不得判超限")
	}
	// 未启用的 weekly 不参与判定。
	if limits.exceeded(quotaCosts{Weekly: 1e9}) {
		t.Fatal("未启用窗口不得触发超限")
	}
}

// TestParseQuotaLimitsPayload 对齐 payload 形状（五键白名单 + 错误边界）。
func TestParseQuotaLimitsPayload(t *testing.T) {
	payload, err := parseQuotaLimitsPayload("   ")
	if err != nil || len(payload) != 0 {
		t.Fatalf("空串应返回空对象: %v %v", payload, err)
	}
	payload, err = parseQuotaLimitsPayload(`{"hourly":{"limit":1},"extra":"drop","other":2}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(payload) != 1 {
		t.Fatalf("只保留白名单键: %v", payload)
	}
	if _, exists := payload["hourly"]; !exists {
		t.Fatalf("hourly 应保留: %v", payload)
	}
	if _, err := parseQuotaLimitsPayload("{broken"); err == nil {
		t.Fatal("非法 JSON 必须报错（调用方释放重放）")
	}
	if _, err := parseQuotaLimitsPayload(`[1,2]`); err == nil {
		t.Fatal("非对象 JSON 必须报错")
	}
}

// TestQuotaResetAtOf 覆盖多窗口超限时的最早重置点。
func TestQuotaResetAtOf(t *testing.T) {
	now := sourcesNow()
	timezone := time.UTC
	// 无超限 → 空串。
	noLimits := parseQuotaLimits(`{"daily":{"enabled":true,"limit":100}}`)
	if got := quotaResetAtOf(noLimits, quotaCosts{Daily: 50}, now, timezone); got != "" {
		t.Fatalf("未超限不得给重置点: %s", got)
	}
	// 仅 daily。
	dailyOnly := parseQuotaLimits(`{"daily":{"enabled":true,"limit":100}}`)
	dailyReset := quotaResetAtOf(dailyOnly, quotaCosts{Daily: 100}, now, timezone)
	if dailyReset != "2026-09-05T00:00:00Z" {
		t.Fatalf("daily 重置点应为次日零点: %s", dailyReset)
	}
	// hourly + monthly：取最早（hourly 边界 11:00 < 次月 10-01）。
	multi := parseQuotaLimits(`{"hourly":{"enabled":true,"limit":5,"hours":1},"monthly":{"enabled":true,"limit":50}}`)
	got := quotaResetAtOf(multi, quotaCosts{Hourly: 5, Monthly: 50}, now, timezone)
	if got != "2026-09-04T11:00:00Z" {
		t.Fatalf("最早重置点应为下一个整点: %s", got)
	}
	// weekly 单独超限。
	weekly := parseQuotaLimits(`{"weekly":{"enabled":true,"limit":5}}`)
	got = quotaResetAtOf(weekly, quotaCosts{Weekly: 5}, now, timezone)
	// 行为存疑：weekly 重置点实现为 now+7 天当日的零点（2026-09-11），与
	// "周窗口从下周一起"的直觉不同；此处按当前实际行为断言。
	if got != "2026-09-11T00:00:00Z" {
		t.Fatalf("weekly 重置点应为 +7 天零点: %s", got)
	}
}

// TestZonedBoundaryHelpers 覆盖时区边界函数（含 12 月跨年与非法 dateKey）。
func TestZonedBoundaryHelpers(t *testing.T) {
	now := time.Date(2026, 12, 31, 23, 15, 0, 0, time.UTC)
	if got := nextZonedHourBoundary(now, time.UTC); got != "2027-01-01T00:00:00Z" {
		t.Fatalf("跨日整点边界不符: %s", got)
	}
	if got := startOfNextZonedMonth(now, time.UTC); got != "2027-01-01T00:00:00Z" {
		t.Fatalf("12 月下一月应跨年: %s", got)
	}
	oct := time.Date(2026, 10, 15, 8, 0, 0, 0, time.UTC)
	if got := startOfNextZonedMonth(oct, time.UTC); got != "2026-11-01T00:00:00Z" {
		t.Fatalf("普通月份下一月边界不符: %s", got)
	}
	if got := startOfZonedDateKey("2026-09-05", time.UTC); got != "2026-09-05T00:00:00Z" {
		t.Fatalf("dateKey 起点不符: %s", got)
	}
	if got := startOfZonedDateKey("bad", time.UTC); got != "" {
		t.Fatalf("非法 dateKey 应返回空串: %s", got)
	}
}

// TestUsageWindowKeys 对齐 usage-stats-helpers 的日/周/月键（含周日跨周）。
func TestUsageWindowKeys(t *testing.T) {
	timezone := time.UTC
	// 2026-09-04 是周五。
	friday := time.Date(2026, 9, 4, 10, 0, 0, 0, time.UTC)
	if got := usageStatDate(friday, timezone); got != "2026-09-04" {
		t.Fatalf("stat date 不符: %s", got)
	}
	if got := usageWeekKey(friday, timezone); got != "2026-08-31" {
		t.Fatalf("周五应归入周一开头的周: %s", got)
	}
	if got := usageMonthKey(friday, timezone); got != "2026-09" {
		t.Fatalf("month key 不符: %s", got)
	}
	// 2026-09-06 是周日：归入 08-31 开头的同一周。
	sunday := time.Date(2026, 9, 6, 23, 0, 0, 0, time.UTC)
	if got := usageWeekKey(sunday, timezone); got != "2026-08-31" {
		t.Fatalf("周日应仍归入本周: %s", got)
	}
	// 2026-09-07 是周一：开启新周。
	monday := time.Date(2026, 9, 7, 0, 30, 0, 0, time.UTC)
	if got := usageWeekKey(monday, timezone); got != "2026-09-07" {
		t.Fatalf("周一应开启新周: %s", got)
	}
}

// TestQuotaCostKey 覆盖小时窗口维度键。
func TestQuotaCostKey(t *testing.T) {
	hours := 2
	withHours := quotaCostKey("sys", "scope", "id", &hours)
	withoutHours := quotaCostKey("sys", "scope", "id", nil)
	if withHours == withoutHours {
		t.Fatal("小时维度必须影响 cost key")
	}
	if got := quotaCostKey("sys", "scope", "id", &hours); got != withHours {
		t.Fatal("同输入必须产出相同 cost key")
	}
}

// TestBalanceSnapshotMatching 覆盖余额快照与配置匹配语义。
func TestBalanceSnapshotMatching(t *testing.T) {
	revision := 3.0
	if balanceSnapshotMatchesConfiguration("2026-09-04T10:00:00.000Z", revision, nil) {
		t.Fatal("nil 快照不匹配")
	}
	record := &balanceSnapshotRecord{snapshotRevision: 3, nextRefreshAfter: "2026-09-04T10:00:00.000Z"}
	if !balanceSnapshotMatchesConfiguration("2026-09-04T10:00:00.000Z", revision, record) {
		t.Fatal("revision 与毫秒都相等必须匹配")
	}
	if balanceSnapshotMatchesConfiguration("2026-09-04T11:00:00.000Z", revision, record) {
		t.Fatal("nextRefresh 毫秒不等不得匹配")
	}
	if balanceSnapshotMatchesConfiguration("2026-09-04T10:00:00.000Z", 4, record) {
		t.Fatal("revision 不同不得匹配")
	}
	// 快照缺 revision（0）时只看毫秒。
	legacy := &balanceSnapshotRecord{nextRefreshAfter: "2026-09-04T10:00:00.000Z"}
	if !balanceSnapshotMatchesConfiguration("2026-09-04T10:00:00.000Z", 99, legacy) {
		t.Fatal("无 revision 快照应跳过 revision 校验")
	}
	if balanceSnapshotMatchesConfiguration("", revision, record) {
		t.Fatal("配置缺 nextRefreshAt 不得匹配")
	}
	if balanceSnapshotMatchesConfiguration("2026-09-04T10:00:00.000Z", revision, &balanceSnapshotRecord{}) {
		t.Fatal("快照缺 nextRefreshAfter 不得匹配")
	}
	bothEmpty := &balanceSnapshotRecord{snapshotRevision: revision}
	if !balanceSnapshotMatchesConfiguration("", revision, bothEmpty) {
		t.Fatal("两侧都为空视为匹配（Node 同语义）")
	}
}

// TestBalanceForListPayload 覆盖 keyBalances 剥离。
func TestBalanceForListPayload(t *testing.T) {
	snapshot := map[string]any{
		"totalBalance": 12.5,
		"keyBalances":  []any{map[string]any{"k": 1}},
		"currency":     "USD",
	}
	payload := balanceForListPayload(snapshot)
	if _, exists := payload["keyBalances"]; exists {
		t.Fatalf("forList 必须剥离 keyBalances: %v", payload)
	}
	if payload["totalBalance"] != 12.5 || payload["currency"] != "USD" {
		t.Fatalf("其余键必须保留: %v", payload)
	}
	if len(snapshot) != 3 {
		t.Fatal("输入快照不得被修改")
	}
}

// TestPublicCircuitSummaryReducer 覆盖公共电路摘要 reducer 的优先级与聚合。
func TestPublicCircuitSummaryReducer(t *testing.T) {
	if got := publicCircuitSummaryOf(nil); got.Status != "normal" {
		t.Fatalf("无 incident 应为 normal: %+v", got)
	}
	base := func(state string, updatedAt int64, next *int64) circuitIncidentRecord {
		return circuitIncidentRecord{
			accountRuntimeKey:  "acc-1",
			state:              state,
			lastFailureClass:   "http_502",
			updatedAtMS:        updatedAt,
			nextTransitionAtMS: next,
		}
	}
	// OPEN 优先于 RECOVERING；同优先级取 updatedAt 最早。
	nextA := int64(5000)
	nextB := int64(3000)
	incidents := []circuitIncidentRecord{
		base("RECOVERING", 300, &nextA),
		base("HALF_OPEN", 200, nil),
		base("OPEN", 250, &nextB),
	}
	got := publicCircuitSummaryOf(incidents)
	if got.Status != "avoided" {
		t.Fatalf("OPEN 应映射 avoided: %+v", got)
	}
	if got.Since != millisToISO(250) {
		t.Fatalf("同优先级应取 updatedAt 最早的 OPEN: %+v", got)
	}
	if got.NextCheckAt != millisToISO(3000) {
		t.Fatalf("nextCheckAt 应取全部 incident 最早 next_transition_at_ms: %+v", got)
	}
	if got.Reason != "http_502" {
		t.Fatalf("reason 应取选中 incident 的 last_failure_class: %+v", got)
	}

	// RECOVERING 映射 recovering。
	got = publicCircuitSummaryOf([]circuitIncidentRecord{base("RECOVERING", 100, nil)})
	if got.Status != "recovering" || got.NextCheckAt != "" {
		t.Fatalf("RECOVERING 映射不符: %+v", got)
	}
	// HALF_OPEN/SUSPECT 映射 verifying。
	got = publicCircuitSummaryOf([]circuitIncidentRecord{base("SUSPECT", 100, nil)})
	if got.Status != "verifying" {
		t.Fatalf("SUSPECT 应映射 verifying: %+v", got)
	}
	// 未知状态映射 verifying（默认）。
	got = publicCircuitSummaryOf([]circuitIncidentRecord{base("UNKNOWN", 100, nil)})
	if got.Status != "verifying" {
		t.Fatalf("未知状态应映射 verifying: %+v", got)
	}
}

// TestMillisToISO 覆盖 ISO 毫秒格式化与非正值。
func TestMillisToISO(t *testing.T) {
	if got := millisToISO(0); got != "" {
		t.Fatalf("0 应返回空串: %s", got)
	}
	if got := millisToISO(-5); got != "" {
		t.Fatalf("负值应返回空串: %s", got)
	}
	if got := millisToISO(1782420000000); got == "" {
		t.Fatal("正值必须格式化")
	}
}

// TestAPIKeyPoolIsolationAndProviderTokens 覆盖 key 池隔离开关的供应商支持面。
func TestAPIKeyPoolIsolationAndProviderTokens(t *testing.T) {
	credentials := map[string]any{"keys": []any{"a", "b"}}
	twoEntries := stubMultiKeyCredentials{count: 2}
	oneEntry := stubMultiKeyCredentials{count: 1}

	supported := []struct{ provider, protocol, version string }{
		{"openai", "", ""},
		{" GPT ", "", ""},
		{"deepseek", "", ""},
		{"GLM", "", ""},
		{"gemini", "", ""},
		{"anthropic", "", ""},
		{"anything", "anthropic", "v1"},
	}
	for _, item := range supported {
		if !apiKeyPoolIsolationEnabled(twoEntries, "api_key", item.provider, item.protocol, item.version, credentials) {
			t.Fatalf("供应商组合 %v 应启用隔离", item)
		}
	}
	unsupported := []struct{ provider, protocol, version string }{
		{"unknown", "", ""},
		{"", "anthropic", "v2"},
		{"", "", ""},
	}
	for _, item := range unsupported {
		if apiKeyPoolIsolationEnabled(twoEntries, "api_key", item.provider, item.protocol, item.version, credentials) {
			t.Fatalf("供应商组合 %v 不得启用隔离", item)
		}
	}
	// 非 api_key 类型 / 单 key 池 / 空 entries 均不启用。
	if apiKeyPoolIsolationEnabled(twoEntries, "oauth", "openai", "", "", credentials) {
		t.Fatal("oauth 类型不得启用隔离")
	}
	if apiKeyPoolIsolationEnabled(oneEntry, "api_key", "openai", "", "", credentials) {
		t.Fatal("单 key 池不得启用隔离")
	}
	if apiKeyRuntimeProbeCandidateStatus("temporary_unavailable") != true ||
		apiKeyRuntimeProbeCandidateStatus("rate_limited") != true ||
		apiKeyRuntimeProbeCandidateStatus("error") != true {
		t.Fatal("探测候选状态判定不符")
	}
	if apiKeyRuntimeProbeCandidateStatus("active") || apiKeyRuntimeProbeCandidateStatus("disabled") {
		t.Fatal("active/disabled 不是探测候选")
	}
	if normalizeProviderToken(" OpenAI ") != "openai" {
		t.Fatal("provider token 归一化不符")
	}
}

// TestAPIKeyRuntimeSummaryPublicPayload 覆盖公共汇总形状（失败字段成组出现）。
func TestAPIKeyRuntimeSummaryPublicPayload(t *testing.T) {
	bare := &apiKeyRuntimeSummary{Total: 3, Active: 2, Unavailable: 1}
	payload := bare.publicPayload()
	if payload["total"] != 3 || payload["active"] != 2 || payload["unavailable"] != 1 {
		t.Fatalf("计数键不符: %v", payload)
	}
	if _, exists := payload["nextProbeAt"]; exists {
		t.Fatalf("无 nextProbeAt 不得输出该键: %v", payload)
	}
	if _, exists := payload["lastFailureAt"]; exists {
		t.Fatalf("无失败不得输出失败键组: %v", payload)
	}
	failed := &apiKeyRuntimeSummary{
		Total: 2, Unavailable: 2, AllUnavailable: true,
		NextProbeAt: "2030-01-01T00:00:00.000Z", LastFailureAt: effPast,
		LastErrorCode: "http_502", LastErrorMessage: "boom", LastTraceID: "t-1",
	}
	payload = failed.publicPayload()
	if payload["allUnavailable"] != true || payload["nextProbeAt"] != failed.NextProbeAt {
		t.Fatalf("全不可用汇总不符: %v", payload)
	}
	if payload["lastErrorCode"] != "http_502" || payload["lastErrorMessage"] != "boom" || payload["lastTraceId"] != "t-1" {
		t.Fatalf("失败键组必须成组输出: %v", payload)
	}
}

// stubMultiKeyCredentials 按 count 返回固定 key 池（隔离开关专用 Mock）。
type stubMultiKeyCredentials struct{ count int }

func (stubMultiKeyCredentials) DecryptCredentials(envelope string) (map[string]any, error) {
	return map[string]any{}, nil
}

func (s stubMultiKeyCredentials) AccountAPIKeyEntries(credentials map[string]any) []APIKeyPoolEntry {
	entries := make([]APIKeyPoolEntry, 0, s.count)
	for i := 0; i < s.count; i++ {
		entries = append(entries, APIKeyPoolEntry{ID: "k", Fingerprint: "fp", Index: i})
	}
	return entries
}
