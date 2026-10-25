package main

// B3（SQLite 与内存模式彻底退役）共享 PG 探针夹具：proberepo 组合根夹具的
// PG 版（原 SQLite 文件夹具随 worker_probe_test.go/wg_circuit_resolve_test.go
// 的 sqlite 臂一并退役）。proberepo PG SQL 经 sqldialect 改写消费 `?`，
// schema 限定 juhe_business 由 DDL 承担。

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"testing"

)

// proberepoStoreHandle 持有 fixture 的底层句柄（proberepo.Store 不导出 db）。
type proberepoStoreHandle struct {
	db *sql.DB
}

// seedProbeCoreTables 在独立 PG 测试库上建探针读取面的核心表（juhe_business
// schema；列集合与生产冻结 schema 同构，TEXT 时间列与生产一致）。
func seedProbeCoreTables(t *testing.T, _ string) {
	t.Helper()
	wgOpenPGDialectDB(t, func(ctx context.Context, db *sql.DB) error {
		_, err := db.Exec(probeCoreTablesPGDDL)
		return err
	})
}

// probeCoreTablesPGDDL 是探针读取面核心表的 fresh DDL（加法幂等）。
const probeCoreTablesPGDDL = `
CREATE SCHEMA IF NOT EXISTS juhe_business;
CREATE TABLE IF NOT EXISTS juhe_business.accounts (
  id TEXT PRIMARY KEY,
  system_account_id TEXT NOT NULL,
  name TEXT NOT NULL DEFAULT '',
  type TEXT NOT NULL,
  status TEXT NOT NULL,
  schedulable INTEGER NOT NULL DEFAULT 1,
  vendor_code TEXT,
  connection_driver_code TEXT NOT NULL DEFAULT 'generic_http',
  oauth_type TEXT,
  protocol_code TEXT,
  protocol_version TEXT,
  health_check_connection_id TEXT,
  health_check_model TEXT NOT NULL DEFAULT '',
  health_check_endpoint_mode TEXT NOT NULL DEFAULT '',
  account_expires_at TEXT,
  cooldown_until TEXT,
  last_error_code TEXT,
  last_error_message TEXT,
  credentials_encrypted TEXT NOT NULL DEFAULT '{}',
  authorization_instance_authorization_id TEXT,
  authorization_instance_source_account_id TEXT,
  authorization_instance_owner_system_account_id TEXT,
  config_revision INTEGER NOT NULL DEFAULT 1,
  dispatch_revision INTEGER NOT NULL DEFAULT 1,
  last_health_success_at TEXT,
  last_used_at TEXT,
  cooldown_retest_failure_count INTEGER NOT NULL DEFAULT 0,
  cooldown_retest_observation_started_at TEXT,
  cooldown_retest_generation TEXT,
  cooldown_retest_last_at TEXT,
  cooldown_retest_last_status_code INTEGER,
  stream_failure_count INTEGER NOT NULL DEFAULT 0,
  stream_failure_window_started_at TEXT,
  deleted_at TEXT,
  updated_at TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS juhe_business.models (
  id TEXT PRIMARY KEY,
  vendor_code TEXT,
  public_model TEXT NOT NULL,
  upstream_model TEXT NOT NULL,
  scope TEXT NOT NULL DEFAULT 'global',
  system_account_id TEXT,
  status TEXT NOT NULL DEFAULT 'active',
  created_at TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS juhe_business.account_supported_models (
  account_id TEXT NOT NULL,
  model_id TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (account_id, model_id)
);
CREATE TABLE IF NOT EXISTS juhe_business.account_connections (
  id TEXT PRIMARY KEY,
  account_id TEXT NOT NULL,
  connection_preset_code TEXT NOT NULL,
  protocol_surface_code TEXT,
  base_url TEXT NOT NULL,
  enabled INTEGER NOT NULL DEFAULT 1,
  created_at TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS juhe_business.account_endpoint_modes (
  connection_id TEXT NOT NULL,
  endpoint_mode TEXT NOT NULL,
  PRIMARY KEY (connection_id, endpoint_mode)
);
CREATE TABLE IF NOT EXISTS juhe_business.groups (
  id TEXT PRIMARY KEY,
  system_account_id TEXT NOT NULL,
  enabled INTEGER NOT NULL DEFAULT 1,
  group_type TEXT,
  scheduling_policy_json TEXT
);
CREATE TABLE IF NOT EXISTS juhe_business.group_accounts (
  group_id TEXT NOT NULL,
  system_account_id TEXT NOT NULL,
  account_id TEXT NOT NULL,
  account_authorization_id TEXT,
  local_priority INTEGER NOT NULL DEFAULT 0,
  local_super_priority_enabled INTEGER NOT NULL DEFAULT 0,
  local_fallback_enabled INTEGER NOT NULL DEFAULT 0,
  enabled INTEGER NOT NULL DEFAULT 1,
  updated_at TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS juhe_business.resource_authorizations (
  id TEXT PRIMARY KEY,
  resource_type TEXT NOT NULL,
  resource_id TEXT NOT NULL,
  grantee_system_account_id TEXT NOT NULL,
  resource_owner_system_account_id TEXT NOT NULL,
  status TEXT NOT NULL,
  expires_at TEXT,
  limits_json TEXT,
  effective_source_type TEXT,
  effective_source_team_id TEXT
);
`

// loadWorkerConfigOrFatal 是配置装载的测试断言包装。
func loadWorkerConfigOrFatal(t *testing.T, env map[string]string) workerConfig {
	t.Helper()
	config, err := loadWorkerConfig(getenvFrom(env))
	if err != nil {
		t.Fatalf("loadWorkerConfig: %v", err)
	}
	return config
}

// assemblyTestLogger 是静默测试 logger。
func assemblyTestLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }
