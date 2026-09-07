package announcements

import (
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

// Mount wires the announcement route family (announcements.routes.ts):
//
//   - GET  /announcements/public        (authed, no admin gate)
//   - GET  /announcements/public/{id}   (authed, no admin gate)
//   - POST /announcements/public/read   (authed, no admin gate)
//   - GET|POST|PATCH|DELETE /announcements[...] (requireAdmin)
//
// Cache/page invalidation side effects: the Node baseline removed
// publishAnnouncementPublicChange (commit d8c5039eb, 2026-07-23) and the Go
// inval bus carries no announcement topic or subscriber, so this family
// deliberately performs no cache invalidation, mirroring the baseline.
func Mount(k *kernel.Kernel, deps *authsys.Deps, store *Store, sink authsys.OperationLogSink) {
	prefix := "/__aisys__/api"

	// Public surface, Node contract. The whole /announcements family sits
	// behind requireAuth (system-api-app.ts `app.use(systemApiPrefix,
	// requireAuth)`); the three /announcements/public* routes add no admin
	// gate. Go 1.22 ServeMux routes the literal /announcements/public ahead
	// of the older admin-gated /announcements/{id}, so normal users reach
	// the public projection without touching the admin gate.
	k.Register("GET "+prefix+"/announcements/public", deps.RequireSession(false)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := authsys.AuthContextFrom(r)
		if auth == nil {
			kernel.WriteError(w, http.StatusUnauthorized, "请先登录")
			return
		}
		// publicListQuerySchema: z.coerce.number().int().min(1).max(30)
		// .optional() — 0/negative/non-numeric/fractional/>30 and repeated
		// keys all render 400 before the repository runs; a missing limit
		// defaults to 30.
		limit, ok := queryIntOption(r.URL.Query(), "limit", 1, publicLimit)
		if !ok {
			kernel.WriteBadRequest(w, "公告查询参数无效")
			return
		}
		items, err := store.ListPublic(r.Context(), auth.SystemAccountID, intValue(limit, publicLimit))
		if err != nil {
			kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		kernel.WriteOK(w, items, "")
	})))
	k.Register("POST "+prefix+"/announcements/public/read", deps.RequireSession(false)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := authsys.AuthContextFrom(r)
		if auth == nil {
			kernel.WriteError(w, http.StatusUnauthorized, "请先登录")
			return
		}
		ids, ok := readAnnouncementIDs(w, r)
		if !ok {
			return
		}
		result, err := store.MarkRead(r.Context(), auth.SystemAccountID, ids)
		if err != nil {
			kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		kernel.WriteOK(w, result, "")
	})))
	k.Register("GET "+prefix+"/announcements/public/{id}", deps.RequireSession(false)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := authsys.AuthContextFrom(r)
		if auth == nil {
			kernel.WriteError(w, http.StatusUnauthorized, "请先登录")
			return
		}
		detail, err := store.FindPublic(r.Context(), r.PathValue("id"))
		if err != nil {
			kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		if detail == nil {
			kernel.WriteError(w, http.StatusNotFound, "公告不存在")
			return
		}
		kernel.WriteOK(w, detail, "")
	})))

	// Go-introduced alternate public paths (no Node counterpart). The Node
	// contract /announcements/public* routes above now cover the same
	// surface and the frontend only calls those; these remain registered for
	// compatibility with existing callers and keep their historical lenient
	// limit/read parsing. Do not extend the lenient parsing to the
	// Node-contract routes.
	k.Register("GET "+prefix+"/my-announcements", deps.RequireSession(false)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := authsys.AuthContextFrom(r)
		if auth == nil {
			kernel.WriteError(w, http.StatusUnauthorized, "请先登录")
			return
		}
		limit := parseIntOr(r.URL.Query().Get("limit"), 0)
		items, err := store.ListPublic(r.Context(), auth.SystemAccountID, limit)
		if err != nil {
			kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		kernel.WriteOK(w, items, "")
	})))
	k.Register("POST "+prefix+"/my-announcements/read", deps.RequireSession(false)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := authsys.AuthContextFrom(r)
		if auth == nil {
			kernel.WriteError(w, http.StatusUnauthorized, "请先登录")
			return
		}
		var body struct {
			AnnouncementIDs []string `json:"announcementIds"`
		}
		if !kernel.DecodeJSON(w, r, &body) {
			return
		}
		if len(body.AnnouncementIDs) > 30 {
			kernel.WriteBadRequest(w, "公告已读参数无效")
			return
		}
		result, err := store.MarkRead(r.Context(), auth.SystemAccountID, body.AnnouncementIDs)
		if err != nil {
			kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		kernel.WriteOK(w, result, "")
	})))

	// Admin surface.
	k.Register("GET "+prefix+"/announcements", deps.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// adminListQuerySchema: page/pageSize are optional coerced integers
		// with page >= 1 and 1 <= pageSize <= 100; violations render 400
		// before the repository runs. Omitted pageSize defaults to 50 inside
		// the store, and total/hasMore/page/pageSize are the normalized
		// values (listAnnouncementsPageAsync).
		query := r.URL.Query()
		page, ok := queryIntOption(query, "page", 1, math.MaxInt)
		if !ok {
			kernel.WriteBadRequest(w, "公告查询参数无效")
			return
		}
		pageSize, ok := queryIntOption(query, "pageSize", 1, maxAnnouncementPageSize)
		if !ok {
			kernel.WriteBadRequest(w, "公告查询参数无效")
			return
		}
		result, err := store.ListPage(r.Context(), page, pageSize)
		if err != nil {
			kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		kernel.WriteOK(w, result, "")
	})))
	k.Register("GET "+prefix+"/announcements/{id}", deps.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		detail, err := store.FindEditDetail(r.Context(), r.PathValue("id"))
		if err != nil {
			kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		if detail == nil {
			kernel.WriteError(w, http.StatusNotFound, "公告不存在")
			return
		}
		kernel.WriteOK(w, detail, "")
	})))
	k.Register("POST "+prefix+"/announcements", mountGuardedCreate(deps, store, sink))
	k.Register("PATCH "+prefix+"/announcements/{id}", deps.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		announcementUpdate(w, r, store, sink)
	})))
	k.Register("POST "+prefix+"/announcements/{id}/publish", deps.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		announcementVersionAction(w, r, store, sink, "publish", "published")
	})))
	k.Register("POST "+prefix+"/announcements/{id}/unpublish", deps.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		announcementVersionAction(w, r, store, sink, "unpublish", "archived")
	})))
	k.Register("DELETE "+prefix+"/announcements/{id}", deps.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		announcementDelete(w, r, store, sink)
	})))
}

func parseIntOr(raw string, fallback int) int {
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 {
		return fallback
	}
	return value
}

// queryIntOption mirrors z.coerce.number().int().min(min).max(max)
// .optional() on a single-value query parameter: an absent key is optional,
// anything present must coerce like JavaScript Number() (scientific
// notation and radix-prefixed integers included) and land within
// [min, max] as an integer. Repeated keys fail like the array coercion in
// Node (Number([...multi]) → NaN).
func queryIntOption(query url.Values, key string, min, max int) (*int, bool) {
	values, present := query[key]
	if !present {
		return nil, true
	}
	if len(values) != 1 {
		return nil, false
	}
	value, ok := coercedInt(values[0])
	if !ok || value < min || value > max {
		return nil, false
	}
	return &value, true
}

// coercedInt mirrors z.coerce.number().int(): JavaScript Number() semantics
// ("1e1" → 10, "0x10" → 16, "" → 0, "10abc" → NaN) plus the integer check.
func coercedInt(raw string) (int, bool) {
	value, ok := jsNumber(raw)
	if !ok || math.IsInf(value, 0) || value != math.Trunc(value) {
		return 0, false
	}
	return int(value), true
}

// jsNumber mirrors JavaScript Number() for string coercion. Go literal
// underscores never occur in JavaScript numeric strings and are rejected.
func jsNumber(raw string) (float64, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return 0, true // Number("") === 0
	}
	if strings.Contains(trimmed, "_") {
		return 0, false
	}
	if value, err := strconv.ParseFloat(trimmed, 64); err == nil {
		return value, true
	}
	if value, err := strconv.ParseInt(trimmed, 0, 64); err == nil {
		return float64(value), true
	}
	return 0, false
}

func intValue(value *int, fallback int) int {
	if value == nil {
		return fallback
	}
	return *value
}

// bodyObject decodes the JSON body and returns it as an object. false means
// the response was already written (malformed JSON via DecodeJSON, or a
// body that is not a JSON object, which renders the route-specific 400 like
// the failing strict zod schema). An absent/empty body decodes as nil and
// fails the object check, mirroring the Express `req.body` default that
// fails every strict schema.
func bodyObject(w http.ResponseWriter, r *http.Request, invalidMessage string) (map[string]any, bool) {
	var payload any
	if !kernel.DecodeJSON(w, r, &payload) {
		return nil, false
	}
	body, isObject := payload.(map[string]any)
	if !isObject {
		kernel.WriteBadRequest(w, invalidMessage)
		return nil, false
	}
	return body, true
}

// hasUnknownField mirrors zod .strict(): any key outside allowed fails.
func hasUnknownField(body map[string]any, allowed ...string) bool {
	for key := range body {
		known := false
		for _, name := range allowed {
			if key == name {
				known = true
				break
			}
		}
		if !known {
			return true
		}
	}
	return false
}

// requiredTextField mirrors z.string().trim().min(1).max(units): the key
// must exist and carry a string whose trimmed value spans 1..units UTF-16
// code units (JavaScript String.prototype.length), returning the trimmed
// value.
func requiredTextField(body map[string]any, key string, units int) (string, bool) {
	value, exists := body[key]
	if !exists {
		return "", false
	}
	return boundedTrimmedText(value, units)
}

// optionalTextField mirrors z.string().trim().min(1).max(units).optional().
func optionalTextField(body map[string]any, key string, units int) (*string, bool) {
	value, exists := body[key]
	if !exists {
		return nil, true
	}
	text, ok := boundedTrimmedText(value, units)
	if !ok {
		return nil, false
	}
	return &text, true
}

func boundedTrimmedText(value any, units int) (string, bool) {
	text, isString := value.(string)
	if !isString {
		return "", false
	}
	trimmed := strings.TrimSpace(text)
	if trimmed == "" || utf16Length(trimmed) > units {
		return "", false
	}
	return trimmed, true
}

// optionalEnumField mirrors z.enum([...]).optional(): exact string matches
// only (no trimming).
func optionalEnumField(body map[string]any, key string, allowed map[string]bool) (*string, bool) {
	value, exists := body[key]
	if !exists {
		return nil, true
	}
	text, isString := value.(string)
	if !isString || !allowed[text] {
		return nil, false
	}
	return &text, true
}

// revisionField mirrors z.string().trim().min(1) for expectedRevision.
func revisionField(body map[string]any) (string, bool) {
	value, exists := body["expectedRevision"]
	if !exists {
		return "", false
	}
	text, isString := value.(string)
	if !isString {
		return "", false
	}
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return "", false
	}
	return trimmed, true
}

// strictRevisionBody mirrors announcementVersionSchema (strict): the body
// carries exactly one key, expectedRevision, a non-empty trimmed string.
func strictRevisionBody(w http.ResponseWriter, body map[string]any, invalidMessage string) (string, bool) {
	if len(body) != 1 {
		kernel.WriteBadRequest(w, invalidMessage)
		return "", false
	}
	expectedRevision, ok := revisionField(body)
	if !ok {
		kernel.WriteBadRequest(w, invalidMessage)
		return "", false
	}
	return expectedRevision, true
}

// readAnnouncementIDs mirrors readAnnouncementsSchema (strict): the body
// must be an object carrying only announcementIds, a string array of at
// most 30 elements whose trimmed values are non-empty. An empty array is
// valid and yields count=0. A false return means the 400
// (公告已读参数无效) was already written.
func readAnnouncementIDs(w http.ResponseWriter, r *http.Request) ([]string, bool) {
	body, ok := bodyObject(w, r, "公告已读参数无效")
	if !ok {
		return nil, false
	}
	rawIDs, exists := body["announcementIds"]
	if !exists || len(body) != 1 {
		kernel.WriteBadRequest(w, "公告已读参数无效")
		return nil, false
	}
	values, isArray := rawIDs.([]any)
	if !isArray || len(values) > publicLimit {
		kernel.WriteBadRequest(w, "公告已读参数无效")
		return nil, false
	}
	ids := make([]string, 0, len(values))
	for _, value := range values {
		text, isString := value.(string)
		if !isString || strings.TrimSpace(text) == "" {
			kernel.WriteBadRequest(w, "公告已读参数无效")
			return nil, false
		}
		ids = append(ids, strings.TrimSpace(text))
	}
	return ids, true
}

// utf16Length mirrors JavaScript String.prototype.length (UTF-16 code
// units: astral runes count twice).
func utf16Length(value string) int {
	length := 0
	for _, symbol := range value {
		if symbol > 0xFFFF {
			length += 2
			continue
		}
		length++
	}
	return length
}

// writeMutationError maps the patch/delete outcome onto the Node route
// contract: nil outcome → 404 公告不存在, revision conflict → 409 +
// currentRevision, store validation (defense in depth behind route
// validation) → 409, storage failures → 500. False means the response was
// written.
func writeMutationError(w http.ResponseWriter, err error, missing bool) bool {
	if err == nil {
		if missing {
			kernel.WriteError(w, http.StatusNotFound, "公告不存在")
			return false
		}
		return true
	}
	var conflict *ConflictError
	var validation *ValidationError
	if errors.As(err, &conflict) {
		writeConflict(w, conflict)
		return false
	}
	if errors.As(err, &validation) {
		kernel.WriteError(w, http.StatusConflict, validation.Message)
		return false
	}
	kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
	return false
}

type diffField struct {
	field string
	label string
}

var updateDiffFields = []diffField{{"title", "标题"}, {"content", "内容"}, {"level", "级别"}, {"status", "状态"}}
var publishDiffFields = []diffField{{"status", "状态"}, {"publishedAt", "发布时间"}}
var unpublishDiffFields = []diffField{{"status", "状态"}}

// mutationDiff mirrors diffSafeFields over the announcement mutation state:
// unchanged fields are skipped via the JSON-comparable rendering (nil
// collapses to null).
func mutationDiff(before, after MutationState, fields []diffField) []authsys.OperationLogChange {
	changes := []authsys.OperationLogChange{}
	for _, field := range fields {
		beforeValue := stateFieldValue(before, field.field)
		afterValue := stateFieldValue(after, field.field)
		if comparableText(beforeValue) == comparableText(afterValue) {
			continue
		}
		changes = append(changes, authsys.OperationLogChange{
			Field:  field.field,
			Label:  field.label,
			Before: safeChangeText(beforeValue),
			After:  safeChangeText(afterValue),
		})
	}
	return changes
}

func stateFieldValue(state MutationState, field string) any {
	switch field {
	case "title":
		return state.Title
	case "content":
		if state.Content == nil {
			return nil
		}
		return *state.Content
	case "level":
		return state.Level
	case "status":
		return state.Status
	case "publishedAt":
		if state.PublishedAt == nil {
			return nil
		}
		return *state.PublishedAt
	}
	return nil
}

// comparableText mirrors operationLogComparableValue (JSON rendering with
// nil collapsing to null).
func comparableText(value any) string {
	if value == nil {
		return "null"
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "null"
	}
	return string(encoded)
}

// safeChangeText mirrors normalizeSafeValue for string values: a 200-UTF-16
// unit clamp; nil renders as empty text like the settings-family helper.
func safeChangeText(value any) string {
	if value == nil {
		return ""
	}
	text, isString := value.(string)
	if !isString {
		encoded, err := json.Marshal(value)
		if err != nil {
			return ""
		}
		return truncateUTF16(string(encoded), 200)
	}
	return truncateUTF16(text, 200)
}

// truncateUTF16 clamps to units UTF-16 code units (JavaScript
// slice(0, units)) and marks the cut with an ellipsis.
func truncateUTF16(value string, units int) string {
	length := 0
	for index, symbol := range value {
		width := 1
		if symbol > 0xFFFF {
			width = 2
		}
		if length+width > units {
			return value[:index] + "..."
		}
		length += width
	}
	return value
}

// announcementUpdate mirrors PATCH /:id: strict updateAnnouncementSchema
// (400 公告参数无效, including the at-least-one-change-field refine), then
// the guarded patch. The operation log fires only when something changed
// (Node outcome.changed) and keys its visibility off the before/after
// status.
func announcementUpdate(w http.ResponseWriter, r *http.Request, store *Store, sink authsys.OperationLogSink) {
	auth := authsys.AuthContextFrom(r)
	if auth == nil {
		kernel.WriteError(w, http.StatusUnauthorized, "请先登录")
		return
	}
	const invalidMessage = "公告参数无效"
	body, ok := bodyObject(w, r, invalidMessage)
	if !ok {
		return
	}
	if hasUnknownField(body, "expectedRevision", "title", "content", "level", "status") {
		kernel.WriteBadRequest(w, invalidMessage)
		return
	}
	expectedRevision, ok := revisionField(body)
	if !ok {
		kernel.WriteBadRequest(w, invalidMessage)
		return
	}
	title, titleOK := optionalTextField(body, "title", 120)
	content, contentOK := optionalTextField(body, "content", 5000)
	level, levelOK := optionalEnumField(body, "level", levels)
	status, statusOK := optionalEnumField(body, "status", statuses)
	if !titleOK || !contentOK || !levelOK || !statusOK {
		kernel.WriteBadRequest(w, invalidMessage)
		return
	}
	if title == nil && content == nil && level == nil && status == nil {
		// updateAnnouncementSchema refine: 至少提交一个公告变更字段.
		kernel.WriteBadRequest(w, invalidMessage)
		return
	}
	outcome, err := store.Patch(r.Context(), r.PathValue("id"),
		MutationInput{Title: title, Content: content, Level: level, Status: status},
		expectedRevision, auth.SystemAccountID)
	if !writeMutationError(w, err, outcome == nil) {
		return
	}
	if outcome.Changed && sink != nil {
		visibilityScope, detailLevel := "admin_only", "full"
		if outcome.After.Status == "published" || outcome.Before.Status == "published" {
			visibilityScope, detailLevel = "all_users", "summary"
		}
		sink.Record(authsys.OperationLogEntry{
			OperationScopeSystemAccountID: auth.SystemAccountID, Mode: "admin",
			Module: "announcements", Action: "update", OperationKey: "announcements.update",
			ResourceType: "announcement", ResourceID: outcome.Receipt.ID,
			ResourceName: outcome.After.Title,
			Summary:      "更新公告：" + outcome.After.Title,
			VisibilityScope: visibilityScope,
			DetailLevel:     detailLevel,
			Changes:         mutationDiff(outcome.Before, outcome.After, updateDiffFields),
		}, r)
	}
	kernel.WriteOK(w, outcome.Receipt, "")
}

// announcementVersionAction mirrors POST /:id/publish and /:id/unpublish:
// strict announcementVersionSchema (400 公告版本参数无效), a status patch
// toward the target status, and a log entry only when the status actually
// changed (always all_users/summary, announcements.routes.ts).
func announcementVersionAction(w http.ResponseWriter, r *http.Request, store *Store, sink authsys.OperationLogSink, action, status string) {
	auth := authsys.AuthContextFrom(r)
	if auth == nil {
		kernel.WriteError(w, http.StatusUnauthorized, "请先登录")
		return
	}
	const invalidMessage = "公告版本参数无效"
	body, ok := bodyObject(w, r, invalidMessage)
	if !ok {
		return
	}
	expectedRevision, ok := strictRevisionBody(w, body, invalidMessage)
	if !ok {
		return
	}
	outcome, err := store.Patch(r.Context(), r.PathValue("id"), MutationInput{Status: &status}, expectedRevision, auth.SystemAccountID)
	if !writeMutationError(w, err, outcome == nil) {
		return
	}
	if outcome.Changed && sink != nil {
		entry := authsys.OperationLogEntry{
			OperationScopeSystemAccountID: auth.SystemAccountID, Mode: "admin",
			Module: "announcements", Action: action, OperationKey: "announcements." + action,
			ResourceType: "announcement", ResourceID: outcome.Receipt.ID,
			ResourceName: outcome.After.Title,
			VisibilityScope: "all_users",
			DetailLevel:     "summary",
		}
		switch action {
		case "publish":
			entry.Summary = "发布公告：" + outcome.After.Title
			entry.Changes = mutationDiff(outcome.Before, outcome.After, publishDiffFields)
		default: // unpublish
			entry.Summary = "下线公告：" + outcome.After.Title
			entry.Changes = mutationDiff(outcome.Before, outcome.After, unpublishDiffFields)
		}
		sink.Record(entry, r)
	}
	kernel.WriteOK(w, outcome.Receipt, "")
}

func announcementDelete(w http.ResponseWriter, r *http.Request, store *Store, sink authsys.OperationLogSink) {
	auth := authsys.AuthContextFrom(r)
	if auth == nil {
		kernel.WriteError(w, http.StatusUnauthorized, "请先登录")
		return
	}
	const invalidMessage = "公告版本参数无效"
	body, ok := bodyObject(w, r, invalidMessage)
	if !ok {
		return
	}
	expectedRevision, ok := strictRevisionBody(w, body, invalidMessage)
	if !ok {
		return
	}
	outcome, err := store.Delete(r.Context(), r.PathValue("id"), expectedRevision)
	if !writeMutationError(w, err, outcome == nil) {
		return
	}
	if sink != nil {
		// announcements.routes.ts keys the delete log visibility off the
		// status before the deletion.
		visibilityScope, detailLevel := "admin_only", "full"
		if outcome.Before.Status == "published" {
			visibilityScope, detailLevel = "all_users", "summary"
		}
		sink.Record(authsys.OperationLogEntry{
			OperationScopeSystemAccountID: auth.SystemAccountID, Mode: "admin",
			Module: "announcements", Action: "delete", OperationKey: "announcements.delete",
			ResourceType: "announcement", ResourceID: outcome.Receipt.ID,
			ResourceName: outcome.Before.Title,
			Summary:      "删除公告：" + outcome.Before.Title,
			VisibilityScope: visibilityScope,
			DetailLevel:     detailLevel,
			Changes: []authsys.OperationLogChange{
				// safeChange('deleted', '删除状态', false, true): native
				// booleans pass through the sink untouched.
				{Field: "deleted", Label: "删除状态", BeforeValue: false, AfterValue: true},
			},
		}, r)
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeConflict(w http.ResponseWriter, conflict *ConflictError) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusConflict)
	body := `{"message":` + jsonString(conflict.Message) + `,"currentRevision":` + jsonString(conflict.CurrentRevision) + `}`
	_, _ = w.Write([]byte(body))
}

func jsonString(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func valueOrText(value *string, fallback string) string {
	if value == nil || strings.TrimSpace(*value) == "" {
		return fallback
	}
	return *value
}

// mountGuardedCreate mounts the guarded create route.
func mountGuardedCreate(d *authsys.Deps, store *Store, sink authsys.OperationLogSink) http.Handler {
	return d.RequireAdmin(kernel.MutationGuardMiddleware(kernel.MutationGuardOptions{
		OperationKey: "announcements.create",
		Fingerprint: func(r *http.Request) (any, error) {
			return map[string]any{
				"title":   kernel.TextField(kernel.BodyField(r, "title")),
				"content": kernel.HashStableValue(kernel.BodyField(r, "content")),
				"level":   kernel.TextField(kernel.BodyField(r, "level")),
				"status":  kernel.TextField(kernel.BodyField(r, "status")),
			}, nil
		},
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := authsys.AuthContextFrom(r)
		if auth == nil {
			kernel.WriteError(w, http.StatusUnauthorized, "请先登录")
			return
		}
		// createAnnouncementSchema (strict): title/content required trimmed
		// strings (≤120/≤5000 UTF-16 units), level/status optional enums,
		// unknown fields and non-string values → 400 公告参数无效.
		input, ok := createAnnouncementInput(w, r)
		if !ok {
			return
		}
		receipt, err := store.Create(r.Context(), input, auth.SystemAccountID)
		if err != nil {
			var validation *ValidationError
			if errors.As(err, &validation) {
				kernel.WriteError(w, http.StatusConflict, validation.Message)
				return
			}
			kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		if sink != nil {
			// announcements.routes.ts keys the create log visibility off the
			// created announcement status (normalizeStatus default: draft).
			createStatus, _ := normalizeStatus(input.Status, "draft")
			visibilityScope, detailLevel := "admin_only", "full"
			if createStatus == "published" {
				visibilityScope, detailLevel = "all_users", "summary"
			}
			sink.Record(authsys.OperationLogEntry{
				ActorSystemAccountID:          auth.SystemAccountID,
				ActorUsername:                 auth.Username,
				ActorDisplayName:              auth.DisplayName,
				ActorRole:                     auth.Role,
				OperationScopeSystemAccountID: auth.SystemAccountID, Mode: "admin",
				Module: "announcements", Action: "create", OperationKey: "announcements.create",
				ResourceType: "announcement", ResourceID: receipt.ID,
				ResourceName: *input.Title,
				Summary:      "创建公告：" + *input.Title,

				VisibilityScope: visibilityScope,
				DetailLevel:     detailLevel,
				Changes: []authsys.OperationLogChange{
					{Field: "title", Label: "标题", After: *input.Title},
					{Field: "level", Label: "级别", After: valueOrText(input.Level, "info")},
					{Field: "status", Label: "状态", After: valueOrText(input.Status, "draft")},
				},
			}, r)
		}
		w.WriteHeader(http.StatusCreated)
		kernel.WriteOK(w, receipt, "")
	})))
}

// createAnnouncementInput mirrors createAnnouncementSchema (strict).
func createAnnouncementInput(w http.ResponseWriter, r *http.Request) (MutationInput, bool) {
	const invalidMessage = "公告参数无效"
	body, ok := bodyObject(w, r, invalidMessage)
	if !ok {
		return MutationInput{}, false
	}
	if hasUnknownField(body, "title", "content", "level", "status") {
		kernel.WriteBadRequest(w, invalidMessage)
		return MutationInput{}, false
	}
	title, ok := requiredTextField(body, "title", 120)
	if !ok {
		kernel.WriteBadRequest(w, invalidMessage)
		return MutationInput{}, false
	}
	content, ok := requiredTextField(body, "content", 5000)
	if !ok {
		kernel.WriteBadRequest(w, invalidMessage)
		return MutationInput{}, false
	}
	level, levelOK := optionalEnumField(body, "level", levels)
	status, statusOK := optionalEnumField(body, "status", statuses)
	if !levelOK || !statusOK {
		kernel.WriteBadRequest(w, invalidMessage)
		return MutationInput{}, false
	}
	return MutationInput{Title: &title, Content: &content, Level: level, Status: status}, true
}
