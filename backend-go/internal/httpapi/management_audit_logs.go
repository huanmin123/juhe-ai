package httpapi

import (
	"context"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"juhe-ai/backend-go/internal/modules/managementauditlogs"
	"juhe-ai/backend-go/internal/modules/managementauth"
)

const managementAuditLogRequestTimeout = 120 * time.Second

type managementAuditLogService interface {
	List(*http.Request, managementauditlogs.ListInput) (managementauditlogs.ListResult, error)
	HotSearch(*http.Request, managementauditlogs.HotSearchInput) (managementauditlogs.HotSearchResult, error)
	ListErrorGroups(*http.Request, managementauditlogs.ErrorGroupListInput) (managementauditlogs.ErrorGroupListResult, error)
	ListErrorGroupEvents(*http.Request, string, managementauditlogs.ListInput) (managementauditlogs.ListResult, error)
	Detail(*http.Request, string) (managementauditlogs.Detail, bool, error)
}
type managementAuditLogServiceAdapter struct{ service *managementauditlogs.Service }

func (a managementAuditLogServiceAdapter) List(r *http.Request, input managementauditlogs.ListInput) (managementauditlogs.ListResult, error) {
	return a.service.List(r.Context(), input)
}
func (a managementAuditLogServiceAdapter) HotSearch(r *http.Request, input managementauditlogs.HotSearchInput) (managementauditlogs.HotSearchResult, error) {
	return a.service.HotSearch(r.Context(), input)
}
func (a managementAuditLogServiceAdapter) Detail(r *http.Request, id string) (managementauditlogs.Detail, bool, error) {
	return a.service.Detail(r.Context(), id)
}
func (a managementAuditLogServiceAdapter) ListErrorGroups(r *http.Request, input managementauditlogs.ErrorGroupListInput) (managementauditlogs.ErrorGroupListResult, error) {
	return a.service.ListErrorGroups(r.Context(), input)
}
func (a managementAuditLogServiceAdapter) ListErrorGroupEvents(r *http.Request, errorGroupID string, input managementauditlogs.ListInput) (managementauditlogs.ListResult, error) {
	return a.service.ListErrorGroupEvents(r.Context(), errorGroupID, input)
}
func NewManagementAuditLogsHandler(service *managementauditlogs.Service) http.Handler {
	if service == nil {
		return newManagementAuditLogsHandler(nil)
	}
	return newManagementAuditLogsHandler(managementAuditLogServiceAdapter{service})
}
func newManagementAuditLogsHandler(service managementAuditLogService) http.Handler {
	readHandler := managementAuditReadHandler{service: service}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		readHandler.handle(w, r, func(service managementAuditLogService, r *http.Request) {
			path := strings.TrimRight(r.URL.Path, "/")
			if chi.URLParam(r, "id") == "" && path == "/__aisys__/api/audit-logs/search-hot" {
				result, err := service.HotSearch(r, parseManagementAuditHotSearchQuery(r.URL.Query()))
				if err != nil {
					writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
					return
				}
				writeData(w, http.StatusOK, result)
				return
			}
			if id := chi.URLParam(r, "id"); id != "" {
				switch id {
				case "search-hot", "runtime", "error-groups":
					writeMessageError(w, 404, "接口不存在")
					return
				}
				result, found, err := service.Detail(r, id)
				if err != nil {
					writeMessageError(w, 500, "服务器内部错误")
					return
				}
				if !found {
					writeMessageError(w, 404, "审计日志不存在")
					return
				}
				writeData(w, 200, result)
				return
			}
			result, err := service.List(r, parseManagementAuditLogListQuery(r.URL.Query()))
			if err != nil {
				writeMessageError(w, 500, "服务器内部错误")
				return
			}
			writeData(w, 200, result)
		})
	})
}

func NewManagementAuditErrorGroupsHandler(service *managementauditlogs.Service) http.Handler {
	if service == nil {
		return newManagementAuditErrorGroupsHandler(nil)
	}
	return newManagementAuditErrorGroupsHandler(managementAuditLogServiceAdapter{service})
}

func newManagementAuditErrorGroupsHandler(service managementAuditLogService) http.Handler {
	readHandler := managementAuditReadHandler{service: service}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		readHandler.handle(w, r, func(service managementAuditLogService, r *http.Request) {
			result, err := service.ListErrorGroups(r, parseManagementAuditErrorGroupListQuery(r.URL.Query()))
			if err != nil {
				writeMessageError(w, 500, "服务器内部错误")
				return
			}
			writeData(w, 200, result)
		})
	})
}

func NewManagementAuditErrorGroupEventsHandler(service *managementauditlogs.Service) http.Handler {
	if service == nil {
		return newManagementAuditErrorGroupEventsHandler(nil)
	}
	return newManagementAuditErrorGroupEventsHandler(managementAuditLogServiceAdapter{service})
}

func newManagementAuditErrorGroupEventsHandler(service managementAuditLogService) http.Handler {
	readHandler := managementAuditReadHandler{service: service}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		readHandler.handle(w, r, func(service managementAuditLogService, r *http.Request) {
			errorGroupID := strings.TrimFunc(chi.URLParam(r, "errorGroupId"), managementGroupListECMAScriptWhitespace)
			if errorGroupID == "" {
				writeMessageError(w, http.StatusBadRequest, "错误分组 ID 不合法")
				return
			}
			result, err := service.ListErrorGroupEvents(r, errorGroupID, parseManagementAuditLogListQuery(r.URL.Query()))
			if err != nil {
				writeMessageError(w, 500, "服务器内部错误")
				return
			}
			writeData(w, 200, result)
		})
	})
}

type managementAuditReadHandler struct {
	service managementAuditLogService
}

func (h managementAuditReadHandler) handle(w http.ResponseWriter, r *http.Request, next func(managementAuditLogService, *http.Request)) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	auth, ok := ManagementAuthContextFromRequest(r)
	if !ok || strings.TrimSpace(auth.SystemAccountID) == "" || h.service == nil {
		writeMessageError(w, 500, "服务器内部错误")
		return
	}
	if !managementauth.IsAdminRole(auth.Role) {
		writeMessageError(w, 403, "需要管理员权限")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), managementAuditLogRequestTimeout)
	defer cancel()
	next(h.service, r.WithContext(ctx))
}

func parseManagementAuditLogListQuery(values url.Values) managementauditlogs.ListInput {
	page, _ := managementGroupListIntegerQueryValue(values, "page")
	pageSize, provided := managementGroupListIntegerQueryValue(values, "pageSize")
	status, _ := managementGroupListIntegerQueryValue(values, "statusCode")
	q := func(key string) string {
		if len(values[key]) == 0 {
			return ""
		}
		return strings.TrimFunc(values[key][0], managementGroupListECMAScriptWhitespace)
	}
	return managementauditlogs.ListInput{TraceID: q("traceId"), ErrorGroupID: q("errorGroupId"), Outcome: q("outcome"), StatusCode: status, Path: q("path"), Model: q("model"), SystemAccountID: q("systemAccountId"), APIKeyID: q("apiKeyId"), GroupID: q("groupId"), AccountID: q("accountId"), ClientIP: q("clientIp"), StartAt: q("startAt"), EndAt: q("endAt"), TrafficSource: q("trafficSource"), Page: page, PageSize: pageSize, PageSizeProvided: provided}
}

func parseManagementAuditErrorGroupListQuery(values url.Values) managementauditlogs.ErrorGroupListInput {
	page, _ := managementGroupListIntegerQueryValue(values, "page")
	pageSize, provided := managementGroupListIntegerQueryValue(values, "pageSize")
	statusCode, _ := managementGroupListIntegerQueryValue(values, "statusCode")
	q := func(key string) string {
		if len(values[key]) == 0 {
			return ""
		}
		return strings.TrimFunc(values[key][0], managementGroupListECMAScriptWhitespace)
	}
	return managementauditlogs.ErrorGroupListInput{
		Path: q("path"), Model: q("model"), StatusCode: statusCode,
		SystemAccountID: q("systemAccountId"), APIKeyID: q("apiKeyId"),
		GroupID: q("groupId"), AccountID: q("accountId"),
		Page: page, PageSize: pageSize, PageSizeProvided: provided,
	}
}

func parseManagementAuditHotSearchQuery(values url.Values) managementauditlogs.HotSearchInput {
	limit, limitProvided := managementAuditHotSearchLimitQueryValue(values, "limit")
	return managementauditlogs.HotSearchInput{
		Keywords:      append([]string(nil), values["keywords"]...),
		Limit:         limit,
		LimitProvided: limitProvided,
		StartAt:       managementAuditHotSearchQueryText(values, "startAt"),
		EndAt:         managementAuditHotSearchQueryText(values, "endAt"),
	}
}

func managementAuditHotSearchQueryText(values url.Values, key string) string {
	if len(values[key]) == 0 {
		return ""
	}
	return strings.TrimFunc(values[key][0], managementGroupListECMAScriptWhitespace)
}

func managementAuditHotSearchLimitQueryValue(values url.Values, key string) (int, bool) {
	text := managementAuditHotSearchQueryText(values, key)
	if text == "" {
		return 0, false
	}
	value, ok := managementGroupListNumber(text)
	if !ok || math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, false
	}
	value = math.Trunc(value)
	maxInt := int(^uint(0) >> 1)
	minInt := -maxInt - 1
	if value >= float64(maxInt) {
		return maxInt, true
	}
	if value <= float64(minInt) {
		return minInt, true
	}
	return int(value), true
}
