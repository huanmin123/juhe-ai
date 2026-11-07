package proxyresolve

import "testing"

func candidate(id string, priority, weight int, health HealthState) Candidate {
	return Candidate{
		Member:  Member{ProfileID: id, Priority: priority, Weight: weight, Enabled: true},
		Profile: Profile{ID: id, Enabled: true, HealthStatus: health},
	}
}

func TestSelectRoundRobin(t *testing.T) {
	candidates := []Candidate{
		candidate("a", 0, 1, HealthHealthy),
		candidate("b", 1, 1, HealthHealthy),
		candidate("c", 2, 1, HealthHealthy),
	}
	selected, ok := SelectMember("round_robin", candidates, map[string]int{"a": 2, "b": 2, "c": 1}, nil)
	if !ok || selected.Profile.ID != "c" {
		t.Fatalf("round_robin selected %v, want c", selected.Profile.ID)
	}
}

func TestSelectRoundRobinTiePriority(t *testing.T) {
	candidates := []Candidate{
		candidate("b", 1, 1, HealthHealthy),
		candidate("a", 1, 1, HealthHealthy),
	}
	selected, ok := SelectMember("round_robin", candidates, map[string]int{"a": 1, "b": 1}, nil)
	if !ok || selected.Profile.ID != "a" {
		t.Fatalf("tie selected %v, want a (profile id tie-break)", selected.Profile.ID)
	}
}

func TestSelectWeighted(t *testing.T) {
	candidates := []Candidate{
		candidate("a", 0, 1, HealthHealthy),
		candidate("b", 1, 2, HealthHealthy),
		candidate("c", 2, 1, HealthHealthy),
	}
	// leases/weight: a=2, b=2, c=2 -> priority a wins.
	selected, ok := SelectMember("weighted", candidates, map[string]int{"a": 2, "b": 4, "c": 2}, nil)
	if !ok || selected.Profile.ID != "a" {
		t.Fatalf("weighted tie selected %v, want a", selected.Profile.ID)
	}
	// leases/weight: a=2, b=1.5, c=2 -> b wins.
	selected, ok = SelectMember("weighted", candidates, map[string]int{"a": 2, "b": 3, "c": 2}, nil)
	if !ok || selected.Profile.ID != "b" {
		t.Fatalf("weighted ratio selected %v, want b", selected.Profile.ID)
	}
}

func TestSelectSkipsDeadAndDisabled(t *testing.T) {
	candidates := []Candidate{
		candidate("a", 0, 1, HealthDead),
		{Member: Member{ProfileID: "b", Priority: 1, Weight: 1, Enabled: true}, Profile: Profile{ID: "b", Enabled: false}},
		candidate("c", 2, 1, HealthDegraded),
	}
	selected, ok := SelectMember("round_robin", candidates, nil, nil)
	if !ok || selected.Profile.ID != "c" {
		t.Fatalf("selected %v, want c", selected.Profile.ID)
	}
	if _, ok := SelectMember("round_robin", candidates[:2], nil, nil); ok {
		t.Fatal("expected no assignable member")
	}
}

func TestSelectRedisHealthOverridesProfileHealth(t *testing.T) {
	candidates := []Candidate{candidate("a", 0, 1, HealthDead)}
	selected, ok := SelectMember("round_robin", candidates, nil, map[string]HealthState{"a": HealthHealthy})
	if !ok || selected.Profile.ID != "a" {
		t.Fatalf("Redis healthy should override PG dead, selected %v", selected.Profile.ID)
	}
}

func TestSelectAppliesStreakOnlyOnPGFallback(t *testing.T) {
	stale := candidate("a", 0, 1, HealthHealthy)
	stale.Profile.HealthFailureStreak = deadFailureStreak
	if _, ok := SelectMember("round_robin", []Candidate{stale}, nil, nil); ok {
		t.Fatal("PG streak >= 3 must be unassignable even when status is not dead")
	}
	// A newer Redis success supersedes the stale PG status/streak pair.
	selected, ok := SelectMember("round_robin", []Candidate{stale}, nil, map[string]HealthState{"a": HealthHealthy})
	if !ok || selected.Profile.ID != "a" {
		t.Fatalf("Redis success should override stale PG streak, selected %v", selected.Profile.ID)
	}
}
