package postgres

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"juhe-ai/backend-go/internal/store/port"
	"juhe-ai/backend-go/internal/store/postgres/postgresqueries"
)

type managementAccountListQueries interface {
	ListManagementAccounts(context.Context, postgresqueries.ListManagementAccountsParams) ([]postgresqueries.ListManagementAccountsRow, error)
}

type managementAccountListPoolQueries struct{ pool *pgxpool.Pool }

const listManagementAccountsSQL = `WITH visible_accounts AS (
SELECT a.id,a.system_account_id,sa.display_name AS system_account_name,a.name,a.provider_code,a.type,a.status,a.schedulable,a.concurrency_limit,a.priority,a.super_priority_enabled,a.fallback_enabled,a.account_expires_at,a.last_used_at,a.updated_at,'owner'::text AS access_type,NULL::text AS account_authorization_id,NULL::text AS authorization_status,NULL::timestamptz AS authorization_expires_at
FROM juhe_business.accounts a INNER JOIN juhe_business.system_accounts sa ON sa.id=a.system_account_id
WHERE a.deleted_at IS NULL AND a.authorization_instance_source_account_id IS NULL AND a.authorization_instance_authorization_id IS NULL AND a.authorization_instance_owner_system_account_id IS NULL AND ($1::text='' OR a.system_account_id=$1::text)
UNION ALL
SELECT a.id,a.system_account_id,ga.display_name,a.name,src.provider_code,src.type,a.status,a.schedulable,src.concurrency_limit,a.priority,a.super_priority_enabled,a.fallback_enabled,a.account_expires_at,a.last_used_at,a.updated_at,'authorized'::text,ra.id,ra.status,ra.expires_at
FROM juhe_business.accounts a INNER JOIN juhe_business.accounts src ON src.id=a.authorization_instance_source_account_id AND src.deleted_at IS NULL INNER JOIN juhe_business.resource_authorizations ra ON ra.id=a.authorization_instance_authorization_id AND ra.resource_type='account' AND ra.resource_id=src.id AND ra.grantee_system_account_id=a.system_account_id AND ra.status IN ('active','paused','expired') INNER JOIN juhe_business.system_accounts ga ON ga.id=a.system_account_id
WHERE a.deleted_at IS NULL AND $1::text<>'' AND a.system_account_id=$1::text
)
SELECT v.id,v.system_account_id,v.system_account_name,v.name,v.provider_code,v.type,v.status,v.schedulable,v.concurrency_limit,v.priority,v.super_priority_enabled,v.fallback_enabled,v.account_expires_at,v.last_used_at,v.access_type,v.account_authorization_id,v.authorization_status,v.authorization_expires_at,coalesce(u.request_count,0)::bigint,coalesce(u.input_tokens,0)::bigint,coalesce(u.output_tokens,0)::bigint,coalesce(u.total_cost_usd,0)::double precision,CASE WHEN coalesce(u.request_count,0)>0 THEN round(u.success_count::numeric*1000000/u.request_count)::bigint ELSE NULL::bigint END
FROM visible_accounts v LEFT JOIN juhe_stats.usage_stats_totals u ON u.system_account_id=v.system_account_id AND u.scope_type=CASE WHEN v.access_type='authorized' THEN 'account_authorization' ELSE 'account' END AND u.scope_id=coalesce(v.account_authorization_id,v.id)
WHERE ($2::text='' OR v.name ILIKE '%'||$2::text||'%') AND ($3::text='' OR v.provider_code=$3::text) AND ($4::text='' OR v.type=$4::text) AND (cardinality($5::text[])=0 OR v.status=ANY($5::text[])) AND (cardinality($6::text[])=0 OR (SELECT count(DISTINCT b.tag_id) FROM juhe_business.account_tag_bindings b WHERE b.account_id=v.id AND b.system_account_id=v.system_account_id AND b.tag_id=ANY($6::text[]))=cardinality($6::text[])) AND ($7::text='all' OR ($7::text='enabled' AND v.schedulable) OR ($7::text IN ('disabled','cooling') AND NOT v.schedulable)) AND ($8::text='' OR EXISTS (SELECT 1 FROM juhe_business.group_accounts b WHERE b.system_account_id=v.system_account_id AND b.account_id=v.id AND b.group_id=$8::text AND b.enabled=true))
ORDER BY CASE WHEN $9='priority' AND $10='asc' THEN v.priority END ASC,CASE WHEN $9='priority' AND $10='desc' THEN v.priority END DESC,CASE WHEN $9='qualityScore' AND $10='asc' THEN u.success_count::numeric/NULLIF(u.request_count,0) END ASC NULLS LAST,CASE WHEN $9='qualityScore' AND $10='desc' THEN u.success_count::numeric/NULLIF(u.request_count,0) END DESC NULLS LAST,CASE WHEN $9='name' AND $10='asc' THEN v.name END ASC,CASE WHEN $9='name' AND $10='desc' THEN v.name END DESC,v.updated_at DESC,v.id DESC LIMIT $11 OFFSET $12`

func (q managementAccountListPoolQueries) ListManagementAccounts(ctx context.Context, arg postgresqueries.ListManagementAccountsParams) ([]postgresqueries.ListManagementAccountsRow, error) {
	rows, err := q.pool.Query(ctx, listManagementAccountsSQL, arg.SystemAccountID, arg.Keyword, arg.ProviderCode, arg.AccountType, arg.Statuses, arg.TagIDs, arg.Schedulable, arg.GroupID, arg.SortField, arg.SortOrder, arg.RowLimit, arg.RowOffset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []postgresqueries.ListManagementAccountsRow{}
	for rows.Next() {
		var row postgresqueries.ListManagementAccountsRow
		if err := rows.Scan(&row.ID, &row.SystemAccountID, &row.SystemAccountName, &row.Name, &row.ProviderCode, &row.Type, &row.Status, &row.Schedulable, &row.ConcurrencyLimit, &row.Priority, &row.SuperPriorityEnabled, &row.FallbackEnabled, &row.AccountExpiresAt, &row.LastUsedAt, &row.AccessType, &row.AccountAuthorizationID, &row.AuthorizationStatus, &row.AuthorizationExpiresAt, &row.RequestCount, &row.InputTokens, &row.OutputTokens, &row.TotalCost, &row.QualityScore); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (s *Store) ListManagementAccounts(ctx context.Context, input port.ManagementAccountListInput) (port.ManagementAccountListPage, error) {
	return listManagementAccounts(ctx, managementAccountListPoolQueries{pool: s.pool}, input)
}

func listManagementAccounts(ctx context.Context, q managementAccountListQueries, input port.ManagementAccountListInput) (port.ManagementAccountListPage, error) {
	var sortField, sortOrder string
	if len(input.Sorts) > 0 {
		sortField, sortOrder = input.Sorts[0].Field, input.Sorts[0].Order
	}
	rows, err := q.ListManagementAccounts(ctx, postgresqueries.ListManagementAccountsParams{SystemAccountID: strings.TrimSpace(input.SystemAccountID), Keyword: strings.TrimSpace(input.Keyword), ProviderCode: strings.TrimSpace(input.ProviderCode), AccountType: strings.TrimSpace(input.Type), Statuses: input.Statuses, TagIDs: input.TagIDs, Schedulable: input.Schedulable, GroupID: strings.TrimSpace(input.GroupID), SortField: sortField, SortOrder: sortOrder, RowLimit: int32(input.Limit), RowOffset: int32(input.Offset)})
	if err != nil {
		return port.ManagementAccountListPage{}, fmt.Errorf("list management accounts: %w", err)
	}
	items := make([]port.ManagementAccountListRow, 0, len(rows))
	for _, row := range rows {
		items = append(items, port.ManagementAccountListRow{ID: row.ID, SystemAccountID: row.SystemAccountID, SystemAccountName: row.SystemAccountName, Name: row.Name, ProviderCode: row.ProviderCode, Type: row.Type, Status: row.Status, Schedulable: row.Schedulable, ConcurrencyLimit: int(row.ConcurrencyLimit), Priority: int(row.Priority), SuperPriorityEnabled: row.SuperPriorityEnabled, FallbackEnabled: row.FallbackEnabled, AccountExpiresAt: nullableTime(row.AccountExpiresAt), LastUsedAt: nullableTime(row.LastUsedAt), AccessType: row.AccessType, AccountAuthorizationID: nullableText(row.AccountAuthorizationID), AuthorizationStatus: nullableText(row.AuthorizationStatus), AuthorizationExpiresAt: nullableTime(row.AuthorizationExpiresAt), RequestCount: row.RequestCount, InputTokens: row.InputTokens, OutputTokens: row.OutputTokens, TotalCost: row.TotalCost, QualityScore: nullableInt64(row.QualityScore)})
	}
	return port.ManagementAccountListPage{Rows: items, HasMore: len(items) > input.Limit}, nil
}

func nullableTime(value pgtype.Timestamptz) *time.Time {
	if !value.Valid {
		return nil
	}
	result := value.Time
	return &result
}
func nullableText(value pgtype.Text) string {
	if !value.Valid {
		return ""
	}
	return value.String
}
func nullableInt64(value pgtype.Int8) *int64 {
	if !value.Valid {
		return nil
	}
	result := value.Int64
	return &result
}

var _ port.ManagementAccountListReader = (*Store)(nil)
var _ = pgx.ErrNoRows
