package circuitstore

import (
	"context"
	"fmt"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/opsjobs"
)

// OpsJobsStore 把 RedisStore 适配为 opsjobs.CircuitStore 窄 port。
// opsjobs 契约类型（snake_case JSON，任务侧传输形状）与本包 wire 类型
// （camelCase，与 Node/Lua 逐字节一致）之间的转换全部收口在本文件；
// 除指针/切片形状外不改变任何语义字段。
type OpsJobsStore struct {
	store *RedisStore
}

// NewOpsJobsStore 包装底层 store。
func NewOpsJobsStore(store *RedisStore) *OpsJobsStore {
	if store == nil {
		return nil
	}
	return &OpsJobsStore{store: store}
}

// Close 释放底层 Redis 连接（Client 注入时为空操作）。
func (s *OpsJobsStore) Close() error {
	return nil
}

// Underlying 暴露 wire store（键空间验证/ Size 等扩展面）。
func (s *OpsJobsStore) Underlying() *RedisStore { return s.store }

// Get 实现 opsjobs.CircuitStore.Get。
func (s *OpsJobsStore) Get(ctx context.Context, scope opsjobs.CircuitScope, nowMS int64) (opsjobs.CircuitState, error) {
	state, err := s.store.Get(ctx, convertScope(scope), &nowMS)
	if err != nil {
		return opsjobs.CircuitState{}, err
	}
	return toOpsState(state)
}

// Restore 实现 opsjobs.CircuitStore.Restore。
func (s *OpsJobsStore) Restore(ctx context.Context, state opsjobs.CircuitState, nowMS int64) (opsjobs.CircuitMutationResult, error) {
	result, err := s.store.Restore(ctx, fromOpsState(state), &nowMS)
	if err != nil {
		return opsjobs.CircuitMutationResult{}, err
	}
	return toOpsMutationResult(result)
}

// ListDue 实现 opsjobs.CircuitStore.ListDue。
func (s *OpsJobsStore) ListDue(ctx context.Context, nowMS int64, limit int) ([]opsjobs.CircuitState, error) {
	states, err := s.store.ListDue(ctx, nowMS, limit)
	if err != nil {
		return nil, err
	}
	out := make([]opsjobs.CircuitState, 0, len(states))
	for _, state := range states {
		converted, err := toOpsState(state)
		if err != nil {
			return nil, err
		}
		out = append(out, converted)
	}
	return out, nil
}

// AcquireConfirmationLease 实现 opsjobs.CircuitStore.AcquireConfirmationLease。
func (s *OpsJobsStore) AcquireConfirmationLease(ctx context.Context, identity opsjobs.CircuitTransitionIdentity, lease opsjobs.CircuitLeaseSpec) (opsjobs.CircuitMutationResult, error) {
	result, err := s.store.AcquireConfirmationLease(ctx, AcquireLeaseInput{
		Scope:            convertScope(identity.Scope),
		Generation:       identity.Generation,
		DispatchRevision: identity.DispatchRevision,
		TransitionID:     identity.TransitionID,
		LeaseID:          lease.LeaseID,
		LeaseUntilMs:     lease.LeaseUntilMS,
		NowMs:            &identity.NowMS,
	})
	if err != nil {
		return opsjobs.CircuitMutationResult{}, err
	}
	return toOpsMutationResult(result)
}

// AcquireCanaryLease 实现 opsjobs.CircuitStore.AcquireCanaryLease。
func (s *OpsJobsStore) AcquireCanaryLease(ctx context.Context, identity opsjobs.CircuitTransitionIdentity, lease opsjobs.CircuitLeaseSpec) (opsjobs.CircuitMutationResult, error) {
	result, err := s.store.AcquireCanaryLease(ctx, AcquireLeaseInput{
		Scope:            convertScope(identity.Scope),
		Generation:       identity.Generation,
		DispatchRevision: identity.DispatchRevision,
		TransitionID:     identity.TransitionID,
		LeaseID:          lease.LeaseID,
		LeaseUntilMs:     lease.LeaseUntilMS,
		NowMs:            &identity.NowMS,
	})
	if err != nil {
		return opsjobs.CircuitMutationResult{}, err
	}
	return toOpsMutationResult(result)
}

// CompleteConfirmation 实现 opsjobs.CircuitStore.CompleteConfirmation。
func (s *OpsJobsStore) CompleteConfirmation(ctx context.Context, identity opsjobs.CircuitTransitionIdentity, leaseID string, completion opsjobs.CircuitCompletion) (opsjobs.CircuitMutationResult, error) {
	result, err := s.store.CompleteConfirmation(ctx, CompleteInput{
		Scope:                      convertScope(identity.Scope),
		Generation:                 identity.Generation,
		DispatchRevision:           identity.DispatchRevision,
		TransitionID:               identity.TransitionID,
		LeaseID:                    leaseID,
		Outcome:                    string(completion.Outcome),
		Reason:                     stringPtr(completion.Reason),
		FailureEvidenceKey:         stringPtr(completion.FailureEvidenceKey),
		FramingCompleteDisposition: stringPtr(completion.FramingCompleteDisposition),
		NowMs:                      &identity.NowMS,
	})
	if err != nil {
		return opsjobs.CircuitMutationResult{}, err
	}
	return toOpsMutationResult(result)
}

// CompleteCanary 实现 opsjobs.CircuitStore.CompleteCanary。
func (s *OpsJobsStore) CompleteCanary(ctx context.Context, identity opsjobs.CircuitTransitionIdentity, leaseID string, completion opsjobs.CircuitCompletion) (opsjobs.CircuitMutationResult, error) {
	result, err := s.store.CompleteCanary(ctx, CompleteInput{
		Scope:            convertScope(identity.Scope),
		Generation:       identity.Generation,
		DispatchRevision: identity.DispatchRevision,
		TransitionID:     identity.TransitionID,
		LeaseID:          leaseID,
		Outcome:          string(completion.Outcome),
		Reason:           stringPtr(completion.Reason),
		EvidenceScopeKey: stringPtr(completion.EvidenceScopeKey),
		NowMs:            &identity.NowMS,
	})
	if err != nil {
		return opsjobs.CircuitMutationResult{}, err
	}
	return toOpsMutationResult(result)
}

// ClearAccountEscalationEvidence 实现 opsjobs.CircuitStore 同名方法。
func (s *OpsJobsStore) ClearAccountEscalationEvidence(ctx context.Context, accountRuntimeKey, dispatchRevision, evidenceID string, nowMS int64) (bool, error) {
	return s.store.ClearAccountEscalationEvidence(ctx, accountRuntimeKey, dispatchRevision, evidenceID, &nowMS)
}

// ReplaceDispatchRevision 实现 opsjobs.CircuitStore.ReplaceDispatchRevision。
func (s *OpsJobsStore) ReplaceDispatchRevision(ctx context.Context, scope opsjobs.CircuitScope, dispatchRevision, transitionID string, nowMS int64) (opsjobs.CircuitMutationResult, error) {
	result, err := s.store.ReplaceDispatchRevision(ctx, convertScope(scope), dispatchRevision, transitionID, &nowMS)
	if err != nil {
		return opsjobs.CircuitMutationResult{}, err
	}
	return toOpsMutationResult(result)
}

// ReplaceAccountDispatchRevision 实现 opsjobs.CircuitStore 同名方法。
func (s *OpsJobsStore) ReplaceAccountDispatchRevision(ctx context.Context, accountRuntimeKey, dispatchRevision, transitionID string, nowMS int64) (int64, error) {
	return s.store.ReplaceAccountDispatchRevision(ctx, accountRuntimeKey, dispatchRevision, transitionID, &nowMS)
}

// ---- 类型转换（形状层，无语义改写）----

func convertScope(scope opsjobs.CircuitScope) Scope {
	return Scope{
		Kind:              string(scope.Kind),
		AccountRuntimeKey: scope.AccountRuntimeKey,
		KeyFingerprint:    scope.KeyFingerprint,
		ProtocolProfile:   scope.ProtocolProfile,
		RequestLane:       scope.RequestLane,
		ModelBucket:       scope.ModelBucket,
	}
}

func toScope(scope Scope) opsjobs.CircuitScope {
	return opsjobs.CircuitScope{
		Kind:              opsjobs.CircuitScopeKind(scope.Kind),
		AccountRuntimeKey: scope.AccountRuntimeKey,
		KeyFingerprint:    scope.KeyFingerprint,
		ProtocolProfile:   scope.ProtocolProfile,
		RequestLane:       scope.RequestLane,
		ModelBucket:       scope.ModelBucket,
	}
}

func toOpsState(state State) (opsjobs.CircuitState, error) {
	expectedScopeKey, err := ScopeKey(state.Scope)
	if err != nil {
		return opsjobs.CircuitState{}, err
	}
	converted := opsjobs.CircuitState{
		ScopeKey:             state.ScopeKey,
		Scope:                toScope(state.Scope),
		Phase:                opsjobs.CircuitPhase(state.Phase),
		Generation:           state.Generation,
		DispatchRevision:     state.DispatchRevision,
		TransitionID:         state.TransitionID,
		BackoffAttempt:       int(state.BackoffAttempt),
		RecoverySuccessCount: int(state.RecoverySuccessCount),
		UpdatedAtMS:          state.UpdatedAtMs,
	}
	if state.ScopeKey != expectedScopeKey {
		return opsjobs.CircuitState{}, fmt.Errorf("账户电路 wire scopeKey 与作用域字段不一致: %s", state.ScopeKey)
	}
	if state.ConfirmationFailuresRequired != nil {
		value := int(*state.ConfirmationFailuresRequired)
		converted.ConfirmationFailuresRequired = &value
	}
	if state.ConfirmationFailureCount != nil {
		value := int(*state.ConfirmationFailureCount)
		converted.ConfirmationFailureCount = &value
	}
	if len(state.FailureEvidenceKeys) > 0 {
		converted.FailureEvidenceKeys = append([]string(nil), state.FailureEvidenceKeys...)
	}
	if state.OpenedAtMs != nil {
		value := *state.OpenedAtMs
		converted.OpenedAtMS = &value
	}
	if state.RetryAtMs != nil {
		value := *state.RetryAtMs
		converted.RetryAtMS = &value
	}
	if state.FailureReason != nil {
		converted.FailureReason = *state.FailureReason
	}
	if state.Lease != nil {
		converted.Lease = &opsjobs.CircuitLease{
			Kind:         opsjobs.CircuitLeaseKind(state.Lease.Kind),
			LeaseID:      state.Lease.LeaseID,
			LeaseUntilMS: state.Lease.LeaseUntilMs,
		}
	}
	if state.HalfOpenOrigin != nil {
		converted.HalfOpenOrigin = *state.HalfOpenOrigin
	}
	if state.IncidentID != nil {
		converted.IncidentID = *state.IncidentID
	}
	if state.ShadowedByIncidentID != nil {
		converted.ShadowedByIncidentID = *state.ShadowedByIncidentID
	}
	if len(state.ChildIncidentIDs) > 0 {
		converted.ChildIncidentIDs = append([]string(nil), state.ChildIncidentIDs...)
	}
	if len(state.ChildScopeKeys) > 0 {
		converted.ChildScopeKeys = append([]string(nil), state.ChildScopeKeys...)
	}
	if len(state.RequiredRecoveryScopeKeys) > 0 {
		converted.RequiredRecoveryScopeKeys = append([]string(nil), state.RequiredRecoveryScopeKeys...)
	}
	if len(state.RecoveryEvidenceScopeKeys) > 0 {
		converted.RecoveryEvidenceScopeKeys = append([]string(nil), state.RecoveryEvidenceScopeKeys...)
	}
	return converted, nil
}

func fromOpsState(state opsjobs.CircuitState) State {
	converted := State{
		ScopeKey:             state.ScopeKey,
		Scope:                convertScope(state.Scope),
		Phase:                string(state.Phase),
		Generation:           state.Generation,
		DispatchRevision:     state.DispatchRevision,
		TransitionID:         state.TransitionID,
		BackoffAttempt:       int64(state.BackoffAttempt),
		RecoverySuccessCount: int64(state.RecoverySuccessCount),
		UpdatedAtMs:          state.UpdatedAtMS,
	}
	if state.ConfirmationFailuresRequired != nil {
		value := int64(*state.ConfirmationFailuresRequired)
		converted.ConfirmationFailuresRequired = &value
	}
	if state.ConfirmationFailureCount != nil {
		value := int64(*state.ConfirmationFailureCount)
		converted.ConfirmationFailureCount = &value
	}
	if len(state.FailureEvidenceKeys) > 0 {
		converted.FailureEvidenceKeys = stringList(append([]string(nil), state.FailureEvidenceKeys...))
	}
	if state.OpenedAtMS != nil {
		value := *state.OpenedAtMS
		converted.OpenedAtMs = &value
	}
	if state.RetryAtMS != nil {
		value := *state.RetryAtMS
		converted.RetryAtMs = &value
	}
	if state.FailureReason != "" {
		converted.FailureReason = strPtr(state.FailureReason)
	}
	if state.Lease != nil {
		converted.Lease = &Lease{
			Kind:         string(state.Lease.Kind),
			LeaseID:      state.Lease.LeaseID,
			LeaseUntilMs: state.Lease.LeaseUntilMS,
		}
	}
	if state.HalfOpenOrigin != "" {
		converted.HalfOpenOrigin = strPtr(state.HalfOpenOrigin)
	}
	if state.IncidentID != "" {
		converted.IncidentID = strPtr(state.IncidentID)
	}
	if state.ShadowedByIncidentID != "" {
		converted.ShadowedByIncidentID = strPtr(state.ShadowedByIncidentID)
	}
	if len(state.ChildIncidentIDs) > 0 {
		converted.ChildIncidentIDs = stringList(append([]string(nil), state.ChildIncidentIDs...))
	}
	if len(state.ChildScopeKeys) > 0 {
		converted.ChildScopeKeys = stringList(append([]string(nil), state.ChildScopeKeys...))
	}
	if len(state.RequiredRecoveryScopeKeys) > 0 {
		converted.RequiredRecoveryScopeKeys = stringList(append([]string(nil), state.RequiredRecoveryScopeKeys...))
	}
	if len(state.RecoveryEvidenceScopeKeys) > 0 {
		converted.RecoveryEvidenceScopeKeys = stringList(append([]string(nil), state.RecoveryEvidenceScopeKeys...))
	}
	return converted
}

func toOpsMutationResult(result MutationResult) (opsjobs.CircuitMutationResult, error) {
	state, err := toOpsState(result.State)
	if err != nil {
		return opsjobs.CircuitMutationResult{}, err
	}
	converted := opsjobs.CircuitMutationResult{
		Status: opsjobs.CircuitMutationStatus(result.Status),
		State:  state,
	}
	related := result.relatedSlice()
	if len(related) > 0 {
		converted.RelatedStates = make([]opsjobs.CircuitState, 0, len(related))
		for _, item := range related {
			convertedRelated, err := toOpsState(item)
			if err != nil {
				return opsjobs.CircuitMutationResult{}, err
			}
			converted.RelatedStates = append(converted.RelatedStates, convertedRelated)
		}
	}
	return converted, nil
}

func stringPtr(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
