package modelcheckquality

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

type EnforcementInput struct {
	SystemAccountID, AccountID, RunID, Action, ScheduleID, Model, Profile, Message   string
	PolicyRevision, PenaltyThreshold, RecoveryIntervalMinutes, AccountConfigRevision int
	DecidedAt                                                                        time.Time
}

type EnforcementResult struct {
	Result, BeforeStatus, AfterStatus, EnforcementID, Message string
	Generation                                                int
	RecoveryDueAt                                             *time.Time
}

// ApplyEnforcement is the Go-owned business CAS boundary. Callers must only
// invoke it after a formed quality decision; it never calls Node or another
// process and is safe to replay with the same run ID.
func ApplyEnforcement(ctx context.Context, db *sql.DB, postgres bool, input EnforcementInput) (EnforcementResult, error) {
	if db == nil || strings.TrimSpace(input.SystemAccountID) == "" || strings.TrimSpace(input.AccountID) == "" || strings.TrimSpace(input.RunID) == "" || input.DecidedAt.IsZero() || input.PolicyRevision < 0 || input.AccountConfigRevision < 1 || input.PenaltyThreshold < 40 || input.PenaltyThreshold > 100 || input.RecoveryIntervalMinutes < 10 || (input.Action != "disable" && input.Action != "fallback" && input.Action != "quality_isolate") || (input.Profile != "quick" && input.Profile != "full") {
		return EnforcementResult{}, errors.New("invalid quality enforcement input")
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return EnforcementResult{}, fmt.Errorf("begin quality enforcement: %w", err)
	}
	defer tx.Rollback()
	q := func(sqlText string) string { return businessSQL(sqlText, postgres) }
	matched, err := enforcementConfigurationMatches(ctx, tx, q, input)
	if err != nil {
		return EnforcementResult{}, err
	}
	if !matched {
		return EnforcementResult{Result: "stale", Message: "检测配置已变化，本次仅保留质量事实，不修改账户"}, tx.Commit()
	}
	var status, existingSystem string
	var revision, fallback, superPriority int
	var authorizationInstance sql.NullString
	accountSQL := `SELECT status,system_account_id,config_revision,fallback_enabled,super_priority_enabled,authorization_instance_authorization_id FROM accounts WHERE id=? AND deleted_at IS NULL`
	if postgres {
		accountSQL += " FOR UPDATE"
	}
	if err := tx.QueryRowContext(ctx, q(accountSQL), input.AccountID).Scan(&status, &existingSystem, &revision, &fallback, &superPriority, &authorizationInstance); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return EnforcementResult{Result: "skipped", Message: "账户不存在或已删除"}, tx.Commit()
		}
		return EnforcementResult{}, err
	}
	if existingSystem != input.SystemAccountID {
		return EnforcementResult{Result: "skipped", BeforeStatus: status, AfterStatus: status, Message: "账户不属于当前系统账户"}, tx.Commit()
	}
	if authorizationInstance.Valid {
		return EnforcementResult{Result: "skipped", BeforeStatus: status, AfterStatus: status, Message: "授权实例不是可处罚的自有账户"}, tx.Commit()
	}
	if revision != input.AccountConfigRevision {
		return EnforcementResult{Result: "stale", BeforeStatus: status, AfterStatus: status, Message: "账户配置已变化，本次处罚已跳过"}, tx.Commit()
	}
	if input.Action == "fallback" && status != "active" || input.Action != "fallback" && status != "active" && !(input.Action == "disable" && status == "quality_isolated") {
		return EnforcementResult{Result: "skipped", BeforeStatus: status, AfterStatus: status, Message: "账户当前状态不允许质量处罚"}, tx.Commit()
	}
	var priorID string
	var priorGeneration int
	var priorState, priorRun string
	err = tx.QueryRowContext(ctx, q(`SELECT enforcement_id,generation,state,trigger_run_id FROM account_quality_enforcements WHERE account_id=?`), input.AccountID).Scan(&priorID, &priorGeneration, &priorState, &priorRun)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return EnforcementResult{}, err
	}
	if err == nil && priorRun == input.RunID && priorState == "active" {
		return EnforcementResult{Result: "already_effective", BeforeStatus: status, AfterStatus: status, EnforcementID: priorID, Generation: priorGeneration, Message: "本次处罚已生效，无需重复执行"}, tx.Commit()
	}
	targetStatus := status
	if input.Action == "disable" {
		targetStatus = "disabled"
	} else if input.Action == "quality_isolate" {
		targetStatus = "quality_isolated"
	}
	recovery := ""
	if input.Action == "quality_isolate" {
		recovery = input.DecidedAt.UTC().Add(time.Duration(input.RecoveryIntervalMinutes) * time.Minute).Format(time.RFC3339Nano)
	}
	if !(input.Action == "fallback" && fallback == 1) && !(input.Action != "fallback" && status == targetStatus) {
		res, err := tx.ExecContext(ctx, q(`UPDATE accounts SET status=?,schedulable=CASE WHEN ? IN ('disable','quality_isolate') THEN 0 ELSE schedulable END,fallback_enabled=CASE WHEN ?='fallback' THEN 1 ELSE fallback_enabled END,super_priority_enabled=CASE WHEN ?='fallback' THEN 0 ELSE super_priority_enabled END,last_error_code='model_quality_failed',last_error_message=?,config_revision=config_revision+1,updated_at=? WHERE id=? AND config_revision=?`), targetStatus, input.Action, input.Action, input.Action, truncate(input.Message, 1000), input.DecidedAt.UTC().Format(time.RFC3339Nano), input.AccountID, input.AccountConfigRevision)
		if err != nil {
			return EnforcementResult{}, err
		}
		n, _ := res.RowsAffected()
		if n != 1 {
			return EnforcementResult{Result: "stale", BeforeStatus: status, AfterStatus: status, Message: "账户在处罚提交前已变化"}, tx.Commit()
		}
	}
	enforcementID, err := newEnforcementID()
	if err != nil {
		return EnforcementResult{}, err
	}
	generation := priorGeneration + 1
	_, err = tx.ExecContext(ctx, q(`INSERT INTO account_quality_enforcements(account_id,system_account_id,enforcement_id,generation,state,action,trigger_run_id,config_source,config_source_id,policy_revision,profile,penalty_threshold,recovery_interval_minutes,recovery_model,account_config_revision,before_status,after_status,fallback_was_enabled,super_priority_was_enabled,started_at,recovery_due_at,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(account_id) DO UPDATE SET enforcement_id=excluded.enforcement_id,generation=excluded.generation,state='active',action=excluded.action,trigger_run_id=excluded.trigger_run_id,config_source=excluded.config_source,config_source_id=excluded.config_source_id,policy_revision=excluded.policy_revision,profile=excluded.profile,penalty_threshold=excluded.penalty_threshold,recovery_interval_minutes=excluded.recovery_interval_minutes,recovery_model=excluded.recovery_model,account_config_revision=excluded.account_config_revision,before_status=excluded.before_status,after_status=excluded.after_status,recovery_due_at=excluded.recovery_due_at,updated_at=excluded.updated_at`), input.AccountID, input.SystemAccountID, enforcementID, generation, "active", input.Action, input.RunID, source(input.ScheduleID), nullable(input.ScheduleID), input.PolicyRevision, input.Profile, input.PenaltyThreshold, input.RecoveryIntervalMinutes, input.Model, input.AccountConfigRevision, status, targetStatus, fallback, superPriority, input.DecidedAt.UTC().Format(time.RFC3339Nano), nullable(recovery), input.DecidedAt.UTC().Format(time.RFC3339Nano), input.DecidedAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return EnforcementResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return EnforcementResult{}, err
	}
	result := EnforcementResult{Result: "applied", BeforeStatus: status, AfterStatus: targetStatus, EnforcementID: enforcementID, Generation: generation, Message: input.Message}
	if recovery != "" {
		parsed, _ := time.Parse(time.RFC3339Nano, recovery)
		result.RecoveryDueAt = &parsed
	}
	return result, nil
}

func source(schedule string) string {
	if strings.TrimSpace(schedule) != "" {
		return "schedule"
	}
	return "manual"
}

func enforcementConfigurationMatches(ctx context.Context, tx *sql.Tx, q func(string) string, input EnforcementInput) (bool, error) {
	if strings.TrimSpace(input.ScheduleID) != "" {
		var revision, threshold, recovery int
		var profile, action, model string
		err := tx.QueryRowContext(ctx, q(`SELECT revision,profile,penalty_threshold,penalty_action,recovery_interval_minutes,model FROM model_quality_schedules WHERE id=? AND system_account_id=? AND account_id=?`), input.ScheduleID, input.SystemAccountID, input.AccountID).Scan(&revision, &profile, &threshold, &action, &recovery, &model)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		return revision == input.PolicyRevision && profile == input.Profile && threshold == input.PenaltyThreshold && action == input.Action && recovery == input.RecoveryIntervalMinutes && model == input.Model, nil
	}
	var revision, threshold, recovery int
	var profile, action string
	err := tx.QueryRowContext(ctx, q(`SELECT revision,profile,penalty_threshold,penalty_action,recovery_interval_minutes FROM model_quality_policies WHERE system_account_id=?`), input.SystemAccountID).Scan(&revision, &profile, &threshold, &action, &recovery)
	if errors.Is(err, sql.ErrNoRows) {
		return input.PolicyRevision == 0 && input.Profile == "quick" && input.PenaltyThreshold == 70 && input.Action == "fallback" && input.RecoveryIntervalMinutes == 10, nil
	}
	if err != nil {
		return false, err
	}
	return revision == input.PolicyRevision && profile == input.Profile && threshold == input.PenaltyThreshold && action == input.Action && recovery == input.RecoveryIntervalMinutes, nil
}
func nullable(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}
func truncate(value string, max int) string {
	runes := []rune(value)
	if len(runes) > max {
		return string(runes[:max])
	}
	return value
}
func bind(query string, postgres bool) string {
	if !postgres {
		return query
	}
	for i := 1; strings.Contains(query, "?"); i++ {
		query = strings.Replace(query, "?", fmt.Sprintf("$%d", i), 1)
	}
	return query
}

func businessSQL(query string, postgres bool) string {
	if postgres {
		for _, table := range []string{"accounts", "model_quality_policies", "model_quality_schedules", "account_quality_enforcements"} {
			query = strings.ReplaceAll(query, table, "juhe_business."+table)
		}
	}
	return bind(query, postgres)
}

func newEnforcementID() (string, error) {
	bytes := make([]byte, 12)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("generate quality enforcement id: %w", err)
	}
	return "mqe-" + hex.EncodeToString(bytes), nil
}
