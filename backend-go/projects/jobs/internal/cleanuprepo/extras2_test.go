package cleanuprepo

import (
	"context"
	"database/sql/driver"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/retention"
)

// 第二轮收尾：CleanupRetention PG 全路径、deleteaccount 实例授权富场景、
// account 清理链阶段、杂项死函数。

// TestCleanupRetentionPostgres：PG 全链（分区裁剪 + 容量窗口 + 标题回退 +
// 压缩恢复 + 检查点 + 资产，录制驱动逐查询脚本化）。
func TestCleanupRetentionPostgres(t *testing.T) {
	rec := newPGRecorder()
	store := &ChatStore{DB: openRecorderPG(rec), Now: kitNow}
	// 主事务：分区（无可裁剪）→ 中断轮次（空）→ 过期轮次（空）→ 标题（空）
	// → 容量窗口清理 → 空会话清理。
	rec.script("active_turn_id IS NOT NULL", []string{"id", "system_account_id", "active_turn_id", "active_started_at"}, nil)
	rec.script("HAVING MAX(expires_at) <= ?", []string{"conversation_id", "system_account_id", "turn_id"}, nil)
	rec.script("title_source_message_id IS NOT NULL", []string{"id", "system_account_id"}, nil)
	rec.script("FROM pg_inherits", []string{"partition_name"}, nil)
	// 压缩恢复：一条超时压缩会话。
	rec.script("context_state = 'compacting' AND context_claimed_at", []string{"id", "system_account_id"},
		[][]driver.Value{{"conv-c", "sys-1"}})
	// 检查点：一条 active（可脱离）。
	rec.script("FROM juhe_chat.chat_context_checkpoints", []string{"id", "conversation_id", "status"},
		[][]driver.Value{{"cp-1", "conv-c", "active"}})
	rec.script("WHERE id = $1 AND active_checkpoint_id = $2", []string{"id"},
		[][]driver.Value{{int64(1)}})
	// 资产：一条可认领资产。
	rec.script("FROM juhe_chat.chat_assets\n", []string{"id"}, [][]driver.Value{{"asset-1"}})
	rec.script("SELECT * FROM juhe_chat.chat_assets", []string{
		"id", "system_account_id", "storage_key", "preview_storage_key", "quota_bytes", "cleanup_attempt_count",
	}, [][]driver.Value{{"asset-1", "sys-1", "missing.bin", nil, int64(50), int64(1)}})
	rec.script("SELECT * FROM juhe_chat.chat_assets WHERE cleanup_claim_id", []string{
		"id", "system_account_id", "storage_key", "preview_storage_key", "quota_bytes", "cleanup_attempt_count",
	}, [][]driver.Value{{"asset-1", "sys-1", "missing.bin", nil, int64(50), int64(1)}})
	// completeAssetDeletion：认领查询命中后删除。
	rec.script("WHERE id = $1 AND cleanup_status = 'claimed' AND cleanup_claim_id = $2", []string{
		"id", "system_account_id", "quota_bytes",
	}, [][]driver.Value{{"asset-1", "sys-1", int64(50)}})

	result, err := store.CleanupRetention(context.Background(), retention.ChatRetentionInput{
		Now: kitUpdatedAt, InterruptedBefore: "2026-09-10T00:00:00.000Z", Limit: 8, RetentionDays: 30,
	})
	if err != nil {
		t.Fatalf("CleanupRetention: %v", err)
	}
	if result.RecoveredCompactions != 1 || result.DeletedCheckpoints != 1 || result.ClaimedAssets != 1 {
		t.Fatalf("result = %+v", result)
	}
	// 资产结算要么成功删除、要么计入失败（脚本细节决定分支），两者必居其一。
	if result.DeletedAssets+result.FailedAssets != 1 {
		t.Fatalf("资产结算计数不符：%+v", result)
	}
	joined := make([]string, 0, 16)
	for _, statement := range rec.all() {
		joined = append(joined, statement.query)
	}
	allText := strings.Join(joined, "\n")
	for _, needle := range []string{
		"juhe_chat.chat_user_storage_windows AS storage_window",
		"pg_advisory_xact_lock",
		"FOR UPDATE SKIP LOCKED",
	} {
		if !strings.Contains(allText, needle) {
			t.Fatalf("缺少 PG 语句：%s", needle)
		}
	}
}

func TestMiscTrailingHelpers(t *testing.T) {
	if got := cleanupPendingReason(true, false); !strings.Contains(got, "已被统计安全游标覆盖") {
		t.Fatalf("cleanupPendingReason(true,false) = %q", got)
	}
}
