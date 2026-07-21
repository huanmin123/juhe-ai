package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"juhe-ai/backend-go/internal/modules/managementaccounttestdispatch"
	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/store/port"
)

type managementAccountTestDispatchScope int

const (
	managementAccountTestDispatchScopeAdmin managementAccountTestDispatchScope = iota
	managementAccountTestDispatchScopeSelf
)

func NewManagementAccountTestDispatchHandler(s *managementaccounttestdispatch.Service) http.Handler {
	return newManagementAccountTestDispatchHandler(s, managementAccountTestDispatchScopeAdmin)
}
func NewManagementMyAccountTestDispatchHandler(s *managementaccounttestdispatch.Service) http.Handler {
	return newManagementAccountTestDispatchHandler(s, managementAccountTestDispatchScopeSelf)
}

func newManagementAccountTestDispatchHandler(service *managementaccounttestdispatch.Service, scope managementAccountTestDispatchScope) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth, ok := ManagementAuthContextFromRequest(r)
		if !ok || strings.TrimSpace(auth.SystemAccountID) == "" {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		if scope == managementAccountTestDispatchScopeAdmin && !managementauth.IsAdminRole(auth.Role) {
			writeMessageError(w, http.StatusForbidden, "需要管理员权限")
			return
		}
		var body struct {
			SessionID        string         `json:"testSessionId"`
			Model            string         `json:"model"`
			TestEndpointMode string         `json:"testEndpointMode"`
			DraftAccount     map[string]any `json:"account"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&body); err != nil {
			writeMessageError(w, http.StatusBadRequest, "账户测试参数无效")
			return
		}
		var extra struct{}
		if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
			writeMessageError(w, http.StatusBadRequest, "账户测试参数无效")
			return
		}
		filter := ""
		if scope == managementAccountTestDispatchScopeSelf {
			filter = auth.SystemAccountID
		} else if value := strings.TrimSpace(r.URL.Query().Get("systemAccountId")); value != "" && value != "all" {
			filter = value
		}
		task, err := service.Dispatch(r.Context(), managementaccounttestdispatch.Input{AccountID: chi.URLParam(r, "id"), SessionID: body.SessionID, Model: body.Model, TestEndpointMode: body.TestEndpointMode, DraftAccount: body.DraftAccount, Access: port.ManagementAccountTestAccess{ActorSystemAccountID: auth.SystemAccountID, ActorRole: auth.Role, FilterSystemAccountID: filter}})
		if err != nil {
			status := http.StatusBadRequest
			if errors.Is(err, managementaccounttestdispatch.ErrEnqueueFailed) {
				status = http.StatusServiceUnavailable
			}
			writeMessageError(w, status, err.Error())
			return
		}
		writeData(w, http.StatusAccepted, task)
	})
}
