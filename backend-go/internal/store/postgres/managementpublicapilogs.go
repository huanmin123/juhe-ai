package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"juhe-ai/backend-go/internal/store/port"
)

const (
	defaultManagementPublicAPILogListLimit = 50
	maxManagementPublicAPILogListLimit     = 100
	maxManagementPublicAPILogListRows      = 1000
)

const managementPublicAPILogListSelectColumns = `  pal.id,
  pal.created_at,
  pal.source_name,
  pal.method,
  pal.path,
  pal.success,
  pal.status_code,
  pal.duration_ms,
  pal.client_ip,
  pal.trace_id`

const managementPublicAPILogSummarySelectColumns = `  pal.id,
  pal.trace_id,
  pal.source_ref_id,
  pal.source_name,
  pal.token_id,
  pal.token_name,
  pal.token_prefix,
  pal.is_test_token,
  pal.method,
  pal.path,
  pal.query_string,
  pal.client_ip,
  pal.user_agent,
  pal.status_code,
  pal.success,
  pal.duration_ms,
  pal.request_size_bytes,
  pal.response_size_bytes,
  pal.request_capture_status,
  pal.response_capture_status,
  pal.error_code,
  pal.error_message,
  pal.started_at,
  pal.ended_at,
  pal.created_at`

const managementPublicAPILogDetailQuery = `SELECT
` + managementPublicAPILogSummarySelectColumns + `,
  pal.request_data_json,
  pal.response_data_json
FROM juhe_dataset.public_api_logs AS pal
WHERE pal.id = $1::text
LIMIT 1`

type managementPublicAPILogListRow struct {
	ID         string
	CreatedAt  time.Time
	SourceName pgtype.Text
	Method     string
	Path       string
	Success    bool
	StatusCode pgtype.Int4
	DurationMs pgtype.Int8
	ClientIP   pgtype.Text
	TraceID    pgtype.Text
}

type managementPublicAPILogRow struct {
	ID                    string
	TraceID               pgtype.Text
	SourceRefID           pgtype.Text
	SourceName            pgtype.Text
	TokenID               pgtype.Text
	TokenName             pgtype.Text
	TokenPrefix           pgtype.Text
	IsTestToken           bool
	Method                string
	Path                  string
	QueryString           pgtype.Text
	ClientIP              pgtype.Text
	UserAgent             pgtype.Text
	StatusCode            pgtype.Int4
	Success               bool
	DurationMs            pgtype.Int8
	RequestSizeBytes      int64
	ResponseSizeBytes     int64
	RequestCaptureStatus  string
	ResponseCaptureStatus string
	ErrorCode             pgtype.Text
	ErrorMessage          pgtype.Text
	StartedAt             time.Time
	EndedAt               time.Time
	CreatedAt             time.Time
}

type managementPublicAPILogDetailRow struct {
	managementPublicAPILogRow
	RequestDataJSON  string
	ResponseDataJSON string
}

type managementPublicAPILogListExecutor interface {
	QueryManagementPublicAPILogs(
		ctx context.Context,
		query string,
		args ...any,
	) ([]managementPublicAPILogListRow, error)
}

type managementPublicAPILogDetailExecutor interface {
	QueryManagementPublicAPILog(
		ctx context.Context,
		query string,
		id string,
	) (managementPublicAPILogDetailRow, error)
}

type postgresManagementPublicAPILogExecutor struct {
	store *Store
}

func (e postgresManagementPublicAPILogExecutor) QueryManagementPublicAPILogs(
	ctx context.Context,
	query string,
	args ...any,
) ([]managementPublicAPILogListRow, error) {
	rows, err := e.store.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]managementPublicAPILogListRow, 0)
	for rows.Next() {
		var item managementPublicAPILogListRow
		if err := rows.Scan(managementPublicAPILogListScanDestinations(&item)...); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

func (e postgresManagementPublicAPILogExecutor) QueryManagementPublicAPILog(
	ctx context.Context,
	query string,
	id string,
) (managementPublicAPILogDetailRow, error) {
	var row managementPublicAPILogDetailRow
	destinations := managementPublicAPILogSummaryScanDestinations(&row.managementPublicAPILogRow)
	destinations = append(destinations, &row.RequestDataJSON, &row.ResponseDataJSON)
	err := e.store.pool.QueryRow(ctx, query, id).Scan(destinations...)
	return row, err
}

func managementPublicAPILogListScanDestinations(row *managementPublicAPILogListRow) []any {
	return []any{
		&row.ID,
		&row.CreatedAt,
		&row.SourceName,
		&row.Method,
		&row.Path,
		&row.Success,
		&row.StatusCode,
		&row.DurationMs,
		&row.ClientIP,
		&row.TraceID,
	}
}

func managementPublicAPILogSummaryScanDestinations(row *managementPublicAPILogRow) []any {
	return []any{
		&row.ID,
		&row.TraceID,
		&row.SourceRefID,
		&row.SourceName,
		&row.TokenID,
		&row.TokenName,
		&row.TokenPrefix,
		&row.IsTestToken,
		&row.Method,
		&row.Path,
		&row.QueryString,
		&row.ClientIP,
		&row.UserAgent,
		&row.StatusCode,
		&row.Success,
		&row.DurationMs,
		&row.RequestSizeBytes,
		&row.ResponseSizeBytes,
		&row.RequestCaptureStatus,
		&row.ResponseCaptureStatus,
		&row.ErrorCode,
		&row.ErrorMessage,
		&row.StartedAt,
		&row.EndedAt,
		&row.CreatedAt,
	}
}

func (s *Store) ListManagementPublicAPILogs(
	ctx context.Context,
	input port.ManagementPublicAPILogListInput,
) (port.ManagementPublicAPILogListResult, error) {
	return listManagementPublicAPILogs(ctx, postgresManagementPublicAPILogExecutor{store: s}, input)
}

func listManagementPublicAPILogs(
	ctx context.Context,
	executor managementPublicAPILogListExecutor,
	input port.ManagementPublicAPILogListInput,
) (port.ManagementPublicAPILogListResult, error) {
	limit := normalizeManagementPublicAPILogListLimit(input.Limit)
	offset := min(max(0, input.Offset), maxManagementPublicAPILogListRows-limit)
	query, args := managementPublicAPILogListQuery(input, limit+1, offset)
	rows, err := executor.QueryManagementPublicAPILogs(ctx, query, args...)
	if err != nil {
		return port.ManagementPublicAPILogListResult{}, fmt.Errorf("list management public API logs: %w", err)
	}

	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	items := make([]port.ManagementPublicAPILogListItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, managementPublicAPILogListItem(row))
	}
	return port.ManagementPublicAPILogListResult{
		Items:   items,
		HasMore: hasMore,
	}, nil
}

func managementPublicAPILogListQuery(
	input port.ManagementPublicAPILogListInput,
	rowLimit int,
	rowOffset int,
) (string, []any) {
	const selectManagementPublicAPILogs = `SELECT
` + managementPublicAPILogListSelectColumns + `
FROM juhe_dataset.public_api_logs AS pal`

	conditions := make([]string, 0, 10)
	args := make([]any, 0, 12)
	addArg := func(value any) string {
		args = append(args, value)
		return fmt.Sprintf("$%d", len(args))
	}

	if traceID := managementPublicAPILogTrimECMAScriptWhitespace(input.TraceID); traceID != "" {
		lowerArg := addArg(traceID)
		upperArg := addArg(textPrefixUpperBound(traceID))
		conditions = append(conditions,
			`pal.trace_id COLLATE "C" >= `+lowerArg+`::text`,
			`pal.trace_id COLLATE "C" < `+upperArg+`::text`,
		)
	}
	if sourceRefID := managementPublicAPILogExactFilter(input.SourceRefID); sourceRefID != "" {
		conditions = append(conditions, `pal.source_ref_id = `+addArg(sourceRefID)+`::text`)
	}
	if path := managementPublicAPILogExactFilter(input.Path); path != "" {
		conditions = append(conditions, `pal.path = `+addArg(path)+`::text`)
	}
	switch input.Result {
	case port.ManagementPublicAPILogResultSuccess:
		conditions = append(conditions, `pal.success = `+addArg(true)+`::boolean`)
	case port.ManagementPublicAPILogResultFailed:
		conditions = append(conditions, `pal.success = `+addArg(false)+`::boolean`)
	}
	if input.StatusCode != nil && *input.StatusCode >= 100 && *input.StatusCode <= 599 {
		conditions = append(conditions, `pal.status_code = `+addArg(int32(*input.StatusCode))+`::integer`)
	}
	if clientIP := managementPublicAPILogTrimECMAScriptWhitespace(input.ClientIP); clientIP != "" {
		lowerArg := addArg(clientIP)
		upperArg := addArg(textPrefixUpperBound(clientIP))
		conditions = append(conditions,
			`pal.client_ip COLLATE "C" >= `+lowerArg+`::text`,
			`pal.client_ip COLLATE "C" < `+upperArg+`::text`,
		)
	}
	if !input.StartAt.IsZero() {
		conditions = append(conditions, `pal.created_at >= `+addArg(input.StartAt.UTC())+`::timestamptz`)
	}
	if !input.EndAt.IsZero() {
		conditions = append(conditions, `pal.created_at <= `+addArg(input.EndAt.UTC())+`::timestamptz`)
	}

	var query strings.Builder
	query.WriteString(selectManagementPublicAPILogs)
	if len(conditions) > 0 {
		query.WriteString("\nWHERE ")
		query.WriteString(strings.Join(conditions, "\n  AND "))
	}
	query.WriteString("\nORDER BY pal.created_at DESC, pal.id DESC")
	query.WriteString("\nLIMIT ")
	query.WriteString(addArg(int32(rowLimit)))
	query.WriteString("::integer")
	query.WriteString("\nOFFSET ")
	query.WriteString(addArg(int32(rowOffset)))
	query.WriteString("::integer")
	return query.String(), args
}

func managementPublicAPILogExactFilter(value string) string {
	return managementPublicAPILogTrimECMAScriptWhitespace(value)
}

func managementPublicAPILogTrimECMAScriptWhitespace(value string) string {
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

func normalizeManagementPublicAPILogListLimit(limit int) int {
	if limit <= 0 {
		return defaultManagementPublicAPILogListLimit
	}
	return min(limit, maxManagementPublicAPILogListLimit)
}

func managementPublicAPILogListItem(row managementPublicAPILogListRow) port.ManagementPublicAPILogListItem {
	return port.ManagementPublicAPILogListItem{
		ID:         row.ID,
		CreatedAt:  row.CreatedAt,
		SourceName: textPtr(row.SourceName),
		Method:     row.Method,
		Path:       row.Path,
		Success:    row.Success,
		StatusCode: int4Ptr(row.StatusCode),
		DurationMs: managementPublicAPILogInt8Ptr(row.DurationMs),
		ClientIP:   textPtr(row.ClientIP),
		TraceID:    textPtr(row.TraceID),
	}
}

func managementPublicAPILogSummary(row managementPublicAPILogRow) port.ManagementPublicAPILogSummary {
	return port.ManagementPublicAPILogSummary{
		ID:                    row.ID,
		TraceID:               textPtr(row.TraceID),
		SourceRefID:           textPtr(row.SourceRefID),
		SourceName:            textPtr(row.SourceName),
		TokenID:               textPtr(row.TokenID),
		TokenName:             textPtr(row.TokenName),
		TokenPrefix:           textPtr(row.TokenPrefix),
		IsTestToken:           row.IsTestToken,
		Method:                row.Method,
		Path:                  row.Path,
		QueryString:           textPtr(row.QueryString),
		ClientIP:              textPtr(row.ClientIP),
		UserAgent:             textPtr(row.UserAgent),
		StatusCode:            int4Ptr(row.StatusCode),
		Success:               row.Success,
		DurationMs:            managementPublicAPILogInt8Ptr(row.DurationMs),
		RequestSizeBytes:      row.RequestSizeBytes,
		ResponseSizeBytes:     row.ResponseSizeBytes,
		RequestCaptureStatus:  port.PublicAPILogCaptureStatus(row.RequestCaptureStatus),
		ResponseCaptureStatus: port.PublicAPILogCaptureStatus(row.ResponseCaptureStatus),
		ErrorCode:             textPtr(row.ErrorCode),
		ErrorMessage:          textPtr(row.ErrorMessage),
		StartedAt:             row.StartedAt.UTC(),
		EndedAt:               row.EndedAt.UTC(),
		CreatedAt:             row.CreatedAt.UTC(),
	}
}

func managementPublicAPILogInt8Ptr(value pgtype.Int8) *int64 {
	if !value.Valid {
		return nil
	}
	output := value.Int64
	return &output
}

func (s *Store) GetManagementPublicAPILog(
	ctx context.Context,
	id string,
) (port.ManagementPublicAPILogDetail, bool, error) {
	return getManagementPublicAPILog(ctx, postgresManagementPublicAPILogExecutor{store: s}, id)
}

func getManagementPublicAPILog(
	ctx context.Context,
	executor managementPublicAPILogDetailExecutor,
	id string,
) (port.ManagementPublicAPILogDetail, bool, error) {
	row, err := executor.QueryManagementPublicAPILog(ctx, managementPublicAPILogDetailQuery, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return port.ManagementPublicAPILogDetail{}, false, nil
	}
	if err != nil {
		return port.ManagementPublicAPILogDetail{}, false, fmt.Errorf("get management public API log: %w", err)
	}
	return port.ManagementPublicAPILogDetail{
		ManagementPublicAPILogSummary: managementPublicAPILogSummary(row.managementPublicAPILogRow),
		RequestDataJSON:               row.RequestDataJSON,
		ResponseDataJSON:              row.ResponseDataJSON,
	}, true, nil
}

var _ port.ManagementPublicAPILogReader = (*Store)(nil)
