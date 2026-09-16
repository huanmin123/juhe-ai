package auditlog

// w10b PostgreSQL gated coverage: real dev-PG contract for the F3 audit
// store's Postgres arms (OpenStore pool branch, EnsureSchema, owner lease
// acquire/renew/release, Persist full chain with lifecycle locks, retention
// with scheduled blob GC, reactivate-on-republish) plus negative-schema and
// canceled-context arms.
//
// 隔离铁律（与 accountbalance w7c 先例一致）：
//   - 只触碰临时子库 juhe_ai_sub2api_dev_w1cover；主库一个字节不动。
//   - 连接配置只从 .local/project-resources/dev/env/shared.env 读取；任何
//     输出不得携带连接串或密码。
//   - env 缺失或 PG 不可达一律 t.Skip；审计行只使用 w10b- 前缀，负臂库
//     juhe_ai_sub2api_dev_w10cneg 由本测试创建并强制 DROP，绝不 DROP 共享对象。

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

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/pgpool"
	_ "github.com/jackc/pgx/v5/stdlib"
)

const (
	w10bCoverDB   = "juhe_ai_sub2api_dev_w1cover"
	w10bNegDB     = "juhe_ai_sub2api_dev_w10cneg"
	w10bEnvPath   = "../../../../../.local/project-resources/dev/env/shared.env"
	w10bOwnerID   = "w10b-pg-owner"
	w10bPgBudget  = 30 * time.Second
	w10bLeaseKey  = "f3-audit-log-persistence"
	w10bErrorCT   = "application/octet-stream"
	w10bErrorBody = "w10b canonical body"
	w10bArmBBody  = "w10b canonical arm-b body"
	w10bArmCBody  = "w10b canonical arm-c body"
)

func w10bSharedEnv(t *testing.T) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(w10bEnvPath)
	if err != nil {
		t.Skipf("dev env 不可达（跳过 PG 门禁测试）: %v", err)
	}
	values := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		values[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	return values
}

// w10bCoverPostgresURL ensures the temp cover database exists, then returns an
// application connection URL. The audit store creates its own juhe_dataset
// schema inside EnsureSchema.
func w10bCoverPostgresURL(t *testing.T) string {
	t.Helper()
	env := w10bSharedEnv(t)
	host := env["DEV_POSTGRES_HOST"]
	port := env["DEV_POSTGRES_DIRECT_PORT"]
	adminUser := env["DEV_POSTGRES_ADMIN_USERNAME"]
	adminPass := env["DEV_POSTGRES_ADMIN_PASSWORD"]
	appURL := env["JUHE_AI_POSTGRES_URL"]
	if host == "" || port == "" || adminUser == "" || appURL == "" {
		t.Skipf("dev env 缺少 PG 键（跳过）")
	}
	admin, err := sql.Open("pgx", fmt.Sprintf("postgres://%s:%s@%s:%s/postgres?sslmode=disable", adminUser, adminPass, host, port))
	if err != nil {
		t.Fatalf("打开 dev PG 管理连接失败: %v", err)
	}
	defer admin.Close()
	ctx, cancel := context.WithTimeout(context.Background(), w10bPgBudget)
	defer cancel()
	if err := admin.PingContext(ctx); err != nil {
		t.Skipf("dev PG 管理口不可达（跳过）: %v", err)
	}
	var exists bool
	if err := admin.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)`, w10bCoverDB).Scan(&exists); err != nil {
		t.Fatalf("查询临时子库失败: %v", err)
	}
	if !exists {
		owner := env["DEV_POSTGRES_APP_USERNAME"]
		if owner == "" {
			owner = adminUser
		}
		if _, err := admin.ExecContext(ctx, fmt.Sprintf(`CREATE DATABASE %s OWNER %s`, w10bCoverDB, owner)); err != nil {
			t.Fatalf("创建临时子库失败: %v", err)
		}
	}
	sep := strings.LastIndex(appURL, "/")
	if sep < 0 {
		t.Skipf("dev env JUHE_AI_POSTGRES_URL 形态异常（跳过）")
	}
	return appURL[:sep+1] + w10bCoverDB
}

// w10bCreateNegativeDatabase creates a short-lived dedicated database (created
// and force-dropped by this test only) for canceled-context and schema-error
// arms.
func w10bCreateNegativeDatabase(t *testing.T) string {
	t.Helper()
	env := w10bSharedEnv(t)
	host := env["DEV_POSTGRES_HOST"]
	port := env["DEV_POSTGRES_DIRECT_PORT"]
	adminUser := env["DEV_POSTGRES_ADMIN_USERNAME"]
	adminPass := env["DEV_POSTGRES_ADMIN_PASSWORD"]
	appURL := env["JUHE_AI_POSTGRES_URL"]
	if host == "" || port == "" || adminUser == "" || appURL == "" {
		t.Skipf("dev env 缺少 PG 键（跳过）")
	}
	admin, err := sql.Open("pgx", fmt.Sprintf("postgres://%s:%s@%s:%s/postgres?sslmode=disable", adminUser, adminPass, host, port))
	if err != nil {
		t.Fatalf("打开 dev PG 管理连接失败: %v", err)
	}
	defer admin.Close()
	ctx, cancel := context.WithTimeout(context.Background(), w10bPgBudget)
	defer cancel()
	if err := admin.PingContext(ctx); err != nil {
		t.Skipf("dev PG 管理口不可达（跳过）: %v", err)
	}
	if _, err := admin.ExecContext(ctx, `DROP DATABASE IF EXISTS `+w10bNegDB+` WITH (FORCE)`); err != nil {
		t.Fatalf("清理负臂临时库失败: %v", err)
	}
	owner := env["DEV_POSTGRES_APP_USERNAME"]
	if owner == "" {
		owner = adminUser
	}
	if _, err := admin.ExecContext(ctx, `CREATE DATABASE `+w10bNegDB+` OWNER `+owner); err != nil {
		t.Fatalf("创建负臂临时库失败: %v", err)
	}
	t.Cleanup(func() {
		dropCtx, dropCancel := context.WithTimeout(context.Background(), w10bPgBudget)
		defer dropCancel()
		_, _ = admin.ExecContext(dropCtx, `DROP DATABASE IF EXISTS `+w10bNegDB+` WITH (FORCE)`)
	})
	sep := strings.LastIndex(appURL, "/")
	if sep < 0 {
		t.Skipf("dev env JUHE_AI_POSTGRES_URL 形态异常（跳过）")
	}
	return appURL[:sep+1] + w10bNegDB
}

func w10bOpenCoverStore(t *testing.T) (Store, string) {
	t.Helper()
	blobDir := t.TempDir()
	store, err := OpenStore(Config{Mode: ModePostgres, PostgresURL: w10bCoverPostgresURL(t), PostgresMaxOpenConns: 4, PostgresMaxIdleConns: 2, PayloadBlobDirectory: blobDir})
	if err != nil {
		t.Fatalf("OpenStore PG: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, blobDir
}

func w10bCleanupAuditRows(t *testing.T, store Store) {
	t.Helper()
	implementation, ok := store.(*sqlStore)
	if !ok {
		t.Fatalf("store 不是 *sqlStore")
	}
	statements := []string{
		`DELETE FROM juhe_dataset.audit_payload_refs WHERE audit_log_id LIKE 'w10b-%'`,
		`DELETE FROM juhe_dataset.audit_log_attempts WHERE audit_log_id LIKE 'w10b-%'`,
		`DELETE FROM juhe_dataset.audit_logs WHERE id LIKE 'w10b-%'`,
		`DELETE FROM juhe_dataset.audit_payload_blob_gc WHERE blob_id LIKE 'blob:w10b%'`,
		`DELETE FROM juhe_dataset.audit_error_groups WHERE first_event_id LIKE 'w10b-%' OR last_event_id LIKE 'w10b-%'`,
		`DELETE FROM juhe_dataset.audit_log_owner_leases WHERE owner_id LIKE 'w10b-%'`,
		// 无引用 blob 行一并清理，避免下一个测试看到"有行无文件"的 canonical 记录。
		`DELETE FROM juhe_dataset.audit_payload_blobs WHERE NOT EXISTS (SELECT 1 FROM juhe_dataset.audit_payload_refs WHERE headers_blob_id = juhe_dataset.audit_payload_blobs.id OR body_blob_id = juhe_dataset.audit_payload_blobs.id)`,
	}
	run := func() {
		ctx, cancel := context.WithTimeout(context.Background(), w10bPgBudget)
		defer cancel()
		for _, statement := range statements {
			if _, err := implementation.db.ExecContext(ctx, statement); err != nil {
				t.Logf("清理临时行失败（不影响断言）: %v", err)
			}
		}
	}
	// 测试开始先清理上一轮残留，结束时再清一次。
	run()
	t.Cleanup(run)
}

func w10bErrorFixture(id string, lifecycle LifecycleStatus, createdAt, body string) AuditLogInput {
	input := fixture(id, lifecycle)
	input.AuditOutcome = AuditOutcomeGatewayFailed
	input.Success = false
	input.CreatedAt = createdAt
	input.HTTPCompletedAt = createdAt
	attempt := AuditLogAttemptInput{AttemptIndex: 0, ProviderCode: "openai", UpstreamMethod: "POST", UpstreamURL: "https://w10b.invalid/v1", StartedAt: createdAt, EndedAt: createdAt, Success: boolPtr(false), UpstreamStatusCode: intPointer(502), ErrorPhase: "upstream", ErrorCode: "w10b_code", ErrorMessage: "w10b message 1234 deadbeef"}
	input.Attempts = []AuditLogAttemptInput{attempt}
	payload := AuditLogPayloadInput{PartType: PayloadPartClientRequest, ContentType: w10bErrorCT, Body: PayloadBody{Bytes: []byte(body), Present: true}}
	input.Payloads = []AuditLogPayloadInput{payload}
	return input
}

func boolPtr(value bool) *bool { return &value }

func w10bPersistExpectOK(t *testing.T, store Store, lease OwnerLease, input AuditLogInput) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), w10bPgBudget)
	defer cancel()
	if _, err := store.Persist(ctx, lease, input); err != nil {
		t.Fatalf("Persist %s: %v", input.ID, err)
	}
}

func w10bBlobIdentity(t *testing.T, store Store, body, contentType string) (string, string) {
	t.Helper()
	implementation := store.(*sqlStore)
	record, _, err := newBlobRecord([]byte(body), contentType, "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), w10bPgBudget)
	defer cancel()
	var id, storageKey string
	if err := implementation.db.QueryRowContext(ctx, `SELECT id, storage_key FROM juhe_dataset.audit_payload_blobs WHERE sha256=$1 AND raw_size_bytes=$2 AND content_type=$3`, record.sha256, record.rawSize, record.contentType).Scan(&id, &storageKey); err != nil {
		t.Fatalf("读取 blob 身份失败: %v", err)
	}
	return id, storageKey
}

func w10bSeedPendingGC(t *testing.T, store Store, blobID, storageKey string) {
	t.Helper()
	implementation := store.(*sqlStore)
	ctx, cancel := context.WithTimeout(context.Background(), w10bPgBudget)
	defer cancel()
	if _, err := implementation.db.ExecContext(ctx, `INSERT INTO juhe_dataset.audit_payload_blob_gc (blob_id, storage_key, scheduled_at) VALUES ($1, $2, clock_timestamp()) ON CONFLICT (blob_id) DO UPDATE SET storage_key=excluded.storage_key, scheduled_at=excluded.scheduled_at`, blobID, storageKey); err != nil {
		t.Fatalf("补建 pending GC 行失败: %v", err)
	}
}

func TestW10BPgStoreFullChain(t *testing.T) {
	store, blobDir := w10bOpenCoverStore(t)
	w10bCleanupAuditRows(t, store)
	implementation := store.(*sqlStore)
	ctx, cancel := context.WithTimeout(context.Background(), w10bPgBudget)
	defer cancel()

	// PG 表名前缀与 OpenStore 默认 hotDir 推导。
	if implementation.leaseTable() != "juhe_dataset.audit_log_owner_leases" || implementation.table("audit_logs") != "juhe_dataset.audit_logs" {
		t.Fatal("PG 表名前缀错误")
	}
	if implementation.hotDir != filepath.Join(filepath.Dir(blobDir), "search-hot") {
		t.Fatalf("默认 hotDir=%q", implementation.hotDir)
	}

	// EnsureSchema PG 分支：advisory 锁 + 分号拆分 + 提交；二次调用走 schemaReady。
	for i := 0; i < 2; i++ {
		if err := store.EnsureSchema(ctx); err != nil {
			t.Fatalf("EnsureSchema #%d: %v", i, err)
		}
	}

	// owner lease：获取、互斥、续约、释放、未知释放。
	lease, acquired, err := store.AcquireOwnerLease(ctx, w10bOwnerID, time.Minute)
	if err != nil || !acquired {
		t.Fatalf("acquire owner lease: %t %v", acquired, err)
	}
	if _, acquired, err := store.AcquireOwnerLease(ctx, "w10b-other-owner", time.Minute); err != nil || acquired {
		t.Fatalf("活跃 owner 必须拒绝第二个 owner: %t %v", acquired, err)
	}
	if ok, err := store.RenewOwnerLease(ctx, lease, time.Minute); err != nil || !ok {
		t.Fatalf("renew owner: %t %v", ok, err)
	}
	if ok, err := store.RenewOwnerLease(ctx, OwnerLease{OwnerID: w10bOwnerID, FenceToken: lease.FenceToken + 999}, time.Minute); err != nil || ok {
		t.Fatalf("错误 fence 续约必须返回 false: %t %v", ok, err)
	}

	// Persist PG 全链：生命周期锁 + blob 锁 + 引用 + error group + 提交前 fence。
	nowText := time.Now().UTC().Format(time.RFC3339Nano)
	first := w10bErrorFixture("w10b-persist-first", LifecycleFinalized, nowText, w10bErrorBody)
	w10bPersistExpectOK(t, store, lease, first)
	// success 行走 trim 路径（audit_outcome='success' + created_at 窗口 + body payload）。
	hot := w10bErrorFixture("w10b-persist-hot", LifecycleFinalized, nowText, w10bErrorBody)
	hot.AuditOutcome = AuditOutcomeSuccess
	hot.Success = true
	hot.SampleReason = "success_hot_full_retention"
	w10bPersistExpectOK(t, store, lease, hot)

	// 同 ID finalized 重放：upsert 条件不匹配 → 忽略。
	result, err := store.Persist(ctx, lease, first)
	if err != nil || !result.Ignored {
		t.Fatalf("同 ID 重放必须被忽略: %+v %v", result, err)
	}

	// 空/失效租约在写面前被拒绝。
	if _, err := store.Persist(ctx, OwnerLease{}, first); !errors.Is(err, ErrOwnerLeaseLost) {
		t.Fatalf("空租约必须拒绝: %v", err)
	}
	past := time.Now().UTC().Add(-time.Minute)
	if _, err := implementation.db.ExecContext(ctx, `UPDATE juhe_dataset.audit_log_owner_leases SET lease_until=$1 WHERE lease_key=$2 AND owner_id=$3`, past, w10bLeaseKey, w10bOwnerID); err != nil {
		t.Fatalf("过期租约失败: %v", err)
	}
	stale := w10bErrorFixture("w10b-persist-stale", LifecycleFinalized, nowText, w10bErrorBody)
	if _, err := store.Persist(ctx, lease, stale); !errors.Is(err, ErrOwnerLeaseLost) {
		t.Fatalf("过期租约 Persist 必须拒绝且不落库: %v", err)
	}
	if err := store.ReleaseOwnerLease(ctx, lease); err != nil {
		t.Fatalf("release owner: %v", err)
	}
	// 释放其他 fence 的租约 → ErrOwnerLeaseLost（n!=1 臂）。
	if err := store.ReleaseOwnerLease(ctx, OwnerLease{OwnerID: w10bOwnerID, FenceToken: lease.FenceToken + 999}); !errors.Is(err, ErrOwnerLeaseLost) {
		t.Fatalf("释放未知 fence 必须报 ErrOwnerLeaseLost: %v", err)
	}

	// 重新获取并验证 takeover fence 递增。
	renewed, acquired, err := store.AcquireOwnerLease(ctx, w10bOwnerID, time.Minute)
	if err != nil || !acquired || renewed.FenceToken <= lease.FenceToken {
		t.Fatalf("重新获取必须递增 fence: %#v %t %v", renewed, acquired, err)
	}
	lease = renewed

	// 临时 blob 清理：持有租约的提交链 + 失效租约拒绝。
	if err := store.CleanupOwnedBlobTemps(ctx, lease, time.Now().UTC().Add(-time.Hour)); err != nil {
		t.Fatalf("CleanupOwnedBlobTemps: %v", err)
	}
	if err := store.CleanupOrphanedBlobTemps(ctx, lease, time.Now().UTC().Add(-time.Hour)); err != nil {
		t.Fatalf("CleanupOrphanedBlobTemps: %v", err)
	}
	if err := store.CleanupOwnedBlobTemps(ctx, OwnerLease{}, time.Now().UTC()); !errors.Is(err, ErrOwnerLeaseLost) {
		t.Fatalf("失效租约清理必须拒绝: %v", err)
	}

	// AppendHotSearch PG 事务链。
	if appended, err := store.AppendHotSearch(ctx, lease, []AuditLogInput{first}); err != nil || appended == 0 {
		t.Fatalf("AppendHotSearch: %d %v", appended, err)
	}

	// retention PG 全链：trim + 删除 + error group GC + 调度 + 物理清理。
	// trim 窗口为 successCutoff <= created_at < hotCutoff；success 行 createdAt=now。
	retentionConfig := RetentionConfig{
		SuccessHotCutoff: time.Now().UTC().Add(time.Hour),
		SuccessCutoff:    time.Now().UTC().Add(-time.Hour),
		FailureCutoff:    time.Now().UTC().Add(time.Hour),
		ErrorGroupCutoff: time.Now().UTC().Add(time.Hour),
		BatchSize:        8,
	}
	report, err := store.CleanupRetention(ctx, lease, retentionConfig)
	if err != nil {
		t.Fatalf("CleanupRetention: %v", err)
	}
	if report.DeletedLogs == 0 || report.SuccessHotTrimmed == 0 {
		t.Fatalf("retention 必须删除 w10b 行: %+v", report)
	}
	if report.DeletedPayloadBlobs == 0 {
		t.Fatalf("retention 必须清理已调度的 blob: %+v", report)
	}
	if entries, readErr := os.ReadDir(filepath.Join(blobDir, "sha256")); readErr == nil && len(entries) != 0 {
		// 同一 canonical blob 可能被成功/失败行共享；只要目录里不再有 w10b blob 文件即可。
		for _, entry := range entries {
			if strings.Contains(entry.Name(), shortHash(w10bErrorCT)) {
				t.Fatalf("w10b blob 文件未被清理: %s", entry.Name())
			}
		}
	}

	// 失效租约下 retention 在写面前被拒绝。
	expiredLease := lease
	if _, err := implementation.db.ExecContext(ctx, `UPDATE juhe_dataset.audit_log_owner_leases SET lease_until=$1 WHERE lease_key=$2 AND owner_id=$3`, past, w10bLeaseKey, w10bOwnerID); err != nil {
		t.Fatalf("过期租约失败: %v", err)
	}
	if _, err := store.CleanupRetention(ctx, expiredLease, retentionConfig); !errors.Is(err, ErrOwnerLeaseLost) {
		t.Fatalf("过期租约 retention 必须拒绝: %v", err)
	}
	_, acquired, err = store.AcquireOwnerLease(ctx, w10bOwnerID, time.Minute)
	if err != nil || !acquired {
		t.Fatalf("重新获取租约失败: %t %v", acquired, err)
	}
}

func TestW10BPgBlobGCReactivationArms(t *testing.T) {
	store, blobDir := w10bOpenCoverStore(t)
	w10bCleanupAuditRows(t, store)
	implementation := store.(*sqlStore)
	ctx, cancel := context.WithTimeout(context.Background(), w10bPgBudget)
	defer cancel()
	lease, acquired, err := store.AcquireOwnerLease(ctx, w10bOwnerID+"-gc", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("acquire owner lease: %t %v", acquired, err)
	}
	nowText := time.Now().UTC().Format(time.RFC3339Nano)

	// 臂 A：GC 行存在 + blob 行已删（同内容 blob）→ 删除失配 GC 元数据后继续发布。
	// 先用 in_progress 写入建立 blob（无引用），再删行删文件并补建 GC 行。
	identity, _, err := newBlobRecord([]byte(w10bErrorBody), w10bErrorCT, "")
	if err != nil {
		t.Fatal(err)
	}
	inProgressA := w10bErrorFixture("w10b-gc-progress-a", LifecycleInProgress, nowText, w10bErrorBody)
	w10bPersistExpectOK(t, store, lease, inProgressA)
	if _, err := implementation.db.ExecContext(ctx, `DELETE FROM juhe_dataset.audit_payload_blobs WHERE id=$1`, identity.id); err != nil {
		t.Fatalf("删除 blob 行失败: %v", err)
	}
	w10bSeedPendingGC(t, store, identity.id, identity.storageKey)
	reactivated := w10bErrorFixture("w10b-gc-reactivate-a", LifecycleFinalized, nowText, w10bErrorBody)
	w10bPersistExpectOK(t, store, lease, reactivated)
	var pending int
	if err := implementation.db.QueryRowContext(ctx, `SELECT count(*) FROM juhe_dataset.audit_payload_blob_gc WHERE blob_id=$1`, identity.id).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 0 {
		t.Fatalf("失配 GC 行必须被清除")
	}
	if _, err := os.Stat(filepath.Join(blobDir, filepath.FromSlash(identity.storageKey))); err != nil {
		t.Fatalf("重新发布必须恢复物理文件: %v", err)
	}

	// 臂 B：GC 行存在 + blob 行存在 + 物理文件缺失 + 无引用 → 删除元数据并重新发布。
	inProgress := w10bErrorFixture("w10b-gc-progress-b", LifecycleInProgress, nowText, w10bArmBBody)
	w10bPersistExpectOK(t, store, lease, inProgress)
	blobID, storageKey := w10bBlobIdentity(t, store, w10bArmBBody, w10bErrorCT)
	w10bSeedPendingGC(t, store, blobID, storageKey)
	if err := os.Remove(filepath.Join(blobDir, filepath.FromSlash(storageKey))); err != nil {
		t.Fatalf("移除物理 blob 文件失败: %v", err)
	}
	finalized := w10bErrorFixture("w10b-gc-final-b", LifecycleFinalized, nowText, w10bArmBBody)
	w10bPersistExpectOK(t, store, lease, finalized)
	if _, err := os.Stat(filepath.Join(blobDir, filepath.FromSlash(storageKey))); err != nil {
		t.Fatalf("重新发布必须恢复物理文件: %v", err)
	}
	if err := implementation.db.QueryRowContext(ctx, `SELECT count(*) FROM juhe_dataset.audit_payload_blob_gc WHERE blob_id=$1`, blobID).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 0 {
		t.Fatalf("重新发布必须取消 pending GC")
	}

	// 臂 C：GC 行存在 + 引用仍在 → 拒绝删除并获得引用的 blob 元数据。
	reference := w10bErrorFixture("w10b-gc-ref-c", LifecycleFinalized, nowText, w10bArmCBody)
	w10bPersistExpectOK(t, store, lease, reference)
	blobID, storageKey = w10bBlobIdentity(t, store, w10bArmCBody, w10bErrorCT)
	w10bSeedPendingGC(t, store, blobID, storageKey)
	if err := os.Remove(filepath.Join(blobDir, filepath.FromSlash(storageKey))); err != nil {
		t.Fatalf("移除物理 blob 文件失败: %v", err)
	}
	conflict := w10bErrorFixture("w10b-gc-conflict-c", LifecycleFinalized, nowText, w10bArmCBody)
	if _, err := store.Persist(ctx, lease, conflict); err == nil || !strings.Contains(err.Error(), "重新获得引用") {
		t.Fatalf("引用仍在时必须拒绝删除元数据: %v", err)
	}
}

func TestW10BPgRetentionErrorGroupCleanup(t *testing.T) {
	store, _ := w10bOpenCoverStore(t)
	w10bCleanupAuditRows(t, store)
	ctx, cancel := context.WithTimeout(context.Background(), w10bPgBudget)
	defer cancel()
	lease, acquired, err := store.AcquireOwnerLease(ctx, w10bOwnerID+"-egc", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("acquire owner lease: %t %v", acquired, err)
	}
	nowText := time.Now().UTC().Format(time.RFC3339Nano)
	now := time.Now().UTC()
	failed := w10bErrorFixture("w10b-egc-failed", LifecycleFinalized, nowText, w10bErrorBody)
	w10bPersistExpectOK(t, store, lease, failed)
	retentionConfig := RetentionConfig{
		SuccessHotCutoff: now.Add(time.Hour),
		SuccessCutoff:    now.Add(time.Hour),
		FailureCutoff:    now.Add(time.Hour),
		ErrorGroupCutoff: now.Add(time.Hour),
		BatchSize:        4,
	}
	report, err := store.CleanupRetention(ctx, lease, retentionConfig)
	if err != nil {
		t.Fatalf("CleanupRetention: %v", err)
	}
	if report.DeletedErrorGroups == 0 {
		t.Fatalf("过期 error group 必须被清理: %+v", report)
	}
	// Retain 是 CleanupRetention 的别名。
	if _, err := store.(interface {
		Retain(context.Context, OwnerLease, RetentionConfig) (RetentionResult, error)
	}).Retain(ctx, lease, RetentionConfig{}); err == nil {
		t.Fatal("空 cutoff 必须被 normalizeRetentionConfig 拒绝")
	}
}

func TestW10BPgOpenStoreAndContextArms(t *testing.T) {
	coverURL := w10bCoverPostgresURL(t)

	// 零值池配置触发 ValidatePoolLimits 失败（pgx 惰性解析 DSN，OpenStore 层不报错）。
	if _, err := OpenStore(Config{Mode: ModePostgres, PostgresURL: coverURL}); err == nil || !strings.Contains(err.Error(), "连接池") {
		t.Fatalf("零值池配置必须失败: %v", err)
	}

	// 外部提供 PostgresPool：跳过 registry 获取，Close 走 pool 分支。
	registry := pgpool.NewRegistry()
	pool, err := registry.Acquire(coverURL, "w10b-audit", 2, 1)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	pooled, err := OpenStore(Config{Mode: ModePostgres, PostgresPool: pool, PayloadBlobDirectory: t.TempDir()})
	if err != nil {
		t.Fatalf("OpenStore with pool: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), w10bPgBudget)
	defer cancel()
	if err := pooled.EnsureSchema(ctx); err != nil {
		t.Fatalf("pooled EnsureSchema: %v", err)
	}
	if err := pooled.Close(); err != nil {
		t.Fatalf("pooled Close: %v", err)
	}

	// 负臂库：canceled context 命中各事务入口失败臂。
	negURL := w10bCreateNegativeDatabase(t)
	negStore, err := OpenStore(Config{Mode: ModePostgres, PostgresURL: negURL, PostgresMaxOpenConns: 2, PostgresMaxIdleConns: 1, PayloadBlobDirectory: t.TempDir()})
	if err != nil {
		t.Fatalf("OpenStore neg: %v", err)
	}
	defer negStore.Close()
	canceled, cancelArm := context.WithCancel(context.Background())
	cancelArm()
	if err := negStore.EnsureSchema(canceled); err == nil || !strings.Contains(err.Error(), "开始 F3 PostgreSQL schema 事务失败") {
		t.Fatalf("canceled EnsureSchema: %v", err)
	}
	lease := OwnerLease{OwnerID: w10bOwnerID, FenceToken: 1}
	if _, _, err := negStore.AcquireOwnerLease(canceled, w10bOwnerID, time.Minute); err == nil {
		t.Fatal("canceled acquire 必须失败")
	}
	if _, err := negStore.RenewOwnerLease(canceled, lease, time.Minute); err == nil {
		t.Fatal("canceled renew 必须失败")
	}
	if _, err := negStore.Persist(canceled, lease, fixture("w10b-canceled", LifecycleFinalized)); err == nil {
		t.Fatal("canceled Persist 必须失败")
	}
	if _, err := negStore.CleanupRetention(canceled, lease, RetentionConfig{BatchSize: 1}); err == nil {
		t.Fatal("canceled retention 必须失败")
	}
	if _, err := negStore.AppendHotSearch(canceled, lease, []AuditLogInput{fixture("w10b-canceled-hot", LifecycleFinalized)}); err == nil {
		t.Fatal("canceled AppendHotSearch 必须失败")
	}
	if _, err := negStore.CleanupHotSearch(canceled, lease, time.Now().UTC(), 1); err == nil {
		t.Fatal("canceled CleanupHotSearch 必须失败")
	}
	if err := negStore.CleanupOwnedBlobTemps(canceled, lease, time.Now().UTC()); err == nil {
		t.Fatal("canceled cleanup temps 必须失败")
	}

	// 无 schema 的负臂库上 Persist 落入 EnsureSchema 失败。
	freshURL := w10bCreateNegativeDatabaseNamed(t, "juhe_ai_sub2api_dev_w10cneg2")
	freshStore, err := OpenStore(Config{Mode: ModePostgres, PostgresURL: freshURL, PostgresMaxOpenConns: 2, PostgresMaxIdleConns: 1, PayloadBlobDirectory: t.TempDir()})
	if err != nil {
		t.Fatalf("OpenStore fresh: %v", err)
	}
	defer freshStore.Close()
	freshCtx, freshCancel := context.WithTimeout(context.Background(), w10bPgBudget)
	defer freshCancel()
	if _, err := freshStore.Persist(freshCtx, lease, fixture("w10b-noschema", LifecycleFinalized)); err == nil {
		t.Fatal("无 schema 时 Persist 必须失败")
	}
}

func w10bCreateNegativeDatabaseNamed(t *testing.T, name string) string {
	t.Helper()
	env := w10bSharedEnv(t)
	host := env["DEV_POSTGRES_HOST"]
	port := env["DEV_POSTGRES_DIRECT_PORT"]
	adminUser := env["DEV_POSTGRES_ADMIN_USERNAME"]
	adminPass := env["DEV_POSTGRES_ADMIN_PASSWORD"]
	appURL := env["JUHE_AI_POSTGRES_URL"]
	if host == "" || port == "" || adminUser == "" || appURL == "" {
		t.Skipf("dev env 缺少 PG 键（跳过）")
	}
	admin, err := sql.Open("pgx", fmt.Sprintf("postgres://%s:%s@%s:%s/postgres?sslmode=disable", adminUser, adminPass, host, port))
	if err != nil {
		t.Fatalf("打开 dev PG 管理连接失败: %v", err)
	}
	defer admin.Close()
	ctx, cancel := context.WithTimeout(context.Background(), w10bPgBudget)
	defer cancel()
	if err := admin.PingContext(ctx); err != nil {
		t.Skipf("dev PG 管理口不可达（跳过）: %v", err)
	}
	if _, err := admin.ExecContext(ctx, `DROP DATABASE IF EXISTS `+name+` WITH (FORCE)`); err != nil {
		t.Fatalf("清理临时库失败: %v", err)
	}
	owner := env["DEV_POSTGRES_APP_USERNAME"]
	if owner == "" {
		owner = adminUser
	}
	if _, err := admin.ExecContext(ctx, `CREATE DATABASE `+name+` OWNER `+owner); err != nil {
		t.Fatalf("创建临时库失败: %v", err)
	}
	t.Cleanup(func() {
		dropCtx, dropCancel := context.WithTimeout(context.Background(), w10bPgBudget)
		defer dropCancel()
		_, _ = admin.ExecContext(dropCtx, `DROP DATABASE IF EXISTS `+name+` WITH (FORCE)`)
	})
	sep := strings.LastIndex(appURL, "/")
	if sep < 0 {
		t.Skipf("dev env JUHE_AI_POSTGRES_URL 形态异常（跳过）")
	}
	return appURL[:sep+1] + name
}
