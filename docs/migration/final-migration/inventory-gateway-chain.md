# AI 网关链分解清单（2026-09-04 子代理盘点）

来源：`backend/src/modules/gateway/`（80,334 行非测试 TS）。用途：G01-G20 工作包的范围、依赖与风险依据。

## 1. 目录行数

routes.ts 3,329；request 8,200；response 8,266；runtime 27,038；dispatch 5,288；protocols 根 267 + _shared 212 + openai-v1 3,872 + anthropic-v1 1,334 + gemini-v1beta 1,525；upstream 2,822；usage 2,710；hybrid 2,659；quota 2,165；routing 1,960；client-profiles 1,780；codex-responses 1,767；adapters/gpt-codex 1,209；audit 1,094；policy 940；observability 797；session-identity 500；diagnostics 283；testing 317。

大文件 TOP：runtime/account-side-effects.service.ts 2,945；dispatch/upstream-dispatch.ts 2,771；response/finalization.ts 2,306；response/stream.ts 2,002；request/preflight.ts 1,926；runtime/account-circuit-redis-store.ts 1,809；runtime/runtime-cache.service.ts 1,694；runtime/session-affinity.service.ts 1,453；runtime/normal-route-latency-degradation.service.ts 1,295。

## 2. 请求管线顺序（/v1/* 进入 → 结束）

中间件层（server.ts 488-499）：协议识别拒绝 → preResolveGatewayRuntime（IP 风控/client-ip 错误熔断/用户请求限数）→ DB 不可用兜底 → speed-first 准入 → raw body parse/capture。

主 handler：routes.ts `handleOpenAIGatewayRequest`（单函数 ~2,300 行）内有序：
1. 快照/审计初始化（clientIp、requestLane、trafficSource、usage snapshots、audit capture）
2. 中断边界（abort-attribution、downstream-commit-state、res close → failure-finalization）
3. preflight `prepareOpenAIGatewayDispatchContext`：models 免鉴权快路 → API Key 鉴权（gateway-api-key.repository + runtime-cache）→ 重试预算器（route-coordination）→ client-ip 熔断 → Gemini interaction 亲和 → client-profile 解析 → session 身份 → 路由策略（normal / hybrid_smart）→ 配额（api-key/authorization/in-flight/snapshot）→ 候选账户 + session 亲和 → codex 桥预检
4. 路由 fallback 循环
5. 派发 `fetchFirstAvailableUpstream`：候选过滤（capability/model/capacity）→ 账户准备/凭据（API key 选择、codex OAuth 适配）→ 并发槽 → 熔断/key-model/hot-quality 尝试
6. 协议执行 `requestUpstream`（首字节 deadline、代理健康、速度优先切流）
7. 流式 `handleStreamUpstreamResponse` → `pipeUpstreamStream`（协议 stream-inspection、SSE 解析、pre-commit buffer、heartbeat）
8. 非流式 `handleNonStreamUpstreamResponse` + JSON inspection
9. 终态：finalize → routing effects → usage 四类记录（record-queue：内存队列 + Redis Stream + spool）
10. 账户副作用/熔断回写（side-effect queue、锁、circuit 双存储）
11. audit 收尾（attempt 绑定、finalize、转发 Go F3）
12. 错误处理（failure-dispatch、error-policy、exhaustion 分类、SSE retry 事件）

## 3. 供应商/协议组合

统一管线 + 三维组合（协议驱动 × 上游适配 × 客户端画像）：

| 组合 | 协议入口 | 上游适配 | 特有文件 |
| --- | --- | --- | --- |
| openai（chat_completions/responses，text/image lane） | protocols/openai-v1/driver | openai_api_key | model-mapping、response-inspection-buffer |
| codex（OAuth Responses） | 同上 + codex-responses 桥 | openai_oauth_codex | adapters/gpt-codex/oauth-adapter、codex-responses/chat-bridge-state(1,143)、compact-preflight、client-profiles/codex-turn-retry(633) |
| anthropic（/v1/messages） | protocols/anthropic-v1/driver | anthropic_api_key（上游仍 openai 账户，格式桥接） | stream-inspection(357)、response-semantics、client-compatibility |
| gemini（streamGenerateContent/interactions） | protocols/gemini-v1beta/driver | gemini_api_key | interaction-affinity(379，唯一带 Redis 状态的协议文件) |
| grok/deepseek 等 | 经 openai-v1 驱动 + providerCode/protocol profile | openai_api_key | 无独立目录 |

SSE：response/stream.ts `pipeUpstreamStream`(2,002) + protocols/*/stream-inspection + openai-v1/stream-events + stream-pre-commit-buffer。取消/aborted：request/abort-attribution、response/client-abort、routes.ts 三处 listener → usage/records `recordDownstreamClosedUpstreamAttempt` + usage/failure-finalization。

## 4. 工作包边界（G01-G20，正文见 work-packages.md）

依赖核心提示：
- routes.ts 主 handler、request/preflight.ts、dispatch/upstream-dispatch.ts、response/finalization.ts 之间经 `OpenAIGatewayDispatchContext`/`RouteAction`/`UpstreamAttemptError` 强耦合——G05/G15/G16 必须先冻结 Go 侧接口契约（建议由同一实现 Agent 或主 Agent 先定 interface，再并行填实现）。
- runtime 的约 20 个 `gateway:*` Redis key 语义须在 G10-G13 动手前固化为 key 清单文档（见 inventory-shared-kernel.md §4）。
- json body 解析为 worker 线程实现（request/json-worker），Go 化为进程内有界解析，语义对照 golden。
- testing/ 的 memory-gateway-http 假件可作为 Go 侧对照测试的黄金样本来源之一。
