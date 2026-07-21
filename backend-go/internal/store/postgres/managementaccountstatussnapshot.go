package postgres

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"juhe-ai/backend-go/internal/store/port"
)

const managementAccountStatusSnapshotSQL = `
SELECT a.id, a.system_account_id, a.name, a.status, a.schedulable,
       COALESCE(a.account_expires_at::text, ''), COALESCE(a.cooldown_until::text, ''),
       COALESCE(a.last_error_code, ''), COALESCE(a.last_error_message, ''), COALESCE(a.last_error_trace_id, ''),
       COALESCE(a.last_health_check_at::text, ''), COALESCE(a.next_health_check_at::text, ''),
       COALESCE(a.last_health_check_status_code, 0), COALESCE(a.last_health_check_error_code, ''),
       COALESCE(a.last_health_check_error_message, ''), COALESCE(a.last_health_check_trace_id, ''), COALESCE(a.last_used_at::text, ''),
       COALESCE(ra.id, ''), COALESCE(ra.status, ''), COALESCE(ra.expires_at::text, ''),
       COALESCE(a.authorization_instance_source_account_id, ''), COALESCE(source.schedulable, false), COALESCE(source.account_expires_at::text, ''), COALESCE(source.status, ''),
       COALESCE(source.cooldown_until::text, ''), COALESCE(source.last_error_code, ''), COALESCE(source.last_error_message, ''), COALESCE(source.last_error_trace_id, ''),
       COALESCE(gb.group_id, ''), COALESCE(g.name, ''),
       CASE
         WHEN gb.group_id IS NULL THEN 'unbound'
         WHEN a.authorization_instance_authorization_id IS NOT NULL AND COALESCE(gb.account_authorization_id, '') <> COALESCE(ra.id, '') THEN 'authorization_unavailable'
         ELSE 'bound'
       END,
       COALESCE((SELECT jsonb_build_object('requestCount', usd.request_count, 'successCount', usd.success_count, 'errorCount', usd.error_count, 'inputTokens', usd.input_tokens, 'outputTokens', usd.output_tokens, 'totalCost', usd.total_cost_usd)::text FROM juhe_stats.usage_stats_daily usd WHERE usd.system_account_id = a.system_account_id AND usd.scope_type = CASE WHEN ra.id IS NULL THEN 'account' ELSE 'account_authorization' END AND usd.scope_id = CASE WHEN ra.id IS NULL THEN a.id ELSE ra.id END AND usd.stat_date = $3), '{}')
FROM juhe_business.accounts a
LEFT JOIN juhe_business.resource_authorizations ra ON ra.id = a.authorization_instance_authorization_id
LEFT JOIN juhe_business.accounts source ON source.id = a.authorization_instance_source_account_id AND source.deleted_at IS NULL
LEFT JOIN LATERAL (SELECT ga.group_id, ga.account_authorization_id FROM juhe_business.group_accounts ga WHERE ga.account_id = a.id AND ga.system_account_id = a.system_account_id AND ga.enabled = true ORDER BY ga.updated_at DESC, ga.group_id LIMIT 1) gb ON true
LEFT JOIN juhe_business.groups g ON g.id = gb.group_id AND g.system_account_id = a.system_account_id
WHERE a.deleted_at IS NULL AND a.id = ANY($1::text[]) AND ($2 = '' OR a.system_account_id = $2)
  AND (a.authorization_instance_authorization_id IS NULL OR ra.status IN ('active', 'paused', 'expired'))
ORDER BY array_position($1::text[], a.id)`

func (s *Store) ListManagementAccountStatusProjections(ctx context.Context, input port.ManagementAccountStatusSnapshotInput) ([]port.ManagementAccountStatusProjection, error) {
	ids := make([]string, 0, len(input.AccountIDs))
	for _, id := range input.AccountIDs {
		if id = strings.TrimSpace(id); id != "" {
			ids = append(ids, id)
		}
	}
	rows, err := s.pool.Query(ctx, managementAccountStatusSnapshotSQL, ids, strings.TrimSpace(input.SystemAccountID), strings.TrimSpace(input.StatDate))
	if err != nil {
		return nil, fmt.Errorf("list management account status projections: %w", err)
	}
	defer rows.Close()
	result := make([]port.ManagementAccountStatusProjection, 0, len(ids))
	for rows.Next() {
		var row port.ManagementAccountStatusProjection
		if err := rows.Scan(&row.ID, &row.SystemAccountID, &row.Name, &row.Status, &row.Schedulable, &row.AccountExpiresAt, &row.CooldownUntil, &row.LastErrorCode, &row.LastErrorMessage, &row.LastErrorTraceID, &row.LastHealthCheckAt, &row.NextHealthCheckAt, &row.LastHealthCheckStatusCode, &row.LastHealthCheckErrorCode, &row.LastHealthCheckErrorMessage, &row.LastHealthCheckTraceID, &row.LastUsedAt, &row.AuthorizationID, &row.AuthorizationStatus, &row.AuthorizationExpiresAt, &row.AuthorizationInstanceSourceAccountID, &row.AuthorizationInstanceSourceSchedulable, &row.AuthorizationInstanceSourceExpiresAt, &row.AuthorizationInstanceSourceAccountStatus, &row.AuthorizationInstanceSourceCooldownUntil, &row.AuthorizationInstanceSourceLastErrorCode, &row.AuthorizationInstanceSourceLastErrorMessage, &row.AuthorizationInstanceSourceLastErrorTraceID, &row.BoundGroupID, &row.BoundGroupName, &row.GroupBindStatus, &row.TodayUsageJSON); err != nil {
			return nil, fmt.Errorf("scan management account status projection: %w", err)
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate management account status projections: %w", err)
	}
	return result, nil
}

var _ port.ManagementAccountStatusSnapshotReader = (*Store)(nil)
var _ pgx.Rows = (pgx.Rows)(nil)
