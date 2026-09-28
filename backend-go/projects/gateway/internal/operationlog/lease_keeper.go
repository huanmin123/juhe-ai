package operationlog

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/safego"
)

// LeaseKeeper holds the single-row F4 persistence owner lease
// (f4-operation-log-persistence) on behalf of ONE process. Every F4 writer of
// this process — the in-process producer (management-plane writes, compose.go)
// and the resident retention owner (retention.go) — shares the same lease so
// the writers of one process can never fence each other out: the lease row has
// a single owner_id/fence_token, and any second acquisition would permanently
// fence the first holder (BUG: producer self-destructed via a zero-TTL renew
// and the sidecar then took the row over).
//
// Lifecycle (2026-09-28 revision, mirror of the auditlog.LeaseKeeper
// BUG-0196 semantics): StartLeaseKeeper acquires once; renewal runs on a
// ticker at ttl/3 (minimum 1s, the retired listener-era cadence). The renewal
// UPDATE is guarded by owner_id + fence_token + lease_until > db-clock, so a
// late retry can never renew somebody else's lease; the store reports logical
// rejection as renewed=false with nil error, therefore any renewal error is
// transport-class (timeout, network, 5xx) and does not prove the lease is
// gone. Transport-class failures keep retrying at the normal cadence inside
// the ownerRenewGraceFactor×TTL window since the first consecutive failure.
// Only a 0-row rejection (expired or taken over) or an exceeded grace window
// is terminal — the keeper records ErrOwnerLeaseLost (or the grace error),
// closes Lost, and stops renewing. reacquire revives a terminal keeper for
// the next supervisor restart of RunOwner with a fresh fence token; replaying
// the stored error without touching the database (the pre-revision behavior
// that left the gateway owner-less until a full process restart) is gone.
type LeaseKeeper struct {
	store Store
	owner string
	ttl   time.Duration
	log   *slog.Logger

	mu      sync.RWMutex
	lease   OwnerLease
	lostErr error
	lostCh  chan struct{}
	// loopDone is closed when the current renewal-loop generation exits; it
	// lets reacquire wait for the old loop (and its unsynchronized fatal
	// bookkeeping) to fully stop before rebuilding one-shot state. Guarded by
	// mu.
	loopDone chan struct{}

	fatalOnce sync.Once
	stopCh    chan struct{}
	stopOnce  sync.Once
	closeOnce sync.Once
	// closed 记录 closeOnce 是否已耗尽：once 用尽后的再次 Close 会被静默
	// 跳过，掩盖 reacquire 重建租约后租约无法释放的现场。仅用于 Close 入口
	// 的跳过告警，不改变 Close 行为。
	closed atomic.Bool
}

// StartLeaseKeeper acquires the F4 persistence lease. ok=false means the row
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
	keeper.startRenewLoop()
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

// ownerRenewGraceFactor bounds how long renewLoop tolerates transport-class
// renewal failures: the guarded renewal UPDATE makes an in-window retry safe,
// so one network jitter must not permanently kill the owner (production
// 2026-09: a single 5s renewal timeout used to go terminal and left the
// gateway permanently owner-less until a full process restart). After
// 2×TTL without a successful renewal the lease can no longer be assumed
// unexpired, so the keeper gives up ownership and lets the supervisor
// restart re-acquire fresh.
const ownerRenewGraceFactor = 2

// startRenewLoop registers a fresh loop generation and spawns it. The done
// channel is published under mu so reacquire can wait for this exact
// generation to exit.
func (k *LeaseKeeper) startRenewLoop() {
	done := make(chan struct{})
	k.mu.Lock()
	k.loopDone = done
	k.mu.Unlock()
	go k.renewLoop(done)
}

func (k *LeaseKeeper) renewLoop(done chan struct{}) {
	// close(done) is registered first so it runs last: the generation is only
	// reported as exited after fatal bookkeeping (and any safego recovery)
	// has completed, which is the ordering reacquire relies on.
	defer close(done)
	defer safego.Recover("operationlog.lease_keeper.renew_loop")
	interval := k.ttl / 3
	if interval < time.Second {
		interval = time.Second
	}
	grace := k.renewGraceWindow()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var firstFailure time.Time
	var consecutiveFailures int
	for {
		select {
		case <-k.stopCh:
			return
		case <-ticker.C:
			renewCtx, cancel := context.WithTimeout(context.Background(), minDuration(5*time.Second, interval))
			renewed, renewErr := k.store.RenewOwnerLease(renewCtx, k.Lease(), k.ttl)
			cancel()
			if renewErr != nil {
				// Transport-class failure: the lease stays plausibly unexpired,
				// so retry at the normal cadence. Each attempt keeps its own
				// bounded context; a lease that really did expire mid-window
				// surfaces as renewed=false (0 rows) on a later attempt.
				if firstFailure.IsZero() {
					firstFailure = time.Now()
				}
				consecutiveFailures++
				if time.Since(firstFailure) > grace {
					k.fatal(fmt.Errorf("续租 F4 operation-log owner lease 失败：连续 %d 次未成功，超过 %s 放宽窗口，放弃所有权: %w", consecutiveFailures, grace, renewErr))
					return
				}
				k.log.Warn("续租 F4 operation-log owner lease 失败；租约仍可能有效，按周期重试", "error", renewErr, "consecutiveFailures", consecutiveFailures, "graceWindow", grace.String())
				continue
			}
			recovered := consecutiveFailures
			firstFailure = time.Time{}
			consecutiveFailures = 0
			if !renewed {
				k.fatal(ErrOwnerLeaseLost)
				return
			}
			if recovered > 0 {
				k.log.Info("续租 F4 operation-log owner lease 已从连续失败中恢复",
					"event", "operation_log_lease_renewal_recovered",
					"consecutiveFailures", recovered)
			}
		}
	}
}

// renewGraceWindow is the relaxed retry window for transport-class renewal
// failures: 2×TTL since the first consecutive failure (see
// ownerRenewGraceFactor).
func (k *LeaseKeeper) renewGraceWindow() time.Duration {
	return ownerRenewGraceFactor * k.ttl
}

func (k *LeaseKeeper) fatal(err error) {
	k.fatalOnce.Do(func() {
		if k.log != nil {
			lease := k.Lease()
			k.log.Error("F4 operation-log owner lease 丢失，放弃所有权", "error", err, "ownerID", lease.OwnerID, "fenceToken", lease.FenceToken)
		}
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
	if k.closed.Load() {
		lease := k.Lease()
		k.log.Warn("F4 operation-log lease keeper 已关闭，跳过本次释放",
			"event", "operation_log_lease_close_skipped",
			"ownerID", lease.OwnerID, "fenceToken", lease.FenceToken)
		return
	}
	k.closeOnce.Do(func() {
		k.closed.Store(true)
		k.stopOnce.Do(func() { close(k.stopCh) })
		if k.LostError() != nil {
			return
		}
		releaseCtx, cancel := storeContext(context.Background())
		defer cancel()
		if err := k.store.ReleaseOwnerLease(releaseCtx, k.Lease()); err != nil {
			k.log.Warn("F4 owner lease release failed", "error", err)
		}
	})
}

// reacquire revives a terminal keeper for the next supervisor restart of
// RunOwner: the stored loss must not be replayed without touching the
// database (production 2026-09: every restart returned the same stored error
// immediately and the gateway stayed owner-less until a process restart).
// A healthy keeper is returned untouched. For a lost keeper the old stopCh is
// closed and the previous loop generation is awaited (fatal() touches
// fatalOnce without mu, so the wait orders the old loop's writes ahead of the
// rebuild), then a fresh AcquireOwnerLease (new fence token) runs: the
// guarded acquire only succeeds when the row is free or expired. On success
// all one-shot state is rebuilt and a new renewal loop starts; lostCh is
// replaced so observers re-reading Lost() (RunOwner re-reads it every select
// iteration) see the fresh state.
func (k *LeaseKeeper) reacquire(ctx context.Context) error {
	if k.LostError() == nil {
		return nil
	}
	k.stopOnce.Do(func() {
		if k.stopCh != nil {
			close(k.stopCh)
		}
	})
	// Wait for the previous loop generation to fully exit before rebuilding
	// one-shot state: fatal() touches fatalOnce without mu, so this wait is
	// what orders the old loop's writes ahead of the resets below.
	k.mu.RLock()
	done := k.loopDone
	k.mu.RUnlock()
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			return fmt.Errorf("等待旧续租循环退出时上下文取消: %w", ctx.Err())
		}
	}
	acquireCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	lease, ok, err := k.store.AcquireOwnerLease(acquireCtx, k.owner, k.ttl)
	if err != nil {
		return fmt.Errorf("重启后重新获取 F4 operation-log owner lease 失败 (owner %s): %w", k.owner, err)
	}
	if !ok {
		return fmt.Errorf("重启后 F4 operation-log owner lease 仍被其他 owner 持有 (owner %s)", k.owner)
	}
	k.mu.Lock()
	k.lease = lease
	k.lostErr = nil
	k.lostCh = make(chan struct{})
	k.stopCh = make(chan struct{})
	k.stopOnce = sync.Once{}
	k.fatalOnce = sync.Once{}
	k.mu.Unlock()
	k.startRenewLoop()
	k.log.Info("F4 operation-log owner lease 已重新获取，owner 组件恢复", "ownerID", lease.OwnerID, "fenceToken", lease.FenceToken)
	return nil
}

func minDuration(first, second time.Duration) time.Duration {
	if first < second {
		return first
	}
	return second
}
