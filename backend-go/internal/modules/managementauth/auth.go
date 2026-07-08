package managementauth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

const (
	SessionCookieName = "juhe_ai_session"

	ErrorCodeMustChangePassword = "must_change_password"
)

type Context struct {
	SystemAccountID    string
	Username           string
	DisplayName        string
	Role               string
	MustChangePassword bool
	SessionID          string
}

type Authenticator struct {
	store   port.ManagementSessionReader
	revoker port.ManagementSessionRevoker
	now     func() time.Time
}

type AuthenticatorOptions struct {
	Store   port.ManagementSessionReader
	Revoker port.ManagementSessionRevoker
	Now     func() time.Time
}

type AuthError struct {
	StatusCode int
	Code       string
	Message    string
}

func (e *AuthError) Error() string {
	return e.Message
}

func NewAuthenticator(opts AuthenticatorOptions) *Authenticator {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	revoker := opts.Revoker
	if revoker == nil {
		if storeRevoker, ok := opts.Store.(port.ManagementSessionRevoker); ok {
			revoker = storeRevoker
		}
	}
	return &Authenticator{store: opts.Store, revoker: revoker, now: now}
}

func HashSessionToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func ParseCookie(cookieHeader string) map[string]string {
	result := map[string]string{}
	for _, part := range strings.Split(cookieHeader, ";") {
		name, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok || name == "" {
			continue
		}
		decoded, err := url.PathUnescape(value)
		if err != nil {
			continue
		}
		value = decoded
		result[name] = value
	}
	return result
}

func (a *Authenticator) AuthenticateCookie(ctx context.Context, cookieHeader string) (Context, error) {
	return a.authenticateCookie(ctx, cookieHeader, true)
}

func (a *Authenticator) AuthenticateCookieForCurrentUser(ctx context.Context, cookieHeader string) (Context, error) {
	return a.authenticateCookie(ctx, cookieHeader, false)
}

func (a *Authenticator) LogoutCookie(ctx context.Context, cookieHeader string) error {
	if a == nil || a.revoker == nil {
		return errors.New("management auth session revoker is required")
	}
	token := strings.TrimSpace(ParseCookie(cookieHeader)[SessionCookieName])
	if token == "" {
		return nil
	}
	return a.revoker.RevokeManagementSessionByTokenHash(ctx, HashSessionToken(token))
}

func (a *Authenticator) authenticateCookie(ctx context.Context, cookieHeader string, requirePasswordChangeCompleted bool) (Context, error) {
	if a == nil || a.store == nil {
		return Context{}, errors.New("management auth store is required")
	}
	token := strings.TrimSpace(ParseCookie(cookieHeader)[SessionCookieName])
	if token == "" {
		return Context{}, &AuthError{StatusCode: http.StatusUnauthorized, Message: "请先登录"}
	}

	session, found, err := a.store.FindManagementSessionByTokenHash(ctx, HashSessionToken(token))
	if err != nil {
		return Context{}, err
	}
	if !found || !session.ExpiresAt.After(a.now().UTC()) || session.Status != "active" {
		return Context{}, &AuthError{StatusCode: http.StatusUnauthorized, Message: "登录会话已过期"}
	}

	mustChangePassword := session.MustChangePassword && !IsAdminRole(session.Role)
	authContext := Context{
		SystemAccountID:    session.AccountID,
		Username:           session.Username,
		DisplayName:        session.DisplayName,
		Role:               session.Role,
		MustChangePassword: mustChangePassword,
		SessionID:          session.SessionID,
	}
	if requirePasswordChangeCompleted && authContext.MustChangePassword {
		return Context{}, &AuthError{StatusCode: http.StatusForbidden, Code: ErrorCodeMustChangePassword, Message: "请先修改初始密码"}
	}
	return authContext, nil
}

func IsAdminRole(role string) bool {
	return role == "super_admin" || role == "admin"
}
