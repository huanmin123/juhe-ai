package modelcheckowner

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// SQLSchedulerSource claims durable scheduler tasks from the Gateway-owned
// J3b store. The table is deliberately separate from Node business tables;
// migration/bootstrap must create it before the scheduler is enabled.
type SQLSchedulerSource struct {
	Store   *Store
	OwnerID string
	Lease   time.Duration
}

func (s *SQLSchedulerSource) Claim(ctx context.Context, kind SchedulerKind, now time.Time, limit int) ([]ScheduleTask, error) {
	if s == nil || s.Store == nil || strings.TrimSpace(s.OwnerID) == "" || limit <= 0 {
		return nil, fmt.Errorf("J3b scheduler claim input is invalid")
	}
	lease := s.Lease
	if lease <= 0 {
		lease = 15 * time.Minute
	}
	tx, err := s.Store.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	query := fmt.Sprintf(`SELECT id,fence_token,payload FROM %s WHERE kind=? AND state IN ('pending','failed') AND due_at<=? AND (claim_until IS NULL OR claim_until<=?) ORDER BY due_at,id LIMIT ?`, s.schedulerTable())
	if s.Store.mode == "postgres" {
		query += " FOR UPDATE SKIP LOCKED"
	}
	rows, err := tx.QueryContext(ctx, s.Store.bind(query), string(kind), now.UTC().Format(time.RFC3339Nano), now.UTC().Format(time.RFC3339Nano), limit)
	if err != nil {
		return nil, fmt.Errorf("claim J3b %s tasks: %w", kind, err)
	}
	defer rows.Close()
	claimed := make([]ScheduleTask, 0, limit)
	for rows.Next() {
		var id string
		var fence int64
		var payload []byte
		if err := rows.Scan(&id, &fence, &payload); err != nil {
			return nil, err
		}
		claimed = append(claimed, ScheduleTask{ID: id, Kind: kind, OwnerID: s.OwnerID, FenceToken: fence + 1, Payload: append([]byte(nil), payload...)})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, task := range claimed {
		_, err = tx.ExecContext(ctx, s.Store.bind(fmt.Sprintf(`UPDATE %s SET claim_owner=?,claim_until=?,fence_token=?,state='pending',updated_at=? WHERE id=? AND (claim_until IS NULL OR claim_until<=?)`, s.schedulerTable())), s.OwnerID, now.Add(lease).UTC().Format(time.RFC3339Nano), task.FenceToken, now.UTC().Format(time.RFC3339Nano), task.ID, now.UTC().Format(time.RFC3339Nano))
		if err != nil {
			return nil, fmt.Errorf("lease J3b task %s: %w", task.ID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return claimed, nil
}

// EnsureHealthRetryTasks materializes one durable retry task for each failed
// health projection. The stable run-derived ID makes repeated scans and
// concurrent Gateway instances idempotent; Claim still supplies the lease and
// fence before the projector is invoked.
func (s *Store) EnsureHealthRetryTasks(ctx context.Context, limit int) error {
	if s == nil || s.db == nil || limit < 1 || limit > 10000 {
		return errors.New("J3b health retry task materialization input is invalid")
	}
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT id FROM `+s.table("model_check_runs")+` WHERE quality_health_sync_status IN ('failed','pending_retry') AND finished_at IS NOT NULL ORDER BY updated_at ASC,id ASC LIMIT ?`), limit)
	if err != nil {
		return fmt.Errorf("scan J3b health retry tasks: %w", err)
	}
	defer rows.Close()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for rows.Next() {
		var runID string
		if err := rows.Scan(&runID); err != nil {
			return err
		}
		payload := fmt.Sprintf(`{"runId":%q}`, runID)
		if _, err := s.db.ExecContext(ctx, s.bind(`INSERT INTO `+s.schedulerTaskTable()+` (id,kind,due_at,state,payload,updated_at) VALUES (?,?,?,?,?,?) ON CONFLICT(id) DO NOTHING`), "health:"+runID, string(SchedulerHealthRetry), now, "pending", payload, now); err != nil {
			return fmt.Errorf("materialize J3b health retry %s: %w", runID, err)
		}
	}
	return rows.Err()
}

func (s *SQLSchedulerSource) Complete(ctx context.Context, task ScheduleTask) error {
	if s == nil || s.Store == nil || task.ID == "" || task.OwnerID == "" || task.FenceToken <= 0 {
		return fmt.Errorf("J3b scheduler completion input is invalid")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := s.Store.db.ExecContext(ctx, s.Store.bind(fmt.Sprintf(`UPDATE %s SET state='completed',claim_owner=NULL,claim_until=NULL,completed_at=?,updated_at=? WHERE id=? AND claim_owner=? AND fence_token=?`, s.schedulerTable())), now, now, task.ID, task.OwnerID, task.FenceToken)
	if err != nil {
		return fmt.Errorf("complete J3b task %s: %w", task.ID, err)
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return fmt.Errorf("complete J3b task %s rejected by owner/fence", task.ID)
	}
	return nil
}

func (s *SQLSchedulerSource) Fail(ctx context.Context, task ScheduleTask, cause error) error {
	if s == nil || s.Store == nil || task.ID == "" || task.OwnerID == "" || task.FenceToken <= 0 {
		return fmt.Errorf("J3b scheduler failure input is invalid")
	}
	message := "scheduler execution failed"
	if cause != nil {
		message = cause.Error()
	}
	if len(message) > 1000 {
		message = message[:1000]
	}
	now := time.Now().UTC()
	result, err := s.Store.db.ExecContext(ctx, s.Store.bind(fmt.Sprintf(`UPDATE %s SET state='failed',last_error=?,due_at=?,claim_owner=NULL,claim_until=NULL,updated_at=? WHERE id=? AND claim_owner=? AND fence_token=?`, s.schedulerTable())), message, now.Add(time.Minute).Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), task.ID, task.OwnerID, task.FenceToken)
	if err != nil {
		return fmt.Errorf("fail J3b task %s: %w", task.ID, err)
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return fmt.Errorf("fail J3b task %s rejected by owner/fence", task.ID)
	}
	return nil
}

var _ SchedulerSource = (*SQLSchedulerSource)(nil)
var _ SchedulerLifecycle = (*SQLSchedulerSource)(nil)

func (s *SQLSchedulerSource) schedulerTable() string {
	return s.Store.schedulerTaskTable()
}
