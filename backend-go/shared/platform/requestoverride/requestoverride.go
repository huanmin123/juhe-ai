// Package requestoverride 是账户请求覆盖两个字段
// （credentials.service_tier_override / reasoning_effort_override）的 wire
// 值域与档位序的单一真源。
//
// 账户保存校验（internal/accounts）与网关运行时覆盖应用
// （internal/gatewaydispatch/gatewayoauthcodex）都引用本包，避免同一份枚举表
// 与档位序在多处漂移。
//
// 语义（2026-10-18 简化定案）：
//   - 值域按“协议族”定义，不再按账户已选模型或供应商模型目录实时过滤；
//     模型目录只在运行时决定是否降级。
//   - 思考级别可向下兼容：配置值不被最终上游模型支持时，取该模型声明值中
//     按档位序不高于配置值的最高档；无可用档位时不改写该字段（保留客户端原值）。
//   - 服务等级不降级：`default` 恒为“强制标准档”（删除客户端 service_tier）；
//     其它档位只在最终上游模型声明支持时写入，否则保留客户端原值。
//   - `ultra` 不属于本值域：它是 Codex 客户端能力，上游 wire 以 `max` 表达。
package requestoverride

import (
	"regexp"
	"strings"
)

// 协议族 token。按账户的协议档案（protocolCode）优先判定，取不到时按供应商
// 码回落到 openai 族（所有 OpenAI 兼容供应商，含 gpt/openai/deepseek/glm/
// xai/volcengine/qwen/minimax 与自定义兼容供应商）。
const (
	FamilyOpenAI    = "openai"
	FamilyAnthropic = "anthropic"
	FamilyGemini    = "gemini"
)

// FamilyForProvider 按供应商码判定协议族。未知供应商回落 openai 族。
func FamilyForProvider(providerCode string) string {
	switch strings.ToLower(strings.TrimSpace(providerCode)) {
	case "anthropic":
		return FamilyAnthropic
	case "gemini":
		return FamilyGemini
	default:
		return FamilyOpenAI
	}
}

// FamilyForProtocol 按协议码判定协议族；空值或未知协议回落空串（由调用方
// 决定是否改用 FamilyForProvider）。
func FamilyForProtocol(protocolCode string) string {
	switch strings.ToLower(strings.TrimSpace(protocolCode)) {
	case "anthropic", "anthropic_messages":
		return FamilyAnthropic
	case "gemini":
		return FamilyGemini
	case "openai":
		return FamilyOpenAI
	default:
		return ""
	}
}

// FamilyOf 组合判定：协议族优先，协议码取不到时回落供应商族。
func FamilyOf(providerCode, protocolCode string) string {
	if family := FamilyForProtocol(protocolCode); family != "" {
		return family
	}
	return FamilyForProvider(providerCode)
}

// reasoningEffortsByFamily 是各协议族可配置的思考级别全集（档位由低到高）。
// 数据来源：provider_model_catalog 种子行 supported_reasoning_efforts_json 的
// 族内并集（gpt/openai 族含 none；anthropic 族最低档为 low；gemini 族最高档为
// high），`ultra` 按设计不进入本值域。
var reasoningEffortsByFamily = map[string][]string{
	FamilyOpenAI:    {"none", "minimal", "low", "medium", "high", "xhigh", "max"},
	FamilyAnthropic: {"low", "medium", "high", "xhigh", "max"},
	FamilyGemini:    {"minimal", "low", "medium", "high"},
}

// serviceTiersByFamily 是各协议族可配置的服务等级全集。Anthropic 当前不提供
// 账户服务等级选项（其原生 `auto`/`standard_only` 不映射为本站通用档位），
// 故该族为空集。
var serviceTiersByFamily = map[string][]string{
	FamilyOpenAI:    {"default", "priority", "flex"},
	FamilyAnthropic: {},
	FamilyGemini:    {"default", "priority", "flex"},
}

// ReasoningEfforts 返回该协议族的思考级别全集（副本，低档在前）。
func ReasoningEfforts(family string) []string {
	return append([]string(nil), reasoningEffortsByFamily[family]...)
}

// ServiceTiers 返回该协议族的服务等级全集（副本）。
func ServiceTiers(family string) []string {
	return append([]string(nil), serviceTiersByFamily[family]...)
}

// SupportsReasoningEffort 报告该协议族是否接受该思考级别值。
func SupportsReasoningEffort(family, value string) bool {
	return contains(reasoningEffortsByFamily[family], value)
}

// ReasoningEffortsWithout 返回该协议族思考级别全集去掉 excluded 后的视图
// （副本，保持真源顺序）。供确有业务语义的子集使用（例如 chat 会话面不接受
// 显式 `none`；自定义模型能力不声明 `default` 服务等级）。
func ReasoningEffortsWithout(family string, excluded ...string) []string {
	return without(reasoningEffortsByFamily[family], excluded)
}

// ServiceTiersWithout 返回该协议族服务等级全集去掉 excluded 后的视图（副本）。
func ServiceTiersWithout(family string, excluded ...string) []string {
	return without(serviceTiersByFamily[family], excluded)
}

// ReasoningEffortSet 返回该协议族思考级别全集的集合视图（副本）。
func ReasoningEffortSet(family string) map[string]bool {
	return toSet(reasoningEffortsByFamily[family])
}

// ReasoningEffortSetWithout 返回去掉 excluded 后的集合视图（副本）。
func ReasoningEffortSetWithout(family string, excluded ...string) map[string]bool {
	return toSet(without(reasoningEffortsByFamily[family], excluded))
}

// ServiceTierSetWithout 返回去掉 excluded 后的集合视图（副本）。
func ServiceTierSetWithout(family string, excluded ...string) map[string]bool {
	return toSet(without(serviceTiersByFamily[family], excluded))
}

// ServiceTierSet 返回该协议族服务等级全集的集合视图（副本）。
func ServiceTierSet(family string) map[string]bool {
	return toSet(serviceTiersByFamily[family])
}

func without(values []string, excluded []string) []string {
	skip := toSet(excluded)
	out := make([]string, 0, len(values))
	for _, value := range values {
		if skip[value] {
			continue
		}
		out = append(out, value)
	}
	return out
}

func toSet(values []string) map[string]bool {
	out := make(map[string]bool, len(values))
	for _, value := range values {
		out[value] = true
	}
	return out
}

// SupportsServiceTier 报告该协议族是否接受该服务等级值。
func SupportsServiceTier(family, value string) bool {
	return contains(serviceTiersByFamily[family], value)
}

// effortOrder 是跨族的统一档位序（由低到高）。跨族比较用于降级：例如配置
// `max`、最终模型只声明 gemini 族的 minimal/low/medium/high 时，取 high。
var effortOrder = []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}

// EffortRank 返回档位序号（由低到高，0 起）；不在序内的值返回 false。
func EffortRank(value string) (int, bool) {
	for index, item := range effortOrder {
		if item == value {
			return index, true
		}
	}
	return 0, false
}

// DegradeReasoningEffort 计算“向下兼容”后的思考级别：
//   - 最终模型声明值里精确含配置值 → 原值；
//   - 否则取声明值中档位序不高于配置值的最高档；
//   - 无可用档位（声明为空、或全部高于配置值、或配置值不在档位序内）→ 空串，
//     调用方保持客户端原值不改写。
func DegradeReasoningEffort(configured string, supported []string) string {
	if configured == "" {
		return ""
	}
	if contains(supported, configured) {
		return configured
	}
	configuredRank, ok := EffortRank(configured)
	if !ok {
		return ""
	}
	best := ""
	bestRank := -1
	for _, value := range supported {
		rank, ranked := EffortRank(value)
		if !ranked || rank > configuredRank || rank <= bestRank {
			continue
		}
		best = value
		bestRank = rank
	}
	return best
}

// TokenPattern 是覆盖字段的凭据 token 形状（与账户凭据写入面同一语义）：
// 首尾空白即非法，长度上限 64。
const TokenPattern = `(?i)^[a-z0-9][a-z0-9._-]{0,63}$`

var tokenRegexp = regexp.MustCompile(TokenPattern)

// IsToken 报告值是否符合覆盖字段的凭据 token 形状（不做 trim 归一）。
func IsToken(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && tokenRegexp.MatchString(value)
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
