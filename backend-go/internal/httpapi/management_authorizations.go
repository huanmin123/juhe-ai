package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"

	operationlogjob "juhe-ai/backend-go/internal/jobs/operationlog"
	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementauthorizations"
	"juhe-ai/backend-go/internal/store/port"
)

type managementAuthorizationScope int

const (
	managementAuthorizationScopeAdmin managementAuthorizationScope = iota
	managementAuthorizationScopeSelf
)

type managementAuthorizationCreateService interface {
	Create(r *http.Request, input managementauthorizations.CreateInput) (managementauthorizations.Summary, error)
}

type managementAuthorizationServiceAdapter struct {
	service *managementauthorizations.Service
}

func (s managementAuthorizationServiceAdapter) Create(r *http.Request, input managementauthorizations.CreateInput) (managementauthorizations.Summary, error) {
	return s.service.Create(r.Context(), input)
}

func NewManagementAuthorizationCreateHandler(service *managementauthorizations.Service) http.Handler {
	return newManagementAuthorizationCreateHandler(managementAuthorizationServiceAdapter{service: service}, managementAuthorizationScopeAdmin)
}

func NewManagementMyAuthorizationCreateHandler(service *managementauthorizations.Service) http.Handler {
	return newManagementAuthorizationCreateHandler(managementAuthorizationServiceAdapter{service: service}, managementAuthorizationScopeSelf)
}

func NewManagementAuthorizationCreateHandlerWithOperationLog(service *managementauthorizations.Service, opts ManagementOperationLogOptions) http.Handler {
	return newManagementAuthorizationCreateHandler(
		managementAuthorizationServiceAdapter{service: service},
		managementAuthorizationScopeAdmin,
		newManagementOperationLogOptions(opts),
	)
}

func NewManagementMyAuthorizationCreateHandlerWithOperationLog(service *managementauthorizations.Service, opts ManagementOperationLogOptions) http.Handler {
	return newManagementAuthorizationCreateHandler(
		managementAuthorizationServiceAdapter{service: service},
		managementAuthorizationScopeSelf,
		newManagementOperationLogOptions(opts),
	)
}

func newManagementAuthorizationCreateHandler(service managementAuthorizationCreateService, scope managementAuthorizationScope, logOptions ...managementOperationLogOptions) http.Handler {
	operationLogs := effectiveManagementOperationLogOptions(logOptions)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authContext, ok := ManagementAuthContextFromRequest(r)
		if !ok || strings.TrimSpace(authContext.SystemAccountID) == "" {
			writeMessageError(w, http.StatusUnauthorized, "未登录")
			return
		}
		if service == nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		ownerSystemAccountID, validScope := managementAuthorizationOwnerScope(authContext, r.URL.Query(), scope)
		if !validScope {
			writeMessageError(w, http.StatusBadRequest, "查询参数不合法")
			return
		}
		if scope == managementAuthorizationScopeAdmin && !managementauth.IsAdminRole(authContext.Role) {
			writeMessageError(w, http.StatusForbidden, "需要管理员权限")
			return
		}
		if scope == managementAuthorizationScopeAdmin && ownerSystemAccountID == "" {
			writeMessageError(w, http.StatusBadRequest, "管理员新增授权时必须指定授权人")
			return
		}
		payload, ok := decodeManagementAuthorizationCreatePayload(w, r)
		if !ok {
			return
		}
		result, err := service.Create(r, managementauthorizations.CreateInput{
			ResourceType:                 payload.ResourceType,
			ResourceID:                   payload.ResourceID,
			ResourceOwnerSystemAccountID: ownerSystemAccountID,
			GranteeType:                  payload.GranteeType,
			GranteeID:                    payload.GranteeID,
			TargetGroupID:                payload.TargetGroupID,
			Remark:                       payload.Remark,
			HasRemark:                    payload.HasRemark,
			ExpiresAt:                    payload.ExpiresAt,
			HasExpiresAt:                 payload.HasExpiresAt,
			Limits:                       payload.Limits,
			HasLimits:                    payload.HasLimits,
			ActorSystemAccountID:         authContext.SystemAccountID,
		})
		if errors.Is(err, managementauthorizations.ErrAuthorizationCreateInvalid) {
			writeMessageError(w, http.StatusBadRequest, "授权参数不合法")
			return
		}
		if err != nil {
			writeMessageError(w, http.StatusBadRequest, err.Error())
			return
		}
		recordAuthorizationCreateOperationLog(r, authContext, result, payload, operationLogs)
		writeData(w, http.StatusCreated, result)
	})
}

func managementAuthorizationOwnerScope(authContext managementauth.Context, values url.Values, scope managementAuthorizationScope) (string, bool) {
	switch scope {
	case managementAuthorizationScopeSelf:
		return authContext.SystemAccountID, true
	case managementAuthorizationScopeAdmin:
		rawValues, exists := values["systemAccountId"]
		if !exists {
			return "", true
		}
		var selected string
		for _, raw := range rawValues {
			value := strings.TrimSpace(raw)
			if value == "" {
				return "", false
			}
			if value == "all" {
				continue
			}
			if selected == "" {
				selected = value
			}
		}
		return selected, true
	default:
		return "", false
	}
}

type managementAuthorizationCreatePayload struct {
	ResourceType  string
	ResourceID    string
	GranteeType   string
	GranteeID     string
	TargetGroupID string
	Remark        string
	HasRemark     bool
	ExpiresAt     string
	HasExpiresAt  bool
	Limits        map[string]any
	HasLimits     bool
}

func decodeManagementAuthorizationCreatePayload(w http.ResponseWriter, r *http.Request) (managementAuthorizationCreatePayload, bool) {
	decoder := json.NewDecoder(r.Body)
	decoder.UseNumber()
	var raw map[string]json.RawMessage
	if err := decoder.Decode(&raw); err != nil {
		writeMessageError(w, http.StatusBadRequest, "授权参数不合法")
		return managementAuthorizationCreatePayload{}, false
	}
	if len(raw) == 0 {
		writeMessageError(w, http.StatusBadRequest, "授权参数不合法")
		return managementAuthorizationCreatePayload{}, false
	}
	if err := decoder.Decode(&struct{}{}); err == nil {
		writeMessageError(w, http.StatusBadRequest, "授权参数不合法")
		return managementAuthorizationCreatePayload{}, false
	}
	allowed := map[string]bool{
		"resourceType":  true,
		"resourceId":    true,
		"granteeType":   true,
		"granteeId":     true,
		"targetGroupId": true,
		"remark":        true,
		"expiresAt":     true,
		"limits":        true,
	}
	for key := range raw {
		if !allowed[key] {
			writeMessageError(w, http.StatusBadRequest, "授权参数不合法")
			return managementAuthorizationCreatePayload{}, false
		}
	}
	var payload managementAuthorizationCreatePayload
	var ok bool
	if payload.ResourceType, ok = rawStringField(w, raw, "resourceType", true); !ok {
		return managementAuthorizationCreatePayload{}, false
	}
	if payload.ResourceID, ok = rawStringField(w, raw, "resourceId", true); !ok {
		return managementAuthorizationCreatePayload{}, false
	}
	if payload.GranteeType, ok = rawStringField(w, raw, "granteeType", true); !ok {
		return managementAuthorizationCreatePayload{}, false
	}
	if payload.GranteeID, ok = rawStringField(w, raw, "granteeId", true); !ok {
		return managementAuthorizationCreatePayload{}, false
	}
	if _, exists := raw["targetGroupId"]; exists {
		if payload.TargetGroupID, ok = rawStringField(w, raw, "targetGroupId", false); !ok {
			return managementAuthorizationCreatePayload{}, false
		}
	}
	if _, exists := raw["remark"]; exists {
		if payload.Remark, ok = rawStringField(w, raw, "remark", false); !ok {
			return managementAuthorizationCreatePayload{}, false
		}
		payload.HasRemark = true
	}
	if _, exists := raw["expiresAt"]; exists {
		if payload.ExpiresAt, ok = rawStringField(w, raw, "expiresAt", false); !ok {
			return managementAuthorizationCreatePayload{}, false
		}
		payload.HasExpiresAt = true
	}
	if rawLimits, exists := raw["limits"]; exists {
		if bytes.Equal(bytes.TrimSpace(rawLimits), []byte("null")) {
			writeMessageError(w, http.StatusBadRequest, "授权参数不合法")
			return managementAuthorizationCreatePayload{}, false
		}
		limits, ok := rawObjectField(w, rawLimits)
		if !ok {
			return managementAuthorizationCreatePayload{}, false
		}
		payload.Limits = limits
		payload.HasLimits = true
	}
	return payload, true
}

func rawStringField(w http.ResponseWriter, raw map[string]json.RawMessage, key string, required bool) (string, bool) {
	value, exists := raw[key]
	if !exists {
		if required {
			writeMessageError(w, http.StatusBadRequest, "授权参数不合法")
			return "", false
		}
		return "", true
	}
	if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
		writeMessageError(w, http.StatusBadRequest, "授权参数不合法")
		return "", false
	}
	var text string
	if err := json.Unmarshal(value, &text); err != nil {
		writeMessageError(w, http.StatusBadRequest, "授权参数不合法")
		return "", false
	}
	return text, true
}

func rawObjectField(w http.ResponseWriter, raw json.RawMessage) (map[string]any, bool) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value map[string]any
	if err := decoder.Decode(&value); err != nil || value == nil {
		writeMessageError(w, http.StatusBadRequest, "授权参数不合法")
		return nil, false
	}
	if err := decoder.Decode(&struct{}{}); err == nil {
		writeMessageError(w, http.StatusBadRequest, "授权参数不合法")
		return nil, false
	}
	return value, true
}

func recordAuthorizationCreateOperationLog(
	r *http.Request,
	authContext managementauth.Context,
	result managementauthorizations.Summary,
	payload managementAuthorizationCreatePayload,
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
		newLogID = func() string {
			return "oplog_" + strings.ReplaceAll(uuid.NewString(), "-", "")
		}
	}
	statusCode := http.StatusCreated
	resourceName := result.ResourceName
	if resourceName == "" {
		resourceName = result.ResourceID
	}
	input := port.OperationLogInput{
		ID:                            newLogID(),
		TraceID:                       requestIDFromContext(r.Context()),
		ActorSystemAccountID:          authContext.SystemAccountID,
		ActorUsername:                 authContext.Username,
		ActorDisplayName:              authContext.DisplayName,
		ActorRole:                     authContext.Role,
		OperationScopeSystemAccountID: result.ResourceOwnerSystemAccountID,
		Mode:                          managementAuthorizationOperationMode(authContext, result.ResourceOwnerSystemAccountID),
		Module:                        "authorizations",
		Action:                        "create",
		OperationKey:                  "authorizations.create",
		ResourceType:                  "authorization",
		ResourceID:                    result.ID,
		ResourceName:                  resourceName,
		Summary:                       "创建资源授权：" + resourceName + " -> " + managementAuthorizationGranteeName(result),
		DetailLevel:                   "full",
		VisibilityScope:               "targeted",
		Changes: []port.OperationLogChange{
			{Field: "resourceType", Label: "资源类型", Before: nil, After: result.ResourceType},
			{Field: "resourceId", Label: "授权资源", Before: nil, After: resourceName},
			{Field: "grantee", Label: "被授权目标", Before: nil, After: managementAuthorizationGranteeName(result)},
			{Field: "targetGroupId", Label: "目标分组", Before: nil, After: strings.TrimSpace(payload.TargetGroupID)},
			{Field: "status", Label: "状态", Before: nil, After: result.Status},
			{Field: "expiresAt", Label: "过期时间", Before: nil, After: payload.ExpiresAt},
			{Field: "limits", Label: "额度限制", Before: nil, After: result.Limits},
		},
		Targets:    managementAuthorizationOperationTargets(result),
		Viewers:    managementAuthorizationOperationViewers(result),
		Method:     r.Method,
		Path:       r.URL.Path,
		StatusCode: &statusCode,
		ClientIP:   opts.clientIP.FromRequest(r),
		UserAgent:  r.UserAgent(),
		CreatedAt:  now().UTC(),
	}
	enqueueCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
	defer cancel()
	if _, err := operationlogjob.EnqueueWrite(enqueueCtx, opts.client, input); err != nil && opts.logger != nil {
		opts.logger.Warn("管理端操作日志入队失败",
			slog.String("event", "operation_log_enqueue_failed"),
			slog.String("operation_key", input.OperationKey),
			slog.String("resource_id", input.ResourceID),
			slog.String("request_id", input.TraceID),
			slog.Any("error", err),
		)
	}
}

func managementAuthorizationOperationMode(authContext managementauth.Context, ownerSystemAccountID string) string {
	if managementauth.IsAdminRole(authContext.Role) && authContext.SystemAccountID != ownerSystemAccountID {
		return "admin"
	}
	return "self"
}

func managementAuthorizationGranteeName(result managementauthorizations.Summary) string {
	if result.GranteeType == "team" {
		if result.GranteeTeamName != "" {
			return result.GranteeTeamName
		}
		return "团队"
	}
	if result.GranteeSystemAccountName != "" {
		return result.GranteeSystemAccountName
	}
	if result.GranteeUsername != "" {
		return result.GranteeUsername
	}
	return "被授权用户"
}

func managementAuthorizationOperationTargets(result managementauthorizations.Summary) []port.OperationLogTargetInput {
	targets := []port.OperationLogTargetInput{{
		TargetType:                 result.ResourceType,
		TargetID:                   result.ResourceID,
		TargetName:                 result.ResourceName,
		TargetOwnerSystemAccountID: result.ResourceOwnerSystemAccountID,
		Relation:                   "owner",
	}}
	if result.GranteeType == "team" {
		targets = append(targets, port.OperationLogTargetInput{
			TargetType: "system_team",
			TargetID:   result.GranteeTeamID,
			TargetName: result.GranteeTeamName,
			Relation:   "grantee",
		})
		return targets
	}
	targets = append(targets, port.OperationLogTargetInput{
		TargetType:                 "system_account",
		TargetID:                   result.GranteeSystemAccountID,
		TargetName:                 managementAuthorizationGranteeName(result),
		TargetOwnerSystemAccountID: result.GranteeSystemAccountID,
		Relation:                   "grantee",
	})
	return targets
}

func managementAuthorizationOperationViewers(result managementauthorizations.Summary) []port.OperationLogViewerInput {
	viewers := []port.OperationLogViewerInput{{
		SystemAccountID:  result.ResourceOwnerSystemAccountID,
		VisibilityReason: "authorization_owner",
		DetailLevel:      "full",
	}}
	if result.GranteeType == "system_account" && result.GranteeSystemAccountID != "" {
		viewers = append(viewers, port.OperationLogViewerInput{
			SystemAccountID:  result.GranteeSystemAccountID,
			VisibilityReason: "authorization_grantee",
			DetailLevel:      "full",
		})
	}
	return viewers
}
