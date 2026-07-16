package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/modules/managementauth"
)

const managementLoginMaxBodyBytes = 256 << 10

type ManagementLoginService interface {
	Login(ctx context.Context, input managementauth.LoginInput) (managementauth.LoginResult, error)
}

type managementLoginServiceAdapter struct {
	service *managementauth.LoginService
}

func (s managementLoginServiceAdapter) Login(ctx context.Context, input managementauth.LoginInput) (managementauth.LoginResult, error) {
	if s.service == nil {
		return managementauth.LoginResult{}, errors.New("management login service is required")
	}
	return s.service.Login(ctx, input)
}

type managementLoginBody struct {
	username    string
	password    string
	captchaID   string
	captchaCode string
}

func NewManagementLoginHandler(service *managementauth.LoginService, cfg config.Config) http.Handler {
	return newManagementLoginHandler(managementLoginServiceAdapter{service: service}, cfg)
}

func newManagementLoginHandler(service ManagementLoginService, cfg config.Config) http.Handler {
	clientIPs := newClientIPResolver(cfg)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if service == nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		body, ok := decodeManagementLoginBody(w, r, cfg.AuthCaptchaDisabled)
		if !ok {
			return
		}
		result, err := service.Login(r.Context(), managementauth.LoginInput{
			Username:    body.username,
			Password:    body.password,
			CaptchaID:   body.captchaID,
			CaptchaCode: body.captchaCode,
			ClientIP:    clientIPs.FromRequest(r),
		})
		switch {
		case errors.Is(err, managementauth.ErrLoginInvalidInput):
			writeMessageError(w, http.StatusBadRequest, "登录参数无效")
			return
		case errors.Is(err, managementauth.ErrLoginWhitespace):
			writeMessageError(w, http.StatusBadRequest, "用户名和密码不能包含空格")
			return
		case errors.Is(err, managementauth.ErrLoginCaptchaInvalid):
			writeMessageError(w, http.StatusBadRequest, "验证码错误或已过期")
			return
		case errors.Is(err, managementauth.ErrLoginCredentialsInvalid):
			writeMessageError(w, http.StatusUnauthorized, "账号或密码错误")
			return
		case err != nil:
			var limitErr *managementauth.LoginLimitError
			if errors.As(err, &limitErr) {
				if limitErr.RetryAfterSeconds > 0 {
					w.Header().Set("Retry-After", intString(limitErr.RetryAfterSeconds))
				}
				message := strings.TrimSpace(limitErr.Message)
				if message == "" {
					message = managementauth.LoginGuardIPBlockedMessage
				}
				writeMessageError(w, http.StatusTooManyRequests, message)
				return
			}
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		setManagementSessionCookie(w, cfg, result.SessionToken, result.SessionExpiresAt)
		writeData(w, http.StatusOK, managementCurrentUserResponse{
			ID:                 result.Account.ID,
			Username:           result.Account.Username,
			DisplayName:        result.Account.DisplayName,
			Role:               result.Account.Role,
			MustChangePassword: result.Account.MustChangePassword,
		})
	})
}

func decodeManagementLoginBody(w http.ResponseWriter, r *http.Request, captchaDisabled bool) (managementLoginBody, bool) {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, managementLoginMaxBodyBytes))
	decoder.DisallowUnknownFields()
	var payload map[string]json.RawMessage
	if err := decoder.Decode(&payload); err != nil {
		writeManagementLoginBodyError(w, err)
		return managementLoginBody{}, false
	}
	var extra struct{}
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		writeMessageError(w, http.StatusBadRequest, "请求体无效")
		return managementLoginBody{}, false
	}
	for field := range payload {
		if field != "username" && field != "password" && field != "captchaId" && field != "captchaCode" {
			writeMessageError(w, http.StatusBadRequest, "登录参数无效")
			return managementLoginBody{}, false
		}
	}
	username, ok := decodeRequiredManagementLoginString(payload, "username")
	if !ok {
		writeMessageError(w, http.StatusBadRequest, "登录参数无效")
		return managementLoginBody{}, false
	}
	password, ok := decodeRequiredManagementLoginString(payload, "password")
	if !ok {
		writeMessageError(w, http.StatusBadRequest, "登录参数无效")
		return managementLoginBody{}, false
	}
	var captchaID, captchaCode string
	if !captchaDisabled {
		captchaID, ok = decodeRequiredManagementLoginString(payload, "captchaId")
		if !ok || strings.TrimSpace(captchaID) == "" {
			writeMessageError(w, http.StatusBadRequest, "登录参数无效")
			return managementLoginBody{}, false
		}
		captchaCode, ok = decodeRequiredManagementLoginString(payload, "captchaCode")
		if !ok || strings.TrimSpace(captchaCode) == "" {
			writeMessageError(w, http.StatusBadRequest, "登录参数无效")
			return managementLoginBody{}, false
		}
	}
	return managementLoginBody{
		username:    username,
		password:    password,
		captchaID:   captchaID,
		captchaCode: captchaCode,
	}, true
}

func writeManagementLoginBodyError(w http.ResponseWriter, err error) {
	var maxBytesErr *http.MaxBytesError
	if errors.As(err, &maxBytesErr) {
		writeMessageError(w, http.StatusRequestEntityTooLarge, "请求体过大")
		return
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		writeMessageError(w, http.StatusBadRequest, "请求体无效")
		return
	}
	var syntaxErr *json.SyntaxError
	if errors.As(err, &syntaxErr) {
		writeMessageError(w, http.StatusBadRequest, "请求体无效")
		return
	}
	writeMessageError(w, http.StatusBadRequest, "登录参数无效")
}

func decodeRequiredManagementLoginString(payload map[string]json.RawMessage, field string) (string, bool) {
	raw, exists := payload[field]
	if !exists {
		return "", false
	}
	if strings.EqualFold(strings.TrimSpace(string(raw)), "null") {
		return "", false
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false
	}
	return value, true
}

func setManagementSessionCookie(w http.ResponseWriter, cfg config.Config, token string, expiresAt time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     managementauth.SessionCookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   int(managementauth.ManagementSessionTTL / time.Second),
		Expires:  expiresAt.UTC(),
		HttpOnly: true,
		Secure:   cfg.CookieSecure,
		SameSite: managementCookieSameSite(cfg),
	})
}

func managementCookieSameSite(cfg config.Config) http.SameSite {
	mode, err := cfg.CookieSameSiteMode()
	if err != nil {
		mode = "lax"
	}
	switch mode {
	case "strict":
		return http.SameSiteStrictMode
	case "none":
		return http.SameSiteNoneMode
	default:
		return http.SameSiteLaxMode
	}
}
