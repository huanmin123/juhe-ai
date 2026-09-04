package logreads

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

// ---------------------------------------------------------------------------
// Runtime logs (F1 dataset): /__aisys__/api/runtime-logs
//
// Contract source: backend/src/modules/runtime-logs/runtime-logs.routes.ts plus
// src/storage/runtime-log-query.repository.ts (list/facets/detail-delta). The
// grep endpoints stay on the Node file scanner and are intentionally not part
// of this dataset read slice.
// ---------------------------------------------------------------------------

// RuntimeLogListOptions mirrors RuntimeLogListOptions.
type RuntimeLogListOptions struct {
	Page     *int
	PageSize *int
	TraceID  string
	Level    string
	Event    string
	Keyword  string
	StartAt  string
	EndAt    string
}

// RuntimeLogListItem mirrors RuntimeLogListItem (list projection with SUBSTR
// bounds applied in SQL).
type RuntimeLogListItem struct {
	ID           string `json:"id"`
	Time         string `json:"time"`
	Level        string `json:"level"`
	TraceID      string `json:"traceId,omitempty"`
	Event        string `json:"event,omitempty"`
	Message      string `json:"message,omitempty"`
	ErrorMessage string `json:"errorMessage,omitempty"`
}

// RuntimeLogDetailDelta mirrors RuntimeLogDetailDelta ({id, rawJson}).
type RuntimeLogDetailDelta struct {
	ID      string `json:"id"`
	RawJSON string `json:"rawJson"`
}

// RuntimeLogLevelFacet mirrors one {value, count} levels entry.
type RuntimeLogLevelFacet struct {
	Value string `json:"value"`
	Count int64  `json:"count"`
}

// RuntimeLogFacets mirrors RuntimeLogFacets.
type RuntimeLogFacets struct {
	RetentionDays     int                    `json:"retentionDays"`
	EarliestIndexedAt string                 `json:"earliestIndexedAt,omitempty"`
	LatestIndexedAt   string                 `json:"latestIndexedAt,omitempty"`
	TotalIndexed      int64                  `json:"totalIndexed"`
	Levels            []RuntimeLogLevelFacet `json:"levels"`
	Events            []string               `json:"events"`
}

// RuntimeLogReader is the read-only runtime log dataset port.
type RuntimeLogReader interface {
	ListRuntimeLogs(ctx context.Context, options RuntimeLogListOptions) (ReadPage[RuntimeLogListItem], error)
	GetRuntimeLogFacets(ctx context.Context) (RuntimeLogFacets, error)
	GetRuntimeLogDetailDelta(ctx context.Context, id string) (*RuntimeLogDetailDelta, error)
}

const (
	runtimeLogDefaultPageSize      = 100
	runtimeLogMaxPageSize          = 100
	runtimeLogKeywordWindowHours   = 6
	runtimeLogDefaultRetentionDays = 14
	runtimeLogMaxRetentionDays     = 90
	runtimeLogFacetBucketKey       = "current"
	runtimeLogFacetMaxEvents       = 80
)

// runtimeLogLevels mirrors the route level validation set ('all' included).
var runtimeLogLevels = map[string]bool{
	"all": true, "trace": true, "debug": true, "info": true, "warn": true, "error": true, "fatal": true,
}

// runtimeLogSQLReader reads the runtime_logs dataset published by the F1
// indexer (projects/jobs/internal/runtimelog). SELECTs only.
type runtimeLogSQLReader struct {
	db   *sql.DB
	mode ReadDBMode
	// Now backs the keyword default time window; nil means time.Now. It is a
	// deliberate seam so tests can pin the window.
	Now func() time.Time
	// RetentionDays overrides the runtimeLogIndexRetentionDays setting read
	// from the business settings store (Node clamps it to 1..90, default 14).
	RetentionDays func() int
}

// NewRuntimeLogSQLReader opens the read-only runtime log dataset adapter. The
// caller owns the database handle and the schema (F1 EnsureSchema).
func NewRuntimeLogSQLReader(db *sql.DB, mode ReadDBMode) (RuntimeLogReader, error) {
	if db == nil {
		return nil, errors.New("runtime 日志数据集数据库句柄必填")
	}
	dialect, err := parseReadDBMode(mode)
	if err != nil {
		return nil, err
	}
	return &runtimeLogSQLReader{db: db, mode: dialect}, nil
}

func (s *runtimeLogSQLReader) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *runtimeLogSQLReader) retentionDays() int {
	if s.RetentionDays == nil {
		return runtimeLogDefaultRetentionDays
	}
	return min(max(1, s.RetentionDays()), runtimeLogMaxRetentionDays)
}

const runtimeLogListSelectColumns = `rl.id, rl.time, rl.level, ` +
	`SUBSTR(rl.trace_id, 1, 256) AS trace_id, SUBSTR(rl.event, 1, 256) AS event, ` +
	`SUBSTR(rl.message, 1, 1000) AS message, SUBSTR(rl.error_message, 1, 1000) AS error_message`

// buildFilters mirrors buildRuntimeLogFilters.
func (s *runtimeLogSQLReader) buildFilters(options RuntimeLogListOptions) (string, []any) {
	var clauses []string
	var params []any
	if traceID := strings.TrimSpace(options.TraceID); traceID != "" {
		expression := s.mode.prefixColumn("rl.trace_id")
		clauses = append(clauses, expression+" >= ? AND "+expression+" < ?")
		params = append(params, traceID, readTextPrefixUpperBound(traceID))
	}
	if event := strings.TrimSpace(options.Event); event != "" {
		clauses = append(clauses, "rl.event = ?")
		params = append(params, event)
	}
	level := strings.ToLower(strings.TrimSpace(options.Level))
	if level != "" && level != "all" {
		clauses = append(clauses, "rl.level = ?")
		params = append(params, level)
	}
	if options.StartAt != "" {
		clauses = append(clauses, "rl.time >= ?")
		params = append(params, s.mode.timeParam(options.StartAt))
	}
	if options.EndAt != "" {
		clauses = append(clauses, "rl.time <= ?")
		params = append(params, s.mode.timeParam(options.EndAt))
	}
	keyword := strings.TrimSpace(options.Keyword)
	if keyword != "" && options.StartAt == "" && options.EndAt == "" {
		clauses = append(clauses, "rl.time >= ?")
		window := s.now().Add(-runtimeLogKeywordWindowHours * time.Hour)
		params = append(params, s.mode.timeParam(readISOText(window)))
	}
	if keyword != "" {
		escaped := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(keyword)
		clauses = append(clauses, `rl.message LIKE ? ESCAPE '\'`)
		params = append(params, "%"+escaped+"%")
	}
	clause := ""
	if len(clauses) > 0 {
		clause = "WHERE " + strings.Join(clauses, " AND ")
	}
	return clause, params
}

func (s *runtimeLogSQLReader) ListRuntimeLogs(ctx context.Context, options RuntimeLogListOptions) (ReadPage[RuntimeLogListItem], error) {
	pageSize := readNormalizePageSize(options.PageSize, runtimeLogDefaultPageSize, runtimeLogMaxPageSize)
	page := readNormalizePage(options.Page, pageSize)
	offset := (page - 1) * pageSize
	clause, params := s.buildFilters(options)
	query := `SELECT ` + runtimeLogListSelectColumns + ` FROM ` + s.mode.table("runtime_logs") + ` rl ` + clause +
		` ORDER BY rl.time DESC, rl.id DESC LIMIT ? OFFSET ?`
	rows, err := readQueryMaps(ctx, s.db, s.mode, query, append(params, pageSize+1, offset)...)
	if err != nil {
		return ReadPage[RuntimeLogListItem]{}, err
	}
	pageRows, hasMore := readPageRows(rows, pageSize)
	items := make([]RuntimeLogListItem, 0, len(pageRows))
	for _, row := range pageRows {
		items = append(items, RuntimeLogListItem{
			ID:           readRawString(row["id"]),
			Time:         readRawString(row["time"]),
			Level:        readRawString(row["level"]),
			TraceID:      readOptionalText(row["trace_id"]),
			Event:        readOptionalText(row["event"]),
			Message:      readOptionalText(row["message"]),
			ErrorMessage: readOptionalText(row["error_message"]),
		})
	}
	return ReadPage[RuntimeLogListItem]{
		Items: items, Total: readPagedTotal(page, pageSize, len(items), hasMore),
		HasMore: hasMore, Page: page, PageSize: pageSize,
	}, nil
}

func (s *runtimeLogSQLReader) GetRuntimeLogFacets(ctx context.Context) (RuntimeLogFacets, error) {
	facets := RuntimeLogFacets{RetentionDays: s.retentionDays(), Levels: []RuntimeLogLevelFacet{}, Events: []string{}}
	summary, err := readQueryOneMap(ctx, s.db, s.mode,
		`SELECT earliest_time, latest_time, total_count FROM `+s.mode.table("runtime_log_facet_summary")+` WHERE bucket_key = ?`,
		runtimeLogFacetBucketKey)
	if err != nil {
		return RuntimeLogFacets{}, err
	}
	if summary != nil {
		facets.EarliestIndexedAt = readOptionalText(summary["earliest_time"])
		facets.LatestIndexedAt = readOptionalText(summary["latest_time"])
		facets.TotalIndexed = readNumberOr(summary["total_count"], 0)
	}
	levelRows, err := readQueryMaps(ctx, s.db, s.mode,
		`SELECT level AS value, count FROM `+s.mode.table("runtime_log_level_facets")+` WHERE bucket_key = ? AND count > 0 ORDER BY count DESC, level ASC`,
		runtimeLogFacetBucketKey)
	if err != nil {
		return RuntimeLogFacets{}, err
	}
	for _, row := range levelRows {
		facets.Levels = append(facets.Levels, RuntimeLogLevelFacet{Value: readRawString(row["value"]), Count: readNumberOr(row["count"], 0)})
	}
	eventRows, err := readQueryMaps(ctx, s.db, s.mode,
		`SELECT event FROM `+s.mode.table("runtime_log_event_facets")+` WHERE bucket_key = ? AND count > 0 ORDER BY latest_time DESC, event ASC LIMIT ?`,
		runtimeLogFacetBucketKey, runtimeLogFacetMaxEvents)
	if err != nil {
		return RuntimeLogFacets{}, err
	}
	for _, row := range eventRows {
		facets.Events = append(facets.Events, readRawString(row["event"]))
	}
	return facets, nil
}

func (s *runtimeLogSQLReader) GetRuntimeLogDetailDelta(ctx context.Context, id string) (*RuntimeLogDetailDelta, error) {
	logID := strings.TrimSpace(id)
	if logID == "" {
		return nil, nil
	}
	row, err := readQueryOneMap(ctx, s.db, s.mode,
		`SELECT id, raw_json FROM `+s.mode.table("runtime_logs")+` WHERE id = ? LIMIT 1`, logID)
	if err != nil || row == nil {
		return nil, err
	}
	return &RuntimeLogDetailDelta{ID: readRawString(row["id"]), RawJSON: readRawString(row["raw_json"])}, nil
}

// parseRuntimeLogListOptions mirrors parseRuntimeLogListOptions.
func parseRuntimeLogListOptions(r *http.Request) (RuntimeLogListOptions, error) {
	startAt, endAt, err := readQueryDateTimeRange(r)
	if err != nil {
		return RuntimeLogListOptions{}, err
	}
	level := strings.ToLower(readQueryText(r, "level"))
	if !runtimeLogLevels[level] {
		level = ""
	}
	return RuntimeLogListOptions{
		Page:     readQueryInt(r, "page"),
		PageSize: readQueryInt(r, "pageSize"),
		TraceID:  readQueryText(r, "traceId"),
		Level:    level,
		Event:    readQueryText(r, "event"),
		Keyword:  readQueryText(r, "keyword"),
		StartAt:  startAt,
		EndAt:    endAt,
	}, nil
}

func (d *ReadsDeps) handleListRuntimeLogs(w http.ResponseWriter, r *http.Request) {
	options, err := parseRuntimeLogListOptions(r)
	if readWriteStoreError(w, err) {
		return
	}
	result, err := d.Runtime.ListRuntimeLogs(r.Context(), options)
	if readWriteStoreError(w, err) {
		return
	}
	kernel.WriteOK(w, result, "")
}

func (d *ReadsDeps) handleRuntimeLogFacets(w http.ResponseWriter, r *http.Request) {
	result, err := d.Runtime.GetRuntimeLogFacets(r.Context())
	if readWriteStoreError(w, err) {
		return
	}
	kernel.WriteOK(w, result, "")
}

func (d *ReadsDeps) handleGetRuntimeLogDetail(w http.ResponseWriter, r *http.Request) {
	detail, err := d.Runtime.GetRuntimeLogDetailDelta(r.Context(), r.PathValue("id"))
	if readWriteStoreError(w, err) {
		return
	}
	if detail == nil {
		kernel.WriteNotFound(w, "运行日志不存在")
		return
	}
	kernel.WriteOK(w, detail, "")
}
