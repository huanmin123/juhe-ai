package accounthealth

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// w15b：EnsureSchema 成功后按实例记忆化（BUG-0307）。
//
// 生产实证（2026-10-11，pg_stat_statements）：AcquireOwnerLease 每次租约
// 获取/续约都全量执行 EnsureSchema，其中的 ALTER TABLE ADD COLUMN IF NOT
// EXISTS 在 PG 即使列已存在也要先取 ACCESS EXCLUSIVE 锁——3 天 400 次调用，
// 锁队列等待最长 143s，冻结 account_health_current_state 全部读写，并把已
// 持有 accounts 行锁的 J1 结算事务连带卡死，/v1 候选选择 FOR UPDATE 跟着
// 阻塞（当日 09:06 一条 /v1/responses 阻塞 366s 后 503）。
// 修复后：成功一次即记忆；失败不记忆，下次重试。
// ---------------------------------------------------------------------------

func TestW15bEnsureSchemaMemoizesSuccessPerStore(t *testing.T) {
	store, err := OpenStore(StoreConfig{Mode: StoreSQLite, DatabasePath: filepath.Join(t.TempDir(), "w15b-schema.sqlite3")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()

	if store.schemaReady.Load() {
		t.Fatal("新 Store 不应预置 schemaReady")
	}
	for round := 1; round <= 3; round++ {
		if err := store.EnsureSchema(ctx); err != nil {
			t.Fatalf("第 %d 次 EnsureSchema 失败: %v", round, err)
		}
	}
	if !store.schemaReady.Load() {
		t.Fatal("成功后必须记忆 schemaReady")
	}

	// 热路径（租约续约）走记忆化：不再触发 DDL，租约功能正常。
	lease, acquired, err := store.AcquireOwnerLease(ctx, "w15b-owner", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("首次 AcquireOwnerLease: acquired=%v err=%v", acquired, err)
	}
	if lease.FenceToken < 1 {
		t.Fatalf("fence token 异常: %d", lease.FenceToken)
	}
}

func TestW15bEnsureSchemaFailureDoesNotMemoize(t *testing.T) {
	store, err := OpenStore(StoreConfig{Mode: StoreSQLite, DatabasePath: filepath.Join(t.TempDir(), "w15b-fail.sqlite3")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	// 破坏底层库使首次 EnsureSchema 失败，验证失败不记忆、恢复后可重试成功。
	if err := store.db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureSchema(context.Background()); err == nil {
		t.Fatal("底层库已关闭，EnsureSchema 必须失败")
	}
	if store.schemaReady.Load() {
		t.Fatal("失败不得记忆 schemaReady")
	}
}
