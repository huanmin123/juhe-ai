package circuitstore

import (
	"context"
	"database/sql"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/opsjobs"
)

// kitControlPlaneSchemaPrepare 在隔离 PG 测试库上建控制面测试 schema（juhe_business
// 限定；表名/列名/列型与生产 PG DDL 对齐，CHECK/FK 不复刻——见
// circuitstore_pgkit_test.go 头注释）。
func kitControlPlaneSchemaPrepare(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS juhe_business;
CREATE TABLE juhe_business.accounts (
	id text PRIMARY KEY,
	dispatch_revision bigint NOT NULL DEFAULT 1,
	circuit_projection_revision bigint NOT NULL DEFAULT 0,
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
	updated_at_ms bigint NOT NULL
)`)
	return err
}

// openControlPlaneFixture 打开隔离 PG 业务库并安装控制面测试 schema
// （juhe_business 限定，列形状与生产 PG DDL 对齐的最小列集），经生产
// Postgres: true 臂装配控制面仓储与 reconcile 游标 store。
func openControlPlaneFixture(t *testing.T) (*ControlPlaneRepo, opsjobs.ReconcileCursorStore, *sql.DB) {
	t.Helper()
	db := openKitPostgresDB(t, kitControlPlaneSchemaPrepare)
	repo, err := NewControlPlaneRepo(ControlPlaneConfig{DB: db, Postgres: true})
	if err != nil {
		t.Fatal(err)
	}
	cursorStore, err := NewReconcileCursorStore(ControlPlaneConfig{DB: db, Postgres: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.EnsureCursorSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	return repo, cursorStore, db
}

func seedIncident(t *testing.T, db *sql.DB, scopeKey, accountID, runtimeKey, state string, generation, dispatchRevision, ledgerRevision, updatedAt int64) {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO account_circuit_incidents (
			circuit_scope_key, account_id, account_runtime_key, scope_kind,
			incident_id, child_incident_ids_json, state,
			generation, dispatch_revision, ledger_revision, transition_id,
			created_at_ms, updated_at_ms
		) VALUES (?, ?, ?, 'account', ?, '[]', ?, ?, ?, ?, 'tr-1', ?, ?)`,
		scopeKey, accountID, runtimeKey, "incident-"+scopeKey, state,
		generation, dispatchRevision, ledgerRevision, updatedAt, updatedAt); err != nil {
		t.Fatal(err)
	}
}

// kitSeedIncident 是 seedIncident 的 PG fixture 变体（juhe_business schema 限定
// 与 $n 占位符；断言语义与失败消息逐字节一致）。w13g5 注入臂的 seedIncident
// 保持 SQLite 原状（scripted 臂原样保留）。
func kitSeedIncident(t *testing.T, db *sql.DB, scopeKey, accountID, runtimeKey, state string, generation, dispatchRevision, ledgerRevision, updatedAt int64) {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO juhe_business.account_circuit_incidents (
			circuit_scope_key, account_id, account_runtime_key, scope_kind,
			incident_id, child_incident_ids_json, state,
			generation, dispatch_revision, ledger_revision, transition_id,
			created_at_ms, updated_at_ms
		) VALUES ($1, $2, $3, 'account', $4, '[]', $5, $6, $7, $8, 'tr-1', $9, $9)`,
		scopeKey, accountID, runtimeKey, "incident-"+scopeKey, state,
		generation, dispatchRevision, ledgerRevision, updatedAt); err != nil {
		t.Fatal(err)
	}
}

// TestLedgerListForRebuildPaging 验证 rebuild 分页：游标推进、closed tombstone
// 保留窗口、dispatch revision 一致性过滤。
func TestLedgerListForRebuildPaging(t *testing.T) {
	repo, _, db := openControlPlaneFixture(t)
	ctx := context.Background()
	if _, err := db.Exec(`INSERT INTO juhe_business.accounts (id, dispatch_revision) VALUES ('acc-1', 5), ('acc-deleted', 3)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE juhe_business.accounts SET deleted_at = '2020-01-01T00:00:00Z' WHERE id = 'acc-deleted'`); err != nil {
		t.Fatal(err)
	}
	kitSeedIncident(t, db, "sk-1", "acc-1", "acc-1", "OPEN", 1, 5, 3, 100)
	kitSeedIncident(t, db, "sk-2", "acc-1", "acc-1", "OPEN", 1, 5, 2, 200)
	kitSeedIncident(t, db, "sk-stale", "acc-1", "acc-1", "OPEN", 1, 4, 1, 300)               // 旧 revision → 不回放
	kitSeedIncident(t, db, "sk-deleted", "acc-deleted", "acc-deleted", "OPEN", 1, 3, 1, 400) // 账户已删 → 不回放

	page, err := repo.ListForRebuild(ctx, opsjobs.RebuildPageQuery{NowMS: 1000, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].CircuitScopeKey != "sk-1" {
		t.Fatalf("第一页应含 sk-1: %+v", page.Items)
	}
	if page.NextCursor == nil {
		t.Fatal("还有剩余行时必须返回游标")
	}
	second, err := repo.ListForRebuild(ctx, opsjobs.RebuildPageQuery{
		NowMS:                1000,
		Limit:                10,
		AfterUpdatedAtMS:     &page.NextCursor.UpdatedAtMS,
		AfterCircuitScopeKey: &page.NextCursor.CircuitScopeKey,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Items) != 1 || second.Items[0].CircuitScopeKey != "sk-2" {
		t.Fatalf("第二页应只含 sk-2: %+v", second.Items)
	}
	if second.NextCursor != nil {
		t.Fatal("最后一页不得返回游标")
	}

	// ListByRuntimeKeys 与 GetByScopeKey。
	records, err := repo.ListByRuntimeKeys(ctx, []string{"acc-1"}, false, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 {
		t.Fatalf("acc-1 应有 2 条活跃 incident: %d", len(records))
	}
	record, err := repo.GetByScopeKey(ctx, "sk-1")
	if err != nil || record == nil || record.IncidentID != "incident-sk-1" {
		t.Fatalf("GetByScopeKey 失败: %v %v", record, err)
	}
}

// TestOutboxClaimAckRelease 验证 outbox claim→ack→投影水位回写与
// release-for-replay 重试语义。
func TestOutboxClaimAckRelease(t *testing.T) {
	repo, _, db := openControlPlaneFixture(t)
	ctx := context.Background()
	if _, err := db.Exec(`INSERT INTO juhe_business.accounts (id, dispatch_revision, circuit_projection_revision) VALUES ('acc-1', 9, 0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO juhe_business.account_circuit_outbox (
			event_id, projection_key, dedupe_key, event_type, account_id, account_runtime_key,
			transition_id, dispatch_revision, status, available_at_ms, attempt_count, created_at_ms, updated_at_ms
		) VALUES ('evt-1', 'account_circuit_runtime_v1', 'dispatch:tr-9', 'dispatch_revision_changed', 'acc-1', 'acc-1',
			'tr-9', 9, 'pending', 0, 0, 1, 1)`); err != nil {
		t.Fatal(err)
	}

	claims, err := repo.Claim(ctx, "owner-1", 1000, 30_000, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(claims) != 1 || claims[0].EventID != "evt-1" || claims[0].ClaimToken == "" {
		t.Fatalf("claim 失败: %+v", claims)
	}
	event := claims[0]

	// release → 重新 pending。
	if err := repo.ReleaseForReplay(ctx, event, "projector_error", 2000, 500); err != nil {
		t.Fatal(err)
	}
	var availableAt int64
	if err := db.QueryRow(`SELECT available_at_ms FROM juhe_business.account_circuit_outbox WHERE event_id = 'evt-1'`).Scan(&availableAt); err != nil {
		t.Fatal(err)
	}
	if availableAt != 2500 {
		t.Fatalf("release 应写入重放时间: %d", availableAt)
	}

	// 二次 claim（available_at <= now）→ ack → 水位回写。
	claims, err = repo.Claim(ctx, "owner-1", 3000, 30_000, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(claims) != 1 {
		t.Fatalf("重放后应可再次 claim: %+v", claims)
	}
	acknowledged, err := repo.Ack(ctx, claims[0], 4000)
	if err != nil {
		t.Fatal(err)
	}
	if !acknowledged {
		t.Fatal("合法 claim 必须可 ack")
	}
	var projectionRevision int64
	if err := db.QueryRow(`SELECT circuit_projection_revision FROM juhe_business.accounts WHERE id = 'acc-1'`).Scan(&projectionRevision); err != nil {
		t.Fatal(err)
	}
	if projectionRevision != 9 {
		t.Fatalf("ack 应推进投影水位: %d", projectionRevision)
	}
	// 重复 ack 幂等返回 true（dispatched 短路）。
	acknowledged, err = repo.Ack(ctx, claims[0], 5000)
	if err != nil || !acknowledged {
		t.Fatalf("重复 ack 应幂等: %v %v", acknowledged, err)
	}

}

// TestReconcileCursorRoundTrip 验证 reconcile 游标持久化（Load 空值 → Save → Load）。
func TestReconcileCursorRoundTrip(t *testing.T) {
	_, cursorStore, _ := openControlPlaneFixture(t)
	ctx := context.Background()
	loaded, err := cursorStore.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if loaded != nil {
		t.Fatalf("初始游标必须为空: %+v", loaded)
	}
	cursor := opsjobs.IncidentCursor{UpdatedAtMS: 4321, CircuitScopeKey: "sk-9"}
	if err := cursorStore.Save(ctx, cursor); err != nil {
		t.Fatal(err)
	}
	loaded, err = cursorStore.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if loaded == nil || loaded.UpdatedAtMS != 4321 || loaded.CircuitScopeKey != "sk-9" {
		t.Fatalf("游标往返失败: %+v", loaded)
	}
}
