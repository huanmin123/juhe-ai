package gatewaycodex

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/rand"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycircuit"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// Port of client-profiles/codex-turn-retry.service.ts: the codex turn
// failure avoidance state (memory + Redis dual driver), account reordering
// and precise fence-scoped clearing.
//
// The persistence implementation originated as Codex turn retry. Its state
// key is now supplied by the common source resolver.

// CodexTurnFailureEvidence mirrors CodexTurnFailureEvidence.
type CodexTurnFailureEvidence = string

// Failure evidence kinds.
const (
	EvidenceRetryableFailure          = "retryable_failure"
	EvidenceCommittedRetrySignal      = "committed_retry_signal"
	EvidenceIncompleteDownstreamAbort = "incomplete_downstream_abort"
)

const (
	codexTurnRetryTtlMs                       = 30 * 60_000
	codexTurnAccountAvoidanceFailureThreshold = 2
	codexTurnIncompleteAbortWindowMs          = 60_000
	codexTurnRecentObservationLimit           = 32
	codexTurnRedisMutationMaxAttempts         = 16
	codexTurnRetryMaxEntries                  = 5000
)

// CodexTurnAccountAvoidanceResult mirrors CodexTurnAccountAvoidanceResult.
type CodexTurnAccountAvoidanceResult struct {
	Accounts           []gatewayruntimecache.OpenAIAccountSecret
	Applied            bool
	ThresholdReached   bool
	FailureCount       int
	AvoidedAccountIDs  []string
	BypassedAllAvoided bool
}

// CodexTurnFailureActivation mirrors the activation payload.
type CodexTurnFailureActivation struct {
	AccountID        string
	SourceGeneration int64
	SourceFenceID    string
}

// CodexTurnFailureRecordResult mirrors CodexTurnFailureRecordResult.
type CodexTurnFailureRecordResult struct {
	StateKey                     string
	FailureCount                 int
	FailedAccountIDs             []string
	AvoidanceActivatedAccountIDs []string
	DuplicateObservation         bool
	Activation                   *CodexTurnFailureActivation
}

// CodexTurnFailureInput mirrors the rememberCodexTurnStreamFailure input.
type CodexTurnFailureInput struct {
	ErrorCode     string
	Message       string
	Evidence      CodexTurnFailureEvidence
	ObservationID string
}

type codexTurnFailedAccount struct {
	AccountID                                  string   `json:"accountId"`
	FailureCount                               int      `json:"failureCount"`
	LastErrorCode                              string   `json:"lastErrorCode,omitempty"`
	LastErrorMessage                           string   `json:"lastErrorMessage,omitempty"`
	LastFailedAtMs                             int64    `json:"lastFailedAtMs"`
	RetryableFailureCount                      *int     `json:"retryableFailureCount,omitempty"`
	CommittedRetrySignalCount                  *int     `json:"committedRetrySignalCount,omitempty"`
	IncompleteDownstreamAbortCount             *int     `json:"incompleteDownstreamAbortCount,omitempty"`
	IncompleteDownstreamAbortWindowStartedAtMs *int64   `json:"incompleteDownstreamAbortWindowStartedAtMs,omitempty"`
	LastIncompleteDownstreamAbortAtMs          *int64   `json:"lastIncompleteDownstreamAbortAtMs,omitempty"`
	AvoidanceGeneration                        *int64   `json:"avoidanceGeneration,omitempty"`
	AvoidanceFenceID                           *string  `json:"avoidanceFenceId,omitempty"`
	RecentObservationIDs                       []string `json:"recentObservationIds,omitempty"`
}

type codexTurnRetryState struct {
	StateKey       string                             `json:"stateKey"`
	FailureCount   int                                `json:"failureCount"`
	FailedAccounts map[string]*codexTurnFailedAccount `json:"failedAccounts"`
	CreatedAtMs    int64                              `json:"createdAtMs"`
	UpdatedAtMs    int64                              `json:"updatedAtMs"`
}

// TurnRetryStateStore ports the consumed surface of
// shared/runtime-state-store.ts (getJson / compareSetJson / incr). A nil
// store selects the in-memory driver (Node
// runtimeConfig.runtimeStateDriver !== 'redis').
type TurnRetryStateStore interface {
	GetJSON(ctx context.Context, key string) (json.RawMessage, error)
	CompareSetJSON(ctx context.Context, key string, expected json.RawMessage, next any, ttlMs int64) (bool, error)
	Incr(ctx context.Context, key string, ttlMs int64) (int64, error)
}

// TurnRetryService carries the codex turn retry collaborators.
type TurnRetryService struct {
	Secret   string
	Clock    Clock
	CreateID IDGenerator
	Logger   gatewaypreauth.Logger
	// Store nil selects the memory driver; non-nil selects the Redis-shaped
	// driver for every async entry point.
	Store TurnRetryStateStore

	memory codexTurnMemoryState
}

type codexTurnMemoryState struct {
	mu          sync.Mutex
	entries     map[string]*memoryCodexTurnRetryEntry
	generations map[string]*avoidanceGenerationTombstone
	// mutationLocks serializes the redis-shaped mutation tails per key.
	mutationLocks map[string]*sync.Mutex
}

type memoryCodexTurnRetryEntry struct {
	value     codexTurnRetryState
	expiresAt int64
}

type avoidanceGenerationTombstone struct {
	generation  int64
	expiresAtMs int64
}

func (m *codexTurnMemoryState) init() {
	if m.entries == nil {
		m.entries = map[string]*memoryCodexTurnRetryEntry{}
	}
	if m.generations == nil {
		m.generations = map[string]*avoidanceGenerationTombstone{}
	}
	if m.mutationLocks == nil {
		m.mutationLocks = map[string]*sync.Mutex{}
	}
}

func (s *TurnRetryService) nowMs() int64 { return NowMs(s.Clock) }

func (s *TurnRetryService) createID() string {
	if s.CreateID != nil {
		return s.CreateID()
	}
	return RandomUUID()
}

func (s *TurnRetryService) newID() string {
	if s.CreateID != nil {
		return s.CreateID()
	}
	return RandomUUID()
}

// ---------------------------------------------------------------------------
// ordering
// ---------------------------------------------------------------------------

// OrderOpenAIAccountsByCodexTurnAvoidance mirrors
// orderOpenAIAccountsByCodexTurnAvoidance (memory driver).
func (s *TurnRetryService) OrderOpenAIAccountsByCodexTurnAvoidance(accounts []gatewayruntimecache.OpenAIAccountSecret, strategy OpenAIGatewayClientStrategyContext, modelPriority *gatewayrouting.GatewayAccountModelPriority) CodexTurnAccountAvoidanceResult {
	stateKey := strategy.ClientSourceAvoidanceStateKey
	var state *codexTurnRetryState
	if stateKey != "" {
		states := make([]*codexTurnRetryState, 0, len(accounts))
		for _, account := range accounts {
			states = append(states, s.getMemoryCodexTurnRetryState(codexTurnAccountStateKey(s.Secret, stateKey, account.ID)))
		}
		state = combineCodexTurnRetryStates(stateKey, states)
	}
	return orderOpenAIAccountsByCodexTurnAvoidanceWithState(accounts, strategy, state, modelPriority)
}

// OrderOpenAIAccountsByCodexTurnAvoidanceAsync mirrors
// orderOpenAIAccountsByCodexTurnAvoidanceAsync.
func (s *TurnRetryService) OrderOpenAIAccountsByCodexTurnAvoidanceAsync(ctx context.Context, accounts []gatewayruntimecache.OpenAIAccountSecret, strategy OpenAIGatewayClientStrategyContext, modelPriority *gatewayrouting.GatewayAccountModelPriority) (CodexTurnAccountAvoidanceResult, error) {
	if s.Store == nil {
		return s.OrderOpenAIAccountsByCodexTurnAvoidance(accounts, strategy, modelPriority), nil
	}
	stateKey := strategy.ClientSourceAvoidanceStateKey
	var state *codexTurnRetryState
	if stateKey != "" {
		states := make([]*codexTurnRetryState, 0, len(accounts))
		for _, account := range accounts {
			// Wait for the per-key mutation tail, then read.
			unlock := s.lockMutation(codexTurnAccountStateKey(s.Secret, stateKey, account.ID))
			raw, err := s.Store.GetJSON(ctx, s.redisCodexTurnRetryStateKey(stateKey, account.ID))
			unlock()
			if err != nil {
				return CodexTurnAccountAvoidanceResult{}, err
			}
			states = append(states, decodeRetryState(raw))
		}
		state = combineCodexTurnRetryStates(stateKey, states)
	}
	return orderOpenAIAccountsByCodexTurnAvoidanceWithState(accounts, strategy, state, modelPriority), nil
}

func orderOpenAIAccountsByCodexTurnAvoidanceWithState(accounts []gatewayruntimecache.OpenAIAccountSecret, strategy OpenAIGatewayClientStrategyContext, state *codexTurnRetryState, modelPriority *gatewayrouting.GatewayAccountModelPriority) CodexTurnAccountAvoidanceResult {
	if !strategy.AllowClientSourceAccountAvoidance || state == nil {
		failureCount := 0
		if state != nil {
			failureCount = state.FailureCount
		}
		return CodexTurnAccountAvoidanceResult{
			Accounts:           accounts,
			Applied:            false,
			ThresholdReached:   false,
			FailureCount:       failureCount,
			AvoidedAccountIDs:  []string{},
			BypassedAllAvoided: false,
		}
	}

	activatedSet := map[string]struct{}{}
	var activatedOrder []string
	nowMs := time.Now().UnixMilli()
	for _, accountState := range state.FailedAccounts {
		if codexTurnAccountAvoidanceActivated(accountState, nowMs) {
			if _, seen := activatedSet[accountState.AccountID]; !seen {
				activatedSet[accountState.AccountID] = struct{}{}
				activatedOrder = append(activatedOrder, accountState.AccountID)
			}
		}
	}
	if len(activatedSet) == 0 {
		return CodexTurnAccountAvoidanceResult{
			Accounts:           accounts,
			Applied:            false,
			ThresholdReached:   false,
			FailureCount:       state.FailureCount,
			AvoidedAccountIDs:  []string{},
			BypassedAllAvoided: false,
		}
	}
	var freshAccounts, failedAccounts []gatewayruntimecache.OpenAIAccountSecret
	for _, account := range accounts {
		if _, activated := activatedSet[account.ID]; activated {
			failedAccounts = append(failedAccounts, account)
		} else {
			freshAccounts = append(freshAccounts, account)
		}
	}
	if len(freshAccounts) == 0 || len(failedAccounts) == 0 {
		var avoided []string
		for _, account := range accounts {
			if _, activated := activatedSet[account.ID]; activated {
				avoided = append(avoided, account.ID)
			}
		}
		return CodexTurnAccountAvoidanceResult{
			Accounts:           accounts,
			Applied:            false,
			ThresholdReached:   true,
			FailureCount:       state.FailureCount,
			AvoidedAccountIDs:  avoided,
			BypassedAllAvoided: len(freshAccounts) == 0 && len(accounts) > 0,
		}
	}

	reordered := make([]gatewayruntimecache.OpenAIAccountSecret, 0, len(accounts))
	reordered = append(reordered, freshAccounts...)
	reordered = append(reordered, failedAccounts...)
	reorderedAccounts := preserveCodexDispatchPriorityTiers(accounts, reordered, modelPriority)
	applied := false
	for index, account := range accounts {
		if account.ID != reorderedAccounts[index].ID {
			applied = true
			break
		}
	}
	return CodexTurnAccountAvoidanceResult{
		Accounts:           reorderedAccounts,
		Applied:            applied,
		ThresholdReached:   true,
		FailureCount:       state.FailureCount,
		AvoidedAccountIDs:  accountIDs(failedAccounts),
		BypassedAllAvoided: false,
	}
}

// preserveCodexDispatchPriorityTiers mirrors
// preserveGatewayAccountDispatchPriorityTiers over the runtime-cache
// accounts through the gatewaycircuit tier helper.
func preserveCodexDispatchPriorityTiers(baseAccounts, reorderedAccounts []gatewayruntimecache.OpenAIAccountSecret, modelPriority *gatewayrouting.GatewayAccountModelPriority) []gatewayruntimecache.OpenAIAccountSecret {
	var modelRankByAccountID map[string]int64
	if modelPriority != nil && modelPriority.RankByAccountID != nil {
		modelRankByAccountID = make(map[string]int64, len(modelPriority.RankByAccountID))
		for id, rank := range modelPriority.RankByAccountID {
			modelRankByAccountID[id] = int64(rank)
		}
	}
	project := func(accounts []gatewayruntimecache.OpenAIAccountSecret) []gatewaycircuit.SuppressibleAccount {
		output := make([]gatewaycircuit.SuppressibleAccount, 0, len(accounts))
		for _, account := range accounts {
			output = append(output, gatewaycircuit.SuppressibleAccount{
				SuppressibleGatewayAccount: gatewaycircuit.SuppressibleGatewayAccount{
					ID:                        account.ID,
					AccessType:                account.AccountAccessType,
					AccountAccessType:         account.AccountAccessType,
					BindingSystemAccountID:    derefString(account.BindingSystemAccountID),
					BoundGroupID:              derefString(account.BoundGroupID),
					AccountAuthorizationID:    derefString(account.AccountAuthorizationID),
					CredentialSourceAccountID: derefString(account.CredentialSourceAccountID),
				},
				FallbackEnabled:      account.FallbackEnabled,
				SuperPriorityEnabled: account.SuperPriorityEnabled,
				Priority:             int64(account.Priority),
			})
		}
		return output
	}
	base := project(baseAccounts)
	reordered := project(reorderedAccounts)
	preserved := gatewaycircuit.PreserveDispatchPriorityTiers(base, reordered, modelRankByAccountID)
	output := make([]gatewayruntimecache.OpenAIAccountSecret, 0, len(preserved))
	byID := make(map[string]gatewayruntimecache.OpenAIAccountSecret, len(baseAccounts))
	for _, account := range baseAccounts {
		byID[account.ID] = account
	}
	for _, item := range preserved {
		if account, ok := byID[item.ID]; ok {
			output = append(output, account)
		}
	}
	return output
}

func accountIDs(accounts []gatewayruntimecache.OpenAIAccountSecret) []string {
	output := make([]string, 0, len(accounts))
	for _, account := range accounts {
		output = append(output, account.ID)
	}
	return output
}

// ---------------------------------------------------------------------------
// remember
// ---------------------------------------------------------------------------

// RememberCodexTurnStreamFailure mirrors rememberCodexTurnStreamFailure
// (memory driver).
func (s *TurnRetryService) RememberCodexTurnStreamFailure(strategy OpenAIGatewayClientStrategyContext, accountID string, input CodexTurnFailureInput) *CodexTurnFailureRecordResult {
	stateKey := strategy.ClientSourceAvoidanceStateKey
	if !strategy.AllowClientSourceAccountAvoidance || stateKey == "" {
		return nil
	}
	now := s.nowMs()
	accountStateKey := codexTurnAccountStateKey(s.Secret, stateKey, accountID)
	current := s.getMemoryCodexTurnRetryState(accountStateKey)
	if current == nil {
		current = &codexTurnRetryState{
			StateKey:       stateKey,
			FailureCount:   0,
			FailedAccounts: map[string]*codexTurnFailedAccount{},
			CreatedAtMs:    now,
			UpdatedAtMs:    now,
		}
	}
	mutation := mutateCodexTurnRetryState(current, accountID, input, now, s.createID)
	if mutation.duplicateObservation {
		result := codexTurnFailureRecordResult(*current, true, nil)
		return &result
	}
	s.applyCodexTurnAvoidanceGeneration(mutation, accountStateKey, nil)
	s.setMemoryCodexTurnRetryState(accountStateKey, mutation.state)
	result := codexTurnFailureRecordResult(mutation.state, mutation.duplicateObservation, mutation.activation)
	return &result
}

// RememberCodexTurnStreamFailureAsync mirrors
// rememberCodexTurnStreamFailureAsync.
func (s *TurnRetryService) RememberCodexTurnStreamFailureAsync(ctx context.Context, strategy OpenAIGatewayClientStrategyContext, accountID string, input CodexTurnFailureInput) (*CodexTurnFailureRecordResult, error) {
	if s.Store == nil {
		return s.RememberCodexTurnStreamFailure(strategy, accountID, input), nil
	}
	stateKey := strategy.ClientSourceAvoidanceStateKey
	if !strategy.AllowClientSourceAccountAvoidance || stateKey == "" {
		return nil, nil
	}
	accountStateKey := codexTurnAccountStateKey(s.Secret, stateKey, accountID)
	unlock := s.lockMutation(accountStateKey)
	defer unlock()
	for attempt := 0; attempt < codexTurnRedisMutationMaxAttempts; attempt++ {
		raw, err := s.Store.GetJSON(ctx, s.redisCodexTurnRetryStateKey(stateKey, accountID))
		if err != nil {
			return nil, err
		}
		current := decodeRetryState(raw)
		now := s.nowMs()
		base := current
		if base == nil {
			base = &codexTurnRetryState{
				StateKey:       stateKey,
				FailureCount:   0,
				FailedAccounts: map[string]*codexTurnFailedAccount{},
				CreatedAtMs:    now,
				UpdatedAtMs:    now,
			}
		}
		mutation := mutateCodexTurnRetryState(base, accountID, input, now, s.createID)
		if mutation.duplicateObservation {
			result := codexTurnFailureRecordResult(*base, true, nil)
			return &result, nil
		}
		var generation *int64
		if mutation.activation != nil {
			incremented, incrErr := s.Store.Incr(ctx, s.redisCodexTurnAvoidanceGenerationKey(stateKey, accountID), codexTurnRetryTtlMs)
			if incrErr != nil {
				return nil, incrErr
			}
			generation = &incremented
		}
		s.applyCodexTurnAvoidanceGeneration(mutation, accountStateKey, generation)
		applied, casErr := s.Store.CompareSetJSON(ctx, s.redisCodexTurnRetryStateKey(stateKey, accountID), raw, mutation.state, codexTurnRetryTtlMs)
		if casErr != nil {
			return nil, casErr
		}
		if applied {
			result := codexTurnFailureRecordResult(mutation.state, mutation.duplicateObservation, mutation.activation)
			return &result, nil
		}
		codexTurnRedisMutationBackoff()
	}
	s.warn("gateway_codex_turn_retry_state_cas_exhausted", map[string]any{
		"stateKey":  stateKey,
		"accountId": accountID,
		"evidence":  orElseEvidence(input.Evidence),
	}, "Codex turn 失败状态并发合并耗尽，按 fail-open 继续")
	return nil, nil
}

func orElseEvidence(evidence string) string {
	if evidence == "" {
		return EvidenceRetryableFailure
	}
	return evidence
}

// ---------------------------------------------------------------------------
// clear
// ---------------------------------------------------------------------------

// ClearCodexTurnAccountAvoidance mirrors clearCodexTurnAccountAvoidance.
//
// A probe success may clear only the exact source/turn/account avoidance it
// verified. It intentionally does not touch account availability or circuit
// state, and cannot clear a different source because the state key is
// scoped.
func (s *TurnRetryService) ClearCodexTurnAccountAvoidance(strategy OpenAIGatewayClientStrategyContext, accountID string) bool {
	stateKey := strategy.ClientSourceAvoidanceStateKey
	normalizedAccountID := strings.TrimSpace(accountID)
	if !strategy.AllowClientSourceAccountAvoidance || stateKey == "" || normalizedAccountID == "" {
		return false
	}
	accountStateKey := codexTurnAccountStateKey(s.Secret, stateKey, normalizedAccountID)
	s.memory.mu.Lock()
	defer s.memory.mu.Unlock()
	s.memory.init()
	entry, ok := s.memory.entries[accountStateKey]
	if !ok || entry.expiresAt <= s.nowMs() {
		return false
	}
	if _, has := entry.value.FailedAccounts[normalizedAccountID]; !has {
		return false
	}
	failedAccounts := make(map[string]*codexTurnFailedAccount, len(entry.value.FailedAccounts))
	for key, value := range entry.value.FailedAccounts {
		if key == normalizedAccountID {
			continue
		}
		failedAccounts[key] = value
	}
	if len(failedAccounts) == 0 {
		delete(s.memory.entries, accountStateKey)
		return true
	}
	next := entry.value
	next.FailedAccounts = failedAccounts
	next.UpdatedAtMs = s.nowMs()
	s.memory.entries[accountStateKey] = &memoryCodexTurnRetryEntry{value: next, expiresAt: s.nowMs() + codexTurnRetryTtlMs}
	return true
}

// ClearCodexTurnAccountAvoidanceAsync mirrors
// clearCodexTurnAccountAvoidanceAsync.
func (s *TurnRetryService) ClearCodexTurnAccountAvoidanceAsync(ctx context.Context, strategy OpenAIGatewayClientStrategyContext, accountID string) (bool, error) {
	if s.Store == nil {
		return s.ClearCodexTurnAccountAvoidance(strategy, accountID), nil
	}
	stateKey := strategy.ClientSourceAvoidanceStateKey
	normalizedAccountID := strings.TrimSpace(accountID)
	if !strategy.AllowClientSourceAccountAvoidance || stateKey == "" || normalizedAccountID == "" {
		return false, nil
	}
	accountStateKey := codexTurnAccountStateKey(s.Secret, stateKey, normalizedAccountID)
	unlock := s.lockMutation(accountStateKey)
	defer unlock()
	for attempt := 0; attempt < codexTurnRedisMutationMaxAttempts; attempt++ {
		raw, err := s.Store.GetJSON(ctx, s.redisCodexTurnRetryStateKey(stateKey, normalizedAccountID))
		if err != nil {
			return false, err
		}
		current := decodeRetryState(raw)
		if current == nil {
			return false, nil
		}
		if _, has := current.FailedAccounts[normalizedAccountID]; !has {
			return false, nil
		}
		failedAccounts := make(map[string]*codexTurnFailedAccount, len(current.FailedAccounts))
		for key, value := range current.FailedAccounts {
			if key == normalizedAccountID {
				continue
			}
			failedAccounts[key] = value
		}
		next := *current
		next.FailedAccounts = failedAccounts
		next.UpdatedAtMs = s.nowMs()
		applied, casErr := s.Store.CompareSetJSON(ctx, s.redisCodexTurnRetryStateKey(stateKey, normalizedAccountID), raw, &next, codexTurnRetryTtlMs)
		if casErr != nil {
			return false, casErr
		}
		if applied {
			return true, nil
		}
		codexTurnRedisMutationBackoff()
	}
	s.warn("gateway_codex_turn_retry_state_clear_cas_exhausted", map[string]any{
		"stateKey":  stateKey,
		"accountId": normalizedAccountID,
	}, "Codex turn 精确避让清理并发合并耗尽，保留短期避让")
	return false, nil
}

// ClearCodexTurnAccountAvoidanceByFenceInput mirrors
// clearCodexTurnAccountAvoidanceByFenceAsync's input.
type ClearCodexTurnAccountAvoidanceByFenceInput struct {
	StateKey         string
	AccountID        string
	SourceGeneration int64
	SourceFenceID    string
}

// ClearCodexTurnAccountAvoidanceByFenceAsync mirrors
// clearCodexTurnAccountAvoidanceByFenceAsync.
func (s *TurnRetryService) ClearCodexTurnAccountAvoidanceByFenceAsync(ctx context.Context, input ClearCodexTurnAccountAvoidanceByFenceInput) (bool, error) {
	normalizedAccountID := strings.TrimSpace(input.AccountID)
	if input.StateKey == "" || normalizedAccountID == "" || !isSourceFenceID(input.SourceFenceID) {
		return false, nil
	}
	accountStateKey := codexTurnAccountStateKey(s.Secret, input.StateKey, normalizedAccountID)
	if s.Store == nil {
		current := s.getMemoryCodexTurnRetryState(accountStateKey)
		if current == nil {
			return false, nil
		}
		failed, has := current.FailedAccounts[normalizedAccountID]
		if !has || failed.AvoidanceGeneration == nil || *failed.AvoidanceGeneration != input.SourceGeneration {
			return false, nil
		}
		if failed.AvoidanceFenceID == nil || *failed.AvoidanceFenceID != input.SourceFenceID {
			return false, nil
		}
		return s.ClearCodexTurnAccountAvoidance(OpenAIGatewayClientStrategyContext{
			AllowClientSourceAccountAvoidance: true,
			ClientSourceAvoidanceStateKey:     input.StateKey,
		}, normalizedAccountID), nil
	}
	unlock := s.lockMutation(accountStateKey)
	defer unlock()
	for attempt := 0; attempt < codexTurnRedisMutationMaxAttempts; attempt++ {
		raw, err := s.Store.GetJSON(ctx, s.redisCodexTurnRetryStateKey(input.StateKey, normalizedAccountID))
		if err != nil {
			return false, err
		}
		current := decodeRetryState(raw)
		if current == nil {
			return false, nil
		}
		failed, has := current.FailedAccounts[normalizedAccountID]
		if !has || failed.AvoidanceGeneration == nil || *failed.AvoidanceGeneration != input.SourceGeneration {
			return false, nil
		}
		if failed.AvoidanceFenceID == nil || *failed.AvoidanceFenceID != input.SourceFenceID {
			return false, nil
		}
		failedAccounts := make(map[string]*codexTurnFailedAccount, len(current.FailedAccounts))
		for key, value := range current.FailedAccounts {
			if key == normalizedAccountID {
				continue
			}
			failedAccounts[key] = value
		}
		next := *current
		next.FailedAccounts = failedAccounts
		next.UpdatedAtMs = s.nowMs()
		applied, casErr := s.Store.CompareSetJSON(ctx, s.redisCodexTurnRetryStateKey(input.StateKey, normalizedAccountID), raw, &next, codexTurnRetryTtlMs)
		if casErr != nil {
			return false, casErr
		}
		if applied {
			return true, nil
		}
		codexTurnRedisMutationBackoff()
	}
	return false, nil
}

// ---------------------------------------------------------------------------
// memory plumbing
// ---------------------------------------------------------------------------

func (s *TurnRetryService) getMemoryCodexTurnRetryState(stateKey string) *codexTurnRetryState {
	s.memory.mu.Lock()
	defer s.memory.mu.Unlock()
	s.memory.init()
	entry, ok := s.memory.entries[stateKey]
	if !ok {
		return nil
	}
	if entry.expiresAt <= s.nowMs() {
		delete(s.memory.entries, stateKey)
		return nil
	}
	value := entry.value
	return &value
}

func (s *TurnRetryService) setMemoryCodexTurnRetryState(stateKey string, state codexTurnRetryState) {
	s.memory.mu.Lock()
	defer s.memory.mu.Unlock()
	s.memory.init()
	// Map iteration order is random in Go; delete-then-set keeps the Node
	// LRU-ish insertion order behavior (oldest eviction) approximated by
	// lowest createdAtMs eviction.
	s.memory.entries[stateKey] = &memoryCodexTurnRetryEntry{value: state, expiresAt: s.nowMs() + codexTurnRetryTtlMs}
	for len(s.memory.entries) > codexTurnRetryMaxEntries {
		oldestKey := ""
		var oldestCreatedAt int64 = -1
		for key, entry := range s.memory.entries {
			if oldestCreatedAt < 0 || entry.value.CreatedAtMs < oldestCreatedAt {
				oldestCreatedAt = entry.value.CreatedAtMs
				oldestKey = key
			}
		}
		if oldestKey == "" {
			break
		}
		delete(s.memory.entries, oldestKey)
	}
}

func (s *TurnRetryService) lockMutation(stateKey string) func() {
	s.memory.mu.Lock()
	s.memory.init()
	lock, ok := s.memory.mutationLocks[stateKey]
	if !ok {
		lock = &sync.Mutex{}
		s.memory.mutationLocks[stateKey] = lock
	}
	s.memory.mu.Unlock()
	lock.Lock()
	return lock.Unlock
}

func (s *TurnRetryService) applyCodexTurnAvoidanceGeneration(mutation codexTurnStateMutation, accountStateKey string, explicitGeneration *int64) {
	if mutation.activation == nil {
		return
	}
	nowMs := s.nowMs()
	s.memory.mu.Lock()
	s.memory.init()
	for key, entry := range s.memory.generations {
		if entry.expiresAtMs <= nowMs {
			delete(s.memory.generations, key)
		}
	}
	var previousGeneration int64
	if current, ok := s.memory.generations[accountStateKey]; ok && current.expiresAtMs > nowMs {
		previousGeneration = current.generation
	}
	generation := previousGeneration + 1
	if explicitGeneration != nil {
		generation = *explicitGeneration
	}
	s.memory.generations[accountStateKey] = &avoidanceGenerationTombstone{
		generation:  maxInt64(previousGeneration, generation),
		expiresAtMs: nowMs + codexTurnRetryTtlMs,
	}
	for len(s.memory.generations) > codexTurnRetryMaxEntries {
		oldestKey := ""
		var oldestExpiry int64 = -1
		for key, entry := range s.memory.generations {
			if oldestExpiry < 0 || entry.expiresAtMs < oldestExpiry {
				oldestExpiry = entry.expiresAtMs
				oldestKey = key
			}
		}
		if oldestKey == "" {
			break
		}
		delete(s.memory.generations, oldestKey)
	}
	s.memory.mu.Unlock()

	if failed, ok := mutation.state.FailedAccounts[mutation.activation.AccountID]; ok {
		generationCopy := generation
		failed.AvoidanceGeneration = &generationCopy
		fenceID := mutation.activation.SourceFenceID
		failed.AvoidanceFenceID = &fenceID
	}
	mutation.activation.SourceGeneration = generation
}

func maxInt64(left, right int64) int64 {
	if left > right {
		return left
	}
	return right
}

func codexTurnAccountStateKey(secret, stateKey, accountID string) string {
	normalizedAccountID := strings.TrimSpace(accountID)
	if normalizedAccountID == "" {
		panic(errors.New("Codex turn retry state requires an account id"))
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("juhe-ai:codex-turn-account-state:v1\n"))
	mac.Write([]byte(stateKey))
	mac.Write([]byte("\n"))
	mac.Write([]byte(normalizedAccountID))
	return stateKey + ":a_" + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (s *TurnRetryService) redisCodexTurnRetryStateKey(stateKey, accountID string) string {
	return "state:" + codexTurnAccountStateKey(s.Secret, stateKey, accountID)
}

func (s *TurnRetryService) redisCodexTurnAvoidanceGenerationKey(stateKey, accountID string) string {
	return "generation:" + codexTurnAccountStateKey(s.Secret, stateKey, accountID)
}

func combineCodexTurnRetryStates(stateKey string, states []*codexTurnRetryState) *codexTurnRetryState {
	var present []*codexTurnRetryState
	for _, state := range states {
		if state != nil {
			present = append(present, state)
		}
	}
	if len(present) == 0 {
		return nil
	}
	combined := codexTurnRetryState{
		StateKey:       stateKey,
		FailedAccounts: map[string]*codexTurnFailedAccount{},
	}
	first := true
	for _, state := range present {
		combined.FailureCount += state.FailureCount
		for id, failed := range state.FailedAccounts {
			combined.FailedAccounts[id] = failed
		}
		if first || state.CreatedAtMs < combined.CreatedAtMs {
			combined.CreatedAtMs = state.CreatedAtMs
		}
		if first || state.UpdatedAtMs > combined.UpdatedAtMs {
			combined.UpdatedAtMs = state.UpdatedAtMs
		}
		first = false
	}
	return &combined
}

type codexTurnStateMutation struct {
	state                codexTurnRetryState
	duplicateObservation bool
	activation           *CodexTurnFailureActivation
}

func mutateCodexTurnRetryState(current *codexTurnRetryState, accountID string, input CodexTurnFailureInput, now int64, createID func() string) codexTurnStateMutation {
	var previousAccountState *codexTurnFailedAccount
	if current.FailedAccounts != nil {
		previousAccountState = current.FailedAccounts[accountID]
	}
	accountState := codexTurnFailedAccount{}
	if previousAccountState != nil {
		accountState = *previousAccountState
		accountState.RecentObservationIDs = append([]string(nil), previousAccountState.RecentObservationIDs...)
	} else {
		accountState = codexTurnFailedAccount{
			AccountID:      accountID,
			FailureCount:   0,
			LastFailedAtMs: now,
		}
		accountState.RecentObservationIDs = []string{}
	}
	observationID := strings.TrimSpace(input.ObservationID)
	if observationID != "" && containsString(accountState.RecentObservationIDs, observationID) {
		return codexTurnStateMutation{state: *current, duplicateObservation: true}
	}

	wasActivated := codexTurnAccountAvoidanceActivated(&accountState, now)

	evidence := input.Evidence
	if evidence == "" {
		evidence = EvidenceRetryableFailure
	}
	accountState.FailureCount++
	switch evidence {
	case EvidenceCommittedRetrySignal:
		count := 0
		if accountState.CommittedRetrySignalCount != nil {
			count = *accountState.CommittedRetrySignalCount
		}
		count++
		accountState.CommittedRetrySignalCount = &count
	case EvidenceIncompleteDownstreamAbort:
		windowExpired := accountState.LastIncompleteDownstreamAbortAtMs == nil ||
			now-*accountState.LastIncompleteDownstreamAbortAtMs > codexTurnIncompleteAbortWindowMs
		if windowExpired {
			count := 1
			accountState.IncompleteDownstreamAbortCount = &count
			start := now
			accountState.IncompleteDownstreamAbortWindowStartedAtMs = &start
		} else {
			count := 0
			if accountState.IncompleteDownstreamAbortCount != nil {
				count = *accountState.IncompleteDownstreamAbortCount
			}
			count++
			accountState.IncompleteDownstreamAbortCount = &count
			if accountState.IncompleteDownstreamAbortWindowStartedAtMs == nil {
				start := now
				accountState.IncompleteDownstreamAbortWindowStartedAtMs = &start
			}
		}
		lastAbort := now
		accountState.LastIncompleteDownstreamAbortAtMs = &lastAbort
	default:
		count := 0
		if accountState.RetryableFailureCount != nil {
			count = *accountState.RetryableFailureCount
		}
		count++
		accountState.RetryableFailureCount = &count
	}
	accountState.LastErrorCode = input.ErrorCode
	accountState.LastErrorMessage = input.Message
	accountState.LastFailedAtMs = now
	if observationID != "" {
		recent := append(accountState.RecentObservationIDs, observationID)
		if len(recent) > codexTurnRecentObservationLimit {
			recent = recent[len(recent)-codexTurnRecentObservationLimit:]
		}
		accountState.RecentObservationIDs = recent
	}
	state := *current
	state.FailedAccounts = make(map[string]*codexTurnFailedAccount, len(current.FailedAccounts)+1)
	for id, failed := range current.FailedAccounts {
		state.FailedAccounts[id] = failed
	}
	state.FailedAccounts[accountID] = &accountState
	state.FailureCount = current.FailureCount + 1
	state.UpdatedAtMs = now
	if !wasActivated && codexTurnAccountAvoidanceActivated(&accountState, now) {
		generation := int64(accountState.FailureCount)
		accountState.AvoidanceGeneration = &generation
	}
	mutation := codexTurnStateMutation{state: state, duplicateObservation: false}
	if !wasActivated && codexTurnAccountAvoidanceActivated(&accountState, now) {
		mutation.activation = &CodexTurnFailureActivation{
			AccountID:        accountID,
			SourceGeneration: int64(accountState.FailureCount),
			SourceFenceID:    createID(),
		}
	}
	return mutation
}

func containsString(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}

func codexTurnAccountAvoidanceActivated(accountState *codexTurnFailedAccount, nowMs int64) bool {
	return codexTurnStrongAvoidanceActivated(accountState) || codexTurnWeakAvoidanceActivated(accountState, nowMs)
}

func codexTurnStrongAvoidanceActivated(accountState *codexTurnFailedAccount) bool {
	committed := 0
	if accountState.CommittedRetrySignalCount != nil {
		committed = *accountState.CommittedRetrySignalCount
	}
	retryable := 0
	if accountState.RetryableFailureCount != nil {
		retryable = *accountState.RetryableFailureCount
	}
	return committed > 0 || retryable >= codexTurnAccountAvoidanceFailureThreshold
}

func codexTurnWeakAvoidanceActivated(accountState *codexTurnFailedAccount, nowMs int64) bool {
	if accountState.LastIncompleteDownstreamAbortAtMs == nil {
		return false
	}
	if nowMs-*accountState.LastIncompleteDownstreamAbortAtMs > codexTurnIncompleteAbortWindowMs {
		return false
	}
	aborts := 0
	if accountState.IncompleteDownstreamAbortCount != nil {
		aborts = *accountState.IncompleteDownstreamAbortCount
	}
	return aborts >= codexTurnAccountAvoidanceFailureThreshold
}

func codexTurnFailureRecordResult(state codexTurnRetryState, duplicateObservation bool, activation *CodexTurnFailureActivation) CodexTurnFailureRecordResult {
	failedAccountIDs := make([]string, 0, len(state.FailedAccounts))
	avoidanceActivatedAccountIDs := make([]string, 0, len(state.FailedAccounts))
	for _, failed := range state.FailedAccounts {
		failedAccountIDs = append(failedAccountIDs, failed.AccountID)
		if codexTurnAccountAvoidanceActivated(failed, time.Now().UnixMilli()) {
			avoidanceActivatedAccountIDs = append(avoidanceActivatedAccountIDs, failed.AccountID)
		}
	}
	result := CodexTurnFailureRecordResult{
		StateKey:                     state.StateKey,
		FailureCount:                 state.FailureCount,
		FailedAccountIDs:             failedAccountIDs,
		AvoidanceActivatedAccountIDs: avoidanceActivatedAccountIDs,
		DuplicateObservation:         duplicateObservation,
	}
	if activation != nil {
		result.Activation = activation
	}
	return result
}

func decodeRetryState(raw json.RawMessage) *codexTurnRetryState {
	if len(raw) == 0 {
		return nil
	}
	var state codexTurnRetryState
	if err := json.Unmarshal(raw, &state); err != nil {
		return nil
	}
	if state.FailedAccounts == nil {
		state.FailedAccounts = map[string]*codexTurnFailedAccount{}
	}
	return &state
}

func codexTurnRedisMutationBackoff() {
	// Node awaits Math.floor(Math.random() * 9) ms; tests drive their own
	// coordination through the CAS loop instead of the timer.
	time.Sleep(time.Duration(rand.Intn(9)) * time.Millisecond)
}

func isSourceFenceID(value string) bool {
	return uuidPattern.MatchString(value)
}

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func (s *TurnRetryService) warn(event string, fields map[string]any, message string) {
	if s.Logger == nil {
		return
	}
	merged := map[string]any{"event": event}
	for key, value := range fields {
		merged[key] = value
	}
	s.Logger.Warn(event, merged, message)
}
