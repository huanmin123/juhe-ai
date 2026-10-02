package modelcheckowner

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckactive"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckprofile"
	"github.com/huanminabc/juhe-ai/backend-go-platform/safego"
)

// BusinessSchedulerSource is the owner of J3b schedule and recovery leases.
// These leases are business facts, so they cannot be mirrored through the J3b
// store or a generic task queue. Health retry remains in the dedicated J3b
// store because its retry fact is a run/outcome projection.
type BusinessSchedulerSource struct {
	Business *sql.DB
	Postgres bool
	Store    *Store
	OwnerID  string
	Lease    time.Duration
}

// CheckContract verifies the Business tables and columns that make the
// schedule/recovery lease and completion transactions safe to mount.
func (s *BusinessSchedulerSource) CheckContract(ctx context.Context) error {
	if s == nil || s.Business == nil {
		return errors.New("J3b Business scheduler database is not initialized")
	}
	tx, err := s.Business.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return fmt.Errorf("open J3b Business scheduler contract: %w", err)
	}
	defer tx.Rollback()
	contracts := []struct{ table, columns string }{
		{"accounts", "id,system_account_id,provider_code,config_revision,dispatch_revision,authorization_instance_source_account_id,deleted_at,authorization_instance_authorization_id,status,health_check_model,availability_schedule_json,schedulable,fallback_enabled,super_priority_enabled,last_error_code,last_error_message,updated_at"},
		{"model_quality_schedules", "id,revision,system_account_id,account_id,model,interval_minutes,profile,penalty_threshold,penalty_action,recovery_interval_minutes,custom_question_ids,enabled,next_run_at,lease_owner,lease_until,last_run_id,last_run_at,last_run_status,updated_at"},
		{"model_quality_policies", "system_account_id,custom_question_ids"},
		{"account_quality_enforcements", "account_id,system_account_id,enforcement_id,generation,state,action,trigger_run_id,config_source,config_source_id,policy_revision,profile,penalty_threshold,recovery_interval_minutes,recovery_model,account_config_revision,recovery_due_at,recovery_lease_owner,recovery_lease_until,last_recovery_run_id,cleared_at,updated_at"},
	}
	for _, contract := range contracts {
		if _, err := tx.ExecContext(ctx, "SELECT "+contract.columns+" FROM "+s.table(contract.table)+" LIMIT 0"); err != nil {
			return fmt.Errorf("verify J3b Business scheduler table %s: %w", contract.table, err)
		}
	}
	return tx.Commit()
}

func (s *BusinessSchedulerSource) Claim(ctx context.Context, kind SchedulerKind, now time.Time, limit int) ([]ScheduleTask, error) {
	if s == nil || s.Business == nil || s.Store == nil || strings.TrimSpace(s.OwnerID) == "" || limit < 1 {
		return nil, errors.New("J3b Business scheduler source is not initialized")
	}
	if kind == SchedulerHealthRetry {
		if err := s.Store.EnsureHealthRetryTasks(ctx, limit); err != nil {
			return nil, err
		}
		return (&SQLSchedulerSource{Store: s.Store, OwnerID: s.OwnerID, Lease: s.Lease}).Claim(ctx, kind, now, limit)
	}
	if kind != SchedulerScheduled && kind != SchedulerQualityRecovery {
		return nil, fmt.Errorf("unsupported J3b Business scheduler kind %q", kind)
	}
	lease := s.Lease
	if lease <= 0 {
		lease = 6 * time.Minute
	}
	tx, err := s.Business.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if kind == SchedulerScheduled {
		tasks, err := s.claimSchedules(ctx, tx, now, limit, lease)
		if err != nil {
			return nil, err
		}
		return tasks, tx.Commit()
	}
	tasks, err := s.claimRecoveries(ctx, tx, now, limit, lease)
	if err != nil {
		return nil, err
	}
	return tasks, tx.Commit()
}

// EffectiveLease returns the Business lease duration Claim actually writes
// (claimSchedules and claimRecoveries share this default). Assembly sites
// derive the run execution budget from it via ScheduleRunBudget so the lease
// window and the in-run deadline cannot drift apart.
func (s *BusinessSchedulerSource) EffectiveLease() time.Duration {
	if s == nil || s.Lease <= 0 {
		return 6 * time.Minute
	}
	return s.Lease
}

func (s *BusinessSchedulerSource) Complete(ctx context.Context, task ScheduleTask) error {
	if task.Kind != SchedulerHealthRetry {
		return nil
	}
	return (&SQLSchedulerSource{Store: s.Store, OwnerID: s.OwnerID, Lease: s.Lease}).Complete(ctx, task)
}
func (s *BusinessSchedulerSource) Fail(ctx context.Context, task ScheduleTask, cause error) error {
	if task.Kind != SchedulerHealthRetry {
		return nil
	}
	return (&SQLSchedulerSource{Store: s.Store, OwnerID: s.OwnerID, Lease: s.Lease}).Fail(ctx, task, cause)
}

func (s *BusinessSchedulerSource) claimSchedules(ctx context.Context, tx *sql.Tx, now time.Time, limit int, lease time.Duration) ([]ScheduleTask, error) {
	q := s.bind
	lock := ""
	if s.Postgres {
		lock = " FOR UPDATE OF mqs SKIP LOCKED"
	}
	rows, err := tx.QueryContext(ctx, q(`SELECT mqs.id,mqs.revision,mqs.system_account_id,mqs.account_id,mqs.model,mqs.interval_minutes,mqs.profile,mqs.penalty_threshold,mqs.penalty_action,mqs.recovery_interval_minutes,mqs.custom_question_ids,a.config_revision,a.dispatch_revision,a.provider_code FROM `+s.table("model_quality_schedules")+` mqs JOIN `+s.table("accounts")+` a ON a.id=mqs.account_id WHERE mqs.enabled=1 AND mqs.next_run_at<=? AND (mqs.lease_until IS NULL OR mqs.lease_until<=?) AND a.deleted_at IS NULL AND a.authorization_instance_authorization_id IS NULL AND a.status='active' ORDER BY mqs.next_run_at,mqs.id LIMIT ?`+lock), now.UTC().Format(time.RFC3339Nano), now.UTC().Format(time.RFC3339Nano), limit)
	if err != nil {
		return nil, fmt.Errorf("claim J3b schedules: %w", err)
	}
	defer rows.Close()
	var tasks []ScheduleTask
	for rows.Next() {
		var id, systemID, accountID, model, profile, action, provider string
		var revision, interval, recoveryInterval, threshold, configRevision, dispatchRevision int
		var customQuestionIds sql.NullString
		if err := rows.Scan(&id, &revision, &systemID, &accountID, &model, &interval, &profile, &threshold, &action, &recoveryInterval, &customQuestionIds, &configRevision, &dispatchRevision, &provider); err != nil {
			return nil, err
		}
		res, err := tx.ExecContext(ctx, q(`UPDATE `+s.table("model_quality_schedules")+` SET lease_owner=?,lease_until=?,updated_at=? WHERE id=? AND revision=? AND enabled=1 AND (lease_until IS NULL OR lease_until<=?)`), s.OwnerID, now.Add(lease).UTC().Format(time.RFC3339Nano), now.UTC().Format(time.RFC3339Nano), id, revision, now.UTC().Format(time.RFC3339Nano))
		if err != nil {
			return nil, err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			continue
		}
		// claim 时冻结：题库 id 取自 schedule 行并写入不可变 payload，运行中
		// 的配置变更不影响本次执行。
		payload, err := json.Marshal(ScheduledPayload{SystemAccountID: systemID, ActorSystemAccountID: systemID, TargetType: "account", TargetID: accountID, Model: model, Profile: profile, ProviderCode: provider, Threshold: threshold, PenaltyAction: action, ConfigRevision: strconv.Itoa(configRevision), DispatchRevision: int64(dispatchRevision), SourceConfigRevision: strconv.Itoa(configRevision), SourceDispatchRevision: int64(dispatchRevision), PolicyRevision: strconv.Itoa(revision), ProbeSetVersion: probeSetForProfile(profile), IdentityKey: systemID + ":" + accountID + ":" + model, ScheduleID: id, OwnerID: s.OwnerID, ScheduleRevision: revision, IntervalMinutes: interval, RecoveryIntervalMinutes: recoveryInterval, CustomQuestionIds: qualityCustomQuestionIds(customQuestionIds)})
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, ScheduleTask{ID: "schedule:" + id + ":" + strconv.Itoa(revision), Kind: SchedulerScheduled, OwnerID: s.OwnerID, Payload: payload})
	}
	return tasks, rows.Err()
}

func (s *BusinessSchedulerSource) claimRecoveries(ctx context.Context, tx *sql.Tx, now time.Time, limit int, lease time.Duration) ([]ScheduleTask, error) {
	q := s.bind
	lock := ""
	if s.Postgres {
		lock = " FOR UPDATE OF aqe SKIP LOCKED"
	}
	rows, err := tx.QueryContext(ctx, q(`SELECT aqe.account_id,aqe.system_account_id,aqe.enforcement_id,aqe.generation,COALESCE(NULLIF(aqe.recovery_model,''),a.health_check_model),a.config_revision,a.dispatch_revision,COALESCE(sa.config_revision,a.config_revision),COALESCE(sa.dispatch_revision,a.dispatch_revision),aqe.policy_revision,COALESCE(aqe.config_source_id,''),aqe.profile,aqe.penalty_threshold,aqe.recovery_interval_minutes,a.provider_code,COALESCE(CASE WHEN aqe.config_source='schedule' THEN mqs.custom_question_ids END,mqp.custom_question_ids) FROM `+s.table("account_quality_enforcements")+` aqe JOIN `+s.table("accounts")+` a ON a.id=aqe.account_id LEFT JOIN `+s.table("accounts")+` sa ON sa.id=a.authorization_instance_source_account_id LEFT JOIN `+s.table("model_quality_schedules")+` mqs ON aqe.config_source='schedule' AND mqs.id=aqe.config_source_id LEFT JOIN `+s.table("model_quality_policies")+` mqp ON mqp.system_account_id=aqe.system_account_id WHERE aqe.state='active' AND aqe.action='quality_isolate' AND aqe.recovery_due_at IS NOT NULL AND aqe.recovery_due_at<=? AND (aqe.recovery_lease_until IS NULL OR aqe.recovery_lease_until<=?) AND a.deleted_at IS NULL AND a.status='quality_isolated' ORDER BY aqe.recovery_due_at,aqe.account_id LIMIT ?`+lock), now.UTC().Format(time.RFC3339Nano), now.UTC().Format(time.RFC3339Nano), limit)
	if err != nil {
		return nil, fmt.Errorf("claim J3b recoveries: %w", err)
	}
	defer rows.Close()
	var tasks []ScheduleTask
	for rows.Next() {
		var accountID, systemID, enforcementID, model, scheduleID, profile, provider string
		var generation, configRevision, dispatchRevision, sourceConfigRevision, sourceDispatchRevision, policyRevision, threshold, interval int
		var customQuestionIds sql.NullString
		if err := rows.Scan(&accountID, &systemID, &enforcementID, &generation, &model, &configRevision, &dispatchRevision, &sourceConfigRevision, &sourceDispatchRevision, &policyRevision, &scheduleID, &profile, &threshold, &interval, &provider, &customQuestionIds); err != nil {
			return nil, err
		}
		if strings.TrimSpace(model) == "" {
			continue
		}
		res, err := tx.ExecContext(ctx, q(`UPDATE `+s.table("account_quality_enforcements")+` SET recovery_lease_owner=?,recovery_lease_until=?,account_config_revision=?,updated_at=? WHERE account_id=? AND enforcement_id=? AND generation=? AND state='active' AND action='quality_isolate' AND (recovery_lease_until IS NULL OR recovery_lease_until<=?)`), s.OwnerID, now.Add(lease).UTC().Format(time.RFC3339Nano), configRevision, now.UTC().Format(time.RFC3339Nano), accountID, enforcementID, generation, now.UTC().Format(time.RFC3339Nano))
		if err != nil {
			return nil, err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			continue
		}
		// 恢复路径的题库配置与 scheduled 同语义：触发源是 schedule 时冻结该
		// schedule 行的配置，否则回落系统账户的 policy 行配置；NULL 一律归一
		// 为空（不执行题库家族）。
		payload, err := json.Marshal(ScheduledPayload{SystemAccountID: systemID, ActorSystemAccountID: systemID, TargetType: "account", TargetID: accountID, Model: model, Profile: profile, ProviderCode: provider, Threshold: threshold, PenaltyAction: "quality_isolate", ConfigRevision: strconv.Itoa(configRevision), DispatchRevision: int64(dispatchRevision), SourceConfigRevision: strconv.Itoa(sourceConfigRevision), SourceDispatchRevision: int64(sourceDispatchRevision), PolicyRevision: strconv.Itoa(policyRevision), ProbeSetVersion: probeSetForProfile(profile), IdentityKey: systemID + ":" + accountID + ":" + model, ScheduleID: scheduleID, OwnerID: s.OwnerID, EnforcementID: enforcementID, Generation: generation, RecoveryIntervalMinutes: interval, CustomQuestionIds: qualityCustomQuestionIds(customQuestionIds)})
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, ScheduleTask{ID: "recovery:" + accountID + ":" + enforcementID + ":" + strconv.Itoa(generation), Kind: SchedulerQualityRecovery, OwnerID: s.OwnerID, Payload: payload})
	}
	return tasks, rows.Err()
}

func (s *BusinessSchedulerSource) CompleteScheduled(ctx context.Context, payload ScheduledPayload, result RunResult) error {
	if s == nil || s.Business == nil || strings.TrimSpace(payload.OwnerID) == "" || strings.TrimSpace(payload.ScheduleID) == "" || payload.ScheduleRevision < 1 || payload.IntervalMinutes < 1 {
		return errors.New("J3b schedule completion input is invalid")
	}
	status := result.Status
	if status != "completed" && status != "failed" && status != "canceled" {
		status = "failed"
	}
	now := time.Now().UTC()
	next := now.Add(time.Duration(payload.IntervalMinutes) * time.Minute).Format(time.RFC3339Nano)
	res, err := s.Business.ExecContext(ctx, s.bind(`UPDATE `+s.table("model_quality_schedules")+` SET last_run_id=?,last_run_at=?,last_run_status=?,next_run_at=?,lease_owner=NULL,lease_until=NULL,updated_at=? WHERE id=? AND revision=? AND lease_owner=? AND lease_until>?`), nullable(result.RunID), now.Format(time.RFC3339Nano), status, next, now.Format(time.RFC3339Nano), payload.ScheduleID, payload.ScheduleRevision, payload.OwnerID, now.Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("complete J3b schedule: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return errors.New("J3b schedule completion lease is stale")
	}
	return nil
}

// TriggerKindScheduleNow 是"计划立即执行"运行在 model_check_runs 里的
// trigger_kind 值：运行仍归属该计划（schedule_id 指向计划行），但由用户
// 即时受理触发，不占用调度租约。
const TriggerKindScheduleNow = "schedule_now"

// ScheduleRunNowSnapshot 是受理瞬间从计划行（连同账户行）冻结的现值快照：
// 执行期内的计划编辑不再影响本次运行，完成回写按快照 revision CAS。
type ScheduleRunNowSnapshot struct {
	ScheduleID, SystemAccountID, AccountID string
	Model, Profile, PenaltyAction          string
	Threshold                              int
	IntervalMinutes                        int
	RecoveryIntervalMinutes                int
	Revision                               int
	Enabled                                bool
	CustomQuestionIds                      []string
	LeaseUntil                             string
	ProviderCode                           string
	ConfigRevision, DispatchRevision       int
}

// ScheduleForRunNow 读取计划行现值（join 账户行取 revision/供应商）。账户已
// 删除或计划不属于该系统账户时返回 found=false（受理侧映射为 not_found）。
func (s *BusinessSchedulerSource) ScheduleForRunNow(ctx context.Context, systemID, scheduleID string) (ScheduleRunNowSnapshot, bool, error) {
	if s == nil || s.Business == nil || strings.TrimSpace(systemID) == "" || strings.TrimSpace(scheduleID) == "" {
		return ScheduleRunNowSnapshot{}, false, errors.New("J3b run-now schedule lookup input is invalid")
	}
	var snap ScheduleRunNowSnapshot
	var enabled int
	var customQuestionIds sql.NullString
	var leaseUntil sql.NullString
	err := s.Business.QueryRowContext(ctx, s.bind(`SELECT mqs.id,mqs.system_account_id,mqs.account_id,mqs.model,mqs.profile,mqs.penalty_threshold,mqs.penalty_action,mqs.interval_minutes,mqs.recovery_interval_minutes,mqs.revision,mqs.enabled,mqs.custom_question_ids,mqs.lease_until,a.provider_code,a.config_revision,a.dispatch_revision FROM `+s.table("model_quality_schedules")+` mqs JOIN `+s.table("accounts")+` a ON a.id=mqs.account_id AND a.deleted_at IS NULL WHERE mqs.id=? AND mqs.system_account_id=?`), scheduleID, systemID).Scan(&snap.ScheduleID, &snap.SystemAccountID, &snap.AccountID, &snap.Model, &snap.Profile, &snap.Threshold, &snap.PenaltyAction, &snap.IntervalMinutes, &snap.RecoveryIntervalMinutes, &snap.Revision, &enabled, &customQuestionIds, &leaseUntil, &snap.ProviderCode, &snap.ConfigRevision, &snap.DispatchRevision)
	if errors.Is(err, sql.ErrNoRows) {
		return ScheduleRunNowSnapshot{}, false, nil
	}
	if err != nil {
		return ScheduleRunNowSnapshot{}, false, fmt.Errorf("read J3b run-now schedule: %w", err)
	}
	snap.Enabled = enabled == 1
	snap.CustomQuestionIds = qualityCustomQuestionIds(customQuestionIds)
	if leaseUntil.Valid {
		snap.LeaseUntil = leaseUntil.String
	}
	return snap, true, nil
}

// AccountRecoveryLeaseActive 报告该账户是否存在未到期的 active 恢复租约
// （恢复调度正在执行恢复探针）。run-now 受理必须与它互斥。
func (s *BusinessSchedulerSource) AccountRecoveryLeaseActive(ctx context.Context, accountID string, now time.Time) (bool, error) {
	if s == nil || s.Business == nil || strings.TrimSpace(accountID) == "" {
		return false, errors.New("J3b run-now recovery lease lookup input is invalid")
	}
	var one int
	err := s.Business.QueryRowContext(ctx, s.bind(`SELECT 1 FROM `+s.table("account_quality_enforcements")+` WHERE account_id=? AND state='active' AND recovery_lease_until IS NOT NULL AND recovery_lease_until>? LIMIT 1`), accountID, now.UTC().Format(time.RFC3339Nano)).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read J3b run-now recovery lease: %w", err)
	}
	return true, nil
}

// ErrRunNowCompletionStale 表示 run-now 完成回写时计划 revision 已漂移
// （执行期间计划被编辑）。回写按快照 revision CAS 跳过，不覆盖新配置。
var ErrRunNowCompletionStale = errors.New("J3b run-now completion revision is stale")

// CompleteRunNow 写回 run-now 的 last_run_* 终值：已启用计划推进
// next_run_at=完成时间+间隔，已暂停计划不改 next_run_at。与 CompleteScheduled
// 不同，run-now 没有领取租约，因此只按 revision CAS，不动 lease 字段。
func (s *BusinessSchedulerSource) CompleteRunNow(ctx context.Context, scheduleID string, revision int, enabled bool, intervalMinutes int, runID, status string, completedAt time.Time) error {
	if s == nil || s.Business == nil || strings.TrimSpace(scheduleID) == "" || revision < 1 || intervalMinutes < 1 || completedAt.IsZero() {
		return errors.New("J3b run-now completion input is invalid")
	}
	if status != "completed" && status != "failed" && status != "canceled" {
		status = "failed"
	}
	now := completedAt.UTC()
	var query string
	var args []any
	if enabled {
		query = `UPDATE ` + s.table("model_quality_schedules") + ` SET last_run_id=?,last_run_at=?,last_run_status=?,next_run_at=?,updated_at=? WHERE id=? AND revision=?`
		args = []any{nullable(runID), now.Format(time.RFC3339Nano), status, now.Add(time.Duration(intervalMinutes) * time.Minute).Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), scheduleID, revision}
	} else {
		query = `UPDATE ` + s.table("model_quality_schedules") + ` SET last_run_id=?,last_run_at=?,last_run_status=?,updated_at=? WHERE id=? AND revision=?`
		args = []any{nullable(runID), now.Format(time.RFC3339Nano), status, now.Format(time.RFC3339Nano), scheduleID, revision}
	}
	res, err := s.Business.ExecContext(ctx, s.bind(query), args...)
	if err != nil {
		return fmt.Errorf("complete J3b run-now schedule: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrRunNowCompletionStale
	}
	return nil
}

func (s *BusinessSchedulerSource) table(name string) string {
	if s.Postgres {
		return "juhe_business." + name
	}
	return name
}
func (s *BusinessSchedulerSource) bind(text string) string {
	if !s.Postgres {
		return text
	}
	var b strings.Builder
	index := 0
	for _, r := range text {
		if r == '?' {
			index++
			fmt.Fprintf(&b, "$%d", index)
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}
func probeSetForProfile(profile string) string {
	if profile == "full" {
		return modelcheckprofile.ProbeSetVersion
	}
	return modelcheckprofile.QuickProbeSetVersion
}

var _ SchedulerSource = (*BusinessSchedulerSource)(nil)
var _ SchedulerLifecycle = (*BusinessSchedulerSource)(nil)

// ---------------------------------------------------------------------------
// 计划立即执行（run-now，D3/D4）
// ---------------------------------------------------------------------------

// run-now 受理结果状态机：单条接口把 not_found/stale_revision/already_running
// 分别映射为 404/409（版本过期）/409（冲突）；批量接口恒 200 逐条透出。
const (
	ScheduleRunNowStarted        = "started"
	ScheduleRunNowAlreadyRunning = "already_running"
	ScheduleRunNowNotFound       = "not_found"
	ScheduleRunNowStaleRevision  = "stale_revision"
	ScheduleRunNowInvalid        = "invalid"
)

// scheduleRunNowIdempotencyTTL 是 (scheduleId, requestId) 幂等记录的保留
// 窗口：窗口内同键重放返回上次受理结果。进程内实现，重启丢失可接受。
const scheduleRunNowIdempotencyTTL = 10 * time.Minute

// scheduleRunNowRequestIDMax 约束 requestId 长度，防止无限键注入进程内缓存。
const scheduleRunNowRequestIDMax = 128

// ScheduleRunNowOutcome 是一次受理的可观察结果（单条响应体 / 批量条目共用）。
type ScheduleRunNowOutcome struct {
	ScheduleID string
	Accepted   bool
	Status     string
	RunID      *string
	Message    string
	// Active 是 already_running 冲突时的在跑摘要（单条 409 载荷用），可为 nil。
	Active *modelcheckactive.Summary
}

// ScheduleRunNowService 受理"计划立即执行"并异步执行（D3）：
// 受理 = 幂等回放 -> 计划现值快照 -> revision 校验 -> 互斥检查 -> 占住
// modelcheckactive 的 target 槽（与手动检测互斥；不占 actor 槽，批量自锁）。
// 执行 = context.Background()+预算内 goroutine：构建、运行、完成回写
// （revision CAS、暂停计划不改 next_run_at）、达标恢复该计划造成的处罚（D4）。
type ScheduleRunNowService struct {
	Source  *BusinessSchedulerSource
	Runtime SchedulerRunner
	// Build 把受理快照解析为完整 RunRequest（Resolve 语义 + revision 栅栏），
	// 由组合根注入（与调度 Build 同源）。
	Build func(context.Context, ScheduleRunNowSnapshot) (RunRequest, error)
	// Restore 是 D4 恢复端口；nil 表示该部署不启用立即复测恢复。
	Restore SchedulePenaltyRestorer
	// Store 提供 CreateFailedRun（构建失败可查记录）与 MergeRunQualityDecision
	// （恢复结果回写）；nil 时相应增强关闭，核心受理/执行不受影响。
	Store  *Store
	Active *modelcheckactive.Registry
	Now    func() time.Time
	// RunBudget 限制一次异步执行的墙钟时长；0 用默认 10 分钟。
	RunBudget time.Duration
	// RunIDWait 是单条受理等待 OnStarted 回调拿 runId 的窗口；0 用默认 3s。
	RunIDWait time.Duration

	idempotency sync.Map // scheduleID + "\x1f" + requestID -> scheduleRunNowIdempotencyEntry
	mu          sync.Mutex
	inflight    map[string]string // scheduleID -> accountID
}

type scheduleRunNowIdempotencyEntry struct {
	outcome   ScheduleRunNowOutcome
	expiresAt time.Time
}

// SchedulePenaltyRestorer 是 D4 恢复端口；*BusinessRecoveryApplier 实现它。
type SchedulePenaltyRestorer interface {
	RestoreSchedulePenalties(context.Context, SchedulePenaltyRestoreInput) (SchedulePenaltyRestoreOutcome, error)
}

func (s *ScheduleRunNowService) now() time.Time {
	if s != nil && s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s *ScheduleRunNowService) initialized() bool {
	return s != nil && s.Source != nil && s.Runtime != nil && s.Build != nil && s.Active != nil
}

func scheduleRunNowTargetKey(accountID string) string {
	return "model-check-target:account:" + strings.TrimSpace(accountID)
}

// RunNow 受理单条立即执行：等待至多 RunIDWait 拿 runId（拿不到返回 null）。
func (s *ScheduleRunNowService) RunNow(ctx context.Context, systemID, scheduleID string, revision *int, requestID string) (ScheduleRunNowOutcome, error) {
	if !s.initialized() {
		return ScheduleRunNowOutcome{}, errors.New("J3b run-now service is not initialized")
	}
	runID := make(chan string, 1)
	outcome, err := s.accept(ctx, systemID, scheduleID, revision, requestID, runID)
	if err != nil || !outcome.Accepted {
		return outcome, err
	}
	wait := s.RunIDWait
	if wait <= 0 {
		wait = 3 * time.Second
	}
	select {
	case id := <-runID:
		outcome.RunID = &id
	case <-time.After(wait):
	}
	s.rememberRequest(scheduleRunNowIdempotencyKey(outcome.ScheduleID, requestID), outcome, s.now(), true)
	return outcome, nil
}

// RunNowBatch 受理批量立即执行：逐条独立受理互不影响，共享一个短暂的
// runId 收集窗口后统一返回（未取得的条目 runId=null，合法）。
func (s *ScheduleRunNowService) RunNowBatch(ctx context.Context, systemID string, items []ScheduleRunNowBatchCommand) ([]ScheduleRunNowOutcome, error) {
	if !s.initialized() {
		return nil, errors.New("J3b run-now service is not initialized")
	}
	results := make([]ScheduleRunNowOutcome, 0, len(items))
	channels := make([]chan string, 0, len(items))
	index := make([]int, 0, len(items))
	for _, item := range items {
		runID := make(chan string, 1)
		outcome, err := s.accept(ctx, systemID, item.ScheduleID, item.Revision, item.RequestID, runID)
		if err != nil {
			return nil, err
		}
		results = append(results, outcome)
		if outcome.Accepted {
			channels = append(channels, runID)
			index = append(index, len(results)-1)
		}
	}
	if len(channels) > 0 {
		deadline := time.After(time.Second)
	collect:
		for {
			for i, ch := range channels {
				if ch == nil {
					continue
				}
				select {
				case id := <-ch:
					results[index[i]].RunID = &id
					channels[i] = nil
				default:
				}
			}
			pending := false
			for _, ch := range channels {
				if ch != nil {
					pending = true
					break
				}
			}
			if !pending {
				break collect
			}
			select {
			case <-deadline:
				break collect
			case <-time.After(20 * time.Millisecond):
			}
		}
	}
	now := s.now()
	for i, item := range items {
		s.rememberRequest(scheduleRunNowIdempotencyKey(results[i].ScheduleID, item.RequestID), results[i], now, true)
	}
	return results, nil
}

// ScheduleRunNowBatchCommand 是批量受理的单条输入。
type ScheduleRunNowBatchCommand struct {
	ScheduleID string
	Revision   *int
	RequestID  string
}

func scheduleRunNowIdempotencyKey(scheduleID, requestID string) string {
	return strings.TrimSpace(scheduleID) + "\x1f" + strings.TrimSpace(requestID)
}

// rememberRequest 写幂等缓存；update=true 时允许覆盖同键（accept 已记录
// 最终结果，RunNow/RunNowBatch 拿到 runId 后再覆盖为带 runId 的终值）。
func (s *ScheduleRunNowService) rememberRequest(key string, outcome ScheduleRunNowOutcome, now time.Time, update bool) {
	if key == "" || strings.HasSuffix(key, "\x1f") {
		return
	}
	if !update {
		if _, loaded := s.idempotency.LoadOrStore(key, &scheduleRunNowIdempotencyEntry{outcome: outcome, expiresAt: now.Add(scheduleRunNowIdempotencyTTL)}); loaded {
			return
		}
	} else {
		s.idempotency.Store(key, &scheduleRunNowIdempotencyEntry{outcome: outcome, expiresAt: now.Add(scheduleRunNowIdempotencyTTL)})
	}
	s.pruneIdempotency(now)
}

func (s *ScheduleRunNowService) recallRequest(key string, now time.Time) (ScheduleRunNowOutcome, bool) {
	if key == "" || strings.HasSuffix(key, "\x1f") {
		return ScheduleRunNowOutcome{}, false
	}
	value, ok := s.idempotency.Load(key)
	if !ok {
		return ScheduleRunNowOutcome{}, false
	}
	entry, ok := value.(*scheduleRunNowIdempotencyEntry)
	if !ok || now.After(entry.expiresAt) {
		s.idempotency.Delete(key)
		return ScheduleRunNowOutcome{}, false
	}
	return entry.outcome, true
}

func (s *ScheduleRunNowService) pruneIdempotency(now time.Time) {
	s.idempotency.Range(func(key, value any) bool {
		if entry, ok := value.(*scheduleRunNowIdempotencyEntry); ok && now.After(entry.expiresAt) {
			s.idempotency.Delete(key)
		}
		return true
	})
}

// accept 执行单条受理流程并落幂等缓存：runID 是缓冲 1 的 OnStarted 通道
// （受理失败时不消耗）。幂等缓存只记录**最终结果**（受理或拒绝都记录，
// 重放返回相同结果）；同键并发重放的去重由 inflight 表承担（同计划在途
// -> already_running）。返回的 outcome 已包含 HTTP 映射所需的全部信息。
func (s *ScheduleRunNowService) accept(ctx context.Context, systemID, scheduleID string, revision *int, requestID string, runID chan string) (ScheduleRunNowOutcome, error) {
	outcome, err := s.acceptOnce(ctx, systemID, scheduleID, revision, requestID, runID)
	if err == nil && strings.TrimSpace(requestID) != "" {
		s.rememberRequest(scheduleRunNowIdempotencyKey(outcome.ScheduleID, strings.TrimSpace(requestID)), outcome, s.now(), true)
	}
	return outcome, err
}

// acceptOnce 是受理的判定主体（不含幂等落缓存）：先查幂等回放，再依次
// 走快照/revision/互斥/占槽；任何拒绝路径直接返回终态 outcome。
func (s *ScheduleRunNowService) acceptOnce(ctx context.Context, systemID, scheduleID string, revision *int, requestID string, runID chan string) (ScheduleRunNowOutcome, error) {
	scheduleID = strings.TrimSpace(scheduleID)
	requestID = strings.TrimSpace(requestID)
	now := s.now()
	if scheduleID == "" {
		return ScheduleRunNowOutcome{ScheduleID: scheduleID, Status: ScheduleRunNowInvalid, Message: "scheduleId 不能为空"}, nil
	}
	if len(requestID) > scheduleRunNowRequestIDMax {
		return ScheduleRunNowOutcome{ScheduleID: scheduleID, Status: ScheduleRunNowInvalid, Message: "requestId 过长"}, nil
	}
	if revision != nil && *revision < 1 {
		return ScheduleRunNowOutcome{ScheduleID: scheduleID, Status: ScheduleRunNowInvalid, Message: "revision 无效"}, nil
	}
	outcome := ScheduleRunNowOutcome{ScheduleID: scheduleID}
	if requestID != "" {
		if cached, ok := s.recallRequest(scheduleRunNowIdempotencyKey(scheduleID, requestID), now); ok {
			cached.ScheduleID = scheduleID
			return cached, nil
		}
	}
	snapshot, found, err := s.Source.ScheduleForRunNow(ctx, strings.TrimSpace(systemID), scheduleID)
	if err != nil {
		return outcome, err
	}
	if !found {
		outcome.Status, outcome.Message = ScheduleRunNowNotFound, "定时检查配置不存在"
		return outcome, nil
	}
	if revision != nil && *revision != snapshot.Revision {
		outcome.Status, outcome.Message = ScheduleRunNowStaleRevision, "定时检查配置已变化，请刷新后重试"
		return outcome, nil
	}
	if until, parseErr := time.Parse(time.RFC3339Nano, strings.TrimSpace(snapshot.LeaseUntil)); parseErr == nil && until.After(now) {
		outcome.Status, outcome.Message = ScheduleRunNowAlreadyRunning, "该计划的定时检查正在执行"
		return outcome, nil
	}
	activeLease, err := s.Source.AccountRecoveryLeaseActive(ctx, snapshot.AccountID, now)
	if err != nil {
		return outcome, err
	}
	if activeLease {
		outcome.Status, outcome.Message = ScheduleRunNowAlreadyRunning, "该账户正在执行恢复检测"
		return outcome, nil
	}
	targetKey := scheduleRunNowTargetKey(snapshot.AccountID)
	if summary, running := s.Active.Get(targetKey); running {
		outcome.Status, outcome.Message = ScheduleRunNowAlreadyRunning, "该账户正在执行模型检测"
		outcome.Active = &summary
		return outcome, nil
	}
	if conflict := s.markInflight(snapshot.ScheduleID, snapshot.AccountID); conflict != "" {
		outcome.Status, outcome.Message = ScheduleRunNowAlreadyRunning, "该计划立即执行正在运行中"
		if summary, running := s.Active.Get(targetKey); running {
			outcome.Active = &summary
		}
		return outcome, nil
	}
	summary := modelcheckactive.Summary{TargetID: snapshot.AccountID, Model: snapshot.Model, Profile: snapshot.Profile, StartedAt: now}
	handle, acquired, current := s.Active.TryStart(ctx, targetKey, summary)
	if !acquired {
		s.clearInflight(snapshot.ScheduleID)
		outcome.Status, outcome.Message = ScheduleRunNowAlreadyRunning, "该账户正在执行模型检测"
		outcome.Active = &current
		return outcome, nil
	}
	outcome.Accepted = true
	outcome.Status = ScheduleRunNowStarted
	release := func() {
		handle.Finish()
		s.clearInflight(snapshot.ScheduleID)
	}
	go s.execute(snapshot, runID, release)
	return outcome, nil
}

// markInflight 记录 schedule->account 在途映射；同计划或同账户已有在途时
// 返回冲突键（不记录）。
func (s *ScheduleRunNowService) markInflight(scheduleID, accountID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.inflight == nil {
		s.inflight = map[string]string{}
	}
	if _, exists := s.inflight[scheduleID]; exists {
		return scheduleID
	}
	for key, account := range s.inflight {
		if account == accountID {
			return key
		}
	}
	s.inflight[scheduleID] = accountID
	return ""
}

func (s *ScheduleRunNowService) clearInflight(scheduleID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.inflight, scheduleID)
}

// execute 在独立 goroutine 中执行一次 run-now：构建（失败落可查 failed run）
// -> 运行 -> 完成回写（暂停计划不改 next_run_at）-> 达标恢复（D4）-> 释放槽。
// 生命周期不绑定 HTTP 请求：context.Background()+RunBudget。
func (s *ScheduleRunNowService) execute(snapshot ScheduleRunNowSnapshot, runID chan string, release func()) {
	budget := s.RunBudget
	if budget <= 0 {
		budget = 10 * time.Minute
	}
	runCtx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	settled := false
	finalRunID, finalStatus := "", string(RunFailed)
	settle := func() {
		if settled {
			return
		}
		settled = true
		s.settleRunNow(snapshot, finalRunID, finalStatus)
		release()
	}
	defer func() { settle() }()
	defer safego.Handle("modelcheckowner.ScheduleRunNowService.execute", func(recovered any) {
		slog.Error("J3b run-now 执行异常终止", "scheduleId", snapshot.ScheduleID, "panic", fmt.Sprint(recovered))
	})
	request, buildErr := s.Build(runCtx, snapshot)
	if buildErr != nil {
		if s.Store != nil {
			recordCtx, recordCancel := context.WithTimeout(context.Background(), 10*time.Second)
			finishedAt := s.now()
			record := snapshot.failureRunRecord(newID("run"), buildErr)
			if err := s.Store.CreateFailedRun(recordCtx, record, "model_check_build_failed", buildErr.Error(), finishedAt); err != nil {
				slog.Error("J3b run-now 构建失败记录写入失败", "scheduleId", snapshot.ScheduleID, "err", err.Error())
			} else {
				finalRunID = record.ID
			}
			recordCancel()
		}
		finalStatus = string(RunFailed)
		slog.Warn("J3b run-now 构建失败", "scheduleId", snapshot.ScheduleID, "err", buildErr.Error())
		return
	}
	// 计划行现值覆盖（与调度 executor.Execute 同语义）：受理快照是本次
	// 执行的唯一事实来源，Build 只补目标解析结果（endpoint/revision 等）。
	request.TriggerKind = TriggerKindScheduleNow
	request.ScheduleID = snapshot.ScheduleID
	request.SystemAccountID = snapshot.SystemAccountID
	request.ActorSystemAccountID = snapshot.SystemAccountID
	request.TargetType = "account"
	request.TargetID = snapshot.AccountID
	request.Model = snapshot.Model
	request.Profile = snapshot.Profile
	request.ProviderCode = snapshot.ProviderCode
	request.Threshold = snapshot.Threshold
	request.PenaltyAction = snapshot.PenaltyAction
	request.RecoveryIntervalMinutes = snapshot.RecoveryIntervalMinutes
	request.PolicyRevision = strconv.Itoa(snapshot.Revision)
	request.ProbeSetVersion = probeSetForProfile(snapshot.Profile)
	request.IdentityKey = snapshot.SystemAccountID + ":" + snapshot.AccountID + ":" + snapshot.Model
	request.CustomQuestionIds = snapshot.CustomQuestionIds
	request.OnStarted = func(id string) {
		select {
		case runID <- id:
		default:
		}
	}
	result, runErr := s.Runtime.Run(runCtx, request)
	finalStatus = string(RunFailed)
	if runErr == nil && (result.Status == string(RunCompleted) || result.Status == string(RunCanceled)) {
		finalStatus = result.Status
	}
	finalRunID = result.RunID
	finishedAt := s.now()
	// D4：仅 schedule_now 且完成的 run 走立即复测恢复；quick/full 的达标
	// 门槛复用调度恢复的同一判定。
	if runErr == nil && result.Status == string(RunCompleted) && s.Restore != nil && runResultRecoveryEligible(result, snapshot.Threshold, snapshot.Profile) {
		restore, restoreErr := s.Restore.RestoreSchedulePenalties(runCtx, SchedulePenaltyRestoreInput{AccountID: snapshot.AccountID, SystemAccountID: snapshot.SystemAccountID, ScheduleID: snapshot.ScheduleID, RunID: result.RunID, OccurredAt: finishedAt})
		if restoreErr != nil {
			slog.Error("J3b run-now 处罚恢复执行失败", "scheduleId", snapshot.ScheduleID, "err", restoreErr.Error())
		}
		s.mergeRestoreDecision(result.RunID, restore, restoreErr)
	}
}

// settleRunNow 完成回写：已启用计划推进 next_run_at=完成时间+间隔，暂停
// 计划保持 next_run_at 不变；revision CAS 失败（执行期间计划被编辑）时跳过
// 并记录，不覆盖新配置。写回使用独立超时上下文，不受执行预算取消影响。
func (s *ScheduleRunNowService) settleRunNow(snapshot ScheduleRunNowSnapshot, runID, status string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.Source.CompleteRunNow(ctx, snapshot.ScheduleID, snapshot.Revision, snapshot.Enabled, snapshot.IntervalMinutes, runID, status, s.now()); err != nil {
		if errors.Is(err, ErrRunNowCompletionStale) {
			slog.Warn("J3b run-now 完成回写跳过：计划在执行期间被修改", "scheduleId", snapshot.ScheduleID)
			return
		}
		slog.Error("J3b run-now 完成回写失败", "scheduleId", snapshot.ScheduleID, "err", err.Error())
	}
}

// mergeRestoreDecision 把 D4 恢复结果回写 run 的 quality_decision：补
// message 与恢复 reason code（schedule_now_restore_applied /
// schedule_now_restore_skipped_cas_mismatch / schedule_now_restore_failed）。
// 独立 restoreReasonCodes 键追加，不覆盖 finalize 已写的 trust reasonCodes。
func (s *ScheduleRunNowService) mergeRestoreDecision(runID string, outcome SchedulePenaltyRestoreOutcome, err error) {
	if s.Store == nil || strings.TrimSpace(runID) == "" {
		return
	}
	code, message := "", ""
	switch {
	case err != nil:
		code, message = "schedule_now_restore_failed", "立即复测恢复执行失败: "+err.Error()
	case outcome.Restored > 0:
		code, message = "schedule_now_restore_applied", "立即复测达标，已恢复该计划造成的处罚"
	case outcome.Skipped > 0:
		code, message = "schedule_now_restore_skipped_cas_mismatch", "立即复测达标，但处罚恢复被版本校验跳过"
	default:
		return
	}
	patch := map[string]any{
		"restoreReasonCodes": []string{code},
		"scheduleNowRestore": map[string]any{"code": code, "restored": outcome.Restored, "skipped": outcome.Skipped},
	}
	if message != "" {
		patch["message"] = message
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Store.MergeRunQualityDecision(ctx, runID, patch); err != nil {
		slog.Error("J3b run-now 恢复结果回写失败", "runId", runID, "err", err.Error())
	}
}

// failureRunRecord 把受理快照与构建失败原因冻结成 failed run 记录（D6 同
// 语义：last_run_id 必须指向可查详情）。
func (snapshot ScheduleRunNowSnapshot) failureRunRecord(runID string, cause error) RunRecord {
	summary := map[string]any{"targetType": "account", "targetId": snapshot.AccountID, "model": snapshot.Model, "profile": snapshot.Profile, "configRevision": strconv.Itoa(snapshot.ConfigRevision), "policyRevision": strconv.Itoa(snapshot.Revision), "scheduleId": snapshot.ScheduleID}
	policy := map[string]any{"revision": strconv.Itoa(snapshot.Revision), "threshold": snapshot.Threshold, "action": snapshot.PenaltyAction, "recoveryIntervalMinutes": snapshot.RecoveryIntervalMinutes}
	requestSummary, _ := json.Marshal(summary)
	policySnapshot, _ := json.Marshal(policy)
	provider := snapshot.ProviderCode
	if strings.TrimSpace(provider) == "" {
		provider = "unknown"
	}
	return RunRecord{ID: runID, SystemAccountID: snapshot.SystemAccountID, ActorSystemAccountID: snapshot.SystemAccountID, ProviderCode: provider, TargetType: "account", TargetID: snapshot.AccountID, AccountID: snapshot.AccountID, Model: snapshot.Model, Profile: snapshot.Profile, TriggerKind: TriggerKindScheduleNow, ScheduleID: snapshot.ScheduleID, ProbeSetVersion: probeSetForProfile(snapshot.Profile), RequestSummary: requestSummary, PolicySnapshot: policySnapshot, StartedAt: time.Now().UTC()}
}

var _ SchedulePenaltyRestorer = (*BusinessRecoveryApplier)(nil)
