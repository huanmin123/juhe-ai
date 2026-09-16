package recordmaintenance

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/retention"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// w12b_pg_gate_test.go 门禁化开发 PostgreSQL（w1cover 临时覆盖库）覆盖
// recordmaintenance 的 PG 分支：bind $N 改写、Table PG 前缀、existingColumns
// information_schema 臂、EnsureSchema PG 建表/幂等升级、Dequeue/Delete 的
// PG SQL 与 DrainOnce 全链路。数据全部 w12b- 前缀 ID，测试结束清理；
// 数据库不可达时 t.Skip。
//
// PG 补列臂（ALTER TABLE ... ADD COLUMN IF NOT EXISTS）：仅当共享表缺 v2 列
// 时执行；w1cover 表通常已带全列（EnsureSchema 幂等建表即全列），该臂由
// SQLite 旧表升级测试覆盖等价逻辑，PG 侧不为此改动共享表结构。

func w12bRMPgSkip(reason string) string { return "w12b recordmaintenance PG gated: " + reason }

func w12bRMPgDSN(t *testing.T) string {
	t.Helper()
	if url := os.Getenv("JUHE_AI_W12B_RECORDMAINTENANCE_PG_URL"); url != "" {
		return url
	}
	raw, err := os.ReadFile(`F:\sub2api-lite\.local\project-resources\dev\env\shared.env`)
	if err != nil {
		t.Skip(w12bRMPgSkip("shared.env 不可读"))
	}
	base := ""
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "JUHE_AI_POSTGRES_URL=") {
			base = strings.TrimPrefix(line, "JUHE_AI_POSTGRES_URL=")
		}
	}
	if base == "" {
		t.Skip(w12bRMPgSkip("shared.env 缺少 JUHE_AI_POSTGRES_URL"))
	}
	base = strings.Replace(base, ":6432/", ":5432/", 1)
	base = strings.Replace(base, "/juhe_ai_sub2api_dev?", "/juhe_ai_sub2api_dev_w1cover?", 1)
	return base
}

func w12bOpenPgStore(t *testing.T) *Store {
	t.Helper()
	db, err := w12bPgOpen(t)
	if err != nil {
		t.Skipf(w12bRMPgSkip("PG 打开失败: %v"), err)
	}
	store, err := OpenStore(db, true)
	if err != nil {
		t.Skipf(w12bRMPgSkip("OpenStore 失败: %v"), err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return store
}

func w12bPgOpen(t *testing.T) (*sql.DB, error) {
	t.Helper()
	db, err := sql.Open("pgx", w12bRMPgDSN(t))
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func TestW12bPgQueueAndDrain(t *testing.T) {
	store := w12bOpenPgStore(t)
	ctx := context.Background()

	if store.Table() != PGSchema+"."+TableName {
		t.Fatalf("PG 表名不符: %s", store.Table())
	}
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatalf("PG EnsureSchema 失败: %v", err)
	}
	// 幂等：再次执行走 ensured 短路（并发安全由 mu 保证）。
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatalf("PG EnsureSchema 幂等失败: %v", err)
	}

	t.Cleanup(func() {
		_, _ = store.db.ExecContext(context.Background(),
			`DELETE FROM `+store.Table()+` WHERE id LIKE 'w12b-%'`)
	})

	// 混合队列：普通清理任务 + 快照任务（snapshot_json 载荷）。
	if _, err := store.db.ExecContext(ctx, store.bind(`INSERT INTO `+store.Table()+`
		(id, type, cutoff_at, batch_size, max_batches, created_at, account_id, kind, source, snapshot_json, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		"w12b-rm-pg-clean", retention.JobTypeNonBusinessDataCleanup, "2026-09-17T00:00:00.000Z",
		1, 1, "2026-09-17T08:00:00.000Z", "", "", "", "", ""); err != nil {
		t.Fatalf("PG 插入清理行失败: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, store.bind(`INSERT INTO `+store.Table()+`
		(id, type, cutoff_at, batch_size, max_batches, created_at, account_id, kind, source, snapshot_json, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		"w12b-rm-pg-snap", retention.JobTypeAccountUsageSnapshotUpsert, "2026-09-17T00:00:00.000Z",
		1, 1, "2026-09-17T08:00:01.000Z", "w12b-acc-1", "usage_daily", "test",
		`{"bucket":"w12b"}`, "2026-09-17T08:00:01.000Z"); err != nil {
		t.Fatalf("PG 插入快照行失败: %v", err)
	}

	jobs, err := store.Dequeue(ctx, 10)
	if err != nil {
		t.Fatalf("PG Dequeue 失败: %v", err)
	}
	if len(jobs) != 2 {
		t.Fatalf("应读出 2 行: %d", len(jobs))
	}
	if jobs[1].ID != "w12b-rm-pg-snap" || jobs[1].Snapshot == nil || jobs[1].Snapshot["bucket"] != "w12b" {
		t.Fatalf("PG 快照行载荷不符: %+v", jobs[1])
	}

	// DrainOnce 全链路：Runner 记录后删行。
	runner := &mockRunner{}
	drainer := &Drainer{Store: store, Runner: runner, Logger: w12bDiscardLogger()}
	processed, err := drainer.DrainOnce(ctx)
	if err != nil {
		t.Fatalf("PG DrainOnce 失败: %v", err)
	}
	if processed != 2 {
		t.Fatalf("应处理并删除 2 行: %d", processed)
	}
	if got := pendingCount(t, store); got != 0 {
		t.Fatalf("PG 队列应已排空: %d", got)
	}
}

func TestW12bPgDrainShutdownArm(t *testing.T) {
	store := w12bOpenPgStore(t)
	ctx := context.Background()
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = store.db.ExecContext(context.Background(),
			`DELETE FROM `+store.Table()+` WHERE id LIKE 'w12b-%'`)
	})
	if _, err := store.db.ExecContext(ctx, store.bind(`INSERT INTO `+store.Table()+`
		(id, type, cutoff_at, batch_size, max_batches, created_at, account_id, kind, source, snapshot_json, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		"w12b-rm-pg-shutdown", retention.JobTypeNonBusinessDataCleanup, "2026-09-17T00:00:00.000Z",
		1, 1, "2026-09-17T08:00:00.000Z", "", "", "", "", ""); err != nil {
		t.Fatal(err)
	}
	drainer := &Drainer{Store: store, Runner: &mockRunner{}, Logger: w12bDiscardLogger()}
	if got := drainer.DrainShutdown(1); got != 1 {
		t.Fatalf("停机排空应处理 1 行: %d", got)
	}
}
