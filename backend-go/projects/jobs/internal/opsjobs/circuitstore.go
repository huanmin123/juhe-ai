package opsjobs

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// 账户电路类型与状态契约逐字段对齐 Node
// modules/gateway/runtime/account-circuit-store.ts。所有毫秒时间为 int64。

type CircuitPhase string

const (
	CircuitPhaseClosed     CircuitPhase = "CLOSED"
	CircuitPhaseSuspect    CircuitPhase = "SUSPECT"
	CircuitPhaseOpen       CircuitPhase = "OPEN"
	CircuitPhaseHalfOpen   CircuitPhase = "HALF_OPEN"
	CircuitPhaseRecovering CircuitPhase = "RECOVERING"
)

type CircuitScopeKind string

const (
	CircuitScopeAccount       CircuitScopeKind = "account"
	CircuitScopeKey           CircuitScopeKind = "key"
	CircuitScopeProtocolModel CircuitScopeKind = "protocol_model"
)

// CircuitScope 请求通道只允许 text/image。
type CircuitScope struct {
	Kind              CircuitScopeKind `json:"kind"`
	AccountRuntimeKey string           `json:"account_runtime_key"`
	KeyFingerprint    string           `json:"key_fingerprint,omitempty"`
	ProtocolProfile   string           `json:"protocol_profile,omitempty"`
	RequestLane       string           `json:"request_lane,omitempty"` // text | image
	ModelBucket       string           `json:"model_bucket,omitempty"`
}

type CircuitLeaseKind string

const (
	CircuitLeaseConfirmation CircuitLeaseKind = "confirmation"
	CircuitLeaseHalfOpen     CircuitLeaseKind = "half_open"
	CircuitLeaseRecovery     CircuitLeaseKind = "recovery"
)

type CircuitLease struct {
	Kind         CircuitLeaseKind `json:"kind"`
	LeaseID      string           `json:"lease_id"`
	LeaseUntilMS int64            `json:"lease_until_ms"`
}

type CircuitState struct {
	ScopeKey                     string        `json:"scope_key"`
	Scope                        CircuitScope  `json:"scope"`
	Phase                        CircuitPhase  `json:"phase"`
	Generation                   int64         `json:"generation"`
	DispatchRevision             string        `json:"dispatch_revision"`
	TransitionID                 string        `json:"transition_id"`
	BackoffAttempt               int           `json:"backoff_attempt"`
	RecoverySuccessCount         int           `json:"recovery_success_count"`
	ConfirmationFailuresRequired *int          `json:"confirmation_failures_required,omitempty"`
	ConfirmationFailureCount     *int          `json:"confirmation_failure_count,omitempty"`
	FailureEvidenceKeys          []string      `json:"failure_evidence_keys,omitempty"`
	OpenedAtMS                   *int64        `json:"opened_at_ms,omitempty"`
	RetryAtMS                    *int64        `json:"retry_at_ms,omitempty"`
	FailureReason                string        `json:"failure_reason,omitempty"`
	Lease                        *CircuitLease `json:"lease,omitempty"`
	HalfOpenOrigin               string        `json:"half_open_origin,omitempty"` // OPEN | RECOVERING
	IncidentID                   string        `json:"incident_id,omitempty"`
	ShadowedByIncidentID         string        `json:"shadowed_by_incident_id,omitempty"`
	ChildIncidentIDs             []string      `json:"child_incident_ids,omitempty"`
	ChildScopeKeys               []string      `json:"child_scope_keys,omitempty"`
	RequiredRecoveryScopeKeys    []string      `json:"required_recovery_scope_keys,omitempty"`
	RecoveryEvidenceScopeKeys    []string      `json:"recovery_evidence_scope_keys,omitempty"`
	UpdatedAtMS                  int64         `json:"updated_at_ms"`
}

type CircuitMutationStatus string

const (
	CircuitMutationApplied               CircuitMutationStatus = "applied"
	CircuitMutationIdempotent            CircuitMutationStatus = "idempotent"
	CircuitMutationNotFound              CircuitMutationStatus = "not_found"
	CircuitMutationStateMismatch         CircuitMutationStatus = "state_mismatch"
	CircuitMutationStaleGeneration       CircuitMutationStatus = "stale_generation"
	CircuitMutationStaleDispatchRevision CircuitMutationStatus = "stale_dispatch_revision"
	CircuitMutationLeaseMismatch         CircuitMutationStatus = "lease_mismatch"
	CircuitMutationNotDue                CircuitMutationStatus = "not_due"
	CircuitMutationCapacityExhausted     CircuitMutationStatus = "capacity_exhausted"
)

type CircuitMutationResult struct {
	Status        CircuitMutationStatus `json:"status"`
	State         CircuitState          `json:"state"`
	RelatedStates []CircuitState        `json:"related_states,omitempty"`
}

// CircuitTransitionIdentity 是所有 mutation 的乐观并发身份。
type CircuitTransitionIdentity struct {
	Scope            CircuitScope
	Generation       int64
	DispatchRevision string
	TransitionID     string
	NowMS            int64
}

type CircuitLeaseSpec struct {
	LeaseID      string
	LeaseUntilMS int64
}

type CircuitCompletion struct {
	Outcome                    CircuitProbeVerdict `json:"outcome"`
	Reason                     string              `json:"reason,omitempty"`
	FailureEvidenceKey         string              `json:"failure_evidence_key,omitempty"`
	FramingCompleteDisposition string              `json:"framing_complete_disposition,omitempty"` // recovering | closed
	EvidenceScopeKey           string              `json:"evidence_scope_key,omitempty"`
}

type CircuitProbeVerdict string

const (
	CircuitVerdictFramingComplete  CircuitProbeVerdict = "framing_complete"
	CircuitVerdictTransportFailure CircuitProbeVerdict = "transport_failure"
	CircuitVerdictUnknown          CircuitProbeVerdict = "unknown"
)

// CircuitStore 是恢复/控制面任务需要的运行态存储窄 port。
// 生产实现是 Redis/DB 账户电路 store；jobs 侧只依赖本接口，
// 与 gateway 语义通过本 port + mock 闭环验证。
type CircuitStore interface {
	Get(ctx context.Context, scope CircuitScope, nowMS int64) (CircuitState, error)
	Restore(ctx context.Context, state CircuitState, nowMS int64) (CircuitMutationResult, error)
	ListDue(ctx context.Context, nowMS int64, limit int) ([]CircuitState, error)
	AcquireConfirmationLease(ctx context.Context, identity CircuitTransitionIdentity, lease CircuitLeaseSpec) (CircuitMutationResult, error)
	AcquireCanaryLease(ctx context.Context, identity CircuitTransitionIdentity, lease CircuitLeaseSpec) (CircuitMutationResult, error)
	CompleteConfirmation(ctx context.Context, identity CircuitTransitionIdentity, leaseID string, completion CircuitCompletion) (CircuitMutationResult, error)
	CompleteCanary(ctx context.Context, identity CircuitTransitionIdentity, leaseID string, completion CircuitCompletion) (CircuitMutationResult, error)
	ClearAccountEscalationEvidence(ctx context.Context, accountRuntimeKey, dispatchRevision, evidenceID string, nowMS int64) (bool, error)
	ReplaceDispatchRevision(ctx context.Context, scope CircuitScope, dispatchRevision, transitionID string, nowMS int64) (CircuitMutationResult, error)
	ReplaceAccountDispatchRevision(ctx context.Context, accountRuntimeKey, dispatchRevision, transitionID string, nowMS int64) (int64, error)
}

// ---- 契约常量（Node account-circuit-store.ts 导出值）----

const (
	CircuitDefaultConfirmationFailuresRequired     = 2
	CircuitLegacyConfirmationFailuresRequired      = 1
	CircuitConfirmationFailuresRequiredMin         = 1
	CircuitConfirmationFailuresRequiredMax         = 5
	CircuitEscalationDistinctScopeThresholdDefault = 3
	CircuitEscalationDistinctScopeThresholdMin     = 3
	CircuitEscalationDistinctScopeThresholdMax     = 64
	CircuitEscalationWindowMSDefault               = 10 * 60_000
	CircuitEscalationWindowMSMin                   = 60_000
	CircuitEscalationWindowMSMax                   = 24 * 60 * 60_000
)

// CircuitBackoffMS 对齐 Node 默认 JUHE_AI_GATEWAY_ACCOUNT_CIRCUIT_BACKOFF_MS。
var CircuitBackoffMS = []int64{3_000, 5_000, 10_000, 30_000, 60_000, 120_000, 300_000, 600_000, 900_000}

// AccountCircuitBackoffDelayMS 对齐 Node accountCircuitBackoffDelayMs；
// jitterSeed 非空时从 sha1(seed) 推导确定性抖动，供 Redis/内存 store 得到同一 deadline。
func AccountCircuitBackoffDelayMS(attempt int64, jitterSeed string, random func(int64) int64) int64 {
	index := int(min64(max64(0, attempt-1), int64(len(CircuitBackoffMS)-1)))
	base := CircuitBackoffMS[index]
	if index < 4 {
		return base
	}
	windowMS := PassiveScheduleJitterWindowMS(base)
	if windowMS <= 0 {
		return base
	}
	if jitterSeed != "" {
		digest := sha1.Sum([]byte(jitterSeed))
		sample := int64(binary.BigEndian.Uint32(digest[:4]))
		offset := sample%(windowMS*2+1) - windowMS
		if offset == 0 {
			offset = 1
		}
		return max64(1, base+offset)
	}
	if random != nil {
		return max64(1, base+random(base))
	}
	return max64(1, base+1)
}

// AccountCircuitScopeKey 对齐 Node accountCircuitScopeKey：长度前缀编码。
func AccountCircuitScopeKey(scope CircuitScope) (string, error) {
	accountRuntimeKey, err := requiredScopePart(scope.AccountRuntimeKey, "accountRuntimeKey")
	if err != nil {
		return "", err
	}
	switch scope.Kind {
	case CircuitScopeAccount:
		return encodedScopeKey([]string{"account", accountRuntimeKey}), nil
	case CircuitScopeKey:
		fingerprint, err := requiredScopePart(scope.KeyFingerprint, "keyFingerprint")
		if err != nil {
			return "", err
		}
		return encodedScopeKey([]string{"key", accountRuntimeKey, fingerprint}), nil
	case CircuitScopeProtocolModel:
		profile, err := requiredScopePart(scope.ProtocolProfile, "protocolProfile")
		if err != nil {
			return "", err
		}
		if scope.RequestLane != "text" && scope.RequestLane != "image" {
			return "", errors.New("账户电路作用域 requestLane 必须是 text 或 image")
		}
		bucket, err := requiredScopePart(scope.ModelBucket, "modelBucket")
		if err != nil {
			return "", err
		}
		return encodedScopeKey([]string{"protocol_model", accountRuntimeKey, profile, scope.RequestLane, bucket}), nil
	default:
		return "", fmt.Errorf("账户电路作用域类型无效: %s", scope.Kind)
	}
}

func encodedScopeKey(parts []string) string {
	encoded := make([]string, 0, len(parts))
	for _, part := range parts {
		encoded = append(encoded, fmt.Sprintf("%d:%s", len(part), part))
	}
	return strings.Join(encoded, "|")
}

func requiredScopePart(value, name string) (string, error) {
	normalized := strings.TrimSpace(value)
	if normalized == "" {
		return "", fmt.Errorf("账户电路作用域缺少 %s", name)
	}
	return normalized, nil
}

func closedAccountCircuitState(scope CircuitScope, dispatchRevision string, generation int64, transitionID string, updatedAtMS int64) CircuitState {
	scopeKey, _ := AccountCircuitScopeKey(scope)
	return CircuitState{
		ScopeKey:         scopeKey,
		Scope:            scope,
		Phase:            CircuitPhaseClosed,
		Generation:       generation,
		DispatchRevision: dispatchRevision,
		TransitionID:     transitionID,
		BackoffAttempt:   0,
		UpdatedAtMS:      updatedAtMS,
	}
}

// NormalizeAccountCircuitConfirmationFailuresRequired 对齐 Node 同名函数；
// 空值回落 legacy=1。
func NormalizeAccountCircuitConfirmationFailuresRequired(value *int) (int, error) {
	normalized := CircuitLegacyConfirmationFailuresRequired
	if value != nil {
		normalized = *value
	}
	if normalized < CircuitConfirmationFailuresRequiredMin || normalized > CircuitConfirmationFailuresRequiredMax {
		return 0, fmt.Errorf("账户电路 confirmationFailuresRequired 必须是 %d..%d 的整数",
			CircuitConfirmationFailuresRequiredMin, CircuitConfirmationFailuresRequiredMax)
	}
	return normalized, nil
}

// NormalizeAccountCircuitFailureEvidenceKey 对齐 Node 同名函数；64 位十六进制
// 原样通过，否则对 fallbackSeed 取 sha256。
func NormalizeAccountCircuitFailureEvidenceKey(value, fallbackSeed string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(value))
	if isFailureEvidenceKey(normalized) {
		return normalized, nil
	}
	seed := strings.TrimSpace(fallbackSeed)
	if seed == "" {
		return "", errors.New("账户电路 failure evidence 缺少 fallbackSeed")
	}
	sum := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(sum[:]), nil
}

func isFailureEvidenceKey(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, r := range value {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}

// AccountCircuitFailureEvidenceKeys 对齐 Node accountCircuitFailureEvidenceKeys：
// 去重后只保留末尾 required+1 个。
func AccountCircuitFailureEvidenceKeys(state CircuitState) ([]string, error) {
	required, err := NormalizeAccountCircuitConfirmationFailuresRequired(state.ConfirmationFailuresRequired)
	if err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	normalized := make([]string, 0, len(state.FailureEvidenceKeys))
	for _, value := range state.FailureEvidenceKeys {
		value = strings.ToLower(strings.TrimSpace(value))
		if !isFailureEvidenceKey(value) {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		normalized = append(normalized, value)
	}
	keep := required + 1
	if len(normalized) > keep {
		normalized = normalized[len(normalized)-keep:]
	}
	return normalized, nil
}

// AccountCircuitConfirmationFailureCount 对齐 Node 同名函数。
func AccountCircuitConfirmationFailureCount(state CircuitState) (int, error) {
	if state.ConfirmationFailureCount == nil {
		return 0, nil
	}
	value := *state.ConfirmationFailureCount
	if value < 0 || value > CircuitConfirmationFailuresRequiredMax {
		return 0, errors.New("账户电路 confirmationFailureCount 无效")
	}
	return value, nil
}

// circuitDueAtMS 对齐 Node accountCircuitDueAtMs。
func circuitDueAtMS(state CircuitState) int64 {
	if state.Phase == CircuitPhaseClosed {
		return int64(^uint64(0) >> 1) // +Inf 等价：永不到期
	}
	if state.Lease != nil {
		return state.Lease.LeaseUntilMS
	}
	if state.Phase == CircuitPhaseSuspect || state.Phase == CircuitPhaseOpen || state.Phase == CircuitPhaseRecovering {
		if state.RetryAtMS != nil {
			return *state.RetryAtMS
		}
		return int64(^uint64(0) >> 1)
	}
	return int64(^uint64(0) >> 1)
}

// circuitOutcome 对齐 recovery 的 circuitOutcome 映射。
func CircuitOutcome(outcome TransportProbeOutcome) CircuitProbeVerdict {
	if outcome.Kind == ProbeOutcomeFramingComplete && (outcome.SemanticSuccess == nil || *outcome.SemanticSuccess) {
		return CircuitVerdictFramingComplete
	}
	if outcome.Kind == ProbeOutcomeTransportIncomplete {
		return CircuitVerdictTransportFailure
	}
	return CircuitVerdictUnknown
}

// CircuitFailureReason 对齐 recovery 的 circuitFailureReason：
// background_probe:<failureKind>[:http_<status>]。
func CircuitFailureReason(outcome TransportProbeOutcome) string {
	if outcome.Kind != ProbeOutcomeTransportIncomplete {
		return ""
	}
	status := ""
	if outcome.StatusCode != nil {
		status = fmt.Sprintf(":http_%d", *outcome.StatusCode)
	}
	return fmt.Sprintf("background_probe:%s%s", outcome.FailureKind, status)
}

// BackgroundConfirmationEvidenceKey 对齐 recovery 的 backgroundConfirmationEvidenceKey。
func BackgroundConfirmationEvidenceKey(state CircuitState, leaseID string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("background_confirmation:%s:%d:%s", state.ScopeKey, state.Generation, leaseID)))
	return hex.EncodeToString(sum[:])
}

func isAppliedOrIdempotent(result CircuitMutationResult) bool {
	return result.Status == CircuitMutationApplied || result.Status == CircuitMutationIdempotent
}

func isFencingResult(result CircuitMutationResult) bool {
	return result.Status == CircuitMutationStaleGeneration ||
		result.Status == CircuitMutationStaleDispatchRevision ||
		result.Status == CircuitMutationLeaseMismatch
}

func isOlderNumericDispatchRevision(candidate, current string) bool {
	candidateNumber, candidateOK := parseSafePositiveInt64(candidate)
	currentNumber, currentOK := parseSafePositiveInt64(current)
	return candidateOK && currentOK && currentNumber > candidateNumber
}

func parseSafePositiveInt64(value string) (int64, bool) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return 0, false
	}
	var parsed int64
	if _, err := fmt.Sscanf(trimmed, "%d", &parsed); err != nil {
		return 0, false
	}
	if parsed <= 0 {
		return 0, false
	}
	return parsed, true
}

func cloneCircuitState(state CircuitState) CircuitState {
	cloned := state
	cloned.Scope = state.Scope
	cloned.FailureEvidenceKeys = cloneStringSlice(state.FailureEvidenceKeys)
	cloned.ChildIncidentIDs = cloneStringSlice(state.ChildIncidentIDs)
	cloned.ChildScopeKeys = cloneStringSlice(state.ChildScopeKeys)
	cloned.RequiredRecoveryScopeKeys = cloneStringSlice(state.RequiredRecoveryScopeKeys)
	cloned.RecoveryEvidenceScopeKeys = cloneStringSlice(state.RecoveryEvidenceScopeKeys)
	if state.Lease != nil {
		lease := *state.Lease
		cloned.Lease = &lease
	}
	if state.ConfirmationFailuresRequired != nil {
		value := *state.ConfirmationFailuresRequired
		cloned.ConfirmationFailuresRequired = &value
	}
	if state.ConfirmationFailureCount != nil {
		value := *state.ConfirmationFailureCount
		cloned.ConfirmationFailureCount = &value
	}
	if state.OpenedAtMS != nil {
		value := *state.OpenedAtMS
		cloned.OpenedAtMS = &value
	}
	if state.RetryAtMS != nil {
		value := *state.RetryAtMS
		cloned.RetryAtMS = &value
	}
	return cloned
}

func cloneStringSlice(values []string) []string {
	if values == nil {
		return nil
	}
	cloned := make([]string, len(values))
	copy(cloned, values)
	return cloned
}

func circuitMutationResult(status CircuitMutationStatus, state CircuitState, relatedStates []CircuitState) CircuitMutationResult {
	result := CircuitMutationResult{Status: status, State: cloneCircuitState(state)}
	if len(relatedStates) > 0 {
		result.RelatedStates = make([]CircuitState, 0, len(relatedStates))
		for _, related := range relatedStates {
			result.RelatedStates = append(result.RelatedStates, cloneCircuitState(related))
		}
	}
	return result
}

// hierarchyTransitionID 对齐 Node accountCircuitHierarchyTransitionId。
func hierarchyTransitionID(action, parentTransitionID, parentIncidentID, childScopeKey string, childGeneration int64) string {
	digest := sha1.New()
	_, _ = digest.Write([]byte(action))
	_, _ = digest.Write([]byte{'\x00'})
	_, _ = digest.Write([]byte(parentTransitionID))
	_, _ = digest.Write([]byte{'\x00'})
	_, _ = digest.Write([]byte(parentIncidentID))
	_, _ = digest.Write([]byte{'\x00'})
	_, _ = digest.Write([]byte(childScopeKey))
	_, _ = digest.Write([]byte{'\x00'})
	_, _ = digest.Write([]byte(fmt.Sprintf("%d", childGeneration)))
	return fmt.Sprintf("hierarchy:%s:%s", action, hex.EncodeToString(digest.Sum(nil)))
}

func sortedStrings(values []string) []string {
	cloned := cloneStringSlice(values)
	sort.Strings(cloned)
	return cloned
}
