package modelcheckowner

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	// HealthRetryMaxAttempts caps how often one health-sync retry task may be
	// claimed and failed before it is dead-lettered. A permanently broken run
	// row (for example, truncated legacy JSON) must not stay claimable every
	// minute forever.
	HealthRetryMaxAttempts = 10
	// HealthRetryDeadLetterDueIn pushes an exhausted task's due_at beyond any
	// practical horizon. The scheduler task schema has no terminal dead state
	// (the Postgres DDL constrains state to pending/failed/completed), so
	// exhaustion is modeled as a failed task that is never due again; the
	// final cause stays readable in last_error and the stable task id blocks
	// EnsureHealthRetryTasks from re-materializing it (ON CONFLICT DO
	// NOTHING).
	HealthRetryDeadLetterDueIn = 100 * 365 * 24 * time.Hour
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
	retries, err := s.ListHealthSyncRetries(ctx, limit)
	if err != nil {
		return fmt.Errorf("list J3b health retry tasks: %w", err)
	}
	runIDs := make([]string, 0, len(retries))
	for _, retry := range retries {
		runIDs = append(runIDs, retry.RunID)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, runID := range runIDs {
		payload := fmt.Sprintf(`{"runId":%q}`, runID)
		if _, err := s.db.ExecContext(ctx, s.bind(`INSERT INTO `+s.schedulerTaskTable()+` (id,kind,due_at,state,payload,updated_at) VALUES (?,?,?,?,?,?) ON CONFLICT(id) DO NOTHING`), "health:"+runID, string(SchedulerHealthRetry), now, "pending", payload, now); err != nil {
			return fmt.Errorf("materialize J3b health retry %s: %w", runID, err)
		}
	}
	return nil
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
	attempts, payload, counted := healthRetryAttemptPayload(task)
	dueAt := now.Add(time.Minute)
	if counted && attempts >= HealthRetryMaxAttempts {
		// Dead letter: the task keeps its failed state but is never due
		// again, so neither this owner nor a takeover re-claims it. The
		// terminal cause is prefixed so operators can distinguish exhaustion
		// from an ordinary retryable failure.
		dueAt = now.Add(HealthRetryDeadLetterDueIn)
		message = fmt.Sprintf("health retry attempts exhausted after %d: %s", attempts, message)
		if len(message) > 1000 {
			message = message[:1000]
		}
	}
	var result sql.Result
	var err error
	if counted {
		result, err = s.Store.db.ExecContext(ctx, s.Store.bind(fmt.Sprintf(`UPDATE %s SET state='failed',last_error=?,due_at=?,claim_owner=NULL,claim_until=NULL,payload=?,updated_at=? WHERE id=? AND claim_owner=? AND fence_token=?`, s.schedulerTable())), message, dueAt.Format(time.RFC3339Nano), payload, now.Format(time.RFC3339Nano), task.ID, task.OwnerID, task.FenceToken)
	} else {
		result, err = s.Store.db.ExecContext(ctx, s.Store.bind(fmt.Sprintf(`UPDATE %s SET state='failed',last_error=?,due_at=?,claim_owner=NULL,claim_until=NULL,updated_at=? WHERE id=? AND claim_owner=? AND fence_token=?`, s.schedulerTable())), message, dueAt.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), task.ID, task.OwnerID, task.FenceToken)
	}
	if err != nil {
		return fmt.Errorf("fail J3b task %s: %w", task.ID, err)
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return fmt.Errorf("fail J3b task %s rejected by owner/fence", task.ID)
	}
	return nil
}

// healthRetryTaskPayload is the mutable projection stored inside a health
// retry task's otherwise stable run-derived payload. Attempts is the retry
// counter consumed by SQLSchedulerSource.Fail.
type healthRetryTaskPayload struct {
	RunID    string `json:"runId"`
	Attempts int    `json:"attempts,omitempty"`
}

// healthRetryAttemptPayload reads the attempt counter from a health retry
// task's payload and returns it incremented together with the re-encoded
// payload. counted is false for tasks that are not health sync retries (for
// example scheduled payloads claimed through a direct SQLSchedulerSource in
// tests); their frozen payload shape is never rewritten by Fail.
// 手工损坏的 payload（空、截断或缺 runId）按首次失败处理：attempt 从 0 起
// 计数并重建 payload（语法损坏经 encoding/json 整体校验失败，runId 不可
// 恢复、投影器必然继续失败），保证损坏行也走 MaxAttempts 死信路径，不再
// 每分钟被无限重认领。
func healthRetryAttemptPayload(task ScheduleTask) (attempts int, payload []byte, counted bool) {
	if task.Kind != SchedulerHealthRetry {
		return 0, nil, false
	}
	var parsed healthRetryTaskPayload
	if len(task.Payload) > 0 {
		// 损坏 payload 解析失败时按首次失败处理（attempts 保持 0 起步）。
		_ = json.Unmarshal(task.Payload, &parsed)
	}
	parsed.Attempts++
	encoded, err := json.Marshal(parsed)
	if err != nil {
		return parsed.Attempts, nil, false
	}
	return parsed.Attempts, encoded, true
}

var _ SchedulerSource = (*SQLSchedulerSource)(nil)
var _ SchedulerLifecycle = (*SQLSchedulerSource)(nil)

func (s *SQLSchedulerSource) schedulerTable() string {
	return s.Store.schedulerTaskTable()
}
