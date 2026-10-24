package runtimelog

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

// runtimelog_pgkit_test.go 是本包真实建库测试迁 PG 的共享基建
// （PLAN-20261024T051925945Z B6 测试迁移半场）：
//   - postgresStore 经生产 OpenStore(ModePostgres) 装配到 pgtestkit 隔离测试库，
//     runtime-log F1 表由生产 EnsureSchema bootstrap DDL 自建，测试不复刻 DDL；
//   - 仅 juhe_business.system_settings（业务域表，其生产 bootstrap 属
//     maintenance 跨模块）按 RuntimeRetentionDays 生产查询消费的最小列形状镜像；
//   - pgfake 脚本化/错误注入臂（wn_pgfake_test.go 及其消费方）与 SQLite-only
//     生产臂测试（组B，随生产臂删除批次退役）不在本迁移面，保持原状。

// kitBusinessSettingsSeed 与 openTestSQLiteStore 的业务设置种子同值，
// 保证迁移前后 RuntimeRetentionDays 读取面行为一致（14 天）。
const kitBusinessSettingsSeed = `INSERT INTO juhe_business.system_settings (system_account_id, key, value_json, updated_at)
VALUES ('sys_admin', 'runtimeLogIndexRetentionDays', '14', '2026-08-08T00:00:00.000Z')`

// openKitPostgresStore 打开 pgtestkit 隔离 PG 测试库并经生产 OpenStore 装配
// postgresStore（EnsureSchema 走生产 bootstrap）。Config 字段与
// openTestSQLiteStore 逐项对齐（BatchSize/保留期/日志目录），仅存储臂换成
// ModePostgres。测试结束自动关闭 store 并删库。
func openKitPostgresStore(t *testing.T) (*postgresStore, Config) {
	t.Helper()
	testDB := pgtestkit.NewTestDatabase(t, func(ctx context.Context, db *sql.DB) error {
		_, err := db.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS juhe_business;
CREATE TABLE IF NOT EXISTS juhe_business.system_settings (
  system_account_id text NOT NULL,
  key text NOT NULL,
  value_json text NOT NULL,
  updated_at text NOT NULL,
  PRIMARY KEY (system_account_id, key)
)`)
		if err != nil {
			return err
		}
		_, err = db.ExecContext(ctx, kitBusinessSettingsSeed)
		return err
	})
	config := Config{
		Mode:              ModePostgres,
		PostgresURL:       testDB.DSN,
		PostgresMaxConns:  4,
		LogDirectory:      t.TempDir(),
		FileEnabled:       true,
		PollInterval:      time.Second,
		RetentionInterval: time.Hour,
		RetentionDays:     1,
		LogRetentionDays:  30,
		LogMaxFiles:       500,
		BatchSize:         2,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	opened, err := OpenStore(ctx, config)
	if err != nil {
		t.Fatalf("打开 kit PostgreSQL store 失败: %v", err)
	}
	store, ok := opened.(*postgresStore)
	if !ok {
		_ = opened.Close()
		t.Fatalf("OpenStore(ModePostgres) 返回 %T，期望 *postgresStore", opened)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := EnsureSchema(ctx, store); err != nil {
		t.Fatal(err)
	}
	return store, config
}

// assertKitRuntimeLogCount 是 assertRuntimeLogCount 的 PG 臂变体
// （juhe_dataset schema 限定），断言语义与失败消息逐字节一致。
func assertKitRuntimeLogCount(t *testing.T, store *postgresStore, want int) {
	t.Helper()
	var actual int
	if err := store.pool.QueryRow(context.Background(), "SELECT COUNT(*) FROM juhe_dataset.runtime_logs").Scan(&actual); err != nil {
		t.Fatal(err)
	}
	if actual != want {
		t.Fatalf("runtime_logs count = %d, want %d", actual, want)
	}
}

// kitFindCursor 是 findCursor 的 PG 臂变体（走 Store 接口），断言语义与
// 失败消息逐字节一致。
func kitFindCursor(t *testing.T, store *postgresStore, path string) Cursor {
	t.Helper()
	cursor, err := store.FindCursor(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if cursor == nil {
		t.Fatalf("未找到 %s 的运行日志 cursor", path)
	}
	return *cursor
}
