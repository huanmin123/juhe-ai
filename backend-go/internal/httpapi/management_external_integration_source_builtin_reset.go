package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementexternalintegrationsources"
	"juhe-ai/backend-go/internal/store/port"
)

type managementExternalIntegrationSourceBuiltInResetService interface {
	Reset(context.Context) (managementexternalintegrationsources.TokenCreateResult, error)
}

func NewManagementExternalIntegrationSourceBuiltInResetHandlerWithOperationLog(
	service *managementexternalintegrationsources.BuiltInResetService,
	opts ManagementOperationLogOptions,
) http.Handler {
	if service == nil {
		return newManagementExternalIntegrationSourceBuiltInResetHandler(nil, newManagementOperationLogOptions(opts))
	}
	return newManagementExternalIntegrationSourceBuiltInResetHandler(service, newManagementOperationLogOptions(opts))
}

func newManagementExternalIntegrationSourceBuiltInResetHandler(
	service managementExternalIntegrationSourceBuiltInResetService,
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

		result, err := service.Reset(r.Context())
		if err != nil {
			if errors.Is(err, managementexternalintegrationsources.ErrBuiltInResetNotFound) {
				writeMessageError(w, http.StatusBadRequest, "内置测试 Token 不存在")
				return
			}
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}

		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Pragma", "no-cache")
		writeData(w, http.StatusOK, result)
		recordManagementExternalIntegrationSourceBuiltInResetOperationLog(r, authContext, result, logOptions)
	})
}

func recordManagementExternalIntegrationSourceBuiltInResetOperationLog(
	r *http.Request,
	authContext managementauth.Context,
	result managementexternalintegrationsources.TokenCreateResult,
	opts managementOperationLogOptions,
) {
	if opts.client == nil {
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
	statusCode := http.StatusOK
	source := result.Source.Source
	token := result.Token
	enqueueManagementOperationLog(r.Context(), opts, port.OperationLogInput{
		ID:                   newLogID(),
		TraceID:              requestIDFromContext(r.Context()),
		ActorSystemAccountID: authContext.SystemAccountID,
		ActorUsername:        authContext.Username,
		ActorDisplayName:     authContext.DisplayName,
		ActorRole:            authContext.Role,
		Mode:                 "self",
		Module:               "external_integration_sources",
		Action:               "reset_builtin_test_token",
		OperationKey:         "external_integration_sources.reset_builtin_test_token",
		ResourceType:         "external_integration_source",
		ResourceID:           source.ID,
		ResourceName:         source.Name,
		Summary:              "重置内置测试 Token",
		DetailLevel:          "full",
		VisibilityScope:      "admin_only",
		Changes: []port.OperationLogChange{{
			Field: "tokenPreview", Label: "Token 标识", Before: nil, After: token.TokenPrefix + "..." + token.TokenSuffix,
		}},
		Method:     r.Method,
		Path:       r.URL.Path,
		StatusCode: &statusCode,
		ClientIP:   opts.clientIP.FromRequest(r),
		UserAgent:  r.UserAgent(),
		CreatedAt:  now().UTC(),
	})
}

var _ managementExternalIntegrationSourceBuiltInResetService = (*managementexternalintegrationsources.BuiltInResetService)(nil)
