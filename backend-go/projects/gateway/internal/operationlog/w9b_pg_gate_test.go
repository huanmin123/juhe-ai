package operationlog

// w9b PG 门禁族测试（PLAN-20261024T051925945Z 迁移）：F4 PostgreSQL 读写面与
// legacy PG 迁移链打到 pgtestkit 每测试独立库（取代旧共享临时库
// juhe_ai_sub2api_dev_w9bcover + skip 门禁；PG 不可达按 pgtestkit 契约显式
// 失败）。juhe_dataset 建表走生产 EnsureSchema；legacy 形态 DDL 与种子同处
// prepare。测试输出永不携带连接串。

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

// w9bInt 是 StatusCode（*int）的便捷构造。
func w9bInt(value int) *int { return &value }

// w9bInsertRetentionSetting 写入 operationLogRetentionDays 业务设置（列型与
// 夹具一致：value_json 为 text）。
func w9bInsertRetentionSetting(t *testing.T, db *sql.DB, value string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO juhe_business.system_settings (system_account_id,key,value_json,updated_at) VALUES ('sys_admin','operationLogRetentionDays',$1,clock_timestamp()::text) ON CONFLICT (system_account_id,key) DO UPDATE SET value_json=EXCLUDED.value_json`, value); err != nil {
		t.Fatalf("写入 retention 设置失败：%v", err)
	}
}

func TestW9BPostgresOwnerLeaseLifecycleArms(t *testing.T) {
	_, store := openW9BCoverStore(t)
	ctx := context.Background()
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema=%v", err)
	}
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema 二次调用必须短路成功：%v", err)
	}
	lease, ok, err := store.AcquireOwnerLease(ctx, "w9b-pg-owner", time.Minute)
	if err != nil || !ok {
		t.Fatalf("首次获取租约：ok=%v err=%v", ok, err)
	}
	// 未过期租约不可被第二持有者夺取。
	if _, ok, err := store.AcquireOwnerLease(ctx, "w9b-pg-other", time.Minute); err != nil || ok {
		t.Fatalf("未过期租约被夺取：ok=%v err=%v", ok, err)
	}
	// 续租成功（PG 事务臂）。
	if renewed, err := store.RenewOwnerLease(ctx, lease, time.Minute); err != nil || !renewed {
		t.Fatalf("续租：renewed=%v err=%v", renewed, err)
	}
	// 过期 fence 续租失败。
	stale := OwnerLease{OwnerID: lease.OwnerID, FenceToken: lease.FenceToken + 999}
	if renewed, err := store.RenewOwnerLease(ctx, stale, time.Minute); err != nil || renewed {
		t.Fatalf("过期 fence 续租应失败：renewed=%v err=%v", renewed, err)
	}
	// 过期 fence 释放返回 ErrOwnerLeaseLost（PG 分支 n!=1 臂）。
	if err := store.ReleaseOwnerLease(ctx, stale); err == nil || err.Error() != ErrOwnerLeaseLost.Error() {
		t.Fatalf("过期 fence 释放应 ErrOwnerLeaseLost：%v", err)
	}
	if err := store.ReleaseOwnerLease(ctx, lease); err != nil {
		t.Fatalf("正常释放=%v", err)
	}
	// 释放后租约立即可以重新获取（fence 递增）。
	next, ok, err := store.AcquireOwnerLease(ctx, "w9b-pg-owner", time.Minute)
	if err != nil || !ok {
		t.Fatalf("释放后再获取：ok=%v err=%v", ok, err)
	}
	if next.FenceToken <= lease.FenceToken {
		t.Fatalf("fence 必须单调递增：before=%d after=%d", lease.FenceToken, next.FenceToken)
	}
	// 即将过期的租约可以被夺取：把 lease_until 推到过去。
	if _, err := store.db.Exec(`UPDATE juhe_dataset.operation_log_owner_leases SET lease_until=clock_timestamp()-interval '1 second'`); err != nil {
		t.Fatalf("推旧租约失败：%v", err)
	}
	if _, ok, err := store.AcquireOwnerLease(ctx, "w9b-pg-takeover", time.Minute); err != nil || !ok {
		t.Fatalf("过期租约应可夺取：ok=%v err=%v", ok, err)
	}
}

func TestW9BPostgresPersistListDetailRetentionArms(t *testing.T) {
	testDB, store := openW9BCoverStore(t)
	ctx := context.Background()
	lease, ok, err := store.AcquireOwnerLease(ctx, "w9b-pg-writer", time.Minute)
	if err != nil || !ok {
		t.Fatalf("获取租约：ok=%v err=%v", ok, err)
	}
	input := Input{
		ID: "w9b-pg-log-1", TraceID: "w9b-trace-1", ActorSystemAccountID: "actor", ActorUsername: "actor", ActorDisplayName: "Actor PG",
		ActorRole: "admin", OperationScopeSystemAccountID: "viewer", Mode: "admin", Module: "accounts", Action: "update",
		OperationKey: "accounts.update", ResourceType: "account", ResourceID: "acc-1", ResourceName: "Alpha",
		Summary: "更新 Alpha 账户 w9b", DetailLevel: "full", VisibilityScope: "targeted",
		Changes:  []Change{{Field: "enabled", Label: "启用", Before: false, After: true}},
		Metadata: []byte(`{"source":"w9b"}`),
		Method:   "PATCH", Path: "/api/accounts/acc-1", StatusCode: w9bInt(200), ClientIP: "203.0.113.9",
		UserAgent: "w9b-agent", CreatedAt: "2026-09-01T00:00:00.000000000Z",
		Targets: []Target{{TargetType: "account", TargetID: "acc-1", TargetName: "Alpha", TargetOwnerSystemAccountID: "viewer", Relation: "primary"}},
		Viewers: []Viewer{{SystemAccountID: "viewer", VisibilityReason: "resource_owner", DetailLevel: "full"}},
	}
	ignored, err := store.Persist(ctx, lease, input)
	if err != nil || ignored {
		t.Fatalf("首次 Persist：ignored=%v err=%v", ignored, err)
	}
	// 租约丢失的写路径（verifyLease PG ErrNoRows 臂）。
	if _, err := store.db.Exec(`UPDATE juhe_dataset.operation_log_owner_leases SET lease_until=clock_timestamp()-interval '1 second' WHERE lease_key='f4-operation-log-persistence'`); err != nil {
		t.Fatalf("推旧租约失败：%v", err)
	}
	fenced := input
	fenced.ID = "w9b-pg-fenced"
	if _, err := store.Persist(ctx, lease, fenced); err == nil || err.Error() != ErrOwnerLeaseLost.Error() {
		t.Fatalf("租约丢失写入必须被拒绝：%v", err)
	}
	if _, err := store.CleanupRetention(ctx, lease, time.Now(), 10); err == nil || err.Error() != ErrOwnerLeaseLost.Error() {
		t.Fatalf("租约丢失清理必须被拒绝：%v", err)
	}
	// 重新获取租约继续。
	lease, ok, err = store.AcquireOwnerLease(ctx, "w9b-pg-writer", time.Minute)
	if err != nil || !ok {
		t.Fatalf("重新获取租约：ok=%v err=%v", ok, err)
	}
	// TraceID 前缀过滤覆盖 PG COLLATE "C" 臂；SummaryKeyword 覆盖 search terms 臂。
	listed, err := store.List(ctx, ListOptions{TraceID: "w9b-trace", SummaryKeyword: "pha 账"})
	if err != nil || len(listed.Items) != 1 || listed.Items[0].ID != input.ID {
		t.Fatalf("PG trace/summary 列表：result=%+v err=%v", listed, err)
	}
	if listed.Items[0].ActorSystemAccountName != "Actor PG" {
		t.Fatalf("PG 账户名解析失败：%+v", listed.Items[0])
	}
	// 受影响账户过滤。
	affected, err := store.List(ctx, ListOptions{AffectedSystemAccountID: "viewer"})
	if err != nil || len(affected.Items) != 1 {
		t.Fatalf("PG affected 列表：%+v err=%v", affected, err)
	}
	// 个人双流列表。
	personal, err := store.List(ctx, ListOptions{ViewerID: "viewer"})
	if err != nil || len(personal.Items) != 1 {
		t.Fatalf("PG personal 列表：%+v err=%v", personal, err)
	}
	// 无效时间范围。
	if _, err := store.List(ctx, ListOptions{StartAt: "not-a-time"}); err == nil {
		t.Fatal("无效 startAt 必须报错")
	}
	// full viewer 详情：包含 changes/targets 且清理 client ip（viewers 列表仅
	// admin 视角读取）。
	detail, found, err := store.Detail(ctx, input.ID, "viewer")
	if err != nil || !found {
		t.Fatalf("viewer 详情：found=%v err=%v", found, err)
	}
	if detail.ClientIP != "" || len(detail.Changes) != 1 || len(detail.Targets) != 1 || len(detail.Viewers) != 0 {
		t.Fatalf("viewer 详情裁剪不符：detail=%+v", detail)
	}
	if detail.Targets[0].TargetOwnerSystemAccountName != "Viewer PG" {
		t.Fatalf("target owner 名解析失败：%+v", detail.Targets[0])
	}
	// admin 视角（viewerID 为空）保留 client ip 且带 viewers 列表。
	adminDetail, found, err := store.Detail(ctx, input.ID, "")
	if err != nil || !found || adminDetail.ClientIP != "203.0.113.9" {
		t.Fatalf("admin 详情：found=%v client=%q err=%v", found, adminDetail.ClientIP, err)
	}
	hasViewerRow := false
	for _, viewer := range adminDetail.Viewers {
		if viewer.SystemAccountID == "viewer" && viewer.SystemAccountName == "Viewer PG" {
			hasViewerRow = true
		}
	}
	if len(adminDetail.Viewers) < 1 || !hasViewerRow {
		t.Fatalf("admin viewers 列表异常：%+v", adminDetail.Viewers)
	}
	// summary 级 viewer 只能拿到空骨架。
	summaryInput := input
	summaryInput.ID = "w9b-pg-summary"
	summaryInput.Summary = "摘要级记录 w9b"
	summaryInput.DetailLevel = "summary"
	summaryInput.Viewers = []Viewer{{SystemAccountID: "viewer", VisibilityReason: "resource_owner", DetailLevel: "summary"}}
	if _, err := store.Persist(ctx, lease, summaryInput); err != nil {
		t.Fatalf("写入 summary 记录失败：%v", err)
	}
	summaryDetail, found, err := store.Detail(ctx, summaryInput.ID, "viewer")
	if err != nil || !found || summaryDetail.Changes == nil || summaryDetail.Targets == nil || len(summaryDetail.Changes) != 0 {
		t.Fatalf("summary viewer 骨架：detail=%+v found=%v err=%v", summaryDetail, found, err)
	}
	// 不存在 id。
	if _, found, err := store.Detail(ctx, "missing-id", ""); err != nil || found {
		t.Fatalf("缺失详情：found=%v err=%v", found, err)
	}
	// summary-only viewer 只能拿到空骨架。
	w9bInsertRetentionSetting(t, testDB.DB, "30")
	if days, err := store.RetentionDays(ctx, 365); err != nil || days != 30 {
		t.Fatalf("PG retention 读取：days=%d err=%v", days, err)
	}
	if _, err := store.db.Exec(`DELETE FROM juhe_business.system_settings WHERE key='operationLogRetentionDays'`); err != nil {
		t.Fatalf("清理 retention 设置失败：%v", err)
	}
	if days, err := store.RetentionDays(ctx, 365); err != nil || days != 365 {
		t.Fatalf("PG retention fallback：days=%d err=%v", days, err)
	}
	// 过期记录清理（PG id=ANY 臂）。
	old := input
	old.ID = "w9b-pg-old"
	old.CreatedAt = "2020-01-01T00:00:00.000000000Z"
	old.TraceID = "w9b-trace-old"
	if _, err := store.Persist(ctx, lease, old); err != nil {
		t.Fatalf("写入过期记录失败：%v", err)
	}
	deleted, err := store.CleanupRetention(ctx, lease, time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC), 10)
	if err != nil || deleted != 1 {
		t.Fatalf("PG 保留清理：deleted=%d err=%v", deleted, err)
	}
}

// TestW9BPostgresLegacyMigrationUnknownPrimaryKey 覆盖 MigrateLegacyPostgres
// 的未知主键拒绝臂（真实 catalog 路径）。
func TestW9BPostgresLegacyMigrationUnknownPrimaryKey(t *testing.T) {
	testDB := pgtestkit.NewTestDatabase(t)
	db := testDB.DB
	ctx := context.Background()
	if _, err := db.Exec(`
CREATE SCHEMA IF NOT EXISTS juhe_dataset;
CREATE TABLE juhe_dataset.operation_logs (id text PRIMARY KEY, trace_id text, actor_system_account_id text NOT NULL, actor_username text, actor_display_name text, actor_role text NOT NULL, operation_scope_system_account_id text, mode text NOT NULL, module text NOT NULL, action text NOT NULL, operation_key text NOT NULL, resource_type text NOT NULL, resource_id text, resource_name text, summary text NOT NULL, detail_level text NOT NULL, visibility_scope text NOT NULL, changes_json jsonb NOT NULL, metadata_json jsonb NOT NULL, method text, path text, status_code integer, client_ip text, user_agent text, created_at timestamptz NOT NULL);
CREATE TABLE juhe_dataset.operation_log_targets (id text PRIMARY KEY, operation_log_id text NOT NULL REFERENCES juhe_dataset.operation_logs(id) ON DELETE CASCADE, target_type text NOT NULL, target_id text, target_name text, target_owner_system_account_id text, relation text NOT NULL, created_at timestamptz NOT NULL);
CREATE TABLE juhe_dataset.operation_log_viewers (operation_log_id text NOT NULL REFERENCES juhe_dataset.operation_logs(id) ON DELETE CASCADE, system_account_id text NOT NULL, visibility_reason text NOT NULL, detail_level text NOT NULL, created_at timestamptz NOT NULL, PRIMARY KEY(operation_log_id,system_account_id));
CREATE TABLE juhe_dataset.operation_log_summary_search_terms (operation_log_id text NOT NULL REFERENCES juhe_dataset.operation_logs(id) ON DELETE CASCADE, term text NOT NULL, created_at timestamptz NOT NULL, PRIMARY KEY(term,operation_log_id));
CREATE TABLE juhe_dataset.operation_log_owner_leases (lease_key text PRIMARY KEY, owner_id text NOT NULL, fence_token bigint NOT NULL, lease_until timestamptz NOT NULL, updated_at timestamptz NOT NULL)`); err != nil {
		t.Fatalf("建旧 Node schema 失败：%v", err)
	}
	_, err := MigrateLegacyPostgres(ctx, Config{Mode: ModePostgres, PostgresURL: testDB.DSN}, LegacyMigrationOptions{NodeStopped: true, GoStopped: true, BackupConfirmed: true})
	if err == nil || !strings.Contains(err.Error(), "未知 operation_log_viewers 主键") {
		t.Fatalf("未知主键必须拒绝：%v", err)
	}
}

// TestW9BPostgresLegacyMigrationLifecycle 覆盖 MigrateLegacyPostgres 的旧主键
// 就地升级全流程（DDL、索引与 search terms 重建、抽样校验）与重复执行的
// no-op 分支（真实 catalog 路径）。
func TestW9BPostgresLegacyMigrationLifecycle(t *testing.T) {
	testDB := pgtestkit.NewTestDatabase(t)
	db := testDB.DB
	ctx := context.Background()
	if _, err := db.Exec(`
CREATE SCHEMA IF NOT EXISTS juhe_dataset;
CREATE TABLE juhe_dataset.operation_logs (id text PRIMARY KEY,trace_id text,actor_system_account_id text NOT NULL,actor_username text,actor_display_name text,actor_role text NOT NULL,operation_scope_system_account_id text,mode text NOT NULL,module text NOT NULL,action text NOT NULL,operation_key text NOT NULL,resource_type text NOT NULL,resource_id text,resource_name text,summary text NOT NULL,detail_level text NOT NULL,visibility_scope text NOT NULL,changes_json jsonb NOT NULL,metadata_json jsonb NOT NULL,method text,path text,status_code integer,client_ip text,user_agent text,created_at timestamptz NOT NULL);
CREATE TABLE juhe_dataset.operation_log_targets (id text PRIMARY KEY,operation_log_id text NOT NULL REFERENCES juhe_dataset.operation_logs(id) ON DELETE CASCADE,target_type text NOT NULL,target_id text,target_name text,target_owner_system_account_id text,relation text NOT NULL,created_at timestamptz NOT NULL);
CREATE TABLE juhe_dataset.operation_log_viewers (operation_log_id text NOT NULL REFERENCES juhe_dataset.operation_logs(id) ON DELETE CASCADE,system_account_id text NOT NULL,visibility_reason text NOT NULL,detail_level text NOT NULL,created_at timestamptz NOT NULL,PRIMARY KEY(operation_log_id,system_account_id,visibility_reason));
CREATE TABLE juhe_dataset.operation_log_summary_search_terms (operation_log_id text NOT NULL REFERENCES juhe_dataset.operation_logs(id) ON DELETE CASCADE,term text NOT NULL,created_at timestamptz NOT NULL,PRIMARY KEY(term,operation_log_id));
CREATE TABLE juhe_dataset.operation_log_owner_leases (lease_key text PRIMARY KEY, owner_id text NOT NULL, fence_token bigint NOT NULL, lease_until timestamptz NOT NULL, updated_at timestamptz NOT NULL);
INSERT INTO juhe_dataset.operation_logs (id,actor_system_account_id,actor_role,mode,module,action,operation_key,resource_type,summary,detail_level,visibility_scope,changes_json,metadata_json,created_at) VALUES ('w9b-legacy-1','actor','admin','self','accounts','update','accounts.update','account','w9b 旧摘要 legacy','full','targeted','[{"field":"enabled","before":false,"after":true}]'::jsonb,'{"source":"legacy"}'::jsonb,clock_timestamp());
INSERT INTO juhe_dataset.operation_logs (id,actor_system_account_id,actor_role,mode,module,action,operation_key,resource_type,summary,detail_level,visibility_scope,changes_json,metadata_json,created_at) VALUES ('w9b-legacy-2','actor','admin','self','accounts','update','accounts.update','account','w9b 新摘要 modern','full','all_users','[]'::jsonb,'{}'::jsonb,clock_timestamp());
INSERT INTO juhe_dataset.operation_log_targets VALUES ('w9b-legacy-target','w9b-legacy-1','account','acc-1','Alpha','owner','primary',clock_timestamp());
INSERT INTO juhe_dataset.operation_log_viewers VALUES ('w9b-legacy-1','actor','actor_self','full',clock_timestamp());`); err != nil {
		t.Fatalf("建旧 Node schema 失败：%v", err)
	}
	options := LegacyMigrationOptions{NodeStopped: true, GoStopped: true, BackupConfirmed: true}
	cfg := Config{Mode: ModePostgres, PostgresURL: testDB.DSN}
	result, err := MigrateLegacyPostgres(ctx, cfg, options)
	if err != nil {
		t.Fatalf("历史迁移失败：%v", err)
	}
	if result.NoOp || !result.SearchTermsRebuilt || result.MigratedOperationLogs != 2 {
		t.Fatalf("迁移结果=%+v", result)
	}
	if err := validatePostgresSchema(ctx, postgresSQLCatalog{queryer: db}); err != nil {
		t.Fatalf("迁移后 catalog 校验失败：%v", err)
	}
	// 旧 viewer 行保留且主键已带 detail_level。
	var detailLevel string
	if err = db.QueryRowContext(ctx, `SELECT detail_level FROM juhe_dataset.operation_log_viewers WHERE operation_log_id='w9b-legacy-1' AND system_account_id='actor'`).Scan(&detailLevel); err != nil || detailLevel != "full" {
		t.Fatalf("旧 viewer 未保留：%q err=%v", detailLevel, err)
	}
	// search terms 已按当前分词规则重建。
	var terms int
	if err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM juhe_dataset.operation_log_summary_search_terms WHERE operation_log_id='w9b-legacy-1'`).Scan(&terms); err != nil || terms == 0 {
		t.Fatalf("search terms 未重建：terms=%d err=%v", terms, err)
	}
	// 迁移后的行数守卫：抽样校验升级前后业务事实一致由迁移事务内部完成，
	// 这里验证重复执行必须 no-op。
	repeated, err := MigrateLegacyPostgres(ctx, cfg, options)
	if err != nil || !repeated.NoOp {
		t.Fatalf("重复迁移必须 no-op：result=%+v err=%v", repeated, err)
	}
}
