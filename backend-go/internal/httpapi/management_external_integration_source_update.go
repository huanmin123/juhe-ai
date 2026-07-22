package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementexternalintegrationsources"
	"juhe-ai/backend-go/internal/store/port"
)

const managementExternalIntegrationSourceUpdateMaxBodyBytes = 256 << 10

type managementExternalIntegrationSourceUpdateService interface {
	Update(context.Context, managementexternalintegrationsources.UpdateInput) (managementexternalintegrationsources.UpdateResult, error)
}

func NewManagementExternalIntegrationSourceUpdateHandlerWithOperationLog(
	service *managementexternalintegrationsources.UpdateService,
	opts ManagementOperationLogOptions,
) http.Handler {
	if service == nil {
		return newManagementExternalIntegrationSourceUpdateHandler(nil, newManagementOperationLogOptions(opts))
	}
	return newManagementExternalIntegrationSourceUpdateHandler(service, newManagementOperationLogOptions(opts))
}

func newManagementExternalIntegrationSourceUpdateHandler(
	service managementExternalIntegrationSourceUpdateService,
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
		payload, ok := decodeManagementExternalIntegrationSourceUpdatePayload(w, r)
		if !ok {
			return
		}
		payload.SourceID = chi.URLParam(r, "id")
		result, err := service.Update(r.Context(), payload)
		status, message, failed := managementExternalIntegrationSourceUpdateError(err)
		if failed {
			writeMessageError(w, status, message)
			return
		}
		recordManagementExternalIntegrationSourceUpdateOperationLog(r, authContext, result, logOptions)
		writeData(w, http.StatusOK, struct {
			ID string `json:"id"`
		}{ID: result.After.ID})
	})
}

func decodeManagementExternalIntegrationSourceUpdatePayload(
	w http.ResponseWriter,
	r *http.Request,
) (managementexternalintegrationsources.UpdateInput, bool) {
	limited := http.MaxBytesReader(w, r.Body, managementExternalIntegrationSourceUpdateMaxBodyBytes)
	body, err := io.ReadAll(limited)
	_ = limited.Close()
	if err != nil {
		writeManagementGroupCreateBodyError(w, err)
		return managementexternalintegrationsources.UpdateInput{}, false
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		writeMessageError(w, http.StatusBadRequest, "请求体无效")
		return managementexternalintegrationsources.UpdateInput{}, false
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		writeMessageError(w, http.StatusBadRequest, "请求体无效")
		return managementexternalintegrationsources.UpdateInput{}, false
	}
	fields, ok := decoded.(map[string]any)
	if !ok {
		writeMessageError(w, http.StatusBadRequest, "来源系统参数无效")
		return managementexternalintegrationsources.UpdateInput{}, false
	}

	var input managementexternalintegrationsources.UpdateInput
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
		case "rateLimits":
			input.HasRateLimits = true
			input.RateLimits, ok = value.([]any)
		case "expiresAt":
			input.HasExpiresAt = true
			input.ExpiresAt = value
			if value != nil {
				_, ok = value.(string)
			}
		case "notes":
			input.HasNotes = true
			input.Notes = value
			if value != nil {
				_, ok = value.(string)
			}
		default:
			ok = false
		}
		if !ok {
			writeMessageError(w, http.StatusBadRequest, "来源系统参数无效")
			return managementexternalintegrationsources.UpdateInput{}, false
		}
	}
	return input, true
}

func managementExternalIntegrationSourceUpdateError(err error) (int, string, bool) {
	if err == nil {
		return 0, "", false
	}
	switch {
	case errors.Is(err, managementexternalintegrationsources.ErrNotFound):
		return http.StatusNotFound, managementexternalintegrationsources.ErrNotFound.Error(), true
	case errors.Is(err, managementexternalintegrationsources.ErrBuiltInUpdateRestricted),
		errors.Is(err, managementexternalintegrationsources.ErrNameExists):
		return http.StatusBadRequest, err.Error(), true
	default:
		if managementexternalintegrationsources.IsUpdateValidationError(err) {
			return http.StatusBadRequest, err.Error(), true
		}
		return http.StatusInternalServerError, "服务器内部错误", true
	}
}

func recordManagementExternalIntegrationSourceUpdateOperationLog(
	r *http.Request,
	authContext managementauth.Context,
	result managementexternalintegrationsources.UpdateResult,
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
	enqueueManagementOperationLog(r.Context(), opts, port.OperationLogInput{
		ID:                   newLogID(),
		TraceID:              requestIDFromContext(r.Context()),
		ActorSystemAccountID: authContext.SystemAccountID,
		ActorUsername:        authContext.Username,
		ActorDisplayName:     authContext.DisplayName,
		ActorRole:            authContext.Role,
		Mode:                 "self",
		Module:               "external_integration_sources",
		Action:               "update",
		OperationKey:         "external_integration_sources.update",
		ResourceType:         "external_integration_source",
		ResourceID:           result.After.ID,
		ResourceName:         result.After.Name,
		Summary:              "更新外部来源系统：" + result.After.Name,
		DetailLevel:          "full",
		VisibilityScope:      "admin_only",
		Changes: []port.OperationLogChange{
			{Field: "name", Label: "名称", Before: result.Before.Name, After: result.After.Name},
			{Field: "status", Label: "状态", Before: result.Before.Status, After: result.After.Status},
			{Field: "expiresAt", Label: "到期时间", Before: result.Before.ExpiresAt, After: result.After.ExpiresAt},
			{
				Field:  "rateLimits",
				Label:  "限频规则",
				Before: formatManagementExternalIntegrationSourceRateLimits(result.Before.RateLimits),
				After:  formatManagementExternalIntegrationSourceRateLimits(result.After.RateLimits),
			},
		},
		Method:     r.Method,
		Path:       r.URL.Path,
		StatusCode: &statusCode,
		ClientIP:   opts.clientIP.FromRequest(r),
		UserAgent:  r.UserAgent(),
		CreatedAt:  now().UTC(),
	})
}

func formatManagementExternalIntegrationSourceRateLimits(
	rules []managementexternalintegrationsources.RateLimitRule,
) string {
	if len(rules) == 0 {
		return "不限制"
	}
	formatted := make([]string, 0, len(rules))
	for _, rule := range rules {
		formatted = append(formatted, fmt.Sprintf("%ds/%d次", rule.WindowSeconds, rule.MaxRequests))
	}
	return strings.Join(formatted, ", ")
}
