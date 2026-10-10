package gatewaydispatch

import (
	"net/http"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch/gatewayoauthcodex"
)

// gatewayoauthcodex 门面桥（REFACTOR-0006 阶段 A）。
//
// OAuth normalizer/overrides、codex 适配器、builtin tools、codex turn 规避与
// provider 协议谓词的 oauth 侧消费面已迁入 gatewayoauthcodex 叶子包；本桥以
// 类型 alias、函数值别名与自由函数转发保持根包留守文件、测试文件与全部外部
// 消费文件的 `gatewaydispatch.XXX` 前缀引用零改动。转发均为一行直调，无接口
// 间接层。
//
// 注入语义：SanitizeCodexHistory 是可注入 hook 变量（生产恒 nil），真身唯一
// 留在 gatewayoauthcodex（oauthnormalizer.go）；根包测试的注入点经
// `gatewayoauthcodex.SanitizeCodexHistory` 前缀直接写同一变量；根包
// accountpreparation.go 的读取点经下方 sanitizeCodexHistoryHookIfSet 以活
// 绑定读取同一变量（调度内核通用化批次 3b：审计门五文件不再出现
// gatewayoauthcodex 前缀），保持单一全局注入语义不变。

// --- OpenAI OAuth Codex 适配器错误（原 errors.go 243-295 块） ---

type OpenAIOAuthCodexAdapterError = gatewayoauthcodex.OpenAIOAuthCodexAdapterError

type CodexAdapterErrorOption = gatewayoauthcodex.CodexAdapterErrorOption

var (
	NewOpenAIOAuthCodexAdapterError = gatewayoauthcodex.NewOpenAIOAuthCodexAdapterError
	IsOpenAIOAuthCodexAdapterError  = gatewayoauthcodex.IsOpenAIOAuthCodexAdapterError

	WithCodexAdapterCode          = gatewayoauthcodex.WithCodexAdapterCode
	WithCodexAdapterStatus        = gatewayoauthcodex.WithCodexAdapterStatus
	WithCodexAdapterType          = gatewayoauthcodex.WithCodexAdapterType
	WithCodexAdapterAccountScoped = gatewayoauthcodex.WithCodexAdapterAccountScoped
)

// --- Codex history 清洗标记（原 serialized.go 13-54 块） ---

type CodexHistorySanitizeResult = gatewayoauthcodex.CodexHistorySanitizeResult

type SanitizeCodexHistoryItems = gatewayoauthcodex.SanitizeCodexHistoryItems

type SanitizeCodexHistoryOptions = gatewayoauthcodex.SanitizeCodexHistoryOptions

var (
	MarkGatewayCodexHistorySanitized = gatewayoauthcodex.MarkGatewayCodexHistorySanitized
	IsGatewayCodexHistorySanitized   = gatewayoauthcodex.IsGatewayCodexHistorySanitized
)

// sanitizeCodexHistoryHookIfSet 以活绑定方式读取可注入历史清理 hook
// （gatewayoauthcodex.SanitizeCodexHistory，生产恒 nil，测试注入）：未注入时
// 返回 false，调用方保持输入不变。经函数读取而非 init 期值拷贝，保证测试
// 注入对根包消费点始终可见。
func sanitizeCodexHistoryHookIfSet(items []any, options SanitizeCodexHistoryOptions) (CodexHistorySanitizeResult, bool) {
	if gatewayoauthcodex.SanitizeCodexHistory == nil {
		return CodexHistorySanitizeResult{}, false
	}
	return gatewayoauthcodex.SanitizeCodexHistory(items, options), true
}

// --- OAuth codex 规范化 / 适配（原 oauthnormalizer.go / oauthadapter.go /
// builtintools.go 导出面） ---

type OpenAIOAuthCodexAccount = gatewayoauthcodex.OpenAIOAuthCodexAccount

type OpenAIOAuthCodexIdentity = gatewayoauthcodex.OpenAIOAuthCodexIdentity

type OpenAIOAuthCodexNormalizeInput = gatewayoauthcodex.OpenAIOAuthCodexNormalizeInput

type OpenAIOAuthCodexRequestOptions = gatewayoauthcodex.OpenAIOAuthCodexRequestOptions

type OpenAIOAuthCodexRequestParts = gatewayoauthcodex.OpenAIOAuthCodexRequestParts

type OpenAIOAuthCodexSessionResolution = gatewayoauthcodex.OpenAIOAuthCodexSessionResolution

type NormalizedCodexBody = gatewayoauthcodex.NormalizedCodexBody

var (
	NormalizeOpenAIOAuthCodexParsedBody = gatewayoauthcodex.NormalizeOpenAIOAuthCodexParsedBody
	NormalizeOpenAIOAuthCodexRawBody    = gatewayoauthcodex.NormalizeOpenAIOAuthCodexRawBody
	BuildOpenAIOAuthCodexRequestParts   = gatewayoauthcodex.BuildOpenAIOAuthCodexRequestParts

	EnsureOpenAIOAuthCodexPlainJsonObject = gatewayoauthcodex.EnsureOpenAIOAuthCodexPlainJsonObject
	ResolveOpenAIOAuthCodexSession        = gatewayoauthcodex.ResolveOpenAIOAuthCodexSession
	IsolateOpenAIOAuthCodexSessionID      = gatewayoauthcodex.IsolateOpenAIOAuthCodexSessionID
	NormalizeOpenAICodexBuiltinTools      = gatewayoauthcodex.NormalizeOpenAICodexBuiltinTools
)

// oauth 规范化内部谓词（finalgap 等留根测试消费，经桥转发）。
var (
	ValidateOpenAIOAuthCodexBody           = gatewayoauthcodex.ValidateOpenAIOAuthCodexBody
	EnsureOpenAIOAuthCodexReasoningInclude = gatewayoauthcodex.EnsureOpenAIOAuthCodexReasoningInclude
)

// --- GPT 账户请求覆盖（原 oauthnormalizer_overrides.go 导出面） ---

type GptAccountOverrideInput = gatewayoauthcodex.GptAccountOverrideInput

type GptAccountRequestOverrideError = gatewayoauthcodex.GptAccountRequestOverrideError

type GptAccountRequestOverrides = gatewayoauthcodex.GptAccountRequestOverrides

type GptReasoningEffortOverride = gatewayoauthcodex.GptReasoningEffortOverride

type GptRequestOverrideModelCapabilities = gatewayoauthcodex.GptRequestOverrideModelCapabilities

type GptRequestOverrideModelCatalog = gatewayoauthcodex.GptRequestOverrideModelCatalog

type GptRequestOverrideModelCatalogItem = gatewayoauthcodex.GptRequestOverrideModelCatalogItem

type GptServiceTierOverride = gatewayoauthcodex.GptServiceTierOverride

var (
	ApplyGptAccountRequestOverridesBody           = gatewayoauthcodex.ApplyGptAccountRequestOverridesBody
	ApplyGptAccountRequestOverridesToUpstreamBody = gatewayoauthcodex.ApplyGptAccountRequestOverridesToUpstreamBody
	AssertGptAccountRequestOverrideValues         = gatewayoauthcodex.AssertGptAccountRequestOverrideValues
	EffectiveGptAccountRequestOverrides           = gatewayoauthcodex.EffectiveGptAccountRequestOverrides
	HasApplicableGptAccountRequestOverrides       = gatewayoauthcodex.HasApplicableGptAccountRequestOverrides
	ReadGptAccountRequestOverrides                = gatewayoauthcodex.ReadGptAccountRequestOverrides
	ResolveGptRequestOverrideModelCapabilities    = gatewayoauthcodex.ResolveGptRequestOverrideModelCapabilities

	SetGptAccountRequestOverridesHook    = gatewayoauthcodex.SetGptAccountRequestOverridesHook
	SetGptRequestOverrideModelCandidates = gatewayoauthcodex.SetGptRequestOverrideModelCandidates
	SetGptRequestOverrideModelCatalog    = gatewayoauthcodex.SetGptRequestOverrideModelCatalog
)

// --- Codex turn 规避过滤（原 codexturnavoidance.go）---
// 调度内核通用化（设计 5.1，批次 1）后内核不再消费 turn 避让纯函数：排除语义
// 由准备层固化的通用调度排除集（SchedulingExclusions）承载，过滤/翻回由内核
// 分派段状态机取代。codexturnavoidance.go 及其桥接转发已随批次删除。

func jsonValueEqual(left, right any) bool {
	return gatewayoauthcodex.JSONValueEqual(left, right)
}

func validateOpenAIOAuthCodexBody(body map[string]any, compact bool) error {
	return gatewayoauthcodex.ValidateOpenAIOAuthCodexBody(body, compact)
}

func ensureOpenAIOAuthCodexReasoningInclude(body map[string]any) {
	gatewayoauthcodex.EnsureOpenAIOAuthCodexReasoningInclude(body)
}

func applyOpenAIOAuthCodexSessionToBody(body map[string]any, session OpenAIOAuthCodexSessionResolution, compact bool) {
	gatewayoauthcodex.ApplyOpenAIOAuthCodexSessionToBody(body, session, compact)
}

func normalizeOpenAIOAuthCodexLegacyFunctions(body map[string]any) {
	gatewayoauthcodex.NormalizeOpenAIOAuthCodexLegacyFunctions(body)
}

func parseOpenAIOAuthCodexJsonObjectBody(req *gatewaypreauth.GatewayRequest) (any, error) {
	return gatewayoauthcodex.ParseOpenAIOAuthCodexJsonObjectBody(req)
}

func sanitizeOpenAIOAuthCodexHistory(body map[string]any, accountID string) {
	gatewayoauthcodex.SanitizeOpenAIOAuthCodexHistory(body, accountID)
}

func resolveOpenAIOAuthCodexSession(inputHeaders http.Header, body map[string]any, account OpenAIOAuthCodexAccount, identity OpenAIOAuthCodexIdentity) OpenAIOAuthCodexSessionResolution {
	return gatewayoauthcodex.ResolveOpenAIOAuthCodexSession(inputHeaders, body, account, identity)
}

func rawBodyOf(req *gatewaypreauth.GatewayRequest) []byte {
	return gatewayoauthcodex.RawBodyOf(req)
}

func gatewaypreauthJSONParseStatusInvalid() string {
	return gatewayoauthcodex.GatewayPreauthJSONParseStatusInvalid()
}

func normalizeOpenAIOAuthCodexInput(body map[string]any) {
	gatewayoauthcodex.NormalizeOpenAIOAuthCodexInput(body)
}

func asOverrideError(err error, target **GptAccountRequestOverrideError) bool {
	return gatewayoauthcodex.AsOverrideError(err, target)
}

func applyOpenAIOAuthCodexAccountRequestOverrides(body map[string]any, input OpenAIOAuthCodexNormalizeInput) error {
	return gatewayoauthcodex.ApplyOpenAIOAuthCodexAccountRequestOverrides(body, input)
}
