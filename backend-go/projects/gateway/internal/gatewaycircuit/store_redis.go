package gatewaycircuit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	redis "github.com/redis/go-redis/v9"
)

// RedisStoreOptions mirrors RedisAccountCircuitStoreOptions. Either RedisURL
// (a client is created like Node getRedisClient) or Client (injected, tests)
// must be set.
type RedisStoreOptions struct {
	RedisURL            string
	Client              redis.Cmdable
	Namespace           string
	Name                string
	Capacity            int64
	ClosedRetentionMs   int64
	ReplayLimitPerScope int64
	Now                 func() int64
}

type redisCircuitKeys struct {
	states            string
	due               string
	closed            string
	escalation        string
	capacitySaturated string
}

// RedisStore mirrors RedisAccountCircuitStore. Every transition, including
// lease-expiry normalization and index maintenance, runs in one Lua call; the
// scripts are the Node scripts verbatim so the semantics cannot drift.
type RedisStore struct {
	client              redis.Cmdable
	keys                redisCircuitKeys
	capacity            int64
	closedRetentionMs   int64
	replayLimitPerScope int64
	now                 func() int64
}

// NewRedisStore mirrors new RedisAccountCircuitStore.
func NewRedisStore(options RedisStoreOptions) (*RedisStore, error) {
	client := options.Client
	if client == nil {
		if strings.TrimSpace(options.RedisURL) == "" {
			return nil, errors.New("账户电路操作缺少 redisUrl")
		}
		parsed, err := redis.ParseURL(options.RedisURL)
		if err != nil {
			return nil, err
		}
		client = redis.NewClient(parsed)
	}
	capacity, err := positiveInteger(options.Capacity, "capacity")
	if err != nil {
		return nil, err
	}
	closedRetentionMs := DefaultClosedRetentionMs
	if options.ClosedRetentionMs != 0 {
		closedRetentionMs = options.ClosedRetentionMs
	}
	closedRetentionMs, err = positiveInteger(closedRetentionMs, "closedRetentionMs")
	if err != nil {
		return nil, err
	}
	replayLimit := DefaultReplayLimitPerScope
	if options.ReplayLimitPerScope != 0 {
		replayLimit = options.ReplayLimitPerScope
	}
	replayLimit, err = positiveInteger(replayLimit, "replayLimitPerScope")
	if err != nil {
		return nil, err
	}
	now := options.Now
	if now == nil {
		now = defaultNowMs
	}
	name := options.Name
	if strings.TrimSpace(name) == "" {
		name = "gateway-account-circuit"
	}
	return &RedisStore{
		client:              client,
		keys:                redisAccountCircuitStoreKeys(name, options.Namespace),
		capacity:            capacity,
		closedRetentionMs:   closedRetentionMs,
		replayLimitPerScope: replayLimit,
		now:                 now,
	}, nil
}

// Get mirrors store.get.
func (s *RedisStore) Get(ctx context.Context, scope Scope, nowMs *int64) (State, error) {
	result, err := s.execute(ctx, "get", scope, map[string]any{
		"nowMs": nowMsValue(nowMs, s.now),
	}, nil)
	if err != nil {
		return State{}, err
	}
	return result.State, nil
}

// Suspect mirrors store.suspect.
func (s *RedisStore) Suspect(ctx context.Context, input SuspectInput) (MutationResult, error) {
	confirmationFailuresRequired, err := NormalizeConfirmationFailuresRequired(input.ConfirmationFailuresRequired, DefaultConfirmationFailuresRequired)
	if err != nil {
		return MutationResult{}, err
	}
	failureEvidenceKey, err := NormalizeFailureEvidenceKey(input.FailureEvidenceKey, "suspect:"+input.TransitionID)
	if err != nil {
		return MutationResult{}, err
	}
	payload := map[string]any{
		"scope":                        input.Scope,
		"dispatchRevision":             input.DispatchRevision,
		"transitionId":                 input.TransitionID,
		"reason":                       input.Reason,
		"confirmationFailuresRequired": confirmationFailuresRequired,
		"failureEvidenceKey":           failureEvidenceKey,
		"nowMs":                        nowMsValue(input.NowMs, s.now),
	}
	return s.executeTransition(ctx, "suspect", input.Scope, payload)
}

// AcquireConfirmationLease mirrors store.acquireConfirmationLease.
func (s *RedisStore) AcquireConfirmationLease(ctx context.Context, input AcquireConfirmationLeaseInput) (MutationResult, error) {
	payload := map[string]any{
		"scope":            input.Scope,
		"generation":       input.Generation,
		"dispatchRevision": input.DispatchRevision,
		"transitionId":     input.TransitionID,
		"leaseId":          input.LeaseID,
		"leaseUntilMs":     input.LeaseUntilMs,
		"nowMs":            nowMsValue(input.NowMs, s.now),
	}
	if input.ExpectedFailureEvidenceKey != nil {
		normalized, err := NormalizeFailureEvidenceKey(input.ExpectedFailureEvidenceKey, "confirmation-acquire:"+input.TransitionID)
		if err != nil {
			return MutationResult{}, err
		}
		payload["expectedFailureEvidenceKey"] = normalized
	}
	if input.ConfirmationEvidenceKey != nil {
		normalized, err := NormalizeFailureEvidenceKey(input.ConfirmationEvidenceKey, "confirmation-evidence:"+input.TransitionID)
		if err != nil {
			return MutationResult{}, err
		}
		payload["confirmationEvidenceKey"] = normalized
	}
	return s.executeTransition(ctx, "acquire_confirmation", input.Scope, payload)
}

// CloseSuspectFromObserver mirrors store.closeSuspectFromObserver.
func (s *RedisStore) CloseSuspectFromObserver(ctx context.Context, input CloseSuspectFromObserverInput) (MutationResult, error) {
	expectedFailureEvidenceKey, err := NormalizeFailureEvidenceKey(strPtr(input.ExpectedFailureEvidenceKey), "observer-close-expected:"+input.TransitionID)
	if err != nil {
		return MutationResult{}, err
	}
	observerEvidenceKey, err := NormalizeFailureEvidenceKey(strPtr(input.ObserverEvidenceKey), "observer-close:"+input.TransitionID)
	if err != nil {
		return MutationResult{}, err
	}
	payload := map[string]any{
		"scope":                      input.Scope,
		"generation":                 input.Generation,
		"dispatchRevision":           input.DispatchRevision,
		"transitionId":               input.TransitionID,
		"expectedFailureEvidenceKey": expectedFailureEvidenceKey,
		"observerEvidenceKey":        observerEvidenceKey,
		"nowMs":                      nowMsValue(input.NowMs, s.now),
	}
	return s.executeTransition(ctx, "close_suspect_observer", input.Scope, payload)
}

// CloseSuspectFromKeyRotation mirrors store.closeSuspectFromKeyRotation.
func (s *RedisStore) CloseSuspectFromKeyRotation(ctx context.Context, input CloseSuspectFromKeyRotationInput) (MutationResult, error) {
	expectedFailureEvidenceKey, err := NormalizeFailureEvidenceKey(strPtr(input.ExpectedFailureEvidenceKey), "key-rotation-close:"+input.TransitionID)
	if err != nil {
		return MutationResult{}, err
	}
	payload := map[string]any{
		"scope":                      input.Scope,
		"generation":                 input.Generation,
		"dispatchRevision":           input.DispatchRevision,
		"transitionId":               input.TransitionID,
		"expectedFailureEvidenceKey": expectedFailureEvidenceKey,
		"nowMs":                      nowMsValue(input.NowMs, s.now),
	}
	return s.executeTransition(ctx, "close_suspect_key_rotation", input.Scope, payload)
}

// CompleteConfirmation mirrors store.completeConfirmation.
func (s *RedisStore) CompleteConfirmation(ctx context.Context, input CompleteConfirmationInput) (MutationResult, error) {
	payload := map[string]any{
		"scope":            input.Scope,
		"generation":       input.Generation,
		"dispatchRevision": input.DispatchRevision,
		"transitionId":     input.TransitionID,
		"leaseId":          input.LeaseID,
		"outcome":          input.Outcome,
		"reason":           input.Reason,
		"nowMs":            nowMsValue(input.NowMs, s.now),
	}
	if input.FramingCompleteDisposition != nil {
		payload["framingCompleteDisposition"] = *input.FramingCompleteDisposition
	}
	if input.Outcome == OutcomeTransportFailure {
		failureEvidenceKey, err := NormalizeFailureEvidenceKey(input.FailureEvidenceKey, "confirmation:"+input.LeaseID)
		if err != nil {
			return MutationResult{}, err
		}
		payload["failureEvidenceKey"] = failureEvidenceKey
	}
	return s.executeTransition(ctx, "complete_confirmation", input.Scope, payload)
}

// AcquireCanaryLease mirrors store.acquireCanaryLease.
func (s *RedisStore) AcquireCanaryLease(ctx context.Context, input AcquireCanaryLeaseInput) (MutationResult, error) {
	payload := map[string]any{
		"scope":            input.Scope,
		"generation":       input.Generation,
		"dispatchRevision": input.DispatchRevision,
		"transitionId":     input.TransitionID,
		"leaseId":          input.LeaseID,
		"leaseUntilMs":     input.LeaseUntilMs,
		"nowMs":            nowMsValue(input.NowMs, s.now),
	}
	return s.executeTransition(ctx, "acquire_canary", input.Scope, payload)
}

// CompleteCanary mirrors store.completeCanary.
func (s *RedisStore) CompleteCanary(ctx context.Context, input CompleteCanaryInput) (MutationResult, error) {
	payload := map[string]any{
		"scope":            input.Scope,
		"generation":       input.Generation,
		"dispatchRevision": input.DispatchRevision,
		"transitionId":     input.TransitionID,
		"leaseId":          input.LeaseID,
		"outcome":          input.Outcome,
		"reason":           input.Reason,
		"nowMs":            nowMsValue(input.NowMs, s.now),
	}
	if input.EvidenceScopeKey != nil {
		payload["evidenceScopeKey"] = *input.EvidenceScopeKey
	}
	return s.executeTransition(ctx, "complete_canary", input.Scope, payload)
}

// RecordProtocolModelOpenEvidence mirrors
// store.recordProtocolModelOpenEvidence.
func (s *RedisStore) RecordProtocolModelOpenEvidence(ctx context.Context, input ProtocolModelOpenEvidenceInput) (EscalationResult, error) {
	nowMs := normalizedNowValue(input.NowMs, s.now)
	if _, err := positiveInteger(input.ConfirmedFailureCount, "confirmedFailureCount"); err != nil {
		return EscalationResult{}, err
	}
	if _, err := positiveInteger(input.WindowMs, "windowMs"); err != nil {
		return EscalationResult{}, err
	}
	maxProtocolScopes, err := positiveInteger(input.MaxProtocolScopes, "maxProtocolScopes")
	if err != nil {
		return EscalationResult{}, err
	}
	distinctScopeThreshold, err := NormalizeEscalationDistinctScopeThreshold(&input.DistinctScopeThreshold, EscalationDistinctScopeThresholdDefault)
	if err != nil {
		return EscalationResult{}, err
	}
	if distinctScopeThreshold > maxProtocolScopes {
		return EscalationResult{}, errors.New("账户电路 distinctScopeThreshold 不能超过 maxProtocolScopes")
	}
	if _, err := requiredValue(input.EvidenceID, "evidenceId"); err != nil {
		return EscalationResult{}, err
	}
	if _, err := requiredValue(input.AccountTransitionID, "accountTransitionId"); err != nil {
		return EscalationResult{}, err
	}
	if _, err := requiredValue(input.Reason, "reason"); err != nil {
		return EscalationResult{}, err
	}
	accountScope := Scope{Kind: ScopeKindAccount, AccountRuntimeKey: input.Scope.AccountRuntimeKey}
	accountScopeKey := MustScopeKey(accountScope)
	scopeKey := MustScopeKey(input.Scope)
	payload := map[string]any{
		"scope":                        input.Scope,
		"generation":                   input.Generation,
		"dispatchRevision":             input.DispatchRevision,
		"evidenceId":                   input.EvidenceID,
		"accountTransitionId":          input.AccountTransitionID,
		"reason":                       input.Reason,
		"confirmedFailureCount":        input.ConfirmedFailureCount,
		"distinctScopeThreshold":       distinctScopeThreshold,
		"windowMs":                     input.WindowMs,
		"maxProtocolScopes":            maxProtocolScopes,
		"nowMs":                        nowMs,
		"scopeKey":                     scopeKey,
		"accountScope":                 accountScope,
		"accountScopeKey":              accountScopeKey,
		"closedAccountState":           ClosedState(accountScope, input.DispatchRevision, 0, "", 0),
		"capacityAccountState":         CapacityExhaustedState(accountScope, input.DispatchRevision, nowMs),
	}
	raw, err := s.client.Eval(ctx, redisAccountCircuitEscalationScript,
		[]string{s.keys.states, s.keys.due, s.keys.closed, s.keys.escalation, s.keys.capacitySaturated},
		encodeJSON(payload), fmt.Sprintf("%d", s.capacity), fmt.Sprintf("%d", s.closedRetentionMs), fmt.Sprintf("%d", s.replayLimitPerScope)).Result()
	if err != nil {
		return EscalationResult{}, err
	}
	encoded, ok := redisStringResult(raw)
	if !ok || encoded == "" {
		return EscalationResult{}, errors.New("Redis 账户电路作用域升级返回值无效")
	}
	var parsed EscalationResult
	if err := decodeStrict(encoded, &parsed); err != nil {
		return EscalationResult{}, errors.New("Redis 账户电路作用域升级结果结构无效")
	}
	if parsed.Status == "" || parsed.AccountState.ScopeKey == "" && parsed.AccountState.Phase == "" {
		return EscalationResult{}, errors.New("Redis 账户电路作用域升级结果结构无效")
	}
	return parsed, nil
}

// ClearAccountEscalationEvidence mirrors store.clearAccountEscalationEvidence.
func (s *RedisStore) ClearAccountEscalationEvidence(ctx context.Context, input ClearAccountEscalationEvidenceInput) (bool, error) {
	if _, err := requiredValue(input.AccountRuntimeKey, "accountRuntimeKey"); err != nil {
		return false, err
	}
	if _, err := requiredValue(input.DispatchRevision, "dispatchRevision"); err != nil {
		return false, err
	}
	if _, err := requiredValue(input.EvidenceID, "evidenceId"); err != nil {
		return false, err
	}
	raw, err := s.client.Eval(ctx, redisAccountCircuitClearEscalationScript, []string{s.keys.escalation},
		input.AccountRuntimeKey, input.DispatchRevision, input.EvidenceID,
		fmt.Sprintf("%d", normalizedNowValue(input.NowMs, s.now))).Result()
	if err != nil {
		return false, err
	}
	numeric, err := numericRedisResult(raw)
	if err != nil {
		return false, err
	}
	return numeric == 1, nil
}

// ReplaceDispatchRevision mirrors store.replaceDispatchRevision.
func (s *RedisStore) ReplaceDispatchRevision(ctx context.Context, input ReplaceDispatchRevisionInput) (MutationResult, error) {
	payload := map[string]any{
		"scope":            input.Scope,
		"dispatchRevision": input.DispatchRevision,
		"transitionId":     input.TransitionID,
		"nowMs":            nowMsValue(input.NowMs, s.now),
	}
	return s.executeTransition(ctx, "replace_revision", input.Scope, payload)
}

// ListDue mirrors store.listDue.
func (s *RedisStore) ListDue(ctx context.Context, nowMs int64, limit int) ([]State, error) {
	now := normalizedNowValue(&nowMs, s.now)
	normalizedLimit, err := positiveInteger(int64(limit), "limit")
	if err != nil {
		return nil, err
	}
	var scopeKeys []string
	seen := map[string]struct{}{}
	scanChunkSize := int64(normalizedLimit) * 2
	if scanChunkSize < 64 {
		scanChunkSize = 64
	}
	if scanChunkSize > 512 {
		scanChunkSize = 512
	}
	retainedOffset := int64(0)
	scanned := int64(0)
	for int64(len(scopeKeys)) < normalizedLimit && scanned < s.capacity {
		raw, err := s.client.Eval(ctx, redisAccountCircuitListDueScript,
			[]string{s.keys.states, s.keys.due},
			fmt.Sprintf("%d", now),
			fmt.Sprintf("%d", normalizedLimit-int64(len(scopeKeys))),
			fmt.Sprintf("%d", int64Min(scanChunkSize, s.capacity-scanned)),
			fmt.Sprintf("%d", retainedOffset)).Result()
		if err != nil {
			return nil, err
		}
		encoded, _ := redisStringResult(raw)
		page, err := parseListDuePage(encoded)
		if err != nil {
			return nil, err
		}
		scanned += page.scanned
		retainedOffset = page.nextOffset
		for _, scopeKey := range page.scopeKeys {
			if _, ok := seen[scopeKey]; !ok {
				seen[scopeKey] = struct{}{}
				scopeKeys = append(scopeKeys, scopeKey)
			}
		}
		if page.exhausted || page.scanned == 0 {
			break
		}
	}
	states := make([]State, 0, len(scopeKeys))
	for _, scopeKey := range scopeKeys {
		raw, err := s.client.HGet(ctx, s.keys.states, scopeKey).Result()
		if errors.Is(err, redis.Nil) {
			continue
		}
		if err != nil {
			return nil, err
		}
		var entry struct {
			State State `json:"state"`
		}
		if err := json.Unmarshal([]byte(raw), &entry); err != nil || entry.State.ScopeKey == "" && entry.State.Phase == "" {
			return nil, errors.New("Redis 账户电路状态结构无效")
		}
		state, err := s.Get(ctx, entry.State.Scope, &now)
		if err != nil {
			return nil, err
		}
		if accountCircuitDueAtMs(state) <= now {
			states = append(states, state)
		}
		if int64(len(states)) >= normalizedLimit {
			break
		}
	}
	return states, nil
}

// Size mirrors store.size.
func (s *RedisStore) Size(ctx context.Context) (int64, error) {
	nowMs := normalizedNowValue(nil, s.now)
	cleanupLimit := int64Min(s.capacity, 256)
	hlen, err := s.client.HLen(ctx, s.keys.states).Result()
	if err != nil {
		return 0, err
	}
	expiredIndexCount, err := s.client.ZCount(ctx, s.keys.closed, "-inf", fmt.Sprintf("%d", nowMs)).Result()
	if err != nil {
		return 0, err
	}
	maxPages := expiredIndexCount/cleanupLimit + 1
	size := hlen
	for page := int64(0); page < maxPages; page++ {
		raw, err := s.client.Eval(ctx, redisAccountCircuitSizeScript,
			[]string{s.keys.states, s.keys.due, s.keys.closed, s.keys.capacitySaturated},
			fmt.Sprintf("%d", nowMs), fmt.Sprintf("%d", s.capacity), fmt.Sprintf("%d", cleanupLimit)).Result()
		if err != nil {
			return 0, err
		}
		encoded, _ := redisStringResult(raw)
		var result struct {
			Size      *int64 `json:"size"`
			Processed *int64 `json:"processed"`
		}
		if encoded == "" || json.Unmarshal([]byte(encoded), &result) != nil ||
			result.Size == nil || result.Processed == nil {
			return 0, errors.New("Redis 账户电路容量统计返回值无效")
		}
		size = *result.Size
		if *result.Processed < cleanupLimit {
			return size, nil
		}
	}
	return size, nil
}

// Restore mirrors store.restore.
func (s *RedisStore) Restore(ctx context.Context, rawState State, nowMs *int64) (MutationResult, error) {
	state, err := normalizeConfirmationState(CloneState(rawState))
	if err != nil {
		return MutationResult{}, err
	}
	if err := AssertStateScopeKey(state); err != nil {
		return MutationResult{}, err
	}
	now := normalizedNowValue(nowMs, s.now)
	raw, err := s.client.Eval(ctx, redisAccountCircuitRestoreScript,
		[]string{s.keys.states, s.keys.due, s.keys.closed, s.keys.capacitySaturated},
		encodeJSON(state), fmt.Sprintf("%d", now), fmt.Sprintf("%d", s.closedRetentionMs),
		fmt.Sprintf("%d", s.capacity),
		encodeJSON(CapacityExhaustedState(state.Scope, state.DispatchRevision, now)),
		fmt.Sprintf("%d", s.replayLimitPerScope)).Result()
	if err != nil {
		return MutationResult{}, err
	}
	encoded, ok := redisStringResult(raw)
	if !ok || encoded == "" {
		return MutationResult{}, errors.New("Redis 账户电路重建返回值无效")
	}
	var parsed MutationResult
	if err := decodeStrict(encoded, &parsed); err != nil {
		return MutationResult{}, errors.New("Redis 账户电路重建返回值无效")
	}
	if parsed.Status == "" {
		return MutationResult{}, errors.New("Redis 账户电路重建返回值无效")
	}
	return parsed, nil
}

// ReplaceAccountDispatchRevision mirrors
// store.replaceAccountDispatchRevision.
func (s *RedisStore) ReplaceAccountDispatchRevision(ctx context.Context, input ReplaceAccountDispatchRevisionInput) (int64, error) {
	nowMs := normalizedNowValue(input.NowMs, s.now)
	statesCursor := "0"
	evidenceCursor := "0"
	var changed int64
	pages := int64(0)
	stateCount, err := s.client.HLen(ctx, s.keys.states).Result()
	if err != nil {
		return 0, err
	}
	evidenceCount, err := s.client.HLen(ctx, s.keys.escalation).Result()
	if err != nil {
		return 0, err
	}
	maxPages := int64(16)
	if candidate := (stateCount + evidenceCount + 1) * 4; candidate > maxPages {
		maxPages = candidate
	}
	seenCursorPairs := map[string]struct{}{}
	for {
		if statesCursor != "done" || evidenceCursor != "done" {
			cursorPair := statesCursor + "\x00" + evidenceCursor
			if _, ok := seenCursorPairs[cursorPair]; ok {
				return 0, errors.New("Redis 账户电路 revision 分页 cursor 未前进")
			}
			seenCursorPairs[cursorPair] = struct{}{}
		}
		raw, err := s.client.Eval(ctx, redisAccountCircuitAccountRevisionScript,
			[]string{s.keys.states, s.keys.due, s.keys.closed, s.keys.escalation, s.keys.capacitySaturated},
			input.AccountRuntimeKey, input.DispatchRevision, input.TransitionID,
			fmt.Sprintf("%d", nowMs), fmt.Sprintf("%d", s.closedRetentionMs),
			statesCursor, evidenceCursor).Result()
		if err != nil {
			return 0, err
		}
		encoded, _ := redisStringResult(raw)
		if encoded == "" {
			return 0, errors.New("Redis 账户电路 revision 分页返回值无效")
		}
		var page struct {
			StatesCursor   any    `json:"statesCursor"`
			EvidenceCursor any    `json:"evidenceCursor"`
			Changed        *int64 `json:"changed"`
		}
		if json.Unmarshal([]byte(encoded), &page) != nil {
			return 0, errors.New("Redis 账户电路 revision 分页返回值无效")
		}
		statesCursor = cursorString(page.StatesCursor, "done")
		evidenceCursor = cursorString(page.EvidenceCursor, "done")
		if page.Changed != nil {
			changed += *page.Changed
		}
		pages++
		if pages > maxPages {
			return 0, errors.New("Redis 账户电路 revision 分页未能收敛")
		}
		if statesCursor == "done" && evidenceCursor == "done" {
			return changed, nil
		}
	}
}

func (s *RedisStore) executeTransition(ctx context.Context, operation string, scope Scope, payload map[string]any) (MutationResult, error) {
	return s.execute(ctx, operation, scope, payload, func(payload map[string]any) error {
		return validateOperationPayload(operation, payload)
	})
}

func (s *RedisStore) execute(
	ctx context.Context, operation string, scope Scope, payload map[string]any,
	validate func(map[string]any) error,
) (MutationResult, error) {
	nowMs := normalizedNowValue(pointerNowMs(payload), s.now)
	payload["scope"] = scope
	payload["scopeKey"] = MustScopeKey(scope)
	payload["nowMs"] = nowMs
	payload["closedState"] = ClosedState(scope, "", 0, "", 0)
	dispatchRevision := ""
	if value, ok := payload["dispatchRevision"].(string); ok {
		dispatchRevision = value
	}
	payload["capacityState"] = CapacityExhaustedState(scope, dispatchRevision, nowMs)
	payload["operation"] = operation
	if validate != nil {
		if err := validate(payload); err != nil {
			return MutationResult{}, err
		}
	}
	raw, err := s.client.Eval(ctx, redisAccountCircuitTransitionScript,
		[]string{s.keys.states, s.keys.due, s.keys.closed, s.keys.escalation, s.keys.capacitySaturated},
		encodeJSON(payload), fmt.Sprintf("%d", s.capacity), fmt.Sprintf("%d", s.closedRetentionMs),
		fmt.Sprintf("%d", s.replayLimitPerScope)).Result()
	if err != nil {
		return MutationResult{}, err
	}
	encoded, ok := redisStringResult(raw)
	if !ok || encoded == "" {
		return MutationResult{}, errors.New("Redis 账户电路转换返回值无效")
	}
	var parsed MutationResult
	if err := decodeStrict(encoded, &parsed); err != nil {
		return MutationResult{}, errors.New("Redis 账户电路转换结果结构无效")
	}
	if parsed.Status == "" || parsed.State.Phase == "" {
		return MutationResult{}, errors.New("Redis 账户电路转换结果结构无效")
	}
	return parsed, nil
}

// pointerNowMs extracts the nowMs value from a payload map that may carry it
// as int64 or *int64.
func pointerNowMs(payload map[string]any) *int64 {
	switch value := payload["nowMs"].(type) {
	case int64:
		return &value
	case *int64:
		return value
	}
	return nil
}

func nowMsValue(nowMs *int64, fallback func() int64) int64 {
	return normalizedNowValue(nowMs, fallback)
}

func cursorString(value any, fallback string) string {
	switch typed := value.(type) {
	case string:
		if typed != "" {
			return typed
		}
	case float64:
		return fmt.Sprintf("%d", int64(typed))
	case json.Number:
		return typed.String()
	}
	return fallback
}

func encodeJSON(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err) // marshal failures on internal payloads are programmer errors
	}
	return string(encoded)
}

// decodeStrict parses a Lua cjson response. Lua encodes an empty array as
// `{}`, so relatedStates is decoded leniently via stringList-style tolerance.
func decodeStrict(encoded string, dst any) error {
	decoder := json.NewDecoder(strings.NewReader(encoded))
	decoder.UseNumber()
	return decoder.Decode(dst)
}

func redisStringResult(raw any) (string, bool) {
	switch typed := raw.(type) {
	case string:
		return typed, true
	case []byte:
		return string(typed), true
	}
	return "", false
}

func numericRedisResult(raw any) (int64, error) {
	switch typed := raw.(type) {
	case int64:
		return typed, nil
	case float64:
		return int64(typed), nil
	case string:
		value, ok := parseSafeInteger(typed)
		if !ok {
			return 0, errors.New("Redis 账户电路数值返回无效")
		}
		return int64(value), nil
	}
	return 0, errors.New("Redis 账户电路数值返回无效")
}

func redisAccountCircuitStoreKeys(name, namespace string) redisCircuitKeys {
	safeName := sanitizeRedisName(name)
	if safeName == "" {
		safeName = "gateway-account-circuit"
	}
	prefix := redisNamespacedKey(fmt.Sprintf("juhe-ai:account-circuit:%s", safeName), namespace)
	return redisCircuitKeys{
		states:            prefix + ":states",
		due:               prefix + ":due",
		closed:            prefix + ":closed",
		escalation:        prefix + ":escalation",
		capacitySaturated: prefix + ":capacity-saturated",
	}
}

// sanitizeRedisName mirrors name.trim().replace(/[^a-zA-Z0-9:_-]/g, '_').
func sanitizeRedisName(name string) string {
	trimmed := strings.TrimSpace(name)
	var out strings.Builder
	for _, c := range trimmed {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == ':', c == '_', c == '-':
			out.WriteRune(c)
		default:
			out.WriteRune('_')
		}
	}
	return out.String()
}

// redisNamespacedKey mirrors shared/redis-namespace.ts: the namespace part is
// inserted after the juhe-ai root, matching the deployed key layout.
func redisNamespacedKey(key, namespace string) string {
	normalized := strings.TrimSpace(key)
	if normalized == "" {
		panic("Redis key 不能为空")
	}
	rootPrefix := "juhe-ai:"
	ns := sanitizeRedisNamespacePart(namespace)
	if ns == "" {
		return normalized
	}
	namespacePrefix := rootPrefix + ns + ":"
	if strings.HasPrefix(normalized, namespacePrefix) {
		return normalized
	}
	if strings.HasPrefix(normalized, rootPrefix) {
		return namespacePrefix + normalized[len(rootPrefix):]
	}
	return namespacePrefix + normalized
}

// sanitizeRedisNamespacePart mirrors sanitizeRedisNamespacePart.
func sanitizeRedisNamespacePart(value string) string {
	normalized := strings.TrimSpace(value)
	if normalized == "" {
		return ""
	}
	var out strings.Builder
	var lastUnderscore bool
	for _, c := range normalized {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_', c == '.', c == ':', c == '-':
			out.WriteRune(c)
			lastUnderscore = false
		default:
			if !lastUnderscore {
				out.WriteRune('_')
				lastUnderscore = true
			}
		}
	}
	result := strings.Trim(out.String(), "_")
	return result
}

type redisListDuePage struct {
	scopeKeys  []string
	scanned    int64
	nextOffset int64
	exhausted  bool
}

func parseListDuePage(encoded string) (redisListDuePage, error) {
	if encoded == "" {
		return redisListDuePage{}, errors.New("Redis 账户电路 due 分页返回无效")
	}
	var parsed struct {
		ScopeKeys  *[]any `json:"scopeKeys"`
		Scanned    *int64 `json:"scanned"`
		NextOffset *int64 `json:"nextOffset"`
		Exhausted  *bool  `json:"exhausted"`
	}
	if err := json.Unmarshal([]byte(encoded), &parsed); err != nil {
		return redisListDuePage{}, errors.New("Redis 账户电路 due 分页返回无效")
	}
	if parsed.ScopeKeys == nil || parsed.Scanned == nil || parsed.NextOffset == nil {
		return redisListDuePage{}, errors.New("Redis 账户电路 due 分页 scopeKeys 无效")
	}
	if *parsed.Scanned < 0 || *parsed.NextOffset < 0 {
		return redisListDuePage{}, errors.New("Redis 账户电路 due 分页游标无效")
	}
	scopeKeys := make([]string, 0, len(*parsed.ScopeKeys))
	for _, item := range *parsed.ScopeKeys {
		scopeKeys = append(scopeKeys, fmt.Sprintf("%v", item))
	}
	exhausted := parsed.Exhausted != nil && *parsed.Exhausted
	return redisListDuePage{scopeKeys: scopeKeys, scanned: *parsed.Scanned, nextOffset: *parsed.NextOffset, exhausted: exhausted}, nil
}

func validateOperationPayload(operation string, input map[string]any) error {
	if operation != "get" {
		if _, err := requiredPayloadString(input, "transitionId"); err != nil {
			return err
		}
	}
	if operation == "suspect" || operation == "replace_revision" {
		if _, err := requiredPayloadString(input, "dispatchRevision"); err != nil {
			return err
		}
	}
	if operation == "suspect" {
		if value, ok := input["confirmationFailuresRequired"].(int64); ok {
			if _, err := NormalizeConfirmationFailuresRequired(&value, LegacyConfirmationFailuresRequired); err != nil {
				return err
			}
		} else {
			if _, err := NormalizeConfirmationFailuresRequired(nil, LegacyConfirmationFailuresRequired); err != nil {
				return err
			}
		}
		if err := requiredEvidenceKeyPayload(input, "failureEvidenceKey"); err != nil {
			return err
		}
	}
	if operation == "acquire_confirmation" || operation == "acquire_canary" {
		if _, err := requiredPayloadString(input, "leaseId"); err != nil {
			return err
		}
		nowMs, _ := payloadInt64(input["nowMs"])
		leaseUntilMs, ok := payloadInt64(input["leaseUntilMs"])
		if !ok {
			return errors.New("账户电路时间必须是有限数值")
		}
		if leaseUntilMs <= nowMs {
			return errors.New("账户电路租约截止时间必须晚于当前时间")
		}
		if operation == "acquire_confirmation" {
			if _, present := input["expectedFailureEvidenceKey"]; present {
				if err := requiredEvidenceKeyPayload(input, "expectedFailureEvidenceKey"); err != nil {
					return err
				}
			}
			if _, present := input["confirmationEvidenceKey"]; present {
				if err := requiredEvidenceKeyPayload(input, "confirmationEvidenceKey"); err != nil {
					return err
				}
			}
		}
	}
	if operation == "close_suspect_observer" || operation == "close_suspect_key_rotation" {
		if err := requiredEvidenceKeyPayload(input, "expectedFailureEvidenceKey"); err != nil {
			return err
		}
		if operation == "close_suspect_observer" {
			if err := requiredEvidenceKeyPayload(input, "observerEvidenceKey"); err != nil {
				return err
			}
		}
	}
	if operation == "complete_confirmation" || operation == "complete_canary" {
		if _, err := requiredPayloadString(input, "leaseId"); err != nil {
			return err
		}
		outcome, _ := input["outcome"].(string)
		if outcome != OutcomeFramingComplete && outcome != OutcomeTransportFailure && outcome != OutcomeUnknown {
			return errors.New("账户电路结果类型无效")
		}
		if operation == "complete_confirmation" && outcome == OutcomeTransportFailure {
			if err := requiredEvidenceKeyPayload(input, "failureEvidenceKey"); err != nil {
				return err
			}
		}
		if operation == "complete_confirmation" {
			if value, present := input["framingCompleteDisposition"]; present {
				disposition, _ := value.(string)
				if disposition != "recovering" && disposition != "closed" {
					return errors.New("账户电路 framingCompleteDisposition 无效")
				}
			}
		}
	}
	return nil
}

func requiredPayloadString(input map[string]any, key string) (string, error) {
	value, _ := input[key].(string)
	normalized, err := requiredValue(value, key)
	if err != nil {
		return "", err
	}
	return normalized, nil
}

func requiredEvidenceKeyPayload(input map[string]any, key string) error {
	value, _ := input[key].(string)
	normalized := strings.ToLower(strings.TrimSpace(value))
	if normalized == "" {
		return fmt.Errorf("账户电路操作缺少 %s", key)
	}
	if !isSHA256Hex(normalized) {
		return errors.New("账户电路 failureEvidenceKey 必须是 SHA256")
	}
	return nil
}

func payloadInt64(value any) (int64, bool) {
	switch typed := value.(type) {
	case int64:
		return typed, true
	case float64:
		return int64(typed), true
	}
	return 0, false
}

func int64Min(left, right int64) int64 {
	if left < right {
		return left
	}
	return right
}
