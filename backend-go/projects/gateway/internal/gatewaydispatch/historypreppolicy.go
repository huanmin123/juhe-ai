package gatewaydispatch

// 调度内核通用化（docs/functions/调度内核通用化设计.md §5.4，批次 3b）：
// 账户准备历史清理门参数化。本文件只承载策略形状与纯函数计算，不执行任何
// 清理动作；执行点（Front/Adapter/Post 三阶段）消费本策略，行为与改造前
// 逐点等价（SanitizeCodexHistory hook 生产恒 nil 的现状形态）。

// HistoryPrepStage 表达历史准备策略中单个阶段的取值。
type HistoryPrepStage string

const (
	// HistoryPrepStagePreserve：该阶段不处理历史，输入原样保留。
	HistoryPrepStagePreserve HistoryPrepStage = "preserve"
	// HistoryPrepStageSanitizeInline：driver 之前对请求上下文的内联清理
	// （仅 Front 阶段使用）。
	HistoryPrepStageSanitizeInline HistoryPrepStage = "sanitize_inline"
	// HistoryPrepStageSanitize：该阶段执行历史清理（Adapter/Post 阶段使用）。
	HistoryPrepStageSanitize HistoryPrepStage = "sanitize"
)

// HistoryPrepPolicy 表达一次账户准备的历史清理策略：三个执行阶段加去重
// 规则，不暴露具体供应商或适配器名称。
//
//   - Front：driver 之前对请求上下文（req.Body）的处理；
//   - Adapter：适配器构造出站 body 时自己的处理（chain_driver 消费）；
//   - Post：driver 返回 body 后的处理；
//   - SkipIfProcessed：使用现有序列化 body 已处理标记，命中时后续清理
//     阶段跳过重复清理（当前唯一消费点是 Post 阶段的标记检查）。
type HistoryPrepPolicy struct {
	Front           HistoryPrepStage // preserve | sanitize_inline
	Adapter         HistoryPrepStage // preserve | sanitize
	Post            HistoryPrepStage // preserve | sanitize
	SkipIfProcessed bool
}

// CodexOAuthVendorEligible 判定 openai 协议 OAuth 账户是否套用 GPT/Codex
// 归一化（GPT/Codex 归一化、Codex 头装配与 sanitize 历史清理只属于 gpt
// 供应商账户；xAI（Grok）OAuth 等已知非 gpt 厂商一律排除，走「原样透传」
// 口径，docs/functions/xAI账号接入.md）。
//
// 迁移期兼容语义：存量账户 vendor_code 回填迁移尚未正式执行（待办 B1），
// 空值代表「未知/未回填」，必须维持既有 Codex 归一化行为以免误伤存量
// OpenAI OAuth 账户；回填迁移完成后收口为只保留 "gpt"（空值随迁移清零，
// 不再表达合法形态）。
func CodexOAuthVendorEligible(vendorCode string) bool {
	return vendorCode == "gpt" || vendorCode == ""
}

// HistoryPrepPolicyForRequest 由账户协议形态与请求 compat/endpoint family
// 计算历史准备策略。纯函数：只读取入参，无副作用；compat/family 的画像
// 字符串比较只允许出现在这里（内核其余文件不得再出现）。
//
// 取值表（与改造前内核结构行为逐点等价，含 SanitizeCodexHistory hook 生产
// 恒 nil 的现状与 OAuth adapter 的无条件已处理标记写入）：
//
//  1. SDK→API Key（compat 非 codex_responses 或 family 非 responses，非
//     oauth 账户）：全 preserve，SkipIfProcessed=false；
//  2. Codex→API Key（compat=codex_responses + responses 族 + 非 oauth）：
//     Front=sanitize_inline，Adapter=preserve，Post=sanitize，
//     SkipIfProcessed=true；
//  3. Codex→OAuth（defer 条件命中，vendor gpt/空）：Front=preserve，
//     Adapter=sanitize，Post=preserve，SkipIfProcessed=true；
//  4. OAuth 能力门拒绝组合（compat 非空非 codex_responses + gpt 形态
//     oauth）：生产被 chain_driver OAuth 能力门淘汰、静态不可达，防御性
//     归入第 3 行同值。
//
// oauth 分支按 account.Type + CodexOAuthVendorEligible 收敛：gpt/空 vendor
// 的 oauth 账户走第 3/4 行（第 3 行 defer 判定的 vendor/profile 要素在
// 生产 OpenAI OAuth 账户上全部满足）；已知非 gpt 供应商的 oauth 账户
// （如 xAI Grok OAuth，本批起为真实生产形态）不再走 OAuth 特化行，落入
// 与非 oauth 账户一致的判定路径（第 2 行或第 1 行）。这些行在 SanitizeCodex
// History hook 生产恒 nil 下 Front/Post 均为 no-op，对原样透传 body 无实
// 际改写；hook 非-nil 仅存在于测试注入形态。
func HistoryPrepPolicyForRequest(account AccountCandidate, requestClientCompatibility, endpointFamily string) HistoryPrepPolicy {
	if account.Type == "oauth" && CodexOAuthVendorEligible(account.VendorCode) {
		// Codex→OAuth（defer 命中）及防御归并形态。适配器标记由 OAuth
		// adapter 在 Adapter=sanitize 下写入，Post 依 SkipIfProcessed 语义
		// 跳过，表达为 Post=preserve。
		return HistoryPrepPolicy{
			Front:           HistoryPrepStagePreserve,
			Adapter:         HistoryPrepStageSanitize,
			Post:            HistoryPrepStagePreserve,
			SkipIfProcessed: true,
		}
	}
	if requestClientCompatibility == "codex_responses" && endpointFamily == "responses" {
		// Codex→API Key：Front 内联清理请求上下文；Post 在未带已处理标记时
		// 清理序列化 body。
		return HistoryPrepPolicy{
			Front:           HistoryPrepStageSanitizeInline,
			Adapter:         HistoryPrepStagePreserve,
			Post:            HistoryPrepStageSanitize,
			SkipIfProcessed: true,
		}
	}
	// SDK→API Key（含 codex_responses compat 但非 responses 族的 chat 形态：
	// 改造前 Front/Post 守卫均未命中）。
	return HistoryPrepPolicy{
		Front:           HistoryPrepStagePreserve,
		Adapter:         HistoryPrepStagePreserve,
		Post:            HistoryPrepStagePreserve,
		SkipIfProcessed: false,
	}
}
