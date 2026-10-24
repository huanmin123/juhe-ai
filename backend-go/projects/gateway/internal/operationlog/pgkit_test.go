package operationlog

// pgkit_test.go 是 operationlog 建库测试迁真实 PG 的共享基建
// （PLAN-20261024T051925945Z B5 operationlog 测试迁移切片）：pgtestkit 每测试
// 独立库 + 生产 store 的 PG 臂（OpenStore ModePostgres，首用时 EnsureSchema
// 建 juhe_dataset schema，与生产启动时序一致）。
//
// 三组分类（D9/D17）：
//   - 组A（被测对象有 PG 臂且断言可逐字节保持）：建库走本文件 helper；测试
//     体内手写 SQL 与生产 PG 臂对齐（juhe_dataset schema 限定、$n 占位符、
//     timestamptz 经生产 storageTimestamp 回读 canonical UTC 文本）；测试名
//     含「SQLite」且迁移后失实的改名。
//   - 组B（SQLite-only 生产臂：文件形态库隔离、业务镜像/兜底句柄、legacy
//     SQLite 迁移链、PRAGMA、方言 bind 的 sqlite 分支）：按 D17 不提前迁移
//     也不提前删除，继续走 createBusinessSettings / wiOpenSQLiteStore 等文件
//     夹具，随其生产臂删除批次同批退役。
//   - scripted 假驱动（w9bScript、w11f/w14h 故障注入包装驱动、伪 catalog）
//     与 fake store（renewGraceFakeStore、w9bFakeStore、fakeStore）不依赖真
//     库，保持原状。
//
// 连接串由 pgtestkit 自 .local dev env 解析，内容不进入任何文件、日志或断言。

import (
	"context"
	"database/sql"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

// pgBusinessFixtureAccounts 与 createBusinessSettings 的 SQLite 账户种子逐行
// 同构，保证迁移前后账户名断言逐字节一致。
var pgBusinessFixtureAccounts = []struct{ id, displayName string }{
	{"actor", "Actor"}, {"other", "Other"}, {"viewer", "Viewer"}, {"owner", "Owner"}, {"actor-1", "Actor 1"},
}

// prepareBusinessFixture 生成 juhe_business 最小形状夹具（列型与 maintenance
// 生产业务 PG DDL 同构：value_json/updated_at 为 text；system_accounts 仅保留
// store 读模型消费的 id/username/display_name 三列）。retentionValue 为空串时
// 不写 retention 行，由测试自行插入或断言回退。
func prepareBusinessFixture(retentionValue string) pgtestkit.PrepareFunc {
	return func(ctx context.Context, db *sql.DB) error {
		script := `CREATE SCHEMA IF NOT EXISTS juhe_business;
CREATE TABLE IF NOT EXISTS juhe_business.system_accounts (id text PRIMARY KEY, username text NOT NULL, display_name text NOT NULL);
CREATE TABLE IF NOT EXISTS juhe_business.system_settings (system_account_id text NOT NULL, key text NOT NULL, value_json text NOT NULL, updated_at text NOT NULL, PRIMARY KEY (system_account_id, key));
INSERT INTO juhe_business.system_accounts (id,username,display_name) VALUES ('actor','actor','Actor'),('other','other','Other'),('viewer','viewer','Viewer'),('owner','owner','Owner'),('actor-1','actor-1','Actor 1')`
		if retentionValue != "" {
			script += `;
INSERT INTO juhe_business.system_settings (system_account_id,key,value_json,updated_at) VALUES ('sys_admin','operationLogRetentionDays','` + retentionValue + `','2026-08-13T00:00:00Z')`
		}
		_, err := db.ExecContext(ctx, script)
		return err
	}
}

// openPostgresStoreFixture 创建隔离 PG 测试库（默认业务夹具）并打开生产
// store 的 PG 形态，返回 store、实现句柄与 pgtestkit 句柄（测试体直读写用）。
// 测试结束先关池、后删库（Cleanup LIFO）。
func openPostgresStoreFixture(t *testing.T, retentionValue string) (Store, *sqlStore, *sql.DB) {
	t.Helper()
	testDB := pgtestkit.NewTestDatabase(t, prepareBusinessFixture(retentionValue))
	store, err := OpenStore(Config{Enabled: true, InstanceID: "pgkit-owner", Mode: ModePostgres, PostgresURL: testDB.DSN})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, store.(*sqlStore), testDB.DB
}

// openPostgresStore 是默认夹具（retention=365，账户种子与 createBusinessSettings
// 同构）的组A测试入口。
func openPostgresStore(t *testing.T) (Store, *sqlStore) {
	store, implementation, _ := openPostgresStoreFixture(t, "365")
	return store, implementation
}

// openW9BCoverStore 是 w9b_pg_gate 门禁族的原夹具承载：w9b 账户显示名与历史
// 断言逐字节一致（Actor PG / Viewer PG），不带 retention 行（测试自插）。
// 返回 pgtestkit 句柄（含 DSN 与直连池）与已装配的生产 store。
func openW9BCoverStore(t *testing.T) (*pgtestkit.TestDatabase, *sqlStore) {
	t.Helper()
	testDB := pgtestkit.NewTestDatabase(t, func(ctx context.Context, db *sql.DB) error {
		_, err := db.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS juhe_business;
CREATE TABLE IF NOT EXISTS juhe_business.system_accounts (id text PRIMARY KEY, username text NOT NULL, display_name text NOT NULL);
CREATE TABLE IF NOT EXISTS juhe_business.system_settings (system_account_id text NOT NULL, key text NOT NULL, value_json text NOT NULL, updated_at text NOT NULL, PRIMARY KEY (system_account_id, key));
INSERT INTO juhe_business.system_accounts (id,username,display_name) VALUES ('actor','actor','Actor PG'),('viewer','viewer','Viewer PG') ON CONFLICT (id) DO UPDATE SET display_name=EXCLUDED.display_name`)
		return err
	})
	store, err := OpenStore(Config{Mode: ModePostgres, PostgresURL: testDB.DSN})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return testDB, store.(*sqlStore)
}
