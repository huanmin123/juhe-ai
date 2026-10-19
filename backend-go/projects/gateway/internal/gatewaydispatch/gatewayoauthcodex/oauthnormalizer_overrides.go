package gatewayoauthcodex

import (
	"context"
	"encoding/json"
	"net/url"
	"regexp"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch/gatewayupstream"
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
// port handed over explicitly per call inside GptRequestOverrideRuntime
// (composition-root wiring mirrors Node listCachedProviderModelCatalogAsync);
// a nil port keeps capability resolution returning nil (overrides inert)
// instead of guessing. Since the T15 convergence the ports ride the explicit
// per-call parameter — there is no process-level global and no Set* accessor.
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

// assertGptOverrideEnumValues 是 gpt 专属 wire 枚举断言：值域单一真源为
// shared/platform/requestoverride（gpt 供应商属于 openai 协议族，经
// FamilyForProvider 解析），service_tier 仅 default|priority|flex，
// reasoning_effort 仅 none|minimal|low|medium|high|xhigh|max（OpenAI 上游
// wire 约束）。空值表示未配置、直接放行；错误类型与文案是本包既有契约，
// 逐字保留。
func assertGptOverrideEnumValues(overrides GptAccountRequestOverrides) error {
	family := requestoverride.FamilyForProvider(gptOverrideProviderCode)
	if overrides.ServiceTier != "" && !requestoverride.SupportsServiceTier(family, overrides.ServiceTier) {
		return &GptAccountRequestOverrideError{Message: "GPT 账户请求覆盖字段 service_tier_override 无效"}
	}
	if overrides.ReasoningEffort != "" && !requestoverride.SupportsReasoningEffort(family, overrides.ReasoningEffort) {
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

// ApplyGptAccountRequestOverridesBody mirrors applyGptAccountRequestOverrides.
// It is the explicit hook value assigned to
// OpenAIOAuthCodexNormalizeInput.ApplyGptAccountRequestOverrides (composition
// root wires it through OpenAIOAuthCodexRequestOptions since the T15
// explicit-parameter convergence; there is no process-level fallback port).
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
	applyGptAccountRequestOverrideWireFields(body, effective, input.EndpointFamily, input.Compact)
	return body, nil
}

// applyGptAccountRequestOverrideWireFields 把 effective 覆盖值就地写入 body
// （共享实现：OAuth hook 路径 ApplyGptAccountRequestOverridesBody 与 api_key
// 合并路径 ApplyGptAccountRequestOverridesAndNormalizeReasoningToUpstreamBody
// 必须保持同一 wire 形态，避免双份漂移）。
// endpoint family 只保留规范族名（生产调用方只传规范名：OAuth 路径在
// ApplyOpenAIOAuthCodexAccountRequestOverrides 硬编码 responses；api_key
// 路径经组合根 gptRequestOverrideEndpointFamily 白名单归一）。
// Node wire 分支（provider-request-overrides.ts:62-84）：anthropic_messages
// 写 output_config.effort；gemini 两族写
// generationConfig.thinkingConfig.thinkingLevel（audit deviation note，
// 本任务 B-6 补齐）。
func applyGptAccountRequestOverrideWireFields(body map[string]any, effective GptAccountRequestOverrides, endpointFamily string, compact bool) {
	if effective.ServiceTier == "default" {
		delete(body, "service_tier")
	} else if effective.ServiceTier != "" {
		body["service_tier"] = effective.ServiceTier
	}
	if compact || effective.ReasoningEffort == "" {
		return
	}
	switch endpointFamily {
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

// GptRequestOverrideRuntime 是覆盖运行时端口的显式传参载体（T15 收敛：取代
// 进程级全局目录端口与候选展开器及对应 Set*）。组合根在构造 driver时装入，
// 随每次能力解析/覆盖应用调用显式传入；nil Catalog 等价旧全局 nil 初始态
// （能力解析返回 nil，覆盖惰性），nil ModelCandidates 使用包内默认展开
// （与旧全局初始值同一实现）。
type GptRequestOverrideRuntime struct {
	// Catalog ports listCachedProviderModelCatalogAsync for the override
	// capability resolver (composition-root handover); nil keeps capability
	// resolution returning nil (overrides inert).
	Catalog GptRequestOverrideModelCatalog
	// ModelCandidates mirrors the
	// modelPricingProviderDriverForProvider(providerCode).buildModelCandidates(model)
	// fallback chain: the requested model plus vendor-equivalent spellings. nil
	// falls back to defaultGptRequestOverrideModelCandidates.
	ModelCandidates func(providerCode, model string) []string
}

// defaultGptRequestOverrideModelCandidates 是 ModelCandidates 为 nil 时的默认
// 展开（与收敛前的全局初始值逐字同实现）：请求模型本体 + trim 归一拼写。
func defaultGptRequestOverrideModelCandidates(providerCode, model string) []string {
	candidates := []string{model}
	if normalized := strings.TrimSpace(model); normalized != model && normalized != "" {
		candidates = append(candidates, normalized)
	}
	return candidates
}

// modelCandidatesOf 解析本次调用生效的候选展开器。
func (runtime GptRequestOverrideRuntime) modelCandidatesOf() func(providerCode, model string) []string {
	if runtime.ModelCandidates != nil {
		return runtime.ModelCandidates
	}
	return defaultGptRequestOverrideModelCandidates
}

// overrideVendorCode 解析覆盖运行时需要的供应商身份：只认供应商解耦后的
// VendorCode（供应商与上游协议面重构设计 §1.1/§2.1）。存量库该列由迁移脚本
// 回填，代码不做旧列回落（不向后兼容）。为空时返回空串：gpt 专属枚举跳过、
// 能力解析不查目录（覆盖惰性）。
func overrideVendorCode(account gatewayruntimecache.OpenAIAccountSecret) string {
	return strings.TrimSpace(account.VendorCode)
}

// resolvedGptAccountRequestOverrides 是覆盖调用链一次性的 Read+Assert 前置产物
// （T14 收敛：api_key 出站链此前对同一凭据 Read×3 / Assert×3，收敛为入口一次，
// 下游仅查能力/应用覆盖直接复用本产物）。
type resolvedGptAccountRequestOverrides struct {
	overrides       GptAccountRequestOverrides
	vendorCode      string
	systemAccountID string
}

// resolveGptAccountRequestOverridesForAccount 读取凭据覆盖并执行供应商感知
// 值域断言；错误语义与文案与单次 Read/Assert 逐字一致（malformed token 原样
// 外抛）。
func resolveGptAccountRequestOverridesForAccount(account gatewayruntimecache.OpenAIAccountSecret) (resolvedGptAccountRequestOverrides, error) {
	overrides, err := ReadGptAccountRequestOverrides(account.Credentials)
	if err != nil {
		return resolvedGptAccountRequestOverrides{}, err
	}
	vendorCode := overrideVendorCode(account)
	if err := AssertGptAccountRequestOverrideValuesForProvider(overrides, vendorCode); err != nil {
		return resolvedGptAccountRequestOverrides{}, err
	}
	return resolvedGptAccountRequestOverrides{
		overrides:       overrides,
		vendorCode:      vendorCode,
		systemAccountID: account.AccountOwnerSystemAccountID,
	}, nil
}

// resolveModelCapabilities 是「仅查能力」变体（T14）：在已解析前置产物之上
// 执行目录能力解析，不再重复 Read/Assert。早退顺序与收敛前逐点一致：
// 模型为空 → 覆盖为空 → 供应商身份为空 → 目录端口缺席 → 目录查询（恰 1 次）。
func (r resolvedGptAccountRequestOverrides) resolveModelCapabilities(ctx context.Context, runtime GptRequestOverrideRuntime, upstreamModel string) (*GptRequestOverrideModelCapabilities, error) {
	model := strings.TrimSpace(upstreamModel)
	if model == "" {
		return nil, nil
	}
	if r.overrides.ServiceTier == "" && r.overrides.ReasoningEffort == "" {
		return nil, nil
	}
	if r.vendorCode == "" {
		return nil, nil
	}
	if runtime.Catalog == nil {
		return nil, nil
	}
	catalog, err := runtime.Catalog.ListGptRequestOverrideModelCatalog(ctx, r.vendorCode, r.systemAccountID, true)
	if err != nil {
		return nil, err
	}
	for _, candidate := range runtime.modelCandidatesOf()(r.vendorCode, model) {
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

// ResolveGptRequestOverrideModelCapabilities mirrors
// resolveGptRequestOverrideModelCapabilities：Read+Assert 前置产物一次解析后
// 进入「仅查能力」变体。目录端口与候选展开器经 runtime 显式传入（T15）。
func ResolveGptRequestOverrideModelCapabilities(ctx context.Context, account gatewayruntimecache.OpenAIAccountSecret, upstreamModel string, runtime GptRequestOverrideRuntime) (*GptRequestOverrideModelCapabilities, error) {
	resolved, err := resolveGptAccountRequestOverridesForAccount(account)
	if err != nil {
		return nil, err
	}
	return resolved.resolveModelCapabilities(ctx, runtime, upstreamModel)
}

// ApplyGptAccountRequestOverridesAndNormalizeReasoningToUpstreamBody 是 api_key
// 出站链的合并入口（T14 + T17 收敛，取代原
// ApplyGptAccountRequestOverridesToUpstreamBody + 独立
// NormalizeOpenAIReasoningFieldsForUpstream 两步）：同一凭据 Read/Assert/目录
// 能力解析各至多一次，body 至多 parse→mutate→serialize 一轮，保序为覆盖先行、
// reasoning wire 归一随后。零行为差契约：
//   - 端点族为空（媒体等非五族路径）时与收敛前一致地整段跳过覆盖：不读取
//     凭据、不执行值域断言，reasoning 归一无动作时逐字节返回原始 body；
//   - 非法凭据 token / gpt 枚举的错误文案逐字外抛（仅五族路径）；
//   - 覆盖与归一都无动作时逐字节返回原始 body（零解析路径）；
//   - 覆盖命中时输出仍是全量重序列化（键序同 json.Marshal(map)）；
//   - body 非 JSON 对象且覆盖命中 →
//     「账户请求覆盖要求请求体是有效的 JSON 对象」；覆盖未命中且仅归一涉及
//     时保持原字节（归一既有语义）。
//
// parsedBody 是调用方持有的 request 级已解析 map（与 body 字节同源，例如
// chain_driver 的 MaterializedParsedJSONObjectBody 缓存）；nil 时本函数按需
// 自行解析一次。map 永不被改写（改写发生在深拷贝上），调用方缓存可安全复用。
func ApplyGptAccountRequestOverridesAndNormalizeReasoningToUpstreamBody(
	ctx context.Context,
	body []byte,
	parsedBody map[string]any,
	account gatewayruntimecache.OpenAIAccountSecret,
	endpointFamily string,
	compact bool,
	upstreamModel string,
	reasoningPath string,
	runtime GptRequestOverrideRuntime,
) ([]byte, error) {
	// 端点族为空时收敛前整段跳过覆盖应用（组合根只在该族非空时调用），
	// 因此这里也不得读取凭据或执行值域断言：媒体等非五族路径的失败面必须
	// 与旧两步管线一致，不因凭据中的覆盖 token 形状而新增错误。
	var resolved resolvedGptAccountRequestOverrides
	if endpointFamily != "" {
		var resolveErr error
		resolved, resolveErr = resolveGptAccountRequestOverridesForAccount(account)
		if resolveErr != nil {
			return nil, resolveErr
		}
	}
	overrideGate := HasApplicableGptAccountRequestOverrides(resolved.overrides, endpointFamily, compact)
	reasoningFamily := openAIReasoningWireFamily(reasoningPath)
	reasoningGate := reasoningFamily != "" && len(body) > 0
	if !overrideGate && !reasoningGate {
		return body, nil
	}

	var effective GptAccountRequestOverrides
	if overrideGate {
		model := strings.TrimSpace(upstreamModel)
		if model == "" {
			// 收敛前语义：模型未随请求传入时先解析 body 提取 model，解析失败
			// 即覆盖硬失败（先于目录查询）。
			parsed, parseOK := parsedJSONObjectOf(body, parsedBody)
			if !parseOK {
				return nil, &GptAccountRequestOverrideError{Message: "账户请求覆盖要求请求体是有效的 JSON 对象"}
			}
			model, _ = parsed["model"].(string)
		}
		capabilities, err := resolved.resolveModelCapabilities(ctx, runtime, model)
		if err != nil {
			return nil, err
		}
		effective = ResolveEffectiveOverrides(resolved.overrides, capabilities)
		if !HasApplicableGptAccountRequestOverrides(effective, endpointFamily, compact) {
			effective = GptAccountRequestOverrides{}
		}
	}
	overrideApplied := effective.ServiceTier != "" || (!compact && effective.ReasoningEffort != "")
	if !overrideApplied && !reasoningGate {
		return body, nil
	}

	parsed, parseOK := parsedJSONObjectOf(body, parsedBody)
	if !parseOK {
		if overrideApplied {
			return nil, &GptAccountRequestOverrideError{Message: "账户请求覆盖要求请求体是有效的 JSON 对象"}
		}
		return body, nil
	}
	// 改写只发生在深拷贝上：调用方的 request 级解析缓存保持只读，候选轮换的
	// 后续尝试仍能复用同一 map。
	work := cloneJSONObject(parsed)
	if overrideApplied {
		applyGptAccountRequestOverrideWireFields(work, effective, endpointFamily, compact)
	}
	reasoningChanged := false
	if reasoningGate {
		reasoningChanged = applyOpenAIReasoningWireMutation(work, reasoningFamily)
	}
	if !overrideApplied && !reasoningChanged {
		return body, nil
	}
	encoded, err := json.Marshal(work)
	if err != nil {
		return nil, err
	}
	return encoded, nil
}

// parsedJSONObjectOf 返回出站 body 的已解析 JSON 对象：优先复用调用方传入的
// request 级 map（必须与 body 字节同源），否则解析一次。解析失败或非 JSON
// 对象（含 null → nil map）返回 ok=false，与收敛前 `json.Unmarshal 失败或
// parsed == nil` 判定逐字同义。
func parsedJSONObjectOf(body []byte, parsedBody map[string]any) (map[string]any, bool) {
	if parsedBody != nil {
		return parsedBody, true
	}
	parsed := map[string]any{}
	if err := json.Unmarshal(body, &parsed); err != nil || parsed == nil {
		return nil, false
	}
	return parsed, true
}

// openAIReasoningWireFamily mirrors internal/gatewaydispatch
// openAIReasoningEndpointFamily（bodypreparation.go，T17 合并所需同源镜像）：
// reasoning wire 归一的路径 → 规范族。T16 收敛点：端点族词表单一入口落地后
// 改引共享 helper。
func openAIReasoningWireFamily(path string) string {
	parsed, err := url.Parse(path)
	if err != nil {
		return ""
	}
	switch gatewayupstream.StripV1Prefix(parsed.Path) {
	case "/chat/completions":
		return "chat_completions"
	case "/responses", "/responses/compact":
		return "responses"
	default:
		return ""
	}
}

// applyOpenAIReasoningWireMutation 是 NormalizeOpenAIReasoningFieldsForUpstream
// 的 map 级镜像（T17 合并所需同源镜像，bodypreparation.go 语义逐点一致）：
// chat_completions 族保留顶层 reasoning_effort、删除 reasoning 对象；
// responses 族保留 reasoning.effort、删除顶层 reasoning_effort；两 shape 的
// effort 均缺省时不动作。返回是否发生协议字段改写。
func applyOpenAIReasoningWireMutation(parsed map[string]any, family string) bool {
	reasoning, hasReasoning := parsed["reasoning"]
	reasoningObject, _ := reasoning.(map[string]any)
	nestedEffort, nestedOK := reasoningObject["effort"].(string)
	nestedEffort = strings.TrimSpace(nestedEffort)
	flatEffort, flatOK := parsed["reasoning_effort"].(string)
	flatEffort = strings.TrimSpace(flatEffort)
	if !nestedOK || nestedEffort == "" {
		nestedEffort = ""
	}
	if !flatOK || flatEffort == "" {
		flatEffort = ""
	}
	if nestedEffort == "" && flatEffort == "" {
		return false
	}

	changed := false
	switch family {
	case "chat_completions":
		if flatEffort == "" && nestedEffort != "" {
			parsed["reasoning_effort"] = nestedEffort
			changed = true
		}
		if hasReasoning {
			delete(parsed, "reasoning")
			changed = true
		}
	case "responses":
		if nestedEffort == "" && flatEffort != "" {
			parsed["reasoning"] = map[string]any{"effort": flatEffort}
			changed = true
		}
		if _, ok := parsed["reasoning_effort"]; ok {
			delete(parsed, "reasoning_effort")
			changed = true
		}
	}
	return changed
}
