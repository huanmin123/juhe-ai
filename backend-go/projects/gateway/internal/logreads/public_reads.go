package logreads

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

// ---------------------------------------------------------------------------
// Public API logs (F5 dataset): /__aisys__/api/public-api-logs
//
// Contract source: backend/src/modules/public-api-logs/public-api-logs.routes.ts
// plus src/storage/public-api-logs.repository.ts. There is no Go F5 store yet
// (rows are written by the Node pipeline), so this file is the read-only SQL
// store over the public_api_logs dataset table.
// ---------------------------------------------------------------------------

// PublicApiLogListOptions mirrors PublicApiLogListOptions.
type PublicApiLogListOptions struct {
	Page        *int
	PageSize    *int
	TraceID     string
	SourceRefID string
	Path        string
	Result      string
	StatusCode  *int
	ClientIP    string
	StartAt     string
	EndAt       string
}

// PublicApiLogListItem mirrors PublicApiLogListItem.
type PublicApiLogListItem struct {
	ID         string `json:"id"`
	CreatedAt  string `json:"createdAt"`
	SourceName string `json:"sourceName,omitempty"`
	Method     string `json:"method"`
	Path       string `json:"path"`
	Success    bool   `json:"success"`
	StatusCode *int64 `json:"statusCode,omitempty"`
	DurationMS *int64 `json:"durationMs,omitempty"`
	ClientIP   string `json:"clientIp,omitempty"`
	TraceID    string `json:"traceId,omitempty"`
}

// PublicApiLogDetailSupplement mirrors PublicApiLogDetailSupplement: the
// columns the admin detail view merges into the list row.
type PublicApiLogDetailSupplement struct {
	SourceRefID           string         `json:"sourceRefId,omitempty"`
	TokenID               string         `json:"tokenId,omitempty"`
	TokenName             string         `json:"tokenName,omitempty"`
	TokenPrefix           string         `json:"tokenPrefix,omitempty"`
	IsTestToken           bool           `json:"isTestToken"`
	QueryString           string         `json:"queryString,omitempty"`
	UserAgent             string         `json:"userAgent,omitempty"`
	RequestSizeBytes      int64          `json:"requestSizeBytes"`
	ResponseSizeBytes     int64          `json:"responseSizeBytes"`
	RequestCaptureStatus  string         `json:"requestCaptureStatus"`
	ResponseCaptureStatus string         `json:"responseCaptureStatus"`
	ErrorCode             string         `json:"errorCode,omitempty"`
	ErrorMessage          string         `json:"errorMessage,omitempty"`
	StartedAt             string         `json:"startedAt"`
	EndedAt               string         `json:"endedAt"`
	RequestData           map[string]any `json:"requestData"`
	ResponseData          map[string]any `json:"responseData"`
}

// PublicApiLogReader is the read-only public API log dataset port.
type PublicApiLogReader interface {
	ListPublicApiLogs(ctx context.Context, options PublicApiLogListOptions) (ReadPage[PublicApiLogListItem], error)
	GetPublicApiLogDetailSupplement(ctx context.Context, id string) (*PublicApiLogDetailSupplement, error)
}

const (
	publicApiLogDefaultPageSize = 50
	publicApiLogMaxPageSize     = 100
)

// publicApiLogResultFilters mirrors the route result filter set.
var publicApiLogResultFilters = map[string]bool{"success": true, "failed": true, "all": true}

// publicApiLogSQLStore is the read-only store over public_api_logs (dataset
// schema juhe_dataset in PostgreSQL, fact file in SQLite). SELECTs only; the
// Node F5 pipeline stays the writer.
type publicApiLogSQLStore struct {
	db   *sql.DB
	mode ReadDBMode
}

// NewPublicApiLogSQLStore opens the read-only public API log store.
func NewPublicApiLogSQLStore(db *sql.DB, mode ReadDBMode) (PublicApiLogReader, error) {
	if db == nil {
		return nil, errors.New("public-api-logs 数据集数据库句柄必填")
	}
	dialect, err := parseReadDBMode(mode)
	if err != nil {
		return nil, err
	}
	return &publicApiLogSQLStore{db: db, mode: dialect}, nil
}

const publicApiLogListSelectColumns = `pal.id, pal.created_at, pal.source_name, pal.method, pal.path, ` +
	`pal.success, pal.status_code, pal.duration_ms, pal.client_ip, pal.trace_id`

const publicApiLogDetailSupplementSelectColumns = `pal.source_ref_id, pal.token_id, pal.token_name, pal.token_prefix, ` +
	`pal.is_test_token, pal.query_string, pal.user_agent, pal.request_size_bytes, pal.response_size_bytes, ` +
	`pal.request_capture_status, pal.response_capture_status, pal.error_code, pal.error_message, ` +
	`pal.started_at, pal.ended_at, pal.request_data_json, pal.response_data_json`

// buildFilters mirrors buildPublicApiLogFilters.
func (s *publicApiLogSQLStore) buildFilters(options PublicApiLogListOptions) (string, []any) {
	var clauses []string
	var params []any
	appendPrefix := func(column, value string) {
		text := strings.TrimSpace(value)
		if text == "" {
			return
		}
		expression := s.mode.prefixColumn(column)
		clauses = append(clauses, expression+" >= ? AND "+expression+" < ?")
		params = append(params, text, readTextPrefixUpperBound(text))
	}
	appendPrefix("pal.trace_id", options.TraceID)
	if sourceRefID := strings.TrimSpace(options.SourceRefID); sourceRefID != "" && sourceRefID != "all" {
		clauses = append(clauses, "pal.source_ref_id = ?")
		params = append(params, sourceRefID)
	}
	if normalized := readNormalizeAuditPathFilter(options.Path); normalized != "" && normalized != "all" {
		clauses = append(clauses, "pal.path = ?")
		params = append(params, normalized)
	}
	appendPrefix("pal.client_ip", options.ClientIP)
	switch options.Result {
	case "success":
		clauses = append(clauses, "pal.success = 1")
	case "failed":
		clauses = append(clauses, "pal.success = 0")
	}
	if options.StatusCode != nil {
		clauses = append(clauses, "pal.status_code = ?")
		params = append(params, *options.StatusCode)
	}
	// Node optionalServerDateTimeIso: invalid values are ignored, not errors.
	if startAt := readOptionalInstant(options.StartAt); startAt != "" {
		clauses = append(clauses, "pal.created_at >= ?")
		params = append(params, s.mode.timeParam(startAt))
	}
	if endAt := readOptionalInstant(options.EndAt); endAt != "" {
		clauses = append(clauses, "pal.created_at <= ?")
		params = append(params, s.mode.timeParam(endAt))
	}
	clause := ""
	if len(clauses) > 0 {
		clause = "WHERE " + strings.Join(clauses, " AND ")
	}
	return clause, params
}

// readOptionalInstant mirrors optionalServerDateTimeIso: canonicalize or drop.
func readOptionalInstant(value string) string {
	text := strings.TrimSpace(value)
	if text == "" {
		return ""
	}
	if canonical, ok := readCanonicalInstant(text); ok {
		return canonical
	}
	return ""
}

func (s *publicApiLogSQLStore) ListPublicApiLogs(ctx context.Context, options PublicApiLogListOptions) (ReadPage[PublicApiLogListItem], error) {
	pageSize := readNormalizePageSize(options.PageSize, publicApiLogDefaultPageSize, publicApiLogMaxPageSize)
	page := readNormalizePage(options.Page, pageSize)
	offset := (page - 1) * pageSize
	clause, params := s.buildFilters(options)
	query := `SELECT ` + publicApiLogListSelectColumns + ` FROM ` + s.mode.table("public_api_logs") + ` pal ` + clause +
		` ORDER BY pal.created_at DESC, pal.id DESC LIMIT ? OFFSET ?`
	rows, err := readQueryMaps(ctx, s.db, s.mode, query, append(params, pageSize+1, offset)...)
	if err != nil {
		return ReadPage[PublicApiLogListItem]{}, err
	}
	pageRows, hasMore := readPageRows(rows, pageSize)
	items := make([]PublicApiLogListItem, 0, len(pageRows))
	for _, row := range pageRows {
		items = append(items, PublicApiLogListItem{
			ID:         readRawString(row["id"]),
			CreatedAt:  readRawString(row["created_at"]),
			SourceName: readOptionalText(row["source_name"]),
			Method:     readRawString(row["method"]),
			Path:       readRawString(row["path"]),
			Success:    readNumberOr(row["success"], 0) == 1,
			StatusCode: readOptionalNumber(row["status_code"]),
			DurationMS: readOptionalNumber(row["duration_ms"]),
			ClientIP:   readOptionalText(row["client_ip"]),
			TraceID:    readOptionalText(row["trace_id"]),
		})
	}
	return ReadPage[PublicApiLogListItem]{
		Items: items, Total: readPagedTotal(page, pageSize, len(items), hasMore),
		HasMore: hasMore, Page: page, PageSize: pageSize,
	}, nil
}

func (s *publicApiLogSQLStore) GetPublicApiLogDetailSupplement(ctx context.Context, id string) (*PublicApiLogDetailSupplement, error) {
	logID := strings.TrimSpace(id)
	if logID == "" {
		return nil, nil
	}
	row, err := readQueryOneMap(ctx, s.db, s.mode,
		`SELECT `+publicApiLogDetailSupplementSelectColumns+` FROM `+s.mode.table("public_api_logs")+` pal WHERE pal.id = ?`,
		logID)
	if err != nil || row == nil {
		return nil, err
	}
	return &PublicApiLogDetailSupplement{
		SourceRefID:           readOptionalText(row["source_ref_id"]),
		TokenID:               readOptionalText(row["token_id"]),
		TokenName:             readOptionalText(row["token_name"]),
		TokenPrefix:           readOptionalText(row["token_prefix"]),
		IsTestToken:           readNumberOr(row["is_test_token"], 0) == 1,
		QueryString:           readOptionalText(row["query_string"]),
		UserAgent:             readOptionalText(row["user_agent"]),
		RequestSizeBytes:      readNumberOr(row["request_size_bytes"], 0),
		ResponseSizeBytes:     readNumberOr(row["response_size_bytes"], 0),
		RequestCaptureStatus:  readCaptureStatus(row["request_capture_status"]),
		ResponseCaptureStatus: readCaptureStatus(row["response_capture_status"]),
		ErrorCode:             readOptionalText(row["error_code"]),
		ErrorMessage:          readOptionalText(row["error_message"]),
		StartedAt:             readRawString(row["started_at"]),
		EndedAt:               readRawString(row["ended_at"]),
		RequestData:           readJSONObject(row["request_data_json"]),
		ResponseData:          readJSONObject(row["response_data_json"]),
	}, nil
}

// readCaptureStatus mirrors normalizeCaptureStatus: unknown values fall back
// to 'empty'.
func readCaptureStatus(value any) string {
	switch readOptionalText(value) {
	case "complete", "truncated", "empty", "dropped":
		return readOptionalText(value)
	default:
		return "empty"
	}
}

// readJSONObject mirrors parseJsonObject: object JSON only, else {}.
func readJSONObject(value any) map[string]any {
	empty := map[string]any{}
	text, ok := value.(string)
	if !ok || strings.TrimSpace(text) == "" {
		if bytes, isBytes := value.([]byte); isBytes {
			text = string(bytes)
		} else {
			return empty
		}
	}
	var parsed any
	if err := json.Unmarshal([]byte(text), &parsed); err != nil {
		return empty
	}
	if object, ok := parsed.(map[string]any); ok {
		return object
	}
	return empty
}

// parsePublicApiLogListOptions mirrors parsePublicApiLogListOptions: the
// route spreads strictDateTimeRangeQueryValue, so invalid bounds are 400 and
// a reversed pair is swapped before the repository normalizes them again.
func parsePublicApiLogListOptions(r *http.Request) (PublicApiLogListOptions, error) {
	startAt, endAt, err := readQueryDateTimeRange(r)
	if err != nil {
		return PublicApiLogListOptions{}, err
	}
	result := readQueryText(r, "result")
	if !publicApiLogResultFilters[result] {
		result = ""
	}
	return PublicApiLogListOptions{
		Page:        readQueryInt(r, "page"),
		PageSize:    readQueryInt(r, "pageSize"),
		TraceID:     readQueryText(r, "traceId"),
		SourceRefID: readQueryText(r, "sourceRefId"),
		Path:        readQueryText(r, "path"),
		Result:      result,
		StatusCode:  readQueryStatusCode(r, "statusCode"),
		ClientIP:    readQueryText(r, "clientIp"),
		StartAt:     startAt,
		EndAt:       endAt,
	}, nil
}

func (d *ReadsDeps) handleListPublicApiLogs(w http.ResponseWriter, r *http.Request) {
	options, err := parsePublicApiLogListOptions(r)
	if readWriteStoreError(w, err) {
		return
	}
	result, err := d.Public.ListPublicApiLogs(r.Context(), options)
	if readWriteStoreError(w, err) {
		return
	}
	kernel.WriteOK(w, result, "")
}

func (d *ReadsDeps) handleGetPublicApiLogDetail(w http.ResponseWriter, r *http.Request) {
	supplement, err := d.Public.GetPublicApiLogDetailSupplement(r.Context(), r.PathValue("id"))
	if readWriteStoreError(w, err) {
		return
	}
	if supplement == nil {
		kernel.WriteNotFound(w, "公开接口日志不存在")
		return
	}
	kernel.WriteOK(w, supplement, "")
}
