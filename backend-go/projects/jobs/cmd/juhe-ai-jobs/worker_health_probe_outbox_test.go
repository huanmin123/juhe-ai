package main

// account_health_probe_request_outbox 消费装配单测：DDL 双侧幂等 ensure、
// store 的 claim（只读 pending）与 complete（幂等 consumed 落位）、boundary
// 的账户 J1 冻结事实读取（SQLite 业务库临时文件）。

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func newProbeOutboxBusinessDB(t *testing.T) *businessDB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "business.sqlite3")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("open business sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &businessDB{db: db, postgres: false}
}

func seedProbeOutboxBoundaryTables(t *testing.T, business *businessDB) {
	t.Helper()
	statements := []string{
		`CREATE TABLE accounts (id TEXT PRIMARY KEY, deleted_at TEXT, config_revision INTEGER, dispatch_revision INTEGER)`,
		`CREATE TABLE account_health_jobs_input_versions (account_id TEXT PRIMARY KEY, current_version INTEGER)`,
	}
	for _, statement := range statements {
		if _, err := business.db.Exec(statement); err != nil {
			t.Fatalf("seed schema: %v", err)
		}
	}
}

func TestEnsureHealthProbeOutboxSchemaIdempotent(t *testing.T) {
	business := newProbeOutboxBusinessDB(t)
	ctx := context.Background()
	// gateway 侧先到先建（同 DDL 逐字一致）后，jobs 侧 ensure 仍须无错收敛。
	if err := EnsureHealthProbeOutboxSchema(ctx, business); err != nil {
		t.Fatalf("jobs-side ensure: %v", err)
	}
	if err := EnsureHealthProbeOutboxSchema(ctx, business); err != nil {
		t.Fatalf("repeated ensure: %v", err)
	}
	if _, err := business.db.Exec(healthProbeOutboxSchema); err != nil {
		t.Fatalf("re-run create: %v", err)
	}
	if _, err := business.db.Exec(healthProbeOutboxIndex); err != nil {
		t.Fatalf("re-run index: %v", err)
	}
	if _, err := business.db.ExecContext(ctx, business.bind(`INSERT INTO account_health_probe_request_outbox
		(request_id, account_id, reason, trace_id, source_fence, deadline_at, status, consumed_at, available_at, created_at, updated_at)
		VALUES ('j1-x', 'a', 'request_failure', '', '', '2026-01-01T00:00:00.000000000Z', 'pending', NULL, '2026-01-01T00:00:00.000000000Z', '2026-01-01T00:00:00.000000000Z', '2026-01-01T00:00:00.000000000Z')`)); err != nil {
		t.Fatalf("insert after ensure: %v", err)
	}
	if err := EnsureHealthProbeOutboxSchema(ctx, nil); err == nil {
		t.Fatal("nil business handle must fail closed")
	}
}

func TestHealthProbeOutboxStoreClaimAndComplete(t *testing.T) {
	business := newProbeOutboxBusinessDB(t)
	if err := EnsureHealthProbeOutboxSchema(context.Background(), business); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	store := healthProbeOutboxStore{business: business}
	ctx := context.Background()
	now := time.Now().UTC()

	insert := func(requestID, status, availableAt string) {
		t.Helper()
		consumed := "NULL"
		if status == "consumed" {
			consumed = "'" + now.Format(time.RFC3339Nano) + "'"
		}
		query := business.bind(`INSERT INTO account_health_probe_request_outbox
			(request_id, account_id, reason, trace_id, source_fence, deadline_at, status, consumed_at, available_at, created_at, updated_at)
			VALUES (?, 'a', 'request_failure', '', '', '2026-01-02T00:00:00.000000000Z', ?, ` + consumed + `, ?, '2026-01-01T00:00:00.000000000Z', '2026-01-01T00:00:00.000000000Z')`)
		if _, err := business.db.Exec(query, requestID, status, availableAt); err != nil {
			t.Fatalf("insert %s: %v", requestID, err)
		}
	}
	insert("j1-ready", "pending", now.Add(-time.Second).Format(time.RFC3339Nano))
	insert("j1-future", "pending", now.Add(time.Hour).Format(time.RFC3339Nano))
	insert("j1-done", "consumed", now.Add(-time.Second).Format(time.RFC3339Nano))

	rows, err := store.ClaimPendingProbeRequests(ctx, 10, now)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if len(rows) != 1 || rows[0].RequestID != "j1-ready" || rows[0].Reason != "request_failure" {
		t.Fatalf("claim = %+v want only the due pending row", rows)
	}
	if rows[0].SourceFence != nil {
		t.Fatalf("empty fence must decode to nil: %+v", rows[0].SourceFence)
	}
	if rows[0].Deadline.IsZero() {
		t.Fatal("deadline must be projected")
	}

	// claim 只读不改状态。
	var status string
	if err := business.db.QueryRow(`SELECT status FROM account_health_probe_request_outbox WHERE request_id = 'j1-ready'`).Scan(&status); err != nil || status != "pending" {
		t.Fatalf("claim must not mutate status: %s %v", status, err)
	}

	// complete 幂等落位；二次 complete 报告未变更。
	first, err := store.CompleteProbeRequest(ctx, "j1-ready", now)
	if err != nil || !first {
		t.Fatalf("complete = %t, %v", first, err)
	}
	second, err := store.CompleteProbeRequest(ctx, "j1-ready", now)
	if err != nil || second {
		t.Fatalf("second complete = %t, %v", second, err)
	}
	rows, err = store.ClaimPendingProbeRequests(ctx, 10, now.Add(2*time.Hour))
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	for _, row := range rows {
		if row.RequestID == "j1-done" {
			t.Fatalf("consumed row must not be reclaimed: %+v", rows)
		}
	}
}

func TestHealthProbeBoundaryReadsJ1Facts(t *testing.T) {
	business := newProbeOutboxBusinessDB(t)
	seedProbeOutboxBoundaryTables(t, business)
	boundary := healthProbeBoundary{business: business}
	ctx := context.Background()

	if _, _, _, ok, err := boundary.CurrentProbeInput(ctx, "missing"); err != nil || ok {
		t.Fatalf("missing account ok=%t err=%v", ok, err)
	}
	seed := func(id string, deletedAt any, configRevision, dispatchRevision, currentVersion int64) {
		t.Helper()
		if _, err := business.db.Exec(`INSERT INTO accounts (id, deleted_at, config_revision, dispatch_revision) VALUES (?, ?, ?, ?)`,
			id, deletedAt, configRevision, dispatchRevision); err != nil {
			t.Fatal(err)
		}
		if currentVersion > 0 {
			if _, err := business.db.Exec(`INSERT INTO account_health_jobs_input_versions (account_id, current_version) VALUES (?, ?)`, id, currentVersion); err != nil {
				t.Fatal(err)
			}
		}
	}
	seed("acc-ok", nil, 4, 5, 7)
	seed("acc-deleted", "2026-01-01T00:00:00Z", 1, 1, 1)
	seed("acc-no-epoch", nil, 1, 1, 0)
	seed("acc-zero-revision", nil, 0, 1, 1)

	configRevision, dispatchRevision, inputVersion, ok, err := boundary.CurrentProbeInput(ctx, "acc-ok")
	if err != nil || !ok || configRevision != 4 || dispatchRevision != 5 || inputVersion != 7 {
		t.Fatalf("ok account = (%d, %d, %d, %t), %v", configRevision, dispatchRevision, inputVersion, ok, err)
	}
	for _, id := range []string{"acc-deleted", "acc-no-epoch", "acc-zero-revision"} {
		if _, _, _, ok, err := boundary.CurrentProbeInput(ctx, id); err != nil || ok {
			t.Fatalf("%s must be out of scope: ok=%t err=%v", id, ok, err)
		}
	}
	// 迁移自被删 healthDispatchBoundary 的绑定参数方言（PG $n）由 bind 覆盖；
	// 空查询串直接容错保持错误路径可见。
	broken := healthProbeBoundary{business: &businessDB{db: business.db, postgres: true}}
	if _, _, _, _, err := broken.CurrentProbeInput(ctx, "acc-ok"); err == nil {
		t.Fatal("postgres bind against a sqlite handle must surface the dialect error")
	} else if errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("unexpected no-rows error: %v", err)
	}
}
