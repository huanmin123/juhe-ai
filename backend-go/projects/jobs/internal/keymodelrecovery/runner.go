package keymodelrecovery

import (
	"context"
	"crypto/rand"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/accounthealth"
)

const recoveryBatchLimit int64 = 128

// Store is intentionally limited to key_model Redis mutations. In particular,
// it contains no account-health cursor or account-status operation.
type Store interface {
	ServerNow(context.Context) (time.Time, error)
	ListDue(context.Context, time.Time, int64) ([]State, error)
	Acquire(context.Context, State, string, bool, bool) (State, MutationStatus, error)
	Renew(context.Context, State, string) (bool, error)
	Commit(context.Context, State, State, string) (MutationStatus, error)
}

type InputLoader interface {
	LoadAccount(context.Context, string) ([]accounthealth.Input, error)
}

type ProbeExecutor func(context.Context, State, accounthealth.Input) Outcome

// Runner owns only model-recovery probe leases. It schedules independently of
// J1: probe results are committed to the key_model state and never projected
// into accounts.status.
type Runner struct {
	store  Store
	loader InputLoader
	probe  ProbeExecutor
	logger *slog.Logger

	mu                sync.Mutex
	running           map[string]Running
	lastClosedCleanup time.Time
}

func NewRunner(store Store, loader InputLoader, logger *slog.Logger) *Runner {
	if logger == nil {
		logger = slog.Default()
	}
	return &Runner{store: store, loader: loader, logger: logger, probe: defaultProbe, running: map[string]Running{}}
}

func (r *Runner) Run(ctx context.Context) error {
	if r == nil || r.store == nil || r.loader == nil {
		return fmt.Errorf("model-recovery runner 未初始化")
	}
	ticker := time.NewTicker(ScanInterval)
	defer ticker.Stop()
	for {
		if err := r.RunCycle(ctx); err != nil && ctx.Err() == nil {
			r.logger.Warn("model-recovery scan failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// RunCycle exists for deterministic tests and is bounded: it creates at most
// 32 local workers, while Redis acquire enforces the shared global/source caps.
func (r *Runner) RunCycle(ctx context.Context) error {
	now, err := r.store.ServerNow(ctx)
	if err != nil {
		return fmt.Errorf("读取 model-recovery Redis 时间: %w", err)
	}
	r.mu.Lock()
	cleanupDue := r.lastClosedCleanup.IsZero() || now.Sub(r.lastClosedCleanup) >= 5*time.Minute
	if cleanupDue {
		r.lastClosedCleanup = now
	}
	r.mu.Unlock()
	if cleanupDue {
		if cleaner, ok := r.store.(interface {
			CleanClosed(context.Context, int64) (int64, error)
		}); ok {
			if _, err := cleaner.CleanClosed(ctx, 1000); err != nil {
				return fmt.Errorf("清理 model-recovery CLOSED state: %w", err)
			}
		}
	}
	due, err := r.store.ListDue(ctx, now, recoveryBatchLimit)
	if err != nil {
		return fmt.Errorf("读取 model-recovery due state: %w", err)
	}
	continuationWaiting := false
	continuationSources := map[string]bool{}
	for _, item := range due {
		if item.Phase == Recovering {
			continuationWaiting = true
			continuationSources[item.CredentialSourceAccountID] = true
		}
	}
	r.mu.Lock()
	selected := SelectDue(asDue(due), runningSlice(r.running), now)
	for _, candidate := range selected {
		leaseID := newLeaseID()
		r.running[leaseID] = Running{SourceID: candidate.State.CredentialSourceAccountID, Continuation: candidate.State.Phase == Recovering}
		go r.runCandidate(ctx, candidate.State, leaseID, continuationWaiting, continuationSources[candidate.State.CredentialSourceAccountID])
	}
	r.mu.Unlock()
	return nil
}

func (r *Runner) runCandidate(parent context.Context, candidate State, leaseID string, continuationWaiting bool, sourceContinuationWaiting bool) {
	defer func() {
		r.mu.Lock()
		delete(r.running, leaseID)
		r.mu.Unlock()
	}()
	state, status, err := r.store.Acquire(parent, candidate, leaseID, continuationWaiting, sourceContinuationWaiting)
	if err != nil || status != Applied {
		if err != nil {
			r.logger.Warn("model-recovery acquire failed", "capabilityHash", candidate.CapabilityHash, "error", err)
		}
		return
	}
	probeCtx, cancelProbe := context.WithTimeout(parent, ProbeTimeout)
	defer cancelProbe()
	lostLease := make(chan struct{}, 1)
	doneRenew := make(chan struct{})
	go r.renewLease(probeCtx, state, leaseID, cancelProbe, lostLease, doneRenew)
	outcome := r.executeProbe(probeCtx, state)
	close(doneRenew)
	select {
	case <-lostLease:
		return // A lost owner has no CAS write authority.
	default:
	}
	settleCtx, settleCancel := context.WithTimeout(context.WithoutCancel(parent), 5*time.Second)
	defer settleCancel()
	observedAt, err := r.store.ServerNow(settleCtx)
	if err != nil {
		r.logger.Warn("model-recovery settlement time failed", "capabilityHash", state.CapabilityHash, "error", err)
		return
	}
	next, settlement := Settle(state, RecoveryResult{Generation: state.Generation, DispatchRevision: state.DispatchRevision, LeaseID: leaseID, Outcome: outcome, ObservedAt: observedAt})
	if settlement != Applied {
		return
	}
	if status, err := r.store.Commit(settleCtx, state, next, leaseID); err != nil || status != Applied {
		if err != nil {
			r.logger.Warn("model-recovery commit failed", "capabilityHash", state.CapabilityHash, "error", err)
		}
	}
}

func (r *Runner) renewLease(ctx context.Context, state State, leaseID string, cancel context.CancelFunc, lostLease chan<- struct{}, done <-chan struct{}) {
	ticker := time.NewTicker(ProbeLeaseRenew)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			ok, err := r.store.Renew(ctx, state, leaseID)
			if err != nil || !ok {
				select {
				case lostLease <- struct{}{}:
				default:
				}
				cancel()
				return
			}
		}
	}
}

func (r *Runner) executeProbe(ctx context.Context, state State) Outcome {
	inputs, err := r.loader.LoadAccount(ctx, state.CredentialSourceAccountID)
	if err != nil {
		return Unknown
	}
	for _, input := range inputs {
		if input.AccountID != state.CredentialSourceAccountID || input.DispatchRevision != state.DispatchRevision {
			continue
		}
		return r.probe(ctx, state, input)
	}
	return Unknown
}

func defaultProbe(ctx context.Context, state State, input accounthealth.Input) Outcome {
	result := accounthealth.ProbeExactKeyModel(ctx, input, state.KeyFingerprint, state.FinalUpstreamModel, state.UpstreamEndpointMode, accounthealth.ProbeOptions{Timeout: ProbeTimeout, MaxResponseBytes: 256 * 1024})
	if result.Outcome == accounthealth.OutcomeSuccess {
		return CompleteSuccess
	}
	if result.Outcome == accounthealth.OutcomeTaskFailed {
		return Unknown
	}
	return UpstreamNotComplete
}

func asDue(states []State) []Due {
	items := make([]Due, 0, len(states))
	for _, state := range states {
		items = append(items, Due{State: state, SourceID: state.CredentialSourceAccountID})
	}
	return items
}

func runningSlice(items map[string]Running) []Running {
	result := make([]Running, 0, len(items))
	for _, item := range items {
		result = append(result, item)
	}
	return result
}

func newLeaseID() string {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err == nil {
		return fmt.Sprintf("mcr-%x", bytes[:])
	}
	return fmt.Sprintf("mcr-%d", time.Now().UnixNano())
}
