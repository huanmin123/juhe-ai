package accounthealth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/schedulejitter"
	"github.com/huanminabc/juhe-ai/backend-go-platform/safego"
)

// Runner is the only J1 scheduler.  Its inputs come from the configured
// direct input reader (PostgreSQL or SQLite business/statistics reads); the
// signed-files channel is only an explicit fallback. Durable state lives in
// the jobs-owned store; it has no Node/Gateway/IPC or Redis dependency.
type Runner struct {
	cfg               Config
	store             *Store
	logger            *slog.Logger
	directInputReader directInputLoader
	// probeDrain 是 account_health_probe_request_outbox 的消费面（去跨进程
	// 战役第二刀接入；nil 表示通道未装配，runCycle 跳过 drain）。
	probeDrain *ProbeRequestDrain
	// usageRecorder 把真实执行的探针观测补记为使用记录（BUG-0194 方案 B：
	// go-only 形态探针直连上游，Node 时代经 /v1 派发链天然产生的
	// account_health_check 等使用记录在此补齐；nil 表示未装配，跳过）。
	usageRecorder ProbeUsageRecorder
	// terminalProjector 是 J1 终态投影补落地的事务入口（BUG-0260）：与
	// OutcomeProjector 的 drain 消费路径同源（ProjectOutcomeNow）。nil 表示
	// 组合根投影面装配失败降级；触发 cooldown_terminal_reproject 时以错误
	// 显式暴露，绝不回退为复测探针循环。
	terminalProjector *OutcomeProjector
	// backlogWarnedAt 是 outbox 堆积告警的上次触发时刻（drain 频控用；
	// mu 保护：告警窗口 10 分钟内不重复）。
	backlogWarnedAt time.Time

	mu     sync.RWMutex
	status RunnerStatus
}

type scheduledDBTask struct {
	ready   bool
	input   Input
	state   CurrentState
	found   bool
	kind    string
	outcome Outcome
}

// directInputLoader permits the scheduler to load immutable, currently eligible
// inputs from the independently configured business read model.  It deliberately
// has no Node/Gateway client surface: signed request files only carry a trigger
// and fences; the effective probe input is read directly from the business
// PostgreSQL or SQLite store.  Signed-files input stays as the explicit
// fallback source (JUHE_AI_ACCOUNT_HEALTH_INPUT_SOURCE=files).
type directInputLoader interface {
	LoadDue(ctx context.Context, limit int) ([]Input, error)
	LoadAccount(ctx context.Context, accountID string) ([]Input, error)
}

type directInputFailureLoader interface {
	LoadDueWithFailures(ctx context.Context, limit int) (DirectInputLoadResult, error)
}

// directInputAccountFailureLoader 是单账户显式请求的隔离加载面（PG/SQLite
// 直读 reader 均实现）：构造失败的账户返回 Failures 而非错误，调用方按
// input_stale 收敛而不是中断整轮（BUG-0218）。
type directInputAccountFailureLoader interface {
	LoadAccountWithFailures(ctx context.Context, accountID string) (DirectInputLoadResult, error)
}

// DirectInputReader 是 PG 与 SQLite 直读适配器的公共装配面：组合根用它承载
// 两种输入源；SetSuppressionProvider 由 Runner 装配时注入 jobs 重试窗口。
type DirectInputReader interface {
	LoadDue(ctx context.Context, limit int) ([]Input, error)
	LoadDueWithFailures(ctx context.Context, limit int) (DirectInputLoadResult, error)
	LoadAccount(ctx context.Context, accountID string) ([]Input, error)
	SetSuppressionProvider(provider func(context.Context, time.Time) ([]DirectInputSuppression, error))
}

type RunnerStatus struct {
	OwnerHeld   bool
	LastScanAt  time.Time
	LastSuccess time.Time
	LastError   string
	Inputs      int
	Executed    int
}

const maxScheduleDuration = 365 * 24 * time.Hour
const maxScheduleMilliseconds = int64(maxScheduleDuration / time.Millisecond)
const cooldownLongTermInterval = time.Hour
const cooldownObservationTimeout = 7 * 24 * time.Hour
const cooldownLimitedProbeTimeout = 10 * time.Minute
const defaultCooldownMaxPauseMinutes = 2
const defaultCooldownMaxRecoveryHours = 12
const ownerLeaseRenewTimeout = 5 * time.Second

func NewRunner(cfg Config, store *Store, logger *slog.Logger) *Runner {
	if logger == nil {
		logger = slog.Default()
	}
	return &Runner{cfg: cfg, store: store, logger: logger}
}

func NewRunnerWithDirectInputReader(cfg Config, store *Store, logger *slog.Logger, reader DirectInputReader) *Runner {
	runner := NewRunner(cfg, store, logger)
	runner.directInputReader = reader
	if reader != nil && store != nil {
		reader.SetSuppressionProvider(store.LoadDirectInputSuppressions)
	}
	return runner
}

// SetOutcomeProjector 注入 outcome 投影器（BUG-0260：组合根在 J1 runner 与
// 投影面都就绪后回绑，与 SetProbeRequestDrain 同款模式；投影面装配失败降级
// 时不注入，终态补落地以错误显式暴露而非静默回退复测循环）。
func (r *Runner) SetOutcomeProjector(projector *OutcomeProjector) {
	r.terminalProjector = projector
}

func (r *Runner) Ready() bool {
	if r == nil {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.status.OwnerHeld && !r.status.LastSuccess.IsZero() && r.status.LastError == ""
}

func (r *Runner) Status() RunnerStatus {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.status
}

func (r *Runner) Run(ctx context.Context) error {
	if r == nil || r.store == nil {
		return errors.New("account-health runner 未初始化")
	}
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		lease, acquired, err := r.store.AcquireOwnerLease(ctx, r.cfg.InstanceID, r.cfg.OwnerLease)
		if err != nil {
			r.setError(err)
			if err := waitContext(ctx, schedulejitter.Delay(r.cfg.ScanInterval)); err != nil {
				return err
			}
			continue
		}
		if !acquired {
			if err := waitContext(ctx, schedulejitter.Delay(minDuration(r.cfg.ScanInterval, r.cfg.OwnerLease/3))); err != nil {
				return err
			}
			continue
		}
		if err := r.runOwned(ctx, lease); err != nil && !errors.Is(err, context.Canceled) {
			r.setError(err)
			r.logger.Warn("account-health owner lease released", "error", err)
		}
		if ctx.Err() == nil {
			if err := waitContext(ctx, schedulejitter.Delay(r.cfg.ScanInterval)); err != nil {
				return err
			}
		}
	}
}

func (r *Runner) runOwned(parent context.Context, lease OwnerLease) error {
	r.setOwnerHeld(true)
	defer func() {
		r.setOwnerHeld(false)
		releaseCtx, releaseCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer releaseCancel()
		if err := r.store.ReleaseOwnerLease(releaseCtx, lease); err != nil {
			r.logger.Warn("release account-health owner lease", "error", err)
		}
	}()
	ctx, cancel := context.WithCancelCause(parent)
	defer cancel(nil)
	renewEvery := maxDuration(3*time.Second, r.cfg.OwnerLease/3)
	renewTicker := time.NewTicker(renewEvery)
	scanTimer := time.NewTimer(schedulejitter.Delay(r.cfg.ScanInterval))
	defer renewTicker.Stop()
	defer scanTimer.Stop()

	// Keep ownership renewal independent from the synchronous probe batch. A
	// batch can legitimately outlive a renewal interval (for example, several
	// network probes at the configured worker concurrency).  There is exactly one cycle
	// goroutine at a time: on a lost/failed renewal we cancel it and wait for its
	// workers to return before releasing the fenced lease or allowing a later
	// owner to run.
	cycleDone := make(chan error, 1)
	cycleRunning := false
	startCycle := func() {
		cycleRunning = true
		go func() {
			defer safego.Handle("accounthealth.scheduler.cycle", func(recovered any) {
				cycleDone <- fmt.Errorf("账户健康扫描周期异常终止: %v", recovered)
			})
			cycleDone <- r.runCycle(ctx, lease)
		}()
	}
	waitCycle := func() {
		if !cycleRunning {
			return
		}
		<-cycleDone
		cycleRunning = false
	}
	stopCycle := func(cause error) error {
		cancel(cause)
		waitCycle()
		return cause
	}
	initialCycle := true
	startCycle()
	for {
		select {
		case <-ctx.Done():
			waitCycle()
			return context.Cause(ctx)
		case cycleErr := <-cycleDone:
			cycleRunning = false
			if ctx.Err() != nil {
				return context.Cause(ctx)
			}
			if errors.Is(cycleErr, ErrOwnerLeaseLost) {
				return stopCycle(ErrOwnerLeaseLost)
			}
			if cycleErr != nil {
				if initialCycle {
					return cycleErr
				}
				r.setError(cycleErr)
				r.logger.Error("account-health scan failed; owner stays alive for the next scan", "error", cycleErr)
			}
			initialCycle = false
		case <-renewTicker.C:
			renewCtx, renewCancel := context.WithTimeout(ctx, ownerLeaseRenewTimeout)
			renewed, err := r.store.RenewOwnerLease(renewCtx, lease, r.cfg.OwnerLease)
			renewCancel()
			if err != nil {
				if ctx.Err() != nil {
					return stopCycle(context.Cause(ctx))
				}
				return stopCycle(fmt.Errorf("续约 account-health owner lease: %w", err))
			}
			if !renewed {
				return stopCycle(ErrOwnerLeaseLost)
			}
		case <-scanTimer.C:
			if !cycleRunning {
				initialCycle = false
				startCycle()
			}
			scanTimer.Reset(schedulejitter.Delay(r.cfg.ScanInterval))
		}
	}
}

func (r *Runner) runCycle(ctx context.Context, lease OwnerLease) error {
	now := r.cfg.Now().UTC()
	r.setScan(now)
	// Outbox drain first: gateway-published probe requests (the removed
	// loopback dispatch bridge's replacement) must not wait behind this
	// cycle's scheduled batch — the drain keeps the old bridge's latency
	// shape (one scan interval between publish and probe).
	if err := r.drainProbeRequestOutbox(ctx, lease); err != nil {
		return err
	}
	var err error
	var inputs []Input
	if r.directInputReader != nil {
		if reader, ok := r.directInputReader.(directInputFailureLoader); ok {
			result, loadErr := reader.LoadDueWithFailures(ctx, r.cfg.DirectInputLimit)
			if loadErr != nil {
				return loadErr
			}
			for _, failure := range result.Failures {
				if err := r.persistDirectInputFailure(ctx, lease, failure, now); err != nil {
					return err
				}
			}
			inputs = result.Inputs
		} else {
			inputs, err = r.directInputReader.LoadDue(ctx, r.cfg.DirectInputLimit)
		}
		if err != nil {
			return err
		}
	} else {
		inputs, err = LoadSignedInputFiles(r.cfg.InputDirectory, r.cfg.InputKeys)
		if err != nil {
			return err
		}
	}
	requests, err := LoadSignedProbeRequests(r.cfg.InputDirectory, r.cfg.InputKeys)
	if err != nil {
		return err
	}
	// A PostgreSQL direct-input read freezes each candidate's issued_at after
	// this cycle starts. Refresh the due-time fence after every input/request
	// read so a newly read active candidate with zero jitter is eligible in
	// this cycle rather than perpetually appearing a few microseconds early.
	// This does not change the durable input fence or broaden any candidate.
	now = r.cfg.Now().UTC()
	r.setScan(now)
	inputsByAccount := make(map[string]Input, len(inputs))
	for _, input := range inputs {
		inputsByAccount[input.AccountID] = input
	}
	for _, request := range requests {
		if _, found := inputsByAccount[request.AccountID]; !found && r.directInputReader != nil {
			var explicitInputs []Input
			if accountLoader, ok := r.directInputReader.(directInputAccountFailureLoader); ok {
				result, loadErr := accountLoader.LoadAccountWithFailures(ctx, request.AccountID)
				if loadErr != nil {
					return loadErr
				}
				if len(result.Failures) > 0 {
					// 候选构造失败是确定性行损坏：保持零值 input 走
					// runExplicitRequest 的 input_stale 终态结算并消费该行，
					// 不得中断整轮（BUG-0218：单坏账户的显式请求把周期
					// inputs 的探测一并停摆 35 分钟）。
					r.logger.Warn("显式探活请求账户候选构造失败，按 input_stale 收敛",
						"event", "account_health_explicit_request_input_stale",
						"requestId", request.RequestID, "accountId", request.AccountID,
						"failureCount", len(result.Failures))
				}
				explicitInputs = result.Inputs
			} else {
				var loadErr error
				explicitInputs, loadErr = r.directInputReader.LoadAccount(ctx, request.AccountID)
				if loadErr != nil {
					return loadErr
				}
			}
			if len(explicitInputs) == 1 {
				inputsByAccount[request.AccountID] = explicitInputs[0]
			}
		}
		if err := r.runExplicitRequest(ctx, lease, inputsByAccount[request.AccountID], request, now); err != nil {
			return err
		}
		if err := r.removeConsumedRequest(ctx, request); err != nil {
			return err
		}
	}
	sort.Slice(inputs, func(left, right int) bool { return inputs[left].AccountID < inputs[right].AccountID })
	ioJobs := make(chan Input)
	dbQueueSize := r.cfg.DBQueueSize
	if dbQueueSize <= 0 {
		dbQueueSize = defaultDBQueueSize
	}
	dbQueue := make(chan scheduledDBTask, dbQueueSize)
	var ioWorkers, dbWorkers sync.WaitGroup
	var firstErr error
	var errMu sync.Mutex
	var executed atomic.Int64
	recordError := func(err error) {
		if err == nil {
			return
		}
		errMu.Lock()
		if firstErr == nil {
			firstErr = err
		}
		errMu.Unlock()
	}
	dbConcurrency := r.cfg.DBConcurrency
	if dbConcurrency <= 0 {
		dbConcurrency = defaultDBConcurrency
	}
	for range maxInt(1, minInt(dbConcurrency, len(inputs))) {
		dbWorkers.Add(1)
		go func() {
			defer safego.Recover("accounthealth.scheduler.dbWorker")
			defer dbWorkers.Done()
			for task := range dbQueue {
				if !task.ready {
					continue
				}
				started := time.Now()
				if err := r.settleScheduledTask(ctx, lease, &task); err != nil {
					recordError(err)
				} else {
					// This is a scan-attempt metric; durable outcome count remains in
					// the store and is never inferred from this in-memory value.
					executed.Add(1)
				}
				r.logger.Debug("account-health DB worker 完成", "phase", "db_write", "account_id", task.input.AccountID, "latency_ms", time.Since(started).Milliseconds(), "queue_depth", len(dbQueue))
			}
		}()
	}
	ioConcurrency := r.cfg.IOConcurrency
	if ioConcurrency <= 0 {
		ioConcurrency = r.cfg.MaxConcurrency
	}
	for range maxInt(1, minInt(ioConcurrency, len(inputs))) {
		ioWorkers.Add(1)
		go func() {
			defer safego.Recover("accounthealth.scheduler.ioWorker")
			defer ioWorkers.Done()
			for input := range ioJobs {
				task, err := r.prepareScheduledInput(ctx, lease, input, now)
				if err != nil {
					recordError(err)
					continue
				}
				if !task.ready {
					continue
				}
				queuedAt := time.Now()
				select {
				case <-ctx.Done():
					recordError(context.Cause(ctx))
				case dbQueue <- task:
					r.logger.Debug("account-health DB queue 入队", "phase", "db_queue", "account_id", input.AccountID, "queue_depth", len(dbQueue), "queue_wait_ms", time.Since(queuedAt).Milliseconds())
				}
			}
		}()
	}
	for _, input := range inputs {
		select {
		case <-ctx.Done():
			close(ioJobs)
			ioWorkers.Wait()
			close(dbQueue)
			dbWorkers.Wait()
			return context.Cause(ctx)
		case ioJobs <- input:
		}
	}
	close(ioJobs)
	ioWorkers.Wait()
	close(dbQueue)
	dbWorkers.Wait()
	errMu.Lock()
	err = firstErr
	errMu.Unlock()
	if err != nil {
		return err
	}
	r.setSuccess(len(inputs), int(executed.Load()))
	return nil
}

func (r *Runner) persistDirectInputFailure(ctx context.Context, lease OwnerLease, failure DirectInputFailure, observed time.Time) error {
	requestID := directInputFailureRequestID(failure, observed)
	nextDue := observed.Add(schedulejitter.Delay(directInputFailureRetryBackoff))
	outcome := Outcome{
		OutcomeID:        requestID,
		RequestID:        requestID,
		AccountID:        failure.AccountID,
		Outcome:          OutcomeTaskFailed,
		ObservedAt:       observed,
		InputVersion:     failure.InputVersion,
		ConfigRevision:   failure.ConfigRevision,
		DispatchRevision: failure.DispatchRevision,
		ErrorCode:        "direct_input_invalid",
		ErrorMessage:     "PG direct input 候选无效；已隔离该账户探活任务并延迟重试",
		NextDueAt:        &nextDue,
		FailureCount:     1,
		FailureStartedAt: &observed,
	}
	_, err := r.store.AppendOutcome(ctx, lease, outcome)
	return err
}

const directInputFailureRetryBackoff = 5 * time.Minute

func directInputFailureRequestID(failure DirectInputFailure, _ time.Time) string {
	value := fmt.Sprintf("direct-input-failure-v3\\x00%s\\x00%d\\x00%d\\x00%d", failure.AccountID, failure.InputVersion, failure.ConfigRevision, failure.DispatchRevision)
	sum := sha256.Sum256([]byte(value))
	return "direct-input-failure-" + hex.EncodeToString(sum[:])
}

func (r *Runner) removeConsumedRequest(ctx context.Context, request ProbeRequest) error {
	if strings.TrimSpace(request.sourcePath) == "" {
		return nil
	}
	completed, err := r.store.HasRequest(ctx, request.RequestID)
	if err != nil {
		return err
	}
	if !completed {
		return nil
	}
	if err := os.Remove(request.sourcePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("清理已消费 account-health request %q 失败: %w", filepath.Base(request.sourcePath), err)
	}
	return nil
}

func (r *Runner) runExplicitRequest(ctx context.Context, lease OwnerLease, input Input, request ProbeRequest, now time.Time) error {
	already, err := r.store.HasRequest(ctx, request.RequestID)
	if err != nil || already {
		return err
	}
	if input.AccountID == "" || input.InputVersion != request.InputVersion || input.ConfigRevision != request.ConfigRevision || input.DispatchRevision != request.DispatchRevision || !inputEligible(input) {
		return r.persistExplicitTerminal(ctx, lease, request, OutcomeStale, now, "input_stale", "request 对应的 input 已失效")
	}
	if requestDeadlineExpired(request, now) {
		return r.persistExplicitTerminal(ctx, lease, request, OutcomeTaskFailed, now, "request_deadline_elapsed", "探活请求已过期")
	}
	if err := validateScheduledInput(input, now); err != nil {
		return r.persistExplicitTerminal(ctx, lease, request, OutcomeTaskFailed, now, "input_invalid", err.Error())
	}
	initialState, initialFound, err := r.store.LoadCurrentState(ctx, input.AccountID)
	if err != nil {
		return err
	}
	mutationKind := ""
	if request.MutateAccount {
		var allowed bool
		mutationKind, allowed = explicitMutationKind(input, initialState, initialFound)
		if !allowed {
			return r.persistExplicitTerminal(ctx, lease, request, OutcomeStale, now, "account_status_not_probeable", "显式请求对应的账户状态不允许 health transition")
		}
	}
	probeCtx, cancel := context.WithDeadline(ctx, request.Deadline)
	outcome, err := ExecuteInputProbe(probeCtx, r.store, lease, input, request, ProbeOptions{Secret: r.cfg.CredentialSecret, Timeout: r.cfg.ProbeTimeout, MaxResponseBytes: r.cfg.MaxResponseBytes, Now: r.cfg.Now})
	cancel()
	if err != nil {
		return err
	}
	prior, found, err := r.store.LoadCurrentState(ctx, input.AccountID)
	if err != nil {
		return err
	}
	applyExplicitRequestDecision(&outcome, input, request, prior, found, mutationKind)
	if _, err := r.store.AppendOutcome(ctx, lease, outcome); err != nil {
		return err
	}
	r.recordProbeUsage(ctx, outcome, input, probeTrafficSourceForReason(request.Reason))
	return nil
}

func (r *Runner) persistExplicitTerminal(ctx context.Context, lease OwnerLease, request ProbeRequest, kind string, observed time.Time, code, message string) error {
	outcome := Outcome{OutcomeID: newOutcomeID(), RequestID: request.RequestID, AccountID: request.AccountID, Outcome: kind, ObservedAt: observed, InputVersion: request.InputVersion, ConfigRevision: request.ConfigRevision, DispatchRevision: request.DispatchRevision, ErrorCode: code, ErrorMessage: message, SourceFence: request.SourceFence, KeyModelFence: request.KeyModelFence}
	_, err := r.store.AppendOutcome(ctx, lease, outcome)
	return err
}

func preserveStateForSourceOnlyOutcome(outcome *Outcome, input Input, prior CurrentState, found bool) {
	if found && prior.InputVersion == input.InputVersion && prior.ConfigRevision == input.ConfigRevision && prior.DispatchRevision == input.DispatchRevision {
		copyStateToOutcome(outcome, prior)
		return
	}
	outcome.AccountStatus = input.Eligibility.AccountStatus
	if (outcome.AccountStatus == "temporary_unavailable" || outcome.AccountStatus == "rate_limited") && validCooldownFence(input.Cooldown, input) {
		outcome.CooldownFence = input.Cooldown
		outcome.NextDueAt = input.Eligibility.CooldownUntil
	}
}

func copyStateToOutcome(outcome *Outcome, prior CurrentState) {
	outcome.NextDueAt = prior.NextDueAt
	outcome.FailureCount = prior.FailureCount
	outcome.FailureStartedAt = prior.FailureStartedAt
	outcome.AccountStatus = prior.AccountStatus
	outcome.CooldownFence = prior.CooldownFence
}

func applyExplicitRequestDecision(outcome *Outcome, input Input, request ProbeRequest, prior CurrentState, found bool, mutationKind string) {
	if request.MutateAccount {
		decisionKind := mutationKind
		if request.Reason == "request_failure" && mutationKind == "health" {
			// A real request failure is the first business signal. Its dedicated
			// confirmation must not wait for the generic anti-flap threshold.
			decisionKind = "request_failure_health"
		}
		applyOutcomeDecision(outcome, input, prior, found, decisionKind)
		return
	}
	if request.SourceFence != nil && outcome.Outcome == OutcomeUpstreamFailed && sourceFenceHealthMutationAllowed(input, prior, found) {
		applyOutcomeDecision(outcome, input, prior, found, "source_health")
		return
	}
	preserveStateForSourceOnlyOutcome(outcome, input, prior, found)
}

// Explicit mutate-account requests are for activation/configuration work.
// If a current, matching state is cooling, it must use the cooldown state
// machine; an already-terminal state has no authority to emit a health
// transition that the Node projector would correctly reject.
func explicitMutationKind(input Input, prior CurrentState, found bool) (string, bool) {
	status := input.Eligibility.AccountStatus
	if found && prior.InputVersion == input.InputVersion && prior.ConfigRevision == input.ConfigRevision && prior.DispatchRevision == input.DispatchRevision && prior.AccountStatus != "" {
		status = prior.AccountStatus
	}
	switch status {
	case "active", "pending_test":
		return "health", true
	case "temporary_unavailable", "rate_limited":
		fence := input.Cooldown
		if found && prior.InputVersion == input.InputVersion && prior.ConfigRevision == input.ConfigRevision && prior.DispatchRevision == input.DispatchRevision && (input.Cooldown == nil || sameCooldownFence(prior.CooldownFence, input.Cooldown)) {
			fence = prior.CooldownFence
		}
		return "cooldown_retest", validCooldownFence(fence, input)
	default:
		return "", false
	}
}

func sourceFenceHealthMutationAllowed(input Input, prior CurrentState, found bool) bool {
	if !found || prior.InputVersion != input.InputVersion || prior.ConfigRevision != input.ConfigRevision || prior.DispatchRevision != input.DispatchRevision {
		return false
	}
	return prior.AccountStatus == "active" || prior.AccountStatus == "pending_test"
}

func (r *Runner) prepareScheduledInput(ctx context.Context, lease OwnerLease, input Input, now time.Time) (scheduledDBTask, error) {
	var task scheduledDBTask
	// A signed revoke/disable snapshot deliberately carries no credential or
	// protocol data. It immediately suppresses older durable state and makes no
	// upstream call; treating it as malformed would create noisy retries.
	if !inputEligible(input) {
		return task, nil
	}
	if err := validateScheduledInput(input, now); err != nil {
		return task, r.persistTaskFailure(ctx, lease, input, now, "input_invalid", err.Error())
	}
	state, found, err := r.store.LoadCurrentState(ctx, input.AccountID)
	if err != nil {
		return task, err
	}
	kind, due, ok := nextDue(input, state, found, now)
	if !ok || due.After(now) {
		return task, nil
	}
	if kind == cooldownTerminalReprojectKind {
		return r.prepareCooldownTerminalReproject(ctx, input, state, now)
	}
	request := ProbeRequest{
		RequestID:        scheduledRequestID(input, kind, due),
		AccountID:        input.AccountID,
		Reason:           kind,
		InputVersion:     input.InputVersion,
		ConfigRevision:   input.ConfigRevision,
		DispatchRevision: input.DispatchRevision,
		Deadline:         now.Add(r.cfg.ProbeTimeout),
	}
	already, err := r.store.HasRequest(ctx, request.RequestID)
	if err != nil {
		return task, err
	}
	if already {
		return task, nil
	}
	probeCtx, cancel := context.WithTimeout(ctx, r.cfg.ProbeTimeout)
	outcome, err := ExecuteInputProbe(probeCtx, r.store, lease, input, request, ProbeOptions{
		Secret:           r.cfg.CredentialSecret,
		Timeout:          r.cfg.ProbeTimeout,
		MaxResponseBytes: r.cfg.MaxResponseBytes,
		Now:              r.cfg.Now,
	})
	cancel()
	if err != nil {
		return task, err
	}
	task.input, task.state, task.found, task.kind, task.outcome = input, state, found, kind, outcome
	task.ready = true
	return task, nil
}

func (r *Runner) applyScheduledOutcome(outcome *Outcome, input Input, state CurrentState, found bool, kind string) {
	decisionKind := kind
	if kind == "health" {
		decisionKind = "scheduled_health"
	}
	applyOutcomeDecision(outcome, input, state, found, decisionKind)
}

// cooldownTerminalReprojectKind 是 nextDue 对「jobs current_state 为 error 终态
// × business 行仍 temporary_unavailable/rate_limited」脑裂形态返回的修复 kind
// （BUG-0260）：不发复测探针，改为补落地丢失的 cooldown_error 终态投影。
const cooldownTerminalReprojectKind = "cooldown_terminal_reproject"

// prepareCooldownTerminalReproject 为终态脑裂形态构造不发探针的补投影任务
// （BUG-0260）。幂等：request ID 只由账户 + input epoch + kind 派生（不含
// 时钟），同一 input_version 内至多尝试一次；已尝试过的 epoch 直接跳过。
func (r *Runner) prepareCooldownTerminalReproject(ctx context.Context, input Input, state CurrentState, now time.Time) (scheduledDBTask, error) {
	var task scheduledDBTask
	requestID := cooldownTerminalReprojectRequestID(input)
	already, err := r.store.HasRequest(ctx, requestID)
	if err != nil {
		return task, err
	}
	if already {
		return task, nil
	}
	task.input, task.state, task.found = input, state, true
	task.kind = cooldownTerminalReprojectKind
	task.outcome = cooldownTerminalReprojectOutcome(input, state, requestID, now)
	task.ready = true
	return task, nil
}

// cooldownTerminalReprojectOutcome 重建丢失的 cooldown_error 终态投影
// （BUG-0260）：写入集对齐 applyCooldownDecision 观察期超时分支的投影形状；
// error_code/error_message/StatusCode/FailureCount 从 jobs state 行透传（保留
// 原终态审计语义，不新造判定）；ExpectedAccountStatus/ExpectedCooldownFence 与
// CooldownFence 取 business 现存状态与 fence（input 快照），业务库 CAS 守卫
// 才能命中。Outcome 取 upstream_failure 以满足投影契约
// outcomeMatchesTransition 对 cooldown_error 的要求。
func cooldownTerminalReprojectOutcome(input Input, state CurrentState, requestID string, observed time.Time) Outcome {
	return Outcome{
		OutcomeID:        newOutcomeID(),
		RequestID:        requestID,
		AccountID:        input.AccountID,
		Outcome:          OutcomeUpstreamFailed,
		ObservedAt:       observed,
		InputVersion:     input.InputVersion,
		ConfigRevision:   input.ConfigRevision,
		DispatchRevision: input.DispatchRevision,
		StatusCode:       state.StatusCode,
		ErrorCode:        state.ErrorCode,
		ErrorMessage:     state.ErrorMessage,
		AccountStatus:    "error",
		FailureCount:     state.FailureCount,
		CooldownFence:    input.Cooldown,
		Projection: &Projection{
			TargetAccountID:       input.AccountID,
			TransitionKind:        "cooldown_error",
			InputVersion:          input.InputVersion,
			ConfigRevision:        input.ConfigRevision,
			DispatchRevision:      input.DispatchRevision,
			SourceRevision:        input.Eligibility.SourceConfigRevision,
			ExpectedAccountStatus: input.Eligibility.AccountStatus,
			ExpectedCooldownFence: input.Cooldown,
			CooldownFence:         input.Cooldown,
		},
	}
}

// applyCooldownTerminalReproject 补落地终态投影并落 audit 行（BUG-0260）。
// 顺序契约：先经投影事务入口（与 drain 消费同源）落地 business，
// applied/stale/rejected 均落 receipt（幂等）；投影瞬时错误直接返回 err 进入
// 下轮重试，此时不写 outcome 行，避免把未投影的尝试记为已处理。投影结论
// 落定后才追加幂等 audit outcome 行。jobs current_state 不改写：error 终态
// 本就是正确状态，且 AppendOutcome 对该形态的 state CAS 永不命中、epoch
// 相同不推进，state 行保持原样。
func (r *Runner) applyCooldownTerminalReproject(ctx context.Context, lease OwnerLease, outcome Outcome) error {
	if r.terminalProjector == nil {
		return fmt.Errorf("补落地 J1 冷却终态投影失败（account=%s）：outcome 投影器未装配", outcome.AccountID)
	}
	result, err := r.terminalProjector.ProjectOutcomeNow(ctx, outcome)
	if err != nil {
		return fmt.Errorf("补落地 J1 冷却终态投影失败（account=%s）: %w", outcome.AccountID, err)
	}
	r.logger.Info("J1 冷却终态投影补落地完成",
		"event", "account_health_cooldown_terminal_reproject",
		"accountId", outcome.AccountID,
		"outcomeId", outcome.OutcomeID,
		"disposition", string(result.Disposition),
		"reason", result.Reason)
	if _, err := r.store.AppendOutcome(ctx, lease, outcome); err != nil {
		return fmt.Errorf("落库 J1 冷却终态补投影 audit outcome 失败（account=%s）: %w", outcome.AccountID, err)
	}
	return nil
}

// cooldownTerminalReprojectRequestID 派生终态补投影的幂等 request ID：仅由
// 账户 + input epoch + kind 构成（不含时钟），保证同一 input_version 内同
// kind 至多尝试一次（BUG-0260 防放大下界）。
func cooldownTerminalReprojectRequestID(input Input) string {
	value := sha256.Sum256([]byte(strings.Join([]string{
		input.AccountID,
		fmt.Sprintf("%d", input.InputVersion),
		fmt.Sprintf("%d", input.ConfigRevision),
		fmt.Sprintf("%d", input.DispatchRevision),
		cooldownTerminalReprojectKind,
	}, "\n")))
	return "account-health-" + hex.EncodeToString(value[:])
}

// settleScheduledTask 是周期批处理与单账户 runInput 共用的任务结算入口：
// 普通 kind 走决策应用 + AppendOutcome + 使用记录补记；终态补投影 kind
// （BUG-0260）不发探针、不补使用记录，改走投影直调通道。
func (r *Runner) settleScheduledTask(ctx context.Context, lease OwnerLease, task *scheduledDBTask) error {
	if task.kind == cooldownTerminalReprojectKind {
		return r.applyCooldownTerminalReproject(ctx, lease, task.outcome)
	}
	r.applyScheduledOutcome(&task.outcome, task.input, task.state, task.found, task.kind)
	if _, err := r.store.AppendOutcome(ctx, lease, task.outcome); err != nil {
		return err
	}
	r.recordProbeUsage(ctx, task.outcome, task.input, probeTrafficSourceForKind(task.kind))
	return nil
}

func (r *Runner) runInput(ctx context.Context, lease OwnerLease, input Input, now time.Time) error {
	task, err := r.prepareScheduledInput(ctx, lease, input, now)
	if err != nil {
		return err
	}
	if !task.ready {
		return nil
	}
	return r.settleScheduledTask(ctx, lease, &task)
}

func (r *Runner) persistTaskFailure(ctx context.Context, lease OwnerLease, input Input, observed time.Time, code, message string) error {
	// Invalid input is a deterministic failure of this immutable input fence,
	// not a new scheduled attempt.  The observed clock is deliberately omitted
	// so repeated scans cannot append one durable failure per scan.
	requestID := invalidInputRequestID(input)
	already, err := r.store.HasRequest(ctx, requestID)
	if err != nil || already {
		return err
	}
	outcome := Outcome{
		OutcomeID:        newOutcomeID(),
		RequestID:        requestID,
		AccountID:        input.AccountID,
		Outcome:          OutcomeTaskFailed,
		ObservedAt:       observed,
		InputVersion:     input.InputVersion,
		ConfigRevision:   input.ConfigRevision,
		DispatchRevision: input.DispatchRevision,
		ErrorCode:        code,
		ErrorMessage:     message,
	}
	_, err = r.store.AppendOutcome(ctx, lease, outcome)
	return err
}

func nextDue(input Input, state CurrentState, found bool, now time.Time) (kind string, due time.Time, ok bool) {
	if !found || state.InputVersion != input.InputVersion || state.ConfigRevision != input.ConfigRevision || state.DispatchRevision != input.DispatchRevision {
		if input.Eligibility.AccountStatus == "temporary_unavailable" || input.Eligibility.AccountStatus == "rate_limited" {
			if !validCooldownFence(input.Cooldown, input) || input.Eligibility.CooldownUntil == nil {
				return "", time.Time{}, false
			}
			return "cooldown_retest", *input.Eligibility.CooldownUntil, true
		}
		if input.Eligibility.AccountStatus == "pending_test" {
			return "health", input.IssuedAt, true
		}
		return "health", input.IssuedAt, true
	}
	// A quarantined direct-input candidate records only retry metadata. Once
	// the business row is repaired, that jobs state intentionally has no
	// account status or cooldown fence; resume from the current business input
	// as a cooldown retest instead of misclassifying it as ordinary health.
	if state.AccountStatus == "" && (input.Eligibility.AccountStatus == "temporary_unavailable" || input.Eligibility.AccountStatus == "rate_limited") {
		if !validCooldownFence(input.Cooldown, input) || input.Eligibility.CooldownUntil == nil {
			return "", time.Time{}, false
		}
		if state.NextDueAt != nil {
			return "cooldown_retest", *state.NextDueAt, true
		}
		return "cooldown_retest", *input.Eligibility.CooldownUntil, true
	}
	// The business account row is the source of truth for the current
	// eligibility epoch. A previous projector may have advanced jobs
	// current_state to active while its business-side recovery CAS was stale;
	// treating this split-brain row as ordinary health work emits
	// health_success with expectedAccountStatus=active and can never repair the
	// still-temporary_unavailable business row. Resume bounded cooldown
	// recovery from the business fence so a successful probe can reconcile it.
	if (input.Eligibility.AccountStatus == "temporary_unavailable" || input.Eligibility.AccountStatus == "rate_limited") &&
		state.AccountStatus != input.Eligibility.AccountStatus {
		if !validCooldownFence(input.Cooldown, input) || input.Eligibility.CooldownUntil == nil {
			return "", time.Time{}, false
		}
		// BUG-0260：jobs current_state 已是 error 终态而 business 行仍冷却，是
		// 终态投影丢失的脑裂形态。若按普通 reconciliation 放行复测，复测 outcome
		// 的 state CAS（updateCooldownCurrentStateTx 要求 account_status=expected
		// 且 epoch 相同不推进）永不命中，jobs state 永卡终态，形成每个扫描周期
		// 一发探针的死循环（生产 21283 次/约两天）。error 是终态语义，不允许被
		// 复测推翻：改走终态投影补落地，不再发探针。
		if state.AccountStatus == "error" {
			return cooldownTerminalReprojectKind, reconciliationDue(*input.Eligibility.CooldownUntil, now), true
		}
		// A due time in the past may already identify an earlier settled request.
		// Use this reconciliation cycle as the idempotency epoch so stale jobs
		// state cannot suppress the repair forever, while preserving a cooldown
		// that has not expired yet.
		return "cooldown_retest", reconciliationDue(*input.Eligibility.CooldownUntil, now), true
	}
	// The business account row is authoritative for the current eligibility
	// epoch. A manual recovery (or a legacy Node projector) can advance the
	// business row back to active while jobs current_state still carries a
	// cooldown/error status. Do not keep emitting cooldown_success with an
	// active expected status (which the projection contract must reject); run
	// one ordinary health decision so the jobs state can be reconciled and the
	// normal health projection can advance the active epoch.
	if input.Eligibility.AccountStatus == "active" && state.AccountStatus != "" && state.AccountStatus != "active" {
		return "health", input.IssuedAt, true
	}
	if state.NextDueAt == nil {
		return "health", now, true
	}
	if state.AccountStatus == "temporary_unavailable" || state.AccountStatus == "rate_limited" {
		if !sameCooldownFence(state.CooldownFence, input.Cooldown) {
			if !validCooldownFence(input.Cooldown, input) || input.Eligibility.CooldownUntil == nil {
				return "", time.Time{}, false
			}
			// A changed business fence can retain the original cooldown_until after
			// an earlier request with that deterministic ID has already settled.
			// Give an overdue reconciliation probe a fresh ID while preserving an
			// unexpired cooldown and fencing the outcome by the current generation.
			return "cooldown_retest", reconciliationDue(*input.Eligibility.CooldownUntil, now), true
		}
		if !validCooldownFence(state.CooldownFence, input) {
			return "", time.Time{}, false
		}
		return "cooldown_retest", *state.NextDueAt, true
	}
	if state.AccountStatus == "error" {
		return "", time.Time{}, false
	}
	return "health", *state.NextDueAt, true
}

func applyOutcomeDecision(outcome *Outcome, input Input, prior CurrentState, priorFound bool, kind string) {
	observed := outcome.ObservedAt.UTC()
	if !matchingCurrentStateInput(prior, priorFound, input) {
		// A new input/config/dispatch epoch has no authority to inherit the
		// previous epoch's counters, window or cooldown generation.
		prior = CurrentState{}
		priorFound = false
	}
	priorStatus := input.Eligibility.AccountStatus
	if priorFound && prior.AccountStatus != "" && prior.AccountStatus == input.Eligibility.AccountStatus {
		priorStatus = prior.AccountStatus
	}
	if kind == "cooldown_retest" {
		applyCooldownDecision(outcome, input, prior, priorFound, priorStatus, observed)
		return
	}
	applyHealthDecision(outcome, input, prior, priorStatus, kind, observed)
}

func matchingCurrentStateInput(prior CurrentState, priorFound bool, input Input) bool {
	return priorFound && prior.InputVersion == input.InputVersion && prior.ConfigRevision == input.ConfigRevision && prior.DispatchRevision == input.DispatchRevision
}

func applyHealthDecision(outcome *Outcome, input Input, prior CurrentState, priorStatus, kind string, observed time.Time) {
	interval := durationMS(input.Schedule.HealthIntervalMS, time.Hour)
	retry := durationMS(input.Schedule.FailureRetryMS, 5*time.Minute)
	switch outcome.Outcome {
	case OutcomeSuccess:
		next := observed.Add(schedulejitter.Delay(interval))
		outcome.NextDueAt = &next
		outcome.FailureCount = 0
		outcome.AccountStatus = "active"
		transition := "health_success"
		if priorStatus == "pending_test" {
			transition = "activation_success"
		}
		outcome.Projection = &Projection{TargetAccountID: input.AccountID, TransitionKind: transition, InputVersion: input.InputVersion, ConfigRevision: input.ConfigRevision, DispatchRevision: input.DispatchRevision, SourceRevision: input.Eligibility.SourceConfigRevision, ExpectedAccountStatus: priorStatus, Values: map[string]any{"last_health_check_at": observed.Format(time.RFC3339Nano), "last_health_success_at": observed.Format(time.RFC3339Nano), "last_health_check_status_code": outcome.StatusCode}}
	case OutcomeNeutral, OutcomeUpstreamFailed:
		failures := prior.FailureCount + 1
		outcome.FailureCount = failures
		started := prior.FailureStartedAt
		if started == nil {
			started = &observed
		}
		outcome.FailureStartedAt = started
		if priorStatus == "pending_test" && observed.Sub(*started) >= 24*time.Hour {
			outcome.AccountStatus = "error"
			outcome.ErrorCode = "account_activation_check_timeout"
			outcome.ErrorMessage = "待检查账户自首次独立失败起持续 24 小时仍未通过探活"
			outcome.Projection = healthProjection(input, "activation_error", priorStatus, nil, observed, outcome, failures)
			return
		}
		immediateCooldown := (kind == "scheduled_health" || kind == "source_health" || kind == "request_failure_health")
		if priorStatus == "active" && (immediateCooldown || failures >= input.Schedule.FailureThreshold) {
			outcome.AccountStatus = "temporary_unavailable"
			// The health threshold count belongs to the health window.  The
			// cooldown retry sequence starts separately at zero, so its first
			// upstream failure is scheduled with the frozen initial backoff.
			outcome.FailureCount = 0
			generation := newOutcomeID()
			next := observed.Add(schedulejitter.Delay(durationMS(input.Schedule.CooldownFailureBackoffMS, 3*time.Second)))
			outcome.NextDueAt = &next
			outcome.Projection = healthProjection(input, "temporary_unavailable", priorStatus, nil, observed, outcome, failures)
			outcome.CooldownFence = &CooldownFence{ObservationStartedAt: observed, Generation: generation, SourceConfigRevision: input.Eligibility.SourceConfigRevision}
			outcome.Projection.CooldownFence = outcome.CooldownFence
			return
		}
		next := observed.Add(schedulejitter.Delay(retry))
		outcome.NextDueAt = &next
		outcome.AccountStatus = priorStatus
		outcome.Projection = healthProjection(input, "health_failure", priorStatus, nil, observed, outcome, failures)
	default:
		next := observed.Add(schedulejitter.Delay(retry))
		outcome.NextDueAt = &next
		outcome.FailureCount = prior.FailureCount
		outcome.FailureStartedAt = prior.FailureStartedAt
		outcome.AccountStatus = priorStatus
	}
}

func applyCooldownDecision(outcome *Outcome, input Input, prior CurrentState, priorFound bool, expectedStatus string, observed time.Time) {
	fence := input.Cooldown
	if priorFound && prior.InputVersion == input.InputVersion && prior.ConfigRevision == input.ConfigRevision && prior.DispatchRevision == input.DispatchRevision && (input.Cooldown == nil || sameCooldownFence(prior.CooldownFence, input.Cooldown)) {
		fence = prior.CooldownFence
	}
	if !validCooldownFence(fence, input) {
		outcome.Outcome = OutcomeTaskFailed
		outcome.ErrorCode = "cooldown_fence_invalid"
		outcome.ErrorMessage = "冷却复测缺少或不匹配五元 fence"
		outcome.AccountStatus = input.Eligibility.AccountStatus
		return
	}
	base := durationMS(input.Schedule.CooldownNeutralBaseMS, 30*time.Second)
	maxDelay := durationMS(input.Schedule.CooldownNeutralMaxMS, 15*time.Minute)
	initialBackoff := durationMS(input.Schedule.CooldownFailureBackoffMS, 3*time.Second)
	switch outcome.Outcome {
	case OutcomeSuccess:
		next := observed.Add(schedulejitter.Delay(durationMS(input.Schedule.HealthIntervalMS, time.Hour)))
		outcome.NextDueAt = &next
		outcome.FailureCount = 0
		outcome.AccountStatus = "active"
		outcome.Projection = &Projection{TargetAccountID: input.AccountID, TransitionKind: "cooldown_success", InputVersion: input.InputVersion, ConfigRevision: input.ConfigRevision, DispatchRevision: input.DispatchRevision, SourceRevision: input.Eligibility.SourceConfigRevision, ExpectedAccountStatus: expectedStatus, ExpectedCooldownFence: fence, Values: map[string]any{"last_health_check_at": observed.Format(time.RFC3339Nano), "last_health_success_at": observed.Format(time.RFC3339Nano), "last_health_check_status_code": outcome.StatusCode}}
	case OutcomeNeutral, OutcomeTaskFailed:
		growthStep := cooldownDeferGrowthStep(fence, observed, base)
		delay := schedulejitter.Delay(cooldownDefer(growthStep, base, maxDelay))
		next := observed.Add(delay)
		outcome.NextDueAt = &next
		outcome.FailureCount = prior.FailureCount
		outcome.FailureStartedAt = prior.FailureStartedAt
		outcome.AccountStatus = expectedStatus
		outcome.CooldownFence = fence
		outcome.Projection = &Projection{TargetAccountID: input.AccountID, TransitionKind: "cooldown_defer", InputVersion: input.InputVersion, ConfigRevision: input.ConfigRevision, DispatchRevision: input.DispatchRevision, SourceRevision: input.Eligibility.SourceConfigRevision, ExpectedAccountStatus: expectedStatus, ExpectedCooldownFence: fence, CooldownFence: fence}
	case OutcomeUpstreamFailed:
		failures := prior.FailureCount + 1
		outcome.FailureCount = failures
		outcome.FailureStartedAt = prior.FailureStartedAt
		elapsed := observed.Sub(fence.ObservationStartedAt)
		_, limitedTemporaryUnavailable := boundedCooldownRemaining(input, expectedStatus, fence, observed)
		if limitedTemporaryUnavailable && elapsed >= cooldownLimitedProbeTimeout {
			outcome.AccountStatus = "error"
			outcome.ErrorCode = "cooldown_retest_limited_probe_timeout"
			outcome.ErrorMessage = "冷却复测有界观察期已超过 10 分钟"
			outcome.CooldownFence = fence
			outcome.Projection = &Projection{TargetAccountID: input.AccountID, TransitionKind: "cooldown_error", InputVersion: input.InputVersion, ConfigRevision: input.ConfigRevision, DispatchRevision: input.DispatchRevision, SourceRevision: input.Eligibility.SourceConfigRevision, ExpectedAccountStatus: expectedStatus, ExpectedCooldownFence: fence, CooldownFence: fence}
			return
		}
		if elapsed >= cooldownObservationTimeout {
			outcome.AccountStatus = "error"
			outcome.ErrorCode = "cooldown_retest_observation_timeout"
			outcome.ErrorMessage = "冷却复测观察期已超过 7 天"
			outcome.CooldownFence = fence
			outcome.Projection = &Projection{TargetAccountID: input.AccountID, TransitionKind: "cooldown_error", InputVersion: input.InputVersion, ConfigRevision: input.ConfigRevision, DispatchRevision: input.DispatchRevision, SourceRevision: input.Eligibility.SourceConfigRevision, ExpectedAccountStatus: expectedStatus, ExpectedCooldownFence: fence, CooldownFence: fence}
			return
		}
		if elapsed >= cooldownMaxRecovery(input.Schedule) {
			next := observed.Add(schedulejitter.Delay(cooldownLongTermInterval))
			outcome.NextDueAt = &next
			outcome.AccountStatus = expectedStatus
			outcome.ErrorCode = "cooldown_retest_long_term_unavailable"
			outcome.CooldownFence = fence
			outcome.Projection = &Projection{TargetAccountID: input.AccountID, TransitionKind: "cooldown_failure", InputVersion: input.InputVersion, ConfigRevision: input.ConfigRevision, DispatchRevision: input.DispatchRevision, SourceRevision: input.Eligibility.SourceConfigRevision, ExpectedAccountStatus: expectedStatus, ExpectedCooldownFence: fence, CooldownFence: fence}
			return
		}
		delay := schedulejitter.Delay(cooldownFailureDelay(input.AccountID, fence.Generation, initialBackoff, failures))
		if remaining, bounded := boundedCooldownRemaining(input, expectedStatus, fence, observed); bounded && delay > remaining {
			delay = passiveDelayBefore(remaining)
		}
		next := observed.Add(delay)
		outcome.NextDueAt = &next
		outcome.AccountStatus = expectedStatus
		outcome.CooldownFence = fence
		outcome.Projection = &Projection{TargetAccountID: input.AccountID, TransitionKind: "cooldown_failure", InputVersion: input.InputVersion, ConfigRevision: input.ConfigRevision, DispatchRevision: input.DispatchRevision, SourceRevision: input.Eligibility.SourceConfigRevision, ExpectedAccountStatus: expectedStatus, ExpectedCooldownFence: fence, CooldownFence: fence}
	default:
		outcome.AccountStatus = prior.AccountStatus
	}
}

func healthProjection(input Input, transition, expectedStatus string, expectedFence *CooldownFence, observed time.Time, outcome *Outcome, failures int) *Projection {
	return &Projection{TargetAccountID: input.AccountID, TransitionKind: transition, InputVersion: input.InputVersion, ConfigRevision: input.ConfigRevision, DispatchRevision: input.DispatchRevision, SourceRevision: input.Eligibility.SourceConfigRevision, ExpectedAccountStatus: expectedStatus, ExpectedCooldownFence: expectedFence, Values: map[string]any{"last_health_check_at": observed.Format(time.RFC3339Nano), "last_health_check_status_code": outcome.StatusCode, "last_health_check_error_code": outcome.ErrorCode, "last_health_check_error_message": outcome.ErrorMessage, "health_check_failure_count": failures}}
}

func validateScheduledInput(input Input, now time.Time) error {
	if strings.TrimSpace(input.AccountID) == "" || input.InputVersion < 1 || input.ConfigRevision < 1 || input.DispatchRevision < 1 {
		return errors.New("input version 或账户 fence 无效")
	}
	if input.IssuedAt.IsZero() || input.ExpiresAt.IsZero() || !input.ExpiresAt.After(now) {
		return errors.New("input 已过期或缺少时间 fence")
	}
	if input.Schedule.HealthIntervalMS < 60_000 || input.Schedule.HealthIntervalMS > maxScheduleMilliseconds || input.Schedule.HealthJitterMS < 0 || input.Schedule.HealthJitterMS > maxScheduleMilliseconds || input.Schedule.HealthJitterMS > input.Schedule.HealthIntervalMS || input.Schedule.FailureThreshold < 1 || input.Schedule.FailureRetryMS < 3_000 || input.Schedule.FailureRetryMS > maxScheduleMilliseconds || input.Schedule.CooldownNeutralBaseMS < 0 || input.Schedule.CooldownNeutralBaseMS > maxScheduleMilliseconds || input.Schedule.CooldownNeutralMaxMS < 0 || input.Schedule.CooldownNeutralMaxMS > maxScheduleMilliseconds || input.Schedule.CooldownFailureBackoffMS < 0 || input.Schedule.CooldownFailureBackoffMS > maxScheduleMilliseconds || input.Schedule.MaxPauseMinutes < 0 || input.Schedule.MaxPauseMinutes > 1440 || input.Schedule.MaxRecoveryHours < 0 || input.Schedule.MaxRecoveryHours > 24*30 {
		return errors.New("input schedule 无效")
	}
	if !input.Eligibility.BoundGroup || !input.Eligibility.AuthorizationEligible {
		return errors.New("input eligibility 缺少绑定或授权证据")
	}
	if input.Eligibility.AccountStatus != "active" && input.Eligibility.AccountStatus != "pending_test" && input.Eligibility.AccountStatus != "temporary_unavailable" && input.Eligibility.AccountStatus != "rate_limited" {
		return errors.New("input account status 不可调度")
	}
	if input.Eligibility.AccountStatus == "temporary_unavailable" || input.Eligibility.AccountStatus == "rate_limited" {
		if input.Eligibility.CooldownUntil == nil || input.Eligibility.CooldownUntil.IsZero() {
			return errors.New("cooldown input 缺少 cooldown_until")
		}
		if !validCooldownFence(input.Cooldown, input) {
			return errors.New("cooldown input 缺少或不匹配五元 fence")
		}
	}
	return nil
}

func cooldownMaxPause(schedule Schedule) time.Duration {
	minutes := schedule.MaxPauseMinutes
	if minutes == 0 {
		minutes = defaultCooldownMaxPauseMinutes
	}
	return time.Duration(minutes) * time.Minute
}

func cooldownMaxRecovery(schedule Schedule) time.Duration {
	hours := schedule.MaxRecoveryHours
	if hours == 0 {
		hours = defaultCooldownMaxRecoveryHours
	}
	return time.Duration(hours) * time.Hour
}

func cooldownFailureDelay(accountID, generation string, initial time.Duration, failures int) time.Duration {
	if initial <= 0 {
		initial = 3 * time.Second
	}
	if failures < 1 {
		failures = 1
	}
	if failures > 5 {
		return cooldownSlowRetryDelay()
	}
	delay := initial
	for step := 1; step < failures; step++ {
		delay *= 2
	}
	return delay
}

func cooldownSlowRetryDelay() time.Duration {
	// Keep the policy base deterministic only; the caller applies the global
	// passive jitter once per retry so every round gets a fresh offset.
	return 60 * time.Second
}

func boundedCooldownRemaining(input Input, expectedStatus string, fence *CooldownFence, observed time.Time) (time.Duration, bool) {
	if expectedStatus != "temporary_unavailable" || input.Eligibility.TemporaryUnavailableContinuousProbeEnabled == nil || *input.Eligibility.TemporaryUnavailableContinuousProbeEnabled || fence == nil {
		return 0, false
	}
	remaining := cooldownLimitedProbeTimeout - observed.Sub(fence.ObservationStartedAt)
	if remaining < 0 {
		remaining = 0
	}
	return remaining, true
}

func validCooldownFence(fence *CooldownFence, input Input) bool {
	if fence == nil || fence.ObservationStartedAt.IsZero() || strings.TrimSpace(fence.Generation) == "" {
		return false
	}
	if input.Eligibility.SourceConfigRevision == nil {
		return fence.SourceConfigRevision == nil
	}
	return fence.SourceConfigRevision != nil && *fence.SourceConfigRevision == *input.Eligibility.SourceConfigRevision
}

func inputEligible(input Input) bool {
	// pending_test is the activation probe state and cooldown states are the
	// recovery probe path; both may legitimately carry schedulable=false.
	return (input.Eligibility.Schedulable || input.Eligibility.AccountStatus == "pending_test" || input.Eligibility.AccountStatus == "temporary_unavailable" || input.Eligibility.AccountStatus == "rate_limited") &&
		input.Eligibility.BoundGroup &&
		input.Eligibility.AuthorizationEligible
}

func scheduledRequestID(input Input, kind string, due time.Time) string {
	value := sha256.Sum256([]byte(strings.Join([]string{input.AccountID, fmt.Sprintf("%d", input.InputVersion), fmt.Sprintf("%d", input.ConfigRevision), fmt.Sprintf("%d", input.DispatchRevision), kind, due.UTC().Format(time.RFC3339Nano)}, "\n")))
	return "account-health-" + hex.EncodeToString(value[:])
}

func reconciliationDue(cooldownUntil, now time.Time) time.Time {
	cooldownUntil = cooldownUntil.UTC()
	if cooldownUntil.After(now) {
		return cooldownUntil
	}
	return now
}

func invalidInputRequestID(input Input) string {
	value := sha256.Sum256([]byte(strings.Join([]string{
		input.AccountID,
		fmt.Sprintf("%d", input.InputVersion),
		fmt.Sprintf("%d", input.ConfigRevision),
		fmt.Sprintf("%d", input.DispatchRevision),
		"invalid_input",
	}, "\n")))
	return "account-health-" + hex.EncodeToString(value[:])
}

func cooldownDeferGrowthStep(fence *CooldownFence, observed time.Time, base time.Duration) int {
	if fence == nil || fence.ObservationStartedAt.IsZero() || base <= 0 || observed.Before(fence.ObservationStartedAt) {
		return 0
	}
	return int(observed.Sub(fence.ObservationStartedAt) / base)
}

func cooldownDefer(growthStep int, base, maximum time.Duration) time.Duration {
	const minimum = 3 * time.Second
	if growthStep < 0 {
		growthStep = 0
	}
	if base < minimum {
		base = minimum
	}
	if base > maxScheduleDuration {
		base = maxScheduleDuration
	}
	if maximum < minimum {
		maximum = minimum
	}
	if maximum > maxScheduleDuration {
		maximum = maxScheduleDuration
	}
	if base > maximum {
		base = maximum
	}
	delay := base
	for step := 0; step < growthStep && delay < maximum; step++ {
		if delay > maximum/2 {
			delay = maximum
			break
		}
		delay *= 2
	}
	if delay > maximum {
		delay = maximum
	}
	// The returned value is the unjittered policy delay. The caller applies the
	// global passive schedule jitter exactly once so every retry gets a fresh
	// bounded offset instead of stacking a stable account-specific offset.
	return delay
}

func durationMS(value int64, fallback time.Duration) time.Duration {
	if value <= 0 {
		return fallback
	}
	if value > maxScheduleMilliseconds {
		return maxScheduleDuration
	}
	return time.Duration(value) * time.Millisecond
}

func ptrTime(value time.Time) *time.Time { return &value }

func (r *Runner) setOwnerHeld(value bool) {
	r.mu.Lock()
	r.status.OwnerHeld = value
	r.mu.Unlock()
}

func (r *Runner) setScan(value time.Time) {
	r.mu.Lock()
	r.status.LastScanAt = value
	r.mu.Unlock()
}

func (r *Runner) setSuccess(inputs, executed int) {
	r.mu.Lock()
	r.status.LastSuccess = r.cfg.Now().UTC()
	r.status.LastError = ""
	r.status.Inputs = inputs
	r.status.Executed += executed
	r.mu.Unlock()
}

func (r *Runner) setError(err error) {
	r.mu.Lock()
	r.status.LastError = err.Error()
	r.mu.Unlock()
}

func waitContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func minDuration(left, right time.Duration) time.Duration {
	if left < right {
		return left
	}
	return right
}

// passiveDelayBefore keeps a retry strictly before a state-machine deadline.
// It still samples a fresh, non-zero offset so reaching the deadline never
// turns a passive probe into a synchronized exact-time operation.
func passiveDelayBefore(deadline time.Duration) time.Duration {
	if deadline <= time.Millisecond {
		return time.Millisecond
	}
	offset := schedulejitter.Offset(deadline)
	if offset < 0 {
		offset = -offset
	}
	delay := deadline - offset
	if delay < time.Millisecond {
		return time.Millisecond
	}
	return delay
}

func maxDuration(left, right time.Duration) time.Duration {
	if left > right {
		return left
	}
	return right
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}
