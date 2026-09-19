package circuitstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"

	redis "github.com/redis/go-redis/v9"

	"github.com/huanminabc/juhe-ai/backend-go-platform/circuitstate"
	"github.com/huanminabc/juhe-ai/backend-go-platform/rediscfg"
)

// 本包是 jobs 侧账户电路运行态 Redis store，移植 Node
// modules/gateway/runtime/account-circuit-redis-store.ts（经 gateway 模块
// internal/gatewaycircuit 的已验证 Go 移植对照复制；跨 module 不可 import，
// 故逐字节复制 Lua 与键形状）。与 Node 网关 / Go 网关读写同一批键：
//
//	juhe-ai:{namespace}:account-circuit:gateway-account-circuit:{states,due,closed,escalation,capacity-saturated}
//
// 命名空间与 JUHE_AI_REDIS_NAMESPACE 一致；store 名保持 Node 默认
// gateway-account-circuit，不引入第二 writer。

// 默认值对齐 Node store options（account-circuit-store.ts）。
const (
	DefaultClosedRetentionMs   = int64(5 * 60_000)
	DefaultReplayLimitPerScope = int64(64)
	// StoreName 是 Node/gateway 双侧默认的键段名；jobs 不改名以保证键空间互通。
	StoreName = "gateway-account-circuit"
)

// REFACTOR-0008 跨模块成对收敛：与 gateway/internal/gatewaycircuit 逐字节
// 相同的共享运行态词汇（Scope/Lease/State/MutationResult 及其列表类型、
// CloneState）下潜到 shared/platform/circuitstate 作为单一事实；本包保留
// 类型别名，调用点零改动。MutationResult 的 RelatedStatesSlice 方法随类型
// 来自平台包（原未导出 relatedSlice 与 gateway 导出 RelatedStatesSlice 是
// 同一行为的两个名字，收敛为平台导出名）。
type (
	Scope          = circuitstate.Scope
	Lease          = circuitstate.Lease
	State          = circuitstate.State
	MutationResult = circuitstate.MutationResult
	stringList     = circuitstate.StringList
	stateList      = circuitstate.StateList
)

// CloneState mirrors cloneAccountCircuitState（下潜委托壳）。
func CloneState(state State) State { return circuitstate.CloneState(state) }

// ScopeKey 与 MustScopeKey 留守：两侧实现已文本漂移（本包内联字面量，
// gatewaycircuit 用作用域 kind 常量），行为等价，按对账结论不强行统一。

// ScopeKey mirrors accountCircuitScopeKey（与 opsjobs.AccountCircuitScopeKey
// 同一长度前缀编码，独立保留以形成单一键契约校验点）。
func ScopeKey(scope Scope) (string, error) {
	accountRuntimeKey, err := requiredScopePart(scope.AccountRuntimeKey, "accountRuntimeKey")
	if err != nil {
		return "", err
	}
	switch scope.Kind {
	case "account":
		return encodedScopeKey("account", accountRuntimeKey), nil
	case "key":
		keyFingerprint, err := requiredScopePart(scope.KeyFingerprint, "keyFingerprint")
		if err != nil {
			return "", err
		}
		return encodedScopeKey("key", accountRuntimeKey, keyFingerprint), nil
	case "protocol_model":
		protocolProfile, err := requiredScopePart(scope.ProtocolProfile, "protocolProfile")
		if err != nil {
			return "", err
		}
		if scope.RequestLane != "text" && scope.RequestLane != "image" {
			return "", errors.New("账户电路作用域 requestLane 必须是 text 或 image")
		}
		modelBucket, err := requiredScopePart(scope.ModelBucket, "modelBucket")
		if err != nil {
			return "", err
		}
		return encodedScopeKey("protocol_model", accountRuntimeKey, protocolProfile, scope.RequestLane, modelBucket), nil
	default:
		return "", fmt.Errorf("账户电路作用域 kind 无效: %s", scope.Kind)
	}
}

// MustScopeKey 用于已验证作用域。
func MustScopeKey(scope Scope) string {
	key, err := ScopeKey(scope)
	if err != nil {
		panic(err)
	}
	return key
}

// ClosedState mirrors closedAccountCircuitState.
func ClosedState(scope Scope, dispatchRevision string, generation int64, transitionID string, updatedAtMs int64) State {
	key := MustScopeKey(scope)
	return State{
		ScopeKey:         key,
		Scope:            scope,
		Phase:            "CLOSED",
		Generation:       generation,
		DispatchRevision: dispatchRevision,
		TransitionID:     transitionID,
		UpdatedAtMs:      updatedAtMs,
	}
}

// CapacityExhaustedState mirrors capacityExhaustedAccountCircuitState.
func CapacityExhaustedState(scope Scope, dispatchRevision string, nowMs int64) State {
	state := ClosedState(scope, dispatchRevision, 0, "runtime-capacity-exhausted", nowMs)
	state.Phase = "SUSPECT"
	reason := "runtime_state_capacity_exhausted"
	state.FailureReason = &reason
	retryAt := nowMs + 1_000
	state.RetryAtMs = &retryAt
	return state
}

// NormalizeFailureEvidenceKey mirrors normalizeAccountCircuitFailureEvidenceKey.
func NormalizeFailureEvidenceKey(value *string, fallbackSeed string) (string, error) {
	normalized := ""
	if value != nil {
		normalized = strings.ToLower(strings.TrimSpace(*value))
	}
	if isSHA256Hex(normalized) {
		return normalized, nil
	}
	seed := strings.TrimSpace(fallbackSeed)
	if seed == "" {
		return "", errors.New("账户电路 failure evidence 缺少 fallbackSeed")
	}
	sum := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(sum[:]), nil
}

// NormalizeConfirmationFailuresRequired mirrors 同名 Node 函数（Legacy=1 回落）。
func NormalizeConfirmationFailuresRequired(value *int64, fallback int64) (int64, error) {
	normalized := fallback
	if value != nil {
		normalized = *value
	}
	if normalized < 1 || normalized > 5 {
		return 0, fmt.Errorf("账户电路 confirmationFailuresRequired 必须是 1..5 的整数")
	}
	return normalized, nil
}

func normalizeConfirmationState(state State) (State, error) {
	if state.Phase == "CLOSED" {
		return state, nil
	}
	required, err := NormalizeConfirmationFailuresRequired(state.ConfirmationFailuresRequired, 1)
	if err != nil {
		return State{}, err
	}
	count := int64(0)
	if state.ConfirmationFailureCount != nil {
		count = *state.ConfirmationFailureCount
		if count < 0 || count > 5 {
			return State{}, errors.New("账户电路 confirmationFailureCount 无效")
		}
	}
	normalizedEvidence := make([]string, 0, len(state.FailureEvidenceKeys))
	seen := map[string]struct{}{}
	for _, value := range state.FailureEvidenceKeys {
		candidate := strings.ToLower(strings.TrimSpace(value))
		if !isSHA256Hex(candidate) {
			continue
		}
		if _, ok := seen[candidate]; ok {
			continue
		}
		seen[candidate] = struct{}{}
		normalizedEvidence = append(normalizedEvidence, candidate)
	}
	keep := int(required) + 1
	if len(normalizedEvidence) > keep {
		normalizedEvidence = normalizedEvidence[len(normalizedEvidence)-keep:]
	}
	next := state
	next.ConfirmationFailuresRequired = &required
	next.ConfirmationFailureCount = &count
	next.FailureEvidenceKeys = stringList(normalizedEvidence)
	if state.Phase == "SUSPECT" && state.RetryAtMs == nil {
		retryAt := state.UpdatedAtMs
		if state.Lease != nil {
			retryAt = state.Lease.LeaseUntilMs
		}
		next.RetryAtMs = &retryAt
	}
	return next, nil
}

// ---- Redis store ----

// RedisStoreOptions mirrors RedisAccountCircuitStoreOptions。RedisURL 建连
// （等价 Node getRedisClient）或注入 Client（测试）二选一。
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

// RedisStore mirrors RedisAccountCircuitStore。所有转移（含租约到期归一化与
// 索引维护）在单次 Lua 调用内完成；脚本为 Node 原文，语义不可漂移。
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
			return nil, fmt.Errorf("解析账户电路 Redis URL: %w", err)
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
		name = StoreName
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

// Close 释放自建 Redis 连接（Client 注入时为空操作）。
func (s *RedisStore) Close() error {
	if client, ok := s.client.(*redis.Client); ok && client != nil {
		return client.Close()
	}
	return nil
}

// Keys 暴露实际键（供键空间互通验证）。
func (s *RedisStore) Keys() (states, due, closed, escalation, capacitySaturated string) {
	return s.keys.states, s.keys.due, s.keys.closed, s.keys.escalation, s.keys.capacitySaturated
}

// Get mirrors store.get.
func (s *RedisStore) Get(ctx context.Context, scope Scope, nowMs *int64) (State, error) {
	result, err := s.execute(ctx, "get", scope, map[string]any{
		"nowMs": normalizedNowValue(nowMs, s.now),
	}, nil)
	if err != nil {
		return State{}, err
	}
	return result.State, nil
}

// AcquireConfirmationLease mirrors store.acquireConfirmationLease.
func (s *RedisStore) AcquireConfirmationLease(ctx context.Context, input AcquireLeaseInput) (MutationResult, error) {
	payload := map[string]any{
		"scope":            input.Scope,
		"generation":       input.Generation,
		"dispatchRevision": input.DispatchRevision,
		"transitionId":     input.TransitionID,
		"leaseId":          input.LeaseID,
		"leaseUntilMs":     input.LeaseUntilMs,
		"nowMs":            normalizedNowValue(input.NowMs, s.now),
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

// AcquireLeaseInput mirrors acquireConfirmationLease / acquireCanaryLease 输入。
type AcquireLeaseInput struct {
	Scope                      Scope
	Generation                 int64
	DispatchRevision           string
	TransitionID               string
	LeaseID                    string
	LeaseUntilMs               int64
	ExpectedFailureEvidenceKey *string
	ConfirmationEvidenceKey    *string
	NowMs                      *int64
}

// AcquireCanaryLease mirrors store.acquireCanaryLease.
func (s *RedisStore) AcquireCanaryLease(ctx context.Context, input AcquireLeaseInput) (MutationResult, error) {
	payload := map[string]any{
		"scope":            input.Scope,
		"generation":       input.Generation,
		"dispatchRevision": input.DispatchRevision,
		"transitionId":     input.TransitionID,
		"leaseId":          input.LeaseID,
		"leaseUntilMs":     input.LeaseUntilMs,
		"nowMs":            normalizedNowValue(input.NowMs, s.now),
	}
	return s.executeTransition(ctx, "acquire_canary", input.Scope, payload)
}

// CompleteConfirmation mirrors store.completeConfirmation.
func (s *RedisStore) CompleteConfirmation(ctx context.Context, input CompleteInput) (MutationResult, error) {
	payload := map[string]any{
		"scope":            input.Scope,
		"generation":       input.Generation,
		"dispatchRevision": input.DispatchRevision,
		"transitionId":     input.TransitionID,
		"leaseId":          input.LeaseID,
		"outcome":          input.Outcome,
		"nowMs":            normalizedNowValue(input.NowMs, s.now),
	}
	if input.Reason != nil && *input.Reason != "" {
		payload["reason"] = *input.Reason
	}
	if input.FramingCompleteDisposition != nil && *input.FramingCompleteDisposition != "" {
		payload["framingCompleteDisposition"] = *input.FramingCompleteDisposition
	}
	if input.Outcome == "transport_failure" {
		failureEvidenceKey, err := NormalizeFailureEvidenceKey(input.FailureEvidenceKey, "confirmation:"+input.LeaseID)
		if err != nil {
			return MutationResult{}, err
		}
		payload["failureEvidenceKey"] = failureEvidenceKey
	}
	return s.executeTransition(ctx, "complete_confirmation", input.Scope, payload)
}

// CompleteCanary mirrors store.completeCanary.
func (s *RedisStore) CompleteCanary(ctx context.Context, input CompleteInput) (MutationResult, error) {
	payload := map[string]any{
		"scope":            input.Scope,
		"generation":       input.Generation,
		"dispatchRevision": input.DispatchRevision,
		"transitionId":     input.TransitionID,
		"leaseId":          input.LeaseID,
		"outcome":          input.Outcome,
		"nowMs":            normalizedNowValue(input.NowMs, s.now),
	}
	if input.Reason != nil && *input.Reason != "" {
		payload["reason"] = *input.Reason
	}
	if input.EvidenceScopeKey != nil && *input.EvidenceScopeKey != "" {
		payload["evidenceScopeKey"] = *input.EvidenceScopeKey
	}
	return s.executeTransition(ctx, "complete_canary", input.Scope, payload)
}

// CompleteInput mirrors completeConfirmation / completeCanary 输入。
type CompleteInput struct {
	Scope                      Scope
	Generation                 int64
	DispatchRevision           string
	TransitionID               string
	LeaseID                    string
	Outcome                    string
	Reason                     *string
	FailureEvidenceKey         *string
	FramingCompleteDisposition *string
	EvidenceScopeKey           *string
	NowMs                      *int64
}

// ClearAccountEscalationEvidence mirrors store.clearAccountEscalationEvidence.
func (s *RedisStore) ClearAccountEscalationEvidence(ctx context.Context, accountRuntimeKey, dispatchRevision, evidenceID string, nowMs *int64) (bool, error) {
	if _, err := requiredValue(accountRuntimeKey, "accountRuntimeKey"); err != nil {
		return false, err
	}
	if _, err := requiredValue(dispatchRevision, "dispatchRevision"); err != nil {
		return false, err
	}
	if _, err := requiredValue(evidenceID, "evidenceId"); err != nil {
		return false, err
	}
	raw, err := s.client.Eval(ctx, redisAccountCircuitClearEscalationScript, []string{s.keys.escalation},
		accountRuntimeKey, dispatchRevision, evidenceID,
		fmt.Sprintf("%d", normalizedNowValue(nowMs, s.now))).Result()
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
func (s *RedisStore) ReplaceDispatchRevision(ctx context.Context, scope Scope, dispatchRevision, transitionID string, nowMs *int64) (MutationResult, error) {
	payload := map[string]any{
		"scope":            scope,
		"dispatchRevision": dispatchRevision,
		"transitionId":     transitionID,
		"nowMs":            normalizedNowValue(nowMs, s.now),
	}
	return s.executeTransition(ctx, "replace_revision", scope, payload)
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
	scanChunkSize := normalizedLimit * 2
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
		scanned += page.Scanned
		retainedOffset = page.NextOffset
		for _, scopeKey := range page.ScopeKeys {
			if _, ok := seen[scopeKey]; !ok {
				seen[scopeKey] = struct{}{}
				scopeKeys = append(scopeKeys, scopeKey)
			}
		}
		if page.Exhausted || page.Scanned == 0 {
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

// Restore mirrors store.restore.
func (s *RedisStore) Restore(ctx context.Context, rawState State, nowMs *int64) (MutationResult, error) {
	state, err := normalizeConfirmationState(CloneState(rawState))
	if err != nil {
		return MutationResult{}, err
	}
	expected, scopeErr := ScopeKey(state.Scope)
	if scopeErr != nil {
		return MutationResult{}, scopeErr
	}
	if state.ScopeKey != expected {
		return MutationResult{}, errors.New("账户电路 scopeKey 与作用域字段不一致")
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

// ReplaceAccountDispatchRevision mirrors store.replaceAccountDispatchRevision.
func (s *RedisStore) ReplaceAccountDispatchRevision(ctx context.Context, accountRuntimeKey, dispatchRevision, transitionID string, nowMs *int64) (int64, error) {
	now := normalizedNowValue(nowMs, s.now)
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
			accountRuntimeKey, dispatchRevision, transitionID,
			fmt.Sprintf("%d", now), fmt.Sprintf("%d", s.closedRetentionMs),
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

// REFACTOR-0008 下潜委托壳：两侧逐字节相同的解析原语收敛到
// shared/platform/circuitstate，包内调用点零改动。
func pointerNowMs(payload map[string]any) *int64 { return circuitstate.PointerNowMs(payload) }

func cursorString(value any, fallback string) string {
	return circuitstate.CursorString(value, fallback)
}

// encodeJSON 保持快速失败 panic 契约（复审 P1-1 恢复）：与 gatewaycircuit 侧
// "忽略错误返空串"的 jsonenc 语义不同，内部 payload 序列化失败属编程错误，
// 本包登记为语义变体不随 jsonenc 收敛。
func encodeJSON(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err) // 内部 payload 序列化失败属编程错误
	}
	return string(encoded)
}

func decodeStrict(encoded string, dst any) error { return circuitstate.DecodeStrict(encoded, dst) }

func redisStringResult(raw any) (string, bool) { return circuitstate.RedisStringResult(raw) }

func numericRedisResult(raw any) (int64, error) { return circuitstate.NumericRedisResult(raw) }

func redisAccountCircuitStoreKeys(name, namespace string) redisCircuitKeys {
	safeName := sanitizeRedisName(name)
	if safeName == "" {
		safeName = StoreName
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

// 实现收敛到 shared/platform/rediscfg（行为逐字节等价）。
func sanitizeRedisName(name string) string { return rediscfg.SanitizeRedisName(name) }

// redisNamespacedKey mirrors shared/redis-namespace.ts（namespace 插在 juhe-ai
// 根之后，与部署键位一致；proberepo 速度优先键同规则）。
// 实现收敛到 shared/platform/rediscfg（panic 语义等价）。
func redisNamespacedKey(key, namespace string) string {
	return rediscfg.NamespacedKey(key, namespace)
}

// 实现收敛到 shared/platform/rediscfg（行为逐字节等价）。
func sanitizeRedisNamespacePart(value string) string {
	return rediscfg.SanitizeRedisNamespacePart(value)
}

func parseListDuePage(encoded string) (circuitstate.RedisListDuePage, error) {
	return circuitstate.ParseListDuePage(encoded)
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
	if operation == "complete_confirmation" || operation == "complete_canary" {
		if _, err := requiredPayloadString(input, "leaseId"); err != nil {
			return err
		}
		outcome, _ := input["outcome"].(string)
		if outcome != "framing_complete" && outcome != "transport_failure" && outcome != "unknown" {
			return errors.New("账户电路结果类型无效")
		}
		if operation == "complete_confirmation" && outcome == "transport_failure" {
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
	return circuitstate.RequiredPayloadString(input, key)
}

func requiredEvidenceKeyPayload(input map[string]any, key string) error {
	return circuitstate.RequiredEvidenceKeyPayload(input, key)
}

func payloadInt64(value any) (int64, bool) { return circuitstate.PayloadInt64(value) }

// ---- 小工具（与 gatewaycircuit 同源）----

// defaultNowMs 留守：经本包 timeNowUnixMilli 读钟（与 gateway 侧直接
// time.Now 实现已漂移，行为等价）。
func defaultNowMs() int64 { return timeNowUnixMilli() }

func accountCircuitDueAtMs(state State) int64 {
	if state.Phase == "CLOSED" {
		return math.MaxInt64
	}
	if state.Lease != nil {
		return state.Lease.LeaseUntilMs
	}
	if state.Phase == "SUSPECT" || state.Phase == "OPEN" || state.Phase == "RECOVERING" {
		if state.RetryAtMs != nil {
			return *state.RetryAtMs
		}
		return math.MaxInt64
	}
	return math.MaxInt64
}

// REFACTOR-0008 下潜委托壳：两侧逐字节相同，收敛到
// shared/platform/circuitstate，包内调用点零改动。
func requiredValue(value, name string) (string, error) {
	return circuitstate.RequiredValue(value, name)
}

func requiredScopePart(value, name string) (string, error) {
	return circuitstate.RequiredScopePart(value, name)
}

func encodedScopeKey(parts ...string) string { return circuitstate.EncodedScopeKey(parts...) }

// normalizedNowValue mirrors normalizedNow（负值截 0）。
func normalizedNowValue(nowMs *int64, fallback func() int64) int64 {
	return circuitstate.NormalizedNowValue(nowMs, fallback)
}

func positiveInteger(value int64, name string) (int64, error) {
	return circuitstate.PositiveInteger(value, name)
}

func isSHA256Hex(value string) bool { return circuitstate.IsSHA256Hex(value) }

// parseSafeInteger mirrors Number(value) + Number.isSafeInteger 检查。
func parseSafeInteger(value string) (float64, bool) { return circuitstate.ParseSafeInteger(value) }

func int64Min(left, right int64) int64 { return circuitstate.Int64Min(left, right) }

func sha1Hex(value string) string { return circuitstate.SHA1Hex(value) }

func strPtr(value string) *string { return circuitstate.StrPtr(value) }
