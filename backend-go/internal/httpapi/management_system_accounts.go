package httpapi

import (
	"net/http"
	"net/url"
	"strings"

	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementsystemaccounts"
)

type managementSystemAccountOptionService interface {
	Options(r *http.Request, input managementsystemaccounts.OptionListInput) ([]managementsystemaccounts.Option, error)
}

type managementSystemAccountOptionServiceAdapter struct {
	service *managementsystemaccounts.Service
}

func (s managementSystemAccountOptionServiceAdapter) Options(r *http.Request, input managementsystemaccounts.OptionListInput) ([]managementsystemaccounts.Option, error) {
	return s.service.Options(r.Context(), input)
}

func NewManagementSystemAccountOptionsHandler(service *managementsystemaccounts.Service) http.Handler {
	return newManagementSystemAccountOptionsHandler(managementSystemAccountOptionServiceAdapter{service: service})
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

func parseManagementSystemAccountOptionListQuery(values url.Values) managementsystemaccounts.OptionListInput {
	return managementsystemaccounts.OptionListInput{
		IDs:     managementTextListQueryValue(values, "ids", 50),
		Keyword: firstManagementQueryText(values, "keyword"),
		Limit:   managementIntegerQueryValue(values, "limit"),
	}
}
