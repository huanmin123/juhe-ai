package httpapi

import (
	"context"
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
	Detail(*http.Request, string) (managementauditlogs.Detail, bool, error)
}
type managementAuditLogServiceAdapter struct{ service *managementauditlogs.Service }

func (a managementAuditLogServiceAdapter) List(r *http.Request, input managementauditlogs.ListInput) (managementauditlogs.ListResult, error) {
	return a.service.List(r.Context(), input)
}
func (a managementAuditLogServiceAdapter) Detail(r *http.Request, id string) (managementauditlogs.Detail, bool, error) {
	return a.service.Detail(r.Context(), id)
}
func NewManagementAuditLogsHandler(service *managementauditlogs.Service) http.Handler {
	return newManagementAuditLogsHandler(managementAuditLogServiceAdapter{service})
}
func newManagementAuditLogsHandler(service managementAuditLogService) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Pragma", "no-cache")
		auth, ok := ManagementAuthContextFromRequest(r)
		if !ok || strings.TrimSpace(auth.SystemAccountID) == "" || service == nil {
			writeMessageError(w, 500, "服务器内部错误")
			return
		}
		if !managementauth.IsAdminRole(auth.Role) {
			writeMessageError(w, 403, "需要管理员权限")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), managementAuditLogRequestTimeout)
		defer cancel()
		r = r.WithContext(ctx)
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
