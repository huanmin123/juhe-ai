package opsjobs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
)

// 账户电路后台恢复扫描，逐语义对齐 Node
// modules/background/account-circuit-recovery.service.ts 的
// AccountCircuitRecoveryService。到期事故按批次并发执行
// acquire -> resolve target -> probe（租约 deadline 内）-> complete，
// 并通过 onMutation 将每次 mutation 投影给控制面。

// CircuitRecoveryProbeTarget 是一次可执行的恢复探针目标。
type CircuitRecoveryProbeTarget struct {
	DispatchRevision string
	Probe            func(ctx context.Context) (TransportProbeOutcome, error)
}

// CircuitRecoveryTargetResolver 按到期状态解析探针目标；found=false 表示目标缺失。
type CircuitRecoveryTargetResolver func(ctx context.Context, state CircuitState) (CircuitRecoveryProbeTarget, bool, error)

// CircuitRecoveryMutation 投影回调输入。
type CircuitRecoveryMutation struct {
	Scope         CircuitScope
	State         CircuitState
	Status        CircuitMutationStatus
	Operation     CircuitRecoveryOperation
	PreviousPhase CircuitPhase
}

type CircuitRecoveryOperation string

const (
	CircuitRecoveryAcquireConfirmation  CircuitRecoveryOperation = "acquire_confirmation"
	CircuitRecoveryAcquireCanary        CircuitRecoveryOperation = "acquire_canary"
	CircuitRecoveryCompleteConfirmation CircuitRecoveryOperation = "complete_confirmation"
	CircuitRecoveryCompleteCanary       CircuitRecoveryOperation = "complete_canary"
	CircuitRecoveryReplaceRevision      CircuitRecoveryOperation = "replace_revision"
)

// CircuitRecoverySweepResult 计数字段与 Node AccountCircuitRecoverySweepResult 一致。
type CircuitRecoverySweepResult struct {
	DueCount                 int `json:"dueCount"`
	LeasedCount              int `json:"leasedCount"`
	FramingCompleteCount     int `json:"framingCompleteCount"`
	TransportIncompleteCount int `json:"transportIncompleteCount"`
	UnknownCount             int `json:"unknownCount"`
	FencedCount              int `json:"fencedCount"`
	SkippedCount             int `json:"skippedCount"`
}

// CircuitRecoveryDefaults 对齐 Node 默认配置
// （JUHE_AI_GATEWAY_ACCOUNT_CIRCUIT_RECOVERY_BATCH_SIZE=200 /
// _LEASE_DURATION_MS=180000）。
const (
	CircuitRecoveryBatchSize          = 200
	CircuitRecoveryLeaseDurationMS    = 180_000
	CircuitRecoveryDefaultConcurrency = 4
)

// CircuitRecoveryServiceOptions 允许注入 clock/id 生成器；并发=0 时串行。
type CircuitRecoveryServiceOptions struct {
	BatchSize       int
	Concurrency     int
	LeaseDurationMS int64
	NowMS           func() int64
	CreateID        func() string
	OnMutation      func(CircuitRecoveryMutation)
}

type CircuitRecoveryService struct {
	store           CircuitStore
	resolveTarget   CircuitRecoveryTargetResolver
	batchSize       int
	concurrency     int
	leaseDurationMS int64
	nowMS           func() int64
	createID        func() string
	onMutation      func(CircuitRecoveryMutation)
}

func NewCircuitRecoveryService(store CircuitStore, resolveTarget CircuitRecoveryTargetResolver, options CircuitRecoveryServiceOptions) (*CircuitRecoveryService, error) {
	if store == nil {
		return nil, errors.New("账户电路恢复 store 未初始化")
	}
	if resolveTarget == nil {
		return nil, errors.New("账户电路恢复 resolver 未初始化")
	}
	batchSize := options.BatchSize
	if batchSize == 0 {
		batchSize = CircuitRecoveryBatchSize
	}
	leaseDurationMS := options.LeaseDurationMS
	if leaseDurationMS == 0 {
		leaseDurationMS = CircuitRecoveryLeaseDurationMS
	}
	if batchSize < 1 {
		return nil, fmt.Errorf("账户电路恢复 batchSize 必须是正整数")
	}
	if leaseDurationMS < 1 {
		return nil, fmt.Errorf("账户电路恢复 leaseDurationMs 必须是正整数")
	}
	concurrency := options.Concurrency
	if concurrency == 0 {
		concurrency = 1
	}
	if concurrency < 1 {
		return nil, fmt.Errorf("账户电路恢复 concurrency 必须是正整数")
	}
	nowMS := options.NowMS
	if nowMS == nil {
		return nil, errors.New("账户电路恢复必须注入 NowMS 时钟")
	}
	createID := options.CreateID
	if createID == nil {
		createID = NewRandomID
	}
	return &CircuitRecoveryService{
		store:           store,
		resolveTarget:   resolveTarget,
		batchSize:       batchSize,
		concurrency:     concurrency,
		leaseDurationMS: leaseDurationMS,
		nowMS:           nowMS,
		createID:        createID,
		onMutation:      options.OnMutation,
	}, nil
}

// Sweep 执行一轮到期恢复。任一 item 失败最终以聚合错误返回，
// 其余 item 的结果仍计入 counters（对齐 Node AggregateError 行为）。
func (s *CircuitRecoveryService) Sweep(ctx context.Context) (CircuitRecoverySweepResult, error) {
	due, err := s.store.ListDue(ctx, s.nowMS(), s.batchSize)
	if err != nil {
		return CircuitRecoverySweepResult{}, fmt.Errorf("读取账户电路到期状态失败: %w", err)
	}
	result := CircuitRecoverySweepResult{DueCount: len(due)}
	var (
		errMu sync.Mutex
		errs  []error
		wg    sync.WaitGroup
		sem   = make(chan struct{}, s.concurrency)
	)
loop:
	for _, state := range due {
		select {
		case <-ctx.Done():
			break loop
		default:
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(state CircuitState) {
			defer func() {
				<-sem
				wg.Done()
			}()
			outcome, leased, itemErr := s.recover(ctx, state)
			errMu.Lock()
			defer errMu.Unlock()
			if itemErr != nil {
				errs = append(errs, fmt.Errorf("scope=%s generation=%d phase=%s: %w", state.ScopeKey, state.Generation, state.Phase, itemErr))
				return
			}
			if leased {
				result.LeasedCount++
			}
			incrementSweepOutcome(&result, outcome)
		}(state)
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return result, fmt.Errorf("账户电路后台恢复已取消: %w", err)
	}
	if len(errs) > 0 {
		return result, fmt.Errorf("账户电路后台恢复失败：%d 个作用域未完成: %w", len(errs), errors.Join(errs...))
	}
	return result, nil
}

type circuitRecoveryItemOutcome string

const (
	circuitRecoveryFramingComplete     circuitRecoveryItemOutcome = "framing_complete"
	circuitRecoveryTransportIncomplete circuitRecoveryItemOutcome = "transport_incomplete"
	circuitRecoveryUnknown             circuitRecoveryItemOutcome = "unknown"
	circuitRecoveryFenced              circuitRecoveryItemOutcome = "fenced"
	circuitRecoverySkipped             circuitRecoveryItemOutcome = "skipped"
)

func (s *CircuitRecoveryService) recover(ctx context.Context, dueState CircuitState) (circuitRecoveryItemOutcome, bool, error) {
	if dueState.Phase != CircuitPhaseSuspect && dueState.Phase != CircuitPhaseOpen && dueState.Phase != CircuitPhaseRecovering {
		return circuitRecoverySkipped, false, nil
	}
	nowMS := s.nowMS()
	leaseID := s.createID()
	leased := false
	isConfirmation := dueState.Phase == CircuitPhaseSuspect
	identity := CircuitTransitionIdentity{
		Scope:            dueState.Scope,
		Generation:       dueState.Generation,
		DispatchRevision: dueState.DispatchRevision,
		TransitionID:     s.createID(),
		NowMS:            nowMS,
	}
	lease := CircuitLeaseSpec{LeaseID: leaseID, LeaseUntilMS: nowMS + s.leaseDurationMS}
	var acquired CircuitMutationResult
	var err error
	if isConfirmation {
		acquired, err = s.store.AcquireConfirmationLease(ctx, identity, lease)
	} else {
		acquired, err = s.store.AcquireCanaryLease(ctx, identity, lease)
	}
	if err != nil {
		return "", leased, err
	}
	operation := CircuitRecoveryAcquireCanary
	if isConfirmation {
		operation = CircuitRecoveryAcquireConfirmation
	}
	if observeErr := s.observeMutation(operation, acquired, dueState.Phase); observeErr != nil {
		return "", leased, observeErr
	}
	if acquired.Status != CircuitMutationApplied {
		if isFencingResult(acquired) {
			return circuitRecoveryFenced, leased, nil
		}
		return circuitRecoverySkipped, leased, nil
	}
	leased = true

	resolveCtx, cancelResolve := context.WithCancel(ctx)
	target, found, resolveErr := s.resolveTarget(resolveCtx, dueState)
	if resolveErr != nil {
		cancelResolve()
		if releaseErr := s.releaseUnknown(ctx, acquired.State, leaseID); releaseErr != nil {
			return "", leased, errors.Join(resolveErr, releaseErr)
		}
		return "", leased, resolveErr
	}
	if !found {
		cancelResolve()
		if releaseErr := s.releaseUnknown(ctx, acquired.State, leaseID); releaseErr != nil {
			return "", leased, releaseErr
		}
		return circuitRecoveryUnknown, leased, nil
	}
	if target.DispatchRevision != dueState.DispatchRevision {
		cancelResolve()
		replaced, err := s.store.ReplaceDispatchRevision(ctx, dueState.Scope, target.DispatchRevision, s.createID(), s.nowMS())
		if err != nil {
			return "", leased, err
		}
		if observeErr := s.observeMutation(CircuitRecoveryReplaceRevision, replaced, acquired.State.Phase); observeErr != nil {
			return "", leased, observeErr
		}
		if isAppliedOrIdempotent(replaced) {
			return circuitRecoveryFenced, leased, nil
		}
		return circuitRecoverySkipped, leased, nil
	}
	probeCtx, cancelProbe := context.WithCancel(ctx)
	outcome, probeErr := runProbeWithinLease(probeCtx, target, s.leaseDurationMS)
	cancelResolve()
	if probeErr != nil {
		cancelProbe()
		if releaseErr := s.releaseUnknown(ctx, acquired.State, leaseID); releaseErr != nil {
			return "", leased, errors.Join(probeErr, releaseErr)
		}
		return "", leased, probeErr
	}

	completion := CircuitCompletion{
		Outcome: CircuitOutcome(outcome),
		Reason:  CircuitFailureReason(outcome),
	}
	if outcome.Kind == ProbeOutcomeTransportIncomplete {
		completion.FailureEvidenceKey = BackgroundConfirmationEvidenceKey(dueState, leaseID)
	}
	if isConfirmation {
		completion.FramingCompleteDisposition = "closed"
	}
	completeIdentity := identity
	completeIdentity.TransitionID = s.createID()
	completeIdentity.NowMS = s.nowMS()
	var completed CircuitMutationResult
	if isConfirmation {
		completed, err = s.store.CompleteConfirmation(ctx, completeIdentity, leaseID, completion)
	} else {
		completed, err = s.store.CompleteCanary(ctx, completeIdentity, leaseID, completion)
	}
	if err != nil {
		cancelProbe()
		return "", leased, err
	}
	completeOperation := CircuitRecoveryCompleteCanary
	if isConfirmation {
		completeOperation = CircuitRecoveryCompleteConfirmation
	}
	cancelProbe()
	if observeErr := s.observeMutation(completeOperation, completed, acquired.State.Phase); observeErr != nil {
		return "", leased, observeErr
	}
	if !isAppliedOrIdempotent(completed) {
		if isFencingResult(completed) {
			return circuitRecoveryFenced, leased, nil
		}
		return circuitRecoverySkipped, leased, nil
	}
	if outcome.Kind == ProbeOutcomeFramingComplete &&
		(outcome.SemanticSuccess == nil || !*outcome.SemanticSuccess) &&
		dueState.Scope.Kind == CircuitScopeProtocolModel {
		if _, err := s.store.ClearAccountEscalationEvidence(ctx, dueState.Scope.AccountRuntimeKey, dueState.DispatchRevision, s.createID(), s.nowMS()); err != nil {
			return "", leased, err
		}
	}
	if outcome.Kind == ProbeOutcomeFramingComplete && (outcome.SemanticSuccess == nil || !*outcome.SemanticSuccess) {
		return circuitRecoveryFramingComplete, leased, nil
	}
	if outcome.Kind == ProbeOutcomeTransportIncomplete {
		return circuitRecoveryTransportIncomplete, leased, nil
	}
	return circuitRecoveryUnknown, leased, nil
}

// releaseUnknown 对齐 Node releaseUnknown：中止探针并以 unknown 完成租约。
func (s *CircuitRecoveryService) releaseUnknown(ctx context.Context, leasedState CircuitState, leaseID string) error {
	identity := CircuitTransitionIdentity{
		Scope:            leasedState.Scope,
		Generation:       leasedState.Generation,
		DispatchRevision: leasedState.DispatchRevision,
		TransitionID:     s.createID(),
		NowMS:            s.nowMS(),
	}
	completion := CircuitCompletion{Outcome: CircuitVerdictUnknown}
	var (
		result CircuitMutationResult
		err    error
	)
	if leasedState.Phase == CircuitPhaseSuspect {
		result, err = s.store.CompleteConfirmation(ctx, identity, leaseID, completion)
	} else {
		result, err = s.store.CompleteCanary(ctx, identity, leaseID, completion)
	}
	if err != nil {
		return err
	}
	operation := CircuitRecoveryCompleteCanary
	if leasedState.Phase == CircuitPhaseSuspect {
		operation = CircuitRecoveryCompleteConfirmation
	}
	if observeErr := s.observeMutation(operation, result, leasedState.Phase); observeErr != nil {
		return observeErr
	}
	if !isAppliedOrIdempotent(result) && !isFencingResult(result) {
		return fmt.Errorf("账户电路未知探针结果释放失败：%s", result.Status)
	}
	return nil
}

func (s *CircuitRecoveryService) observeMutation(operation CircuitRecoveryOperation, result CircuitMutationResult, previousPhase CircuitPhase) error {
	if result.Status == CircuitMutationNotFound || s.onMutation == nil {
		return nil
	}
	s.onMutation(CircuitRecoveryMutation{
		Scope:         result.State.Scope,
		State:         result.State,
		Status:        result.Status,
		Operation:     operation,
		PreviousPhase: previousPhase,
	})
	return nil
}

// runProbeWithinLease 在租约 deadline 内执行探针；超时视为 unknown/task_failure。
// 租约属于本任务，不能证明上游超时，因此不进入账户失败证据。
func runProbeWithinLease(ctx context.Context, target CircuitRecoveryProbeTarget, leaseDurationMS int64) (TransportProbeOutcome, error) {
	probeDone := make(chan probeResult, 1)
	go func() {
		outcome, err := target.Probe(ctx)
		probeDone <- probeResult{outcome: outcome, err: err}
	}()
	timer := newTimer(leaseDurationMS)
	defer stopTimer(timer)
	select {
	case result := <-probeDone:
		return result.outcome, result.err
	case <-timer.C:
		return TransportProbeOutcome{Kind: ProbeOutcomeUnknown, FailureKind: ProbeFailureTaskFailure}, nil
	case <-ctx.Done():
		return TransportProbeOutcome{}, ctx.Err()
	}
}

type probeResult struct {
	outcome TransportProbeOutcome
	err     error
}

func incrementSweepOutcome(result *CircuitRecoverySweepResult, outcome circuitRecoveryItemOutcome) {
	switch outcome {
	case circuitRecoveryFramingComplete:
		result.FramingCompleteCount++
	case circuitRecoveryTransportIncomplete:
		result.TransportIncompleteCount++
	case circuitRecoveryUnknown:
		result.UnknownCount++
	case circuitRecoveryFenced:
		result.FencedCount++
	default:
		result.SkippedCount++
	}
}

// NewRandomID 生成 128 位随机十六进制 ID（恢复租约/transition 幂等键）。
func NewRandomID() string {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return fmt.Sprintf("ops-%d", randomFallbackCounter.Add(1))
	}
	return hex.EncodeToString(bytes[:])
}

// ParseRecoveryRuntimeIdentity 对齐 Node parseRecoveryRuntimeIdentity：
// account 或 account:authorized:systemAccount:group:authorization。
type RecoveryRuntimeIdentity struct {
	Kind            string // owner | authorized
	AccountID       string
	SystemAccountID string
	GroupID         string
	AuthorizationID string
}

func ParseRecoveryRuntimeIdentity(runtimeKey string) (RecoveryRuntimeIdentity, bool) {
	const marker = ":authorized:"
	markerIndex := indexOf(runtimeKey, marker)
	if markerIndex < 0 {
		accountID := runtimeAccountIDFromKey(runtimeKey)
		if accountID == "" {
			return RecoveryRuntimeIdentity{}, false
		}
		return RecoveryRuntimeIdentity{Kind: "owner", AccountID: accountID}, true
	}
	accountID := trimSpaces(runtimeKey[:markerIndex])
	rest := runtimeKey[markerIndex+len(marker):]
	parts := splitNonEmpty(rest, ":")
	if accountID == "" || len(parts) != 3 {
		return RecoveryRuntimeIdentity{}, false
	}
	return RecoveryRuntimeIdentity{
		Kind:            "authorized",
		AccountID:       accountID,
		SystemAccountID: parts[0],
		GroupID:         parts[1],
		AuthorizationID: parts[2],
	}, true
}

// RuntimeAccountIDFromKey 对齐 Node runtimeAccountIdFromKey：取首个 ':' 之前。
func RuntimeAccountIDFromKey(runtimeKey string) string {
	for index, r := range runtimeKey {
		if r == ':' {
			return runtimeKey[:index]
		}
	}
	return runtimeKey
}

func runtimeAccountIDFromKey(runtimeKey string) string {
	return trimSpaces(RuntimeAccountIDFromKey(runtimeKey))
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

func splitNonEmpty(value, sep string) []string {
	parts := stringsSplit(value, sep)
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := trimSpaces(part)
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

// CurrentDispatchRevision 对齐 Node currentDispatchRevision：正整数才有值。
func CurrentDispatchRevision(dispatchRevision int64) (string, bool) {
	if dispatchRevision > 0 {
		return fmt.Sprintf("%d", dispatchRevision), true
	}
	return "", false
}
