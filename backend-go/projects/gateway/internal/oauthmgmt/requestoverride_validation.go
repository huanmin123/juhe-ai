package oauthmgmt

import (
	"github.com/huanminabc/juhe-ai/backend-go-platform/requestoverride"
)

// 账户请求覆盖字段（credentials.service_tier_override /
// reasoning_effort_override）的落库值域校验（C3，2026-10-18 简化定案）。
//
// 写入面同一规则：token 形状（requestoverride.IsToken：首尾空白非法、长度上限
// 64、字符集 [a-z0-9][a-z0-9._-]）+ 协议族值域全集（requestoverride
// ServiceTiers / ReasoningEfforts）。枚举与谓词的单一真源是 shared/platform
// requestoverride——账户写入面归一化（internal/accounts
// normalizeGPTAccountRequestOverrides）与网关运行时都引用同一包，本层调用同一
// 谓词，不是在本包重抄枚举表，两处规则不会漂移。
//
// 到达点差异（providers.go patch parser 注释的自述由本文件补全）：创建链路
// （CreateOAuthAccount → accounts.Create）已过写入面归一化；轮换与重授权
// （refreshToken / reauthorizeFrom* HTTP 链路）共用 RotateCredentials 落库、
// 历史上不经过写入面——畸形值可落库并在请求时被运行时
// gatewayoauthcodex.ReadGptAccountRequestOverrides 打成硬失败。本校验在
// RotateCredentials 事务内先执行，非法值拒绝且不写库。
//
// 协议族判定：oauthmgmt 可达的四家 OAuth 供应商 providerCode 与协议族一一对应
// （gpt/xai → openai 族、anthropic → anthropic 族、gemini → gemini 族），
// FamilyForProvider 与写入面 FamilyOf(providerCode, protocolCode) 在本入口
// 可达账户上判定一致。写入面的 Gemini Interactions-only 可配置性守卫不在此
// 重复：覆盖字段与 endpoint modes 的组合只能经创建/更新面引入（该面已执行
// 守卫），轮换按 mergeRotationCredentials 原样保留存量组合，不会新造出
// "Interactions-only + 覆盖字段"状态。

// validateAccountRequestOverrideCredentials 校验凭据记录中的两个请求覆盖
// 字段：缺省/空串放行（写入面语义为省略字段，本层不改写凭据）；非法值返回
// ValidationError，错误文案与写入面逐字一致。
func validateAccountRequestOverrideCredentials(providerCode string, credentials map[string]any) error {
	if credentials == nil {
		return nil
	}
	serviceTier, err := overrideCredentialToken(credentials, "service_tier_override", "服务等级覆盖")
	if err != nil {
		return err
	}
	reasoningEffort, err := overrideCredentialToken(credentials, "reasoning_effort_override", "思考级别覆盖")
	if err != nil {
		return err
	}
	if serviceTier == "" && reasoningEffort == "" {
		return nil
	}
	family := requestoverride.FamilyForProvider(providerCode)
	// default 是「强制标准档」哨兵，语义族无关，恒可保存；其余档位必须属于
	// 协议族服务等级全集（anthropic 族为空集，任何非 default 值都被拒）。
	if serviceTier != "" && serviceTier != "default" && !requestoverride.SupportsServiceTier(family, serviceTier) {
		return &ValidationError{Message: "服务等级覆盖无效"}
	}
	if reasoningEffort != "" && !requestoverride.SupportsReasoningEffort(family, reasoningEffort) {
		return &ValidationError{Message: "思考级别覆盖无效"}
	}
	return nil
}

// overrideCredentialToken 与写入面 optionalCredentialToken 同语义：缺省/nil/
// 空串 → 空串（省略）；非字符串或不符合 requestoverride.IsToken 形状 →
// ValidationError（label + "无效"）。
func overrideCredentialToken(credentials map[string]any, key, label string) (string, error) {
	raw, present := credentials[key]
	if !present || raw == nil {
		return "", nil
	}
	text, ok := raw.(string)
	if !ok {
		return "", &ValidationError{Message: label + "无效"}
	}
	if text == "" {
		return "", nil
	}
	// IsToken 不做 trim 归一：带首尾空白的值非法（与写入面一致）。经
	// credentialsPatch 进来的值已由 patch parser trim，此处拦截的是直接构造
	// RotateCredentialsInput 的调用方。
	if !requestoverride.IsToken(text) {
		return "", &ValidationError{Message: label + "无效"}
	}
	return text, nil
}
