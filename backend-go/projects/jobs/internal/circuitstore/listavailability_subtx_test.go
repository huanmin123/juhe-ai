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

