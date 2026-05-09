# OpenAI API Key 透传细节统计与比较

> 创建时间：2026-05-08
> 关联文档：[中转透传机制调研与定位修正](中转透传机制调研与定位修正.md)、[OpenAI OAuth 透传细节统计与比较](OpenAI%20OAuth透传细节统计与比较.md)、[OpenAI 账号接入](OpenAI账号接入.md)

本文用于复查 `juhe-ai` 的 OpenAI API Key 账号透传链路，并对比 `F:\temp-project\中转` 下主流中转项目的请求头、请求体和账号边界处理。结论只用于协议正确性、账号隔离、异常噪声收敛和可观测性，不用于规避平台限制、绕过风控或伪造身份。

## 一句话结论

他们的成熟点主要在 Header 边界：`one-api` 默认只转少数通用头，`new-api` 即使支持 header passthrough 也会跳过危险头，Portkey 把 OpenAI 组织 / 项目 / Beta 放在 provider options，LiteLLM 对 OpenAI org 转发有显式开关并会过滤 SDK 噪声。

我们的优势在 body：`/v1` 入口用 `express.raw` 捕获原始字节，API Key 账号透传开启时优先把 `req.rawBody` 原样送上游，不解析再重组。这比很多会读取 body 后再序列化的传统 relay 更接近真正的 API Key 公开协议透传。

最终取舍：API Key 透传不是“所有入站头都裸转”，而是“body 真透传 + Header 最小清洗 + 认证替换 + 不暴露账号上下文字段”。这样既保留客户端请求语义，又避免把本地网关、代理链路、客户端 SDK 内部追踪和错误组织 / 项目信息带给上游。

## 本次复查范围

### 本项目

| 范围 | 关键文件 | 关注点 |
| --- | --- | --- |
| `/v1` 原始 body | `backend/src/server.ts` | `express.raw({ type: () => true, limit: '64mb' })` 捕获 `rawBody`，JSON 请求再解析给本地调度使用 |
| API Key 上游 body | `backend/src/modules/gateway/openai-gateway-upstream.ts` | 透传开启时优先返回 `req.rawBody`；关闭时才 `JSON.stringify(req.body)` |
| API Key 上游 header | `backend/src/modules/gateway/openai-gateway-upstream.ts` | 复制客户端语义头，过滤危险头、代理链路头、SDK / tracing 噪声，替换认证，不从账号凭据写入 OpenAI 组织 / 项目 / Beta |
| API Key 表单 | `frontend/src/views/accounts/AccountApiKeySection.vue` | 只配置 API Key 和 Base URL，不暴露 OpenAI 组织、项目和 Beta 字段 |
| 回归验证 | `backend/src/scripts/openai-api-key-passthrough-regression.ts` | 覆盖 raw body、Header 过滤、残留账号凭据不生效和非透传 JSON fallback |

### 本地参考项目

| 项目 | 关键文件 | 对 API Key 透传的启发 |
| --- | --- | --- |
| `one-api` | `relay/adaptor/common.go`、`relay/adaptor/openai/adaptor.go`、`relay/adaptor/proxy/adaptor.go` | 常规 OpenAI adaptor 只设置 `Content-Type` / `Accept` 并重写认证；proxy adaptor 才复制更多头，但仍删除 `Host`、`Content-Length`、`Accept-Encoding`、`Connection` |
| `new-api` | `relay/channel/api_request.go`、`relay/channel/openai/adaptor.go` | 支持 header override / passthrough 规则，但 wildcard / regex 透传会跳过 hop-by-hop、cookie、认证、长度、压缩和 WebSocket 握手头 |
| `portkey-gateway` | `src/providers/openai/api.ts`、`src/handlers/handlerUtils.ts` | OpenAI `Authorization`、`OpenAI-Organization`、`OpenAI-Project`、`OpenAI-Beta` 来自 provider options；额外客户端头必须显式 `forwardHeaders` |
| `litellm` | `litellm/proxy/litellm_pre_call_utils.py`、`litellm/llms/openai/common_utils.py` | 企业网关默认不做全量裸转；OpenAI org 之类跨账号字段要通过配置控制，且会过滤 `x-stainless-*` 等 SDK 噪声 |
| `sub2api_source` | OpenAI 透传链路 | 更接近账号池中转：保留客户端语义头，但剔除本地认证、代理链路和内部头 |
| `proxify` / `relayplane-proxy` | proxy / streaming 入口 | 可参考“body 原样流转”和简单代理模型，但缺少账号调度、多租户隔离和使用统计边界 |

### 官方边界

OpenAI OpenAPI spec 当前公开 base URL 是 `https://api.openai.com/v1`；`POST /responses` 示例使用 `Content-Type: application/json` 和 `Authorization: Bearer $OPENAI_API_KEY`。`OpenAI-Organization`、`OpenAI-Project`、`OpenAI-Beta` 属于公开 API 可用的附加头，但在账号池中转里不应默认相信任意下游客户端传入的组织 / 项目头，也不应要求普通账号维护者猜测这些值。

## 统计摘要

| 维度 | 统计结论 |
| --- | --- |
| 本项目是否保留 raw body | 是，API Key 透传开启时优先原样使用 `req.rawBody` |
| 参考项目是否普遍全量复制所有 Header | 否。成熟项目要么只设置少数通用头，要么支持显式透传规则但跳过危险头 |
| 认证头如何处理 | 全部参考实现都会重写上游认证，不把本地 API Key 直接传给上游 |
| 组织 / 项目头如何处理 | Portkey 按 provider options 写入；LiteLLM 有显式配置；不建议把客户端 `OpenAI-Organization` / `OpenAI-Project` 默认透给账号池 |
| SDK / 平台噪声如何处理 | LiteLLM 明确处理 `x-stainless`；企业网关通常不让 `x-litellm` / `x-portkey` / 部署平台 tracing 头直接泄露 |
| `Idempotency-Key` 是否应保留 | 当前保留。它是公开 API 的幂等语义，但如果未来实现跨账号自动重试，需要单独评估同一 idempotency key 打到不同上游账号的语义 |

## 请求体策略比较

| 项目 | API Key 请求体做法 | 风险 / 取舍 |
| --- | --- | --- |
| `juhe-ai` | 透传开启时优先原始 `rawBody`；关闭时才重新序列化 JSON | body 层最接近真实透传；需要通过旁路解析服务本地统计和调度 |
| `one-api` | 传统 relay 通常读取 OpenAI 请求对象并转发或转换 | 易做模型映射和计费，但不是严格原始字节透传 |
| `new-api` | 按 channel / relay mode 读取、转换或转发 | 能兼容多厂商，但 body 会经过更多中间处理 |
| `Portkey` | 网关 body 会进入 provider / hooks / cache / retry 流程 | 企业能力强，但不是轻量 raw passthrough |
| `LiteLLM` | 代理层注入 metadata、团队、路由、预算等上下文 | 适合企业治理，不适合把 API Key 账号定义成低干预透传 |

结论：API Key 这条链路上，我们的 body 处理比很多参考项目更好，应当保留。不要为了加 header 规则而破坏 `rawBody`。

## 请求头策略比较

| 项目 | 默认 Header 策略 | 对我们的启发 |
| --- | --- | --- |
| `juhe-ai` 旧实现 | 黑名单跳过危险头后复制其余入站头，替换 `authorization` | body 很好，但 Header 容易把客户端组织 / 项目、SDK 内部头、tracing 头和部署平台头带给上游 |
| `juhe-ai` 当前建议 | 过滤危险头、代理链路、本地认证、`OpenAI-Organization` / `OpenAI-Project` 和常见噪声前缀；保留客户端 API 语义头；不提供账号级组织 / 项目 / Beta 输入项 | 保留真实请求语义，同时避免把不可解释字段交给用户 |
| `one-api` 常规 OpenAI adaptor | 只设置 `Content-Type`、`Accept`，再写 `Authorization` | 稳，但对客户端语义透传较少 |
| `one-api` proxy adaptor | 复制更多客户端头，但删除 `Host`、`Content-Length`、`Accept-Encoding`、`Connection` | 即使 proxy 模式也不是无条件裸转 |
| `new-api` | Header override 支持 `*` / regex passthrough，但统一跳过不安全头 | 适合借鉴“显式透传也要有不可绕过的安全跳过集” |
| `Portkey` | provider headers 来自 provider options；客户端额外头来自 `forwardHeaders` | 组织、项目、Beta 在企业网关里通常属于管理员配置；本项目当前不把这些高级项放进普通账户表单 |
| `LiteLLM` | 通过代理配置决定 OpenAI org 是否转发，并过滤 SDK 噪声 | 对账号池中转来说，默认不信客户端跨账号头更稳 |

## API Key Header 决策

### 必须替换

- `Authorization`
- `x-api-key`
- `x-goog-api-key`
- `api-key`

这些都是本地调用方或其他供应商认证语义，不能进入 OpenAI API Key 上游请求。

### 必须过滤

- HTTP hop-by-hop：`connection`、`keep-alive`、`proxy-authenticate`、`proxy-authorization`、`te`、`trailer`、`transfer-encoding`、`upgrade`
- 由 Node HTTP 客户端重算或不适合转发：`host`、`content-length`、`expect`
- 压缩与解析风险：`accept-encoding`、`content-encoding`
- 本地会话 / 管理态：`cookie`、`set-cookie`
- 代理链路：`x-forwarded-*`、`x-real-ip`、`forwarded`、`via`、`cf-connecting-ip`
- tracing / 部署平台：`traceparent`、`tracestate`、`baggage`、`x-amzn-trace-id`、`x-cloud-trace-context`、`x-request-id`、`x-vercel-*`
- SDK / 内部实现噪声：`x-stainless-*`、`x-openai-*`
- Codex / ChatGPT OAuth 专用：`chatgpt-account-id`

### 默认不信客户端的 OpenAI 账号头

- `OpenAI-Organization`
- `OpenAI-Project`

原因：这两个头决定上游账号归属上下文。账号池中转里，下游客户端只拥有本地 API Key，不应该能用自己的请求头影响被调度上游账号所属组织或项目。服务端也不凭空生成或从账号凭据写入这两个头，避免把用户不知道来源的字段变成错误配置。

### 可保留

- `OpenAI-Beta`

原因：`OpenAI-Beta` 更多是公开 API 功能语义，例如启用某些 beta API。客户端确实知道自己要请求的 beta 能力时可以显式传入；服务端不生成默认 Beta，也不从账号凭据覆盖。

### 当前保留

- `Idempotency-Key`
- `Content-Type`
- `Accept`
- `Accept-Language`
- `User-Agent`
- 普通业务自定义头，例如 `x-custom-header`

`Idempotency-Key` 当前保留是为了兼容公开 API 幂等语义。后续如果网关实现 API Key 账号之间的自动重试或切换，需要单独评估：同一个幂等键在不同上游账号之间通常不具备共享语义。

## API Key 账号配置边界

OpenAI API Key 账号凭据只保留当前必要字段：

| 字段 | 上游 Header | 默认值 | 用途 |
| --- | --- | --- | --- |
| `api_key` | `Authorization` | 必填 | 被调度上游账号的 API Key |
| `base_url` | 请求 URL | `https://api.openai.com/v1` | OpenAI compatible 上游地址 |

不新增 `openai_organization`、`openai_project`、`openai_beta` 这类普通表单字段。组织 / 项目 ID 不能由服务端生产；用户不知道来源时也不应被要求填写。即使历史数据里残留这些键，API Key 上游 Header 构造也不会读取它们。

## 他们的好还是我们的好

| 维度 | 他们更好 | 我们更好 | 结论 |
| --- | --- | --- | --- |
| Header 边界 | `one-api`、`new-api`、Portkey、LiteLLM 都比旧实现更克制 | 这次补齐后已接近成熟项目边界 | Header 之前是短板，现在按账号池语义收敛 |
| Body 透传 | 多数成熟网关会读取和转换 body | 我们的 API Key raw body 更接近真透传 | 保留，不要回退成重序列化 |
| 账号池隔离 | Portkey / LiteLLM 的企业治理更成熟 | 我们轻量、直接，适合本地账号调度 | 当前版本不引入重型策略，但关键边界要有 |
| 前端复杂度 | 企业网关配置更多 | 我们只保留 API Key 和 Base URL，不暴露用户难以判断的 OpenAI 账号上下文字段 | 保持轻量，把复杂边界留在后端策略里 |
| OAuth / API Key 分层 | `new-api`、`sub2api_source` 对 Codex / OAuth 分层更明确 | 已通过 PLAN-0012 拆出 OAuth Codex adapter | API Key 和 OAuth 必须继续分开看 |

总体评价：API Key body 透传我们更好；旧 Header 策略他们更稳。当前应把两者合起来：保持我们的 raw body 优势，吸收他们的 Header 边界。

## 本次已落地事项

- API Key 上游 Header 过滤补充 `OpenAI-Organization`、`OpenAI-Project`、tracing、SDK 和部署平台噪声。
- `OpenAI-Beta` 保留客户端显式语义，但账号凭据不能覆盖。
- API Key 账号表单撤回 `OpenAI 组织`、`OpenAI 项目`、`OpenAI Beta` 三个可选字段，只保留用户明确知道的 API Key 和 Base URL。
- API Key 回归脚本覆盖 raw body 字节级保留、危险头过滤、残留账号凭据不生效、`Idempotency-Key` 保留和非透传 JSON fallback。
- 文档明确 API Key 透传边界，避免把 OAuth Codex adapter 的策略误套到公开 API Key 链路。

## 后续打磨建议

| 优先级 | 建议 | 触发条件 |
| --- | --- | --- |
| P1 | 原始审计日志增加 `header_policy` / `filtered_header_names` 摘要 | 如果后续继续排查“透传不对劲”，需要快速定位被过滤的头 |
| P1 | API Key 自动重试前评估 `Idempotency-Key` | 如果实现跨账号请求级重试，必须避免幂等语义误导 |
| P2 | 增加只读 Header 策略摘要 | 如果后续继续排查“透传不对劲”，可在测试结果里展示脱敏后的保留 / 过滤摘要 |
| P2 | multipart / upload 场景回归 | 如果后续重点支持文件上传、图片编辑等 multipart 接口，需要补 raw body + boundary 验证 |
| P2 | 按 provider 拆 Header policy 单元 | 后续新增 Anthropic、Gemini 等供应商时，不能复用 OpenAI 的 Header 规则 |

## 验证建议

| 测试类型 | 用例 | 预期 |
| --- | --- | --- |
| 单元 / 脚本 | API Key 请求带复杂 JSON raw body | 上游 body 与 `rawBody` 字节完全一致 |
| 单元 / 脚本 | 客户端带本地认证、cookie、代理链路、tracing、SDK 噪声头 | 上游 Header 不包含这些字段 |
| 单元 / 脚本 | 客户端带 `OpenAI-Organization` / `OpenAI-Project`，历史账号凭据也残留对应字段 | 上游不包含组织 / 项目头 |
| 单元 / 脚本 | 客户端带 `OpenAI-Beta`，账号未配置 | 上游保留客户端值 |
| 单元 / 脚本 | 客户端带 `OpenAI-Beta`，历史账号凭据残留 `openai_beta` | 上游仍使用客户端值，不被账号凭据覆盖 |
| 单元 / 脚本 | 客户端带 `Idempotency-Key` | 上游保留 |
| 回归 | OAuth 账号请求 | 仍走 `openai_oauth_codex` adapter，不受 API Key Header 策略影响 |
| 类型检查 | 前后端类型 | TypeScript 通过 |

## 结论

API Key 透传的重心不是把所有 Header 都发给上游，而是让上游看到“公开 OpenAI API 真实需要的请求语义”。当前最优路线是：

1. 请求体继续 raw passthrough。
2. 上游认证永远由被调度账号替换。
3. 危险头、代理链路、本地认证、SDK / tracing / 部署平台噪声默认过滤。
4. `OpenAI-Organization` / `OpenAI-Project` 不从客户端透传，也不从账号凭据生成。
5. `OpenAI-Beta` 保留客户端功能语义，不做账号级覆盖。

这套策略比旧实现更接近主流中转的稳态，也保留了 `juhe-ai` 当前最有价值的 raw body 真透传优势。
