package keymodelruntime

import (
	"context"
	"crypto/rand"
	"fmt"
	"sync"
	"time"
)

const RecoveryBatchLimit = 100000

// Go does not inherit Node's event-loop throughput cap. The value remains a
// defensive upper bound for a malformed due scan, while Redis leases provide
// the actual cross-instance exclusion.
const RecoveryGlobalLimit = 100000

type RecoveryInput struct {
	AccountID        string
	DispatchRevision int64
}

type InputLoader interface {
	Load(context.Context, string) ([]RecoveryInput, error)
}
type ProbeExecutor func(context.Context, State, RecoveryInput) Outcome

type RecoveryStore interface {
	ServerNow(context.Context) (time.Time, error)
	ListDue(context.Context, time.Time, int) ([]State, error)
	AcquireRecovery(context.Context, State, string, bool, bool) (State, MutationStatus, error)
	RenewRecovery(context.Context, State, string) (bool, error)
	CommitRecovery(context.Context, State, State, string) (MutationStatus, error)
}

type Runner struct {
	store   RecoveryStore
	loader  InputLoader
	probe   ProbeExecutor
	mu      sync.Mutex
	running map[string]struct{}
}

func NewRunner(store RecoveryStore, loader InputLoader, probe ProbeExecutor) (*Runner, error) {
	if store == nil || loader == nil {
		return nil, fmt.Errorf("key-model recovery dependencies are required")
	}
	if probe == nil {
		probe = func(context.Context, State, RecoveryInput) Outcome { return OutcomeUnknown }
	}
	return &Runner{store: store, loader: loader, probe: probe, running: make(map[string]struct{})}, nil
}

func (r *Runner) Run(ctx context.Context) error {
	if r == nil {
		return fmt.Errorf("key-model recovery runner is required")
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if err := r.RunCycle(ctx); err != nil && ctx.Err() == nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (r *Runner) RunCycle(ctx context.Context) error {
	now, err := r.store.ServerNow(ctx)
	if err != nil {
		return fmt.Errorf("read key-model Redis time: %w", err)
	}
	due, err := r.store.ListDue(ctx, now, RecoveryBatchLimit)
	if err != nil {
		return fmt.Errorf("list key-model due state: %w", err)
	}
	continuation := false
	sources := map[string]bool{}
	for _, state := range due {
		if state.Phase == PhaseRecovering {
			continuation = true
			sources[state.CredentialSourceAccountID] = true
		}
	}
	for _, candidate := range due {
		r.mu.Lock()
		if len(r.running) >= RecoveryGlobalLimit {
			r.mu.Unlock()
			break
		}
		if _, exists := r.running[candidate.CapabilityHash]; exists {
			r.mu.Unlock()
			continue
		}
		leaseID := newLeaseID()
		r.running[candidate.CapabilityHash] = struct{}{}
		r.mu.Unlock()
		go r.runCandidate(ctx, candidate, leaseID, continuation, sources[candidate.CredentialSourceAccountID])
	}
	return nil
}

func (r *Runner) runCandidate(parent context.Context, candidate State, leaseID string, continuation, sourceContinuation bool) {
	defer func() { r.mu.Lock(); delete(r.running, candidate.CapabilityHash); r.mu.Unlock() }()
	state, status, err := r.store.AcquireRecovery(parent, candidate, leaseID, continuation, sourceContinuation)
	if err != nil || status != StatusApplied {
		return
	}
	inputs, err := r.loader.Load(parent, state.CredentialSourceAccountID)
	if err != nil {
		r.settleUnknown(parent, state, leaseID)
		return
	}
	var input *RecoveryInput
	for i := range inputs {
		if inputs[i].AccountID == state.CredentialSourceAccountID && inputs[i].DispatchRevision == state.DispatchRevision {
			input = &inputs[i]
			break
		}
	}
	if input == nil {
		r.settleUnknown(parent, state, leaseID)
		return
	}
	probeCtx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	lost := make(chan struct{}, 1)
	done := make(chan struct{})
	go r.renewLease(probeCtx, state, leaseID, cancel, lost, done)
	outcome := r.probe(probeCtx, state, *input)
	close(done)
	select {
	case <-lost:
		return
	default:
	}
	observed, err := r.store.ServerNow(context.WithoutCancel(parent))
	if err != nil {
		return
	}
	status, next := SettleRecovery(state, state.Generation, state.DispatchRevision, leaseID, outcome, observed)
	if status != StatusApplied {
		return
	}
	_, _ = r.store.CommitRecovery(context.WithoutCancel(parent), state, next, leaseID)
}

func (r *Runner) settleUnknown(ctx context.Context, state State, leaseID string) {
	observed, err := r.store.ServerNow(context.WithoutCancel(ctx))
	if err != nil {
		return
	}
	status, next := SettleRecovery(state, state.Generation, state.DispatchRevision, leaseID, OutcomeUnknown, observed)
	if status == StatusApplied {
		_, _ = r.store.CommitRecovery(context.WithoutCancel(ctx), state, next, leaseID)
	}
}

func (r *Runner) renewLease(ctx context.Context, state State, leaseID string, cancel context.CancelFunc, lost chan<- struct{}, done <-chan struct{}) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			ok, err := r.store.RenewRecovery(ctx, state, leaseID)
			if err != nil || !ok {
				select {
				case lost <- struct{}{}:
				default:
				}
				cancel()
				return
			}
		}
	}
}

func newLeaseID() string {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err == nil {
		return fmt.Sprintf("gkmr-%x", bytes[:])
	}
	return fmt.Sprintf("gkmr-%d", time.Now().UnixNano())
}
