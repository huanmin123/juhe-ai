package main

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/instantmigration"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/upgradepipeline"
)

// revision27Columns 是 Phase 2-4 dataset/jobs 批次的静态列清单：juhe_dataset
// 的 API 日志与记录清理目标、juhe_j3b 的模型检查/信任投影、juhe_stats 的
// 后台任务运行与租约表。运行态/租约列保留 timestamptz(6)，其余为 (3)。
var revision27Columns = []instantColumn{
	{"account_record_cleanup_targets", "created_at", "timestamptz(3)"},
	{"account_record_cleanup_targets", "last_attempt_at", "timestamptz(3)"},
	{"account_record_cleanup_targets", "updated_at", "timestamptz(3)"},
	{"api_key_record_cleanup_targets", "created_at", "timestamptz(3)"},
	{"api_key_record_cleanup_targets", "last_attempt_at", "timestamptz(3)"},
	{"api_key_record_cleanup_targets", "updated_at", "timestamptz(3)"},
	{"public_api_logs", "created_at", "timestamptz(3)"},
	{"public_api_logs", "ended_at", "timestamptz(3)"},
	{"public_api_logs", "started_at", "timestamptz(3)"},
	{"account_quality_health_hourly", "observed_at", "timestamptz(3)"},
	{"account_quality_health_hourly", "updated_at", "timestamptz(3)"},
	{"model_account_trust_results", "last_observed_at", "timestamptz(3)"},
	{"model_account_trust_results", "updated_at", "timestamptz(3)"},
	{"model_check_items", "created_at", "timestamptz(3)"},
	{"model_check_items", "updated_at", "timestamptz(3)"},
	{"model_check_observations", "aggregation_completed_at", "timestamptz(3)"},
	{"model_check_observations", "created_at", "timestamptz(3)"},
	{"model_check_runs", "created_at", "timestamptz(3)"},
	{"model_check_runs", "finished_at", "timestamptz(3)"},
	{"model_check_runs", "started_at", "timestamptz(3)"},
	{"model_check_runs", "updated_at", "timestamptz(3)"},
	{"model_token_intercept_baseline_versions", "first_observed_at", "timestamptz(3)"},
	{"model_token_intercept_baseline_versions", "last_observed_at", "timestamptz(3)"},
	{"model_token_intercept_baseline_versions", "updated_at", "timestamptz(3)"},
	{"model_trust_aggregation_state", "cursor_created_at", "timestamptz(3)"},
	{"model_trust_aggregation_state", "last_success_at", "timestamptz(3)"},
	{"model_trust_aggregation_state", "updated_at", "timestamptz(3)"},
	{"model_trust_latest_dirty_accounts", "updated_at", "timestamptz(3)"},
	{"model_trust_observation_receipts", "observation_created_at", "timestamptz(3)"},
	{"model_trust_observation_receipts", "processed_at", "timestamptz(3)"},
	{"background_job_leases", "heartbeat_at", "timestamptz(6)"},
	{"background_job_leases", "lease_until", "timestamptz(6)"},
	{"background_job_leases", "started_at", "timestamptz(6)"},
	{"background_job_leases", "updated_at", "timestamptz(6)"},
	{"background_task_runs", "created_at", "timestamptz(6)"},
	{"background_task_runs", "finished_at", "timestamptz(6)"},
	{"background_task_runs", "heartbeat_at", "timestamptz(6)"},
	{"background_task_runs", "started_at", "timestamptz(6)"},
	{"background_task_runs", "submitted_at", "timestamptz(6)"},
	{"background_task_runs", "updated_at", "timestamptz(6)"},
}

var revision27 = instantRevision{columns: revision27Columns}

func revision27Actions() []instantmigration.Action { return revision27.actions() }

func revision27Satisfied(ctx context.Context, dsn string) (bool, error) {
	return revision27.satisfied(ctx, dsn)
}

// applyRevision27 在批次事务内临时移除 dataset 清理目标的 COALESCE 表达式
// 索引：逐列 ALTER 时索引表达式会短暂出现 timestamptz/text 混合类型，
// PostgreSQL 拒绝重写（42804）；迁移后按原定义重建。
func applyRevision27(ctx context.Context, dsn string) (upgradepipeline.Detail, error) {
	db, err := openPG(dsn)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	actions := revision27.actions()
	type indexDef struct{ schema, name, create string }
	indexes := []indexDef{
		{"juhe_dataset", "idx_account_record_cleanup_targets_attempt", `CREATE INDEX IF NOT EXISTS idx_account_record_cleanup_targets_attempt ON juhe_dataset.account_record_cleanup_targets(COALESCE(last_attempt_at, created_at), created_at, account_id)`},
		{"juhe_dataset", "idx_api_key_record_cleanup_targets_attempt", `CREATE INDEX IF NOT EXISTS idx_api_key_record_cleanup_targets_attempt ON juhe_dataset.api_key_record_cleanup_targets(COALESCE(last_attempt_at, created_at), created_at, api_key_id)`},
	}
	had := make([]bool, len(indexes))
	before := func(tx *sql.Tx) error {
		for i, idx := range indexes {
			if err := tx.QueryRowContext(ctx, `SELECT to_regclass($1) IS NOT NULL`, idx.schema+"."+idx.name).Scan(&had[i]); err != nil {
				return fmt.Errorf("检查索引 %s 失败: %w", idx.name, err)
			}
			if had[i] {
				if _, err := tx.ExecContext(ctx, `DROP INDEX `+idx.schema+`.`+idx.name); err != nil {
					return fmt.Errorf("临时移除索引 %s 失败: %w", idx.name, err)
				}
			}
		}
		return nil
	}
	after := func(tx *sql.Tx) error {
		for i, idx := range indexes {
			if had[i] {
				if _, err := tx.ExecContext(ctx, idx.create); err != nil {
					return fmt.Errorf("重建索引 %s 失败: %w", idx.name, err)
				}
			}
		}
		return nil
	}
	applied, err := instantmigration.ApplyBatchWithHooks(ctx, db, actions, before, after)
	if err != nil {
		return nil, err
	}
	return upgradepipeline.Detail{"applied": applied, "columns": len(actions)}, nil
}
