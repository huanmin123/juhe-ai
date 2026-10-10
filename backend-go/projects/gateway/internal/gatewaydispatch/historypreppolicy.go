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
//  3. Codex→OAuth（defer 条件命中）：Front=preserve，Adapter=sanitize，
//     Post=preserve，SkipIfProcessed=true；
//  4. OAuth 能力门拒绝组合（compat 非空非 codex_responses + oauth）：生产被
//     chain_driver OAuth 能力门淘汰、静态不可达，防御性归入第 3 行同值。
//
// oauth 分支按 account.Type 收敛（第 3 行 defer 判定的 vendor/profile 要素
// 在生产 OpenAI OAuth 账户上全部满足）；归并的两类防御形态与第 4 行同一
// 原则，且保持结构等价：
//   - oauth 非 GPT/openai-v1 画像形态（可构造、生产不存在）：改造前 Front
//     守卫命中但 hook 恒 nil 为 no-op、Post 被适配器已处理标记跳过；
//   - oauth + compat 非空非 codex_responses（能力门淘汰）。
//     该收敛同时保证 chain_driver isCodexOAuthAccount 分支内 Adapter 恒为
//     sanitize，适配器选项取值与改造前逐点一致（已处理标记写入不变）。
func HistoryPrepPolicyForRequest(account AccountCandidate, requestClientCompatibility, endpointFamily string) HistoryPrepPolicy {
	if account.Type == "oauth" {
		// Codex→OAuth（defer 命中）及上述两类防御归并形态。适配器标记由
		// OAuth adapter 在 Adapter=sanitize 下写入，Post 依 SkipIfProcessed
		// 语义跳过，表达为 Post=preserve。
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
