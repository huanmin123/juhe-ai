// Request body validation mirroring route-strategies.routes.ts zod schemas:
// strict key sets, field types and the create/patch refinements. Store-level
// domain normalization (ranges, binding boundary) lives in config.go /
// bindings.go.
package routestrategies

import "strings"

const invalidMutationMessage = "策略路由参数无效"

// allowed top-level mutation keys (routeStrategyMutationSchema.strict()).
var mutationTopLevelKeys = map[string]bool{
	"name":                true,
	"description":         true,
	"mode":                true,
	"status":              true,
	"groupBindings":       true,
	"normalRoutingConfig": true,
}

var normalRoutingConfigKeys = map[string]bool{
	"schedulingPreference": true,
	"firstByteDeadlineMs":  true,
	"speedFirstConfig":     true,
}

// LegacySchedulingConfigRejectedMessage 是策略写入口拒绝旧调度字段的稳定错
// 误文案（P2 契约：调度偏好已独立为调度偏好实体，策略路由不再是调度配置来
// 源；写侧显式报错，不静默忽略、不剥离后继续）。管理/个人 POST/PATCH 与公开
// add|update 共用同一文案，响应形态为 400 { message }。
const LegacySchedulingConfigRejectedMessage = "调度偏好已独立为调度偏好实体，请在 API Key 上绑定调度偏好；策略路由不再接受调度配置字段（schedulingPreference / speedFirstConfig / firstByteDeadlineMs / firstByteThresholdMs）"

// retiredSchedulingConfigPayloadKeys 是 normalRoutingConfig payload 中已退场
// 的调度配置键（以 config.go 的 NormalRoutingConfig 结构体字段为准，含
// speedFirstConfig 内已知旧别名 firstByteThresholdMs；该别名出现在顶层或嵌
// 套对象内同样命中，speedFirstConfig 整键存在时已先行命中）。
var retiredSchedulingConfigPayloadKeys = []string{
	"schedulingPreference",
	"speedFirstConfig",
	"firstByteDeadlineMs",
	"firstByteThresholdMs",
}

// DetectRetiredSchedulingConfigPayload 判定请求 payload 的 normalRoutingConfig
// 是否携带任一退场调度键。判定对象是请求 payload（map 形态的 JSON object）；
// 存储值不走本判定（存储解析保持完全宽容，回滚 oracle）。非 object 形态交由
// 既有类型校验处理，不在本判定范围。
func DetectRetiredSchedulingConfigPayload(normalConfig any) bool {
	record, ok := normalConfig.(map[string]any)
	if !ok {
		return false
	}
	for _, key := range retiredSchedulingConfigPayloadKeys {
		if _, present := record[key]; present {
			return true
		}
	}
	return false
}

var speedFirstConfigKeys = map[string]bool{
	"firstByteThresholdMs":               true,
	"slowTriggerCount":                   true,
	"slowWindowSeconds":                  true,
	"recoverySuccessCount":               true,
	"probeIntervalSeconds":               true,
	"degradedTtlSeconds":                 true,
	"maxFirstByteRetriesPerRequest":      true,
	"totalTimeDeadlineSeconds":           true,
	"compactionTotalTimeDeadlineSeconds": true,
}

var bindingItemKeys = map[string]bool{
	"groupId":  true,
	"priority": true,
	"weight":   true,
	"status":   true,
}

// parseCreateBody mirrors routeStrategyCreateSchema.safeParse: name required,
// groupBindings required (refine), everything else optional. Returns the
// first zod-style issue message or "".
func parseCreateBody(body map[string]any) (MutationInput, string) {
	for key := range body {
		if !mutationTopLevelKeys[key] {
			return MutationInput{}, invalidMutationMessage
		}
	}
	input, problem := parseMutationFields(body, true)
	if problem != "" {
		return MutationInput{}, problem
	}
	if !input.HasBindings || len(input.Bindings) == 0 {
		return MutationInput{}, "策略路由至少需要绑定一个分组"
	}
	return input, ""
}

// parsePatchBody mirrors routeStrategyUpdateSchema.safeParse minus
// expectedUpdatedAt (validated by the handler): every field optional.
func parsePatchBody(body map[string]any) (MutationInput, string) {
	for key := range body {
		if key == "expectedUpdatedAt" {
			continue
		}
		if !mutationTopLevelKeys[key] {
			return MutationInput{}, invalidMutationMessage
		}
	}
	return parseMutationFields(body, false)
}

func parseMutationFields(body map[string]any, requireName bool) (MutationInput, string) {
	input := MutationInput{}
	if raw, present := body["name"]; present {
		text, ok := raw.(string)
		if !ok || strings.TrimSpace(text) == "" {
			if requireName {
				return input, "请填写策略路由名称"
			}
			return input, invalidMutationMessage
		}
		input.Name = &text
	} else if requireName {
		return input, "请填写策略路由名称"
	}
	if raw, present := body["description"]; present && raw != nil {
		text, ok := raw.(string)
		if !ok {
			return input, invalidMutationMessage
		}
		if utf16CodeUnits(strings.TrimSpace(text)) > 200 {
			return input, "策略路由说明不能超过 200 个字符"
		}
		input.HasDescription = true
		input.Description = &text
	} else if present {
		// explicit null (nullable in the zod schema)
		input.HasDescription = true
	}
	// mode/status are .optional() but not .nullable(): an explicit null is a
	// schema failure, not an omitted field.
	if raw, present := body["mode"]; present {
		if raw == nil {
			return input, invalidMutationMessage
		}
		text, ok := raw.(string)
		if !ok || !IsWritableRouteStrategyMode(text) {
			return input, invalidMutationMessage
		}
		input.Mode = &text
	}
	if raw, present := body["status"]; present {
		if raw == nil {
			return input, invalidMutationMessage
		}
		text, ok := raw.(string)
		if !ok || (text != "active" && text != "disabled") {
			return input, invalidMutationMessage
		}
		input.Status = &text
	}
	if raw, present := body["groupBindings"]; present && raw != nil {
		list, ok := raw.([]any)
		if !ok {
			return input, invalidMutationMessage
		}
		if len(list) == 0 {
			return input, "策略路由至少需要绑定一个分组"
		}
		if len(list) > maxRouteStrategyGroupBindings {
			return input, "策略路由最多绑定 " + itoa(maxRouteStrategyGroupBindings) + " 个分组"
		}
		input.HasBindings = true
		input.Bindings = make([]BindingInput, 0, len(list))
		for index, item := range list {
			binding, problem := parseBindingItem(item, index)
			if problem != "" {
				return input, problem
			}
			input.Bindings = append(input.Bindings, binding)
		}
	} else if present {
		// explicit null is not an array → invalid.
		return input, invalidMutationMessage
	}
	if raw, present := body["normalRoutingConfig"]; present {
		if raw != nil {
			// P2 契约：请求 payload 携带退场调度字段显式拒绝（先于严格键校验，
			// 旧别名 firstByteThresholdMs 同样命中专用文案）。
			if DetectRetiredSchedulingConfigPayload(raw) {
				return input, LegacySchedulingConfigRejectedMessage
			}
			record, ok := strictObject(raw, normalRoutingConfigKeys)
			if !ok {
				return input, invalidMutationMessage
			}
			if speedFirst, hasSpeedFirst := record["speedFirstConfig"]; hasSpeedFirst && speedFirst != nil {
				if _, ok := strictObject(speedFirst, speedFirstConfigKeys); !ok {
					return input, invalidMutationMessage
				}
			}
			input.HasNormalConfig = true
			input.NormalConfigRaw = record
		} else {
			input.HasNormalConfig = true
		}
	}
	return input, ""
}

// parseBindingItem mirrors routeStrategyGroupBindingSchema (strict) plus the
// repository field normalizers.
func parseBindingItem(item any, index int) (BindingInput, string) {
	record, ok := strictObject(item, bindingItemKeys)
	if !ok {
		return BindingInput{}, "策略路由分组绑定项无效"
	}
	input := BindingInput{Status: "active"}
	rawGroupID, ok := record["groupId"].(string)
	if !ok || strings.TrimSpace(rawGroupID) == "" {
		return BindingInput{}, "策略路由分组无效"
	}
	input.GroupID = rawGroupID
	// Binding item fields are .optional() but not .nullable(): explicit null
	// is a schema failure, not an omitted field.
	if raw, present := record["priority"]; present {
		if raw == nil {
			return BindingInput{}, invalidMutationMessage
		}
		number, ok := raw.(float64)
		if !ok || number != float64(int64(number)) || number < 1 {
			return BindingInput{}, "策略路由分组优先级必须是大于 0 的整数"
		}
		priority := int(number)
		input.Priority = &priority
		input.priorityProvided = true
	} else {
		fallback := index + 1
		input.Priority = &fallback
	}
	if raw, present := record["weight"]; present {
		if raw == nil {
			return BindingInput{}, "分组权重必须是数字"
		}
		weight, problem := parseBindingWeight(raw)
		if problem != "" {
			return BindingInput{}, problem
		}
		input.Weight = &weight
		input.weightProvided = true
	}
	if raw, present := record["status"]; present {
		if raw == nil {
			return BindingInput{}, invalidMutationMessage
		}
		text, ok := raw.(string)
		if !ok || (text != "active" && text != "disabled") {
			return BindingInput{}, "策略路由分组绑定状态无效"
		}
		input.Status = text
		input.statusProvided = true
	}
	return input, ""
}

// parseBindingWeight mirrors the zod weight messages (数字/整数/1-100 之间).
func parseBindingWeight(raw any) (int, string) {
	number, ok := raw.(float64)
	if !ok {
		return 0, "分组权重必须是数字"
	}
	if number != float64(int64(number)) {
		return 0, "分组权重必须是整数"
	}
	if number < 1 || number > 100 {
		return 0, "分组权重必须在 1-100 之间"
	}
	return int(number), ""
}

// strictObject requires a JSON object whose keys all appear in allowed.
func strictObject(value any, allowed map[string]bool) (map[string]any, bool) {
	record, ok := value.(map[string]any)
	if !ok {
		return nil, false
	}
	for key := range record {
		if !allowed[key] {
			return nil, false
		}
	}
	return record, true
}
