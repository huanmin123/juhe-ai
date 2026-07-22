package httpapi

import (
	"net/http"
	"strings"

	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementmodelcheckoptions"
)

type managementModelCheckOptionsScope int

const (
	managementModelCheckOptionsScopeAdmin managementModelCheckOptionsScope = iota
	managementModelCheckOptionsScopeSelf
)

type managementModelCheckOptionsService interface {
	Options() managementmodelcheckoptions.Result
}

func NewManagementModelCheckOptionsHandler(service *managementmodelcheckoptions.Service) http.Handler {
	return newManagementModelCheckOptionsHandler(managementModelCheckOptionsServiceFrom(service), managementModelCheckOptionsScopeAdmin)
}

func NewManagementMyModelCheckOptionsHandler(service *managementmodelcheckoptions.Service) http.Handler {
	return newManagementModelCheckOptionsHandler(managementModelCheckOptionsServiceFrom(service), managementModelCheckOptionsScopeSelf)
}

func managementModelCheckOptionsServiceFrom(service *managementmodelcheckoptions.Service) managementModelCheckOptionsService {
	if service == nil {
		return nil
	}
	return service
}

func newManagementModelCheckOptionsHandler(service managementModelCheckOptionsService, scope managementModelCheckOptionsScope) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth, ok := ManagementAuthContextFromRequest(r)
		if !ok || strings.TrimSpace(auth.SystemAccountID) == "" {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		if scope == managementModelCheckOptionsScopeAdmin && !managementauth.IsAdminRole(auth.Role) {
			writeMessageError(w, http.StatusForbidden, "需要管理员权限")
			return
		}
		if service == nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		writeData(w, http.StatusOK, service.Options())
	})
}
