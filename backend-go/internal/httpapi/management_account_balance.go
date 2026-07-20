package httpapi

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"

	"juhe-ai/backend-go/internal/modules/managementaccountbalance"
	"juhe-ai/backend-go/internal/modules/managementauth"
)

const managementAccountBalanceAdapterUnavailableMessage = "账户余额查询适配器未配置"

type managementAccountBalanceScope int

const (
	managementAccountBalanceScopeAdmin managementAccountBalanceScope = iota
	managementAccountBalanceScopeSelf
)

type managementAccountBalanceService interface {
	Get(*http.Request, managementaccountbalance.Input) (managementaccountbalance.Snapshot, bool, error)
	Refresh(*http.Request, managementaccountbalance.Input) (managementaccountbalance.Snapshot, bool, error)
}

type managementAccountBalanceServiceAdapter struct {
	service *managementaccountbalance.Service
}

func (s managementAccountBalanceServiceAdapter) Get(r *http.Request, input managementaccountbalance.Input) (managementaccountbalance.Snapshot, bool, error) {
	return s.service.Get(r.Context(), input)
}

func (s managementAccountBalanceServiceAdapter) Refresh(r *http.Request, input managementaccountbalance.Input) (managementaccountbalance.Snapshot, bool, error) {
	return s.service.Refresh(r.Context(), input)
}

func NewManagementAccountBalanceHandler(service *managementaccountbalance.Service) http.Handler {
	return newManagementAccountBalanceHandler(managementAccountBalanceServiceAdapter{service: service}, managementAccountBalanceScopeAdmin)
}

func NewManagementMyAccountBalanceHandler(service *managementaccountbalance.Service) http.Handler {
	return newManagementAccountBalanceHandler(managementAccountBalanceServiceAdapter{service: service}, managementAccountBalanceScopeSelf)
}

func NewManagementAccountBalanceRefreshHandler(service *managementaccountbalance.Service) http.Handler {
	return newManagementAccountBalanceRefreshHandler(managementAccountBalanceServiceAdapter{service: service}, managementAccountBalanceScopeAdmin)
}

func NewManagementMyAccountBalanceRefreshHandler(service *managementaccountbalance.Service) http.Handler {
	return newManagementAccountBalanceRefreshHandler(managementAccountBalanceServiceAdapter{service: service}, managementAccountBalanceScopeSelf)
}

func newManagementAccountBalanceHandler(service managementAccountBalanceService, scope managementAccountBalanceScope) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		input, allowed := managementAccountBalanceInput(r, scope)
		if !allowed {
			writeMessageError(w, http.StatusForbidden, "需要管理员权限")
			return
		}
		if scope == managementAccountBalanceScopeAdmin && !managementAccountBalanceSystemAccountIDValid(r.URL.Query()) {
			writeMessageError(w, http.StatusBadRequest, "查询参数不合法")
			return
		}
		if service == nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		snapshot, found, err := service.Get(r, input)
		if err != nil {
			writeManagementAccountBalanceError(w, err)
			return
		}
		if !found {
			writeMessageError(w, http.StatusNotFound, "账户余额快照不存在")
			return
		}
		writeData(w, http.StatusOK, snapshot)
	})
}

func newManagementAccountBalanceRefreshHandler(service managementAccountBalanceService, scope managementAccountBalanceScope) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		input, allowed := managementAccountBalanceInput(r, scope)
		if !allowed {
			writeMessageError(w, http.StatusForbidden, "需要管理员权限")
			return
		}
		if scope == managementAccountBalanceScopeAdmin && !managementAccountBalanceSystemAccountIDValid(r.URL.Query()) {
			writeMessageError(w, http.StatusBadRequest, "查询参数不合法")
			return
		}
		if service == nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		snapshot, found, err := service.Refresh(r, input)
		if errors.Is(err, managementaccountbalance.ErrBalanceQueryMissing) {
			writeMessageError(w, http.StatusServiceUnavailable, managementAccountBalanceAdapterUnavailableMessage)
			return
		}
		if err != nil {
			writeManagementAccountBalanceError(w, err)
			return
		}
		if !found {
			writeMessageError(w, http.StatusNotFound, "账户不存在或未开启余额查询")
			return
		}
		writeData(w, http.StatusOK, snapshot)
	})
}

func managementAccountBalanceInput(r *http.Request, scope managementAccountBalanceScope) (managementaccountbalance.Input, bool) {
	authContext, ok := ManagementAuthContextFromRequest(r)
	if !ok || strings.TrimSpace(authContext.SystemAccountID) == "" {
		return managementaccountbalance.Input{}, false
	}
	input := managementaccountbalance.Input{AccountID: chi.URLParam(r, "id")}
	switch scope {
	case managementAccountBalanceScopeAdmin:
		if !managementauth.IsAdminRole(authContext.Role) {
			return managementaccountbalance.Input{}, false
		}
		input.SystemAccountID = managementAccountBalanceSystemAccountID(r.URL.Query())
	case managementAccountBalanceScopeSelf:
		input.SystemAccountID = authContext.SystemAccountID
	}
	return input, true
}

func managementAccountBalanceSystemAccountID(values url.Values) string {
	value := strings.TrimSpace(values.Get("systemAccountId"))
	if value == "all" {
		return ""
	}
	return value
}

func managementAccountBalanceSystemAccountIDValid(values url.Values) bool {
	items, exists := values["systemAccountId"]
	if !exists {
		return true
	}
	return len(items) == 1 && strings.TrimSpace(items[0]) != ""
}

func writeManagementAccountBalanceError(w http.ResponseWriter, err error) {
	if errors.Is(err, managementaccountbalance.ErrBalanceQueryMissing) {
		writeMessageError(w, http.StatusServiceUnavailable, managementAccountBalanceAdapterUnavailableMessage)
		return
	}
	writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
}
