package httpapi

import (
	"net/http"
	"net/url"

	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementproviders"
)

type managementProviderOptionService interface {
	List(r *http.Request, input managementproviders.ListInput) ([]managementproviders.Option, error)
	Options(r *http.Request, input managementproviders.OptionListInput) ([]managementproviders.Option, error)
	SelectOptions(r *http.Request) ([]managementproviders.SelectOption, error)
}

type managementProviderOptionServiceAdapter struct {
	service *managementproviders.Service
}

func (s managementProviderOptionServiceAdapter) Options(r *http.Request, input managementproviders.OptionListInput) ([]managementproviders.Option, error) {
	return s.service.Options(r.Context(), input)
}

func (s managementProviderOptionServiceAdapter) SelectOptions(r *http.Request) ([]managementproviders.SelectOption, error) {
	return s.service.SelectOptions(r.Context())
}

func (s managementProviderOptionServiceAdapter) List(r *http.Request, input managementproviders.ListInput) ([]managementproviders.Option, error) {
	return s.service.List(r.Context(), input)
}

func NewManagementProvidersHandler(service *managementproviders.Service) http.Handler {
	return newManagementProvidersHandler(managementProviderOptionServiceAdapter{service: service})
}

func NewManagementProviderOptionsHandler(service *managementproviders.Service) http.Handler {
	return newManagementProviderOptionsHandler(managementProviderOptionServiceAdapter{service: service})
}

func NewManagementProviderDefinitionsHandler(service *managementproviders.Service) http.Handler {
	return newManagementProviderDefinitionsHandler(managementProviderOptionServiceAdapter{service: service})
}

func newManagementProvidersHandler(service managementProviderOptionService) http.Handler {
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
		providers, err := service.List(r, managementProviderListInput())
		if err != nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		writeData(w, http.StatusOK, providers)
	})
}

func newManagementProviderOptionsHandler(service managementProviderOptionService) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		options, err := service.SelectOptions(r)
		if err != nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		writeData(w, http.StatusOK, options)
	})
}

func newManagementProviderDefinitionsHandler(service managementProviderOptionService) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		definitions, err := service.Options(r, managementProviderOptionListInput(r))
		if err != nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		writeData(w, http.StatusOK, definitions)
	})
}

func managementProviderListInput() managementproviders.ListInput {
	return managementproviders.ListInput{
		SystemAccountID: "",
	}
}

func managementProviderOptionListInput(r *http.Request) managementproviders.OptionListInput {
	authContext, _ := ManagementAuthContextFromRequest(r)
	return managementproviders.OptionListInput{
		SystemAccountID: managementScopedSystemAccountID(authContext, r.URL.Query()),
	}
}

func managementScopedSystemAccountID(authContext managementauth.Context, values url.Values) string {
	if managementauth.IsAdminRole(authContext.Role) {
		systemAccountID := firstManagementQueryText(values, "systemAccountId")
		if systemAccountID != "" && systemAccountID != "all" {
			return systemAccountID
		}
	}
	return authContext.SystemAccountID
}
