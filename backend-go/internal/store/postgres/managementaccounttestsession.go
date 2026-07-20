package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"juhe-ai/backend-go/internal/store/port"
)

const accountTestSessionProjectionSQL = `
SELECT id, status, COALESCE(cancel_reason, ''), last_heartbeat_at, cancel_requested_at, finished_at, created_at, updated_at
FROM juhe_business.account_test_sessions
WHERE id = $1 AND request_system_account_id = $2 AND COALESCE(request_system_account_filter_id, '') = $3
LIMIT 1`

const accountTestTaskProjectionSQL = `
SELECT t.id, COALESCE(st.session_id, ''), t.account_id, t.account_name, t.provider_code,
       COALESCE(t.provider_protocol_profile_id, ''), COALESCE(t.protocol_code, ''), COALESCE(t.protocol_version, ''),
       t.account_type, t.status, COALESCE(t.status_message, ''), COALESCE(t.model, ''), COALESCE(t.test_endpoint_mode, ''),
       COALESCE(t.result_json, ''), t.cancel_requested, t.created_at, t.queued_at, t.started_at, t.finished_at, t.updated_at
FROM juhe_business.account_test_tasks t
LEFT JOIN juhe_business.account_test_session_tasks st ON st.task_id = t.id
WHERE t.id = $1 AND t.request_system_account_id = $2 AND COALESCE(t.request_system_account_filter_id, '') = $3
LIMIT 1`

const accountTestSessionTasksSQL = `
SELECT t.id, st.session_id, t.account_id, t.account_name, t.provider_code,
       COALESCE(t.provider_protocol_profile_id, ''), COALESCE(t.protocol_code, ''), COALESCE(t.protocol_version, ''),
       t.account_type, t.status, COALESCE(t.status_message, ''), COALESCE(t.model, ''), COALESCE(t.test_endpoint_mode, ''),
       COALESCE(t.result_json, ''), t.cancel_requested, t.created_at, t.queued_at, t.started_at, t.finished_at, t.updated_at
FROM juhe_business.account_test_session_tasks st
JOIN juhe_business.account_test_tasks t ON t.id = st.task_id
JOIN juhe_business.account_test_sessions s ON s.id = st.session_id
WHERE st.session_id = $1 AND s.request_system_account_id = $2 AND COALESCE(s.request_system_account_filter_id, '') = $3
ORDER BY t.queued_at ASC, t.id ASC
LIMIT $4`

func (s *Store) CreateManagementAccountTestSession(ctx context.Context, id string, access port.ManagementAccountTestAccess) (port.ManagementAccountTestSession, error) {
	var row sessionRow
	err := s.pool.QueryRow(ctx, `INSERT INTO juhe_business.account_test_sessions (id, request_system_account_id, request_role, request_system_account_filter_id, status, last_heartbeat_at, created_at, updated_at)
VALUES ($1, $2, $3, NULLIF($4, ''), 'running', now(), now(), now())
RETURNING id, status, COALESCE(cancel_reason, ''), last_heartbeat_at, cancel_requested_at, finished_at, created_at, updated_at`, id, access.ActorSystemAccountID, access.ActorRole, access.FilterSystemAccountID).Scan(rowArgs(&row)...)
	if err != nil {
		return port.ManagementAccountTestSession{}, fmt.Errorf("create management account test session: %w", err)
	}
	return row.session(), nil
}

func (s *Store) HeartbeatManagementAccountTestSession(ctx context.Context, id string, access port.ManagementAccountTestAccess) (port.ManagementAccountTestSession, bool, error) {
	_, err := s.pool.Exec(ctx, `UPDATE juhe_business.account_test_sessions SET last_heartbeat_at = now(), updated_at = now() WHERE id = $1 AND status = 'running' AND request_system_account_id = $2 AND COALESCE(request_system_account_filter_id, '') = $3`, id, access.ActorSystemAccountID, access.FilterSystemAccountID)
	if err != nil {
		return port.ManagementAccountTestSession{}, false, fmt.Errorf("heartbeat management account test session: %w", err)
	}
	return s.getSession(ctx, id, access)
}

func (s *Store) CompleteManagementAccountTestSession(ctx context.Context, id string, access port.ManagementAccountTestAccess) (port.ManagementAccountTestSession, bool, error) {
	_, err := s.pool.Exec(ctx, `UPDATE juhe_business.account_test_sessions s SET status = 'completed', finished_at = COALESCE(s.finished_at, now()), updated_at = now()
WHERE s.id = $1 AND s.status = 'running' AND s.request_system_account_id = $2 AND COALESCE(s.request_system_account_filter_id, '') = $3
AND NOT EXISTS (SELECT 1 FROM juhe_business.account_test_session_tasks st JOIN juhe_business.account_test_tasks t ON t.id = st.task_id WHERE st.session_id = s.id AND t.status IN ('queued','running'))`, id, access.ActorSystemAccountID, access.FilterSystemAccountID)
	if err != nil {
		return port.ManagementAccountTestSession{}, false, fmt.Errorf("complete management account test session: %w", err)
	}
	return s.getSession(ctx, id, access)
}

func (s *Store) CancelManagementAccountTestSession(ctx context.Context, id string, access port.ManagementAccountTestAccess) (port.ManagementAccountTestSession, []string, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return port.ManagementAccountTestSession{}, nil, false, err
	}
	defer tx.Rollback(ctx)
	var found string
	err = tx.QueryRow(ctx, `SELECT id FROM juhe_business.account_test_sessions WHERE id = $1 AND request_system_account_id = $2 AND COALESCE(request_system_account_filter_id, '') = $3 LIMIT 1`, id, access.ActorSystemAccountID, access.FilterSystemAccountID).Scan(&found)
	if errors.Is(err, pgx.ErrNoRows) {
		return port.ManagementAccountTestSession{}, nil, false, nil
	}
	if err != nil {
		return port.ManagementAccountTestSession{}, nil, false, err
	}
	rows, err := tx.Query(ctx, `SELECT t.id FROM juhe_business.account_test_session_tasks st JOIN juhe_business.account_test_tasks t ON t.id = st.task_id WHERE st.session_id = $1 AND t.status IN ('queued','running') ORDER BY t.queued_at, t.id`, id)
	if err != nil {
		return port.ManagementAccountTestSession{}, nil, true, err
	}
	var taskIDs []string
	for rows.Next() {
		var taskID string
		if err := rows.Scan(&taskID); err != nil {
			rows.Close()
			return port.ManagementAccountTestSession{}, nil, true, err
		}
		taskIDs = append(taskIDs, taskID)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return port.ManagementAccountTestSession{}, nil, true, err
	}
	_, err = tx.Exec(ctx, `UPDATE juhe_business.account_test_sessions SET status = 'canceled', cancel_reason = '用户取消账户测试', cancel_requested_at = COALESCE(cancel_requested_at, now()), finished_at = COALESCE(finished_at, now()), updated_at = now() WHERE id = $1 AND status = 'running'`, id)
	if err != nil {
		return port.ManagementAccountTestSession{}, nil, true, err
	}
	_, err = tx.Exec(ctx, `UPDATE juhe_business.account_test_tasks SET status = CASE WHEN status = 'queued' THEN 'canceled' ELSE status END, status_message = '用户取消账户测试', cancel_requested = true, finished_at = CASE WHEN status = 'queued' THEN COALESCE(finished_at, now()) ELSE finished_at END, updated_at = now() WHERE id IN (SELECT task_id FROM juhe_business.account_test_session_tasks WHERE session_id = $1) AND status IN ('queued','running')`, id)
	if err != nil {
		return port.ManagementAccountTestSession{}, nil, true, err
	}
	if err := tx.Commit(ctx); err != nil {
		return port.ManagementAccountTestSession{}, nil, true, err
	}
	session, foundAgain, err := s.getSession(ctx, id, access)
	return session, taskIDs, foundAgain, err
}

func (s *Store) CancelManagementAccountTestTask(ctx context.Context, id string, access port.ManagementAccountTestAccess) (port.ManagementAccountTestTask, bool, error) {
	_, err := s.pool.Exec(ctx, `UPDATE juhe_business.account_test_tasks SET status = CASE WHEN status = 'queued' THEN 'canceled' ELSE status END, status_message = '用户取消账户测试', cancel_requested = true, finished_at = CASE WHEN status = 'queued' THEN COALESCE(finished_at, now()) ELSE finished_at END, updated_at = now() WHERE id = $1 AND request_system_account_id = $2 AND COALESCE(request_system_account_filter_id, '') = $3 AND status IN ('queued','running')`, id, access.ActorSystemAccountID, access.FilterSystemAccountID)
	if err != nil {
		return port.ManagementAccountTestTask{}, false, err
	}
	return s.getTask(ctx, id, access)
}

type sessionRow struct {
	ID, Status, Message           string
	LastHeartbeatAt               time.Time
	CancelRequestedAt, FinishedAt *time.Time
	CreatedAt, UpdatedAt          time.Time
}

func rowArgs(r *sessionRow) []any {
	return []any{&r.ID, &r.Status, &r.Message, &r.LastHeartbeatAt, &r.CancelRequestedAt, &r.FinishedAt, &r.CreatedAt, &r.UpdatedAt}
}
func (r sessionRow) session() port.ManagementAccountTestSession {
	return port.ManagementAccountTestSession{ID: r.ID, Status: r.Status, Message: r.Message, LastHeartbeatAt: r.LastHeartbeatAt, CancelRequestedAt: r.CancelRequestedAt, FinishedAt: r.FinishedAt, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
}
func (s *Store) getSession(ctx context.Context, id string, access port.ManagementAccountTestAccess) (port.ManagementAccountTestSession, bool, error) {
	var r sessionRow
	err := s.pool.QueryRow(ctx, accountTestSessionProjectionSQL, id, access.ActorSystemAccountID, access.FilterSystemAccountID).Scan(rowArgs(&r)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return port.ManagementAccountTestSession{}, false, nil
	}
	if err != nil {
		return port.ManagementAccountTestSession{}, false, err
	}
	return r.session(), true, nil
}

type taskRow struct {
	ID, SessionID, AccountID, AccountName, ProviderCode, ProviderProfile, ProtocolCode, ProtocolVersion, Type, Status, Message, Model, Endpoint, ResultJSON string
	CancelRequested                                                                                                                                         bool
	CreatedAt, QueuedAt                                                                                                                                     time.Time
	StartedAt, FinishedAt                                                                                                                                   *time.Time
	UpdatedAt                                                                                                                                               time.Time
}

func taskArgs(r *taskRow) []any {
	return []any{&r.ID, &r.SessionID, &r.AccountID, &r.AccountName, &r.ProviderCode, &r.ProviderProfile, &r.ProtocolCode, &r.ProtocolVersion, &r.Type, &r.Status, &r.Message, &r.Model, &r.Endpoint, &r.ResultJSON, &r.CancelRequested, &r.CreatedAt, &r.QueuedAt, &r.StartedAt, &r.FinishedAt, &r.UpdatedAt}
}
func (r taskRow) task() port.ManagementAccountTestTask {
	var result map[string]any
	if strings.TrimSpace(r.ResultJSON) != "" {
		_ = json.Unmarshal([]byte(r.ResultJSON), &result)
	}
	return port.ManagementAccountTestTask{ID: r.ID, SessionID: r.SessionID, AccountID: r.AccountID, AccountName: r.AccountName, ProviderCode: r.ProviderCode, ProviderProtocolProfileID: r.ProviderProfile, ProtocolCode: r.ProtocolCode, ProtocolVersion: r.ProtocolVersion, Type: r.Type, Status: r.Status, Message: r.Message, Model: r.Model, TestEndpointMode: r.Endpoint, Result: result, CancelRequested: r.CancelRequested, CreatedAt: r.CreatedAt, QueuedAt: r.QueuedAt, StartedAt: r.StartedAt, FinishedAt: r.FinishedAt, UpdatedAt: r.UpdatedAt}
}
func (s *Store) getTask(ctx context.Context, id string, a port.ManagementAccountTestAccess) (port.ManagementAccountTestTask, bool, error) {
	var r taskRow
	err := s.pool.QueryRow(ctx, accountTestTaskProjectionSQL, id, a.ActorSystemAccountID, a.FilterSystemAccountID).Scan(taskArgs(&r)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return port.ManagementAccountTestTask{}, false, nil
	}
	if err != nil {
		return port.ManagementAccountTestTask{}, false, err
	}
	return r.task(), true, nil
}

var _ port.ManagementAccountTestSessionStore = (*Store)(nil)
