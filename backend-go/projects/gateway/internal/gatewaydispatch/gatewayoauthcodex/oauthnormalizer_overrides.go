package gatewayoauthcodex

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"

	"github.com/huanminabc/juhe-ai/backend-go-platform/requestoverride"
)

// GPT account request overrides (BUG-0175 D-151), ported from
// providers/drivers/gpt/request-overrides.ts + request-override-capabilities.ts
// + request-override-body.ts:
//
//	credentials.service_tier_override / reasoning_effort_override are applied to
//	the upstream body at dispatch time, gated by the provider model catalog
//	capabilities (supportedServiceTiers / supportedReasoningEfforts). Without
//	capability evidence the overrides stay inert exactly like the Node
//	effectiveGptAccountRequestOverrides gate.
//
// The catalog read goes through the narrow GptRequestOverrideModelCatalog
// port (composition-root handover mirrors Node
// listCachedProviderModelCatalogAsync); a nil port keeps capability resolution
// returning undefined (overrides inert) instead of guessing.
//
// 值域分层（供应商感知断言）：service_tier / reasoning_effort 的 OpenAI 枚举是
// gpt 供应商专属的 wire 约束，只对 providerCode 规范化后为 gpt 的账户生效；
// 其它供应商（含未知/空 providerCode）只保留凭据 token 形状校验。运行时有效值
// 由 ResolveEffectiveOverrides 按「最终上游模型声明能力」解析（2026-10-18
// 简化定案）：思考级别按 requestoverride 档位序向下兼容降级，服务等级不降级
// （default 恒为强制标准档）；未命中的字段保持客户端原值，不产生请求级硬失败。

// GptServiceTierOverride mirrors GptServiceTierOverride.
type GptServiceTierOverride = string

// GptReasoningEffortOverride mirrors GptReasoningEffortOverride.
type GptReasoningEffortOverride = string

// GptAccountRequestOverrides mirrors GptAccountRequestOverrides.
type GptAccountRequestOverrides struct {
	ServiceTier     GptServiceTierOverride
	ReasoningEffort GptReasoningEffortOverride
}

// gptAccountRequestOverrideCredentialPattern mirrors the optionalCredentialToken
// token shape /^[a-z0-9][a-z0-9._-]{0,63}$/i.
var gptAccountRequestOverrideCredentialPattern = regexp.MustCompile(`(?i)^[a-z0-9][a-z0-9._-]{0,63}$`)

// ReadGptAccountRequestOverrides mirrors readGptAccountRequestOverrides. Like
// the Node source it throws (returns an error) on malformed credential tokens.
func ReadGptAccountRequestOverrides(credentials map[string]any) (GptAccountRequestOverrides, error) {
	serviceTier, err := gptCredentialOverrideToken(credentials, "service_tier_override")
	if err != nil {
		return GptAccountRequestOverrides{}, err
	}
	reasoningEffort, err := gptCredentialOverrideToken(credentials, "reasoning_effort_override")
	if err != nil {
		return GptAccountRequestOverrides{}, err
	}
	return GptAccountRequestOverrides{
		ServiceTier:     serviceTier,
		ReasoningEffort: reasoningEffort,
	}, nil
}

// gptCredentialOverrideToken mirrors optionalCredentialToken.
func gptCredentialOverrideToken(credentials map[string]any, key string) (string, error) {
	if credentials == nil {
		return "", nil
	}
	raw, ok := credentials[key]
	if !ok || raw == nil {
		return "", nil
	}
	text, ok := raw.(string)
	if !ok {
		return "", &GptAccountRequestOverrideError{Message: "账户请求覆盖字段 " + key + " 无效"}
	}
	if text == "" {
		return "", nil
	}
	if text == strings.TrimSpace(text) && gptAccountRequestOverrideCredentialPattern.MatchString(text) {
		return text, nil
	}
	return "", &GptAccountRequestOverrideError{Message: "账户请求覆盖字段 " + key + " 无效"}
}

// gptOverrideProviderCode 是 gpt 专属 wire 枚举适用的供应商 token（与写入面
// accounts.gptVendorCode / accountscore.GptVendorCode 同一 token）；dispatch
// 叶子包不反向依赖 accounts 子域，本地按 lowercase+trim 规范化后比较。
const gptOverrideProviderCode = "gpt"

// normalizeOverrideProviderCode 与 gatewaypreauth / accountkeystates 的
// normalizeProviderToken 同一口径。
func normalizeOverrideProviderCode(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

// AssertGptAccountRequestOverrideValuesForProvider 是供应商感知的值域断言：
// 仅 providerCode 规范化后为 gpt 时执行 gpt 专属 wire 枚举；其它供应商或
// 未知/空 providerCode 跳过枚举（遗漏传入只会少一层冗余校验，不会把请求
// 打成硬失败），值域交给目录能力门。
func AssertGptAccountRequestOverrideValuesForProvider(overrides GptAccountRequestOverrides, providerCode string) error {
	if normalizeOverrideProviderCode(providerCode) != gptOverrideProviderCode {
		return nil
	}
	return assertGptOverrideEnumValues(overrides)
}

// assertGptOverrideEnumValues 是唯一的 gpt 枚举表：service_tier 仅
// default|priority|flex，reasoning_effort 仅
// none|minimal|low|medium|high|xhigh|max（OpenAI 上游 wire 约束）。
func assertGptOverrideEnumValues(overrides GptAccountRequestOverrides) error {
	if overrides.ServiceTier != "" && overrides.ServiceTier != "default" && overrides.ServiceTier != "priority" && overrides.ServiceTier != "flex" {
		return &GptAccountRequestOverrideError{Message: "GPT 账户请求覆盖字段 service_tier_override 无效"}
	}
	switch overrides.ReasoningEffort {
	case "", "none", "minimal", "low", "medium", "high", "xhigh", "max":
	default:
		return &GptAccountRequestOverrideError{Message: "GPT 账户请求覆盖字段 reasoning_effort_override 无效"}
	}
	return nil
}

// AssertGptAccountRequestOverrideValues 保留原函数名，语义收窄为 gpt 专属
// 枚举断言（gatewayoauthcodex_bridge.go 原样导出，根包测试沿用）：等价于对
// gpt 供应商调用 AssertGptAccountRequestOverrideValuesForProvider。
func AssertGptAccountRequestOverrideValues(overrides GptAccountRequestOverrides) error {
	return assertGptOverrideEnumValues(overrides)
}

// HasApplicableGptAccountRequestOverrides mirrors hasApplicableGptAccountRequestOverrides.
func HasApplicableGptAccountRequestOverrides(overrides GptAccountRequestOverrides, endpointFamily string, compact bool) bool {
	if endpointFamily == "" {
		return false
	}
	return overrides.ServiceTier != "" || (!compact && overrides.ReasoningEffort != "")
}

// ResolveEffectiveOverrides 按「最终上游模型声明能力」解析覆盖的有效值
// （2026-10-18 简化定案，取代旧 effectiveGptAccountRequestOverrides 的
// “精确匹配否则丢弃”门，命名与注释反映降级语义）：
//   - 思考级别向下兼容：声明值精确含配置值 → 原值；否则取声明值中档位序不高于
//     配置值的最高档（requestoverride.DegradeReasoningEffort）；无可用档位
//     （声明为空、全部高于配置值或配置值不在档位序内）→ 空串，调用方保留
//     客户端原值不改写。
//   - 服务等级不降级：default 恒为「强制标准档」（调用方删除客户端
//     service_tier，不要求任何档位声明）；priority/flex 仅在声明支持时写入，
//     否则空串（保留客户端原值）。
//   - capabilities 为 nil（目录未命中 / 目录端口未注入）等价于无任何声明。
func ResolveEffectiveOverrides(overrides GptAccountRequestOverrides, capabilities *GptRequestOverrideModelCapabilities) GptAccountRequestOverrides {
	if overrides.ServiceTier == "" && overrides.ReasoningEffort == "" {
		return GptAccountRequestOverrides{}
	}
	effective := GptAccountRequestOverrides{}
	if overrides.ServiceTier != "" {
		if overrides.ServiceTier == "default" {
			effective.ServiceTier = "default"
		} else if capabilities != nil && gptOverrideListContains(capabilities.SupportedServiceTiers, overrides.ServiceTier) {
			effective.ServiceTier = overrides.ServiceTier
		}
	}
	if overrides.ReasoningEffort != "" && capabilities != nil {
		effective.ReasoningEffort = requestoverride.DegradeReasoningEffort(overrides.ReasoningEffort, capabilities.SupportedReasoningEfforts)
	}
	return effective
}

// gptAccountRequestOverridesHook is the composition-root-injected override
// hook consumed by applyOpenAIOAuthCodexAccountRequestOverrides when the
// per-call input leaves the port unset. Set it through
// SetGptAccountRequestOverridesHook (newChainProviderDriver).
var gptAccountRequestOverridesHook func(body map[string]any, input GptAccountOverrideInput) (map[string]any, error)

// SetGptAccountRequestOverridesHooks wires the override hook (composition
// root). The expected value is ApplyGptAccountRequestOverridesBody.
func SetGptAccountRequestOverridesHook(hook func(body map[string]any, input GptAccountOverrideInput) (map[string]any, error)) {
	gptAccountRequestOverridesHook = hook
}

// ApplyGptAccountRequestOverridesBody mirrors applyGptAccountRequestOverrides.
// It is the hook value assigned to
// OpenAIOAuthCodexNormalizeInput.ApplyGptAccountRequestOverrides (the port the
// composition root previously left unwired).
func ApplyGptAccountRequestOverridesBody(inputBody map[string]any, input GptAccountOverrideInput) (map[string]any, error) {
	overrides, err := ReadGptAccountRequestOverrides(input.Credentials)
	if err != nil {
		return nil, err
	}
	if err := AssertGptAccountRequestOverrideValuesForProvider(overrides, input.VendorCode); err != nil {
		return nil, err
	}
	effective := ResolveEffectiveOverrides(overrides, input.ModelCapabilities)
	if !HasApplicableGptAccountRequestOverrides(effective, input.EndpointFamily, input.Compact) {
		return inputBody, nil
	}
	body := cloneJSONObject(inputBody)
	if effective.ServiceTier == "default" {
		delete(body, "service_tier")
	} else if effective.ServiceTier != "" {
		body["service_tier"] = effective.ServiceTier
	}
	if input.Compact || effective.ReasoningEffort == "" {
		return body, nil
	}
	// endpoint family 只保留规范族名（生产调用方只传规范名：OAuth 路径在
	// ApplyOpenAIOAuthCodexAccountRequestOverrides 硬编码 responses；api_key
	// 路径经组合根 gptRequestOverrideEndpointFamily 白名单归一）。
	// Node wire 分支（provider-request-overrides.ts:62-84）：anthropic_messages
	// 写 output_config.effort；gemini 两族写
	// generationConfig.thinkingConfig.thinkingLevel（audit deviation note，
	// 本任务 B-6 补齐）。
	switch input.EndpointFamily {
	case "responses":
		reasoning := map[string]any{}
		if existing := bridgeObjectValueOf(body["reasoning"]); existing != nil {
			reasoning = existing
		}
		reasoning["effort"] = effective.ReasoningEffort
		body["reasoning"] = reasoning
		delete(body, "reasoning_effort")
	case "chat_completions":
		body["reasoning_effort"] = effective.ReasoningEffort
		delete(body, "reasoning")
	case "anthropic_messages":
		outputConfig := map[string]any{}
		if existing := bridgeObjectValueOf(body["output_config"]); existing != nil {
			outputConfig = existing
		}
		outputConfig["effort"] = effective.ReasoningEffort
		body["output_config"] = outputConfig
	case "gemini_generate_content", "gemini_stream_generate":
		generationConfig := map[string]any{}
		if existing := bridgeObjectValueOf(body["generationConfig"]); existing != nil {
			generationConfig = existing
		}
		thinkingConfig := map[string]any{}
		if existing := bridgeObjectValueOf(generationConfig["thinkingConfig"]); existing != nil {
			thinkingConfig = existing
		}
		thinkingConfig["thinkingLevel"] = effective.ReasoningEffort
		generationConfig["thinkingConfig"] = thinkingConfig
		body["generationConfig"] = generationConfig
	}
	return body, nil
}

// gptOverrideListContains reports whether values contains target verbatim.
func gptOverrideListContains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func bridgeObjectValueOf(value any) map[string]any {
	if object, ok := value.(map[string]any); ok {
		return object
	}
	return nil
}

// ---------------------------------------------------------------------------
// Model capability resolution (request-override-capabilities.ts)
// ---------------------------------------------------------------------------

// GptRequestOverrideModelCatalogItem mirrors the catalog row fields the
// capability resolver consumes.
type GptRequestOverrideModelCatalogItem struct {
	Model                     string
	SupportedServiceTiers     []string
	SupportedReasoningEfforts []string
}

// GptRequestOverrideModelCatalog ports listCachedProviderModelCatalogAsync
// for the override capability resolver (composition-root handover).
type GptRequestOverrideModelCatalog interface {
	ListGptRequestOverrideModelCatalog(ctx context.Context, providerCode, systemAccountID string, includeUnpriced bool) ([]GptRequestOverrideModelCatalogItem, error)
}

// gptRequestOverrideModelCatalog is the injectable catalog port; nil keeps
// ResolveGptRequestOverrideModelCapabilities returning nil (overrides inert).
var gptRequestOverrideModelCatalog GptRequestOverrideModelCatalog

// gptRequestOverrideModelCandidates mirrors the
// modelPricingProviderDriverForProvider(providerCode).buildModelCandidates(model)
// fallback chain: the requested model plus vendor-equivalent spellings. The
// gateway side only needs the plain identity candidate unless a provider
// driver registers extra spellings.
var gptRequestOverrideModelCandidates = func(providerCode, model string) []string {
	candidates := []string{model}
	if normalized := strings.TrimSpace(model); normalized != model && normalized != "" {
		candidates = append(candidates, normalized)
	}
	return candidates
}

// SetGptRequestOverrideModelCatalog wires the catalog port (composition root).
func SetGptRequestOverrideModelCatalog(catalog GptRequestOverrideModelCatalog) {
	gptRequestOverrideModelCatalog = catalog
}

// SetGptRequestOverrideModelCandidates overrides the model-candidate expander
// (test / provider-driver handover).
func SetGptRequestOverrideModelCandidates(expander func(providerCode, model string) []string) {
	if expander != nil {
		gptRequestOverrideModelCandidates = expander
	}
}

// overrideVendorCode 解析覆盖运行时需要的供应商身份：只认供应商解耦后的
// VendorCode（供应商与上游协议面重构设计 §1.1/§2.1）。存量库该列由迁移脚本
// 回填，代码不做旧列回落（不向后兼容）。为空时返回空串：gpt 专属枚举跳过、
// 能力解析不查目录（覆盖惰性）。
func overrideVendorCode(account gatewayruntimecache.OpenAIAccountSecret) string {
	return strings.TrimSpace(account.VendorCode)
}

// ResolveGptRequestOverrideModelCapabilities mirrors
// resolveGptRequestOverrideModelCapabilities.
func ResolveGptRequestOverrideModelCapabilities(ctx context.Context, account gatewayruntimecache.OpenAIAccountSecret, upstreamModel string) (*GptRequestOverrideModelCapabilities, error) {
	model := strings.TrimSpace(upstreamModel)
	if model == "" {
		return nil, nil
	}
	overrides, err := ReadGptAccountRequestOverrides(account.Credentials)
	if err != nil {
		return nil, err
	}
	if err := AssertGptAccountRequestOverrideValuesForProvider(overrides, overrideVendorCode(account)); err != nil {
		return nil, err
	}
	if overrides.ServiceTier == "" && overrides.ReasoningEffort == "" {
		return nil, nil
	}
	providerCode := overrideVendorCode(account)
	if providerCode == "" {
		return nil, nil
	}
	if gptRequestOverrideModelCatalog == nil {
		return nil, nil
	}
	catalog, err := gptRequestOverrideModelCatalog.ListGptRequestOverrideModelCatalog(ctx, providerCode, account.AccountOwnerSystemAccountID, true)
	if err != nil {
		return nil, err
	}
	for _, candidate := range gptRequestOverrideModelCandidates(providerCode, model) {
		for _, item := range catalog {
			if strings.TrimSpace(item.Model) != candidate {
				continue
			}
			return &GptRequestOverrideModelCapabilities{
				SupportedServiceTiers:     item.SupportedServiceTiers,
				SupportedReasoningEfforts: item.SupportedReasoningEfforts,
			}, nil
		}
	}
	return nil, nil
}

// ApplyGptAccountRequestOverridesToUpstreamBody mirrors
// applyGptAccountRequestOverridesToBody for the gateway non-OAuth request
// path: body stays untouched unless applicable overrides survive the
// capability gate; otherwise the JSON object body is rewritten in place.
func ApplyGptAccountRequestOverridesToUpstreamBody(
	ctx context.Context,
	body []byte,
	account gatewayruntimecache.OpenAIAccountSecret,
	endpointFamily string,
	compact bool,
	upstreamModel string,
) ([]byte, error) {
	overrides, err := ReadGptAccountRequestOverrides(account.Credentials)
	if err != nil {
		return nil, err
	}
	if err := AssertGptAccountRequestOverrideValuesForProvider(overrides, overrideVendorCode(account)); err != nil {
		return nil, err
	}
	if !HasApplicableGptAccountRequestOverrides(overrides, endpointFamily, compact) {
		return body, nil
	}
	model := strings.TrimSpace(upstreamModel)
	if model == "" {
		parsed := map[string]any{}
		if err := json.Unmarshal(body, &parsed); err != nil || parsed == nil {
			return nil, &GptAccountRequestOverrideError{Message: "账户请求覆盖要求请求体是有效的 JSON 对象"}
		}
		model, _ = parsed["model"].(string)
	}
	capabilities, err := ResolveGptRequestOverrideModelCapabilities(ctx, account, model)
	if err != nil {
		return nil, err
	}
	effective := ResolveEffectiveOverrides(overrides, capabilities)
	if !HasApplicableGptAccountRequestOverrides(effective, endpointFamily, compact) {
		return body, nil
	}
	parsed := map[string]any{}
	if err := json.Unmarshal(body, &parsed); err != nil || parsed == nil {
		return nil, &GptAccountRequestOverrideError{Message: "账户请求覆盖要求请求体是有效的 JSON 对象"}
	}
	overridden, err := ApplyGptAccountRequestOverridesBody(parsed, GptAccountOverrideInput{
		Credentials:       account.Credentials,
		VendorCode:        overrideVendorCode(account),
		EndpointFamily:    endpointFamily,
		Compact:           compact,
		ModelCapabilities: capabilities,
	})
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(overridden)
	if err != nil {
		return nil, err
	}
	return encoded, nil
}
