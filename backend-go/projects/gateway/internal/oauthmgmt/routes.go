package oauthmgmt

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

// Deps bundles the M17 slice collaborators.
type Deps struct {
	Store *Store
	Auth  *authsys.Deps
	Sink  authsys.OperationLogSink
}

// Mount wires the four provider families: admin surfaces on
// /{provider}-oauth (requireAdmin) and the forceSelfAccessScope mirrors on
// /my-{provider}-oauth (Node mounts the same router at both prefixes; my-*
// pins the scope to the caller).
func (d *Deps) Mount(k *kernel.Kernel) {
	prefix := "/__aisys__/api"
	for _, plan := range providerPlans() {
		d.mountProvider(k, prefix, plan)
	}
}

func (d *Deps) mountProvider(k *kernel.Kernel, prefix string, plan providerPlan) {
	adminSlug := prefix + "/" + plan.slug + "-oauth"
	selfSlug := prefix + "/my-" + plan.slug + "-oauth"

	// Admin surface.
	k.Register("POST "+adminSlug+"/auth-url", d.Auth.RequireAdmin(d.authURL(plan, false)))
	if plan.capabilities {
		k.Register("GET "+adminSlug+"/capabilities", d.Auth.RequireAdmin(d.capabilities()))
	}
	k.Register("POST "+adminSlug+"/create-from-code", d.Auth.RequireAdmin(
		d.guarded(plan, plan.module+".create_from_code", 180*time.Second, false, d.createFromCode(plan))))
	k.Register("POST "+adminSlug+"/create-from-refresh-token", d.Auth.RequireAdmin(
		d.guarded(plan, plan.module+".create_from_refresh_token", 180*time.Second, false, d.createFromRefreshToken(plan))))
	k.Register("POST "+adminSlug+"/accounts/{id}/refresh-token", d.Auth.RequireAdmin(d.refreshToken(plan, false)))
	k.Register("POST "+adminSlug+"/accounts/{id}/reauthorize-from-code", d.Auth.RequireAdmin(d.reauthorizeFromCode(plan, false)))
	k.Register("POST "+adminSlug+"/accounts/{id}/reauthorize-from-refresh-token", d.Auth.RequireAdmin(d.reauthorizeFromRefreshToken(plan, false)))
	if plan.sso {
		k.Register("POST "+adminSlug+"/sso-to-oauth", d.Auth.RequireAdmin(
			d.guarded(plan, plan.module+".sso_to_oauth", 15*time.Minute, false, d.ssoToOAuth(plan))))
	}

	// Self surface (forceSelfAccessScope).
	k.Register("POST "+selfSlug+"/auth-url", d.Auth.RequireSession(true)(d.authURL(plan, true)))
	if plan.capabilities {
		k.Register("GET "+selfSlug+"/capabilities", d.Auth.RequireSession(true)(d.capabilities()))
	}
	k.Register("POST "+selfSlug+"/create-from-code", d.Auth.RequireSession(true)(
		d.guarded(plan, plan.module+".create_from_code", 180*time.Second, true, d.createFromCode(plan))))
	k.Register("POST "+selfSlug+"/create-from-refresh-token", d.Auth.RequireSession(true)(
		d.guarded(plan, plan.module+".create_from_refresh_token", 180*time.Second, true, d.createFromRefreshToken(plan))))
	k.Register("POST "+selfSlug+"/accounts/{id}/refresh-token", d.Auth.RequireSession(true)(d.refreshToken(plan, true)))
	k.Register("POST "+selfSlug+"/accounts/{id}/reauthorize-from-code", d.Auth.RequireSession(true)(d.reauthorizeFromCode(plan, true)))
	k.Register("POST "+selfSlug+"/accounts/{id}/reauthorize-from-refresh-token", d.Auth.RequireSession(true)(d.reauthorizeFromRefreshToken(plan, true)))
	if plan.sso {
		k.Register("POST "+selfSlug+"/sso-to-oauth", d.Auth.RequireSession(true)(
			d.guarded(plan, plan.module+".sso_to_oauth", 15*time.Minute, true, d.ssoToOAuth(plan))))
	}
}

// guarded wraps a mutation with the dedupe mutation guard (Node mutationGuard:
// operationKey + 180s/15min processing TTL + owner/session fingerprint).
func (d *Deps) guarded(plan providerPlan, operationKey string, ttl time.Duration, selfOnly bool, handle func(w http.ResponseWriter, r *http.Request, access AccessScope)) http.Handler {
	guard := kernel.MutationGuardMiddleware(kernel.MutationGuardOptions{
		OperationKey: operationKey,
		Actor:        actorResolver,
		Scope: func(r *http.Request) (any, error) {
			return strings.TrimSpace(r.URL.Query().Get("systemAccountId")), nil
		},
		Fingerprint: func(r *http.Request) (any, error) {
			body := kernel.ParsedBody(r)
			fingerprint := map[string]any{
				"owner": strings.TrimSpace(r.URL.Query().Get("systemAccountId")),
			}
			if value := kernel.TextField(kernel.BodyField(r, "sessionId")); value != "" {
				fingerprint["sessionId"] = value
			}
			if value := kernel.TextField(kernel.BodyField(r, "callbackUrl")); value != "" {
				fingerprint["callbackUrl"] = kernel.HashStableValue(value)
			}
			if value := kernel.TextField(kernel.BodyField(r, "refreshToken")); value != "" {
				fingerprint["refreshToken"] = kernel.HashStableValue(value)
			}
			if value := kernel.TextField(kernel.BodyField(r, "name")); value != "" {
				fingerprint["name"] = strings.TrimSpace(value)
			}
			if value := kernel.TextField(kernel.BodyField(r, "oauthType")); value != "" {
				fingerprint["oauthType"] = strings.TrimSpace(value)
			}
			if value := kernel.TextField(kernel.BodyField(r, "projectId")); value != "" {
				fingerprint["projectId"] = strings.TrimSpace(value)
			}
			if value := kernel.TextField(kernel.BodyField(r, "tierId")); value != "" {
				fingerprint["tierId"] = strings.TrimSpace(value)
			}
			if value := kernel.TextField(kernel.BodyField(r, "clientId")); value != "" {
				fingerprint["clientId"] = strings.TrimSpace(value)
			}
			if value := kernel.TextField(kernel.BodyField(r, "clientSecret")); value != "" {
				fingerprint["clientSecret"] = kernel.HashStableValue(value)
			}
			if value := kernel.TextField(kernel.BodyField(r, "providerProtocolProfileId")); value != "" {
				fingerprint["providerProtocolProfileId"] = strings.TrimSpace(value)
			}
			fingerprint["status"] = creationStatusValue(body, "status")
			if tokens := ssoFingerprintTokens(r); tokens != "" {
				fingerprint["ssoTokens"] = tokens
			}
			return fingerprint, nil
		},
		ProcessingTTL: ttl,
	})
	wrapped := guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := authsys.AuthContextFrom(r)
		if auth == nil {
			kernel.WriteError(w, http.StatusUnauthorized, "请先登录")
			return
		}
		if !scopeQueryOK(r) {
			kernel.WriteBadRequest(w, "系统账号 ID 不能为空")
			return
		}
		handle(w, r, scopeFor(r, selfOnly))
	}))
	return wrapped
}

// actorResolver mirrors the guard actor binding.
// ssoFingerprintTokens mirrors the grok sso-to-oauth fingerprint input:
// normalized, deduplicated, sorted SSO tokens hashed for the guard.
func ssoFingerprintTokens(r *http.Request) string {
	rawList, _ := kernel.BodyField(r, "ssoTokens").([]any)
	list := make([]string, 0, len(rawList))
	for _, item := range rawList {
		if text, ok := item.(string); ok {
			list = append(list, text)
		}
	}
	tokens := normalizeGrokSSOImportTokens(list, kernel.TextField(kernel.BodyField(r, "ssoToken")))
	if len(tokens) == 0 {
		return ""
	}
	sorted := append([]string{}, tokens...)
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && sorted[j] < sorted[j-1]; j-- {
			sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
		}
	}
	return kernel.HashStableValue(strings.Join(sorted, "\n"))
}

func actorResolver(r *http.Request) string {
	if auth := authsys.AuthContextFrom(r); auth != nil {
		return auth.SystemAccountID
	}
	return "anonymous"
}

// scopeFor mirrors getRequestAccessScope for the admin surface (systemAccountId
// filter, "all" dropped) and forceSelfAccessScope for the my-* surface.
func scopeFor(r *http.Request, selfOnly bool) AccessScope {
	auth := authsys.AuthContextFrom(r)
	if selfOnly {
		return AccessScope{ViewerID: auth.SystemAccountID}
	}
	filter := strings.TrimSpace(r.URL.Query().Get("systemAccountId"))
	if filter == "all" {
		filter = ""
	}
	return AccessScope{ViewerID: auth.SystemAccountID, IsAdmin: true, FilterID: filter}
}

// scopeQueryOK mirrors parseRequestScopeQuery: an explicit systemAccountId
// query value must survive trimming.
func scopeQueryOK(r *http.Request) bool {
	values := r.URL.Query()["systemAccountId"]
	if len(values) == 0 {
		return true
	}
	return strings.TrimSpace(values[0]) != ""
}

// --- body helpers -----------------------------------------------------------

// strictBody mirrors zod .strict(): unknown keys reject the payload.
func strictBody(body map[string]any, allowed []string) bool {
	permitted := map[string]bool{}
	for _, key := range allowed {
		permitted[key] = true
	}
	for key := range body {
		if !permitted[key] {
			return false
		}
	}
	return true
}

// bodyString renders a present string field; ok=false when absent or typed
// differently.
func bodyString(body map[string]any, key string) (string, bool) {
	value, exists := body[key]
	if !exists || value == nil {
		return "", false
	}
	text, ok := value.(string)
	if !ok {
		return "", false
	}
	return text, true
}

// requiredTrimmedString mirrors z.string().trim().min(1).
func requiredTrimmedString(body map[string]any, key string) (string, bool) {
	text, present := bodyString(body, key)
	if !present {
		return "", false
	}
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return "", false
	}
	return trimmed, true
}

// optionalTrimmedText mirrors the gemini optionalTrimmedTextSchema: blank
// strings count as absent.
func optionalTrimmedText(body map[string]any, key string) string {
	text, _ := bodyString(body, key)
	return strings.TrimSpace(text)
}

// bodyInt mirrors z.number().int().min(min); absent renders nil.
func bodyInt(body map[string]any, key string, min int) (*int, bool) {
	value, exists := body[key]
	if !exists || value == nil {
		return nil, true
	}
	number, ok := value.(float64)
	if !ok || number != float64(int64(number)) {
		return nil, false
	}
	converted := int(number)
	if converted < min {
		return nil, false
	}
	return &converted, true
}

// requiredRevision mirrors z.number().int().min(1) required fields.
func requiredRevision(body map[string]any, key string) (int64, bool) {
	value, exists := body[key]
	if !exists || value == nil {
		return 0, false
	}
	number, ok := value.(float64)
	if !ok || number != float64(int64(number)) || number < 1 {
		return 0, false
	}
	return int64(number), true
}

// creationStatusValue mirrors accountCreationStatusInput for fingerprints.
func creationStatusValue(body map[string]any, key string) string {
	if text, ok := bodyString(body, key); ok {
		switch strings.TrimSpace(text) {
		case "active", "disabled":
			return strings.TrimSpace(text)
		}
	}
	return "pending_test"
}

// creationStatusText mirrors accountCreationStatusInput.
func creationStatusText(value any) string {
	if text, ok := value.(string); ok {
		switch strings.TrimSpace(text) {
		case "active", "disabled":
			return strings.TrimSpace(text)
		}
	}
	return "pending_test"
}

// --- managed account field mapping ------------------------------------------

// managedFields are the shared managed-account body keys (managedAccountFields).
var managedFields = []string{
	"providerProtocolProfileId", "name", "groupId", "concurrencyLimit", "priority",
	"status", "superPriorityEnabled", "fallbackEnabled", "supportedModels",
	"healthCheckModel", "healthCheckEndpointMode", "temporaryUnavailableContinuousProbeEnabled",
	"modelMappings", "tags", "proxyProfileId", "accountExpiresAt", "availabilitySchedule",
	"credentialsPatch", "notes",
}

var healthCheckEndpointModes = map[string]bool{
	"chat_json": true, "chat_sse": true, "responses_json": true, "responses_sse": true,
	"messages_json": true, "messages_sse": true,
	"generate_content_json": true, "generate_content_sse": true,
}

// parseManagedFields mirrors the shared managedAccountFields schema subset the
// M08 create consumes (temporaryUnavailableContinuousProbeEnabled is accepted
// per the M08 route contract but not mapped: its persistence belongs to the
// probe slice).
func parseManagedFields(body map[string]any) (managedInput, bool) {
	input := managedInput{}
	if text, ok := requiredTrimmedString(body, "providerProtocolProfileId"); !ok {
		return input, false
	} else {
		input.ProviderProtocolProfileID = text
	}
	if text, present := bodyString(body, "name"); present {
		trimmed := strings.TrimSpace(text)
		if trimmed == "" {
			return input, false
		}
		input.Name = &trimmed
	}
	if text, present := bodyString(body, "groupId"); present && strings.TrimSpace(text) != "" {
		trimmed := strings.TrimSpace(text)
		input.GroupID = &trimmed
	}
	if limit, ok := bodyInt(body, "concurrencyLimit", 1); !ok {
		return input, false
	} else if limit != nil {
		input.ConcurrencyLimit = limit
	}
	if priority, ok := bodyInt(body, "priority", 0); !ok {
		return input, false
	} else if priority != nil {
		input.Priority = priority
	}
	if value, present := body["status"]; present && value != nil {
		text, ok := value.(string)
		if !ok {
			return input, false
		}
		status := strings.TrimSpace(text)
		if status != "active" && status != "pending_test" && status != "disabled" {
			return input, false
		}
		input.Status = creationStatusText(value)
	}
	if value, present := body["superPriorityEnabled"]; present && value != nil {
		flag, ok := value.(bool)
		if !ok {
			return input, false
		}
		input.SuperPriorityEnabled = &flag
	}
	if value, present := body["fallbackEnabled"]; present && value != nil {
		flag, ok := value.(bool)
		if !ok {
			return input, false
		}
		input.FallbackEnabled = &flag
	}
	if value, present := body["supportedModels"]; present && value != nil {
		models, ok := parseStringSlice(value, 500)
		if !ok || len(models) == 0 {
			return input, false
		}
		input.SupportedModels = models
	}
	if text, present := bodyString(body, "healthCheckModel"); present {
		if strings.TrimSpace(text) == "" {
			return input, false
		}
		trimmed := strings.TrimSpace(text)
		input.HealthCheckModel = &trimmed
	}
	if text, present := bodyString(body, "healthCheckEndpointMode"); present {
		if !healthCheckEndpointModes[strings.TrimSpace(text)] {
			return input, false
		}
		trimmed := strings.TrimSpace(text)
		input.HealthCheckEndpointMode = &trimmed
	}
	if value, present := body["temporaryUnavailableContinuousProbeEnabled"]; present && value != nil {
		if _, ok := value.(bool); !ok {
			return input, false
		}
	}
	if value, present := body["modelMappings"]; present && value != nil {
		list, ok := value.([]any)
		if !ok {
			return input, false
		}
		for _, item := range list {
			object, ok := item.(map[string]any)
			if !ok {
				return input, false
			}
			mapping := accounts.ModelMapping{
				SourceModel:            strings.TrimSpace(kernel.TextField(object["sourceModel"])),
				SourceEndpointFamily:   strings.TrimSpace(kernel.TextField(object["sourceEndpointFamily"])),
				UpstreamModel:          strings.TrimSpace(kernel.TextField(object["upstreamModel"])),
				UpstreamEndpointFamily: strings.TrimSpace(kernel.TextField(object["upstreamEndpointFamily"])),
			}
			if mapping.SourceModel == "" || mapping.UpstreamModel == "" ||
				mapping.SourceEndpointFamily == "" || mapping.UpstreamEndpointFamily == "" {
				return input, false
			}
			if enabled, ok := object["enabled"].(bool); ok {
				mapping.Enabled = &enabled
			}
			input.ModelMappings = append(input.ModelMappings, mapping)
		}
	}
	if value, present := body["tags"]; present && value != nil {
		tags, ok := parseStringSlice(value, 24)
		if !ok {
			return input, false
		}
		input.Tags = tags
	}
	if text, present := bodyString(body, "proxyProfileId"); present && strings.TrimSpace(text) != "" {
		trimmed := strings.TrimSpace(text)
		input.ProxyProfileID = &trimmed
	}
	if value, present := body["accountExpiresAt"]; present && value != nil {
		text, ok := value.(string)
		if !ok {
			return input, false
		}
		input.AccountExpiresAt = &text
	}
	if value, present := body["availabilitySchedule"]; present {
		input.AvailabilitySchedule = value
	}
	if text, present := bodyString(body, "notes"); present {
		input.Notes = &text
	}
	return input, true
}

// parseStringSlice mirrors z.array(z.string().trim()).max(cap).
func parseStringSlice(value any, cap int) ([]string, bool) {
	list, ok := value.([]any)
	if !ok {
		return nil, false
	}
	output := []string{}
	for _, item := range list {
		text, ok := item.(string)
		if !ok {
			return nil, false
		}
		trimmed := strings.TrimSpace(text)
		if trimmed == "" {
			return nil, false
		}
		if len(output) >= cap {
			return nil, false
		}
		output = append(output, trimmed)
	}
	return output, true
}

// managedInput is the parsed managed-account field bundle.
type managedInput struct {
	ProviderProtocolProfileID string
	Name                      *string
	GroupID                   *string
	ConcurrencyLimit          *int
	Priority                  *int
	Status                    string
	SuperPriorityEnabled      *bool
	FallbackEnabled           *bool
	SupportedModels           []string
	HealthCheckModel          *string
	HealthCheckEndpointMode   *string
	ModelMappings             []accounts.ModelMapping
	Tags                      []string
	ProxyProfileID            *string
	AccountExpiresAt          *string
	AvailabilitySchedule      any
	Notes                     *string
}

// tokenOutcome carries the exchanged token credentials and the preferred
// account-name fallback (email where the provider reports one).
type tokenOutcome struct {
	Credentials map[string]any
	Name        string
}

// --- route handlers ---------------------------------------------------------

func (d *Deps) authURL(plan providerPlan, selfOnly bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !scopeQueryOK(r) {
			kernel.WriteBadRequest(w, "系统账号 ID 不能为空")
			return
		}
		var body map[string]any
		if !kernel.DecodeJSON(w, r, &body) {
			return
		}
		if body == nil {
			body = map[string]any{}
		}
		if !strictBody(body, plan.authURLKeys) {
			kernel.WriteBadRequest(w, plan.label+" 授权链接参数无效")
			return
		}
		payload, err := plan.authURL(r.Context(), d.Store, body, scopeFor(r, selfOnly).ViewerID)
		if err != nil {
			var validation *ValidationError
			if errors.As(err, &validation) {
				kernel.WriteBadRequest(w, validation.Message)
				return
			}
			// Node rethrows to the 500 error handler for auth-url failures.
			kernel.WriteError(w, http.StatusInternalServerError, err.Error())
			return
		}
		kernel.WriteOK(w, payload, "")
	}
}

func (d *Deps) capabilities() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		kernel.WriteOK(w, geminiOAuthCapabilities(), "")
	}
}

func (d *Deps) createFromCode(plan providerPlan) func(w http.ResponseWriter, r *http.Request, access AccessScope) {
	return func(w http.ResponseWriter, r *http.Request, access AccessScope) {
		d.handleCreate(w, r, plan, access, plan.createCodeKeys, "授权码", func(body map[string]any) (*tokenOutcome, error) {
			return plan.exchangeCode(r.Context(), d.Store, body, access.ViewerID)
		})
	}
}

func (d *Deps) createFromRefreshToken(plan providerPlan) func(w http.ResponseWriter, r *http.Request, access AccessScope) {
	return func(w http.ResponseWriter, r *http.Request, access AccessScope) {
		d.handleCreate(w, r, plan, access, plan.createRefreshKeys, "刷新令牌", func(body map[string]any) (*tokenOutcome, error) {
			return plan.exchangeRefresh(r.Context(), d.Store, body)
		})
	}
}

// handleCreate implements the shared create-from-* flow: strict body,
// provider/profile resolution, group binding check, token exchange
// (mock-injected), M08 account creation and the create operation log.
func (d *Deps) handleCreate(w http.ResponseWriter, r *http.Request, plan providerPlan, access AccessScope, allowedKeys []string, kind string, exchange func(map[string]any) (*tokenOutcome, error)) {
	var body map[string]any
	if !kernel.DecodeJSON(w, r, &body) {
		return
	}
	if body == nil {
		body = map[string]any{}
	}
	if !strictBody(body, append(append([]string{}, allowedKeys...), managedFields...)) {
		kernel.WriteBadRequest(w, plan.label+" "+kind+"参数无效")
		return
	}
	managed, ok := parseManagedFields(body)
	if !ok {
		kernel.WriteBadRequest(w, plan.label+" "+kind+"参数无效")
		return
	}
	profile, err := d.Store.resolveProviderProfile(r.Context(), plan.providerCode, managed.ProviderProtocolProfileID, plan.accountType, plan.requiredProfileID)
	if err != nil {
		d.writeProfileError(w, err)
		return
	}
	if managed.GroupID != nil {
		groupOK, groupErr := d.Store.findGroupForProvider(r.Context(), *managed.GroupID, access, plan.providerCode)
		if groupErr != nil {
			kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		if !groupOK {
			kernel.WriteBadRequest(w, "账户分组无效")
			return
		}
	}
	// Node fallback copy: create_from_code → 'X 授权码交换失败',
	// create_from_refresh_token → 'X 刷新令牌授权失败'.
	fallback := plan.label + " 授权码交换失败"
	if kind == "刷新令牌" {
		fallback = plan.label + " 刷新令牌授权失败"
	}
	outcome, err := exchange(body)
	if err != nil {
		d.writeOAuthError(w, err, fallback, "")
		return
	}
	name := accountName(managed.Name, outcome, plan)
	result, err := d.Store.CreateOAuthAccount(r.Context(), CreateAccountInput{
		ProviderCode:              plan.providerCode,
		ProviderProtocolProfileID: profile.ID,
		Name:                      name,
		AccountType:               plan.accountType,
		Credentials:               outcome.Credentials,
		Status:                    managed.Status,
		ConcurrencyLimit:          managed.ConcurrencyLimit,
		Priority:                  managed.Priority,
		SuperPriorityEnabled:      managed.SuperPriorityEnabled,
		FallbackEnabled:           managed.FallbackEnabled,
		SupportedModels:           managed.SupportedModels,
		HealthCheckModel:          managed.HealthCheckModel,
		HealthCheckEndpointMode:   managed.HealthCheckEndpointMode,
		ModelMappings:             managed.ModelMappings,
		Tags:                      managed.Tags,
		ProxyProfileID:            managed.ProxyProfileID,
		GroupID:                   managed.GroupID,
		AccountExpiresAt:          managed.AccountExpiresAt,
		AvailabilitySchedule:      managed.AvailabilitySchedule,
		Notes:                     managed.Notes,
	}, access)
	if err != nil {
		d.writeCreateError(w, err, fallback)
		return
	}
	operationKey, summaryPrefix := plan.createLogContext(kind)
	d.recordCreateLog(r, plan, access, operationKey, summaryPrefix, result.ID, result.OwnerSystemAccountID, name, managed.GroupID)
	writeCreated(w, map[string]any{"id": result.ID, "status": result.Status})
}

// createLogContext mirrors the per-route operation keys and summary prefixes:
// create_from_code → '通过授权码创建 X OAuth 账户', create_from_refresh_token →
// '通过 Refresh Token 创建 X OAuth 账户'.
func (p providerPlan) createLogContext(kind string) (operationKey, summaryPrefix string) {
	if kind == "授权码" {
		return p.module + ".create_from_code", "通过授权码创建 " + p.label + " OAuth 账户"
	}
	return p.module + ".create_from_refresh_token", "通过 Refresh Token 创建 " + p.label + " OAuth 账户"
}

// accountName mirrors `input.name ?? tokenInfo.email ?? 'X OAuth Account'`
// (gemini skips the email fallback).
func accountName(requested *string, outcome *tokenOutcome, plan providerPlan) string {
	if requested != nil && strings.TrimSpace(*requested) != "" {
		return strings.TrimSpace(*requested)
	}
	if plan.emailNameFallback && outcome != nil {
		if email := strings.TrimSpace(outcome.Name); email != "" {
			return email
		}
	}
	return plan.defaultAccountName
}

// --- refresh / reauthorize --------------------------------------------------

func (d *Deps) refreshToken(plan providerPlan, selfOnly bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !scopeQueryOK(r) {
			kernel.WriteBadRequest(w, "系统账号 ID 不能为空")
			return
		}
		access := scopeFor(r, selfOnly)
		auth := authsys.AuthContextFrom(r)
		var body map[string]any
		if !kernel.DecodeJSON(w, r, &body) {
			return
		}
		if body == nil {
			body = map[string]any{}
		}
		if !strictBody(body, []string{"expectedConfigRevision"}) {
			kernel.WriteBadRequest(w, plan.label+" 访问令牌刷新参数无效")
			return
		}
		revision, ok := requiredRevision(body, "expectedConfigRevision")
		if !ok {
			kernel.WriteBadRequest(w, plan.label+" 访问令牌刷新参数无效")
			return
		}
		current, err := d.Store.findRotationAccount(r.Context(), r.PathValue("id"), access)
		if err != nil {
			d.writeStoreError(w, err)
			return
		}
		if current == nil || !plan.rotatable(current) {
			kernel.WriteError(w, http.StatusNotFound, plan.label+" OAuth 账户不存在或无权操作")
			return
		}
		if plan.slug == "openai" && isOpenAIBlockedErrorAccount(current) {
			kernel.WriteBadRequest(w, "异常账户请先执行异常恢复后再操作")
			return
		}
		if plan.slug != "openai" && stringCredential(current.Credentials, "refresh_token") == "" {
			kernel.WriteBadRequest(w, plan.label+" OAuth 账户缺少 Refresh Token")
			return
		}
		if current.ConfigRevision != revision {
			kernel.WriteError(w, http.StatusConflict, plan.label+" OAuth 账户已被其他操作更新，请刷新页面后重试")
			return
		}
		tokenCredentials, err := plan.refreshStored(r.Context(), d.Store, current)
		if err != nil {
			d.writeOAuthError(w, err, plan.label+" 访问令牌刷新失败", plan.revisionConflictMessage)
			return
		}
		updated, err := d.Store.RotateCredentials(r.Context(), RotateCredentialsInput{
			AccountID:                         current.ID,
			ExpectedConfigRevision:            revision,
			ExpectedProviderCode:              plan.providerCode,
			ExpectedAccountType:               plan.accountType,
			ExpectedProviderProtocolProfileID: current.ProviderProtocolProfileID,
			Credentials:                       mergeRotationCredentials(current.Credentials, tokenCredentials, plan.preserveBaseURL),
			Access:                            access,
		})
		if err != nil {
			d.writeOAuthError(w, err, plan.label+" 访问令牌刷新失败", plan.revisionConflictMessage)
			return
		}
		if updated == nil {
			kernel.WriteError(w, http.StatusNotFound, plan.label+" OAuth 账户不存在或无权操作")
			return
		}
		d.recordUpdateLog(r, plan, access, auth, current, updated, "refresh_token", "刷新 "+plan.label+" OAuth Token")
		kernel.WriteOK(w, map[string]any{
			"id": updated.ID, "configRevision": updated.ConfigRevision, "updatedAt": updated.UpdatedAt,
		}, "")
	}
}

func (d *Deps) reauthorizeFromCode(plan providerPlan, selfOnly bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !scopeQueryOK(r) {
			kernel.WriteBadRequest(w, "系统账号 ID 不能为空")
			return
		}
		access := scopeFor(r, selfOnly)
		auth := authsys.AuthContextFrom(r)
		var body map[string]any
		if !kernel.DecodeJSON(w, r, &body) {
			return
		}
		if body == nil {
			body = map[string]any{}
		}
		if !strictBody(body, plan.reauthCodeKeys) {
			kernel.WriteBadRequest(w, plan.label+" 重新授权参数无效")
			return
		}
		revision, ok := requiredRevision(body, "expectedConfigRevision")
		if !ok {
			kernel.WriteBadRequest(w, plan.label+" 重新授权参数无效")
			return
		}
		current, err := d.Store.findRotationAccount(r.Context(), r.PathValue("id"), access)
		if err != nil {
			d.writeStoreError(w, err)
			return
		}
		if current == nil || !plan.rotatable(current) {
			kernel.WriteError(w, http.StatusNotFound, plan.label+" OAuth 账户不存在或无权操作")
			return
		}
		if current.ConfigRevision != revision {
			kernel.WriteError(w, http.StatusConflict, plan.revisionMessage(plan.label+" OAuth 重新授权失败"))
			return
		}
		tokenCredentials, err := plan.exchangeCode(r.Context(), d.Store, body, access.ViewerID)
		if err != nil {
			d.writeOAuthError(w, err, plan.label+" OAuth 重新授权失败", plan.revisionConflictMessage)
			return
		}
		updated, err := d.Store.RotateCredentials(r.Context(), RotateCredentialsInput{
			AccountID:                         current.ID,
			ExpectedConfigRevision:            revision,
			ExpectedProviderCode:              plan.providerCode,
			ExpectedAccountType:               plan.accountType,
			ExpectedProviderProtocolProfileID: current.ProviderProtocolProfileID,
			Credentials:                       mergeRotationCredentials(current.Credentials, tokenCredentials.Credentials, plan.preserveBaseURL),
			Access:                            access,
		})
		if err != nil {
			d.writeOAuthError(w, err, plan.label+" OAuth 重新授权失败", plan.revisionConflictMessage)
			return
		}
		if updated == nil {
			kernel.WriteError(w, http.StatusNotFound, plan.label+" OAuth 账户不存在或无权操作")
			return
		}
		d.recordUpdateLog(r, plan, access, auth, current, updated, "reauthorize_from_code", "重新授权 "+plan.label+" OAuth 账户")
		writeRotationReceipt(w, updated)
	}
}

func (d *Deps) reauthorizeFromRefreshToken(plan providerPlan, selfOnly bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !scopeQueryOK(r) {
			kernel.WriteBadRequest(w, "系统账号 ID 不能为空")
			return
		}
		access := scopeFor(r, selfOnly)
		auth := authsys.AuthContextFrom(r)
		var body map[string]any
		if !kernel.DecodeJSON(w, r, &body) {
			return
		}
		if body == nil {
			body = map[string]any{}
		}
		if !strictBody(body, plan.reauthRefreshKeys) {
			kernel.WriteBadRequest(w, plan.label+" 刷新令牌参数无效")
			return
		}
		revision, ok := requiredRevision(body, "expectedConfigRevision")
		if !ok {
			kernel.WriteBadRequest(w, plan.label+" 刷新令牌参数无效")
			return
		}
		current, err := d.Store.findRotationAccount(r.Context(), r.PathValue("id"), access)
		if err != nil {
			d.writeStoreError(w, err)
			return
		}
		if current == nil || !plan.rotatable(current) {
			kernel.WriteError(w, http.StatusNotFound, plan.label+" OAuth 账户不存在或无权操作")
			return
		}
		if current.ConfigRevision != revision {
			kernel.WriteError(w, http.StatusConflict, plan.revisionMessage(plan.label+" 刷新令牌重新授权失败"))
			return
		}
		tokenCredentials, err := plan.refreshInput(r.Context(), d.Store, body, current)
		if err != nil {
			d.writeOAuthError(w, err, plan.label+" 刷新令牌重新授权失败", plan.revisionConflictMessage)
			return
		}
		updated, err := d.Store.RotateCredentials(r.Context(), RotateCredentialsInput{
			AccountID:                         current.ID,
			ExpectedConfigRevision:            revision,
			ExpectedProviderCode:              plan.providerCode,
			ExpectedAccountType:               plan.accountType,
			ExpectedProviderProtocolProfileID: current.ProviderProtocolProfileID,
			Credentials:                       mergeRotationCredentials(current.Credentials, tokenCredentials, plan.preserveBaseURL),
			Access:                            access,
		})
		if err != nil {
			d.writeOAuthError(w, err, plan.label+" 刷新令牌重新授权失败", plan.revisionConflictMessage)
			return
		}
		if updated == nil {
			kernel.WriteError(w, http.StatusNotFound, plan.label+" OAuth 账户不存在或无权操作")
			return
		}
		d.recordUpdateLog(r, plan, access, auth, current, updated, "reauthorize_from_refresh_token", "使用 Refresh Token 重新授权 "+plan.label+" OAuth 账户")
		writeRotationReceipt(w, updated)
	}
}

// --- grok SSO import --------------------------------------------------------

func (d *Deps) ssoToOAuth(plan providerPlan) func(w http.ResponseWriter, r *http.Request, access AccessScope) {
	return func(w http.ResponseWriter, r *http.Request, access AccessScope) {
		var body map[string]any
		if !kernel.DecodeJSON(w, r, &body) {
			return
		}
		if body == nil {
			body = map[string]any{}
		}
		if !strictBody(body, append(append([]string{}, "ssoTokens", "ssoToken"), managedFields...)) {
			kernel.WriteBadRequest(w, "Grok SSO 导入参数无效")
			return
		}
		var rawTokens []string
		if value, present := body["ssoTokens"]; present && value != nil {
			list, ok := value.([]any)
			if !ok {
				kernel.WriteBadRequest(w, "Grok SSO 导入参数无效")
				return
			}
			for _, item := range list {
				text, ok := item.(string)
				if !ok || len(text) > 16384 {
					kernel.WriteBadRequest(w, "Grok SSO 导入参数无效")
					return
				}
				rawTokens = append(rawTokens, text)
			}
		}
		single := ""
		if value, present := body["ssoToken"]; present && value != nil {
			text, ok := value.(string)
			if !ok || len(text) > 16384 {
				kernel.WriteBadRequest(w, "Grok SSO 导入参数无效")
				return
			}
			single = text
		}
		tokens := normalizeGrokSSOImportTokens(rawTokens, single)
		if len(tokens) == 0 {
			kernel.WriteBadRequest(w, "Grok SSO Cookie 不能为空")
			return
		}
		if len(tokens) > 3 {
			kernel.WriteBadRequest(w, "Grok SSO Cookie 单次最多导入 3 个")
			return
		}
		managed, ok := parseManagedFields(body)
		if !ok {
			kernel.WriteBadRequest(w, "Grok SSO 导入参数无效")
			return
		}
		profile, err := d.Store.resolveProviderProfile(r.Context(), plan.providerCode, managed.ProviderProtocolProfileID, plan.accountType, plan.requiredProfileID)
		if err != nil {
			d.writeProfileError(w, err)
			return
		}
		if managed.GroupID != nil {
			groupOK, groupErr := d.Store.findGroupForProvider(r.Context(), *managed.GroupID, access, plan.providerCode)
			if groupErr != nil {
				kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
				return
			}
			if !groupOK {
				kernel.WriteBadRequest(w, "账户分组无效")
				return
			}
		}
		createdIDs := []string{}
		failed := []map[string]any{}
		for index, token := range tokens {
			position := index + 1
			outcome, convertErr := d.Store.exchangeGrokSSOToken(r.Context(), token)
			if convertErr != nil {
				failed = append(failed, map[string]any{
					"index": position,
					"error": oauthErrorText(convertErr, "Grok SSO Cookie 转换失败"),
				})
				continue
			}
			name := grokSSOImportAccountName(managed.Name, outcome, position, len(tokens))
			accountExpiresAt, expiresErr := grokSSOImportAccountExpiresAt(managed.AccountExpiresAt, outcome)
			if expiresErr != nil {
				failed = append(failed, map[string]any{
					"index": position,
					"error": expiresErr.Error(),
				})
				continue
			}
			result, createErr := d.Store.CreateOAuthAccount(r.Context(), CreateAccountInput{
				ProviderCode:              plan.providerCode,
				ProviderProtocolProfileID: profile.ID,
				Name:                      name,
				AccountType:               plan.accountType,
				Credentials:               outcome.Credentials,
				Status:                    managed.Status,
				ConcurrencyLimit:          managed.ConcurrencyLimit,
				Priority:                  managed.Priority,
				SuperPriorityEnabled:      managed.SuperPriorityEnabled,
				FallbackEnabled:           managed.FallbackEnabled,
				SupportedModels:           managed.SupportedModels,
				HealthCheckModel:          managed.HealthCheckModel,
				HealthCheckEndpointMode:   managed.HealthCheckEndpointMode,
				ModelMappings:             managed.ModelMappings,
				Tags:                      managed.Tags,
				ProxyProfileID:            managed.ProxyProfileID,
				GroupID:                   managed.GroupID,
				AccountExpiresAt:          accountExpiresAt,
				AvailabilitySchedule:      managed.AvailabilitySchedule,
				Notes:                     managed.Notes,
			}, access)
			if createErr != nil {
				failed = append(failed, map[string]any{
					"index": position,
					"error": oauthErrorText(createErr, "Grok SSO Cookie 转换失败"),
				})
				continue
			}
			d.recordCreateLog(r, plan, access, plan.module+".sso_to_oauth",
				"通过 SSO Cookie 创建 Grok OAuth 账户", result.ID, result.OwnerSystemAccountID, name, managed.GroupID)
			createdIDs = append(createdIDs, result.ID)
		}
		kernel.WriteOK(w, map[string]any{
			"createdCount": len(createdIDs),
			"createdIds":   createdIDs,
			"failed":       failed,
		}, "")
	}
}

// exchangeGrokSSOToken mirrors exchangeGrokSSOToken: device-flow conversion and
// the shared token-info normalization.
func (s *Store) exchangeGrokSSOToken(ctx context.Context, ssoToken string) (*tokenOutcome, error) {
	payload, err := convertGrokSSOToOAuth(ensureContext(ctx), ssoToken, s.ssoDeps())
	if err != nil {
		return nil, err
	}
	info := toGrokTokenInfo(*payload, GrokOAuthClientID, func() int64 { return s.now().UnixMilli() })
	return &tokenOutcome{
		Credentials: buildGrokOAuthCredentials(info, ""),
		Name:        info.Email,
	}, nil
}

// grokSSOImportAccountName mirrors grokSSOImportAccountName.
func grokSSOImportAccountName(requested *string, outcome *tokenOutcome, index, total int) string {
	baseName := ""
	if requested != nil {
		baseName = strings.TrimSpace(*requested)
	}
	if baseName == "" && outcome != nil {
		baseName = strings.TrimSpace(outcome.Name)
	}
	if baseName == "" {
		baseName = "Grok OAuth Account"
	}
	if total > 1 {
		return baseName + " #" + itoa(index)
	}
	return baseName
}

// grokSSOImportAccountExpiresAt mirrors grokSSOImportAccountExpiresAt: without
// a refresh token the account expires no later than the access token itself.
func grokSSOImportAccountExpiresAt(requested *string, outcome *tokenOutcome) (*string, error) {
	credentials := outcome.Credentials
	if stringCredential(credentials, "refresh_token") != "" {
		return requested, nil
	}
	tokenExpiresAt := stringCredential(credentials, "expires_at")
	tokenMillis, ok := rfc3339Millis(tokenExpiresAt)
	if !ok {
		return nil, errors.New("Grok OAuth token expiresAt 必须是带 Z 或数值 offset 的 RFC3339 时间")
	}
	if requested == nil || strings.TrimSpace(*requested) == "" {
		return &tokenExpiresAt, nil
	}
	requestedMillis, ok := rfc3339Millis(*requested)
	if !ok {
		return nil, &ValidationError{Message: "Grok OAuth 到期时间必须是带 Z 或数值 offset 的 RFC3339 时间"}
	}
	if requestedMillis < tokenMillis {
		return requested, nil
	}
	return &tokenExpiresAt, nil
}

// rfc3339Millis mirrors rfc3339InstantMilliseconds.
func rfc3339Millis(value string) (int64, bool) {
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
	if err != nil {
		return 0, false
	}
	return parsed.UnixMilli(), true
}
