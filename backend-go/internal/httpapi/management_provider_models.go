package httpapi

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"juhe-ai/backend-go/internal/modules/managementprovidermodels"
)

type managementProviderModelService interface {
	ModelOptions(r *http.Request, input managementprovidermodels.ModelOptionListInput) ([]managementprovidermodels.ModelOption, error)
	Models(r *http.Request, input managementprovidermodels.ModelListInput) ([]managementprovidermodels.ModelCatalogItem, error)
}

type managementProviderModelServiceAdapter struct {
	service *managementprovidermodels.Service
}

func (s managementProviderModelServiceAdapter) ModelOptions(r *http.Request, input managementprovidermodels.ModelOptionListInput) ([]managementprovidermodels.ModelOption, error) {
	return s.service.ModelOptions(r.Context(), input)
}

func (s managementProviderModelServiceAdapter) Models(r *http.Request, input managementprovidermodels.ModelListInput) ([]managementprovidermodels.ModelCatalogItem, error) {
	return s.service.Models(r.Context(), input)
}

func NewManagementProviderModelOptionsHandler(service *managementprovidermodels.Service) http.Handler {
	return newManagementProviderModelOptionsHandler(managementProviderModelServiceAdapter{service: service})
}

func NewManagementProviderModelsHandler(service *managementprovidermodels.Service) http.Handler {
	return newManagementProviderModelsHandler(managementProviderModelServiceAdapter{service: service})
}

func newManagementProviderModelOptionsHandler(service managementProviderModelService) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authContext, ok := ManagementAuthContextFromRequest(r)
		if !ok {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		options, err := service.ModelOptions(r, managementprovidermodels.ModelOptionListInput{
			SystemAccountID: managementScopedSystemAccountID(authContext, r.URL.Query()),
			Protocol:        firstManagementQueryText(r.URL.Query(), "protocol"),
		})
		if err != nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		writeData(w, http.StatusOK, options)
	})
}

func newManagementProviderModelsHandler(service managementProviderModelService) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authContext, ok := ManagementAuthContextFromRequest(r)
		if !ok {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		includeInactive, _ := managementBooleanQueryValue(r.URL.Query(), "includeInactive")
		includeUnpriced, _ := managementBooleanQueryValue(r.URL.Query(), "includeUnpriced")
		models, err := service.Models(r, managementprovidermodels.ModelListInput{
			ProviderCode:    chi.URLParam(r, "code"),
			SystemAccountID: managementScopedSystemAccountID(authContext, r.URL.Query()),
			IncludeInactive: includeInactive,
			IncludeUnpriced: includeUnpriced,
		})
		if err != nil {
			if errors.Is(err, managementprovidermodels.ErrProviderNotFound) {
				writeMessageError(w, http.StatusNotFound, "供应商不存在")
				return
			}
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		writeData(w, http.StatusOK, models)
	})
}
