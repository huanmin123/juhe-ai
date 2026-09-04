// routes.go ports backend/src/modules/ip-stats/ip-stats.routes.ts: the admin
// list read and the four policy write endpoints. All five endpoints require
// the admin role; the writes additionally pass the kernel mutation guard
// (Node mutationGuard) with the Node fingerprint fields, emit the
// client_ip_stats operation log entry after success and invalidate the
// client IP policy cache after the store transaction commits.
package ipstats

import (
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

// Deps bundles the M15 slice collaborators.
type Deps struct {
	Store *Store
	Auth  *authsys.Deps
	Sink  authsys.OperationLogSink
}

// Mount wires the ip-stats route family onto /__aisys__/api/ip-stats.
func (d *Deps) Mount(k *kernel.Kernel) {
	prefix := "/__aisys__/api/ip-stats"
	k.Register("GET "+prefix, d.Auth.RequireAdmin(http.HandlerFunc(d.handleList)))
	k.Register("POST "+prefix+"/{ipHash}/allowlist", d.guardedWrite("client_ip_stats.allowlist", simplePolicyFingerprint, d.handleAllowlist))
	k.Register("POST "+prefix+"/{ipHash}/unallowlist", d.guardedWrite("client_ip_stats.unallowlist", simplePolicyFingerprint, d.handleUnallowlist))
	k.Register("POST "+prefix+"/{ipHash}/blacklist", d.guardedWrite("client_ip_stats.blacklist", blacklistFingerprint, d.handleBlacklist))
	k.Register("POST "+prefix+"/{ipHash}/unblock", d.guardedWrite("client_ip_stats.unblock", simplePolicyFingerprint, d.handleUnblock))
}

// guardedWrite mirrors the Node middleware chain: admin role -> mutation
// guard -> handler.
func (d *Deps) guardedWrite(operationKey string, fingerprint kernel.FingerprintFunc, handler http.HandlerFunc) http.Handler {
	guard := kernel.MutationGuardMiddleware(kernel.MutationGuardOptions{
		OperationKey: operationKey,
		Actor:        actorResolver,
		Fingerprint:  fingerprint,
	})
	return d.Auth.RequireAdmin(guard(handler))
}

func actorResolver(r *http.Request) string {
	if auth := authsys.AuthContextFrom(r); auth != nil {
		return auth.SystemAccountID
	}
	return "anonymous"
}

// simplePolicyFingerprint mirrors the { ipHash, reason } fingerprint: Node
// JSON.stringify drops undefined fields but keeps explicit nulls, so absent
// body fields stay out of the map while null values are kept.
func simplePolicyFingerprint(r *http.Request) (any, error) {
	fingerprint := map[string]any{"ipHash": r.PathValue("ipHash")}
	if value, exists := kernel.ParsedBody(r)["reason"]; exists {
		fingerprint["reason"] = value
	}
	return fingerprint, nil
}

// blacklistFingerprint mirrors the blacklist fingerprint: raw ipHash plus
// reason and both Node JSON-number duration fields (1, 1.0 and 1e0 decode to
// the same float64 and therefore the same claim).
func blacklistFingerprint(r *http.Request) (any, error) {
	fingerprint := map[string]any{"ipHash": r.PathValue("ipHash")}
	body := kernel.ParsedBody(r)
	for _, field := range []string{"reason", "durationMinutes", "durationDays"} {
		if value, exists := body[field]; exists {
			fingerprint[field] = value
		}
	}
	return fingerprint, nil
}

// listQuerySchema mirrors the zod list query: duplicated known parameters and
// invalid values reject with IP 统计参数无效; page/pageSize follow Node Number
// coercion and the server clamps the page into the 1000-row window.
func (d *Deps) handleList(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	for _, name := range []string{"page", "pageSize", "keyword", "status", "startDate", "endDate", "lastUsedStartDate", "lastUsedEndDate", "sortField", "sortOrder"} {
		if len(query[name]) > 1 {
			kernel.WriteBadRequest(w, "IP 统计参数无效")
			return
		}
	}
	options := ListOptions{LastUsedSortScope: "global"}
	options.PageSize = boundedPageSizeFromQuery(query)
	if options.PageSize == 0 {
		kernel.WriteBadRequest(w, "IP 统计参数无效")
		return
	}
	options.Page = boundedPage(1, options.PageSize)
	if raw, present := querySingle(query, "page"); present {
		value, ok := parseNodeNumber(raw)
		if !ok || value != math.Trunc(value) || value < 1 {
			kernel.WriteBadRequest(w, "IP 统计参数无效")
			return
		}
		options.Page = boundedPage(int(value), options.PageSize)
	}
	options.Keyword = strings.TrimSpace(query.Get("keyword"))
	if raw, present := querySingle(query, "status"); present {
		switch raw {
		case StatusAll, StatusNormal, StatusBlacklisted, StatusAllowlisted:
			options.Status = raw
		default:
			kernel.WriteBadRequest(w, "IP 统计参数无效")
			return
		}
	}
	options.StartDate = strings.TrimSpace(query.Get("startDate"))
	options.EndDate = strings.TrimSpace(query.Get("endDate"))
	options.LastUsedStartDate = strings.TrimSpace(query.Get("lastUsedStartDate"))
	options.LastUsedEndDate = strings.TrimSpace(query.Get("lastUsedEndDate"))
	if raw, present := querySingle(query, "sortField"); present {
		switch raw {
		case "requestCount", "successCount", "errorCount", "errorRate", "totalTokens", "totalCost", "activeDays", "lastUsedAt":
			options.SortField = raw
		default:
			kernel.WriteBadRequest(w, "IP 统计参数无效")
			return
		}
	}
	if raw, present := querySingle(query, "sortOrder"); present {
		switch raw {
		case "asc", "desc":
			options.SortOrder = raw
		default:
			kernel.WriteBadRequest(w, "IP 统计参数无效")
			return
		}
	}
	result, err := d.Store.List(r.Context(), options)
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	kernel.WriteOK(w, result, "")
}

func querySingle(query map[string][]string, name string) (string, bool) {
	values := query[name]
	if len(values) != 1 {
		return "", false
	}
	return values[0], true
}

// boundedPageSizeFromQuery validates pageSize (1..100 after Node Number
// coercion; default 20 when absent) and reports invalid values by returning
// the sentinel that fails the 400 check in handleList.
func boundedPageSizeFromQuery(query map[string][]string) int {
	raw, present := querySingle(query, "pageSize")
	if !present {
		return 20
	}
	value, ok := parseNodeNumber(raw)
	if !ok || value != math.Trunc(value) || value < 1 || value > 100 {
		return 0
	}
	return int(value)
}

// boundedPage mirrors boundedPage: clamp into the 1000-row window.
func boundedPage(page, pageSize int) int {
	maxPage := (maxListWindowRows - 1) / pageSize
	if maxPage < 1 {
		maxPage = 1
	}
	if page > maxPage {
		return maxPage
	}
	if page < 1 {
		return 1
	}
	return page
}

// parseNodeNumber mirrors Number(): "" -> 0, trimmed Unicode whitespace,
// decimal/exponent forms plus 0x/0o/0b integer prefixes; NaN/Infinity fail.
func parseNodeNumber(raw string) (float64, bool) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return 0, true
	}
	sign := 1.0
	body := text
	if strings.HasPrefix(body, "+") {
		body = body[1:]
	} else if strings.HasPrefix(body, "-") {
		sign = -1
		body = body[1:]
	}
	lower := strings.ToLower(body)
	radix := 0
	switch {
	case strings.HasPrefix(lower, "0x"):
		radix = 16
	case strings.HasPrefix(lower, "0o"):
		radix = 8
	case strings.HasPrefix(lower, "0b"):
		radix = 2
	}
	if radix != 0 {
		digits := lower[2:]
		if digits == "" {
			return 0, false
		}
		parsed, err := strconv.ParseUint(digits, radix, 64)
		if err != nil {
			return 0, false
		}
		return sign * float64(parsed), true
	}
	parsed, err := strconv.ParseFloat(text, 64)
	if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
		return 0, false
	}
	return parsed, true
}

func (d *Deps) handleAllowlist(w http.ResponseWriter, r *http.Request) {
	d.handleCreatePolicy(w, r, PolicyTypeAllowlist)
}

func (d *Deps) handleBlacklist(w http.ResponseWriter, r *http.Request) {
	d.handleCreatePolicy(w, r, PolicyTypeBlacklist)
}

func (d *Deps) handleUnallowlist(w http.ResponseWriter, r *http.Request) {
	d.handleDisablePolicy(w, r, PolicyTypeAllowlist)
}

func (d *Deps) handleUnblock(w http.ResponseWriter, r *http.Request) {
	d.handleDisablePolicy(w, r, PolicyTypeBlacklist)
}

// policyBody mirrors the strict zod bodies: reason everywhere, durations only
// for blacklist. Returns the trimmed reason plus resolved duration.
type policyBody struct {
	reason          *string
	durationMinutes *float64
	durationDays    *float64
}

func parsePolicyBody(w http.ResponseWriter, r *http.Request, allowDuration bool) (policyBody, bool) {
	var body map[string]any
	if !kernel.DecodeJSON(w, r, &body) {
		return policyBody{}, false
	}
	parsed := policyBody{}
	if value, exists := body["reason"]; exists {
		text, isString := value.(string)
		if !isString {
			kernel.WriteBadRequest(w, "IP 策略参数无效")
			return policyBody{}, false
		}
		trimmed := strings.TrimSpace(text)
		if int(len(utf16.Encode([]rune(trimmed)))) > 500 {
			kernel.WriteBadRequest(w, "原因不能超过 500 个字符")
			return policyBody{}, false
		}
		parsed.reason = &trimmed
	}
	if allowDuration {
		if value, exists := body["durationMinutes"]; exists {
			if value == nil {
				kernel.WriteBadRequest(w, "IP 策略参数无效")
				return policyBody{}, false
			}
			number, ok := jsonInt(w, value, "封禁分钟数", 1, 525600)
			if !ok {
				return policyBody{}, false
			}
			parsed.durationMinutes = &number
		}
		if value, exists := body["durationDays"]; exists {
			if value == nil {
				kernel.WriteBadRequest(w, "IP 策略参数无效")
				return policyBody{}, false
			}
			number, ok := jsonInt(w, value, "封禁天数", 1, 3650)
			if !ok {
				return policyBody{}, false
			}
			parsed.durationDays = &number
		}
	}
	for _, key := range bodyKeys(body) {
		allowed := key == "reason" || (allowDuration && (key == "durationMinutes" || key == "durationDays"))
		if !allowed {
			kernel.WriteBadRequest(w, "IP 策略参数包含未知字段")
			return policyBody{}, false
		}
	}
	return parsed, true
}

func bodyKeys(body map[string]any) []string {
	keys := make([]string, 0, len(body))
	for key := range body {
		keys = append(keys, key)
	}
	return keys
}

// jsonInt mirrors z.number().int().min(min).max(max): JSON numbers only,
// integer-valued (1 and 1.0 both pass), with the Node field messages.
func jsonInt(w http.ResponseWriter, value any, label string, min, max float64) (float64, bool) {
	number, isNumber := value.(float64)
	if !isNumber || math.IsNaN(number) || math.IsInf(number, 0) {
		kernel.WriteBadRequest(w, "IP 策略参数无效")
		return 0, false
	}
	if number < min {
		kernel.WriteBadRequest(w, label+"不能小于 "+strconv.FormatFloat(min, 'f', -1, 64))
		return 0, false
	}
	if number > max {
		kernel.WriteBadRequest(w, label+"不能超过 "+strconv.FormatFloat(max, 'f', -1, 64))
		return 0, false
	}
	if number != math.Trunc(number) {
		kernel.WriteBadRequest(w, "IP 策略参数无效")
		return 0, false
	}
	return number, true
}

// resolveBlacklistDuration mirrors resolvePolicyDuration.
func resolveBlacklistDuration(parsed policyBody, now time.Time) (*string, string, bool) {
	hasMinutes := parsed.durationMinutes != nil
	hasDays := parsed.durationDays != nil
	if hasMinutes && hasDays {
		return nil, "", false
	}
	if hasMinutes {
		expiresAt := isoMillis(now.Add(time.Duration(*parsed.durationMinutes) * time.Minute))
		label := formatJSONNumber(*parsed.durationMinutes) + " 分钟"
		return &expiresAt, label, true
	}
	if hasDays {
		expiresAt := isoMillis(now.Add(time.Duration(*parsed.durationDays) * 24 * time.Hour))
		label := formatJSONNumber(*parsed.durationDays) + " 天"
		return &expiresAt, label, true
	}
	return nil, "永久", true
}

// formatJSONNumber renders the integer-valued duration the way Node template
// literals render numbers (no trailing decimals).
func formatJSONNumber(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}

func (d *Deps) handleCreatePolicy(w http.ResponseWriter, r *http.Request, policyType string) {
	rawHash, ipHash, ok := pathIPHash(w, r)
	if !ok {
		return
	}
	parsed, ok := parsePolicyBody(w, r, policyType == PolicyTypeBlacklist)
	if !ok {
		return
	}
	var expiresAt *string
	durationLabel := "永久"
	if policyType == PolicyTypeBlacklist {
		resolved, label, valid := resolveBlacklistDuration(parsed, d.Store.now())
		if !valid {
			kernel.WriteBadRequest(w, "封禁时长只能选择一种")
			return
		}
		expiresAt, durationLabel = resolved, label
	}
	auth := authsys.AuthContextFrom(r)
	if auth == nil {
		kernel.WriteError(w, http.StatusUnauthorized, "请先登录")
		return
	}
	policy, err := d.Store.CreatePolicy(r.Context(), PolicyMutationInput{
		IPHash:               ipHash,
		PolicyType:           policyType,
		Reason:               parsed.reason,
		ExpiresAt:            expiresAt,
		ActorSystemAccountID: auth.SystemAccountID,
	})
	if err != nil {
		d.writePolicyError(w, err, "IP 策略保存失败")
		return
	}
	action := policyType
	if d.Sink != nil {
		changes := []authsys.OperationLogChange{
			{Field: "reason", Label: "原因", After: textValue(parsed.reason)},
			{Field: "policyType", Label: "策略类型", After: policyType},
			{Field: "duration", Label: durationFieldLabel(policyType), After: durationLabel},
		}
		if expiresAt != nil {
			changes = append(changes, authsys.OperationLogChange{Field: "expiresAt", Label: "过期时间", After: *expiresAt})
		}
		d.Sink.Record(authsys.OperationLogEntry{
			ActorSystemAccountID: auth.SystemAccountID,
			ActorUsername:        auth.Username,
			ActorDisplayName:     auth.DisplayName,
			ActorRole:            auth.Role,
			Mode:                 "admin",
			Module:               "client_ip_stats",
			Action:               action,
			OperationKey:         "client_ip_stats." + action,
			ResourceType:         "client_ip",
			ResourceID:           rawHash,
			ResourceName:         rawHash[:12],
			Summary:              createSummary(policyType) + "：" + rawHash[:12],
			Changes:              changes,
		}, r)
	}
	kernel.WriteOK(w, policy, "")
}

func (d *Deps) handleDisablePolicy(w http.ResponseWriter, r *http.Request, policyType string) {
	rawHash, ipHash, ok := pathIPHash(w, r)
	if !ok {
		return
	}
	parsed, ok := parsePolicyBody(w, r, false)
	if !ok {
		return
	}
	auth := authsys.AuthContextFrom(r)
	if auth == nil {
		kernel.WriteError(w, http.StatusUnauthorized, "请先登录")
		return
	}
	result, err := d.Store.DisablePolicies(r.Context(), PolicyDisableInput{
		IPHash:               ipHash,
		PolicyType:           policyType,
		Reason:               parsed.reason,
		ActorSystemAccountID: auth.SystemAccountID,
	})
	if err != nil {
		d.writePolicyError(w, err, "IP 策略停用失败")
		return
	}
	action := "unallowlist"
	summary := "移出 IP 白名单"
	if policyType == PolicyTypeBlacklist {
		action = "unblock"
		summary = "解除 IP 封禁"
	}
	if d.Sink != nil {
		d.Sink.Record(authsys.OperationLogEntry{
			ActorSystemAccountID: auth.SystemAccountID,
			ActorUsername:        auth.Username,
			ActorDisplayName:     auth.DisplayName,
			ActorRole:            auth.Role,
			Mode:                 "admin",
			Module:               "client_ip_stats",
			Action:               action,
			OperationKey:         "client_ip_stats." + action,
			ResourceType:         "client_ip",
			ResourceID:           rawHash,
			ResourceName:         rawHash[:12],
			Summary:              summary + "：" + rawHash[:12],
			Changes: []authsys.OperationLogChange{
				{Field: "disabledCount", Label: "停用策略数", After: strconv.FormatInt(result.DisabledCount, 10)},
				{Field: "policyType", Label: "策略类型", Before: policyType},
				{Field: "reason", Label: "原因", After: textValue(parsed.reason)},
			},
		}, r)
	}
	kernel.WriteOK(w, result, "")
}

// pathIPHash mirrors ipHashParamSchema: trim, 64 hex digits; the raw trimmed
// value feeds the operation log while the store normalizes to lowercase.
func pathIPHash(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	raw := strings.TrimSpace(r.PathValue("ipHash"))
	ipHash, err := NormalizeIPHash(raw)
	if err != nil {
		kernel.WriteBadRequest(w, "IP 标识无效")
		return "", "", false
	}
	return raw, ipHash, true
}

// writePolicyError mirrors the Node catch paths: business, registry and
// storage errors all render as 400 (W6 keeps the Node contract instead of a
// generic 500).
func (d *Deps) writePolicyError(w http.ResponseWriter, err error, fallback string) {
	var validation *ValidationError
	if errors.As(err, &validation) {
		kernel.WriteBadRequest(w, validation.Message)
		return
	}
	kernel.WriteBadRequest(w, fallback)
}

func textValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func durationFieldLabel(policyType string) string {
	if policyType == PolicyTypeBlacklist {
		return "封禁时长"
	}
	return "白名单时长"
}

func createSummary(policyType string) string {
	if policyType == PolicyTypeBlacklist {
		return "封禁 IP"
	}
	return "加入 IP 白名单"
}
