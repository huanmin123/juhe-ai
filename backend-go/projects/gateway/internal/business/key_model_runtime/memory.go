package keymodelruntime

import (
	"errors"
	"sync"
	"time"
)

type ForegroundDecision string

const (
	ForegroundAdmitted ForegroundDecision = "admitted"
	ForegroundBusy     ForegroundDecision = "busy"
	ForegroundBlocked  ForegroundDecision = "blocked"
)

type ForegroundPermit struct {
	CapabilityHash string
	AttemptID      string
	LeaseUntil     time.Time
}

type MemoryStore struct {
	mu      sync.Mutex
	states  map[string]State
	permits map[string]map[string]time.Time
	wakes   map[string]uint64
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{states: make(map[string]State), permits: make(map[string]map[string]time.Time), wakes: make(map[string]uint64)}
}

func (s *MemoryStore) Get(capability Capability) (State, bool, error) {
	hash, err := HashCapability(capability)
	if err != nil {
		return State{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state, ok := s.states[hash]
	return clone(state), ok, nil
}

func (s *MemoryStore) RecordFailure(capability Capability, now time.Time) (MutationStatus, State, error) {
	state, err := Open(capability, now)
	if err != nil {
		return "", State{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if current, ok := s.states[state.CapabilityHash]; ok {
		if current.DispatchRevision > state.DispatchRevision {
			return StatusStale, current, nil
		}
		if current.DispatchRevision == state.DispatchRevision && current.Phase != PhaseClosed {
			current.LastObservedAt, current.LastOutcome = now, OutcomeUpstreamIncomplete
			s.states[state.CapabilityHash] = current
			return StatusIdempotent, clone(current), nil
		}
		state.Generation = current.Generation + 1
	}
	s.states[state.CapabilityHash] = state
	return StatusApplied, clone(state), nil
}

func (s *MemoryStore) AdmitForeground(capability Capability, attemptID string, now time.Time) (ForegroundDecision, ForegroundPermit, uint64, error) {
	hash, err := HashCapability(capability)
	if err != nil {
		return "", ForegroundPermit{}, 0, err
	}
	if attemptID == "" {
		return "", ForegroundPermit{}, 0, errors.New("attempt id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if state, ok := s.states[hash]; ok && state.DispatchRevision == capability.DispatchRevision && state.Phase != PhaseClosed {
		return ForegroundBlocked, ForegroundPermit{}, s.wakes[hash], nil
	}
	permits := s.permits[hash]
	if permits == nil {
		permits = make(map[string]time.Time)
		s.permits[hash] = permits
	}
	for id, until := range permits {
		if !until.After(now) {
			delete(permits, id)
		}
	}
	if existing, ok := permits[attemptID]; ok {
		return ForegroundAdmitted, ForegroundPermit{CapabilityHash: hash, AttemptID: attemptID, LeaseUntil: existing}, s.wakes[hash], nil
	}
	if len(permits) >= ForegroundLimit {
		return ForegroundBusy, ForegroundPermit{}, s.wakes[hash], nil
	}
	until := now.Add(90 * time.Second)
	permits[attemptID] = until
	return ForegroundAdmitted, ForegroundPermit{CapabilityHash: hash, AttemptID: attemptID, LeaseUntil: until}, s.wakes[hash], nil
}

func (s *MemoryStore) ReleaseForeground(permit ForegroundPermit) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	permits := s.permits[permit.CapabilityHash]
	if permits == nil {
		return false
	}
	if _, ok := permits[permit.AttemptID]; !ok {
		return false
	}
	delete(permits, permit.AttemptID)
	s.wakes[permit.CapabilityHash]++
	return true
}

func (s *MemoryStore) RenewForeground(permit ForegroundPermit, now time.Time) (ForegroundPermit, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	permits := s.permits[permit.CapabilityHash]
	if permits == nil {
		return ForegroundPermit{}, false
	}
	if _, ok := permits[permit.AttemptID]; !ok {
		return ForegroundPermit{}, false
	}
	permit.LeaseUntil = now.Add(90 * time.Second)
	permits[permit.AttemptID] = permit.LeaseUntil
	return permit, true
}

func (s *MemoryStore) AcquireRecovery(capability Capability, generation int, leaseID string, now time.Time) (MutationStatus, State, error) {
	hash, err := HashCapability(capability)
	if err != nil {
		return "", State{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state, ok := s.states[hash]
	if !ok {
		return StatusStale, State{}, nil
	}
	status, next := AcquireRecoveryLease(state, generation, capability.DispatchRevision, leaseID, now)
	if status == StatusApplied {
		s.states[hash] = next
	}
	return status, next, nil
}

func (s *MemoryStore) SettleRecovery(capability Capability, generation int, leaseID string, outcome Outcome, now time.Time) (MutationStatus, State, error) {
	hash, err := HashCapability(capability)
	if err != nil {
		return "", State{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state, ok := s.states[hash]
	if !ok {
		return StatusStale, State{}, nil
	}
	status, next := SettleRecovery(state, generation, capability.DispatchRevision, leaseID, outcome, now)
	if status == StatusApplied {
		s.states[hash] = next
	}
	return status, next, nil
}
