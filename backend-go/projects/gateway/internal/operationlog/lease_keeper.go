package operationlog

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// LeaseKeeper holds the single-row F4 persistence owner lease
// (f4-operation-log-persistence) on behalf of ONE process. The in-process
// producer (management-plane writes, compose.go) and the F4 input server
// (Node-origin dispatches) share the same lease so the two writers of one
// process can never fence each other out: the lease row has a single
// owner_id/fence_token, and any second acquisition would permanently fence
// the first holder (BUG: producer self-destructed via a zero-TTL renew and
// the sidecar then took the row over).
//
// Lifecycle: StartLeaseKeeper acquires once; renewal runs on a ticker at
// ttl/3 (the previous RunInputServer cadence). A transient renewal error
// keeps the lease valid until ttl elapses, so the next tick retries; a
// rejected renewal (expired or taken over) is terminal — the keeper records
// ErrOwnerLeaseLost, closes Lost, and stops renewing. Recovery from a lost
// fence is a process restart (fresh acquisition, fresh fence token); the
// sidecar retries via its supervisor boundary until then and stays
// not-ready, never silently writing with a stale fence.
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
	ticker := time.NewTicker(k.ttl / 3)
	defer ticker.Stop()
	for {
		select {
		case <-k.stopCh:
			return
		case <-ticker.C:
			renewCtx, cancel := storeContext(context.Background())
			renewed, err := k.store.RenewOwnerLease(renewCtx, k.Lease(), k.ttl)
			cancel()
			if err != nil {
				// Transient storage failure: the lease stays valid until ttl
				// elapses, so keep the process alive and retry next tick.
				k.log.Error("F4 owner lease renewal failed; retrying next tick", "error", err)
				continue
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
		releaseCtx, cancel := storeContext(context.Background())
		defer cancel()
		if err := k.store.ReleaseOwnerLease(releaseCtx, k.Lease()); err != nil {
			k.log.Warn("F4 owner lease release failed", "error", err)
		}
	})
}
