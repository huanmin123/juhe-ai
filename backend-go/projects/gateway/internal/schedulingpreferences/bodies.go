// Request body validation for the scheduling-preference family (契约「调度偏
// 好」章；v2 用户裁决修订第 4 条)：POST 严格只允许 name/chatMode/
// chatSpeedFirstConfig，PATCH 白名单 name/chatMode/chatSpeedFirstConfig/
// expectedUpdatedAt。enabled 已退役（创建即启用，无停用态），携带者由严格键
// 校验拒绝。范围与默认值校验不在本层——统一走
// routestrategies.NormalizeNormalRoutingConfig（单一来源），这里只做键白名单
// 与形状检查。
package schedulingpreferences

// invalidMutationMessage 是通用 schema 失败文案（路由族惯例：策略路由侧为
// 「策略路由参数无效」）.
const invalidMutationMessage = "调度偏好参数无效"

var preferenceCreateKeys = map[string]bool{
	"name":                 true,
	"chatMode":             true,
	"chatSpeedFirstConfig": true,
}

var preferencePatchKeys = map[string]bool{
	"name":                 true,
	"chatMode":             true,
	"chatSpeedFirstConfig": true,
}

// preferenceSpeedConfigKeys 是 chatSpeedFirstConfig 的外层键（D2 形状）.
var preferenceSpeedConfigKeys = map[string]bool{
	"firstByteDeadlineMs": true,
	"speedFirstConfig":    true,
}

// preferenceSpeedInnerKeys 是 speedFirstConfig 内层键（与策略路由一致，含旧
// firstByteThresholdMs 别名——读取兼容、双字段同时出现由 normalize 拒绝）.
var preferenceSpeedInnerKeys = map[string]bool{
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

// MutationInput is the create/patch payload; pointers/flags mirror hasOwnInput
// semantics (absent vs explicit null vs value).
type MutationInput struct {
	Name      *string
	ChatMode  *string
	HasConfig bool
	ConfigRaw any
}

// Empty reports whether no patchable field is present beyond
// expectedUpdatedAt (契约：空变更 400 请提供要修改的调度偏好内容).
func (m MutationInput) Empty() bool {
	return m.Name == nil && m.ChatMode == nil && !m.HasConfig
}

// parseCreateBody mirrors the strict create schema: name/chatMode required,
// chatSpeedFirstConfig optional.
func parseCreateBody(body map[string]any) (MutationInput, string) {
	for key := range body {
		if !preferenceCreateKeys[key] {
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
		if !preferencePatchKeys[key] {
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
		return input, "调度偏好名称不能为空"
	}
	if raw, present := body["chatMode"]; present {
		text, ok := raw.(string)
		if !ok {
			return input, "调度偏好对话形态无效"
		}
		input.ChatMode = &text
	} else if requireAll {
		return input, "调度偏好对话形态无效"
	}
	if raw, present := body["chatSpeedFirstConfig"]; present {
		input.HasConfig = true
		if raw != nil {
			record, ok := strictObject(raw, preferenceSpeedConfigKeys)
			if !ok {
				return input, "速度优先配置无效"
			}
			if inner, has := record["speedFirstConfig"]; has && inner != nil {
				if _, ok := strictObject(inner, preferenceSpeedInnerKeys); !ok {
					return input, "速度优先配置无效"
				}
			}
			input.ConfigRaw = record
		}
	}
	return input, ""
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
