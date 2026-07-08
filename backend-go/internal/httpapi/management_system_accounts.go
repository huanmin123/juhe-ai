package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	operationlogjob "juhe-ai/backend-go/internal/jobs/operationlog"
	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementsystemaccounts"
	"juhe-ai/backend-go/internal/store/port"
)

type managementSystemAccountOptionService interface {
	List(r *http.Request, input managementsystemaccounts.ListInput) (managementsystemaccounts.ListResult, error)
	Options(r *http.Request, input managementsystemaccounts.OptionListInput) ([]managementsystemaccounts.Option, error)
}

type managementSystemAccountPatchService interface {
	ResetPassword(ctx context.Context, input managementsystemaccounts.PasswordResetInput) (managementsystemaccounts.PasswordResetResult, error)
	UpdateStatus(ctx context.Context, input managementsystemaccounts.StatusUpdateInput) (managementsystemaccounts.StatusUpdateResult, error)
	UpdateImageGeneration(ctx context.Context, input managementsystemaccounts.ImageGenerationUpdateInput) (managementsystemaccounts.ImageGenerationUpdateResult, error)
	UpdateProfile(ctx context.Context, input managementsystemaccounts.ProfileUpdateInput) (managementsystemaccounts.ProfileUpdateResult, error)
	Update(ctx context.Context, input managementsystemaccounts.UpdateInput) (managementsystemaccounts.UpdateResult, error)
}

type managementSystemAccountOptionServiceAdapter struct {
	service *managementsystemaccounts.Service
}

func (s managementSystemAccountOptionServiceAdapter) Options(r *http.Request, input managementsystemaccounts.OptionListInput) ([]managementsystemaccounts.Option, error) {
	return s.service.Options(r.Context(), input)
}

func (s managementSystemAccountOptionServiceAdapter) List(r *http.Request, input managementsystemaccounts.ListInput) (managementsystemaccounts.ListResult, error) {
	return s.service.List(r.Context(), input)
}

func (s managementSystemAccountOptionServiceAdapter) ResetPassword(ctx context.Context, input managementsystemaccounts.PasswordResetInput) (managementsystemaccounts.PasswordResetResult, error) {
	return s.service.ResetPassword(ctx, input)
}

func (s managementSystemAccountOptionServiceAdapter) UpdateStatus(ctx context.Context, input managementsystemaccounts.StatusUpdateInput) (managementsystemaccounts.StatusUpdateResult, error) {
	return s.service.UpdateStatus(ctx, input)
}

func (s managementSystemAccountOptionServiceAdapter) UpdateImageGeneration(ctx context.Context, input managementsystemaccounts.ImageGenerationUpdateInput) (managementsystemaccounts.ImageGenerationUpdateResult, error) {
	return s.service.UpdateImageGeneration(ctx, input)
}

func (s managementSystemAccountOptionServiceAdapter) UpdateProfile(ctx context.Context, input managementsystemaccounts.ProfileUpdateInput) (managementsystemaccounts.ProfileUpdateResult, error) {
	return s.service.UpdateProfile(ctx, input)
}

func (s managementSystemAccountOptionServiceAdapter) Update(ctx context.Context, input managementsystemaccounts.UpdateInput) (managementsystemaccounts.UpdateResult, error) {
	return s.service.Update(ctx, input)
}

func NewManagementSystemAccountsHandler(service *managementsystemaccounts.Service) http.Handler {
	return newManagementSystemAccountsHandler(managementSystemAccountOptionServiceAdapter{service: service})
}

func NewManagementSystemAccountOptionsHandler(service *managementsystemaccounts.Service) http.Handler {
	return newManagementSystemAccountOptionsHandler(managementSystemAccountOptionServiceAdapter{service: service})
}

func NewManagementSystemAccountPatchHandler(service *managementsystemaccounts.Service) http.Handler {
	return newManagementSystemAccountPatchHandler(managementSystemAccountOptionServiceAdapter{service: service})
}

func NewManagementSystemAccountPatchHandlerWithOperationLog(service *managementsystemaccounts.Service, opts ManagementOperationLogOptions) http.Handler {
	return newManagementSystemAccountPatchHandler(
		managementSystemAccountOptionServiceAdapter{service: service},
		newManagementOperationLogOptions(opts),
	)
}

func newManagementSystemAccountsHandler(service managementSystemAccountOptionService) http.Handler {
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
		result, err := service.List(r, parseManagementSystemAccountListQuery(r.URL.Query()))
		if err != nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		writeData(w, http.StatusOK, result)
	})
}

func newManagementSystemAccountOptionsHandler(service managementSystemAccountOptionService) http.Handler {
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
		options, err := service.Options(r, parseManagementSystemAccountOptionListQuery(r.URL.Query()))
		if err != nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		writeData(w, http.StatusOK, options)
	})
}

func newManagementSystemAccountPatchHandler(service managementSystemAccountPatchService, logOptions ...managementOperationLogOptions) http.Handler {
	operationLogs := effectiveManagementOperationLogOptions(logOptions)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authContext, ok := ManagementAuthContextFromRequest(r)
		if !ok || strings.TrimSpace(authContext.SystemAccountID) == "" {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		if !managementauth.IsSuperAdminRole(authContext.Role) {
			writeMessageError(w, http.StatusForbidden, "需要超级管理员权限")
			return
		}
		patch, ok := decodeManagementSystemAccountPatchBody(w, r)
		if !ok {
			return
		}
		result, err := service.Update(r.Context(), managementsystemaccounts.UpdateInput{
			SystemAccountID:        chi.URLParam(r, "id"),
			Password:               patch.password,
			DisplayName:            patch.displayName,
			HasDescription:         patch.hasDescription,
			Description:            patch.description,
			Role:                   patch.role,
			Status:                 patch.status,
			MustChangePassword:     patch.mustChangePassword,
			ImageGenerationEnabled: patch.imageGenerationEnabled,
		})
		if errors.Is(err, managementsystemaccounts.ErrPasswordResetWhitespace) {
			writeMessageError(w, http.StatusBadRequest, "登录密码不能包含空格")
			return
		}
		if errors.Is(err, managementsystemaccounts.ErrProfileUpdateWhitespace) {
			writeMessageError(w, http.StatusBadRequest, "用户名称不能包含空格")
			return
		}
		if errors.Is(err, managementsystemaccounts.ErrPasswordResetInvalid) ||
			errors.Is(err, managementsystemaccounts.ErrProfileUpdateInvalid) ||
			errors.Is(err, managementsystemaccounts.ErrStatusUpdateInvalid) ||
			errors.Is(err, managementsystemaccounts.ErrImageGenerationUpdateInvalid) {
			writeMessageError(w, http.StatusBadRequest, "系统账户参数无效")
			return
		}
		if errors.Is(err, managementsystemaccounts.ErrSystemAccountNotFound) {
			writeMessageError(w, http.StatusNotFound, "系统账户不存在")
			return
		}
		if errors.Is(err, managementsystemaccounts.ErrProfileUpdateDisplayNameDup) {
			writeMessageError(w, http.StatusConflict, "用户名称已存在")
			return
		}
		if errors.Is(err, managementsystemaccounts.ErrActiveSuperAdminRequired) {
			writeMessageError(w, http.StatusConflict, "至少保留一个启用的超级管理员")
			return
		}
		if err != nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		recordSystemAccountUpdateOperationLog(r, authContext, result, operationLogs)
		writeData(w, http.StatusOK, result.Account)
	})
}

type managementSystemAccountPatchAction string

const (
	managementSystemAccountPatchPassword        managementSystemAccountPatchAction = "password"
	managementSystemAccountPatchStatus          managementSystemAccountPatchAction = "status"
	managementSystemAccountPatchImageGeneration managementSystemAccountPatchAction = "imageGeneration"
	managementSystemAccountPatchProfile         managementSystemAccountPatchAction = "profile"
)

type managementSystemAccountPatchBody struct {
	action                 managementSystemAccountPatchAction
	password               *string
	mustChangePassword     *bool
	status                 *string
	imageGenerationEnabled *bool
	displayName            *string
	hasDescription         bool
	description            *string
	role                   *string
}

func decodeManagementSystemAccountPatchBody(w http.ResponseWriter, r *http.Request) (managementSystemAccountPatchBody, bool) {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	var payload map[string]json.RawMessage
	if err := decoder.Decode(&payload); err != nil || payload == nil {
		writeManagementSystemAccountPatchBodyError(w, err)
		return managementSystemAccountPatchBody{}, false
	}
	var extra struct{}
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		writeMessageError(w, http.StatusBadRequest, "请求体无效")
		return managementSystemAccountPatchBody{}, false
	}
	if _, exists := payload["username"]; exists {
		writeMessageError(w, http.StatusBadRequest, "用户账户创建后不能修改")
		return managementSystemAccountPatchBody{}, false
	}
	if len(payload) == 0 {
		writeMessageError(w, http.StatusBadRequest, "系统账户参数无效")
		return managementSystemAccountPatchBody{}, false
	}
	patch := managementSystemAccountPatchBody{action: managementSystemAccountPatchProfile}
	for field, raw := range payload {
		switch field {
		case "password":
			var value *string
			if err := json.Unmarshal(raw, &value); err != nil || value == nil {
				writeMessageError(w, http.StatusBadRequest, "系统账户参数无效")
				return managementSystemAccountPatchBody{}, false
			}
			patch.password = value
		case "status":
			var value *string
			if err := json.Unmarshal(raw, &value); err != nil || value == nil {
				writeMessageError(w, http.StatusBadRequest, "系统账户参数无效")
				return managementSystemAccountPatchBody{}, false
			}
			patch.status = value
		case "imageGenerationEnabled":
			var value *bool
			if err := json.Unmarshal(raw, &value); err != nil || value == nil {
				writeMessageError(w, http.StatusBadRequest, "系统账户参数无效")
				return managementSystemAccountPatchBody{}, false
			}
			patch.imageGenerationEnabled = value
		case "displayName":
			var value *string
			if err := json.Unmarshal(raw, &value); err != nil || value == nil {
				writeMessageError(w, http.StatusBadRequest, "系统账户参数无效")
				return managementSystemAccountPatchBody{}, false
			}
			patch.displayName = value
		case "description":
			var value *string
			if err := json.Unmarshal(raw, &value); err != nil {
				writeMessageError(w, http.StatusBadRequest, "系统账户参数无效")
				return managementSystemAccountPatchBody{}, false
			}
			patch.hasDescription = true
			patch.description = value
		case "role":
			var value *string
			if err := json.Unmarshal(raw, &value); err != nil || value == nil {
				writeMessageError(w, http.StatusBadRequest, "系统账户参数无效")
				return managementSystemAccountPatchBody{}, false
			}
			patch.role = value
		case "mustChangePassword":
			var value *bool
			if err := json.Unmarshal(raw, &value); err != nil || value == nil {
				writeMessageError(w, http.StatusBadRequest, "系统账户参数无效")
				return managementSystemAccountPatchBody{}, false
			}
			patch.mustChangePassword = value
		default:
			writeMessageError(w, http.StatusBadRequest, "系统账户参数无效")
			return managementSystemAccountPatchBody{}, false
		}
	}
	return patch, true
}

func writeManagementSystemAccountPatchBodyError(w http.ResponseWriter, err error) {
	var maxBytesErr *http.MaxBytesError
	if errors.As(err, &maxBytesErr) {
		writeMessageError(w, http.StatusRequestEntityTooLarge, "请求体过大")
		return
	}
	var syntaxErr *json.SyntaxError
	if errors.As(err, &syntaxErr) || errors.Is(err, io.ErrUnexpectedEOF) {
		writeMessageError(w, http.StatusBadRequest, "请求体无效")
		return
	}
	writeMessageError(w, http.StatusBadRequest, "系统账户参数无效")
}

func recordSystemAccountUpdateOperationLog(
	r *http.Request,
	authContext managementauth.Context,
	result managementsystemaccounts.UpdateResult,
	opts managementOperationLogOptions,
) {
	if opts.client == nil || (!result.Changed && result.RevokedSessionCount == 0) {
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
	action := "update"
	operationKey := "system_accounts.update"
	summary := "更新系统账户：" + result.Account.DisplayName
	if result.PasswordChanged {
		action = "reset_password"
		operationKey = "system_accounts.reset_password"
		summary = "重置系统账户密码：" + result.Account.DisplayName
	}
	statusCode := http.StatusOK
	input := port.OperationLogInput{
		ID:                            newLogID(),
		TraceID:                       requestIDFromContext(r.Context()),
		ActorSystemAccountID:          authContext.SystemAccountID,
		ActorUsername:                 authContext.Username,
		ActorDisplayName:              authContext.DisplayName,
		ActorRole:                     authContext.Role,
		OperationScopeSystemAccountID: result.Account.ID,
		Mode:                          "admin",
		Module:                        "system_accounts",
		Action:                        action,
		OperationKey:                  operationKey,
		ResourceType:                  "system_account",
		ResourceID:                    result.Account.ID,
		ResourceName:                  result.Account.DisplayName,
		Summary:                       summary,
		DetailLevel:                   "full",
		VisibilityScope:               "targeted",
		Changes:                       systemAccountUpdateChanges(result),
		Metadata: map[string]any{
			"revokedSessionCount": result.RevokedSessionCount,
		},
		Method:     r.Method,
		Path:       r.URL.Path,
		StatusCode: &statusCode,
		ClientIP:   opts.clientIP.FromRequest(r),
		UserAgent:  r.UserAgent(),
		Viewers: []port.OperationLogViewerInput{
			{
				SystemAccountID:  result.Account.ID,
				VisibilityReason: "admin_managed_my_resource",
				DetailLevel:      "full",
			},
		},
		CreatedAt: now().UTC(),
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

func recordSystemAccountPasswordResetOperationLog(
	r *http.Request,
	authContext managementauth.Context,
	result managementsystemaccounts.PasswordResetResult,
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
	input := port.OperationLogInput{
		ID:                            newLogID(),
		TraceID:                       requestIDFromContext(r.Context()),
		ActorSystemAccountID:          authContext.SystemAccountID,
		ActorUsername:                 authContext.Username,
		ActorDisplayName:              authContext.DisplayName,
		ActorRole:                     authContext.Role,
		OperationScopeSystemAccountID: result.Account.ID,
		Mode:                          "admin",
		Module:                        "system_accounts",
		Action:                        "reset_password",
		OperationKey:                  "system_accounts.reset_password",
		ResourceType:                  "system_account",
		ResourceID:                    result.Account.ID,
		ResourceName:                  result.Account.DisplayName,
		Summary:                       "重置系统账户密码：" + result.Account.DisplayName,
		DetailLevel:                   "full",
		VisibilityScope:               "targeted",
		Changes: []port.OperationLogChange{
			{
				Field:     "password",
				Label:     "登录密码",
				After:     "已重置",
				Sensitive: true,
			},
		},
		Metadata: map[string]any{
			"revokedSessionCount": result.RevokedSessionCount,
		},
		Method:     r.Method,
		Path:       r.URL.Path,
		StatusCode: &statusCode,
		ClientIP:   opts.clientIP.FromRequest(r),
		UserAgent:  r.UserAgent(),
		Viewers: []port.OperationLogViewerInput{
			{
				SystemAccountID:  result.Account.ID,
				VisibilityReason: "admin_managed_my_resource",
				DetailLevel:      "full",
			},
		},
		CreatedAt: now().UTC(),
	}
	if result.Before.MustChangePassword != result.Account.MustChangePassword {
		input.Changes = append(input.Changes, port.OperationLogChange{
			Field:  "mustChangePassword",
			Label:  "初始密码状态",
			Before: result.Before.MustChangePassword,
			After:  result.Account.MustChangePassword,
		})
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

func recordSystemAccountStatusUpdateOperationLog(
	r *http.Request,
	authContext managementauth.Context,
	result managementsystemaccounts.StatusUpdateResult,
	opts managementOperationLogOptions,
) {
	if opts.client == nil {
		return
	}
	if result.Before.Status == result.Account.Status && result.RevokedSessionCount == 0 {
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
	changes := []port.OperationLogChange{}
	if result.Before.Status != result.Account.Status {
		changes = append(changes, port.OperationLogChange{
			Field:  "status",
			Label:  "状态",
			Before: result.Before.Status,
			After:  result.Account.Status,
		})
	}
	input := port.OperationLogInput{
		ID:                            newLogID(),
		TraceID:                       requestIDFromContext(r.Context()),
		ActorSystemAccountID:          authContext.SystemAccountID,
		ActorUsername:                 authContext.Username,
		ActorDisplayName:              authContext.DisplayName,
		ActorRole:                     authContext.Role,
		OperationScopeSystemAccountID: result.Account.ID,
		Mode:                          "admin",
		Module:                        "system_accounts",
		Action:                        "update",
		OperationKey:                  "system_accounts.update",
		ResourceType:                  "system_account",
		ResourceID:                    result.Account.ID,
		ResourceName:                  result.Account.DisplayName,
		Summary:                       "更新系统账户：" + result.Account.DisplayName,
		DetailLevel:                   "full",
		VisibilityScope:               "targeted",
		Changes:                       changes,
		Metadata: map[string]any{
			"revokedSessionCount": result.RevokedSessionCount,
		},
		Method:     r.Method,
		Path:       r.URL.Path,
		StatusCode: &statusCode,
		ClientIP:   opts.clientIP.FromRequest(r),
		UserAgent:  r.UserAgent(),
		Viewers: []port.OperationLogViewerInput{
			{
				SystemAccountID:  result.Account.ID,
				VisibilityReason: "admin_managed_my_resource",
				DetailLevel:      "full",
			},
		},
		CreatedAt: now().UTC(),
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

func recordSystemAccountImageGenerationUpdateOperationLog(
	r *http.Request,
	authContext managementauth.Context,
	result managementsystemaccounts.ImageGenerationUpdateResult,
	opts managementOperationLogOptions,
) {
	if opts.client == nil || !result.Changed {
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
	input := port.OperationLogInput{
		ID:                            newLogID(),
		TraceID:                       requestIDFromContext(r.Context()),
		ActorSystemAccountID:          authContext.SystemAccountID,
		ActorUsername:                 authContext.Username,
		ActorDisplayName:              authContext.DisplayName,
		ActorRole:                     authContext.Role,
		OperationScopeSystemAccountID: result.Account.ID,
		Mode:                          "admin",
		Module:                        "system_accounts",
		Action:                        "update",
		OperationKey:                  "system_accounts.update",
		ResourceType:                  "system_account",
		ResourceID:                    result.Account.ID,
		ResourceName:                  result.Account.DisplayName,
		Summary:                       "更新系统账户：" + result.Account.DisplayName,
		DetailLevel:                   "full",
		VisibilityScope:               "targeted",
		Changes: []port.OperationLogChange{
			{
				Field:  "imageGenerationEnabled",
				Label:  "支持图像生成",
				Before: result.Before.ImageGenerationEnabled,
				After:  result.Account.ImageGenerationEnabled,
			},
		},
		Method:     r.Method,
		Path:       r.URL.Path,
		StatusCode: &statusCode,
		ClientIP:   opts.clientIP.FromRequest(r),
		UserAgent:  r.UserAgent(),
		Viewers: []port.OperationLogViewerInput{
			{
				SystemAccountID:  result.Account.ID,
				VisibilityReason: "admin_managed_my_resource",
				DetailLevel:      "full",
			},
		},
		CreatedAt: now().UTC(),
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

func recordSystemAccountProfileUpdateOperationLog(
	r *http.Request,
	authContext managementauth.Context,
	result managementsystemaccounts.ProfileUpdateResult,
	opts managementOperationLogOptions,
) {
	if opts.client == nil || !result.Changed {
		return
	}
	changes := systemAccountProfileUpdateChanges(result)
	if len(changes) == 0 {
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
	input := port.OperationLogInput{
		ID:                            newLogID(),
		TraceID:                       requestIDFromContext(r.Context()),
		ActorSystemAccountID:          authContext.SystemAccountID,
		ActorUsername:                 authContext.Username,
		ActorDisplayName:              authContext.DisplayName,
		ActorRole:                     authContext.Role,
		OperationScopeSystemAccountID: result.Account.ID,
		Mode:                          "admin",
		Module:                        "system_accounts",
		Action:                        "update",
		OperationKey:                  "system_accounts.update",
		ResourceType:                  "system_account",
		ResourceID:                    result.Account.ID,
		ResourceName:                  result.Account.DisplayName,
		Summary:                       "更新系统账户：" + result.Account.DisplayName,
		DetailLevel:                   "full",
		VisibilityScope:               "targeted",
		Changes:                       changes,
		Method:                        r.Method,
		Path:                          r.URL.Path,
		StatusCode:                    &statusCode,
		ClientIP:                      opts.clientIP.FromRequest(r),
		UserAgent:                     r.UserAgent(),
		Viewers: []port.OperationLogViewerInput{
			{
				SystemAccountID:  result.Account.ID,
				VisibilityReason: "admin_managed_my_resource",
				DetailLevel:      "full",
			},
		},
		CreatedAt: now().UTC(),
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

func systemAccountProfileUpdateChanges(result managementsystemaccounts.ProfileUpdateResult) []port.OperationLogChange {
	changes := make([]port.OperationLogChange, 0, 4)
	if result.Before.DisplayName != result.Account.DisplayName {
		changes = append(changes, port.OperationLogChange{
			Field:  "displayName",
			Label:  "用户名称",
			Before: result.Before.DisplayName,
			After:  result.Account.DisplayName,
		})
	}
	if result.Before.Description != result.Account.Description {
		changes = append(changes, port.OperationLogChange{
			Field:  "description",
			Label:  "说明",
			Before: result.Before.Description,
			After:  result.Account.Description,
		})
	}
	if result.Before.Role != result.Account.Role {
		changes = append(changes, port.OperationLogChange{
			Field:  "role",
			Label:  "角色",
			Before: result.Before.Role,
			After:  result.Account.Role,
		})
	}
	if result.Before.MustChangePassword != result.Account.MustChangePassword {
		changes = append(changes, port.OperationLogChange{
			Field:  "mustChangePassword",
			Label:  "下次登录改密",
			Before: result.Before.MustChangePassword,
			After:  result.Account.MustChangePassword,
		})
	}
	return changes
}

func systemAccountUpdateChanges(result managementsystemaccounts.UpdateResult) []port.OperationLogChange {
	changes := make([]port.OperationLogChange, 0, 7)
	if result.PasswordChanged {
		changes = append(changes, port.OperationLogChange{
			Field:     "password",
			Label:     "登录密码",
			After:     "已重置",
			Sensitive: true,
		})
	}
	if result.Before.DisplayName != result.Account.DisplayName {
		changes = append(changes, port.OperationLogChange{
			Field:  "displayName",
			Label:  "用户名称",
			Before: result.Before.DisplayName,
			After:  result.Account.DisplayName,
		})
	}
	if result.Before.Description != result.Account.Description {
		changes = append(changes, port.OperationLogChange{
			Field:  "description",
			Label:  "说明",
			Before: result.Before.Description,
			After:  result.Account.Description,
		})
	}
	if result.Before.Role != result.Account.Role {
		changes = append(changes, port.OperationLogChange{
			Field:  "role",
			Label:  "角色",
			Before: result.Before.Role,
			After:  result.Account.Role,
		})
	}
	if result.Before.Status != result.Account.Status {
		changes = append(changes, port.OperationLogChange{
			Field:  "status",
			Label:  "状态",
			Before: result.Before.Status,
			After:  result.Account.Status,
		})
	}
	if result.Before.MustChangePassword != result.Account.MustChangePassword {
		changes = append(changes, port.OperationLogChange{
			Field:  "mustChangePassword",
			Label:  "下次登录改密",
			Before: result.Before.MustChangePassword,
			After:  result.Account.MustChangePassword,
		})
	}
	if result.Before.ImageGenerationEnabled != result.Account.ImageGenerationEnabled {
		changes = append(changes, port.OperationLogChange{
			Field:  "imageGenerationEnabled",
			Label:  "支持图像生成",
			Before: result.Before.ImageGenerationEnabled,
			After:  result.Account.ImageGenerationEnabled,
		})
	}
	return changes
}

func parseManagementSystemAccountListQuery(values url.Values) managementsystemaccounts.ListInput {
	return managementsystemaccounts.ListInput{
		Keyword:  firstManagementQueryText(values, "keyword"),
		Page:     managementIntegerQueryValue(values, "page"),
		PageSize: managementIntegerQueryValue(values, "pageSize"),
	}
}

func parseManagementSystemAccountOptionListQuery(values url.Values) managementsystemaccounts.OptionListInput {
	return managementsystemaccounts.OptionListInput{
		IDs:     managementTextListQueryValue(values, "ids", 50),
		Keyword: firstManagementQueryText(values, "keyword"),
		Limit:   managementIntegerQueryValue(values, "limit"),
	}
}
