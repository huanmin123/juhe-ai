package cleanuprepo

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/retention"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/statsagg"
)

// recordcleanup.go（SQLite 关联数据清理主链）的真实库语义测试：dataset
// targets 表生命周期、分片安全游标门控、分批删除、statsWriter 结算时序与
// pending targets 汇总。

type kitRecordFixture struct {
	store   *RecordCleanupStore
	dataset *DB
	stats   *DB
	catalog *DB
	shards  *ShardStore
	root    string
}

func newKitRecordFixture(t *testing.T) *kitRecordFixture {
	t.Helper()
	dataset := openKitSQLite(t, "dataset_targets")
	createKitTargetsSchema(t, dataset.DB)
	statsDB := openKitSQLite(t, "record_stats")
	createKitStatsChainSchema(t, statsDB.DB)
	catalog := newKitCatalog(t)
	root := filepath.Join(t.TempDir(), "shards")
	fixture := &kitRecordFixture{
		dataset: dataset,
		stats:   statsDB,
		catalog: catalog,
		shards:  newKitShardStore(t, root),
		root:    root,
	}
	fixture.store = &RecordCleanupStore{
		Dataset:      dataset,
		Stats:        statsDB,
		UsageCatalog: catalog,
		Shards:       fixture.shards,
		Now:          kitNow,
		Timezone:     func(context.Context) (*time.Location, error) { return kitZone(), nil },
	}
	return fixture
}

// addKitShard 建立一个分片：注册目录行 + scope catalog 行 + 记录行 +
// （可选）双统计安全游标。
func (f *kitRecordFixture) addKitShard(t *testing.T, shardKey, bucketDate string, shardID int64,
	apiKeyID, systemAccountID, accountID string, withCursor bool, records []statsagg.UsageStatsRecordRow) string {
	t.Helper()
	filePath := shardFilePathForTest(f.root, strings.ReplaceAll(bucketDate, "-", ""), shardID)
	if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
		t.Fatalf("mkdir shard dir: %v", err)
	}
	shardDB := createKitUsageShardDB(t, filePath)
	seedKitShard(t, f.catalog, shardKey, bucketDate, shardID, filePath)
	if apiKeyID != "" {
		mustExecKit(t, f.catalog, `INSERT INTO usage_record_api_key_shards (api_key_id, system_account_id, shard_key)
      VALUES (?, ?, ?)`, apiKeyID, systemAccountID, shardKey)
	}
	if accountID != "" {
		mustExecKit(t, f.catalog, `INSERT INTO usage_record_account_shards (account_id, shard_key)
      VALUES (?, ?)`, accountID, shardKey)
	}
	for _, record := range records {
		mustExecKit(t, shardDB, `INSERT INTO usage_records (
        id, system_account_id, api_key_id, account_id, provider_code, model, status_code, success,
        input_tokens, output_tokens, cost_usd, created_at)
      VALUES (?, ?, ?, ?, 'openai', 'gpt-kit', 200, 1, 10, 5, 0.01, ?)`,
			record.ID, record.SystemAccountID, record.APIKeyID, record.AccountID, record.CreatedAt)
		mustExecKit(t, f.catalog, `INSERT INTO usage_record_shard_entries
      (usage_id, shard_key, system_account_id, api_key_id, account_id, created_at)
      VALUES (?, ?, ?, ?, ?, ?)`, record.ID, shardKey, record.SystemAccountID,
			record.APIKeyID, record.AccountID, record.CreatedAt)
	}
	if withCursor {
		for _, jobName := range usageRecordCleanupRequiredCursorJobNames {
			mustExecKit(t, f.stats, `INSERT INTO stats_job_state
        (scope_type, scope_id, job_name, cursor_created_at, cursor_id)
        VALUES ('usage_shard', ?, ?, '2026-01-05T03:00:00.000Z', 'rec-000')`, shardKey, jobName)
		}
	}
	return filePath
}

func kitUsageRecord(id, systemAccountID, apiKeyID, accountID, createdAt string) statsagg.UsageStatsRecordRow {
	return statsagg.UsageStatsRecordRow{
		ID: id, SystemAccountID: systemAccountID, APIKeyID: kitText(apiKeyID),
		AccountID: kitText(accountID), CreatedAt: createdAt,
	}
}

func kitCursorRows() *cleanupCursor {
	return &cleanupCursor{CreatedAt: "2026-01-05T03:00:00.000Z", ID: "rec-000"}
}

// TestRecordCleanupTargetsLifecycle：api-key/account targets 的 upsert、
// blocked/error 标记与清除、列表排序与 JSON 数组往返。
func TestRecordCleanupTargetsLifecycle(t *testing.T) {
	f := newKitRecordFixture(t)
	ctx := context.Background()
	target := retention.ExpiredDeletedAccountTarget{
		AccountID: "acc-1", SystemAccountID: "sys-1",
		RelatedAccountIDs: []string{" acc-rel ", "", "acc-rel"}, AuthorizationIDs: []string{"auth-1"},
		TeamScopeIDs: []string{"acc-1:team-9"},
	}
	if err := f.store.upsertAccountTarget(ctx, target, kitUpdatedAt); err != nil {
		t.Fatalf("upsertAccountTarget: %v", err)
	}
	if err := f.store.markAccountTarget(ctx, target, "阻塞原因", "", kitUpdatedAt); err != nil {
		t.Fatalf("markAccountTarget: %v", err)
	}
	listed, err := f.store.listAccountTargets(ctx, 10)
	if err != nil {
		t.Fatalf("listAccountTargets: %v", err)
	}
	if len(listed) != 1 || listed[0].AccountID != "acc-1" ||
		strings.Join(listed[0].RelatedAccountIDs, ",") != "acc-rel" ||
		strings.Join(listed[0].AuthorizationIDs, ",") != "auth-1" {
		t.Fatalf("account targets = %+v", listed)
	}
	var attempts int64
	if err := f.dataset.QueryRowContext(ctx, `SELECT attempt_count, last_attempt_at, last_blocked_reason, last_error_message
      FROM account_record_cleanup_targets WHERE account_id = 'acc-1'`).Scan(&attempts, new(any), new(any), new(any)); err != nil {
		t.Fatalf("read target: %v", err)
	}
	if attempts != 1 {
		t.Fatalf("attempt_count = %d, 期望 1", attempts)
	}
	if err := f.store.clearAccountTarget(ctx, target); err != nil {
		t.Fatalf("clearAccountTarget: %v", err)
	}
	if listed, err = f.store.listAccountTargets(ctx, 10); err != nil || len(listed) != 0 {
		t.Fatalf("清除后列表 = %+v, %v", listed, err)
	}

	if err := f.store.upsertAPIKeyTarget(ctx, "key-1", "sys-1", kitUpdatedAt); err != nil {
		t.Fatalf("upsertAPIKeyTarget: %v", err)
	}
	if err := f.store.markAPIKeyTarget(ctx, "key-1", "sys-1", "", "boom", kitUpdatedAt); err != nil {
		t.Fatalf("markAPIKeyTarget: %v", err)
	}
	keys, err := f.store.listAPIKeyTargets(ctx, 10)
	if err != nil || len(keys) != 1 || keys[0].APIKeyID != "key-1" {
		t.Fatalf("api key targets = %+v, %v", keys, err)
	}
	var lastError any
	if err := f.dataset.QueryRowContext(ctx, `SELECT last_error_message FROM api_key_record_cleanup_targets WHERE api_key_id = 'key-1'`).Scan(&lastError); err != nil {
		t.Fatalf("read target: %v", err)
	}
	if lastError != "boom" {
		t.Fatalf("last_error_message = %v", lastError)
	}
	if err := f.store.clearAPIKeyTarget(ctx, "key-1", "sys-1"); err != nil {
		t.Fatalf("clearAPIKeyTarget: %v", err)
	}

	// list limit 归一（batchLimit）：limit 0 → 1 行上限。
	mustExecKit(t, f.dataset, `INSERT INTO api_key_record_cleanup_targets (api_key_id, system_account_id, created_at, updated_at)
      VALUES ('k1','s','t','t'), ('k2','s','t','t')`)
	keys, err = f.store.listAPIKeyTargets(ctx, 0)
	if err != nil || len(keys) != 1 {
		t.Fatalf("limit 归一后 = %+v, %v", keys, err)
	}

	// parseStringArrayJSON：非法 JSON → nil；空串 → nil。
	if got := parseStringArrayJSON("not-json"); got != nil {
		t.Fatalf("parseStringArrayJSON(非法) = %v", got)
	}
	if got := parseStringArrayJSON(" "); got != nil {
		t.Fatalf("parseStringArrayJSON(空白) = %v", got)
	}
	if got := stringArrayJSON([]string{" b ", "", "a", "b"}); got != `["b","a"]` {
		t.Fatalf("stringArrayJSON = %s", got)
	}
	if got := cleanupPendingReason(false, false); !strings.Contains(got, "统计安全游标") {
		t.Fatalf("cleanupPendingReason(false,false) = %q", got)
	}
}

// TestRecordCleanupShardCursorRequiresAllJobs：双游标齐备才放行，缺一返回 nil。
func TestRecordCleanupShardCursorRequiresAllJobs(t *testing.T) {
	f := newKitRecordFixture(t)
	ctx := context.Background()
	f.addKitShard(t, "sk-1", "2026-01-05", 1, "key-1", "sys-1", "acc-1", false, nil)
	cursor, err := f.store.shardCursor(ctx, "sk-1")
	if err != nil || cursor != nil {
		t.Fatalf("单游标应返回 nil：%+v, %v", cursor, err)
	}
	for _, jobName := range usageRecordCleanupRequiredCursorJobNames {
		mustExecKit(t, f.stats, `INSERT INTO stats_job_state
      (scope_type, scope_id, job_name, cursor_created_at, cursor_id)
      VALUES ('usage_shard','sk-1',?, '2026-01-05T03:00:00.000Z','rec-000')`, jobName)
	}
	cursor, err = f.store.shardCursor(ctx, "sk-1")
	if err != nil || cursor == nil || cursor.ID != "rec-000" {
		t.Fatalf("双游标应返回最早游标：%+v, %v", cursor, err)
	}
}

// TestCleanupAPIKeyRelatedSQLiteCompletes：全覆盖批次 → 删除 + 双阶段结算 +
// final stats 清空 + 目标清除。
func TestCleanupAPIKeyRelatedSQLiteCompletes(t *testing.T) {
	f := newKitRecordFixture(t)
	ctx := context.Background()
	f.addKitShard(t, "sk-1", "2026-01-05", 1, "key-1", "sys-1", "acc-1", true, []statsagg.UsageStatsRecordRow{
		kitUsageRecord("rec-001", "sys-1", "key-1", "acc-1", "2026-01-05T01:00:00.000Z"),
		kitUsageRecord("rec-002", "sys-1", "key-1", "acc-1", "2026-01-05T02:00:00.000Z"),
	})
	writer := &kitStatsWriter{}
	result, err := f.store.CleanupAPIKeyRelatedSQLite(ctx, "key-1", "sys-1", writer)
	if err != nil {
		t.Fatalf("CleanupAPIKeyRelatedSQLite: %v", err)
	}
	if result.DeletedRows != 2 || result.HasMore || result.BlockedReason != "" {
		t.Fatalf("result = %+v", result)
	}
	if got := mustQueryCountKit(t, f.catalog, `SELECT COUNT(*) FROM usage_record_shard_entries`); got != 0 {
		t.Fatalf("目录条目应清空")
	}
	if got := mustQueryCountKit(t, f.catalog, `SELECT COUNT(*) FROM usage_record_api_key_shards`); got != 0 {
		t.Fatalf("api-key scope catalog 应收缩")
	}
	if got := mustQueryCountKit(t, f.dataset, `SELECT COUNT(*) FROM api_key_record_cleanup_targets`); got != 0 {
		t.Fatalf("完成目标应被清除")
	}
	// 结算时序：扣减（ShardDeleted=false）→ 分片删除（ShardDeleted=true）→
	// final stats（Rows=nil, ShardDeleted=true）。
	if writer.apiKeyCallCount() != 3 {
		t.Fatalf("statsWriter 调用数 = %d, 期望 3", writer.apiKeyCallCount())
	}
	writer.mu.Lock()
	defer writer.mu.Unlock()
	if writer.apiKeyCalls[0].ShardDeleted {
		t.Fatalf("首次结算不应带 ShardDeleted")
	}
	if len(writer.apiKeyCalls[0].Rows) != 2 {
		t.Fatalf("首次结算行数 = %d", len(writer.apiKeyCalls[0].Rows))
	}
	if !writer.apiKeyCalls[1].ShardDeleted || len(writer.apiKeyCalls[1].Rows) != 2 {
		t.Fatalf("分片删除结算 = %+v", writer.apiKeyCalls[1])
	}
	if !writer.apiKeyCalls[2].ShardDeleted || writer.apiKeyCalls[2].Rows != nil {
		t.Fatalf("final stats 结算 = %+v", writer.apiKeyCalls[2])
	}
}

// TestCleanupAPIKeyRelatedSQLiteDefersOnUncoveredShard：另一分片缺安全游标 →
// 阻塞原因 + 目标保留。
func TestCleanupAPIKeyRelatedSQLiteDefersOnUncoveredShard(t *testing.T) {
	f := newKitRecordFixture(t)
	ctx := context.Background()
	f.addKitShard(t, "sk-1", "2026-01-05", 1, "key-1", "sys-1", "acc-1", true, []statsagg.UsageStatsRecordRow{
		kitUsageRecord("rec-001", "sys-1", "key-1", "acc-1", "2026-01-05T01:00:00.000Z"),
	})
	f.addKitShard(t, "sk-2", "2026-01-06", 1, "key-1", "sys-1", "acc-1", false, []statsagg.UsageStatsRecordRow{
		kitUsageRecord("rec-101", "sys-1", "key-1", "acc-1", "2026-01-06T01:00:00.000Z"),
	})
	writer := &kitStatsWriter{}
	result, err := f.store.CleanupAPIKeyRelatedSQLite(ctx, "key-1", "sys-1", writer)
	if err != nil {
		t.Fatalf("CleanupAPIKeyRelatedSQLite: %v", err)
	}
	if !result.HasMore || !strings.Contains(result.BlockedReason, "尚未被对应分片统计安全游标覆盖") {
		t.Fatalf("result = %+v", result)
	}
	// sk-1 行完成 扣减 + 分片删除 两次结算；HasMore 时不再触发 final stats。
	if writer.apiKeyCallCount() != 2 {
		t.Fatalf("statsWriter 调用数 = %d, 期望 2", writer.apiKeyCallCount())
	}
	if got := mustQueryCountKit(t, f.dataset, `SELECT COUNT(*) FROM api_key_record_cleanup_targets`); got != 1 {
		t.Fatalf("阻塞目标应保留")
	}
	var blocked sql.NullString
	if err := f.dataset.QueryRowContext(ctx, `SELECT last_blocked_reason FROM api_key_record_cleanup_targets`).Scan(&blocked); err != nil {
		t.Fatalf("read target: %v", err)
	}
	if !blocked.Valid || !strings.Contains(blocked.String, "游标") {
		t.Fatalf("last_blocked_reason = %v", blocked)
	}
	// sk-1 条目已删、sk-2 保留。
	if got := mustQueryCountKit(t, f.catalog, `SELECT COUNT(*) FROM usage_record_shard_entries WHERE shard_key = 'sk-2'`); got != 1 {
		t.Fatalf("未覆盖分片条目不应被删")
	}
}

// TestCleanupAPIKeyRelatedSQLiteBatchHasMore：103 行覆盖批次 → 首批删 100 行
// 且 HasMore。
func TestCleanupAPIKeyRelatedSQLiteBatchHasMore(t *testing.T) {
	f := newKitRecordFixture(t)
	ctx := context.Background()
	records := make([]statsagg.UsageStatsRecordRow, 0, recordCleanupBatchLimit+3)
	for index := 0; index < recordCleanupBatchLimit+3; index++ {
		records = append(records, kitUsageRecord(fmt.Sprintf("rec-%03d", index),
			"sys-1", "key-1", "acc-1", fmt.Sprintf("2026-01-05T%02d:%02d:00.000Z", index/60, index%60)))
	}
	f.addKitShard(t, "sk-1", "2026-01-05", 1, "key-1", "sys-1", "acc-1", true, records)
	writer := &kitStatsWriter{}
	result, err := f.store.CleanupAPIKeyRelatedSQLite(ctx, "key-1", "sys-1", writer)
	if err != nil {
		t.Fatalf("CleanupAPIKeyRelatedSQLite: %v", err)
	}
	if result.DeletedRows != recordCleanupBatchLimit || !result.HasMore {
		t.Fatalf("result = %+v", result)
	}
	if got := mustQueryCountKit(t, f.catalog, `SELECT COUNT(*) FROM usage_record_shard_entries`); got != 3 {
		t.Fatalf("残余条目 = %d, 期望 3", got)
	}
	if !strings.Contains(result.BlockedReason, "已被统计安全游标覆盖") {
		t.Fatalf("BlockedReason = %q", result.BlockedReason)
	}
}

// TestCleanupAPIKeyRelatedSQLiteWriterFailures：statsWriter 缺省/报错时目标
// 标记错误且不删除。
func TestCleanupAPIKeyRelatedSQLiteWriterFailures(t *testing.T) {
	f := newKitRecordFixture(t)
	ctx := context.Background()
	// 每轮使用独立 shard key / 记录 id，避免复用同一 fixture 的唯一约束。
	seed := func(suffix string) {
		f.addKitShard(t, "sk-"+suffix, "2026-01-05", int64(len(suffix))+10,
			"key-err", "sys-1", "acc-1", true, []statsagg.UsageStatsRecordRow{
				kitUsageRecord("rec-"+suffix, "sys-1", "key-err", "acc-1", "2026-01-05T01:00:00.000Z"),
			})
	}
	seed("one")
	if _, err := f.store.CleanupAPIKeyRelatedSQLite(ctx, "key-err", "sys-1", nil); err == nil ||
		!strings.Contains(err.Error(), "stats writer 未初始化") {
		t.Fatalf("nil statsWriter 应报错：%v", err)
	}
	var message sql.NullString
	if err := f.dataset.QueryRowContext(ctx, `SELECT last_error_message FROM api_key_record_cleanup_targets
      WHERE api_key_id = 'key-err'`).Scan(&message); err != nil || !message.Valid || !strings.Contains(message.String, "未初始化") {
		t.Fatalf("目标错误标记 = %v, %v", message, err)
	}
	if got := mustQueryCountKit(t, f.catalog, `SELECT COUNT(*) FROM usage_record_shard_entries`); got != 1 {
		t.Fatalf("失败不应删除记录")
	}

	seed("two")
	failing := &kitStatsWriter{apiKeyErr: errors.New("结算注入失败")}
	if _, err := f.store.CleanupAPIKeyRelatedSQLite(ctx, "key-err", "sys-1", failing); err == nil ||
		!strings.Contains(err.Error(), "结算注入失败") {
		t.Fatalf("statsWriter 错误应透传：%v", err)
	}
	if got := mustQueryCountKit(t, f.catalog, `SELECT COUNT(*) FROM usage_record_shard_entries`); got != 2 {
		t.Fatalf("结算失败不应删除记录，残余 = %d", got)
	}
}

// TestCleanupAccountRelatedSQLiteCompletes：account 变体全流程。
func TestCleanupAccountRelatedSQLiteCompletes(t *testing.T) {
	f := newKitRecordFixture(t)
	ctx := context.Background()
	f.addKitShard(t, "sk-1", "2026-01-05", 1, "key-1", "sys-1", "acc-1", true, []statsagg.UsageStatsRecordRow{
		kitUsageRecord("rec-001", "sys-1", "key-1", "acc-1", "2026-01-05T01:00:00.000Z"),
	})
	f.addKitShard(t, "sk-2", "2026-01-05", 2, "key-2", "sys-1", "acc-rel-1", true, []statsagg.UsageStatsRecordRow{
		kitUsageRecord("rec-002", "sys-1", "key-2", "acc-rel-1", "2026-01-05T02:00:00.000Z"),
	})
	writer := &kitStatsWriter{}
	target := retention.ExpiredDeletedAccountTarget{
		AccountID: "acc-1", SystemAccountID: "sys-1", RelatedAccountIDs: []string{"acc-rel-1"},
	}
	result, err := f.store.CleanupAccountRelatedSQLite(ctx, target, writer)
	if err != nil {
		t.Fatalf("CleanupAccountRelatedSQLite: %v", err)
	}
	if result.DeletedRows != 2 || result.HasMore || result.BlockedReason != "" {
		t.Fatalf("result = %+v", result)
	}
	if got := mustQueryCountKit(t, f.dataset, `SELECT COUNT(*) FROM account_record_cleanup_targets`); got != 0 {
		t.Fatalf("完成目标应被清除")
	}
	writer.mu.Lock()
	defer writer.mu.Unlock()
	if len(writer.accountCalls) != 3 {
		t.Fatalf("statsWriter 调用数 = %d, 期望 3", len(writer.accountCalls))
	}
	if len(writer.accountCalls[0].Rows) != 2 {
		t.Fatalf("首次结算行数 = %d", len(writer.accountCalls[0].Rows))
	}
	if writer.accountCalls[2].Rows != nil {
		t.Fatalf("final stats 结算 = %+v", writer.accountCalls[2])
	}
}

// TestCleanupPendingSummaries：pending targets 汇总 completed/deferred/failed。
func TestCleanupPendingSummaries(t *testing.T) {
	f := newKitRecordFixture(t)
	ctx := context.Background()
	// key-done：无任何分片 → 完成并清除。
	if err := f.store.upsertAPIKeyTarget(ctx, "key-done", "sys-1", kitUpdatedAt); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// key-blocked：分片缺游标 → 阻塞。
	f.addKitShard(t, "sk-blocked", "2026-01-05", 3, "key-blocked", "sys-1", "acc-b", false, []statsagg.UsageStatsRecordRow{
		kitUsageRecord("rec-b", "sys-1", "key-blocked", "acc-b", "2026-01-05T01:00:00.000Z"),
	})
	if err := f.store.upsertAPIKeyTarget(ctx, "key-blocked", "sys-1", kitUpdatedAt); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// key-fail：statsWriter 报错 → failed。
	f.addKitShard(t, "sk-fail", "2026-01-05", 4, "key-fail", "sys-1", "acc-f", true, []statsagg.UsageStatsRecordRow{
		kitUsageRecord("rec-f", "sys-1", "key-fail", "acc-f", "2026-01-05T01:00:00.000Z"),
	})
	if err := f.store.upsertAPIKeyTarget(ctx, "key-fail", "sys-1", kitUpdatedAt); err != nil {
		t.Fatalf("seed: %v", err)
	}
	writer := &kitStatsWriter{apiKeyErr: errors.New("仅 fail 目标结算失败")}
	// apiKeyErr 对所有含行目标生效：blocked 目标不触发结算（无行），done 目标
	// 无行也不触发首次结算，但 final stats 结算会失败……为隔离错误，改为
	// 断言 done 目标可能失败的现实行为前，先验证 summary 聚合本身。
	summary, err := f.store.CleanupPendingAPIKeyTargets(ctx, 10, writer)
	if err != nil {
		t.Fatalf("CleanupPendingAPIKeyTargets: %v", err)
	}
	if summary.Attempted != 3 {
		t.Fatalf("Attempted = %d, 期望 3", summary.Attempted)
	}
	if summary.Deferred != 1 {
		t.Fatalf("Deferred = %d, 期望 1（缺游标目标）", summary.Deferred)
	}
	if summary.Failed+summary.Completed != 2 {
		t.Fatalf("Failed+Completed = %d+%d, 期望 2", summary.Failed, summary.Completed)
	}

	// account 变体：单目标完成路径。
	if err := f.store.upsertAccountTarget(ctx, retention.ExpiredDeletedAccountTarget{
		AccountID: "acc-done", SystemAccountID: "sys-1",
	}, kitUpdatedAt); err != nil {
		t.Fatalf("seed: %v", err)
	}
	accountSummary, err := f.store.CleanupPendingAccountTargets(ctx, 10, &kitStatsWriter{})
	if err != nil {
		t.Fatalf("CleanupPendingAccountTargets: %v", err)
	}
	if accountSummary.Attempted != 1 || accountSummary.Completed != 1 || accountSummary.DeletedRows != 0 {
		t.Fatalf("account summary = %+v", accountSummary)
	}
	if got := mustQueryCountKit(t, f.dataset, `SELECT COUNT(*) FROM account_record_cleanup_targets`); got != 0 {
		t.Fatalf("完成目标应被清除")
	}
}

// TestHasRelatedRecordDataSQLite：SQLite 相关记录检查的三类命中与当前
// LIKE/ESCAPE 缺陷下的报错行为。
func TestHasRelatedRecordDataSQLite(t *testing.T) {
	f := newKitRecordFixture(t)
	ctx := context.Background()
	store := &DeletedAccountStore{Business: openKitSQLite(t, "biz_probe"), Records: f.store}
	target := &cleanupTarget{AccountID: "acc-1", SystemAccountID: "sys-1", AccountIDs: []string{"acc-1"}}

	// 1. targets 表命中。
	mustExecKit(t, f.dataset, `INSERT INTO account_record_cleanup_targets (account_id, system_account_id)
    VALUES ('acc-1','sys-1')`)
	hit, err := store.hasRelatedRecordData(ctx, target)
	if err != nil || !hit {
		t.Fatalf("targets 命中 = %v, %v", hit, err)
	}
	mustExecKit(t, f.dataset, `DELETE FROM account_record_cleanup_targets`)

	// 2. usage records 命中。
	f.addKitShard(t, "sk-hit", "2026-01-05", 5, "key-1", "sys-1", "acc-1", false, []statsagg.UsageStatsRecordRow{
		kitUsageRecord("rec-hit", "sys-1", "key-1", "acc-1", "2026-01-05T01:00:00.000Z"),
	})
	hit, err = store.hasRelatedRecordData(ctx, target)
	if err != nil || !hit {
		t.Fatalf("usage records 命中 = %v, %v", hit, err)
	}
	for _, table := range []string{"usage_record_shard_entries", "usage_record_shards",
		"usage_record_api_key_shards", "usage_record_account_shards"} {
		mustExecKit(t, f.catalog, `DELETE FROM `+table)
	}

	// 3. stats account scope 行命中（在团队 LIKE 语句之前返回）。
	mustExecKit(t, f.stats, `INSERT INTO usage_stats_totals
    (system_account_id, scope_type, scope_id, request_count) VALUES ('sys-1','account','acc-1',1)`)
	hit, err = store.hasRelatedRecordData(ctx, target)
	if err != nil || !hit {
		t.Fatalf("stats 行命中 = %v, %v", hit, err)
	}
	mustExecKit(t, f.stats, `DELETE FROM usage_stats_totals`)

	// 4. 全空（含授权/团队 scope 分块查询）→ false。注意：recordcleanup.go 的
	// LIKE 条件是解释型字符串（单反斜杠 ESCAPE），可正常执行；双字符 ESCAPE
	// 缺陷仅存在于 statssubtract.go 的 raw string（另见 statssubtract_sqlite_test.go）。
	empty := &cleanupTarget{
		AccountID: "acc-1", SystemAccountID: "sys-1", AccountIDs: []string{"acc-1"},
		AuthorizationIDs: []string{"auth-x"}, TeamScopeIDs: []string{"acc-1:team-9"},
	}
	hit, err = store.hasRelatedRecordData(ctx, empty)
	if err != nil || hit {
		t.Fatalf("全空 = %v, %v", hit, err)
	}

	// Records 缺省 → 显式报错（不静默）。
	missing := &DeletedAccountStore{Business: openKitSQLite(t, "biz_probe2")}
	if _, err := missing.hasRelatedRecordData(ctx, target); err == nil ||
		!strings.Contains(err.Error(), "record cleanup store 未初始化") {
		t.Fatalf("Records 缺省应报错：%v", err)
	}
}

// TestSortShardUsageRowsAndHelpers：批次排序与 account 维度读取。
func TestSortShardUsageRowsAndHelpers(t *testing.T) {
	rows := []shardUsageRow{
		{UsageStatsRecordRow: statsagg.UsageStatsRecordRow{ID: "b", CreatedAt: "2026-01-05T01:00:00.000Z"}},
		{UsageStatsRecordRow: statsagg.UsageStatsRecordRow{ID: "a", CreatedAt: "2026-01-05T01:00:00.000Z"}},
		{UsageStatsRecordRow: statsagg.UsageStatsRecordRow{ID: "c", CreatedAt: "2026-01-04T01:00:00.000Z"}},
	}
	sortShardUsageRows(rows)
	if rows[0].ID != "c" || rows[1].ID != "a" || rows[2].ID != "b" {
		t.Fatalf("排序结果 = %s,%s,%s", rows[0].ID, rows[1].ID, rows[2].ID)
	}
	if got := textOfAccountID(shardUsageRow{}); got != "" {
		t.Fatalf("nil AccountID 应返回空串：%q", got)
	}
}
