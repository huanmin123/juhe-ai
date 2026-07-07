package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"juhe-ai/backend-go/internal/modules/publicapi"
	"juhe-ai/backend-go/internal/store/port"
)

const (
	ErrorCodeTokenMissing     = "external_source_token_missing"
	ErrorCodeUnauthorized     = "external_source_unauthorized"
	ErrorCodeSourceDisabled   = "external_source_disabled"
	ErrorCodeSourceExpired    = "external_source_expired"
	ErrorCodeTokenUnavailable = "external_source_token_unavailable"
	ErrorCodeScopeForbidden   = "external_source_scope_forbidden"
)

const lastUsedTouchInterval = time.Minute

var bearerTokenPattern = regexp.MustCompile(`(?i)^Bearer\s+(.+)$`)

type Authenticator struct {
	store port.PublicAPIAuthStore
	now   func() time.Time
}

type AuthenticatorOptions struct {
	Store port.PublicAPIAuthStore
	Now   func() time.Time
}

type AuthContext struct {
	SourceRefID string
	SourceName  string
	TokenID     string
	TokenName   string
	TokenPrefix string
	Scopes      []string
	RateLimits  []port.PublicAPIRateLimitRule
	IsTestToken bool
}

type AuthError struct {
	StatusCode int
	Code       string
	Message    string
	Context    *AuthContext
}

func (e *AuthError) Error() string {
	return e.Message
}

func ExternalSourceRateLimitKey(ctx AuthContext) string {
	return ctx.SourceRefID + ":" + ctx.TokenID + ":" + ctx.TokenPrefix
}

func NewAuthenticator(opts AuthenticatorOptions) *Authenticator {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Authenticator{
		store: opts.Store,
		now:   now,
	}
}

func ParseBearerToken(headerValue string) (string, bool) {
	value := strings.TrimSpace(headerValue)
	if value == "" {
		return "", false
	}
	match := bearerTokenPattern.FindStringSubmatch(value)
	if len(match) != 2 {
		return "", false
	}
	token := strings.TrimSpace(match[1])
	return token, token != ""
}

func HashExternalSourceToken(token string) string {
	sum := sha256.Sum256([]byte("external-integration-source-token:" + token))
	return hex.EncodeToString(sum[:])
}

func (a *Authenticator) Authenticate(ctx context.Context, authorizationHeader string, requiredScope string) (AuthContext, error) {
	if a == nil || a.store == nil {
		return AuthContext{}, errors.New("public api auth store is required")
	}

	token, ok := ParseBearerToken(authorizationHeader)
	if !ok {
		return AuthContext{}, newAuthError(http.StatusUnauthorized, ErrorCodeTokenMissing, "缺少来源系统 token", nil)
	}

	record, found, err := a.store.FindPublicAPIAuthTokenByHash(ctx, HashExternalSourceToken(token))
	if err != nil {
		return AuthContext{}, err
	}
	if !found {
		return AuthContext{}, newAuthError(http.StatusUnauthorized, ErrorCodeUnauthorized, "来源系统或 token 无效", nil)
	}

	now := a.now().UTC()
	authContext := authContextFromRecord(record)
	if strings.ToLower(record.SourceStatus) != publicapi.SourceStatusActive {
		return AuthContext{}, newAuthError(http.StatusForbidden, ErrorCodeSourceDisabled, "来源系统未启用", &authContext)
	}
	if record.SourceExpiresAt != nil && !record.SourceExpiresAt.After(now) {
		return AuthContext{}, newAuthError(http.StatusForbidden, ErrorCodeSourceExpired, "来源系统已过期", &authContext)
	}
	if strings.ToLower(record.TokenStatus) != publicapi.TokenStatusActive || (record.TokenExpiresAt != nil && !record.TokenExpiresAt.After(now)) {
		return AuthContext{}, newAuthError(http.StatusUnauthorized, ErrorCodeTokenUnavailable, "来源系统 token 不可用", &authContext)
	}
	if requiredScope != "" && !containsScope(authContext.Scopes, requiredScope) {
		return AuthContext{}, newAuthError(http.StatusForbidden, ErrorCodeScopeForbidden, "来源系统没有调用该接口的权限", &authContext)
	}

	touch := port.PublicAPIAuthLastUsedTouch{
		SourceRefID: record.SourceRefID,
		TokenID:     record.TokenID,
		Now:         now,
		TouchSource: shouldTouchLastUsed(record.SourceLastUsedAt, now),
		TouchToken:  shouldTouchLastUsed(record.TokenLastUsedAt, now),
	}
	if touch.TouchSource || touch.TouchToken {
		if err := a.store.TouchPublicAPIAuthLastUsed(ctx, touch); err != nil {
			return AuthContext{}, err
		}
	}

	return authContext, nil
}

func newAuthError(statusCode int, code string, message string, context *AuthContext) *AuthError {
	return &AuthError{
		StatusCode: statusCode,
		Code:       code,
		Message:    message,
		Context:    context,
	}
}

func authContextFromRecord(record port.PublicAPIAuthRecord) AuthContext {
	return AuthContext{
		SourceRefID: record.SourceRefID,
		SourceName:  record.SourceName,
		TokenID:     record.TokenID,
		TokenName:   record.TokenName,
		TokenPrefix: record.TokenPrefix,
		Scopes:      intersectScopes(record.SourceScopes, record.TokenScopes),
		RateLimits:  append([]port.PublicAPIRateLimitRule(nil), record.SourceRateLimits...),
		IsTestToken: publicapi.IsBuiltInTestSource(record.SourceRefID) || publicapi.IsBuiltInTestToken(record.TokenID),
	}
}

func intersectScopes(sourceScopes []string, tokenScopes []string) []string {
	sourceSet := make(map[string]struct{}, len(sourceScopes))
	for _, scope := range sourceScopes {
		sourceSet[scope] = struct{}{}
	}
	granted := make([]string, 0, len(tokenScopes))
	for _, scope := range tokenScopes {
		if _, ok := sourceSet[scope]; ok {
			granted = append(granted, scope)
		}
	}
	return granted
}

func containsScope(scopes []string, required string) bool {
	for _, scope := range scopes {
		if scope == required {
			return true
		}
	}
	return false
}

func shouldTouchLastUsed(previous *time.Time, now time.Time) bool {
	return previous == nil || now.Sub(previous.UTC()) >= lastUsedTouchInterval
}
