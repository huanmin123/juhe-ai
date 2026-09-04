// Operation-log (F4) read family: the M13 slice mounting the admin
// /operation-logs and self /my-operation-logs routes. It owns only HTTP
// parameter mapping and permission control; storage, visibility SQL and
// pagination semantics stay in the F4 operationlog store, which the Node
// module reaches through the very same methods
// (operation-log-go-input.service.ts). The admin surface mirrors
// operationLogsRouter and the self surface mirrors myOperationLogsRouter.
// The shared package documentation lives in audit_reads.go.
package logreads

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/operationlog"
)

// OperationLogReader is the read half of operationlog.Store. Keeping the
// interface local (j3creadonly pattern) prevents this HTTP slice from
// acquiring the F4 write path or the owner lease.
type OperationLogReader interface {
	List(context.Context, operationlog.ListOptions) (operationlog.ListResult, error)
	Detail(context.Context, string, string) (operationlog.DetailSupplement, bool, error)
}

// Deps bundles the M13 slice collaborators.
type Deps struct {
	Reader OperationLogReader
	Auth   *authsys.Deps
}

// Mount wires the operation-log read route family: the admin surface on
// /operation-logs (requireAdmin, unrestricted visibility) and the self
// surface on /my-operation-logs (caller pinned as the visibility viewer,
// mirroring Node forceSelfAccessScope + context.systemAccountId).
func (d *Deps) Mount(k *kernel.Kernel) {
	prefix := "/__aisys__/api"
	k.Register("GET "+prefix+"/operation-logs", d.Auth.RequireAdmin(d.list(false)))
	k.Register("GET "+prefix+"/operation-logs/{id}", d.Auth.RequireAdmin(d.detail(false)))
	k.Register("GET "+prefix+"/my-operation-logs", d.Auth.RequireSession(true)(d.list(true)))
	k.Register("GET "+prefix+"/my-operation-logs/{id}", d.Auth.RequireSession(true)(d.detail(true)))
}

func (d *Deps) list(self bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		options, badRequest := parseListOptions(r.URL.Query(), !self, time.Now())
		if badRequest != "" {
			kernel.WriteBadRequest(w, badRequest)
			return
		}
		if self {
			options.ViewerID = authsys.AuthContextFrom(r).SystemAccountID
		}
		result, err := d.Reader.List(r.Context(), options)
		if err != nil {
			d.writeReadError(w, err)
			return
		}
		kernel.WriteOK(w, result, "")
	}
}

func (d *Deps) detail(self bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		viewerID := ""
		if self {
			viewerID = authsys.AuthContextFrom(r).SystemAccountID
		}
		supplement, found, err := d.Reader.Detail(r.Context(), r.PathValue("id"), viewerID)
		if err != nil {
			d.writeReadError(w, err)
			return
		}
		if !found {
			kernel.WriteNotFound(w, "操作日志不存在")
			return
		}
		kernel.WriteOK(w, supplement, "")
	}
}

// writeReadError maps store failures onto the route contract. Invalid client
// time ranges are a request fault (the F4 input server answers 400 for the
// same error); everything else stays an opaque internal failure.
func (d *Deps) writeReadError(w http.ResponseWriter, err error) {
	if errors.Is(err, operationlog.ErrInvalidListTime) {
		kernel.WriteBadRequest(w, "时间必须是带 Z 或数值 offset 的 RFC3339 时间")
		return
	}
	kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
}

// parseListOptions mirrors parseOperationLogListOptions: shared filters for
// both surfaces, admin-only account filters, and the management default
// window of 31 calendar days (UTC) that an exact trace ID or an explicit
// start/end bypasses. The second return value is a bad-request message and
// empty when the query is valid.
func parseListOptions(query url.Values, admin bool, now time.Time) (operationlog.ListOptions, string) {
	options := operationlog.ListOptions{
		Page:           integerQueryValue(query.Get("page")),
		PageSize:       integerQueryValue(query.Get("pageSize")),
		SummaryKeyword: optionalQueryText(query.Get("summaryKeyword")),
		Module:         optionalQueryText(query.Get("module")),
		Action:         optionalQueryText(query.Get("action")),
		ResourceType:   optionalQueryText(query.Get("resourceType")),
		ResourceID:     optionalQueryText(query.Get("resourceId")),
		TraceID:        optionalQueryText(query.Get("traceId")),
	}
	startAt, endAt, badRequest := strictDateTimeRange(query.Get("startAt"), query.Get("endAt"))
	if badRequest != "" {
		return operationlog.ListOptions{}, badRequest
	}
	if admin && !exactTraceID(options.TraceID) && startAt == "" && endAt == "" {
		startAt, endAt = defaultManagementWindow(now)
	}
	options.StartAt, options.EndAt = startAt, endAt
	if admin {
		options.ActorSystemAccountID = optionalQueryText(query.Get("actorSystemAccountId"))
		options.AffectedSystemAccountID = optionalQueryText(query.Get("affectedSystemAccountId"))
		options.OperationScopeSystemAccountID = optionalQueryText(query.Get("operationScopeSystemAccountId"))
	}
	return options, ""
}

// strictDateTimeRange mirrors strictDateTimeRangeQueryValue: both bounds must
// carry a Z or numeric offset (a bare datetime is never guessed as local
// time); the store re-canonicalizes and swaps an inverted range.
func strictDateTimeRange(startRaw, endRaw string) (string, string, string) {
	startAt, ok := strictDateTimeQueryValue(startRaw)
	if !ok {
		return "", "", "开始时间必须是带 Z 或数值 offset 的 RFC3339 时间"
	}
	endAt, ok := strictDateTimeQueryValue(endRaw)
	if !ok {
		return "", "", "结束时间必须是带 Z 或数值 offset 的 RFC3339 时间"
	}
	return startAt, endAt, ""
}

func strictDateTimeQueryValue(raw string) (string, bool) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return "", true
	}
	parsed, err := time.Parse(time.RFC3339Nano, text)
	if err != nil {
		return "", false
	}
	return parsed.UTC().Format(time.RFC3339Nano), true
}

// defaultManagementWindow mirrors defaultManagementOperationLogDateRange:
// [today-30d 00:00:00.000Z, today 23:59:59.999Z] in UTC, 31 calendar days.
func defaultManagementWindow(now time.Time) (string, string) {
	day := now.UTC()
	end := time.Date(day.Year(), day.Month(), day.Day(), 23, 59, 59, int(999*time.Millisecond), time.UTC)
	startDay := end.AddDate(0, 0, -(managementDefaultOperationLogWindowDays - 1))
	start := time.Date(startDay.Year(), startDay.Month(), startDay.Day(), 0, 0, 0, 0, time.UTC)
	return start.Format(rfc3339Millis), end.Format(rfc3339Millis)
}

const (
	managementDefaultOperationLogWindowDays = 31
	rfc3339Millis                           = "2006-01-02T15:04:05.000Z07:00"
)

// exactTraceID mirrors isExactTraceId: only a full UUID bypasses the default
// management window, a bare prefix keeps it.
var exactTraceIDPattern = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func exactTraceID(value string) bool {
	return value != "" && exactTraceIDPattern.MatchString(value)
}

func optionalQueryText(raw string) string {
	return strings.TrimSpace(raw)
}

// integerQueryValue mirrors finiteNumberQueryValue for the integer page
// fields: absent or non-integer input falls back to the store defaults
// (page 1, pageSize 20) instead of the legacy RPC round-trip failure.
func integerQueryValue(raw string) int {
	text := strings.TrimSpace(raw)
	if text == "" {
		return 0
	}
	value, err := strconv.Atoi(text)
	if err != nil {
		return 0
	}
	return value
}
