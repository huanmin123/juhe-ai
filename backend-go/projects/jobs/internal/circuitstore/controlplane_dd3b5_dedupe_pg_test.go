package circuitstore

// DD3-B5 回归（2026-10-31，R4 confirmed A 类即修）：circuit dedupe 行永生毒化。
//
// 缺陷：CompareAndSetIncident 的 outbox dedupe 查询无 status 过滤——jobs
// Cleanup 只删 outbox 全部 dispatched 的 incident 行（dispatched outbox 行
// 永生、无生产清理 caller），incident 行删除后同 transition_id 对账重试仍
// dedupe 命中 dispatched 行 → 幂等臂读不到 incident 行 →
// "deduplicated incident receipt has no incident" 硬错误恒败。
//
// 修复：dedupe 查询追加 AND status<>'dispatched'——已完成的行不再作幂等
// 回放证据，同 tid 重试走新行 CAS 插入臂；pending/processing 幂等臂不变。
//
// CompareAndSetIncident 依赖行锁事务，只有真实 PG 可验证（pgtestkit 隔离
// 测试库，JUHE_AI_TEST_EXTERNAL 授权门控，日常回归默认跳过）。fixture 按
// kit 约定不复刻 CHECK/FK，保留 PK/UNIQUE（CAS 的 ON CONFLICT 与 outbox
// 去重依赖）。

import (
	"context"
	"database/sql"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/opsjobs"
)

const dd3b5Prepare = `CREATE SCHEMA juhe_business;
CREATE TABLE juhe_business.accounts (
  id text PRIMARY KEY,
  dispatch_revision bigint NOT NULL DEFAULT 1,
  deleted_at text
);
CREATE TABLE juhe_business.account_circuit_incidents (
  circuit_scope_key text PRIMARY KEY,
  account_id text NOT NULL,
  account_runtime_key text NOT NULL,
  scope_kind text NOT NULL,
  key_fingerprint text,
  protocol_code text,
  request_lane text,
  model_family text,
  client_model text,
  capability_hash text,
  credential_source_account_id text,
  client_endpoint_family text,
  final_upstream_model text,
  upstream_endpoint_mode text,
  incident_id text NOT NULL,
  parent_incident_id text,
  child_incident_ids_json text NOT NULL DEFAULT '[]',
  caused_by_terminal_outcome_id text,
  state text NOT NULL,
  failure_scope text,
  generation bigint NOT NULL,
  dispatch_revision bigint NOT NULL,
  ledger_revision bigint NOT NULL,
  projected_ledger_revision bigint NOT NULL DEFAULT 0,
  transition_id text NOT NULL,
  cooldown_observation_generation integer NOT NULL DEFAULT 0,
  open_until_ms bigint,
  next_transition_at_ms bigint,
  lease_id text,
  lease_purpose text,
  lease_owner_run_id text,
  lease_until_ms bigint,
  attempt_started_at_ms bigint,
  attempt_hard_deadline_ms bigint,
  upstream_attempt_observed integer NOT NULL DEFAULT 0,
  backoff_level integer NOT NULL DEFAULT 0,
  consecutive_failures integer NOT NULL DEFAULT 0,
  confirmation_failures_required integer NOT NULL DEFAULT 1,
  confirmation_failure_evidence_keys_json text NOT NULL DEFAULT '[]',
  recovering_successes integer NOT NULL DEFAULT 0,
  last_failure_class text,
  retained_until_ms bigint,
  created_at_ms bigint NOT NULL,
  updated_at_ms bigint NOT NULL
);
CREATE TABLE juhe_business.account_circuit_outbox (
  event_id text PRIMARY KEY,
  projection_key text NOT NULL,
  dedupe_key text NOT NULL,
  event_type text NOT NULL,
  account_id text NOT NULL,
  account_runtime_key text NOT NULL,
  circuit_scope_key text,
  incident_id text,
  transition_id text NOT NULL,
  dispatch_revision bigint NOT NULL,
  generation bigint,
  ledger_revision bigint,
  status text NOT NULL DEFAULT 'pending',
  available_at_ms bigint NOT NULL,
  claim_token text,
  claimed_by text,
  claim_until_ms bigint,
  attempt_count integer NOT NULL DEFAULT 0,
  last_error_class text,
  acknowledged_at_ms bigint,
  created_at_ms bigint NOT NULL,
  updated_at_ms bigint NOT NULL,
  UNIQUE (projection_key, dedupe_key)
)`

// dd3b5OpenRepo 打开隔离测试库、建三张业务表并种入账户行（dispatch_revision=9
// 与 casTestInput 对齐）。
func dd3b5OpenRepo(t *testing.T, accountID string) (*ControlPlaneRepo, *sql.DB) {
	t.Helper()
	db := openKitPostgresDB(t, func(ctx context.Context, handle *sql.DB) error {
		_, err := handle.ExecContext(ctx, dd3b5Prepare)
		return err
	})
	if _, err := db.Exec(`INSERT INTO juhe_business.accounts (id, dispatch_revision) VALUES ($1, 9)`, accountID); err != nil {
		t.Fatalf("种子账户行失败: %v", err)
	}
	repo, err := NewControlPlaneRepo(ControlPlaneConfig{DB: db})
	if err != nil {
		t.Fatal(err)
	}
	return repo, db
}

// dd3b5FirstCAS 执行第一轮 CAS：落 incident 行 + pending outbox 行。
func dd3b5FirstCAS(t *testing.T, repo *ControlPlaneRepo, scopeKey, accountID string) opsjobs.IncidentCASInput {
	t.Helper()
	input := casTestInput(scopeKey, accountID, "SUSPECT", 1, 9, 1000)
	result, err := repo.CompareAndSetIncident(context.Background(), input)
	if err != nil {
		t.Fatalf("第一轮 CAS 失败: %v", err)
	}
	if result.Status != opsjobs.IncidentCASApplied {
		t.Fatalf("第一轮 CAS 必须 applied: %+v", result)
	}
	return input
}

// TestDD3B5DispatchedOutboxMissingIncidentRetriesViaInsert 臂钉住毒化场景
// 修复：incident 行被 Cleanup 删除（其 outbox 行已全部 dispatched、行永生）
// 后，同 transition_id 对账重试不再命中 dispatched 行作幂等回放证据（修复
// 前此处恒返回 "deduplicated incident receipt has no incident" 硬错误），
// 而是走新行 CAS 插入臂 applied；插入臂对永生 dispatched 行占据的
// (projection_key, dedupe_key) 唯一键冲突重置复用（status 回 pending 重新
// 派发，供重建 incident 行投影；纯 INSERT 在此撞 23505，真实 PG 复现）。
func TestDD3B5DispatchedOutboxMissingIncidentRetriesViaInsert(t *testing.T) {
	repo, db := dd3b5OpenRepo(t, "acc-dd3b5-a")
	input := dd3b5FirstCAS(t, repo, "scope-dd3b5-a", "acc-dd3b5-a")

	// 模拟 Cleanup 后状态：outbox 行 dispatch 完成、incident 行物理删除。
	if _, err := db.Exec(`UPDATE juhe_business.account_circuit_outbox SET status='dispatched', acknowledged_at_ms=2000 WHERE dedupe_key='incident:tr-scope-dd3b5-a'`); err != nil {
		t.Fatalf("置 dispatched 失败: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM juhe_business.account_circuit_incidents WHERE circuit_scope_key='scope-dd3b5-a'`); err != nil {
		t.Fatalf("删除 incident 行失败: %v", err)
	}

	retry, err := repo.CompareAndSetIncident(context.Background(), input)
	if err != nil {
		t.Fatalf("dispatched 行存在 + incident 缺失不得硬错误（修复前恒败）: %v", err)
	}
	if retry.Status != opsjobs.IncidentCASApplied {
		t.Fatalf("同 tid 重试必须走插入臂 applied: %+v", retry)
	}
	if retry.Incident == nil || retry.Incident.LedgerRevision != 1 {
		t.Fatalf("重试必须插入新行（ledger_revision 从 1 重启）: %+v", retry.Incident)
	}
	// 冲突重置复用：dedupe 行回 pending、claim 面与 acknowledged 清空，
	// 重建 incident 行的投影事件可被重新认领派发。
	var (
		status    string
		attempts  int
		claimTok  sql.NullString
		acked     sql.NullInt64
		eventType string
	)
	if err := db.QueryRow(`SELECT status, attempt_count, claim_token, acknowledged_at_ms, event_type FROM juhe_business.account_circuit_outbox WHERE dedupe_key='incident:tr-scope-dd3b5-a'`).
		Scan(&status, &attempts, &claimTok, &acked, &eventType); err != nil {
		t.Fatalf("读回重置后的 dedupe 行失败: %v", err)
	}
	if status != "pending" || attempts != 0 || claimTok.Valid || acked.Valid || eventType != "incident_changed" {
		t.Fatalf("永生 dispatched 行必须被重置复用为可派发 pending 行: status=%s attempts=%d claim=%v acked=%v event=%s",
			status, attempts, claimTok, acked, eventType)
	}
}

// TestDD3B5PendingOutboxIdempotentArmUnchanged 回归幂等臂：pending outbox
// 行仍作幂等回放证据——同 tid 重试在 incident 行存在时返回 idempotent，
// 不重复推进 ledger revision。
func TestDD3B5PendingOutboxIdempotentArmUnchanged(t *testing.T) {
	repo, _ := dd3b5OpenRepo(t, "acc-dd3b5-b")
	input := dd3b5FirstCAS(t, repo, "scope-dd3b5-b", "acc-dd3b5-b")

	retry, err := repo.CompareAndSetIncident(context.Background(), input)
	if err != nil {
		t.Fatalf("pending 行幂等重试失败: %v", err)
	}
	if retry.Status != opsjobs.IncidentCASIdempotent {
		t.Fatalf("pending 行必须维持幂等臂: %+v", retry)
	}
	if retry.Incident == nil || retry.Incident.LedgerRevision != 1 {
		t.Fatalf("幂等回放不得推进 ledger revision: %+v", retry.Incident)
	}
}

// TestDD3B5DispatchedOutboxWithIncidentRowGoesThroughCAS 钉住修复后的另一半
// 语义：incident 行仍在而 outbox 行已 dispatched 时，同 tid 重试不再返回
// idempotent（dispatched 行不作回放证据），而是进入正常 CAS 比较——
// expected 缺省且行存在 → conflict（调用方按冲突臂重读行后带 expected 重试）。
func TestDD3B5DispatchedOutboxWithIncidentRowGoesThroughCAS(t *testing.T) {
	repo, db := dd3b5OpenRepo(t, "acc-dd3b5-c")
	input := dd3b5FirstCAS(t, repo, "scope-dd3b5-c", "acc-dd3b5-c")

	if _, err := db.Exec(`UPDATE juhe_business.account_circuit_outbox SET status='dispatched', acknowledged_at_ms=2000 WHERE dedupe_key='incident:tr-scope-dd3b5-c'`); err != nil {
		t.Fatalf("置 dispatched 失败: %v", err)
	}

	retry, err := repo.CompareAndSetIncident(context.Background(), input)
	if err != nil {
		t.Fatalf("dispatched 行存在 + incident 行存在不得硬错误: %v", err)
	}
	if retry.Status != opsjobs.IncidentCASConflict {
		t.Fatalf("dispatched 行不作回放证据，重试必须进入 CAS conflict 臂: %+v", retry)
	}
	if retry.Incident == nil || retry.Incident.LedgerRevision != 1 {
		t.Fatalf("conflict 响应必须携带现存行: %+v", retry.Incident)
	}
}
