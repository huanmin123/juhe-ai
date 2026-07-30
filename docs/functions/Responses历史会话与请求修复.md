# Responses 历史会话与请求修复

## 目标与边界

Codex Responses 的历史 `input` 可能包含只属于原上游账户、旧会话或不可持久化请求的 item ID。网关在请求发送到目标上游前修复这些上下文引用，避免上游因 ID 前缀、账户边界或 `store=false` 状态拒绝请求。

本机制只处理请求侧上下文：不检查、修复、拦截或标注上游响应 SSE / JSON 的 item 字段，也不因上游 Responses 响应格式差异切换账户或写账户异常状态。上游响应按原字节转发；通用响应检查策略仍按其独立配置工作，不属于本机制。

## 请求处理顺序

1. 请求 preflight 恢复网关保存的 `previous_response_id` 上下文状态，并判断候选账户能否承接该状态。
2. 账户派发前，`prepareCodexResponsesContextForAccount` 根据目标账户准备上下文。
3. `sanitizeCodexResponseHistoryItems` 清理不能转发到目标账户的历史 item `id`。
4. 账户适配和 OAuth normalizer 完成后，`sanitizePreparedCodexResponsesHistoryForAccount` 对最终 outbound JSON 再做一次兜底清理，防止后续适配器重新写入不可信 ID。

这两次清理复用结构化请求体；仅在适配器产生未绑定字符串或 Buffer 时才重新解析 JSON。

## 清理规则

- item 类型和 ID 前缀不匹配、ID 无效、目标账户不持久化上游状态、或历史跨账户 / 跨作用域时，删除 item `id`，保留 item 顺序、内容、工具名、参数和 `call_id`。
- 只含不可重放 ID、缺少必要语义字段的历史 item 以 `codex_history_item_unrecoverable` 在本地失败，避免静默丢失上下文。
- 当前 item contract registry 仅供请求历史识别合法 item 类型和 ID 前缀，不承担响应协议校验。
- `previous_response_id` 的恢复、桥接状态保存和 compaction contract 属于上下文状态机制，保持独立。

## 配置与可观测性

请求侧修复是 Responses 内部固定机制，没有账户级开关、前端表单项或 `JUHE_AI_CODEX_PROTOCOL_GUARD_MODE` 环境变量。历史数据库中遗留的旧响应 guard 凭据字段不再读取、投影或写回；不需要数据迁移。

审计和使用记录不再写 `codexResponsesGuard`、`late_violation`、响应修复规则或响应协议诊断。请求上下文无法恢复或历史无法重放时，继续使用既有本地请求错误和审计链路。

## 验证

```powershell
pnpm --filter juhe-ai-backend test:codex-responses-tool-item-identity
pnpm --filter juhe-ai-backend test:codex-responses-history-sanitizer
pnpm --filter juhe-ai-backend test:codex-responses-bridge-native-switch
pnpm --filter juhe-ai-backend test:codex-responses-response-passthrough
```

前三项验证请求送往上游前的 ID 生成、历史清理和 bridge/native 切换边界；最后一项验证含不一致 item 字段的上游 SSE 保持原样透传，不触发响应质量拦截、账户失败或切号。
