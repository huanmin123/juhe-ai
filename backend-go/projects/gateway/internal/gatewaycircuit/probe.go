package gatewaycircuit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
)

// Availability probe kinds mirror AvailabilityProbeKind.
const (
	ProbeKindCodexSourceAvoidance = "codex_source_avoidance"
	ProbeKindAccountHealthCheck   = "account_health_check"
)

// Availability probe outcomes mirror AvailabilityProbeOutcome.
const (
	ProbeOutcomeSuccess         = "success"
	ProbeOutcomeHealthFailure   = "health_failure"
	ProbeOutcomeUnknown         = "unknown"
	ProbeOutcomeProbeTaskFailure = "probe_task_failure"
	ProbeOutcomeCanceled        = "canceled"
	ProbeOutcomeStale           = "stale"
)

// Probe ownership defaults (availability-probe-coordinator.ts): the account
// health diagnostic deadline is 65 seconds by default; the ownership lease
// stays longer than the full diagnostic ladder.
const (
	defaultProbeLeaseMs     = int64(90_000)
	defaultProbeRetentionMs = int64(5 * 60_000)
)

// ProbeSourceFence mirrors AvailabilityProbeSourceFence.
type ProbeSourceFence struct {
	StateKey         string
	AccountID        string
	SourceGeneration int64
	SourceFenceID    string
}

// ProbeState mirrors the AvailabilityProbeState stored per runtime key.
type ProbeState struct {
	RuntimeKey             string   `json:"runtimeKey"`
	Generation             int64    `json:"generation"`
	NextProbeAtMs          int64    `json:"nextProbeAtMs"`
	AccountRuntimeScope    string   `json:"accountRuntimeScope"`
	ProbeKind              string   `json:"probeKind"`
	ConfigRevision         int64    `json:"configRevision"`
	ProbeRunID             *string  `json:"probeRunId,omitempty"`
	ProbeRunUntilMs        *int64   `json:"probeRunUntilMs,omitempty"`
	DispatchPending        *bool    `json:"dispatchPending,omitempty"`
	DispatchPendingUntilMs *int64   `json:"dispatchPendingUntilMs,omitempty"`
	Outcome                *string  `json:"outcome,omitempty"`
	CompletedAtMs          *int64   `json:"completedAtMs,omitempty"`
	SourceFences           []string `json:"sourceFences,omitempty"`
}

// ReplacedProbeFenceSettlement mirrors ReplacedAvailabilityProbeFenceSettlement.
type ReplacedProbeFenceSettlement struct {
	Generation     int64
	ConfigRevision int64
	Outcome        string
	SourceFences   []ProbeSourceFence
}

// ProbeMergeOptions mirrors the merge() options the coordinator uses.
type ProbeMergeOptions struct {
	PreserveCurrentFields []string
	// UnionArrayFields is [{ field, maxItems }] in Node.
	UnionArrayFields []ProbeUnionArrayField
}

// ProbeUnionArrayField mirrors one unionArrayFields entry.
type ProbeUnionArrayField struct {
	Field    string
	MaxItems int
}

// ProbeStateStore mirrors the consumed surface of
// shared/runtime-probe-state-store.ts. Redis is authoritative whenever the
// runtime state driver is Redis; a memory driver is a deliberate
// single-process fallback. The store implementation lands with its own work
// package; tests inject mocks.
type ProbeStateStore interface {
	Get(ctx context.Context, runtimeKey string) (*ProbeState, error)
	NextGeneration(ctx context.Context, runtimeKey string, retentionMs int64) (int64, error)
	SetIfAbsent(ctx context.Context, state ProbeState, retentionMs int64) (bool, error)
	Merge(ctx context.Context, state ProbeState, retentionMs int64, options ProbeMergeOptions) (*ProbeState, error)
	AcquireGenerationRun(ctx context.Context, runtimeKey string, generation int64, ownerToken string, leaseUntilMs int64, retentionMs int64) (*ProbeState, error)
	CommitGenerationRun(ctx context.Context, next ProbeState, ownerToken string, retentionMs int64) (bool, error)
	ReplaceSettledGeneration(ctx context.Context, next ProbeState, expectedGeneration int64, retentionMs int64) (*ProbeState, error)
}

// ProbeAcquireResult mirrors AvailabilityProbeAcquireResult.
type ProbeAcquireResult struct {
	// Disposition is 'owner' | 'joined'.
	Disposition             string
	RuntimeKey              string
	Generation              int64
	OwnerToken              string
	RetryAtMs               int64
	ReplacedFenceSettlement *ReplacedProbeFenceSettlement
}

// Probe acquire dispositions.
const (
	ProbeDispositionOwner  = "owner"
	ProbeDispositionJoined = "joined"
)

// ProbeAcquireInput mirrors the acquireAvailabilityProbe input.
type ProbeAcquireInput struct {
	AccountRuntimeScope string
	ProbeKind           string
	ConfigRevision      int64
	NowMs               *int64
	LeaseMs             *int64
	RetentionMs         *int64
	SourceFence         *ProbeSourceFence
	ExecutionRole       string // 'source_dispatch' | 'health_probe' | ''
	ForceNewGeneration  bool
}

// ProbeCoordinator mirrors the availability-probe-coordinator module surface
// with an injected state store, clock and id generator.
type ProbeCoordinator struct {
	store    ProbeStateStore
	now      func() int64
	createID func() string

	mu          sync.Mutex
	testStore   ProbeStateStore
}

// NewProbeCoordinator mirrors the module singleton wiring with explicit
// dependencies.
func NewProbeCoordinator(store ProbeStateStore, now func() int64, createID func() string) *ProbeCoordinator {
	if now == nil {
		now = defaultNowMs
	}
	if createID == nil {
		createID = defaultCreateID
	}
	return &ProbeCoordinator{store: store, now: now, createID: createID}
}

// SetStoreForTest mirrors setAvailabilityProbeStateStoreForTest.
func (c *ProbeCoordinator) SetStoreForTest(store ProbeStateStore) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.testStore = store
}

func (c *ProbeCoordinator) currentStore() ProbeStateStore {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.testStore != nil {
		return c.testStore
	}
	return c.store
}

// Acquire mirrors acquireAvailabilityProbe: shared, fenced ownership for
// availability probes.
func (c *ProbeCoordinator) Acquire(ctx context.Context, input ProbeAcquireInput) (ProbeAcquireResult, error) {
	accountRuntimeScope := strings.TrimSpace(input.AccountRuntimeScope)
	if accountRuntimeScope == "" {
		return ProbeAcquireResult{}, errors.New("availability probe requires an account runtime scope")
	}
	configRevision := normalizedRevision(input.ConfigRevision)
	runtimeKey := AvailabilityProbeRuntimeKey(accountRuntimeScope, input.ProbeKind, configRevision)
	nowMs := c.now()
	if input.NowMs != nil {
		nowMs = *input.NowMs
	}
	leaseMs := defaultProbeLeaseMs
	if input.LeaseMs != nil {
		leaseMs = normalizedDuration(*input.LeaseMs)
	}
	retentionMs := defaultProbeRetentionMs
	if input.RetentionMs != nil {
		retentionMs = normalizedDuration(*input.RetentionMs)
	}
	store := c.currentStore()
	current, err := store.Get(ctx, runtimeKey)
	if err != nil {
		return ProbeAcquireResult{}, err
	}
	ownerToken := c.createID()

	if current == nil {
		generation, err := store.NextGeneration(ctx, runtimeKey, retentionMs)
		if err != nil {
			return ProbeAcquireResult{}, err
		}
		state := ProbeState{
			RuntimeKey:          runtimeKey,
			Generation:          generation,
			NextProbeAtMs:       nowMs + leaseMs,
			AccountRuntimeScope: accountRuntimeScope,
			ProbeKind:           input.ProbeKind,
			ConfigRevision:      configRevision,
			ProbeRunID:          strPtr(ownerToken),
			ProbeRunUntilMs:     int64Ptr(nowMs + leaseMs),
		}
		if input.SourceFence != nil {
			state.SourceFences = []string{encodeSourceFence(*input.SourceFence)}
		}
		if ok, err := store.SetIfAbsent(ctx, state, retentionMs); err != nil {
			return ProbeAcquireResult{}, err
		} else if ok {
			return ProbeAcquireResult{Disposition: ProbeDispositionOwner, RuntimeKey: runtimeKey, Generation: generation, OwnerToken: ownerToken}, nil
		}
		return c.joinOrTakeOver(ctx, joinOrTakeOverInput{
			store:       store,
			runtimeKey:  runtimeKey,
			ownerToken:  ownerToken,
			nowMs:       nowMs,
			leaseMs:     leaseMs,
			retentionMs: retentionMs,
			sourceFence: input.SourceFence,
			executionRole: input.ExecutionRole,
			replacement: &probeReplacementIdentity{
				accountRuntimeScope: accountRuntimeScope,
				probeKind:           input.ProbeKind,
				configRevision:      configRevision,
			},
		})
	}
	if current.Outcome != nil && (input.ForceNewGeneration || (input.ExecutionRole == "source_dispatch" && input.SourceFence != nil)) {
		return c.replaceSettledGeneration(ctx, replaceSettledInput{
			store:               store,
			current:             *current,
			runtimeKey:          runtimeKey,
			accountRuntimeScope: accountRuntimeScope,
			probeKind:           input.ProbeKind,
			configRevision:      configRevision,
			ownerToken:          ownerToken,
			nowMs:               nowMs,
			leaseMs:             leaseMs,
			retentionMs:         retentionMs,
			sourceFence:         input.SourceFence,
			executionRole:       input.ExecutionRole,
		})
	}
	return c.joinOrTakeOver(ctx, joinOrTakeOverInput{
		store:       store,
		runtimeKey:  runtimeKey,
		ownerToken:  ownerToken,
		nowMs:       nowMs,
		leaseMs:     leaseMs,
		retentionMs: retentionMs,
		provided:    current,
		sourceFence: input.SourceFence,
		executionRole: input.ExecutionRole,
		replacement: &probeReplacementIdentity{
			accountRuntimeScope: accountRuntimeScope,
			probeKind:           input.ProbeKind,
			configRevision:      configRevision,
		},
	})
}

// SourceFences mirrors availabilityProbeSourceFences.
func (c *ProbeCoordinator) SourceFences(ctx context.Context, runtimeKey string, generation int64) ([]ProbeSourceFence, error) {
	state, err := c.currentStore().Get(ctx, runtimeKey)
	if err != nil {
		return nil, err
	}
	if state == nil || state.Generation != generation {
		return []ProbeSourceFence{}, nil
	}
	var fences []ProbeSourceFence
	for _, value := range state.SourceFences {
		fences = append(fences, decodeSourceFence(value)...)
	}
	return fences, nil
}

// Settle mirrors settleAvailabilityProbe: commitGenerationRun is the fencing
// point, so a stale owner cannot settle a replacement generation.
func (c *ProbeCoordinator) Settle(ctx context.Context, input SettleProbeInput) (bool, error) {
	store := c.currentStore()
	current, err := store.Get(ctx, input.RuntimeKey)
	if err != nil {
		return false, err
	}
	if current == nil || current.Generation != input.Generation || current.ProbeRunID == nil || *current.ProbeRunID != input.OwnerToken {
		return false, nil
	}
	nowMs := c.now()
	if input.NowMs != nil {
		nowMs = *input.NowMs
	}
	retentionMs := defaultProbeRetentionMs
	if input.RetentionMs != nil {
		retentionMs = normalizedDuration(*input.RetentionMs)
	}
	next := *current
	next.NextProbeAtMs = nowMs
	next.ProbeRunID = nil
	next.ProbeRunUntilMs = nil
	next.DispatchPending = nil
	next.DispatchPendingUntilMs = nil
	outcome := input.Outcome
	next.Outcome = &outcome
	next.CompletedAtMs = &nowMs
	return store.CommitGenerationRun(ctx, next, input.OwnerToken, retentionMs)
}

// SettleProbeInput mirrors the settleAvailabilityProbe input.
type SettleProbeInput struct {
	RuntimeKey  string
	Generation  int64
	OwnerToken  string
	Outcome     string
	NowMs       *int64
	RetentionMs *int64
}

// ReleaseForExecution mirrors releaseAvailabilityProbeForExecution: hands an
// acquired generation to the executing component; the owner token fences the
// hand-off.
func (c *ProbeCoordinator) ReleaseForExecution(ctx context.Context, input ReleaseProbeInput) (bool, error) {
	store := c.currentStore()
	current, err := store.Get(ctx, input.RuntimeKey)
	if err != nil {
		return false, err
	}
	if current == nil || current.Generation != input.Generation ||
		current.ProbeRunID == nil || *current.ProbeRunID != input.OwnerToken ||
		current.Outcome != nil {
		return false, nil
	}
	nowMs := c.now()
	if input.NowMs != nil {
		nowMs = *input.NowMs
	}
	leaseMs := defaultProbeLeaseMs
	if input.LeaseMs != nil {
		leaseMs = normalizedDuration(*input.LeaseMs)
	}
	retentionMs := defaultProbeRetentionMs
	if input.RetentionMs != nil {
		retentionMs = normalizedDuration(*input.RetentionMs)
	}
	next := *current
	next.NextProbeAtMs = nowMs
	next.ProbeRunID = nil
	next.ProbeRunUntilMs = nil
	dispatchPending := true
	next.DispatchPending = &dispatchPending
	until := nowMs + leaseMs
	next.DispatchPendingUntilMs = &until
	return store.CommitGenerationRun(ctx, next, input.OwnerToken, retentionMs)
}

// ReleaseProbeInput mirrors the releaseAvailabilityProbeForExecution input.
type ReleaseProbeInput struct {
	RuntimeKey  string
	Generation  int64
	OwnerToken  string
	NowMs       *int64
	LeaseMs     *int64
	RetentionMs *int64
}

// SettleDispatchedBySourceFence mirrors settleDispatchedAvailabilityProbeBySourceFence.
func (c *ProbeCoordinator) SettleDispatchedBySourceFence(ctx context.Context, input SettleDispatchedProbeInput) (bool, error) {
	store := c.currentStore()
	current, err := store.Get(ctx, input.RuntimeKey)
	if err != nil {
		return false, err
	}
	if current == nil || current.Generation != input.Generation || current.Outcome != nil ||
		current.DispatchPending == nil || !*current.DispatchPending ||
		!containsString(current.SourceFences, encodeSourceFence(input.SourceFence)) {
		return false, nil
	}
	ownerToken := c.createID()
	nowMs := c.now()
	if input.NowMs != nil {
		nowMs = *input.NowMs
	}
	leaseMs := defaultProbeLeaseMs
	if input.LeaseMs != nil {
		leaseMs = normalizedDuration(*input.LeaseMs)
	}
	retentionMs := defaultProbeRetentionMs
	if input.RetentionMs != nil {
		retentionMs = normalizedDuration(*input.RetentionMs)
	}
	taken, err := store.AcquireGenerationRun(ctx, input.RuntimeKey, input.Generation, ownerToken, nowMs+leaseMs, retentionMs)
	if err != nil {
		return false, err
	}
	if taken == nil || taken.ProbeRunID == nil || *taken.ProbeRunID != ownerToken {
		return false, nil
	}
	retention := input.RetentionMs
	return c.Settle(ctx, SettleProbeInput{
		RuntimeKey:  input.RuntimeKey,
		Generation:  input.Generation,
		OwnerToken:  ownerToken,
		Outcome:     input.Outcome,
		NowMs:       int64Ptr(nowMs),
		RetentionMs: retention,
	})
}

// SettleDispatchedProbeInput mirrors the settle input with a source fence.
type SettleDispatchedProbeInput struct {
	RuntimeKey  string
	Generation  int64
	SourceFence ProbeSourceFence
	Outcome     string
	NowMs       *int64
	LeaseMs     *int64
	RetentionMs *int64
}

// SourceFenceSettlementDisposition mirrors
// availabilityProbeSourceFenceSettlementDisposition.
func (c *ProbeCoordinator) SourceFenceSettlementDisposition(ctx context.Context, input SourceFenceDispositionInput) (ProbeSourceFenceSettlementDisposition, error) {
	current, err := c.currentStore().Get(ctx, input.RuntimeKey)
	if err != nil {
		return ProbeSourceFenceSettlementDisposition{}, err
	}
	// The coordinator state is deliberately ephemeral. Once its retention has
	// elapsed or a Gateway restarts without the memory fallback, no later
	// source-fenced outcome can safely recreate or settle that generation.
	if current == nil {
		return ProbeSourceFenceSettlementDisposition{Disposition: ProbeSettlementTerminal}, nil
	}
	if current.Generation != input.Generation {
		return ProbeSourceFenceSettlementDisposition{Disposition: ProbeSettlementTerminal}, nil
	}
	if !containsString(current.SourceFences, encodeSourceFence(input.SourceFence)) {
		return ProbeSourceFenceSettlementDisposition{Disposition: ProbeSettlementTerminal}, nil
	}
	if current.Outcome != nil {
		return ProbeSourceFenceSettlementDisposition{Disposition: ProbeSettlementTerminal, CompletedOutcome: *current.Outcome}, nil
	}
	nowMs := c.now()
	if input.NowMs != nil {
		nowMs = *input.NowMs
	}
	if current.ProbeRunUntilMs != nil && *current.ProbeRunUntilMs > nowMs {
		return ProbeSourceFenceSettlementDisposition{Disposition: ProbeSettlementRetry}, nil
	}
	if current.DispatchPending != nil && *current.DispatchPending &&
		current.DispatchPendingUntilMs != nil && *current.DispatchPendingUntilMs > nowMs {
		return ProbeSourceFenceSettlementDisposition{Disposition: ProbeSettlementRetry}, nil
	}
	return ProbeSourceFenceSettlementDisposition{Disposition: ProbeSettlementTerminal}, nil
}

// SourceFenceDispositionInput mirrors the disposition input.
type SourceFenceDispositionInput struct {
	RuntimeKey  string
	Generation  int64
	SourceFence ProbeSourceFence
	NowMs       *int64
}

// Settlement dispositions mirror the Node union.
const (
	ProbeSettlementRetry    = "retry"
	ProbeSettlementTerminal = "terminal"
)

// ProbeSourceFenceSettlementDisposition mirrors the disposition result.
type ProbeSourceFenceSettlementDisposition struct {
	Disposition      string
	CompletedOutcome string
}

// GetState mirrors getAvailabilityProbeState.
func (c *ProbeCoordinator) GetState(ctx context.Context, runtimeKey string) (*ProbeState, error) {
	return c.currentStore().Get(ctx, runtimeKey)
}

// AvailabilityProbeRuntimeKey mirrors availabilityProbeRuntimeKey.
func AvailabilityProbeRuntimeKey(accountRuntimeScope, probeKind string, configRevision int64) string {
	return fmt.Sprintf("availability:%s:%s:r%d", strings.TrimSpace(accountRuntimeScope), probeKind, normalizedRevision(configRevision))
}

type probeReplacementIdentity struct {
	accountRuntimeScope string
	probeKind           string
	configRevision      int64
}

type joinOrTakeOverInput struct {
	store         ProbeStateStore
	runtimeKey    string
	ownerToken    string
	nowMs         int64
	leaseMs       int64
	retentionMs   int64
	provided      *ProbeState
	sourceFence   *ProbeSourceFence
	executionRole string
	replacement   *probeReplacementIdentity
}

func (c *ProbeCoordinator) joinOrTakeOver(ctx context.Context, input joinOrTakeOverInput) (ProbeAcquireResult, error) {
	current := input.provided
	if current == nil {
		loaded, err := input.store.Get(ctx, input.runtimeKey)
		if err != nil {
			return ProbeAcquireResult{}, err
		}
		current = loaded
	}
	if current == nil {
		// The contender won neither the absent write nor a stable read.
		return ProbeAcquireResult{
			Disposition: ProbeDispositionJoined,
			RuntimeKey:  input.runtimeKey,
			Generation:  0,
			RetryAtMs:   input.nowMs + input.leaseMs,
		}, nil
	}
	if input.sourceFence != nil {
		merged, err := input.store.Merge(ctx, *current, input.retentionMs, ProbeMergeOptions{
			PreserveCurrentFields: []string{"probeRunId", "probeRunUntilMs", "outcome", "completedAtMs"},
			UnionArrayFields:      []ProbeUnionArrayField{{Field: "sourceFences", MaxItems: 64}},
		})
		if err != nil {
			return ProbeAcquireResult{}, err
		}
		if merged != nil {
			current = merged
		}
	}
	if current.Outcome != nil {
		if input.sourceFence != nil && input.executionRole == "source_dispatch" && input.replacement != nil {
			// merge() is also the completion/join boundary. If the owner
			// settled before this fence was merged, atomically replace that
			// settled epoch; never let a late fence consume the old success.
			return c.replaceSettledGeneration(ctx, replaceSettledInput{
				store:               input.store,
				current:             *current,
				runtimeKey:          input.runtimeKey,
				accountRuntimeScope: input.replacement.accountRuntimeScope,
				probeKind:           input.replacement.probeKind,
				configRevision:      input.replacement.configRevision,
				ownerToken:          input.ownerToken,
				nowMs:               input.nowMs,
				leaseMs:             input.leaseMs,
				retentionMs:         input.retentionMs,
				sourceFence:         input.sourceFence,
				executionRole:       input.executionRole,
			})
		}
		retryAt := input.nowMs
		if current.CompletedAtMs != nil {
			retryAt = *current.CompletedAtMs
		}
		return ProbeAcquireResult{
			Disposition: ProbeDispositionJoined,
			RuntimeKey:  input.runtimeKey,
			Generation:  current.Generation,
			RetryAtMs:   retryAt,
		}, nil
	}
	if current.DispatchPending != nil && *current.DispatchPending && input.executionRole == "source_dispatch" &&
		current.DispatchPendingUntilMs != nil && *current.DispatchPendingUntilMs > input.nowMs {
		// The source owner already accepted one background dispatch. Source
		// observers must keep joining until that worker acquires the same
		// generation.
		return ProbeAcquireResult{
			Disposition: ProbeDispositionJoined,
			RuntimeKey:  input.runtimeKey,
			Generation:  current.Generation,
			RetryAtMs:   *current.DispatchPendingUntilMs,
		}, nil
	}
	if current.ProbeRunUntilMs != nil && *current.ProbeRunUntilMs > input.nowMs {
		return ProbeAcquireResult{
			Disposition: ProbeDispositionJoined,
			RuntimeKey:  input.runtimeKey,
			Generation:  current.Generation,
			RetryAtMs:   *current.ProbeRunUntilMs,
		}, nil
	}
	taken, err := input.store.AcquireGenerationRun(ctx, input.runtimeKey, current.Generation, input.ownerToken, input.nowMs+input.leaseMs, input.retentionMs)
	if err != nil {
		return ProbeAcquireResult{}, err
	}
	if taken != nil && taken.ProbeRunID != nil && *taken.ProbeRunID == input.ownerToken {
		return ProbeAcquireResult{
			Disposition: ProbeDispositionOwner,
			RuntimeKey:  input.runtimeKey,
			Generation:  current.Generation,
			OwnerToken:  input.ownerToken,
		}, nil
	}
	latest := taken
	if latest == nil {
		loaded, err := input.store.Get(ctx, input.runtimeKey)
		if err != nil {
			return ProbeAcquireResult{}, err
		}
		latest = loaded
	}
	generation := current.Generation
	retryAt := input.nowMs + input.leaseMs
	if latest != nil {
		generation = latest.Generation
		if latest.ProbeRunUntilMs != nil {
			retryAt = *latest.ProbeRunUntilMs
		} else if latest.NextProbeAtMs != 0 {
			retryAt = latest.NextProbeAtMs
		}
	}
	return ProbeAcquireResult{
		Disposition: ProbeDispositionJoined,
		RuntimeKey:  input.runtimeKey,
		Generation:  generation,
		RetryAtMs:   retryAt,
	}, nil
}

type replaceSettledInput struct {
	store               ProbeStateStore
	current             ProbeState
	runtimeKey          string
	accountRuntimeScope string
	probeKind           string
	configRevision      int64
	ownerToken          string
	nowMs               int64
	leaseMs             int64
	retentionMs         int64
	sourceFence         *ProbeSourceFence
	executionRole       string
}

func (c *ProbeCoordinator) replaceSettledGeneration(ctx context.Context, input replaceSettledInput) (ProbeAcquireResult, error) {
	// Replace a settled result in one state-store transaction: deleting first
	// opens a window where a concurrent source can join the stale generation.
	generation, err := input.store.NextGeneration(ctx, input.runtimeKey, input.retentionMs)
	if err != nil {
		return ProbeAcquireResult{}, err
	}
	next := ProbeState{
		RuntimeKey:          input.runtimeKey,
		Generation:          generation,
		NextProbeAtMs:       input.nowMs + input.leaseMs,
		AccountRuntimeScope: input.accountRuntimeScope,
		ProbeKind:           input.probeKind,
		ConfigRevision:      input.configRevision,
		ProbeRunID:          strPtr(input.ownerToken),
		ProbeRunUntilMs:     int64Ptr(input.nowMs + input.leaseMs),
	}
	if input.sourceFence != nil {
		next.SourceFences = []string{encodeSourceFence(*input.sourceFence)}
	}
	replaced, err := input.store.ReplaceSettledGeneration(ctx, next, input.current.Generation, input.retentionMs)
	if err != nil {
		return ProbeAcquireResult{}, err
	}
	if replaced == nil {
		latest, err := input.store.Get(ctx, input.runtimeKey)
		if err != nil {
			return ProbeAcquireResult{}, err
		}
		if latest != nil && latest.Outcome != nil {
			return c.replaceSettledGeneration(ctx, replaceSettledInput{
				store:               input.store,
				current:             *latest,
				runtimeKey:          input.runtimeKey,
				accountRuntimeScope: input.accountRuntimeScope,
				probeKind:           input.probeKind,
				configRevision:      input.configRevision,
				ownerToken:          c.createID(),
				nowMs:               input.nowMs,
				leaseMs:             input.leaseMs,
				retentionMs:         input.retentionMs,
				sourceFence:         input.sourceFence,
				executionRole:       input.executionRole,
			})
		}
		if latest != nil {
			return c.joinOrTakeOver(ctx, joinOrTakeOverInput{
				store:         input.store,
				runtimeKey:    input.runtimeKey,
				ownerToken:    input.ownerToken,
				nowMs:         input.nowMs,
				leaseMs:       input.leaseMs,
				retentionMs:   input.retentionMs,
				provided:      latest,
				sourceFence:   input.sourceFence,
				executionRole: input.executionRole,
				replacement: &probeReplacementIdentity{
					accountRuntimeScope: input.accountRuntimeScope,
					probeKind:           input.probeKind,
					configRevision:      input.configRevision,
				},
			})
		}
		return ProbeAcquireResult{
			Disposition: ProbeDispositionJoined,
			RuntimeKey:  input.runtimeKey,
			Generation:  input.current.Generation,
			RetryAtMs:   input.nowMs + input.leaseMs,
		}, nil
	}
	result := ProbeAcquireResult{
		Disposition: ProbeDispositionOwner,
		RuntimeKey:  input.runtimeKey,
		Generation:  generation,
		OwnerToken:  input.ownerToken,
	}
	if settlement := settlementFromReplacedGeneration(*replaced); settlement != nil {
		result.ReplacedFenceSettlement = settlement
	}
	return result, nil
}

func settlementFromReplacedGeneration(state ProbeState) *ReplacedProbeFenceSettlement {
	if state.Outcome == nil {
		return nil
	}
	var fences []ProbeSourceFence
	for _, value := range state.SourceFences {
		fences = append(fences, decodeSourceFence(value)...)
	}
	return &ReplacedProbeFenceSettlement{
		Generation:     state.Generation,
		ConfigRevision: state.ConfigRevision,
		Outcome:        *state.Outcome,
		SourceFences:   fences,
	}
}

func encodeSourceFence(fence ProbeSourceFence) string {
	encoded, _ := json.Marshal([]any{fence.StateKey, fence.AccountID, fence.SourceGeneration, fence.SourceFenceID})
	return string(encoded)
}

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func decodeSourceFence(value string) []ProbeSourceFence {
	var parsed []any
	if err := json.Unmarshal([]byte(value), &parsed); err != nil || len(parsed) != 4 {
		return nil
	}
	stateKey, _ := parsed[0].(string)
	accountID, _ := parsed[1].(string)
	sourceFenceID, _ := parsed[3].(string)
	sourceGeneration, sourceOK := parsed[2].(float64)
	if stateKey == "" || accountID == "" || !sourceOK || sourceGeneration != float64(int64(sourceGeneration)) || !uuidPattern.MatchString(strings.ToLower(sourceFenceID)) {
		return nil
	}
	return []ProbeSourceFence{{
		StateKey:         stateKey,
		AccountID:        accountID,
		SourceGeneration: int64(sourceGeneration),
		SourceFenceID:    strings.ToLower(sourceFenceID),
	}}
}

func normalizedDuration(value int64) int64 {
	if value < 1 {
		return 1
	}
	return value
}

func normalizedRevision(value int64) int64 {
	if value < 1 {
		return 1
	}
	return value
}
