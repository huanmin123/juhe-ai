package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	operationlogjob "juhe-ai/backend-go/internal/jobs/operationlog"
	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementsettings"
	"juhe-ai/backend-go/internal/modules/publicsettings"
	"juhe-ai/backend-go/internal/store/port"
)

const managementGlobalSettingsMaxBodyBytes = 1 << 20

type managementGlobalSettingsUpdateService interface {
	Update(ctx context.Context, input managementsettings.UpdateInput) (managementsettings.UpdateResult, error)
}

type managementGlobalSettingsUpdateServiceAdapter struct {
	service *managementsettings.Service
}

func (s managementGlobalSettingsUpdateServiceAdapter) Update(ctx context.Context, input managementsettings.UpdateInput) (managementsettings.UpdateResult, error) {
	if s.service == nil {
		return managementsettings.UpdateResult{}, errors.New("management global settings update service is required")
	}
	return s.service.Update(ctx, input)
}

func NewManagementGlobalSettingsHandler(service *publicsettings.Service) http.Handler {
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

func NewManagementGlobalSettingsUpdateHandler(service *managementsettings.Service) http.Handler {
	return newManagementGlobalSettingsUpdateHandler(managementGlobalSettingsUpdateServiceAdapter{service: service})
}

func NewManagementGlobalSettingsUpdateHandlerWithOperationLog(service *managementsettings.Service, opts ManagementOperationLogOptions) http.Handler {
	return newManagementGlobalSettingsUpdateHandler(
		managementGlobalSettingsUpdateServiceAdapter{service: service},
		newManagementOperationLogOptions(opts),
	)
}

func newManagementGlobalSettingsUpdateHandler(service managementGlobalSettingsUpdateService, logOptions ...managementOperationLogOptions) http.Handler {
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
		input, ok := decodeManagementGlobalSettingsUpdateBody(w, r)
		if !ok {
			return
		}
		result, err := service.Update(r.Context(), input)
		if err != nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		recordGlobalSettingsUpdateOperationLog(r, authContext, result, operationLogs)
		writeData(w, http.StatusOK, result.Settings)
	})
}

func decodeManagementGlobalSettingsUpdateBody(w http.ResponseWriter, r *http.Request) (managementsettings.UpdateInput, bool) {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, managementGlobalSettingsMaxBodyBytes))
	var payload map[string]json.RawMessage
	if err := decoder.Decode(&payload); err != nil {
		writeManagementGlobalSettingsBodyError(w, err)
		return managementsettings.UpdateInput{}, false
	}
	if payload == nil {
		writeMessageError(w, http.StatusBadRequest, "请求体必须是对象")
		return managementsettings.UpdateInput{}, false
	}
	var extra struct{}
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		writeManagementGlobalSettingsTrailingBodyError(w, err)
		return managementsettings.UpdateInput{}, false
	}
	if len(payload) == 0 {
		writeMessageError(w, http.StatusBadRequest, "全局设置更新不能为空")
		return managementsettings.UpdateInput{}, false
	}

	unknownField := ""
	for field := range payload {
		if field != "appName" && field != "appIcon" && (unknownField == "" || field < unknownField) {
			unknownField = field
		}
	}
	if unknownField != "" {
		writeMessageError(w, http.StatusBadRequest, "未知全局设置字段："+unknownField)
		return managementsettings.UpdateInput{}, false
	}

	var input managementsettings.UpdateInput
	if raw, exists := payload["appName"]; exists {
		value, ok := decodeManagementGlobalSettingsString(w, "appName", raw)
		if !ok {
			return managementsettings.UpdateInput{}, false
		}
		input.AppName = &value
	}
	if raw, exists := payload["appIcon"]; exists {
		value, ok := decodeManagementGlobalSettingsString(w, "appIcon", raw)
		if !ok {
			return managementsettings.UpdateInput{}, false
		}
		input.AppIcon = &value
	}
	return input, true
}

func decodeManagementGlobalSettingsString(w http.ResponseWriter, field string, raw json.RawMessage) (string, bool) {
	var value *string
	if err := json.Unmarshal(raw, &value); err != nil || value == nil {
		writeMessageError(w, http.StatusBadRequest, field+" 必须是非空字符串")
		return "", false
	}
	normalized := strings.TrimSpace(*value)
	if normalized == "" {
		writeMessageError(w, http.StatusBadRequest, field+" 必须是非空字符串")
		return "", false
	}
	return normalized, true
}

func writeManagementGlobalSettingsBodyError(w http.ResponseWriter, err error) {
	var maxBytesErr *http.MaxBytesError
	if errors.As(err, &maxBytesErr) {
		writeMessageError(w, http.StatusBadRequest, "请求体过大")
		return
	}
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) {
		writeMessageError(w, http.StatusBadRequest, "请求体必须是对象")
		return
	}
	writeMessageError(w, http.StatusBadRequest, "请求体无效")
}

func writeManagementGlobalSettingsTrailingBodyError(w http.ResponseWriter, err error) {
	var maxBytesErr *http.MaxBytesError
	if errors.As(err, &maxBytesErr) {
		writeMessageError(w, http.StatusBadRequest, "请求体过大")
		return
	}
	writeMessageError(w, http.StatusBadRequest, "请求体无效")
}

func recordGlobalSettingsUpdateOperationLog(
	r *http.Request,
	authContext managementauth.Context,
	result managementsettings.UpdateResult,
	opts managementOperationLogOptions,
) {
	if opts.client == nil {
		return
	}
	changes := globalSettingsUpdateOperationChanges(result)
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
	input := port.OperationLogInput{
		ID:                   newLogID(),
		TraceID:              requestIDFromContext(r.Context()),
		ActorSystemAccountID: authContext.SystemAccountID,
		ActorUsername:        authContext.Username,
		ActorDisplayName:     authContext.DisplayName,
		ActorRole:            authContext.Role,
		Mode:                 "admin",
		Module:               "settings",
		Action:               "update_global",
		OperationKey:         "settings.update_global",
		ResourceType:         "global_settings",
		ResourceID:           "global",
		ResourceName:         "全局品牌设置",
		Summary:              "更新全局品牌设置",
		DetailLevel:          "summary",
		VisibilityScope:      "all_users",
		Changes:              changes,
		Method:               r.Method,
		Path:                 r.URL.Path,
		StatusCode:           &statusCode,
		ClientIP:             opts.clientIP.FromRequest(r),
		UserAgent:            r.UserAgent(),
		CreatedAt:            now().UTC(),
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

func globalSettingsUpdateOperationChanges(result managementsettings.UpdateResult) []port.OperationLogChange {
	changes := make([]port.OperationLogChange, 0, 2)
	if result.Before.AppName != result.Settings.AppName {
		changes = append(changes, port.OperationLogChange{
			Field:  "appName",
			Label:  "系统名称",
			Before: result.Before.AppName,
			After:  result.Settings.AppName,
		})
	}
	if result.Before.AppIcon != result.Settings.AppIcon {
		changes = append(changes, port.OperationLogChange{
			Field:  "appIcon",
			Label:  "系统图标",
			Before: result.Before.AppIcon,
			After:  result.Settings.AppIcon,
		})
	}
	return changes
}
