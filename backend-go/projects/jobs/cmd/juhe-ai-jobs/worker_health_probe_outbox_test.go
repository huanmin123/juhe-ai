package main

// account_health_probe_request_outbox 消费/清理装配单测：DDL 双侧幂等 ensure、
// store 的 claim（只读 pending）与 complete（处理成功即删行，幂等 DELETE）、
// boundary 的账户 J1 冻结事实读取、prune 的保留期删除（status 无关）、
// retention env 解析与 J1 关闭时 prune 仍装配（SQLite 业务库临时文件）。

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
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

	// complete 幂等出队（处理成功即删行）；二次 complete 报告未变更且行不存在。
	first, err := store.CompleteProbeRequest(ctx, "j1-ready", now)
	if err != nil || !first {
		t.Fatalf("complete = %t, %v", first, err)
	}
	var readyRows int
	if err := business.db.QueryRow(`SELECT COUNT(*) FROM account_health_probe_request_outbox WHERE request_id = 'j1-ready'`).Scan(&readyRows); err != nil || readyRows != 0 {
		t.Fatalf("completed row must be deleted: rows=%d err=%v", readyRows, err)
	}
	second, err := store.CompleteProbeRequest(ctx, "j1-ready", now)
	if err != nil || second {
		t.Fatalf("second complete = %t, %v", second, err)
	}
	rows, err = store.ClaimPendingProbeRequests(ctx, 10, now.Add(2*time.Hour))
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if len(rows) != 1 || rows[0].RequestID != "j1-future" {
		// now+2h 时 j1-future 的 available_at 已到，reclaim 只应含它；
		// j1-ready 已删行出队，j1-done（consumed）不被 claim。
		t.Fatalf("reclaim = %+v want only j1-future", rows)
	}
	// 历史遗留 consumed 行不被 claim，complete 也不出队（只删 pending 行），
	// 由 prune 的保留期删除兜底。
	if _, err := store.CompleteProbeRequest(ctx, "j1-done", now); err != nil {
		t.Fatalf("complete consumed-seed row: %v", err)
	}
	var doneRows int
	if err := business.db.QueryRow(`SELECT COUNT(*) FROM account_health_probe_request_outbox WHERE request_id = 'j1-done'`).Scan(&doneRows); err != nil || doneRows != 1 {
		t.Fatalf("consumed row must stay until prune: rows=%d err=%v", doneRows, err)
	}
}

// TestHealthProbeOutboxCompleteReplayRowIsReclaimable：消费后删行形态下的
// 幂等重放——同 request_id 再灌行（模拟消费前 gateway 重写/崩溃重启）能被
// 再次 claim 并再次出队，表内不产生重复行；outcome 层不双写由 HasRequest
// 拦截（juhe_jobs.account_health_outcomes 幂等键），accounthealth 包
// outbox_drain_test.go 的 replay 测试断言该链路。
func TestHealthProbeOutboxCompleteReplayRowIsReclaimable(t *testing.T) {
	business := newProbeOutboxBusinessDB(t)
	if err := EnsureHealthProbeOutboxSchema(context.Background(), business); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	store := healthProbeOutboxStore{business: business}
	ctx := context.Background()
	now := time.Now().UTC()
	insert := func(requestID string) {
		t.Helper()
		query := business.bind(`INSERT INTO account_health_probe_request_outbox
			(request_id, account_id, reason, trace_id, source_fence, deadline_at, status, consumed_at, available_at, created_at, updated_at)
			VALUES (?, 'a', 'request_failure', '', '', '2026-01-02T00:00:00.000000000Z', 'pending', NULL, ?, ?, ?)`)
		stamp := now.Format(time.RFC3339Nano)
		if _, err := business.db.Exec(query, requestID, stamp, stamp, stamp); err != nil {
			t.Fatalf("insert %s: %v", requestID, err)
		}
	}
	insert("j1-replay")
	if first, err := store.CompleteProbeRequest(ctx, "j1-replay", now); err != nil || !first {
		t.Fatalf("first complete = %t, %v", first, err)
	}
	// 重灌同 request_id：主键冲突不存在（首行已删），重放行可再次消费。
	insert("j1-replay")
	rows, err := store.ClaimPendingProbeRequests(ctx, 10, now)
	if err != nil || len(rows) != 1 || rows[0].RequestID != "j1-replay" {
		t.Fatalf("replayed row must be reclaimable: %+v, %v", rows, err)
	}
	if again, err := store.CompleteProbeRequest(ctx, "j1-replay", now); err != nil || !again {
		t.Fatalf("replay complete = %t, %v", again, err)
	}
	var total int
	if err := business.db.QueryRow(`SELECT COUNT(*) FROM account_health_probe_request_outbox`).Scan(&total); err != nil || total != 0 {
		t.Fatalf("outbox must stay empty after replay: rows=%d err=%v", total, err)
	}
}

// TestHealthProbeOutboxPruneDeletesExpiredRowsRegardlessOfStatus：prune 只删
// created_at 早于保留期的行且与 status 无关；保留窗口内的行（含 consumed
// 历史遗留行）不动。
func TestHealthProbeOutboxPruneDeletesExpiredRowsRegardlessOfStatus(t *testing.T) {
	business := newProbeOutboxBusinessDB(t)
	if err := EnsureHealthProbeOutboxSchema(context.Background(), business); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	ctx := context.Background()
	now := time.Now().UTC()
	insert := func(requestID, status, createdAt string) {
		t.Helper()
		consumed := "NULL"
		if status == "consumed" {
			consumed = "'" + now.Format(time.RFC3339Nano) + "'"
		}
		query := business.bind(`INSERT INTO account_health_probe_request_outbox
			(request_id, account_id, reason, trace_id, source_fence, deadline_at, status, consumed_at, available_at, created_at, updated_at)
			VALUES (?, 'a', 'request_failure', '', '', '2026-01-02T00:00:00.000000000Z', ?, ` + consumed + `, ?, ?, ?)`)
		if _, err := business.db.Exec(query, requestID, status, createdAt, createdAt, createdAt); err != nil {
			t.Fatalf("insert %s: %v", requestID, err)
		}
	}
	expired := now.Add(-8 * 24 * time.Hour).Format(time.RFC3339Nano)
	fresh := now.Format(time.RFC3339Nano)
	insert("j1-expired-pending", "pending", expired)
	insert("j1-expired-consumed", "consumed", expired)
	insert("j1-fresh-pending", "pending", fresh)
	insert("j1-fresh-consumed", "consumed", fresh)

	pruner := healthProbeOutboxPruner{
		business:  business,
		retention: 7 * 24 * time.Hour,
		interval:  time.Hour,
		logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	deleted, err := pruner.pruneOnce(ctx, now)
	if err != nil {
		t.Fatalf("pruneOnce: %v", err)
	}
	if deleted != 2 {
		t.Fatalf("prune deleted = %d want exactly the 2 expired rows", deleted)
	}
	for _, requestID := range []string{"j1-expired-pending", "j1-expired-consumed"} {
		var count int
		if err := business.db.QueryRow(`SELECT COUNT(*) FROM account_health_probe_request_outbox WHERE request_id = ?`, requestID).Scan(&count); err != nil || count != 0 {
			t.Fatalf("expired row %s must be pruned: count=%d err=%v", requestID, count, err)
		}
	}
	for _, requestID := range []string{"j1-fresh-pending", "j1-fresh-consumed"} {
		var count int
		if err := business.db.QueryRow(`SELECT COUNT(*) FROM account_health_probe_request_outbox WHERE request_id = ?`, requestID).Scan(&count); err != nil || count != 1 {
			t.Fatalf("fresh row %s must survive: count=%d err=%v", requestID, count, err)
		}
	}
}

// TestHealthProbeOutboxPrunerRunConsumesImmediatelyAndStops：Run 启动即跑首轮
// （既有堆积启动收敛），ctx 取消后退出。
func TestHealthProbeOutboxPrunerRunConsumesImmediatelyAndStops(t *testing.T) {
	business := newProbeOutboxBusinessDB(t)
	if err := EnsureHealthProbeOutboxSchema(context.Background(), business); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	now := time.Now().UTC()
	expired := now.Add(-30 * 24 * time.Hour).Format(time.RFC3339Nano)
	query := business.bind(`INSERT INTO account_health_probe_request_outbox
		(request_id, account_id, reason, trace_id, source_fence, deadline_at, status, consumed_at, available_at, created_at, updated_at)
		VALUES ('j1-stale', 'a', 'request_failure', '', '', '2026-01-02T00:00:00.000000000Z', 'pending', NULL, ?, ?, ?)`)
	if _, err := business.db.Exec(query, expired, expired, expired); err != nil {
		t.Fatalf("insert: %v", err)
	}
	pruner := healthProbeOutboxPruner{
		business:  business,
		retention: 7 * 24 * time.Hour,
		interval:  time.Hour,
		logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- pruner.Run(ctx) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var count int
		if err := business.db.QueryRow(`SELECT COUNT(*) FROM account_health_probe_request_outbox`).Scan(&count); err != nil {
			t.Fatalf("count: %v", err)
		}
		if count == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("first prune cycle must run immediately on start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("run exit err = %v want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("pruner loop did not stop after cancel")
	}
}

// TestParseProbeOutboxRetentionDays：默认/边界/非法值语义（非法取默认并 warn）。
func TestParseProbeOutboxRetentionDays(t *testing.T) {
	warned := 0
	warn := func(string) { warned++ }
	getenv := func(value string) func(string) string {
		return func(name string) string {
			if name == probeOutboxRetentionEnvVar {
				return value
			}
			return ""
		}
	}
	if days := parseProbeOutboxRetentionDays(getenv(""), warn); days != defaultProbeOutboxRetentionDays || warned != 0 {
		t.Fatalf("unset env = %d warned=%d want default without warn", days, warned)
	}
	if days := parseProbeOutboxRetentionDays(nil, warn); days != defaultProbeOutboxRetentionDays {
		t.Fatalf("nil getenv = %d want default", days)
	}
	if days := parseProbeOutboxRetentionDays(getenv(" 30 "), warn); days != 30 || warned != 0 {
		t.Fatalf("trimmed value = %d warned=%d", days, warned)
	}
	for _, boundary := range []struct {
		value string
		want  int
	}{{"1", 1}, {"365", 365}} {
		if days := parseProbeOutboxRetentionDays(getenv(boundary.value), warn); days != boundary.want || warned > 0 {
			t.Fatalf("boundary %s = %d warned=%d want %d without warn", boundary.value, days, warned, boundary.want)
		}
	}
	for _, value := range []string{"0", "366", "-1", "abc", "7.5"} {
		before := warned
		if days := parseProbeOutboxRetentionDays(getenv(value), warn); days != defaultProbeOutboxRetentionDays {
			t.Fatalf("invalid %q = %d want default", value, days)
		}
		if warned != before+1 {
			t.Fatalf("invalid %q must warn (warned %d -> %d)", value, before, warned)
		}
	}
}

// TestWireHealthProbeOutboxFaceWithoutJ1StillWiresPruner：J1 关闭（合法部署
// 形态）时 drain 保持未装配，prune 仍装配——J1 关闭部署的 pending 堆积由
// 保留期删除兜底。
func TestWireHealthProbeOutboxFaceWithoutJ1StillWiresPruner(t *testing.T) {
	assembly := &workerAssembly{
		config: workerConfig{Driver: "sqlite", BusinessSQLitePath: filepath.Join(t.TempDir(), "business.sqlite3")},
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	face, err := assembly.wireHealthProbeOutboxFace(func(string) string { return "" })
	if err != nil {
		t.Fatalf("wire face: %v", err)
	}
	if face.drain != nil {
		t.Fatal("J1 disabled must keep the drain unwired")
	}
	if face.pruner == nil {
		t.Fatal("pruner must stay wired without J1")
	}
	if face.pruner.retention != time.Duration(defaultProbeOutboxRetentionDays)*24*time.Hour {
		t.Fatalf("pruner retention = %s", face.pruner.retention)
	}
	if face.pruner.interval != defaultProbeOutboxPruneInterval {
		t.Fatalf("pruner interval = %s", face.pruner.interval)
	}
	if _, err := face.pruner.pruneOnce(context.Background(), time.Now()); err != nil {
		t.Fatalf("pruneOnce on wired face: %v", err)
	}
	assembly.closeStores()
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
