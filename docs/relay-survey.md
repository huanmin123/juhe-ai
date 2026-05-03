# 中转透传机制调研与推荐方案

调研日期：2026-05-04。

本文用于沉淀 `sub2api_source` 与多个开源 AI 中转 / 网关项目的源码对照结论，并给出 `juhe-ai` 后续中转透传能力的推荐技术路线。

## 一句话结论

- `sub2api_source` 不是无约束 raw proxy，而是“稳定中转 + 兼容型透传”：白名单头转发、上游认证替换、会话隔离、OAuth / Codex 兼容补齐、body 小范围修正、流式处理与失败切换都在服务端完成。
- 主流网关项目也很少默认“全量头 + 全量 body + 全量响应头”原样转发；更常见的是安全白名单、显式 `forwardHeaders`、provider adaptor、响应兼容和可观测头。
- 真正接近 raw reverse proxy 的代表是 `proxify`，但它基本不做账号调度、会话隔离、错误策略、用量治理和多租户安全；不能直接照搬到 `juhe-ai` 的账号中转模型里。
- 客户端真实 IP 不能靠 HTTP 头“变成”上游看到的 TCP 源 IP。`X-Forwarded-For` 只是一个可伪造请求头，默认转发或伪造反而容易制造代理指纹；如果业务合规要求上游识别客户身份，应使用明确的客户端标识、独立上游账号或合规的出站 IP 绑定策略。

## 已下载源码

| 项目 | 本地目录 | 远端 | 参考价值 |
| --- | --- | --- | --- |
| `sub2api_source` | `F:\sub2api_source` | 本地稳定版本 | OpenAI OAuth / Codex 兼容型透传基线 |
| `one-api` | `F:\temp-project\one-api` | [songquanpeng/one-api](https://github.com/songquanpeng/one-api) | 轻量 provider adaptor、最小请求头转发 |
| `new-api` | `F:\temp-project\new-api` | [QuantumNous/new-api](https://github.com/QuantumNous/new-api) | Header override / passthrough 规则、流式保活、参数兼容 |
| `LiteLLM` | `F:\temp-project\litellm` | [BerriAI/litellm](https://github.com/BerriAI/litellm) | 大型路由、预算、guardrail、显式头清洗 |
| `Portkey Gateway` | `F:\temp-project\portkey-gateway` | [Portkey-AI/gateway](https://github.com/Portkey-AI/gateway) | `forwardHeaders`、proxy 端点、重试 / fallback / hooks |
| `Proxify` | `F:\temp-project\proxify` | [poixeai/proxify](https://github.com/poixeai/proxify) | 接近原样反代、头 / body 复制、流式直通 |
| `RelayPlane Proxy` | `F:\temp-project\relayplane-proxy` | [RelayPlane/proxy](https://github.com/RelayPlane/proxy) | 本地成本代理、会话 / 代理指标、部分 OAuth header passthrough |
| `Helicone AI Gateway` | `F:\temp-project\helicone-ai-gateway` | [Helicone/ai-gateway](https://github.com/Helicone/ai-gateway) | direct provider proxy、统一路由、可观测与重试 |
| `agentgateway` | `F:\temp-project\agentgateway` | [agentgateway/agentgateway](https://github.com/agentgateway/agentgateway) | 策略、header/body transformation、RBAC、xDS 架构 |
| `Envoy AI Gateway` | `F:\temp-project\envoy-ai-gateway` | [envoyproxy/ai-gateway](https://github.com/envoyproxy/ai-gateway) | 企业级网关边界、quota-aware routing、ExtProc |

## `sub2api_source` 当前做法

关键源码：`F:\sub2api_source\backend\internal\service\openai_gateway_service.go`。

### 请求头策略

- 非透传与透传都有独立白名单，透传白名单只放行 `accept`、`accept-language`、`content-type`、`conversation_id`、`openai-beta`、`user-agent`、`originator`、`session_id`、`x-codex-turn-state`、`x-codex-turn-metadata`。
- `X-Forwarded-For`、`X-Real-IP` 只出现在诊断日志白名单，不参与上游透传。
- 构造上游请求时会删除入站 `authorization`、`x-api-key`、`x-goog-api-key`，再写入选中账号的上游 token。
- OAuth 账号会补齐 `chatgpt-account-id`、`OpenAI-Beta`、`originator`、`accept`、`version` 等兼容头。
- OAuth 账号会对 `session_id` / `conversation_id` 做基于本地 API Key 的隔离，避免不同用户共用相同会话标识导致串会话或命中对方缓存。
- 账号可配置自定义 `User-Agent`，也支持强制 Codex CLI UA 兜底。

### Body 策略

- 透传分支仍会轻量读取模型、stream、reasoning effort 等字段。
- OAuth 透传会做 body normalize，`responses compact` 可能映射 model。
- 会清理空 base64 图片输入、执行 OpenAI fast policy、在特定请求上拒绝不满足上游要求的 body。
- 非透传分支更明显：会补默认 `instructions`、注入 / 规范化部分工具字段、做模型与参数兼容。

### 响应与错误策略

- 流式响应会按 SSE / 首 token 时间 / usage 做处理。
- 透传分支遇到 `429` / `529` 这类容量错误会尝试切换账号。
- 上游错误会被读取、清洗、记录，并纳入账号错误策略与限流快照。

### 这样设计的目标

- 保持上游协议兼容，尤其是 OpenAI OAuth / Codex 这类内部接口对 `User-Agent`、`originator`、`session_id`、`OpenAI-Beta` 的要求。
- 避免把客户端环境噪声、浏览器 / 代理链路头、网关内部鉴权头直接暴露给上游。
- 避免跨用户会话碰撞，保证同一个本地网关被多人使用时不会串会话、串 prompt cache。
- 保证流式请求稳定、错误可切换、用量可追踪，而不是只做一次盲目 TCP 转发。

## 开源项目对照

| 项目 | 请求头处理 | Body / 协议处理 | 流式 / 重试 / 路由 | 对 `juhe-ai` 的启发 |
| --- | --- | --- | --- | --- |
| `one-api` | `SetupCommonRequestHeader` 默认只带 `Content-Type`、`Accept`，再按 provider 写认证头 | Provider adaptor 负责 URL、model、响应格式兼容 | 有 OpenAI 格式响应处理和 usage 估算 | 简单稳定，但不适合“全量透传”诉求 |
| `new-api` | 默认最小头；`HeaderOverride` 支持 `*` / regex passthrough，但跳过 hop-by-hop、host、content-length、accept-encoding、cookie、认证头等 | OpenAI adaptor 会针对 OpenRouter、o 系列、GPT-5、音频、图片等做参数转换 | 流式保活、WebSocket、provider adaptor、错误包装 | 最适合作为 `compat + 显式头透传规则` 的参考 |
| `LiteLLM` | 先 `clean_headers`，默认不转发 provider auth；可配置 `forward_llm_provider_auth_headers`，只显式转发 traceparent 等少数头 | 会注入 metadata、team / user 信息，清洗不可信 tags 与路由控制字段 | Router、budget、guardrails、fallback、OpenAI-compatible 响应头 | 说明大型网关默认更重视安全清洗，不是 raw proxy |
| `Portkey` | 常规 provider 请求使用 provider 映射头；`forwardHeaders` 可显式带客户端头；`proxy` endpoint 会复制大部分请求头并跳过少数 ignore 头 | `transformToProviderRequest` 负责 provider body 转换；strict OpenAI compliance 可控制响应兼容程度 | retry、fallback、cache、hooks、stream handler | `forwardHeaders` 和 `proxy` 分层值得借鉴 |
| `Proxify` | `ProxyHandler` 直接遍历复制 `c.Request.Header` 到上游请求 | 默认 body 原样流向上游；可选 route-level `model` rewrite | 响应头复制，SSE / chunked 直接 copy，可选 stream smoothing | `strict raw proxy` 的最佳参考，但缺账号调度和多租户隔离 |
| `RelayPlane` | 常规路径根据 provider 重新写认证头；对 Claude Max 场景强调 `user-agent` / `x-app` passthrough | 会读取 body 做模型、成本、策略、agent fingerprint | 预算、降级、cascade、cooldown、成本账本 | 可借鉴成本与预算，不适合作为纯透传网关 |
| `Helicone` | 区分 direct provider proxy 与统一 `/ai/*` router | 统一路由会做模型 / provider 选择和日志 | 负载均衡、重试、日志上报 | 值得借鉴“直通入口”和“调度入口”分离 |
| `agentgateway` | 策略可 set/add/remove header，也支持 backend auth / passthrough auth 概念 | CEL 可做 body transformation、RBAC、ext auth | 动态配置、xDS、rate limit、MCP / LLM 策略 | 适合未来企业级策略层，不宜第一阶段重搬 |
| `Envoy AI Gateway` | 以网关资源和 ExtProc 控制路由 / headers | 更偏控制面和企业网关资源 | quota-aware routing、priority fallback、全局限流 | 未来多区域 / 多供应商容量治理参考 |

## 推荐技术方式

不要把“透传”做成一个单一布尔开关，应拆成不同语义的模式。

### 1. `compat` 模式：默认推荐

适用：OpenAI OAuth、Codex internal、需要账号调度和错误切换的普通中转。

- Header：使用供应商级白名单，默认不转发 `X-Forwarded-For`、`X-Real-IP`、`Forwarded`、`Via`、`Host`、`Content-Length`、`Accept-Encoding`、`Cookie`、hop-by-hop headers。
- Auth：客户端本地 API Key 只用于本地鉴权；上游 `Authorization` / `x-api-key` 必须由选中账号重写。
- Session：OAuth / Codex 账号默认做 `session_id` / `conversation_id` 隔离。
- Body：只允许“明确平台兼容所必需”的修正，并记录原因；不要无条件补默认 prompt / instructions。
- Stream：透传 SSE 数据为主；只有在需要 usage / 错误策略时做轻量旁路解析。
- Failover：只在上游还没向客户端输出前切换账号；一旦流式输出开始，不再切换，避免客户端收到拼接流。

### 2. `strict` 模式：显式开启

适用：用户明确要求尽可能原样转发，且账号 / 分组风险可控。

- Method、path、query、body 默认原样。
- 请求头复制“几乎全部”，但仍必须跳过协议不安全或本地网关内部头：`connection`、`keep-alive`、`proxy-authenticate`、`proxy-authorization`、`te`、`trailer`、`transfer-encoding`、`upgrade`、`host`、`content-length`、`accept-encoding`、`via`、`x-juhe-*` 等。
- 如果本地仍使用 `Authorization: Bearer <juhe-key>` 鉴权，则不能把客户端 `Authorization` 原样发给上游；必须重写为选中账号凭据。
- 严格禁止 body 注入、model 自动改写、默认 instructions 填充和响应结构重写。
- 响应头默认按上游返回复制给客户端，但可删除 `content-length`，因为服务端可能采用流式 pipe。
- 错误处理只做记录，不在已经发送响应的情况下改写错误体。

### 3. `direct-proxy` 模式：单独入口，不参与账号调度

适用：客户端自己持有上游 API Key，只想让 `juhe-ai` 做通用反向代理。

- 本地鉴权不能占用 `Authorization`；建议使用 `X-Juhe-Key`、mTLS、Cookie 或仅内网访问。
- 客户端的上游 `Authorization` / `x-api-key` 可以原样转发。
- 不做账号池选择、模型映射、失败切换和 body 解析；最多做访问控制、速率限制和异步日志。
- 这个模式最接近 `proxify`，但产品语义应与账号中转完全分开。

## 关于客户端 IP

- 上游 TCP 层看到的是 `juhe-ai` 服务端或其出站代理的 IP，不是客户端 IP。
- `X-Forwarded-For` / `X-Real-IP` 只能声明客户端 IP，不能改变真实网络来源；很多上游不会信任，甚至会把它当作代理链路指纹。
- 默认不建议转发或伪造客户端 IP 相关头。
- 如果未来有合规场景需要让上游知道客户身份，应通过明确的客户标识、独立上游账号、合同允许的代理头，或每客户绑定独立出站代理 / egress IP 来实现，而不是把“伪装 IP”作为网关能力。

## `juhe-ai` 落地建议

### 字段语义

- `passthrough_mode`: `off | compat | strict | direct_proxy`。
- `header_policy`: `compat_whitelist | strict_skiplist | explicit_forward_list`。
- `body_policy`: `no_modify | compat_patch | provider_transform`。
- `session_policy`: `preserve | isolate_by_api_key | isolate_by_account`。
- `failover_policy`: `before_first_byte_only | disabled`。

### 默认值

- OpenAI API Key 账号：默认 `compat`，认证重写，body 默认不注入。
- OpenAI OAuth 账号：默认 `compat`，启用 Codex / OpenAI 必需头、会话隔离和必要 body normalize。
- `strict`：默认关闭，只能账号级显式开启，并在 UI 文案提示会降低错误兼容和账号保护能力。
- `direct_proxy`：默认关闭，单独路由入口，不能与分组账号调度混用。

### 后端实现重点

- 使用 Node 原生流或 `undici` 做请求 / 响应 pipe，避免把大响应完整读入内存。
- Header 处理先计算“客户端头快照”，再按模式生成“上游头”；不要边读边改导致不可审计。
- 本地鉴权头、网关内部追踪头、代理链路头默认不得进入上游。
- 严格区分“写给上游的头”和“写给客户端的网关观测头”；`x-juhe-*` 只能返回给客户端或写日志。
- usage 统计优先用上游响应；strict / direct proxy 下如果无法安全解析，就只记录请求耗时、状态码、账号命中和字节数。

## 最终判断

`juhe-ai` 不应该追求一个“服务端完全不做任何事”的统一透传开关。最优解是：

1. 默认做 `compat`，借鉴 `sub2api_source` 的 OpenAI OAuth / Codex 兼容能力和 `new-api` 的安全 header passthrough 规则。
2. 增加 `strict`，借鉴 `proxify` 和 `Portkey proxy endpoint` 的原样反代思路，但保留必要的协议 skiplist 和本地认证边界。
3. 如果确实需要客户端上游凭据全量透传，单独做 `direct-proxy`，不要混入账号池调度。
4. 不默认转发客户端 IP 头，不承诺“让上游以为 TCP 源 IP 是客户端”。

