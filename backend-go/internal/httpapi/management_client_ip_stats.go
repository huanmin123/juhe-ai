package httpapi

import (
	"net/http"
	"net/url"
	"strings"

	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementclientipstats"
)

type managementClientIPStatsService interface {
	List(
		request *http.Request,
		input managementclientipstats.ListInput,
	) (managementclientipstats.ListResult, error)
}

type managementClientIPStatsServiceAdapter struct {
	service *managementclientipstats.Service
}

func (adapter managementClientIPStatsServiceAdapter) List(
	request *http.Request,
	input managementclientipstats.ListInput,
) (managementclientipstats.ListResult, error) {
	return adapter.service.List(request.Context(), input)
}

func NewManagementClientIPStatsHandler(service *managementclientipstats.Service) http.Handler {
	if service == nil {
		return newManagementClientIPStatsHandler(nil)
	}
	return newManagementClientIPStatsHandler(managementClientIPStatsServiceAdapter{service: service})
}

func newManagementClientIPStatsHandler(service managementClientIPStatsService) http.Handler {
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

		input, valid := managementClientIPStatsListInput(request.URL.Query())
		if !valid {
			writeMessageError(writer, http.StatusBadRequest, "IP 统计参数无效")
			return
		}
		result, err := service.List(request, input)
		if err != nil {
			writeMessageError(writer, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		writeData(writer, http.StatusOK, result)
	})
}

func managementClientIPStatsListInput(values url.Values) (managementclientipstats.ListInput, bool) {
	page, valid := managementClientIPStatsIntegerQuery(values, "page", 1, 0)
	if !valid {
		return managementclientipstats.ListInput{}, false
	}
	pageSize, valid := managementClientIPStatsIntegerQuery(values, "pageSize", 1, 100)
	if !valid {
		return managementclientipstats.ListInput{}, false
	}
	keyword, valid := managementClientIPStatsTrimmedStringQuery(values, "keyword")
	if !valid {
		return managementclientipstats.ListInput{}, false
	}
	status, valid := managementClientIPStatsEnumQuery(
		values,
		"status",
		"all",
		"normal",
		"blacklisted",
		"allowlisted",
	)
	if !valid {
		return managementclientipstats.ListInput{}, false
	}
	startDate, valid := managementClientIPStatsTrimmedStringQuery(values, "startDate")
	if !valid {
		return managementclientipstats.ListInput{}, false
	}
	endDate, valid := managementClientIPStatsTrimmedStringQuery(values, "endDate")
	if !valid {
		return managementclientipstats.ListInput{}, false
	}
	lastUsedStartDate, valid := managementClientIPStatsTrimmedStringQuery(values, "lastUsedStartDate")
	if !valid {
		return managementclientipstats.ListInput{}, false
	}
	lastUsedEndDate, valid := managementClientIPStatsTrimmedStringQuery(values, "lastUsedEndDate")
	if !valid {
		return managementclientipstats.ListInput{}, false
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
		return managementclientipstats.ListInput{}, false
	}
	sortOrder, valid := managementClientIPStatsEnumQuery(values, "sortOrder", "asc", "desc")
	if !valid {
		return managementclientipstats.ListInput{}, false
	}

	return managementclientipstats.ListInput{
		Page:              page,
		PageSize:          pageSize,
		Keyword:           keyword,
		Status:            status,
		StartDate:         startDate,
		EndDate:           endDate,
		LastUsedStartDate: lastUsedStartDate,
		LastUsedEndDate:   lastUsedEndDate,
		SortField:         sortField,
		SortOrder:         sortOrder,
	}, true
}

func managementClientIPStatsIntegerQuery(
	values url.Values,
	key string,
	minimum int,
	maximum int,
) (int, bool) {
	items, exists := values[key]
	if !exists {
		return 0, true
	}
	if len(items) != 1 {
		return 0, false
	}
	value, valid := managementGroupListIntegerQueryValue(values, key)
	if !valid || value < minimum || maximum > 0 && value > maximum {
		return 0, false
	}
	return value, true
}

func managementClientIPStatsTrimmedStringQuery(values url.Values, key string) (string, bool) {
	items, exists := values[key]
	if !exists {
		return "", true
	}
	if len(items) != 1 {
		return "", false
	}
	return strings.TrimFunc(items[0], managementGroupListECMAScriptWhitespace), true
}

func managementClientIPStatsEnumQuery(values url.Values, key string, allowed ...string) (string, bool) {
	items, exists := values[key]
	if !exists {
		return "", true
	}
	if len(items) != 1 {
		return "", false
	}
	for _, candidate := range allowed {
		if items[0] == candidate {
			return items[0], true
		}
	}
	return "", false
}
