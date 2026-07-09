package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/modules/managementauth"
)

const managementAuthContextKey contextKey = "management_auth_context"

type ManagementAPIAuthenticator interface {
	AuthenticateCookie(ctx context.Context, cookieHeader string) (managementauth.Context, error)
}

type ManagementAPIAuthTouchAuthenticator interface {
	AuthenticateCookieAndTouch(ctx context.Context, cookieHeader string) (managementauth.Context, error)
}

type ManagementCurrentUserAuthenticator interface {
	AuthenticateCookieForCurrentUser(ctx context.Context, cookieHeader string) (managementauth.Context, error)
}

type ManagementCurrentUserTouchAuthenticator interface {
	AuthenticateCookieForCurrentUserAndTouch(ctx context.Context, cookieHeader string) (managementauth.Context, error)
}

type ManagementLogoutAuthenticator interface {
	LogoutCookie(ctx context.Context, cookieHeader string) error
}

type ManagementSessionService interface {
	List(ctx context.Context, input managementauth.SessionListInput) (managementauth.SessionListResult, error)
	Revoke(ctx context.Context, input managementauth.SessionRevokeInput) (managementauth.SessionRevokeResult, error)
}

type managementCurrentUserResponse struct {
	ID                 string `json:"id"`
	Username           string `json:"username"`
	DisplayName        string `json:"displayName"`
	Role               string `json:"role"`
	MustChangePassword bool   `json:"mustChangePassword"`
}

type managementLogoutResponse struct {
	LoggedOut bool `json:"loggedOut"`
}

func NewManagementAPIAuthMiddleware(authenticator ManagementAPIAuthenticator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if authenticator == nil {
				writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
				return
			}
			authContext, err := authenticator.AuthenticateCookie(r.Context(), r.Header.Get("Cookie"))
			if err != nil {
				writeManagementAuthError(w, err)
				return
			}
			ctx := context.WithValue(r.Context(), managementAuthContextKey, authContext)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func NewManagementAPIAuthTouchMiddleware(authenticator ManagementAPIAuthTouchAuthenticator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if authenticator == nil {
				writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
				return
			}
			authContext, err := authenticator.AuthenticateCookieAndTouch(r.Context(), r.Header.Get("Cookie"))
			if err != nil {
				writeManagementAuthError(w, err)
				return
			}
			ctx := context.WithValue(r.Context(), managementAuthContextKey, authContext)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func NewManagementCurrentUserHandler(authenticator ManagementCurrentUserAuthenticator) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if authenticator == nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		authContext, err := authenticator.AuthenticateCookieForCurrentUser(r.Context(), r.Header.Get("Cookie"))
		if err != nil {
			writeManagementAuthError(w, err)
			return
		}
		writeData(w, http.StatusOK, managementCurrentUserResponse{
			ID:                 authContext.SystemAccountID,
			Username:           authContext.Username,
			DisplayName:        authContext.DisplayName,
			Role:               authContext.Role,
			MustChangePassword: authContext.MustChangePassword,
		})
	})
}

func NewManagementLogoutHandler(authenticator ManagementLogoutAuthenticator, cfg config.Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if authenticator == nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		if err := authenticator.LogoutCookie(r.Context(), r.Header.Get("Cookie")); err != nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		clearManagementSessionCookie(w, cfg)
		writeData(w, http.StatusOK, managementLogoutResponse{LoggedOut: true})
	})
}

func NewManagementSessionListHandler(service *managementauth.SessionService) http.Handler {
	return newManagementSessionListHandler(service)
}

func NewManagementSessionRevokeHandler(service *managementauth.SessionService, cfg config.Config) http.Handler {
	return newManagementSessionRevokeHandler(service, cfg)
}

func newManagementSessionListHandler(service ManagementSessionService) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authContext, ok := ManagementAuthContextFromRequest(r)
		if !ok || strings.TrimSpace(authContext.SystemAccountID) == "" {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		if service == nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		page, err := managementSessionIntegerQueryValue(r.URL.Query(), "page")
		if err != nil {
			writeMessageError(w, http.StatusBadRequest, "会话参数无效")
			return
		}
		pageSize, err := managementSessionIntegerQueryValue(r.URL.Query(), "pageSize")
		if err != nil {
			writeMessageError(w, http.StatusBadRequest, "会话参数无效")
			return
		}
		result, err := service.List(r.Context(), managementauth.SessionListInput{
			SystemAccountID:  authContext.SystemAccountID,
			CurrentSessionID: authContext.SessionID,
			Page:             page,
			PageSize:         pageSize,
		})
		if errors.Is(err, managementauth.ErrSessionInputInvalid) {
			writeMessageError(w, http.StatusBadRequest, "会话参数无效")
			return
		}
		if err != nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		writeData(w, http.StatusOK, result)
	})
}

func newManagementSessionRevokeHandler(service ManagementSessionService, cfg config.Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authContext, ok := ManagementAuthContextFromRequest(r)
		if !ok || strings.TrimSpace(authContext.SystemAccountID) == "" {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		if service == nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		result, err := service.Revoke(r.Context(), managementauth.SessionRevokeInput{
			SystemAccountID:  authContext.SystemAccountID,
			SessionID:        chi.URLParam(r, "id"),
			CurrentSessionID: authContext.SessionID,
		})
		if errors.Is(err, managementauth.ErrSessionInputInvalid) {
			writeMessageError(w, http.StatusBadRequest, "会话参数无效")
			return
		}
		if errors.Is(err, managementauth.ErrSessionNotFound) {
			writeMessageError(w, http.StatusNotFound, "会话不存在")
			return
		}
		if err != nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		if result.Current {
			clearManagementSessionCookie(w, cfg)
		}
		writeData(w, http.StatusOK, result)
	})
}

func managementSessionIntegerQueryValue(values url.Values, key string) (int, error) {
	text := firstManagementQueryText(values, key)
	if text == "" {
		return 0, nil
	}
	value, err := strconv.Atoi(text)
	if err != nil {
		return 0, managementauth.ErrSessionInputInvalid
	}
	return value, nil
}

func ManagementAuthContextFromRequest(r *http.Request) (managementauth.Context, bool) {
	value, ok := r.Context().Value(managementAuthContextKey).(managementauth.Context)
	return value, ok
}

func clearManagementSessionCookie(w http.ResponseWriter, cfg config.Config) {
	http.SetCookie(w, &http.Cookie{
		Name:     managementauth.SessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   cfg.CookieSecure,
		SameSite: managementCookieSameSite(cfg),
	})
}

func writeManagementAuthError(w http.ResponseWriter, err error) {
	var authErr *managementauth.AuthError
	if !errors.As(err, &authErr) {
		writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	payload := map[string]string{"message": authErr.Message}
	if authErr.Code != "" {
		payload["code"] = authErr.Code
	}
	writeJSON(w, authErr.StatusCode, payload)
}
