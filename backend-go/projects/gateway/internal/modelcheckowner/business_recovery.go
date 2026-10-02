package modelcheckowner

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
	now := input.CompletedAt.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	var state, action, systemID string
	var policyRevision, accountRevision int
	row := tx.QueryRowContext(ctx, q(`SELECT state,action,system_account_id,policy_revision,account_config_revision FROM `+a.table("account_quality_enforcements")+` WHERE account_id=? AND enforcement_id=? AND generation=? AND recovery_lease_owner=? AND recovery_lease_until>?`+lock), input.AccountID, input.EnforcementID, input.Generation, input.OwnerID, now.Format(time.RFC3339Nano))
	if err := row.Scan(&state, &action, &systemID, &policyRevision, &accountRevision); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return tx.Commit()
		}
		return fmt.Errorf("read J3b recovery lease: %w", err)
	}
	next := now.Add(time.Duration(input.RecoveryIntervalMinutes) * time.Minute).Format(time.RFC3339Nano)
	reschedule := func() error {
		_, err := tx.ExecContext(ctx, q(`UPDATE `+a.table("account_quality_enforcements")+` SET last_recovery_run_id=?,recovery_due_at=?,recovery_lease_owner=NULL,recovery_lease_until=NULL,updated_at=? WHERE account_id=? AND enforcement_id=? AND generation=? AND recovery_lease_owner=? AND recovery_lease_until>?`), input.RunID, next, now.Format(time.RFC3339Nano), input.AccountID, input.EnforcementID, input.Generation, input.OwnerID, now.Format(time.RFC3339Nano))
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

// SchedulePenaltyRestoreInput 是立即复测恢复（D4）的输入：恢复目标只来自
// enforcement 行（config_source='schedule' AND config_source_id=计划ID AND
// state='active'），账户人工停用（非质量处罚）不会被此路径改动。
type SchedulePenaltyRestoreInput struct {
	AccountID, SystemAccountID, ScheduleID, RunID string
	OccurredAt                                    time.Time
}

// SchedulePenaltyRestoreOutcome 汇总一次恢复尝试：Restored=成功清除并回滚
// 账户侧处罚效果的行数；Skipped=行存在但被代次/版本 CAS 或状态前置校验跳过
// （含账户行缺失/状态不符）。恢复失败（DB 错误）以 error 返回。
type SchedulePenaltyRestoreOutcome struct {
	Restored int
	Skipped  int
}

// RestoreSchedulePenalties 恢复"该计划造成"的处罚（D4）：对每条
// config_source='schedule' 且 state='active' 的 enforcement 行，按行内记录
// 的处罚语义回滚——quality_isolate 复用 Complete 的恢复 CAS（账户
// quality_isolated->active/disabled + enforcement cleared）；disable 恢复为
// 行内 before_status（空则 active）；fallback 恢复行内记录的降级标记前值。
// config_source='manual' 的行一律不碰。
//
// 账户 config_revision 的 CAS 基准与 claimRecoveries 同语义：处罚事务把
// 账户 revision+1 后，enforcement 行记录的是处罚前值，两者恒差 1；因此
// 恢复事务先锁定账户行（PG FOR UPDATE，SQLite 依赖事务写锁）读取**当前**
// config_revision 作为基准（账户 UPDATE 的 WHERE 与比对都用当前值），并把
// 处罚行 account_config_revision 刷新为当前值。纯 revision 漂移（执行期
// 账户被并发编辑但仍处于本处罚的目标状态）不阻断恢复；真正跳过恢复的是
// 状态前置校验（quality_isolated/disabled 不再匹配，说明并发变更已改变
// 处罚语义）与代次（generation）CAS，均计 Skipped 不覆盖。账户缺失/已删
// 按既有 Skipped 处理。
func (a *BusinessRecoveryApplier) RestoreSchedulePenalties(ctx context.Context, input SchedulePenaltyRestoreInput) (SchedulePenaltyRestoreOutcome, error) {
	if a == nil || a.db == nil {
		return SchedulePenaltyRestoreOutcome{}, errors.New("J3b Business recovery owner is not initialized")
	}
	if strings.TrimSpace(input.AccountID) == "" || strings.TrimSpace(input.SystemAccountID) == "" || strings.TrimSpace(input.ScheduleID) == "" || strings.TrimSpace(input.RunID) == "" {
		return SchedulePenaltyRestoreOutcome{}, errors.New("J3b schedule penalty restore input is invalid")
	}
	now := input.OccurredAt.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return SchedulePenaltyRestoreOutcome{}, fmt.Errorf("begin J3b schedule penalty restore: %w", err)
	}
	defer tx.Rollback()
	lock := ""
	if a.postgres {
		lock = " FOR UPDATE"
	}
	rows, err := tx.QueryContext(ctx, a.bind(`SELECT enforcement_id,generation,action,before_status,fallback_was_enabled,super_priority_was_enabled FROM `+a.table("account_quality_enforcements")+` WHERE account_id=? AND system_account_id=? AND config_source='schedule' AND config_source_id=? AND state='active'`+lock), input.AccountID, input.SystemAccountID, input.ScheduleID)
	if err != nil {
		return SchedulePenaltyRestoreOutcome{}, fmt.Errorf("read J3b schedule penalty rows: %w", err)
	}
	type pendingRestore struct {
		enforcementID           string
		generation              int
		action, beforeStatus    string
		fallbackWasEnabled      bool
		superPriorityWasEnabled bool
	}
	pending := make([]pendingRestore, 0, 1)
	for rows.Next() {
		var item pendingRestore
		if err := rows.Scan(&item.enforcementID, &item.generation, &item.action, &item.beforeStatus, &item.fallbackWasEnabled, &item.superPriorityWasEnabled); err != nil {
			rows.Close()
			return SchedulePenaltyRestoreOutcome{}, fmt.Errorf("scan J3b schedule penalty row: %w", err)
		}
		pending = append(pending, item)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return SchedulePenaltyRestoreOutcome{}, fmt.Errorf("iterate J3b schedule penalty rows: %w", err)
	}
	outcome := SchedulePenaltyRestoreOutcome{}
	// clearEnforcement 清除处罚行并把 account_config_revision 刷新为恢复
	// 事务锁定的账户当前值（claimRecoveries 的刷新语义），使行内 revision
	// 与账户事实保持一致。
	clearEnforcement := func(item pendingRestore, accountRevision int) bool {
		res, err := tx.ExecContext(ctx, a.bind(`UPDATE `+a.table("account_quality_enforcements")+` SET state='cleared',last_recovery_run_id=?,cleared_at=?,recovery_due_at=NULL,recovery_lease_owner=NULL,recovery_lease_until=NULL,account_config_revision=?,updated_at=? WHERE account_id=? AND enforcement_id=? AND generation=? AND state='active'`), input.RunID, now.Format(time.RFC3339Nano), accountRevision, now.Format(time.RFC3339Nano), input.AccountID, item.enforcementID, item.generation)
		if err != nil {
			return false
		}
		n, _ := res.RowsAffected()
		return n == 1
	}
	for _, item := range pending {
		var status, availability string
		var currentRevision int
		err := tx.QueryRowContext(ctx, a.bind(`SELECT status,config_revision,COALESCE(availability_schedule_json,'') FROM `+a.table("accounts")+` WHERE id=? AND system_account_id=? AND deleted_at IS NULL`+lock), input.AccountID, input.SystemAccountID).Scan(&status, &currentRevision, &availability)
		if errors.Is(err, sql.ErrNoRows) {
			outcome.Skipped++
			continue
		}
		if err != nil {
			return SchedulePenaltyRestoreOutcome{}, fmt.Errorf("read J3b schedule penalty account: %w", err)
		}
		// CAS 基准 = 锁定账户行后读到的当前 config_revision（处罚事务已把
		// 账户 revision+1，处罚行内是旧值，不能用于比对）。纯 revision 漂移
		// 不跳过；跳过由下方各 action 的状态前置校验与 generation CAS 表达。
		switch item.action {
		case "quality_isolate":
			if status != "quality_isolated" {
				outcome.Skipped++
				continue
			}
			allowed, err := availabilityAllowedGateway(availability, now)
			if err != nil {
				return SchedulePenaltyRestoreOutcome{}, fmt.Errorf("evaluate J3b schedule restore availability: %w", err)
			}
			after := "disabled"
			if allowed {
				after = "active"
			}
			res, err := tx.ExecContext(ctx, a.bind(`UPDATE `+a.table("accounts")+` SET status=?,schedulable=?,last_error_code=NULL,last_error_message=NULL,config_revision=config_revision+1,updated_at=? WHERE id=? AND system_account_id=? AND status='quality_isolated' AND config_revision=?`), after, boolIntGateway(after == "active"), now.Format(time.RFC3339Nano), input.AccountID, input.SystemAccountID, currentRevision)
			if err != nil {
				return SchedulePenaltyRestoreOutcome{}, fmt.Errorf("restore J3b schedule isolate account: %w", err)
			}
			if n, _ := res.RowsAffected(); n != 1 {
				outcome.Skipped++
				continue
			}
			if !clearEnforcement(item, currentRevision) {
				// 账户侧已回滚处罚效果，处罚行清除必须同一事务落库；失败整体
				// 回滚，绝不留下"账户已恢复但处罚仍 active"的半程状态。
				return SchedulePenaltyRestoreOutcome{}, errors.New("J3b schedule penalty restore enforcement CAS failed")
			}
		case "disable":
			after := strings.TrimSpace(item.beforeStatus)
			if after == "" {
				after = "active"
			}
			if status != "disabled" {
				outcome.Skipped++
				continue
			}
			res, err := tx.ExecContext(ctx, a.bind(`UPDATE `+a.table("accounts")+` SET status=?,schedulable=?,last_error_code=NULL,last_error_message=NULL,config_revision=config_revision+1,updated_at=? WHERE id=? AND system_account_id=? AND status='disabled' AND config_revision=?`), after, boolIntGateway(after == "active"), now.Format(time.RFC3339Nano), input.AccountID, input.SystemAccountID, currentRevision)
			if err != nil {
				return SchedulePenaltyRestoreOutcome{}, fmt.Errorf("restore J3b schedule disable account: %w", err)
			}
			if n, _ := res.RowsAffected(); n != 1 {
				outcome.Skipped++
				continue
			}
			if !clearEnforcement(item, currentRevision) {
				return SchedulePenaltyRestoreOutcome{}, errors.New("J3b schedule penalty restore enforcement CAS failed")
			}
		case "fallback":
			res, err := tx.ExecContext(ctx, a.bind(`UPDATE `+a.table("accounts")+` SET fallback_enabled=?,super_priority_enabled=?,last_error_code=NULL,last_error_message=NULL,config_revision=config_revision+1,updated_at=? WHERE id=? AND system_account_id=? AND config_revision=?`), boolIntGateway(item.fallbackWasEnabled), boolIntGateway(item.superPriorityWasEnabled), now.Format(time.RFC3339Nano), input.AccountID, input.SystemAccountID, currentRevision)
			if err != nil {
				return SchedulePenaltyRestoreOutcome{}, fmt.Errorf("restore J3b schedule fallback account: %w", err)
			}
			if n, _ := res.RowsAffected(); n != 1 {
				outcome.Skipped++
				continue
			}
			if !clearEnforcement(item, currentRevision) {
				return SchedulePenaltyRestoreOutcome{}, errors.New("J3b schedule penalty restore enforcement CAS failed")
			}
		default:
			outcome.Skipped++
			continue
		}
		outcome.Restored++
	}
	if err := tx.Commit(); err != nil {
		return SchedulePenaltyRestoreOutcome{}, fmt.Errorf("commit J3b schedule penalty restore: %w", err)
	}
	return outcome, nil
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
	Date           string          `json:"date"`
	Action         string          `json:"action"`
	Windows        []gatewayWindow `json:"windows"`
	windowsPresent bool
}

// UnmarshalJSON keeps the distinction between an omitted windows field and an
// explicitly provided null, and applies the narrower exception-window schema
// (start/end only) used by the Node gateway.
func (e *gatewayException) UnmarshalJSON(data []byte) error {
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
	var windows []gatewayExceptionWindow
	windowDecoder := json.NewDecoder(bytes.NewReader(raw.Windows))
	windowDecoder.DisallowUnknownFields()
	if err := windowDecoder.Decode(&windows); err != nil {
		return err
	}
	// raw.Windows 由 encoding/json Decoder 从单一 JSON 值原样捕获，不可能再
	// 携带尾随值，二次 Decode 的尾随分支不可达（w14m 甄别：不可达防御臂）。
	e.Windows = make([]gatewayWindow, len(windows))
	for i, window := range windows {
		e.Windows[i] = gatewayWindow{Start: window.Start, End: window.End}
	}
	return nil
}

type gatewayExceptionWindow struct {
	Start string `json:"start"`
	End   string `json:"end"`
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
		if !gatewayDate(ex.Date) || (ex.Action != "allow" && ex.Action != "deny") || (ex.Action == "deny" && ex.windowsPresent) || (ex.Action == "allow" && (!ex.windowsPresent || len(ex.Windows) == 0)) {
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
