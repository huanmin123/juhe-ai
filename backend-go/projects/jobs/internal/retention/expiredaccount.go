package retention

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

// expiredDeletedAccountCleanupDbServiceTimeoutMs mirrors the Node constant.
const (
	ExpiredDeletedAccountDbServiceTimeout = 60 * time.Second
	// Schedule constants mirror the ops-worker registry entry for
	// expired-deleted-account-cleanup in background-jobs.ts.
	ExpiredDeletedAccountInterval     = 24 * time.Hour
	ExpiredDeletedAccountInitialDelay = 14 * time.Minute
	ExpiredDeletedAccountLeaseTTL     = 10 * time.Minute
)

// ExpiredDeletedAccountJob mirrors runExpiredDeletedAccountCleanup: run the
// expired logical-delete physical cleanup, then enqueue record-maintenance
// jobs for every target that still owns related records.
type ExpiredDeletedAccountJob struct {
	DB       DbService
	Enqueuer RecordMaintenanceEnqueuer
	Clock    Clock
	Logger   *slog.Logger
	// Timeout overrides ExpiredDeletedAccountDbServiceTimeout when non-zero.
	Timeout time.Duration
}

// Run wraps run with the shared failure log (Node try/catch: the catch logs
// and rethrows, including the missing-result guard error).
func (j *ExpiredDeletedAccountJob) Run(ctx context.Context) error {
	if err := j.run(ctx); err != nil {
		j.logger().Error("超过一个月的逻辑删除 AI 账户物理清理失败",
			"event", "background_expired_deleted_account_cleanup_failed", "error", err)
		return err
	}
	return nil
}

func (j *ExpiredDeletedAccountJob) run(ctx context.Context) error {
	if j == nil || j.DB == nil {
		return errors.New("retention db service 未初始化")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	timeout := j.Timeout
	if timeout <= 0 {
		timeout = ExpiredDeletedAccountDbServiceTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	summary, err := j.DB.CleanupExpiredDeletedAccounts(runCtx)
	if err != nil {
		return err
	}
	if summary == nil {
		return errors.New("DB service 未返回逻辑删除 AI 账户清理结果")
	}
	for _, target := range summary.RecordCleanupTargets {
		job, jobErr := AccountRelatedCleanupJob(target, clockNow(j.Clock))
		if jobErr != nil {
			return jobErr
		}
		result := j.enqueuer().Enqueue(ctx, job)
		if !result.Queued {
			j.logger().Warn("逻辑删除 AI 账户物理清理发现关联记录未清空，投递记录清理失败",
				"event", "background_expired_deleted_account_record_cleanup_enqueue_failed",
				"accountId", target.AccountID,
				"systemAccountId", target.SystemAccountID,
				"droppedReason", result.DroppedReason,
			)
		}
	}
	if summary.Attempted > 0 || summary.OrphanedAuthorizationInstances > 0 {
		j.logger().Info("逻辑删除 AI 账户物理清理与孤儿授权实例扫尾完成",
			append([]any{"event", "background_expired_deleted_account_cleanup_completed"}, summary.Values()...)...)
	}
	return nil
}

func (j *ExpiredDeletedAccountJob) enqueuer() RecordMaintenanceEnqueuer {
	if j.Enqueuer == nil {
		return missingEnqueuer{}
	}
	return j.Enqueuer
}

func (j *ExpiredDeletedAccountJob) logger() *slog.Logger {
	if j.Logger != nil {
		return j.Logger
	}
	return slog.Default()
}

type missingEnqueuer struct{}

func (missingEnqueuer) Enqueue(context.Context, RecordMaintenanceJob) EnqueueResult {
	return EnqueueResult{Queued: false, DroppedReason: "worker_dispatch_failed"}
}

func (missingEnqueuer) EnqueueAsync(context.Context, RecordMaintenanceJob) error {
	return errors.New("retention record maintenance enqueuer 未初始化")
}

// Values returns the Node log object key order for the cleanup summary.
func (s ExpiredDeletedAccountSummary) Values() []any {
	return []any{
		"cutoffDeletedAt", s.CutoffDeletedAt,
		"orphanedAuthorizationInstances", s.OrphanedAuthorizationInstances,
		"attempted", s.Attempted,
		"completed", s.Completed,
		"deferred", s.Deferred,
		"failed", s.Failed,
		"deletedRows", s.DeletedRows,
		"physicallyDeletedAccounts", s.PhysicallyDeletedAccounts,
		"physicallyDeletedAuthorizations", s.PhysicallyDeletedAuthorizations,
		"physicallyDeletedGrants", s.PhysicallyDeletedGrants,
		"physicallyDeletedGroupBindings", s.PhysicallyDeletedGroupBindings,
		"recordCleanupTargets", len(s.RecordCleanupTargets),
	}
}
