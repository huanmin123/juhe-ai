package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"juhe-ai/backend-go/internal/modules/managementaccountauthorizeddispatch"
	"juhe-ai/backend-go/internal/modules/managementauth"
)

type managementAccountAuthorizedDispatchScope int

const (
	managementAccountAuthorizedDispatchScopeAdmin managementAccountAuthorizedDispatchScope = iota
	managementAccountAuthorizedDispatchScopeSelf
)

type managementAccountAuthorizedDispatchService interface {
	Update(*http.Request, managementaccountauthorizeddispatch.Input) (managementaccountauthorizeddispatch.Result, error)
}

type managementAccountAuthorizedDispatchAdapter struct {
	service *managementaccountauthorizeddispatch.Service
}

func (a managementAccountAuthorizedDispatchAdapter) Update(r *http.Request, input managementaccountauthorizeddispatch.Input) (managementaccountauthorizeddispatch.Result, error) {
	return a.service.Update(r.Context(), input)
}

func NewManagementAccountAuthorizedDispatchHandler(service *managementaccountauthorizeddispatch.Service) http.Handler {
	return newManagementAccountAuthorizedDispatchHandler(managementAccountAuthorizedDispatchAdapter{service}, managementAccountAuthorizedDispatchScopeAdmin)
}

func NewManagementMyAccountAuthorizedDispatchHandler(service *managementaccountauthorizeddispatch.Service) http.Handler {
	return newManagementAccountAuthorizedDispatchHandler(managementAccountAuthorizedDispatchAdapter{service}, managementAccountAuthorizedDispatchScopeSelf)
}

func newManagementAccountAuthorizedDispatchHandler(service managementAccountAuthorizedDispatchService, scope managementAccountAuthorizedDispatchScope) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth, ok := ManagementAuthContextFromRequest(r)
		if !ok || strings.TrimSpace(auth.SystemAccountID) == "" {
			writeMessageError(w, 500, "服务器内部错误")
			return
		}
		if scope == managementAccountAuthorizedDispatchScopeAdmin && !managementauth.IsAdminRole(auth.Role) {
			writeMessageError(w, http.StatusForbidden, "需要管理员权限")
			return
		}
		payload, ok := decodeManagementAccountAuthorizedDispatch(w, r)
		if !ok {
			writeMessageError(w, http.StatusBadRequest, "授权账户调度参数无效")
			return
		}
		systemID := ""
		if scope == managementAccountAuthorizedDispatchScopeAdmin {
			values, exists := r.URL.Query()["systemAccountId"]
			if exists && (len(values) == 0 || strings.TrimSpace(values[0]) == "") {
				writeMessageError(w, 400, "查询参数不合法")
				return
			}
			if exists {
				systemID = strings.TrimSpace(values[0])
			}
		}
		payload.ActorSystemAccountID, payload.ActorRole = auth.SystemAccountID, auth.Role
		payload.SystemAccountID, payload.SelfOnly = systemID, scope == managementAccountAuthorizedDispatchScopeSelf
		payload.AccountID = chi.URLParam(r, "id")
		result, err := service.Update(r, payload)
		if err != nil {
			writeManagementAccountAuthorizedDispatchError(w, err)
			return
		}
		writeData(w, http.StatusOK, result.Account)
	})
}

func decodeManagementAccountAuthorizedDispatch(w http.ResponseWriter, r *http.Request) (managementaccountauthorizeddispatch.Input, bool) {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	var raw map[string]json.RawMessage
	if err := decoder.Decode(&raw); err != nil {
		return managementaccountauthorizeddispatch.Input{}, false
	}
	var extra struct{}
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return managementaccountauthorizeddispatch.Input{}, false
	}
	var out managementaccountauthorizeddispatch.Input
	for key, value := range raw {
		if string(value) == "null" {
			return out, false
		}
		switch key {
		case "status":
			var v string
			if json.Unmarshal(value, &v) != nil {
				return out, false
			}
			out.Status = &v
		case "priority":
			var v int
			if json.Unmarshal(value, &v) != nil {
				return out, false
			}
			out.Priority = &v
		case "superPriorityEnabled":
			var v bool
			if json.Unmarshal(value, &v) != nil {
				return out, false
			}
			out.SuperPriorityEnabled = &v
		case "fallbackEnabled":
			var v bool
			if json.Unmarshal(value, &v) != nil {
				return out, false
			}
			out.FallbackEnabled = &v
		case "clearFailureState":
			if json.Unmarshal(value, &out.ClearFailureState) != nil {
				return out, false
			}
		default:
			return out, false
		}
	}
	return out, true
}

func writeManagementAccountAuthorizedDispatchError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, managementaccountauthorizeddispatch.ErrInvalid), errors.Is(err, managementaccountauthorizeddispatch.ErrNotFound),
		errors.Is(err, managementaccountauthorizeddispatch.ErrPendingTest), errors.Is(err, managementaccountauthorizeddispatch.ErrExclusive),
		errors.Is(err, managementaccountauthorizeddispatch.ErrUnavailable):
		writeMessageError(w, http.StatusBadRequest, err.Error())
	default:
		writeMessageError(w, http.StatusInternalServerError, "更新授权账户调度设置失败")
	}
}
