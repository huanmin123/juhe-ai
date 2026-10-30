// Package upstreambaseurlmigration 实现「上游 Base URL 版本根语义 v2」的存量
// 数据迁移（供应商与上游协议面重构设计 R2/R3/R6）：
//
//	v2 契约：最终上游 URL = TrimRight(base_url, "/") + 操作路径；操作路径不含
//	版本前缀（qwen 例外为全路径）。因此 Base URL 必须自带版本根
//	（/v1、/api/v3、/api/plan/v3、/api/coding/v3、/api/paas/v4、/anthropic/v1、
//	/v1beta、/v1beta/openai），
//	唯一例外是 qwen_dashscope——其 base_url 语义是 origin（裸根）。
//
// 本文件承载分类与回填规则的纯函数实现（零 I/O，表驱动可测）：
//   - 家族判定：连接行按 connection_preset_code；账户凭据按供应商与协议档案
//     合成 preset 提示（vendor+protocol → registry preset 常量）；
//   - 逐值分类（幂等）：空值按 registry 默认回填（generic 无默认 → 复核桶）、
//     裸 origin 补版本根、已合规（保留后缀 / registry 默认 / qwen 裸根）不变、
//     其余非空值进人工复核桶（不改数据，仅 manifest 记录）。
package upstreambaseurlmigration

import (
	"net/url"
	"strings"

	contracts "github.com/huanminabc/juhe-ai/backend-go-contracts"
)

// RuleVersion 是分类规则版本；manifest 携带，规则语义变化时递增。
const RuleVersion = 1

// Family 是版本根迁移的家族词表（任务契约的五个判定族 + 无规则族）。
type Family string

const (
	FamilyOpenAI    Family = "openai"
	FamilyAnthropic Family = "anthropic"
	FamilyGemini    Family = "gemini"
	FamilyQwen      Family = "qwen"
	FamilyArk       Family = "volcengine_ark"
	// FamilyNone 表示该 preset 不在版本根契约词表内（volcengine_openspeech、
	// 未知 preset）：registry 默认值原样保留，其余值进复核桶，绝不猜测改写。
	FamilyNone Family = ""
)

// Disposition 是单行分类结果词表。
type Disposition string

const (
	DispositionUnchanged Disposition = "unchanged" // 已合规，不改
	DispositionRewrite   Disposition = "rewrite"   // 就地改写（补版本根 / 剥 qwen 后缀 / 规整尾斜杠）
	DispositionFill      Disposition = "fill"      // 空值 → registry 默认
	DispositionReview    Disposition = "review"    // 人工复核桶：不改数据，manifest 记录
)

// ReviewReason 是复核桶的原因词表。
type ReviewReason string

const (
	ReviewNonconforming ReviewReason = "nonconforming_path" // 非空值不在家族版本根词表内
	ReviewInvalidURL    ReviewReason = "invalid_url"        // 非绝对 http/https URL / 带 userinfo/query/fragment
	ReviewUnknownPreset ReviewReason = "unknown_preset"     // preset 无版本根规则且值非 registry 默认
	ReviewNoDefault     ReviewReason = "no_registry_default"
	ReviewDecryptFailed ReviewReason = "credential_decrypt_failed"
)

// ChangeReason 是改写/回填的原因词表（manifest 逐行披露）。
type ChangeReason string

const (
	ReasonAppendVersionRoot   ChangeReason = "append_version_root" // 裸 origin 补版本根
	ReasonStripQwenSuffix     ChangeReason = "strip_qwen_suffix"   // 剥离 /compatible-mode/v1 回 origin
	ReasonNormalizeSlash      ChangeReason = "normalize_trailing_slash"
	ReasonFillRegistryDefault ChangeReason = "fill_registry_default"
)

// qwenStripSuffix 是 qwen 旧默认（compatible-mode OpenAI 兼容根）后缀；v2 下
// 该版本段属于操作路径，base_url 须剥回 DashScope origin。
const qwenStripSuffix = "/compatible-mode/v1"

// FamilyForPreset 返回连接 preset 的家族。
func FamilyForPreset(preset string) Family {
	switch preset {
	case contracts.PresetGPTOpenAIV1, contracts.PresetXAIOpenAIV1, contracts.PresetDeepSeekOpenAIV1,
		contracts.PresetMiniMaxMedia, contracts.PresetGenericOpenAIV1, contracts.PresetGLMGeneralOpenAIV1,
		contracts.PresetGLMCodingOpenAIV1, contracts.PresetGeminiOpenAIChatV1Beta:
		return FamilyOpenAI
	case contracts.PresetAnthropicV1, contracts.PresetDeepSeekAnthropicV1,
		contracts.PresetGLMCodingAnthropicV1, contracts.PresetGenericAnthropicV1:
		return FamilyAnthropic
	case contracts.PresetGeminiNativeV1Beta, contracts.PresetGenericGeminiV1Beta:
		return FamilyGemini
	case contracts.PresetQwenDashScope:
		return FamilyQwen
	case contracts.PresetVolcengineArk:
		return FamilyArk
	default:
		return FamilyNone
	}
}

// accountDefaultPreset 是账户级 (vendor_code, protocol_code) → registry preset
// 提示的固定表（账户行不存 preset；凭据 base_url 的家族与空值回填按供应商与
// 协议档案推导。glm 的 openai 协议账户按 general 档案根 /api/paas/v4 处理，
// coding 连接的根由连接行各自的 preset 承载）。vendor 为空串（generic_http）
// 按协议面落 generic preset（registry 默认值为空 → 空值进复核桶）。
var accountDefaultPreset = map[string]map[string]string{
	"gpt":        {"openai": contracts.PresetGPTOpenAIV1},
	"xai":        {"openai": contracts.PresetXAIOpenAIV1},
	"deepseek":   {"openai": contracts.PresetDeepSeekOpenAIV1, "anthropic": contracts.PresetDeepSeekAnthropicV1},
	"anthropic":  {"anthropic": contracts.PresetAnthropicV1, "openai": contracts.PresetAnthropicV1},
	"gemini":     {"gemini": contracts.PresetGeminiNativeV1Beta, "openai": contracts.PresetGeminiOpenAIChatV1Beta},
	"glm":        {"openai": contracts.PresetGLMGeneralOpenAIV1, "anthropic": contracts.PresetGLMCodingAnthropicV1},
	"minimax":    {"openai": contracts.PresetMiniMaxMedia},
	"volcengine": {"openai": contracts.PresetVolcengineArk},
	"qwen":       {"openai": contracts.PresetQwenDashScope},
	"":           {"openai": contracts.PresetGenericOpenAIV1, "anthropic": contracts.PresetGenericAnthropicV1, "gemini": contracts.PresetGenericGeminiV1Beta},
}

// AccountPresetHint 由账户的供应商与协议档案合成 registry preset 提示；未知
// 组合返回空串（家族为 FamilyNone，只保留 registry 默认值、其余进复核桶）。
func AccountPresetHint(vendorCode, protocolCode string) string {
	if byProtocol, known := accountDefaultPreset[strings.TrimSpace(vendorCode)]; known {
		return byProtocol[strings.TrimSpace(protocolCode)]
	}
	return ""
}

// bareOriginRoot 返回 (家族, preset) 裸 origin 应补的版本根。openai 族内
// glm / gemini-openai-chat 档案的版本根不是 /v1，按 registry 默认值的路径段
// 落根（任务契约「补 /v1」对这些档案落地为其 registry 版本根，避免写出
// open.bigmodel.cn/v1 这类不存在的端点）。
func bareOriginRoot(family Family, preset string) string {
	switch family {
	case FamilyAnthropic:
		return "/v1"
	case FamilyGemini:
		return "/v1beta"
	case FamilyArk:
		return "/api/v3"
	case FamilyOpenAI:
		switch preset {
		case contracts.PresetGLMGeneralOpenAIV1:
			return "/api/paas/v4"
		case contracts.PresetGLMCodingOpenAIV1:
			return "/api/coding/paas/v4"
		case contracts.PresetGeminiOpenAIChatV1Beta:
			return "/v1beta/openai"
		default:
			return "/v1"
		}
	default:
		return ""
	}
}

// familyKeepSuffixes 是家族已合规版本根后缀集合（对 TrimRight "/" 后的值做
// 大小写不敏感后缀匹配；anthropic 契约明确大小写不敏感，全族同规）。
func familyKeepSuffixes(family Family) []string {
	switch family {
	case FamilyOpenAI:
		// 方舟 Plan 套餐根（/api/plan/v3、/api/coding/v3）是真实官方版本根，
		// 账户无论挂在哪个 openai 系供应商档案下都直拼可达，识别为已合规。
		return []string{"/v1", "/api/paas/v4", "/api/coding/paas/v4", "/v1beta/openai", "/api/plan/v3", "/api/coding/v3"}
	case FamilyAnthropic:
		return []string{"/v1"}
	case FamilyGemini:
		return []string{"/v1beta"}
	case FamilyArk:
		// 经典按量根之外，方舟 Agent Plan（/api/plan/v3）与 Coding Plan
		//（/api/coding/v3）套餐是官方并行的版本根形态，已合规不进复核桶。
		return []string{"/api/v3", "/api/plan/v3", "/api/coding/v3"}
	default:
		return nil
	}
}

// Classification 是单值分类结果（纯函数输出）。
type Classification struct {
	Family       Family
	Disposition  Disposition
	NewValue     string       // Rewrite / Fill 时的新值（无尾斜杠规整）；其余为空
	ChangeReason ChangeReason // Rewrite / Fill 的原因；其余为空
	ReviewReason ReviewReason // 仅 Review；其余为空
}

// ClassifyBaseURL 按 (preset, 原始值) 分类一条 base_url（幂等：对返回的新值
// 重跑必然 unchanged）。fillDefault 传 registry 默认（连接行 = contracts.
// PresetDefaultBaseURL(preset)；账户行同源），空值回填与默认值等值判定使用。
func ClassifyBaseURL(preset, fillDefault, raw string) Classification {
	family := FamilyForPreset(preset)
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		switch family {
		case FamilyNone:
			return Classification{Family: family, Disposition: DispositionReview, ReviewReason: ReviewUnknownPreset}
		case FamilyQwen:
			// 契约：qwen 空 → 不变。
			return Classification{Family: family, Disposition: DispositionUnchanged}
		default:
			if strings.TrimSpace(fillDefault) == "" {
				return Classification{Family: family, Disposition: DispositionReview, ReviewReason: ReviewNoDefault}
			}
			return Classification{Family: family, Disposition: DispositionFill,
				NewValue: strings.TrimRight(strings.TrimSpace(fillDefault), "/"), ChangeReason: ReasonFillRegistryDefault}
		}
	}
	if _, valid := canonicalBaseURL(trimmed); !valid {
		return Classification{Family: family, Disposition: DispositionReview, ReviewReason: ReviewInvalidURL}
	}
	normalized := strings.TrimRight(trimmed, "/")
	lowerNormalized := strings.ToLower(normalized)
	// 无规则家族：registry 默认值原样保留，其余进复核桶。
	if family == FamilyNone {
		if fillDefault != "" && lowerNormalized == strings.ToLower(strings.TrimRight(strings.TrimSpace(fillDefault), "/")) {
			return unchangedOrNormalize(family, trimmed, normalized)
		}
		return Classification{Family: family, Disposition: DispositionReview, ReviewReason: ReviewUnknownPreset}
	}
	// registry 默认值本身即合规（含尾斜杠变体的等值规整）。
	if fillDefault != "" && lowerNormalized == strings.ToLower(strings.TrimRight(strings.TrimSpace(fillDefault), "/")) {
		return unchangedOrNormalize(family, trimmed, normalized)
	}
	if family == FamilyQwen {
		if strings.HasSuffix(lowerNormalized, qwenStripSuffix) {
			return Classification{Family: family, Disposition: DispositionRewrite,
				NewValue: normalized[:len(normalized)-len(qwenStripSuffix)], ChangeReason: ReasonStripQwenSuffix}
		}
		if isBareOrigin(normalized) {
			// 裸 origin 就是 qwen 的目标形态。
			return unchangedOrNormalize(family, trimmed, normalized)
		}
		return Classification{Family: family, Disposition: DispositionReview, ReviewReason: ReviewNonconforming}
	}
	for _, suffix := range familyKeepSuffixes(family) {
		if strings.HasSuffix(lowerNormalized, suffix) {
			return unchangedOrNormalize(family, trimmed, normalized)
		}
	}
	if isBareOrigin(normalized) {
		return Classification{Family: family, Disposition: DispositionRewrite,
			NewValue: normalized + bareOriginRoot(family, preset), ChangeReason: ReasonAppendVersionRoot}
	}
	return Classification{Family: family, Disposition: DispositionReview, ReviewReason: ReviewNonconforming}
}

// unchangedOrNormalize 返回不变分类；仅当原值带尾斜杠时降级为等值规整改写
// （v2 拼接按 TrimRight 执行，去尾斜杠不改变语义，但让重跑严格幂等）。
func unchangedOrNormalize(family Family, trimmed, normalized string) Classification {
	if trimmed == normalized {
		return Classification{Family: family, Disposition: DispositionUnchanged}
	}
	return Classification{Family: family, Disposition: DispositionRewrite, NewValue: normalized, ChangeReason: ReasonNormalizeSlash}
}

// canonicalBaseURL 校验绝对 http/https URL（host 必填、无 userinfo/query/
// fragment），沿用已退役的 provider connection migration 规则 12 口径。
func canonicalBaseURL(raw string) (string, bool) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", false
	}
	return strings.TrimSpace(raw), true
}

// isBareOrigin 判定规整后 URL 是否裸 origin（TrimRight "/" 之后无任何路径）。
func isBareOrigin(normalized string) bool {
	parsed, err := url.Parse(normalized)
	if err != nil {
		return false
	}
	return parsed.EscapedPath() == ""
}
