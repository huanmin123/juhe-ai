package runtimelog

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

type ownerLeaseContextKey struct{}

var ErrOwnerLeaseLost = errors.New("运行日志 owner lease 已失效或已移交")

// ErrOwnerLeaseFenced remains an alias for callers that used the original
// fence-oriented name. A lost lease and a rejected stale fence token have the
// same recovery requirement: do not perform the write-side effect.
var ErrOwnerLeaseFenced = ErrOwnerLeaseLost

func withOwnerLease(ctx context.Context, lease OwnerLease) context.Context {
	return context.WithValue(ctx, ownerLeaseContextKey{}, lease)
}

func ownerLeaseFromContext(ctx context.Context) (OwnerLease, error) {
	lease, ok := ctx.Value(ownerLeaseContextKey{}).(OwnerLease)
	if !ok || lease.OwnerID == "" || lease.FenceToken <= 0 {
		return OwnerLease{}, errors.New("运行日志写入缺少有效 owner fence token")
	}
	return lease, nil
}

// RunWithOwnerLease makes a Go process the sole F1 writer for one Store. The
// lease lives beside the indexed facts, so it works in both SQLite and PG mode
// without introducing a queue or a separate coordination service.
func RunWithOwnerLease(ctx context.Context, config Config, store Store, run func(context.Context) error) error {
	lease, acquired, err := store.AcquireOwnerLease(ctx, config.OwnerID, config.OwnerLease)
	if err != nil {
		return fmt.Errorf("获取运行日志 owner lease 失败: %w", err)
	}
	if !acquired {
		return fmt.Errorf("运行日志 owner lease 已由另一个 Go 实例持有")
	}

	runCtx, cancel := context.WithCancelCause(withOwnerLease(ctx, lease))
	defer cancel(nil)
	stopRenewal := make(chan struct{})
	renewalDone := make(chan struct{})
	var renewalErr error
	var renewalMu sync.Mutex
	go func() {
		defer close(renewalDone)
		interval := config.OwnerLease / 3
		if interval <= 0 {
			interval = time.Second
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stopRenewal:
				return
			case <-runCtx.Done():
				return
			case <-ticker.C:
				renewed, err := store.RenewOwnerLease(runCtx, lease, config.OwnerLease)
				if err == nil && renewed {
					continue
				}
				if err == nil {
					err = errors.New("运行日志 owner lease 已丢失")
				}
				renewalMu.Lock()
				renewalErr = err
				renewalMu.Unlock()
				cancel(err)
				return
			}
		}
	}()

	runErr := run(runCtx)
	close(stopRenewal)
	<-renewalDone
	releaseCtx, releaseCancel := context.WithTimeout(context.Background(), 5*time.Second)
	releaseErr := store.ReleaseOwnerLease(releaseCtx, lease)
	releaseCancel()
	renewalMu.Lock()
	leaseErr := renewalErr
	renewalMu.Unlock()
	if errors.Is(runErr, context.Canceled) && leaseErr != nil {
		runErr = nil
	}
	return errors.Join(runErr, leaseErr, releaseErr)
}
