package httpapi

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"

	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementusagerecords"
)

type managementUsageRecordScope int

const (
	managementUsageRecordScopeAdmin managementUsageRecordScope = iota
	managementUsageRecordScopeSelf
)

type managementUsageRecordService interface {
	List(*http.Request, managementusagerecords.ListInput) (managementusagerecords.ListResult, error)
	Detail(*http.Request, managementusagerecords.DetailInput) (managementusagerecords.Summary, bool, error)
}

type managementUsageRecordServiceAdapter struct {
	service *managementusagerecords.Service
}

func (a managementUsageRecordServiceAdapter) List(r *http.Request, input managementusagerecords.ListInput) (managementusagerecords.ListResult, error) {
	return a.service.List(r.Context(), input)
}

func (a managementUsageRecordServiceAdapter) Detail(r *http.Request, input managementusagerecords.DetailInput) (managementusagerecords.Summary, bool, error) {
	return a.service.Detail(r.Context(), input)
}

func NewManagementUsageRecordsHandler(service *managementusagerecords.Service) http.Handler {
	return newManagementUsageRecordsHandler(managementUsageRecordServiceAdapter{service: service}, managementUsageRecordScopeAdmin)
}

func NewManagementMyUsageRecordsHandler(service *managementusagerecords.Service) http.Handler {
	return newManagementUsageRecordsHandler(managementUsageRecordServiceAdapter{service: service}, managementUsageRecordScopeSelf)
}

func newManagementUsageRecordsHandler(service managementUsageRecordService, scope managementUsageRecordScope) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authContext, ok := ManagementAuthContextFromRequest(r)
		if !ok || strings.TrimSpace(authContext.SystemAccountID) == "" || service == nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		input, allowed := managementUsageRecordListInput(authContext, r.URL.Query(), scope)
		if !allowed {
			writeMessageError(w, http.StatusForbidden, "需要管理员权限")
			return
		}
		if scope == managementUsageRecordScopeAdmin && input.ScopeSystemAccountID == "" && managementUsageRecordHasUnsupportedAllAccountFilters(r.URL.Query()) {
			writeMessageError(w, http.StatusBadRequest, "请先选择系统账户后筛选")
			return
		}
		if id := chi.URLParam(r, "id"); id != "" {
			detail, found, err := service.Detail(r, managementusagerecords.DetailInput{
				ID: id, ScopeSystemAccountID: input.ScopeSystemAccountID, IncludeSystemAccount: input.IncludeSystemAccount,
			})
			if err != nil {
				writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
				return
			}
			if !found {
				writeMessageError(w, http.StatusNotFound, "使用记录不存在")
				return
			}
			writeData(w, http.StatusOK, detail)
			return
		}
		result, err := service.List(r, input)
		if err != nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		writeData(w, http.StatusOK, result)
	})
}

func managementUsageRecordListInput(authContext managementauth.Context, values url.Values, scope managementUsageRecordScope) (managementusagerecords.ListInput, bool) {
	page, _ := managementGroupListIntegerQueryValue(values, "page")
	pageSize, pageSizeProvided := managementGroupListIntegerQueryValue(values, "pageSize")
	statusCode, _ := managementGroupListIntegerQueryValue(values, "statusCode")
	input := managementusagerecords.ListInput{
		ScopeSystemAccountID: managementUsageRecordQueryText(values, "systemAccountId"),
		TraceID:              managementUsageRecordQueryText(values, "traceId"),
		AccountKeyword:       managementUsageRecordQueryText(values, "accountKeyword"),
		ClientIP:             managementUsageRecordQueryText(values, "clientIp"),
		Result:               managementUsageRecordQueryText(values, "result"),
		StatusCode:           statusCode,
		GroupID:              managementUsageRecordQueryText(values, "groupId"),
		Model:                managementUsageRecordQueryText(values, "model"),
		TrafficSource:        managementUsageRecordQueryText(values, "trafficSource"),
		StartDate:            managementUsageRecordQueryText(values, "startDate"),
		EndDate:              managementUsageRecordQueryText(values, "endDate"),
		SortOrder:            managementUsageRecordQueryText(values, "sortOrder"),
		Page:                 page,
		PageSize:             pageSize,
		PageSizeProvided:     pageSizeProvided,
	}
	switch scope {
	case managementUsageRecordScopeAdmin:
		if !managementauth.IsAdminRole(authContext.Role) {
			return managementusagerecords.ListInput{}, false
		}
		input.IncludeSystemAccount = true
		if input.ScopeSystemAccountID == "all" {
			input.ScopeSystemAccountID = ""
		}
	case managementUsageRecordScopeSelf:
		input.ScopeSystemAccountID = authContext.SystemAccountID
		input.IncludeSystemAccount = false
	}
	return input, true
}

func managementUsageRecordHasUnsupportedAllAccountFilters(values url.Values) bool {
	for _, key := range []string{"accountKeyword", "result", "statusCode", "clientIp", "groupId", "model", "traceId", "trafficSource", "startDate", "endDate"} {
		if managementUsageRecordQueryText(values, key) != "" {
			return true
		}
	}
	sortBy := managementUsageRecordQueryText(values, "sortBy")
	sortOrder := managementUsageRecordQueryText(values, "sortOrder")
	return (sortBy != "" && sortBy != "createdAt") || (sortOrder != "" && sortOrder != "desc")
}

func managementUsageRecordQueryText(values url.Values, key string) string {
	items := values[key]
	if len(items) == 0 {
		return ""
	}
	return strings.TrimFunc(items[0], managementGroupListECMAScriptWhitespace)
}
