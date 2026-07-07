package httpapi

import (
	"net/http"
	"net/url"

	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementproviders"
)

type managementProviderOptionService interface {
	Options(r *http.Request, input managementproviders.OptionListInput) ([]managementproviders.Option, error)
}

type managementProviderOptionServiceAdapter struct {
	service *managementproviders.Service
}

func (s managementProviderOptionServiceAdapter) Options(r *http.Request, input managementproviders.OptionListInput) ([]managementproviders.Option, error) {
	return s.service.Options(r.Context(), input)
}

func NewManagementProviderOptionsHandler(service *managementproviders.Service) http.Handler {
	return newManagementProviderOptionsHandler(managementProviderOptionServiceAdapter{service: service})
}

func newManagementProviderOptionsHandler(service managementProviderOptionService) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		input := managementProviderOptionListInput(r)
		options, err := service.Options(r, input)
		if err != nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		writeData(w, http.StatusOK, options)
	})
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
