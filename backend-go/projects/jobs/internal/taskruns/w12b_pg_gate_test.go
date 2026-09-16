package taskruns

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// w12b_pg_gate_test.go 门禁化开发 PostgreSQL（w1cover 临时覆盖库）覆盖
// taskruns 的 PG 专有分支：EnsureSchema PG 臂、advisory lock 获取与
// advisory_busy 快路径、ScheduledLease 三段 SQL 的 PG 臂、ReconcileStale
// PG 对账 SQL、`?` 占位符生命周期方法在 PG 模式下的真实行为（w12b 门控
// 曾实测 pgx 下直接 42601，已按前波同类缺陷修复并在此回归）。
// 数据全部 w12b- 前缀 ID，测试结束清理；数据库不可达时 t.Skip。

func w12bPgSkip(reason string) string { return "w12b taskruns PG gated: " + reason }

func w12bPgDSN(t *testing.T) string {
	t.Helper()
	if url := os.Getenv("JUHE_AI_W12B_TASKRUNS_PG_URL"); url != "" {
		return url
	}
	raw, err := os.ReadFile(`F:\sub2api-lite\.local\project-resources\dev\env\shared.env`)
	if err != nil {
		t.Skip(w12bPgSkip("shared.env 不可读"))
	}
	base := ""
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "JUHE_AI_POSTGRES_URL=") {
			base = strings.TrimPrefix(line, "JUHE_AI_POSTGRES_URL=")
		}
	}
	if base == "" {
		t.Skip(w12bPgSkip("shared.env 缺少 JUHE_AI_POSTGRES_URL"))
	}
	base = strings.Replace(base, ":6432/", ":5432/", 1)
	base = strings.Replace(base, "/juhe_ai_sub2api_dev?", "/juhe_ai_sub2api_dev_w1cover?", 1)
	return base
}

func w12bOpenPgStore(t *testing.T) *Store {
	t.Helper()
	store, err := OpenStore(StoreConfig{
		Mode:                 ModePostgres,
		PostgresURL:          w12bPgDSN(t),
		PostgresMaxOpenConns: 4,
		PostgresMaxIdleConns: 2,
	})
	if err != nil {
		t.Skipf(w12bPgSkip("OpenStore PG 打开失败: %v"), err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatalf("PG EnsureSchema 失败: %v", err)
	}
	clock := NewFakeClock(time.Date(2026, 9, 17, 8, 0, 0, 0, time.UTC))
	store.SetClock(clock)
	return store
}

func w12bPgCleanupRuns(t *testing.T, store *Store) {
	t.Helper()
	t.Cleanup(func() {
		ctx := context.Background()
		if _, err := store.db.ExecContext(ctx, `DELETE FROM juhe_stats.background_task_runs WHERE run_id LIKE 'w12b-%' OR lease_key LIKE 'w12b-%' OR job_name LIKE 'w12b-%'`); err != nil {
			t.Logf("w12b cleanup task_runs: %v", err)
		}
		if _, err := store.db.ExecContext(ctx, `DELETE FROM juhe_stats.background_job_leases WHERE lease_key LIKE 'w12b-%' OR run_id LIKE 'w12b-%' OR job_name LIKE 'w12b-%'`); err != nil {
			t.Logf("w12b cleanup job_leases: %v", err)
		}
	})
}

// TestW12bPgScheduledLeaseFencing 覆盖 PG 臂：advisory 获取、advisory_busy、
// lease_held、Renew/Assert/Release 的 PG SQL 与 fencing token 自增。
func TestW12bPgScheduledLeaseFencing(t *testing.T) {
	store := w12bOpenPgStore(t)
	ctx := context.Background()
	w12bPgCleanupRuns(t, store)
	leaseKey := "w12b-scheduled:account-quality-refresh:global"

	first, err := store.TryAcquireScheduledLease(ctx, ScheduledLeaseAcquireInput{
		JobName: "w12b-account-quality-refresh", LeaseKey: leaseKey, OwnerID: "w12b-owner-1", TTL: time.Minute,
	})
	if err != nil || !first.Acquired {
		t.Fatalf("PG 首次获取应成功: %+v %v", first, err)
	}
	if first.Lease == nil || first.Lease.FencingToken != 1 {
		t.Fatalf("PG fencing token 应为 1: %+v", first.Lease)
	}

	// 持有中：先 lease_held（advisory 空闲但行未过期）。
	held, err := store.TryAcquireScheduledLease(ctx, ScheduledLeaseAcquireInput{
		JobName: "w12b-account-quality-refresh", LeaseKey: leaseKey, OwnerID: "w12b-owner-2", TTL: time.Minute,
	})
	if err != nil || held.Acquired || held.Reason != AcquireLeaseHeld {
		t.Fatalf("PG 持有中应 lease_held: %+v %v", held, err)
	}

	// advisory_busy：另一事务持有 advisory xact lock 时快路径失败。
	advisory, err := ScheduledLeaseAdvisoryKey(leaseKey)
	if err != nil {
		t.Fatal(err)
	}
	blockTx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := blockTx.ExecContext(ctx, `SELECT pg_try_advisory_xact_lock($1::bigint)`, advisory); err != nil {
		t.Fatalf("持有 advisory lock 失败: %v", err)
	}
	busy, err := store.TryAcquireScheduledLease(ctx, ScheduledLeaseAcquireInput{
		JobName: "w12b-account-quality-refresh", LeaseKey: leaseKey, OwnerID: "w12b-owner-3", TTL: time.Minute,
	})
	if err != nil || busy.Acquired || busy.Reason != AcquireAdvisoryBusy {
		t.Fatalf("PG advisory 持有中应 advisory_busy: %+v %v", busy, err)
	}
	if err := blockTx.Rollback(); err != nil {
		t.Fatal(err)
	}

	// PG 臂续租成功且身份一致。
	renewed, err := store.RenewScheduledLease(ctx, *first.Lease, time.Minute)
	if err != nil || renewed == nil {
		t.Fatalf("PG 续租应成功: %v %v", renewed, err)
	}
	if renewed.LeaseKey != leaseKey || renewed.OwnerID != "w12b-owner-1" || renewed.FencingToken != 1 {
		t.Fatalf("PG 续租身份不符: %+v", renewed)
	}
	// PG 臂 fence 校验：持约人应通过，陌生 token 应报 ErrLeaseLost。
	if err := store.AssertScheduledLease(ctx, renewed.Fence()); err != nil {
		t.Fatalf("PG 持约人 fence 校验应通过: %v", err)
	}
	stranger := LeaseFence{LeaseKey: leaseKey, OwnerID: "w12b-owner-1", FencingToken: 99}
	if assertErr := store.AssertScheduledLease(ctx, stranger); assertErr == nil {
		t.Fatal("PG 陌生 token fence 校验应失败")
	}

	// PG 臂释放：置为过期后再获取 token 自增。
	released, err := store.ReleaseScheduledLease(ctx, *renewed)
	if err != nil || !released {
		t.Fatalf("PG 释放应命中: %v %v", released, err)
	}
	takeover, err := store.TryAcquireScheduledLease(ctx, ScheduledLeaseAcquireInput{
		JobName: "w12b-account-quality-refresh", LeaseKey: leaseKey, OwnerID: "w12b-owner-2", TTL: time.Minute,
	})
	if err != nil || !takeover.Acquired || takeover.Lease.FencingToken != 2 {
		t.Fatalf("PG 释放后接管应成功且 token=2: %+v %v", takeover, err)
	}
}

// TestW12bPgTaskRunLifecycle 覆盖 `?` 占位符生命周期方法在 PG 模式下的
// queued→running→heartbeat→finish 全链路（修复回归）。
func TestW12bPgTaskRunLifecycle(t *testing.T) {
	store := w12bOpenPgStore(t)
	ctx := context.Background()
	w12bPgCleanupRuns(t, store)

	run, err := store.CreateTaskRun(ctx, TaskRunCreateInput{
		JobName:    "w12b-temporary-maintenance-worker",
		JobType:    "probe",
		WorkerRole: TemporaryMaintenanceWorkerRole,
		LeaseKey:   "w12b-lifecycle-lease",
		Params:     map[string]any{"target": "w12b-target"},
	})
	if err != nil {
		t.Fatalf("PG CreateTaskRun 失败: %v", err)
	}
	started, err := store.TryStartTaskRun(ctx, TaskRunStartInput{
		RunID: run.RunID, OwnerID: "w12b-worker", LeaseUntil: time.Now().Add(2 * time.Minute),
	})
	if err != nil || !started {
		t.Fatalf("PG TryStartTaskRun 应成功: %v %v", started, err)
	}
	ok, err := store.HeartbeatTaskRun(ctx, run.RunID, "w12b-worker", time.Now().Add(2*time.Minute), nil)
	if err != nil || !ok {
		t.Fatalf("PG HeartbeatTaskRun 应成功: %v %v", ok, err)
	}
	exit := int64(0)
	changed, err := store.FinishTaskRun(ctx, TaskRunFinishInput{
		RunID: run.RunID, Status: StatusCompleted, Result: map[string]any{"ok": true}, ExitCode: &exit,
	})
	if err != nil || !changed {
		t.Fatalf("PG FinishTaskRun 应命中: %v %v", changed, err)
	}
	final, err := store.GetTaskRun(ctx, run.RunID)
	if err != nil || final == nil {
		t.Fatalf("PG GetTaskRun: %v", err)
	}
	if final.Status != StatusCompleted || final.Result["ok"] != true {
		t.Fatalf("PG 终态不符: %+v", final)
	}
	if final.StartedAt == nil || final.FinishedAt == nil || final.DurationMs == nil {
		t.Fatalf("PG 时间戳字段不齐: %+v", final)
	}
	// 释放租约后新 owner 可获取（`?` upsert CAS 在 PG 下可用）。
	acquired, err := store.AcquireLease(ctx, LeaseAcquireInput{
		LeaseKey: TemporaryTaskLeaseKey(run.RunID), JobName: TemporaryMaintenanceWorkerRole,
		ShardKey: run.RunID, OwnerID: "w12b-next", LeaseUntil: time.Now().Add(time.Minute),
	})
	if err != nil || !acquired {
		t.Fatalf("PG AcquireLease 应成功: %v %v", acquired, err)
	}
	if err := store.ReleaseLease(ctx, TemporaryTaskLeaseKey(run.RunID), "w12b-next"); err != nil {
		t.Fatalf("PG ReleaseLease 应成功: %v", err)
	}
}

// TestW12bPgReconcileStale 覆盖 ReconcileStale 的 PG 对账 SQL 臂：
// queued/running 收口与过期租约删除。
func TestW12bPgReconcileStale(t *testing.T) {
	store := w12bOpenPgStore(t)
	ctx := context.Background()
	w12bPgCleanupRuns(t, store)

	stale := time.Now().Add(-time.Hour).UTC().Format("2006-01-02T15:04:05.000Z07:00")
	insertRun := func(runID, status, submittedAt, heartbeatAt string) {
		t.Helper()
		_, err := store.db.ExecContext(ctx, `
		INSERT INTO juhe_stats.background_task_runs (
		  run_id, job_name, job_type, worker_role, status, lease_key, params_json, result_json,
		  submitted_at, created_at, updated_at, started_at, heartbeat_at
		) VALUES ($1, $2, 'probe', $3, $4, $5, '{}', '{}', $6, $6, $6, $7, $8)`,
			runID, "w12b-reconcile-job", TemporaryMaintenanceWorkerRole, status,
			"w12b-lease:"+runID, submittedAt, submittedAt, heartbeatAt)
		if err != nil {
			t.Fatalf("插入 w12b 对账场景行失败: %v", err)
		}
	}
	staleQueued := "w12b-rec-queued-" + formatTimeBase36(time.Now())
	staleRunning := "w12b-rec-running-" + formatTimeBase36(time.Now().Add(time.Millisecond))
	insertRun(staleQueued, "queued", stale, "")
	insertRun(staleRunning, "running", stale, stale)

	result, err := store.ReconcileStale(ctx, TaskRunReconcileInput{
		QueuedBefore:           time.Now().Add(-10 * time.Minute),
		RunningHeartbeatBefore: time.Now().Add(-10 * time.Minute),
		Now:                    w12bPtr(time.Now()),
		Limit:                  100,
	})
	if err != nil {
		t.Fatalf("PG ReconcileStale 失败: %v", err)
	}
	if result.FailedQueuedCount != 1 || result.FailedRunningCount != 1 {
		t.Fatalf("PG 对账计数不符: %+v", result)
	}
	reconciled, err := store.GetTaskRun(ctx, staleQueued)
	if err != nil || reconciled == nil || reconciled.Status != StatusFailed {
		t.Fatalf("PG stale queued 应收口为 failed: %+v %v", reconciled, err)
	}
}

func w12bPtr(t time.Time) *time.Time { return &t }
