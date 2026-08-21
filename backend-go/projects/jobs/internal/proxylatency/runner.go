package proxylatency

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

type inputLoader interface {
	LoadDue(context.Context, int) ([]InputDraft, error)
}

type RunnerStatus struct {
	OwnerHeld     bool
	LastCycleAt   time.Time
	LastSuccess   time.Time
	LastError     string
	Inputs        int
	Executed      int
	ProxyFailures int
}

// Runner is the J3a owner loop. The ordering in runCycle is intentional:
// owner lease -> read-only LoadDue -> Store IssueInput -> proxy lease ->
// ExecuteIssuedInput. No upstream request is possible before all fences hold.
type Runner struct {
	cfg    RuntimeConfig
	store  *Store
	reader inputLoader
	logger *slog.Logger
	mu     sync.RWMutex
	status RunnerStatus
	// These hooks keep lifecycle failure paths executable in unit tests while
	// production defaults remain the Store methods.
	renewOwnerLease    func(context.Context, OwnerLease, time.Duration) error
	releaseOwnerLease  func(context.Context, OwnerLease) error
	releaseProxyLease  func(context.Context, ProxyLease) error
	acquireProxyLease  func(context.Context, OwnerLease, string, time.Duration) (ProxyLease, bool, error)
	executeIssuedInput func(context.Context, *Store, OwnerLease, ProxyLease, IssuedInput, ExecutorOptions) (Outcome, bool, error)
}

func NewRunner(cfg RuntimeConfig, store *Store, reader inputLoader, logger *slog.Logger) *Runner {
	if logger == nil {
		logger = slog.Default()
	}
	runner := &Runner{cfg: cfg, store: store, reader: reader, logger: logger, executeIssuedInput: ExecuteIssuedInput}
	if store != nil {
		runner.renewOwnerLease = store.RenewOwnerLease
		runner.releaseOwnerLease = store.ReleaseOwnerLease
		runner.releaseProxyLease = store.ReleaseProxyLease
		runner.acquireProxyLease = store.AcquireProxyLease
	}
	return runner
}

func (r *Runner) Snapshot() (RunnerStatus, bool) {
	if r == nil {
		return RunnerStatus{}, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	status := r.status
	ready := status.OwnerHeld && !status.LastSuccess.IsZero() && status.LastError == ""
	return status, ready
}

func (r *Runner) Status() RunnerStatus { status, _ := r.Snapshot(); return status }
func (r *Runner) Ready() bool          { _, ready := r.Snapshot(); return ready }

func (r *Runner) Run(ctx context.Context) error {
	if r == nil || r.store == nil || r.reader == nil {
		return errors.New("J3a runner 未初始化")
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		lease, acquired, err := r.store.AcquireOwnerLease(ctx, r.cfg.InstanceID, r.cfg.OwnerLease)
		if err != nil {
			r.recordError(err)
			if err := waitRuntime(ctx, r.cfg.Interval); err != nil {
				return err
			}
			continue
		}
		if !acquired {
			if err := waitRuntime(ctx, minRuntime(r.cfg.Interval, r.cfg.OwnerLease/3)); err != nil {
				return err
			}
			continue
		}
		r.setOwnerHeld(true)
		err = r.runOwned(ctx, lease)
		r.setOwnerHeld(false)
		releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		releaseErr := r.releaseOwnerLease(releaseCtx, lease)
		cancel()
		runErr := err
		if releaseErr != nil {
			if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				runErr = releaseErr
			} else {
				runErr = errors.Join(err, releaseErr)
			}
		}
		if releaseErr != nil {
			// Release failures are operationally significant even when the
			// bounded release context itself timed out; never hide them behind
			// the normal context-cancellation filter.
			r.recordError(runErr)
		} else if runErr != nil && !errors.Is(runErr, context.Canceled) && !errors.Is(runErr, context.DeadlineExceeded) {
			r.recordError(runErr)
		}
		if errors.Is(err, context.Canceled) || ctx.Err() != nil {
			return ctx.Err()
		}
	}
}

func (r *Runner) runOwned(ctx context.Context, lease OwnerLease) error {
	ownedCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	renewErr := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(minRuntime(r.cfg.OwnerLease/3, time.Second*30))
		defer ticker.Stop()
		for {
			select {
			case <-ownedCtx.Done():
				return
			case <-ticker.C:
				if err := r.renewOwnerLease(ownedCtx, lease, r.cfg.OwnerLease); err != nil {
					r.recordError(err)
					select {
					case renewErr <- err:
					default:
					}
					cancel()
					return
				}
			}
		}
	}()
	if err := r.runCycle(ownedCtx, lease); err != nil {
		if renewal := readRenewalError(renewErr); renewal != nil {
			return errors.Join(err, renewal)
		}
		return err
	}
	ticker := time.NewTicker(r.cfg.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ownedCtx.Done():
			if renewal := readRenewalError(renewErr); renewal != nil {
				return renewal
			}
			return ownedCtx.Err()
		case <-ticker.C:
			if err := r.runCycle(ownedCtx, lease); err != nil {
				if renewal := readRenewalError(renewErr); renewal != nil {
					return errors.Join(err, renewal)
				}
				return err
			}
		}
	}
}

func readRenewalError(ch <-chan error) error {
	select {
	case err := <-ch:
		return err
	default:
		return nil
	}
}

func (r *Runner) runCycle(ctx context.Context, owner OwnerLease) error {
	attempt := r.now()
	if err := r.store.VerifyOwnerLease(ctx, owner); err != nil {
		r.recordCycle(attempt, 0, 0, 1, err)
		return err
	}
	drafts, err := r.reader.LoadDue(ctx, r.cfg.InputLimit)
	if err != nil {
		r.recordCycle(attempt, 0, 0, 1, err)
		return err
	}
	inputs, executed, failures := 0, 0, 0
	var cycleErr error
	for _, draft := range drafts {
		if err := ctx.Err(); err != nil {
			return errors.Join(cycleErr, err)
		}
		if err := r.store.VerifyOwnerLease(ctx, owner); err != nil {
			cycleErr = errors.Join(cycleErr, err)
			r.recordCycle(attempt, inputs, executed, failures+1, cycleErr)
			return cycleErr
		}
		issued, err := r.store.IssueInput(ctx, draft)
		if err != nil {
			failures++
			cycleErr = errors.Join(cycleErr, err)
			r.logger.Warn("J3a IssueInput failed", "error", err)
			continue
		}
		inputs++
		acquireProxy := r.acquireProxyLease
		if acquireProxy == nil {
			acquireProxy = r.store.AcquireProxyLease
		}
		proxy, acquired, err := acquireProxy(ctx, owner, issued.ProxyID, r.cfg.ProxyLease)
		if err != nil {
			failures++
			cycleErr = errors.Join(cycleErr, err)
			r.logger.Warn("J3a proxy lease failed", "proxyID", issued.ProxyID, "error", err)
			if fatalLeaseError(err) {
				r.recordCycle(attempt, inputs, executed, failures, cycleErr)
				return cycleErr
			}
			continue
		}
		if !acquired {
			failures++
			cycleErr = errors.Join(cycleErr, ErrProxyLeaseHeld)
			continue
		}
		proxyWindow := executionWindowUntil(r.now(), issued.ExpiresAt, proxy.LeaseUntil)
		if proxyWindow <= 0 {
			failures++
			releaseCtx, releaseCancel := context.WithTimeout(context.Background(), 5*time.Second)
			releaseErr := r.releaseProxyLease(releaseCtx, proxy)
			releaseCancel()
			if releaseErr != nil {
				cycleErr = errors.Join(cycleErr, releaseErr)
			}
			continue
		}
		execCtx, execCancel := context.WithTimeout(ctx, proxyWindow)
		execute := r.executeIssuedInput
		if execute == nil {
			execute = ExecuteIssuedInput
		}
		_, committed, err := execute(execCtx, r.store, owner, proxy, issued, ExecutorOptions{CredentialSecret: r.cfg.CredentialSecret, Timeout: r.cfg.ProbeTimeout, Now: r.cfg.Now})
		execCancel()
		releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		releaseErr := r.releaseProxyLease(releaseCtx, proxy)
		cancel()
		if releaseErr != nil {
			cycleErr = errors.Join(cycleErr, releaseErr)
			r.recordError(releaseErr)
		}
		if err != nil {
			cycleErr = errors.Join(cycleErr, err)
			if errors.Is(err, ErrOwnerLeaseLost) || errors.Is(err, ErrProxyLeaseLost) {
				r.recordCycle(attempt, inputs, executed, failures+1, cycleErr)
				return cycleErr
			}
			failures++
			r.logger.Warn("J3a proxy execution failed", "proxyID", issued.ProxyID, "error", err)
			continue
		}
		if committed {
			executed++
		}
	}
	if failures > 0 {
		if cycleErr == nil {
			cycleErr = errors.New("J3a cycle had proxy failures")
		}
		r.recordCycle(attempt, inputs, executed, failures, cycleErr)
	} else if cycleErr != nil {
		r.recordCycle(attempt, inputs, executed, failures, cycleErr)
	} else {
		r.recordCycle(attempt, inputs, executed, failures, nil)
	}
	return nil
}

func fatalLeaseError(err error) bool {
	return errors.Is(err, ErrOwnerLeaseLost) || errors.Is(err, ErrProxyLeaseLost)
}

func (r *Runner) recordCycle(attempt time.Time, inputs, executed, failures int, cycleErr error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.status.LastCycleAt = attempt
	r.status.Inputs = inputs
	r.status.Executed += executed
	r.status.ProxyFailures = failures
	if cycleErr == nil {
		r.status.LastSuccess = r.now()
		r.status.LastError = ""
	} else {
		r.status.LastError = cycleErr.Error()
	}
}

func (r *Runner) setOwnerHeld(value bool) { r.mu.Lock(); r.status.OwnerHeld = value; r.mu.Unlock() }
func (r *Runner) recordError(err error) {
	if err == nil {
		return
	}
	r.mu.Lock()
	r.status.LastError = err.Error()
	r.mu.Unlock()
}
func (r *Runner) now() time.Time {
	if r.cfg.Now != nil {
		return r.cfg.Now().UTC()
	}
	return time.Now().UTC()
}

func (r *Runner) HealthHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodGet || req.URL.Path != "/health" {
			http.NotFound(w, req)
			return
		}
		s, ready := r.Snapshot()
		payload := map[string]any{"ready": ready, "j3aEnabled": r.cfg.Enabled, "ownerHeld": s.OwnerHeld, "lastCycleAt": formatRuntimeTime(s.LastCycleAt), "lastSuccessAt": formatRuntimeTime(s.LastSuccess), "lastError": s.LastError, "inputs": s.Inputs, "executed": s.Executed, "proxyFailures": s.ProxyFailures}
		w.Header().Set("Content-Type", "application/json")
		if !ready {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		_ = json.NewEncoder(w).Encode(payload)
	})
}

func formatRuntimeTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}
func waitRuntime(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
func minRuntime(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

func executionWindow(now, expiresAt time.Time, lease time.Duration) time.Duration {
	return executionWindowUntil(now, expiresAt, now.Add(lease))
}

func executionWindowUntil(now, expiresAt, leaseUntil time.Time) time.Duration {
	remaining := expiresAt.Sub(now)
	leaseRemaining := leaseUntil.Sub(now)
	if remaining <= 0 || leaseRemaining <= 0 {
		return 0
	}
	return minRuntime(leaseRemaining, remaining)
}
