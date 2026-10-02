package modelcheckowner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// HealthFact is the durable J3b health projection input. Raw upstream
// responses and credentials must never be placed here.
type HealthFact struct {
	AccountID, SystemAccountID, StatHour, RunID, ProviderCode, Model, Profile, ScheduleID string
	PolicyRevision, AccountConfigRevision                                                 string
	ObservedAt                                                                            time.Time
	Score, Threshold, RecoveryIntervalMinutes                                             int
	Level, ErrorCode, ErrorMessage, PenaltyAction                                         string
	// HardQualityFailure carries an explicit quality gate that is independent
	// of the aggregate score (currently an undeclared response-model mismatch).
	// It is only supplied by the owning runtime; incomplete evidence without an
	// explicit hard failure remains fail-closed below.
	HardQualityFailure bool
	EnforcementAllowed bool
}

// HealthReader is the narrow read-only contract that a future J3c consumer
// may depend on. It deliberately exposes no mutation method and requires an
// explicit account/hour scope for every lookup.
type HealthReader interface {
	ReadHealthFact(context.Context, string, string) (HealthFact, bool, error)
}

var _ HealthReader = (*Store)(nil)

// QualityProjector is the only path that may publish a J3b health fact. It
// requires a fully formed, trusted aggregate for full diagnostics. Quick
// diagnostics intentionally use a smaller evidence set, so a completed quick
// quality failure is also eligible for the Node-compatible health path; failed
// publication remains retryable on the same run.
type QualityProjector struct {
	Store       *Store
	Enforcement EnforcementApplier
}

// EnforcementApplier is the Business-owner mutation port for a formed
// quality failure. Implementations must perform account status and
// account_quality_enforcements CAS in one transaction using frozen revisions.
type EnforcementApplier interface {
	Apply(context.Context, QualityEnforcement) (EnforcementOutcome, error)
}

// EnforcementOutcome 是处罚端口的可观察结果：健康投影据此把 result/
// enforcementId/generation/beforeStatus/afterStatus/recoveryDueAt 增补回
// run 行的 quality_decision_json。AlreadyEffective 表示账户已处于同
// action 的有效处罚（含同 run 重试幂等命中），不是错误。
type EnforcementOutcome struct {
	EnforcementID    string
	Generation       int
	BeforeStatus     string
	AfterStatus      string
	RecoveryDueAt    string
	AlreadyEffective bool
}

type QualityEnforcement struct {
	AccountID, SystemAccountID, RunID, ProviderCode, Model, Profile, ScheduleID string
	PolicyRevision, AccountConfigRevision                                       string
	Action                                                                      string
	Score, Threshold, RecoveryIntervalMinutes                                   int
	Message                                                                     string
	OccurredAt                                                                  time.Time
	// HardQualityFailure 表示独立于分数的显式质量硬失败（例如
	// undeclared_mismatch）。分数达标但硬失败为真时仍是合法处罚输入。
	HardQualityFailure bool
}

type HealthSyncRetryExecutor struct {
	Projector *QualityProjector
}

// markHealthSyncFailure must remain usable after an HTTP request is canceled:
// the durable run is already terminal and needs a retry marker, otherwise a
// canceled request can leave a failed health publication looking pending.
func (p *QualityProjector) markHealthSyncFailure(ctx context.Context, runID string) {
	if ctx == nil || ctx.Err() != nil {
		background, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = p.Store.MarkHealthSync(background, runID, "failed")
		return
	}
	_ = p.Store.MarkHealthSync(ctx, runID, "failed")
}

func (e *HealthSyncRetryExecutor) Execute(ctx context.Context, task ScheduleTask) error {
	if e == nil || e.Projector == nil || e.Projector.Store == nil {
		return errors.New("J3b health retry executor is not initialized")
	}
	if task.Kind != SchedulerHealthRetry {
		return fmt.Errorf("J3b health retry executor received %s", task.Kind)
	}
	var payload struct {
		RunID string `json:"runId"`
	}
	if err := json.Unmarshal(task.Payload, &payload); err != nil || payload.RunID == "" {
		return errors.New("J3b health retry task payload lacks runId")
	}
	retries, err := e.Projector.Store.ListHealthSyncRetries(ctx, 1000)
	if err != nil {
		return err
	}
	for _, retry := range retries {
		if retry.RunID != payload.RunID {
			continue
		}
		return e.Projector.Project(ctx, retry.RunID, EvidenceAggregate{Formed: retry.EvidenceFormed, TrustFormed: retry.TrustFormed}, HealthFact{AccountID: retry.AccountID, SystemAccountID: retry.SystemAccountID, StatHour: retry.StatHour, RunID: retry.RunID, ProviderCode: retry.ProviderCode, Model: retry.Model, Profile: retry.Profile, ScheduleID: retry.ScheduleID, PolicyRevision: retry.PolicyRevision, AccountConfigRevision: retry.AccountConfigRevision, PenaltyAction: retry.PenaltyAction, RecoveryIntervalMinutes: retry.RecoveryIntervalMinutes, EnforcementAllowed: retry.EnforcementAllowed, ObservedAt: retry.ObservedAt, Score: retry.Score, Threshold: retry.Threshold, Level: retry.Level, HardQualityFailure: retry.HardQualityFailure})
	}
	return fmt.Errorf("J3b health retry run %s not found", payload.RunID)
}

func (p *QualityProjector) Project(ctx context.Context, runID string, aggregate EvidenceAggregate, fact HealthFact) error {
	if p == nil || p.Store == nil {
		return errors.New("J3b quality projector is not initialized")
	}
	runID = strings.TrimSpace(runID)
	factRunID := strings.TrimSpace(fact.RunID)
	if runID == "" || (factRunID != "" && factRunID != runID) || (aggregate.Formed && factRunID == "") {
		// Do not mutate any run on an identity mismatch: the caller supplied
		// contradictory identities, so marking runID failed could poison an
		// unrelated durable run and make retries replay the wrong fact.
		return errors.New("J3b health projection run identity mismatch")
	}
	// Unavailable means the upstream could not yield quality evidence. It is
	// still a valid health fact for retry/recovery purposes, but it must never
	// authorize enforcement. Quick diagnostics intentionally have a smaller
	// family set than full diagnostics and follow Node's completed quality
	// decision path without the full formed+trusted gate.
	qualityFailure := fact.Score < fact.Threshold || fact.Level == "suspicious" || fact.HardQualityFailure
	quickQualityFailure := fact.Profile == "quick" && fact.Level != "unavailable" && qualityFailure
	if fact.Level != "unavailable" && !quickQualityFailure && (!aggregate.Formed || !aggregate.TrustFormed) {
		p.markHealthSyncFailure(ctx, runID)
		return errors.New("J3b evidence is not formed; health projection is denied")
	}
	if strings.TrimSpace(fact.AccountID) == "" || strings.TrimSpace(fact.SystemAccountID) == "" || strings.TrimSpace(fact.ProviderCode) == "" || strings.TrimSpace(fact.Model) == "" || strings.TrimSpace(fact.Profile) == "" || !validHealthStatHour(fact.StatHour) || fact.ObservedAt.IsZero() || fact.Threshold < 40 || fact.Threshold > 100 || fact.Score < 0 || fact.Score > 100 {
		p.markHealthSyncFailure(ctx, runID)
		return errors.New("J3b health projection scope is incomplete")
	}
	if fact.Score >= fact.Threshold && fact.Level != "unavailable" && fact.Level != "suspicious" && !fact.HardQualityFailure {
		return errors.New("J3b health projection requires a quality failure or unavailable result")
	}
	if fact.Level == "unavailable" {
		fact.EnforcementAllowed = false
	}
	// 处罚与健康事实解耦（J3B 修复）：处罚结果只记入 quality_decision_json
	// 的结果字段，不再阻断 ApplyHealthFact，也不再注册无限重试；stale 类
	// 配置漂移明确不重试。unavailable 只是可用性事实，不构成质量不达标
	// 判定，保持 finalize 写入的 not_triggered 不被改写。
	triggered := fact.Level != "unavailable" && qualityFailure
	enforcementResult, enforcementMessage := "", ""
	var outcome EnforcementOutcome
	if triggered && fact.EnforcementAllowed {
		if p.Enforcement == nil {
			enforcementResult, enforcementMessage = "skipped", "J3b quality enforcement owner is not configured"
		} else {
			action := fact.PenaltyAction
			if action == "" {
				action = "quality_isolate"
			}
			if action != "disable" && action != "fallback" && action != "quality_isolate" {
				p.markHealthSyncFailure(ctx, runID)
				return errors.New("J3b quality enforcement action is invalid")
			}
			applied, applyErr := p.Enforcement.Apply(ctx, QualityEnforcement{AccountID: fact.AccountID, SystemAccountID: fact.SystemAccountID, RunID: fact.RunID, ProviderCode: fact.ProviderCode, Model: fact.Model, Profile: fact.Profile, PolicyRevision: fact.PolicyRevision, AccountConfigRevision: fact.AccountConfigRevision, ScheduleID: fact.ScheduleID, Score: fact.Score, Threshold: fact.Threshold, RecoveryIntervalMinutes: fact.RecoveryIntervalMinutes, Action: action, OccurredAt: fact.ObservedAt, Message: fact.ErrorMessage, HardQualityFailure: fact.HardQualityFailure})
			switch {
			case applyErr == nil && applied.AlreadyEffective:
				enforcementResult, outcome = "already_effective", applied
			case applyErr == nil:
				enforcementResult, outcome = "applied", applied
			case isStaleEnforcementError(applyErr):
				enforcementResult, enforcementMessage = "stale", applyErr.Error()
			default:
				enforcementResult, enforcementMessage = "failed", applyErr.Error()
			}
		}
	} else if triggered {
		enforcementResult, enforcementMessage = "skipped", "质量处罚未启用或本次运行不满足处罚资格"
	}
	// 健康事实写入失败时处罚副作用已经发生：先尽力把处罚结果与
	// healthSyncResult="pending_retry" 合并进 quality_decision，再保留
	// markHealthSyncFailure 的重试标记并返回原始错误；重试成功后由
	// applied/already_effective 终值覆盖 pending_retry。
	outcomePatch := qualityDecisionOutcomePatch(enforcementResult, enforcementMessage, outcome)
	if _, err := p.Store.ApplyHealthFact(ctx, fact); err != nil {
		p.markHealthSyncFailure(ctx, runID)
		p.mergeHealthPendingRetry(ctx, runID, outcomePatch)
		return err
	}
	patch := outcomePatch
	patch["healthSyncResult"] = "applied"
	patch["healthStatHour"] = fact.StatHour
	if err := p.Store.MergeRunQualityDecision(ctx, runID, patch); err != nil {
		// 健康事实已写入但结果字段未落 run：保持 failed 以便重试幂等补写，
		// 重试会经 AlreadyEffectives 分支得到 already_effective 终值。
		p.markHealthSyncFailure(ctx, runID)
		return fmt.Errorf("merge J3b quality decision outcome: %w", err)
	}
	return p.Store.MarkHealthSync(ctx, runID, "applied")
}

// qualityDecisionOutcomePatch 构造处罚结果的 quality_decision 增补字段；
// 未触发处罚（result 为空）时返回空 patch，不写任何处罚键。
func qualityDecisionOutcomePatch(enforcementResult, enforcementMessage string, outcome EnforcementOutcome) map[string]any {
	patch := map[string]any{}
	if enforcementResult == "" {
		return patch
	}
	patch["result"] = enforcementResult
	patch["enforcementId"] = nullIfEmpty(outcome.EnforcementID)
	patch["generation"] = intOrNull(outcome.Generation)
	patch["beforeStatus"] = nullIfEmpty(outcome.BeforeStatus)
	patch["afterStatus"] = nullIfEmpty(outcome.AfterStatus)
	patch["recoveryDueAt"] = nullIfEmpty(outcome.RecoveryDueAt)
	if enforcementMessage != "" {
		patch["message"] = enforcementMessage
	}
	return patch
}

// mergeHealthPendingRetry 在健康事实写入失败后把 healthSyncResult=
// "pending_retry" 连同已发生的处罚结果合并进 quality_decision；ctx 已
// 取消时改用后台超时上下文（与 markHealthSyncFailure 同一防御），合并
// 失败不掩盖健康事实的原始错误。
func (p *QualityProjector) mergeHealthPendingRetry(ctx context.Context, runID string, patch map[string]any) {
	if patch == nil {
		patch = map[string]any{}
	}
	patch["healthSyncResult"] = "pending_retry"
	mergeCtx := ctx
	if ctx == nil || ctx.Err() != nil {
		background, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		mergeCtx = background
	}
	_ = p.Store.MergeRunQualityDecision(mergeCtx, runID, patch)
}

// isStaleEnforcementError 识别处罚端口返回的配置过期类错误（策略/账户
// revision 漂移）。这类错误重试必然复用冻结 revision 而永远失败，投影
// 层将其记为 result=stale 且不再注册重试。
func isStaleEnforcementError(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	return strings.Contains(message, "configuration is stale") || strings.Contains(message, "revision is stale")
}

func nullIfEmpty(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func intOrNull(value int) any {
	if value <= 0 {
		return nil
	}
	return value
}

// CompareLatestWins matches Node's predicate: observed_at first, then run ID.
func CompareLatestWins(candidate, current HealthFact) (int, error) {
	if candidate.AccountID == "" || candidate.StatHour == "" || candidate.RunID == "" || candidate.ObservedAt.IsZero() {
		return 0, fmt.Errorf("health fact identity is incomplete")
	}
	if current.AccountID != "" && (candidate.AccountID != current.AccountID || candidate.StatHour != current.StatHour) {
		return 0, fmt.Errorf("health fact scope mismatch")
	}
	if current.ObservedAt.IsZero() {
		return 1, nil
	}
	if candidate.ObservedAt.After(current.ObservedAt) {
		return 1, nil
	}
	if candidate.ObservedAt.Before(current.ObservedAt) {
		return -1, nil
	}
	if candidate.RunID > current.RunID {
		return 1, nil
	}
	if candidate.RunID < current.RunID {
		return -1, nil
	}
	return 0, nil
}
