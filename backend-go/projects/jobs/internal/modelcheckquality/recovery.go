package modelcheckquality

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// RecoveryCandidate is the immutable snapshot handed to a recovery probe.
// The caller must execute the probe with AllowQualityIsolated=true and return
// the exact lease/generation values to CompleteRecovery.
type RecoveryCandidate struct {
	AccountID, SystemAccountID, EnforcementID, Model, ScheduleID string
	Generation, AccountConfigRevision, PolicyRevision            int
	Profile, Action                                              string
	PenaltyThreshold, RecoveryIntervalMinutes                    int
}

type RecoveryClaimInput struct {
	OwnerID string
	Now     time.Time
	Limit   int
	Lease   time.Duration
}

type RecoveryCompletionInput struct {
	OwnerID, AccountID, EnforcementID, RunID            string
	Generation, PolicyRevision, RecoveryIntervalMinutes int
	Passed                                              bool
	CompletedAt                                         time.Time
}

type RecoveryResult struct {
	Result, BeforeStatus, AfterStatus, Message string
	NextRecoveryAt                             *time.Time
}

// ClaimDueRecoveries atomically leases due quality-isolated accounts. It is
// deliberately a primitive; scheduler cadence/owner lease are mounted later.
func ClaimDueRecoveries(ctx context.Context, db *sql.DB, postgres bool, input RecoveryClaimInput) ([]RecoveryCandidate, error) {
	if db == nil || strings.TrimSpace(input.OwnerID) == "" || input.Now.IsZero() || input.Limit < 1 || input.Limit > 1000 || input.Lease <= 0 {
		return nil, errors.New("invalid recovery claim input")
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("begin recovery claim: %w", err)
	}
	defer tx.Rollback()
	q := func(s string) string { return businessSQL(s, postgres) }
	now := input.Now.UTC().Format(time.RFC3339Nano)
	until := input.Now.UTC().Add(input.Lease).Format(time.RFC3339Nano)
	query := `SELECT aqe.account_id,aqe.system_account_id,aqe.enforcement_id,aqe.generation,COALESCE(NULLIF(aqe.recovery_model,''),a.health_check_model),a.config_revision,aqe.policy_revision,aqe.config_source_id,aqe.profile,aqe.penalty_threshold,aqe.recovery_interval_minutes FROM account_quality_enforcements aqe JOIN accounts a ON a.id=aqe.account_id WHERE aqe.state='active' AND aqe.action='quality_isolate' AND aqe.recovery_due_at IS NOT NULL AND aqe.recovery_due_at<=? AND (aqe.recovery_lease_until IS NULL OR aqe.recovery_lease_until<=?) AND a.deleted_at IS NULL AND a.status='quality_isolated' ORDER BY aqe.recovery_due_at ASC,aqe.account_id ASC LIMIT ?`
	if postgres {
		query += " FOR UPDATE OF aqe SKIP LOCKED"
	}
	rows, err := tx.QueryContext(ctx, q(query), now, now, input.Limit)
	if err != nil {
		return nil, fmt.Errorf("query due recoveries: %w", err)
	}
	defer rows.Close()
	var out []RecoveryCandidate
	for rows.Next() {
		var c RecoveryCandidate
		var schedule sql.NullString
		if err := rows.Scan(&c.AccountID, &c.SystemAccountID, &c.EnforcementID, &c.Generation, &c.Model, &c.AccountConfigRevision, &c.PolicyRevision, &schedule, &c.Profile, &c.PenaltyThreshold, &c.RecoveryIntervalMinutes); err != nil {
			return nil, err
		}
		if schedule.Valid {
			c.ScheduleID = schedule.String
		}
		res, err := tx.ExecContext(ctx, q(`UPDATE account_quality_enforcements SET recovery_lease_owner=?,recovery_lease_until=?,account_config_revision=?,updated_at=? WHERE account_id=? AND enforcement_id=? AND generation=? AND state='active' AND action='quality_isolate' AND (recovery_lease_until IS NULL OR recovery_lease_until<=? )`), input.OwnerID, until, c.AccountConfigRevision, now, c.AccountID, c.EnforcementID, c.Generation, now)
		if err != nil {
			return nil, err
		}
		if n, _ := res.RowsAffected(); n == 1 {
			out = append(out, c)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

// CompleteRecovery mirrors Node's lease + generation + config CAS. A failed
// probe only reschedules; it never clears quality isolation.
func CompleteRecovery(ctx context.Context, db *sql.DB, postgres bool, input RecoveryCompletionInput) (RecoveryResult, error) {
	if db == nil || strings.TrimSpace(input.OwnerID) == "" || strings.TrimSpace(input.AccountID) == "" || strings.TrimSpace(input.EnforcementID) == "" || strings.TrimSpace(input.RunID) == "" || input.Generation < 1 || input.PolicyRevision < 0 || input.RecoveryIntervalMinutes < 10 || input.RecoveryIntervalMinutes > 10080 || input.CompletedAt.IsZero() {
		return RecoveryResult{}, errors.New("invalid recovery completion input")
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return RecoveryResult{}, err
	}
	defer tx.Rollback()
	q := func(s string) string { return businessSQL(s, postgres) }
	completed := input.CompletedAt.UTC()
	completedText := completed.Format(time.RFC3339Nano)
	var state, action, systemID string
	var accountRevision, policyRevision int
	enfQ := `SELECT state,action,system_account_id,account_config_revision,policy_revision FROM account_quality_enforcements WHERE account_id=? AND enforcement_id=? AND generation=? AND recovery_lease_owner=?`
	if postgres {
		enfQ += " FOR UPDATE"
	}
	if err := tx.QueryRowContext(ctx, q(enfQ), input.AccountID, input.EnforcementID, input.Generation, input.OwnerID).Scan(&state, &action, &systemID, &accountRevision, &policyRevision); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			_ = tx.Commit()
			return RecoveryResult{Result: "stale", Message: "质量隔离处罚代次或恢复租约已变化，本次恢复结果已忽略"}, nil
		}
		return RecoveryResult{}, err
	}
	if state != "active" || action != "quality_isolate" {
		_ = tx.Commit()
		return RecoveryResult{Result: "stale", Message: "质量隔离处罚代次或恢复租约已变化，本次恢复结果已忽略"}, nil
	}
	next := completed.Add(time.Duration(input.RecoveryIntervalMinutes) * time.Minute)
	reschedule := func() error {
		_, e := tx.ExecContext(ctx, q(`UPDATE account_quality_enforcements SET last_recovery_run_id=?,recovery_due_at=?,recovery_lease_owner=NULL,recovery_lease_until=NULL,updated_at=? WHERE account_id=? AND enforcement_id=? AND generation=? AND recovery_lease_owner=?`), input.RunID, next.Format(time.RFC3339Nano), completedText, input.AccountID, input.EnforcementID, input.Generation, input.OwnerID)
		return e
	}
	if policyRevision != input.PolicyRevision {
		if err := reschedule(); err != nil {
			return RecoveryResult{}, err
		}
		if err := tx.Commit(); err != nil {
			return RecoveryResult{}, err
		}
		return RecoveryResult{Result: "stale", NextRecoveryAt: &next, Message: "处罚配置快照已变化，本次不解除隔离并等待复检"}, nil
	}
	if !input.Passed {
		if err := reschedule(); err != nil {
			return RecoveryResult{}, err
		}
		if err := tx.Commit(); err != nil {
			return RecoveryResult{}, err
		}
		return RecoveryResult{Result: "kept_isolated", BeforeStatus: "quality_isolated", AfterStatus: "quality_isolated", NextRecoveryAt: &next, Message: "质量恢复检查未达标，账户继续隔离"}, nil
	}
	var status string
	var configRevision int
	var scheduleNull sql.NullString
	accQ := `SELECT status,config_revision,availability_schedule_json FROM accounts WHERE id=? AND system_account_id=? AND deleted_at IS NULL`
	if postgres {
		accQ += " FOR UPDATE"
	}
	if err := tx.QueryRowContext(ctx, q(accQ), input.AccountID, systemID).Scan(&status, &configRevision, &scheduleNull); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			_ = tx.Commit()
			return RecoveryResult{Result: "stale", Message: "账户已被用户或其他任务修改，本次恢复结果已忽略"}, nil
		}
		return RecoveryResult{}, err
	}
	if status != "quality_isolated" {
		_ = tx.Commit()
		return RecoveryResult{Result: "stale", BeforeStatus: status, AfterStatus: status, Message: "账户已被用户或其他任务修改，本次恢复结果已忽略"}, nil
	}
	if configRevision != accountRevision {
		if err := reschedule(); err != nil {
			return RecoveryResult{}, err
		}
		if err := tx.Commit(); err != nil {
			return RecoveryResult{}, err
		}
		return RecoveryResult{Result: "stale", BeforeStatus: status, AfterStatus: status, NextRecoveryAt: &next, Message: "账户配置在恢复检查期间发生变化，本次不解除隔离并等待重新复检"}, nil
	}
	scheduleJSON := ""
	if scheduleNull.Valid {
		scheduleJSON = scheduleNull.String
	}
	after := "active"
	allowed, err := availabilityAllowed(scheduleJSON, completed)
	if err != nil {
		return RecoveryResult{}, fmt.Errorf("evaluate account availability schedule: %w", err)
	}
	if !allowed {
		after = "disabled"
	}
	updated, err := tx.ExecContext(ctx, q(`UPDATE accounts SET status=?,schedulable=?,last_error_code=NULL,last_error_message=NULL,config_revision=config_revision+1,updated_at=? WHERE id=? AND status='quality_isolated'`), after, boolInt(after == "active"), completedText, input.AccountID)
	if err != nil {
		return RecoveryResult{}, err
	}
	if n, _ := updated.RowsAffected(); n != 1 {
		_ = tx.Commit()
		return RecoveryResult{Result: "stale", BeforeStatus: status, AfterStatus: status, Message: "账户状态在恢复提交前已变化"}, nil
	}
	if _, err = tx.ExecContext(ctx, q(`UPDATE account_quality_enforcements SET state='cleared',last_recovery_run_id=?,cleared_at=?,recovery_due_at=NULL,recovery_lease_owner=NULL,recovery_lease_until=NULL,updated_at=? WHERE account_id=? AND enforcement_id=? AND generation=? AND recovery_lease_owner=?`), input.RunID, completedText, completedText, input.AccountID, input.EnforcementID, input.Generation, input.OwnerID); err != nil {
		return RecoveryResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return RecoveryResult{}, err
	}
	return RecoveryResult{Result: "recovered", BeforeStatus: status, AfterStatus: after, Message: "质量恢复检查达标，账户已完成恢复"}, nil
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

type availabilitySchedule struct {
	Enabled    bool                    `json:"enabled"`
	Timezone   string                  `json:"timezone"`
	Mode       string                  `json:"mode"`
	Windows    []availabilityWindow    `json:"windows"`
	DateRange  *availabilityDateRange  `json:"dateRange"`
	Exceptions []availabilityException `json:"exceptions"`
}
type availabilityWindow struct {
	Days  []int  `json:"daysOfWeek"`
	Start string `json:"start"`
	End   string `json:"end"`
}
type availabilityDateRange struct {
	Start string `json:"startDate"`
	End   string `json:"endDate"`
}
type availabilityException struct {
	Date           string               `json:"date"`
	Action         string               `json:"action"`
	Windows        []availabilityWindow `json:"windows"`
	windowsPresent bool
}

// UnmarshalJSON preserves whether an exception windows field was omitted or
// explicitly provided as null, and restricts exception windows to the Node
// contract's start/end-only shape.
func (e *availabilityException) UnmarshalJSON(data []byte) error {
	var raw struct {
		Date    string          `json:"date"`
		Action  string          `json:"action"`
		Windows json.RawMessage `json:"windows"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&raw); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return errors.New("trailing content")
		}
		return err
	}
	e.Date, e.Action = raw.Date, raw.Action
	e.Windows = nil
	e.windowsPresent = raw.Windows != nil
	if !e.windowsPresent || bytes.Equal(bytes.TrimSpace(raw.Windows), []byte("null")) {
		return nil
	}
	var windows []availabilityExceptionWindow
	windowDecoder := json.NewDecoder(bytes.NewReader(raw.Windows))
	windowDecoder.DisallowUnknownFields()
	if err := windowDecoder.Decode(&windows); err != nil {
		return err
	}
	if err := windowDecoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return errors.New("trailing content")
		}
		return err
	}
	e.Windows = make([]availabilityWindow, len(windows))
	for i, window := range windows {
		e.Windows[i] = availabilityWindow{Start: window.Start, End: window.End}
	}
	return nil
}

type availabilityExceptionWindow struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

func availabilityAllowed(raw string, now time.Time) (bool, error) {
	if strings.TrimSpace(raw) == "" {
		return true, nil
	}
	var s availabilitySchedule
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&s); err != nil {
		return false, fmt.Errorf("invalid availability schedule JSON: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return false, errors.New("invalid availability schedule JSON trailing content")
	}
	if !s.Enabled || s.Mode != "allow_windows" {
		return false, errors.New("invalid availability schedule mode")
	}
	loc, err := time.LoadLocation(s.Timezone)
	if err != nil {
		return false, fmt.Errorf("invalid availability timezone: %w", err)
	}
	if err := validateAvailabilitySchedule(s); err != nil {
		return false, err
	}
	local := now.In(loc)
	currentDate := local.Format("2006-01-02")
	minute := local.Hour()*60 + local.Minute()
	previousDate := local.AddDate(0, 0, -1).Format("2006-01-02")
	for _, startDate := range []string{currentDate, previousDate} {
		if s.DateRange != nil && ((s.DateRange.Start != "" && startDate < s.DateRange.Start) || (s.DateRange.End != "" && startDate > s.DateRange.End)) {
			continue
		}
		if exception, ok := availabilityExceptionFor(s.Exceptions, startDate); ok {
			if exception.Action == "deny" {
				continue
			}
			if windowsAllowAt(exception.Windows, startDate, currentDate, minute, false) {
				return true, nil
			}
			continue
		}
		if windowsAllowAt(s.Windows, startDate, currentDate, minute, true) {
			return true, nil
		}
	}
	return false, nil
}
func windowsAllowAt(ws []availabilityWindow, startDate, currentDate string, minute int, requireDays bool) bool {
	for _, w := range ws {
		if requireDays && !includesDay(w.Days, dayForDate(startDate)) {
			continue
		}
		start, _ := availabilityMinute(w.Start)
		end, _ := availabilityMinute(w.End)
		if start < end && currentDate == startDate && minute >= start && minute < end {
			return true
		}
		if start > end && ((currentDate == startDate && minute >= start) || (currentDate == nextDate(startDate) && minute < end)) {
			return true
		}
	}
	return false
}

func validateAvailabilitySchedule(s availabilitySchedule) error {
	if strings.TrimSpace(s.Timezone) == "" || len(s.Windows) == 0 || len(s.Windows) > 32 || len(s.Exceptions) > 128 {
		return errors.New("invalid availability schedule")
	}
	if s.DateRange != nil {
		if err := validateDateRange(*s.DateRange); err != nil {
			return err
		}
	}
	for _, w := range s.Windows {
		if err := validateAvailabilityWindow(w, true); err != nil {
			return err
		}
	}
	for _, e := range s.Exceptions {
		if !validDate(e.Date) || (e.Action != "allow" && e.Action != "deny") || (e.Action == "deny" && e.windowsPresent) || (e.Action == "allow" && (!e.windowsPresent || len(e.Windows) == 0)) || len(e.Windows) > 32 {
			return errors.New("invalid availability exception")
		}
		for _, w := range e.Windows {
			if err := validateAvailabilityWindow(w, false); err != nil {
				return err
			}
		}
	}
	return nil
}
func validateDateRange(r availabilityDateRange) error {
	if !validDateOrBlank(r.Start) || !validDateOrBlank(r.End) || (r.Start != "" && r.End != "" && r.Start > r.End) {
		return errors.New("invalid availability date range")
	}
	return nil
}
func validateAvailabilityWindow(w availabilityWindow, requireDays bool) error {
	start, ok := availabilityMinute(w.Start)
	if !ok {
		return errors.New("invalid availability start")
	}
	end, ok := availabilityMinute(w.End)
	if !ok || start == end {
		return errors.New("invalid availability end")
	}
	if requireDays && len(w.Days) == 0 {
		return errors.New("availability window requires days")
	}
	for _, d := range w.Days {
		if d < 1 || d > 7 {
			return errors.New("invalid availability day")
		}
	}
	return nil
}
func availabilityMinute(value string) (int, bool) {
	if len(value) != 5 || value[2] != ':' || value[0] < '0' || value[0] > '2' || value[1] < '0' || value[1] > '9' || value[3] < '0' || value[3] > '5' || value[4] < '0' || value[4] > '9' {
		return 0, false
	}
	hours := int(value[0]-'0')*10 + int(value[1]-'0')
	if hours > 23 {
		return 0, false
	}
	return hours*60 + int(value[3]-'0')*10 + int(value[4]-'0'), true
}
func validDateOrBlank(value string) bool { return value == "" || validDate(value) }
func validDate(value string) bool {
	if len(value) != 10 {
		return false
	}
	_, err := time.Parse("2006-01-02", value)
	return err == nil
}
func includesDay(days []int, day int) bool {
	for _, d := range days {
		if d == day {
			return true
		}
	}
	return false
}
func dayForDate(date string) int {
	parsed, _ := time.Parse("2006-01-02", date)
	day := int(parsed.Weekday())
	if day == 0 {
		return 7
	}
	return day
}
func nextDate(date string) string {
	parsed, _ := time.Parse("2006-01-02", date)
	return parsed.AddDate(0, 0, 1).Format("2006-01-02")
}
func availabilityExceptionFor(exceptions []availabilityException, date string) (availabilityException, bool) {
	for _, e := range exceptions {
		if e.Date == date {
			return e, true
		}
	}
	return availabilityException{}, false
}
