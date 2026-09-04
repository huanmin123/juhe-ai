package gatewayhybrid

import (
	"math"
	"sort"
	"strconv"
	"strings"
)

// Hot quality candidate selection, mirroring
// backend/src/modules/gateway/routing/hot-quality-candidate-selection.ts.
// Fully deterministic: tier ordering plus cursor-based same-tier exploration
// fairness (Node has no randomness here, so no rng is injected).

// HotQualityRoutingMode mirrors HotQualityRoutingMode.
type HotQualityRoutingMode = string

const (
	HotQualityModeCostFirst  = "cost_first"
	HotQualityModeSpeedFirst = "speed_first"
)

// HotQualityDispatchIntent mirrors HotQualityDispatchIntent.
type HotQualityDispatchIntent = string

const (
	DispatchIntentPrimaryService      = "primary_service"
	DispatchIntentSameTierExploration = "same_tier_exploration"
)

// Hot quality reliability / sample states (mirror the Node unions).
type HotQualityReliabilityLevel = string

const (
	ReliabilityUnknown   = "unknown"
	ReliabilityHealthy   = "healthy"
	ReliabilityUncertain = "uncertain"
	ReliabilityUnhealthy = "unhealthy"
)

type HotQualitySampleState = string

const (
	SampleStateCold    = "cold"
	SampleStateWarming = "warming"
	SampleStateKnown   = "known"
)

// Exploration credit constants (mirror the exported Node constants).
const (
	SameTierExplorationCreditPerEligibleDispatch = 0.05
	SameTierExplorationCreditCap                 = 1.0
	SameTierExplorationCreditCost                = 1.0
	SameTierExplorationTargetCooldownMs          = int64(60_000)
	maxSafeInteger                                = int64(1)<<53 - 1
)

// GatewayAccountConfigurationTier mirrors GatewayAccountConfigurationTier.
type GatewayAccountConfigurationTier struct {
	ModelMatchRank       int
	FallbackEnabled      bool
	SuperPriorityEnabled bool
	Priority             int
}

// HotQualityWindowSnapshot carries the window fields candidate selection
// reads (subset of HotQualityWindowSnapshot).
type HotQualityWindowSnapshot struct {
	QualityAttempts    int
	LastCompletedAtMs  *int64
	LastFailureAtMs    *int64
}

// HotQualitySnapshot carries the snapshot fields candidate selection reads.
type HotQualitySnapshot struct {
	Window5m              HotQualityWindowSnapshot
	Window10m             HotQualityWindowSnapshot
	Window30m             HotQualityWindowSnapshot
	EffectiveReliability  float64
	ReliabilityLevel      HotQualityReliabilityLevel
	SampleState           HotQualitySampleState
	FirstByteEwma5m       *float64
	FirstByteP95Bucket10m *float64
}

// HotQualityCandidate mirrors HotQualityCandidate.
type HotQualityCandidate struct {
	AccountID                   string
	AccountRuntimeKey           string
	RouteScopeKey               string
	ConfigurationTier           GatewayAccountConfigurationTier
	StableBindingOrder          int
	HotQuality                  *HotQualitySnapshot
	LatencyDegraded             bool
	LastExplorationAttemptAtMs  *int64
}

// SameTierExplorationState mirrors SameTierExplorationState.
type SameTierExplorationState struct {
	Enabled                       bool
	EligibleFirstPrimaryDispatch  bool
	CreditAccrualAlreadyApplied   bool
	RequestAlreadyExplored        bool
	HasLeftHighestNormalTier      bool
	Credit                        float64
	Cursor                        int64
	NowMs                         int64
	KnownSampleStaleAfterMs       int64
	TargetInFlightRuntimeKeys     []string
	TargetCooldownUntilMsByRuntimeKey map[string]int64
}

// Same-tier exploration statuses (mirror the Node union).
const (
	ExplorationStatusNotConfigured           = "not_configured"
	ExplorationStatusDisabled                = "disabled"
	ExplorationStatusNoPrimaryCandidate      = "no_primary_candidate"
	ExplorationStatusIneligiblePrimaryDispatch = "ineligible_primary_dispatch"
	ExplorationStatusRequestAlreadyExplored  = "request_already_explored"
	ExplorationStatusLeftHighestNormalTier   = "left_highest_normal_tier"
	ExplorationStatusFallbackTier            = "fallback_tier"
	ExplorationStatusInsufficientCredit      = "insufficient_credit"
	ExplorationStatusNoEligibleTarget        = "no_eligible_target"
	ExplorationStatusSelected                = "selected"
)

// Selection reasons (mirror the Node union).
const (
	SelectionReasonNoCandidate         = "no_candidate"
	SelectionReasonRankedPrimary       = "ranked_primary"
	SelectionReasonSameTierExploration = "same_tier_exploration"
)

// SameTierExplorationExplanation mirrors SameTierExplorationExplanation;
// SelectedTargetAccountID stays empty when Node leaves it undefined.
type SameTierExplorationExplanation struct {
	Status                          string
	CreditBefore                    float64
	CreditAccrued                   float64
	CreditAfterAccrual              float64
	CreditSpendOnSuccessfulDispatch float64
	CreditAfterSuccessfulDispatch   float64
	CreditAfterFailedDispatch       float64
	CursorBefore                    int64
	CursorAfterSuccessfulDispatch   int64
	CursorAfterFailedDispatch       int64
	EligibleTargetAccountIDs        []string
	FairCursorPeerAccountIDs        []string
	SelectedTargetAccountID         string
}

// HotQualityCandidateSelectionExplanation mirrors
// HotQualityCandidateSelectionExplanation.
type HotQualityCandidateSelectionExplanation struct {
	Mode                          HotQualityRoutingMode
	RouteScopeKey                 string
	SelectionReason               string
	BaselinePrimaryAccountID      string
	SelectedAccountID             string
	SelectedAccountRuntimeKey     string
	SelectedTierKey               string
	SelectedSampleState           HotQualitySampleState
	SelectedReliabilityLevel      HotQualityReliabilityLevel
	LatencyDegradedOverrideApplied bool
	QualityReorderedTierKeys      []string
	DuplicateRuntimeAccountIDs    []string
	Exploration                   SameTierExplorationExplanation
}

// HotQualityCandidateDecision mirrors HotQualityCandidateDecision; candidates
// keep their original generic payload via T.
type HotQualityCandidateDecision[T any] struct {
	SelectedCandidate        *T
	DispatchIntent           HotQualityDispatchIntent
	QualityOrderedCandidates []T
	OrderedCandidates        []T
	Explanation              HotQualityCandidateSelectionExplanation
}

// DecideHotQualityCandidateInput mirrors DecideHotQualityCandidateInput. Base
// projects each payload onto its HotQualityCandidate view (mirrors the TS
// generic constraint TCandidate extends HotQualityCandidate).
type DecideHotQualityCandidateInput[T any] struct {
	Mode          HotQualityRoutingMode
	RouteScopeKey string
	Candidates    []T
	Base          func(T) HotQualityCandidate
	Exploration   *SameTierExplorationState
}

// indexedCandidate mirrors IndexedCandidate; identity comparisons in Node
// become original-index comparisons here.
type indexedCandidate[T any] struct {
	payload         T
	base            HotQualityCandidate
	originalIndex   int
	tierKey         string
	sampleState     HotQualitySampleState
	reliabilityLevel HotQualityReliabilityLevel
}

type explorationRankedCandidate[T any] struct {
	indexed                       *indexedCandidate[T]
	sampleRank                    int
	sampleGap                     int
	lastValidBusinessObservationAtMs int64
	lastExplorationAttemptAtMs    int64
}

// GatewayAccountConfigurationTierKey mirrors
// gatewayAccountConfigurationTierKey; invalid tier numbers raise the
// byte-identical RangeError messages.
func GatewayAccountConfigurationTierKey(tier GatewayAccountConfigurationTier) (string, error) {
	modelMatchRank, err := normalizedNonNegativeInteger(int64(tier.ModelMatchRank), "modelMatchRank")
	if err != nil {
		return "", err
	}
	priority, err := normalizedSafeInteger(int64(tier.Priority), "priority")
	if err != nil {
		return "", err
	}
	fallback := 0
	if tier.FallbackEnabled {
		fallback = 1
	}
	super := 0
	if tier.SuperPriorityEnabled {
		super = 1
	}
	return "model=" + strconv.Itoa(int(modelMatchRank)) +
		"|fallback=" + strconv.Itoa(fallback) +
		"|super=" + strconv.Itoa(super) +
		"|priority=" + strconv.Itoa(int(priority)), nil
}

// DecideHotQualityCandidate mirrors decideHotQualityCandidate.
func DecideHotQualityCandidate[T any](input DecideHotQualityCandidateInput[T]) (*HotQualityCandidateDecision[T], error) {
	routeScopeKey, err := requiredKey(input.RouteScopeKey, "路由范围")
	if err != nil {
		return nil, err
	}
	mode, err := normalizedMode(input.Mode)
	if err != nil {
		return nil, err
	}
	normalized, duplicateRuntimeAccountIDs, err := normalizeCandidates(input.Candidates, input.Base, routeScopeKey)
	if err != nil {
		return nil, err
	}

	baseTierOrder := distinctTierKeys(normalized)
	qualityReorderedTierKeys := []string{}
	qualityByTier := map[string][]*indexedCandidate[T]{}

	for _, tierKey := range baseTierOrder {
		originalTier := candidatesOfTier(normalized, tierKey)
		qualityTier := sortedWithinTier(originalTier)
		qualityByTier[tierKey] = qualityTier
		if !sameCandidateOrder(originalTier, qualityTier) {
			qualityReorderedTierKeys = append(qualityReorderedTierKeys, tierKey)
		}
	}

	costOrdered := flattenTiers(baseTierOrder, qualityByTier)
	qualityOrdered := costOrdered
	if mode == HotQualityModeSpeedFirst {
		qualityOrdered = nil
		for _, latencyDegraded := range []bool{false, true} {
			for _, tierKey := range baseTierOrder {
				for _, candidate := range qualityByTier[tierKey] {
					if candidate.base.LatencyDegraded == latencyDegraded {
						qualityOrdered = append(qualityOrdered, candidate)
					}
				}
			}
		}
	}
	latencyDegradedOverrideApplied := mode == HotQualityModeSpeedFirst && !sameCandidateOrder(costOrdered, qualityOrdered)

	var baselinePrimary *indexedCandidate[T]
	if len(qualityOrdered) > 0 {
		baselinePrimary = qualityOrdered[0]
	}
	exploration, err := decideExploration(mode, normalized, qualityOrdered, baseTierOrder, input.Exploration)
	if err != nil {
		return nil, err
	}
	selected := exploration.selected
	if selected == nil {
		selected = baselinePrimary
	}
	finalOrdered := qualityOrdered
	if exploration.selected != nil {
		finalOrdered = moveBefore(qualityOrdered, exploration.selected)
	}
	selectionReason := SelectionReasonNoCandidate
	if selected != nil {
		if exploration.selected != nil {
			selectionReason = SelectionReasonSameTierExploration
		} else {
			selectionReason = SelectionReasonRankedPrimary
		}
	}

	decision := &HotQualityCandidateDecision[T]{
		DispatchIntent:           DispatchIntentPrimaryService,
		QualityOrderedCandidates: payloadsOf(qualityOrdered),
		OrderedCandidates:        payloadsOf(finalOrdered),
		Explanation: HotQualityCandidateSelectionExplanation{
			Mode:                           mode,
			RouteScopeKey:                  routeScopeKey,
			SelectionReason:                selectionReason,
			LatencyDegradedOverrideApplied: latencyDegradedOverrideApplied,
			QualityReorderedTierKeys:       qualityReorderedTierKeys,
			DuplicateRuntimeAccountIDs:     duplicateRuntimeAccountIDs,
			Exploration:                    exploration.explanation,
		},
	}
	if exploration.selected != nil {
		decision.DispatchIntent = DispatchIntentSameTierExploration
	}
	if baselinePrimary != nil {
		decision.Explanation.BaselinePrimaryAccountID = baselinePrimary.base.AccountID
	}
	if selected != nil {
		decision.SelectedCandidate = &selected.payload
		decision.Explanation.SelectedAccountID = selected.base.AccountID
		decision.Explanation.SelectedAccountRuntimeKey = selected.base.AccountRuntimeKey
		decision.Explanation.SelectedTierKey = selected.tierKey
		decision.Explanation.SelectedSampleState = selected.sampleState
		decision.Explanation.SelectedReliabilityLevel = selected.reliabilityLevel
	}
	return decision, nil
}

type explorationOutcome[T any] struct {
	selected    *indexedCandidate[T]
	explanation SameTierExplorationExplanation
}

// decideExploration mirrors decideExploration including its validation order:
// credit/cursor normalization happens before the status short-circuits once a
// state exists.
func decideExploration[T any](
	mode HotQualityRoutingMode,
	candidates []*indexedCandidate[T],
	qualityOrdered []*indexedCandidate[T],
	baseTierOrder []string,
	state *SameTierExplorationState,
) (explorationOutcome[T], error) {
	var primary *indexedCandidate[T]
	if len(qualityOrdered) > 0 {
		primary = qualityOrdered[0]
	}
	highestNormalTierKey := ""
	for _, tierKey := range baseTierOrder {
		for _, candidate := range candidates {
			if candidate.tierKey == tierKey {
				if !candidate.base.ConfigurationTier.FallbackEnabled {
					highestNormalTierKey = tierKey
				}
				break
			}
		}
		if highestNormalTierKey != "" {
			break
		}
	}
	eligibleCreditAccrual := state != nil &&
		state.EligibleFirstPrimaryDispatch &&
		!state.CreditAccrualAlreadyApplied &&
		!state.RequestAlreadyExplored &&
		!state.HasLeftHighestNormalTier &&
		primary != nil &&
		!primary.base.ConfigurationTier.FallbackEnabled &&
		primary.tierKey == highestNormalTierKey

	creditBefore := 0.0
	cursorBefore := int64(0)
	if state != nil {
		var err error
		creditBefore, err = normalizedCredit(state.Credit)
		if err != nil {
			return explorationOutcome[T]{}, err
		}
		cursorBefore, err = normalizedCursor(state.Cursor)
		if err != nil {
			return explorationOutcome[T]{}, err
		}
	}
	creditAccrued := 0.0
	if eligibleCreditAccrual {
		creditAccrued = SameTierExplorationCreditPerEligibleDispatch
	}
	creditAfterAccrual := roundedCredit(math.Min(SameTierExplorationCreditCap, creditBefore+creditAccrued))

	baseExplanation := func(status string) SameTierExplorationExplanation {
		return SameTierExplorationExplanation{
			Status:                        status,
			CreditBefore:                  creditBefore,
			CreditAccrued:                 creditAccrued,
			CreditAfterAccrual:            creditAfterAccrual,
			CreditSpendOnSuccessfulDispatch: 0,
			CreditAfterSuccessfulDispatch: creditAfterAccrual,
			CreditAfterFailedDispatch:     creditAfterAccrual,
			CursorBefore:                  cursorBefore,
			CursorAfterSuccessfulDispatch: cursorBefore,
			CursorAfterFailedDispatch:     cursorBefore,
			EligibleTargetAccountIDs:      []string{},
			FairCursorPeerAccountIDs:      []string{},
		}
	}
	if state == nil {
		return explorationOutcome[T]{explanation: baseExplanation(ExplorationStatusNotConfigured)}, nil
	}
	if primary == nil {
		return explorationOutcome[T]{explanation: baseExplanation(ExplorationStatusNoPrimaryCandidate)}, nil
	}
	if !state.Enabled {
		return explorationOutcome[T]{explanation: baseExplanation(ExplorationStatusDisabled)}, nil
	}
	if !state.EligibleFirstPrimaryDispatch {
		return explorationOutcome[T]{explanation: baseExplanation(ExplorationStatusIneligiblePrimaryDispatch)}, nil
	}
	if state.RequestAlreadyExplored {
		return explorationOutcome[T]{explanation: baseExplanation(ExplorationStatusRequestAlreadyExplored)}, nil
	}
	if primary.base.ConfigurationTier.FallbackEnabled {
		return explorationOutcome[T]{explanation: baseExplanation(ExplorationStatusFallbackTier)}, nil
	}
	if state.HasLeftHighestNormalTier || highestNormalTierKey == "" || primary.tierKey != highestNormalTierKey {
		return explorationOutcome[T]{explanation: baseExplanation(ExplorationStatusLeftHighestNormalTier)}, nil
	}
	if creditAfterAccrual < SameTierExplorationCreditCost {
		return explorationOutcome[T]{explanation: baseExplanation(ExplorationStatusInsufficientCredit)}, nil
	}

	inFlightKeys := map[string]bool{}
	for _, key := range state.TargetInFlightRuntimeKeys {
		normalized, err := requiredKey(key, "探索在途账号键")
		if err != nil {
			return explorationOutcome[T]{}, err
		}
		inFlightKeys[normalized] = true
	}
	nowMs, err := normalizedTimestamp(state.NowMs, "探索当前时间")
	if err != nil {
		return explorationOutcome[T]{}, err
	}
	knownSampleStaleAfterMs, err := normalizedNonNegativeInteger(state.KnownSampleStaleAfterMs, "known 样本过旧阈值")
	if err != nil {
		return explorationOutcome[T]{}, err
	}

	eligible := []*explorationRankedCandidate[T]{}
	for _, candidate := range candidates {
		if candidate.tierKey != primary.tierKey {
			continue
		}
		if candidate.base.AccountRuntimeKey == primary.base.AccountRuntimeKey {
			continue
		}
		if mode == HotQualityModeSpeedFirst && candidate.base.LatencyDegraded {
			continue
		}
		if inFlightKeys[candidate.base.AccountRuntimeKey] {
			continue
		}
		cooldownUntil := int64(0)
		if value, ok := state.TargetCooldownUntilMsByRuntimeKey[candidate.base.AccountRuntimeKey]; ok {
			cooldownUntil, err = normalizedCooldownUntil(value)
			if err != nil {
				return explorationOutcome[T]{}, err
			}
		}
		lastAttempt := int64(0)
		if candidate.base.LastExplorationAttemptAtMs != nil {
			lastAttempt = normalizedPastTimestamp(*candidate.base.LastExplorationAttemptAtMs, nowMs)
		}
		attemptCooldown := int64(0)
		if candidate.base.LastExplorationAttemptAtMs != nil {
			attemptCooldown = lastAttempt + SameTierExplorationTargetCooldownMs
		}
		if int64(math.Max(float64(cooldownUntil), float64(attemptCooldown))) > nowMs {
			continue
		}
		ranked := explorationRank(candidate, nowMs)
		if ranked.sampleRank >= 2 && nowMs-ranked.lastValidBusinessObservationAtMs < knownSampleStaleAfterMs {
			continue
		}
		eligible = append(eligible, ranked)
	}
	sort.SliceStable(eligible, func(left, right int) bool {
		return compareExplorationRank(eligible[left], eligible[right]) < 0
	})
	if len(eligible) == 0 {
		return explorationOutcome[T]{explanation: baseExplanation(ExplorationStatusNoEligibleTarget)}, nil
	}
	best := eligible[0]
	equallyPreferred := []*explorationRankedCandidate[T]{}
	for _, candidate := range eligible {
		if sameExplorationPriority(candidate, best) {
			equallyPreferred = append(equallyPreferred, candidate)
		}
	}
	selectedRanked := equallyPreferred[cursorBefore%int64(len(equallyPreferred))]
	selected := selectedRanked.indexed
	eligibleTargetAccountIDs := make([]string, 0, len(eligible))
	for _, candidate := range eligible {
		eligibleTargetAccountIDs = append(eligibleTargetAccountIDs, candidate.indexed.base.AccountID)
	}
	fairCursorPeerAccountIDs := make([]string, 0, len(equallyPreferred))
	for _, candidate := range equallyPreferred {
		fairCursorPeerAccountIDs = append(fairCursorPeerAccountIDs, candidate.indexed.base.AccountID)
	}
	cursorAfter := cursorBefore + 1
	if cursorBefore == maxSafeInteger {
		cursorAfter = 0
	}
	return explorationOutcome[T]{
		selected: selected,
		explanation: SameTierExplorationExplanation{
			Status:                          ExplorationStatusSelected,
			CreditBefore:                    creditBefore,
			CreditAccrued:                   creditAccrued,
			CreditAfterAccrual:              creditAfterAccrual,
			CreditSpendOnSuccessfulDispatch: SameTierExplorationCreditCost,
			CreditAfterSuccessfulDispatch:   roundedCredit(creditAfterAccrual - SameTierExplorationCreditCost),
			CreditAfterFailedDispatch:       creditAfterAccrual,
			CursorBefore:                    cursorBefore,
			CursorAfterSuccessfulDispatch:   cursorAfter,
			CursorAfterFailedDispatch:       cursorBefore,
			EligibleTargetAccountIDs:        eligibleTargetAccountIDs,
			FairCursorPeerAccountIDs:        fairCursorPeerAccountIDs,
			SelectedTargetAccountID:         selected.base.AccountID,
		},
	}, nil
}

func explorationRank[T any](candidate *indexedCandidate[T], nowMs int64) *explorationRankedCandidate[T] {
	sampleRank := 2
	if candidate.sampleState == SampleStateCold {
		sampleRank = 0
	} else if candidate.sampleState == SampleStateWarming {
		sampleRank = 1
	}
	qualityAttempts10m := 0
	if candidate.base.HotQuality != nil {
		qualityAttempts10m = candidate.base.HotQuality.Window10m.QualityAttempts
	}
	sampleGap := 3 - qualityAttempts10m
	if sampleGap < 0 {
		sampleGap = 0
	}
	return &explorationRankedCandidate[T]{
		indexed:                       candidate,
		sampleRank:                    sampleRank,
		sampleGap:                     sampleGap,
		lastValidBusinessObservationAtMs: lastValidBusinessObservationAtMs(candidate.base.HotQuality, nowMs),
		lastExplorationAttemptAtMs:    normalizedPastTimestamp(pointerInt64OrZero(candidate.base.LastExplorationAttemptAtMs), nowMs),
	}
}

func compareExplorationRank[T any](left, right *explorationRankedCandidate[T]) int {
	if left.sampleRank != right.sampleRank {
		return left.sampleRank - right.sampleRank
	}
	if left.sampleGap != right.sampleGap {
		return right.sampleGap - left.sampleGap
	}
	if left.lastValidBusinessObservationAtMs != right.lastValidBusinessObservationAtMs {
		if left.lastValidBusinessObservationAtMs < right.lastValidBusinessObservationAtMs {
			return -1
		}
		return 1
	}
	if left.lastExplorationAttemptAtMs != right.lastExplorationAttemptAtMs {
		if left.lastExplorationAttemptAtMs < right.lastExplorationAttemptAtMs {
			return -1
		}
		return 1
	}
	return strings.Compare(left.indexed.base.AccountID, right.indexed.base.AccountID)
}

func sameExplorationPriority[T any](left, right *explorationRankedCandidate[T]) bool {
	return left.sampleRank == right.sampleRank &&
		left.sampleGap == right.sampleGap &&
		left.lastValidBusinessObservationAtMs == right.lastValidBusinessObservationAtMs &&
		left.lastExplorationAttemptAtMs == right.lastExplorationAttemptAtMs
}

// compareWithinTier mirrors compareWithinTier.
func compareWithinTier[T any](left, right *indexedCandidate[T]) int {
	reliability := reliabilityRank(left.reliabilityLevel) - reliabilityRank(right.reliabilityLevel)
	if reliability != 0 {
		return reliability
	}
	effective := effectiveReliabilityForOrdering(right) - effectiveReliabilityForOrdering(left)
	if effective != 0 {
		if effective > 0 {
			return 1
		}
		return -1
	}
	if left.sampleState != SampleStateCold && right.sampleState != SampleStateCold {
		speed := compareSpeed(left.base.HotQuality, right.base.HotQuality)
		if speed != 0 {
			return speed
		}
	}
	if left.base.StableBindingOrder != right.base.StableBindingOrder {
		return left.base.StableBindingOrder - right.base.StableBindingOrder
	}
	if cmp := strings.Compare(left.base.AccountID, right.base.AccountID); cmp != 0 {
		return cmp
	}
	return left.originalIndex - right.originalIndex
}

// compareSpeed mirrors compareSpeed.
func compareSpeed(left, right *HotQualitySnapshot) int {
	leftEwma, leftOK := normalizedOptionalDuration(left, func(snapshot *HotQualitySnapshot) *float64 { return snapshot.FirstByteEwma5m })
	rightEwma, rightOK := normalizedOptionalDuration(right, func(snapshot *HotQualitySnapshot) *float64 { return snapshot.FirstByteEwma5m })
	if leftOK && rightOK && leftEwma != rightEwma {
		if leftEwma < rightEwma {
			return -1
		}
		return 1
	}
	leftP95, leftP95OK := normalizedOptionalDuration(left, func(snapshot *HotQualitySnapshot) *float64 { return snapshot.FirstByteP95Bucket10m })
	rightP95, rightP95OK := normalizedOptionalDuration(right, func(snapshot *HotQualitySnapshot) *float64 { return snapshot.FirstByteP95Bucket10m })
	if leftP95OK && rightP95OK && leftP95 != rightP95 {
		if leftP95 < rightP95 {
			return -1
		}
		return 1
	}
	return 0
}

// normalizeCandidates mirrors normalizeCandidates (duplicate runtime keys are
// reported, scope mismatches raise the RangeError).
func normalizeCandidates[T any](
	candidates []T,
	base func(T) HotQualityCandidate,
	routeScopeKey string,
) ([]*indexedCandidate[T], []string, error) {
	seenRuntimeKeys := map[string]bool{}
	normalized := []*indexedCandidate[T]{}
	duplicateRuntimeAccountIDs := []string{}
	for originalIndex, payload := range candidates {
		candidate := base(payload)
		accountID, err := requiredKey(candidate.AccountID, "账号 ID")
		if err != nil {
			return nil, nil, err
		}
		accountRuntimeKey, err := requiredKey(candidate.AccountRuntimeKey, "账号运行态键")
		if err != nil {
			return nil, nil, err
		}
		candidateRouteScopeKey, err := requiredKey(candidate.RouteScopeKey, "候选路由范围")
		if err != nil {
			return nil, nil, err
		}
		if candidateRouteScopeKey != routeScopeKey {
			return nil, nil, rangeError("候选账号 %s 不属于当前路由范围", accountID)
		}
		if _, err := normalizedNonNegativeInteger(int64(candidate.StableBindingOrder), "稳定绑定顺序"); err != nil {
			return nil, nil, err
		}
		tierKey, err := GatewayAccountConfigurationTierKey(candidate.ConfigurationTier)
		if err != nil {
			return nil, nil, err
		}
		if seenRuntimeKeys[accountRuntimeKey] {
			duplicateRuntimeAccountIDs = append(duplicateRuntimeAccountIDs, accountID)
			continue
		}
		seenRuntimeKeys[accountRuntimeKey] = true
		sampleState := SampleStateCold
		if candidate.HotQuality != nil && candidate.HotQuality.SampleState != "" {
			sampleState = candidate.HotQuality.SampleState
		}
		reliabilityLevel := ReliabilityUnknown
		if sampleState != SampleStateCold && candidate.HotQuality != nil && candidate.HotQuality.ReliabilityLevel != "" {
			reliabilityLevel = candidate.HotQuality.ReliabilityLevel
		}
		normalized = append(normalized, &indexedCandidate[T]{
			payload:          payload,
			base:             candidate,
			originalIndex:    originalIndex,
			tierKey:          tierKey,
			sampleState:      sampleState,
			reliabilityLevel: reliabilityLevel,
		})
	}
	return normalized, duplicateRuntimeAccountIDs, nil
}

func reliabilityRank(level HotQualityReliabilityLevel) int {
	if level == ReliabilityHealthy {
		return 0
	}
	if level == ReliabilityUncertain {
		return 1
	}
	if level == ReliabilityUnknown {
		return 2
	}
	return 3
}

func lastValidBusinessObservationAtMs(snapshot *HotQualitySnapshot, nowMs int64) int64 {
	if snapshot == nil {
		return 0
	}
	maxMs := int64(0)
	for _, window := range []HotQualityWindowSnapshot{snapshot.Window30m, snapshot.Window10m, snapshot.Window5m} {
		maxMs = int64(math.Max(float64(maxMs), float64(normalizedPastTimestamp(pointerInt64OrZero(window.LastCompletedAtMs), nowMs))))
		maxMs = int64(math.Max(float64(maxMs), float64(normalizedPastTimestamp(pointerInt64OrZero(window.LastFailureAtMs), nowMs))))
	}
	return maxMs
}

func moveBefore[T any](candidates []*indexedCandidate[T], selected *indexedCandidate[T]) []*indexedCandidate[T] {
	ordered := []*indexedCandidate[T]{selected}
	for _, candidate := range candidates {
		if candidate.originalIndex != selected.originalIndex {
			ordered = append(ordered, candidate)
		}
	}
	return ordered
}

func sameCandidateOrder[T any](left, right []*indexedCandidate[T]) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].originalIndex != right[index].originalIndex {
			return false
		}
	}
	return true
}

func distinctTierKeys[T any](candidates []*indexedCandidate[T]) []string {
	seen := map[string]bool{}
	keys := []string{}
	for _, candidate := range candidates {
		if !seen[candidate.tierKey] {
			seen[candidate.tierKey] = true
			keys = append(keys, candidate.tierKey)
		}
	}
	return keys
}

func candidatesOfTier[T any](candidates []*indexedCandidate[T], tierKey string) []*indexedCandidate[T] {
	members := []*indexedCandidate[T]{}
	for _, candidate := range candidates {
		if candidate.tierKey == tierKey {
			members = append(members, candidate)
		}
	}
	return members
}

func flattenTiers[T any](tierOrder []string, qualityByTier map[string][]*indexedCandidate[T]) []*indexedCandidate[T] {
	flattened := []*indexedCandidate[T]{}
	for _, tierKey := range tierOrder {
		flattened = append(flattened, qualityByTier[tierKey]...)
	}
	return flattened
}

func payloadsOf[T any](candidates []*indexedCandidate[T]) []T {
	payloads := make([]T, 0, len(candidates))
	for _, candidate := range candidates {
		payloads = append(payloads, candidate.payload)
	}
	return payloads
}

func sortedWithinTier[T any](tier []*indexedCandidate[T]) []*indexedCandidate[T] {
	sorted := append([]*indexedCandidate[T]{}, tier...)
	sort.SliceStable(sorted, func(left, right int) bool {
		return compareWithinTier(sorted[left], sorted[right]) < 0
	})
	return sorted
}

func normalizedMode(value HotQualityRoutingMode) (HotQualityRoutingMode, error) {
	if value != HotQualityModeCostFirst && value != HotQualityModeSpeedFirst {
		return "", typeError("热质量路由模式无效")
	}
	return value, nil
}

func requiredKey(value string, name string) (string, error) {
	normalized := strings.TrimSpace(value)
	if normalized == "" {
		return "", typeError("%s不能为空", name)
	}
	return normalized, nil
}

func normalizedCredit(value float64) (float64, error) {
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > SameTierExplorationCreditCap {
		return 0, rangeError("同层探索 credit 必须位于 0..1")
	}
	return roundedCredit(value), nil
}

func roundedCredit(value float64) float64 {
	return math.Round(value*1_000_000) / 1_000_000
}

func normalizedCursor(value int64) (int64, error) {
	return normalizedNonNegativeInteger(value, "同层探索 cursor")
}

func normalizedSafeInteger(value int64, name string) (int64, error) {
	if value > maxSafeInteger || value < -maxSafeInteger {
		return 0, rangeError("%s 必须是安全整数", name)
	}
	return value, nil
}

func normalizedNonNegativeInteger(value int64, name string) (int64, error) {
	normalized, err := normalizedSafeInteger(value, name)
	if err != nil {
		return 0, err
	}
	if normalized < 0 {
		return 0, rangeError("%s 不能为负数", name)
	}
	return normalized, nil
}

func normalizedTimestamp(value int64, name string) (int64, error) {
	if value > maxSafeInteger || value < -maxSafeInteger {
		return 0, rangeError("%s 必须是有限数值", name)
	}
	return value, nil
}

func normalizedPastTimestamp(value int64, nowMs int64) int64 {
	if value > nowMs {
		return nowMs
	}
	if value < 0 {
		return 0
	}
	return value
}

func normalizedCooldownUntil(value int64) (int64, error) {
	if value > maxSafeInteger || value < -maxSafeInteger {
		return 0, rangeError("探索冷却截止时间必须是有限数值")
	}
	return value, nil
}

func normalizedReliability(value float64, present bool) float64 {
	if !present || math.IsNaN(value) || math.IsInf(value, 0) {
		return 0.5
	}
	return math.Max(0, math.Min(1, value))
}

func effectiveReliabilityForOrdering[T any](candidate *indexedCandidate[T]) float64 {
	if candidate.sampleState == SampleStateCold {
		return 0.5
	}
	if candidate.base.HotQuality == nil {
		return normalizedReliability(0, false)
	}
	return normalizedReliability(candidate.base.HotQuality.EffectiveReliability, true)
}

func normalizedOptionalDuration(snapshot *HotQualitySnapshot, selector func(*HotQualitySnapshot) *float64) (float64, bool) {
	if snapshot == nil {
		return 0, false
	}
	value := selector(snapshot)
	if value == nil || math.IsNaN(*value) || math.IsInf(*value, 0) || *value < 0 {
		return 0, false
	}
	return *value, true
}

func pointerInt64OrZero(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}
