// Request body validation mirroring route-strategies.routes.ts zod schemas:
// strict key sets, field types and the create/patch refinements. Store-level
// domain normalization (ranges, hybrid coverage, binding boundary) lives in
// config.go / bindings.go.
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
	"hybridRoutingConfig": true,
}

var normalRoutingConfigKeys = map[string]bool{
	"schedulingPreference": true,
	"firstByteDeadlineMs":  true,
	"speedFirstConfig":     true,
}

var speedFirstConfigKeys = map[string]bool{
	"firstByteThresholdMs":          true,
	"slowTriggerCount":              true,
	"slowWindowSeconds":             true,
	"recoverySuccessCount":          true,
	"probeIntervalSeconds":          true,
	"degradedTtlSeconds":            true,
	"maxFirstByteRetriesPerRequest": true,
}

var hybridRoutingConfigKeys = map[string]bool{
	"scoringGroupId":               true,
	"scoringModel":                 true,
	"scoringContextMode":           true,
	"qualityPreference":            true,
	"scoringTimeoutMs":             true,
	"scoringFallbackMaxLevel":      true,
	"scoringCacheEnabled":          true,
	"scoringCacheTtlSeconds":       true,
	"cacheAffinityEnabled":         true,
	"affinityTtlSeconds":           true,
	"switchMinLevelDelta":          true,
	"downgradeConsecutiveLowCount": true,
	"levelRoutes":                  true,
	"qualityInspection":            true,
}

var hybridLevelRouteKeys = map[string]bool{
	"minLevel":    true,
	"maxLevel":    true,
	"targetModel": true,
	"enabled":     true,
}

var hybridQualityInspectionKeys = map[string]bool{
	"enabled":           true,
	"scoringGroupId":    true,
	"scoringModel":      true,
	"triggerMode":       true,
	"maxTriggerLevel":   true,
	"maxRetries":        true,
	"failureAction":     true,
	"unavailableAction": true,
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
		if !ok || !IsRouteStrategyMode(text) {
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
	if raw, present := body["hybridRoutingConfig"]; present {
		if raw != nil {
			if !validHybridConfigShape(raw) {
				return input, invalidMutationMessage
			}
			input.HasHybridConfig = true
			input.HybridConfigRaw = raw
		} else {
			input.HasHybridConfig = true
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

// validHybridConfigShape checks the strict key sets of the hybrid config,
// its level route items and the nested quality inspection (zod .strict()).
func validHybridConfigShape(raw any) bool {
	record, ok := strictObject(raw, hybridRoutingConfigKeys)
	if !ok {
		return false
	}
	if levelRoutes, present := record["levelRoutes"]; present && levelRoutes != nil {
		list, ok := levelRoutes.([]any)
		if !ok {
			return false
		}
		for _, item := range list {
			if _, ok := strictObject(item, hybridLevelRouteKeys); !ok {
				return false
			}
		}
	}
	if inspection, present := record["qualityInspection"]; present && inspection != nil {
		if _, ok := strictObject(inspection, hybridQualityInspectionKeys); !ok {
			return false
		}
	}
	return true
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
