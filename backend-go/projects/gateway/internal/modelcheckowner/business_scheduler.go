package modelcheckowner

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckprofile"
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
		{"model_quality_schedules", "id,revision,system_account_id,account_id,model,interval_minutes,profile,penalty_threshold,penalty_action,enabled,next_run_at,lease_owner,lease_until,last_run_id,last_run_at,last_run_status,updated_at"},
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
	rows, err := tx.QueryContext(ctx, q(`SELECT mqs.id,mqs.revision,mqs.system_account_id,mqs.account_id,mqs.model,mqs.interval_minutes,mqs.profile,mqs.penalty_threshold,mqs.penalty_action,mqs.recovery_interval_minutes,a.config_revision,a.dispatch_revision,a.provider_code FROM `+s.table("model_quality_schedules")+` mqs JOIN `+s.table("accounts")+` a ON a.id=mqs.account_id WHERE mqs.enabled=1 AND mqs.next_run_at<=? AND (mqs.lease_until IS NULL OR mqs.lease_until<=?) AND a.deleted_at IS NULL AND a.authorization_instance_authorization_id IS NULL AND a.status='active' ORDER BY mqs.next_run_at,mqs.id LIMIT ?`+lock), now.UTC().Format(time.RFC3339Nano), now.UTC().Format(time.RFC3339Nano), limit)
	if err != nil {
		return nil, fmt.Errorf("claim J3b schedules: %w", err)
	}
	defer rows.Close()
	var tasks []ScheduleTask
	for rows.Next() {
		var id, systemID, accountID, model, profile, action, provider string
		var revision, interval, recoveryInterval, threshold, configRevision, dispatchRevision int
		if err := rows.Scan(&id, &revision, &systemID, &accountID, &model, &interval, &profile, &threshold, &action, &recoveryInterval, &configRevision, &dispatchRevision, &provider); err != nil {
			return nil, err
		}
		res, err := tx.ExecContext(ctx, q(`UPDATE `+s.table("model_quality_schedules")+` SET lease_owner=?,lease_until=?,updated_at=? WHERE id=? AND revision=? AND enabled=1 AND (lease_until IS NULL OR lease_until<=?)`), s.OwnerID, now.Add(lease).UTC().Format(time.RFC3339Nano), now.UTC().Format(time.RFC3339Nano), id, revision, now.UTC().Format(time.RFC3339Nano))
		if err != nil {
			return nil, err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			continue
		}
		payload, err := json.Marshal(ScheduledPayload{SystemAccountID: systemID, ActorSystemAccountID: systemID, TargetType: "account", TargetID: accountID, Model: model, Profile: profile, ProviderCode: provider, Threshold: threshold, PenaltyAction: action, ConfigRevision: strconv.Itoa(configRevision), DispatchRevision: int64(dispatchRevision), SourceConfigRevision: strconv.Itoa(configRevision), SourceDispatchRevision: int64(dispatchRevision), PolicyRevision: strconv.Itoa(revision), ProbeSetVersion: probeSetForProfile(profile), IdentityKey: systemID + ":" + accountID + ":" + model, ScheduleID: id, OwnerID: s.OwnerID, ScheduleRevision: revision, IntervalMinutes: interval, RecoveryIntervalMinutes: recoveryInterval})
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
	rows, err := tx.QueryContext(ctx, q(`SELECT aqe.account_id,aqe.system_account_id,aqe.enforcement_id,aqe.generation,COALESCE(NULLIF(aqe.recovery_model,''),a.health_check_model),a.config_revision,a.dispatch_revision,COALESCE(sa.config_revision,a.config_revision),COALESCE(sa.dispatch_revision,a.dispatch_revision),aqe.policy_revision,COALESCE(aqe.config_source_id,''),aqe.profile,aqe.penalty_threshold,aqe.recovery_interval_minutes,a.provider_code FROM `+s.table("account_quality_enforcements")+` aqe JOIN `+s.table("accounts")+` a ON a.id=aqe.account_id LEFT JOIN `+s.table("accounts")+` sa ON sa.id=a.authorization_instance_source_account_id WHERE aqe.state='active' AND aqe.action='quality_isolate' AND aqe.recovery_due_at IS NOT NULL AND aqe.recovery_due_at<=? AND (aqe.recovery_lease_until IS NULL OR aqe.recovery_lease_until<=?) AND a.deleted_at IS NULL AND a.status='quality_isolated' ORDER BY aqe.recovery_due_at,aqe.account_id LIMIT ?`+lock), now.UTC().Format(time.RFC3339Nano), now.UTC().Format(time.RFC3339Nano), limit)
	if err != nil {
		return nil, fmt.Errorf("claim J3b recoveries: %w", err)
	}
	defer rows.Close()
	var tasks []ScheduleTask
	for rows.Next() {
		var accountID, systemID, enforcementID, model, scheduleID, profile, provider string
		var generation, configRevision, dispatchRevision, sourceConfigRevision, sourceDispatchRevision, policyRevision, threshold, interval int
		if err := rows.Scan(&accountID, &systemID, &enforcementID, &generation, &model, &configRevision, &dispatchRevision, &sourceConfigRevision, &sourceDispatchRevision, &policyRevision, &scheduleID, &profile, &threshold, &interval, &provider); err != nil {
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
		payload, err := json.Marshal(ScheduledPayload{SystemAccountID: systemID, ActorSystemAccountID: systemID, TargetType: "account", TargetID: accountID, Model: model, Profile: profile, ProviderCode: provider, Threshold: threshold, PenaltyAction: "quality_isolate", ConfigRevision: strconv.Itoa(configRevision), DispatchRevision: int64(dispatchRevision), SourceConfigRevision: strconv.Itoa(sourceConfigRevision), SourceDispatchRevision: int64(sourceDispatchRevision), PolicyRevision: strconv.Itoa(policyRevision), ProbeSetVersion: probeSetForProfile(profile), IdentityKey: systemID + ":" + accountID + ":" + model, ScheduleID: scheduleID, OwnerID: s.OwnerID, EnforcementID: enforcementID, Generation: generation, RecoveryIntervalMinutes: interval})
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, ScheduleTask{ID: "recovery:" + accountID + ":" + enforcementID + ":" + strconv.Itoa(generation), Kind: SchedulerQualityRecovery, OwnerID: s.OwnerID, Payload: payload})
	}
	return tasks, rows.Err()
}

func (s *BusinessSchedulerSource) CompleteScheduled(ctx context.Context, payload ScheduledPayload, result RunResult) error {
	if s == nil || s.Business == nil || strings.TrimSpace(payload.OwnerID) == "" || strings.TrimSpace(payload.ScheduleID) == "" || payload.ScheduleRevision < 1 || payload.IntervalMinutes < 10 {
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
