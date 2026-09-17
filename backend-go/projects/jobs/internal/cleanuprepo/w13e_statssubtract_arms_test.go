package cleanuprepo

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/retention"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/statsagg"
)

// w13e_statssubtract_arms_test.go 覆盖 statssubtract.go 剩余错误臂与数据驱动
// 分支：行扫描失败、扣减台账逐语句失败、结算入口 Commit/Begin 臂、
// deleteAccountScopeStatsRows 逐表失败、派生窗口刷新失败与快照 upsert 臂；
// 以及 hasRelatedRecordDataSQLite 的存在性真分支。

// TestW13eStatsSubtractRowScanArms：分片行扫描失败与 map 读取分支。
func TestW13eStatsSubtractRowScanArms(t *testing.T) {
	ctx := context.Background()
	// 列数不匹配 → scan 失败。
	{
		f := w13eFixture(t, "")
		f.addKitStageShard(t, "sk-1", "20260105", true)
		shard := w13eOpenDecoratedRawSQLite(t, shardFilePathForTest(f.root, "20260105", 1),
			w13eSQLiteOptions{rowsScripts: []w13eRowsScript{{match: "FROM usage_records", columns: []string{"id"},
				values: [][]driver.Value{{"x"}}, errAfterRows: -1}}})
		store := w13eKitPlainStore(t, f)
		if _, err := store.scanUsageRows(ctx, shard, "SELECT id FROM usage_records WHERE id IN (?)", "rec-1"); err == nil {
			t.Fatalf("scan 失败应透传")
		}
	}
	// map 读取：空字符串与空 []byte 回退 nil。
	{
		row, err := statsaggRowFromMap(map[string]any{"api_key_id": "", "status_code": []byte(""), "cost_usd": "1.5"})
		if err != nil {
			t.Fatalf("statsaggRowFromMap: %v", err)
		}
		if row.APIKeyID != nil || row.StatusCode != nil || row.CostUsd == nil || *row.CostUsd != 1.5 {
			t.Fatalf("map 读取回退语义错误: %+v", row)
		}
	}
	// latency bucket 上界哨兵（循环出口）。
	if got := latencyBucketUpperBound(math.Nextafter(60000, 100000)); got != -1 {
		t.Fatalf("超过最大上界应返回 -1: %d", got)
	}
}

// w13eKitStatsTx 在 seed stats 句柄上开事务并在断言后回滚。
func w13eKitStatsTx(t *testing.T, ctx context.Context, store *RecordCleanupStore) *sql.Tx {
	t.Helper()
	tx, err := store.Stats.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })
	return tx
}

// TestW13eSubtractUsageRecordArms：subtractUsageStatsRecord 各子扣减失败臂。
func TestW13eSubtractUsageRecordArms(t *testing.T) {
	ctx := context.Background()
	validRow := func() statsagg.UsageStatsRecordRow {
		return kitFailedRow()
	}
	// ShouldAggregate 拒绝（缺 access type）。
	{
		f := w13eFixture(t, "")
		store := w13eKitPlainStore(t, f)
		row := kitFailedRow()
		row.AccountAccessType = nil
		tx := w13eKitStatsTx(t, ctx, store)
		if err := store.subtractUsageStatsRecord(ctx, tx, row, kitUpdatedAt, kitZone()); err != nil {
			t.Fatalf("缺 access type 应跳过扣减: %v", err)
		}
	}
	// 非法 created_at → 时间键失败。
	{
		f := w13eFixture(t, "")
		store := w13eKitPlainStore(t, f)
		row := kitFailedRow()
		row.CreatedAt = "not-a-time"
		tx := w13eKitStatsTx(t, ctx, store)
		if err := store.subtractUsageStatsRecord(ctx, tx, row, kitUpdatedAt, kitZone()); err == nil {
			t.Fatalf("非法时间应报错")
		}
	}
	// 各子扣减语句失败。
	stages := []pgStage{
		{"model bucket delete", "AND thinking_tokens = 0 AND input_image_tokens = 0"},
		{"error bucket delete", "AND request_count = 0 AND error_count = 0"},
		{"quality update", "first_token_ms_sum = MAX(0, first_token_ms_sum - ?)"},
		{"dirty accounts", "INSERT INTO account_quality_dirty_accounts"},
	}
	runSQLiteStages(t, stages, func(t *testing.T, stage pgStage) error {
		f := w13eFixture(t, stage.failOn)
		store := &RecordCleanupStore{Stats: f.stats, Now: kitNow}
		tx := w13eKitStatsTx(t, ctx, store)
		return store.subtractUsageStatsRecord(ctx, tx, validRow(), kitUpdatedAt, kitZone())
	})
	// 成功路径（StatusCode 分支 + first token 分支 + quality/health 删除）。
	{
		f := w13eFixture(t, "")
		store := w13eKitPlainStore(t, f)
		tx := w13eKitStatsTx(t, ctx, store)
		if err := store.subtractUsageStatsRecord(ctx, tx, validRow(), kitUpdatedAt, kitZone()); err != nil {
			t.Fatalf("正常扣减不应报错: %v", err)
		}
	}
	// ErrorCode 为 nil 且 StatusCode 存在（错误码回退分支）。
	{
		f := w13eFixture(t, "")
		store := w13eKitPlainStore(t, f)
		row := validRow()
		row.ErrorCode = nil
		tx := w13eKitStatsTx(t, ctx, store)
		if err := store.subtractUsageStatsRecord(ctx, tx, row, kitUpdatedAt, kitZone()); err != nil {
			t.Fatalf("错误码回退扣减不应报错: %v", err)
		}
	}
}

// TestW13eSubtractOnceArms：subtractAPIKeyUsageRowsOnce / markUsageRowsDeleted 臂。
func TestW13eSubtractOnceArms(t *testing.T) {
	ctx := context.Background()
	rows := func() []statsagg.UsageStatsRecordRow {
		return []statsagg.UsageStatsRecordRow{kitFailedRow()}
	}
	// location nil 回退 UTC。
	{
		f := w13eFixture(t, "")
		store := w13eKitPlainStore(t, f)
		tx := w13eKitStatsTx(t, ctx, store)
		if err := store.subtractAPIKeyUsageRowsOnce(ctx, tx, rows(), "key-1", "sys-1", kitUpdatedAt, nil); err != nil {
			t.Fatalf("location nil 不应报错: %v", err)
		}
	}
	// 空 rows 直通。
	{
		f := w13eFixture(t, "")
		store := w13eKitPlainStore(t, f)
		tx := w13eKitStatsTx(t, ctx, store)
		if err := store.subtractAPIKeyUsageRowsOnce(ctx, tx, nil, "key-1", "sys-1", kitUpdatedAt, kitZone()); err != nil {
			t.Fatalf("空 rows 不应报错: %v", err)
		}
		if err := store.markUsageRowsDeleted(ctx, tx, nil, "key-1", "sys-1", kitUpdatedAt); err != nil {
			t.Fatalf("mark 空 rows 不应报错: %v", err)
		}
	}
	// 台账 INSERT 失败。
	{
		f := w13eFixture(t, "INSERT INTO usage_record_cleanup_deductions")
		store := &RecordCleanupStore{Stats: f.stats}
		tx := w13eKitStatsTx(t, ctx, store)
		if err := store.subtractAPIKeyUsageRowsOnce(ctx, tx, rows(), "key-1", "sys-1", kitUpdatedAt, kitZone()); err == nil {
			t.Fatalf("台账 INSERT 失败应透传")
		}
	}
	// 台账 SELECT 失败。
	{
		f := w13eFixture(t, "SELECT stats_subtracted_at")
		store := &RecordCleanupStore{Stats: f.stats}
		tx := w13eKitStatsTx(t, ctx, store)
		if err := store.subtractAPIKeyUsageRowsOnce(ctx, tx, rows(), "key-1", "sys-1", kitUpdatedAt, kitZone()); err == nil {
			t.Fatalf("台账 SELECT 失败应透传")
		}
	}
	// 已扣减行跳过。
	{
		f := w13eFixture(t, "")
		store := w13eKitPlainStore(t, f)
		mustExecKit(t, f.seedStats, `INSERT INTO usage_record_cleanup_deductions
      (usage_id, source_shard_key, stats_subtracted_at, created_at, updated_at)
      VALUES ('rec-kit-1', '', '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`)
		tx := w13eKitStatsTx(t, ctx, store)
		if err := store.subtractAPIKeyUsageRowsOnce(ctx, tx, rows(), "key-1", "sys-1", kitUpdatedAt, kitZone()); err != nil {
			t.Fatalf("已扣减行应跳过: %v", err)
		}
	}
	// mark 失败。
	{
		f := w13eFixture(t, "SET shard_deleted_at = COALESCE")
		store := &RecordCleanupStore{Stats: f.stats}
		tx := w13eKitStatsTx(t, ctx, store)
		if err := store.markUsageRowsDeleted(ctx, tx, rows(), "key-1", "sys-1", kitUpdatedAt); err == nil {
			t.Fatalf("mark 失败应透传")
		}
	}
	// jsonMarshal NaN 行失败。
	{
		f := w13eFixture(t, "")
		store := w13eKitPlainStore(t, f)
		tx := w13eKitStatsTx(t, ctx, store)
		row := kitFailedRow()
		row.CostUsd = func() *float64 { v := math.NaN(); return &v }()
		if err := store.subtractAPIKeyUsageRowsOnce(ctx, tx, []statsagg.UsageStatsRecordRow{row}, "key-1", "sys-1", kitUpdatedAt, kitZone()); err == nil {
			t.Fatalf("NaN 成本应导致 json 序列化失败")
		}
	}
}

// TestW13eCleanupRecordStatsEntryArms：结算入口的 Begin/subtract/mark/Commit 臂。
func TestW13eCleanupRecordStatsEntryArms(t *testing.T) {
	ctx := context.Background()
	target := retention.APIKeyCleanupTarget{APIKeyID: "key-1", SystemAccountID: "sys-1"}
	accountTarget := retention.ExpiredDeletedAccountTarget{AccountID: "acc-1", SystemAccountID: "sys-1"}
	rows := []map[string]any{{"id": "rec-kit-1", "system_account_id": "sys-1", "created_at": kitCreatedAt}}
	// Begin 失败（stats 句柄关闭）。
	{
		f := w13eFixture(t, "")
		store := w13eKitPlainStore(t, f)
		if err := store.Stats.Close(); err != nil {
			t.Fatalf("close: %v", err)
		}
		if err := store.CleanupAPIKeyRecordStatsData(ctx, target, rows, kitUpdatedAt, false, kitZone()); err == nil {
			t.Fatalf("api key 结算 Begin 失败应透传")
		}
		if err := store.CleanupAccountRecordStatsData(ctx, accountTarget, rows, kitUpdatedAt, false, kitZone()); err == nil {
			t.Fatalf("account 结算 Begin 失败应透传")
		}
		if err := store.UpsertAccountUsageSnapshots(ctx, f.seedDataset, []retention.AccountUsageSnapshotUpsertInput{{AccountID: "acc-1"}}); err == nil {
			t.Fatalf("快照 Begin 失败应透传")
		}
	}
	// subtract 失败。
	{
		f := w13eFixture(t, "INSERT INTO usage_record_cleanup_deductions")
		store := &RecordCleanupStore{Stats: f.stats, Now: kitNow}
		if err := store.CleanupAPIKeyRecordStatsData(ctx, target, rows, kitUpdatedAt, false, kitZone()); err == nil {
			t.Fatalf("api key 扣减失败应透传")
		}
		if err := store.CleanupAccountRecordStatsData(ctx, accountTarget, rows, kitUpdatedAt, false, kitZone()); err == nil {
			t.Fatalf("account 扣减失败应透传")
		}
	}
	// mark 失败（shardDeleted=true）。
	{
		f := w13eFixture(t, "SET shard_deleted_at = COALESCE")
		store := &RecordCleanupStore{Stats: f.stats, Now: kitNow}
		if err := store.CleanupAPIKeyRecordStatsData(ctx, target, rows, kitUpdatedAt, true, kitZone()); err == nil {
			t.Fatalf("api key mark 失败应透传")
		}
		if err := store.CleanupAccountRecordStatsData(ctx, accountTarget, rows, kitUpdatedAt, true, kitZone()); err == nil {
			t.Fatalf("account mark 失败应透传")
		}
	}
	// 空 rows + scope 删除失败。
	{
		f := w13eFixture(t, "scope_type IN ('account', 'caller_account')")
		store := &RecordCleanupStore{Stats: f.stats, Now: kitNow}
		if err := store.CleanupAccountRecordStatsData(ctx, accountTarget, nil, kitUpdatedAt, false, kitZone()); err == nil {
			t.Fatalf("account scope 删除失败应透传")
		}
	}
	// 空 rows + 派生窗口刷新失败。
	{
		f := w13eFixture(t, "")
		store := &RecordCleanupStore{
			Stats: f.stats, Now: kitNow,
			DerivedWindows: &kitDerivedWindows{err: errors.New("w13e derived 失败")},
		}
		if err := store.CleanupAPIKeyRecordStatsData(ctx, target, nil, kitUpdatedAt, false, kitZone()); err == nil ||
			!strings.Contains(err.Error(), "w13e derived 失败") {
			t.Fatalf("派生窗口失败应透传: %v", err)
		}
		if err := store.refreshDerivedWindows(ctx, "acc-1"); err == nil {
			t.Fatalf("refreshDerivedWindows 失败应透传")
		}
	}
	// 空 rows + Commit 失败。
	{
		f := w13eFixture(t, "")
		commitStats := w13eOpenDecoratedSQLite(t, f.dir+"/stats.sqlite3", w13eSQLiteOptions{failCommit: true})
		store := &RecordCleanupStore{Stats: commitStats, Now: kitNow}
		if err := store.CleanupAPIKeyRecordStatsData(ctx, target, nil, kitUpdatedAt, false, kitZone()); err == nil {
			t.Fatalf("空 rows Commit 失败应透传")
		}
		if err := store.CleanupAccountRecordStatsData(ctx, accountTarget, nil, kitUpdatedAt, false, kitZone()); err == nil {
			t.Fatalf("空 rows account Commit 失败应透传")
		}
	}
	// 有 rows 的成功提交（return tx.Commit() 路径）。
	{
		f := w13eFixture(t, "")
		store := w13eKitPlainStore(t, f)
		if err := store.CleanupAPIKeyRecordStatsData(ctx, target, rows, kitUpdatedAt, true, kitZone()); err != nil {
			t.Fatalf("api key 结算成功路径不应报错: %v", err)
		}
		if err := store.CleanupAccountRecordStatsData(ctx, accountTarget, rows, kitUpdatedAt, true, kitZone()); err != nil {
			t.Fatalf("account 结算成功路径不应报错: %v", err)
		}
	}
}

// TestW13eDeleteAccountScopeStatsArms：deleteAccountScopeStatsRows 逐语句失败。
func TestW13eDeleteAccountScopeStatsArms(t *testing.T) {
	ctx := context.Background()
	target := retention.ExpiredDeletedAccountTarget{
		AccountID: "acc-1", SystemAccountID: "sys-1",
		AuthorizationIDs: []string{"auth-1"}, TeamScopeIDs: []string{"acc-1:team-9"},
	}
	stages := []pgStage{
		{"scope stats account", "scope_type IN ('account', 'caller_account')"},
		{"scope stats team like", "scope_type = 'account_authorization_team' AND scope_id LIKE"},
		{"scope stats auth chunk", "scope_type = 'account_authorization' AND scope_id IN"},
		{"scope stats team chunk", "scope_type = 'account_authorization_team' AND scope_id IN"},
		{"job state account", "DELETE FROM stats_job_state WHERE scope_type IN ('account', 'caller_account')"},
		{"quality scores", "DELETE FROM account_quality_scores"},
		{"quality dirty", "DELETE FROM account_quality_dirty_accounts"},
		{"quality minute", "DELETE FROM account_quality_minute_stats"},
		{"health hourly", "DELETE FROM account_health_hourly"},
		{"usage snapshots", "DELETE FROM account_usage_snapshots"},
		{"deductions", "DELETE FROM usage_record_cleanup_deductions"},
		{"auth report", "resource_filter_type = 'account' AND resource_filter_id"},
		{"job state auth", "DELETE FROM stats_job_state WHERE scope_type = 'account_authorization'"},
		{"job state team", "DELETE FROM stats_job_state WHERE scope_type = 'account_authorization_team'"},
	}
	runSQLiteStages(t, stages, func(t *testing.T, stage pgStage) error {
		f := w13eFixture(t, stage.failOn)
		store := &RecordCleanupStore{Stats: f.stats}
		tx, err := store.Stats.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		err = store.deleteAccountScopeStatsRows(ctx, tx, target)
		_ = tx.Rollback()
		return err
	})
}

// TestW13eUpsertSnapshotsArms：UpsertAccountUsageSnapshots 错误臂。
func TestW13eUpsertSnapshotsArms(t *testing.T) {
	ctx := context.Background()
	inputs := []retention.AccountUsageSnapshotUpsertInput{{AccountID: "acc-1", Snapshot: map[string]any{"k": "v"}}}
	// 归属查询失败。
	{
		f := w13eFixture(t, "SELECT system_account_id FROM")
		store := w13eKitPlainStore(t, f)
		business := w13eOpenDecoratedSQLite(t, f.dir+"/dataset.sqlite3", w13eSQLiteOptions{failOn: "SELECT system_account_id FROM"})
		if err := store.UpsertAccountUsageSnapshots(ctx, business, inputs); err == nil {
			t.Fatalf("归属查询失败应透传")
		}
	}
	// 账户不存在。
	{
		f := w13eFixture(t, "")
		store := w13eKitPlainStore(t, f)
		execKitSchema(t, f.seedDataset.DB, `CREATE TABLE IF NOT EXISTS accounts (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL)`)
		if err := store.UpsertAccountUsageSnapshots(ctx, f.seedDataset, inputs); err == nil ||
			!strings.Contains(err.Error(), "缺少账户归属") {
			t.Fatalf("缺归属应报错: %v", err)
		}
	}
	// updatedAt 非法。
	{
		f := w13eFixture(t, "")
		store := w13eKitPlainStore(t, f)
		execKitSchema(t, f.seedDataset.DB, `CREATE TABLE IF NOT EXISTS accounts (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL)`)
		mustExecKit(t, f.seedDataset, `INSERT INTO accounts (id, system_account_id) VALUES ('acc-1', 'sys-1')`)
		bad := []retention.AccountUsageSnapshotUpsertInput{{AccountID: "acc-1", UpdatedAt: "bad", Snapshot: map[string]any{}}}
		if err := store.UpsertAccountUsageSnapshots(ctx, f.seedDataset, bad); err == nil {
			t.Fatalf("非法 updatedAt 应报错")
		}
	}
	// 空 inputs 直通。
	{
		f := w13eFixture(t, "")
		store := w13eKitPlainStore(t, f)
		if err := store.UpsertAccountUsageSnapshots(ctx, f.seedDataset, nil); err != nil {
			t.Fatalf("空 inputs 不应报错: %v", err)
		}
	}
}

// TestW13eHasRelatedRecordDataSQLiteTrueArms：存在性检查的真分支。
func TestW13eHasRelatedRecordDataSQLiteTrueArms(t *testing.T) {
	ctx := context.Background()
	target := func() *cleanupTarget {
		return &cleanupTarget{
			AccountID: "acc-1", SystemAccountID: "sys-1",
			AuthorizationIDs: []string{"auth-1"}, TeamScopeIDs: []string{"acc-1:team-9"},
		}
	}
	cases := []struct {
		name string
		seed func(t *testing.T, f *kitSQLiteStageFixture)
	}{
		{"scope stats account", func(t *testing.T, f *kitSQLiteStageFixture) {
			seedKitTotals(t, f.seedStats, "sys-1", "account", "acc-1", 1, 1, 1)
		}},
		{"scope stats team like", func(t *testing.T, f *kitSQLiteStageFixture) {
			seedKitTotals(t, f.seedStats, "sys-1", "account_authorization_team", "acc-1:team-9", 1, 1, 1)
		}},
		{"job state account", func(t *testing.T, f *kitSQLiteStageFixture) {
			mustExecKit(t, f.seedStats, `INSERT INTO stats_job_state (scope_type, scope_id, job_name)
        VALUES ('account', 'acc-1', 'usage_stats_aggregation')`)
		}},
		{"quality scores", func(t *testing.T, f *kitSQLiteStageFixture) {
			mustExecKit(t, f.seedStats, `INSERT INTO account_quality_scores (account_id) VALUES ('acc-1')`)
		}},
		{"auth report table", func(t *testing.T, f *kitSQLiteStageFixture) {
			mustExecKit(t, f.seedStats, `INSERT INTO authorization_team_usage_summary_daily
        (system_account_id, stat_date, resource_filter_type, resource_filter_id)
        VALUES ('sys-1', '2026-01-01', 'account', 'acc-1')`)
		}},
		{"auth chunk", func(t *testing.T, f *kitSQLiteStageFixture) {
			seedKitTotals(t, f.seedStats, "sys-1", "account_authorization", "auth-1", 1, 1, 1)
		}},
		{"job state auth", func(t *testing.T, f *kitSQLiteStageFixture) {
			mustExecKit(t, f.seedStats, `INSERT INTO stats_job_state (scope_type, scope_id, job_name)
        VALUES ('account_authorization', 'auth-1', 'usage_stats_aggregation')`)
		}},
		{"team chunk", func(t *testing.T, f *kitSQLiteStageFixture) {
			seedKitTotals(t, f.seedStats, "sys-1", "account_authorization_team", "acc-1:team-9", 1, 1, 1)
		}},
		{"targets self", func(t *testing.T, f *kitSQLiteStageFixture) {
			mustExecKit(t, f.seedDataset, `INSERT INTO account_record_cleanup_targets
        (account_id, system_account_id) VALUES ('acc-1', 'sys-1')`)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := w13eFixture(t, "")
			store := w13eKitPlainStore(t, f)
			tc.seed(t, f)
			ok, err := store.hasRelatedRecordDataSQLite(ctx, target())
			if err != nil || !ok {
				t.Fatalf("应检测到关联数据: %v %v", ok, err)
			}
		})
	}
	// 无关联数据 → false。
	{
		f := w13eFixture(t, "")
		store := w13eKitPlainStore(t, f)
		ok, err := store.hasRelatedRecordDataSQLite(ctx, target())
		if err != nil || ok {
			t.Fatalf("无数据应返回 false: %v %v", ok, err)
		}
	}
	// usage records 定位失败。
	{
		f := w13eFixture(t, "usage_record_account_shards")
		store := w13eKitPlainStore(t, f)
		store.UsageCatalog = f.catalog
		if _, err := store.hasRelatedRecordDataSQLite(ctx, target()); err == nil {
			t.Fatalf("定位失败应透传")
		}
	}
}

// TestW13eTimezoneStatsRowBranches：时区归一化的剩余行级分支。
func TestW13eTimezoneStatsRowBranches(t *testing.T) {
	ctx := context.Background()
	// account quality 的 success 分支（FirstTokenMs>=0）。
	f := w13eFixture(t, "")
	store := w13eKitPlainStore(t, f)
	row := kitFailedRow()
	row.Success = 1
	row.FirstTokenMs = kitNum(120)
	tx := w13eKitStatsTx(t, ctx, store)
	if err := store.subtractAccountQualityMinuteStats(ctx, tx, row, kitUpdatedAt, kitZone()); err != nil {
		t.Fatalf("quality 扣减不应报错: %v", err)
	}
	_ = time.Now
}
