package proxylatency

import (
	"context"
	"database/sql"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

// proxylatency_pgkit_test.go 是本包真实建库测试迁 PG 的共享基建
// （PLAN-20261024T051925945Z「SQLite 与内存模式彻底退役」proxylatency 切片）：
//   - Store 经生产构造 OpenStore(StoreConfig{Mode: StorePostgres, PostgresURL:
//     pgtestkit 隔离库 DSN}) 装配，与生产 pgpool openDriver 同走
//     sqldialect.OpenDB driver 兜底改写路径（PG 语句自绑 $n，两层互不干扰）；
//     juhe_jobs 六表由生产 EnsureSchema 的 postgresSchema DDL 自建，prepare
//     仅补建外部 bootstrap 契约要求的 schema 命名空间（生产由 maintenance
//     bootstrap 创建，属跨模块边界），测试零 DDL 复刻。
//   - 不在本迁移面、保持原状的部分（各文件头披露证据）：SQLite-only 生产臂
//     测试（组B：CheckSchema/OpenStore sqlite 装配校验、SQLite TEXT 畸形字节
//     篡改注入、projector SQLite 业务库真库面，随生产 sqlite 臂删除批次退役）；
//     scripted 录制/失败注入驱动族（wfOpenRecorderDB/wfNewPGProjector 消费方，
//     PLAN §3 非目标）；env-gated 的 postgres_integration_test.go 冒烟。
//   - PG 臂租约到期判定由 DB statement_timestamp() 驱动（Node 同语义），不
//     消费注入时钟；本切片迁 PG 的夹具时间基准统一为微秒级精度（与 PG
//     timestamptz 微秒存储精度对齐，重钉披露见各测试内注释）。

// openKitPostgresTestDB 打开 pgtestkit 隔离 PG 测试库并补建 juhe_jobs schema
// 命名空间；测试结束自动删库。连接串内容不进入任何日志或断言。
func openKitPostgresTestDB(t *testing.T) *pgtestkit.TestDatabase {
	t.Helper()
	return pgtestkit.NewTestDatabase(t, func(ctx context.Context, db *sql.DB) error {
		_, err := db.ExecContext(ctx, `CREATE SCHEMA juhe_jobs`)
		return err
	})
}

// openStoreOnKitDSN 在既有隔离测试库上经生产构造装配 Store 并按生产 DDL 建表；
// 测试结束自动关闭 store（库生命周期由 pgtestkit cleanup 承接）。
func openStoreOnKitDSN(t *testing.T, dsn string) *Store {
	t.Helper()
	store, err := OpenStore(StoreConfig{Mode: StorePostgres, PostgresURL: dsn, PostgresMaxOpenConns: 4, PostgresMaxIdleConns: 2})
	if err != nil {
		t.Fatalf("打开 PG Store 失败: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("关闭 PG Store 失败: %v", err)
		}
	})
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatalf("PG EnsureSchema 失败: %v", err)
	}
	return store
}

// openKitPostgresStore 每测试一个隔离 PG 库，返回生产装配的 Store（含建表）。
func openKitPostgresStore(t *testing.T) *Store {
	t.Helper()
	return openStoreOnKitDSN(t, openKitPostgresTestDB(t).DSN)
}

// openKitRuntimeStore 以独立 PG 隔离库装配 runner 面 RuntimeConfig 的 Store。
// cfg 的其余字段保持调用方取值；cfg.Store 由本 helper 的生产 PG 装配取代，
// 迁移后测试代码只消费返回的 Store，不再读取 cfg.Store。
func openKitRuntimeStore(t *testing.T, cfg RuntimeConfig) *Store {
	t.Helper()
	return openStoreOnKitDSN(t, openKitPostgresTestDB(t).DSN)
}
