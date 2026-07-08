package httpapi

import (
	"net/http"
	"net/url"
	"strings"

	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementsystemaccounts"
)

type managementSystemAccountOptionService interface {
	List(r *http.Request, input managementsystemaccounts.ListInput) (managementsystemaccounts.ListResult, error)
	Options(r *http.Request, input managementsystemaccounts.OptionListInput) ([]managementsystemaccounts.Option, error)
}

type managementSystemAccountOptionServiceAdapter struct {
	service *managementsystemaccounts.Service
}

func (s managementSystemAccountOptionServiceAdapter) Options(r *http.Request, input managementsystemaccounts.OptionListInput) ([]managementsystemaccounts.Option, error) {
	return s.service.Options(r.Context(), input)
}

func (s managementSystemAccountOptionServiceAdapter) List(r *http.Request, input managementsystemaccounts.ListInput) (managementsystemaccounts.ListResult, error) {
	return s.service.List(r.Context(), input)
}

func NewManagementSystemAccountsHandler(service *managementsystemaccounts.Service) http.Handler {
	return newManagementSystemAccountsHandler(managementSystemAccountOptionServiceAdapter{service: service})
}

func NewManagementSystemAccountOptionsHandler(service *managementsystemaccounts.Service) http.Handler {
	return newManagementSystemAccountOptionsHandler(managementSystemAccountOptionServiceAdapter{service: service})
}

func newManagementSystemAccountsHandler(service managementSystemAccountOptionService) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authContext, ok := ManagementAuthContextFromRequest(r)
		if !ok || strings.TrimSpace(authContext.SystemAccountID) == "" {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		if !managementauth.IsAdminRole(authContext.Role) {
			writeMessageError(w, http.StatusForbidden, "需要管理员权限")
			return
		}
		result, err := service.List(r, parseManagementSystemAccountListQuery(r.URL.Query()))
		if err != nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		writeData(w, http.StatusOK, result)
	})
}

func newManagementSystemAccountOptionsHandler(service managementSystemAccountOptionService) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authContext, ok := ManagementAuthContextFromRequest(r)
		if !ok || strings.TrimSpace(authContext.SystemAccountID) == "" {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		if !managementauth.IsAdminRole(authContext.Role) {
			writeMessageError(w, http.StatusForbidden, "需要管理员权限")
			return
		}
		options, err := service.Options(r, parseManagementSystemAccountOptionListQuery(r.URL.Query()))
		if err != nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		writeData(w, http.StatusOK, options)
	})
}

func parseManagementSystemAccountListQuery(values url.Values) managementsystemaccounts.ListInput {
	return managementsystemaccounts.ListInput{
		Keyword:  firstManagementQueryText(values, "keyword"),
		Page:     managementIntegerQueryValue(values, "page"),
		PageSize: managementIntegerQueryValue(values, "pageSize"),
	}
}

func parseManagementSystemAccountOptionListQuery(values url.Values) managementsystemaccounts.OptionListInput {
	return managementsystemaccounts.OptionListInput{
		IDs:     managementTextListQueryValue(values, "ids", 50),
		Keyword: firstManagementQueryText(values, "keyword"),
		Limit:   managementIntegerQueryValue(values, "limit"),
	}
}
