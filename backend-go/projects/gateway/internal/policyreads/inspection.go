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
	"database/sql"
	"errors"
	"strings"
	"time"
)

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
		id: "default_gpt_upstream_error", name: "GPT 上游限流/过载错误", priority: 0,
		scopeType: "provider", protocolCode: protocolCodeOpenAI, providerCode: vendorCodeGPT,
		match: InspectionMatch{
			"errorTypes":           {"upstream_error", "server_error"},
			"errorMessageIncludes": {"overloaded"},
		},
		action: "retry_no_avoidance",
		notes:  "upstream_error 是上游包装层的流内失败帧标记（type=upstream_error 无 code），不只代表过载——为避免误杀，本规则在 type 之上叠加 errorMessageIncludes=overloaded（AND），仅上游过载文案（'Our servers are currently overloaded'）原地有界重试（瞬态白名单授予同账号重试资格），不切换账户；其余 upstream_error 帧落通用 error 对象兜底交客户端。server_error 兼容直连 OpenAI 官方账户（官方原生过载 type，中转包装场景见 upstream_error）。供应商可预知，按供应商级配置（同 default_gpt_cyber_policy 先例）。",
	},
	{
		id: "default_openai_transient_precommit_error_type", name: "OpenAI 首输出前短暂错误（官方原生 type）", priority: 0,
		scopeType: "protocol", protocolCode: protocolCodeOpenAI,
		match: InspectionMatch{
			"clientProfiles": {"generic_openai", "codex"},
			"errorTypes":     {"server_error"},
		},
		action: "retry_next_account",
		notes:  "直连官方账户的流内错误帧为官方原生形态 type=server_error 且 code 通常为空，仅配 errorCodes 的瞬态规则接不到（改写型中转见 default_gpt_upstream_error / upstream_error）。server_error 是官方定义的 5xx 可重试错误；语义与 Anthropic/Gemini 的 errorTypes 瞬态规则同构。匹配谓词为 AND，故与仅配 errorCodes 的规则拆分为两条。",
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
