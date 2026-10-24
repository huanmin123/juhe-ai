package circuitstore

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// TestEnqueueMissingSubTransactionsBatching 覆盖 EnqueueMissing 拆批短事务：
// 45 个缺失账户（> enqueueSubTxRows）分 20+20+5 三个子事务全部入队，重复调用
// 排空为 0（扫描以"未标脏"为过滤条件的幂等续跑语义）。
func TestEnqueueMissingSubTransactionsBatching(t *testing.T) {
	repo, db := openListAvailabilityFixture(t)
	ctx := context.Background()
	nowMS := time.Date(2026, 9, 4, 10, 0, 0, 0, time.UTC).UnixMilli()
	const accountCount = 45
	values := ""
	for i := 0; i < accountCount; i++ {
		if i > 0 {
			values += ","
		}
		values += fmt.Sprintf("('acc-batch-%02d', 'viewer-1', '2026-01-01T00:%02d:00.000Z')", i, i)
	}
	if _, err := db.Exec(`INSERT INTO juhe_business.accounts (id, system_account_id, created_at) VALUES ` + values); err != nil {
		t.Fatal(err)
	}
	enqueued, err := repo.EnqueueMissing(ctx, accountCount, nowMS)
	if err != nil {
		t.Fatal(err)
	}
	if enqueued != accountCount {
		t.Fatalf("跨子事务累计应入队 %d: %d", accountCount, enqueued)
	}
	var dirtyCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM juhe_business.account_list_availability_dirty WHERE reason = 'projection_missing'`).Scan(&dirtyCount); err != nil {
		t.Fatal(err)
	}
	if dirtyCount != accountCount {
		t.Fatalf("dirty 行数应 %d: %d", accountCount, dirtyCount)
	}
	if again, err := repo.EnqueueMissing(ctx, accountCount, nowMS); err != nil || again != 0 {
		t.Fatalf("续跑应排空为 0: %d %v", again, err)
	}
}

// TestEnqueueDueSubTransactionsBatching 覆盖 EnqueueDue 拆批短事务：45 个到期
// 投影分 20+20+5 三个子事务全部转移，重复调用排空为 0。
// 组B保留（不迁 PG）：EnqueueDue 的 PG 分支经 instantParam 把 time.Time 参数
// 与 next_transition_at（text 列）比较，真实 PG 上恒不匹配（生产 PG 臂缺陷，
// 取证见 TestEnqueueDueTransitionsDueProjections 注释），本测试随该臂修复批次
// 再迁 PG；此处保持 SQLite fixture 与断言原状。
func TestEnqueueDueSubTransactionsBatching(t *testing.T) {
	repo, db, _ := w13g5CircuitListFixture(t)
	ctx := context.Background()
	nowMS := time.Date(2026, 9, 4, 10, 0, 0, 0, time.UTC).UnixMilli()
	const accountCount = 45
	accountValues := ""
	projectionValues := ""
	for i := 0; i < accountCount; i++ {
		accountID := fmt.Sprintf("acc-due-%02d", i)
		if i > 0 {
			accountValues += ","
			projectionValues += ","
		}
		accountValues += fmt.Sprintf("('%s', 'viewer-1')", accountID)
		projectionValues += fmt.Sprintf(
			"('viewer-1', '%s', 'active', 'enabled', 'api_key', '%s', 0, 0, 0, 0, '2026-01-01T00:00:00.000Z', '{}', 1, '2026-09-04T09:00:00.000Z', '2026-09-04T09:00:00.000Z')",
			accountID, accountID)
	}
	if _, err := db.Exec(`INSERT INTO accounts (id, system_account_id) VALUES ` + accountValues); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO account_list_availability_projections (
			viewer_system_account_id, account_id, effective_status, schedulable_bucket,
			account_type, name_sort_key,
			priority_sort_key, super_priority_sort_key, fallback_sort_key, concurrency_sort_key,
			created_at_sort_key, payload_json, source_generation, next_transition_at, projected_at
		) VALUES ` + projectionValues); err != nil {
		t.Fatal(err)
	}
	enqueued, err := repo.EnqueueDue(ctx, accountCount, nowMS)
	if err != nil {
		t.Fatal(err)
	}
	if enqueued != accountCount {
		t.Fatalf("跨子事务累计应入队 %d: %d", accountCount, enqueued)
	}
	if again, err := repo.EnqueueDue(ctx, accountCount, nowMS); err != nil || again != 0 {
		t.Fatalf("续跑应排空为 0: %d %v", again, err)
	}
}
