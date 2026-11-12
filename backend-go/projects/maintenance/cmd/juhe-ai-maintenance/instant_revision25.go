package main

import (
	"context"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/instantmigration"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/upgradepipeline"
)

// revision25Columns 是 Phase 2-2 CRUD 批次的静态列清单。来源为
// maintenance audit-time-columns 清单与 migrate-instant-columns --scope phase2
// 的 crud 批次；增删列必须同步 fresh schema、运行时适配与 plan 文档。
var revision25Columns = []instantColumn{
	{"announcement_reads", "read_at", "timestamptz(3)"},
	{"announcements", "created_at", "timestamptz(3)"},
	{"announcements", "published_at", "timestamptz(3)"},
	{"announcements", "updated_at", "timestamptz(3)"},
	{"api_key_schedule_status_events", "executed_at", "timestamptz(3)"},
	{"api_keys", "availability_schedule_next_check_at", "timestamptz(3)"},
	{"api_keys", "created_at", "timestamptz(3)"},
	{"api_keys", "expires_at", "timestamptz(3)"},
	{"api_keys", "last_used_at", "timestamptz(3)"},
	{"api_keys", "updated_at", "timestamptz(3)"},
	{"group_account_stats_dirty", "updated_at", "timestamptz(3)"},
	{"group_authorization_settings", "created_at", "timestamptz(3)"},
	{"group_authorization_settings", "updated_at", "timestamptz(3)"},
	{"groups", "created_at", "timestamptz(3)"},
	{"groups", "updated_at", "timestamptz(3)"},
	{"resource_authorization_grants", "created_at", "timestamptz(3)"},
	{"resource_authorization_grants", "expires_at", "timestamptz(3)"},
	{"resource_authorization_grants", "revoked_at", "timestamptz(3)"},
	{"resource_authorization_grants", "updated_at", "timestamptz(3)"},
	{"resource_authorization_sources", "activated_at", "timestamptz(3)"},
	{"resource_authorization_sources", "created_at", "timestamptz(3)"},
	{"resource_authorization_sources", "ended_at", "timestamptz(3)"},
	{"resource_authorization_sources", "revoked_at", "timestamptz(3)"},
	{"resource_authorization_sources", "updated_at", "timestamptz(3)"},
	{"resource_authorizations", "activated_at", "timestamptz(3)"},
	{"resource_authorizations", "created_at", "timestamptz(3)"},
	{"resource_authorizations", "expires_at", "timestamptz(3)"},
	{"resource_authorizations", "last_source_changed_at", "timestamptz(3)"},
	{"resource_authorizations", "revoked_at", "timestamptz(3)"},
	{"resource_authorizations", "updated_at", "timestamptz(3)"},
	{"security_policies", "created_at", "timestamptz(3)"},
	{"security_policies", "updated_at", "timestamptz(3)"},
	{"route_strategies", "created_at", "timestamptz(3)"},
	{"route_strategies", "updated_at", "timestamptz(3)"},
	{"route_strategy_groups", "created_at", "timestamptz(3)"},
	{"route_strategy_groups", "updated_at", "timestamptz(3)"},
	{"scheduling_preferences", "created_at", "timestamptz(3)"},
	{"scheduling_preferences", "updated_at", "timestamptz(3)"},
	{"system_team_members", "created_at", "timestamptz(3)"},
	{"system_team_members", "joined_at", "timestamptz(3)"},
	{"system_team_members", "removed_at", "timestamptz(3)"},
	{"system_team_members", "updated_at", "timestamptz(3)"},
	{"system_teams", "created_at", "timestamptz(3)"},
	{"system_teams", "updated_at", "timestamptz(3)"},
}

var revision25 = instantRevision{columns: revision25Columns}

func revision25Actions() []instantmigration.Action { return revision25.actions() }

func revision25Satisfied(ctx context.Context, dsn string) (bool, error) {
	return revision25.satisfied(ctx, dsn)
}

func applyRevision25(ctx context.Context, dsn string) (upgradepipeline.Detail, error) {
	return revision25.apply(ctx, dsn)
}
