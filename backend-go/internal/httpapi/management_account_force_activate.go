package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"juhe-ai/backend-go/internal/modules/managementaccountforceactivate"
	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/store/port"
)

type managementAccountForceActivateScope int

const (
	managementAccountForceActivateScopeAdmin managementAccountForceActivateScope = iota
	managementAccountForceActivateScopeSelf
)

type managementAccountForceActivateService interface {
	ForceActivate(*http.Request, managementaccountforceactivate.Input) (managementaccountforceactivate.Result, error)
}

type managementAccountForceActivateServiceAdapter struct {
	service *managementaccountforceactivate.Service
}

func (s managementAccountForceActivateServiceAdapter) ForceActivate(r *http.Request, input managementaccountforceactivate.Input) (managementaccountforceactivate.Result, error) {
	return s.service.ForceActivate(r.Context(), input)
}

func NewManagementAccountForceActivateHandler(service *managementaccountforceactivate.Service) http.Handler {
	return newManagementAccountForceActivateHandler(managementAccountForceActivateServiceAdapter{service: service}, managementAccountForceActivateScopeAdmin)
}

func NewManagementMyAccountForceActivateHandler(service *managementaccountforceactivate.Service) http.Handler {
	return newManagementAccountForceActivateHandler(managementAccountForceActivateServiceAdapter{service: service}, managementAccountForceActivateScopeSelf)
}

func NewManagementAccountForceActivateHandlerWithOperationLog(service *managementaccountforceactivate.Service, opts ManagementOperationLogOptions) http.Handler {
	return newManagementAccountForceActivateHandler(managementAccountForceActivateServiceAdapter{service: service}, managementAccountForceActivateScopeAdmin, newManagementOperationLogOptions(opts))
}

func NewManagementMyAccountForceActivateHandlerWithOperationLog(service *managementaccountforceactivate.Service, opts ManagementOperationLogOptions) http.Handler {
	return newManagementAccountForceActivateHandler(managementAccountForceActivateServiceAdapter{service: service}, managementAccountForceActivateScopeSelf, newManagementOperationLogOptions(opts))
}

func newManagementAccountForceActivateHandler(service managementAccountForceActivateService, scope managementAccountForceActivateScope, logOptions ...managementOperationLogOptions) http.Handler {
	operationLogs := effectiveManagementOperationLogOptions(logOptions)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authContext, ok := ManagementAuthContextFromRequest(r)
		if !ok || strings.TrimSpace(authContext.SystemAccountID) == "" {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		if scope == managementAccountForceActivateScopeAdmin && !managementauth.IsAdminRole(authContext.Role) {
			writeMessageError(w, http.StatusForbidden, "需要管理员权限")
			return
		}
		systemAccountID, valid := managementAccountForceActivateSystemAccountID(r, scope, authContext.SystemAccountID)
		if !valid {
			writeMessageError(w, http.StatusBadRequest, "查询参数不合法")
			return
		}
		var payload struct {
			Acknowledged bool `json:"acknowledgedAccountAvailable"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&payload); err != nil {
			writeMessageError(w, http.StatusBadRequest, "人工恢复参数无效")
			return
		}
		var extra struct{}
		if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
			writeMessageError(w, http.StatusBadRequest, "人工恢复参数无效")
			return
		}
		result, err := service.ForceActivate(r, managementaccountforceactivate.Input{
			AccountID:       chi.URLParam(r, "id"),
			SystemAccountID: systemAccountID,
			Acknowledged:    payload.Acknowledged,
		})
		if err != nil {
			writeManagementAccountForceActivateError(w, err)
			return
		}
		recordAccountForceActivateOperationLog(r, authContext, scope, result, operationLogs)
		writeData(w, http.StatusOK, result.After)
	})
}

func managementAccountForceActivateSystemAccountID(r *http.Request, scope managementAccountForceActivateScope, actorSystemAccountID string) (string, bool) {
	if scope == managementAccountForceActivateScopeSelf {
		return strings.TrimSpace(actorSystemAccountID), true
	}
	values, exists := r.URL.Query()["systemAccountId"]
	if !exists {
		return "", true
	}
	if len(values) != 1 || strings.TrimSpace(values[0]) == "" {
		return "", false
	}
	value := strings.TrimSpace(values[0])
	if value == "all" {
		return "", true
	}
	return value, true
}

func writeManagementAccountForceActivateError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, managementaccountforceactivate.ErrConfirmation):
		writeMessageError(w, http.StatusBadRequest, "请先确认账户当前可用并接受人工恢复风险")
	case errors.Is(err, managementaccountforceactivate.ErrNotFound):
		writeMessageError(w, http.StatusNotFound, "账户不存在")
	case errors.Is(err, managementaccountforceactivate.ErrAuthorized):
		writeMessageError(w, http.StatusBadRequest, "授权账户不能人工恢复来源账户状态")
	case errors.Is(err, managementaccountforceactivate.ErrInvalidStatus):
		writeMessageError(w, http.StatusConflict, "只有待检查账户可以人工恢复正常")
	case errors.Is(err, managementaccountforceactivate.ErrStateChanged):
		writeMessageError(w, http.StatusConflict, "账户状态已变化，请刷新后重试")
	default:
		writeMessageError(w, http.StatusBadRequest, err.Error())
	}
}

func recordAccountForceActivateOperationLog(r *http.Request, authContext managementauth.Context, scope managementAccountForceActivateScope, result managementaccountforceactivate.Result, opts managementOperationLogOptions) {
	if opts.client == nil {
		return
	}
	now := opts.now
	if now == nil {
		now = time.Now
	}
	newLogID := opts.newLogID
	if newLogID == nil {
		newLogID = func() string { return "oplog_" + strings.ReplaceAll(uuid.NewString(), "-", "") }
	}
	mode := "self"
	if scope == managementAccountForceActivateScopeAdmin {
		mode = "admin"
	}
	accountID := forceActivateMapText(result.After, "id")
	accountName := forceActivateMapText(result.After, "name")
	statusCode := http.StatusOK
	input := port.OperationLogInput{
		ID: newLogID(), TraceID: requestIDFromContext(r.Context()),
		ActorSystemAccountID: authContext.SystemAccountID, ActorUsername: authContext.Username,
		ActorDisplayName: authContext.DisplayName, ActorRole: authContext.Role,
		OperationScopeSystemAccountID: result.OwnerSystemID, Mode: mode,
		Module: "accounts", Action: "force_activate", OperationKey: "accounts.force_activate_pending",
		ResourceType: "account", ResourceID: accountID, ResourceName: accountName,
		Summary: "人工恢复待检查 AI 账户：" + accountName, DetailLevel: "full", VisibilityScope: "targeted",
		Changes: []port.OperationLogChange{
			{Field: "status", Label: "状态", Before: result.BeforeStatus, After: result.AfterStatus},
			{Field: "acknowledgedAccountAvailable", Label: "确认账户当前可用", Before: false, After: true},
		},
		Method: r.Method, Path: r.URL.Path, StatusCode: &statusCode,
		ClientIP: opts.clientIP.FromRequest(r), UserAgent: r.UserAgent(),
		Viewers:   []port.OperationLogViewerInput{{SystemAccountID: result.OwnerSystemID, VisibilityReason: "resource_owner", DetailLevel: "full"}},
		CreatedAt: now().UTC(),
	}
	enqueueManagementOperationLog(r.Context(), opts, input)
}

func forceActivateMapText(value map[string]any, key string) string {
	text, _ := value[key].(string)
	return strings.TrimSpace(text)
}
