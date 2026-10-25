package auditlog

// pgkit_test.go 是 auditlog 建库测试迁真实 PG 的共享基建
// （PLAN-20261024T051925945Z B5 auditlog 测试迁移切片）：pgtestkit 每测试
// 独立库 + 生产 store 的 PG 臂（OpenStore ModePostgres，首用时 EnsureSchema
// 建 juhe_dataset schema，与生产启动时序一致）。
//
// 分组（D9/D17，2026-10-24 存储形态收口后为单一形态）：
//   - 建库走本文件 helper，测试体内手写 SQL 与生产 PG 臂对齐
//     （juhe_dataset schema 限定、$n 占位符、timestamptz 以 time.Time 回读）。
//   - 原「组B」（SQLite-only 生产臂：PRAGMA/schema 内省、ref_count 本地计数
//     维护、legacy SQLite 迁移链、触发器故障注入、文件形态库隔离校验）已随
//     sqlite 生产臂同批退役。
//
// 连接串由 pgtestkit 自 .local dev env 解析，内容不进入任何文件、日志或断言。

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

// openPostgresStoreConfig 创建 pgtestkit 隔离 PG 测试库并打开生产 store 的
// PG 形态，返回 store、实现句柄与 LoadConfig 产物（测试可再覆盖字段）。
// prepare 预建业务设置热读的 juhe_business.system_settings 空表（列与业务库
// PG DDL 同构；缺行回落 env 固化值）。
// 测试结束先关池、后删库（注册顺序晚于 pgtestkit，Cleanup LIFO）。
func openPostgresStoreConfig(t *testing.T) (Store, *sqlStore, Config) {
	t.Helper()
	testDB := pgtestkit.NewTestDatabase(t, func(ctx context.Context, db *sql.DB) error {
		_, err := db.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS juhe_business;
CREATE TABLE IF NOT EXISTS juhe_business.system_settings (
  system_account_id text NOT NULL, key text NOT NULL, value_json text NOT NULL, updated_at text NOT NULL,
  PRIMARY KEY (system_account_id, key))`)
		return err
	})
	root := t.TempDir()
	env := map[string]string{
		"JUHE_AI_AUDIT_LOG_INSTANCE_ID":           "f3-test-instance",
		"JUHE_AI_AUDIT_LOG_STORE":                 "postgres",
		"JUHE_AI_AUDIT_LOG_POSTGRES_URL":          testDB.DSN,
		"JUHE_AI_AUDIT_LOG_BLOB_DIRECTORY":        filepath.Join(root, "audit-blobs"),
		"JUHE_AI_AUDIT_LOG_BUSINESS_SETTINGS_URL": testDB.DSN,
	}
	cfg, err := LoadConfig(func(name string) string { return env[name] })
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, store.(*sqlStore), cfg
}

// openPostgresStore 是不需要 Config 的组A测试入口。
func openPostgresStore(t *testing.T) (Store, *sqlStore) {
	store, implementation, _ := openPostgresStoreConfig(t)
	return store, implementation
}
