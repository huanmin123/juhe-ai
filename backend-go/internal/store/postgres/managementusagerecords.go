package postgres

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"golang.org/x/text/unicode/norm"

	"juhe-ai/backend-go/internal/store/port"
)

const managementUsageRecordSelectColumns = `
  ur.id,
  ur.system_account_id,
  sa.display_name AS system_account_name,
  ur.trace_id,
  ur.traffic_source,
  ur.client_ip,
  ur.api_key_id,
  ak.name AS api_key_name,
  ur.group_id,
  grp.name AS group_name,
  ur.account_id,
  acc.name AS account_name,
  ur.endpoint,
  ur.provider_code,
  ur.provider_protocol_profile_id,
  ur.usage_semantic,
  ur.model,
  ur.upstream_model,
  ur.pricing_model,
  ur.requested_service_tier,
  ur.effective_service_tier,
  ur.reported_service_tier,
  ur.billed_service_tier,
  ur.requested_reasoning_effort,
  ur.effective_reasoning_effort,
  ur.cost_breakdown_snapshot_json,
  ur.model_mapping_applied,
  ur.model_mapping_source,
  ur.source_endpoint_family,
  ur.upstream_endpoint_family,
  ur.stream,
  ur.status_code,
  ur.success,
  ur.failure_attribution,
  ur.first_token_ms,
  ur.duration_ms,
  ur.input_tokens,
  ur.output_tokens,
  ur.cache_read_tokens,
  ur.cache_read_cost_usd,
  ur.cache_write_tokens,
  ur.cache_write_1h_tokens,
  ur.cache_write_cost_usd,
  ur.thinking_tokens,
  ur.input_image_tokens,
  ur.output_image_tokens,
  ur.input_audio_tokens,
  ur.output_audio_tokens,
  ur.output_image_count,
  ur.cost_usd,
  ur.error_code,
  ur.error_message,
  ur.created_at`

var managementUsageRecordIDDatePattern = regexp.MustCompile(`^usage_(\d{8})_s\d+_`)

const managementUsageRecordFromClause = `
FROM juhe_usage.usage_records AS ur
LEFT JOIN juhe_business.system_accounts AS sa ON sa.id = ur.system_account_id
LEFT JOIN juhe_business.api_keys AS ak ON ak.id = ur.api_key_id
LEFT JOIN juhe_business.groups AS grp ON grp.id = ur.group_id
LEFT JOIN juhe_business.accounts AS acc ON acc.id = ur.account_id`

type managementUsageRecordRow struct {
	ID                        string        `db:"id"`
	SystemAccountID           pgtype.Text   `db:"system_account_id"`
	SystemAccountName         pgtype.Text   `db:"system_account_name"`
	TraceID                   string        `db:"trace_id"`
	TrafficSource             string        `db:"traffic_source"`
	ClientIP                  pgtype.Text   `db:"client_ip"`
	APIKeyID                  pgtype.Text   `db:"api_key_id"`
	APIKeyName                pgtype.Text   `db:"api_key_name"`
	GroupID                   pgtype.Text   `db:"group_id"`
	GroupName                 pgtype.Text   `db:"group_name"`
	AccountID                 pgtype.Text   `db:"account_id"`
	AccountName               pgtype.Text   `db:"account_name"`
	Endpoint                  pgtype.Text   `db:"endpoint"`
	ProviderCode              pgtype.Text   `db:"provider_code"`
	ProviderProtocolProfileID pgtype.Text   `db:"provider_protocol_profile_id"`
	UsageSemantic             pgtype.Text   `db:"usage_semantic"`
	Model                     pgtype.Text   `db:"model"`
	UpstreamModel             pgtype.Text   `db:"upstream_model"`
	PricingModel              pgtype.Text   `db:"pricing_model"`
	RequestedServiceTier      pgtype.Text   `db:"requested_service_tier"`
	EffectiveServiceTier      pgtype.Text   `db:"effective_service_tier"`
	ReportedServiceTier       pgtype.Text   `db:"reported_service_tier"`
	BilledServiceTier         pgtype.Text   `db:"billed_service_tier"`
	RequestedReasoningEffort  pgtype.Text   `db:"requested_reasoning_effort"`
	EffectiveReasoningEffort  pgtype.Text   `db:"effective_reasoning_effort"`
	CostBreakdownSnapshotJSON pgtype.Text   `db:"cost_breakdown_snapshot_json"`
	ModelMappingApplied       int64         `db:"model_mapping_applied"`
	ModelMappingSource        pgtype.Text   `db:"model_mapping_source"`
	SourceEndpointFamily      pgtype.Text   `db:"source_endpoint_family"`
	UpstreamEndpointFamily    pgtype.Text   `db:"upstream_endpoint_family"`
	Stream                    int64         `db:"stream"`
	StatusCode                pgtype.Int4   `db:"status_code"`
	Success                   int64         `db:"success"`
	FailureAttribution        pgtype.Text   `db:"failure_attribution"`
	FirstTokenMs              pgtype.Int8   `db:"first_token_ms"`
	DurationMs                pgtype.Int8   `db:"duration_ms"`
	InputTokens               pgtype.Int8   `db:"input_tokens"`
	OutputTokens              pgtype.Int8   `db:"output_tokens"`
	CacheReadTokens           pgtype.Int8   `db:"cache_read_tokens"`
	CacheReadCostUSD          pgtype.Float8 `db:"cache_read_cost_usd"`
	CacheWriteTokens          pgtype.Int8   `db:"cache_write_tokens"`
	CacheWrite1hTokens        pgtype.Int8   `db:"cache_write_1h_tokens"`
	CacheWriteCostUSD         pgtype.Float8 `db:"cache_write_cost_usd"`
	ThinkingTokens            pgtype.Int8   `db:"thinking_tokens"`
	InputImageTokens          pgtype.Int8   `db:"input_image_tokens"`
	OutputImageTokens         pgtype.Int8   `db:"output_image_tokens"`
	InputAudioTokens          pgtype.Int8   `db:"input_audio_tokens"`
	OutputAudioTokens         pgtype.Int8   `db:"output_audio_tokens"`
	OutputImageCount          pgtype.Int8   `db:"output_image_count"`
	CostUSD                   pgtype.Float8 `db:"cost_usd"`
	ErrorCode                 pgtype.Text   `db:"error_code"`
	ErrorMessage              pgtype.Text   `db:"error_message"`
	CreatedAt                 time.Time     `db:"created_at"`
	RequestSnapshotJSON       pgtype.Text   `db:"request_snapshot_json"`
	ResponseSnapshotJSON      pgtype.Text   `db:"response_snapshot_json"`
}

func (s *Store) ListManagementUsageRecords(ctx context.Context, input port.ManagementUsageRecordListInput) (port.ManagementUsageRecordListResult, error) {
	limit := min(max(input.Limit, 1), 200)
	offset := min(max(input.Offset, 0), 1000-limit)
	query, args := managementUsageRecordListQuery(input, limit+1, offset)
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return port.ManagementUsageRecordListResult{}, fmt.Errorf("list management usage records: %w", err)
	}
	items, err := pgx.CollectRows(rows, pgx.RowToStructByNameLax[managementUsageRecordRow])
	if err != nil {
		return port.ManagementUsageRecordListResult{}, fmt.Errorf("scan management usage records: %w", err)
	}
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	result := make([]port.ManagementUsageRecordSummary, 0, len(items))
	for _, item := range items {
		result = append(result, managementUsageRecordSummary(item))
	}
	return port.ManagementUsageRecordListResult{Items: result, HasMore: hasMore}, nil
}

func (s *Store) GetManagementUsageRecord(ctx context.Context, input port.ManagementUsageRecordDetailInput) (port.ManagementUsageRecordDetail, bool, error) {
	query, args := managementUsageRecordDetailQuery(input)
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return port.ManagementUsageRecordDetail{}, false, fmt.Errorf("get management usage record: %w", err)
	}
	items, err := pgx.CollectRows(rows, pgx.RowToStructByNameLax[managementUsageRecordRow])
	if err != nil {
		return port.ManagementUsageRecordDetail{}, false, fmt.Errorf("scan management usage record: %w", err)
	}
	if len(items) == 0 {
		return port.ManagementUsageRecordDetail{}, false, nil
	}
	item := items[0]
	return port.ManagementUsageRecordDetail{
		ManagementUsageRecordSummary: managementUsageRecordSummary(item),
		RequestSnapshotJSON:          textValue(item.RequestSnapshotJSON),
		ResponseSnapshotJSON:         textValue(item.ResponseSnapshotJSON),
	}, true, nil
}

func managementUsageRecordListQuery(input port.ManagementUsageRecordListInput, limit, offset int) (string, []any) {
	conditions := make([]string, 0, 14)
	args := make([]any, 0, 18)
	addArg := func(value any) string {
		args = append(args, value)
		return fmt.Sprintf("$%d", len(args))
	}
	systemAccountArg := ""
	if value := strings.TrimSpace(input.SystemAccountID); value != "" {
		systemAccountArg = addArg(value)
		conditions = append(conditions, "ur.system_account_id = "+systemAccountArg+"::text")
	}
	addManagementUsageRecordPrefixFilter(&conditions, &args, "ur.trace_id", input.TraceID)
	if value := strings.TrimSpace(norm.NFKC.String(input.AccountKeyword)); value != "" {
		lower := addArg(value)
		upper := addArg(textPrefixUpperBound(value))
		accountMatches := []string{`SELECT accounts.id, accounts.name AS match_name, 1 AS source_rank
    FROM juhe_business.accounts AS accounts
    WHERE accounts.deleted_at IS NULL
      AND accounts.name COLLATE "C" >= ` + lower + `::text
      AND accounts.name COLLATE "C" < ` + upper + `::text`,
			`SELECT instances.id, sources.name AS match_name, 2 AS source_rank
    FROM juhe_business.accounts AS sources
    INNER JOIN juhe_business.accounts AS instances
      ON instances.authorization_instance_source_account_id = sources.id
    WHERE sources.deleted_at IS NULL
      AND instances.deleted_at IS NULL
      AND sources.name COLLATE "C" >= ` + lower + `::text
      AND sources.name COLLATE "C" < ` + upper + `::text`}
		if systemAccountArg != "" {
			accountMatches[0] += "\n      AND accounts.system_account_id = " + systemAccountArg + "::text"
			accountMatches[1] += "\n      AND instances.system_account_id = " + systemAccountArg + "::text"
			accountMatches = append(accountMatches,
				`SELECT accounts.id, accounts.name AS match_name, 3 AS source_rank
    FROM juhe_business.accounts AS accounts
    INNER JOIN juhe_business.resource_authorizations AS direct_authorization
      ON direct_authorization.resource_type = 'account'
      AND direct_authorization.resource_id = accounts.id
      AND direct_authorization.grantee_system_account_id = `+systemAccountArg+`::text
    WHERE accounts.deleted_at IS NULL
      AND accounts.name COLLATE "C" >= `+lower+`::text
      AND accounts.name COLLATE "C" < `+upper+`::text`,
				`SELECT accounts.id, accounts.name AS match_name, 4 AS source_rank
    FROM juhe_business.accounts AS accounts
    INNER JOIN juhe_business.group_accounts AS ga
      ON ga.account_id = accounts.id
      AND ga.enabled = true
    INNER JOIN juhe_business.resource_authorizations AS group_authorization
      ON group_authorization.resource_type = 'group'
      AND group_authorization.resource_id = ga.group_id
      AND group_authorization.grantee_system_account_id = `+systemAccountArg+`::text
    WHERE accounts.deleted_at IS NULL
      AND accounts.name COLLATE "C" >= `+lower+`::text
      AND accounts.name COLLATE "C" < `+upper+`::text`)
		}
		aliases := []string{"owned_candidates", "instance_candidates", "direct_candidates", "group_candidates"}
		orders := []string{
			`accounts.name COLLATE "C", accounts.id`,
			`sources.name COLLATE "C", instances.id`,
			`accounts.name COLLATE "C", accounts.id`,
			`accounts.name COLLATE "C", accounts.id`,
		}
		for index := range accountMatches {
			accountMatches[index] = boundedManagementUsageRecordAccountMatch(accountMatches[index], aliases[index], orders[index])
		}
		conditions = append(conditions, `ur.account_id IN (
  SELECT ordered.id
  FROM (
    SELECT DISTINCT ON (matched.id) matched.id, matched.match_name, matched.source_rank
    FROM (
      `+strings.Join(accountMatches, "\n      UNION ALL\n      ")+`
    ) AS matched
    ORDER BY matched.id, matched.source_rank, matched.match_name COLLATE "C"
  ) AS ordered
  ORDER BY ordered.source_rank, ordered.match_name COLLATE "C", ordered.id
  LIMIT 200
)`)
	}
	switch input.Result {
	case "success":
		conditions = append(conditions, "ur.success = "+addArg(int64(1))+"::bigint")
	case "failed":
		conditions = append(conditions, "ur.success = "+addArg(int64(0))+"::bigint")
	}
	if input.StatusCode != nil {
		conditions = append(conditions, "ur.status_code = "+addArg(int32(*input.StatusCode))+"::integer")
	}
	addManagementUsageRecordPrefixFilter(&conditions, &args, "ur.client_ip", input.ClientIP)
	for _, filter := range []struct{ column, value string }{
		{"ur.group_id", input.GroupID}, {"ur.model", input.Model}, {"ur.traffic_source", input.TrafficSource},
	} {
		if value := strings.TrimSpace(filter.value); value != "" {
			conditions = append(conditions, filter.column+" = "+addArg(value)+"::text")
		}
	}
	if !input.StartAt.IsZero() {
		conditions = append(conditions, "ur.created_at >= "+addArg(input.StartAt.UTC())+"::timestamptz")
	}
	if !input.EndAt.IsZero() {
		conditions = append(conditions, "ur.created_at < "+addArg(input.EndAt.UTC())+"::timestamptz")
	}
	direction := "DESC"
	if input.SortAscending {
		direction = "ASC"
	}
	var query strings.Builder
	query.WriteString("SELECT")
	query.WriteString(managementUsageRecordSelectColumns)
	query.WriteString(managementUsageRecordFromClause)
	if len(conditions) > 0 {
		query.WriteString("\nWHERE ")
		query.WriteString(strings.Join(conditions, "\n  AND "))
	}
	query.WriteString("\nORDER BY ur.created_at ")
	query.WriteString(direction)
	query.WriteString(", ur.id ")
	query.WriteString(direction)
	query.WriteString("\nLIMIT ")
	query.WriteString(addArg(int32(limit)))
	query.WriteString("::integer\nOFFSET ")
	query.WriteString(addArg(int32(offset)))
	query.WriteString("::integer")
	return query.String(), args
}

func boundedManagementUsageRecordAccountMatch(query, alias, orderBy string) string {
	return `SELECT ` + alias + `.id, ` + alias + `.match_name, ` + alias + `.source_rank
    FROM (
      ` + query + `
      ORDER BY ` + orderBy + `
      LIMIT 200
    ) AS ` + alias
}

func managementUsageRecordDetailQuery(input port.ManagementUsageRecordDetailInput) (string, []any) {
	recordID := strings.TrimSpace(input.ID)
	args := []any{recordID}
	addArg := func(value any) string {
		args = append(args, value)
		return fmt.Sprintf("$%d", len(args))
	}
	query := "SELECT" + managementUsageRecordSelectColumns + `,
  ur.request_snapshot_json,
  ur.response_snapshot_json` + managementUsageRecordFromClause + "\nWHERE ur.id = $1::text"
	if startAt, endAt, ok := managementUsageRecordPartitionBoundsFromID(recordID); ok {
		query += "\n  AND ur.created_at >= " + addArg(startAt) + "::timestamptz"
		query += "\n  AND ur.created_at < " + addArg(endAt) + "::timestamptz"
	}
	if systemAccountID := strings.TrimSpace(input.SystemAccountID); systemAccountID != "" {
		query += "\n  AND ur.system_account_id = " + addArg(systemAccountID) + "::text"
	}
	query += "\nLIMIT 1"
	return query, args
}

func managementUsageRecordPartitionBoundsFromID(id string) (time.Time, time.Time, bool) {
	match := managementUsageRecordIDDatePattern.FindStringSubmatch(strings.TrimSpace(id))
	if len(match) != 2 {
		return time.Time{}, time.Time{}, false
	}
	startAt, err := time.Parse("20060102", match[1])
	if err != nil {
		return time.Time{}, time.Time{}, false
	}
	return startAt, startAt.AddDate(0, 0, 1), true
}

func addManagementUsageRecordPrefixFilter(conditions *[]string, args *[]any, column, value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	*args = append(*args, value)
	lower := fmt.Sprintf("$%d", len(*args))
	*args = append(*args, textPrefixUpperBound(value))
	upper := fmt.Sprintf("$%d", len(*args))
	*conditions = append(*conditions, column+` COLLATE "C" >= `+lower+`::text`, column+` COLLATE "C" < `+upper+`::text`)
}

func managementUsageRecordSummary(row managementUsageRecordRow) port.ManagementUsageRecordSummary {
	return port.ManagementUsageRecordSummary{
		ID: row.ID, SystemAccountID: textPtr(row.SystemAccountID), SystemAccountName: textPtr(row.SystemAccountName),
		TraceID: row.TraceID, TrafficSource: row.TrafficSource, ClientIP: textPtr(row.ClientIP),
		APIKeyID: textPtr(row.APIKeyID), APIKeyName: textPtr(row.APIKeyName), GroupID: textPtr(row.GroupID), GroupName: textPtr(row.GroupName), AccountID: textPtr(row.AccountID), AccountName: textPtr(row.AccountName),
		Endpoint: textPtr(row.Endpoint), ProviderCode: textPtr(row.ProviderCode), ProviderProtocolProfileID: textPtr(row.ProviderProtocolProfileID), UsageSemantic: textPtr(row.UsageSemantic),
		Model: textPtr(row.Model), UpstreamModel: textPtr(row.UpstreamModel), PricingModel: textPtr(row.PricingModel),
		RequestedServiceTier: textPtr(row.RequestedServiceTier), EffectiveServiceTier: textPtr(row.EffectiveServiceTier), ReportedServiceTier: textPtr(row.ReportedServiceTier), BilledServiceTier: textPtr(row.BilledServiceTier),
		RequestedReasoningEffort: textPtr(row.RequestedReasoningEffort), EffectiveReasoningEffort: textPtr(row.EffectiveReasoningEffort), CostBreakdownSnapshotJSON: textPtr(row.CostBreakdownSnapshotJSON),
		ModelMappingApplied: row.ModelMappingApplied == 1, ModelMappingSource: textPtr(row.ModelMappingSource), SourceEndpointFamily: textPtr(row.SourceEndpointFamily), UpstreamEndpointFamily: textPtr(row.UpstreamEndpointFamily),
		Stream: row.Stream == 1, StatusCode: int4Ptr(row.StatusCode), Success: row.Success == 1, FailureAttribution: textPtr(row.FailureAttribution),
		FirstTokenMs: int8ValuePtr(row.FirstTokenMs), DurationMs: int8ValuePtr(row.DurationMs), InputTokens: int8ValuePtr(row.InputTokens), OutputTokens: int8ValuePtr(row.OutputTokens),
		CacheReadTokens: int8ValuePtr(row.CacheReadTokens), CacheReadCostUSD: float8Ptr(row.CacheReadCostUSD), CacheWriteTokens: int8ValuePtr(row.CacheWriteTokens), CacheWrite1hTokens: int8ValuePtr(row.CacheWrite1hTokens), CacheWriteCostUSD: float8Ptr(row.CacheWriteCostUSD),
		ThinkingTokens: int8ValuePtr(row.ThinkingTokens), InputImageTokens: int8ValuePtr(row.InputImageTokens), OutputImageTokens: int8ValuePtr(row.OutputImageTokens), InputAudioTokens: int8ValuePtr(row.InputAudioTokens), OutputAudioTokens: int8ValuePtr(row.OutputAudioTokens), OutputImageCount: int8ValuePtr(row.OutputImageCount),
		CostUSD: float8Ptr(row.CostUSD), ErrorCode: textPtr(row.ErrorCode), ErrorMessage: textPtr(row.ErrorMessage), CreatedAt: row.CreatedAt.UTC(),
	}
}

func int8ValuePtr(value pgtype.Int8) *int64 {
	if !value.Valid {
		return nil
	}
	result := value.Int64
	return &result
}
