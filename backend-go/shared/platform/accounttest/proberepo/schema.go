package proberepo

import (
	"context"
	"fmt"
)

// EnsureSchema 幂等创建探针族专属的两张辅助表（与 Node 迁移产物同形，
// CREATE TABLE IF NOT EXISTS 语义与 accountquality.StatsStore.EnsureSchema 一致）。
// 核心业务表（accounts/groups/group_accounts 等）归生产迁移所有，本包只在
// ValidateCoreTables 里做存在性校验，缺失时由组合根登记 disabled，不代建。
func (s *Store) EnsureSchema(ctx context.Context) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS ` + s.table("account_api_key_runtime_states") + ` (
      id TEXT PRIMARY KEY,
      system_account_id TEXT NOT NULL,
      account_id TEXT NOT NULL,
      key_fingerprint TEXT NOT NULL,
      key_index INTEGER NOT NULL DEFAULT 0,
      status TEXT NOT NULL,
      failure_count INTEGER NOT NULL DEFAULT 0,
      consecutive_failures INTEGER NOT NULL DEFAULT 0,
      success_count INTEGER NOT NULL DEFAULT 0,
      cooldown_until TEXT,
      next_probe_at TEXT,
      probe_backoff_seconds INTEGER NOT NULL DEFAULT 0,
      recovery_started_at TEXT,
      last_attempt_at TEXT,
      last_success_at TEXT,
      last_failure_at TEXT,
      last_error_code TEXT,
      last_error_message TEXT,
      last_trace_id TEXT,
      last_probe_at TEXT,
      probe_claim_token TEXT,
      probe_claimed_until TEXT,
      created_at TEXT NOT NULL DEFAULT '',
      updated_at TEXT NOT NULL DEFAULT ''
    )`,
		`CREATE UNIQUE INDEX IF NOT EXISTS account_api_key_runtime_states_account_fingerprint ON ` +
			s.table("account_api_key_runtime_states") + ` (account_id, key_fingerprint)`,
		`CREATE TABLE IF NOT EXISTS ` + s.table("account_supported_models") + ` (
      account_id TEXT NOT NULL,
      provider_code TEXT,
      model TEXT NOT NULL,
      created_at TEXT NOT NULL DEFAULT ''
    )`,
	}
	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("初始化探针族 schema 失败: %w", err)
		}
	}
	return nil
}

// ValidateCoreTables 校验探针族依赖的核心业务表存在（不代建）。
func (s *Store) ValidateCoreTables(ctx context.Context) error {
	tables := []string{"accounts", "groups", "group_accounts", "resource_authorizations"}
	for _, table := range tables {
		query := "SELECT COUNT(*) FROM " + s.table(table) + " WHERE 1 = 0"
		var count int
		if err := s.db.QueryRowContext(ctx, query).Scan(&count); err != nil {
			return fmt.Errorf("业务库缺少核心表 %s: %w", table, err)
		}
	}
	return nil
}
