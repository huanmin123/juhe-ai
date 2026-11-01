package oauthrefresh

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/quotahourly"
)

// Availability schedule status sync ×2 mirrors
// syncApiKeyAvailabilityScheduleStatuses(Async) and
// syncAccountAvailabilityScheduleStatuses(Async)
// (api-key-schedule-status-sync.repository.ts /
// account-availability-schedule-status-sync.repository.ts): due rows are
// evaluated for window boundary events, applied exactly once through the
// *_schedule_status_events dedupe tables, and the derived next-check column is
// advanced for every scanned row.
//
// T91-7 deviation (api_keys only): the boundary branch alone never fires when
// the exact boundary minute is missed (process restart / failed round), so the
// api-key sync additionally recomputes the target status from "is now inside
// an allowed window" on every due row and compensates a missed boundary with
// the canonical boundary event key (see catchUpScheduleEvent). The dedupe key
// keeps the BUG-0025 contract intact: manual enable/disable between two
// boundaries is not continuously overridden.

// DefaultScheduleSyncBatchLimit mirrors
// runtimeConfig.background.{apiKeyScheduleSyncBatchLimit,
// accountAvailabilityScheduleSyncBatchLimit} (default 500).
const DefaultScheduleSyncBatchLimit = 500

// ScheduleStatusSyncResult mirrors ApiKeyScheduleStatusSyncResult /
// AccountAvailabilityScheduleStatusSyncResult. Invalid counts rows whose data
// could not be applied this round: an unparseable schedule, and for api_keys
// also a quota document whose hourly window cannot be determined (that row is
// left at its current status instead of being activated without a binding).
type ScheduleStatusSyncResult struct {
	Scanned    int
	Activated  int
	Disabled   int
	Unchanged  int
	Skipped    int
	Invalid    int
	ChangedIDs []string
	InvalidIDs []string
}

// ActivationHook runs the activation side effects of an account schedule sync
// (Node advanceAccountCircuitDispatchRevisionFamily*): the gateway circuit
// control plane lives in the gateway module, so the jobs wiring supplies the
// implementation; nil keeps the status flip only. The hook receives the sync's
// own transaction exactly like the Node archive (the side effect runs inside
// the same tx as the status flip); opening a second transaction inside the
// hook would self-deadlock on the uncommitted flip (SQLite single writer / PG
// row lock), so implementations must use the given tx.
type ActivationHook interface {
	OnAccountActivated(ctx context.Context, tx *sql.Tx, accountID string, nowIso string) error
}

// ActivationHookFunc adapts a function to ActivationHook.
type ActivationHookFunc func(ctx context.Context, tx *sql.Tx, accountID string, nowIso string) error

// OnAccountActivated implements ActivationHook.
func (f ActivationHookFunc) OnAccountActivated(ctx context.Context, tx *sql.Tx, accountID, nowIso string) error {
	return f(ctx, tx, accountID, nowIso)
}

// APIKeyActivationHook runs the activation side effects of an api_key schedule
// sync. The schedule flip itself writes only api_keys.status, but an hourly
// quota API Key additionally needs its request_quota_hourly_window_scope_bindings
// row: the binding table is the only source statsagg uses to build
// usage_quota_hourly_windows, so a key activated without it keeps an hourly
// quota that silently reads zero cost (B3-H11). gateway internal/apikeys owns
// the same write for manual create/edit; jobs wires the schedule-side rebuild
// because it owns the flip transaction.
//
// The hook runs inside the sync transaction (same tx as the status flip), so a
// hook error rolls the flip back instead of leaving an active key without its
// binding. The returned afterCommit callback runs only after the transaction
// commits (dirty-scope marking); its failures must be handled by the callback
// and never fail the committed sync, mirroring gateway's after-commit dirty
// mark.
type APIKeyActivationHook interface {
	OnAPIKeyActivated(ctx context.Context, tx *sql.Tx, apiKeyID string, nowIso string) (afterCommit func(), err error)
}

// APIKeyActivationHookFunc adapts a function to APIKeyActivationHook.
type APIKeyActivationHookFunc func(ctx context.Context, tx *sql.Tx, apiKeyID string, nowIso string) (func(), error)

// OnAPIKeyActivated implements APIKeyActivationHook.
func (f APIKeyActivationHookFunc) OnAPIKeyActivated(ctx context.Context, tx *sql.Tx, apiKeyID, nowIso string) (func(), error) {
	return f(ctx, tx, apiKeyID, nowIso)
}

// scheduleSyncUpdate carries one evaluated row (Node
// ScheduledApiKeyStatusUpdate).
type scheduleSyncUpdate struct {
	id          string
	eventKey    string // "" = next-check-only update
	status      string // "" = next-check-only update
	nextCheckAt string
}

// SyncApiKeyScheduleStatuses mirrors the api_keys sync. hook is required for the
// production path: every activation flip rebuilds the Key's hourly quota window
// binding inside the flip transaction (B3-H11); nil keeps the status flip only
// and is reserved for tests that assert the bare schedule semantics.
func (s *Store) SyncApiKeyScheduleStatuses(ctx context.Context, now time.Time, batchLimit int, hook APIKeyActivationHook) (ScheduleStatusSyncResult, error) {
	rows, err := s.listScheduleRows(ctx, s.table("api_keys"), false, batchLimit, isoMillis(now))
	if err != nil {
		return ScheduleStatusSyncResult{}, err
	}
	updates, result, err := s.evaluateScheduleRows(rows, now, false)
	if err != nil {
		return ScheduleStatusSyncResult{}, err
	}
	activation := func(tx *sql.Tx, update scheduleSyncUpdate) (func(), error) {
		if hook == nil || update.status != "active" {
			return nil, nil
		}
		return hook.OnAPIKeyActivated(ctx, tx, update.id, isoMillis(now))
	}
	if err := s.applyScheduleUpdates(ctx, s.table("api_keys"), s.table("api_key_schedule_status_events"), "api_key_id", false, updates, isoMillis(now), &result, activation); err != nil {
		return ScheduleStatusSyncResult{}, err
	}
	return result, nil
}

// SyncAccountScheduleStatuses mirrors the accounts sync: only active/disabled
// rows are schedule-mutable, an active disable-enforcement blocks the flip and
// activation side effects ride the ActivationHook.
func (s *Store) SyncAccountScheduleStatuses(ctx context.Context, now time.Time, batchLimit int, hook ActivationHook) (ScheduleStatusSyncResult, error) {
	rows, err := s.listScheduleRows(ctx, s.table("accounts"), true, batchLimit, isoMillis(now))
	if err != nil {
		return ScheduleStatusSyncResult{}, err
	}
	updates, result, err := s.evaluateScheduleRows(rows, now, true)
	if err != nil {
		return ScheduleStatusSyncResult{}, err
	}
	activation := func(tx *sql.Tx, update scheduleSyncUpdate) (func(), error) {
		if hook == nil || update.status != "active" {
			return nil, nil
		}
		return nil, hook.OnAccountActivated(ctx, tx, update.id, isoMillis(now))
	}
	if err := s.applyScheduleUpdates(ctx, s.table("accounts"), s.table("account_schedule_status_events"), "account_id", true, updates, isoMillis(now), &result, activation); err != nil {
		return ScheduleStatusSyncResult{}, err
	}
	return result, nil
}

// evaluateScheduleRows mirrors the per-row evaluation half of both syncs
// (parse → next-check → due event; invalid schedules disable active rows).
func (s *Store) evaluateScheduleRows(rows []scheduleRow, now time.Time, accountMode bool) ([]scheduleSyncUpdate, ScheduleStatusSyncResult, error) {
	result := ScheduleStatusSyncResult{Scanned: len(rows)}
	updates := make([]scheduleSyncUpdate, 0, len(rows))
	for _, row := range rows {
		schedule, parseErr := ParseScheduleJSON(row.scheduleJSON)
		if parseErr != nil {
			result.Invalid++
			result.InvalidIDs = append(result.InvalidIDs, row.id)
			if accountMode {
				// Invalid account schedules disable active rows
				// ("已按不可调度处理"); other statuses only drop the schedule.
				if row.status == "active" {
					updates = append(updates, scheduleSyncUpdate{id: row.id, status: "disabled", nextCheckAt: ""})
				} else {
					updates = append(updates, scheduleSyncUpdate{id: row.id, nextCheckAt: ""})
				}
			} else {
				if row.status != "disabled" {
					updates = append(updates, scheduleSyncUpdate{id: row.id, status: "disabled", nextCheckAt: ""})
				} else {
					updates = append(updates, scheduleSyncUpdate{id: row.id, nextCheckAt: ""})
				}
			}
			continue
		}
		nextCheckAt, _ := NextScheduleCheckAt(schedule, now)
		event, hasEvent := DueScheduleEvent(schedule, now)
		// T91-7 补偿只落在 API Key 同步：账户同步还叠加质量处罚 / 可调度状态
		// 等保护语义，本轮裁决范围不含账户计划。
		if !hasEvent && !accountMode {
			event, hasEvent = catchUpScheduleEvent(schedule, row, now)
		}
		if !hasEvent {
			updates = append(updates, scheduleSyncUpdate{id: row.id, nextCheckAt: nextCheckAt})
			result.Unchanged++
			continue
		}
		if accountMode && row.status != "active" && row.status != "disabled" {
			// isScheduleMutableAccountStatus: pending_test/error/… rows only
			// advance the next check.
			updates = append(updates, scheduleSyncUpdate{id: row.id, nextCheckAt: nextCheckAt})
			result.Unchanged++
			continue
		}
		if !accountMode && event.Status == "active" && row.status != "active" {
			// B3-H11：非 active → active 的翻转必须能重建小时额度窗口绑定。额度
			// 文档无法判定小时窗口时不得翻为 active（否则小时额度不可判定），按
			// 「该行本轮无效」逐项隔离：只推进下次检查、保持当前状态，不影响同批
			// 其它 Key 的转换。已是 active 的行不在此列（守卫本就拒绝重复翻转）。
			if _, _, quotaErr := quotahourly.HourlyWindowHours(row.quotaLimitsJSON.String); quotaErr != nil {
				result.Invalid++
				result.InvalidIDs = append(result.InvalidIDs, row.id)
				updates = append(updates, scheduleSyncUpdate{id: row.id, nextCheckAt: nextCheckAt})
				continue
			}
		}
		updates = append(updates, scheduleSyncUpdate{
			id:          row.id,
			eventKey:    row.id + ":" + event.EventKey,
			status:      event.Status,
			nextCheckAt: nextCheckAt,
		})
	}
	return updates, result, nil
}

// catchUpScheduleEvent 是 T91-7 的补偿判定（仅 API Key 同步）：窗口边界只在
// 精确分钟产出事件，进程重启 / 任务失败错过该分钟后状态一整天不纠正。这里
// 每轮按「当前时刻是否在允许窗口内」重算目标状态，只有目标状态与当前落库
// 状态不同、且能定位到「最近一个本应执行但尚未执行」的边界事件时才补偿。
//
// 三条与既有契约的对齐点：
//   - 事件键沿用被错过边界的标准键（CatchUpScheduleEvent），已执行过的边界
//     在事件表里天然去重，因此人工启用 / 停用在两个边界之间不被持续覆盖
//     （BUG-0025 契约：下一次计划边界才接管）。
//   - next_check_at 为空的行只推进下次检查、不补偿：该形态来自导入/存量行，
//     没有「已错过边界」的事实，按既有「人工状态不被持续纠偏」处理。
//   - 补偿事件的 Status 必须与当前时刻的目标状态一致，否则放弃本轮（多窗口
//     重叠等边界语义变化时宁可不写，也不写错状态）。
func catchUpScheduleEvent(schedule *AvailabilitySchedule, row scheduleRow, now time.Time) (ScheduleDueEvent, bool) {
	if schedule == nil || !schedule.Enabled {
		return ScheduleDueEvent{}, false
	}
	if !row.nextCheckAt.Valid || row.nextCheckAt.String == "" {
		return ScheduleDueEvent{}, false
	}
	target, ok := ScheduleStatus(schedule, now)
	if !ok || target == row.status {
		return ScheduleDueEvent{}, false
	}
	event, ok := CatchUpScheduleEvent(schedule, now)
	if !ok || event.Status != target {
		return ScheduleDueEvent{}, false
	}
	return event, true
}

type scheduleRow struct {
	id           string
	status       string
	scheduleJSON string
	nextCheckAt  sql.NullString
	// quotaLimitsJSON 只在 api_keys 同步读取（激活时要重建小时额度窗口绑定，
	// B3-H11）；accounts 同步留空。
	quotaLimitsJSON sql.NullString
}

func (s *Store) listScheduleRows(ctx context.Context, table string, accountMode bool, batchLimit int, dueAt string) ([]scheduleRow, error) {
	if batchLimit <= 0 {
		batchLimit = DefaultScheduleSyncBatchLimit
	}
	columns := "id, status, availability_schedule_json, availability_schedule_next_check_at"
	if !accountMode {
		columns += ", quota_limits_json"
	}
	query := `SELECT ` + columns + `
		FROM ` + table + `
		WHERE availability_schedule_json IS NOT NULL
			AND (availability_schedule_next_check_at IS NULL OR availability_schedule_next_check_at <= ?)`
	if accountMode {
		query += `
			AND deleted_at IS NULL`
	}
	query += `
		ORDER BY availability_schedule_next_check_at IS NOT NULL ASC, availability_schedule_next_check_at ASC, id ASC
		LIMIT ?`
	rows, err := s.db.QueryContext(ctx, s.bind(query), dueAt, batchLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	output := []scheduleRow{}
	for rows.Next() {
		var row scheduleRow
		if accountMode {
			if err := rows.Scan(&row.id, &row.status, &row.scheduleJSON, &row.nextCheckAt); err != nil {
				return nil, err
			}
		} else if err := rows.Scan(&row.id, &row.status, &row.scheduleJSON, &row.nextCheckAt, &row.quotaLimitsJSON); err != nil {
			return nil, err
		}
		output = append(output, row)
	}
	return output, rows.Err()
}

// applyScheduleUpdates mirrors the transaction half of both syncs. activation
// runs inside the transaction for a successful activation flip and may return
// an afterCommit callback; callbacks run only after Commit succeeds and are
// responsible for their own failure handling (the committed sync must not be
// failed by a tail effect).
func (s *Store) applyScheduleUpdates(
	ctx context.Context,
	table string,
	eventsTable string,
	entityColumn string,
	accountMode bool,
	updates []scheduleSyncUpdate,
	updatedAt string,
	result *ScheduleStatusSyncResult,
	activation func(tx *sql.Tx, update scheduleSyncUpdate) (afterCommit func(), err error),
) error {
	if len(updates) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	afterCommit := []func(){}
	for _, update := range updates {
		if update.status != "" && update.eventKey != "" {
			inserted, err := s.insertScheduleStatusEvent(ctx, tx, eventsTable, entityColumn, update, updatedAt)
			if err != nil {
				return err
			}
			if !inserted {
				result.Skipped++
				if err := s.updateScheduleNextCheckAt(ctx, tx, table, update.id, update.nextCheckAt); err != nil {
					return err
				}
				continue
			}
		}
		if update.status == "" {
			if err := s.updateScheduleNextCheckAt(ctx, tx, table, update.id, update.nextCheckAt); err != nil {
				return err
			}
			continue
		}
		changed, err := s.applyScheduleStatusFlip(ctx, tx, table, accountMode, update, updatedAt, result)
		if err != nil {
			return err
		}
		if !changed {
			if err := s.updateScheduleNextCheckAt(ctx, tx, table, update.id, update.nextCheckAt); err != nil {
				return err
			}
			result.Unchanged++
			continue
		}
		result.ChangedIDs = append(result.ChangedIDs, update.id)
		if update.status == "active" {
			if activation != nil {
				after, err := activation(tx, update)
				if err != nil {
					return err
				}
				if after != nil {
					afterCommit = append(afterCommit, after)
				}
			}
			result.Activated++
		} else {
			result.Disabled++
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	for _, callback := range afterCommit {
		callback()
	}
	return nil
}

// insertScheduleStatusEvent mirrors INSERT OR IGNORE /
// ON CONFLICT(event_key) DO NOTHING; false = the event already ran.
func (s *Store) insertScheduleStatusEvent(ctx context.Context, tx *sql.Tx, eventsTable, entityColumn string, update scheduleSyncUpdate, executedAt string) (bool, error) {
	query := `INSERT INTO ` + eventsTable + ` (event_key, ` + entityColumn + `, status, executed_at)
		VALUES (?, ?, ?, ?)`
	query += ` ON CONFLICT(event_key) DO NOTHING`
	result, err := tx.ExecContext(ctx, s.bind(query), update.eventKey, update.id, update.status, executedAt)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected > 0, nil
}

// applyScheduleStatusFlip mirrors the guarded status update:
// api_keys guard on availability_schedule_json + status <> target; accounts
// additionally guard deleted_at, the mutable status set and the active
// disable-enforcement.
func (s *Store) applyScheduleStatusFlip(ctx context.Context, tx *sql.Tx, table string, accountMode bool, update scheduleSyncUpdate, updatedAt string, result *ScheduleStatusSyncResult) (bool, error) {
	query := `UPDATE ` + table + `
		SET status = ?, availability_schedule_next_check_at = ?, updated_at = ?
		WHERE id = ?
			AND availability_schedule_json IS NOT NULL`
	args := []any{update.status, update.nextCheckAt, updatedAt, update.id}
	if accountMode {
		query += `
			AND deleted_at IS NULL
			AND status IN ('active', 'disabled')
			AND status <> ?
			AND NOT EXISTS (
				SELECT 1 FROM ` + s.table("account_quality_enforcements") + ` aqe
				WHERE aqe.account_id = ` + outerEntityName(table) + `.id
					AND aqe.state = 'active'
					AND aqe.action = 'disable'
			)`
		args = append(args, update.status)
	} else {
		query += `
			AND status <> ?`
		args = append(args, update.status)
	}
	executed, err := tx.ExecContext(ctx, s.bind(query), args...)
	if err != nil {
		return false, err
	}
	affected, err := executed.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected > 0, nil
}

// outerEntityName keeps the Node NOT EXISTS correlation name ("accounts")
// while allowing the api_keys variant to reference its own table.
func outerEntityName(table string) string {
	if strings.HasSuffix(table, "accounts") {
		return "accounts"
	}
	return "api_keys"
}

func (s *Store) updateScheduleNextCheckAt(ctx context.Context, tx *sql.Tx, table, id, nextCheckAt string) error {
	query := `UPDATE ` + table + `
		SET availability_schedule_next_check_at = ?
		WHERE id = ?
			AND availability_schedule_json IS NOT NULL
			AND COALESCE(availability_schedule_next_check_at, '') <> COALESCE(?, '')`
	_, err := tx.ExecContext(ctx, s.bind(query), nextCheckAt, id, nextCheckAt)
	return err
}
