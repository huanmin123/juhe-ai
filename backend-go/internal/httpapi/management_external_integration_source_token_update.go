package httpapi

import (
	"bytes"
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

const managementExternalIntegrationSourceTokenUpdateMaxBodyBytes = 256 << 10

type managementExternalIntegrationSourceTokenUpdateService interface {
	Update(context.Context, managementexternalintegrationsources.TokenUpdateInput) (managementexternalintegrationsources.TokenUpdateResult, error)
}

func NewManagementExternalIntegrationSourceTokenUpdateHandlerWithOperationLog(
	service *managementexternalintegrationsources.TokenUpdateService,
	opts ManagementOperationLogOptions,
) http.Handler {
	if service == nil {
		return newManagementExternalIntegrationSourceTokenUpdateHandler(nil, newManagementOperationLogOptions(opts))
	}
	return newManagementExternalIntegrationSourceTokenUpdateHandler(service, newManagementOperationLogOptions(opts))
}

func newManagementExternalIntegrationSourceTokenUpdateHandler(
	service managementExternalIntegrationSourceTokenUpdateService,
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

		sourceID := chi.URLParam(r, "id")
		tokenID := chi.URLParam(r, "tokenId")
		if strings.TrimSpace(sourceID) == "" || strings.TrimSpace(tokenID) == "" {
			writeMessageError(w, http.StatusBadRequest, "Token 参数无效")
			return
		}
		input, ok := decodeManagementExternalIntegrationSourceTokenUpdatePayload(w, r)
		if !ok {
			return
		}
		input.SourceID = sourceID
		input.TokenID = tokenID
		result, err := service.Update(r.Context(), input)
		status, message, failed := managementExternalIntegrationSourceTokenUpdateError(err)
		if failed {
			writeMessageError(w, status, message)
			return
		}

		w.Header().Set("Cache-Control", "no-store")
		writeData(w, http.StatusOK, result.After)
		recordManagementExternalIntegrationSourceTokenUpdateOperationLog(r, authContext, sourceID, result, logOptions)
	})
}

func decodeManagementExternalIntegrationSourceTokenUpdatePayload(
	w http.ResponseWriter,
	r *http.Request,
) (managementexternalintegrationsources.TokenUpdateInput, bool) {
	limited := http.MaxBytesReader(w, r.Body, managementExternalIntegrationSourceTokenUpdateMaxBodyBytes)
	body, err := io.ReadAll(limited)
	_ = limited.Close()
	if err != nil {
		writeManagementGroupCreateBodyError(w, err)
		return managementexternalintegrationsources.TokenUpdateInput{}, false
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		writeMessageError(w, http.StatusBadRequest, "请求体无效")
		return managementexternalintegrationsources.TokenUpdateInput{}, false
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		writeMessageError(w, http.StatusBadRequest, "请求体无效")
		return managementexternalintegrationsources.TokenUpdateInput{}, false
	}
	fields, ok := decoded.(map[string]any)
	if !ok {
		writeMessageError(w, http.StatusBadRequest, "Token 参数无效")
		return managementexternalintegrationsources.TokenUpdateInput{}, false
	}

	var input managementexternalintegrationsources.TokenUpdateInput
	for field, value := range fields {
		switch field {
		case "name":
			input.HasName = true
			input.Name, ok = value.(string)
		case "status":
			input.HasStatus = true
			input.Status, ok = value.(string)
		case "scopes":
			input.HasScopes = true
			input.Scopes, ok = value.([]any)
		case "expiresAt":
			input.HasExpiresAt = true
			input.ExpiresAt = value
			if value != nil {
				_, ok = value.(string)
			}
		default:
			ok = false
		}
		if !ok {
			writeMessageError(w, http.StatusBadRequest, "Token 参数无效")
			return managementexternalintegrationsources.TokenUpdateInput{}, false
		}
	}
	return input, true
}

func managementExternalIntegrationSourceTokenUpdateError(err error) (int, string, bool) {
	if err == nil {
		return 0, "", false
	}
	switch {
	case managementexternalintegrationsources.IsTokenUpdateValidationError(err),
		errors.Is(err, managementexternalintegrationsources.ErrBuiltInTokenUpdateRestricted):
		return http.StatusBadRequest, err.Error(), true
	case errors.Is(err, managementexternalintegrationsources.ErrTokenNotFound):
		return http.StatusNotFound, managementexternalintegrationsources.ErrTokenNotFound.Error(), true
	default:
		return http.StatusInternalServerError, "服务器内部错误", true
	}
}

func recordManagementExternalIntegrationSourceTokenUpdateOperationLog(
	r *http.Request,
	authContext managementauth.Context,
	sourceID string,
	result managementexternalintegrationsources.TokenUpdateResult,
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
	statusCode := http.StatusOK
	var expiresAt any
	if result.After.ExpiresAt != nil {
		expiresAt = *result.After.ExpiresAt
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
		Action:               "update_token",
		OperationKey:         "external_integration_sources.update_token",
		ResourceType:         "external_integration_source",
		ResourceID:           sourceID,
		ResourceName:         sourceID,
		Summary:              "更新外部来源系统 Token：" + result.After.Name,
		DetailLevel:          "full",
		VisibilityScope:      "admin_only",
		Changes: []port.OperationLogChange{
			{Field: "tokenName", Label: "Token 名称", Before: nil, After: result.After.Name},
			{Field: "tokenStatus", Label: "Token 状态", Before: nil, After: result.After.Status},
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

var _ managementExternalIntegrationSourceTokenUpdateService = (*managementexternalintegrationsources.TokenUpdateService)(nil)
