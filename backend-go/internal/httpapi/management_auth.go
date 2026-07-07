package httpapi

import (
	"context"
	"errors"
	"net/http"

	"juhe-ai/backend-go/internal/modules/managementauth"
)

const managementAuthContextKey contextKey = "management_auth_context"

type ManagementAPIAuthenticator interface {
	AuthenticateCookie(ctx context.Context, cookieHeader string) (managementauth.Context, error)
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

func ManagementAuthContextFromRequest(r *http.Request) (managementauth.Context, bool) {
	value, ok := r.Context().Value(managementAuthContextKey).(managementauth.Context)
	return value, ok
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
