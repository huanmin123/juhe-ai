package recordmaintenance

// T207 C1 毒丸修复回归（真实 PG，pgtestkit 隔离库）：
//   - 未知 type 的毒丸行不再队头阻塞——置 status='failed' 终态隔离出队，后续
//     行照常消费；
//   - failed 行不再被 Dequeue 选中；
//   - 瞬时失败按 attempt_count 指数退避（60s/300s/900s/3600s 封顶）保留
//     pending，退避未到期不再入选，到期后重试成功即删行；
//   - snapshot_json 损坏行在 Dequeue 读面直接置 failed 隔离；
//   - 保留清理按 created_at 删超龄行（含 pending/failed）。

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

// newQueuePG 创建 pgtestkit 隔离 PG 库（预建 juhe_dataset schema，表与升级
// DDL 由生产 EnsureSchema 幂等路径创建）。
func newQueuePG(t *testing.T) (*sql.DB, *Store) {
	t.Helper()
	db := pgtestkit.New(t, func(ctx context.Context, db *sql.DB) error {
		_, err := db.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS juhe_dataset`)
		return err
	})
	store, err := OpenStore(db)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	return db, store
}

// t207SeedRow 按交接契约列集写入一行（快照列按需覆盖哨兵值）。
func t207SeedRow(t *testing.T, db *sql.DB, id, jobType, cutoffAt string, batchSize, maxBatches int, createdAt, kind, snapshotJSON string) {
	t.Helper()
	updatedAt := createdAt
	t207SeedRowUpdatedAt(t, db, id, jobType, cutoffAt, batchSize, maxBatches, createdAt, kind, snapshotJSON, updatedAt)
}

// t207SeedRowUpdatedAt 显式指定 updated_at（快照行载荷字段；清理行走哨兵）。
func t207SeedRowUpdatedAt(t *testing.T, db *sql.DB, id, jobType, cutoffAt string, batchSize, maxBatches int, createdAt, kind, snapshotJSON, updatedAt string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO juhe_dataset.record_maintenance_jobs
		(id, type, cutoff_at, batch_size, max_batches, created_at, account_id, kind, source, snapshot_json, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, '', $7, '', $8, $9)`,
		id, jobType, cutoffAt, batchSize, maxBatches, createdAt, kind, snapshotJSON, updatedAt); err != nil {
		t.Fatalf("seed row %s: %v", id, err)
	}
}

func t207RowStatus(t *testing.T, db *sql.DB, id string) (string, int, int64) {
	t.Helper()
	var status string
	var attemptCount int
	var nextAttemptAtMs int64
	if err := db.QueryRow(`SELECT status, attempt_count, next_attempt_at_ms FROM juhe_dataset.record_maintenance_jobs WHERE id = $1`, id).
		Scan(&status, &attemptCount, &nextAttemptAtMs); err != nil {
		t.Fatalf("read row %s: %v", id, err)
	}
	return status, attemptCount, nextAttemptAtMs
}

func t207RowCount(t *testing.T, db *sql.DB, where string, args ...any) int {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM juhe_dataset.record_maintenance_jobs WHERE `+where, args...).Scan(&count); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	return count
}

// TestT207PoisonRowDoesNotBlockQueue：未知 type 毒丸行置 failed 隔离出队，
// 后续合法行照常消费；failed 行不再入选，重复 drain 幂等。
func TestT207PoisonRowDoesNotBlockQueue(t *testing.T) {
	db, store := newQueuePG(t)
	t207SeedRow(t, db, "t207-poison", "totally_unknown_type", "", 0, 0, "2026-11-13T00:00:00.000Z", "", "")
	t207SeedRow(t, db, "t207-good", "usage_records_cleanup", "2026-11-13T00:00:00.000Z", 500, 10, "2026-11-13T00:01:00.000Z", "", "")
	runner := &mockRunner{}
	drainer := &Drainer{Store: store, Runner: runner, Logger: discardLogger()}

	processed, err := drainer.DrainOnce(context.Background())
	if err != nil {
		t.Fatalf("drain once: %v", err)
	}
	if processed != 1 {
		t.Fatalf("processed = %d want 1（毒丸行不阻塞后续行）", processed)
	}
	status, _, _ := t207RowStatus(t, db, "t207-poison")
	if status != "failed" {
		t.Fatalf("毒丸行 status = %q want failed", status)
	}
	if ran := runner.ranIDs(); len(ran) != 1 || ran[0] != "t207-good" {
		t.Fatalf("执行器应只执行合法行: %v", ran)
	}
	if got := t207RowCount(t, db, `id = $1`, "t207-good"); got != 0 {
		t.Fatalf("合法行执行成功后应删行，剩余 %d", got)
	}
	// failed 行不再入选：再次 drain 空转。
	processed, err = drainer.DrainOnce(context.Background())
	if err != nil {
		t.Fatalf("second drain: %v", err)
	}
	if processed != 0 {
		t.Fatalf("second processed = %d want 0（failed 行不再被消费）", processed)
	}
	if got := t207RowCount(t, db, `id = $1`, "t207-poison"); got != 1 {
		t.Fatalf("failed 行保留在表内供排查/保留清理, count = %d", got)
	}
}

// TestT207TransientFailureBackoff：瞬时失败行按退避阶梯保留 pending；退避未
// 到期不再入选；到期后重试成功即删行。attempt_count 递增且封顶 3600s。
func TestT207TransientFailureBackoff(t *testing.T) {
	db, store := newQueuePG(t)
	t207SeedRow(t, db, "t207-flaky", "usage_records_cleanup", "2026-11-13T00:00:00.000Z", 500, 10, "2026-11-13T00:00:00.000Z", "", "")
	runner := &mockRunner{failFor: map[string]error{"t207-flaky": errTransient}}
	drainer := &Drainer{Store: store, Runner: runner, Logger: discardLogger()}

	now := time.Now()
	store.now = func() time.Time { return now }

	if _, err := drainer.DrainOnce(context.Background()); err != nil {
		t.Fatalf("drain once: %v", err)
	}
	status, attempt, nextMs := t207RowStatus(t, db, "t207-flaky")
	if status != "pending" || attempt != 1 {
		t.Fatalf("瞬时失败行应保留 pending attempt=1, got %s/%d", status, attempt)
	}
	if want := now.UnixMilli() + 60_000; nextMs != want {
		t.Fatalf("第一次退避 next_attempt_at_ms = %d want %d", nextMs, want)
	}
	// 退避未到期：Dequeue 不再选中（不再本轮空转重试）。
	jobs, err := store.Dequeue(context.Background(), 10)
	if err != nil {
		t.Fatalf("dequeue: %v", err)
	}
	if len(jobs) != 0 {
		t.Fatalf("退避未到期仍入选 %d 行, want 0", len(jobs))
	}
	// 推进时钟越过退避窗口，重试成功后删行。
	runner.setFailure("t207-flaky", nil)
	store.now = func() time.Time { return now.Add(61 * time.Second) }
	processed, err := drainer.DrainOnce(context.Background())
	if err != nil {
		t.Fatalf("retry drain: %v", err)
	}
	if processed != 1 {
		t.Fatalf("退避到期重试 processed = %d want 1", processed)
	}
	if got := t207RowCount(t, db, `id = $1`, "t207-flaky"); got != 0 {
		t.Fatalf("重试成功后应删行, count = %d", got)
	}
}

// TestT207BackoffLadderCapsAtOneHour：直接驱动 RetryWithBackoff 锁定阶梯
// 60s/300s/900s/3600s 封顶（Store 时钟注入固定值，断言精确到毫秒）。
func TestT207BackoffLadderCapsAtOneHour(t *testing.T) {
	db, store := newQueuePG(t)
	t207SeedRow(t, db, "t207-ladder", "usage_records_cleanup", "2026-11-13T00:00:00.000Z", 500, 10, "2026-11-13T00:00:00.000Z", "", "")
	now := time.Now()
	store.now = func() time.Time { return now }
	for attempt, wantDelay := range []int64{60_000, 300_000, 900_000, 3_600_000, 3_600_000} {
		if err := store.RetryWithBackoff(context.Background(), "t207-ladder"); err != nil {
			t.Fatalf("retry backoff #%d: %v", attempt+1, err)
		}
		_, got, nextMs := t207RowStatus(t, db, "t207-ladder")
		if got != attempt+1 {
			t.Fatalf("attempt_count = %d want %d", got, attempt+1)
		}
		if want := now.UnixMilli() + wantDelay; nextMs != want {
			t.Fatalf("第 %d 次退避 next_attempt_at_ms = %d want %d", attempt+1, nextMs, want)
		}
	}
}

// TestT207SnapshotCorruptRowIsolatedInDequeue：snapshot_json 损坏（确定性缺陷）
// 在读面置 failed 隔离，同批后续行照常读出。
func TestT207SnapshotCorruptRowIsolatedInDequeue(t *testing.T) {
	db, store := newQueuePG(t)
	t207SeedRow(t, db, "t207-corrupt", "account_usage_snapshot_upsert", "", 0, 0, "2026-11-13T00:00:00.000Z", "openai_codex", "{not-json")
	t207SeedRow(t, db, "t207-valid", "account_usage_snapshot_upsert", "", 0, 0, "2026-11-13T00:01:00.000Z", "openai_codex", `{"codex_usage_updated_at":"2026-11-13T00:00:00.000Z"}`)
	jobs, err := store.Dequeue(context.Background(), 10)
	if err != nil {
		t.Fatalf("dequeue: %v", err)
	}
	if len(jobs) != 1 || jobs[0].ID != "t207-valid" {
		t.Fatalf("损坏行应被隔离，仅读出合法行: %+v", jobs)
	}
	status, _, _ := t207RowStatus(t, db, "t207-corrupt")
	if status != "failed" {
		t.Fatalf("损坏行 status = %q want failed", status)
	}
}

// TestT207SnapshotPoisonKindIsolated：未知 kind 的快照行在执行前置 failed，
// 段内合法行照常执行删行。
func TestT207SnapshotPoisonKindIsolated(t *testing.T) {
	db, store := newQueuePG(t)
	t207SeedRow(t, db, "t207-bad-kind", "account_usage_snapshot_upsert", "", 0, 0, "2026-11-13T00:00:00.000Z", "future_kind", `{"k":"v"}`)
	t207SeedRow(t, db, "t207-ok-kind", "account_usage_snapshot_upsert", "", 0, 0, "2026-11-13T00:01:00.000Z", "openai_codex", `{"k":"v"}`)
	runner := &mockRunner{}
	drainer := &Drainer{Store: store, Runner: runner, Logger: discardLogger()}
	processed, err := drainer.DrainOnce(context.Background())
	if err != nil {
		t.Fatalf("drain once: %v", err)
	}
	if processed != 1 {
		t.Fatalf("processed = %d want 1（毒丸 kind 不阻塞段内合法行）", processed)
	}
	status, _, _ := t207RowStatus(t, db, "t207-bad-kind")
	if status != "failed" {
		t.Fatalf("未知 kind 行 status = %q want failed", status)
	}
	if ran := runner.ranIDs(); len(ran) != 1 || ran[0] != "t207-ok-kind" {
		t.Fatalf("执行器应只执行合法 kind 行: %v", ran)
	}
}

// TestT207RetentionCleanupJobsBefore：保留清理按 created_at 删超龄行
//（含 pending/failed），窗口内行保留。
func TestT207RetentionCleanupJobsBefore(t *testing.T) {
	db, _ := newQueuePG(t)
	t207SeedRow(t, db, "t207-old-pending", "usage_records_cleanup", "2026-08-01T00:00:00.000Z", 500, 10, "2026-08-01T00:00:00.000Z", "", "")
	t207SeedRow(t, db, "t207-old-poison", "totally_unknown_type", "", 0, 0, "2026-08-02T00:00:00.000Z", "", "")
	if _, err := db.Exec(`UPDATE juhe_dataset.record_maintenance_jobs SET status = 'failed' WHERE id = $1`, "t207-old-poison"); err != nil {
		t.Fatalf("mark failed: %v", err)
	}
	t207SeedRow(t, db, "t207-fresh", "usage_records_cleanup", "2026-11-13T00:00:00.000Z", 500, 10, "2026-11-13T00:00:00.000Z", "", "")

	deleted, err := CleanupJobsBefore(context.Background(), db, "2026-10-09T00:00:00.000Z", 100)
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if deleted != 2 {
		t.Fatalf("deleted = %d want 2（超龄 pending/failed 行都删）", deleted)
	}
	if got := t207RowCount(t, db, `id = $1`, "t207-fresh"); got != 1 {
		t.Fatalf("窗口内行应保留, fresh count = %d", got)
	}
	// 全表超龄时最终归零。
	deleted, err = CleanupJobsBefore(context.Background(), db, "2026-11-13T00:01:00.000Z", 100)
	if err != nil {
		t.Fatalf("cleanup 2: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("deleted = %d want 1", deleted)
	}
	if got := t207RowCount(t, db, `true`); got != 0 {
		t.Fatalf("生产者停止后表应归零, count = %d", got)
	}
}

// errTransient 模拟执行器运行期失败（DB/上游类）。
var errTransient = &t207TransientError{}

type t207TransientError struct{}

func (*t207TransientError) Error() string { return "下游统计写入暂不可用" }
