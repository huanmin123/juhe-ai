package oauthrefresh

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

// Availability schedule status sync ×2 mirrors
// syncApiKeyAvailabilityScheduleStatuses(Async) and
// syncAccountAvailabilityScheduleStatuses(Async)
// (api-key-schedule-status-sync.repository.ts /
// account-availability-schedule-status-sync.repository.ts): due rows are
// evaluated for window boundary events, applied exactly once through the
// *_schedule_status_events dedupe tables, and the derived next-check column is
// advanced for every scanned row.

// DefaultScheduleSyncBatchLimit mirrors
// runtimeConfig.background.{apiKeyScheduleSyncBatchLimit,
// accountAvailabilityScheduleSyncBatchLimit} (default 500).
const DefaultScheduleSyncBatchLimit = 500

// ScheduleStatusSyncResult mirrors ApiKeyScheduleStatusSyncResult /
// AccountAvailabilityScheduleStatusSyncResult.
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
// implementation; nil keeps the status flip only.
type ActivationHook interface {
	OnAccountActivated(ctx context.Context, accountID string, nowIso string) error
}

// ActivationHookFunc adapts a function to ActivationHook.
type ActivationHookFunc func(ctx context.Context, accountID string, nowIso string) error

// OnAccountActivated implements ActivationHook.
func (f ActivationHookFunc) OnAccountActivated(ctx context.Context, accountID, nowIso string) error {
	return f(ctx, accountID, nowIso)
}

// scheduleSyncUpdate carries one evaluated row (Node
// ScheduledApiKeyStatusUpdate).
type scheduleSyncUpdate struct {
	id          string
	eventKey    string // "" = next-check-only update
	status      string // "" = next-check-only update
	nextCheckAt string
}

// SyncApiKeyScheduleStatuses mirrors the api_keys sync.
func (s *Store) SyncApiKeyScheduleStatuses(ctx context.Context, now time.Time, batchLimit int) (ScheduleStatusSyncResult, error) {
	rows, err := s.listScheduleRows(ctx, s.table("api_keys"), false, batchLimit, isoMillis(now))
	if err != nil {
		return ScheduleStatusSyncResult{}, err
	}
	updates, result, err := s.evaluateScheduleRows(rows, now, false)
	if err != nil {
		return ScheduleStatusSyncResult{}, err
	}
	if err := s.applyScheduleUpdates(ctx, s.table("api_keys"), s.table("api_key_schedule_status_events"), "api_key_id", false, updates, isoMillis(now), &result, nil); err != nil {
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
	activation := func(update scheduleSyncUpdate) func() error {
		if hook == nil || update.status != "active" {
			return nil
		}
		return func() error { return hook.OnAccountActivated(ctx, update.id, isoMillis(now)) }
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
		updates = append(updates, scheduleSyncUpdate{
			id:          row.id,
			eventKey:    row.id + ":" + event.EventKey,
			status:      event.Status,
			nextCheckAt: nextCheckAt,
		})
	}
	return updates, result, nil
}

type scheduleRow struct {
	id           string
	status       string
	scheduleJSON string
	nextCheckAt  sql.NullString
}

func (s *Store) listScheduleRows(ctx context.Context, table string, accountMode bool, batchLimit int, dueAt string) ([]scheduleRow, error) {
	if batchLimit <= 0 {
		batchLimit = DefaultScheduleSyncBatchLimit
	}
	columns := "id, status, availability_schedule_json, availability_schedule_next_check_at"
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
		if err := rows.Scan(&row.id, &row.status, &row.scheduleJSON, &row.nextCheckAt); err != nil {
			return nil, err
		}
		output = append(output, row)
	}
	return output, rows.Err()
}

// applyScheduleUpdates mirrors the transaction half of both syncs.
func (s *Store) applyScheduleUpdates(
	ctx context.Context,
	table string,
	eventsTable string,
	entityColumn string,
	accountMode bool,
	updates []scheduleSyncUpdate,
	updatedAt string,
	result *ScheduleStatusSyncResult,
	activation func(scheduleSyncUpdate) func() error,
) error {
	if len(updates) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
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
				if apply := activation(update); apply != nil {
					if err := apply(); err != nil {
						return err
					}
				}
			}
			result.Activated++
		} else {
			result.Disabled++
		}
	}
	return tx.Commit()
}

// insertScheduleStatusEvent mirrors INSERT OR IGNORE /
// ON CONFLICT(event_key) DO NOTHING; false = the event already ran.
func (s *Store) insertScheduleStatusEvent(ctx context.Context, tx *sql.Tx, eventsTable, entityColumn string, update scheduleSyncUpdate, executedAt string) (bool, error) {
	query := `INSERT INTO ` + eventsTable + ` (event_key, ` + entityColumn + `, status, executed_at)
		VALUES (?, ?, ?, ?)`
	if !s.pg {
		query = `INSERT OR IGNORE INTO ` + eventsTable + ` (event_key, ` + entityColumn + `, status, executed_at)
			VALUES (?, ?, ?, ?)`
	} else {
		query += ` ON CONFLICT(event_key) DO NOTHING`
	}
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
