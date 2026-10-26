package circuitstore

// circuitstore_pgkit_test.go 是本包真实建库测试迁 PG 的共享基建
// （PLAN-20261024T051925945Z 测试迁移切片）：
//   - openControlPlaneFixture / openListAvailabilityFixture / newLoaderTestDB
//     三个真实建库 fixture 经 pgtestkit 隔离 PG 测试库装配（生产 Postgres: true
//     臂，业务表位于 juhe_business schema、统计表位于 juhe_stats schema，
//     列名/列型与 maintenance pg_schema_business_tables.go 的生产 PG DDL 一一
//     对齐；CHECK/FK 不复刻——cleanuprepo/runtimelog 先例同风格，且孤儿行
//     防御臂测试需要允许缺账户父行的种子）。
//   - 句柄经 sqldialect.OpenDB 装配（与生产 jobs pgpool 同形）：repo 方言共享
//     SQL 以顺序 `?` 占位符直发，ControlPlaneRepo 无 bind 包装，生产依赖
//     driver 层兜底改写；测试必须运行在同一 driver 路径上。测试体内手写
//     SQL 仍按 PG 方言写 $n 与 schema 限定，两层互不干扰。
//   - 本切片不迁、保持原状的部分：
//     1) 失败注入/scripted 假驱动（w13g5CircuitFixture SQLite 注入族与
//     w14jPGFixture PG 脚本化注入臂，其 PG 化留删臂批次统一裁决）；
//     2) 组B（生产臂缺陷/适配缺口取证后保留 SQLite 原状，随生产臂修复批次再
//     迁，各测试注释披露证据）：a) EnqueueDue 到期转移链——PG 分支经
//     instantParam 把 time.Time 参数与 next_transition_at（text 列）比较，
//     真实 PG 上无报错但恒不匹配（time.Time 以 Go 默认文本形态参与字典序
//     比较），PG 模式到期转移静默失效（TestEnqueueDueTransitionsDueProjections
//     注释）；b) loader 族（ProjectionItemLoader 读面）——生产
//     juhe_stats.account_usage_snapshots 时间列为 timestamptz，loader 读 SQL
//     裸扫 text 断言，同族适配缺口（TestHydratePageFailClosed 注释）。
//     原门禁化共享 cover 库的 w15_claim_dirty_pg_test.go（env-gated skip）
//     已迁入本文件 kit 夹具。
//   - ListAvailabilityRepo 的 ClaimDirty/ReleaseForReplay PG 分支租约时间由
//     DB clock_timestamp() 驱动（Node 同语义），不消费注入时钟：相关断言按
//     真实时钟重钉，重钉点在各测试内注释披露；ControlPlaneRepo 全部方法时间
//     由参数驱动，断言逐字节保持。

import (
	"context"
	"database/sql"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
	"github.com/huanminabc/juhe-ai/backend-go-platform/sqldialect"
	stdlib "github.com/jackc/pgx/v5/stdlib"
)

// openKitPostgresDB 打开 pgtestkit 隔离 PG 测试库并执行 prepare（建 schema 与
// 测试所需最小表集），再经生产同形的 sqldialect driver 兜底改写包装返回句柄；
// 测试结束自动关闭并删库。连接串内容不进入任何日志或断言。
func openKitPostgresDB(t *testing.T, prepare func(ctx context.Context, db *sql.DB) error) *sql.DB {
	t.Helper()
	testDB := pgtestkit.NewTestDatabase(t, prepare)
	db, err := sqldialect.OpenDB(stdlib.GetDefaultDriver(), testDB.DSN)
	if err != nil {
		t.Fatalf("打开 sqldialect 包装句柄失败: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
