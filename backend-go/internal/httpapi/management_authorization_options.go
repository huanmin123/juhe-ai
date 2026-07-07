package httpapi

import (
	"net/http"
	"net/url"
	"strings"

	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementauthorizationoptions"
)

type managementAuthorizationOptionScope int

const (
	managementAuthorizationOptionScopeAdmin managementAuthorizationOptionScope = iota
	managementAuthorizationOptionScopeSelf
)

type managementAuthorizationOptionService interface {
	GranteeAccounts(r *http.Request, input managementauthorizationoptions.PrincipalOptionListInput) ([]managementauthorizationoptions.GranteeAccountOption, error)
}

type managementAuthorizationOptionServiceAdapter struct {
	service *managementauthorizationoptions.Service
}

func (s managementAuthorizationOptionServiceAdapter) GranteeAccounts(r *http.Request, input managementauthorizationoptions.PrincipalOptionListInput) ([]managementauthorizationoptions.GranteeAccountOption, error) {
	return s.service.GranteeAccounts(r.Context(), input)
}

func NewManagementAuthorizationGranteeAccountsHandler(service *managementauthorizationoptions.Service) http.Handler {
	return newManagementAuthorizationGranteeAccountsHandler(managementAuthorizationOptionServiceAdapter{service: service}, managementAuthorizationOptionScopeAdmin)
}

func NewManagementMyAuthorizationGranteeAccountsHandler(service *managementauthorizationoptions.Service) http.Handler {
	return newManagementAuthorizationGranteeAccountsHandler(managementAuthorizationOptionServiceAdapter{service: service}, managementAuthorizationOptionScopeSelf)
}

func newManagementAuthorizationGranteeAccountsHandler(service managementAuthorizationOptionService, scope managementAuthorizationOptionScope) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authContext, ok := ManagementAuthContextFromRequest(r)
		if !ok || strings.TrimSpace(authContext.SystemAccountID) == "" {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		if scope == managementAuthorizationOptionScopeAdmin && !managementauth.IsAdminRole(authContext.Role) {
			writeMessageError(w, http.StatusForbidden, "需要管理员权限")
			return
		}
		options, err := service.GranteeAccounts(r, parseManagementAuthorizationPrincipalOptionListQuery(r.URL.Query()))
		if err != nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		writeData(w, http.StatusOK, options)
	})
}

func parseManagementAuthorizationPrincipalOptionListQuery(values url.Values) managementauthorizationoptions.PrincipalOptionListInput {
	return managementauthorizationoptions.PrincipalOptionListInput{
		IDs:     managementTextListQueryValue(values, "ids", 50),
		Keyword: firstManagementQueryText(values, "keyword"),
		Limit:   managementIntegerQueryValue(values, "limit"),
	}
}
