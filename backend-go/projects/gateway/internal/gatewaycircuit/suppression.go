package gatewaycircuit

import (
	"fmt"
	"sync"
)

// Availability status values mirror AccountRuntimeAvailabilityStatus.
const (
	AvailabilityStatusNormal         = "normal"
	AvailabilityStatusDegraded       = "degraded"
	AvailabilityStatusLocalSuppressed = "local_suppressed"
	AvailabilityStatusHalfOpen       = "half_open"
	AvailabilityStatusPrecheckPending = "precheck_pending"
	AvailabilityStatusPrecheckFailed  = "precheck_failed"
)

// Suppression window constants mirror account-local-suppression-store.ts.
const (
	LocalSuppressionMaxMs                        = int64(10 * 60_000)
	LocalDegradationWindowMs                     = int64(5 * 60_000)
	LocalDegradationActivationFailureThreshold   = int64(2)
	LocalDegradationMinObservationMs             = int64(60_000)
	LocalSuppressionPrecheckMinObservationMs     = int64(60_000)

	localSuppressionHalfOpenLeaseMs = int64(180_000)
	localSuppressionIdleRetentionMs = int64(60_000)
)

// localSuppressionDelayMs mirrors the fixed suppression ladder.
var localSuppressionDelayMs = []int64{3_000, 5_000, 10_000}

// AccountRuntimeAvailability mirrors AccountRuntimeAvailability.
type AccountRuntimeAvailability struct {
	Status               string `json:"status"`
	Reason               string `json:"reason,omitempty"`
	Since                string `json:"since,omitempty"`
	Until                string `json:"until,omitempty"`
	FailureCount         *int64 `json:"failureCount,omitempty"`
	DistinctClientIPCount *int64 `json:"distinctClientIpCount,omitempty"`
	DistinctAPIKeyCount  *int64 `json:"distinctApiKeyCount,omitempty"`
	PrecheckAttemptCount *int64 `json:"precheckAttemptCount,omitempty"`
	LocalFailureCount    *int64 `json:"localFailureCount,omitempty"`
	ProbePresentation    any    `json:"probePresentation,omitempty"`
}

// LocalAccountSuppression mirrors the stored suppression entry.
type LocalAccountSuppression struct {
	AccountID                 string
	AccountConcurrencyAccountID string
	UntilMs                   int64
	Reason                    string
	SinceMs                   int64
	Status                    string
	FailureCount              *int64
	DistinctClientIPCount     *int64
	DistinctAPIKeyCount       *int64
	PrecheckAttemptCount      *int64
	LocalFailureCount         *int64
	HalfOpenLeaseUntilMs      *int64
	HalfOpenLeaseID           *string
}

type localAccountDegradation struct {
	accountID      string
	reason         string
	sinceMs        int64
	firstFailureMs int64
	lastFailureMs  int64
	failureCount   int64
}

// LocalSuppressionResult mirrors GatewayAccountLocalSuppressionResult.
type LocalSuppressionResult struct {
	RuntimeKey        string
	Action            string // 'suppressed' | 'precheck_required' | 'redis_managed'
	Reason            string
	LocalFailureCount int64
	DelayMs           int64
	HasDelayMs        bool
	Until             string
}

// Suppression result actions.
const (
	SuppressionActionSuppressed    = "suppressed"
	SuppressionActionPrecheckRequired = "precheck_required"
	SuppressionActionRedisManaged  = "redis_managed"
)

// HalfOpenLease mirrors GatewayAccountHalfOpenLease.
type HalfOpenLease struct {
	RuntimeKey string
	AccountID  string
	LeaseID    string
	// Release mirrors release(); false mirrors a lost lease.
	Release func() bool
}

// SuppressionFilterOptions mirrors LocalAccountSuppressionFilterOptions.
type SuppressionFilterOptions struct {
	AcquireHalfOpenLease        bool
	AcquirePrecheckHalfOpenLease bool
	PrecheckHalfOpenGroupKey    string
}

// PrecheckRuntimeBlockingPredicate mirrors the isPrecheckRuntimeBlocking predicate.
type PrecheckRuntimeBlockingPredicate func(runtimeKey string) bool

// SuppressibleAccount is the account carrier the filter/order helpers take
// (mirrors SuppressibleGatewayAccount consumers).
type SuppressibleAccount struct {
	SuppressibleGatewayAccount
	FallbackEnabled       bool
	SuperPriorityEnabled  bool
	Priority              int64
	ModelRank             int64
	HasModelRank          bool
}

// SuppressionFilterResult mirrors LocalAccountSuppressionFilterResult.
type SuppressionFilterResult struct {
	Accounts                            []SuppressibleAccount
	SuppressedCount                     int
	AllSuppressed                       bool
	SuppressedAccountIDs                []string
	AcquiredHalfOpenLeases              []HalfOpenLease
	PrecheckSuppressedAccountIDs        []string
	ConfiguredPolicySuppressedAccountIDs []string
	PrecheckSuppressedRuntimeScopes     []PrecheckSuppressedRuntimeScope
	NextRetryAtMs                       *int64
	NextRetryAfterMs                    *int64
}

// PrecheckSuppressedRuntimeScope mirrors { runtimeKey, generation }.
type PrecheckSuppressedRuntimeScope struct {
	RuntimeKey string
	Generation int64
}

// DegradationOrderResult mirrors LocalAccountDegradationOrderResult.
type DegradationOrderResult struct {
	Accounts            []SuppressibleAccount
	DegradedCount       int
	DegradedAccountIDs  []string
	Applied             bool
	BypassedAllDegraded bool
}

// Logger mirrors the logging surface the Node store uses (structured fields
// + Chinese message copy).
type Logger interface {
	Info(fields map[string]any, message string)
	Warn(fields map[string]any, message string)
}

type nopLogger struct{}

func (nopLogger) Info(map[string]any, string)  {}
func (nopLogger) Warn(map[string]any, string)  {}

// NopLogger is the default no-op logger.
var NopLogger Logger = nopLogger{}

// LocalSuppressionStoreOptions carries the injectable environment.
type LocalSuppressionStoreOptions struct {
	// Now mirrors Date.now; defaults to the wall clock.
	Now func() int64
	// CanUseProcessLocal mirrors canUseProcessLocalAccountRuntimeState:
	// false models the Redis runtime state driver.
	CanUseProcessLocal func() bool
	// AccountConcurrency mirrors getAccountCurrentConcurrency.
	AccountConcurrency func(concurrencyAccountID string) int
	// Logger defaults to a no-op logger.
	Logger Logger
}

// LocalSuppressionStore mirrors the module-level suppression state of
// account-local-suppression-store.ts. The zero value is not usable; construct
// through NewLocalSuppressionStore.
type LocalSuppressionStore struct {
	mu                    sync.Mutex
	suppressions          map[string]*LocalAccountSuppression
	degradations          map[string]*localAccountDegradation
	halfOpenLeaseSequence int64
	now                   func() int64
	canUseProcessLocal    func() bool
	accountConcurrency    func(string) int
	logger                Logger
}

// NewLocalSuppressionStore mirrors the module initialization.
func NewLocalSuppressionStore(options LocalSuppressionStoreOptions) *LocalSuppressionStore {
	now := options.Now
	if now == nil {
		now = defaultNowMs
	}
	canUse := options.CanUseProcessLocal
	if canUse == nil {
		canUse = func() bool { return true }
	}
	concurrency := options.AccountConcurrency
	if concurrency == nil {
		concurrency = func(string) int { return 0 }
	}
	logger := options.Logger
	if logger == nil {
		logger = NopLogger
	}
	return &LocalSuppressionStore{
		suppressions:       map[string]*LocalAccountSuppression{},
		degradations:       map[string]*localAccountDegradation{},
		now:                now,
		canUseProcessLocal: canUse,
		accountConcurrency: concurrency,
		logger:             logger,
	}
}

// DegradeForGatewayFailure mirrors degradeLocalAccountForGatewayFailure.
func (s *LocalSuppressionStore) DegradeForGatewayFailure(runtimeKey, accountID, reason string) AccountRuntimeAvailability {
	if !s.canUseProcessLocal() {
		s.mu.Lock()
		s.clearLocked()
		s.mu.Unlock()
		return AccountRuntimeAvailability{
			Status:       AvailabilityStatusNormal,
			Reason:       reason,
			Since:        msToRFC3339(s.now()),
			FailureCount: int64Ptr(0),
		}
	}
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupExpiredDegradationsLocked(now)
	currentSuppression := s.suppressions[runtimeKey]
	shouldAdvanceFailureCount := shouldAdvanceLocalDegradationFailureCount(currentSuppression, now)
	current := s.degradations[runtimeKey]
	withinWindow := current != nil && now-current.firstFailureMs <= LocalDegradationWindowMs
	var nextFailureCount int64
	if shouldAdvanceFailureCount {
		if withinWindow {
			nextFailureCount = current.failureCount + 1
		} else {
			nextFailureCount = 1
		}
	} else {
		nextFailureCount = int64(1)
		if current != nil && current.failureCount > 1 {
			nextFailureCount = current.failureCount
		}
	}
	degradation := &localAccountDegradation{
		accountID:      accountID,
		reason:         reason,
		sinceMs:        now,
		firstFailureMs: now,
		lastFailureMs:  now,
		failureCount:   nextFailureCount,
	}
	if current != nil {
		degradation.sinceMs = current.sinceMs
		if withinWindow {
			degradation.firstFailureMs = current.firstFailureMs
		}
	}
	s.degradations[runtimeKey] = degradation
	if !shouldAdvanceFailureCount {
		if isLocalAccountDegradationActive(degradation) {
			return localAccountDegradationAvailability(degradation)
		}
		return localAccountDegradationObservationAvailability(degradation)
	}
	if !isLocalAccountDegradationActive(degradation) {
		s.logger.Info(map[string]any{
			"event":                    "gateway_account_runtime_degradation_observed",
			"accountId":                accountID,
			"runtimeKey":               runtimeKey,
			"failureCount":             degradation.failureCount,
			"activationFailureThreshold": LocalDegradationActivationFailureThreshold,
			"observationWindowSeconds": LocalDegradationWindowMs / 1000,
			"reason":                   reason,
		}, "账号近期失败已记录，暂未达到运行态调度降级门槛")
		return localAccountDegradationObservationAvailability(degradation)
	}
	s.logger.Warn(map[string]any{
		"event":                    "gateway_account_runtime_degraded",
		"accountId":                accountID,
		"runtimeKey":               runtimeKey,
		"failureCount":             degradation.failureCount,
		"activationFailureThreshold": LocalDegradationActivationFailureThreshold,
		"observationWindowSeconds": LocalDegradationWindowMs / 1000,
		"reason":                   reason,
	}, "账号近期失败，已进入运行态调度降级，仅在普通候选不足时兜底尝试")
	return localAccountDegradationAvailability(degradation)
}

// SuppressForGatewayFailure mirrors suppressLocalAccountForGatewayFailure.
func (s *LocalSuppressionStore) SuppressForGatewayFailure(runtimeKey, accountID, reason string, accountConcurrencyAccountID string) LocalSuppressionResult {
	if accountConcurrencyAccountID == "" {
		accountConcurrencyAccountID = accountID
	}
	if !s.canUseProcessLocal() {
		s.mu.Lock()
		s.clearLocked()
		s.mu.Unlock()
		return LocalSuppressionResult{
			RuntimeKey:        runtimeKey,
			Action:            SuppressionActionRedisManaged,
			Reason:            reason,
			LocalFailureCount: 0,
		}
	}
	now := s.now()
	s.mu.Lock()
	current := s.suppressions[runtimeKey]
	currentFailureCount := int64(0)
	if current != nil && current.LocalFailureCount != nil {
		currentFailureCount = *current.LocalFailureCount
	}
	shouldAdvanceFailureCount := current == nil ||
		current.Status == AvailabilityStatusHalfOpen ||
		(current.Status == AvailabilityStatusLocalSuppressed && current.UntilMs <= now)
	localFailureCount := int64Max64(1, currentFailureCount)
	if shouldAdvanceFailureCount {
		localFailureCount = currentFailureCount + 1
	}
	suppress := func(delayMs int64, status string) {
		s.suppressLocked(runtimeKey, delayMs, reason, status, now, &suppressionMetadata{
			accountID:                   accountID,
			accountConcurrencyAccountID: accountConcurrencyAccountID,
			localFailureCount:           &localFailureCount,
		})
	}
	s.mu.Unlock()

	if localFailureCount > int64(len(localSuppressionDelayMs)) {
		fallbackDelayMs := localSuppressionDelayMs[len(localSuppressionDelayMs)-1]
		var observedForMs int64
		s.mu.Lock()
		if current != nil {
			observedForMs = now - current.SinceMs
		}
		s.mu.Unlock()
		if observedForMs < LocalSuppressionPrecheckMinObservationMs {
			s.mu.Lock()
			suppress(fallbackDelayMs, AvailabilityStatusLocalSuppressed)
			s.mu.Unlock()
			s.logger.Warn(map[string]any{
				"event":             "gateway_account_local_suppression_precheck_delayed",
				"accountId":         accountID,
				"runtimeKey":        runtimeKey,
				"localFailureCount": localFailureCount,
				"observedForMs":     observedForMs,
				"minObservationMs":  LocalSuppressionPrecheckMinObservationMs,
				"reason":            reason,
			}, "账号短暂避让半开探测失败，但未达到事前确认最小观察时间")
			return LocalSuppressionResult{
				RuntimeKey:        runtimeKey,
				Action:            SuppressionActionSuppressed,
				Reason:            reason,
				LocalFailureCount: localFailureCount,
				DelayMs:           fallbackDelayMs,
				HasDelayMs:        true,
				Until:             msToRFC3339(s.now() + fallbackDelayMs),
			}
		}
		s.mu.Lock()
		suppress(fallbackDelayMs, AvailabilityStatusLocalSuppressed)
		s.mu.Unlock()
		s.logger.Warn(map[string]any{
			"event":             "gateway_account_local_suppression_precheck_required",
			"accountId":         accountID,
			"runtimeKey":        runtimeKey,
			"localFailureCount": localFailureCount,
			"observedForMs":     observedForMs,
			"minObservationMs":  LocalSuppressionPrecheckMinObservationMs,
			"reason":            reason,
		}, "账号短暂避让半开探测连续失败，要求进入事前确认")
		return LocalSuppressionResult{
			RuntimeKey:        runtimeKey,
			Action:            SuppressionActionPrecheckRequired,
			Reason:            reason,
			LocalFailureCount: localFailureCount,
		}
	}

	delayMs := localSuppressionDelayMs[localFailureCount-1]
	s.mu.Lock()
	suppress(delayMs, AvailabilityStatusLocalSuppressed)
	s.mu.Unlock()
	return LocalSuppressionResult{
		RuntimeKey:        runtimeKey,
		Action:            SuppressionActionSuppressed,
		Reason:            reason,
		LocalFailureCount: localFailureCount,
		DelayMs:           delayMs,
		HasDelayMs:        true,
		Until:             msToRFC3339(s.now() + delayMs),
	}
}

// suppressionMetadata mirrors the metadata override object.
type suppressionMetadata struct {
	accountID                   string
	accountConcurrencyAccountID string
	sinceMs                     *int64
	failureCount                *int64
	distinctClientIPCount       *int64
	distinctAPIKeyCount         *int64
	precheckAttemptCount        *int64
	localFailureCount           *int64
	halfOpenLeaseUntilMs        *int64
	halfOpenLeaseID             *string
}

// Suppress mirrors suppressLocalAccount.
func (s *LocalSuppressionStore) Suppress(runtimeKey string, durationMs int64, reason string, status string, metadata *suppressionMetadata) {
	if !s.canUseProcessLocal() {
		s.mu.Lock()
		s.clearLocked()
		s.mu.Unlock()
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.suppressLocked(runtimeKey, durationMs, reason, status, s.now(), metadata)
}

func (s *LocalSuppressionStore) suppressLocked(runtimeKey string, durationMs int64, reason, status string, now int64, metadata *suppressionMetadata) {
	untilMs := now + durationMs
	current := s.suppressions[runtimeKey]
	metadataAccountID := ""
	metadataConcurrencyID := ""
	if metadata != nil {
		metadataAccountID = metadata.accountID
		metadataConcurrencyID = metadata.accountConcurrencyAccountID
	}
	accountID := metadataAccountID
	if accountID == "" && current != nil {
		accountID = current.AccountID
	}
	if accountID == "" {
		accountID = RuntimeAccountIDFromKey(runtimeKey)
	}
	accountConcurrencyAccountID := metadataConcurrencyID
	if accountConcurrencyAccountID == "" && current != nil {
		accountConcurrencyAccountID = current.AccountConcurrencyAccountID
	}
	if accountConcurrencyAccountID == "" {
		accountConcurrencyAccountID = accountID
	}
	shouldPreserveLongerUntil := current != nil &&
		current.UntilMs >= untilMs &&
		!(current.Status == AvailabilityStatusHalfOpen && status == AvailabilityStatusLocalSuppressed)
	if shouldPreserveLongerUntil {
		preserved := *current
		preserved.AccountID = accountID
		preserved.AccountConcurrencyAccountID = accountConcurrencyAccountID
		preserved.Status = status
		preserved.Reason = reason
		preserved.HalfOpenLeaseUntilMs = nil
		preserved.HalfOpenLeaseID = nil
		if metadata != nil {
			preserved.HalfOpenLeaseUntilMs = metadata.halfOpenLeaseUntilMs
			preserved.HalfOpenLeaseID = metadata.halfOpenLeaseID
			if metadata.sinceMs != nil {
				preserved.SinceMs = *metadata.sinceMs
			}
			applySuppressionMetadata(&preserved, metadata)
		}
		s.suppressions[runtimeKey] = &preserved
		return
	}
	next := LocalAccountSuppression{
		AccountID:                 accountID,
		AccountConcurrencyAccountID: accountConcurrencyAccountID,
		UntilMs:                   untilMs,
		Reason:                    reason,
		SinceMs:                   now,
		Status:                    status,
	}
	if current != nil {
		next.SinceMs = current.SinceMs
		next.LocalFailureCount = current.LocalFailureCount
	}
	if metadata != nil {
		if metadata.sinceMs != nil {
			next.SinceMs = *metadata.sinceMs
		}
		next.HalfOpenLeaseUntilMs = metadata.halfOpenLeaseUntilMs
		next.HalfOpenLeaseID = metadata.halfOpenLeaseID
		applySuppressionMetadata(&next, metadata)
	}
	s.suppressions[runtimeKey] = &next
	s.logger.Warn(map[string]any{
		"event":             "gateway_account_local_suppressed",
		"accountId":         accountID,
		"runtimeKey":        runtimeKey,
		"until":             msToRFC3339(untilMs),
		"runtimeStatus":     status,
		"localFailureCount": metadataLocalFailureCount(metadata),
		"reason":            reason,
	}, "网关账号已进入 Web 进程本地短期屏蔽")
}

func applySuppressionMetadata(target *LocalAccountSuppression, metadata *suppressionMetadata) {
	if metadata == nil {
		return
	}
	if metadata.failureCount != nil {
		target.FailureCount = metadata.failureCount
	}
	if metadata.distinctClientIPCount != nil {
		target.DistinctClientIPCount = metadata.distinctClientIPCount
	}
	if metadata.distinctAPIKeyCount != nil {
		target.DistinctAPIKeyCount = metadata.distinctAPIKeyCount
	}
	if metadata.precheckAttemptCount != nil {
		target.PrecheckAttemptCount = metadata.precheckAttemptCount
	}
	if metadata.localFailureCount != nil {
		target.LocalFailureCount = metadata.localFailureCount
	}
}

func metadataLocalFailureCount(metadata *suppressionMetadata) *int64 {
	if metadata == nil {
		return nil
	}
	return metadata.localFailureCount
}

// ReleaseHalfOpenLease mirrors releaseLocalAccountHalfOpenLease.
func (s *LocalSuppressionStore) ReleaseHalfOpenLease(runtimeKey, accountID, leaseID string) bool {
	if !s.canUseProcessLocal() {
		s.mu.Lock()
		s.clearLocked()
		s.mu.Unlock()
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.suppressions[runtimeKey]
	if current == nil || current.Status != AvailabilityStatusHalfOpen || current.HalfOpenLeaseID == nil || *current.HalfOpenLeaseID != leaseID {
		return false
	}
	now := s.now()
	next := *current
	next.Status = AvailabilityStatusLocalSuppressed
	next.UntilMs = now
	next.HalfOpenLeaseUntilMs = nil
	next.HalfOpenLeaseID = nil
	reason := truncateString(fmt.Sprintf("半开探测请求结束，等待下一次调度确认；%s", current.Reason), 1000)
	next.Reason = reason
	s.suppressions[runtimeKey] = &next
	s.logger.Info(map[string]any{
		"event":             "gateway_account_local_half_open_released",
		"accountId":         accountID,
		"runtimeKey":        runtimeKey,
		"localFailureCount": current.LocalFailureCount,
	}, "账号短暂避让半开探测租约已释放")
	return true
}

// SnapshotAvailability mirrors snapshotLocalAccountRuntimeAvailability.
func (s *LocalSuppressionStore) SnapshotAvailability(isPrecheckRuntimeBlocking PrecheckRuntimeBlockingPredicate) map[string]AccountRuntimeAvailability {
	if !s.canUseProcessLocal() {
		s.mu.Lock()
		s.clearLocked()
		s.mu.Unlock()
		return map[string]AccountRuntimeAvailability{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	s.cleanupExpiredSuppressionsLocked(now, isPrecheckRuntimeBlocking)
	s.cleanupExpiredDegradationsLocked(now)
	snapshot := map[string]AccountRuntimeAvailability{}
	for runtimeKey, suppression := range s.suppressions {
		if !isLocalSuppressionVisible(runtimeKey, suppression, now, isPrecheckRuntimeBlocking, s.accountConcurrency) {
			continue
		}
		snapshot[runtimeKey] = AccountRuntimeAvailability{
			Status:               suppression.Status,
			Reason:               suppression.Reason,
			Since:                msToRFC3339(suppression.SinceMs),
			Until:                msToRFC3339(localSuppressionVisibleUntilMs(suppression, now, s.accountConcurrency)),
			FailureCount:         suppression.FailureCount,
			DistinctClientIPCount: suppression.DistinctClientIPCount,
			DistinctAPIKeyCount:  suppression.DistinctAPIKeyCount,
			PrecheckAttemptCount: suppression.PrecheckAttemptCount,
			LocalFailureCount:    suppression.LocalFailureCount,
		}
	}
	for runtimeKey, degradation := range s.degradations {
		if !isLocalAccountDegradationActive(degradation) {
			continue
		}
		if _, exists := snapshot[runtimeKey]; exists {
			continue
		}
		if isPrecheckRuntimeBlocking(runtimeKey) {
			continue
		}
		snapshot[runtimeKey] = localAccountDegradationAvailability(degradation)
	}
	return snapshot
}

// OrderDegradations mirrors orderLocalAccountDegradations.
func (s *LocalSuppressionStore) OrderDegradations(accounts []SuppressibleAccount, modelRankByAccountID map[string]int64) DegradationOrderResult {
	if !s.canUseProcessLocal() {
		s.mu.Lock()
		s.clearLocked()
		s.mu.Unlock()
		return DegradationOrderResult{Accounts: accounts}
	}
	if len(accounts) == 0 {
		return DegradationOrderResult{Accounts: accounts}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupExpiredDegradationsLocked(s.now())
	var normalAccounts, degradedAccounts []SuppressibleAccount
	var degradedAccountIDs []string
	for _, account := range accounts {
		runtimeKey := mustRuntimeKey(account.SuppressibleGatewayAccount)
		degradation := s.degradations[runtimeKey]
		if degradation != nil && isLocalAccountDegradationActive(degradation) {
			degradedAccounts = append(degradedAccounts, account)
			degradedAccountIDs = append(degradedAccountIDs, account.ID)
		} else {
			normalAccounts = append(normalAccounts, account)
		}
	}
	if len(degradedAccounts) == 0 {
		return DegradationOrderResult{Accounts: accounts}
	}
	if len(normalAccounts) == 0 {
		return DegradationOrderResult{
			Accounts:            accounts,
			DegradedCount:       len(degradedAccounts),
			DegradedAccountIDs:  degradedAccountIDs,
			BypassedAllDegraded: true,
		}
	}
	reordered := append(append([]SuppressibleAccount{}, normalAccounts...), degradedAccounts...)
	return DegradationOrderResult{
		Accounts:           preserveDispatchPriorityTiers(accounts, reordered, modelRankByAccountID),
		DegradedCount:      len(degradedAccounts),
		DegradedAccountIDs: degradedAccountIDs,
		Applied:            true,
	}
}

// FilterSuppressions mirrors filterLocalAccountSuppressions.
func (s *LocalSuppressionStore) FilterSuppressions(
	accounts []SuppressibleAccount,
	isPrecheckRuntimeBlocking PrecheckRuntimeBlockingPredicate,
	options SuppressionFilterOptions,
) SuppressionFilterResult {
	if !s.canUseProcessLocal() {
		s.mu.Lock()
		s.clearLocked()
		s.mu.Unlock()
		return SuppressionFilterResult{Accounts: accounts}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	s.cleanupExpiredSuppressionsLocked(now, isPrecheckRuntimeBlocking)
	var filtered []SuppressibleAccount
	var suppressedAccountIDs []string
	var acquiredHalfOpenLeases []HalfOpenLease
	var nextRetryAtMs *int64
	for _, account := range accounts {
		runtimeKey := mustRuntimeKey(account.SuppressibleGatewayAccount)
		suppression := s.suppressions[runtimeKey]
		if isPrecheckRuntimeBlocking(runtimeKey) {
			suppressedAccountIDs = append(suppressedAccountIDs, account.ID)
			candidate := int64Max64(derefInt64(suppressionUntilMs(suppression)), now+1000)
			nextRetryAtMs = minRetryAtMs(nextRetryAtMs, candidate)
			continue
		}
		if suppression == nil || !isLocalSuppressionBlocking(suppression, now, s.accountConcurrency) {
			if suppression != nil && options.AcquireHalfOpenLease && canAcquireLocalHalfOpenLease(suppression, now, s.accountConcurrency) {
				acquiredHalfOpenLeases = append(acquiredHalfOpenLeases, s.acquireHalfOpenLeaseLocked(runtimeKey, account, suppression, now))
			}
			filtered = append(filtered, account)
			continue
		}
		suppressedAccountIDs = append(suppressedAccountIDs, account.ID)
		nextRetryAtMs = minRetryAtMs(nextRetryAtMs, localSuppressionVisibleUntilMs(suppression, now, s.accountConcurrency))
	}
	result := SuppressionFilterResult{
		Accounts:             filtered,
		SuppressedCount:      len(suppressedAccountIDs),
		AllSuppressed:        len(filtered) == 0 && len(accounts) > 0,
		SuppressedAccountIDs: suppressedAccountIdsCopy(suppressedAccountIDs),
		AcquiredHalfOpenLeases: acquiredHalfOpenLeases,
		NextRetryAtMs:        nextRetryAtMs,
	}
	if nextRetryAtMs != nil {
		after := int64Max64(0, *nextRetryAtMs-now)
		result.NextRetryAfterMs = &after
	}
	return result
}

func suppressionUntilMs(suppression *LocalAccountSuppression) *int64 {
	if suppression == nil {
		return nil
	}
	return int64Ptr(suppression.UntilMs)
}

func suppressedAccountIdsCopy(values []string) []string {
	if values == nil {
		return nil
	}
	return append([]string{}, values...)
}

// ClearSuppression mirrors clearLocalAccountSuppression.
func (s *LocalSuppressionStore) ClearSuppression(runtimeKey string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.canUseProcessLocal() {
		s.clearLocked()
		return false
	}
	if _, ok := s.suppressions[runtimeKey]; !ok {
		return false
	}
	delete(s.suppressions, runtimeKey)
	return true
}

// ClearDegradation mirrors clearLocalAccountDegradation.
func (s *LocalSuppressionStore) ClearDegradation(runtimeKey string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.canUseProcessLocal() {
		s.clearLocked()
		return false
	}
	if _, ok := s.degradations[runtimeKey]; !ok {
		return false
	}
	delete(s.degradations, runtimeKey)
	return true
}

// AgeDegradationForTest mirrors ageLocalAccountDegradationForTest.
func (s *LocalSuppressionStore) AgeDegradationForTest(runtimeKey string, ageMs int64) {
	if !s.canUseProcessLocal() {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.degradations[runtimeKey]
	if current == nil {
		return
	}
	now := s.now()
	firstFailureMs := now - int64Max64(0, ageMs)
	if firstFailureMs < current.sinceMs {
		current.sinceMs = firstFailureMs
	}
	current.firstFailureMs = firstFailureMs
}

// ActivateRuntimeDegradation mirrors activateLocalAccountRuntimeDegradation.
func (s *LocalSuppressionStore) ActivateRuntimeDegradation(runtimeKey, accountID, reason string, sinceMs *int64, failureCount *int64) AccountRuntimeAvailability {
	if !s.canUseProcessLocal() {
		s.mu.Lock()
		s.clearLocked()
		s.mu.Unlock()
		return AccountRuntimeAvailability{
			Status:       AvailabilityStatusNormal,
			Reason:       reason,
			Since:        msToRFC3339(s.now()),
			FailureCount: int64Ptr(0),
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	effectiveSinceMs := now - LocalDegradationMinObservationMs
	if sinceMs != nil {
		effectiveSinceMs = *sinceMs
	}
	effectiveFailureCount := LocalDegradationActivationFailureThreshold
	if failureCount != nil && *failureCount > LocalDegradationActivationFailureThreshold {
		effectiveFailureCount = *failureCount
	}
	degradation := &localAccountDegradation{
		accountID:      accountID,
		reason:         reason,
		sinceMs:        effectiveSinceMs,
		firstFailureMs: effectiveSinceMs,
		lastFailureMs:  now,
		failureCount:   effectiveFailureCount,
	}
	minFirstFailure := now - LocalDegradationMinObservationMs
	if degradation.firstFailureMs > minFirstFailure {
		degradation.firstFailureMs = minFirstFailure
	}
	s.degradations[runtimeKey] = degradation
	s.logger.Warn(map[string]any{
		"event":                    "gateway_account_runtime_degraded",
		"accountId":                accountID,
		"runtimeKey":               runtimeKey,
		"failureCount":             effectiveFailureCount,
		"activationFailureThreshold": LocalDegradationActivationFailureThreshold,
		"observationWindowSeconds": LocalDegradationWindowMs / 1000,
		"reason":                   reason,
	}, "后台探针确认账号近期不稳，已进入运行态调度降级")
	return localAccountDegradationAvailability(degradation)
}

// AgeSuppressionSinceForTest rewrites the suppression's sinceMs so tests can
// model an old observation window (Node tests manipulate the module maps
// directly).
func (s *LocalSuppressionStore) AgeSuppressionSinceForTest(runtimeKey string, sinceMs int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if suppression, ok := s.suppressions[runtimeKey]; ok {
		suppression.SinceMs = sinceMs
	}
}

// ClearForTest mirrors clearLocalAccountSuppressionsForTest.
func (s *LocalSuppressionStore) ClearForTest() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clearLocked()
}

// CleanupExpiredSuppressions mirrors cleanupExpiredLocalSuppressions.
func (s *LocalSuppressionStore) CleanupExpiredSuppressions(isPrecheckRuntimeBlocking PrecheckRuntimeBlockingPredicate) {
	if !s.canUseProcessLocal() {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupExpiredSuppressionsLocked(s.now(), isPrecheckRuntimeBlocking)
}

// CountVisibleSuppressions mirrors countVisibleLocalSuppressions.
func (s *LocalSuppressionStore) CountVisibleSuppressions(isPrecheckRuntimeBlocking PrecheckRuntimeBlockingPredicate) int {
	if !s.canUseProcessLocal() {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	count := 0
	for runtimeKey, suppression := range s.suppressions {
		if isLocalSuppressionVisible(runtimeKey, suppression, now, isPrecheckRuntimeBlocking, s.accountConcurrency) {
			count++
		}
	}
	return count
}

// CountDegradations mirrors countLocalAccountDegradations.
func (s *LocalSuppressionStore) CountDegradations() int {
	if !s.canUseProcessLocal() {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupExpiredDegradationsLocked(s.now())
	count := 0
	for _, degradation := range s.degradations {
		if isLocalAccountDegradationActive(degradation) {
			count++
		}
	}
	return count
}

func (s *LocalSuppressionStore) clearLocked() {
	s.suppressions = map[string]*LocalAccountSuppression{}
	s.degradations = map[string]*localAccountDegradation{}
}

func (s *LocalSuppressionStore) cleanupExpiredSuppressionsLocked(now int64, isPrecheckRuntimeBlocking PrecheckRuntimeBlockingPredicate) {
	for runtimeKey, suppression := range s.suppressions {
		if isPrecheckRuntimeBlocking(runtimeKey) {
			continue
		}
		if suppression.Status == AvailabilityStatusHalfOpen && s.accountConcurrency(localSuppressionConcurrencyAccountID(suppression)) > 0 {
			continue
		}
		retainUntilMs := int64Max64(suppression.UntilMs, derefInt64(suppression.HalfOpenLeaseUntilMs)) + localSuppressionIdleRetentionMs
		if retainUntilMs <= now {
			delete(s.suppressions, runtimeKey)
		}
	}
}

func (s *LocalSuppressionStore) cleanupExpiredDegradationsLocked(now int64) {
	for runtimeKey, degradation := range s.degradations {
		if isLocalAccountDegradationActive(degradation) {
			continue
		}
		if now-degradation.firstFailureMs > LocalDegradationWindowMs {
			delete(s.degradations, runtimeKey)
		}
	}
}

func (s *LocalSuppressionStore) acquireHalfOpenLeaseLocked(
	runtimeKey string,
	account SuppressibleAccount,
	suppression *LocalAccountSuppression,
	now int64,
) HalfOpenLease {
	leaseUntilMs := now + localSuppressionHalfOpenLeaseMs
	s.halfOpenLeaseSequence++
	leaseID := fmt.Sprintf("%d:%d", now, s.halfOpenLeaseSequence)
	next := *suppression
	next.AccountID = account.ID
	next.AccountConcurrencyAccountID = GatewayAccountConcurrencyAccountID(account.ID, account.CredentialSourceAccountID)
	next.Status = AvailabilityStatusHalfOpen
	next.UntilMs = leaseUntilMs
	next.HalfOpenLeaseUntilMs = &leaseUntilMs
	next.HalfOpenLeaseID = &leaseID
	next.Reason = truncateString(fmt.Sprintf("短暂避让到期，允许一个请求半开探测；%s", suppression.Reason), 1000)
	s.suppressions[runtimeKey] = &next
	s.logger.Info(map[string]any{
		"event":             "gateway_account_local_half_open_acquired",
		"accountId":         account.ID,
		"runtimeKey":        runtimeKey,
		"leaseUntil":        msToRFC3339(leaseUntilMs),
		"localFailureCount": suppression.LocalFailureCount,
		"reason":            suppression.Reason,
	}, "账号短暂避让到期，已放行一个真实请求进行半开探测")
	return HalfOpenLease{
		RuntimeKey: runtimeKey,
		AccountID:  account.ID,
		LeaseID:    leaseID,
		Release: func() bool {
			return s.ReleaseHalfOpenLease(runtimeKey, account.ID, leaseID)
		},
	}
}

func isLocalSuppressionVisible(
	runtimeKey string,
	suppression *LocalAccountSuppression,
	now int64,
	isPrecheckRuntimeBlocking PrecheckRuntimeBlockingPredicate,
	concurrency func(string) int,
) bool {
	return isPrecheckRuntimeBlocking(runtimeKey) || isLocalSuppressionBlocking(suppression, now, concurrency)
}

func isLocalSuppressionBlocking(suppression *LocalAccountSuppression, now int64, concurrency func(string) int) bool {
	if suppression.Status == AvailabilityStatusHalfOpen {
		leaseUntil := suppression.UntilMs
		if suppression.HalfOpenLeaseUntilMs != nil {
			leaseUntil = *suppression.HalfOpenLeaseUntilMs
		}
		return leaseUntil > now || concurrency(localSuppressionConcurrencyAccountID(suppression)) > 0
	}
	if suppression.Status == AvailabilityStatusPrecheckPending || suppression.Status == AvailabilityStatusPrecheckFailed {
		return suppression.UntilMs > now
	}
	return suppression.UntilMs > now
}

func canAcquireLocalHalfOpenLease(suppression *LocalAccountSuppression, now int64, concurrency func(string) int) bool {
	if suppression.Status == AvailabilityStatusLocalSuppressed {
		return suppression.UntilMs <= now
	}
	if suppression.Status == AvailabilityStatusHalfOpen {
		leaseUntil := suppression.UntilMs
		if suppression.HalfOpenLeaseUntilMs != nil {
			leaseUntil = *suppression.HalfOpenLeaseUntilMs
		}
		return leaseUntil <= now && concurrency(localSuppressionConcurrencyAccountID(suppression)) <= 0
	}
	return false
}

func localSuppressionVisibleUntilMs(suppression *LocalAccountSuppression, now int64, concurrency func(string) int) int64 {
	if suppression.Status != AvailabilityStatusHalfOpen {
		return suppression.UntilMs
	}
	leaseUntilMs := suppression.UntilMs
	if suppression.HalfOpenLeaseUntilMs != nil {
		leaseUntilMs = *suppression.HalfOpenLeaseUntilMs
	}
	if concurrency(localSuppressionConcurrencyAccountID(suppression)) > 0 {
		return int64Max64(leaseUntilMs, now+1000)
	}
	return leaseUntilMs
}

func localSuppressionConcurrencyAccountID(suppression *LocalAccountSuppression) string {
	if suppression.AccountConcurrencyAccountID != "" {
		return suppression.AccountConcurrencyAccountID
	}
	return suppression.AccountID
}

func minRetryAtMs(current *int64, candidate int64) *int64 {
	if current == nil {
		return &candidate
	}
	value := int64Min(*current, candidate)
	return &value
}

func shouldAdvanceLocalDegradationFailureCount(currentSuppression *LocalAccountSuppression, now int64) bool {
	if currentSuppression == nil {
		return true
	}
	if currentSuppression.Status == AvailabilityStatusHalfOpen {
		return true
	}
	return currentSuppression.Status == AvailabilityStatusLocalSuppressed && currentSuppression.UntilMs <= now
}

func localAccountDegradationAvailability(degradation *localAccountDegradation) AccountRuntimeAvailability {
	return AccountRuntimeAvailability{
		Status:       AvailabilityStatusDegraded,
		Reason:       degradation.reason,
		Since:        msToRFC3339(degradation.sinceMs),
		FailureCount: int64Ptr(degradation.failureCount),
	}
}

func localAccountDegradationObservationAvailability(degradation *localAccountDegradation) AccountRuntimeAvailability {
	return AccountRuntimeAvailability{
		Status:       AvailabilityStatusNormal,
		Reason:       degradation.reason,
		Since:        msToRFC3339(degradation.sinceMs),
		FailureCount: int64Ptr(degradation.failureCount),
	}
}

func isLocalAccountDegradationActive(degradation *localAccountDegradation) bool {
	return degradation.failureCount >= LocalDegradationActivationFailureThreshold &&
		degradation.lastFailureMs-degradation.firstFailureMs >= LocalDegradationMinObservationMs
}

func derefInt64(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

func int64Max64(left, right int64) int64 {
	if left > right {
		return left
	}
	return right
}

func truncateString(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

func mustRuntimeKey(account SuppressibleGatewayAccount) string {
	key, err := GatewayAccountRuntimeKey(account)
	if err != nil {
		panic(err)
	}
	return key
}

// PreserveDispatchPriorityTiers mirrors
// preserveGatewayAccountDispatchPriorityTiers (account-dispatch-priority-order.ts).
func PreserveDispatchPriorityTiers(baseAccounts, reorderedAccounts []SuppressibleAccount, modelRankByAccountID map[string]int64) []SuppressibleAccount {
	return preserveDispatchPriorityTiers(baseAccounts, reorderedAccounts, modelRankByAccountID)
}

func preserveDispatchPriorityTiers(baseAccounts, reorderedAccounts []SuppressibleAccount, modelRankByAccountID map[string]int64) []SuppressibleAccount {
	if len(baseAccounts) < 2 || len(reorderedAccounts) < 2 {
		return append([]SuppressibleAccount{}, reorderedAccounts...)
	}
	var baseTierOrder []string
	seenBaseTiers := map[string]struct{}{}
	for _, account := range baseAccounts {
		tier := dispatchPriorityTier(account, modelRankByAccountID)
		if _, ok := seenBaseTiers[tier]; ok {
			continue
		}
		seenBaseTiers[tier] = struct{}{}
		baseTierOrder = append(baseTierOrder, tier)
	}
	reorderedByTier := map[string][]SuppressibleAccount{}
	var unknownTierAccounts []SuppressibleAccount
	for _, account := range reorderedAccounts {
		tier := dispatchPriorityTier(account, modelRankByAccountID)
		if _, ok := seenBaseTiers[tier]; !ok {
			unknownTierAccounts = append(unknownTierAccounts, account)
			continue
		}
		reorderedByTier[tier] = append(reorderedByTier[tier], account)
	}
	output := make([]SuppressibleAccount, 0, len(reorderedAccounts))
	for _, tier := range baseTierOrder {
		output = append(output, reorderedByTier[tier]...)
	}
	output = append(output, unknownTierAccounts...)
	return output
}

const unknownGatewayDispatchModelRank = int64(3)

// DispatchPriorityTier mirrors gatewayAccountDispatchPriorityTier.
func DispatchPriorityTier(account SuppressibleAccount, modelRankByAccountID map[string]int64) string {
	return dispatchPriorityTier(account, modelRankByAccountID)
}

func dispatchPriorityTier(account SuppressibleAccount, modelRankByAccountID map[string]int64) string {
	// Node: no map -> 0; missing/unknown rank -> 3; otherwise the clamped rank.
	modelRank := int64(0)
	if modelRankByAccountID != nil {
		if rank, ok := modelRankByAccountID[account.ID]; ok {
			modelRank = int64Max64(0, rank)
		} else {
			modelRank = unknownGatewayDispatchModelRank
		}
	}
	fallbackRank := int64(0)
	if account.FallbackEnabled {
		fallbackRank = 1
	}
	superRank := int64(1)
	if account.SuperPriorityEnabled {
		superRank = 0
	}
	return fmt.Sprintf("%d:%d:%d:%d", modelRank, fallbackRank, superRank, account.Priority)
}

func isNaNInt64(value int64) bool { return false }
