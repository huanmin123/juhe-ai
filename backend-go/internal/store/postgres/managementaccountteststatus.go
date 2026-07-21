package postgres

import (
	"context"
	"fmt"

	"juhe-ai/backend-go/internal/store/port"
)

func (s *Store) GetManagementAccountTestSession(ctx context.Context, id string, access port.ManagementAccountTestAccess) (port.ManagementAccountTestSession, bool, error) {
	return s.getSession(ctx, id, access)
}

func (s *Store) GetManagementAccountTestTask(ctx context.Context, id string, access port.ManagementAccountTestAccess) (port.ManagementAccountTestTask, bool, error) {
	return s.getTask(ctx, id, access)
}

func (s *Store) ListManagementAccountTestSessionTasks(ctx context.Context, sessionID string, access port.ManagementAccountTestAccess, limit int) ([]port.ManagementAccountTestTask, bool, error) {
	if _, found, err := s.getSession(ctx, sessionID, access); err != nil || !found {
		return nil, found, err
	}
	rows, err := s.pool.Query(ctx, accountTestSessionTasksSQL, sessionID, access.ActorSystemAccountID, access.FilterSystemAccountID, limit)
	if err != nil {
		return nil, true, fmt.Errorf("list management account test session tasks: %w", err)
	}
	defer rows.Close()
	result := make([]port.ManagementAccountTestTask, 0)
	for rows.Next() {
		var row taskRow
		if err := rows.Scan(taskArgs(&row)...); err != nil {
			return nil, true, err
		}
		result = append(result, row.task())
	}
	if err := rows.Err(); err != nil {
		return nil, true, err
	}
	return result, true, nil
}

func (s *Store) ListManagementAccountTestTasks(ctx context.Context, ids []string, access port.ManagementAccountTestAccess) ([]port.ManagementAccountTestTask, error) {
	if len(ids) == 0 {
		return []port.ManagementAccountTestTask{}, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT t.id, COALESCE(st.session_id, ''), t.account_id, t.account_name, t.provider_code,
COALESCE(t.provider_protocol_profile_id, ''), COALESCE(t.protocol_code, ''), COALESCE(t.protocol_version, ''), t.account_type, t.status,
COALESCE(t.status_message, ''), COALESCE(t.model, ''), COALESCE(t.test_endpoint_mode, ''), COALESCE(t.result_json, ''), t.cancel_requested,
t.created_at, t.queued_at, t.started_at, t.finished_at, t.updated_at
FROM juhe_business.account_test_tasks t LEFT JOIN juhe_business.account_test_session_tasks st ON st.task_id=t.id
WHERE t.id=ANY($1::text[]) AND t.request_system_account_id=$2 AND COALESCE(t.request_system_account_filter_id,'')=$3
ORDER BY array_position($1::text[],t.id)`, ids, access.ActorSystemAccountID, access.FilterSystemAccountID)
	if err != nil {
		return nil, fmt.Errorf("list management account test tasks: %w", err)
	}
	defer rows.Close()
	result := make([]port.ManagementAccountTestTask, 0, len(ids))
	for rows.Next() {
		var row taskRow
		if err := rows.Scan(taskArgs(&row)...); err != nil {
			return nil, err
		}
		result = append(result, row.task())
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

var _ port.ManagementAccountTestStatusReader = (*Store)(nil)
