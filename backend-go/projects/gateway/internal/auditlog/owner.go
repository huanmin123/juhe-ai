package auditlog

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/schedulejitter"
)

// Owner residency (去跨进程战役第四刀): the owner-lease renewal loop and the
// retention cadence previously lived inside RunInputServer's accept loop.
// With the loopback F3 input listener deleted, they move here as the resident
// F3 owner component. Both cadences are unchanged: renewal ticks at
// OwnerLease/3 (minimum 1s, the retired RunInputServer ticker), retention
// keeps one schedulejitter-delayed pass per RetentionInterval.
//
// The keeper follows the operationlog.LeaseKeeper sharing model (one process,
// one owner_id/fence_token shared by the producer and the resident owner) but
// keeps the original F3 renewal failure semantics: a renewal transport error
// is terminal for this process (the retired RunInputServer returned
// "续租 F3 audit owner lease 失败"), not a retry-until-ttl transient.
type LeaseKeeper struct {
	store Store
	owner string
	ttl   time.Duration
	log   *slog.Logger

	mu      sync.RWMutex
	lease   OwnerLease
	lostErr error
	lostCh  chan struct{}

	fatalOnce sync.Once
	stopCh    chan struct{}
	stopOnce  sync.Once
	closeOnce sync.Once
}

// StartLeaseKeeper acquires the F3 audit owner lease. ok=false means the row
// is currently held elsewhere (another active owner process); the caller must
// refuse to start rather than write fenced.
func StartLeaseKeeper(ctx context.Context, store Store, owner string, ttl time.Duration, logger *slog.Logger) (*LeaseKeeper, bool, error) {
	if ttl <= 0 {
		ttl = defaultOwnerLease
	}
	lease, ok, err := store.AcquireOwnerLease(ctx, owner, ttl)
	if err != nil || !ok {
		return nil, false, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	keeper := &LeaseKeeper{store: store, owner: owner, ttl: ttl, log: logger, lease: lease, lostCh: make(chan struct{}), stopCh: make(chan struct{})}
	go keeper.renewLoop()
	return keeper, true, nil
}

// Lease returns the currently held lease (owner + fence token).
func (k *LeaseKeeper) Lease() OwnerLease {
	k.mu.RLock()
	defer k.mu.RUnlock()
	return k.lease
}

// TTL returns the renewal interval base (the configured owner lease TTL).
func (k *LeaseKeeper) TTL() time.Duration {
	return k.ttl
}

// Lost is closed once the lease is lost; read LostError for the cause.
func (k *LeaseKeeper) Lost() <-chan struct{} {
	return k.lostCh
}

// LostError returns the terminal lease-loss cause (nil while held).
func (k *LeaseKeeper) LostError() error {
	k.mu.RLock()
	defer k.mu.RUnlock()
	return k.lostErr
}

func (k *LeaseKeeper) renewLoop() {
	interval := k.ttl / 3
	if interval < time.Second {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-k.stopCh:
			return
		case <-ticker.C:
			renewCtx, cancel := context.WithTimeout(context.Background(), minDuration(5*time.Second, interval))
			renewed, renewErr := k.store.RenewOwnerLease(renewCtx, k.Lease(), k.ttl)
			cancel()
			if renewErr != nil {
				k.fatal(fmt.Errorf("续租 F3 audit owner lease 失败: %w", renewErr))
				return
			}
			if !renewed {
				k.fatal(ErrOwnerLeaseLost)
				return
			}
		}
	}
}

func (k *LeaseKeeper) fatal(err error) {
	k.fatalOnce.Do(func() {
		k.mu.Lock()
		k.lostErr = err
		k.mu.Unlock()
		close(k.lostCh)
	})
}

// Close stops the renewal loop and releases the lease so a successor process
// can take over immediately. Closing an already-lost keeper only stops the
// loop (the row is no longer ours; ReleaseOwnerLease would report
// ErrOwnerLeaseLost).
func (k *LeaseKeeper) Close() {
	k.closeOnce.Do(func() {
		k.stopOnce.Do(func() { close(k.stopCh) })
		if k.LostError() != nil {
			return
		}
		releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := k.store.ReleaseOwnerLease(releaseCtx, k.Lease()); err != nil {
			k.log.Error("释放 F3 audit owner lease 失败", "error", err)
		}
	})
}

// RunOwner is the resident F3 owner body: it hosts the retention cadence and
// surfaces the shared keeper's lease state to the supervisor boundary. It
// returns nil on context cancellation (graceful stop, the retired
// RunInputServer contract) and the component error otherwise.
func RunOwner(ctx context.Context, store Store, keeper *LeaseKeeper, cfg Config, logger *slog.Logger) error {
	if err := cfg.validateRetentionPolicy(); err != nil {
		return fmt.Errorf("F3 audit retention 配置无效: %w", err)
	}
	logger = loggerOrDefault(logger)
	if keeper == nil {
		return fmt.Errorf("F3 audit owner 组件要求共享 lease keeper")
	}
	// The maintenance loop derives its own cancelable context so any terminal
	// return path (lease lost, retention fatal) stops it before the defer
	// waits for its exit — the retired RunInputServer stopMaintenance order.
	maintenanceCtx, stopMaintenance := context.WithCancel(ctx)
	componentFatal := make(chan error, 1)
	maintenanceDone := runRetentionMaintenance(maintenanceCtx, store, keeper.Lease(), cfg, logger, componentFatal)
	defer func() {
		stopMaintenance()
		<-maintenanceDone
	}()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-keeper.Lost():
			if lostErr := keeper.LostError(); lostErr != nil {
				return lostErr
			}
			return ErrOwnerLeaseLost
		case err := <-componentFatal:
			return fmt.Errorf("F3 audit retention 组件异常: %w", err)
		}
	}
}

// runRetentionMaintenance keeps one bounded maintenance pass per configured
// interval. Retention also removes completed hot-search buckets, so both
// cleanup surfaces share the same owner fence and do not need a second task.
func runRetentionMaintenance(ctx context.Context, store Store, lease OwnerLease, cfg Config, logger *slog.Logger, fatal chan<- error) <-chan struct{} {
	logger = loggerOrDefault(logger)
	done := make(chan struct{})
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				reportRetentionFatal(ctx, fatal, fmt.Errorf("F3 audit retention maintenance goroutine panic: %v\n%s", recovered, debug.Stack()))
			}
			close(done)
		}()
		timer := time.NewTimer(schedulejitter.Delay(cfg.RetentionInterval))
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				_, err := store.CleanupRetention(ctx, lease, cfg.RetentionConfigAt(time.Now()))
				if err == nil {
					timer.Reset(schedulejitter.Delay(cfg.RetentionInterval))
					continue
				}
				if ctx.Err() != nil {
					return
				}
				if errors.Is(err, ErrOwnerLeaseLost) {
					reportRetentionFatal(ctx, fatal, err)
					return
				}
				logger.Error("F3 audit retention maintenance failed", "error", err)
				reportRetentionFatal(ctx, fatal, fmt.Errorf("F3 audit retention maintenance failed: %w", err))
				return
			}
		}
	}()
	return done
}

func reportRetentionFatal(ctx context.Context, fatal chan<- error, err error) {
	if fatal == nil {
		return
	}
	select {
	case fatal <- err:
	case <-ctx.Done():
	default:
	}
}

func loggerOrDefault(logger *slog.Logger) *slog.Logger {
	if logger == nil {
		return slog.Default()
	}
	return logger
}

func minDuration(first, second time.Duration) time.Duration {
	if first < second {
		return first
	}
	return second
}
