package tablemonitor

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"sync"
	"time"
)

type ownerLeaseContextKey struct{}

var ErrOwnerLeaseLost = errors.New("表监控 owner lease 已失效或已移交")

func ownerLeaseFromContext(ctx context.Context) (OwnerLease, error) {
	lease, ok := ctx.Value(ownerLeaseContextKey{}).(OwnerLease)
	if !ok || lease.OwnerID == "" || lease.FenceToken <= 0 {
		return OwnerLease{}, ErrOwnerLeaseLost
	}
	return lease, nil
}

// RunWithOwnerLease makes this process the sole table-monitor writer. The
// lease is stored with the snapshots, so both SQLite and PostgreSQL have the
// same single-owner behavior without a queue, bridge, or Node switch.
func RunWithOwnerLease(ctx context.Context, cfg Config, store *Store, run func(context.Context) error) (resultErr error) {
	lease, acquired, err := store.AcquireOwnerLease(ctx, cfg.InstanceID, cfg.OwnerLease)
	if err != nil {
		return fmt.Errorf("获取表监控 owner lease 失败: %w", err)
	}
	if !acquired {
		return fmt.Errorf("表监控 owner lease 已由另一个 Go 实例持有")
	}
	runCtx, cancel := context.WithCancelCause(context.WithValue(ctx, ownerLeaseContextKey{}, lease))
	stopRenewal := make(chan struct{})
	renewalDone := make(chan struct{})
	var renewalErr error
	var renewalMu sync.Mutex
	var stopRenewalOnce sync.Once
	defer func() {
		if recovered := recover(); recovered != nil {
			resultErr = fmt.Errorf("表监控 owner lease 回调 panic: %v\n%s", recovered, debug.Stack())
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
				err := fmt.Errorf("表监控 owner lease 续租 goroutine panic: %v\n%s", recovered, debug.Stack())
				renewalMu.Lock()
				renewalErr = err
				renewalMu.Unlock()
				cancel(err)
			}
			close(renewalDone)
		}()
		interval := cfg.OwnerLease / 3
		if interval < time.Second {
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
				renewed, renewErr := store.RenewOwnerLease(runCtx, lease, cfg.OwnerLease)
				if renewErr == nil && renewed {
					continue
				}
				if renewErr == nil {
					renewErr = ErrOwnerLeaseLost
				}
				renewalMu.Lock()
				renewalErr = renewErr
				renewalMu.Unlock()
				cancel(renewErr)
				return
			}
		}
	}()

	resultErr = run(runCtx)
	return resultErr
}

func releaseOwnerLeaseRecoverably(ctx context.Context, store *Store, lease OwnerLease) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("释放表监控 owner lease panic: %v\n%s", recovered, debug.Stack())
		}
	}()
	return store.ReleaseOwnerLease(ctx, lease)
}
