package opsjobs

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// 普通路由速度优先恢复探针，逐语义对齐 Node
// modules/background/normal-route-speed-first-recovery-probe.service.ts。
// claim/renew/release 状态保存在降级运行态（Redis），进程被 kill 后重启
// 只需重新入队候选：claim TTL 过期即自动可被任何节点重新领用。

// ProbeScope 对齐候选里的路由作用域。
type ProbeScope struct {
	RouteStrategyID string `json:"route_strategy_id"`
	GroupID         string `json:"group_id"`
	SystemAccountID string `json:"system_account_id"`
}

// ProbeConfig 对齐候选里的阈值配置。
type ProbeConfig struct {
	FirstByteDeadlineMS  int64 `json:"first_byte_deadline_ms"`
	RecoverySuccessCount int   `json:"recovery_success_count"`
}

// ProbeCandidate 对齐 NormalRouteLatencyProbeCandidate 窄投影。
// Generation/RuntimeKey/DegradationEventID/DegradedUntil/NextProbeAt 与轮次
// 计数参与 Redis 降级运行态的 candidate-match 围栏（Node
// latencyProbeCandidateMatchesState 的字段集），不得缺省。
type ProbeCandidate struct {
	StateKey             string      `json:"state_key"`
	AccountID            string      `json:"account_id"`
	AccountName          string      `json:"account_name,omitempty"`
	RuntimeKey           string      `json:"runtime_key"`
	Scope                ProbeScope  `json:"scope"`
	Generation           string      `json:"generation"`
	DegradationEventID   string      `json:"degradation_event_id,omitempty"`
	DegradedUntil        string      `json:"degraded_until,omitempty"`
	NextProbeAt          string      `json:"next_probe_at,omitempty"`
	RecoverySuccessCount int         `json:"recovery_success_count"`
	RoundAttemptCount    int         `json:"recovery_probe_round_attempt_count"`
	RoundSuccessCount    int         `json:"recovery_probe_round_success_count"`
	Config               ProbeConfig `json:"config"`
}

// ProbeClaim 是一次已领用的探针 claim。
type ProbeClaim struct {
	Token     string
	Candidate ProbeCandidate
}

// SpeedFirstClaimStore 是降级运行态 claim port。
type SpeedFirstClaimStore interface {
	AcquireClaim(ctx context.Context, candidate ProbeCandidate) (*ProbeClaim, error)
	RenewClaim(ctx context.Context, claim ProbeClaim) (bool, error)
	ReleaseClaim(ctx context.Context, claim ProbeClaim) error
	// Discard 清理目标失效时的降级状态。
	Discard(ctx context.Context, candidate ProbeCandidate) error
	// Defer 保留降级状态并顺延探针（中性结果）。
	Defer(ctx context.Context, candidate ProbeCandidate) (bool, error)
	// RecordSuccess 记录达标结果并累计恢复次数。
	RecordSuccess(ctx context.Context, candidate ProbeCandidate, candidateAccountRef ProbeAccountRef, firstByteMS *int64) (SpeedFirstRecoveryResult, error)
	// RecordFailure 记录未达标结果并顺延下次探针。
	RecordFailure(ctx context.Context, candidate ProbeCandidate, reason string) error
}

// ProbeAccountRef 指向命中的候选账户凭据。
type ProbeAccountRef struct {
	AccountID string
	GroupID   string
}

// SpeedFirstRecoveryResult 是 RecordSuccess 的投影。
type SpeedFirstRecoveryResult struct {
	Cleared                      bool
	RecoverySuccessCount         int
	RequiredRecoverySuccessCount int
}

// SpeedFirstAccountSummary 是账户资格判定的窄投影。
type SpeedFirstAccountSummary struct {
	Status             string
	Schedulable        bool
	AccountExpiresAt   string // RFC3339（必须带 Z 或数值 offset）
	ExpiresAtMS        *int64 // 由调用方解析后注入；nil 表示未提供
	EffectiveAvailable *bool
}

// SpeedFirstCandidateSource 提供账户与候选凭据解析。
type SpeedFirstCandidateSource interface {
	FindAccountForTest(ctx context.Context, accountID string, systemAccountID string) (*SpeedFirstAccountSummary, error)
	FindCandidateAccount(ctx context.Context, groupID, accountID, systemAccountID string) (*ProbeAccountRef, error)
}

// SpeedFirstProbeRunnerOptions 控制 claim 心跳与探针超时。
type SpeedFirstProbeRunnerOptions struct {
	ClaimRenewInterval time.Duration // 0 = 不启用心跳 goroutine（测试可控）
	NowMS              func() int64
}

// SpeedFirstProbeRunner 执行单个候选探针的完整 claim 生命周期。
type SpeedFirstProbeRunner struct {
	store   SpeedFirstClaimStore
	source  SpeedFirstCandidateSource
	probe   func(ctx context.Context, account *SpeedFirstAccountSummary, candidate ProbeCandidate, candidateAccount *ProbeAccountRef) (ProbeResultSnapshot, TransportProbeOutcome)
	options SpeedFirstProbeRunnerOptions
	nowMS   func() int64
}

func NewSpeedFirstProbeRunner(store SpeedFirstClaimStore, source SpeedFirstCandidateSource, probe func(ctx context.Context, account *SpeedFirstAccountSummary, candidate ProbeCandidate, candidateAccount *ProbeAccountRef) (ProbeResultSnapshot, TransportProbeOutcome), options SpeedFirstProbeRunnerOptions) (*SpeedFirstProbeRunner, error) {
	if store == nil || source == nil || probe == nil {
		return nil, errors.New("速度优先恢复探针依赖未初始化")
	}
	nowMS := options.NowMS
	if nowMS == nil {
		return nil, errors.New("速度优先恢复探针必须注入 NowMS 时钟")
	}
	return &SpeedFirstProbeRunner{store: store, source: source, probe: probe, options: options, nowMS: nowMS}, nil
}

// ProbeTimeoutSeconds 对齐 Node probeTimeoutSeconds：
// max(10, ceil((firstByteDeadlineMs + 10_000) / 1000))。
func ProbeTimeoutSeconds(firstByteDeadlineMS int64) int64 {
	return max64(10, (firstByteDeadlineMS+10_000+999)/1000)
}

// NormalRouteSpeedFirstRecoveryProbeRequiresWindowReset 对齐 Node 同名导出。
func NormalRouteSpeedFirstRecoveryProbeRequiresWindowReset(result ProbeResultSnapshot, outcome TransportProbeOutcome) bool {
	return outcome.Kind != ProbeOutcomeFramingComplete || !result.Success
}

// SpeedFirstProbeAccountEligible 对齐 isNormalRouteSpeedFirstProbeAccountEligible。
// accountExpiresAt 非空但缺少解析结果时返回错误；nowMS 为注入时钟。
func SpeedFirstProbeAccountEligible(account *SpeedFirstAccountSummary, nowMS int64) (bool, error) {
	if account == nil {
		return false, nil
	}
	if account.Status != "active" || !account.Schedulable {
		return false, nil
	}
	if account.AccountExpiresAt != "" {
		if account.ExpiresAtMS == nil {
			return false, fmt.Errorf("速度优先恢复探针 accountExpiresAt 必须是带 Z 或数值 offset 的 RFC3339 时间：%s", account.AccountExpiresAt)
		}
		if *account.ExpiresAtMS <= nowMS {
			return false, nil
		}
	}
	if account.EffectiveAvailable != nil && !*account.EffectiveAvailable {
		return false, nil
	}
	return true, nil
}

// ProbeFailureReason 对齐 Node probeFailureReason 的拼接与 1000 字符截断。
func ProbeFailureReason(result ProbeResultSnapshot, thresholdMS int64) string {
	var parts []string
	parts = append(parts, fmt.Sprintf("普通路由速度优先恢复探针未满足 %dms 首字阈值", thresholdMS))
	if result.FirstTokenMS != nil {
		parts = append(parts, fmt.Sprintf("首字 %dms", *result.FirstTokenMS))
	}
	if result.StatusCode != nil {
		parts = append(parts, fmt.Sprintf("HTTP %d", *result.StatusCode))
	}
	if result.ErrorCode != "" {
		parts = append(parts, result.ErrorCode)
	}
	if result.Message != "" {
		parts = append(parts, result.Message)
	}
	joined := strings.Join(parts, "；")
	if len(joined) > 1000 {
		// Node 按 UTF-16 code unit 截断；Go 按 rune 截断在中文场景等价安全。
		runes := []rune(joined)
		if len(runes) > 1000 {
			joined = string(runes[:1000])
		}
	}
	return joined
}

// Run 执行单个候选：claim -> 资格检查 -> 探针 -> 结果回写。
// 返回 true 表示流程完成（无论结果如何）；claim 被其他节点领用时直接跳过。
func (r *SpeedFirstProbeRunner) Run(ctx context.Context, candidate ProbeCandidate) (bool, error) {
	claim, err := r.store.AcquireClaim(ctx, candidate)
	if err != nil {
		return false, err
	}
	if claim == nil {
		// 已由其他节点领用，跳过重复上游探测。
		return true, nil
	}

	var (
		claimMu       sync.Mutex
		claimLost     bool
		stopHeartbeat = func() {}
	)
	ensureClaim := func(phase string) bool {
		claimMu.Lock()
		defer claimMu.Unlock()
		if claimLost {
			return false
		}
		renewed, err := r.store.RenewClaim(ctx, *claim)
		if err == nil && renewed {
			return true
		}
		claimLost = true
		// 续租失败或已失效，停止提交本节点探测结果。
		return false
	}
	if r.options.ClaimRenewInterval > 0 {
		ctxDone := ctx.Done()
		ticker := time.NewTicker(r.options.ClaimRenewInterval)
		stopped := make(chan struct{})
		stopHeartbeat = func() {
			ticker.Stop()
			close(stopped)
		}
		go func() {
			for {
				select {
				case <-stopped:
					return
				case <-ctxDone:
					return
				case <-ticker.C:
					ensureClaim("heartbeat")
				}
			}
		}()
	}
	defer func() {
		stopHeartbeat()
		_ = r.store.ReleaseClaim(context.WithoutCancel(ctx), *claim)
	}()

	if !ensureClaim("before_account_load") {
		return true, nil
	}
	account, err := r.source.FindAccountForTest(ctx, candidate.AccountID, candidate.Scope.SystemAccountID)
	if err != nil {
		return false, err
	}
	eligible, err := SpeedFirstProbeAccountEligible(account, r.nowMS())
	if err != nil {
		return false, err
	}
	if !eligible {
		if !ensureClaim("before_discard_ineligible_account") {
			return true, nil
		}
		if err := r.store.Discard(ctx, candidate); err != nil {
			return false, err
		}
		return true, nil
	}

	candidateAccount, err := r.source.FindCandidateAccount(ctx, candidate.Scope.GroupID, candidate.AccountID, candidate.Scope.SystemAccountID)
	if err != nil {
		return false, err
	}
	if candidateAccount == nil {
		if !ensureClaim("before_discard_missing_group_account") {
			return true, nil
		}
		if err := r.store.Discard(ctx, candidate); err != nil {
			return false, err
		}
		return true, nil
	}

	if !ensureClaim("before_upstream_probe") {
		return true, nil
	}
	result, outcome := r.probe(ctx, account, candidate, candidateAccount)
	firstByteMS := result.FirstTokenMS

	if TransportProbeMeetsFirstByteTarget(result, outcome, candidate.Config.FirstByteDeadlineMS) {
		if !ensureClaim("before_record_success") {
			return true, nil
		}
		if _, err := r.store.RecordSuccess(ctx, candidate, *candidateAccount, firstByteMS); err != nil {
			return false, err
		}
		return true, nil
	}

	// 中性结果无法证明账户仍然慢，必须丢弃当前双探针窗口。
	if NormalRouteSpeedFirstRecoveryProbeRequiresWindowReset(result, outcome) {
		if !ensureClaim("before_defer_neutral_result") {
			return true, nil
		}
		if _, err := r.store.Defer(ctx, candidate); err != nil {
			return false, err
		}
		return true, nil
	}

	if !ensureClaim("before_record_failure") {
		return true, nil
	}
	if err := r.store.RecordFailure(ctx, candidate, ProbeFailureReason(result, candidate.Config.FirstByteDeadlineMS)); err != nil {
		return false, err
	}
	return true, nil
}
