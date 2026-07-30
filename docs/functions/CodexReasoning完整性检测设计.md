# Codex Reasoning 完整性检测设计

> 状态：不在网关运行时实施。2026-07-30 已移除 Codex Responses 上游响应质量 guard，本文只保留诊断结论和重新评估边界。

## 已确认事实

Codex Responses 的 `reasoning` 可以没有可读 summary、没有 raw content，或仅有 `encrypted_content`。这些状态不能证明模型、账户或响应异常。`response.output_item.done` 和 `response.completed` 描述协议事件生命周期，但上游 SSE / JSON 的字段差异也不能作为自动切号或账户处罚依据。

请求历史中的 reasoning item 仍由请求侧上下文修复处理：目标账户不能识别的历史 ID 在发送上游前清理；没有可重放语义内容的历史 item 以 `codex_history_item_unrecoverable` 本地失败。详细规则见 [Responses 历史会话与请求修复](Responses历史会话与请求修复.md)。

## 当前运行时边界

- 不构造 reasoning / Responses 响应状态机，不检查上游 SSE 或 JSON item 字段。
- 不修复、改写或拦截上游响应，不产生 `late_violation`，不因此切号或更新账户状态。
- 不从 `<thinking>`、`reasoning unavailable`、文本长度、内容完整度或逻辑质量推断协议失败。
- 通用响应检查策略与本设计独立；只有管理员明确配置的通用策略才可产生其既有的处理结果。

## 重新评估条件

只有获得上游提供的稳定可验证完成信号，且能够证明其误判率、账户副作用和客户端兼容性可接受时，才评估新增诊断功能。任何候选方案必须默认只观察，不能改变上游响应字节、路由选择或账户运行态。
