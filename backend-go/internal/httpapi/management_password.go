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
)

const managementPasswordChangeMaxBodyBytes = 256 << 10

type managementPasswordChangeService interface {
	ChangePassword(ctx context.Context, input managementauth.PasswordChangeInput) (managementauth.PasswordChangeResult, error)
}

type managementPasswordChangeServiceAdapter struct {
	service *managementauth.PasswordService
}

func (s managementPasswordChangeServiceAdapter) ChangePassword(ctx context.Context, input managementauth.PasswordChangeInput) (managementauth.PasswordChangeResult, error) {
	if s.service == nil {
		return managementauth.PasswordChangeResult{}, errors.New("management password change service is required")
	}
	return s.service.ChangePassword(ctx, input)
}

type managementPasswordChangeResponse struct {
	ID                     string `json:"id"`
	Username               string `json:"username"`
	DisplayName            string `json:"displayName"`
	Description            string `json:"description,omitempty"`
	Role                   string `json:"role"`
	Status                 string `json:"status"`
	MustChangePassword     bool   `json:"mustChangePassword"`
	ImageGenerationEnabled bool   `json:"imageGenerationEnabled"`
	LastLoginAt            string `json:"lastLoginAt,omitempty"`
	CreatedAt              string `json:"createdAt"`
	UpdatedAt              string `json:"updatedAt"`
}

func NewManagementPasswordChangeHandler(authenticator ManagementCurrentUserAuthenticator, service *managementauth.PasswordService) http.Handler {
	return newManagementPasswordChangeHandler(authenticator, managementPasswordChangeServiceAdapter{service: service})
}

func newManagementPasswordChangeHandler(authenticator ManagementCurrentUserAuthenticator, service managementPasswordChangeService) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if authenticator == nil || service == nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		authContext, err := authenticator.AuthenticateCookieForCurrentUser(r.Context(), r.Header.Get("Cookie"))
		if err != nil {
			writeManagementAuthError(w, err)
			return
		}
		input, ok := decodeManagementPasswordChangeBody(w, r)
		if !ok {
			return
		}
		result, err := service.ChangePassword(r.Context(), managementauth.PasswordChangeInput{
			AuthContext: authContext,
			OldPassword: input.oldPassword,
			NewPassword: input.newPassword,
		})
		switch {
		case errors.Is(err, managementauth.ErrPasswordInvalid):
			writeMessageError(w, http.StatusBadRequest, "密码参数无效")
			return
		case errors.Is(err, managementauth.ErrPasswordWhitespace):
			writeMessageError(w, http.StatusBadRequest, "登录密码不能包含空格")
			return
		case errors.Is(err, managementauth.ErrPasswordOldRequired):
			writeMessageError(w, http.StatusBadRequest, "请填写当前密码")
			return
		case errors.Is(err, managementauth.ErrPasswordOldIncorrect):
			writeMessageError(w, http.StatusBadRequest, "当前密码不正确")
			return
		case errors.Is(err, managementauth.ErrPasswordAccountGone):
			writeMessageError(w, http.StatusNotFound, "系统账户不存在")
			return
		case err != nil:
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		writeData(w, http.StatusOK, managementPasswordChangeResponseFromSummary(result.Account))
	})
}

type managementPasswordChangeBody struct {
	oldPassword *string
	newPassword string
}

func decodeManagementPasswordChangeBody(w http.ResponseWriter, r *http.Request) (managementPasswordChangeBody, bool) {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, managementPasswordChangeMaxBodyBytes))
	decoder.DisallowUnknownFields()
	var payload map[string]json.RawMessage
	if err := decoder.Decode(&payload); err != nil {
		writeManagementPasswordChangeBodyError(w, err)
		return managementPasswordChangeBody{}, false
	}
	var extra struct{}
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		writeMessageError(w, http.StatusBadRequest, "请求体无效")
		return managementPasswordChangeBody{}, false
	}
	for field := range payload {
		if field != "oldPassword" && field != "newPassword" {
			writeMessageError(w, http.StatusBadRequest, "密码参数无效")
			return managementPasswordChangeBody{}, false
		}
	}
	newPassword, ok := decodeRequiredManagementPasswordString(payload, "newPassword")
	if !ok {
		writeMessageError(w, http.StatusBadRequest, "密码参数无效")
		return managementPasswordChangeBody{}, false
	}
	oldPassword, ok := decodeOptionalManagementPasswordString(payload, "oldPassword")
	if !ok {
		writeMessageError(w, http.StatusBadRequest, "密码参数无效")
		return managementPasswordChangeBody{}, false
	}
	return managementPasswordChangeBody{oldPassword: oldPassword, newPassword: newPassword}, true
}

func writeManagementPasswordChangeBodyError(w http.ResponseWriter, err error) {
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
	writeMessageError(w, http.StatusBadRequest, "密码参数无效")
}

func decodeRequiredManagementPasswordString(payload map[string]json.RawMessage, field string) (string, bool) {
	raw, exists := payload[field]
	if !exists {
		return "", false
	}
	return decodeManagementPasswordString(raw)
}

func decodeOptionalManagementPasswordString(payload map[string]json.RawMessage, field string) (*string, bool) {
	raw, exists := payload[field]
	if !exists {
		return nil, true
	}
	value, ok := decodeManagementPasswordString(raw)
	if !ok {
		return nil, false
	}
	return &value, true
}

func decodeManagementPasswordString(raw json.RawMessage) (string, bool) {
	if strings.EqualFold(strings.TrimSpace(string(raw)), "null") {
		return "", false
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false
	}
	return value, true
}

func managementPasswordChangeResponseFromSummary(account managementauth.SystemAccountSummary) managementPasswordChangeResponse {
	return managementPasswordChangeResponse{
		ID:                     account.ID,
		Username:               account.Username,
		DisplayName:            account.DisplayName,
		Description:            account.Description,
		Role:                   account.Role,
		Status:                 account.Status,
		MustChangePassword:     account.MustChangePassword,
		ImageGenerationEnabled: account.ImageGenerationEnabled,
		LastLoginAt:            managementPasswordTimeString(account.LastLoginAt),
		CreatedAt:              managementPasswordRequiredTimeString(account.CreatedAt),
		UpdatedAt:              managementPasswordRequiredTimeString(account.UpdatedAt),
	}
}

func managementPasswordTimeString(value *time.Time) string {
	if value == nil {
		return ""
	}
	return managementPasswordRequiredTimeString(*value)
}

func managementPasswordRequiredTimeString(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}
