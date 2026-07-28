# Codex Reasoning 完整性检测源码审计报告（2026-07-28）

## 结论

OpenAI/Codex 对 reasoning 的公开实现定义了结构和生命周期，没有定义“隐藏思考在语义上完整”的可验证信号。juhe-ai 可以可靠识别协议不完整、传输不完整和结构非法；不能依据 summary 缺失、raw reasoning 不可见或一段文字像没说完就判定账户故障。

截图里的 `<thinking>[reasoning unavailable]</thinking>` 不存在于本次 Codex 开源源码的渲染路径。它更像客户端、网站或上游包装层的占位文本，必须结合原始 SSE 和最终 response 判断，不能单凭 UI 文案定责。

## 审计基线

- 源码：`F:\temp-project\agent\openai-codex-main`
- Git origin：`https://github.com/openai/codex.git`
- commit：`1bbdb32789e1f79932df44941236ea3658f6e965`
- commit 日期：2026-07-15
- juhe-ai：当前工作树，只读审计运行时代码，未修改生产逻辑。

官方开发文档站在本次环境中返回 HTTP 403；已配置 OpenAI developer docs MCP 供后续应用重启后使用，但当前会话无法加载新增 MCP 工具。因此本报告不补写无法核实的官网枚举，结论以 OpenAI 官方 Codex 公共仓库的实际类型、解析器和测试为准。

## Codex 事实

1. `codex-rs/protocol/src/models.rs` 的 reasoning item 要求 `summary` 数组，但数组可为空；`content`、`encrypted_content` 都不是可见性保证。
2. `codex-rs/core/src/client.rs` 只在模型支持且配置未关闭时请求 summary；请求包含 `reasoning.encrypted_content`。
3. `codex-rs/rollout-trace/src/model/conversation.rs` 明确允许 reasoning 只有加密内容，没有可读文本。
4. `codex-rs/codex-api/src/sse/responses.rs` 把 `response.incomplete` 和 `response.failed` 变成流错误；流在 `response.completed` 前关闭也报错。
5. `response.reasoning_summary_text.done` 只表示 summary 片段结束；item 完成依赖 `response.output_item.done`，整条响应成功依赖 `response.completed`。
6. Codex 开源 CLI/TUI 对空 reasoning 不渲染内容，源码未找到 `[reasoning unavailable]` 占位文字。

## juhe-ai 差距

| 位置 | 当前事实 | 风险 |
| --- | --- | --- |
| `gateway/protocols/openai-v1/stream-events.ts` | `response.incomplete` 是 terminal，但不在 failed 集合 | 生产流可能把不完整响应按正常终态结算 |
| `gateway/response/finalization.ts` | Responses 非流式对象只排除 `status=failed` | HTTP 200 的 `status=incomplete` 可能被当成功 |
| `gateway/codex-responses/stream-contract-state.ts` | 跟踪 added/delta/done 和 completed，但 completed 时未强制清空活动 identity | reasoning 已开始却未 done 可能漏过 |
| `model-checks-response-parsing.ts` | 探针已经把 incomplete/failed/error 当失败 | 模型检测方向正确，但缺少 reasoning 生命周期证据 |
| `model-checks.payloads.ts` | Responses 探针没有 reasoning summary/include 专项 | 无法区分 opaque、empty 和 item lifecycle 异常 |

## 可检测矩阵

| 现象 | 可信度 | 动作 |
| --- | --- | --- |
| `response.incomplete` / `response.failed` | 确定 | 失败；提交前可换号，提交后受控中断 |
| 非流式 `status=incomplete / failed` | 确定 | 失败；交付前可换号 |
| completed 前 EOF/超时 | 确定 | 失败 |
| reasoning added/delta 后没有 output_item.done | 确定协议异常 | 严格模式拦截 |
| 最终 output 与流中 identity 不一致 | 确定协议异常 | 严格模式拦截 |
| summary 缺失、content 缺失、encrypted-only | 合法 | 放行 |
| 完全空 reasoning item | 可疑 | warning；重复采样，不单次切号 |
| summary 半句、逻辑差、循环思考 | 启发式 | 模型检测评分，不进入热路径硬拦截 |
| UI 显示 reasoning unavailable | 信息不足 | 查原始 SSE、终态和来源层 |
| 请求历史缺少 reasoning replay 材料 | 来源相关 | 不补造；区分上游原始缺失与 bridge 丢字段 |

## 建议

优先修复流式 `response.incomplete`、非流式 `status=incomplete` 的生产失败语义和 completed 时未完成 identity 对账。这些证据确定、开销低、误判面小。请求历史在适配前后都应校验，但不能补造缺失的 replay 材料。reasoning 可见性探针应随后加入模型检测，并以三次稳定复现为能力健康证据；不要尝试由网关补造 reasoning，也不要因为加密或不可见就处罚账户。

长期规则见 `docs/functions/CodexReasoning完整性检测设计.md`。
