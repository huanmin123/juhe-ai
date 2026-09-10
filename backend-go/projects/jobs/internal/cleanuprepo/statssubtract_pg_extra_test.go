package cleanuprepo

import (
	"context"
	"database/sql/driver"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/statsagg"
)

// statssubtractpostgres.go 剩余路径的录制驱动测试：估算回填、聚合条目合并、
// 授权查找、team 过滤日报扣减、quality 脏账户多行 upsert、派生窗口脏范围。

// TestApplyPostgresEstimatedCacheReadCost：cache_read_cost 缺失时的估算回填。
func TestApplyPostgresEstimatedCacheReadCost(t *testing.T) {
	row := statsagg.UsageStatsRecordRow{ProviderCode: kitText("openai"), Model: kitText("gpt-kit"), CacheReadTokens: kitNum(10)}
	// estimator 缺省 → 不回填。
	untouched := row
	applyPostgresEstimatedCacheReadCost(&untouched, nil)
	if untouched.CacheReadCostUsd != nil {
		t.Fatalf("nil estimator 不应回填")
	}
	// 估算失败 / 非正数 → 不回填。
	noEstimate := row
	applyPostgresEstimatedCacheReadCost(&noEstimate, func(providerCode, model string, tokens float64) (float64, bool) {
		return 0, false
	})
	if noEstimate.CacheReadCostUsd != nil {
		t.Fatalf("估算失败不应回填")
	}
	zero := row
	applyPostgresEstimatedCacheReadCost(&zero, func(providerCode, model string, tokens float64) (float64, bool) {
		return 0, true
	})
	if zero.CacheReadCostUsd != nil {
		t.Fatalf("零值不应回填")
	}
	// 估算成功 → 回填；tokens 缺省按 0 传入。
	withCost := row
	withCost.CacheReadTokens = nil
	applyPostgresEstimatedCacheReadCost(&withCost, func(providerCode, model string, tokens float64) (float64, bool) {
		if tokens != 0 || providerCode != "openai" || model != "gpt-kit" {
			t.Fatalf("estimator 参数 = %q/%q/%v", providerCode, model, tokens)
		}
		return 0.25, true
	})
	if withCost.CacheReadCostUsd == nil || *withCost.CacheReadCostUsd != 0.25 {
		t.Fatalf("回填 = %v", withCost.CacheReadCostUsd)
	}
	// 已有值不覆盖。
	keep := statsagg.UsageStatsRecordRow{CacheReadCostUsd: kitNum(1), CacheReadTokens: kitNum(2)}
	applyPostgresEstimatedCacheReadCost(&keep, func(string, string, float64) (float64, bool) { return 9, true })
	if *keep.CacheReadCostUsd != 1 {
		t.Fatalf("已有值不应覆盖")
	}
	if got := orZeroFloat(nil); got != 0 {
		t.Fatalf("orZeroFloat(nil) = %v", got)
	}
	if got := orZeroFloat(kitNum(3.5)); got != 3.5 {
		t.Fatalf("orZeroFloat = %v", got)
	}
	if got := trimFloatStatus(404); got != "404" {
		t.Fatalf("trimFloatStatus(404) = %q", got)
	}
	if got := trimFloatStatus(404.5); got != "404.5" {
		t.Fatalf("trimFloatStatus(404.5) = %q", got)
	}
	if got := nullableNumberPG(func() *float64 { v := math.NaN(); return &v }()); got != nil {
		t.Fatalf("NaN 应归一 NULL")
	}
	inf := math.Inf(1)
	if got := nullableNumberPG(&inf); got != nil {
		t.Fatalf("Inf 应归一 NULL")
	}
	value := 3.0
	if got := nullableNumberPG(&value); got == nil || *got != 3 {
		t.Fatalf("有限数值应保留")
	}
	if got := nullableNumberPG(nil); got != nil {
		t.Fatalf("nil 应保留")
	}
}

// TestPostgresAggregateEntryMerge：totals/time 桶聚合条目按 key 合并且保持
// 首次插入顺序。
func TestPostgresAggregateEntryMerge(t *testing.T) {
	entry := statsagg.UsageStatsEntry{SystemAccountID: "sys-1", ScopeType: "system_account", ScopeID: "sys-1"}
	state := &postgresSubtractState{}
	state.addStatsEntry(&state.totals, entry)
	second := entry
	second.Accumulator = statsagg.UsageStatsAccumulator{RequestCount: 2}
	state.addStatsEntry(&state.totals, second)
	listed := state.totals.list()
	if len(listed) != 1 || listed[0].accumulator.RequestCount != 2 {
		t.Fatalf("merge 后 = %+v", listed)
	}
	bucket := usageStatsBucketDefs[0]
	state.addTimeEntry(bucket, "m", entry)
	state.addTimeEntry(bucket, "m", second)
	if got := state.timeBuckets.list(); len(got) != 1 || got[0].accumulator.RequestCount != 2 {
		t.Fatalf("time merge = %+v", got)
	}
}

// TestAddQualityEntryMerge：同 (account, minute) 合并；探针流量/缺维度跳过。
func TestAddQualityEntryMerge(t *testing.T) {
	row := statsagg.UsageStatsRecordRow{
		SystemAccountID: "sys-1", Success: 1, AccountID: kitText("acc-1"),
		APIKeyID: kitText("key-1"), FirstTokenMs: kitNum(120), CreatedAt: kitCreatedAt,
	}
	timeKeys := statsagg.UsageStatsTimeKeys{StatMinute: "m"}
	state := &postgresSubtractState{}
	state.addQualityEntry(row, timeKeys)
	state.addQualityEntry(row, timeKeys)
	failed := row
	failed.Success = 0
	failed.FailureAttribution = kitText("account_upstream")
	state.addQualityEntry(failed, timeKeys)
	listed := state.quality.list()
	if len(listed) != 1 {
		t.Fatalf("应合并为单条目：%+v", listed)
	}
	item := listed[0]
	if item.requestCount != 3 || item.successCount != 2 || item.errorCount != 1 ||
		item.firstTokenMsSum != 240 || item.firstTokenMsCount != 2 {
		t.Fatalf("合并条目 = %+v", item)
	}
	probe := row
	probe.TrafficSource = "runtime_recovery_probe"
	state.addQualityEntry(probe, timeKeys)
	noAccount := row
	noAccount.AccountID = nil
	state.addQualityEntry(noAccount, timeKeys)
	if len(state.quality.list()) != 1 {
		t.Fatalf("探针/缺维度行不应计 quality")
	}
}

// TestPostgresAuthorizationLookup：business 缺省 fail-closed、空 ids 直通、
// 查找结果归并 resource/instance 归属。
func TestPostgresAuthorizationLookup(t *testing.T) {
	store := &RecordCleanupStore{Stats: openRecorderPG(newPGRecorder())}
	// business 缺省且存在授权 ID → 报错。
	row := statsagg.UsageStatsRecordRow{AccountAuthorizationID: kitText("auth-1")}
	if _, err := store.createPostgresUsageStatsAuthorizationLookup(context.Background(), store.Stats, []statsagg.UsageStatsRecordRow{row}); err == nil {
		t.Fatalf("缺 business 句柄应报错")
	}
	// 空 ids → 空 lookup。
	rec := newPGRecorder()
	store = &RecordCleanupStore{Stats: openRecorderPG(rec), Business: openRecorderPG(rec)}
	lookup, err := store.createPostgresUsageStatsAuthorizationLookup(context.Background(), store.Stats, []statsagg.UsageStatsRecordRow{{}})
	if err != nil || lookup == nil || len(rec.all()) != 0 {
		t.Fatalf("空 ids = %+v, %v", lookup, err)
	}
	// 授权查找：resource + instance 归并。
	store.Business = openRecorderPG(rec)
	rec.script("FROM juhe_business.resource_authorizations authorizations", []string{"id", "resource_id", "instance_account_id"},
		[][]driver.Value{{"auth-1", "acc-real", "acc-inst-1"}})
	lookup, err = store.createPostgresUsageStatsAuthorizationLookup(context.Background(), store.Stats,
		[]statsagg.UsageStatsRecordRow{{AccountAuthorizationID: kitText("auth-1")}})
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if lookup.AccountAuthorizationResourceIDs["auth-1"] != "acc-real" ||
		lookup.AccountAuthorizationInstanceAccountIDs["auth-1"] != "acc-inst-1" {
		t.Fatalf("lookup = %+v", lookup)
	}
}

// TestSubtractAuthorizationSummaryRowsPostgresTeam：team source 展开的
// team/user 过滤键与空行清理。
func TestSubtractAuthorizationSummaryRowsPostgresTeam(t *testing.T) {
	rec := newPGRecorder()
	store := &RecordCleanupStore{Stats: openRecorderPG(rec)}
	tx, err := store.Stats.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	row := postgresAuthorizationReportRow{
		authorizationID: "account:auth-1", owner: "owner-2", grantee: "sys-1",
		resourceType: "account", resourceID: "acc-1",
		sourceType: kitText("team"), sourceTeamID: kitText("team-9"),
	}
	filters := []postgresAuthorizationFilter{
		{resourceFilterType: "all"}, {resourceFilterType: "account"}, {resourceFilterType: "account", resourceFilterID: "acc-1"},
	}
	if err := store.subtractAuthorizationSummaryRowsPostgres(context.Background(), tx, row, filters,
		statsagg.UsageStatsAccumulator{RequestCount: 1}, "2026-01-05", kitUpdatedAt); err != nil {
		t.Fatalf("subtractAuthorizationSummaryRowsPostgres: %v", err)
	}
	statements := rec.all()
	teamUpdates, userUpdates, teamDeletes, userDeletes := 0, 0, 0, 0
	for _, statement := range statements {
		switch {
		case strings.Contains(statement.query, "UPDATE juhe_stats.authorization_team_usage_summary_daily"):
			teamUpdates++
		case strings.Contains(statement.query, "UPDATE juhe_stats.authorization_user_usage_summary_daily"):
			userUpdates++
		case strings.Contains(statement.query, "DELETE FROM juhe_stats.authorization_team_usage_summary_daily"):
			teamDeletes++
		case strings.Contains(statement.query, "DELETE FROM juhe_stats.authorization_user_usage_summary_daily"):
			userDeletes++
		}
	}
	// 3 filter × (2 team 键 + 4 user 键)。
	if teamUpdates != 6 || userUpdates != 12 || teamDeletes != 6 || userDeletes != 12 {
		t.Fatalf("team/user 语句数 = %d/%d/%d/%d", teamUpdates, userUpdates, teamDeletes, userDeletes)
	}
	// team UPDATE 尾参：owner, statDate, teamFilterID, filterType, filterID。
	// 首条为 (all, teamFilterID="")；teamFilterID 键集合 = {"", "team-9"}。
	teamFilterIDs := map[string]bool{}
	firstChecked := false
	for index := range statements {
		statement := statements[index]
		if !strings.Contains(statement.query, "UPDATE juhe_stats.authorization_team_usage_summary_daily") {
			continue
		}
		tail := []string{"owner-2", "2026-01-05", "", "all", ""}
		if !firstChecked {
			for position, want := range tail {
				value := fmt.Sprintf("%v", statement.args[len(statement.args)-len(tail)+position])
				if value != want {
					t.Fatalf("首条 team UPDATE 尾参[%d] = %q, 期望 %q", position, value, want)
				}
			}
			firstChecked = true
		}
		teamFilterIDs[fmt.Sprintf("%v", statement.args[len(statement.args)-3])] = true
	}
	if !teamFilterIDs[""] || !teamFilterIDs["team-9"] {
		t.Fatalf("team 过滤键 = %v", teamFilterIDs)
	}
}

// TestSubtractPostgresAccountQualityEntriesDirty：多账户脏标记多行 VALUES 与去重。
func TestSubtractPostgresAccountQualityEntriesDirty(t *testing.T) {
	rec := newPGRecorder()
	store := &RecordCleanupStore{Stats: openRecorderPG(rec)}
	tx, err := store.Stats.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	entries := []*postgresAggregatedAccountQualityEntry{
		{accountID: "acc-1", statMinute: "2026-01-05T11:04", requestCount: 1},
		{accountID: "acc-2", statMinute: "2026-01-05T11:04", requestCount: 1},
		{accountID: "", statMinute: "2026-01-05T11:04"},
	}
	if err := store.subtractPostgresAccountQualityEntries(context.Background(), tx, entries, kitUpdatedAt); err != nil {
		t.Fatalf("subtractPostgresAccountQualityEntries: %v", err)
	}
	var dirtyInsert *recordedStatement
	for index := range rec.statements {
		if strings.Contains(rec.statements[index].query, "INSERT INTO juhe_stats.account_quality_dirty_accounts") {
			dirtyInsert = &rec.statements[index]
		}
	}
	if dirtyInsert == nil {
		t.Fatalf("缺少脏账户 INSERT")
	}
	// flattenTriples：[id, firstDirty, updated, id, firstDirty, updated]。
	if got := fmt.Sprintf("%v", dirtyInsert.args); got != "[acc-1 "+kitUpdatedAt+" "+kitUpdatedAt+" acc-2 "+kitUpdatedAt+" "+kitUpdatedAt+"]" {
		t.Fatalf("dirty args = %v", dirtyInsert.args)
	}
}

// TestMarkPostgresDerivedWindowDirtyScopesAIPerf：account 日桶 →
// ai_performance 脏范围；空条目 → 无语句。
func TestMarkPostgresDerivedWindowDirtyScopesAIPerf(t *testing.T) {
	rec := newPGRecorder()
	store := &RecordCleanupStore{Stats: openRecorderPG(rec)}
	tx, err := store.Stats.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	bucket := usageStatsBucketDefs[2] // usage_stats_daily
	entries := []*postgresAggregatedTimeEntry{
		{bucket: bucket, timeValue: "2026-01-05", systemAccountID: "sys-1", scopeType: "account", scopeID: "acc-1"},
		{bucket: bucket, timeValue: "2026-01-06", systemAccountID: "sys-1", scopeType: "account", scopeID: "acc-1"},
		{bucket: bucket, timeValue: "2026-01-06", systemAccountID: "sys-1", scopeType: "api_key", scopeID: "key-1"},
	}
	if err := store.markPostgresDerivedWindowDirtyScopes(context.Background(), tx, entries, kitUpdatedAt); err != nil {
		t.Fatalf("markPostgresDerivedWindowDirtyScopes: %v", err)
	}
	statements := rec.all()
	aiFound := false
	for _, statement := range statements {
		if strings.Contains(statement.query, "ai_performance_summary_dirty_system_accounts") {
			aiFound = true
			// min/max 跨条目聚合：2026-01-05 / 2026-01-06。
			if fmt.Sprintf("%v", statement.args[1]) != "2026-01-05" || fmt.Sprintf("%v", statement.args[2]) != "2026-01-06" {
				t.Fatalf("ai 脏范围参数 = %v", statement.args)
			}
		}
	}
	if !aiFound {
		t.Fatalf("缺少 ai 脏范围 INSERT")
	}
	// 空条目 → 无语句。
	emptyRec := newPGRecorder()
	emptyStore := &RecordCleanupStore{Stats: openRecorderPG(emptyRec)}
	emptyTx, err := emptyStore.Stats.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	defer func() { _ = emptyTx.Rollback() }()
	if err := emptyStore.markPostgresDerivedWindowDirtyScopes(context.Background(), emptyTx, nil, kitUpdatedAt); err != nil {
		t.Fatalf("空条目: %v", err)
	}
	if len(emptyRec.all()) != 0 {
		t.Fatalf("空条目不应产生语句")
	}
}

// TestSubtractPostgresUsageStatsRowsGuards：空行集、非法 created_at、时区缺省。
func TestSubtractPostgresUsageStatsRowsGuards(t *testing.T) {
	rec := newPGRecorder()
	store := &RecordCleanupStore{Stats: openRecorderPG(rec)}
	tx, err := store.Stats.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := store.subtractPostgresUsageStatsRows(context.Background(), tx, nil, kitUpdatedAt, kitZone()); err != nil {
		t.Fatalf("空行集应直通：%v", err)
	}
	if len(rec.all()) != 0 {
		t.Fatalf("空行集不应产生语句")
	}
	bad := statsagg.UsageStatsRecordRow{ID: "r", SystemAccountID: "sys-1", CreatedAt: "bad"}
	if err := store.subtractPostgresUsageStatsRows(context.Background(), tx, []statsagg.UsageStatsRecordRow{bad}, kitUpdatedAt, kitZone()); err == nil {
		t.Fatalf("非法 created_at 应报错")
	}
	noZone := &RecordCleanupStore{Stats: openRecorderPG(rec)}
	if err := noZone.subtractPostgresUsageStatsRows(context.Background(), tx,
		[]statsagg.UsageStatsRecordRow{{ID: "r", SystemAccountID: "sys-1", CreatedAt: kitCreatedAt}}, kitUpdatedAt, nil); err == nil {
		t.Fatalf("时区缺省应报错")
	}
	if err := store.subtractPostgresUsageStatsRows(context.Background(), tx,
		[]statsagg.UsageStatsRecordRow{{ID: "r", SystemAccountID: "sys-1", CreatedAt: kitCreatedAt}}, "bad-time", kitZone()); err == nil {
		t.Fatalf("非法 updatedAt 应报错")
	}
}

// TestPostgresAuthorizationReportRows：lookup 覆盖 resource、group 行与去重。
func TestPostgresAuthorizationReportRows(t *testing.T) {
	row := statsagg.UsageStatsRecordRow{
		SystemAccountID: "sys-1",
		AccountID:       kitText("acc-1"), AccountAuthorizationID: kitText("auth-1"),
		AccountOwnerSystemAccountID: kitText("owner-2"),
		GroupID:                     kitText("grp-1"), GroupAuthorizationID: kitText("gauth-1"),
		GroupOwnerSystemAccountID: kitText("owner-3"),
	}
	lookup := &statsagg.AuthorizationLookup{AccountAuthorizationResourceIDs: map[string]string{"auth-1": "acc-real"}}
	rows := postgresAuthorizationReportRows(row, lookup)
	if len(rows) != 2 {
		t.Fatalf("rows = %+v", rows)
	}
	if rows[0].resourceID != "acc-real" || rows[0].resourceType != "account" {
		t.Fatalf("account 行 = %+v", rows[0])
	}
	if rows[1].resourceID != "grp-1" || rows[1].resourceType != "group" || rows[1].owner != "owner-3" {
		t.Fatalf("group 行 = %+v", rows[1])
	}
	// 授权归属即调用方（owner == caller）→ 不产生报表行。
	self := row
	self.AccountOwnerSystemAccountID = kitText("sys-1")
	self.GroupOwnerSystemAccountID = kitText("sys-1")
	if got := postgresAuthorizationReportRows(self, lookup); len(got) != 0 {
		t.Fatalf("自归属不应产生行：%+v", got)
	}
	// 相同 authorizationID 去重。
	dup := postgresAuthorizationReportRows(row, lookup)
	if len(dup) != 2 {
		t.Fatalf("去重失败：%+v", dup)
	}
}
