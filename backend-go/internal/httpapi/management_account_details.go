package httpapi

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"

	"juhe-ai/backend-go/internal/modules/managementaccountdetails"
	"juhe-ai/backend-go/internal/modules/managementauth"
)

type managementAccountDetailScope int

const (
	managementAccountDetailScopeAdmin managementAccountDetailScope = iota
	managementAccountDetailScopeSelf
)

type managementAccountDetailService interface {
	Get(*http.Request, managementaccountdetails.Input, managementaccountdetails.Level) (map[string]any, bool, error)
	APIKeyRuntime(*http.Request, managementaccountdetails.Input) (managementaccountdetails.APIKeyRuntimeResponse, bool, error)
}

type managementAccountDetailServiceAdapter struct {
	service *managementaccountdetails.Service
}

func (s managementAccountDetailServiceAdapter) Get(
	r *http.Request,
	input managementaccountdetails.Input,
	level managementaccountdetails.Level,
) (map[string]any, bool, error) {
	return s.service.Get(r.Context(), input, level)
}

func (s managementAccountDetailServiceAdapter) APIKeyRuntime(
	r *http.Request,
	input managementaccountdetails.Input,
) (managementaccountdetails.APIKeyRuntimeResponse, bool, error) {
	return s.service.APIKeyRuntime(r.Context(), input)
}

func NewManagementAccountDetailHandler(service *managementaccountdetails.Service) http.Handler {
	return newManagementAccountDetailHandler(detailServiceFrom(service), managementAccountDetailScopeAdmin, managementaccountdetails.LevelBasic)
}

func NewManagementMyAccountDetailHandler(service *managementaccountdetails.Service) http.Handler {
	return newManagementAccountDetailHandler(detailServiceFrom(service), managementAccountDetailScopeSelf, managementaccountdetails.LevelBasic)
}

func NewManagementAccountEditBasicDetailHandler(service *managementaccountdetails.Service) http.Handler {
	return newManagementAccountDetailHandler(detailServiceFrom(service), managementAccountDetailScopeAdmin, managementaccountdetails.LevelEditBasic)
}

func NewManagementMyAccountEditBasicDetailHandler(service *managementaccountdetails.Service) http.Handler {
	return newManagementAccountDetailHandler(detailServiceFrom(service), managementAccountDetailScopeSelf, managementaccountdetails.LevelEditBasic)
}

func NewManagementAccountAdvancedDetailHandler(service *managementaccountdetails.Service) http.Handler {
	return newManagementAccountDetailHandler(detailServiceFrom(service), managementAccountDetailScopeAdmin, managementaccountdetails.LevelAdvanced)
}

func NewManagementMyAccountAdvancedDetailHandler(service *managementaccountdetails.Service) http.Handler {
	return newManagementAccountDetailHandler(detailServiceFrom(service), managementAccountDetailScopeSelf, managementaccountdetails.LevelAdvanced)
}

func NewManagementAccountAPIKeyRuntimeHandler(service *managementaccountdetails.Service) http.Handler {
	return newManagementAccountAPIKeyRuntimeHandler(detailServiceFrom(service), managementAccountDetailScopeAdmin)
}

func NewManagementMyAccountAPIKeyRuntimeHandler(service *managementaccountdetails.Service) http.Handler {
	return newManagementAccountAPIKeyRuntimeHandler(detailServiceFrom(service), managementAccountDetailScopeSelf)
}

func detailServiceFrom(service *managementaccountdetails.Service) managementAccountDetailService {
	if service == nil {
		return nil
	}
	return managementAccountDetailServiceAdapter{service: service}
}

func newManagementAccountDetailHandler(
	service managementAccountDetailService,
	scope managementAccountDetailScope,
	level managementaccountdetails.Level,
) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		input, allowed := managementAccountDetailInput(r, scope)
		if !allowed {
			writeMessageError(w, http.StatusForbidden, "需要管理员权限")
			return
		}
		if service == nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		result, found, err := service.Get(r, input, level)
		if errors.Is(err, managementaccountdetails.ErrCredentialsForbidden) {
			writeMessageError(w, http.StatusForbidden, "无权查看账户凭据")
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

func newManagementAccountAPIKeyRuntimeHandler(
	service managementAccountDetailService,
	scope managementAccountDetailScope,
) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		input, allowed := managementAccountDetailInput(r, scope)
		if !allowed {
			writeMessageError(w, http.StatusForbidden, "需要管理员权限")
			return
		}
		if service == nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		result, found, err := service.APIKeyRuntime(r, input)
		if errors.Is(err, managementaccountdetails.ErrRuntimeForbidden) {
			writeMessageError(w, http.StatusForbidden, "无权查看账户 API Key 运行明细")
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

func managementAccountDetailInput(r *http.Request, scope managementAccountDetailScope) (managementaccountdetails.Input, bool) {
	authContext, ok := ManagementAuthContextFromRequest(r)
	if !ok || strings.TrimSpace(authContext.SystemAccountID) == "" {
		return managementaccountdetails.Input{}, false
	}
	input := managementaccountdetails.Input{AccountID: chi.URLParam(r, "id")}
	switch scope {
	case managementAccountDetailScopeAdmin:
		if !managementauth.IsAdminRole(authContext.Role) {
			return managementaccountdetails.Input{}, false
		}
		input.SystemAccountID = managementDetailSystemAccountID(r.URL.Query())
		input.CanViewDisabledProxy = true
	case managementAccountDetailScopeSelf:
		input.SystemAccountID = authContext.SystemAccountID
	}
	return input, true
}

func managementDetailSystemAccountID(values url.Values) string {
	value := firstManagementQueryText(values, "systemAccountId")
	if value == "all" {
		return ""
	}
	return value
}
