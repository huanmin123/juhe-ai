package accountquality

// pgkit_test.go 是 accountquality 统计存储测试迁真实 PG 的共享基建
// （PLAN-20261024T051925945Z SQLite 与内存模式彻底退役，accountquality 测试
// 切片）：pgtestkit 每测试独立库 + 生产 store 的 StatsPostgres 臂
// （OpenStatsStore Mode=StatsPostgres 注入句柄，EnsureSchema 走包内
// postgresQualitySchema——与 maintenance juhe_stats 冻结 DDL 同形的生产建表
// 路径）。
//
// 三组分类（D9/D17；组B 已于 2026-10-25 随 StatsSQLite 生产臂物理删除退役）：
//   - 组A（StatsPostgres 生产臂，断言逐字节保持）：store_refresh /
//     w12h store matrix·arms·sweep 中方言中立的存储行为族与 refresh 编排族
//     走本文件 newQualityStorePostgres；测试体手写 SQL 对齐生产 PG 臂
//     （juhe_stats schema 限定、$n 占位符、text 时间列原样绑定）。
//   - 组B（StatsSQLite 生产臂为被测对象：newQualityStore SQLite 夹具与依赖
//     SQLite 弱类型 / 触发器注入 / Close 关闭语义 / PRAGMA 校验 / sqlite 假
//     句柄错误臂的测试——损坏行读取矩阵、触发器中止矩阵、关闭句柄与目录
//     路径校验、PGModeStoreHelpers 假句柄臂）无 PG 等价断言，随臂退役。
//   - scripted mock（mockProber / mockReader / fakeLogger 等）与纯函数测试
//     不依赖真库，保持原状。
//
// 连接串由 pgtestkit 自 .local dev env 解析，内容不进入任何文件、日志或断言。

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

// newQualityStorePostgres 在 pgtestkit 隔离 PG 测试库上打开生产 store 的
// StatsPostgres 形态。prepare 先建 juhe_stats schema：postgresQualitySchema
// 只建表不建 schema（生产时序中 schema 由 maintenance 预置），测试与生产
// 启动顺序一致。
func newQualityStorePostgres(t *testing.T) (*StatsStore, *fakeClock, *mapBusinessLookup) {
	t.Helper()
	db := pgtestkit.New(t, func(ctx context.Context, db *sql.DB) error {
		_, err := db.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS juhe_stats`)
		return err
	})
	store, err := OpenStatsStore(StatsStoreConfig{Mode: StatsPostgres, PostgresDB: db})
	if err != nil {
		t.Fatal(err)
	}
	clock := &fakeClock{current: time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC)}
	store.SetClock(clock)
	lookup := &mapBusinessLookup{accounts: map[string]AccountMetadata{}}
	store.SetBusiness(lookup)
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	return store, clock, lookup
}
