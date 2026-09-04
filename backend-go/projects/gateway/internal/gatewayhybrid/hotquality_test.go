package gatewayhybrid

import (
	"strings"
	"testing"
)

type hqPayload struct {
	label string
}

// HotQualityCandidatePayload pairs the base candidate view with an opaque
// extension payload (mirrors the TS generic TCandidate).
type HotQualityCandidatePayload struct {
	Candidate HotQualityCandidate
	Payload   hqPayload
}

func hqCandidate(label string, tier GatewayAccountConfigurationTier, mutate func(*HotQualityCandidate)) HotQualityCandidatePayload {
	candidate := HotQualityCandidate{
		AccountID:          label,
		AccountRuntimeKey:  "rt-" + label,
		RouteScopeKey:      "scope",
		ConfigurationTier:  tier,
		StableBindingOrder: 0,
	}
	if mutate != nil {
		mutate(&candidate)
	}
	return HotQualityCandidatePayload{Candidate: candidate, Payload: hqPayload{label: label}}
}

func baseOf(payload HotQualityCandidatePayload) HotQualityCandidate { return payload.Candidate }

func normalTier() GatewayAccountConfigurationTier {
	return GatewayAccountConfigurationTier{ModelMatchRank: 0, FallbackEnabled: false, SuperPriorityEnabled: false, Priority: 0}
}

func fallbackTier() GatewayAccountConfigurationTier {
	return GatewayAccountConfigurationTier{ModelMatchRank: 1, FallbackEnabled: true, SuperPriorityEnabled: false, Priority: 0}
}

func TestGatewayAccountConfigurationTierKey(t *testing.T) {
	key, err := GatewayAccountConfigurationTierKey(GatewayAccountConfigurationTier{ModelMatchRank: 2, FallbackEnabled: true, SuperPriorityEnabled: false, Priority: -3})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key != "model=2|fallback=1|super=0|priority=-3" {
		t.Fatalf("key = %s", key)
	}
	if _, err := GatewayAccountConfigurationTierKey(GatewayAccountConfigurationTier{ModelMatchRank: -1}); err == nil || err.Error() != "modelMatchRank 不能为负数" {
		t.Fatalf("err = %v", err)
	}
}

func TestDecideHotQualityCandidateValidationErrors(t *testing.T) {
	if _, err := DecideHotQualityCandidate(DecideHotQualityCandidateInput[HotQualityCandidatePayload]{
		RouteScopeKey: "  ", Candidates: nil, Base: baseOf,
	}); err == nil || err.Error() != "路由范围不能为空" {
		t.Fatalf("err = %v", err)
	}
	if _, err := DecideHotQualityCandidate(DecideHotQualityCandidateInput[HotQualityCandidatePayload]{
		RouteScopeKey: "scope", Mode: "cheapest", Base: baseOf,
	}); err == nil || err.Error() != "热质量路由模式无效" {
		t.Fatalf("err = %v", err)
	}
	if _, err := DecideHotQualityCandidate(DecideHotQualityCandidateInput[HotQualityCandidatePayload]{
		RouteScopeKey: "scope", Mode: HotQualityModeCostFirst, Base: baseOf,
		Candidates: []HotQualityCandidatePayload{hqCandidate("a", normalTier(), func(candidate *HotQualityCandidate) {
			candidate.RouteScopeKey = "other"
		})},
	}); err == nil || err.Error() != "候选账号 a 不属于当前路由范围" {
		t.Fatalf("err = %v", err)
	}
	if _, err := DecideHotQualityCandidate(DecideHotQualityCandidateInput[HotQualityCandidatePayload]{
		RouteScopeKey: "scope", Mode: HotQualityModeCostFirst, Base: baseOf,
		Candidates:  []HotQualityCandidatePayload{hqCandidate("a", normalTier(), nil)},
		Exploration: &SameTierExplorationState{Enabled: true, Credit: 1.5},
	}); err == nil || err.Error() != "同层探索 credit 必须位于 0..1" {
		t.Fatalf("err = %v", err)
	}
	if _, err := DecideHotQualityCandidate(DecideHotQualityCandidateInput[HotQualityCandidatePayload]{
		RouteScopeKey: "scope", Mode: HotQualityModeCostFirst, Base: baseOf,
		Candidates:  []HotQualityCandidatePayload{hqCandidate("a", normalTier(), nil)},
		Exploration: &SameTierExplorationState{Enabled: true, Credit: 0.5, Cursor: -1},
	}); err == nil || err.Error() != "同层探索 cursor 不能为负数" {
		t.Fatalf("err = %v", err)
	}
	if _, err := DecideHotQualityCandidate(DecideHotQualityCandidateInput[HotQualityCandidatePayload]{
		RouteScopeKey: "scope", Mode: HotQualityModeCostFirst, Base: baseOf,
		Candidates: []HotQualityCandidatePayload{hqCandidate("", normalTier(), nil)},
	}); err == nil || err.Error() != "账号 ID不能为空" {
		t.Fatalf("err = %v", err)
	}
}

func TestDecideHotQualityCandidateEmptyAndDuplicate(t *testing.T) {
	decision, err := DecideHotQualityCandidate(DecideHotQualityCandidateInput[HotQualityCandidatePayload]{
		RouteScopeKey: "scope", Mode: HotQualityModeCostFirst, Base: baseOf,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision.SelectedCandidate != nil || decision.DispatchIntent != DispatchIntentPrimaryService {
		t.Fatalf("decision = %+v", decision)
	}
	if decision.Explanation.SelectionReason != SelectionReasonNoCandidate {
		t.Fatalf("reason = %s", decision.Explanation.SelectionReason)
	}
	if decision.Explanation.Exploration.Status != ExplorationStatusNotConfigured {
		t.Fatalf("exploration = %+v", decision.Explanation.Exploration)
	}

	// Duplicate runtime keys are reported and dropped from ordering.
	duplicated := []HotQualityCandidatePayload{
		hqCandidate("a", normalTier(), nil),
		hqCandidate("a2", normalTier(), func(candidate *HotQualityCandidate) {
			candidate.AccountRuntimeKey = "rt-a"
		}),
		hqCandidate("b", normalTier(), nil),
	}
	decision, err = DecideHotQualityCandidate(DecideHotQualityCandidateInput[HotQualityCandidatePayload]{
		RouteScopeKey: "scope", Mode: HotQualityModeCostFirst, Base: baseOf, Candidates: duplicated,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(decision.Explanation.DuplicateRuntimeAccountIDs) != 1 || decision.Explanation.DuplicateRuntimeAccountIDs[0] != "a2" {
		t.Fatalf("duplicates = %v", decision.Explanation.DuplicateRuntimeAccountIDs)
	}
	if len(decision.OrderedCandidates) != 2 {
		t.Fatalf("ordered = %d", len(decision.OrderedCandidates))
	}
}

func TestDecideHotQualityCandidateTierOrdering(t *testing.T) {
	// Tiers keep first-seen order: the normal tier leads even though the
	// fallback tier has a lower binding order.
	candidates := []HotQualityCandidatePayload{
		hqCandidate("normal-1", normalTier(), func(candidate *HotQualityCandidate) {
			candidate.StableBindingOrder = 1
		}),
		hqCandidate("fallback-1", fallbackTier(), func(candidate *HotQualityCandidate) {
			candidate.StableBindingOrder = 0
		}),
	}
	decision, err := DecideHotQualityCandidate(DecideHotQualityCandidateInput[HotQualityCandidatePayload]{
		RouteScopeKey: "scope", Mode: HotQualityModeCostFirst, Base: baseOf, Candidates: candidates,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(decision.Explanation.QualityReorderedTierKeys) != 0 {
		t.Fatalf("reordered tiers = %v", decision.Explanation.QualityReorderedTierKeys)
	}
	if got := decision.OrderedCandidates[0].Payload.label; got != "normal-1" {
		t.Fatalf("first = %s", got)
	}
	if got := decision.OrderedCandidates[1].Payload.label; got != "fallback-1" {
		t.Fatalf("second = %s", got)
	}
	if decision.Explanation.BaselinePrimaryAccountID != "normal-1" {
		t.Fatalf("baseline = %s", decision.Explanation.BaselinePrimaryAccountID)
	}

	// Within a tier: healthy ranks before cold (unknown), unhealthy last;
	// ties fall back to binding order, account id, then input order.
	reliabilityCandidates := []HotQualityCandidatePayload{
		hqCandidate("warm", normalTier(), func(candidate *HotQualityCandidate) {
			candidate.StableBindingOrder = 0
			candidate.HotQuality = &HotQualitySnapshot{SampleState: SampleStateWarming, ReliabilityLevel: ReliabilityUnhealthy}
		}),
		hqCandidate("cold", normalTier(), func(candidate *HotQualityCandidate) {
			candidate.StableBindingOrder = 1
		}),
		hqCandidate("healthy", normalTier(), func(candidate *HotQualityCandidate) {
			candidate.StableBindingOrder = 2
			candidate.HotQuality = &HotQualitySnapshot{SampleState: SampleStateKnown, ReliabilityLevel: ReliabilityHealthy}
		}),
	}
	decision, err = DecideHotQualityCandidate(DecideHotQualityCandidateInput[HotQualityCandidatePayload]{
		RouteScopeKey: "scope", Mode: HotQualityModeCostFirst, Base: baseOf, Candidates: reliabilityCandidates,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	labels := []string{}
	for _, payload := range decision.OrderedCandidates {
		labels = append(labels, payload.Payload.label)
	}
	// healthy (rank 0) < cold (cold→0.5, unknown rank) < warm (unhealthy rank 3).
	if strings.Join(labels, ",") != "healthy,cold,warm" {
		t.Fatalf("labels = %v", labels)
	}
}

func TestDecideHotQualityCandidateSpeedFirstLatencyGrouping(t *testing.T) {
	degraded := func(candidate *HotQualityCandidate) { candidate.LatencyDegraded = true }
	candidates := []HotQualityCandidatePayload{
		hqCandidate("degraded-1", normalTier(), degraded),
		hqCandidate("fast-1", normalTier(), nil),
		hqCandidate("fast-2", normalTier(), nil),
	}
	decision, err := DecideHotQualityCandidate(DecideHotQualityCandidateInput[HotQualityCandidatePayload]{
		RouteScopeKey: "scope", Mode: HotQualityModeSpeedFirst, Base: baseOf, Candidates: candidates,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !decision.Explanation.LatencyDegradedOverrideApplied {
		t.Fatal("override flag expected")
	}
	if decision.OrderedCandidates[0].Payload.label != "fast-1" || decision.OrderedCandidates[1].Payload.label != "fast-2" {
		t.Fatalf("fast group first: %+v", decision.OrderedCandidates)
	}
	if decision.OrderedCandidates[2].Payload.label != "degraded-1" {
		t.Fatalf("degraded last: %s", decision.OrderedCandidates[2].Payload.label)
	}

	// cost_first keeps the base order (no override).
	costDecision, err := DecideHotQualityCandidate(DecideHotQualityCandidateInput[HotQualityCandidatePayload]{
		RouteScopeKey: "scope", Mode: HotQualityModeCostFirst, Base: baseOf, Candidates: candidates,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if costDecision.Explanation.LatencyDegradedOverrideApplied {
		t.Fatal("cost mode must not apply override")
	}
}

func TestDecideHotQualityCandidateSpeedCompareEwma(t *testing.T) {
	candidates := []HotQualityCandidatePayload{
		hqCandidate("slow", normalTier(), func(candidate *HotQualityCandidate) {
			candidate.HotQuality = &HotQualitySnapshot{SampleState: SampleStateKnown, ReliabilityLevel: ReliabilityHealthy, FirstByteEwma5m: floatPtr(500)}
		}),
		hqCandidate("quick", normalTier(), func(candidate *HotQualityCandidate) {
			candidate.HotQuality = &HotQualitySnapshot{SampleState: SampleStateKnown, ReliabilityLevel: ReliabilityHealthy, FirstByteEwma5m: floatPtr(120)}
		}),
	}
	decision, err := DecideHotQualityCandidate(DecideHotQualityCandidateInput[HotQualityCandidatePayload]{
		RouteScopeKey: "scope", Mode: HotQualityModeCostFirst, Base: baseOf, Candidates: candidates,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision.OrderedCandidates[0].Payload.label != "quick" {
		t.Fatalf("ewma ordering failed: %s first", decision.OrderedCandidates[0].Payload.label)
	}
}

func TestDecideExplorationStatuses(t *testing.T) {
	candidateFor := func(label string, mutate func(*HotQualityCandidate)) HotQualityCandidatePayload {
		return hqCandidate(label, normalTier(), mutate)
	}
	// The warming candidate ranks ahead of the cold one, so exploration
	// targets the cold candidate.
	primary := candidateFor("primary", func(candidate *HotQualityCandidate) {
		candidate.StableBindingOrder = 0
		candidate.HotQuality = &HotQualitySnapshot{SampleState: SampleStateWarming, ReliabilityLevel: ReliabilityHealthy}
	})
	coldTarget := candidateFor("cold-target", func(candidate *HotQualityCandidate) {
		candidate.StableBindingOrder = 1
	})
	candidates := []HotQualityCandidatePayload{primary, coldTarget}

	state := func(mutate func(*SameTierExplorationState)) *SameTierExplorationState {
		exploration := &SameTierExplorationState{
			Enabled:                      true,
			EligibleFirstPrimaryDispatch: true,
			// Full credit: per-dispatch accrual is only 0.05, so selection
			// requires a topped-up balance (Node gates on creditAfterAccrual
			// >= credit cost 1).
			Credit:                  1,
			Cursor:                  0,
			NowMs:                   10_000,
			KnownSampleStaleAfterMs: 60_000,
		}
		if mutate != nil {
			mutate(exploration)
		}
		return exploration
	}

	tests := []struct {
		name       string
		state      *SameTierExplorationState
		wantStatus string
	}{
		{"disabled", state(func(exploration *SameTierExplorationState) { exploration.Enabled = false }), ExplorationStatusDisabled},
		{"ineligible", state(func(exploration *SameTierExplorationState) { exploration.EligibleFirstPrimaryDispatch = false }), ExplorationStatusIneligiblePrimaryDispatch},
		{"already explored", state(func(exploration *SameTierExplorationState) { exploration.RequestAlreadyExplored = true }), ExplorationStatusRequestAlreadyExplored},
		{"left highest tier", state(func(exploration *SameTierExplorationState) { exploration.HasLeftHighestNormalTier = true }), ExplorationStatusLeftHighestNormalTier},
		{
			// Accrual is skipped when already applied: 0.99 + 0 < 1.
			"insufficient credit", state(func(exploration *SameTierExplorationState) {
				exploration.Credit = 0.99
				exploration.CreditAccrualAlreadyApplied = true
			}), ExplorationStatusInsufficientCredit,
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			decision, err := DecideHotQualityCandidate(DecideHotQualityCandidateInput[HotQualityCandidatePayload]{
				RouteScopeKey: "scope", Mode: HotQualityModeCostFirst, Base: baseOf, Candidates: candidates, Exploration: testCase.state,
			})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if decision.Explanation.Exploration.Status != testCase.wantStatus {
				t.Fatalf("status = %s, want %s", decision.Explanation.Exploration.Status, testCase.wantStatus)
			}
			if decision.Explanation.SelectionReason != SelectionReasonRankedPrimary {
				t.Fatalf("selection reason = %s", decision.Explanation.SelectionReason)
			}
		})
	}

	t.Run("fallback tier primary rejects", func(t *testing.T) {
		decision, err := DecideHotQualityCandidate(DecideHotQualityCandidateInput[HotQualityCandidatePayload]{
			RouteScopeKey: "scope", Mode: HotQualityModeCostFirst, Base: baseOf,
			Candidates:  []HotQualityCandidatePayload{hqCandidate("fb", fallbackTier(), nil)},
			Exploration: state(nil),
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if decision.Explanation.Exploration.Status != ExplorationStatusFallbackTier {
			t.Fatalf("status = %s", decision.Explanation.Exploration.Status)
		}
	})

	t.Run("credit boundary 1.0 selects and spends", func(t *testing.T) {
		decision, err := DecideHotQualityCandidate(DecideHotQualityCandidateInput[HotQualityCandidatePayload]{
			RouteScopeKey: "scope", Mode: HotQualityModeCostFirst, Base: baseOf, Candidates: candidates,
			Exploration: state(func(exploration *SameTierExplorationState) { exploration.Credit = 1 }),
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if decision.Explanation.Exploration.Status != ExplorationStatusSelected {
			t.Fatalf("status = %s", decision.Explanation.Exploration.Status)
		}
		explanation := decision.Explanation.Exploration
		if explanation.CreditAfterSuccessfulDispatch != 0 {
			t.Fatalf("credit after = %v", explanation.CreditAfterSuccessfulDispatch)
		}
		if explanation.CreditAfterFailedDispatch != 1 {
			t.Fatalf("credit after fail = %v", explanation.CreditAfterFailedDispatch)
		}
		if explanation.CreditSpendOnSuccessfulDispatch != 1 {
			t.Fatalf("credit spend = %v", explanation.CreditSpendOnSuccessfulDispatch)
		}
		if decision.DispatchIntent != DispatchIntentSameTierExploration {
			t.Fatalf("intent = %s", decision.DispatchIntent)
		}
		if decision.SelectedCandidate == nil || decision.SelectedCandidate.Payload.label != "cold-target" {
			t.Fatalf("selected = %+v", decision.SelectedCandidate)
		}
		// Selected target moves to the front of the ordered list.
		if decision.OrderedCandidates[0].Payload.label != "cold-target" {
			t.Fatalf("ordered[0] = %s", decision.OrderedCandidates[0].Payload.label)
		}
		if len(explanation.FairCursorPeerAccountIDs) != 1 || explanation.FairCursorPeerAccountIDs[0] != "cold-target" {
			t.Fatalf("fair peers = %v", explanation.FairCursorPeerAccountIDs)
		}
	})

	t.Run("cursor rotates through fair peers", func(t *testing.T) {
		peer2 := candidateFor("peer-2", func(candidate *HotQualityCandidate) {
			candidate.StableBindingOrder = 2
		})
		all := []HotQualityCandidatePayload{primary, coldTarget, peer2}
		first, err := DecideHotQualityCandidate(DecideHotQualityCandidateInput[HotQualityCandidatePayload]{
			RouteScopeKey: "scope", Mode: HotQualityModeCostFirst, Base: baseOf, Candidates: all,
			Exploration: state(nil),
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		second, err := DecideHotQualityCandidate(DecideHotQualityCandidateInput[HotQualityCandidatePayload]{
			RouteScopeKey: "scope", Mode: HotQualityModeCostFirst, Base: baseOf, Candidates: all,
			Exploration: state(func(exploration *SameTierExplorationState) { exploration.Cursor = 1 }),
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if first.SelectedCandidate.Payload.label == second.SelectedCandidate.Payload.label {
			t.Fatalf("cursor did not rotate: %s vs %s", first.SelectedCandidate.Payload.label, second.SelectedCandidate.Payload.label)
		}
		if first.Explanation.Exploration.CursorAfterSuccessfulDispatch != 1 {
			t.Fatalf("cursor after = %d", first.Explanation.Exploration.CursorAfterSuccessfulDispatch)
		}
	})

	t.Run("in flight and cooldown filter targets", func(t *testing.T) {
		blocked, err := DecideHotQualityCandidate(DecideHotQualityCandidateInput[HotQualityCandidatePayload]{
			RouteScopeKey: "scope", Mode: HotQualityModeCostFirst, Base: baseOf, Candidates: candidates,
			Exploration: state(func(exploration *SameTierExplorationState) {
				exploration.TargetInFlightRuntimeKeys = []string{"rt-cold-target"}
			}),
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if blocked.Explanation.Exploration.Status != ExplorationStatusNoEligibleTarget {
			t.Fatalf("in-flight status = %s", blocked.Explanation.Exploration.Status)
		}
		cooled, err := DecideHotQualityCandidate(DecideHotQualityCandidateInput[HotQualityCandidatePayload]{
			RouteScopeKey: "scope", Mode: HotQualityModeCostFirst, Base: baseOf, Candidates: candidates,
			Exploration: state(func(exploration *SameTierExplorationState) {
				exploration.TargetCooldownUntilMsByRuntimeKey = map[string]int64{"rt-cold-target": 10_000}
			}),
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// Cooldown ending exactly at nowMs stays eligible (<= nowMs).
		if cooled.Explanation.Exploration.Status != ExplorationStatusSelected {
			t.Fatalf("cooldown boundary status = %s", cooled.Explanation.Exploration.Status)
		}
		blockedCooldown, err := DecideHotQualityCandidate(DecideHotQualityCandidateInput[HotQualityCandidatePayload]{
			RouteScopeKey: "scope", Mode: HotQualityModeCostFirst, Base: baseOf, Candidates: candidates,
			Exploration: state(func(exploration *SameTierExplorationState) {
				exploration.TargetCooldownUntilMsByRuntimeKey = map[string]int64{"rt-cold-target": 10_001}
			}),
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if blockedCooldown.Explanation.Exploration.Status != ExplorationStatusNoEligibleTarget {
			t.Fatalf("cooldown blocked status = %s", blockedCooldown.Explanation.Exploration.Status)
		}
	})

	t.Run("known sample needs staleness", func(t *testing.T) {
		warmingPrimary := candidateFor("warm", func(candidate *HotQualityCandidate) {
			candidate.StableBindingOrder = 0
			candidate.HotQuality = &HotQualitySnapshot{SampleState: SampleStateWarming, ReliabilityLevel: ReliabilityHealthy}
		})
		known := candidateFor("known", func(candidate *HotQualityCandidate) {
			candidate.StableBindingOrder = 1
			candidate.HotQuality = &HotQualitySnapshot{
				SampleState:      SampleStateKnown,
				ReliabilityLevel: ReliabilityHealthy,
				Window30m:        HotQualityWindowSnapshot{LastCompletedAtMs: int64Ptr(9_000)},
			}
		})
		pair := []HotQualityCandidatePayload{warmingPrimary, known}
		fresh, err := DecideHotQualityCandidate(DecideHotQualityCandidateInput[HotQualityCandidatePayload]{
			RouteScopeKey: "scope", Mode: HotQualityModeCostFirst, Base: baseOf, Candidates: pair,
			Exploration: state(nil),
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if fresh.Explanation.Exploration.Status != ExplorationStatusNoEligibleTarget {
			t.Fatalf("fresh known status = %s", fresh.Explanation.Exploration.Status)
		}
		stale, err := DecideHotQualityCandidate(DecideHotQualityCandidateInput[HotQualityCandidatePayload]{
			RouteScopeKey: "scope", Mode: HotQualityModeCostFirst, Base: baseOf, Candidates: pair,
			Exploration: state(func(exploration *SameTierExplorationState) {
				exploration.NowMs = 200_000
			}),
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if stale.Explanation.Exploration.Status != ExplorationStatusSelected {
			t.Fatalf("stale known status = %s", stale.Explanation.Exploration.Status)
		}
	})
}
