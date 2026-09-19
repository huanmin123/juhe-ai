package auditlog

// w16a 覆盖收尾第五批：PostgreSQL 门禁直调臂。复用 w10b 的 w1cover 夹具库
// 与负臂库创建模式（连接配置仅从 .local/project-resources/dev/env/shared.env
// 读取；不可达一律 t.Skip）。本文件只使用 w16a- 前缀的 owner/audit ID，并在
// 结束时清理本前缀的全部行；任何输出不得携带连接串或密码。

import (
	"context"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

const w16aPgOwner = "w16a-pg-direct-owner"

func w16aCleanupPgRows(t *testing.T, store Store) {
	t.Helper()
	implementation, ok := store.(*sqlStore)
	if !ok {
		t.Fatalf("store 不是 *sqlStore")
	}
	ctx, cancel := context.WithTimeout(context.Background(), w10bPgBudget)
	defer cancel()
	for _, statement := range []string{
		`DELETE FROM juhe_dataset.audit_payload_refs WHERE audit_log_id LIKE 'w16a-%' OR headers_blob_id LIKE 'blob:w16a%' OR body_blob_id LIKE 'blob:w16a%'`,
		`DELETE FROM juhe_dataset.audit_log_attempts WHERE audit_log_id LIKE 'w16a-%'`,
		`DELETE FROM juhe_dataset.audit_logs WHERE id LIKE 'w16a-%'`,
		`DELETE FROM juhe_dataset.audit_payload_blob_gc WHERE blob_id LIKE 'blob:w16a%'`,
		`DELETE FROM juhe_dataset.audit_error_groups WHERE first_event_id LIKE 'w16a-%' OR last_event_id LIKE 'w16a-%'`,
		`DELETE FROM juhe_dataset.audit_payload_blobs WHERE NOT EXISTS (SELECT 1 FROM juhe_dataset.audit_payload_refs WHERE headers_blob_id = juhe_dataset.audit_payload_blobs.id OR body_blob_id = juhe_dataset.audit_payload_blobs.id)`,
		`DELETE FROM juhe_dataset.audit_log_owner_leases WHERE owner_id LIKE 'w16a-%'`,
	} {
		if _, err := implementation.db.ExecContext(ctx, statement); err != nil {
			t.Logf("清理 w16a 行失败（不影响断言）: %v", err)
		}
	}
}

func TestW16aPgCanceledContextArms(t *testing.T) {
	store, _ := w10bOpenCoverStore(t)
	w16aCleanupPgRows(t, store)
	ctx, cancel := context.WithTimeout(context.Background(), w10bPgBudget)
	defer cancel()
	// 先用健康上下文建 schema，再切换取消上下文命中各错误臂。
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	canceled, cancelArm := context.WithCancel(context.Background())
	cancelArm()
	if _, _, err := store.AcquireOwnerLease(canceled, w16aPgOwner, time.Minute); err == nil {
		t.Fatal("取消上下文 acquire 必须失败")
	}
	if _, err := store.RenewOwnerLease(canceled, OwnerLease{OwnerID: w16aPgOwner, FenceToken: 1}, time.Minute); err == nil {
		t.Fatal("取消上下文 renew 必须失败")
	}
	if err := store.ReleaseOwnerLease(canceled, OwnerLease{OwnerID: w16aPgOwner, FenceToken: 1}); err == nil {
		t.Fatal("取消上下文 release 必须失败")
	}
}

func TestW16aPgFreshStoreReleaseEnsureSchemaArm(t *testing.T) {
	negURL := w10bCreateNegativeDatabaseNamed(t, "juhe_ai_sub2api_dev_w16aneg")
	store, err := OpenStore(Config{Mode: ModePostgres, PostgresURL: negURL, PostgresMaxOpenConns: 2, PostgresMaxIdleConns: 1, PayloadBlobDirectory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	// schemaReady=false 时取消上下文 → EnsureSchema 事务失败臂透传。
	if err := store.ReleaseOwnerLease(canceled, OwnerLease{OwnerID: w16aPgOwner, FenceToken: 1}); err == nil || !strings.Contains(err.Error(), "开始 F3 PostgreSQL schema 事务失败") {
		t.Fatalf("取消上下文 release 必须因 schema 失败: %v", err)
	}
}

func TestW16aPgDirectTxFaultArms(t *testing.T) {
	store, _ := w10bOpenCoverStore(t)
	w16aCleanupPgRows(t, store)
	ctx, cancel := context.WithTimeout(context.Background(), w10bPgBudget)
	defer cancel()
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	lease, acquired, err := store.AcquireOwnerLease(ctx, w16aPgOwner, time.Minute)
	if err != nil || !acquired {
		t.Fatalf("acquire lease: %t %v", acquired, err)
	}
	implementation := store.(*sqlStore)
	tx, err := implementation.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	canceled, cancelArm := context.WithCancel(context.Background())
	cancelArm()

	// PG 初始 fence 查询失败臂。
	if err := implementation.verifyLeaseTx(canceled, tx, lease); err == nil {
		t.Fatal("取消上下文 verifyLeaseTx 必须失败")
	}
	// PG commit fence 查询失败臂。
	if err := implementation.verifyLeaseBeforeCommit(canceled, tx, lease); err == nil {
		t.Fatal("取消上下文 verifyLeaseBeforeCommit 必须失败")
	}
	// PG audit-ID 生命周期锁失败臂（单个与批量）。
	if err := implementation.lockAuditLogLifecycleTx(canceled, tx, "w16a-audit"); err == nil {
		t.Fatal("取消上下文 lockAuditLogLifecycleTx 必须失败")
	}
	if err := implementation.lockAuditLogLifecycleIDs(canceled, tx, []string{"w16a-audit-a", "w16a-audit-b"}); err == nil {
		t.Fatal("取消上下文 lockAuditLogLifecycleIDs 必须失败")
	}
	// PG blob 生命周期锁失败臂（单个、批量传播、空/重复计划直通）。
	if err := implementation.lockBlobLifecycleTx(canceled, tx, "blob:w16a-direct"); err == nil {
		t.Fatal("取消上下文 lockBlobLifecycleTx 必须失败")
	}
	if err := implementation.lockBlobLifecyclePlans(canceled, tx, []blobPlan{{record: blobRecord{id: "blob:w16a-p"}}, {record: blobRecord{id: "blob:w16a-p"}}}); err == nil {
		t.Fatal("取消上下文 lockBlobLifecyclePlans 必须失败")
	}
	_ = tx.Rollback()

	// 提交前 fence 的 ErrNoRows 臂：租约过期后 commit fence 查不到行。
	expiredCtx, cancelExpired := context.WithTimeout(context.Background(), w10bPgBudget)
	defer cancelExpired()
	if _, err := implementation.db.ExecContext(expiredCtx, `UPDATE juhe_dataset.audit_log_owner_leases SET lease_until=clock_timestamp() - INTERVAL '1 millisecond' WHERE lease_key=$1 AND owner_id=$2`, "f3-audit-log-persistence", w16aPgOwner); err != nil {
		t.Fatal(err)
	}
	expiredTx, err := implementation.db.BeginTx(expiredCtx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = expiredTx.Rollback() }()
	if err := implementation.verifyLeaseBeforeCommit(expiredCtx, expiredTx, lease); err == nil {
		t.Fatal("过期租约的 commit fence 必须失败")
	}
}
