package gatewaysession

import (
	"context"
	"math"
	"sort"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// Account ordering under session affinity, migrated from
// runtime/session-affinity.service.ts (git HEAD).

// DispatchOrderingOptions mirrors OpenAIAccountDispatchOrderingOptions.
type DispatchOrderingOptions struct {
	GroupType             string
	SchedulingPolicy      map[string]any
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
// (the synchronous, process-local driver path).
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
	if options.GroupType == GroupTypeHighConcurrency {
		return s.orderOpenAIHighConcurrencyAccounts(preferenceOrderedAccounts, sessionAffinityKey, options.SchedulingPolicy, trafficMigrationTargetAccountID, options.ModelPriority)
	}
	if trafficMigrationTargetAccountID != "" {
		return preferenceOrderedAccounts, nil
	}
	return s.orderOpenAIPersonalAccountsBySessionAffinity(preferenceOrderedAccounts, sessionAffinityKey, options.ModelPriority), nil
}

// OrderOpenAIAccountsBySessionAffinityAsync mirrors
// orderOpenAIAccountsBySessionAffinityAsync (Redis bindings and in-flight
// stats when the drivers ask for them).
func (s *AffinityService) OrderOpenAIAccountsBySessionAffinityAsync(ctx context.Context, accounts []gatewayruntimecache.OpenAIAccountSecret, sessionAffinityKey string, options DispatchOrderingOptions) ([]gatewayruntimecache.OpenAIAccountSecret, error) {
	if s.cfg.CacheDriver != CacheDriverRedis && options.GroupType != GroupTypeHighConcurrency {
		return s.OrderOpenAIAccountsBySessionAffinity(accounts, sessionAffinityKey, options)
	}
	modelOrderedAccounts := orderOpenAIAccountsByModelPriority(accounts, options.ModelPriority)
	ids := accountsIDs(modelOrderedAccounts)
	var (
		trafficMigrationPreference *TrafficMigrationPreference
		sessionBinding             *SessionBinding
	)
	if s.cfg.CacheDriver == CacheDriverRedis {
		trafficMigrationPreference = s.trafficMigrationPreferenceForAccountsAsync(ctx, ids, options.TrafficMigrationScope)
		sessionBinding = s.getRedisSessionAffinityBindingForOrdering(ctx, sessionAffinityKey)
	} else {
		trafficMigrationPreference = s.trafficMigrationPreferenceForAccounts(ids, options.TrafficMigrationScope)
		if sessionAffinityKey != "" {
			s.mu.Lock()
			sessionBinding = s.sessionAffinityCacheGetLocked(sessionAffinityKey)
			s.mu.Unlock()
		}
	}
	sessionTrafficMigrationTargetAccountID := ""
	if trafficMigrationPreference == nil {
		sessionTrafficMigrationTargetAccountID = sessionTrafficMigrationTargetForAccountsFromBinding(ids, sessionBinding)
	}
	trafficMigrationTargetAccountID := sessionTrafficMigrationTargetAccountID
	if trafficMigrationPreference != nil {
		trafficMigrationTargetAccountID = trafficMigrationPreference.TargetAccountID
	}
	preferenceOrderedAccounts := orderOpenAIAccountsByTrafficMigrationPreference(modelOrderedAccounts, trafficMigrationTargetAccountID, options.ModelPriority)
	if options.GroupType == GroupTypeHighConcurrency {
		return s.orderOpenAIHighConcurrencyAccountsAsync(ctx, preferenceOrderedAccounts, sessionAffinityKey, options.SchedulingPolicy, trafficMigrationTargetAccountID, options.ModelPriority, sessionBinding)
	}
	if trafficMigrationTargetAccountID != "" {
		return preferenceOrderedAccounts, nil
	}
	return s.orderOpenAIPersonalAccountsBySessionBinding(preferenceOrderedAccounts, sessionBinding, options.ModelPriority), nil
}

// AreOpenAIHighConcurrencyAccountsHardBusy mirrors
// areOpenAIHighConcurrencyAccountsHardBusy.
func (s *AffinityService) AreOpenAIHighConcurrencyAccountsHardBusy(accounts []gatewayruntimecache.OpenAIAccountSecret, options DispatchOrderingOptions) bool {
	if options.GroupType != GroupTypeHighConcurrency || len(accounts) == 0 {
		return false
	}
	for _, account := range accounts {
		if accountCurrentConcurrency(account, nil) < accountHardConcurrencyLimit(account) {
			return false
		}
	}
	return true
}

// AreOpenAIHighConcurrencyAccountsBusyForLane mirrors
// areOpenAIHighConcurrencyAccountsBusyForLane.
func (s *AffinityService) AreOpenAIHighConcurrencyAccountsBusyForLane(accounts []gatewayruntimecache.OpenAIAccountSecret, options BusyLaneOptions) (bool, error) {
	if options.GroupType != GroupTypeHighConcurrency || len(accounts) == 0 {
		return false, nil
	}
	if s.cfg.Concurrency == nil {
		return false, nil
	}
	for _, account := range accounts {
		hardLimit := accountHardConcurrencyLimit(account)
		currentConcurrency := accountCurrentConcurrency(account, nil)
		if currentConcurrency >= hardLimit {
			return true, nil
		}
		if options.RequestLane != RequestLaneImage {
			continue
		}
		imageLaneLimit, err := EffectiveImageLaneConcurrencyLimit(int64(hardLimit), options.SchedulingPolicy, s.schedulingDefaults)
		if err != nil {
			return false, err
		}
		if s.cfg.Concurrency.GetAccountCurrentConcurrency(GatewayAccountConcurrencyAccountID(GatewayAccountConcurrencyIdentityOf(account)), RequestLaneImage) >= int(imageLaneLimit) {
			return true, nil
		}
	}
	return false, nil
}

// AreOpenAIHighConcurrencyAccountsBusyForLaneAsync mirrors
// areOpenAIHighConcurrencyAccountsBusyForLaneAsync.
func (s *AffinityService) AreOpenAIHighConcurrencyAccountsBusyForLaneAsync(ctx context.Context, accounts []gatewayruntimecache.OpenAIAccountSecret, options BusyLaneOptions) (bool, error) {
	if s.cfg.RuntimeStateDriver != RuntimeStateDriverRedis {
		return s.AreOpenAIHighConcurrencyAccountsBusyForLane(accounts, options)
	}
	if options.GroupType != GroupTypeHighConcurrency || len(accounts) == 0 {
		return false, nil
	}
	if s.cfg.Concurrency == nil {
		return false, nil
	}
	accountIDs := GatewayAccountConcurrencyAccountIDs(GatewayAccountConcurrencyIdentities(accounts))
	totalConcurrency, err := s.cfg.Concurrency.LoadAccountCurrentConcurrencyByIDsAsync(ctx, accountIDs, "")
	if err != nil {
		return false, err
	}
	var imageLaneConcurrency map[string]int
	if options.RequestLane == RequestLaneImage {
		imageLaneConcurrency, err = s.cfg.Concurrency.LoadAccountCurrentConcurrencyByIDsAsync(ctx, accountIDs, RequestLaneImage)
		if err != nil {
			return false, err
		}
	}
	for _, account := range accounts {
		hardLimit := accountHardConcurrencyLimit(account)
		concurrencyAccountID := GatewayAccountConcurrencyAccountID(GatewayAccountConcurrencyIdentityOf(account))
		runtimeValue := ptrIntFromMap(totalConcurrency, concurrencyAccountID)
		currentConcurrency := accountCurrentConcurrency(account, runtimeValue)
		if currentConcurrency >= hardLimit {
			return true, nil
		}
		if options.RequestLane != RequestLaneImage {
			continue
		}
		imageLaneLimit, err := EffectiveImageLaneConcurrencyLimit(int64(hardLimit), options.SchedulingPolicy, s.schedulingDefaults)
		if err != nil {
			return false, err
		}
		if imageLaneConcurrency[concurrencyAccountID] >= int(imageLaneLimit) {
			return true, nil
		}
	}
	return false, nil
}

func ptrIntFromMap(values map[string]int, key string) *int {
	if value, ok := values[key]; ok {
		copy := value
		return &copy
	}
	return nil
}

// orderOpenAIPersonalAccountsBySessionAffinity mirrors
// orderOpenAIPersonalAccountsBySessionAffinity.
func (s *AffinityService) orderOpenAIPersonalAccountsBySessionAffinity(accounts []gatewayruntimecache.OpenAIAccountSecret, sessionAffinityKey string, modelPriority *GatewayAccountModelPriority) []gatewayruntimecache.OpenAIAccountSecret {
	if anySuperPriority(accounts) {
		return accounts
	}
	if sessionAffinityKey == "" || len(accounts) < 2 {
		return accounts
	}
	s.mu.Lock()
	binding := s.sessionAffinityCacheGetLocked(sessionAffinityKey)
	s.mu.Unlock()
	return s.orderOpenAIPersonalAccountsBySessionBinding(accounts, binding, modelPriority)
}

// orderOpenAIPersonalAccountsBySessionBinding mirrors
// orderOpenAIPersonalAccountsBySessionBinding: the bound account is promoted
// over equal-or-worse peers and rotates within its own tier.
func (s *AffinityService) orderOpenAIPersonalAccountsBySessionBinding(accounts []gatewayruntimecache.OpenAIAccountSecret, binding *SessionBinding, modelPriority *GatewayAccountModelPriority) []gatewayruntimecache.OpenAIAccountSecret {
	if anySuperPriority(accounts) {
		return accounts
	}
	if len(accounts) < 2 {
		return accounts
	}
	if binding == nil {
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
	rotationEndIndex := boundIndex + 1
	for ; rotationEndIndex < len(accounts); rotationEndIndex++ {
		if !canSessionAffinityRotateWithinSameTier(boundAccount, accounts[rotationEndIndex], modelPriority) {
			break
		}
	}
	ordered := make([]gatewayruntimecache.OpenAIAccountSecret, 0, len(accounts))
	ordered = append(ordered, accounts[:targetIndex]...)
	ordered = append(ordered, boundAccount)
	ordered = append(ordered, accounts[boundIndex+1:rotationEndIndex]...)
	ordered = append(ordered, accounts[targetIndex:boundIndex]...)
	ordered = append(ordered, accounts[rotationEndIndex:]...)
	return ordered
}

func anySuperPriority(accounts []gatewayruntimecache.OpenAIAccountSecret) bool {
	for _, account := range accounts {
		if account.SuperPriorityEnabled {
			return true
		}
	}
	return false
}

// canSessionAffinityRotateWithinSameTier mirrors
// canSessionAffinityRotateWithinSameTier.
func canSessionAffinityRotateWithinSameTier(boundAccount gatewayruntimecache.OpenAIAccountSecret, currentAccount gatewayruntimecache.OpenAIAccountSecret, modelPriority *GatewayAccountModelPriority) bool {
	return canSessionAffinityPromoteOver(boundAccount, currentAccount, modelPriority) &&
		canSessionAffinityPromoteOver(currentAccount, boundAccount, modelPriority)
}

// orderOpenAIHighConcurrencyAccounts mirrors orderOpenAIHighConcurrencyAccounts.
func (s *AffinityService) orderOpenAIHighConcurrencyAccounts(accounts []gatewayruntimecache.OpenAIAccountSecret, sessionAffinityKey string, policyInput map[string]any, trafficMigrationTargetAccountID string, modelPriority *GatewayAccountModelPriority) ([]gatewayruntimecache.OpenAIAccountSecret, error) {
	if len(accounts) < 2 {
		return accounts, nil
	}
	policy, err := resolvePolicyOrDefault(policyInput, s.schedulingDefaults)
	if err != nil {
		return nil, err
	}
	if !policy.FastFirstEnabled {
		if trafficMigrationTargetAccountID != "" {
			return s.orderOpenAIHighConcurrencyHardBusyLast(accounts)
		}
		return s.orderOpenAIPersonalAccountsBySessionAffinity(accounts, sessionAffinityKey, modelPriority), nil
	}
	var binding *SessionBinding
	if sessionAffinityKey != "" {
		s.mu.Lock()
		binding = s.sessionAffinityCacheGetLocked(sessionAffinityKey)
		s.mu.Unlock()
	}
	accountIDs := GatewayAccountConcurrencyAccountIDs(GatewayAccountConcurrencyIdentities(accounts))
	inFlightStats := map[string]AccountInFlightStats{}
	if s.cfg.Concurrency != nil {
		inFlightStats = s.cfg.Concurrency.LoadAccountInFlightStatsByIDs(accountIDs, InFlightThresholds{
			SlowRequestThresholdMs:     policy.SlowRequestThresholdMs,
			FirstOutputSlowThresholdMs: policy.FirstOutputSlowThresholdMs,
		})
	}
	candidates := s.buildHighConcurrencyCandidates(accounts, binding, inFlightStats, policy, trafficMigrationTargetAccountID)
	primarySoftAvailable := hasPrimarySoftAvailable(candidates)
	sort.SliceStable(candidates, func(i, j int) bool {
		return compareHighConcurrencyCandidates(candidates[i], candidates[j], policy, primarySoftAvailable, modelPriority) < 0
	})
	ordered := make([]gatewayruntimecache.OpenAIAccountSecret, 0, len(candidates))
	for _, candidate := range candidates {
		ordered = append(ordered, candidate.account)
	}
	return ordered, nil
}

// orderOpenAIHighConcurrencyAccountsAsync mirrors
// orderOpenAIHighConcurrencyAccountsAsync.
func (s *AffinityService) orderOpenAIHighConcurrencyAccountsAsync(ctx context.Context, accounts []gatewayruntimecache.OpenAIAccountSecret, sessionAffinityKey string, policyInput map[string]any, trafficMigrationTargetAccountID string, modelPriority *GatewayAccountModelPriority, sessionBinding *SessionBinding) ([]gatewayruntimecache.OpenAIAccountSecret, error) {
	if s.cfg.RuntimeStateDriver != RuntimeStateDriverRedis {
		return s.orderOpenAIHighConcurrencyAccounts(accounts, sessionAffinityKey, policyInput, trafficMigrationTargetAccountID, modelPriority)
	}
	if len(accounts) < 2 {
		return accounts, nil
	}
	policy, err := resolvePolicyOrDefault(policyInput, s.schedulingDefaults)
	if err != nil {
		return nil, err
	}
	fallbackBinding := sessionBinding
	if fallbackBinding == nil && s.cfg.CacheDriver != CacheDriverRedis && sessionAffinityKey != "" {
		s.mu.Lock()
		fallbackBinding = s.sessionAffinityCacheGetLocked(sessionAffinityKey)
		s.mu.Unlock()
	}
	if !policy.FastFirstEnabled {
		if trafficMigrationTargetAccountID != "" {
			return s.orderOpenAIHighConcurrencyHardBusyLastAsync(ctx, accounts)
		}
		return s.orderOpenAIPersonalAccountsBySessionBinding(accounts, fallbackBinding, modelPriority), nil
	}
	var inFlightStats map[string]AccountInFlightStats
	if s.cfg.Concurrency != nil {
		inFlightStats, err = s.cfg.Concurrency.LoadAccountInFlightStatsByIDsAsync(ctx, GatewayAccountConcurrencyAccountIDs(GatewayAccountConcurrencyIdentities(accounts)), InFlightThresholds{
			SlowRequestThresholdMs:     policy.SlowRequestThresholdMs,
			FirstOutputSlowThresholdMs: policy.FirstOutputSlowThresholdMs,
		})
		if err != nil {
			return nil, err
		}
	} else {
		inFlightStats = map[string]AccountInFlightStats{}
	}
	candidates := s.buildHighConcurrencyCandidates(accounts, fallbackBinding, inFlightStats, policy, trafficMigrationTargetAccountID)
	primarySoftAvailable := hasPrimarySoftAvailable(candidates)
	sort.SliceStable(candidates, func(i, j int) bool {
		return compareHighConcurrencyCandidates(candidates[i], candidates[j], policy, primarySoftAvailable, modelPriority) < 0
	})
	ordered := make([]gatewayruntimecache.OpenAIAccountSecret, 0, len(candidates))
	for _, candidate := range candidates {
		ordered = append(ordered, candidate.account)
	}
	return ordered, nil
}

// resolvePolicyOrDefault mirrors `resolveGroupSchedulingPolicy('high_concurrency', input) ?? DEFAULT`.
func resolvePolicyOrDefault(policyInput map[string]any, defaults SchedulingDefaults) (*SchedulingPolicyValues, error) {
	policy, err := ResolveGroupSchedulingPolicy(GroupTypeHighConcurrency, policyInput, defaults)
	if err != nil {
		return nil, err
	}
	if policy != nil {
		return policy, nil
	}
	resolved := DefaultHighConcurrencyGroupSchedulingPolicy(defaults)
	return &resolved, nil
}

// highConcurrencyCandidate mirrors HighConcurrencyCandidate.
type highConcurrencyCandidate struct {
	account                   gatewayruntimecache.OpenAIAccountSecret
	index                     int
	currentConcurrency        int
	hardLimit                 int
	softLimit                 int
	slowInFlightCount         int
	firstOutputSlowCount      int
	oldestInFlightMs          int64
	affinityAllowed           bool
	trafficMigrationPreferred bool
	hardBusy                  bool
	softBusy                  bool
}

func (s *AffinityService) buildHighConcurrencyCandidates(accounts []gatewayruntimecache.OpenAIAccountSecret, binding *SessionBinding, inFlightStats map[string]AccountInFlightStats, policy *SchedulingPolicyValues, trafficMigrationTargetAccountID string) []highConcurrencyCandidate {
	candidates := make([]highConcurrencyCandidate, 0, len(accounts))
	for index, account := range accounts {
		concurrencyAccountID := GatewayAccountConcurrencyAccountID(GatewayAccountConcurrencyIdentityOf(account))
		runtimeStats, hasStats := inFlightStats[concurrencyAccountID]
		// Node: runtimeStats?.currentConcurrency — an absent entry falls back
		// to the account snapshot, a present zero stays zero.
		var runtimeConcurrency *int
		if hasStats {
			runtimeConcurrency = &runtimeStats.CurrentConcurrency
		}
		currentConcurrency := accountCurrentConcurrency(account, runtimeConcurrency)
		hardLimit := accountHardConcurrencyLimit(account)
		softLimit := int(effectiveSoftConcurrencyLimitResolved(int64(hardLimit), policy))
		boundToSession := binding != nil && binding.AccountID == account.ID
		affinityAllowed := boundToSession &&
			currentConcurrency < hardLimit &&
			(!policy.BreakAffinityOnSoftLimit || currentConcurrency < softLimit)
		softBusy := true
		switch {
		case !policy.BreakAffinityOnSoftLimit && boundToSession:
			softBusy = false
		default:
			softBusy = currentConcurrency >= softLimit
		}
		candidates = append(candidates, highConcurrencyCandidate{
			account:                   account,
			index:                     index,
			currentConcurrency:        currentConcurrency,
			hardLimit:                 hardLimit,
			softLimit:                 softLimit,
			slowInFlightCount:         runtimeStats.SlowInFlightCount,
			firstOutputSlowCount:      runtimeStats.FirstOutputSlowCount,
			oldestInFlightMs:          runtimeStats.OldestInFlightMs,
			affinityAllowed:           affinityAllowed,
			trafficMigrationPreferred: account.ID == trafficMigrationTargetAccountID,
			hardBusy:                  currentConcurrency >= hardLimit,
			softBusy:                  softBusy,
		})
	}
	return candidates
}

func hasPrimarySoftAvailable(candidates []highConcurrencyCandidate) bool {
	for _, candidate := range candidates {
		if !candidate.account.FallbackEnabled && !candidate.hardBusy && !candidate.softBusy {
			return true
		}
	}
	return false
}

// compareHighConcurrencyCandidates mirrors compareHighConcurrencyCandidates.
func compareHighConcurrencyCandidates(left highConcurrencyCandidate, right highConcurrencyCandidate, policy *SchedulingPolicyValues, primarySoftAvailable bool, modelPriority *GatewayAccountModelPriority) int {
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
	if !policy.FallbackOnQueueEnabled || primarySoftAvailable {
		fallbackDelta := accountFallbackRank(left.account) - accountFallbackRank(right.account)
		if fallbackDelta != 0 {
			return sign(fallbackDelta)
		}
	}
	if left.softBusy != right.softBusy {
		if left.softBusy {
			return 1
		}
		return -1
	}
	if policy.FallbackOnQueueEnabled && !primarySoftAvailable {
		fallbackDelta := accountFallbackRank(left.account) - accountFallbackRank(right.account)
		if fallbackDelta != 0 {
			return sign(fallbackDelta)
		}
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
	loadRatioDelta := (float64(left.currentConcurrency) / float64(left.softLimit)) - (float64(right.currentConcurrency) / float64(right.softLimit))
	if math.Abs(loadRatioDelta) > 0.000001 {
		if loadRatioDelta < 0 {
			return -1
		}
		return 1
	}
	if left.currentConcurrency != right.currentConcurrency {
		return sign(left.currentConcurrency - right.currentConcurrency)
	}
	if left.firstOutputSlowCount != right.firstOutputSlowCount {
		return sign(left.firstOutputSlowCount - right.firstOutputSlowCount)
	}
	if left.slowInFlightCount != right.slowInFlightCount {
		return sign(left.slowInFlightCount - right.slowInFlightCount)
	}
	if left.oldestInFlightMs != right.oldestInFlightMs {
		return signInt64(left.oldestInFlightMs - right.oldestInFlightMs)
	}
	if delta := compareAccountQualityRank(left.account, right.account); delta != 0 {
		return delta
	}
	if left.affinityAllowed != right.affinityAllowed {
		if left.affinityAllowed {
			return -1
		}
		return 1
	}
	return sign(left.index - right.index)
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

func signInt64(value int64) int {
	if value < 0 {
		return -1
	}
	if value > 0 {
		return 1
	}
	return 0
}

// orderOpenAIHighConcurrencyHardBusyLast mirrors
// orderOpenAIHighConcurrencyHardBusyLast.
func (s *AffinityService) orderOpenAIHighConcurrencyHardBusyLast(accounts []gatewayruntimecache.OpenAIAccountSecret) ([]gatewayruntimecache.OpenAIAccountSecret, error) {
	if len(accounts) < 2 {
		return accounts, nil
	}
	policy := DefaultHighConcurrencyGroupSchedulingPolicy(s.schedulingDefaults)
	inFlightStats := map[string]AccountInFlightStats{}
	if s.cfg.Concurrency != nil {
		inFlightStats = s.cfg.Concurrency.LoadAccountInFlightStatsByIDs(GatewayAccountConcurrencyAccountIDs(GatewayAccountConcurrencyIdentities(accounts)), InFlightThresholds{
			SlowRequestThresholdMs:     policy.SlowRequestThresholdMs,
			FirstOutputSlowThresholdMs: policy.FirstOutputSlowThresholdMs,
		})
	}
	var available []gatewayruntimecache.OpenAIAccountSecret
	var hardBusy []gatewayruntimecache.OpenAIAccountSecret
	for _, account := range accounts {
		concurrencyAccountID := GatewayAccountConcurrencyAccountID(GatewayAccountConcurrencyIdentityOf(account))
		runtimeStats, hasStats := inFlightStats[concurrencyAccountID]
		var runtimeConcurrency *int
		if hasStats {
			runtimeConcurrency = &runtimeStats.CurrentConcurrency
		}
		if accountCurrentConcurrency(account, runtimeConcurrency) >= accountHardConcurrencyLimit(account) {
			hardBusy = append(hardBusy, account)
		} else {
			available = append(available, account)
		}
	}
	if len(available) > 0 && len(hardBusy) > 0 {
		ordered := make([]gatewayruntimecache.OpenAIAccountSecret, 0, len(accounts))
		ordered = append(ordered, available...)
		ordered = append(ordered, hardBusy...)
		return ordered, nil
	}
	return accounts, nil
}

// orderOpenAIHighConcurrencyHardBusyLastAsync mirrors
// orderOpenAIHighConcurrencyHardBusyLastAsync.
func (s *AffinityService) orderOpenAIHighConcurrencyHardBusyLastAsync(ctx context.Context, accounts []gatewayruntimecache.OpenAIAccountSecret) ([]gatewayruntimecache.OpenAIAccountSecret, error) {
	if s.cfg.RuntimeStateDriver != RuntimeStateDriverRedis {
		return s.orderOpenAIHighConcurrencyHardBusyLast(accounts)
	}
	if len(accounts) < 2 {
		return accounts, nil
	}
	currentConcurrencyByAccount := map[string]int{}
	if s.cfg.Concurrency != nil {
		values, err := s.cfg.Concurrency.LoadAccountCurrentConcurrencyByIDsAsync(ctx, GatewayAccountConcurrencyAccountIDs(GatewayAccountConcurrencyIdentities(accounts)), "")
		if err != nil {
			return nil, err
		}
		currentConcurrencyByAccount = values
	}
	var available []gatewayruntimecache.OpenAIAccountSecret
	var hardBusy []gatewayruntimecache.OpenAIAccountSecret
	for _, account := range accounts {
		concurrencyAccountID := GatewayAccountConcurrencyAccountID(GatewayAccountConcurrencyIdentityOf(account))
		runtimeValue := ptrIntFromMap(currentConcurrencyByAccount, concurrencyAccountID)
		if accountCurrentConcurrency(account, runtimeValue) >= accountHardConcurrencyLimit(account) {
			hardBusy = append(hardBusy, account)
		} else {
			available = append(available, account)
		}
	}
	if len(available) > 0 && len(hardBusy) > 0 {
		ordered := make([]gatewayruntimecache.OpenAIAccountSecret, 0, len(accounts))
		ordered = append(ordered, available...)
		ordered = append(ordered, hardBusy...)
		return ordered, nil
	}
	return accounts, nil
}

// canSessionAffinityPromoteOver mirrors canSessionAffinityPromoteOver.
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
	return accountQualityRank(boundAccount) <= accountQualityRank(currentAccount)
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
