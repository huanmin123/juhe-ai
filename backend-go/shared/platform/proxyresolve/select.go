package proxyresolve

import "strings"

// Candidate is an assignable pool member joined with its profile.
type Candidate struct {
	Member  Member
	Profile Profile
}

// SelectMember chooses a member by the pool strategy. It filters disabled
// members/profiles and dead health; degraded/unknown remain assignable.
//
// round_robin: minimum current lease count, tie by priority then profile ID.
// weighted: minimum leases/weight (integer cross multiplication), tie by
// priority then profile ID. This is allocation by account count, not a
// per-request cursor (proxy design §6.3/§6.4).
func SelectMember(
	strategy string,
	candidates []Candidate,
	leaseCounts map[string]int,
	health map[string]HealthState,
) (Candidate, bool) {
	assignable := make([]Candidate, 0, len(candidates))
	for _, candidate := range candidates {
		if !memberAssignable(candidate.Member, candidate.Profile, health) {
			continue
		}
		assignable = append(assignable, candidate)
	}
	if len(assignable) == 0 {
		return Candidate{}, false
	}
	if strings.EqualFold(strings.TrimSpace(strategy), "weighted") {
		return selectWeighted(assignable, leaseCounts), true
	}
	return selectRoundRobin(assignable, leaseCounts), true
}

func selectRoundRobin(candidates []Candidate, leaseCounts map[string]int) Candidate {
	best := candidates[0]
	for _, candidate := range candidates[1:] {
		if roundRobinBefore(candidate, best, leaseCounts) {
			best = candidate
		}
	}
	return best
}

func roundRobinBefore(left, right Candidate, leaseCounts map[string]int) bool {
	leftLeases := leaseCounts[left.Profile.ID]
	rightLeases := leaseCounts[right.Profile.ID]
	if leftLeases != rightLeases {
		return leftLeases < rightLeases
	}
	if left.Member.Priority != right.Member.Priority {
		return left.Member.Priority < right.Member.Priority
	}
	return left.Profile.ID < right.Profile.ID
}

func selectWeighted(candidates []Candidate, leaseCounts map[string]int) Candidate {
	best := candidates[0]
	for _, candidate := range candidates[1:] {
		if weightedBefore(candidate, best, leaseCounts) {
			best = candidate
		}
	}
	return best
}

func weightedBefore(left, right Candidate, leaseCounts map[string]int) bool {
	leftWeight := normalizedWeight(left.Member.Weight)
	rightWeight := normalizedWeight(right.Member.Weight)
	leftLeases := int64(leaseCounts[left.Profile.ID])
	rightLeases := int64(leaseCounts[right.Profile.ID])
	// leftLeases/leftWeight < rightLeases/rightWeight.
	leftProduct := leftLeases * int64(rightWeight)
	rightProduct := rightLeases * int64(leftWeight)
	if leftProduct != rightProduct {
		return leftProduct < rightProduct
	}
	if left.Member.Priority != right.Member.Priority {
		return left.Member.Priority < right.Member.Priority
	}
	return left.Profile.ID < right.Profile.ID
}

func normalizedWeight(weight int) int {
	if weight < 1 {
		return 1
	}
	return weight
}

// redisHealth returns a usable Redis projection entry. Missing / empty /
// unknown entries fall back to PG.
func redisHealth(profileID string, health map[string]HealthState) (HealthState, bool) {
	if health == nil {
		return "", false
	}
	state, ok := health[profileID]
	if !ok || state == "" || state == HealthUnknown {
		return "", false
	}
	return state, true
}

// deadFailureStreak is the §6.2 streak threshold.
const deadFailureStreak = 3

// memberAssignable is the single §6.2 gate shared by selection and the
// resolver: member+profile enabled, health not dead, and failure streak below
// 3. Redis carries only the state and is newer than the PG row, so when a Redis
// entry exists it supersedes the PG status/streak pair; the streak check only
// applies on the PG fallback (a stale PG streak must not veto a newer Redis
// success).
func memberAssignable(member Member, profile Profile, health map[string]HealthState) bool {
	return member.Enabled && profileAssignable(profile, health)
}

func profileAssignable(profile Profile, health map[string]HealthState) bool {
	if !profile.Enabled {
		return false
	}
	if state, ok := redisHealth(profile.ID, health); ok {
		return state != HealthDead
	}
	if profile.HealthStatus == HealthDead {
		return false
	}
	return profile.HealthFailureStreak < deadFailureStreak
}
