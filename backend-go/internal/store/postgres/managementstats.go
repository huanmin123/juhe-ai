package postgres

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"golang.org/x/text/unicode/norm"

	"juhe-ai/backend-go/internal/store/port"
)

const managementStatsAccountUsageColumns = `
  usage_window.scope_id AS id,
  accounts.name,
  accounts.type,
  accounts.status,
  accounts.provider_code,
  accounts.system_account_id,
  COALESCE(system_accounts.display_name, system_accounts.username, accounts.system_account_id) AS system_account_name,
  accounts.system_account_id AS owner_system_account_id,
  COALESCE(system_accounts.display_name, system_accounts.username, accounts.system_account_id) AS owner_system_account_name,
  CASE
    WHEN $4::text = 'account' THEN 'owner'
    WHEN accounts.authorization_instance_authorization_id IS NOT NULL THEN 'authorized'
    WHEN accounts.system_account_id = $2::text THEN 'owner'
    ELSE 'authorized'
  END AS access_type,
  usage_window.request_count,
  usage_window.input_tokens,
  usage_window.output_tokens,
  usage_window.cache_read_tokens,
  CAST(usage_window.cache_read_cost_usd AS double precision) AS cache_read_cost_usd,
  usage_window.cache_write_tokens,
  usage_window.cache_write_1h_tokens,
  CAST(usage_window.cache_write_cost_usd AS double precision) AS cache_write_cost_usd,
  usage_window.thinking_tokens,
  usage_window.input_image_tokens,
  usage_window.output_image_tokens,
  CAST(usage_window.total_cost_usd AS double precision) AS total_cost_usd,
  usage_window.last_used_at`

type managementStatsAccountUsageRow struct {
	ID                     string      `db:"id"`
	Name                   string      `db:"name"`
	Type                   string      `db:"type"`
	Status                 string      `db:"status"`
	ProviderCode           string      `db:"provider_code"`
	SystemAccountID        string      `db:"system_account_id"`
	SystemAccountName      string      `db:"system_account_name"`
	OwnerSystemAccountID   string      `db:"owner_system_account_id"`
	OwnerSystemAccountName string      `db:"owner_system_account_name"`
	AccessType             string      `db:"access_type"`
	RequestCount           int64       `db:"request_count"`
	InputTokens            int64       `db:"input_tokens"`
	OutputTokens           int64       `db:"output_tokens"`
	CacheReadTokens        int64       `db:"cache_read_tokens"`
	CacheReadCostUSD       float64     `db:"cache_read_cost_usd"`
	CacheWriteTokens       int64       `db:"cache_write_tokens"`
	CacheWrite1hTokens     int64       `db:"cache_write_1h_tokens"`
	CacheWriteCostUSD      float64     `db:"cache_write_cost_usd"`
	ThinkingTokens         int64       `db:"thinking_tokens"`
	InputImageTokens       int64       `db:"input_image_tokens"`
	OutputImageTokens      int64       `db:"output_image_tokens"`
	TotalCostUSD           float64     `db:"total_cost_usd"`
	LastUsedAt             pgtype.Text `db:"last_used_at"`
}

type managementStatsUsageAggregateRow struct {
	RequestCount       int64       `db:"request_count"`
	InputTokens        int64       `db:"input_tokens"`
	OutputTokens       int64       `db:"output_tokens"`
	CacheReadTokens    int64       `db:"cache_read_tokens"`
	CacheReadCostUSD   float64     `db:"cache_read_cost_usd"`
	CacheWriteTokens   int64       `db:"cache_write_tokens"`
	CacheWrite1hTokens int64       `db:"cache_write_1h_tokens"`
	CacheWriteCostUSD  float64     `db:"cache_write_cost_usd"`
	ThinkingTokens     int64       `db:"thinking_tokens"`
	InputImageTokens   int64       `db:"input_image_tokens"`
	OutputImageTokens  int64       `db:"output_image_tokens"`
	TotalCostUSD       float64     `db:"total_cost_usd"`
	LastUsedAt         pgtype.Text `db:"last_used_at"`
}

func (s *Store) ReadManagementAccountUsage(ctx context.Context, input port.ManagementAccountUsageReadInput) (port.ManagementAccountUsageReadResult, error) {
	pageSize := min(max(input.PageSize, 1), 200)
	page := max(input.Page, 1)
	offset := min((page-1)*pageSize, 1000-pageSize)
	input.AccountIDs = boundedManagementStatsIDs(input.AccountIDs, 50)
	pageRows, err := s.readManagementAccountUsageRows(ctx, input, pageSize+1, offset, nil)
	if err != nil {
		return port.ManagementAccountUsageReadResult{}, err
	}
	hasMore := len(pageRows) > pageSize
	if hasMore {
		pageRows = pageRows[:pageSize]
	}
	selectedRows, err := s.readManagementAccountUsageRows(ctx, input, len(input.AccountIDs), 0, input.AccountIDs)
	if err != nil {
		return port.ManagementAccountUsageReadResult{}, err
	}
	merged := mergeManagementAccountUsageRows(pageRows, selectedRows)
	summary, err := s.readManagementAccountUsageSummary(ctx, input)
	if err != nil {
		return port.ManagementAccountUsageReadResult{}, err
	}
	defaultIDs, err := s.readManagementStatsRankIDs(ctx, input.Scope, 10)
	if err != nil {
		return port.ManagementAccountUsageReadResult{}, err
	}
	return port.ManagementAccountUsageReadResult{Rows: mapManagementAccountUsageRows(merged), Summary: summary, DefaultTrendAccountIDs: defaultIDs, PageRowCount: len(pageRows), HasMore: hasMore}, nil
}

func (s *Store) readManagementAccountUsageRows(ctx context.Context, input port.ManagementAccountUsageReadInput, limit, offset int, selectedIDs []string) ([]managementStatsAccountUsageRow, error) {
	if limit <= 0 {
		return nil, nil
	}
	keyword := norm.NFKC.String(strings.TrimSpace(input.Keyword))
	visibilitySQL := managementStatsVisibilitySQL(input.Scope, "accounts", true)
	selectionSQL := ""
	keywordJoinSQL := ""
	keywordPredicateSQL := ""
	positiveSQL := `AND (
    usage_window.request_count > 0
    OR usage_window.input_tokens > 0
    OR usage_window.output_tokens > 0
    OR usage_window.cache_read_tokens > 0
    OR usage_window.total_cost_usd > 0
    OR usage_window.last_used_at IS NOT NULL
  )`
	args := []any{input.Scope.SystemAccountID, input.Scope.ViewerSystemAccountID, input.Range.StartDate + ":" + input.Range.EndDate, input.Scope.ScopeType}
	if len(selectedIDs) > 0 {
		selectionSQL = fmt.Sprintf("AND usage_window.scope_id = ANY($%d::text[])", len(args)+1)
		positiveSQL = ""
		args = append(args, selectedIDs)
	} else if keyword != "" {
		keywordArg := len(args) + 1
		keywordUpperArg := keywordArg + 1
		keywordJoinSQL = `LEFT JOIN juhe_business.accounts AS source_accounts ON source_accounts.id = accounts.authorization_instance_source_account_id`
		keywordPredicateSQL = fmt.Sprintf(`AND (
    (accounts.name COLLATE "C" >= $%[1]d::text AND accounts.name COLLATE "C" < $%[2]d::text AND starts_with(accounts.name, $%[1]d::text))
    OR (accounts.provider_code COLLATE "C" >= $%[1]d::text AND accounts.provider_code COLLATE "C" < $%[2]d::text AND starts_with(accounts.provider_code, $%[1]d::text))
    OR (accounts.type COLLATE "C" >= $%[1]d::text AND accounts.type COLLATE "C" < $%[2]d::text AND starts_with(accounts.type, $%[1]d::text))
    OR (source_accounts.deleted_at IS NULL AND source_accounts.name COLLATE "C" >= $%[1]d::text AND source_accounts.name COLLATE "C" < $%[2]d::text AND starts_with(source_accounts.name, $%[1]d::text))
    OR EXISTS (
      SELECT 1
      FROM juhe_business.group_accounts AS keyword_group_accounts
      INNER JOIN juhe_business.groups AS keyword_groups ON keyword_groups.id = keyword_group_accounts.group_id
      WHERE keyword_group_accounts.account_id = accounts.id
        AND keyword_group_accounts.system_account_id = $2::text
        AND keyword_group_accounts.enabled = true
        AND keyword_groups.name COLLATE "C" >= $%[1]d::text
        AND keyword_groups.name COLLATE "C" < $%[2]d::text
        AND starts_with(keyword_groups.name, $%[1]d::text)
    )
  )`, keywordArg, keywordUpperArg)
		args = append(args, keyword, managementStatsPrefixUpperBound(keyword))
	}
	limitArg := len(args) + 1
	offsetArg := len(args) + 2
	args = append(args, limit, offset)
	query := `SELECT` + managementStatsAccountUsageColumns + `
FROM juhe_stats.usage_scope_range_windows AS usage_window
INNER JOIN juhe_business.accounts AS accounts ON accounts.id = usage_window.scope_id
LEFT JOIN juhe_business.system_accounts AS system_accounts ON system_accounts.id = accounts.system_account_id
` + keywordJoinSQL + `
WHERE usage_window.system_account_id = $1::text
  AND usage_window.scope_type = $4::text
  AND usage_window.window_key = $3::text
  AND accounts.deleted_at IS NULL
  AND (` + visibilitySQL + `)
  ` + keywordPredicateSQL + `
  ` + selectionSQL + `
  ` + positiveSQL + `
ORDER BY usage_window.request_count DESC,
  usage_window.total_cost_usd DESC,
  (usage_window.input_tokens + usage_window.output_tokens) DESC,
  usage_window.last_used_at DESC,
  usage_window.scope_id ASC
LIMIT $` + fmt.Sprint(limitArg) + `::integer
OFFSET $` + fmt.Sprint(offsetArg) + `::integer`
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("read management account usage rows: %w", err)
	}
	result, err := pgx.CollectRows(rows, pgx.RowToStructByNameLax[managementStatsAccountUsageRow])
	if err != nil {
		return nil, fmt.Errorf("scan management account usage rows: %w", err)
	}
	return result, nil
}

func (s *Store) readManagementAccountUsageSummary(ctx context.Context, input port.ManagementAccountUsageReadInput) (port.ManagementUsageAggregate, error) {
	scopeID := input.Scope.SystemAccountID
	if input.Scope.ScopeType == "account" {
		scopeID = "global"
	}
	rows, err := s.pool.Query(ctx, `
SELECT request_count, input_tokens, output_tokens, cache_read_tokens,
  CAST(cache_read_cost_usd AS double precision) AS cache_read_cost_usd,
  cache_write_tokens, cache_write_1h_tokens,
  CAST(cache_write_cost_usd AS double precision) AS cache_write_cost_usd,
  thinking_tokens, input_image_tokens, output_image_tokens,
  CAST(total_cost_usd AS double precision) AS total_cost_usd,
  last_used_at
FROM juhe_stats.usage_scope_range_windows
WHERE system_account_id = $1::text
  AND scope_type = 'system_account'
  AND scope_id = $2::text
  AND window_key = $3::text
LIMIT $4::integer
OFFSET $5::integer`, input.Scope.SystemAccountID, scopeID, input.Range.StartDate+":"+input.Range.EndDate, 1, 0)
	if err != nil {
		return port.ManagementUsageAggregate{}, fmt.Errorf("read management account usage summary: %w", err)
	}
	result, err := pgx.CollectRows(rows, pgx.RowToStructByNameLax[managementStatsUsageAggregateRow])
	if err != nil {
		return port.ManagementUsageAggregate{}, fmt.Errorf("scan management account usage summary: %w", err)
	}
	if len(result) == 0 {
		return port.ManagementUsageAggregate{}, nil
	}
	return mapManagementUsageAggregate(result[0]), nil
}

func (s *Store) ReadManagementAccountUsageTrend(ctx context.Context, input port.ManagementAccountUsageTrendReadInput) (port.ManagementAccountUsageTrendReadResult, error) {
	input.AccountIDs = boundedManagementStatsIDs(input.AccountIDs, 10)
	usageRows, err := s.readManagementAccountUsageRows(ctx, port.ManagementAccountUsageReadInput{Scope: input.Scope, Range: input.Range}, len(input.AccountIDs), 0, input.AccountIDs)
	if err != nil {
		return port.ManagementAccountUsageTrendReadResult{}, err
	}
	accounts := make([]port.ManagementStatsAccount, 0, len(usageRows))
	for _, row := range usageRows {
		accounts = append(accounts, port.ManagementStatsAccount{ID: row.ID, Name: row.Name, Type: row.Type, Status: row.Status, ProviderCode: row.ProviderCode, SystemAccountID: row.SystemAccountID, SystemAccountName: row.SystemAccountName, OwnerSystemAccountID: row.OwnerSystemAccountID, OwnerSystemAccountName: row.OwnerSystemAccountName, AccessType: row.AccessType})
	}
	if len(accounts) == 0 {
		return port.ManagementAccountUsageTrendReadResult{Accounts: accounts}, nil
	}
	ids := managementStatsAccountIDs(accounts)
	rows, err := s.pool.Query(ctx, `
SELECT scope_id AS account_id, stat_date, request_count, input_tokens, output_tokens,
  cache_read_tokens, CAST(cache_read_cost_usd AS double precision) AS cache_read_cost_usd,
  cache_write_tokens, cache_write_1h_tokens,
  CAST(cache_write_cost_usd AS double precision) AS cache_write_cost_usd,
  thinking_tokens, input_image_tokens, output_image_tokens,
  CAST(total_cost_usd AS double precision) AS total_cost_usd,
  last_used_at
FROM juhe_stats.usage_stats_daily
WHERE system_account_id = $1::text
  AND scope_type = $2::text
  AND scope_id = ANY($3::text[])
  AND stat_date >= $4::text
  AND stat_date <= $5::text
ORDER BY scope_id ASC, stat_date ASC
LIMIT $6::integer
OFFSET $7::integer`, input.Scope.SystemAccountID, input.Scope.ScopeType, ids, input.Range.StartDate, input.Range.EndDate, len(ids)*31, 0)
	if err != nil {
		return port.ManagementAccountUsageTrendReadResult{}, fmt.Errorf("read management account usage daily rows: %w", err)
	}
	type dailyRow struct {
		AccountID string `db:"account_id"`
		StatDate  string `db:"stat_date"`
		managementStatsUsageAggregateRow
	}
	dailyRows, err := pgx.CollectRows(rows, pgx.RowToStructByNameLax[dailyRow])
	if err != nil {
		return port.ManagementAccountUsageTrendReadResult{}, fmt.Errorf("scan management account usage daily rows: %w", err)
	}
	result := make([]port.ManagementAccountUsageDailyRow, 0, len(dailyRows))
	for _, row := range dailyRows {
		result = append(result, port.ManagementAccountUsageDailyRow{AccountID: row.AccountID, StatDate: row.StatDate, Usage: mapManagementUsageAggregate(row.managementStatsUsageAggregateRow)})
	}
	return port.ManagementAccountUsageTrendReadResult{Accounts: accounts, DailyRows: result}, nil
}

func (s *Store) ReadManagementAIPerformance(ctx context.Context, input port.ManagementAIPerformanceReadInput) (port.ManagementAIPerformanceReadResult, error) {
	input.AccountIDs = boundedManagementStatsIDs(input.AccountIDs, 20)
	defaultAccounts, err := s.readManagementStatsRankedAccounts(ctx, input.Scope, "", 10)
	if err != nil {
		return port.ManagementAIPerformanceReadResult{}, err
	}
	selectedAccounts, err := s.readManagementStatsAccountsByIDs(ctx, input.Scope, input.AccountIDs)
	if err != nil {
		return port.ManagementAIPerformanceReadResult{}, err
	}
	allAccounts := mergeManagementStatsAccounts(defaultAccounts, selectedAccounts)
	hourlyRows, err := s.readManagementAIPerformanceHourly(ctx, input, managementStatsAccountIDs(allAccounts))
	if err != nil {
		return port.ManagementAIPerformanceReadResult{}, err
	}
	summary, err := s.readManagementAIPerformanceSummary(ctx, input)
	if err != nil {
		return port.ManagementAIPerformanceReadResult{}, err
	}
	return port.ManagementAIPerformanceReadResult{DefaultAccounts: defaultAccounts, SelectedAccounts: selectedAccounts, HourlyRows: hourlyRows, Summary: summary}, nil
}

func (s *Store) ReadManagementAIPerformanceAccounts(ctx context.Context, input port.ManagementAIPerformanceAccountsReadInput) ([]port.ManagementStatsAccount, error) {
	input.AccountIDs = boundedManagementStatsIDs(input.AccountIDs, 20)
	limit := min(max(input.Limit, 1), 50)
	searchRows, err := s.readManagementStatsRankedAccounts(ctx, input.Scope, norm.NFKC.String(strings.TrimSpace(input.Keyword)), limit)
	if err != nil {
		return nil, err
	}
	selectedRows, err := s.readManagementStatsAccountsByIDs(ctx, input.Scope, input.AccountIDs)
	if err != nil {
		return nil, err
	}
	return mergeManagementStatsAccounts(searchRows, selectedRows), nil
}

func (s *Store) readManagementStatsRankIDs(ctx context.Context, scope port.ManagementStatsScope, limit int) ([]string, error) {
	rows, err := s.pool.Query(ctx, `
SELECT scope_id
FROM juhe_stats.usage_rank_snapshots
WHERE system_account_id = $1::text
  AND scope_type = $2::text
  AND window_key = 'last7d'
  AND metric = 'request_count'
  AND snapshot_at = (
    SELECT MAX(snapshot_at)
    FROM juhe_stats.usage_rank_snapshots
    WHERE system_account_id = $1::text
      AND scope_type = $2::text
      AND window_key = 'last7d'
      AND metric = 'request_count'
  )
ORDER BY rank ASC
LIMIT $3::integer
OFFSET $4::integer`, scope.SystemAccountID, scope.ScopeType, min(max(limit, 1), 50), 0)
	if err != nil {
		return nil, fmt.Errorf("read management stats rank ids: %w", err)
	}
	result, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("scan management stats rank ids: %w", err)
	}
	return result, nil
}

func (s *Store) readManagementStatsRankedAccounts(ctx context.Context, scope port.ManagementStatsScope, keyword string, limit int) ([]port.ManagementStatsAccount, error) {
	if keyword != "" {
		ids, err := s.readManagementStatsKeywordAccountIDs(ctx, scope, keyword, limit)
		if err != nil {
			return nil, err
		}
		return s.readManagementStatsAccountsByIDs(ctx, scope, ids)
	}
	ids, err := s.readManagementStatsRankIDs(ctx, scope, limit)
	if err != nil {
		return nil, err
	}
	return s.readManagementStatsAccountsByIDs(ctx, scope, ids)
}

func (s *Store) readManagementStatsKeywordAccountIDs(ctx context.Context, scope port.ManagementStatsScope, keyword string, limit int) ([]string, error) {
	upper := managementStatsPrefixUpperBound(keyword)
	visibilitySQL := managementStatsVisibilitySQL(scope, "accounts", false)
	rows, err := s.pool.Query(ctx, `
SELECT accounts.id
FROM juhe_business.accounts AS accounts
LEFT JOIN juhe_business.accounts AS source_accounts ON source_accounts.id = accounts.authorization_instance_source_account_id
WHERE accounts.deleted_at IS NULL
  AND (`+visibilitySQL+`)
  AND (
    (accounts.name COLLATE "C" >= $3::text AND accounts.name COLLATE "C" < $4::text AND starts_with(accounts.name, $3::text))
    OR (source_accounts.deleted_at IS NULL AND source_accounts.name COLLATE "C" >= $3::text AND source_accounts.name COLLATE "C" < $4::text AND starts_with(source_accounts.name, $3::text))
  )
ORDER BY COALESCE(source_accounts.name, accounts.name) COLLATE "C" ASC, accounts.id ASC
LIMIT $5::integer
OFFSET $6::integer`, scope.SystemAccountID, scope.ViewerSystemAccountID, keyword, upper, min(max(limit, 1), 50), 0)
	if err != nil {
		return nil, fmt.Errorf("read management stats keyword account ids: %w", err)
	}
	result, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("scan management stats keyword account ids: %w", err)
	}
	return result, nil
}

type managementStatsAccountRow struct {
	ID                     string      `db:"id"`
	Name                   string      `db:"name"`
	Type                   string      `db:"type"`
	Status                 string      `db:"status"`
	ProviderCode           string      `db:"provider_code"`
	SystemAccountID        string      `db:"system_account_id"`
	SystemAccountName      pgtype.Text `db:"system_account_name"`
	OwnerSystemAccountID   string      `db:"owner_system_account_id"`
	OwnerSystemAccountName pgtype.Text `db:"owner_system_account_name"`
	AccessType             string      `db:"access_type"`
	RequestCountLast7d     int64       `db:"request_count_last_7d"`
}

func (s *Store) readManagementStatsAccountsByIDs(ctx context.Context, scope port.ManagementStatsScope, ids []string) ([]port.ManagementStatsAccount, error) {
	ids = boundedManagementStatsIDs(ids, 50)
	if len(ids) == 0 {
		return []port.ManagementStatsAccount{}, nil
	}
	visibilitySQL := managementStatsVisibilitySQL(scope, "accounts", false)
	rows, err := s.pool.Query(ctx, `
WITH selected AS (
  SELECT id, ord
  FROM unnest($3::text[]) WITH ORDINALITY AS selected(id, ord)
), latest_rank AS (
  SELECT scope_id, metric_value
  FROM juhe_stats.usage_rank_snapshots
  WHERE system_account_id = $1::text
    AND scope_type = $4::text
    AND window_key = 'last7d'
    AND metric = 'request_count'
    AND snapshot_at = (
      SELECT MAX(snapshot_at)
      FROM juhe_stats.usage_rank_snapshots
      WHERE system_account_id = $1::text
        AND scope_type = $4::text
        AND window_key = 'last7d'
        AND metric = 'request_count'
    )
)
SELECT accounts.id, accounts.name, accounts.type, accounts.status, accounts.provider_code,
  accounts.system_account_id,
  system_accounts.display_name AS system_account_name,
  CASE
    WHEN accounts.authorization_instance_authorization_id IS NOT NULL
    THEN COALESCE(accounts.authorization_instance_owner_system_account_id, instance_authorizations.resource_owner_system_account_id, accounts.system_account_id)
    ELSE accounts.system_account_id
  END AS owner_system_account_id,
  owner_system_accounts.display_name AS owner_system_account_name,
  CASE
    WHEN $4::text = 'account' THEN 'owner'
    WHEN accounts.authorization_instance_authorization_id IS NOT NULL THEN 'authorized'
    WHEN accounts.system_account_id = $2::text THEN 'owner'
    ELSE 'authorized'
  END AS access_type,
  COALESCE(latest_rank.metric_value, 0)::bigint AS request_count_last_7d
FROM selected
INNER JOIN juhe_business.accounts AS accounts ON accounts.id = selected.id
LEFT JOIN juhe_business.system_accounts AS system_accounts ON system_accounts.id = accounts.system_account_id
LEFT JOIN juhe_business.resource_authorizations AS instance_authorizations ON instance_authorizations.id = accounts.authorization_instance_authorization_id
LEFT JOIN juhe_business.system_accounts AS owner_system_accounts ON owner_system_accounts.id = CASE
  WHEN accounts.authorization_instance_authorization_id IS NOT NULL
  THEN COALESCE(accounts.authorization_instance_owner_system_account_id, instance_authorizations.resource_owner_system_account_id, accounts.system_account_id)
  ELSE accounts.system_account_id
END
LEFT JOIN latest_rank ON latest_rank.scope_id = accounts.id
WHERE accounts.deleted_at IS NULL
  AND (`+visibilitySQL+`)
ORDER BY selected.ord ASC
LIMIT $5::integer
OFFSET $6::integer`, scope.SystemAccountID, scope.ViewerSystemAccountID, ids, scope.ScopeType, len(ids), 0)
	if err != nil {
		return nil, fmt.Errorf("read management stats accounts: %w", err)
	}
	result, err := pgx.CollectRows(rows, pgx.RowToStructByNameLax[managementStatsAccountRow])
	if err != nil {
		return nil, fmt.Errorf("scan management stats accounts: %w", err)
	}
	accounts := make([]port.ManagementStatsAccount, 0, len(result))
	for _, row := range result {
		accounts = append(accounts, port.ManagementStatsAccount{ID: row.ID, Name: row.Name, Type: row.Type, Status: row.Status, ProviderCode: row.ProviderCode, SystemAccountID: row.SystemAccountID, SystemAccountName: textValue(row.SystemAccountName), OwnerSystemAccountID: row.OwnerSystemAccountID, OwnerSystemAccountName: textValue(row.OwnerSystemAccountName), AccessType: row.AccessType, RequestCountLast7d: row.RequestCountLast7d})
	}
	return accounts, nil
}

func managementStatsVisibilitySQL(scope port.ManagementStatsScope, alias string, includeDirect bool) string {
	if scope.ScopeType == "account" && scope.SystemAccountID == "global" {
		return "TRUE"
	}
	directSQL := ""
	if includeDirect {
		directSQL = `OR EXISTS (
      SELECT 1
      FROM juhe_business.resource_authorizations AS visible_direct_authorizations
      WHERE visible_direct_authorizations.resource_type = 'account'
        AND visible_direct_authorizations.resource_id = ` + alias + `.id
        AND visible_direct_authorizations.grantee_system_account_id = $2::text
        AND visible_direct_authorizations.status = 'active'
        AND (visible_direct_authorizations.expires_at IS NULL OR visible_direct_authorizations.expires_at::timestamptz > CURRENT_TIMESTAMP)
    )`
	}
	return `(` + alias + `.system_account_id = $2::text
    ` + directSQL + `
    OR EXISTS (
      SELECT 1
      FROM juhe_business.group_accounts AS visible_group_accounts
      INNER JOIN juhe_business.resource_authorizations AS visible_group_authorizations
        ON visible_group_authorizations.resource_type = 'group'
        AND visible_group_authorizations.resource_id = visible_group_accounts.group_id
        AND visible_group_authorizations.grantee_system_account_id = $2::text
        AND visible_group_authorizations.status = 'active'
        AND (visible_group_authorizations.expires_at IS NULL OR visible_group_authorizations.expires_at::timestamptz > CURRENT_TIMESTAMP)
      WHERE visible_group_accounts.account_id = ` + alias + `.id
        AND visible_group_accounts.enabled = true
    )
  )`
}

func (s *Store) readManagementAIPerformanceHourly(ctx context.Context, input port.ManagementAIPerformanceReadInput, accountIDs []string) ([]port.ManagementAIPerformanceHourlyRow, error) {
	accountIDs = boundedManagementStatsIDs(accountIDs, 30)
	if len(accountIDs) == 0 {
		return []port.ManagementAIPerformanceHourlyRow{}, nil
	}
	rows, err := s.pool.Query(ctx, `
SELECT scope_id AS account_id, stat_hour, request_count,
  first_token_ms_sum, first_token_ms_count, first_token_ms_max,
  duration_ms_sum, duration_ms_count, duration_ms_max
FROM juhe_stats.usage_stats_hourly
WHERE system_account_id = $1::text
  AND scope_type = $2::text
  AND scope_id = ANY($3::text[])
  AND stat_hour >= $4::text
  AND stat_hour <= $5::text
ORDER BY scope_id ASC, stat_hour ASC
LIMIT $6::integer
OFFSET $7::integer`, input.Scope.SystemAccountID, input.Scope.ScopeType, accountIDs, input.Range.StartDate+"T00", input.Range.EndDate+"T23", len(accountIDs)*31*24, 0)
	if err != nil {
		return nil, fmt.Errorf("read management ai performance hourly: %w", err)
	}
	type hourlyRow struct {
		AccountID         string `db:"account_id"`
		StatHour          string `db:"stat_hour"`
		RequestCount      int64  `db:"request_count"`
		FirstTokenMSSum   int64  `db:"first_token_ms_sum"`
		FirstTokenMSCount int64  `db:"first_token_ms_count"`
		FirstTokenMSMax   int64  `db:"first_token_ms_max"`
		DurationMSSum     int64  `db:"duration_ms_sum"`
		DurationMSCount   int64  `db:"duration_ms_count"`
		DurationMSMax     int64  `db:"duration_ms_max"`
	}
	scanned, err := pgx.CollectRows(rows, pgx.RowToStructByNameLax[hourlyRow])
	if err != nil {
		return nil, fmt.Errorf("scan management ai performance hourly: %w", err)
	}
	result := make([]port.ManagementAIPerformanceHourlyRow, 0, len(scanned))
	for _, row := range scanned {
		result = append(result, port.ManagementAIPerformanceHourlyRow{AccountID: row.AccountID, StatHour: row.StatHour, RequestCount: row.RequestCount, FirstTokenMSSum: row.FirstTokenMSSum, FirstTokenMSCount: row.FirstTokenMSCount, FirstTokenMSMax: row.FirstTokenMSMax, DurationMSSum: row.DurationMSSum, DurationMSCount: row.DurationMSCount, DurationMSMax: row.DurationMSMax})
	}
	return result, nil
}

func (s *Store) readManagementAIPerformanceSummary(ctx context.Context, input port.ManagementAIPerformanceReadInput) (port.ManagementAIPerformanceAggregate, error) {
	rows, err := s.pool.Query(ctx, `
SELECT request_count, first_token_ms_sum, first_token_ms_count, first_token_ms_max,
  duration_ms_sum, duration_ms_count, duration_ms_max
FROM juhe_stats.ai_performance_summary_windows
WHERE system_account_id = $1::text
  AND window_key = $2::text
  AND start_date = $3::text
  AND end_date = $4::text
LIMIT $5::integer
OFFSET $6::integer`, input.Scope.SystemAccountID, input.Range.StartDate+":"+input.Range.EndDate, input.Range.StartDate, input.Range.EndDate, 1, 0)
	if err != nil {
		return port.ManagementAIPerformanceAggregate{}, fmt.Errorf("read management ai performance summary: %w", err)
	}
	type summaryRow struct {
		RequestCount      int64 `db:"request_count"`
		FirstTokenMSSum   int64 `db:"first_token_ms_sum"`
		FirstTokenMSCount int64 `db:"first_token_ms_count"`
		FirstTokenMSMax   int64 `db:"first_token_ms_max"`
		DurationMSSum     int64 `db:"duration_ms_sum"`
		DurationMSCount   int64 `db:"duration_ms_count"`
		DurationMSMax     int64 `db:"duration_ms_max"`
	}
	result, err := pgx.CollectRows(rows, pgx.RowToStructByNameLax[summaryRow])
	if err != nil {
		return port.ManagementAIPerformanceAggregate{}, fmt.Errorf("scan management ai performance summary: %w", err)
	}
	if len(result) == 0 {
		return port.ManagementAIPerformanceAggregate{}, nil
	}
	row := result[0]
	return port.ManagementAIPerformanceAggregate{RequestCount: row.RequestCount, FirstTokenMSSum: row.FirstTokenMSSum, FirstTokenMSCount: row.FirstTokenMSCount, FirstTokenMSMax: row.FirstTokenMSMax, DurationMSSum: row.DurationMSSum, DurationMSCount: row.DurationMSCount, DurationMSMax: row.DurationMSMax}, nil
}

func mapManagementAccountUsageRows(rows []managementStatsAccountUsageRow) []port.ManagementAccountUsageRow {
	result := make([]port.ManagementAccountUsageRow, 0, len(rows))
	for _, row := range rows {
		result = append(result, port.ManagementAccountUsageRow{
			Account: port.ManagementStatsAccount{ID: row.ID, Name: row.Name, Type: row.Type, Status: row.Status, ProviderCode: row.ProviderCode, SystemAccountID: row.SystemAccountID, SystemAccountName: row.SystemAccountName, OwnerSystemAccountID: row.OwnerSystemAccountID, OwnerSystemAccountName: row.OwnerSystemAccountName, AccessType: row.AccessType},
			Usage:   port.ManagementUsageAggregate{RequestCount: row.RequestCount, InputTokens: row.InputTokens, OutputTokens: row.OutputTokens, CacheReadTokens: row.CacheReadTokens, CacheReadCostUSD: row.CacheReadCostUSD, CacheWriteTokens: row.CacheWriteTokens, CacheWrite1hTokens: row.CacheWrite1hTokens, CacheWriteCostUSD: row.CacheWriteCostUSD, ThinkingTokens: row.ThinkingTokens, InputImageTokens: row.InputImageTokens, OutputImageTokens: row.OutputImageTokens, TotalCostUSD: row.TotalCostUSD, LastUsedAt: textPtr(row.LastUsedAt)},
		})
	}
	return result
}

func mapManagementUsageAggregate(row managementStatsUsageAggregateRow) port.ManagementUsageAggregate {
	return port.ManagementUsageAggregate{RequestCount: row.RequestCount, InputTokens: row.InputTokens, OutputTokens: row.OutputTokens, CacheReadTokens: row.CacheReadTokens, CacheReadCostUSD: row.CacheReadCostUSD, CacheWriteTokens: row.CacheWriteTokens, CacheWrite1hTokens: row.CacheWrite1hTokens, CacheWriteCostUSD: row.CacheWriteCostUSD, ThinkingTokens: row.ThinkingTokens, InputImageTokens: row.InputImageTokens, OutputImageTokens: row.OutputImageTokens, TotalCostUSD: row.TotalCostUSD, LastUsedAt: textPtr(row.LastUsedAt)}
}

func mergeManagementAccountUsageRows(first, second []managementStatsAccountUsageRow) []managementStatsAccountUsageRow {
	seen := map[string]struct{}{}
	result := make([]managementStatsAccountUsageRow, 0, len(first)+len(second))
	for _, row := range append(append([]managementStatsAccountUsageRow{}, first...), second...) {
		if _, ok := seen[row.ID]; ok {
			continue
		}
		seen[row.ID] = struct{}{}
		result = append(result, row)
	}
	return result
}

func mergeManagementStatsAccounts(first, second []port.ManagementStatsAccount) []port.ManagementStatsAccount {
	seen := map[string]struct{}{}
	result := make([]port.ManagementStatsAccount, 0, len(first)+len(second))
	for _, row := range append(append([]port.ManagementStatsAccount{}, first...), second...) {
		if _, ok := seen[row.ID]; ok {
			continue
		}
		seen[row.ID] = struct{}{}
		result = append(result, row)
	}
	return result
}

func managementStatsAccountIDs(rows []port.ManagementStatsAccount) []string {
	result := make([]string, 0, len(rows))
	for _, row := range rows {
		result = append(result, row.ID)
	}
	return result
}

func boundedManagementStatsIDs(values []string, limit int) []string {
	seen := map[string]struct{}{}
	result := make([]string, 0, min(len(values), limit))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
		if len(result) == limit {
			break
		}
	}
	return result
}

func managementStatsPrefixUpperBound(value string) string {
	runes := []rune(value)
	for index := len(runes) - 1; index >= 0; index-- {
		if runes[index] >= rune(0x10ffff) {
			continue
		}
		return string(append(runes[:index], runes[index]+1))
	}
	return value + "\uffff"
}

var _ port.ManagementStatsReader = (*Store)(nil)
