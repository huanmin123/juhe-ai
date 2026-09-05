package proxyprofiles

// HTTP surface of the proxy family (Node proxies.routes.ts):
//
//	GET    /__aisys__/api/proxies/options   (requireAuth)
//	GET    /__aisys__/api/proxies           (requireAdmin)
//	POST   /__aisys__/api/proxies           (requireAdmin + mutation guard)
//	PATCH  /__aisys__/api/proxies/{id}      (requireAdmin)
//	DELETE /__aisys__/api/proxies/{id}      (requireAdmin)

import (
	"encoding/json"
	"errors"
	"math"
	"math/big"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

// Mount wires the proxy route family.
func Mount(k *kernel.Kernel, deps *authsys.Deps, store *Store, sink authsys.OperationLogSink) {
	prefix := "/__aisys__/api"
	k.Register("GET "+prefix+"/proxies/options", deps.RequireSession(true)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		optionsHandler(w, r, store)
	})))
	k.Register("GET "+prefix+"/proxies", deps.RequireAdmin(http.HandlerFunc(listHandler(store))))
	k.Register("POST "+prefix+"/proxies", deps.RequireAdmin(kernel.MutationGuardMiddleware(kernel.MutationGuardOptions{
		OperationKey: "proxies.create",
		Actor:        actorResolver,
		Fingerprint: func(r *http.Request) (any, error) {
			return map[string]any{
				"name":     kernel.TextField(kernel.BodyField(r, "name")),
				"type":     kernel.TextField(kernel.BodyField(r, "type")),
				"host":     kernel.TextField(kernel.BodyField(r, "host")),
				"port":     kernel.BodyField(r, "port"),
				"username": kernel.TextField(kernel.BodyField(r, "username")),
				// Node sensitiveFingerprint never leaks the material.
				"password": sensitiveFingerprint(kernel.BodyField(r, "password")),
			}, nil
		},
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		createHandler(w, r, store, sink)
	}))))
	k.Register("PATCH "+prefix+"/proxies/{id}", deps.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		patchHandler(w, r, store, sink)
	})))
	k.Register("DELETE "+prefix+"/proxies/{id}", deps.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		deleteHandler(w, r, store, sink)
	})))
}

func actorResolver(r *http.Request) string {
	if auth := authsys.AuthContextFrom(r); auth != nil {
		return auth.SystemAccountID
	}
	return "anonymous"
}

// sensitiveFingerprint mirrors sensitiveFingerprint: absent vs set only.
func sensitiveFingerprint(value any) string {
	if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
		return "[set]"
	}
	return ""
}

// ---------------------------------------------------------------------------
// GET /options
// ---------------------------------------------------------------------------

func optionsHandler(w http.ResponseWriter, r *http.Request, store *Store) {
	values := r.URL.Query()
	selectedIds, badRequest := parseSelectedProxyOptionIds(values)
	if badRequest != "" {
		kernel.WriteBadRequest(w, badRequest)
		return
	}
	limitValue, hasLimit := integerQueryValue(values.Get("limit"))
	limit := optionLimitValue(limitValue, hasLimit)
	options, err := store.ListOptions(r.Context(), strings.TrimSpace(values.Get("keyword")), limit, selectedIds)
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	kernel.WriteOK(w, options, "")
}

// parseSelectedProxyOptionIds mirrors parseSelectedProxyOptionIds: only the
// selectedIds / selectedIds[] keys are accepted, scalar strings without
// commas, deduped, sorted, max 20.
func parseSelectedProxyOptionIds(values url.Values) ([]string, string) {
	for key := range values {
		if key == "selectedIds" || key == "selectedIds[]" {
			continue
		}
		if strings.HasPrefix(key, "selectedIds[") {
			return nil, "代理选项 selectedIds 无效"
		}
	}
	rawValues := []string{}
	rawValues = append(rawValues, values["selectedIds"]...)
	rawValues = append(rawValues, values["selectedIds[]"]...)
	if len(rawValues) == 0 {
		return nil, ""
	}
	selectedIds := []string{}
	seen := map[string]bool{}
	for _, value := range rawValues {
		text := strings.TrimSpace(value)
		if text == "" {
			continue
		}
		if strings.Contains(text, ",") || strings.HasPrefix(text, "[") || len(text) > 120 {
			return nil, "代理选项 selectedIds 无效"
		}
		if seen[text] {
			continue
		}
		seen[text] = true
		selectedIds = append(selectedIds, text)
	}
	sort.Strings(selectedIds)
	if len(selectedIds) > 20 {
		return nil, "代理选项 selectedIds 最多 20 个"
	}
	return selectedIds, ""
}

// optionLimitValue mirrors optionLimitValue (proxies.routes.ts:123-125):
// present integer -> clamp 1..50, otherwise 50. Non-integer inputs ("abc",
// "1.8") resolve to absent upstream and fall back to 50.
func optionLimitValue(value float64, present bool) int {
	if !present {
		return 50
	}
	if value > 50 {
		return 50
	}
	if value < 1 {
		return 1
	}
	return int(value)
}

// integerQueryValue mirrors Node integerQueryValue (shared/query-values.ts:21-30):
// trim; empty or a non-integer Number() result is absent. Number() accepts the
// ECMAScript decimal grammar, 0x/0o/0b radix literals ("1e2" is 100) and the
// exact "Infinity" spelling; everything else is NaN.
func integerQueryValue(raw string) (float64, bool) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return 0, false
	}
	number, ok := jsStringToNumber(text)
	if !ok {
		return 0, false
	}
	if math.IsNaN(number) || math.IsInf(number, 0) || number != math.Trunc(number) {
		return 0, false
	}
	return number, true
}

func jsStringToNumber(text string) (float64, bool) {
	body := text
	negative := false
	switch body[0] {
	case '+':
		body = body[1:]
	case '-':
		negative = true
		body = body[1:]
	}
	if body == "" {
		return 0, false
	}
	if body == "Infinity" {
		if negative {
			return math.Inf(-1), true
		}
		return math.Inf(1), true
	}
	// Radix literals are unsigned: Number("-0x10") is NaN.
	if len(body) > 2 && body[0] == '0' {
		var base int
		switch body[1] {
		case 'x', 'X':
			base = 16
		case 'o', 'O':
			base = 8
		case 'b', 'B':
			base = 2
		}
		if base != 0 {
			if negative {
				return 0, false
			}
			parsed, exact := new(big.Int).SetString(body[2:], base)
			if !exact {
				return 0, false
			}
			value, _ := new(big.Float).SetInt(parsed).Float64()
			return value, true
		}
	}
	if !isDecimalNumericLiteral(body) {
		return 0, false
	}
	value, err := strconv.ParseFloat(body, 64)
	if err != nil {
		// ErrRange overflows to ±Inf (not an integer) or underflows to zero
		// (Number("1e-400") is the integer 0).
		if errors.Is(err, strconv.ErrRange) && value == 0 {
			return 0, true
		}
		return 0, false
	}
	if negative {
		value = -value
	}
	return value, true
}

// isDecimalNumericLiteral gates strconv.ParseFloat onto the ECMAScript decimal
// string grammar; ParseFloat alone would also accept hex-float and the
// case-insensitive inf/nan spellings that JS Number() rejects.
func isDecimalNumericLiteral(text string) bool {
	index := 0
	digits := func() bool {
		start := index
		for index < len(text) && text[index] >= '0' && text[index] <= '9' {
			index++
		}
		return index > start
	}
	intPart := digits()
	fractionPart := false
	if index < len(text) && text[index] == '.' {
		index++
		fractionPart = digits()
	}
	if !intPart && !fractionPart {
		return false
	}
	if index < len(text) && (text[index] == 'e' || text[index] == 'E') {
		index++
		if index < len(text) && (text[index] == '+' || text[index] == '-') {
			index++
		}
		if !digits() {
			return false
		}
	}
	return index == len(text)
}

// intFromQuery adapts an integerQueryValue result onto the int-typed store
// inputs: absent -> fallback; present values saturate (the store clamps page
// and pageSize ranges itself).
func intFromQuery(value float64, present bool, fallback int) int {
	if !present {
		return fallback
	}
	if value > float64(math.MaxInt) {
		return math.MaxInt
	}
	if value < float64(math.MinInt) {
		return math.MinInt
	}
	return int(value)
}

// ---------------------------------------------------------------------------
// GET / (paged list)
// ---------------------------------------------------------------------------

func listHandler(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		values := r.URL.Query()
		page, hasPage := integerQueryValue(values.Get("page"))
		pageSize, hasPageSize := integerQueryValue(values.Get("pageSize"))
		result, err := store.ListPage(r.Context(), intFromQuery(page, hasPage, 1), intFromQuery(pageSize, hasPageSize, 20), strings.TrimSpace(values.Get("keyword")))
		if err != nil {
			kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		kernel.WriteOK(w, result, "")
	}
}

// ---------------------------------------------------------------------------
// POST /
// ---------------------------------------------------------------------------

func createHandler(w http.ResponseWriter, r *http.Request, store *Store, sink authsys.OperationLogSink) {
	auth := authsys.AuthContextFrom(r)
	if auth == nil {
		kernel.WriteError(w, http.StatusUnauthorized, "请先登录")
		return
	}
	var body map[string]any
	if !kernel.DecodeJSON(w, r, &body) {
		return
	}
	input, badRequest := parseProxyInput(body, false)
	if badRequest != "" {
		kernel.WriteBadRequest(w, badRequest)
		return
	}
	profile, err := store.Create(r.Context(), input, auth.SystemAccountID)
	if err != nil {
		writeMutationError(w, err, "创建代理失败")
		return
	}
	if sink != nil {
		sink.Record(authsys.OperationLogEntry{
			ActorSystemAccountID: auth.SystemAccountID,
			ActorUsername:        auth.Username,
			ActorDisplayName:     auth.DisplayName,
			ActorRole:            auth.Role,
			Mode:                 "admin",
			Module:               "proxies",
			Action:               "create",
			OperationKey:         "proxies.create",
			ResourceType:         "proxy",
			ResourceID:           profile.ID,
			ResourceName:         profile.Name,
			Summary:              "创建代理：" + profile.Name,
			VisibilityScope:      "admin_only",
			Changes: []authsys.OperationLogChange{
				safeChange("name", "名称", nil, profile.Name),
				safeChange("type", "类型", nil, profile.Type),
				safeChange("host", "主机", nil, profile.Host),
				safeChange("port", "端口", nil, profile.Port),
				safeChange("username", "用户名", nil, profile.Username),
				safeChange("password", "密码", nil, textOrNil(input.Password)),
				safeChange("enabled", "启用状态", nil, profile.Enabled),
			},
		}, r)
	}
	w.Header().Set("Cache-Control", "no-store")
	kernel.WriteJSON(w, http.StatusCreated, map[string]any{"data": profile})
}

// ---------------------------------------------------------------------------
// PATCH /{id}
// ---------------------------------------------------------------------------

func patchHandler(w http.ResponseWriter, r *http.Request, store *Store, sink authsys.OperationLogSink) {
	auth := authsys.AuthContextFrom(r)
	if auth == nil {
		kernel.WriteError(w, http.StatusUnauthorized, "请先登录")
		return
	}
	var body map[string]any
	if !kernel.DecodeJSON(w, r, &body) {
		return
	}
	input, badRequest := parseProxyInput(body, true)
	if badRequest != "" {
		kernel.WriteBadRequest(w, badRequest)
		return
	}
	outcome, err := store.Patch(r.Context(), r.PathValue("id"), input)
	if err != nil {
		writeMutationError(w, err, "更新代理失败")
		return
	}
	if outcome == nil {
		kernel.WriteJSON(w, http.StatusNotFound, map[string]string{"message": "代理不存在"})
		return
	}
	if !outcome.Mutation.Changed {
		kernel.WriteOK(w, outcome.Mutation, "")
		return
	}
	if sink != nil {
		changes := diffSafeChanges(outcome.Before, outcome.After)
		if outcome.PasswordChanged {
			changes = append(changes, safeChange("password", "密码", nil, textOrNil(input.Password)))
		}
		sink.Record(authsys.OperationLogEntry{
			ActorSystemAccountID: auth.SystemAccountID,
			ActorUsername:        auth.Username,
			ActorDisplayName:     auth.DisplayName,
			ActorRole:            auth.Role,
			Mode:                 "admin",
			Module:               "proxies",
			Action:               "update",
			OperationKey:         "proxies.update",
			ResourceType:         "proxy",
			ResourceID:           outcome.Mutation.ID,
			ResourceName:         outcome.Name,
			Summary:              "更新代理：" + outcome.Name,
			VisibilityScope:      "admin_only",
			Changes:              changes,
		}, r)
	}
	kernel.WriteOK(w, outcome.Mutation, "")
}

// diffSafeChanges mirrors diffSafeFields for the proxy label map.
func diffSafeChanges(before, after map[string]any) []authsys.OperationLogChange {
	labels := map[string]string{
		"name": "名称", "description": "说明", "type": "类型", "host": "主机",
		"port": "端口", "username": "用户名", "enabled": "启用状态",
	}
	changes := []authsys.OperationLogChange{}
	for field, label := range labels {
		beforeValue := before[field]
		afterValue := after[field]
		if comparableValue(beforeValue) == comparableValue(afterValue) {
			continue
		}
		changes = append(changes, safeChange(field, label, beforeValue, afterValue))
	}
	return changes
}

// comparableValue mirrors operationLogComparableValue (JSON text identity).
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

// ---------------------------------------------------------------------------
// DELETE /{id}
// ---------------------------------------------------------------------------

func deleteHandler(w http.ResponseWriter, r *http.Request, store *Store, sink authsys.OperationLogSink) {
	auth := authsys.AuthContextFrom(r)
	if auth == nil {
		kernel.WriteError(w, http.StatusUnauthorized, "请先登录")
		return
	}
	id := r.PathValue("id")
	name, err := store.Delete(r.Context(), id)
	if err != nil {
		writeMutationError(w, err, "删除代理失败")
		return
	}
	if name == "" {
		kernel.WriteJSON(w, http.StatusNotFound, map[string]string{"message": "代理不存在"})
		return
	}
	if sink != nil {
		sink.Record(authsys.OperationLogEntry{
			ActorSystemAccountID: auth.SystemAccountID,
			ActorUsername:        auth.Username,
			ActorDisplayName:     auth.DisplayName,
			ActorRole:            auth.Role,
			Mode:                 "admin",
			Module:               "proxies",
			Action:               "delete",
			OperationKey:         "proxies.delete",
			ResourceType:         "proxy",
			ResourceID:           id,
			ResourceName:         name,
			Summary:              "删除代理：" + name,
			VisibilityScope:      "admin_only",
			Changes:              []authsys.OperationLogChange{safeChange("deleted", "删除状态", false, true)},
		}, r)
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Shared helpers.
// ---------------------------------------------------------------------------

// parseProxyInput mirrors the zod proxy schema + partial update schema.
// The strict field set rejects unknown keys with 代理参数无效; the update
// variant additionally requires expectedUpdatedAt and at least one payload
// field (refine) — also rendered as 代理参数无效 like the Node route.
func parseProxyInput(body map[string]any, update bool) (proxyInput, string) {
	input := proxyInput{}
	allowed := map[string]bool{
		"name": true, "description": true, "type": true, "host": true,
		"port": true, "username": true, "password": true, "enabled": true,
		"expectedUpdatedAt": update,
	}
	for key := range body {
		if !allowed[key] {
			return input, "代理参数无效"
		}
	}
	if value, ok := body["name"]; ok && value != nil {
		text, ok := value.(string)
		if !ok {
			return input, "代理参数无效"
		}
		input.Name = &text
	}
	if value, ok := body["description"]; ok {
		if value == nil {
			input.HasDescription = true
		} else if text, isText := value.(string); isText {
			input.HasDescription = true
			input.Description = &text
		} else {
			return input, "代理参数无效"
		}
	}
	if value, ok := body["type"]; ok && value != nil {
		text, isText := value.(string)
		if !isText {
			return input, "代理参数无效"
		}
		input.Type = &text
	}
	if value, ok := body["host"]; ok && value != nil {
		text, isText := value.(string)
		if !isText {
			return input, "代理参数无效"
		}
		input.Host = &text
	}
	if value, ok := body["port"]; ok && value != nil {
		number, isNumber := value.(float64)
		if !isNumber || number != float64(int64(number)) {
			return input, "代理参数无效"
		}
		port := int(number)
		input.Port = &port
	}
	if value, ok := body["username"]; ok && value != nil {
		text, isText := value.(string)
		if !isText {
			return input, "代理参数无效"
		}
		input.HasUsername = true
		input.Username = &text
	}
	if value, ok := body["password"]; ok {
		if value == nil {
			return input, "代理参数无效"
		}
		text, isText := value.(string)
		if !isText {
			return input, "代理参数无效"
		}
		input.HasPassword = true
		input.Password = &text
	}
	if value, ok := body["enabled"]; ok && value != nil {
		enabled, isBool := value.(bool)
		if !isBool {
			return input, "代理参数无效"
		}
		input.Enabled = &enabled
	}
	if update {
		expectedUpdatedAt, ok := body["expectedUpdatedAt"]
		if !ok || expectedUpdatedAt == nil {
			return input, "代理参数无效"
		}
		text, isText := expectedUpdatedAt.(string)
		if !isText || !validRFC3339(text) {
			// Node rfc3339InstantSchema('代理配置版本无效') -> route renders
			// the generic 代理参数无效 message on any schema failure.
			return input, "代理参数无效"
		}
		input.ExpectedUpdatedAt = text
		// refine: at least one non-expectedUpdatedAt key.
		payloadKeys := 0
		for key := range body {
			if key != "expectedUpdatedAt" {
				payloadKeys++
			}
		}
		if payloadKeys == 0 {
			return input, "代理参数无效"
		}
	} else {
		if input.Name == nil || strings.TrimSpace(*input.Name) == "" {
			return input, "代理参数无效"
		}
		if input.Type == nil || input.Host == nil || input.Port == nil {
			return input, "代理参数无效"
		}
	}
	// Any repository-normalizer failure maps onto the same generic message:
	// the zod schema already rejected it in Node before the store ran.
	if err := input.normalize(); err != nil {
		return input, "代理参数无效"
	}
	return input, ""
}

// writeMutationError mirrors the Node catch blocks: 409 for conflicts and
// duplicate names, 400 with the message otherwise.
func writeMutationError(w http.ResponseWriter, err error, fallback string) {
	var conflict interface{ Error() string }
	if errors.Is(err, ErrConflict) {
		kernel.WriteJSON(w, http.StatusConflict, map[string]string{"message": err.Error()})
		return
	}
	var duplicate *DuplicateNameError
	if errors.As(err, &duplicate) {
		kernel.WriteJSON(w, http.StatusConflict, map[string]string{"message": duplicate.Error()})
		return
	}
	var inUse *InUseError
	if errors.As(err, &inUse) {
		kernel.WriteJSON(w, http.StatusConflict, map[string]string{"message": inUse.Error()})
		return
	}
	_ = conflict
	message := fallback
	if err != nil {
		message = err.Error()
	}
	kernel.WriteBadRequest(w, message)
}

func textOrNil(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

func validRFC3339(value string) bool {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return false
	}
	_, err := time.Parse(time.RFC3339Nano, trimmed)
	return err == nil
}

// safeChange mirrors operation-log.service safeChange for the proxy fields
// (password is a sensitive container and never carries material).
func safeChange(field string, label string, before, after any) authsys.OperationLogChange {
	if isSensitiveChangeField(field) {
		entry := authsys.OperationLogChange{Field: field, Label: label, Sensitive: true}
		if before == nil || before == "" {
			entry.Before = "未设置"
		} else {
			entry.Before = "已设置"
		}
		if after == nil || after == "" {
			entry.After = "未设置"
		} else {
			entry.After = "已变更"
		}
		return entry
	}
	return authsys.OperationLogChange{
		Field: field, Label: label,
		Before: normalizeSafeValue(before), After: normalizeSafeValue(after),
	}
}

var sensitiveChangeFields = map[string]bool{
	"credentials": true, "credential": true, "token": true, "key": true,
	"secret": true, "password": true, "apikey": true, "api_key": true,
	"apikeys": true, "api_keys": true, "accesstoken": true, "access_token": true,
	"refreshtoken": true, "refresh_token": true, "idtoken": true, "id_token": true,
	"identitytoken": true, "identity_token": true, "clientsecret": true,
	"client_secret": true, "sessiontoken": true, "session_token": true,
	"proxypassword": true, "proxy_password": true,
}

func isSensitiveChangeField(field string) bool {
	return sensitiveChangeFields[strings.TrimSpace(strings.ToLower(field))]
}

// normalizeSafeValue mirrors normalizeSafeValue: strings truncate at 200,
// structured values serialize truncated.
func normalizeSafeValue(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		const limit = 200
		if len(text) > limit {
			return text[:limit]
		}
		return text
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	const limit = 200
	if len(encoded) > limit {
		return string(encoded[:limit])
	}
	return string(encoded)
}
