package httpapi

import (
	"context"
	"net/http"
	"regexp"
	"strings"

	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementstatsoverview"
)

var managementStatsOverviewDatePattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

type managementStatsUsageOverviewService interface {
	Overview(context.Context, string, managementstatsoverview.Input) (managementstatsoverview.Overview, error)
}

func NewManagementStatsUsageOverviewHandler(service *managementstatsoverview.Service) http.Handler {
	return newManagementStatsUsageOverviewHandler(service)
}

func NewManagementMyStatsUsageOverviewHandler(service *managementstatsoverview.Service) http.Handler {
	return newManagementMyStatsUsageOverviewHandler(service)
}

func newManagementStatsUsageOverviewHandler(service managementStatsUsageOverviewService) http.Handler {
	return managementStatsUsageOverviewHandler(service, true)
}

func newManagementMyStatsUsageOverviewHandler(service managementStatsUsageOverviewService) http.Handler {
	return managementStatsUsageOverviewHandler(service, false)
}

func managementStatsUsageOverviewHandler(service managementStatsUsageOverviewService, adminScope bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authContext, ok := ManagementAuthContextFromRequest(r)
		if !ok || strings.TrimSpace(authContext.SystemAccountID) == "" || service == nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		if adminScope && !managementauth.IsAdminRole(authContext.Role) {
			writeMessageError(w, http.StatusForbidden, "需要管理员权限")
			return
		}
		input, ok := managementStatsOverviewInput(r)
		if !ok {
			writeMessageError(w, http.StatusBadRequest, "统计日期范围不合法")
			return
		}
		systemAccountID := authContext.SystemAccountID
		if adminScope {
			systemAccountID = managementStatsOverviewAdminScope(r)
		}
		result, err := service.Overview(r.Context(), systemAccountID, input)
		if err != nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		writeData(w, http.StatusOK, result)
	})
}

func managementStatsOverviewInput(r *http.Request) (managementstatsoverview.Input, bool) {
	startDate, ok := managementStatsOverviewDateQuery(r, "startDate")
	if !ok {
		return managementstatsoverview.Input{}, false
	}
	endDate, ok := managementStatsOverviewDateQuery(r, "endDate")
	if !ok {
		return managementstatsoverview.Input{}, false
	}
	return managementstatsoverview.Input{StartDate: startDate, EndDate: endDate}, true
}

func managementStatsOverviewDateQuery(r *http.Request, key string) (string, bool) {
	values, exists := r.URL.Query()[key]
	if !exists {
		return "", true
	}
	if len(values) != 1 {
		return "", false
	}
	value := strings.TrimFunc(values[0], managementGroupListECMAScriptWhitespace)
	if !managementStatsOverviewDatePattern.MatchString(value) {
		return "", false
	}
	return value, true
}

func managementStatsOverviewAdminScope(r *http.Request) string {
	values := r.URL.Query()["systemAccountId"]
	if len(values) == 0 {
		return managementstatsoverview.GlobalSystemAccountID()
	}
	value := strings.TrimFunc(values[0], managementGroupListECMAScriptWhitespace)
	if value == "" || value == "all" {
		return managementstatsoverview.GlobalSystemAccountID()
	}
	return value
}
