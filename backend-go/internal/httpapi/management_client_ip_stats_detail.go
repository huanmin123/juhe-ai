package httpapi

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"

	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementclientipstats"
)

type managementClientIPStatsDetailService interface {
	Detail(
		request *http.Request,
		input managementclientipstats.DetailInput,
	) (managementclientipstats.DetailResult, error)
}

func (adapter managementClientIPStatsServiceAdapter) Detail(
	request *http.Request,
	input managementclientipstats.DetailInput,
) (managementclientipstats.DetailResult, error) {
	return adapter.service.Detail(request.Context(), input)
}

func NewManagementClientIPStatsDetailHandler(service *managementclientipstats.Service) http.Handler {
	if service == nil {
		return newManagementClientIPStatsDetailHandler(nil)
	}
	return newManagementClientIPStatsDetailHandler(managementClientIPStatsServiceAdapter{service: service})
}

func newManagementClientIPStatsDetailHandler(service managementClientIPStatsDetailService) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		authContext, ok := ManagementAuthContextFromRequest(request)
		if !ok || strings.TrimSpace(authContext.SystemAccountID) == "" {
			writeMessageError(writer, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		if !managementauth.IsAdminRole(authContext.Role) {
			writeMessageError(writer, http.StatusForbidden, "需要管理员权限")
			return
		}
		if service == nil {
			writeMessageError(writer, http.StatusInternalServerError, "服务器内部错误")
			return
		}

		ipHash := strings.TrimFunc(chi.URLParam(request, "ipHash"), managementGroupListECMAScriptWhitespace)
		if !validManagementClientIPHash(ipHash) {
			writeMessageError(writer, http.StatusBadRequest, "IP 标识无效")
			return
		}
		input, valid := managementClientIPStatsDetailInput(request.URL.Query())
		if !valid {
			writeMessageError(writer, http.StatusBadRequest, "IP 详情参数无效")
			return
		}
		input.IPHash = ipHash
		result, err := service.Detail(request, input)
		if errors.Is(err, managementclientipstats.ErrIPNotFound) {
			writeMessageError(writer, http.StatusNotFound, "IP 不存在")
			return
		}
		if err != nil {
			writeMessageError(writer, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		writeData(writer, http.StatusOK, result)
	})
}

func managementClientIPStatsDetailInput(values url.Values) (managementclientipstats.DetailInput, bool) {
	page, valid := managementClientIPStatsIntegerQuery(values, "page", 1, 0)
	if !valid {
		return managementclientipstats.DetailInput{}, false
	}
	pageSize, valid := managementClientIPStatsIntegerQuery(values, "pageSize", 1, 100)
	if !valid {
		return managementclientipstats.DetailInput{}, false
	}
	startDate, valid := managementClientIPStatsTrimmedStringQuery(values, "startDate")
	if !valid {
		return managementclientipstats.DetailInput{}, false
	}
	endDate, valid := managementClientIPStatsTrimmedStringQuery(values, "endDate")
	if !valid {
		return managementclientipstats.DetailInput{}, false
	}
	sortField, valid := managementClientIPStatsEnumQuery(
		values,
		"sortField",
		"requestCount",
		"successCount",
		"errorCount",
		"errorRate",
		"totalTokens",
		"totalCost",
		"activeDays",
		"lastUsedAt",
	)
	if !valid {
		return managementclientipstats.DetailInput{}, false
	}
	sortOrder, valid := managementClientIPStatsEnumQuery(values, "sortOrder", "asc", "desc")
	if !valid {
		return managementclientipstats.DetailInput{}, false
	}
	return managementclientipstats.DetailInput{
		Page:      page,
		PageSize:  pageSize,
		StartDate: startDate,
		EndDate:   endDate,
		SortField: sortField,
		SortOrder: sortOrder,
	}, true
}
