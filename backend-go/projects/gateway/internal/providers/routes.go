package providers

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

// Deps bundles the providers collaborators.
type Deps struct {
	Store *Store
	Auth  *authsys.Deps
	// Sink receives the built-in model configuration operation log entries
	// (Node recordOperationLogAsync); nil disables recording.
	Sink authsys.OperationLogSink
}

// Mount wires the providers route family exactly as Node mounts it
// (system-api-app.ts): a single ${systemApiPrefix}/providers surface, global
// session auth only, the admin gate on GET /providers (providersRouter
// requireAdmin) and the viewScope=admin management fork on /list, /:code and
// /:code/models. Node has no my-providers surface; the earlier Go mirror is
// removed. The write family (POST/PATCH/DELETE /{code}/models,
// PUT /{code}/default-health-check-model) is session-level in Node
// (providers.routes.ts:188-241,334-606 — getRequestAuthContext without an
// admin gate), so Go mounts it behind RequireSession(true) (the write touch)
// and enforces the per-route admin forks inside the handlers.
func (d *Deps) Mount(k *kernel.Kernel) {
	prefix := "/__aisys__/api"
	admin := d.Auth.RequireAdmin
	// Node requireAuth with the providers GET rule at mode 'read'
	// (system-api-db-access.ts) touches no session.
	read := d.Auth.RequireSession(false)
	write := d.Auth.RequireSession(true)

	k.Register("GET "+prefix+"/providers/list", read(http.HandlerFunc(d.listItems)))
	k.Register("GET "+prefix+"/providers", admin(http.HandlerFunc(d.listDefinitions)))
	k.Register("GET "+prefix+"/providers/options", read(http.HandlerFunc(d.options)))
	k.Register("GET "+prefix+"/providers/definitions", read(http.HandlerFunc(d.definitions)))
	k.Register("GET "+prefix+"/providers/models/options", read(http.HandlerFunc(d.modelOptions)))
	k.Register("GET "+prefix+"/providers/{code}", read(http.HandlerFunc(d.find)))
	k.Register("GET "+prefix+"/providers/{code}/models", read(http.HandlerFunc(d.models)))
	k.Register("GET "+prefix+"/providers/{code}/models/{modelId}/capabilities", read(http.HandlerFunc(d.modelCapabilities)))

	base := prefix + "/providers"
	k.Register("POST "+base+"/{code}/models", write(http.HandlerFunc(d.createModel)))
	k.Register("PATCH "+base+"/{code}/models/{modelId}", write(http.HandlerFunc(d.patchModel)))
	k.Register("DELETE "+base+"/{code}/models/{modelId}", write(http.HandlerFunc(d.deleteModel)))
	k.Register("PUT "+base+"/{code}/default-health-check-model", write(http.HandlerFunc(d.putDefaultHealthCheckModel)))
}

// isManagementProviderRequest mirrors providers.routes.ts:612-618: the
// viewScope=admin query (single value, Node string identity) plus an admin
// role (super_admin or admin).
func isManagementProviderRequest(r *http.Request) bool {
	values := r.URL.Query()["viewScope"]
	if len(values) != 1 || values[0] != "admin" {
		return false
	}
	auth := authsys.AuthContextFrom(r)
	return auth != nil && isAdminRole(auth.Role)
}

func isAdminRole(role string) bool {
	return role == "super_admin" || role == "admin"
}

// requestSystemAccountID mirrors providerModelRequestSystemAccountId(
// getRequestAccessScope(req.query.systemAccountId)): admins may pass a
// systemAccountId filter (anything but 'all'), everyone else is pinned to
// the caller identity.
func requestSystemAccountID(r *http.Request) string {
	auth := authsys.AuthContextFrom(r)
	if auth == nil {
		return ""
	}
	filter := ""
	if values := r.URL.Query()["systemAccountId"]; len(values) == 1 {
		filter = strings.TrimSpace(values[0])
		if filter == "all" {
			filter = ""
		}
	}
	if filter != "" && isAdminRole(auth.Role) {
		return filter
	}
	return auth.SystemAccountID
}

// firstQueryValue mirrors firstQueryValue: repeated params collapse to the
// first value; anything else is absent.
func firstQueryValue(r *http.Request, key string) string {
	if values := r.URL.Query()[key]; len(values) > 0 {
		return values[0]
	}
	return ""
}

// booleanQueryValue mirrors booleanQueryValue.
func booleanQueryValue(r *http.Request, key string) *bool {
	values := r.URL.Query()[key]
	if len(values) == 0 {
		return nil
	}
	switch strings.ToLower(strings.TrimSpace(values[0])) {
	case "1", "true", "yes":
		enabled := true
		return &enabled
	case "0", "false", "no":
		disabled := false
		return &disabled
	}
	return nil
}

// listItems serves GET /list: the catalogue list rows with the
// defaultHealthCheckModel preference overlay; the management fork
// (viewScope=admin + admin role) sees disabled providers too.
func (d *Deps) listItems(w http.ResponseWriter, r *http.Request) {
	items, err := d.Store.ListCatalogListItems(r.Context())
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	if !isManagementProviderRequest(r) {
		enabled := items[:0]
		for _, item := range items {
			if item.Enabled {
				enabled = append(enabled, item)
			}
		}
		items = enabled
	}
	d.overlayListItems(r, items)
	kernel.WriteOK(w, items, "")
}

func (d *Deps) overlayListItems(r *http.Request, items []ProviderListItem) {
	if len(items) == 0 {
		return
	}
	codes := make([]string, 0, len(items))
	for _, item := range items {
		codes = append(codes, item.Code)
	}
	preferences, err := d.Store.ListDefaultHealthCheckModelPreferences(r.Context(), requestSystemAccountID(r), codes)
	if err != nil {
		preferences = map[string]string{}
	}
	systemDefaults, err := d.Store.ListSystemDefaultHealthCheckModels(r.Context(), codes)
	if err != nil {
		systemDefaults = map[string]string{}
	}
	overlayListItemHealthCheckModels(items, preferences, systemDefaults)
}

// listDefinitions serves GET / (requireAdmin): the flat ProviderDefinition
// array (non-paginated envelope) with the preference overlay.
func (d *Deps) listDefinitions(w http.ResponseWriter, r *http.Request) {
	definitions, err := d.Store.ListDefinitions(r.Context())
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	d.overlayDefinitions(r, definitions)
	kernel.WriteOK(w, definitions, "")
}

// definitions serves GET /definitions: the flat ProviderDefinition array
// filtered to enabled providers.
func (d *Deps) definitions(w http.ResponseWriter, r *http.Request) {
	definitions, err := d.Store.ListDefinitions(r.Context())
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	enabled := definitions[:0]
	for _, definition := range definitions {
		if definition.Enabled {
			enabled = append(enabled, definition)
		}
	}
	definitions = enabled
	d.overlayDefinitions(r, definitions)
	kernel.WriteOK(w, definitions, "")
}

func (d *Deps) overlayDefinitions(r *http.Request, definitions []ProviderDefinition) {
	if len(definitions) == 0 {
		return
	}
	codes := make([]string, 0, len(definitions))
	for _, definition := range definitions {
		codes = append(codes, definition.Code)
	}
	preferences, err := d.Store.ListDefaultHealthCheckModelPreferences(r.Context(), requestSystemAccountID(r), codes)
	if err != nil {
		preferences = map[string]string{}
	}
	systemDefaults, err := d.Store.ListSystemDefaultHealthCheckModels(r.Context(), codes)
	if err != nil {
		systemDefaults = map[string]string{}
	}
	overlayDefinitionHealthCheckModels(definitions, preferences, systemDefaults)
}

// options serves GET /options: enabled {id, code, name, enabled} rows.
func (d *Deps) options(w http.ResponseWriter, r *http.Request) {
	providerOptions, err := d.Store.ListProviderOptions(r.Context())
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	kernel.WriteOK(w, providerOptions, "")
}

// modelOptions serves GET /models/options: query normalization (400 on
// invalid protocol/limit), the enabled-provider 404 fork, then the merged
// built-in/custom selection options.
func (d *Deps) modelOptions(w http.ResponseWriter, r *http.Request) {
	query, message := normalizeModelOptionQuery(r)
	if message != "" {
		kernel.WriteBadRequest(w, message)
		return
	}
	if query.ProviderCode != "" {
		provider, err := d.Store.FindProviderOption(r.Context(), query.ProviderCode)
		if err != nil {
			kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		if provider == nil || !provider.Enabled {
			kernel.WriteNotFound(w, "供应商不存在或已停用")
			return
		}
	}
	query.SystemAccountID = requestSystemAccountID(r)
	providerModelOptions, err := d.Store.ListProviderModelSelectionOptions(r.Context(), query)
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	kernel.WriteOK(w, providerModelOptions, "")
}

// normalizeModelOptionQuery mirrors normalizeProviderModelOptionQuery and
// returns the 400 message on invalid input.
func normalizeModelOptionQuery(r *http.Request) (ModelOptionQuery, string) {
	query := ModelOptionQuery{Limit: 50}
	query.ProviderCode = strings.TrimSpace(firstQueryValue(r, "providerCode"))
	protocol := strings.TrimSpace(firstQueryValue(r, "protocol"))
	if protocol != "" && protocol != "openai" && protocol != "anthropic" && protocol != "gemini" {
		return query, "protocol 必须是 openai、anthropic 或 gemini"
	}
	query.Protocol = protocol
	query.Keyword = strings.TrimSpace(firstQueryValue(r, "keyword"))
	if limitText := strings.TrimSpace(firstQueryValue(r, "limit")); limitText != "" {
		limit, parseErr := strconv.ParseFloat(limitText, 64)
		if parseErr != nil || limit != float64(int64(limit)) || limit < 1 || limit > 50 {
			return query, "limit 必须是 1 到 50 的整数"
		}
		query.Limit = int(limit)
	}
	query.SelectedIDs = normalizeSelectedIDs(r)
	return query, ""
}

// normalizeSelectedIDs mirrors the selectedIds / selectedIds[] union with
// comma splitting, trimming, dedupe and the 50 entry cap.
func normalizeSelectedIDs(r *http.Request) []string {
	values := append(append([]string{}, r.URL.Query()["selectedIds"]...), r.URL.Query()["selectedIds[]"]...)
	seen := map[string]bool{}
	output := []string{}
	for _, raw := range values {
		for _, piece := range strings.Split(raw, ",") {
			trimmed := strings.TrimSpace(piece)
			if trimmed == "" || seen[trimmed] {
				continue
			}
			seen[trimmed] = true
			if len(output) < 50 {
				output = append(output, trimmed)
			}
		}
	}
	return output
}

// find serves GET /{code}: the ProviderDefinition by code with the
// preference overlay; non-management requests receive the Node 404 for
// disabled providers.
func (d *Deps) find(w http.ResponseWriter, r *http.Request) {
	definition, err := d.Store.FindDefinition(r.Context(), r.PathValue("code"))
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	if definition == nil || (!definition.Enabled && !isManagementProviderRequest(r)) {
		kernel.WriteNotFound(w, "供应商不存在或已停用")
		return
	}
	roster := []ProviderDefinition{*definition}
	d.overlayDefinitions(r, roster)
	kernel.WriteOK(w, roster[0], "")
}

// models serves GET /{code}/models: the merged model catalog; the 404 fork
// carries Node's shorter message for this route.
func (d *Deps) models(w http.ResponseWriter, r *http.Request) {
	provider, err := d.Store.FindProviderOption(r.Context(), r.PathValue("code"))
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	if provider == nil || (!provider.Enabled && !isManagementProviderRequest(r)) {
		kernel.WriteError(w, http.StatusNotFound, "供应商不存在")
		return
	}
	items, err := d.Store.ListProviderModelsForRequest(r.Context(), provider.Code, requestSystemAccountID(r),
		booleanOrFalse(booleanQueryValue(r, "includeInactive")),
		booleanOrFalse(booleanQueryValue(r, "includeUnpriced")))
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	kernel.WriteOK(w, items, "")
}

func booleanOrFalse(value *bool) bool {
	return value != nil && *value
}

// modelCapabilities serves GET /{code}/models/{modelId}/capabilities: the
// provider must exist and be enabled (no management bypass, Node :164), the
// model resolution follows the merged test catalog.
func (d *Deps) modelCapabilities(w http.ResponseWriter, r *http.Request) {
	definitions, err := d.Store.ListDefinitions(r.Context())
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	code := r.PathValue("code")
	providerFound := false
	for _, definition := range definitions {
		if definition.Code == code {
			providerFound = definition.Enabled
			break
		}
	}
	if !providerFound {
		kernel.WriteNotFound(w, "供应商不存在或已停用")
		return
	}
	capability, err := d.Store.FindProviderModelCapabilities(r.Context(), code, requestSystemAccountID(r), r.PathValue("modelId"))
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	if capability == nil {
		kernel.WriteNotFound(w, "模型不存在")
		return
	}
	kernel.WriteOK(w, capability, "")
}
