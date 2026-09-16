package cleanuprepo

import (
	"context"
	"database/sql/driver"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/retention"
)

// recordcleanuppostgres.go 剩余路径：account 目标表的 deferred / error / list /
// pending 批处理入口（CleanupPendingAccountTargetsPostgres 及其依赖的
// listPostgresAccountCleanupTargets、markPostgresAccountCleanupTargetDeferred）。
// 复用 pgRecorder + openKitFailingRecorderPG（按子串注入失败）打法。

func w10eAccountTarget(accountID string) retention.ExpiredDeletedAccountTarget {
	target := retentionTarget()
	target.AccountID = accountID
	target.TeamScopeIDs = []string{"team-" + accountID}
	return target
}

// TestW10ECleanupAccountRelatedPostgresDeferred：批次成功 + usage 残余 →
// HasMore=true → markPostgresAccountCleanupTargetDeferred（此前 0%）。
func TestW10ECleanupAccountRelatedPostgresDeferred(t *testing.T) {
	rec := newPGRecorder()
	store := newPGTestStore(rec)
	text := func(v string) *string { return &v }
	row := pgTestRow()
	row.AccountID = text("acc-1")
	rec.script("stats_job_state", []string{"job_name", "cursor_created_at", "cursor_id"}, [][]driver.Value{
		{"usage_stats_aggregation", "2026-01-05T03:00:00.000Z", "rec-0"},
		{"client_ip_stats_aggregation", "2026-01-05T03:00:00.000Z", "rec-0"},
	})
	rec.script("FROM juhe_usage.usage_records", usageRecordColumns(), [][]driver.Value{usageRecordDriverRow(row)})
	rec.script("SELECT 1 AS found", []string{"found"}, [][]driver.Value{{int64(1)}})

	result, err := store.CleanupAccountRelatedPostgres(context.Background(), w10eAccountTarget("acc-1"))
	if err != nil {
		t.Fatalf("CleanupAccountRelatedPostgres: %v", err)
	}
	if !result.HasMore || result.DeletedRows != 1 {
		t.Fatalf("result = %+v，期望 HasMore=true 且 DeletedRows=1", result)
	}
	if result.BlockedReason == "" {
		t.Fatalf("HasMore 时应携带 BlockedReason")
	}
	// 末条应为 deferred 更新（attempt_count 递增 + last_blocked_reason）。
	statements := rec.all()
	last := statements[len(statements)-1]
	if !strings.Contains(last.query, "UPDATE juhe_dataset.account_record_cleanup_targets") ||
		!strings.Contains(last.query, "attempt_count = attempt_count + 1") {
		t.Fatalf("末条应为 deferred 更新：%s", last.query)
	}
}

// TestW10ECleanupAccountRelatedPostgresDeferredStatsOnly：usage 无残余但 stats
// 残余 → HasMore=true，BlockedReason 走 stats 残余分支 → deferred。
func TestW10ECleanupAccountRelatedPostgresDeferredStatsOnly(t *testing.T) {
	rec := newPGRecorder()
	store := newPGTestStore(rec)
	text := func(v string) *string { return &v }
	row := pgTestRow()
	row.AccountID = text("acc-1")
	rec.script("stats_job_state", []string{"job_name", "cursor_created_at", "cursor_id"}, [][]driver.Value{
		{"usage_stats_aggregation", "2026-01-05T03:00:00.000Z", "rec-0"},
		{"client_ip_stats_aggregation", "2026-01-05T03:00:00.000Z", "rec-0"},
	})
	rec.script("FROM juhe_usage.usage_records", usageRecordColumns(), [][]driver.Value{usageRecordDriverRow(row)})
	rec.script("scope_type IN ('account', 'caller_account')", []string{"1"}, [][]driver.Value{{int64(1)}})

	result, err := store.CleanupAccountRelatedPostgres(context.Background(), w10eAccountTarget("acc-1"))
	if err != nil {
		t.Fatalf("CleanupAccountRelatedPostgres: %v", err)
	}
	if !result.HasMore {
		t.Fatalf("result = %+v，期望 HasMore=true", result)
	}
	if result.BlockedReason != "仍有使用记录尚未被统计安全游标覆盖，已保留待后台重试清理" {
		t.Fatalf("stats 残余应走 stats 残余 reason：%s", result.BlockedReason)
	}
	statements := rec.all()
	last := statements[len(statements)-1]
	if !strings.Contains(last.query, "UPDATE juhe_dataset.account_record_cleanup_targets") {
		t.Fatalf("末条应为 deferred 更新：%s", last.query)
	}
}

// TestW10ECleanupAccountRelatedPostgresErrorArms：主链尾部错误臂逐阶段注入。
func TestW10ECleanupAccountRelatedPostgresErrorArms(t *testing.T) {
	stages := []pgStage{
		{"target upsert", "INSERT INTO juhe_dataset.account_record_cleanup_targets"},
		{"usage exists", "SELECT 1 AS found"},
		{"final stats", "scope_id LIKE $"},
		{"stats exists", "WHERE scope_type IN ('account', 'caller_account')\n"},
		{"clear target", "DELETE FROM juhe_dataset.account_record_cleanup_targets"},
		{"deferred mark", "UPDATE juhe_dataset.account_record_cleanup_targets"},
	}
	runPGStages(t, stages, func(t *testing.T, stage pgStage) error {
		rec := newPGRecorder()
		text := func(v string) *string { return &v }
		row := pgTestRow()
		row.AccountID = text("acc-1")
		rec.script("stats_job_state", []string{"job_name", "cursor_created_at", "cursor_id"}, [][]driver.Value{
			{"usage_stats_aggregation", "2026-01-05T03:00:00.000Z", "rec-0"},
			{"client_ip_stats_aggregation", "2026-01-05T03:00:00.000Z", "rec-0"},
		})
		rec.script("FROM juhe_usage.usage_records", usageRecordColumns(), [][]driver.Value{usageRecordDriverRow(row)})
		if stage.name == "deferred mark" {
			rec.script("SELECT 1 AS found", []string{"found"}, [][]driver.Value{{int64(1)}})
		}
		store := &RecordCleanupStore{
			Stats:    openKitFailingRecorderPG(rec, stage.failOn),
			Dataset:  openKitFailingRecorderPG(rec, stage.failOn),
			Business: openRecorderPG(rec),
			Now:      kitNow,
			Timezone: func(context.Context) (*time.Location, error) { return kitZone(), nil },
		}
		_, err := store.CleanupAccountRelatedPostgres(context.Background(), w10eAccountTarget("acc-1"))
		return err
	})
}

// TestW10ECleanupPendingAccountTargetsPostgresFlow：list 过滤空 account_id、
// 目标含 team scope JSON、批次完成与 deferred 汇总。
func TestW10ECleanupPendingAccountTargetsPostgresFlow(t *testing.T) {
	rec := newPGRecorder()
	store := newPGTestStore(rec)
	text := func(v string) *string { return &v }
	row := pgTestRow()
	row.AccountID = text("acc-1")
	rec.script("FROM juhe_dataset.account_record_cleanup_targets", []string{
		"account_id", "system_account_id", "related_account_ids_json", "authorization_ids_json", "team_scope_ids_json",
	}, [][]driver.Value{
		{"acc-1", "sys-1", `["acc-rel-1"]`, `["auth-1"]`, "[]"},
		{"", "sys-2", "[]", "[]", "[]"}, // 空 account_id → 过滤
		{"acc-2", "sys-2", "[]", "[]", `["team-9"]`},
	})
	rec.script("stats_job_state", []string{"job_name", "cursor_created_at", "cursor_id"}, [][]driver.Value{
		{"usage_stats_aggregation", "2026-01-05T03:00:00.000Z", "rec-0"},
		{"client_ip_stats_aggregation", "2026-01-05T03:00:00.000Z", "rec-0"},
	})
	rec.script("FROM juhe_usage.usage_records", usageRecordColumns(), [][]driver.Value{usageRecordDriverRow(row)})

	summary, err := store.CleanupPendingAccountTargetsPostgres(context.Background(), 50)
	if err != nil {
		t.Fatalf("CleanupPendingAccountTargetsPostgres: %v", err)
	}
	// acc-1：游标齐备 + 1 行删除 + 无残余 → completed；acc-2：无游标 → completed。
	if summary.Attempted != 2 || summary.Completed != 2 || summary.Failed != 0 || summary.Deferred != 0 || summary.DeletedRows != 1 {
		t.Fatalf("summary = %+v", summary)
	}
}

// TestW10ECleanupPendingAccountTargetsPostgresListError：list 失败 → 直接返回错误。
func TestW10ECleanupPendingAccountTargetsPostgresListError(t *testing.T) {
	rec := newPGRecorder()
	store := &RecordCleanupStore{
		Stats:    openKitFailingRecorderPG(rec, "FROM juhe_dataset.account_record_cleanup_targets"),
		Dataset:  openKitFailingRecorderPG(rec, "FROM juhe_dataset.account_record_cleanup_targets"),
		Business: openRecorderPG(rec),
		Now:      kitNow,
		Timezone: func(context.Context) (*time.Location, error) { return kitZone(), nil },
	}
	if _, err := store.CleanupPendingAccountTargetsPostgres(context.Background(), 50); err == nil {
		t.Fatalf("list 失败应产生错误")
	}
}

// TestW10ECleanupPendingAccountTargetsPostgresFailure：批次失败后的 markError
// 成功（Failed++ continue）与失败（return markErr）两个分支。
func TestW10ECleanupPendingAccountTargetsPostgresFailure(t *testing.T) {
	run := func(markFails bool) (retention.PendingCleanupSummary, error) {
		rec := newPGRecorder()
		failOn := []string{"INSERT INTO juhe_stats.usage_record_cleanup_deductions"}
		if markFails {
			failOn = append(failOn, "UPDATE juhe_dataset.account_record_cleanup_targets")
		}
		rec.script("FROM juhe_dataset.account_record_cleanup_targets", []string{
			"account_id", "system_account_id", "related_account_ids_json", "authorization_ids_json", "team_scope_ids_json",
		}, [][]driver.Value{
			{"acc-1", "sys-1", "[]", "[]", "[]"},
		})
		// floor 游标齐备 + usage 行存在，让批次真正执行到台账 INSERT（failOn 命中点）。
		rec.script("stats_job_state", []string{"job_name", "cursor_created_at", "cursor_id"}, [][]driver.Value{
			{"usage_stats_aggregation", "2026-01-05T03:00:00.000Z", "rec-0"},
			{"client_ip_stats_aggregation", "2026-01-05T03:00:00.000Z", "rec-0"},
		})
		rec.script("FROM juhe_usage.usage_records", usageRecordColumns(), [][]driver.Value{usageRecordDriverRow(pgTestRow())})
		store := &RecordCleanupStore{
			Stats:    openKitFailingRecorderPG(rec, failOn...),
			Dataset:  openKitFailingRecorderPG(rec, failOn...),
			Business: openRecorderPG(rec),
			Now:      kitNow,
			Timezone: func(context.Context) (*time.Location, error) { return kitZone(), nil },
		}
		return store.CleanupPendingAccountTargetsPostgres(context.Background(), 50)
	}
	summary, err := run(false)
	if err != nil {
		t.Fatalf("markError 成功分支不应返回错误: %v", err)
	}
	if summary.Failed != 1 || summary.Attempted != 1 {
		t.Fatalf("summary = %+v，期望 Failed=1", summary)
	}
	if _, err := run(true); err == nil {
		t.Fatalf("markError 失败分支应返回 markErr")
	}
}
