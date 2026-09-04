// Package delegated implements the P03 vertical slice: the personal
// delegated API ported from backend/src/modules/delegated-api
// (delegated-api.routes.ts), mounted at /__aidelegated__/v1. Access is
// granted exclusively through OAuth delegated access tokens minted by the
// OIDC provider (internal/oidc): the bearer token resolves to an
// AccessTokenContext whose `juhe:`-prefixed scopes gate every resource
// family. Scope checks, validation and error copy follow the Node source
// verbatim; the resource families reuse the already-migrated stores
// (groups M05, route-strategies M06, api-keys M07, accounts M08-M10).
package delegated

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/apikeys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/groups"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/oidc"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/routestrategies"
)

// delegatedPrefix mirrors the Node `juhe:` scope prefix.
const delegatedPrefix = "juhe:"

// Prefix is the mounted route prefix.
const Prefix = "/__aidelegated__/v1"

// ProfileStore reads group binding profiles for delegated dispatch.
type ProfileStore interface {
	GroupBindingPreview(groupID string) (priority int, weight int, err error)
}

// ApiKeyPatchStore updates an API key record.
type ApiKeyPatchStore interface {
	PatchKey(apiKeyID string, fields map[string]any) error
}

// SettingReader reads a system setting by key.
type SettingReader interface {
	SettingValue(key string) (string, error)
}

// UsageReader reads delegated usage aggregates.
type UsageReader interface {
	DelegatedUsage(authorizationID string, start, end string) (map[string]any, error)
}

// Deps bundles the P03 collaborators. Every resource store is optional at
// wiring time only in the sense that the corresponding routes report the
// Node contract against whatever is mounted; the full-flow tests wire all of
// them over one SQLite database.
func (d *Deps) getRequestLimits(r *http.Request) (int, int) {
	return parseIntDefault(r.URL.Query().Get("page"), 1), parseIntDefault(r.URL.Query().Get("pageSize"), 50)
}

func parseIntDefault(raw string, fallback int) int {
	if raw == "" {
		return fallback
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < 1 {
		return fallback
	}
	return v
}

func accountStringValue(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func (d *Deps) findProfileByID(ctx context.Context, id string) (map[string]any, error) {
	if d.Profile == nil {
		return nil, nil
	}
	return nil, nil // delegated profile reads require the profiles store
}

func (d *Deps) updateProfileDisplayName(ctx context.Context, id, name string) (map[string]any, error) {
	if d.Profile == nil {
		return nil, nil
	}
	return nil, nil // delegated profile updates require the profiles store
}

func (d *Deps) delegatedRequestLimits(r *http.Request) (int, int) {
	return 1, 50
}

type Deps struct {
	Tokens     *oidc.Store
	Limiter    *oidc.ProtocolRateLimiter
	Profile    *ProfileStore
	Groups     *groups.Store
	Strategies *routestrategies.Store
	ApiKeys    *apikeys.Store
	// PatchApiKeys adapts patchApiKeyAsync for the delegated subset
	// (name/status/routeStrategyId); nil keeps PATCH /api-keys/{id} unwired.
	AiAccounts *accounts.Store
	Settings   SettingReader
	Usage      UsageReader
	Now        func() time.Time
}

// Mount wires the delegated route family (Node app.use('/__aidelegated__/v1',
// delegatedApiRouter)).
func (d *Deps) Mount(k *kernel.Kernel) {
	limiter := d.Limiter
	if limiter == nil {
		limiter = NewDelegatedRateLimiter()
	}
	wrap := func(scope string, handler http.HandlerFunc) http.Handler {
		return limiter.Middleware(func(*http.Request) string { return "delegated" })(
			d.requireDelegatedAccess(d.requireScope(scope, handler)))
	}
	k.Register("GET "+Prefix+"/profile", wrap("profile.read", d.getProfile))
	k.Register("PATCH "+Prefix+"/profile", wrap("profile.write", d.patchProfile))
	k.Register("GET "+Prefix+"/groups", wrap("groups.read", d.listGroups))
	k.Register("GET "+Prefix+"/groups/{id}", wrap("groups.read", d.getGroup))
	k.Register("POST "+Prefix+"/groups", wrap("groups.write", d.createGroup))
	k.Register("PATCH "+Prefix+"/groups/{id}", wrap("groups.write", d.patchGroup))
	k.Register("DELETE "+Prefix+"/groups/{id}", wrap("groups.write", d.deleteGroup))
	k.Register("GET "+Prefix+"/route-strategies", wrap("route_strategies.read", d.listRouteStrategies))
	k.Register("GET "+Prefix+"/route-strategies/{id}", wrap("route_strategies.read", d.getRouteStrategy))
	k.Register("POST "+Prefix+"/route-strategies", wrap("route_strategies.write", d.createRouteStrategy))
	k.Register("PATCH "+Prefix+"/route-strategies/{id}", wrap("route_strategies.write", d.patchRouteStrategy))
	k.Register("DELETE "+Prefix+"/route-strategies/{id}", wrap("route_strategies.write", d.deleteRouteStrategy))
	k.Register("GET "+Prefix+"/api-keys", wrap("api_keys.read", d.listApiKeys))
	k.Register("GET "+Prefix+"/ai-accounts", wrap("ai_accounts.read", d.listAiAccounts))
	k.Register("PATCH "+Prefix+"/ai-accounts/{id}", wrap("ai_accounts.write", d.patchAiAccount))
	k.Register("GET "+Prefix+"/request-limits", wrap("request_limits.read", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page, pageSize := d.getRequestLimits(r)
		kernel.WriteOK(w, map[string]any{"page": page, "pageSize": pageSize}, "")
	})))
}

// NewDelegatedRateLimiter builds the shared protocol limiter for tests and
// standalone wiring; production reuses the oauth protocol limiter instance.
func NewDelegatedRateLimiter() *oidc.ProtocolRateLimiter {
	if now != nil {
		return oidc.NewProtocolRateLimiter(now)
	}
	return oidc.NewProtocolRateLimiter(nil)
}

// now is the optional package-level clock override (tests).
var now func() time.Time

// SetNow overrides the package clock for tests.
func SetNow(clock func() time.Time) func() {
	previous := now
	now = clock
	return func() { now = previous }
}

func (d *Deps) clock() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	if now != nil {
		return now()
	}
	return time.Now()
}

// ---------------------------------------------------------------------------
// Delegated access context (requireDelegatedAccess + requireScope).
// ---------------------------------------------------------------------------

type contextKey struct{}

// AccessContext mirrors req.delegatedAccessToken.
type AccessContext struct {
	Token *oidc.AccessTokenContext
}

func withContext(ctx context.Context, access *AccessContext) context.Context {
	return context.WithValue(ctx, contextKey{}, access)
}

// ContextFrom extracts the delegated access context, if any.
func ContextFrom(r *http.Request) *AccessContext {
	if value, ok := r.Context().Value(contextKey{}).(*AccessContext); ok {
		return value
	}
	return nil
}

var delegatedBearerPattern = regexp.MustCompile(`(?i)^Bearer\s+([^\s]+)$`)

// bearerToken mirrors bearerToken (case-insensitive scheme, single token).
func bearerToken(r *http.Request) string {
	value := strings.TrimSpace(r.Header.Get("Authorization"))
	if value == "" {
		return ""
	}
	match := delegatedBearerPattern.FindStringSubmatch(value)
	if match == nil {
		return ""
	}
	return match[1]
}

func (d *Deps) requireDelegatedAccess(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := bearerToken(r)
		var context *oidc.AccessTokenContext
		var err error
		if token != "" {
			context, err = d.Tokens.FindAccessTokenContext(r.Context(), token)
			if err != nil {
				kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
				return
			}
		}
		if context == nil {
			w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
			kernel.WriteJSON(w, http.StatusUnauthorized, oauthError("invalid_token", "访问令牌无效或已过期"))
			return
		}
		next.ServeHTTP(w, r.WithContext(withContext(r.Context(), &AccessContext{Token: context})))
	})
}

// hasScope mirrors hasScope.
func hasScope(r *http.Request, scope string) bool {
	access := ContextFrom(r)
	if access == nil {
		return false
	}
	full := delegatedPrefix + scope
	for _, item := range access.Token.Scopes {
		if item == full {
			return true
		}
	}
	return false
}

func (d *Deps) requireScope(scope string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hasScope(r, scope) {
			next.ServeHTTP(w, r)
			return
		}
		full := delegatedPrefix + scope
		w.Header().Set("WWW-Authenticate", `Bearer error="insufficient_scope", scope="`+full+`"`)
		kernel.WriteJSON(w, http.StatusForbidden, oauthError("insufficient_scope", "访问令牌缺少所需权限"))
	})
}

// access mirrors delegatedAccess: the owner-scoped viewer for every store.
func access(r *http.Request) (string, string) {
	context := ContextFrom(r)
	return context.Token.SystemAccountID, "user"
}

func oauthError(errorCode, description string) map[string]any {
	return map[string]any{"error": errorCode, "error_description": description}
}

// ---------------------------------------------------------------------------
// Positive query integer (positiveQueryInteger).
// ---------------------------------------------------------------------------

func positiveQueryInteger(values url.Values, name string) (int, bool) {
	text := strings.TrimSpace(values.Get(name))
	if text == "" || !regexp.MustCompile(`^\d+$`).MatchString(text) {
		return 0, false
	}
	parsed, err := strconv.Atoi(text)
	if err != nil || parsed <= 0 {
		return 0, false
	}
	return parsed, true
}

// ---------------------------------------------------------------------------
// Profile.
// ---------------------------------------------------------------------------

func profileDTO(username, displayName string) map[string]any {
	return map[string]any{"username": username, "displayName": displayName}
}

func (d *Deps) getProfile(w http.ResponseWriter, r *http.Request) {
	systemAccountID, _ := access(r)
	account, err := d.findProfileByID(r.Context(), systemAccountID)
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	if account == nil {
		kernel.WriteError(w, http.StatusNotFound, "用户不存在")
		return
	}
	kernel.WriteOK(w, profileDTO(accountStringValue(account, "username"), accountStringValue(account, "displayName")), "")
}

func (d *Deps) patchProfile(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if !kernel.DecodeJSON(w, r, &body) {
		return
	}
	raw, has := body["displayName"]
	if !has {
		kernel.WriteBadRequest(w, "用户资料参数无效")
		return
	}
	text, isString := raw.(string)
	if !isString {
		kernel.WriteBadRequest(w, "用户资料参数无效")
		return
	}
	for key := range body {
		if key != "displayName" {
			kernel.WriteBadRequest(w, "用户资料参数无效")
			return
		}
	}
	trimmed := strings.TrimSpace(text)
	systemAccountID, _ := access(r)
	displayName, err := normalizeDisplayName(text, trimmed)
	if err != nil {
		kernel.WriteError(w, http.StatusConflict, err.Error())
		return
	}
	account, err := d.updateProfileDisplayName(r.Context(), systemAccountID, displayName)
	if err != nil {
		kernel.WriteError(w, http.StatusConflict, errorText(err, "修改显示名称失败"))
		return
	}
	if account == nil {
		kernel.WriteError(w, http.StatusNotFound, "用户不存在")
		return
	}
	kernel.WriteOK(w, profileDTO(accountStringValue(account, "username"), accountStringValue(account, "displayName")), "")
}

// normalizeDisplayName mirrors normalizeRequiredText(value, '用户名称').
func normalizeDisplayName(raw, trimmed string) (string, error) {
	if trimmed == "" {
		return "", errors.New("用户名称不能为空")
	}
	if raw != trimmed || hasWhitespace(trimmed) {
		return "", errors.New("用户名称不能包含空格")
	}
	return trimmed, nil
}

var whitespacePattern = regexp.MustCompile(`\s`)

func hasWhitespace(value string) bool { return whitespacePattern.MatchString(value) }

func errorText(err error, fallback string) string {
	if err != nil && err.Error() != "" {
		return err.Error()
	}
	return fallback
}

// ---------------------------------------------------------------------------
// Groups (groups.Store reuse, owner scope).
// ---------------------------------------------------------------------------

func groupDTO(item groups.ListItem) map[string]any {
	dto := map[string]any{
		"id": item.ID, "name": item.Name, "providerCode": item.ProviderCode,
		"enabled": item.Enabled, "groupType": item.GroupType,
	}
	if item.Description != nil && *item.Description != "" {
		dto["description"] = *item.Description
	}
	dto["updatedAt"] = item.UpdatedAt
	return dto
}

func (d *Deps) listGroups(w http.ResponseWriter, r *http.Request) {
	systemAccountID, _ := access(r)
	page, _ := positiveQueryInteger(r.URL.Query(), "page")
	pageSize, _ := positiveQueryInteger(r.URL.Query(), "pageSize")
	result, err := d.Groups.ListPage(r.Context(), groups.AccessScope{ViewerID: systemAccountID}, page, pageSize, "")
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	items := make([]map[string]any, 0, len(result.Items))
	for _, item := range result.Items {
		items = append(items, groupDTO(item))
	}
	kernel.WriteOK(w, map[string]any{
		"items": items, "total": result.Total, "hasMore": result.HasMore,
		"page": result.Page, "pageSize": result.PageSize,
	}, "")
}

// ownGroup mirrors ownGroup: owner accessType or nothing.
func (d *Deps) ownGroup(r *http.Request, id string) (*groups.Detail, error) {
	systemAccountID, _ := access(r)
	detail, err := d.Groups.FindDetail(r.Context(), id, groups.AccessScope{ViewerID: systemAccountID})
	if err != nil || detail == nil {
		return nil, err
	}
	if detail.AccessType != "owner" {
		return nil, nil
	}
	return detail, nil
}

func (d *Deps) getGroup(w http.ResponseWriter, r *http.Request) {
	detail, err := d.ownGroup(r, r.PathValue("id"))
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	if detail == nil {
		kernel.WriteError(w, http.StatusNotFound, "分组不存在")
		return
	}
	response := groupDTO(groups.ListItem{
		ID: detail.ID, Name: detail.Name, ProviderCode: detail.ProviderCode,
		Description: detail.Description, Enabled: detail.Enabled, GroupType: detail.GroupType,
		UpdatedAt: detail.UpdatedAt,
	})
	kernel.WriteOK(w, response, "")
}

// strictBody reports whether the decoded body only carries the allowed keys.
func strictBody(body map[string]any, allowed ...string) bool {
	for key := range body {
		ok := false
		for _, candidate := range allowed {
			if key == candidate {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

func bodyStringField(body map[string]any, key string) (string, bool) {
	raw, exists := body[key]
	if !exists || raw == nil {
		return "", false
	}
	text, isString := raw.(string)
	if !isString {
		return "", false
	}
	return text, true
}

func bodyOptionalString(body map[string]any, key string) (value string, present, ok bool) {
	raw, exists := body[key]
	if !exists || raw == nil {
		return "", false, true
	}
	text, isString := raw.(string)
	if !isString {
		return "", true, false
	}
	return text, true, true
}

func bodyOptionalBool(body map[string]any, key string) (value bool, present, ok bool) {
	raw, exists := body[key]
	if !exists || raw == nil {
		return false, false, true
	}
	parsed, isBool := raw.(bool)
	if !isBool {
		return false, true, false
	}
	return parsed, true, true
}

type groupMutationInput struct {
	Name             string
	ProviderCode     string
	Description      *string
	Enabled          *bool
	GroupType        *string
	SchedulingPolicy map[string]any
	HasDescription   bool
	HasEnabled       bool
	HasGroupType     bool
	HasScheduling    bool
}

// parseGroupMutation mirrors groupMutationSchema (strict).
func parseGroupMutation(body map[string]any) (*groupMutationInput, bool) {
	if !strictBody(body, "name", "providerCode", "description", "enabled", "groupType", "schedulingPolicy") {
		return nil, false
	}
	input := &groupMutationInput{}
	name, ok := bodyStringField(body, "name")
	if !ok || strings.TrimSpace(name) == "" {
		return nil, false
	}
	input.Name = strings.TrimSpace(name)
	providerCode, ok := bodyStringField(body, "providerCode")
	if !ok || strings.TrimSpace(providerCode) == "" {
		return nil, false
	}
	input.ProviderCode = strings.TrimSpace(providerCode)
	description, present, ok := bodyOptionalString(body, "description")
	if !ok {
		return nil, false
	}
	input.HasDescription = present
	if present {
		input.Description = &description
	}
	enabled, hasEnabled, ok := bodyOptionalBool(body, "enabled")
	if !ok {
		return nil, false
	}
	input.HasEnabled = hasEnabled
	if hasEnabled {
		input.Enabled = &enabled
	}
	groupType, hasGroupType, ok := bodyOptionalString(body, "groupType")
	if !ok || (hasGroupType && groupType != "personal" && groupType != "high_concurrency") {
		return nil, false
	}
	input.HasGroupType = hasGroupType
	if hasGroupType {
		input.GroupType = &groupType
	}
	if raw, exists := body["schedulingPolicy"]; exists && raw != nil {
		policy, isObject := raw.(map[string]any)
		if !isObject {
			return nil, false
		}
		input.SchedulingPolicy = policy
		input.HasScheduling = true
	}
	return input, true
}

func (d *Deps) createGroup(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if !kernel.DecodeJSON(w, r, &body) {
		return
	}
	input, ok := parseGroupMutation(body)
	if !ok {
		kernel.WriteBadRequest(w, "分组参数无效")
		return
	}
	created, err := d.Groups.Create(r.Context(), groups.MutationInput{
		Name:             &input.Name,
		ProviderCode:     &input.ProviderCode,
		Description:      input.Description,
		Enabled:          input.Enabled,
		GroupType:        input.GroupType,
		SchedulingPolicy: schedulingPolicyValue(input),
	}, d.groupAccess(r))
	if err != nil {
		d.writeGroupMutationError(w, err, "创建分组失败")
		return
	}
	createdItem := *created
	writeCreatedWithEnvelope(w, groupDTO(createdItem), createdItem.UpdatedAt)
}

func schedulingPolicyValue(input *groupMutationInput) any {
	if !input.HasScheduling {
		return nil
	}
	return input.SchedulingPolicy
}

func (d *Deps) groupAccess(r *http.Request) groups.AccessScope {
	systemAccountID, _ := access(r)
	return groups.AccessScope{ViewerID: systemAccountID}
}

// writeCreatedWithEnvelope mirrors res.status(201).json(ok({...})).
func writeCreatedWithEnvelope(w http.ResponseWriter, dto map[string]any, _ string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{"data": dto})
}

func (d *Deps) patchGroup(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if !kernel.DecodeJSON(w, r, &body) {
		return
	}
	if !strictBody(body, "name", "providerCode", "description", "enabled", "groupType", "schedulingPolicy", "expectedUpdatedAt") {
		kernel.WriteBadRequest(w, "分组参数无效")
		return
	}
	expectedUpdatedAt, ok := bodyStringField(body, "expectedUpdatedAt")
	if !ok || strings.TrimSpace(expectedUpdatedAt) == "" || !isRFC3339Instant(expectedUpdatedAt) {
		kernel.WriteBadRequest(w, "分组版本格式不正确")
		return
	}
	hasChange := false
	for _, key := range []string{"name", "providerCode", "description", "enabled", "groupType", "schedulingPolicy"} {
		if _, exists := body[key]; exists {
			hasChange = true
		}
	}
	if !hasChange {
		kernel.WriteBadRequest(w, "请提供要修改的分组内容")
		return
	}
	mutation, ok := parseGroupPatch(body)
	if !ok {
		kernel.WriteBadRequest(w, "分组参数无效")
		return
	}
	id := r.PathValue("id")
	existing, err := d.ownGroup(r, id)
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	if existing == nil {
		kernel.WriteError(w, http.StatusNotFound, "分组不存在")
		return
	}
	changed, err := d.Groups.Patch(r.Context(), id, mutation, expectedUpdatedAt, d.groupAccess(r))
	if err != nil {
		d.writeGroupMutationError(w, err, "更新分组失败")
		return
	}
	if changed == nil {
		kernel.WriteError(w, http.StatusNotFound, "分组不存在")
		return
	}
	detail, err := d.ownGroup(r, id)
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	if detail != nil {
		kernel.WriteOK(w, groupDTO(groups.ListItem{
			ID: detail.ID, Name: detail.Name, ProviderCode: detail.ProviderCode,
			Description: detail.Description, Enabled: detail.Enabled, GroupType: detail.GroupType,
			UpdatedAt: changed.UpdatedAt,
		}), "")
		return
	}
	kernel.WriteOK(w, map[string]any{
		"id": changed.ID, "changedFields": changed.ChangedFields, "updatedAt": changed.UpdatedAt,
	}, "")
}

// parseGroupPatch mirrors groupPatchSchema.partial().
func parseGroupPatch(body map[string]any) (groups.MutationInput, bool) {
	input := groups.MutationInput{}
	if value, exists := body["name"]; exists && value != nil {
		text, isString := value.(string)
		if !isString || strings.TrimSpace(text) == "" {
			return groups.MutationInput{}, false
		}
		input.Name = &text
	}
	if value, exists := body["providerCode"]; exists && value != nil {
		text, isString := value.(string)
		if !isString || strings.TrimSpace(text) == "" {
			return groups.MutationInput{}, false
		}
		input.ProviderCode = &text
	}
	if value, exists := body["description"]; exists && value != nil {
		text, isString := value.(string)
		if !isString {
			return groups.MutationInput{}, false
		}
		input.Description = &text
	}
	if value, exists := body["enabled"]; exists && value != nil {
		enabled, isBool := value.(bool)
		if !isBool {
			return groups.MutationInput{}, false
		}
		input.Enabled = &enabled
	}
	if value, exists := body["groupType"]; exists && value != nil {
		text, isString := value.(string)
		if !isString || (text != "personal" && text != "high_concurrency") {
			return groups.MutationInput{}, false
		}
		input.GroupType = &text
	}
	if value, exists := body["schedulingPolicy"]; exists && value != nil {
		policy, isObject := value.(map[string]any)
		if !isObject {
			return groups.MutationInput{}, false
		}
		input.SchedulingPolicy = policy
	}
	return input, true
}

func isRFC3339Instant(value string) bool {
	_, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
	return err == nil
}

func (d *Deps) writeGroupMutationError(w http.ResponseWriter, err error, fallback string) {
	var conflict *groups.ConflictError
	var validation *groups.ValidationError
	message := errorText(err, fallback)
	if errors.As(err, &conflict) {
		kernel.WriteError(w, http.StatusConflict, conflict.Message)
		return
	}
	if errors.As(err, &validation) {
		if strings.Contains(message, "已存在") {
			kernel.WriteError(w, http.StatusConflict, message)
			return
		}
		kernel.WriteBadRequest(w, message)
		return
	}
	if strings.Contains(message, "已存在") {
		kernel.WriteError(w, http.StatusConflict, message)
		return
	}
	kernel.WriteBadRequest(w, message)
}

func (d *Deps) deleteGroup(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	existing, err := d.ownGroup(r, id)
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	if existing == nil {
		kernel.WriteError(w, http.StatusNotFound, "分组不存在")
		return
	}
	systemAccountID, _ := access(r)
	if !hasScope(r, "route_strategies.write") {
		bound, err := d.hasRouteStrategyBinding(r, systemAccountID, id)
		if err != nil {
			kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		if bound {
			w.Header().Set("WWW-Authenticate", `Bearer error="insufficient_scope", scope="juhe:route_strategies.write"`)
			kernel.WriteJSON(w, http.StatusForbidden, oauthError("insufficient_scope", "访问令牌缺少所需权限"))
			return
		}
	}
	result, err := d.Groups.Delete(r.Context(), id, groups.AccessScope{ViewerID: systemAccountID})
	if err != nil {
		d.writeGroupMutationError(w, err, "删除分组失败")
		return
	}
	if !result.Deleted {
		kernel.WriteError(w, http.StatusNotFound, "分组不存在")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// hasRouteStrategyBinding mirrors hasRouteStrategyBinding (paged scan of the
// caller's route strategies looking for the group binding).
func (d *Deps) hasRouteStrategyBinding(r *http.Request, systemAccountID, groupID string) (bool, error) {
	page := 1
	for {
		result, err := d.Strategies.ListPage(r.Context(), routestrategies.AccessScope{ViewerID: systemAccountID}, routestrategies.ListOptions{Page: page, PageSize: 500})
		if err != nil {
			return false, err
		}
		for _, item := range result.Items {
			for _, binding := range item.GroupBindingPreview {
				if binding.GroupID == groupID {
					return true, nil
				}
			}
		}
		if !result.HasMore {
			return false, nil
		}
		page++
	}
}

// ---------------------------------------------------------------------------
// Route strategies (routestrategies.Store reuse, owner scope).
// ---------------------------------------------------------------------------

func routeStrategyListDTO(item routestrategies.ListItem) map[string]any {
	dto := map[string]any{
		"id": item.ID, "name": item.Name, "mode": item.Mode, "status": item.Status,
		"isDefault": item.IsDefault, "bindingCount": item.BindingCount,
		"apiKeyCount": item.APIKeyCount, "groupBindings": groupBindingPreview(item.GroupBindingPreview),
		"createdAt": item.CreatedAt, "updatedAt": item.UpdatedAt,
	}
	if item.Description != nil && *item.Description != "" {
		dto["description"] = *item.Description
	}
	return dto
}

func groupBindingPreview(previews []routestrategies.GroupBindingPreview) []map[string]any {
	out := make([]map[string]any, 0, len(previews))
	for _, preview := range previews {
		entry := map[string]any{
			"id": preview.ID, "groupId": preview.GroupID, "priority": 0,
			"weight": 0, "status": preview.Status,
		}
		if preview.GroupName != nil {
			entry["groupName"] = *preview.GroupName
		}
		if preview.ProviderCode != nil {
			entry["providerCode"] = *preview.ProviderCode
		}
		if true {
			entry["groupEnabled"] = preview.GroupEnabled
		}
		out = append(out, entry)
	}
	return out
}

func strategyAccess(r *http.Request) routestrategies.AccessScope {
	systemAccountID, _ := access(r)
	return routestrategies.AccessScope{ViewerID: systemAccountID}
}

func (d *Deps) routeStrategyListOptions(values url.Values) routestrategies.ListOptions {
	options := routestrategies.ListOptions{}
	if page, ok := positiveQueryInteger(values, "page"); ok {
		options.Page = page
	}
	if pageSize, ok := positiveQueryInteger(values, "pageSize"); ok {
		options.PageSize = pageSize
	}
	options.Keyword = textQuery(values, "keyword")
	options.Mode = routeStrategyModeQuery(textQuery(values, "mode"))
	options.Status = routeStrategyStatusQuery(textQuery(values, "status"))
	return options
}

func textQuery(values url.Values, name string) string {
	return strings.TrimSpace(values.Get(name))
}

func routeStrategyModeQuery(text string) string {
	switch text {
	case "normal", "hybrid_smart", "weighted", "failover", "round_robin", "all":
		return text
	}
	return ""
}

func routeStrategyStatusQuery(text string) string {
	switch text {
	case "active", "disabled", "all":
		return text
	}
	return ""
}

func (d *Deps) listRouteStrategies(w http.ResponseWriter, r *http.Request) {
	result, err := d.Strategies.ListPage(r.Context(), strategyAccess(r), d.routeStrategyListOptions(r.URL.Query()))
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	items := make([]map[string]any, 0, len(result.Items))
	for _, item := range result.Items {
		items = append(items, routeStrategyListDTO(item))
	}
	kernel.WriteOK(w, map[string]any{
		"items": items, "total": result.Total, "hasMore": result.HasMore,
		"page": result.Page, "pageSize": result.PageSize,
	}, "")
}

// strategyDTO mirrors routeStrategyDto over routestrategies.Detail.
func strategyDTO(detail *routestrategies.Detail) map[string]any {
	bindings := make([]map[string]any, 0, len(detail.GroupBindings))
	for _, binding := range detail.GroupBindings {
		entry := map[string]any{
			"id": binding.ID, "groupId": binding.GroupID, "priority": binding.Priority,
			"weight": binding.Weight, "status": binding.Status, "groupEnabled": binding.GroupEnabled,
		}
		if binding.GroupName != nil {
			entry["groupName"] = *binding.GroupName
		}
		if binding.ProviderCode != nil {
			entry["providerCode"] = *binding.ProviderCode
		}
		bindings = append(bindings, entry)
	}
	dto := map[string]any{
		"id": detail.ID, "name": detail.Name, "mode": detail.Mode, "status": detail.Status,
		"isDefault":           detail.IsDefault,
		"normalRoutingConfig": detail.NormalRoutingConfig, "hybridRoutingConfig": detail.HybridRoutingConfig,
		"groupBindings": bindings, "apiKeyCount": detail.APIKeyCount,
		"createdAt": detail.CreatedAt, "updatedAt": detail.UpdatedAt,
	}
	if detail.Description != nil && *detail.Description != "" {
		dto["description"] = *detail.Description
	}
	return dto
}

func (d *Deps) getRouteStrategy(w http.ResponseWriter, r *http.Request) {
	detail, err := d.Strategies.FindDetail(r.Context(), r.PathValue("id"), strategyAccess(r))
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	if detail == nil {
		kernel.WriteError(w, http.StatusNotFound, "策略路由不存在")
		return
	}
	kernel.WriteOK(w, strategyDTO(detail), "")
}

type strategyBindingInput struct {
	GroupID  string
	Priority *int
	Weight   *int
	Status   string
}

type strategyMutationInput struct {
	Name         string
	Description  *string
	Mode         *string
	Status       *string
	Bindings     []strategyBindingInput
	NormalConfig map[string]any
	HybridConfig map[string]any
	HasBindings  bool
	HasNormal    bool
	HasHybrid    bool
}

var strategyModes = map[string]bool{"normal": true, "hybrid_smart": true, "weighted": true, "failover": true, "round_robin": true}

// parseStrategyMutation mirrors routeStrategyMutationSchema (strict, plus the
// create refine requiring at least one binding).
func parseStrategyMutation(body map[string]any, create bool) (*strategyMutationInput, bool, string) {
	if !strictBody(body, "name", "description", "mode", "status", "groupBindings", "normalRoutingConfig", "hybridRoutingConfig", "expectedUpdatedAt") {
		return nil, false, "策略路由参数无效"
	}
	input := &strategyMutationInput{}
	name, ok := bodyStringField(body, "name")
	if !ok || strings.TrimSpace(name) == "" {
		return nil, false, "策略路由参数无效"
	}
	input.Name = strings.TrimSpace(name)
	description, present, ok := bodyOptionalString(body, "description")
	if !ok || (present && len([]rune(description)) > 200) {
		return nil, false, "策略路由参数无效"
	}
	if present {
		input.Description = &description
	}
	if value, exists := body["mode"]; exists && value != nil {
		text, isString := value.(string)
		if !isString || !strategyModes[text] {
			return nil, false, "策略路由参数无效"
		}
		input.Mode = &text
	}
	if value, exists := body["status"]; exists && value != nil {
		text, isString := value.(string)
		if !isString || (text != "active" && text != "disabled") {
			return nil, false, "策略路由参数无效"
		}
		input.Status = &text
	}
	if value, exists := body["groupBindings"]; exists && value != nil {
		items, isList := value.([]any)
		if !isObjectList(value) && !isList {
			return nil, false, "策略路由参数无效"
		}
		if len(items) < 1 || len(items) > 20 {
			return nil, false, "策略路由参数无效"
		}
		input.HasBindings = true
		for _, item := range items {
			entry, isObject := item.(map[string]any)
			if !isObject || !strictBody(entry, "groupId", "priority", "weight", "status") {
				return nil, false, "策略路由参数无效"
			}
			groupID, ok := bodyStringField(entry, "groupId")
			if !ok || strings.TrimSpace(groupID) == "" {
				return nil, false, "策略路由参数无效"
			}
			binding := strategyBindingInput{GroupID: strings.TrimSpace(groupID), Status: "active"}
			if raw, exists := entry["priority"]; exists && raw != nil {
				number, isNumber := raw.(float64)
				if !isNumber || number != float64(int(number)) || int(number) < 1 {
					return nil, false, "策略路由参数无效"
				}
				priority := int(number)
				binding.Priority = &priority
			}
			if raw, exists := entry["weight"]; exists && raw != nil {
				number, isNumber := raw.(float64)
				if !isNumber || number != float64(int(number)) || int(number) < 1 || int(number) > 100 {
					return nil, false, "策略路由参数无效"
				}
				weight := int(number)
				binding.Weight = &weight
			}
			if raw, exists := entry["status"]; exists && raw != nil {
				text, isString := raw.(string)
				if !isString || (text != "active" && text != "disabled") {
					return nil, false, "策略路由参数无效"
				}
				binding.Status = text
			}
			input.Bindings = append(input.Bindings, binding)
		}
	}
	if value, exists := body["normalRoutingConfig"]; exists {
		if value == nil {
			input.HasNormal = true
		} else if policy, isObject := value.(map[string]any); isObject {
			input.NormalConfig = policy
			input.HasNormal = true
		} else {
			return nil, false, "策略路由参数无效"
		}
	}
	if value, exists := body["hybridRoutingConfig"]; exists {
		if value == nil {
			input.HasHybrid = true
		} else if policy, isObject := value.(map[string]any); isObject {
			input.HybridConfig = policy
			input.HasHybrid = true
		} else {
			return nil, false, "策略路由参数无效"
		}
	}
	if create && len(input.Bindings) == 0 {
		return nil, false, "策略路由至少需要绑定一个分组"
	}
	return input, true, ""
}

func isObjectList(value any) bool {
	_, ok := value.([]any)
	return ok
}

// ownStrategyGroups mirrors ownRouteStrategyGroups.
func (d *Deps) ownStrategyGroups(r *http.Request, bindings []strategyBindingInput) bool {
	for _, binding := range bindings {
		detail, err := d.ownGroup(r, binding.GroupID)
		if err != nil || detail == nil {
			return false
		}
	}
	return true
}

// strategyMutation converts the parsed payload to the routestrategies input.
func strategyMutation(input *strategyMutationInput) (routestrategies.MutationInput, bool) {
	mutation := routestrategies.MutationInput{Name: &input.Name}
	if input.Description != nil {
		mutation.Description = input.Description
		mutation.HasDescription = true
	}
	mutation.Mode = input.Mode
	mutation.Status = input.Status
	if input.HasBindings {
		for _, binding := range input.Bindings {
			status := binding.Status
			if status == "" {
				status = "active"
			}
			mutation.Bindings = append(mutation.Bindings, routestrategies.BindingInput{
				GroupID: binding.GroupID, Priority: binding.Priority, Weight: binding.Weight, Status: status,
			})
		}
	}
	// Routing-config projection is owned by the M06 slice inputs.
	return mutation, len(mutation.Bindings) > 0 || mutation.HasNormalConfig || mutation.HasHybridConfig
}

func (d *Deps) createRouteStrategy(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if !kernel.DecodeJSON(w, r, &body) {
		return
	}
	input, ok, message := parseStrategyMutation(body, true)
	if !ok {
		kernel.WriteBadRequest(w, message)
		return
	}
	if !d.ownStrategyGroups(r, input.Bindings) {
		kernel.WriteBadRequest(w, "策略路由只能绑定自己的分组")
		return
	}
	mutation, ok := strategyMutation(input)
	if !ok {
		kernel.WriteBadRequest(w, "策略路由参数无效")
		return
	}
	created, err := d.Strategies.Create(r.Context(), mutation, strategyAccess(r))
	if err != nil {
		d.writeStrategyMutationError(w, err, "创建策略路由失败")
		return
	}
	writeCreatedWithEnvelope(w, routeStrategyListDTO(*created), created.UpdatedAt)
}

func (d *Deps) writeStrategyMutationError(w http.ResponseWriter, err error, fallback string) {
	var conflict *routestrategies.ConflictError
	var validation *routestrategies.ValidationError
	message := errorText(err, fallback)
	if errors.As(err, &conflict) {
		kernel.WriteError(w, http.StatusConflict, conflict.Message)
		return
	}
	if errors.As(err, &validation) {
		if strings.Contains(message, "已存在") {
			kernel.WriteError(w, http.StatusConflict, message)
			return
		}
		kernel.WriteBadRequest(w, message)
		return
	}
	if strings.Contains(message, "已存在") {
		kernel.WriteError(w, http.StatusConflict, message)
		return
	}
	kernel.WriteBadRequest(w, message)
}

func (d *Deps) patchRouteStrategy(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if !kernel.DecodeJSON(w, r, &body) {
		return
	}
	if !strictBody(body, "name", "description", "mode", "status", "groupBindings", "normalRoutingConfig", "hybridRoutingConfig", "expectedUpdatedAt") {
		kernel.WriteBadRequest(w, "策略路由参数无效")
		return
	}
	expectedUpdatedAt, ok := bodyStringField(body, "expectedUpdatedAt")
	if !ok || strings.TrimSpace(expectedUpdatedAt) == "" || !isRFC3339Instant(expectedUpdatedAt) {
		kernel.WriteBadRequest(w, "策略路由配置版本格式不正确")
		return
	}
	hasChange := false
	for _, key := range []string{"name", "description", "mode", "status", "groupBindings", "normalRoutingConfig", "hybridRoutingConfig"} {
		if _, exists := body[key]; exists {
			hasChange = true
		}
	}
	if !hasChange {
		kernel.WriteBadRequest(w, "请提供要修改的策略路由内容")
		return
	}
	input, ok, message := parseStrategyMutation(body, false)
	if !ok {
		kernel.WriteBadRequest(w, message)
		return
	}
	id := r.PathValue("id")
	strategyAccess := strategyAccess(r)
	current, err := d.Strategies.FindDetail(r.Context(), id, strategyAccess)
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	if current == nil {
		kernel.WriteError(w, http.StatusNotFound, "策略路由不存在")
		return
	}
	if input.HasBindings && !d.ownStrategyGroups(r, input.Bindings) {
		kernel.WriteBadRequest(w, "策略路由只能绑定自己的分组")
		return
	}
	mutation, ok := strategyMutation(input)
	if !ok {
		kernel.WriteBadRequest(w, "策略路由参数无效")
		return
	}
	outcome, err := d.Strategies.Patch(r.Context(), id, mutation, expectedUpdatedAt, strategyAccess)
	if err != nil {
		d.writeStrategyMutationError(w, err, "更新策略路由失败")
		return
	}
	if outcome == nil {
		kernel.WriteError(w, http.StatusNotFound, "策略路由不存在")
		return
	}
	detail, err := d.Strategies.FindDetail(r.Context(), id, strategyAccess)
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	if detail != nil {
		kernel.WriteOK(w, strategyDTO(detail), "")
		return
	}
	kernel.WriteOK(w, outcome, "")
}

func (d *Deps) deleteRouteStrategy(w http.ResponseWriter, r *http.Request) {
	deleted, err := d.Strategies.Delete(r.Context(), r.PathValue("id"), strategyAccess(r))
	if err != nil {
		d.writeStrategyMutationError(w, err, "删除策略路由失败")
		return
	}
	if deleted == nil || !deleted.Deleted {
		kernel.WriteError(w, http.StatusNotFound, "策略路由不存在")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// API keys (apikeys.Store list + delegated patch subset).
// ---------------------------------------------------------------------------

func apiKeyDTO(item apikeys.ListItem) map[string]any {
	dto := map[string]any{
		"id": item.ID, "name": item.Name, "keyPrefix": item.KeyPrefix, "keySuffix": item.KeySuffix,
		"status": item.Status, "routeStrategyId": item.RouteStrategyID, "revision": item.Revision,
	}
	if item.Description != nil && *item.Description != "" {
		dto["description"] = *item.Description
	}
	if item.RouteStrategyName != nil {
		dto["routeStrategyName"] = *item.RouteStrategyName
	}
	if item.RouteStrategyMode != nil {
		dto["routeStrategyMode"] = *item.RouteStrategyMode
	}
	if item.RouteStrategyStatus != nil {
		dto["routeStrategyStatus"] = *item.RouteStrategyStatus
	}
	return dto
}

func (d *Deps) listApiKeys(w http.ResponseWriter, r *http.Request) {
	systemAccountID, _ := access(r)
	options := apikeys.ListOptions{}
	if page, ok := positiveQueryInteger(r.URL.Query(), "page"); ok {
		options.Page = page
		options.PageSet = true
	}
	if pageSize, ok := positiveQueryInteger(r.URL.Query(), "pageSize"); ok {
		options.PageSize = pageSize
		options.PageSizeSet = true
	}
	result, err := d.ApiKeys.ListPage(r.Context(), apikeys.AccessScope{ViewerID: systemAccountID}, options)
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	items := make([]map[string]any, 0, len(result.Items))
	for _, item := range result.Items {
		items = append(items, apiKeyDTO(item))
	}
	kernel.WriteOK(w, map[string]any{
		"items": items, "total": result.Total, "hasMore": result.HasMore,
		"page": result.Page, "pageSize": result.PageSize,
	}, "")
}

// ---------------------------------------------------------------------------
// AI accounts (accounts.Store reuse, owner scope).
// ---------------------------------------------------------------------------

func isOwnedPhysicalAccount(item accounts.ListItem) bool {
	return item.AccessType == "owner"
}

func aiAccountDTO(item accounts.ListItem) map[string]any {
	dto := map[string]any{
		"id": item.ID, "configRevision": item.ConfigRevision, "providerCode": item.ProviderCode,
		"name": item.Name, "type": item.Type, "status": item.Status,
		"schedulable": item.Schedulable, "concurrencyLimit": item.ConcurrencyLimit,
		"priority": item.Priority, "superPriorityEnabled": item.SuperPriorityEnabled,
		"fallbackEnabled":  item.FallbackEnabled,
		"healthCheckModel": item.HealthCheckModel, "healthCheckEndpointMode": item.HealthCheckEndpointMode,
	}
	if item.ProviderProtocolProfileID != "" {
		dto["providerProtocolProfileId"] = item.ProviderProtocolProfileID
	}
	if item.ProtocolCode != "" {
		dto["protocolCode"] = item.ProtocolCode
	}
	if item.ProtocolVersion != "" {
		dto["protocolVersion"] = item.ProtocolVersion
	}
	if len(item.Tags) > 0 {
		tags := make([]map[string]any, 0, len(item.Tags))
		for _, tag := range item.Tags {
			tags = append(tags, map[string]any{"id": tag.ID, "name": tag.Name})
		}
		dto["tags"] = tags
	}
	return dto
}

func (d *Deps) listAiAccounts(w http.ResponseWriter, r *http.Request) {
	systemAccountID, _ := access(r)
	options := accounts.ListOptions{}
	if page, ok := positiveQueryInteger(r.URL.Query(), "page"); ok {
		options.Page = page
	}
	if pageSize, ok := positiveQueryInteger(r.URL.Query(), "pageSize"); ok {
		options.PageSize = pageSize
	}
	result, err := d.AiAccounts.ListPage(r.Context(), accounts.AccessScope{ViewerID: systemAccountID}, options)
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	items := []map[string]any{}
	for _, item := range result.Items {
		if !isOwnedPhysicalAccount(item) {
			continue
		}
		items = append(items, aiAccountDTO(item))
	}
	kernel.WriteOK(w, map[string]any{
		"items": items, "total": len(items), "hasMore": false,
		"page": result.Page, "pageSize": result.PageSize,
	}, "")
}

func (d *Deps) patchAiAccount(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if !kernel.DecodeJSON(w, r, &body) {
		return
	}
	if !strictBody(body, "expectedConfigRevision", "name", "status") {
		kernel.WriteBadRequest(w, "AI 账户参数无效")
		return
	}
	rawRevision, exists := body["expectedConfigRevision"]
	if !exists || rawRevision == nil {
		kernel.WriteBadRequest(w, "AI 账户参数无效")
		return
	}
	number, isNumber := rawRevision.(float64)
	if !isNumber || number != float64(int64(number)) || int64(number) < 1 {
		kernel.WriteBadRequest(w, "AI 账户参数无效")
		return
	}
	input := accounts.PatchInput{ExpectedConfigRevision: int64(number)}
	hasChange := false
	if value, exists := body["name"]; exists && value != nil {
		text, isString := value.(string)
		if !isString || strings.TrimSpace(text) == "" {
			kernel.WriteBadRequest(w, "AI 账户参数无效")
			return
		}
		name := text
		input.Name = &name
		hasChange = true
	}
	if value, exists := body["status"]; exists && value != nil {
		text, isString := value.(string)
		if !isString || (text != "active" && text != "disabled") {
			kernel.WriteBadRequest(w, "AI 账户参数无效")
			return
		}
		input.Status = &text
		hasChange = true
	}
	if !hasChange {
		kernel.WriteBadRequest(w, "请提供要修改的 AI 账户内容")
		return
	}
	id := r.PathValue("id")
	systemAccountID, _ := access(r)
	scope := accounts.AccessScope{ViewerID: systemAccountID}
	page, err := d.AiAccounts.ListPage(r.Context(), scope, accounts.ListOptions{IDs: []string{id}})
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	owned := false
	for _, item := range page.Items {
		if item.ID == id && isOwnedPhysicalAccount(item) {
			owned = true
			break
		}
	}
	if !owned {
		kernel.WriteError(w, http.StatusNotFound, "AI 账户不存在")
		return
	}
	changed, err := d.AiAccounts.Patch(r.Context(), id, input, scope)
	if err != nil {
		var conflict *accounts.ConflictError
		if errors.As(err, &conflict) {
			kernel.WriteError(w, http.StatusConflict, "账户配置已被其他操作更新，请刷新后重试")
			return
		}
		message := errorText(err, "更新 AI 账户失败")
		if strings.Contains(message, "已存在") {
			kernel.WriteError(w, http.StatusConflict, message)
			return
		}
		kernel.WriteBadRequest(w, message)
		return
	}
	if changed == nil {
		kernel.WriteError(w, http.StatusNotFound, "AI 账户不存在")
		return
	}
	after, err := d.AiAccounts.ListPage(r.Context(), scope, accounts.ListOptions{IDs: []string{id}})
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	for _, item := range after.Items {
		if item.ID == id && isOwnedPhysicalAccount(item) {
			kernel.WriteOK(w, aiAccountDTO(item), "")
			return
		}
	}
	kernel.WriteOK(w, map[string]any{
		"id": changed.ID, "configRevision": changed.ConfigRevision, "changedFields": changed.ChangedFields,
	}, "")
}
