package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/store/port"
)

type managementProfileUpdateService interface {
	UpdateProfile(ctx context.Context, input managementauth.ProfileUpdateInput) (managementauth.ProfileUpdateResult, error)
}

type managementProfileUpdateServiceAdapter struct {
	service *managementauth.ProfileService
}

func (s managementProfileUpdateServiceAdapter) UpdateProfile(ctx context.Context, input managementauth.ProfileUpdateInput) (managementauth.ProfileUpdateResult, error) {
	if s.service == nil {
		return managementauth.ProfileUpdateResult{}, errors.New("management profile update service is required")
	}
	return s.service.UpdateProfile(ctx, input)
}

func NewManagementProfileUpdateHandler(service *managementauth.ProfileService) http.Handler {
	return newManagementProfileUpdateHandler(managementProfileUpdateServiceAdapter{service: service})
}

func NewManagementProfileUpdateHandlerWithOperationLog(service *managementauth.ProfileService, opts ManagementOperationLogOptions) http.Handler {
	return newManagementProfileUpdateHandler(
		managementProfileUpdateServiceAdapter{service: service},
		newManagementOperationLogOptions(opts),
	)
}

func newManagementProfileUpdateHandler(service managementProfileUpdateService, logOptions ...managementOperationLogOptions) http.Handler {
	operationLogs := effectiveManagementOperationLogOptions(logOptions)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authContext, ok := ManagementAuthContextFromRequest(r)
		if !ok || strings.TrimSpace(authContext.SystemAccountID) == "" {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		displayName, ok := decodeManagementProfileUpdateBody(w, r)
		if !ok {
			return
		}
		result, err := service.UpdateProfile(r.Context(), managementauth.ProfileUpdateInput{
			AuthContext: authContext,
			DisplayName: displayName,
		})
		if errors.Is(err, managementauth.ErrProfileDisplayNameInvalid) {
			writeMessageError(w, http.StatusBadRequest, "用户资料参数无效")
			return
		}
		if errors.Is(err, managementauth.ErrProfileDisplayNameWhitespace) {
			writeMessageError(w, http.StatusBadRequest, "显示名称不能包含空格")
			return
		}
		if errors.Is(err, managementauth.ErrProfileNotFound) {
			writeMessageError(w, http.StatusNotFound, "系统账户不存在")
			return
		}
		if errors.Is(err, managementauth.ErrProfileDisplayNameExists) {
			writeMessageError(w, http.StatusConflict, "用户名称已存在")
			return
		}
		if err != nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		recordProfileUpdateOperationLog(r, authContext, result, operationLogs)
		writeData(w, http.StatusOK, managementCurrentUserResponse{
			ID:                 result.Account.ID,
			Username:           result.Account.Username,
			DisplayName:        result.Account.DisplayName,
			Role:               result.Account.Role,
			MustChangePassword: result.Account.MustChangePassword,
		})
	})
}

func decodeManagementProfileUpdateBody(w http.ResponseWriter, r *http.Request) (string, bool) {
	var payload struct {
		DisplayName *string `json:"displayName"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil || payload.DisplayName == nil {
		writeManagementProfileBodyError(w, err)
		return "", false
	}
	var extra struct{}
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		writeMessageError(w, http.StatusBadRequest, "请求体无效")
		return "", false
	}
	return *payload.DisplayName, true
}

func writeManagementProfileBodyError(w http.ResponseWriter, err error) {
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
	writeMessageError(w, http.StatusBadRequest, "用户资料参数无效")
}

func recordProfileUpdateOperationLog(
	r *http.Request,
	authContext managementauth.Context,
	result managementauth.ProfileUpdateResult,
	opts managementOperationLogOptions,
) {
	if opts.submitter == nil || !result.Changed {
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
		Mode:                          "self",
		Module:                        "system_accounts",
		Action:                        "update",
		OperationKey:                  "auth.update_profile",
		ResourceType:                  "system_account",
		ResourceID:                    result.Account.ID,
		ResourceName:                  result.Account.DisplayName,
		Summary:                       "修改显示名称：" + result.Account.DisplayName,
		DetailLevel:                   "full",
		VisibilityScope:               "targeted",
		Changes: []port.OperationLogChange{
			{
				Field:  "displayName",
				Label:  "显示名称",
				Before: result.Before.DisplayName,
				After:  result.Account.DisplayName,
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
				VisibilityReason: "resource_owner",
				DetailLevel:      "full",
			},
		},
		CreatedAt: now().UTC(),
	}
	enqueueManagementOperationLog(r.Context(), opts, input)
}
