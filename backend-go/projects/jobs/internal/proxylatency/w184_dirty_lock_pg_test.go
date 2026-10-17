package proxylatency

// w184_dirty_lock_pg_test.go（录制契约部分）用录制驱动锁定生产代码契约：
// PG 方言下凡是将要 UPDATE proxy_profiles 的投影事务（projectStored /
// ProjectManualNoTargets / ProjectManualOutbound），advisory 7001001 必须先于
// receipt/FOR UPDATE 行语句与 CAS UPDATE 出现；SQLite 方言不产生 advisory。
//
// 历史注记（PLAN-20261017T035500000Z）：本文件原有的真实 PG 死锁复现门禁
// （TestW184ProxyDirtyAdvisoryLockOrderOnW1Cover，BUG-0237 三方死锁环修复前/
// 修复后双阶段复现）随 account_list_availability_proxies 触发器移除而失去
// 复现前提（proxy UPDATE 不再经触发器中途取 7001001），已一并拆除；J1 侧
// 双键锁序契约仍由 accounthealth/w184_projection_lockorder_test.go 的录制
// 测试锁定。J3a 事务保留先取 7001001（保守串行化 + 事务级等待有界），是否
// 可随触发器移除一并撤销见问题-0313 后续记录跟进项。

import (
	"context"
	"database/sql/driver"
	"strings"
	"testing"
	"time"
)

// w184StatementIndex 返回录制流中第一条包含 match 的语句下标（-1 未找到）。
func w184StatementIndex(statements []wfStatement, match string) int {
	for index, statement := range statements {
		if strings.Contains(statement.query, match) {
			return index
		}
	}
	return -1
}

// TestW184ResultProjectorTxsTakeDirtyLockFirst 用录制驱动锁定生产代码契约：
// PG 方言下凡是将要 UPDATE proxy_profiles 的投影事务（projectStored /
// ProjectManualNoTargets / ProjectManualOutbound），advisory 7001001 必须先于
// receipt/FOR UPDATE 行语句与 CAS UPDATE 出现；SQLite 方言不产生 advisory。
func TestW184ResultProjectorTxsTakeDirtyLockFirst(t *testing.T) {
	const advisoryQuery = "SELECT pg_advisory_xact_lock($1)"
	const fenceQuery = "FROM juhe_business.proxy_profiles WHERE id=$1 FOR UPDATE"
	ctx := context.Background()

	// projectStored（Drain/ProjectOutcome 共用的投影事务形状）。
	func(t *testing.T) {
		rec := newWFRecorder()
		db := wfOpenRecorderDB(t, rec)
		projector := wfNewPGProjector(db)
		observedAt := wfProjBase.Add(2 * time.Second)
		outcome := Outcome{
			OutcomeID: "outcome-w184-1", RequestID: "request-w184-1", ProxyID: "p-w184",
			ObservedAt: observedAt, InputVersion: 1, ConfigRevision: wfProjRevision, Trigger: TriggerPeriodic,
			OwnerFenceToken: 1, ProxyFenceToken: 1, OverallStatus: OverallPassed,
			Items: []ItemResult{{Provider: "gpt", ProfileID: "profile-gpt", Status: ItemPassed, HTTPStatus: 200, LatencyMS: 12, Outcome: OutcomeSuccess}},
		}
		rec.script("FROM juhe_business.proxy_latency_projection_receipts WHERE outcome_id=$1 FOR UPDATE", nil, nil)
		rec.script(fenceQuery, []string{"id", "updated_at", "last_tested_at"}, [][]driver.Value{{outcome.ProxyID, wfProjRevision, nil}})
		rec.scriptExec("UPDATE juhe_business.proxy_profiles SET test_status=$1", 1)
		rec.scriptExec("INSERT INTO juhe_business.proxy_latency_projection_receipts", 1)
		result, err := projector.projectStored(ctx, StoredOutcome{StoredAt: wfProjBase, Outcome: outcome}, false)
		if err != nil || result.Disposition != ProjectionApplied {
			t.Fatalf("录制投影结果=%+v err=%v", result, err)
		}
		statements := rec.all()
		if !strings.Contains(statements[0].query, "set_config('lock_timeout'") {
			t.Fatalf("projectStored 事务首语句必须是取锁等待上限事务级 set_config lock_timeout（BUG-0313），实际: %q", statements[0].query)
		}
		advisory := w184StatementIndex(statements, advisoryQuery)
		if advisory != 1 {
			t.Fatalf("projectStored 事务首条取锁语句必须是 advisory 7001001，实际下标=%d 语句=%q", advisory, statements[1].query)
		}
		if len(statements[1].args) != 1 || statements[1].args[0] != int64(7001001) {
			t.Fatalf("advisory 参数必须是 7001001，实际: %v", statements[1].args)
		}
		for _, required := range []string{fenceQuery, "UPDATE juhe_business.proxy_profiles SET test_status=$1"} {
			if index := w184StatementIndex(statements, required); index < 0 || index < advisory {
				t.Fatalf("projectStored 中 %q 必须在 advisory 之后，下标=%d advisory=%d", required, index, advisory)
			}
		}
	}(t)

	// ProjectManualNoTargets。
	func(t *testing.T) {
		rec := newWFRecorder()
		db := wfOpenRecorderDB(t, rec)
		projector := wfNewPGProjector(db)
		rec.script(fenceQuery, []string{"id", "updated_at", "last_tested_at"}, [][]driver.Value{{"p-w184", wfProjRevision, nil}})
		rec.scriptExec("UPDATE juhe_business.proxy_profiles SET test_status=$1", 1)
		request := ManualRequest{SchemaVersion: 1, ProxyID: "p-w184", ProxyName: "w184", ConfigRevision: wfProjRevision, ProxyType: "http", ProxyHost: "127.0.0.1", ProxyPort: 8080}
		result, err := projector.ProjectManualNoTargets(ctx, request, wfProjBase)
		if err != nil || result.Disposition != ProjectionApplied {
			t.Fatalf("录制 no-targets 结果=%+v err=%v", result, err)
		}
		statements := rec.all()
		if !strings.Contains(statements[0].query, "set_config('lock_timeout'") {
			t.Fatalf("ProjectManualNoTargets 事务首语句必须是事务级 set_config lock_timeout（BUG-0313），实际: %q", statements[0].query)
		}
		if advisory := w184StatementIndex(statements, advisoryQuery); advisory != 1 {
			t.Fatalf("ProjectManualNoTargets 事务首条取锁语句必须是 advisory 7001001，实际下标=%d", advisory)
		}
		if fence := w184StatementIndex(statements, fenceQuery); fence != 2 {
			t.Fatalf("ProjectManualNoTargets 的 FOR UPDATE 必须紧跟 advisory 之后，下标=%d", fence)
		}
	}(t)

	// ProjectManualOutbound（CAS 快路径也必须持锁）。
	func(t *testing.T) {
		rec := newWFRecorder()
		db := wfOpenRecorderDB(t, rec)
		projector := wfNewPGProjector(db)
		rec.scriptExec("SET outbound_ip=$1,outbound_region=$2", 1)
		outcome := Outcome{ProxyID: "p-w184", ConfigRevision: wfProjRevision, ObservedAt: wfProjBase}
		if err := projector.ProjectManualOutbound(ctx, outcome, "1.2.3.4", "JP"); err != nil {
			t.Fatalf("录制 outbound 失败: %v", err)
		}
		statements := rec.all()
		if !strings.Contains(statements[0].query, "set_config('lock_timeout'") {
			t.Fatalf("ProjectManualOutbound 事务首语句必须是事务级 set_config lock_timeout（BUG-0313），实际: %q", statements[0].query)
		}
		if advisory := w184StatementIndex(statements, advisoryQuery); advisory != 1 {
			t.Fatalf("ProjectManualOutbound 事务首条取锁语句必须是 advisory 7001001，实际下标=%d", advisory)
		}
		if cas := w184StatementIndex(statements, "SET outbound_ip=$1,outbound_region=$2"); cas != 2 {
			t.Fatalf("outbound CAS 必须紧跟 advisory 之后，下标=%d", cas)
		}
	}(t)

	// SQLite 方言 no-op：不产生任何 pg_advisory 语句。
	func(t *testing.T) {
		rec := newWFRecorder()
		db := wfOpenRecorderDB(t, rec)
		projector := &ResultProjector{business: db, mode: StoreSQLite, cfg: ResultProjectorConfig{Now: func() time.Time { return wfProjBase }}}
		rec.scriptExec("SET outbound_ip=?,outbound_region=?", 1)
		outcome := Outcome{ProxyID: "p-w184", ConfigRevision: wfProjRevision, ObservedAt: wfProjBase}
		if err := projector.ProjectManualOutbound(ctx, outcome, "1.2.3.4", "JP"); err != nil {
			t.Fatalf("SQLite outbound 失败: %v", err)
		}
		for _, statement := range rec.all() {
			if strings.Contains(statement.query, "pg_advisory") {
				t.Fatalf("SQLite 方言不应执行 advisory 语句: %q", statement.query)
			}
		}
	}(t)
}
