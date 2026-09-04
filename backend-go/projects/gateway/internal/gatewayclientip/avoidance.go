package gatewayclientip

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// Constants mirror client-ip-account-avoidance.service.ts verbatim.
const (
	clientIPAccountAvoidanceMaxEntries                 = 5_000
	clientIPAccountAvoidanceMaxPendingFailures         = 256
	clientIPAccountAvoidanceMaxTTL                     = 10 * 60_000 // ms
	clientIPAccountAvoidanceDefaultTTL                 = 5 * 60_000 // ms
	clientIPAccountAvoidanceActivationFailureThreshold = 2

	// avoidanceStateStoreName mirrors createRuntimeStateStore('gateway-client-ip-account-avoidance').
	avoidanceStateStoreName = "gateway-client-ip-account-avoidance"
)

// AvoidanceScopeInput mirrors ClientIpAccountAvoidanceScopeInput. GroupID is
// carried for call-site parity; Node normalizeScope ignores it.
type AvoidanceScopeInput struct {
	SystemAccountID string
	GroupID         string
	APIKeyID        string
	ClientIP        string
}

// AccountFailure mirrors ClientIpAccountFailure. ErrorPhase is
// 'upstream_response' | 'upstream_request' | 'stream'.
type AccountFailure struct {
	AccountID    string `json:"accountId"`
	AccountName  string `json:"accountName,omitempty"`
	StatusCode   *int64 `json:"statusCode,omitempty"`
	ErrorCode    string `json:"errorCode,omitempty"`
	ErrorType    string `json:"errorType,omitempty"`
	ErrorPhase   string `json:"errorPhase"`
	ErrorMessage string `json:"errorMessage,omitempty"`
	Endpoint     string `json:"endpoint,omitempty"`
}

// AvoidanceOrderResult mirrors ClientIpAccountAvoidanceOrderResult.
type AvoidanceOrderResult struct {
	Accounts           []gatewayruntimecache.OpenAIAccountSecret
	Applied            bool
	AvoidedAccountIDs  []string
	BypassedAllAvoided bool
}

// AvoidanceConfirmResult mirrors ClientIpAccountAvoidanceConfirmResult.
type AvoidanceConfirmResult struct {
	ConfirmedAccountIDs []string
	ClearedAccountID    string
	Cleared             bool
}

// avoidanceScope mirrors ClientIpAccountAvoidanceScope.
type avoidanceScope struct {
	systemAccountID string
	apiKeyID        string
	clientIP        string
}

// avoidanceEntry mirrors ClientIpAccountAvoidanceEntry (the stored payload;
// field order follows the Node object literal).
type avoidanceEntry struct {
	AccountID       string `json:"accountId"`
	AccountName     string `json:"accountName,omitempty"`
	StatusCode      *int64 `json:"statusCode,omitempty"`
	ErrorCode       string `json:"errorCode,omitempty"`
	ErrorType       string `json:"errorType,omitempty"`
	ErrorPhase      string `json:"errorPhase"`
	ErrorMessage    string `json:"errorMessage,omitempty"`
	Endpoint        string `json:"endpoint,omitempty"`
	EntryKey        string `json:"entryKey"`
	ScopeKey        string `json:"scopeKey"`
	FailureCount    int    `json:"failureCount"`
	FirstFailedAtMs int64  `json:"firstFailedAtMs"`
	LastFailedAtMs  int64  `json:"lastFailedAtMs"`
	AvoidUntilMs    int64  `json:"avoidUntilMs"`
}

// AvoidanceTracker mirrors ClientIpAccountAvoidanceTracker. It is the opaque
// per-request handle (gatewaypreauth.ClientIPAccountAvoidanceTracker is
// interface{}).
type AvoidanceTracker struct {
	Scope                          *avoidanceScope
	PendingFailures                []AccountFailure
	pendingFailureIndexByAccountID map[string]int
}

// AvoidanceOptions configures the avoidance memory.
type AvoidanceOptions struct {
	Clock Clock
	// RuntimeStateDriver mirrors runtimeConfig.runtimeStateDriver
	// ('memory' | 'redis').
	RuntimeStateDriver string
	// StateRedisURL mirrors runtimeConfig.redis.stateUrl (redis driver).
	StateRedisURL string
	// RedisNamespace mirrors runtimeConfig.redis.namespace (redis driver).
	RedisNamespace string
	// StateStore overrides the constructed Redis store (tests / injected
	// handles). Requires StateStoreClose when it owns resources.
	StateStore       RuntimeStateStore
	StateStoreClose  func()
}

// Avoidance owns the client-IP account avoidance memory. It satisfies the
// G05 gatewaypreauth.ClientIPAccountAvoidanceFactory port.
type Avoidance struct {
	clock    Clock
	useRedis bool
	store    RuntimeStateStore
	closeFn  func()

	entries *orderedExpiryMap[avoidanceEntry]
}

// NewAvoidance builds the store.
func NewAvoidance(opts AvoidanceOptions) (*Avoidance, error) {
	clock := opts.Clock
	if clock == nil {
		clock = systemClock()
	}
	useRedis := opts.RuntimeStateDriver == RuntimeStateDriverRedis
	store := opts.StateStore
	closeFn := opts.StateStoreClose
	if useRedis && store == nil {
		constructed, constructedClose, err := NewRedisRuntimeStateStore(opts.StateRedisURL, opts.RedisNamespace, avoidanceStateStoreName)
		if err != nil {
			return nil, err
		}
		store = constructed
		closeFn = constructedClose
	}
	return &Avoidance{
		clock:    clock,
		useRedis: useRedis,
		store:    store,
		closeFn:  closeFn,
		entries:  newOrderedExpiryMap[avoidanceEntry](clock, clientIPAccountAvoidanceMaxEntries),
	}, nil
}

// Close disposes the Redis state store when this instance owns one.
func (a *Avoidance) Close() {
	if a.closeFn != nil {
		a.closeFn()
	}
}

// CreateTracker mirrors createClientIpAccountAvoidanceTracker and is the G05
// gatewaypreauth.ClientIPAccountAvoidanceFactory port.
func (a *Avoidance) CreateTracker(input gatewaypreauth.ClientIPAccountAvoidanceInput) gatewaypreauth.ClientIPAccountAvoidanceTracker {
	return a.CreateAvoidanceTracker(AvoidanceScopeInput{
		SystemAccountID: input.SystemAccountID,
		GroupID:         input.GroupID,
		APIKeyID:        input.APIKeyID,
		ClientIP:        input.ClientIP,
	})
}

// CreateAvoidanceTracker mirrors createClientIpAccountAvoidanceTracker with
// the full Node input.
func (a *Avoidance) CreateAvoidanceTracker(input AvoidanceScopeInput) *AvoidanceTracker {
	return &AvoidanceTracker{
		Scope:                          normalizeAvoidanceScope(input),
		PendingFailures:                []AccountFailure{},
		pendingFailureIndexByAccountID: map[string]int{},
	}
}

// ---------------------------------------------------------------------------
// ordering
// ---------------------------------------------------------------------------

// OrderAccountsByClientIPAccountAvoidance mirrors
// orderOpenAIAccountsByClientIpAccountAvoidance (memory path).
func (a *Avoidance) OrderAccountsByClientIPAccountAvoidance(
	accounts []gatewayruntimecache.OpenAIAccountSecret,
	input AvoidanceScopeInput,
	modelPriority *gatewayrouting.GatewayAccountModelPriority,
) AvoidanceOrderResult {
	scope := normalizeAvoidanceScope(input)
	if scope == nil || len(accounts) == 0 {
		return notAppliedAvoidanceOrder(accounts)
	}
	freshAccounts := make([]gatewayruntimecache.OpenAIAccountSecret, 0, len(accounts))
	avoidedAccounts := make([]gatewayruntimecache.OpenAIAccountSecret, 0)
	for i := range accounts {
		entry := a.getMemoryAvoidanceEntry(avoidanceEntryKey(scope, accounts[i].ID))
		if entry != nil && entry.FailureCount >= clientIPAccountAvoidanceActivationFailureThreshold {
			avoidedAccounts = append(avoidedAccounts, accounts[i])
		} else {
			freshAccounts = append(freshAccounts, accounts[i])
		}
	}
	return avoidanceOrderFromSplit(accounts, freshAccounts, avoidedAccounts, modelPriority)
}

// OrderAccountsByClientIPAccountAvoidanceAsync mirrors
// orderOpenAIAccountsByClientIpAccountAvoidanceAsync (redis path).
func (a *Avoidance) OrderAccountsByClientIPAccountAvoidanceAsync(
	ctx context.Context,
	accounts []gatewayruntimecache.OpenAIAccountSecret,
	input AvoidanceScopeInput,
	modelPriority *gatewayrouting.GatewayAccountModelPriority,
) (AvoidanceOrderResult, error) {
	if !a.useRedis {
		return a.OrderAccountsByClientIPAccountAvoidance(accounts, input, modelPriority), nil
	}
	scope := normalizeAvoidanceScope(input)
	if scope == nil || len(accounts) == 0 {
		return notAppliedAvoidanceOrder(accounts), nil
	}
	freshAccounts := make([]gatewayruntimecache.OpenAIAccountSecret, 0, len(accounts))
	avoidedAccounts := make([]gatewayruntimecache.OpenAIAccountSecret, 0)
	for i := range accounts {
		entry, err := a.getRedisAvoidanceEntry(ctx, avoidanceEntryKey(scope, accounts[i].ID))
		if err != nil {
			return AvoidanceOrderResult{}, err
		}
		if entry != nil && entry.FailureCount >= clientIPAccountAvoidanceActivationFailureThreshold {
			avoidedAccounts = append(avoidedAccounts, accounts[i])
		} else {
			freshAccounts = append(freshAccounts, accounts[i])
		}
	}
	result := avoidanceOrderFromSplit(accounts, freshAccounts, avoidedAccounts, modelPriority)
	return result, nil
}

func notAppliedAvoidanceOrder(accounts []gatewayruntimecache.OpenAIAccountSecret) AvoidanceOrderResult {
	return AvoidanceOrderResult{Accounts: accounts}
}

// avoidanceOrderFromSplit mirrors the shared tail of the two Node ordering
// functions (avoided empty → untouched; fresh empty → bypassedAllAvoided).
func avoidanceOrderFromSplit(
	accounts []gatewayruntimecache.OpenAIAccountSecret,
	freshAccounts []gatewayruntimecache.OpenAIAccountSecret,
	avoidedAccounts []gatewayruntimecache.OpenAIAccountSecret,
	modelPriority *gatewayrouting.GatewayAccountModelPriority,
) AvoidanceOrderResult {
	avoidedAccountIDs := accountIDsOf(avoidedAccounts)
	if len(avoidedAccounts) == 0 {
		return AvoidanceOrderResult{Accounts: accounts}
	}
	if len(freshAccounts) == 0 {
		return AvoidanceOrderResult{
			Accounts:           accounts,
			Applied:            false,
			AvoidedAccountIDs:  avoidedAccountIDs,
			BypassedAllAvoided: true,
		}
	}
	reordered := append(append([]gatewayruntimecache.OpenAIAccountSecret{}, freshAccounts...), avoidedAccounts...)
	return AvoidanceOrderResult{
		Accounts: PreserveGatewayAccountDispatchPriorityTiers(accounts, reordered, DispatchPriorityTierInput{
			ModelRankByAccountID: ModelRankByAccountID(modelPriority),
		}),
		Applied:            true,
		AvoidedAccountIDs:  avoidedAccountIDs,
		BypassedAllAvoided: false,
	}
}

func accountIDsOf(accounts []gatewayruntimecache.OpenAIAccountSecret) []string {
	ids := make([]string, 0, len(accounts))
	for i := range accounts {
		ids = append(ids, accounts[i].ID)
	}
	return ids
}

// ---------------------------------------------------------------------------
// pending failure tracking
// ---------------------------------------------------------------------------

// RememberPendingFailure mirrors rememberClientIpAccountPendingFailure:
// account is Pick<UpstreamAccount, 'id' | 'name'>.
func (a *Avoidance) RememberPendingFailure(
	tracker *AvoidanceTracker,
	accountID string,
	accountName string,
	failure AccountFailure,
) {
	if tracker == nil || tracker.Scope == nil {
		return
	}
	pendingFailure := failure
	pendingFailure.AccountID = accountID
	pendingFailure.AccountName = accountName
	if index, ok := tracker.pendingFailureIndexByAccountID[accountID]; ok {
		tracker.PendingFailures[index] = pendingFailure
		return
	}
	if len(tracker.PendingFailures) >= clientIPAccountAvoidanceMaxPendingFailures {
		return
	}
	tracker.pendingFailureIndexByAccountID[accountID] = len(tracker.PendingFailures)
	tracker.PendingFailures = append(tracker.PendingFailures, pendingFailure)
}

// TransferPendingFailures mirrors transferClientIpAccountPendingFailures.
func (a *Avoidance) TransferPendingFailures(source *AvoidanceTracker, target *AvoidanceTracker) {
	if source == nil || target == nil || source.Scope == nil || target.Scope == nil || len(source.PendingFailures) == 0 {
		return
	}
	for _, failure := range source.PendingFailures {
		a.RememberPendingFailure(target, failure.AccountID, failure.AccountName, AccountFailure{
			StatusCode:   failure.StatusCode,
			ErrorCode:    failure.ErrorCode,
			ErrorType:    failure.ErrorType,
			ErrorPhase:   failure.ErrorPhase,
			ErrorMessage: failure.ErrorMessage,
			Endpoint:     failure.Endpoint,
		})
	}
	clearTrackerPendingFailures(source)
}

// ConfirmAfterSuccess mirrors confirmClientIpAccountAvoidanceAfterSuccess.
func (a *Avoidance) ConfirmAfterSuccess(
	tracker *AvoidanceTracker,
	successAccountID string,
	settings *gatewayruntimecache.GatewaySettings,
) AvoidanceConfirmResult {
	if tracker == nil || tracker.Scope == nil {
		return AvoidanceConfirmResult{}
	}
	cleared := a.ClearForAccount(tracker, successAccountID)
	confirmed := a.confirmTrackerPendingFailures(tracker, settings, successAccountID)
	return AvoidanceConfirmResult{
		ConfirmedAccountIDs: uniqueStrings(confirmed),
		ClearedAccountID:    successAccountID,
		Cleared:             cleared,
	}
}

// ConfirmAfterSuccessAsync mirrors confirmClientIpAccountAvoidanceAfterSuccessAsync.
func (a *Avoidance) ConfirmAfterSuccessAsync(
	ctx context.Context,
	tracker *AvoidanceTracker,
	successAccountID string,
	settings *gatewayruntimecache.GatewaySettings,
) (AvoidanceConfirmResult, error) {
	if !a.useRedis {
		return a.ConfirmAfterSuccess(tracker, successAccountID, settings), nil
	}
	if tracker == nil || tracker.Scope == nil {
		return AvoidanceConfirmResult{}, nil
	}
	cleared, err := a.ClearForAccountAsync(ctx, tracker, successAccountID)
	if err != nil {
		return AvoidanceConfirmResult{}, err
	}
	confirmed, err := a.confirmTrackerPendingFailuresAsync(ctx, tracker, settings, successAccountID)
	if err != nil {
		return AvoidanceConfirmResult{}, err
	}
	return AvoidanceConfirmResult{
		ConfirmedAccountIDs: uniqueStrings(confirmed),
		ClearedAccountID:    successAccountID,
		Cleared:             cleared,
	}, nil
}

// ConfirmAfterFinalFailure mirrors confirmClientIpAccountAvoidanceAfterFinalFailure.
func (a *Avoidance) ConfirmAfterFinalFailure(
	tracker *AvoidanceTracker,
	settings *gatewayruntimecache.GatewaySettings,
) AvoidanceConfirmResult {
	if tracker == nil || tracker.Scope == nil {
		return AvoidanceConfirmResult{}
	}
	confirmed := a.confirmTrackerPendingFailures(tracker, settings, "")
	return AvoidanceConfirmResult{
		ConfirmedAccountIDs: uniqueStrings(confirmed),
	}
}

// ConfirmAfterFinalFailureAsync mirrors
// confirmClientIpAccountAvoidanceAfterFinalFailureAsync.
func (a *Avoidance) ConfirmAfterFinalFailureAsync(
	ctx context.Context,
	tracker *AvoidanceTracker,
	settings *gatewayruntimecache.GatewaySettings,
) (AvoidanceConfirmResult, error) {
	if !a.useRedis {
		return a.ConfirmAfterFinalFailure(tracker, settings), nil
	}
	if tracker == nil || tracker.Scope == nil {
		return AvoidanceConfirmResult{}, nil
	}
	confirmed, err := a.confirmTrackerPendingFailuresAsync(ctx, tracker, settings, "")
	if err != nil {
		return AvoidanceConfirmResult{}, err
	}
	return AvoidanceConfirmResult{
		ConfirmedAccountIDs: uniqueStrings(confirmed),
	}, nil
}

// confirmTrackerPendingFailures mirrors confirmTrackerPendingFailures.
func (a *Avoidance) confirmTrackerPendingFailures(
	tracker *AvoidanceTracker,
	settings *gatewayruntimecache.GatewaySettings,
	skipAccountID string,
) []string {
	scope := tracker.Scope
	if scope == nil {
		return nil
	}
	ttlMs := avoidanceTTL(settings)
	now := a.clock.Now().UnixMilli()
	confirmedAccountIDs := make([]string, 0, len(tracker.PendingFailures))
	for _, failure := range tracker.PendingFailures {
		if failure.AccountID == skipAccountID {
			continue
		}
		key := avoidanceEntryKey(scope, failure.AccountID)
		current := a.getMemoryAvoidanceEntry(key)
		entry := avoidanceEntryFromFailure(failure, key, scope, current, now, ttlMs)
		a.setMemoryAvoidanceEntry(key, entry, ttlMs)
		confirmedAccountIDs = append(confirmedAccountIDs, failure.AccountID)
	}
	clearTrackerPendingFailures(tracker)
	return confirmedAccountIDs
}

// confirmTrackerPendingFailuresAsync mirrors confirmTrackerPendingFailuresAsync.
func (a *Avoidance) confirmTrackerPendingFailuresAsync(
	ctx context.Context,
	tracker *AvoidanceTracker,
	settings *gatewayruntimecache.GatewaySettings,
	skipAccountID string,
) ([]string, error) {
	scope := tracker.Scope
	if scope == nil {
		return nil, nil
	}
	ttlMs := avoidanceTTL(settings)
	now := a.clock.Now().UnixMilli()
	confirmedAccountIDs := make([]string, 0, len(tracker.PendingFailures))
	for _, failure := range tracker.PendingFailures {
		if failure.AccountID == skipAccountID {
			continue
		}
		key := avoidanceEntryKey(scope, failure.AccountID)
		current, err := a.getRedisAvoidanceEntry(ctx, key)
		if err != nil {
			return nil, err
		}
		entry := avoidanceEntryFromFailure(failure, key, scope, current, now, ttlMs)
		if err := a.setRedisAvoidanceEntry(ctx, key, entry, ttlMs); err != nil {
			return nil, err
		}
		confirmedAccountIDs = append(confirmedAccountIDs, failure.AccountID)
	}
	clearTrackerPendingFailures(tracker)
	return confirmedAccountIDs, nil
}

// avoidanceEntryFromFailure builds the stored entry with the Node literal
// field order: failure spread first, then entryKey/scopeKey/failureCount/
// firstFailedAtMs/lastFailedAtMs/avoidUntilMs.
func avoidanceEntryFromFailure(
	failure AccountFailure,
	key string,
	scope *avoidanceScope,
	current *avoidanceEntry,
	now int64,
	ttlMs int64,
) avoidanceEntry {
	return avoidanceEntry{
		AccountID:       failure.AccountID,
		AccountName:     failure.AccountName,
		StatusCode:      failure.StatusCode,
		ErrorCode:       failure.ErrorCode,
		ErrorType:       failure.ErrorType,
		ErrorPhase:      failure.ErrorPhase,
		ErrorMessage:    failure.ErrorMessage,
		Endpoint:        failure.Endpoint,
		EntryKey:        key,
		ScopeKey:        avoidanceScopeKey(scope),
		FailureCount:    currentFailureCount(current) + 1,
		FirstFailedAtMs: currentFirstFailedAt(current, now),
		LastFailedAtMs:  now,
		AvoidUntilMs:    now + ttlMs,
	}
}

func currentFailureCount(current *avoidanceEntry) int {
	if current == nil {
		return 0
	}
	return current.FailureCount
}

func currentFirstFailedAt(current *avoidanceEntry, now int64) int64 {
	if current == nil {
		return now
	}
	return current.FirstFailedAtMs
}

func clearTrackerPendingFailures(tracker *AvoidanceTracker) {
	tracker.PendingFailures = []AccountFailure{}
	tracker.pendingFailureIndexByAccountID = map[string]int{}
}

// ---------------------------------------------------------------------------
// clearing + snapshots
// ---------------------------------------------------------------------------

// ClearForAccount mirrors clearClientIpAccountAvoidanceForAccount.
func (a *Avoidance) ClearForAccount(tracker *AvoidanceTracker, accountID string) bool {
	if tracker == nil || tracker.Scope == nil {
		return false
	}
	key := avoidanceEntryKey(tracker.Scope, accountID)
	existed := a.getMemoryAvoidanceEntry(key) != nil
	a.entries.Delete(key)
	return existed
}

// ClearForAccountAsync mirrors clearClientIpAccountAvoidanceForAccountAsync.
func (a *Avoidance) ClearForAccountAsync(ctx context.Context, tracker *AvoidanceTracker, accountID string) (bool, error) {
	if !a.useRedis {
		return a.ClearForAccount(tracker, accountID), nil
	}
	if tracker == nil || tracker.Scope == nil {
		return false, nil
	}
	key := avoidanceEntryKey(tracker.Scope, accountID)
	existing, err := a.getRedisAvoidanceEntry(ctx, key)
	if err != nil {
		return false, err
	}
	existed := existing != nil
	if err := a.store.Delete(ctx, redisAvoidanceStateKey(key)); err != nil {
		return existed, err
	}
	return existed, nil
}

// ClearForTest mirrors clearClientIpAccountAvoidanceForTest.
func (a *Avoidance) ClearForTest() {
	a.entries = newOrderedExpiryMap[avoidanceEntry](a.clock, clientIPAccountAvoidanceMaxEntries)
}

// AvoidanceSnapshotRow mirrors the getClientIpAccountAvoidanceSnapshotForTest
// row.
type AvoidanceSnapshotRow struct {
	AccountID       string
	FailureCount    int
	Active          bool
	ClientIP        string
	APIKeyID        string
	SystemAccountID string
}

// SnapshotForTest mirrors getClientIpAccountAvoidanceSnapshotForTest.
func (a *Avoidance) SnapshotForTest() []AvoidanceSnapshotRow {
	values := a.entries.Values()
	snapshot := make([]AvoidanceSnapshotRow, 0, len(values))
	for _, entry := range values {
		scope := parseAvoidanceScopeKey(entry.ScopeKey)
		snapshot = append(snapshot, AvoidanceSnapshotRow{
			AccountID:       entry.AccountID,
			FailureCount:    entry.FailureCount,
			Active:          entry.FailureCount >= clientIPAccountAvoidanceActivationFailureThreshold,
			ClientIP:        scope.clientIP,
			APIKeyID:        scope.apiKeyID,
			SystemAccountID: scope.systemAccountID,
		})
	}
	return snapshot
}

// ---------------------------------------------------------------------------
// memory entries
// ---------------------------------------------------------------------------

// getMemoryAvoidanceEntry mirrors getMemoryClientIpAccountAvoidanceEntry.
func (a *Avoidance) getMemoryAvoidanceEntry(key string) *avoidanceEntry {
	value, ok := a.entries.Get(key)
	if !ok {
		return nil
	}
	return &value
}

// setMemoryAvoidanceEntry mirrors setMemoryClientIpAccountAvoidanceEntry.
func (a *Avoidance) setMemoryAvoidanceEntry(key string, value avoidanceEntry, ttlMs int64) {
	a.entries.Set(key, value, ttlMs)
}

// ---------------------------------------------------------------------------
// redis entries
// ---------------------------------------------------------------------------

func redisAvoidanceStateKey(key string) string {
	return "entry:" + key
}

func (a *Avoidance) getRedisAvoidanceEntry(ctx context.Context, key string) (*avoidanceEntry, error) {
	var entry avoidanceEntry
	found, err := a.store.GetJSON(ctx, redisAvoidanceStateKey(key), &entry)
	if err != nil || !found {
		return nil, err
	}
	return &entry, nil
}

func (a *Avoidance) setRedisAvoidanceEntry(ctx context.Context, key string, entry avoidanceEntry, ttlMs int64) error {
	return a.store.SetJSON(ctx, redisAvoidanceStateKey(key), entry, ttlMs)
}

// ---------------------------------------------------------------------------
// keys + ttl
// ---------------------------------------------------------------------------

// avoidanceEntryKey mirrors entryKey.
func avoidanceEntryKey(scope *avoidanceScope, accountID string) string {
	return avoidanceScopeKey(scope) + ":" + accountID
}

// avoidanceScopeKey mirrors scopeKey: JSON.stringify field order is
// systemAccountId, apiKeyId, clientIp — byte-stable so Node and Go instances
// share Redis state keys during coexistence.
func avoidanceScopeKey(scope *avoidanceScope) string {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(struct {
		SystemAccountID string `json:"systemAccountId"`
		APIKeyID        string `json:"apiKeyId"`
		ClientIP        string `json:"clientIp"`
	}{
		SystemAccountID: scope.systemAccountID,
		APIKeyID:        scope.apiKeyID,
		ClientIP:        scope.clientIP,
	}); err != nil {
		return ""
	}
	return strings.TrimRight(buffer.String(), "\n")
}

// parseAvoidanceScopeKey mirrors parseScopeKey (malformed keys degrade to
// empty scope parts).
func parseAvoidanceScopeKey(value string) avoidanceScope {
	var parsed struct {
		SystemAccountID string `json:"systemAccountId"`
		APIKeyID        string `json:"apiKeyId"`
		ClientIP        string `json:"clientIp"`
	}
	if err := json.Unmarshal([]byte(value), &parsed); err != nil {
		return avoidanceScope{}
	}
	return avoidanceScope{
		systemAccountID: parsed.SystemAccountID,
		apiKeyID:        parsed.APIKeyID,
		clientIP:        parsed.ClientIP,
	}
}

// avoidanceTTL mirrors avoidanceTtlMs.
func avoidanceTTL(settings *gatewayruntimecache.GatewaySettings) int64 {
	var numeric int64
	if settings != nil {
		numeric = maxInt64(1, settings.DefaultTemporaryUnschedulableMinutes)
	} else {
		numeric = clientIPAccountAvoidanceDefaultTTL / 60_000
	}
	numericMs := numeric * 60_000
	if numericMs > clientIPAccountAvoidanceMaxTTL {
		return clientIPAccountAvoidanceMaxTTL
	}
	return numericMs
}

// normalizeAvoidanceScope mirrors normalizeScope.
func normalizeAvoidanceScope(input AvoidanceScopeInput) *avoidanceScope {
	clientIP := strings.TrimSpace(input.ClientIP)
	if clientIP == "" {
		return nil
	}
	apiKeyID := strings.TrimSpace(input.APIKeyID)
	if apiKeyID == "" {
		apiKeyID = "internal"
	}
	return &avoidanceScope{
		systemAccountID: input.SystemAccountID,
		apiKeyID:        apiKeyID,
		clientIP:        clientIP,
	}
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	output := make([]string, 0, len(values))
	for _, value := range values {
		if seen[value] {
			continue
		}
		seen[value] = true
		output = append(output, value)
	}
	return output
}

// Compile-time G05 port assertion for G20 assembly.
var _ gatewaypreauth.ClientIPAccountAvoidanceFactory = (*Avoidance)(nil)
