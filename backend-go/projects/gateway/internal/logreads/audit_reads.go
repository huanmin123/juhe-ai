// Package logreads mounts the read-only admin routes for the three log
// datasets migrated from Node: audit-logs (F3 dataset tables), runtime-logs
// (F1 dataset tables) and public-api-logs (F5 dataset table). The package is
// deliberately read-only: it never creates or mutates schema objects and never
// owns a writer. Query semantics, parameter names and pagination envelopes
// mirror the Node contracts in backend/src/modules/{audit-logs,runtime-logs,
// public-api-logs} and their storage repositories.
//
// File layout (prefixes keep parallel read slices coexistable in this
// package): audit_reads.go carries the shared read plumbing plus the audit
// family, runtime_reads.go the runtime family, public_reads.go the public API
// log family. Each family has a local Reader interface so route tests can
// mock the store boundary.
package logreads

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

// ReadDBMode selects the SQL dialect of a log dataset reader. The dataset
// tables live in SQLite fact files during migration and in the PostgreSQL
// juhe_dataset schema in production.
type ReadDBMode string

// Supported dataset modes.
const (
	ReadSQLite   ReadDBMode = "sqlite"
	ReadPostgres ReadDBMode = "postgres"
)

// logReadsDatasetSchema is the PostgreSQL schema holding the dataset tables
// (Node defaults every dataset repository to juhe_dataset).
const logReadsDatasetSchema = "juhe_dataset"

func parseReadDBMode(value ReadDBMode) (ReadDBMode, error) {
	switch value {
	case ReadSQLite, ReadPostgres:
		return value, nil
	case "":
		return "", errors.New("logreads 数据库模式必填（sqlite 或 postgres）")
	default:
		return "", fmt.Errorf("logreads 数据库模式必须为 sqlite 或 postgres，收到 %q", string(value))
	}
}

// table quotes a dataset table name for the active dialect.
func (m ReadDBMode) table(name string) string {
	if m == ReadPostgres {
		return `"` + logReadsDatasetSchema + `"."` + name + `"`
	}
	return `"` + name + `"`
}

// bind rewrites ? placeholders into PostgreSQL $N ordinals; SQLite keeps ?.
func (m ReadDBMode) bind(query string) string {
	if m != ReadPostgres {
		return query
	}
	var builder strings.Builder
	index := 0
	for _, character := range query {
		if character != '?' {
			builder.WriteRune(character)
			continue
		}
		index++
		builder.WriteByte('$')
		builder.WriteString(strconv.Itoa(index))
	}
	return builder.String()
}

// prefixColumn mirrors the postgres-only COLLATE "C" prefix filters.
func (m ReadDBMode) prefixColumn(column string) string {
	if m == ReadPostgres {
		return column + ` COLLATE "C"`
	}
	return column
}

// timeParam passes time filters as strings to SQLite and as time values to
// PostgreSQL so timestamptz comparisons never fall back to text casting.
func (m ReadDBMode) timeParam(canonicalISO string) any {
	if m != ReadPostgres {
		return canonicalISO
	}
	if parsed, err := time.Parse(time.RFC3339Nano, canonicalISO); err == nil {
		return parsed.UTC()
	}
	return canonicalISO
}

// readParamError is a 400-class query contract error (Node throws errors with
// a statusCode field from the route parsers).
type readParamError struct {
	message string
}

func (e *readParamError) Error() string { return e.message }

func readParamErrorf(format string, args ...any) *readParamError {
	return &readParamError{message: fmt.Sprintf(format, args...)}
}

// readQueryText mirrors optionalQueryText: first value, trimmed, "" when
// absent or blank.
func readQueryText(r *http.Request, name string) string {
	return strings.TrimSpace(r.URL.Query().Get(name))
}

// readQueryInt mirrors finiteNumberQueryValue + Number.isInteger: nil when the
// value is absent, non-finite or fractional.
func readQueryInt(r *http.Request, name string) *int {
	text := readQueryText(r, name)
	if text == "" {
		return nil
	}
	value, err := strconv.ParseFloat(text, 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value != math.Trunc(value) {
		return nil
	}
	if value < -9e15 || value > 9e15 {
		return nil
	}
	out := int(value)
	return &out
}

// readQueryStatusCode mirrors isHttpStatusCode.
func readQueryStatusCode(r *http.Request, name string) *int {
	value := readQueryInt(r, name)
	if value == nil || *value < 100 || *value > 599 {
		return nil
	}
	return value
}

// readCanonicalInstant mirrors canonicalizeRfc3339Instant (RFC3339 with Z or
// numeric offset, normalized to a UTC ISO string).
func readCanonicalInstant(text string) (string, bool) {
	parsed, err := time.Parse(time.RFC3339Nano, text)
	if err != nil {
		return "", false
	}
	return readISOText(parsed), true
}

// readISOText formats instants the way JavaScript toISOString does.
func readISOText(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z")
}

// readQueryDateTimeRange mirrors strictDateTimeRangeQueryValue: both bounds
// are strict RFC3339 instants and a reversed pair is swapped.
func readQueryDateTimeRange(r *http.Request) (startAt string, endAt string, err error) {
	startAt, err = readQueryStrictInstant(r, "startAt", "开始时间")
	if err != nil {
		return "", "", err
	}
	endAt, err = readQueryStrictInstant(r, "endAt", "结束时间")
	if err != nil {
		return "", "", err
	}
	if startAt != "" && endAt != "" && startAt > endAt {
		return endAt, startAt, nil
	}
	return startAt, endAt, nil
}

func readQueryStrictInstant(r *http.Request, name, label string) (string, error) {
	text := readQueryText(r, name)
	if text == "" {
		return "", nil
	}
	if canonical, ok := readCanonicalInstant(text); ok {
		return canonical, nil
	}
	return "", readParamErrorf("%s必须是带 Z 或数值 offset 的 RFC3339 时间", label)
}

// readNormalizePageSize mirrors normalizePageSize / normalizeRuntimeLogPageSize.
func readNormalizePageSize(value *int, fallback int, max int) int {
	if value == nil {
		return fallback
	}
	if *value < 1 {
		return fallback
	}
	return min(*value, max)
}

// readPageWindowRows is the shared 1001-row list window (defaultListWindowRows).
const readPageWindowRows = 1001

// readNormalizePage mirrors normalizeListPage: pages beyond the window clamp.
func readNormalizePage(value *int, pageSize int) int {
	maxPage := max(1, (readPageWindowRows-1)/max(1, pageSize))
	if value == nil {
		return 1
	}
	return min(maxPage, max(1, *value))
}

// readNormalizeAuditLogPage mirrors normalizeAuditLogPage: a sessionId filter
// unlocks deep pagination because sessions are small and strictly ordered.
func readNormalizeAuditLogPage(value *int, pageSize int, sessionID string) int {
	if strings.TrimSpace(sessionID) != "" {
		if value == nil {
			return 1
		}
		return max(1, *value)
	}
	return readNormalizePage(value, pageSize)
}

// readPagedTotal mirrors pagedTotalUpperBound: totals are upper bounds so the
// admin tables can render "more than N" without COUNT(*) scans.
func readPagedTotal(page int, pageSize int, itemCount int, hasMore bool) int64 {
	total := (max(1, page)-1)*max(0, pageSize) + max(0, itemCount)
	if hasMore {
		total++
	}
	return int64(total)
}

// readPageRows mirrors takePageRows: fetch pageSize+1 rows to detect hasMore.
func readPageRows[T any](rows []T, pageSize int) ([]T, bool) {
	if len(rows) > pageSize {
		return rows[:pageSize], true
	}
	return rows, false
}

// readTextPrefixUpperBound mirrors the shared query-utils textPrefixUpperBound
// (runtime-logs / public-api-logs): increment the last code point.
func readTextPrefixUpperBound(value string) string {
	runes := []rune(value)
	for index := len(runes) - 1; index >= 0; index-- {
		if runes[index] < 0x10FFFF {
			return string(runes[:index]) + string(runes[index]+1)
		}
	}
	return value + "\U0010FFFF"
}

// readAuditPrefixUpperBound mirrors the F3 helper textPrefixUpperBound, which
// appends U+FFFF instead of incrementing.
func readAuditPrefixUpperBound(value string) string {
	return value + "\uFFFF"
}

// readQueryMaps runs a read-only query and returns rows as column→value maps,
// mirroring the Node AuditLogRow/Record<string, unknown> row style so the
// mappers below stay 1:1 with the Node repositories.
func readQueryMaps(ctx context.Context, db *sql.DB, mode ReadDBMode, query string, args ...any) ([]map[string]any, error) {
	rows, err := db.QueryContext(ctx, mode.bind(query), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for rows.Next() {
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for index := range values {
			pointers[index] = &values[index]
		}
		if err := rows.Scan(pointers...); err != nil {
			return nil, err
		}
		row := make(map[string]any, len(columns))
		for index, name := range columns {
			row[name] = values[index]
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func readQueryOneMap(ctx context.Context, db *sql.DB, mode ReadDBMode, query string, args ...any) (map[string]any, error) {
	rows, err := readQueryMaps(ctx, db, mode, query, args...)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return rows[0], nil
}

// readRawString mirrors String(row.x) with driver-aware conversions.
func readRawString(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case []byte:
		return string(typed)
	case time.Time:
		return readISOText(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	case float64:
		return strconv.FormatFloat(typed, 'g', -1, 64)
	case bool:
		if typed {
			return "true"
		}
		return "false"
	default:
		return fmt.Sprint(value)
	}
}

// readOptionalText mirrors the mapper text() helper: trimmed non-empty strings
// only; everything else collapses to "" (dropped by omitempty).
func readOptionalText(value any) string {
	return strings.TrimSpace(readRawString(value))
}

// readOptionalNumber mirrors num(): finite numbers only.
func readOptionalNumber(value any) *int64 {
	number, ok := readInt64(value)
	if !ok {
		return nil
	}
	return &number
}

func readNumberOr(value any, fallback int64) int64 {
	if number, ok := readInt64(value); ok {
		return number
	}
	return fallback
}

func readInt64(value any) (int64, bool) {
	switch typed := value.(type) {
	case nil:
		return 0, false
	case int64:
		return typed, true
	case int:
		return int64(typed), true
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) {
			return 0, false
		}
		return int64(typed), true
	case bool:
		if typed {
			return 1, true
		}
		return 0, true
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
			return 0, false
		}
		return int64(parsed), true
	case time.Time:
		return typed.UnixMilli(), true
	default:
		return 0, false
	}
}

// readBool mirrors the mapper bool() helper: true/1/"1" are truthy.
func readBool(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case bool:
		return typed
	case int64:
		return typed != 0
	case int:
		return typed != 0
	case float64:
		return typed != 0
	case string:
		return typed == "1" || strings.EqualFold(typed, "true")
	default:
		return false
	}
}

// ReadsDeps wires the three log read route families onto the admin surface.
type ReadsDeps struct {
	Audit   AuditLogReader
	Runtime RuntimeLogReader
	Public  PublicApiLogReader
	Auth    *authsys.Deps
}

// Mount registers every read route under /__aisys__/api behind requireAdmin,
// matching the Node routers (each router mounts requireAdmin for the whole
// family).
func (d *ReadsDeps) Mount(k *kernel.Kernel) {
	prefix := "/__aisys__/api"
	admin := func(handler http.Handler) http.Handler {
		return d.Auth.RequireAdmin(handler)
	}
	// Audit-logs family (backend/src/modules/audit-logs/audit-logs.routes.ts).
	k.Register("GET "+prefix+"/audit-logs", admin(http.HandlerFunc(d.handleListAuditLogs)))
	k.Register("GET "+prefix+"/audit-logs/runtime", admin(http.HandlerFunc(d.handleAuditLogRuntime)))
	k.Register("GET "+prefix+"/audit-logs/error-groups", admin(http.HandlerFunc(d.handleListAuditErrorGroups)))
	k.Register("GET "+prefix+"/audit-logs/error-groups/{id}/events", admin(http.HandlerFunc(d.handleListAuditErrorGroupEvents)))
	k.Register("GET "+prefix+"/audit-logs/{id}", admin(http.HandlerFunc(d.handleGetAuditLogDetail)))
	// Runtime-logs family (runtime-logs.routes.ts); grep endpoints stay on the
	// Node file scanner and are not part of this dataset slice.
	k.Register("GET "+prefix+"/runtime-logs", admin(http.HandlerFunc(d.handleListRuntimeLogs)))
	k.Register("GET "+prefix+"/runtime-logs/facets", admin(http.HandlerFunc(d.handleRuntimeLogFacets)))
	k.Register("GET "+prefix+"/runtime-logs/{id}", admin(http.HandlerFunc(d.handleGetRuntimeLogDetail)))
	// Public-api-logs family (public-api-logs.routes.ts).
	k.Register("GET "+prefix+"/public-api-logs", admin(http.HandlerFunc(d.handleListPublicApiLogs)))
	k.Register("GET "+prefix+"/public-api-logs/{id}", admin(http.HandlerFunc(d.handleGetPublicApiLogDetail)))
}

// writes 400 for readParamError and 500 otherwise; returns true when handled.
func readWriteStoreError(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	var param *readParamError
	if errors.As(err, &param) {
		kernel.WriteError(w, http.StatusBadRequest, param.message)
		return true
	}
	kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
	return true
}

// ---------------------------------------------------------------------------
// Audit logs (F3 dataset): /__aisys__/api/audit-logs
// ---------------------------------------------------------------------------

// AuditLogListOptions mirrors AuditLogListOptions in audit-log-f3-types.ts.
// Pointer integers mean "query value provided".
type AuditLogListOptions struct {
	Page              *int
	PageSize          *int
	TraceID           string
	SessionID         string
	SessionClientType string
	ErrorGroupID      string
	Outcome           string
	StatusCode        *int
	Path              string
	Model             string
	SystemAccountID   string
	APIKeyID          string
	GroupID           string
	AccountID         string
	ClientIP          string
	StartAt           string
	EndAt             string
	TrafficSource     string
}

// AuditErrorGroupListOptions mirrors AuditErrorGroupListOptions.
type AuditErrorGroupListOptions struct {
	Page            *int
	PageSize        *int
	Path            string
	Model           string
	StatusCode      *int
	SystemAccountID string
	APIKeyID        string
	GroupID         string
	AccountID       string
}

// AuditLogListItem mirrors AuditLogListItem (audit-log-f3-types.ts) with the
// auditLogListSelectColumns projection; *Name joins stay unset because the
// Node repository never resolves them.
type AuditLogListItem struct {
	ID                  string `json:"id"`
	TraceID             string `json:"traceId"`
	SessionID           string `json:"sessionId,omitempty"`
	SessionClientType   string `json:"sessionClientType,omitempty"`
	TrafficSource       string `json:"trafficSource"`
	SystemAccountID     string `json:"systemAccountId,omitempty"`
	APIKeyID            string `json:"apiKeyId,omitempty"`
	GroupID             string `json:"groupId,omitempty"`
	AccountID           string `json:"accountId,omitempty"`
	Method              string `json:"method"`
	Path                string `json:"path"`
	Model               string `json:"model,omitempty"`
	UpstreamModel       string `json:"upstreamModel,omitempty"`
	ModelMappingApplied bool   `json:"modelMappingApplied"`
	Stream              bool   `json:"stream"`
	AuditOutcome        string `json:"auditOutcome"`
	Success             bool   `json:"success"`
	FinalStatusCode     *int64 `json:"finalStatusCode,omitempty"`
	LifecycleStatus     string `json:"lifecycleStatus"`
	DurationMS          *int64 `json:"durationMs,omitempty"`
	HTTPDurationMS      *int64 `json:"httpDurationMs,omitempty"`
	CreatedAt           string `json:"createdAt"`
}

// AuditLogAttemptSummary mirrors AuditLogAttemptSummary.
type AuditLogAttemptSummary struct {
	ID                          string `json:"id"`
	AttemptIndex                int64  `json:"attemptIndex"`
	AccountID                   string `json:"accountId,omitempty"`
	AccountOwnerSystemAccountID string `json:"accountOwnerSystemAccountId,omitempty"`
	GroupID                     string `json:"groupId,omitempty"`
	ProxyURL                    string `json:"proxyUrl,omitempty"`
	ProviderCode                string `json:"providerCode,omitempty"`
	Model                       string `json:"model,omitempty"`
	UpstreamModel               string `json:"upstreamModel,omitempty"`
	PricingModel                string `json:"pricingModel,omitempty"`
	ModelMappingApplied         bool   `json:"modelMappingApplied"`
	ModelMappingSource          string `json:"modelMappingSource,omitempty"`
	SourceEndpointFamily        string `json:"sourceEndpointFamily,omitempty"`
	UpstreamEndpointFamily      string `json:"upstreamEndpointFamily,omitempty"`
	UpstreamMethod              string `json:"upstreamMethod"`
	UpstreamURL                 string `json:"upstreamUrl"`
	UpstreamStatusCode          *int64 `json:"upstreamStatusCode,omitempty"`
	Success                     bool   `json:"success"`
	ErrorPhase                  string `json:"errorPhase,omitempty"`
	ErrorCode                   string `json:"errorCode,omitempty"`
	ErrorMessage                string `json:"errorMessage,omitempty"`
	StartedAt                   string `json:"startedAt"`
	EndedAt                     string `json:"endedAt,omitempty"`
	DurationMS                  *int64 `json:"durationMs,omitempty"`
}

// AuditLogPayloadSummary mirrors AuditLogPayloadSummary (metadata only; body
// windows live behind the payload-detail route which reads blob files).
type AuditLogPayloadSummary struct {
	ID                  string `json:"id"`
	AttemptID           string `json:"attemptId,omitempty"`
	PartType            string `json:"partType"`
	SequenceIndex       int64  `json:"sequenceIndex"`
	ContentType         string `json:"contentType,omitempty"`
	ContentEncoding     string `json:"contentEncoding,omitempty"`
	HeadersSha256       string `json:"headersSha256,omitempty"`
	BodySha256          string `json:"bodySha256,omitempty"`
	SizeBytes           int64  `json:"sizeBytes"`
	CompressedSizeBytes int64  `json:"compressedSizeBytes"`
	CaptureStatus       string `json:"captureStatus"`
	DropReason          string `json:"dropReason,omitempty"`
	CreatedAt           string `json:"createdAt"`
	HasHeaders          bool   `json:"hasHeaders"`
	HasBody             bool   `json:"hasBody"`
}

// AuditErrorGroupSummary mirrors AuditErrorGroupSummary.
type AuditErrorGroupSummary struct {
	ID                 string `json:"id"`
	Fingerprint        string `json:"fingerprint"`
	WindowStartedAt    string `json:"windowStartedAt"`
	WindowEndedAt      string `json:"windowEndedAt"`
	SystemAccountID    string `json:"systemAccountId,omitempty"`
	APIKeyID           string `json:"apiKeyId,omitempty"`
	GroupID            string `json:"groupId,omitempty"`
	AccountID          string `json:"accountId,omitempty"`
	ProviderCode       string `json:"providerCode,omitempty"`
	Path               string `json:"path,omitempty"`
	Model              string `json:"model,omitempty"`
	StatusCode         *int64 `json:"statusCode,omitempty"`
	ErrorPhase         string `json:"errorPhase,omitempty"`
	ErrorCode          string `json:"errorCode,omitempty"`
	ErrorType          string `json:"errorType,omitempty"`
	RequestFingerprint string `json:"requestFingerprint,omitempty"`
	ErrorFingerprint   string `json:"errorFingerprint,omitempty"`
	Count              int64  `json:"count"`
	FirstEventID       string `json:"firstEventId,omitempty"`
	LastEventID        string `json:"lastEventId,omitempty"`
	SampleEventID      string `json:"sampleEventId,omitempty"`
	LastMessage        string `json:"lastMessage,omitempty"`
	CreatedAt          string `json:"createdAt"`
	UpdatedAt          string `json:"updatedAt"`
}

// AuditLogDetail mirrors AuditLogDetail.
type AuditLogDetail struct {
	AuditLogListItem
	ConversationKey        string                   `json:"conversationKey,omitempty"`
	QueryString            string                   `json:"queryString,omitempty"`
	ErrorMessage           string                   `json:"errorMessage,omitempty"`
	SampleBucket           int64                    `json:"sampleBucket"`
	SampleReason           string                   `json:"sampleReason"`
	AttemptCount           int64                    `json:"attemptCount"`
	PayloadCount           int64                    `json:"payloadCount"`
	RawPayloadBytes        int64                    `json:"rawPayloadBytes"`
	CompressedPayloadBytes int64                    `json:"compressedPayloadBytes"`
	CompressionSavedBytes  int64                    `json:"compressionSavedBytes"`
	ErrorGroupID           string                   `json:"errorGroupId,omitempty"`
	CaptureStatus          string                   `json:"captureStatus"`
	StartedAt              string                   `json:"startedAt"`
	EndedAt                string                   `json:"endedAt"`
	HTTPCompletedAt        string                   `json:"httpCompletedAt,omitempty"`
	FirstTokenMS           *int64                   `json:"firstTokenMs,omitempty"`
	Attempts               []AuditLogAttemptSummary `json:"attempts"`
	ErrorGroup             *AuditErrorGroupSummary  `json:"errorGroup,omitempty"`
	Payloads               []AuditLogPayloadSummary `json:"payloads"`
}

// AuditLogRuntime mirrors AuditLogF3Runtime; the route adds available:true.
type AuditLogRuntime struct {
	Mode        string `json:"mode"`
	ReadOnly    bool   `json:"readOnly"`
	QueryOnly   bool   `json:"queryOnly"`
	SchemaReady bool   `json:"schemaReady"`
}

// ReadPage is the shared pagination envelope {items, total, hasMore, page,
// pageSize} used by every Node list repository in this slice.
type ReadPage[T any] struct {
	Items    []T   `json:"items"`
	Total    int64 `json:"total"`
	HasMore  bool  `json:"hasMore"`
	Page     int   `json:"page"`
	PageSize int   `json:"pageSize"`
}

// AuditLogReader is the read-only audit dataset port.
type AuditLogReader interface {
	ListAuditLogs(ctx context.Context, options AuditLogListOptions) (ReadPage[AuditLogListItem], error)
	ListAuditErrorGroups(ctx context.Context, options AuditErrorGroupListOptions) (ReadPage[AuditErrorGroupSummary], error)
	GetAuditLogDetail(ctx context.Context, id string) (*AuditLogDetail, error)
	Runtime() AuditLogRuntime
}

// auditLogNonPersistedTrafficSources mirrors persistedAuditTrafficSourceParams:
// internal probe traffic is never returned by the admin list/detail reads.
var auditLogNonPersistedTrafficSources = []string{"account_health_check", "runtime_recovery_probe", "cooldown_retest"}

// auditLogTrafficSources mirrors the route trafficSource validation set.
var auditLogTrafficSources = map[string]bool{
	"gateway": true, "manual_account_test": true, "hybrid_scoring": true, "hybrid_quality_scoring": true,
}

// auditLogOutcomes mirrors the route outcome validation set ('all' included).
var auditLogOutcomes = map[string]bool{
	"all": true, "success": true, "success_after_retry": true, "gateway_succeeded": true,
	"gateway_failed": true, "upstream_failed": true, "stream_failed": true, "downstream_closed": true,
}

const (
	auditLogDefaultPageSize = 100
	auditLogMaxPageSize     = 100
)

// auditLogSQLReader reads the F3 audit dataset published by
// internal/auditlog (F3 writer). It performs SELECTs only.
type auditLogSQLReader struct {
	db   *sql.DB
	mode ReadDBMode
}

// NewAuditLogSQLReader opens the read-only audit dataset adapter. The caller
// owns the database handle and the schema (F3 EnsureSchema).
func NewAuditLogSQLReader(db *sql.DB, mode ReadDBMode) (AuditLogReader, error) {
	if db == nil {
		return nil, errors.New("audit 数据集数据库句柄必填")
	}
	dialect, err := parseReadDBMode(mode)
	if err != nil {
		return nil, err
	}
	return &auditLogSQLReader{db: db, mode: dialect}, nil
}

func (s *auditLogSQLReader) Runtime() AuditLogRuntime {
	return AuditLogRuntime{Mode: string(s.mode), ReadOnly: true, QueryOnly: true, SchemaReady: true}
}

const auditLogListSelectColumns = `al.id, al.trace_id, al.session_id, al.session_client_type, al.traffic_source, ` +
	`al.system_account_id, al.api_key_id, al.group_id, al.account_id, al.method, al.path, al.model, ` +
	`al.upstream_model, al.model_mapping_applied, al.stream, al.audit_outcome, al.success, al.lifecycle_status, ` +
	`al.final_status_code, al.duration_ms, al.http_duration_ms, al.created_at`

const auditErrorGroupSelectColumns = `aeg.id, aeg.fingerprint, aeg.window_started_at, aeg.window_ended_at, ` +
	`aeg.system_account_id, aeg.api_key_id, aeg.group_id, aeg.account_id, aeg.provider_code, aeg.path, aeg.model, ` +
	`aeg.status_code, aeg.error_phase, aeg.error_code, aeg.error_type, aeg.request_fingerprint, aeg.error_fingerprint, ` +
	`aeg.count, aeg.first_event_id, aeg.last_event_id, aeg.sample_event_id, aeg.last_message, aeg.created_at, aeg.updated_at`

// persistedTrafficClause mirrors persistedTrafficClause (audit-log-f3-query-helpers).
func (s *auditLogSQLReader) persistedTrafficClause(alias string) string {
	return alias + `.traffic_source NOT IN (?, ?, ?)`
}

// buildFilters mirrors auditLogFilters; callers append LIMIT/OFFSET params.
func (s *auditLogSQLReader) buildFilters(options AuditLogListOptions) (string, []any) {
	clauses := []string{s.persistedTrafficClause("al")}
	params := []any{
		auditLogNonPersistedTrafficSources[0],
		auditLogNonPersistedTrafficSources[1],
		auditLogNonPersistedTrafficSources[2],
	}
	appendPrefix := func(column, value string) {
		text := strings.TrimSpace(value)
		if text == "" {
			return
		}
		expression := s.mode.prefixColumn(column)
		clauses = append(clauses, expression+" >= ? AND "+expression+" < ?")
		params = append(params, text, readAuditPrefixUpperBound(text))
	}
	appendExact := func(column, value string) {
		if text := strings.TrimSpace(value); text != "" {
			clauses = append(clauses, column+" = ?")
			params = append(params, text)
		}
	}
	appendPrefix("al.trace_id", options.TraceID)
	appendExact("al.session_id", options.SessionID)
	appendExact("al.session_client_type", options.SessionClientType)
	appendAuditPathFilter(&clauses, &params, "al.path", options.Path)
	appendExact("al.model", options.Model)
	appendPrefix("al.client_ip", options.ClientIP)
	if options.Outcome != "" && options.Outcome != "all" {
		clauses = append(clauses, "al.audit_outcome = ?")
		params = append(params, options.Outcome)
	}
	if options.StatusCode != nil {
		clauses = append(clauses, "al.final_status_code = ?")
		params = append(params, *options.StatusCode)
	}
	if options.TrafficSource != "" {
		clauses = append(clauses, "al.traffic_source = ?")
		params = append(params, options.TrafficSource)
	}
	if options.StartAt != "" {
		clauses = append(clauses, "al.created_at >= ?")
		params = append(params, s.mode.timeParam(options.StartAt))
	}
	if options.EndAt != "" {
		clauses = append(clauses, "al.created_at <= ?")
		params = append(params, s.mode.timeParam(options.EndAt))
	}
	for _, filter := range []struct{ column, value string }{
		{"al.system_account_id", options.SystemAccountID},
		{"al.api_key_id", options.APIKeyID},
		{"al.group_id", options.GroupID},
		{"al.account_id", options.AccountID},
		{"al.error_group_id", options.ErrorGroupID},
	} {
		appendExact(filter.column, filter.value)
	}
	return "WHERE " + strings.Join(clauses, " AND "), params
}

// readNormalizeAuditPathFilter mirrors the shared path() filter: drop a
// leading HTTP method and any query string.
func readNormalizeAuditPathFilter(value string) string {
	text := strings.TrimSpace(value)
	if text == "" {
		return ""
	}
	text = strings.TrimSpace(text)
	for _, method := range []string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"} {
		prefix := method + " "
		if len(text) >= len(prefix) && strings.EqualFold(text[:len(prefix)], prefix) {
			text = strings.TrimSpace(text[len(prefix):])
			break
		}
	}
	if index := strings.IndexByte(text, '?'); index >= 0 {
		text = strings.TrimSpace(text[:index])
	}
	return text
}

func appendAuditPathFilter(clauses *[]string, params *[]any, column, value string) {
	normalized := readNormalizeAuditPathFilter(value)
	if normalized == "" {
		return
	}
	*clauses = append(*clauses, column+" = ?")
	*params = append(*params, normalized)
}

func (s *auditLogSQLReader) ListAuditLogs(ctx context.Context, options AuditLogListOptions) (ReadPage[AuditLogListItem], error) {
	pageSize := readNormalizePageSize(options.PageSize, auditLogDefaultPageSize, auditLogMaxPageSize)
	page := readNormalizeAuditLogPage(options.Page, pageSize, options.SessionID)
	offset := (page - 1) * pageSize
	clause, params := s.buildFilters(options)
	query := `SELECT ` + auditLogListSelectColumns + ` FROM ` + s.mode.table("audit_logs") + ` al ` + clause +
		` ORDER BY al.created_at DESC, al.id DESC LIMIT ? OFFSET ?`
	rows, err := readQueryMaps(ctx, s.db, s.mode, query, append(params, pageSize+1, offset)...)
	if err != nil {
		return ReadPage[AuditLogListItem]{}, err
	}
	pageRows, hasMore := readPageRows(rows, pageSize)
	items := make([]AuditLogListItem, 0, len(pageRows))
	for _, row := range pageRows {
		items = append(items, auditLogListItemFromRow(row))
	}
	return ReadPage[AuditLogListItem]{
		Items: items, Total: readPagedTotal(page, pageSize, len(items), hasMore),
		HasMore: hasMore, Page: page, PageSize: pageSize,
	}, nil
}

func (s *auditLogSQLReader) ListAuditErrorGroups(ctx context.Context, options AuditErrorGroupListOptions) (ReadPage[AuditErrorGroupSummary], error) {
	pageSize := readNormalizePageSize(options.PageSize, auditLogDefaultPageSize, auditLogMaxPageSize)
	page := readNormalizePage(options.Page, pageSize)
	offset := (page - 1) * pageSize
	var clauses []string
	var params []any
	if normalized := readNormalizeAuditPathFilter(options.Path); normalized != "" {
		clauses = append(clauses, "aeg.path = ?")
		params = append(params, normalized)
	}
	if model := strings.TrimSpace(options.Model); model != "" {
		clauses = append(clauses, "aeg.model = ?")
		params = append(params, model)
	}
	if options.StatusCode != nil {
		clauses = append(clauses, "aeg.status_code = ?")
		params = append(params, *options.StatusCode)
	}
	for _, filter := range []struct{ column, value string }{
		{"aeg.system_account_id", options.SystemAccountID},
		{"aeg.api_key_id", options.APIKeyID},
		{"aeg.group_id", options.GroupID},
		{"aeg.account_id", options.AccountID},
	} {
		if text := strings.TrimSpace(filter.value); text != "" {
			clauses = append(clauses, filter.column+" = ?")
			params = append(params, text)
		}
	}
	clause := ""
	if len(clauses) > 0 {
		clause = "WHERE " + strings.Join(clauses, " AND ")
	}
	query := `SELECT ` + auditErrorGroupSelectColumns + ` FROM ` + s.mode.table("audit_error_groups") + ` aeg ` + clause +
		` ORDER BY aeg.updated_at DESC, aeg.id DESC LIMIT ? OFFSET ?`
	rows, err := readQueryMaps(ctx, s.db, s.mode, query, append(params, pageSize+1, offset)...)
	if err != nil {
		return ReadPage[AuditErrorGroupSummary]{}, err
	}
	pageRows, hasMore := readPageRows(rows, pageSize)
	items := make([]AuditErrorGroupSummary, 0, len(pageRows))
	for _, row := range pageRows {
		items = append(items, auditErrorGroupFromRow(row))
	}
	return ReadPage[AuditErrorGroupSummary]{
		Items: items, Total: readPagedTotal(page, pageSize, len(items), hasMore),
		HasMore: hasMore, Page: page, PageSize: pageSize,
	}, nil
}

func (s *auditLogSQLReader) GetAuditLogDetail(ctx context.Context, id string) (*AuditLogDetail, error) {
	logID := strings.TrimSpace(id)
	if logID == "" {
		return nil, nil
	}
	query := `SELECT al.* FROM ` + s.mode.table("audit_logs") + ` al WHERE al.id = ? AND ` + s.persistedTrafficClause("al")
	row, err := readQueryOneMap(ctx, s.db, s.mode, query, append([]any{logID},
		auditLogNonPersistedTrafficSources[0], auditLogNonPersistedTrafficSources[1], auditLogNonPersistedTrafficSources[2])...)
	if err != nil || row == nil {
		return nil, err
	}
	attempts, err := s.listAttempts(ctx, logID)
	if err != nil {
		return nil, err
	}
	payloads, err := s.listPayloads(ctx, logID)
	if err != nil {
		return nil, err
	}
	detail := &AuditLogDetail{
		AuditLogListItem: auditLogListItemFromRow(row),
		ConversationKey:  readOptionalText(row["conversation_key"]),
		QueryString:      readOptionalText(row["query_string"]),
		ErrorMessage:     readOptionalText(row["error_message"]),
		SampleBucket:     readNumberOr(row["sample_bucket"], 0),
		SampleReason:     readRawString(row["sample_reason"]),
		// Node Number(row.attempt_count ?? attempts.length): the columns are
		// NOT NULL, so 0 stays 0; the fallback only covers NULL scans.
		AttemptCount:    readNumberOr(row["attempt_count"], int64(len(attempts))),
		PayloadCount:    readNumberOr(row["payload_count"], int64(len(payloads))),
		ErrorGroupID:    readOptionalText(row["error_group_id"]),
		CaptureStatus:   readRawString(row["capture_status"]),
		StartedAt:       readRawString(row["started_at"]),
		EndedAt:         readRawString(row["ended_at"]),
		HTTPCompletedAt: readOptionalText(row["http_completed_at"]),
		FirstTokenMS:    readOptionalNumber(row["first_token_ms"]),
		Attempts:        attempts,
		Payloads:        payloads,
	}
	raw := readNumberOr(row["raw_payload_bytes"], 0)
	compressed := readNumberOr(row["compressed_payload_bytes"], raw)
	detail.RawPayloadBytes = raw
	detail.CompressedPayloadBytes = compressed
	detail.CompressionSavedBytes = readNumberOr(row["compression_saved_bytes"], max(int64(0), raw-compressed))
	if detail.ErrorGroupID != "" {
		group, err := s.findErrorGroup(ctx, detail.ErrorGroupID)
		if err != nil {
			return nil, err
		}
		detail.ErrorGroup = group
	}
	return detail, nil
}

func (s *auditLogSQLReader) listAttempts(ctx context.Context, auditLogID string) ([]AuditLogAttemptSummary, error) {
	query := `SELECT * FROM ` + s.mode.table("audit_log_attempts") + ` WHERE audit_log_id = ? ORDER BY attempt_index ASC, id ASC`
	rows, err := readQueryMaps(ctx, s.db, s.mode, query, auditLogID)
	if err != nil {
		return nil, err
	}
	attempts := make([]AuditLogAttemptSummary, 0, len(rows))
	for _, row := range rows {
		attempts = append(attempts, AuditLogAttemptSummary{
			ID:                          readRawString(row["id"]),
			AttemptIndex:                readNumberOr(row["attempt_index"], 0),
			AccountID:                   readOptionalText(row["account_id"]),
			AccountOwnerSystemAccountID: readOptionalText(row["account_owner_system_account_id"]),
			GroupID:                     readOptionalText(row["group_id"]),
			ProxyURL:                    readOptionalText(row["proxy_url"]),
			ProviderCode:                readOptionalText(row["provider_code"]),
			Model:                       readOptionalText(row["attempt_model"]),
			UpstreamModel:               readOptionalText(row["attempt_upstream_model"]),
			PricingModel:                readOptionalText(row["attempt_pricing_model"]),
			ModelMappingApplied:         readBool(row["attempt_model_mapping_applied"]),
			ModelMappingSource:          readOptionalText(row["attempt_model_mapping_source"]),
			SourceEndpointFamily:        readOptionalText(row["attempt_source_endpoint_family"]),
			UpstreamEndpointFamily:      readOptionalText(row["attempt_upstream_endpoint_family"]),
			UpstreamMethod:              readRawString(row["upstream_method"]),
			UpstreamURL:                 readRawString(row["upstream_url"]),
			UpstreamStatusCode:          readOptionalNumber(row["upstream_status_code"]),
			Success:                     readBool(row["success"]),
			ErrorPhase:                  readOptionalText(row["error_phase"]),
			ErrorCode:                   readOptionalText(row["error_code"]),
			ErrorMessage:                readOptionalText(row["error_message"]),
			StartedAt:                   readRawString(row["started_at"]),
			EndedAt:                     readOptionalText(row["ended_at"]),
			DurationMS:                  readOptionalNumber(row["duration_ms"]),
		})
	}
	return attempts, nil
}

func (s *auditLogSQLReader) listPayloads(ctx context.Context, auditLogID string) ([]AuditLogPayloadSummary, error) {
	query := `SELECT * FROM ` + s.mode.table("audit_payload_refs") + ` WHERE audit_log_id = ? ORDER BY sequence_index ASC, id ASC`
	rows, err := readQueryMaps(ctx, s.db, s.mode, query, auditLogID)
	if err != nil {
		return nil, err
	}
	payloads := make([]AuditLogPayloadSummary, 0, len(rows))
	for _, row := range rows {
		size := readNumberOr(row["raw_size_bytes"], 0)
		payloads = append(payloads, AuditLogPayloadSummary{
			ID:                  readRawString(row["id"]),
			AttemptID:           readOptionalText(row["attempt_id"]),
			PartType:            readRawString(row["part_type"]),
			SequenceIndex:       readNumberOr(row["sequence_index"], 0),
			ContentType:         readOptionalText(row["content_type"]),
			ContentEncoding:     readOptionalText(row["content_encoding"]),
			HeadersSha256:       readOptionalText(row["headers_sha256"]),
			BodySha256:          readOptionalText(row["body_sha256"]),
			SizeBytes:           size,
			CompressedSizeBytes: readNumberOr(row["compressed_size_bytes"], size),
			CaptureStatus:       readRawString(row["capture_status"]),
			DropReason:          readOptionalText(row["drop_reason"]),
			CreatedAt:           readRawString(row["created_at"]),
			HasHeaders:          readOptionalText(row["headers_blob_id"]) != "",
			HasBody:             readOptionalText(row["body_blob_id"]) != "",
		})
	}
	return payloads, nil
}

func (s *auditLogSQLReader) findErrorGroup(ctx context.Context, id string) (*AuditErrorGroupSummary, error) {
	query := `SELECT ` + auditErrorGroupSelectColumns + ` FROM ` + s.mode.table("audit_error_groups") + ` aeg WHERE aeg.id = ?`
	row, err := readQueryOneMap(ctx, s.db, s.mode, query, id)
	if err != nil || row == nil {
		return nil, err
	}
	group := auditErrorGroupFromRow(row)
	return &group, nil
}

func auditLogListItemFromRow(row map[string]any) AuditLogListItem {
	lifecycle := readOptionalText(row["lifecycle_status"])
	if lifecycle == "" {
		lifecycle = "finalized"
	}
	return AuditLogListItem{
		ID:                  readRawString(row["id"]),
		TraceID:             readRawString(row["trace_id"]),
		SessionID:           readOptionalText(row["session_id"]),
		SessionClientType:   readOptionalText(row["session_client_type"]),
		TrafficSource:       readRawString(row["traffic_source"]),
		SystemAccountID:     readOptionalText(row["system_account_id"]),
		APIKeyID:            readOptionalText(row["api_key_id"]),
		GroupID:             readOptionalText(row["group_id"]),
		AccountID:           readOptionalText(row["account_id"]),
		Method:              readRawString(row["method"]),
		Path:                readRawString(row["path"]),
		Model:               readOptionalText(row["model"]),
		UpstreamModel:       readOptionalText(row["upstream_model"]),
		ModelMappingApplied: readBool(row["model_mapping_applied"]),
		Stream:              readBool(row["stream"]),
		AuditOutcome:        readRawString(row["audit_outcome"]),
		Success:             readBool(row["success"]),
		FinalStatusCode:     readOptionalNumber(row["final_status_code"]),
		LifecycleStatus:     lifecycle,
		DurationMS:          readOptionalNumber(row["duration_ms"]),
		HTTPDurationMS:      readOptionalNumber(row["http_duration_ms"]),
		CreatedAt:           readRawString(row["created_at"]),
	}
}

func auditErrorGroupFromRow(row map[string]any) AuditErrorGroupSummary {
	return AuditErrorGroupSummary{
		ID:                 readRawString(row["id"]),
		Fingerprint:        readRawString(row["fingerprint"]),
		WindowStartedAt:    readRawString(row["window_started_at"]),
		WindowEndedAt:      readRawString(row["window_ended_at"]),
		SystemAccountID:    readOptionalText(row["system_account_id"]),
		APIKeyID:           readOptionalText(row["api_key_id"]),
		GroupID:            readOptionalText(row["group_id"]),
		AccountID:          readOptionalText(row["account_id"]),
		ProviderCode:       readOptionalText(row["provider_code"]),
		Path:               readOptionalText(row["path"]),
		Model:              readOptionalText(row["model"]),
		StatusCode:         readOptionalNumber(row["status_code"]),
		ErrorPhase:         readOptionalText(row["error_phase"]),
		ErrorCode:          readOptionalText(row["error_code"]),
		ErrorType:          readOptionalText(row["error_type"]),
		RequestFingerprint: readOptionalText(row["request_fingerprint"]),
		ErrorFingerprint:   readOptionalText(row["error_fingerprint"]),
		Count:              readNumberOr(row["count"], 0),
		FirstEventID:       readOptionalText(row["first_event_id"]),
		LastEventID:        readOptionalText(row["last_event_id"]),
		SampleEventID:      readOptionalText(row["sample_event_id"]),
		LastMessage:        readOptionalText(row["last_message"]),
		CreatedAt:          readRawString(row["created_at"]),
		UpdatedAt:          readRawString(row["updated_at"]),
	}
}

// parseAuditLogListOptions mirrors parseAuditLogListOptions in
// audit-logs.routes.ts, including the strict time range and the 400 on an
// unknown trafficSource.
func parseAuditLogListOptions(r *http.Request) (AuditLogListOptions, error) {
	startAt, endAt, err := readQueryDateTimeRange(r)
	if err != nil {
		return AuditLogListOptions{}, err
	}
	options := AuditLogListOptions{
		Page:              readQueryInt(r, "page"),
		PageSize:          readQueryInt(r, "pageSize"),
		TraceID:           readQueryText(r, "traceId"),
		SessionID:         readQueryText(r, "sessionId"),
		SessionClientType: readQueryText(r, "sessionClientType"),
		ErrorGroupID:      readQueryText(r, "errorGroupId"),
		Outcome:           readQueryText(r, "outcome"),
		StatusCode:        readQueryStatusCode(r, "statusCode"),
		Path:              readQueryText(r, "path"),
		Model:             readQueryText(r, "model"),
		SystemAccountID:   readQueryText(r, "systemAccountId"),
		APIKeyID:          readQueryText(r, "apiKeyId"),
		GroupID:           readQueryText(r, "groupId"),
		AccountID:         readQueryText(r, "accountId"),
		ClientIP:          readQueryText(r, "clientIp"),
		StartAt:           startAt,
		EndAt:             endAt,
		TrafficSource:     readQueryText(r, "trafficSource"),
	}
	if !auditLogOutcomes[options.Outcome] {
		options.Outcome = ""
	}
	if options.TrafficSource != "" && !auditLogTrafficSources[options.TrafficSource] {
		return AuditLogListOptions{}, readParamErrorf("审计日志来源筛选无效，仅支持网关请求、AI 账户测试、混合路由选型或回答质量复核")
	}
	return options, nil
}

func (d *ReadsDeps) handleListAuditLogs(w http.ResponseWriter, r *http.Request) {
	options, err := parseAuditLogListOptions(r)
	if readWriteStoreError(w, err) {
		return
	}
	result, err := d.Audit.ListAuditLogs(r.Context(), options)
	if readWriteStoreError(w, err) {
		return
	}
	kernel.WriteOK(w, result, "")
}

func (d *ReadsDeps) handleAuditLogRuntime(w http.ResponseWriter, r *http.Request) {
	runtime := d.Audit.Runtime()
	kernel.WriteOK(w, struct {
		AuditLogRuntime
		Available bool `json:"available"`
	}{runtime, true}, "")
}

func (d *ReadsDeps) handleListAuditErrorGroups(w http.ResponseWriter, r *http.Request) {
	options := AuditErrorGroupListOptions{
		Page:            readQueryInt(r, "page"),
		PageSize:        readQueryInt(r, "pageSize"),
		Path:            readQueryText(r, "path"),
		Model:           readQueryText(r, "model"),
		StatusCode:      readQueryStatusCode(r, "statusCode"),
		SystemAccountID: readQueryText(r, "systemAccountId"),
		APIKeyID:        readQueryText(r, "apiKeyId"),
		GroupID:         readQueryText(r, "groupId"),
		AccountID:       readQueryText(r, "accountId"),
	}
	result, err := d.Audit.ListAuditErrorGroups(r.Context(), options)
	if readWriteStoreError(w, err) {
		return
	}
	kernel.WriteOK(w, result, "")
}

func (d *ReadsDeps) handleListAuditErrorGroupEvents(w http.ResponseWriter, r *http.Request) {
	options, err := parseAuditLogListOptions(r)
	if readWriteStoreError(w, err) {
		return
	}
	options.ErrorGroupID = strings.TrimSpace(r.PathValue("id"))
	result, err := d.Audit.ListAuditLogs(r.Context(), options)
	if readWriteStoreError(w, err) {
		return
	}
	kernel.WriteOK(w, result, "")
}

func (d *ReadsDeps) handleGetAuditLogDetail(w http.ResponseWriter, r *http.Request) {
	detail, err := d.Audit.GetAuditLogDetail(r.Context(), r.PathValue("id"))
	if readWriteStoreError(w, err) {
		return
	}
	if detail == nil {
		kernel.WriteNotFound(w, "审计日志不存在")
		return
	}
	kernel.WriteOK(w, detail, "")
}
