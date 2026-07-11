package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementsettings"
	"juhe-ai/backend-go/internal/store/port"
	"juhe-ai/backend-go/internal/systemsettings"
)

func managementSettingsJSONBodyMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		limited := http.MaxBytesReader(w, r.Body, managementGlobalSettingsMaxBodyBytes)
		body, err := io.ReadAll(limited)
		if err != nil {
			writeManagementGlobalSettingsBodyError(w, err)
			return
		}
		if !json.Valid(body) {
			writeMessageError(w, http.StatusBadRequest, "请求体无效")
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))
		next.ServeHTTP(w, r)
	})
}

type managementSystemSettingsReadService interface {
	Get(ctx context.Context) (systemsettings.Snapshot, error)
}

type managementSystemSettingsUpdateService interface {
	Update(ctx context.Context, input managementsettings.SystemUpdateInput) (managementsettings.SystemUpdateResult, error)
}

type managementSystemSettingsServiceAdapter struct {
	service *managementsettings.SystemService
}

func (s managementSystemSettingsServiceAdapter) Get(ctx context.Context) (systemsettings.Snapshot, error) {
	if s.service == nil {
		return systemsettings.Snapshot{}, errors.New("management system settings service is required")
	}
	return s.service.Get(ctx)
}

func (s managementSystemSettingsServiceAdapter) Update(
	ctx context.Context,
	input managementsettings.SystemUpdateInput,
) (managementsettings.SystemUpdateResult, error) {
	if s.service == nil {
		return managementsettings.SystemUpdateResult{}, errors.New("management system settings service is required")
	}
	return s.service.Update(ctx, input)
}

func NewManagementSystemSettingsHandler(service *managementsettings.SystemService) http.Handler {
	return newManagementSystemSettingsHandler(managementSystemSettingsServiceAdapter{service: service})
}

func NewManagementSystemSettingsUpdateHandler(service *managementsettings.SystemService) http.Handler {
	return newManagementSystemSettingsUpdateHandler(managementSystemSettingsServiceAdapter{service: service})
}

func NewManagementSystemSettingsUpdateHandlerWithOperationLog(
	service *managementsettings.SystemService,
	opts ManagementOperationLogOptions,
) http.Handler {
	return newManagementSystemSettingsUpdateHandler(
		managementSystemSettingsServiceAdapter{service: service},
		newManagementOperationLogOptions(opts),
	)
}

func newManagementSystemSettingsHandler(service managementSystemSettingsReadService) http.Handler {
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

		settings, err := service.Get(r.Context())
		if err != nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		writeData(w, http.StatusOK, settings)
	})
}

func newManagementSystemSettingsUpdateHandler(
	service managementSystemSettingsUpdateService,
	logOptions ...managementOperationLogOptions,
) http.Handler {
	operationLogs := effectiveManagementOperationLogOptions(logOptions)
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

		input, ok := decodeManagementSystemSettingsUpdateBody(w, r)
		if !ok {
			return
		}
		result, err := service.Update(r.Context(), input)
		if err != nil {
			writeManagementSystemSettingsUpdateError(w, err)
			return
		}

		recordSystemSettingsUpdateOperationLog(r, authContext, input, result, operationLogs)
		writeData(w, http.StatusOK, result.Settings)
	})
}

func decodeManagementSystemSettingsUpdateBody(
	w http.ResponseWriter,
	r *http.Request,
) (managementsettings.SystemUpdateInput, bool) {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, managementGlobalSettingsMaxBodyBytes))
	var payload map[string]json.RawMessage
	if err := decoder.Decode(&payload); err != nil {
		writeManagementGlobalSettingsBodyError(w, err)
		return managementsettings.SystemUpdateInput{}, false
	}
	if payload == nil {
		writeMessageError(w, http.StatusBadRequest, "请求体必须是对象")
		return managementsettings.SystemUpdateInput{}, false
	}
	var extra struct{}
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		writeManagementGlobalSettingsTrailingBodyError(w, err)
		return managementsettings.SystemUpdateInput{}, false
	}
	if len(payload) == 0 {
		writeMessageError(w, http.StatusBadRequest, systemsettings.ErrPatchEmpty.Error())
		return managementsettings.SystemUpdateInput{}, false
	}
	return managementsettings.SystemUpdateInput{Values: payload}, true
}

func writeManagementSystemSettingsUpdateError(w http.ResponseWriter, err error) {
	var validationErr *systemsettings.ValidationError
	switch {
	case errors.As(err, &validationErr):
		writeMessageError(w, http.StatusBadRequest, validationErr.Error())
	case errors.Is(err, systemsettings.ErrPatchEmpty):
		writeMessageError(w, http.StatusBadRequest, systemsettings.ErrPatchEmpty.Error())
	case errors.Is(err, managementsettings.ErrUsageStatsTimezoneOnlineUpdateUnsupported):
		writeMessageError(
			w,
			http.StatusBadRequest,
			managementsettings.ErrUsageStatsTimezoneOnlineUpdateUnsupported.Error(),
		)
	default:
		writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
	}
}

func recordSystemSettingsUpdateOperationLog(
	r *http.Request,
	authContext managementauth.Context,
	input managementsettings.SystemUpdateInput,
	result managementsettings.SystemUpdateResult,
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
	logInput := port.OperationLogInput{
		ID:                   newLogID(),
		TraceID:              requestIDFromContext(r.Context()),
		ActorSystemAccountID: authContext.SystemAccountID,
		ActorUsername:        authContext.Username,
		ActorDisplayName:     authContext.DisplayName,
		ActorRole:            authContext.Role,
		Mode:                 "admin",
		Module:               "settings",
		Action:               "update_settings",
		OperationKey:         "settings.update",
		ResourceType:         "system_settings",
		ResourceID:           "system",
		ResourceName:         "系统运行设置",
		Summary:              "更新系统运行设置",
		DetailLevel:          "summary",
		VisibilityScope:      "all_users",
		Changes:              systemSettingsUpdateOperationChanges(input, result),
		Method:               r.Method,
		Path:                 r.URL.Path,
		StatusCode:           &statusCode,
		ClientIP:             opts.clientIP.FromRequest(r),
		UserAgent:            r.UserAgent(),
		CreatedAt:            now().UTC(),
	}
	enqueueManagementOperationLog(r.Context(), opts, logInput)
}

func systemSettingsUpdateOperationChanges(
	input managementsettings.SystemUpdateInput,
	result managementsettings.SystemUpdateResult,
) []port.OperationLogChange {
	keys := make([]string, 0, len(input.Values))
	for key := range input.Values {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	changes := make([]port.OperationLogChange, 0, len(keys))
	for _, key := range keys {
		before, beforeExists := result.Before.Value(key)
		after, afterExists := result.Settings.Value(key)
		if !beforeExists || !afterExists || string(before) == string(after) {
			continue
		}
		changes = append(changes, port.OperationLogChange{
			Field:  key,
			Label:  key,
			Before: systemSettingOperationLogValue(key, before),
			After:  systemSettingOperationLogValue(key, after),
		})
	}
	return changes
}

func systemSettingOperationLogValue(key string, raw json.RawMessage) any {
	definition, ok := systemsettings.DefinitionFor(key)
	if !ok {
		return string(raw)
	}
	switch definition.Kind {
	case systemsettings.ValueKindInteger:
		var value int
		if err := json.Unmarshal(raw, &value); err == nil {
			return value
		}
	case systemsettings.ValueKindDecimal:
		var value float64
		if err := json.Unmarshal(raw, &value); err == nil {
			return value
		}
	case systemsettings.ValueKindTimezone:
		var value string
		if err := json.Unmarshal(raw, &value); err == nil {
			return value
		}
	}
	return string(raw)
}
