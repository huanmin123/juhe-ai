// Package gatewayhotquality defines deterministic, side-effect-free ordering
// for a supplied runtime-quality snapshot. It does not own sampling, Redis,
// leases, attempt accounting, or the gateway listener.
package gatewayhotquality

import (
	"cmp"
	"fmt"
	"math"
	"slices"
	"strings"
)

type RoutingMode string

const (
	RoutingModeCostFirst  RoutingMode = "cost_first"
	RoutingModeSpeedFirst RoutingMode = "speed_first"
)

type SampleState string

const (
	SampleStateCold    SampleState = "cold"
	SampleStateWarming SampleState = "warming"
	SampleStateKnown   SampleState = "known"
)

type Reliability string

const (
	ReliabilityHealthy   Reliability = "healthy"
	ReliabilityUncertain Reliability = "uncertain"
	ReliabilityUnknown   Reliability = "unknown"
	ReliabilityUnhealthy Reliability = "unhealthy"
)

type Tier struct {
	ModelRank     int
	Fallback      bool
	SuperPriority bool
	LocalPriority int
}

type Snapshot struct {
	SampleState          SampleState
	Reliability          Reliability
	EffectiveReliability float64
	EWMAFirstByteMS      float64
	P95FirstByteMS       float64
}

type Candidate struct {
	AccountID          string
	RuntimeKey         string
	RouteScope         string
	StableBindingOrder int
	Tier               Tier
	LatencyDegraded    bool
	Snapshot           Snapshot
}

type DecisionInput struct {
	Mode       RoutingMode
	RouteScope string
	Candidates []Candidate
}

type Decision struct {
	Ordered             []Candidate
	DroppedDuplicateKey int
}

// Decide validates a same-scope snapshot, removes later duplicates by runtime
// key, then produces a stable order without mutating its input.
func Decide(input DecisionInput) (Decision, error) {
	if strings.TrimSpace(input.RouteScope) == "" {
		return Decision{}, fmt.Errorf("hot quality route scope is required")
	}
	if input.Mode != RoutingModeCostFirst && input.Mode != RoutingModeSpeedFirst {
		return Decision{}, fmt.Errorf("unsupported hot quality routing mode %q", input.Mode)
	}
	ordered := make([]Candidate, 0, len(input.Candidates))
	seen := make(map[string]struct{}, len(input.Candidates))
	dropped := 0
	for _, candidate := range input.Candidates {
		if err := validateCandidate(candidate, input.RouteScope); err != nil {
			return Decision{}, err
		}
		if _, exists := seen[candidate.RuntimeKey]; exists {
			dropped++
			continue
		}
		seen[candidate.RuntimeKey] = struct{}{}
		ordered = append(ordered, candidate)
	}
	slices.SortStableFunc(ordered, func(left, right Candidate) int {
		if input.Mode == RoutingModeSpeedFirst {
			if result := compareBool(left.LatencyDegraded, right.LatencyDegraded); result != 0 {
				return result
			}
		}
		if result := compareTier(left.Tier, right.Tier); result != 0 {
			return result
		}
		if result := cmp.Compare(reliabilityRank(left.Snapshot), reliabilityRank(right.Snapshot)); result != 0 {
			return result
		}
		if result := cmp.Compare(effectiveReliability(left.Snapshot), effectiveReliability(right.Snapshot)); result != 0 {
			return result
		}
		if left.Snapshot.SampleState != SampleStateCold && right.Snapshot.SampleState != SampleStateCold {
			if result := cmp.Compare(left.Snapshot.EWMAFirstByteMS, right.Snapshot.EWMAFirstByteMS); result != 0 {
				return result
			}
			if result := cmp.Compare(left.Snapshot.P95FirstByteMS, right.Snapshot.P95FirstByteMS); result != 0 {
				return result
			}
		}
		if result := cmp.Compare(left.StableBindingOrder, right.StableBindingOrder); result != 0 {
			return result
		}
		if result := strings.Compare(left.AccountID, right.AccountID); result != 0 {
			return result
		}
		return strings.Compare(left.RuntimeKey, right.RuntimeKey)
	})
	return Decision{Ordered: ordered, DroppedDuplicateKey: dropped}, nil
}

func validateCandidate(candidate Candidate, scope string) error {
	if strings.TrimSpace(candidate.AccountID) == "" || strings.TrimSpace(candidate.RuntimeKey) == "" {
		return fmt.Errorf("hot quality candidate identity is required")
	}
	if candidate.RouteScope != scope {
		return fmt.Errorf("hot quality candidate %q is outside route scope", candidate.AccountID)
	}
	if candidate.Tier.ModelRank < 0 || candidate.Tier.LocalPriority < 0 || candidate.StableBindingOrder < 0 {
		return fmt.Errorf("hot quality candidate %q has invalid tier", candidate.AccountID)
	}
	if candidate.Snapshot.SampleState != SampleStateCold && candidate.Snapshot.SampleState != SampleStateWarming && candidate.Snapshot.SampleState != SampleStateKnown {
		return fmt.Errorf("hot quality candidate %q has invalid sample state", candidate.AccountID)
	}
	if candidate.Snapshot.Reliability != ReliabilityHealthy && candidate.Snapshot.Reliability != ReliabilityUncertain && candidate.Snapshot.Reliability != ReliabilityUnknown && candidate.Snapshot.Reliability != ReliabilityUnhealthy {
		return fmt.Errorf("hot quality candidate %q has invalid reliability", candidate.AccountID)
	}
	if candidate.Snapshot.EffectiveReliability < 0 || candidate.Snapshot.EffectiveReliability > 1 || candidate.Snapshot.EWMAFirstByteMS < 0 || candidate.Snapshot.P95FirstByteMS < 0 || math.IsNaN(candidate.Snapshot.EffectiveReliability) || math.IsInf(candidate.Snapshot.EffectiveReliability, 0) || math.IsNaN(candidate.Snapshot.EWMAFirstByteMS) || math.IsInf(candidate.Snapshot.EWMAFirstByteMS, 0) || math.IsNaN(candidate.Snapshot.P95FirstByteMS) || math.IsInf(candidate.Snapshot.P95FirstByteMS, 0) {
		return fmt.Errorf("hot quality candidate %q has invalid metrics", candidate.AccountID)
	}
	return nil
}

func compareTier(left, right Tier) int {
	if result := cmp.Compare(left.ModelRank, right.ModelRank); result != 0 {
		return result
	}
	if result := compareBool(left.Fallback, right.Fallback); result != 0 {
		return result
	}
	if result := compareBool(right.SuperPriority, left.SuperPriority); result != 0 {
		return result
	}
	return cmp.Compare(left.LocalPriority, right.LocalPriority)
}

func compareBool(left, right bool) int {
	if left == right {
		return 0
	}
	if left {
		return 1
	}
	return -1
}

func reliabilityRank(snapshot Snapshot) int {
	if snapshot.SampleState == SampleStateCold {
		return 2
	}
	switch snapshot.Reliability {
	case ReliabilityHealthy:
		return 0
	case ReliabilityUncertain:
		return 1
	case ReliabilityUnknown:
		return 2
	default:
		return 3
	}
}

func effectiveReliability(snapshot Snapshot) float64 {
	if snapshot.SampleState == SampleStateCold {
		return 0.5
	}
	return snapshot.EffectiveReliability
}
