package gatewaycircuit

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/safego"
)

// 契约源：backend-go/projects/jobs/internal/opsjobs/circuitrecovery.go（成对
// 维护，逐段语义对照；上游 Node
// modules/background/account-circuit-recovery.service.ts）。到期事故按批次
// 并发执行 acquire -> resolve target -> probe（租约 deadline 内）-> complete，
// 并通过 OnMutation 将每次 mutation 以 ServiceOptions.OnMutation 同形状
// （MutationEvent）通知投影——cmd 层可把主链既有 persist hook（bridge.Observe
// 包装，chain_circuit_controlplane.go）直接传给恢复 sweep，恢复转换与请求
// 热路径转换共用同一条 ledger 投影管道。
//
// 与 jobs 侧的形状差异（语义等价的适配，非行为偏离）：
//   - jobs CircuitStore 的 acquire/complete 以 (identity, lease/completion)
//     多参调用；gatewaycircuit.Store 以单一 Input struct 调用（types.go）。
//   - jobs CircuitRecoveryMutation 是恢复专用投影形状；gateway 侧直接复用
//     MutationEvent，Operation 携带恢复操作词表（见下方常量），Status/
//     PreviousPhase 语义一致。
//
// 恢复操作词表对照 jobs CircuitRecoveryOperation（五值）。acquire_confirmation/
// complete_confirmation/replace_revision 与主链 service 词表同字符串同语义
// （复用既有常量）；acquire_canary/complete_canary 为主链请求路径不存在的
// 恢复专属操作。
const (
	OperationAcquireCanary  CircuitOperation = "acquire_canary"
	OperationCompleteCanary CircuitOperation = "complete_canary"
)

// 恢复默认值对齐 Node（JUHE_AI_GATEWAY_ACCOUNT_CIRCUIT_RECOVERY_BATCH_SIZE=200
// / _LEASE_DURATION_MS=180000；契约源 CircuitRecoveryDefaults）。
const (
	RecoveryBatchSize          = 200
	RecoveryLeaseDurationMS    = int64(180_000)
	RecoveryDefaultConcurrency = 4
)

// RecoveryProbeTarget 是一次可执行的恢复探针目标（契约源
// CircuitRecoveryProbeTarget）。探针执行器以函数注入，便于 Mock。
type RecoveryProbeTarget struct {
	DispatchRevision string
	Probe            func(ctx context.Context) (TransportProbeOutcome, error)
}

// RecoveryTargetResolver 按到期状态解析探针目标；found=false 表示目标缺失
// （契约源 CircuitRecoveryTargetResolver）。
type RecoveryTargetResolver func(ctx context.Context, state State) (RecoveryProbeTarget, bool, error)

// RecoveryServiceOptions 允许注入 clock/id 生成器；并发=0 时取
// RecoveryDefaultConcurrency。OnMutation 与 ServiceOptions.OnMutation 同形状
// （func(ctx, MutationEvent) error），nil 时静默。
type RecoveryServiceOptions struct {
	BatchSize       int
	Concurrency     int
	LeaseDurationMS int64
	NowMS           func() int64
	CreateID        func() string
	OnMutation      func(ctx context.Context, input MutationEvent) error
}

// RecoverySweepResult 计数字段与 jobs CircuitRecoverySweepResult（及 Node
// AccountCircuitRecoverySweepResult）一致，含 CredentialRejectedCount。
type RecoverySweepResult struct {
	DueCount                 int `json:"dueCount"`
	LeasedCount              int `json:"leasedCount"`
	FramingCompleteCount     int `json:"framingCompleteCount"`
	TransportIncompleteCount int `json:"transportIncompleteCount"`
	UnknownCount             int `json:"unknownCount"`
	FencedCount              int `json:"fencedCount"`
	SkippedCount             int `json:"skippedCount"`
	CredentialRejectedCount  int `json:"credentialRejectedCount"`
}

type recoveryItemOutcome string

const (
	recoveryFramingComplete     recoveryItemOutcome = "framing_complete"
	recoveryTransportIncomplete recoveryItemOutcome = "transport_incomplete"
	recoveryUnknown             recoveryItemOutcome = "unknown"
	recoveryFenced              recoveryItemOutcome = "fenced"
	recoverySkipped             recoveryItemOutcome = "skipped"
	// credential_rejected：探测 401/403（凭据失效），相对 Node 的刻意偏离
	// 类别；结算走 transport_failure 臂（重新 OPEN + 退避），不推进恢复。
	recoveryCredentialRejected recoveryItemOutcome = "credential_rejected"
)

// RecoveryService 是账户电路后台恢复扫描（契约源 CircuitRecoveryService）。
type RecoveryService struct {
	store           Store
	resolveTarget   RecoveryTargetResolver
	batchSize       int
	concurrency     int
	leaseDurationMS int64
	nowMS           func() int64
	createID        func() string
	onMutation      func(ctx context.Context, input MutationEvent) error
}

// NewRecoveryService 构造恢复服务；store/resolver 必填，数值参数校验对齐
// 契约源（batchSize/leaseDurationMS/concurrency 均为正整数）。
func NewRecoveryService(store Store, resolveTarget RecoveryTargetResolver, options RecoveryServiceOptions) (*RecoveryService, error) {
	if store == nil {
		return nil, errors.New("账户电路恢复 store 未初始化")
	}
	if resolveTarget == nil {
		return nil, errors.New("账户电路恢复 resolver 未初始化")
	}
	batchSize := options.BatchSize
	if batchSize == 0 {
		batchSize = RecoveryBatchSize
	}
	leaseDurationMS := options.LeaseDurationMS
	if leaseDurationMS == 0 {
		leaseDurationMS = RecoveryLeaseDurationMS
	}
	if batchSize < 1 {
		return nil, fmt.Errorf("账户电路恢复 batchSize 必须是正整数")
	}
	if leaseDurationMS < 1 {
		return nil, fmt.Errorf("账户电路恢复 leaseDurationMs 必须是正整数")
	}
	concurrency := options.Concurrency
	if concurrency == 0 {
		concurrency = RecoveryDefaultConcurrency
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
		createID = NewRecoveryRandomID
	}
	return &RecoveryService{
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

// Sweep 执行一轮到期恢复。任一 item 失败最终以聚合错误返回，其余 item 的
// 结果仍计入 counters（对齐契约源 AggregateError 行为）。
func (s *RecoveryService) Sweep(ctx context.Context) (RecoverySweepResult, error) {
	due, err := s.store.ListDue(ctx, s.nowMS(), s.batchSize)
	if err != nil {
		return RecoverySweepResult{}, fmt.Errorf("读取账户电路到期状态失败: %w", err)
	}
	result := RecoverySweepResult{DueCount: len(due)}
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
		go func(state State) {
			defer safego.Recover("gatewaycircuit.recovery.sweepWorker")
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
			incrementRecoverySweepOutcome(&result, outcome)
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

func (s *RecoveryService) recover(ctx context.Context, dueState State) (recoveryItemOutcome, bool, error) {
	if dueState.Phase != PhaseSuspect && dueState.Phase != PhaseOpen && dueState.Phase != PhaseRecovering {
		return recoverySkipped, false, nil
	}
	nowMS := s.nowMS()
	leaseID := s.createID()
	leased := false
	isConfirmation := dueState.Phase == PhaseSuspect
	transitionID := s.createID()
	leaseUntilMS := nowMS + s.leaseDurationMS
	var acquired MutationResult
	var err error
	if isConfirmation {
		acquired, err = s.store.AcquireConfirmationLease(ctx, AcquireConfirmationLeaseInput{
			Scope:            dueState.Scope,
			Generation:       dueState.Generation,
			DispatchRevision: dueState.DispatchRevision,
			TransitionID:     transitionID,
			LeaseID:          leaseID,
			LeaseUntilMs:     leaseUntilMS,
		})
	} else {
		acquired, err = s.store.AcquireCanaryLease(ctx, AcquireCanaryLeaseInput{
			Scope:            dueState.Scope,
			Generation:       dueState.Generation,
			DispatchRevision: dueState.DispatchRevision,
			TransitionID:     transitionID,
			LeaseID:          leaseID,
			LeaseUntilMs:     leaseUntilMS,
		})
	}
	if err != nil {
		return "", leased, err
	}
	operation := OperationAcquireCanary
	if isConfirmation {
		operation = OperationAcquireConfirmation
	}
	if observeErr := s.observeMutation(ctx, operation, acquired, dueState.Phase); observeErr != nil {
		return "", leased, observeErr
	}
	if acquired.Status != MutationApplied {
		if isRecoveryFencingResult(acquired) {
			return recoveryFenced, leased, nil
		}
		return recoverySkipped, leased, nil
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
		return recoveryUnknown, leased, nil
	}
	if target.DispatchRevision != dueState.DispatchRevision {
		cancelResolve()
		replaced, err := s.store.ReplaceDispatchRevision(ctx, ReplaceDispatchRevisionInput{
			Scope:            dueState.Scope,
			DispatchRevision: target.DispatchRevision,
			TransitionID:     s.createID(),
			NowMs:            nowPtr(s.nowMS()),
		})
		if err != nil {
			return "", leased, err
		}
		if observeErr := s.observeMutation(ctx, OperationReplaceRevision, replaced, acquired.State.Phase); observeErr != nil {
			return "", leased, observeErr
		}
		if isAppliedOrIdempotent(replaced) {
			return recoveryFenced, leased, nil
		}
		return recoverySkipped, leased, nil
	}
	probeCtx, cancelProbe := context.WithCancel(ctx)
	outcome, probeErr := runRecoveryProbeWithinLease(probeCtx, target, s.leaseDurationMS)
	cancelResolve()
	if probeErr != nil {
		cancelProbe()
		if releaseErr := s.releaseUnknown(ctx, acquired.State, leaseID); releaseErr != nil {
			return "", leased, errors.Join(probeErr, releaseErr)
		}
		return "", leased, probeErr
	}

	// 失败证据：transport_incomplete 与 credential_rejected 都记一条独立
	// evidence key（契约源同段注释）。后者必须携带——complete_confirmation 的
	// transport_failure 臂要求 failureEvidenceKey（store 校验必填）；401/403
	// 作为独立确认失败证据语义也成立（凭据失效是真实的账号失败）。契约源对
	// canary 分支同样赋值该字段，但 store 的 completeCanary（两侧逐字节对齐）
	// 不消费它——gateway 侧 CompleteCanaryInput 无此字段，语义等价。
	failureEvidenceKey := ""
	if outcome.Kind == RecoveryProbeOutcomeTransportIncomplete || outcome.Kind == RecoveryProbeOutcomeCredentialRejected {
		failureEvidenceKey = BackgroundConfirmationEvidenceKey(dueState, leaseID)
	}
	var completed MutationResult
	if isConfirmation {
		disposition := "closed"
		completed, err = s.store.CompleteConfirmation(ctx, CompleteConfirmationInput{
			Scope:                      dueState.Scope,
			Generation:                 dueState.Generation,
			DispatchRevision:           dueState.DispatchRevision,
			TransitionID:               s.createID(),
			LeaseID:                    leaseID,
			Outcome:                    recoveryOutcomeVerdict(outcome),
			Reason:                     reasonPtr(recoveryFailureReason(outcome)),
			FailureEvidenceKey:         &failureEvidenceKey,
			FramingCompleteDisposition: &disposition,
			NowMs:                      nowPtr(s.nowMS()),
		})
	} else {
		completed, err = s.store.CompleteCanary(ctx, CompleteCanaryInput{
			Scope:            dueState.Scope,
			Generation:       dueState.Generation,
			DispatchRevision: dueState.DispatchRevision,
			TransitionID:     s.createID(),
			LeaseID:          leaseID,
			Outcome:          recoveryOutcomeVerdict(outcome),
			Reason:           reasonPtr(recoveryFailureReason(outcome)),
			NowMs:            nowPtr(s.nowMS()),
		})
	}
	if err != nil {
		cancelProbe()
		return "", leased, err
	}
	completeOperation := OperationCompleteCanary
	if isConfirmation {
		completeOperation = OperationCompleteConfirmation
	}
	cancelProbe()
	if observeErr := s.observeMutation(ctx, completeOperation, completed, acquired.State.Phase); observeErr != nil {
		return "", leased, observeErr
	}
	if !isAppliedOrIdempotent(completed) {
		if isRecoveryFencingResult(completed) {
			return recoveryFenced, leased, nil
		}
		return recoverySkipped, leased, nil
	}
	if outcome.Kind == RecoveryProbeOutcomeFramingComplete &&
		(outcome.SemanticSuccess == nil || !*outcome.SemanticSuccess) &&
		dueState.Scope.Kind == ScopeKindProtocolModel {
		if _, err := s.store.ClearAccountEscalationEvidence(ctx, ClearAccountEscalationEvidenceInput{
			AccountRuntimeKey: dueState.Scope.AccountRuntimeKey,
			DispatchRevision:  dueState.DispatchRevision,
			EvidenceID:        s.createID(),
			NowMs:             nowPtr(s.nowMS()),
		}); err != nil {
			return "", leased, err
		}
	}
	if outcome.Kind == RecoveryProbeOutcomeFramingComplete && (outcome.SemanticSuccess == nil || !*outcome.SemanticSuccess) {
		return recoveryFramingComplete, leased, nil
	}
	if outcome.Kind == RecoveryProbeOutcomeTransportIncomplete {
		return recoveryTransportIncomplete, leased, nil
	}
	if outcome.Kind == RecoveryProbeOutcomeCredentialRejected {
		return recoveryCredentialRejected, leased, nil
	}
	return recoveryUnknown, leased, nil
}

// releaseUnknown 对齐契约源 releaseUnknown：中止探针并以 unknown 完成租约。
func (s *RecoveryService) releaseUnknown(ctx context.Context, leasedState State, leaseID string) error {
	input := CompleteCanaryInput{
		Scope:            leasedState.Scope,
		Generation:       leasedState.Generation,
		DispatchRevision: leasedState.DispatchRevision,
		TransitionID:     s.createID(),
		LeaseID:          leaseID,
		Outcome:          OutcomeUnknown,
		NowMs:            nowPtr(s.nowMS()),
	}
	var (
		result MutationResult
		err    error
	)
	if leasedState.Phase == PhaseSuspect {
		result, err = s.store.CompleteConfirmation(ctx, CompleteConfirmationInput{
			Scope:            input.Scope,
			Generation:       input.Generation,
			DispatchRevision: input.DispatchRevision,
			TransitionID:     input.TransitionID,
			LeaseID:          input.LeaseID,
			Outcome:          input.Outcome,
			NowMs:            input.NowMs,
		})
	} else {
		result, err = s.store.CompleteCanary(ctx, input)
	}
	if err != nil {
		return err
	}
	operation := OperationCompleteCanary
	if leasedState.Phase == PhaseSuspect {
		operation = OperationCompleteConfirmation
	}
	if observeErr := s.observeMutation(ctx, operation, result, leasedState.Phase); observeErr != nil {
		return observeErr
	}
	if !isAppliedOrIdempotent(result) && !isRecoveryFencingResult(result) {
		return fmt.Errorf("账户电路未知探针结果释放失败：%s", result.Status)
	}
	return nil
}

// observeMutation 把每次 store mutation 以 MutationEvent 形状通知 OnMutation
// （契约源 observeMutation：NotFound 静默，回调缺席静默）。生产 hook 是
// bridge.Observe 包装（恒返回 nil，见 chain_circuit_controlplane.go）；
// 回调返回错误时按契约源语义向上传播并中止本项。
func (s *RecoveryService) observeMutation(ctx context.Context, operation CircuitOperation, result MutationResult, previousPhase string) error {
	if result.Status == MutationNotFound || s.onMutation == nil {
		return nil
	}
	return s.onMutation(ctx, MutationEvent{
		Scope:         result.State.Scope,
		State:         result.State,
		Status:        result.Status,
		Operation:     operation,
		PreviousPhase: previousPhase,
	})
}

// runRecoveryProbeWithinLease 在租约 deadline 内执行探针；超时视为
// unknown/task_failure。租约属于本任务，不能证明上游超时，因此不进入账户
// 失败证据。（契约源 runProbeWithinLease。）
func runRecoveryProbeWithinLease(ctx context.Context, target RecoveryProbeTarget, leaseDurationMS int64) (TransportProbeOutcome, error) {
	probeDone := make(chan recoveryProbeResult, 1)
	go func() {
		defer safego.Recover("gatewaycircuit.recovery.probe")
		outcome, err := target.Probe(ctx)
		probeDone <- recoveryProbeResult{outcome: outcome, err: err}
	}()
	timer := time.NewTimer(time.Duration(leaseDurationMS) * time.Millisecond)
	defer timer.Stop()
	select {
	case result := <-probeDone:
		return result.outcome, result.err
	case <-timer.C:
		return TransportProbeOutcome{Kind: RecoveryProbeOutcomeUnknown, FailureKind: RecoveryProbeFailureTaskFailure}, nil
	case <-ctx.Done():
		return TransportProbeOutcome{}, ctx.Err()
	}
}

type recoveryProbeResult struct {
	outcome TransportProbeOutcome
	err     error
}

func incrementRecoverySweepOutcome(result *RecoverySweepResult, outcome recoveryItemOutcome) {
	switch outcome {
	case recoveryFramingComplete:
		result.FramingCompleteCount++
	case recoveryTransportIncomplete:
		result.TransportIncompleteCount++
	case recoveryUnknown:
		result.UnknownCount++
	case recoveryFenced:
		result.FencedCount++
	case recoveryCredentialRejected:
		result.CredentialRejectedCount++
	default:
		result.SkippedCount++
	}
}

// NewRecoveryRandomID 生成 128 位随机十六进制 ID（恢复租约/transition 幂等
// 键；契约源 NewRandomID）。
func NewRecoveryRandomID() string {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return fmt.Sprintf("gateway-recovery-%d", recoveryRandomFallbackCounter.Add(1))
	}
	return hex.EncodeToString(bytes[:])
}

var recoveryRandomFallbackCounter atomic.Int64

// recoveryOutcomeVerdict 对齐契约源 CircuitOutcome：恢复探针 outcome 到
// store 结算 outcome 三值的映射。credential_rejected（探测 401/403，相对
// Node 的刻意偏离）落到现有 transport_failure 臂：complete_canary 重新 OPEN
// + 退避，complete_confirmation 记一次确认失败。不新增 store outcome 值，
// 运行态状态机（circuitstate 与两 store 的三值校验）不动。
func recoveryOutcomeVerdict(outcome TransportProbeOutcome) string {
	if outcome.Kind == RecoveryProbeOutcomeFramingComplete && (outcome.SemanticSuccess == nil || *outcome.SemanticSuccess) {
		return OutcomeFramingComplete
	}
	if outcome.Kind == RecoveryProbeOutcomeTransportIncomplete {
		return OutcomeTransportFailure
	}
	if outcome.Kind == RecoveryProbeOutcomeCredentialRejected {
		return OutcomeTransportFailure
	}
	return OutcomeUnknown
}

// recoveryFailureReason 对齐契约源 CircuitFailureReason：
// background_probe:<failureKind>[:http_<status>]。credential_rejected 用
// 独立原因段（不冒充 transport_incomplete），保证 OPEN 后 failureReason
// 的排查日志不被误导。
func recoveryFailureReason(outcome TransportProbeOutcome) string {
	status := ""
	if outcome.StatusCode != nil {
		status = fmt.Sprintf(":http_%d", *outcome.StatusCode)
	}
	switch outcome.Kind {
	case RecoveryProbeOutcomeTransportIncomplete:
		return fmt.Sprintf("background_probe:%s%s", outcome.FailureKind, status)
	case RecoveryProbeOutcomeCredentialRejected:
		return fmt.Sprintf("background_probe:credential_rejected%s", status)
	default:
		return ""
	}
}

// BackgroundConfirmationEvidenceKey 对齐契约源 BackgroundConfirmationEvidenceKey。
func BackgroundConfirmationEvidenceKey(state State, leaseID string) string {
	return sha256Hex(fmt.Sprintf("background_confirmation:%s:%d:%s", state.ScopeKey, state.Generation, leaseID))
}

func isAppliedOrIdempotent(result MutationResult) bool {
	return result.Status == MutationApplied || result.Status == MutationIdempotent
}

func isRecoveryFencingResult(result MutationResult) bool {
	return result.Status == MutationStaleGeneration ||
		result.Status == MutationStaleDispatchRevision ||
		result.Status == MutationLeaseMismatch
}

func nowPtr(value int64) *int64 { return &value }

func reasonPtr(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
