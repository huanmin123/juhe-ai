package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"juhe-ai/backend-go/internal/modules/managementaccountbalance"
	"juhe-ai/backend-go/internal/modules/managementaccountdraft"
	"juhe-ai/backend-go/internal/modules/managementaccounttestdispatch"
	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/store/port"
)

type managementAccountDraftScope int

const (
	managementAccountDraftScopeAdmin managementAccountDraftScope = iota
	managementAccountDraftScopeSelf
)

type managementAccountDraftDispatchService interface {
	DispatchDraft(context.Context, managementaccounttestdispatch.DraftInput) (managementaccounttestdispatch.Task, error)
}

type managementAccountBalanceDraftService interface {
	TestDraft(context.Context, managementaccountbalance.DraftInput) (managementaccountbalance.Snapshot, error)
}

type managementAccountDraftTestBody struct {
	Account          *managementaccountdraft.Account `json:"account"`
	TestEndpointMode string                          `json:"testEndpointMode,omitempty"`
	Prompt           string                          `json:"prompt,omitempty"`
	TestSessionID    *string                         `json:"testSessionId,omitempty"`
}

type managementAccountBalanceDraftTestBody struct {
	Account            *managementaccountdraft.Account            `json:"account"`
	BalanceQueryConfig *managementaccountdraft.BalanceQueryConfig `json:"balanceQueryConfig"`
}

func NewManagementAccountDraftTestHandler(service *managementaccounttestdispatch.Service) http.Handler {
	return newManagementAccountDraftTestHandler(service, managementAccountDraftScopeAdmin)
}

func NewManagementMyAccountDraftTestHandler(service *managementaccounttestdispatch.Service) http.Handler {
	return newManagementAccountDraftTestHandler(service, managementAccountDraftScopeSelf)
}

func NewManagementAccountBalanceDraftTestHandler(service *managementaccountbalance.Service) http.Handler {
	return newManagementAccountBalanceDraftTestHandler(service, managementAccountDraftScopeAdmin)
}

func NewManagementMyAccountBalanceDraftTestHandler(service *managementaccountbalance.Service) http.Handler {
	return newManagementAccountBalanceDraftTestHandler(service, managementAccountDraftScopeSelf)
}

func newManagementAccountDraftTestHandler(service managementAccountDraftDispatchService, scope managementAccountDraftScope) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		access, status, message := managementAccountDraftAccess(r, scope)
		if status != 0 {
			writeMessageError(w, status, message)
			return
		}
		if service == nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		var body managementAccountDraftTestBody
		if !decodeManagementAccountDraftBody(w, r, &body) || body.Account == nil || body.TestSessionID != nil && strings.TrimSpace(*body.TestSessionID) == "" {
			writeMessageError(w, http.StatusBadRequest, managementaccountdraft.ErrInvalid.Error())
			return
		}
		sessionID := ""
		if body.TestSessionID != nil {
			sessionID = strings.TrimSpace(*body.TestSessionID)
		}
		task, err := service.DispatchDraft(r.Context(), managementaccounttestdispatch.DraftInput{
			SessionID: sessionID, TestEndpointMode: strings.TrimSpace(body.TestEndpointMode), Account: *body.Account, Access: access,
		})
		if err != nil {
			writeManagementAccountDraftDispatchError(w, err)
			return
		}
		writeData(w, http.StatusAccepted, task)
	})
}

func newManagementAccountBalanceDraftTestHandler(service managementAccountBalanceDraftService, scope managementAccountDraftScope) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		access, status, message := managementAccountDraftAccess(r, scope)
		if status != 0 {
			writeMessageError(w, status, message)
			return
		}
		if service == nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		var body managementAccountBalanceDraftTestBody
		if !decodeManagementAccountDraftBody(w, r, &body) || body.Account == nil || body.BalanceQueryConfig == nil {
			writeMessageError(w, http.StatusBadRequest, "余额查询测试参数无效")
			return
		}
		snapshot, err := service.TestDraft(r.Context(), managementaccountbalance.DraftInput{
			Account: *body.Account, Config: *body.BalanceQueryConfig, Access: access,
		})
		if err != nil {
			writeManagementAccountBalanceDraftError(w, err)
			return
		}
		writeData(w, http.StatusOK, snapshot)
	})
}

func managementAccountDraftAccess(r *http.Request, scope managementAccountDraftScope) (port.ManagementAccountTestAccess, int, string) {
	auth, ok := ManagementAuthContextFromRequest(r)
	if !ok || strings.TrimSpace(auth.SystemAccountID) == "" {
		return port.ManagementAccountTestAccess{}, http.StatusInternalServerError, "服务器内部错误"
	}
	filter := strings.TrimSpace(auth.SystemAccountID)
	if scope == managementAccountDraftScopeAdmin {
		if !managementauth.IsAdminRole(auth.Role) {
			return port.ManagementAccountTestAccess{}, http.StatusForbidden, "需要管理员权限"
		}
		if !managementAccountBalanceSystemAccountIDValid(r.URL.Query()) {
			return port.ManagementAccountTestAccess{}, http.StatusBadRequest, "查询参数不合法"
		}
		filter = managementAccountBalanceSystemAccountID(r.URL.Query())
	}
	return port.ManagementAccountTestAccess{
		ActorSystemAccountID: auth.SystemAccountID, ActorRole: auth.Role, FilterSystemAccountID: filter,
	}, 0, ""
}

func decodeManagementAccountDraftBody(w http.ResponseWriter, r *http.Request, destination any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(destination); err != nil {
		return false
	}
	var extra struct{}
	return errors.Is(decoder.Decode(&extra), io.EOF)
}

func writeManagementAccountDraftDispatchError(w http.ResponseWriter, err error) {
	if errors.Is(err, managementaccounttestdispatch.ErrEnqueueFailed) {
		writeMessageError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	if errors.Is(err, managementaccountdraft.ErrInvalid) || errors.Is(err, managementaccountdraft.ErrGroupInvalid) || errors.Is(err, managementaccountdraft.ErrProviderInvalid) || errors.Is(err, managementaccounttestdispatch.ErrInvalidInput) {
		writeMessageError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
}

func writeManagementAccountBalanceDraftError(w http.ResponseWriter, err error) {
	if errors.Is(err, managementaccountbalance.ErrBalanceQueryMissing) {
		writeMessageError(w, http.StatusServiceUnavailable, managementAccountBalanceAdapterUnavailableMessage)
		return
	}
	if errors.Is(err, managementaccountdraft.ErrInvalid) || errors.Is(err, managementaccountdraft.ErrGroupInvalid) || errors.Is(err, managementaccountdraft.ErrProviderInvalid) || errors.Is(err, managementaccountdraft.ErrBalanceUnsupported) || errors.Is(err, managementaccountdraft.ErrBalanceConfigInvalid) {
		writeMessageError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeMessageError(w, http.StatusBadGateway, "余额查询测试失败")
}
