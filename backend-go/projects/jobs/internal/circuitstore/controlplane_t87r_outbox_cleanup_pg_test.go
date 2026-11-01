package circuitstore

// T87r 回归（2026-11-01）：dispatched outbox 行永生无界累积，清理接线收归
// jobs 侧 ControlPlaneRepo.Cleanup 第三臂（gateway 写侧 Store.Cleanup 死代码
// 同批删除，T87r 前全仓无生产清理路径）。本文件钉住 outbox 臂删除条件与
// incident 两臂守卫的完整语义：
//
//   - acknowledged_at_ms 超过 circuitDispatchedOutboxRetentionMS 的
//     dispatched 行删除；删除不做 event_type 过滤（incident_changed 与
//     dispatch_revision_changed 同删，DD6-S4 语义：dispatch 持久事实在
//     accounts.dispatch_revision，同 tid 重试由 CAS stale 臂兜底）；
//   - acknowledged_at_ms 恰等于保留期边界的行删除（<=）；保留期内
//     dispatched 行、pending 行、processing 行全部保留；
//   - incident CLOSED 臂照常：CLOSED 到期且 outbox 全 dispatched 的行删除，
//     带 pending outbox 的 CLOSED 到期行被 noPendingOutbox 守卫保留；
//     dispatched outbox 行的存在（无论新旧）不影响该守卫；
//   - 幂等与推进：同参数重跑删除计数为 0、行集合不变；时间推进越过保留期
//     后，原保留的 dispatched 行被下一轮回收。
//
// Cleanup 依赖真实 PG 事务语义（pgtestkit 隔离测试库，JUHE_AI_TEST_EXTERNAL
// 授权门控，日常回归默认跳过）。fixture 按 kit 约定不复刻 CHECK/FK。

import (
	"context"
	"testing"
)

// t87rNow 是各用例共用的 Cleanup nowMS 基准（任意远期常数，ack 时间相对其
// 推导，避免依赖墙钟）。
const t87rNow = int64(7_000_000_000)

// TestT87RCleanupRetiresAgedDispatchedOutbox 钉住 outbox 臂删除条件：超龄
// dispatched 行（两种 event_type）与边界行删除；保留期内 dispatched 行与
// pending/processing 行保留；重跑幂等。
func TestT87RCleanupRetiresAgedDispatchedOutbox(t *testing.T) {
	repo, db := dd3b5OpenRepo(t, "acc-t87r-a")
	ctx := context.Background()
	retention := circuitDispatchedOutboxRetentionMS
	overdue := t87rNow - retention - 3_600_000 // 超龄 1 小时
	boundary := t87rNow - retention            // 恰等于保留期边界（<=）
	fresh := t87rNow - 3_600_000               // 保留期内

	insertOutbox := func(eventID, dedupeKey, eventType, status string, ackedMS *int64) {
		t.Helper()
		var scopeKey, incidentID any
		var generation, ledgerRevision any
		if eventType == "incident_changed" {
			scopeKey = "scope-t87r-" + eventID
			incidentID = "incident-" + eventID
			generation = int64(1)
			ledgerRevision = int64(1)
		}
		if _, err := db.Exec(`INSERT INTO juhe_business.account_circuit_outbox (
			event_id, projection_key, dedupe_key, event_type, account_id, account_runtime_key,
			circuit_scope_key, incident_id, transition_id, dispatch_revision, generation, ledger_revision,
			status, available_at_ms, claim_token, claimed_by, claim_until_ms, attempt_count, acknowledged_at_ms,
			created_at_ms, updated_at_ms
		) VALUES ($1, 'projection:circuit', $2, $3, 'acc-t87r-a', 'acc-t87r-a',
			$4, $5, 'tr-'||$1, 9, $6, $7,
			$8, 0, NULL, NULL, NULL, 0, $9,
			0, 0)`,
			eventID, dedupeKey, eventType, scopeKey, incidentID, generation, ledgerRevision, status, ackedMS); err != nil {
			t.Fatalf("插入 outbox 行 %s 失败: %v", eventID, err)
		}
	}

	// 期望删除：超龄 incident_changed、超龄 dispatch_revision_changed、边界行。
	insertOutbox("evt-obsolete-inc", "dedupe-obsolete-inc", "incident_changed", "dispatched", &overdue)
	insertOutbox("evt-obsolete-disp", "dedupe-obsolete-disp", "dispatch_revision_changed", "dispatched", &overdue)
	insertOutbox("evt-boundary", "dedupe-boundary", "incident_changed", "dispatched", &boundary)
	// 期望保留：保留期内 dispatched、pending、processing。
	insertOutbox("evt-fresh", "dedupe-fresh", "incident_changed", "dispatched", &fresh)
	insertOutbox("evt-pending", "dedupe-pending", "incident_changed", "pending", nil)
	processingAck := overdue // processing 行即使带遗留 ack 值也必须保留
	insertOutbox("evt-processing", "dedupe-processing", "dispatch_revision_changed", "processing", &processingAck)

	result, err := repo.Cleanup(ctx, t87rNow)
	if err != nil {
		t.Fatalf("Cleanup 失败: %v", err)
	}
	if result.DeletedOutbox != 3 {
		t.Fatalf("超龄 dispatched 行应删 3（含边界行与两种 event_type）: %+v", result)
	}
	if result.DeletedIncidents != 0 {
		t.Fatalf("无 incident 行时不得误删: %+v", result)
	}

	var remaining int
	if err := db.QueryRow(`SELECT count(*) FROM juhe_business.account_circuit_outbox`).Scan(&remaining); err != nil {
		t.Fatalf("统计剩余 outbox 行失败: %v", err)
	}
	if remaining != 3 {
		t.Fatalf("应保留 fresh/pending/processing 三行: got %d", remaining)
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM juhe_business.account_circuit_outbox WHERE event_id='evt-fresh'`).Scan(&status); err != nil || status != "dispatched" {
		t.Fatalf("保留期内 dispatched 行必须原样保留: status=%q err=%v", status, err)
	}

	// 幂等：同参数重跑零删除、行集合不变。
	again, err := repo.Cleanup(ctx, t87rNow)
	if err != nil {
		t.Fatalf("重跑 Cleanup 失败: %v", err)
	}
	if again.DeletedOutbox != 0 || again.DeletedIncidents != 0 {
		t.Fatalf("重跑必须幂等零删除: %+v", again)
	}
	if err := db.QueryRow(`SELECT count(*) FROM juhe_business.account_circuit_outbox`).Scan(&remaining); err != nil {
		t.Fatalf("重跑后统计失败: %v", err)
	}
	if remaining != 3 {
		t.Fatalf("重跑后行集合不得变化: got %d", remaining)
	}
}

// TestT87RCleanupIncidentArmsUnchangedByOutboxArm 钉住 incident 两臂与
// outbox 臂互不干扰：CLOSED 到期且 outbox 全 dispatched 的 incident 行删除，
// 其保留期内 dispatched outbox 行原地保留；带 pending outbox 的 CLOSED 到期
// 行被 noPendingOutbox 守卫保留；活动（OPEN）行保留；时间推进越过保留期后
// 原 fresh dispatched 行被下一轮回收。
func TestT87RCleanupIncidentArmsUnchangedByOutboxArm(t *testing.T) {
	repo, db := dd3b5OpenRepo(t, "acc-t87r-b")
	ctx := context.Background()

	insertIncident := func(scopeKey, state string, retainedUntilMS *int64) {
		t.Helper()
		if _, err := db.Exec(`INSERT INTO juhe_business.account_circuit_incidents (
			circuit_scope_key, account_id, account_runtime_key, scope_kind, incident_id,
			child_incident_ids_json, state, generation, dispatch_revision, ledger_revision,
			projected_ledger_revision, transition_id, retained_until_ms, created_at_ms, updated_at_ms
		) VALUES ($1, 'acc-t87r-b', 'acc-t87r-b', 'account', 'incident-'||$1,
			'[]', $2, 1, 9, 1,
			1, 'tr-'||$1, $3, 0, 0)`,
			scopeKey, state, retainedUntilMS); err != nil {
			t.Fatalf("插入 incident 行 %s 失败: %v", scopeKey, err)
		}
	}
	insertOutbox := func(eventID, scopeKey, status string, ackedMS *int64) {
		t.Helper()
		if _, err := db.Exec(`INSERT INTO juhe_business.account_circuit_outbox (
			event_id, projection_key, dedupe_key, event_type, account_id, account_runtime_key,
			circuit_scope_key, incident_id, transition_id, dispatch_revision, generation, ledger_revision,
			status, available_at_ms, attempt_count, acknowledged_at_ms, created_at_ms, updated_at_ms
		) VALUES ($1, 'projection:circuit', 'dedupe-'||$1, 'incident_changed', 'acc-t87r-b', 'acc-t87r-b',
			$2, 'incident-'||$2, 'tr-'||$2, 9, 1, 1,
			$3, 0, 0, $4, 0, 0)`,
			eventID, scopeKey, status, ackedMS); err != nil {
			t.Fatalf("插入 outbox 行 %s 失败: %v", eventID, err)
		}
	}

	retainedExpired := t87rNow - 1 // CLOSED 保留期已到期
	// expired-dispatched：CLOSED 到期 + outbox 全 dispatched（fresh，保留期内）→ incident 删。
	insertIncident("scope-t87r-expired-dispatched", "CLOSED", &retainedExpired)
	fresh := t87rNow - 3_600_000
	insertOutbox("evt-b-fresh", "scope-t87r-expired-dispatched", "dispatched", &fresh)
	// expired-pending：CLOSED 到期 + 带 pending outbox → 守卫保留。
	insertIncident("scope-t87r-expired-pending", "CLOSED", &retainedExpired)
	insertOutbox("evt-b-pending", "scope-t87r-expired-pending", "pending", nil)
	// active：活动行 → 保留。
	insertIncident("scope-t87r-active", "OPEN", nil)
	insertOutbox("evt-b-active", "scope-t87r-active", "pending", nil)

	result, err := repo.Cleanup(ctx, t87rNow)
	if err != nil {
		t.Fatalf("Cleanup 失败: %v", err)
	}
	if result.DeletedIncidents != 1 {
		t.Fatalf("应只删 outbox 全 dispatched 的 CLOSED 到期行: %+v", result)
	}
	if result.DeletedOutbox != 0 {
		t.Fatalf("保留期内 dispatched 行不得删除: %+v", result)
	}

	var state string
	if err := db.QueryRow(`SELECT state FROM juhe_business.account_circuit_incidents WHERE circuit_scope_key='scope-t87r-expired-pending'`).Scan(&state); err != nil || state != "CLOSED" {
		t.Fatalf("带 pending outbox 的 CLOSED 到期行必须被守卫保留: state=%q err=%v", state, err)
	}
	var activeState string
	if err := db.QueryRow(`SELECT state FROM juhe_business.account_circuit_incidents WHERE circuit_scope_key='scope-t87r-active'`).Scan(&activeState); err != nil || activeState != "OPEN" {
		t.Fatalf("活动行必须保留: state=%q err=%v", activeState, err)
	}
	var remainingOutbox int
	if err := db.QueryRow(`SELECT count(*) FROM juhe_business.account_circuit_outbox`).Scan(&remainingOutbox); err != nil {
		t.Fatalf("统计 outbox 行失败: %v", err)
	}
	if remainingOutbox != 3 {
		t.Fatalf("outbox 三行全部保留: got %d", remainingOutbox)
	}

	// 推进保留期后重跑：expired-dispatched 的 fresh outbox 行转为超龄删除，
	// incident 已删行不复活，守卫行与活动行仍保留。
	advanced := t87rNow + circuitDispatchedOutboxRetentionMS
	again, err := repo.Cleanup(ctx, advanced)
	if err != nil {
		t.Fatalf("推进时间重跑失败: %v", err)
	}
	if again.DeletedOutbox != 1 {
		t.Fatalf("超龄后的 fresh dispatched 行应删除: %+v", again)
	}
	if again.DeletedIncidents != 0 {
		t.Fatalf("守卫行与活动行不得删除: %+v", again)
	}
	var afterAdvanced int
	if err := db.QueryRow(`SELECT count(*) FROM juhe_business.account_circuit_outbox WHERE event_id='evt-b-fresh'`).Scan(&afterAdvanced); err != nil || afterAdvanced != 0 {
		t.Fatalf("超龄 dispatched 行应物理消失: count=%d err=%v", afterAdvanced, err)
	}
}
