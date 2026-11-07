package proxypools

// HTTP surface of the pool family (代理池设计 §8.1，全部 admin：
//
//	GET    /__aisys__/api/proxy-pools/member-options (requireAdmin; 注册在 /{id} 之前)
//	GET    /__aisys__/api/proxy-pools                (requireAdmin)
//	POST   /__aisys__/api/proxy-pools                (requireAdmin + mutation guard proxy-pools.create)
//	GET    /__aisys__/api/proxy-pools/{id}           (requireAdmin)
//	PATCH  /__aisys__/api/proxy-pools/{id}           (requireAdmin)
//	DELETE /__aisys__/api/proxy-pools/{id}           (requireAdmin)
//
// 池全局可见（C6 A）：不按 system_account_id 过滤，该列只作创建者审计。

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

// Mount wires the pool route family. member-options registers before the
// /{id} routes per §8.1 so the literal path is never captured as a pool id.
func Mount(k *kernel.Kernel, deps *authsys.Deps, store *Store, sink authsys.OperationLogSink) {
	prefix := "/__aisys__/api"
	k.Register("GET "+prefix+"/proxy-pools/member-options", deps.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		memberOptionsHandler(w, r, store)
	})))
	k.Register("GET "+prefix+"/proxy-pools", deps.RequireAdmin(http.HandlerFunc(listHandler(store))))
	k.Register("POST "+prefix+"/proxy-pools", deps.RequireAdmin(kernel.MutationGuardMiddleware(kernel.MutationGuardOptions{
		OperationKey: "proxy-pools.create",
		Actor:        actorResolver,
		Fingerprint: func(r *http.Request) (any, error) {
			// 敏感面白名单：池载荷不含凭据，直接结构化指纹。
			return map[string]any{
				"name":        kernel.TextField(kernel.BodyField(r, "name")),
				"description": kernel.TextField(kernel.BodyField(r, "description")),
				"strategy":    kernel.TextField(kernel.BodyField(r, "strategy")),
				"enabled":     kernel.BodyField(r, "enabled"),
				"members":     membersFingerprint(kernel.BodyField(r, "members")),
			}, nil
		},
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		createHandler(w, r, store, sink)
	}))))
	k.Register("GET "+prefix+"/proxy-pools/{id}", deps.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		detailHandler(w, r, store)
	})))
	k.Register("PATCH "+prefix+"/proxy-pools/{id}", deps.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		patchHandler(w, r, store, sink)
	})))
	k.Register("DELETE "+prefix+"/proxy-pools/{id}", deps.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		deleteHandler(w, r, store, sink)
	})))
}

func actorResolver(r *http.Request) string {
	if auth := authsys.AuthContextFrom(r); auth != nil {
		return auth.SystemAccountID
	}
	return "anonymous"
}

// membersFingerprint renders the members array as a stable fingerprint:
// sorted profile ids with their weight/enabled spec (order matters for
// priority, so the ordered list is preserved).
func membersFingerprint(value any) []map[string]any {
	list, ok := value.([]any)
	if !ok {
		return []map[string]any{}
	}
	out := make([]map[string]any, 0, len(list))
	for _, item := range list {
		entry, ok := item.(map[string]any)
		if !ok {
			out = append(out, map[string]any{"proxyProfileId": ""})
			continue
		}
		fingerprint := map[string]any{}
		fingerprint["proxyProfileId"] = kernel.TextField(entry["proxyProfileId"])
		fingerprint["weight"] = entry["weight"]
		fingerprint["enabled"] = entry["enabled"]
		out = append(out, fingerprint)
	}
	return out
}

// ---------------------------------------------------------------------------
// GET /member-options
// ---------------------------------------------------------------------------

func memberOptionsHandler(w http.ResponseWriter, r *http.Request, store *Store) {
	values := r.URL.Query()
	selectedIds, badRequest := parseSelectedMemberOptionIds(values)
	if badRequest != "" {
		kernel.WriteBadRequest(w, badRequest)
		return
	}
	limitValue, hasLimit := integerQueryValue(values.Get("limit"))
	limit := optionLimitValue(limitValue, hasLimit)
	options, err := store.ListMemberOptions(r.Context(), strings.TrimSpace(values.Get("keyword")), limit, selectedIds)
	if err != nil {
		kernel.WriteErrorCause(r, w, http.StatusInternalServerError, "服务器内部错误", err)
		return
	}
	kernel.WriteOK(w, options, "")
}

// parseSelectedMemberOptionIds mirrors parseSelectedProxyOptionIds
// (proxyprofiles): only selectedIds / selectedIds[] keys, scalar strings
// without commas, deduped, sorted, max 20.
func parseSelectedMemberOptionIds(values url.Values) ([]string, string) {
	for key := range values {
		if key == "selectedIds" || key == "selectedIds[]" {
			continue
		}
		if strings.HasPrefix(key, "selectedIds[") {
			return nil, "代理池成员选项 selectedIds 无效"
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
			return nil, "代理池成员选项 selectedIds 无效"
		}
		if seen[text] {
			continue
		}
		seen[text] = true
		selectedIds = append(selectedIds, text)
	}
	sort.Strings(selectedIds)
	if len(selectedIds) > 20 {
		return nil, "代理池成员选项 selectedIds 最多 20 个"
	}
	return selectedIds, ""
}

// optionLimitValue mirrors optionLimitValue (proxyprofiles): present integer
// -> clamp 1..50, otherwise 50.
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

// integerQueryValue mirrors Node integerQueryValue (shared/query-values.ts)
// byte for byte — see proxyprofiles/routes.go for the grammar notes.
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
// inputs (page/pageSize); the store clamps ranges itself.
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
			kernel.WriteErrorCause(r, w, http.StatusInternalServerError, "服务器内部错误", err)
			return
		}
		kernel.WriteOK(w, result, "")
	}
}

// ---------------------------------------------------------------------------
// GET /{id}
// ---------------------------------------------------------------------------

func detailHandler(w http.ResponseWriter, r *http.Request, store *Store) {
	detail, err := store.GetDetail(r.Context(), r.PathValue("id"))
	if err != nil {
		kernel.WriteErrorCause(r, w, http.StatusInternalServerError, "服务器内部错误", err)
		return
	}
	if detail == nil {
		kernel.WriteJSON(w, http.StatusNotFound, map[string]string{"message": "代理池不存在"})
		return
	}
	kernel.WriteOK(w, detail, "")
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
	input, badRequest := parseCreateInput(body)
	if badRequest != "" {
		kernel.WriteBadRequest(w, badRequest)
		return
	}
	detail, err := store.Create(r.Context(), input, auth.SystemAccountID)
	if err != nil {
		writeMutationError(w, err, "创建代理池失败")
		return
	}
	if sink != nil {
		sink.Record(authsys.OperationLogEntry{
			ActorSystemAccountID: auth.SystemAccountID,
			ActorUsername:        auth.Username,
			ActorDisplayName:     auth.DisplayName,
			ActorRole:            auth.Role,
			Mode:                 "admin",
			Module:               "proxy-pools",
			Action:               "create",
			OperationKey:         "proxy-pools.create",
			ResourceType:         "proxy_pool",
			ResourceID:           detail.ID,
			ResourceName:         detail.Name,
			Summary:              "创建代理池：" + detail.Name,
			VisibilityScope:      "admin_only",
			Changes: []authsys.OperationLogChange{
				safeChange("name", "名称", nil, detail.Name),
				safeChange("description", "说明", nil, detail.Description),
				safeChange("strategy", "策略", nil, detail.Strategy),
				safeChange("enabled", "启用状态", nil, detail.Enabled),
				safeChange("members", "成员", nil, memberCountText(len(detail.Members))),
			},
		}, r)
	}
	w.Header().Set("Cache-Control", "no-store")
	kernel.WriteJSON(w, http.StatusCreated, map[string]any{"data": detail})
}

func memberCountText(count int) string {
	return "成员 " + strconv.Itoa(count)
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
	input, badRequest := parsePatchInput(body)
	if badRequest != "" {
		kernel.WriteBadRequest(w, badRequest)
		return
	}
	outcome, err := store.Patch(r.Context(), r.PathValue("id"), input)
	if err != nil {
		writeMutationError(w, err, "更新代理池失败")
		return
	}
	if outcome == nil {
		kernel.WriteJSON(w, http.StatusNotFound, map[string]string{"message": "代理池不存在"})
		return
	}
	if !outcome.Changed {
		kernel.WriteJSON(w, http.StatusOK, map[string]any{
			"data": map[string]any{
				"id":        outcome.ID,
				"updatedAt": outcome.UpdatedAt,
				"changed":   false,
				"values":    map[string]any{},
			},
		})
		return
	}
	if sink != nil {
		changes := diffSafeChanges(outcome.Before, outcome.After)
		if outcome.MembersSummary != "" {
			// 操作日志保留完整成员变更摘要（设计 §8.1：新增/移除数量），
			// 不只记录变更前后总数。
			changes = append(changes, safeChange("members", "成员", outcome.Before["members"], outcome.MembersSummary))
		}
		sink.Record(authsys.OperationLogEntry{
			ActorSystemAccountID: auth.SystemAccountID,
			ActorUsername:        auth.Username,
			ActorDisplayName:     auth.DisplayName,
			ActorRole:            auth.Role,
			Mode:                 "admin",
			Module:               "proxy-pools",
			Action:               "update",
			OperationKey:         "proxy-pools.update",
			ResourceType:         "proxy_pool",
			ResourceID:           outcome.ID,
			ResourceName:         outcome.Name,
			Summary:              "更新代理池：" + outcome.Name,
			VisibilityScope:      "admin_only",
			Changes:              changes,
		}, r)
	}
	kernel.WriteJSON(w, http.StatusOK, map[string]any{
		"data": map[string]any{
			"id":        outcome.ID,
			"updatedAt": outcome.UpdatedAt,
			"changed":   true,
			"values":    outcome.Values,
		},
	})
}

// diffSafeChanges mirrors diffSafeChanges for the pool label map.
func diffSafeChanges(before, after map[string]any) []authsys.OperationLogChange {
	labels := map[string]string{
		"name": "名称", "description": "说明", "strategy": "策略", "enabled": "启用状态",
	}
	changes := []authsys.OperationLogChange{}
	for field, label := range labels {
		beforeValue, hasBefore := before[field]
		afterValue, hasAfter := after[field]
		if !hasBefore || !hasAfter {
			continue
		}
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
	name, _, err := store.Delete(r.Context(), id)
	if err != nil {
		writeMutationError(w, err, "删除代理池失败")
		return
	}
	if name == "" {
		kernel.WriteJSON(w, http.StatusNotFound, map[string]string{"message": "代理池不存在"})
		return
	}
	if sink != nil {
		sink.Record(authsys.OperationLogEntry{
			ActorSystemAccountID: auth.SystemAccountID,
			ActorUsername:        auth.Username,
			ActorDisplayName:     auth.DisplayName,
			ActorRole:            auth.Role,
			Mode:                 "admin",
			Module:               "proxy-pools",
			Action:               "delete",
			OperationKey:         "proxy-pools.delete",
			ResourceType:         "proxy_pool",
			ResourceID:           id,
			ResourceName:         name,
			Summary:              "删除代理池：" + name,
			VisibilityScope:      "admin_only",
			Changes:              []authsys.OperationLogChange{safeChange("deleted", "删除状态", false, true)},
		}, r)
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Input parsing (strict field sets, mirror proxyprofiles parseProxyInput).
// ---------------------------------------------------------------------------

// parseCreateInput mirrors the strict create whitelist: name/description/
// strategy/enabled/members only; name/strategy/members required; strategy
// restricted to round_robin/weighted; members may be an empty array (§8.6
// proxy_pool_empty only gates account binding, not pool structure).
func parseCreateInput(body map[string]any) (createInput, string) {
	input := createInput{}
	allowed := map[string]bool{
		"name": true, "description": true, "strategy": true, "enabled": true, "members": true,
	}
	for key := range body {
		if !allowed[key] {
			return input, "代理池参数无效"
		}
	}
	name, ok := body["name"]
	if !ok || name == nil {
		return input, "代理池参数无效"
	}
	nameText, isText := name.(string)
	if !isText {
		return input, "代理池参数无效"
	}
	input.Name = strings.TrimSpace(nameText)
	if input.Name == "" {
		return input, "代理池参数无效"
	}
	strategy, ok := body["strategy"]
	if !ok || strategy == nil {
		return input, "代理池参数无效"
	}
	strategyText, isText := strategy.(string)
	if !isText {
		return input, "代理池参数无效"
	}
	input.Strategy = strategyText
	if input.Strategy != StrategyRoundRobin && input.Strategy != StrategyWeighted {
		return input, "代理池参数无效"
	}
	if value, ok := body["description"]; ok {
		if value == nil {
			input.Description = nil
		} else if text, isText := value.(string); isText {
			trimmed := strings.TrimSpace(text)
			if len([]rune(trimmed)) > 200 {
				return input, "代理池参数无效"
			}
			input.Description = &trimmed
		} else {
			return input, "代理池参数无效"
		}
	}
	// enabled 缺省 true（与代理创建的 zod default 口径一致）。
	input.Enabled = true
	if value, ok := body["enabled"]; ok && value != nil {
		enabled, isBool := value.(bool)
		if !isBool {
			return input, "代理池参数无效"
		}
		input.Enabled = enabled
	}
	membersValue, ok := body["members"]
	if !ok {
		return input, "代理池参数无效"
	}
	members, badRequest := parseMemberInputs(membersValue)
	if badRequest != "" {
		return input, badRequest
	}
	input.Members = members
	return input, ""
}

// parsePatchInput mirrors the strict update whitelist plus expectedUpdatedAt
// and the refine rule (at least one payload key).
func parsePatchInput(body map[string]any) (patchInput, string) {
	input := patchInput{}
	allowed := map[string]bool{
		"name": true, "description": true, "strategy": true, "enabled": true,
		"members": true, "expectedUpdatedAt": true,
	}
	for key := range body {
		if !allowed[key] {
			return input, "代理池参数无效"
		}
	}
	expected, ok := body["expectedUpdatedAt"]
	if !ok || expected == nil {
		return input, "代理池参数无效"
	}
	expectedText, isText := expected.(string)
	if !isText || !validRFC3339(expectedText) {
		return input, "代理池参数无效"
	}
	input.ExpectedUpdatedAt = expectedText
	payloadKeys := 0
	for key := range body {
		if key != "expectedUpdatedAt" {
			payloadKeys++
		}
	}
	if payloadKeys == 0 {
		return input, "代理池参数无效"
	}
	if value, ok := body["name"]; ok && value != nil {
		text, isText := value.(string)
		if !isText {
			return input, "代理池参数无效"
		}
		trimmed := strings.TrimSpace(text)
		if trimmed == "" {
			return input, "代理池参数无效"
		}
		input.Name = &trimmed
	}
	if value, ok := body["description"]; ok {
		if value == nil {
			input.HasDescription = true
		} else if text, isText := value.(string); isText {
			trimmed := strings.TrimSpace(text)
			if len([]rune(trimmed)) > 200 {
				return input, "代理池参数无效"
			}
			input.HasDescription = true
			input.Description = &trimmed
		} else {
			return input, "代理池参数无效"
		}
	}
	if value, ok := body["strategy"]; ok && value != nil {
		text, isText := value.(string)
		if !isText || (text != StrategyRoundRobin && text != StrategyWeighted) {
			return input, "代理池参数无效"
		}
		input.Strategy = &text
	}
	if value, ok := body["enabled"]; ok && value != nil {
		enabled, isBool := value.(bool)
		if !isBool {
			return input, "代理池参数无效"
		}
		input.Enabled = &enabled
	}
	if value, ok := body["members"]; ok {
		members, badRequest := parseMemberInputs(value)
		if badRequest != "" {
			return input, badRequest
		}
		input.HasMembers = true
		input.Members = members
	}
	return input, ""
}

// parseMemberInputs validates the members payload shape: array of strict
// {proxyProfileId, weight?, enabled?} objects.
func parseMemberInputs(value any) ([]memberInput, string) {
	list, isList := value.([]any)
	if !isList {
		return nil, "代理池参数无效"
	}
	members := make([]memberInput, 0, len(list))
	for _, item := range list {
		entry, ok := item.(map[string]any)
		if !ok {
			return nil, "代理池参数无效"
		}
		for key := range entry {
			switch key {
			case "proxyProfileId", "weight", "enabled":
			default:
				return nil, "代理池参数无效"
			}
		}
		rawID, ok := entry["proxyProfileId"]
		if !ok || rawID == nil {
			return nil, "代理池参数无效"
		}
		idText, isText := rawID.(string)
		if !isText || strings.TrimSpace(idText) == "" {
			return nil, "代理池参数无效"
		}
		member := memberInput{ProxyProfileID: strings.TrimSpace(idText), Weight: 1, Enabled: true}
		if rawWeight, ok := entry["weight"]; ok && rawWeight != nil {
			number, isNumber := rawWeight.(float64)
			if !isNumber || number != float64(int64(number)) {
				return nil, "代理池参数无效"
			}
			member.Weight = int(number)
		}
		if rawEnabled, ok := entry["enabled"]; ok && rawEnabled != nil {
			enabled, isBool := rawEnabled.(bool)
			if !isBool {
				return nil, "代理池参数无效"
			}
			member.Enabled = enabled
		}
		members = append(members, member)
	}
	return members, ""
}

func validRFC3339(value string) bool {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return false
	}
	_, err := time.Parse(time.RFC3339Nano, trimmed)
	return err == nil
}

// ---------------------------------------------------------------------------
// Error rendering (代理池设计 §8.6 code table).
// ---------------------------------------------------------------------------

// writeMutationError maps typed store failures onto the §8.6 envelopes:
// 409 for the revision/bound-account/duplicate-name conflicts, 400 with the
// §8.6 code for member validation failures, 400 with the message otherwise.
func writeMutationError(w http.ResponseWriter, err error, fallback string) {
	if errors.Is(err, ErrRevisionConflict) {
		kernel.WriteJSON(w, http.StatusConflict, map[string]string{
			"code":    "proxy_pool_revision_conflict",
			"message": ErrRevisionConflict.Error(),
		})
		return
	}
	var duplicate *PoolNameDuplicateError
	if errors.As(err, &duplicate) {
		kernel.WriteJSON(w, http.StatusConflict, map[string]string{"message": duplicate.Error()})
		return
	}
	var bound *PoolBoundAccountsError
	if errors.As(err, &bound) {
		kernel.WriteJSON(w, http.StatusConflict, map[string]string{
			"code":    bound.ErrorCode(),
			"message": bound.Error(),
		})
		return
	}
	var coder errorCoder
	if errors.As(err, &coder) {
		kernel.WriteJSON(w, http.StatusBadRequest, map[string]string{
			"code":    coder.ErrorCode(),
			"message": err.Error(),
		})
		return
	}
	if errors.Is(err, ErrNameRequired) || errors.Is(err, ErrStrategyInvalid) || errors.Is(err, ErrEnabledInvalid) || errors.Is(err, ErrDescriptionInvalid) {
		kernel.WriteBadRequest(w, err.Error())
		return
	}
	message := fallback
	if err != nil {
		message = err.Error()
	}
	kernel.WriteBadRequest(w, message)
}

// ---------------------------------------------------------------------------
// Operation-log helpers (池 CRUD 记录池名/策略/成员摘要；不含 host/凭据).
// ---------------------------------------------------------------------------

// safeChange mirrors operation-log.service safeChange; pool payloads carry no
// sensitive field, so the sensitive branch stays unused here.
func safeChange(field string, label string, before, after any) authsys.OperationLogChange {
	return authsys.OperationLogChange{
		Field:  field,
		Label:  label,
		Before: normalizeSafeValue(before),
		After:  normalizeSafeValue(after),
	}
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
