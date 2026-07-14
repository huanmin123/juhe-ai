package httpapi

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"

	"juhe-ai/backend-go/internal/modules/managementaccounttestoptions"
	"juhe-ai/backend-go/internal/modules/managementauth"
)

type managementAccountTestOptionsScope int

const (
	managementAccountTestOptionsScopeAdmin managementAccountTestOptionsScope = iota
	managementAccountTestOptionsScopeSelf
)

type managementAccountTestOptionsService interface {
	Get(r *http.Request, input managementaccounttestoptions.Input) (managementaccounttestoptions.Result, bool, error)
}

type managementAccountTestOptionsServiceAdapter struct {
	service *managementaccounttestoptions.Service
}

func (s managementAccountTestOptionsServiceAdapter) Get(
	r *http.Request,
	input managementaccounttestoptions.Input,
) (managementaccounttestoptions.Result, bool, error) {
	return s.service.Get(r.Context(), input)
}

func NewManagementAccountTestOptionsHandler(service *managementaccounttestoptions.Service) http.Handler {
	return newManagementAccountTestOptionsHandler(
		managementAccountTestOptionsServiceFrom(service),
		managementAccountTestOptionsScopeAdmin,
	)
}

func NewManagementMyAccountTestOptionsHandler(service *managementaccounttestoptions.Service) http.Handler {
	return newManagementAccountTestOptionsHandler(
		managementAccountTestOptionsServiceFrom(service),
		managementAccountTestOptionsScopeSelf,
	)
}

func managementAccountTestOptionsServiceFrom(service *managementaccounttestoptions.Service) managementAccountTestOptionsService {
	if service == nil {
		return nil
	}
	return managementAccountTestOptionsServiceAdapter{service: service}
}

func newManagementAccountTestOptionsHandler(
	service managementAccountTestOptionsService,
	scope managementAccountTestOptionsScope,
) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authContext, ok := ManagementAuthContextFromRequest(r)
		if !ok || strings.TrimSpace(authContext.SystemAccountID) == "" {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		input, allowed := managementAccountTestOptionsInput(
			authContext,
			r.URL.Query(),
			scope,
			chi.URLParam(r, "id"),
		)
		if !allowed {
			writeMessageError(w, http.StatusForbidden, "需要管理员权限")
			return
		}
		if service == nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}

		result, found, err := service.Get(r, input)
		if message, validation := managementaccounttestoptions.ValidationMessage(err); validation {
			writeMessageError(w, http.StatusBadRequest, message)
			return
		}
		if err != nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		if !found {
			writeMessageError(w, http.StatusNotFound, "账户不存在")
			return
		}
		writeData(w, http.StatusOK, result)
	})
}

func managementAccountTestOptionsInput(
	authContext managementauth.Context,
	values url.Values,
	scope managementAccountTestOptionsScope,
	accountID string,
) (managementaccounttestoptions.Input, bool) {
	input := managementaccounttestoptions.Input{AccountID: accountID}
	switch scope {
	case managementAccountTestOptionsScopeAdmin:
		if !managementauth.IsAdminRole(authContext.Role) {
			return managementaccounttestoptions.Input{}, false
		}
		systemAccountID := firstManagementQueryText(values, "systemAccountId")
		if systemAccountID != "" && systemAccountID != "all" {
			input.SystemAccountID = systemAccountID
		}
	case managementAccountTestOptionsScopeSelf:
		input.SystemAccountID = authContext.SystemAccountID
	}
	return input, true
}
