package httpapi

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementpublicapilogs"
)

var managementPublicAPILogDateTimePattern = regexp.MustCompile(
	`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(?:\.[0-9]{3})?Z$`,
)

type managementPublicAPILogService interface {
	List(r *http.Request, input managementpublicapilogs.ListInput) (managementpublicapilogs.ListResult, error)
	Detail(r *http.Request, id string) (managementpublicapilogs.Detail, bool, error)
}

type managementPublicAPILogServiceAdapter struct {
	service *managementpublicapilogs.Service
}

func (s managementPublicAPILogServiceAdapter) List(
	r *http.Request,
	input managementpublicapilogs.ListInput,
) (managementpublicapilogs.ListResult, error) {
	return s.service.List(r.Context(), input)
}

func (s managementPublicAPILogServiceAdapter) Detail(
	r *http.Request,
	id string,
) (managementpublicapilogs.Detail, bool, error) {
	return s.service.Detail(r.Context(), id)
}

func NewManagementPublicAPILogsHandler(service *managementpublicapilogs.Service) http.Handler {
	if service == nil {
		return newManagementPublicAPILogsHandler(nil)
	}
	return newManagementPublicAPILogsHandler(managementPublicAPILogServiceAdapter{service: service})
}

func newManagementPublicAPILogsHandler(service managementPublicAPILogService) http.Handler {
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
		if service == nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}

		if id := chi.URLParam(r, "id"); id != "" {
			detail, found, err := service.Detail(r, id)
			if err != nil {
				writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
				return
			}
			if !found {
				writeMessageError(w, http.StatusNotFound, "公开接口日志不存在")
				return
			}
			writeData(w, http.StatusOK, detail)
			return
		}

		result, err := service.List(r, parseManagementPublicAPILogListQuery(r.URL.Query()))
		if err != nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		writeData(w, http.StatusOK, result)
	})
}

func parseManagementPublicAPILogListQuery(values url.Values) managementpublicapilogs.ListInput {
	page, _ := managementGroupListIntegerQueryValue(values, "page")
	pageSize, pageSizeProvided := managementGroupListIntegerQueryValue(values, "pageSize")
	return managementpublicapilogs.ListInput{
		TraceID:          managementPublicAPILogQueryText(values, "traceId"),
		SourceRefID:      managementPublicAPILogQueryText(values, "sourceRefId"),
		Path:             managementPublicAPILogQueryText(values, "path"),
		Result:           managementPublicAPILogResultQueryValue(values),
		StatusCode:       managementPublicAPILogStatusCodeQueryValue(values),
		ClientIP:         managementPublicAPILogQueryText(values, "clientIp"),
		StartAt:          managementPublicAPILogDateTimeQueryValue(managementPublicAPILogQueryText(values, "startAt")),
		EndAt:            managementPublicAPILogDateTimeQueryValue(managementPublicAPILogQueryText(values, "endAt")),
		Page:             page,
		PageSize:         pageSize,
		PageSizeProvided: pageSizeProvided,
	}
}

func managementPublicAPILogQueryText(values url.Values, key string) string {
	items := values[key]
	if len(items) == 0 {
		return ""
	}
	return strings.TrimFunc(items[0], managementGroupListECMAScriptWhitespace)
}

func managementPublicAPILogResultQueryValue(values url.Values) string {
	result := managementPublicAPILogQueryText(values, "result")
	switch result {
	case "success", "failed", "all":
		return result
	default:
		return ""
	}
}

func managementPublicAPILogStatusCodeQueryValue(values url.Values) int {
	statusCode, provided := managementGroupListIntegerQueryValue(values, "statusCode")
	if !provided || statusCode < 100 || statusCode > 599 {
		return 0
	}
	return statusCode
}

func managementPublicAPILogDateTimeQueryValue(text string) time.Time {
	if !managementPublicAPILogDateTimePattern.MatchString(text) {
		return time.Time{}
	}
	value, err := time.Parse(time.RFC3339Nano, text)
	if err != nil {
		return time.Time{}
	}
	return value.UTC()
}
