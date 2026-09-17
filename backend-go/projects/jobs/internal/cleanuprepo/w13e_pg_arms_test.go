package cleanuprepo

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/retention"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/statsagg"
)

// w13e_pg_arms_test.go 覆盖 PG 半区剩余错误臂与数据驱动分支：
// recordcleanuppostgres.go / statssubtractpostgres.go / dataretention.go（PG 半区）/
// codexcontext.go（PG 半区）。注入手法沿用 PG 录制驱动：failOn 子串注入错误、
// script 子串脚本行集、w13e 装饰句柄补 BeginTx/Commit/RowsAffected 失败。

// w13ePGNaNRow 返回 cost 为 NaN 的使用记录（触发 record_json 序列化失败）。
func w13ePGNaNRow() statsagg.UsageStatsRecordRow {
	row := pgTestRow()
	nan := math.NaN()
	row.CostUsd = &nan
	return row
}

// TestW13ePGRecordCleanupPostgresArms：PG 记录清理链剩余臂。
func TestW13ePGRecordCleanupPostgresArms(t *testing.T) {
	ctx := context.Background()
	seed := func(t *testing.T, rec *pgRecorder, withResidual bool) {
		seedKitPGAPIKeyFlow(t, rec, withResidual)
	}
	// targets 列表失败。
	runPGStages(t, []pgStage{
		{"api key targets list", "FROM juhe_dataset.api_key_record_cleanup_targets"},
	}, func(t *testing.T, stage pgStage) error {
		rec := newPGRecorder()
		store := &RecordCleanupStore{
			Stats:    w13eOpenDecoratedPG(rec, w13ePGOptions{failOn: []string{stage.failOn}}),
			Dataset:  w13eOpenDecoratedPG(rec, w13ePGOptions{failOn: []string{stage.failOn}}),
			Business: openRecorderPG(rec),
			Now:      kitNow,
		}
		_, err := store.CleanupPendingAPIKeyTargetsPostgres(ctx, 5)
		return err
	})
	// record_json 序列化失败（NaN 成本）。
	{
		rec := newPGRecorder()
		// 先注册 NaN 行（scripted FIFO：先登记先消费，避免被 seed 行遮蔽）。
		rec.script("FROM juhe_usage.usage_records", usageRecordColumns(), [][]driver.Value{
			usageRecordDriverRow(w13ePGNaNRow()),
		})
		seed(t, rec, false)
		store := &RecordCleanupStore{
			Stats:    openRecorderPG(rec),
			Dataset:  openRecorderPG(rec),
			Business: openRecorderPG(rec),
			Now:      kitNow,
			Timezone: func(context.Context) (*time.Location, error) { return pgTestZone, nil },
		}
		if _, err := store.CleanupAPIKeyRelatedPostgres(ctx, "key-1", "sys-1"); err == nil {
			t.Fatalf("NaN record_json 应失败")
		}
	}
	// 无可扣减行（ShouldAggregate 拒绝）→ 空集提前返回。
	{
		rec := newPGRecorder()
		row := pgTestRow()
		row.AccountAccessType = nil
		rec.script("FROM juhe_usage.usage_records", usageRecordColumns(), [][]driver.Value{
			usageRecordDriverRow(row),
		})
		seed(t, rec, false)
		store := &RecordCleanupStore{
			Stats:    openRecorderPG(rec),
			Dataset:  openRecorderPG(rec),
			Business: openRecorderPG(rec),
			Now:      kitNow,
			Timezone: func(context.Context) (*time.Location, error) { return pgTestZone, nil },
		}
		if _, err := store.CleanupAPIKeyRelatedPostgres(ctx, "key-1", "sys-1"); err != nil {
			t.Fatalf("不可聚合行应跳过扣减: %v", err)
		}
	}
	// 批次行 id 去重 / 空 id / 全空 → usageIDs 空。
	{
		rec := newPGRecorder()
		row := pgTestRow()
		rec.script("FROM juhe_usage.usage_records", usageRecordColumns(), [][]driver.Value{
			usageRecordDriverRow(row), usageRecordDriverRow(row),
		})
		seed(t, rec, false)
		store := &RecordCleanupStore{
			Stats:    openRecorderPG(rec),
			Dataset:  openRecorderPG(rec),
			Business: openRecorderPG(rec),
			Now:      kitNow,
			Timezone: func(context.Context) (*time.Location, error) { return pgTestZone, nil },
		}
		// 重复 id：去重后仍可继续（不要求报错）。
		if _, err := store.CleanupAPIKeyRelatedPostgres(ctx, "key-1", "sys-1"); err != nil {
			t.Fatalf("重复 id 不应报错: %v", err)
		}
		// 空 id：usageIDs 空 → 空批次直通。
		rec2 := newPGRecorder()
		empty := pgTestRow()
		empty.ID = "  "
		rec2.script("FROM juhe_usage.usage_records", usageRecordColumns(), [][]driver.Value{
			usageRecordDriverRow(empty),
		})
		seed(t, rec2, false)
		store2 := &RecordCleanupStore{
			Stats:    openRecorderPG(rec2),
			Dataset:  openRecorderPG(rec2),
			Business: openRecorderPG(rec2),
			Now:      kitNow,
			Timezone: func(context.Context) (*time.Location, error) { return pgTestZone, nil },
		}
		if _, err := store2.CleanupAPIKeyRelatedPostgres(ctx, "key-1", "sys-1"); err != nil {
			t.Fatalf("空 id 行不应报错: %v", err)
		}
	}
	// mark deleted 失败。
	runPGStages(t, []pgStage{
		{"mark deleted", "shard_deleted_at"},
	}, func(t *testing.T, stage pgStage) error {
		rec := newPGRecorder()
		seed(t, rec, false)
		store := &RecordCleanupStore{
			Stats:    w13eOpenDecoratedPG(rec, w13ePGOptions{failOn: []string{stage.failOn}}),
			Dataset:  w13eOpenDecoratedPG(rec, w13ePGOptions{failOn: []string{stage.failOn}}),
			Business: openRecorderPG(rec),
			Now:      kitNow,
			Timezone: func(context.Context) (*time.Location, error) { return pgTestZone, nil },
		}
		_, err := store.CleanupAPIKeyRelatedPostgres(ctx, "key-1", "sys-1")
		return err
	})
	// usage exists 有残余（SELECT 1 AS found 命中）→ deferred 分支。
	{
		rec := newPGRecorder()
		seed(t, rec, true)
		store := &RecordCleanupStore{
			Stats:    openRecorderPG(rec),
			Dataset:  openRecorderPG(rec),
			Business: openRecorderPG(rec),
			Now:      kitNow,
			Timezone: func(context.Context) (*time.Location, error) { return pgTestZone, nil },
		}
		result, err := store.CleanupAPIKeyRelatedPostgres(ctx, "key-1", "sys-1")
		if err != nil || !result.HasMore {
			t.Fatalf("残余行应 HasMore: %+v %v", result, err)
		}
	}
	// account 批次：空 account/auth 目标直通（直调）。
	{
		rec := newPGRecorder()
		store := newPGTestStore(rec)
		tx, err := store.Stats.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		deleted, err := store.deletePostgresAccountUsageDataBatch(ctx, tx, retention.ExpiredDeletedAccountTarget{}, 5, kitUpdatedAt)
		if err != nil || deleted != 0 {
			t.Fatalf("空目标应零删除: %d %v", deleted, err)
		}
		_ = tx.Rollback()
	}
	// 分区键删除：空键 / 空 created_at 行直通（直调）。
	{
		rec := newPGRecorder()
		store := newPGTestStore(rec)
		tx, err := store.Stats.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		deleted, err := store.deletePostgresUsageRecordsByPartitionKeys(ctx, tx, []statsagg.UsageStatsRecordRow{
			{ID: " ", CreatedAt: " "}, {ID: "", CreatedAt: ""},
		})
		if err != nil || deleted != 0 {
			t.Fatalf("空键行应跳过: %d %v", deleted, err)
		}
		_ = tx.Rollback()
	}
	// 账户统计存在性：auth/team chunk 失败与全空直通。
	{
		target := retention.ExpiredDeletedAccountTarget{
			AccountID: "acc-1", SystemAccountID: "sys-1",
			AuthorizationIDs: []string{"auth-1"}, TeamScopeIDs: []string{"acc-1:team-9"},
		}
		runPGStages(t, []pgStage{
			{"auth stats exists", "scope_type = 'account_authorization'\n        AND scope_id = ANY("},
			{"team stats exists", "scope_type = 'account_authorization_team'\n        AND scope_id = ANY("},
		}, func(t *testing.T, stage pgStage) error {
			rec := newPGRecorder()
			store := &RecordCleanupStore{
				Stats:    w13eOpenDecoratedPG(rec, w13ePGOptions{failOn: []string{stage.failOn}}),
				Dataset:  openRecorderPG(rec),
				Business: openRecorderPG(rec),
				Now:      kitNow,
			}
			ok, err := store.hasPostgresAccountStatsRows(ctx, target)
			if err == nil && ok {
				return errors.New("不应存在")
			}
			return err
		})
		// 全空 → false, nil。
		{
			rec := newPGRecorder()
			store := &RecordCleanupStore{
				Stats:    openRecorderPG(rec),
				Dataset:  openRecorderPG(rec),
				Business: openRecorderPG(rec),
				Now:      kitNow,
			}
			ok, err := store.hasPostgresAccountStatsRows(ctx, retention.ExpiredDeletedAccountTarget{AccountID: "acc-1"})
			if err != nil || ok {
				t.Fatalf("无数据应 false: %v %v", ok, err)
			}
		}
	}
	// final stats：BeginTx 失败、scope 删除失败、清理扣减失败、Commit 失败。
	{
		accountTarget := retention.ExpiredDeletedAccountTarget{
			AccountID: "acc-1", SystemAccountID: "sys-1",
			AuthorizationIDs: []string{"auth-1"}, TeamScopeIDs: []string{"acc-1:team-9"},
		}
		stages := []pgStage{
			{"scope stats account", "scope_type IN ('account', 'caller_account')"},
			{"scope stats auth", "scope_type = 'account_authorization' AND scope_id = ANY"},
			{"scope stats team", "scope_type = 'account_authorization_team' AND scope_id = ANY"},
			{"job state account", "DELETE FROM juhe_stats.stats_job_state WHERE scope_type IN ('account', 'caller_account')"},
			{"job state team like", "scope_type = 'account_authorization_team' AND scope_id LIKE"},
			{"quality scores", "DELETE FROM juhe_stats.account_quality_scores"},
			{"quality minute", "DELETE FROM juhe_stats.account_quality_minute_stats"},
			{"auth report", "DELETE FROM juhe_stats.authorization_team_usage_summary_daily WHERE resource_filter_type"},
			{"job state auth", "DELETE FROM juhe_stats.stats_job_state WHERE scope_type = 'account_authorization' AND scope_id = ANY"},
			{"job state team", "DELETE FROM juhe_stats.stats_job_state WHERE scope_type = 'account_authorization_team' AND scope_id = ANY"},
			{"deductions", "DELETE FROM juhe_stats.usage_record_cleanup_deductions"},
			{"snapshots", "DELETE FROM juhe_stats.account_usage_snapshots"},
		}
		runPGStages(t, stages, func(t *testing.T, stage pgStage) error {
			rec := newPGRecorder()
			store := &RecordCleanupStore{
				Stats:    w13eOpenDecoratedPG(rec, w13ePGOptions{failOn: []string{stage.failOn}}),
				Dataset:  openRecorderPG(rec),
				Business: openRecorderPG(rec),
				Now:      kitNow,
			}
			return store.cleanupPostgresAccountFinalStats(ctx, accountTarget)
		})
		// BeginTx / Commit 失败。
		{
			rec := newPGRecorder()
			store := &RecordCleanupStore{
				Stats:    w13eOpenDecoratedPG(rec, w13ePGOptions{failBegin: true}),
				Dataset:  openRecorderPG(rec),
				Business: openRecorderPG(rec),
				Now:      kitNow,
			}
			if err := store.cleanupPostgresAPIKeyFinalStats(ctx, "key-1", "sys-1"); err == nil {
				t.Fatalf("api key final stats BeginTx 失败应透传")
			}
			if err := store.cleanupPostgresAccountFinalStats(ctx, accountTarget); err == nil {
				t.Fatalf("account final stats BeginTx 失败应透传")
			}
		}
		{
			rec := newPGRecorder()
			store := &RecordCleanupStore{
				Stats:    w13eOpenDecoratedPG(rec, w13ePGOptions{failCommit: true}),
				Dataset:  openRecorderPG(rec),
				Business: openRecorderPG(rec),
				Now:      kitNow,
			}
			if err := store.cleanupPostgresAPIKeyFinalStats(ctx, "key-1", "sys-1"); err == nil {
				t.Fatalf("api key final stats Commit 失败应透传")
			}
			if err := store.cleanupPostgresAccountFinalStats(ctx, accountTarget); err == nil {
				t.Fatalf("account final stats Commit 失败应透传")
			}
		}
		// 授权报表删除空 ids 直通。
		{
			rec := newPGRecorder()
			store := &RecordCleanupStore{Stats: openRecorderPG(rec), Now: kitNow}
			tx, err := store.Stats.BeginTx(ctx, nil)
			if err != nil {
				t.Fatalf("begin: %v", err)
			}
			if err := store.deletePostgresAccountAuthorizationReportRows(ctx, tx, nil); err != nil {
				t.Fatalf("空 ids 不应报错: %v", err)
			}
			_ = tx.Rollback()
		}
		// runPostgresBatchInTx Begin/Commit 失败。
		{
			rec := newPGRecorder()
			store := &RecordCleanupStore{
				Stats: w13eOpenDecoratedPG(rec, w13ePGOptions{failBegin: true}),
				Now:   kitNow,
			}
			if _, err := store.runPostgresBatchInTx(ctx, func(_ *sql.Tx) (int64, error) { return 0, nil }); err == nil {
				t.Fatalf("batch BeginTx 失败应透传")
			}
		}
	}
}
