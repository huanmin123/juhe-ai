package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"juhe-ai/backend-go/internal/modules/managementapikeys"
	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/store/port"
)

const managementAPIKeyRefreshMaxBodyBytes = 256 << 10

type managementAPIKeySecretService interface {
	Reveal(r *http.Request, input managementapikeys.SecretInput) (managementapikeys.SecretResult, error)
	Refresh(r *http.Request, input managementapikeys.SecretInput) (managementapikeys.RefreshResult, error)
}

type managementAPIKeySecretServiceAdapter struct {
	service *managementapikeys.Service
}

func (s managementAPIKeySecretServiceAdapter) Reveal(
	r *http.Request,
	input managementapikeys.SecretInput,
) (managementapikeys.SecretResult, error) {
	return s.service.Reveal(r.Context(), input)
}

func (s managementAPIKeySecretServiceAdapter) Refresh(
	r *http.Request,
	input managementapikeys.SecretInput,
) (managementapikeys.RefreshResult, error) {
	return s.service.Refresh(r.Context(), input)
}

func NewManagementAPIKeySecretHandlerWithOperationLog(
	service *managementapikeys.Service,
	opts ManagementOperationLogOptions,
) http.Handler {
	return newManagementAPIKeySecretHandler(
		managementAPIKeySecretServiceFrom(service),
		managementAPIKeyScopeAdmin,
		newManagementOperationLogOptions(opts),
	)
}

func NewManagementMyAPIKeySecretHandlerWithOperationLog(
	service *managementapikeys.Service,
	opts ManagementOperationLogOptions,
) http.Handler {
	return newManagementAPIKeySecretHandler(
		managementAPIKeySecretServiceFrom(service),
		managementAPIKeyScopeSelf,
		newManagementOperationLogOptions(opts),
	)
}

func NewManagementAPIKeyRefreshHandlerWithOperationLog(
	service *managementapikeys.Service,
	opts ManagementOperationLogOptions,
) http.Handler {
	return newManagementAPIKeyRefreshHandler(
		managementAPIKeySecretServiceFrom(service),
		managementAPIKeyScopeAdmin,
		newManagementOperationLogOptions(opts),
	)
}

func NewManagementMyAPIKeyRefreshHandlerWithOperationLog(
	service *managementapikeys.Service,
	opts ManagementOperationLogOptions,
) http.Handler {
	return newManagementAPIKeyRefreshHandler(
		managementAPIKeySecretServiceFrom(service),
		managementAPIKeyScopeSelf,
		newManagementOperationLogOptions(opts),
	)
}

func managementAPIKeySecretServiceFrom(
	service *managementapikeys.Service,
) managementAPIKeySecretService {
	if service == nil {
		return nil
	}
	return managementAPIKeySecretServiceAdapter{service: service}
}

func newManagementAPIKeySecretHandler(
	service managementAPIKeySecretService,
	scope managementAPIKeyScope,
	logOptions managementOperationLogOptions,
) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authContext, ok := ManagementAuthContextFromRequest(r)
		if !ok || strings.TrimSpace(authContext.SystemAccountID) == "" {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		if scope == managementAPIKeyScopeAdmin && !managementauth.IsAdminRole(authContext.Role) {
			writeMessageError(w, http.StatusForbidden, "需要管理员权限")
			return
		}
		if service == nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}

		result, err := service.Reveal(r, managementAPIKeySecretInput(authContext, r, scope))
		if errors.Is(err, managementapikeys.ErrAPIKeyNotFound) {
			writeMessageError(w, http.StatusNotFound, "API Key 不存在")
			return
		}
		if err != nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}

		setManagementAPIKeySecretHeaders(w)
		writeData(w, http.StatusOK, result)
		recordManagementAPIKeySecretOperationLog(r, authContext, scope, result, logOptions)
	})
}

func newManagementAPIKeyRefreshHandler(
	service managementAPIKeySecretService,
	scope managementAPIKeyScope,
	logOptions managementOperationLogOptions,
) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authContext, ok := ManagementAuthContextFromRequest(r)
		if !ok || strings.TrimSpace(authContext.SystemAccountID) == "" {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		if scope == managementAPIKeyScopeAdmin && !managementauth.IsAdminRole(authContext.Role) {
			writeMessageError(w, http.StatusForbidden, "需要管理员权限")
			return
		}
		if service == nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		if !decodeManagementAPIKeyRefreshBody(w, r) {
			return
		}

		result, err := service.Refresh(r, managementAPIKeySecretInput(authContext, r, scope))
		if errors.Is(err, managementapikeys.ErrAPIKeyNotFound) {
			writeMessageError(w, http.StatusNotFound, "API Key 不存在")
			return
		}
		if err != nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}

		setManagementAPIKeySecretHeaders(w)
		writeJSON(w, http.StatusOK, DataResponse{
			Data:    result,
			Message: "API Key 密钥已刷新，请立即复制完整密钥",
		})
		recordManagementAPIKeyRefreshOperationLog(r, authContext, scope, result, logOptions)
	})
}

func managementAPIKeySecretInput(
	authContext managementauth.Context,
	r *http.Request,
	scope managementAPIKeyScope,
) managementapikeys.SecretInput {
	input := managementapikeys.SecretInput{
		ActorSystemAccountID: authContext.SystemAccountID,
		ActorRole:            authContext.Role,
		APIKeyID:             chi.URLParam(r, "id"),
	}
	switch scope {
	case managementAPIKeyScopeAdmin:
		systemAccountID := firstManagementQueryText(r.URL.Query(), "systemAccountId")
		if systemAccountID != "all" {
			input.SystemAccountID = systemAccountID
		}
	case managementAPIKeyScopeSelf:
		input.SystemAccountID = authContext.SystemAccountID
		input.SelfOnly = true
	}
	return input
}

func decodeManagementAPIKeyRefreshBody(w http.ResponseWriter, r *http.Request) bool {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, managementAPIKeyRefreshMaxBodyBytes))
	if err != nil {
		writeManagementGlobalSettingsBodyError(w, err)
		return false
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return true
	}
	var payload map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(&payload); err != nil {
		writeManagementGlobalSettingsBodyError(w, err)
		return false
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		writeManagementGlobalSettingsTrailingBodyError(w, err)
		return false
	}
	if payload == nil || len(payload) != 0 {
		writeMessageError(w, http.StatusBadRequest, "请求体无效")
		return false
	}
	return true
}

func setManagementAPIKeySecretHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
}

func recordManagementAPIKeySecretOperationLog(
	r *http.Request,
	authContext managementauth.Context,
	scope managementAPIKeyScope,
	result managementapikeys.SecretResult,
	opts managementOperationLogOptions,
) {
	recordManagementAPIKeySecretChangeOperationLog(
		r,
		authContext,
		scope,
		result.OwnerSystemAccountID,
		result.APIKeyID,
		result.Name,
		"reveal_secret",
		"api_keys.reveal_secret",
		"查看 API Key 完整密钥："+result.Name,
		nil,
		result.KeyMarker,
		opts,
	)
}

func recordManagementAPIKeyRefreshOperationLog(
	r *http.Request,
	authContext managementauth.Context,
	scope managementAPIKeyScope,
	result managementapikeys.RefreshResult,
	opts managementOperationLogOptions,
) {
	recordManagementAPIKeySecretChangeOperationLog(
		r,
		authContext,
		scope,
		result.OwnerSystemAccountID,
		result.ID,
		result.Name,
		"refresh_key",
		"api_keys.refresh_key",
		"刷新 API Key 密钥："+result.Name,
		result.PreviousKeyMarker,
		result.KeyMarker,
		opts,
	)
}

func recordManagementAPIKeySecretChangeOperationLog(
	r *http.Request,
	authContext managementauth.Context,
	scope managementAPIKeyScope,
	ownerSystemAccountID string,
	apiKeyID string,
	apiKeyName string,
	action string,
	operationKey string,
	summary string,
	before any,
	after any,
	opts managementOperationLogOptions,
) {
	if opts.client == nil {
		return
	}
	now := opts.now
	if now == nil {
		now = time.Now
	}
	mode := "self"
	if scope == managementAPIKeyScopeAdmin {
		mode = "admin"
	}
	statusCode := http.StatusOK
	input := port.OperationLogInput{
		ID:                            opts.newLogID(),
		TraceID:                       requestIDFromContext(r.Context()),
		ActorSystemAccountID:          authContext.SystemAccountID,
		ActorUsername:                 authContext.Username,
		ActorDisplayName:              authContext.DisplayName,
		ActorRole:                     authContext.Role,
		OperationScopeSystemAccountID: ownerSystemAccountID,
		Mode:                          mode,
		Module:                        "api_keys",
		Action:                        action,
		OperationKey:                  operationKey,
		ResourceType:                  "api_key",
		ResourceID:                    apiKeyID,
		ResourceName:                  apiKeyName,
		Summary:                       summary,
		DetailLevel:                   "full",
		VisibilityScope:               "targeted",
		Changes: []port.OperationLogChange{{
			Field:  "key",
			Label:  "密钥标识",
			Before: before,
			After:  after,
		}},
		Method:     r.Method,
		Path:       r.URL.Path,
		StatusCode: &statusCode,
		ClientIP:   opts.clientIP.FromRequest(r),
		UserAgent:  r.UserAgent(),
		Viewers: []port.OperationLogViewerInput{{
			SystemAccountID:  ownerSystemAccountID,
			VisibilityReason: "resource_owner",
			DetailLevel:      "full",
		}},
		CreatedAt: now().UTC(),
	}
	enqueueManagementOperationLog(r.Context(), opts, input)
}

var _ managementAPIKeySecretService = managementAPIKeySecretServiceAdapter{}
