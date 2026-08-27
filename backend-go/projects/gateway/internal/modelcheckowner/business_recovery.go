package modelcheckowner

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// BusinessRecoveryApplier commits the quality-isolation recovery result in
// the same Business-owner transaction that owns the account and enforcement.
// A stale lease/generation is a harmless no-op; the scheduler may acknowledge
// the task without allowing an old probe to change current account state.
type BusinessRecoveryApplier struct {
	db       *sql.DB
	postgres bool
}

func NewBusinessRecoveryApplier(db *sql.DB, postgres bool) (*BusinessRecoveryApplier, error) {
	if db == nil {
		return nil, errors.New("J3b Business recovery database is required")
	}
	return &BusinessRecoveryApplier{db: db, postgres: postgres}, nil
}

func (a *BusinessRecoveryApplier) Complete(ctx context.Context, input RecoveryPayload, passed bool) error {
	if a == nil || a.db == nil || strings.TrimSpace(input.OwnerID) == "" || strings.TrimSpace(input.AccountID) == "" || strings.TrimSpace(input.EnforcementID) == "" || strings.TrimSpace(input.RunID) == "" || input.Generation < 1 || input.PolicyRevision < 0 || input.RecoveryIntervalMinutes < 10 {
		return errors.New("J3b Business recovery input is invalid")
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin J3b Business recovery: %w", err)
	}
	defer tx.Rollback()
	q := func(s string) string { return a.bind(s) }
	lock := ""
	if a.postgres {
		lock = " FOR UPDATE"
	}
	var state, action, systemID string
	var policyRevision, accountRevision int
	row := tx.QueryRowContext(ctx, q(`SELECT state,action,system_account_id,policy_revision,account_config_revision FROM `+a.table("account_quality_enforcements")+` WHERE account_id=? AND enforcement_id=? AND generation=? AND recovery_lease_owner=?`+lock), input.AccountID, input.EnforcementID, input.Generation, input.OwnerID)
	if err := row.Scan(&state, &action, &systemID, &policyRevision, &accountRevision); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return tx.Commit()
		}
		return fmt.Errorf("read J3b recovery lease: %w", err)
	}
	now := input.CompletedAt.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	next := now.Add(time.Duration(input.RecoveryIntervalMinutes) * time.Minute).Format(time.RFC3339Nano)
	reschedule := func() error {
		_, err := tx.ExecContext(ctx, q(`UPDATE `+a.table("account_quality_enforcements")+` SET last_recovery_run_id=?,recovery_due_at=?,recovery_lease_owner=NULL,recovery_lease_until=NULL,updated_at=? WHERE account_id=? AND enforcement_id=? AND generation=? AND recovery_lease_owner=?`), input.RunID, next, now.Format(time.RFC3339Nano), input.AccountID, input.EnforcementID, input.Generation, input.OwnerID)
		return err
	}
	if state != "active" || action != "quality_isolate" {
		return tx.Commit()
	}
	if policyRevision != input.PolicyRevision {
		if err := reschedule(); err != nil {
			return err
		}
		return tx.Commit()
	}
	if !passed {
		if err := reschedule(); err != nil {
			return err
		}
		return tx.Commit()
	}
	var status, schedule string
	var currentRevision int
	row = tx.QueryRowContext(ctx, q(`SELECT status,config_revision,COALESCE(availability_schedule_json,'') FROM `+a.table("accounts")+` WHERE id=? AND system_account_id=? AND deleted_at IS NULL`+lock), input.AccountID, systemID)
	if err := row.Scan(&status, &currentRevision, &schedule); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return tx.Commit()
		}
		return fmt.Errorf("read J3b recovery account: %w", err)
	}
	if status != "quality_isolated" || currentRevision != accountRevision {
		if err := reschedule(); err != nil {
			return err
		}
		return tx.Commit()
	}
	allowed, err := availabilityAllowedGateway(schedule, now)
	if err != nil {
		return fmt.Errorf("evaluate J3b account availability: %w", err)
	}
	after := "disabled"
	if allowed {
		after = "active"
	}
	result, err := tx.ExecContext(ctx, q(`UPDATE `+a.table("accounts")+` SET status=?,schedulable=?,last_error_code=NULL,last_error_message=NULL,config_revision=config_revision+1,updated_at=? WHERE id=? AND system_account_id=? AND status='quality_isolated' AND config_revision=?`), after, boolIntGateway(after == "active"), now.Format(time.RFC3339Nano), input.AccountID, systemID, accountRevision)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return tx.Commit()
	}
	if _, err := tx.ExecContext(ctx, q(`UPDATE `+a.table("account_quality_enforcements")+` SET state='cleared',last_recovery_run_id=?,cleared_at=?,recovery_due_at=NULL,recovery_lease_owner=NULL,recovery_lease_until=NULL,updated_at=? WHERE account_id=? AND enforcement_id=? AND generation=? AND recovery_lease_owner=?`), input.RunID, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), input.AccountID, input.EnforcementID, input.Generation, input.OwnerID); err != nil {
		return err
	}
	return tx.Commit()
}

func (a *BusinessRecoveryApplier) table(name string) string {
	if a.postgres {
		return "juhe_business." + name
	}
	return name
}
func (a *BusinessRecoveryApplier) bind(s string) string {
	if !a.postgres {
		return s
	}
	var b strings.Builder
	index := 0
	for _, r := range s {
		if r == '?' {
			index++
			fmt.Fprintf(&b, "$%d", index)
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}
func boolIntGateway(v bool) int {
	if v {
		return 1
	}
	return 0
}

type gatewayAvailability struct {
	Enabled    bool               `json:"enabled"`
	Timezone   string             `json:"timezone"`
	Mode       string             `json:"mode"`
	Windows    []gatewayWindow    `json:"windows"`
	DateRange  *gatewayDateRange  `json:"dateRange"`
	Exceptions []gatewayException `json:"exceptions"`
}
type gatewayWindow struct {
	Days  []int  `json:"daysOfWeek"`
	Start string `json:"start"`
	End   string `json:"end"`
}
type gatewayDateRange struct {
	Start string `json:"startDate"`
	End   string `json:"endDate"`
}
type gatewayException struct {
	Date    string          `json:"date"`
	Action  string          `json:"action"`
	Windows []gatewayWindow `json:"windows"`
}

func availabilityAllowedGateway(raw string, now time.Time) (bool, error) {
	if strings.TrimSpace(raw) == "" {
		return true, nil
	}
	var s gatewayAvailability
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&s); err != nil {
		return false, fmt.Errorf("invalid availability schedule JSON: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return false, errors.New("invalid availability schedule JSON trailing content")
	}
	if err := validateGatewayAvailability(s); err != nil {
		return false, err
	}
	loc, err := time.LoadLocation(s.Timezone)
	if err != nil {
		return false, fmt.Errorf("invalid availability timezone: %w", err)
	}
	local := now.In(loc)
	currentDate := local.Format("2006-01-02")
	minute := local.Hour()*60 + local.Minute()
	previousDate := local.AddDate(0, 0, -1).Format("2006-01-02")
	for _, startDate := range []string{currentDate, previousDate} {
		if s.DateRange != nil && ((s.DateRange.Start != "" && startDate < s.DateRange.Start) || (s.DateRange.End != "" && startDate > s.DateRange.End)) {
			continue
		}
		if ex, ok := gatewayExceptionFor(s.Exceptions, startDate); ok {
			if ex.Action == "deny" {
				continue
			}
			if gatewayWindowsAllow(ex.Windows, startDate, currentDate, minute, false) {
				return true, nil
			}
			continue
		}
		if gatewayWindowsAllow(s.Windows, startDate, currentDate, minute, true) {
			return true, nil
		}
	}
	return false, nil
}
func validateGatewayAvailability(s gatewayAvailability) error {
	if !s.Enabled || s.Mode != "allow_windows" || strings.TrimSpace(s.Timezone) == "" || len(s.Windows) == 0 || len(s.Windows) > 32 || len(s.Exceptions) > 128 {
		return errors.New("invalid availability schedule")
	}
	if s.DateRange != nil && (!gatewayDateOrBlank(s.DateRange.Start) || !gatewayDateOrBlank(s.DateRange.End) || (s.DateRange.Start != "" && s.DateRange.End != "" && s.DateRange.Start > s.DateRange.End)) {
		return errors.New("invalid availability date range")
	}
	for _, w := range s.Windows {
		if err := validateGatewayWindow(w, true); err != nil {
			return err
		}
	}
	for _, ex := range s.Exceptions {
		if !gatewayDate(ex.Date) || (ex.Action != "allow" && ex.Action != "deny") || (ex.Action == "deny" && len(ex.Windows) > 0) || (ex.Action == "allow" && len(ex.Windows) == 0) {
			return errors.New("invalid availability exception")
		}
		for _, w := range ex.Windows {
			if err := validateGatewayWindow(w, false); err != nil {
				return err
			}
		}
	}
	return nil
}
func validateGatewayWindow(w gatewayWindow, requireDays bool) error {
	start, ok := gatewayMinute(w.Start)
	if !ok {
		return errors.New("invalid availability start")
	}
	end, ok := gatewayMinute(w.End)
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
func gatewayWindowsAllow(windows []gatewayWindow, startDate, currentDate string, minute int, requireDays bool) bool {
	for _, w := range windows {
		if requireDays && !containsGatewayDay(w.Days, gatewayDay(startDate)) {
			continue
		}
		start, _ := gatewayMinute(w.Start)
		end, _ := gatewayMinute(w.End)
		if start < end && currentDate == startDate && minute >= start && minute < end {
			return true
		}
		if start > end && ((currentDate == startDate && minute >= start) || (currentDate == gatewayNextDate(startDate) && minute < end)) {
			return true
		}
	}
	return false
}
func containsGatewayDay(days []int, day int) bool {
	for _, d := range days {
		if d == day {
			return true
		}
	}
	return false
}
func gatewayMinute(v string) (int, bool) {
	if len(v) != 5 || v[2] != ':' || v[0] < '0' || v[0] > '2' || v[1] < '0' || v[1] > '9' || v[3] < '0' || v[3] > '5' || v[4] < '0' || v[4] > '9' {
		return 0, false
	}
	h := int(v[0]-'0')*10 + int(v[1]-'0')
	if h > 23 {
		return 0, false
	}
	return h*60 + int(v[3]-'0')*10 + int(v[4]-'0'), true
}
func gatewayDateOrBlank(v string) bool { return v == "" || gatewayDate(v) }
func gatewayDate(v string) bool {
	if len(v) != 10 {
		return false
	}
	_, err := time.Parse("2006-01-02", v)
	return err == nil
}
func gatewayDay(date string) int {
	parsed, _ := time.Parse("2006-01-02", date)
	day := int(parsed.Weekday())
	if day == 0 {
		return 7
	}
	return day
}
func gatewayNextDate(date string) string {
	parsed, _ := time.Parse("2006-01-02", date)
	return parsed.AddDate(0, 0, 1).Format("2006-01-02")
}
func gatewayExceptionFor(exceptions []gatewayException, date string) (gatewayException, bool) {
	for _, ex := range exceptions {
		if ex.Date == date {
			return ex, true
		}
	}
	return gatewayException{}, false
}
