package keymodelrecovery

import (
	"context"
	"crypto/rand"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/accounthealth"
	"github.com/huanminabc/juhe-ai/backend-go-platform/safego"
	"github.com/huanminabc/juhe-ai/backend-go-platform/schedulejitter"
)

const recoveryBatchLimit int64 = 128

// Store is intentionally limited to key_model Redis mutations. In particular,
// it contains no account-health cursor or account-status operation.
type Store interface {
	ServerNow(context.Context) (time.Time, error)
	ListDue(context.Context, time.Time, int64) ([]State, error)
	Acquire(context.Context, State, string, bool, bool) (State, MutationStatus, error)
	Renew(context.Context, State, string) (bool, error)
	Commit(context.Context, State, State, string) (MutationStatus, error)
}

type InputLoader interface {
	LoadAccount(context.Context, string) ([]accounthealth.Input, error)
}

type ProbeExecutor func(context.Context, State, accounthealth.Input) Outcome

// Runner owns only model-recovery probe leases. It schedules independently of
// J1: probe results are committed to the key_model state and never projected
// into accounts.status.
type Runner struct {
	store  Store
	loader InputLoader
	probe  ProbeExecutor
	logger *slog.Logger

	// scanDelay 计算扫描循环每轮等待时长，默认 schedulejitter.Delay（1s 扫描按
	// <1min 档位窗口 = ±interval/2 = ±0.5s 抖动，不扩窗）；抽成字段供单测注入
	// 桩，断言间隔来源每轮重新求值（客户端版本自动跟版设计 §9 抖动缺口修复）。
	scanDelay func(time.Duration) time.Duration

	mu                sync.Mutex
	running           map[string]Running
	lastClosedCleanup time.Time

	// settledApplied / settledUnknown 是 probe 结算累计计数（runCandidate 在
	// Commit Applied / outcome Unknown 时递增），RunCycle 汇总时读取并清零。
	// probe 结算异步滞后于轮次，计数反映自上轮汇总以来的完成量。
	settledApplied atomic.Int64
	settledUnknown atomic.Int64

	// inputLoadWarnMu/inputLoadWarnedAt 支撑 LoadAccount 失败告警按账户 30s
	// 节流（镜像 gatewayruntimecache 节流模式）：扫描周期 1s + Unknown 退避
	// 10s + 每轮候选上限 128，业务库故障 + 候选堆积时不节流理论上可达约
	// 13 条/s（日志治理终审建议）。
	inputLoadWarnMu   sync.Mutex
	inputLoadWarnedAt map[string]time.Time
}

func NewRunner(store Store, loader InputLoader, logger *slog.Logger) *Runner {
	if logger == nil {
		logger = slog.Default()
	}
	return &Runner{store: store, loader: loader, logger: logger, probe: defaultProbe, scanDelay: schedulejitter.Delay, running: map[string]Running{}, inputLoadWarnedAt: map[string]time.Time{}}
}

func (r *Runner) Run(ctx context.Context) error {
	if r == nil || r.store == nil || r.loader == nil {
		return fmt.Errorf("model-recovery runner 未初始化")
	}
	// 每轮重随机扫描间隔（对齐 accountbalance.Service.Run 范式）：裸 ticker 的
	// 相位锁定在进程起跑时刻，重启后多实例同相位扫描、due 候选同相位打上游；
	// Timer 每轮 Reset 抖动后的延迟可逐轮解耦相位。等待仍从上一轮到期时刻起算
	// （Ticker 语义），慢轮次不会往后累积漂移，只表现为立即进入下一轮。
	timer := time.NewTimer(r.scanDelay(ScanInterval))
	defer timer.Stop()
	for {
		if err := r.RunCycle(ctx); err != nil && ctx.Err() == nil {
			r.logger.Warn("model-recovery scan failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			timer.Reset(r.scanDelay(ScanInterval))
		}
	}
}

// RunCycle exists for deterministic tests and is bounded: it creates at most
// 32 local workers, while Redis acquire enforces the shared global/source caps.
func (r *Runner) RunCycle(ctx context.Context) error {
	started := time.Now()
	now, err := r.store.ServerNow(ctx)
	if err != nil {
		return fmt.Errorf("读取 model-recovery Redis 时间: %w", err)
	}
	r.mu.Lock()
	cleanupDue := r.lastClosedCleanup.IsZero() || now.Sub(r.lastClosedCleanup) >= 5*time.Minute
	if cleanupDue {
		r.lastClosedCleanup = now
	}
	r.mu.Unlock()
	if cleanupDue {
		if cleaner, ok := r.store.(interface {
			CleanClosed(context.Context, int64) (int64, error)
		}); ok {
			if _, err := cleaner.CleanClosed(ctx, 1000); err != nil {
				return fmt.Errorf("清理 model-recovery CLOSED state: %w", err)
			}
		}
	}
	due, err := r.store.ListDue(ctx, now, recoveryBatchLimit)
	if err != nil {
		return fmt.Errorf("读取 model-recovery due state: %w", err)
	}
	continuationWaiting := false
	continuationSources := map[string]bool{}
	for _, item := range due {
		if item.Phase == Recovering {
			continuationWaiting = true
			continuationSources[item.CredentialSourceAccountID] = true
		}
	}
	r.mu.Lock()
	selected := SelectDue(asDue(due), runningSlice(r.running), now)
	for _, candidate := range selected {
		leaseID := newLeaseID()
		r.running[leaseID] = Running{SourceID: candidate.State.CredentialSourceAccountID, Continuation: candidate.State.Phase == Recovering}
		go r.runCandidate(ctx, candidate.State, leaseID, continuationWaiting, continuationSources[candidate.State.CredentialSourceAccountID])
	}
	r.mu.Unlock()
	applied := r.settledApplied.Swap(0)
	unknown := r.settledUnknown.Swap(0)
	fields := []any{
		"event", "model_recovery_cycle_summary",
		"due", len(due),
		"selected", len(selected),
		"applied", applied,
		"unknown", unknown,
		"durationMs", time.Since(started).Milliseconds(),
	}
	if len(due)+len(selected)+int(applied)+int(unknown) > 0 {
		r.logger.Info("model-recovery cycle completed", fields...)
		return nil
	}
	r.logger.Debug("model-recovery cycle idle", fields...)
	return nil
}

func (r *Runner) runCandidate(parent context.Context, candidate State, leaseID string, continuationWaiting bool, sourceContinuationWaiting bool) {
	defer safego.Recover("keymodelrecovery.runner.runCandidate")
	defer func() {
		r.mu.Lock()
		delete(r.running, leaseID)
		r.mu.Unlock()
	}()
	state, status, err := r.store.Acquire(parent, candidate, leaseID, continuationWaiting, sourceContinuationWaiting)
	if err != nil || status != Applied {
		if err != nil {
			r.logger.Warn("model-recovery acquire failed", "capabilityHash", candidate.CapabilityHash, "error", err)
		}
		return
	}
	probeCtx, cancelProbe := context.WithTimeout(parent, ProbeTimeout)
	defer cancelProbe()
	lostLease := make(chan struct{}, 1)
	doneRenew := make(chan struct{})
	safego.Go("keymodelrecovery.runner.renewLease", func() { r.renewLease(probeCtx, state, leaseID, cancelProbe, lostLease, doneRenew) })
	outcome := r.executeProbe(probeCtx, state)
	close(doneRenew)
	select {
	case <-lostLease:
		return // A lost owner has no CAS write authority.
	default:
	}
	settleCtx, settleCancel := context.WithTimeout(context.WithoutCancel(parent), 5*time.Second)
	defer settleCancel()
	observedAt, err := r.store.ServerNow(settleCtx)
	if err != nil {
		r.logger.Warn("model-recovery settlement time failed", "capabilityHash", state.CapabilityHash, "error", err)
		return
	}
	next, settlement := Settle(state, RecoveryResult{Generation: state.Generation, DispatchRevision: state.DispatchRevision, LeaseID: leaseID, Outcome: outcome, ObservedAt: observedAt})
	if settlement != Applied {
		r.logger.Debug("model-recovery settlement not applied",
			"event", "model_recovery_settlement_not_applied",
			"runId", leaseID,
			"accountId", state.CredentialSourceAccountID,
			"outcome", outcome,
			"status", string(settlement))
		return
	}
	if outcome == Unknown {
		r.settledUnknown.Add(1)
	}
	if status, err := r.store.Commit(settleCtx, state, next, leaseID); err != nil || status != Applied {
		if err != nil {
			r.logger.Warn("model-recovery commit failed", "capabilityHash", state.CapabilityHash, "error", err)
		} else {
			r.logger.Debug("model-recovery commit not applied",
				"event", "model_recovery_commit_not_applied",
				"runId", leaseID,
				"accountId", state.CredentialSourceAccountID,
				"outcome", outcome,
				"status", string(status))
		}
		return
	}
	r.settledApplied.Add(1)
}

func (r *Runner) renewLease(ctx context.Context, state State, leaseID string, cancel context.CancelFunc, lostLease chan<- struct{}, done <-chan struct{}) {
	ticker := time.NewTicker(ProbeLeaseRenew)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			ok, err := r.store.Renew(ctx, state, leaseID)
			if err != nil || !ok {
				select {
				case lostLease <- struct{}{}:
				default:
				}
				cancel()
				return
			}
		}
	}
}

// warnInputLoadFailed 按账户 30s 节流输出业务库输入加载失败告警（日志治理
// 终审建议：候选堆积 + 业务库故障时逐次 Warn 可达约 13 条/s）。零值构造
// （map 为 nil）时首条计入后正常节流。
func (r *Runner) warnInputLoadFailed(accountID string, err error) {
	const warnInterval = 30 * time.Second
	now := time.Now()
	r.inputLoadWarnMu.Lock()
	if r.inputLoadWarnedAt == nil {
		r.inputLoadWarnedAt = map[string]time.Time{}
	}
	if last, ok := r.inputLoadWarnedAt[accountID]; ok && now.Sub(last) < warnInterval {
		r.inputLoadWarnMu.Unlock()
		return
	}
	r.inputLoadWarnedAt[accountID] = now
	r.inputLoadWarnMu.Unlock()
	r.logger.Warn("model-recovery input load failed",
		"event", "model_recovery_input_load_failed",
		"credentialSourceAccountId", accountID,
		"error", err.Error())
}

func (r *Runner) executeProbe(ctx context.Context, state State) Outcome {
	inputs, err := r.loader.LoadAccount(ctx, state.CredentialSourceAccountID)
	if err != nil {
		r.warnInputLoadFailed(state.CredentialSourceAccountID, err)
		return Unknown
	}
	for _, input := range inputs {
		if input.AccountID != state.CredentialSourceAccountID || input.DispatchRevision != state.DispatchRevision {
			continue
		}
		outcome := r.probe(ctx, state, input)
		r.logger.Debug("model-recovery probe outcome",
			"event", "model_recovery_probe_outcome",
			"accountId", state.CredentialSourceAccountID,
			"outcome", outcome)
		return outcome
	}
	return Unknown
}

func defaultProbe(ctx context.Context, state State, input accounthealth.Input) Outcome {
	// 缺陷修复：此前 ProbeOptions 缺少 Secret，decryptToken 对空 secret 直接
	// 报"凭据 envelope 缺失"，恢复探针永远只能得到 unknown。凭据 envelope 由
	// account-health 输入读取器以 JUHE_AI_ACCOUNT_HEALTH_CREDENTIAL_SECRET
	// 加密（见 accounthealth/config.go），这里读取同一契约变量。
	result := accounthealth.ProbeExactKeyModel(ctx, input, state.KeyFingerprint, state.FinalUpstreamModel, state.UpstreamEndpointMode, accounthealth.ProbeOptions{Secret: os.Getenv("JUHE_AI_ACCOUNT_HEALTH_CREDENTIAL_SECRET"), Timeout: ProbeTimeout, MaxResponseBytes: 256 * 1024})
	if result.Outcome == accounthealth.OutcomeSuccess {
		return CompleteSuccess
	}
	if result.Outcome == accounthealth.OutcomeTaskFailed {
		return Unknown
	}
	return UpstreamNotComplete
}

func asDue(states []State) []Due {
	items := make([]Due, 0, len(states))
	for _, state := range states {
		items = append(items, Due{State: state, SourceID: state.CredentialSourceAccountID})
	}
	return items
}

func runningSlice(items map[string]Running) []Running {
	result := make([]Running, 0, len(items))
	for _, item := range items {
		result = append(result, item)
	}
	return result
}

func newLeaseID() string {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err == nil {
		return fmt.Sprintf("mcr-%x", bytes[:])
	}
	return fmt.Sprintf("mcr-%d", time.Now().UnixNano())
}
