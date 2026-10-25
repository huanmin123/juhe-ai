package circuitcontrolplane

// pgkit_test.go 是 circuit_control_plane 建库测试迁真实 PG 的共享基建
// （PLAN-20261024T051925945Z 测试基建迁移切片）：pgtestkit 每测试独立库 +
// 生产 store 的 PG 臂（New(db, defaultBusinessSchema)，schema 限定
// 与生产一致；maintenance 的 schema 包是 internal，gateway 不可导入，故 prepare
// 内手写与生产同构的建表 DDL——复用 w9e_store_pg_test.go 已对齐生产的 BIGINT
// 形态常量，不做 CHECK/FK 复刻，保证既有测试数据与断言逐字节保持）。
//
// 三组分类（D9/D17）：
//   - 组A（被测对象有 PG 臂且断言可逐字节保持）：建库走本文件 wkPGStoreLabeled；
//     测试体内手写 SQL 与 PG 臂对齐（juhe_business schema 限定、$n 占位符）。
//   - 组B（SQLite-only 生产臂为被测对象）：wkSQLiteLegacyStoreLabeled 保留
//     SQLite 内存建库，供 TestCheckContractPostgresModeOnSQLiteFails（PG 模式
//     契约查询落在 SQLite 库上的必然失败分支）与 w9eDDLDB（SQLite 模式
//     PRAGMA 索引契约分支）继续覆盖，随生产 sqlite 臂删除批次同批退役。
//   - scripted 假驱动（w14g 故障注入包装连接器）与 fakeScanner 不依赖真库，
//     保持原状。
//
// 连接串由 pgtestkit 自 .local dev env 解析，内容不进入任何文件、日志或断言。

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

// wkPGStoreAccountsDDL 与生产 juhe_business.accounts 的控制面契约列同构
// （CheckContract 只要求 id/dispatch_revision/circuit_projection_revision/deleted_at）。
const wkPGStoreAccountsDDL = `CREATE TABLE IF NOT EXISTS ` + defaultBusinessSchema + `.accounts (
 id TEXT PRIMARY KEY, dispatch_revision BIGINT NOT NULL DEFAULT 1,
 circuit_projection_revision BIGINT NOT NULL DEFAULT 0, deleted_at TEXT)`

// wkPGStoreLabeled 创建 pgtestkit 隔离 PG 测试库并打开生产 store 的 PG 形态，
// 与原 SQLite 内存 fixture（wkDDLs）同 schema 同种子：accounts/incidents/outbox
// 三张关系、key_model capability 部分唯一索引、账户 a1。label 仅保留调用点
// 可读性，库唯一性由 pgtestkit 按测试名 + 进程内计数保证。
func wkPGStoreLabeled(t *testing.T, label string, gate OwnerGate) (*Store, *sql.DB) {
	t.Helper()
	_ = label
	testDB := pgtestkit.NewTestDatabase(t, func(ctx context.Context, db *sql.DB) error {
		for _, ddl := range []string{
			`CREATE SCHEMA IF NOT EXISTS ` + defaultBusinessSchema,
			wkPGStoreAccountsDDL,
			w9eCPPIncidentsDDL,
			w9eCPPOutboxDDL,
			w9eCPPCapabilityIndex,
			`INSERT INTO ` + defaultBusinessSchema + `.accounts(id,dispatch_revision,circuit_projection_revision) VALUES ('a1',1,0)`,
		} {
			if _, err := db.ExecContext(ctx, ddl); err != nil {
				return err
			}
		}
		return nil
	})
	s, err := New(testDB.DB, defaultBusinessSchema, gate)
	if err != nil {
		t.Fatal(err)
	}
	return s, testDB.DB
}

// wkSQLiteLegacyStoreLabeled 是组B 专用 fixture：SQLite 内存建库打开 store 的
// SQLite 形态，只供"PG 模式跑在 SQLite 库上必然失败"这类以 SQLite 生产臂为
// 被测对象的测试使用（与迁移前 wkStoreLabeled 逐字节同实现）。
func wkSQLiteLegacyStoreLabeled(t *testing.T, label string, gate OwnerGate) (*Store, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+strings.ReplaceAll(t.Name(), "/", "-")+label+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, ddl := range wkDDLs {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`CREATE UNIQUE INDEX idx_account_circuit_incidents_key_model_capability ON account_circuit_incidents(scope_kind, capability_hash) WHERE scope_kind = 'key_model' AND capability_hash IS NOT NULL`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO accounts(id,dispatch_revision,circuit_projection_revision) VALUES ('a1',1,0)`); err != nil {
		t.Fatal(err)
	}
	s, err := New(db, "", gate)
	if err != nil {
		t.Fatal(err)
	}
	return s, db
}
