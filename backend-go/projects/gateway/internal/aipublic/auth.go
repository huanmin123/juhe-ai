// Token validation for the /__aipublic__ family, ported from
// backend/src/storage/external-integration-source-auth.repository.ts plus
// external-source-auth.middleware.ts context plumbing. The token hash is the
// Node-compatible sha256("external-integration-source-token:<token>") (the
// apikeys.HashSecret helper used by the M16b admin store). last_used_at is
// touched per row at most once per 60s like Node's touchLastUsed.
package aipublic

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/apikeys"
)

// touchLastUsedIntervalMs mirrors touchLastUsedIntervalMs.
const touchLastUsedIntervalMs = 60_000

// RateLimitRule mirrors ExternalIntegrationRateLimitRule.
type RateLimitRule struct {
	WindowSeconds int `json:"windowSeconds"`
	MaxRequests   int `json:"maxRequests"`
}

// AuthContext mirrors ExternalIntegrationSourceAuthContext.
type AuthContext struct {
	SourceRefID     string
	SourceName      string
	TokenID         string
	TokenName       string
	TokenPrefix     string
	Scopes          []string
	RateLimits      []RateLimitRule
	AuthenticatedAt string
	IsTestToken     bool
}

// AuthError mirrors ExternalIntegrationSourceAuthResult failure branches.
type AuthError struct {
	StatusCode int
	Code       string
	Message    string
}

// tokenAuthRow mirrors ExternalIntegrationSourceTokenRow (normalized).
type tokenAuthRow struct {
	sourceRowID      string
	sourceName       string
	sourceStatus     string
	sourceScopesJSON string
	sourceRateLimits string
	sourceExpiresAt  sql.NullString
	sourceLastUsedAt sql.NullString
	tokenID          string
	tokenName        string
	tokenPrefix      string
	tokenSuffix      string
	tokenStatus      string
	tokenScopesJSON  string
	tokenExpiresAt   sql.NullString
	tokenLastUsedAt  sql.NullString
}

func (d *Deps) table(name string) string {
	if d.PGDialect {
		return "juhe_business." + name
	}
	return name
}

// ValidateToken mirrors validateExternalIntegrationSourceTokenAsync: hash
// lookup, source/token status + expiry checks, scope intersection and the
// last_used touch on success.
func (d *Deps) ValidateToken(ctx context.Context, token string, requiredScope string) (*AuthContext, *AuthError) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, &AuthError{StatusCode: 401, Code: "external_source_token_missing", Message: "缺少来源系统 token"}
	}
	row, err := d.loadTokenForAuth(ctx, token)
	if err != nil {
		return nil, &AuthError{StatusCode: 500, Code: "external_source_internal_error", Message: "服务器内部错误"}
	}
	if row == nil {
		return nil, &AuthError{StatusCode: 401, Code: "external_source_unauthorized", Message: "来源系统或 token 无效"}
	}
	nowText := d.nowISO()
	authContext, authErr := d.validateTokenRow(row, requiredScope, nowText)
	if authErr != nil {
		return authContext, authErr
	}
	d.touchLastUsed(ctx, row, nowText)
	return authContext, nil
}

func (d *Deps) loadTokenForAuth(ctx context.Context, token string) (*tokenAuthRow, error) {
	var row tokenAuthRow
	err := d.db().QueryRowContext(ctx, d.bind(`SELECT
		sources.id, sources.name, sources.status, sources.scopes_json, sources.rate_limits_json,
		sources.expires_at, sources.last_used_at,
		tokens.id, tokens.name, tokens.token_prefix, tokens.token_suffix, tokens.status,
		tokens.scopes_json, tokens.expires_at, tokens.last_used_at
	FROM `+d.table("external_integration_source_tokens")+` AS tokens
	JOIN `+d.table("external_integration_sources")+` AS sources ON sources.id = tokens.source_ref_id
	WHERE tokens.token_hash = ?
	LIMIT 1`), apikeys.HashSecret("external-integration-source-token:"+token)).Scan(
		&row.sourceRowID, &row.sourceName, &row.sourceStatus, &row.sourceScopesJSON, &row.sourceRateLimits,
		&row.sourceExpiresAt, &row.sourceLastUsedAt,
		&row.tokenID, &row.tokenName, &row.tokenPrefix, &row.tokenSuffix, &row.tokenStatus,
		&row.tokenScopesJSON, &row.tokenExpiresAt, &row.tokenLastUsedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// validateTokenRow mirrors validateExternalIntegrationSourceTokenRow: granted
// scopes are the token scopes intersected with the source scopes (in token
// order), status/expiry gate in the Node order, then the required scope.
func (d *Deps) validateTokenRow(row *tokenAuthRow, requiredScope, nowText string) (*AuthContext, *AuthError) {
	sourceScopes := decodeScopesList(row.sourceScopesJSON)
	tokenScopes := decodeScopesList(row.tokenScopesJSON)
	granted := make([]string, 0, len(tokenScopes))
	for _, scope := range tokenScopes {
		for _, candidate := range sourceScopes {
			if scope == candidate {
				granted = append(granted, scope)
				break
			}
		}
	}
	context := &AuthContext{
		SourceRefID:     row.sourceRowID,
		SourceName:      row.sourceName,
		TokenID:         row.tokenID,
		TokenName:       row.tokenName,
		TokenPrefix:     row.tokenPrefix,
		Scopes:          granted,
		RateLimits:      decodeRateLimitsList(row.sourceRateLimits),
		AuthenticatedAt: nowText,
		IsTestToken:     row.sourceRowID == builtInTestSourceID || row.tokenID == builtInTestTokenID,
	}
	if row.sourceStatus != "active" {
		return context, &AuthError{StatusCode: 403, Code: "external_source_disabled", Message: "来源系统未启用"}
	}
	nowMs := rfc3339Millis(nowText)
	if sourceExpires := nullMillis(row.sourceExpiresAt); sourceExpires != nil && nowMs != nil && *sourceExpires <= *nowMs {
		return context, &AuthError{StatusCode: 403, Code: "external_source_expired", Message: "来源系统已过期"}
	}
	if row.tokenStatus != "active" {
		return context, &AuthError{StatusCode: 401, Code: "external_source_token_unavailable", Message: "来源系统 token 不可用"}
	}
	if tokenExpires := nullMillis(row.tokenExpiresAt); tokenExpires != nil && nowMs != nil && *tokenExpires <= *nowMs {
		return context, &AuthError{StatusCode: 401, Code: "external_source_token_unavailable", Message: "来源系统 token 不可用"}
	}
	if requiredScope != "" && !containsString(granted, requiredScope) {
		return context, &AuthError{StatusCode: 403, Code: "external_source_scope_forbidden", Message: "来源系统没有调用该接口的权限"}
	}
	return context, nil
}

// touchLastUsed mirrors touchExternalIntegrationSourceLastUsed*: both rows are
// updated at most once per 60s; failures never fail the request (Node swallows
// the background touch errors too).
func (d *Deps) touchLastUsed(ctx context.Context, row *tokenAuthRow, nowText string) {
	nowMs := rfc3339Millis(nowText)
	if nowMs == nil {
		return
	}
	if shouldTouchLastUsed(row.tokenLastUsedAt, nowMs) {
		_, _ = d.db().ExecContext(ctx, d.bind(`UPDATE `+d.table("external_integration_source_tokens")+`
			SET last_used_at = ?, updated_at = ? WHERE id = ?`), nowText, nowText, row.tokenID)
	}
	if shouldTouchLastUsed(row.sourceLastUsedAt, nowMs) {
		_, _ = d.db().ExecContext(ctx, d.bind(`UPDATE `+d.table("external_integration_sources")+`
			SET last_used_at = ?, updated_at = ? WHERE id = ?`), nowText, nowText, row.sourceRowID)
	}
}

func shouldTouchLastUsed(previous sql.NullString, nowMs *int64) bool {
	if nowMs == nil {
		return false
	}
	if !previous.Valid {
		return true
	}
	previousMs := rfc3339Millis(previous.String)
	if previousMs == nil {
		return false
	}
	return *nowMs-*previousMs >= touchLastUsedIntervalMs
}

// decodeScopesList mirrors decodeScopes for stored scope JSON arrays: parse,
// keep strings, trim, drop blanks/unknowns, dedupe + sort.
func decodeScopesList(value string) []string {
	var parsed any
	if err := jsonUnmarshal([]byte(value), &parsed); err != nil {
		return []string{}
	}
	items, isArray := parsed.([]any)
	if !isArray {
		return []string{}
	}
	seen := map[string]bool{}
	out := []string{}
	for _, item := range items {
		text, isString := item.(string)
		if !isString {
			continue
		}
		trimmed := strings.TrimSpace(text)
		if trimmed == "" || !scopeSupported(trimmed) || seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		out = append(out, trimmed)
	}
	sortStrings(out)
	return out
}

// decodeRateLimitsList mirrors decodeRateLimits for stored rate-limit JSON.
func decodeRateLimitsList(value string) []RateLimitRule {
	if strings.TrimSpace(value) == "" {
		return []RateLimitRule{}
	}
	var parsed any
	if err := jsonUnmarshal([]byte(value), &parsed); err != nil {
		return []RateLimitRule{}
	}
	items, isArray := parsed.([]any)
	if !isArray {
		return []RateLimitRule{}
	}
	seen := map[int]bool{}
	out := []RateLimitRule{}
	for _, item := range items {
		record, isObject := item.(map[string]any)
		if !isObject {
			continue
		}
		window, windowOK := numberToInt(record["windowSeconds"])
		maxRequests, maxOK := numberToInt(record["maxRequests"])
		if !windowOK || !maxOK || window < 1 || window > 86400 || maxRequests < 1 || maxRequests > 100000 {
			continue
		}
		if seen[window] {
			continue
		}
		seen[window] = true
		out = append(out, RateLimitRule{WindowSeconds: window, MaxRequests: maxRequests})
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].WindowSeconds < out[j-1].WindowSeconds; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Auth context plumbing (getExternalIntegrationSourceContext).
// ---------------------------------------------------------------------------

type authContextKey struct{}

func withAuthContext(ctx context.Context, authContext *AuthContext) context.Context {
	return context.WithValue(ctx, authContextKey{}, authContext)
}

// AuthContextFrom mirrors getExternalIntegrationSourceContext (nil = missing).
func AuthContextFrom(r *http.Request) *AuthContext {
	value, _ := r.Context().Value(authContextKey{}).(*AuthContext)
	return value
}

// ---------------------------------------------------------------------------
// Small shared helpers (dual-mode SQL plumbing mirrors the other slices).
// ---------------------------------------------------------------------------

func (d *Deps) clock() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

func (d *Deps) nowISO() string {
	return d.clock().UTC().Format(time.RFC3339Nano)
}
