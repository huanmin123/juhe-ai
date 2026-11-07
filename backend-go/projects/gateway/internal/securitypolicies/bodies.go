// Request body validation for the security-policy family (API Key 安全策略与
// 出站内容脱敏设计 §4.3 策略 CRUD)：POST 严格只允许 name/description/rules，
// PATCH 白名单 name/description/rules/expectedUpdatedAt。范围与规则集校验不
// 在本层——owner 由路由 query 钉定（镜像调度偏好），规则集统一走 rules.go 的
// normalizeRulesInput（单点校验与规范化），这里只做键白名单与形状检查。
package securitypolicies

// invalidMutationMessage 是通用 schema 失败文案（路由族惯例：调度偏好侧为
// 「调度偏好参数无效」）.
const invalidMutationMessage = "安全策略参数无效"

var policyCreateKeys = map[string]bool{
	"name":        true,
	"description": true,
	"rules":       true,
}

var policyPatchKeys = map[string]bool{
	"name":        true,
	"description": true,
	"rules":       true,
}

// MutationInput is the create/patch payload; pointers/flags mirror hasOwnInput
// semantics (absent vs explicit null vs value). Description 的显式 null /
// 空串折算为清空（NULL 列）；rules 的显式 null 折算为缺省空规则集（设计 §4.1：
// 空规则 = 不处理 = 现有行为）。
type MutationInput struct {
	Name           *string
	Description    *string
	HasDescription bool
	HasRules       bool
	RulesRaw       any
}

// Empty reports whether no patchable field is present beyond
// expectedUpdatedAt (契约：空变更 400 请提供要修改的安全策略内容).
func (m MutationInput) Empty() bool {
	return m.Name == nil && !m.HasDescription && !m.HasRules
}

// parseCreateBody mirrors the strict create schema: name required,
// description/rules optional.
func parseCreateBody(body map[string]any) (MutationInput, string) {
	for key := range body {
		if !policyCreateKeys[key] {
			return MutationInput{}, invalidMutationMessage
		}
	}
	return parseMutationFields(body, true)
}

// parsePatchBody mirrors the strict patch schema minus expectedUpdatedAt
// (validated by the handler): every field optional.
func parsePatchBody(body map[string]any) (MutationInput, string) {
	for key := range body {
		if key == "expectedUpdatedAt" {
			continue
		}
		if !policyPatchKeys[key] {
			return MutationInput{}, invalidMutationMessage
		}
	}
	return parseMutationFields(body, false)
}

func parseMutationFields(body map[string]any, requireAll bool) (MutationInput, string) {
	input := MutationInput{}
	if raw, present := body["name"]; present {
		text, ok := raw.(string)
		if !ok {
			return input, invalidMutationMessage
		}
		input.Name = &text
	} else if requireAll {
		return input, "安全策略名称不能为空"
	}
	if raw, present := body["description"]; present {
		input.HasDescription = true
		if raw == nil {
			input.Description = nil
		} else {
			text, ok := raw.(string)
			if !ok {
				return input, invalidMutationMessage
			}
			input.Description = &text
		}
	}
	if raw, present := body["rules"]; present {
		input.HasRules = true
		input.RulesRaw = raw
	}
	return input, ""
}
