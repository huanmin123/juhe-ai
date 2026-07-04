# BUG-0032 compact 摘要子请求被误改写为流式

## 状态

- 状态：已修复
- 严重程度：P1
- 模块：后端 / 网关 / Codex Responses / DeepSeek bridge / 模型映射
- 发现日期：2026-07-04
- 修复日期：2026-07-04

## 现象

`pnpm --filter juhe-ai-backend test:deepseek-gateway-mock-ai` 失败在 `DeepSeek Codex bridge /responses/compact 应返回网关摘要`。

网关返回 502 `codex_bridge_compact_summary_empty`，表示 `/responses/compact` 内部摘要请求没有抽取到 Chat Completions JSON 摘要。

## 根因

`/responses/compact` 会构造一个 synthetic Chat Completions 请求去上游生成摘要，body 明确设置为 `stream: false`。

但 synthetic request 继承了原始 Codex Responses 请求上的模型映射端点族，并且代码又把 synthetic request 的 `gatewayModelMappingSourceEndpointFamilyOverride` 设置成 `responses`。DeepSeek driver 因此继续把它当作 Responses -> Chat bridge 请求处理，调用 `buildCodexResponsesChatBridgeBody()` 后强制 `stream: true`。

上游 mock 看到 `stream: true` 后返回 SSE chunk；`extractChatCompletionSummary()` 只支持 Chat Completions JSON 的 `choices[].message.content`，因此摘要为空并返回 502。

## 修复

- `backend/src/modules/gateway/codex-responses/compact-preflight.ts`
  - synthetic summary request 的模型映射端点族改为 `chat_completions`。
  - 保持 summary 子请求为普通 Chat Completions JSON，不继承外层 Responses bridge 流式改写。

## 验证

- `pnpm --filter juhe-ai-backend test:deepseek-gateway-mock-ai`：通过
- `pnpm --filter juhe-ai-backend typecheck`：通过

## 关联

- 关联测试：`backend/src/scripts/regression/deepseek-gateway-mock-ai-regression.ts`
- 与 `BUG-0030` 无直接根因关联；该问题不是 SSE 心跳-only 保护引起。
