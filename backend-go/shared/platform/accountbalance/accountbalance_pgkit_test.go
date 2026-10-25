package accountbalance

// accountbalance_pgkit_test.go 是本包真实建库测试迁 PG 的共享基建
// （PLAN-20261024T051925945Z B7 accountbalance 测试迁移切片）：
//   - 组A（PG 臂被测且断言逐字节保持）经 pgtestkit 每测试独立库 + 生产
//     OpenStore(StorePostgres) 装配（PostgresURL 直开臂，经 sqldialect 改写
//     driver，与生产 jobs 侧装配同形）；juhe_jobs schema 由 prepare 建（生产
//     由 maintenance bootstrap 创建，跨模块边界由调用方承接，pgtestkit 边界
//     约定），四表 DDL 由生产 EnsureSchema 的 PG 臂自建，测试零 DDL 复刻。
//   - 组B（StoreSQLite 臂被测的 w7c_store_sqlite_arms_test.go 与
//     w12h_store_runner_service_arms_test.go SQLite 失败注入族）已于
//     2026-10-25 随生产 sqlite 臂物理删除整体退役（D9 裁决：无 PG 逐字节
//     等价断言）；w7c_pg_gate_test.go / w7c_pg_gate_arms_test.go 中引用
//     SQLite 夹具的注入子臂同批摘除，门禁共享 w1cover 库测试面保持现状
//     （计划 §4.2）。
//   - scripted/mock（httptest、scripted HTTPDoer、fake pool）不依赖真库，
//     原样保留。
//   - 本包 PG 臂的租约/快照时间全部由 Go 侧 time.Now 参数驱动（SQL 语句内
//     无 clock_timestamp()），不存在 DB 时钟 vs 注入时钟的差异面：注入时钟
//     族测试（如 runner 的 Now 注入、business due 的固定时钟）断言逐字节
//     保持，无需重钉。
//   - 连接串由 pgtestkit 自 JUHE_AI_TEST_PG_ADMIN_URL 或 .local dev env 解析，
//     内容不进入任何文件、日志或断言。

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

// openKitPostgresStore 打开 pgtestkit 隔离 PG 测试库（prepare 补建 juhe_jobs
// schema 命名空间），经生产 OpenStore(StorePostgres) 装配并 EnsureSchema 按
// 生产 DDL 建四表；测试结束先关 store、后删库（Cleanup LIFO）。
func openKitPostgresStore(t *testing.T) *Store {
	t.Helper()
	testDB := pgtestkit.NewTestDatabase(t, func(ctx context.Context, db *sql.DB) error {
		_, err := db.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS juhe_jobs`)
		return err
	})
	store, err := OpenStore(StoreConfig{Mode: StorePostgres, PostgresURL: testDB.DSN})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatalf("accountbalance PG EnsureSchema 失败: %v", err)
	}
	return store
}

// kitOwner 是迁移测试的 owner 租约 fixture。
func kitOwner(t *testing.T, store *Store, id string) OwnerLease {
	t.Helper()
	lease, acquired, err := store.AcquireOwnerLease(context.Background(), id, time.Minute)
	if err != nil || !acquired {
		t.Fatalf("acquire owner %s: %v %t", id, err, acquired)
	}
	return lease
}

// kitAccount 是迁移测试的 account 租约 fixture。
func kitAccount(t *testing.T, store *Store, owner OwnerLease, id string) AccountLease {
	t.Helper()
	lease, acquired, err := store.AcquireAccountLease(context.Background(), owner, id, time.Minute)
	if err != nil || !acquired {
		t.Fatalf("acquire account %s: %v %t", id, err, acquired)
	}
	return lease
}

// kitValidBalanceInput 是迁移测试的合法输入 fixture（原组B 同语义 helper
// w7cValidBalanceInput 随 sqlite 臂退役后，HTTP/单元臂测试统一复用本 fixture）。
func kitValidBalanceInput(accountID string) Input {
	now := time.Now().UTC()
	due := now.Add(-time.Minute)
	credential, err := NewCredentialEnvelope("w7c-balance-secret", "api_key", map[string]string{"api_key": "sk-w7c"})
	if err != nil {
		panic(err)
	}
	return Input{
		AccountID: accountID, SystemAccountID: "w7c-sys", InputVersion: 5, ConfigRevision: 5,
		Provider: "openai", Type: "api_key", Status: "active", Schedulable: true,
		BaseURL: "https://example.test", Config: QueryConfig{Adapter: Adapter("builtin"), IntervalMinutes: 5},
		APIKey: credential, Credential: credential,
		Trigger: TriggerPeriodic, IssuedAt: now, ExpiresAt: now.Add(time.Minute), NextRefreshAt: &due,
	}
}
