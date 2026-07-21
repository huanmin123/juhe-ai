package postgres

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	"juhe-ai/backend-go/internal/store/port"
)

const managementAuditLogSelect = `SELECT
  al.id, al.trace_id, al.traffic_source, al.system_account_id, sa.display_name,
  al.api_key_id, ak.name, al.group_id, grp.name, al.account_id, acc.name, al.provider_code,
  al.method, al.path, al.query_string, al.model, al.upstream_model, al.pricing_model,
  al.model_mapping_applied, al.model_mapping_source, al.source_endpoint_family, al.upstream_endpoint_family,
  al.stream, al.client_ip, al.user_agent, al.audit_outcome, al.success, al.final_status_code,
  al.error_phase, al.error_code, al.error_message, al.sample_bucket, al.sample_reason,
  al.attempt_count, al.payload_count, al.raw_payload_bytes, al.compressed_payload_bytes,
  al.compression_saved_bytes, al.error_group_id, al.capture_status, al.started_at, al.ended_at,
  al.duration_ms, al.http_completed_at, al.http_duration_ms, al.first_token_ms, al.created_at
FROM juhe_dataset.audit_logs AS al
LEFT JOIN juhe_business.system_accounts AS sa ON sa.id = al.system_account_id
LEFT JOIN juhe_business.api_keys AS ak ON ak.id = al.api_key_id
LEFT JOIN juhe_business.groups AS grp ON grp.id = al.group_id
LEFT JOIN juhe_business.accounts AS acc ON acc.id = al.account_id`

type managementAuditLogRow struct {
	ID, TraceID, TrafficSource                                       string
	SystemAccountID, SystemAccountName, APIKeyID, APIKeyName         pgtype.Text
	GroupID, GroupName, AccountID, AccountName, ProviderCode         pgtype.Text
	Method, Path                                                     string
	QueryString, Model, UpstreamModel, PricingModel                  pgtype.Text
	ModelMappingApplied                                              int64
	ModelMappingSource, SourceEndpointFamily, UpstreamEndpointFamily pgtype.Text
	Stream                                                           int64
	ClientIP, UserAgent                                              pgtype.Text
	AuditOutcome                                                     string
	Success                                                          int64
	FinalStatusCode                                                  pgtype.Int4
	ErrorPhase, ErrorCode, ErrorMessage                              pgtype.Text
	SampleBucket                                                     int32
	SampleReason                                                     string
	AttemptCount, PayloadCount                                       int32
	RawPayloadBytes, CompressedPayloadBytes, CompressionSavedBytes   int64
	ErrorGroupID                                                     pgtype.Text
	CaptureStatus, StartedAt, EndedAt                                string
	DurationMs                                                       pgtype.Int4
	HTTPCompletedAt                                                  pgtype.Text
	HTTPDurationMs, FirstTokenMs                                     pgtype.Int4
	CreatedAt                                                        string
}

func (s *Store) ListManagementAuditLogs(ctx context.Context, input port.ManagementAuditLogListInput) (port.ManagementAuditLogListResult, error) {
	limit := min(max(input.Limit, 1), 100)
	offset := min(max(input.Offset, 0), 1000-limit)
	query, args := managementAuditLogListQuery(input, limit+1, offset)
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return port.ManagementAuditLogListResult{}, fmt.Errorf("list management audit logs: %w", err)
	}
	defer rows.Close()
	items := make([]port.ManagementAuditLogSummary, 0, limit)
	for rows.Next() {
		var row managementAuditLogRow
		err = rows.Scan(&row.ID, &row.TraceID, &row.TrafficSource, &row.SystemAccountID, &row.SystemAccountName, &row.APIKeyID, &row.APIKeyName, &row.GroupID, &row.GroupName, &row.AccountID, &row.AccountName, &row.ProviderCode, &row.Method, &row.Path, &row.QueryString, &row.Model, &row.UpstreamModel, &row.PricingModel, &row.ModelMappingApplied, &row.ModelMappingSource, &row.SourceEndpointFamily, &row.UpstreamEndpointFamily, &row.Stream, &row.ClientIP, &row.UserAgent, &row.AuditOutcome, &row.Success, &row.FinalStatusCode, &row.ErrorPhase, &row.ErrorCode, &row.ErrorMessage, &row.SampleBucket, &row.SampleReason, &row.AttemptCount, &row.PayloadCount, &row.RawPayloadBytes, &row.CompressedPayloadBytes, &row.CompressionSavedBytes, &row.ErrorGroupID, &row.CaptureStatus, &row.StartedAt, &row.EndedAt, &row.DurationMs, &row.HTTPCompletedAt, &row.HTTPDurationMs, &row.FirstTokenMs, &row.CreatedAt)
		if err != nil {
			return port.ManagementAuditLogListResult{}, fmt.Errorf("scan management audit logs: %w", err)
		}
		items = append(items, managementAuditLogSummary(row))
	}
	if err = rows.Err(); err != nil {
		return port.ManagementAuditLogListResult{}, fmt.Errorf("scan management audit logs: %w", err)
	}
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	return port.ManagementAuditLogListResult{Items: items, HasMore: hasMore}, nil
}

func managementAuditLogListQuery(input port.ManagementAuditLogListInput, limit, offset int) (string, []any) {
	conditions := make([]string, 0, 18)
	args := make([]any, 0, 20)
	add := func(v any) string { args = append(args, v); return fmt.Sprintf("$%d", len(args)) }
	prefix := func(column, value string) {
		value = auditLogTrimECMAScriptWhitespace(value)
		if value != "" {
			lo := add(value)
			hi := add(textPrefixUpperBound(value))
			conditions = append(conditions, column+` COLLATE "C" >= `+lo+`::text`, column+` COLLATE "C" < `+hi+`::text`)
		}
	}
	exact := func(column, value string) {
		if value = auditLogTrimECMAScriptWhitespace(value); value != "" {
			conditions = append(conditions, column+" = "+add(value)+"::text")
		}
	}
	prefix("al.trace_id", input.TraceID)
	exact("al.path", input.Path)
	exact("al.model", input.Model)
	prefix("al.client_ip", input.ClientIP)
	exact("al.audit_outcome", input.Outcome)
	if input.StatusCode != nil && *input.StatusCode >= 100 && *input.StatusCode <= 599 {
		conditions = append(conditions, "al.final_status_code = "+add(int32(*input.StatusCode))+"::integer")
	}
	exact("al.traffic_source", input.TrafficSource)
	if input.StartAt != "" {
		conditions = append(conditions, "al.created_at >= "+add(input.StartAt)+"::text")
	}
	if input.EndAt != "" {
		conditions = append(conditions, "al.created_at <= "+add(input.EndAt)+"::text")
	}
	for _, f := range []struct{ column, value string }{{"al.system_account_id", input.SystemAccountID}, {"al.api_key_id", input.APIKeyID}, {"al.group_id", input.GroupID}, {"al.account_id", input.AccountID}, {"al.error_group_id", input.ErrorGroupID}} {
		exact(f.column, f.value)
	}
	var query strings.Builder
	query.WriteString(managementAuditLogSelect)
	if len(conditions) > 0 {
		query.WriteString("\nWHERE ")
		query.WriteString(strings.Join(conditions, "\n  AND "))
	}
	query.WriteString("\nORDER BY al.created_at DESC, al.id DESC\nLIMIT ")
	query.WriteString(add(int32(limit)))
	query.WriteString("::integer\nOFFSET ")
	query.WriteString(add(int32(offset)))
	query.WriteString("::integer")
	return query.String(), args
}

func auditLogTrimECMAScriptWhitespace(value string) string {
	return strings.TrimFunc(value, func(character rune) bool {
		switch character {
		case '\u0009', '\u000B', '\u000C', '\u0020', '\u00A0', '\u1680',
			'\u2000', '\u2001', '\u2002', '\u2003', '\u2004', '\u2005',
			'\u2006', '\u2007', '\u2008', '\u2009', '\u200A', '\u202F',
			'\u205F', '\u3000', '\uFEFF', '\u000A', '\u000D', '\u2028',
			'\u2029':
			return true
		default:
			return false
		}
	})
}

func managementAuditLogSummary(row managementAuditLogRow) port.ManagementAuditLogSummary {
	return port.ManagementAuditLogSummary{
		ID: row.ID, TraceID: row.TraceID, TrafficSource: row.TrafficSource,
		SystemAccountID: textPtr(row.SystemAccountID), SystemAccountName: textPtr(row.SystemAccountName),
		APIKeyID: textPtr(row.APIKeyID), APIKeyName: textPtr(row.APIKeyName),
		GroupID: textPtr(row.GroupID), GroupName: textPtr(row.GroupName),
		AccountID: textPtr(row.AccountID), AccountName: textPtr(row.AccountName),
		ProviderCode: textPtr(row.ProviderCode), Method: row.Method, Path: row.Path,
		QueryString: textPtr(row.QueryString), Model: textPtr(row.Model), UpstreamModel: textPtr(row.UpstreamModel),
		PricingModel: textPtr(row.PricingModel), ModelMappingApplied: row.ModelMappingApplied == 1,
		ModelMappingSource: textPtr(row.ModelMappingSource), SourceEndpointFamily: textPtr(row.SourceEndpointFamily),
		UpstreamEndpointFamily: textPtr(row.UpstreamEndpointFamily), Stream: row.Stream == 1,
		ClientIP: textPtr(row.ClientIP), UserAgent: textPtr(row.UserAgent), AuditOutcome: row.AuditOutcome,
		Success: row.Success == 1, FinalStatusCode: int4Ptr(row.FinalStatusCode),
		ErrorPhase: textPtr(row.ErrorPhase), ErrorCode: textPtr(row.ErrorCode), ErrorMessage: textPtr(row.ErrorMessage),
		SampleBucket: int(row.SampleBucket), SampleReason: row.SampleReason,
		AttemptCount: int(row.AttemptCount), PayloadCount: int(row.PayloadCount),
		RawPayloadBytes: row.RawPayloadBytes, CompressedPayloadBytes: row.CompressedPayloadBytes,
		CompressionSavedBytes: row.CompressionSavedBytes, ErrorGroupID: textPtr(row.ErrorGroupID),
		CaptureStatus: row.CaptureStatus, StartedAt: row.StartedAt, EndedAt: row.EndedAt,
		DurationMs: auditDuration(row.DurationMs), HTTPCompletedAt: textPtr(row.HTTPCompletedAt),
		HTTPDurationMs: auditDuration(row.HTTPDurationMs), FirstTokenMs: auditDuration(row.FirstTokenMs),
		CreatedAt: row.CreatedAt,
	}
}

func auditDuration(v pgtype.Int4) *int64 {
	if !v.Valid {
		return nil
	}
	n := int64(v.Int32)
	return &n
}
