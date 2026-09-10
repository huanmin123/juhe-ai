package cleanuprepo

import (
	"context"
	"database/sql/driver"
	"fmt"
	"strings"
	"testing"
)

// deleteaccount.go（过期逻辑删除物理清理）的语义测试：SQLite 真库覆盖
// 候选列表、目标构建（关联实例/授权/团队 scope/grants）与物理删除主链；
// PG 路径用录制驱动覆盖孤儿扫尾、授权回收与 tombstone outbox。

func newKitDeletedAccountStore(t *testing.T, records *RecordCleanupStore) (*DeletedAccountStore, *DB) {
	t.Helper()
	business := openKitSQLite(t, "business_cleanup")
	createKitBusinessSchema(t, business.DB)
	store := &DeletedAccountStore{
		Business: business,
		Records:  records,
		Now:      kitNow,
	}
	return store, business
}

// seedKitDeletedAccount 插入账户行；空字符串字段写入 NULL（候选查询按
// IS NULL / IS NOT NULL 过滤，空串语义不同）。
func seedKitDeletedAccount(t *testing.T, business *DB, id, deletedAt, instanceAuthID, instanceSourceID string) {
	t.Helper()
	nullIfEmpty := func(value string) any {
		if value == "" {
			return nil
		}
		return value
	}
	mustExecKit(t, business, `INSERT INTO accounts (
      id, system_account_id, status, schedulable, provider_code, type, deleted_at,
      authorization_instance_authorization_id, authorization_instance_source_account_id,
      created_at, updated_at)
    VALUES (?, 'sys-1', 'disabled', 0, 'openai', 'oauth', ?, ?, ?, '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`,
		id, nullIfEmpty(deletedAt), nullIfEmpty(instanceAuthID), nullIfEmpty(instanceSourceID))
}

// TestDeletedAccountCleanupExpiredSQLiteCompletes：无关联数据的过期账户被
// 物理删除，扫尾未接线时显式上报。
func TestDeletedAccountCleanupExpiredSQLiteCompletes(t *testing.T) {
	f := newKitRecordFixture(t)
	store, business := newKitDeletedAccountStore(t, f.store)
	seedKitDeletedAccount(t, business, "acc-1", "2026-06-01T00:00:00.000Z", "", "")
	// 未来删除时间不进候选。
	seedKitDeletedAccount(t, business, "acc-future", "2026-09-30T00:00:00.000Z", "", "")

	skipReasons := make([]string, 0, 2)
	store.OnOrphanSweepSkipped = func(_ context.Context, reason string) { skipReasons = append(skipReasons, reason) }

	summary, err := store.CleanupExpired(context.Background())
	if err != nil {
		t.Fatalf("CleanupExpired: %v", err)
	}
	if summary.CutoffDeletedAt != "2026-08-10T00:00:00.000Z" {
		t.Fatalf("CutoffDeletedAt = %q", summary.CutoffDeletedAt)
	}
	if summary.Attempted != 1 || summary.Completed != 1 || summary.Deferred != 0 {
		t.Fatalf("summary = %+v", summary)
	}
	if summary.PhysicallyDeletedAccounts != 1 || summary.DeletedRows != 1 {
		t.Fatalf("物理删除计数 = %+v", summary)
	}
	if len(skipReasons) != 1 || !strings.Contains(skipReasons[0], "未接线") {
		t.Fatalf("扫尾跳过上报 = %v", skipReasons)
	}
	var remaining int64
	if err := business.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM accounts WHERE id = 'acc-1'`).Scan(&remaining); err != nil {
		t.Fatalf("read accounts: %v", err)
	}
	if remaining != 0 {
		t.Fatalf("过期账户应被物理删除")
	}
	if got := mustQueryCountKit(t, business, `SELECT COUNT(*) FROM accounts WHERE id = 'acc-future'`); got != 1 {
		t.Fatalf("未过期账户应保留，实际 = %d", got)
	}

	// 启用扫尾但 SQLite 模式 → 显式跳过（依赖未迁移的运行态同步域）。
	store.OrphanSweepEnabled = true
	skipReasons = skipReasons[:0]
	if _, err := store.CleanupExpired(context.Background()); err != nil {
		t.Fatalf("second CleanupExpired: %v", err)
	}
	if len(skipReasons) != 1 || !strings.Contains(skipReasons[0], "尚未随迁移进入 jobs") {
		t.Fatalf("SQLite 扫尾跳过上报 = %v", skipReasons)
	}
}

// TestDeletedAccountCleanupExpiredDeferred：仍有统计/记录数据时目标移交
// record cleanup（Deferred）且不物理删除。
func TestDeletedAccountCleanupExpiredDeferred(t *testing.T) {
	f := newKitRecordFixture(t)
	store, business := newKitDeletedAccountStore(t, f.store)
	seedKitDeletedAccount(t, business, "acc-1", "2026-06-01T00:00:00.000Z", "", "")
	mustExecKit(t, f.stats, `INSERT INTO usage_stats_totals
    (system_account_id, scope_type, scope_id, request_count) VALUES ('sys-1','account','acc-1',1)`)

	summary, err := store.CleanupExpired(context.Background())
	if err != nil {
		t.Fatalf("CleanupExpired: %v", err)
	}
	if summary.Attempted != 1 || summary.Deferred != 1 || summary.Completed != 0 {
		t.Fatalf("summary = %+v", summary)
	}
	if len(summary.RecordCleanupTargets) != 1 || summary.RecordCleanupTargets[0].AccountID != "acc-1" {
		t.Fatalf("RecordCleanupTargets = %+v", summary.RecordCleanupTargets)
	}
	var remaining int64
	if err := business.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM accounts`).Scan(&remaining); err != nil {
		t.Fatalf("read accounts: %v", err)
	}
	if remaining != 1 {
		t.Fatalf("Deferred 目标不应被物理删除")
	}
	if store.LastTargetError != "" {
		t.Fatalf("不应记录目标错误：%q", store.LastTargetError)
	}
}

// TestDeletedAccountBuildTargetAndPhysicalDelete：关联实例账户/授权/团队
// scope/grants 全图构建与物理删除计数。
func TestDeletedAccountBuildTargetAndPhysicalDelete(t *testing.T) {
	f := newKitRecordFixture(t)
	store, business := newKitDeletedAccountStore(t, f.store)
	seedKitDeletedAccount(t, business, "acc-1", "2026-06-01T00:00:00.000Z", "", "")
	// 关联授权实例账户：deleted_at NULL（仍属 root 的关联行）。
	mustExecKit(t, business, `INSERT INTO accounts (
      id, system_account_id, status, deleted_at, authorization_instance_authorization_id,
      authorization_instance_source_account_id, created_at, updated_at)
    VALUES ('acc-2','sys-2','disabled', NULL, 'auth-9', 'acc-1', '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`)
	mustExecKit(t, business, `INSERT INTO resource_authorizations
    (id, resource_type, resource_id, grantee_system_account_id, resource_owner_system_account_id, status)
    VALUES ('auth-9','account','acc-1','sys-1','owner-2','active')`)
	mustExecKit(t, business, `INSERT INTO resource_authorization_sources
    (id, authorization_id, source_type, source_team_id, status)
    VALUES ('src-1','auth-9','manual','team-77','active')`)
	mustExecKit(t, business, `INSERT INTO resource_authorization_grants
    (id, resource_type, resource_id, resource_owner_system_account_id, grantee_type, grantee_system_account_id, status)
    VALUES ('g-1','account','acc-1','owner-2','system_account','sys-1','active')`)
	mustExecKit(t, business, `INSERT INTO group_accounts (id, group_id, account_id, account_authorization_id)
    VALUES ('ga-1','grp-1','acc-1', NULL), ('ga-2','grp-1','acc-2', NULL), ('ga-3','grp-2', NULL, 'auth-9')`)
	for _, table := range []string{"account_supported_models", "account_model_mappings", "account_tag_bindings"} {
		mustExecKit(t, business, `INSERT INTO `+table+` (id, account_id) VALUES ('x-1','acc-1')`)
	}
	mustExecKit(t, business, `INSERT INTO request_quota_hourly_window_scope_bindings
    (id, scope_type, scope_id, source_type, source_id)
    VALUES ('q-1','account_authorization','auth-9','',''), ('q-2','','','resource_authorization_grant','g-1')`)

	summary, err := store.CleanupExpired(context.Background())
	if err != nil {
		t.Fatalf("CleanupExpired: %v", err)
	}
	if summary.Attempted != 1 || summary.Completed != 1 {
		t.Fatalf("summary = %+v", summary)
	}
	if summary.PhysicallyDeletedAccounts != 2 || summary.PhysicallyDeletedAuthorizations != 1 ||
		summary.PhysicallyDeletedGrants != 1 || summary.PhysicallyDeletedGroupBindings != 3 {
		t.Fatalf("物理删除计数 = %+v", summary)
	}
	if summary.DeletedRows != 2+1+1+3 {
		t.Fatalf("DeletedRows = %d", summary.DeletedRows)
	}
	for _, table := range []string{
		"accounts", "resource_authorizations", "resource_authorization_sources",
		"resource_authorization_grants", "group_accounts", "account_supported_models",
		"account_model_mappings", "account_tag_bindings", "request_quota_hourly_window_scope_bindings",
	} {
		if got := mustQueryCountKit(t, business, `SELECT COUNT(*) FROM `+table); got != 0 {
			t.Fatalf("%s 应被清空，残余 %d", table, got)
		}
	}
}

// TestListCandidatesIncludesInstanceWithLiveSource：source 账户未删除时，
// 过期实例账户自身作为候选（root 查询不覆盖）。
func TestListCandidatesIncludesInstanceWithLiveSource(t *testing.T) {
	f := newKitRecordFixture(t)
	store, business := newKitDeletedAccountStore(t, f.store)
	seedKitDeletedAccount(t, business, "acc-live", "", "", "")
	seedKitDeletedAccount(t, business, "acc-inst", "2026-06-01T00:00:00.000Z", "auth-5", "acc-live")
	mustExecKit(t, business, `INSERT INTO resource_authorizations
    (id, resource_type, resource_id, grantee_system_account_id, resource_owner_system_account_id, status)
    VALUES ('auth-5','account','acc-live','sys-1','owner-2','active')`)

	summary, err := store.CleanupExpired(context.Background())
	if err != nil {
		t.Fatalf("CleanupExpired: %v", err)
	}
	if summary.Attempted != 1 || summary.Completed != 1 {
		t.Fatalf("summary = %+v", summary)
	}
	if summary.PhysicallyDeletedAccounts != 1 || summary.PhysicallyDeletedAuthorizations != 1 {
		t.Fatalf("物理删除计数 = %+v", summary)
	}
}

// TestOrphanSweepPostgres：孤儿授权实例扫尾（录制驱动，断言 revoke 链与
// tombstone outbox 事务序列）。
func TestOrphanSweepPostgres(t *testing.T) {
	rec := newPGRecorder()
	business := openRecorderPG(rec)
	store := &DeletedAccountStore{Business: business, Now: kitNow}
	rec.script("SELECT accounts.id, accounts.system_account_id", []string{
		"id", "system_account_id", "authorization_instance_authorization_id",
		"authorization_instance_source_account_id", "deleted_at",
		"source_deleted_at", "resource_deleted_at",
	}, [][]driver.Value{
		{"acc-inst", "sys-1", "auth-7", "acc-src", nil, "2026-06-01T00:00:00.000Z", "2026-06-01T00:00:00.000Z"},
	})
	rec.script("SELECT resource_type, resource_id", []string{"resource_type", "resource_id"},
		[][]driver.Value{{"account", "acc-src"}})
	rec.script("WHERE resource_type = 'account' AND resource_id", []string{"id"}, [][]driver.Value{{"auth-7"}})
	// 注意：PG 路径经 Bind 改写为 $n，脚本匹配键不能包含字面 `?`。
	rec.script("SELECT id FROM juhe_business.accounts WHERE deleted_at", []string{"id"}, [][]driver.Value{{"acc-inst"}})
	rec.script("provider_code IN", []string{"id", "config_revision", "dispatch_revision"},
		[][]driver.Value{{"acc-inst", int64(4), int64(6)}})

	orphaned, err := store.orphanSweepPostgres(context.Background(), 10)
	if err != nil {
		t.Fatalf("orphanSweepPostgres: %v", err)
	}
	if len(orphaned) != 1 || orphaned[0] != "acc-inst" {
		t.Fatalf("orphaned = %v", orphaned)
	}
	statements := rec.all()
	// revoke 链：实例授权回收（sources + authorizations UPDATE）必须在事务内。
	var sourcesUpdate, authorizationsUpdate, outboxInsert *recordedStatement
	for index := range statements {
		statement := statements[index]
		switch {
		case strings.Contains(statement.query, "UPDATE juhe_business.resource_authorization_sources") &&
			strings.Contains(statement.query, "account_deleted"):
			sourcesUpdate = &statements[index]
		case strings.Contains(statement.query, "UPDATE juhe_business.resource_authorizations") &&
			strings.Contains(statement.query, "revoked_reason"):
			authorizationsUpdate = &statements[index]
		case strings.Contains(statement.query, "INSERT INTO account_health_jobs_input_outbox"):
			outboxInsert = &statements[index]
		}
	}
	if sourcesUpdate == nil || authorizationsUpdate == nil || outboxInsert == nil {
		t.Fatalf("缺少 revoke/outbox 语句：\n%v\n%v\n%v", sourcesUpdate, authorizationsUpdate, outboxInsert)
	}
	if sourcesUpdate.tx != 1 || authorizationsUpdate.tx != 1 {
		t.Fatalf("revoke 语句应在事务内执行")
	}
	// tombstone outbox：input_version=1（无既有版本行），kind/reason 为 SQL 字面量。
	if fmt.Sprintf("%v", outboxInsert.args[1]) != "acc-inst" ||
		fmt.Sprintf("%v", outboxInsert.args[2]) != "1" ||
		fmt.Sprintf("%v", outboxInsert.args[3]) != "4" || fmt.Sprintf("%v", outboxInsert.args[4]) != "6" {
		t.Fatalf("outbox 参数 = %v", outboxInsert.args)
	}
	if !strings.Contains(outboxInsert.query, "'tombstone'") || !strings.Contains(outboxInsert.query, "'account_deleted'") {
		t.Fatalf("outbox 语句缺少字面 kind/reason：\n%s", outboxInsert.query)
	}
	// 实例账户逻辑删除 + tags/搜索清理语句存在。
	joined := make([]string, 0, len(statements))
	for _, statement := range statements {
		joined = append(joined, statement.query)
	}
	allText := strings.Join(joined, "\n")
	for _, needle := range []string{
		"UPDATE juhe_business.accounts",
		"DELETE FROM juhe_business.account_tag_bindings",
		"DELETE FROM juhe_business.account_name_search_terms",
		"UPDATE juhe_business.resource_authorization_grants",
		"request_quota_hourly_window_scope_bindings",
	} {
		if !strings.Contains(allText, needle) {
			t.Fatalf("缺少语句：%s", needle)
		}
	}
	if rec.begins != 1 || rec.commits != 1 {
		t.Fatalf("事务数 begins=%d commits=%d, 期望单孤儿一个事务", rec.begins, rec.commits)
	}

	// 空孤儿集 → 空列表且无事务。
	emptyRec := newPGRecorder()
	emptyStore := &DeletedAccountStore{Business: openRecorderPG(emptyRec), Now: kitNow}
	orphaned, err = emptyStore.orphanSweepPostgres(context.Background(), 10)
	if err != nil || len(orphaned) != 0 {
		t.Fatalf("空孤儿集 = %v, %v", orphaned, err)
	}
	if emptyRec.begins != 0 {
		t.Fatalf("空孤儿集不应开启事务")
	}
}

// TestRevokeAuthorizationInstancePostgres：空授权 ID 直通；非 account 资源
// 跳过 grants 回收但保留 sources/authorizations 状态更新。
func TestRevokeAuthorizationInstancePostgres(t *testing.T) {
	rec := newPGRecorder()
	business := openRecorderPG(rec)
	store := &DeletedAccountStore{Business: business, Now: kitNow}
	tx, err := business.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := store.revokeAuthorizationInstancePostgres(context.Background(), tx, "", "sys_admin", kitUpdatedAt); err != nil {
		t.Fatalf("空授权 ID 应直通：%v", err)
	}
	if len(rec.all()) != 0 {
		t.Fatalf("空授权 ID 不应产生语句")
	}
	rec.script("SELECT resource_type, resource_id", []string{"resource_type", "resource_id"},
		[][]driver.Value{{"group", "grp-1"}})
	if err := store.revokeAuthorizationInstancePostgres(context.Background(), tx, "auth-8", "sys_admin", kitUpdatedAt); err != nil {
		t.Fatalf("revokeAuthorizationInstancePostgres: %v", err)
	}
	statements := rec.all()
	revokedStatements := 0
	for _, statement := range statements {
		if strings.Contains(statement.query, "UPDATE juhe_business.resource_authorization_sources") {
			revokedStatements++
		}
		if strings.Contains(statement.query, "UPDATE juhe_business.resource_authorizations") &&
			strings.Contains(statement.query, "WHERE id = $3") {
			revokedStatements++
			// 回收 actor 参数。
			if fmt.Sprintf("%v", statement.args[1]) != "sys_admin" {
				t.Fatalf("revoked_by 参数 = %v", statement.args)
			}
		}
	}
	if revokedStatements != 2 {
		t.Fatalf("sources/authorizations UPDATE 数 = %d, 期望 2", revokedStatements)
	}
	for _, statement := range statements {
		if strings.Contains(statement.query, "resource_authorization_grants") {
			t.Fatalf("非 account 资源不应触发 grants 回收：%s", statement.query)
		}
	}
}

// TestLogicallyDeleteAccountsTxSQLite：SQLite 行级逻辑删除 + tombstone
// outbox + 标签/搜索清理。
func TestLogicallyDeleteAccountsTxSQLite(t *testing.T) {
	store, business := newKitDeletedAccountStore(t, nil)
	mustExecKit(t, business, `INSERT INTO accounts (
      id, system_account_id, status, provider_code, type, config_revision, dispatch_revision, created_at, updated_at)
    VALUES ('acc-t','sys-1','active','openai','oauth',4,6,'2026-01-01T00:00:00.000Z','2026-01-01T00:00:00.000Z')`)
	for _, table := range []string{"account_tag_bindings", "account_name_search_terms", "account_name_search_documents"} {
		mustExecKit(t, business, `INSERT INTO `+table+` (id, account_id) VALUES ('r-1','acc-t')`)
	}
	tx, err := business.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	deletedIDs, err := store.logicallyDeleteAccountsTx(context.Background(), tx, []string{"acc-t"}, "sys_admin", kitUpdatedAt)
	if err != nil {
		t.Fatalf("logicallyDeleteAccountsTx: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if len(deletedIDs) != 1 || deletedIDs[0] != "acc-t" {
		t.Fatalf("deletedIDs = %v", deletedIDs)
	}
	var status string
	var schedulable int64
	if err := business.QueryRowContext(context.Background(), `SELECT status, schedulable FROM accounts WHERE id = 'acc-t'`).Scan(&status, &schedulable); err != nil {
		t.Fatalf("read account: %v", err)
	}
	if status != "disabled" || schedulable != 0 {
		t.Fatalf("账户逻辑删除状态不符：status=%q schedulable=%d", status, schedulable)
	}
	if got := mustQueryCountKit(t, business, `SELECT COUNT(*) FROM account_health_jobs_input_outbox
    WHERE account_id = 'acc-t' AND event_kind = 'tombstone'`); got != 1 {
		t.Fatalf("tombstone outbox 应入队")
	}
	if got := mustQueryCountKit(t, business, `SELECT current_version FROM account_health_jobs_input_versions
    WHERE account_id = 'acc-t'`); got != 1 {
		t.Fatalf("版本应初始化为 1")
	}
	for _, table := range []string{"account_tag_bindings", "account_name_search_terms", "account_name_search_documents"} {
		if got := mustQueryCountKit(t, business, `SELECT COUNT(*) FROM `+table); got != 0 {
			t.Fatalf("%s 应被清理", table)
		}
	}
}

// TestEnqueueTombstoneOutboxTx：版本自增走 UPDATE；空账户 ID 报错。
func TestEnqueueTombstoneOutboxTx(t *testing.T) {
	rec := newPGRecorder()
	business := openRecorderPG(rec)
	tx, err := business.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := enqueueTombstoneOutboxTx(context.Background(), tx, business, " ", 1, 2, kitUpdatedAt); err == nil {
		t.Fatalf("空账户 ID 应报错")
	}
	rec.script("SELECT current_version", []string{"current_version"}, [][]driver.Value{{int64(3)}})
	if err := enqueueTombstoneOutboxTx(context.Background(), tx, business, "acc-9", 1, 2, kitUpdatedAt); err != nil {
		t.Fatalf("enqueueTombstoneOutboxTx: %v", err)
	}
	var versionUpdate, outboxInsert *recordedStatement
	for index := range rec.statements {
		statement := rec.statements[index]
		switch {
		case strings.Contains(statement.query, "UPDATE account_health_jobs_input_versions"):
			versionUpdate = &rec.statements[index]
		case strings.Contains(statement.query, "INSERT INTO account_health_jobs_input_outbox"):
			outboxInsert = &rec.statements[index]
		}
	}
	if versionUpdate == nil || outboxInsert == nil {
		t.Fatalf("缺少版本 UPDATE/outbox INSERT：%v %v", versionUpdate, outboxInsert)
	}
	if fmt.Sprintf("%v", versionUpdate.args[0]) != "4" {
		t.Fatalf("nextVersion = %v, 期望 4", versionUpdate.args[0])
	}
	if fmt.Sprintf("%v", outboxInsert.args[2]) != "4" || fmt.Sprintf("%v", outboxInsert.args[3]) != "1" ||
		fmt.Sprintf("%v", outboxInsert.args[4]) != "2" {
		t.Fatalf("outbox 参数 = %v", outboxInsert.args)
	}
}

// TestHasRelatedRecordDataDispatch：PG 模式分派 PG 检查；两个账户 ID 全空时
// 的批量检查链（录制驱动无 SQL 校验，覆盖语句家族）。
func TestHasRelatedRecordDataDispatch(t *testing.T) {
	rec := newPGRecorder()
	store := &DeletedAccountStore{Business: openRecorderPG(rec), Dataset: openRecorderPG(rec)}
	target := &cleanupTarget{
		AccountID: "acc-1", SystemAccountID: "sys-1", AccountIDs: []string{"acc-1"},
		AuthorizationIDs: []string{"auth-1"}, TeamScopeIDs: []string{"acc-1:team-9"},
	}
	hit, err := store.hasRelatedRecordData(context.Background(), target)
	if err != nil || hit {
		t.Fatalf("空录制库应返回 false：%v, %v", hit, err)
	}
	statements := rec.all()
	if len(statements) == 0 {
		t.Fatalf("PG 检查应产生查询")
	}
	joined := make([]string, 0, len(statements))
	for _, statement := range statements {
		joined = append(joined, statement.query)
	}
	allText := strings.Join(joined, "\n")
	for _, needle := range []string{
		"juhe_dataset.account_record_cleanup_targets",
		"juhe_usage.usage_records WHERE account_id = ANY",
		"juhe_usage.usage_records WHERE account_authorization_id = ANY",
		"group_authorization_id = ANY",
		"juhe_dataset.audit_logs",
		"juhe_stats.account_quality_scores",
		"juhe_stats.account_usage_snapshots",
		"juhe_stats.usage_stats_totals",
		"juhe_stats.usage_rank_snapshots",
	} {
		if !strings.Contains(allText, needle) {
			t.Fatalf("缺少 PG 检查查询：%s", needle)
		}
	}
}
