package authsys

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	businesssettings "github.com/huanminabc/juhe-ai/backend-go-gateway/internal/business/settings"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/businessauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckauth"
)

const (
	SessionCookieName = "juhe_ai_session"
	sessionMaxAge     = 14 * 24 * time.Hour
)

var temporaryTokenPattern = regexp.MustCompile(`^juhe_tmp_[A-Za-z0-9_-]{43}$`)
var bearerPattern = regexp.MustCompile(`(?i)^Bearer\s+(.+)$`)

// ResolveSystemAccessToken mirrors temporary-access-token.ts: Authorization
// takes absolute precedence; absence of both yields none.
func ResolveSystemAccessToken(authorization, cookieToken string) (kind string, token string, temporary bool) {
	if authorization != "" {
		trimmed := strings.TrimSpace(authorization)
		match := bearerPattern.FindStringSubmatch(trimmed)
		if match == nil {
			return "invalid", "", false
		}
		candidate := match[1]
		if !temporaryTokenPattern.MatchString(candidate) {
			return "invalid", "", false
		}
		return "token", candidate, true
	}
	if cookieToken == "" {
		return "none", "", false
	}
	return "token", cookieToken, false
}

// ParseCookie mirrors parseCookie from auth.routes.ts.
func ParseCookie(cookieHeader string) map[string]string {
	result := map[string]string{}
	for _, part := range strings.Split(cookieHeader, ";") {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}
		pieces := strings.SplitN(trimmed, "=", 2)
		if len(pieces) != 2 || pieces[0] == "" {
			continue
		}
		result[pieces[0]] = pieces[1]
	}
	return result
}

func secretHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

type authContextKey struct{}

// SystemSettingReader is the narrow settings boundary needed by profile. It
// keeps auth HTTP tests independent from a concrete settings store.
type SystemSettingReader interface {
	GetSystem(context.Context, string, string) (businesssettings.Setting, bool, error)
}

// AuthContext mirrors RequestAuthContext.
type AuthContext struct {
	SystemAccountID    string
	Username           string
	DisplayName        string
	Role               string
	MustChangePassword bool
	SessionID          string
}

func WithAuthContext(ctx context.Context, auth *AuthContext) context.Context {
	return context.WithValue(ctx, authContextKey{}, auth)
}

// AuthContextFrom extracts the authenticated actor, if any.
func AuthContextFrom(r *http.Request) *AuthContext {
	if value, ok := r.Context().Value(authContextKey{}).(*AuthContext); ok {
		return value
	}
	return nil
}

// Deps bundles the K2 collaborators.
type Deps struct {
	Port       businessauth.Port
	Accounts   *AccountStore
	Settings   SystemSettingReader
	Captcha    *modelcheckauth.CaptchaService
	LoginGuard *modelcheckauth.LoginGuard
	// TemporaryAccessIPAllowlist is the parsed, exact source-IP allowlist for
	// POST /auth/temporary-access-tokens. An empty list denies every source.
	TemporaryAccessIPAllowlist []string
	Now                        func() time.Time
	CaptchaDisabled            bool
	DevAutoLoginUsername       string
	Sink                       OperationLogSink
}

// RequireSession mirrors requireSessionContext: token resolution, dev auto
// login when no token, session lookup, and touch on side-effect requests.
func (d *Deps) RequireSession(touch bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cookies := ParseCookie(r.Header.Get("Cookie"))
			kind, token, _ := ResolveSystemAccessToken(r.Header.Get("Authorization"), cookies[SessionCookieName])
			switch kind {
			case "invalid":
				kernel.WriteError(w, http.StatusUnauthorized, "访问令牌无效或已过期")
				return
			case "none":
				if auth := d.developmentAutoLogin(); auth != nil {
					next.ServeHTTP(w, r.WithContext(WithAuthContext(r.Context(), auth)))
					return
				}
				kernel.WriteError(w, http.StatusUnauthorized, "请先登录")
				return
			}
			actor, err := d.Port.Authenticate(r.Context(), token, false, touch)
			if err != nil {
				if errors.Is(err, modelcheckauth.ErrSessionExpired) {
					kernel.WriteError(w, http.StatusUnauthorized, "登录会话已过期")
					return
				}
				kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
				return
			}
			auth := &AuthContext{
				SystemAccountID:    actor.SystemAccountID,
				Username:           actor.Username,
				DisplayName:        actor.DisplayName,
				Role:               actor.Role,
				MustChangePassword: actor.MustChangePassword,
				SessionID:          actor.SessionID,
			}
			next.ServeHTTP(w, r.WithContext(WithAuthContext(r.Context(), auth)))
		})
	}
}

// RequireAdmin mirrors requireAdmin.
func (d *Deps) RequireAdmin(next http.Handler) http.Handler {
	return d.RequireSession(true)(requireRole("admin", "需要管理员权限")(next))
}

// RequireSuperAdmin mirrors requireSuperAdmin.
func (d *Deps) RequireSuperAdmin(next http.Handler) http.Handler {
	return d.RequireSession(true)(requireRole("super_admin", "需要超级管理员权限")(next))
}

func requireRole(allowedRole, message string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			auth := AuthContextFrom(r)
			if auth == nil {
				kernel.WriteError(w, http.StatusUnauthorized, "请先登录")
				return
			}
			ok := auth.Role == allowedRole || (allowedRole == "admin" && auth.Role == "super_admin")
			if !ok {
				kernel.WriteError(w, http.StatusForbidden, message)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ForceSelfAccessScope mirrors forceSelfAccessScope: self routes pin the
// access scope to the caller.
func ForceSelfAccessScope(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := AuthContextFrom(r)
		if auth == nil {
			kernel.WriteError(w, http.StatusUnauthorized, "请先登录")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (d *Deps) developmentAutoLogin() *AuthContext {
	if d.DevAutoLoginUsername == "" {
		return nil
	}
	summary, err := d.Accounts.FindByUsername(context.Background(), d.DevAutoLoginUsername)
	if err != nil || summary.ID == "" || summary.Status != "active" {
		return nil
	}
	return &AuthContext{
		SystemAccountID:    summary.ID,
		Username:           summary.Username,
		DisplayName:        summary.DisplayName,
		Role:               summary.Role,
		MustChangePassword: false,
		SessionID:          "development-auto-login",
	}
}

// SetSessionCookie writes the session cookie with the Node contract
// (HttpOnly, Path=/, SameSite from config, MaxAge 14 days).
func SetSessionCookie(w http.ResponseWriter, token string, sameSite string, secure bool, maxAge time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   int(maxAge / time.Second),
		HttpOnly: true,
		SameSite: sameSiteMode(sameSite),
		Secure:   secure,
	})
}

func ClearSessionCookie(w http.ResponseWriter, sameSite string, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name: SessionCookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true,
		SameSite: sameSiteMode(sameSite), Secure: secure,
	})
}

func sameSiteMode(value string) http.SameSite {
	switch strings.ToLower(value) {
	case "strict":
		return http.SameSiteStrictMode
	case "none":
		return http.SameSiteNoneMode
	default:
		return http.SameSiteLaxMode
	}
}

// CurrentUserSummary mirrors currentUserSummary.
type CurrentUserSummary struct {
	ID                 string `json:"id"`
	Username           string `json:"username"`
	DisplayName        string `json:"displayName"`
	Role               string `json:"role"`
	MustChangePassword bool   `json:"mustChangePassword"`
}

func currentUserSummary(auth *AuthContext) CurrentUserSummary {
	return CurrentUserSummary{
		ID: auth.SystemAccountID, Username: auth.Username, DisplayName: auth.DisplayName,
		Role: auth.Role, MustChangePassword: auth.MustChangePassword,
	}
}
