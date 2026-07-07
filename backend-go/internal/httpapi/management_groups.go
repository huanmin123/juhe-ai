package httpapi

import (
	"net/http"
	"net/url"
	"strings"

	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementgroups"
)

type managementGroupOptionScope int

const (
	managementGroupScopeAdmin managementGroupOptionScope = iota
	managementGroupScopeSelf
)

type managementGroupOptionService interface {
	Options(r *http.Request, input managementgroups.OptionListInput) ([]managementgroups.Option, error)
}

type managementGroupAccountOptionService interface {
	AccountOptions(r *http.Request, input managementgroups.OptionListInput) ([]managementgroups.AccountOption, error)
}

type managementGroupOptionServiceAdapter struct {
	service *managementgroups.Service
}

func (s managementGroupOptionServiceAdapter) Options(r *http.Request, input managementgroups.OptionListInput) ([]managementgroups.Option, error) {
	return s.service.Options(r.Context(), input)
}

func (s managementGroupOptionServiceAdapter) AccountOptions(r *http.Request, input managementgroups.OptionListInput) ([]managementgroups.AccountOption, error) {
	return s.service.AccountOptions(r.Context(), input)
}

func NewManagementGroupOptionsHandler(service *managementgroups.Service) http.Handler {
	return newManagementGroupOptionsHandler(managementGroupOptionServiceAdapter{service: service}, managementGroupScopeAdmin)
}

func NewManagementMyGroupOptionsHandler(service *managementgroups.Service) http.Handler {
	return newManagementGroupOptionsHandler(managementGroupOptionServiceAdapter{service: service}, managementGroupScopeSelf)
}

func NewManagementGroupAccountOptionsHandler(service *managementgroups.Service) http.Handler {
	return newManagementGroupAccountOptionsHandler(managementGroupOptionServiceAdapter{service: service}, managementGroupScopeAdmin)
}

func NewManagementMyGroupAccountOptionsHandler(service *managementgroups.Service) http.Handler {
	return newManagementGroupAccountOptionsHandler(managementGroupOptionServiceAdapter{service: service}, managementGroupScopeSelf)
}

func newManagementGroupOptionsHandler(service managementGroupOptionService, scope managementGroupOptionScope) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authContext, ok := ManagementAuthContextFromRequest(r)
		if !ok || strings.TrimSpace(authContext.SystemAccountID) == "" {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		input, allowed := managementGroupOptionListInput(authContext, r.URL.Query(), scope)
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

func newManagementGroupAccountOptionsHandler(service managementGroupAccountOptionService, scope managementGroupOptionScope) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authContext, ok := ManagementAuthContextFromRequest(r)
		if !ok || strings.TrimSpace(authContext.SystemAccountID) == "" {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		input, allowed := managementGroupOptionListInput(authContext, r.URL.Query(), scope)
		if !allowed {
			writeMessageError(w, http.StatusForbidden, "需要管理员权限")
			return
		}
		options, err := service.AccountOptions(r, input)
		if err != nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		writeData(w, http.StatusOK, options)
	})
}

func managementGroupOptionListInput(
	authContext managementauth.Context,
	values url.Values,
	scope managementGroupOptionScope,
) (managementgroups.OptionListInput, bool) {
	input := parseManagementGroupOptionListQuery(values)
	switch scope {
	case managementGroupScopeAdmin:
		if !managementauth.IsAdminRole(authContext.Role) {
			return managementgroups.OptionListInput{}, false
		}
		input.IncludeSystemAccountFields = true
		systemAccountID := firstManagementQueryText(values, "systemAccountId")
		if systemAccountID != "" && systemAccountID != "all" {
			input.SystemAccountID = systemAccountID
		}
	case managementGroupScopeSelf:
		input.SystemAccountID = authContext.SystemAccountID
		input.IncludeSystemAccountFields = false
	}
	return input, true
}

func parseManagementGroupOptionListQuery(values url.Values) managementgroups.OptionListInput {
	manageableOnly := false
	if value, ok := managementBooleanQueryValue(values, "manageableOnly"); ok {
		manageableOnly = value
	}
	preferDefault := false
	if value, ok := managementBooleanQueryValue(values, "preferDefault"); ok {
		preferDefault = value
	}
	return managementgroups.OptionListInput{
		IDs:            managementTextListQueryValue(values, "ids", 50),
		Keyword:        firstManagementQueryText(values, "keyword"),
		ProviderCode:   firstManagementQueryText(values, "providerCode"),
		Limit:          managementIntegerQueryValue(values, "limit"),
		ManageableOnly: manageableOnly,
		PreferDefault:  preferDefault,
	}
}
