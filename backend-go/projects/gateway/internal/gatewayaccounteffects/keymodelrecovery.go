package gatewayaccounteffects

import (
	"context"
	"sync"
	"time"
)

// Key-model memory recovery runner constants (key-model-memory-recovery.ts).
const (
	KeyModelRecoveryScanIntervalMs  = int64(1_000)
	keyModelRecoveryBatchSize       = 128
	keyModelRecoveryConcurrency     = 32
	keyModelRecoveryContinuationSlots = 8
	keyModelRecoverySourceLimit     = 2
	keyModelRecoveryContinuationSourceReserve = 1
)

// KeyModelRecoveryProbeInput mirrors KeyModelRecoveryProbeInput.
type KeyModelRecoveryProbeInput struct {
	State  KeyModelState
	Target KeyModelRecoveryTarget
	Ctx    context.Context
}

// KeyModelRecoveryProbe mirrors the KeyModelRecoveryProbe function type.
type KeyModelRecoveryProbe func(input KeyModelRecoveryProbeInput) KeyModelOutcome

// KeyModelMemoryRecoveryRunnerOptions mirrors KeyModelMemoryRecoveryRunnerOptions.
type KeyModelMemoryRecoveryRunnerOptions struct {
	Store       InMemoryKeyModelRecoveryStore
	Probe       KeyModelRecoveryProbe
	Now         func() int64
	CreateID    func() string
	Concurrency int
	Logger      Logger
}

// SweepResult mirrors the sweep() return value.
type SweepResult struct {
	DueCount     int
	StartedCount int
	SettledCount int
}

// KeyModelMemoryRecoveryRunner mirrors KeyModelMemoryRecoveryRunner: the
// process-local counterpart of the Go Redis model-recovery runner (jobs
// internal/keymodelrecovery). Both share the same pure state contract; this
// one keeps no cross-process durability.
type KeyModelMemoryRecoveryRunner struct {
	store       InMemoryKeyModelRecoveryStore
	probe       KeyModelRecoveryProbe
	now         func() int64
	createID    func() string
	concurrency int
	logger      Logger

	mu                         sync.Mutex
	running                    map[string]struct{}
	runningSources             map[string]int
	runningContinuationSources map[string]int
	runningOpenCount           int
}

// NewKeyModelMemoryRecoveryRunner builds the runner with the Node defaults.
func NewKeyModelMemoryRecoveryRunner(options KeyModelMemoryRecoveryRunnerOptions) *KeyModelMemoryRecoveryRunner {
	if options.Store == nil {
		options.Store = NewInMemoryKeyModelRuntimeStore(nil)
	}
	if options.Probe == nil {
		options.Probe = func(KeyModelRecoveryProbeInput) KeyModelOutcome { return KeyModelOutcomeUnknown }
	}
	now := options.Now
	if now == nil {
		now = func() int64 { return time.Now().UnixMilli() }
	}
	createID := options.CreateID
	if createID == nil {
		createID = newUUID
	}
	concurrency := keyModelRecoveryConcurrency
	if options.Concurrency != 0 {
		truncated := options.Concurrency
		if truncated > keyModelRecoveryConcurrency {
			truncated = keyModelRecoveryConcurrency
		}
		if truncated < 1 {
			truncated = 1
		}
		concurrency = truncated
	}
	logger := options.Logger
	if logger == nil {
		logger = NopLogger{}
	}
	return &KeyModelMemoryRecoveryRunner{
		store:                      options.Store,
		probe:                      options.Probe,
		now:                        now,
		createID:                   createID,
		concurrency:                concurrency,
		logger:                     logger,
		running:                    map[string]struct{}{},
		runningSources:             map[string]int{},
		runningContinuationSources: map[string]int{},
	}
}

// Sweep mirrors sweep(): list due states, apply the continuation-first
// selection with 32/2 limits and 8/1 continuation reserve, then acquire a
// lease per candidate and settle the probe outcome.
func (r *KeyModelMemoryRecoveryRunner) Sweep(ctx context.Context) SweepResult {
	nowMs := r.now()
	due, err := r.store.ListDue(nowMs, keyModelRecoveryBatchSize)
	if err != nil {
		r.logger.Warn(map[string]any{"event": "gateway_key_model_memory_recovery_failed", "error": err.Error()}, "单机 Key-model 恢复探针执行失败，按 unknown 处理")
		return SweepResult{}
	}
	continuationWaiting := false
	continuationDueSources := map[string]struct{}{}
	for _, state := range due {
		if state.Phase == KeyModelPhaseRecovering {
			continuationWaiting = true
			continuationDueSources[state.CredentialSourceAccountID] = struct{}{}
		}
	}

	type selection struct {
		state KeyModelState
		index int
	}
	var selected []selection
	selectedSources := map[string]int{}
	selectedContinuationSources := map[string]int{}
	selectedOpenCount := 0
	for index := range due {
		select {
		case <-ctx.Done():
			goto selectionDone
		default:
		}
		{
			state := due[index]
			r.mu.Lock()
			_, alreadyRunning := r.running[state.CapabilityHash]
			runningSize := len(r.running)
			runningOpenCount := r.runningOpenCount
			runningContinuationsForSource := r.runningContinuationSources[state.CredentialSourceAccountID]
			r.mu.Unlock()
			if alreadyRunning {
				continue
			}
			if runningSize+len(selected) >= r.concurrency {
				break
			}
			source := state.CredentialSourceAccountID
			activeSourceCount := r.runningSources[source] + selectedSources[source]
			if activeSourceCount >= keyModelRecoverySourceLimit {
				continue
			}
			if state.Phase == KeyModelPhaseOpen && continuationWaiting {
				if runningOpenCount+selectedOpenCount >= r.concurrency-keyModelRecoveryContinuationSlots {
					continue
				}
				_, sourceHasContinuationDue := continuationDueSources[source]
				activeContinuationsForSource := runningContinuationsForSource + selectedContinuationSources[source]
				sourceOpenLimit := keyModelRecoverySourceLimit
				if sourceHasContinuationDue && activeContinuationsForSource == 0 {
					sourceOpenLimit = keyModelRecoverySourceLimit - keyModelRecoveryContinuationSourceReserve
				}
				if activeSourceCount >= sourceOpenLimit {
					continue
				}
			}
			selected = append(selected, selection{state: state, index: index})
			selectedSources[source] = selectedSources[source] + 1
			if state.Phase == KeyModelPhaseRecovering {
				selectedContinuationSources[source] = selectedContinuationSources[source] + 1
			} else {
				selectedOpenCount++
			}
		}
	}
selectionDone:

	startedCount := 0
	settledCount := 0
	var wg sync.WaitGroup
	var countersMu sync.Mutex
	for _, item := range selected {
		state := item.state
		r.mu.Lock()
		r.running[state.CapabilityHash] = struct{}{}
		r.runningSources[state.CredentialSourceAccountID] = r.runningSources[state.CredentialSourceAccountID] + 1
		if state.Phase == KeyModelPhaseRecovering {
			r.runningContinuationSources[state.CredentialSourceAccountID] = r.runningContinuationSources[state.CredentialSourceAccountID] + 1
		} else {
			r.runningOpenCount++
		}
		r.mu.Unlock()

		wg.Add(1)
		go func(state KeyModelState) {
			defer wg.Done()
			defer func() {
				r.mu.Lock()
				delete(r.running, state.CapabilityHash)
				remaining := r.runningSources[state.CredentialSourceAccountID] - 1
				if remaining > 0 {
					r.runningSources[state.CredentialSourceAccountID] = remaining
				} else {
					delete(r.runningSources, state.CredentialSourceAccountID)
				}
				if state.Phase == KeyModelPhaseRecovering {
					remainingContinuations := r.runningContinuationSources[state.CredentialSourceAccountID] - 1
					if remainingContinuations > 0 {
						r.runningContinuationSources[state.CredentialSourceAccountID] = remainingContinuations
					} else {
						delete(r.runningContinuationSources, state.CredentialSourceAccountID)
					}
				} else {
					r.runningOpenCount--
					if r.runningOpenCount < 0 {
						r.runningOpenCount = 0
					}
				}
				r.mu.Unlock()
			}()

			target := r.store.GetRecoveryTarget(state.CapabilityKey)
			if target == nil {
				return
			}
			leaseID := r.createID()
			status, acquired := r.store.AcquireRecoveryLease(MemoryRecoveryLeaseInput{
				Capability:       state.CapabilityKey,
				Generation:       state.Generation,
				DispatchRevision: state.DispatchRevision,
				LeaseID:          leaseID,
				NowMs:            r.now(),
			})
			if status != KeyModelMutationApplied {
				return
			}
			countersMu.Lock()
			startedCount++
			countersMu.Unlock()
			outcome := r.runProbeWithLease(ctx, acquired, *target, leaseID)
			settled, settledState := r.store.SettleRecovery(MemoryRecoverySettleInput{
				Capability:       acquired.CapabilityKey,
				Generation:       acquired.Generation,
				DispatchRevision: acquired.DispatchRevision,
				LeaseID:          leaseID,
				Outcome:          outcome,
				NowMs:            r.now(),
			})
			if settled == KeyModelMutationApplied {
				countersMu.Lock()
				settledCount++
				countersMu.Unlock()
			}
			_ = settledState
		}(state)
	}
	wg.Wait()
	return SweepResult{DueCount: len(due), StartedCount: startedCount, SettledCount: settledCount}
}

// runProbeWithLease mirrors runProbeWithLease: probe deadline (30s →
// unknown), periodic lease renewal (10s), abort on lost lease.
func (r *KeyModelMemoryRecoveryRunner) runProbeWithLease(parent context.Context, state KeyModelState, target KeyModelRecoveryTarget, leaseID string) KeyModelOutcome {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	probeDone := make(chan KeyModelOutcome, 1)
	go func() {
		defer func() {
			// The probe goroutine must never panic the sweep.
			if recovered := recover(); recovered != nil {
				probeDone <- KeyModelOutcomeUnknown
			}
		}()
		probeDone <- r.probe(KeyModelRecoveryProbeInput{State: state, Target: target, Ctx: ctx})
	}()

	renewStop := make(chan struct{})
	renewDone := make(chan struct{})
	go func() {
		defer close(renewDone)
		ticker := time.NewTicker(time.Duration(KeyModelProbeLeaseRenewMs) * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-renewStop:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				ok := r.store.RenewRecoveryLease(MemoryRecoveryRenewInput{
					CapabilityHash:   state.CapabilityHash,
					Generation:       state.Generation,
					DispatchRevision: state.DispatchRevision,
					LeaseID:          leaseID,
					NowMs:            r.now(),
				})
				if !ok {
					cancel()
					return
				}
			}
		}
	}()

	deadline := time.AfterFunc(time.Duration(KeyModelProbeTimeoutMs)*time.Millisecond, func() {
		cancel()
	})
	defer deadline.Stop()

	var outcome KeyModelOutcome
	select {
	case value := <-probeDone:
		outcome = value
	case <-ctx.Done():
		outcome = KeyModelOutcomeUnknown
	}
	close(renewStop)
	<-renewDone
	return outcome
}
