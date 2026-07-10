package httpapi

import (
	"net/http"
	"strings"

	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementstats"
)

type managementStatsUsageWindowScope int

const (
	managementStatsUsageWindowScopeAdmin managementStatsUsageWindowScope = iota
	managementStatsUsageWindowScopeSelf
)

func NewManagementStatsUsageWindowHandler(service *managementstats.Service) http.Handler {
	return newManagementStatsUsageWindowHandler(service, managementStatsUsageWindowScopeAdmin)
}

func NewManagementMyStatsUsageWindowHandler(service *managementstats.Service) http.Handler {
	return newManagementStatsUsageWindowHandler(service, managementStatsUsageWindowScopeSelf)
}

func newManagementStatsUsageWindowHandler(service *managementstats.Service, scope managementStatsUsageWindowScope) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authContext, ok := ManagementAuthContextFromRequest(r)
		if !ok || strings.TrimSpace(authContext.SystemAccountID) == "" {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		if scope == managementStatsUsageWindowScopeAdmin && !managementauth.IsAdminRole(authContext.Role) {
			writeMessageError(w, http.StatusForbidden, "需要管理员权限")
			return
		}
		window, err := service.UsageWindow(r.Context())
		if err != nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		writeData(w, http.StatusOK, window)
	})
}
