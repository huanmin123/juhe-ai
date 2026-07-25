// Package gatewayroutecoordination owns cross-request ordering state for a
// route strategy. It intentionally has no gateway listener or persistence
// owner: callers can choose an implementation that is safe for their owner.
package gatewayroutecoordination

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"

	"juhe-ai/backend-go/internal/modules/gatewayrouting"
)

const MaxBindings = 20

type Scope struct {
	SystemAccountID string
	RouteStrategyID string
}

type Snapshot struct {
	Scope    Scope
	Mode     gatewayrouting.Mode
	Bindings []gatewayrouting.Binding
}

type Plan struct {
	Scope         Scope
	Revision      string
	Mode          gatewayrouting.Mode
	Ordered       []gatewayrouting.Binding
	StateAdvanced bool
}

type Coordinator interface {
	Plan(context.Context, Snapshot) (Plan, error)
}

// MemoryStore is deliberately process-local. It is useful in unit tests and
// explicit single-process development only; a production multi-instance owner
// must provide a shared Store rather than silently using this implementation.
type MemoryStore struct {
	mu     sync.Mutex
	states map[string]state
}

type state struct {
	sequence int64
	weighted map[string]int
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{states: make(map[string]state)}
}

func (s *MemoryStore) Plan(ctx context.Context, snapshot Snapshot) (Plan, error) {
	if err := ctx.Err(); err != nil {
		return Plan{}, err
	}
	revision, err := Revision(snapshot)
	if err != nil {
		return Plan{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	scopeKey := snapshot.Scope.SystemAccountID + "\x00" + snapshot.Scope.RouteStrategyID + "\x00"
	key := scopeKey + revision
	current := s.states[key]
	input := gatewayrouting.OrderInput{Mode: snapshot.Mode, Bindings: cloneBindings(snapshot.Bindings)}
	advanced := false
	switch snapshot.Mode {
	case gatewayrouting.ModeRoundRobin:
		input.Sequence = current.sequence
		advanced = true
	case gatewayrouting.ModeWeighted:
		input.WeightedState = cloneState(current.weighted)
		advanced = true
	}
	ordered, err := gatewayrouting.OrderBindings(input)
	if err != nil {
		return Plan{}, err
	}
	if advanced {
		for oldKey := range s.states {
			if strings.HasPrefix(oldKey, scopeKey) && oldKey != key {
				delete(s.states, oldKey)
			}
		}
		if snapshot.Mode == gatewayrouting.ModeRoundRobin {
			current.sequence++
		} else {
			current.weighted = cloneState(ordered.NextWeightedState)
		}
		s.states[key] = current
	}
	return Plan{Scope: snapshot.Scope, Revision: revision, Mode: snapshot.Mode, Ordered: cloneBindings(ordered.Bindings), StateAdvanced: advanced}, nil
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
