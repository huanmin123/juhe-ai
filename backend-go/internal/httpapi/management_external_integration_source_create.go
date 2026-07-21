package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementexternalintegrationsources"
	"juhe-ai/backend-go/internal/store/port"
)

type managementExternalIntegrationSourceCreateService interface {
	Create(context.Context, managementexternalintegrationsources.CreateInput) (managementexternalintegrationsources.CreateResult, error)
}

func NewManagementExternalIntegrationSourceCreateHandlerWithOperationLog(
	service *managementexternalintegrationsources.CreateService,
	opts ManagementOperationLogOptions,
) http.Handler {
	if service == nil {
		return newManagementExternalIntegrationSourceCreateHandler(nil, newManagementOperationLogOptions(opts))
	}
	return newManagementExternalIntegrationSourceCreateHandler(service, newManagementOperationLogOptions(opts))
}

func newManagementExternalIntegrationSourceCreateHandler(
	service managementExternalIntegrationSourceCreateService,
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

		input, ok := decodeManagementExternalIntegrationSourceCreatePayload(w, r)
		if !ok {
			return
		}
		result, err := service.Create(r.Context(), input)
		status, message, failed := managementExternalIntegrationSourceCreateError(err)
		if failed {
			writeMessageError(w, status, message)
			return
		}

		w.Header().Set("Pragma", "no-cache")
		writeData(w, http.StatusCreated, struct {
			Token managementexternalintegrationsources.CreatedToken `json:"token"`
		}{Token: result.Token})
		recordManagementExternalIntegrationSourceCreateOperationLog(r, authContext, result, logOptions)
	})
}

func decodeManagementExternalIntegrationSourceCreatePayload(
	w http.ResponseWriter,
	r *http.Request,
) (managementexternalintegrationsources.CreateInput, bool) {
	decoder := json.NewDecoder(r.Body)
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		writeMessageError(w, http.StatusBadRequest, "请求体无效")
		return managementexternalintegrationsources.CreateInput{}, false
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		writeMessageError(w, http.StatusBadRequest, "请求体无效")
		return managementexternalintegrationsources.CreateInput{}, false
	}
	fields, ok := decoded.(map[string]any)
	if !ok {
		writeMessageError(w, http.StatusBadRequest, "来源系统参数无效")
		return managementexternalintegrationsources.CreateInput{}, false
	}

	var input managementexternalintegrationsources.CreateInput
	for field, value := range fields {
		switch field {
		case "name":
			input.Name, ok = value.(string)
		case "status":
			input.Status, ok = value.(string)
		case "scopes":
			input.Scopes, ok = value.([]any)
		case "rateLimits":
			input.RateLimits, ok = value.([]any)
		case "expiresAt":
			input.ExpiresAt = value
			if value != nil {
				_, ok = value.(string)
			}
		case "notes":
			input.Notes = value
			if value != nil {
				_, ok = value.(string)
			}
		default:
			ok = false
		}
		if !ok {
			writeMessageError(w, http.StatusBadRequest, "来源系统参数无效")
			return managementexternalintegrationsources.CreateInput{}, false
		}
	}
	return input, true
}

func managementExternalIntegrationSourceCreateError(err error) (int, string, bool) {
	if err == nil {
		return 0, "", false
	}
	switch {
	case managementexternalintegrationsources.IsCreateValidationError(err):
		return http.StatusBadRequest, err.Error(), true
	case errors.Is(err, managementexternalintegrationsources.ErrNameExists):
		return http.StatusBadRequest, "来源系统名称已存在", true
	case errors.Is(err, managementexternalintegrationsources.ErrTokenExists):
		return http.StatusBadRequest, "来源系统 token 已存在，请重新生成", true
	default:
		return http.StatusInternalServerError, "服务器内部错误", true
	}
}

func recordManagementExternalIntegrationSourceCreateOperationLog(
	r *http.Request,
	authContext managementauth.Context,
	result managementexternalintegrationsources.CreateResult,
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
	statusCode := http.StatusCreated
	source := result.Source
	enqueueManagementOperationLog(r.Context(), opts, port.OperationLogInput{
		ID:                   newLogID(),
		TraceID:              requestIDFromContext(r.Context()),
		ActorSystemAccountID: authContext.SystemAccountID,
		ActorUsername:        authContext.Username,
		ActorDisplayName:     authContext.DisplayName,
		ActorRole:            authContext.Role,
		Mode:                 "self",
		Module:               "external_integration_sources",
		Action:               "create",
		OperationKey:         "external_integration_sources.create",
		ResourceType:         "external_integration_source",
		ResourceID:           source.ID,
		ResourceName:         source.Name,
		Summary:              "创建外部来源系统：" + source.Name,
		DetailLevel:          "full",
		VisibilityScope:      "admin_only",
		Changes: []port.OperationLogChange{
			{Field: "name", Label: "名称", Before: nil, After: source.Name},
			{Field: "status", Label: "状态", Before: nil, After: source.Status},
			{Field: "expiresAt", Label: "到期时间", Before: nil, After: source.ExpiresAt},
			{Field: "rateLimits", Label: "限频规则", Before: nil, After: formatManagementExternalIntegrationSourceRateLimits(source.RateLimits)},
		},
		Method:     r.Method,
		Path:       r.URL.Path,
		StatusCode: &statusCode,
		ClientIP:   opts.clientIP.FromRequest(r),
		UserAgent:  r.UserAgent(),
		CreatedAt:  now().UTC(),
	})
}

var _ managementExternalIntegrationSourceCreateService = (*managementexternalintegrationsources.CreateService)(nil)
