package main

import (
	"context"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/instantmigration"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/upgradepipeline"
)

// revision24Columns 是 Phase 2-1 小表批次的静态列清单。列顺序按
// table/column 排序，来源为 maintenance audit-time-columns 清单与
// migrate-instant-columns --scope phase2 的 small_tables 批次；增删列必须
// 同步 fresh schema、运行时适配与 plan 文档。
var revision24Columns = []instantColumn{
	{"external_integration_source_tokens", "created_at", "timestamptz(3)"},
	{"external_integration_source_tokens", "expires_at", "timestamptz(3)"},
	{"external_integration_source_tokens", "last_used_at", "timestamptz(3)"},
	{"external_integration_source_tokens", "revoked_at", "timestamptz(3)"},
	{"external_integration_source_tokens", "updated_at", "timestamptz(3)"},
	{"external_integration_sources", "created_at", "timestamptz(3)"},
	{"external_integration_sources", "expires_at", "timestamptz(3)"},
	{"external_integration_sources", "last_used_at", "timestamptz(3)"},
	{"external_integration_sources", "updated_at", "timestamptz(3)"},
	{"global_settings", "updated_at", "timestamptz(3)"},
	{"media_jobs", "created_at", "timestamptz(3)"},
	{"media_jobs", "updated_at", "timestamptz(3)"},
	{"model_check_question_bank", "created_at", "timestamptz(3)"},
	{"model_check_question_bank", "reviewed_at", "timestamptz(3)"},
	{"model_check_question_bank", "updated_at", "timestamptz(3)"},
	{"model_check_question_tests", "created_at", "timestamptz(3)"},
	{"model_quality_policies", "created_at", "timestamptz(3)"},
	{"model_quality_policies", "updated_at", "timestamptz(3)"},
	{"model_quality_schedules", "created_at", "timestamptz(3)"},
	{"model_quality_schedules", "last_run_at", "timestamptz(3)"},
	{"model_quality_schedules", "lease_until", "timestamptz(6)"},
	{"model_quality_schedules", "next_run_at", "timestamptz(3)"},
	{"model_quality_schedules", "updated_at", "timestamptz(3)"},
	{"models", "created_at", "timestamptz(3)"},
	{"models", "updated_at", "timestamptz(3)"},
	{"openai_compatible_files", "created_at", "timestamptz(3)"},
	{"openai_compatible_files", "deleted_at", "timestamptz(3)"},
	{"openai_compatible_files", "expires_at", "timestamptz(3)"},
	{"openai_compatible_files", "updated_at", "timestamptz(3)"},
	{"openai_compatible_vector_store_chunks", "created_at", "timestamptz(3)"},
	{"openai_compatible_vector_store_files", "created_at", "timestamptz(3)"},
	{"openai_compatible_vector_store_files", "deleted_at", "timestamptz(3)"},
	{"openai_compatible_vector_store_files", "updated_at", "timestamptz(3)"},
	{"openai_compatible_vector_stores", "created_at", "timestamptz(3)"},
	{"openai_compatible_vector_stores", "deleted_at", "timestamptz(3)"},
	{"openai_compatible_vector_stores", "expires_at", "timestamptz(3)"},
	{"openai_compatible_vector_stores", "updated_at", "timestamptz(3)"},
	{"protocol_endpoint_families", "created_at", "timestamptz(3)"},
	{"protocol_endpoint_families", "updated_at", "timestamptz(3)"},
	{"protocols", "created_at", "timestamptz(3)"},
	{"protocols", "updated_at", "timestamptz(3)"},
	{"request_quota_hourly_window_configs", "created_at", "timestamptz(3)"},
	{"request_quota_hourly_window_configs", "updated_at", "timestamptz(3)"},
	{"request_quota_hourly_window_scope_bindings", "created_at", "timestamptz(3)"},
	{"request_quota_hourly_window_scope_bindings", "updated_at", "timestamptz(3)"},
	{"response_inspection_policies", "created_at", "timestamptz(3)"},
	{"response_inspection_policies", "updated_at", "timestamptz(3)"},
	{"super_group_members", "created_at", "timestamptz(3)"},
	{"super_group_members", "updated_at", "timestamptz(3)"},
	{"system_accounts", "created_at", "timestamptz(3)"},
	{"system_accounts", "last_login_at", "timestamptz(3)"},
	{"system_accounts", "updated_at", "timestamptz(3)"},
	{"system_sessions", "created_at", "timestamptz(3)"},
	{"system_sessions", "expires_at", "timestamptz(3)"},
	{"system_sessions", "last_seen_at", "timestamptz(3)"},
	{"system_settings", "updated_at", "timestamptz(3)"},
	{"vendors", "created_at", "timestamptz(3)"},
	{"vendors", "updated_at", "timestamptz(3)"},
}

var revision24 = instantRevision{columns: revision24Columns}

func revision24Actions() []instantmigration.Action { return revision24.actions() }

func revision24Satisfied(ctx context.Context, dsn string) (bool, error) {
	return revision24.satisfied(ctx, dsn)
}

func applyRevision24(ctx context.Context, dsn string) (upgradepipeline.Detail, error) {
	return revision24.apply(ctx, dsn)
}
