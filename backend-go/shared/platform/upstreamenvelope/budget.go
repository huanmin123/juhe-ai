package upstreamenvelope

import "fmt"

// DefaultProbeOutputTokens 是 F2 账户测试与 F4 健康/恢复探针的既有文本输出
// 预算（accountprobe outputTokenLimit = exactkeyprobe probeOutputTokenLimit
// = 1024）。设计文档 §4.3：本次事故修正目标即 Responses protocol_basic 使用
// 有界 1024（≥ 事故上游观测到的 100 下限）；统一的是字段生成与合法性校验
// 逻辑，不把所有探针请求统一为该值——M2 起按探针族声明各自预算，短采样/
// Token 统计等短预算语义不得被全局改成 1024（§4.4）。
const DefaultProbeOutputTokens = 1024

// BudgetPolicy 是命名的输出预算策略（§4.3 R2）：以
// endpointMode + probe family + adapter 解析，由信封按 catalog 条目的
// BudgetField 写入或明确省略。
type BudgetPolicy struct {
	name  string
	limit int
}

// BudgetBounded 有界预算：信封按 catalog 条目的预算字段写入 limit，
// limit 必须为正。
func BudgetBounded(limit int) BudgetPolicy {
	return BudgetPolicy{name: "bounded", limit: limit}
}

// BudgetNone 明确无预算字段（Codex 等不支持预算字段的适配器、契约无预算
// 字段的端点）：信封省略预算字段，不得伪造兜底（§4.3 Codex 例外）。
var BudgetNone = BudgetPolicy{name: "none"}

// Name 返回策略名（bounded / none）。
func (p BudgetPolicy) Name() string { return p.name }

// Limit 返回预算上限；none 策略恒为 0。
func (p BudgetPolicy) Limit() int { return p.limit }

// IsNone 报告策略是否为"明确省略预算字段"。
func (p BudgetPolicy) IsNone() bool { return p.name == BudgetNone.name }

// unspecified 报告调用方是否未选择策略（零值）。Build 对未选择的策略报错，
// 不静默兜底。
func (p BudgetPolicy) unspecified() bool { return p.name == "" }

// ResolveBudgetPolicy 按 endpointMode + probe family + adapter 解析命名预算
// 策略（§4.3）。M1 只落两类：
//   - Codex 适配器（F5）→ BudgetNone：Codex 归一化会移除其不接受的 tuning
//     字段，策略必须表达"无预算字段"，不能要求与普通 Responses 请求字面
//     相同；
//   - 其余 → BudgetBounded(DefaultProbeOutputTokens)（F2/F4 既有语义）。
//
// 契约无预算字段的端点（images/audio/interactions）恒返回 BudgetNone，
// 不套用文本输出预算（§1 端点表）。probeFamily 是解析键的组成部分；M1 不
// 按 family 细分，M2 起按 §4.4 盘点的探针族预算声明扩展。
func ResolveBudgetPolicy(mode EndpointMode, probeFamily string, adapter Adapter) (BudgetPolicy, error) {
	if adapter != AdapterNone {
		def, ok := adapters[adapter]
		if !ok {
			return BudgetPolicy{}, fmt.Errorf("upstreamenvelope: 适配器 %q 未在适配器登记表登记（设计文档 §4.2）", adapter)
		}
		if def.budget != nil {
			return *def.budget, nil
		}
	}
	entry, err := Lookup(mode)
	if err != nil {
		return BudgetPolicy{}, err
	}
	if entry.BudgetField == BudgetFieldNone {
		return BudgetNone, nil
	}
	return BudgetBounded(DefaultProbeOutputTokens), nil
}
