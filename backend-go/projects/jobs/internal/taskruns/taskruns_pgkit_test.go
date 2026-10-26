package taskruns

import (
	"context"
	"database/sql"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

// taskruns_pgkit_test.go 是本包真实建库测试迁 PG 的共享基建
// （PLAN-20261024T051925945Z B6 taskruns 切片）：
//   - Store 经生产构造 NewStore(*sql.DB, ModePostgres) 装配到 pgtestkit 隔离
//     测试库；background_task_runs / background_job_leases 两表由生产
//     EnsureSchema 的 juhe_stats 冻结 DDL 自建（schema.go postgresSchema），
//     测试零表 DDL 复刻；prepare 仅补建 schema 命名空间（生产由 maintenance
//     bootstrap 创建，属跨模块边界）。
//   - 不在本迁移面、保持原状的部分：SQLite-only 生产臂测试（组B：OpenStore
//     sqlite 装配/PRAGMA 臂等，随生产 sqlite 臂删除批次退役）；scripted 假
//     驱动（w12bFailDriver 注入族及其消费方，PLAN §3 非目标）。原 env-gated
//     skip 语义的 w12b_pg_gate_test.go（共享 cover 库）已同批迁入本 helper。
//   - scheduled lease 家族（TryAcquire/Renew/Release/AssertScheduledLease）
//     的 PG 臂租约时间由 DB clock_timestamp() 驱动，不消费注入时钟：相关
//     测试的过期/续租时序按真实时钟重钉，重钉点在各测试内注释披露。

// openKitPostgresDB 打开 pgtestkit 隔离 PG 测试库并补建 juhe_stats schema；
// 测试结束自动删库。
func openKitPostgresDB(t *testing.T) *sql.DB {
	t.Helper()
	return pgtestkit.New(t, func(ctx context.Context, db *sql.DB) error {
		_, err := db.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS juhe_stats`)
		return err
	})
}

// openKitPostgresStore 经生产构造装配 ModePostgres Store 并按生产 DDL 建表；
// 时间源保持 SystemClock，需要 FakeClock 的测试自行 SetClock。测试结束自动
// 关闭 store 并删库。
func openKitPostgresStore(t *testing.T) *Store {
	t.Helper()
	store := NewStore(openKitPostgresDB(t))
	t.Cleanup(func() { _ = store.Close() })
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatalf("taskruns PG EnsureSchema 失败: %v", err)
	}
	return store
}
