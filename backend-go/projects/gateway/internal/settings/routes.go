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

// Mount wires the settings route family. GET/PATCH /settings sit behind
// requireAdmin (Node settings.routes.ts); GET /settings/public is registered
// on the system API prefix before the auth chain (system-api-app.ts) and is
// reachable without a session.
func (d *Deps) Mount(k *kernel.Kernel) {
	prefix := "/__aisys__/api/settings"
	k.Register("GET "+prefix+"/public", http.HandlerFunc(d.publicSettings))
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
// with the diffSafeFields change list (empty when values did not change).
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
	changes := []authsys.OperationLogChange{}
	for _, field := range fields {
		beforeValue := before[field]
		afterValue := after[field]
		if comparableValue(beforeValue) == comparableValue(afterValue) {
			continue
		}
		changes = append(changes, authsys.OperationLogChange{
			Field:  field,
			Label:  field,
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
// carry (strings, JSON integers): strings stay verbatim (200-char clamp),
// everything else renders as its JSON text.
func safeChangeText(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		if len(text) > 200 {
			return text[:200] + "..."
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
