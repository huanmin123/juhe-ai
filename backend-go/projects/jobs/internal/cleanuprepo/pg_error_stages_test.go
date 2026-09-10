package cleanuprepo

import (
	"context"
	"database/sql/driver"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/retention"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/statsagg"
)

// PG 链路的「按阶段注入失败」测试：对每个语句家族依次注入错误，覆盖主链
// 上所有 `return err` 分支。录制驱动不校验 SQL 文本，因此 PG 专有语句
// （ESCAPE、DETACH PARTITION 等）在此同样可执行。

type pgStage struct {
	name   string
	failOn string
}

func runPGStages(t *testing.T, stages []pgStage, build func(t *testing.T, stage pgStage) error) {
	t.Helper()
	for _, stage := range stages {
		t.Run(stage.name, func(t *testing.T) {
			if err := build(t, stage); err == nil {
				t.Fatalf("阶段 %s（failOn=%s）应产生错误", stage.name, stage.failOn)
			}
		})
	}
}

func seedKitPGAPIKeyFlow(t *testing.T, rec *pgRecorder, withResidualUsage bool) {
	t.Helper()
	row := pgTestRow()
	rec.script("stats_job_state", []string{"job_name", "cursor_created_at", "cursor_id"}, [][]driver.Value{
		{"usage_stats_aggregation", "2026-01-05T03:00:00.000Z", "rec-0"},
		{"client_ip_stats_aggregation", "2026-01-05T03:00:00.000Z", "rec-0"},
	})
	rec.script("FROM juhe_usage.usage_records", usageRecordColumns(), [][]driver.Value{usageRecordDriverRow(row)})
	rec.script("SELECT usage_id, shard_key", []string{
		"usage_id", "shard_key", "system_account_id", "api_key_id", "account_id",
	}, [][]driver.Value{{"rec-1", "sk-1", "sys-1", "key-1", "acc-1"}})
	if withResidualUsage {
		rec.script("SELECT 1 AS found", []string{"found"}, [][]driver.Value{{int64(1)}})
	}
}

// TestPGAPIKeyFlowErrorStages：CleanupAPIKeyRelatedPostgres 全链逐阶段失败。
func TestPGAPIKeyFlowErrorStages(t *testing.T) {
	stages := []pgStage{
		{"target upsert", "INSERT INTO juhe_dataset.api_key_record_cleanup_targets"},
		{"floor cursor", "juhe_stats.stats_job_state"},
		{"batch select", "FROM juhe_usage.usage_records"},
		{"deduction insert", "INSERT INTO juhe_stats.usage_record_cleanup_deductions"},
		{"deduction select", "FOR UPDATE"},
		{"stats subtract", "GREATEST(0, request_count"},
		{"mark subtracted", "UPDATE juhe_stats.usage_record_cleanup_deductions"},
		{"catalog entries", "usage_record_shard_entries"},
		{"api key scope shrink", "usage_record_api_key_shards"},
		{"partition delete", "DELETE FROM juhe_usage.usage_records"},
		{"mark deleted", "shard_deleted_at"},
		{"usage exists", "SELECT 1 AS found"},
		{"final stats", "scope_type = 'api_key'"},
		{"deductions clear", "usage_record_cleanup_deductions WHERE api_key_id"},
		{"stats exists", "FROM juhe_stats.usage_rank_snapshots"},
		{"clear target", "DELETE FROM juhe_dataset.api_key_record_cleanup_targets"},
	}
	runPGStages(t, stages, func(t *testing.T, stage pgStage) error {
		rec := newPGRecorder()
		seedKitPGAPIKeyFlow(t, rec, stage.name == "deferred mark")
		store := &RecordCleanupStore{
			Stats:    openKitFailingRecorderPG(rec, stage.failOn),
			Dataset:  openKitFailingRecorderPG(rec, stage.failOn),
			Business: openRecorderPG(rec),
			Now:      kitNow,
			Timezone: func(context.Context) (*time.Location, error) { return kitZone(), nil },
		}
		_, err := store.CleanupAPIKeyRelatedPostgres(context.Background(), "key-1", "sys-1")
		return err
	})
	// deferred 阶段（有残余使用行）：deferred 标记失败。
	rec := newPGRecorder()
	seedKitPGAPIKeyFlow(t, rec, true)
	failing := &RecordCleanupStore{
		Stats:    openKitFailingRecorderPG(rec, "last_blocked_reason"),
		Dataset:  openKitFailingRecorderPG(rec, "last_blocked_reason"),
		Business: openRecorderPG(rec),
		Now:      kitNow,
		Timezone: func(context.Context) (*time.Location, error) { return kitZone(), nil },
	}
	if _, err := failing.CleanupAPIKeyRelatedPostgres(context.Background(), "key-1", "sys-1"); err == nil {
		t.Fatalf("deferred 标记注入应报错")
	}
	// 无失败注入 → 完成并清除目标。
	rec = newPGRecorder()
	seedKitPGAPIKeyFlow(t, rec, false)
	store := &RecordCleanupStore{
		Stats:    openRecorderPG(rec),
		Dataset:  openRecorderPG(rec),
		Business: openRecorderPG(rec),
		Now:      kitNow,
		Timezone: func(context.Context) (*time.Location, error) { return kitZone(), nil },
	}
	result, err := store.CleanupAPIKeyRelatedPostgres(context.Background(), "key-1", "sys-1")
	if err != nil {
		t.Fatalf("无注入应成功：%v", err)
	}
	if result.HasMore {
		t.Fatalf("无残余应完成：%+v", result)
	}
}

// TestPGAccountFlowErrorStages：CleanupAccountRelatedPostgres 全链逐阶段失败。
func TestPGAccountFlowErrorStages(t *testing.T) {
	stages := []pgStage{
		{"target upsert", "INSERT INTO juhe_dataset.account_record_cleanup_targets"},
		{"floor cursor", "juhe_stats.stats_job_state"},
		{"batch select", "FROM juhe_usage.usage_records"},
		{"deduction insert", "INSERT INTO juhe_stats.usage_record_cleanup_deductions"},
		{"deduction select", "FOR UPDATE"},
		{"stats subtract", "GREATEST(0, request_count"},
		{"mark subtracted", "UPDATE juhe_stats.usage_record_cleanup_deductions"},
		{"catalog entries", "usage_record_shard_entries"},
		{"account scope shrink", "usage_record_account_shards"},
		{"partition delete", "DELETE FROM juhe_usage.usage_records"},
		{"mark deleted", "shard_deleted_at"},
		{"usage exists", "SELECT 1 AS found"},
		{"final stats", "scope_type IN ('account', 'caller_account')"},
		{"stats exists", "FROM juhe_stats.usage_rank_snapshots"},
	}
	runPGStages(t, stages, func(t *testing.T, stage pgStage) error {
		rec := newPGRecorder()
		seedKitPGAPIKeyFlow(t, rec, false)
		row := pgTestRow()
		accountID := "acc-1"
		row.AccountID = &accountID
		rec.script("FROM juhe_usage.usage_records", usageRecordColumns(), [][]driver.Value{usageRecordDriverRow(row)})
		store := &RecordCleanupStore{
			Stats:    openKitFailingRecorderPG(rec, stage.failOn),
			Dataset:  openKitFailingRecorderPG(rec, stage.failOn),
			Business: openRecorderPG(rec),
			Now:      kitNow,
			Timezone: func(context.Context) (*time.Location, error) { return kitZone(), nil },
		}
		_, err := store.CleanupAccountRelatedPostgres(context.Background(), retentionTarget())
		return err
	})
}

// TestSubtractPostgresRowsErrorStages：subtractPostgresUsageStatsRows 全家族。
func TestSubtractPostgresRowsErrorStages(t *testing.T) {
	stages := []pgStage{
		{"auth lookup", "FROM juhe_business.resource_authorizations"},
		{"totals update", "UPDATE juhe_stats.usage_stats_totals"},
		{"totals delete", "DELETE FROM juhe_stats.usage_stats_totals"},
		{"minute update", "UPDATE juhe_stats.usage_stats_minute"},
		{"minute delete", "DELETE FROM juhe_stats.usage_stats_minute"},
		{"hourly update", "UPDATE juhe_stats.usage_stats_hourly"},
		{"daily update", "UPDATE juhe_stats.usage_stats_daily"},
		{"weekly update", "UPDATE juhe_stats.usage_stats_weekly"},
		{"monthly update", "UPDATE juhe_stats.usage_stats_monthly"},
		{"latency minute", "UPDATE juhe_stats.usage_latency_minute"},
		{"latency hourly", "UPDATE juhe_stats.usage_latency_hourly"},
		{"latency daily", "UPDATE juhe_stats.usage_latency_daily"},
		{"latency weekly", "UPDATE juhe_stats.usage_latency_weekly"},
		{"latency monthly", "UPDATE juhe_stats.usage_latency_monthly"},
		{"model minute", "UPDATE juhe_stats.usage_model_minute"},
		{"model hourly", "UPDATE juhe_stats.usage_model_hourly"},
		{"model daily", "UPDATE juhe_stats.usage_model_daily"},
		{"model weekly", "UPDATE juhe_stats.usage_model_weekly"},
		{"model monthly", "UPDATE juhe_stats.usage_model_monthly"},
		{"error minute", "UPDATE juhe_stats.usage_error_minute"},
		{"error hourly", "UPDATE juhe_stats.usage_error_hourly"},
		{"error daily", "UPDATE juhe_stats.usage_error_daily"},
		{"error weekly", "UPDATE juhe_stats.usage_error_weekly"},
		{"error monthly", "UPDATE juhe_stats.usage_error_monthly"},
		{"auth team update", "authorization_team_usage_summary_daily"},
		{"auth user update", "UPDATE juhe_stats.authorization_user_usage_summary_daily"},
		{"quality update", "UPDATE juhe_stats.account_quality_minute_stats"},
		{"quality delete", "DELETE FROM juhe_stats.account_quality_minute_stats"},
		{"quality dirty", "INSERT INTO juhe_stats.account_quality_dirty_accounts"},
		{"health delete", "DELETE FROM juhe_stats.account_health_hourly"},
		{"overview dirty", "INSERT INTO juhe_stats.usage_overview_dirty_scopes"},
		{"ai dirty", "INSERT INTO juhe_stats.ai_performance_summary_dirty_system_accounts"},
		{"quota dirty", "INSERT INTO juhe_stats.usage_quota_hourly_window_dirty_scopes"},
	}
	text := "acc-1"
	num := 404.0
	duration := 340.0
	firstToken := 120.0
	team := "team-9"
	row := statsagg.UsageStatsRecordRow{
		ID: "rec-stage", SystemAccountID: "sys-1", TrafficSource: "account_health_check",
		APIKeyID: kitText("key-1"), AccountID: &text, ProviderCode: kitText("openai"),
		Model: kitText("gpt-stage"), StatusCode: &num, Success: 0,
		DurationMs: &duration, FirstTokenMs: &firstToken,
		FailureAttribution:          kitText("account_upstream"),
		AccountOwnerSystemAccountID: kitText("owner-2"), AccountAccessType: kitText("account_authorized"),
		AccountAuthorizationID: kitText("auth-1"), CreatedAt: kitCreatedAt,
		AccountAuthorizationSourceType: kitText("team"), AccountAuthorizationSourceTeamID: &team,
	}
	runPGStages(t, stages, func(t *testing.T, stage pgStage) error {
		rec := newPGRecorder()
		rec.script("FROM juhe_business.resource_authorizations authorizations",
			[]string{"id", "resource_id", "instance_account_id"}, nil)
		store := &RecordCleanupStore{
			Stats:    openKitFailingRecorderPG(rec, stage.failOn),
			Business: openRecorderPG(rec),
			Now:      kitNow,
			Timezone: func(context.Context) (*time.Location, error) { return kitZone(), nil },
		}
		tx, err := store.Stats.BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatalf("BeginTx: %v", err)
		}
		defer func() { _ = tx.Rollback() }()
		return store.subtractPostgresUsageStatsRows(context.Background(), tx,
			[]statsagg.UsageStatsRecordRow{row}, kitUpdatedAt, kitZone())
	})
}

// TestCodexPGErrorStages：codex PG 清理与结算逐阶段失败。
func TestCodexPGErrorStages(t *testing.T) {
	stages := []pgStage{
		{"sessions select", "FROM juhe_codex_context.codex_context_sessions"},
		{"responses select", "juhe_codex_context.codex_context_responses\n      WHERE session_id IN"},
		{"compacts select", "juhe_codex_context.codex_context_compacts\n      WHERE session_id IN"},
		{"queue enqueue", "INSERT INTO juhe_codex_context.codex_context_storage_cleanup_queue"},
		{"responses delete", "DELETE FROM juhe_codex_context.codex_context_responses"},
		{"compacts delete", "DELETE FROM juhe_codex_context.codex_context_compacts"},
		{"remaining select", "SELECT session_id, MAX(expires_at)"},
		{"session refresh", "UPDATE juhe_codex_context.codex_context_sessions"},
		{"session delete", "DELETE FROM juhe_codex_context.codex_context_sessions"},
		{"pending select", "WHERE next_attempt_at <="},
		{"referenced delete", "DELETE FROM codex_context_storage_cleanup_queue"},
	}
	seed := func(rec *pgRecorder) {
		rec.script("FROM juhe_codex_context.codex_context_sessions", []string{"id", "expires_at"},
			[][]driver.Value{{"s-1", "2026-09-01T00:00:00.000Z"}, {"s-2", "2026-09-02T00:00:00.000Z"}})
		rec.script("juhe_codex_context.codex_context_responses\n      WHERE session_id IN", []string{"storage_key"},
			[][]driver.Value{{"k-1"}})
		rec.script("juhe_codex_context.codex_context_compacts\n      WHERE session_id IN", []string{"storage_key"}, nil)
		rec.script("juhe_codex_context.codex_context_responses\n        WHERE session_id IN", []string{"session_id", "expires_at"},
			[][]driver.Value{{"s-2", "2026-12-01T00:00:00.000Z"}})
		rec.script("juhe_codex_context.codex_context_compacts\n        WHERE session_id IN", []string{"session_id", "expires_at"}, nil)
		rec.script("WHERE next_attempt_at <=", []string{"storage_key"}, [][]driver.Value{{"k-1"}})
		rec.script("juhe_codex_context.codex_context_responses\n        WHERE storage_key IN", []string{"storage_key"},
			[][]driver.Value{{"k-1"}})
		rec.script("juhe_codex_context.codex_context_compacts\n        WHERE storage_key IN", []string{"storage_key"}, nil)
	}
	runPGStages(t, stages, func(t *testing.T, stage pgStage) error {
		rec := newPGRecorder()
		seed(rec)
		store := &CodexContextStore{Postgres: true, PG: openKitFailingRecorderPG(rec, stage.failOn), Now: kitNow}
		_, err := store.CleanupExpiredStates(context.Background(), "2026-09-10T00:00:00.000Z", 5)
		return err
	})
}

// TestChatPGErrorStages：chat 清理子链（分区/压缩/检查点/资产）逐阶段失败。
func TestChatPGErrorStages(t *testing.T) {
	t.Run("partitions", func(t *testing.T) {
		stages := []pgStage{
			{"inherit select", "FROM pg_inherits"},
			{"conversation select", "chat_messages_20260101"},
			{"conversation bump", "UPDATE juhe_chat.chat_conversations"},
			{"drop partition", "DROP TABLE IF EXISTS"},
		}
		runPGStages(t, stages, func(t *testing.T, stage pgStage) error {
			rec := newPGRecorder()
			store := &ChatStore{DB: openKitFailingRecorderPG(rec, stage.failOn), Now: kitNow}
			rec.script("FROM pg_inherits", []string{"partition_name"}, [][]driver.Value{{"chat_messages_20260101"}})
			rec.script("chat_messages_20260101", []string{"conversation_id", "system_account_id"},
				[][]driver.Value{{"conv-1", "sys-1"}})
			tx, err := store.DB.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatalf("BeginTx: %v", err)
			}
			defer func() { _ = tx.Rollback() }()
			outcome, err := store.dropExpiredChatPartitions(context.Background(), tx, kitUpdatedAt, 30,
				func(string, string) {})
			if err != nil {
				return err
			}
			if outcome.DroppedPartitions != 1 {
				t.Fatalf("无注入应裁剪一个分区：%+v", outcome)
			}
			return nil
		})
	})
	t.Run("checkpoints", func(t *testing.T) {
		stages := []pgStage{
			{"select", "FROM juhe_chat.chat_context_checkpoints"},
			{"detach", "context_revision = context_revision + 1"},
			{"delete", "DELETE FROM juhe_chat.chat_context_checkpoints"},
		}
		runPGStages(t, stages, func(t *testing.T, stage pgStage) error {
			rec := newPGRecorder()
			store := &ChatStore{DB: openKitFailingRecorderPG(rec, stage.failOn), Now: kitNow}
			rec.script("FROM juhe_chat.chat_context_checkpoints", []string{"id", "conversation_id", "status"},
				[][]driver.Value{{"cp-1", "conv-1", "active"}})
			_, err := store.cleanupExpiredCheckpoints(context.Background(), kitUpdatedAt, 5)
			return err
		})
	})
	t.Run("assets", func(t *testing.T) {
		stages := []pgStage{
			{"select", "FROM juhe_chat.chat_assets"},
			{"claim", "cleanup_status = 'claimed'"},
			{"claimed select", "SELECT * FROM juhe_chat.chat_assets"},
			{"advisory lock", "pg_advisory_xact_lock"},
			{"asset delete", "DELETE FROM juhe_chat.chat_assets"},
			{"usage update", "UPDATE juhe_chat.chat_user_asset_usage"},
			{"usage delete", "DELETE FROM juhe_chat.chat_user_asset_usage"},
		}
		runPGStages(t, stages, func(t *testing.T, stage pgStage) error {
			rec := newPGRecorder()
			store := &ChatStore{DB: openKitFailingRecorderPG(rec, stage.failOn), Now: kitNow}
			rec.script("FROM juhe_chat.chat_assets", []string{"id", "system_account_id", "storage_key",
				"preview_storage_key", "quota_bytes", "cleanup_attempt_count"},
				[][]driver.Value{{"asset-1", "sys-1", "missing.bin", nil, int64(100), int64(1)}})
			outcome, err := store.cleanupExpiredAssets(context.Background(), kitUpdatedAt, 5)
			if err != nil {
				return err
			}
			// 结算/配额阶段的失败被计入 FailedAssets 而不中止流程。
			if outcome.FailedAssets > 0 {
				return nil
			}
			return context.Canceled
		})
	})
}

// TestDeleteAccountPGErrorStages：孤儿扫尾链逐阶段失败。
func TestDeleteAccountPGErrorStages(t *testing.T) {
	stages := []pgStage{
		{"orphan select", "SELECT accounts.id"},
		{"revoke lookup", "SELECT resource_type, resource_id"},
		{"grants revoke", "UPDATE juhe_business.resource_authorization_grants"},
		{"sources revoke", "UPDATE juhe_business.resource_authorization_sources"},
		{"auth revoke", "UPDATE juhe_business.resource_authorizations"},
		{"quota bindings", "request_quota_hourly_window_scope_bindings"},
		{"account update", "UPDATE juhe_business.accounts"},
		{"deleted ids", "SELECT id FROM juhe_business.accounts WHERE deleted_at"},
		{"tombstone select", "provider_code IN"},
		{"version select", "SELECT current_version"},
		{"version upsert", "INSERT INTO account_health_jobs_input_versions"},
		{"outbox insert", "INSERT INTO account_health_jobs_input_outbox"},
		{"tags delete", "DELETE FROM juhe_business.account_tag_bindings"},
		{"search terms", "DELETE FROM juhe_business.account_name_search_terms"},
		{"search documents", "DELETE FROM juhe_business.account_name_search_documents"},
	}
	runPGStages(t, stages, func(t *testing.T, stage pgStage) error {
		rec := newPGRecorder()
		rec.script("SELECT accounts.id, accounts.system_account_id", []string{
			"id", "system_account_id", "authorization_instance_authorization_id",
			"authorization_instance_source_account_id", "deleted_at",
			"source_deleted_at", "resource_deleted_at",
		}, [][]driver.Value{{"acc-inst", "sys-1", "auth-7", "acc-src", nil, "2026-06-01T00:00:00.000Z", "2026-06-01T00:00:00.000Z"}})
		rec.script("SELECT resource_type, resource_id", []string{"resource_type", "resource_id"},
			[][]driver.Value{{"account", "acc-src"}})
		rec.script("WHERE resource_type = 'account' AND resource_id", []string{"id"}, [][]driver.Value{{"auth-7"}})
		rec.script("SELECT id FROM juhe_business.accounts WHERE deleted_at", []string{"id"}, [][]driver.Value{{"acc-inst"}})
		rec.script("provider_code IN", []string{"id", "config_revision", "dispatch_revision"},
			[][]driver.Value{{"acc-inst", int64(4), int64(6)}})
		store := &DeletedAccountStore{Business: openKitFailingRecorderPG(rec, stage.failOn), Now: kitNow}
		_, err := store.orphanSweepPostgres(context.Background(), 10)
		return err
	})
}

// TestDataRetentionPGErrorStages：保留清理与 usage records PG 链逐阶段失败。
func TestDataRetentionPGErrorStages(t *testing.T) {
	t.Run("stats retention", func(t *testing.T) {
		stages := []pgStage{
			{"first table", "juhe_stats.account_quality_minute_stats"},
			{"mid table", "juhe_stats.usage_rank_snapshots"},
			{"last table", "juhe_stats.account_usage_snapshots"},
		}
		runPGStages(t, stages, func(t *testing.T, stage pgStage) error {
			rec := newPGRecorder()
			store := &StatsRetentionStore{DB: openKitFailingRecorderPG(rec, stage.failOn)}
			_, err := store.CleanupUsageStatsRetention(context.Background(), retention.UsageStatsRetentionInput{
				MinuteCutoffMinute: "2026-01-01T00:00", Limit: 5,
			})
			return err
		})
	})
	t.Run("metrics retention", func(t *testing.T) {
		rec := newPGRecorder()
		store := &StatsRetentionStore{DB: openKitFailingRecorderPG(rec, "juhe_stats.system_metrics_samples")}
		if _, err := store.CleanupSystemMetricsRetention(context.Background(), retention.SystemMetricsRetentionInput{
			SamplesCutoffIso: kitUpdatedAt, Limit: 5,
		}); err == nil {
			t.Fatalf("注入失败应报错")
		}
	})
	t.Run("public api logs", func(t *testing.T) {
		rec := newPGRecorder()
		store := &PublicApiLogsStore{DB: openKitFailingRecorderPG(rec, "juhe_dataset.public_api_logs")}
		if _, err := store.CleanupBefore(context.Background(), kitUpdatedAt, 5); err == nil {
			t.Fatalf("注入失败应报错")
		}
	})
	t.Run("system sessions", func(t *testing.T) {
		rec := newPGRecorder()
		store := &SystemSessionsStore{DB: openKitFailingRecorderPG(rec, "juhe_business.system_sessions")}
		if _, err := store.CleanupExpired(context.Background(), kitUpdatedAt, 5); err == nil {
			t.Fatalf("注入失败应报错")
		}
	})
	t.Run("usage records", func(t *testing.T) {
		stages := []pgStage{
			{"floor cursor", "juhe_stats.stats_job_state"},
			{"partition select", "FROM pg_inherits inherit"},
			{"partition count", "SELECT COUNT(*) AS total"},
			{"partition detach", "DETACH PARTITION"},
			{"batch select", "SELECT id, created_at"},
			{"scope entries", "SELECT usage_id, shard_key"},
			{"entries delete", "DELETE FROM juhe_usage.usage_record_shard_entries"},
			{"account shrink", "DELETE FROM juhe_usage.usage_record_account_shards"},
			{"api key shrink", "DELETE FROM juhe_usage.usage_record_api_key_shards"},
			{"rows delete", "DELETE FROM juhe_usage.usage_records WHERE (created_at, id) IN"},
		}
		runPGStages(t, stages, func(t *testing.T, stage pgStage) error {
			rec := newPGRecorder()
			rec.script("juhe_stats.stats_job_state", []string{"job_name", "cursor_created_at", "cursor_id"},
				[][]driver.Value{
					{"usage_stats_aggregation", "2026-01-05T03:00:00.000Z", "rec-0"},
					{"client_ip_stats_aggregation", "2026-01-05T03:00:00.000Z", "rec-0"},
				})
			// 仅 partition 阶段需要可裁剪分区；行级阶段以空分区列表直达批删。
			if strings.HasPrefix(stage.name, "partition") {
				rec.script("FROM pg_inherits inherit", []string{"partition_name", "partition_bound"},
					[][]driver.Value{{"usage_records_20260101", "FOR VALUES FROM ('2026-01-01') TO ('2026-01-02')"}})
				rec.script("SELECT COUNT(*) AS total", []string{"total"}, [][]driver.Value{{int64(3)}})
			}
			rec.script("SELECT id, created_at", []string{"id", "created_at"},
				[][]driver.Value{{"rec-1", "2026-01-05T01:00:00.000Z"}})
			rec.script("SELECT usage_id, shard_key", []string{
				"usage_id", "shard_key", "system_account_id", "api_key_id", "account_id",
			}, [][]driver.Value{{"rec-1", "sk-1", "sys-1", "key-1", "acc-1"}})
			store := &UsageRecordsStore{
				Catalog: openKitFailingRecorderPG(rec, stage.failOn),
				Stats:   openKitFailingRecorderPG(rec, stage.failOn),
			}
			_, err := store.CleanupProcessedBefore(context.Background(), "2026-09-10T00:00:00.000Z", 5)
			return err
		})
		// 双游标缺一 → cursor nil：无记录时返回空批次。
		emptyRec := newPGRecorder()
		emptyStore := &UsageRecordsStore{Catalog: openRecorderPG(emptyRec), Stats: openRecorderPG(emptyRec)}
		batch, err := emptyStore.CleanupProcessedBefore(context.Background(), "2026-09-10T00:00:00.000Z", 10)
		if err != nil || batch.BlockedReason != "" || batch.DeletedRows != 0 {
			t.Fatalf("无游标空批次 = %+v, %v", batch, err)
		}
		// 无游标 + 有记录 → 阻塞。
		emptyRec.script("FROM juhe_usage.usage_records", []string{"found"}, [][]driver.Value{{int64(1)}})
		batch, err = emptyStore.CleanupProcessedBefore(context.Background(), "2026-09-10T00:00:00.000Z", 10)
		if err != nil || !strings.Contains(batch.BlockedReason, "游标尚未建立") {
			t.Fatalf("阻塞批次 = %+v, %v", batch, err)
		}
	})
}

// TestNormalizeKitSQLAndTextSet：纯 helper 收尾。
func TestNormalizeKitSQLAndTextSet(t *testing.T) {
	if normalizeKitSQL("\n  a\n  b\n") != "a\nb" {
		t.Fatalf("normalizeKitSQL 结果不符")
	}
	if textSet(nil) || textSet(kitText("")) || !textSet(kitText("x")) {
		t.Fatalf("textSet 语义错误")
	}
}
