package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"juhe-ai/backend-go/internal/modules/managementaccountdelete"
	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/store/port"
)

type managementAccountDeleteService interface {
	Delete(r *http.Request, input managementaccountdelete.DeleteInput) (managementaccountdelete.DeleteResult, error)
}

type managementAccountDeleteServiceAdapter struct {
	service *managementaccountdelete.Service
}

func (s managementAccountDeleteServiceAdapter) Delete(r *http.Request, input managementaccountdelete.DeleteInput) (managementaccountdelete.DeleteResult, error) {
	return s.service.Delete(r.Context(), input)
}

func NewManagementAccountDeleteHandlerWithOperationLog(service *managementaccountdelete.Service, opts ManagementOperationLogOptions) http.Handler {
	return newManagementAccountDeleteHandler(managementAccountDeleteServiceFrom(service), managementAccountScopeAdmin, newManagementOperationLogOptions(opts))
}

func NewManagementMyAccountDeleteHandlerWithOperationLog(service *managementaccountdelete.Service, opts ManagementOperationLogOptions) http.Handler {
	return newManagementAccountDeleteHandler(managementAccountDeleteServiceFrom(service), managementAccountScopeSelf, newManagementOperationLogOptions(opts))
}

func managementAccountDeleteServiceFrom(service *managementaccountdelete.Service) managementAccountDeleteService {
	if service == nil {
		return nil
	}
	return managementAccountDeleteServiceAdapter{service: service}
}

func newManagementAccountDeleteHandler(service managementAccountDeleteService, scope managementAccountOptionScope, logOptions managementOperationLogOptions) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authContext, ok := ManagementAuthContextFromRequest(r)
		if !ok || strings.TrimSpace(authContext.SystemAccountID) == "" {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		if scope == managementAccountScopeAdmin && !managementauth.IsAdminRole(authContext.Role) {
			writeMessageError(w, http.StatusForbidden, "需要管理员权限")
			return
		}
		if service == nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}

		systemAccountID := ""
		selfOnly := scope == managementAccountScopeSelf
		if !selfOnly {
			var message string
			systemAccountID, message, ok = managementGroupDetailSystemAccountID(r.URL.Query())
			if !ok {
				writeMessageError(w, http.StatusBadRequest, message)
				return
			}
		}
		result, err := service.Delete(r, managementaccountdelete.DeleteInput{
			ActorSystemAccountID: authContext.SystemAccountID,
			ActorRole:            authContext.Role,
			SystemAccountID:      systemAccountID,
			SelfOnly:             selfOnly,
			AccountID:            chi.URLParam(r, "id"),
		})
		switch {
		case errors.Is(err, managementaccountdelete.ErrAccountNotFound):
			writeMessageError(w, http.StatusNotFound, "账户不存在")
		case errors.Is(err, managementaccountdelete.ErrAuthorizationInstance):
			writeMessageError(w, http.StatusBadRequest, "授权账户请使用归还操作")
		case err != nil:
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
		default:
			recordManagementAccountDeleteOperationLog(r, authContext, scope, result, logOptions)
			w.WriteHeader(http.StatusNoContent)
		}
	})
}

func recordManagementAccountDeleteOperationLog(r *http.Request, authContext managementauth.Context, scope managementAccountOptionScope, result managementaccountdelete.DeleteResult, opts managementOperationLogOptions) {
	if opts.submitter == nil {
		return
	}
	ownerSystemAccountID := firstNonEmptyText(result.Before.SystemAccountID, authContext.SystemAccountID)
	mode := "self"
	if scope == managementAccountScopeAdmin {
		mode = "admin"
	}
	statusCode := http.StatusNoContent
	now := time.Now
	if opts.now != nil {
		now = opts.now
	}
	input := port.OperationLogInput{
		ID: opts.newLogID(), TraceID: requestIDFromContext(r.Context()),
		ActorSystemAccountID: authContext.SystemAccountID, ActorUsername: authContext.Username, ActorDisplayName: authContext.DisplayName, ActorRole: authContext.Role,
		OperationScopeSystemAccountID: ownerSystemAccountID, Mode: mode, Module: "accounts", Action: "delete", OperationKey: "accounts.delete", ResourceType: "account", ResourceID: result.Before.ID, ResourceName: result.Before.Name,
		Summary: "删除 AI 账户：" + result.Before.Name, DetailLevel: "full", VisibilityScope: "targeted",
		Changes: []port.OperationLogChange{{Field: "deleted", Label: "删除状态", Before: false, After: true}},
		Method:  r.Method, Path: r.URL.Path, StatusCode: &statusCode, ClientIP: opts.clientIP.FromRequest(r), UserAgent: r.UserAgent(),
		Viewers: []port.OperationLogViewerInput{{SystemAccountID: ownerSystemAccountID, VisibilityReason: "resource_owner", DetailLevel: "full"}}, CreatedAt: now().UTC(),
	}
	enqueueManagementOperationLog(r.Context(), opts, input)
}

var _ managementAccountDeleteService = managementAccountDeleteServiceAdapter{}
