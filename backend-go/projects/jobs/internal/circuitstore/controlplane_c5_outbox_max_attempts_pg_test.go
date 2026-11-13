package circuitstore

// C5 回归（T207 扫描项）：circuit outbox 确定性失败事件经 ReleaseForReplay
// 无限回 pending 重派——applyOutboxEvent 恒败（身份/投影键冲突类硬错误）时
// 每次 claim（attempt_count +1）→ release → available_at_ms=now+delay 循环
// 永不终止。修复：ReleaseForReplay 读出本次 claim 的 attempt_count，达到
// outboxMaxAttempts 即置终态 failed（claim 面清空、acknowledged_at_ms 落
// now、last_error_class 保留）：
//
//   - attempt < outboxMaxAttempts：维持原语义回 pending（available_at_ms 推
//     进、claim 清空、last_error_class 记录错误类）；
//   - attempt >= outboxMaxAttempts（含恰好 = 8 的边界）：置 failed 终态，
//     不再回 pending；
//   - failed 行不被 Claim 选中（claim 只认 pending/processing），由 Cleanup
//     的 ack 保留期臂随 dispatched 行一起物理回收。
//
// 依赖真实 PG 行锁事务（pgtestkit 隔离测试库，JUHE_AI_TEST_EXTERNAL 授权门
// 控，日常回归默认跳过）。fixture 按 kit 约定不复刻 CHECK/FK，failed 行可
// 直接种入；claim 链路行必须种在主投影键（ProjectionKey）下才会被 Claim 命中。

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/opsjobs"
)

// c5InsertPendingRow 种一行 pending 态 outbox 事件（attempt_count 可直取任意
// 值，模拟已历经 N 次重派），种在主投影键下供真实 claim 链路认领。
func c5InsertPendingRow(t *testing.T, db *sql.DB, accountID, eventID, dedupeKey string, attemptCount int64) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO juhe_business.account_circuit_outbox (
		event_id, projection_key, dedupe_key, event_type, account_id, account_runtime_key,
		circuit_scope_key, incident_id, transition_id, dispatch_revision, generation, ledger_revision,
		status, available_at_ms, claim_token, claimed_by, claim_until_ms, attempt_count, last_error_class,
		created_at_ms, updated_at_ms
	) VALUES ($1, $2, $3, 'incident_changed', $5, $5,
		'scope-c5-'||$1, 'incident-c5-'||$1, 'tr-c5-'||$1, 9, 1, 1,
		'pending', to_timestamp(0), NULL, NULL, NULL, $4, NULL,
		to_timestamp(0), to_timestamp(0))`,
		eventID, ProjectionKey, dedupeKey, attemptCount, accountID); err != nil {
		t.Fatalf("种 outbox 行 %s 失败: %v", eventID, err)
	}
}

// c5ClaimOne 认领并返回目标事件（断言恰好命中）。
func c5ClaimOne(t *testing.T, repo *ControlPlaneRepo, eventID string, nowMS int64) opsjobs.OutboxEvent {
	t.Helper()
	claims, err := repo.Claim(context.Background(), "c5-owner", nowMS, 60_000, 10)
	if err != nil {
		t.Fatalf("Claim 失败: %v", err)
	}
	for _, event := range claims {
		if event.EventID == eventID {
			return event
		}
	}
	t.Fatalf("Claim 未命中目标事件 %s: 认领 %d 行", eventID, len(claims))
	return opsjobs.OutboxEvent{}
}

// c5ReadOutbox 读回事件行状态面。
func c5ReadOutbox(t *testing.T, db *sql.DB, eventID string) (status string, attempt int64, claimToken sql.NullString, acked sql.NullInt64, lastErrorClass sql.NullString, availableAtMS int64) {
	t.Helper()
	var ackedTime sql.NullTime
	var availableAt time.Time
	err := db.QueryRow(`SELECT status, attempt_count, claim_token, acknowledged_at_ms, last_error_class, available_at_ms
		FROM juhe_business.account_circuit_outbox WHERE event_id=$1`, eventID).
		Scan(&status, &attempt, &claimToken, &ackedTime, &lastErrorClass, &availableAt)
	if err != nil {
		t.Fatalf("读回 outbox 行 %s 失败: %v", eventID, err)
	}
	if ackedTime.Valid {
		acked = sql.NullInt64{Int64: ackedTime.Time.UnixMilli(), Valid: true}
	}
	availableAtMS = availableAt.UnixMilli()
	return
}

// TestC5ReleaseForReplayAttemptOverLimitSetsFailedTerminal 钉住超限臂：第 8
// 次认领（attempt_count=8 == outboxMaxAttempts）失败后释放必须置终态
// failed——不回 pending、acknowledged_at_ms 落 now、claim 面清空、
// last_error_class 保留；随后同参数 Claim 不得再选中该行。
func TestC5ReleaseForReplayAttemptOverLimitSetsFailedTerminal(t *testing.T) {
	repo, db := dd3b5OpenRepo(t, "acc-c5-a")
	const eventID = "evt-c5-over"
	const nowMS = int64(7_100_000_000)

	c5InsertPendingRow(t, db, "acc-c5-a", eventID, "incident:tr-c5-over", 7)
	event := c5ClaimOne(t, repo, eventID, nowMS) // attempt_count: 7 -> 8

	if err := repo.ReleaseForReplay(context.Background(), event, "incident_cas_stale", nowMS, 5_000); err != nil {
		t.Fatalf("超限释放必须成功置终态: %v", err)
	}
	status, attempt, claimToken, acked, lastErrorClass, _ := c5ReadOutbox(t, db, eventID)
	if status != "failed" {
		t.Fatalf("attempt=%d 达 outboxMaxAttempts=%d 必须置 failed 终态: status=%q", attempt, outboxMaxAttempts, status)
	}
	if attempt != 8 {
		t.Fatalf("attempt_count 应为 8（7+1）: got %d", attempt)
	}
	if claimToken.Valid {
		t.Fatalf("failed 行必须清空 claim: claim=%v", claimToken)
	}
	if !acked.Valid {
		t.Fatalf("failed 行必须带 acknowledged_at_ms")
	}
	if acked.Int64 != nowMS {
		t.Fatalf("acknowledged_at_ms 应落 now(%d): got %d", nowMS, acked.Int64)
	}
	if !lastErrorClass.Valid || lastErrorClass.String != "incident_cas_stale" {
		t.Fatalf("failed 行必须保留 last_error_class: got %v", lastErrorClass)
	}

	// 终态 failed 行不再被 claim/重放：同参数 Claim 返回空。
	again, err := repo.Claim(context.Background(), "c5-owner", nowMS+60_000, 60_000, 10)
	if err != nil {
		t.Fatalf("终态后 Claim 失败: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("failed 行不得被再次认领: %+v", again)
	}
}

// TestC5ReleaseForReplayUnderLimitRequeuesPending 钉住未超限臂：attempt_count
// 未达上限时维持原语义——回 pending、available_at_ms=now+retryDelay、claim
// 清空、last_error_class 记录错误类；到期后可再次认领（重派继续）。
func TestC5ReleaseForReplayUnderLimitRequeuesPending(t *testing.T) {
	repo, db := dd3b5OpenRepo(t, "acc-c5-b")
	const eventID = "evt-c5-under"
	const nowMS = int64(7_200_000_000)

	c5InsertPendingRow(t, db, "acc-c5-b", eventID, "incident:tr-c5-under", 0)
	event := c5ClaimOne(t, repo, eventID, nowMS) // attempt_count: 0 -> 1

	if err := repo.ReleaseForReplay(context.Background(), event, "reconcile_read_failed", nowMS, 30_000); err != nil {
		t.Fatalf("未超限释放必须成功: %v", err)
	}
	status, attempt, claimToken, acked, lastErrorClass, availableAtMS := c5ReadOutbox(t, db, eventID)
	if status != "pending" || attempt != 1 {
		t.Fatalf("未超限必须回 pending 且 attempt=1: status=%q attempt=%d", status, attempt)
	}
	if claimToken.Valid || acked.Valid {
		t.Fatalf("pending 行必须清空 claim 且不带 acknowledged_at_ms: claim=%v acked=%v", claimToken, acked)
	}
	if !lastErrorClass.Valid || lastErrorClass.String != "reconcile_read_failed" {
		t.Fatalf("释放必须记录 last_error_class: got %v", lastErrorClass)
	}
	if availableAtMS != nowMS+30_000 {
		t.Fatalf("available_at_ms 应为 now+retryDelay(%d): got %d", nowMS+30_000, availableAtMS)
	}

	// 到期后重派链路继续：同参数 Claim 再次命中，claim token 换新。
	reclaimed := c5ClaimOne(t, repo, eventID, nowMS+30_000)
	if reclaimed.ClaimToken == "" || reclaimed.ClaimToken == event.ClaimToken {
		t.Fatalf("重派认领必须换新 claim token: got %q", reclaimed.ClaimToken)
	}
}

// TestC5CleanupRetiresAgedFailedOutbox 钉住清理面感知：failed 终态行按
// acknowledged_at_ms 与 dispatched 行同走保留期回收（超龄删除、保留期内保
// 留、幂等重跑零删除），不影响 dispatched 行的回收判定。
func TestC5CleanupRetiresAgedFailedOutbox(t *testing.T) {
	repo, db := dd3b5OpenRepo(t, "acc-c5-c")
	ctx := context.Background()
	const nowMS = int64(7_300_000_000)
	retention := circuitDispatchedOutboxRetentionMS
	overdue := nowMS - retention - 3_600_000
	fresh := nowMS - 3_600_000

	insertTerminalRow := func(eventID, status string, ackedMS int64) {
		t.Helper()
		if _, err := db.Exec(`INSERT INTO juhe_business.account_circuit_outbox (
			event_id, projection_key, dedupe_key, event_type, account_id, account_runtime_key,
			circuit_scope_key, incident_id, transition_id, dispatch_revision, generation, ledger_revision,
			status, available_at_ms, attempt_count, last_error_class, acknowledged_at_ms,
			created_at_ms, updated_at_ms
		) VALUES ($1, $2, 'dedupe-'||$1, 'incident_changed', 'acc-c5-c', 'acc-c5-c',
			'scope-c5-'||$1, 'incident-c5-'||$1, 'tr-c5-'||$1, 9, 1, 1,
			$3, to_timestamp(0), 9, 'incident_cas_stale', $4,
			to_timestamp(0), to_timestamp(0))`,
			eventID, ProjectionKey, status, time.UnixMilli(ackedMS).UTC()); err != nil {
			t.Fatalf("种终态行 %s 失败: %v", eventID, err)
		}
	}
	insertTerminalRow("evt-c5-failed-overdue", "failed", overdue)
	insertTerminalRow("evt-c5-failed-fresh", "failed", fresh)
	insertTerminalRow("evt-c5-dispatched-overdue", "dispatched", overdue)

	result, err := repo.Cleanup(ctx, nowMS)
	if err != nil {
		t.Fatalf("Cleanup 失败: %v", err)
	}
	if result.DeletedOutbox != 2 {
		t.Fatalf("超龄 failed 与超龄 dispatched 行应各删 1: %+v", result)
	}
	if result.DeletedIncidents != 0 {
		t.Fatalf("无 incident 行时不得误删: %+v", result)
	}
	var freshStatus string
	if err := db.QueryRow(`SELECT status FROM juhe_business.account_circuit_outbox WHERE event_id='evt-c5-failed-fresh'`).Scan(&freshStatus); err != nil || freshStatus != "failed" {
		t.Fatalf("保留期内 failed 行必须原样保留: status=%q err=%v", freshStatus, err)
	}

	again, err := repo.Cleanup(ctx, nowMS)
	if err != nil {
		t.Fatalf("重跑 Cleanup 失败: %v", err)
	}
	if again.DeletedOutbox != 0 || again.DeletedIncidents != 0 {
		t.Fatalf("重跑必须幂等零删除: %+v", again)
	}
}
