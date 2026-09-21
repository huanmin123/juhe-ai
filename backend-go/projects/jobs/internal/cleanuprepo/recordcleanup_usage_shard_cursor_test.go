package cleanuprepo

import (
	"context"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/retention"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/statsagg"
)

// recordcleanup 关联清理链放行门从 usage_shard 逐分片游标切到双聚合 global
// floor 的语义测试。取证背景：usage_shard 游标是 Node 时代 client-ip 逐分片
// 聚合的产物，Go 侧无生产写入方；修复前分片一旦没有 usage_shard 游标行，
// 该链对它零删除并永久 pending（探针实证 DeletedRows=0 + HasMore=true）。

// TestCleanupAPIKeyRelatedSQLiteDeletesWithGlobalFloorOnly：分片完全没有
// usage_shard 游标行（死亡游标的存量形态）、仅 global floor 存在时，覆盖行
// 应被删除、目标收敛；同分片内非目标 key 的行保留。
func TestCleanupAPIKeyRelatedSQLiteDeletesWithGlobalFloorOnly(t *testing.T) {
	f := newKitRecordFixture(t)
	ctx := context.Background()
	f.addKitShard(t, "sk-1", "2026-01-05", 1, "key-1", "sys-1", "acc-1", true, []statsagg.UsageStatsRecordRow{
		kitUsageRecord("rec-001", "sys-1", "key-1", "acc-1", "2026-01-05T01:00:00.000Z"),
		kitUsageRecord("rec-002", "sys-1", "key-2", "acc-2", "2026-01-05T02:00:00.000Z"),
	})
	// 非目标 key-2 的 scope catalog 注册（夹具只登记目标 key-1）。
	mustExecKit(t, f.catalog, `INSERT INTO usage_record_api_key_shards (api_key_id, system_account_id, shard_key)
      VALUES ('key-2', 'sys-1', 'sk-1')`)
	result, err := f.store.CleanupAPIKeyRelatedSQLite(ctx, "key-1", "sys-1", &kitStatsWriter{})
	if err != nil {
		t.Fatalf("CleanupAPIKeyRelatedSQLite: %v", err)
	}
	if result.DeletedRows != 1 || result.HasMore || result.BlockedReason != "" {
		t.Fatalf("result = %+v", result)
	}
	// 目标行：分片行、目录条目、scope catalog 全部收缩。
	if got := mustQueryCountKit(t, f.catalog, `SELECT COUNT(*) FROM usage_record_shard_entries`); got != 1 {
		t.Fatalf("目录条目 = %d, 期望保留 1（非目标 key-2）", got)
	}
	if got := mustQueryCountKit(t, f.dataset, `SELECT COUNT(*) FROM api_key_record_cleanup_targets`); got != 0 {
		t.Fatalf("完成目标应被清除")
	}
	// 非目标行保留：key-2 的分片行仍在（经目录条目间接验证 + 分片文件直查）。
	kept, err := f.store.hasAPIKeyUsageRecords(ctx, "key-2", "sys-1")
	if err != nil || !kept {
		t.Fatalf("key-2 行应保留: %v %v", kept, err)
	}
}

// TestCleanupAPIKeyRelatedSQLiteFloorAdvanceConvergence：floor 之外的行等待
// 聚合游标推进，重复执行按批收敛且幂等；重复执行不再重复扣减。
func TestCleanupAPIKeyRelatedSQLiteFloorAdvanceConvergence(t *testing.T) {
	f := newKitRecordFixture(t)
	ctx := context.Background()
	f.seedKitGlobalFloor(t, "2026-01-05T03:00:00.000Z", "rec-000")
	f.addKitShard(t, "sk-1", "2026-01-05", 1, "key-1", "sys-1", "acc-1", false, []statsagg.UsageStatsRecordRow{
		kitUsageRecord("rec-001", "sys-1", "key-1", "acc-1", "2026-01-05T01:00:00.000Z"),
		kitUsageRecord("rec-101", "sys-1", "key-1", "acc-1", "2026-01-06T01:00:00.000Z"),
	})
	writer := &kitStatsWriter{}
	// 第一轮：rec-001 在 floor 内删除，rec-101 在 floor 外 defer。
	result, err := f.store.CleanupAPIKeyRelatedSQLite(ctx, "key-1", "sys-1", writer)
	if err != nil {
		t.Fatalf("第一轮: %v", err)
	}
	if result.DeletedRows != 1 || !result.HasMore || !strings.Contains(result.BlockedReason, "尚未被统计聚合安全游标覆盖") {
		t.Fatalf("第一轮 result = %+v", result)
	}
	if got := mustQueryCountKit(t, f.dataset, `SELECT COUNT(*) FROM api_key_record_cleanup_targets`); got != 1 {
		t.Fatalf("defer 目标应保留")
	}
	// 聚合游标推进覆盖 rec-101。
	f.seedKitGlobalFloor(t, "2026-01-06T12:00:00.000Z", "rec-101")
	// 第二轮：删除剩余行并收敛清除目标。
	result, err = f.store.CleanupAPIKeyRelatedSQLite(ctx, "key-1", "sys-1", writer)
	if err != nil {
		t.Fatalf("第二轮: %v", err)
	}
	if result.DeletedRows != 1 || result.HasMore || result.BlockedReason != "" {
		t.Fatalf("第二轮 result = %+v", result)
	}
	if got := mustQueryCountKit(t, f.catalog, `SELECT COUNT(*) FROM usage_record_shard_entries`); got != 0 {
		t.Fatalf("目录条目应清空")
	}
	// 第三轮（幂等）：无行无目标，零删除直通。
	result, err = f.store.CleanupAPIKeyRelatedSQLite(ctx, "key-1", "sys-1", writer)
	if err != nil {
		t.Fatalf("第三轮: %v", err)
	}
	if result.DeletedRows != 0 || result.HasMore || result.BlockedReason != "" {
		t.Fatalf("第三轮 result = %+v", result)
	}
	// 结算幂等：三轮合计只对 rec-001 / rec-101 各结算一次（首轮扣减 + 次轮
	// ShardDeleted 标记 + 末轮 final stats），无重复行结算。
	writer.mu.Lock()
	defer writer.mu.Unlock()
	var settled []string
	for _, call := range writer.apiKeyCalls {
		if call.ShardDeleted && len(call.Rows) == 0 {
			continue // final stats 收尾调用
		}
		if !call.ShardDeleted {
			for _, row := range call.Rows {
				settled = append(settled, row["id"].(string))
			}
		}
	}
	if len(settled) != 2 || settled[0] != "rec-001" || settled[1] != "rec-101" {
		t.Fatalf("扣减结算行 = %v, 期望每行恰好一次", settled)
	}
}

// TestCleanupAccountRelatedSQLiteGlobalFloorSemantics：account 变体在 global
// floor 门下的行为——目标账户与关联账户行删除，非目标账户行保留，floor 外
// 行 defer。
func TestCleanupAccountRelatedSQLiteGlobalFloorSemantics(t *testing.T) {
	f := newKitRecordFixture(t)
	ctx := context.Background()
	f.seedKitGlobalFloor(t, "2026-01-05T03:00:00.000Z", "rec-000")
	// 同一分片：acc-1（目标）、acc-rel-1（关联）、acc-other（非目标）、
	// acc-1 的 floor 外行（defer）。
	f.addKitShard(t, "sk-1", "2026-01-05", 1, "key-1", "sys-1", "acc-1", false, []statsagg.UsageStatsRecordRow{
		kitUsageRecord("rec-001", "sys-1", "key-1", "acc-1", "2026-01-05T01:00:00.000Z"),
		kitUsageRecord("rec-002", "sys-1", "key-2", "acc-rel-1", "2026-01-05T02:00:00.000Z"),
		kitUsageRecord("rec-003", "sys-1", "key-3", "acc-other", "2026-01-05T02:30:00.000Z"),
		kitUsageRecord("rec-101", "sys-1", "key-1", "acc-1", "2026-01-06T01:00:00.000Z"),
	})
	// 关联账户与非目标账户的 scope catalog 注册（夹具只登记 acc-1）。
	for _, accountID := range []string{"acc-rel-1", "acc-other"} {
		mustExecKit(t, f.catalog, `INSERT INTO usage_record_account_shards (account_id, shard_key)
      VALUES (?, 'sk-1')`, accountID)
	}
	target := retention.ExpiredDeletedAccountTarget{
		AccountID: "acc-1", SystemAccountID: "sys-1", RelatedAccountIDs: []string{"acc-rel-1"},
	}
	result, err := f.store.CleanupAccountRelatedSQLite(ctx, target, &kitStatsWriter{})
	if err != nil {
		t.Fatalf("CleanupAccountRelatedSQLite: %v", err)
	}
	if result.DeletedRows != 2 || !result.HasMore || !strings.Contains(result.BlockedReason, "尚未被统计聚合安全游标覆盖") {
		t.Fatalf("result = %+v", result)
	}
	kept, err := f.store.hasAccountUsageRecords(ctx, []string{"acc-other"})
	if err != nil || !kept {
		t.Fatalf("非目标 acc-other 行应保留: %v %v", kept, err)
	}
	kept, err = f.store.hasAccountUsageRecords(ctx, []string{"acc-1"})
	if err != nil || !kept {
		t.Fatalf("floor 外的 acc-1 行应保留: %v %v", kept, err)
	}
	if got := mustQueryCountKit(t, f.dataset, `SELECT COUNT(*) FROM account_record_cleanup_targets`); got != 1 {
		t.Fatalf("defer 目标应保留")
	}
}

// TestRecordCleanupCoexistsWithDataRetentionGate：两条清理链共用双聚合
// global floor 且互不干扰——recordcleanup 消费分片行、dataretention 消费
// stats 镜像行，floor 缺失时双方都不删除，floor 推进后各自收敛。
func TestRecordCleanupCoexistsWithDataRetentionGate(t *testing.T) {
	f := newKitRecordFixture(t)
	ctx := context.Background()
	f.addKitShard(t, "sk-1", "2026-01-05", 1, "key-1", "sys-1", "acc-1", false, []statsagg.UsageStatsRecordRow{
		kitUsageRecord("rec-001", "sys-1", "key-1", "acc-1", "2026-01-05T01:00:00.000Z"),
		kitUsageRecord("rec-101", "sys-1", "key-1", "acc-1", "2026-01-06T01:00:00.000Z"),
	})
	// 镜像行照 usagewriter mirrorStatsUsageRecords 同批镜像。
	seedStatsMirrorRecord(t, f.stats, "rec-001", "2026-01-05T01:00:00.000Z")
	seedStatsMirrorRecord(t, f.stats, "rec-101", "2026-01-06T01:00:00.000Z")
	usageRecords := &UsageRecordsStore{Catalog: f.catalog, Stats: f.stats, Shards: f.shards}
	cutoff := "2026-09-10T00:00:00.000Z"

	// floor 缺失：两条链都不删除，各自显式上报阻塞。
	batch, err := usageRecords.CleanupProcessedBefore(ctx, cutoff, 10)
	if err != nil || batch.DeletedRows != 0 || !strings.Contains(batch.BlockedReason, "聚合安全游标尚未建立") {
		t.Fatalf("dataretention 无 floor 应阻塞: %+v %v", batch, err)
	}
	result, err := f.store.CleanupAPIKeyRelatedSQLite(ctx, "key-1", "sys-1", &kitStatsWriter{})
	if err != nil || result.DeletedRows != 0 || !result.HasMore {
		t.Fatalf("recordcleanup 无 floor 应阻塞: %+v %v", result, err)
	}

	// floor 覆盖 rec-001：recordcleanup 删除分片行（scope 过滤），dataretention
	// 的镜像半区删除对应镜像行；floor 外的 rec-101 双链都保留。
	f.seedKitGlobalFloor(t, "2026-01-05T03:00:00.000Z", "rec-000")
	result, err = f.store.CleanupAPIKeyRelatedSQLite(ctx, "key-1", "sys-1", &kitStatsWriter{})
	if err != nil || result.DeletedRows != 1 || !result.HasMore {
		t.Fatalf("recordcleanup 首批: %+v %v", result, err)
	}
	batch, err = usageRecords.CleanupProcessedBefore(ctx, cutoff, 10)
	if err != nil || batch.DeletedRows != 1 {
		t.Fatalf("dataretention 镜像半区应删 1 行: %+v %v", batch, err)
	}

	// floor 推进覆盖 rec-101：recordcleanup 收敛清目标；dataretention 清理
	// 剩余镜像行；重复执行双方均零删除（幂等）。
	f.seedKitGlobalFloor(t, "2026-01-06T12:00:00.000Z", "rec-101")
	result, err = f.store.CleanupAPIKeyRelatedSQLite(ctx, "key-1", "sys-1", &kitStatsWriter{})
	if err != nil || result.DeletedRows != 1 || result.HasMore {
		t.Fatalf("recordcleanup 收敛: %+v %v", result, err)
	}
	if got := mustQueryCountKit(t, f.dataset, `SELECT COUNT(*) FROM api_key_record_cleanup_targets`); got != 0 {
		t.Fatalf("目标应被清除")
	}
	batch, err = usageRecords.CleanupProcessedBefore(ctx, cutoff, 10)
	if err != nil || batch.DeletedRows != 1 {
		t.Fatalf("dataretention 收敛: %+v %v", batch, err)
	}
	batch, err = usageRecords.CleanupProcessedBefore(ctx, cutoff, 10)
	if err != nil || batch.DeletedRows != 0 || batch.HasMore {
		t.Fatalf("dataretention 重复执行应零删除: %+v %v", batch, err)
	}
	result, err = f.store.CleanupAPIKeyRelatedSQLite(ctx, "key-1", "sys-1", &kitStatsWriter{})
	if err != nil || result.DeletedRows != 0 || result.HasMore {
		t.Fatalf("recordcleanup 重复执行应零删除: %+v %v", result, err)
	}
}
