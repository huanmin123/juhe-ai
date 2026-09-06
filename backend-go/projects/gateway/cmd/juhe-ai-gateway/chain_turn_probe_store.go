package main

// Turn-avoidance availability probe assembly: the composition-root bridge
// that mounts the gatewaycodex.TurnAvoidanceProbeService (port of
// client-profiles/codex-turn-availability-probe.service.ts) onto the
// gatewaycircuit.ProbeCoordinator (G11 availability-probe-coordinator.ts).
//
// The coordinator needs a gatewaycircuit.ProbeStateStore. That store's own
// work package (shared/runtime-probe-state-store.ts) has not migrated its
// Redis driver into gatewaycircuit yet, so this file ports the Node memory
// driver subset the coordinator consumes, byte for byte
// (MemoryRuntimeProbeStateStore: get / setIfAbsent / merge /
// acquireGenerationRun / commitGenerationRun / replaceSettledGeneration /
// nextGeneration — the merge semantics of mergeProbeStateValues restricted to
// the coordinator's fixed options). Cross-instance probe coordination waits
// for the gatewaycircuit Redis store package; until then this matches the
// Node runtimeStateDriver !== 'redis' behaviour exactly.

import (
	"context"
	"sync"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycircuit"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycodex"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

// chainMemoryProbeStateEntry mirrors MemoryProbeEntry.
type chainMemoryProbeStateEntry struct {
	value       gatewaycircuit.ProbeState
	expiresAtMs int64
}

// chainMemoryProbeStateStore implements gatewaycircuit.ProbeStateStore with
// the Node memory-driver semantics. nowMs mirrors the Node Date.now() reads.
type chainMemoryProbeStateStore struct {
	mu          sync.Mutex
	entries     map[string]*chainMemoryProbeStateEntry
	generations map[string]int64
	nowMs       func() int64
}

func newChainMemoryProbeStateStore(nowMs func() int64) *chainMemoryProbeStateStore {
	return &chainMemoryProbeStateStore{
		entries:     map[string]*chainMemoryProbeStateEntry{},
		generations: map[string]int64{},
		nowMs:       nowMs,
	}
}

// freshEntry mirrors the private freshEntry helper (expired entries delete on
// read).
func (s *chainMemoryProbeStateStore) freshEntry(runtimeKey string) *chainMemoryProbeStateEntry {
	entry, ok := s.entries[runtimeKey]
	if !ok {
		return nil
	}
	if entry.expiresAtMs <= s.nowMs() {
		delete(s.entries, runtimeKey)
		return nil
	}
	return entry
}

// Get mirrors get.
func (s *chainMemoryProbeStateStore) Get(_ context.Context, runtimeKey string) (*gatewaycircuit.ProbeState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.freshEntry(runtimeKey)
	if entry == nil {
		return nil, nil
	}
	value := entry.value
	return &value, nil
}

// NextGeneration mirrors nextGeneration: a per-runtime-key monotonic counter
// that never expires (Node memory driver).
func (s *chainMemoryProbeStateStore) NextGeneration(_ context.Context, runtimeKey string, _ int64) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.generations[runtimeKey] + 1
	s.generations[runtimeKey] = next
	return next, nil
}

// SetIfAbsent mirrors setIfAbsent.
func (s *chainMemoryProbeStateStore) SetIfAbsent(_ context.Context, state gatewaycircuit.ProbeState, retentionMs int64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.freshEntry(state.RuntimeKey) != nil {
		return false, nil
	}
	s.entries[state.RuntimeKey] = &chainMemoryProbeStateEntry{value: state, expiresAtMs: s.nowMs() + chainNormalizedTtlMs(retentionMs)}
	return true, nil
}

// Merge mirrors merge + mergeProbeStateValues for the coordinator's fixed
// options: incoming spreads over current, generation keeps the stored value,
// preserveCurrentFields keep the stored non-nil fields and the sourceFences
// array unions through unionStringArrays (maxItems 64).
func (s *chainMemoryProbeStateStore) Merge(_ context.Context, state gatewaycircuit.ProbeState, retentionMs int64, options gatewaycircuit.ProbeMergeOptions) (*gatewaycircuit.ProbeState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.freshEntry(state.RuntimeKey)
	var current *gatewaycircuit.ProbeState
	if entry != nil {
		current = &entry.value
	}
	incoming := state
	merged := chainMergeProbeStateValues(current, &incoming, options)
	s.entries[state.RuntimeKey] = &chainMemoryProbeStateEntry{value: merged, expiresAtMs: s.nowMs() + chainNormalizedTtlMs(retentionMs)}
	return &merged, nil
}

// AcquireGenerationRun mirrors acquireGenerationRun for the fields the Go
// ProbeState carries (the Node half-open lease fields do not exist on the Go
// coordinator path).
func (s *chainMemoryProbeStateStore) AcquireGenerationRun(_ context.Context, runtimeKey string, generation int64, runID string, runUntilMs int64, retentionMs int64) (*gatewaycircuit.ProbeState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.freshEntry(runtimeKey)
	if entry == nil || entry.value.Generation != generation {
		return nil, nil
	}
	now := s.nowMs()
	current := entry.value
	if current.ProbeRunID != nil && *current.ProbeRunID != runID &&
		current.ProbeRunUntilMs != nil && *current.ProbeRunUntilMs > now {
		return nil, nil
	}
	running := current
	previousNext := current.NextProbeAtMs
	if previousNext < runUntilMs {
		running.NextProbeAtMs = runUntilMs
	} else {
		running.NextProbeAtMs = previousNext
	}
	runIDCopy := runID
	running.ProbeRunID = &runIDCopy
	untilCopy := runUntilMs
	running.ProbeRunUntilMs = &untilCopy
	s.entries[runtimeKey] = &chainMemoryProbeStateEntry{value: running, expiresAtMs: now + chainNormalizedTtlMs(retentionMs)}
	return &running, nil
}

// CommitGenerationRun mirrors commitGenerationRun: the exact run must still
// own the generation, the probe run facts clear and the sourceFences arrays
// union (unionStringArrays, maxItems 64).
func (s *chainMemoryProbeStateStore) CommitGenerationRun(_ context.Context, next gatewaycircuit.ProbeState, ownerToken string, retentionMs int64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.freshEntry(next.RuntimeKey)
	if entry == nil || entry.value.Generation != next.Generation ||
		entry.value.ProbeRunID == nil || *entry.value.ProbeRunID != ownerToken {
		return false, nil
	}
	committed := next
	committed.ProbeRunID = nil
	committed.ProbeRunUntilMs = nil
	committed.SourceFences = chainUnionStringArrays(entry.value.SourceFences, next.SourceFences, 64)
	s.entries[next.RuntimeKey] = &chainMemoryProbeStateEntry{value: committed, expiresAtMs: s.nowMs() + chainNormalizedTtlMs(retentionMs)}
	return true, nil
}

// ReplaceSettledGeneration mirrors replaceSettledGeneration: only a settled,
// run-free generation of the exact epoch replaces, and the exact prior
// snapshot returns for fence settlement.
func (s *chainMemoryProbeStateStore) ReplaceSettledGeneration(_ context.Context, state gatewaycircuit.ProbeState, expectedGeneration int64, retentionMs int64) (*gatewaycircuit.ProbeState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.freshEntry(state.RuntimeKey)
	if entry == nil || entry.value.Generation != expectedGeneration ||
		entry.value.Outcome == nil || entry.value.ProbeRunID != nil {
		return nil, nil
	}
	current := entry.value
	s.entries[state.RuntimeKey] = &chainMemoryProbeStateEntry{value: state, expiresAtMs: s.nowMs() + chainNormalizedTtlMs(retentionMs)}
	return &current, nil
}

// chainMergeProbeStateValues mirrors mergeProbeStateValues for the
// coordinator's fixed option set
// (preserve: probeRunId / probeRunUntilMs / outcome / completedAtMs;
// union: sourceFences maxItems 64). The base spread is the incoming state;
// generation and the preserved fields come from the stored snapshot.
func chainMergeProbeStateValues(current, incoming *gatewaycircuit.ProbeState, options gatewaycircuit.ProbeMergeOptions) gatewaycircuit.ProbeState {
	merged := gatewaycircuit.ProbeState{}
	if incoming != nil {
		merged = *incoming
	}
	if current != nil {
		// Node: merged.generation = current.generation when present.
		if current.Generation != 0 {
			merged.Generation = current.Generation
		}
		for _, field := range options.PreserveCurrentFields {
			switch field {
			case "probeRunId":
				if current.ProbeRunID != nil {
					value := *current.ProbeRunID
					merged.ProbeRunID = &value
				}
			case "probeRunUntilMs":
				if current.ProbeRunUntilMs != nil {
					value := *current.ProbeRunUntilMs
					merged.ProbeRunUntilMs = &value
				}
			case "outcome":
				if current.Outcome != nil {
					value := *current.Outcome
					merged.Outcome = &value
				}
			case "completedAtMs":
				if current.CompletedAtMs != nil {
					value := *current.CompletedAtMs
					merged.CompletedAtMs = &value
				}
			}
		}
		for _, union := range options.UnionArrayFields {
			if union.Field == "sourceFences" {
				merged.SourceFences = chainUnionStringArrays(current.SourceFences, incomingSourceFences(incoming), union.MaxItems)
			}
		}
	}
	return merged
}

func incomingSourceFences(state *gatewaycircuit.ProbeState) []string {
	if state == nil {
		return nil
	}
	return state.SourceFences
}

// chainUnionStringArrays mirrors unionStringArrays: first-array priority,
// trimmed dedupe, bounded by maxItems.
func chainUnionStringArrays(first, second []string, maxItems int) []string {
	limit := maxItems
	if limit < 1 {
		limit = 1
	}
	output := []string{}
	seen := map[string]struct{}{}
	for _, values := range [][]string{first, second} {
		for _, item := range values {
			value := trimProbeFenceText(item)
			if value == "" {
				continue
			}
			if _, ok := seen[value]; ok {
				continue
			}
			seen[value] = struct{}{}
			output = append(output, value)
			if len(output) >= limit {
				return output
			}
		}
	}
	return output
}

func trimProbeFenceText(value string) string {
	start, end := 0, len(value)
	for start < end && (value[start] == ' ' || value[start] == '\t') {
		start++
	}
	for end > start && (value[end-1] == ' ' || value[end-1] == '\t') {
		end--
	}
	return value[start:end]
}

// chainNormalizedTtlMs mirrors normalizedTtlMs: at least 1ms.
func chainNormalizedTtlMs(ttlMs int64) int64 {
	if ttlMs < 1 {
		return 1
	}
	return ttlMs
}

// newChainTurnAvoidanceProbeService assembles the
// gatewaycodex.TurnAvoidanceProbeService: the memory probe-state store under
// a fresh gatewaycircuit.ProbeCoordinator, the shared turn-retry state
// service, and the health-check dispatch bridge as the default dispatch
// (Node dispatchAccountHealthCheckWithOutcome through the module seam).
func newChainTurnAvoidanceProbeService(turnRetry *gatewaycodex.TurnRetryService, clock gatewaypreauth.Clock, health *chainRequestFailureHealthDispatcher) *gatewaycodex.TurnAvoidanceProbeService {
	nowMs := func() int64 { return gatewaycodex.NowMs(clock) }
	coordinator := gatewaycircuit.NewProbeCoordinator(newChainMemoryProbeStateStore(nowMs), nowMs, gatewaycodex.RandomUUID)
	return &gatewaycodex.TurnAvoidanceProbeService{
		Coordinator: coordinator,
		TurnRetry:   turnRetry,
		DefaultDispatch: func(accountID, reason, traceID string, sourceFence *gatewaycodex.SourceProbeFence) gatewaycodex.HealthCheckDispatchOutcome {
			return health.dispatchWithOutcome(accountID, reason, traceID, sourceFence)
		},
	}
}
