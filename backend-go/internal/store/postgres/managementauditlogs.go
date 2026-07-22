package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5"
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

type managementAuditLogAttemptRow struct {
	ID, UpstreamMethod, UpstreamURL, StartedAt                       string
	AccountID, AccountName, AccountOwnerSystemAccountID              pgtype.Text
	GroupID, GroupName, ProxyURL, ProviderCode                       pgtype.Text
	Model, UpstreamModel, PricingModel                               pgtype.Text
	ModelMappingSource, SourceEndpointFamily, UpstreamEndpointFamily pgtype.Text
	ErrorPhase, ErrorCode, ErrorMessage, EndedAt                     pgtype.Text
	AttemptIndex                                                     int32
	ModelMappingApplied, Success                                     int64
	UpstreamStatusCode, DurationMs                                   pgtype.Int4
}

type managementAuditLogPayloadSummaryRow struct {
	ID, PartType, CaptureStatus, CreatedAt               string
	AttemptID, ContentType, ContentEncoding              pgtype.Text
	HeadersBlobID, BodyBlobID, HeadersSHA256, BodySHA256 pgtype.Text
	SequenceIndex                                        int32
	SizeBytes, CompressedSizeBytes                       int64
}

type managementAuditErrorGroupRow struct {
	ID, Fingerprint, WindowStartedAt, WindowEndedAt, CreatedAt, UpdatedAt string
	SystemAccountID, SystemAccountName, APIKeyID, APIKeyName              pgtype.Text
	GroupID, GroupName, AccountID, AccountName, ProviderCode              pgtype.Text
	Path, Model, ErrorPhase, ErrorCode, ErrorType                         pgtype.Text
	RequestFingerprint, ErrorFingerprint                                  pgtype.Text
	FirstEventID, LastEventID, SampleEventID, LastMessage                 pgtype.Text
	StatusCode                                                            pgtype.Int4
	Count                                                                 int32
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
		err = scanManagementAuditLogRow(rows, &row)
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

func (s *Store) GetManagementAuditLog(ctx context.Context, id string) (port.ManagementAuditLogDetail, bool, error) {
	var row managementAuditLogRow
	if err := scanManagementAuditLogRow(s.pool.QueryRow(ctx, managementAuditLogDetailQuery(), id), &row); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return port.ManagementAuditLogDetail{}, false, nil
		}
		return port.ManagementAuditLogDetail{}, false, fmt.Errorf("get management audit log: %w", err)
	}

	detail := port.ManagementAuditLogDetail{ManagementAuditLogSummary: managementAuditLogSummary(row)}
	childContext, cancel := context.WithCancel(ctx)
	defer cancel()
	var group sync.WaitGroup
	var errorOnce sync.Once
	var childError error
	var attempts []port.ManagementAuditLogAttempt
	var payloads []port.ManagementAuditLogPayloadSummary
	var errorGroup *port.ManagementAuditErrorGroup
	recordError := func(err error) {
		if err == nil {
			return
		}
		errorOnce.Do(func() {
			childError = err
			cancel()
		})
	}
	group.Add(2)
	go func() {
		defer group.Done()
		items, err := s.listManagementAuditLogAttempts(childContext, id)
		if err == nil {
			attempts = items
		}
		recordError(err)
	}()
	go func() {
		defer group.Done()
		items, err := s.listManagementAuditLogPayloadSummaries(childContext, id)
		if err == nil {
			payloads = items
		}
		recordError(err)
	}()
	if row.ErrorGroupID.Valid && row.ErrorGroupID.String != "" {
		group.Add(1)
		go func() {
			defer group.Done()
			item, err := s.getManagementAuditErrorGroup(childContext, row.ErrorGroupID.String)
			if err == nil {
				errorGroup = item
			}
			recordError(err)
		}()
	}
	group.Wait()
	if childError != nil {
		return port.ManagementAuditLogDetail{}, false, childError
	}
	if attempts == nil {
		attempts = []port.ManagementAuditLogAttempt{}
	}
	if payloads == nil {
		payloads = []port.ManagementAuditLogPayloadSummary{}
	}
	detail.Attempts = attempts
	detail.Payloads = payloads
	detail.ErrorGroup = errorGroup
	return detail, true, nil
}

type auditLogRowScanner interface {
	Scan(...any) error
}

func scanManagementAuditLogRow(scanner auditLogRowScanner, row *managementAuditLogRow) error {
	return scanner.Scan(&row.ID, &row.TraceID, &row.TrafficSource, &row.SystemAccountID, &row.SystemAccountName, &row.APIKeyID, &row.APIKeyName, &row.GroupID, &row.GroupName, &row.AccountID, &row.AccountName, &row.ProviderCode, &row.Method, &row.Path, &row.QueryString, &row.Model, &row.UpstreamModel, &row.PricingModel, &row.ModelMappingApplied, &row.ModelMappingSource, &row.SourceEndpointFamily, &row.UpstreamEndpointFamily, &row.Stream, &row.ClientIP, &row.UserAgent, &row.AuditOutcome, &row.Success, &row.FinalStatusCode, &row.ErrorPhase, &row.ErrorCode, &row.ErrorMessage, &row.SampleBucket, &row.SampleReason, &row.AttemptCount, &row.PayloadCount, &row.RawPayloadBytes, &row.CompressedPayloadBytes, &row.CompressionSavedBytes, &row.ErrorGroupID, &row.CaptureStatus, &row.StartedAt, &row.EndedAt, &row.DurationMs, &row.HTTPCompletedAt, &row.HTTPDurationMs, &row.FirstTokenMs, &row.CreatedAt)
}

func managementAuditLogDetailQuery() string {
	return managementAuditLogSelect + "\nWHERE al.id = $1::text"
}

func managementAuditLogAttemptsQuery() string {
	return `SELECT
  attempts.id, attempts.attempt_index, attempts.account_id, accounts.name,
  attempts.account_owner_system_account_id, attempts.group_id, groups.name, attempts.proxy_url,
  attempts.provider_code, attempts.attempt_model, attempts.attempt_upstream_model, attempts.attempt_pricing_model,
  attempts.attempt_model_mapping_applied, attempts.attempt_model_mapping_source,
  attempts.attempt_source_endpoint_family, attempts.attempt_upstream_endpoint_family,
  attempts.upstream_method, attempts.upstream_url, attempts.upstream_status_code, attempts.success,
  attempts.error_phase, attempts.error_code, attempts.error_message,
  attempts.started_at, attempts.ended_at, attempts.duration_ms
FROM juhe_dataset.audit_log_attempts AS attempts
LEFT JOIN juhe_business.accounts AS accounts ON accounts.id = attempts.account_id
LEFT JOIN juhe_business.groups AS groups ON groups.id = attempts.group_id
WHERE attempts.audit_log_id = $1::text
ORDER BY attempts.attempt_index ASC, attempts.id ASC`
}

func managementAuditLogPayloadSummariesQuery() string {
	return `SELECT
  refs.id, refs.attempt_id, refs.part_type, refs.sequence_index, refs.content_type, refs.content_encoding,
  refs.headers_blob_id, refs.body_blob_id, refs.headers_sha256, refs.body_sha256,
  refs.raw_size_bytes, refs.compressed_size_bytes, refs.capture_status, refs.created_at
FROM juhe_dataset.audit_payload_refs AS refs
WHERE refs.audit_log_id = $1::text
ORDER BY refs.sequence_index ASC, refs.id ASC`
}

func managementAuditErrorGroupDetailQuery() string {
	return `SELECT
  groups.id, groups.fingerprint, groups.window_started_at, groups.window_ended_at,
  groups.system_account_id, system_accounts.display_name, groups.api_key_id, api_keys.name,
  groups.group_id, business_groups.name, groups.account_id, accounts.name, groups.provider_code,
  groups.path, groups.model, groups.status_code, groups.error_phase, groups.error_code, groups.error_type,
  groups.request_fingerprint, groups.error_fingerprint, groups.count,
  groups.first_event_id, groups.last_event_id, groups.sample_event_id, groups.last_message,
  groups.created_at, groups.updated_at
FROM juhe_dataset.audit_error_groups AS groups
LEFT JOIN juhe_business.system_accounts AS system_accounts ON system_accounts.id = groups.system_account_id
LEFT JOIN juhe_business.api_keys AS api_keys ON api_keys.id = groups.api_key_id
LEFT JOIN juhe_business.groups AS business_groups ON business_groups.id = groups.group_id
LEFT JOIN juhe_business.accounts AS accounts ON accounts.id = groups.account_id
WHERE groups.id = $1::text`
}

func (s *Store) listManagementAuditLogAttempts(ctx context.Context, id string) ([]port.ManagementAuditLogAttempt, error) {
	rows, err := s.pool.Query(ctx, managementAuditLogAttemptsQuery(), id)
	if err != nil {
		return nil, fmt.Errorf("list management audit log attempts: %w", err)
	}
	defer rows.Close()
	items := make([]port.ManagementAuditLogAttempt, 0)
	for rows.Next() {
		var row managementAuditLogAttemptRow
		if err = rows.Scan(&row.ID, &row.AttemptIndex, &row.AccountID, &row.AccountName, &row.AccountOwnerSystemAccountID, &row.GroupID, &row.GroupName, &row.ProxyURL, &row.ProviderCode, &row.Model, &row.UpstreamModel, &row.PricingModel, &row.ModelMappingApplied, &row.ModelMappingSource, &row.SourceEndpointFamily, &row.UpstreamEndpointFamily, &row.UpstreamMethod, &row.UpstreamURL, &row.UpstreamStatusCode, &row.Success, &row.ErrorPhase, &row.ErrorCode, &row.ErrorMessage, &row.StartedAt, &row.EndedAt, &row.DurationMs); err != nil {
			return nil, fmt.Errorf("scan management audit log attempts: %w", err)
		}
		items = append(items, port.ManagementAuditLogAttempt{
			ID: row.ID, AttemptIndex: int(row.AttemptIndex), AccountID: textPtr(row.AccountID), AccountName: textPtr(row.AccountName),
			AccountOwnerSystemAccountID: textPtr(row.AccountOwnerSystemAccountID), GroupID: textPtr(row.GroupID), GroupName: textPtr(row.GroupName),
			ProxyURL: textPtr(row.ProxyURL), ProviderCode: textPtr(row.ProviderCode), Model: textPtr(row.Model), UpstreamModel: textPtr(row.UpstreamModel), PricingModel: textPtr(row.PricingModel),
			ModelMappingApplied: row.ModelMappingApplied == 1, ModelMappingSource: textPtr(row.ModelMappingSource),
			SourceEndpointFamily: textPtr(row.SourceEndpointFamily), UpstreamEndpointFamily: textPtr(row.UpstreamEndpointFamily),
			UpstreamMethod: row.UpstreamMethod, UpstreamURL: row.UpstreamURL, UpstreamStatusCode: int4Ptr(row.UpstreamStatusCode), Success: row.Success == 1,
			ErrorPhase: textPtr(row.ErrorPhase), ErrorCode: textPtr(row.ErrorCode), ErrorMessage: textPtr(row.ErrorMessage),
			StartedAt: row.StartedAt, EndedAt: textPtr(row.EndedAt), DurationMs: auditDuration(row.DurationMs),
		})
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("scan management audit log attempts: %w", err)
	}
	return items, nil
}

func (s *Store) listManagementAuditLogPayloadSummaries(ctx context.Context, id string) ([]port.ManagementAuditLogPayloadSummary, error) {
	rows, err := s.pool.Query(ctx, managementAuditLogPayloadSummariesQuery(), id)
	if err != nil {
		return nil, fmt.Errorf("list management audit log payload summaries: %w", err)
	}
	defer rows.Close()
	items := make([]port.ManagementAuditLogPayloadSummary, 0)
	for rows.Next() {
		var row managementAuditLogPayloadSummaryRow
		if err = rows.Scan(&row.ID, &row.AttemptID, &row.PartType, &row.SequenceIndex, &row.ContentType, &row.ContentEncoding, &row.HeadersBlobID, &row.BodyBlobID, &row.HeadersSHA256, &row.BodySHA256, &row.SizeBytes, &row.CompressedSizeBytes, &row.CaptureStatus, &row.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan management audit log payload summaries: %w", err)
		}
		items = append(items, port.ManagementAuditLogPayloadSummary{
			ID: row.ID, AttemptID: textPtr(row.AttemptID), PartType: row.PartType, SequenceIndex: int(row.SequenceIndex),
			ContentType: textPtr(row.ContentType), ContentEncoding: textPtr(row.ContentEncoding), HeadersSHA256: textPtr(row.HeadersSHA256), BodySHA256: textPtr(row.BodySHA256),
			SizeBytes: row.SizeBytes, CompressedSizeBytes: row.CompressedSizeBytes, CaptureStatus: row.CaptureStatus, CreatedAt: row.CreatedAt,
			HasHeaders: row.HeadersBlobID.Valid && row.HeadersBlobID.String != "", HasBody: row.BodyBlobID.Valid && row.BodyBlobID.String != "",
		})
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("scan management audit log payload summaries: %w", err)
	}
	return items, nil
}

func (s *Store) getManagementAuditErrorGroup(ctx context.Context, id string) (*port.ManagementAuditErrorGroup, error) {
	var row managementAuditErrorGroupRow
	err := s.pool.QueryRow(ctx, managementAuditErrorGroupDetailQuery(), id).Scan(
		&row.ID, &row.Fingerprint, &row.WindowStartedAt, &row.WindowEndedAt,
		&row.SystemAccountID, &row.SystemAccountName, &row.APIKeyID, &row.APIKeyName,
		&row.GroupID, &row.GroupName, &row.AccountID, &row.AccountName, &row.ProviderCode,
		&row.Path, &row.Model, &row.StatusCode, &row.ErrorPhase, &row.ErrorCode, &row.ErrorType,
		&row.RequestFingerprint, &row.ErrorFingerprint, &row.Count,
		&row.FirstEventID, &row.LastEventID, &row.SampleEventID, &row.LastMessage,
		&row.CreatedAt, &row.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get management audit error group: %w", err)
	}
	return &port.ManagementAuditErrorGroup{
		ID: row.ID, Fingerprint: row.Fingerprint, WindowStartedAt: row.WindowStartedAt, WindowEndedAt: row.WindowEndedAt,
		SystemAccountID: textPtr(row.SystemAccountID), SystemAccountName: textPtr(row.SystemAccountName), APIKeyID: textPtr(row.APIKeyID), APIKeyName: textPtr(row.APIKeyName),
		GroupID: textPtr(row.GroupID), GroupName: textPtr(row.GroupName), AccountID: textPtr(row.AccountID), AccountName: textPtr(row.AccountName), ProviderCode: textPtr(row.ProviderCode),
		Path: textPtr(row.Path), Model: textPtr(row.Model), StatusCode: int4Ptr(row.StatusCode), ErrorPhase: textPtr(row.ErrorPhase), ErrorCode: textPtr(row.ErrorCode), ErrorType: textPtr(row.ErrorType),
		RequestFingerprint: textPtr(row.RequestFingerprint), ErrorFingerprint: textPtr(row.ErrorFingerprint), Count: int(row.Count),
		FirstEventID: textPtr(row.FirstEventID), LastEventID: textPtr(row.LastEventID), SampleEventID: textPtr(row.SampleEventID), LastMessage: textPtr(row.LastMessage),
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}, nil
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
