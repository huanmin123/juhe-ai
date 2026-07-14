package httpapi

import (
	"context"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementruntimelogs"
)

const managementRuntimeLogRequestTimeout = 120 * time.Second

type managementRuntimeLogService interface {
	List(r *http.Request, input managementruntimelogs.ListInput) (managementruntimelogs.ListResult, error)
	Detail(r *http.Request, id string) (managementruntimelogs.Detail, bool, error)
}

type managementRuntimeLogServiceAdapter struct {
	service *managementruntimelogs.Service
}

func (s managementRuntimeLogServiceAdapter) List(r *http.Request, input managementruntimelogs.ListInput) (managementruntimelogs.ListResult, error) {
	return s.service.List(r.Context(), input)
}

func (s managementRuntimeLogServiceAdapter) Detail(r *http.Request, id string) (managementruntimelogs.Detail, bool, error) {
	return s.service.Detail(r.Context(), id)
}

type managementRuntimeLogListResponse struct {
	Items                         []managementruntimelogs.Summary `json:"items"`
	Total                         int                             `json:"total"`
	HasMore                       bool                            `json:"hasMore"`
	Page                          int                             `json:"page"`
	PageSize                      int                             `json:"pageSize"`
	ElapsedMS                     int64                           `json:"elapsedMs"`
	RetentionDays                 *int                            `json:"retentionDays"`
	RetentionDaysSource           string                          `json:"retentionDaysSource"`
	RuntimeAvailable              bool                            `json:"runtimeAvailable"`
	WorkerSnapshotAvailable       bool                            `json:"workerSnapshotAvailable"`
	RuntimeLogIndexQueueAvailable bool                            `json:"runtimeLogIndexQueueAvailable"`
}

func NewManagementRuntimeLogsHandler(service *managementruntimelogs.Service) http.Handler {
	return newManagementRuntimeLogsHandler(managementRuntimeLogServiceAdapter{service: service}, time.Now)
}

func newManagementRuntimeLogsHandler(service managementRuntimeLogService, now func() time.Time) http.Handler {
	if now == nil {
		now = time.Now
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authContext, ok := ManagementAuthContextFromRequest(r)
		if !ok || strings.TrimSpace(authContext.SystemAccountID) == "" {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		if !managementauth.IsAdminRole(authContext.Role) {
			writeMessageError(w, http.StatusForbidden, "需要管理员权限")
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), managementRuntimeLogRequestTimeout)
		defer cancel()
		r = r.WithContext(ctx)

		rawID := chi.URLParam(r, "id")
		if rawID != "" {
			if rawID == "facets" || rawID == "grep" {
				writeError(w, http.StatusNotFound, "接口不存在")
				return
			}
			id := strings.TrimFunc(rawID, managementGroupListECMAScriptWhitespace)
			if id == "" {
				writeMessageError(w, http.StatusNotFound, "运行日志不存在")
				return
			}
			detail, found, err := service.Detail(r, id)
			if err != nil {
				writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
				return
			}
			if !found {
				writeMessageError(w, http.StatusNotFound, "运行日志不存在")
				return
			}
			writeData(w, http.StatusOK, detail)
			return
		}

		startedAt := now()
		result, err := service.List(r, parseManagementRuntimeLogListQuery(r.URL.Query()))
		if err != nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		elapsedMS := int64(math.Round(float64(now().Sub(startedAt)) / float64(time.Millisecond)))
		if elapsedMS < 0 {
			elapsedMS = 0
		}
		writeData(w, http.StatusOK, managementRuntimeLogListResponse{
			Items:                         result.Items,
			Total:                         result.Total,
			HasMore:                       result.HasMore,
			Page:                          result.Page,
			PageSize:                      result.PageSize,
			ElapsedMS:                     elapsedMS,
			RetentionDays:                 nil,
			RetentionDaysSource:           "unavailable",
			RuntimeAvailable:              false,
			WorkerSnapshotAvailable:       false,
			RuntimeLogIndexQueueAvailable: false,
		})
	})
}

func parseManagementRuntimeLogListQuery(values url.Values) managementruntimelogs.ListInput {
	page, _ := managementGroupListIntegerQueryValue(values, "page")
	pageSize, pageSizeProvided := managementGroupListIntegerQueryValue(values, "pageSize")
	startAt, endAt := managementRuntimeLogDateTimeRangeQueryValue(
		managementRuntimeLogQueryText(values, "startAt"),
		managementRuntimeLogQueryText(values, "endAt"),
	)
	return managementruntimelogs.ListInput{
		Page:             page,
		PageSize:         pageSize,
		PageSizeProvided: pageSizeProvided,
		TraceID:          managementRuntimeLogQueryText(values, "traceId"),
		Level:            managementRuntimeLogQueryText(values, "level"),
		Event:            managementRuntimeLogQueryText(values, "event"),
		Keyword:          managementRuntimeLogQueryText(values, "keyword"),
		StartAt:          startAt,
		EndAt:            endAt,
	}
}

func managementRuntimeLogDateTimeRangeQueryValue(startText string, endText string) (time.Time, time.Time) {
	startAt := managementRuntimeLogDateTimeQueryValue(startText)
	endAt := managementRuntimeLogDateTimeQueryValue(endText)
	if !startAt.IsZero() && !endAt.IsZero() && startAt.After(endAt) {
		return endAt, startAt
	}
	return startAt, endAt
}

func managementRuntimeLogDateTimeQueryValue(text string) time.Time {
	if text == "" {
		return time.Time{}
	}
	isoText := normalizeRuntimeLogISODateSeparators(text)
	for _, layout := range []string{
		time.RFC3339Nano,
		"2006-01-02T15:04Z07:00",
		"2006-01-02T15:04:05.999999999Z0700",
		"2006-01-02T15:04Z0700",
		"2006-01-02 15:04:05.999999999Z07:00",
		"2006-01-02 15:04Z07:00",
		"2006-01-02 15:04:05.999999999Z0700",
		"2006-01-02 15:04Z0700",
		time.RFC1123Z,
		time.RFC1123,
		time.RFC850,
	} {
		value, err := time.Parse(layout, isoText)
		if err == nil {
			return value.UTC()
		}
	}
	for _, layout := range []string{"2006", "2006-01", time.DateOnly} {
		value, err := time.ParseInLocation(layout, text, time.UTC)
		if err == nil {
			return value.UTC()
		}
	}
	if value, err := time.ParseInLocation(time.ANSIC, text, time.Local); err == nil {
		return value.UTC()
	}
	for _, layout := range []string{
		"2006-01-02T15:04:05.999999999",
		"2006-01-02T15:04",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04",
		"2006/01/02 15:04:05.999999999",
		"2006/01/02 15:04",
	} {
		value, err := time.ParseInLocation(layout, isoText, time.Local)
		if err == nil {
			return value.UTC()
		}
	}
	return time.Time{}
}

func normalizeRuntimeLogISODateSeparators(text string) string {
	bytes := []byte(text)
	if len(bytes) > len("2006-01-02") && bytes[len("2006-01-02")] == 't' {
		bytes[len("2006-01-02")] = 'T'
	}
	if len(bytes) > 0 && bytes[len(bytes)-1] == 'z' {
		bytes[len(bytes)-1] = 'Z'
	}
	return string(bytes)
}

func managementRuntimeLogQueryText(values url.Values, key string) string {
	items := values[key]
	if len(items) == 0 {
		return ""
	}
	return strings.TrimFunc(items[0], managementGroupListECMAScriptWhitespace)
}
