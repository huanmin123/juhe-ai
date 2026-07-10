package postgres

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"juhe-ai/backend-go/internal/store/port"
)

func (s *Store) ListGatewayQuotaSnapshotAPIKeys(ctx context.Context, limit int) (port.GatewayQuotaSnapshotRows[port.GatewayQuotaSnapshotAPIKeyRow], error) {
	if limit <= 0 {
		return port.GatewayQuotaSnapshotRows[port.GatewayQuotaSnapshotAPIKeyRow]{Complete: true}, nil
	}
	rows, err := s.pool.Query(ctx, `
SELECT id, system_account_id, quota_limits_json
FROM juhe_business.api_keys
WHERE status = 'active'
  AND quota_limits_json IS NOT NULL
ORDER BY updated_at DESC, id ASC
LIMIT $1
`, limit+1)
	if err != nil {
		return port.GatewayQuotaSnapshotRows[port.GatewayQuotaSnapshotAPIKeyRow]{}, fmt.Errorf("list gateway quota snapshot api keys: %w", err)
	}
	defer rows.Close()
	items := make([]port.GatewayQuotaSnapshotAPIKeyRow, 0, limit)
	for rows.Next() {
		var id, systemAccountID string
		var limitsJSON pgtype.Text
		if err := rows.Scan(&id, &systemAccountID, &limitsJSON); err != nil {
			return port.GatewayQuotaSnapshotRows[port.GatewayQuotaSnapshotAPIKeyRow]{}, fmt.Errorf("scan gateway quota snapshot api key: %w", err)
		}
		limits, err := managementAuthorizationLimitsFromJSON(limitsJSON)
		if err != nil {
			return port.GatewayQuotaSnapshotRows[port.GatewayQuotaSnapshotAPIKeyRow]{}, err
		}
		items = append(items, port.GatewayQuotaSnapshotAPIKeyRow{
			ID:              id,
			SystemAccountID: systemAccountID,
			Limits:          limits,
		})
	}
	if err := rows.Err(); err != nil {
		return port.GatewayQuotaSnapshotRows[port.GatewayQuotaSnapshotAPIKeyRow]{}, fmt.Errorf("iterate gateway quota snapshot api keys: %w", err)
	}
	return boundedGatewayQuotaSnapshotRows(items, limit), nil
}

func (s *Store) ListGatewayQuotaSnapshotAuthorizations(ctx context.Context, limit int) (port.GatewayQuotaSnapshotRows[port.GatewayQuotaSnapshotAuthorizationRow], error) {
	if limit <= 0 {
		return port.GatewayQuotaSnapshotRows[port.GatewayQuotaSnapshotAuthorizationRow]{Complete: true}, nil
	}
	rows, err := s.pool.Query(ctx, `
SELECT ra.id,
  ra.resource_owner_system_account_id,
  ra.grantee_system_account_id,
  ra.resource_type,
  ra.resource_id,
  COALESCE(ra.effective_source_team_id, '') AS effective_source_team_id,
  ra.limits_json
FROM juhe_business.resource_authorizations AS ra
WHERE ra.status = 'active'
  AND (
    ra.limits_json IS NOT NULL
    OR (
      ra.effective_source_team_id IS NOT NULL
      AND EXISTS (
        SELECT 1
        FROM juhe_business.resource_authorization_grants AS grant_rows
        WHERE grant_rows.resource_type = ra.resource_type
          AND grant_rows.resource_id = ra.resource_id
          AND grant_rows.grantee_type = 'team'
          AND grant_rows.grantee_team_id = ra.effective_source_team_id
          AND grant_rows.status = 'active'
          AND grant_rows.limits_json IS NOT NULL
        LIMIT 1
      )
    )
  )
ORDER BY ra.updated_at DESC, ra.id ASC
LIMIT $1
`, limit+1)
	if err != nil {
		return port.GatewayQuotaSnapshotRows[port.GatewayQuotaSnapshotAuthorizationRow]{}, fmt.Errorf("list gateway quota snapshot authorizations: %w", err)
	}
	defer rows.Close()
	items := make([]port.GatewayQuotaSnapshotAuthorizationRow, 0, limit)
	for rows.Next() {
		var item port.GatewayQuotaSnapshotAuthorizationRow
		var limitsJSON pgtype.Text
		if err := rows.Scan(
			&item.ID,
			&item.ResourceOwnerSystemAccountID,
			&item.GranteeSystemAccountID,
			&item.ResourceType,
			&item.ResourceID,
			&item.EffectiveSourceTeamID,
			&limitsJSON,
		); err != nil {
			return port.GatewayQuotaSnapshotRows[port.GatewayQuotaSnapshotAuthorizationRow]{}, fmt.Errorf("scan gateway quota snapshot authorization: %w", err)
		}
		limits, err := managementAuthorizationLimitsFromJSON(limitsJSON)
		if err != nil {
			return port.GatewayQuotaSnapshotRows[port.GatewayQuotaSnapshotAuthorizationRow]{}, err
		}
		item.Limits = limits
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return port.GatewayQuotaSnapshotRows[port.GatewayQuotaSnapshotAuthorizationRow]{}, fmt.Errorf("iterate gateway quota snapshot authorizations: %w", err)
	}
	return boundedGatewayQuotaSnapshotRows(items, limit), nil
}

func (s *Store) ListGatewayQuotaSnapshotTeamAuthorizations(ctx context.Context, limit int) (port.GatewayQuotaSnapshotRows[port.GatewayQuotaSnapshotTeamAuthorizationRow], error) {
	if limit <= 0 {
		return port.GatewayQuotaSnapshotRows[port.GatewayQuotaSnapshotTeamAuthorizationRow]{Complete: true}, nil
	}
	rows, err := s.pool.Query(ctx, `
SELECT ra.id AS authorization_id,
  grant_rows.resource_owner_system_account_id,
  COALESCE(ra.grantee_system_account_id, '') AS authorization_grantee_system_account_id,
  grant_rows.resource_type,
  grant_rows.resource_id,
  COALESCE(instance_accounts.id, '') AS authorization_instance_account_id,
  ra.effective_source_team_id,
  grant_rows.limits_json
FROM juhe_business.resource_authorizations AS ra
INNER JOIN juhe_business.resource_authorization_grants AS grant_rows
  ON grant_rows.resource_type = ra.resource_type
  AND grant_rows.resource_id = ra.resource_id
  AND grant_rows.grantee_type = 'team'
  AND grant_rows.grantee_team_id = ra.effective_source_team_id
  AND grant_rows.status = 'active'
LEFT JOIN juhe_business.accounts AS instance_accounts
  ON ra.resource_type = 'account'
  AND instance_accounts.authorization_instance_authorization_id = ra.id
  AND instance_accounts.system_account_id = ra.grantee_system_account_id
  AND instance_accounts.authorization_instance_source_account_id = ra.resource_id
WHERE ra.status = 'active'
  AND ra.effective_source_team_id IS NOT NULL
  AND grant_rows.limits_json IS NOT NULL
ORDER BY ra.updated_at DESC, ra.id ASC
LIMIT $1
`, limit+1)
	if err != nil {
		return port.GatewayQuotaSnapshotRows[port.GatewayQuotaSnapshotTeamAuthorizationRow]{}, fmt.Errorf("list gateway quota snapshot team authorizations: %w", err)
	}
	defer rows.Close()
	items := make([]port.GatewayQuotaSnapshotTeamAuthorizationRow, 0, limit)
	for rows.Next() {
		var item port.GatewayQuotaSnapshotTeamAuthorizationRow
		var limitsJSON pgtype.Text
		if err := rows.Scan(
			&item.AuthorizationID,
			&item.ResourceOwnerSystemAccountID,
			&item.AuthorizationGranteeSystemAccountID,
			&item.ResourceType,
			&item.ResourceID,
			&item.AuthorizationInstanceAccountID,
			&item.EffectiveSourceTeamID,
			&limitsJSON,
		); err != nil {
			return port.GatewayQuotaSnapshotRows[port.GatewayQuotaSnapshotTeamAuthorizationRow]{}, fmt.Errorf("scan gateway quota snapshot team authorization: %w", err)
		}
		limits, err := managementAuthorizationLimitsFromJSON(limitsJSON)
		if err != nil {
			return port.GatewayQuotaSnapshotRows[port.GatewayQuotaSnapshotTeamAuthorizationRow]{}, err
		}
		item.Limits = limits
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return port.GatewayQuotaSnapshotRows[port.GatewayQuotaSnapshotTeamAuthorizationRow]{}, fmt.Errorf("iterate gateway quota snapshot team authorizations: %w", err)
	}
	return boundedGatewayQuotaSnapshotRows(items, limit), nil
}

func (s *Store) LoadGatewayQuotaSnapshotCosts(ctx context.Context, inputs []port.GatewayQuotaCostLookupInput) (map[string]port.GatewayQuotaCosts, error) {
	requests := uniqueGatewayQuotaCostLookupInputs(inputs)
	output := make(map[string]port.GatewayQuotaCosts, len(requests))
	for _, request := range requests {
		output[request.Key] = port.GatewayQuotaCosts{}
	}
	if len(requests) == 0 {
		return output, nil
	}
	if err := s.loadGatewayQuotaCostRows(ctx, output, requests, "juhe_stats.usage_stats_totals", []string{"system_account_id", "scope_type", "scope_id"}, func(request port.GatewayQuotaCostLookupInput) []any {
		return []any{request.SystemAccountID, request.ScopeType, request.ScopeID}
	}, func(costs *port.GatewayQuotaCosts, value float64) { costs.Total = value }); err != nil {
		return nil, err
	}
	if err := s.loadGatewayQuotaCostRows(ctx, output, requests, "juhe_stats.usage_stats_daily", []string{"system_account_id", "scope_type", "scope_id", "stat_date"}, func(request port.GatewayQuotaCostLookupInput) []any {
		return []any{request.SystemAccountID, request.ScopeType, request.ScopeID, request.StatDate}
	}, func(costs *port.GatewayQuotaCosts, value float64) { costs.Daily = value }); err != nil {
		return nil, err
	}
	if err := s.loadGatewayQuotaCostRows(ctx, output, requests, "juhe_stats.usage_stats_weekly", []string{"system_account_id", "scope_type", "scope_id", "stat_week"}, func(request port.GatewayQuotaCostLookupInput) []any {
		return []any{request.SystemAccountID, request.ScopeType, request.ScopeID, request.StatWeek}
	}, func(costs *port.GatewayQuotaCosts, value float64) { costs.Weekly = value }); err != nil {
		return nil, err
	}
	if err := s.loadGatewayQuotaCostRows(ctx, output, requests, "juhe_stats.usage_stats_monthly", []string{"system_account_id", "scope_type", "scope_id", "stat_month"}, func(request port.GatewayQuotaCostLookupInput) []any {
		return []any{request.SystemAccountID, request.ScopeType, request.ScopeID, request.StatMonth}
	}, func(costs *port.GatewayQuotaCosts, value float64) { costs.Monthly = value }); err != nil {
		return nil, err
	}
	hourlyRequests := make([]port.GatewayQuotaCostLookupInput, 0, len(requests))
	for _, request := range requests {
		if request.HourlyWindowHours > 0 {
			hourlyRequests = append(hourlyRequests, request)
		}
	}
	if err := s.loadGatewayQuotaCostRows(ctx, output, hourlyRequests, "juhe_stats.usage_quota_hourly_windows", []string{"system_account_id", "scope_type", "scope_id", "window_hours"}, func(request port.GatewayQuotaCostLookupInput) []any {
		return []any{request.SystemAccountID, request.ScopeType, request.ScopeID, request.HourlyWindowHours}
	}, func(costs *port.GatewayQuotaCosts, value float64) { costs.Hourly = value }); err != nil {
		return nil, err
	}
	return output, nil
}

func (s *Store) loadGatewayQuotaCostRows(
	ctx context.Context,
	output map[string]port.GatewayQuotaCosts,
	requests []port.GatewayQuotaCostLookupInput,
	tableName string,
	columns []string,
	tupleFor func(port.GatewayQuotaCostLookupInput) []any,
	apply func(*port.GatewayQuotaCosts, float64),
) error {
	keysByTuple := map[string][]string{}
	tuples := make([][]any, 0, len(requests))
	for _, request := range requests {
		tuple := tupleFor(request)
		if !completeGatewayQuotaTuple(tuple) {
			continue
		}
		key := gatewayQuotaTupleKey(tuple)
		if _, exists := keysByTuple[key]; !exists {
			tuples = append(tuples, tuple)
		}
		keysByTuple[key] = append(keysByTuple[key], request.Key)
	}
	if len(tuples) == 0 {
		return nil
	}
	chunkSize := max(1, 800/max(1, len(columns)))
	for index := 0; index < len(tuples); index += chunkSize {
		end := min(len(tuples), index+chunkSize)
		query, args := gatewayQuotaCostRowsQuery(tableName, columns, tuples[index:end])
		rows, err := s.pool.Query(ctx, query, args...)
		if err != nil {
			return fmt.Errorf("load gateway quota costs from %s: %w", tableName, err)
		}
		if err := scanGatewayQuotaCostRows(rows, output, keysByTuple, apply); err != nil {
			return fmt.Errorf("scan gateway quota costs from %s: %w", tableName, err)
		}
	}
	return nil
}

func scanGatewayQuotaCostRows(
	rows pgx.Rows,
	output map[string]port.GatewayQuotaCosts,
	keysByTuple map[string][]string,
	apply func(*port.GatewayQuotaCosts, float64),
) error {
	defer rows.Close()
	for rows.Next() {
		values, err := rows.Values()
		if err != nil {
			return err
		}
		if len(values) < 2 {
			return fmt.Errorf("gateway quota cost row returned %d values", len(values))
		}
		totalCost, ok := values[len(values)-1].(float64)
		if !ok {
			return fmt.Errorf("gateway quota total_cost has unexpected type %T", values[len(values)-1])
		}
		tuple := values[:len(values)-1]
		for _, key := range keysByTuple[gatewayQuotaTupleKey(tuple)] {
			costs := output[key]
			apply(&costs, totalCost)
			output[key] = costs
		}
	}
	return rows.Err()
}

func gatewayQuotaCostRowsQuery(tableName string, columns []string, tuples [][]any) (string, []any) {
	args := make([]any, 0, len(tuples)*len(columns))
	clauses := make([]string, 0, len(tuples))
	for _, tuple := range tuples {
		parts := make([]string, 0, len(columns))
		for columnIndex, column := range columns {
			args = append(args, tuple[columnIndex])
			parts = append(parts, fmt.Sprintf("%s = $%d", column, len(args)))
		}
		clauses = append(clauses, "("+strings.Join(parts, " AND ")+")")
	}
	selectColumns := append([]string{}, columns...)
	selectColumns = append(selectColumns, "CAST(COALESCE(total_cost_usd, 0) AS double precision) AS total_cost")
	return fmt.Sprintf("SELECT %s FROM %s WHERE %s", strings.Join(selectColumns, ", "), tableName, strings.Join(clauses, " OR ")), args
}

func uniqueGatewayQuotaCostLookupInputs(inputs []port.GatewayQuotaCostLookupInput) []port.GatewayQuotaCostLookupInput {
	seen := map[string]bool{}
	output := make([]port.GatewayQuotaCostLookupInput, 0, len(inputs))
	for _, input := range inputs {
		key := strings.TrimSpace(input.Key)
		if key == "" || seen[key] {
			continue
		}
		input.Key = key
		seen[key] = true
		output = append(output, input)
	}
	return output
}

func boundedGatewayQuotaSnapshotRows[T any](items []T, limit int) port.GatewayQuotaSnapshotRows[T] {
	complete := len(items) <= limit
	if !complete {
		items = items[:limit]
	}
	return port.GatewayQuotaSnapshotRows[T]{
		Rows:     items,
		Complete: complete,
	}
}

func completeGatewayQuotaTuple(tuple []any) bool {
	for _, value := range tuple {
		switch typed := value.(type) {
		case string:
			if strings.TrimSpace(typed) == "" {
				return false
			}
		case int:
			if typed <= 0 {
				return false
			}
		case nil:
			return false
		}
	}
	return true
}

func gatewayQuotaTupleKey(tuple []any) string {
	parts := make([]string, 0, len(tuple))
	for _, value := range tuple {
		parts = append(parts, fmt.Sprint(value))
	}
	return strings.Join(parts, "\x00")
}

var _ port.GatewayQuotaSnapshotReader = (*Store)(nil)
