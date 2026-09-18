package statsagg

// w13g5_statsagg_units_test.go 以同包白盒直调覆盖纯函数分支：
// 记录归一化、累加器合并、质量/健康/错误条目聚合、水位序列化、
// 窗口计划与趋势桶键。全部为确定性纯计算，可回放。

import (
	"context"
	"math"
	"testing"
	"time"
)

func w13g5s(value string) *string { return &value }

func w13g5f(value float64) *float64 { return &value }

func TestW13g5UsageStatsEntriesProtocolProfileAndMetadataArms(t *testing.T) {
	// ProviderProtocolProfileID 维度展开（54-56）。
	row := UsageStatsRecordRow{SystemAccountID: "w13g5-sa", ProviderProtocolProfileID: w13g5s("w13g5-ppp")}
	entries := UsageStatsEntries(row, nil)
	found := false
	for _, entry := range entries {
		if entry.ScopeType == "provider_protocol_profile" {
			found = true
		}
	}
	if !found {
		t.Fatal("ProviderProtocolProfileID 必须展开为 provider_protocol_profile 维度")
	}
	// usageStatsAccountMetadata：有 account 无归属 → nil（117-119）。
	if got := usageStatsAccountMetadata(UsageStatsRecordRow{AccountID: w13g5s("w13g5-acc")}); got != nil {
		t.Fatal("缺归属的 account metadata 必须为 nil")
	}
	// usageStatsGroupMetadata：有 group 无归属 → nil（128-130）。
	if got := usageStatsGroupMetadata(UsageStatsRecordRow{GroupID: w13g5s("w13g5-grp")}); got != nil {
		t.Fatal("缺归属的 group metadata 必须为 nil")
	}
	// accountAuthorizationTeamAccountID：无 account 无查找 → ""（143）。
	if got := accountAuthorizationTeamAccountID(UsageStatsRecordRow{}, nil); got != "" {
		t.Fatal("无 account 时必须返回空串")
	}
}

func TestW13g5NormalizePostgresUsageStatsRecordRow(t *testing.T) {
	row := normalizePostgresUsageStatsRecordRow(UsageStatsRecordRow{
		CreatedAt:          "2026-09-18T00:00:00Z",
		StatusCode:         w13g5f(math.NaN()),
		Success:            1,
		FirstTokenMs:       nil,
		DurationMs:         w13g5f(5),
		InputTokens:        w13g5f(-1),
		CacheWrite1hTokens: w13g5f(math.NaN()),
	})
	if row.CreatedAt != "2026-09-18T00:00:00.000Z" {
		t.Fatalf("created_at 必须规范化: %s", row.CreatedAt)
	}
	if row.StatusCode != nil || row.Success != 1 {
		t.Fatal("NaN 数值必须归一为 nil，Success 保持原值")
	}
}

func TestW13g5MergeAccumulatorTimestampErrorArms(t *testing.T) {
	target := UsageStatsAccumulator{LastUsedAt: "w13g5-not-a-time"}
	if err := MergeAccumulator(&target, UsageStatsAccumulator{LastUsedAt: "2026-09-18T00:00:00.000Z"}); err == nil {
		t.Fatal("LastUsedAt 非法必须报错")
	}
	target2 := UsageStatsAccumulator{}
	if err := MergeAccumulator(&target2, UsageStatsAccumulator{LastErrorAt: "w13g5-not-a-time"}); err == nil {
		t.Fatal("LastErrorAt 非法必须报错")
	}
}

func TestW13g5PostgresAccountQualityStatsSystemAccountIDArms(t *testing.T) {
	if _, err := postgresAccountQualityStatsSystemAccountID(UsageStatsRecordRow{ID: "w13g5-r1"}); err == nil {
		t.Fatal("缺 account_access_type 必须报错")
	}
	if _, err := postgresAccountQualityStatsSystemAccountID(UsageStatsRecordRow{
		ID:                "w13g5-r2",
		AccountAccessType: w13g5s("owner"),
	}); err == nil {
		t.Fatal("owner 访问缺归属必须报错")
	}
	if got, err := postgresAccountQualityStatsSystemAccountID(UsageStatsRecordRow{
		ID:                          "w13g5-r3",
		AccountAccessType:           w13g5s("account_authorized"),
		SystemAccountID:             "w13g5-caller",
	}); err != nil || got != "w13g5-caller" {
		t.Fatalf("account_authorized 必须取调用方: %v %v", got, err)
	}
}

func TestW13g5AddPostgresAggregatedAccountQualityEntryArms(t *testing.T) {
	entries := map[string]*aggregatedAccountQualityEntry{}
	// 缺 access type 触发 578-580 错误传播。
	bad := UsageStatsRecordRow{
		ID: "w13g5-bad", SystemAccountID: "w13g5-sa", TrafficSource: "gateway",
		Success: 1, CreatedAt: "2026-09-18T07:15:00.000Z",
		AccountID: w13g5s("w13g5-acc"), APIKeyID: w13g5s("w13g5-key"),
	}
	if err := addPostgresAggregatedAccountQualityEntry(entries, bad, UsageStatsTimeKeys{StatMinute: "2026-09-18T07:15"}); err == nil {
		t.Fatal("质量聚合缺 access type 必须报错")
	}
	good := bad
	good.AccountAccessType = w13g5s("owner")
	good.AccountOwnerSystemAccountID = w13g5s("w13g5-sa")
	if err := addPostgresAggregatedAccountQualityEntry(entries, good, UsageStatsTimeKeys{StatMinute: "2026-09-18T07:15"}); err != nil {
		t.Fatal(err)
	}
	// 同键第二条走 existing 分支：first token 计数（616-618）、成功替换（624-627）。
	second := good
	second.CreatedAt = "2026-09-18T07:15:30.000Z"
	second.FirstTokenMs = w13g5f(30)
	if err := addPostgresAggregatedAccountQualityEntry(entries, second, UsageStatsTimeKeys{StatMinute: "2026-09-18T07:15"}); err != nil {
		t.Fatal(err)
	}
	// 失败行替换 lastError（628-641）。
	failed := good
	failed.Success = 0
	failed.ErrorMessage = w13g5s("w13g5-boom")
	failed.CreatedAt = "2026-09-18T07:15:45.000Z"
	if err := addPostgresAggregatedAccountQualityEntry(entries, failed, UsageStatsTimeKeys{StatMinute: "2026-09-18T07:15"}); err != nil {
		t.Fatal(err)
	}
	entry := entries["w13g5-acc\x002026-09-18T07:15"]
	if entry == nil || entry.RequestCount != 3 || entry.FirstTokenMsCount != 2 || entry.LastErrorMessage != "w13g5-boom" {
		t.Fatalf("聚合结果不符合预期: %+v", entry)
	}
}

func TestW13g5AddPostgresAggregatedUsageErrorEntriesExistingBranch(t *testing.T) {
	entries := map[statsErrorKey]*aggregatedUsageErrorEntry{}
	base := UsageStatsRecordRow{
		SystemAccountID: "w13g5-sa", Success: 0, StatusCode: w13g5f(429),
		ProviderCode: w13g5s("openai"), ErrorCode: w13g5s("rate_limited"),
		CreatedAt: "2026-09-18T07:15:00.000Z",
	}
	keys := UsageStatsTimeKeys{StatMinute: "2026-09-18T07:15", StatHour: "2026-09-18T07", StatDate: "2026-09-18"}
	addPostgresAggregatedUsageErrorEntries(entries, base, keys)
	second := base
	second.ErrorMessage = w13g5s("w13g5-later-message")
	addPostgresAggregatedUsageErrorEntries(entries, second, keys)
	found := false
	for _, entry := range entries {
		if entry.ErrorMessage != nil && *entry.ErrorMessage == "w13g5-later-message" {
			found = true
			if entry.RequestCount != 2 || entry.ErrorCount != 2 {
				t.Fatalf("existing 分支必须累加: %+v", entry)
			}
		}
	}
	if !found {
		t.Fatal("existing 分支必须保留最新 error message")
	}
}

func TestW13g5AddPostgresAggregatedAccountHealthEntryExistingBranch(t *testing.T) {
	entries := map[string]*aggregatedAccountHealthEntry{}
	row := UsageStatsRecordRow{
		SystemAccountID: "w13g5-sa", TrafficSource: "account_health_check",
		AccountID: w13g5s("w13g5-acc"), Success: 1,
		ProviderCode: w13g5s("openai"),
	}
	addPostgresAggregatedAccountHealthEntry(entries, row, "2026-09-18T07")
	older := row
	older.CreatedAt = "2026-09-18T06:00:00.000Z"
	older.ID = "w13g5-old"
	addPostgresAggregatedAccountHealthEntry(entries, older, "2026-09-18T07")
	if entries["w13g5-acc\x002026-09-18T07"].LastObservedAt == older.CreatedAt {
		t.Fatal("更旧的观察不得覆盖")
	}
	newer := row
	newer.CreatedAt = "2026-09-18T08:00:00.000Z"
	newer.ID = "w13g5-new"
	addPostgresAggregatedAccountHealthEntry(entries, newer, "2026-09-18T07")
	if entries["w13g5-acc\x002026-09-18T07"].LastRecordID != "w13g5-new" {
		t.Fatal("更新的观察必须覆盖")
	}
}

func TestW13g5StableWatermarkRowsMultiRowAndEscapes(t *testing.T) {
	rows := []rawRow{
		{columns: []string{"b", "a"}, values: map[string]any{"a": "x\n\"quote\"", "b": int64(3)}},
		{columns: []string{"k"}, values: map[string]any{"k": 1.5}},
		{columns: []string{"u"}, values: map[string]any{"u": nil}},
		{columns: []string{"c"}, values: map[string]any{"c": "\x01"}},
	}
	out := stableWatermarkRows("w13g5-table", rows)
	if len(out) == 0 {
		t.Fatal("序列化输出不能为空")
	}
	// 控制字符走 \u00xx 转义分支。
	if got := jsonEscape("\x01"); got != `\u0001` {
		t.Fatalf("控制字符必须转义: %q", got)
	}
	if got := stableWatermarkValue(true); got != "true" {
		t.Fatalf("布尔序列化: %s", got)
	}
	if got := stableWatermarkValue(7.0); got != "7" {
		t.Fatalf("整数值浮点必须无小数点: %s", got)
	}
	if got := stableWatermarkValue(1e20); got == "7" || len(got) == 0 {
		t.Fatalf("超大浮点走最短表示: %s", got)
	}
}

func TestW13g5AggregateTrendRowsBranches(t *testing.T) {
	system := aggregateSystemMetricsRows([]systemMetricsTrendHourlyRow{
		{StatHour: "2026-09-18T00", SampleCount: 1, CPUPercentMax: w13g5f(10)},
		{StatHour: "2026-09-18T01", SampleCount: 2, CPUPercentMax: w13g5f(30)},
		{StatHour: "2026-09-17T23", SampleCount: 3, CPUPercentMax: w13g5f(20)},
	}, 24)
	if len(system) != 2 {
		t.Fatalf("24h 桶必须按日聚合: %+v", system)
	}
	process := aggregateProcessEventLoopRows([]processEventLoopTrendHourlyRow{
		{StatHour: "2026-09-18T00", ProcessRole: "server", SampleCount: 1, EventLoopLagMsMax: w13g5f(5)},
		{StatHour: "2026-09-18T01", ProcessRole: "server", SampleCount: 1, EventLoopLagMsMax: w13g5f(9)},
		{StatHour: "2026-09-18T01", ProcessRole: "w13g5-bogus-role", SampleCount: 1},
	}, 24)
	if len(process) != 1 || process[0].SampleCount != 2 {
		t.Fatalf("进程趋势聚合必须合并同桶且跳过非法角色: %+v", process)
	}
}

func TestW13g5SumMaxMergeProcessMemoryArms(t *testing.T) {
	bucket := &processEventLoopTrendHourlyRow{StatHour: "2026-09-18T00", ProcessRole: "server"}
	row := processEventLoopTrendHourlyRow{
		StatHour: "2026-09-18T00", ProcessRole: "server",
		ProcessRssBytesSum: 10, ProcessRssBytesMax: w13g5f(10),
		ProcessHeapUsedBytesSum: 11, ProcessHeapUsedBytesMax: w13g5f(11),
		ProcessHeapTotalBytesSum: 12, ProcessHeapTotalBytesMax: w13g5f(12),
		ProcessExternalBytesSum: 13, ProcessExternalBytesMax: w13g5f(13),
		ProcessArrayBuffersBytesSum: 14, ProcessArrayBuffersBytesMax: w13g5f(14),
	}
	for _, metric := range []string{"ProcessRssBytes", "ProcessHeapUsedBytes", "ProcessHeapTotalBytes", "ProcessExternalBytes", "ProcessArrayBuffersBytes"} {
		sumMaxMergeProcessMemory(bucket, row, metric)
	}
	if bucket.ProcessRssBytesSum != 10 || bucket.ProcessArrayBuffersBytesMax == nil || *bucket.ProcessArrayBuffersBytesMax != 14 {
		t.Fatalf("内存指标合并失败: %+v", bucket)
	}
}

func TestW13g5ProcessEventLoopRoleValidation(t *testing.T) {
	if !isValidProcessEventLoopRole("gateway:worker-1") {
		t.Fatal("gateway: 前缀角色必须合法")
	}
	if !isValidProcessEventLoopRole("stats-worker:3") {
		t.Fatal("worker 副本角色必须合法")
	}
	if isValidProcessEventLoopRole("") || isValidProcessEventLoopRole("gateway:") || isValidProcessEventLoopRole(string(make([]byte, 97))) {
		t.Fatal("空/空前缀/超长角色必须非法")
	}
}

func TestW13g5StageSourceTablesUnknownStage(t *testing.T) {
	if tables := stageSourceTables("w13g5-unknown-stage"); tables != nil {
		t.Fatal("未知 stage 必须返回 nil")
	}
}

func TestW13g5ConsumeModelRowsNilArm(t *testing.T) {
	if err := consumeModelRows(nil, func(string, string, string, []float64) {}); err != nil {
		t.Fatal("nil rows 必须静默返回")
	}
}

func TestW13g5ScanWindowRowsErrorArms(t *testing.T) {
	if _, err := scanDailyWindowRows(nil, w13g5StatsInjectedErr()); err == nil {
		t.Fatal("daily 扫描必须传播错误")
	}
	if _, err := scanHourlyWindowRows(nil, w13g5StatsInjectedErr()); err == nil {
		t.Fatal("hourly 扫描必须传播错误")
	}
	if _, err := scanSystemMetricsHourlyRows(nil, w13g5StatsInjectedErr()); err == nil {
		t.Fatal("系统指标扫描必须传播错误")
	}
}

func TestW13g5WindowPlanEdgeArms(t *testing.T) {
	if keys := FixedUsageStatsDateKeys("w13g5-bogus"); len(keys) != 0 {
		t.Fatal("非法日期键必须返回空")
	}
	if ranges := HotUsageStatsRanges("w13g5-bogus"); len(ranges) != 0 {
		t.Fatal("非法今日键必须返回空")
	}
	if keys := DateKeysInRange("w13g5-bad", "2026-09-18"); len(keys) != 0 {
		t.Fatal("非法区间起点必须返回空")
	}
	if keys := DateKeysInRange("2026-09-18", "2026-09-01"); len(keys) != 0 {
		t.Fatal("倒序区间必须返回空")
	}
	if got := NextCalendarDateKey("w13g5-bad"); got != "w13g5-bad" {
		t.Fatal("非法日期必须原样返回")
	}
	grouped := RowsByStatHourDate([]string{"w1", "2026-09-18T00"}, func(v string) string { return v })
	if len(grouped) != 1 {
		t.Fatalf("短于 10 位的 stat_hour 必须跳过: %v", grouped)
	}
	if got := TrendBucketKey("2026-09-18TXX", 6); got != "2026-09-18TXX" {
		t.Fatal("小时解析失败必须原样返回")
	}
	if got := TrendBucketKey("2026-09-18", 6); got != "2026-09-18" {
		t.Fatal("过短 stat_hour 必须原样返回")
	}
}

func TestW13g5AuthorizationReportRowHelpers(t *testing.T) {
	row := authorizationReportRow{
		authorizationID: "account:w13g5", ownerSystemAccountID: GlobalStatsSystemAccountID,
		granteeSystemAccountID: "w13g5-grantee", resourceType: "account", resourceID: "w13g5-acc",
	}
	if scopes := authorizationReportScopeRows(row); len(scopes) != 1 {
		t.Fatal("global owner 不得追加 global 维度")
	}
	row.ownerSystemAccountID = "w13g5-owner"
	if scopes := authorizationReportScopeRows(row); len(scopes) != 2 {
		t.Fatal("非 global owner 必须追加 global 维度")
	}
	teamRow := row
	teamRow.sourceType = "team"
	teamRow.sourceTeamID = "w13g5-team"
	teamKeys, userKeys := authorizationSummaryKeys(teamRow, authorizationReportResourceFilters(teamRow))
	if len(teamKeys) != 6 || len(userKeys) != 12 {
		t.Fatalf("team 授权键展开必须含 team 维度: team=%d user=%d", len(teamKeys), len(userKeys))
	}
	noTeamKeys, _ := authorizationSummaryKeys(row, authorizationReportResourceFilters(row))
	if len(noTeamKeys) != 0 {
		t.Fatal("manual 授权不得展开 team 维度")
	}
}

func TestW13g5UpsertAuthorizationUsageReportRowsZeroRows(t *testing.T) {
	env, _ := w13g5StatsOpenFailEnv(t)
	ctx := context.Background()
	tx, err := env.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	// 无授权字段的行走零行快速路径。
	row := w13g5QualitySeedRow("w13g5-auth-none")
	if err := env.aggregator().upsertAuthorizationUsageReportRows(ctx, tx, row, "2026-09-18", "2026-09-18T00:00:00.000Z", nil); err != nil {
		t.Fatal(err)
	}
}

func TestW13g5StatsLagSecondsFromCursorNegativeClamp(t *testing.T) {
	// now 早于 cursor 时 lag 必须截断为 0。
	now := time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC)
	if got := statsLagSecondsFromCursor("2026-09-19T00:00:00.000Z", now); got != 0 {
		t.Fatalf("未来 cursor 必须截断为 0: %v", got)
	}
}
