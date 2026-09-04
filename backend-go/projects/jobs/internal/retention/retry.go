package retention

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

// Schedule constants mirror the ingest-worker registry entries for
// api-key-record-cleanup-retry and account-record-cleanup-retry in
// background-jobs.ts.
const (
	RecordCleanupRetryInterval     = time.Minute
	APIKeyRecordRetryInitialDelay  = 24 * time.Second
	AccountRecordRetryInitialDelay = 42 * time.Second
	RecordCleanupRetryLeaseTTL     = 2 * time.Minute
	RecordCleanupRetryTargetLimit  = 1
)

// RecordCleanupRetryJob mirrors runApiKeyRecordCleanupRetry and
// runAccountRecordCleanupRetry: each retry round processes at most one
// pending target; postgres mode forwards no stats-writer callback.
type RecordCleanupRetryJob struct {
	Mode    Mode
	Clock   Clock
	Logger  *slog.Logger
	APIKey  APIKeyRecordCleanupRetryer
	Account AccountRecordCleanupRetryer
	Stats   StatsWriter
}

// RunAPIKey mirrors runApiKeyRecordCleanupRetry.
func (j *RecordCleanupRetryJob) RunAPIKey(ctx context.Context) error {
	summary, err := j.apiKey().CleanupPendingTargets(ctx, RecordCleanupRetryTargetLimit, j.statsWriter())
	if err != nil {
		j.logger().Error("已删除 API Key 关联数据清理重试失败",
			"event", "background_api_key_record_cleanup_retry_failed", "error", err)
		return err
	}
	if summary.Attempted > 0 {
		j.logger().Info("已删除 API Key 关联数据清理重试完成",
			append([]any{"event", "background_api_key_record_cleanup_retry_completed"}, pendingSummaryValues(summary)...)...)
	}
	return nil
}

// RunAccount mirrors runAccountRecordCleanupRetry.
func (j *RecordCleanupRetryJob) RunAccount(ctx context.Context) error {
	summary, err := j.account().CleanupPendingTargets(ctx, RecordCleanupRetryTargetLimit, j.statsWriter())
	if err != nil {
		j.logger().Error("已删除 AI 账户关联数据清理重试失败",
			"event", "background_account_record_cleanup_retry_failed", "error", err)
		return err
	}
	if summary.Attempted > 0 {
		j.logger().Info("已删除 AI 账户关联数据清理重试完成",
			append([]any{"event", "background_account_record_cleanup_retry_completed"}, pendingSummaryValues(summary)...)...)
	}
	return nil
}

func (j *RecordCleanupRetryJob) apiKey() APIKeyRecordCleanupRetryer {
	if j.APIKey == nil {
		return missingAPIKeyRetryer{}
	}
	return j.APIKey
}

func (j *RecordCleanupRetryJob) account() AccountRecordCleanupRetryer {
	if j.Account == nil {
		return missingAccountRetryer{}
	}
	return j.Account
}

// statsWriter mirrors the Node conditional callback: postgres mode passes
// nil so the retryer skips stats deduction dispatch.
func (j *RecordCleanupRetryJob) statsWriter() StatsWriter {
	if j.Mode == ModePostgres {
		return nil
	}
	return j.Stats
}

func (j *RecordCleanupRetryJob) logger() *slog.Logger {
	if j.Logger != nil {
		return j.Logger
	}
	return slog.Default()
}

type missingAPIKeyRetryer struct{}

func (missingAPIKeyRetryer) CleanupPendingTargets(context.Context, int, StatsWriter) (PendingCleanupSummary, error) {
	return PendingCleanupSummary{}, errRetryerMissing("API Key")
}

type missingAccountRetryer struct{}

func (missingAccountRetryer) CleanupPendingTargets(context.Context, int, StatsWriter) (PendingCleanupSummary, error) {
	return PendingCleanupSummary{}, errRetryerMissing("AI 账户")
}

func errRetryerMissing(domain string) error {
	return errors.New("retention " + domain + " record cleanup retryer 未初始化")
}

func pendingSummaryValues(summary PendingCleanupSummary) []any {
	return []any{
		"attempted", summary.Attempted,
		"completed", summary.Completed,
		"deferred", summary.Deferred,
		"failed", summary.Failed,
		"deletedRows", summary.DeletedRows,
	}
}
