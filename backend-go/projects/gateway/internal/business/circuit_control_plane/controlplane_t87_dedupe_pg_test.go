package circuitcontrolplane

// T87 回归（2026-10-31，R4 登记 A 类即修）：gateway 写侧 circuit dedupe 行永生
// 毒化，与 jobs 侧 DD3-B5 同构。
//
// 缺陷：CompareAndSetIncident 的 outbox dedupe 查询无 status 过滤——gateway
// Cleanup 与 jobs Cleanup 都只删 outbox 全部 dispatched 的 incident 行
// （dispatched outbox 行永生、gateway 写侧 Cleanup 无生产 caller），incident
// 行删除后同 transition_id 重试仍 dedupe 命中 dispatched 行 → 幂等臂读不到
// incident 行 → "deduplicated incident receipt has no incident" 硬错误恒败；
// 即使排除 dispatched，插入臂纯 INSERT 也会撞 UNIQUE(projection_key,
// dedupe_key) 23505（jobs 侧真实 PG 复现）。
//
// 修复（契约模板 jobs internal/circuitstore/controlplane.go DD3-B5）：
// incident 臂 dedupe 查询排除 dispatched 行；incident_changed 插入臂
// ON CONFLICT 冲突重置（status 回 pending、attempt/claim/ack 清空）+
// identity 守卫（身份不匹配 0 行受影响 → ErrIdentityReplay）。dispatch 推进
// 臂保持 dispatched 行作幂等证据（accounts.dispatch_revision 永续，排除会
// 使同 tid 重试二次推进 revision），由
// TestT87DispatchAdvanceKeepsDispatchedRowAsEvidence 钉住。
//
// CompareAndSetIncident 依赖行锁事务，只有真实 PG 可验证（pgtestkit 隔离
// 测试库，JUHE_AI_TEST_EXTERNAL 授权门控，日常回归默认跳过）。fixture 按
// kit 约定不复刻 CHECK/FK，保留 PK/UNIQUE（ON CONFLICT 与 outbox 去重依赖）。

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

// t87OpenStore 打开隔离测试库（wkPGStoreLabeled 已种 a1@revision=1）并种入
// 第二账户 a2（dispatch_revision=1），供 identity 复用臂使用。
func t87OpenStore(t *testing.T) (*Store, *sql.DB) {
	t.Helper()
	s, db := wkPGStoreLabeled(t, "t87", OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	if _, err := db.Exec(`INSERT INTO ` + defaultBusinessSchema + `.accounts(id,dispatch_revision,circuit_projection_revision) VALUES ('a2',1,0)`); err != nil {
		t.Fatalf("种子 a2 失败: %v", err)
	}
	return s, db
}

// t87DispatchOutbox 把指定 dedupe 行置为 dispatched（模拟 Cleanup 只删
// incident 行后留下的永生 dispatched outbox 行）。
func t87DispatchOutbox(t *testing.T, db *sql.DB, dedupeKey string) {
	t.Helper()
	if _, err := db.Exec(`UPDATE ` + defaultBusinessSchema + `.account_circuit_outbox SET status='dispatched', acknowledged_at_ms=2000 WHERE dedupe_key='` + dedupeKey + `'`); err != nil {
		t.Fatalf("置 dispatched 失败: %v", err)
	}
}

// TestT87DispatchedOutboxMissingIncidentRetriesViaInsert 臂钉住毒化场景修复：
// incident 行被 Cleanup 删除（其 outbox 行已全部 dispatched、行永生）后，同
// transition_id 重试不再命中 dispatched 行作幂等回放证据（修复前恒返回
// "deduplicated incident receipt has no incident" 硬错误），而是走插入臂
// applied；插入臂对永生 dispatched 行占据的 (projection_key, dedupe_key)
// 唯一键冲突重置复用（status 回 pending 重新派发，供重建 incident 行投影；
// 纯 INSERT 在此撞 23505）。
func TestT87DispatchedOutboxMissingIncidentRetriesViaInsert(t *testing.T) {
	s, db := t87OpenStore(t)
	ctx := context.Background()
	input := wkBaseIncident("scope-t87-a", "a1", 1)
	if result, err := s.CompareAndSetIncident(ctx, input); err != nil || result.Status != "applied" {
		t.Fatalf("第一轮 CAS = %+v err=%v", result, err)
	}

	// 模拟 Cleanup 后状态：outbox 行 dispatch 完成、incident 行物理删除。
	t87DispatchOutbox(t, db, "incident:tr-scope-t87-a")
	if _, err := db.Exec(`DELETE FROM ` + defaultBusinessSchema + `.account_circuit_incidents WHERE circuit_scope_key='scope-t87-a'`); err != nil {
		t.Fatalf("删除 incident 行失败: %v", err)
	}

	retry, err := s.CompareAndSetIncident(ctx, input)
	if err != nil {
		t.Fatalf("dispatched 行存在 + incident 缺失不得硬错误（修复前恒败）: %v", err)
	}
	if retry.Status != "applied" {
		t.Fatalf("同 tid 重试必须走插入臂 applied: %+v", retry)
	}
	if retry.Incident == nil || retry.Incident.LedgerRevision != 1 {
		t.Fatalf("重试必须插入新行（ledger_revision 从 1 重启）: %+v", retry.Incident)
	}
	// 冲突重置复用：dedupe 行回 pending、claim 面与 acknowledged 清空，
	// 重建 incident 行的投影事件可被重新认领派发。
	var (
		status    string
		attempts  int64
		claimTok  sql.NullString
		acked     sql.NullInt64
		eventType string
	)
	if err := db.QueryRow(`SELECT status, attempt_count, claim_token, acknowledged_at_ms, event_type FROM `+defaultBusinessSchema+`.account_circuit_outbox WHERE dedupe_key='incident:tr-scope-t87-a'`).
		Scan(&status, &attempts, &claimTok, &acked, &eventType); err != nil {
		t.Fatalf("读回重置后的 dedupe 行失败: %v", err)
	}
	if status != "pending" || attempts != 0 || claimTok.Valid || acked.Valid || eventType != "incident_changed" {
		t.Fatalf("永生 dispatched 行必须被重置复用为可派发 pending 行: status=%s attempts=%d claim=%v acked=%v event=%s",
			status, attempts, claimTok, acked, eventType)
	}
}

// TestT87PendingOutboxIdempotentArmUnchanged 回归幂等臂：pending outbox 行仍
// 作幂等回放证据——同 tid 重试在 incident 行存在时返回 idempotent，不重复
// 推进 ledger revision。
func TestT87PendingOutboxIdempotentArmUnchanged(t *testing.T) {
	s, _ := t87OpenStore(t)
	ctx := context.Background()
	input := wkBaseIncident("scope-t87-b", "a1", 1)
	if result, err := s.CompareAndSetIncident(ctx, input); err != nil || result.Status != "applied" {
		t.Fatalf("第一轮 CAS = %+v err=%v", result, err)
	}

	retry, err := s.CompareAndSetIncident(ctx, input)
	if err != nil {
		t.Fatalf("pending 行幂等重试失败: %v", err)
	}
	if retry.Status != "idempotent" {
		t.Fatalf("pending 行必须维持幂等臂: %+v", retry)
	}
	if retry.Incident == nil || retry.Incident.LedgerRevision != 1 {
		t.Fatalf("幂等回放不得推进 ledger revision: %+v", retry.Incident)
	}
}

// TestT87IdentityMismatchReturnsIdentityReplay 钉住插入臂 identity 守卫：
// 永生 dispatched 行占据 dedupe 键时，同 transition_id 换身份（账户/runtime/
// scope 任一不同）重试落入插入臂，唯一键冲突且守卫未命中 → 0 行受影响 →
// ErrIdentityReplay，事务整体回滚、锚点 dispatched 行不被改写。
func TestT87IdentityMismatchReturnsIdentityReplay(t *testing.T) {
	s, db := t87OpenStore(t)
	ctx := context.Background()
	first := wkBaseIncident("scope-t87-c", "a1", 1)
	if result, err := s.CompareAndSetIncident(ctx, first); err != nil || result.Status != "applied" {
		t.Fatalf("第一轮 CAS = %+v err=%v", result, err)
	}
	t87DispatchOutbox(t, db, "incident:tr-scope-t87-c")

	hijack := wkBaseIncident("scope-t87-hijack", "a2", 1)
	hijack.TransitionID = "tr-scope-t87-c"
	if _, err := s.CompareAndSetIncident(ctx, hijack); !errors.Is(err, ErrIdentityReplay) {
		t.Fatalf("transition_id 被另一身份复用必须返回 ErrIdentityReplay: err=%v", err)
	}
	var hijackRows int
	if err := db.QueryRow(`SELECT count(*) FROM ` + defaultBusinessSchema + `.account_circuit_incidents WHERE circuit_scope_key='scope-t87-hijack'`).Scan(&hijackRows); err != nil {
		t.Fatal(err)
	}
	if hijackRows != 0 {
		t.Fatalf("identity 冲突必须整体回滚: hijack rows=%d", hijackRows)
	}
	var (
		accountID string
		status    string
	)
	if err := db.QueryRow(`SELECT account_id, status FROM `+defaultBusinessSchema+`.account_circuit_outbox WHERE dedupe_key='incident:tr-scope-t87-c'`).Scan(&accountID, &status); err != nil {
		t.Fatal(err)
	}
	if accountID != "a1" || status != "dispatched" {
		t.Fatalf("锚点 dispatched 行不得被改写: account=%s status=%s", accountID, status)
	}
}

// TestT87DispatchAdvanceKeepsDispatchedRowAsEvidence 钉住 dispatch 推进臂的
// 刻意不加过滤决策：dispatched 行仍是 dispatch 推进的幂等证据（dispatch 的
// 持久事实 accounts.dispatch_revision 不被任何清理删除），同 tid 重试返回已
// 推进的 revision，不二次推进、不重派事件。
func TestT87DispatchAdvanceKeepsDispatchedRowAsEvidence(t *testing.T) {
	s, db := t87OpenStore(t)
	ctx := context.Background()
	first, err := s.AdvanceDispatchRevision(ctx, DispatchRevision{AccountID: "a1", AccountRuntimeKey: "a1", TransitionID: "tr-t87-disp", NowMS: 100})
	if err != nil || first.Status != "applied" || first.DispatchRevision != 2 {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	claimed, err := s.ClaimOutbox(ctx, "t87-worker", 100, 20, 10)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim=%+v err=%v", claimed, err)
	}
	if ok, err := s.AcknowledgeOutbox(ctx, claimed[0].EventID, ProjectionKey, *claimed[0].ClaimToken, 110); err != nil || !ok {
		t.Fatalf("ack=%v err=%v", ok, err)
	}

	replay, err := s.AdvanceDispatchRevision(ctx, DispatchRevision{AccountID: "a1", AccountRuntimeKey: "a1", TransitionID: "tr-t87-disp", NowMS: 120})
	if err != nil || replay.Status != "idempotent" || replay.DispatchRevision != 2 {
		t.Fatalf("dispatched 行重试必须幂等返回 revision 2: %+v err=%v", replay, err)
	}
	var revision int64
	if err := db.QueryRow(`SELECT dispatch_revision FROM ` + defaultBusinessSchema + `.accounts WHERE id='a1'`).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if revision != 2 {
		t.Fatalf("dispatched 行幂等重试不得二次推进: revision=%d", revision)
	}
}
