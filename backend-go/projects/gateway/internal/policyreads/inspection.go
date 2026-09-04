// Package policyreads owns the M16 vertical slice: three admin-surface
// management domains ported from the Node system API —
//
//   - M16a response inspection policies (backend/src/modules/response-inspection-policies
//   - backend/src/storage/response-inspection-policy.repository.ts; business
//     table response_inspection_policies),
//   - M16b external integration sources (backend/src/modules/external-integrations
//     /external-integration-sources.routes.ts + backend/src/storage/
//     external-integration-source*.ts; business tables external_integration_sources
//     and external_integration_source_tokens),
//   - M16c OAuth client management (backend/src/modules/oidc-provider
//     /oidc-provider.routes.ts oauthManagementRouter; business table
//     oauth_clients).
//
// All three families mount behind requireAdmin on /__aisys__/api. The public
// OAuth protocol surface, the external public API itself and the gateway
// runtime consumers are companion slices; this package mirrors the management
// contracts only, including mutation guards, optimistic locking, conflicts and
// operation logs. The three domains share one package (file prefix split:
// inspection / external / oauth) because they reuse the same dual-mode
// persistence helpers and zod-message shims.
package policyreads

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

// ConflictError maps to Node conflict outcomes rendered as 409 (patch conflicts
// and guarded duplicates across the three domains).
type ConflictError struct{ Message string }

func (e *ConflictError) Error() string { return e.Message }

// ValidationError maps to Node throw-Error paths rendered as 400.
type ValidationError struct{ Message string }

func (e *ValidationError) Error() string { return e.Message }

// OidcCiphertextError maps to Node OidcCiphertextError (oauth.go).
type OidcCiphertextError struct{ Message string }

func (e *OidcCiphertextError) Error() string { return e.Message }

// RuntimeInvalidator is the K5 gateway runtime cache invalidation port
// (Node notifyGatewayRuntimeCacheInvalidation). *inval.Bus satisfies it; nil
// keeps the slice self-contained with no-op invalidation.
type RuntimeInvalidator interface {
	Invalidate(topic, reason string)
}

// TopicGatewayRuntime mirrors the Node gateway runtime cache topic constant.
const TopicGatewayRuntime = "topic:gateway_runtime_cache"

// baseStore is the dual-mode (SQLite + PostgreSQL) persistence core shared by
// the three domains.
type baseStore struct {
	db    *sql.DB
	pg    bool
	now   func() time.Time
	newID func(string) string
	inval RuntimeInvalidator
}

func newBaseStore(db *sql.DB, postgres bool, now func() time.Time, newID func(string) string, inval RuntimeInvalidator) (baseStore, error) {
	if db == nil {
		return baseStore{}, errors.New("policyreads store requires a database")
	}
	if now == nil {
		now = time.Now
	}
	if newID == nil {
		newID = randomPrefixedID
	}
	return baseStore{db: db, pg: postgres, now: now, newID: newID, inval: inval}, nil
}

func (b *baseStore) table(name string) string {
	if b.pg {
		return "juhe_business." + name
	}
	return name
}

func (b *baseStore) bind(query string) string {
	if !b.pg {
		return query
	}
	var out strings.Builder
	index := 1
	for i := 0; i < len(query); i++ {
		if query[i] == '?' {
			out.WriteString("$" + strconv.Itoa(index))
			index++
		} else {
			out.WriteByte(query[i])
		}
	}
	return out.String()
}

func (b *baseStore) nowISO() string { return isoMillis(b.now()) }

func (b *baseStore) generateID(prefix string) string { return b.newID(prefix) }

// invalidateRuntime mirrors invalidateGatewayRuntimeAfterBusinessWrite.
func (b *baseStore) invalidateRuntime(reason string) {
	if b.inval != nil {
		b.inval.Invalidate(TopicGatewayRuntime, reason)
	}
}

// randomPrefixedID mirrors Node newId(prefix) (random hex suffix).
func randomPrefixedID(prefix string) string {
	buf := make([]byte, 12)
	_, _ = rand.Read(buf)
	return prefix + "_" + hex.EncodeToString(buf)
}

func randomBase64URLBytes(n int) string {
	buf := make([]byte, n)
	_, _ = rand.Read(buf)
	return base64RawURL(buf)
}

func base64RawURL(data []byte) string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	out := make([]byte, 0, (len(data)*8+5)/6)
	var buffer, bits int
	for _, b := range data {
		buffer = buffer<<8 | int(b)
		bits += 8
		for bits >= 6 {
			bits -= 6
			out = append(out, alphabet[(buffer>>bits)&0x3f])
		}
	}
	if bits > 0 {
		out = append(out, alphabet[(buffer<<(6-bits))&0x3f])
	}
	return string(out)
}

func newUUIDv4() string {
	buf := make([]byte, 16)
	_, _ = rand.Read(buf)
	buf[6] = (buf[6] & 0x0f) | 0x40
	buf[8] = (buf[8] & 0x3f) | 0x80
	h := hex.EncodeToString(buf)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

func ensureCtx(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

// isoMillis mirrors Node nowIso()/toISOString() millisecond precision.
func isoMillis(t time.Time) string {
	return t.UTC().Truncate(time.Millisecond).Format("2006-01-02T15:04:05.000Z07:00")
}

var rfc3339InstantPattern = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.(\d{1,9}))?(Z|[+-]\d{2}:\d{2})$`)

// canonicalRFC3339Millis mirrors canonicalizeRfc3339Instant: RFC3339 with a
// mandatory Z or numeric offset, normalized to millisecond-precision UTC.
func canonicalRFC3339Millis(value string) (string, bool) {
	text := strings.TrimSpace(value)
	if !rfc3339InstantPattern.MatchString(text) {
		return "", false
	}
	parsed, err := time.Parse(time.RFC3339Nano, text)
	if err != nil {
		return "", false
	}
	return isoMillis(parsed), true
}

// parseRFC3339Millis mirrors rfc3339InstantMilliseconds.
func parseRFC3339Millis(value string) (int64, bool) {
	text := strings.TrimSpace(value)
	if !rfc3339InstantPattern.MatchString(text) {
		return 0, false
	}
	parsed, err := time.Parse(time.RFC3339Nano, text)
	if err != nil {
		return 0, false
	}
	return parsed.UnixMilli(), true
}

// nextRFC3339Millis mirrors nextPolicyUpdatedAt /
// nextExternalIntegrationUpdatedAt: monotonic RFC3339 millis from now.
func nextRFC3339Millis(current string, now time.Time, label string) (string, error) {
	currentMs, ok := parseRFC3339Millis(current)
	if !ok {
		return "", &ValidationError{Message: label + "：" + current}
	}
	next := now.UnixMilli()
	if floor := currentMs + 1; next < floor {
		next = floor
	}
	return isoMillis(time.UnixMilli(next)), nil
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func ptrString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func nullPtrString(value sql.NullString) *string {
	if !value.Valid || value.String == "" {
		return nil
	}
	return &value.String
}

func runeLen(value string) int {
	runes := []rune(value)
	return len(runes)
}

func truncateRunes(value string, max int) string {
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	return string(runes[:max]) + "..."
}

// escapeLikePrefix mirrors storage/query-utils.ts escapeLikePrefix.
func escapeLikePrefix(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return replacer.Replace(value)
}

// uniqueSortedStrings mirrors [...new Set(values)].sort().
func uniqueSortedStrings(values []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, value := range values {
		if seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// safeChangeText mirrors operation-log.service.ts normalizeSafeValue for the
// string-only Go change struct.
func safeChangeText(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return truncateRunes(typed, 200)
	case bool:
		return strconv.FormatBool(typed)
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	case float64:
		return strconv.FormatFloat(typed, 'g', -1, 64)
	default:
		serialized, err := json.Marshal(value)
		if err != nil {
			return truncateRunes(fmt.Sprintf("%v", value), 500)
		}
		return truncateRunes(string(serialized), 500)
	}
}

// ---------------------------------------------------------------------------
// zod v3 message shims (locales/en.cjs) shared by the schema mirrors.
// ---------------------------------------------------------------------------

const zodRequired = "Required"

func zodReceived(value any) string {
	switch value.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case string:
		return "string"
	case float64, int, int64:
		return "number"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	default:
		return "unknown"
	}
}

func zodInvalidType(expected string, value any) string {
	return "Expected " + expected + ", received " + zodReceived(value)
}

func zodEnumMessage(options []string, received string) string {
	quoted := make([]string, len(options))
	for i, option := range options {
		quoted[i] = "'" + option + "'"
	}
	return "Invalid enum value. Expected " + strings.Join(quoted, " | ") + ", received '" + received + "'"
}

func zodStringMin(n int) string {
	return fmt.Sprintf("String must contain at least %d character(s)", n)
}

func zodStringMax(n int) string {
	return fmt.Sprintf("String must contain at most %d character(s)", n)
}

func zodArrayMin(n int) string {
	return fmt.Sprintf("Array must contain at least %d element(s)", n)
}

func zodArrayMax(n int) string {
	return fmt.Sprintf("Array must contain at most %d element(s)", n)
}

func zodNumberMin(n int) string {
	return fmt.Sprintf("Number must be greater than or equal to %d", n)
}

func zodNumberMax(n int) string {
	return fmt.Sprintf("Number must be less than or equal to %d", n)
}

func zodUnrecognizedKeys(keys []string) string {
	sorted := append([]string{}, keys...)
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && sorted[j] < sorted[j-1]; j-- {
			sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
		}
	}
	return "Unrecognized key(s) in object: " + strings.Join(sorted, ", ")
}

// asStringList bridges zod-transformed []any lists and already-normalized
// []string values inside match normalization.
func asStringList(value any) ([]string, bool) {
	switch typed := value.(type) {
	case nil:
		return nil, true
	case []any:
		out := make([]string, len(typed))
		for i, item := range typed {
			text, isString := item.(string)
			if !isString {
				return nil, false
			}
			out[i] = text
		}
		return out, true
	case []string:
		return typed, true
	default:
		return nil, false
	}
}

// ===========================================================================
// M16a: response inspection policies.
// ===========================================================================

const (
	inspectionPrefix = "/__aisys__/api/response-inspection-policies"

	// maxManagementResponseInspectionPolicies mirrors
	// maxManagementResponseInspectionPolicies.
	maxManagementResponseInspectionPolicies = 100

	protocolCodeOpenAI    = "openai"
	protocolCodeAnthropic = "anthropic"
	protocolCodeGemini    = "gemini"
	vendorCodeGPT         = "gpt"
)

var inspectionProtocolCodes = []string{protocolCodeOpenAI, protocolCodeAnthropic, protocolCodeGemini}

var inspectionClientProfiles = []string{"codex", "generic_openai", "claude_code", "generic_anthropic", "generic_gemini", "gemini_cli"}

var inspectionMatchKeys = []string{
	"clientProfiles", "outputTextIncludes", "outputTextExcludes", "errorCodes",
	"errorTypes", "errorMessageIncludes", "finishReasons", "jsonPathsExists", "rawTextIncludes",
}

var inspectionPositiveMatchKeys = []string{
	"outputTextIncludes", "errorCodes", "errorTypes", "errorMessageIncludes",
	"finishReasons", "jsonPathsExists", "rawTextIncludes",
}

var inspectionPolicyActions = []string{
	"observe", "drop_event", "retry_no_avoidance", "retry_next_account",
	"avoid_account_ttl", "avoid_upstream_bucket_ttl",
}

// InspectionMatch mirrors ResponseInspectionPolicyMatch: known keys only,
// deduplicated trimmed string lists. Map marshaling sorts keys, which keeps
// JSON equality comparisons stable.
type InspectionMatch map[string][]string

// InspectionOverview mirrors ResponseInspectionPolicyOverview.
type InspectionOverview struct {
	ID           string  `json:"id"`
	DefaultRule  bool    `json:"defaultRule"`
	Editable     bool    `json:"editable"`
	Name         string  `json:"name"`
	Enabled      bool    `json:"enabled"`
	Priority     int     `json:"priority"`
	ScopeType    string  `json:"scopeType"`
	ProtocolCode string  `json:"protocolCode"`
	ProviderCode *string `json:"providerCode,omitempty"`
	ProviderName *string `json:"providerName,omitempty"`
	Action       string  `json:"action"`
	UpdatedAt    *string `json:"updatedAt,omitempty"`
}

// InspectionDetail mirrors ResponseInspectionPolicyDetail.
type InspectionDetail struct {
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	Enabled      bool            `json:"enabled"`
	Priority     int             `json:"priority"`
	ScopeType    string          `json:"scopeType"`
	ProtocolCode string          `json:"protocolCode"`
	ProviderCode *string         `json:"providerCode,omitempty"`
	ProviderName *string         `json:"providerName,omitempty"`
	Match        InspectionMatch `json:"match"`
	Action       string          `json:"action"`
	Notes        *string         `json:"notes,omitempty"`
	UpdatedAt    *string         `json:"updatedAt,omitempty"`
}

// InspectionListResult mirrors ResponseInspectionPolicyListResult.
type InspectionListResult struct {
	DefaultRules []InspectionOverview `json:"defaultRules"`
	Policies     []InspectionOverview `json:"policies"`
}

// InspectionProviderOption mirrors ResponseInspectionPolicyProviderOption.
type InspectionProviderOption struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

// systemDefaultRule mirrors one systemDefaultRules entry.
type systemDefaultRule struct {
	id           string
	name         string
	priority     int
	scopeType    string
	protocolCode string
	providerCode string
	match        InspectionMatch
	action       string
	notes        string
}

// systemDefaultRules mirrors systemDefaultRules in
// storage/response-inspection-policy.repository.ts.
var systemDefaultRules = []systemDefaultRule{
	{
		id: "default_openai_transient_precommit_error", name: "OpenAI 首输出前短暂错误", priority: 0,
		scopeType: "protocol", protocolCode: protocolCodeOpenAI,
		match: InspectionMatch{
			"clientProfiles": {"generic_openai", "codex"},
			"errorCodes":     {"server_error", "internal_server_error", "server_overloaded", "overloaded", "service_unavailable", "temporarily_unavailable", "unavailable", "timeout", "deadline_exceeded", "resource_exhausted", "internal", "cancelled", "canceled"},
		},
		action: "retry_next_account",
		notes:  "仅限尚未向客户端提交语义输出的明确短暂上游错误；网关先按当前物理账号的有界预算重试，耗尽后再切换候选，不写长期账号状态。",
	},
	{
		id: "default_openai_context_window_error", name: "OpenAI 上下文窗口错误", priority: 1,
		scopeType: "protocol", protocolCode: protocolCodeOpenAI,
		match: InspectionMatch{
			"clientProfiles": {"generic_openai", "codex"},
			"errorCodes":     {"context_length_exceeded", "input_too_large", "max_tokens_exceeded"},
		},
		action: "retry_next_account",
		notes:  "上下文容量属于当前账号/模型约束，直接切换候选账号，不在同一账号重复提交。",
	},
	{
		id: "default_openai_error_object", name: "OpenAI error 对象", priority: 2,
		scopeType: "protocol", protocolCode: protocolCodeOpenAI,
		match:  InspectionMatch{"jsonPathsExists": {"error"}},
		action: "retry_no_avoidance",
		notes:  "OpenAI v1 JSON / SSE data.error 默认检查规则；是否允许客户端专用重试由运行时客户端能力门控。",
	},
	{
		id: "default_openai_response_error", name: "OpenAI response.error", priority: 3,
		scopeType: "protocol", protocolCode: protocolCodeOpenAI,
		match:  InspectionMatch{"jsonPathsExists": {"response.error"}},
		action: "retry_no_avoidance",
		notes:  "OpenAI v1 Responses response.error 默认检查规则。",
	},
	{
		id: "default_openai_failed_status", name: "OpenAI failed 状态", priority: 4,
		scopeType: "protocol", protocolCode: protocolCodeOpenAI,
		match:  InspectionMatch{"finishReasons": {"failed"}},
		action: "retry_no_avoidance",
		notes:  "OpenAI v1 Responses failed 状态默认检查规则。",
	},
	{
		id: "default_codex_response_incomplete", name: "Codex response.incomplete", priority: 5,
		scopeType: "protocol", protocolCode: protocolCodeOpenAI,
		match:  InspectionMatch{"clientProfiles": {"codex"}, "finishReasons": {"incomplete"}},
		action: "retry_no_avoidance",
		notes:  "Codex 客户端会把 Responses response.incomplete 当成可重试流式错误；网关在写下游前拦截为统一可重试失败，避免服务端误判成功。",
	},
	{
		id: "default_codex_compaction_contract", name: "Codex compact 输出契约", priority: 5,
		scopeType: "protocol", protocolCode: protocolCodeOpenAI,
		match:  InspectionMatch{"clientProfiles": {"codex"}, "errorCodes": {"codex_compaction_contract_mismatch"}},
		action: "retry_next_account",
		notes:  "Codex Remote Compaction V2 的本地结构契约；只接受网关生成的契约失败帧，上游同名错误码不能触发。",
	},
	{
		id: "default_gpt_cyber_policy", name: "GPT cyber_policy", priority: 6,
		scopeType: "provider", protocolCode: protocolCodeOpenAI, providerCode: vendorCodeGPT,
		match:  InspectionMatch{"errorCodes": {"cyber_policy"}},
		action: "retry_no_avoidance",
		notes:  "GPT 供应商 cyber_policy 规则，适用于该供应商的所有下游客户端；不能扩散为所有 OpenAI-compatible 供应商语义。",
	},
	{
		id: "default_anthropic_transient_precommit_error", name: "Anthropic 首输出前短暂错误", priority: 0,
		scopeType: "protocol", protocolCode: protocolCodeAnthropic,
		match: InspectionMatch{
			"clientProfiles": {"generic_anthropic", "claude_code"},
			"errorTypes":     {"api_error", "overloaded_error", "server_error", "internal_error", "service_unavailable"},
		},
		action: "retry_next_account",
		notes:  "仅限尚未向客户端提交语义输出的明确短暂上游错误；先按当前物理账号的有界预算重试，耗尽后使用与 OpenAI/Gemini 相同的候选切换机制。",
	},
	{
		id: "default_anthropic_error_object", name: "Anthropic error 对象", priority: 1,
		scopeType: "protocol", protocolCode: protocolCodeAnthropic,
		match:  InspectionMatch{"jsonPathsExists": {"error"}},
		action: "retry_no_avoidance",
		notes:  "Anthropic Messages JSON / SSE event:error 默认检查规则；错误类型只作为响应语义输入，不直接写账号状态。",
	},
	{
		id: "default_gemini_transient_precommit_error", name: "Gemini 首输出前短暂错误", priority: 0,
		scopeType: "protocol", protocolCode: protocolCodeGemini,
		match: InspectionMatch{
			"clientProfiles": {"generic_gemini", "gemini_cli"},
			"errorTypes":     {"RESOURCE_EXHAUSTED", "UNAVAILABLE", "DEADLINE_EXCEEDED", "INTERNAL", "CANCELLED"},
		},
		action: "retry_next_account",
		notes:  "仅限尚未向客户端提交语义输出的 Google canonical 短暂错误；网关先按当前物理账号的有界预算重试，耗尽后切换候选而不是把首次失败直接交给客户端。",
	},
	{
		id: "default_gemini_cli_retryable_error", name: "Gemini CLI 可重试错误", priority: 1,
		scopeType: "protocol", protocolCode: protocolCodeGemini,
		match: InspectionMatch{
			"clientProfiles": {"gemini_cli"},
			"errorTypes":     {"RESOURCE_EXHAUSTED", "UNAVAILABLE", "DEADLINE_EXCEEDED", "INTERNAL", "CANCELLED"},
		},
		action: "retry_next_account",
		notes:  "gemini-cli 已知会把 429、499、5xx 和超时类 Google canonical error 当作可重试错误；该规则只在 gemini_cli 客户端画像下请求下一个账号，不扩散到普通 Gemini 客户端。",
	},
	{
		id: "default_gemini_error_object", name: "Gemini error 对象", priority: 20,
		scopeType: "protocol", protocolCode: protocolCodeGemini,
		match:  InspectionMatch{"jsonPathsExists": {"error"}},
		action: "retry_no_avoidance",
		notes:  "Gemini JSON / SSE error 默认检查规则；错误状态只作为响应语义输入，不直接写账号状态。",
	},
}

// InspectionStore is the dual-mode response_inspection_policies persistence.
type InspectionStore struct {
	baseStore
}

// NewInspectionStore builds the inspection store; inval may be nil.
func NewInspectionStore(db *sql.DB, postgres bool, now func() time.Time, newID func(string) string, inval RuntimeInvalidator) (*InspectionStore, error) {
	base, err := newBaseStore(db, postgres, now, newID, inval)
	if err != nil {
		return nil, err
	}
	return &InspectionStore{baseStore: base}, nil
}

type inspectionOverviewRow struct {
	id           string
	name         string
	enabled      bool
	priority     int
	scopeType    string
	protocolCode string
	providerCode sql.NullString
	providerName sql.NullString
	action       string
	updatedAt    string
}

func scanInspectionOverviewRow(scan func(...any) error) (inspectionOverviewRow, error) {
	var row inspectionOverviewRow
	var enabled int
	err := scan(&row.id, &row.name, &enabled, &row.priority, &row.scopeType, &row.protocolCode,
		&row.providerCode, &row.providerName, &row.action, &row.updatedAt)
	if err != nil {
		return inspectionOverviewRow{}, err
	}
	row.enabled = enabled == 1
	return row, nil
}

type inspectionPatchRow struct {
	id           string
	name         string
	enabled      bool
	priority     int
	scopeType    string
	protocolCode string
	providerCode sql.NullString
	matchJSON    string
	action       string
	notes        sql.NullString
	updatedAt    string
}

func scanInspectionPatchRow(scan func(...any) error) (inspectionPatchRow, error) {
	var row inspectionPatchRow
	var enabled int
	err := scan(&row.id, &row.name, &enabled, &row.priority, &row.scopeType, &row.protocolCode,
		&row.providerCode, &row.matchJSON, &row.action, &row.notes, &row.updatedAt)
	if err != nil {
		return inspectionPatchRow{}, err
	}
	row.enabled = enabled == 1
	return row, nil
}

func (s *InspectionStore) providerNames(ctx context.Context, codes []string) (map[string]string, error) {
	names := map[string]string{}
	unique := uniqueSortedStrings(codes)
	if len(unique) == 0 {
		return names, nil
	}
	placeholders := make([]string, len(unique))
	args := make([]any, 0, len(unique)+1)
	for i, code := range unique {
		placeholders[i] = "?"
		args = append(args, code)
	}
	args = append(args, len(unique))
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT code, name FROM `+s.table("providers")+`
		WHERE code IN (`+strings.Join(placeholders, ",")+`)
		ORDER BY code ASC
		LIMIT ?`), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var code, name string
		if err := rows.Scan(&code, &name); err != nil {
			return nil, err
		}
		names[code] = name
	}
	return names, rows.Err()
}

func (s *InspectionStore) providerName(ctx context.Context, code *string) (*string, error) {
	if code == nil || *code == "" {
		return nil, nil
	}
	names, err := s.providerNames(ctx, []string{*code})
	if err != nil {
		return nil, err
	}
	return ptrString(names[*code]), nil
}

// ListPage mirrors listResponseInspectionPoliciesAsync: static default rules
// with provider names plus the management rows (LIMIT 100) ordered by
// priority ASC, updated_at DESC, id ASC.
func (s *InspectionStore) ListPage(ctx context.Context) (*InspectionListResult, error) {
	ctx = ensureCtx(ctx)
	defaultCodes := make([]string, 0, len(systemDefaultRules))
	for _, rule := range systemDefaultRules {
		if rule.providerCode != "" {
			defaultCodes = append(defaultCodes, rule.providerCode)
		}
	}
	defaultNames, err := s.providerNames(ctx, defaultCodes)
	if err != nil {
		return nil, err
	}
	defaultRules := make([]InspectionOverview, 0, len(systemDefaultRules))
	for _, rule := range systemDefaultRules {
		overview := inspectionOverviewFromSummary(
			rule.id, rule.name, true, false, rule.priority, rule.scopeType, rule.protocolCode,
			ptrString(rule.providerCode), rule.action, nil)
		if rule.providerCode != "" {
			overview.ProviderName = ptrString(defaultNames[rule.providerCode])
		}
		defaultRules = append(defaultRules, overview)
	}
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT rip.id, rip.name, rip.enabled, rip.priority, rip.scope_type, rip.protocol_code,
		rip.provider_code, p.name AS provider_name, rip.action, rip.updated_at
		FROM `+s.table("response_inspection_policies")+` rip
		LEFT JOIN `+s.table("providers")+` p ON p.code = rip.provider_code
		ORDER BY rip.priority ASC, rip.updated_at DESC, rip.id ASC
		LIMIT ?`), maxManagementResponseInspectionPolicies)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	policies := []InspectionOverview{}
	for rows.Next() {
		row, scanErr := scanInspectionOverviewRow(rows.Scan)
		if scanErr != nil {
			return nil, scanErr
		}
		scopeType, scanErr := normalizeScopeType(row.scopeType)
		if scanErr != nil {
			return nil, scanErr
		}
		action, scanErr := normalizeInspectionAction(row.action)
		if scanErr != nil {
			return nil, scanErr
		}
		overview := inspectionOverviewFromSummary(
			row.id, row.name, row.enabled, true, row.priority, scopeType, row.protocolCode,
			nullPtrString(row.providerCode), action, ptrString(row.updatedAt))
		overview.ProviderName = nullPtrString(row.providerName)
		policies = append(policies, overview)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return &InspectionListResult{DefaultRules: defaultRules, Policies: policies}, nil
}

func inspectionOverviewFromSummary(
	id, name string, enabled, editable bool, priority int, scopeType, protocolCode string,
	providerCode *string, action string, updatedAt *string,
) InspectionOverview {
	return InspectionOverview{
		ID: id, DefaultRule: !editable, Editable: editable, Name: name, Enabled: enabled,
		Priority: priority, ScopeType: scopeType, ProtocolCode: protocolCode,
		ProviderCode: providerCode, Action: action, UpdatedAt: updatedAt,
	}
}

// overviewFromDetail mirrors the route-local policyOverview.
func overviewFromDetail(detail *InspectionDetail) InspectionOverview {
	return InspectionOverview{
		ID: detail.ID, DefaultRule: false, Editable: true, Name: detail.Name, Enabled: detail.Enabled,
		Priority: detail.Priority, ScopeType: detail.ScopeType, ProtocolCode: detail.ProtocolCode,
		ProviderCode: detail.ProviderCode, ProviderName: detail.ProviderName, Action: detail.Action,
		UpdatedAt: detail.UpdatedAt,
	}
}

// FindDetail mirrors getResponseInspectionPolicyDetailAsync: default rules are
// answered from the static table, everything else from the management row.
func (s *InspectionStore) FindDetail(ctx context.Context, id string) (*InspectionDetail, error) {
	ctx = ensureCtx(ctx)
	normalizedID := strings.TrimSpace(id)
	if normalizedID == "" {
		return nil, nil
	}
	for _, rule := range systemDefaultRules {
		if rule.id != normalizedID {
			continue
		}
		providerCode := ptrString(rule.providerCode)
		providerName, err := s.providerName(ctx, providerCode)
		if err != nil {
			return nil, err
		}
		match := InspectionMatch{}
		for key, values := range rule.match {
			match[key] = append([]string{}, values...)
		}
		return &InspectionDetail{
			ID: rule.id, Name: rule.name, Enabled: true, Priority: rule.priority,
			ScopeType: rule.scopeType, ProtocolCode: rule.protocolCode,
			ProviderCode: providerCode, ProviderName: providerName,
			Match: match, Action: rule.action, Notes: ptrString(rule.notes),
		}, nil
	}
	var row inspectionPatchRow
	var providerName sql.NullString
	err := s.db.QueryRowContext(ctx, s.bind(`SELECT rip.id, rip.name, rip.enabled, rip.priority, rip.scope_type, rip.protocol_code,
		rip.provider_code, rip.match_json, rip.action, rip.notes, rip.updated_at,
		p.name AS provider_name
		FROM `+s.table("response_inspection_policies")+` rip
		LEFT JOIN `+s.table("providers")+` p ON p.code = rip.provider_code
		WHERE rip.id = ?`), normalizedID).Scan(&row.id, &row.name, &row.enabled, &row.priority, &row.scopeType,
		&row.protocolCode, &row.providerCode, &row.matchJSON, &row.action, &row.notes, &row.updatedAt, &providerName)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	normalized, err := s.normalizedFromPatchRow(ctx, row)
	if err != nil {
		return nil, err
	}
	detail := normalized.detail()
	detail.ProviderName = nullPtrString(providerName)
	return detail, nil
}

// ProviderOptions mirrors listResponseInspectionPolicyProviderOptionsAsync.
func (s *InspectionStore) ProviderOptions(ctx context.Context, protocolCode, scopeType, keyword string) ([]InspectionProviderOption, error) {
	ctx = ensureCtx(ctx)
	protocolCode = strings.TrimSpace(protocolCode)
	if !inspectionSupportedProtocol(protocolCode) || scopeType != "provider" {
		return []InspectionProviderOption{}, nil
	}
	keyword = strings.TrimSpace(keyword)
	where := []string{"p.enabled = 1", "ppp.enabled = 1", "ppp.protocol_code = ?"}
	args := []any{protocolCode}
	if keyword != "" {
		where = append(where, "(lower(p.code) LIKE lower(?) ESCAPE '\\' OR lower(p.name) LIKE lower(?) ESCAPE '\\')")
		pattern := escapeLikePrefix(keyword) + "%"
		args = append(args, pattern, pattern)
	}
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT DISTINCT p.code, p.name
		FROM `+s.table("providers")+` p
		INNER JOIN `+s.table("provider_protocol_profiles")+` ppp ON ppp.provider_code = p.code
		WHERE `+strings.Join(where, "\n        AND ")+`
		ORDER BY p.name ASC, p.code ASC`), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	options := []InspectionProviderOption{}
	for rows.Next() {
		var option InspectionProviderOption
		if err := rows.Scan(&option.Code, &option.Name); err != nil {
			return nil, err
		}
		options = append(options, option)
	}
	return options, rows.Err()
}

func inspectionSupportedProtocol(code string) bool {
	for _, supported := range inspectionProtocolCodes {
		if code == supported {
			return true
		}
	}
	return false
}

// isProtocolProviderCode mirrors storage/provider.repository.ts
// isProtocolProviderCode (no explicit version).
func (s *InspectionStore) isProtocolProviderCode(ctx context.Context, q queryer, providerCode, protocolCode string) (bool, error) {
	code := strings.TrimSpace(providerCode)
	if code == "" || strings.TrimSpace(protocolCode) == "" {
		return false, nil
	}
	var found int
	err := q.QueryRowContext(ctx, s.bind(`SELECT 1
		FROM `+s.table("provider_protocol_profiles")+`
		INNER JOIN `+s.table("providers")+` ON providers.code = provider_protocol_profiles.provider_code
		WHERE provider_protocol_profiles.provider_code = ?
			AND providers.enabled = 1
			AND provider_protocol_profiles.enabled = 1
			AND protocol_code = ?
		LIMIT 1`), code, strings.TrimSpace(protocolCode)).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// queryer abstracts *sql.DB / *sql.Tx reads and writes so the transactional
// paths never touch s.db while a transaction holds the connection (the SQLite
// test runtime runs with MaxOpenConns(1)).
type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func normalizeScopeType(value string) (string, error) {
	if value == "protocol" || value == "provider" {
		return value, nil
	}
	return "", &ValidationError{Message: "响应检查策略作用层级无效"}
}

func normalizeInspectionAction(value string) (string, error) {
	for _, action := range inspectionPolicyActions {
		if value == action {
			return value, nil
		}
	}
	return "", &ValidationError{Message: "响应检查策略动作无效"}
}

func requiredTextField(value any, label string, max int) (string, error) {
	text, isString := value.(string)
	if !isString {
		return "", &ValidationError{Message: label + "无效"}
	}
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return "", &ValidationError{Message: label + "不能为空"}
	}
	if runeLen(trimmed) > max {
		return "", &ValidationError{Message: label + "不能超过 " + strconv.Itoa(max) + " 个字符"}
	}
	return trimmed, nil
}

// normalizeInspectionMatch mirrors normalizeMatch.
func normalizeInspectionMatch(value any) (InspectionMatch, error) {
	record, isObject := value.(map[string]any)
	if value == nil || !isObject {
		// Node: non-object inputs normalize to an empty match and then fail
		// the hasMatcher guard below.
		record = map[string]any{}
	}
	match := InspectionMatch{}
	clientProfiles, err := normalizeKnownStringList(record["clientProfiles"], "响应检查策略clientProfiles", inspectionClientProfiles)
	if err != nil {
		return nil, err
	}
	if len(clientProfiles) > 0 {
		match["clientProfiles"] = clientProfiles
	}
	for _, key := range inspectionMatchKeys {
		if key == "clientProfiles" {
			continue
		}
		items, err := normalizeStringList(record[key], "响应检查策略"+key)
		if err != nil {
			return nil, err
		}
		if len(items) > 0 {
			match[key] = items
		}
	}
	for _, key := range inspectionPositiveMatchKeys {
		if len(match[key]) > 0 {
			return match, nil
		}
	}
	return nil, &ValidationError{Message: "响应检查策略至少需要一个匹配条件"}
}

func normalizeStringList(value any, label string) ([]string, error) {
	items, ok := asStringList(value)
	if !ok {
		if value == nil {
			return []string{}, nil
		}
		return nil, &ValidationError{Message: label + "必须是字符串数组"}
	}
	if value == nil {
		return []string{}, nil
	}
	if len(items) > 50 {
		return nil, &ValidationError{Message: label + "不能超过 50 项"}
	}
	out := []string{}
	for _, item := range items {
		trimmed := strings.TrimSpace(item)
		if trimmed == "" {
			return nil, &ValidationError{Message: label + "不能为空"}
		}
		if runeLen(trimmed) > 200 {
			return nil, &ValidationError{Message: label + "不能超过 200 个字符"}
		}
		duplicate := false
		for _, existing := range out {
			if existing == trimmed {
				duplicate = true
				break
			}
		}
		if !duplicate {
			out = append(out, trimmed)
		}
	}
	return out, nil
}

func normalizeKnownStringList(value any, label string, allowed []string) ([]string, error) {
	items, err := normalizeStringList(value, label)
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		supported := false
		for _, candidate := range allowed {
			if candidate == item {
				supported = true
				break
			}
		}
		if !supported {
			return nil, &ValidationError{Message: label + "包含不支持的值：" + item}
		}
	}
	return items, nil
}

// inspectionNormalized is the normalized policy projection shared by create
// and patch (Node ResponseInspectionPolicySummary).
type inspectionNormalized struct {
	Name         string
	Enabled      bool
	Priority     int
	ScopeType    string
	ProtocolCode string
	ProviderCode *string // nil = undefined
	Match        InspectionMatch
	Action       string
	Notes        *string // nil = undefined
	UpdatedAt    string
}

func (n *inspectionNormalized) detail() *InspectionDetail {
	match := InspectionMatch{}
	for key, values := range n.Match {
		match[key] = append([]string{}, values...)
	}
	return &InspectionDetail{
		Name: n.Name, Enabled: n.Enabled, Priority: n.Priority, ScopeType: n.ScopeType,
		ProtocolCode: n.ProtocolCode, ProviderCode: ptrDeref(n.ProviderCode), Match: match,
		Action: n.Action, Notes: ptrDeref(n.Notes), UpdatedAt: ptrString(n.UpdatedAt),
	}
}

func ptrDeref(value *string) *string {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}

// inspectionMergedInput carries the merged (current + patch) raw values in the
// tri-state shape the Node normalizer consumes.
type inspectionMergedInput struct {
	Name         string
	Enabled      bool
	Priority     int
	ScopeType    string
	ProtocolCode string
	ProviderCode any // string | nil (null/undefined)
	Match        any
	Action       string
	Notes        any // string | nil (null/undefined)
}

func providerCodeTruthy(value any) bool {
	text, isString := value.(string)
	return isString && text != ""
}

// normalizeMerged mirrors normalizePolicyInput.
func (s *InspectionStore) normalizeMerged(ctx context.Context, q queryer, input inspectionMergedInput, validateMembership bool) (*inspectionNormalized, error) {
	scopeType, err := normalizeScopeType(input.ScopeType)
	if err != nil {
		return nil, err
	}
	protocolCode, err := normalizeInspectionProtocolCode(input.ProtocolCode)
	if err != nil {
		return nil, err
	}
	var providerCode *string
	if scopeType == "provider" {
		text, err := requiredTextField(input.ProviderCode, "供应商编码", 80)
		if err != nil {
			return nil, err
		}
		providerCode = &text
	}
	if providerCode != nil && validateMembership {
		available, err := s.isProtocolProviderCode(ctx, q, *providerCode, protocolCode)
		if err != nil {
			return nil, err
		}
		if !available {
			return nil, &ValidationError{Message: "响应检查策略供应商必须使用同协议启用档案"}
		}
	}
	if scopeType == "protocol" && providerCodeTruthy(input.ProviderCode) {
		return nil, &ValidationError{Message: "协议层响应检查策略不能绑定供应商"}
	}
	name, err := requiredTextField(input.Name, "规则名称", 100)
	if err != nil {
		return nil, err
	}
	priority := input.Priority
	if err := positiveIntBounds(priority, 1, 9999, "优先级"); err != nil {
		return nil, err
	}
	match, err := normalizeInspectionMatch(input.Match)
	if err != nil {
		return nil, err
	}
	action, err := normalizeInspectionAction(input.Action)
	if err != nil {
		return nil, err
	}
	var notes *string
	if text, isString := input.Notes.(string); isString {
		value, err := requiredTextField(text, "备注", 1000)
		if err != nil {
			return nil, err
		}
		notes = &value
	}
	return &inspectionNormalized{
		Name: name, Enabled: input.Enabled, Priority: priority, ScopeType: scopeType,
		ProtocolCode: protocolCode, ProviderCode: providerCode, Match: match,
		Action: action, Notes: notes,
	}, nil
}

func positiveIntBounds(value, min, max int, label string) error {
	if value < min || value > max {
		return &ValidationError{Message: label + "必须是 " + strconv.Itoa(min) + "-" + strconv.Itoa(max) + " 的整数"}
	}
	return nil
}

func normalizeInspectionProtocolCode(value any) (string, error) {
	text, err := requiredTextField(value, "协议编码", 80)
	if err != nil {
		return "", err
	}
	if !inspectionSupportedProtocol(text) {
		return "", &ValidationError{Message: "当前响应检查策略只支持 OpenAI v1、Anthropic v1 或 Gemini v1beta 协议"}
	}
	return text, nil
}

func (s *InspectionStore) normalizedFromPatchRow(ctx context.Context, row inspectionPatchRow) (*inspectionNormalized, error) {
	scopeType, err := normalizeScopeType(row.scopeType)
	if err != nil {
		return nil, err
	}
	action, err := normalizeInspectionAction(row.action)
	if err != nil {
		return nil, err
	}
	var matchValue any
	if err := json.Unmarshal([]byte(row.matchJSON), &matchValue); err != nil {
		matchValue = nil
	}
	match, err := normalizeInspectionMatch(matchValue)
	if err != nil {
		return nil, err
	}
	var providerCode *string
	if row.providerCode.Valid && row.providerCode.String != "" {
		value := row.providerCode.String
		providerCode = &value
	}
	var notes *string
	if row.notes.Valid && row.notes.String != "" {
		value := row.notes.String
		notes = &value
	}
	return &inspectionNormalized{
		Name: row.name, Enabled: row.enabled, Priority: row.priority, ScopeType: scopeType,
		ProtocolCode: row.protocolCode, ProviderCode: providerCode, Match: match,
		Action: action, Notes: notes, UpdatedAt: row.updatedAt,
	}, nil
}

// assertCapacity mirrors assertManagementPolicyCapacity.
func (s *InspectionStore) assertCapacity(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT id FROM `+s.table("response_inspection_policies")+` LIMIT ?`),
		maxManagementResponseInspectionPolicies+1)
	if err != nil {
		return err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		count++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if count >= maxManagementResponseInspectionPolicies {
		return &ValidationError{Message: "响应检查策略最多允许 " + strconv.Itoa(maxManagementResponseInspectionPolicies) + " 条"}
	}
	return nil
}

// Create mirrors createResponseInspectionPolicyAsync.
func (s *InspectionStore) Create(ctx context.Context, input *InspectionCreateInput) (*InspectionDetail, error) {
	ctx = ensureCtx(ctx)
	if err := s.assertCapacity(ctx); err != nil {
		return nil, err
	}
	merged := inspectionMergedInput{
		Name:         input.Name,
		Enabled:      input.Enabled == nil || *input.Enabled,
		Priority:     100,
		ScopeType:    input.ScopeType,
		ProtocolCode: input.ProtocolCode,
		ProviderCode: nullableAny(input.ProviderCode),
		Match:        input.Match,
		Action:       input.Action,
		Notes:        nullableAny(input.Notes),
	}
	if input.Priority != nil {
		merged.Priority = *input.Priority
	}
	normalized, err := s.normalizeMerged(ctx, s.db, merged, true)
	if err != nil {
		return nil, err
	}
	now := s.nowISO()
	id := s.generateID("rip")
	matchJSON, err := json.Marshal(normalized.Match)
	if err != nil {
		return nil, err
	}
	if _, err := s.db.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("response_inspection_policies")+`
		(id, name, enabled, priority, scope_type, protocol_code, provider_code, match_json, action, notes, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		id, normalized.Name, boolToInt(normalized.Enabled), normalized.Priority, normalized.ScopeType,
		normalized.ProtocolCode, normalized.ProviderCode, string(matchJSON), normalized.Action,
		normalized.Notes, now, now); err != nil {
		return nil, err
	}
	s.invalidateRuntime("response_inspection_policy_created")
	normalized.UpdatedAt = now
	detail := normalized.detail()
	detail.ID = id
	detail.ProviderName, err = s.providerName(ctx, normalized.ProviderCode)
	if err != nil {
		return nil, err
	}
	return detail, nil
}

// InspectionCreateInput is the zod-validated POST payload; nil pointers mean
// "absent or null" exactly like the optional/nullable zod fields.
type InspectionCreateInput struct {
	Name         string
	Enabled      *bool
	Priority     *int
	ScopeType    string
	ProtocolCode string
	ProviderCode *string // nil = absent or null
	Match        any
	Action       string
	Notes        *string // nil = absent or null
}

// InspectionPatch is the zod-validated PATCH payload; SetFields tracks which
// patchable fields are present (expectedUpdatedAt excluded). Present-as-null
// fields (providerCode, notes) carry a nil value with the flag set.
type InspectionPatch struct {
	SetFields    map[string]bool
	Name         *string
	Enabled      *bool
	Priority     *int
	ScopeType    *string
	ProtocolCode *string
	ProviderCode any // string when present; nil when present-as-null
	Match        any
	Action       *string
	Notes        any // string when present; nil when present-as-null
	ExpectedAt   string
}

// InspectionPatchOutcome mirrors ResponseInspectionPolicyPatchOutcome.
type InspectionPatchOutcome struct {
	Status        string // not_found | conflict | noop | updated
	Current       *InspectionDetail
	Policy        *InspectionDetail
	ChangedFields []string
}

// Patch mirrors patchResponseInspectionPolicyAsync.
func (s *InspectionStore) Patch(ctx context.Context, id string, patch *InspectionPatch) (*InspectionPatchOutcome, error) {
	ctx = ensureCtx(ctx)
	var row inspectionPatchRow
	err := s.db.QueryRowContext(ctx, s.bind(`SELECT id, name, enabled, priority, scope_type, protocol_code,
		provider_code, match_json, action, notes, updated_at
		FROM `+s.table("response_inspection_policies")+` WHERE id = ?`), id).
		Scan(&row.id, &row.name, &row.enabled, &row.priority, &row.scopeType, &row.protocolCode,
			&row.providerCode, &row.matchJSON, &row.action, &row.notes, &row.updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return &InspectionPatchOutcome{Status: "not_found"}, nil
	}
	if err != nil {
		return nil, err
	}
	if row.updatedAt != patch.ExpectedAt {
		return &InspectionPatchOutcome{Status: "conflict"}, nil
	}
	current, err := s.normalizedFromPatchRow(ctx, row)
	if err != nil {
		return nil, err
	}
	merged := inspectionMergedInput{
		Name: current.Name, Enabled: current.Enabled, Priority: current.Priority,
		ScopeType: current.ScopeType, ProtocolCode: current.ProtocolCode,
		ProviderCode: nullableAny(current.ProviderCode), Match: matchAsAny(current.Match),
		Action: current.Action, Notes: nullableAny(current.Notes),
	}
	if patch.SetFields["name"] {
		merged.Name = *patch.Name
	}
	if patch.SetFields["enabled"] {
		merged.Enabled = *patch.Enabled
	}
	if patch.SetFields["priority"] {
		merged.Priority = *patch.Priority
	}
	if patch.SetFields["scopeType"] {
		merged.ScopeType = *patch.ScopeType
	}
	if patch.SetFields["protocolCode"] {
		merged.ProtocolCode = *patch.ProtocolCode
	}
	if patch.SetFields["providerCode"] {
		merged.ProviderCode = patch.ProviderCode
	}
	if patch.SetFields["match"] {
		merged.Match = patch.Match
	}
	if patch.SetFields["action"] {
		merged.Action = *patch.Action
	}
	if patch.SetFields["notes"] {
		merged.Notes = patch.Notes
	}
	validateMembership := patch.SetFields["scopeType"] || patch.SetFields["protocolCode"] || patch.SetFields["providerCode"]
	next, err := s.normalizeMerged(ctx, s.db, merged, validateMembership)
	if err != nil {
		return nil, err
	}
	currentProviderName, err := s.providerName(ctx, current.ProviderCode)
	if err != nil {
		return nil, err
	}
	currentDetail := current.detail()
	currentDetail.ProviderName = currentProviderName

	nextUpdatedAt, err := nextRFC3339Millis(patch.ExpectedAt, s.now(), "响应检查策略 updatedAt 必须是带 Z 或数值 offset 的 RFC3339 时间")
	if err != nil {
		return nil, err
	}

	assignments := []string{}
	values := []any{}
	changedFields := []string{}
	addChange := func(field, column string, value any) {
		assignments = append(assignments, column+" = ?")
		values = append(values, value)
		changedFields = append(changedFields, field)
	}
	if patch.SetFields["name"] && current.Name != next.Name {
		addChange("name", "name", next.Name)
	}
	if patch.SetFields["enabled"] && current.Enabled != next.Enabled {
		addChange("enabled", "enabled", boolToInt(next.Enabled))
	}
	if patch.SetFields["priority"] && current.Priority != next.Priority {
		addChange("priority", "priority", next.Priority)
	}
	if patch.SetFields["scopeType"] && current.ScopeType != next.ScopeType {
		addChange("scopeType", "scope_type", next.ScopeType)
	}
	if patch.SetFields["protocolCode"] && current.ProtocolCode != next.ProtocolCode {
		addChange("protocolCode", "protocol_code", next.ProtocolCode)
	}
	if patch.SetFields["providerCode"] && !sameNullable(current.ProviderCode, next.ProviderCode) {
		addChange("providerCode", "provider_code", next.ProviderCode)
	}
	if patch.SetFields["match"] && !matchEqual(current.Match, next.Match) {
		matchJSON, err := json.Marshal(next.Match)
		if err != nil {
			return nil, err
		}
		addChange("match", "match_json", string(matchJSON))
	}
	if patch.SetFields["action"] && current.Action != next.Action {
		addChange("action", "action", next.Action)
	}
	if patch.SetFields["notes"] && !sameNullable(current.Notes, next.Notes) {
		addChange("notes", "notes", next.Notes)
	}

	if len(assignments) == 0 {
		return &InspectionPatchOutcome{
			Status: "noop", Current: currentDetail, Policy: currentDetail, ChangedFields: []string{},
		}, nil
	}
	values = append(values, nextUpdatedAt, id, patch.ExpectedAt)
	update, err := s.db.ExecContext(ctx, s.bind(`UPDATE `+s.table("response_inspection_policies")+`
		SET `+strings.Join(assignments, ", ")+", updated_at = ? WHERE id = ? AND updated_at = ?"), values...)
	if err != nil {
		return nil, err
	}
	if affected, _ := update.RowsAffected(); affected == 0 {
		return &InspectionPatchOutcome{Status: "conflict"}, nil
	}
	s.invalidateRuntime("response_inspection_policy_updated")
	next.UpdatedAt = nextUpdatedAt
	policyDetail := next.detail()
	if sameNullable(current.ProviderCode, next.ProviderCode) {
		policyDetail.ProviderName = currentProviderName
	} else {
		providerName, err := s.providerName(ctx, next.ProviderCode)
		if err != nil {
			return nil, err
		}
		policyDetail.ProviderName = providerName
	}
	return &InspectionPatchOutcome{
		Status: "updated", Current: currentDetail, Policy: policyDetail, ChangedFields: changedFields,
	}, nil
}

func nullableAny(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

func matchAsAny(match InspectionMatch) any {
	if match == nil {
		return nil
	}
	converted := map[string]any{}
	for key, values := range match {
		list := make([]any, len(values))
		for i, value := range values {
			list[i] = value
		}
		converted[key] = list
	}
	return converted
}

func sameNullable(current, next *string) bool {
	if current == nil || *current == "" {
		return next == nil || *next == ""
	}
	return next != nil && *current == *next
}

func matchEqual(left, right InspectionMatch) bool {
	leftJSON, err := json.Marshal(left)
	if err != nil {
		return false
	}
	rightJSON, err := json.Marshal(right)
	if err != nil {
		return false
	}
	return string(leftJSON) == string(rightJSON)
}

// Delete mirrors deleteResponseInspectionPolicyAsync.
func (s *InspectionStore) Delete(ctx context.Context, id string) (bool, error) {
	ctx = ensureCtx(ctx)
	result, err := s.db.ExecContext(ctx, s.bind(`DELETE FROM `+s.table("response_inspection_policies")+` WHERE id = ?`), id)
	if err != nil {
		return false, err
	}
	deleted, _ := result.RowsAffected()
	if deleted > 0 {
		s.invalidateRuntime("response_inspection_policy_deleted")
	}
	return deleted > 0, nil
}

// ---------------------------------------------------------------------------
// M16a route family (mounted behind requireAdmin, like the Node
// `app.use(prefix + "/response-inspection-policies", requireAdmin, router)`).
// ---------------------------------------------------------------------------

// InspectionDeps bundles the M16a collaborators.
type InspectionDeps struct {
	Store *InspectionStore
	Auth  *authsys.Deps
	Sink  authsys.OperationLogSink
}

// Mount wires the response-inspection-policies route family.
func (d *InspectionDeps) Mount(k *kernel.Kernel) {
	k.Register("GET "+inspectionPrefix, d.Auth.RequireAdmin(http.HandlerFunc(d.list)))
	k.Register("GET "+inspectionPrefix+"/provider-options", d.Auth.RequireAdmin(http.HandlerFunc(d.providerOptions)))
	k.Register("GET "+inspectionPrefix+"/{id}", d.Auth.RequireAdmin(http.HandlerFunc(d.detail)))
	k.Register("POST "+inspectionPrefix, d.Auth.RequireAdmin(d.guardedCreate()))
	k.Register("PATCH "+inspectionPrefix+"/{id}", d.Auth.RequireAdmin(d.guardedPatch()))
	k.Register("DELETE "+inspectionPrefix+"/{id}", d.Auth.RequireAdmin(d.guardedDelete()))
}

func (d *InspectionDeps) list(w http.ResponseWriter, r *http.Request) {
	result, err := d.Store.ListPage(r.Context())
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	kernel.WriteOK(w, result, "")
}

func (d *InspectionDeps) providerOptions(w http.ResponseWriter, r *http.Request) {
	protocolCode, scopeType, keyword, message := parseInspectionProviderOptionsQuery(r.URL.Query())
	if message != "" {
		kernel.WriteBadRequest(w, message)
		return
	}
	options, err := d.Store.ProviderOptions(r.Context(), protocolCode, scopeType, keyword)
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	kernel.WriteOK(w, options, "")
}

func (d *InspectionDeps) detail(w http.ResponseWriter, r *http.Request) {
	detail, err := d.Store.FindDetail(r.Context(), r.PathValue("id"))
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	if detail == nil {
		kernel.WriteError(w, http.StatusNotFound, "响应检查策略不存在")
		return
	}
	kernel.WriteOK(w, detail, "")
}

func (d *InspectionDeps) guardedCreate() http.Handler {
	guard := kernel.MutationGuardMiddleware(kernel.MutationGuardOptions{
		OperationKey: "response_inspection_policies.create",
		Actor:        policyreadsActorResolver,
		Fingerprint: func(r *http.Request) (any, error) {
			return map[string]any{"payload": kernel.ParsedBody(r)}, nil
		},
	})
	handler := guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if !kernel.DecodeJSON(w, r, &body) {
			return
		}
		input, message := parseInspectionCreateBody(body)
		if message != "" {
			kernel.WriteBadRequest(w, message)
			return
		}
		policy, err := d.Store.Create(r.Context(), input)
		if err != nil {
			kernel.WriteBadRequest(w, storeErrorMessage(err, "响应检查策略创建失败"))
			return
		}
		d.recordPolicyOperation(r, "create", policy.ID, policy.Name, []authsys.OperationLogChange{
			{Field: "name", Label: "规则名称", After: policy.Name},
			{Field: "protocolCode", Label: "协议", After: policy.ProtocolCode},
			{Field: "scopeType", Label: "作用层级", After: policy.ScopeType},
			{Field: "providerCode", Label: "供应商", After: safeChangeText(policy.ProviderCode)},
			{Field: "enabled", Label: "启用状态", After: safeChangeText(policy.Enabled)},
			{Field: "priority", Label: "优先级", After: safeChangeText(policy.Priority)},
		})
		writeCreatedOK(w, overviewFromDetail(policy))
	}))
	return handler
}

func (d *InspectionDeps) guardedPatch() http.Handler {
	guard := kernel.MutationGuardMiddleware(kernel.MutationGuardOptions{
		OperationKey: "response_inspection_policies.update",
		Actor:        policyreadsActorResolver,
		Fingerprint: func(r *http.Request) (any, error) {
			return map[string]any{"id": r.PathValue("id"), "payload": kernel.ParsedBody(r)}, nil
		},
	})
	handler := guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		var body map[string]any
		if !kernel.DecodeJSON(w, r, &body) {
			return
		}
		patch, message := parseInspectionPatchBody(body)
		if message != "" {
			kernel.WriteBadRequest(w, message)
			return
		}
		outcome, err := d.Store.Patch(r.Context(), id, patch)
		if err != nil {
			kernel.WriteBadRequest(w, storeErrorMessage(err, "响应检查策略更新失败"))
			return
		}
		switch outcome.Status {
		case "not_found":
			kernel.WriteError(w, http.StatusNotFound, "响应检查策略不存在")
			return
		case "conflict":
			kernel.WriteError(w, http.StatusConflict, "响应检查策略已被其他操作更新，请刷新后重试")
			return
		case "updated":
			d.recordPolicyOperation(r, "update", outcome.Policy.ID, outcome.Policy.Name,
				inspectionOperationChanges(outcome.Current, outcome.Policy, outcome.ChangedFields))
		}
		kernel.WriteOK(w, overviewFromDetail(outcome.Policy), "")
	}))
	return handler
}

func (d *InspectionDeps) guardedDelete() http.Handler {
	guard := kernel.MutationGuardMiddleware(kernel.MutationGuardOptions{
		OperationKey: "response_inspection_policies.delete",
		Actor:        policyreadsActorResolver,
		Fingerprint: func(r *http.Request) (any, error) {
			return map[string]any{"id": r.PathValue("id")}, nil
		},
	})
	handler := guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		deleted, err := d.Store.Delete(r.Context(), id)
		if err != nil {
			kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		if !deleted {
			kernel.WriteError(w, http.StatusNotFound, "响应检查策略不存在")
			return
		}
		d.recordPolicyOperation(r, "delete", id, id, []authsys.OperationLogChange{
			{Field: "deleted", Label: "删除", After: "true"},
		})
		kernel.WriteOK(w, map[string]any{"deleted": true}, "")
	}))
	return handler
}

func (d *InspectionDeps) recordPolicyOperation(r *http.Request, action, policyID, policyName string, changes []authsys.OperationLogChange) {
	if d.Sink == nil {
		return
	}
	auth := authsys.AuthContextFrom(r)
	if auth == nil {
		return
	}
	actionText := "删除"
	if action == "create" {
		actionText = "创建"
	} else if action == "update" {
		actionText = "更新"
	}
	d.Sink.Record(authsys.OperationLogEntry{
		ActorSystemAccountID: auth.SystemAccountID,
		ActorUsername:        auth.Username,
		ActorDisplayName:     auth.DisplayName,
		ActorRole:            auth.Role,
		Mode:                 "admin",
		Module:               "response_inspection_policies",
		Action:               action,
		OperationKey:         "response_inspection_policies." + action,
		ResourceType:         "response_inspection_policy",
		ResourceID:           policyID,
		ResourceName:         policyName,
		Summary:              actionText + "响应检查策略：" + policyName,
		Changes:              changes,
	}, r)
}

var inspectionChangeLabels = map[string]string{
	"name": "规则名称", "enabled": "启用状态", "priority": "优先级", "scopeType": "作用层级",
	"protocolCode": "协议", "providerCode": "供应商", "match": "匹配条件", "action": "处置动作", "notes": "备注",
}

// inspectionOperationChanges mirrors policyOperationChanges + safeChange.
func inspectionOperationChanges(current, policy *InspectionDetail, fields []string) []authsys.OperationLogChange {
	changes := make([]authsys.OperationLogChange, 0, len(fields))
	for _, field := range fields {
		label := inspectionChangeLabels[field]
		changes = append(changes, authsys.OperationLogChange{
			Field:  field,
			Label:  label,
			Before: inspectionFieldValue(field, current),
			After:  inspectionFieldValue(field, policy),
		})
	}
	return changes
}

func inspectionFieldValue(field string, detail *InspectionDetail) string {
	switch field {
	case "name":
		return safeChangeText(detail.Name)
	case "enabled":
		return safeChangeText(detail.Enabled)
	case "priority":
		return safeChangeText(detail.Priority)
	case "scopeType":
		return safeChangeText(detail.ScopeType)
	case "protocolCode":
		return safeChangeText(detail.ProtocolCode)
	case "providerCode":
		return safeChangeText(detail.ProviderCode)
	case "match":
		return safeChangeText(detail.Match)
	case "action":
		return safeChangeText(detail.Action)
	case "notes":
		return safeChangeText(detail.Notes)
	default:
		return ""
	}
}

func storeErrorMessage(err error, fallback string) string {
	var validation *ValidationError
	var conflict *ConflictError
	if errors.As(err, &validation) {
		return validation.Message
	}
	if errors.As(err, &conflict) {
		return conflict.Message
	}
	if err.Error() != "" && !errors.Is(err, errUnknownStoreFailure) {
		return err.Error()
	}
	return fallback
}

var errUnknownStoreFailure = errors.New("unknown store failure")

func policyreadsActorResolver(r *http.Request) string {
	if auth := authsys.AuthContextFrom(r); auth != nil {
		return auth.SystemAccountID
	}
	return "anonymous"
}

// createdEnvelope mirrors res.status(201).json(ok(data)).
type createdEnvelope struct {
	Data any `json:"data"`
}

func writeCreatedOK(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(createdEnvelope{Data: data})
}

// ---------------------------------------------------------------------------
// M16a zod schema mirrors (zod v3 messages).
// ---------------------------------------------------------------------------

// parseInspectionProviderOptionsQuery mirrors providerOptionsQuerySchema.
func parseInspectionProviderOptionsQuery(query map[string][]string) (protocolCode, scopeType, keyword, message string) {
	for key := range query {
		switch key {
		case "protocolCode", "scopeType", "keyword":
		default:
			return "", "", "", zodUnrecognizedKeys([]string{key})
		}
	}
	rawProtocol, hasProtocol := firstQueryValue(query, "protocolCode")
	if !hasProtocol {
		return "", "", "", "请选择响应检查策略协议"
	}
	if len(query["protocolCode"]) > 1 {
		return "", "", "", "响应检查策略协议无效"
	}
	if !inspectionSupportedProtocol(rawProtocol) {
		return "", "", "", zodEnumMessage(inspectionProtocolCodes, rawProtocol)
	}
	rawScope, hasScope := firstQueryValue(query, "scopeType")
	if !hasScope {
		return "", "", "", zodRequired
	}
	if len(query["scopeType"]) > 1 {
		return "", "", "", zodInvalidType("string", []any{})
	}
	if rawScope != "provider" && rawScope != "protocol" {
		return "", "", "", zodEnumMessage([]string{"provider", "protocol"}, rawScope)
	}
	keywordValue, hasKeyword := firstQueryValue(query, "keyword")
	if hasKeyword {
		if len(query["keyword"]) > 1 {
			return "", "", "", zodInvalidType("string", []any{})
		}
		trimmed := strings.TrimSpace(keywordValue)
		if runeLen(trimmed) > 80 {
			return "", "", "", zodStringMax(80)
		}
		keyword = trimmed
	}
	return rawProtocol, rawScope, keyword, ""
}

func firstQueryValue(query map[string][]string, key string) (string, bool) {
	values, exists := query[key]
	if !exists || len(values) == 0 {
		return "", false
	}
	return values[0], true
}

// parseInspectionCreateBody mirrors policyBodySchema (shape issues in
// definition order, then unknown keys, then superRefine issues).
func parseInspectionCreateBody(body map[string]any) (*InspectionCreateInput, string) {
	input := &InspectionCreateInput{}
	// name: z.string().trim().min(1, '规则名称不能为空').max(100, '...')
	raw, present := body["name"]
	if !present {
		return nil, zodRequired
	}
	name, isString := raw.(string)
	if !isString {
		return nil, zodInvalidType("string", raw)
	}
	input.Name = strings.TrimSpace(name)
	if input.Name == "" {
		return nil, "规则名称不能为空"
	}
	if runeLen(input.Name) > 100 {
		return nil, "规则名称不能超过 100 个字符"
	}
	// enabled: z.boolean().optional()
	if value, exists := body["enabled"]; exists && value != nil {
		enabled, isBool := value.(bool)
		if !isBool {
			return nil, zodInvalidType("boolean", value)
		}
		input.Enabled = &enabled
	}
	// priority: z.number().int().min(1).max(9999).optional()
	if value, exists := body["priority"]; exists && value != nil {
		number, isNumber := value.(float64)
		if !isNumber {
			return nil, zodInvalidType("number", value)
		}
		if number != float64(int64(number)) {
			return nil, "Expected integer, received float"
		}
		intValue := int(number)
		if intValue < 1 {
			return nil, zodNumberMin(1)
		}
		if intValue > 9999 {
			return nil, zodNumberMax(9999)
		}
		input.Priority = &intValue
	}
	// scopeType: z.enum(['protocol','provider'], {required_error, invalid_type_error})
	raw, present = body["scopeType"]
	if !present {
		return nil, "请选择响应检查策略作用层级"
	}
	scopeText, isString := raw.(string)
	if !isString {
		return nil, "响应检查策略作用层级无效"
	}
	if scopeText != "protocol" && scopeText != "provider" {
		return nil, zodEnumMessage([]string{"protocol", "provider"}, scopeText)
	}
	input.ScopeType = scopeText
	// protocolCode: z.enum([...], {required_error, invalid_type_error})
	raw, present = body["protocolCode"]
	if !present {
		return nil, "请选择响应检查策略协议"
	}
	protocolText, isString := raw.(string)
	if !isString {
		return nil, "响应检查策略协议无效"
	}
	if !inspectionSupportedProtocol(protocolText) {
		return nil, zodEnumMessage(inspectionProtocolCodes, protocolText)
	}
	input.ProtocolCode = protocolText
	// providerCode: z.string().trim().min(1, ...).max(80, ...).nullable().optional()
	if value, exists := body["providerCode"]; exists && value != nil {
		text, isString := value.(string)
		if !isString {
			return nil, zodInvalidType("string", value)
		}
		trimmed := strings.TrimSpace(text)
		if trimmed == "" {
			return nil, "请选择供应商"
		}
		if runeLen(trimmed) > 80 {
			return nil, "供应商编码不能超过 80 个字符"
		}
		input.ProviderCode = &trimmed
	}
	// match: matchSchema.optional()
	if value, exists := body["match"]; exists && value != nil {
		message := validateInspectionMatchSchema(value)
		if message != "" {
			return nil, message
		}
		input.Match = value
	} else {
		input.Match = map[string]any{}
	}
	// action: z.enum([...])
	raw, present = body["action"]
	if !present {
		return nil, zodRequired
	}
	actionText, isString := raw.(string)
	if !isString {
		return nil, zodInvalidType("string", raw)
	}
	if !containsString(inspectionPolicyActions, actionText) {
		return nil, zodEnumMessage(inspectionPolicyActions, actionText)
	}
	input.Action = actionText
	// notes: z.string().trim().max(1000, ...).nullable().optional()
	if value, exists := body["notes"]; exists && value != nil {
		text, isString := value.(string)
		if !isString {
			return nil, zodInvalidType("string", value)
		}
		trimmed := strings.TrimSpace(text)
		if runeLen(trimmed) > 1000 {
			return nil, "备注不能超过 1000 个字符"
		}
		input.Notes = &trimmed
	}
	// strict(): unknown keys after the shape pass.
	if message := inspectionUnknownBodyKey(body, inspectionCreateBodyKeys); message != "" {
		return nil, message
	}
	// superRefine.
	if input.ScopeType == "protocol" && input.ProviderCode != nil && *input.ProviderCode != "" {
		return nil, "协议层响应检查策略不能绑定供应商"
	}
	if input.ScopeType == "provider" && (input.ProviderCode == nil || *input.ProviderCode == "") {
		return nil, "供应商层响应检查策略必须选择供应商"
	}
	hasMatcher := false
	matchMap, _ := input.Match.(map[string]any)
	for _, key := range inspectionPositiveMatchKeys {
		items, _ := asStringList(matchMap[key])
		if len(items) > 0 {
			hasMatcher = true
			break
		}
	}
	if !hasMatcher {
		return nil, "至少需要填写一个匹配条件"
	}
	return input, ""
}

// validateInspectionMatchSchema mirrors matchSchema (strict + partial).
func validateInspectionMatchSchema(value any) string {
	match, isObject := value.(map[string]any)
	if !isObject {
		return zodInvalidType("object", value)
	}
	for key := range match {
		switch key {
		case "clientProfiles", "outputTextIncludes", "outputTextExcludes", "errorCodes",
			"errorTypes", "errorMessageIncludes", "finishReasons", "jsonPathsExists", "rawTextIncludes":
		default:
			return zodUnrecognizedKeys([]string{key})
		}
	}
	for _, key := range inspectionMatchKeys {
		raw, exists := match[key]
		if !exists || raw == nil {
			continue
		}
		items, isList := raw.([]any)
		if !isList {
			return zodInvalidType("array", raw)
		}
		maxItems := 50
		if key == "clientProfiles" {
			maxItems = 6
		}
		for _, item := range items {
			text, isString := item.(string)
			if !isString {
				return zodInvalidType("string", item)
			}
			trimmed := strings.TrimSpace(text)
			if trimmed == "" {
				return zodStringMin(1)
			}
			if runeLen(trimmed) > 200 {
				return zodStringMax(200)
			}
			if key == "clientProfiles" && !containsString(inspectionClientProfiles, trimmed) {
				return zodEnumMessage(inspectionClientProfiles, trimmed)
			}
		}
		if len(items) > maxItems {
			return zodArrayMax(maxItems)
		}
	}
	return ""
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

var inspectionCreateBodyKeys = []string{"name", "enabled", "priority", "scopeType", "protocolCode", "providerCode", "match", "action", "notes"}

func inspectionUnknownBodyKey(body map[string]any, allowed []string) string {
	unknown := []string{}
	for key := range body {
		if !containsString(allowed, key) {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) == 0 {
		return ""
	}
	return zodUnrecognizedKeys(unknown)
}

// parseInspectionPatchBody mirrors policyPatchSchema (strict + refine).
func parseInspectionPatchBody(body map[string]any) (*InspectionPatch, string) {
	patch := &InspectionPatch{SetFields: map[string]bool{}}
	hasChange := false
	// expectedUpdatedAt: rfc3339InstantSchema('响应检查策略版本无效')
	raw, present := body["expectedUpdatedAt"]
	if !present {
		return nil, zodRequired
	}
	expectedText, isString := raw.(string)
	if !isString {
		return nil, zodInvalidType("string", raw)
	}
	canonical, ok := canonicalRFC3339Millis(expectedText)
	if !ok {
		return nil, "响应检查策略版本无效"
	}
	patch.ExpectedAt = canonical
	// name
	if value, exists := body["name"]; exists && value != nil {
		text, isString := value.(string)
		if !isString {
			return nil, zodInvalidType("string", value)
		}
		trimmed := strings.TrimSpace(text)
		if trimmed == "" {
			return nil, "规则名称不能为空"
		}
		if runeLen(trimmed) > 100 {
			return nil, "规则名称不能超过 100 个字符"
		}
		patch.Name = &trimmed
		patch.SetFields["name"] = true
		hasChange = true
	}
	// enabled
	if value, exists := body["enabled"]; exists && value != nil {
		enabled, isBool := value.(bool)
		if !isBool {
			return nil, zodInvalidType("boolean", value)
		}
		patch.Enabled = &enabled
		patch.SetFields["enabled"] = true
		hasChange = true
	}
	// priority
	if value, exists := body["priority"]; exists && value != nil {
		number, isNumber := value.(float64)
		if !isNumber {
			return nil, zodInvalidType("number", value)
		}
		if number != float64(int64(number)) {
			return nil, "Expected integer, received float"
		}
		intValue := int(number)
		if intValue < 1 {
			return nil, zodNumberMin(1)
		}
		if intValue > 9999 {
			return nil, zodNumberMax(9999)
		}
		patch.Priority = &intValue
		patch.SetFields["priority"] = true
		hasChange = true
	}
	// scopeType: default enum messages on patch
	if value, exists := body["scopeType"]; exists && value != nil {
		text, isString := value.(string)
		if !isString {
			return nil, zodInvalidType("string", value)
		}
		if text != "protocol" && text != "provider" {
			return nil, zodEnumMessage([]string{"protocol", "provider"}, text)
		}
		patch.ScopeType = &text
		patch.SetFields["scopeType"] = true
		hasChange = true
	}
	// protocolCode: default enum messages on patch
	if value, exists := body["protocolCode"]; exists && value != nil {
		text, isString := value.(string)
		if !isString {
			return nil, zodInvalidType("string", value)
		}
		if !inspectionSupportedProtocol(text) {
			return nil, zodEnumMessage(inspectionProtocolCodes, text)
		}
		patch.ProtocolCode = &text
		patch.SetFields["protocolCode"] = true
		hasChange = true
	}
	// providerCode: nullable optional
	if value, exists := body["providerCode"]; exists {
		if value == nil {
			patch.ProviderCode = nil
			patch.SetFields["providerCode"] = true
			hasChange = true
		} else {
			text, isString := value.(string)
			if !isString {
				return nil, zodInvalidType("string", value)
			}
			trimmed := strings.TrimSpace(text)
			if trimmed == "" {
				return nil, "请选择供应商"
			}
			if runeLen(trimmed) > 80 {
				return nil, "供应商编码不能超过 80 个字符"
			}
			patch.ProviderCode = trimmed
			patch.SetFields["providerCode"] = true
			hasChange = true
		}
	}
	// match
	if value, exists := body["match"]; exists && value != nil {
		if message := validateInspectionMatchSchema(value); message != "" {
			return nil, message
		}
		patch.Match = value
		patch.SetFields["match"] = true
		hasChange = true
	}
	// action: default enum messages on patch
	if value, exists := body["action"]; exists && value != nil {
		text, isString := value.(string)
		if !isString {
			return nil, zodInvalidType("string", value)
		}
		if !containsString(inspectionPolicyActions, text) {
			return nil, zodEnumMessage(inspectionPolicyActions, text)
		}
		patch.Action = &text
		patch.SetFields["action"] = true
		hasChange = true
	}
	// notes: nullable optional
	if value, exists := body["notes"]; exists {
		if value == nil {
			patch.Notes = nil
			patch.SetFields["notes"] = true
			hasChange = true
		} else {
			text, isString := value.(string)
			if !isString {
				return nil, zodInvalidType("string", value)
			}
			trimmed := strings.TrimSpace(text)
			if runeLen(trimmed) > 1000 {
				return nil, "备注不能超过 1000 个字符"
			}
			patch.Notes = trimmed
			patch.SetFields["notes"] = true
			hasChange = true
		}
	}
	// strict(): unknown keys after the shape pass (expectedUpdatedAt included).
	if message := inspectionUnknownBodyKey(body, append(inspectionCreateBodyKeys, "expectedUpdatedAt")); message != "" {
		return nil, message
	}
	// refine: at least one field beyond expectedUpdatedAt.
	if !hasChange {
		return nil, "至少需要提交一个变化字段"
	}
	return patch, ""
}
