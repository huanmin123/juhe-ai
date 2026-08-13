package runtimelog

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
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
func RunWithOwnerLease(ctx context.Context, config Config, store Store, run func(context.Context) error) (resultErr error) {
	lease, acquired, err := store.AcquireOwnerLease(ctx, config.OwnerID, config.OwnerLease)
	if err != nil {
		return fmt.Errorf("获取运行日志 owner lease 失败: %w", err)
	}
	if !acquired {
		return fmt.Errorf("运行日志 owner lease 已由另一个 Go 实例持有")
	}

	runCtx, cancel := context.WithCancelCause(withOwnerLease(ctx, lease))
	stopRenewal := make(chan struct{})
	renewalDone := make(chan struct{})
	var renewalErr error
	var renewalMu sync.Mutex
	var stopRenewalOnce sync.Once
	defer func() {
		if recovered := recover(); recovered != nil {
			resultErr = fmt.Errorf("运行日志 owner lease 回调 panic: %v\n%s", recovered, debug.Stack())
		}
		cancel(nil)
		stopRenewalOnce.Do(func() { close(stopRenewal) })
		<-renewalDone
		releaseCtx, releaseCancel := context.WithTimeout(context.Background(), 5*time.Second)
		releaseErr := releaseOwnerLeaseRecoverably(releaseCtx, store, lease)
		releaseCancel()
		renewalMu.Lock()
		leaseErr := renewalErr
		renewalMu.Unlock()
		if errors.Is(resultErr, context.Canceled) && leaseErr != nil {
			resultErr = nil
		}
		resultErr = errors.Join(resultErr, leaseErr, releaseErr)
	}()
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				err := fmt.Errorf("运行日志 owner lease 续租 goroutine panic: %v\n%s", recovered, debug.Stack())
				renewalMu.Lock()
				renewalErr = err
				renewalMu.Unlock()
				cancel(err)
			}
			close(renewalDone)
		}()
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

	resultErr = run(runCtx)
	return resultErr
}

func releaseOwnerLeaseRecoverably(ctx context.Context, store Store, lease OwnerLease) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("释放运行日志 owner lease panic: %v\n%s", recovered, debug.Stack())
		}
	}()
	return store.ReleaseOwnerLease(ctx, lease)
}
