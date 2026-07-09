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

	"github.com/go-chi/chi/v5"
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

type managementAuthorizationUpdateService interface {
	Update(r *http.Request, input managementauthorizations.UpdateInput) (managementauthorizations.Summary, bool, error)
}

type managementAuthorizationListService interface {
	List(r *http.Request, input managementauthorizations.ListInput) (managementauthorizations.ListResult, error)
}

type managementAuthorizationGetService interface {
	Get(r *http.Request, input managementauthorizations.GetInput) (managementauthorizations.Detail, bool, error)
}

type managementAuthorizationReturnService interface {
	Return(r *http.Request, input managementauthorizations.ReturnInput) (managementauthorizations.Summary, bool, error)
}

type managementAuthorizationRevokeService interface {
	Revoke(r *http.Request, input managementauthorizations.RevokeInput) (managementauthorizations.Summary, bool, error)
}

type managementAuthorizationServiceAdapter struct {
	service *managementauthorizations.Service
}

func (s managementAuthorizationServiceAdapter) Create(r *http.Request, input managementauthorizations.CreateInput) (managementauthorizations.Summary, error) {
	return s.service.Create(r.Context(), input)
}

func (s managementAuthorizationServiceAdapter) Update(r *http.Request, input managementauthorizations.UpdateInput) (managementauthorizations.Summary, bool, error) {
	return s.service.Update(r.Context(), input)
}

func (s managementAuthorizationServiceAdapter) List(r *http.Request, input managementauthorizations.ListInput) (managementauthorizations.ListResult, error) {
	return s.service.List(r.Context(), input)
}

func (s managementAuthorizationServiceAdapter) Get(r *http.Request, input managementauthorizations.GetInput) (managementauthorizations.Detail, bool, error) {
	return s.service.Get(r.Context(), input)
}

func (s managementAuthorizationServiceAdapter) Return(r *http.Request, input managementauthorizations.ReturnInput) (managementauthorizations.Summary, bool, error) {
	return s.service.Return(r.Context(), input)
}

func (s managementAuthorizationServiceAdapter) Revoke(r *http.Request, input managementauthorizations.RevokeInput) (managementauthorizations.Summary, bool, error) {
	return s.service.Revoke(r.Context(), input)
}

func NewManagementAuthorizationListHandler(service *managementauthorizations.Service) http.Handler {
	return newManagementAuthorizationListHandler(managementAuthorizationServiceAdapter{service: service}, managementAuthorizationScopeAdmin)
}

func NewManagementMyAuthorizationListHandler(service *managementauthorizations.Service) http.Handler {
	return newManagementAuthorizationListHandler(managementAuthorizationServiceAdapter{service: service}, managementAuthorizationScopeSelf)
}

func NewManagementAuthorizationDetailHandler(service *managementauthorizations.Service) http.Handler {
	return newManagementAuthorizationDetailHandler(managementAuthorizationServiceAdapter{service: service}, managementAuthorizationScopeAdmin)
}

func NewManagementMyAuthorizationDetailHandler(service *managementauthorizations.Service) http.Handler {
	return newManagementAuthorizationDetailHandler(managementAuthorizationServiceAdapter{service: service}, managementAuthorizationScopeSelf)
}

func NewManagementAuthorizationCreateHandler(service *managementauthorizations.Service) http.Handler {
	return newManagementAuthorizationCreateHandler(managementAuthorizationServiceAdapter{service: service}, managementAuthorizationScopeAdmin)
}

func NewManagementMyAuthorizationCreateHandler(service *managementauthorizations.Service) http.Handler {
	return newManagementAuthorizationCreateHandler(managementAuthorizationServiceAdapter{service: service}, managementAuthorizationScopeSelf)
}

func NewManagementAuthorizationUpdateHandler(service *managementauthorizations.Service) http.Handler {
	return newManagementAuthorizationUpdateHandler(managementAuthorizationServiceAdapter{service: service}, managementAuthorizationScopeAdmin)
}

func NewManagementMyAuthorizationUpdateHandler(service *managementauthorizations.Service) http.Handler {
	return newManagementAuthorizationUpdateHandler(managementAuthorizationServiceAdapter{service: service}, managementAuthorizationScopeSelf)
}

func NewManagementAuthorizationExpireUpdateHandler(service *managementauthorizations.Service) http.Handler {
	return newManagementAuthorizationExpireUpdateHandler(managementAuthorizationServiceAdapter{service: service}, managementAuthorizationScopeAdmin)
}

func NewManagementMyAuthorizationExpireUpdateHandler(service *managementauthorizations.Service) http.Handler {
	return newManagementAuthorizationExpireUpdateHandler(managementAuthorizationServiceAdapter{service: service}, managementAuthorizationScopeSelf)
}

func NewManagementAuthorizationReturnHandler(service *managementauthorizations.Service) http.Handler {
	return newManagementAuthorizationReturnHandler(managementAuthorizationServiceAdapter{service: service}, managementAuthorizationScopeAdmin)
}

func NewManagementMyAuthorizationReturnHandler(service *managementauthorizations.Service) http.Handler {
	return newManagementAuthorizationReturnHandler(managementAuthorizationServiceAdapter{service: service}, managementAuthorizationScopeSelf)
}

func NewManagementAuthorizationRevokeHandler(service *managementauthorizations.Service) http.Handler {
	return newManagementAuthorizationRevokeHandler(managementAuthorizationServiceAdapter{service: service}, managementAuthorizationScopeAdmin)
}

func NewManagementMyAuthorizationRevokeHandler(service *managementauthorizations.Service) http.Handler {
	return newManagementAuthorizationRevokeHandler(managementAuthorizationServiceAdapter{service: service}, managementAuthorizationScopeSelf)
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

func NewManagementAuthorizationUpdateHandlerWithOperationLog(service *managementauthorizations.Service, opts ManagementOperationLogOptions) http.Handler {
	return newManagementAuthorizationUpdateHandler(
		managementAuthorizationServiceAdapter{service: service},
		managementAuthorizationScopeAdmin,
		newManagementOperationLogOptions(opts),
	)
}

func NewManagementMyAuthorizationUpdateHandlerWithOperationLog(service *managementauthorizations.Service, opts ManagementOperationLogOptions) http.Handler {
	return newManagementAuthorizationUpdateHandler(
		managementAuthorizationServiceAdapter{service: service},
		managementAuthorizationScopeSelf,
		newManagementOperationLogOptions(opts),
	)
}

func NewManagementAuthorizationExpireUpdateHandlerWithOperationLog(service *managementauthorizations.Service, opts ManagementOperationLogOptions) http.Handler {
	return newManagementAuthorizationExpireUpdateHandler(
		managementAuthorizationServiceAdapter{service: service},
		managementAuthorizationScopeAdmin,
		newManagementOperationLogOptions(opts),
	)
}

func NewManagementMyAuthorizationExpireUpdateHandlerWithOperationLog(service *managementauthorizations.Service, opts ManagementOperationLogOptions) http.Handler {
	return newManagementAuthorizationExpireUpdateHandler(
		managementAuthorizationServiceAdapter{service: service},
		managementAuthorizationScopeSelf,
		newManagementOperationLogOptions(opts),
	)
}

func NewManagementAuthorizationReturnHandlerWithOperationLog(service *managementauthorizations.Service, opts ManagementOperationLogOptions) http.Handler {
	return newManagementAuthorizationReturnHandler(
		managementAuthorizationServiceAdapter{service: service},
		managementAuthorizationScopeAdmin,
		newManagementOperationLogOptions(opts),
	)
}

func NewManagementMyAuthorizationReturnHandlerWithOperationLog(service *managementauthorizations.Service, opts ManagementOperationLogOptions) http.Handler {
	return newManagementAuthorizationReturnHandler(
		managementAuthorizationServiceAdapter{service: service},
		managementAuthorizationScopeSelf,
		newManagementOperationLogOptions(opts),
	)
}

func NewManagementAuthorizationRevokeHandlerWithOperationLog(service *managementauthorizations.Service, opts ManagementOperationLogOptions) http.Handler {
	return newManagementAuthorizationRevokeHandler(
		managementAuthorizationServiceAdapter{service: service},
		managementAuthorizationScopeAdmin,
		newManagementOperationLogOptions(opts),
	)
}

func NewManagementMyAuthorizationRevokeHandlerWithOperationLog(service *managementauthorizations.Service, opts ManagementOperationLogOptions) http.Handler {
	return newManagementAuthorizationRevokeHandler(
		managementAuthorizationServiceAdapter{service: service},
		managementAuthorizationScopeSelf,
		newManagementOperationLogOptions(opts),
	)
}

func newManagementAuthorizationListHandler(service managementAuthorizationListService, scope managementAuthorizationScope) http.Handler {
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
		if scope == managementAuthorizationScopeAdmin && !managementauth.IsAdminRole(authContext.Role) {
			writeMessageError(w, http.StatusForbidden, "需要管理员权限")
			return
		}
		scopedSystemAccountID, validScope := managementAuthorizationListScope(authContext, r.URL.Query(), scope)
		if !validScope {
			writeMessageError(w, http.StatusBadRequest, "查询参数不合法")
			return
		}
		input, ok := parseManagementAuthorizationListQuery(w, r.URL.Query(), scope)
		if !ok {
			return
		}
		input.ActorSystemAccountID = authContext.SystemAccountID
		input.ActorRole = authContext.Role
		input.ScopedSystemAccountID = scopedSystemAccountID
		result, err := service.List(r, input)
		if errors.Is(err, managementauthorizations.ErrAuthorizationListInvalid) {
			writeMessageError(w, http.StatusBadRequest, "查询参数不合法")
			return
		}
		if err != nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		writeData(w, http.StatusOK, result)
	})
}

func newManagementAuthorizationDetailHandler(service managementAuthorizationGetService, scope managementAuthorizationScope) http.Handler {
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
		if scope == managementAuthorizationScopeAdmin && !managementauth.IsAdminRole(authContext.Role) {
			writeMessageError(w, http.StatusForbidden, "需要管理员权限")
			return
		}
		scopedSystemAccountID, validScope := managementAuthorizationListScope(authContext, r.URL.Query(), scope)
		if !validScope {
			writeMessageError(w, http.StatusBadRequest, "查询参数不合法")
			return
		}
		authorizationID := strings.TrimSpace(chi.URLParam(r, "id"))
		if authorizationID == "" {
			writeMessageError(w, http.StatusBadRequest, "授权记录 ID 不合法")
			return
		}
		result, found, err := service.Get(r, managementauthorizations.GetInput{
			AuthorizationID:       authorizationID,
			ActorSystemAccountID:  authContext.SystemAccountID,
			ActorRole:             authContext.Role,
			ScopedSystemAccountID: scopedSystemAccountID,
		})
		if errors.Is(err, managementauthorizations.ErrAuthorizationListInvalid) {
			writeMessageError(w, http.StatusBadRequest, "授权记录 ID 不合法")
			return
		}
		if err != nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		if !found {
			writeMessageError(w, http.StatusNotFound, "授权记录不存在")
			return
		}
		writeData(w, http.StatusOK, result)
	})
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

func newManagementAuthorizationUpdateHandler(service managementAuthorizationUpdateService, scope managementAuthorizationScope, logOptions ...managementOperationLogOptions) http.Handler {
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
		if scope == managementAuthorizationScopeAdmin && !managementauth.IsAdminRole(authContext.Role) {
			writeMessageError(w, http.StatusForbidden, "需要管理员权限")
			return
		}
		scopedSystemAccountID, validScope := managementAuthorizationOwnerScope(authContext, r.URL.Query(), scope)
		if !validScope {
			writeMessageError(w, http.StatusBadRequest, "查询参数不合法")
			return
		}
		authorizationID := strings.TrimSpace(chi.URLParam(r, "id"))
		if authorizationID == "" {
			writeMessageError(w, http.StatusBadRequest, "授权记录 ID 不合法")
			return
		}
		payload, ok := decodeManagementAuthorizationUpdatePayload(w, r)
		if !ok {
			return
		}
		result, found, err := service.Update(r, managementauthorizations.UpdateInput{
			AuthorizationID:       authorizationID,
			ActorSystemAccountID:  authContext.SystemAccountID,
			ActorRole:             authContext.Role,
			ScopedSystemAccountID: scopedSystemAccountID,
			HasStatus:             payload.HasStatus,
			Status:                payload.Status,
			HasExpiresAt:          payload.HasExpiresAt,
			ExpiresAt:             payload.ExpiresAt,
			HasLimits:             payload.HasLimits,
			Limits:                payload.Limits,
			LimitsIsNull:          payload.LimitsIsNull,
		})
		if errors.Is(err, managementauthorizations.ErrAuthorizationUpdateInvalid) {
			writeMessageError(w, http.StatusBadRequest, "修改授权参数不合法")
			return
		}
		if err != nil {
			writeMessageError(w, http.StatusBadRequest, err.Error())
			return
		}
		if !found {
			writeMessageError(w, http.StatusNotFound, "授权记录不存在")
			return
		}
		recordAuthorizationUpdateOperationLog(r, authContext, scope, result, payload, operationLogs)
		writeData(w, http.StatusOK, result)
	})
}

func newManagementAuthorizationExpireUpdateHandler(service managementAuthorizationUpdateService, scope managementAuthorizationScope, logOptions ...managementOperationLogOptions) http.Handler {
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
		if scope == managementAuthorizationScopeAdmin && !managementauth.IsAdminRole(authContext.Role) {
			writeMessageError(w, http.StatusForbidden, "需要管理员权限")
			return
		}
		scopedSystemAccountID, validScope := managementAuthorizationOwnerScope(authContext, r.URL.Query(), scope)
		if !validScope {
			writeMessageError(w, http.StatusBadRequest, "查询参数不合法")
			return
		}
		authorizationID := strings.TrimSpace(chi.URLParam(r, "id"))
		if authorizationID == "" {
			writeMessageError(w, http.StatusBadRequest, "授权记录 ID 不合法")
			return
		}
		payload, ok := decodeManagementAuthorizationExpireUpdatePayload(w, r)
		if !ok {
			return
		}
		result, found, err := service.Update(r, managementauthorizations.UpdateInput{
			AuthorizationID:       authorizationID,
			ActorSystemAccountID:  authContext.SystemAccountID,
			ActorRole:             authContext.Role,
			ScopedSystemAccountID: scopedSystemAccountID,
			HasExpiresAt:          true,
			ExpiresAt:             payload.ExpiresAt,
			HasLimits:             payload.HasLimits,
			Limits:                payload.Limits,
			LimitsIsNull:          payload.LimitsIsNull,
		})
		if errors.Is(err, managementauthorizations.ErrAuthorizationUpdateInvalid) {
			writeMessageError(w, http.StatusBadRequest, "修改授权参数不合法")
			return
		}
		if err != nil {
			writeMessageError(w, http.StatusBadRequest, err.Error())
			return
		}
		if !found {
			writeMessageError(w, http.StatusNotFound, "授权记录不存在")
			return
		}
		recordAuthorizationExpireUpdateOperationLog(r, authContext, scope, result, payload, operationLogs)
		writeData(w, http.StatusOK, result)
	})
}

func newManagementAuthorizationReturnHandler(service managementAuthorizationReturnService, scope managementAuthorizationScope, logOptions ...managementOperationLogOptions) http.Handler {
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
		if scope == managementAuthorizationScopeAdmin && !managementauth.IsAdminRole(authContext.Role) {
			writeMessageError(w, http.StatusForbidden, "需要管理员权限")
			return
		}
		granteeSystemAccountID, validScope := managementAuthorizationGranteeScope(authContext, r.URL.Query(), scope)
		if !validScope {
			writeMessageError(w, http.StatusBadRequest, "查询参数不合法")
			return
		}
		authorizationID := strings.TrimSpace(chi.URLParam(r, "id"))
		if authorizationID == "" {
			writeMessageError(w, http.StatusBadRequest, "授权记录 ID 不合法")
			return
		}
		result, found, err := service.Return(r, managementauthorizations.ReturnInput{
			AuthorizationID:        authorizationID,
			GranteeSystemAccountID: granteeSystemAccountID,
			ActorSystemAccountID:   authContext.SystemAccountID,
		})
		if errors.Is(err, managementauthorizations.ErrAuthorizationReturnInvalid) {
			writeMessageError(w, http.StatusBadRequest, "授权记录 ID 不合法")
			return
		}
		if err != nil {
			writeMessageError(w, http.StatusBadRequest, err.Error())
			return
		}
		if !found {
			writeMessageError(w, http.StatusNotFound, "授权记录不存在")
			return
		}
		recordAuthorizationReturnOperationLog(r, authContext, scope, result, operationLogs)
		w.WriteHeader(http.StatusNoContent)
	})
}

func newManagementAuthorizationRevokeHandler(service managementAuthorizationRevokeService, scope managementAuthorizationScope, logOptions ...managementOperationLogOptions) http.Handler {
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
		if scope == managementAuthorizationScopeAdmin && !managementauth.IsAdminRole(authContext.Role) {
			writeMessageError(w, http.StatusForbidden, "需要管理员权限")
			return
		}
		scopedSystemAccountID, validScope := managementAuthorizationOwnerScope(authContext, r.URL.Query(), scope)
		if !validScope {
			writeMessageError(w, http.StatusBadRequest, "查询参数不合法")
			return
		}
		authorizationID := strings.TrimSpace(chi.URLParam(r, "id"))
		if authorizationID == "" {
			writeMessageError(w, http.StatusBadRequest, "授权记录 ID 不合法")
			return
		}
		result, found, err := service.Revoke(r, managementauthorizations.RevokeInput{
			AuthorizationID:       authorizationID,
			ActorSystemAccountID:  authContext.SystemAccountID,
			ActorRole:             authContext.Role,
			ScopedSystemAccountID: scopedSystemAccountID,
		})
		if errors.Is(err, managementauthorizations.ErrAuthorizationRevokeInvalid) {
			writeMessageError(w, http.StatusBadRequest, "授权记录 ID 不合法")
			return
		}
		if err != nil {
			writeMessageError(w, http.StatusBadRequest, err.Error())
			return
		}
		if !found {
			writeMessageError(w, http.StatusNotFound, "授权记录不存在")
			return
		}
		recordAuthorizationRevokeOperationLog(r, authContext, scope, result, operationLogs)
		writeData(w, http.StatusOK, result)
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

func managementAuthorizationListScope(authContext managementauth.Context, values url.Values, scope managementAuthorizationScope) (string, bool) {
	switch scope {
	case managementAuthorizationScopeSelf:
		return authContext.SystemAccountID, true
	case managementAuthorizationScopeAdmin:
		return managementAuthorizationQuerySystemAccountID(values)
	default:
		return "", false
	}
}

func managementAuthorizationGranteeScope(authContext managementauth.Context, values url.Values, scope managementAuthorizationScope) (string, bool) {
	switch scope {
	case managementAuthorizationScopeSelf:
		return authContext.SystemAccountID, true
	case managementAuthorizationScopeAdmin:
		selected, valid := managementAuthorizationQuerySystemAccountID(values)
		if !valid {
			return "", false
		}
		if selected == "" {
			return authContext.SystemAccountID, true
		}
		return selected, true
	default:
		return "", false
	}
}

func managementAuthorizationQuerySystemAccountID(values url.Values) (string, bool) {
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
}

func parseManagementAuthorizationListQuery(w http.ResponseWriter, values url.Values, scope managementAuthorizationScope) (managementauthorizations.ListInput, bool) {
	resourceType, err := optionalQueryEnum(values, "resourceType", []string{"account", "group"})
	if err != nil {
		writeMessageError(w, http.StatusBadRequest, "查询参数不合法")
		return managementauthorizations.ListInput{}, false
	}
	status, err := optionalQueryEnum(values, "status", []string{"all", "active", "paused", "expired", "revoked", "returned"})
	if err != nil {
		writeMessageError(w, http.StatusBadRequest, "查询参数不合法")
		return managementauthorizations.ListInput{}, false
	}
	direction, err := optionalQueryEnum(values, "direction", []string{"all", "outbound", "inbound"})
	if err != nil {
		writeMessageError(w, http.StatusBadRequest, "查询参数不合法")
		return managementauthorizations.ListInput{}, false
	}
	sourceType, err := optionalQueryEnum(values, "sourceType", []string{"all", "manual", "team"})
	if err != nil {
		writeMessageError(w, http.StatusBadRequest, "查询参数不合法")
		return managementauthorizations.ListInput{}, false
	}
	keyword, err := optionalQueryString(values, "keyword", 0, 120)
	if err != nil {
		writeMessageError(w, http.StatusBadRequest, "查询参数不合法")
		return managementauthorizations.ListInput{}, false
	}
	resourceID, err := optionalQueryString(values, "resourceId", 1, 160)
	if err != nil {
		writeMessageError(w, http.StatusBadRequest, "查询参数不合法")
		return managementauthorizations.ListInput{}, false
	}
	ownerID, err := optionalQueryString(values, "resourceOwnerSystemAccountId", 1, 160)
	if err != nil {
		writeMessageError(w, http.StatusBadRequest, "查询参数不合法")
		return managementauthorizations.ListInput{}, false
	}
	granteeID, err := optionalQueryString(values, "granteeSystemAccountId", 1, 160)
	if err != nil {
		writeMessageError(w, http.StatusBadRequest, "查询参数不合法")
		return managementauthorizations.ListInput{}, false
	}
	teamID, err := optionalQueryString(values, "teamId", 1, 160)
	if err != nil {
		writeMessageError(w, http.StatusBadRequest, "查询参数不合法")
		return managementauthorizations.ListInput{}, false
	}
	page, err := optionalQueryInt(values, "page", 1, 0)
	if err != nil {
		writeMessageError(w, http.StatusBadRequest, "查询参数不合法")
		return managementauthorizations.ListInput{}, false
	}
	pageSize, err := optionalQueryInt(values, "pageSize", 1, 500)
	if err != nil {
		writeMessageError(w, http.StatusBadRequest, "查询参数不合法")
		return managementauthorizations.ListInput{}, false
	}
	if !validateManagementAuthorizationListDate(values, "startDate", w) || !validateManagementAuthorizationListDate(values, "endDate", w) {
		return managementauthorizations.ListInput{}, false
	}
	if status == "all" {
		status = ""
	}
	if sourceType == "all" {
		sourceType = ""
	}
	if direction == "all" || scope == managementAuthorizationScopeAdmin {
		direction = ""
	}
	return managementauthorizations.ListInput{
		ResourceType:                 resourceType,
		ResourceID:                   resourceID,
		ResourceOwnerSystemAccountID: ownerID,
		GranteeSystemAccountID:       granteeID,
		TeamID:                       teamID,
		Status:                       status,
		Direction:                    direction,
		SourceType:                   sourceType,
		Keyword:                      keyword,
		Page:                         page,
		PageSize:                     pageSize,
	}, true
}

func validateManagementAuthorizationListDate(values url.Values, key string, w http.ResponseWriter) bool {
	value, err := optionalQueryString(values, key, 1, 10)
	if err != nil {
		writeMessageError(w, http.StatusBadRequest, "查询参数不合法")
		return false
	}
	if value == "" {
		return true
	}
	if _, err := time.Parse("2006-01-02", value); err != nil {
		writeMessageError(w, http.StatusBadRequest, "查询参数不合法")
		return false
	}
	return true
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

type managementAuthorizationUpdatePayload struct {
	Status       string
	HasStatus    bool
	ExpiresAt    *string
	HasExpiresAt bool
	Limits       map[string]any
	HasLimits    bool
	LimitsIsNull bool
}

type managementAuthorizationExpireUpdatePayload struct {
	ExpiresAt    *string
	HasLimits    bool
	Limits       map[string]any
	LimitsIsNull bool
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

func decodeManagementAuthorizationUpdatePayload(w http.ResponseWriter, r *http.Request) (managementAuthorizationUpdatePayload, bool) {
	const message = "修改授权参数不合法"
	decoder := json.NewDecoder(r.Body)
	decoder.UseNumber()
	var raw map[string]json.RawMessage
	if err := decoder.Decode(&raw); err != nil {
		writeMessageError(w, http.StatusBadRequest, message)
		return managementAuthorizationUpdatePayload{}, false
	}
	if len(raw) == 0 {
		writeMessageError(w, http.StatusBadRequest, message)
		return managementAuthorizationUpdatePayload{}, false
	}
	if err := decoder.Decode(&struct{}{}); err == nil {
		writeMessageError(w, http.StatusBadRequest, message)
		return managementAuthorizationUpdatePayload{}, false
	}
	allowed := map[string]bool{
		"status":    true,
		"expiresAt": true,
		"limits":    true,
	}
	for key := range raw {
		if !allowed[key] {
			writeMessageError(w, http.StatusBadRequest, message)
			return managementAuthorizationUpdatePayload{}, false
		}
	}
	var payload managementAuthorizationUpdatePayload
	var ok bool
	if _, exists := raw["status"]; exists {
		if payload.Status, ok = rawStringFieldWithMessage(w, raw, "status", false, message); !ok {
			return managementAuthorizationUpdatePayload{}, false
		}
		payload.HasStatus = true
	}
	if rawExpiresAt, exists := raw["expiresAt"]; exists {
		payload.HasExpiresAt = true
		if !bytes.Equal(bytes.TrimSpace(rawExpiresAt), []byte("null")) {
			var text string
			if err := json.Unmarshal(rawExpiresAt, &text); err != nil {
				writeMessageError(w, http.StatusBadRequest, message)
				return managementAuthorizationUpdatePayload{}, false
			}
			payload.ExpiresAt = &text
		}
	}
	if rawLimits, exists := raw["limits"]; exists {
		payload.HasLimits = true
		if bytes.Equal(bytes.TrimSpace(rawLimits), []byte("null")) {
			payload.LimitsIsNull = true
		} else {
			limits, ok := rawObjectFieldWithMessage(w, rawLimits, message)
			if !ok {
				return managementAuthorizationUpdatePayload{}, false
			}
			payload.Limits = limits
		}
	}
	if !payload.HasStatus && !payload.HasExpiresAt && !payload.HasLimits {
		writeMessageError(w, http.StatusBadRequest, message)
		return managementAuthorizationUpdatePayload{}, false
	}
	return payload, true
}

func decodeManagementAuthorizationExpireUpdatePayload(w http.ResponseWriter, r *http.Request) (managementAuthorizationExpireUpdatePayload, bool) {
	const message = "修改授权参数不合法"
	decoder := json.NewDecoder(r.Body)
	decoder.UseNumber()
	var raw map[string]json.RawMessage
	if err := decoder.Decode(&raw); err != nil {
		writeMessageError(w, http.StatusBadRequest, message)
		return managementAuthorizationExpireUpdatePayload{}, false
	}
	if err := decoder.Decode(&struct{}{}); err == nil {
		writeMessageError(w, http.StatusBadRequest, message)
		return managementAuthorizationExpireUpdatePayload{}, false
	}
	allowed := map[string]bool{
		"expiresAt": true,
		"limits":    true,
	}
	for key := range raw {
		if !allowed[key] {
			writeMessageError(w, http.StatusBadRequest, message)
			return managementAuthorizationExpireUpdatePayload{}, false
		}
	}
	rawExpiresAt, exists := raw["expiresAt"]
	if !exists {
		writeMessageError(w, http.StatusBadRequest, message)
		return managementAuthorizationExpireUpdatePayload{}, false
	}
	var payload managementAuthorizationExpireUpdatePayload
	if !bytes.Equal(bytes.TrimSpace(rawExpiresAt), []byte("null")) {
		var text string
		if err := json.Unmarshal(rawExpiresAt, &text); err != nil {
			writeMessageError(w, http.StatusBadRequest, message)
			return managementAuthorizationExpireUpdatePayload{}, false
		}
		payload.ExpiresAt = &text
	}
	if rawLimits, exists := raw["limits"]; exists {
		payload.HasLimits = true
		if bytes.Equal(bytes.TrimSpace(rawLimits), []byte("null")) {
			payload.LimitsIsNull = true
		} else {
			limits, ok := rawObjectFieldWithMessage(w, rawLimits, message)
			if !ok {
				return managementAuthorizationExpireUpdatePayload{}, false
			}
			payload.Limits = limits
		}
	}
	return payload, true
}

func rawStringField(w http.ResponseWriter, raw map[string]json.RawMessage, key string, required bool) (string, bool) {
	return rawStringFieldWithMessage(w, raw, key, required, "授权参数不合法")
}

func rawStringFieldWithMessage(w http.ResponseWriter, raw map[string]json.RawMessage, key string, required bool, message string) (string, bool) {
	value, exists := raw[key]
	if !exists {
		if required {
			writeMessageError(w, http.StatusBadRequest, message)
			return "", false
		}
		return "", true
	}
	if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
		writeMessageError(w, http.StatusBadRequest, message)
		return "", false
	}
	var text string
	if err := json.Unmarshal(value, &text); err != nil {
		writeMessageError(w, http.StatusBadRequest, message)
		return "", false
	}
	return text, true
}

func rawObjectField(w http.ResponseWriter, raw json.RawMessage) (map[string]any, bool) {
	return rawObjectFieldWithMessage(w, raw, "授权参数不合法")
}

func rawObjectFieldWithMessage(w http.ResponseWriter, raw json.RawMessage, message string) (map[string]any, bool) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value map[string]any
	if err := decoder.Decode(&value); err != nil || value == nil {
		writeMessageError(w, http.StatusBadRequest, message)
		return nil, false
	}
	if err := decoder.Decode(&struct{}{}); err == nil {
		writeMessageError(w, http.StatusBadRequest, message)
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

func recordAuthorizationUpdateOperationLog(
	r *http.Request,
	authContext managementauth.Context,
	scope managementAuthorizationScope,
	result managementauthorizations.Summary,
	payload managementAuthorizationUpdatePayload,
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
	statusCode := http.StatusOK
	mode := managementAuthorizationOperationMode(authContext, result.ResourceOwnerSystemAccountID)
	if scope == managementAuthorizationScopeSelf {
		mode = "self"
	}
	resourceName := result.ResourceName
	if resourceName == "" {
		resourceName = result.ResourceID
	}
	changes := make([]port.OperationLogChange, 0, 3)
	if payload.HasStatus {
		changes = append(changes, port.OperationLogChange{
			Field: "status",
			Label: "状态",
			After: result.Status,
		})
	}
	if payload.HasExpiresAt {
		changes = append(changes, port.OperationLogChange{
			Field: "expiresAt",
			Label: "过期时间",
			After: result.ExpiresAt,
		})
	}
	if payload.HasLimits {
		changes = append(changes, port.OperationLogChange{
			Field: "limits",
			Label: "额度限制",
			After: result.Limits,
		})
	}
	input := port.OperationLogInput{
		ID:                            newLogID(),
		TraceID:                       requestIDFromContext(r.Context()),
		ActorSystemAccountID:          authContext.SystemAccountID,
		ActorUsername:                 authContext.Username,
		ActorDisplayName:              authContext.DisplayName,
		ActorRole:                     authContext.Role,
		OperationScopeSystemAccountID: result.ResourceOwnerSystemAccountID,
		Mode:                          mode,
		Module:                        "authorizations",
		Action:                        "update",
		OperationKey:                  "authorizations.update",
		ResourceType:                  "authorization",
		ResourceID:                    result.ID,
		ResourceName:                  resourceName,
		Summary:                       "更新资源授权：" + resourceName + " -> " + managementAuthorizationGranteeName(result),
		DetailLevel:                   "full",
		VisibilityScope:               "targeted",
		Changes:                       changes,
		Targets:                       managementAuthorizationOperationTargets(result),
		Viewers:                       managementAuthorizationOperationViewers(result),
		Method:                        r.Method,
		Path:                          r.URL.Path,
		StatusCode:                    &statusCode,
		ClientIP:                      opts.clientIP.FromRequest(r),
		UserAgent:                     r.UserAgent(),
		CreatedAt:                     now().UTC(),
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

func recordAuthorizationExpireUpdateOperationLog(
	r *http.Request,
	authContext managementauth.Context,
	scope managementAuthorizationScope,
	result managementauthorizations.Summary,
	payload managementAuthorizationExpireUpdatePayload,
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
	statusCode := http.StatusOK
	mode := managementAuthorizationOperationMode(authContext, result.ResourceOwnerSystemAccountID)
	if scope == managementAuthorizationScopeSelf {
		mode = "self"
	}
	resourceName := result.ResourceName
	if resourceName == "" {
		resourceName = result.ResourceID
	}
	changes := []port.OperationLogChange{
		{
			Field: "status",
			Label: "状态",
			After: result.Status,
		},
		{
			Field: "expiresAt",
			Label: "过期时间",
			After: result.ExpiresAt,
		},
	}
	if payload.HasLimits {
		changes = append(changes, port.OperationLogChange{
			Field: "limits",
			Label: "额度限制",
			After: result.Limits,
		})
	}
	input := port.OperationLogInput{
		ID:                            newLogID(),
		TraceID:                       requestIDFromContext(r.Context()),
		ActorSystemAccountID:          authContext.SystemAccountID,
		ActorUsername:                 authContext.Username,
		ActorDisplayName:              authContext.DisplayName,
		ActorRole:                     authContext.Role,
		OperationScopeSystemAccountID: result.ResourceOwnerSystemAccountID,
		Mode:                          mode,
		Module:                        "authorizations",
		Action:                        "update_expire",
		OperationKey:                  "authorizations.update_expire",
		ResourceType:                  "authorization",
		ResourceID:                    result.ID,
		ResourceName:                  resourceName,
		Summary:                       "更新授权有效期：" + resourceName + " -> " + managementAuthorizationGranteeName(result),
		DetailLevel:                   "full",
		VisibilityScope:               "targeted",
		Changes:                       changes,
		Targets:                       managementAuthorizationOperationTargets(result),
		Viewers:                       managementAuthorizationOperationViewers(result),
		Method:                        r.Method,
		Path:                          r.URL.Path,
		StatusCode:                    &statusCode,
		ClientIP:                      opts.clientIP.FromRequest(r),
		UserAgent:                     r.UserAgent(),
		CreatedAt:                     now().UTC(),
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

func recordAuthorizationReturnOperationLog(
	r *http.Request,
	authContext managementauth.Context,
	scope managementAuthorizationScope,
	result managementauthorizations.Summary,
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
	statusCode := http.StatusNoContent
	mode := "self"
	if scope == managementAuthorizationScopeAdmin {
		mode = "admin"
	}
	input := port.OperationLogInput{
		ID:                            newLogID(),
		TraceID:                       requestIDFromContext(r.Context()),
		ActorSystemAccountID:          authContext.SystemAccountID,
		ActorUsername:                 authContext.Username,
		ActorDisplayName:              authContext.DisplayName,
		ActorRole:                     authContext.Role,
		OperationScopeSystemAccountID: result.GranteeSystemAccountID,
		Mode:                          mode,
		Module:                        "authorizations",
		Action:                        "return",
		OperationKey:                  "authorizations.return",
		ResourceType:                  "authorization",
		ResourceID:                    result.ID,
		ResourceName:                  result.ResourceID,
		Summary:                       "归还授权使用权：" + result.ResourceID,
		DetailLevel:                   "full",
		VisibilityScope:               "targeted",
		Changes: []port.OperationLogChange{{
			Field:  "returned",
			Label:  "归还授权",
			Before: false,
			After:  true,
		}},
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

func recordAuthorizationRevokeOperationLog(
	r *http.Request,
	authContext managementauth.Context,
	scope managementAuthorizationScope,
	result managementauthorizations.Summary,
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
	statusCode := http.StatusOK
	mode := managementAuthorizationOperationMode(authContext, result.ResourceOwnerSystemAccountID)
	if scope == managementAuthorizationScopeSelf {
		mode = "self"
	}
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
		Mode:                          mode,
		Module:                        "authorizations",
		Action:                        "revoke",
		OperationKey:                  "authorizations.revoke",
		ResourceType:                  "authorization",
		ResourceID:                    result.ID,
		ResourceName:                  resourceName,
		Summary:                       "回收资源授权：" + resourceName + " -> " + managementAuthorizationGranteeName(result),
		DetailLevel:                   "full",
		VisibilityScope:               "targeted",
		Changes: []port.OperationLogChange{{
			Field:  "revoked",
			Label:  "回收状态",
			Before: false,
			After:  true,
		}},
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
