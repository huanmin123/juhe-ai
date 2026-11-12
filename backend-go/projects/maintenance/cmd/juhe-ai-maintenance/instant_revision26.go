package main

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/instantmigration"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/upgradepipeline"
)

// revision26Columns 是 Phase 2-3 accounts 聚合与投影批次的静态列清单。来源为
// maintenance audit-time-columns 清单与 migrate-instant-columns --scope phase2
// 的 accounts 批次；增删列必须同步 fresh schema、运行时适配与 plan 文档。
//
// 与 Phase 2-1/2-2 不同，本批包含 leases/outbox 游标列。精度按 inventory
// 逐列判定：租约/CAS/outbox 可见时间与投影游标以 timestamptz(6) 保留微秒，
// 其余业务/API 列为 timestamptz(3)。三个投影 cursor 的 observed_at/stored_at
// 必须是 (6)：它们直接与 juhe_jobs 的微秒时间比较，截到毫秒会让游标每轮
// 重放同一 outcome。
//
// 依赖处理：`accounts.last_used_at` 被 `UPDATE OF last_used_at` 触发器引用、
// `accounts.updated_at` 被 `CASE WHEN status='pending_test' THEN updated_at END`
// 表达式索引引用，PostgreSQL 禁止直接 ALTER 这两列（0A000/42804）；apply 在
// 同一事务内先移除、迁移后按原定义重建（见 applyRevision26）。
var revision26Columns = []instantColumn{
	{"account_api_key_pool_probe_cursors", "updated_at", "timestamptz(3)"},
	{"account_api_key_runtime_states", "cooldown_until", "timestamptz(3)"},
	{"account_api_key_runtime_states", "created_at", "timestamptz(3)"},
	{"account_api_key_runtime_states", "last_attempt_at", "timestamptz(3)"},
	{"account_api_key_runtime_states", "last_failure_at", "timestamptz(3)"},
	{"account_api_key_runtime_states", "last_probe_at", "timestamptz(6)"},
	{"account_api_key_runtime_states", "last_success_at", "timestamptz(3)"},
	{"account_api_key_runtime_states", "next_probe_at", "timestamptz(6)"},
	{"account_api_key_runtime_states", "probe_claimed_until", "timestamptz(6)"},
	{"account_api_key_runtime_states", "recovery_started_at", "timestamptz(6)"},
	{"account_api_key_runtime_states", "updated_at", "timestamptz(3)"},
	{"account_balance_projection_cursors", "observed_at", "timestamptz(6)"},
	{"account_balance_projection_cursors", "updated_at", "timestamptz(3)"},
	{"account_connections", "created_at", "timestamptz(3)"},
	{"account_connections", "updated_at", "timestamptz(3)"},
	{"account_entry_mappings", "created_at", "timestamptz(3)"},
	{"account_entry_mappings", "updated_at", "timestamptz(3)"},
	{"account_health_jobs_input_outbox", "available_at", "timestamptz(6)"},
	{"account_health_jobs_input_outbox", "claimed_until", "timestamptz(6)"},
	{"account_health_jobs_input_outbox", "created_at", "timestamptz(3)"},
	{"account_health_jobs_input_outbox", "updated_at", "timestamptz(3)"},
	{"account_health_jobs_input_versions", "reserved_at", "timestamptz(6)"},
	{"account_health_probe_request_outbox", "available_at", "timestamptz(6)"},
	{"account_health_probe_request_outbox", "consumed_at", "timestamptz(3)"},
	{"account_health_probe_request_outbox", "created_at", "timestamptz(3)"},
	{"account_health_probe_request_outbox", "deadline_at", "timestamptz(3)"},
	{"account_health_probe_request_outbox", "updated_at", "timestamptz(3)"},
	{"account_health_projection_cursors", "observed_at", "timestamptz(6)"},
	{"account_health_projection_cursors", "updated_at", "timestamptz(3)"},
	{"account_health_projection_receipts", "applied_at", "timestamptz(3)"},
	{"account_list_availability_projection_dependency_health", "updated_at", "timestamptz(3)"},
	{"account_list_availability_projection_viewer_health", "next_transition_at", "timestamptz(3)"},
	{"account_list_availability_projection_viewer_health", "oldest_projected_at", "timestamptz(6)"},
	{"account_list_availability_projection_viewer_health", "updated_at", "timestamptz(3)"},
	{"account_list_availability_projections", "next_transition_at", "timestamptz(3)"},
	{"account_list_availability_projections", "projected_at", "timestamptz(6)"},
	{"account_list_availability_runtime_overlays", "next_reconcile_at", "timestamptz(6)"},
	{"account_list_availability_runtime_overlays", "observed_at", "timestamptz(3)"},
	{"account_lock_states", "deadline_at", "timestamptz(3)"},
	{"account_lock_states", "incident_started_at", "timestamptz(6)"},
	{"account_lock_states", "updated_at", "timestamptz(3)"},
	{"account_quality_enforcements", "cleared_at", "timestamptz(6)"},
	{"account_quality_enforcements", "created_at", "timestamptz(3)"},
	{"account_quality_enforcements", "recovery_due_at", "timestamptz(6)"},
	{"account_quality_enforcements", "recovery_lease_until", "timestamptz(6)"},
	{"account_quality_enforcements", "started_at", "timestamptz(3)"},
	{"account_quality_enforcements", "updated_at", "timestamptz(3)"},
	{"account_schedule_status_events", "executed_at", "timestamptz(3)"},
	{"account_supported_models", "created_at", "timestamptz(3)"},
	{"account_tag_bindings", "created_at", "timestamptz(3)"},
	{"account_tags", "created_at", "timestamptz(3)"},
	{"account_tags", "updated_at", "timestamptz(3)"},
	{"accounts", "account_expires_at", "timestamptz(3)"},
	{"accounts", "availability_schedule_next_check_at", "timestamptz(3)"},
	{"accounts", "balance_query_next_refresh_at", "timestamptz(3)"},
	{"accounts", "cooldown_retest_last_at", "timestamptz(3)"},
	{"accounts", "cooldown_retest_observation_started_at", "timestamptz(6)"},
	{"accounts", "cooldown_until", "timestamptz(3)"},
	{"accounts", "created_at", "timestamptz(3)"},
	{"accounts", "deleted_at", "timestamptz(3)"},
	{"accounts", "health_check_failure_started_at", "timestamptz(3)"},
	{"accounts", "last_health_check_at", "timestamptz(3)"},
	{"accounts", "last_health_success_at", "timestamptz(3)"},
	{"accounts", "last_used_at", "timestamptz(3)"},
	{"accounts", "next_health_check_at", "timestamptz(3)"},
	{"accounts", "oauth_access_token_expires_at", "timestamptz(3)"},
	{"accounts", "stream_failure_window_started_at", "timestamptz(3)"},
	{"accounts", "updated_at", "timestamptz(3)"},
	{"group_accounts", "created_at", "timestamptz(3)"},
	{"group_accounts", "updated_at", "timestamptz(3)"},
	{"proxy_latency_projection_cursors", "stored_at", "timestamptz(6)"},
	{"proxy_latency_projection_cursors", "updated_at", "timestamptz(3)"},
	{"proxy_latency_projection_receipts", "applied_at", "timestamptz(3)"},
}

var revision26 = instantRevision{columns: revision26Columns}

func revision26Actions() []instantmigration.Action { return revision26.actions() }

func revision26Satisfied(ctx context.Context, dsn string) (bool, error) {
	return revision26.satisfied(ctx, dsn)
}

// applyRevision26 在通用批次事务内临时移除依赖 accounts.last_used_at 的
// `UPDATE OF last_used_at` 触发器：PostgreSQL 不允许直接 ALTER 被触发器
// 定义引用的列类型（SQLSTATE 0A000），迁移后按原定义重建，语义不变。
func applyRevision26(ctx context.Context, dsn string) (upgradepipeline.Detail, error) {
	db, err := openPG(dsn)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	actions := revision26.actions()
	hadTrigger := false
	hadIndex := false
	before := func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, `SELECT to_regclass('juhe_business.idx_accounts_health_check_candidate_order') IS NOT NULL`).Scan(&hadIndex); err != nil {
			return fmt.Errorf("检查 accounts 健康检查候选索引失败: %w", err)
		}
		if hadIndex {
			if _, err := tx.ExecContext(ctx, `DROP INDEX juhe_business.idx_accounts_health_check_candidate_order`); err != nil {
				return fmt.Errorf("临时移除 accounts 健康检查候选索引失败: %w", err)
			}
		}
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (
			SELECT 1 FROM pg_catalog.pg_trigger
			WHERE tgrelid = 'juhe_business.accounts'::regclass
			  AND tgname = 'account_list_availability_accounts_last_used'
			  AND NOT tgisinternal)`).Scan(&hadTrigger); err != nil {
			return fmt.Errorf("检查 accounts.last_used_at 触发器失败: %w", err)
		}
		if !hadTrigger {
			return nil
		}
		if _, err := tx.ExecContext(ctx, `DROP TRIGGER account_list_availability_accounts_last_used ON juhe_business.accounts`); err != nil {
			return fmt.Errorf("临时移除 accounts.last_used_at 触发器失败: %w", err)
		}
		return nil
	}
	after := func(tx *sql.Tx) error {
		if hadIndex {
			if _, err := tx.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_accounts_health_check_candidate_order
				ON juhe_business.accounts(
					(CASE WHEN status = 'pending_test' THEN 0 ELSE 1 END) ASC,
					(CASE WHEN status = 'pending_test' THEN updated_at END) DESC,
					(next_health_check_at IS NOT NULL) ASC,
					next_health_check_at ASC,
					last_health_check_at ASC,
					created_at ASC,
					id ASC
				)
				WHERE deleted_at IS NULL
					AND status IN ('active', 'pending_test')
					AND (status = 'pending_test' OR schedulable = 1)
					AND type IN ('api_key', 'oauth', 'google_oauth')`); err != nil {
				return fmt.Errorf("重建 accounts 健康检查候选索引失败: %w", err)
			}
		}
		if !hadTrigger {
			return nil
		}
		if _, err := tx.ExecContext(ctx, `CREATE TRIGGER account_list_availability_accounts_last_used
			AFTER UPDATE OF last_used_at ON juhe_business.accounts
			FOR EACH ROW EXECUTE FUNCTION juhe_business.account_list_availability_accounts_last_used_projection_trigger()`); err != nil {
			return fmt.Errorf("重建 accounts.last_used_at 触发器失败: %w", err)
		}
		return nil
	}
	applied, err := instantmigration.ApplyBatchWithHooks(ctx, db, actions, before, after)
	if err != nil {
		return nil, err
	}
	return upgradepipeline.Detail{"applied": applied, "columns": len(actions)}, nil
}
