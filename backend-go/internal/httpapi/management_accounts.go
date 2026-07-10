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

	"juhe-ai/backend-go/internal/config"
	operationlogjob "juhe-ai/backend-go/internal/jobs/operationlog"
	"juhe-ai/backend-go/internal/modules/managementaccounts"
	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/store/port"
)

type managementAccountOptionScope int

const (
	managementAccountScopeAdmin managementAccountOptionScope = iota
	managementAccountScopeSelf
)

type managementAccountOptionService interface {
	Options(r *http.Request, input managementaccounts.OptionListInput) ([]managementaccounts.Option, error)
}

type managementAccountTagService interface {
	Tags(r *http.Request, input managementaccounts.TagListInput) ([]managementaccounts.Tag, error)
	DeleteTag(r *http.Request, input managementaccounts.TagDeleteInput) (bool, error)
	UpdateTags(r *http.Request, input managementaccounts.TagUpdateInput) (managementaccounts.TagUpdateResult, error)
}

type managementAccountOptionServiceAdapter struct {
	service *managementaccounts.Service
}

func (s managementAccountOptionServiceAdapter) Options(r *http.Request, input managementaccounts.OptionListInput) ([]managementaccounts.Option, error) {
	return s.service.Options(r.Context(), input)
}

func (s managementAccountOptionServiceAdapter) Tags(r *http.Request, input managementaccounts.TagListInput) ([]managementaccounts.Tag, error) {
	return s.service.Tags(r.Context(), input)
}

func (s managementAccountOptionServiceAdapter) DeleteTag(r *http.Request, input managementaccounts.TagDeleteInput) (bool, error) {
	return s.service.DeleteTag(r.Context(), input)
}

func (s managementAccountOptionServiceAdapter) UpdateTags(r *http.Request, input managementaccounts.TagUpdateInput) (managementaccounts.TagUpdateResult, error) {
	return s.service.UpdateTags(r.Context(), input)
}

type ManagementOperationLogOptions struct {
	Config         config.Config
	Logger         *slog.Logger
	Client         operationlogjob.EnqueueClient
	SettingsReader OperationLogSettingsReader
	Now            func() time.Time
	NewLogID       func() string
}

type OperationLogSettingsReader interface {
	OperationLogMaxChangesPerRecord(ctx context.Context) (int, error)
}

type managementOperationLogOptions struct {
	logger         *slog.Logger
	client         operationlogjob.EnqueueClient
	settingsReader OperationLogSettingsReader
	clientIP       clientIPResolver
	now            func() time.Time
	newLogID       func() string
}

func NewManagementAccountOptionsHandler(service *managementaccounts.Service) http.Handler {
	return newManagementAccountOptionsHandler(managementAccountOptionServiceAdapter{service: service}, managementAccountScopeAdmin)
}

func NewManagementMyAccountOptionsHandler(service *managementaccounts.Service) http.Handler {
	return newManagementAccountOptionsHandler(managementAccountOptionServiceAdapter{service: service}, managementAccountScopeSelf)
}

func NewManagementAccountTagsHandler(service *managementaccounts.Service) http.Handler {
	return newManagementAccountTagsHandler(managementAccountOptionServiceAdapter{service: service}, managementAccountScopeAdmin)
}

func NewManagementMyAccountTagsHandler(service *managementaccounts.Service) http.Handler {
	return newManagementAccountTagsHandler(managementAccountOptionServiceAdapter{service: service}, managementAccountScopeSelf)
}

func NewManagementAccountTagDeleteHandler(service *managementaccounts.Service) http.Handler {
	return newManagementAccountTagDeleteHandler(managementAccountOptionServiceAdapter{service: service}, managementAccountScopeAdmin)
}

func NewManagementMyAccountTagDeleteHandler(service *managementaccounts.Service) http.Handler {
	return newManagementAccountTagDeleteHandler(managementAccountOptionServiceAdapter{service: service}, managementAccountScopeSelf)
}

func NewManagementAccountTagUpdateHandler(service *managementaccounts.Service) http.Handler {
	return newManagementAccountTagUpdateHandler(managementAccountOptionServiceAdapter{service: service}, managementAccountScopeAdmin)
}

func NewManagementMyAccountTagUpdateHandler(service *managementaccounts.Service) http.Handler {
	return newManagementAccountTagUpdateHandler(managementAccountOptionServiceAdapter{service: service}, managementAccountScopeSelf)
}

func NewManagementAccountTagUpdateHandlerWithOperationLog(service *managementaccounts.Service, opts ManagementOperationLogOptions) http.Handler {
	return newManagementAccountTagUpdateHandler(
		managementAccountOptionServiceAdapter{service: service},
		managementAccountScopeAdmin,
		newManagementOperationLogOptions(opts),
	)
}

func NewManagementMyAccountTagUpdateHandlerWithOperationLog(service *managementaccounts.Service, opts ManagementOperationLogOptions) http.Handler {
	return newManagementAccountTagUpdateHandler(
		managementAccountOptionServiceAdapter{service: service},
		managementAccountScopeSelf,
		newManagementOperationLogOptions(opts),
	)
}

func newManagementAccountOptionsHandler(service managementAccountOptionService, scope managementAccountOptionScope) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authContext, ok := ManagementAuthContextFromRequest(r)
		if !ok || strings.TrimSpace(authContext.SystemAccountID) == "" {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		input, allowed := managementAccountOptionListInput(authContext, r.URL.Query(), scope)
		if !allowed {
			writeMessageError(w, http.StatusForbidden, "需要管理员权限")
			return
		}
		options, err := service.Options(r, input)
		if err != nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		writeData(w, http.StatusOK, options)
	})
}

func newManagementAccountTagsHandler(service managementAccountTagService, scope managementAccountOptionScope) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authContext, ok := ManagementAuthContextFromRequest(r)
		if !ok || strings.TrimSpace(authContext.SystemAccountID) == "" {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		input, allowed := managementAccountTagListInput(authContext, r.URL.Query(), scope)
		if !allowed {
			writeMessageError(w, http.StatusForbidden, "需要管理员权限")
			return
		}
		tags, err := service.Tags(r, input)
		if err != nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		writeData(w, http.StatusOK, tags)
	})
}

func newManagementAccountTagDeleteHandler(service managementAccountTagService, scope managementAccountOptionScope) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authContext, ok := ManagementAuthContextFromRequest(r)
		if !ok || strings.TrimSpace(authContext.SystemAccountID) == "" {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		scopeInput, allowed := managementAccountTagListInput(authContext, r.URL.Query(), scope)
		if !allowed {
			writeMessageError(w, http.StatusForbidden, "需要管理员权限")
			return
		}
		deleted, err := service.DeleteTag(r, managementaccounts.TagDeleteInput{
			ID:              chi.URLParam(r, "tagId"),
			SystemAccountID: scopeInput.SystemAccountID,
		})
		if errors.Is(err, managementaccounts.ErrAccountTagInUse) {
			writeMessageError(w, http.StatusBadRequest, "标签已绑定账户，不能删除")
			return
		}
		if err != nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		if !deleted {
			writeMessageError(w, http.StatusNotFound, "标签不存在")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

func newManagementAccountTagUpdateHandler(service managementAccountTagService, scope managementAccountOptionScope, logOptions ...managementOperationLogOptions) http.Handler {
	operationLogs := effectiveManagementOperationLogOptions(logOptions)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authContext, ok := ManagementAuthContextFromRequest(r)
		if !ok || strings.TrimSpace(authContext.SystemAccountID) == "" {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		scopeInput, allowed := managementAccountTagListInput(authContext, r.URL.Query(), scope)
		if !allowed {
			writeMessageError(w, http.StatusForbidden, "需要管理员权限")
			return
		}
		var payload struct {
			Tags *[]string `json:"tags"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&payload); err != nil || payload.Tags == nil {
			writeMessageError(w, http.StatusBadRequest, "账户标签参数无效")
			return
		}
		var extra struct{}
		if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
			writeMessageError(w, http.StatusBadRequest, "账户标签参数无效")
			return
		}
		result, err := service.UpdateTags(r, managementaccounts.TagUpdateInput{
			AccountID:       chi.URLParam(r, "id"),
			SystemAccountID: scopeInput.SystemAccountID,
			Tags:            *payload.Tags,
		})
		if errors.Is(err, managementaccounts.ErrAccountNotFound) {
			writeMessageError(w, http.StatusNotFound, "账户不存在")
			return
		}
		if message, ok := managementaccounts.ValidationMessage(err); ok {
			writeMessageError(w, http.StatusBadRequest, message)
			return
		}
		if err != nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		recordAccountTagUpdateOperationLog(r, authContext, scope, scopeInput.SystemAccountID, result, operationLogs)
		writeData(w, http.StatusOK, result.Account)
	})
}

func managementAccountOptionListInput(
	authContext managementauth.Context,
	values url.Values,
	scope managementAccountOptionScope,
) (managementaccounts.OptionListInput, bool) {
	input := parseManagementAccountOptionListQuery(values)
	switch scope {
	case managementAccountScopeAdmin:
		if !managementauth.IsAdminRole(authContext.Role) {
			return managementaccounts.OptionListInput{}, false
		}
		input.IncludeSystemAccountFields = true
		systemAccountID := firstManagementQueryText(values, "systemAccountId")
		if systemAccountID != "" && systemAccountID != "all" {
			input.SystemAccountID = systemAccountID
		}
	case managementAccountScopeSelf:
		input.SystemAccountID = authContext.SystemAccountID
		input.IncludeSystemAccountFields = false
	}
	return input, true
}

func managementAccountTagListInput(
	authContext managementauth.Context,
	values url.Values,
	scope managementAccountOptionScope,
) (managementaccounts.TagListInput, bool) {
	input := managementaccounts.TagListInput{SystemAccountID: authContext.SystemAccountID}
	switch scope {
	case managementAccountScopeAdmin:
		if !managementauth.IsAdminRole(authContext.Role) {
			return managementaccounts.TagListInput{}, false
		}
		systemAccountID := firstManagementQueryText(values, "systemAccountId")
		if systemAccountID != "" && systemAccountID != "all" {
			input.SystemAccountID = systemAccountID
		}
	case managementAccountScopeSelf:
		input.SystemAccountID = authContext.SystemAccountID
	}
	return input, true
}

func parseManagementAccountOptionListQuery(values url.Values) managementaccounts.OptionListInput {
	return managementaccounts.OptionListInput{
		IDs:          managementTextListQueryValue(values, "ids", 50),
		Page:         managementIntegerQueryValue(values, "page"),
		Limit:        managementIntegerQueryValue(values, "limit"),
		Keyword:      firstManagementQueryText(values, "keyword"),
		ProviderCode: firstManagementQueryText(values, "providerCode"),
		GroupID:      firstManagementQueryText(values, "groupId"),
		TagIDs:       managementTextListQueryValue(values, "tagIds", 100),
		Type:         firstManagementQueryText(values, "type"),
		Status:       strings.Join(managementTextListQueryValue(values, "status", 100), ","),
		Schedulable:  firstManagementQueryText(values, "schedulable"),
	}
}

func newManagementOperationLogOptions(opts ManagementOperationLogOptions) managementOperationLogOptions {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	newLogID := opts.NewLogID
	if newLogID == nil {
		newLogID = func() string {
			return "oplog_" + strings.ReplaceAll(uuid.NewString(), "-", "")
		}
	}
	return managementOperationLogOptions{
		logger:         opts.Logger,
		client:         opts.Client,
		settingsReader: opts.SettingsReader,
		clientIP:       newClientIPResolver(opts.Config),
		now:            now,
		newLogID:       newLogID,
	}
}

func effectiveManagementOperationLogOptions(values []managementOperationLogOptions) managementOperationLogOptions {
	if len(values) == 0 {
		return managementOperationLogOptions{}
	}
	return values[0]
}

func recordAccountTagUpdateOperationLog(
	r *http.Request,
	authContext managementauth.Context,
	scope managementAccountOptionScope,
	scopeSystemAccountID string,
	result managementaccounts.TagUpdateResult,
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
	ownerSystemAccountID := firstNonEmptyText(
		scopeSystemAccountID,
		result.Account.SystemAccountID,
		result.Account.OwnerSystemAccountID,
		authContext.SystemAccountID,
	)
	mode := "self"
	if scope == managementAccountScopeAdmin {
		mode = "admin"
	}
	statusCode := http.StatusOK
	input := port.OperationLogInput{
		ID:                            newLogID(),
		TraceID:                       requestIDFromContext(r.Context()),
		ActorSystemAccountID:          authContext.SystemAccountID,
		ActorUsername:                 authContext.Username,
		ActorDisplayName:              authContext.DisplayName,
		ActorRole:                     authContext.Role,
		OperationScopeSystemAccountID: ownerSystemAccountID,
		Mode:                          mode,
		Module:                        "accounts",
		Action:                        "update_tags",
		OperationKey:                  "accounts.update_tags",
		ResourceType:                  "account",
		ResourceID:                    result.Account.ID,
		ResourceName:                  result.Account.Name,
		Summary:                       "更新账户标签：" + result.Account.Name,
		DetailLevel:                   "full",
		VisibilityScope:               "targeted",
		Changes: []port.OperationLogChange{
			{
				Field:  "tags",
				Label:  "标签",
				Before: result.PreviousTags,
				After:  result.Account.Tags,
			},
		},
		Method:     r.Method,
		Path:       r.URL.Path,
		StatusCode: &statusCode,
		ClientIP:   opts.clientIP.FromRequest(r),
		UserAgent:  r.UserAgent(),
		Viewers: []port.OperationLogViewerInput{
			{
				SystemAccountID:  ownerSystemAccountID,
				VisibilityReason: "resource_owner",
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

func firstNonEmptyText(values ...string) string {
	for _, value := range values {
		text := strings.TrimSpace(value)
		if text != "" {
			return text
		}
	}
	return ""
}
