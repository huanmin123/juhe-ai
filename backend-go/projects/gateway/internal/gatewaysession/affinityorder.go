package gatewaysession

import (
	"context"
	"math"
	"sort"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayusage"
)

// Account ordering under session affinity, migrated from
// runtime/session-affinity.service.ts (git HEAD).

// DispatchOrderingOptions mirrors OpenAIAccountDispatchOrderingOptions.
type DispatchOrderingOptions struct {
	GroupType             string
	SchedulingPolicy      *gatewayruntimecache.GroupSchedulingPolicy
	ModelPriority         *GatewayAccountModelPriority
	TrafficMigrationScope *OpenAIGatewaySessionAffinityScope
}

// BusyLaneOptions mirrors OpenAIAccountDispatchOrderingOptions & { requestLane }.
type BusyLaneOptions struct {
	DispatchOrderingOptions
	RequestLane string
}

func accountsIDs(accounts []gatewayruntimecache.OpenAIAccountSecret) []string {
	ids := make([]string, 0, len(accounts))
	for _, account := range accounts {
		ids = append(ids, account.ID)
	}
	return ids
}

// OrderOpenAIAccountsBySessionAffinity mirrors orderOpenAIAccountsBySessionAffinity
// (the synchronous, process-local driver path). The retired high_concurrency
// type is gone; a non-default defaultSoftConcurrency enables the unified
// hardBusy/softBusy ordering, otherwise the existing personal binding path is
// preserved byte-for-byte.
func (s *AffinityService) OrderOpenAIAccountsBySessionAffinity(accounts []gatewayruntimecache.OpenAIAccountSecret, sessionAffinityKey string, options DispatchOrderingOptions) ([]gatewayruntimecache.OpenAIAccountSecret, error) {
	modelOrderedAccounts := orderOpenAIAccountsByModelPriority(accounts, options.ModelPriority)
	trafficMigrationPreference := s.trafficMigrationPreferenceForAccounts(accountsIDs(modelOrderedAccounts), options.TrafficMigrationScope)
	sessionTrafficMigrationTargetAccountID := ""
	if trafficMigrationPreference == nil {
		sessionTrafficMigrationTargetAccountID = s.sessionTrafficMigrationTargetForAccounts(accountsIDs(modelOrderedAccounts), sessionAffinityKey)
	}
	trafficMigrationTargetAccountID := sessionTrafficMigrationTargetAccountID
	if trafficMigrationPreference != nil {
		trafficMigrationTargetAccountID = trafficMigrationPreference.TargetAccountID
	}
	preferenceOrderedAccounts := orderOpenAIAccountsByTrafficMigrationPreference(modelOrderedAccounts, trafficMigrationTargetAccountID, options.ModelPriority)
	policy, err := ResolveGroupSchedulingPolicy(options.SchedulingPolicy)
	if err != nil {
		return nil, err
	}
	if policy.DefaultSoftConcurrency <= 0 {
		if trafficMigrationTargetAccountID != "" {
			return preferenceOrderedAccounts, nil
		}
		return s.orderOpenAIPersonalAccountsBySessionAffinity(preferenceOrderedAccounts, sessionAffinityKey, options.ModelPriority), nil
	}
	var binding *SessionBinding
	if sessionAffinityKey != "" {
		s.mu.Lock()
		binding = s.sessionAffinityCacheGetLocked(sessionAffinityKey)
		s.mu.Unlock()
	}
	return s.orderAccountsBySoftConcurrency(preferenceOrderedAccounts, binding, policy, trafficMigrationTargetAccountID, options.ModelPriority), nil
}

// OrderOpenAIAccountsBySessionAffinityAsync mirrors
// orderOpenAIAccountsBySessionAffinityAsync (Redis bindings when configured).
// SoftBusy uses the request-frozen candidate snapshot; no in-flight stats or
// extra Redis read is required.
func (s *AffinityService) OrderOpenAIAccountsBySessionAffinityAsync(ctx context.Context, accounts []gatewayruntimecache.OpenAIAccountSecret, sessionAffinityKey string, options DispatchOrderingOptions) ([]gatewayruntimecache.OpenAIAccountSecret, error) {
	if s.cfg.CacheDriver != CacheDriverRedis {
		return s.OrderOpenAIAccountsBySessionAffinity(accounts, sessionAffinityKey, options)
	}
	modelOrderedAccounts := orderOpenAIAccountsByModelPriority(accounts, options.ModelPriority)
	ids := accountsIDs(modelOrderedAccounts)
	trafficMigrationPreference := s.trafficMigrationPreferenceForAccountsAsync(ctx, ids, options.TrafficMigrationScope)
	sessionBinding := s.getRedisSessionAffinityBindingForOrdering(ctx, sessionAffinityKey)
	sessionTrafficMigrationTargetAccountID := ""
	if trafficMigrationPreference == nil {
		sessionTrafficMigrationTargetAccountID = sessionTrafficMigrationTargetForAccountsFromBinding(ids, sessionBinding)
	}
	trafficMigrationTargetAccountID := sessionTrafficMigrationTargetAccountID
	if trafficMigrationPreference != nil {
		trafficMigrationTargetAccountID = trafficMigrationPreference.TargetAccountID
	}
	preferenceOrderedAccounts := orderOpenAIAccountsByTrafficMigrationPreference(modelOrderedAccounts, trafficMigrationTargetAccountID, options.ModelPriority)
	policy, err := ResolveGroupSchedulingPolicy(options.SchedulingPolicy)
	if err != nil {
		return nil, err
	}
	if policy.DefaultSoftConcurrency <= 0 {
		if trafficMigrationTargetAccountID != "" {
			return preferenceOrderedAccounts, nil
		}
		return s.orderOpenAIPersonalAccountsBySessionBinding(preferenceOrderedAccounts, sessionBinding, options.ModelPriority), nil
	}
	return s.orderAccountsBySoftConcurrency(preferenceOrderedAccounts, sessionBinding, policy, trafficMigrationTargetAccountID, options.ModelPriority), nil
}

// orderOpenAIPersonalAccountsBySessionAffinity mirrors
// orderOpenAIPersonalAccountsBySessionAffinity.
func (s *AffinityService) orderOpenAIPersonalAccountsBySessionAffinity(accounts []gatewayruntimecache.OpenAIAccountSecret, sessionAffinityKey string, modelPriority *GatewayAccountModelPriority) []gatewayruntimecache.OpenAIAccountSecret {
	if sessionAffinityKey == "" || len(accounts) < 2 {
		return accounts
	}
	s.mu.Lock()
	binding := s.sessionAffinityCacheGetLocked(sessionAffinityKey)
	s.mu.Unlock()
	return s.orderOpenAIPersonalAccountsBySessionBinding(accounts, binding, modelPriority)
}

// orderOpenAIPersonalAccountsBySessionBinding mirrors
// orderOpenAIPersonalAccountsBySessionBinding: the bound account is moved to
// the front of its dispatch tier; peer relative order is preserved. An expired
// lease is treated as no binding.
func (s *AffinityService) orderOpenAIPersonalAccountsBySessionBinding(accounts []gatewayruntimecache.OpenAIAccountSecret, binding *SessionBinding, modelPriority *GatewayAccountModelPriority) []gatewayruntimecache.OpenAIAccountSecret {
	if len(accounts) < 2 {
		return accounts
	}
	if binding == nil || !binding.LeaseActive(s.clock().UnixMilli()) {
		return accounts
	}
	boundIndex := -1
	for index, account := range accounts {
		if account.ID == binding.AccountID {
			boundIndex = index
			break
		}
	}
	if boundIndex <= 0 {
		return accounts
	}
	boundAccount := accounts[boundIndex]
	targetIndex := boundIndex
	for index := boundIndex - 1; index >= 0; index-- {
		if !canSessionAffinityPromoteOver(boundAccount, accounts[index], modelPriority) {
			break
		}
		targetIndex = index
	}
	if targetIndex == boundIndex {
		return accounts
	}
	ordered := make([]gatewayruntimecache.OpenAIAccountSecret, 0, len(accounts))
	ordered = append(ordered, accounts[:targetIndex]...)
	ordered = append(ordered, boundAccount)
	ordered = append(ordered, accounts[targetIndex:boundIndex]...)
	ordered = append(ordered, accounts[boundIndex+1:]...)
	// 同层 owner 被从后面提到同层首位：没有亲和时排在前面的 peer 会继续承接，
	// 这次实际重排即为一次“回切被阻止”。每轮排序最多 +1。
	gatewayusage.RecordGatewaySessionAffinityBackSwitchPreventedMetric()
	return ordered
}

// canSessionAffinityRotateWithinSameTier mirrors
// canSessionAffinityRotateWithinSameTier.
func canSessionAffinityRotateWithinSameTier(boundAccount gatewayruntimecache.OpenAIAccountSecret, currentAccount gatewayruntimecache.OpenAIAccountSecret, modelPriority *GatewayAccountModelPriority) bool {
	return canSessionAffinityPromoteOver(boundAccount, currentAccount, modelPriority) &&
		canSessionAffinityPromoteOver(currentAccount, boundAccount, modelPriority)
}

// softConcurrencyCandidate is the unified hardBusy/softBusy ordering row. It
// deliberately carries no queue/slow-request keys: the retired high-concurrency
// mechanisms are not part of the ordinary-group contract.
type softConcurrencyCandidate struct {
	account                   gatewayruntimecache.OpenAIAccountSecret
	index                     int
	currentConcurrency        int
	hardLimit                 int
	softLimit                 int
	affinityAllowed           bool
	trafficMigrationPreferred bool
	hardBusy                  bool
	softBusy                  bool
}

// orderAccountsBySoftConcurrency applies the ordinary-group soft threshold.
// Tier keys (model priority/traffic/fallback/super/priority) stay ahead of
// softBusy, so load-shedding never crosses a dispatch tier; within a tier the
// owner yields when softBusy and healthy peers come first.
func (s *AffinityService) orderAccountsBySoftConcurrency(accounts []gatewayruntimecache.OpenAIAccountSecret, binding *SessionBinding, policy *SchedulingPolicyValues, trafficMigrationTargetAccountID string, modelPriority *GatewayAccountModelPriority) []gatewayruntimecache.OpenAIAccountSecret {
	if len(accounts) < 2 {
		return accounts
	}
	nowMs := s.clock().UnixMilli()
	leaseActive := binding != nil && binding.LeaseActive(nowMs)
	originalOwnerIndex := -1
	if leaseActive {
		for index, account := range accounts {
			if account.ID == binding.AccountID {
				originalOwnerIndex = index
				break
			}
		}
	}
	candidates := make([]softConcurrencyCandidate, 0, len(accounts))
	for index, account := range accounts {
		currentConcurrency := accountCurrentConcurrency(account, nil)
		hardLimit := accountHardConcurrencyLimit(account)
		softLimit := policy.SoftLimit(hardLimit)
		hardBusy := currentConcurrency >= hardLimit
		softBusy := policy.SoftBusy(currentConcurrency, hardLimit)
		boundToSession := leaseActive && binding.AccountID == account.ID
		candidates = append(candidates, softConcurrencyCandidate{
			account:                   account,
			index:                     index,
			currentConcurrency:        currentConcurrency,
			hardLimit:                 hardLimit,
			softLimit:                 softLimit,
			affinityAllowed:           boundToSession && !hardBusy && !softBusy,
			trafficMigrationPreferred: account.ID == trafficMigrationTargetAccountID,
			hardBusy:                  hardBusy,
			softBusy:                  softBusy,
		})
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		return compareSoftConcurrencyCandidates(candidates[i], candidates[j], modelPriority) < 0
	})
	ordered := make([]gatewayruntimecache.OpenAIAccountSecret, 0, len(candidates))
	for _, candidate := range candidates {
		ordered = append(ordered, candidate.account)
	}
	if leaseActive && originalOwnerIndex > 0 {
		finalOwnerIndex := -1
		for index, account := range ordered {
			if account.ID == binding.AccountID {
				finalOwnerIndex = index
				break
			}
		}
		if finalOwnerIndex >= 0 && finalOwnerIndex < originalOwnerIndex &&
			sessionAffinityOwnerFirstInLayer(ordered, binding.AccountID, modelPriority) {
			gatewayusage.RecordGatewaySessionAffinityBackSwitchPreventedMetric()
		}
	}
	return ordered
}

// sessionAffinityOwnerFirstInLayer 报告 owner 是否已是其四元调度层内的首个
// 候选；用于区分“亲和把 owner 留在本层首位”和跨层候选本就排在前面的情况。
func sessionAffinityOwnerFirstInLayer(accounts []gatewayruntimecache.OpenAIAccountSecret, ownerID string, modelPriority *GatewayAccountModelPriority) bool {
	ownerIndex := -1
	for index, account := range accounts {
		if account.ID == ownerID {
			ownerIndex = index
			break
		}
	}
	if ownerIndex < 0 {
		return false
	}
	owner := accounts[ownerIndex]
	for index := 0; index < ownerIndex; index++ {
		peer := accounts[index]
		if canSessionAffinityPromoteOver(owner, peer, modelPriority) &&
			canSessionAffinityPromoteOver(peer, owner, modelPriority) {
			return false
		}
	}
	return true
}

func compareSoftConcurrencyCandidates(left softConcurrencyCandidate, right softConcurrencyCandidate, modelPriority *GatewayAccountModelPriority) int {
	if left.hardBusy != right.hardBusy {
		if left.hardBusy {
			return 1
		}
		return -1
	}
	if delta := CompareGatewayAccountModelPriority(left.account.ID, right.account.ID, modelPriority); delta != 0 {
		return sign(delta)
	}
	if left.trafficMigrationPreferred != right.trafficMigrationPreferred {
		if left.trafficMigrationPreferred {
			return -1
		}
		return 1
	}
	if fallbackDelta := accountFallbackRank(left.account) - accountFallbackRank(right.account); fallbackDelta != 0 {
		return sign(fallbackDelta)
	}
	if left.account.SuperPriorityEnabled != right.account.SuperPriorityEnabled {
		if left.account.SuperPriorityEnabled {
			return -1
		}
		return 1
	}
	if left.account.Priority != right.account.Priority {
		return sign(left.account.Priority - right.account.Priority)
	}
	// 同层 latch：健康 owner 优先于更空闲/质量更高的 peer；softBusy 已在前，
	// owner 达到软阈值时让位。affinityAllowed 必须在 load/quality 之前。
	if left.affinityAllowed != right.affinityAllowed {
		if left.affinityAllowed {
			return -1
		}
		return 1
	}
	if left.softBusy != right.softBusy {
		if left.softBusy {
			return 1
		}
		return -1
	}
	leftRatio := softConcurrencyLoadRatio(left)
	rightRatio := softConcurrencyLoadRatio(right)
	loadRatioDelta := leftRatio - rightRatio
	if math.Abs(loadRatioDelta) > 0.000001 {
		if loadRatioDelta < 0 {
			return -1
		}
		return 1
	}
	if left.currentConcurrency != right.currentConcurrency {
		return sign(left.currentConcurrency - right.currentConcurrency)
	}
	if delta := compareAccountQualityBand(left.account, right.account); delta != 0 {
		return delta
	}
	return sign(left.index - right.index)
}

func softConcurrencyLoadRatio(candidate softConcurrencyCandidate) float64 {
	denominator := candidate.softLimit
	if denominator <= 0 {
		denominator = candidate.hardLimit
	}
	if denominator <= 0 {
		denominator = 1
	}
	return float64(candidate.currentConcurrency) / float64(denominator)
}

func sign(value int) int {
	if value < 0 {
		return -1
	}
	if value > 0 {
		return 1
	}
	return 0
}

// canSessionAffinityPromoteOver mirrors canSessionAffinityPromoteOver.
// canSessionAffinityPromoteOver mirrors canSessionAffinityPromoteOver with the
// same-layer latch contract: model priority / super / fallback / priority are
// the dispatch layer; within the same layer the bound account wins regardless
// of quality rank or load. Quality is only a successor-selection preference
// (see the account-affinity latch design), not a promotion blocker.
func canSessionAffinityPromoteOver(boundAccount gatewayruntimecache.OpenAIAccountSecret, currentAccount gatewayruntimecache.OpenAIAccountSecret, modelPriority *GatewayAccountModelPriority) bool {
	modelPriorityDelta := CompareGatewayAccountModelPriority(boundAccount.ID, currentAccount.ID, modelPriority)
	if modelPriorityDelta > 0 {
		return false
	}
	if modelPriorityDelta < 0 {
		return true
	}
	if boundAccount.SuperPriorityEnabled != currentAccount.SuperPriorityEnabled {
		return false
	}
	if boundAccount.FallbackEnabled != currentAccount.FallbackEnabled {
		return false
	}
	if boundAccount.Priority != currentAccount.Priority {
		return false
	}
	return true
}

// orderOpenAIAccountsByTrafficMigrationPreference mirrors
// orderOpenAIAccountsByTrafficMigrationPreference.
func orderOpenAIAccountsByTrafficMigrationPreference(accounts []gatewayruntimecache.OpenAIAccountSecret, targetAccountID string, modelPriority *GatewayAccountModelPriority) []gatewayruntimecache.OpenAIAccountSecret {
	if targetAccountID == "" || len(accounts) < 2 {
		return accounts
	}
	originalTargetIndex := -1
	for index, account := range accounts {
		if account.ID == targetAccountID {
			originalTargetIndex = index
			break
		}
	}
	if originalTargetIndex <= 0 {
		return accounts
	}
	targetAccount := accounts[originalTargetIndex]
	targetIndex := originalTargetIndex
	for index := originalTargetIndex - 1; index >= 0; index-- {
		if CompareGatewayAccountModelPriority(targetAccount.ID, accounts[index].ID, modelPriority) > 0 {
			break
		}
		targetIndex = index
	}
	if targetIndex == originalTargetIndex {
		return accounts
	}
	ordered := make([]gatewayruntimecache.OpenAIAccountSecret, 0, len(accounts))
	ordered = append(ordered, accounts[:targetIndex]...)
	ordered = append(ordered, targetAccount)
	ordered = append(ordered, accounts[targetIndex:originalTargetIndex]...)
	ordered = append(ordered, accounts[originalTargetIndex+1:]...)
	return ordered
}
