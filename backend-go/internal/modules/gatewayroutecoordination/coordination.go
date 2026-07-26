// Package gatewayroutecoordination owns cross-request ordering state for a
// route strategy. It intentionally has no gateway listener or persistence
// owner: callers can choose an implementation that is safe for their owner.
package gatewayroutecoordination

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"juhe-ai/backend-go/internal/modules/gatewayrouting"
)

const MaxBindings = 20

const maxRouteSequence int64 = 1<<63 - 1

var ErrStaleDispatchGeneration = errors.New("route coordination snapshot dispatch generation is stale")

type Scope struct {
	SystemAccountID string
	RouteStrategyID string
}

type Snapshot struct {
	Scope              Scope
	DispatchGeneration int64
	Mode               gatewayrouting.Mode
	Bindings           []gatewayrouting.Binding
}

type Plan struct {
	Scope              Scope
	DispatchGeneration int64
	Revision           string
	Mode               gatewayrouting.Mode
	Ordered            []gatewayrouting.Binding
	StateAdvanced      bool
}

// SharedState is the complete serializable state needed for one route scope.
// A future shared-store adapter must atomically load this value, call Advance,
// and persist Next only when Plan.StateAdvanced is true. A revision mismatch
// or dispatch-generation advance deliberately resets both sequence and
// smooth-weight debt.
type SharedState struct {
	DispatchGeneration int64
	Revision           string
	Sequence           int64
	Weighted           map[string]int
}

type Coordinator interface {
	Plan(context.Context, Snapshot) (Plan, error)
}

// MemoryStore is deliberately process-local. It is useful in unit tests and
// explicit single-process development only; a production multi-instance owner
// must provide a shared Store rather than silently using this implementation.
type MemoryStore struct {
	mu     sync.Mutex
	states map[string]SharedState
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{states: make(map[string]SharedState)}
}

func (s *MemoryStore) Plan(ctx context.Context, snapshot Snapshot) (Plan, error) {
	if ctx == nil {
		return Plan{}, fmt.Errorf("route coordination context is required")
	}
	if err := ctx.Err(); err != nil {
		return Plan{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	scopeKey := snapshot.Scope.SystemAccountID + "\x00" + snapshot.Scope.RouteStrategyID
	plan, next, err := Advance(snapshot, s.states[scopeKey])
	if err != nil {
		return Plan{}, err
	}
	if plan.StateAdvanced {
		s.states[scopeKey] = cloneSharedState(next)
	} else {
		delete(s.states, scopeKey)
	}
	return plan, nil
}

// Advance is the side-effect-free shared-state transition. It is intentionally
// separate from MemoryStore so a future Redis Lua/CAS adapter can use the same
// dispatch-generation fence and smooth weighted behavior without reimplementing
// routing. A positive dispatch generation is supplied by the future durable
// preflight owner. Zero remains valid only for the process-local compatibility
// seam; RedisStore rejects it instead of treating it as a safe distributed
// generation.
func Advance(snapshot Snapshot, current SharedState) (Plan, SharedState, error) {
	revision, err := Revision(snapshot)
	if err != nil {
		return Plan{}, SharedState{}, err
	}
	if snapshot.DispatchGeneration > 0 && current.DispatchGeneration > snapshot.DispatchGeneration {
		return Plan{}, SharedState{}, ErrStaleDispatchGeneration
	}
	next := SharedState{DispatchGeneration: snapshot.DispatchGeneration, Revision: revision}
	if current.DispatchGeneration == snapshot.DispatchGeneration && current.Revision == revision {
		next.Sequence = current.Sequence
		next.Weighted = cloneState(current.Weighted)
	}
	input := gatewayrouting.OrderInput{Mode: snapshot.Mode, Bindings: cloneBindings(snapshot.Bindings)}
	advanced := false
	switch snapshot.Mode {
	case gatewayrouting.ModeRoundRobin:
		input.Sequence = next.Sequence
		if next.Sequence == maxRouteSequence {
			return Plan{}, SharedState{}, fmt.Errorf("route coordination sequence exhausted")
		}
		next.Sequence++
		advanced = true
	case gatewayrouting.ModeWeighted:
		input.WeightedState = cloneState(next.Weighted)
		advanced = true
	}
	ordered, err := gatewayrouting.OrderBindings(input)
	if err != nil {
		return Plan{}, SharedState{}, err
	}
	if snapshot.Mode == gatewayrouting.ModeWeighted {
		next.Weighted = cloneState(ordered.NextWeightedState)
	}
	return Plan{Scope: snapshot.Scope, DispatchGeneration: snapshot.DispatchGeneration, Revision: revision, Mode: snapshot.Mode, Ordered: cloneBindings(ordered.Bindings), StateAdvanced: advanced}, next, nil
}

// Revision is a semantic SHA-256 fingerprint of the complete route snapshot.
// It intentionally includes disabled bindings: toggling availability must not
// reuse an old cursor or smooth-weight debt.
func Revision(snapshot Snapshot) (string, error) {
	if err := validateSnapshot(snapshot); err != nil {
		return "", err
	}
	bindings := cloneBindings(snapshot.Bindings)
	sort.Slice(bindings, func(i, j int) bool {
		return bindingCanonical(bindings[i]) < bindingCanonical(bindings[j])
	})
	var builder strings.Builder
	builder.WriteString("v1\x00")
	builder.WriteString(string(snapshot.Mode))
	builder.WriteString("\x00")
	builder.WriteString(snapshot.Scope.SystemAccountID)
	builder.WriteString("\x00")
	builder.WriteString(snapshot.Scope.RouteStrategyID)
	for _, binding := range bindings {
		builder.WriteString("\x00")
		builder.WriteString(bindingCanonical(binding))
	}
	digest := sha256.Sum256([]byte(builder.String()))
	return hex.EncodeToString(digest[:]), nil
}

func validateSnapshot(snapshot Snapshot) error {
	if !safeIdentity(snapshot.Scope.SystemAccountID) || !safeIdentity(snapshot.Scope.RouteStrategyID) {
		return fmt.Errorf("route coordination scope is required")
	}
	if len(snapshot.Bindings) == 0 || len(snapshot.Bindings) > MaxBindings {
		return fmt.Errorf("route coordination bindings must be between 1 and %d", MaxBindings)
	}
	if snapshot.DispatchGeneration < 0 {
		return fmt.Errorf("route coordination dispatch generation must not be negative")
	}
	switch snapshot.Mode {
	case gatewayrouting.ModeNormal, gatewayrouting.ModeFailover, gatewayrouting.ModeHybridSmart, gatewayrouting.ModeRoundRobin, gatewayrouting.ModeWeighted:
	default:
		return fmt.Errorf("unsupported gateway route mode %q", snapshot.Mode)
	}
	seen := make(map[string]struct{}, len(snapshot.Bindings))
	eligible := 0
	for _, binding := range snapshot.Bindings {
		if !safeIdentity(binding.ID) || !safeIdentity(binding.GroupID) {
			return fmt.Errorf("route coordination binding identity is required")
		}
		if _, exists := seen[binding.ID]; exists {
			return fmt.Errorf("route coordination has duplicate binding %q", binding.ID)
		}
		seen[binding.ID] = struct{}{}
		if binding.Weight < 1 || binding.Weight > 100 {
			return fmt.Errorf("route coordination binding %q has invalid weight", binding.ID)
		}
		if binding.Active && binding.GroupEnabled {
			eligible++
		}
	}
	if eligible == 0 {
		return fmt.Errorf("route coordination has no eligible bindings")
	}
	return nil
}

func safeIdentity(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && len(value) <= 256 && !strings.ContainsAny(value, "\r\n\x00")
}

func bindingCanonical(binding gatewayrouting.Binding) string {
	return fmt.Sprintf("%s\x00%s\x00%+d\x00%+d\x00%t\x00%t", binding.ID, binding.GroupID, binding.Priority, binding.Weight, binding.Active, binding.GroupEnabled)
}

func cloneBindings(input []gatewayrouting.Binding) []gatewayrouting.Binding {
	return append([]gatewayrouting.Binding(nil), input...)
}

func cloneState(input map[string]int) map[string]int {
	if len(input) == 0 {
		return nil
	}
	result := make(map[string]int, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}

func cloneSharedState(input SharedState) SharedState {
	return SharedState{DispatchGeneration: input.DispatchGeneration, Revision: input.Revision, Sequence: input.Sequence, Weighted: cloneState(input.Weighted)}
}
