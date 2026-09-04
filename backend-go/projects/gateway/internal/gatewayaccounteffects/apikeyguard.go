package gatewayaccounteffects

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// Failure guard constants (account-api-key-failure-guard.service.ts).
var apiKeyLocalSuppressionDelayMs = []int64{3_000, 5_000, 10_000}

const (
	apiKeyLocalSuppressionMaxMs           = int64(10 * 60_000)
	apiKeyLocalObservationFenceRetentionMs = int64(10 * 60_000)
	apiKeyLocalObservationFenceCapacity    = 50_000
	apiKeyDistributedStateStoreName        = "gateway-account-api-key-transient-avoidance"
)

// AccountApiKeyRuntimeTarget mirrors AccountApiKeyRuntimeTarget.
type AccountApiKeyRuntimeTarget struct {
	AccountID          string
	KeyFingerprint     string
	KeyIndex           *int
	TransientGeneration string
}

// LocalApiKeySuppression mirrors LocalApiKeySuppression.
type LocalApiKeySuppression struct {
	AccountID       string
	KeyFingerprint  string
	KeyIndex        *int
	Status          AccountApiKeyFailureStatus
	FailureCount    int
	FirstFailedAtMs int64
	LastFailedAtMs  int64
	SuppressUntilMs int64
	Reason          *string
}

// LocalApiKeyObservationFence mirrors LocalApiKeyObservationFence.
type LocalApiKeyObservationFence struct {
	LatestEpoch int64
	ExpiresAtMs int64
}

// GatewayAccountApiKeyFailureGuardInput mirrors GatewayAccountApiKeyFailureGuardInput.
type GatewayAccountApiKeyFailureGuardInput struct {
	Status          AccountApiKeyFailureStatus
	StatusCode      *int64
	ErrorCode       *string
	ErrorMessage    *string
	TrafficSource   string
	MutationContext *AccountApiKeyPersistentMutationContext
	ClientIP        string
	APIKeyID        string
	ObservationEpoch *int64
	Source          string
}

// Failure guard decision reasons.
const (
	GuardReasonNotSelectedAPIKey              = "not_selected_api_key"
	GuardReasonPersistentMutationAuthorized   = "persistent_mutation_authorized"
	GuardReasonPersistentMutationUnauthorized = "persistent_mutation_unauthorized"
	GuardReasonGatewayLocalOnly               = "gateway_local_only"
	GuardReasonStaleGatewayObservation        = "stale_gateway_observation"
	GuardReasonRedisTransientOnly             = "redis_transient_only"
)

// GatewayAccountApiKeyFailureGuardDecision mirrors GatewayAccountApiKeyFailureGuardDecision.
type GatewayAccountApiKeyFailureGuardDecision struct {
	Persist              bool
	Reason               string
	FailureCount         *int
	DistinctClientIPCount *int
	DistinctAPIKeyCount  *int
	SuccessCount         *int
	FailureRatio         *float64
}

// GatewayAccountApiKeyFailureGuardSnapshotEntry mirrors the snapshot entry.
type GatewayAccountApiKeyFailureGuardSnapshotEntry struct {
	AccountID         string
	KeyFingerprint    string
	Status            AccountApiKeyFailureStatus
	LocalFailureCount int
	StormFailureCount *int
	DistinctClientIPCount *int
	DistinctAPIKeyCount   *int
	Suppressed        bool
}

// AccountApiKeyRuntimeSelectionState mirrors the dispatch selection state the
// guard reports (storage/account-api-key-rotation.ts runtime subset).
type AccountApiKeyRuntimeSelectionState struct {
	APIKeyID            string  `json:"apiKeyId,omitempty"`
	KeyFingerprint      string  `json:"fingerprint"`
	Status              string  `json:"status"`
	KeyIndex            *int    `json:"keyIndex,omitempty"`
	NextProbeAt         *string `json:"nextProbeAt,omitempty"`
	TransientGeneration *string `json:"transientGeneration,omitempty"`
}

// TransientStateStoreFactory builds the distributed transient store lazily
// (Node gatewayAccountApiKeyTransientStateStore()).
type TransientStateStoreFactory func() (AccountApiKeyTransientStateStore, error)

// AccountAPIKeyFailureGuard mirrors the module-level state of
// account-api-key-failure-guard.service.ts.
type AccountAPIKeyFailureGuard struct {
	clock     Clock
	config    SideEffectsConfig
	logger    Logger
	newStore  TransientStateStoreFactory

	mu               sync.Mutex
	suppressions     map[string]*LocalApiKeySuppression
	fences           map[string]*LocalApiKeyObservationFence
	observationEpoch int64
	storeOverride    AccountApiKeyTransientStateStore
	store            AccountApiKeyTransientStateStore
}

// NewAccountAPIKeyFailureGuard builds the guard. defaultStoreFactory is used
// lazily in the redis driver; nil falls back to the Redis store built from
// JUHE_AI_REDIS_STATE_URL configuration supplied at construction time.
func NewAccountAPIKeyFailureGuard(config SideEffectsConfig, clock Clock, logger Logger, defaultStoreFactory TransientStateStoreFactory) *AccountAPIKeyFailureGuard {
	if clock == nil {
		clock = SystemClock{}
	}
	if logger == nil {
		logger = NopLogger{}
	}
	return &AccountAPIKeyFailureGuard{
		clock:        clock,
		config:       config,
		logger:       logger,
		newStore:     defaultStoreFactory,
		suppressions: map[string]*LocalApiKeySuppression{},
		fences:       map[string]*LocalApiKeyObservationFence{},
	}
}

// SetTransientStateStoreForTest mirrors setGatewayAccountApiKeyTransientStateStoreForTest.
func (g *AccountAPIKeyFailureGuard) SetTransientStateStoreForTest(store AccountApiKeyTransientStateStore) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.storeOverride = store
	g.store = nil
}

// CaptureFailureObservation mirrors captureGatewayAccountApiKeyFailureObservation.
func (g *AccountAPIKeyFailureGuard) CaptureFailureObservation(account gatewayruntimecache.OpenAIAccountSecret) *int64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.canUseProcessLocalAPIKeyRuntimeStateLocked() {
		return nil
	}
	target, ok := accountAPIKeyRuntimeTarget(account)
	if !ok {
		return nil
	}
	now := NowMs(g.clock)
	epoch := g.nextLocalAPIKeyObservationEpochLocked()
	g.rememberLocalAPIKeyObservationFenceLocked(apiKeyRuntimeKey(target), epoch, now)
	return &epoch
}

// RecordFailureGuard mirrors recordGatewayAccountApiKeyFailureGuard.
func (g *AccountAPIKeyFailureGuard) RecordFailureGuard(account gatewayruntimecache.OpenAIAccountSecret, input GatewayAccountApiKeyFailureGuardInput) GatewayAccountApiKeyFailureGuardDecision {
	target, ok := accountAPIKeyRuntimeTarget(account)
	if !ok {
		return GatewayAccountApiKeyFailureGuardDecision{Persist: false, Reason: GuardReasonNotSelectedAPIKey}
	}
	authorization := AuthorizeAccountApiKeyPersistentMutationForTrafficSource(MutationKindFailure, input.TrafficSource, input.MutationContext)
	if authorization.Allowed {
		return GatewayAccountApiKeyFailureGuardDecision{Persist: true, Reason: GuardReasonPersistentMutationAuthorized}
	}
	if input.MutationContext != nil {
		return GatewayAccountApiKeyFailureGuardDecision{Persist: false, Reason: GuardReasonPersistentMutationUnauthorized}
	}
	if input.TrafficSource != TrafficSourceGateway {
		return GatewayAccountApiKeyFailureGuardDecision{Persist: false, Reason: GuardReasonPersistentMutationUnauthorized}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.canUseProcessLocalAPIKeyRuntimeStateLocked() {
		return GatewayAccountApiKeyFailureGuardDecision{Persist: false, Reason: GuardReasonRedisTransientOnly}
	}
	if !g.acceptLocalAPIKeyFailureObservationLocked(target, input.ObservationEpoch) {
		return GatewayAccountApiKeyFailureGuardDecision{Persist: false, Reason: GuardReasonStaleGatewayObservation}
	}
	status := normalizeFailureStatus(input.Status)
	g.rememberLocalAPISuppressionLocked(target, status, input.ErrorMessage)
	return GatewayAccountApiKeyFailureGuardDecision{Persist: false, Reason: GuardReasonGatewayLocalOnly}
}

// RecordTransientFailure mirrors recordGatewayAccountApiKeyTransientFailure.
func (g *AccountAPIKeyFailureGuard) RecordTransientFailure(ctx context.Context, account gatewayruntimecache.OpenAIAccountSecret, status AccountApiKeyFailureStatus) (bool, error) {
	if !g.config.IsRedisDriver() {
		return false, nil
	}
	target, ok := accountAPIKeyRuntimeTarget(account)
	if !ok || target.TransientGeneration == "" {
		return false, nil
	}
	store, err := g.transientStateStore()
	if err != nil {
		return false, err
	}
	result, err := store.RecordFailure(ctx, TransientMutationInput{
		Target: AccountApiKeyTransientTarget{
			AccountID:      target.AccountID,
			KeyFingerprint: target.KeyFingerprint,
			KeyIndex:       target.KeyIndex,
		},
		Status:             normalizeFailureStatus(status),
		ExpectedGeneration: target.TransientGeneration,
	})
	if err != nil {
		return false, err
	}
	return result.Applied, nil
}

// LoadTransientStatesForDispatch mirrors loadGatewayAccountApiKeyTransientStatesForDispatch.
func (g *AccountAPIKeyFailureGuard) LoadTransientStatesForDispatch(ctx context.Context, accountID string, keyFingerprints []string) ([]AccountApiKeyRuntimeSelectionState, error) {
	if !g.config.IsRedisDriver() {
		return g.LocalRuntimeStatesForDispatch(accountID), nil
	}
	normalizedAccountID := strings.TrimSpace(accountID)
	if normalizedAccountID == "" {
		return []AccountApiKeyRuntimeSelectionState{}, nil
	}
	fingerprints := uniqueNonEmpty(keyFingerprints)
	if len(fingerprints) == 0 {
		return []AccountApiKeyRuntimeSelectionState{}, nil
	}
	store, err := g.transientStateStore()
	if err != nil {
		return nil, err
	}
	states, err := store.LoadMany(ctx, normalizedAccountID, fingerprints)
	if err != nil {
		return nil, err
	}
	output := make([]AccountApiKeyRuntimeSelectionState, 0, len(states))
	for _, entry := range states {
		state := entry.State
		if state == nil {
			continue
		}
		selection := AccountApiKeyRuntimeSelectionState{
			KeyFingerprint:      state.KeyFingerprint,
			KeyIndex:            state.KeyIndex,
			Status:              "active",
			TransientGeneration: &state.Generation,
		}
		if entry.Suppressed && state.Status != "" {
			selection.Status = string(state.Status)
		}
		if entry.Suppressed && state.SuppressUntilMs != nil {
			nextProbeAt := canonicalRFC3339(time.UnixMilli(*state.SuppressUntilMs))
			selection.NextProbeAt = &nextProbeAt
		}
		output = append(output, selection)
	}
	return output, nil
}

// ClearTransientFailure mirrors clearGatewayAccountApiKeyTransientFailure.
func (g *AccountAPIKeyFailureGuard) ClearTransientFailure(ctx context.Context, account gatewayruntimecache.OpenAIAccountSecret) (bool, error) {
	if !g.config.IsRedisDriver() {
		return false, nil
	}
	target, ok := accountAPIKeyRuntimeTarget(account)
	if !ok || target.TransientGeneration == "" {
		return false, nil
	}
	store, err := g.transientStateStore()
	if err != nil {
		return false, err
	}
	result, err := store.RecordSuccess(ctx, TransientMutationInput{
		Target: AccountApiKeyTransientTarget{
			AccountID:      target.AccountID,
			KeyFingerprint: target.KeyFingerprint,
			KeyIndex:       target.KeyIndex,
		},
		ExpectedGeneration: target.TransientGeneration,
	})
	if err != nil {
		return false, err
	}
	return result.Applied || (result.State != nil && result.State.ObservationKind == "success"), nil
}

// ClearFailureGuard mirrors clearGatewayAccountApiKeyFailureGuard.
func (g *AccountAPIKeyFailureGuard) ClearFailureGuard(account gatewayruntimecache.OpenAIAccountSecret) bool {
	target, ok := accountAPIKeyRuntimeTarget(account)
	if !ok {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.canUseProcessLocalAPIKeyRuntimeStateLocked() {
		return false
	}
	key := apiKeyRuntimeKey(target)
	if _, exists := g.suppressions[key]; !exists {
		return false
	}
	delete(g.suppressions, key)
	return true
}

// RecordSuccessGuard mirrors recordGatewayAccountApiKeySuccessGuard.
func (g *AccountAPIKeyFailureGuard) RecordSuccessGuard(account gatewayruntimecache.OpenAIAccountSecret) bool {
	target, ok := accountAPIKeyRuntimeTarget(account)
	if !ok {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.canUseProcessLocalAPIKeyRuntimeStateLocked() {
		return false
	}
	now := NowMs(g.clock)
	g.rememberLocalAPIKeyObservationFenceLocked(apiKeyRuntimeKey(target), g.nextLocalAPIKeyObservationEpochLocked(), now)
	return g.clearFailureGuardLocked(target)
}

// LocalRuntimeStatesForDispatch mirrors localAccountApiKeyRuntimeStatesForDispatch.
func (g *AccountAPIKeyFailureGuard) LocalRuntimeStatesForDispatch(accountID string) []AccountApiKeyRuntimeSelectionState {
	normalizedAccountID := strings.TrimSpace(accountID)
	if normalizedAccountID == "" {
		return []AccountApiKeyRuntimeSelectionState{}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.canUseProcessLocalAPIKeyRuntimeStateLocked() {
		return []AccountApiKeyRuntimeSelectionState{}
	}
	g.cleanupExpiredAPIKeyRuntimeStateLocked(NowMs(g.clock))
	now := NowMs(g.clock)
	states := []AccountApiKeyRuntimeSelectionState{}
	for _, suppression := range g.suppressions {
		if suppression.AccountID != normalizedAccountID || suppression.SuppressUntilMs <= now {
			continue
		}
		nextProbeAt := canonicalRFC3339(time.UnixMilli(suppression.SuppressUntilMs))
		states = append(states, AccountApiKeyRuntimeSelectionState{
			KeyFingerprint: suppression.KeyFingerprint,
			KeyIndex:       suppression.KeyIndex,
			Status:         string(suppression.Status),
			NextProbeAt:    &nextProbeAt,
		})
	}
	return states
}

// SnapshotForTest mirrors getGatewayAccountApiKeyFailureGuardSnapshotForTest.
func (g *AccountAPIKeyFailureGuard) SnapshotForTest() []GatewayAccountApiKeyFailureGuardSnapshotEntry {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.canUseProcessLocalAPIKeyRuntimeStateLocked() {
		return []GatewayAccountApiKeyFailureGuardSnapshotEntry{}
	}
	now := NowMs(g.clock)
	g.cleanupExpiredAPIKeyRuntimeStateLocked(now)
	output := make([]GatewayAccountApiKeyFailureGuardSnapshotEntry, 0, len(g.suppressions))
	for _, suppression := range g.suppressions {
		output = append(output, GatewayAccountApiKeyFailureGuardSnapshotEntry{
			AccountID:         suppression.AccountID,
			KeyFingerprint:    suppression.KeyFingerprint,
			Status:            suppression.Status,
			LocalFailureCount: suppression.FailureCount,
			Suppressed:        suppression.SuppressUntilMs > now,
		})
	}
	return output
}

// ClearForTest mirrors clearGatewayAccountApiKeyFailureGuardsForTest.
func (g *AccountAPIKeyFailureGuard) ClearForTest() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.suppressions = map[string]*LocalApiKeySuppression{}
	g.fences = map[string]*LocalApiKeyObservationFence{}
}

func (g *AccountAPIKeyFailureGuard) clearFailureGuardLocked(target AccountApiKeyRuntimeTarget) bool {
	key := apiKeyRuntimeKey(target)
	if _, exists := g.suppressions[key]; !exists {
		return false
	}
	delete(g.suppressions, key)
	return true
}

func (g *AccountAPIKeyFailureGuard) rememberLocalAPISuppressionLocked(target AccountApiKeyRuntimeTarget, status AccountApiKeyFailureStatus, reason *string) {
	if !g.canUseProcessLocalAPIKeyRuntimeStateLocked() {
		return
	}
	now := NowMs(g.clock)
	key := apiKeyRuntimeKey(target)
	current := g.suppressions[key]
	failureCount := 1
	firstFailedAtMs := now
	if current != nil {
		failureCount = current.FailureCount + 1
		firstFailedAtMs = current.FirstFailedAtMs
	}
	delayIndex := failureCount - 1
	if delayIndex >= len(apiKeyLocalSuppressionDelayMs) {
		delayIndex = len(apiKeyLocalSuppressionDelayMs) - 1
	}
	if delayIndex < 0 {
		delayIndex = 0
	}
	delayMs := apiKeyLocalSuppressionDelayMs[delayIndex]
	suppressUntil := now + delayMs
	if delayMs > apiKeyLocalSuppressionMaxMs {
		suppressUntil = now + apiKeyLocalSuppressionMaxMs
	}
	keyIndex := target.KeyIndex
	g.suppressions[key] = &LocalApiKeySuppression{
		AccountID:       target.AccountID,
		KeyFingerprint:  target.KeyFingerprint,
		KeyIndex:        keyIndex,
		Status:          status,
		FailureCount:    failureCount,
		FirstFailedAtMs: firstFailedAtMs,
		LastFailedAtMs:  now,
		SuppressUntilMs: suppressUntil,
		Reason:          reason,
	}
}

func (g *AccountAPIKeyFailureGuard) cleanupExpiredAPIKeyRuntimeStateLocked(now int64) {
	for key, suppression := range g.suppressions {
		if suppression.SuppressUntilMs <= now {
			delete(g.suppressions, key)
		}
	}
}

func (g *AccountAPIKeyFailureGuard) acceptLocalAPIKeyFailureObservationLocked(target AccountApiKeyRuntimeTarget, observationEpoch *int64) bool {
	now := NowMs(g.clock)
	key := apiKeyRuntimeKey(target)
	if observationEpoch == nil {
		g.rememberLocalAPIKeyObservationFenceLocked(key, g.nextLocalAPIKeyObservationEpochLocked(), now)
		return true
	}
	epoch := *observationEpoch
	if !isSafeInteger(epoch) || epoch <= 0 {
		return false
	}
	current := g.fences[key]
	if current == nil || current.ExpiresAtMs <= now || current.LatestEpoch != epoch {
		if current != nil && current.ExpiresAtMs <= now {
			delete(g.fences, key)
		}
		return false
	}
	current.ExpiresAtMs = now + apiKeyLocalObservationFenceRetentionMs
	return true
}

func (g *AccountAPIKeyFailureGuard) rememberLocalAPIKeyObservationFenceLocked(key string, epoch int64, now int64) {
	delete(g.fences, key)
	g.fences[key] = &LocalApiKeyObservationFence{LatestEpoch: epoch, ExpiresAtMs: now + apiKeyLocalObservationFenceRetentionMs}
	for len(g.fences) > apiKeyLocalObservationFenceCapacity {
		oldestKey := ""
		for candidate := range g.fences {
			oldestKey = candidate
			break
		}
		if oldestKey == "" {
			break
		}
		delete(g.fences, oldestKey)
	}
}

func (g *AccountAPIKeyFailureGuard) nextLocalAPIKeyObservationEpochLocked() int64 {
	if g.observationEpoch >= safeIntegerMax {
		g.observationEpoch = 0
		g.fences = map[string]*LocalApiKeyObservationFence{}
	}
	g.observationEpoch++
	return g.observationEpoch
}

func (g *AccountAPIKeyFailureGuard) transientStateStore() (AccountApiKeyTransientStateStore, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.storeOverride != nil {
		return g.storeOverride, nil
	}
	if g.store != nil {
		return g.store, nil
	}
	if g.newStore == nil {
		return nil, errors.New("JUHE_AI_REDIS_STATE_URL 在 Redis runtime state driver 下必须配置")
	}
	store, err := g.newStore()
	if err != nil {
		return nil, err
	}
	g.store = store
	return store, nil
}

func (g *AccountAPIKeyFailureGuard) canUseProcessLocalAPIKeyRuntimeStateLocked() bool {
	if !g.config.IsRedisDriver() {
		return true
	}
	g.suppressions = map[string]*LocalApiKeySuppression{}
	g.fences = map[string]*LocalApiKeyObservationFence{}
	return false
}

// accountAPIKeyRuntimeTarget mirrors accountApiKeyRuntimeTarget.
func accountAPIKeyRuntimeTarget(account gatewayruntimecache.OpenAIAccountSecret) (AccountApiKeyRuntimeTarget, bool) {
	keyFingerprint := ""
	if account.SelectedAPIKeyFingerprint != nil {
		keyFingerprint = strings.TrimSpace(*account.SelectedAPIKeyFingerprint)
	}
	if keyFingerprint == "" {
		return AccountApiKeyRuntimeTarget{}, false
	}
	accountID := account.ID
	if account.CredentialSourceAccountID != nil && strings.TrimSpace(*account.CredentialSourceAccountID) != "" {
		accountID = strings.TrimSpace(*account.CredentialSourceAccountID)
	}
	if accountID == "" {
		return AccountApiKeyRuntimeTarget{}, false
	}
	target := AccountApiKeyRuntimeTarget{AccountID: accountID, KeyFingerprint: keyFingerprint}
	if account.SelectedAPIKeyIndex != nil {
		keyIndex := *account.SelectedAPIKeyIndex
		target.KeyIndex = &keyIndex
	}
	if account.SelectedAPIKeyTransientGeneration != nil && strings.TrimSpace(*account.SelectedAPIKeyTransientGeneration) != "" {
		generation := strings.TrimSpace(*account.SelectedAPIKeyTransientGeneration)
		target.TransientGeneration = generation
	}
	return target, true
}

func apiKeyRuntimeKey(target AccountApiKeyRuntimeTarget) string {
	return target.AccountID + ":" + target.KeyFingerprint
}

func normalizeFailureStatus(status AccountApiKeyFailureStatus) AccountApiKeyFailureStatus {
	if status == APIKeyStatusRateLimited || status == APIKeyStatusError {
		return status
	}
	return APIKeyStatusTemporaryUnavailable
}
