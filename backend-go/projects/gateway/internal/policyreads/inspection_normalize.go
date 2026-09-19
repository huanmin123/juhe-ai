// inspection_normalize.go owns the response inspection policy normalizers:
// scope/action/match validation and the merged-input projection shared by
// create and patch.
package policyreads

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
)

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
