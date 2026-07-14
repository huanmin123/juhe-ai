package httpapi

import (
	"net/http"

	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/publicapi"
)

func NewManagementExternalIntegrationSourceScopesHandler() http.Handler {
	return newManagementExternalIntegrationSourceScopesHandler()
}

func newManagementExternalIntegrationSourceScopesHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authContext, ok := ManagementAuthContextFromRequest(r)
		if !ok {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		if !managementauth.IsAdminRole(authContext.Role) {
			writeMessageError(w, http.StatusForbidden, "需要管理员权限")
			return
		}

		writeData(w, http.StatusOK, publicapi.ScopeOptions())
	})
}
