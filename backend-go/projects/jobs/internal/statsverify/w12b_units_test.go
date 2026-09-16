package statsverify

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// w12b_units_test.go 覆盖 statsverify 的单元级分支：scan/日期/IP 归一辅助、
// usage_records 坏类型行解码、latestIgnored/lag 数据臂、client-ip 聚合与
// 写入路径、窗口刷新的 full/per-hash/stale/pending 臂、一致性检查错误臂、
// job 入口错误臂、groupstats join 行坏类型扫描。
//
// 不可达登记见 w12b_failinject_test.go 头注释；本文件补充：
//   - store.go sqliteDSN 的 filepath.Abs 错误臂：常规路径无法触发。
//   - clientipwindows.go 空窗口臂与重复窗口去重臂：FixedUsageStatsDateKeys
//     恒返回固定天数，候选窗口恒互异。
//   - groupstats.go per-group 路径里 join 行不属于目标的分支：join 查询
//     INNER JOIN groups 保证行必然属于目标组。
//   - clientipagg.go selectClientIPUsageRecords 的 created_at 非 RFC3339 臂：
//     WHERE created_at <= 游标安全线按文本词法比较，非法文本行恒被过滤。
//   - latestIgnoredCursorAndLag 同类：非 RFC3339 文本被词法谓词排除；
//     TEXT 亲和性列写入数值已被转为文本，sqlText/sqlStringPtr 恒成功。

// w12bSvStatsSpec 取当前 stats 句柄的注入 spec（供逐用例布防）。
func w12bSvStatsSpec(t *testing.T) *w12bFailSpec {
	t.Helper()
	w12bSvDriversMu.Lock()
	defer w12bSvDriversMu.Unlock()
	spec := w12bSvDrivers[w12bSvStatsDriverName]
	if spec == nil {
		spec = &w12bFailSpec{}
		w12bSvDrivers[w12bSvStatsDriverName] = spec
	}
	return spec
}

func TestW12bSvScanAndDateHelpers(t *testing.T) {
	// scan.go 错误臂。
	if _, err := sqlStringPtr(3.5); err == nil {
		t.Fatal("数值转字符串指针应报错")
	}
	if _, err := sqlIntPtr("w12b-not-int"); err == nil {
		t.Fatal("非法整型应报错")
	}
	if _, err := sqlFloatPtr(struct{}{}); err == nil {
		t.Fatal("未知类型应报错")
	}
	if p, err := sqlStringPtr(nil); p != nil || err != nil {
		t.Fatalf("NULL 字符串应返回 nil 指针: %v %v", p, err)
	}
	if p, err := sqlIntPtr(nil); p != nil || err != nil {
		t.Fatalf("NULL 整型应返回 nil 指针: %v %v", p, err)
	}
	if p, err := sqlFloatPtr(nil); p != nil || err != nil {
		t.Fatalf("NULL 数值应返回 nil 指针: %v %v", p, err)
	}
	// dates.go parseDateKeyParts 错误臂。
	if _, ok := parseDateKeyParts("w12b-bad-date"); ok {
		t.Fatal("非法日期应返回 false")
	}
}

func TestW12bSvIpNormDefensiveArms(t *testing.T) {
	// 前置校验后的防御臂：非 IPv4 输入返回 nil。
	if NormalizeClientIPForStats("w12b-not-an-ip") != nil {
		t.Fatal("非法 IP 应返回 nil")
	}
	if normalizeIpv4("::1") != "" {
		t.Fatal("IPv6 应返回空串")
	}
	if NormalizeClientIPForStats("2001:db8::1") != nil {
		t.Fatal("IPv6 应被排除")
	}
}

func TestW12bSvUsageRecordBadTypes(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	// client_ip/account_id 数值臂不可达：SQLite TEXT 亲和性在写入时已把数值
	// 转为文本，sqlStringPtr 恒收到字符串（登记不可达）。
	cases := []struct{ name, extraColumns, extraValues string }{
		{"success 文本", "", ""},
		{"first_token_ms 文本", ", first_token_ms", ", 'x'"},
		{"duration_ms 文本", ", duration_ms", ", 'x'"},
		{"input_tokens 文本", ", input_tokens", ", 'x'"},
		{"output_tokens 文本", ", output_tokens", ", 'x'"},
		{"cache_read_tokens 文本", ", cache_read_tokens", ", 'x'"},
		{"cache_read_cost 文本", ", cache_read_cost_usd", ", 'x'"},
		{"cache_write_tokens 文本", ", cache_write_tokens", ", 'x'"},
		{"cache_write_1h 文本", ", cache_write_1h_tokens", ", 'x'"},
		{"cache_write_cost 文本", ", cache_write_cost_usd", ", 'x'"},
		{"thinking_tokens 文本", ", thinking_tokens", ", 'x'"},
		{"input_image_tokens 文本", ", input_image_tokens", ", 'x'"},
		{"output_image_tokens 文本", ", output_image_tokens", ", 'x'"},
		{"total_cost 文本", ", cost_usd", ", 'x'"},
	}
	for _, tc := range cases {
		store, _, _ := w12bSvOpenStore(t)
		mustExec(t, ctx, store.business, `INSERT INTO system_settings (system_account_id, key, value_json) VALUES ('sys_admin', 'usageStatsTimezone', '"UTC"')`)
		rowID := "w12b-bad-" + tc.name
		switch tc.name {
		case "success 文本":
			mustExec(t, ctx, store.db, `INSERT INTO usage_records (id, system_account_id, trace_id, traffic_source, success, created_at)
				VALUES (?, 'sys', 't', 'api', 'abc', '2026-09-17T10:00:00.000Z')`, rowID)
		default:
			query := `INSERT INTO usage_records (id, system_account_id, trace_id, traffic_source, success, created_at, ` + strings.TrimPrefix(tc.extraColumns, ", ") + `)
				VALUES ('` + rowID + `', 'sys', 't', 'api', 1, '2026-09-17T10:00:00.000Z', ` + strings.TrimPrefix(tc.extraValues, ", ") + `)`
			mustExec(t, ctx, store.db, query)
		}
		if _, err := store.AggregateClientIPStatsBatch(ctx, 10, now); err == nil {
			t.Fatalf("%s 应解码失败", tc.name)
		}
	}
}

func TestW12bSvIgnoredCursorDataArms(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)

	// created_at 数值：ignored created_at 非 RFC3339 解析错误臂（词法命中）。
	store, _, _ := w12bSvOpenStore(t)
	mustExec(t, ctx, store.business, `INSERT INTO system_settings (system_account_id, key, value_json) VALUES ('sys_admin', 'usageStatsTimezone', '"UTC"')`)
	mustExec(t, ctx, store.db, `INSERT INTO usage_records (id, system_account_id, trace_id, traffic_source, success, created_at)
		VALUES ('w12b-ign', 'sys', 't', 'runtime_recovery_probe', 1, 12345)`)
	if _, err := store.AggregateClientIPStatsBatch(ctx, 10, now); err == nil {
		t.Fatal("ignored created_at 数值应报错")
	}

	// lag 查询最新行 created_at 数值文本（词法命中后解析失败）。
	store4, _, _ := w12bSvOpenStore(t)
	mustExec(t, ctx, store4.business, `INSERT INTO system_settings (system_account_id, key, value_json) VALUES ('sys_admin', 'usageStatsTimezone', '"UTC"')`)
	mustExec(t, ctx, store4.db, `INSERT INTO usage_records (id, system_account_id, trace_id, traffic_source, success, created_at)
		VALUES ('w12b-lag', 'sys', 't', 'api', 1, 12345)`)
	if _, err := store4.AggregateClientIPStatsBatch(ctx, 10, now); err == nil {
		t.Fatal("lag created_at 数值应报错")
	}
}

func TestW12bSvJobStateBadLag(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	store, _, _ := w12bSvOpenStore(t)
	mustExec(t, ctx, store.business, `INSERT INTO system_settings (system_account_id, key, value_json) VALUES ('sys_admin', 'usageStatsTimezone', '"UTC"')`)
	mustExec(t, ctx, store.db, `INSERT INTO stats_job_state (scope_type, scope_id, job_name, lag_seconds, updated_at)
		VALUES ('global', '', 'client_ip_stats_aggregation', 'abc', '2026-09-17T00:00:00.000Z')`)
	if _, err := store.AggregateClientIPStatsBatch(ctx, 10, now); err == nil {
		t.Fatal("lag_seconds 文本应报错")
	}
}

func TestW12bSvAggregateWriteArms(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	seed := func(t *testing.T) (*Store, *w12bFailSpec) {
		store, statsSpec, _ := w12bSvOpenStore(t)
		mustExec(t, ctx, store.business, `INSERT INTO system_settings (system_account_id, key, value_json) VALUES ('sys_admin', 'usageStatsTimezone', '"UTC"')`)
		insertUsageRecord(t, ctx, store, UsageStatsRecordRow{
			ID: "w12b-agg", SystemAccountID: "sys", ClientIP: strPtr("1.2.3.4"), AccountID: strPtr("acc"),
			Success: 1, CreatedAt: "2026-09-17T10:00:00.000Z",
		})
		return store, statsSpec
	}
	arms := []string{
		"INSERT INTO client_ip_registry",
		"INSERT INTO client_ip_stats_daily",
		"INSERT INTO client_ip_account_stats_daily",
		"INSERT INTO client_ip_range_window_dirty_ips",
		"INSERT INTO client_ip_account_range_window_dirty_ips",
	}
	for _, match := range arms {
		store, statsSpec := seed(t)
		statsSpec.arm(match)
		if _, err := store.AggregateClientIPStatsBatch(ctx, 10, now); err == nil {
			t.Fatalf("%s 注入应报错", match)
		}
		statsSpec.disarm()
	}
	// 空批次 COMMIT 注入。
	store, statsSpec := seed(t)
	if _, err := store.AggregateClientIPStatsBatch(ctx, 10, now); err != nil {
		t.Fatalf("首次聚合应成功: %v", err)
	}
	statsSpec.arm("COMMIT")
	if _, err := store.AggregateClientIPStatsBatch(ctx, 10, now); err == nil {
		t.Fatal("空批次 COMMIT 注入应报错")
	}
	statsSpec.disarm()
	// 有批次的 job state UPSERT 注入。
	store2, statsSpec2 := seed(t)
	statsSpec2.arm("INSERT INTO stats_job_state")
	if _, err := store2.AggregateClientIPStatsBatch(ctx, 10, now); err == nil {
		t.Fatal("批次内 job state UPSERT 注入应报错")
	}
	statsSpec2.disarm()
}

func TestW12bSvWindowRefreshArms(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	markDirty := func(store *Store) {
		mustExec(t, ctx, store.db, `INSERT INTO client_ip_range_window_dirty_ips (ip_hash, first_dirty_at, generation, updated_at) VALUES ('w12b-hash', '2026-09-17T00:00:00.000Z', 1, '2026-09-17T00:00:00.000Z')`)
		mustExec(t, ctx, store.db, `INSERT INTO client_ip_account_range_window_dirty_ips (ip_hash, first_dirty_at, generation, updated_at) VALUES ('w12b-hash', '2026-09-17T00:00:00.000Z', 1, '2026-09-17T00:00:00.000Z')`)
	}
	seed := func(t *testing.T) *Store {
		store, _, _ := w12bSvOpenStore(t)
		mustExec(t, ctx, store.business, `INSERT INTO system_settings (system_account_id, key, value_json) VALUES ('sys_admin', 'usageStatsTimezone', '"UTC"')`)
		return store
	}

	// full 路径：账户窗口 / IP 窗口 / 清理脏表注入。
	for _, match := range []string{
		"DELETE FROM client_ip_account_usage_range_windows",
		"INSERT INTO client_ip_account_usage_range_windows",
		"DELETE FROM client_ip_usage_range_windows",
		"INSERT INTO client_ip_usage_range_windows",
		"DELETE FROM client_ip_range_window_dirty_ips",
		"DELETE FROM client_ip_account_range_window_dirty_ips",
	} {
		store := seed(t)
		markDirty(store)
		statsSpec := w12bSvStatsSpec(t)
		statsSpec.arm(match)
		if err := store.RefreshClientIPUsageRangeWindows(ctx, ClientIPRangeWindowRefreshOptions{Full: true, Now: now}); err == nil {
			t.Fatalf("full 路径 %s 注入应报错", match)
		}
		statsSpec.disarm()
	}
	// full 路径成功臂（窗口重建 + ready 标记）。
	store := seed(t)
	markDirty(store)
	if err := store.RefreshClientIPUsageRangeWindows(ctx, ClientIPRangeWindowRefreshOptions{Full: true, Now: now}); err != nil {
		t.Fatalf("full 刷新应成功: %v", err)
	}

	// per-hash 路径注入：IP 窗口 / 账户窗口 / 清理。
	for _, match := range []string{
		"AND ip_hash IN",
		"client_ip_account_usage_range_windows",
		"DELETE FROM client_ip_account_range_window_dirty_ips",
		"INSERT INTO stats_job_state",
	} {
		store := seed(t)
		markDirty(store)
		statsSpec := w12bSvStatsSpec(t)
		statsSpec.arm(match)
		if err := store.RefreshClientIPUsageRangeWindows(ctx, ClientIPRangeWindowRefreshOptions{Now: now}); err == nil {
			t.Fatalf("per-hash 路径 %s 注入应报错", match)
		}
		statsSpec.disarm()
	}

	// stale 路径：空脏表 + 窗口 state 存在但 last_success_at 为空。
	store = seed(t)
	mustExec(t, ctx, store.db, `INSERT INTO stats_job_state (scope_type, scope_id, job_name, updated_at)
		VALUES ('client_ip_range_window', '2026-09-17:2026-09-17', 'client_ip_range_window_refresh', '2026-09-17T00:00:00.000Z')`)
	statsSpec := w12bSvStatsSpec(t)
	statsSpec.arm("scope_type = ")
	if err := store.RefreshClientIPUsageRangeWindows(ctx, ClientIPRangeWindowRefreshOptions{Now: now}); err == nil {
		t.Fatal("stale 查询注入应报错")
	}
	statsSpec.disarm()
	// stale 为真后窗口重建注入。
	statsSpec.arm("INSERT INTO client_ip_usage_range_windows")
	if err := store.RefreshClientIPUsageRangeWindows(ctx, ClientIPRangeWindowRefreshOptions{Now: now}); err == nil {
		t.Fatal("stale 重建注入应报错")
	}
	statsSpec.disarm()
	// stale 为假 + 无脏行：无操作提交。
	if err := store.RefreshClientIPUsageRangeWindows(ctx, ClientIPRangeWindowRefreshOptions{Now: now}); err != nil {
		t.Fatalf("干净路径应成功: %v", err)
	}

	// hasPendingClientIPRangeWindowDirty 直调：内存短路 / 空表 / 查询注入。
	store2, statsSpec2, _ := w12bSvOpenStore(t)
	tx, err := store2.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := store2.hasPendingClientIPRangeWindowDirty(ctx, tx)
	if err != nil || pending {
		t.Fatalf("空表应无 pending: %v %v", pending, err)
	}
	store2.rememberDirtyIPHashes([]string{"w12b-hash"})
	pending, err = store2.hasPendingClientIPRangeWindowDirty(ctx, tx)
	if err != nil || !pending {
		t.Fatalf("内存哈希应短路为 pending: %v %v", pending, err)
	}
	store2.forgetDirtyIPHashes([]string{"w12b-hash"})
	statsSpec2.arm("SELECT 1 FROM")
	if _, err := store2.hasPendingClientIPRangeWindowDirty(ctx, tx); err == nil {
		t.Fatal("pending 查询注入应报错")
	}
	statsSpec2.disarm()
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}

	// markClientIPUsageRangeWindowsReady 错误臂。
	store3, statsSpec3, _ := w12bSvOpenStore(t)
	windows := ClientIPRangeWindowsForTimezone(time.UTC, now)
	statsSpec3.arm("INSERT INTO stats_job_state")
	if err := store3.markClientIPUsageRangeWindowsReady(ctx, store3.db, windows, "2026-09-17T00:00:00.000Z"); err == nil {
		t.Fatal("ready 标记注入应报错")
	}
	statsSpec3.disarm()
	if err := store3.markClientIPUsageRangeWindowsReady(ctx, store3.db, windows, "2026-09-17T00:00:00.000Z"); err != nil {
		t.Fatalf("ready 标记应成功: %v", err)
	}
}

func TestW12bSvConsistencyAndJobsArms(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)

	// nil store 与时区错误臂。
	var nilStore *Store
	if _, err := nilStore.CheckUsageStatsConsistency(ctx, UsageStatsConsistencyOptions{Now: now}); err == nil {
		t.Fatal("nil store 应报错")
	}
	store, _, businessSpec := w12bSvOpenStore(t)
	businessSpec.arm("system_settings")
	if _, err := store.CheckUsageStatsConsistency(ctx, UsageStatsConsistencyOptions{Now: now}); err == nil {
		t.Fatal("时区注入应报错")
	}
	businessSpec.disarm()

	// 一致性样本行坏类型（sqlFloat 错误臂）。
	mustExec(t, ctx, store.business, `INSERT INTO system_settings (system_account_id, key, value_json) VALUES ('sys_admin', 'usageStatsTimezone', '"UTC"')`)
	mustExec(t, ctx, store.db, `INSERT INTO usage_stats_daily (
		system_account_id, scope_type, scope_id, stat_date, request_count, success_count, error_count,
		input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, cache_write_tokens, cache_write_1h_tokens,
		cache_write_cost_usd, thinking_tokens, input_image_tokens, output_image_tokens, total_cost_usd, updated_at
	) VALUES ('sys', 'system', '', '2026-09-16', 'abc', 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, '2026-09-16T00:00:00.000Z')`)
	if _, err := store.CheckUsageStatsConsistency(ctx, UsageStatsConsistencyOptions{Now: now}); err == nil {
		t.Fatal("样本坏类型应报错")
	}

	// job 入口：gate 错误 / 聚合错误 / 窗口刷新错误。
	gateErr := errors.New("w12b gate 失败")
	if _, err := store.RunClientIPStatsAggregation(ctx, RunClientIPStatsAggregationOptions{
		IngestGate: w12bFailGate{err: gateErr}, Clock: NewFixedClock(now),
	}); !errors.Is(err, gateErr) {
		t.Fatalf("gate 错误应透传: %v", err)
	}
	store2, statsSpec2, _ := w12bSvOpenStore(t)
	mustExec(t, ctx, store2.business, `INSERT INTO system_settings (system_account_id, key, value_json) VALUES ('sys_admin', 'usageStatsTimezone', '"UTC"')`)
	statsSpec2.arm("FROM usage_records")
	if _, err := store2.RunClientIPStatsAggregation(ctx, RunClientIPStatsAggregationOptions{
		IngestGate: w12bStubGate{}, Clock: NewFixedClock(now),
	}); err == nil {
		t.Fatal("聚合注入应报错")
	}
	statsSpec2.disarm()
	if _, err := store2.RunGroupAccountStatsRefresh(ctx, now); err != nil {
		t.Fatalf("组统计 job 空表应成功: %v", err)
	}
	statsSpec2.arm("FROM usage_stats_daily")
	if _, err := store2.RunUsageStatsConsistencyCheck(ctx, now, w12bSvDiscardLogger()); err == nil {
		t.Fatal("一致性 job 注入应报错")
	}
	statsSpec2.disarm()

	// boundPositiveInt 边界与 nil-consistency 等归一函数。
	if boundPositiveInt(0, 1, 10) != 1 || boundPositiveInt(99, 1, 10) != 10 || boundPositiveInt(5, 1, 10) != 5 {
		t.Fatal("boundPositiveInt 不符")
	}
	if boundConsistencySampleLimit(0) != consistencySampleLimitDefault || boundConsistencySampleLimit(1000) != consistencySampleLimitMax {
		t.Fatal("boundConsistencySampleLimit 不符")
	}
	if consistencyMetricTolerance("total_cost_usd") == 0 || consistencyMetricTolerance("request_count") != 0 {
		t.Fatal("consistencyMetricTolerance 不符")
	}
	if len(ClientIPRangeWindowsForTimezone(time.UTC, now)) == 0 {
		t.Fatal("UTC 窗口不应为空")
	}
	if got := chunkStrings([]string{"a", "b", "c"}, 2); len(got) != 2 || len(got[1]) != 1 {
		t.Fatalf("chunkStrings 余量分片不符: %#v", got)
	}
}

type w12bFailGate struct{ err error }

func (g w12bFailGate) EnsureUsageRecordsIngested(context.Context) error { return g.err }

func TestW12bSvBuildAggregatesArms(t *testing.T) {
	location := time.UTC
	bad := []UsageStatsRecordRow{{ID: "r", ClientIP: strPtr("1.2.3.4"), CreatedAt: "not-a-time"}}
	if _, _, err := buildClientIPAggregates(bad, location); err == nil {
		t.Fatal("非法 created_at 应报错")
	}
	good := UsageStatsRecordRow{
		ID: "r1", SystemAccountID: "sys", ClientIP: strPtr("1.2.3.4"), AccountID: strPtr("acc"),
		Success: 1, DurationMs: intPtr(100), CostUsd: f64Ptr(0.5), CreatedAt: "2026-09-17T10:00:00.000Z",
	}
	sameKey := UsageStatsRecordRow{
		ID: "r2", SystemAccountID: "sys", ClientIP: strPtr("1.2.3.4"), AccountID: strPtr("acc"),
		Success: 0, DurationMs: intPtr(300), CreatedAt: "2026-09-17T11:00:00.000Z",
	}
	ips, accounts, err := buildClientIPAggregates([]UsageStatsRecordRow{good, sameKey}, location)
	if err != nil {
		t.Fatal(err)
	}
	if len(ips) != 1 || len(accounts) != 1 {
		t.Fatalf("同键应合并: %d/%d", len(ips), len(accounts))
	}
	if ips[0].durationMsMax != 300 || ips[0].requestCount != 2 {
		t.Fatalf("累加不符: %+v", ips[0])
	}
	// 无 IP 行应被跳过。
	ips2, accounts2, err := buildClientIPAggregates([]UsageStatsRecordRow{{ID: "r3", CreatedAt: "2026-09-17T10:00:00.000Z"}}, location)
	if err != nil || len(ips2) != 0 || len(accounts2) != 0 {
		t.Fatalf("无 IP 行应跳过: %v %v", err, ips2)
	}
}

func TestW12bSvAggregateCursorUpdateArm(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	store, statsSpec, _ := w12bSvOpenStore(t)
	mustExec(t, ctx, store.business, `INSERT INTO system_settings (system_account_id, key, value_json) VALUES ('sys_admin', 'usageStatsTimezone', '"UTC"')`)
	insertUsageRecord(t, ctx, store, UsageStatsRecordRow{
		ID: "w12b-cursor", SystemAccountID: "sys", ClientIP: strPtr("1.2.3.4"), Success: 1,
		CreatedAt: "2026-09-17T10:00:00.000Z",
	})
	// job state UPSERT 专属匹配（含 cursor_created_at 列，区别于窗口 stale 标记）。
	statsSpec.arm("job_name, cursor_created_at")
	if _, err := store.AggregateClientIPStatsBatch(ctx, 10, now); err == nil {
		t.Fatal("批次内游标 UPSERT 注入应报错")
	}
	statsSpec.disarm()
}

func TestW12bSvLagParseFallback(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	store, _, _ := w12bSvOpenStore(t)
	mustExec(t, ctx, store.business, `INSERT INTO system_settings (system_account_id, key, value_json) VALUES ('sys_admin', 'usageStatsTimezone', '"UTC"')`)
	mustExec(t, ctx, store.db, `INSERT INTO usage_records (id, system_account_id, trace_id, traffic_source, success, created_at)
		VALUES ('w12b-lagtext', 'sys', 't', 'api', 1, '12345')`)
	if _, err := store.AggregateClientIPStatsBatch(ctx, 10, now); err == nil {
		t.Fatal("lag created_at 词法命中但非法应报错")
	}
}

func TestW12bSvWindowRefreshExtraArms(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	seed := func(t *testing.T) *Store {
		store, _, _ := w12bSvOpenStore(t)
		mustExec(t, ctx, store.business, `INSERT INTO system_settings (system_account_id, key, value_json) VALUES ('sys_admin', 'usageStatsTimezone', '"UTC"')`)
		return store
	}

	// BEGIN 注入。
	store := seed(t)
	statsSpec := w12bSvStatsSpec(t)
	statsSpec.arm("BEGIN")
	if err := store.RefreshClientIPUsageRangeWindows(ctx, ClientIPRangeWindowRefreshOptions{Now: now}); err == nil {
		t.Fatal("窗口刷新 BEGIN 注入应报错")
	}
	statsSpec.disarm()

	// full 路径 markReady 注入。
	store2 := seed(t)
	mustExec(t, ctx, store2.db, `INSERT INTO client_ip_range_window_dirty_ips (ip_hash, first_dirty_at, generation, updated_at) VALUES ('w12b-h2', '2026-09-17T00:00:00.000Z', 1, '2026-09-17T00:00:00.000Z')`)
	mustExec(t, ctx, store2.db, `INSERT INTO client_ip_account_range_window_dirty_ips (ip_hash, first_dirty_at, generation, updated_at) VALUES ('w12b-h2', '2026-09-17T00:00:00.000Z', 1, '2026-09-17T00:00:00.000Z')`)
	statsSpec2 := w12bSvStatsSpec(t)
	statsSpec2.arm("INSERT INTO stats_job_state")
	if err := store2.RefreshClientIPUsageRangeWindows(ctx, ClientIPRangeWindowRefreshOptions{Full: true, Now: now}); err == nil {
		t.Fatal("full ready 标记注入应报错")
	}
	statsSpec2.disarm()

	// per-hash：不同哈希分表 + usage 窗口 INSERT 注入 + 清理注入 + generation 变体。
	store3 := seed(t)
	mustExec(t, ctx, store3.db, `INSERT INTO client_ip_range_window_dirty_ips (ip_hash, first_dirty_at, generation, updated_at) VALUES ('w12b-h3', '2026-09-17T00:00:00.000Z', 1, '2026-09-17T00:00:00.000Z')`)
	mustExec(t, ctx, store3.db, `INSERT INTO client_ip_account_range_window_dirty_ips (ip_hash, first_dirty_at, generation, updated_at) VALUES ('w12b-h4', '2026-09-17T00:00:00.000Z', 1, '2026-09-17T00:00:00.000Z')`)
	statsSpec3 := w12bSvStatsSpec(t)
	statsSpec3.arm("INSERT INTO client_ip_usage_range_windows")
	if err := store3.RefreshClientIPUsageRangeWindows(ctx, ClientIPRangeWindowRefreshOptions{Now: now}); err == nil {
		t.Fatal("per-hash usage 窗口 INSERT 注入应报错")
	}
	statsSpec3.disarm()

	store4 := seed(t)
	mustExec(t, ctx, store4.db, `INSERT INTO client_ip_range_window_dirty_ips (ip_hash, first_dirty_at, generation, updated_at) VALUES ('w12b-h3', '2026-09-17T00:00:00.000Z', 1, '2026-09-17T00:00:00.000Z')`)
	mustExec(t, ctx, store4.db, `INSERT INTO client_ip_account_range_window_dirty_ips (ip_hash, first_dirty_at, generation, updated_at) VALUES ('w12b-h4', '2026-09-17T00:00:00.000Z', 1, '2026-09-17T00:00:00.000Z')`)
	statsSpec4 := w12bSvStatsSpec(t)
	statsSpec4.arm("DELETE FROM client_ip_range_window_dirty_ips")
	if err := store4.RefreshClientIPUsageRangeWindows(ctx, ClientIPRangeWindowRefreshOptions{Now: now}); err == nil {
		t.Fatal("清理脏表注入应报错")
	}
	statsSpec4.disarm()

	// readDirtyRows 数据臂：generation=0 跳过 / generation 文本错误。
	store5 := seed(t)
	mustExec(t, ctx, store5.db, `INSERT INTO client_ip_range_window_dirty_ips (ip_hash, first_dirty_at, generation, updated_at) VALUES ('w12b-zero', '2026-09-17T00:00:00.000Z', 0, '2026-09-17T00:00:00.000Z')`)
	if err := store5.RefreshClientIPUsageRangeWindows(ctx, ClientIPRangeWindowRefreshOptions{Now: now}); err != nil {
		t.Fatalf("generation=0 应被跳过: %v", err)
	}
	store6 := seed(t)
	mustExec(t, ctx, store6.db, `INSERT INTO client_ip_range_window_dirty_ips (ip_hash, first_dirty_at, generation, updated_at) VALUES ('w12b-text', '2026-09-17T00:00:00.000Z', 'abc', '2026-09-17T00:00:00.000Z')`)
	if err := store6.RefreshClientIPUsageRangeWindows(ctx, ClientIPRangeWindowRefreshOptions{Now: now}); err == nil {
		t.Fatal("generation 文本应报错")
	}

	// hasPending 数据命中臂：脏表有行且内存为空。
	store7 := seed(t)
	mustExec(t, ctx, store7.db, `INSERT INTO client_ip_range_window_dirty_ips (ip_hash, first_dirty_at, generation, updated_at) VALUES ('w12b-pend', '2026-09-17T00:00:00.000Z', 1, '2026-09-17T00:00:00.000Z')`)
	tx, err := store7.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := store7.hasPendingClientIPRangeWindowDirty(ctx, tx)
	if err != nil || !pending {
		t.Fatalf("脏表有行应 pending: %v %v", pending, err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}

	// chunkDirtyRows 余量分片。
	rows := make([]clientIPRangeWindowDirtyRow, 3)
	if got := chunkDirtyRows(rows, 2); len(got) != 2 || len(got[1]) != 1 {
		t.Fatalf("chunkDirtyRows 余量分片不符: %d", len(got))
	}
	if got := chunkDirtyRows(rows, 0); len(got) != 3 {
		t.Fatalf("size<1 应按 1 分片: %d", len(got))
	}
}

func TestW12bSvAccumulatorMergeArms(t *testing.T) {
	target := newClientIPAggregate(NormalizeClientIPForStats("1.2.3.4"), "2026-09-17", "acc", UsageStatsAccumulator{}, "2026-09-17T10:00:00.000Z")
	// lastErrorAt 新旧替换 + first/last seen 比较臂。
	addAccumulatorToClientIPAggregate(target, UsageStatsAccumulator{RequestCount: 1, LastErrorAt: "2026-09-17T11:00:00.000Z"}, "2026-09-17T12:00:00.000Z")
	addAccumulatorToClientIPAggregate(target, UsageStatsAccumulator{RequestCount: 1, LastErrorAt: "2026-09-17T10:30:00.000Z"}, "2026-09-17T09:00:00.000Z")
	if target.lastErrorAt != "2026-09-17T11:00:00.000Z" {
		t.Fatalf("lastErrorAt 应保留最新: %s", target.lastErrorAt)
	}
	if target.firstSeenAt != "2026-09-17T09:00:00.000Z" || target.lastUsedAt != "2026-09-17T12:00:00.000Z" {
		t.Fatalf("first/last seen 不符: %s %s", target.firstSeenAt, target.lastUsedAt)
	}
	// panic 臂：坏 createdAt（fail-fast 语义）。
	assertPanics(t, func() {
		addAccumulatorToClientIPAggregate(target, UsageStatsAccumulator{}, "w12b-bad")
	})
	// registryAggregatesFromIPAggregates 合并臂。
	second := newClientIPAggregate(NormalizeClientIPForStats("1.2.3.4"), "2026-09-18", "", UsageStatsAccumulator{}, "2026-09-17T08:00:00.000Z")
	second.lastUsedAt = "2026-09-17T13:00:00.000Z"
	entries := registryAggregatesFromIPAggregates([]*clientIPAggregate{target, second})
	if len(entries) != 1 || entries[0].firstSeenAt != "2026-09-17T08:00:00.000Z" || entries[0].lastSeenAt != "2026-09-17T13:00:00.000Z" {
		t.Fatalf("registry 合并不符: %+v", entries)
	}
}

func assertPanics(t *testing.T, fn func()) {
	t.Helper()
	defer func() {
		if recovered := recover(); recovered == nil {
			t.Fatal("应触发 fail-fast panic")
		}
	}()
	fn()
}
