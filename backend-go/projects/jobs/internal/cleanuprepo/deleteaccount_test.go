package cleanuprepo

import (
	"context"
	"database/sql/driver"
	"fmt"
	"strings"
	"testing"
)

// deleteaccount.go（过期逻辑删除物理清理）的语义测试：SQLite 真库覆盖
// 候选列表、目标构建（关联实例/授权/团队 scope/grants）、物理删除主链与
// 孤儿授权实例扫尾（SQLite 臂真实数据 + 语句级失败注入）；PG 路径用录制
// 驱动覆盖孤儿扫尾、授权回收与 tombstone outbox。

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
	rec.script("vendor_code IN", []string{"id", "config_revision", "dispatch_revision"},
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
