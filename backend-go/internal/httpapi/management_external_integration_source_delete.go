package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementexternalintegrationsources"
	"juhe-ai/backend-go/internal/store/port"
)

type managementExternalIntegrationSourceDeleteService interface {
	Delete(
		context.Context,
		managementexternalintegrationsources.DeleteInput,
	) (managementexternalintegrationsources.DeleteResult, error)
}

func NewManagementExternalIntegrationSourceDeleteHandlerWithOperationLog(
	service *managementexternalintegrationsources.DeleteService,
	opts ManagementOperationLogOptions,
) http.Handler {
	if service == nil {
		return newManagementExternalIntegrationSourceDeleteHandler(nil, newManagementOperationLogOptions(opts))
	}
	return newManagementExternalIntegrationSourceDeleteHandler(service, newManagementOperationLogOptions(opts))
}

func newManagementExternalIntegrationSourceDeleteHandler(
	service managementExternalIntegrationSourceDeleteService,
	logOptions managementOperationLogOptions,
) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authContext, ok := ManagementAuthContextFromRequest(r)
		if !ok || strings.TrimSpace(authContext.SystemAccountID) == "" {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		if !managementauth.IsAdminRole(authContext.Role) {
			writeMessageError(w, http.StatusForbidden, "需要管理员权限")
			return
		}
		if service == nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}

		result, err := service.Delete(r.Context(), managementexternalintegrationsources.DeleteInput{
			SourceID: chi.URLParam(r, "id"),
		})
		status, message, failed := managementExternalIntegrationSourceDeleteError(err)
		if failed {
			writeMessageError(w, status, message)
			return
		}

		recordManagementExternalIntegrationSourceDeleteOperationLog(
			r,
			authContext,
			result,
			logOptions,
		)
		w.WriteHeader(http.StatusNoContent)
	})
}

func managementExternalIntegrationSourceDeleteError(err error) (int, string, bool) {
	if err == nil {
		return 0, "", false
	}
	switch {
	case errors.Is(err, managementexternalintegrationsources.ErrDeleteInvalid):
		return http.StatusBadRequest, managementexternalintegrationsources.ErrDeleteInvalid.Error(), true
	case errors.Is(err, managementexternalintegrationsources.ErrNotFound):
		return http.StatusNotFound, managementexternalintegrationsources.ErrNotFound.Error(), true
	case errors.Is(err, managementexternalintegrationsources.ErrBuiltInDeleteRestricted):
		return http.StatusBadRequest, managementexternalintegrationsources.ErrBuiltInDeleteRestricted.Error(), true
	default:
		return http.StatusInternalServerError, "服务器内部错误", true
	}
}

func recordManagementExternalIntegrationSourceDeleteOperationLog(
	r *http.Request,
	authContext managementauth.Context,
	result managementexternalintegrationsources.DeleteResult,
	opts managementOperationLogOptions,
) {
	if opts.submitter == nil {
		return
	}
	now := opts.now
	if now == nil {
		now = time.Now
	}
	newLogID := opts.newLogID
	if newLogID == nil {
		newLogID = defaultManagementOperationLogID
	}
	statusCode := http.StatusNoContent
	enqueueManagementOperationLog(r.Context(), opts, port.OperationLogInput{
		ID:                   newLogID(),
		TraceID:              requestIDFromContext(r.Context()),
		ActorSystemAccountID: authContext.SystemAccountID,
		ActorUsername:        authContext.Username,
		ActorDisplayName:     authContext.DisplayName,
		ActorRole:            authContext.Role,
		Mode:                 "self",
		Module:               "external_integration_sources",
		Action:               "delete",
		OperationKey:         "external_integration_sources.delete",
		ResourceType:         "external_integration_source",
		ResourceID:           result.SourceID,
		ResourceName:         result.SourceName,
		Summary:              "删除外部来源系统：" + result.SourceName,
		DetailLevel:          "full",
		VisibilityScope:      "admin_only",
		Changes: []port.OperationLogChange{
			{Field: "deleted", Label: "删除状态", Before: false, After: true},
			{Field: "tokenCount", Label: "关联 Token 数量", Before: result.TokenCount, After: int64(0)},
		},
		Method:     r.Method,
		Path:       r.URL.Path,
		StatusCode: &statusCode,
		ClientIP:   opts.clientIP.FromRequest(r),
		UserAgent:  r.UserAgent(),
		CreatedAt:  now().UTC(),
	})
}

var _ managementExternalIntegrationSourceDeleteService = (*managementexternalintegrationsources.DeleteService)(nil)
