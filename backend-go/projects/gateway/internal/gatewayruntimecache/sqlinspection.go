package gatewayruntimecache

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
)

// ---------------------------------------------------------------------------
// active response inspection policies for the gateway (Node
// listActiveResponseInspectionPoliciesForGateway + systemDefaultRules)
// ---------------------------------------------------------------------------

// ListActiveResponseInspectionPolicies mirrors
// listActiveResponseInspectionPoliciesForGateway: the static default rules
// matching the gateway scope first, then the stored enabled rows.
func (m *SQLReadModels) ListActiveResponseInspectionPolicies(ctx context.Context, protocolCode string, providerCode string) ([]ResponseInspectionPolicySummary, error) {
	ctx = ensureModelCtx(ctx)
	normalizedProtocol := normalizeGatewayPolicyProtocolCode(protocolCode)
	if normalizedProtocol == "" {
		return []ResponseInspectionPolicySummary{}, nil
	}
	defaults := []ResponseInspectionPolicySummary{}
	for i := range systemDefaultInspectionRules {
		rule := &systemDefaultInspectionRules[i]
		if policyMatchesGatewayScope(&rule.summary, normalizedProtocol, providerCode) {
			defaults = append(defaults, CloneResponseInspectionPolicy(rule.summary))
		}
	}

	scopeFilter := `AND scope_type = 'protocol'
			AND provider_code IS NULL`
	args := []any{normalizedProtocol, maxGatewayInspectionPolicies}
	if providerCode != "" {
		scopeFilter = `AND (
				(scope_type = 'protocol' AND provider_code IS NULL)
				OR (scope_type = 'provider' AND provider_code = ?)
			)`
		args = []any{normalizedProtocol, providerCode, maxGatewayInspectionPolicies}
	}
	rows, err := m.db.QueryContext(ctx, m.bind(`SELECT id, name, enabled, priority, scope_type, protocol_code,
			provider_code, match_json, action, notes, created_at, updated_at
		FROM `+m.table("response_inspection_policies")+`
		WHERE enabled = 1
			AND protocol_code = ?
			`+scopeFilter+`
		ORDER BY CASE scope_type WHEN 'provider' THEN 0 ELSE 1 END ASC, priority ASC, updated_at DESC, id ASC
		LIMIT ?`), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	policies := defaults
	for rows.Next() {
		policy, scanErr := scanInspectionPolicyRow(rows.Scan)
		if scanErr != nil {
			return nil, scanErr
		}
		policies = append(policies, *policy)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return policies, nil
}

// maxGatewayInspectionPolicies mirrors maxManagementResponseInspectionPolicies.
const maxGatewayInspectionPolicies = 100

// normalizeGatewayPolicyProtocolCode mirrors normalizeGatewayPolicyProtocolCode.
func normalizeGatewayPolicyProtocolCode(value string) string {
	text := strings.TrimSpace(value)
	if text == "" || len(text) > 80 {
		return ""
	}
	switch text {
	case "openai", "anthropic", "gemini":
		return text
	default:
		return ""
	}
}

func scanInspectionPolicyRow(scan func(...any) error) (*ResponseInspectionPolicySummary, error) {
	var id, name, scopeType, protocolCode, action string
	var enabled, priority int
	var providerCode, notes, createdAt, updatedAt sql.NullString
	var matchJSON string
	if err := scan(&id, &name, &enabled, &priority, &scopeType, &protocolCode,
		&providerCode, &matchJSON, &action, &notes, &createdAt, &updatedAt); err != nil {
		return nil, err
	}
	if scopeType != "protocol" && scopeType != "provider" {
		return nil, errors.New("响应检查策略作用层级无效")
	}
	if !isValidInspectionAction(action) {
		return nil, errors.New("响应检查策略动作无效")
	}
	match, err := decodeInspectionMatch(matchJSON)
	if err != nil {
		return nil, err
	}
	return &ResponseInspectionPolicySummary{
		ID:           id,
		DefaultRule:  false,
		Editable:     true,
		Name:         name,
		Enabled:      enabled == 1,
		Priority:     priority,
		ScopeType:    scopeType,
		ProtocolCode: protocolCode,
		ProviderCode: nullToStrPtr(providerCode),
		Match:        match,
		Action:       action,
		Notes:        nullToStrPtr(notes),
		CreatedAt:    nullToStrPtr(createdAt),
		UpdatedAt:    nullToStrPtr(updatedAt),
	}, nil
}

func isValidInspectionAction(value string) bool {
	switch value {
	case "observe", "drop_event", "retry_no_avoidance", "retry_next_account",
		"avoid_account_ttl", "avoid_upstream_bucket_ttl":
		return true
	default:
		return false
	}
}

// decodeInspectionMatch mirrors normalizeMatch for the gateway read: unknown
// keys and malformed lists are storage anomalies (Node throws).
func decodeInspectionMatch(raw string) (ResponseInspectionPolicyMatch, error) {
	decoded := map[string]any{}
	if strings.TrimSpace(raw) != "" {
		if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
			return ResponseInspectionPolicyMatch{}, err
		}
	}
	known := map[string]bool{
		"clientProfiles": true, "outputTextIncludes": true, "outputTextExcludes": true,
		"errorCodes": true, "errorTypes": true, "errorMessageIncludes": true,
		"finishReasons": true, "jsonPathsExists": true, "rawTextIncludes": true,
	}
	for key := range decoded {
		if !known[key] {
			return ResponseInspectionPolicyMatch{}, errors.New("响应检查策略匹配条件包含不支持字段：" + key)
		}
	}
	stringList := func(key string) []string {
		items, _ := decoded[key].([]any)
		out := make([]string, 0, len(items))
		for _, item := range items {
			if text, ok := item.(string); ok {
				out = append(out, text)
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	}
	return ResponseInspectionPolicyMatch{
		ClientProfiles:        stringList("clientProfiles"),
		OutputTextIncludes:    stringList("outputTextIncludes"),
		OutputTextExcludes:    stringList("outputTextExcludes"),
		ErrorCodes:            stringList("errorCodes"),
		ErrorTypes:            stringList("errorTypes"),
		ErrorMessagesIncludes: stringList("errorMessageIncludes"),
		FinishReasons:         stringList("finishReasons"),
		JSONPathsExists:       stringList("jsonPathsExists"),
		RawTextIncludes:       stringList("rawTextIncludes"),
	}, nil
}

// policyMatchesGatewayScope mirrors policyMatchesGatewayScope.
func policyMatchesGatewayScope(policy *ResponseInspectionPolicySummary, protocolCode string, providerCode string) bool {
	if !policy.Enabled || policy.ProtocolCode != protocolCode {
		return false
	}
	if policy.ScopeType == "protocol" {
		return policy.ProviderCode == nil
	}
	return providerCode != "" && policy.ProviderCode != nil && *policy.ProviderCode == providerCode
}

// inspectionDefaultRule is one systemDefaultRules entry (storage/
// response-inspection-policy.repository.ts) mirrored for the gateway read.
type inspectionDefaultRule struct {
	summary ResponseInspectionPolicySummary
}

func defaultRule(id, name string, priority int, scopeType, protocolCode, providerCode string, match ResponseInspectionPolicyMatch, action string, notes string) inspectionDefaultRule {
	summary := ResponseInspectionPolicySummary{
		ID: id, DefaultRule: true, Editable: false, Name: name, Enabled: true,
		Priority: priority, ScopeType: scopeType, ProtocolCode: protocolCode,
		Match: match, Action: action, Notes: strPtrIfSet(notes),
	}
	if providerCode != "" {
		provider := providerCode
		summary.ProviderCode = &provider
	}
	return inspectionDefaultRule{summary: summary}
}

// systemDefaultInspectionRules mirrors systemDefaultRules verbatim.
var systemDefaultInspectionRules = []inspectionDefaultRule{
	defaultRule("default_openai_transient_precommit_error", "OpenAI 首输出前短暂错误", 0, "protocol", "openai", "",
		ResponseInspectionPolicyMatch{
			ClientProfiles: []string{"generic_openai", "codex"},
			ErrorCodes:     []string{"server_error", "internal_server_error", "server_overloaded", "overloaded", "service_unavailable", "temporarily_unavailable", "unavailable", "timeout", "deadline_exceeded", "resource_exhausted", "internal", "cancelled", "canceled"},
		},
		"retry_next_account",
		"仅限尚未向客户端提交语义输出的明确短暂上游错误；网关先按当前物理账号的有界预算重试，耗尽后再切换候选，不写长期账号状态。"),
	defaultRule("default_openai_context_window_error", "OpenAI 上下文窗口错误", 1, "protocol", "openai", "",
		ResponseInspectionPolicyMatch{
			ClientProfiles: []string{"generic_openai", "codex"},
			ErrorCodes:     []string{"context_length_exceeded", "input_too_large", "max_tokens_exceeded"},
		},
		"retry_next_account",
		"上下文容量属于当前账号/模型约束，直接切换候选账号，不在同一账号重复提交。"),
	defaultRule("default_openai_error_object", "OpenAI error 对象", 2, "protocol", "openai", "",
		ResponseInspectionPolicyMatch{JSONPathsExists: []string{"error"}},
		"retry_no_avoidance",
		"OpenAI v1 JSON / SSE data.error 默认检查规则；是否允许客户端专用重试由运行时客户端能力门控。"),
	defaultRule("default_openai_response_error", "OpenAI response.error", 3, "protocol", "openai", "",
		ResponseInspectionPolicyMatch{JSONPathsExists: []string{"response.error"}},
		"retry_no_avoidance",
		"OpenAI v1 Responses response.error 默认检查规则。"),
	defaultRule("default_openai_failed_status", "OpenAI failed 状态", 4, "protocol", "openai", "",
		ResponseInspectionPolicyMatch{FinishReasons: []string{"failed"}},
		"retry_no_avoidance",
		"OpenAI v1 Responses failed 状态默认检查规则。"),
	defaultRule("default_codex_response_incomplete", "Codex response.incomplete", 5, "protocol", "openai", "",
		ResponseInspectionPolicyMatch{ClientProfiles: []string{"codex"}, FinishReasons: []string{"incomplete"}},
		"retry_no_avoidance",
		"Codex 客户端会把 Responses response.incomplete 当成可重试流式错误；网关在写下游前拦截为统一可重试失败，避免服务端误判成功。"),
	defaultRule("default_codex_compaction_contract", "Codex compact 输出契约", 5, "protocol", "openai", "",
		ResponseInspectionPolicyMatch{ClientProfiles: []string{"codex"}, ErrorCodes: []string{"codex_compaction_contract_mismatch"}},
		"retry_next_account",
		"Codex Remote Compaction V2 的本地结构契约；只接受网关生成的契约失败帧，上游同名错误码不能触发。"),
	defaultRule("default_gpt_cyber_policy", "GPT cyber_policy", 6, "provider", "openai", "gpt",
		ResponseInspectionPolicyMatch{ErrorCodes: []string{"cyber_policy"}},
		"retry_no_avoidance",
		"GPT 供应商 cyber_policy 规则，适用于该供应商的所有下游客户端；不能扩散为所有 OpenAI-compatible 供应商语义。"),
	defaultRule("default_anthropic_transient_precommit_error", "Anthropic 首输出前短暂错误", 0, "protocol", "anthropic", "",
		ResponseInspectionPolicyMatch{
			ClientProfiles: []string{"generic_anthropic", "claude_code"},
			ErrorTypes:     []string{"api_error", "overloaded_error", "server_error", "internal_error", "service_unavailable"},
		},
		"retry_next_account",
		"仅限尚未向客户端提交语义输出的明确短暂上游错误；先按当前物理账号的有界预算重试，耗尽后使用与 OpenAI/Gemini 相同的候选切换机制。"),
	defaultRule("default_anthropic_error_object", "Anthropic error 对象", 1, "protocol", "anthropic", "",
		ResponseInspectionPolicyMatch{JSONPathsExists: []string{"error"}},
		"retry_no_avoidance",
		"Anthropic Messages JSON / SSE event:error 默认检查规则；错误类型只作为响应语义输入，不直接写账号状态。"),
	defaultRule("default_gemini_transient_precommit_error", "Gemini 首输出前短暂错误", 0, "protocol", "gemini", "",
		ResponseInspectionPolicyMatch{
			ClientProfiles: []string{"generic_gemini", "gemini_cli"},
			ErrorTypes:     []string{"RESOURCE_EXHAUSTED", "UNAVAILABLE", "DEADLINE_EXCEEDED", "INTERNAL", "CANCELLED"},
		},
		"retry_next_account",
		"仅限尚未向客户端提交语义输出的 Google canonical 短暂错误；网关先按当前物理账号的有界预算重试，耗尽后切换候选而不是把首次失败直接交给客户端。"),
	defaultRule("default_gemini_cli_retryable_error", "Gemini CLI 可重试错误", 1, "protocol", "gemini", "",
		ResponseInspectionPolicyMatch{
			ClientProfiles: []string{"gemini_cli"},
			ErrorTypes:     []string{"RESOURCE_EXHAUSTED", "UNAVAILABLE", "DEADLINE_EXCEEDED", "INTERNAL", "CANCELLED"},
		},
		"retry_next_account",
		"gemini-cli 已知会把 429、499、5xx 和超时类 Google canonical error 当作可重试错误；该规则只在 gemini_cli 客户端画像下请求下一个账号，不扩散到普通 Gemini 客户端。"),
	defaultRule("default_gemini_error_object", "Gemini error 对象", 20, "protocol", "gemini", "",
		ResponseInspectionPolicyMatch{JSONPathsExists: []string{"error"}},
		"retry_no_avoidance",
		"Gemini JSON / SSE error 默认检查规则；错误状态只作为响应语义输入，不直接写账号状态。"),
}
