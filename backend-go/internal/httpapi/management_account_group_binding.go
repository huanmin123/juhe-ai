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

	"juhe-ai/backend-go/internal/modules/managementaccountgroupbinding"
	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/store/port"
)

type managementAccountGroupBindingScope int

const (
	managementAccountGroupBindingScopeAdmin managementAccountGroupBindingScope = iota
	managementAccountGroupBindingScopeSelf
)

type managementAccountGroupBindingService interface {
	Bind(r *http.Request, input managementaccountgroupbinding.BindInput) (managementaccountgroupbinding.BindResult, error)
}

type managementAccountGroupBindingServiceAdapter struct {
	service *managementaccountgroupbinding.Service
}

func (s managementAccountGroupBindingServiceAdapter) Bind(r *http.Request, input managementaccountgroupbinding.BindInput) (managementaccountgroupbinding.BindResult, error) {
	return s.service.Bind(r.Context(), input)
}

func NewManagementAccountGroupBindingHandler(service *managementaccountgroupbinding.Service) http.Handler {
	return newManagementAccountGroupBindingHandler(managementAccountGroupBindingServiceAdapter{service: service}, managementAccountGroupBindingScopeAdmin)
}

func NewManagementMyAccountGroupBindingHandler(service *managementaccountgroupbinding.Service) http.Handler {
	return newManagementAccountGroupBindingHandler(managementAccountGroupBindingServiceAdapter{service: service}, managementAccountGroupBindingScopeSelf)
}

func NewManagementAccountGroupBindingHandlerWithOperationLog(service *managementaccountgroupbinding.Service, opts ManagementOperationLogOptions) http.Handler {
	return newManagementAccountGroupBindingHandler(managementAccountGroupBindingServiceAdapter{service: service}, managementAccountGroupBindingScopeAdmin, newManagementOperationLogOptions(opts))
}

func NewManagementMyAccountGroupBindingHandlerWithOperationLog(service *managementaccountgroupbinding.Service, opts ManagementOperationLogOptions) http.Handler {
	return newManagementAccountGroupBindingHandler(managementAccountGroupBindingServiceAdapter{service: service}, managementAccountGroupBindingScopeSelf, newManagementOperationLogOptions(opts))
}

func newManagementAccountGroupBindingHandler(service managementAccountGroupBindingService, scope managementAccountGroupBindingScope, logOptions ...managementOperationLogOptions) http.Handler {
	operationLogs := effectiveManagementOperationLogOptions(logOptions)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authContext, ok := ManagementAuthContextFromRequest(r)
		if !ok || strings.TrimSpace(authContext.SystemAccountID) == "" {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		if scope == managementAccountGroupBindingScopeAdmin && !managementauth.IsAdminRole(authContext.Role) {
			writeMessageError(w, http.StatusForbidden, "需要管理员权限")
			return
		}
		systemAccountID, valid := managementAccountGroupBindingSystemAccountID(r, scope)
		if !valid {
			writeMessageError(w, http.StatusBadRequest, "查询参数不合法")
			return
		}
		var payload struct {
			GroupID *string `json:"groupId"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&payload); err != nil || payload.GroupID == nil || strings.TrimSpace(*payload.GroupID) == "" {
			writeMessageError(w, http.StatusBadRequest, "绑定分组参数无效")
			return
		}
		var extra struct{}
		if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
			writeMessageError(w, http.StatusBadRequest, "绑定分组参数无效")
			return
		}
		result, err := service.Bind(r, managementaccountgroupbinding.BindInput{
			ActorSystemAccountID: authContext.SystemAccountID,
			ActorRole:            authContext.Role,
			SystemAccountID:      systemAccountID,
			SelfOnly:             scope == managementAccountGroupBindingScopeSelf,
			AccountID:            chi.URLParam(r, "id"),
			GroupID:              *payload.GroupID,
		})
		if err != nil {
			writeMessageError(w, http.StatusBadRequest, err.Error())
			return
		}
		recordAccountGroupBindingOperationLog(r, authContext, scope, result, operationLogs)
		writeData(w, http.StatusOK, result.Account)
	})
}

func managementAccountGroupBindingSystemAccountID(r *http.Request, scope managementAccountGroupBindingScope) (string, bool) {
	if scope == managementAccountGroupBindingScopeSelf {
		return "", true
	}
	values, exists := r.URL.Query()["systemAccountId"]
	if !exists {
		return "", true
	}
	if len(values) == 0 || strings.TrimSpace(values[0]) == "" {
		return "", false
	}
	return strings.TrimSpace(values[0]), true
}

func recordAccountGroupBindingOperationLog(r *http.Request, authContext managementauth.Context, scope managementAccountGroupBindingScope, result managementaccountgroupbinding.BindResult, opts managementOperationLogOptions) {
	if opts.submitter == nil {
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
	if scope == managementAccountGroupBindingScopeAdmin {
		mode = "admin"
	}
	statusCode := http.StatusOK
	input := port.OperationLogInput{
		ID:                            newLogID(),
		TraceID:                       requestIDFromContext(r.Context()),
		ActorSystemAccountID:          authContext.SystemAccountID,
		ActorUsername:                 authContext.Username,
		ActorDisplayName:              authContext.DisplayName,
		ActorRole:                     authContext.Role,
		OperationScopeSystemAccountID: result.Account.SystemAccountID,
		Mode:                          mode,
		Module:                        "accounts",
		Action:                        "bind_group",
		OperationKey:                  "accounts.bind_group",
		ResourceType:                  "account",
		ResourceID:                    result.Account.ID,
		ResourceName:                  result.Account.Name,
		Summary:                       "绑定账户分组：" + result.Account.Name,
		DetailLevel:                   "full",
		VisibilityScope:               "targeted",
		Changes: []port.OperationLogChange{{
			Field: "groupId", Label: "绑定分组", Before: result.PreviousGroupID, After: result.Account.BoundGroupID,
		}},
		Method: r.Method, Path: r.URL.Path, StatusCode: &statusCode,
		ClientIP: opts.clientIP.FromRequest(r), UserAgent: r.UserAgent(),
		Viewers: []port.OperationLogViewerInput{{
			SystemAccountID: result.Account.SystemAccountID, VisibilityReason: "resource_owner", DetailLevel: "full",
		}},
		CreatedAt: now().UTC(),
	}
	enqueueManagementOperationLog(r.Context(), opts, input)
}
