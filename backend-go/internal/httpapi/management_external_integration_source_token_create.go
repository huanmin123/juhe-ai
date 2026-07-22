package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementexternalintegrationsources"
	"juhe-ai/backend-go/internal/store/port"
)

type managementExternalIntegrationSourceTokenCreateService interface {
	Create(context.Context, managementexternalintegrationsources.TokenCreateInput) (managementexternalintegrationsources.TokenCreateResult, error)
}

func NewManagementExternalIntegrationSourceTokenCreateHandlerWithOperationLog(
	service *managementexternalintegrationsources.TokenCreateService,
	opts ManagementOperationLogOptions,
) http.Handler {
	if service == nil {
		return newManagementExternalIntegrationSourceTokenCreateHandler(nil, newManagementOperationLogOptions(opts))
	}
	return newManagementExternalIntegrationSourceTokenCreateHandler(service, newManagementOperationLogOptions(opts))
}

func newManagementExternalIntegrationSourceTokenCreateHandler(
	service managementExternalIntegrationSourceTokenCreateService,
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

		input, ok := decodeManagementExternalIntegrationSourceTokenCreatePayload(w, r)
		if !ok {
			return
		}
		input.SourceID = chi.URLParam(r, "id")
		result, err := service.Create(r.Context(), input)
		status, message, failed := managementExternalIntegrationSourceTokenCreateError(err)
		if failed {
			writeMessageError(w, status, message)
			return
		}

		w.Header().Set("Pragma", "no-cache")
		writeData(w, http.StatusCreated, struct {
			Token managementexternalintegrationsources.CreatedToken `json:"token"`
		}{Token: result.Token})
		recordManagementExternalIntegrationSourceTokenCreateOperationLog(r, authContext, result, logOptions)
	})
}

func decodeManagementExternalIntegrationSourceTokenCreatePayload(
	w http.ResponseWriter,
	r *http.Request,
) (managementexternalintegrationsources.TokenCreateInput, bool) {
	decoder := json.NewDecoder(r.Body)
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		writeMessageError(w, http.StatusBadRequest, "请求体无效")
		return managementexternalintegrationsources.TokenCreateInput{}, false
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		writeMessageError(w, http.StatusBadRequest, "请求体无效")
		return managementexternalintegrationsources.TokenCreateInput{}, false
	}
	fields, ok := decoded.(map[string]any)
	if !ok {
		writeMessageError(w, http.StatusBadRequest, "Token 参数无效")
		return managementexternalintegrationsources.TokenCreateInput{}, false
	}

	var input managementexternalintegrationsources.TokenCreateInput
	for field, value := range fields {
		switch field {
		case "name":
			input.Name, ok = value.(string)
		case "status":
			input.Status, ok = value.(string)
		case "scopes":
			input.Scopes, ok = value.([]any)
		case "expiresAt":
			input.ExpiresAt = value
			if value != nil {
				_, ok = value.(string)
			}
		default:
			ok = false
		}
		if !ok {
			writeMessageError(w, http.StatusBadRequest, "Token 参数无效")
			return managementexternalintegrationsources.TokenCreateInput{}, false
		}
	}
	return input, true
}

func managementExternalIntegrationSourceTokenCreateError(err error) (int, string, bool) {
	if err == nil {
		return 0, "", false
	}
	switch {
	case managementexternalintegrationsources.IsTokenCreateValidationError(err):
		return http.StatusBadRequest, err.Error(), true
	case errors.Is(err, managementexternalintegrationsources.ErrNotFound):
		return http.StatusBadRequest, "来源系统不存在", true
	case errors.Is(err, managementexternalintegrationsources.ErrBuiltInTokenCreateRestricted):
		return http.StatusBadRequest, managementexternalintegrationsources.ErrBuiltInTokenCreateRestricted.Error(), true
	case errors.Is(err, managementexternalintegrationsources.ErrTokenExists):
		return http.StatusBadRequest, managementexternalintegrationsources.ErrTokenExists.Error(), true
	default:
		return http.StatusInternalServerError, "服务器内部错误", true
	}
}

func recordManagementExternalIntegrationSourceTokenCreateOperationLog(
	r *http.Request,
	authContext managementauth.Context,
	result managementexternalintegrationsources.TokenCreateResult,
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
	statusCode := http.StatusCreated
	source := result.Source.Source
	token := result.Token
	var expiresAt any
	if token.ExpiresAt != nil {
		expiresAt = *token.ExpiresAt
	}
	enqueueManagementOperationLog(r.Context(), opts, port.OperationLogInput{
		ID:                   newLogID(),
		TraceID:              requestIDFromContext(r.Context()),
		ActorSystemAccountID: authContext.SystemAccountID,
		ActorUsername:        authContext.Username,
		ActorDisplayName:     authContext.DisplayName,
		ActorRole:            authContext.Role,
		Mode:                 "self",
		Module:               "external_integration_sources",
		Action:               "create_token",
		OperationKey:         "external_integration_sources.create_token",
		ResourceType:         "external_integration_source",
		ResourceID:           source.ID,
		ResourceName:         source.Name,
		Summary:              "生成外部来源系统 Token：" + source.Name,
		DetailLevel:          "full",
		VisibilityScope:      "admin_only",
		Changes: []port.OperationLogChange{
			{Field: "tokenName", Label: "Token 名称", Before: nil, After: token.Name},
			{Field: "tokenPreview", Label: "Token 标识", Before: nil, After: token.TokenPrefix + "..." + token.TokenSuffix},
			{Field: "expiresAt", Label: "到期时间", Before: nil, After: expiresAt},
		},
		Method:     r.Method,
		Path:       r.URL.Path,
		StatusCode: &statusCode,
		ClientIP:   opts.clientIP.FromRequest(r),
		UserAgent:  r.UserAgent(),
		CreatedAt:  now().UTC(),
	})
}

var _ managementExternalIntegrationSourceTokenCreateService = (*managementexternalintegrationsources.TokenCreateService)(nil)
