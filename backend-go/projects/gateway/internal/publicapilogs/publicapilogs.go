// Package publicapilogs owns the P4-S20 (F5) vertical slice: public API log
// capture, in-process bounded queuing, asynchronous batch persistence and
// retention cleanup, ported from backend/src/modules/public-api-logs/
// (public-api-log-capture.middleware.ts, public-api-log-queue.service.ts) and
// backend/src/storage/public-api-logs.repository.ts.
//
// Architecture difference approved by the migration plan (S20: 消灭 Redis
// Stream 队列): Node ships logs through a Redis Stream queue (or IPC /
// in-process array queue depending on process role); Go replaces every queue
// hop with one in-process bounded channel feeding a direct async batch writer
// plus retention. Externally observable behavior (record fields, sanitize
// rules, retention semantics, query results through the F5 reader) matches
// Node field for field; the durability difference (records not yet flushed
// are lost on a process crash instead of surviving in Redis) is the approved
// trade. Loss accounting stays observable via Runtime() (DroppedCount /
// FlushFailureCount), mirroring getPublicApiLogQueueRuntime.
//
// Rows land in the dataset table public_api_logs (PostgreSQL schema
// juhe_dataset, unqualified in SQLite). Reads belong to the F5 reader slice
// (internal/logreads) and to the admin; this package only writes and cleans.
package publicapilogs

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"math"
	"strconv"
	"time"
)

// CaptureStatus mirrors PublicApiLogCaptureStatus.
type CaptureStatus string

const (
	CaptureStatusComplete  CaptureStatus = "complete"
	CaptureStatusTruncated CaptureStatus = "truncated"
	CaptureStatusEmpty     CaptureStatus = "empty"
	CaptureStatusDropped   CaptureStatus = "dropped"
)

// Result filters mirror PublicApiLogResultFilter (reader side, kept here for
// the shared vocabulary).
const (
	ResultSuccess = "success"
	ResultFailed  = "failed"
	ResultAll     = "all"
)

// SourceContext mirrors ExternalIntegrationSourceAuthContext (the subset the
// log record consumes: res.locals.externalIntegrationSource).
type SourceContext struct {
	SourceRefID string
	SourceName  string
	TokenID     string
	TokenName   string
	TokenPrefix string
	IsTestToken bool
}

// Input mirrors PublicApiLogInput. Nil strings mean "absent" exactly like the
// Node undefined fields; RequestData / ResponseData carry the bounded capture
// snapshots built by BuildInput.
type Input struct {
	ID                    string
	TraceID               string
	SourceRefID           string
	SourceName            string
	TokenID               string
	TokenName             string
	TokenPrefix           string
	IsTestToken           bool
	Method                string
	Path                  string
	QueryString           string
	ClientIP              string
	UserAgent             string
	StatusCode            any // number or nil
	Success               bool
	DurationMS            any // number or nil
	RequestSizeBytes      int64
	ResponseSizeBytes     int64
	RequestCaptureStatus  CaptureStatus
	ResponseCaptureStatus CaptureStatus
	RequestData           any
	ResponseData          any
	ErrorCode             string
	ErrorMessage          string
	StartedAt             string
	EndedAt               string
	CreatedAt             string
}

// normalized mirrors NormalizedPublicApiLogInput: the exact row shape handed
// to INSERT.
type normalized struct {
	id                    string
	traceID               any
	sourceRefID           any
	sourceName            any
	tokenID               any
	tokenName             any
	tokenPrefix           any
	isTestToken           int
	method                string
	path                  string
	queryString           any
	clientIP              any
	userAgent             any
	statusCode            any
	success               int
	durationMS            any
	requestSizeBytes      int64
	responseSizeBytes     int64
	requestCaptureStatus  string
	responseCaptureStatus string
	requestDataJSON       string
	responseDataJSON      string
	errorCode             any
	errorMessage          any
	startedAt             string
	endedAt               string
	createdAt             string
}

// normalizeInput mirrors normalizePublicApiLogInput: stable id + createdAt
// defaults, integer coercion and '{}' JSON defaults.
func normalizeInput(input Input, now func() time.Time, newID func(string) string) normalized {
	// The queue stamps a stable id before dispatch; direct callers get the
	// repository default here.
	if input.ID == "" {
		input.ID = newID("publog")
	}
	if input.CreatedAt == "" {
		input.CreatedAt = isoMillis(now())
	}
	row := normalized{
		id:                    input.ID,
		isTestToken:           boolToInt(input.IsTestToken),
		method:                input.Method,
		path:                  input.Path,
		success:               boolToInt(input.Success),
		requestSizeBytes:      normalizeNonNegativeInt64(input.RequestSizeBytes),
		responseSizeBytes:     normalizeNonNegativeInt64(input.ResponseSizeBytes),
		requestCaptureStatus:  normalizeCaptureStatus(input.RequestCaptureStatus),
		responseCaptureStatus: normalizeCaptureStatus(input.ResponseCaptureStatus),
		requestDataJSON:       safeJSONObjectStringify(input.RequestData),
		responseDataJSON:      safeJSONObjectStringify(input.ResponseData),
		startedAt:             input.StartedAt,
		endedAt:               input.EndedAt,
		createdAt:             input.CreatedAt,
	}
	row.traceID = nullableString(input.TraceID)
	row.sourceRefID = nullableString(input.SourceRefID)
	row.sourceName = nullableString(input.SourceName)
	row.tokenID = nullableString(input.TokenID)
	row.tokenName = nullableString(input.TokenName)
	row.tokenPrefix = nullableString(input.TokenPrefix)
	row.queryString = nullableString(input.QueryString)
	row.clientIP = nullableString(input.ClientIP)
	row.userAgent = nullableString(input.UserAgent)
	row.statusCode = integerOrNull(input.StatusCode)
	row.durationMS = integerOrNull(input.DurationMS)
	row.errorCode = nullableString(input.ErrorCode)
	row.errorMessage = nullableString(input.ErrorMessage)
	return row
}

// isoMillis mirrors Node nowIso()/toISOString() millisecond precision.
func isoMillis(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z07:00")
}

// newRecordID mirrors Node newId('publog') (`${prefix}_${Date.now()}_${hex8}`).
func newRecordID(prefix string, now func() time.Time) string {
	buf := make([]byte, 4)
	_, _ = rand.Read(buf)
	return prefix + "_" + strconv.FormatInt(now().UnixMilli(), 10) + "_" + hex.EncodeToString(buf)
}

// newQueueID mirrors ensurePublicApiLogQueueId: `publog_${Date.now()}_${uuid}`.
func newQueueID(now func() time.Time) string {
	buf := make([]byte, 16)
	_, _ = rand.Read(buf)
	buf[6] = (buf[6] & 0x0f) | 0x40
	buf[8] = (buf[8] & 0x3f) | 0x80
	hexed := hex.EncodeToString(buf)
	uuid := hexed[0:8] + "-" + hexed[8:12] + "-" + hexed[12:16] + "-" + hexed[16:20] + "-" + hexed[20:32]
	return "publog_" + strconv.FormatInt(now().UnixMilli(), 10) + "_" + uuid
}

func normalizeCaptureStatus(value CaptureStatus) string {
	switch value {
	case CaptureStatusComplete, CaptureStatusTruncated, CaptureStatusEmpty, CaptureStatusDropped:
		return string(value)
	}
	return string(CaptureStatusEmpty)
}

// safeJSONObjectStringify mirrors safeJsonObjectStringify: object values are
// serialized compactly, everything else (including arrays and nil) becomes
// '{}'.
func safeJSONObjectStringify(value any) string {
	switch typed := value.(type) {
	case nil:
		return "{}"
	case *snapshotObject:
		if typed == nil {
			return "{}"
		}
		text, err := typed.MarshalJSON()
		if err != nil {
			return "{}"
		}
		return string(text)
	case snapshotObject:
		text, err := typed.MarshalJSON()
		if err != nil {
			return "{}"
		}
		return string(text)
	case map[string]any:
		if typed == nil {
			return "{}"
		}
		text, err := marshalCompact(typed)
		if err != nil {
			return "{}"
		}
		return text
	}
	return "{}"
}

// integerOrNull mirrors integerOrNull: finite numbers truncate, everything
// else (nil included) is NULL.
func integerOrNull(value any) any {
	switch typed := value.(type) {
	case nil:
		return nil
	case int:
		return int64(typed)
	case int32:
		return int64(typed)
	case int64:
		return typed
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) {
			return nil
		}
		return int64(typed)
	case json.Number:
		if parsed, err := strconv.ParseInt(typed.String(), 10, 64); err == nil {
			return parsed
		}
		if parsed, err := strconv.ParseFloat(typed.String(), 64); err == nil {
			return int64(parsed)
		}
		return nil
	}
	return nil
}

// normalizeNonNegativeInt64 mirrors normalizeNonNegativeInteger.
func normalizeNonNegativeInt64(value int64) int64 {
	if value < 0 {
		return 0
	}
	return value
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
