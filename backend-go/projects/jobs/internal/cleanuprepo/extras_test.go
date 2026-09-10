package cleanuprepo

import (
	"context"
	"database/sql/driver"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/retention"
)

// 剩余缺口的收尾测试：recordcleanuppostgres 错误路径（注入失败）、chat 的
// PG 租约/压缩恢复与缺省时钟、codex HasMore 与错误传播、usage catalog 错误
// 传播、stats 保留 PG schema。

func newKitPGRecordStore(rec *pgRecorder) *RecordCleanupStore {
	return &RecordCleanupStore{
		Stats:        openRecorderPG(rec),
		Dataset:      openRecorderPG(rec),
		UsageCatalog: openRecorderPG(rec),
		Business:     openRecorderPG(rec),
		Now:          kitNow,
		Timezone:     func(context.Context) (*time.Location, error) { return kitZone(), nil },
	}
}

// TestCleanupAPIKeyRelatedPostgresBatchError：事务内语句失败 → 目标标记
// last_error_message 并向上传播。
func TestCleanupAPIKeyRelatedPostgresBatchError(t *testing.T) {
	rec := newPGRecorder()
	store := &RecordCleanupStore{
		Stats:    openKitFailingRecorderPG(rec, "stats_job_state"),
		Dataset:  openRecorderPG(rec),
		Business: openRecorderPG(rec),
		Now:      kitNow,
	}
	if _, err := store.CleanupAPIKeyRelatedPostgres(context.Background(), "key-err", "sys-1"); err == nil {
		t.Fatalf("批次失败应报错")
	}
	statements := rec.all()
	last := statements[len(statements)-1]
	if !strings.Contains(last.query, "UPDATE juhe_dataset.api_key_record_cleanup_targets") ||
		!strings.Contains(last.query, "last_error_message = $2") {
		t.Fatalf("末条应为目标错误标记：%s", last.query)
	}
	if !strings.Contains(fmt.Sprintf("%v", last.args[1]), "stats_job_state") {
		t.Fatalf("错误信息 = %v", last.args)
	}
}

// TestCleanupAccountRelatedPostgresBatchError：account 变体同语义。
func TestCleanupAccountRelatedPostgresBatchError(t *testing.T) {
	rec := newPGRecorder()
	store := &RecordCleanupStore{
		Stats:    openKitFailingRecorderPG(rec, "usage_records"),
		Dataset:  openRecorderPG(rec),
		Business: openRecorderPG(rec),
		Now:      kitNow,
	}
	if _, err := store.CleanupAccountRelatedPostgres(context.Background(), retentionTarget()); err == nil {
		t.Fatalf("批次失败应报错")
	}
	last := rec.all()[len(rec.all())-1]
	if !strings.Contains(last.query, "UPDATE juhe_dataset.account_record_cleanup_targets") {
		t.Fatalf("末条应为目标错误标记：%s", last.query)
	}
}

// TestCleanupPendingAPIKeyTargetsPostgresFailure：失败目标计入 Failed 并标记。
func TestCleanupPendingAPIKeyTargetsPostgresFailure(t *testing.T) {
	rec := newPGRecorder()
	store := &RecordCleanupStore{
		Stats:    openKitFailingRecorderPG(rec, "stats_job_state"),
		Dataset:  openRecorderPG(rec),
		Business: openRecorderPG(rec),
		Now:      kitNow,
	}
	rec.script("juhe_dataset.api_key_record_cleanup_targets", []string{"api_key_id", "system_account_id"},
		[][]driver.Value{{"key-fail", "sys-1"}})
	summary, err := store.CleanupPendingAPIKeyTargetsPostgres(context.Background(), 10)
	if err != nil {
		t.Fatalf("CleanupPendingAPIKeyTargetsPostgres: %v", err)
	}
	if summary.Attempted != 1 || summary.Failed != 1 {
		t.Fatalf("summary = %+v", summary)
	}
}

// TestPostgresExistenceHelpers：hasPostgresAPIKey/Account 行命中与空集合。
func TestPostgresExistenceHelpers(t *testing.T) {
	rec := newPGRecorder()
	store := newKitPGRecordStore(rec)
	// 命中第一张 scope 表。
	rec.script("juhe_stats.usage_stats_totals", []string{"1"}, [][]driver.Value{{int64(1)}})
	hit, err := store.hasPostgresAPIKeyStatsRows(context.Background(), "key-1", "sys-1")
	if err != nil || !hit {
		t.Fatalf("api key stats 命中 = %v, %v", hit, err)
	}
	hit, err = store.hasPostgresAPIKeyUsageRecords(context.Background(), "key-1", "sys-1")
	if err != nil || hit {
		t.Fatalf("空录制库 usage = %v, %v", hit, err)
	}
	target := retention.ExpiredDeletedAccountTarget{AccountID: "acc-1", SystemAccountID: "sys-1"}
	hit, err = store.hasPostgresAccountUsageRecords(context.Background(), target)
	if err != nil || hit {
		t.Fatalf("空录制库 account usage = %v, %v", hit, err)
	}
	// 空目标 → false 且不发查询。
	empty := &pgRecorder{}
	emptyStore := newKitPGRecordStore(empty)
	hit, err = emptyStore.hasPostgresAccountUsageRecords(context.Background(), retention.ExpiredDeletedAccountTarget{})
	if err != nil || hit || len(empty.all()) != 0 {
		t.Fatalf("空目标 = %v, %v, %d", hit, err, len(empty.all()))
	}
	// 空事务标记 no-op。
	tx, err := store.Stats.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := store.markPostgresUsageCleanupRowsSubtracted(context.Background(), tx, nil, kitUpdatedAt); err != nil {
		t.Fatalf("空 usageIDs subtracted: %v", err)
	}
	if err := store.markPostgresUsageCleanupRowsDeleted(context.Background(), tx, nil, kitUpdatedAt); err != nil {
		t.Fatalf("空 usageIDs deleted: %v", err)
	}
}

// TestPinScheduledLeasePostgres：PG 租约校验链。
func TestPinScheduledLeasePostgres(t *testing.T) {
	rec := newPGRecorder()
	db := openRecorderPG(rec)
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	lease := &retention.ScheduledLeaseFence{LeaseKey: " ", OwnerID: "o", FencingToken: 1}
	if err := pinScheduledLease(context.Background(), db, tx, lease); err == nil || !strings.Contains(err.Error(), "leaseKey") {
		t.Fatalf("空 leaseKey 应报错：%v", err)
	}
	lease = &retention.ScheduledLeaseFence{LeaseKey: "k", OwnerID: " ", FencingToken: 1}
	if err := pinScheduledLease(context.Background(), db, tx, lease); err == nil || !strings.Contains(err.Error(), "ownerId") {
		t.Fatalf("空 ownerId 应报错：%v", err)
	}
	lease = &retention.ScheduledLeaseFence{LeaseKey: "k", OwnerID: "o", FencingToken: 7}
	// 无租约行 → 已失效。
	if err := pinScheduledLease(context.Background(), db, tx, lease); err == nil ||
		!strings.Contains(err.Error(), "租约已失效") {
		t.Fatalf("无租约应报错：%v", err)
	}
	// 命中租约行 → nil。
	rec.script("FROM juhe_stats.background_job_leases", []string{"lease_key"}, [][]driver.Value{{"k"}})
	if err := pinScheduledLease(context.Background(), db, tx, lease); err != nil {
		t.Fatalf("有效租约不应报错：%v", err)
	}
}

// TestChatRecoverStaleCompactionsPostgres：PG 行锁后缀与恢复语句。
func TestChatRecoverStaleCompactionsPostgres(t *testing.T) {
	rec := newPGRecorder()
	store := &ChatStore{DB: openRecorderPG(rec), Now: kitNow}
	rec.script("FROM juhe_chat.chat_conversations", []string{"id", "system_account_id"},
		[][]driver.Value{{"conv-1", "sys-1"}})
	recovered, err := store.recoverStaleCompactions(context.Background(), kitUpdatedAt, "2026-09-10T00:00:00.000Z", 500)
	if err != nil {
		t.Fatalf("recoverStaleCompactions: %v", err)
	}
	if recovered != 1 {
		t.Fatalf("recovered = %d", recovered)
	}
	statements := rec.all()
	if !strings.Contains(statements[0].query, "FOR UPDATE SKIP LOCKED") {
		t.Fatalf("PG 应带行锁后缀：%s", statements[0].query)
	}
	if !strings.Contains(statements[1].query, "context_state = 'compact_failed'") {
		t.Fatalf("缺少恢复 UPDATE")
	}
}

// TestChatNowDefaults：未注入时钟时的缺省路径。
func TestChatNowDefaults(t *testing.T) {
	store := &ChatStore{}
	if store.now().IsZero() {
		t.Fatalf("缺省时钟不应为零值")
	}
	if !okInstant(store.nowIso()) {
		t.Fatalf("nowIso 应产出可解析时间：%q", store.nowIso())
	}
	chatStore := &ChatStore{}
	if chatStore.now().IsZero() {
		t.Fatalf("chat 缺省时钟不应为零值")
	}
	codex := &CodexContextStore{}
	if codex.now().IsZero() {
		t.Fatalf("codex 缺省时钟不应为零值")
	}
	if !okInstant(codex.nowIso()) {
		t.Fatalf("codex nowIso 应产出可解析时间")
	}
	deleted := &DeletedAccountStore{}
	if deleted.now().IsZero() {
		t.Fatalf("deleted 缺省时钟不应为零值")
	}
}

func okInstant(value string) bool {
	_, ok := parseInstant(value)
	return ok
}

// TestExecChangedQError：执行失败透传（覆盖 execChangedQ 错误分支）。
func TestExecChangedQError(t *testing.T) {
	db := openKitSQLite(t, "exec_q_error")
	if _, err := execChangedQ(context.Background(), db, `INSERT INTO missing VALUES (1)`); err == nil {
		t.Fatalf("SQL 失败应透传")
	}
}

// TestReleaseAssetDeletionClaimDirect：成功释放、缺省错误码与超长截断。
func TestReleaseAssetDeletionClaimDirect(t *testing.T) {
	store, db := newKitChatStore(t)
	mustExecKit(t, db, `INSERT INTO chat_assets (
      id, system_account_id, cleanup_status, cleanup_claim_id, cleanup_attempt_count, expires_at, updated_at)
    VALUES ('asset-1','sys-1','claimed','claim-1',1,'2026-10-01T00:00:00.000Z','2026-09-01T00:00:00.000Z')`)
	released, releaseErr := store.releaseAssetDeletionClaim(context.Background(), "asset-1", "claim-1", "", kitUpdatedAt, kitUpdatedAt)
	if releaseErr != nil || !released {
		t.Fatalf("释放 = %v, %v", released, releaseErr)
	}
	var errorCode string
	if err := db.QueryRowContext(context.Background(), `SELECT cleanup_error_code FROM chat_assets WHERE id = 'asset-1'`).Scan(&errorCode); err != nil {
		t.Fatalf("read asset: %v", err)
	}
	if errorCode != "chat_asset_cleanup_failed" {
		t.Fatalf("缺省错误码 = %q", errorCode)
	}
	// 认领不匹配 → false。
	released, releaseErr = store.releaseAssetDeletionClaim(context.Background(), "asset-1", "claim-1", "x", kitUpdatedAt, kitUpdatedAt)
	if releaseErr != nil || released {
		t.Fatalf("认领不匹配应 false：%v, %v", released, releaseErr)
	}
	// 超长错误码截断到 120（先恢复 claimed 以命中更新）。
	mustExecKit(t, db, `UPDATE chat_assets SET cleanup_status = 'claimed', cleanup_claim_id = 'claim-2'
    WHERE id = 'asset-1'`)
	long := strings.Repeat("e", 200)
	released, releaseErr = store.releaseAssetDeletionClaim(context.Background(), "asset-1", "claim-2", long, kitUpdatedAt, kitUpdatedAt)
	if releaseErr != nil || !released {
		t.Fatalf("释放 = %v, %v", released, releaseErr)
	}
	if err := db.QueryRowContext(context.Background(), `SELECT cleanup_error_code FROM chat_assets WHERE id = 'asset-1'`).Scan(&errorCode); err != nil {
		t.Fatalf("read asset: %v", err)
	}
	if len(errorCode) != 120 || errorCode != long[:120] {
		t.Fatalf("截断错误码长度 = %d", len(errorCode))
	}
}

// TestCodexHasMoreAndErrorPaths：批满 HasMore、残句刷新分支与句柄错误传播。
func TestCodexHasMoreAndErrorPaths(t *testing.T) {
	store := newKitCodexStore(t, 1)
	seedKitCodexSession(t, store, 0, "s-1", "2026-09-01T00:00:00.000Z")
	seedKitCodexSession(t, store, 0, "s-2", "2026-09-02T00:00:00.000Z")
	result, err := store.CleanupExpiredStates(context.Background(), "2026-09-10T00:00:00.000Z", 1)
	if err != nil {
		t.Fatalf("CleanupExpiredStates: %v", err)
	}
	if result.DeletedSessions != 1 || !result.HasMore {
		t.Fatalf("result = %+v", result)
	}
	// limit 抬升 → 剩余会话可清。
	result, err = store.CleanupExpiredStates(context.Background(), "2026-09-10T00:00:00.000Z", 10)
	if err != nil || result.DeletedSessions != 1 || result.HasMore {
		t.Fatalf("second result = %+v, %v", result, err)
	}
	// 关闭分片句柄 → 错误传播。
	shardDB, err := store.shard(0)
	if err != nil {
		t.Fatalf("shard: %v", err)
	}
	if err := shardDB.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := store.CleanupExpiredStates(context.Background(), "2026-09-10T00:00:00.000Z", 1); err == nil {
		t.Fatalf("关闭句柄应报错")
	}
	// 关闭底层句柄但保留缓存 → 结算事务开启失败。
	failedSettle := newKitCodexStore(t, 1)
	cached, err := failedSettle.shard(0)
	if err != nil {
		t.Fatalf("shard: %v", err)
	}
	if err := cached.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := failedSettle.SettleStorageCleanup(context.Background(), Settlement{
		Failures: []SettlementFailure{{StorageKey: "k", Error: "x"}}, Now: kitUpdatedAt,
	}); err == nil {
		t.Fatalf("关闭句柄结算应报错")
	}
}

// TestUsageCatalogErrorPropagation：目录库句柄失败时的错误传播。
func TestUsageCatalogErrorPropagation(t *testing.T) {
	catalog := openKitSQLite(t, "catalog_errors")
	createKitUsageCatalogSchema(t, catalog.DB)
	store := newKitShardStore(t, t.TempDir())
	if err := catalog.DB.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := ListLocationsForApiKey(context.Background(), catalog, "k", "s", 1); err == nil {
		t.Fatalf("关闭句柄应报错")
	}
	if _, err := store.DeleteShardEntries(context.Background(), catalog, []string{"u"}); err == nil {
		t.Fatalf("关闭句柄应报错")
	}
	if _, err := store.CleanupEmptyShardFilesBefore(context.Background(), catalog, kitUpdatedAt, 1); err == nil {
		t.Fatalf("关闭句柄应报错")
	}
	records := &UsageRecordsStore{Catalog: catalog, Stats: catalog, Shards: store}
	if _, err := records.CleanupProcessedBefore(context.Background(), kitUpdatedAt, 1); err == nil {
		t.Fatalf("关闭句柄应报错")
	}
}

// TestNonBusinessDatasetTimezoneError：时区来源失败时显式报错。
func TestNonBusinessDatasetTimezoneError(t *testing.T) {
	f := newKitRecordFixture(t)
	store := &NonBusinessDatasetStore{
		Dataset:      f.dataset,
		UsageCatalog: f.catalog,
		Stats:        f.stats,
		UsageRecords: &UsageRecordsStore{Catalog: f.catalog, Stats: f.stats, Shards: f.shards},
		Timezone:     func(context.Context) (*time.Location, error) { return nil, context.DeadlineExceeded },
	}
	if _, err := store.CleanupBefore(context.Background(), kitUpdatedAt, 10); err == nil {
		t.Fatalf("时区失败应报错")
	}
}

// TestStatsRetentionPostgresSchema：PG schema 限定（juhe_stats）与批删链。
func TestStatsRetentionPostgresSchema(t *testing.T) {
	rec := newPGRecorder()
	store := &StatsRetentionStore{DB: openRecorderPG(rec)}
	counts, err := store.CleanupUsageStatsRetention(context.Background(), retention.UsageStatsRetentionInput{
		MinuteCutoffMinute: "2026-01-01T00:00", Limit: 5,
	})
	if err != nil {
		t.Fatalf("CleanupUsageStatsRetention: %v", err)
	}
	if counts.UsageStatsMinute != 1 {
		t.Fatalf("录制库 RowsAffected 恒 1，实际 %d", counts.UsageStatsMinute)
	}
	if got := len(rec.all()); got != len(usageStatsRetentionSpecs) {
		t.Fatalf("语句数 = %d, 期望 %d", got, len(usageStatsRetentionSpecs))
	}
	if !strings.Contains(rec.all()[0].query, "juhe_stats.account_quality_minute_stats") {
		t.Fatalf("首条应带 schema 限定：%s", rec.all()[0].query)
	}
}
