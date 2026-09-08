package settings

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

// Deps bundles the M12 slice collaborators.
type Deps struct {
	Store *Store
	Auth  *authsys.Deps
	Sink  authsys.OperationLogSink
}

// Mount wires the settings route family. GET/PATCH /settings,
// GET/PATCH /settings/global and GET/PATCH /settings/sections/{sectionKey}
// sit behind requireAdmin (Node settings.routes.ts); GET /settings/public is
// registered on the system API prefix before the auth chain
// (system-api-app.ts) and is reachable without a session.
func (d *Deps) Mount(k *kernel.Kernel) {
	prefix := "/__aisys__/api/settings"
	k.Register("GET "+prefix+"/public", http.HandlerFunc(d.publicSettings))
	k.Register("GET "+prefix+"/global", d.Auth.RequireAdmin(http.HandlerFunc(d.getGlobalSettings)))
	k.Register("PATCH "+prefix+"/global", d.Auth.RequireAdmin(http.HandlerFunc(d.patchGlobalSettings)))
	k.Register("GET "+prefix+"/sections/{sectionKey}", d.Auth.RequireAdmin(http.HandlerFunc(d.getSettingsSection)))
	k.Register("PATCH "+prefix+"/sections/{sectionKey}", d.Auth.RequireAdmin(http.HandlerFunc(d.patchSettingsSection)))
	k.Register("GET "+prefix, d.Auth.RequireAdmin(http.HandlerFunc(d.getSettings)))
	k.Register("PATCH "+prefix, d.Auth.RequireAdmin(http.HandlerFunc(d.patchSettings)))
}

// publicSettings mirrors GET /settings/public: ok(await
// listPublicGlobalSettingsAsync()) without any auth requirement.
func (d *Deps) publicSettings(w http.ResponseWriter, r *http.Request) {
	settings, err := d.Store.LoadPublic(r.Context())
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	kernel.WriteOK(w, settings, "")
}

// getGlobalSettings mirrors GET /settings/global: ok(await
// listGlobalSettingsAsync()) behind requireAdmin. Storage failures take
// next(error) — the generic 500.
func (d *Deps) getGlobalSettings(w http.ResponseWriter, r *http.Request) {
	settings, err := d.Store.LoadGlobal(r.Context())
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	kernel.WriteOK(w, settings, "")
}

// patchGlobalSettings mirrors PATCH /settings/global: runLoggedOperationAsync
// wraps updateGlobalSettingsAsync and always appends the settings.update_global
// operation log with the 系统名称/系统图标 change labels. Every failure inside
// the route try-block renders 400 with the verbatim message
// (settings.routes.ts catch block).
func (d *Deps) patchGlobalSettings(w http.ResponseWriter, r *http.Request) {
	auth := authsys.AuthContextFrom(r)
	if auth == nil {
		kernel.WriteError(w, http.StatusUnauthorized, "请先登录")
		return
	}
	var body map[string]any
	if !kernel.DecodeJSON(w, r, &body) {
		return
	}
	before, err := d.Store.LoadGlobal(r.Context())
	if err != nil {
		kernel.WriteBadRequest(w, errorText(err, "全局设置参数无效"))
		return
	}
	settings, err := d.Store.UpdateGlobal(r.Context(), body)
	if err != nil {
		kernel.WriteBadRequest(w, errorText(err, "全局设置参数无效"))
		return
	}
	if d.Sink != nil {
		d.Sink.Record(authsys.OperationLogEntry{
			ActorSystemAccountID: auth.SystemAccountID,
			ActorUsername:        auth.Username,
			ActorDisplayName:     auth.DisplayName,
			ActorRole:            auth.Role,
			Mode:                 "admin",
			Module:               "settings",
			Action:               "update_global",
			OperationKey:         "settings.update_global",
			ResourceType:         "global_settings",
			ResourceID:           "global",
			ResourceName:         "全局品牌设置",
			Summary:              "更新全局品牌设置",
			VisibilityScope:      "all_users",
			DetailLevel:          "summary",
			Changes: diffSafeFieldsWithLabels(before, settings, map[string]string{
				"appName": "系统名称",
				"appIcon": "系统图标",
			}),
		}, r)
	}
	kernel.WriteOK(w, settings, "")
}

// getSettingsSection mirrors GET /settings/sections/:sectionKey: ok({
// sectionKey, values: await getManagementSettingsSectionAsync(sectionKey) }).
// An unknown section key renders 400 未知设置分区：key
// (InvalidSettingsSectionError); every other failure takes next(error) — the
// generic 500.
func (d *Deps) getSettingsSection(w http.ResponseWriter, r *http.Request) {
	sectionKey, err := parseSettingsSectionKey(r.PathValue("sectionKey"))
	if err != nil {
		kernel.WriteBadRequest(w, err.Error())
		return
	}
	values, err := d.Store.LoadSection(r.Context(), sectionKey)
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	kernel.WriteOK(w, map[string]any{"sectionKey": sectionKey, "values": values}, "")
}

// patchSettingsSection mirrors PATCH /settings/sections/:sectionKey: the body
// must be a plain JSON object (parseSectionPatch), the section whitelist and
// per-key validation reject bad payloads, and the runLoggedOperationAsync
// wrapper appends the update_global (brand) or update_settings (system)
// operation log with resourceId=sectionKey. Every failure inside the route
// try-block renders 400 with the verbatim message.
func (d *Deps) patchSettingsSection(w http.ResponseWriter, r *http.Request) {
	auth := authsys.AuthContextFrom(r)
	if auth == nil {
		kernel.WriteError(w, http.StatusUnauthorized, "请先登录")
		return
	}
	sectionKey, err := parseSettingsSectionKey(r.PathValue("sectionKey"))
	if err != nil {
		kernel.WriteBadRequest(w, err.Error())
		return
	}
	var raw any
	if !kernel.DecodeJSON(w, r, &raw) {
		return
	}
	body, ok := raw.(map[string]any)
	if !ok {
		kernel.WriteBadRequest(w, "设置分区更新必须是普通 JSON 对象")
		return
	}
	before, err := d.Store.LoadSection(r.Context(), sectionKey)
	if err != nil {
		kernel.WriteBadRequest(w, errorText(err, "设置分区参数无效"))
		return
	}
	values, err := d.Store.UpdateSection(r.Context(), sectionKey, body)
	if err != nil {
		kernel.WriteBadRequest(w, errorText(err, "设置分区参数无效"))
		return
	}
	if d.Sink != nil {
		action, operationKey, resourceType := "update_settings", "settings.update", "system_settings"
		if sectionKey == "brand" {
			action, operationKey, resourceType = "update_global", "settings.update_global", "global_settings"
		}
		d.Sink.Record(authsys.OperationLogEntry{
			ActorSystemAccountID: auth.SystemAccountID,
			ActorUsername:        auth.Username,
			ActorDisplayName:     auth.DisplayName,
			ActorRole:            auth.Role,
			Mode:                 "admin",
			Module:               "settings",
			Action:               action,
			OperationKey:         operationKey,
			ResourceType:         resourceType,
			ResourceID:           sectionKey,
			ResourceName:         "设置分区 " + sectionKey,
			Summary:              "更新设置分区 " + sectionKey,
			VisibilityScope:      "all_users",
			DetailLevel:          "summary",
			Changes:              diffSafeFields(before, values, bodyKeys(body)),
		}, r)
	}
	kernel.WriteOK(w, map[string]any{"sectionKey": sectionKey, "values": values}, "")
}

// parseSettingsSectionKey mirrors parseSectionKey (settings.routes.ts): only
// catalog keys pass; anything else is the 400 未知设置分区 error.
func parseSettingsSectionKey(value string) (string, error) {
	if _, ok := ManagementSettingsSectionCatalog[value]; !ok {
		return "", &UnknownSettingsSectionError{Key: value}
	}
	return value, nil
}

// errorText mirrors the route catch blocks: Node returns error.message for
// Error throws and the fixed fallback otherwise; Go errors always carry text.
func errorText(err error, fallback string) string {
	if err == nil {
		return fallback
	}
	if text := err.Error(); text != "" {
		return text
	}
	return fallback
}

// getSettings mirrors GET /settings: ok(await getSettingsAsync()) — the full
// system settings snapshot. Storage anomalies (missing/unknown/invalid rows)
// render as the generic 500.
func (d *Deps) getSettings(w http.ResponseWriter, r *http.Request) {
	settings, err := d.Store.Load(r.Context())
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	kernel.WriteOK(w, settings, "")
}

// patchSettings mirrors PATCH /settings: runLoggedOperationAsync wraps
// updateSettingsAsync and always appends the settings.update operation log
// with the diffSafeFields change list (empty when values did not change) and
// the all_users/summary visibility set by settings.routes.ts.
func (d *Deps) patchSettings(w http.ResponseWriter, r *http.Request) {
	auth := authsys.AuthContextFrom(r)
	if auth == nil {
		kernel.WriteError(w, http.StatusUnauthorized, "请先登录")
		return
	}
	var body map[string]any
	if !kernel.DecodeJSON(w, r, &body) {
		return
	}
	before, err := d.Store.Load(r.Context())
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	settings, err := d.Store.Update(r.Context(), body)
	if err != nil {
		d.writeMutationError(w, err)
		return
	}
	if d.Sink != nil {
		d.Sink.Record(authsys.OperationLogEntry{
			ActorSystemAccountID: auth.SystemAccountID,
			ActorUsername:        auth.Username,
			ActorDisplayName:     auth.DisplayName,
			ActorRole:            auth.Role,
			Mode:                 "admin",
			Module:               "settings",
			Action:               "update_settings",
			OperationKey:         "settings.update",
			ResourceType:         "system_settings",
			ResourceID:           "system",
			ResourceName:         "系统运行设置",
			Summary:              "更新系统运行设置",
			VisibilityScope:      "all_users",
			DetailLevel:          "summary",
			Changes:              diffSafeFields(before, settings, bodyKeys(body)),
		}, r)
	}
	kernel.WriteOK(w, settings, "")
}

// writeMutationError maps store errors onto the Node route contract: every
// deliberate settings rejection renders 400 with the verbatim message;
// storage failures render the generic 500.
func (d *Deps) writeMutationError(w http.ResponseWriter, err error) {
	var validation *ValidationError
	if errors.As(err, &validation) {
		kernel.WriteBadRequest(w, validation.Message)
		return
	}
	kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
}

// bodyKeys mirrors Object.keys(body); sorted for deterministic change order.
func bodyKeys(body map[string]any) []string {
	keys := make([]string, 0, len(body))
	for key := range body {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// diffSafeFields mirrors operation-log.service.ts diffSafeFields for the
// settings route: labels are the field keys themselves, unchanged fields are
// skipped via the JSON-comparable rendering.
func diffSafeFields(before, after map[string]any, fields []string) []authsys.OperationLogChange {
	labels := make(map[string]string, len(fields))
	for _, field := range fields {
		labels[field] = field
	}
	return diffSafeFieldsWithLabels(before, after, labels)
}

// diffSafeFieldsWithLabels mirrors diffSafeFields with an explicit label map
// (PATCH /settings/global passes 系统名称/系统图标). Keys are sorted for a
// deterministic change order.
func diffSafeFieldsWithLabels(before, after map[string]any, labels map[string]string) []authsys.OperationLogChange {
	fields := make([]string, 0, len(labels))
	for field := range labels {
		fields = append(fields, field)
	}
	sort.Strings(fields)
	changes := []authsys.OperationLogChange{}
	for _, field := range fields {
		beforeValue := before[field]
		afterValue := after[field]
		if comparableValue(beforeValue) == comparableValue(afterValue) {
			continue
		}
		changes = append(changes, authsys.OperationLogChange{
			Field:  field,
			Label:  labels[field],
			Before: safeChangeText(beforeValue),
			After:  safeChangeText(afterValue),
		})
	}
	return changes
}

// comparableValue mirrors operationLogComparableValue (JSON rendering with
// nil collapsing to null).
func comparableValue(value any) string {
	if value == nil {
		return "null"
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "null"
	}
	return string(encoded)
}

// safeChangeText mirrors normalizeSafeValue for the value shapes settings
// carry (strings, JSON integers): strings stay verbatim (200-rune clamp,
// Unicode-safe), everything else renders as its JSON text.
func safeChangeText(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		runes := []rune(text)
		if len(runes) > 200 {
			return string(runes[:200]) + "..."
		}
		return text
	}
	if number, ok := value.(float64); ok {
		return strconv.FormatFloat(number, 'f', -1, 64)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(encoded)
}
