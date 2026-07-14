package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"juhe-ai/backend-go/internal/store/port"
	"juhe-ai/backend-go/internal/store/postgres/postgresqueries"
)

const (
	defaultRuntimeLogListLimit = 100
	maxRuntimeLogListLimit     = 100
	maxRuntimeLogListRows      = 1000
)

type runtimeLogQueries interface {
	GetRuntimeLogDetail(
		ctx context.Context,
		id string,
	) (postgresqueries.GetRuntimeLogDetailRow, error)
}

type runtimeLogListRow struct {
	ID           string
	Time         string
	Level        string
	TraceID      pgtype.Text
	Event        pgtype.Text
	Message      pgtype.Text
	ErrorMessage pgtype.Text
	CreatedAt    string
}

type runtimeLogListExecutor interface {
	QueryRuntimeLogs(ctx context.Context, query string, args ...any) ([]runtimeLogListRow, error)
}

type postgresRuntimeLogListExecutor struct {
	store *Store
}

func (e postgresRuntimeLogListExecutor) QueryRuntimeLogs(
	ctx context.Context,
	query string,
	args ...any,
) ([]runtimeLogListRow, error) {
	rows, err := e.store.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]runtimeLogListRow, 0)
	for rows.Next() {
		var item runtimeLogListRow
		if err := rows.Scan(
			&item.ID,
			&item.Time,
			&item.Level,
			&item.TraceID,
			&item.Event,
			&item.Message,
			&item.ErrorMessage,
			&item.CreatedAt,
		); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

func (s *Store) ListManagementRuntimeLogs(
	ctx context.Context,
	input port.ManagementRuntimeLogListInput,
) (port.ManagementRuntimeLogListResult, error) {
	return listManagementRuntimeLogs(ctx, postgresRuntimeLogListExecutor{store: s}, input)
}

func listManagementRuntimeLogs(
	ctx context.Context,
	executor runtimeLogListExecutor,
	input port.ManagementRuntimeLogListInput,
) (port.ManagementRuntimeLogListResult, error) {
	limit := normalizeRuntimeLogListLimit(input.Limit)
	offset := min(max(0, input.Offset), maxRuntimeLogListRows-limit)
	query, args := runtimeLogListQuery(input, limit+1, offset)
	rows, err := executor.QueryRuntimeLogs(ctx, query, args...)
	if err != nil {
		return port.ManagementRuntimeLogListResult{}, fmt.Errorf("list runtime logs: %w", err)
	}

	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	items := make([]port.ManagementRuntimeLogSummary, 0, len(rows))
	for _, row := range rows {
		items = append(items, runtimeLogSummary(
			row.ID,
			row.Time,
			row.Level,
			textPtr(row.TraceID),
			textPtr(row.Event),
			textPtr(row.Message),
			textPtr(row.ErrorMessage),
			row.CreatedAt,
		))
	}
	return port.ManagementRuntimeLogListResult{
		Items:   items,
		HasMore: hasMore,
	}, nil
}

func runtimeLogListQuery(
	input port.ManagementRuntimeLogListInput,
	rowLimit int,
	rowOffset int,
) (string, []any) {
	const selectRuntimeLogs = `SELECT
  rl.id,
  rl.time,
  rl.level,
  rl.trace_id,
  rl.event,
  rl.message,
  rl.error_message,
  rl.created_at
FROM juhe_dataset.runtime_logs AS rl`

	conditions := make([]string, 0, 7)
	args := make([]any, 0, 9)
	addArg := func(value any) string {
		args = append(args, value)
		return fmt.Sprintf("$%d", len(args))
	}

	traceID := runtimeLogTrimECMAScriptWhitespace(input.TraceID)
	if traceID != "" {
		traceLowerArg := addArg(traceID)
		traceUpperArg := addArg(runtimeLogTraceUpper(traceID))
		conditions = append(conditions,
			`rl.trace_id COLLATE "C" >= `+traceLowerArg+`::text`,
			`rl.trace_id COLLATE "C" < `+traceUpperArg+`::text`,
		)
	}
	if level := runtimeLogLevel(input.Level); level != "" {
		conditions = append(conditions, `rl.level = `+addArg(level)+`::text`)
	}
	if event := runtimeLogTrimECMAScriptWhitespace(input.Event); event != "" {
		conditions = append(conditions, `rl.event = `+addArg(event)+`::text`)
	}
	startAt, endAt := runtimeLogTimeRange(input.StartAt, input.EndAt)
	if startAt != "" {
		conditions = append(conditions, `rl.time >= `+addArg(startAt)+`::text`)
	}
	if endAt != "" {
		conditions = append(conditions, `rl.time <= `+addArg(endAt)+`::text`)
	}
	if keywordPattern := runtimeLogKeywordPattern(input.Keyword); keywordPattern != "" {
		conditions = append(conditions, `rl.message LIKE `+addArg(keywordPattern)+`::text ESCAPE '\'`)
	}

	var query strings.Builder
	query.WriteString(selectRuntimeLogs)
	if len(conditions) > 0 {
		query.WriteString("\nWHERE ")
		query.WriteString(strings.Join(conditions, "\n  AND "))
	}
	query.WriteString("\nORDER BY rl.time DESC, rl.id DESC")
	query.WriteString("\nLIMIT ")
	query.WriteString(addArg(int32(rowLimit)))
	query.WriteString("::int")
	query.WriteString("\nOFFSET ")
	query.WriteString(addArg(int32(rowOffset)))
	query.WriteString("::int")
	return query.String(), args
}

func (s *Store) GetManagementRuntimeLog(
	ctx context.Context,
	id string,
) (port.ManagementRuntimeLog, bool, error) {
	return getManagementRuntimeLog(ctx, s.queries(), id)
}

func getManagementRuntimeLog(
	ctx context.Context,
	q runtimeLogQueries,
	id string,
) (port.ManagementRuntimeLog, bool, error) {
	row, err := q.GetRuntimeLogDetail(ctx, runtimeLogTrimECMAScriptWhitespace(id))
	if errors.Is(err, pgx.ErrNoRows) {
		return port.ManagementRuntimeLog{}, false, nil
	}
	if err != nil {
		return port.ManagementRuntimeLog{}, false, fmt.Errorf("get runtime log detail: %w", err)
	}
	return port.ManagementRuntimeLog{
		ManagementRuntimeLogSummary: runtimeLogSummary(
			row.ID,
			row.Time,
			row.Level,
			textPtr(row.TraceID),
			textPtr(row.Event),
			textPtr(row.Message),
			textPtr(row.ErrorMessage),
			row.CreatedAt,
		),
		RawJSON: row.RawJson,
	}, true, nil
}

func runtimeLogSummary(
	id string,
	timeText string,
	level string,
	traceID *string,
	event *string,
	message *string,
	errorMessage *string,
	createdAt string,
) port.ManagementRuntimeLogSummary {
	return port.ManagementRuntimeLogSummary{
		ID:           id,
		Time:         timeText,
		Level:        level,
		TraceID:      traceID,
		Event:        event,
		Message:      message,
		ErrorMessage: errorMessage,
		CreatedAt:    createdAt,
	}
}

func normalizeRuntimeLogListLimit(limit int) int {
	if limit <= 0 {
		return defaultRuntimeLogListLimit
	}
	return min(limit, maxRuntimeLogListLimit)
}

func runtimeLogLevel(level string) string {
	value := strings.ToLower(runtimeLogTrimECMAScriptWhitespace(level))
	switch value {
	case port.RuntimeLogLevelTrace,
		port.RuntimeLogLevelDebug,
		port.RuntimeLogLevelInfo,
		port.RuntimeLogLevelWarn,
		port.RuntimeLogLevelError,
		port.RuntimeLogLevelFatal:
		return value
	default:
		return ""
	}
}

func runtimeLogTraceUpper(traceID string) string {
	if traceID == "" {
		return ""
	}
	return textPrefixUpperBound(traceID)
}

func runtimeLogKeywordPattern(keyword string) string {
	keyword = runtimeLogTrimECMAScriptWhitespace(keyword)
	if keyword == "" {
		return ""
	}
	escaped := strings.NewReplacer(
		`\`, `\\`,
		`%`, `\%`,
		`_`, `\_`,
	).Replace(keyword)
	return "%" + escaped + "%"
}

func runtimeLogTimeRange(startAt string, endAt string) (string, string) {
	startAt = runtimeLogTrimECMAScriptWhitespace(startAt)
	endAt = runtimeLogTrimECMAScriptWhitespace(endAt)
	if startAt != "" && endAt != "" && startAt > endAt {
		return endAt, startAt
	}
	return startAt, endAt
}

func runtimeLogTrimECMAScriptWhitespace(value string) string {
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

var _ port.ManagementRuntimeLogReader = (*Store)(nil)
