package operationlog

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/schedulejitter"
)

// RunOwner is the resident F4 owner body (去跨进程战役第四刀): the retention
// ticker previously lived inside the deleted input server's accept loop
// (RunInputServerSharedLease) and moves here unchanged — one bounded
// retention pass per schedulejitter-delayed RetentionInterval, reading the
// retention-days business setting each pass, fenced by the shared
// LeaseKeeper. A lost lease is terminal for the component (the supervisor
// boundary retries until the process restarts with a fresh fence), while a
// transient retention failure keeps the cadence alive like before.
func RunOwner(ctx context.Context, store Store, keeper *LeaseKeeper, cfg Config, logger *slog.Logger) error {
	if keeper == nil {
		return fmt.Errorf("F4 owner 组件要求共享 lease keeper")
	}
	if logger == nil {
		logger = slog.Default()
	}
	lease := keeper.Lease()
	retention := time.NewTimer(schedulejitter.Delay(cfg.RetentionInterval))
	defer retention.Stop()
	for {
		select {
		case <-ctx.Done():
			// Graceful stop mirrors the deleted listener loop: cancellation is
			// not an owner failure, so the component settles quietly.
			return nil
		case <-keeper.Lost():
			// The shared lease is gone (expired or taken over): surface the
			// terminal fence error instead of serving fenced writes.
			if lostErr := keeper.LostError(); lostErr != nil {
				return lostErr
			}
			return ErrOwnerLeaseLost
		case <-retention.C:
			retentionCtx, cancel := storeContext(ctx)
			days, err := store.RetentionDays(retentionCtx, cfg.RetentionDays)
			if err != nil {
				cancel()
				logger.Error("F4 operation log retention setting unavailable; skip pass", "error", err)
				retention.Reset(schedulejitter.Delay(cfg.RetentionInterval))
				continue
			}
			cutoff := time.Now().UTC().AddDate(0, 0, -days)
			deleted, err := store.CleanupRetention(retentionCtx, lease, cutoff, cfg.RetentionBatchSize)
			cancel()
			if err != nil {
				if errors.Is(err, ErrOwnerLeaseLost) {
					return err
				}
				logger.Error("F4 operation log retention failed", "error", err, "mode", cfg.Mode, "cutoff", cutoff.Format(time.RFC3339Nano))
			} else {
				logger.Info("F4 operation log retention complete", "mode", cfg.Mode, "retentionDays", days, "cutoff", cutoff.Format(time.RFC3339Nano), "deleted", deleted)
			}
			retention.Reset(schedulejitter.Delay(cfg.RetentionInterval))
		}
	}
}
