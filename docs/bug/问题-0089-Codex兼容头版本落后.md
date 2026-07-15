# BUG-0089 Codex 兼容头版本落后

- 状态：已修复并完成生产验证
- 严重程度：P1
- 模块：网关 / Codex Responses / OpenAI OAuth
- 发现日期：2026-07-15

## 现象

调用 `gpt-5.6-sol` 时，上游返回 `The 'gpt-5.6-sol' model requires a newer version of Codex`。服务仍模拟 Codex `0.125.0`，并发送旧 `version`、`OpenAI-Beta: responses=experimental`、`session_id` 和 `conversation_id`。

## 根因

Codex 兼容头在多个适配器中重复硬编码，版本升级时没有统一更新。最新本地 Codex 源码运行时为 `0.144.4`，HTTP Responses 已使用 `session-id` / `thread-id`，GPT-5.6 Sol、Terra、Luna 请求还需要 Responses Lite 标志。

## 修复

- 统一由共享 helper 输出 `codex_cli_rs/0.144.4`，删除旧 `version` 和 HTTP `responses=experimental`。
- OAuth 会话头改为 `session-id` / `thread-id`；GPT-5.6 三个模型请求增加 Responses Lite 标志。
- OAuth authorize、授权码交换和 refresh 请求形态对齐当前源码。
- `/models` 不按 `client_version` 过滤，不增加版本拦截；模型可见性继续只由当前模型目录管理。

## 验证

- `pnpm --dir backend run test:codex-latest-compatibility`
- `pnpm --dir backend run test:openai-oauth-protocol-contract`
- 后端 typecheck 与 build。
- 生产发布后用测试 OAuth 账户执行 Sol SSE 首事件验证。
- 2026-07-16 修正 release 上线后，生产最近 5 分钟有 131 条 `gpt-5.6-sol` 流式请求成功，HTTP 200，131 条均记录首包时间，证明真实 Sol SSE 链路已工作。

## 防复发

- Codex 版本和请求头只能在共享 helper 维护，适配器不得重复硬编码。
- 升级前以本地当前源码、模型清单和实际运行时 pin 为事实来源，不能只看旧稳定 tag。
- 发现额外优化先记录结论和风险；没有用户确认时，不把 `/models` 过滤或其他策略性限制顺带加入故障修复。
