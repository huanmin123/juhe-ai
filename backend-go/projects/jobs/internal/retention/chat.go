package retention

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

// Schedule constants mirror the ops-worker registry entry for
// chat-retention-cleanup in background-jobs.ts.
const (
	ChatRetentionInterval                = 10 * time.Minute
	ChatRetentionInitialDelay            = 270 * time.Second
	ChatRetentionSchedulerTimeout        = 2 * time.Minute
	ChatRetentionLeaseTTL                = 5 * time.Minute
	ChatRetentionDbServiceTimeout        = 60 * time.Second
	ChatRetentionLimit                   = 1000
	chatRetentionInterruptedBeforeMillis = 20 * 60 * 1000
)

// ChatRetentionJob mirrors runChatRetentionCleanup: forward the
// cleanup_chat_retention operation to the DB service with the fixed
// interruption window and limit, and log the completion summary.
type ChatRetentionJob struct {
	DB            DbService
	Clock         Clock
	Logger        *slog.Logger
	RetentionDays int
	// Lease mirrors the Postgres scheduled-lease fence; the DB service
	// rejects postgres chat retention without it.
	Lease *ScheduledLeaseFence
	// Timeout overrides ChatRetentionDbServiceTimeout when non-zero.
	Timeout time.Duration
}

// Run mirrors runChatRetentionCleanup. Errors and the missing-result guard
// keep the Node messages byte for byte.
func (j *ChatRetentionJob) Run(ctx context.Context) error {
	if j == nil || j.DB == nil {
		return errors.New("retention db service 未初始化")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	now := clockNow(j.Clock)
	input := ChatRetentionInput{
		Now:               ISOString(now),
		InterruptedBefore: ISOString(time.UnixMilli(now.UnixMilli() - chatRetentionInterruptedBeforeMillis)),
		Limit:             ChatRetentionLimit,
		RetentionDays:     j.RetentionDays,
		ScheduledLease:    j.Lease,
		Priority:          "low",
	}
	timeout := j.Timeout
	if timeout <= 0 {
		timeout = ChatRetentionDbServiceTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	result, err := j.DB.CleanupChatRetention(runCtx, input)
	if err != nil {
		return err
	}
	if result == nil {
		return errors.New("DB service 未返回 AI 问答保留清理结果")
	}
	if result.ShouldLog() {
		logger := j.logger()
		logger.Info("AI 问答过期数据清理与中断轮次恢复完成",
			append([]any{
				"event", "chat_retention_cleanup_completed",
				"retentionDays", j.RetentionDays,
			}, result.Values()...)...)
	}
	return nil
}

func (j *ChatRetentionJob) logger() *slog.Logger {
	if j.Logger != nil {
		return j.Logger
	}
	return slog.Default()
}

func clockNow(clock Clock) time.Time {
	if clock != nil {
		return clock()
	}
	return time.Now()
}

// Values returns the Node log object key order for chat retention results.
func (r ChatRetentionResult) Values() []any {
	return []any{
		"droppedPartitions", r.DroppedPartitions,
		"deletedMessages", r.DeletedMessages,
		"deletedConversations", r.DeletedConversations,
		"recoveredTurns", r.RecoveredTurns,
		"recoveredCompactions", r.RecoveredCompactions,
		"claimedAssets", r.ClaimedAssets,
		"deletedAssets", r.DeletedAssets,
		"failedAssets", r.FailedAssets,
		"hasMoreAssets", r.HasMoreAssets,
		"deletedCheckpoints", r.DeletedCheckpoints,
		"hasMoreCheckpoints", r.HasMoreCheckpoints,
		"hasMore", r.HasMore,
	}
}
