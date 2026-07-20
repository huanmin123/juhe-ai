package httpapi

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"juhe-ai/backend-go/internal/modules/managementaccounttestsession"
	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/store/port"
)

type managementAccountTestScope int

const (
	managementAccountTestScopeAdmin managementAccountTestScope = iota
	managementAccountTestScopeSelf
)

type accountTestSessionService interface {
	Create(*http.Request, port.ManagementAccountTestAccess) (port.ManagementAccountTestSession, error)
	Heartbeat(*http.Request, string, port.ManagementAccountTestAccess) (port.ManagementAccountTestSession, bool, error)
	Complete(*http.Request, string, port.ManagementAccountTestAccess) (port.ManagementAccountTestSession, bool, error)
	CancelSession(*http.Request, string, port.ManagementAccountTestAccess) (port.ManagementAccountTestSession, bool, error)
	CancelTask(*http.Request, string, port.ManagementAccountTestAccess) (port.ManagementAccountTestTask, bool, error)
}
type accountTestSessionAdapter struct {
	service *managementaccounttestsession.Service
}

func (a accountTestSessionAdapter) Create(r *http.Request, x port.ManagementAccountTestAccess) (port.ManagementAccountTestSession, error) {
	return a.service.Create(r.Context(), x)
}
func (a accountTestSessionAdapter) Heartbeat(r *http.Request, id string, x port.ManagementAccountTestAccess) (port.ManagementAccountTestSession, bool, error) {
	return a.service.Heartbeat(r.Context(), id, x)
}
func (a accountTestSessionAdapter) Complete(r *http.Request, id string, x port.ManagementAccountTestAccess) (port.ManagementAccountTestSession, bool, error) {
	return a.service.Complete(r.Context(), id, x)
}
func (a accountTestSessionAdapter) CancelSession(r *http.Request, id string, x port.ManagementAccountTestAccess) (port.ManagementAccountTestSession, bool, error) {
	return a.service.CancelSession(r.Context(), id, x)
}
func (a accountTestSessionAdapter) CancelTask(r *http.Request, id string, x port.ManagementAccountTestAccess) (port.ManagementAccountTestTask, bool, error) {
	return a.service.CancelTask(r.Context(), id, x)
}

func NewManagementAccountTestSessionCreateHandler(s *managementaccounttestsession.Service) http.Handler {
	return newAccountTestSessionCreateHandler(accountTestSessionAdapter{s}, managementAccountTestScopeAdmin)
}
func NewManagementMyAccountTestSessionCreateHandler(s *managementaccounttestsession.Service) http.Handler {
	return newAccountTestSessionCreateHandler(accountTestSessionAdapter{s}, managementAccountTestScopeSelf)
}
func NewManagementAccountTestSessionHeartbeatHandler(s *managementaccounttestsession.Service) http.Handler {
	return newAccountTestSessionMutationHandler(accountTestSessionAdapter{s}, managementAccountTestScopeAdmin, "heartbeat")
}
func NewManagementMyAccountTestSessionHeartbeatHandler(s *managementaccounttestsession.Service) http.Handler {
	return newAccountTestSessionMutationHandler(accountTestSessionAdapter{s}, managementAccountTestScopeSelf, "heartbeat")
}
func NewManagementAccountTestSessionCompleteHandler(s *managementaccounttestsession.Service) http.Handler {
	return newAccountTestSessionMutationHandler(accountTestSessionAdapter{s}, managementAccountTestScopeAdmin, "complete")
}
func NewManagementMyAccountTestSessionCompleteHandler(s *managementaccounttestsession.Service) http.Handler {
	return newAccountTestSessionMutationHandler(accountTestSessionAdapter{s}, managementAccountTestScopeSelf, "complete")
}
func NewManagementAccountTestSessionCancelHandler(s *managementaccounttestsession.Service) http.Handler {
	return newAccountTestSessionMutationHandler(accountTestSessionAdapter{s}, managementAccountTestScopeAdmin, "cancel")
}
func NewManagementMyAccountTestSessionCancelHandler(s *managementaccounttestsession.Service) http.Handler {
	return newAccountTestSessionMutationHandler(accountTestSessionAdapter{s}, managementAccountTestScopeSelf, "cancel")
}
func NewManagementAccountTestTaskCancelHandler(s *managementaccounttestsession.Service) http.Handler {
	return newAccountTestTaskCancelHandler(accountTestSessionAdapter{s}, managementAccountTestScopeAdmin)
}
func NewManagementMyAccountTestTaskCancelHandler(s *managementaccounttestsession.Service) http.Handler {
	return newAccountTestTaskCancelHandler(accountTestSessionAdapter{s}, managementAccountTestScopeSelf)
}

func newAccountTestSessionCreateHandler(service accountTestSessionService, scope managementAccountTestScope) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		access, ok := accountTestAccess(w, r, scope)
		if !ok {
			return
		}
		result, err := service.Create(r, access)
		if err != nil {
			writeMessageError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeData(w, http.StatusCreated, result)
	})
}
func newAccountTestSessionMutationHandler(service accountTestSessionService, scope managementAccountTestScope, action string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		access, ok := accountTestAccess(w, r, scope)
		if !ok {
			return
		}
		id := chi.URLParam(r, "sessionId")
		var result port.ManagementAccountTestSession
		var found bool
		var err error
		switch action {
		case "heartbeat":
			result, found, err = service.Heartbeat(r, id, access)
		case "complete":
			result, found, err = service.Complete(r, id, access)
		default:
			result, found, err = service.CancelSession(r, id, access)
		}
		if err != nil {
			writeMessageError(w, http.StatusBadRequest, err.Error())
			return
		}
		if !found {
			writeMessageError(w, http.StatusNotFound, "账户测试会话不存在")
			return
		}
		writeData(w, http.StatusOK, result)
	})
}
func newAccountTestTaskCancelHandler(service accountTestSessionService, scope managementAccountTestScope) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		access, ok := accountTestAccess(w, r, scope)
		if !ok {
			return
		}
		result, found, err := service.CancelTask(r, chi.URLParam(r, "taskId"), access)
		if err != nil {
			writeMessageError(w, http.StatusBadRequest, err.Error())
			return
		}
		if !found {
			writeMessageError(w, http.StatusNotFound, "账户测试任务不存在")
			return
		}
		writeData(w, http.StatusOK, result)
	})
}

func accountTestAccess(w http.ResponseWriter, r *http.Request, scope managementAccountTestScope) (port.ManagementAccountTestAccess, bool) {
	auth, ok := ManagementAuthContextFromRequest(r)
	if !ok || strings.TrimSpace(auth.SystemAccountID) == "" {
		writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
		return port.ManagementAccountTestAccess{}, false
	}
	if scope == managementAccountTestScopeAdmin && !managementauth.IsAdminRole(auth.Role) {
		writeMessageError(w, http.StatusForbidden, "需要管理员权限")
		return port.ManagementAccountTestAccess{}, false
	}
	filter := ""
	if scope == managementAccountTestScopeAdmin {
		if values, exists := r.URL.Query()["systemAccountId"]; exists {
			if len(values) != 1 || strings.TrimSpace(values[0]) == "" {
				writeMessageError(w, http.StatusBadRequest, "查询参数不合法")
				return port.ManagementAccountTestAccess{}, false
			}
			filter = strings.TrimSpace(values[0])
			if filter == "all" {
				filter = ""
			}
		}
	}
	return port.ManagementAccountTestAccess{ActorSystemAccountID: auth.SystemAccountID, ActorRole: auth.Role, FilterSystemAccountID: filter}, true
}
