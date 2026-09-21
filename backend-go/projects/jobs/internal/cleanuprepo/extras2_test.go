package cleanuprepo

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/retention"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/statsagg"
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

// TestDeletedAccountInstanceCandidateRichGraph：实例候选（授权实例 + 团队
// source + manual source + grants join）完整构建与删除。
func TestDeletedAccountInstanceCandidateRichGraph(t *testing.T) {
	f := newKitRecordFixture(t)
	store, business := newKitDeletedAccountStore(t, f.store)
	seedKitDeletedAccount(t, business, "acc-live", "", "", "")
	seedKitDeletedAccount(t, business, "acc-inst", "2026-06-01T00:00:00.000Z", "auth-5", "acc-live")
	mustExecKit(t, business, `INSERT INTO resource_authorizations
    (id, resource_type, resource_id, grantee_system_account_id, resource_owner_system_account_id, status)
    VALUES ('auth-5','account','acc-live','sys-1','owner-2','active')`)
	mustExecKit(t, business, `INSERT INTO resource_authorization_sources
    (id, authorization_id, source_type, source_team_id, status)
    VALUES ('src-5','auth-5','manual','team-31','active')`)
	mustExecKit(t, business, `INSERT INTO resource_authorization_grants
    (id, resource_type, resource_id, resource_owner_system_account_id, grantee_type, grantee_system_account_id, status)
    VALUES ('g-5','account','acc-live','owner-2','system_account','sys-1','active')`)
	mustExecKit(t, business, `INSERT INTO group_accounts (id, account_id, account_authorization_id)
    VALUES ('ga-5', NULL, 'auth-5')`)

	summary, err := store.CleanupExpired(context.Background())
	if err != nil {
		t.Fatalf("CleanupExpired: %v", err)
	}
	if summary.Completed != 1 || summary.PhysicallyDeletedAccounts != 1 ||
		summary.PhysicallyDeletedAuthorizations != 1 || summary.PhysicallyDeletedGrants != 1 {
		t.Fatalf("summary = %+v", summary)
	}
}

// TestRecordCleanupSQLiteAccountErrorStages：account 清理链阶段注入。
func TestRecordCleanupSQLiteAccountErrorStages(t *testing.T) {
	stages := []pgStage{
		{"target upsert", "INSERT INTO account_record_cleanup_targets"},
		{"target mark", "UPDATE account_record_cleanup_targets"},
		{"target clear", "DELETE FROM account_record_cleanup_targets"},
	}
	runSQLiteStages(t, stages, func(t *testing.T, stage pgStage) error {
		f := newKitSQLiteStageFixture(t, stage.failOn)
		// sk-1 在 global floor 内（有记录可删）；"target mark" 阶段把 floor
		// 回拨到记录之前 → 行未覆盖 → deferred 路径走到 mark（带阻塞原因）。
		f.addKitStageShard(t, "sk-1", "20260105", true)
		if stage.name == "target mark" {
			mustExecKit(t, f.seedStats, `UPDATE stats_job_state
        SET cursor_created_at = '2026-01-05T00:00:00.000Z' WHERE scope_type = 'global'`)
		}
		store := kitStageStore(t, f)
		store.Shards.SetOpener(func(path string) (*sql.DB, error) {
			return openKitFailingSQLite(path, stage.failOn), nil
		})
		target := retention.ExpiredDeletedAccountTarget{AccountID: "acc-1", SystemAccountID: "sys-1"}
		_, err := store.CleanupAccountRelatedSQLite(context.Background(), target, &kitStatsWriter{})
		return err
	})
	// statsWriter 失败时的 markErr 路径（含 Pending 汇总）。
	g := newKitRecordFixture(t)
	g.addKitShard(t, "sk-3", "2026-01-07", 3, "key-1", "sys-1", "acc-1", true, []statsagg.UsageStatsRecordRow{
		kitUsageRecord("rec-3", "sys-1", "key-1", "acc-1", "2026-01-05T01:00:00.000Z"),
	})
	failingWriter := &kitStatsWriter{accountErr: errors.New("account 结算注入失败")}
	if err := g.store.upsertAccountTarget(context.Background(),
		retention.ExpiredDeletedAccountTarget{AccountID: "acc-1", SystemAccountID: "sys-1"}, kitUpdatedAt); err != nil {
		t.Fatalf("seed target: %v", err)
	}
	summary, err := g.store.CleanupPendingAccountTargets(context.Background(), 10, failingWriter)
	if err != nil {
		t.Fatalf("CleanupPendingAccountTargets: %v", err)
	}
	if summary.Failed != 1 {
		t.Fatalf("summary = %+v", summary)
	}
}

// TestMiscTrailingHelpers：收尾纯函数。
func TestMiscTrailingHelpers(t *testing.T) {
	if !strings.Contains(sqliteBusyBlockedReason("api key"), "api key") {
		t.Fatalf("sqliteBusyBlockedReason = %q", sqliteBusyBlockedReason("api key"))
	}
	if got := cleanupPendingReason(true, false); !strings.Contains(got, "已被统计安全游标覆盖") {
		t.Fatalf("cleanupPendingReason(true,false) = %q", got)
	}
}
