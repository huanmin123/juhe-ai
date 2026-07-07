package httpapi

import (
	"net/http"
	"net/url"
	"strings"

	"juhe-ai/backend-go/internal/modules/managementaccounts"
	"juhe-ai/backend-go/internal/modules/managementauth"
)

type managementAccountOptionScope int

const (
	managementAccountScopeAdmin managementAccountOptionScope = iota
	managementAccountScopeSelf
)

type managementAccountOptionService interface {
	Options(r *http.Request, input managementaccounts.OptionListInput) ([]managementaccounts.Option, error)
}

type managementAccountOptionServiceAdapter struct {
	service *managementaccounts.Service
}

func (s managementAccountOptionServiceAdapter) Options(r *http.Request, input managementaccounts.OptionListInput) ([]managementaccounts.Option, error) {
	return s.service.Options(r.Context(), input)
}

func NewManagementAccountOptionsHandler(service *managementaccounts.Service) http.Handler {
	return newManagementAccountOptionsHandler(managementAccountOptionServiceAdapter{service: service}, managementAccountScopeAdmin)
}

func NewManagementMyAccountOptionsHandler(service *managementaccounts.Service) http.Handler {
	return newManagementAccountOptionsHandler(managementAccountOptionServiceAdapter{service: service}, managementAccountScopeSelf)
}

func newManagementAccountOptionsHandler(service managementAccountOptionService, scope managementAccountOptionScope) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authContext, ok := ManagementAuthContextFromRequest(r)
		if !ok || strings.TrimSpace(authContext.SystemAccountID) == "" {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		input, allowed := managementAccountOptionListInput(authContext, r.URL.Query(), scope)
		if !allowed {
			writeMessageError(w, http.StatusForbidden, "需要管理员权限")
			return
		}
		options, err := service.Options(r, input)
		if err != nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		writeData(w, http.StatusOK, options)
	})
}

func managementAccountOptionListInput(
	authContext managementauth.Context,
	values url.Values,
	scope managementAccountOptionScope,
) (managementaccounts.OptionListInput, bool) {
	input := parseManagementAccountOptionListQuery(values)
	switch scope {
	case managementAccountScopeAdmin:
		if !managementauth.IsAdminRole(authContext.Role) {
			return managementaccounts.OptionListInput{}, false
		}
		input.IncludeSystemAccountFields = true
		systemAccountID := firstManagementQueryText(values, "systemAccountId")
		if systemAccountID != "" && systemAccountID != "all" {
			input.SystemAccountID = systemAccountID
		}
	case managementAccountScopeSelf:
		input.SystemAccountID = authContext.SystemAccountID
		input.IncludeSystemAccountFields = false
	}
	return input, true
}

func parseManagementAccountOptionListQuery(values url.Values) managementaccounts.OptionListInput {
	return managementaccounts.OptionListInput{
		IDs:          managementTextListQueryValue(values, "ids", 50),
		Page:         managementIntegerQueryValue(values, "page"),
		Limit:        managementIntegerQueryValue(values, "limit"),
		Keyword:      firstManagementQueryText(values, "keyword"),
		ProviderCode: firstManagementQueryText(values, "providerCode"),
		GroupID:      firstManagementQueryText(values, "groupId"),
		TagIDs:       managementTextListQueryValue(values, "tagIds", 100),
		Type:         firstManagementQueryText(values, "type"),
		Status:       strings.Join(managementTextListQueryValue(values, "status", 100), ","),
		Schedulable:  firstManagementQueryText(values, "schedulable"),
	}
}
