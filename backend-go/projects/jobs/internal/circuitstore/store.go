package circuitstore

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	redis "github.com/redis/go-redis/v9"
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

// Scope mirrors AccountCircuitScope（camelCase JSON 与 Lua/Node 逐字段一致）。
type Scope struct {
	Kind              string `json:"kind"`
	AccountRuntimeKey string `json:"accountRuntimeKey,omitempty"`
	KeyFingerprint    string `json:"keyFingerprint,omitempty"`
	ProtocolProfile   string `json:"protocolProfile,omitempty"`
	RequestLane       string `json:"requestLane,omitempty"`
	ModelBucket       string `json:"modelBucket,omitempty"`
}

// Lease mirrors AccountCircuitLease.
type Lease struct {
	Kind         string `json:"kind"`
	LeaseID      string `json:"leaseId"`
	LeaseUntilMs int64  `json:"leaseUntilMs"`
}

// stringList 解码 Lua cjson 往返的数组（空数组编码为 {}）。
type stringList []string

func (l stringList) clone() stringList {
	if l == nil {
		return nil
	}
	out := make(stringList, len(l))
	copy(out, l)
	return out
}

func (l *stringList) UnmarshalJSON(raw []byte) error {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		*l = nil
		return nil
	}
	if trimmed == "{}" || trimmed == "[]" {
		*l = stringList{}
		return nil
	}
	var values []string
	if err := json.Unmarshal(raw, &values); err != nil {
		return err
	}
	*l = values
	return nil
}

// State mirrors AccountCircuitState（可选字段用指针保持 Node 的 undefined 语义）。
type State struct {
	ScopeKey                     string     `json:"scopeKey"`
	Scope                        Scope      `json:"scope"`
	Phase                        string     `json:"phase"`
	Generation                   int64      `json:"generation"`
	DispatchRevision             string     `json:"dispatchRevision"`
	TransitionID                 string     `json:"transitionId"`
	BackoffAttempt               int64      `json:"backoffAttempt"`
	RecoverySuccessCount         int64      `json:"recoverySuccessCount"`
	ConfirmationFailuresRequired *int64     `json:"confirmationFailuresRequired,omitempty"`
	ConfirmationFailureCount     *int64     `json:"confirmationFailureCount,omitempty"`
	FailureEvidenceKeys          stringList `json:"failureEvidenceKeys,omitempty"`
	OpenedAtMs                   *int64     `json:"openedAtMs,omitempty"`
	RetryAtMs                    *int64     `json:"retryAtMs,omitempty"`
	FailureReason                *string    `json:"failureReason,omitempty"`
	Lease                        *Lease     `json:"lease,omitempty"`
	HalfOpenOrigin               *string    `json:"halfOpenOrigin,omitempty"`
	IncidentID                   *string    `json:"incidentId,omitempty"`
	ShadowedByIncidentID         *string    `json:"shadowedByIncidentId,omitempty"`
	ChildIncidentIDs             stringList `json:"childIncidentIds,omitempty"`
	ChildScopeKeys               stringList `json:"childScopeKeys,omitempty"`
	RequiredRecoveryScopeKeys    stringList `json:"requiredRecoveryScopeKeys,omitempty"`
	RecoveryEvidenceScopeKeys    stringList `json:"recoveryEvidenceScopeKeys,omitempty"`
	UpdatedAtMs                  int64      `json:"updatedAtMs"`
}

// CloneState mirrors cloneAccountCircuitState.
func CloneState(state State) State {
	out := state
	out.Scope = Scope{
		Kind:              state.Scope.Kind,
		AccountRuntimeKey: state.Scope.AccountRuntimeKey,
		KeyFingerprint:    state.Scope.KeyFingerprint,
		ProtocolProfile:   state.Scope.ProtocolProfile,
		RequestLane:       state.Scope.RequestLane,
		ModelBucket:       state.Scope.ModelBucket,
	}
	if state.Lease != nil {
		lease := *state.Lease
		out.Lease = &lease
	}
	out.FailureEvidenceKeys = state.FailureEvidenceKeys.clone()
	out.ChildIncidentIDs = state.ChildIncidentIDs.clone()
	out.ChildScopeKeys = state.ChildScopeKeys.clone()
	out.RequiredRecoveryScopeKeys = state.RequiredRecoveryScopeKeys.clone()
	out.RecoveryEvidenceScopeKeys = state.RecoveryEvidenceScopeKeys.clone()
	return out
}

// stateList 解码 relatedStates（Lua 空数组 = {}）。
type stateList []State

func (l *stateList) UnmarshalJSON(raw []byte) error {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" || trimmed == "{}" || trimmed == "[]" {
		*l = nil
		return nil
	}
	var values []State
	if err := json.Unmarshal(raw, &values); err != nil {
		return err
	}
	*l = values
	return nil
}

func (l stateList) slice() []State {
	if l == nil {
		return nil
	}
	return append([]State{}, l...)
}

// MutationResult mirrors AccountCircuitMutationResult.
type MutationResult struct {
	Status        string    `json:"status"`
	State         State     `json:"state"`
	RelatedStates stateList `json:"relatedStates,omitempty"`
}

func (r MutationResult) relatedSlice() []State { return r.RelatedStates.slice() }

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

func pointerNowMs(payload map[string]any) *int64 {
	switch value := payload["nowMs"].(type) {
	case int64:
		return &value
	case *int64:
		return value
	}
	return nil
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
		panic(err) // 内部 payload 序列化失败属编程错误
	}
	return string(encoded)
}

// decodeStrict 解析 Lua cjson 响应（UseNumber 保持整数精度）。
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

// redisNamespacedKey mirrors shared/redis-namespace.ts（namespace 插在 juhe-ai
// 根之后，与部署键位一致；proberepo 速度优先键同规则）。
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
	return strings.Trim(out.String(), "_")
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

// ---- 小工具（与 gatewaycircuit 同源）----

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

func requiredValue(value, name string) (string, error) {
	normalized := strings.TrimSpace(value)
	if normalized == "" {
		return "", fmt.Errorf("账户电路操作缺少 %s", name)
	}
	return normalized, nil
}

func requiredScopePart(value, name string) (string, error) {
	normalized := strings.TrimSpace(value)
	if normalized == "" {
		return "", fmt.Errorf("账户电路作用域缺少 %s", name)
	}
	return normalized, nil
}

func encodedScopeKey(parts ...string) string {
	encoded := make([]string, len(parts))
	for i, part := range parts {
		encoded[i] = fmt.Sprintf("%d:%s", len(part), part)
	}
	return strings.Join(encoded, "|")
}

// normalizedNowValue mirrors normalizedNow（负值截 0）。
func normalizedNowValue(nowMs *int64, fallback func() int64) int64 {
	value := int64(0)
	if nowMs != nil {
		value = *nowMs
	} else if fallback != nil {
		value = fallback()
	}
	if value < 0 {
		return 0
	}
	return value
}

func positiveInteger(value int64, name string) (int64, error) {
	if value < 1 {
		return 0, fmt.Errorf("账户电路 %s 必须是正整数", name)
	}
	return value, nil
}

func isSHA256Hex(value string) bool {
	if len(value) != 64 {
		return false
	}
	for i := 0; i < len(value); i++ {
		c := value[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

// parseSafeInteger mirrors Number(value) + Number.isSafeInteger 检查。
func parseSafeInteger(value string) (float64, bool) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return 0, false
	}
	number, err := strconv.ParseFloat(trimmed, 64)
	if err != nil || math.IsNaN(number) || math.IsInf(number, 0) {
		return 0, false
	}
	if number != math.Trunc(number) || math.Abs(number) > 9007199254740991 {
		return 0, false
	}
	return number, true
}

func int64Min(left, right int64) int64 {
	if left < right {
		return left
	}
	return right
}

func sha1Hex(value string) string {
	sum := sha1.Sum([]byte(value))
	return hex.EncodeToString(sum[:])
}

func strPtr(value string) *string { return &value }
