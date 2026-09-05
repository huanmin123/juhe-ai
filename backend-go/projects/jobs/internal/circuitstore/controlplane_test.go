package circuitstore

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/opsjobs"
)

// openControlPlaneFixture 构建临时 SQLite 业务库并安装控制面测试 schema
// （与 Node business-schema 的 accounts/account_circuit_* 表同形最小列集）。
func openControlPlaneFixture(t *testing.T) (*ControlPlaneRepo, opsjobs.ReconcileCursorStore, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "business.sqlite3")
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	statements := []string{
		`CREATE TABLE accounts (
			id TEXT PRIMARY KEY,
			dispatch_revision INTEGER NOT NULL DEFAULT 1,
			circuit_projection_revision INTEGER NOT NULL DEFAULT 0,
			deleted_at TEXT
		)`,
		`CREATE TABLE account_circuit_incidents (
			circuit_scope_key TEXT PRIMARY KEY,
			account_id TEXT NOT NULL,
			account_runtime_key TEXT NOT NULL,
			scope_kind TEXT NOT NULL,
			key_fingerprint TEXT,
			protocol_code TEXT,
			request_lane TEXT,
			model_family TEXT,
			incident_id TEXT NOT NULL,
			parent_incident_id TEXT,
			child_incident_ids_json TEXT NOT NULL DEFAULT '[]',
			state TEXT NOT NULL,
			generation INTEGER NOT NULL,
			dispatch_revision INTEGER NOT NULL,
			ledger_revision INTEGER NOT NULL,
			projected_ledger_revision INTEGER NOT NULL DEFAULT 0,
			transition_id TEXT NOT NULL,
			lease_id TEXT,
			lease_purpose TEXT,
			lease_until_ms INTEGER,
			backoff_level INTEGER NOT NULL DEFAULT 0,
			consecutive_failures INTEGER NOT NULL DEFAULT 0,
			confirmation_failures_required INTEGER NOT NULL DEFAULT 1,
			confirmation_failure_evidence_keys_json TEXT NOT NULL DEFAULT '[]',
			recovering_successes INTEGER NOT NULL DEFAULT 0,
			next_transition_at_ms INTEGER,
			open_until_ms INTEGER,
			last_failure_class TEXT,
			retained_until_ms INTEGER,
			created_at_ms INTEGER NOT NULL,
			updated_at_ms INTEGER NOT NULL
		)`,
		`CREATE TABLE account_circuit_outbox (
			event_id TEXT PRIMARY KEY,
			projection_key TEXT NOT NULL,
			dedupe_key TEXT NOT NULL,
			event_type TEXT NOT NULL,
			account_id TEXT NOT NULL,
			account_runtime_key TEXT NOT NULL,
			circuit_scope_key TEXT,
			incident_id TEXT,
			transition_id TEXT NOT NULL,
			dispatch_revision INTEGER NOT NULL,
			generation INTEGER,
			ledger_revision INTEGER,
			status TEXT NOT NULL DEFAULT 'pending',
			available_at_ms INTEGER NOT NULL,
			claim_token TEXT,
			claimed_by TEXT,
			claim_until_ms INTEGER,
			attempt_count INTEGER NOT NULL DEFAULT 0,
			last_error_class TEXT,
			acknowledged_at_ms INTEGER,
			created_at_ms INTEGER NOT NULL,
			updated_at_ms INTEGER NOT NULL
		)`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	repo, err := NewControlPlaneRepo(ControlPlaneConfig{DB: db, Postgres: false})
	if err != nil {
		t.Fatal(err)
	}
	cursorStore, err := NewReconcileCursorStore(ControlPlaneConfig{DB: db, Postgres: false})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.EnsureCursorSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
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

// TestLedgerListForRebuildPaging 验证 rebuild 分页：游标推进、closed tombstone
// 保留窗口、dispatch revision 一致性过滤。
func TestLedgerListForRebuildPaging(t *testing.T) {
	repo, _, db := openControlPlaneFixture(t)
	ctx := context.Background()
	if _, err := db.Exec(`INSERT INTO accounts (id, dispatch_revision) VALUES ('acc-1', 5), ('acc-deleted', 3)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE accounts SET deleted_at = '2020-01-01T00:00:00Z' WHERE id = 'acc-deleted'`); err != nil {
		t.Fatal(err)
	}
	seedIncident(t, db, "sk-1", "acc-1", "acc-1", "OPEN", 1, 5, 3, 100)
	seedIncident(t, db, "sk-2", "acc-1", "acc-1", "OPEN", 1, 5, 2, 200)
	seedIncident(t, db, "sk-stale", "acc-1", "acc-1", "OPEN", 1, 4, 1, 300)               // 旧 revision → 不回放
	seedIncident(t, db, "sk-deleted", "acc-deleted", "acc-deleted", "OPEN", 1, 3, 1, 400) // 账户已删 → 不回放

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
	if _, err := db.Exec(`INSERT INTO accounts (id, dispatch_revision, circuit_projection_revision) VALUES ('acc-1', 9, 0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO account_circuit_outbox (
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
	if err := db.QueryRow(`SELECT available_at_ms FROM account_circuit_outbox WHERE event_id = 'evt-1'`).Scan(&availableAt); err != nil {
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
	if err := db.QueryRow(`SELECT circuit_projection_revision FROM accounts WHERE id = 'acc-1'`).Scan(&projectionRevision); err != nil {
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
